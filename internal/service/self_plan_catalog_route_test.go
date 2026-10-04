// Independently authored for docs/employee-self-plan-catalog-route-boundary-contract.md.
package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const catalogBoundaryWebMarker = "catalog-boundary-web-marker"

func TestSelfPlanCatalogRouteShape(t *testing.T) {
	p := selfPlanCatalogRoutePath
	for _, tc := range []struct {
		name, target   string
		owned, literal bool
	}{
		{"canonical", p, true, true},
		{"canonical query", p + "?currency=USD", true, true},
		{"canonical bare query", p + "?", true, true},
		{"double slash", "/self/api/v1/billing//plans", true, false},
		{"dot segment", "/self/api/v1/billing/./plans", true, false},
		{"parent and return", p + "/../plans", true, false},
		{"child", p + "/extra", true, false},
		{"trailing slash", p + "/", true, false},
		{"complete plans segment before plans-other", p + "/../plans-other", true, false},
		{"complete plans segment before plansx", p + "/../plansx", true, false},
		{"overlap with one-shot owner remains rejection evidence only", p + "/../subscriptions/id/one-shot-renewal", true, false},
		{"ASCII case", "/SELF/API/V1/BILLING/PLANS", true, false},
		{"encoded letter", "/self/api/v1/billing/%70lans", true, false},
		{"encoded slash", "/self/api/v1/billing%2Fplans", true, false},
		{"backslash", "/self/api/v1/billing\\plans", true, false},
		{"encoded child slash", p + "%2Fextra", true, false},
		{"double encoded slash", p + "%252Fextra", true, false},
		{"plansx", p + "x", false, false},
		{"plans-other", p + "-other", false, false},
		{"encoded hyphen neighbor", p + "%2Dother", false, false},
		{"dot neighbor", p + ".extra", false, false},
		{"encoded question neighbor", p + "%3Ffoo", false, false},
		{"encoded hash neighbor", p + "%23foo", false, false},
		{"purchase quote neighbor", selfPlanPurchaseQuotePath, false, false},
		{"subscription neighbor", selfPlanPurchasePath, false, false},
		{"subscription child neighbor", selfPlanPurchasePath + "/id/cancel", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, literal := selfPlanCatalogRouteShape(httptest.NewRequest(http.MethodGet, tc.target, nil))
			if owned != tc.owned || literal != tc.literal {
				t.Fatalf("target=%q owned=%v literal=%v, want %v/%v", tc.target, owned, literal, tc.owned, tc.literal)
			}
		})
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
	}{
		{"missing RequestURI", &http.Request{URL: &url.URL{Path: p}}},
		{"conflicting RawPath", &http.Request{RequestURI: p, URL: &url.URL{Path: p, RawPath: p + "x"}}},
		{"conflicting Path", &http.Request{RequestURI: p, URL: &url.URL{Path: "/unrelated", RawPath: p}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, literal := selfPlanCatalogRouteShape(tc.r)
			if !owned || literal {
				t.Fatalf("owned=%v literal=%v; a conflicting view cannot enter the catalog", owned, literal)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/self/api/v1/billing/%70lans?currency=USD", nil)
	r.SetPathValue("owned-by-other-route", "sentinel")
	before := [...]string{r.RequestURI, r.URL.Path, r.URL.RawPath, r.URL.EscapedPath(), r.PathValue("owned-by-other-route")}
	owned, literal := selfPlanCatalogRouteShape(r)
	after := [...]string{r.RequestURI, r.URL.Path, r.URL.RawPath, r.URL.EscapedPath(), r.PathValue("owned-by-other-route")}
	if !owned || literal || after != before {
		t.Fatalf("classification changed request fields: owned=%v literal=%v before=%q after=%q", owned, literal, before, after)
	}
}

func TestSelfPlanCatalogRouteBudgetAndViews(t *testing.T) {
	parent, stem := "/self/api/v1/billing/", "plans"
	short := parent + strings.Repeat("/", selfPlanCatalogRouteMaxBytes-1-len(parent)-len(stem)) + stem
	hidden := short[:len(parent)] + "/" + short[len(parent):]
	if len(short) != 8191 || len(hidden) != 8192 {
		t.Fatal("incorrect budget fixture")
	}
	for _, tc := range []struct {
		name, target string
		want         bool
	}{
		{"8191 complete", short, true},
		{"8192 followed by query", hidden + "?" + strings.Repeat("x", 1<<20), true},
		{"8193 path byte", hidden + "x", false},
		{"8193 hidden separator", hidden + "/", false},
		{"visible separator before budget", selfPlanCatalogRoutePath + "/" + strings.Repeat("x", 8192), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RequestURI: tc.target, URL: &url.URL{Path: "/unrelated"}}
			owned, literal := selfPlanCatalogRouteShape(r)
			if owned != tc.want || literal {
				t.Fatalf("owned=%v literal=%v want %v/false", owned, literal, tc.want)
			}
		})
	}
	if owned, literal := selfPlanCatalogRouteShape(&http.Request{
		RequestURI: selfPlanCatalogRoutePath + "?" + strings.Repeat("x", 1<<20),
		URL:        &url.URL{Path: selfPlanCatalogRoutePath},
	}); !owned || !literal {
		t.Fatalf("long query changed literal ownership: %v/%v", owned, literal)
	}
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"complete RawPath", hidden, true},
		{"incomplete RawPath", hidden + "x", false},
		{"long RawPath with proved segment", selfPlanCatalogRoutePath + "/" + strings.Repeat("%41", 1<<18), true},
		{"long RawPath neighbor", selfPlanCatalogRoutePath + "-other/" + strings.Repeat("%41", 1<<18), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/unrelated", RawPath: tc.raw}}
			owned, literal := selfPlanCatalogRouteShape(r)
			if owned != tc.want || literal {
				t.Fatalf("owned=%v literal=%v want %v/false", owned, literal, tc.want)
			}
		})
	}
	deep := func(depth int) string {
		return selfPlanCatalogRoutePath + "%" + strings.Repeat("25", depth-1) + "2Fextra"
	}
	for _, tc := range []struct {
		depth int
		want  bool
	}{{16, true}, {17, false}} {
		if got := catalogRejectEvidence(catalogBoundPath(deep(tc.depth))); got != tc.want {
			t.Errorf("isolated %d-layer view owned=%v want %v", tc.depth, got, tc.want)
		}
	}
	for _, tc := range []struct {
		depth int
		want  bool
	}{{17, true}, {18, false}} {
		r := httptest.NewRequest(http.MethodGet, deep(tc.depth), nil)
		owned, literal := selfPlanCatalogRouteShape(r)
		if owned != tc.want || literal {
			t.Errorf("wire %d layers owned=%v literal=%v want %v/false", tc.depth, owned, literal, tc.want)
		}
	}
}

func TestSelfPlanCatalogRouteGuardPassesOtherMethods(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		called := false
		guard := (&App{}).selfPlanCatalogRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(218)
		}))
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, httptest.NewRequest(method, selfPlanCatalogRoutePath, nil))
		if !called || w.Code != 218 {
			t.Fatalf("method=%s called=%v status=%d", method, called, w.Code)
		}
	}
}

type catalogBoundaryFixture struct {
	app         *App
	server      *httptest.Server
	cookie      *http.Cookie
	adminCookie *http.Cookie
}

func newCatalogBoundaryFixture(t *testing.T, catalogEnabled, purchaseEnabled bool) catalogBoundaryFixture {
	t.Helper()
	dataDir, webDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(catalogBoundaryWebMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", WebDir: webDir, Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true,
		EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfPlanCatalogEnabled: catalogEnabled,
		EmployeeSelfPlanPurchaseEnabled: purchaseEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	employee := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, employee.ID, adminCookie, adminCSRF)
	cookie, _ := selfRedeem(t, server.URL, employee.ID, secret)
	return catalogBoundaryFixture{app, server, cookie, adminCookie}
}

type catalogWireResult struct {
	status                                    int
	location, allow, cache, contentType, code string
	marker                                    bool
	bodyBytes                                 int
}

func catalogWire(t *testing.T, serverURL, method, target string, cookie *http.Cookie, origin string, duplicateOrigin bool, bearer ...string) catalogWireResult {
	t.Helper()
	address := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var request strings.Builder
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, target, address)
	if origin != "" {
		fmt.Fprintf(&request, "Origin: %s\r\n", origin)
		if duplicateOrigin {
			fmt.Fprintf(&request, "Origin: %s\r\n", origin)
		}
	}
	if cookie != nil {
		fmt.Fprintf(&request, "Cookie: %s=%s\r\n", cookie.Name, cookie.Value)
	}
	if len(bearer) != 0 && bearer[0] != "" {
		fmt.Fprintf(&request, "Authorization: Bearer %s\r\n", bearer[0])
	}
	fmt.Fprint(&request, "Content-Length: 0\r\n\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	return catalogWireResult{
		status: response.StatusCode, location: response.Header.Get("Location"),
		allow: response.Header.Get("Allow"), cache: response.Header.Get("Cache-Control"),
		contentType: response.Header.Get("Content-Type"), code: parsed.Error.Code,
		marker: strings.Contains(string(body), catalogBoundaryWebMarker), bodyBytes: len(body),
	}
}

func catalogFinancialCounts(t *testing.T, app *App) [4]int {
	t.Helper()
	var counts [4]int
	for i, table := range [...]string{"financial_plans", "financial_accounts", "financial_entries", "financial_subscriptions"} {
		if err := app.store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestSelfPlanCatalogRouteBoundaryWireAndNeighbors(t *testing.T) {
	for _, flags := range []struct {
		name              string
		catalog, purchase bool
	}{{"catalog-off", false, false}, {"catalog-only", true, false}, {"catalog-and-purchase", true, true}} {
		t.Run(flags.name, func(t *testing.T) {
			f := newCatalogBoundaryFixture(t, flags.catalog, flags.purchase)
			enabled := flags.catalog
			financeBefore := catalogFinancialCounts(t, f.app)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, auth := range []struct {
					name   string
					cookie *http.Cookie
				}{{"anonymous", nil}, {"valid", f.cookie}} {
					for _, tc := range []struct {
						name, target string
						malformed    bool
					}{
						{"canonical", selfPlanCatalogRoutePath + "?currency=USD", false},
						{"double slash", "/self/api/v1/billing//plans?currency=USD", true},
						{"dot segment", "/self/api/v1/billing/./plans?currency=USD", true},
						{"encoded letter", "/self/api/v1/billing/%70lans?currency=USD", true},
						{"descendant", selfPlanCatalogRoutePath + "/extra?currency=USD", true},
						{"cross to plans-other", selfPlanCatalogRoutePath + "/../plans-other?currency=USD", true},
						{"cross to plansx", selfPlanCatalogRoutePath + "/../plansx?currency=USD", true},
					} {
						t.Run(method+"/"+auth.name+"/"+tc.name, func(t *testing.T) {
							got := catalogWire(t, f.server.URL, method, tc.target, auth.cookie, f.server.URL, false)
							want := http.StatusNotFound
							if enabled {
								if auth.cookie == nil {
									want = http.StatusUnauthorized
								} else if tc.malformed {
									want = http.StatusBadRequest
								} else {
									want = http.StatusOK
								}
							}
							if got.status != want || got.location != "" || got.allow != "" || got.cache != "no-store" || got.marker {
								t.Fatalf("status=%d Location=%q Allow=%q Cache=%q marker=%v, want %d/no redirect/no marker", got.status, got.location, got.allow, got.cache, got.marker, want)
							}
							if method == http.MethodHead && got.bodyBytes != 0 {
								t.Fatalf("HEAD returned %d body bytes", got.bodyBytes)
							}
							if method == http.MethodGet && enabled && tc.malformed && auth.cookie != nil && got.code != "invalid_request" {
								t.Fatalf("malformed status %d code=%q", got.status, got.code)
							}
						})
					}
				}
			}
			for _, target := range []string{selfPlanCatalogRoutePath + "x", selfPlanCatalogRoutePath + "-other", selfPlanCatalogRoutePath + "%2Dother"} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					got := catalogWire(t, f.server.URL, method, target, f.cookie, f.server.URL, false)
					if got.status != http.StatusNotFound || got.location != "" || got.cache != "no-store" {
						t.Fatalf("neighbor %s %s: %+v", method, target, got)
					}
				}
			}
			// The existing one-shot guard wraps the catalog guard. Although the
			// raw target contains a complete P/ segment, this cleaned sibling is
			// owned by one-shot while that feature is disabled.
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, cookie := range []*http.Cookie{nil, f.cookie} {
					got := catalogWire(t, f.server.URL, method,
						selfPlanCatalogRoutePath+"/../subscriptions/id/one-shot-renewal", cookie, f.server.URL, false)
					if got.status != http.StatusNotFound || got.location != "" || got.allow != "" ||
						got.cache != "no-store" || got.contentType != "text/plain; charset=utf-8" || got.marker {
						t.Fatalf("one-shot owner method=%s self=%v catalog=%v: %+v", method, cookie != nil, enabled, got)
					}
					if method == http.MethodHead && got.bodyBytes != 0 {
						t.Fatalf("one-shot HEAD returned %d bytes", got.bodyBytes)
					}
				}
			}
			for _, tc := range []struct {
				method, target string
				want           int
			}{
				{http.MethodPost, selfPlanCatalogRoutePath, http.StatusNotFound},
				{http.MethodGet, selfPlanPurchasePath, http.StatusOK},
				{http.MethodGet, selfPlanPurchasePath + "/id", http.StatusNotFound},
				{http.MethodPost, selfPlanPurchaseQuotePath, map[bool]int{false: 404, true: 403}[flags.purchase]},
				{http.MethodPost, selfPlanPurchasePath, map[bool]int{false: 404, true: 403}[flags.purchase]},
				{http.MethodPost, "/self/api/v1/billing//plan-purchase-quotes", map[bool]int{false: 404, true: 403}[flags.purchase]},
			} {
				got := catalogWire(t, f.server.URL, tc.method, tc.target, f.cookie, f.server.URL, false)
				if got.status != tc.want || got.cache != "no-store" {
					t.Fatalf("original owner %s %s: %+v", tc.method, tc.target, got)
				}
			}
			redirect := catalogWire(t, f.server.URL, http.MethodGet,
				"/self/api/v1/billing/./subscriptions", f.cookie, f.server.URL, false)
			if redirect.status != http.StatusTemporaryRedirect || redirect.location != selfPlanPurchasePath || redirect.cache != "no-store" {
				t.Fatalf("GET subscription dot-segment original owner: %+v", redirect)
			}
			for _, tc := range []struct {
				name   string
				cookie *http.Cookie
				bearer string
			}{
				{"admin cookie", f.adminCookie, ""},
				{"model Bearer only", nil, "synthetic-model-key"},
			} {
				got := catalogWire(t, f.server.URL, http.MethodGet,
					"/self/api/v1/billing//plans?currency=USD", tc.cookie, f.server.URL, false, tc.bearer)
				want := http.StatusNotFound
				if enabled {
					want = http.StatusUnauthorized
				}
				if got.status != want || got.location != "" || got.allow != "" || got.cache != "no-store" {
					t.Fatalf("%s: %+v", tc.name, got)
				}
			}
			withoutOrigin := catalogWire(t, f.server.URL, http.MethodGet,
				"/self/api/v1/billing//plans?currency=USD", f.cookie, "", false)
			wantWithoutOrigin := http.StatusNotFound
			if enabled {
				wantWithoutOrigin = http.StatusBadRequest
			}
			if withoutOrigin.status != wantWithoutOrigin {
				t.Fatalf("optional Origin: %+v", withoutOrigin)
			}
			for _, origin := range []string{"http://wrong.invalid", f.server.URL} {
				duplicate := origin == f.server.URL
				for _, cookie := range []*http.Cookie{nil, f.cookie, f.adminCookie} {
					got := catalogWire(t, f.server.URL, http.MethodGet,
						"/self/api/v1/billing//plans?currency=USD", cookie, origin, duplicate)
					want := http.StatusNotFound
					if enabled {
						want = http.StatusForbidden
					}
					if got.status != want || got.location != "" || got.allow != "" || got.cache != "no-store" {
						t.Fatalf("Origin priority enabled=%v cookie=%v origin=%q duplicate=%v: %+v", enabled, cookie != nil, origin, duplicate, got)
					}
				}
			}
			if financeAfter := catalogFinancialCounts(t, f.app); financeAfter != financeBefore {
				t.Fatalf("route probes changed financial counts: before=%v after=%v", financeBefore, financeAfter)
			}
		})
	}
}
