// Independently authored for docs/employee-retirement-final-state-contract.md.
// Synthetic owned SQLite and real HTTP/authentication; no reference implementation.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
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
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

const erfPassword = "synthetic-retirement-final-password"
const erfTarget = "emp-erf-target"
const erfPeer = "emp-erf-peer"
const erfOldRevocation = "2025-01-02T03:04:05.000000000Z"

// Pinned to modernc.org/sqlite v1.46.2/lib/sqlite_windows.go and the PR97
// compatibility contract, not a permissive runtime-derived expectation.
const erfSQLiteVersion = "3.51.3"
const erfSQLiteSourceID = "2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6d618"
const erfSQLiteIdentitySQL = "SELECT sqlite_version(),sqlite_source_id()"

type erfSQLiteScanner interface {
	Scan(...any) error
}

type erfSQLiteIdentityQuery func(context.Context, string) erfSQLiteScanner

func erfSQLiteNativeQuery(conn *sql.Conn) erfSQLiteIdentityQuery {
	return func(ctx context.Context, statement string) erfSQLiteScanner {
		return conn.QueryRowContext(ctx, statement)
	}
}

// Return only source-owned safe labels; never err.Error or observed values.
// Both identity and FK reads are made through one held physical sql.Conn.
func erfSQLiteIdentityFailure(ctx context.Context, query erfSQLiteIdentityQuery) string {
	var version, source string
	if query(ctx, erfSQLiteIdentitySQL).Scan(&version, &source) != nil {
		return "ERF_SQLITE_IDENTITY_QUERY"
	}
	if version != erfSQLiteVersion {
		return "ERF_SQLITE_VERSION_MISMATCH"
	}
	if source != erfSQLiteSourceID {
		return "ERF_SQLITE_SOURCE_ID_MISMATCH"
	}
	var foreign int
	if query(ctx, "PRAGMA foreign_keys").Scan(&foreign) != nil {
		return "ERF_SQLITE_FOREIGN_KEYS_QUERY"
	}
	if foreign != 1 {
		return "ERF_SQLITE_FOREIGN_KEYS_OFF"
	}
	return ""
}

type erfSQLiteControlledIdentity struct {
	row   *sql.Row
	field int
}

func (r erfSQLiteControlledIdentity) Scan(dest ...any) error {
	if err := r.row.Scan(dest...); err != nil {
		return err
	}
	if len(dest) != 2 {
		return errors.New("ERF_IDENTITY_CONTROL_SHAPE")
	}
	value, ok := dest[r.field].(*string)
	if !ok || len(*value) == 0 {
		return errors.New("ERF_IDENTITY_CONTROL_TYPE")
	}
	if r.field == 0 {
		*value = "3.50.3" // Deliberately rejected old expectation, not an engine swap.
	} else {
		// Change only the last hex digit: a prefix/truncated source-ID check fails
		// this control even though the real native row was first read successfully.
		tail := "0"
		if strings.HasSuffix(*value, tail) {
			tail = "1"
		}
		*value = (*value)[:len(*value)-1] + tail
	}
	return nil
}

type erfIdentityOwner struct {
	store  *store
	conn   *sql.Conn
	once   sync.Once
	closes int
}

func newERFIdentityOwner(t *testing.T) *erfIdentityOwner {
	t.Helper()
	dir := t.TempDir()
	o := &erfIdentityOwner{}
	t.Cleanup(func() { o.close(t) })
	s, err := openStore(dir)
	if err != nil {
		t.Fatal("ERF_IDENTITY_STORE")
	}
	o.store = s
	if keypolicy.Migrate(t.Context(), s.db) != nil || s.migrateSelfService(t.Context()) != nil {
		t.Fatal("ERF_IDENTITY_MIGRATIONS")
	}
	o.conn, err = s.db.Conn(t.Context())
	if err != nil {
		t.Fatal("ERF_IDENTITY_CONNECTION")
	}
	return o // Exclusive, unpublished physical Conn: no App or HTTP server exists.
}

func (o *erfIdentityOwner) close(t *testing.T) {
	t.Helper()
	o.once.Do(func() {
		if o.conn != nil {
			if o.conn.Close() != nil {
				t.Error("ERF_IDENTITY_CONN_CLOSE")
			}
			if !errors.Is(o.conn.PingContext(context.Background()), sql.ErrConnDone) {
				t.Error("ERF_IDENTITY_CONN_STILL_OPEN")
			}
		}
		if o.store != nil {
			o.closes++
			if o.store.close() != nil || o.store.db.PingContext(context.Background()) == nil {
				t.Error("ERF_IDENTITY_STORE_CLOSE")
			}
		}
	})
}

type erfAccess struct {
	cookie *http.Cookie
	csrf   string
	origin string
	bearer string
}

type erfReply struct {
	status int
	body   []byte
	header http.Header
	err    error
}

type erfFixture struct {
	dir           string
	app           *App
	store         *store
	server        *httptest.Server
	client        *http.Client
	cookie        *http.Cookie
	csrf          string
	actor         string
	hooks         atomic.Pointer[employeeRetirementHooks]
	arrived       atomic.Pointer[func(*http.Request)]
	requestCancel atomic.Pointer[context.CancelFunc]
	mu            sync.Mutex
	closing       bool
	handlers      sync.WaitGroup
	closeOnce     sync.Once
	closes        int
}

func newERFFixture(t *testing.T) *erfFixture {
	t.Helper()
	f := &erfFixture{dir: t.TempDir(), client: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	t.Cleanup(func() { f.close(t) })
	if Initialize(t.Context(), f.dir, strings.NewReader(erfPassword+"\n")) != nil {
		t.Fatal("ERF_INIT")
	}
	s, err := openStore(f.dir)
	if err != nil {
		t.Fatal("ERF_STORE")
	}
	f.store = s
	if keypolicy.Migrate(t.Context(), s.db) != nil || s.migrateSelfService(t.Context()) != nil {
		t.Fatal("ERF_REAL_MIGRATIONS")
	}
	identityConn, err := s.db.Conn(t.Context())
	if err != nil {
		t.Fatal("ERF_SQLITE_CONNECTION")
	}
	identityFailure := erfSQLiteIdentityFailure(t.Context(), erfSQLiteNativeQuery(identityConn))
	if identityConn.Close() != nil {
		t.Fatal("ERF_SQLITE_CONNECTION_CLOSE")
	}
	if identityFailure != "" {
		t.Fatal(identityFailure)
	}
	cfg := Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "retirement-final-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("ERF_SYNTHETIC_SECRETS")
	}
	trusted, err := keypolicy.NewTrustedProxySet(nil)
	if err != nil {
		t.Fatal("ERF_PROXY_CONFIG")
	}
	app := &App{cfg: cfg, store: s, secrets: sec, trustedProxies: trusted, logins: make(map[string]*loginAttempt)}
	rtctx, rtcancel := context.WithCancel(context.Background())
	change, changeCancel := context.WithCancel(context.Background())
	app.accountPool = &accountPoolRuntime{app: app, ctx: rtctx, cancel: rtcancel,
		changeContext: change, changeCancel: changeCancel, active: make(map[*accountPoolLease]struct{}),
		maintenanceActive: make(map[*accountMaintenanceLease]struct{})}
	f.app = app // One fixed DB owner, before HTTP publication; no Open/Handler/Start.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	mux.HandleFunc("GET /admin/api/v1/session", app.requireAdmin(app.sessionInfo, false))
	mux.HandleFunc("POST /admin/api/v1/employees/{id}/retire-credentials", app.requireAdmin(app.retireEmployeeCredentials, true))
	mux.HandleFunc("PATCH /admin/api/v1/employees/{id}", app.requireAdmin(app.updateEmployee, true))
	mux.HandleFunc("POST /admin/api/v1/employees/{id}/keys", app.requireAdmin(app.createKey, true))
	// This test-only route invokes the real credential/auth-policy reader, not a
	// substituted authenticator or upstream/model execution implementation.
	mux.HandleFunc("GET /erf/employee-auth", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := app.authenticateEmployee(w, r); ok {
			w.WriteHeader(http.StatusNoContent)
		}
	})
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.closing {
			f.mu.Unlock()
			w.WriteHeader(503)
			return
		}
		f.handlers.Add(1)
		f.mu.Unlock()
		defer f.handlers.Done()
		if fn := f.arrived.Load(); fn != nil {
			(*fn)(r)
		}
		if strings.HasSuffix(r.URL.Path, "/retire-credentials") {
			owned, cancel := context.WithCancel(r.Context())
			defer cancel()
			f.requestCancel.Store(&cancel)
			defer f.requestCancel.Store(nil)
			r = r.WithContext(owned)
			if h := f.hooks.Load(); h != nil {
				r = r.WithContext(context.WithValue(r.Context(), employeeRetirementHooksKey{}, *h))
			}
		}
		mux.ServeHTTP(w, r)
	})))
	login := f.call("POST", "/admin/api/v1/sessions", `{"username":"admin","password":"`+erfPassword+`"}`, erfAccess{origin: f.server.URL})
	erfStatus(t, login, 200)
	var answer struct {
		CSRF string `json:"csrf_token"`
	}
	if json.Unmarshal(login.body, &answer) != nil || answer.CSRF == "" {
		t.Fatal("ERF_LOGIN_RESPONSE")
	}
	cookies := (&http.Response{Header: login.header}).Cookies()
	if len(cookies) != 1 || cookies[0].Name != adminCookieName {
		t.Fatal("ERF_LOGIN_COOKIE")
	}
	f.cookie, f.csrf = cookies[0], answer.CSRF
	if s.db.QueryRow("SELECT admin_id FROM sessions WHERE token_digest=?", sec.digest("admin-session/v1", f.cookie.Value)).Scan(&f.actor) != nil || f.actor == "" {
		t.Fatal("ERF_REAL_ADMIN_ACTOR")
	}
	return f
}

func (f *erfFixture) close(t *testing.T) {
	t.Helper()
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closing = true
		f.mu.Unlock()
		if f.server != nil {
			f.server.Close()
		}
		f.handlers.Wait()
		if f.app != nil {
			_, before := f.app.accountPool.changeSnapshot()
			f.closes++
			if f.app.Close() != nil || before.Err() != context.Canceled {
				t.Error("ERF_OWNER_CLOSE")
			}
		} else if f.store != nil {
			f.closes++
			if f.store.close() != nil {
				t.Error("ERF_PARTIAL_OWNER_CLOSE")
			}
		}
		if f.store != nil && f.store.db.PingContext(context.Background()) == nil {
			t.Error("ERF_OWNER_STILL_OPEN")
		}
	})
}

func (f *erfFixture) access() erfAccess {
	return erfAccess{cookie: f.cookie, csrf: f.csrf, origin: f.server.URL}
}

func (f *erfFixture) call(method, path, body string, access erfAccess) erfReply {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		return erfReply{err: errors.New("ERF_REQUEST_BUILD")}
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
		return erfReply{err: errors.New("ERF_HTTP_TRANSPORT")}
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return erfReply{err: errors.New("ERF_HTTP_BODY")}
	}
	return erfReply{status: response.StatusCode, body: data, header: response.Header.Clone()}
}

func erfStatus(t *testing.T, r erfReply, status int) {
	t.Helper()
	if r.err != nil || r.status != status {
		t.Fatal("ERF_HTTP_STATUS")
	}
	if status == 503 && !bytes.Equal(bytes.TrimSpace(r.body), []byte(`{"error":{"code":"storage_unavailable","message":"Service is temporarily unavailable."}}`)) {
		t.Fatal("ERF_FIXED_STORAGE_ERROR")
	}
}

func (f *erfFixture) retire(revision int64) erfReply {
	return f.call("POST", "/admin/api/v1/employees/"+erfTarget+"/retire-credentials", fmt.Sprintf(`{"expected_revision":%d}`, revision), f.access())
}

func erfExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal("ERF_FIXTURE_SQL")
	}
}

func erfCount(t *testing.T, db *sql.DB, statement string, args ...any) int64 {
	t.Helper()
	var value int64
	if db.QueryRow(statement, args...).Scan(&value) != nil {
		t.Fatal("ERF_FACT_COUNT")
	}
	return value
}

func (f *erfFixture) seed(t *testing.T, status string, keys int, slot string) string {
	t.Helper()
	db, stamp := f.store.db, "2025-02-03T04:05:06.000000000Z"
	erfExec(t, db, "INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,?,?,?,'all',7,?)", erfTarget, "Target", "Fixture", "retained note", status, stamp)
	erfExec(t, db, "INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,?,?,'active','all',11,?)", erfPeer, "Peer", "Peer department", "peer note", stamp)
	var hash []byte
	if db.QueryRow("SELECT password_hash FROM admins WHERE id=?", f.actor).Scan(&hash) != nil {
		t.Fatal("ERF_PASSWORD_FIXTURE")
	}
	for _, id := range []string{erfTarget, erfPeer} {
		erfExec(t, db, "INSERT INTO employee_self_credentials(employee_id,password_hash,updated_at) VALUES(?,?,?)", id, hash, stamp)
		erfExec(t, db, "INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)", "self-"+id, id, f.app.secrets.digest("erf-synthetic-session", id), "synthetic-csrf", selfTime(time.Now().Add(24*time.Hour)), stamp)
	}
	for i := 0; i < keys; i++ {
		f.seedKey(t, fmt.Sprintf("key-erf-%d", i), erfTarget, fmt.Sprintf("erf-selector-%d", i))
	}
	f.seedKey(t, "key-erf-peer", erfPeer, "erf-selector-peer")
	if keys >= 3 {
		erfExec(t, db, "UPDATE access_keys SET revoked_at=? WHERE id='key-erf-1'", erfOldRevocation)
		erfExec(t, db, "UPDATE access_keys SET expires_at='2020-01-01T00:00:00.000000000Z' WHERE id='key-erf-2'")
	}
	if slot != "" {
		if keys < 1 {
			t.Fatal("ERF_SLOT_FIXTURE_KEY")
		}
		var fingerprint any
		var employeeRev, keyRev, governanceRev, budgetRev any
		var armed, issued, cancelled any
		if slot == "armed" || slot == "issued" {
			fingerprint = bytes.Repeat([]byte{0x52}, 32)
			employeeRev, keyRev, governanceRev, budgetRev = int64(7), int64(1), int64(3), int64(4)
			armed = stamp
		}
		if slot == "issued" {
			issued = stamp
		}
		if slot == "cancelled" {
			cancelled = stamp
		}
		erfExec(t, db, "INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,arm_employee_revision,arm_key_policy_revision,arm_governance_revision,arm_budget_revision,created_at,armed_at,issued_at,cancelled_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)", erfTarget, "key-erf-0", slot, fingerprint, employeeRev, keyRev, governanceRev, budgetRev, stamp, armed, issued, cancelled)
	}
	f.seedPolicyFacts(t)
	return "cpac_erf-selector-0." + strings.Repeat("k", 43)
}

func (f *erfFixture) seedPolicyFacts(t *testing.T) {
	t.Helper()
	stamp := "2025-02-03T04:05:06.000000000Z"
	erfExec(t, f.store.db, "INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('erf-policy-upstream','Synthetic policy source','openai-compatible','https://example.invalid',0,zeroblob(1),1,1,?)", stamp)
	erfExec(t, f.store.db, "INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('erf-policy-model','erf-policy-upstream','synthetic-model',0,?)", stamp)
	erfExec(t, f.store.db, "INSERT INTO account_groups(id,name,revision,created_at) VALUES('erf-policy-group','Synthetic policy group',1,?)", stamp)
	erfExec(t, f.store.db, "UPDATE employees SET model_mode='selected' WHERE id=?", erfPeer)
	erfExec(t, f.store.db, "INSERT INTO employee_models(employee_id,model_id) VALUES(?,'erf-policy-model')", erfPeer)
	for _, key := range []string{"key-erf-1", "key-erf-peer"} {
		if erfCount(t, f.store.db, "SELECT COUNT(*) FROM access_keys WHERE id=?", key) == 0 {
			continue
		}
		erfExec(t, f.store.db, "UPDATE access_key_policies SET model_mode='selected' WHERE key_id=?", key)
		erfExec(t, f.store.db, "INSERT INTO access_key_policy_models(key_id,model_id) VALUES(?,'erf-policy-model')", key)
		erfExec(t, f.store.db, "UPDATE access_key_policy_sources SET source_mode='selected' WHERE key_id=?", key)
		erfExec(t, f.store.db, "INSERT INTO access_key_policy_source_cidrs(key_id,cidr) VALUES(?,'127.0.0.0/8')", key)
		erfExec(t, f.store.db, "UPDATE access_key_policy_account_groups SET account_group_mode='selected' WHERE key_id=?", key)
		erfExec(t, f.store.db, "INSERT INTO access_key_policy_account_group_members(key_id,account_group_id) VALUES(?,'erf-policy-group')", key)
	}
}

func (f *erfFixture) seedKey(t *testing.T, id, employeeID, selector string) {
	t.Helper()
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal("ERF_KEY_TX")
	}
	defer tx.Rollback()
	stamp := time.Date(2025, 2, 3, 4, 5, 6, 0, time.UTC)
	if _, err := tx.Exec("INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at) VALUES(?,?,?,?,?,1,?,'',?,?)", id, employeeID, "Synthetic Key", selector, f.app.secrets.digest("employee-key/v1\x00"+selector, strings.Repeat("k", 43)), id, selfTime(time.Now().Add(24*time.Hour)), selfTime(stamp)); err != nil {
		t.Fatal("ERF_KEY_INSERT")
	}
	if keypolicy.CreateDefaultTx(t.Context(), tx, id, stamp) != nil || tx.Commit() != nil {
		t.Fatal("ERF_KEY_REAL_POLICY")
	}
}

func (f *erfFixture) auth(t *testing.T, token string, status int) {
	t.Helper()
	erfStatus(t, f.call("GET", "/erf/employee-auth", "", erfAccess{bearer: token}), status)
}

// The independent test oracle snapshots every real user table, not the product
// reader's table list. Values are type-tagged before JSON and never printed.
func erfFacts(t *testing.T, db *sql.DB, omit map[string]bool) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal("ERF_TABLE_INVENTORY")
	}
	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			rows.Close()
			t.Fatal("ERF_TABLE_NAME")
		}
		names = append(names, name)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatal("ERF_TABLE_CLOSE")
	}
	result := make(map[string]string)
	for _, name := range names {
		if omit[name] {
			continue
		}
		query := `SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`
		result[name] = erfRows(t, db, query)
	}
	return result
}

func erfRows(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal("ERF_ROWS_QUERY")
	}
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		t.Fatal("ERF_ROWS_COLUMNS")
	}
	canonical := []string{}
	for rows.Next() {
		values, targets := make([]any, len(cols)), make([]any, len(cols))
		for i := range values {
			targets[i] = &values[i]
		}
		if rows.Scan(targets...) != nil {
			rows.Close()
			t.Fatal("ERF_ROWS_SCAN")
		}
		tagged := make([][2]any, len(values))
		for i, value := range values {
			switch v := value.(type) {
			case nil:
				tagged[i] = [2]any{"null", nil}
			case string:
				tagged[i] = [2]any{"text", v}
			case []byte:
				tagged[i] = [2]any{"blob", hex.EncodeToString(v)}
			case int64:
				tagged[i] = [2]any{"integer", v}
			case float64:
				tagged[i] = [2]any{"real", v}
			default:
				rows.Close()
				t.Fatal("ERF_ROWS_TYPE")
			}
		}
		raw, err := json.Marshal(tagged)
		if err != nil {
			rows.Close()
			t.Fatal("ERF_ROWS_ENCODING")
		}
		canonical = append(canonical, string(raw))
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatal("ERF_ROWS_CLOSE")
	}
	sort.Strings(canonical)
	raw, err := json.Marshal(struct {
		Columns []string
		Rows    []string
	}{cols, canonical})
	if err != nil {
		t.Fatal("ERF_FACT_ENCODING")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func erfReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal("ERF_RO_ABSOLUTE")
	}
	p := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" && filepath.VolumeName(absolute) != "" {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal("ERF_RO_OPEN")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if db.PingContext(t.Context()) != nil {
		db.Close()
		t.Fatal("ERF_RO_CONNECT")
	}
	return db
}

func erfSQLiteCode(err error) int {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code() & 255
	}
	return -1
}

func erfCloseRO(t *testing.T, db *sql.DB) {
	t.Helper()
	if db.Close() != nil {
		t.Fatal("ERF_RO_CLOSE")
	}
}

func erfEpoch(t *testing.T, f *erfFixture, before uint64, prior context.Context, changed bool) {
	t.Helper()
	after, next := f.app.accountPool.changeSnapshot()
	if changed {
		if after != before+1 || prior.Err() != context.Canceled || next.Err() != nil {
			t.Fatal("ERF_WAKE_EXPECTED")
		}
	} else if after != before || prior.Err() != nil || next != prior {
		t.Fatal("ERF_UNEXPECTED_WAKE")
	}
}

func TestEmployeeRetirementFinalFixtureIdentity(t *testing.T) {
	cases := []struct {
		name, want string
		queries    int
	}{
		{"native_on", "", 2},
		{"version_mismatch", "ERF_SQLITE_VERSION_MISMATCH", 1},
		{"source_mismatch", "ERF_SQLITE_SOURCE_ID_MISMATCH", 1},
		{"identity_query_error", "ERF_SQLITE_IDENTITY_QUERY", 1},
		{"foreign_query_error", "ERF_SQLITE_FOREIGN_KEYS_QUERY", 2},
		{"foreign_off_real", "ERF_SQLITE_FOREIGN_KEYS_OFF", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newERFIdentityOwner(t)
			if erfSQLiteIdentityFailure(t.Context(), erfSQLiteNativeQuery(o.conn)) != "" {
				t.Fatal("ERF_IDENTITY_NATIVE_BASELINE")
			}
			if tc.name == "foreign_off_real" {
				if _, err := o.conn.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF"); err != nil {
					t.Fatal("ERF_IDENTITY_FOREIGN_OFF_SETUP")
				}
			}
			queries := 0
			query := func(ctx context.Context, statement string) erfSQLiteScanner {
				queries++
				if statement == erfSQLiteIdentitySQL {
					switch tc.name {
					case "version_mismatch":
						return erfSQLiteControlledIdentity{o.conn.QueryRowContext(ctx, statement), 0}
					case "source_mismatch":
						return erfSQLiteControlledIdentity{o.conn.QueryRowContext(ctx, statement), 1}
					case "identity_query_error":
						return o.conn.QueryRowContext(ctx, "SELECT erf_fixture_missing_sql_function()")
					}
				}
				if statement == "PRAGMA foreign_keys" && tc.name == "foreign_query_error" {
					return o.conn.QueryRowContext(ctx, "SELECT erf_fixture_missing_sql_function()")
				}
				return o.conn.QueryRowContext(ctx, statement)
			}
			if got := erfSQLiteIdentityFailure(t.Context(), query); got != tc.want || queries != tc.queries {
				t.Fatal("ERF_IDENTITY_FAIL_CLOSED_CLASSIFICATION")
			}
			if tc.name == "foreign_off_real" {
				if _, err := o.conn.ExecContext(t.Context(), "PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal("ERF_IDENTITY_FOREIGN_ON_RESTORE")
				}
			}
			if erfSQLiteIdentityFailure(t.Context(), erfSQLiteNativeQuery(o.conn)) != "" {
				t.Fatal("ERF_IDENTITY_NATIVE_REUSE")
			}
			o.close(t)
			if o.closes != 1 {
				t.Fatal("ERF_IDENTITY_OWNER_COUNT")
			}
		})
	}
}

func TestEmployeeRetirementFinalFixtureAuthentication(t *testing.T) {
	f := newERFFixture(t)
	token := f.seed(t, "active", 4, "")
	before := erfFacts(t, f.store.db, nil)
	epoch, prior := f.app.accountPool.changeSnapshot()
	t.Run("correct_secret", func(t *testing.T) { f.auth(t, token, 204) })
	t.Run("wrong_secret", func(t *testing.T) {
		f.auth(t, "cpac_erf-selector-0."+strings.Repeat("z", 43), 401)
	})
	t.Run("missing_authorization", func(t *testing.T) { f.auth(t, "", 401) })
	if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) ||
		erfCount(t, f.store.db, "SELECT COUNT(*) FROM account_lifecycle_audit WHERE action='employee.retire_credentials'") != 0 {
		t.Fatal("ERF_FIXTURE_AUTH_MUTATED_FACTS")
	}
	erfEpoch(t, f, epoch, prior, false)
	f.close(t)
	if f.closes != 1 {
		t.Fatal("ERF_FIXTURE_AUTH_CLOSE_COUNT")
	}
}

func TestEmployeeRetirementFinalReadOnly(t *testing.T) {
	t.Run("existing_no_write", func(t *testing.T) {
		f := newERFFixture(t)
		f.seed(t, "active", 1, "")
		before := erfFacts(t, f.store.db, nil)
		f.close(t)
		if f.closes != 1 {
			t.Fatal("ERF_OWNER_COUNT")
		}
		db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
		if _, err := db.Exec("PRAGMA query_only=OFF"); err != nil {
			db.Close()
			t.Fatal("ERF_RO_QUERY_ONLY")
		}
		if _, err := db.Exec("CREATE TABLE erf_forbidden_write(x)"); erfSQLiteCode(err) != 8 {
			db.Close()
			t.Fatal("ERF_RO_WRITE_NOT_BLOCKED")
		}
		if !reflect.DeepEqual(before, erfFacts(t, db, nil)) {
			db.Close()
			t.Fatal("ERF_RO_FACTS")
		}
		erfCloseRO(t, db)
	})
	t.Run("missing_no_create", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absent.sqlite")
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatal("ERF_MISSING_ABSOLUTE")
		}
		p := filepath.ToSlash(absolute)
		if runtime.GOOS == "windows" && filepath.VolumeName(absolute) != "" {
			p = "/" + p
		}
		u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro"}
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			t.Fatal("ERF_MISSING_OPEN")
		}
		if erfSQLiteCode(db.PingContext(t.Context())) != 14 {
			db.Close()
			t.Fatal("ERF_MISSING_NOT_REFUSED")
		}
		erfCloseRO(t, db)
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ERF_MISSING_CREATED_FILE")
			}
		}
	})
}

func TestEmployeeRetirementFinalControl(t *testing.T) {
	cases := []struct {
		name, status, slot string
		keys               int
	}{
		{"active_zero", "active", "", 0}, {"active_multiple", "active", "", 4}, {"disabled_multiple", "disabled", "", 4},
		{"slot_pending", "active", "pending", 4}, {"slot_armed", "active", "armed", 4},
		{"slot_issued", "active", "issued", 4}, {"slot_cancelled", "active", "cancelled", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newERFFixture(t)
			token := f.seed(t, tc.status, tc.keys, tc.slot)
			if tc.keys > 0 && tc.status == "active" && (tc.slot == "" || tc.slot == "issued") {
				f.auth(t, token, 204)
			}
			f.auth(t, "cpac_erf-selector-0."+strings.Repeat("z", 43), 401)
			immutable := erfFacts(t, f.store.db, map[string]bool{"employees": true, "access_keys": true, "employee_self_key_slots": true, "employee_self_credentials": true, "employee_self_sessions": true, "account_lifecycle_audit": true})
			keys := erfRows(t, f.store.db, "SELECT id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at FROM access_keys")
			peer := erfRows(t, f.store.db, "SELECT * FROM employees WHERE id=?", erfPeer)
			peerSelf := erfRows(t, f.store.db, "SELECT * FROM employee_self_credentials WHERE employee_id=?", erfPeer)
			peerSessions := erfRows(t, f.store.db, "SELECT * FROM employee_self_sessions WHERE employee_id=?", erfPeer)
			slotBefore := erfRows(t, f.store.db, "SELECT * FROM employee_self_key_slots")
			before, prior := f.app.accountPool.changeSnapshot()
			r := f.retire(7)
			erfStatus(t, r, 200)
			if r.header.Get("Cache-Control") != "no-store" || !bytes.Equal(bytes.TrimSpace(r.body), []byte(`{"id":"emp-erf-target","status":"disabled","revision":8}`)) {
				t.Fatal("ERF_SUCCESS_RESPONSE")
			}
			erfEpoch(t, f, before, prior, true)
			if erfCount(t, f.store.db, "SELECT COUNT(*) FROM employees WHERE id=? AND status='disabled' AND revision=8 AND name='Target' AND department='Fixture' AND note='retained note' AND model_mode='all' AND created_at='2025-02-03T04:05:06.000000000Z'", erfTarget) != 1 {
				t.Fatal("ERF_EMPLOYEE_FINAL")
			}
			if keys != erfRows(t, f.store.db, "SELECT id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at FROM access_keys") || peer != erfRows(t, f.store.db, "SELECT * FROM employees WHERE id=?", erfPeer) || peerSelf != erfRows(t, f.store.db, "SELECT * FROM employee_self_credentials WHERE employee_id=?", erfPeer) || peerSessions != erfRows(t, f.store.db, "SELECT * FROM employee_self_sessions WHERE employee_id=?", erfPeer) {
				t.Fatal("ERF_PROTECTED_FACTS")
			}
			if !reflect.DeepEqual(immutable, erfFacts(t, f.store.db, map[string]bool{"employees": true, "access_keys": true, "employee_self_key_slots": true, "employee_self_credentials": true, "employee_self_sessions": true, "account_lifecycle_audit": true})) {
				t.Fatal("ERF_OTHER_TABLES")
			}
			if erfCount(t, f.store.db, "SELECT COUNT(*) FROM access_keys WHERE employee_id=? AND revoked_at IS NULL", erfTarget) != 0 || erfCount(t, f.store.db, "SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id=?", erfTarget) != 0 || erfCount(t, f.store.db, "SELECT COUNT(*) FROM employee_self_sessions WHERE employee_id=?", erfTarget) != 0 {
				t.Fatal("ERF_CREDENTIALS_FINAL")
			}
			if tc.keys >= 3 && erfCount(t, f.store.db, "SELECT COUNT(*) FROM access_keys WHERE id='key-erf-1' AND revoked_at=?", erfOldRevocation) != 1 {
				t.Fatal("ERF_OLD_REVOCATION")
			}
			if tc.keys > 0 && erfCount(t, f.store.db, "SELECT COUNT(DISTINCT revoked_at) FROM access_keys WHERE employee_id=? AND id<>'key-erf-1'", erfTarget) != 1 {
				t.Fatal("ERF_COMMON_STAMP")
			}
			if tc.slot == "pending" || tc.slot == "armed" {
				if erfCount(t, f.store.db, "SELECT COUNT(*) FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.employee_id=? AND s.state='cancelled' AND s.arm_fingerprint IS NULL AND s.arm_employee_revision IS NULL AND s.arm_key_policy_revision IS NULL AND s.arm_governance_revision IS NULL AND s.arm_budget_revision IS NULL AND s.cancelled_at=k.revoked_at AND s.issued_at IS NULL AND s.created_at='2025-02-03T04:05:06.000000000Z'", erfTarget) != 1 {
					t.Fatal("ERF_SLOT_FINAL")
				}
			} else if slotBefore != erfRows(t, f.store.db, "SELECT * FROM employee_self_key_slots") {
				t.Fatal("ERF_SLOT_TOMBSTONE")
			}
			if erfCount(t, f.store.db, "SELECT COUNT(*) FROM account_lifecycle_audit WHERE actor_id=? AND target_id=? AND action='employee.retire_credentials' AND result='succeeded'", f.actor, erfTarget) != 1 {
				t.Fatal("ERF_RETAINED_AUDIT")
			}
			f.auth(t, token, 401)
			erfStatus(t, f.retire(7), 409)
			erfStatus(t, f.retire(8), 200) // Already disabled is still a new CAS operation.
			persisted := erfFacts(t, f.store.db, nil)
			f.close(t)
			db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
			if !reflect.DeepEqual(persisted, erfFacts(t, db, nil)) {
				db.Close()
				t.Fatal("ERF_PERSISTENT_CONTROL")
			}
			erfCloseRO(t, db)
		})
	}
}

func TestEmployeeRetirementFinalAuditRestore(t *testing.T) {
	f := newERFFixture(t)
	token := f.seed(t, "active", 4, "")
	f.auth(t, token, 204)
	f.auth(t, "cpac_erf-selector-0."+strings.Repeat("z", 43), 401)
	before := erfFacts(t, f.store.db, nil)
	erfExec(t, f.store.db, `CREATE TRIGGER erf_restore AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='employee.retire_credentials' AND NEW.target_id='emp-erf-target' BEGIN UPDATE employees SET status='active',revision=7 WHERE id='emp-erf-target'; UPDATE access_keys SET revoked_at=NULL WHERE id='key-erf-0'; END`)
	epoch, prior := f.app.accountPool.changeSnapshot()
	r := f.retire(7)
	old := os.Getenv("CPA_ERF_OLD_ORACLE") == "1"
	if old {
		erfStatus(t, r, 200)
		erfEpoch(t, f, epoch, prior, true)
		if erfCount(t, f.store.db, "SELECT COUNT(*) FROM account_lifecycle_audit WHERE actor_id=? AND action='employee.retire_credentials' AND target_id=? AND result='succeeded'", f.actor, erfTarget) != 1 {
			t.Fatal("ERF_OLD_AUDIT_NOT_RETAINED")
		}
		f.auth(t, token, 204)
		persisted := erfFacts(t, f.store.db, nil)
		f.close(t)
		db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
		if !reflect.DeepEqual(persisted, erfFacts(t, db, nil)) || erfCount(t, db, "SELECT COUNT(*) FROM employees e JOIN access_keys k ON k.employee_id=e.id WHERE e.id=? AND e.status='active' AND e.revision=7 AND k.id='key-erf-0' AND k.revoked_at IS NULL", erfTarget) != 1 {
			db.Close()
			t.Fatal("ERF_OLD_PERSISTENT_CHAIN")
		}
		if _, err := db.Exec("PRAGMA query_only=OFF"); err != nil {
			db.Close()
			t.Fatal("ERF_OLD_RO_SETTING")
		}
		if _, err := db.Exec("CREATE TABLE erf_old_forbidden(x)"); erfSQLiteCode(err) != 8 {
			db.Close()
			t.Fatal("ERF_OLD_RO_PROOF")
		}
		erfCloseRO(t, db)
		if f.closes != 1 {
			t.Fatal("ERF_OLD_OWNER_COUNT")
		}
		t.Error("EMPLOYEE_RETIREMENT_FINAL_OLD_ACCEPTED")
		return
	}
	erfStatus(t, r, 503)
	erfEpoch(t, f, epoch, prior, false)
	if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
		t.Fatal("ERF_RESTORATION_NOT_ROLLED_BACK")
	}
	f.auth(t, token, 204)
	f.close(t)
	db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
	if !reflect.DeepEqual(before, erfFacts(t, db, nil)) {
		db.Close()
		t.Fatal("ERF_REJECT_PERSISTED_FACTS")
	}
	erfCloseRO(t, db)
}

func TestEmployeeRetirementFinalReversals(t *testing.T) {
	cases := []struct{ name, slot, statement string }{
		{"employee_status", "armed", `UPDATE employees SET status='active' WHERE id='emp-erf-target';`},
		{"employee_revision", "armed", `UPDATE employees SET revision=7 WHERE id='emp-erf-target';`},
		{"employee_immutable", "armed", `UPDATE employees SET note='changed' WHERE id='emp-erf-target';`},
		{"key_revocation", "armed", `UPDATE access_keys SET revoked_at=NULL WHERE id='key-erf-3';`},
		{"key_owner", "armed", `UPDATE access_keys SET employee_id='emp-erf-peer' WHERE id='key-erf-0';`},
		{"key_digest", "armed", `UPDATE access_keys SET digest=zeroblob(32) WHERE id='key-erf-3';`},
		{"key_expiry", "armed", `UPDATE access_keys SET expires_at='2030-01-01T00:00:00.000000000Z' WHERE id='key-erf-3';`},
		{"old_revocation", "armed", `UPDATE access_keys SET revoked_at='2024-01-01T00:00:00.000000000Z' WHERE id='key-erf-1';`},
		{"key_deleted", "armed", `DELETE FROM access_keys WHERE id='key-erf-3';`},
		{"key_inserted", "armed", `INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('key-erf-added','emp-erf-target','Synthetic added','erf-added',zeroblob(32),1,'erf-added','2025-01-01T00:00:00.000000000Z');`},
		{"policy_revision", "armed", `UPDATE access_key_policies SET revision=revision+1 WHERE key_id='key-erf-3';`},
		{"policy_protocol", "armed", `DELETE FROM access_key_policy_protocols WHERE key_id='key-erf-3' AND protocol='openai-chat';`},
		{"policy_models", "armed", `DELETE FROM access_key_policy_models WHERE key_id='key-erf-1';`},
		{"policy_source", "armed", `UPDATE access_key_policy_sources SET source_mode='all' WHERE key_id='key-erf-1'; DELETE FROM access_key_policy_source_cidrs WHERE key_id='key-erf-1';`},
		{"policy_cidr", "armed", `DELETE FROM access_key_policy_source_cidrs WHERE key_id='key-erf-1';`},
		{"policy_group", "armed", `UPDATE access_key_policy_account_groups SET account_group_mode='all' WHERE key_id='key-erf-1'; DELETE FROM access_key_policy_account_group_members WHERE key_id='key-erf-1';`},
		{"policy_group_member", "armed", `DELETE FROM access_key_policy_account_group_members WHERE key_id='key-erf-1';`},
		{"key_name_storage_class", "armed", `UPDATE access_keys SET name=CAST('Synthetic Key' AS BLOB) WHERE id='key-erf-3';`},
		{"key_digest_real", "armed", `UPDATE access_keys SET digest=1.25 WHERE id='key-erf-3';`},
		{"slot_rearmed", "armed", `UPDATE employee_self_key_slots SET state='armed',arm_fingerprint=zeroblob(32),arm_employee_revision=7,arm_key_policy_revision=1,arm_governance_revision=3,arm_budget_revision=4,cancelled_at=NULL WHERE employee_id='emp-erf-target';`},
		{"slot_key", "armed", `UPDATE employee_self_key_slots SET key_id='key-erf-peer' WHERE employee_id='emp-erf-target';`},
		{"slot_owner", "armed", `UPDATE employee_self_key_slots SET employee_id='emp-erf-peer' WHERE employee_id='emp-erf-target';`},
		{"issued_history", "issued", `UPDATE employee_self_key_slots SET armed_at='2024-01-01T00:00:00.000000000Z' WHERE employee_id='emp-erf-target';`},
		{"cancelled_history", "cancelled", `UPDATE employee_self_key_slots SET cancelled_at='2024-01-01T00:00:00.000000000Z' WHERE employee_id='emp-erf-target';`},
		{"self_credential", "armed", `INSERT INTO employee_self_credentials(employee_id,password_hash,updated_at) SELECT 'emp-erf-target',password_hash,'2025-01-01T00:00:00.000000000Z' FROM admins LIMIT 1;`},
		{"self_session", "armed", `INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES('erf-restored-session','emp-erf-target',zeroblob(32),'synthetic','2030-01-01T00:00:00.000000000Z','2025-01-01T00:00:00.000000000Z');`},
		{"peer_employee", "armed", `UPDATE employees SET note='changed' WHERE id='emp-erf-peer';`},
		{"peer_key", "armed", `UPDATE access_keys SET digest=zeroblob(32) WHERE id='key-erf-peer';`},
		{"peer_policy", "armed", `UPDATE access_key_policies SET revision=revision+1 WHERE key_id='key-erf-peer';`},
		{"peer_credential", "armed", `UPDATE employee_self_credentials SET updated_at='2024-01-01T00:00:00.000000000Z' WHERE employee_id='emp-erf-peer';`},
		{"peer_session", "armed", `DELETE FROM employee_self_sessions WHERE employee_id='emp-erf-peer';`},
		{"employee_model", "armed", `DELETE FROM employee_models WHERE employee_id='emp-erf-peer' AND model_id='erf-policy-model';`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newERFFixture(t)
			f.seed(t, "active", 4, tc.slot)
			for _, table := range []string{"employee_models", "access_key_policy_protocols", "access_key_policy_models", "access_key_policy_sources", "access_key_policy_source_cidrs", "access_key_policy_account_groups", "access_key_policy_account_group_members"} {
				if erfCount(t, f.store.db, "SELECT COUNT(*) FROM "+table) < 1 {
					t.Fatal("ERF_POLICY_BASELINE_EMPTY")
				}
			}
			f.auth(t, "cpac_erf-selector-3."+strings.Repeat("k", 43), 204)
			before := erfFacts(t, f.store.db, nil)
			erfExec(t, f.store.db, `CREATE TRIGGER erf_reversal AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='employee.retire_credentials' AND NEW.target_id='emp-erf-target' BEGIN `+tc.statement+` END`)
			epoch, prior := f.app.accountPool.changeSnapshot()
			erfStatus(t, f.retire(7), 503)
			erfEpoch(t, f, epoch, prior, false)
			if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
				t.Fatal("ERF_REVERSAL_NOT_ROLLED_BACK")
			}
			f.close(t)
			db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
			if !reflect.DeepEqual(before, erfFacts(t, db, nil)) {
				db.Close()
				t.Fatal("ERF_REVERSAL_PERSISTENT_FACTS")
			}
			erfCloseRO(t, db)
		})
	}
}

func TestEmployeeRetirementFinalOwnership(t *testing.T) {
	for _, name := range []string{"target_foreign_key", "reverse_foreign_slot"} {
		t.Run(name, func(t *testing.T) {
			f := newERFFixture(t)
			f.seed(t, "active", 4, "")
			if name == "target_foreign_key" {
				erfExec(t, f.store.db, "INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)", erfTarget, "key-erf-peer", utcNow())
			} else {
				erfExec(t, f.store.db, "INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)", erfPeer, "key-erf-0", utcNow())
			}
			before := erfFacts(t, f.store.db, nil)
			epoch, prior := f.app.accountPool.changeSnapshot()
			erfStatus(t, f.retire(7), 503)
			erfEpoch(t, f, epoch, prior, false)
			if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
				t.Fatal("ERF_CROSS_OWNER_MUTATION")
			}
		})
	}
}

func TestEmployeeRetirementFinalAdmission(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"anonymous", 401}, {"model_bearer", 401}, {"self_cookie", 401},
		{"origin_missing", 403}, {"origin_wrong", 403}, {"csrf_missing", 403}, {"csrf_wrong", 403},
		{"revision_stale", 409}, {"revision_zero", 400}, {"missing_target", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newERFFixture(t)
			token := f.seed(t, "active", 4, "")
			access, body := f.access(), `{"expected_revision":7}`
			path := "/admin/api/v1/employees/" + erfTarget + "/retire-credentials"
			switch tc.name {
			case "anonymous":
				access = erfAccess{}
			case "model_bearer":
				access = erfAccess{bearer: token}
			case "self_cookie":
				access = erfAccess{cookie: &http.Cookie{Name: "cpa_self_session", Value: "synthetic-self-only"}, csrf: f.csrf, origin: f.server.URL}
			case "origin_missing":
				access.origin = ""
			case "origin_wrong":
				access.origin = "http://other.invalid"
			case "csrf_missing":
				access.csrf = ""
			case "csrf_wrong":
				access.csrf = "synthetic-wrong"
			case "revision_stale":
				body = `{"expected_revision":6}`
			case "revision_zero":
				body = `{"expected_revision":0}`
			case "missing_target":
				path = "/admin/api/v1/employees/erf-absent/retire-credentials"
			}
			before := erfFacts(t, f.store.db, nil)
			epoch, prior := f.app.accountPool.changeSnapshot()
			erfStatus(t, f.call("POST", path, body, access), tc.status)
			erfEpoch(t, f, epoch, prior, false)
			if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
				t.Fatal("ERF_GUARD_MUTATED_FACTS")
			}
		})
	}
}

func TestEmployeeRetirementFinalCAS(t *testing.T) {
	f := newERFFixture(t)
	f.seed(t, "active", 4, "")
	entered, secondArrived, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	var once, releaseOnce sync.Once
	var arrivals atomic.Int32
	arrival := func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/retire-credentials") && arrivals.Add(1) == 2 {
			secondArrived <- struct{}{}
		}
	}
	f.arrived.Store(&arrival)
	f.hooks.Store(&employeeRetirementHooks{afterStep: func(stage string, tx *sql.Tx) error {
		if stage == "employee" {
			once.Do(func() { entered <- struct{}{} })
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return errors.New("ERF_CAS_BARRIER")
			}
		}
		return nil
	}})
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	results := make(chan erfReply, 2)
	var workers sync.WaitGroup
	start := func() {
		workers.Add(1)
		go func() { defer workers.Done(); var r erfReply; defer func() { results <- r }(); r = f.retire(7) }()
	}
	start()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("ERF_CAS_FIRST_BARRIER")
	}
	start()
	select {
	case <-secondArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("ERF_CAS_SECOND_BARRIER")
	}
	unblock()
	joined := make(chan struct{})
	go func() { workers.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(15 * time.Second):
		t.Fatal("ERF_CAS_JOIN")
	}
	statuses := map[int]int{}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal("ERF_CAS_HTTP")
		}
		statuses[r.status]++
	}
	if statuses[200] != 1 || statuses[409] != 1 || arrivals.Load() != 2 || len(results) != 0 {
		t.Fatal("ERF_CAS_OUTCOMES")
	}
	if erfCount(t, f.store.db, "SELECT COUNT(*) FROM account_lifecycle_audit WHERE target_id=? AND action='employee.retire_credentials'", erfTarget) != 1 {
		t.Fatal("ERF_CAS_AUDIT")
	}
}

func TestEmployeeRetirementFinalK04(t *testing.T) {
	f := newERFFixture(t)
	token := f.seed(t, "active", 4, "")
	f.auth(t, token, 204)
	path := "/admin/api/v1/employees/" + erfTarget
	erfStatus(t, f.call("PATCH", path, `{"expected_revision":7,"status":"disabled"}`, f.access()), 200)
	f.auth(t, token, 401)
	erfStatus(t, f.call("PATCH", path, `{"expected_revision":8,"status":"active"}`, f.access()), 200)
	f.auth(t, token, 204) // Ordinary disable/re-enable did not retire the old Key.
	erfStatus(t, f.retire(9), 200)
	f.auth(t, token, 401)
	created := f.call("POST", path+"/keys", `{"name":"Synthetic later allocation","operation_id":"erf-later-allocation"}`, f.access())
	erfStatus(t, created, 201)
	var next struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(created.body, &next) != nil || next.Key == "" {
		t.Fatal("ERF_LATER_KEY_CREATION")
	}
	f.auth(t, next.Key, 401) // Creation for a disabled owner remains allowed, use does not.
	erfStatus(t, f.call("PATCH", path, `{"expected_revision":10,"status":"active"}`, f.access()), 200)
	f.auth(t, token, 401)
	f.auth(t, next.Key, 204)
}

func TestEmployeeRetirementFinalCommitOutcomes(t *testing.T) {
	for _, name := range []string{"refused", "deferred_fk", "committed_error", "committed_cancel"} {
		t.Run(name, func(t *testing.T) {
			f := newERFFixture(t)
			token := f.seed(t, "active", 4, "")
			committed := name == "committed_error" || name == "committed_cancel"
			var didCancel atomic.Bool
			if name == "deferred_fk" {
				erfExec(t, f.store.db, "CREATE TABLE erf_commit_parent(id INTEGER PRIMARY KEY)")
				erfExec(t, f.store.db, "CREATE TABLE erf_commit_child(parent_id INTEGER REFERENCES erf_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)")
				f.hooks.Store(&employeeRetirementHooks{afterStep: func(stage string, tx *sql.Tx) error {
					if stage == "audit" {
						_, err := tx.Exec("INSERT INTO erf_commit_child(parent_id) VALUES(1)")
						return err
					}
					return nil
				}})
			} else {
				f.hooks.Store(&employeeRetirementHooks{commit: func(tx *sql.Tx) error {
					if committed {
						if tx.Commit() != nil {
							return errors.New("ERF_REAL_COMMIT_FAILED")
						}
					}
					if name == "committed_cancel" {
						if cancel := f.requestCancel.Load(); cancel != nil {
							(*cancel)()
							didCancel.Store(true)
						}
						return context.Canceled
					}
					return errors.New("ERF_CONTROLLED_COMMIT_RETURN")
				}})
			}
			before := erfFacts(t, f.store.db, nil)
			epoch, prior := f.app.accountPool.changeSnapshot()
			erfStatus(t, f.retire(7), 503)
			erfEpoch(t, f, epoch, prior, true) // The existing Commit-error advisory wake stays.
			f.hooks.Store(nil)
			if name == "committed_cancel" && !didCancel.Load() {
				t.Fatal("ERF_POST_COMMIT_CANCEL_NOT_EXECUTED")
			}
			if committed {
				if erfCount(t, f.store.db, "SELECT COUNT(*) FROM employees WHERE id=? AND status='disabled' AND revision=8", erfTarget) != 1 || erfCount(t, f.store.db, "SELECT COUNT(*) FROM access_keys WHERE employee_id=? AND revoked_at IS NULL", erfTarget) != 0 || erfCount(t, f.store.db, "SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id=?", erfTarget) != 0 || erfCount(t, f.store.db, "SELECT COUNT(*) FROM employee_self_sessions WHERE employee_id=?", erfTarget) != 0 {
					t.Fatal("ERF_UNCERTAIN_PERSISTED_STATE")
				}
				f.auth(t, token, 401)
				erfStatus(t, f.retire(7), 409)
			} else {
				if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
					t.Fatal("ERF_REFUSED_COMMIT_FACTS")
				}
				f.auth(t, token, 204)
			}
			persisted := erfFacts(t, f.store.db, nil)
			f.close(t)
			db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
			if !reflect.DeepEqual(persisted, erfFacts(t, db, nil)) {
				db.Close()
				t.Fatal("ERF_COMMIT_FRESH_FACTS")
			}
			if !committed && !reflect.DeepEqual(before, erfFacts(t, db, nil)) {
				db.Close()
				t.Fatal("ERF_REFUSAL_FRESH_FACTS")
			}
			erfCloseRO(t, db)
		})
	}
}

func TestEmployeeRetirementFinalLastHook(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"employee", "UPDATE employees SET status='active' WHERE id='emp-erf-target'"},
		{"key_policy", "UPDATE access_key_policies SET revision=revision+1 WHERE key_id='key-erf-3'"},
		{"self_session", "INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES('erf-last-session','emp-erf-target',zeroblob(32),'synthetic','2030-01-01T00:00:00.000000000Z','2025-01-01T00:00:00.000000000Z')"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newERFFixture(t)
			f.seed(t, "active", 4, "armed")
			before := erfFacts(t, f.store.db, nil)
			var mutated atomic.Bool
			f.hooks.Store(&employeeRetirementHooks{afterStep: func(stage string, tx *sql.Tx) error {
				if stage != "audit" {
					return nil
				}
				result, err := tx.Exec(tc.statement)
				if err != nil {
					return errors.New("ERF_LAST_HOOK_SQL")
				}
				count, err := result.RowsAffected()
				if err != nil || count != 1 {
					return errors.New("ERF_LAST_HOOK_ROWS")
				}
				mutated.Store(true)
				return nil
			}})
			epoch, prior := f.app.accountPool.changeSnapshot()
			erfStatus(t, f.retire(7), 503)
			if !mutated.Load() {
				t.Fatal("ERF_LAST_HOOK_NOT_MUTATED")
			}
			erfEpoch(t, f, epoch, prior, false)
			if !reflect.DeepEqual(before, erfFacts(t, f.store.db, nil)) {
				t.Fatal("ERF_LAST_HOOK_ROLLBACK")
			}
			f.close(t)
			db := erfReadOnly(t, filepath.Join(f.dir, "cpa-cloud.db"))
			if !reflect.DeepEqual(before, erfFacts(t, db, nil)) {
				db.Close()
				t.Fatal("ERF_LAST_HOOK_PERSISTENT_FACTS")
			}
			erfCloseRO(t, db)
		})
	}
}
