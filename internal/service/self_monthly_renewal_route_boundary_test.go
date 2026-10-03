package service

// Independently authored for docs/employee-self-monthly-renewal-route-boundary-contract.md.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSelfMonthlyRenewalRouteBoundaryShapeOwnership(t *testing.T) {
	const prefix = "/self/api/v1/billing/subscriptions/"
	for _, tc := range []struct{ path, want string }{
		{prefix + "id/renewal-quotes", "/renewal-quotes"},
		{prefix + "id/renew", "/renew"},
		{prefix + "id%20part/renew", "/renew"},
		{prefix + "/renew", "/renew"},
		{prefix + "id/renew/extra", "/renew"},
		{prefix + "id/renewal-quotes/extra", "/renewal-quotes"},
		{prefix + "id%2Fother/renew", "/renew"},
		{prefix + "id%252Fother/renew", "/renew"},
		{prefix + "id%5Cother/renew", "/renew"},
		{prefix + "id%255Cother/renew", "/renew"},
		{prefix + "%2e%2e/renew", "/renew"},
		{prefix + "id/../id/renew", "/renew"},
		{"/self//api/v1/billing/subscriptions/id/renew", "/renew"},
		{"/self/api/./v1/billing/subscriptions/id/renew", "/renew"},
		{"/self/api/%2e/v1/billing/subscriptions/id/renew", "/renew"},
		{prefix + "id/%72enew", "/renew"},
		{prefix + "id/%2572enew", "/renew"},
		{prefix + "id/renew/%2e%2e/cancel", "/renew"},
		{prefix + "id/renew/%252e%252e/cancel", "/renew"},
		{prefix + "id%2Frenew", ""},
		{prefix + "id%252Frenew", ""},
		{prefix + "id/unknown/renew", ""},
		{prefix + "id/renewal-links", ""},
		{prefix + "id/cancel", ""},
		{prefix + "id/one-shot-renewal", ""},
		{prefix + "id/purchase-snapshot", ""},
		{prefix + "id/renewal-quotes-extra", ""},
		{prefix + "id/renewals", ""},
		{prefix + "id/renewal-links/../cancel", ""},
		{prefix + "id/renewal-links/../renew", "/renew"},
		{prefix + "id/renew/../cancel", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if got := selfMonthlyRenewalShape(request); got != tc.want {
				t.Fatalf("shape=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestSelfMonthlyRenewalRouteBoundaryNoFollowBaseline(t *testing.T) {
	const base = "/self/api/v1/billing/subscriptions/id/"
	for _, enabled := range []bool{false, true} {
		var f selfPurchaseFixture
		if enabled {
			f = newSelfMonthlyRenewalFixture(t)
		} else {
			f = newSelfPurchaseFixture(t)
		}
		for _, operation := range []string{"renewal-quotes", "renew"} {
			for _, target := range []string{
				"/self//api/v1/billing/subscriptions/id/" + operation,
				"/self/api/./v1/billing/subscriptions/id/" + operation,
				base + "../id/" + operation,
			} {
				t.Run(target, func(t *testing.T) {
					request := httptest.NewRequest(http.MethodPost, target, nil)
					if enabled {
						request.Host = f.server.Listener.Addr().String()
						request.AddCookie(f.cookie)
						request.Header.Set("Origin", f.server.URL)
						request.Header.Set("X-Self-Request", "1")
						request.Header.Set("X-CSRF-Token", f.csrf)
					}
					response := httptest.NewRecorder()
					f.app.Handler().ServeHTTP(response, request)
					result := response.Result()
					defer result.Body.Close()
					want := http.StatusNotFound
					if enabled {
						want = http.StatusBadRequest
					}
					t.Logf("enabled=%v target=%q status=%d Location=%q Allow=%q Cache-Control=%q Content-Type=%q", enabled,
						target, result.StatusCode, result.Header.Get("Location"), result.Header.Get("Allow"),
						result.Header.Get("Cache-Control"), result.Header.Get("Content-Type"))
					if result.StatusCode != want || result.Header.Get("Location") != "" || result.Header.Get("Allow") != "" ||
						result.Header.Get("Cache-Control") != "no-store" {
						t.Fatalf("route boundary: want status %d, no redirect/Allow, no-store", want)
					}
				})
			}
		}
	}
}

func monthlyBoundaryRequest(t *testing.T, f selfWalletFixture, method, target string, cookie *http.Cookie,
	origin, selfHeader, csrf string) *http.Response {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Host = f.server.Listener.Addr().String()
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if selfHeader != "" {
		request.Header.Set("X-Self-Request", selfHeader)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(response, request)
	result := response.Result()
	result.Request = request
	return result
}

func TestSelfMonthlyRenewalRouteBoundaryMethodAndAuthMatrix(t *testing.T) {
	off := newSelfPurchaseFixture(t)
	on := newSelfMonthlyRenewalFixture(t)
	for _, operation := range []string{"renewal-quotes", "renew"} {
		canonical := "/self/api/v1/billing/subscriptions/id/" + operation
		for _, target := range []string{
			canonical,
			"/self//api/v1/billing/subscriptions/id/" + operation,
			"/self/api/%2e/v1/billing/subscriptions/id/" + operation,
			"/self/api/v1/billing/subscriptions/id%2Fother/" + operation,
			"/self/api/v1/billing/subscriptions/id%252Fother/" + operation,
			"/self/api/v1/billing/subscriptions/id/%72" + operation[1:],
			canonical + "/extra",
		} {
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete} {
				t.Run(method+" "+target, func(t *testing.T) {
					assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, off.selfWalletFixture, method, target,
						off.adminCookie, "https://wrong.example", "", ""), 404, "", "")
					if target == canonical && method == http.MethodPost {
						return // Existing quote/commit tests own canonical business semantics.
					}
					wantStatus, wantCode, wantAllow := 400, "invalid_request", ""
					if target == canonical {
						wantStatus, wantCode, wantAllow = 405, "method_not_allowed", http.MethodPost
					}
					selfHeader, csrf := "", ""
					if method == http.MethodPost {
						selfHeader, csrf = "1", on.csrf
					}
					assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, method, target,
						on.cookie, on.server.URL, selfHeader, csrf), wantStatus, wantCode, wantAllow)
					assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, method, target,
						nil, "", selfHeader, csrf), 401, "authentication_required", "")
					assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, method, target,
						on.adminCookie, "", selfHeader, csrf), 401, "authentication_required", "")
					assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, method, target,
						on.cookie, "https://wrong.example", selfHeader, csrf), 403, "request_rejected", "")
				})
			}
		}
	}
	const malformed = "/self//api/v1/billing/subscriptions/id/renew"
	for _, tc := range []struct{ selfHeader, csrf, code string }{
		{"", on.csrf, "request_rejected"}, {"wrong", on.csrf, "request_rejected"},
		{"1", "", "csrf_rejected"}, {"1", "wrong", "csrf_rejected"},
	} {
		assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, http.MethodPost, malformed,
			on.cookie, on.server.URL, tc.selfHeader, tc.csrf), 403, tc.code, "")
	}
	request := httptest.NewRequest(http.MethodGet, malformed, nil)
	request.Host = on.server.Listener.Addr().String()
	request.AddCookie(on.cookie)
	request.Header.Add("Origin", on.server.URL)
	request.Header.Add("Origin", on.server.URL)
	response := httptest.NewRecorder()
	on.app.Handler().ServeHTTP(response, request)
	result := response.Result()
	result.Request = request
	assertRenewalBoundaryResponse(t, result, 403, "request_rejected", "")
	key := createTestKey(t, on.server.URL, on.id, "monthly-boundary-key", on.adminCookie, on.adminCSRF)
	bearer := httptest.NewRequest(http.MethodGet, malformed, nil)
	bearer.Host = on.server.Listener.Addr().String()
	bearer.Header.Set("Authorization", "Bearer "+key.Key)
	bearerResponse := httptest.NewRecorder()
	on.app.Handler().ServeHTTP(bearerResponse, bearer)
	bearerResult := bearerResponse.Result()
	bearerResult.Request = bearer
	assertRenewalBoundaryResponse(t, bearerResult, 401, "authentication_required", "")
	for _, operation := range []string{"renewal-quotes", "renew"} {
		encodedCanonical := "/self/api/v1/billing/subscriptions/id%20part/" + operation
		assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, http.MethodGet,
			encodedCanonical, on.cookie, "", "", ""), 405, "method_not_allowed", http.MethodPost)
	}
	if _, err := on.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), on.id); err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, http.MethodPost, malformed,
		on.cookie, on.server.URL, "1", on.csrf), 401, "authentication_required", "")
	if _, err := on.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), on.id); err != nil {
		t.Fatal(err)
	}
	if _, err := on.app.store.db.Exec(`UPDATE employees SET status='disabled' WHERE id=?`, on.id); err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, http.MethodGet, malformed,
		on.cookie, "", "", ""), 401, "authentication_required", "")
	if _, err := on.app.store.db.Exec(`UPDATE employees SET status='active' WHERE id=?`, on.id); err != nil {
		t.Fatal(err)
	}
	if _, err := on.app.store.db.Exec(`DELETE FROM employee_self_sessions WHERE employee_id=?`, on.id); err != nil {
		t.Fatal(err)
	}
	assertRenewalBoundaryResponse(t, monthlyBoundaryRequest(t, on.selfWalletFixture, http.MethodDelete, malformed,
		on.cookie, "", "", ""), 401, "authentication_required", "")
}

func TestSelfMonthlyRenewalRouteBoundaryDoesNotDelegateRejectedRequests(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	delegated := 0
	guard := f.app.selfMonthlyRenewalRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		delegated++
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, target := range []string{
		"/self//api/v1/billing/subscriptions/id/renewal-quotes",
		"/self/api/%2e/v1/billing/subscriptions/id/renew",
		"/self/api/v1/billing/subscriptions/id%2Fother/renew",
		"/self/api/v1/billing/subscriptions/id/renew/extra",
	} {
		request := httptest.NewRequest(http.MethodPost, target, nil)
		request.Host = f.server.Listener.Addr().String()
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", f.server.URL)
		request.Header.Set("X-Self-Request", "1")
		request.Header.Set("X-CSRF-Token", f.csrf)
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatalf("target=%q status=%d", target, response.Code)
		}
	}
	if delegated != 0 {
		t.Fatalf("rejected route delegated=%d", delegated)
	}
	for _, operation := range []string{"renew", "renewal-quotes"} {
		request := httptest.NewRequest(http.MethodPost, "/self/api/v1/billing/subscriptions/id/"+operation, nil)
		guard.ServeHTTP(httptest.NewRecorder(), request)
	}
	if delegated != 2 {
		t.Fatalf("canonical POST delegated=%d", delegated)
	}
	for _, sibling := range []string{"renewal-links", "cancel", "one-shot-renewal", "purchase-snapshot", "unknown/renew"} {
		request := httptest.NewRequest(http.MethodGet, "/self/api/v1/billing/subscriptions/id/"+sibling, nil)
		guard.ServeHTTP(httptest.NewRecorder(), request)
	}
	if delegated != 7 {
		t.Fatalf("sibling routes delegated=%d", delegated)
	}
}

func TestSelfMonthlyRenewalRouteBoundaryPreservesSiblingOwners(t *testing.T) {
	configure := func(enabled bool) func(*Config) {
		return func(cfg *Config) {
			cfg.EmployeeSelfSubscriptionRenewalEnabled = enabled
			cfg.EmployeeSelfSubscriptionRenewalLinksEnabled = true
			cfg.EmployeeSelfSubscriptionCancelEnabled = true
			cfg.EmployeeSelfOneShotRenewalDisarmEnabled = true
		}
	}
	off := newSelfPurchaseSnapshotConfiguredFixture(t, true, configure(false))
	on := newSelfPurchaseSnapshotConfiguredFixture(t, true, configure(true))
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/renewal-links"},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id/cancel"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/one-shot-renewal"},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id/one-shot-renewal/disarm"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/purchase-snapshot"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/unknown/renew"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/renewal-quotes-extra"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id%2Frenew"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/renewal-links/%2e%2e/cancel"},
		{http.MethodPost, "/self/api/v1/billing/subscriptions/id/renewal-links/../cancel"},
		{http.MethodGet, "/self/api/v1/billing/subscriptions/id/renew/../cancel"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			before := renewalBoundarySignature(t, monthlyBoundaryRequest(t, off, tc.method, tc.target,
				off.cookie, off.server.URL, "1", off.csrf))
			after := renewalBoundarySignature(t, monthlyBoundaryRequest(t, on, tc.method, tc.target,
				on.cookie, on.server.URL, "1", on.csrf))
			if before != after {
				t.Fatalf("sibling changed off=%+v on=%+v", before, after)
			}
		})
	}
}
