package service

// Independently authored for docs/employee-self-one-shot-route-boundary-contract.md.
// These HTTP requests disable redirect following so ServeMux's original result
// remains observable before and after the route-boundary repair.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSelfOneShotRouteFixture(t *testing.T, enabled bool) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html><body>self route test</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", WebDir: webDir, Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfOneShotRenewalDisarmEnabled: enabled,
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

func selfOneShotRouteHTTP(t *testing.T, f selfWalletFixture, method, path string, cookie *http.Cookie) *http.Response {
	return selfOneShotRouteHTTPWith(t, f, method, path, cookie, "", "", "", "")
}

func selfOneShotRouteHTTPWith(t *testing.T, f selfWalletFixture, method, path string, cookie *http.Cookie, origin, selfHeader, csrf, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
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

func checkSelfOneShotRouteResponse(t *testing.T, resp *http.Response, status int, code, allow string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != status || resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("Location") != "" || resp.Header.Get("Allow") != allow {
		t.Fatalf("status=%d cache=%q location=%q allow=%q want status=%d allow=%q", resp.StatusCode,
			resp.Header.Get("Cache-Control"), resp.Header.Get("Location"), resp.Header.Get("Allow"), status, allow)
	}
	if code == "" || resp.Request.Method == http.MethodHead {
		_, _ = io.Copy(io.Discard, resp.Body)
		return
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error.Code != code {
		t.Fatalf("error=%q decode=%v want=%q", body.Error.Code, err, code)
	}
}

func TestSelfOneShotRouteClassification(t *testing.T) {
	const prefix = "/self/api/v1/billing/subscriptions/"
	cases := []struct {
		path      string
		kind      selfOneShotRouteKind
		canonical bool
	}{
		{prefix + "id/one-shot-renewal", selfOneShotRouteStatus, true},
		{prefix + "id/one-shot-renewal/disarm", selfOneShotRouteDisarm, true},
		{prefix + "id%20space/one-shot-renewal", selfOneShotRouteStatus, true},
		{prefix + "/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "one-shot-renewal/disarm", selfOneShotRouteDisarm, false},
		{prefix + "id/extra/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/one-shot-renewal/extra", selfOneShotRouteStatus, false},
		{prefix + "id/one-shot-renewal/disarm/extra", selfOneShotRouteDisarm, false},
		{prefix + "id/one-shot-renewal/extra/disarm", selfOneShotRouteDisarm, false},
		{prefix + "id%2Fextra/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id%2fextra/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id%252Fextra/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id%5Cextra/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "./id/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/../id/one-shot-renewal", selfOneShotRouteStatus, false},
		{"/self//api/v1/billing/subscriptions/id/one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/%6Fne-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/%256Fne-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/one-shot-renewal/%64isarm", selfOneShotRouteDisarm, false},
		{prefix + "id/one-shot-renewal%2Fdisarm", selfOneShotRouteDisarm, false},
		{prefix + "id/renew/../one-shot-renewal", selfOneShotRouteStatus, false},
		{prefix + "id/one-shot-renewals", selfOneShotRouteNone, false},
		{prefix + "id/one-shot-renewal-other", selfOneShotRouteNone, false},
		{prefix + "one-shot-renewal/other", selfOneShotRouteNone, false},
		{prefix + "id/renew", selfOneShotRouteNone, false},
		{prefix + "id/renewal-quotes", selfOneShotRouteNone, false},
		{prefix + "id/renewal-links", selfOneShotRouteNone, false},
		{prefix + "id/purchase-snapshot", selfOneShotRouteNone, false},
		{prefix + "id/cancel", selfOneShotRouteNone, false},
		{prefix + "id/renewal-links/one-shot-renewal", selfOneShotRouteNone, false},
		{prefix + "id%2Fone-shot-renewal/renewal-links", selfOneShotRouteNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			kind := selfOneShotShapedPath(r)
			if kind != tc.kind || selfOneShotCanonicalRoute(r, kind) != tc.canonical {
				t.Fatalf("kind=%d canonical=%v want kind=%d canonical=%v escaped=%q", kind, selfOneShotCanonicalRoute(r, kind), tc.kind, tc.canonical, r.URL.EscapedPath())
			}
		})
	}
}

func TestSelfOneShotRouteNoFollowBaseline(t *testing.T) {
	const canonical = "/self/api/v1/billing/subscriptions/id/one-shot-renewal"
	const malformed = "/self/api/v1/billing/subscriptions/id/../id/one-shot-renewal"
	t.Run("disabled malformed", func(t *testing.T) {
		f := newSelfOneShotRouteFixture(t, false)
		resp := selfOneShotRouteHTTP(t, f, http.MethodGet, malformed, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Location") != "" || resp.Header.Get("Allow") != "" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d location=%q allow=%q cache=%q", resp.StatusCode, resp.Header.Get("Location"), resp.Header.Get("Allow"), resp.Header.Get("Cache-Control"))
		}
	})
	t.Run("enabled head", func(t *testing.T) {
		f := newSelfOneShotRouteFixture(t, true)
		resp := selfOneShotRouteHTTP(t, f, http.MethodHead, canonical, f.cookie)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != http.MethodGet || resp.Header.Get("Location") != "" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d location=%q allow=%q cache=%q", resp.StatusCode, resp.Header.Get("Location"), resp.Header.Get("Allow"), resp.Header.Get("Cache-Control"))
		}
	})
	t.Run("enabled malformed", func(t *testing.T) {
		f := newSelfOneShotRouteFixture(t, true)
		resp := selfOneShotRouteHTTP(t, f, http.MethodGet, malformed, f.cookie)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" || resp.Header.Get("Allow") != "" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d location=%q allow=%q cache=%q", resp.StatusCode, resp.Header.Get("Location"), resp.Header.Get("Allow"), resp.Header.Get("Cache-Control"))
		}
	})
}

func TestSelfOneShotRouteDisabledBeforeMuxAndIdentity(t *testing.T) {
	f := newSelfOneShotRouteFixture(t, false)
	const prefix = "/self/api/v1/billing/subscriptions/"
	paths := []string{
		prefix + "id/one-shot-renewal",
		prefix + "id/one-shot-renewal/disarm",
		prefix + "/one-shot-renewal",
		prefix + "id/one-shot-renewal/extra",
		prefix + "id/one-shot-renewal/disarm/extra",
		prefix + "id%2Fextra/one-shot-renewal",
		prefix + "id%252Fextra/one-shot-renewal",
		prefix + "id%5Cextra/one-shot-renewal",
		prefix + "id/../id/one-shot-renewal",
		"/self//api/v1/billing/subscriptions/id/one-shot-renewal",
		prefix + "id/%6Fne-shot-renewal",
		prefix + "id/%256Fne-shot-renewal",
		prefix + "id/one-shot-renewal/%64isarm",
		prefix + "id/renew/../one-shot-renewal",
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, method, path, nil), http.StatusNotFound, "", "")
			})
		}
	}
}

func TestSelfOneShotRouteEnabledMethodAuthAndMalformedPriority(t *testing.T) {
	f := newSelfOneShotRouteFixture(t, true)
	const statusPath = "/self/api/v1/billing/subscriptions/id/one-shot-renewal"
	const disarmPath = statusPath + "/disarm"
	const malformedStatus = "/self/api/v1/billing/subscriptions/id/../id/one-shot-renewal"
	const malformedDisarm = "/self/api/v1/billing/subscriptions/id%2Fextra/one-shot-renewal/disarm"
	for _, tc := range []struct {
		path, allowed string
		methods       []string
	}{
		{statusPath, http.MethodGet, []string{http.MethodHead, http.MethodPost, http.MethodDelete}},
		{disarmPath, http.MethodPost, []string{http.MethodGet, http.MethodHead, http.MethodDelete}},
	} {
		for _, method := range tc.methods {
			t.Run("canonical "+method+" "+tc.path, func(t *testing.T) {
				checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, method, tc.path, f.cookie), http.StatusMethodNotAllowed, "method_not_allowed", tc.allowed)
			})
		}
		checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodDelete, tc.path, nil), http.StatusUnauthorized, "authentication_required", "")
		checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodDelete, tc.path, f.adminCookie), http.StatusUnauthorized, "authentication_required", "")
		checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTPWith(t, f, http.MethodDelete, tc.path, f.cookie, "https://other.example", "", "", ""), http.StatusForbidden, "request_rejected", "")
	}
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, malformedStatus},
		{http.MethodHead, malformedStatus},
		{http.MethodPost, malformedStatus},
		{http.MethodGet, malformedDisarm},
		{http.MethodHead, malformedDisarm},
	} {
		t.Run("malformed "+tc.method+" "+tc.path, func(t *testing.T) {
			checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, tc.method, tc.path, f.cookie), http.StatusBadRequest, "invalid_request", "")
		})
	}
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions//one-shot-renewal",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal/extra",
		"/self/api/v1/billing/subscriptions/id%2Fextra/one-shot-renewal",
		"/self/api/v1/billing/subscriptions/id%252Fextra/one-shot-renewal",
		"/self/api/v1/billing/subscriptions/id%5Cextra/one-shot-renewal",
		"/self//api/v1/billing/subscriptions/id/one-shot-renewal",
		"/self/api/v1/billing/subscriptions/id/%6Fne-shot-renewal",
		"/self/api/v1/billing/subscriptions/id/%256Fne-shot-renewal",
		"/self/api/v1/billing/subscriptions/id/renew/../one-shot-renewal",
	} {
		t.Run("status shape "+path, func(t *testing.T) {
			checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodGet, path, f.cookie), http.StatusBadRequest, "invalid_request", "")
		})
	}
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions//one-shot-renewal/disarm",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal/disarm/extra",
		"/self/api/v1/billing/subscriptions/id%2Fextra/one-shot-renewal/disarm",
		"/self/api/v1/billing/subscriptions/id%252Fextra/one-shot-renewal/disarm",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal/%64isarm",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal%2Fdisarm",
		"/self/api/v1/billing/subscriptions/id/../id/one-shot-renewal/disarm",
	} {
		t.Run("disarm shape "+path, func(t *testing.T) {
			resp := selfOneShotRouteHTTPWith(t, f, http.MethodPost, path, f.cookie, f.server.URL, "1", f.csrf, "")
			checkSelfOneShotRouteResponse(t, resp, http.StatusBadRequest, "invalid_request", "")
			f.app.clearSelfFailures("127.0.0.1", f.id)
		})
	}
	checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodGet, malformedStatus, nil), http.StatusUnauthorized, "authentication_required", "")
	checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodGet, malformedStatus, f.adminCookie), http.StatusUnauthorized, "authentication_required", "")
	checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTPWith(t, f, http.MethodGet, malformedStatus, f.cookie, "https://other.example", "", "", ""), http.StatusForbidden, "request_rejected", "")
	checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTPWith(t, f, http.MethodPost, malformedDisarm, f.cookie, f.server.URL, "1", f.csrf, ""), http.StatusBadRequest, "invalid_request", "")
	f.app.clearSelfFailures("127.0.0.1", f.id)
	for _, tc := range []struct {
		name, origin, selfHeader, csrf string
		cookie                         *http.Cookie
		status                         int
		code                           string
	}{
		{"anonymous", f.server.URL, "1", f.csrf, nil, 401, "authentication_required"},
		{"admin", f.server.URL, "1", f.csrf, f.adminCookie, 401, "authentication_required"},
		{"bad origin", "https://other.example", "1", f.csrf, f.cookie, 403, "request_rejected"},
		{"missing self header", f.server.URL, "", f.csrf, f.cookie, 403, "request_rejected"},
		{"wrong self header", f.server.URL, "2", f.csrf, f.cookie, 403, "request_rejected"},
		{"bad csrf", f.server.URL, "1", "wrong", f.cookie, 403, "csrf_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := selfOneShotRouteHTTPWith(t, f, http.MethodPost, malformedDisarm, tc.cookie, tc.origin, tc.selfHeader, tc.csrf, "")
			checkSelfOneShotRouteResponse(t, resp, tc.status, tc.code, "")
		})
	}
	t.Run("duplicate origin", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, f.server.URL+malformedDisarm, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Add("Origin", f.server.URL)
		req.Header.Add("Origin", f.server.URL)
		req.Header.Set("X-Self-Request", "1")
		req.Header.Set("X-CSRF-Token", f.csrf)
		req.AddCookie(f.cookie)
		client := f.server.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		checkSelfOneShotRouteResponse(t, resp, http.StatusForbidden, "request_rejected", "")
	})
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions/id/renew",
		"/self/api/v1/billing/subscriptions/id/renewal-quotes",
		"/self/api/v1/billing/subscriptions/id/renewal-links",
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/cancel",
		"/self/api/v1/billing/subscriptions/id%2Fone-shot-renewal/renewal-links",
	} {
		t.Run("sibling "+path, func(t *testing.T) {
			checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, http.MethodGet, path, f.cookie), http.StatusNotFound, "", "")
		})
	}
}

func TestSelfOneShotRouteRejectsBeforeBusinessHandler(t *testing.T) {
	f := newSelfOneShotRouteFixture(t, true)
	var delegated atomic.Int32
	probe := httptest.NewServer(requestMiddleware(f.app.selfOneShotRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))))
	defer probe.Close()
	client := probe.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, tc := range []struct {
		method, path, origin, selfHeader, csrf string
		status                                 int
	}{
		{http.MethodHead, "/self/api/v1/billing/subscriptions/id/one-shot-renewal", "", "", "", 405},
		{http.MethodDelete, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/disarm", "", "", "", 405},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/../id/one-shot-renewal", "", "", "", 400},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id%2Fextra/one-shot-renewal/disarm", probe.URL, "1", f.csrf, 400},
	} {
		req, err := http.NewRequest(tc.method, probe.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(f.cookie)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.selfHeader != "" {
			req.Header.Set("X-Self-Request", tc.selfHeader)
		}
		if tc.csrf != "" {
			req.Header.Set("X-CSRF-Token", tc.csrf)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.status || delegated.Load() != 0 {
			t.Fatalf("method=%s path=%s status=%d delegated=%d", tc.method, tc.path, resp.StatusCode, delegated.Load())
		}
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/one-shot-renewal"},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/disarm"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		f.app.selfOneShotRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			delegated.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(httptest.NewRecorder(), req)
	}
	if delegated.Load() != 2 {
		t.Fatalf("canonical delegation=%d want=2", delegated.Load())
	}
}

func TestSelfOneShotRouteDoesNotTreatModelKeyOrExpiredSessionAsSelfIdentity(t *testing.T) {
	f := newSelfOneShotRouteFixture(t, true)
	key := createTestKey(t, f.server.URL, f.id, "self-one-shot-route-role", f.adminCookie, f.adminCSRF)
	for _, route := range []struct {
		method, path string
	}{
		{http.MethodHead, "/self/api/v1/billing/subscriptions/id/one-shot-renewal"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/%64isarm"},
	} {
		req, err := http.NewRequest(route.method, f.server.URL+route.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key.Key)
		resp, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		checkSelfOneShotRouteResponse(t, resp, http.StatusUnauthorized, "authentication_required", "")
	}
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		method, path string
	}{
		{http.MethodHead, "/self/api/v1/billing/subscriptions/id/one-shot-renewal"},
		{http.MethodDelete, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/disarm"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/%64isarm"},
	} {
		checkSelfOneShotRouteResponse(t, selfOneShotRouteHTTP(t, f, route.method, route.path, f.cookie),
			http.StatusUnauthorized, "authentication_required", "")
	}
}
