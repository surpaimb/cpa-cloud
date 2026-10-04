// Independently authored for docs/employee-self-wallet-balance-route-boundary-contract.md.
package service

import (
	"bufio"
	"bytes"
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
	"strings"
	"testing"
	"time"
)

func balanceShapeRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	parsed, err := url.ParseRequestURI(target)
	if err != nil {
		t.Fatalf("parse %q: %v", target, err)
	}
	return &http.Request{Method: http.MethodGet, RequestURI: target, URL: parsed}
}

func TestSelfWalletBalanceRouteShape(t *testing.T) {
	b := selfWalletBalanceRoutePath
	for _, tc := range []struct {
		name, target   string
		owned, literal bool
	}{
		{"literal", b, true, true},
		{"literal query", b + "?currency=USD", true, true},
		{"double slash", "/self/api/v1/billing//balance?currency=USD", true, false},
		{"dot", "/self/api/v1/billing/./balance", true, false},
		{"uppercase", "/SELF/API/V1/BILLING/BALANCE", true, false},
		{"encoded letter", "/self/api/v1/billing/%62alance", true, false},
		{"encoded slash", "/self/api/v1/billing%2Fbalance", true, false},
		{"encoded child", b + "%2Fextra", true, false},
		{"child", b + "/extra", true, false},
		{"return to balance", b + "/../balance", true, false},
		{"absolute", "http://example.invalid" + b, true, false},
		{"absolute uppercase scheme", "HTTP://example.invalid" + b, true, false},
		{"absolute userinfo", "http://synthetic@example.invalid" + b, true, false},
		{"absolute empty host", "http://" + b, true, false},
		{"scheme single slash", "http:" + b, true, false},
		{"custom scheme single slash", "custom:" + b, true, false},
		{"sibling entries", b + "/../entries", false, false},
		{"sibling plans", b + "/../plans", false, false},
		{"neighbor", b + "-other", false, false},
		{"encoded neighbor", b + "%2Dother", false, false},
		{"prefix neighbor", b + "x", false, false},
		{"encoded question", b + "%3Ffoo", false, false},
		{"encoded hash", b + "%23foo", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := balanceShapeRequest(t, tc.target)
			if tc.name == "absolute uppercase scheme" {
				r.Host = "different.invalid"
			}
			owned, literal := selfWalletBalanceRouteShape(r)
			if owned != tc.owned || literal != tc.literal {
				t.Fatalf("target=%q got %v/%v want %v/%v", tc.target, owned, literal, tc.owned, tc.literal)
			}
		})
	}
}

func TestSelfWalletBalanceRouteShapeBudgetAndConsistency(t *testing.T) {
	b := selfWalletBalanceRoutePath
	deep := func(n int) string { return b + "%" + strings.Repeat("25", n-1) + "2Fextra" }
	nearStem := "/self/api/v1/billing/" + strings.Repeat("/", 8192-len("/self/api/v1/billing/")-len("balance")) + "balance"
	if len(nearStem) != 8192 {
		t.Fatal("incorrect near-boundary fixture")
	}
	prefix1024 := "http://" + strings.Repeat("a", 1024-len("http://"))
	prefix1025 := prefix1024 + "a"
	for _, tc := range []struct {
		name, target string
		owned        bool
	}{
		{"8191 path", b + "/" + strings.Repeat("x", 8191-len(b)-1), true},
		{"8192 path", b + "/" + strings.Repeat("x", 8192-len(b)-1), true},
		{"8192 ending in balance", nearStem, true},
		{"8193 byte is question", nearStem + "?" + strings.Repeat("x", 1<<20), true},
		{"8193 extends balance to neighbor", nearStem + "x", false},
		{"8193 path", b + "/" + strings.Repeat("x", 8193-len(b)-1), false},
		{"long query", b + "?" + strings.Repeat("x", 1<<20), true},
		{"sixteen decode", deep(16), true},
		{"seventeen decode", deep(17), false},
		{"non-origin prefix 1024", prefix1024 + b, true},
		{"non-origin prefix 1025", prefix1025 + b, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, _ := selfWalletBalanceRouteShape(balanceShapeRequest(t, tc.target))
			if owned != tc.owned {
				t.Fatalf("owned=%v want %v", owned, tc.owned)
			}
		})
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
	}{
		{"missing target", &http.Request{URL: &url.URL{Path: b}}},
		{"contradictory Path", &http.Request{RequestURI: b, URL: &url.URL{Path: "/unrelated"}}},
		{"contradictory RawPath", &http.Request{RequestURI: b, URL: &url.URL{Path: b, RawPath: b + "x"}}},
		{"opaque", &http.Request{RequestURI: "http:opaque", URL: &url.URL{Scheme: "http", Opaque: "opaque", Path: b}}},
		{"absolute contradictory host", &http.Request{RequestURI: "http://one.invalid" + b, URL: &url.URL{Scheme: "http", Host: "two.invalid", Path: b}}},
		{"long RawPath", &http.Request{RequestURI: b, URL: &url.URL{Path: b, RawPath: b + strings.Repeat("x", 8192)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if owned, literal := selfWalletBalanceRouteShape(tc.r); owned || literal {
				t.Fatalf("contradictory view got %v/%v", owned, literal)
			}
		})
	}
	request := balanceShapeRequest(t, "/self/api/v1/billing/%62alance?currency=USD")
	request.SetPathValue("unrelated", "sentinel")
	before := [...]string{request.RequestURI, request.URL.Path, request.URL.RawPath, request.URL.EscapedPath(), request.PathValue("unrelated")}
	selfWalletBalanceRouteShape(request)
	after := [...]string{request.RequestURI, request.URL.Path, request.URL.RawPath, request.URL.EscapedPath(), request.PathValue("unrelated")}
	if before != after {
		t.Fatalf("classification mutated request: before=%q after=%q", before, after)
	}
}

func TestSelfWalletBalanceRouteGuardMethodsAndOff(t *testing.T) {
	b := selfWalletBalanceRoutePath
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		called := false
		guard := (&App{}).selfWalletBalanceRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(218)
		}))
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, httptest.NewRequest(method, b, nil))
		if !called || response.Code != 218 {
			t.Fatalf("%s owner changed: called=%v status=%d", method, called, response.Code)
		}
	}
	guard := (&App{}).selfWalletBalanceRouteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(218)
	}))
	for _, target := range []string{b, "/self/api/v1/billing//balance", "http://example.invalid" + b} {
		response := httptest.NewRecorder()
		guard.ServeHTTP(response, balanceShapeRequest(t, target))
		if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" || response.Header().Get("Allow") != "" {
			t.Fatalf("off target=%q status=%d headers=%v", target, response.Code, response.Header())
		}
	}
}

type balanceBoundaryWire struct {
	status, bodyBytes                               int
	location, allow, cache, contentType, code, hash string
	marker                                          bool
}

func balanceBoundaryFinancialCounts(t *testing.T, app *App) [4]int {
	t.Helper()
	var counts [4]int
	for i, table := range [...]string{"financial_plans", "financial_accounts", "financial_entries", "financial_subscriptions"} {
		if err := app.store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func balanceBoundaryRequest(t *testing.T, serverURL, method, target string, cookie *http.Cookie, origin string) balanceBoundaryWire {
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
	}
	if cookie != nil {
		fmt.Fprintf(&request, "Cookie: %s=%s\r\n", cookie.Name, cookie.Value)
	}
	request.WriteString("Content-Length: 0\r\n\r\n")
	if _, err := io.WriteString(conn, request.String()); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	cut := bytes.Index(raw, []byte("\r\n\r\n"))
	if cut < 0 {
		t.Fatal("response missing header terminator")
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &decoded)
	wireBody := raw[cut+4:]
	return balanceBoundaryWire{
		status: response.StatusCode, location: response.Header.Get("Location"),
		allow: response.Header.Get("Allow"), cache: response.Header.Get("Cache-Control"),
		contentType: response.Header.Get("Content-Type"), code: decoded.Error.Code,
		bodyBytes: len(wireBody), hash: fmt.Sprintf("%X", sha256.Sum256(wireBody)),
		marker: bytes.Contains(wireBody, []byte("wallet-boundary-web-marker")),
	}
}

func TestSelfWalletBalanceRouteWireBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "base-only"
		if enabled {
			name = "balance-on"
		}
		t.Run(name, func(t *testing.T) {
			dataDir, webDir := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("wallet-boundary-web-marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
				t.Fatal(err)
			}
			app, err := Open(context.Background(), Config{
				DataDir: dataDir, Listen: "127.0.0.1:0", WebDir: webDir, Version: "test",
				EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: enabled,
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
			selfCookie, _ := selfRedeem(t, server.URL, employee.ID, secret)
			financialBefore := balanceBoundaryFinancialCounts(t, app)
			b := selfWalletBalanceRoutePath
			for _, tc := range []struct {
				name, target string
				alias        bool
			}{
				{"literal", b + "?currency=USD", false},
				{"double slash", "/self/api/v1/billing//balance?currency=USD", true},
				{"dot", "/self/api/v1/billing/./balance?currency=USD", true},
				{"encoded letter", "/self/api/v1/billing/%62alance?currency=USD", true},
				{"absolute", "http://authority.invalid" + b + "?currency=USD", true},
				{"uppercase scheme", "HTTP://authority.invalid" + b + "?currency=USD", true},
				{"userinfo", "http://synthetic@authority.invalid" + b + "?currency=USD", true},
				{"empty URL host", "http://" + b + "?currency=USD", true},
				{"scheme single slash", "http:" + b + "?currency=USD", true},
				{"custom scheme single slash", "custom:" + b + "?currency=USD", true},
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, identity := range []string{"anonymous", "self"} {
						t.Run(tc.name+"/"+method+"/"+identity, func(t *testing.T) {
							var cookie *http.Cookie
							if identity == "self" {
								cookie = selfCookie
							}
							got := balanceBoundaryRequest(t, server.URL, method, tc.target, cookie, "")
							want := http.StatusNotFound
							if enabled {
								want = http.StatusUnauthorized
								if identity == "self" {
									want = http.StatusOK
									if tc.alias {
										want = http.StatusBadRequest
									}
								}
							}
							if got.status != want || got.location != "" || got.allow != "" || got.cache != "no-store" || got.marker ||
								(method == http.MethodHead && got.bodyBytes != 0) {
								t.Fatalf("target=%q status=%d want=%d location=%q allow=%q cache=%q body=%d marker=%v", tc.target, got.status, want, got.location, got.allow, got.cache, got.bodyBytes, got.marker)
							}
							if enabled && tc.alias && identity == "self" && got.code != "invalid_request" && method != http.MethodHead {
								t.Fatalf("alias code=%q", got.code)
							}
						})
					}
				}
			}
			near8192 := "/self/api/v1/billing/" + strings.Repeat("/", 8192-len("/self/api/v1/billing/")-len("balance")) + "balance"
			near8191 := "/self/api/v1/billing/" + strings.Repeat("/", 8191-len("/self/api/v1/billing/")-len("balance")) + "balance"
			prefix1024 := "http://" + strings.Repeat("a", 1024-len("http://"))
			for _, tc := range []struct {
				name, target string
				owned        bool
			}{
				{"8191", near8191 + "?currency=USD", true},
				{"8192", near8192, true},
				{"question at byte 8193", near8192 + "?currency=USD", true},
				{"neighbor extension at byte 8193", near8192 + "x?currency=USD", false},
				{"non-origin prefix 1024", prefix1024 + b + "?currency=USD", true},
				{"non-origin prefix 1025", prefix1024 + "a" + b + "?currency=USD", false},
			} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, identity := range []string{"anonymous", "self"} {
						t.Run("budget/"+tc.name+"/"+method+"/"+identity, func(t *testing.T) {
							var cookie *http.Cookie
							if identity == "self" {
								cookie = selfCookie
							}
							got := balanceBoundaryRequest(t, server.URL, method, tc.target, cookie, "")
							want := http.StatusNotFound
							wantLocation := ""
							if tc.name == "neighbor extension at byte 8193" {
								want = http.StatusTemporaryRedirect // original ServeMux cleans to balancex, not B
								wantLocation = "/self/api/v1/billing/balancex?currency=USD"
							}
							if enabled && tc.name != "neighbor extension at byte 8193" {
								want = http.StatusUnauthorized
								if identity == "self" {
									want = http.StatusBadRequest
									if !tc.owned {
										want = http.StatusOK // original ServeMux owner, not this guard
									}
								}
							}
							if got.status != want || got.cache != "no-store" || got.marker ||
								got.location != wantLocation || got.allow != "" ||
								(method == http.MethodHead && got.bodyBytes != 0) {
								t.Fatalf("budget %s %s %s status=%d want=%d location=%q body=%d marker=%v", tc.name, method, identity, got.status, want, got.location, got.bodyBytes, got.marker)
							}
						})
					}
				}
			}
			if enabled {
				got := balanceBoundaryRequest(t, server.URL, http.MethodGet, "/self/api/v1/billing//balance?currency=USD", selfCookie, "https://different.invalid")
				if got.status != http.StatusForbidden || got.code != "request_rejected" {
					t.Fatalf("Origin priority: status=%d code=%q", got.status, got.code)
				}
			}
			for _, target := range []string{b + "?currency=USD", "/self/api/v1/billing//balance?currency=USD", "/self/api/v1/billing/./balance?currency=USD", b + "-other?currency=USD"} {
				for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
					got := balanceBoundaryRequest(t, server.URL, method, target, selfCookie, "")
					wantStatus, wantLocation, wantType, wantBytes, wantHash := 404, "", "text/plain; charset=utf-8", 19, "B16E15764B8BC06C5C3F9F19BC8B99FA48E7894AA5A6CCDAD65DA49BBF564793"
					if strings.Contains(target, "//balance") || strings.Contains(target, "/./balance") {
						wantStatus, wantLocation, wantType, wantBytes, wantHash = 307, b+"?currency=USD", "", 0, "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"
					}
					if got.status != wantStatus || got.location != wantLocation || got.allow != "" || got.cache != "no-store" ||
						got.contentType != wantType || got.code != "" || got.bodyBytes != wantBytes || got.hash != wantHash {
						t.Fatalf("wrong method %s %q: %+v", method, target, got)
					}
				}
			}
			if financialAfter := balanceBoundaryFinancialCounts(t, app); financialAfter != financialBefore {
				t.Fatalf("route probes changed financial rows: before=%v after=%v", financialBefore, financialAfter)
			}
		})
	}
}

func TestSelfWalletBalanceRouteBothOffBeforeOriginOrWeb(t *testing.T) {
	dataDir, webDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("wallet-boundary-web-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", WebDir: webDir, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	for _, target := range []string{
		selfWalletBalanceRoutePath + "?currency=USD",
		"/self/api/v1/billing//balance?currency=USD",
		"HTTP://authority.invalid" + selfWalletBalanceRoutePath + "?currency=USD",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			got := balanceBoundaryRequest(t, server.URL, method, target, nil, "https://different.invalid")
			if got.status != 404 || got.location != "" || got.allow != "" || got.cache != "no-store" || got.marker ||
				(method == http.MethodHead && got.bodyBytes != 0) {
				t.Fatalf("both-off %s %q: %+v", method, target, got)
			}
		}
	}
}
