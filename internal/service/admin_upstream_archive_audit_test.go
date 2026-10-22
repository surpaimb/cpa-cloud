package service

// Independently authored from the upstream archive audit contract. Temporary
// SQLite faults and request-owned post-real-Commit returns are distinguished.
// No App.Open, worker, provider, live DB replacement, or archived product source.
import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const auaTarget = "aua-target"

var auaIDs = []string{auaTarget, "aua-peer", "aua-control"}
var auaFactTables = []string{"upstreams", "codex_oauth_bindings", "codex_oauth_refresh_states", "upstream_proxy_bindings", "account_recovery_states", "scheduled_test_plans", "channel_monitor_plans", "scheduled_test_runs", "channel_monitor_runs", "models", "model_account_pool_configs", "model_account_pool_routes", "account_channels", "outbound_proxies", "account_pool_runtime_cooldowns", "admins", "sessions", "employees"}

type auaFacts map[string]map[int64]map[string]string
type auaAudit struct{ ID, Actor, Action, Type, Target, Result, Time string }
type auaReply struct {
	Status int
	Body   []byte
	Header http.Header
}
type auaAccess struct {
	Cookie               *http.Cookie
	CSRF, Origin, Bearer string
}
type auaObserver struct {
	ctx    context.Context
	cancel context.CancelFunc
	calls  atomic.Int64
}
type auaSignals struct {
	Epoch           uint64
	ChangeCancelled bool
	Catalog         [3]bool
	Calls           [6]int64
	Cancelled       [6]bool
	Wake            [2]int
}
type auaFixture struct {
	dir         string
	app         *App
	db          *sql.DB
	server      *httptest.Server
	client      *http.Client
	actor, csrf string
	cookie      *http.Cookie
	adapter     *auaConnector
	observers   [6]*auaObserver
	once        sync.Once
	ownerCloses int
}

func auaExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatal("AUA_FIXTURE_SQL")
	}
}
func auaCanonical(v any) string {
	switch x := v.(type) {
	case nil:
		return "n"
	case int64:
		return "i:" + strconv.FormatInt(x, 10)
	case float64:
		return "f:" + strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return "s:" + x
	case []byte:
		return "b:" + hex.EncodeToString(x)
	default:
		panic("AUA_UNEXPECTED_SQL_TYPE")
	}
}
func auaCloseRows(t *testing.T, rows *sql.Rows) {
	t.Helper()
	if rows.Close() != nil {
		t.Error("AUA_ROWS_CLOSE_ERROR")
	}
}
func auaReadFacts(t *testing.T, db *sql.DB) auaFacts {
	t.Helper()
	result := make(auaFacts)
	for _, table := range auaFactTables {
		rows, err := db.QueryContext(t.Context(), "SELECT rowid,* FROM "+table+" ORDER BY rowid")
		if err != nil {
			t.Fatal("AUA_FACT_QUERY")
		}
		columns, err := rows.Columns()
		if err != nil {
			auaCloseRows(t, rows)
			t.Fatal("AUA_FACT_COLUMNS")
		}
		set := make(map[int64]map[string]string)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if rows.Scan(pointers...) != nil {
				auaCloseRows(t, rows)
				t.Fatal("AUA_FACT_SCAN")
			}
			rowID, ok := values[0].(int64)
			if !ok {
				auaCloseRows(t, rows)
				t.Fatal("AUA_FACT_ROWID")
			}
			row := make(map[string]string)
			for i := 1; i < len(values); i++ {
				row[columns[i]] = auaCanonical(values[i])
			}
			set[rowID] = row
		}
		iteration, closeErr := rows.Err(), rows.Close()
		if iteration != nil || closeErr != nil {
			t.Fatal("AUA_FACT_ROWS_CLOSE")
		}
		result[table] = set
	}
	return result
}
func auaReadAudits(t *testing.T, db *sql.DB) map[string]auaAudit {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id,actor_id,action,target_type,target_id,result,occurred_at FROM account_lifecycle_audit ORDER BY id")
	if err != nil {
		t.Fatal("AUA_AUDIT_QUERY")
	}
	result := map[string]auaAudit{}
	for rows.Next() {
		var a auaAudit
		if rows.Scan(&a.ID, &a.Actor, &a.Action, &a.Type, &a.Target, &a.Result, &a.Time) != nil {
			auaCloseRows(t, rows)
			t.Fatal("AUA_AUDIT_SCAN")
		}
		result[a.ID] = a
	}
	iteration, closeErr := rows.Err(), rows.Close()
	if iteration != nil || closeErr != nil {
		t.Fatal("AUA_AUDIT_ROWS_CLOSE")
	}
	return result
}
func auaCopyFacts(in auaFacts) auaFacts {
	out := make(auaFacts)
	for table, set := range in {
		out[table] = make(map[int64]map[string]string)
		for id, row := range set {
			out[table][id] = make(map[string]string)
			for k, v := range row {
				out[table][id][k] = v
			}
		}
	}
	return out
}
func auaExpectedArchive(t *testing.T, before auaFacts, id, actor, stamp string) auaFacts {
	t.Helper()
	expected := auaCopyFacts(before)
	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal("AUA_ARCHIVE_TIME")
	}
	found := false
	for _, row := range expected["upstreams"] {
		if row["id"] == "s:"+id {
			found = true
			revision, _ := strconv.ParseInt(strings.TrimPrefix(row["revision"], "i:"), 10, 64)
			row["enabled"] = "i:0"
			row["archived"] = "i:1"
			row["archived_at"] = "s:" + stamp
			row["revision"] = "i:" + strconv.FormatInt(revision+1, 10)
			row["credential_ciphertext"] = "b:"
			row["key_version"] = "i:0"
			row["verified_at"] = "n"
			row["credential_state"] = "n"
			if row["provider_kind"] == "s:"+codexMembershipProvider {
				row["credential_state"] = "s:" + codexStateReauth
			}
		}
	}
	if !found {
		t.Fatal("AUA_ARCHIVE_TARGET_MISSING")
	}
	for _, table := range []string{"codex_oauth_bindings", "codex_oauth_refresh_states", "upstream_proxy_bindings", "account_recovery_states"} {
		key := "upstream_id"
		if table == "account_recovery_states" {
			key = "account_id"
		}
		for rid, row := range expected[table] {
			if row[key] == "s:"+id {
				delete(expected[table], rid)
			}
		}
	}
	for _, table := range []string{"scheduled_test_plans", "channel_monitor_plans"} {
		for _, row := range expected[table] {
			if row["upstream_id"] == "s:"+id && row["archived_at"] == "n" {
				revision, _ := strconv.ParseInt(strings.TrimPrefix(row["revision"], "i:"), 10, 64)
				row["enabled"] = "i:0"
				row["revision"] = "i:" + strconv.FormatInt(revision+1, 10)
				row["next_run_at"] = "n"
				row["updated_by_admin_id"] = "s:" + actor
				row["updated_at"] = "s:" + formatAccountPoolTime(when.UTC())
			}
		}
	}
	return expected
}
func newAUAFixture(t *testing.T, codex, enabled bool, mode string) *auaFixture {
	t.Helper()
	f := &auaFixture{dir: t.TempDir()}
	t.Cleanup(func() { f.close(t) })
	if Initialize(t.Context(), f.dir, strings.NewReader("aua-synthetic-admin-password\n")) != nil {
		t.Fatal("AUA_INITIALIZE")
	}
	s, err := openStore(f.dir)
	if err != nil {
		t.Fatal("AUA_OPEN_STORE")
	}
	f.db = s.db
	// Until publication the bootstrap owner is solely responsible for Close.
	f.app = &App{store: s}
	cfg := Config{DataDir: f.dir, Listen: "127.0.0.1:0", Version: "upstream-archive-audit", ExperimentalCodexMembership: true, ScheduledTestsEnabled: true, ChannelMonitorsEnabled: true}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("AUA_SECRETS")
	}
	if newOutboundProxyStore(s.db, sec).Migrate(t.Context()) != nil || migrateScheduledTests(t.Context(), s.db, time.Now().UTC()) != nil || migrateChannelMonitors(t.Context(), s.db, time.Now().UTC()) != nil {
		t.Fatal("AUA_REAL_MIGRATIONS")
	}
	if s.db.QueryRowContext(t.Context(), "SELECT id FROM admins WHERE username='admin'").Scan(&f.actor) != nil {
		t.Fatal("AUA_ACTOR")
	}
	stamp := "2001-02-03T04:05:06Z"
	auaExec(t, s.db, "INSERT INTO admins(id,username,password_hash,created_at) SELECT 'aua-other-admin','other',password_hash,created_at FROM admins WHERE id=?", f.actor)
	auaExec(t, s.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('aua-employee','Synthetic','active','all',1,?)", stamp)
	for index, id := range auaIDs {
		provider := codexMembershipProvider
		if index == 0 && !codex {
			provider = "openai-compatible"
		}
		var state, verified, operation, client any
		source := "api_key"
		cipher, cipherErr := sec.encryptCredential(id, "aua-synthetic-credential")
		if provider == codexMembershipProvider {
			state = codexStateVerified
			verified = stamp
			operation = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
			client = "aua-synthetic-client"
			source = "authorization_code"
			cipher, cipherErr = sec.encryptCodexAuth(id, []byte(`{"access_token":"aua-synthetic","account_id":"aua-public-fixture"}`))
		}
		if cipherErr != nil || len(cipher) == 0 {
			t.Fatal("AUA_ENCRYPTED_FIXTURE")
		}
		upEnabled := 1
		if index == 0 && !enabled {
			upEnabled = 0
		}
		auaExec(t, s.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id) VALUES(?,? ,?,'https://example.invalid',?,?,1,7,?,?,?,?)", id, "Synthetic "+id, provider, upEnabled, cipher, stamp, state, verified, operation)
		model := id + "-model"
		channel := id + "-channel"
		auaExec(t, s.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,revision,archived,archived_at,created_at) VALUES(?,?,'synthetic-model',0,2,1,?,?)", model, id, stamp, stamp)
		auaExec(t, s.db, "INSERT INTO account_channels(id,name,revision,created_at) VALUES(?,?,1,?)", channel, channel, stamp)
		auaExec(t, s.db, "INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)", model, stamp)
		auaExec(t, s.db, "INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,channel_id,position) VALUES(?,?,'synthetic-model',0,1,1,?,0)", model, id, channel)
		if provider == codexMembershipProvider {
			auaExec(t, s.db, "INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES(?,?,'authorization_code',?)", id, client, stamp)
			auaExec(t, s.db, "INSERT INTO codex_oauth_refresh_states(upstream_id,state,attempt_revision,updated_at) VALUES(?,'ready',7,?)", id, stamp)
		}
		proxy := id + "-proxy"
		auaExec(t, s.db, "INSERT INTO outbound_proxies(id,name,scheme,host,port,address_scope,enabled,revision,connection_revision,operation_id,input_fingerprint,created_at,updated_at) VALUES(?,?,'https','example.invalid',443,'public',1,1,1,?,zeroblob(32),?,?)", proxy, proxy, id+"-proxy-op", stamp, stamp)
		auaExec(t, s.db, "INSERT INTO upstream_proxy_bindings(upstream_id,proxy_id,proxy_connection_revision,created_at,updated_at) VALUES(?,?,1,?,?)", id, proxy, stamp, stamp)
		auaExec(t, s.db, "INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,'transient',?,?)", id, id+"-event", stamp, stamp)
		auaExec(t, s.db, "INSERT INTO account_recovery_states(account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,next_probe_at,created_at,updated_at) VALUES(?,?,?,1,1,7,?,?,?,?,?,'openai-chat','required',?,?,?)", id, id+"-event", id+"-recovery-op", provider, source, client, model, "synthetic-model", stamp, stamp, stamp)
		for _, kind := range []string{"enabled", "disabled", "archived"} {
			plan := id + "-" + kind
			planEnabled := 0
			var next, archived any
			if kind == "enabled" {
				planEnabled = 1
				next = "2090-01-01T00:00:00Z"
			}
			if kind == "archived" {
				archived = stamp
			}
			auaExec(t, s.db, "INSERT INTO scheduled_test_plans(id,name,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,created_by_admin_id,updated_by_admin_id,created_at,updated_at,archived_at) VALUES(?,?,?,'local_credential',300,?,3,?,'aua-other-admin','aua-other-admin',?,?,?)", plan, plan, id, planEnabled, next, stamp, stamp, archived)
			auaExec(t, s.db, "INSERT INTO channel_monitor_plans(id,name,channel_id,model_id,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,channel_revision,model_revision,pool_revision,upstream_revision,route_upstream_model,route_wire_protocol,route_position,created_by_admin_id,updated_by_admin_id,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,'local_credential',300,?,3,?,1,2,1,7,'synthetic-model','legacy-native',0,'aua-other-admin','aua-other-admin',?,?,?)", plan, plan, channel, model, id, planEnabled, next, stamp, stamp, archived)
			auaExec(t, s.db, "INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,3,?,?,'local_credential','completed','local_credential_ok',?,?,0,'system')", plan, id, plan+"-scheduled-run", stamp, stamp)
			auaExec(t, s.db, "INSERT INTO channel_monitor_runs(plan_id,plan_revision,channel_id,channel_revision,model_id,model_revision,pool_revision,upstream_id,upstream_revision,route_upstream_model,route_wire_protocol,route_position,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,3,?,1,?,2,1,?,7,'synthetic-model','legacy-native',0,?,'local_credential','completed','local_credential_ok',?,?,0,'system')", plan, channel, model, id, plan+"-channel-run", stamp, stamp)
		}
	}
	var fk int
	rows, err := s.db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal("AUA_FIXTURE_FK_QUERY")
	}
	if rows.Next() {
		fk++
	}
	iteration, closeErr := rows.Err(), rows.Close()
	if fk != 0 || iteration != nil || closeErr != nil {
		t.Fatal("AUA_FIXTURE_FK")
	}
	base := s.db.Driver()
	if s.close() != nil {
		t.Fatal("AUA_BOOTSTRAP_CLOSE")
	}
	f.db = nil
	f.app = nil
	f.adapter = &auaConnector{base: base, dsn: filepath.Join(f.dir, "cpa-cloud.db"), mode: mode}
	f.db = sql.OpenDB(f.adapter)
	f.db.SetMaxOpenConns(1)
	f.db.SetMaxIdleConns(1)
	if f.db.PingContext(t.Context()) != nil {
		_ = f.db.Close()
		t.Fatal("AUA_FIXED_DB_OPEN")
	}
	app := &App{cfg: cfg, store: &store{db: f.db}, secrets: sec, logins: make(map[string]*loginAttempt), catalogs: make(map[string]codexCatalogCacheEntry)}
	app.refresh = newCodexRefreshCoordinator(app)
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	app.accountPool = &accountPoolRuntime{app: app, ctx: runtimeCtx, cancel: runtimeCancel, changeContext: changeCtx, changeCancel: changeCancel, active: make(map[*accountPoolLease]struct{}), maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	scheduledCtx, scheduledCancel := context.WithCancel(context.Background())
	channelCtx, channelCancel := context.WithCancel(context.Background())
	app.scheduledTests = &scheduledTestCoordinator{app: app, ctx: scheduledCtx, cancel: scheduledCancel, done: make(chan struct{}), wake: make(chan struct{}, 1), active: make(map[string]scheduledTestActive)}
	app.channelMonitors = &channelMonitorCoordinator{app: app, ctx: channelCtx, cancel: channelCancel, done: make(chan struct{}), wake: make(chan struct{}, 1), active: make(map[string]channelMonitorActive)}
	for i, id := range auaIDs {
		app.catalogs[id] = codexCatalogCacheEntry{}
		for j, parent := range []context.Context{scheduledCtx, channelCtx} {
			o := &auaObserver{}
			o.ctx, o.cancel = context.WithCancel(parent)
			f.observers[i*2+j] = o
			cancel := func() { o.calls.Add(1); o.cancel() }
			if j == 0 {
				app.scheduledTests.active[id] = scheduledTestActive{upstreamID: id, revision: 3, cancel: cancel}
			} else {
				app.channelMonitors.active[id] = channelMonitorActive{upstreamID: id, modelID: id + "-model", revision: 3, cancel: cancel}
			}
		}
	}
	// This is the production SQL function composition, not a simulated hook.
	app.archiveUpstreamTxHook = func(ctx context.Context, tx *sql.Tx, id, actor, stamp string) error {
		when, err := parseTime(stamp)
		if err != nil {
			return err
		}
		if _, err = archiveScheduledTestsForUpstreamTx(ctx, tx, id, actor, when); err != nil {
			return err
		}
		return archiveChannelMonitorsForUpstreamTx(ctx, tx, id, actor, when)
	}
	app.upstreamArchivedHook = func(id string) { app.scheduledTests.cancelUpstream(id); app.channelMonitors.cancelUpstream(id) }
	f.app = app
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	mux.HandleFunc("DELETE /admin/api/v1/upstreams/{id}", app.requireAdmin(func(w http.ResponseWriter, r *http.Request, s adminSession) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		ctx = context.WithValue(ctx, auaRequestKey{}, &auaRequest{owner: f.adapter, target: r.PathValue("id"), cancel: cancel})
		app.archiveUpstream(w, r.WithContext(ctx), s)
	}, true))
	f.server = httptest.NewServer(requestMiddleware(mux))
	f.client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := f.call(t, context.Background(), "POST", "/admin/api/v1/sessions", `{"username":"admin","password":"aua-synthetic-admin-password"}`, auaAccess{Origin: f.server.URL})
	var auth struct {
		CSRF string `json:"csrf_token"`
	}
	if login.Status != 200 || json.Unmarshal(login.Body, &auth) != nil || auth.CSRF == "" {
		t.Fatal("AUA_REAL_LOGIN")
	}
	f.csrf = auth.CSRF
	for _, c := range (&http.Response{Header: login.Header}).Cookies() {
		if c.Name == adminCookieName {
			f.cookie = c
		}
	}
	if f.cookie == nil || app.refresh == nil {
		t.Fatal("AUA_REAL_AUTH_LOCK")
	}
	before := auaReadFacts(t, f.db)
	audits := auaReadAudits(t, f.db)
	signals := f.signals()
	_, prior := app.accountPool.changeSnapshot()
	control := f.archive(t, "aua-control", `{"expected_revision":7}`)
	f.requireSuccess(t, "aua-control", before, audits, signals, prior, control)
	if f.adapter.begins.Load() != 0 || f.adapter.commits.Load() != 0 {
		t.Fatal("AUA_CONTROL_ADAPTER_OWNERSHIP")
	}
	<-app.scheduledTests.wake
	<-app.channelMonitors.wake
	return f
}
func (f *auaFixture) close(t *testing.T) {
	t.Helper()
	f.once.Do(func() {
		if f.server != nil {
			f.server.Close()
		}
		if f.client != nil {
			f.client.CloseIdleConnections()
		}
		if f.app != nil {
			f.ownerCloses++
			if f.app.Close() != nil {
				t.Error("AUA_OWNER_CLOSE")
			}
		}
		if f.db != nil && f.db.PingContext(context.Background()) == nil {
			t.Error("AUA_OWNER_STILL_OPEN")
		}
		if f.ownerCloses != 1 {
			t.Error("AUA_OWNER_COUNT")
		}
	})
}
func (f *auaFixture) access() auaAccess {
	return auaAccess{Cookie: f.cookie, CSRF: f.csrf, Origin: f.server.URL}
}
func (f *auaFixture) call(t *testing.T, ctx context.Context, method, path, body string, access auaAccess) auaReply {
	t.Helper()
	reply, err := f.request(ctx, method, path, body, access)
	if err != nil {
		t.Fatal("AUA_HTTP_CALL_FAILED")
	}
	return reply
}

// This helper has no testing.T or FailNow boundary and is safe for a worker.
// Callers report only fixed labels, never the wrapped transport/error text.
func (f *auaFixture) request(ctx context.Context, method, path, body string, access auaAccess) (auaReply, error) {
	r, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		return auaReply{}, errors.Join(errors.New("AUA_HTTP_REQUEST"), err)
	}
	r.Header.Set("Content-Type", "application/json")
	if access.Cookie != nil {
		r.AddCookie(access.Cookie)
	}
	if access.CSRF != "" {
		r.Header.Set("X-CSRF-Token", access.CSRF)
	}
	if access.Origin != "" {
		r.Header.Set("Origin", access.Origin)
	}
	if access.Bearer != "" {
		r.Header.Set("Authorization", "Bearer "+access.Bearer)
	}
	response, err := f.client.Do(r)
	if err != nil {
		return auaReply{}, errors.Join(errors.New("AUA_HTTP_TRANSPORT"), err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > 16384 {
		return auaReply{}, errors.Join(errors.New("AUA_HTTP_BODY"), readErr, closeErr)
	}
	return auaReply{response.StatusCode, raw, response.Header}, nil
}
func (f *auaFixture) archive(t *testing.T, id, body string) auaReply {
	return f.call(t, context.Background(), "DELETE", "/admin/api/v1/upstreams/"+id, body, f.access())
}
func auaStatus(t *testing.T, r auaReply, status int, code string) {
	t.Helper()
	if r.Status != status {
		t.Fatal("AUA_HTTP_STATUS")
	}
	if code != "" {
		var v struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(r.Body, &v) != nil || v.Error.Code != code {
			t.Fatal("AUA_FIXED_ERROR_CODE")
		}
	}
}
func (f *auaFixture) signals() auaSignals {
	epoch, ctx := f.app.accountPool.changeSnapshot()
	s := auaSignals{Epoch: epoch, ChangeCancelled: ctx.Err() != nil}
	f.app.catalogMu.Lock()
	for i, id := range auaIDs {
		_, s.Catalog[i] = f.app.catalogs[id]
	}
	f.app.catalogMu.Unlock()
	for i, o := range f.observers {
		s.Calls[i] = o.calls.Load()
		s.Cancelled[i] = o.ctx.Err() != nil
	}
	s.Wake = [2]int{len(f.app.scheduledTests.wake), len(f.app.channelMonitors.wake)}
	return s
}
func (f *auaFixture) requireSignals(t *testing.T, id string, before auaSignals, prior context.Context) {
	t.Helper()
	after := f.signals()
	expected := before
	expected.Epoch++
	for i, key := range auaIDs {
		if key == id {
			expected.Catalog[i] = false
			for j := 0; j < 2; j++ {
				expected.Calls[2*i+j]++
				expected.Cancelled[2*i+j] = true
			}
		}
	}
	expected.Wake = [2]int{1, 1}
	if after != expected || prior.Err() != context.Canceled {
		t.Fatal("AUA_SUCCESS_SIGNALS")
	}
}
func (f *auaFixture) requireSuccess(t *testing.T, id string, before auaFacts, audits map[string]auaAudit, signals auaSignals, prior context.Context, r auaReply) auaAudit {
	t.Helper()
	auaStatus(t, r, 200, "")
	var view upstreamView
	if json.Unmarshal(r.Body, &view) != nil || view.ID != id || !view.Archived || view.Enabled || view.Revision != 8 || view.ArchiveResult != "archived" || view.ArchivedAt == nil || view.VerifiedAt != nil || view.OAuthRefresh != nil {
		t.Fatal("AUA_ARCHIVE_RESPONSE")
	}
	if !reflect.DeepEqual(auaExpectedArchive(t, before, id, f.actor, *view.ArchivedAt), auaReadFacts(t, f.db)) {
		t.Fatal("AUA_ARCHIVE_COMPLETE_FACTS")
	}
	f.requireSignals(t, id, signals, prior)
	var added auaAudit
	count := 0
	for key, a := range auaReadAudits(t, f.db) {
		if _, old := audits[key]; !old && a.Target == id {
			added = a
			count++
		}
	}
	if count != 1 || added.ID == "" || added.Actor != f.actor || added.Action != "upstream.archive" || added.Type != "upstream" || added.Result != "succeeded" {
		t.Fatal("AUA_EXACT_AUDIT")
	}
	if _, err := time.Parse(time.RFC3339Nano, added.Time); err != nil {
		t.Fatal("AUA_AUDIT_TIME")
	}
	return added
}
func (f *auaFixture) arm() {
	before := f.signals()
	f.adapter.arm(func() bool { return f.signals() == before })
}

// mode=ro is interpreted by the pinned modernc driver / SQLite URI open, not
// simulated by query_only. An absent filename cannot be created by this mode.
func auaOpenReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

func auaPrimaryCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code() & 255
	}
	return -1
}

func auaReopen(t *testing.T, dir string) (auaFacts, map[string]auaAudit) {
	t.Helper()
	db, err := auaOpenReadOnly(filepath.Join(dir, "cpa-cloud.db"))
	if err != nil {
		t.Fatal("AUA_REOPEN")
	}
	defer func() {
		if db.Close() != nil {
			t.Error("AUA_REOPEN_DB_CLOSE")
		}
	}()
	// Explicitly remove any logical query_only barrier before the write probe.
	// SQLITE_READONLY then witnesses the real connection's open-mode boundary.
	if _, err := db.ExecContext(t.Context(), "PRAGMA query_only=OFF"); err != nil {
		t.Fatal("AUA_REOPEN_QUERY_ONLY_OFF")
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE aua_read_only_probe(id INTEGER)"); auaPrimaryCode(err) != 8 {
		t.Fatal("AUA_REOPEN_NATIVE_WRITE_NOT_REJECTED")
	}
	facts, audits := auaReadFacts(t, db), auaReadAudits(t, db)
	return facts, audits
}
func (f *auaFixture) seedAudits(t *testing.T, count int, future bool) {
	t.Helper()
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("AUA_SEED_TX")
	}
	defer tx.Rollback()
	stamp := "2000-01-01T00:00:00Z"
	if future {
		stamp = "2099-01-01T00:00:00Z"
	}
	for i := 0; i < count; i++ {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'upstream.archive','upstream',?,'succeeded',?)", fmt.Sprintf("aua-seed-%04d", i), f.actor, auaTarget, stamp); err != nil {
			t.Fatal("AUA_SEED_AUDIT")
		}
	}
	if tx.Commit() != nil {
		t.Fatal("AUA_SEED_COMMIT")
	}
}

func TestAdminUpstreamArchiveAuditPositiveControl(t *testing.T) {
	for _, codex := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			name := fmt.Sprintf("codex_%t_enabled_%t", codex, enabled)
			t.Run(name, func(t *testing.T) {
				f := newAUAFixture(t, codex, enabled, "")
				before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
				_, prior := f.app.accountPool.changeSnapshot()
				f.arm()
				r := f.archive(t, auaTarget, `{"expected_revision":7}`)
				audit := f.requireSuccess(t, auaTarget, before, audits, signals, prior, r)
				captured := f.adapter.captured()
				if captured != audit || f.adapter.begins.Load() != 1 || f.adapter.commits.Load() != 1 || !f.adapter.realCommitted.Load() || !f.adapter.beforeClean.Load() || !f.adapter.afterClean.Load() {
					t.Fatal("AUA_COMMIT_SIGNAL_ORDER")
				}
				final := auaReadFacts(t, f.db)
				f.close(t)
				reopened, events := auaReopen(t, f.dir)
				if !reflect.DeepEqual(final, reopened) || events[audit.ID] != audit {
					t.Fatal("AUA_DURABLE_CONTROL")
				}
			})
		}
	}
}

func TestAdminUpstreamArchiveAuditRetention(t *testing.T) {
	for _, fault := range []string{"ignore", "future_1000", "insert_delete"} {
		t.Run(fault, func(t *testing.T) {
			f := newAUAFixture(t, true, true, "")
			switch fault {
			case "ignore":
				auaExec(t, f.db, `CREATE TRIGGER aua_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.action='upstream.archive' AND NEW.target_id='aua-target' BEGIN SELECT RAISE(IGNORE); END`)
			case "future_1000":
				f.seedAudits(t, 1000, true)
			case "insert_delete":
				auaExec(t, f.db, `CREATE TRIGGER aua_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='upstream.archive' AND NEW.target_id='aua-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id=NEW.id; END`)
			}
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			_, prior := f.app.accountPool.changeSnapshot()
			f.arm()
			r := f.archive(t, auaTarget, `{"expected_revision":7}`)
			captured := f.adapter.captured()
			if captured.ID == "" || captured.Actor != f.actor || captured.Action != "upstream.archive" || captured.Type != "upstream" || captured.Target != auaTarget || captured.Result != "succeeded" {
				t.Fatal("AUA_CURRENT_INSERT_ID")
			}
			if _, old := audits[captured.ID]; old {
				t.Fatal("AUA_CURRENT_ID_NOT_NEW")
			}
			if r.Status == 200 {
				var view upstreamView
				if json.Unmarshal(r.Body, &view) != nil || view.ArchiveResult != "archived" || view.ArchivedAt == nil || view.Enabled || !view.Archived || view.Revision != 8 {
					t.Fatal("AUA_OLD_RESPONSE_NOT_ARCHIVE")
				}
				expected := auaExpectedArchive(t, before, auaTarget, f.actor, *view.ArchivedAt)
				if !reflect.DeepEqual(expected, auaReadFacts(t, f.db)) {
					t.Fatal("AUA_OLD_FACT_CHAIN_INCOMPLETE")
				}
				f.requireSignals(t, auaTarget, signals, prior)
				if f.adapter.commits.Load() != 1 || !f.adapter.realCommitted.Load() || !f.adapter.beforeClean.Load() || !f.adapter.afterClean.Load() {
					t.Fatal("AUA_OLD_COMMIT_SIGNAL_CHAIN")
				}
				f.close(t)
				reopened, events := auaReopen(t, f.dir)
				if !reflect.DeepEqual(expected, reopened) {
					t.Fatal("AUA_OLD_REOPEN_FACT_CHAIN")
				}
				if _, present := events[captured.ID]; present {
					t.Fatal("AUA_OLD_CURRENT_AUDIT_NOT_MISSING")
				}
				t.Fatal("AUA_OLD_" + strings.ToUpper(fault) + "_ARCHIVED_WITHOUT_CURRENT_AUDIT")
			}
			auaStatus(t, r, 503, "storage_unavailable")
			if !reflect.DeepEqual(before, auaReadFacts(t, f.db)) || !reflect.DeepEqual(audits, auaReadAudits(t, f.db)) || signals != f.signals() || f.adapter.commits.Load() != 0 || f.adapter.realCommitted.Load() {
				t.Fatal("AUA_REJECT_UNCOMMITTED")
			}
			f.close(t)
			reopened, events := auaReopen(t, f.dir)
			if !reflect.DeepEqual(before, reopened) || !reflect.DeepEqual(audits, events) {
				t.Fatal("AUA_REJECT_DURABLE_FACTS")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditCapRetention(t *testing.T) {
	for _, count := range []int{999, 1000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			f := newAUAFixture(t, true, true, "")
			f.seedAudits(t, count, false)
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			_, prior := f.app.accountPool.changeSnapshot()
			f.arm()
			reply := f.archive(t, auaTarget, `{"expected_revision":7}`)
			added := f.requireSuccess(t, auaTarget, before, audits, signals, prior, reply)
			events := auaReadAudits(t, f.db)
			if len(events) != 1000 || events[added.ID] != f.adapter.captured() {
				t.Fatal("AUA_CAP_EXACT_CURRENT_RETAINED")
			}
		})
	}
}

type auaHTTPResult struct {
	Index int
	Reply auaReply
	Err   error
}
type auaPairResult struct {
	Replies   [2]auaHTTPResult
	Delivered int
	Exited    int64
}

// Only context-bounded HTTP work enters these workers. Each worker's defer
// delivers one result even on panic, before Done. A buffer of two prevents a
// failed request from starving the parent of a result; all assertions are parent-only.
func auaArchivePair(ctx context.Context, request func(context.Context, int) (auaReply, error)) auaPairResult {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	replies := make(chan auaHTTPResult, 2)
	var workers sync.WaitGroup
	var exited atomic.Int64
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func(index int) {
			defer func() { exited.Add(1); workers.Done() }()
			result := auaHTTPResult{Index: index}
			defer func() {
				if recover() != nil {
					result.Err = errors.New("AUA_HTTP_WORKER_PANIC")
				}
				replies <- result
			}()
			result.Reply, result.Err = request(ctx, index)
		}(i)
	}
	workers.Wait()
	close(replies)
	var pair auaPairResult
	for result := range replies {
		pair.Replies[result.Index] = result
		pair.Delivered++
	}
	pair.Exited = exited.Load()
	return pair
}

var auaControlledHTTPError = errors.New("AUA_CONTROLLED_HTTP_REQUEST_ERROR")

type auaErrorTransport struct {
	base  http.RoundTripper
	fail  int64
	calls atomic.Int64
}

func (tr *auaErrorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr.calls.Add(1) <= tr.fail {
		return nil, auaControlledHTTPError
	}
	return tr.base.RoundTrip(r)
}
func (tr *auaErrorTransport) CloseIdleConnections() {
	if closer, ok := tr.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func TestAdminUpstreamArchiveAuditFixtureWorkerErrors(t *testing.T) {
	for _, failures := range []int64{1, 2} {
		t.Run(fmt.Sprintf("errors_%d", failures), func(t *testing.T) {
			f := newAUAFixture(t, true, true, "")
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			_, prior := f.app.accountPool.changeSnapshot()
			f.arm()
			transport := &auaErrorTransport{base: f.client.Transport, fail: failures}
			f.client.Transport = transport
			started := time.Now()
			pair := auaArchivePair(t.Context(), func(ctx context.Context, _ int) (auaReply, error) {
				return f.request(ctx, "DELETE", "/admin/api/v1/upstreams/"+auaTarget, `{"expected_revision":7}`, f.access())
			})
			if pair.Delivered != 2 || pair.Exited != 2 || transport.calls.Load() != 2 {
				t.Fatal("AUA_FIXTURE_WORKER_DELIVERY_EXIT")
			}
			var errorCount int64
			for _, result := range pair.Replies {
				if result.Err != nil {
					if !errors.Is(result.Err, auaControlledHTTPError) {
						t.Fatal("AUA_FIXTURE_UNEXPECTED_HTTP_ERROR")
					}
					errorCount++
				} else {
					f.requireSuccess(t, auaTarget, before, audits, signals, prior, result.Reply)
				}
			}
			if errorCount != failures {
				t.Fatal("AUA_FIXTURE_HTTP_ERROR_COUNT")
			}
			if failures == 2 && (!reflect.DeepEqual(before, auaReadFacts(t, f.db)) || !reflect.DeepEqual(audits, auaReadAudits(t, f.db)) || signals != f.signals()) {
				t.Fatal("AUA_FIXTURE_FAILED_REQUEST_CHANGED_FACTS")
			}
			f.close(t)
			if time.Since(started) >= 10*time.Second {
				t.Fatal("AUA_FIXTURE_WORKER_CLEANUP_BOUND")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditFixtureReadOnly(t *testing.T) {
	t.Run("existing_no_write", func(t *testing.T) {
		f := newAUAFixture(t, true, true, "")
		before, audits := auaReadFacts(t, f.db), auaReadAudits(t, f.db)
		f.close(t)
		reopened, events := auaReopen(t, f.dir)
		if !reflect.DeepEqual(before, reopened) || !reflect.DeepEqual(audits, events) {
			t.Fatal("AUA_FIXTURE_READ_ONLY_FACTS")
		}
	})
	t.Run("missing_no_create", func(t *testing.T) {
		dir := t.TempDir()
		missing := filepath.Join(dir, "missing.db")
		db, err := auaOpenReadOnly(missing)
		if db != nil {
			if db.Close() != nil {
				t.Error("AUA_FIXTURE_MISSING_DB_CLOSE")
			}
			t.Fatal("AUA_FIXTURE_MISSING_DB_OPENED")
		}
		if auaPrimaryCode(err) != 14 {
			t.Fatal("AUA_FIXTURE_MISSING_NATIVE_OPEN_CODE")
		}
		if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("AUA_FIXTURE_READ_ONLY_CREATED_FILE")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("AUA_FIXTURE_READ_ONLY_CREATED_SIDECAR")
		}
	})
}

func TestAdminUpstreamArchiveAuditNoopAndConcurrency(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(fmt.Sprintf("codex_%t", codex), func(t *testing.T) {
			f := newAUAFixture(t, codex, false, "")
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			_, prior := f.app.accountPool.changeSnapshot()
			f.arm()
			pair := auaArchivePair(t.Context(), func(ctx context.Context, _ int) (auaReply, error) {
				return f.request(ctx, "DELETE", "/admin/api/v1/upstreams/"+auaTarget, `{"expected_revision":7}`, f.access())
			})
			if pair.Delivered != 2 || pair.Exited != 2 || pair.Replies[0].Err != nil || pair.Replies[1].Err != nil {
				t.Fatal("AUA_CONCURRENT_HTTP_RESULT")
			}
			first, second := pair.Replies[0].Reply, pair.Replies[1].Reply
			var a, b upstreamView
			auaStatus(t, first, 200, "")
			auaStatus(t, second, 200, "")
			if json.Unmarshal(first.Body, &a) != nil || json.Unmarshal(second.Body, &b) != nil {
				t.Fatal("AUA_CONCURRENT_JSON")
			}
			archived, noop := first, second
			if a.ArchiveResult == "already_archived" {
				archived, noop = second, first
			}
			var no upstreamView
			if json.Unmarshal(noop.Body, &no) != nil || no.ArchiveResult != "already_archived" || no.Revision != 8 {
				t.Fatal("AUA_CONCURRENT_NOOP")
			}
			f.requireSuccess(t, auaTarget, before, audits, signals, prior, archived)
			if f.adapter.commits.Load() != 1 {
				t.Fatal("AUA_CONCURRENT_SINGLE_COMMIT")
			}
			final, events, quiet := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			again := f.archive(t, auaTarget, `{"expected_revision":1}`)
			auaStatus(t, again, 200, "")
			if json.Unmarshal(again.Body, &no) != nil || no.ArchiveResult != "already_archived" || !reflect.DeepEqual(final, auaReadFacts(t, f.db)) || !reflect.DeepEqual(events, auaReadAudits(t, f.db)) || quiet != f.signals() || f.adapter.commits.Load() != 1 {
				t.Fatal("AUA_ALREADY_ARCHIVED_NO_REPAIR")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditGuards(t *testing.T) {
	for _, kind := range []string{"no_cookie", "bearer", "origin_missing", "origin_wrong", "csrf_missing", "csrf_wrong", "invalid_revision", "stale_revision", "missing", "direct_reference_disabled", "pool_reference_disabled"} {
		t.Run(kind, func(t *testing.T) {
			f := newAUAFixture(t, true, true, "")
			path := "/admin/api/v1/upstreams/" + auaTarget
			body := `{"expected_revision":7}`
			access := f.access()
			status, code := 403, "csrf_rejected"
			switch kind {
			case "no_cookie":
				access.Cookie = nil
				status, code = 401, "authentication_required"
			case "bearer":
				access.Cookie = nil
				access.Bearer = "cpac_public.synthetic"
				status, code = 401, "authentication_required"
			case "origin_missing":
				access.Origin = ""
				code = "origin_rejected"
			case "origin_wrong":
				access.Origin = "https://example.invalid"
				code = "origin_rejected"
			case "csrf_missing":
				access.CSRF = ""
			case "csrf_wrong":
				access.CSRF = "wrong"
			case "invalid_revision":
				body = `{"expected_revision":0}`
				status, code = 400, "invalid_revision"
			case "stale_revision":
				body = `{"expected_revision":6}`
				status, code = 409, "revision_conflict"
			case "missing":
				path = "/admin/api/v1/upstreams/aua-absent"
				status, code = 404, "not_found"
			case "direct_reference_disabled":
				auaExec(t, f.db, "UPDATE models SET archived=0,archived_at=NULL WHERE id=?", auaTarget+"-model")
				status, code = 409, "upstream_in_use"
			case "pool_reference_disabled":
				auaExec(t, f.db, "UPDATE models SET archived=0,archived_at=NULL WHERE id='aua-peer-model'")
				auaExec(t, f.db, "INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES('aua-peer-model',?,'synthetic-model',0,1,1,1)", auaTarget)
				status, code = 409, "upstream_in_use"
			}
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			f.arm()
			r := f.call(t, context.Background(), "DELETE", path, body, access)
			auaStatus(t, r, status, code)
			if !reflect.DeepEqual(before, auaReadFacts(t, f.db)) || !reflect.DeepEqual(audits, auaReadAudits(t, f.db)) || signals != f.signals() || f.adapter.commits.Load() != 0 {
				t.Fatal("AUA_GUARD_CHANGED_FACTS")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditLockCancellation(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(fmt.Sprintf("codex_%t", codex), func(t *testing.T) {
			f := newAUAFixture(t, codex, true, "")
			lock := f.app.refresh.accountLock(auaTarget)
			if !lock.lock(t.Context()) {
				t.Fatal("AUA_REAL_LOCK_ACQUIRE")
			}
			defer lock.unlock()
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			f.arm()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			request := httptest.NewRequest("DELETE", "http://127.0.0.1/admin/api/v1/upstreams/"+auaTarget, strings.NewReader(`{"expected_revision":7}`)).WithContext(ctx)
			request.SetPathValue("id", auaTarget)
			recorder := httptest.NewRecorder()
			f.app.archiveUpstream(recorder, request, adminSession{AdminID: f.actor})
			auaStatus(t, auaReply{recorder.Code, recorder.Body.Bytes(), recorder.Header()}, 408, "request_cancelled")
			if f.adapter.begins.Load() != 0 || !reflect.DeepEqual(before, auaReadFacts(t, f.db)) || !reflect.DeepEqual(audits, auaReadAudits(t, f.db)) || signals != f.signals() {
				t.Fatal("AUA_REAL_LOCK_CANCEL_FACTS")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditRejection(t *testing.T) {
	for _, fault := range []string{"audit_sql", "trim_sql", "upstream_reverse", "cipher_reverse", "binding_reverse", "refresh_reverse", "proxy_reverse", "recovery_reverse", "scheduled_reverse", "channel_reverse", "peer_plan_reverse", "hook_error", "commit_fk"} {
		t.Run(fault, func(t *testing.T) {
			f := newAUAFixture(t, true, true, "")
			sqlBody := ""
			switch fault {
			case "audit_sql":
				auaExec(t, f.db, `CREATE TRIGGER aua_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.target_id='aua-target' BEGIN SELECT RAISE(ABORT,'AUA_CONTROLLED_AUDIT'); END`)
			case "trim_sql":
				f.seedAudits(t, 1000, false)
				auaExec(t, f.db, `CREATE TRIGGER aua_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'AUA_CONTROLLED_TRIM'); END`)
			case "upstream_reverse":
				sqlBody = `UPDATE upstreams SET enabled=1,archived=0,archived_at=NULL WHERE id='aua-target';`
			case "cipher_reverse":
				sqlBody = `UPDATE upstreams SET credential_ciphertext=x'01' WHERE id='aua-target';`
			case "binding_reverse":
				sqlBody = `INSERT INTO codex_oauth_bindings VALUES('aua-target','aua-synthetic-client','authorization_code','2001-02-03T04:05:06Z');`
			case "refresh_reverse":
				sqlBody = `INSERT INTO codex_oauth_refresh_states VALUES('aua-target','ready',NULL,7,'2001-02-03T04:05:06Z');`
			case "proxy_reverse":
				sqlBody = `INSERT INTO upstream_proxy_bindings VALUES('aua-target','aua-target-proxy',1,'2001-02-03T04:05:06Z','2001-02-03T04:05:06Z');`
			case "recovery_reverse":
				auaExec(t, f.db, "CREATE TABLE aua_saved AS SELECT * FROM account_recovery_states WHERE account_id=?", auaTarget)
				sqlBody = `INSERT INTO account_recovery_states SELECT * FROM aua_saved;`
			case "scheduled_reverse":
				sqlBody = `UPDATE scheduled_test_plans SET revision=revision+1 WHERE upstream_id='aua-target' AND archived_at IS NULL;`
			case "channel_reverse":
				sqlBody = `UPDATE channel_monitor_plans SET updated_by_admin_id='aua-other-admin' WHERE upstream_id='aua-target' AND archived_at IS NULL;`
			case "peer_plan_reverse":
				sqlBody = `UPDATE scheduled_test_plans SET revision=revision+1 WHERE upstream_id='aua-peer';`
			case "hook_error":
				realHook := f.app.archiveUpstreamTxHook
				f.app.archiveUpstreamTxHook = func(ctx context.Context, tx *sql.Tx, id, actor, stamp string) error {
					if err := realHook(ctx, tx, id, actor, stamp); err != nil {
						return err
					}
					return errors.New("AUA_CONTROLLED_AFTER_REAL_HOOK")
				}
			case "commit_fk":
				auaExec(t, f.db, "CREATE TABLE aua_fk_parent(id INTEGER PRIMARY KEY)")
				auaExec(t, f.db, "CREATE TABLE aua_fk_child(id INTEGER REFERENCES aua_fk_parent(id) DEFERRABLE INITIALLY DEFERRED)")
				sqlBody = `INSERT INTO aua_fk_child VALUES(1);`
			}
			if sqlBody != "" {
				auaExec(t, f.db, `CREATE TRIGGER aua_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.target_id='aua-target' BEGIN `+sqlBody+` END`)
			}
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			f.arm()
			r := f.archive(t, auaTarget, `{"expected_revision":7}`)
			auaStatus(t, r, 503, "storage_unavailable")
			if f.signals() != signals || f.adapter.realCommitted.Load() {
				t.Fatal("AUA_REJECT_SIGNALS_OR_COMMIT")
			}
			if fault == "commit_fk" {
				if f.adapter.commits.Load() != 1 || f.adapter.nativeCode.Load()&255 != 19 {
					t.Fatal("AUA_NATIVE_COMMIT_REJECT")
				}
			} else if f.adapter.commits.Load() != 0 {
				t.Fatal("AUA_REJECT_REACHED_COMMIT")
			}
			f.close(t)
			reopened, events := auaReopen(t, f.dir)
			if !reflect.DeepEqual(before, reopened) || !reflect.DeepEqual(audits, events) {
				t.Fatal("AUA_REJECTION_NOT_DURABLE_ROLLBACK")
			}
		})
	}
}

func TestAdminUpstreamArchiveAuditCommitUncertain(t *testing.T) {
	for _, mode := range []string{"after_error", "after_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newAUAFixture(t, true, true, mode)
			before, audits, signals := auaReadFacts(t, f.db), auaReadAudits(t, f.db), f.signals()
			f.arm()
			r := f.archive(t, auaTarget, `{"expected_revision":7}`)
			auaStatus(t, r, 503, "storage_unavailable")
			captured := f.adapter.captured()
			if captured.ID == "" || !f.adapter.realCommitted.Load() || f.adapter.commits.Load() != 1 || !f.adapter.beforeClean.Load() || !f.adapter.afterClean.Load() || signals != f.signals() {
				t.Fatal("AUA_CONTROLLED_COMMIT_UNCERTAIN")
			}
			var stamp string
			if f.db.QueryRowContext(t.Context(), "SELECT archived_at FROM upstreams WHERE id=?", auaTarget).Scan(&stamp) != nil {
				t.Fatal("AUA_UNCERTAIN_STATE")
			}
			expected := auaExpectedArchive(t, before, auaTarget, f.actor, stamp)
			if !reflect.DeepEqual(expected, auaReadFacts(t, f.db)) || auaReadAudits(t, f.db)[captured.ID] != captured {
				t.Fatal("AUA_UNCERTAIN_NOT_REAL_COMMIT")
			}
			if _, old := audits[captured.ID]; old {
				t.Fatal("AUA_UNCERTAIN_OLD_ID")
			}
			events := auaReadAudits(t, f.db)
			again := f.archive(t, auaTarget, `{"expected_revision":7}`)
			auaStatus(t, again, 200, "")
			var view upstreamView
			if json.Unmarshal(again.Body, &view) != nil || view.ArchiveResult != "already_archived" || signals != f.signals() || f.adapter.commits.Load() != 1 || !reflect.DeepEqual(events, auaReadAudits(t, f.db)) {
				t.Fatal("AUA_UNCERTAIN_NO_REPAIR")
			}
			f.close(t)
			reopened, reopenedEvents := auaReopen(t, f.dir)
			if !reflect.DeepEqual(expected, reopened) || !reflect.DeepEqual(events, reopenedEvents) {
				t.Fatal("AUA_UNCERTAIN_REOPEN")
			}
		})
	}
}

// Every SQL call delegates to the real SQLite driver. Only an authenticated
// target request owns counters, INSERT identity capture, and controlled returns.
type auaRequestKey struct{}
type auaRequest struct {
	owner  *auaConnector
	target string
	cancel context.CancelFunc
}
type auaConnector struct {
	base                                   driver.Driver
	dsn, mode                              string
	mu                                     sync.Mutex
	audit                                  auaAudit
	check                                  func() bool
	begins, commits                        atomic.Int64
	realCommitted, beforeClean, afterClean atomic.Bool
	nativeCode                             atomic.Int64
}

func (d *auaConnector) Driver() driver.Driver { return d.base }
func (d *auaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := d.base.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	for _, q := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		s, err := c.Prepare(q)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		_, execErr := s.Exec(nil)
		closeErr := s.Close()
		if execErr != nil {
			_ = c.Close()
			return nil, execErr
		}
		if closeErr != nil {
			_ = c.Close()
			return nil, closeErr
		}
	}
	return &auaConn{Conn: c, owner: d}, nil
}
func (d *auaConnector) arm(check func() bool) {
	d.mu.Lock()
	d.audit = auaAudit{}
	d.check = check
	d.mu.Unlock()
	d.begins.Store(0)
	d.commits.Store(0)
	d.realCommitted.Store(false)
	d.beforeClean.Store(false)
	d.afterClean.Store(false)
	d.nativeCode.Store(0)
}
func (d *auaConnector) captured() auaAudit { d.mu.Lock(); defer d.mu.Unlock(); return d.audit }
func (d *auaConnector) quiet() bool {
	d.mu.Lock()
	check := d.check
	d.mu.Unlock()
	return check != nil && check()
}

type auaConn struct {
	driver.Conn
	owner *auaConnector
}

func (c *auaConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	r, _ := ctx.Value(auaRequestKey{}).(*auaRequest)
	if r == nil || r.owner != c.owner || r.target != auaTarget {
		r = nil
	} else {
		c.owner.begins.Add(1)
	}
	var tx driver.Tx
	var err error
	if x, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = x.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &auaTx{Tx: tx, owner: c.owner, request: r}, nil
}
func (c *auaConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	r, _ := ctx.Value(auaRequestKey{}).(*auaRequest)
	if r != nil && r.owner == c.owner && r.target == auaTarget && strings.HasPrefix(strings.TrimSpace(q), "INSERT INTO account_lifecycle_audit(") && len(args) == 7 {
		values := [7]string{}
		valid := true
		for i, a := range args {
			var ok bool
			values[i], ok = a.Value.(string)
			valid = valid && ok
		}
		if valid {
			c.owner.mu.Lock()
			c.owner.audit = auaAudit{values[0], values[1], values[2], values[3], values[4], values[5], values[6]}
			c.owner.mu.Unlock()
		}
	}
	if x, ok := c.Conn.(driver.ExecerContext); ok {
		return x.ExecContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}
func (c *auaConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if x, ok := c.Conn.(driver.QueryerContext); ok {
		return x.QueryContext(ctx, q, args)
	}
	return nil, driver.ErrSkip
}

type auaTx struct {
	driver.Tx
	owner   *auaConnector
	request *auaRequest
}

func (tx *auaTx) Commit() error {
	if tx.request == nil {
		return tx.Tx.Commit()
	}
	tx.owner.commits.Add(1)
	tx.owner.beforeClean.Store(tx.owner.quiet())
	if err := tx.Tx.Commit(); err != nil {
		var code interface{ Code() int }
		if errors.As(err, &code) {
			tx.owner.nativeCode.Store(int64(code.Code()))
		} else {
			tx.owner.nativeCode.Store(-1)
		}
		return err
	}
	tx.owner.realCommitted.Store(true)
	tx.owner.afterClean.Store(tx.owner.quiet())
	switch tx.owner.mode {
	case "after_error":
		return errors.New("AUA_CONTROLLED_AFTER_REAL_COMMIT")
	case "after_cancel":
		tx.request.cancel()
		return context.Canceled
	}
	return nil
}
