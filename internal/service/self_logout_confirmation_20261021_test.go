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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"

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
		if diag.handoff != nil && query == slog21DiagObjects {
			diag.handoff.observe(t, slog21HandSchema, slog21HandError, err, -1, false)
		}
		slog21Fail(t, "SNAPSHOT_QUERY")
	}
	if diag.handoff != nil && query == slog21DiagObjects {
		diag.handoff.observe(t, slog21HandSchema, slog21HandOK, nil, -1, false)
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
func slog21Snapshot(t *testing.T, db *sql.DB, handoff ...*slog21HandoffTrace) slog21Snap {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	diag := &slog21DiagTrace{started: time.Now(), cut: slog21DiagLocate()}
	if len(handoff) > 1 || (len(handoff) == 1 && handoff[0] == nil) {
		slog21Fail(t, "HANDOFF_TRACE_MISSING")
	}
	if len(handoff) == 1 {
		diag.handoff = handoff[0]
		diag.handoff.observe(t, slog21HandBeginEnter, slog21HandEnter, nil, -1, false)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		diag.capture(slog21DiagContext(ctx), slog21DiagNoQuery, slog21DiagBegin, err, diag.started, false).emit(t)
		if diag.handoff != nil {
			diag.handoff.observe(t, slog21HandBeginReturn, slog21HandError, err, -1, false)
			diag.handoff.observe(t, slog21HandSchema, slog21HandNotAttempted, nil, -1, false)
		}
		slog21Fail(t, "SNAPSHOT_BEGIN")
	}
	defer diag.rollback(t, ctx, tx)
	if diag.handoff != nil {
		diag.handoff.observe(t, slog21HandBeginReturn, slog21HandOK, nil, -1, false)
	}
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
	handoffs                                                map[*sql.DB]*slog21HandoffTrace
	segments                                                [8]*slog21SegmentRecorder
	segmentCount                                            int
	segmentCurrent                                          *slog21SegmentRecorder
	segmentCensusInvalid                                    bool
	expiryRecorders                                         [8]*slog21ExpiryRecorder
	expiryCount                                             int
	expiryCensusInvalid                                     bool
}

func newSlog21(t *testing.T) *slog21Fixture {
	t.Helper()
	f := &slog21Fixture{t: t, dir: t.TempDir(), client: &http.Client{Timeout: 10 * time.Second}}
	t.Cleanup(func() {
		if f.app != nil && !f.closed {
			f.prepareSegments(slog21HandFinalClean, true)
		}
		if f.close() != nil {
			t.Error("SLOG21_UNEXPECTED_CLEANUP")
		}
		// The old close/observer/oracle path has finished before any new gate.
		f.flushSegments()
		f.flushExpirySQL()
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
	expiryRecorder := &slog21ExpiryRecorder{}
	startup := withExpiryOpenObserver(ctx, expiryRecorder.capture)
	a, err := Open(startup, Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "slog-owned"})
	if err != nil || a == nil {
		if a != nil && a.Close() != nil {
			f.t.Error("SLOG21_UNEXPECTED_PARTIAL_CLEANUP")
		}
		slog21Fail(f.t, "OPEN")
	}
	f.bindSegments(a)
	if f.expiryCount < len(f.expiryRecorders) {
		f.expiryRecorders[f.expiryCount] = expiryRecorder
		f.expiryCount++
	} else {
		f.expiryCensusInvalid = true
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
		if f.closeRaw(db, true) != nil {
			f.t.Error("SLOG21_UNEXPECTED_RAW_CLEANUP")
		}
	})
	return db
}
func (f *slog21Fixture) closedRaw(before slog21Snap) *sql.DB {
	f.t.Helper()
	handoff := &slog21HandoffTrace{stage: slog21HandoffStage(), old: f.app.store.db}
	f.prepareSegments(handoff.stage, false)
	handoff.observe(f.t, slog21HandCloseEnter, slog21HandEnter, nil, -1, false)
	started := time.Now()
	closeErr := f.close()
	closeElapsed := time.Since(started)
	handoff.observe(f.t, slog21HandCloseReturn, slog21HandoffResult(closeErr), closeErr, closeElapsed, false)
	if closeErr != nil {
		slog21Fail(f.t, "APP_CLOSE")
	}
	db := f.raw()
	handoff.raw, handoff.rawCreated = db, true
	if f.handoffs == nil {
		f.handoffs = make(map[*sql.DB]*slog21HandoffTrace)
	}
	f.handoffs[db] = handoff
	handoff.observe(f.t, slog21HandRawCreated, slog21HandOK, nil, -1, false)
	slog21Exact(f.t, before, slog21Snapshot(f.t, db, handoff))
	if !handoff.complete() {
		slog21Fail(f.t, "HANDOFF_TRACE_INCOMPLETE")
	}
	return db
}
func (f *slog21Fixture) openRaw(db *sql.DB) {
	f.t.Helper()
	start := time.Now().UTC()
	before := slog21Snapshot(f.t, db)
	if f.closeRaw(db, false) != nil {
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
		f.closeRaw(db, false)
		slog21Fail(f.t, "TRIGGER_KIND")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	_, err := db.ExecContext(ctx, statement)
	if err != nil {
		f.closeRaw(db, false)
		slog21Fail(f.t, "TRIGGER_INSTALL")
	}
	post := slog21Snapshot(f.t, db)
	if !reflect.DeepEqual(pre.Tables, post.Tables) || len(post.Schema) != len(pre.Schema)+1 {
		f.closeRaw(db, false)
		slog21Fail(f.t, "DDL_ROWS_OR_COUNT")
	}
	filtered := [][]slog21Cell{}
	found := 0
	for _, o := range post.Schema {
		if o[0].Text == "trigger" && o[1].Text == name {
			found++
			if o[2].Text != "employee_self_sessions" || o[3].Kind != "string" || strings.TrimSpace(o[3].Text) != statement {
				f.closeRaw(db, false)
				slog21Fail(f.t, "TRIGGER_MANIFEST")
			}
		} else {
			filtered = append(filtered, o)
		}
	}
	if found != 1 || !reflect.DeepEqual(filtered, pre.Schema) {
		f.closeRaw(db, false)
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
	if f.closeRaw(db, false) != nil {
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
	handoff       *slog21HandoffTrace
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
		{function: "(*slog21Fixture).closedRaw", line: 399, cut: slog21CutAfterClose},
		{function: "(*slog21Fixture).openRaw", line: 408, cut: slog21CutBeforeOpen},
		{function: "(*slog21Fixture).openRaw", line: 413, cut: slog21CutAfterOpen},
		{function: "(*slog21Fixture).reopen", line: 418, cut: slog21CutReopenBeforeClose},
		{function: "(*slog21Fixture).controlWindow", line: 561, cut: slog21CutControlsBefore},
		{function: "(*slog21Fixture).controlWindow", line: 563, cut: slog21CutControlsAfter},
		{function: "(*slog21Fixture).install", line: 590, cut: slog21CutTriggerBeforeClose},
		{function: "(*slog21Fixture).install", line: 592, cut: slog21CutDDLBefore},
		{function: "(*slog21Fixture).install", line: 614, cut: slog21CutDDLAfter},
		{function: "(*slog21Fixture).clean", line: 668, cut: slog21CutCleanupBefore},
		{function: "TestSelfLogout20261021JointOracle", line: 683, cut: slog21CutDeleteBefore},
		{function: "TestSelfLogout20261021JointOracle", line: 692, cut: slog21CutDeleteAfter},
		{function: "TestSelfLogout20261021JointOracle", line: 700, cut: slog21CutOracleTarget},
		{function: "(*slog21Fixture).expectedAfter", line: 728, cut: slog21CutPostResponse},
		{function: "(*slog21Fixture).expectedAfter", line: 758, cut: slog21CutPersistedTarget},
		{function: "TestSelfLogout20261021Normal", line: 767, cut: slog21CutNormalBefore},
		{function: "TestSelfLogout20261021NativeAbort", line: 783, cut: slog21CutAbortBefore},
		{function: "TestSelfLogout20261021Boundaries", line: 806, cut: slog21CutBoundaryBefore},
		{function: "TestSelfLogout20261021DeferredFK", line: 892, cut: slog21CutFKBefore},
		{function: "TestSelfLogout20261021AuthBoundaries", line: 925, cut: slog21CutAuthBefore},
		{function: "TestSelfLogout20261021AuthBoundaries", line: 973, cut: slog21CutAuthAfter},
		{function: "TestSelfLogout20261021TwoAuthenticated", line: 979, cut: slog21CutDualBefore},
	}
}

// Independently authored Close/handoff observation. All pointers stay local to
// the existing fixture; Stats does not open a connection or identify a lock owner.
type slog21HandStage uint8
type slog21HandEvent uint8
type slog21HandResult uint8
type slog21HandCount uint8

const (
	slog21HandInstall slog21HandStage = iota + 1
	slog21HandReopen
	slog21HandFinalClean
)
const (
	slog21HandCloseEnter slog21HandEvent = iota + 1
	slog21HandCloseReturn
	slog21HandRawCreated
	slog21HandBeginEnter
	slog21HandBeginReturn
	slog21HandSchema
	slog21HandRawClose
)
const (
	slog21HandEnter slog21HandResult = iota + 1
	slog21HandOK
	slog21HandError
	slog21HandNotAttempted
)
const (
	slog21HandAbsent slog21HandCount = iota
	slog21HandZero
	slog21HandNonzero
	slog21HandUnknown
)

func slog21HandoffStageAt(function string) slog21HandStage {
	switch function {
	case "cpacloud.local/server/internal/service.(*slog21Fixture).install":
		return slog21HandInstall
	case "cpacloud.local/server/internal/service.(*slog21Fixture).reopen":
		return slog21HandReopen
	case "cpacloud.local/server/internal/service.(*slog21Fixture).clean":
		return slog21HandFinalClean
	default:
		return 0
	}
}
func slog21HandoffStage() slog21HandStage {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if stage := slog21HandoffStageAt(frame.Function); stage != 0 {
			return stage
		}
		if !more {
			return 0
		}
	}
}
func slog21HandoffCount(n int) slog21HandCount {
	if n < 0 {
		return slog21HandUnknown
	}
	if n == 0 {
		return slog21HandZero
	}
	return slog21HandNonzero
}
func slog21HandoffPool(s sql.DBStats) [3]slog21HandCount {
	return [3]slog21HandCount{slog21HandoffCount(s.OpenConnections), slog21HandoffCount(s.InUse), slog21HandoffCount(s.Idle)}
}
func slog21HandoffStats(db *sql.DB) [3]slog21HandCount {
	if db == nil {
		return [3]slog21HandCount{}
	}
	return slog21HandoffPool(db.Stats())
}
func slog21HandoffResult(err error) slog21HandResult {
	if err != nil {
		return slog21HandError
	}
	return slog21HandOK
}

type slog21HandoffRecord struct {
	stage                                  slog21HandStage
	event                                  slog21HandEvent
	result                                 slog21HandResult
	err                                    slog21DiagErr
	sqlite                                 slog21DiagCode
	elapsed                                slog21DiagBucket
	timed                                  bool
	oldPool, rawPool                       [3]slog21HandCount
	rawCreated, rawCloseCalled, rawCloseOK bool
	cleanup                                bool
}

func (r slog21HandoffRecord) valid() bool {
	if r.stage < slog21HandInstall || r.stage > slog21HandFinalClean || r.event < slog21HandCloseEnter || r.event > slog21HandRawClose || r.result < slog21HandEnter || r.result > slog21HandNotAttempted || r.err < slog21ErrNone || r.err > slog21ErrSQLite || r.sqlite == slog21CodeUnknown || r.sqlite > slog21DiagSQLiteCode(4106) {
		return false
	}
	if r.timed && (r.elapsed < 1 || r.elapsed > 6) {
		return false
	}
	for i := range r.oldPool {
		if r.oldPool[i] < slog21HandZero || r.oldPool[i] > slog21HandNonzero {
			return false
		}
		if r.rawCreated {
			if r.rawPool[i] < slog21HandZero || r.rawPool[i] > slog21HandNonzero {
				return false
			}
		} else if r.rawPool[i] != slog21HandAbsent {
			return false
		}
	}
	if (!r.rawCreated && r.rawCloseCalled) || (r.rawCloseOK && !r.rawCloseCalled) || (r.result == slog21HandError) != (r.err != slog21ErrNone) {
		return false
	}
	return (r.err == slog21ErrSQLite) == (r.sqlite != slog21CodeNone)
}
func (r slog21HandoffRecord) text() string {
	count := func(n slog21HandCount) string {
		return slog21DiagLabel(int(n), []string{"ABSENT", "ZERO", "NONZERO", "UNKNOWN"})
	}
	elapsed := "NA"
	if r.timed {
		elapsed = slog21DiagLabel(int(r.elapsed), []string{"INVALID", "LT1MS", "1MS_LT100MS", "100MS_LT1S", "1S_LT10S", "10S_LT60S", "GE60S"})
	}
	return "SLOG21_CLOSE_HANDOFF_DIAGNOSTIC" +
		" stage=" + slog21DiagLabel(int(r.stage), []string{"UNKNOWN", "INSTALL", "REOPEN", "FINAL_CLEAN"}) +
		" event=" + slog21DiagLabel(int(r.event), []string{"UNKNOWN", "CLOSE_ENTER", "CLOSE_RETURN", "RAW_CREATED", "BEGIN_ENTER", "BEGIN_RETURN", "SCHEMA_RETURN", "RAW_CLOSE"}) +
		" result=" + slog21DiagLabel(int(r.result), []string{"UNKNOWN", "ENTER", "OK", "ERROR", "NOT_ATTEMPTED"}) +
		" error=" + slog21DiagLabel(int(r.err), []string{"UNKNOWN", "NONE", "CONTEXT_DEADLINE", "CONTEXT_CANCELLED", "TX_DONE", "CONN_DONE", "SQLITE"}) +
		" sqlite=" + slog21DiagLabel(int(r.sqlite), []string{"NONE", "UNKNOWN", "1", "4", "5", "6", "9", "10", "19", "261", "517", "773", "262", "518", "4106"}) +
		" elapsed=" + elapsed +
		" old_open=" + count(r.oldPool[0]) + " old_in_use=" + count(r.oldPool[1]) + " old_idle=" + count(r.oldPool[2]) +
		" raw_open=" + count(r.rawPool[0]) + " raw_in_use=" + count(r.rawPool[1]) + " raw_idle=" + count(r.rawPool[2]) +
		" raw_created=" + slog21DiagBool(r.rawCreated) + " raw_close_called=" + slog21DiagBool(r.rawCloseCalled) + " raw_close_ok=" + slog21DiagBool(r.rawCloseOK) +
		" cleanup=" + slog21DiagBool(r.cleanup)
}

type slog21HandoffTrace struct {
	stage                                  slog21HandStage
	old, raw                               *sql.DB
	rawCreated, rawCloseCalled, rawCloseOK bool
	last                                   slog21HandEvent
	invalid                                bool
}

// accept is a pure sequence gate. Repeated RAW_CLOSE only observes the original
// fixture's existing idempotent cleanup call; it never causes another Close.
func (h *slog21HandoffTrace) accept(event slog21HandEvent) bool {
	if event < slog21HandCloseEnter || event > slog21HandRawClose || (event != h.last+1 && !(event == slog21HandRawClose && h.last == slog21HandRawClose)) {
		h.invalid = true
		return false
	}
	h.last = event
	return true
}
func (h *slog21HandoffTrace) complete() bool {
	return !h.invalid && h.last >= slog21HandSchema
}
func (h *slog21HandoffTrace) observe(t *testing.T, event slog21HandEvent, result slog21HandResult, err error, elapsed time.Duration, cleanup bool) {
	t.Helper()
	kind, code := slog21DiagError(err)
	r := slog21HandoffRecord{stage: h.stage, event: event, result: result, err: kind, sqlite: code, oldPool: slog21HandoffStats(h.old), rawPool: slog21HandoffStats(h.raw), rawCreated: h.rawCreated, rawCloseCalled: h.rawCloseCalled, rawCloseOK: h.rawCloseOK, cleanup: cleanup, timed: event == slog21HandCloseReturn}
	if r.timed {
		r.elapsed = slog21DiagElapsed(elapsed)
	}
	valid := r.valid()
	ordered := h.accept(event)
	t.Log(r.text())
	if !valid || !ordered {
		h.invalid = true
		t.Error("SLOG21_UNEXPECTED_HANDOFF_RECORD")
	}
}
func (f *slog21Fixture) closeRaw(db *sql.DB, cleanup bool) error {
	// The original Close call stays first and occurs exactly once per original
	// callsite. Sampling never opens, probes, retries or forces another handle.
	err := db.Close()
	handoff := f.handoffs[db]
	if handoff == nil {
		f.t.Error("SLOG21_UNEXPECTED_HANDOFF_TRACE_MISSING")
		return err
	}
	handoff.rawCloseCalled, handoff.rawCloseOK = true, err == nil
	handoff.observe(f.t, slog21HandRawClose, slog21HandoffResult(err), err, -1, cleanup)
	if !handoff.complete() {
		f.t.Error("SLOG21_UNEXPECTED_HANDOFF_TRACE_INCOMPLETE")
	}
	return err
}

func TestSelfLogout20261021CloseHandoffDiagnostic(t *testing.T) {
	t.Run("pool-buckets", func(t *testing.T) {
		for _, c := range []struct {
			n    int
			want slog21HandCount
		}{{-1, slog21HandUnknown}, {0, slog21HandZero}, {1, slog21HandNonzero}, {1000000, slog21HandNonzero}} {
			if slog21HandoffCount(c.n) != c.want {
				t.Fatal("SLOG21_HANDOFF_BUCKET_CONTROL")
			}
		}
		if slog21HandoffStats(nil) != [3]slog21HandCount{} || slog21HandoffPool(sql.DBStats{OpenConnections: 2, InUse: 1, Idle: 1}) != [3]slog21HandCount{slog21HandNonzero, slog21HandNonzero, slog21HandNonzero} {
			t.Fatal("SLOG21_HANDOFF_STATS_CONTROL")
		}
	})
	t.Run("stage-map", func(t *testing.T) {
		for i, fn := range []string{"install", "reopen", "clean"} {
			if slog21HandoffStageAt("cpacloud.local/server/internal/service.(*slog21Fixture)."+fn) != slog21HandStage(i+1) {
				t.Fatal("SLOG21_HANDOFF_STAGE_CONTROL")
			}
		}
		if slog21HandoffStageAt("SYNTHETIC_HANDOFF_PUBLIC_CANARY") != 0 {
			t.Fatal("SLOG21_HANDOFF_UNKNOWN_STAGE_CONTROL")
		}
	})
	t.Run("format-canary", func(t *testing.T) {
		r := slog21HandoffRecord{stage: slog21HandInstall, event: slog21HandCloseEnter, result: slog21HandEnter, err: slog21ErrNone, oldPool: [3]slog21HandCount{slog21HandZero, slog21HandZero, slog21HandZero}}
		if !r.valid() || len(strings.Fields(r.text())) != 17 || !strings.HasPrefix(r.text(), "SLOG21_CLOSE_HANDOFF_DIAGNOSTIC stage=INSTALL event=CLOSE_ENTER result=ENTER error=NONE sqlite=NONE elapsed=NA ") {
			t.Fatal("SLOG21_HANDOFF_FORMAT_CONTROL")
		}
		for _, err := range []error{slog21DiagPanicError{}, slog21DiagWrappedError{slog21DiagPanicError{}}, errors.New("SYNTHETIC_HANDOFF_PUBLIC_CANARY")} {
			r.err, r.sqlite = slog21DiagError(err)
			r.result = slog21HandError
			if r.valid() || strings.Contains(r.text(), "SYNTHETIC_HANDOFF_PUBLIC_CANARY") || !strings.Contains(r.text(), "error=UNKNOWN") {
				t.Fatal("SLOG21_HANDOFF_CANARY_CONTROL")
			}
		}
	})
	t.Run("sequence", func(t *testing.T) {
		for _, stage := range []slog21HandStage{slog21HandInstall, slog21HandReopen, slog21HandFinalClean} {
			h := slog21HandoffTrace{stage: stage}
			for event := slog21HandCloseEnter; event <= slog21HandRawClose; event++ {
				if !h.accept(event) || h.complete() != (event >= slog21HandSchema) {
					t.Fatal("SLOG21_HANDOFF_SEQUENCE_CONTROL")
				}
			}
			if !h.accept(slog21HandRawClose) || !h.complete() {
				t.Fatal("SLOG21_HANDOFF_IDEMPOTENT_CLOSE_CONTROL")
			}
		}
	})
	t.Run("missing-and-unknown", func(t *testing.T) {
		for _, event := range []slog21HandEvent{0, slog21HandCloseReturn, 255} {
			h := slog21HandoffTrace{}
			if h.accept(event) || h.complete() || !h.invalid {
				t.Fatal("SLOG21_HANDOFF_MISSING_CONTROL")
			}
		}
		r := slog21HandoffRecord{stage: slog21HandInstall, event: slog21HandCloseReturn, result: slog21HandOK, err: slog21ErrNone, oldPool: [3]slog21HandCount{slog21HandZero, slog21HandZero, slog21HandZero}, timed: true, elapsed: 1}
		if !r.valid() {
			t.Fatal("SLOG21_HANDOFF_VALID_CONTROL")
		}
		for _, mutate := range []func(*slog21HandoffRecord){
			func(v *slog21HandoffRecord) { v.stage = 0 },
			func(v *slog21HandoffRecord) { v.event = 255 },
			func(v *slog21HandoffRecord) { v.err = slog21ErrUnknown },
			func(v *slog21HandoffRecord) { v.sqlite = slog21CodeUnknown },
			func(v *slog21HandoffRecord) { v.elapsed = 0 },
			func(v *slog21HandoffRecord) { v.oldPool[0] = slog21HandAbsent },
			func(v *slog21HandoffRecord) { v.rawPool[0] = slog21HandUnknown },
			func(v *slog21HandoffRecord) { v.rawCloseOK = true },
		} {
			v := r
			mutate(&v)
			if v.valid() {
				t.Fatal("SLOG21_HANDOFF_UNKNOWN_CONTROL")
			}
		}
	})
}

// Independently authored for the passive App.Close-segments diagnostic contract.
// This expectation is fixed from the frozen fixture Config/construction, never
// from observed events or current App component pointers.
type slog21SegmentSpec struct {
	phase appClosePhase
	edge  appCloseEdge
}

func slog21SegmentExpected() [27]slog21SegmentSpec {
	return [27]slog21SegmentSpec{
		{appCloseOneShot, appCloseEnter}, {appCloseOneShot, appCloseReturn},
		{appCloseExpiry, appCloseEnter}, {appCloseExpiry, appCloseReturn},
		{appCloseBackupAutomation, appCloseEnter}, {appCloseBackupAutomation, appCloseReturn},
		{appCloseBackgroundResponses, appCloseSkipNil},
		{appCloseScheduledTests, appCloseEnter}, {appCloseScheduledTests, appCloseReturn},
		{appCloseChannelMonitors, appCloseEnter}, {appCloseChannelMonitors, appCloseReturn},
		{appCloseGovernance, appCloseEnter}, {appCloseGovernance, appCloseReturn},
		{appCloseProxyTests, appCloseEnter}, {appCloseProxyTests, appCloseReturn},
		{appCloseRecovery, appCloseEnter}, {appCloseRecovery, appCloseReturn},
		{appCloseHealthTests, appCloseEnter}, {appCloseHealthTests, appCloseReturn},
		{appCloseAccountPool, appCloseEnter}, {appCloseAccountPool, appCloseReturn},
		{appCloseRefresh, appCloseEnter}, {appCloseRefresh, appCloseReturn},
		{appCloseProxyClients, appCloseEnter}, {appCloseProxyClients, appCloseReturn},
		{appCloseStore, appCloseEnter}, {appCloseStore, appCloseReturn},
	}
}

type slog21SegmentRecord struct {
	stage   slog21HandStage
	phase   appClosePhase
	edge    appCloseEdge
	pool    [3]slog21HandCount
	elapsed slog21DiagBucket
	cleanup bool
}

func (r slog21SegmentRecord) valid() bool {
	if r.stage < slog21HandInstall || r.stage > slog21HandFinalClean || r.phase < appCloseOneShot || r.phase > appCloseStore || r.edge < appCloseEnter || r.edge > appCloseSkipNil {
		return false
	}
	if r.edge == appCloseReturn {
		if r.elapsed < 1 || r.elapsed > 6 {
			return false
		}
	} else if r.elapsed != 0 {
		return false
	}
	for _, n := range r.pool {
		if n < slog21HandZero || n > slog21HandNonzero {
			return false
		}
	}
	return true
}

func (r slog21SegmentRecord) text() string {
	count := func(n slog21HandCount) string {
		return slog21DiagLabel(int(n), []string{"ABSENT", "ZERO", "NONZERO", "UNKNOWN"})
	}
	elapsed := "NA"
	if r.edge == appCloseReturn {
		elapsed = slog21DiagLabel(int(r.elapsed), []string{"INVALID", "LT1MS", "1MS_LT100MS", "100MS_LT1S", "1S_LT10S", "10S_LT60S", "GE60S"})
	}
	return "SLOG21_APP_CLOSE_SEGMENT" +
		" stage=" + slog21DiagLabel(int(r.stage), []string{"UNKNOWN", "INSTALL", "REOPEN", "FINAL_CLEAN"}) +
		" phase=" + slog21DiagLabel(int(r.phase), []string{"UNKNOWN", "oneShot", "expiry", "backupAutomation", "backgroundResponses", "scheduledTests", "channelMonitors", "governance", "proxyTests", "recovery", "healthTests", "accountPool", "refresh", "proxyClients", "store"}) +
		" edge=" + slog21DiagLabel(int(r.edge), []string{"UNKNOWN", "ENTER", "RETURN", "SKIP_NIL"}) +
		" old_open=" + count(r.pool[0]) + " old_in_use=" + count(r.pool[1]) + " old_idle=" + count(r.pool[2]) +
		" elapsed=" + elapsed + " cleanup=" + slog21DiagBool(r.cleanup)
}

type slog21SegmentRecorder struct {
	db            *sql.DB
	busy, invalid atomic.Bool
	overflow      bool
	stage         slog21HandStage
	cleanup       bool
	flushed       bool
	starts        [15]time.Time
	records       [32]slog21SegmentRecord
	n             int
}

// No logging, testing.T, error interface, SQL/Conn/Ping or product mutation is
// reachable from this callback. A busy guard only latches and returns; no wait.
func (r *slog21SegmentRecorder) capture(phase appClosePhase, edge appCloseEdge) {
	if !r.busy.CompareAndSwap(false, true) {
		r.invalid.Store(true)
		return
	}
	now := time.Now()
	v := slog21SegmentRecord{stage: r.stage, phase: phase, edge: edge, cleanup: r.cleanup}
	if r.db == nil || r.flushed {
		r.invalid.Store(true)
	}
	v.pool = slog21HandoffStats(r.db)
	if phase >= appCloseOneShot && phase <= appCloseStore {
		if edge == appCloseEnter {
			if !r.starts[phase].IsZero() {
				r.invalid.Store(true)
			}
			r.starts[phase] = now
		} else if edge == appCloseReturn && !r.starts[phase].IsZero() {
			v.elapsed = slog21DiagElapsed(now.Sub(r.starts[phase]))
		}
	}
	r.appendRecord(v)
	r.busy.Store(false)
}

// Caller owns the nonblocking guard (or a pure, unpublished control instance).
func (r *slog21SegmentRecorder) appendRecord(v slog21SegmentRecord) {
	if r.n >= len(r.records) {
		r.overflow = true
		r.invalid.Store(true)
		return
	}
	expected := slog21SegmentExpected()
	if r.n >= len(expected) || expected[r.n] != (slog21SegmentSpec{v.phase, v.edge}) || !v.valid() || v.stage != r.stage || v.cleanup != r.cleanup {
		r.invalid.Store(true)
	}
	r.records[r.n] = v
	r.n++
}

func (r *slog21SegmentRecorder) prepare(stage slog21HandStage, cleanup bool) {
	if !r.busy.CompareAndSwap(false, true) {
		r.invalid.Store(true)
		return
	}
	if r.stage != 0 || r.n != 0 || stage < slog21HandInstall || stage > slog21HandFinalClean || r.flushed {
		r.invalid.Store(true)
	} else {
		r.stage, r.cleanup = stage, cleanup
	}
	r.busy.Store(false)
}

func (r *slog21SegmentRecorder) complete() bool {
	if r.invalid.Load() || r.overflow || r.n != len(slog21SegmentExpected()) {
		return false
	}
	for i, spec := range slog21SegmentExpected() {
		v := r.records[i]
		if !v.valid() || (slog21SegmentSpec{v.phase, v.edge}) != spec || v.stage != r.stage || v.cleanup != r.cleanup {
			return false
		}
	}
	return true
}

func (f *slog21Fixture) bindSegments(a *App) {
	r := &slog21SegmentRecorder{db: a.store.db}
	a.closeObserver = r.capture
	f.segmentCurrent = r
	if f.segmentCount >= len(f.segments) {
		// Still bind an independent recorder; never skip the real close/oracle.
		f.segmentCensusInvalid = true
		return
	}
	f.segments[f.segmentCount] = r
	f.segmentCount++
}

func (f *slog21Fixture) prepareSegments(stage slog21HandStage, cleanup bool) {
	if f.segmentCurrent == nil {
		f.segmentCensusInvalid = true
		return
	}
	f.segmentCurrent.prepare(stage, cleanup)
}

// Invoked only at the end of the original fixture cleanup, after its original
// close and the test's original observer/oracle path. It cannot replace the
// first original failure or interrupt a later original test-body observation.
func (f *slog21Fixture) flushSegments() {
	if f.segmentCensusInvalid || f.segmentCount == 0 {
		f.t.Error("SLOG21_SEGMENT_CENSUS_INVALID")
	}
	for i := 0; i < f.segmentCount; i++ {
		r := f.segments[i]
		if r == nil || !r.busy.CompareAndSwap(false, true) {
			if r != nil {
				r.invalid.Store(true)
			}
			f.t.Error("SLOG21_SEGMENT_FLUSH_REENTRY")
			continue
		}
		valid := !r.flushed && r.complete()
		records, n := r.records, r.n
		r.flushed = true
		r.busy.Store(false)
		for j := 0; j < n; j++ {
			f.t.Log(records[j].text())
		}
		if !valid {
			f.t.Error("SLOG21_SEGMENT_TRACE_INVALID")
		}
	}
}

// Pure controls: no new App.Open, shutdown, DB, connection, worker or goroutine.
func TestSelfLogout20261021AppCloseSegmentsDiagnostic(t *testing.T) {
	value := func(s slog21SegmentSpec) slog21SegmentRecord {
		v := slog21SegmentRecord{stage: slog21HandReopen, phase: s.phase, edge: s.edge, pool: [3]slog21HandCount{slog21HandZero, slog21HandZero, slog21HandZero}}
		if s.edge == appCloseReturn {
			v.elapsed = 1
		}
		return v
	}
	fill := func(r *slog21SegmentRecorder) {
		for _, spec := range slog21SegmentExpected() {
			r.appendRecord(value(spec))
		}
	}
	t.Run("fixed-presence", func(t *testing.T) {
		presence := [14]bool{true, true, true, false, true, true, true, true, true, true, true, true, true, true}
		expected, index := slog21SegmentExpected(), 0
		for i, present := range presence {
			phase := appClosePhase(i + 1)
			if !present {
				if expected[index] != (slog21SegmentSpec{phase, appCloseSkipNil}) {
					t.Fatal("SLOG21_SEGMENT_PINNED_NIL_CONTROL")
				}
				index++
				continue
			}
			if expected[index] != (slog21SegmentSpec{phase, appCloseEnter}) || expected[index+1] != (slog21SegmentSpec{phase, appCloseReturn}) {
				t.Fatal("SLOG21_SEGMENT_PINNED_PAIR_CONTROL")
			}
			index += 2
		}
		r := &slog21SegmentRecorder{stage: slog21HandReopen}
		fill(r)
		if index != 27 || !r.complete() {
			t.Fatal("SLOG21_SEGMENT_PINNED_TOTAL_CONTROL")
		}
	})
	t.Run("fixed-capacity-overflow", func(t *testing.T) {
		r := &slog21SegmentRecorder{stage: slog21HandReopen}
		for i := 0; i < 33; i++ {
			r.appendRecord(value(slog21SegmentSpec{appCloseOneShot, appCloseEnter}))
		}
		if r.n != 32 || !r.overflow || !r.invalid.Load() || r.complete() {
			t.Fatal("SLOG21_SEGMENT_OVERFLOW_CONTROL")
		}
	})
	t.Run("unknown-duplicate-order", func(t *testing.T) {
		for _, bad := range []slog21SegmentSpec{{0, appCloseEnter}, {255, 255}, {appCloseExpiry, appCloseReturn}, {appCloseOneShot, appCloseSkipNil}, {appCloseOneShot, appCloseEnter}} {
			r := &slog21SegmentRecorder{stage: slog21HandReopen}
			r.appendRecord(value(slog21SegmentSpec{appCloseOneShot, appCloseEnter}))
			r.appendRecord(value(bad))
			if !r.invalid.Load() || r.complete() {
				t.Fatal("SLOG21_SEGMENT_ORDER_CONTROL")
			}
		}
	})
	t.Run("missing-zero", func(t *testing.T) {
		r := &slog21SegmentRecorder{stage: slog21HandReopen}
		if r.complete() {
			t.Fatal("SLOG21_SEGMENT_ZERO_CONTROL")
		}
		fill(r)
		r.n--
		if r.complete() {
			t.Fatal("SLOG21_SEGMENT_MISSING_CONTROL")
		}
		for _, mutate := range []func(*slog21SegmentRecord){
			func(v *slog21SegmentRecord) { v.stage = 0 },
			func(v *slog21SegmentRecord) { v.pool[0] = slog21HandAbsent },
			func(v *slog21SegmentRecord) { v.elapsed = 255 },
		} {
			v := value(slog21SegmentSpec{appCloseStore, appCloseReturn})
			mutate(&v)
			if v.valid() {
				t.Fatal("SLOG21_SEGMENT_INVALID_VALUE_CONTROL")
			}
		}
	})
	t.Run("reentry-latch", func(t *testing.T) {
		r := &slog21SegmentRecorder{}
		r.busy.Store(true)
		r.capture(appCloseOneShot, appCloseEnter)
		if !r.invalid.Load() || r.n != 0 {
			t.Fatal("SLOG21_SEGMENT_REENTRY_CONTROL")
		}
		r.busy.Store(false)
		r.prepare(slog21HandReopen, false)
		fill(r)
		if r.complete() {
			t.Fatal("SLOG21_SEGMENT_LATCH_RESET_CONTROL")
		}
	})
	t.Run("per-app-independent", func(t *testing.T) {
		var a, b App
		if a.closeObserver != nil || b.closeObserver != nil {
			t.Fatal("SLOG21_SEGMENT_DEFAULT_NIL_CONTROL")
		}
		r, s := &slog21SegmentRecorder{stage: slog21HandReopen}, &slog21SegmentRecorder{stage: slog21HandReopen}
		a.closeObserver, b.closeObserver = r.capture, s.capture
		fill(r)
		if !r.complete() || s.n != 0 || s.invalid.Load() || s.complete() {
			t.Fatal("SLOG21_SEGMENT_INDEPENDENCE_CONTROL")
		}
		fill(s)
		r.records[0].phase = 255
		if r.complete() || !s.complete() {
			t.Fatal("SLOG21_SEGMENT_BUFFER_ALIAS_CONTROL")
		}
	})
	t.Run("safe-delayed-format", func(t *testing.T) {
		r := &slog21SegmentRecorder{stage: slog21HandReopen}
		fill(r)
		if r.flushed || !r.complete() {
			t.Fatal("SLOG21_SEGMENT_PASSIVE_RECORDING_CONTROL")
		}
		want := "SLOG21_APP_CLOSE_SEGMENT stage=REOPEN phase=oneShot edge=ENTER old_open=ZERO old_in_use=ZERO old_idle=ZERO elapsed=NA cleanup=false"
		if r.records[0].text() != want {
			t.Fatal("SLOG21_SEGMENT_FINITE_FORMAT_CONTROL")
		}
		unknown := slog21SegmentRecord{stage: 255, phase: 255, edge: 255, pool: [3]slog21HandCount{255, 255, 255}}
		if unknown.text() != "SLOG21_APP_CLOSE_SEGMENT stage=UNKNOWN phase=UNKNOWN edge=UNKNOWN old_open=UNKNOWN old_in_use=UNKNOWN old_idle=UNKNOWN elapsed=NA cleanup=false" {
			t.Fatal("SLOG21_SEGMENT_UNKNOWN_FORMAT_CONTROL")
		}
		for _, spec := range slog21SegmentExpected() {
			if len(strings.Fields(value(spec).text())) != 9 {
				t.Fatal("SLOG21_SEGMENT_FORMAT_SHAPE_CONTROL")
			}
		}
	})
}

// Independently authored expiry-only recorder. The two real producers have
// independent fixed buffers; legitimate Close/worker overlap is not reentry.
const slog21ExpiryWorkerCapacity = 4096
const slog21ExpiryControlCapacity = 32

type slog21ExpiryRecorder struct {
	worker                    [slog21ExpiryWorkerCapacity]financial.ExpiryObservation
	control                   [slog21ExpiryControlCapacity]financial.ExpiryObservation
	workerN, controlN         int
	workerBusy, controlBusy   atomic.Bool
	invalid, bound, doneReady atomic.Bool
	flushed                   bool
}

func (r *slog21ExpiryRecorder) capture(o financial.ExpiryObservation) {
	control := o.Phase == financial.ExpiryBind || o.Phase == financial.ExpiryCancel || o.Phase == financial.ExpiryWait
	guard := &r.workerBusy
	if control {
		guard = &r.controlBusy
	}
	if !guard.CompareAndSwap(false, true) {
		r.invalid.Store(true)
		return
	}
	if !o.Valid() || r.flushed {
		r.invalid.Store(true)
	}
	if o.Phase == financial.ExpiryStart && !r.bound.Load() || o.Phase == financial.ExpiryWait && o.Edge == financial.ExpiryReturn && !r.doneReady.Load() {
		r.invalid.Store(true)
	}
	if control {
		if r.controlN < len(r.control) {
			r.control[r.controlN] = o
			r.controlN++
		} else {
			r.invalid.Store(true)
		}
	} else if r.workerN < len(r.worker) {
		r.worker[r.workerN] = o
		r.workerN++
	} else {
		r.invalid.Store(true)
	}
	if o.Phase == financial.ExpiryBind {
		r.bound.Store(true)
	}
	if o.Phase == financial.ExpiryDoneReady {
		r.doneReady.Store(true)
	}
	guard.Store(false)
}

type slog21ExpiryCursor struct {
	records []financial.ExpiryObservation
	n       int
}

func (c *slog21ExpiryCursor) peek() financial.ExpiryPhase {
	if c.n >= len(c.records) {
		return 0
	}
	return c.records[c.n].Phase
}

func (c *slog21ExpiryCursor) point(phase financial.ExpiryPhase, pass uint16, item uint8) bool {
	if c.n >= len(c.records) {
		return false
	}
	o := c.records[c.n]
	if o.Phase != phase || o.Edge != financial.ExpiryPoint || o.Pass != pass || o.Item != item {
		return false
	}
	c.n++
	return true
}

func (c *slog21ExpiryCursor) pair(phase financial.ExpiryPhase, pass uint16, item uint8) (financial.ExpiryObservation, bool) {
	if c.n+1 >= len(c.records) {
		return financial.ExpiryObservation{}, false
	}
	a, b := c.records[c.n], c.records[c.n+1]
	if a.Phase != phase || b.Phase != phase || a.Edge != financial.ExpiryEnter || b.Edge != financial.ExpiryReturn || a.Pass != pass || b.Pass != pass || a.Item != item || b.Item != item {
		return financial.ExpiryObservation{}, false
	}
	c.n += 2
	return b, true
}

func (c *slog21ExpiryCursor) failed(pass uint16) (bool, financial.ExpiryCount, bool) {
	_, ok := c.pair(financial.ExpiryRollback, pass, 0)
	return true, financial.ExpiryCountZero, ok
}

// sql validates only the original function's branch grammar. A nonnil cleanup
// error remains observed, not converted into a new product error or repair.
func (c *slog21ExpiryCursor) sql(pass uint16) (bool, financial.ExpiryCount, bool) {
	begin, ok := c.pair(financial.ExpiryBegin, pass, 0)
	if !ok {
		return false, 0, false
	}
	if begin.Error != financial.ExpiryErrorNone {
		return true, financial.ExpiryCountZero, true
	}
	query, ok := c.pair(financial.ExpiryQuery, pass, 0)
	if !ok {
		return false, 0, false
	}
	if query.Error != financial.ExpiryErrorNone {
		return c.failed(pass)
	}
	var scanned uint8
	for c.peek() == financial.ExpiryScan {
		if scanned == 100 {
			return false, 0, false
		}
		scanned++
		scan, ok := c.pair(financial.ExpiryScan, pass, scanned)
		if !ok {
			return false, 0, false
		}
		if scan.Error != financial.ExpiryErrorNone {
			if _, ok := c.pair(financial.ExpiryRowsClose, pass, 0); !ok {
				return false, 0, false
			}
			return c.failed(pass)
		}
		if c.peek() == financial.ExpiryValidate {
			if !c.point(financial.ExpiryValidate, pass, scanned) {
				return false, 0, false
			}
			if _, ok := c.pair(financial.ExpiryRowsClose, pass, 0); !ok {
				return false, 0, false
			}
			return c.failed(pass)
		}
	}
	iteration, ok := c.pair(financial.ExpiryRowsErr, pass, 0)
	if !ok {
		return false, 0, false
	}
	closed, ok := c.pair(financial.ExpiryRowsClose, pass, 0)
	if !ok {
		return false, 0, false
	}
	if iteration.Error != financial.ExpiryErrorNone || closed.Error != financial.ExpiryErrorNone {
		return c.failed(pass)
	}
	for item := uint8(1); item <= scanned; item++ {
		executed, ok := c.pair(financial.ExpiryExec, pass, item)
		if !ok {
			return false, 0, false
		}
		if executed.Error != financial.ExpiryErrorNone {
			return c.failed(pass)
		}
		metadata, ok := c.pair(financial.ExpiryRowsAffected, pass, item)
		if !ok {
			return false, 0, false
		}
		if metadata.Error != financial.ExpiryErrorNone || metadata.Count != financial.ExpiryCountOne {
			return c.failed(pass)
		}
	}
	committed, ok := c.pair(financial.ExpiryCommit, pass, 0)
	if !ok {
		return false, 0, false
	}
	if _, ok := c.pair(financial.ExpiryRollback, pass, 0); !ok {
		return false, 0, false
	}
	if committed.Error != financial.ExpiryErrorNone {
		return true, financial.ExpiryCountZero, true
	}
	return false, expiryWorkerCount(int(scanned)), true
}

func (r *slog21ExpiryRecorder) complete() bool {
	if r.invalid.Load() || !r.bound.Load() || !r.doneReady.Load() || r.controlN != 5 || r.workerN < 2 || r.workerN > len(r.worker) {
		return false
	}
	for _, o := range r.control[:r.controlN] {
		if !o.Valid() {
			return false
		}
	}
	for _, o := range r.worker[:r.workerN] {
		if !o.Valid() {
			return false
		}
	}
	control := slog21ExpiryCursor{records: r.control[:r.controlN]}
	if !control.point(financial.ExpiryBind, 0, 0) {
		return false
	}
	if r.control[0].Context != financial.ExpiryContextNone {
		return false
	}
	cancel, ok := control.pair(financial.ExpiryCancel, 0, 0)
	if !ok || cancel.Error != financial.ExpiryErrorNone || cancel.Context != financial.ExpiryContextCancelled {
		return false
	}
	wait, ok := control.pair(financial.ExpiryWait, 0, 0)
	if !ok || wait.Error != financial.ExpiryErrorNone || wait.Context != financial.ExpiryContextCancelled || control.n != len(control.records) {
		return false
	}
	worker := slog21ExpiryCursor{records: r.worker[:r.workerN]}
	if !worker.point(financial.ExpiryStart, 0, 0) {
		return false
	}
	var pass uint16
	for worker.peek() == financial.ExpiryPass {
		if pass == ^uint16(0) {
			return false
		}
		pass++
		if worker.n >= len(worker.records) {
			return false
		}
		entered := worker.records[worker.n]
		if entered.Phase != financial.ExpiryPass || entered.Edge != financial.ExpiryEnter || entered.Pass != pass {
			return false
		}
		worker.n++
		failed, wantCount, ok := worker.sql(pass)
		if !ok || worker.n >= len(worker.records) {
			return false
		}
		returned := worker.records[worker.n]
		kind := financial.ExpiryErrorNone
		if failed {
			kind = financial.ExpiryErrorOther
		}
		if returned.Phase != financial.ExpiryPass || returned.Edge != financial.ExpiryReturn || returned.Pass != pass || returned.Error != kind || returned.Count != wantCount {
			return false
		}
		worker.n++
		stopped, ok := worker.pair(financial.ExpiryStop, pass, 0)
		if !ok || stopped.Error != financial.ExpiryErrorNone || (stopped.Context != financial.ExpiryContextCancelled && stopped.Context != financial.ExpiryContextDeadline) {
			return false
		}
	}
	if pass == 0 || !worker.point(financial.ExpiryDoneReady, 0, 0) || worker.n != len(worker.records) || worker.records[worker.n-1].Context != financial.ExpiryContextCancelled {
		return false
	}
	return true
}

func slog21ExpiryText(o financial.ExpiryObservation, control bool, slot int) string {
	lane := "WORKER"
	if control {
		lane = "CONTROL"
	}
	if slot < 1 || slot > 8 {
		slot = 0
	}
	return "SLOG21_EXPIRY_SQL_LIFECYCLE lane=" + lane + " app=" + strconv.Itoa(slot) +
		" pass=" + strconv.Itoa(int(o.Pass)) + " item=" + strconv.Itoa(int(o.Item)) +
		" phase=" + slog21DiagLabel(int(o.Phase), []string{"UNKNOWN", "BIND", "START", "PASS", "STOP", "CANCEL", "WAIT", "DONE_READY", "BEGIN", "QUERY", "SCAN", "VALIDATE", "ROWS_ERR", "ROWS_CLOSE", "EXEC", "ROWS_AFFECTED", "COMMIT", "ROLLBACK"}) +
		" edge=" + slog21DiagLabel(int(o.Edge), []string{"UNKNOWN", "POINT", "ENTER", "RETURN"}) +
		" error=" + slog21DiagLabel(int(o.Error), []string{"UNKNOWN", "NONE", "CANCELLED", "DEADLINE", "TX_DONE", "CONN_DONE", "SQLITE", "OTHER"}) +
		" sqlite=" + slog21DiagLabel(int(o.SQLite), []string{"NONE", "UNKNOWN", "1", "4", "5", "6", "9", "10", "19", "261", "517", "773", "262", "518", "4106"}) +
		" context=" + slog21DiagLabel(int(o.Context), []string{"UNKNOWN", "NONE", "CANCELLED", "DEADLINE", "OTHER"}) +
		" elapsed=" + slog21DiagLabel(int(o.Elapsed), []string{"NA", "LT1MS", "1MS_LT100MS", "100MS_LT1S", "1S_LT10S", "10S_LT60S", "GE60S", "INVALID"}) +
		" count=" + slog21DiagLabel(int(o.Count), []string{"NA", "ZERO", "ONE", "MANY", "INVALID"})
}

func (f *slog21Fixture) flushExpirySQL() {
	if f.expiryCensusInvalid || f.expiryCount == 0 {
		f.t.Error("SLOG21_EXPIRY_CENSUS_INVALID")
	}
	for slot := 0; slot < f.expiryCount; slot++ {
		r := f.expiryRecorders[slot]
		if r == nil || !r.workerBusy.CompareAndSwap(false, true) {
			f.t.Error("SLOG21_EXPIRY_FLUSH_INVALID")
			continue
		}
		if !r.controlBusy.CompareAndSwap(false, true) {
			r.invalid.Store(true)
			r.workerBusy.Store(false)
			f.t.Error("SLOG21_EXPIRY_FLUSH_INVALID")
			continue
		}
		valid := !r.flushed && r.complete()
		for i := 0; i < r.controlN; i++ {
			f.t.Log(slog21ExpiryText(r.control[i], true, slot+1))
		}
		for i := 0; i < r.workerN; i++ {
			f.t.Log(slog21ExpiryText(r.worker[i], false, slot+1))
		}
		r.flushed = true
		valid = valid && !r.invalid.Load()
		r.controlBusy.Store(false)
		r.workerBusy.Store(false)
		if !valid {
			f.t.Error("SLOG21_EXPIRY_TRACE_INVALID")
		}
	}
}

// Pure finite vectors only: these helpers do not open an App, DB or worker.
func slog21ExpiryPureRecord(phase financial.ExpiryPhase, edge financial.ExpiryEdge, pass uint16, item uint8) financial.ExpiryObservation {
	o := financial.ExpiryObservation{Phase: phase, Edge: edge, Pass: pass, Item: item, Error: financial.ExpiryErrorNone, Context: financial.ExpiryContextNone}
	if edge == financial.ExpiryReturn {
		o.Elapsed = financial.ExpiryLT1MS
		if phase == financial.ExpiryPass {
			o.Count = financial.ExpiryCountZero
		}
		if phase == financial.ExpiryRowsAffected {
			o.Count = financial.ExpiryCountOne
		}
	}
	if phase == financial.ExpiryWait || phase == financial.ExpiryDoneReady || edge == financial.ExpiryReturn && (phase == financial.ExpiryCancel || phase == financial.ExpiryStop) {
		o.Context = financial.ExpiryContextCancelled
	}
	return o
}

func slog21ExpiryPurePair(r *slog21ExpiryRecorder, phase financial.ExpiryPhase, pass uint16, item uint8, kind financial.ExpiryError) {
	r.capture(slog21ExpiryPureRecord(phase, financial.ExpiryEnter, pass, item))
	o := slog21ExpiryPureRecord(phase, financial.ExpiryReturn, pass, item)
	o.Error = kind
	r.capture(o)
}

func slog21ExpiryPurePass(r *slog21ExpiryRecorder, pass uint16) {
	r.capture(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryEnter, pass, 0))
	for _, phase := range []financial.ExpiryPhase{financial.ExpiryBegin, financial.ExpiryQuery, financial.ExpiryRowsErr, financial.ExpiryRowsClose, financial.ExpiryCommit} {
		slog21ExpiryPurePair(r, phase, pass, 0, financial.ExpiryErrorNone)
	}
	slog21ExpiryPurePair(r, financial.ExpiryRollback, pass, 0, financial.ExpiryErrorTxDone)
	r.capture(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryReturn, pass, 0))
	slog21ExpiryPurePair(r, financial.ExpiryStop, pass, 0, financial.ExpiryErrorNone)
}

func slog21ExpiryPureFinish(r *slog21ExpiryRecorder) {
	slog21ExpiryPurePair(r, financial.ExpiryCancel, 0, 0, financial.ExpiryErrorNone)
	r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryEnter, 0, 0))
	r.capture(slog21ExpiryPureRecord(financial.ExpiryDoneReady, financial.ExpiryPoint, 0, 0))
	r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryReturn, 0, 0))
}

func slog21ExpiryPureComplete(passes uint16) *slog21ExpiryRecorder {
	r := &slog21ExpiryRecorder{}
	r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
	r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
	for pass := uint16(1); pass <= passes; pass++ {
		slog21ExpiryPurePass(r, pass)
	}
	slog21ExpiryPureFinish(r)
	return r
}

func slog21ExpiryPureBranch(failure financial.ExpiryPhase, metadata financial.ExpiryCount) *slog21ExpiryRecorder {
	r := &slog21ExpiryRecorder{}
	r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
	r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
	r.capture(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryEnter, 1, 0))
	pair := func(phase financial.ExpiryPhase, item uint8) bool {
		kind := financial.ExpiryErrorNone
		if phase == failure {
			kind = financial.ExpiryErrorOther
		}
		r.capture(slog21ExpiryPureRecord(phase, financial.ExpiryEnter, 1, item))
		o := slog21ExpiryPureRecord(phase, financial.ExpiryReturn, 1, item)
		o.Error = kind
		if phase == financial.ExpiryRowsAffected {
			o.Count = metadata
			if kind != financial.ExpiryErrorNone {
				o.Count = financial.ExpiryCountNA
			}
		}
		r.capture(o)
		return kind == financial.ExpiryErrorNone
	}
	begun := pair(financial.ExpiryBegin, 0)
	succeeded := begun
	if succeeded {
		succeeded = pair(financial.ExpiryQuery, 0)
		if succeeded {
			succeeded = pair(financial.ExpiryScan, 1)
			if succeeded && failure == financial.ExpiryValidate {
				o := slog21ExpiryPureRecord(financial.ExpiryValidate, financial.ExpiryPoint, 1, 1)
				o.Error = financial.ExpiryErrorOther
				r.capture(o)
				succeeded = false
			}
			if !succeeded {
				pair(financial.ExpiryRowsClose, 0)
			} else {
				iterationOK := pair(financial.ExpiryRowsErr, 0)
				closeOK := pair(financial.ExpiryRowsClose, 0)
				succeeded = iterationOK && closeOK
				if succeeded {
					succeeded = pair(financial.ExpiryExec, 1)
				}
				if succeeded {
					succeeded = pair(financial.ExpiryRowsAffected, 1) && metadata == financial.ExpiryCountOne
				}
				if succeeded {
					succeeded = pair(financial.ExpiryCommit, 0)
				}
			}
		}
		// This cleanup error is deliberately not assigned to succeeded.
		pair(financial.ExpiryRollback, 0)
	}
	o := slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryReturn, 1, 0)
	if succeeded {
		o.Count = financial.ExpiryCountOne
	} else {
		o.Error = financial.ExpiryErrorOther
	}
	r.capture(o)
	slog21ExpiryPurePair(r, financial.ExpiryStop, 1, 0, financial.ExpiryErrorNone)
	slog21ExpiryPureFinish(r)
	return r
}

func TestExpirySQLRecorderPure20261021(t *testing.T) {
	t.Run("startup-binding", func(t *testing.T) {
		type originalKey struct{}
		base, cancel := context.WithCancelCause(context.WithValue(context.Background(), originalKey{}, "synthetic-public-value"))
		defer cancel(nil)
		calls := 0
		sink := func(financial.ExpiryObservation) { calls++ }
		startup := withExpiryOpenObserver(base, sink)
		original, extracted := takeExpiryOpenObserver(startup)
		if original != base || extracted == nil || withExpiryOpenObserver(base, nil) != base || startup.Done() != base.Done() || startup.Value(originalKey{}) != base.Value(originalKey{}) || calls != 0 {
			t.Fatal("EXPIRY_STARTUP_BINDING_CONTROL")
		}
		if _, absent := takeExpiryOpenObserver(original); absent != nil {
			t.Fatal("EXPIRY_STARTUP_VALUE_PROPAGATED")
		}
		cause := errors.New("synthetic-public-cause")
		cancel(cause)
		if startup.Err() != base.Err() || context.Cause(startup) != context.Cause(base) || context.Cause(original) != cause {
			t.Fatal("EXPIRY_STARTUP_CONTEXT_CONTROL")
		}
	})
	t.Run("legal-interleaving", func(t *testing.T) {
		// Cancel and Wait-enter may occur before the launched goroutine runs.
		r := &slog21ExpiryRecorder{}
		r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
		slog21ExpiryPurePair(r, financial.ExpiryCancel, 0, 0, financial.ExpiryErrorNone)
		r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryEnter, 0, 0))
		emit := func(o financial.ExpiryObservation) {
			o.Context = financial.ExpiryContextCancelled
			r.capture(o)
		}
		emit(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
		emit(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryEnter, 1, 0))
		emit(slog21ExpiryPureRecord(financial.ExpiryBegin, financial.ExpiryEnter, 1, 0))
		begun := slog21ExpiryPureRecord(financial.ExpiryBegin, financial.ExpiryReturn, 1, 0)
		begun.Error = financial.ExpiryErrorCancelled
		emit(begun)
		returned := slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryReturn, 1, 0)
		returned.Error = financial.ExpiryErrorOther
		emit(returned)
		emit(slog21ExpiryPureRecord(financial.ExpiryStop, financial.ExpiryEnter, 1, 0))
		emit(slog21ExpiryPureRecord(financial.ExpiryStop, financial.ExpiryReturn, 1, 0))
		emit(slog21ExpiryPureRecord(financial.ExpiryDoneReady, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryReturn, 0, 0))
		if !r.complete() {
			t.Fatal("EXPIRY_CANCEL_BEFORE_START_CONTROL")
		}
		// Cancel while a pass owns a successfully begun logical Tx. No new
		// wait/SQL is used to produce this pure legal partial-order witness.
		r = &slog21ExpiryRecorder{}
		r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryEnter, 1, 0))
		slog21ExpiryPurePair(r, financial.ExpiryBegin, 1, 0, financial.ExpiryErrorNone)
		slog21ExpiryPurePair(r, financial.ExpiryCancel, 0, 0, financial.ExpiryErrorNone)
		r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryEnter, 0, 0))
		slog21ExpiryPurePair(r, financial.ExpiryQuery, 1, 0, financial.ExpiryErrorCancelled)
		slog21ExpiryPurePair(r, financial.ExpiryRollback, 1, 0, financial.ExpiryErrorTxDone)
		r.capture(returned)
		slog21ExpiryPurePair(r, financial.ExpiryStop, 1, 0, financial.ExpiryErrorNone)
		r.capture(slog21ExpiryPureRecord(financial.ExpiryDoneReady, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryWait, financial.ExpiryReturn, 0, 0))
		if !r.complete() {
			t.Fatal("EXPIRY_CANCEL_DURING_PASS_CONTROL")
		}
	})
	t.Run("multiple-passes", func(t *testing.T) {
		if !slog21ExpiryPureComplete(2).complete() || !slog21ExpiryPureComplete(3).complete() {
			t.Fatal("EXPIRY_MULTIPASS_CONTROL")
		}
		// A pure one-item grammar vector is not a Scan/UPDATE DB execution.
		r := &slog21ExpiryRecorder{}
		r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
		r.capture(slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryEnter, 1, 0))
		for _, phase := range []financial.ExpiryPhase{financial.ExpiryBegin, financial.ExpiryQuery} {
			slog21ExpiryPurePair(r, phase, 1, 0, financial.ExpiryErrorNone)
		}
		slog21ExpiryPurePair(r, financial.ExpiryScan, 1, 1, financial.ExpiryErrorNone)
		for _, phase := range []financial.ExpiryPhase{financial.ExpiryRowsErr, financial.ExpiryRowsClose} {
			slog21ExpiryPurePair(r, phase, 1, 0, financial.ExpiryErrorNone)
		}
		for _, phase := range []financial.ExpiryPhase{financial.ExpiryExec, financial.ExpiryRowsAffected} {
			slog21ExpiryPurePair(r, phase, 1, 1, financial.ExpiryErrorNone)
		}
		slog21ExpiryPurePair(r, financial.ExpiryCommit, 1, 0, financial.ExpiryErrorNone)
		slog21ExpiryPurePair(r, financial.ExpiryRollback, 1, 0, financial.ExpiryErrorTxDone)
		o := slog21ExpiryPureRecord(financial.ExpiryPass, financial.ExpiryReturn, 1, 0)
		o.Count = financial.ExpiryCountOne
		r.capture(o)
		slog21ExpiryPurePair(r, financial.ExpiryStop, 1, 0, financial.ExpiryErrorNone)
		slog21ExpiryPureFinish(r)
		if !r.complete() {
			t.Fatal("EXPIRY_ONE_ITEM_GRAMMAR_CONTROL")
		}
		for _, phase := range []financial.ExpiryPhase{financial.ExpiryBegin, financial.ExpiryQuery, financial.ExpiryScan, financial.ExpiryValidate, financial.ExpiryRowsErr, financial.ExpiryRowsClose, financial.ExpiryExec, financial.ExpiryRowsAffected, financial.ExpiryCommit, financial.ExpiryRollback} {
			if !slog21ExpiryPureBranch(phase, financial.ExpiryCountOne).complete() {
				t.Fatal("EXPIRY_BRANCH_GRAMMAR_CONTROL")
			}
		}
		for _, count := range []financial.ExpiryCount{financial.ExpiryCountZero, financial.ExpiryCountMany} {
			if !slog21ExpiryPureBranch(0, count).complete() {
				t.Fatal("EXPIRY_METADATA_BRANCH_CONTROL")
			}
		}
	})
	t.Run("missing-zero", func(t *testing.T) {
		if (&slog21ExpiryRecorder{}).complete() || slog21ExpiryPureComplete(0).complete() {
			t.Fatal("EXPIRY_ZERO_EVIDENCE_CONTROL")
		}
		r := slog21ExpiryPureComplete(1)
		r.workerN--
		if r.complete() {
			t.Fatal("EXPIRY_MISSING_DONE_CONTROL")
		}
	})
	t.Run("overflow-latch", func(t *testing.T) {
		r := &slog21ExpiryRecorder{}
		r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
		for i := 0; i <= slog21ExpiryWorkerCapacity; i++ {
			r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
		}
		if r.workerN != 4096 || !r.invalid.Load() || r.complete() {
			t.Fatal("EXPIRY_WORKER_OVERFLOW_CONTROL")
		}
		r = &slog21ExpiryRecorder{}
		for i := 0; i <= slog21ExpiryControlCapacity; i++ {
			r.capture(slog21ExpiryPureRecord(financial.ExpiryBind, financial.ExpiryPoint, 0, 0))
		}
		if r.controlN != 32 || !r.invalid.Load() || r.complete() {
			t.Fatal("EXPIRY_CONTROL_OVERFLOW_CONTROL")
		}
	})
	t.Run("illegal-sequence", func(t *testing.T) {
		for _, mutate := range []func(*slog21ExpiryRecorder){
			func(r *slog21ExpiryRecorder) { r.worker[1].Pass = 2 },
			func(r *slog21ExpiryRecorder) { r.worker[10].Phase = financial.ExpiryRollback },
			func(r *slog21ExpiryRecorder) { r.worker[5].SQLite = financial.ExpirySQLiteUnknown },
			func(r *slog21ExpiryRecorder) { r.worker[5].Context = 255 },
			func(r *slog21ExpiryRecorder) { r.control[1] = r.control[0] },
			func(r *slog21ExpiryRecorder) { copy(r.worker[10:], r.worker[12:r.workerN]); r.workerN -= 2 },
		} {
			r := slog21ExpiryPureComplete(1)
			mutate(r)
			if r.complete() {
				t.Fatal("EXPIRY_ILLEGAL_SEQUENCE_CONTROL")
			}
		}
		r := &slog21ExpiryRecorder{}
		r.workerBusy.Store(true)
		r.capture(slog21ExpiryPureRecord(financial.ExpiryStart, financial.ExpiryPoint, 0, 0))
		if !r.invalid.Load() || r.workerN != 0 {
			t.Fatal("EXPIRY_SAME_LANE_REENTRY_CONTROL")
		}
		r.workerBusy.Store(false)
	})
	t.Run("finite-format", func(t *testing.T) {
		r := slog21ExpiryPureComplete(2)
		for i := 0; i < r.workerN; i++ {
			line := slog21ExpiryText(r.worker[i], false, 8)
			if len(line) > 256 || len(strings.Fields(line)) != 12 || strings.Contains(line, "SYNTHETIC_EXPIRY_PUBLIC_CANARY") {
				t.Fatal("EXPIRY_FINITE_FORMAT_CONTROL")
			}
			for _, b := range []byte(line) {
				if b > 127 {
					t.Fatal("EXPIRY_NONASCII_FORMAT_CONTROL")
				}
			}
		}
		unknown := financial.ExpiryObservation{Phase: 255, Edge: 255, Error: 255, SQLite: 255, Context: 255, Elapsed: 255, Count: 255}
		if slog21ExpiryText(unknown, false, 0) != "SLOG21_EXPIRY_SQL_LIFECYCLE lane=WORKER app=0 pass=0 item=0 phase=UNKNOWN edge=UNKNOWN error=UNKNOWN sqlite=UNKNOWN context=UNKNOWN elapsed=UNKNOWN count=UNKNOWN" {
			t.Fatal("EXPIRY_UNKNOWN_FORMAT_CONTROL")
		}
	})
}
