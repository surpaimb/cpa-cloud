package service

// Independently authored HTTP checks for docs/employee-self-redemption-credit-history-contract.md.
// All employees, passwords, and redemption codes are synthetic.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSelfRedemptionHistoryFixture(t *testing.T) selfRedemptionFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true,
		EmployeeSelfWalletEntryClassificationEnabled: true, EmployeeSelfRedemptionCreditHistoryEnabled: true,
		EmployeeSelfRedemptionEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	cookie, csrf := selfRedeem(t, server.URL, item.ID, secret)
	return selfRedemptionFixture{selfWalletFixture{app: app, server: server, dir: dir, id: item.ID,
		cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}}
}

func redemptionHistoryRequest(t *testing.T, baseURL, query, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, baseURL+selfRedemptionHistoryPath+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestSelfRedemptionHistoryShapedPathsStayAPIWithWebFallback(t *testing.T) {
	f := newSelfRedemptionHistoryFixture(t)
	disabled := newSelfActivityFixture(t)
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html>synthetic SPA fallback</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.WebDir = webDir
	disabled.app.cfg.WebDir = webDir
	enabledServer := httptest.NewServer(f.app.Handler())
	t.Cleanup(enabledServer.Close)
	disabledServer := httptest.NewServer(disabled.app.Handler())
	t.Cleanup(disabledServer.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	paths := []string{
		"/self/api/v1/billing%5Credemption-credits",
		"/self/api/v1/billing%255Credemption-credits",
		"/self%5Capi/v1/billing/redemption-credits",
		"/self/api/v1/billing/redemption-credits%5C",
		"/self/api/v1/billing%2Fredemption-credits",
		"/self//api/v1/billing/redemption-credits",
		"/self/api/v1/billing/./redemption-credits",
		"/self/api/v1/billing/other/../redemption-credits",
	}
	for _, rawPath := range paths {
		for _, mode := range []struct {
			name, origin string
			cookie       *http.Cookie
			want         int
		}{
			{"enabled-anonymous", enabledServer.URL, nil, 401},
			{"enabled-employee", enabledServer.URL, f.cookie, 400},
			{"disabled-anonymous", disabledServer.URL, nil, 404},
			{"disabled-employee", disabledServer.URL, disabled.cookie, 404},
		} {
			t.Run(mode.name+rawPath, func(t *testing.T) {
				request, err := http.NewRequest(http.MethodGet, mode.origin+rawPath+"?currency=USD", nil)
				if err != nil {
					t.Fatal(err)
				}
				if mode.cookie != nil {
					request.AddCookie(mode.cookie)
				}
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != mode.want || response.Header.Get("Location") != "" ||
					strings.Contains(response.Header.Get("Content-Type"), "text/html") ||
					response.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("shaped path %q mode=%s status=%d location=%q type=%q cache=%q",
						rawPath, mode.name, response.StatusCode, response.Header.Get("Location"),
						response.Header.Get("Content-Type"), response.Header.Get("Cache-Control"))
				}
			})
		}
	}
}

func TestSelfRedemptionHistoryGatesStrictInputAndMinimalPage(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfRedemptionCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfRedemptionCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfRedemptionCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfRedemptionCreditHistoryEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	disabled := newSelfActivityFixture(t)
	if response := redemptionHistoryRequest(t, disabled.server.URL, "?currency=USD", "", disabled.cookie); response.StatusCode != 404 {
		response.Body.Close()
		t.Fatalf("disabled route status=%d", response.StatusCode)
	} else {
		response.Body.Close()
	}
	f := newSelfRedemptionHistoryFixture(t)
	f.setCommercial(t, true)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_redemption_credit_history"] != true {
		t.Fatalf("missing capability: %v", features)
	}
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "", nil), 401)
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "", f.adminCookie), 401)
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "http://evil.invalid", f.cookie), 403)
	for _, query := range []string{"", "?", "?currency=usd", "?currency=USD&currency=EUR", "?currency=USD&employee_id=other", "?currency=USD&limit=01", "?currency=USD&cursor=abc", "?currency=USD&bad=1"} {
		value := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, query, "", f.cookie), 400)
		if value["items"] != nil {
			t.Fatalf("invalid query leaked items: %q %+v", query, value)
		}
	}
	wrongMethod := selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionHistoryPath+"?currency=USD", "", "", f.cookie, "")
	if wrongMethod.StatusCode != 405 || wrongMethod.Header.Get("Allow") != "GET" {
		t.Fatalf("wrong method status=%d allow=%q", wrongMethod.StatusCode, wrongMethod.Header.Get("Allow"))
	}
	wrongMethod.Body.Close()
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+selfRedemptionHistoryPath+"?currency=USD", `{}`, "", f.cookie, ""), 400)
	empty := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "", f.cookie), 200)
	if len(empty) != 6 || empty["currency"] != "USD" || empty["has_account"] != false || len(empty["items"].([]any)) != 0 || empty["next_cursor"] != nil {
		t.Fatalf("missing account shape=%+v", empty)
	}
	code := f.issueCode(t, "USD", 47, 1, time.Now().Add(time.Hour))
	posted := f.redeem(t, "history-credit-one", code, "a-long-self-password", f.cookie, f.csrf)
	readSelfWalletResponse(t, posted, 201)
	page := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "", f.cookie), 200)
	items := page["items"].([]any)
	if len(page) != 6 || page["has_account"] != true || len(items) != 1 || page["next_cursor"] != nil {
		t.Fatalf("credit page shape=%+v", page)
	}
	item := items[0].(map[string]any)
	if len(item) != 2 || item["amount_micro"] != "47" || item["credited_at"] == nil {
		t.Fatalf("credit item shape=%+v", item)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), code) || strings.Contains(string(encoded), "history-credit-one") || strings.Contains(string(encoded), "account_id") {
		t.Fatalf("history leaked code or internal ID: %s", encoded)
	}
	f.setCommercial(t, false)
	if _, err := f.app.store.db.Exec(`UPDATE financial_redemption_codes SET enabled=0,expires_at=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	still := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD", "", f.cookie), 200)
	if len(still["items"].([]any)) != 1 {
		t.Fatalf("closed gates hid committed history: %+v", still)
	}
}

func TestSelfRedemptionHistoryCursorPurposeAndBadLookahead(t *testing.T) {
	f := newSelfRedemptionHistoryFixture(t)
	f.setCommercial(t, true)
	for _, id := range []string{"history-page-one", "history-page-two"} {
		code := f.issueCode(t, "USD", 41, 1, time.Now().Add(time.Hour))
		readSelfWalletResponse(t, f.redeem(t, id, code, "a-long-self-password", f.cookie, f.csrf), 201)
	}
	first := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=1", "", f.cookie), 200)
	cursor, ok := first["next_cursor"].(string)
	if !ok || len(first["items"].([]any)) != 1 {
		t.Fatalf("first page=%+v", first)
	}
	second := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+cursor, "", f.cookie), 200)
	if len(second["items"].([]any)) != 1 || second["next_cursor"] != nil || second["window_start"] != first["window_start"] {
		t.Fatalf("continuation=%+v", second)
	}
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=EUR&limit=1&cursor="+cursor, "", f.cookie), 400)
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=2&cursor="+cursor, "", f.cookie), 400)
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+cursor+"x", "", f.cookie), 400)
	otherSession, _ := selfLoginTest(t, f.server.URL, f.id, "a-long-self-password")
	readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+cursor, "", otherSession), 400)
	selector := strings.Split(f.cookie.Value, ".")[0]
	if _, err := f.app.decodeSelfRedemptionHistoryCursor(cursor, selfSession{Selector: selector, EmployeeID: f.id},
		selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC().Add(16*time.Minute)); err == nil {
		t.Fatal("accepted a cursor after its 15-minute window")
	}
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+selfClassificationPath+"?currency=USD&limit=1&cursor="+cursor, "", "", f.cookie, ""), 400)
	var olderOperation string
	if err := f.app.store.db.QueryRow(`SELECT operation_id FROM financial_entries WHERE operation_id IN ('history-page-one','history-page-two') ORDER BY created_at DESC,id DESC LIMIT 1 OFFSET 1`).Scan(&olderOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`DROP TRIGGER financial_commercial_operations_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_commercial_operations SET payload_digest=randomblob(32) WHERE operation_id=?`, olderOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`CREATE TRIGGER financial_commercial_operations_no_update BEFORE UPDATE ON financial_commercial_operations BEGIN SELECT RAISE(ABORT,'financial commercial operations are immutable'); END`); err != nil {
		t.Fatal(err)
	}
	broken := readSelfWalletResponse(t, redemptionHistoryRequest(t, f.server.URL, "?currency=USD&limit=1", "", f.cookie), 503)
	if broken["items"] != nil || broken["error"] == nil {
		t.Fatalf("lookahead failure emitted partial page: %+v", broken)
	}
}

func TestSelfRedemptionHistoryFinalOutputSerializesLogout(t *testing.T) {
	f := newSelfRedemptionHistoryFixture(t)
	atFinal, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.app.selfRedemptionHistoryBeforeWrite = func() { close(atFinal); <-release }
	inner := f.app.Handler()
	arrived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/self/api/v1/sessions" && r.Method == http.MethodDelete {
			close(arrived)
		}
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	type result struct {
		response *http.Response
		err      error
	}
	readDone := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, server.URL+selfRedemptionHistoryPath+"?currency=USD", nil)
		if err != nil {
			readDone <- result{err: err}
			return
		}
		req.AddCookie(f.cookie)
		response, err := http.DefaultClient.Do(req)
		readDone <- result{response: response, err: err}
	}()
	select {
	case <-atFinal:
	case <-time.After(10 * time.Second):
		t.Fatal("history did not reach final output")
	}
	logoutDone := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodDelete, server.URL+"/self/api/v1/sessions", nil)
		if err != nil {
			logoutDone <- result{err: err}
			return
		}
		req.AddCookie(f.cookie)
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-CSRF-Token", f.csrf)
		req.Header.Set("X-Self-Request", "1")
		response, err := http.DefaultClient.Do(req)
		logoutDone <- result{response: response, err: err}
	}()
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("logout did not arrive")
	}
	select {
	case result := <-logoutDone:
		if result.response != nil {
			result.response.Body.Close()
		}
		t.Fatalf("logout completed before history output: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case result := <-readDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		readSelfWalletResponse(t, result.response, 200)
	case <-time.After(10 * time.Second):
		t.Fatal("history response stalled")
	}
	select {
	case result := <-logoutDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.response.StatusCode != 204 {
			t.Fatalf("logout status=%d", result.response.StatusCode)
		}
		result.response.Body.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("logout response stalled")
	}
	readSelfWalletResponse(t, redemptionHistoryRequest(t, server.URL, "?currency=USD", "", f.cookie), 401)
}
