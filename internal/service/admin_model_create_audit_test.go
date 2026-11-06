package service

import (
	"bytes"
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
	"strings"
	"sync"
	"sync/atomic"
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
		audits:  mcaRows(t, db, mcaP2AuditFactsQuery),
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

// This selector is independent of the historical 23-case old oracle.
func TestAdminModelCreateP2PrewriteOracle(t *testing.T) {
	f := newMCAFixture(t)
	if f.auditCount(t) != 0 {
		t.Fatal("MCA_P2_INITIAL_BASELINE")
	}
	mcaExec(t, f.store.db, "CREATE TRIGGER mca_p2_prewrite AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('mca-p2-prewrite-extra','mca-p2-other','model.create','model','mca-peer','succeeded','2000-01-01T00:00:00Z'); END")
	before := f.facts(t, f.store.db)
	status, body, _ := f.post(t, f.want)
	if os.Getenv("CPA_MCA_P2_PREWRITE_ORACLE") != "1" {
		mcaError(t, status, body, http.StatusServiceUnavailable, "storage_unavailable")
		if f.facts(t, f.store.db) != before {
			t.Fatal("MCA_P2_REJECTED_FACTS_CHANGED")
		}
		f.finish(t)
		return
	}
	f.checkView(t, status, body, f.want)
	var extra int
	if f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM account_lifecycle_audit WHERE id='mca-p2-prewrite-extra' AND actor_id='mca-p2-other' AND action='model.create' AND target_type='model' AND target_id='mca-peer' AND result='succeeded' AND occurred_at='2000-01-01T00:00:00Z' AND typeof(rowid)='integer' AND typeof(id)='text' AND typeof(actor_id)='text' AND typeof(action)='text' AND typeof(target_type)='text' AND typeof(target_id)='text' AND typeof(result)='text' AND typeof(occurred_at)='text'`).Scan(&extra) != nil {
		t.Fatal("MCA_P2_EXTRA_QUERY")
	}
	if extra != 1 || f.auditCount(t) != 2 || !f.matchingModel(t) || !f.currentAudit(t) || f.facts(t, f.store.db).peers != before.peers {
		t.Fatal("MCA_P2_OLD_COUNTEREXAMPLE_MISSING")
	}
	f.finish(t)
	if !f.removed {
		t.Fatal("MCA_P2_OLD_CLEANUP_INCOMPLETE")
	}
	t.Fatal("MODEL_CREATE_P2_PREWRITE_OLD_FALSE_201")
}

const mcaP2AuditFactsQuery = `SELECT typeof(rowid),rowid,typeof(id),id,typeof(actor_id),actor_id,typeof(action),action,typeof(target_type),target_type,typeof(target_id),target_id,typeof(result),result,typeof(occurred_at),occurred_at FROM account_lifecycle_audit ORDER BY rowid`

func mcaP2Insert(t *testing.T, db *sql.DB, values [7]string) {
	t.Helper()
	mcaExec(t, db, "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,?,?,?,?,?)", values[0], values[1], values[2], values[3], values[4], values[5], values[6])
}

func mcaP2SeedTotal(t *testing.T, f *mcaFixture, over bool) {
	t.Helper()
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("MCA_P2_BYTE_SEED_BEGIN")
	}
	defer tx.Rollback()
	for i := 0; i < 256; i++ {
		values := [7]string{fmt.Sprintf("mca-seed-bytes-%04d", i), f.actor, strings.Repeat("a", 4096), strings.Repeat("t", 4096), strings.Repeat("u", 4096), "", "2000-01-01T00:00:00Z"}
		fixed := 0
		for _, value := range values {
			fixed += len(value)
		}
		fill := 16384 - fixed
		if fill < 0 || fill >= 4096 {
			t.Fatal("MCA_P2_BYTE_SEED_BUDGET")
		}
		values[5] = strings.Repeat("r", fill)
		if over && i == 255 {
			values[5] += "x"
		}
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,?,?,?,?,?)", values[0], values[1], values[2], values[3], values[4], values[5], values[6]); err != nil {
			t.Fatal("MCA_P2_BYTE_SEED_INSERT")
		}
	}
	if tx.Commit() != nil {
		t.Fatal("MCA_P2_BYTE_SEED_COMMIT")
	}
}

func TestAdminModelCreateP2Positive(t *testing.T) {
	for _, name := range []string{"empty", "one", "999", "1000", "transient_event", "identical_rowid_replacement"} {
		t.Run(name, func(t *testing.T) {
			f := newMCAFixture(t)
			n := 0
			switch name {
			case "one", "identical_rowid_replacement":
				n = 1
			case "999":
				n = 999
			case "1000":
				n = 1000
			}
			f.seed(t, n, false)
			if name == "transient_event" {
				mcaExec(t, f.store.db, "CREATE TRIGGER mca_p2_positive AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN INSERT INTO account_lifecycle_audit VALUES('mca-p2-transient','other','model.create','model','mca-peer','succeeded','2000-01-01T00:00:00Z'); DELETE FROM account_lifecycle_audit WHERE id='mca-p2-transient'; END")
			}
			if name == "identical_rowid_replacement" {
				mcaExec(t, f.store.db, "CREATE TRIGGER mca_p2_positive AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id='mca-seed-0000'; INSERT INTO account_lifecycle_audit(rowid,id,actor_id,action,target_type,target_id,result,occurred_at) SELECT 1,'mca-seed-0000',id,'model.create','model','mca-target','succeeded','2000-01-01T00:00:00Z' FROM admins WHERE username='admin'; END")
			}
			priorQuery := "SELECT rowid,* FROM account_lifecycle_audit WHERE id LIKE 'mca-seed-%'"
			if n == 1000 {
				priorQuery += " AND id<>'mca-seed-0000'"
			}
			prior := mcaRows(t, f.store.db, priorQuery+" ORDER BY id")
			before := f.facts(t, f.store.db)
			status, body, _ := f.post(t, f.want)
			f.checkView(t, status, body, f.want)
			count := n + 1
			if count > 1000 {
				count = 1000
			}
			if !f.matchingModel(t) || !f.currentAudit(t) || f.auditCount(t) != count || f.facts(t, f.store.db).peers != before.peers || mcaRows(t, f.store.db, "SELECT rowid,* FROM account_lifecycle_audit WHERE id LIKE 'mca-seed-%' ORDER BY id") != prior {
				t.Fatal("MCA_P2_POSITIVE_FACTS")
			}
			f.finish(t)
		})
	}
}

func TestAdminModelCreateP2CheckpointFaults(t *testing.T) {
	cases := []struct{ name, trigger string }{
		{"model_add_seeded", "CREATE TRIGGER mca_p2_fault AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN INSERT INTO account_lifecycle_audit VALUES('mca-p2-extra','other','model.create','model','mca-peer','succeeded','2000-01-01T00:00:00Z'); END"},
		{"model_change_seed", "CREATE TRIGGER mca_p2_fault AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN UPDATE account_lifecycle_audit SET actor_id='changed' WHERE id='mca-seed-0000'; END"},
		{"model_delete_seed", "CREATE TRIGGER mca_p2_fault AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id='mca-seed-0000'; END"},
		{"model_rowid_reuse", "CREATE TRIGGER mca_p2_fault AFTER INSERT ON models WHEN NEW.id='mca-target' BEGIN DELETE FROM account_lifecycle_audit WHERE id='mca-seed-0000'; INSERT INTO account_lifecycle_audit(rowid,id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(1,'mca-p2-replacement','other','model.create','model','mca-peer','succeeded','2000-01-01T00:00:00Z'); END"},
		{"audit_remove_seed", "DELETE FROM account_lifecycle_audit WHERE id='mca-seed-0000';"},
		{"audit_change_seed", "UPDATE account_lifecycle_audit SET result='changed' WHERE id='mca-seed-0000';"},
		{"audit_rowid_reuse", "DELETE FROM account_lifecycle_audit WHERE id='mca-seed-0000'; INSERT INTO account_lifecycle_audit(rowid,id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(1,'mca-p2-replacement','other','model.create','model','mca-peer','succeeded','2000-01-01T00:00:00Z');"},
		{"audit_old_extra_trimmed", "INSERT INTO account_lifecycle_audit VALUES('mca-p2-old-extra','other','model.create','model','mca-peer','succeeded','1900-01-01T00:00:00Z');"},
		{"trim_change_retained", "CREATE TRIGGER mca_p2_fault BEFORE DELETE ON account_lifecycle_audit WHEN OLD.id='mca-seed-0000' BEGIN UPDATE account_lifecycle_audit SET actor_id='changed' WHERE id='mca-seed-0001'; END"},
		{"trim_add_old", "CREATE TRIGGER mca_p2_fault BEFORE DELETE ON account_lifecycle_audit WHEN OLD.id='mca-seed-0000' BEGIN INSERT INTO account_lifecycle_audit VALUES('mca-p2-trim-extra','other','model.create','model','mca-peer','succeeded','1900-01-01T00:00:00Z'); END"},
		{"audit_blob_seed", "UPDATE account_lifecycle_audit SET actor_id=CAST(actor_id AS BLOB) WHERE id='mca-seed-0000';"},
		{"baseline_overflow", ""},
		{"baseline_overflow_duplicate", ""},
		{"baseline_blob", ""},
		{"baseline_field_oversize", ""},
		{"baseline_total_oversize", ""},
		{"pretrim_total_overflow", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMCAFixture(t)
			n := 1
			switch tc.name {
			case "audit_old_extra_trimmed", "trim_change_retained", "trim_add_old":
				n = 1000
			case "baseline_overflow", "baseline_overflow_duplicate":
				n = 1001
			case "baseline_total_oversize", "pretrim_total_overflow":
				n = 0
			}
			f.seed(t, n, false)
			if tc.name == "baseline_total_oversize" || tc.name == "pretrim_total_overflow" {
				mcaP2SeedTotal(t, f, tc.name == "baseline_total_oversize")
			}
			if tc.name == "baseline_blob" {
				mcaExec(t, f.store.db, "UPDATE account_lifecycle_audit SET actor_id=CAST(actor_id AS BLOB)")
			}
			if tc.name == "baseline_field_oversize" {
				mcaExec(t, f.store.db, "UPDATE account_lifecycle_audit SET action=?", strings.Repeat("x", 4097))
			}
			if strings.Contains(tc.name, "rowid_reuse") {
				var rowid int64
				if f.store.db.QueryRowContext(t.Context(), "SELECT rowid FROM account_lifecycle_audit WHERE id='mca-seed-0000'").Scan(&rowid) != nil || rowid != 1 {
					t.Fatal("MCA_P2_ROWID_FIXTURE")
				}
			}
			if tc.trigger != "" {
				trigger := tc.trigger
				if !strings.HasPrefix(trigger, "CREATE TRIGGER") {
					trigger = "CREATE TRIGGER mca_p2_fault AFTER INSERT ON account_lifecycle_audit WHEN NEW.target_id='mca-target' AND NEW.id NOT LIKE 'mca-seed-%' BEGIN " + trigger + " END"
				}
				mcaExec(t, f.store.db, trigger)
			}
			before := f.facts(t, f.store.db)
			input := f.want
			if tc.name == "baseline_overflow_duplicate" {
				input.ID = "mca-peer"
			}
			status, body, _ := f.post(t, input)
			mcaError(t, status, body, http.StatusServiceUnavailable, "storage_unavailable")
			if f.facts(t, f.store.db) != before {
				t.Fatal("MCA_P2_CHECKPOINT_ROLLBACK_FACTS")
			}
			f.finish(t)
		})
	}
}

func TestAdminModelCreateP2ReaderBounds(t *testing.T) {
	columns := [7]string{"id", "actor_id", "action", "target_type", "target_id", "result", "occurred_at"}
	type boundsCase struct {
		name, mode                 string
		field, rows, cap, wantRows int
		top, wantError             bool
	}
	cases := []boundsCase{
		{name: "full_1000", rows: 1000, cap: 1000, wantRows: 1000},
		{name: "full_1001_overflow", rows: 1001, cap: 1000, wantError: true},
		{name: "pretrim_1001", rows: 1001, cap: 1001, wantRows: 1001},
		{name: "pretrim_1002_overflow", rows: 1002, cap: 1001, wantError: true},
	}
	for i, column := range columns {
		cases = append(cases,
			boundsCase{name: column + "_at_limit", mode: "at", field: i, cap: 1000, wantRows: 1},
			boundsCase{name: column + "_over_limit", mode: "over", field: i, cap: 1000, wantError: true},
			boundsCase{name: column + "_blob", mode: "blob", field: i, cap: 1000, wantError: true})
	}
	cases = append(cases,
		boundsCase{name: "id_null", mode: "null", cap: 1000, wantError: true},
		boundsCase{name: "total_at_limit", mode: "total", cap: 1000, wantRows: 256},
		boundsCase{name: "total_over_limit", mode: "total_over", cap: 1000, wantError: true},
		boundsCase{name: "native_top_order", mode: "order", rows: 1001, cap: 1000, top: true, wantRows: 1000})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newMCAFixture(t)
			if tc.rows != 0 {
				f.seed(t, tc.rows, false)
			}
			switch tc.mode {
			case "at", "over", "blob", "null":
				values := [7]string{"mca-p2-bound", f.actor, "model.create", "model", "mca-peer", "succeeded", "2000-01-01T00:00:00Z"}
				if tc.mode == "at" || tc.mode == "over" {
					size := 4096
					if tc.mode == "over" {
						size++
					}
					values[tc.field] = strings.Repeat("x", size)
				}
				mcaP2Insert(t, f.store.db, values)
				if tc.mode == "blob" {
					mcaExec(t, f.store.db, "UPDATE account_lifecycle_audit SET "+columns[tc.field]+"=CAST("+columns[tc.field]+" AS BLOB)")
				}
				if tc.mode == "null" {
					mcaExec(t, f.store.db, "UPDATE account_lifecycle_audit SET id=NULL")
				}
			case "total", "total_over":
				mcaP2SeedTotal(t, f, tc.mode == "total_over")
			case "order":
				for _, pair := range [][2]string{{"mca-seed-0000", "000-smallest"}, {"mca-seed-1000", "Ω-max"}, {"mca-seed-0999", "a-tie"}, {"mca-seed-0998", "Z-tie"}} {
					mcaExec(t, f.store.db, "UPDATE account_lifecycle_audit SET id=? WHERE id=?", pair[1], pair[0])
				}
			}
			before := f.facts(t, f.store.db)
			tx, err := f.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal("MCA_P2_READER_BEGIN")
			}
			snapshot, readErr := readModelCreateAuditSnapshot(t.Context(), tx, tc.cap, tc.top)
			if tx.Rollback() != nil {
				t.Fatal("MCA_P2_READER_ROLLBACK")
			}
			if tc.wantError {
				if readErr == nil || snapshot != nil {
					t.Fatal("MCA_P2_READER_ACCEPTED_INVALID")
				}
			} else {
				if readErr != nil || len(snapshot) != tc.wantRows {
					t.Fatal("MCA_P2_READER_REJECTED_VALID")
				}
				for _, row := range snapshot {
					if row.RowIDType != "integer" || row.Types != [7]string{"text", "text", "text", "text", "text", "text", "text"} {
						t.Fatal("MCA_P2_READER_TYPES")
					}
				}
				if tc.top {
					a, z := -1, -1
					for i, row := range snapshot {
						if row.Values[0] == "000-smallest" {
							t.Fatal("MCA_P2_TOP_INCLUDED_OLDEST_ID")
						}
						if row.Values[0] == "a-tie" {
							a = i
						}
						if row.Values[0] == "Z-tie" {
							z = i
						}
					}
					if snapshot[0].Values[0] != "Ω-max" || a < 0 || z < 0 || a >= z {
						t.Fatal("MCA_P2_NATIVE_BINARY_ORDER")
					}
				}
			}
			if f.facts(t, f.store.db) != before {
				t.Fatal("MCA_P2_READER_WROTE_FACTS")
			}
			f.finish(t)
		})
	}
}

var mcaP2ReaderBoundary = errors.New("mca-p2-controlled-reader-boundary")

type mcaP2ReaderFault struct {
	base       driver.Driver
	target     int64
	onClose    bool
	queries    atomic.Int64
	injected   atomic.Bool
	unexpected atomic.Bool
}

func (d *mcaP2ReaderFault) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &mcaP2ReaderConn{Conn: c, owner: d}, nil
}

type mcaP2ReaderConn struct {
	driver.Conn
	owner *mcaP2ReaderFault
}

func (c *mcaP2ReaderConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		return begin.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *mcaP2ReaderConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := q.QueryContext(ctx, query, args)
	if err != nil {
		c.owner.unexpected.Store(true)
		return nil, err
	}
	if strings.HasPrefix(query, "SELECT typeof(rowid),rowid") && strings.Contains(query, "FROM account_lifecycle_audit") && c.owner.queries.Add(1) == c.owner.target {
		return &mcaP2ReaderRows{Rows: rows, owner: c.owner}, nil
	}
	return rows, nil
}

type mcaP2ReaderRows struct {
	driver.Rows
	owner *mcaP2ReaderFault
}

func (r *mcaP2ReaderRows) Next(values []driver.Value) error {
	if !r.owner.onClose {
		r.owner.injected.Store(true)
		return mcaP2ReaderBoundary
	}
	err := r.Rows.Next(values)
	if err != nil && err != io.EOF {
		r.owner.unexpected.Store(true)
	}
	return err
}

func (r *mcaP2ReaderRows) Close() error {
	err := r.Rows.Close()
	if err != nil {
		r.owner.unexpected.Store(true)
	}
	if r.owner.onClose {
		r.owner.injected.Store(true)
		return errors.Join(err, mcaP2ReaderBoundary)
	}
	return err
}

func TestAdminModelCreateP2ReaderErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target int64
		close  bool
	}{{"metadata_next", 1, false}, {"metadata_close", 1, true}, {"values_next", 2, false}, {"values_close", 2, true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bootstrap, err := openStore(dir)
			if err != nil {
				t.Fatal("MCA_P2_ERROR_BOOTSTRAP")
			}
			t.Cleanup(func() {
				if bootstrap.close() != nil {
					t.Error("MCA_P2_ERROR_BOOTSTRAP_CLOSE")
				}
			})
			mcaP2Insert(t, bootstrap.db, [7]string{"mca-p2-unit", "actor", "model.create", "model", "target", "succeeded", "2000-01-01T00:00:00Z"})
			before := mcaRows(t, bootstrap.db, mcaP2AuditFactsQuery)
			owner := &mcaP2ReaderFault{base: bootstrap.db.Driver(), target: tc.target, onClose: tc.close}
			if bootstrap.close() != nil {
				t.Fatal("MCA_P2_ERROR_BOOTSTRAP_CLOSE")
			}
			name := "mca-p2-reader-" + tc.name
			sql.Register(name, owner)
			db, err := sql.Open(name, filepath.Join(dir, "cpa-cloud.db"))
			if err != nil {
				t.Fatal("MCA_P2_ERROR_OPEN")
			}
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			t.Cleanup(func() {
				if db.Close() != nil {
					t.Error("MCA_P2_ERROR_DB_CLOSE")
				}
			})
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal("MCA_P2_ERROR_BEGIN")
			}
			snapshot, readErr := readModelCreateAuditSnapshot(t.Context(), tx, 1000, false)
			if tx.Rollback() != nil {
				t.Fatal("MCA_P2_ERROR_ROLLBACK")
			}
			if snapshot != nil || !errors.Is(readErr, mcaP2ReaderBoundary) || !owner.injected.Load() || owner.unexpected.Load() {
				t.Fatal("MCA_P2_CONTROLLED_READER_ERROR")
			}
			if db.Close() != nil {
				t.Fatal("MCA_P2_ERROR_DB_CLOSE")
			}
			ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "cpa-cloud.db"))+"?mode=ro")
			if err != nil {
				t.Fatal("MCA_P2_ERROR_READONLY_OPEN")
			}
			t.Cleanup(func() {
				if ro.Close() != nil {
					t.Error("MCA_P2_ERROR_READONLY_CLOSE")
				}
			})
			if mcaRows(t, ro, mcaP2AuditFactsQuery) != before {
				t.Fatal("MCA_P2_ERROR_PERSISTENT_FACTS")
			}
			if ro.Close() != nil {
				t.Fatal("MCA_P2_ERROR_READONLY_CLOSE")
			}
			rel, err := filepath.Rel(os.TempDir(), dir)
			if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
				t.Fatal("MCA_P2_ERROR_REMOVE_SCOPE")
			}
			if os.RemoveAll(dir) != nil {
				t.Fatal("MCA_P2_ERROR_REMOVE")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("MCA_P2_ERROR_DIRECTORY_REMAINS")
			}
		})
	}
}
