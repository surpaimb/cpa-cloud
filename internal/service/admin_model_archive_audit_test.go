package service

// Independently authored for the archive-only contract. SQLite/HTTP work is real
// and temporary; post-real-Commit errors are controlled adapter simulations, not
// real I/O faults. PR124's PATCH results are not archive acceptance evidence.
import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const amaTarget = "ama-target"

type amaModel struct {
	id, kind, upstream, model, wire, created string
	enabled, revision, archived              int64
	archivedAt                               sql.NullString
}
type amaAudit struct{ id, actor, action, targetType, target, result, stamp string }
type amaReply struct {
	status int
	body   []byte
	header http.Header
}
type amaAccess struct {
	cookie               *http.Cookie
	csrf, origin, bearer string
}
type amaMonitor struct {
	ctx    context.Context
	calls  *atomic.Int64
	cancel context.CancelFunc
}
type amaSignals struct {
	epoch                  uint64
	prior                  context.Context
	targetCalls, peerCalls int64
	targetErr              error
}
type amaObservation struct {
	model  amaModel
	audits map[string]amaAudit
	facts  map[string]string
}
type amaFixture struct {
	dir                    string
	app                    *App
	bootstrap              *store
	db                     *sql.DB
	server                 *httptest.Server
	client                 *http.Client
	cookie                 *http.Cookie
	csrf, actor, key       string
	runtimeCtx, monitorCtx context.Context
	target, peer, control  amaMonitor
	handlers               sync.WaitGroup
	closing                atomic.Bool
	releaseOnce            sync.Once
	ownerCloses            int
	adapter                *amaConnector
}

func amaExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal("AMA_FIXTURE_SQL")
	}
}

func amaObservedContext(parent context.Context) amaMonitor {
	ctx, cancel := context.WithCancel(parent)
	calls := new(atomic.Int64)
	return amaMonitor{ctx: ctx, calls: calls, cancel: func() { calls.Add(1); cancel() }}
}

func newAMAFixture(t *testing.T, enabled bool, mode string) *amaFixture {
	t.Helper()
	dir := t.TempDir()
	f := &amaFixture{dir: dir}
	t.Cleanup(func() { f.release(t) })
	if Initialize(t.Context(), dir, strings.NewReader("a-strong-preview-password\n")) != nil {
		t.Fatal("AMA_INITIALIZE")
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal("AMA_OPEN_STORE")
	}
	f.bootstrap, f.db = s, s.db
	cfg := Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "model-archive-audit-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("AMA_FIXTURE_SECRETS")
	}
	if f.db.QueryRowContext(t.Context(), "SELECT id FROM admins WHERE username='admin'").Scan(&f.actor) != nil || f.actor == "" {
		t.Fatal("AMA_FIXTURE_ACTOR")
	}
	stamp := utcNow()
	amaExec(t, f.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('ama-up','Fixture','openai-compatible','https://example.invalid',1,?,1,1,?)", []byte("synthetic-opaque"), stamp)
	for _, id := range []string{amaTarget, "ama-peer", "ama-control"} {
		state := 1
		if id == amaTarget {
			state = boolInt(enabled)
		}
		amaExec(t, f.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,'ama-up','fixture-model',?,?)", id, state, stamp)
	}
	for _, pair := range []struct{ employee, model string }{{"ama-employee", amaTarget}, {"ama-other", "ama-peer"}} {
		amaExec(t, f.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,'Fixture','active','selected',1,?)", pair.employee, stamp)
		amaExec(t, f.db, "INSERT INTO employee_models(employee_id,model_id) VALUES(?,?)", pair.employee, pair.model)
	}
	selector, secret := "ama-selector", strings.Repeat("k", 43)
	f.key = "cpac_" + selector + "." + secret
	amaExec(t, f.db, "INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('ama-key','ama-employee','Fixture',?,?,1,'ama-key-operation',?)", selector, sec.digest("employee-key/v1\x00"+selector, secret), stamp)
	// A model tombstone must preserve these references rather than require the
	// pool to be empty or delete employee / pool facts as updateModel would not.
	amaExec(t, f.db, "INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)", amaTarget, stamp)
	amaExec(t, f.db, "INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES(?,'ama-up','fixture-model',0,1,1,0)", amaTarget)
	if mode != "" {
		base := f.db.Driver()
		if s.close() != nil {
			t.Fatal("AMA_BOOTSTRAP_CLOSE")
		}
		f.bootstrap, f.db = nil, nil
		f.adapter = &amaConnector{base: base, dsn: filepath.Join(dir, "cpa-cloud.db"), mode: mode}
		f.db = sql.OpenDB(f.adapter)
		f.bootstrap = &store{db: f.db}
		f.db.SetMaxOpenConns(1)
		f.db.SetMaxIdleConns(1)
		if f.db.PingContext(t.Context()) != nil {
			t.Fatal("AMA_ADAPTER_OPEN")
		}
		s = f.bootstrap
	}
	// One immutable DB assignment, before App/runtime/server publication. No
	// App.Open/Handler or worker constructor, and no live DB replacement.
	app := &App{cfg: cfg, store: s, secrets: sec, logins: make(map[string]*loginAttempt)}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	app.accountPool = &accountPoolRuntime{app: app, ctx: runtimeCtx, cancel: runtimeCancel, changeContext: changeCtx, changeCancel: changeCancel,
		active: make(map[*accountPoolLease]struct{}), maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	f.target, f.peer, f.control = amaObservedContext(monitorCtx), amaObservedContext(monitorCtx), amaObservedContext(monitorCtx)
	done := make(chan struct{})
	close(done)
	app.channelMonitors = &channelMonitorCoordinator{app: app, ctx: monitorCtx, cancel: monitorCancel, done: done, wake: make(chan struct{}, 1),
		active: map[string]channelMonitorActive{"target": {modelID: amaTarget, cancel: f.target.cancel}, "peer": {modelID: "ama-peer", cancel: f.peer.cancel}, "control": {modelID: "ama-control", cancel: f.control.cancel}}}
	f.runtimeCtx, f.monitorCtx, f.app, f.bootstrap = runtimeCtx, monitorCtx, app, nil
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	mux.HandleFunc("DELETE /admin/api/v1/models/{id}", app.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		ctx := r.Context()
		if f.adapter != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			ctx = context.WithValue(ctx, amaRequestKey{}, &amaRequest{owner: f.adapter, target: r.PathValue("id"), cancel: cancel})
		}
		app.archiveModel(w, r.WithContext(ctx), session)
	}, true))
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.closing.Load() {
			w.WriteHeader(503)
			return
		}
		f.handlers.Add(1)
		defer f.handlers.Done()
		mux.ServeHTTP(w, r)
	})))
	f.client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := f.call(t, http.MethodPost, "/admin/api/v1/sessions", `{"username":"admin","password":"a-strong-preview-password"}`, amaAccess{origin: f.server.URL})
	var value struct {
		CSRF string `json:"csrf_token"`
	}
	if login.status != 200 || json.Unmarshal(login.body, &value) != nil || value.CSRF == "" {
		t.Fatal("AMA_LOGIN")
	}
	for _, cookie := range (&http.Response{Header: login.header}).Cookies() {
		if cookie.Name == adminCookieName {
			f.cookie = cookie
		}
	}
	f.csrf = value.CSRF
	if f.cookie == nil || f.cookie.Value == "" || app.accountPool == nil || app.channelMonitors == nil {
		t.Fatal("AMA_REAL_AUTH_RUNTIME")
	}
	// Real endpoint positive control on a separate model BEFORE any target fault.
	epoch, prior := app.accountPool.changeSnapshot()
	controlBefore := f.model(t, "ama-control")
	reply := f.archive(t, "ama-control", `{"expected_revision":1}`)
	f.requireStatus(t, reply, 200, "")
	f.requireArchived(t, controlBefore, reply, "archived")
	f.auditAdded(t, map[string]amaAudit{}, "ama-control")
	next, _ := app.accountPool.changeSnapshot()
	if next != epoch+1 || prior.Err() != context.Canceled || f.control.calls.Load() != 1 || f.control.ctx.Err() != context.Canceled || f.target.calls.Load() != 0 || f.peer.calls.Load() != 0 || f.target.ctx.Err() != nil || f.peer.ctx.Err() != nil {
		t.Fatal("AMA_CONTROL_SIGNALS")
	}
	return f
}

func (f *amaFixture) release(t *testing.T) {
	t.Helper()
	f.releaseOnce.Do(func() {
		f.closing.Store(true)
		if f.server != nil {
			f.server.Close()
		}
		f.handlers.Wait()
		if f.client != nil {
			f.client.CloseIdleConnections()
		}
		f.ownerCloses++
		if f.app != nil {
			if f.app.Close() != nil {
				t.Error("AMA_CLEANUP_APP")
			}
			if f.runtimeCtx.Err() != context.Canceled || f.monitorCtx.Err() != context.Canceled {
				t.Error("AMA_CLEANUP_CONTEXTS")
			}
		} else if f.bootstrap != nil {
			if f.bootstrap.close() != nil {
				t.Error("AMA_CLEANUP_BOOTSTRAP")
			}
		}
		if f.db != nil && f.db.PingContext(context.Background()) == nil {
			t.Error("AMA_CLEANUP_DB_OPEN")
		}
		if f.ownerCloses != 1 {
			t.Error("AMA_CLEANUP_OWNER_COUNT")
		}
	})
}

func (f *amaFixture) access() amaAccess {
	return amaAccess{cookie: f.cookie, csrf: f.csrf, origin: f.server.URL}
}

// This client helper has no testing.T and is safe for concurrent clients.
func (f *amaFixture) request(ctx context.Context, method, path, body string, access amaAccess) (amaReply, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		return amaReply{}, errors.New("AMA_HTTP_REQUEST")
	}
	req.Header.Set("Content-Type", "application/json")
	if access.cookie != nil {
		req.AddCookie(access.cookie)
	}
	if access.csrf != "" {
		req.Header.Set("X-CSRF-Token", access.csrf)
	}
	if access.origin != "" {
		req.Header.Set("Origin", access.origin)
	}
	if access.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+access.bearer)
	}
	response, err := f.client.Do(req)
	if err != nil {
		return amaReply{}, errors.New("AMA_HTTP_TRANSPORT")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return amaReply{}, errors.New("AMA_HTTP_BODY")
	}
	return amaReply{status: response.StatusCode, body: data, header: response.Header.Clone()}, nil
}

func (f *amaFixture) call(t *testing.T, method, path, body string, access amaAccess) amaReply {
	t.Helper()
	reply, err := f.request(t.Context(), method, path, body, access)
	if err != nil {
		t.Fatal("AMA_HTTP_CALL")
	}
	// All responding requests have entered their handler; no new client is
	// launched until this join. Concurrent tests instead join all clients first.
	f.handlers.Wait()
	return reply
}
func (f *amaFixture) archive(t *testing.T, id, body string) amaReply {
	return f.call(t, http.MethodDelete, "/admin/api/v1/models/"+id, body, f.access())
}

func (f *amaFixture) requireStatus(t *testing.T, reply amaReply, status int, code string) {
	t.Helper()
	if reply.status != status {
		t.Fatal("AMA_HTTP_STATUS")
	}
	for _, secret := range []string{f.key, f.csrf, "synthetic-opaque", "a-strong-preview-password"} {
		if secret != "" && strings.Contains(string(reply.body), secret) {
			t.Fatal("AMA_RESPONSE_METADATA")
		}
	}
	if code != "" {
		var value struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(reply.body, &value) != nil || value.Error.Code != code {
			t.Fatal("AMA_HTTP_ERROR_CODE")
		}
		if status == 503 && value.Error.Message != "Service is temporarily unavailable." {
			t.Fatal("AMA_FIXED_STORAGE_ERROR")
		}
	}
}

func (f *amaFixture) model(t *testing.T, id string) amaModel {
	t.Helper()
	var m amaModel
	if f.db.QueryRowContext(t.Context(), "SELECT id,model_kind,upstream_id,upstream_model,wire_protocol,created_at,enabled,revision,archived,archived_at FROM models WHERE id=?", id).
		Scan(&m.id, &m.kind, &m.upstream, &m.model, &m.wire, &m.created, &m.enabled, &m.revision, &m.archived, &m.archivedAt) != nil {
		t.Fatal("AMA_MODEL_FACT")
	}
	return m
}

func (f *amaFixture) requireArchived(t *testing.T, old amaModel, reply amaReply, result string) amaModel {
	t.Helper()
	actual := f.model(t, old.id)
	if !actual.archivedAt.Valid {
		t.Fatal("AMA_TOMBSTONE_TIME_MISSING")
	}
	if _, err := parseTime(actual.archivedAt.String); err != nil {
		t.Fatal("AMA_TOMBSTONE_TIME")
	}
	want := old
	if result == "archived" {
		want.enabled = 0
		want.archived = 1
		want.revision++
		want.archivedAt = actual.archivedAt
	}
	if actual != want {
		t.Fatal("AMA_TOMBSTONE_MODEL_FACTS")
	}
	var value modelView
	if json.Unmarshal(reply.body, &value) != nil || value.ID != actual.id || value.ModelKind != actual.kind || value.UpstreamID != actual.upstream || value.UpstreamModel != actual.model || value.WireProtocol != actual.wire || value.Enabled || !value.Archived || value.Revision != actual.revision || value.ArchivedAt == nil || *value.ArchivedAt != actual.archivedAt.String || value.ArchiveResult != result {
		t.Fatal("AMA_ARCHIVE_RESPONSE")
	}
	return actual
}

func (f *amaFixture) audits(t *testing.T) map[string]amaAudit {
	t.Helper()
	rows, err := f.db.QueryContext(t.Context(), "SELECT id,actor_id,action,target_type,target_id,result,occurred_at FROM account_lifecycle_audit ORDER BY id")
	if err != nil {
		t.Fatal("AMA_AUDIT_READ")
	}
	result := make(map[string]amaAudit)
	for rows.Next() {
		var a amaAudit
		if rows.Scan(&a.id, &a.actor, &a.action, &a.targetType, &a.target, &a.result, &a.stamp) != nil {
			_ = rows.Close()
			t.Fatal("AMA_AUDIT_SCAN")
		}
		result[a.id] = a
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		t.Fatal("AMA_AUDIT_CLOSE")
	}
	return result
}

func (f *amaFixture) auditAdded(t *testing.T, before map[string]amaAudit, target string) amaAudit {
	t.Helper()
	after := f.audits(t)
	var added amaAudit
	n := 0
	for id, a := range after {
		if _, old := before[id]; old {
			continue
		}
		n++
		added = a
		if a.actor != f.actor || a.action != "model.archive" || a.targetType != "model" || a.target != target || a.result != "succeeded" {
			t.Fatal("AMA_CURRENT_AUDIT_FIELDS")
		}
		if _, err := parseTime(a.stamp); err != nil {
			t.Fatal("AMA_CURRENT_AUDIT_TIME")
		}
	}
	if n != 1 {
		t.Fatal("AMA_CURRENT_AUDIT_IDENTITY")
	}
	all := make([]amaAudit, 0, len(before)+1)
	for _, a := range before {
		all = append(all, a)
	}
	all = append(all, added)
	sort.Slice(all, func(i, j int) bool {
		if all[i].stamp != all[j].stamp {
			return all[i].stamp > all[j].stamp
		}
		return all[i].id > all[j].id
	})
	if len(all) > lifecycleAuditLimit {
		all = all[:lifecycleAuditLimit]
	}
	want := make(map[string]amaAudit)
	for _, a := range all {
		want[a.id] = a
	}
	if !reflect.DeepEqual(want, after) {
		t.Fatal("AMA_EXACT_RETENTION_ORDER")
	}
	return added
}

// Snapshot every user table's row values, including auth and unrelated seeded
// facts, without printing any value. ORDER BY ordinal also supports WITHOUT ROWID.
func (f *amaFixture) facts(t *testing.T, stable bool) map[string]string {
	t.Helper()
	rows, err := f.db.QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal("AMA_FACT_TABLES")
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			_ = rows.Close()
			t.Fatal("AMA_FACT_TABLE_NAME")
		}
		tables = append(tables, name)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		t.Fatal("AMA_FACT_TABLE_CLOSE")
	}
	result := make(map[string]string)
	for _, table := range tables {
		if stable && (table == "account_lifecycle_audit" || table == "ama_fk_child") {
			continue
		}
		quoted := "\"" + strings.ReplaceAll(table, "\"", "\"\"") + "\""
		columnRows, err := f.db.QueryContext(t.Context(), "SELECT * FROM "+quoted+" LIMIT 0")
		if err != nil {
			t.Fatal("AMA_FACT_COLUMNS_QUERY")
		}
		columns, columnErr := columnRows.Columns()
		columnClose := columnRows.Close()
		if columnErr != nil || columnClose != nil || len(columns) == 0 {
			t.Fatal("AMA_FACT_COLUMNS")
		}
		query := "SELECT * FROM " + quoted
		if stable && table == "models" {
			query += " WHERE id<>'ama-target'"
		}
		order := make([]string, len(columns))
		for i := range order {
			order[i] = strconv.Itoa(i + 1)
		}
		dataRows, err := f.db.QueryContext(t.Context(), query+" ORDER BY "+strings.Join(order, ","))
		if err != nil {
			t.Fatal("AMA_FACT_QUERY")
		}
		var all [][]any
		for dataRows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if dataRows.Scan(pointers...) != nil {
				_ = dataRows.Close()
				t.Fatal("AMA_FACT_SCAN")
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = append([]byte(nil), b...)
				}
			}
			all = append(all, values)
		}
		dataErr, dataClose := dataRows.Err(), dataRows.Close()
		if dataErr != nil || dataClose != nil {
			t.Fatal("AMA_FACT_CLOSE")
		}
		encoded, err := json.Marshal(all)
		if err != nil {
			t.Fatal("AMA_FACT_ENCODE")
		}
		result[table] = string(encoded)
	}
	return result
}

func (f *amaFixture) seedAudits(t *testing.T, count int, future bool) {
	t.Helper()
	amaExec(t, f.db, "DELETE FROM account_lifecycle_audit")
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("AMA_SEED_BEGIN")
	}
	defer tx.Rollback()
	stamp := "2000-01-01T00:00:00Z"
	if future {
		stamp = "2099-01-01T00:00:00Z"
	}
	for i := 0; i < count; i++ {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'model.archive','model',?,'succeeded',?)", fmt.Sprintf("ama-old-%04d", i), f.actor, amaTarget, stamp); err != nil {
			t.Fatal("AMA_SEED_INSERT")
		}
	}
	if tx.Commit() != nil || len(f.audits(t)) != count {
		t.Fatal("AMA_SEED_COMMIT")
	}
}

func (f *amaFixture) signals() amaSignals {
	epoch, prior := f.app.accountPool.changeSnapshot()
	return amaSignals{epoch: epoch, prior: prior, targetCalls: f.target.calls.Load(), peerCalls: f.peer.calls.Load(), targetErr: f.target.ctx.Err()}
}
func (f *amaFixture) requireSignals(t *testing.T, before amaSignals, changed bool) {
	t.Helper()
	epoch, _ := f.app.accountPool.changeSnapshot()
	if changed {
		if epoch != before.epoch+1 || before.prior.Err() != context.Canceled || f.target.calls.Load() != before.targetCalls+1 || f.target.ctx.Err() != context.Canceled {
			t.Fatal("AMA_SUCCESS_SIGNALS")
		}
	}
	if !changed {
		if epoch != before.epoch || before.prior.Err() != nil || f.target.calls.Load() != before.targetCalls || f.target.ctx.Err() != before.targetErr {
			t.Fatal("AMA_NOOP_FAILURE_SIGNALS")
		}
	}
	if f.peer.calls.Load() != before.peerCalls || f.peer.ctx.Err() != nil {
		t.Fatal("AMA_PEER_CANCELLED")
	}
}

func amaReopened(t *testing.T, dir string) amaObservation {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cpa-cloud.db"))
	if err != nil {
		t.Fatal("AMA_REOPEN_CONNECTION")
	}
	defer func() {
		if db.Close() != nil {
			t.Error("AMA_REOPEN_CLOSE")
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	reader := &amaFixture{db: db}
	return amaObservation{model: reader.model(t, amaTarget), audits: reader.audits(t), facts: reader.facts(t, false)}
}

func TestAdminModelArchiveAuditPositive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		count   int
	}{{"enabled", true, 1}, {"disabled", false, 1}, {"near_limit", true, 999}, {"at_limit", true, 1000}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAMAFixture(t, tc.enabled, "")
			f.seedAudits(t, tc.count, false)
			old, priorAudits, stable, signals := f.model(t, amaTarget), f.audits(t), f.facts(t, true), f.signals()
			reply := f.archive(t, amaTarget, `{"expected_revision":1}`)
			f.requireStatus(t, reply, 200, "")
			model := f.requireArchived(t, old, reply, "archived")
			f.auditAdded(t, priorAudits, amaTarget)
			f.requireSignals(t, signals, true)
			if !reflect.DeepEqual(stable, f.facts(t, true)) {
				t.Fatal("AMA_POSITIVE_OTHER_FACTS")
			}
			facts, audits := f.facts(t, false), f.audits(t)
			f.release(t)
			if t.Failed() {
				t.Fatal("AMA_POSITIVE_OWNER_CLOSE")
			}
			reopened := amaReopened(t, f.dir)
			if reopened.model != model || !reflect.DeepEqual(facts, reopened.facts) || !reflect.DeepEqual(audits, reopened.audits) {
				t.Fatal("AMA_POSITIVE_REOPEN_FACTS")
			}
		})
	}
}

func TestAdminModelArchiveAuditRetention(t *testing.T) {
	for _, name := range []string{"ignore", "future_window", "delete_inserted"} {
		expectedOld := false
		passed := t.Run(name, func(t *testing.T) {
			f := newAMAFixture(t, true, "")
			if name == "future_window" {
				f.seedAudits(t, 1000, true)
			} else {
				f.seedAudits(t, 1, false)
			}
			switch name {
			case "ignore":
				amaExec(t, f.db, `CREATE TRIGGER ama_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.action='model.archive' AND NEW.target_id='ama-target' BEGIN SELECT RAISE(IGNORE); END`)
			case "delete_inserted":
				amaExec(t, f.db, `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='model.archive' AND NEW.target_id='ama-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id=NEW.id; END`)
			}
			old, audits, before, stable, signals := f.model(t, amaTarget), f.audits(t), f.facts(t, false), f.facts(t, true), f.signals()
			if old.archived != 0 || old.revision != 1 {
				t.Fatal("AMA_OLD_FIRST_TRANSITION_PRECONDITION")
			}
			reply := f.archive(t, amaTarget, `{"expected_revision":1}`)
			if os.Getenv("CPA_AMA_OLD_ORACLE") == "1" {
				f.requireStatus(t, reply, 200, "")
				model := f.requireArchived(t, old, reply, "archived")
				if !reflect.DeepEqual(audits, f.audits(t)) || !reflect.DeepEqual(stable, f.facts(t, true)) {
					t.Fatal("AMA_OLD_AUDIT_FACTS")
				}
				f.requireSignals(t, signals, true)
				committedFacts := f.facts(t, false)
				f.release(t)
				if t.Failed() {
					t.Fatal("AMA_OLD_OWNER_CLEANUP")
				}
				reopened := amaReopened(t, f.dir)
				if reopened.model != model || !reflect.DeepEqual(audits, reopened.audits) || !reflect.DeepEqual(committedFacts, reopened.facts) {
					t.Fatal("AMA_OLD_REOPEN_PERSISTENCE")
				}
				if t.Failed() {
					t.Fatal("AMA_OLD_READER_CLEANUP")
				}
				expectedOld = true
				t.Error("MODEL_ARCHIVE_AUDIT_OLD_COMMITTED_WITHOUT_CURRENT_EVENT")
				return
			}
			f.requireStatus(t, reply, 503, "storage_unavailable")
			f.requireSignals(t, signals, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("AMA_RETENTION_ROLLBACK_FACTS")
			}
		})
		if !passed && !expectedOld {
			t.Fatal("AMA_STOP_UNEXPECTED_RETENTION")
		}
	}
}

func TestAdminModelArchiveAuditFaults(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		full      bool
	}{
		{"wrong_actor", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET actor_id='not-the-actor' WHERE id=NEW.id; END`, false},
		{"wrong_target", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET target_id='ama-peer' WHERE id=NEW.id; END`, false},
		{"wrong_action", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET action='model.update' WHERE id=NEW.id; END`, false},
		{"wrong_type", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET target_type='upstream' WHERE id=NEW.id; END`, false},
		{"wrong_result", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET result='failed' WHERE id=NEW.id; END`, false},
		{"wrong_time", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET occurred_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, false},
		{"wrong_id", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET id='ama-substitute' WHERE id=NEW.id; END`, false},
		{"duplicate", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN INSERT INTO account_lifecycle_audit VALUES('ama-extra',NEW.actor_id,NEW.action,NEW.target_type,NEW.target_id,NEW.result,NEW.occurred_at); END`, false},
		{"insert_error", `CREATE TRIGGER ama_fault BEFORE INSERT ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'AMA_CONTROLLED_INSERT'); END`, false},
		{"trim_ignore", `CREATE TRIGGER ama_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(IGNORE); END`, true},
		{"trim_error", `CREATE TRIGGER ama_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'AMA_CONTROLLED_TRIM'); END`, true},
		{"reverse_model", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE models SET enabled=1,archived=0,archived_at=NULL,revision=1 WHERE id='ama-target'; END`, false},
		{"change_created", `CREATE TRIGGER ama_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE models SET created_at='2000-01-01T00:00:00Z' WHERE id='ama-target'; END`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAMAFixture(t, true, "")
			count := 1
			if tc.full {
				count = 1000
			}
			f.seedAudits(t, count, false)
			amaExec(t, f.db, tc.sql)
			before, signals := f.facts(t, false), f.signals()
			f.requireStatus(t, f.archive(t, amaTarget, `{"expected_revision":1}`), 503, "storage_unavailable")
			f.requireSignals(t, signals, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("AMA_FAULT_ROLLBACK_FACTS")
			}
		})
	}
}

func TestAdminModelArchiveAuditAdmission(t *testing.T) {
	for _, name := range []string{"no_session", "employee_bearer", "origin_before_csrf", "csrf", "decode", "unknown_field", "empty", "zero", "negative", "missing", "stale", "model_sql", "model_ignore", "archived_invalid_revision"} {
		t.Run(name, func(t *testing.T) {
			f := newAMAFixture(t, true, "")
			access := f.access()
			body, target := `{"expected_revision":1}`, amaTarget
			status, code := 400, "invalid_request"
			switch name {
			case "no_session":
				access.cookie = nil
				status, code = 401, "authentication_required"
			case "employee_bearer":
				access.cookie = nil
				access.bearer = f.key
				status, code = 401, "authentication_required"
			case "origin_before_csrf":
				access.origin = "https://example.invalid"
				access.csrf = "wrong"
				status, code = 403, "origin_rejected"
			case "csrf":
				access.csrf = "wrong"
				status, code = 403, "csrf_rejected"
			case "decode":
				body = `{"expected_revision":`
			case "unknown_field":
				body = `{"expected_revision":1,"extra":true}`
			case "empty":
				body = `{}`
				code = "invalid_revision"
			case "zero":
				body = `{"expected_revision":0}`
				code = "invalid_revision"
			case "negative":
				body = `{"expected_revision":-1}`
				code = "invalid_revision"
			case "missing":
				target = "ama-missing"
				status, code = 404, "not_found"
			case "stale":
				body = `{"expected_revision":2}`
				status, code = 409, "revision_conflict"
			case "model_sql":
				amaExec(t, f.db, `CREATE TRIGGER ama_model_fault BEFORE UPDATE ON models BEGIN SELECT RAISE(ABORT,'AMA_CONTROLLED_MODEL'); END`)
				status, code = 503, "storage_unavailable"
			case "model_ignore":
				amaExec(t, f.db, `CREATE TRIGGER ama_model_fault BEFORE UPDATE ON models BEGIN SELECT RAISE(IGNORE); END`)
				status, code = 409, "revision_conflict"
			case "archived_invalid_revision":
				amaExec(t, f.db, "UPDATE models SET archived=1,enabled=0,archived_at='2000-01-01T00:00:00Z',revision=7 WHERE id=?", amaTarget)
				body = `{"expected_revision":0}`
				code = "invalid_revision"
			}
			before, signals := f.facts(t, false), f.signals()
			f.requireStatus(t, f.call(t, http.MethodDelete, "/admin/api/v1/models/"+target, body, access), status, code)
			f.requireSignals(t, signals, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("AMA_ADMISSION_FACTS")
			}
		})
	}
}

func TestAdminModelArchiveAuditAlreadyArchived(t *testing.T) {
	for _, name := range []string{"after_first_transition", "without_historical_audit"} {
		t.Run(name, func(t *testing.T) {
			f := newAMAFixture(t, true, "native_positive")
			if name == "after_first_transition" {
				old, signals := f.model(t, amaTarget), f.signals()
				f.adapter.arm()
				reply := f.archive(t, amaTarget, `{"expected_revision":1}`)
				f.requireStatus(t, reply, 200, "")
				f.requireArchived(t, old, reply, "archived")
				f.requireSignals(t, signals, true)
			} else {
				amaExec(t, f.db, "UPDATE models SET archived=1,enabled=0,archived_at='2000-01-01T00:00:00Z',revision=7 WHERE id=?", amaTarget)
				amaExec(t, f.db, "DELETE FROM account_lifecycle_audit")
			}
			before, model, commits := f.facts(t, false), f.model(t, amaTarget), f.adapter.commits.Load()
			for _, body := range []string{`{"expected_revision":1}`, `{"expected_revision":9999}`} {
				signals := f.signals()
				reply := f.archive(t, amaTarget, body)
				f.requireStatus(t, reply, 200, "")
				f.requireArchived(t, model, reply, "already_archived")
				f.requireSignals(t, signals, false)
				if f.adapter.commits.Load() != commits || !reflect.DeepEqual(before, f.facts(t, false)) {
					t.Fatal("AMA_ALREADY_ARCHIVED_WRITES")
				}
			}
		})
	}
}

func TestAdminModelArchiveAuditConcurrentDelete(t *testing.T) {
	f := newAMAFixture(t, true, "")
	f.seedAudits(t, 1, false)
	old, audits, stable, signals := f.model(t, amaTarget), f.audits(t), f.facts(t, true), f.signals()
	type result struct {
		reply amaReply
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var clients sync.WaitGroup
	for i := 0; i < 2; i++ {
		clients.Add(1)
		go func() {
			defer clients.Done()
			<-start
			reply, err := f.request(t.Context(), http.MethodDelete, "/admin/api/v1/models/"+amaTarget, `{"expected_revision":1}`, f.access())
			results <- result{reply: reply, err: err}
		}()
	}
	close(start)
	clients.Wait()
	close(results)
	f.handlers.Wait()
	first, noOp := 0, 0
	for r := range results {
		if r.err != nil {
			t.Fatal("AMA_CONCURRENT_HTTP")
		}
		f.requireStatus(t, r.reply, 200, "")
		var value modelView
		if json.Unmarshal(r.reply.body, &value) != nil {
			t.Fatal("AMA_CONCURRENT_RESPONSE")
		}
		switch value.ArchiveResult {
		case "archived":
			first++
			f.requireArchived(t, old, r.reply, "archived")
		case "already_archived":
			noOp++
			f.requireArchived(t, f.model(t, amaTarget), r.reply, "already_archived")
		default:
			t.Fatal("AMA_CONCURRENT_RESULT")
		}
	}
	if first != 1 || noOp != 1 {
		t.Fatal("AMA_CONCURRENT_TRANSITIONS")
	}
	f.auditAdded(t, audits, amaTarget)
	f.requireSignals(t, signals, true)
	if !reflect.DeepEqual(stable, f.facts(t, true)) {
		t.Fatal("AMA_CONCURRENT_OTHER_FACTS")
	}
}

// Private request ownership prevents login/control operations from receiving an
// injected return. Real SQLite statements and Commit are delegated unchanged.
type amaRequestKey struct{}
type amaRequest struct {
	owner  *amaConnector
	target string
	cancel context.CancelFunc
}
type amaConnector struct {
	base            driver.Driver
	dsn, mode       string
	inject          atomic.Bool
	begins, commits atomic.Int64
	realCommitted   atomic.Bool
	nativeCode      atomic.Int64
}

func (d *amaConnector) Driver() driver.Driver { return d.base }
func (d *amaConnector) Connect(ctx context.Context) (driver.Conn, error) {
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
	return &amaConn{Conn: c, owner: d}, nil
}
func (d *amaConnector) arm() {
	d.begins.Store(0)
	d.commits.Store(0)
	d.realCommitted.Store(false)
	d.nativeCode.Store(0)
	d.inject.Store(true)
}

type amaConn struct {
	driver.Conn
	owner *amaConnector
}

func (c *amaConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	r, _ := ctx.Value(amaRequestKey{}).(*amaRequest)
	if r == nil || r.owner != c.owner || r.target != amaTarget {
		r = nil
	} else {
		c.owner.begins.Add(1)
	}
	var tx driver.Tx
	var err error
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = begin.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &amaTx{Tx: tx, owner: c.owner, request: r}, nil
}

type amaTx struct {
	driver.Tx
	owner   *amaConnector
	request *amaRequest
}

func (tx *amaTx) Commit() error {
	if tx.request == nil {
		return tx.Tx.Commit()
	}
	tx.owner.commits.Add(1)
	if err := tx.Tx.Commit(); err != nil {
		var coded interface{ Code() int }
		if errors.As(err, &coded) {
			tx.owner.nativeCode.Store(int64(coded.Code()))
		} else {
			tx.owner.nativeCode.Store(-1)
		}
		return err
	}
	tx.owner.realCommitted.Store(true)
	if tx.owner.inject.Load() {
		switch tx.owner.mode {
		case "after_error":
			return errors.New("AMA_CONTROLLED_AFTER_COMMIT")
		case "after_cancel":
			tx.request.cancel()
			return context.Canceled
		}
	}
	return nil
}

func TestAdminModelArchiveAuditCommit(t *testing.T) {
	for _, mode := range []string{"native_positive", "native_refusal", "after_error", "after_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newAMAFixture(t, true, mode)
			if strings.HasPrefix(mode, "native_") {
				amaExec(t, f.db, "CREATE TABLE ama_fk_parent(id TEXT PRIMARY KEY)")
				amaExec(t, f.db, "CREATE TABLE ama_fk_child(id TEXT PRIMARY KEY,parent_id TEXT REFERENCES ama_fk_parent(id) DEFERRABLE INITIALLY DEFERRED)")
				if mode == "native_positive" {
					amaExec(t, f.db, "INSERT INTO ama_fk_parent VALUES('parent')")
				}
				amaExec(t, f.db, `CREATE TRIGGER ama_commit AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='model.archive' AND NEW.target_id='ama-target' BEGIN INSERT INTO ama_fk_child VALUES(NEW.id,'parent'); END`)
				var fk int
				if f.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk) != nil || fk != 1 {
					t.Fatal("AMA_COMMIT_FK_OFF")
				}
			}
			old, before, audits, stable, signals := f.model(t, amaTarget), f.facts(t, false), f.audits(t), f.facts(t, true), f.signals()
			f.adapter.arm()
			status, code := 503, "storage_unavailable"
			if mode == "native_positive" {
				status, code = 200, ""
			}
			reply := f.archive(t, amaTarget, `{"expected_revision":1}`)
			f.requireStatus(t, reply, status, code)
			f.requireSignals(t, signals, mode == "native_positive")
			if f.adapter.begins.Load() != 1 || f.adapter.commits.Load() != 1 {
				t.Fatal("AMA_COMMIT_OWNERSHIP")
			}
			if mode == "native_refusal" {
				if f.adapter.realCommitted.Load() || f.adapter.nativeCode.Load()&255 != 19 || !reflect.DeepEqual(before, f.facts(t, false)) {
					t.Fatal("AMA_NATIVE_REFUSAL_FACTS")
				}
				return
			}
			if !f.adapter.realCommitted.Load() || f.adapter.nativeCode.Load() != 0 {
				t.Fatal("AMA_REAL_COMMIT_NOT_CONFIRMED")
			}
			actual := f.model(t, amaTarget)
			want := old
			want.enabled = 0
			want.archived = 1
			want.revision++
			want.archivedAt = actual.archivedAt
			if actual != want || !actual.archivedAt.Valid {
				t.Fatal("AMA_COMMITTED_TOMBSTONE")
			}
			if _, err := parseTime(actual.archivedAt.String); err != nil {
				t.Fatal("AMA_COMMITTED_TIME")
			}
			f.auditAdded(t, audits, amaTarget)
			if !reflect.DeepEqual(stable, f.facts(t, true)) {
				t.Fatal("AMA_COMMIT_OTHER_FACTS")
			}
			if mode == "after_error" || mode == "after_cancel" {
				// Explicit later DELETE preserves archive's no-op-before-CAS contract,
				// unlike PATCH: it is200 already_archived, not409 and not auto-retry.
				committed, signals := f.facts(t, false), f.signals()
				later := f.archive(t, amaTarget, `{"expected_revision":1}`)
				f.requireStatus(t, later, 200, "")
				f.requireArchived(t, actual, later, "already_archived")
				f.requireSignals(t, signals, false)
				if f.adapter.commits.Load() != 1 || !reflect.DeepEqual(committed, f.facts(t, false)) {
					t.Fatal("AMA_UNCERTAIN_NOOP_REPLAY")
				}
			}
			facts := f.facts(t, false)
			f.release(t)
			if t.Failed() {
				t.Fatal("AMA_COMMIT_OWNER_CLEANUP")
			}
			reopened := amaReopened(t, f.dir)
			if reopened.model != actual || !reflect.DeepEqual(facts, reopened.facts) {
				t.Fatal("AMA_COMMIT_REOPEN_FACTS")
			}
		})
	}
}
