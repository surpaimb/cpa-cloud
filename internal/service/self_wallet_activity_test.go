// Independently authored tests for docs/employee-self-wallet-activity-contract.md.
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfActivityFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, Version: "test"})
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
	return selfWalletFixture{app: app, server: server, dir: dir, id: item.ID, cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func selfActivityRequest(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/billing/entries"+query, body, origin, cookie, "")
}

type selfActivityHTTPPage struct {
	Currency    string `json:"currency"`
	HasAccount  bool   `json:"has_account"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Items       []struct {
		OccurredAt string `json:"occurred_at"`
		DeltaMicro string `json:"delta_micro"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func readActivityPage(t *testing.T, r *http.Response) selfActivityHTTPPage {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", r.StatusCode, r.Header.Get("Cache-Control"), readBody(r))
	}
	var page selfActivityHTTPPage
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || page.Currency == "" || page.WindowStart == "" || page.WindowEnd == "" {
		t.Fatalf("incomplete page=%+v", page)
	}
	return page
}

func activityPost(t *testing.T, f selfWalletFixture, operation string, owner financial.Owner, currency string, kind financial.EntryKind, amount int64, at time.Time) {
	t.Helper()
	_, err := financial.NewLedger(f.app.store.db).Post(context.Background(), financial.Post{
		OperationID: operation, Action: "adjustment", ResourceKind: "adjustment", ResourceID: operation, ObservedAt: at,
		Entries: []financial.EntryInput{{Owner: owner, Currency: currency, Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: operation}},
	})
	if err != nil {
		t.Fatalf("post %s: %v", operation, err)
	}
}

func TestSelfWalletActivityGatesRolesAndStrictInput(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfWalletActivityEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletActivityEnabled: true},
		{EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted activity without both prerequisites: %+v", cfg)
		}
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {EmployeeSelfServiceEnabled: true}, {EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true}} {
		cfg.DataDir, cfg.Listen, cfg.Version = dir, "127.0.0.1:0", "test"
		app, err := Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		r := selfActivityRequest(t, server.URL, "?currency=USD", "", "", nil)
		if r.StatusCode != 404 {
			t.Fatalf("disabled config=%+v status=%d", cfg, r.StatusCode)
		}
		r.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfActivityFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", cookie), 401)
	}
	key := createTestKey(t, f.server.URL, f.id, "activity-role-key", f.adminCookie, f.adminCSRF)
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/entries?currency=USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 401)
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "http://evil.invalid", f.cookie), 403)
	for _, query := range []string{"", "?currency=", "?currency=usd", "?currency=US", "?currency=USDD", "?currency=%EF%BC%B5SD", "?currency=USD&currency=EUR", "?currency=USD&employee_id=x", "?currency=USD&account_id=x", "?currency=USD&key_id=x", "?currency=USD&owner_kind=key", "?currency=USD&bad=1", "?currency=USD&limit=0", "?currency=USD&limit=51", "?currency=USD&limit=01", "?currency=USD&limit=1&limit=2", "?currency=USD&cursor=x", "?currency=USD&limit=1&cursor=", "?currency=USD;foo=x", "?currency=USD&limit=1&cursor=" + strings.Repeat("x", selfWalletActivityCursorMax+1)} {
		response := selfActivityRequest(t, f.server.URL, query, "", "", f.cookie)
		value := readSelfWalletResponse(t, response, 400)
		if value["items"] != nil || value["has_account"] != nil {
			t.Fatalf("query %q leaked page=%+v", query, value)
		}
	}
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, "?currency=USD", `{}`, "", f.cookie), 400)
	req, err = http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/entries?currency=USD", io.NopCloser(strings.NewReader(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(f.cookie)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 400)
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, "")
	value := readSelfWalletResponse(t, r, 200)
	features, _ := value["features"].(map[string]any)
	if features["employee_self_wallet_activity"] != true {
		t.Fatalf("missing independent self capability: %+v", value)
	}
}

func TestSelfWalletActivityOwnerPaginationCursorRestartAndRevocation(t *testing.T) {
	f := newSelfActivityFixture(t)
	missing := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=JPY", "", "", f.cookie))
	if missing.HasAccount || len(missing.Items) != 0 || missing.NextCursor != nil {
		t.Fatalf("missing account=%+v", missing)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	activityPost(t, f, "activity-old", owner, "USD", financial.EntryAdjustmentCredit, 9, at.Add(-32*24*time.Hour))
	beforeWindow := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie))
	if !beforeWindow.HasAccount || len(beforeWindow.Items) != 0 || beforeWindow.NextCursor != nil {
		t.Fatalf("existing account outside window=%+v", beforeWindow)
	}
	activityPost(t, f, "activity-eur", owner, "EUR", financial.EntryAdjustmentCredit, 7, at)
	activityPost(t, f, "activity-own-credit", owner, "USD", financial.EntryAdjustmentCredit, 42, at)
	activityPost(t, f, "activity-own-debit", owner, "USD", financial.EntryAdjustmentDebit, -42, at.Add(time.Second))
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	activityPost(t, f, "activity-other", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, "USD", financial.EntryAdjustmentCredit, 91, at)
	key := createTestKey(t, f.server.URL, f.id, "activity-owner-key", f.adminCookie, f.adminCSRF)
	activityPost(t, f, "activity-key", financial.Owner{Kind: financial.OwnerKey, EmployeeID: f.id, KeyID: key.ID}, "USD", financial.EntryAdjustmentCredit, 500, at)
	activityPost(t, f, "activity-resource", financial.Owner{Kind: financial.OwnerResource, EmployeeID: f.id, ResourceKind: "response", ResourceID: "response-one"}, "USD", financial.EntryAdjustmentCredit, 600, at)
	var beforeAccounts, beforeEntries int
	_ = f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&beforeAccounts)
	_ = f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&beforeEntries)
	first := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if !first.HasAccount || len(first.Items) != 1 || first.Items[0].DeltaMicro != "-42" || first.NextCursor == nil || strings.Contains(*first.NextCursor, "activity-own-debit") {
		t.Fatalf("first page=%+v", first)
	}
	secondQuery := "?currency=USD&limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	second := readActivityPage(t, selfActivityRequest(t, f.server.URL, secondQuery, "", "", f.cookie))
	if !second.HasAccount || len(second.Items) != 1 || second.Items[0].DeltaMicro != "42" || second.NextCursor != nil || second.WindowStart != first.WindowStart || second.WindowEnd != first.WindowEnd {
		t.Fatalf("second page=%+v", second)
	}
	tampered := []byte(*first.NextCursor)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	for _, query := range []string{
		"?currency=EUR&limit=1&cursor=" + url.QueryEscape(*first.NextCursor),
		"?currency=USD&limit=2&cursor=" + url.QueryEscape(*first.NextCursor),
		"?currency=USD&limit=1&cursor=" + url.QueryEscape(string(tampered)),
	} {
		readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, query, "", "", f.cookie), 400)
	}
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, secondQuery, "", "", otherCookie), 400)
	r := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(f.id)+`,"password":"a-long-self-password"}`, f.server.URL, nil, "")
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("second session status=%d", r.StatusCode)
	}
	newCookie := r.Cookies()[0]
	r.Body.Close()
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, secondQuery, "", "", newCookie), 400)
	otherPage := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", otherCookie))
	if !otherPage.HasAccount || len(otherPage.Items) != 1 || otherPage.Items[0].DeltaMicro != "91" {
		t.Fatalf("other employee page=%+v", otherPage)
	}
	var afterAccounts, afterEntries int
	_ = f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&afterAccounts)
	_ = f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&afterEntries)
	if beforeAccounts != afterAccounts || beforeEntries != afterEntries {
		t.Fatalf("read wrote ledger accounts=%d/%d entries=%d/%d", beforeAccounts, afterAccounts, beforeEntries, afterEntries)
	}
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	page := readActivityPage(t, selfActivityRequest(t, server.URL, secondQuery, "", "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].DeltaMicro != "42" {
		t.Fatalf("restart cursor page=%+v", page)
	}
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r = requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable status=%d body=%s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	readSelfWalletResponse(t, selfActivityRequest(t, server.URL, secondQuery, "", "", f.cookie), 401)
}

func TestSelfWalletActivityStorageAndExpiredCursorFailClosed(t *testing.T) {
	f := newSelfActivityFixture(t)
	selector := strings.Split(f.cookie.Value, ".")[0]
	currentEnd := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	current, err := f.app.encodeSelfWalletActivityCursor(selfWalletActivityCursor{
		Version: 1, EmployeeID: f.id, Session: selector, Currency: "USD",
		WindowStart: currentEnd.Add(-selfWalletActivityWindow).Format(time.RFC3339), WindowEnd: currentEnd.Format(time.RFC3339), Limit: 1,
		LastTime: currentEnd.Add(-time.Second).Format(time.RFC3339Nano), LastID: "entry-current",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherDir := t.TempDir()
	if err := Initialize(context.Background(), otherDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	otherSecrets, err := loadSecrets(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{secrets: otherSecrets}).decodeSelfWalletActivityCursor(current, selfSession{EmployeeID: f.id, Selector: selector}, selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC()); err == nil {
		t.Fatal("another installation accepted encrypted cursor")
	}
	oldEnd := time.Now().UTC().Add(-16 * time.Minute).Truncate(time.Second)
	oldStart := oldEnd.Add(-selfWalletActivityWindow)
	encoded, err := f.app.encodeSelfWalletActivityCursor(selfWalletActivityCursor{
		Version: 1, EmployeeID: f.id, Session: selector, Currency: "USD",
		WindowStart: oldStart.Format(time.RFC3339), WindowEnd: oldEnd.Format(time.RFC3339), Limit: 1,
		LastTime: oldEnd.Add(-time.Second).Format(time.RFC3339Nano), LastID: "entry-old",
	})
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(encoded), "", "", f.cookie), 400)
	if _, err := f.app.store.db.Exec(`DROP INDEX financial_entries_account_idx`); err != nil {
		t.Fatal(err)
	}
	r := selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie)
	defer r.Body.Close()
	if r.StatusCode != 503 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("storage status=%d cache=%q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "financial_") || strings.Contains(string(body), f.id) || strings.Contains(string(body), f.cookie.Value) || strings.Contains(string(body), "items") {
		t.Fatalf("storage error leaked page: %s", body)
	}
}
