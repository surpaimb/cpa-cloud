package service

// Independently authored for docs/employee-self-redemption-route-boundary-contract.md.
// These tests use synthetic sessions and never contact a payment provider.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSelfRedemptionBoundaryGoParserRequestViews(t *testing.T) {
	// This separate, real-socket net/http server records path views only.
	// No sessions, credentials, request bodies, or secret-bearing headers exist.
	type views struct{ requestURI, path, escaped, raw string }
	seen := make(chan views, 5)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- views{r.RequestURI, r.URL.Path, r.URL.EscapedPath(), r.URL.RawPath}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	address := server.Listener.Addr().String()
	for _, test := range []struct{ target, path string }{
		{selfRedemptionPath + "%25other", selfRedemptionPath + "%other"},
		{selfRedemptionPath + "%25zz", selfRedemptionPath + "%zz"},
		{selfRedemptionPath + "%2525other", selfRedemptionPath + "%25other"},
		{selfRedemptionPath + "%252fextra", selfRedemptionPath + "%2fextra"},
		{"/self/api/v1/billing/%" + strings.Repeat("25", 18) + "72edemptions",
			"/self/api/v1/billing/%" + strings.Repeat("25", 17) + "72edemptions"},
	} {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", test.target, address); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		conn.Close()
		got := <-seen
		if response.StatusCode != http.StatusNoContent || got.requestURI != test.target || got.path != test.path {
			t.Fatalf("parser target=%q status=%d RequestURI=%q Path=%q", test.target,
				response.StatusCode, got.requestURI, got.path)
		}
		t.Logf("target=%q RequestURI=%q URL.Path=%q EscapedPath=%q RawPath=%q",
			test.target, got.requestURI, got.path, got.escaped, got.raw)
	}
}

func selfRedemptionBoundaryRequest(t *testing.T, serverURL, method, target string, cookie *http.Cookie, origin, csrf string, writeHeaders bool) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, serverURL+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if writeHeaders {
		req.Header.Set("X-Self-Request", "1")
		req.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func selfRedemptionBoundaryStatus(t *testing.T, response *http.Response, status int, allow, code string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != status || response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get("Location") != "" || response.Header.Get("Allow") != allow {
		t.Fatalf("status=%d cache=%q location=%q allow=%q; want status=%d allow=%q",
			response.StatusCode, response.Header.Get("Cache-Control"), response.Header.Get("Location"),
			response.Header.Get("Allow"), status, allow)
	}
	if code != "" && response.Request.Method != http.MethodHead {
		if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("content type=%q", got)
		}
		var value struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(response.Body).Decode(&value); err != nil || value.Error.Code != code {
			t.Fatalf("error code=%q decode=%v; want %q", value.Error.Code, err, code)
		}
	}
}

func TestSelfRedemptionBoundaryDisabledBeforeStorageOrWebFallback(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("synthetic-preview-password-123\n")); err != nil {
		t.Fatal(err)
	}
	webDir := filepath.Join(t.TempDir(), "web")
	if err := os.MkdirAll(webDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("route-boundary-web-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", WebDir: webDir,
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	// The shape guard must not need the database while the capability is off.
	if err := app.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{
		selfRedemptionPath, selfRedemptionPath + "/extra", selfRedemptionPath + "%2fextra",
		selfRedemptionPath + "%252fextra", selfRedemptionPath + "%25252fextra",
		selfRedemptionPath + "%255cextra", selfRedemptionPath + "%253fquery=1",
		selfRedemptionPath + "/%252e%252e",
		"/self/api/v1/billing//redemptions", "/self/api/v1/billing/Redemptions",
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodHead, http.MethodPut} {
			response := selfRedemptionBoundaryRequest(t, server.URL, method, target, nil, "", "", false)
			selfRedemptionBoundaryStatus(t, response, http.StatusNotFound, "", "")
		}
	}
}

func TestSelfRedemptionBoundaryEnabledAuthBeforeMalformedMethod(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	alias := selfRedemptionPath + "%252fextra"
	f.app.clearSelfFailures("127.0.0.1", f.id)
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodPost, alias,
		nil, f.server.URL, f.csrf, true), http.StatusUnauthorized, "", "authentication_required")
	f.app.clearSelfFailures("127.0.0.1", f.id)
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodPost, alias,
		f.cookie, "http://wrong.invalid", f.csrf, true), http.StatusForbidden, "", "request_rejected")
	f.app.clearSelfFailures("127.0.0.1", f.id)
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodPost, alias,
		f.cookie, f.server.URL, "wrong", true), http.StatusForbidden, "", "request_rejected")
	f.app.clearSelfFailures("127.0.0.1", f.id)
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodGet, alias,
		f.cookie, f.server.URL, "", false), http.StatusForbidden, "", "request_rejected")
	f.app.clearSelfFailures("127.0.0.1", f.id)
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodGet, selfRedemptionPath,
		f.cookie, f.server.URL, "", false), http.StatusMethodNotAllowed, http.MethodPost, "method_not_allowed")
	selfRedemptionBoundaryStatus(t, selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodGet, selfRedemptionPath,
		nil, f.server.URL, "", false), http.StatusUnauthorized, "", "authentication_required")
}

func TestSelfRedemptionBoundaryEnabledRejectsOnlyItsAliases(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	financialTables := []string{"financial_accounts", "financial_operations", "financial_entries",
		"financial_redemptions", "financial_commercial_operations", "financial_redemption_codes"}
	before := make([]int, len(financialTables))
	for i, table := range financialTables {
		before[i] = f.count(t, "SELECT COUNT(*) FROM "+table)
	}
	aliases := []string{
		selfRedemptionPath + "/extra", selfRedemptionPath + "%2fextra",
		selfRedemptionPath + "%252fextra", selfRedemptionPath + "%25252fextra",
		selfRedemptionPath + "%5cextra", selfRedemptionPath + "%255cextra",
		selfRedemptionPath + "%3fquery=1", selfRedemptionPath + "%253fquery=1",
		selfRedemptionPath + "%23fragment",
		selfRedemptionPath + "/%2e%2e", selfRedemptionPath + "/%252e%252e",
		"/self/api/v1/billing//redemptions", "/self/api/v1/billing/Redemptions",
		"/self/api/v1/billing/%72edemptions", "/self/api/v1/billing/%2572edemptions",
		selfRedemptionPath + "?unexpected=1", selfRedemptionPath + "?",
	}
	for _, target := range aliases {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodPut} {
			f.app.clearSelfFailures("127.0.0.1", f.id)
			response := selfRedemptionBoundaryRequest(t, f.server.URL, method, target,
				f.cookie, f.server.URL, f.csrf, true)
			selfRedemptionBoundaryStatus(t, response, http.StatusBadRequest, "", "invalid_request")
		}
	}
	for _, target := range []string{
		"/self/api/v1/billing/redemption-credits", "/self/api/v1/billing/redemptions-other",
		"/self/api/v1/billing/redemptions%252dother", "/self/api/v1/billing/balance",
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if selfRedemptionShapedRequest(req) {
			t.Fatalf("sibling claimed by redemption guard: %q", target)
		}
	}
	for i, table := range financialTables {
		if after := f.count(t, "SELECT COUNT(*) FROM "+table); after != before[i] {
			t.Fatalf("shape probe wrote %s: before=%d after=%d", table, before[i], after)
		}
	}
}

func TestSelfRedemptionBoundaryMalformedFailuresReachExistingLimiter(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.app.clearSelfFailures("127.0.0.1", f.id)
	alias := selfRedemptionPath + "%252fextra"
	for i := 0; i < 6; i++ {
		wantStatus, wantCode := http.StatusBadRequest, "invalid_request"
		if i == 5 {
			wantStatus, wantCode = http.StatusTooManyRequests, "login_limited"
		}
		response := selfRedemptionBoundaryRequest(t, f.server.URL, http.MethodPost, alias,
			f.cookie, f.server.URL, f.csrf, true)
		selfRedemptionBoundaryStatus(t, response, wantStatus, "", wantCode)
	}
}

func TestSelfRedemptionBoundaryRequestViewsAreRejectOnly(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.RequestURI = selfRedemptionPath + "%252fextra" },
		func(r *http.Request) { r.URL.Path = selfRedemptionPath + "%2fextra" },
		func(r *http.Request) { r.URL.RawPath = selfRedemptionPath + "%252fextra" },
	} {
		r := httptest.NewRequest(http.MethodPost, "/other", nil)
		mutate(r)
		if !selfRedemptionShapedRequest(r) {
			t.Fatal("encoded view not recognized")
		}
	}
	deep := selfRedemptionPath + "%" + strings.Repeat("25", selfRedemptionShapeMaxRounds+1) + "2fextra"
	if !selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, deep, nil)) {
		t.Fatal("decode budget must fail closed for anchored route")
	}
	app := &App{cfg: Config{EmployeeSelfRedemptionEnabled: true}}
	for _, target := range []string{selfRedemptionPath, "/self/api/v1/billing/redemption-credits"} {
		r := httptest.NewRequest(http.MethodPost, target, nil)
		r.SetPathValue("id", "untouched")
		beforeURI, beforePath, beforeRaw := r.RequestURI, r.URL.Path, r.URL.RawPath
		passed := false
		app.selfRedemptionRouteGuard(http.HandlerFunc(func(_ http.ResponseWriter, got *http.Request) {
			passed = true
			if got.RequestURI != beforeURI || got.URL.Path != beforePath || got.URL.RawPath != beforeRaw ||
				got.PathValue("id") != "untouched" {
				t.Fatal("route guard rewrote a pass-through request")
			}
		})).ServeHTTP(httptest.NewRecorder(), r)
		if !passed {
			t.Fatalf("pass-through route rejected: %q", target)
		}
	}
}

func TestSelfRedemptionBoundaryOverBudgetEncodedLetterKeepsFamilyAndNeighborSeparate(t *testing.T) {
	// An encoded first route letter may stay hidden when the fixed decode
	// budget expires. The same encoding on a sibling must not be claimed.
	prefix := "/self/api/v1/billing/%" + strings.Repeat("25", selfRedemptionShapeMaxRounds+2) + "72edemptions"
	for _, test := range []struct {
		name   string
		target string
		want   bool
	}{
		{name: "family", target: prefix, want: true},
		{name: "neighbor", target: prefix + "-other", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, test.target, nil))
			if got != test.want {
				t.Fatalf("over-budget shape claimed=%t, want=%t for %q", got, test.want, test.target)
			}
		})
	}
}

func TestSelfRedemptionBoundaryByteCapLeavesHiddenSeparatorAndNeighborUnclaimed(t *testing.T) {
	// The first distinguishing byte is beyond every 8192-byte request view.
	// Equal-length paths ultimately end in a family separator or a sibling '-'.
	prefix := selfRedemptionPath + "%" + strings.Repeat("25", selfRedemptionShapeMaxBytes/2+40)
	family := httptest.NewRequest(http.MethodPost, prefix+"2fother", nil)
	neighbor := httptest.NewRequest(http.MethodPost, prefix+"2dother", nil)
	for _, view := range [][2]string{
		{family.RequestURI, neighbor.RequestURI},
		{family.URL.Path, neighbor.URL.Path},
		{family.URL.EscapedPath(), neighbor.URL.EscapedPath()},
	} {
		if len(view[0]) <= selfRedemptionShapeMaxBytes || len(view[1]) != len(view[0]) ||
			view[0][:selfRedemptionShapeMaxBytes] != view[1][:selfRedemptionShapeMaxBytes] {
			t.Fatal("constructed paths are distinguishable within the byte budget")
		}
	}
	if selfRedemptionShapedRequest(family) {
		t.Fatal("deep family separator beyond the byte cap was claimed")
	}
	if selfRedemptionShapedRequest(neighbor) {
		t.Fatal("deep sibling separator was claimed by family guard")
	}
}

func TestSelfRedemptionBoundaryResidualSyntaxDepthAndNeighbors(t *testing.T) {
	parent := "/self/api/v1/billing/"
	for _, depth := range []int{16, 17, 18} {
		encoded := "%" + strings.Repeat("25", depth)
		for _, test := range []struct {
			name   string
			target string
			want   bool
		}{
			{name: "first-letter", target: parent + encoded + "72edemptions", want: true},
			{name: "multi-letter-case", target: parent + encoded + "52" + encoded + "65demptions", want: true},
			{name: "encoded-slash", target: "/self/api/v1/billing" + encoded + "2fRedemptions", want: true},
			{name: "encoded-backslash", target: "/self/api/v1/billing" + encoded + "5cRedemptions", want: true},
			{name: "encoded-question", target: selfRedemptionPath + encoded + "3fquery", want: true},
			{name: "dot-and-letter", target: parent + "./" + encoded + "72edemptions", want: true},
			{name: "cleaned-parent-and-letter", target: parent + "other/../" + encoded + "72edemptions", want: true},
			{name: "literal-hyphen-neighbor", target: parent + encoded + "72edemptions-other", want: false},
			{name: "encoded-hyphen-neighbor", target: selfRedemptionPath + encoded + "2dother", want: false},
			{name: "letter-neighbor", target: parent + encoded + "72edemptionsx", want: false},
			{name: "credits-neighbor", target: parent + encoded + "72edemption-credits", want: false},
		} {
			t.Run(strconv.Itoa(depth)+"/"+test.name, func(t *testing.T) {
				got := selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, test.target, nil))
				if got != test.want {
					t.Fatalf("shape claimed=%t, want=%t for %q", got, test.want, test.target)
				}
			})
		}
	}
	for _, test := range []struct {
		target string
		want   bool
	}{
		{selfRedemptionPath + "%252dother", false},
		{selfRedemptionPath + "%25other", false},
		{selfRedemptionPath + "%25zz", false},
		{selfRedemptionPath + "%2525other", false},
		{"/self/api/v1/billing/redemption-credits", false},
	} {
		got := selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, test.target, nil))
		if got != test.want {
			t.Fatalf("shape claimed=%t, want=%t for %q", got, test.want, test.target)
		}
	}
}

func TestSelfRedemptionBoundaryByteCapRequiresVisibleBoundary(t *testing.T) {
	parent := "/self/api/v1/billing/"
	padding := strings.Repeat("/", selfRedemptionShapeMaxBytes-len(parent)-len("redemptions")-1)
	family := parent + padding + "redemptions/"
	neighbor := parent + padding + "redemptionsx"
	if len(family) != selfRedemptionShapeMaxBytes || len(neighbor) != len(family) {
		t.Fatal("boundary byte is not at the cap")
	}
	for _, test := range []struct {
		target string
		want   bool
	}{
		{family, true}, {family + "extra", true}, {neighbor, false},
	} {
		got := selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, test.target, nil))
		if got != test.want {
			t.Fatalf("shape claimed=%t, want=%t at cap", got, test.want)
		}
	}
	// An earlier, unrelated redemptions/ segment must not make a later
	// truncated redemptionsx look like a completed family segment.
	cleanHead := "/self/api/v1/billing/foo/redemptions/../../"
	cleanPadding := strings.Repeat("/", selfRedemptionShapeMaxBytes-len(cleanHead)-len("redemptions")-1)
	cleanPrefix := cleanHead + cleanPadding + "redemptions"
	if len(cleanPrefix)+1 != selfRedemptionShapeMaxBytes {
		t.Fatal("cleaned-path boundary byte is not at the cap")
	}
	if selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, cleanPrefix+"x", nil)) {
		t.Fatal("unrelated earlier segment concealed an over-limit neighbor")
	}
	if !selfRedemptionShapedRequest(httptest.NewRequest(http.MethodPost, cleanPrefix+"/", nil)) {
		t.Fatal("visible cleaned-path family boundary was missed")
	}
}
