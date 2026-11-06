package service

// Independently authored from the bounded model-audit contract and SQLite/Go
// public interfaces. The adapter below only simulates return-boundary errors;
// it is not a real I/O fault or a production worker/credential test.
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const muaTarget = "mua-target"

type muaModel struct {
	id, kind, upstream, model, wire, created string
	enabled, revision, archived              int64
	archivedAt                               sql.NullString
}

type muaAudit struct {
	id, actor, action, targetType, target, result, stamp string
}

type muaReply struct {
	status int
	body   []byte
	header http.Header
}

type muaFixture struct {
	dir                         string
	app                         *App
	bootstrap                   *store
	db                          *sql.DB
	server                      *httptest.Server
	client                      *http.Client
	cookie                      *http.Cookie
	csrf, actor, key            string
	runtimeCtx, monitorCtx      context.Context
	targetCtx, peerCtx, ctrlCtx context.Context
	handlers                    sync.WaitGroup
	finished                    chan struct{}
	closing                     atomic.Bool
	releaseOnce                 sync.Once
	control                     *muaConnector
}

func muaExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal("MUA_FIXTURE_SQL")
	}
}

func newMUAFixture(t *testing.T, mode string) *muaFixture {
	t.Helper()
	dir := t.TempDir()
	f := &muaFixture{dir: dir, finished: make(chan struct{}, 16)}
	t.Cleanup(func() { f.release(t) })
	if Initialize(t.Context(), dir, strings.NewReader("a-strong-preview-password\n")) != nil {
		t.Fatal("MUA_INITIALIZE")
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal("MUA_OPEN_STORE")
	}
	f.bootstrap, f.db = s, s.db
	cfg := Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "model-update-audit-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("MUA_FIXTURE_SECRETS")
	}
	if f.db.QueryRowContext(t.Context(), "SELECT id FROM admins WHERE username='admin'").Scan(&f.actor) != nil || f.actor == "" {
		t.Fatal("MUA_FIXTURE_ACTOR")
	}
	stamp := utcNow()
	for _, id := range []string{"mua-up", "mua-up2"} {
		muaExec(t, f.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,'Fixture','openai-compatible','https://example.invalid',1,?,1,1,?)", id, []byte("synthetic-opaque"), stamp)
	}
	for _, id := range []string{muaTarget, "mua-peer", "mua-control"} {
		muaExec(t, f.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,'mua-up','fixture-model',1,?)", id, stamp)
	}
	for _, id := range []string{"mua-employee", "mua-other"} {
		muaExec(t, f.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,'Fixture','active','selected',1,?)", id, stamp)
		muaExec(t, f.db, "INSERT INTO employee_models(employee_id,model_id) VALUES(?,'mua-peer')", id)
	}
	selector, secret := "mua-selector", strings.Repeat("k", 43)
	f.key = "cpac_" + selector + "." + secret
	muaExec(t, f.db, "INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('mua-key','mua-employee','Fixture',?,?,1,'mua-key-operation',?)", selector, sec.digest("employee-key/v1\x00"+selector, secret), stamp)
	if mode != "" {
		base := f.db.Driver()
		if s.close() != nil {
			t.Fatal("MUA_BOOTSTRAP_CLOSE")
		}
		f.bootstrap, f.db = nil, nil
		f.control = &muaConnector{base: base, dsn: filepath.Join(dir, "cpa-cloud.db"), mode: mode}
		f.db = sql.OpenDB(f.control)
		f.bootstrap = &store{db: f.db}
		f.db.SetMaxOpenConns(1)
		f.db.SetMaxIdleConns(1)
		if f.db.PingContext(t.Context()) != nil {
			t.Fatal("MUA_ADAPTER_OPEN")
		}
		s = f.bootstrap
	}
	// The single DB is fixed before App/runtime/HTTP publication. No App.Open,
	// Handler, worker constructor or running-store reassignment occurs here.
	app := &App{cfg: cfg, store: s, secrets: sec, logins: make(map[string]*loginAttempt)}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	app.accountPool = &accountPoolRuntime{app: app, ctx: runtimeCtx, cancel: runtimeCancel,
		changeContext: changeCtx, changeCancel: changeCancel,
		active: make(map[*accountPoolLease]struct{}), maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	f.targetCtx, f.peerCtx, f.ctrlCtx = muaMonitorContexts(app, monitorCtx, monitorCancel)
	f.runtimeCtx, f.monitorCtx, f.app, f.bootstrap = runtimeCtx, monitorCtx, app, nil
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	mux.HandleFunc("PATCH /admin/api/v1/models/{id}", app.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		ctx := r.Context()
		if f.control != nil {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			ctx = context.WithValue(ctx, muaRequestKey{}, &muaRequest{owner: f.control, target: r.PathValue("id"), cancel: cancel})
		}
		app.updateModel(w, r.WithContext(ctx), session)
	}, true))
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.closing.Load() {
			w.WriteHeader(503)
			return
		}
		f.handlers.Add(1)
		defer f.handlers.Done()
		if r.Method == http.MethodPatch {
			defer func() { f.finished <- struct{}{} }()
		}
		mux.ServeHTTP(w, r)
	})))
	f.client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := f.call(t, http.MethodPost, "/admin/api/v1/sessions", `{"username":"admin","password":"a-strong-preview-password"}`, nil, "", f.server.URL, "")
	var value struct {
		CSRF string `json:"csrf_token"`
	}
	if login.status != 200 || json.Unmarshal(login.body, &value) != nil || value.CSRF == "" {
		t.Fatal("MUA_LOGIN")
	}
	for _, cookie := range (&http.Response{Header: login.header}).Cookies() {
		if cookie.Name == adminCookieName {
			f.cookie = cookie
		}
	}
	f.csrf = value.CSRF
	if f.cookie == nil || f.cookie.Value == "" || f.app.accountPool == nil || f.app.channelMonitors == nil {
		t.Fatal("MUA_REAL_AUTH_RUNTIME")
	}
	// Every fixture proves real HTTP, audit, notification and target-only
	// cancellation on a separate control model before installing any fault.
	epoch, changed := app.accountPool.changeSnapshot()
	reply := f.patch(t, "mua-control", `{"expected_revision":1,"enabled":false}`)
	f.requireStatus(t, reply, 200, "")
	if f.model(t, "mua-control").revision != 2 || f.model(t, "mua-control").enabled != 0 || f.ctrlCtx.Err() != context.Canceled || f.targetCtx.Err() != nil || f.peerCtx.Err() != nil {
		t.Fatal("MUA_CONTROL_FACTS")
	}
	next, _ := app.accountPool.changeSnapshot()
	if next != epoch+1 || changed.Err() != context.Canceled {
		t.Fatal("MUA_CONTROL_NOTIFICATION")
	}
	f.requireCurrentAudit(t, map[string]muaAudit{}, "mua-control")
	return f
}

func muaMonitorContexts(app *App, ctx context.Context, cancel context.CancelFunc) (context.Context, context.Context, context.Context) {
	target, targetCancel := context.WithCancel(ctx)
	peer, peerCancel := context.WithCancel(ctx)
	control, controlCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	close(done) // workerless, disabled coordinator: no worker was launched
	app.channelMonitors = &channelMonitorCoordinator{app: app, ctx: ctx, cancel: cancel, done: done, wake: make(chan struct{}, 1),
		active: map[string]channelMonitorActive{
			"target":  {modelID: muaTarget, cancel: targetCancel},
			"peer":    {modelID: "mua-peer", cancel: peerCancel},
			"control": {modelID: "mua-control", cancel: controlCancel},
		}}
	return target, peer, control
}

func (f *muaFixture) release(t *testing.T) {
	t.Helper()
	f.releaseOnce.Do(func() {
		f.closing.Store(true)
		if f.server != nil {
			f.server.Close()
		}
		f.handlers.Wait()
		if f.app != nil {
			if f.app.Close() != nil {
				t.Error("MUA_CLEANUP_APP")
			}
			if f.runtimeCtx.Err() != context.Canceled || f.monitorCtx.Err() != context.Canceled {
				t.Error("MUA_CLEANUP_CONTEXT")
			}
		} else if f.bootstrap != nil {
			if f.bootstrap.close() != nil {
				t.Error("MUA_CLEANUP_BOOTSTRAP")
			}
		}
		if f.db != nil && f.db.PingContext(context.Background()) == nil {
			t.Error("MUA_CLEANUP_DB_OPEN")
		}
	})
}

func (f *muaFixture) call(t *testing.T, method, path, body string, cookie *http.Cookie, csrf, origin, bearer string) muaReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal("MUA_HTTP_REQUEST")
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := f.client.Do(req)
	if err != nil {
		t.Fatal("MUA_HTTP_TRANSPORT")
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("MUA_HTTP_BODY")
	}
	if method == http.MethodPatch {
		select {
		case <-f.finished:
		case <-time.After(5 * time.Second):
			t.Fatal("MUA_HTTP_COMPLETION")
		}
	}
	return muaReply{status: response.StatusCode, body: data, header: response.Header.Clone()}
}

func (f *muaFixture) patch(t *testing.T, target, body string) muaReply {
	return f.call(t, http.MethodPatch, "/admin/api/v1/models/"+target, body, f.cookie, f.csrf, f.server.URL, "")
}

func (f *muaFixture) requireStatus(t *testing.T, reply muaReply, status int, code string) {
	t.Helper()
	if reply.status != status {
		t.Fatal("MUA_HTTP_STATUS")
	}
	for _, secret := range []string{f.key, f.csrf, "synthetic-opaque", "a-strong-preview-password"} {
		if secret != "" && strings.Contains(string(reply.body), secret) {
			t.Fatal("MUA_RESPONSE_METADATA")
		}
	}
	if code != "" {
		var value struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(reply.body, &value) != nil || value.Error.Code != code {
			t.Fatal("MUA_HTTP_ERROR_CODE")
		}
		if status == 503 && value.Error.Message != "Service is temporarily unavailable." {
			t.Fatal("MUA_FIXED_STORAGE_ERROR")
		}
	}
}

func (f *muaFixture) model(t *testing.T, id string) muaModel {
	t.Helper()
	var m muaModel
	if f.db.QueryRowContext(t.Context(), "SELECT id,model_kind,upstream_id,upstream_model,wire_protocol,created_at,enabled,revision,archived,archived_at FROM models WHERE id=?", id).
		Scan(&m.id, &m.kind, &m.upstream, &m.model, &m.wire, &m.created, &m.enabled, &m.revision, &m.archived, &m.archivedAt) != nil {
		t.Fatal("MUA_MODEL_FACT")
	}
	return m
}

func (f *muaFixture) audits(t *testing.T) map[string]muaAudit {
	t.Helper()
	rows, err := f.db.QueryContext(t.Context(), "SELECT id,actor_id,action,target_type,target_id,result,occurred_at FROM account_lifecycle_audit ORDER BY id")
	if err != nil {
		t.Fatal("MUA_AUDIT_READ")
	}
	result := make(map[string]muaAudit)
	for rows.Next() {
		var a muaAudit
		if rows.Scan(&a.id, &a.actor, &a.action, &a.targetType, &a.target, &a.result, &a.stamp) != nil {
			_ = rows.Close()
			t.Fatal("MUA_AUDIT_SCAN")
		}
		result[a.id] = a
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		t.Fatal("MUA_AUDIT_CLOSE")
	}
	return result
}

func (f *muaFixture) requireCurrentAudit(t *testing.T, before map[string]muaAudit, target string) {
	t.Helper()
	added := 0
	for id, a := range f.audits(t) {
		if _, old := before[id]; old {
			continue
		}
		added++
		if a.actor != f.actor || a.action != "model.update" || a.targetType != "model" || a.target != target || a.result != "succeeded" {
			t.Fatal("MUA_CURRENT_AUDIT_FIELDS")
		}
		if _, err := parseTime(a.stamp); err != nil {
			t.Fatal("MUA_CURRENT_AUDIT_TIME")
		}
	}
	if added != 1 {
		t.Fatal("MUA_CURRENT_AUDIT_IDENTITY")
	}
}

func (f *muaFixture) facts(t *testing.T, stable bool) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, table := range []string{"models", "upstreams", "employees", "employee_models", "access_keys", "account_lifecycle_audit"} {
		if stable && table == "account_lifecycle_audit" {
			continue
		}
		query := "SELECT * FROM " + table
		if stable && table == "models" {
			query += " WHERE id<>'mua-target'"
		}
		rows, err := f.db.QueryContext(t.Context(), query+" ORDER BY rowid")
		if err != nil {
			t.Fatal("MUA_FACTS_QUERY")
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal("MUA_FACTS_COLUMNS")
		}
		var all [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if rows.Scan(pointers...) != nil {
				_ = rows.Close()
				t.Fatal("MUA_FACTS_SCAN")
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = append([]byte(nil), b...)
				}
			}
			all = append(all, values)
		}
		rowErr, closeErr := rows.Err(), rows.Close()
		if rowErr != nil || closeErr != nil {
			t.Fatal("MUA_FACTS_CLOSE")
		}
		encoded, err := json.Marshal(all)
		if err != nil {
			t.Fatal("MUA_FACTS_ENCODE")
		}
		result[table] = string(encoded)
	}
	return result
}

func (f *muaFixture) seedAudits(t *testing.T, count int, future bool) {
	t.Helper()
	muaExec(t, f.db, "DELETE FROM account_lifecycle_audit")
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("MUA_SEED_BEGIN")
	}
	defer tx.Rollback()
	stamp := "2000-01-01T00:00:00Z"
	if future {
		stamp = "2099-01-01T00:00:00Z"
	}
	for i := 0; i < count; i++ {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'model.update','model',?,'succeeded',?)", fmt.Sprintf("mua-old-%04d", i), f.actor, muaTarget, stamp); err != nil {
			t.Fatal("MUA_SEED_INSERT")
		}
	}
	if tx.Commit() != nil || len(f.audits(t)) != count {
		t.Fatal("MUA_SEED_COMMIT")
	}
}

func (f *muaFixture) notification(t *testing.T, epoch uint64, prior context.Context, success bool) {
	t.Helper()
	next, _ := f.app.accountPool.changeSnapshot()
	if success {
		if next != epoch+1 || prior.Err() != context.Canceled || f.targetCtx.Err() != context.Canceled {
			t.Fatal("MUA_SUCCESS_NOTIFICATION")
		}
	} else if next != epoch || prior.Err() != nil || f.targetCtx.Err() != nil {
		t.Fatal("MUA_FAILURE_NOTIFICATION")
	}
	if f.peerCtx.Err() != nil {
		t.Fatal("MUA_PEER_CANCELLED")
	}
}

func (f *muaFixture) arm() {
	if f.control != nil {
		f.control.begins.Store(0)
		f.control.commits.Store(0)
		f.control.realCommitted.Store(false)
		f.control.nativeError.Store(false)
		f.control.inject.Store(true)
	}
}

func TestAdminModelUpdateAuditPositive(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
	}{
		{"ordinary", `{"expected_revision":1,"enabled":false}`, 1},
		{"same_value", `{"expected_revision":1,"enabled":true}`, 1},
		{"target_and_wire", `{"expected_revision":1,"upstream_id":"mua-up2","upstream_model":"next-model","wire_protocol":"openai-responses"}`, 1},
		{"near_limit", `{"expected_revision":1,"enabled":false}`, 999},
		{"at_limit", `{"expected_revision":1,"enabled":false}`, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMUAFixture(t, "")
			f.seedAudits(t, tc.count, false)
			before, stable, old := f.audits(t), f.facts(t, true), f.model(t, muaTarget)
			epoch, changed := f.app.accountPool.changeSnapshot()
			reply := f.patch(t, muaTarget, tc.body)
			f.requireStatus(t, reply, 200, "")
			after := f.model(t, muaTarget)
			if after.revision != old.revision+1 || after.created != old.created || after.kind != old.kind || after.archived != 0 || after.archivedAt.Valid {
				t.Fatal("MUA_POSITIVE_PERSISTED_MODEL")
			}
			var value modelView
			if json.Unmarshal(reply.body, &value) != nil || value.ID != after.id || value.Revision != after.revision || value.UpstreamID != after.upstream || value.UpstreamModel != after.model || value.WireProtocol != after.wire || value.Enabled != (after.enabled != 0) {
				t.Fatal("MUA_POSITIVE_RESPONSE")
			}
			if tc.name == "target_and_wire" {
				if after.upstream != "mua-up2" || after.model != "next-model" || after.wire != "openai-responses" || after.enabled != 1 {
					t.Fatal("MUA_TARGET_WIRE_CHANGE")
				}
			} else if tc.name == "same_value" {
				if after.enabled != 1 || after.upstream != old.upstream || after.model != old.model || after.wire != old.wire {
					t.Fatal("MUA_SAME_VALUE_CHANGE")
				}
			} else if after.enabled != 0 || after.upstream != old.upstream || after.model != old.model || after.wire != old.wire {
				t.Fatal("MUA_ENABLED_CHANGE")
			}
			f.requireCurrentAudit(t, before, muaTarget)
			f.notification(t, epoch, changed, true)
			wanted := tc.count + 1
			if wanted > lifecycleAuditLimit {
				wanted = lifecycleAuditLimit
			}
			if len(f.audits(t)) != wanted || !reflect.DeepEqual(stable, f.facts(t, true)) {
				t.Fatal("MUA_POSITIVE_PRESERVATION")
			}
			facts := f.facts(t, false)
			f.release(t)
			s, err := openStore(f.dir)
			if err != nil {
				t.Fatal("MUA_RESTART_OPEN")
			}
			defer func() {
				if s.close() != nil {
					t.Error("MUA_RESTART_CLOSE")
				}
			}()
			reopened := &muaFixture{db: s.db}
			equal := reflect.DeepEqual(facts, reopened.facts(t, false))
			if !equal {
				t.Fatal("MUA_RESTART_FACTS")
			}
		})
	}
}

func TestAdminModelUpdateAuditRetention(t *testing.T) {
	for _, name := range []string{"ignore", "future_window", "delete_inserted"} {
		expectedOld := false
		passed := t.Run(name, func(t *testing.T) {
			f := newMUAFixture(t, "")
			if name == "future_window" {
				f.seedAudits(t, 1000, true)
			} else {
				f.seedAudits(t, 1, false)
			}
			switch name {
			case "ignore":
				muaExec(t, f.db, `CREATE TRIGGER mua_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.action='model.update' AND NEW.target_id='mua-target' BEGIN SELECT RAISE(IGNORE); END`)
			case "delete_inserted":
				muaExec(t, f.db, `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='model.update' AND NEW.target_id='mua-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id=NEW.id; END`)
			}
			before, audits, old := f.facts(t, false), f.audits(t), f.model(t, muaTarget)
			epoch, changed := f.app.accountPool.changeSnapshot()
			reply := f.patch(t, muaTarget, `{"expected_revision":1,"enabled":false}`)
			if os.Getenv("CPA_MUA_OLD_ORACLE") == "1" {
				f.requireStatus(t, reply, 200, "")
				after := f.model(t, muaTarget)
				want := old
				want.revision++
				want.enabled = 0
				if after != want || !reflect.DeepEqual(audits, f.audits(t)) {
					t.Fatal("MUA_OLD_ORACLE_FACTS")
				}
				f.notification(t, epoch, changed, true)
				f.release(t)
				if t.Failed() {
					t.Fatal("MUA_OLD_CLEANUP")
				}
				expectedOld = true
				t.Error("MODEL_UPDATE_AUDIT_OLD_COMMITTED_WITHOUT_CURRENT_EVENT")
				return
			}
			f.requireStatus(t, reply, 503, "storage_unavailable")
			f.notification(t, epoch, changed, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("MUA_RETENTION_ROLLBACK")
			}
		})
		if !passed && !expectedOld {
			t.Fatal("MUA_STOP_UNEXPECTED_RETENTION")
		}
	}
}

func TestAdminModelUpdateAuditFaults(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		full      bool
	}{
		{"wrong_actor", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET actor_id='not-the-actor' WHERE id=NEW.id; END`, false},
		{"wrong_target", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET target_id='mua-peer' WHERE id=NEW.id; END`, false},
		{"wrong_action", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET action='model.archive' WHERE id=NEW.id; END`, false},
		{"wrong_result", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET result='failed' WHERE id=NEW.id; END`, false},
		{"wrong_type", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET target_type='upstream' WHERE id=NEW.id; END`, false},
		{"wrong_time", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET occurred_at='2000-01-01T00:00:00Z' WHERE id=NEW.id; END`, false},
		{"wrong_identity", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE account_lifecycle_audit SET id='mua-substitute' WHERE id=NEW.id; END`, false},
		{"duplicate", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN INSERT INTO account_lifecycle_audit VALUES('mua-extra',NEW.actor_id,NEW.action,NEW.target_type,NEW.target_id,NEW.result,NEW.occurred_at); END`, false},
		{"trim_ignore", `CREATE TRIGGER mua_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(IGNORE); END`, true},
		{"trim_error", `CREATE TRIGGER mua_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'MUA_CONTROLLED_TRIM'); END`, true},
		{"insert_error", `CREATE TRIGGER mua_fault BEFORE INSERT ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'MUA_CONTROLLED_INSERT'); END`, false},
		{"reverse_model", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE models SET enabled=1,revision=1 WHERE id='mua-target'; END`, false},
		{"change_created", `CREATE TRIGGER mua_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE models SET created_at='2000-01-01T00:00:00Z' WHERE id='mua-target'; END`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMUAFixture(t, "")
			count := 1
			if tc.full {
				count = 1000
			}
			f.seedAudits(t, count, false)
			muaExec(t, f.db, tc.sql)
			before := f.facts(t, false)
			epoch, changed := f.app.accountPool.changeSnapshot()
			f.requireStatus(t, f.patch(t, muaTarget, `{"expected_revision":1,"enabled":false}`), 503, "storage_unavailable")
			f.notification(t, epoch, changed, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("MUA_FAULT_ROLLBACK")
			}
		})
	}
}

func TestAdminModelUpdateAuditRejections(t *testing.T) {
	for _, name := range []string{"no_session", "employee_bearer", "origin_before_csrf", "csrf", "decode", "unknown_field", "invalid_revision", "no_changes", "missing", "archived", "stale", "provider", "wire", "pool", "model_sql"} {
		t.Run(name, func(t *testing.T) {
			f := newMUAFixture(t, "")
			body, target := `{"expected_revision":1,"enabled":false}`, muaTarget
			cookie, csrf, origin, bearer := f.cookie, f.csrf, f.server.URL, ""
			status, code := 400, "invalid_request"
			switch name {
			case "no_session":
				cookie = nil
				status, code = 401, "authentication_required"
			case "employee_bearer":
				cookie = nil
				bearer = f.key
				status, code = 401, "authentication_required"
			case "origin_before_csrf":
				origin = "https://example.invalid"
				csrf = "wrong"
				status, code = 403, "origin_rejected"
			case "csrf":
				csrf = "wrong"
				status, code = 403, "csrf_rejected"
			case "decode":
				body = `{"expected_revision":`
			case "unknown_field":
				body = `{"expected_revision":1,"enabled":false,"extra":true}`
			case "invalid_revision":
				body = `{"expected_revision":0,"enabled":false}`
				code = "invalid_revision"
			case "no_changes":
				body = `{"expected_revision":1}`
			case "missing":
				target = "mua-missing"
				status, code = 404, "not_found"
			case "archived":
				muaExec(t, f.db, "UPDATE models SET archived=1,enabled=0 WHERE id=?", muaTarget)
				status, code = 409, "model_archived"
			case "stale":
				body = `{"expected_revision":2,"enabled":false}`
				status, code = 409, "revision_conflict"
			case "provider":
				body = `{"expected_revision":1,"upstream_id":"mua-missing"}`
			case "wire":
				body = `{"expected_revision":1,"wire_protocol":"unknown"}`
			case "pool":
				muaExec(t, f.db, "INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)", muaTarget, utcNow())
				muaExec(t, f.db, "INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES(?,'mua-up','fixture-model',0,1,1,0)", muaTarget)
				body = `{"expected_revision":1,"upstream_model":"next-model"}`
				status, code = 409, "model_pool_not_empty"
			case "model_sql":
				muaExec(t, f.db, `CREATE TRIGGER mua_model_fault BEFORE UPDATE ON models BEGIN SELECT RAISE(ABORT,'MUA_CONTROLLED_MODEL'); END`)
				status, code = 503, "storage_unavailable"
			}
			before := f.facts(t, false)
			epoch, changed := f.app.accountPool.changeSnapshot()
			f.requireStatus(t, f.call(t, http.MethodPatch, "/admin/api/v1/models/"+target, body, cookie, csrf, origin, bearer), status, code)
			f.notification(t, epoch, changed, false)
			if !reflect.DeepEqual(before, f.facts(t, false)) {
				t.Fatal("MUA_REJECTION_CHANGED_FACTS")
			}
		})
	}
}

// A fresh, request-owned connector; real SQLite work is delegated unchanged.
// Only a typed authenticated target request is eligible for controlled errors.
type muaRequestKey struct{}
type muaRequest struct {
	owner  *muaConnector
	target string
	cancel context.CancelFunc
}
type muaConnector struct {
	base                       driver.Driver
	dsn, mode                  string
	inject                     atomic.Bool
	begins, commits            atomic.Int64
	realCommitted, nativeError atomic.Bool
}

func (d *muaConnector) Driver() driver.Driver { return d.base }
func (d *muaConnector) Connect(ctx context.Context) (driver.Conn, error) {
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
	return &muaConn{Conn: c, owner: d}, nil
}

type muaConn struct {
	driver.Conn
	owner *muaConnector
}

func (c *muaConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	r, _ := ctx.Value(muaRequestKey{}).(*muaRequest)
	if r == nil || r.owner != c.owner || r.target != muaTarget {
		r = nil
	} else {
		c.owner.begins.Add(1)
	}
	var tx driver.Tx
	var err error
	if b, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = b.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &muaTx{Tx: tx, owner: c.owner, request: r}, nil
}

type muaTx struct {
	driver.Tx
	owner   *muaConnector
	request *muaRequest
}

func (tx *muaTx) Commit() error {
	if tx.request == nil {
		return tx.Tx.Commit()
	}
	tx.owner.commits.Add(1)
	if err := tx.Tx.Commit(); err != nil {
		tx.owner.nativeError.Store(true)
		return err
	}
	tx.owner.realCommitted.Store(true)
	if tx.owner.inject.Load() {
		switch tx.owner.mode {
		case "after_error":
			return errors.New("MUA_CONTROLLED_AFTER_COMMIT")
		case "after_cancel":
			tx.request.cancel()
			return context.Canceled
		}
	}
	return nil
}

func TestAdminModelUpdateAuditCommit(t *testing.T) {
	for _, mode := range []string{"native_positive", "native_refusal", "after_error", "after_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newMUAFixture(t, mode)
			if strings.HasPrefix(mode, "native_") {
				muaExec(t, f.db, "CREATE TABLE mua_parent(id TEXT PRIMARY KEY)")
				muaExec(t, f.db, "CREATE TABLE mua_child(id TEXT PRIMARY KEY,parent_id TEXT REFERENCES mua_parent(id) DEFERRABLE INITIALLY DEFERRED)")
				if mode == "native_positive" {
					muaExec(t, f.db, "INSERT INTO mua_parent VALUES('parent')")
				}
				muaExec(t, f.db, `CREATE TRIGGER mua_commit AFTER INSERT ON account_lifecycle_audit WHEN NEW.target_id='mua-target' BEGIN INSERT INTO mua_child VALUES(NEW.id,'parent'); END`)
				var enabled int
				if f.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&enabled) != nil || enabled != 1 {
					t.Fatal("MUA_COMMIT_FK_OFF")
				}
			}
			before, oldAudits := f.facts(t, false), f.audits(t)
			epoch, changed := f.app.accountPool.changeSnapshot()
			f.arm()
			status, code := 503, "storage_unavailable"
			if mode == "native_positive" {
				status, code = 200, ""
			}
			f.requireStatus(t, f.patch(t, muaTarget, `{"expected_revision":1,"enabled":false}`), status, code)
			f.notification(t, epoch, changed, mode == "native_positive")
			if f.control.begins.Load() != 1 || f.control.commits.Load() != 1 {
				t.Fatal("MUA_COMMIT_REQUEST_OWNERSHIP")
			}
			if mode == "native_refusal" {
				if !f.control.nativeError.Load() || f.control.realCommitted.Load() || !reflect.DeepEqual(before, f.facts(t, false)) {
					t.Fatal("MUA_NATIVE_REFUSAL_FACTS")
				}
			} else {
				if !f.control.realCommitted.Load() || f.control.nativeError.Load() || f.model(t, muaTarget).revision != 2 || f.model(t, muaTarget).enabled != 0 {
					t.Fatal("MUA_COMMITTED_PAIR")
				}
				f.requireCurrentAudit(t, oldAudits, muaTarget)
			}
			if mode == "after_error" || mode == "after_cancel" {
				// Explicit later request, never an automatic retry: old CAS conflicts.
				committed := f.facts(t, false)
				epoch, changed = f.app.accountPool.changeSnapshot()
				f.requireStatus(t, f.patch(t, muaTarget, `{"expected_revision":1,"enabled":false}`), 409, "revision_conflict")
				f.notification(t, epoch, changed, false)
				if !reflect.DeepEqual(committed, f.facts(t, false)) || f.control.commits.Load() != 1 {
					t.Fatal("MUA_UNCERTAIN_NO_REPLAY")
				}
			}
		})
	}
}
