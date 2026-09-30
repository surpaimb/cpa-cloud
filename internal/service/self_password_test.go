// Independently authored tests for docs/employee-self-password-change-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const selfOldPassword = "a-long-self-password"
const selfNewPassword = "a-new-long-password"

type selfPasswordFixture struct {
	app         *App
	server      *httptest.Server
	id          string
	adminCookie *http.Cookie
	adminCSRF   string
	cookie      *http.Cookie
	csrf        string
	dir         string
}

func newSelfPasswordFixture(t *testing.T) selfPasswordFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, true)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	cookie, csrf := selfRedeem(t, server.URL, item.ID, secret)
	return selfPasswordFixture{app, server, item.ID, adminCookie, adminCSRF, cookie, csrf, dir}
}

func (f selfPasswordFixture) change(t *testing.T, cookie *http.Cookie, csrf, body, origin string) *http.Response {
	t.Helper()
	return selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/password", body, origin, cookie, csrf)
}

func selfPasswordBody(old, next string) string {
	return `{"current_password":` + quoteJSON(old) + `,"new_password":` + quoteJSON(next) + `}`
}

func (f selfPasswordFixture) login(t *testing.T, password string) *http.Response {
	t.Helper()
	return selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(f.id)+`,"password":`+quoteJSON(password)+`}`, f.server.URL, nil, "")
}

func TestSelfPasswordAuthorizationAndValidation(t *testing.T) {
	f := newSelfPasswordFixture(t)
	body := selfPasswordBody(selfOldPassword, selfNewPassword)
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		csrf   string
		origin string
		status int
	}{
		{"anonymous", nil, "", f.server.URL, 401},
		{"admin cookie", f.adminCookie, f.adminCSRF, f.server.URL, 401},
		{"missing origin", f.cookie, f.csrf, "", 403},
		{"wrong origin", f.cookie, f.csrf, "http://evil.invalid", 403},
		{"missing csrf", f.cookie, "", f.server.URL, 403},
		{"wrong csrf", f.cookie, "wrong", f.server.URL, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.change(t, tc.cookie, tc.csrf, body, tc.origin)
			if r.StatusCode != tc.status {
				t.Fatalf("status %d, want %d", r.StatusCode, tc.status)
			}
			r.Body.Close()
		})
	}
	req, err := http.NewRequest("POST", f.server.URL+"/self/api/v1/password", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("X-CSRF-Token", f.csrf)
	req.AddCookie(f.cookie)
	withoutCustomHeader, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if withoutCustomHeader.StatusCode != 403 {
		t.Fatalf("missing custom header accepted: %d", withoutCustomHeader.StatusCode)
	}
	withoutCustomHeader.Body.Close()
	for _, malformed := range []string{
		`{}`, `{"current_password":"x"}`, `{"current_password":1,"new_password":"valid-new-password"}`,
		`{"current_password":"x","new_password":"y","extra":"z"}`,
		`{"current_password":"x","current_password":"x","new_password":"y"}`,
		`{"current_password":"x","new_password":"y"} trailing`,
		strings.Repeat("x", 4097),
		`{"current_password":"` + string([]byte{0xff}) + `","new_password":"a-new-long-password"}`,
		selfPasswordBody(selfOldPassword, "short"),
	} {
		r := f.change(t, f.cookie, f.csrf, malformed, f.server.URL)
		if r.StatusCode != 400 {
			t.Fatalf("malformed request accepted: %d", r.StatusCode)
		}
		r.Body.Close()
		f.app.clearSelfFailures("127.0.0.1", f.id)
	}
	r := f.login(t, selfOldPassword)
	if r.StatusCode != 200 {
		t.Fatalf("validation changed password: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfPasswordWrongOldSamePasswordAndLimit(t *testing.T) {
	f := newSelfPasswordFixture(t)
	first := ""
	for i := range 5 {
		old := "wrong-old-password"
		next := selfNewPassword
		if i == 1 {
			old, next = selfOldPassword, selfOldPassword
		}
		r := f.change(t, f.cookie, f.csrf, selfPasswordBody(old, next), f.server.URL)
		if r.StatusCode != 401 {
			t.Fatalf("attempt %d status %d", i, r.StatusCode)
		}
		text := readBody(r)
		if strings.Contains(text, old) || strings.Contains(text, next) {
			t.Fatal("password echoed in failure response")
		}
		if i == 0 {
			first = text
		} else if text != first {
			t.Fatalf("credential failure details differ: %q != %q", text, first)
		}
	}
	r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("limiter bypassed: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfPasswordRevokesEverySessionAndSurvivesRestart(t *testing.T) {
	f := newSelfPasswordFixture(t)
	second := f.login(t, selfOldPassword)
	if second.StatusCode != 200 || len(second.Cookies()) != 1 {
		t.Fatalf("second login: %d", second.StatusCode)
	}
	secondCookie := second.Cookies()[0]
	second.Body.Close()
	r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
	if r.StatusCode != 204 || len(r.Cookies()) != 1 || r.Cookies()[0].MaxAge != -1 || r.Cookies()[0].Path != "/self/" {
		t.Fatalf("change result: status=%d cookies=%+v", r.StatusCode, r.Cookies())
	}
	if body := readBody(r); body != "" {
		t.Fatalf("success echoed content: %q", body)
	}
	var storedHash []byte
	if err := f.app.store.db.QueryRow(`SELECT password_hash FROM employee_self_credentials WHERE employee_id=?`, f.id).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storedHash), selfOldPassword) || strings.Contains(string(storedHash), selfNewPassword) || !strings.HasPrefix(string(storedHash), "$2") {
		t.Fatal("replacement password was not stored as a bcrypt hash")
	}
	for _, cookie := range []*http.Cookie{f.cookie, secondCookie} {
		r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/session", "", "", cookie, "")
		if r.StatusCode != 401 {
			t.Fatalf("old session active: %d", r.StatusCode)
		}
		r.Body.Close()
	}
	f.server.Close()
	_ = f.app.Close()
	reopened := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	for password, want := range map[string]int{selfOldPassword: 401, selfNewPassword: 200} {
		r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(f.id)+`,"password":`+quoteJSON(password)+`}`, server.URL, nil, "")
		if r.StatusCode != want {
			t.Fatalf("restart login status for expected %d: %d", want, r.StatusCode)
		}
		r.Body.Close()
	}
}

func TestSelfPasswordConcurrentCompareAndSwap(t *testing.T) {
	f := newSelfPasswordFixture(t)
	second := f.login(t, selfOldPassword)
	if second.StatusCode != 200 {
		t.Fatalf("second login: %d", second.StatusCode)
	}
	secondCookie := second.Cookies()[0]
	var secondCSRF struct {
		CSRF string `json:"csrf_token"`
	}
	decodeResponse(t, second, &secondCSRF)
	cookies := []*http.Cookie{f.cookie, secondCookie}
	csrf := []string{f.csrf, secondCSRF.CSRF}
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.change(t, cookies[i], csrf[i], selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
			results <- r.StatusCode
			r.Body.Close()
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for status := range results {
		if status == 204 {
			success++
		} else if status != 401 {
			t.Fatalf("concurrent status %d", status)
		}
	}
	if success != 1 {
		t.Fatalf("successful changes: %d", success)
	}
}

func TestSelfPasswordLogoutDuringHashingCannotCommit(t *testing.T) {
	f := newSelfPasswordFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.app.selfPasswordBeforeTx = func() {
		close(entered)
		<-release
	}
	status := make(chan int, 1)
	go func() {
		r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
		status <- r.StatusCode
		r.Body.Close()
	}()
	<-entered
	r := selfRequestTest(t, "DELETE", f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
	if r.StatusCode != 204 {
		t.Fatalf("logout status %d", r.StatusCode)
	}
	r.Body.Close()
	close(release)
	if got := <-status; got != 401 {
		t.Fatalf("revoked session changed password: %d", got)
	}
	r = f.login(t, selfOldPassword)
	if r.StatusCode != 200 {
		t.Fatalf("old password changed after logout: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfPasswordStorageFailuresAndUnknownCommit(t *testing.T) {
	for _, statement := range []string{
		`CREATE TRIGGER fail_password_update BEFORE UPDATE ON employee_self_credentials BEGIN SELECT RAISE(ABORT, 'injected'); END`,
		`CREATE TRIGGER fail_password_delete BEFORE DELETE ON employee_self_sessions BEGIN SELECT RAISE(ABORT, 'injected'); END`,
	} {
		t.Run(statement[:28], func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			if _, err := f.app.store.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
			if r.StatusCode != 503 || len(r.Cookies()) != 0 {
				t.Fatalf("fault returned success material: %d %+v", r.StatusCode, r.Cookies())
			}
			r.Body.Close()
			r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/session", "", "", f.cookie, "")
			if r.StatusCode != 200 {
				t.Fatalf("rolled-back session lost: %d", r.StatusCode)
			}
			r.Body.Close()
		})
	}
	t.Run("commit rejected", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		f.app.selfPasswordCommit = func(*sql.Tx) error { return errors.New("injected commit failure") }
		r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
		if r.StatusCode != 503 || len(r.Cookies()) != 0 {
			t.Fatalf("commit fault returned success material: %d %+v", r.StatusCode, r.Cookies())
		}
		r.Body.Close()
		r = f.login(t, selfOldPassword)
		if r.StatusCode != 200 {
			t.Fatalf("old password not restored: %d", r.StatusCode)
		}
		r.Body.Close()
	})
	t.Run("commit outcome unknown", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		f.app.selfPasswordCommit = func(tx *sql.Tx) error {
			if err := tx.Commit(); err != nil {
				return err
			}
			return errors.New("injected ambiguous commit result")
		}
		r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
		if r.StatusCode != 503 || len(r.Cookies()) != 0 {
			t.Fatalf("unknown commit returned success material: %d %+v", r.StatusCode, r.Cookies())
		}
		r.Body.Close()
		r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/session", "", "", f.cookie, "")
		if r.StatusCode != 401 {
			t.Fatalf("committed session not revoked: %d", r.StatusCode)
		}
		r.Body.Close()
		r = f.login(t, selfNewPassword)
		if r.StatusCode != 200 {
			t.Fatalf("new password not recoverable after uncertain commit: %d", r.StatusCode)
		}
		r.Body.Close()
	})
}

func TestSelfPasswordDisableCannotReactivate(t *testing.T) {
	f := newSelfPasswordFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.app.selfPasswordCommit = func(tx *sql.Tx) error {
		close(entered)
		<-release
		return tx.Commit()
	}
	changeStatus := make(chan int, 1)
	go func() {
		r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
		changeStatus <- r.StatusCode
		r.Body.Close()
	}()
	<-entered
	disableStatus := make(chan int, 1)
	go func() {
		r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, f.adminCookie, f.adminCSRF, f.server.URL)
		disableStatus <- r.StatusCode
		r.Body.Close()
	}()
	close(release)
	if status := <-changeStatus; status != 204 {
		t.Fatalf("change status %d", status)
	}
	if status := <-disableStatus; status != 200 {
		t.Fatalf("disable status %d", status)
	}
	r := f.login(t, selfNewPassword)
	if r.StatusCode != 401 {
		t.Fatalf("disable resurrected credential: %d", r.StatusCode)
	}
	r.Body.Close()
	var credentials int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id=?`, f.id).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("credential after disable: %d %v", credentials, err)
	}
}
