// Independently authored HTTP tests for docs/employee-self-admin-adjustment-history-contract.md.
package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfAdminAdjustmentFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true,
		EmployeeSelfWalletEntryClassificationEnabled: true, EmployeeSelfAdminAdjustmentHistoryEnabled: true})
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
	return selfWalletFixture{app: app, server: server, dir: dir, id: item.ID, cookie: cookie, csrf: csrf,
		adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func adminAdjustmentRequest(t *testing.T, base, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, base+selfAdminAdjustmentPath+query, "", "", cookie, "")
}

func postFixtureAdminAdjustment(t *testing.T, f selfWalletFixture, operation string, amount int64) {
	t.Helper()
	var adminID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	kind := financial.EntryAdjustmentCredit
	if amount < 0 {
		kind = financial.EntryAdjustmentDebit
	}
	_, err := financial.NewLedger(f.app.store.db).Post(context.Background(), financial.Post{
		OperationID: operation, Action: "adjustment", ActorAdminID: adminID,
		ResourceKind: "adjustment", ResourceID: operation, ObservedAt: time.Now().UTC(), RequireNonNegative: true,
		Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, Currency: "USD", Kind: kind,
			AmountMicro: amount, ResourceKind: "adjustment", ResourceID: operation}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfAdminAdjustmentHistoryStrictRouteAndInputs(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfAdminAdjustmentHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfAdminAdjustmentHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfAdminAdjustmentHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfAdminAdjustmentHistoryEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	f := newSelfAdminAdjustmentFixture(t)
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("synthetic SPA"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.WebDir = webDir
	server := httptest.NewServer(f.app.Handler())
	t.Cleanup(server.Close)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, shaped := range []string{
		"/self/api/v1/billing%5Cadmin-adjustments", "/self/api/v1/billing%255Cadmin-adjustments",
		"/self/api/v1/billing/admin-adjustments%5C", "/self//api/v1/billing/admin-adjustments",
		"/self/api/v1/billing/./admin-adjustments", "/self/api/v1/billing%2Fadmin-adjustments",
	} {
		request, err := http.NewRequest(http.MethodGet, server.URL+shaped+"?currency=USD", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(f.cookie)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 400 || response.Header.Get("Location") != "" || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("shaped %q: %d %q", shaped, response.StatusCode, response.Header.Get("Location"))
		}
		response.Body.Close()
	}
	readSelfWalletResponse(t, adminAdjustmentRequest(t, server.URL, "?currency=USD", nil), 401)
	readSelfWalletResponse(t, adminAdjustmentRequest(t, server.URL, "?currency=USD", f.adminCookie), 401)
	for _, query := range []string{"", "?", "?currency=usd", "?currency=USD&currency=EUR", "?currency=USD&limit=01", "?currency=USD&cursor=abc", "?currency=USD&x=1", "?curr%65ncy=USD", "?currency=US%44"} {
		readSelfWalletResponse(t, adminAdjustmentRequest(t, server.URL, query, f.cookie), 400)
	}
	wrong := selfRequestTest(t, http.MethodPost, server.URL+selfAdminAdjustmentPath+"?currency=USD", "", "", f.cookie, "")
	if wrong.StatusCode != 405 || wrong.Header.Get("Allow") != "GET" {
		t.Fatalf("method=%d allow=%q", wrong.StatusCode, wrong.Header.Get("Allow"))
	}
	wrong.Body.Close()
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+selfAdminAdjustmentPath+"?currency=USD", `{}`, "", f.cookie, ""), 400)
	missing := readSelfWalletResponse(t, adminAdjustmentRequest(t, server.URL, "?currency=USD", f.cookie), 200)
	if len(missing) != 6 || missing["has_account"] != false || len(missing["items"].([]any)) != 0 {
		t.Fatalf("missing=%+v", missing)
	}
}

func TestSelfAdminAdjustmentHistorySignedPageAndCursorIsolation(t *testing.T) {
	f := newSelfAdminAdjustmentFixture(t)
	postFixtureAdminAdjustment(t, f, "self-admin-credit", 42)
	postFixtureAdminAdjustment(t, f, "self-admin-debit", -7)
	first := readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=USD&limit=1", f.cookie), 200)
	f.app.selfAdminAdjustmentNonce = func([]byte) (int, error) { return 0, errors.New("synthetic entropy failure") }
	readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=USD&limit=1", f.cookie), 503)
	f.app.selfAdminAdjustmentNonce = nil
	cursor, ok := first["next_cursor"].(string)
	if !ok || len(first) != 6 || len(first["items"].([]any)) != 1 {
		t.Fatalf("first=%+v", first)
	}
	second := readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+cursor, f.cookie), 200)
	if second["next_cursor"] != nil || second["window_start"] != first["window_start"] {
		t.Fatalf("second=%+v", second)
	}
	values := map[string]bool{}
	for _, page := range []map[string]any{first, second} {
		item := page["items"].([]any)[0].(map[string]any)
		if len(item) != 2 {
			t.Fatalf("item=%+v", item)
		}
		values[item["delta_micro"].(string)] = true
	}
	if !values["42"] || !values["-7"] {
		t.Fatalf("signed values=%+v", values)
	}
	readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=EUR&limit=1&cursor="+cursor, f.cookie), 400)
	readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=USD&limit=2&cursor="+cursor, f.cookie), 400)
	oldRoute := selfRequestTest(t, http.MethodGet, f.server.URL+selfRedemptionHistoryPath+"?currency=USD&limit=1&cursor="+cursor, "", "", f.cookie, "")
	if oldRoute.StatusCode != 404 {
		t.Fatalf("old disabled route=%d", oldRoute.StatusCode)
	}
	oldRoute.Body.Close()
	selector := strings.Split(f.cookie.Value, ".")[0]
	if _, err := f.app.decodeSelfAdminAdjustmentCursor(cursor, selfSession{Selector: selector, EmployeeID: f.id},
		selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC().Add(16*time.Minute)); err == nil {
		t.Fatal("accepted expired cursor")
	}
}

func TestSelfAdminAdjustmentHistoryFinalOutputSerializesLogout(t *testing.T) {
	f := newSelfAdminAdjustmentFixture(t)
	postFixtureAdminAdjustment(t, f, "self-admin-linearized", 9)
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	f.app.selfAdminAdjustmentBeforeWrite = func() { close(entered); <-release }
	defer func() { f.app.selfAdminAdjustmentBeforeWrite = nil }()
	type outcome struct {
		status int
		err    error
	}
	readDone := make(chan outcome, 1)
	go func() {
		request, err := http.NewRequest(http.MethodGet, f.server.URL+selfAdminAdjustmentPath+"?currency=USD", nil)
		if err != nil {
			readDone <- outcome{err: err}
			return
		}
		request.AddCookie(f.cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			readDone <- outcome{err: err}
			return
		}
		defer response.Body.Close()
		readDone <- outcome{status: response.StatusCode}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("history did not reach final output boundary")
	}
	logoutDone := make(chan outcome, 1)
	go func() {
		request, err := http.NewRequest(http.MethodDelete, f.server.URL+"/self/api/v1/sessions", nil)
		if err != nil {
			logoutDone <- outcome{err: err}
			return
		}
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", f.server.URL)
		request.Header.Set("X-Self-Request", "1")
		request.Header.Set("X-CSRF-Token", f.csrf)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			logoutDone <- outcome{err: err}
			return
		}
		defer response.Body.Close()
		logoutDone <- outcome{status: response.StatusCode}
	}()
	select {
	case result := <-logoutDone:
		t.Fatalf("logout overtook final output: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	released = true
	for _, test := range []struct {
		name     string
		expected int
		channel  chan outcome
	}{
		{"read", 200, readDone}, {"logout", 204, logoutDone},
	} {
		select {
		case result := <-test.channel:
			if result.err != nil || result.status != test.expected {
				t.Fatalf("%s outcome=%+v", test.name, result)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not complete", test.name)
		}
	}
	readSelfWalletResponse(t, adminAdjustmentRequest(t, f.server.URL, "?currency=USD", f.cookie), 401)
}
