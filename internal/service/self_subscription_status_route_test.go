// Independently authored for
// docs/employee-self-subscription-status-route-boundary-contract.md.
package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func subscriptionCollectionRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	parsed, err := url.ParseRequestURI(target)
	if err != nil {
		t.Fatalf("parse target %q: %v", target, err)
	}
	return &http.Request{
		Method: method, RequestURI: target, URL: parsed, Host: "header.invalid",
		Header: make(http.Header),
	}
}

func TestSelfSubscriptionCollectionShapeOwners(t *testing.T) {
	s := selfSubscriptionCollectionPath
	for _, tc := range []struct {
		name, target   string
		owned, literal bool
	}{
		{"literal", s, true, true},
		{"literal query", s + "?limit=20", true, true},
		{"double slash", "/self/api/v1/billing//subscriptions", true, false},
		{"prefix double slash", "/self//api/v1/billing/subscriptions", true, false},
		{"dot segment", "/self/api/v1/billing/./subscriptions", true, false},
		{"encoded letter", "/self/api/v1/billing/%73ubscriptions", true, false},
		{"encoded separator before collection", "/self/api/v1/billing%2Fsubscriptions", true, false},
		{"empty trailing segment", s + "/", true, false},
		{"return to collection", s + "/../subscriptions", true, false},
		{"scheme one slash", "http:" + s, true, false},
		{"custom one slash", "custom:" + s, true, false},
		{"absolute", "http://authority.invalid" + s, true, false},
		{"upper scheme", "HTTP://authority.invalid" + s, true, false},
		{"userinfo", "http://u:synthetic@authority.invalid" + s, true, false},
		{"empty URL host", "http://" + s, true, false},
		{"ID child", s + "/sub_example", false, false},
		{"ID operation", s + "/sub_example/renewal-links", false, false},
		{"ID then dot back", s + "/sub_example/..", false, false},
		{"encoded ID then dot back", s + "%2Fsub_example%2F..", false, false},
		{"sibling plans", s + "/../plans", false, false},
		{"sibling visited before collection", "/self/api/v1/billing/plans/../subscriptions", false, false},
		{"prefix neighbor", s + "x", false, false},
		{"hyphen neighbor", s + "-old", false, false},
		{"encoded hyphen neighbor", s + "%2Dold", false, false},
		{"singular neighbor", "/self/api/v1/billing/subscription", false, false},
		{"encoded question path data", s + "%3Ffoo", false, false},
		{"encoded hash path data", s + "%23foo", false, false},
		{"upper path is not canonical", "/self/api/v1/billing/Subscriptions", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := subscriptionCollectionRequest(t, http.MethodGet, tc.target)
			owned, literal := selfSubscriptionCollectionShape(req)
			if owned != tc.owned || literal != tc.literal {
				t.Fatalf("%q: got owned/literal=%v/%v; want %v/%v",
					tc.target, owned, literal, tc.owned, tc.literal)
			}
		})
	}
}

func TestSelfSubscriptionCollectionBudgetAndViews(t *testing.T) {
	s := selfSubscriptionCollectionPath
	base := "/self/api/v1/billing/"
	at8192 := base + strings.Repeat("/", 8192-len(base)-len("subscriptions")) + "subscriptions"
	if len(at8192) != 8192 {
		t.Fatal("incorrect boundary fixture")
	}
	at8191 := base + strings.Repeat("/", 8191-len(base)-len("subscriptions")) + "subscriptions"
	if len(at8191) != 8191 {
		t.Fatal("incorrect 8191 fixture")
	}
	prefix1024 := "http://" + strings.Repeat("a", 1024-len("http://"))
	deep := func(rounds int) string {
		return base + "%" + strings.Repeat("25", rounds-1) + "73ubscriptions"
	}
	for _, tc := range []struct {
		name, target string
		owned        bool
	}{
		{"8192 no query", at8192, true},
		{"8192 followed by question", at8192 + "?x", true},
		{"8193 path character", at8192 + "x", false},
		{"8193 path then question", at8192 + "x?x", false},
		{"short path long query", s + "?" + strings.Repeat("x", 1<<20), true},
		{"1024 prefix", prefix1024 + s, true},
		{"1025 prefix", prefix1024 + "a" + s, false},
		{"16 decode rounds", deep(16), true},
		{"17 decode rounds", deep(17), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, _ := selfSubscriptionCollectionShape(subscriptionCollectionRequest(t, http.MethodGet, tc.target))
			if owned != tc.owned {
				t.Fatalf("%q owned=%v want %v", tc.name, owned, tc.owned)
			}
		})
	}
	// A 8191-byte fixture also stays within the byte budget even when a
	// leading slash is removed from the repeated-slash region.
	if owned, _ := selfSubscriptionCollectionShape(subscriptionCollectionRequest(t, http.MethodGet, at8191)); !owned {
		t.Fatal("8191-byte collection alias was not owned")
	}
	for _, tc := range []struct {
		name string
		req  *http.Request
	}{
		{"missing target", &http.Request{URL: &url.URL{Path: s}}},
		{"contradictory Path", &http.Request{RequestURI: s, URL: &url.URL{Path: "/elsewhere"}}},
		{"contradictory RawPath", &http.Request{RequestURI: s, URL: &url.URL{Path: s, RawPath: s + "x"}}},
		{"opaque", &http.Request{RequestURI: "http:opaque", URL: &url.URL{Scheme: "http", Opaque: "opaque", Path: s}}},
		{"contradictory URL host", &http.Request{RequestURI: "http://one.invalid" + s, URL: &url.URL{Scheme: "http", Host: "two.invalid", Path: s}}},
		{"oversized RawPath", &http.Request{RequestURI: s, URL: &url.URL{Path: s, RawPath: s + strings.Repeat("x", 8192)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if owned, literal := selfSubscriptionCollectionShape(tc.req); owned || literal {
				t.Fatalf("inconsistent request owned/literal=%v/%v", owned, literal)
			}
		})
	}
	req := subscriptionCollectionRequest(t, http.MethodGet, base+"%73ubscriptions?limit=20")
	req.SetPathValue("unrelated", "sentinel")
	before := [...]string{req.RequestURI, req.URL.Path, req.URL.RawPath, req.URL.EscapedPath(), req.PathValue("unrelated")}
	selfSubscriptionCollectionShape(req)
	after := [...]string{req.RequestURI, req.URL.Path, req.URL.RawPath, req.URL.EscapedPath(), req.PathValue("unrelated")}
	if before != after {
		t.Fatalf("classification changed request: %q -> %q", before, after)
	}
}

func TestSelfSubscriptionCollectionGuardPassThroughAndOff(t *testing.T) {
	s := selfSubscriptionCollectionPath
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		called := false
		guard := (&App{}).selfSubscriptionCollectionRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.Header().Set("Allow", "old-owner")
			w.WriteHeader(218)
		}))
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, subscriptionCollectionRequest(t, method, s))
		if !called || response.Code != 218 || response.Header().Get("Allow") != "old-owner" {
			t.Fatalf("%s owner changed: called=%v status=%d headers=%v", method, called, response.Code, response.Header())
		}
	}
	for _, target := range []string{s, "/self/api/v1/billing//subscriptions", "http://authority.invalid" + s} {
		called := false
		guard := (&App{}).selfSubscriptionCollectionRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(218)
		}))
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, subscriptionCollectionRequest(t, http.MethodGet, target))
		if called || response.Code != http.StatusNotFound ||
			response.Header().Get("Location") != "" || response.Header().Get("Allow") != "" {
			t.Fatalf("off target=%q called=%v status=%d headers=%v",
				target, called, response.Code, response.Header())
		}
	}
	for _, target := range []string{s + "/sub_example/purchase-snapshot", s + "/../plans", s + "-old"} {
		called := false
		guard := (&App{}).selfSubscriptionCollectionRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(218)
		}))
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, subscriptionCollectionRequest(t, http.MethodGet, target))
		if !called || response.Code != 218 {
			t.Fatalf("neighbor target=%q owner changed: called=%v status=%d", target, called, response.Code)
		}
	}
}

func TestSelfSubscriptionCollectionEnabledReadPrecedence(t *testing.T) {
	f := newSelfSubscriptionFixture(t)
	s := selfSubscriptionCollectionPath
	financialBefore := balanceBoundaryFinancialCounts(t, f.app)
	for _, tc := range []struct {
		name, method, target, origin string
		cookie                       *http.Cookie
		want                         int
	}{
		{"literal valid GET", http.MethodGet, s + "?limit=20", "", f.cookie, 200},
		{"literal valid HEAD", http.MethodHead, s + "?limit=20", "", f.cookie, 200},
		{"alias anonymous", http.MethodGet, "/self/api/v1/billing//subscriptions?limit=20", "", nil, 401},
		{"alias valid", http.MethodGet, "/self/api/v1/billing//subscriptions?limit=20", "", f.cookie, 400},
		{"alias valid HEAD", http.MethodHead, "/self/api/v1/billing/./subscriptions?limit=20", "", f.cookie, 400},
		{"alias bad Origin before session", http.MethodGet, "/self/api/v1/billing//subscriptions?limit=20", "http://evil.invalid", nil, 403},
		{"alias bad Origin before shape", http.MethodGet, "/self/api/v1/billing//subscriptions?limit=20", "http://evil.invalid", f.cookie, 403},
		{"non-origin valid", http.MethodGet, "http:" + s + "?limit=20", "", f.cookie, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := subscriptionCollectionRequest(t, tc.method, tc.target)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			f.app.Handler().ServeHTTP(response, req)
			if response.Code != tc.want || response.Header().Get("Location") != "" ||
				response.Header().Get("Allow") != "" || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v want=%d", response.Code, response.Header(), tc.want)
			}
		})
	}
	if financialAfter := balanceBoundaryFinancialCounts(t, f.app); financialAfter != financialBefore {
		t.Fatalf("route checks changed financial rows: before=%v after=%v", financialBefore, financialAfter)
	}
}
