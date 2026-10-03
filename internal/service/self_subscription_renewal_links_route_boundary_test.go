package service

// Independently authored acceptance tests for
// docs/employee-self-subscription-renewal-links-route-boundary-contract.md.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelfRenewalLinksRouteBoundaryShapeAndSiblings(t *testing.T) {
	const prefix = "/self/api/v1/billing/subscriptions/"
	for _, tc := range []struct {
		path      string
		shaped    bool
		canonical bool
	}{
		{prefix + "id/renewal-links", true, true},
		{prefix + "id%20part/renewal-links", true, true},
		{prefix + "/renewal-links", true, false},
		{prefix + "id/renewal-links/extra", true, false},
		{prefix + "id/renewal-links/", true, false},
		{prefix + "id%2Fextra/renewal-links", true, false},
		{prefix + "id%252Fextra/renewal-links", true, false},
		{prefix + "id%5Cextra/renewal-links", true, false},
		{prefix + "id%255Cextra/renewal-links", true, false},
		{prefix + "%2e%2e/renewal-links", true, false},
		{prefix + "./id/renewal-links", true, false},
		{prefix + "id/../id/renewal-links", true, false},
		{"/self//api/v1/billing/subscriptions/id/renewal-links", true, false},
		{"/self/api/./v1/billing/subscriptions/id/renewal-links", true, false},
		{"/self/api/%2e/v1/billing/subscriptions/id/renewal-links", true, false},
		{prefix + "id/%72enewal-links", true, false},
		{prefix + "id/%2572enewal-links", true, false},
		{prefix + "id/renewal-links/%2e%2e/cancel", true, false},
		{prefix + "id/renewal-links/%252e%252e/cancel", true, false},
		{prefix + "id/%72enewal-links/%2e%2e/cancel", true, false},
		{prefix + "id%2Frenewal-links", false, false},
		{prefix + "id%252Frenewal-links", false, false},
		{prefix + "id/renewal-links-extra", false, false},
		{prefix + "id/unknown/renewal-links", false, false},
		{prefix + "id/renew", false, false},
		{prefix + "id/renewal-quotes", false, false},
		{prefix + "id/cancel", false, false},
		{prefix + "id/one-shot-renewal", false, false},
		{prefix + "id/purchase-snapshot", false, false},
		{prefix + "id/renewal-links/../cancel", false, false},
		{prefix + "id/%72enewal-links/../cancel", false, false},
		{prefix + "id/renewal-links/../purchase-snapshot", false, false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if got := selfRenewalLinksShapedPath(request); got != tc.shaped {
				t.Fatalf("shaped=%v want=%v escaped=%q", got, tc.shaped, request.URL.EscapedPath())
			}
			if tc.shaped {
				if got := !malformedSelfRenewalLinksPath(request); got != tc.canonical {
					t.Fatalf("canonical=%v want=%v escaped=%q", got, tc.canonical, request.URL.EscapedPath())
				}
			}
		})
	}
}

func TestSelfRenewalLinksRouteBoundaryDisabledNoRedirect(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, false)
	for _, target := range []string{
		"/self/api/v1/billing/subscriptions/id/renewal-links",
		"/self/api/v1/billing/subscriptions/id/../id/renewal-links",
		"/self//api/v1/billing/subscriptions/id/renewal-links",
		"/self/api/./v1/billing/subscriptions/id/renewal-links",
		"/self/api/%2e/v1/billing/subscriptions/id/renewal-links",
		"/self/api/v1/billing/subscriptions/id/%72enewal-links",
		"/self/api/v1/billing/subscriptions/id/renewal-links/extra",
		"/self/api/v1/billing/subscriptions/id/renewal-links/%2e%2e/cancel",
		"/self/api/v1/billing/subscriptions/id/renewal-links/%252e%252e/cancel",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			t.Run(method+" "+target, func(t *testing.T) {
				request := httptest.NewRequest(method, target, nil)
				response := httptest.NewRecorder()
				f.app.Handler().ServeHTTP(response, request)
				result := response.Result()
				defer result.Body.Close()
				if result.StatusCode != http.StatusNotFound || result.Header.Get("Location") != "" ||
					result.Header.Get("Allow") != "" || result.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("status=%d Location=%q Allow=%q Cache-Control=%q", result.StatusCode,
						result.Header.Get("Location"), result.Header.Get("Allow"), result.Header.Get("Cache-Control"))
				}
			})
		}
	}
	for _, cookie := range []*http.Cookie{f.cookie, f.adminCookie} {
		assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, http.MethodPost,
			"/self//api/v1/billing/subscriptions/id/renewal-links", cookie, "https://other.example"), 404, "", "")
	}
}

func TestSelfRenewalLinksRouteBoundaryEnabledPriorities(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, true)
	const canonical = "/self/api/v1/billing/subscriptions/missing/renewal-links"
	const malformed = "/self//api/v1/billing/subscriptions/missing/renewal-links"
	for _, target := range []string{
		canonical, malformed,
		"/self/api/v1/billing/subscriptions/id/renewal-links/%2e%2e/cancel",
		"/self/api/v1/billing/subscriptions/id/renewal-links/%252e%252e/cancel",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			wantStatus, wantCode, wantAllow := 400, "invalid_request", ""
			if target == canonical {
				if method == http.MethodGet {
					wantStatus, wantCode = 404, "not_found"
				} else {
					wantStatus, wantCode, wantAllow = 405, "method_not_allowed", http.MethodGet
				}
			}
			t.Run(method+" "+target, func(t *testing.T) {
				assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, method, target, f.cookie, ""), wantStatus, wantCode, wantAllow)
				assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, method, target, nil, ""), 401, "authentication_required", "")
				assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, method, target, f.adminCookie, ""), 401, "authentication_required", "")
				assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, method, target, f.cookie, "https://other.example"), 403, "request_rejected", "")
			})
		}
	}
	assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, http.MethodGet,
		"/self/api/v1/billing/subscriptions/id%20part/renewal-links", f.cookie, ""), 404, "not_found", "")
	for _, target := range []string{canonical, malformed} {
		request, err := http.NewRequest(http.MethodPost, f.server.URL+target, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(f.cookie)
		request.Header.Add("Origin", f.server.URL)
		request.Header.Add("Origin", f.server.URL)
		client := f.server.Client()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		assertRenewalBoundaryResponse(t, response, 403, "request_rejected", "")
	}
	key := createTestKey(t, f.server.URL, f.id, "renewal-boundary-key", f.adminCookie, f.adminCSRF)
	request, err := http.NewRequest(http.MethodPost, f.server.URL+malformed, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key.Key)
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, response, 401, "authentication_required", "")
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, http.MethodDelete, malformed, f.cookie, ""), 401, "authentication_required", "")
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE employees SET status='disabled' WHERE id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, f, http.MethodHead, canonical, f.cookie, ""), 401, "authentication_required", "")
}

func TestSelfRenewalLinksRouteBoundaryRejectsWithoutBusinessDelegation(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, true)
	delegated, committed := 0, 0
	f.app.selfRenewalLinksCommit = func(*sql.Tx) error { committed++; return nil }
	guard := f.app.selfRenewalLinksRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated++
		w.WriteHeader(http.StatusNoContent)
	}))
	const prefix = "/self/api/v1/billing/subscriptions/id/"
	for _, target := range []string{
		"/self//api/v1/billing/subscriptions/id/renewal-links",
		"/self/api/./v1/billing/subscriptions/id/renewal-links",
		"/self/api/%2e/v1/billing/subscriptions/id/renewal-links",
		prefix + "%72enewal-links",
		prefix + "renewal-links/extra",
		prefix + "renewal-links/%2e%2e/cancel",
		"/self/api/v1/billing/subscriptions/id%2Fother/renewal-links",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
			request := httptest.NewRequest(method, target, nil)
			request.AddCookie(f.cookie)
			response := httptest.NewRecorder()
			guard.ServeHTTP(response, request)
			if got := response.Code; got != 400 {
				t.Fatalf("%s %s status=%d want=400", method, target, got)
			}
		}
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodDelete} {
		request := httptest.NewRequest(method, prefix+"renewal-links", nil)
		request.AddCookie(f.cookie)
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, request)
		if response.Code != 405 || response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("canonical %s status=%d Allow=%q", method, response.Code, response.Header().Get("Allow"))
		}
	}
	if delegated != 0 || committed != 0 {
		t.Fatalf("rejected route reached next=%d commit=%d", delegated, committed)
	}
	guard.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, prefix+"renewal-links", nil))
	if delegated != 1 {
		t.Fatalf("canonical GET delegation=%d want=1", delegated)
	}
}

func TestSelfRenewalLinksRouteBoundaryPreservesSiblingOwners(t *testing.T) {
	configure := func(enabled bool) func(*Config) {
		return func(cfg *Config) {
			cfg.EmployeeSelfSubscriptionRenewalLinksEnabled = enabled
			cfg.EmployeeSelfSubscriptionRenewalEnabled = true
			cfg.EmployeeSelfSubscriptionCancelEnabled = true
			cfg.EmployeeSelfOneShotRenewalDisarmEnabled = true
		}
	}
	without := newSelfPurchaseSnapshotConfiguredFixture(t, true, configure(false))
	with := newSelfPurchaseSnapshotConfiguredFixture(t, true, configure(true))
	const prefix = "/self/api/v1/billing/subscriptions/id/"
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, prefix + "renew"},
		{http.MethodPost, prefix + "renewal-quotes"},
		{http.MethodPost, prefix + "cancel"},
		{http.MethodGet, prefix + "one-shot-renewal"},
		{http.MethodPost, prefix + "one-shot-renewal/disarm"},
		{http.MethodGet, prefix + "purchase-snapshot"},
		{http.MethodGet, prefix + "unknown/renewal-links"},
		{http.MethodGet, prefix + "renewal-links-extra"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id%2Frenewal-links"},
		{http.MethodGet, prefix + "renewal-links/../purchase-snapshot"},
		{http.MethodPost, prefix + "renewal-links/../cancel"},
		{http.MethodPost, prefix + "%72enewal-links/../cancel"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			before := renewalBoundarySignature(t, renewalBoundaryRequest(t, without, tc.method, tc.path, without.cookie, ""))
			after := renewalBoundarySignature(t, renewalBoundaryRequest(t, with, tc.method, tc.path, with.cookie, ""))
			if before != after {
				t.Fatalf("sibling changed without=%+v with=%+v", before, after)
			}
		})
	}
	// With both features enabled, an encoded dot tail is still a malformed
	// renewal-links request, not a cancel write-auth request.
	for _, target := range []string{
		prefix + "renewal-links/%2e%2e/cancel",
		prefix + "renewal-links/%252e%252e/cancel",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			assertRenewalBoundaryResponse(t, renewalBoundaryRequest(t, with, method, target, with.cookie, ""),
				400, "invalid_request", "")
		}
	}
}

func renewalBoundaryRequest(t *testing.T, f selfWalletFixture, method, target string, cookie *http.Cookie, origin string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, f.server.URL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertRenewalBoundaryResponse(t *testing.T, response *http.Response, wantStatus int, wantCode, wantAllow string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus || response.Header.Get("Allow") != wantAllow ||
		response.Header.Get("Location") != "" || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d Allow=%q Location=%q Cache-Control=%q; want status=%d Allow=%q",
			response.StatusCode, response.Header.Get("Allow"), response.Header.Get("Location"), response.Header.Get("Cache-Control"), wantStatus, wantAllow)
	}
	if response.Request.Method == http.MethodHead || wantCode == "" {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	errorObject, ok := body["error"].(map[string]any)
	if !ok || errorObject["code"] != wantCode || len(body) != 1 {
		t.Fatalf("body=%v want error code=%q", body, wantCode)
	}
}

type renewalBoundaryResponseSignature struct {
	status, allow, location, cache, code string
}

func renewalBoundarySignature(t *testing.T, response *http.Response) renewalBoundaryResponseSignature {
	t.Helper()
	defer response.Body.Close()
	signature := renewalBoundaryResponseSignature{
		status: response.Status, allow: response.Header.Get("Allow"),
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
