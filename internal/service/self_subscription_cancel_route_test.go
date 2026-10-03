package service

// Independently authored acceptance tests for
// docs/employee-self-subscription-cancel-route-boundary-contract.md.

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
)

func TestSelfCancelRouteClassification(t *testing.T) {
	cases := []struct {
		path      string
		shaped    bool
		canonical bool
	}{
		{"/self/api/v1/billing/subscriptions/subscription_0123456789abcdef0123456789abcdef/cancel", true, true},
		{"/self/api/v1/billing/subscriptions//cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/extra/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/cancel/extra", true, false},
		{"/self/api/v1/billing/subscriptions/id%2Fextra/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id%2fextra/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id%252Fextra/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id%5Cextra/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/./id/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/../cancel", true, false},
		{"/self//api/v1/billing/subscriptions/id/cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/%63ancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/%2563ancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/renew/../cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/renewal-links/../cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/purchase-snapshot/../cancel", true, false},
		{"/self/api/v1/billing/subscriptions/id/renew/../cancel/extra", true, false},
		{"/self/api/v1/billing/subscriptions/id/cancelled", false, false},
		{"/self/api/v1/billing/subscriptions/cancel/renew", false, false},
		{"/self/api/v1/billing/subscriptions/id/renewal-quotes", false, false},
		{"/self/api/v1/billing/subscriptions/id/renewal-links", false, false},
		{"/self/api/v1/billing/subscriptions/id/one-shot-renewal", false, false},
		{"/self/api/v1/billing/subscriptions/id/purchase-snapshot", false, false},
		{"/self/api/v1/billing/subscriptions/id%2Fcancel/renewal-links", false, false},
		{"/self/api/v1/billing/subscriptions/id%252Fcancel/renewal-links", false, false},
		{"/self/api/v1/billing/subscriptions/id%2Frenewal-links/cancel", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if got := selfCancelShapedPath(r); got != tc.shaped {
				t.Fatalf("shaped=%v want=%v escaped=%q path=%q", got, tc.shaped, r.URL.EscapedPath(), r.URL.Path)
			}
			if got := selfCancelCanonicalPath(r); got != tc.canonical {
				t.Fatalf("canonical=%v want=%v escaped=%q path=%q", got, tc.canonical, r.URL.EscapedPath(), r.URL.Path)
			}
		})
	}
}

func newSelfCancelRouteFixture(t *testing.T, enabled bool) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html><body>SPA fallback</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test", WebDir: webDir,
		EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfSubscriptionCancelEnabled: enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	f := selfWalletFixture{app: app, server: server, dir: dir}
	if enabled {
		f.adminCookie, f.adminCSRF = loginTestAdmin(t, server.URL)
		employee := selfCreateEmployee(t, server.URL, f.adminCookie, f.adminCSRF)
		f.id = employee.ID
		secret := selfIssue(t, server.URL, employee.ID, f.adminCookie, f.adminCSRF)
		f.cookie, f.csrf = selfRedeem(t, server.URL, employee.ID, secret)
	}
	return f
}

func selfCancelRouteHTTP(t *testing.T, f selfWalletFixture, method, path, origin, selfHeader, csrf string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if selfHeader != "" {
		req.Header.Set("X-Self-Request", selfHeader)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func checkSelfCancelRouteResponse(t *testing.T, resp *http.Response, wantStatus int, wantCode string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Location") != "" {
		t.Fatalf("status=%d cache=%q location=%q want=%d", resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Location"), wantStatus)
	}
	if resp.StatusCode == 301 || resp.StatusCode == 307 || resp.StatusCode == 308 {
		t.Fatal("redirect from cancellation route")
	}
	if wantCode == "" {
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body["error"].(map[string]any)["code"] != wantCode {
		t.Fatalf("body=%v decode=%v want error=%q", body, err, wantCode)
	}
}

func TestSelfCancelRouteDisabledIsUnobservable(t *testing.T) {
	f := newSelfCancelRouteFixture(t, false)
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions/id/cancel",
		"/self/api/v1/billing/subscriptions//cancel",
		"/self/api/v1/billing/subscriptions/id/cancel/extra",
		"/self/api/v1/billing/subscriptions/id/extra/cancel",
		"/self/api/v1/billing/subscriptions/id%2Fextra/cancel",
		"/self/api/v1/billing/subscriptions/id%2fextra/cancel",
		"/self/api/v1/billing/subscriptions/id%252Fextra/cancel",
		"/self/api/v1/billing/subscriptions/./id/cancel",
		"/self/api/v1/billing/subscriptions/id/../cancel",
		"/self//api/v1/billing/subscriptions/id/cancel",
		"/self/api/v1/billing/subscriptions/id/%63ancel",
		"/self/api/v1/billing/subscriptions/id/%2563ancel",
		"/self/api/v1/billing/subscriptions/id/renew/../cancel",
		"/self/api/v1/billing/subscriptions/id/renewal-links/../cancel",
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot/../cancel",
		"/self/api/v1/billing/subscriptions/id/renew/../cancel/extra",
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodHead, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				resp := selfCancelRouteHTTP(t, f, method, path, "", "", "", nil)
				if resp.Header.Get("Allow") != "" {
					t.Fatalf("disabled route revealed Allow=%q", resp.Header.Get("Allow"))
				}
				checkSelfCancelRouteResponse(t, resp, 404, "")
			})
		}
	}
}

func TestSelfCancelRouteEnabledAuthMethodAndSiblingIsolation(t *testing.T) {
	f := newSelfCancelRouteFixture(t, true)
	canonical := "/self/api/v1/billing/subscriptions/subscription_0123456789abcdef0123456789abcdef/cancel"
	malformed := "/self/api/v1/billing/subscriptions/id%2Fextra/cancel"
	var beforeOperations int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations`).Scan(&beforeOperations); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		malformed,
		"/self/api/v1/billing/subscriptions//cancel",
		"/self/api/v1/billing/subscriptions/id/cancel/extra",
		"/self/api/v1/billing/subscriptions/id/extra/cancel",
		"/self/api/v1/billing/subscriptions/id%2fextra/cancel",
		"/self/api/v1/billing/subscriptions/id%252Fextra/cancel",
		"/self/api/v1/billing/subscriptions/id%5Cextra/cancel",
		"/self/api/v1/billing/subscriptions/./id/cancel",
		"/self/api/v1/billing/subscriptions/id/../cancel",
		"/self//api/v1/billing/subscriptions/id/cancel",
		"/self/api/v1/billing/subscriptions/id/%63ancel",
		"/self/api/v1/billing/subscriptions/id/%2563ancel",
		"/self/api/v1/billing/subscriptions/id/renew/../cancel",
		"/self/api/v1/billing/subscriptions/id/renewal-links/../cancel",
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot/../cancel",
		"/self/api/v1/billing/subscriptions/id/renew/../cancel/extra",
	} {
		t.Run("malformed "+path, func(t *testing.T) {
			resp := selfCancelRouteHTTP(t, f, http.MethodPost, path, f.server.URL, "1", f.csrf, f.cookie)
			checkSelfCancelRouteResponse(t, resp, 400, "invalid_request")
			f.app.clearSelfFailures("127.0.0.1", f.id)
		})
	}
	for _, tc := range []struct {
		name, origin, selfHeader, csrf string
		cookie                         *http.Cookie
		want                           int
		code                           string
	}{
		{"anonymous", f.server.URL, "1", f.csrf, nil, 401, "authentication_required"},
		{"admin", f.server.URL, "1", f.csrf, f.adminCookie, 401, "authentication_required"},
		{"wrong origin", "https://other.example", "1", f.csrf, f.cookie, 403, "request_rejected"},
		{"missing self header", f.server.URL, "", f.csrf, f.cookie, 403, "request_rejected"},
		{"wrong self header", f.server.URL, "2", f.csrf, f.cookie, 403, "request_rejected"},
		{"wrong csrf", f.server.URL, "1", "wrong", f.cookie, 403, "csrf_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := selfCancelRouteHTTP(t, f, http.MethodPost, malformed, tc.origin, tc.selfHeader, tc.csrf, tc.cookie)
			checkSelfCancelRouteResponse(t, resp, tc.want, tc.code)
		})
	}
	t.Run("duplicate origin", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, f.server.URL+malformed, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Add("Origin", f.server.URL)
		req.Header.Add("Origin", f.server.URL)
		req.Header.Set("X-Self-Request", "1")
		req.Header.Set("X-CSRF-Token", f.csrf)
		req.AddCookie(f.cookie)
		resp, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		checkSelfCancelRouteResponse(t, resp, 403, "request_rejected")
	})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
		t.Run("canonical "+method, func(t *testing.T) {
			resp := selfCancelRouteHTTP(t, f, method, canonical, "", "", "", f.cookie)
			if resp.Header.Get("Allow") != http.MethodPost {
				t.Fatalf("Allow=%q", resp.Header.Get("Allow"))
			}
			if method == http.MethodHead {
				checkSelfCancelRouteResponse(t, resp, 405, "")
			} else {
				checkSelfCancelRouteResponse(t, resp, 405, "method_not_allowed")
			}
		})
	}
	checkSelfCancelRouteResponse(t, selfCancelRouteHTTP(t, f, http.MethodGet, canonical, "", "", "", nil), 401, "authentication_required")
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions/id/renew",
		"/self/api/v1/billing/subscriptions/id/renewal-quotes",
		"/self/api/v1/billing/subscriptions/id/renewal-links",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal",
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/cancelled",
		"/self/api/v1/billing/subscriptions/id%2Fcancel/renewal-links",
	} {
		t.Run("sibling "+path, func(t *testing.T) {
			resp := selfCancelRouteHTTP(t, f, http.MethodPost, path, f.server.URL, "1", f.csrf, f.cookie)
			checkSelfCancelRouteResponse(t, resp, 404, "")
		})
	}
	var afterOperations int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations`).Scan(&afterOperations); err != nil {
		t.Fatal(err)
	}
	if afterOperations != beforeOperations {
		t.Fatalf("route rejection changed commercial operations: before=%d after=%d", beforeOperations, afterOperations)
	}
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "subscription_0123456789abcdef0123456789abcdef", selfPurchaseOwner(f.id), "one_time", "active", false)
	body := selfCancelBody("cancel-route-canonical", 1, selfPurchaseTestPassword)
	first := readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+canonical, body, f.server.URL, f.cookie, f.csrf), 200)
	if first["status"] != "cancelled" || first["replay"] != false || first["subscription_id"] != "subscription_0123456789abcdef0123456789abcdef" {
		t.Fatalf("canonical cancel=%+v", first)
	}
	replay := readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+canonical, body, f.server.URL, f.cookie, f.csrf), 200)
	if replay["replay"] != true || replay["cancelled_at"] != first["cancelled_at"] {
		t.Fatalf("canonical replay=%+v", replay)
	}
}
