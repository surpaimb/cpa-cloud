// Independently authored for docs/employee-self-estimated-cost-route-boundary-contract.md.
package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
)

func TestSelfEstimatedCostRouteShapeBudgetAndNeighbors(t *testing.T) {
	p := selfEstimatedCostPath
	deep := func(hex string, depth int) string { return "%" + strings.Repeat("25", depth-1) + hex }
	cases := []struct {
		name    string
		path    string
		shaped  bool
		literal bool
	}{
		{"canonical", p, true, true},
		{"bare query", p + "?", true, true},
		{"literal neighbor", p + "-other", false, false},
		{"encoded hyphen neighbor", p + "%2Dother", false, false},
		{"double encoded hyphen neighbor", p + "%252Dother", false, false},
		{"percent neighbor", p + "%25other", false, false},
		{"suffix x neighbor", p + "x", false, false},
		{"suffix underscore neighbor", p + "_other", false, false},
		{"literal dot neighbor", p + ".extra", false, false},
		{"literal semicolon neighbor", p + ";extra", false, false},
		{"unrelated self route", "/self/api/v1/usage/summary", false, false},
		{"child", p + "/extra", true, false},
		{"trailing slash", p + "/", true, false},
		{"duplicate slash", "/self//api/v1/usage/estimated-cost-summary", true, false},
		{"backslash", "/self/api/v1/usage\\estimated-cost-summary", true, false},
		{"dot segment before", "/self/api/v1/usage/./estimated-cost-summary", true, false},
		{"dot segment after", p + "/../other", true, false},
		{"case variant", "/SELF/api/v1/usage/ESTIMATED-COST-SUMMARY", true, false},
		{"encoded route letter", "/self/api/v1/usage/%65stimated-cost-summary", true, false},
		{"encoded slash", p + "%2Fextra", true, false},
		{"double encoded slash", p + "%252Fextra", true, false},
		{"encoded question tail", p + "%3Fx", true, false},
		{"encoded semicolon tail", p + "%3Bextra", true, false},
		{"encoded dot tail", p + "%2Eextra", true, false},
		{"double encoded dot tail", p + "%252Eextra", true, false},
	}
	for _, depth := range []int{16, 17, 18} {
		cases = append(cases,
			struct {
				name, path      string
				shaped, literal bool
			}{fmt.Sprintf("slash depth %d", depth), p + deep("2F", depth) + "extra", true, false},
			struct {
				name, path      string
				shaped, literal bool
			}{fmt.Sprintf("letter depth %d", depth), "/self/api/v1/usage/" + deep("65", depth) + "stimated-cost-summary", true, false},
			struct {
				name, path      string
				shaped, literal bool
			}{fmt.Sprintf("hyphen neighbor depth %d", depth), p + deep("2D", depth) + "other", false, false},
		)
	}
	deepSlash := deep("2F", 18)
	deepBackslash := deep("5C", 18)
	cases = append(cases,
		struct {
			name, path      string
			shaped, literal bool
		}{"depth 18 duplicate separators family", "/self" + deepSlash + deepSlash + "api/v1/usage/estimated-cost-summary", true, false},
		struct {
			name, path      string
			shaped, literal bool
		}{"depth 18 slash backslash family", "/self" + deepSlash + deepBackslash + "api/v1/usage/estimated-cost-summary", true, false},
		struct {
			name, path      string
			shaped, literal bool
		}{"depth 18 duplicate separators neighbor", "/self" + deepSlash + deepSlash + "api/v1/usage/estimated-cost-summary-other", false, false},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1"+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.RequestURI = tc.path
			gotShape, gotLiteral := selfEstimatedCostRouteShape(r)
			if gotShape != tc.shaped || gotLiteral != tc.literal {
				t.Errorf("path=%q shape=%v literal=%v; want %v/%v", tc.path, gotShape, gotLiteral, tc.shaped, tc.literal)
			}
		})
	}
	parent, stem := "/self/api/v1/usage/", "estimated-cost-summary"
	padding := strings.Repeat("/", selfEstimatedCostShapeBytes-len(parent)-len(stem)-1)
	proved := parent + padding + stem + "/"
	if len(proved) != selfEstimatedCostShapeBytes {
		t.Fatal("boundary fixture length")
	}
	hidden := parent + strings.Repeat("/", selfEstimatedCostShapeBytes-len(parent)-len(stem)) + stem
	if len(hidden) != selfEstimatedCostShapeBytes {
		t.Fatal("hidden boundary fixture length")
	}
	for _, tc := range []struct {
		name, view string
		want       bool
	}{
		{"exact budget", proved, true},
		{"over budget with proved separator", proved + "extra", true},
		{"neighbor at budget", parent + padding + stem + "x", false},
		{"unresolved family beyond budget", hidden + "/extra", false},
		{"unresolved neighbor beyond budget", hidden + "-other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfEstimatedCostShapedPath(tc.view); got != tc.want {
				t.Errorf("len=%d shape=%v want=%v", len(tc.view), got, tc.want)
			}
		})
	}
}

func TestSelfEstimatedCostShapeDoesNotReadPastSourceBudget(t *testing.T) {
	prefix := strings.Repeat("/", selfEstimatedCostShapeBytes-len(selfEstimatedCostPath)) + selfEstimatedCostPath
	if len(prefix) != selfEstimatedCostShapeBytes {
		t.Fatal("request target boundary fixture")
	}
	// The query marker is outside the bounded source view. An inconsistent
	// in-process URL isolates RequestURI classification from the other views.
	r := &http.Request{RequestURI: prefix + "?x", URL: &url.URL{Path: "/unrelated"}}
	if shaped, _ := selfEstimatedCostRouteShape(r); shaped {
		t.Fatal("request-target query beyond byte budget was scanned as a complete path")
	}
}

func TestSelfEstimatedCostShapeDoesNotBuildHugeEscapedPath(t *testing.T) {
	// A huge URL.Path requiring escaping must not make the classifier build a
	// huge EscapedPath. Its bounded work should remain far below input size.
	r := &http.Request{RequestURI: "/unrelated", URL: &url.URL{Path: "/" + strings.Repeat(" ", 1<<20)}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	selfEstimatedCostRouteShape(r)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("bounded shape allocation for 1 MiB path: %d bytes", allocated)
	if allocated > 512<<10 {
		t.Fatalf("classifier allocated %d bytes for a 1 MiB source; want <= 512 KiB", allocated)
	}
}

type estimatedCostWireResponse struct {
	status                                          int
	contentType, cache, location, allow, code, body string
}

// A real TCP exchange reads only the first response. No client follows redirects.
func estimatedCostWire(t *testing.T, serverURL, method, target string, cookie *http.Cookie, origin, bearer, body string) estimatedCostWireResponse {
	t.Helper()
	addr := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var request strings.Builder
	fmt.Fprintf(&request, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", method, target, addr)
	if cookie != nil {
		fmt.Fprintf(&request, "Cookie: %s=%s\r\n", cookie.Name, cookie.Value)
	}
	if origin != "" {
		fmt.Fprintf(&request, "Origin: %s\r\n", origin)
	}
	if bearer != "" {
		fmt.Fprintf(&request, "Authorization: Bearer %s\r\n", bearer)
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
	return estimatedCostWireResponse{status: response.StatusCode, contentType: response.Header.Get("Content-Type"),
		cache: response.Header.Get("Cache-Control"), location: response.Header.Get("Location"),
		allow: response.Header.Get("Allow"), code: parsed.Error.Code, body: string(payload)}
}

func TestSelfEstimatedCostRouteBoundaryWire(t *testing.T) {
	p := selfEstimatedCostPath
	off := newEstimatedCostFixtureEnabled(t, false)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut} {
		for _, target := range []string{p, p + "/extra", p + "%3Bextra"} {
			got := estimatedCostWire(t, off.server.URL, method, target, nil, "", "", "")
			if got.status != 404 || got.cache != "no-store" || got.location != "" || got.allow != "" || strings.Contains(got.body, "SPA fallback marker") {
				t.Errorf("off %s %s: %+v", method, target, got)
			}
		}
	}
	f := newEstimatedCostFixture(t)
	var requestsBefore, entriesBefore int64
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM accounting_requests`).Scan(&requestsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM financial_entries`).Scan(&entriesBefore); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name, method, target string
		cookie               *http.Cookie
		origin, bearer, body string
		status               int
		code, allow          string
		json                 bool
	}{
		{"canonical", "GET", p, f.cookie, "", "", "", 200, "", "", true},
		{"bare query", "GET", p + "?", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"GET body", "GET", p, f.cookie, "", "", "{}", 400, "invalid_request", "", true},
		{"canonical nonGET", "POST", p, f.cookie, "", "", "", 405, "method_not_allowed", "GET", true},
		{"canonical HEAD", "HEAD", p, f.cookie, "", "", "", 405, "", "GET", true},
		{"anonymous shape", "GET", p + "/extra", nil, "", "", "", 401, "authentication_required", "", true},
		{"admin cookie shape", "GET", p + "/extra", f.adminCookie, "", "", "", 401, "authentication_required", "", true},
		{"Bearer without self cookie", "GET", p + "/extra", nil, "", "synthetic-key-not-valid", "", 401, "authentication_required", "", true},
		{"wrong Origin before cookie", "GET", p + "/extra", nil, "http://wrong.invalid", "", "", 403, "request_rejected", "", true},
		{"encoded hyphen neighbor", "GET", p + "%2Dother", f.cookie, "", "", "", 404, "", "", false},
		{"literal hyphen neighbor", "GET", p + "-other", f.cookie, "", "", "", 404, "", "", false},
		{"percent neighbor", "GET", p + "%25other", f.cookie, "", "", "", 404, "", "", false},
		{"child", "GET", p + "/extra", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"encoded semicolon", "GET", p + "%3Bextra", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"encoded dot", "GET", p + "%2Eextra", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"encoded question", "GET", p + "%3Fx", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"cost child POST still uses read gate", "POST", p + "/extra", f.cookie, "", "", "", 400, "invalid_request", "", true},
		{"redemption crossover POST disabled sibling", "POST", p + "/../../billing/redemptions", f.cookie, "", "", "", 404, "", "", false},
		{"sibling usage summary", "GET", "/self/api/v1/usage/summary", f.cookie, "", "", "", 200, "", "", true},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			got := estimatedCostWire(t, f.server.URL, tc.method, tc.target, tc.cookie, tc.origin, tc.bearer, tc.body)
			if got.status != tc.status || got.code != tc.code || got.allow != tc.allow || got.cache != "no-store" || got.location != "" ||
				strings.HasPrefix(got.contentType, "application/json") != tc.json || strings.Contains(got.body, "SPA fallback marker") {
				t.Errorf("%s %s: %+v", tc.method, tc.target, got)
			}
		})
	}
	var requestsAfter, entriesAfter int64
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM accounting_requests`).Scan(&requestsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM financial_entries`).Scan(&entriesAfter); err != nil {
		t.Fatal(err)
	}
	if requestsAfter != requestsBefore || entriesAfter != entriesBefore {
		t.Fatalf("read-only route changed accounting/financial counts: requests %d->%d entries %d->%d", requestsBefore, requestsAfter, entriesBefore, entriesAfter)
	}
}

func TestSelfEstimatedCostFirstViewInnerOwnership(t *testing.T) {
	p := selfEstimatedCostPath
	f := newEstimatedCostFixture(t) // Cost on; every inner sibling remains off.
	var requestsBefore, entriesBefore int64
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM accounting_requests`).Scan(&requestsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM financial_entries`).Scan(&entriesBefore); err != nil {
		t.Fatal(err)
	}
	paths := []struct {
		name, target string
		status       int
		code         string
	}{
		{"topup", p + "/../../billing/topup-credits", 404, ""},
		{"admin adjustments", p + "/../../billing/admin-adjustments", 404, ""},
		{"redemption credits", p + "/../../billing/redemption-credits", 404, ""},
		{"entry classifications", p + "/../../billing/entry-classifications", 404, ""},
		{"redemptions", p + "/../../billing/redemptions", 404, ""},
		{"reverse retained segment", "/self/api/v1/billing/redemptions/../../usage/estimated-cost-summary", 404, ""},
		{"outer cancel", p + "/../../billing/subscriptions/id/cancel", 404, ""},
		{"upper case", "/SELF/API/V1/USAGE/ESTIMATED-COST-SUMMARY/../../BILLING/REDEMPTIONS", 404, ""},
		{"duplicate slash", p + "/..//../billing//redemptions", 404, ""},
		{"redemption backslash", p + `/..\..\billing\redemptions`, 404, ""},
		{"redemption encoded question", p + "/../../billing/redemptions%3Fx", 404, ""},
		{"redemption encoded hash", p + "/../../billing/redemptions%23x", 404, ""},
		{"classification backslash is not a delimiter", p + `/..\..\billing\entry-classifications`, 400, "invalid_request"},
		{"cost-only child", p + "/extra", 400, "invalid_request"},
		{"literal neighbor", p + "-other", 404, ""},
		{"encoded neighbor", p + "%2Dother", 404, ""},
	}
	for _, family := range []struct{ name, route string }{
		{"topup", "topup-credits"},
		{"adjustment", "admin-adjustments"},
		{"redemption credit", "redemption-credits"},
		{"classification", "entry-classifications"},
	} {
		paths = append(paths,
			struct {
				name, target string
				status       int
				code         string
			}{family.name + " uppercase", strings.ToUpper(p + "/../../billing/" + family.route), 404, ""},
			struct {
				name, target string
				status       int
				code         string
			}{family.name + " duplicate slash", p + "/..//../billing//" + family.route, 404, ""},
		)
		if family.name != "classification" {
			paths = append(paths, struct {
				name, target string
				status       int
				code         string
			}{family.name + " backslash", p + `/..\..\billing\` + family.route, 404, ""})
		}
	}
	for _, depth := range []int{17, 18} {
		wrappedDot := "%" + strings.Repeat("25", depth-1) + "2E"
		paths = append(paths, struct {
			name, target string
			status       int
			code         string
		}{fmt.Sprintf("inner only after %d decodes", depth), p + "/" + wrappedDot + wrappedDot + "/" + wrappedDot + wrappedDot + "/billing/redemptions", 400, "invalid_request"})
	}
	near := p + "/" + strings.Repeat("/", selfEstimatedCostShapeBytes-len(p)-len("/../../billing/redemptions")) + "../../billing/redemptions"
	if len(near) != selfEstimatedCostShapeBytes {
		t.Fatalf("near-budget fixture length=%d", len(near))
	}
	paths = append(paths,
		struct {
			name, target string
			status       int
			code         string
		}{"inner at byte budget", near, 404, ""},
		struct {
			name, target string
			status       int
			code         string
		}{"inner past byte budget", "/" + near, 400, "invalid_request"},
	)
	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			got := estimatedCostWire(t, f.server.URL, "GET", tc.target, f.cookie, "", "", "")
			if got.status != tc.status || got.code != tc.code || got.cache != "no-store" || got.location != "" || strings.Contains(got.body, "SPA fallback marker") {
				t.Errorf("%s: status=%d code=%q cache=%q location=%q marker=%v", tc.name, got.status, got.code, got.cache, got.location, strings.Contains(got.body, "SPA fallback marker"))
			}
		})
	}
	for _, tc := range []struct {
		name, target string
		status       int
		code         string
	}{
		{"cost anonymous", p + "/extra", 401, "authentication_required"},
		{"cost wrong origin", p + "/extra", 403, "request_rejected"},
		{"inner anonymous stays feature-off", p + "/../../billing/redemptions", 404, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := ""
			if tc.name == "cost wrong origin" {
				origin = "http://wrong.invalid"
			}
			got := estimatedCostWire(t, f.server.URL, "GET", tc.target, nil, origin, "", "")
			if got.status != tc.status || got.code != tc.code || got.cache != "no-store" || got.location != "" {
				t.Errorf("%s: %+v", tc.name, got)
			}
		})
	}
	var requestsAfter, entriesAfter int64
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM accounting_requests`).Scan(&requestsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT count(*) FROM financial_entries`).Scan(&entriesAfter); err != nil {
		t.Fatal(err)
	}
	if requestsAfter != requestsBefore || entriesAfter != entriesBefore {
		t.Fatalf("cross-family reads changed accounting/financial counts: requests %d->%d entries %d->%d", requestsBefore, requestsAfter, entriesBefore, entriesAfter)
	}
}

func TestSelfEstimatedCostInnerDeclineIsTerminal(t *testing.T) {
	f := newEstimatedCostFixture(t)
	f.app.cfg.EmployeeSelfTopupCreditHistoryEnabled = true
	r := httptest.NewRequest(http.MethodGet, selfEstimatedCostPath+"/extra", nil)
	r.URL.Path = selfTopupCreditPath // Deliberately differs from the raw request target.
	r.URL.RawPath = ""
	r.AddCookie(f.cookie)
	r.SetPathValue("id", "synthetic-path-value")
	beforeURI, beforePath, beforeRawPath, beforeQuery, beforeValue := r.RequestURI, r.URL.Path, r.URL.RawPath, r.URL.RawQuery, r.PathValue("id")
	var fellThrough bool
	guard := f.app.selfEstimatedCostRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fellThrough = true
		w.WriteHeader(http.StatusTeapot)
	}))
	w := httptest.NewRecorder()
	guard.ServeHTTP(w, r)
	if fellThrough || w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("terminal fallback failed: status=%d fellThrough=%v body=%s", w.Code, fellThrough, w.Body.String())
	}
	if r.RequestURI != beforeURI || r.URL.Path != beforePath || r.URL.RawPath != beforeRawPath || r.URL.RawQuery != beforeQuery || r.PathValue("id") != beforeValue {
		t.Fatal("route-boundary guard mutated request target or PathValue")
	}
}
