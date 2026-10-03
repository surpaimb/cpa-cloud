// Independently authored tests for docs/employee-self-upstream-estimated-cost-summary-contract.md.
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newEstimatedCostFixture(t *testing.T) selfPasswordFixture {
	return newEstimatedCostFixtureEnabled(t, true)
}

func newEstimatedCostFixtureEnabled(t *testing.T, enabled bool) selfPasswordFixture {
	t.Helper()
	dir := t.TempDir()
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>SPA fallback marker</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", WebDir: webDir, EmployeeSelfServiceEnabled: true, EmployeeSelfUpstreamEstimatedCostSummaryEnabled: enabled, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	cookie, csrf := selfRedeem(t, server.URL, item.ID, secret)
	return selfPasswordFixture{app: app, server: server, id: item.ID, adminCookie: adminCookie, adminCSRF: adminCSRF, cookie: cookie, csrf: csrf, dir: dir}
}

func estimatedCostGET(t *testing.T, f selfPasswordFixture, suffix string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, f.server.URL+selfEstimatedCostPath+suffix, "", "", cookie, "")
}

func TestSelfEstimatedCostDefaultOffRolesAndStrictRead(t *testing.T) {
	old := newEstimatedCostFixtureEnabled(t, false)
	response := estimatedCostGET(t, old, "", old.cookie)
	if response.StatusCode != 404 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("default-off status=%d headers=%v", response.StatusCode, response.Header)
	}
	response.Body.Close()
	f := newEstimatedCostFixture(t)
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		status int
	}{{"anonymous", nil, 401}, {"administrator", f.adminCookie, 401}, {"employee", f.cookie, 200}} {
		response = estimatedCostGET(t, f, "", tc.cookie)
		if response.StatusCode != tc.status || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d headers=%v", tc.name, response.StatusCode, response.Header)
		}
		if tc.status == 200 {
			var raw map[string]json.RawMessage
			decodeResponse(t, response, &raw)
			if len(raw) != 4 || raw["costs"] == nil || !strings.Contains(string(raw["costs"]), "[]") || !strings.Contains(string(raw["attempts"]), `"total":"0"`) {
				t.Fatalf("empty summary=%v", raw)
			}
		} else {
			response.Body.Close()
		}
	}
	for _, suffix := range []string{"?", "?currency=USD", "?employee_id=other", "?from=2026-01-01T00:00:00Z"} {
		response = estimatedCostGET(t, f, suffix, f.cookie)
		if response.StatusCode != 400 {
			t.Fatalf("query %q status=%d", suffix, response.StatusCode)
		}
		response.Body.Close()
	}
	response = selfRequestTest(t, http.MethodGet, f.server.URL+selfEstimatedCostPath, "{}", "", f.cookie, "")
	if response.StatusCode != 400 {
		t.Fatalf("GET body status=%d", response.StatusCode)
	}
	response.Body.Close()
	response = selfRequestTest(t, http.MethodPost, f.server.URL+selfEstimatedCostPath, "", "", f.cookie, "")
	if response.StatusCode != 405 || response.Header.Get("Allow") != "GET" {
		t.Fatalf("method status=%d Allow=%q", response.StatusCode, response.Header.Get("Allow"))
	}
	response.Body.Close()
	request, err := http.NewRequest(http.MethodGet, f.server.URL+selfEstimatedCostPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "http://other.invalid")
	request.AddCookie(f.cookie)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 403 {
		t.Fatalf("origin status=%d", response.StatusCode)
	}
	response.Body.Close()
	for _, suffix := range []string{"/", "/extra", "%2f", "/../estimated-cost-summary", "\\"} {
		request = httptest.NewRequest(http.MethodGet, selfEstimatedCostPath+suffix, nil)
		request.AddCookie(f.cookie)
		recorder := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(recorder, request)
		if recorder.Code == 200 || recorder.Code == 301 || recorder.Code == 307 || recorder.Code == 308 {
			t.Fatalf("shaped path %q status=%d", suffix, recorder.Code)
		}
	}
}

func TestSelfEstimatedCostEncodedSuffixCannotFallBackToWebDir(t *testing.T) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, enabled := range []bool{false, true} {
		f := newEstimatedCostFixtureEnabled(t, enabled)
		for _, suffix := range []string{"%3Fx", "%3Bextra", "%2Eextra"} {
			request, err := http.NewRequest(http.MethodGet, f.server.URL+selfEstimatedCostPath+suffix, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(f.cookie)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			want := http.StatusBadRequest
			if !enabled {
				want = http.StatusNotFound
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Location") != "" || strings.Contains(string(body), "SPA fallback marker") {
				t.Fatalf("enabled=%v suffix=%q status=%d headers=%v body=%s", enabled, suffix, response.StatusCode, response.Header, body)
			}
			if enabled && !strings.Contains(string(body), `"code":"invalid_request"`) {
				t.Fatalf("enabled suffix %q missing fixed JSON error: %s", suffix, body)
			}
		}
	}
}

func TestSelfEstimatedCostLegacyUnknownNoLeakStorageFailureAndSessionRecheck(t *testing.T) {
	f := newEstimatedCostFixture(t)
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertTokenRequest(t, f, "legacy-cost-request", f.id, at, "succeeded")
	insertTokenAttempt(t, f, "legacy-cost-attempt", "legacy-cost-request", at, "succeeded", int64(100), int64(0), int64(0), int64(0))
	response := estimatedCostGET(t, f, "", f.cookie)
	if response.StatusCode != 200 {
		t.Fatalf("legacy status=%d body=%s", response.StatusCode, readBody(response))
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var result selfEstimatedCostResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Attempts.Total != "1" || result.Attempts.Terminal != "1" || len(result.Costs) != 1 || result.Costs[0].Currency != nil || result.Costs[0].KnownMicro != "0" || result.Costs[0].UnknownCount != "1" {
		t.Fatalf("legacy summary=%+v", result)
	}
	for _, secret := range []string{"legacy-cost-request", "legacy-cost-attempt", "account_id", "provider", "input_tokens"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
	// Release the initial admission lock while a read is in flight. Logout
	// must win the final session check and prevent the stale response.
	entered := make(chan struct{})
	release := make(chan struct{})
	f.app.selfEstimatedCostBeforeFinal = func() { close(entered); <-release }
	finished := make(chan *http.Response, 1)
	go func() { finished <- estimatedCostGET(t, f, "", f.cookie) }()
	<-entered
	logout := selfRequestTest(t, http.MethodDelete, f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
	if logout.StatusCode != 204 {
		t.Fatalf("logout status=%d", logout.StatusCode)
	}
	logout.Body.Close()
	close(release)
	response = <-finished
	if response.StatusCode != 401 {
		t.Fatalf("stale session status=%d", response.StatusCode)
	}
	response.Body.Close()
	f.app.selfEstimatedCostBeforeFinal = nil
	// A new valid session sees one fixed storage failure with no partial sum.
	login := f.login(t, selfOldPassword)
	if login.StatusCode != 200 {
		t.Fatalf("login status=%d", login.StatusCode)
	}
	cookie := login.Cookies()[0]
	login.Body.Close()
	if _, err := f.app.store.db.Exec(`DROP INDEX accounting_usage_corrections_attempt_idx`); err != nil {
		t.Fatal(err)
	}
	response = estimatedCostGET(t, f, "", cookie)
	if response.StatusCode != 503 {
		t.Fatalf("storage status=%d", response.StatusCode)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(body), "known_estimated_cost_micro") || strings.Contains(string(body), "legacy-cost") {
		t.Fatalf("partial storage response=%s", body)
	}
}
