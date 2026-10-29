package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const mcaTarget = "mca-target"

type mcaFixture struct {
	dir       string
	store     *store
	app       *App
	server    *httptest.Server
	transport *http.Transport
	client    *http.Client
	cookie    *http.Cookie
	csrf      string
	actor     string
	finished  chan struct{}
	handlers  sync.WaitGroup
	cancel    context.CancelFunc
	changed   context.Context
	closed    bool
	removed   bool
	want      mcaInput
}

type mcaInput struct {
	ID    string `json:"id"`
	Kind  string `json:"model_kind,omitempty"`
	Up    string `json:"upstream_id"`
	Model string `json:"upstream_model"`
	Wire  string `json:"wire_protocol,omitempty"`
}

type mcaFacts struct {
	model, audits, witness, peers string
}

func mcaExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal("MCA_FIXTURE_SQL")
	}
}

func mcaRows(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query, args...)
	if err != nil {
		t.Fatal("MCA_FACT_QUERY")
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		t.Fatal("MCA_FACT_COLUMNS")
	}
	result := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if rows.Scan(pointers...) != nil {
			_ = rows.Close()
			t.Fatal("MCA_FACT_SCAN")
		}
		for i, value := range values {
			if raw, ok := value.([]byte); ok {
				values[i] = append([]byte(nil), raw...)
			}
		}
		result = append(result, values)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatal("MCA_FACT_ROWS_CLOSE")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal("MCA_FACT_ENCODING")
	}
	return string(encoded)
}

func (f *mcaFixture) facts(t *testing.T, db *sql.DB) mcaFacts {
	t.Helper()
	peers := mcaRows(t, db, "SELECT * FROM models WHERE id<>? ORDER BY id", mcaTarget)
	for _, table := range []string{"upstreams", "employees", "employee_models", "access_keys"} {
		peers += "\n" + mcaRows(t, db, "SELECT * FROM "+table+" ORDER BY rowid")
	}
	return mcaFacts{
		model:   mcaRows(t, db, "SELECT * FROM models WHERE id=?", mcaTarget),
		audits:  mcaRows(t, db, "SELECT * FROM account_lifecycle_audit ORDER BY id"),
		witness: mcaRows(t, db, "SELECT * FROM mca_witness ORDER BY rowid"),
		peers:   peers,
	}
}

func newMCAFixture(t *testing.T) *mcaFixture {
	t.Helper()
	f := &mcaFixture{dir: t.TempDir(), finished: make(chan struct{}, 1), want: mcaInput{
		ID: mcaTarget, Up: "mca-up", Model: "fixture-model",
	}}
	t.Cleanup(func() { f.release(t) })
	if Initialize(t.Context(), f.dir, strings.NewReader("a-strong-preview-password\n")) != nil {
		t.Fatal("MCA_INITIALIZE")
	}
	s, err := openStore(f.dir)
	if err != nil {
		t.Fatal("MCA_OPEN_STORE")
	}
	f.store = s
	cfg := Config{DataDir: f.dir, Listen: "127.0.0.1:0", Version: "model-create-audit-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("MCA_SECRETS")
	}
	if s.db.QueryRowContext(t.Context(), "SELECT id FROM admins WHERE username='admin'").Scan(&f.actor) != nil {
		t.Fatal("MCA_ACTOR")
	}
	stamp := utcNow()
	for _, id := range []string{"mca-up", "mca-up2", "mca-disabled", "mca-archived"} {
		mcaExec(t, s.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,'Fixture','openai-compatible','https://example.invalid',1,?,1,1,?)", id, []byte("synthetic-opaque"), stamp)
	}
	mcaExec(t, s.db, "UPDATE upstreams SET enabled=0 WHERE id='mca-disabled'")
	mcaExec(t, s.db, "UPDATE upstreams SET archived=1,archived_at=? WHERE id='mca-archived'", stamp)
	mcaExec(t, s.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('mca-peer','mca-up','peer-model',1,?)", stamp)
	mcaExec(t, s.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('mca-employee','Fixture','active','selected',1,?)", stamp)
	mcaExec(t, s.db, "INSERT INTO employee_models(employee_id,model_id) VALUES('mca-employee','mca-peer')")
	selector, secret := "mca-selector", strings.Repeat("k", 43)
	mcaExec(t, s.db, "INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('mca-key','mca-employee','Fixture',?,?,1,'mca-key-operation',?)", selector, sec.digest("employee-key/v1\x00"+selector, secret), stamp)
	mcaExec(t, s.db, "CREATE TABLE mca_witness(model_time TEXT,audit_id TEXT,audit_time TEXT)")
	mcaExec(t, s.db, "CREATE TRIGGER mca_capture_model AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN INSERT INTO mca_witness(model_time) VALUES(NEW.created_at); END")
	mcaExec(t, s.db, "CREATE TRIGGER mca_capture_audit AFTER INSERT ON account_lifecycle_audit WHEN NEW.target_id='mca-target' AND NEW.id NOT LIKE 'mca-seed-%' BEGIN UPDATE mca_witness SET audit_id=NEW.id,audit_time=NEW.occurred_at; END")

	// The store is fixed before App and HTTP publication. No worker is started.
	app := &App{cfg: cfg, store: s, secrets: sec, logins: make(map[string]*loginAttempt)}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	f.cancel = func() { changeCancel(); runtimeCancel() }
	app.accountPool = &accountPoolRuntime{app: app, ctx: runtimeCtx, cancel: runtimeCancel,
		changeContext: changeCtx, changeCancel: changeCancel,
		active: make(map[*accountPoolLease]struct{}), maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	f.app = app
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	mux.HandleFunc("POST /admin/api/v1/models", app.requireAdmin(app.createModel, true))
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.handlers.Add(1)
		defer f.handlers.Done()
		if r.URL.Path == "/admin/api/v1/models" {
			defer func() { f.finished <- struct{}{} }()
		}
		mux.ServeHTTP(w, r)
	})))
	f.transport = &http.Transport{}
	f.client = &http.Client{Transport: f.transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status, body, header := f.call(t, "/admin/api/v1/sessions", `{"username":"admin","password":"a-strong-preview-password"}`, nil, "", f.server.URL)
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &login) != nil || login.CSRF == "" {
		t.Fatal("MCA_REAL_LOGIN")
	}
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		if cookie.Name == adminCookieName {
			f.cookie = cookie
		}
	}
	if f.cookie == nil || f.cookie.Value == "" {
		t.Fatal("MCA_ADMIN_COOKIE")
	}
	f.csrf = login.CSRF
	_, f.changed = app.accountPool.changeSnapshot()
	control := mcaInput{ID: "mca-control", Up: "mca-up", Model: "control-model"}
	status, body, _ = f.post(t, control)
	f.checkView(t, status, body, control)
	var modelCount, auditCount int
	if s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM models WHERE id='mca-control' AND upstream_id='mca-up' AND upstream_model='control-model' AND enabled=1 AND revision=1 AND archived=0 AND archived_at IS NULL").Scan(&modelCount) != nil ||
		s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM account_lifecycle_audit WHERE actor_id=? AND action='model.create' AND target_type='model' AND target_id='mca-control' AND result='succeeded'", f.actor).Scan(&auditCount) != nil || modelCount != 1 || auditCount != 1 {
		t.Fatal("MCA_POSITIVE_CONTROL_FACTS")
	}
	f.unnotified(t)
	mcaExec(t, s.db, "DELETE FROM account_lifecycle_audit")
	return f
}

func (f *mcaFixture) call(t *testing.T, path, body string, cookie *http.Cookie, csrf, origin string) (int, []byte, http.Header) {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal("MCA_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal("MCA_HTTP")
	}
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("MCA_RESPONSE_CLOSE")
	}
	if path == "/admin/api/v1/models" {
		select {
		case <-f.finished:
		case <-time.After(5 * time.Second):
			t.Fatal("MCA_HANDLER_COMPLETION")
		}
	}
	return response.StatusCode, payload, response.Header.Clone()
}

func mcaBody(t *testing.T, input mcaInput) string {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal("MCA_REQUEST_ENCODING")
	}
	return string(encoded)
}

func (f *mcaFixture) post(t *testing.T, input mcaInput) (int, []byte, http.Header) {
	t.Helper()
	return f.call(t, "/admin/api/v1/models", mcaBody(t, input), f.cookie, f.csrf, f.server.URL)
}

func (f *mcaFixture) checkView(t *testing.T, status int, body []byte, input mcaInput) {
	t.Helper()
	var view struct {
		ID         string  `json:"id"`
		Kind       string  `json:"model_kind"`
		Up         string  `json:"upstream_id"`
		Model      string  `json:"upstream_model"`
		Wire       string  `json:"wire_protocol"`
		Enabled    bool    `json:"enabled"`
		Revision   int64   `json:"revision"`
		Archived   bool    `json:"archived"`
		ArchivedAt *string `json:"archived_at"`
	}
	kind, wire := input.Kind, input.Wire
	if kind == "" {
		kind = "generation"
	}
	if wire == "" {
		wire = "legacy-native"
	}
	if status != http.StatusCreated || json.Unmarshal(body, &view) != nil || view.ID != input.ID || view.Kind != kind || view.Up != input.Up || view.Model != input.Model || view.Wire != wire || !view.Enabled || view.Revision != 1 || view.Archived || view.ArchivedAt != nil {
		t.Fatal("MCA_CREATED_VIEW")
	}
}

func mcaError(t *testing.T, status int, body []byte, expected int, code string) {
	t.Helper()
	encoded, err := json.Marshal(code)
	if err != nil || status != expected || !json.Valid(body) || !bytes.Contains(body, encoded) {
		t.Fatal("MCA_ERROR_RESPONSE")
	}
}

func (f *mcaFixture) unnotified(t *testing.T) {
	t.Helper()
	_, changed := f.app.accountPool.changeSnapshot()
	if changed != f.changed || f.app.store != f.store || f.app.channelMonitors != nil {
		t.Fatal("MCA_NO_NOTIFICATION_FIXED_STORE")
	}
	select {
	case <-f.changed.Done():
		t.Fatal("MCA_UNEXPECTED_CANCELLATION")
	default:
	}
}

func (f *mcaFixture) release(t *testing.T) {
	t.Helper()
	if f.closed {
		return
	}
	f.closed = true
	if f.transport != nil {
		f.transport.CloseIdleConnections()
	}
	if f.server != nil {
		f.server.Close()
		f.handlers.Wait()
	}
	if f.cancel != nil {
		f.cancel()
	}
	if f.store != nil {
		if f.store.close() != nil {
			t.Error("MCA_STORE_CLOSE")
		}
		if f.store.db.PingContext(t.Context()) == nil {
			t.Error("MCA_STORE_STILL_OPEN")
		}
	}
}

func (f *mcaFixture) finish(t *testing.T) {
	t.Helper()
	f.unnotified(t)
	want := f.facts(t, f.store.db)
	f.release(t)
	if t.Failed() {
		t.Fatal("MCA_OWNER_CLEANUP")
	}
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(f.dir, "cpa-cloud.db"))+"?mode=ro")
	if err != nil {
		t.Fatal("MCA_READONLY_OPEN")
	}
	ro.SetMaxOpenConns(1)
	ro.SetMaxIdleConns(1)
	if ro.PingContext(t.Context()) != nil {
		_ = ro.Close()
		t.Fatal("MCA_READONLY_PING")
	}
	mcaExec(t, ro, "PRAGMA query_only=ON")
	got := f.facts(t, ro)
	if ro.Close() != nil {
		t.Fatal("MCA_READONLY_CLOSE")
	}
	if got != want {
		t.Fatal("MCA_REOPEN_FACTS")
	}
	rel, err := filepath.Rel(os.TempDir(), f.dir)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		t.Fatal("MCA_CLEANUP_SCOPE")
	}
	if os.RemoveAll(f.dir) != nil {
		t.Fatal("MCA_FIXTURE_REMOVE")
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("MCA_FIXTURE_REMAINS")
	}
	f.removed = true
}

func (f *mcaFixture) seed(t *testing.T, count int, future bool) {
	t.Helper()
	stamp := "2000-01-01T00:00:00Z"
	if future {
		stamp = "2999-01-01T00:00:00Z"
	}
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("MCA_SEED_BEGIN")
	}
	defer tx.Rollback()
	for i := 0; i < count; i++ {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'model.create','model',?,'succeeded',?)", fmt.Sprintf("mca-seed-%04d", i), f.actor, mcaTarget, stamp); err != nil {
			t.Fatal("MCA_SEED_INSERT")
		}
	}
	if tx.Commit() != nil {
		t.Fatal("MCA_SEED_COMMIT")
	}
}

func (f *mcaFixture) matchingModel(t *testing.T) bool {
	t.Helper()
	var modelTime sql.NullString
	err := f.store.db.QueryRowContext(t.Context(), "SELECT model_time FROM mca_witness").Scan(&modelTime)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal("MCA_MODEL_WITNESS_QUERY")
	}
	if !modelTime.Valid {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, modelTime.String); err != nil {
		t.Fatal("MCA_MODEL_TIMESTAMP")
	}
	kind, wire := f.want.Kind, f.want.Wire
	if kind == "" {
		kind = "generation"
	}
	if wire == "" {
		wire = "legacy-native"
	}
	var count int
	if f.store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM models WHERE id=? AND model_kind=? AND upstream_id=? AND upstream_model=? AND wire_protocol=? AND enabled=1 AND revision=1 AND archived=0 AND archived_at IS NULL AND created_at=?", mcaTarget, kind, f.want.Up, f.want.Model, wire, modelTime.String).Scan(&count) != nil {
		t.Fatal("MCA_MODEL_MATCH_QUERY")
	}
	return count == 1
}

func (f *mcaFixture) currentAudit(t *testing.T) bool {
	t.Helper()
	var id, stamp sql.NullString
	err := f.store.db.QueryRowContext(t.Context(), "SELECT audit_id,audit_time FROM mca_witness").Scan(&id, &stamp)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatal("MCA_AUDIT_WITNESS_QUERY")
	}
	if !id.Valid || !stamp.Valid {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, stamp.String); err != nil {
		t.Fatal("MCA_AUDIT_TIMESTAMP")
	}
	var count int
	if f.store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM account_lifecycle_audit WHERE id=? AND actor_id=? AND action='model.create' AND target_type='model' AND target_id=? AND result='succeeded' AND occurred_at=?", id.String, f.actor, mcaTarget, stamp.String).Scan(&count) != nil {
		t.Fatal("MCA_AUDIT_MATCH_QUERY")
	}
	return count == 1
}

func (f *mcaFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	if f.store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM account_lifecycle_audit").Scan(&count) != nil {
		t.Fatal("MCA_AUDIT_COUNT")
	}
	return count
}

func (f *mcaFixture) priorAudits(t *testing.T) string {
	t.Helper()
	return mcaRows(t, f.store.db, "SELECT * FROM account_lifecycle_audit WHERE id LIKE 'mca-seed-%' ORDER BY id")
}

func TestAdminModelCreateAuditPositive(t *testing.T) {
	for _, name := range []string{"generation", "embedding", "disabled_upstream", "retention"} {
		t.Run(name, func(t *testing.T) {
			f := newMCAFixture(t)
			if name == "embedding" {
				f.want.Kind = "embedding"
			}
			if name == "disabled_upstream" {
				f.want.Up = "mca-disabled"
			}
			if name == "retention" {
				f.seed(t, 1000, false)
			}
			before := f.facts(t, f.store.db)
			status, body, _ := f.post(t, f.want)
			f.checkView(t, status, body, f.want)
			if !f.matchingModel(t) || !f.currentAudit(t) || f.facts(t, f.store.db).peers != before.peers {
				t.Fatal("MCA_POSITIVE_FACTS")
			}
			count := 1
			if name == "retention" {
				count = 1000
				var oldest int
				if f.store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM account_lifecycle_audit WHERE id='mca-seed-0000'").Scan(&oldest) != nil || oldest != 0 {
					t.Fatal("MCA_RETENTION_OLDEST")
				}
			}
			if f.auditCount(t) != count {
				t.Fatal("MCA_POSITIVE_AUDIT_COUNT")
			}
			f.finish(t)
		})
	}
}

func TestAdminModelCreateAuditFaults(t *testing.T) {
	cases := []struct{ name, trigger string }{
		{"model_ignore", "CREATE TRIGGER mca_fault BEFORE INSERT ON models WHEN NEW.id='mca-target' BEGIN SELECT RAISE(IGNORE); END"},
		{"audit_ignore", "CREATE TRIGGER mca_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.target_id='mca-target' AND NEW.id NOT LIKE 'mca-seed-%' BEGIN SELECT RAISE(IGNORE); END"},
		{"future_window", ""},
		{"audit_delete", "DELETE FROM account_lifecycle_audit WHERE id=NEW.id;"},
		{"audit_actor", "UPDATE account_lifecycle_audit SET actor_id='mca-other' WHERE id=NEW.id;"},
		{"audit_action", "UPDATE account_lifecycle_audit SET action='model.archive' WHERE id=NEW.id;"},
		{"audit_type", "UPDATE account_lifecycle_audit SET target_type='upstream' WHERE id=NEW.id;"},
		{"audit_target", "UPDATE account_lifecycle_audit SET target_id='mca-other' WHERE id=NEW.id;"},
		{"audit_result", "UPDATE account_lifecycle_audit SET result='failed' WHERE id=NEW.id;"},
		{"audit_time", "UPDATE account_lifecycle_audit SET occurred_at='2000-01-01T00:00:00Z' WHERE id=NEW.id;"},
		{"audit_id", "UPDATE account_lifecycle_audit SET id='mca-substitute' WHERE id=NEW.id;"},
		{"audit_extra", "INSERT INTO account_lifecycle_audit VALUES('mca-extra','mca-other','model.create','model','mca-other','succeeded','2000-01-01T00:00:00Z');"},
		{"model_delete", "DELETE FROM models WHERE id='mca-target';"},
		{"model_kind", "UPDATE models SET model_kind='embedding' WHERE id='mca-target';"},
		{"model_upstream", "UPDATE models SET upstream_id='mca-up2' WHERE id='mca-target';"},
		{"model_name", "UPDATE models SET upstream_model='other-model' WHERE id='mca-target';"},
		{"model_wire", "UPDATE models SET wire_protocol='openai-chat' WHERE id='mca-target';"},
		{"model_enabled", "UPDATE models SET enabled=0 WHERE id='mca-target';"},
		{"model_revision", "UPDATE models SET revision=2 WHERE id='mca-target';"},
		{"model_archived", "UPDATE models SET archived=1 WHERE id='mca-target';"},
		{"model_archived_at", "UPDATE models SET archived_at='2000-01-01T00:00:00Z' WHERE id='mca-target';"},
		{"model_created_at", "UPDATE models SET created_at='2000-01-01T00:00:00Z' WHERE id='mca-target';"},
		{"trim_ignore", "CREATE TRIGGER mca_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(IGNORE); END"},
	}
	old := os.Getenv("CPA_MCA_OLD_ORACLE") == "1"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMCAFixture(t)
			count := 1
			if tc.name == "future_window" || tc.name == "trim_ignore" {
				count = 1000
			}
			f.seed(t, count, tc.name == "future_window")
			if tc.trigger != "" {
				trigger := tc.trigger
				if !strings.HasPrefix(trigger, "CREATE TRIGGER") {
					trigger = "CREATE TRIGGER mca_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.target_id='mca-target' AND NEW.id NOT LIKE 'mca-seed-%' BEGIN " + trigger + " END"
				}
				mcaExec(t, f.store.db, trigger)
			}
			before, prior := f.facts(t, f.store.db), f.priorAudits(t)
			status, body, _ := f.post(t, f.want)
			if !old {
				mcaError(t, status, body, http.StatusServiceUnavailable, "storage_unavailable")
				if f.facts(t, f.store.db) != before {
					t.Fatal("MCA_REJECTED_TRANSACTION_CHANGED")
				}
				f.finish(t)
				return
			}
			f.checkView(t, status, body, f.want)
			if f.facts(t, f.store.db).peers != before.peers || f.priorAudits(t) != prior {
				t.Fatal("MCA_OLD_PEER_PRESERVATION")
			}
			modelOK, auditOK := f.matchingModel(t), f.currentAudit(t)
			witness := false
			switch tc.name {
			case "model_ignore", "model_delete":
				var actual int
				if f.store.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM models WHERE id=?", mcaTarget).Scan(&actual) != nil {
					t.Fatal("MCA_OLD_MODEL_COUNT")
				}
				witness = actual == 0
			case "trim_ignore":
				witness = modelOK && auditOK && f.auditCount(t) == 1001
			case "audit_extra":
				witness = modelOK && auditOK && f.auditCount(t) == 3
			default:
				if strings.HasPrefix(tc.name, "model_") {
					witness = !modelOK && auditOK
				} else {
					witness = modelOK && !auditOK
				}
			}
			if tc.name == "future_window" && f.auditCount(t) != 1000 {
				witness = false
			}
			if !witness {
				t.Fatal("MCA_OLD_COUNTEREXAMPLE_MISSING")
			}
			f.finish(t)
			if !f.removed {
				t.Fatal("MCA_OLD_CLEANUP_INCOMPLETE")
			}
			t.Fatal("MODEL_CREATE_AUDIT_OLD_FALSE_201")
		})
	}
}

func TestAdminModelCreateAuditRejections(t *testing.T) {
	for _, name := range []string{"no_session", "origin", "csrf", "json", "absent_upstream", "missing_upstream", "archived_upstream", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			f := newMCAFixture(t)
			cookie, csrf, origin := f.cookie, f.csrf, f.server.URL
			expected, code := http.StatusBadRequest, "invalid_request"
			input := f.want
			switch name {
			case "no_session":
				cookie, expected, code = nil, http.StatusUnauthorized, "authentication_required"
			case "origin":
				origin, expected, code = "https://example.invalid", http.StatusForbidden, "origin_rejected"
			case "csrf":
				csrf, expected, code = "mca-invalid", http.StatusForbidden, "csrf_rejected"
			case "absent_upstream":
				input.Up = ""
			case "missing_upstream":
				input.Up = "mca-missing"
			case "archived_upstream":
				input.Up = "mca-archived"
			case "duplicate":
				input.ID, expected, code = "mca-peer", http.StatusConflict, "already_exists"
			}
			body := mcaBody(t, input)
			if name == "json" {
				body = "{"
			}
			before := f.facts(t, f.store.db)
			status, payload, _ := f.call(t, "/admin/api/v1/models", body, cookie, csrf, origin)
			mcaError(t, status, payload, expected, code)
			if f.facts(t, f.store.db) != before {
				t.Fatal("MCA_ADMISSION_CHANGED_FACTS")
			}
			f.finish(t)
		})
	}
}

func TestAdminModelCreateAuditStorageErrors(t *testing.T) {
	for _, name := range []string{"audit_insert_abort", "trim_abort"} {
		t.Run(name, func(t *testing.T) {
			f := newMCAFixture(t)
			if name == "audit_insert_abort" {
				mcaExec(t, f.store.db, "CREATE TRIGGER mca_fault BEFORE INSERT ON account_lifecycle_audit WHEN NEW.target_id='mca-target' BEGIN SELECT RAISE(ABORT,'mca-fixed-abort'); END")
			} else {
				f.seed(t, 1000, false)
				mcaExec(t, f.store.db, "CREATE TRIGGER mca_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'mca-fixed-abort'); END")
			}
			before := f.facts(t, f.store.db)
			status, body, _ := f.post(t, f.want)
			mcaError(t, status, body, http.StatusServiceUnavailable, "storage_unavailable")
			if f.facts(t, f.store.db) != before {
				t.Fatal("MCA_ABORT_CHANGED_FACTS")
			}
			f.finish(t)
		})
	}
}
