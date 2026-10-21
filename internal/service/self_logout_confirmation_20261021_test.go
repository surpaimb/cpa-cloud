// Independently authored for the new 2026-10-21 logout confirmation contract.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const slog21Time = "2006-01-02T15:04:05.000000000Z"

type slog21Cell struct {
	Kind, SQLType, Text string
	Integer             int64
	Bits                uint64
	Blob                []byte
}
type slog21Table struct {
	Columns        []string
	Metadata, Rows [][]slog21Cell
}
type slog21Snap struct {
	Schema [][]slog21Cell
	Tables map[string]slog21Table
}

func slog21Fail(t *testing.T, tag string) { t.Helper(); t.Fatal("SLOG21_UNEXPECTED_" + tag) }
func slog21Read(t *testing.T, ctx context.Context, tx *sql.Tx, q string, typed bool) ([]string, [][]slog21Cell) {
	t.Helper()
	r, err := tx.QueryContext(ctx, q)
	if err != nil {
		slog21Fail(t, "SNAPSHOT_QUERY")
	}
	defer r.Close()
	cols, err := r.Columns()
	if err != nil {
		slog21Fail(t, "SNAPSHOT_COLUMNS")
	}
	n := len(cols)
	if typed {
		if n%2 != 0 {
			slog21Fail(t, "SNAPSHOT_WIDTH")
		}
		n /= 2
	}
	rows := make([][]slog21Cell, 0)
	for r.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if r.Scan(p...) != nil {
			slog21Fail(t, "SNAPSHOT_SCAN")
		}
		row := make([]slog21Cell, n)
		for i := range row {
			c := slog21Cell{}
			switch x := v[i].(type) {
			case nil:
				c.Kind = "null"
			case int64:
				c.Kind = "int64"
				c.Integer = x
			case float64:
				c.Kind = "float64"
				c.Bits = math.Float64bits(x)
			case string:
				c.Kind = "string"
				c.Text = x
			case []byte:
				c.Kind = "blob"
				c.Blob = append([]byte{}, x...)
			default:
				slog21Fail(t, "SNAPSHOT_TYPE")
			}
			if typed {
				kind, ok := v[n+i].(string)
				if !ok {
					slog21Fail(t, "SQLITE_TYPE")
				}
				c.SQLType = kind
			}
			row[i] = c
		}
		rows = append(rows, row)
	}
	err = r.Err()
	closeErr := r.Close()
	if err != nil || closeErr != nil {
		slog21Fail(t, "SNAPSHOT_CLOSE")
	}
	sort.Slice(rows, func(i, j int) bool {
		a, _ := json.Marshal(rows[i])
		b, _ := json.Marshal(rows[j])
		return bytes.Compare(a, b) < 0
	})
	return cols[:n], rows
}
func slog21Quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func slog21Snapshot(t *testing.T, db *sql.DB) slog21Snap {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		slog21Fail(t, "SNAPSHOT_BEGIN")
	}
	defer tx.Rollback()
	_, objects := slog21Read(t, ctx, tx, `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE substr(name,1,7)<>'sqlite_'`, false)
	s := slog21Snap{Schema: objects, Tables: map[string]slog21Table{}}
	for _, o := range objects {
		if len(o) != 4 || o[0].Kind != "string" || o[1].Kind != "string" {
			slog21Fail(t, "OBJECT_TYPE")
		}
		if o[0].Text != "table" {
			continue
		}
		name := o[1].Text
		c, _ := slog21Read(t, ctx, tx, `SELECT * FROM `+slog21Quote(name)+` WHERE 0`, false)
		_, m := slog21Read(t, ctx, tx, `PRAGMA table_xinfo(`+slog21Quote(name)+`)`, false)
		if len(c) == 0 || len(c) != len(m) {
			slog21Fail(t, "COLUMN_METADATA")
		}
		expr := []string{}
		for _, col := range c {
			expr = append(expr, slog21Quote(col))
		}
		for _, col := range c {
			expr = append(expr, `typeof(`+slog21Quote(col)+`)`)
		}
		_, rows := slog21Read(t, ctx, tx, `SELECT `+strings.Join(expr, ",")+` FROM `+slog21Quote(name), true)
		s.Tables[name] = slog21Table{Columns: c, Metadata: m, Rows: rows}
	}
	if len(s.Tables) == 0 || tx.Commit() != nil {
		slog21Fail(t, "SNAPSHOT_COMMIT")
	}
	return s
}
func slog21Clone(s slog21Snap) slog21Snap {
	b, _ := json.Marshal(s)
	var c slog21Snap
	_ = json.Unmarshal(b, &c)
	return c
}
func slog21Clock(t *testing.T, s slog21Snap) time.Time {
	t.Helper()
	v, ok := s.Tables["governance_budget_clock"]
	if !ok || !reflect.DeepEqual(v.Columns, []string{"singleton", "last_effective_at"}) || len(v.Rows) != 1 || len(v.Rows[0]) != 2 {
		slog21Fail(t, "CLOCK_SHAPE")
	}
	r := v.Rows[0]
	if r[0].Kind != "int64" || r[0].SQLType != "integer" || r[0].Integer != 1 || r[1].Kind != "string" || r[1].SQLType != "text" {
		slog21Fail(t, "CLOCK_TYPE")
	}
	p, err := time.Parse(slog21Time, r[1].Text)
	if err != nil || p.Format(slog21Time) != r[1].Text {
		slog21Fail(t, "CLOCK_TIME")
	}
	return p
}
func slog21Exact(t *testing.T, a, b slog21Snap) {
	t.Helper()
	if !reflect.DeepEqual(a, b) {
		slog21Fail(t, "STRICT_FACTS_CHANGED")
	}
}
func slog21OpenWindow(t *testing.T, a, b slog21Snap, start, end time.Time) {
	t.Helper()
	x, y := slog21Clock(t, a), slog21Clock(t, b)
	if end.Before(start) || x.After(start) || y.Before(x) || (!y.Equal(x) && (y.Before(start) || y.After(end))) {
		slog21Fail(t, "CLOCK_WINDOW")
	}
	c, d := slog21Clone(a), slog21Clone(b)
	for _, s := range []*slog21Snap{&c, &d} {
		v := s.Tables["governance_budget_clock"]
		v.Rows[0][1].Text = ""
		s.Tables["governance_budget_clock"] = v
	}
	slog21Exact(t, c, d)
}

type slog21Fixture struct {
	t                                                       *testing.T
	dir                                                     string
	app                                                     *App
	closed                                                  bool
	server                                                  *httptest.Server
	client                                                  *http.Client
	admin, current, sibling, other                          *http.Cookie
	adminCSRF, csrf, employee, otherEmployee, selector, key string
	mu                                                      sync.Mutex
	transform                                               func(*http.Request) *http.Request
}

func newSlog21(t *testing.T) *slog21Fixture {
	t.Helper()
	f := &slog21Fixture{t: t, dir: t.TempDir(), client: &http.Client{Timeout: 10 * time.Second}}
	t.Cleanup(func() {
		if f.close() != nil {
			t.Error("SLOG21_UNEXPECTED_CLEANUP")
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if Initialize(ctx, f.dir, strings.NewReader("synthetic-self-logout-admin\n")) != nil {
		slog21Fail(t, "INIT")
	}
	f.open()
	r := f.call("POST", "/admin/api/v1/sessions", `{"username":"admin","password":"synthetic-self-logout-admin"}`, nil, "")
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	f.decode(r, 200, &login)
	f.admin = f.cookie(r, adminCookieName)
	f.adminCSRF = login.CSRF
	f.employee = f.createEmployee()
	f.current, f.csrf = f.enroll(f.employee)
	f.sibling, _ = f.loginSelf(f.employee)
	f.otherEmployee = f.createEmployee()
	f.other, _ = f.enroll(f.otherEmployee)
	f.selector = strings.Split(f.current.Value, ".")[0]
	r = f.call("POST", "/admin/api/v1/employees/"+f.employee+"/keys", `{"name":"synthetic-control","operation_id":"slog-key-control"}`, f.admin, f.adminCSRF)
	var key struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	f.decode(r, 201, &key)
	f.key = key.Key
	if login.CSRF == "" || key.ID == "" || f.key == "" {
		slog21Fail(t, "NONEMPTY_CONTROL")
	}
	if _, err := f.app.store.db.ExecContext(t.Context(), `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,finished_at,outcome) VALUES('slog-accounting',?,?,'synthetic-local',?,?,'succeeded')`, f.employee, key.ID, utcNow(), utcNow()); err != nil {
		slog21Fail(t, "ACCOUNTING_CONTROL")
	}
	f.controls(true)
	return f
}
func (f *slog21Fixture) open() {
	f.t.Helper()
	if f.app != nil && !f.closed {
		slog21Fail(f.t, "DOUBLE_OPEN")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	a, err := Open(ctx, Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "slog-owned"})
	if err != nil || a == nil {
		if a != nil && a.Close() != nil {
			f.t.Error("SLOG21_UNEXPECTED_PARTIAL_CLEANUP")
		}
		slog21Fail(f.t, "OPEN")
	}
	f.app = a
	f.closed = false
	h := a.Handler()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		transform := f.transform
		f.mu.Unlock()
		if transform != nil {
			r = transform(r)
		}
		h.ServeHTTP(w, r)
	}))
}
func (f *slog21Fixture) close() error {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	f.client.CloseIdleConnections()
	if f.app == nil || f.closed {
		return nil
	}
	f.closed = true
	return f.app.Close()
}
func (f *slog21Fixture) raw() *sql.DB {
	f.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "cpa-cloud.db"))
	if err != nil {
		slog21Fail(f.t, "RAW_OPEN")
	}
	db.SetMaxOpenConns(1)
	f.t.Cleanup(func() {
		if db.Close() != nil {
			f.t.Error("SLOG21_UNEXPECTED_RAW_CLEANUP")
		}
	})
	return db
}
func (f *slog21Fixture) closedRaw(before slog21Snap) *sql.DB {
	f.t.Helper()
	if f.close() != nil {
		slog21Fail(f.t, "APP_CLOSE")
	}
	db := f.raw()
	slog21Exact(f.t, before, slog21Snapshot(f.t, db))
	return db
}
func (f *slog21Fixture) openRaw(db *sql.DB) {
	f.t.Helper()
	start := time.Now().UTC()
	before := slog21Snapshot(f.t, db)
	if db.Close() != nil {
		slog21Fail(f.t, "RAW_CLOSE")
	}
	f.open()
	after := slog21Snapshot(f.t, f.app.store.db)
	slog21OpenWindow(f.t, before, after, start, time.Now().UTC())
}
func (f *slog21Fixture) reopen() {
	f.t.Helper()
	before := slog21Snapshot(f.t, f.app.store.db)
	db := f.closedRaw(before)
	f.mu.Lock()
	f.transform = nil
	f.mu.Unlock()
	f.openRaw(db)
}
func (f *slog21Fixture) finish(r *http.Response) {
	f.t.Helper()
	_, err := io.Copy(io.Discard, io.LimitReader(r.Body, 65536))
	closeErr := r.Body.Close()
	if err != nil || closeErr != nil {
		slog21Fail(f.t, "RESPONSE_CLOSE")
	}
}
func (f *slog21Fixture) call(method, path, body string, cookie *http.Cookie, csrf string) *http.Response {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		slog21Fail(f.t, "REQUEST")
	}
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-Self-Request", "1")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := f.client.Do(r)
	if err != nil {
		slog21Fail(f.t, "HTTP")
	}
	return response
}
func (f *slog21Fixture) decode(r *http.Response, status int, body any) {
	f.t.Helper()
	defer f.finish(r)
	if r.StatusCode != status || json.NewDecoder(io.LimitReader(r.Body, 32768)).Decode(body) != nil {
		slog21Fail(f.t, "RESPONSE")
	}
}
func (f *slog21Fixture) cookie(r *http.Response, name string) *http.Cookie {
	f.t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == name && c.Value != "" {
			copy := *c
			return &copy
		}
	}
	slog21Fail(f.t, "COOKIE")
	return nil
}
func (f *slog21Fixture) createEmployee() string {
	r := f.call("POST", "/admin/api/v1/employees", `{"name":"Synthetic logout employee"}`, f.admin, f.adminCSRF)
	var b struct {
		ID string `json:"id"`
	}
	f.decode(r, 201, &b)
	if b.ID == "" {
		slog21Fail(f.t, "EMPLOYEE")
	}
	return b.ID
}
func (f *slog21Fixture) enroll(id string) (*http.Cookie, string) {
	r := f.call("POST", "/admin/api/v1/employees/"+id+"/self-enrollment", `{}`, f.admin, f.adminCSRF)
	var e struct {
		Secret string `json:"enrollment_secret"`
	}
	f.decode(r, 201, &e)
	if e.Secret == "" {
		slog21Fail(f.t, "ENROLLMENT")
	}
	r = f.call("POST", "/self/api/v1/enroll", `{"employee_id":`+quoteJSON(id)+`,"enrollment_secret":`+quoteJSON(e.Secret)+`,"password":"synthetic-self-logout-password"}`, nil, "")
	var b struct {
		CSRF string `json:"csrf_token"`
	}
	f.decode(r, 200, &b)
	if b.CSRF == "" {
		slog21Fail(f.t, "ENROLL_CSRF")
	}
	return f.cookie(r, selfCookieName), b.CSRF
}
func (f *slog21Fixture) loginSelf(id string) (*http.Cookie, string) {
	r := f.call("POST", "/self/api/v1/sessions", `{"employee_id":`+quoteJSON(id)+`,"password":"synthetic-self-logout-password"}`, nil, "")
	var b struct {
		CSRF string `json:"csrf_token"`
	}
	f.decode(r, 200, &b)
	if b.CSRF == "" {
		slog21Fail(f.t, "LOGIN_CSRF")
	}
	return f.cookie(r, selfCookieName), b.CSRF
}
func (f *slog21Fixture) profile(c *http.Cookie, id string, status int) {
	f.t.Helper()
	r := f.call("GET", "/self/api/v1/profile", "", c, "")
	if r.StatusCode != status || len(r.Header.Values("Set-Cookie")) != 0 || r.Header.Get("Cache-Control") != "no-store" {
		f.finish(r)
		slog21Fail(f.t, "REPLAY_AUTH")
	}
	if status == 200 {
		var p selfProfile
		f.decode(r, 200, &p)
		if p.ID != id || p.Status != "active" {
			slog21Fail(f.t, "PROFILE_IDENTITY")
		}
	} else {
		f.finish(r)
	}
}
func (f *slog21Fixture) controls(current bool) {
	f.t.Helper()
	if current {
		f.profile(f.current, f.employee, 200)
	}
	f.profile(f.sibling, f.employee, 200)
	f.profile(f.other, f.otherEmployee, 200)
	r := f.call("GET", "/admin/api/v1/session", "", f.admin, "")
	if r.StatusCode != 200 {
		f.finish(r)
		slog21Fail(f.t, "ADMIN_CONTROL")
	}
	f.finish(r)
	req, err := http.NewRequestWithContext(f.t.Context(), "GET", f.server.URL+"/v1/models", nil)
	if err != nil {
		slog21Fail(f.t, "KEY_REQUEST")
	}
	req.Header.Set("Authorization", "Bearer "+f.key)
	r, err = f.client.Do(req)
	if err != nil {
		slog21Fail(f.t, "KEY_HTTP")
	}
	if r.StatusCode != 200 {
		f.finish(r)
		slog21Fail(f.t, "KEY_CONTROL")
	}
	f.finish(r)
}
func (f *slog21Fixture) controlWindow(current bool) {
	before := slog21Snapshot(f.t, f.app.store.db)
	f.controls(current)
	slog21Exact(f.t, before, slog21Snapshot(f.t, f.app.store.db))
}
func (f *slog21Fixture) target(s slog21Snap, required bool) []slog21Cell {
	f.t.Helper()
	v, ok := s.Tables["employee_self_sessions"]
	if !ok || !reflect.DeepEqual(v.Columns, []string{"selector", "employee_id", "verifier_digest", "csrf_token", "expires_at", "created_at"}) {
		slog21Fail(f.t, "TARGET_COLUMNS")
	}
	var row []slog21Cell
	for _, r := range v.Rows {
		if len(r) != 6 {
			slog21Fail(f.t, "TARGET_WIDTH")
		}
		if r[0].Kind == "string" && r[0].SQLType == "text" && r[0].Text == f.selector {
			if row != nil {
				slog21Fail(f.t, "TARGET_MULTIPLE")
			}
			row = r
		}
	}
	if (row != nil) != required {
		slog21Fail(f.t, "TARGET_PRESENCE")
	}
	return row
}
func (f *slog21Fixture) install(kind string) {
	f.t.Helper()
	before := slog21Snapshot(f.t, f.app.store.db)
	db := f.closedRaw(before)
	pre := slog21Snapshot(f.t, db)
	name := "self_logout_21_" + kind
	condition := ` WHEN OLD.selector='` + strings.ReplaceAll(f.selector, "'", "''") + `' `
	var statement string
	switch kind {
	case "ignore":
		statement = `CREATE TRIGGER ` + name + ` BEFORE DELETE ON employee_self_sessions` + condition + `BEGIN SELECT RAISE(IGNORE); END`
	case "reinsert":
		statement = `CREATE TRIGGER ` + name + ` AFTER DELETE ON employee_self_sessions` + condition + `BEGIN INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES(OLD.selector,OLD.employee_id,OLD.verifier_digest,OLD.csrf_token,OLD.expires_at,OLD.created_at); END`
	case "abort":
		statement = `CREATE TRIGGER ` + name + ` BEFORE DELETE ON employee_self_sessions` + condition + `BEGIN SELECT RAISE(ABORT,'controlled'); END`
	default:
		db.Close()
		slog21Fail(f.t, "TRIGGER_KIND")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, statement)
	if err != nil {
		db.Close()
		slog21Fail(f.t, "TRIGGER_INSTALL")
	}
	post := slog21Snapshot(f.t, db)
	if !reflect.DeepEqual(pre.Tables, post.Tables) || len(post.Schema) != len(pre.Schema)+1 {
		db.Close()
		slog21Fail(f.t, "DDL_ROWS_OR_COUNT")
	}
	filtered := [][]slog21Cell{}
	found := 0
	for _, o := range post.Schema {
		if o[0].Text == "trigger" && o[1].Text == name {
			found++
			if o[2].Text != "employee_self_sessions" || o[3].Kind != "string" || strings.TrimSpace(o[3].Text) != statement {
				db.Close()
				slog21Fail(f.t, "TRIGGER_MANIFEST")
			}
		} else {
			filtered = append(filtered, o)
		}
	}
	if found != 1 || !reflect.DeepEqual(filtered, pre.Schema) {
		db.Close()
		slog21Fail(f.t, "DDL_MANIFEST")
	}
	f.openRaw(db)
}
func (f *slog21Fixture) assert204(r *http.Response) {
	f.t.Helper()
	defer f.finish(r)
	if r.StatusCode != 204 || len(r.Header.Values("Set-Cookie")) != 1 || len(r.Cookies()) != 1 || r.Header.Get("Cache-Control") != "no-store" {
		slog21Fail(f.t, "204")
	}
	c := r.Cookies()[0]
	if c.Name != selfCookieName || c.Value != "" || c.Path != "/self/" || c.MaxAge != -1 || !c.HttpOnly || c.Secure || c.SameSite != http.SameSiteStrictMode {
		slog21Fail(f.t, "CLEAR_COOKIE")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 2))
	if err != nil || len(b) != 0 {
		slog21Fail(f.t, "204_BODY")
	}
}
func (f *slog21Fixture) assert503(r *http.Response) {
	f.t.Helper()
	defer f.finish(r)
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if r.StatusCode != 503 || len(r.Header.Values("Set-Cookie")) != 0 || r.Header.Get("Cache-Control") != "no-store" || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b) != nil || b.Error.Code != "storage_unavailable" || b.Error.Message != "Request could not be completed." {
		slog21Fail(f.t, "FIXED_503")
	}
}
func (f *slog21Fixture) clean() {
	f.t.Helper()
	s := slog21Snapshot(f.t, f.app.store.db)
	db := f.closedRaw(s)
	if db.Close() != nil {
		slog21Fail(f.t, "FINAL_RAW_CLOSE")
	}
}

// Frozen before A; kept unchanged for B. Each case has an independent new DB.
func TestSelfLogout20261021JointOracle(t *testing.T) {
	old := 0
	for _, kind := range []string{"ignore", "reinsert"} {
		f := newSlog21(t)
		f.controlWindow(true)
		f.install(kind)
		f.controlWindow(true)
		before := slog21Snapshot(t, f.app.store.db)
		target := f.target(before, true)
		r := f.call("DELETE", "/self/api/v1/sessions", "", f.current, f.csrf)
		if r.StatusCode == 204 {
			f.assert204(r)
			old++
		} else {
			f.assert503(r)
		}
		after := slog21Snapshot(t, f.app.store.db)
		if !reflect.DeepEqual(target, f.target(after, true)) {
			slog21Fail(t, "TARGET_ROW_CHANGED")
		}
		slog21Exact(t, before, after)
		f.controlWindow(true)
		f.reopen()
		f.controlWindow(true)
		if !reflect.DeepEqual(target, f.target(slog21Snapshot(t, f.app.store.db), true)) {
			slog21Fail(t, "REOPEN_TARGET")
		}
		f.clean()
		t.Logf("SLOG21_CASE phase=%s complete=true cleanup=true", kind)
	}
	if old == 2 {
		t.Fatal("SELF_LOGOUT_20261021_OLD_204_DURABLE_REPLAY")
	}
	if old != 0 {
		slog21Fail(t, "MIXED_JOINT_WITNESS")
	}
}

// Stage B: appended only after the complete frozen A expected-red gate.
func (f *slog21Fixture) hooks(h selfLogoutOperationHooks) {
	f.mu.Lock()
	f.transform = func(r *http.Request) *http.Request {
		if r.Method == "DELETE" && r.URL.Path == "/self/api/v1/sessions" {
			return r.WithContext(context.WithValue(r.Context(), selfLogoutContextKey{}, h))
		}
		return r
	}
	f.mu.Unlock()
}

func (f *slog21Fixture) expectedAfter(before slog21Snap, present bool) {
	f.t.Helper()
	after := slog21Snapshot(f.t, f.app.store.db)
	original := f.target(before, true)
	if present {
		slog21Exact(f.t, before, after)
	} else {
		want := slog21Clone(before)
		v := want.Tables["employee_self_sessions"]
		rows := make([][]slog21Cell, 0)
		for _, r := range v.Rows {
			if r[0].Text != f.selector {
				rows = append(rows, r)
			}
		}
		if len(rows) != len(v.Rows)-1 {
			slog21Fail(f.t, "EXPECTED_SINGLE_DELETE")
		}
		v.Rows = rows
		want.Tables["employee_self_sessions"] = v
		slog21Exact(f.t, want, after)
	}
	f.target(after, present)
	status := 401
	if present {
		status = 200
	}
	f.profile(f.current, f.employee, status)
	f.controlWindow(false)
	f.reopen()
	f.profile(f.current, f.employee, status)
	f.controlWindow(false)
	if present && !reflect.DeepEqual(original, f.target(slog21Snapshot(f.t, f.app.store.db), true)) {
		slog21Fail(f.t, "DURABLE_TARGET")
	}
}

func TestSelfLogout20261021Normal(t *testing.T) {
	for _, scenario := range []string{"empty", "ignored-body-query"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSlog21(t)
			before := slog21Snapshot(t, f.app.store.db)
			path, body := "/self/api/v1/sessions", ""
			if scenario != "empty" {
				path += "?ignored=1"
				body = "not a JSON object"
			}
			f.assert204(f.call("DELETE", path, body, f.current, f.csrf))
			f.expectedAfter(before, false)
			f.clean()
		})
	}
}

func TestSelfLogout20261021NativeAbort(t *testing.T) {
	f := newSlog21(t)
	f.install("abort")
	before := slog21Snapshot(t, f.app.store.db)
	var resultCalled atomic.Bool
	f.hooks(selfLogoutOperationHooks{result: func(r sql.Result) sql.Result { resultCalled.Store(true); return r }})
	f.assert503(f.call("DELETE", "/self/api/v1/sessions", "", f.current, f.csrf))
	if resultCalled.Load() {
		slog21Fail(t, "REAL_SQL_ERROR_ERASED")
	}
	f.expectedAfter(before, true)
	f.clean()
}

type slog21Result struct {
	sql.Result
	count int64
	err   error
}

func (r slog21Result) RowsAffected() (int64, error) { return r.count, r.err }

func TestSelfLogout20261021Boundaries(t *testing.T) {
	for _, scenario := range []string{"begin-refusal", "begin-cancelled", "nil-result", "rows-error", "zero-rows", "multiple-rows", "employee-predicate", "verifier-predicate", "confirmation-query-error", "replacement-employee", "replacement-verifier", "pre-commit-refusal", "post-commit-report"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSlog21(t)
			before := slog21Snapshot(t, f.app.store.db)
			row := f.target(before, true)
			h := selfLogoutOperationHooks{}
			present := true
			var committed atomic.Bool
			switch scenario {
			case "begin-refusal":
				h.beforeBegin = func(context.Context) error { return sql.ErrConnDone }
			case "begin-cancelled":
				f.mu.Lock()
				f.transform = func(r *http.Request) *http.Request {
					if r.Method != "DELETE" {
						return r
					}
					ctx, cancel := context.WithCancel(r.Context())
					hooks := selfLogoutOperationHooks{beforeBegin: func(context.Context) error { cancel(); return nil }}
					return r.WithContext(context.WithValue(ctx, selfLogoutContextKey{}, hooks))
				}
				f.mu.Unlock()
			case "nil-result":
				h.result = func(sql.Result) sql.Result { return nil }
			case "rows-error":
				h.result = func(r sql.Result) sql.Result { return slog21Result{Result: r, count: 1, err: sql.ErrConnDone} }
			case "zero-rows":
				h.result = func(r sql.Result) sql.Result { return slog21Result{Result: r, count: 0} }
			case "multiple-rows":
				h.result = func(r sql.Result) sql.Result { return slog21Result{Result: r, count: 2} }
			case "employee-predicate":
				h.afterBegin = func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), `UPDATE employee_self_sessions SET employee_id=? WHERE selector=?`, f.otherEmployee, f.selector)
					return err
				}
			case "verifier-predicate":
				h.afterBegin = func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), `UPDATE employee_self_sessions SET verifier_digest=? WHERE selector=?`, []byte("synthetic-different-verifier"), f.selector)
					return err
				}
			case "confirmation-query-error":
				h.beforeConfirm = func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), `ALTER TABLE employee_self_sessions RENAME TO self_logout_21_hidden`)
					return err
				}
			case "replacement-employee", "replacement-verifier":
				h.beforeConfirm = func(tx *sql.Tx) error {
					owner, digest := f.employee, row[2].Blob
					if scenario == "replacement-employee" {
						owner = f.otherEmployee
					} else {
						digest = []byte("synthetic-replacement-verifier")
					}
					_, err := tx.ExecContext(t.Context(), `INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`, f.selector, owner, digest, row[3].Text, row[4].Text, row[5].Text)
					return err
				}
			case "pre-commit-refusal":
				h.beforeCommit = func(*sql.Tx) error { return sql.ErrConnDone }
			case "post-commit-report":
				present = false
				h.afterCommit = func(err error) error { committed.Store(err == nil); return sql.ErrConnDone }
			}
			if scenario != "begin-cancelled" {
				f.hooks(h)
			}
			f.assert503(f.call("DELETE", "/self/api/v1/sessions", "", f.current, f.csrf))
			if scenario == "post-commit-report" && !committed.Load() {
				slog21Fail(t, "REAL_COMMIT_NOT_CONFIRMED")
			}
			f.expectedAfter(before, present)
			f.clean()
		})
	}
}

func TestSelfLogout20261021DeferredFK(t *testing.T) {
	f := newSlog21(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var enabled int
	if f.app.store.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled) != nil || enabled != 1 {
		slog21Fail(t, "FK_PRECONDITION")
	}
	if _, err := f.app.store.db.ExecContext(ctx, `CREATE TABLE self_logout_21_guard(selector TEXT REFERENCES employee_self_sessions(selector) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		slog21Fail(t, "FK_CREATE")
	}
	if _, err := f.app.store.db.ExecContext(ctx, `INSERT INTO self_logout_21_guard(selector) VALUES(?)`, f.selector); err != nil {
		slog21Fail(t, "FK_INSERT")
	}
	before := slog21Snapshot(t, f.app.store.db)
	live := f.app
	var observed, rejected atomic.Bool
	f.hooks(selfLogoutOperationHooks{afterCommit: func(err error) error { observed.Store(true); rejected.Store(err != nil); return nil }})
	f.assert503(f.call("DELETE", "/self/api/v1/sessions", "", f.current, f.csrf))
	if !observed.Load() || !rejected.Load() {
		slog21Fail(t, "NATIVE_COMMIT_NOT_REJECTED")
	}
	// Same live App, no manual connection repair/reopen/ROLLBACK of the failed
	// operation: a genuinely new read/write transaction must be usable now.
	tx, err := f.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		slog21Fail(t, "LIVE_NEXT_BEGIN")
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled) != nil || enabled != 1 || tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM self_logout_21_guard WHERE selector=?`, f.selector).Scan(&count) != nil || count != 1 {
		slog21Fail(t, "LIVE_NEXT_READ")
	}
	r, err := tx.ExecContext(ctx, `UPDATE self_logout_21_guard SET selector=selector WHERE selector=?`, f.selector)
	if err != nil || r == nil {
		slog21Fail(t, "LIVE_NEXT_WRITE")
	}
	n, err := r.RowsAffected()
	if err != nil || n != 1 || tx.Commit() != nil || f.app != live || f.closed {
		slog21Fail(t, "LIVE_NEXT_COMMIT")
	}
	f.expectedAfter(before, true)
	f.clean()
}

func TestSelfLogout20261021AuthBoundaries(t *testing.T) {
	f := newSlog21(t)
	before := slog21Snapshot(t, f.app.store.db)
	var entered atomic.Bool
	f.hooks(selfLogoutOperationHooks{beforeBegin: func(context.Context) error { entered.Store(true); return sql.ErrConnDone }})
	for _, scenario := range []struct {
		name   string
		status int
	}{{"missing-cookie", 401}, {"missing-csrf", 403}, {"wrong-csrf", 403}, {"missing-origin", 403}, {"foreign-origin", 403}, {"foreign-origin-before-auth", 403}, {"duplicate-origin", 403}, {"missing-self-request", 403}, {"admin-cookie", 401}, {"model-key-only", 401}} {
		t.Run(scenario.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), "DELETE", f.server.URL+"/self/api/v1/sessions", nil)
			if err != nil {
				slog21Fail(t, "AUTH_REQUEST")
			}
			req.Header.Set("Origin", f.server.URL)
			req.Header.Set("X-Self-Request", "1")
			req.Header.Set("X-CSRF-Token", f.csrf)
			switch scenario.name {
			case "missing-cookie", "model-key-only", "foreign-origin-before-auth":
			case "admin-cookie":
				req.AddCookie(f.admin)
			default:
				req.AddCookie(f.current)
			}
			switch scenario.name {
			case "missing-csrf":
				req.Header.Del("X-CSRF-Token")
			case "wrong-csrf":
				req.Header.Set("X-CSRF-Token", "synthetic-wrong")
			case "missing-origin":
				req.Header.Del("Origin")
			case "foreign-origin", "foreign-origin-before-auth":
				req.Header.Set("Origin", "http://unrelated.invalid")
			case "duplicate-origin":
				req.Header.Add("Origin", f.server.URL)
			case "missing-self-request":
				req.Header.Del("X-Self-Request")
			case "model-key-only":
				req.Header.Set("Authorization", "Bearer "+f.key)
			}
			r, err := f.client.Do(req)
			if err != nil {
				slog21Fail(t, "AUTH_HTTP")
			}
			defer f.finish(r)
			if r.StatusCode != scenario.status || len(r.Header.Values("Set-Cookie")) != 0 || r.Header.Get("Cache-Control") != "no-store" || entered.Load() {
				slog21Fail(t, "AUTH_PRIORITY")
			}
		})
	}
	slog21Exact(t, before, slog21Snapshot(t, f.app.store.db))
	f.clean()
}

func TestSelfLogout20261021TwoAuthenticated(t *testing.T) {
	f := newSlog21(t)
	before := slog21Snapshot(t, f.app.store.db)
	arrived := make(chan string, 2)
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var onceA, onceB sync.Once
	defer onceA.Do(func() { close(releaseA) })
	defer onceB.Do(func() { close(releaseB) })
	f.mu.Lock()
	f.transform = func(r *http.Request) *http.Request {
		if r.Method != "DELETE" {
			return r
		}
		id := r.Header.Get("X-Slog21-Request")
		release := releaseA
		if id == "B" {
			release = releaseB
		}
		h := selfLogoutOperationHooks{beforeBegin: func(ctx context.Context) error {
			select {
			case arrived <- id:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
		return r.WithContext(context.WithValue(r.Context(), selfLogoutContextKey{}, h))
	}
	f.mu.Unlock()
	type reply struct {
		r   *http.Response
		err error
	}
	a, b := make(chan reply, 1), make(chan reply, 1)
	launch := func(id string, out chan reply) {
		req, err := http.NewRequestWithContext(t.Context(), "DELETE", f.server.URL+"/self/api/v1/sessions", nil)
		if err != nil {
			slog21Fail(t, "BARRIER_REQUEST")
		}
		req.AddCookie(f.current)
		req.Header.Set("Origin", f.server.URL)
		req.Header.Set("X-Self-Request", "1")
		req.Header.Set("X-CSRF-Token", f.csrf)
		req.Header.Set("X-Slog21-Request", id)
		go func() { r, err := f.client.Do(req); out <- reply{r, err} }()
	}
	launch("A", a)
	launch("B", b)
	seen := map[string]bool{}
	for range 2 {
		select {
		case id := <-arrived:
			if (id != "A" && id != "B") || seen[id] {
				slog21Fail(t, "BARRIER_ID")
			}
			seen[id] = true
		case <-time.After(10 * time.Second):
			slog21Fail(t, "BARRIER_TIMEOUT")
		}
	}
	onceA.Do(func() { close(releaseA) })
	select {
	case r := <-a:
		if r.err != nil {
			slog21Fail(t, "BARRIER_A_HTTP")
		}
		f.assert204(r.r)
	case <-time.After(10 * time.Second):
		slog21Fail(t, "BARRIER_A_TIMEOUT")
	}
	onceB.Do(func() { close(releaseB) })
	select {
	case r := <-b:
		if r.err != nil {
			slog21Fail(t, "BARRIER_B_HTTP")
		}
		f.assert503(r.r)
	case <-time.After(10 * time.Second):
		slog21Fail(t, "BARRIER_B_TIMEOUT")
	}
	// A new authentication happens after Commit and is rejected before the hook.
	r := f.call("DELETE", "/self/api/v1/sessions", "", f.current, f.csrf)
	if r.StatusCode != 401 || len(r.Header.Values("Set-Cookie")) != 0 {
		f.finish(r)
		slog21Fail(t, "LATER_FRONT_AUTH")
	}
	f.finish(r)
	f.expectedAfter(before, false)
	f.clean()
}
