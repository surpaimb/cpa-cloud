// Independently authored tests for docs/employee-self-signout-others-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func selfSignOutBody(password string) string {
	return `{"current_password":` + quoteJSON(password) + `}`
}

func (f selfPasswordFixture) signOutOthers(t *testing.T, cookie *http.Cookie, csrf, body, origin string) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/sessions/revoke-others", body, origin, cookie, csrf)
}

func (f selfPasswordFixture) anotherSelfSession(t *testing.T, id string) (*http.Cookie, string) {
	t.Helper()
	r := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(id)+`,"password":`+quoteJSON(selfOldPassword)+`}`, f.server.URL, nil, "")
	if r.StatusCode != http.StatusOK || len(r.Cookies()) != 1 {
		t.Fatalf("additional self login status=%d", r.StatusCode)
	}
	var result struct {
		CSRF string `json:"csrf_token"`
	}
	decodeResponse(t, r, &result)
	return r.Cookies()[0], result.CSRF
}

func selfSessionStatus(t *testing.T, baseURL string, cookie *http.Cookie) int {
	t.Helper()
	r := selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/session", "", "", cookie, "")
	status := r.StatusCode
	r.Body.Close()
	return status
}

func TestSelfSignOutOthersDefaultOffAndAuthority(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	r := selfRequestTest(t, http.MethodPost, server.URL+"/self/api/v1/sessions/revoke-others", selfSignOutBody(selfOldPassword), server.URL, nil, "")
	if r.StatusCode != 404 {
		t.Fatalf("default-off route=%d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	for _, tc := range []struct {
		name         string
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{
		{"anonymous", nil, "", f.server.URL, 401},
		{"admin cookie", f.adminCookie, f.adminCSRF, f.server.URL, 401},
		{"missing origin", f.cookie, f.csrf, "", 403},
		{"wrong origin", f.cookie, f.csrf, "http://evil.invalid", 403},
		{"missing csrf", f.cookie, "", f.server.URL, 403},
		{"wrong csrf", f.cookie, "wrong", f.server.URL, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.signOutOthers(t, tc.cookie, tc.csrf, selfSignOutBody(selfOldPassword), tc.origin)
			if r.StatusCode != tc.want || r.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q", r.StatusCode, r.Header.Get("Cache-Control"))
			}
			r.Body.Close()
		})
	}
	request, err := http.NewRequest(http.MethodPost, f.server.URL+"/self/api/v1/sessions/revoke-others", strings.NewReader(selfSignOutBody(selfOldPassword)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", f.server.URL)
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 403 {
		t.Fatalf("missing custom header=%d", r.StatusCode)
	}
	r.Body.Close()
	request, err = http.NewRequest(http.MethodPost, f.server.URL+"/self/api/v1/sessions/revoke-others", strings.NewReader(selfSignOutBody(selfOldPassword)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", f.server.URL)
	request.Header.Set("Authorization", "Bearer synthetic-employee-key")
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 401 {
		t.Fatalf("Bearer authenticated self=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfSignOutOthersStrictInputAndLimiter(t *testing.T) {
	f := newSelfPasswordFixture(t)
	for _, body := range []string{
		`{}`, `{"current_password":1}`, `{"current_password":"x","employee_id":"other"}`,
		`{"current_password":"x","selector":"self_other"}`,
		`{"current_password":"x","current_password":"x"}`,
		`{"current_password":"x"} trailing`, strings.Repeat("x", 4097),
		`{"current_password":"` + string([]byte{0xff}) + `"}`,
	} {
		r := f.signOutOthers(t, f.cookie, f.csrf, body, f.server.URL)
		if r.StatusCode != 400 {
			t.Fatalf("malformed body accepted: %d", r.StatusCode)
		}
		if secret := readBody(r); strings.Contains(secret, "self_other") {
			t.Fatal("selector echoed")
		}
		f.app.clearSelfFailures("127.0.0.1", f.id)
	}
	r := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/sessions/revoke-others?employee_id=other", selfSignOutBody(selfOldPassword), f.server.URL, f.cookie, f.csrf)
	if r.StatusCode != 400 {
		t.Fatalf("query accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	f.app.clearSelfFailures("127.0.0.1", f.id)
	var first string
	for i := 0; i < 5; i++ {
		password := "wrong-old-password"
		if i == 1 {
			password = "short"
		}
		r = f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(password), f.server.URL)
		if r.StatusCode != 401 {
			t.Fatalf("wrong password attempt %d=%d", i, r.StatusCode)
		}
		body := readBody(r)
		if strings.Contains(body, password) || strings.Contains(body, f.id) {
			t.Fatal("secret or identity echoed")
		}
		if i == 0 {
			first = body
		} else if body != first {
			t.Fatal("credential failure changed with input")
		}
	}
	r = f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("limiter=%d retry=%q", r.StatusCode, r.Header.Get("Retry-After"))
	}
	r.Body.Close()
}

func TestSelfSignOutOthersSelectiveAndRestart(t *testing.T) {
	f := newSelfPasswordFixture(t)
	second, secondCSRF := f.anotherSelfSession(t, f.id)
	third, thirdCSRF := f.anotherSelfSession(t, f.id)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	if count := selfSessionCount(t, f.app, f.id); count != 3 {
		t.Fatalf("before count=%d", count)
	}
	r := f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
	if r.StatusCode != 204 || r.Header.Get("Cache-Control") != "no-store" || len(r.Cookies()) != 0 || readBody(r) != "" {
		t.Fatalf("signout status=%d", r.StatusCode)
	}
	if count := selfSessionCount(t, f.app, f.id); count != 1 {
		t.Fatalf("after count=%d", count)
	}
	for _, cookie := range []*http.Cookie{second, third} {
		if status := selfSessionStatus(t, f.server.URL, cookie); status != 401 {
			t.Fatalf("other session survived: %d", status)
		}
	}
	if status := selfSessionStatus(t, f.server.URL, f.cookie); status != 200 {
		t.Fatalf("current session lost: %d", status)
	}
	if status := selfSessionStatus(t, f.server.URL, otherCookie); status != 200 {
		t.Fatalf("foreign employee session lost: %d", status)
	}
	for _, tc := range []struct {
		cookie *http.Cookie
		csrf   string
	}{{second, secondCSRF}, {third, thirdCSRF}} {
		r = f.signOutOthers(t, tc.cookie, tc.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
		if r.StatusCode != 401 {
			t.Fatalf("removed session mutation=%d", r.StatusCode)
		}
		r.Body.Close()
	}
	r = f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
	if r.StatusCode != 204 || readBody(r) != "" {
		t.Fatalf("zero-others status=%d", r.StatusCode)
	}
	f.server.Close()
	_ = f.app.Close()
	reopened := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	if status := selfSessionStatus(t, server.URL, f.cookie); status != 200 {
		t.Fatalf("current session after restart=%d", status)
	}
	if status := selfSessionStatus(t, server.URL, second); status != 401 {
		t.Fatalf("revoked session after restart=%d", status)
	}
	if status := selfSessionStatus(t, server.URL, otherCookie); status != 200 {
		t.Fatalf("foreign after restart=%d", status)
	}
}

func selfSessionCount(t *testing.T, app *App, id string) int {
	t.Helper()
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM employee_self_sessions WHERE employee_id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSelfSignOutOthersSameAndDifferentEmployeeRaces(t *testing.T) {
	t.Run("same employee", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		second, secondCSRF := f.anotherSelfSession(t, f.id)
		entered := make(chan struct{})
		release := make(chan struct{})
		var arrivals atomic.Int32
		f.app.selfSignOutOthersBeforeTx = func() {
			if arrivals.Add(1) == 2 {
				close(entered)
			}
			<-release
		}
		results := make(chan int, 2)
		var wg sync.WaitGroup
		for _, tc := range []struct {
			cookie *http.Cookie
			csrf   string
		}{{f.cookie, f.csrf}, {second, secondCSRF}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := f.signOutOthers(t, tc.cookie, tc.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
				results <- r.StatusCode
				r.Body.Close()
			}()
		}
		<-entered
		close(release)
		wg.Wait()
		close(results)
		seen := map[int]int{}
		for status := range results {
			seen[status]++
		}
		if seen[204] != 1 || seen[401] != 1 || selfSessionCount(t, f.app, f.id) != 1 {
			t.Fatalf("race results=%v", seen)
		}
	})
	t.Run("different employees", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		f.anotherSelfSession(t, f.id)
		other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
		secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
		otherCookie, otherCSRF := selfRedeem(t, f.server.URL, other.ID, secret)
		f.anotherSelfSession(t, other.ID)
		results := make(chan int, 2)
		var wg sync.WaitGroup
		for _, tc := range []struct {
			cookie *http.Cookie
			csrf   string
		}{{f.cookie, f.csrf}, {otherCookie, otherCSRF}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := f.signOutOthers(t, tc.cookie, tc.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
				results <- r.StatusCode
				r.Body.Close()
			}()
		}
		wg.Wait()
		close(results)
		for status := range results {
			if status != 204 {
				t.Fatalf("different employee status=%d", status)
			}
		}
		if selfSessionCount(t, f.app, f.id) != 1 || selfSessionCount(t, f.app, other.ID) != 1 {
			t.Fatal("cross-employee session loss")
		}
	})
}

func TestSelfSignOutOthersFinalRecheck(t *testing.T) {
	for _, scenario := range []string{"logout", "password change", "disable", "expiry"} {
		t.Run(scenario, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			other, _ := f.anotherSelfSession(t, f.id)
			entered := make(chan struct{})
			release := make(chan struct{})
			f.app.selfSignOutOthersBeforeTx = func() { close(entered); <-release }
			result := make(chan int, 1)
			go func() {
				r := f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
				result <- r.StatusCode
				r.Body.Close()
			}()
			<-entered
			switch scenario {
			case "logout":
				r := selfRequestTest(t, http.MethodDelete, f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
				if r.StatusCode != 204 {
					t.Fatalf("logout=%d", r.StatusCode)
				}
				r.Body.Close()
			case "password change":
				r := f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
				if r.StatusCode != 204 {
					t.Fatalf("password change=%d", r.StatusCode)
				}
				r.Body.Close()
			case "disable":
				r := requestJSON(t, http.MethodPatch, f.server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, f.adminCookie, f.adminCSRF, f.server.URL)
				if r.StatusCode != 200 {
					t.Fatalf("disable=%d", r.StatusCode)
				}
				r.Body.Close()
			case "expiry":
				selector := strings.SplitN(f.cookie.Value, ".", 2)[0]
				if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), selector); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if status := <-result; status != 401 {
				t.Fatalf("stale request=%d", status)
			}
			if scenario == "logout" || scenario == "expiry" {
				if status := selfSessionStatus(t, f.server.URL, other); status != 200 {
					t.Fatalf("other session erroneously removed: %d", status)
				}
			}
		})
	}
}

func TestSelfSignOutOthersStorageFaultAndUnknownCommit(t *testing.T) {
	t.Run("statement", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		other, _ := f.anotherSelfSession(t, f.id)
		if _, err := f.app.store.db.Exec(`CREATE TRIGGER fail_self_other_delete BEFORE DELETE ON employee_self_sessions BEGIN SELECT RAISE(ABORT, 'synthetic secret in SQL error'); END`); err != nil {
			t.Fatal(err)
		}
		r := f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
		body := readBody(r)
		if r.StatusCode != 503 || strings.Contains(body, "synthetic secret") || strings.Contains(body, selfOldPassword) {
			t.Fatalf("storage fault=%d %q", r.StatusCode, body)
		}
		if status := selfSessionStatus(t, f.server.URL, other); status != 200 {
			t.Fatalf("rollback lost session=%d", status)
		}
	})
	for _, uncertain := range []bool{false, true} {
		name := "commit rejected"
		if uncertain {
			name = "commit uncertain"
		}
		t.Run(name, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			other, _ := f.anotherSelfSession(t, f.id)
			f.app.selfSignOutOthersCommit = func(tx *sql.Tx) error {
				if uncertain {
					if err := tx.Commit(); err != nil {
						return err
					}
				}
				return errors.New("synthetic secret in commit error")
			}
			r := f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
			body := readBody(r)
			if r.StatusCode != 503 || strings.Contains(body, "synthetic secret") || strings.Contains(body, selfOldPassword) {
				t.Fatalf("commit fault=%d %q", r.StatusCode, body)
			}
			want := 200
			if uncertain {
				want = 401
			}
			if status := selfSessionStatus(t, f.server.URL, other); status != want {
				t.Fatalf("other status=%d want=%d", status, want)
			}
			if status := selfSessionStatus(t, f.server.URL, f.cookie); status != 200 {
				t.Fatalf("current status=%d", status)
			}
			f.app.selfSignOutOthersCommit = nil
			r = f.signOutOthers(t, f.cookie, f.csrf, selfSignOutBody(selfOldPassword), f.server.URL)
			if r.StatusCode != 204 {
				t.Fatalf("retry=%d", r.StatusCode)
			}
			r.Body.Close()
		})
	}
}
