// Independently authored tests for docs/employee-self-service-foundation-contract.md.
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openSelfTestApp(t *testing.T, dir string, enabled bool) *App {
	t.Helper()
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: enabled, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func selfRequestTest(t *testing.T, method, target, body, origin string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("X-Self-Request", "1")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func selfCreateEmployee(t *testing.T, baseURL string, cookie *http.Cookie, csrf string) employee {
	t.Helper()
	r := requestJSON(t, "POST", baseURL+"/admin/api/v1/employees", `{"name":"Alice","department":"Research","note":"private memo"}`, cookie, csrf, baseURL)
	if r.StatusCode != 201 {
		t.Fatalf("create employee: %d %s", r.StatusCode, readBody(r))
	}
	var item employee
	decodeResponse(t, r, &item)
	return item
}

func selfIssue(t *testing.T, baseURL, id string, cookie *http.Cookie, csrf string) string {
	t.Helper()
	r := requestJSON(t, "POST", baseURL+"/admin/api/v1/employees/"+id+"/self-enrollment", `{}`, cookie, csrf, baseURL)
	if r.StatusCode != 201 {
		t.Fatalf("issue: %d %s", r.StatusCode, readBody(r))
	}
	var issued struct {
		Secret string `json:"enrollment_secret"`
	}
	decodeResponse(t, r, &issued)
	if len(issued.Secret) != 43 {
		t.Fatal("missing one-time secret")
	}
	return issued.Secret
}

func selfRedeem(t *testing.T, baseURL, id, secret string) (*http.Cookie, string) {
	t.Helper()
	r := selfRequestTest(t, "POST", baseURL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(id)+`,"enrollment_secret":`+quoteJSON(secret)+`,"password":"a-long-self-password"}`, baseURL, nil, "")
	if r.StatusCode != 200 {
		t.Fatalf("redeem: %d %s", r.StatusCode, readBody(r))
	}
	var result struct {
		CSRF    string      `json:"csrf_token"`
		Profile selfProfile `json:"profile"`
	}
	decodeResponse(t, r, &result)
	if result.Profile.ID != id || result.Profile.Name != "Alice" || result.Profile.Department != "Research" || result.Profile.Status != "active" {
		t.Fatalf("wrong profile: %+v", result.Profile)
	}
	if len(r.Cookies()) != 1 {
		t.Fatal("missing self cookie")
	}
	return r.Cookies()[0], result.CSRF
}

func TestSelfServiceDefaultOffAndIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	app.cfg.WebDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(app.cfg.WebDir, "index.html"), []byte("SPA fallback"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	for _, target := range []string{"/self", "/self/", "/self/api/v1/session"} {
		r := requestJSON(t, "GET", server.URL+target, "", nil, "", "")
		if r.StatusCode != 404 {
			t.Fatalf("default-off %s: %d", target, r.StatusCode)
		}
		r.Body.Close()
	}
	r := requestJSON(t, "POST", server.URL+"/admin/api/v1/employees/"+item.ID+"/self-enrollment", `{}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 404 {
		t.Fatalf("default-off issue: %d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	active := openSelfTestApp(t, dir, true)
	active.cfg.WebDir = t.TempDir() // Exercise ServeMux registration with SPA fallback enabled.
	server = httptest.NewServer(active.Handler())
	defer server.Close()
	r = selfRequestTest(t, "GET", server.URL+"/self/api/v1/session", "", "", adminCookie, "")
	if r.StatusCode != 401 {
		t.Fatalf("admin cookie accepted as self: %d", r.StatusCode)
	}
	r.Body.Close()
	r = requestJSON(t, "POST", server.URL+"/admin/api/v1/employees/"+item.ID+"/self-enrollment", `{}`, nil, "", server.URL)
	if r.StatusCode != 401 {
		t.Fatalf("anonymous issue: %d", r.StatusCode)
	}
	r.Body.Close()
	adminCookie, adminCSRF = loginTestAdmin(t, server.URL)
	secret1 := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	secret2 := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret2)+`,"password":"a-long-self-password"}`, "", nil, "")
	if r.StatusCode != 403 {
		t.Fatalf("missing Origin accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(item.ID)+`,"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret2)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 400 {
		t.Fatalf("duplicate field accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret1)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 401 {
		t.Fatalf("old secret accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	selfCookie, selfCSRF := selfRedeem(t, server.URL, item.ID, secret2)
	if selfCookie.Path != "/self/" || !selfCookie.HttpOnly || selfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie attributes: %+v", selfCookie)
	}
	r = selfRequestTest(t, "GET", server.URL+"/self/api/v1/profile", "", "", selfCookie, "")
	if r.StatusCode != 200 {
		t.Fatalf("profile status: %d", r.StatusCode)
	}
	var profile map[string]any
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if len(profile) != 4 || profile["note"] != nil {
		t.Fatalf("profile leaked fields: %+v", profile)
	}
	r = requestJSON(t, "GET", server.URL+"/self/api/v1/profile", "", nil, "", "")
	if r.StatusCode != 401 {
		t.Fatalf("anonymous profile accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "DELETE", server.URL+"/self/api/v1/sessions", "", server.URL, selfCookie, "")
	if r.StatusCode != 403 {
		t.Fatalf("missing CSRF accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "DELETE", server.URL+"/self/api/v1/sessions", "", "http://evil.invalid", selfCookie, selfCSRF)
	if r.StatusCode != 403 {
		t.Fatalf("foreign Origin accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	r = requestJSON(t, "GET", server.URL+"/admin/api/v1/session", "", selfCookie, "", "")
	if r.StatusCode != 401 {
		t.Fatalf("self cookie accepted as admin: %d", r.StatusCode)
	}
	r.Body.Close()
	r = requestJSON(t, "PATCH", server.URL+"/admin/api/v1/employees/"+item.ID, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable: %d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	r = selfRequestTest(t, "GET", server.URL+"/self/api/v1/session", "", "", selfCookie, "")
	if r.StatusCode != 401 {
		t.Fatalf("disabled session active: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(item.ID)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 401 {
		t.Fatalf("disabled login active: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfEnrollmentConcurrentSingleUseAndRestart(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, true)
	server := httptest.NewServer(app.Handler())
	adminCookie, csrf := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, csrf)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, csrf)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", server.URL+"/self/api/v1/enroll", strings.NewReader(`{"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret)+`,"password":"a-long-self-password"}`))
			req.Header.Set("Origin", server.URL)
			req.Header.Set("X-Self-Request", "1")
			req.Header.Set("Content-Type", "application/json")
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- 0
				return
			}
			results <- r.StatusCode
			r.Body.Close()
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == 200 {
			successes++
		} else if status != 401 {
			t.Fatalf("concurrent redemption status: %d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
	server.Close()
	_ = app.Close()
	reopened := openSelfTestApp(t, dir, true)
	server = httptest.NewServer(reopened.Handler())
	defer server.Close()
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(item.ID)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 200 {
		t.Fatalf("restart login: %d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
}

func TestSelfSchemaRejectsMalformedTableWithoutOldDataLoss(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`CREATE TABLE employee_self_credentials(employee_id TEXT PRIMARY KEY,password_hash TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.close()
	if app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true}); err == nil {
		_ = app.Close()
		t.Fatal("accepted malformed self schema")
	}
	s, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old admin row lost: %d %v", count, err)
	}
}

func TestSelfExpiredEnrollmentAndSession(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, true)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	adminCookie, csrf := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, csrf)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, csrf)
	if _, err := app.store.db.Exec(`UPDATE employee_self_credentials SET enrollment_expires_at=? WHERE employee_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), item.ID); err != nil {
		t.Fatal(err)
	}
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 401 {
		t.Fatalf("expired secret accepted: %d", r.StatusCode)
	}
	r.Body.Close()
	secret = selfIssue(t, server.URL, item.ID, adminCookie, csrf)
	cookie, _ := selfRedeem(t, server.URL, item.ID, secret)
	if _, err := app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	r = selfRequestTest(t, "GET", server.URL+"/self/api/v1/session", "", "", cookie, "")
	if r.StatusCode != 401 {
		t.Fatalf("expired session accepted: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfTimeOrderingAcrossSecondPrecision(t *testing.T) {
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	if !(selfTime(base) < selfTime(base.Add(100*time.Millisecond))) || !(selfTime(base.Add(100*time.Millisecond)) < selfTime(base.Add(time.Second))) {
		t.Fatal("self-service timestamps must sort chronologically across second and fraction boundaries")
	}
}

func TestSelfRedemptionPersistenceFailureIsRetryable(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, true)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	adminCookie, csrf := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, csrf)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, csrf)
	if _, err := app.store.db.Exec(`CREATE TRIGGER self_fail_session BEFORE INSERT ON employee_self_sessions BEGIN SELECT RAISE(ABORT, 'injected persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(item.ID)+`,"enrollment_secret":`+quoteJSON(secret)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 503 || len(r.Cookies()) != 0 {
		t.Fatalf("persistence failure emitted success material: status=%d cookies=%d", r.StatusCode, len(r.Cookies()))
	}
	r.Body.Close()
	if _, err := app.store.db.Exec(`DROP TRIGGER self_fail_session`); err != nil {
		t.Fatal(err)
	}
	_, _ = selfRedeem(t, server.URL, item.ID, secret)
}

func TestSelfLoginFailureIsUniformAndRateLimited(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, true)
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	adminCookie, csrf := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, csrf)
	var first string
	for index, id := range []string{"unknown", item.ID, "unknown", item.ID, "unknown"} {
		r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(id)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
		if r.StatusCode != 401 {
			t.Fatalf("failure %d: %d", index, r.StatusCode)
		}
		body := readBody(r)
		if index == 0 {
			first = body
		} else if body != first {
			t.Fatalf("failure text differs for unknown/unregistered employee: %q versus %q", first, body)
		}
	}
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":"another","password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 429 {
		t.Fatalf("peer rate limit bypassed: %d", r.StatusCode)
	}
	r.Body.Close()
}
