// Independently authored tests for docs/employee-self-wallet-entry-classification-contract.md.
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfClassificationFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true,
		EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true, Version: "test"})
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

func selfClassificationRequest(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+selfClassificationPath+query, body, origin, cookie, "")
}

type selfClassificationHTTPPage struct {
	Currency    string `json:"currency"`
	HasAccount  bool   `json:"has_account"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Items       []struct {
		OccurredAt string `json:"occurred_at"`
		DeltaMicro string `json:"delta_micro"`
		EntryKind  string `json:"entry_kind"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func readClassificationPage(t *testing.T, r *http.Response) selfClassificationHTTPPage {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", r.StatusCode, r.Header.Get("Cache-Control"), readBody(r))
	}
	var page selfClassificationHTTPPage
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

func TestSelfClassificationGatesRolesAndStrictInput(t *testing.T) {
	for _, cfg := range []Config{{EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing classification prerequisite: %+v", cfg)
		}
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {EmployeeSelfServiceEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true}} {
		cfg.DataDir, cfg.Listen, cfg.Version = dir, "127.0.0.1:0", "test"
		app, err := Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		r := selfClassificationRequest(t, server.URL, "?currency=USD", "", "", nil)
		if r.StatusCode != 404 {
			t.Fatalf("disabled classification config=%+v status=%d", cfg, r.StatusCode)
		}
		r.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfClassificationFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "", cookie), 401)
	}
	key := createTestKey(t, f.server.URL, f.id, "classification-role-key", f.adminCookie, f.adminCSRF)
	req, err := http.NewRequest(http.MethodGet, f.server.URL+selfClassificationPath+"?currency=USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 401)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "http://evil.invalid", f.cookie), 403)
	for _, query := range []string{"", "?currency=", "?currency=usd", "?currency=US", "?currency=USDD", "?currency=%EF%BC%B5SD",
		"?currency=USD&currency=EUR", "?currency=USD&employee_id=x", "?currency=USD&account_id=x", "?currency=USD&key_id=x",
		"?currency=USD&bad=1", "?currency=USD&limit=0", "?currency=USD&limit=51", "?currency=USD&limit=01",
		"?currency=USD&limit=1&limit=2", "?currency=USD&cursor=x", "?currency=USD&limit=1&cursor=", "?currency=USD;bad=x"} {
		value := readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, query, "", "", f.cookie), 400)
		if value["items"] != nil {
			t.Fatalf("invalid query %q leaked page=%v", query, value)
		}
	}
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", `{}`, "", f.cookie), 400)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_wallet_entry_classification"] != true || features["employee_self_wallet_activity"] != true {
		t.Fatalf("classification feature=%v", features)
	}
	old := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie))
	if old.HasAccount || len(old.Items) != 0 {
		t.Fatalf("old activity route changed=%+v", old)
	}
}

func TestSelfClassificationPageCursorSeparationAndRevocation(t *testing.T) {
	f := newSelfClassificationFixture(t)
	missing := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=JPY", "", "", f.cookie))
	if missing.HasAccount || len(missing.Items) != 0 || missing.NextCursor != nil {
		t.Fatalf("missing=%+v", missing)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	activityPost(t, f, "classification-own-credit", owner, "USD", financial.EntryAdjustmentCredit, 42, at)
	activityPost(t, f, "classification-own-debit", owner, "USD", financial.EntryAdjustmentDebit, -7, at.Add(time.Second))
	activityPost(t, f, "classification-other-currency", owner, "EUR", financial.EntryAdjustmentCredit, 11, at)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	activityPost(t, f, "classification-other-employee", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, "USD", financial.EntryAdjustmentCredit, 91, at)
	key := createTestKey(t, f.server.URL, f.id, "classification-owner-key", f.adminCookie, f.adminCSRF)
	activityPost(t, f, "classification-key", financial.Owner{Kind: financial.OwnerKey, EmployeeID: f.id, KeyID: key.ID}, "USD", financial.EntryAdjustmentCredit, 500, at)
	activityPost(t, f, "classification-resource", financial.Owner{Kind: financial.OwnerResource, EmployeeID: f.id, ResourceKind: "response", ResourceID: "one"}, "USD", financial.EntryAdjustmentCredit, 600, at)
	first := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if !first.HasAccount || len(first.Items) != 1 || first.Items[0].EntryKind != "adjustment_debit" || first.Items[0].DeltaMicro != "-7" || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	secondQuery := "?currency=USD&limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	second := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, secondQuery, "", "", f.cookie))
	if len(second.Items) != 1 || second.Items[0].EntryKind != "adjustment_credit" || second.NextCursor != nil || second.WindowEnd != first.WindowEnd {
		t.Fatalf("second=%+v", second)
	}
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, secondQuery, "", "", f.cookie), 400)
	old := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if old.NextCursor == nil || len(old.Items) != 1 {
		t.Fatalf("old page=%+v", old)
	}
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(*old.NextCursor), "", "", f.cookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", otherCookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=EUR&limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", f.cookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=2&cursor="+url.QueryEscape(*first.NextCursor), "", "", f.cookie), 400)
	otherPage := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "", otherCookie))
	if len(otherPage.Items) != 1 || otherPage.Items[0].DeltaMicro != "91" {
		t.Fatalf("other employee=%+v", otherPage)
	}
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true,
		EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	continued := readClassificationPage(t, selfClassificationRequest(t, server.URL, secondQuery, "", "", f.cookie))
	if len(continued.Items) != 1 || continued.Items[0].DeltaMicro != "42" {
		t.Fatalf("restart continuation=%+v", continued)
	}
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable status=%d body=%s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	readSelfWalletResponse(t, selfClassificationRequest(t, server.URL, secondQuery, "", "", f.cookie), 401)
}
