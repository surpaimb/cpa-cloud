// Independently authored tests for docs/employee-self-subscription-status-contract.md.
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

func newSelfSubscriptionFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, Version: "test"})
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
	return selfWalletFixture{app: app, server: server, dir: dir, id: employee.ID, cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func selfSubscriptionRequest(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/billing/subscriptions"+query, body, origin, cookie, "")
}

type selfSubscriptionHTTPPage struct {
	Items []struct {
		SubscriptionID string  `json:"subscription_id"`
		Interval       string  `json:"interval"`
		Status         string  `json:"status"`
		StartedAt      string  `json:"started_at"`
		PeriodEndAt    *string `json:"period_end_at"`
		CancelledAt    *string `json:"cancelled_at"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func readSelfSubscriptionPage(t *testing.T, response *http.Response) selfSubscriptionHTTPPage {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page selfSubscriptionHTTPPage
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil || page.Items == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	return page
}

func seedSelfSubscription(t *testing.T, f selfWalletFixture, id string, owner financial.Owner, interval, status string, cancelled bool) {
	t.Helper()
	activityPost(t, f, "account-"+id, owner, "USD", financial.EntryAdjustmentCredit, 100, time.Now().UTC())
	var accountID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM financial_accounts WHERE owner_kind=? AND employee_id=? AND currency='USD' ORDER BY id DESC LIMIT 1`, owner.Kind, owner.EmployeeID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	start := "2026-01-31T08:00:00Z"
	var end, cancellation any
	if interval == "monthly" {
		end = "2026-02-28T08:00:00.000000000Z"
	}
	if cancelled {
		cancellation = "2026-02-01T08:00:00Z"
	}
	planID := "self-sub-month-plan"
	if interval == "one_time" {
		planID = "self-sub-once-plan"
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,cancelled_at,revision)
		VALUES(?,?,?,1,10,20,'USD',?,?,?,?,?,1)`, id, accountID, planID, interval, status, start, end, cancellation); err != nil {
		t.Fatal(err)
	}
}

func createSelfSubscriptionPlan(t *testing.T, f selfWalletFixture) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at)
		VALUES('self-sub-month-plan','synthetic monthly','USD',10,20,'monthly',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('self-sub-once-plan','synthetic once','USD',10,20,'one_time',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

func TestSelfSubscriptionStatusGatesRolesAndStrictInput(t *testing.T) {
	if _, err := Open(context.Background(), Config{EmployeeSelfSubscriptionStatusEnabled: true}); err == nil {
		t.Fatal("subscription status accepted without self service")
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {EmployeeSelfServiceEnabled: true}} {
		cfg.DataDir, cfg.Listen, cfg.Version = dir, "127.0.0.1:0", "test"
		app, err := Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		response := selfSubscriptionRequest(t, server.URL, "", "", "", nil)
		if response.StatusCode != 404 {
			t.Fatalf("disabled status=%d", response.StatusCode)
		}
		response.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfSubscriptionFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, "", "", "", cookie), 401)
	}
	key := createTestKey(t, f.server.URL, f.id, "self-sub-role", f.adminCookie, f.adminCSRF)
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, response, 401)
	readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, "", "", "http://evil.invalid", f.cookie), 403)
	for _, query := range []string{"?employee_id=x", "?account_id=x", "?key_id=x", "?plan_id=x", "?status=active", "?limit=0", "?limit=51", "?limit=01", "?limit=1&limit=2", "?cursor=x", "?limit=1&cursor=", "?limit=1&cursor=" + strings.Repeat("x", selfSubscriptionCursorMax+1), "?bad=1", "?limit=1&&cursor=x", "?limit=%GG"} {
		value := readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, query, "", "", f.cookie), 400)
		if value["items"] != nil {
			t.Fatalf("bad query %q leaked items", query)
		}
	}
	readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, "", `{}`, "", f.cookie), 400)
	req, _ = http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions", io.NopCloser(strings.NewReader(`{}`)))
	req.AddCookie(f.cookie)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, response, 400)
	value := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)
	features, _ := value["features"].(map[string]any)
	if features["employee_self_subscription_status"] != true || features["employee_self_wallet_balance"] != false {
		t.Fatalf("independent capability=%+v", features)
	}
}

func TestSelfSubscriptionStatusOwnerCursorRestartAndStorageFailure(t *testing.T) {
	f := newSelfSubscriptionFixture(t)
	empty := readSelfSubscriptionPage(t, selfSubscriptionRequest(t, f.server.URL, "", "", "", f.cookie))
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatalf("empty=%+v", empty)
	}
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "sub-z-own", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, "one_time", "active", false)
	seedSelfSubscription(t, f, "sub-y-own", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, "monthly", "active", false)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, secret)
	seedSelfSubscription(t, f, "sub-x-other", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, "monthly", "active", false)
	seedSelfSubscription(t, f, "sub-w-resource", financial.Owner{Kind: financial.OwnerResource, EmployeeID: f.id, ResourceKind: "response", ResourceID: "self-sub-resource"}, "monthly", "active", false)
	first := readSelfSubscriptionPage(t, selfSubscriptionRequest(t, f.server.URL, "?limit=1", "", "", f.cookie))
	if len(first.Items) != 1 || first.Items[0].SubscriptionID != "sub-z-own" || first.Items[0].PeriodEndAt != nil || first.NextCursor == nil || strings.Contains(*first.NextCursor, "sub-z-own") {
		t.Fatalf("first=%+v", first)
	}
	query := "?limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	second := readSelfSubscriptionPage(t, selfSubscriptionRequest(t, f.server.URL, query, "", "", f.cookie))
	if len(second.Items) != 1 || second.Items[0].SubscriptionID != "sub-y-own" || second.Items[0].Status != "expired" || second.NextCursor != nil {
		t.Fatalf("second=%+v", second)
	}
	tampered := []byte(*first.NextCursor)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	for _, bad := range []string{"?limit=2&cursor=" + url.QueryEscape(*first.NextCursor), "?limit=1&cursor=" + url.QueryEscape(string(tampered))} {
		readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, bad, "", "", f.cookie), 400)
	}
	readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, query, "", "", otherCookie), 400)
	selector := strings.Split(f.cookie.Value, ".")[0]
	expired, err := f.app.encodeSelfSubscriptionCursor(selfSubscriptionCursor{Version: 1, EmployeeID: f.id, Session: selector, IssuedAt: time.Now().UTC().Add(-16 * time.Minute).Format(time.RFC3339Nano), Limit: 1, LastID: "sub-z-own"})
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, "?limit=1&cursor="+url.QueryEscape(expired), "", "", f.cookie), 400)
	otherDir := t.TempDir()
	if err := Initialize(context.Background(), otherDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	otherSecrets, err := loadSecrets(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{secrets: otherSecrets}).decodeSelfSubscriptionCursor(*first.NextCursor, selfSession{EmployeeID: f.id, Selector: selector}, selfSubscriptionParams{limit: 1}, time.Now().UTC()); err == nil {
		t.Fatal("foreign installation cursor accepted")
	}
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	page := readSelfSubscriptionPage(t, selfSubscriptionRequest(t, server.URL, query, "", "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].SubscriptionID != "sub-y-own" {
		t.Fatalf("restart=%+v", page)
	}
	if _, err := reopened.store.db.Exec(`DROP INDEX financial_subscriptions_account_idx`); err != nil {
		t.Fatal(err)
	}
	value := readSelfWalletResponse(t, selfSubscriptionRequest(t, server.URL, "", "", "", f.cookie), 503)
	if value["items"] != nil {
		t.Fatalf("storage error leaked page=%+v", value)
	}
	logout := selfRequestTest(t, http.MethodDelete, server.URL+"/self/api/v1/sessions", "", server.URL, f.cookie, f.csrf)
	logout.Body.Close()
	if logout.StatusCode != 204 {
		t.Fatalf("logout status=%d", logout.StatusCode)
	}
	readSelfWalletResponse(t, selfSubscriptionRequest(t, server.URL, query, "", "", f.cookie), 401)
}
