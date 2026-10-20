// Independently authored for docs/admin-logout-confirmation-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type adminLogoutFixture struct {
	t                          *testing.T
	dir                        string
	app                        *App
	appCloseAttempted          bool
	server                     *httptest.Server
	client                     *http.Client
	cookie, otherCookie        *http.Cookie
	csrf, otherCSRF, id, admin string
	otherID                    string
	mu                         sync.Mutex
	transform                  func(*http.Request) *http.Request
}

func newAdminLogoutFixture(t *testing.T) *adminLogoutFixture {
	t.Helper()
	f := &adminLogoutFixture{t: t, dir: t.TempDir(), client: &http.Client{Timeout: 10 * time.Second}}
	t.Cleanup(func() {
		if f.closeOwned() != nil {
			t.Error("ALOG_FIXTURE_CLOSE")
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if Initialize(ctx, f.dir, strings.NewReader("synthetic-logout-password\n")) != nil {
		t.Fatal("ALOG_FIXTURE_INIT")
	}
	if f.openOwned() != nil {
		t.Fatal("ALOG_FIXTURE_OPEN")
	}
	f.cookie, f.csrf = f.login()
	f.otherCookie, f.otherCSRF = f.login()
	if f.app.store.db.QueryRow(`SELECT id,admin_id FROM sessions WHERE token_digest=?`, f.app.secrets.digest("admin-session/v1", f.cookie.Value)).Scan(&f.id, &f.admin) != nil ||
		f.app.store.db.QueryRow(`SELECT id FROM sessions WHERE token_digest=?`, f.app.secrets.digest("admin-session/v1", f.otherCookie.Value)).Scan(&f.otherID) != nil {
		t.Fatal("ALOG_FIXTURE_IDENTITIES")
	}
	f.addOAuth(f.id, "current")
	f.addOAuth(f.otherID, "other")
	if f.oauthCount(f.id) != 1 || f.oauthCount(f.otherID) != 1 {
		t.Fatal("ALOG_NONEMPTY_OAUTH_CONTROLS")
	}
	return f
}

func (f *adminLogoutFixture) addOAuth(sessionID, suffix string) {
	f.t.Helper()
	secret := codexOAuthSessionSecret{State: "synthetic-logout-state-" + suffix, Verifier: "synthetic-logout-verifier-" + suffix, ClientID: "synthetic-logout-client", RedirectURI: "http://127.0.0.1/return"}
	payload, err := json.Marshal(secret)
	if err != nil {
		f.t.Fatal("ALOG_OAUTH_MARSHAL")
	}
	id := "logout-oauth-" + suffix
	ciphertext, err := f.app.secrets.encryptCodexOAuthSession(id, payload)
	clear(payload)
	if err != nil || len(ciphertext) == 0 {
		f.t.Fatal("ALOG_OAUTH_ENCRYPT")
	}
	_, err = f.app.store.db.Exec(`INSERT INTO codex_oauth_sessions(id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,used_at,created_at,status) VALUES(?,?,?,?,?,?,?,?,NULL,?,'pending')`, id, "logout-operation-"+suffix, f.admin, sessionID, f.app.secrets.digest("codex-oauth-state/v1", secret.State), ciphertext, "Synthetic logout control", time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), utcNow())
	if err != nil {
		f.t.Fatal("ALOG_OAUTH_INSERT")
	}
}

func (f *adminLogoutFixture) oauthCount(sessionID string) int {
	f.t.Helper()
	var count int
	if f.app.store.db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_sessions WHERE admin_session_id=?`, sessionID).Scan(&count) != nil {
		f.t.Fatal("ALOG_OAUTH_COUNT")
	}
	return count
}

func (f *adminLogoutFixture) assert503(response *http.Response) {
	f.t.Helper()
	defer adminLogoutClose(response)
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	var body map[string]any
	if response.StatusCode != http.StatusServiceUnavailable || len(response.Header.Values("Set-Cookie")) != 0 || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") || err != nil || len(raw) > 4096 || json.Unmarshal(raw, &body) != nil || len(body) != 1 {
		f.t.Fatal("ALOG_FIXED_503_RESPONSE")
	}
	detail, ok := body["error"].(map[string]any)
	if !ok || len(detail) != 2 || detail["code"] != "storage_unavailable" || detail["message"] != "Service is temporarily unavailable." {
		f.t.Fatal("ALOG_FIXED_503_JSON")
	}
}

func (f *adminLogoutFixture) startServer() {
	handler := f.app.Handler()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		transform := f.transform
		f.mu.Unlock()
		if transform != nil {
			r = transform(r)
		}
		handler.ServeHTTP(w, r)
	}))
}

func (f *adminLogoutFixture) request(method, path string, cookie *http.Cookie, csrf, origin string) *http.Response {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+path, nil)
	if err != nil {
		f.t.Fatal("ALOG_REQUEST_CREATE")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("ALOG_REQUEST_HTTP")
	}
	return response
}

func (f *adminLogoutFixture) login() (*http.Cookie, string) {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), http.MethodPost, f.server.URL+"/admin/api/v1/sessions",
		strings.NewReader(`{"username":"admin","password":"synthetic-logout-password"}`))
	if err != nil {
		f.t.Fatal("ALOG_LOGIN_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.server.URL)
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("ALOG_LOGIN_HTTP")
	}
	defer response.Body.Close()
	var body struct {
		CSRF string `json:"csrf_token"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) != nil || body.CSRF == "" {
		f.t.Fatal("ALOG_LOGIN_RESPONSE")
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == adminCookieName && cookie.Value != "" {
			return cookie, body.CSRF
		}
	}
	f.t.Fatal("ALOG_LOGIN_COOKIE")
	return nil, ""
}

func adminLogoutClearsCookie(response *http.Response) bool {
	for _, cookie := range response.Cookies() {
		if cookie.Name == adminCookieName && cookie.Value == "" && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}

func adminLogoutClose(response *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
}

func (f *adminLogoutFixture) exists(id string) bool {
	f.t.Helper()
	var count int
	if f.app.store.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, id).Scan(&count) != nil {
		f.t.Fatal("ALOG_PERSISTENCE_READ")
	}
	return count == 1
}

func (f *adminLogoutFixture) protectedFacts() [][]any {
	return f.sessionFacts(f.otherID)
}

func (f *adminLogoutFixture) sessionFacts(sessionID string) [][]any {
	f.t.Helper()
	var facts [][]any
	for _, query := range []string{
		`SELECT * FROM sessions WHERE id=? ORDER BY id`,
		`SELECT * FROM codex_oauth_sessions WHERE admin_session_id=? ORDER BY id`,
	} {
		rows, err := f.app.store.db.Query(query, sessionID)
		if err != nil {
			f.t.Fatal("ALOG_PROTECTED_QUERY")
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			f.t.Fatal("ALOG_PROTECTED_COLUMNS")
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if rows.Scan(pointers...) != nil {
				_ = rows.Close()
				f.t.Fatal("ALOG_PROTECTED_SCAN")
			}
			for i, value := range values {
				if b, ok := value.([]byte); ok {
					values[i] = append([]byte(nil), b...)
				}
			}
			facts = append(facts, values)
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if readErr != nil || closeErr != nil {
			f.t.Fatal("ALOG_PROTECTED_CLOSE")
		}
	}
	return facts
}

func (f *adminLogoutFixture) assertProtected(before [][]any) {
	f.t.Helper()
	if !reflect.DeepEqual(before, f.protectedFacts()) {
		f.t.Fatal("ALOG_OTHER_SESSION_CHANGED")
	}
	response := f.request(http.MethodGet, "/admin/api/v1/session", f.otherCookie, "", f.server.URL)
	defer adminLogoutClose(response)
	if response.StatusCode != http.StatusOK {
		f.t.Fatal("ALOG_OTHER_AUTH")
	}
}

func TestAdminLogoutDeleteAbortFailsClosed(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	trigger := `CREATE TRIGGER logout_abort BEFORE DELETE ON sessions WHEN OLD.id='` +
		strings.ReplaceAll(f.id, "'", "''") + `' BEGIN SELECT RAISE(ABORT,'synthetic-logout-abort'); END`
	if _, err := f.app.store.db.Exec(trigger); err != nil {
		t.Fatal("ALOG_ABORT_TRIGGER")
	}
	response := f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL)
	status, cleared := response.StatusCode, adminLogoutClearsCookie(response)
	if status == http.StatusServiceUnavailable {
		f.assert503(response)
	} else {
		adminLogoutClose(response)
	}
	present := f.exists(f.id)
	auth := f.request(http.MethodGet, "/admin/api/v1/session", f.cookie, "", f.server.URL)
	authStatus := auth.StatusCode
	adminLogoutClose(auth)
	f.assertProtected(before)
	if status == http.StatusNoContent && cleared && present && authStatus == http.StatusOK {
		t.Fatal("ADMIN_LOGOUT_OLD_DELETE_ABORT_ACCEPTED")
	}
	if status != http.StatusServiceUnavailable || cleared || !present || authStatus != http.StatusOK || f.oauthCount(f.id) != 1 {
		t.Fatal("ALOG_ABORT_FAIL_CLOSED")
	}
}

func (f *adminLogoutFixture) hooks(hooks adminLogoutHooks) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transform = func(r *http.Request) *http.Request {
		if r.Method == http.MethodDelete && r.URL.Path == "/admin/api/v1/sessions" {
			return r.WithContext(context.WithValue(r.Context(), adminLogoutHooksKey{}, hooks))
		}
		return r
	}
}

func (f *adminLogoutFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.app.store.db.Exec(query, args...); err != nil {
		f.t.Fatal("ALOG_OWNED_FIXTURE_SQL")
	}
}

func (f *adminLogoutFixture) closeOwned() error {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.client != nil {
		f.client.CloseIdleConnections()
	}
	if f.app == nil || f.appCloseAttempted {
		return nil
	}
	// Record the attempt before Close, so cleanup never retries a failed Close
	// or dereferences a nil App after a failed Open. An error is not a cleanup proof.
	f.appCloseAttempted = true
	return f.app.Close()
}

func (f *adminLogoutFixture) openOwned() error {
	if f.app != nil && !f.appCloseAttempted {
		return errors.New("controlled fixture already open")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	next, err := Open(ctx, Config{DataDir: f.dir, Listen: "127.0.0.1:0", Version: "logout-test"})
	if err != nil {
		if next != nil && next.Close() != nil {
			f.t.Error("ALOG_PARTIAL_OPEN_CLOSE")
		}
		return err
	}
	if next == nil {
		return errors.New("controlled fixture empty successful Open")
	}
	// Preserve the last owned App on failure; replace the reference only on success.
	f.app, f.appCloseAttempted = next, false
	f.startServer()
	return nil
}

func (f *adminLogoutFixture) reopen() {
	f.t.Helper()
	if f.closeOwned() != nil {
		f.t.Fatal("ALOG_REOPEN_CLOSE")
	}
	f.mu.Lock()
	f.transform = nil
	f.mu.Unlock()
	if f.openOwned() != nil {
		f.t.Fatal("ALOG_REOPEN_OPEN")
	}
}

func (f *adminLogoutFixture) assertState(present bool) {
	f.t.Helper()
	wantOAuth, wantStatus := 0, http.StatusUnauthorized
	if present {
		wantOAuth, wantStatus = 1, http.StatusOK
		var owner string
		if f.app.store.db.QueryRow(`SELECT admin_id FROM sessions WHERE id=?`, f.id).Scan(&owner) != nil || owner != f.admin {
			f.t.Fatal("ALOG_CURRENT_OWNER_PRESERVED")
		}
	}
	if f.exists(f.id) != present || f.oauthCount(f.id) != wantOAuth {
		f.t.Fatal("ALOG_CURRENT_PERSISTENT_STATE")
	}
	response := f.request(http.MethodGet, "/admin/api/v1/session", f.cookie, "", f.server.URL)
	defer adminLogoutClose(response)
	if response.StatusCode != wantStatus || len(response.Header.Values("Set-Cookie")) != 0 {
		f.t.Fatal("ALOG_MANUAL_OLD_COOKIE_AUTH")
	}
}

func (f *adminLogoutFixture) assert204(response *http.Response) {
	f.t.Helper()
	defer adminLogoutClose(response)
	if response.StatusCode != http.StatusNoContent || !adminLogoutClearsCookie(response) || len(response.Cookies()) != 1 {
		f.t.Fatal("ALOG_CONFIRMED_204")
	}
	cookie := response.Cookies()[0]
	if cookie.Name != adminCookieName || cookie.Path != "/admin/" || !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		f.t.Fatal("ALOG_CLEAR_COOKIE_ATTRIBUTES")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2))
	if err != nil || len(body) != 0 {
		f.t.Fatal("ALOG_204_BODY")
	}
}

func TestAdminLogoutConfirmedPersistence(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	f.assert204(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
	f.assertState(false)
	f.assertProtected(before)
	f.reopen()
	f.assertState(false)
	f.assertProtected(before)
}

func TestAdminLogoutNativeIgnoreZero(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	f.exec(`CREATE TRIGGER logout_ignore BEFORE DELETE ON sessions WHEN OLD.id='` + strings.ReplaceAll(f.id, "'", "''") + `' BEGIN SELECT RAISE(IGNORE); END`)
	f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
	f.assertState(true)
	f.assertProtected(before)
	f.reopen()
	f.assertState(true)
	f.assertProtected(before)
}

func TestAdminLogoutNativeReinsertConfirmation(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	f.exec(`CREATE TRIGGER logout_reinsert AFTER DELETE ON sessions WHEN OLD.id='` + strings.ReplaceAll(f.id, "'", "''") + `' BEGIN INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(OLD.id,OLD.admin_id,OLD.token_digest,OLD.csrf_token,OLD.expires_at,OLD.created_at); END`)
	f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
	f.assertState(true)
	f.assertProtected(before)
	f.reopen()
	f.assertState(true)
	f.assertProtected(before)
}

func TestAdminLogoutRealCommitRefusal(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	targetBefore := f.sessionFacts(f.id)
	liveApp := f.app
	var fk int
	if f.app.store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk) != nil || fk != 1 {
		t.Fatal("ALOG_REAL_COMMIT_FK_PRECONDITION")
	}
	f.exec(`CREATE TABLE logout_commit_guard(session_id TEXT REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED)`)
	f.exec(`INSERT INTO logout_commit_guard(session_id) VALUES(?)`, f.id)
	var observed, rejected atomic.Bool
	f.hooks(adminLogoutHooks{afterCommit: func(realErr error) error {
		observed.Store(true)
		rejected.Store(realErr != nil)
		return nil // A nil report cannot erase the actual SQLite Commit error.
	}})
	f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
	if !observed.Load() || !rejected.Load() {
		t.Fatal("ALOG_REAL_COMMIT_REFUSAL_NOT_OBSERVED")
	}
	// No reopen, manual ROLLBACK, connection replacement or pool reset can serve
	// as this control: the same live App must support a new read/write transaction.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	next, err := f.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("ALOG_LIVE_AFTER_COMMIT_BEGIN")
	}
	defer next.Rollback()
	var enabled, rows int
	if next.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled) != nil || enabled != 1 ||
		next.QueryRowContext(ctx, `SELECT COUNT(*) FROM logout_commit_guard WHERE session_id=?`, f.id).Scan(&rows) != nil || rows != 1 {
		t.Fatal("ALOG_LIVE_AFTER_COMMIT_READ")
	}
	result, err := next.ExecContext(ctx, `UPDATE logout_commit_guard SET session_id=session_id WHERE session_id=?`, f.id)
	if err != nil {
		t.Fatal("ALOG_LIVE_AFTER_COMMIT_WRITE")
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 || next.Commit() != nil || f.app != liveApp || f.appCloseAttempted {
		t.Fatal("ALOG_LIVE_AFTER_COMMIT_CONFIRM")
	}
	f.assertState(true)
	f.assertProtected(before)
	if !reflect.DeepEqual(targetBefore, f.sessionFacts(f.id)) {
		t.Fatal("ALOG_LIVE_AFTER_COMMIT_TARGET_CHANGED")
	}
	f.reopen()
	f.assertState(true)
	f.assertProtected(before)
	if !reflect.DeepEqual(targetBefore, f.sessionFacts(f.id)) {
		t.Fatal("ALOG_REOPEN_AFTER_COMMIT_TARGET_CHANGED")
	}
}

func TestAdminLogoutCommitUncertainty(t *testing.T) {
	for _, name := range []string{"before-real-commit", "after-real-commit"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminLogoutFixture(t)
			before := f.protectedFacts()
			var entered, committed atomic.Bool
			hooks := adminLogoutHooks{}
			if name == "before-real-commit" {
				hooks.beforeCommit = func(*sql.Tx) error {
					entered.Store(true)
					return errors.New("controlled pre-commit refusal")
				}
			} else {
				hooks.afterCommit = func(realErr error) error {
					entered.Store(true)
					committed.Store(realErr == nil)
					return errors.New("controlled post-real-commit report")
				}
			}
			f.hooks(hooks)
			f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
			if !entered.Load() || committed.Load() != (name == "after-real-commit") {
				t.Fatal("ALOG_COMMIT_ADAPTER_BOUNDARY")
			}
			f.assertState(name == "before-real-commit")
			f.assertProtected(before)
			f.reopen()
			f.assertState(name == "before-real-commit")
			f.assertProtected(before)
		})
	}
}

func TestAdminLogoutBoundaryErrors(t *testing.T) {
	for _, name := range []string{"begin-real-after-auth", "begin-adapter", "rows-error", "rows-zero", "rows-two", "confirmation-query-error"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminLogoutFixture(t)
			before := f.protectedFacts()
			var entered atomic.Bool
			hooks := adminLogoutHooks{}
			switch name {
			case "begin-real-after-auth":
				hooks.beforeBegin = func(context.Context) error {
					if err := f.app.store.db.Close(); err != nil {
						return err
					}
					entered.Store(true) // This callback is after requireAdmin, before real BeginTx.
					return nil
				}
			case "begin-adapter":
				hooks.beforeBegin = func(context.Context) error {
					entered.Store(true)
					return errors.New("controlled Begin boundary refusal")
				}
			default:
				hooks.result = func(tx *sql.Tx, result sql.Result) (int64, error) {
					count, err := result.RowsAffected()
					if err != nil || count != 1 {
						return 0, errors.New("controlled result precondition")
					}
					entered.Store(true)
					switch name {
					case "rows-error":
						return 0, errors.New("controlled result error")
					case "rows-zero":
						return 0, nil
					case "rows-two":
						return 2, nil
					case "confirmation-query-error":
						// Owned, transactional metadata adapter, not product DDL or real IO failure.
						if _, err := tx.Exec(`ALTER TABLE sessions RENAME TO logout_hidden_sessions`); err != nil {
							entered.Store(false)
							return 0, err
						}
					}
					return count, nil
				}
			}
			f.hooks(hooks)
			f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
			if !entered.Load() {
				t.Fatal("ALOG_FAILURE_ADAPTER_NOT_ENTERED")
			}
			if name == "begin-real-after-auth" {
				f.reopen() // Do not mistake authentication failure on a closed DB for Begin failure.
			}
			f.assertState(true)
			f.assertProtected(before)
			f.reopen()
			f.assertState(true)
			f.assertProtected(before)
		})
	}
}

func TestAdminLogoutAuthPrecedence(t *testing.T) {
	for _, name := range []string{"anonymous", "wrong-cookie", "missing-origin", "wrong-origin", "missing-csrf", "wrong-csrf"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminLogoutFixture(t)
			before := f.protectedFacts()
			var entered atomic.Int32
			f.hooks(adminLogoutHooks{beforeBegin: func(context.Context) error {
				entered.Add(1)
				return errors.New("must not reach logout boundary")
			}})
			cookie, csrf, origin := f.cookie, f.csrf, f.server.URL
			want := http.StatusForbidden
			switch name {
			case "anonymous":
				cookie, want = nil, http.StatusUnauthorized
			case "wrong-cookie":
				wrong := *f.cookie
				wrong.Value = "invalid-synthetic-cookie"
				cookie, want = &wrong, http.StatusUnauthorized
			case "missing-origin":
				origin = ""
			case "wrong-origin":
				origin = "http://wrong-origin.invalid"
			case "missing-csrf":
				csrf = ""
			case "wrong-csrf":
				csrf = "invalid-synthetic-csrf"
			}
			response := f.request(http.MethodDelete, "/admin/api/v1/sessions", cookie, csrf, origin)
			if response.StatusCode != want || len(response.Header.Values("Set-Cookie")) != 0 {
				adminLogoutClose(response)
				t.Fatal("ALOG_AUTH_PRECEDENCE")
			}
			adminLogoutClose(response)
			if entered.Load() != 0 {
				t.Fatal("ALOG_UNAUTHORIZED_HANDLER_ENTRY")
			}
			f.assertState(true)
			f.assertProtected(before)
		})
	}
}

func TestAdminLogoutConcurrentZeroRows(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	barrier, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f.hooks(adminLogoutHooks{beforeBegin: func(ctx context.Context) error {
		select {
		case arrived <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		case <-barrier.Done():
			return barrier.Err()
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-barrier.Done():
			return barrier.Err()
		}
	}})
	type outcome struct {
		response *http.Response
		err      error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		request, err := http.NewRequestWithContext(barrier, http.MethodDelete, f.server.URL+"/admin/api/v1/sessions", nil)
		if err != nil {
			t.Fatal("ALOG_CONCURRENT_REQUEST_CREATE")
		}
		request.AddCookie(f.cookie)
		request.Header.Set("X-CSRF-Token", f.csrf)
		request.Header.Set("Origin", f.server.URL)
		go func(request *http.Request) {
			response, err := f.client.Do(request)
			results <- outcome{response: response, err: err}
		}(request)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-barrier.Done():
			t.Fatal("ALOG_BOTH_AUTHENTICATED_BARRIER")
		}
	}
	releaseOnce.Do(func() { close(release) })
	successes, refusals := 0, 0
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.err != nil || result.response == nil {
				t.Fatal("ALOG_CONCURRENT_HTTP")
			}
			switch result.response.StatusCode {
			case http.StatusNoContent:
				successes++
				f.assert204(result.response)
			case http.StatusServiceUnavailable:
				refusals++
				f.assert503(result.response)
			default:
				adminLogoutClose(result.response)
				t.Fatal("ALOG_CONCURRENT_TERMINAL")
			}
		case <-barrier.Done():
			t.Fatal("ALOG_CONCURRENT_RESULT_BOUND")
		}
	}
	if successes != 1 || refusals != 1 {
		t.Fatal("ALOG_CONCURRENT_ONE_CONFIRMED_DELETE")
	}
	f.assertState(false)
	f.assertProtected(before)
	f.reopen()
	f.assertState(false)
	f.assertProtected(before)
}

func TestAdminLogoutOwnerPredicate(t *testing.T) {
	f := newAdminLogoutFixture(t)
	before := f.protectedFacts()
	targetBefore := f.sessionFacts(f.id)
	if len(before) != 2 || len(targetBefore) != 2 || f.oauthCount(f.id) != 1 || f.oauthCount(f.otherID) != 1 {
		t.Fatal("ALOG_OWNER_NONEMPTY_SNAPSHOT_CONTROLS")
	}
	var ownerChanged atomic.Bool
	f.hooks(adminLogoutHooks{afterBegin: func(tx *sql.Tx) error {
		// Both the extra administrator and owner change belong to this one tx.
		insert, err := tx.Exec(`INSERT INTO admins(id,username,password_hash,created_at) SELECT 'logout-other-admin','logout-other-admin',password_hash,created_at FROM admins WHERE id=?`, f.admin)
		if err != nil {
			return err
		}
		inserted, err := insert.RowsAffected()
		if err != nil || inserted != 1 {
			return errors.New("controlled temporary-admin precondition")
		}
		result, err := tx.Exec(`UPDATE sessions SET admin_id='logout-other-admin' WHERE id=? AND admin_id=?`, f.id, f.admin)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("controlled owner-change precondition")
		}
		ownerChanged.Store(true)
		return nil
	}})
	f.assert503(f.request(http.MethodDelete, "/admin/api/v1/sessions", f.cookie, f.csrf, f.server.URL))
	if !ownerChanged.Load() {
		t.Fatal("ALOG_OWNER_CHANGE_NOT_ENTERED")
	}
	assertRollbackFacts := func() {
		t.Helper()
		var total, temporary int
		if f.app.store.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&total) != nil || total != 1 ||
			f.app.store.db.QueryRow(`SELECT COUNT(*) FROM admins WHERE id='logout-other-admin'`).Scan(&temporary) != nil || temporary != 0 ||
			!reflect.DeepEqual(targetBefore, f.sessionFacts(f.id)) || !reflect.DeepEqual(before, f.protectedFacts()) ||
			f.oauthCount(f.id) != 1 || f.oauthCount(f.otherID) != 1 {
			t.Fatal("ALOG_OWNER_ROLLBACK_FACTS")
		}
	}
	assertRollbackFacts()
	f.assertState(true)
	f.assertProtected(before)
	f.reopen()
	assertRollbackFacts()
	f.assertState(true)
	f.assertProtected(before)

	// Independent negative profile: a genuinely persisted second admin remains
	// invalid. Do not delete it to obtain a successful Open or relax production.
	invalid := newAdminLogoutFixture(t)
	invalid.exec(`INSERT INTO admins(id,username,password_hash,created_at) SELECT 'logout-other-admin','logout-other-admin',password_hash,created_at FROM admins WHERE id=?`, invalid.admin)
	var total int
	if invalid.app.store.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&total) != nil || total != 2 {
		t.Fatal("ALOG_MULTI_ADMIN_NEGATIVE_PRECONDITION")
	}
	lastOwned := invalid.app
	if invalid.closeOwned() != nil {
		t.Fatal("ALOG_MULTI_ADMIN_NEGATIVE_CLOSE")
	}
	err := invalid.openOwned()
	if err == nil || err.Error() != "exactly one initialized administrator is required; run with --init" ||
		invalid.app != lastOwned || !invalid.appCloseAttempted || invalid.server != nil {
		t.Fatal("ALOG_PERSISTENT_MULTI_ADMIN_OPEN_REJECTION")
	}
	if invalid.closeOwned() != nil || (&adminLogoutFixture{}).closeOwned() != nil {
		t.Fatal("ALOG_CLOSED_AND_NIL_CLEANUP")
	}
}
