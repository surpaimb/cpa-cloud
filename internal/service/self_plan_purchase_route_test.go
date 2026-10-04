// Independently authored for docs/employee-self-plan-purchase-route-boundary-contract.md.
package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const selfPlanPurchaseBoundaryMarker = "self-plan-purchase-boundary-web-marker"

func newSelfPlanPurchaseBoundaryFixture(t *testing.T, enabled bool) selfWalletFixture {
	t.Helper()
	dir, webDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(selfPlanPurchaseBoundaryMarker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", WebDir: webDir, Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true,
		EmployeeSelfPlanCatalogEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfPlanPurchaseEnabled: enabled,
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
	cookie, csrf := selfRedeem(t, server.URL, employee.ID, secret)
	return selfWalletFixture{app: app, server: server, dir: dir, id: employee.ID,
		cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func TestSelfPlanPurchaseRouteShapeAndNeighbors(t *testing.T) {
	q, s := selfPlanPurchaseQuotePath, selfPlanPurchasePath
	cases := []struct {
		name, target    string
		shaped, literal bool
	}{
		{"quote", q, true, true},
		{"purchase", s, true, true},
		{"quote bare query", q + "?", true, true},
		{"purchase query", s + "?x", true, true},
		{"quote double slash", "/self/api/v1/billing//plan-purchase-quotes", true, false},
		{"purchase dot", "/self/api/v1/billing/./subscriptions", true, false},
		{"quote child", q + "/extra", true, false},
		{"quote trailing slash", q + "/", true, false},
		{"purchase dot child", s + "/./", true, false},
		{"purchase ID dotdot", s + "/id/..", true, false},
		{"quote case", "/SELF/API/V1/BILLING/PLAN-PURCHASE-QUOTES", true, false},
		{"purchase case", "/SELF/API/V1/BILLING/SUBSCRIPTIONS", true, false},
		{"quote encoded letter", "/self/api/v1/billing/%70lan-purchase-quotes", true, false},
		{"purchase encoded slash", "/self/api/v1/billing%2Fsubscriptions", true, false},
		{"quote backslash", "/self/api/v1/billing\\plan-purchase-quotes", true, false},
		{"purchase backslash", "/self/api/v1/billing\\subscriptions", true, false},
		{"quote encoded slash child", q + "%2Fextra", true, false},
		{"quote double encoded slash child", q + "%252Fextra", true, false},
		{"quote hyphen neighbor", q + "-other", false, false},
		{"quote encoded hyphen neighbor", q + "%2Dother", false, false},
		{"quote x neighbor", q + "x", false, false},
		{"quote dot neighbor", q + ".extra", false, false},
		{"quote encoded question neighbor", q + "%3Ffoo", false, false},
		{"quote encoded hash neighbor", q + "%23foo", false, false},
		{"purchase x neighbor", s + "x", false, false},
		{"purchase hyphen neighbor", s + "-other", false, false},
		{"purchase ID neighbor", s + "/id", false, false},
		{"purchase doubled slash ID neighbor", s + "//id", false, false},
		{"purchase cancel neighbor", s + "/id/cancel", false, false},
		{"purchase renewal neighbor", s + "/id/renew", false, false},
		{"plans neighbor", "/self/api/v1/billing/plans", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.target, nil)
			shaped, literal := selfPlanPurchaseRouteShape(r)
			if shaped != tc.shaped || literal != tc.literal {
				t.Errorf("target=%q shape=%v literal=%v, want %v/%v", tc.target, shaped, literal, tc.shaped, tc.literal)
			}
		})
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"conflicting RawPath", &http.Request{RequestURI: q, URL: &url.URL{Path: q, RawPath: s}}, true},
		{"missing RequestURI", &http.Request{URL: &url.URL{Path: q}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shaped, literal := selfPlanPurchaseRouteShape(tc.r)
			if shaped != tc.want || literal {
				t.Fatalf("shape=%v literal=%v", shaped, literal)
			}
		})
	}
}

func TestSelfPlanPurchaseRouteGuardLeavesOtherMethodsAndRejectsOffBeforeMux(t *testing.T) {
	q := selfPlanPurchaseQuotePath
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete, http.MethodOptions} {
		for _, target := range []string{q, q + "/extra", selfPlanPurchasePath} {
			t.Run(method+" "+target, func(t *testing.T) {
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					called = true
					w.WriteHeader(218)
				})
				guard := (&App{}).selfPlanPurchaseRouteGuard(next)
				w := httptest.NewRecorder()
				guard.ServeHTTP(w, httptest.NewRequest(method, target, nil))
				if !called || w.Code != 218 {
					t.Fatalf("method=%s target=%s called=%v status=%d", method, target, called, w.Code)
				}
			})
		}
	}
	for _, target := range []string{q, q + "/extra", selfPlanPurchasePath, "/self/api/v1/billing/./subscriptions"} {
		called := false
		guard := (&App{}).selfPlanPurchaseRouteGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, httptest.NewRequest(http.MethodPost, target, nil))
		if called || w.Code != http.StatusNotFound || w.Header().Get("Location") != "" || w.Header().Get("Allow") != "" {
			t.Fatalf("disabled target=%s called=%v status=%d headers=%v", target, called, w.Code, w.Header())
		}
	}
}

func TestSelfPlanPurchaseRouteBudget(t *testing.T) {
	q := selfPlanPurchaseQuotePath
	deep := func(depth int) string { return "%" + strings.Repeat("25", depth-1) + "2F" }
	for _, tc := range []struct {
		depth int
		want  bool
	}{{1, true}, {2, true}, {16, true}, {17, false}} {
		r := &http.Request{RequestURI: q + deep(tc.depth) + "extra", URL: &url.URL{Path: "/unrelated"}}
		shaped, literal := selfPlanPurchaseRouteShape(r)
		if shaped != tc.want || literal {
			t.Errorf("depth %d shape=%v literal=%v want %v/false", tc.depth, shaped, literal, tc.want)
		}
	}
	parent, stem := "/self/api/v1/billing/", "plan-purchase-quotes"
	padding := strings.Repeat("/", selfPlanPurchasePathBytes-len(parent)-len(stem)-1)
	proved := parent + padding + stem + "/"
	if len(proved) != selfPlanPurchasePathBytes {
		t.Fatal("bad budget fixture")
	}
	hidden := parent + strings.Repeat("/", selfPlanPurchasePathBytes-len(parent)-len(stem)) + stem
	for _, tc := range []struct {
		name, target string
		want         bool
	}{
		{"8192 proved", proved, true},
		{"8193 proved", proved + "x", true},
		{"8192 hidden complete", hidden, true},
		{"8193 hidden separator", hidden + "/", false},
		{"8193 hidden neighbor", hidden + "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RequestURI: tc.target, URL: &url.URL{Path: "/unrelated"}}
			got, _ := selfPlanPurchaseRouteShape(r)
			if got != tc.want {
				t.Errorf("len=%d shape=%v want=%v", len(tc.target), got, tc.want)
			}
		})
	}
	query := q + "?" + strings.Repeat("x", 1<<20)
	r := &http.Request{RequestURI: query, URL: &url.URL{Path: q, RawQuery: strings.Repeat("x", 1<<20)}}
	if shaped, literal := selfPlanPurchaseRouteShape(r); !shaped || !literal {
		t.Fatalf("long query shape=%v literal=%v", shaped, literal)
	}
	r = &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/" + strings.Repeat(" ", 1<<20), RawPath: "/" + strings.Repeat("x", 1<<20)}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	selfPlanPurchaseRouteShape(r)
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512<<10 {
		t.Fatalf("oversized Path/RawPath allocated %d bytes", allocated)
	}
}

func TestSelfPlanPurchaseRouteBudgetViews(t *testing.T) {
	parent, stem := "/self/api/v1/billing/", "plan-purchase-quotes"
	short := parent + strings.Repeat("/", selfPlanPurchasePathBytes-1-len(parent)-len(stem)) + stem
	hidden := parent + strings.Repeat("/", selfPlanPurchasePathBytes-len(parent)-len(stem)) + stem
	if len(short) != 8191 || len(hidden) != 8192 {
		t.Fatal("invalid boundary fixtures")
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"RequestURI 8191 end", &http.Request{RequestURI: short, URL: &url.URL{Path: "/unrelated"}}, true},
		{"RequestURI 8192 question", &http.Request{RequestURI: hidden + "?x", URL: &url.URL{Path: "/unrelated"}}, true},
		{"RequestURI 8193 path", &http.Request{RequestURI: hidden + "x", URL: &url.URL{Path: "/unrelated"}}, false},
		{"Path 8192 complete", &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: hidden}}, true},
		{"Path 8193 incomplete", &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: hidden + "x"}}, false},
		{"RawPath 8192 complete", &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/unrelated", RawPath: hidden}}, true},
		{"RawPath 8193 incomplete", &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/unrelated", RawPath: hidden + "x"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, literal := selfPlanPurchaseRouteShape(tc.r)
			if got != tc.want || literal {
				t.Fatalf("shape=%v literal=%v want=%v/false", got, literal, tc.want)
			}
		})
	}
	escapedBase := "/unrelated/"
	escapedSpaces := (selfPlanPurchasePathBytes - len(escapedBase)) / 3
	escapedCompletePath := escapedBase + strings.Repeat(" ", escapedSpaces) + strings.Repeat("x", selfPlanPurchasePathBytes-len(escapedBase)-3*escapedSpaces)
	for _, tc := range []struct {
		name, value  string
		wantComplete bool
	}{
		{"escaped 8192", escapedCompletePath, true},
		{"escaped 8193", escapedCompletePath + "x", false},
		{"inherited Path incomplete", strings.Repeat("/", 8193), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := selfPlanPurchaseEscapedView(boundedSelfPlanPurchasePath(tc.value))
			if len(view.value) != selfPlanPurchasePathBytes || view.complete != tc.wantComplete {
				t.Fatalf("escaped length=%d complete=%v want length=8192 complete=%v", len(view.value), view.complete, tc.wantComplete)
			}
		})
	}
	for _, tc := range []struct {
		name, rawPath string
		want          bool
	}{
		// A full URL.EscapedPath call would unescape over a million bytes;
		// the classifier must only inspect its 8192-byte RawPath prefix.
		{"oversized Q-family RawPath", selfPlanPurchaseQuotePath + "/" + strings.Repeat("%41", 1<<20), true},
		{"oversized Q-neighbor RawPath", selfPlanPurchaseQuotePath + "-other/" + strings.Repeat("%41", 1<<20), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/unrelated", RawPath: tc.rawPath}}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got, literal := selfPlanPurchaseRouteShape(r)
			runtime.ReadMemStats(&after)
			if got != tc.want || literal {
				t.Fatalf("shape=%v literal=%v want=%v/false", got, literal, tc.want)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512<<10 {
				t.Fatalf("oversized RawPath allocated %d bytes (full EscapedPath must not run)", allocated)
			}
		})
	}
}

type selfPlanPurchaseWireAuth struct {
	cookie          *http.Cookie
	csrf            string
	origin          string
	selfHeader      bool
	selfHeaderValue string
	duplicateSelf   bool
	duplicateOrigin bool
	bearer          string
}

type selfPlanPurchaseWireResult struct {
	status                                    int
	location, allow, cache, contentType, code string
	marker                                    bool
}

func selfPlanPurchaseWire(t *testing.T, serverURL, method, target string, auth selfPlanPurchaseWireAuth) selfPlanPurchaseWireResult {
	t.Helper()
	addr := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	body := ""
	if method == http.MethodPost {
		body = "{}"
	}
	var request strings.Builder
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, target, addr)
	if auth.cookie != nil {
		fmt.Fprintf(&request, "Cookie: %s=%s\r\n", auth.cookie.Name, auth.cookie.Value)
	}
	if auth.origin != "" {
		fmt.Fprintf(&request, "Origin: %s\r\n", auth.origin)
		if auth.duplicateOrigin {
			fmt.Fprintf(&request, "Origin: %s\r\n", auth.origin)
		}
	}
	if auth.selfHeader {
		value := auth.selfHeaderValue
		if value == "" {
			value = "1"
		}
		fmt.Fprintf(&request, "X-Self-Request: %s\r\n", value)
		if auth.duplicateSelf {
			fmt.Fprintf(&request, "X-Self-Request: %s\r\n", value)
		}
	}
	if auth.csrf != "" {
		fmt.Fprintf(&request, "X-CSRF-Token: %s\r\n", auth.csrf)
	}
	if auth.bearer != "" {
		fmt.Fprintf(&request, "Authorization: Bearer %s\r\n", auth.bearer)
	}
	fmt.Fprintf(&request, "Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := io.WriteString(conn, request.String()); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &parsed)
	return selfPlanPurchaseWireResult{
		status: response.StatusCode, location: response.Header.Get("Location"),
		allow: response.Header.Get("Allow"), cache: response.Header.Get("Cache-Control"),
		contentType: response.Header.Get("Content-Type"), code: parsed.Error.Code,
		marker: strings.Contains(string(payload), selfPlanPurchaseBoundaryMarker),
	}
}

func selfPlanPurchaseFieldEvidence(value string) string {
	if len(value) <= 160 {
		return fmt.Sprintf("%q", value)
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("len=%d sha256=%x first=%q last=%q", len(value), sum, value[:32], value[len(value)-32:])
}

func TestSelfPlanPurchaseRouteBoundaryTrueHTTPBudgetFields(t *testing.T) {
	f := newSelfPlanPurchaseBoundaryFixture(t, true)
	observed := make(chan struct{ requestURI, path, escaped, rawPath string }, 1)
	wireServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- struct{ requestURI, path, escaped, rawPath string }{r.RequestURI, r.URL.Path, r.URL.EscapedPath(), r.URL.RawPath}
		f.app.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(wireServer.Close)
	auth := selfPlanPurchaseWireAuth{cookie: f.cookie, csrf: f.csrf, origin: wireServer.URL, selfHeader: true}
	deep := func(depth int) string { return "%" + strings.Repeat("25", depth-1) + "2F" }
	parent, stem := "/self/api/v1/billing/", "plan-purchase-quotes"
	hidden := parent + strings.Repeat("/", selfPlanPurchasePathBytes-len(parent)-len(stem)) + stem
	if len(hidden) != selfPlanPurchasePathBytes {
		t.Fatal("invalid 8192-byte fixture")
	}
	financeBefore := selfPlanPurchaseFinancialCounts(t, f.app)
	for _, tc := range []struct {
		name, target  string
		purchaseOwned bool
	}{
		{"raw 16 unescapes", selfPlanPurchaseQuotePath + deep(16) + "extra", true},
		{"raw 17 but Path already decoded once", selfPlanPurchaseQuotePath + deep(17) + "extra", true},
		{"raw 18 exceeds every view", selfPlanPurchaseQuotePath + deep(18) + "extra", false},
		{"8192 then literal question", hidden + "?x", true},
		{"8193rd path byte", hidden + "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := selfPlanPurchaseWire(t, wireServer.URL, http.MethodPost, tc.target, auth)
			fields := <-observed
			pathTarget := strings.SplitN(tc.target, "?", 2)[0]
			decoded, err := url.PathUnescape(pathTarget)
			if err != nil {
				t.Fatal(err)
			}
			if fields.requestURI != tc.target || fields.path != decoded || fields.escaped != pathTarget ||
				(fields.rawPath != "" && fields.rawPath != pathTarget) {
				t.Fatalf("unexpected Go URL fields: RequestURI=%s Path=%s EscapedPath=%s RawPath=%s",
					selfPlanPurchaseFieldEvidence(fields.requestURI), selfPlanPurchaseFieldEvidence(fields.path),
					selfPlanPurchaseFieldEvidence(fields.escaped), selfPlanPurchaseFieldEvidence(fields.rawPath))
			}
			t.Logf("RequestURI=%s Path=%s EscapedPath=%s RawPath=%s first_status=%d code=%q Location=%q Allow=%q no_store=%v marker=%v",
				selfPlanPurchaseFieldEvidence(fields.requestURI), selfPlanPurchaseFieldEvidence(fields.path),
				selfPlanPurchaseFieldEvidence(fields.escaped), selfPlanPurchaseFieldEvidence(fields.rawPath),
				result.status, result.code, result.location, result.allow, result.cache == "no-store", result.marker)
			if result.cache != "no-store" {
				t.Fatalf("missing no-store: %+v", result)
			}
			if tc.purchaseOwned {
				if result.status != 400 || result.code != "invalid_request" || result.location != "" || result.allow != "" || result.marker {
					t.Fatalf("owned malformed POST: %+v", result)
				}
			} else if result.status == 400 && result.code == "invalid_request" {
				t.Fatalf("budget-exceeded neighbor captured: %+v", result)
			}
		})
	}
	if financeAfter := selfPlanPurchaseFinancialCounts(t, f.app); financeAfter != financeBefore {
		t.Fatalf("budget requests changed financial facts: before=%v after=%v", financeBefore, financeAfter)
	}
}

func selfPlanPurchaseFinancialCounts(t *testing.T, app *App) [4]int {
	t.Helper()
	var counts [4]int
	for i, table := range [...]string{"financial_commercial_operations", "financial_operations", "financial_entries", "financial_subscriptions"} {
		if err := app.store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestSelfPlanPurchaseRouteBoundaryWireAndOwnership(t *testing.T) {
	q, s := selfPlanPurchaseQuotePath, selfPlanPurchasePath
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("purchase-enabled=%v", enabled), func(t *testing.T) {
			f := newSelfPlanPurchaseBoundaryFixture(t, enabled)
			observed := make(chan struct{ requestURI, path, escaped, rawPath string }, 64)
			wireServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- struct{ requestURI, path, escaped, rawPath string }{r.RequestURI, r.URL.Path, r.URL.EscapedPath(), r.URL.RawPath}
				f.app.Handler().ServeHTTP(w, r)
			}))
			t.Cleanup(wireServer.Close)
			valid := selfPlanPurchaseWireAuth{cookie: f.cookie, csrf: f.csrf, origin: wireServer.URL, selfHeader: true}
			anonymous := selfPlanPurchaseWireAuth{origin: wireServer.URL, selfHeader: true, csrf: "invalid"}
			before := selfPlanPurchaseFinancialCounts(t, f.app)
			for _, tc := range []struct {
				name, target           string
				auth                   selfPlanPurchaseWireAuth
				want                   int
				path, escaped, rawPath string
			}{
				{"Q double slash anonymous", "/self/api/v1/billing//plan-purchase-quotes", anonymous, map[bool]int{false: 404, true: 401}[enabled], "/self/api/v1/billing//plan-purchase-quotes", "/self/api/v1/billing//plan-purchase-quotes", ""},
				{"S dot anonymous", "/self/api/v1/billing/./subscriptions", anonymous, map[bool]int{false: 404, true: 401}[enabled], "/self/api/v1/billing/./subscriptions", "/self/api/v1/billing/./subscriptions", ""},
				{"Q double slash valid", "/self/api/v1/billing//plan-purchase-quotes", valid, map[bool]int{false: 404, true: 400}[enabled], "/self/api/v1/billing//plan-purchase-quotes", "/self/api/v1/billing//plan-purchase-quotes", ""},
				{"S dot valid", "/self/api/v1/billing/./subscriptions", valid, map[bool]int{false: 404, true: 400}[enabled], "/self/api/v1/billing/./subscriptions", "/self/api/v1/billing/./subscriptions", ""},
				{"Q canonical valid", q, valid, map[bool]int{false: 404, true: 400}[enabled], q, q, ""},
				{"S canonical valid", s, valid, map[bool]int{false: 404, true: 400}[enabled], s, s, ""},
				{"Q encoded letter valid", "/self/api/v1/billing/%70lan-purchase-quotes", valid, map[bool]int{false: 404, true: 400}[enabled], q, "/self/api/v1/billing/%70lan-purchase-quotes", "/self/api/v1/billing/%70lan-purchase-quotes"},
				{"S encoded slash valid", "/self/api/v1/billing%2Fsubscriptions", valid, map[bool]int{false: 404, true: 400}[enabled], s, "/self/api/v1/billing%2Fsubscriptions", "/self/api/v1/billing%2Fsubscriptions"},
				{"S ID dotdot valid", s + "/id/..", valid, map[bool]int{false: 404, true: 400}[enabled], s + "/id/..", s + "/id/..", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := selfPlanPurchaseWire(t, wireServer.URL, http.MethodPost, tc.target, tc.auth)
					fields := <-observed
					if fields.requestURI != tc.target || fields.path != tc.path || fields.escaped != tc.escaped || fields.rawPath != tc.rawPath {
						t.Fatalf("Go HTTP request fields: got=%+v want target=%q Path=%q EscapedPath=%q RawPath=%q", fields, tc.target, tc.path, tc.escaped, tc.rawPath)
					}
					if got.status != tc.want || got.cache != "no-store" || got.location != "" || got.allow != "" || got.marker {
						t.Fatalf("target=%q result=%+v", tc.target, got)
					}
					if enabled && tc.want == 400 && got.code != "invalid_request" {
						t.Fatalf("target=%q code=%q", tc.target, got.code)
					}
				})
			}
			if after := selfPlanPurchaseFinancialCounts(t, f.app); after != before {
				t.Fatalf("rejected route changed financial facts: before=%v after=%v", before, after)
			}
			for _, target := range []string{s + "/id/cancel/../..", "/self/api/v1/billing/redemptions/../plan-purchase-quotes", q + "/../subscriptions/id/cancel"} {
				got := selfPlanPurchaseWire(t, wireServer.URL, http.MethodPost, target, valid)
				<-observed
				if got.status != 404 || got.location != "" || got.allow != "" || got.cache != "no-store" {
					t.Errorf("sibling target=%q result=%+v", target, got)
				}
			}
			for _, target := range []string{q + "-other", s + "x", s + "//id"} {
				got := selfPlanPurchaseWire(t, wireServer.URL, http.MethodPost, target, valid)
				<-observed
				if got.status == 400 && got.code == "invalid_request" {
					t.Errorf("neighbor captured: %q %+v", target, got)
				}
			}
		})
	}
}

func TestSelfPlanPurchaseRouteBoundaryWriteAuthorization(t *testing.T) {
	f := newSelfPlanPurchaseBoundaryFixture(t, true)
	q := selfPlanPurchaseQuotePath + "/extra"
	server := f.server.URL
	valid := selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: f.csrf, selfHeader: true}
	modelKey := createTestKey(t, server, f.id, "plan-route-model-key", f.adminCookie, f.adminCSRF)
	financeBefore := selfPlanPurchaseFinancialCounts(t, f.app)
	for _, tc := range []struct {
		name string
		auth selfPlanPurchaseWireAuth
		want int
		code string
	}{
		{"valid", valid, 400, "invalid_request"},
		{"anonymous", selfPlanPurchaseWireAuth{origin: server, csrf: f.csrf, selfHeader: true}, 401, "authentication_required"},
		{"admin cookie", selfPlanPurchaseWireAuth{cookie: f.adminCookie, origin: server, csrf: f.csrf, selfHeader: true}, 401, "authentication_required"},
		{"employee model key only", selfPlanPurchaseWireAuth{origin: server, csrf: f.csrf, selfHeader: true, bearer: modelKey.Key}, 401, "authentication_required"},
		{"wrong Origin", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: "http://wrong.invalid", csrf: f.csrf, selfHeader: true}, 403, "request_rejected"},
		{"duplicate Origin", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: f.csrf, selfHeader: true, duplicateOrigin: true}, 403, "request_rejected"},
		{"missing self header", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: f.csrf}, 403, "request_rejected"},
		{"wrong self header", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: f.csrf, selfHeader: true, selfHeaderValue: "wrong"}, 403, "request_rejected"},
		{"duplicate self header", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: f.csrf, selfHeader: true, duplicateSelf: true}, 403, "request_rejected"},
		{"missing CSRF", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, selfHeader: true}, 403, "csrf_rejected"},
		{"wrong CSRF", selfPlanPurchaseWireAuth{cookie: f.cookie, origin: server, csrf: "wrong", selfHeader: true}, 403, "csrf_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selfPlanPurchaseWire(t, server, http.MethodPost, q, tc.auth)
			if got.status != tc.want || got.code != tc.code || got.location != "" || got.allow != "" || got.cache != "no-store" {
				t.Fatalf("%s: %+v", tc.name, got)
			}
		})
	}
	for i := 0; i < 5; i++ {
		f.app.selfFailure("127.0.0.1", f.id)
	}
	limited := selfPlanPurchaseWire(t, server, http.MethodPost, q, valid)
	if limited.status != 429 || limited.code != "login_limited" {
		t.Fatalf("limit precedence: %+v", limited)
	}
	canonicalQuote := selfPlanPurchaseWire(t, server, http.MethodPost, selfPlanPurchaseQuotePath, valid)
	if canonicalQuote.status != 400 || canonicalQuote.code != "invalid_request" {
		t.Fatalf("canonical Q must not inherit malformed-only selfGate: %+v", canonicalQuote)
	}
	if _, err := f.app.store.db.Exec(`UPDATE employees SET status='disabled' WHERE id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	if got := selfPlanPurchaseWire(t, server, http.MethodPost, q, valid); got.status != 401 || got.code != "authentication_required" {
		t.Fatalf("disabled self session: %+v", got)
	}
	if _, err := f.app.store.db.Exec(`UPDATE employees SET status='active' WHERE id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE employee_id=?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), f.id); err != nil {
		t.Fatal(err)
	}
	if got := selfPlanPurchaseWire(t, server, http.MethodPost, q, valid); got.status != 401 || got.code != "authentication_required" {
		t.Fatalf("expired self session: %+v", got)
	}
	if _, err := f.app.store.db.Exec(`DELETE FROM employee_self_sessions WHERE employee_id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	if got := selfPlanPurchaseWire(t, server, http.MethodPost, q, valid); got.status != 401 || got.code != "authentication_required" {
		t.Fatalf("revoked self session: %+v", got)
	}
	if financeAfter := selfPlanPurchaseFinancialCounts(t, f.app); financeAfter != financeBefore {
		t.Fatalf("write-auth rejection changed financial facts: before=%v after=%v", financeBefore, financeAfter)
	}
}
