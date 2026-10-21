// Independently authored for the new 2026-10-21 logout confirmation contract.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	diagSQLite "modernc.org/sqlite"
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
func slog21Read(t *testing.T, ctx context.Context, tx *sql.Tx, q string, typed bool, diag *slog21DiagTrace, query slog21DiagQuery) ([]string, [][]slog21Cell) {
	t.Helper()
	queryStarted := time.Now()
	r, err := tx.QueryContext(ctx, q)
	if err != nil {
		diag.capture(slog21DiagContext(ctx), query, slog21DiagQueryContext, err, queryStarted, false).emit(t)
		slog21Fail(t, "SNAPSHOT_QUERY")
	}
	defer diag.closeRows(t, ctx, r, query, queryStarted)
	cols, err := r.Columns()
	if err != nil {
		diag.capture(slog21DiagContext(ctx), query, slog21DiagColumns, err, queryStarted, false).emit(t)
		slog21Fail(t, "SNAPSHOT_COLUMNS")
	}
	n := len(cols)
	if typed {
		if n%2 != 0 {
			diag.internal(ctx, query, slog21DiagWidth, queryStarted).emit(t)
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
		if scanErr := r.Scan(p...); scanErr != nil {
			diag.capture(slog21DiagContext(ctx), query, slog21DiagScan, scanErr, queryStarted, false).emit(t)
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
				diag.internal(ctx, query, slog21DiagValueType, queryStarted).emit(t)
				slog21Fail(t, "SNAPSHOT_TYPE")
			}
			if typed {
				kind, ok := v[n+i].(string)
				if !ok {
					diag.internal(ctx, query, slog21DiagSQLiteType, queryStarted).emit(t)
					slog21Fail(t, "SQLITE_TYPE")
				}
				c.SQLType = kind
			}
			row[i] = c
		}
		rows = append(rows, row)
	}
	err = r.Err()
	var iterationRecord, closeRecord slog21DiagRecord
	if err != nil {
		iterationRecord = diag.capture(slog21DiagContext(ctx), query, slog21DiagRowsErr, err, queryStarted, false)
	}
	closeContext := slog21DiagContext(ctx)
	closeErr := r.Close()
	if closeErr != nil {
		closeRecord = diag.capture(closeContext, query, slog21DiagRowsClose, closeErr, queryStarted, true)
	}
	if err != nil || closeErr != nil {
		if err != nil {
			iterationRecord.emit(t)
		}
		if closeErr != nil {
			closeRecord.emit(t)
		}
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
	diag := &slog21DiagTrace{started: time.Now(), cut: slog21DiagLocate()}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		diag.capture(slog21DiagContext(ctx), slog21DiagNoQuery, slog21DiagBegin, err, diag.started, false).emit(t)
		slog21Fail(t, "SNAPSHOT_BEGIN")
	}
	defer diag.rollback(t, ctx, tx)
	_, objects := slog21Read(t, ctx, tx, `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE substr(name,1,7)<>'sqlite_'`, false, diag, slog21DiagObjects)
	s := slog21Snap{Schema: objects, Tables: map[string]slog21Table{}}
	for _, o := range objects {
		if len(o) != 4 || o[0].Kind != "string" || o[1].Kind != "string" {
			diag.internal(ctx, slog21DiagObjects, slog21DiagObjectType, diag.started).emit(t)
			slog21Fail(t, "OBJECT_TYPE")
		}
		if o[0].Text != "table" {
			continue
		}
		name := o[1].Text
		c, _ := slog21Read(t, ctx, tx, `SELECT * FROM `+slog21Quote(name)+` WHERE 0`, false, diag, slog21DiagDiscovery)
		_, m := slog21Read(t, ctx, tx, `PRAGMA table_xinfo(`+slog21Quote(name)+`)`, false, diag, slog21DiagDeclaration)
		if len(c) == 0 || len(c) != len(m) {
			diag.internal(ctx, slog21DiagDeclaration, slog21DiagMetadata, diag.started).emit(t)
			slog21Fail(t, "COLUMN_METADATA")
		}
		expr := []string{}
		for _, col := range c {
			expr = append(expr, slog21Quote(col))
		}
		for _, col := range c {
			expr = append(expr, `typeof(`+slog21Quote(col)+`)`)
		}
		_, rows := slog21Read(t, ctx, tx, `SELECT `+strings.Join(expr, ",")+` FROM `+slog21Quote(name), true, diag, slog21DiagTypedRows)
		s.Tables[name] = slog21Table{Columns: c, Metadata: m, Rows: rows}
	}
	if len(s.Tables) == 0 || diag.commit(t, ctx, tx) != nil {
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

// Diagnostic-only observers for docs/employee-self-logout-query-diagnostic-2026-10-21.md.
// No record contains an error, SQL, object name, value, identity or runtime path.
type slog21DiagCut uint8

const (
	slog21CutUnknown slog21DiagCut = iota
	slog21CutControlsBefore
	slog21CutControlsAfter
	slog21CutReopenBeforeClose
	slog21CutAfterClose
	slog21CutBeforeOpen
	slog21CutAfterOpen
	slog21CutTriggerBeforeClose
	slog21CutDDLBefore
	slog21CutDDLAfter
	slog21CutDeleteBefore
	slog21CutDeleteAfter
	slog21CutOracleTarget
	slog21CutPostResponse
	slog21CutPersistedTarget
	slog21CutCleanupBefore
	slog21CutNormalBefore
	slog21CutAbortBefore
	slog21CutBoundaryBefore
	slog21CutFKBefore
	slog21CutAuthBefore
	slog21CutAuthAfter
	slog21CutDualBefore
)

type slog21DiagQuery uint8

const (
	slog21DiagNoQuery slog21DiagQuery = iota
	slog21DiagObjects
	slog21DiagDiscovery
	slog21DiagDeclaration
	slog21DiagTypedRows
)

type slog21DiagOp uint8

const (
	slog21DiagUnknownOp slog21DiagOp = iota
	slog21DiagBegin
	slog21DiagQueryContext
	slog21DiagColumns
	slog21DiagWidth
	slog21DiagScan
	slog21DiagValueType
	slog21DiagSQLiteType
	slog21DiagRowsErr
	slog21DiagRowsClose
	slog21DiagObjectType
	slog21DiagMetadata
	slog21DiagCommit
	slog21DiagRollback
)

type slog21DiagCtx uint8
type slog21DiagErr uint8
type slog21DiagCode uint8
type slog21DiagBucket uint8

const (
	slog21CtxNone slog21DiagCtx = iota
	slog21CtxDeadline
	slog21CtxCancelled
	slog21CtxOther
)
const (
	slog21ErrUnknown slog21DiagErr = iota
	slog21ErrNone
	slog21ErrDeadline
	slog21ErrCancelled
	slog21ErrTxDone
	slog21ErrConnDone
	slog21ErrSQLite
)
const (
	slog21CodeNone slog21DiagCode = iota
	slog21CodeUnknown
	slog21Code1
	slog21Code4
	slog21Code5
	slog21Code6
	slog21Code9
	slog21Code10
	slog21Code19
	slog21Code261
	slog21Code517
	slog21Code773
	slog21Code262
	slog21Code518
	slog21Code4106
)

func slog21DiagLabel(index int, labels []string) string {
	if index < 0 || index >= len(labels) {
		return "UNKNOWN"
	}
	return labels[index]
}
func slog21DiagSQLiteCode(code int) slog21DiagCode {
	switch code {
	case 1:
		return slog21Code1
	case 4:
		return slog21Code4
	case 5:
		return slog21Code5
	case 6:
		return slog21Code6
	case 9:
		return slog21Code9
	case 10:
		return slog21Code10
	case 19:
		return slog21Code19
	case 261:
		return slog21Code261
	case 517:
		return slog21Code517
	case 773:
		return slog21Code773
	case 262:
		return slog21Code262
	case 518:
		return slog21Code518
	case 4106:
		return slog21Code4106
	default:
		return slog21CodeUnknown
	}
}
func slog21DiagError(err error) (slog21DiagErr, slog21DiagCode) {
	switch {
	case err == nil:
		return slog21ErrNone, slog21CodeNone
	case errors.Is(err, context.DeadlineExceeded):
		return slog21ErrDeadline, slog21CodeNone
	case errors.Is(err, context.Canceled):
		return slog21ErrCancelled, slog21CodeNone
	case errors.Is(err, sql.ErrTxDone):
		return slog21ErrTxDone, slog21CodeNone
	case errors.Is(err, sql.ErrConnDone):
		return slog21ErrConnDone, slog21CodeNone
	}
	var native *diagSQLite.Error
	if errors.As(err, &native) && native != nil {
		return slog21ErrSQLite, slog21DiagSQLiteCode(native.Code())
	}
	return slog21ErrUnknown, slog21CodeNone
}
func slog21DiagContext(ctx context.Context) slog21DiagCtx {
	err := ctx.Err()
	switch {
	case err == nil:
		return slog21CtxNone
	case errors.Is(err, context.DeadlineExceeded):
		return slog21CtxDeadline
	case errors.Is(err, context.Canceled):
		return slog21CtxCancelled
	default:
		return slog21CtxOther
	}
}
func slog21DiagElapsed(d time.Duration) slog21DiagBucket {
	switch {
	case d < 0:
		return 0
	case d < time.Millisecond:
		return 1
	case d < 100*time.Millisecond:
		return 2
	case d < time.Second:
		return 3
	case d < 10*time.Second:
		return 4
	case d < time.Minute:
		return 5
	default:
		return 6
	}
}

type slog21DiagRecord struct {
	cut                           slog21DiagCut
	query                         slog21DiagQuery
	op                            slog21DiagOp
	context                       slog21DiagCtx
	err                           slog21DiagErr
	sqlite                        slog21DiagCode
	snapshotElapsed, queryElapsed slog21DiagBucket
	cleanup, primary              bool
}

func slog21DiagBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
func (r slog21DiagRecord) text() string {
	return "SLOG21_SNAPSHOT_DIAGNOSTIC" +
		" cut=" + slog21DiagLabel(int(r.cut), []string{"UNKNOWN", "CONTROLS_BEFORE", "CONTROLS_AFTER", "REOPEN_BEFORE_CLOSE", "AFTER_APP_CLOSE", "BEFORE_OPEN", "AFTER_OPEN", "TRIGGER_BEFORE_CLOSE", "DDL_BEFORE", "DDL_AFTER", "DELETE_BEFORE", "DELETE_AFTER", "ORACLE_REOPENED_TARGET", "POST_RESPONSE", "PERSISTED_TARGET", "CLEANUP_BEFORE_CLOSE", "NORMAL_BEFORE", "ABORT_BEFORE", "BOUNDARY_BEFORE", "FK_BEFORE", "AUTH_BEFORE", "AUTH_AFTER", "DUAL_BEFORE"}) +
		" query=" + slog21DiagLabel(int(r.query), []string{"NONE", "OBJECT_ENUMERATION", "COLUMN_DISCOVERY", "COLUMN_DECLARATION", "TYPED_ROWS"}) +
		" op=" + slog21DiagLabel(int(r.op), []string{"UNKNOWN", "BEGIN", "QUERY_CONTEXT", "COLUMNS", "WIDTH", "SCAN", "VALUE_TYPE", "SQLITE_TYPE", "ROWS_ERR", "ROWS_CLOSE", "OBJECT_TYPE", "COLUMN_METADATA", "COMMIT", "ROLLBACK"}) +
		" context=" + slog21DiagLabel(int(r.context), []string{"NONE", "DEADLINE", "CANCELLED", "OTHER"}) +
		" error=" + slog21DiagLabel(int(r.err), []string{"UNKNOWN", "NONE", "CONTEXT_DEADLINE", "CONTEXT_CANCELLED", "TX_DONE", "CONN_DONE", "SQLITE"}) +
		" sqlite=" + slog21DiagLabel(int(r.sqlite), []string{"NONE", "UNKNOWN", "1", "4", "5", "6", "9", "10", "19", "261", "517", "773", "262", "518", "4106"}) +
		" snapshot_elapsed=" + slog21DiagLabel(int(r.snapshotElapsed), []string{"INVALID", "LT1MS", "1MS_LT100MS", "100MS_LT1S", "1S_LT10S", "10S_LT60S", "GE60S"}) +
		" query_elapsed=" + slog21DiagLabel(int(r.queryElapsed), []string{"INVALID", "LT1MS", "1MS_LT100MS", "100MS_LT1S", "1S_LT10S", "10S_LT60S", "GE60S"}) +
		" cleanup=" + slog21DiagBool(r.cleanup) + " primary=" + slog21DiagBool(r.primary)
}
func (r slog21DiagRecord) emit(t *testing.T) { t.Helper(); t.Log(r.text()) }

type slog21DiagTrace struct {
	started       time.Time
	cut           slog21DiagCut
	firstObserved bool
}

func (d *slog21DiagTrace) capture(state slog21DiagCtx, query slog21DiagQuery, op slog21DiagOp, err error, queryStarted time.Time, cleanup bool) slog21DiagRecord {
	// state is a value sampled before any original cancellation/cleanup call.
	kind, code := slog21DiagError(err)
	r := slog21DiagRecord{cut: d.cut, query: query, op: op, context: state, err: kind, sqlite: code, snapshotElapsed: slog21DiagElapsed(time.Since(d.started)), queryElapsed: slog21DiagElapsed(time.Since(queryStarted)), cleanup: cleanup, primary: err != nil && !d.firstObserved}
	if err != nil {
		d.firstObserved = true
	}
	return r
}
func (d *slog21DiagTrace) internal(ctx context.Context, query slog21DiagQuery, op slog21DiagOp, queryStarted time.Time) slog21DiagRecord {
	r := d.capture(slog21DiagContext(ctx), query, op, nil, queryStarted, false)
	r.err = slog21ErrUnknown
	r.primary = !d.firstObserved
	d.firstObserved = true
	return r
}
func (d *slog21DiagTrace) closeRows(t *testing.T, ctx context.Context, r *sql.Rows, query slog21DiagQuery, queryStarted time.Time) {
	t.Helper()
	state := slog21DiagContext(ctx)
	err := r.Close()
	if err != nil {
		d.capture(state, query, slog21DiagRowsClose, err, queryStarted, true).emit(t)
	}
}
func (d *slog21DiagTrace) rollback(t *testing.T, ctx context.Context, tx *sql.Tx) {
	t.Helper()
	state := slog21DiagContext(ctx)
	err := tx.Rollback()
	if err != nil && !errors.Is(err, sql.ErrTxDone) {
		d.capture(state, slog21DiagNoQuery, slog21DiagRollback, err, d.started, true).emit(t)
	}
}
func (d *slog21DiagTrace) commit(t *testing.T, ctx context.Context, tx *sql.Tx) error {
	t.Helper()
	state := slog21DiagContext(ctx)
	err := tx.Commit()
	if err != nil {
		d.capture(state, slog21DiagNoQuery, slog21DiagCommit, err, d.started, false).emit(t)
	}
	return err
}

type slog21DiagSite struct {
	function string
	line     int
	cut      slog21DiagCut
}

func slog21DiagLocate() slog21DiagCut {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if !strings.HasSuffix(frame.Function, ".slog21Snapshot") {
			return slog21DiagCutAt(frame.Function, frame.Line)
		}
		if !more {
			break
		}
	}
	return slog21CutUnknown
}
func slog21DiagCutAt(function string, line int) slog21DiagCut {
	for _, site := range slog21DiagSites() {
		if line == site.line && (strings.HasSuffix(function, "."+site.function) || strings.Contains(function, "."+site.function+".func")) {
			return site.cut
		}
	}
	return slog21CutUnknown
}

// These pure controls never call a DB, fixture, old oracle or error formatter.
type slog21DiagPanicError struct{}

func (slog21DiagPanicError) Error() string { panic("synthetic diagnostic Error method must not run") }

type slog21DiagWrappedError struct{ next error }

func (slog21DiagWrappedError) Error() string   { panic("synthetic wrapper Error method must not run") }
func (e slog21DiagWrappedError) Unwrap() error { return e.next }

type slog21DiagContextValue struct {
	context.Context
	err error
}

func (c slog21DiagContextValue) Err() error { return c.err }

func TestSelfLogoutSnapshotErrorDiagnostic20261021(t *testing.T) {
	t.Run("error-classes", func(t *testing.T) {
		var typedNil *diagSQLite.Error
		for _, c := range []struct {
			err  error
			kind slog21DiagErr
			code slog21DiagCode
		}{
			{nil, slog21ErrNone, slog21CodeNone},
			{context.DeadlineExceeded, slog21ErrDeadline, slog21CodeNone},
			{context.Canceled, slog21ErrCancelled, slog21CodeNone},
			{slog21DiagWrappedError{context.DeadlineExceeded}, slog21ErrDeadline, slog21CodeNone},
			{sql.ErrTxDone, slog21ErrTxDone, slog21CodeNone},
			{sql.ErrConnDone, slog21ErrConnDone, slog21CodeNone},
			{slog21DiagPanicError{}, slog21ErrUnknown, slog21CodeNone},
			{slog21DiagWrappedError{slog21DiagPanicError{}}, slog21ErrUnknown, slog21CodeNone},
			{errors.New("SYNTHETIC_SNAPSHOT_DIAG_PUBLIC_CANARY"), slog21ErrUnknown, slog21CodeNone},
			{&diagSQLite.Error{}, slog21ErrSQLite, slog21CodeUnknown},
			{typedNil, slog21ErrUnknown, slog21CodeNone},
		} {
			kind, code := slog21DiagError(c.err)
			if kind != c.kind || code != c.code {
				t.Fatal("SLOG21_DIAG_CLASSIFIER_UNEXPECTED")
			}
		}
	})
	t.Run("format-canary", func(t *testing.T) {
		d := slog21DiagTrace{started: time.Now(), cut: slog21CutPostResponse}
		r := d.capture(slog21CtxNone, slog21DiagTypedRows, slog21DiagQueryContext, slog21DiagPanicError{}, d.started, false)
		line := r.text()
		fields := strings.Fields(line)
		if len(fields) != 11 || strings.Contains(line, "SYNTHETIC_SNAPSHOT_DIAG_PUBLIC_CANARY") || !strings.HasPrefix(line, "SLOG21_SNAPSHOT_DIAGNOSTIC cut=POST_RESPONSE query=TYPED_ROWS op=QUERY_CONTEXT context=NONE error=UNKNOWN sqlite=NONE ") {
			t.Fatal("SLOG21_DIAG_FORMAT_UNEXPECTED")
		}
		r = slog21DiagRecord{cut: 255, query: 255, op: 255, context: 255, err: 255, sqlite: 255, snapshotElapsed: 255, queryElapsed: 255}
		if r.text() != "SLOG21_SNAPSHOT_DIAGNOSTIC cut=UNKNOWN query=UNKNOWN op=UNKNOWN context=UNKNOWN error=UNKNOWN sqlite=UNKNOWN snapshot_elapsed=UNKNOWN query_elapsed=UNKNOWN cleanup=false primary=false" {
			t.Fatal("SLOG21_DIAG_FORMAT_UNKNOWN_UNEXPECTED")
		}
	})
	t.Run("bucket-boundaries", func(t *testing.T) {
		for _, c := range []struct {
			elapsed time.Duration
			want    slog21DiagBucket
		}{{-1, 0}, {0, 1}, {time.Millisecond, 2}, {100 * time.Millisecond, 3}, {time.Second, 4}, {10 * time.Second, 5}, {time.Minute, 6}} {
			if slog21DiagElapsed(c.elapsed) != c.want {
				t.Fatal("SLOG21_DIAG_BUCKET_UNEXPECTED")
			}
		}
	})
	t.Run("sqlite-code-whitelist", func(t *testing.T) {
		for i, code := range []int{1, 4, 5, 6, 9, 10, 19, 261, 517, 773, 262, 518, 4106} {
			if slog21DiagSQLiteCode(code) != slog21DiagCode(i+2) {
				t.Fatal("SLOG21_DIAG_CODE_UNEXPECTED")
			}
		}
		for _, code := range []int{-1, 0, 999999} {
			if slog21DiagSQLiteCode(code) != slog21CodeUnknown {
				t.Fatal("SLOG21_DIAG_UNKNOWN_CODE_UNEXPECTED")
			}
		}
	})
	t.Run("primary-cleanup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		d := slog21DiagTrace{started: time.Now(), cut: slog21CutPostResponse}
		state := slog21DiagContext(ctx)
		first := d.capture(state, slog21DiagTypedRows, slog21DiagRowsErr, slog21DiagPanicError{}, d.started, false)
		cancel()
		cleanup := d.capture(state, slog21DiagTypedRows, slog21DiagRowsClose, sql.ErrConnDone, d.started, true)
		if !first.primary || first.cleanup || first.context != slog21CtxNone || cleanup.primary || !cleanup.cleanup || cleanup.context != slog21CtxNone || slog21DiagContext(ctx) != slog21CtxCancelled {
			t.Fatal("SLOG21_DIAG_PRE_CLEANUP_CAPTURE_UNEXPECTED")
		}
		expired, expireCancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
		expiredState := slog21DiagContext(expired)
		expireCancel()
		if expiredState != slog21CtxDeadline || slog21DiagContext(slog21DiagContextValue{Context: t.Context(), err: slog21DiagPanicError{}}) != slog21CtxOther {
			t.Fatal("SLOG21_DIAG_CONTEXT_CLASS_UNEXPECTED")
		}
	})
	t.Run("cut-map", func(t *testing.T) {
		if len(slog21DiagSites()) != 22 {
			t.Fatal("SLOG21_DIAG_CUT_COUNT_UNEXPECTED")
		}
		for _, site := range slog21DiagSites() {
			if site.line <= 0 || site.cut == slog21CutUnknown || slog21DiagCutAt("cpacloud.local/server/internal/service."+site.function, site.line) != site.cut {
				t.Fatal("SLOG21_DIAG_CUT_MAP_UNEXPECTED")
			}
		}
		if slog21DiagCutAt("SYNTHETIC_SNAPSHOT_DIAG_PUBLIC_CANARY", 1) != slog21CutUnknown {
			t.Fatal("SLOG21_DIAG_CUT_UNKNOWN_UNEXPECTED")
		}
	})
}

// Filled from the final frozen source callsites, not runtime paths or SQL.
func slog21DiagSites() []slog21DiagSite {
	return []slog21DiagSite{
		{function: "(*slog21Fixture).closedRaw", line: 340, cut: slog21CutAfterClose},
		{function: "(*slog21Fixture).openRaw", line: 346, cut: slog21CutBeforeOpen},
		{function: "(*slog21Fixture).openRaw", line: 351, cut: slog21CutAfterOpen},
		{function: "(*slog21Fixture).reopen", line: 356, cut: slog21CutReopenBeforeClose},
		{function: "(*slog21Fixture).controlWindow", line: 499, cut: slog21CutControlsBefore},
		{function: "(*slog21Fixture).controlWindow", line: 501, cut: slog21CutControlsAfter},
		{function: "(*slog21Fixture).install", line: 528, cut: slog21CutTriggerBeforeClose},
		{function: "(*slog21Fixture).install", line: 530, cut: slog21CutDDLBefore},
		{function: "(*slog21Fixture).install", line: 552, cut: slog21CutDDLAfter},
		{function: "(*slog21Fixture).clean", line: 606, cut: slog21CutCleanupBefore},
		{function: "TestSelfLogout20261021JointOracle", line: 621, cut: slog21CutDeleteBefore},
		{function: "TestSelfLogout20261021JointOracle", line: 630, cut: slog21CutDeleteAfter},
		{function: "TestSelfLogout20261021JointOracle", line: 638, cut: slog21CutOracleTarget},
		{function: "(*slog21Fixture).expectedAfter", line: 666, cut: slog21CutPostResponse},
		{function: "(*slog21Fixture).expectedAfter", line: 696, cut: slog21CutPersistedTarget},
		{function: "TestSelfLogout20261021Normal", line: 705, cut: slog21CutNormalBefore},
		{function: "TestSelfLogout20261021NativeAbort", line: 721, cut: slog21CutAbortBefore},
		{function: "TestSelfLogout20261021Boundaries", line: 744, cut: slog21CutBoundaryBefore},
		{function: "TestSelfLogout20261021DeferredFK", line: 830, cut: slog21CutFKBefore},
		{function: "TestSelfLogout20261021AuthBoundaries", line: 863, cut: slog21CutAuthBefore},
		{function: "TestSelfLogout20261021AuthBoundaries", line: 911, cut: slog21CutAuthAfter},
		{function: "TestSelfLogout20261021TwoAuthenticated", line: 917, cut: slog21CutDualBefore},
	}
}
