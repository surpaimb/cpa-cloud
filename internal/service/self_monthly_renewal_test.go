package service

// Independently authored HTTP and transaction checks for
// docs/employee-self-monthly-renewal-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfMonthlyRenewalFixture(t *testing.T) selfPurchaseFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfSubscriptionRenewalEnabled: true,
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
	return selfPurchaseFixture{
		selfWalletFixture: selfWalletFixture{app: app, server: server, dir: dir, id: employee.ID, cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF},
		planID:            "self-monthly-plan",
	}
}

func seedSelfMonthlyPredecessor(t *testing.T, f selfPurchaseFixture, id string) time.Time {
	t.Helper()
	f.seedPlan(t, 40, 10, "monthly")
	f.setCommercial(t, true)
	f.fundWallet(t, "monthly-fund-"+id, 100)
	var accountID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM financial_accounts WHERE owner_kind='employee' AND employee_id=? AND currency='USD'`, f.id).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, id, accountID, f.planID, 1, 10, 20, "USD", "monthly", "active", start.Format(time.RFC3339Nano), end.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Add(time.Second)
	f.app.selfMonthlyRenewalNow = func() time.Time { return observed }
	return observed
}

func selfMonthlyQuote(t *testing.T, f selfPurchaseFixture, id string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/"+id+"/renewal-quotes", "", f.server.URL, cookie, csrf)
}

func selfMonthlyRenew(t *testing.T, f selfPurchaseFixture, id, op, token, password string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":%q,"quote_token":%q,"current_password":%q}`, op, token, password)
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/"+id+"/renew", body, f.server.URL, cookie, csrf)
}

func selfMonthlyToken(t *testing.T, f selfPurchaseFixture, id string) string {
	t.Helper()
	value := readSelfWalletResponse(t, selfMonthlyQuote(t, f, id, f.cookie, f.csrf), 201)
	if len(value) != 10 || value["predecessor_id"] != id || value["plan_id"] != f.planID || value["revision"] != float64(1) ||
		value["currency"] != "USD" || value["interval"] != "monthly" || value["price_micro"] != "40" || value["credit_micro"] != "10" ||
		value["predecessor_period_end_at"] != "2026-09-30T08:00:00.000000000Z" {
		t.Fatal("renewal quote shape mismatch")
	}
	token, ok := value["quote_token"].(string)
	if !ok || token == "" || len(token) > selfMonthlyRenewalTokenMax {
		t.Fatal("invalid renewal quote token")
	}
	return token
}

func TestSelfMonthlyRenewalGatesAndStrictPaths(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfSubscriptionRenewalEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionRenewalEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionRenewalEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfSubscriptionRenewalEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	old := newSelfPurchaseFixture(t)
	off := selfMonthlyQuote(t, old, "old", old.cookie, old.csrf)
	if off.StatusCode != 404 {
		t.Fatalf("flag-off status=%d", off.StatusCode)
	}
	off.Body.Close()
	f := newSelfMonthlyRenewalFixture(t)
	seedSelfMonthlyPredecessor(t, f, "monthly-old")
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_subscription_renewal"] != true {
		t.Fatalf("renewal capability=%+v", features)
	}
	readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", nil, ""), 401)
	readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", f.adminCookie, f.adminCSRF), 401)
	readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", f.cookie, "bad-csrf"), 403)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/monthly-old/renewal-quotes?", "", f.server.URL, f.cookie, f.csrf), 400)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/monthly-old/renewal-quotes", `{}`, f.server.URL, f.cookie, f.csrf), 400)
	for _, path := range []string{
		"/self/api/v1/billing/subscriptions/monthly-old/renew/extra",
		"/self/api/v1/billing/subscriptions/monthly-old/renewal-quotes/extra",
		"/self/api/v1/billing/subscriptions/monthly-old%2Fother/renew",
		"/self/api/v1/billing/subscriptions/%2E%2E/renew",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			readSelfWalletResponse(t, selfRequestTest(t, method, f.server.URL+path, "", f.server.URL, f.cookie, f.csrf), 400)
		}
	}
	response := selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions/monthly-old/renew", "", f.server.URL, f.cookie, f.csrf)
	if response.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("Allow=%q", response.Header.Get("Allow"))
	}
	readSelfWalletResponse(t, response, 405)
	for _, body := range []string{
		`{"operation_id":"x","quote_token":"x"}`,
		`{"operation_id":"x","quote_token":"x","current_password":"x","extra":"x"}`,
		`{"operation_id":"x","operation_id":"y","quote_token":"x","current_password":"x"}`,
	} {
		readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/monthly-old/renew", body, f.server.URL, f.cookie, f.csrf), 400)
	}
	chunked, err := http.NewRequest(http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/monthly-old/renew", strings.NewReader(`{"operation_id":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	chunked.ContentLength = -1
	chunked.TransferEncoding = []string{"chunked"}
	chunked.Header.Set("Origin", f.server.URL)
	chunked.Header.Set("X-Self-Request", "1")
	chunked.Header.Set("X-CSRF-Token", f.csrf)
	chunked.AddCookie(f.cookie)
	chunkedResponse, err := http.DefaultClient.Do(chunked)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, chunkedResponse, 400)
}

func TestSelfMonthlyRenewalCommitReplayAndNoNewWallet(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	observed := seedSelfMonthlyPredecessor(t, f, "monthly-old")
	token := selfMonthlyToken(t, f, "monthly-old")
	secondToken := selfMonthlyToken(t, f, "monthly-old")
	if secondToken == token {
		t.Fatal("independent quotes reused the same nonce")
	}
	// Persisting expiry changes status/revision, but the quote binds the old
	// frozen end rather than mutable storage revision.
	if _, err := f.app.store.db.Exec(`UPDATE financial_subscriptions SET status='expired',revision=2 WHERE id='monthly-old'`); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "self-monthly-op", token, "wrong-password", f.cookie, f.csrf), 401)
	first := readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "self-monthly-op", token, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	if len(first) != 12 || first["replay"] != false || first["predecessor_id"] != "monthly-old" || first["price_micro"] != "40" || first["credit_micro"] != "10" || first["started_at"] != observed.Format(time.RFC3339Nano) {
		t.Fatalf("first=%+v", first)
	}
	if c, l, e, s := f.operationCounts(t, "self-monthly-op"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("facts=%d/%d/%d/%d", c, l, e, s)
	}
	var links, accounts int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals WHERE predecessor_id='monthly-old' AND operation_id='self-monthly-op'`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("accounts=%d err=%v", accounts, err)
	}
	if balance, err := financial.NewLedger(f.app.store.db).Balance(context.Background(), selfPurchaseOwner(f.id), "USD"); err != nil || balance.AmountMicro != 70 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_plans SET enabled=0,price_micro=99,revision=2 WHERE id=?`, f.planID); err != nil {
		t.Fatal(err)
	}
	f.setCommercial(t, false)
	f.app.selfMonthlyRenewalNow = func() time.Time { return observed.Add(6 * time.Minute) }
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "new-monthly-op", token, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	second := readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "self-monthly-op", secondToken, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if second["replay"] != true || second["subscription_id"] != first["subscription_id"] {
		t.Fatalf("replay=%+v first=%+v", second, first)
	}
	if c, l, e, s := f.operationCounts(t, "self-monthly-op"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("replay facts=%d/%d/%d/%d", c, l, e, s)
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "self-monthly-op", token, "wrong-password", f.cookie, f.csrf), 401)
}

func TestSelfMonthlyRenewalUnknownCommitAndRollback(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	seedSelfMonthlyPredecessor(t, f, "monthly-old")
	token := selfMonthlyToken(t, f, "monthly-old")
	f.app.selfMonthlyRenewalCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic unknown commit")
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "monthly-uncertain", token, selfPurchaseTestPassword, f.cookie, f.csrf), 503)
	f.app.selfMonthlyRenewalCommit = nil
	if c, l, e, s := f.operationCounts(t, "monthly-uncertain"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("unknown commit facts=%d/%d/%d/%d", c, l, e, s)
	}
	value := readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "monthly-uncertain", token, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if value["replay"] != true {
		t.Fatalf("uncertain replay=%+v", value)
	}
}

func TestSelfMonthlyRenewalQuoteBindingPlanAndExistingCurrencyWallet(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	seedSelfMonthlyPredecessor(t, f, "monthly-old")
	token := selfMonthlyToken(t, f, "monthly-old")
	newCookie, newCSRF := selfLoginTest(t, f.server.URL, f.id, selfPurchaseTestPassword)
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "cross-session", token, selfPurchaseTestPassword, newCookie, newCSRF), 400)
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "changed-token", token+"a", selfPurchaseTestPassword, f.cookie, f.csrf), 400)
	if _, err := f.app.store.db.Exec(`UPDATE financial_plans SET currency='EUR',price_micro=45,credit_micro=12,revision=2,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), f.planID); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "stale-plan", token, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	if c, l, e, s := f.operationCounts(t, "stale-plan"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("stale plan facts=%d/%d/%d/%d", c, l, e, s)
	}
	_, err := financial.NewLedger(f.app.store.db).Post(context.Background(), financial.Post{
		OperationID: "monthly-eur-fund", Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: f.id},
		ResourceKind: "adjustment", ResourceID: "monthly-eur-fund", ObservedAt: time.Now().UTC(),
		Entries: []financial.EntryInput{{Owner: selfPurchaseOwner(f.id), Currency: "EUR", Kind: financial.EntryAdjustmentCredit, AmountMicro: 100,
			ResourceKind: "adjustment", ResourceID: "monthly-eur-fund"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Add(time.Minute)
	f.app.selfMonthlyRenewalNow = func() time.Time { return observed }
	value := readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", f.cookie, f.csrf), 201)
	if value["currency"] != "EUR" || value["price_micro"] != "45" || value["credit_micro"] != "12" || value["revision"] != float64(2) {
		t.Fatal("EUR quote snapshot mismatch")
	}
	result := readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "eur-renewal", value["quote_token"].(string), selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	if result["currency"] != "EUR" || result["price_micro"] != "45" {
		t.Fatalf("EUR renewal=%+v", result)
	}
	if balance, err := financial.NewLedger(f.app.store.db).Balance(context.Background(), selfPurchaseOwner(f.id), "EUR"); err != nil || balance.AmountMicro != 67 {
		t.Fatalf("EUR balance=%+v err=%v", balance, err)
	}
	if balance, err := financial.NewLedger(f.app.store.db).Balance(context.Background(), selfPurchaseOwner(f.id), "USD"); err != nil || balance.AmountMicro != 100 {
		t.Fatalf("USD balance=%+v err=%v", balance, err)
	}
}

func TestSelfMonthlyRenewalFinalSessionAndRollback(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	seedSelfMonthlyPredecessor(t, f, "monthly-old")
	token := selfMonthlyToken(t, f, "monthly-old")
	f.app.selfMonthlyRenewalBeforeTx = func() {
		if _, err := f.app.store.db.Exec(`DELETE FROM employee_self_sessions WHERE selector=?`, strings.Split(f.cookie.Value, ".")[0]); err != nil {
			t.Fatal(err)
		}
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "revoked-before-tx", token, selfPurchaseTestPassword, f.cookie, f.csrf), 401)
	f.app.selfMonthlyRenewalBeforeTx = nil
	if c, l, e, s := f.operationCounts(t, "revoked-before-tx"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("revoked facts=%d/%d/%d/%d", c, l, e, s)
	}
	newCookie, newCSRF := selfLoginTest(t, f.server.URL, f.id, selfPurchaseTestPassword)
	newToken := readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-old", newCookie, newCSRF), 201)["quote_token"].(string)
	f.app.selfMonthlyRenewalCommit = func(*sql.Tx) error { return errors.New("synthetic failed commit") }
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "rolled-back", newToken, selfPurchaseTestPassword, newCookie, newCSRF), 503)
	f.app.selfMonthlyRenewalCommit = nil
	if c, l, e, s := f.operationCounts(t, "rolled-back"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("rollback facts=%d/%d/%d/%d", c, l, e, s)
	}
	var links int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals WHERE predecessor_id='monthly-old'`).Scan(&links); err != nil || links != 0 {
		t.Fatalf("rollback links=%d err=%v", links, err)
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-old", "rolled-back", newToken, selfPurchaseTestPassword, newCookie, newCSRF), 201)
}

func TestSelfMonthlyRenewalSupersedesArmedOneShotInSameCommit(t *testing.T) {
	f := newSelfMonthlyRenewalFixture(t)
	f.seedPlan(t, 40, 10, "monthly")
	f.setCommercial(t, true)
	f.fundWallet(t, "monthly-armed-fund", 100)
	var accountID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM financial_accounts WHERE owner_kind='employee' AND employee_id=? AND currency='USD'`, f.id).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	due := start.AddDate(0, 1, 0)
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
		"monthly-armed", accountID, f.planID, 1, 40, 10, "USD", "monthly", "active", start.Format(time.RFC3339Nano), due.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	status, response := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/subscriptions/monthly-armed/one-shot-renewal", `{"operation_id":"20000000-0000-4000-8000-000000009611","expected_revision":1}`, f.adminCookie, f.adminCSRF, f.server.URL, nil)
	if status != 200 {
		t.Fatalf("arm status=%d body=%s", status, response)
	}
	f.app.selfMonthlyRenewalNow = func() time.Time { return due.Add(time.Hour) }
	quoted := readSelfWalletResponse(t, selfMonthlyQuote(t, f, "monthly-armed", f.cookie, f.csrf), 201)
	token, ok := quoted["quote_token"].(string)
	if !ok || token == "" || quoted["predecessor_period_end_at"] != due.Format("2006-01-02T15:04:05.000000000Z") {
		t.Fatal("armed renewal quote mismatch")
	}
	readSelfWalletResponse(t, selfMonthlyRenew(t, f, "monthly-armed", "monthly-armed-renew", token, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	var state, reason string
	var revision int
	if err := f.app.store.db.QueryRow(`SELECT state,reason,revision FROM financial_subscription_one_shot_renewals WHERE predecessor_id='monthly-armed'`).Scan(&state, &reason, &revision); err != nil || state != "superseded" || reason != "manual_renewal" || revision != 2 {
		t.Fatalf("reservation state=%q reason=%q revision=%d err=%v", state, reason, revision, err)
	}
	if c, l, e, s := f.operationCounts(t, "monthly-armed-renew"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("armed renewal facts=%d/%d/%d/%d", c, l, e, s)
	}
}
