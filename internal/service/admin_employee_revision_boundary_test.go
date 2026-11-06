// Independently authored from docs/admin-employee-revision-boundary-contract.md.
// Owned synthetic SQLite/HTTP only; no reference implementation or real secrets.
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
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const employeeRevisionTarget = "emp-erb-target"

type employeeRevisionAccess struct {
	cookie               *http.Cookie
	csrf, origin, bearer string
}

type employeeRevisionReply struct {
	status int
	header http.Header
	body   []byte
}

type employeeRevisionFixture struct {
	dir            string
	app            *App
	ownedStore     *store // sole owner until the complete App/runtime handoff
	db             *sql.DB
	server         *httptest.Server
	client         *http.Client
	cookie         *http.Cookie
	csrf           string
	runtimeContext context.Context
	handlers       sync.WaitGroup
	finished       chan struct{}
	closing        atomic.Bool
	releaseOnce    sync.Once
	ownerCloses    int
}

func newEmployeeRevisionFixture(t *testing.T) *employeeRevisionFixture {
	t.Helper()
	return newEmployeeRevisionFixtureStage(t, "complete")
}

// The two early-return stages are cleanup controls, not actual migration faults.
func newEmployeeRevisionFixtureStage(t *testing.T, stage string) *employeeRevisionFixture {
	t.Helper()
	if stage != "complete" && stage != "after_store" && stage != "after_runtime" {
		t.Fatal("ERB_FIXTURE_STAGE")
	}
	// Cleanup is LIFO: register the resource closer AFTER TempDir's remover.
	dir := t.TempDir()
	f := &employeeRevisionFixture{dir: dir, finished: make(chan struct{}, 8)}
	t.Cleanup(func() { f.release(t) })
	if Initialize(t.Context(), dir, strings.NewReader("a-strong-preview-password\n")) != nil {
		t.Fatal("ERB_INIT")
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal("ERB_STORE")
	}
	f.ownedStore = s
	f.db = s.db
	if stage == "after_store" {
		return f
	}
	// openStore does not install these independently managed self-auth tables.
	// Use the real migration before any self credential/session fixture seed.
	if s.migrateSelfService(t.Context()) != nil {
		t.Fatal("ERB_SELF_MIGRATION")
	}
	cfg := Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "employee-revision-boundary-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("ERB_SECRETS")
	}
	var actor string
	var passwordHash []byte
	if f.db.QueryRow("SELECT id,password_hash FROM admins WHERE username='admin'").Scan(&actor, &passwordHash) != nil || actor == "" {
		t.Fatal("ERB_ADMIN")
	}
	stamp, future := utcNow(), selfTime(time.Now().Add(6*time.Hour))
	adminAuditExec(t, f.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,'Revision target','active','selected',1,?)", employeeRevisionTarget, stamp)
	adminAuditExec(t, f.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('emp-erb-other','Other employee','active','all',1,?)", stamp)
	adminAuditExec(t, f.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('emp-erb-peer','Registered peer','active','all',1,?)", stamp)
	// Controlled metadata only; this fixture never decrypts or calls an upstream.
	adminAuditExec(t, f.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('up-erb','Fixture','openai-compatible','https://example.invalid',1,?,1,1,?)", []byte("synthetic-opaque-fixture"), stamp)
	adminAuditExec(t, f.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('erb-model','up-erb','fixture',1,?)", stamp)
	adminAuditExec(t, f.db, "INSERT INTO employee_models(employee_id,model_id) VALUES(?,'erb-model')", employeeRevisionTarget)
	for _, k := range []struct{ id, owner, selector string }{
		{"key-erb-ordinary", employeeRevisionTarget, "erb-ordinary"},
		{"key-erb-slot", employeeRevisionTarget, "erb-slot"},
		{"key-erb-other", "emp-erb-other", "erb-other"},
	} {
		adminAuditExec(t, f.db, "INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,'Fixture',?,?,1,?,?)", k.id, k.owner, k.selector, sec.digest("employee-key/v1\x00"+k.selector, strings.Repeat("k", 43)), k.id, stamp)
	}
	view := &adminRevokeAuditFixture{app: &App{store: s}, actor: actor}
	adminAuditInstallSlot(t, view, "key-erb-slot", "armed")
	adminAuditInstallSlot(t, view, "key-erb-other", "cancelled")
	adminAuditExec(t, f.db, "INSERT INTO employee_self_credentials(employee_id,password_hash,updated_at) VALUES(?,?,?)", employeeRevisionTarget, passwordHash, stamp)
	adminAuditExec(t, f.db, "INSERT INTO employee_self_credentials(employee_id,enrollment_digest,enrollment_expires_at,updated_at) VALUES('emp-erb-other',?,?,?)", sec.digest("erb-fixture/v1", "enrollment"), future, stamp)
	adminAuditExec(t, f.db, "INSERT INTO employee_self_credentials(employee_id,password_hash,updated_at) VALUES('emp-erb-peer',?,?)", passwordHash, stamp)
	adminAuditExec(t, f.db, "INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES('erb-self',?,?,'synthetic-self-csrf',?,?)", employeeRevisionTarget, sec.digest("erb-fixture/v1", "session"), future, stamp)
	adminAuditExec(t, f.db, "INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES('erb-self-peer','emp-erb-peer',?,'synthetic-peer-csrf',?,?)", sec.digest("erb-fixture/v1", "peer-session"), future, stamp)
	adminAuditSeed(t, view, 1, false)
	// One DB, fixed before publication; no App.Open/Handler or worker constructor.
	app := &App{cfg: cfg, store: s, secrets: sec, logins: make(map[string]*loginAttempt)}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	app.accountPool = &accountPoolRuntime{app: app, ctx: runtimeCtx, cancel: runtimeCancel,
		changeContext: changeCtx, changeCancel: changeCancel,
		active: make(map[*accountPoolLease]struct{}), maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	// Only the fully constructed App/runtime becomes the sole resource owner.
	f.runtimeContext, f.app, f.ownedStore = runtimeCtx, app, nil
	if stage == "after_runtime" {
		return f
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", f.app.login)
	mux.HandleFunc("PATCH /admin/api/v1/employees/{id}", f.app.requireAdmin(f.app.updateEmployee, true))
	mux.HandleFunc("PUT /admin/api/v1/employees/{id}/model-policy", f.app.requireAdmin(f.app.updateModelPolicy, true))
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.closing.Load() {
			w.WriteHeader(503)
			return
		}
		f.handlers.Add(1)
		defer f.handlers.Done()
		if r.Method == "PATCH" || r.Method == "PUT" {
			defer func() { f.finished <- struct{}{} }()
		}
		mux.ServeHTTP(w, r)
	})))
	f.client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login := f.call(t, "POST", "/admin/api/v1/sessions", `{"username":"admin","password":"a-strong-preview-password"}`, employeeRevisionAccess{origin: f.server.URL})
	var value struct {
		CSRF string `json:"csrf_token"`
	}
	if login.status != 200 || json.Unmarshal(login.body, &value) != nil || value.CSRF == "" {
		t.Fatal("ERB_LOGIN")
	}
	cookies := (&http.Response{Header: login.header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != adminCookieName {
		t.Fatal("ERB_LOGIN_COOKIE")
	}
	f.cookie, f.csrf = cookies[0], value.CSRF
	var actual string
	if f.db.QueryRow("SELECT admin_id FROM sessions WHERE token_digest=?", sec.digest("admin-session/v1", f.cookie.Value)).Scan(&actual) != nil || actual != actor {
		t.Fatal("ERB_LOGIN_REAL_ACTOR")
	}
	f.validAdmin(t)
	return f
}

func (f *employeeRevisionFixture) release(t *testing.T) {
	t.Helper()
	f.releaseOnce.Do(func() {
		f.closing.Store(true)
		if f.server != nil {
			f.server.Close()
		}
		f.handlers.Wait()
		if f.app != nil {
			var changed context.Context
			if f.app.accountPool != nil {
				_, changed = f.app.accountPool.changeSnapshot()
			} else {
				t.Error("ERB_CLEANUP_RUNTIME_MISSING")
			}
			f.ownerCloses++
			// App.Close owns both the complete runtime and this fixed DB.
			// Report errors without skipping the remaining release checks.
			if f.app.Close() != nil {
				t.Error("ERB_CLEANUP_APP")
			}
			if changed == nil || changed.Err() != context.Canceled {
				t.Error("ERB_CLEANUP_CHANGE_CONTEXT")
			}
			if f.runtimeContext == nil || f.runtimeContext.Err() != context.Canceled {
				t.Error("ERB_CLEANUP_RUNTIME_CONTEXT")
			}
		} else if f.ownedStore != nil {
			f.ownerCloses++
			if f.ownedStore.close() != nil {
				t.Error("ERB_CLEANUP_STORE")
			}
		}
		if f.db != nil && f.db.PingContext(context.Background()) == nil {
			t.Error("ERB_CLEANUP_DB_OPEN")
		}
	})
}

func TestAdminEmployeeRevisionBoundaryFixtureOwnership(t *testing.T) {
	for _, stage := range []string{"after_store", "after_runtime", "complete"} {
		var f *employeeRevisionFixture
		if !t.Run(stage, func(t *testing.T) {
			f = newEmployeeRevisionFixtureStage(t, stage)
			if f.db == nil || f.db.PingContext(t.Context()) != nil {
				t.Fatal("ERB_FIXTURE_DB_NOT_OPEN")
			}
			if stage == "after_store" {
				if f.app != nil || f.ownedStore == nil || f.server != nil {
					t.Fatal("ERB_PARTIAL_STORE_OWNERSHIP")
				}
			} else {
				if f.app == nil || f.ownedStore != nil || f.app.store.db != f.db || f.app.accountPool == nil {
					t.Fatal("ERB_COMPLETE_APP_OWNERSHIP")
				}
				_, changed := f.app.accountPool.changeSnapshot()
				if changed == nil || changed.Err() != nil || f.runtimeContext.Err() != nil {
					t.Fatal("ERB_FIXTURE_CONTEXT_NOT_LIVE")
				}
				var credentials, sessions int
				if f.db.QueryRow("SELECT COUNT(*) FROM employee_self_credentials").Scan(&credentials) != nil || credentials != 3 ||
					f.db.QueryRow("SELECT COUNT(*) FROM employee_self_sessions").Scan(&sessions) != nil || sessions != 2 {
					t.Fatal("ERB_SELF_MIGRATED_AND_SEEDED")
				}
			}
		}) {
			return
		}
		// Child cleanups, including TempDir removal, have completed here.
		if f == nil || f.ownerCloses != 1 || f.db.PingContext(context.Background()) == nil {
			t.Fatal("ERB_FIXTURE_RELEASE_PROOF")
		}
		if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
			t.Fatal("ERB_FIXTURE_TEMPDIR_REMAINED")
		}
		f.release(t)
		if f.ownerCloses != 1 {
			t.Fatal("ERB_FIXTURE_DOUBLE_OWNER_CLOSE")
		}
	}
}

// Canonical typed rows stay private; only an exact assertion label is emitted.
func employeeRevisionRows(t *testing.T, db *sql.DB, table, where string, omit map[string]bool) string {
	t.Helper()
	rows, err := db.Query("SELECT * FROM "+table+" WHERE "+where, employeeRevisionTarget)
	if err != nil {
		t.Fatal("ERB_PROTECTED_ROWS")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal("ERB_PROTECTED_COLUMNS")
	}
	encoded := []string{}
	for rows.Next() {
		values, dest := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if rows.Scan(dest...) != nil {
			t.Fatal("ERB_PROTECTED_SCAN")
		}
		typed := []any{}
		for i, value := range values {
			if !omit[columns[i]] {
				typed = append(typed, columns[i], fmt.Sprintf("%T", value), value)
			}
		}
		data, err := json.Marshal(typed)
		if err != nil {
			t.Fatal("ERB_PROTECTED_ENCODE")
		}
		encoded = append(encoded, string(data))
	}
	if rows.Err() != nil || rows.Close() != nil {
		t.Fatal("ERB_PROTECTED_CLOSE")
	}
	sort.Strings(encoded)
	data, err := json.Marshal(encoded)
	if err != nil {
		t.Fatal("ERB_PROTECTED_SORTED_ENCODE")
	}
	return string(data)
}

func (f *employeeRevisionFixture) protectedPositiveFacts(t *testing.T, method string) map[string]string {
	t.Helper()
	facts := adminAuditFacts(t, f.db)
	// Every non-target employee field and row remains protected, including any
	// future columns. Only these two target columns may change for this request.
	facts["employees"] = employeeRevisionRows(t, f.db, "employees", "id<>?", nil)
	omit := map[string]bool{"revision": true}
	if method == "PATCH" {
		omit["status"] = true
	} else {
		omit["model_mode"] = true
	}
	facts["target_employee_immutable_columns"] = employeeRevisionRows(t, f.db, "employees", "id=?", omit)
	if method == "PATCH" {
		for _, table := range []string{"employee_self_credentials", "employee_self_sessions"} {
			facts[table] = employeeRevisionRows(t, f.db, table, "employee_id<>?", nil)
		}
	} else {
		facts["employee_models"] = employeeRevisionRows(t, f.db, "employee_models", "employee_id<>?", nil)
	}
	return facts
}

func (f *employeeRevisionFixture) validAdmin(t *testing.T) {
	t.Helper()
	var expires string
	if f.db.QueryRow("SELECT expires_at FROM sessions WHERE token_digest=?", f.app.secrets.digest("admin-session/v1", f.cookie.Value)).Scan(&expires) != nil {
		t.Fatal("ERB_SESSION")
	}
	expiry, err := parseTime(expires)
	if err != nil || time.Until(expiry) < time.Hour {
		t.Fatal("ERB_SESSION_NOT_EFFECTIVE")
	}
}

func (f *employeeRevisionFixture) access() employeeRevisionAccess {
	return employeeRevisionAccess{cookie: f.cookie, csrf: f.csrf, origin: f.server.URL}
}

func employeeRevisionPath(method, id string) string {
	path := "/admin/api/v1/employees/" + id
	if method == "PUT" {
		path += "/model-policy"
	}
	return path
}

func (f *employeeRevisionFixture) call(t *testing.T, method, path, body string, access employeeRevisionAccess) employeeRevisionReply {
	t.Helper()
	r, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal("ERB_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	if access.cookie != nil {
		r.AddCookie(access.cookie)
	}
	if access.csrf != "" {
		r.Header.Set("X-CSRF-Token", access.csrf)
	}
	if access.origin != "" {
		r.Header.Set("Origin", access.origin)
	}
	if access.bearer != "" {
		r.Header.Set("Authorization", "Bearer "+access.bearer)
	}
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal("ERB_TRANSPORT")
	}
	data, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("ERB_RESPONSE_READ")
	}
	if method == "PATCH" || method == "PUT" {
		select {
		case <-f.finished:
		case <-time.After(5 * time.Second):
			t.Fatal("ERB_HANDLER_JOIN")
		}
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Location") != "" {
		t.Fatal("ERB_RESPONSE_HEADERS")
	}
	return employeeRevisionReply{status: response.StatusCode, header: response.Header, body: data}
}

func (f *employeeRevisionFixture) unchanged(t *testing.T, before map[string]string, epoch uint64, prior context.Context) {
	t.Helper()
	currentEpoch, current := f.app.accountPool.changeSnapshot()
	if f.app.store.db != f.db || !reflect.DeepEqual(before, adminAuditFacts(t, f.db)) {
		t.Fatal("ERB_REJECTION_MUTATED_FACTS")
	}
	if currentEpoch != epoch || current != prior || current == nil || current.Err() != nil {
		t.Fatal("ERB_REJECTION_NOTIFIED")
	}
	f.validAdmin(t)
}

func employeeRevisionError(t *testing.T, r employeeRevisionReply, status int, code, message string) {
	t.Helper()
	want, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	if r.status != status || r.header.Get("Content-Type") != "application/json; charset=utf-8" || !json.Valid(r.body) || !bytes.Equal(r.body, append(want, '\n')) {
		t.Fatal("ERB_ERROR_ENVELOPE")
	}
}

func TestAdminEmployeeRevisionBoundaryInvalidRevision(t *testing.T) {
	f := newEmployeeRevisionFixture(t)
	for _, method := range []string{"PATCH", "PUT"} {
		fields := `"status":"disabled"`
		if method == "PUT" {
			fields = `"mode":"all","models":[]`
		}
		for _, tc := range []struct{ name, body string }{
			{"missing", `{` + fields + `}`}, {"null", `{"expected_revision":null,` + fields + `}`},
			{"zero", `{"expected_revision":0,` + fields + `}`}, {"negative", `{"expected_revision":-1,` + fields + `}`},
			{"top_null", `null`}, {"empty_object", `{}`},
		} {
			expectedOld := false
			passed := t.Run(method+"_"+tc.name, func(t *testing.T) {
				before := adminAuditFacts(t, f.db)
				epoch, prior := f.app.accountPool.changeSnapshot()
				r := f.call(t, method, employeeRevisionPath(method, employeeRevisionTarget), tc.body, f.access())
				f.unchanged(t, before, epoch, prior)
				if r.status == 200 && len(r.body) == 0 {
					expectedOld = true
					t.Error("ADMIN_EMPLOYEE_INVALID_REVISION_OLD_EMPTY_200")
					return
				}
				employeeRevisionError(t, r, 400, "invalid_revision", "A valid expected_revision is required.")
			})
			if !passed && !expectedOld {
				t.FailNow()
			}
		}
	}
}

func TestAdminEmployeeRevisionBoundaryDecodeAndAdmission(t *testing.T) {
	f := newEmployeeRevisionFixture(t)
	for _, method := range []string{"PATCH", "PUT"} {
		for _, tc := range []struct{ name, body string }{
			{"empty", ``}, {"malformed", `{`}, {"unknown", `{"expected_revision":1,"unknown":true}`},
			{"trailing", `{"expected_revision":1} {}`}, {"oversize", strings.Repeat(" ", int(adminMaxBody)+1) + `{}`},
			{"string", `{"expected_revision":"1"}`}, {"bool", `{"expected_revision":true}`},
			{"fraction", `{"expected_revision":1.5}`}, {"object", `{"expected_revision":{}}`},
			{"array", `{"expected_revision":[]}`}, {"top_array", `[]`},
			{"overflow", `{"expected_revision":9223372036854775808}`}, {"underflow", `{"expected_revision":-9223372036854775809}`},
		} {
			t.Run(method+"_"+tc.name, func(t *testing.T) {
				before := adminAuditFacts(t, f.db)
				epoch, prior := f.app.accountPool.changeSnapshot()
				r := f.call(t, method, employeeRevisionPath(method, employeeRevisionTarget), tc.body, f.access())
				employeeRevisionError(t, r, 400, "invalid_request", "Invalid request.")
				f.unchanged(t, before, epoch, prior)
			})
		}
		for _, tc := range []struct {
			name          string
			access        employeeRevisionAccess
			status        int
			code, message string
		}{
			{"anonymous", employeeRevisionAccess{}, 401, "authentication_required", "Administrator authentication is required."},
			{"bad_cookie", employeeRevisionAccess{cookie: &http.Cookie{Name: adminCookieName, Value: "synthetic-invalid"}}, 401, "authentication_required", "Administrator authentication is required."},
			{"self_cookie", employeeRevisionAccess{cookie: &http.Cookie{Name: "cpa_self_session", Value: "synthetic-self"}}, 401, "authentication_required", "Administrator authentication is required."},
			{"employee_bearer", employeeRevisionAccess{bearer: "cpac_erb-ordinary." + strings.Repeat("k", 43)}, 401, "authentication_required", "Administrator authentication is required."},
			{"origin_before_csrf", employeeRevisionAccess{cookie: f.cookie, origin: "http://foreign.invalid", csrf: "invalid"}, 403, "origin_rejected", "Request origin is not allowed."},
			{"missing_origin", employeeRevisionAccess{cookie: f.cookie, csrf: f.csrf}, 403, "origin_rejected", "Request origin is not allowed."},
			{"csrf_before_body", employeeRevisionAccess{cookie: f.cookie, origin: f.server.URL, csrf: "invalid"}, 403, "csrf_rejected", "CSRF validation failed."},
		} {
			for _, body := range []string{`{`, `{"expected_revision":0}`} {
				name := "invalid_revision"
				if body == `{` {
					name = "malformed"
				}
				t.Run(method+"_"+tc.name+"_"+name, func(t *testing.T) {
					before := adminAuditFacts(t, f.db)
					epoch, prior := f.app.accountPool.changeSnapshot()
					r := f.call(t, method, employeeRevisionPath(method, "emp-erb-missing"), body, tc.access)
					employeeRevisionError(t, r, tc.status, tc.code, tc.message)
					f.unchanged(t, before, epoch, prior)
				})
			}
		}
	}
}

func TestAdminEmployeeRevisionBoundaryPositiveAndCAS(t *testing.T) {
	for _, method := range []string{"PATCH", "PUT"} {
		t.Run(method, func(t *testing.T) {
			f := newEmployeeRevisionFixture(t)
			protected := f.protectedPositiveFacts(t, method)
			epoch, prior := f.app.accountPool.changeSnapshot()
			body := `{"expected_revision":1,"status":"disabled"}`
			if method == "PUT" {
				body = `{"expected_revision":1,"mode":"all","models":[]}`
			}
			r := f.call(t, method, employeeRevisionPath(method, employeeRevisionTarget), body, f.access())
			var result employee
			if r.status != 200 || json.Unmarshal(r.body, &result) != nil || result.ID != employeeRevisionTarget || result.Revision != 2 {
				t.Fatal("ERB_POSITIVE_RESPONSE")
			}
			currentEpoch, current := f.app.accountPool.changeSnapshot()
			if currentEpoch != epoch+1 || prior.Err() != context.Canceled || current == nil || current == prior || current.Err() != nil {
				t.Fatal("ERB_POSITIVE_NOTIFICATION")
			}
			if method == "PATCH" {
				if result.Status != "disabled" || result.ModelMode != "selected" {
					t.Fatal("ERB_DISABLE_STATE")
				}
				var remaining int
				if f.db.QueryRow("SELECT (SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id=?)+(SELECT COUNT(*) FROM employee_self_sessions WHERE employee_id=?)", employeeRevisionTarget, employeeRevisionTarget).Scan(&remaining) != nil || remaining != 0 {
					t.Fatal("ERB_DISABLE_SELF_STATE")
				}
				var other int
				if f.db.QueryRow("SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id='emp-erb-other' AND enrollment_digest IS NOT NULL").Scan(&other) != nil || other != 1 {
					t.Fatal("ERB_OTHER_SELF_STATE")
				}
			} else {
				if result.Status != "active" || result.ModelMode != "all" || len(result.Models) != 0 {
					t.Fatal("ERB_POLICY_STATE")
				}
				var policyRows int
				if f.db.QueryRow("SELECT COUNT(*) FROM employee_models WHERE employee_id=?", employeeRevisionTarget).Scan(&policyRows) != nil || policyRows != 0 {
					t.Fatal("ERB_POLICY_ROWS_REMAINED")
				}
			}
			var persistedStatus, persistedMode string
			var persistedRevision int64
			if f.db.QueryRow("SELECT status,model_mode,revision FROM employees WHERE id=?", employeeRevisionTarget).Scan(&persistedStatus, &persistedMode, &persistedRevision) != nil ||
				persistedStatus != result.Status || persistedMode != result.ModelMode || persistedRevision != 2 {
				t.Fatal("ERB_POSITIVE_PERSISTED_TARGET")
			}
			if !reflect.DeepEqual(protected, f.protectedPositiveFacts(t, method)) {
				t.Fatal("ERB_POSITIVE_PROTECTED_ROWS_CHANGED")
			}
			var otherRevision int
			if f.db.QueryRow("SELECT revision FROM employees WHERE id='emp-erb-other' AND status='active' AND model_mode='all'").Scan(&otherRevision) != nil || otherRevision != 1 {
				t.Fatal("ERB_OTHER_EMPLOYEE")
			}
			cases := []struct {
				name, id, body string
				status         int
				code, message  string
			}{
				{"stale", employeeRevisionTarget, body, 409, "revision_conflict", "The object was changed by another request."},
				{"missing", "emp-erb-missing", strings.Replace(body, `"expected_revision":1`, `"expected_revision":2`, 1), 404, "not_found", "Employee was not found."},
			}
			if method == "PATCH" {
				cases = append(cases, struct {
					name, id, body string
					status         int
					code, message  string
				}{"no_changes", employeeRevisionTarget, `{"expected_revision":2}`, 400, "invalid_request", "No employee changes were provided."}, struct {
					name, id, body string
					status         int
					code, message  string
				}{"invalid_status", employeeRevisionTarget, `{"expected_revision":2,"status":"retired"}`, 400, "invalid_request", "Invalid employee status."})
			} else {
				cases = append(cases, struct {
					name, id, body string
					status         int
					code, message  string
				}{"invalid_mode", employeeRevisionTarget, `{"expected_revision":2,"mode":"invalid","models":[]}`, 400, "invalid_request", "Invalid model policy."}, struct {
					name, id, body string
					status         int
					code, message  string
				}{"all_with_models", employeeRevisionTarget, `{"expected_revision":2,"mode":"all","models":["erb-model"]}`, 400, "invalid_request", "All mode must not include model IDs."}, struct {
					name, id, body string
					status         int
					code, message  string
				}{"unknown_model", employeeRevisionTarget, `{"expected_revision":2,"mode":"selected","models":["erb-missing-model"]}`, 400, "invalid_request", "Model policy refers to an unknown model."})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					before := adminAuditFacts(t, f.db)
					epoch, prior := f.app.accountPool.changeSnapshot()
					r := f.call(t, method, employeeRevisionPath(method, tc.id), tc.body, f.access())
					employeeRevisionError(t, r, tc.status, tc.code, tc.message)
					f.unchanged(t, before, epoch, prior)
				})
			}
		})
	}
}
