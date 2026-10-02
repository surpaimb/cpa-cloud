package service

// Independently authored HTTP tests for docs/employee-self-subscription-renewal-links-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfRenewalLinksFixture(t *testing.T, enabled bool, monthlyEnabled ...bool) selfWalletFixture {
	t.Helper()
	withMonthly := len(monthlyEnabled) > 0 && monthlyEnabled[0]
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfSubscriptionRenewalLinksEnabled: enabled,
		EmployeeSelfWalletBalanceEnabled:            withMonthly, EmployeeSelfSubscriptionRenewalEnabled: withMonthly,
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
	return selfWalletFixture{app: app, server: server, dir: dir, id: employee.ID, cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func TestSelfRenewalLinksAndMonthlyRenewalGuardsTogether(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, true, true)
	first, second := seedSelfRenewalLinks(t, f)
	root := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "", f.cookie), 200)
	if len(root) != 3 || root["subscription_id"] != first || root["predecessor_id"] != nil || root["successor_id"] != second {
		t.Fatalf("both-flags root=%+v", root)
	}
	leaf := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, second+"/renewal-links", "", "", f.cookie), 200)
	if len(leaf) != 3 || leaf["predecessor_id"] != first || leaf["successor_id"] != nil {
		t.Fatalf("both-flags leaf=%+v", leaf)
	}
	postLinks := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/"+first+"/renewal-links", "", f.server.URL, f.cookie, f.csrf)
	if postLinks.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("both-flags renewal-links Allow=%q", postLinks.Header.Get("Allow"))
	}
	readSelfWalletResponse(t, postLinks, 405)
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links/extra", "", "", f.cookie), 400)
	getRenew := selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions/"+first+"/renew", "", f.server.URL, f.cookie, f.csrf)
	if getRenew.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("monthly renewal Allow=%q", getRenew.Header.Get("Allow"))
	}
	readSelfWalletResponse(t, getRenew, 405)
}

func TestSelfRenewalLinksEmployeeActorAfterSelfRenewal(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, true, true)
	commercial := financial.NewCommercial(f.app.store.db)
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	if _, err := commercial.SetEnabled(context.Background(), selfSnapshotMeta(t, f, "links-employee-enable", start.Add(-2*time.Hour)), 1, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(context.Background(), financial.CreatePlan{
		Meta: selfSnapshotMeta(t, f, "links-employee-plan", start.Add(-2*time.Hour)), Name: "Synthetic employee renewal monthly",
		Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	activityPost(t, f, "links-employee-fund", owner, "USD", financial.EntryAdjustmentCredit, 100, start.Add(-time.Hour))
	root, _, err := commercial.PurchaseSubscription(context.Background(), financial.PurchaseSubscription{
		Meta: selfSnapshotMeta(t, f, "links-employee-buy", start), Owner: owner, PlanID: plan.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	monthly := selfPurchaseFixture{selfWalletFixture: f, planID: plan.ID}
	quote := readSelfWalletResponse(t, selfMonthlyQuote(t, monthly, root.ID, f.cookie, f.csrf), 201)
	token, ok := quote["quote_token"].(string)
	if !ok || token == "" {
		t.Fatalf("missing employee quote token: %+v", quote)
	}
	renewed := readSelfWalletResponse(t, selfMonthlyRenew(t, monthly, root.ID, "links-employee-renew", token, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	successor, ok := renewed["subscription_id"].(string)
	if !ok || successor == "" {
		t.Fatalf("missing employee successor: %+v", renewed)
	}
	for _, item := range []struct{ id, prior, next string }{
		{root.ID, "", successor}, {successor, root.ID, ""},
	} {
		got := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, item.id+"/renewal-links", "", "", f.cookie), 200)
		if len(got) != 3 || got["subscription_id"] != item.id ||
			(item.prior == "" && got["predecessor_id"] != nil || item.prior != "" && got["predecessor_id"] != item.prior) ||
			(item.next == "" && got["successor_id"] != nil || item.next != "" && got["successor_id"] != item.next) {
			t.Fatalf("typed employee links=%+v expected=%+v", got, item)
		}
	}
}

func seedSelfRenewalLinks(t *testing.T, f selfWalletFixture) (string, string) {
	t.Helper()
	commercial := financial.NewCommercial(f.app.store.db)
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	if _, err := commercial.SetEnabled(context.Background(), selfSnapshotMeta(t, f, "links-enable", start.Add(-3*time.Hour)), 1, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(context.Background(), financial.CreatePlan{Meta: selfSnapshotMeta(t, f, "links-plan", start.Add(-2*time.Hour)), Name: "Synthetic monthly", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	activityPost(t, f, "links-fund", owner, "USD", financial.EntryAdjustmentCredit, 100, start.Add(-time.Hour))
	first, _, err := commercial.PurchaseSubscription(context.Background(), financial.PurchaseSubscription{Meta: selfSnapshotMeta(t, f, "links-buy", start), Owner: owner, PlanID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := commercial.RenewSubscription(context.Background(), financial.RenewSubscription{Meta: selfSnapshotMeta(t, f, "links-renew", time.Now().UTC()), ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	return first.ID, second.ID
}

func selfRenewalLinksRequest(t *testing.T, f selfWalletFixture, suffix, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions/"+suffix, body, origin, cookie, "")
}

func TestSelfRenewalLinksGateStrictPathAndAuth(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfSubscriptionRenewalLinksEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionRenewalLinksEnabled: true},
		{EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionRenewalLinksEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	off := newSelfRenewalLinksFixture(t, false)
	response := selfRenewalLinksRequest(t, off, "missing/renewal-links", "", "", off.cookie)
	if response.StatusCode != 404 {
		t.Fatalf("feature-off status=%d", response.StatusCode)
	}
	response.Body.Close()
	f := newSelfRenewalLinksFixture(t, true)
	first, second := seedSelfRenewalLinks(t, f)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_subscription_renewal_links"] != true || features["employee_self_wallet_balance"] != false {
		t.Fatalf("independent feature gate=%+v", features)
	}
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "", cookie), 401)
	}
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "http://evil.invalid", f.cookie), 403)
	for _, suffix := range []string{
		first + "/renewal-links?", first + "/renewal-links?x=1", first + "/renewal-links/extra",
		"a%2Fb/renewal-links", "%2e%2e/renewal-links", "a%252Fb/renewal-links", "a%5Cb/renewal-links",
	} {
		readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, suffix, "", "", f.cookie), 400)
	}
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", `{}`, "", f.cookie), 400)
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, "missing/renewal-links", "", "", f.cookie), 404)
	post := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/"+second+"/renewal-links", `{}`, f.server.URL, f.cookie, f.csrf)
	if post.Header.Get("Allow") != http.MethodGet {
		t.Fatalf("Allow=%q", post.Header.Get("Allow"))
	}
	readSelfWalletResponse(t, post, 405)
}

func TestSelfRenewalLinksThreeFieldsHistoricalAndCommitFailure(t *testing.T) {
	f := newSelfRenewalLinksFixture(t, true)
	first, second := seedSelfRenewalLinks(t, f)
	var before int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	root := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "", f.cookie), 200)
	if len(root) != 3 || root["subscription_id"] != first || root["predecessor_id"] != nil || root["successor_id"] != second {
		t.Fatalf("root=%+v", root)
	}
	leaf := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, second+"/renewal-links", "", "", f.cookie), 200)
	if len(leaf) != 3 || leaf["subscription_id"] != second || leaf["predecessor_id"] != first || leaf["successor_id"] != nil {
		t.Fatalf("leaf=%+v", leaf)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_plans SET enabled=0,price_micro=12,revision=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := financial.NewCommercial(f.app.store.db).SetEnabled(context.Background(), selfSnapshotMeta(t, f, "links-disable", time.Now().UTC()), 2, false); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, second+"/renewal-links", "", "", f.cookie), 200)
	var after int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&after); err != nil || after != before {
		t.Fatalf("read changed financial entries %d to %d, err=%v", before, after, err)
	}
	f.app.selfRenewalLinksCommit = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
	failed := readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "", f.cookie), 503)
	if _, leaked := failed["successor_id"]; leaked {
		t.Fatalf("partial link leaked: %+v", failed)
	}
	f.app.selfRenewalLinksCommit = nil
	if _, err := f.app.store.db.Exec(`DROP TRIGGER financial_subscription_renewals_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_subscription_renewals SET created_at='malformed' WHERE predecessor_id=?`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`CREATE TRIGGER IF NOT EXISTS financial_subscription_renewals_no_update BEFORE UPDATE ON financial_subscription_renewals BEGIN SELECT RAISE(ABORT,'financial subscription renewals are immutable'); END`); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfRenewalLinksRequest(t, f, first+"/renewal-links", "", "", f.cookie), 503)
}
