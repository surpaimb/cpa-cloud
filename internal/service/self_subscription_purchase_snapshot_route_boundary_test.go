package service

// Independently authored acceptance tests for
// docs/employee-self-subscription-purchase-snapshot-route-boundary-contract.md.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelfPurchaseSnapshotRouteBoundaryShapeAndSiblingClassification(t *testing.T) {
	const prefix = "/self/api/v1/billing/subscriptions/"
	cases := []struct {
		path      string
		shaped    bool
		canonical bool
	}{
		{prefix + "id/purchase-snapshot", true, true},
		{prefix + "id%20part/purchase-snapshot", true, true},
		{prefix + "/purchase-snapshot", true, false},
		{prefix + "id/purchase-snapshot/extra", true, false},
		{prefix + "id%2Fextra/purchase-snapshot", true, false},
		{prefix + "id%252Fextra/purchase-snapshot", true, false},
		{prefix + "id%5Cextra/purchase-snapshot", true, false},
		{prefix + "id%255Cextra/purchase-snapshot", true, false},
		{prefix + "./id/purchase-snapshot", true, false},
		{prefix + "id/../purchase-snapshot", true, false},
		{prefix + "id/../id/purchase-snapshot", true, false},
		{"/self//api/v1/billing/subscriptions/id/purchase-snapshot", true, false},
		{"/self/api/./v1/billing/subscriptions/id/purchase-snapshot", true, false},
		{prefix + "id/%70urchase-snapshot", true, false},
		{prefix + "id/%2570urchase-snapshot", true, false},
		{prefix + "id%2Fpurchase-snapshot", true, false},
		{prefix + "id%252Fpurchase-snapshot", true, false},
		{prefix + "id/renew/../purchase-snapshot", true, false},
		{prefix + "id/purchase-snapshot/../cancel", false, false},
		{prefix + "id/purchase-snapshot/../renewal-links", false, false},
		{prefix + "id/purchase-snapshots", false, false},
		{prefix + "id/unknown/purchase-snapshot", false, false},
		{prefix + "id/renew", false, false},
		{prefix + "id/renewal-quotes", false, false},
		{prefix + "id/renewal-links", false, false},
		{prefix + "id/cancel", false, false},
		{prefix + "id/one-shot-renewal", false, false},
		{prefix + "id/renewal-links/purchase-snapshot", false, false},
		{prefix + "id%2Fpurchase-snapshot/renewal-links", false, false},
		{prefix + "id%252Fpurchase-snapshot/renewal-links", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if got := snapshotShapedRawPath(r); got != tc.shaped {
				t.Fatalf("shaped=%v want=%v escaped=%q", got, tc.shaped, r.URL.EscapedPath())
			}
			if got := !malformedPurchaseSnapshotRawPath(r); got != tc.canonical {
				t.Fatalf("canonical=%v want=%v escaped=%q", got, tc.canonical, r.URL.EscapedPath())
			}
		})
	}
}

func TestSelfPurchaseSnapshotRouteBoundaryDisabledNoFollow(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, false)
	paths := []string{
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/../id/purchase-snapshot",
		"/self//api/v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/./v1/billing/subscriptions/id/purchase-snapshot",
	}
	for _, target := range paths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			t.Run(method+" "+target, func(t *testing.T) {
				response := selfSnapshotRouteRequest(t, f, method, target, nil, "")
				defer response.Body.Close()
				if response.StatusCode != http.StatusNotFound || response.Header.Get("Location") != "" ||
					response.Header.Get("Allow") != "" || response.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("status=%d Location=%q Allow=%q Cache-Control=%q", response.StatusCode,
						response.Header.Get("Location"), response.Header.Get("Allow"), response.Header.Get("Cache-Control"))
				}
			})
		}
	}
	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
		origin string
	}{
		{"employee", f.cookie, ""},
		{"admin", f.adminCookie, ""},
		{"bad origin", f.cookie, "https://other.example"},
	} {
		t.Run("off identity "+tc.name, func(t *testing.T) {
			checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, http.MethodPost,
				"/self//api/v1/billing/subscriptions/id/purchase-snapshot", tc.cookie, tc.origin), 404, "", "")
		})
	}
	withSiblings := newSelfPurchaseSnapshotConfiguredFixture(t, false, enableSnapshotSiblingFlags)
	for _, target := range []string{
		"/self/api/v1/billing/subscriptions/id/renew/../purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/renewal-links/../purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/cancel/../purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/one-shot-renewal/../purchase-snapshot",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			t.Run("siblings "+method+" "+target, func(t *testing.T) {
				checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, withSiblings, method, target, nil, ""), 404, "", "")
			})
		}
	}
}

func TestSelfPurchaseSnapshotRouteBoundaryEnabledPriority(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, true)
	canonical := "/self/api/v1/billing/subscriptions/known/purchase-snapshot"
	malformed := "/self//api/v1/billing/subscriptions/known/purchase-snapshot"
	for _, target := range []string{canonical, malformed} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			wantStatus, wantCode, wantAllow := 400, "invalid_request", ""
			if target == canonical {
				if method == http.MethodGet {
					wantStatus, wantCode = 404, "not_found"
				} else {
					wantStatus, wantCode, wantAllow = 405, "method_not_allowed", http.MethodGet
				}
			}
			t.Run("employee "+method+" "+target, func(t *testing.T) {
				checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method, target, f.cookie, ""), wantStatus, wantCode, wantAllow)
			})
			t.Run("anonymous "+method+" "+target, func(t *testing.T) {
				checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method, target, nil, ""), 401, "authentication_required", "")
			})
			t.Run("admin "+method+" "+target, func(t *testing.T) {
				checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method, target, f.adminCookie, ""), 401, "authentication_required", "")
			})
			t.Run("wrong origin "+method+" "+target, func(t *testing.T) {
				checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method, target, f.cookie, "https://other.example"), 403, "request_rejected", "")
			})
		}
	}
	checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, http.MethodGet,
		"/self/api/v1/billing/subscriptions/id%20part/purchase-snapshot", f.cookie, ""), 404, "not_found", "")
	for _, target := range []string{canonical, malformed} {
		req, err := http.NewRequest(http.MethodPost, f.server.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(f.cookie)
		req.Header.Add("Origin", f.server.URL)
		req.Header.Add("Origin", f.server.URL)
		response, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		checkSelfSnapshotRouteResponse(t, response, 403, "request_rejected", "")
	}
	key := createTestKey(t, f.server.URL, f.id, "snapshot-route-role", f.adminCookie, f.adminCSRF)
	req, err := http.NewRequest(http.MethodPost, f.server.URL+malformed, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Key)
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	checkSelfSnapshotRouteResponse(t, response, 401, "authentication_required", "")
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, http.MethodPost, malformed, f.cookie, ""), 401, "authentication_required", "")
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE employees SET status='disabled' WHERE id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, http.MethodHead, canonical, f.cookie, ""), 401, "authentication_required", "")
}

func TestSelfPurchaseSnapshotRouteBoundaryMalformedMethodsNeverReachBusiness(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, true)
	called := 0
	f.app.selfPurchaseSnapshotCommit = func(_ *sql.Tx) error { called++; return nil }
	for _, target := range []string{
		"/self/api/v1/billing/subscriptions/id/../id/purchase-snapshot",
		"/self//api/v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/./v1/billing/subscriptions/id/purchase-snapshot",
		"/self/api/v1/billing/subscriptions/id/purchase-snapshot/extra",
		"/self/api/v1/billing/subscriptions/id/%70urchase-snapshot",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method, target, f.cookie, ""), 400, "invalid_request", "")
		}
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodDelete} {
		checkSelfSnapshotRouteResponse(t, selfSnapshotRouteRequest(t, f, method,
			"/self/api/v1/billing/subscriptions/id/purchase-snapshot", f.cookie, ""), 405, "method_not_allowed", http.MethodGet)
	}
	if called != 0 {
		t.Fatalf("business commit hook called %d times", called)
	}
}

func TestSelfPurchaseSnapshotRouteBoundaryDelegatesOnlyCanonicalGet(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, true)
	delegated := 0
	guard := f.app.selfPurchaseSnapshotRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated++
		w.WriteHeader(http.StatusNoContent)
	}))
	const canonical = "/self/api/v1/billing/subscriptions/id/purchase-snapshot"
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/self//api/v1/billing/subscriptions/id/purchase-snapshot"},
		{http.MethodHead, "/self//api/v1/billing/subscriptions/id/purchase-snapshot"},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id/../id/purchase-snapshot"},
		{http.MethodDelete, "/self/api/v1/billing/subscriptions/id/%70urchase-snapshot"},
		{http.MethodHead, canonical},
		{http.MethodPost, canonical},
		{http.MethodDelete, canonical},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(f.cookie)
		guard.ServeHTTP(httptest.NewRecorder(), req)
		if delegated != 0 {
			t.Fatalf("delegated rejected %s %s", tc.method, tc.path)
		}
	}
	req := httptest.NewRequest(http.MethodGet, canonical, nil)
	guard.ServeHTTP(httptest.NewRecorder(), req)
	if delegated != 1 {
		t.Fatalf("canonical GET delegation=%d want=1", delegated)
	}
}

func TestSelfPurchaseSnapshotRouteBoundaryDoesNotChangeSiblingRoutes(t *testing.T) {
	without := newSelfPurchaseSnapshotConfiguredFixture(t, false, enableSnapshotSiblingFlags)
	with := newSelfPurchaseSnapshotConfiguredFixture(t, true, enableSnapshotSiblingFlags)
	const prefix = "/self/api/v1/billing/subscriptions/"
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, prefix + "id/renew"},
		{http.MethodPost, prefix + "id/renewal-quotes"},
		{http.MethodGet, prefix + "id/renewal-links"},
		{http.MethodPost, prefix + "id/cancel"},
		{http.MethodGet, prefix + "id/one-shot-renewal"},
		{http.MethodPost, prefix + "id/one-shot-renewal/disarm"},
		{http.MethodGet, prefix + "id/unknown/purchase-snapshot"},
		{http.MethodGet, prefix + "id/purchase-snapshots"},
		{http.MethodGet, prefix + "id/renewal-links/purchase-snapshot"},
		{http.MethodPost, prefix + "id/purchase-snapshot/../cancel"},
		{http.MethodGet, prefix + "id/purchase-snapshot/../renewal-links"},
		{http.MethodGet, prefix + "id%2Fpurchase-snapshot/renewal-links"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			before := snapshotRouteSignature(t, selfSnapshotRouteRequest(t, without, tc.method, tc.path, without.cookie, ""))
			after := snapshotRouteSignature(t, selfSnapshotRouteRequest(t, with, tc.method, tc.path, with.cookie, ""))
			if before != after {
				t.Fatalf("sibling response changed without=%+v with=%+v", before, after)
			}
		})
	}
}

func enableSnapshotSiblingFlags(cfg *Config) {
	cfg.EmployeeSelfSubscriptionRenewalEnabled = true
	cfg.EmployeeSelfSubscriptionRenewalLinksEnabled = true
	cfg.EmployeeSelfSubscriptionCancelEnabled = true
	cfg.EmployeeSelfOneShotRenewalDisarmEnabled = true
}

type snapshotResponseSignature struct {
	status   int
	allow    string
	location string
	cache    string
	code     string
}

func snapshotRouteSignature(t *testing.T, response *http.Response) snapshotResponseSignature {
	t.Helper()
	defer response.Body.Close()
	signature := snapshotResponseSignature{
		status: response.StatusCode, allow: response.Header.Get("Allow"),
		location: response.Header.Get("Location"), cache: response.Header.Get("Cache-Control"),
	}
	if response.Request.Method != http.MethodHead {
		var body map[string]any
		if err := json.NewDecoder(response.Body).Decode(&body); err == nil {
			if object, ok := body["error"].(map[string]any); ok {
				signature.code, _ = object["code"].(string)
			}
		}
	}
	return signature
}

func checkSelfSnapshotRouteResponse(t *testing.T, response *http.Response, wantStatus int, wantCode, wantAllow string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus || response.Header.Get("Location") != "" ||
		response.Header.Get("Allow") != wantAllow || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d Location=%q Allow=%q Cache-Control=%q want status=%d Allow=%q", response.StatusCode,
			response.Header.Get("Location"), response.Header.Get("Allow"), response.Header.Get("Cache-Control"), wantStatus, wantAllow)
	}
	if response.Request.Method == http.MethodHead || wantCode == "" {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	object, ok := body["error"].(map[string]any)
	if !ok || object["code"] != wantCode || len(body) != 1 {
		t.Fatalf("body=%v want code=%q", body, wantCode)
	}
}

func selfSnapshotRouteRequest(t *testing.T, f selfWalletFixture, method, target string, cookie *http.Cookie, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
