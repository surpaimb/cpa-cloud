// Independently authored tests for docs/employee-self-plan-catalog-contract.md
// and docs/employee-self-plan-catalog-copy-truth-contract.md.
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
)

func newSelfPlanCatalogFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfPlanCatalogEnabled: true, Version: "test"})
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

func requestSelfPlanCatalog(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/billing/plans"+query, body, origin, cookie, "")
}

type selfPlanCatalogHTTPPage struct {
	Currency  string `json:"currency"`
	Available bool   `json:"available"`
	Items     []struct {
		PlanID      string `json:"plan_id"`
		Name        string `json:"name"`
		Interval    string `json:"interval"`
		PriceMicro  string `json:"price_micro"`
		CreditMicro string `json:"credit_micro"`
		Revision    int64  `json:"revision"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func readSelfPlanCatalogPage(t *testing.T, response *http.Response) selfPlanCatalogHTTPPage {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page selfPlanCatalogHTTPPage
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil || page.Items == nil {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("trailing page data: %v", err)
	}
	return page
}

func seedSelfPlanCatalog(t *testing.T, f selfWalletFixture) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at)
		VALUES('plan-a','Current monthly','USD',13,29,'monthly',1,2,'2026-01-01T00:00:00Z','2026-01-02T00:00:00Z'),
		('plan-b','Current once','USD',10,20,'one_time',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('plan-c','Disabled','USD',90,100,'monthly',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('plan-d','Other currency','EUR',40,50,'monthly',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

func setSelfPlanCatalogCommercial(t *testing.T, f selfWalletFixture, enabled int) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`UPDATE financial_settings SET enabled=?,revision=revision+1,updated_at='2026-01-02T00:00:00Z' WHERE singleton=1`, enabled); err != nil {
		t.Fatal(err)
	}
}

func TestSelfPlanCatalogGatesRolesAndStrictInput(t *testing.T) {
	if _, err := Open(context.Background(), Config{EmployeeSelfPlanCatalogEnabled: true}); err == nil {
		t.Fatal("catalog accepted without self service")
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
		response := requestSelfPlanCatalog(t, server.URL, "?currency=USD", "", "", nil)
		if response.StatusCode != 404 {
			t.Fatalf("disabled route status=%d", response.StatusCode)
		}
		response.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfPlanCatalogFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD", "", "", cookie), 401)
	}
	key := createTestKey(t, f.server.URL, f.id, "catalog-role", f.adminCookie, f.adminCSRF)
	request, _ := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/plans?currency=USD", nil)
	request.Header.Set("Authorization", "Bearer "+key.Key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, response, 401)
	readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD", "", "http://evil.invalid", f.cookie), 403)
	for _, query := range []string{"", "?currency=", "?currency=usd", "?currency=US", "?currency=USDD", "?currency=USD&currency=EUR", "?currency=%GG", "?currency=USD&employee_id=x", "?currency=USD&account_id=x", "?currency=USD&key_id=x", "?currency=USD&plan_id=x", "?currency=USD&limit=0", "?currency=USD&limit=51", "?currency=USD&limit=01", "?currency=USD&limit=1&limit=2", "?currency=USD&cursor=x", "?currency=USD&limit=1&cursor=", "?currency=USD&limit=1&&cursor=x", "?currency=USD&limit=1&cursor=" + strings.Repeat("x", selfPlanCatalogCursorMax+1)} {
		readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, query, "", "", f.cookie), 400)
	}
	readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD", `{}`, "", f.cookie), 400)
	request, _ = http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/plans?currency=USD", io.NopCloser(strings.NewReader(`{}`)))
	request.AddCookie(f.cookie)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, response, 400)
	value := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)
	features, _ := value["features"].(map[string]any)
	if features["employee_self_plan_catalog"] != true || features["employee_self_wallet_balance"] != false || features["employee_self_subscription_status"] != false {
		t.Fatalf("independent capability=%+v", features)
	}
}

func TestSelfPlanCatalogCrossCapabilityTruth(t *testing.T) {
	tests := []struct {
		name          string
		wallet        bool
		cancel        bool
		renewal       bool
		enabledRoute  string
		disabledRoute string
	}{
		{
			name:          "catalog and own cancellation without purchase",
			cancel:        true,
			enabledRoute:  "/self/api/v1/billing/subscriptions/sub-example/cancel",
			disabledRoute: "/self/api/v1/billing/subscriptions/sub-example/renewal-quotes",
		},
		{
			name:          "catalog and own monthly renewal without purchase",
			wallet:        true,
			renewal:       true,
			enabledRoute:  "/self/api/v1/billing/subscriptions/sub-example/renewal-quotes",
			disabledRoute: "/self/api/v1/billing/subscriptions/sub-example/cancel",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
				t.Fatal(err)
			}
			app, err := Open(context.Background(), Config{
				DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
				EmployeeSelfServiceEnabled: true, EmployeeSelfPlanCatalogEnabled: true,
				EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfWalletBalanceEnabled: test.wallet,
				EmployeeSelfSubscriptionCancelEnabled: test.cancel, EmployeeSelfSubscriptionRenewalEnabled: test.renewal,
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

			session := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+"/self/api/v1/session", "", "", cookie, ""), 200)
			features, ok := session["features"].(map[string]any)
			if !ok || features["employee_self_plan_catalog"] != true || features["employee_self_subscription_status"] != true ||
				features["employee_self_wallet_balance"] != test.wallet || features["employee_self_subscription_cancel"] != test.cancel ||
				features["employee_self_subscription_renewal"] != test.renewal || features["employee_self_plan_purchase"] != false {
				t.Fatalf("cross-capability session=%+v", session)
			}
			catalog := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, server.URL, "?currency=USD", "", "", cookie))
			if catalog.Currency != "USD" || catalog.Available || len(catalog.Items) != 0 || catalog.NextCursor != nil {
				t.Fatalf("commercial-off catalog=%+v", catalog)
			}
			subscriptions := readSelfSubscriptionPage(t, selfSubscriptionRequest(t, server.URL, "?limit=20", "", "", cookie))
			if len(subscriptions.Items) != 0 || subscriptions.NextCursor != nil {
				t.Fatalf("empty own subscriptions=%+v", subscriptions)
			}
			for _, route := range []string{"/self/api/v1/billing/plan-purchase-quotes", "/self/api/v1/billing/subscriptions"} {
				response := selfRequestTest(t, http.MethodPost, server.URL+route, `{}`, server.URL, cookie, csrf)
				if response.StatusCode != http.StatusNotFound {
					t.Errorf("disabled purchase route %s status=%d", route, response.StatusCode)
				}
				response.Body.Close()
			}
			for _, probe := range []struct {
				route  string
				status int
			}{
				{test.enabledRoute, http.StatusMethodNotAllowed},
				{test.disabledRoute, http.StatusNotFound},
			} {
				response := selfRequestTest(t, http.MethodGet, server.URL+probe.route, "", "", cookie, "")
				if response.StatusCode != probe.status || (probe.status == http.StatusMethodNotAllowed && response.Header.Get("Allow") != http.MethodPost) {
					t.Errorf("independent operation route %s status=%d allow=%q", probe.route, response.StatusCode, response.Header.Get("Allow"))
				}
				response.Body.Close()
			}
		})
	}
}

func TestSelfPlanCatalogCurrentCursorRestartAndFailure(t *testing.T) {
	f := newSelfPlanCatalogFixture(t)
	seedSelfPlanCatalog(t, f)
	off := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD", "", "", f.cookie))
	if off.Currency != "USD" || off.Available || len(off.Items) != 0 || off.NextCursor != nil {
		t.Fatalf("off=%+v", off)
	}
	setSelfPlanCatalogCommercial(t, f, 1)
	first := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if first.Currency != "USD" || !first.Available || len(first.Items) != 1 || first.Items[0].PlanID != "plan-a" ||
		first.Items[0].Name != "Current monthly" || first.Items[0].PriceMicro != "13" || first.Items[0].CreditMicro != "29" ||
		first.Items[0].Revision != 2 || first.NextCursor == nil || strings.Contains(*first.NextCursor, "plan-a") {
		t.Fatalf("first=%+v", first)
	}
	query := "?currency=USD&limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	second := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, query, "", "", f.cookie))
	if !second.Available || len(second.Items) != 1 || second.Items[0].PlanID != "plan-b" || second.Items[0].Interval != "one_time" || second.NextCursor != nil {
		t.Fatalf("second=%+v", second)
	}
	euro := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=EUR", "", "", f.cookie))
	if !euro.Available || len(euro.Items) != 1 || euro.Items[0].PlanID != "plan-d" {
		t.Fatalf("other currency=%+v", euro)
	}
	tampered := []byte(*first.NextCursor)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}
	for _, bad := range []string{"?currency=EUR&limit=1&cursor=" + url.QueryEscape(*first.NextCursor), "?currency=USD&limit=2&cursor=" + url.QueryEscape(*first.NextCursor), "?currency=USD&limit=1&cursor=" + url.QueryEscape(string(tampered))} {
		readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, bad, "", "", f.cookie), 400)
	}
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, secret)
	readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, query, "", "", otherCookie), 400)
	selector := strings.Split(f.cookie.Value, ".")[0]
	expired, err := f.app.encodeSelfPlanCatalogCursor(selfPlanCatalogCursor{Version: 1, EmployeeID: f.id, Session: selector, Currency: "USD", IssuedAt: time.Now().UTC().Add(-16 * time.Minute).Format(time.RFC3339Nano), Limit: 1, LastID: "plan-a"})
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(expired), "", "", f.cookie), 400)
	otherDir := t.TempDir()
	if err := Initialize(context.Background(), otherDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	otherSecrets, err := loadSecrets(otherDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{secrets: otherSecrets}).decodeSelfPlanCatalogCursor(*first.NextCursor, selfSession{EmployeeID: f.id, Selector: selector}, selfPlanCatalogParams{currency: "USD", limit: 1}, time.Now().UTC()); err == nil {
		t.Fatal("foreign installation cursor accepted")
	}
	setSelfPlanCatalogCommercial(t, f, 0)
	off = readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, query, "", "", f.cookie))
	if off.Available || len(off.Items) != 0 || off.NextCursor != nil {
		t.Fatalf("disabled continuation=%+v", off)
	}
	setSelfPlanCatalogCommercial(t, f, 1)
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfPlanCatalogEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	page := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, server.URL, query, "", "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].PlanID != "plan-b" {
		t.Fatalf("restart page=%+v", page)
	}
	if _, err := reopened.store.db.Exec(`DROP INDEX financial_subscriptions_account_idx`); err != nil {
		t.Fatal(err)
	}
	value := readSelfWalletResponse(t, requestSelfPlanCatalog(t, server.URL, "?currency=USD", "", "", f.cookie), 503)
	if value["items"] != nil {
		t.Fatalf("storage error leaked items=%+v", value)
	}
	logout := selfRequestTest(t, http.MethodDelete, server.URL+"/self/api/v1/sessions", "", server.URL, f.cookie, f.csrf)
	logout.Body.Close()
	if logout.StatusCode != 204 {
		t.Fatalf("logout status=%d", logout.StatusCode)
	}
	readSelfWalletResponse(t, requestSelfPlanCatalog(t, server.URL, query, "", "", f.cookie), 401)
}

func TestSelfPlanCatalogCursorAcceptsExistingPlanIDDomain(t *testing.T) {
	f := newSelfPlanCatalogFixture(t)
	ids := []string{"plan one", "plan two", "套餐 三", "套餐 四"}
	for _, id := range ids {
		if _, err := f.app.store.db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, id, "Current plan", "USD", 10, 20, "monthly", 1, 1, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	setSelfPlanCatalogCommercial(t, f, 1)
	query := "?currency=USD&limit=1"
	for index, id := range ids {
		page := readSelfPlanCatalogPage(t, requestSelfPlanCatalog(t, f.server.URL, query, "", "", f.cookie))
		if !page.Available || len(page.Items) != 1 || page.Items[0].PlanID != id || (page.NextCursor == nil) != (index == len(ids)-1) {
			t.Fatalf("page %d=%+v", index, page)
		}
		if page.NextCursor != nil {
			query = "?currency=USD&limit=1&cursor=" + url.QueryEscape(*page.NextCursor)
		}
	}

	selector := strings.Split(f.cookie.Value, ".")[0]
	for _, badID := range []string{"", " plan", "plan ", "plan\x00id", strings.Repeat("x", 257)} {
		cursor, err := f.app.encodeSelfPlanCatalogCursor(selfPlanCatalogCursor{
			Version: 1, EmployeeID: f.id, Session: selector, Currency: "USD",
			IssuedAt: time.Now().UTC().Format(time.RFC3339Nano), Limit: 1, LastID: badID,
		})
		if err != nil {
			t.Fatal(err)
		}
		readSelfWalletResponse(t, requestSelfPlanCatalog(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(cursor), "", "", f.cookie), 400)
	}
}
