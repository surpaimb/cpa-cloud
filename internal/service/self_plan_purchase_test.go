package service

// Independently authored HTTP/transaction tests for
// docs/employee-self-plan-purchase-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

const selfPurchaseTestPassword = "a-long-self-password"

type selfPurchaseFixture struct {
	selfWalletFixture
	planID string
}

func newSelfPurchaseFixture(t *testing.T) selfPurchaseFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true,
		EmployeeSelfPlanCatalogEnabled: true, EmployeeSelfPlanPurchaseEnabled: true,
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
		planID:            "self-purchase-plan",
	}
}

func (f selfPurchaseFixture) seedPlan(t *testing.T, price, credit int64, interval string) {
	t.Helper()
	_, err := f.app.store.db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at)
		VALUES(?,?,?,?,?,?,1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, f.planID, "Once", "USD", price, credit, interval)
	if err != nil {
		t.Fatal(err)
	}
}

func (f selfPurchaseFixture) setCommercial(t *testing.T, enabled bool) {
	t.Helper()
	value := 0
	if enabled {
		value = 1
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_settings SET enabled=?,revision=revision+1,updated_at=? WHERE singleton=1`, value, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func (f selfPurchaseFixture) fundWallet(t *testing.T, operationID string, amount int64) {
	t.Helper()
	_, err := financial.NewLedger(f.app.store.db).Post(context.Background(), financial.Post{
		OperationID: operationID, Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: f.id},
		ResourceKind: "adjustment", ResourceID: operationID, ObservedAt: time.Now().UTC(),
		Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, Currency: "USD", Kind: financial.EntryAdjustmentCredit, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: operationID}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f selfPurchaseFixture) quote(t *testing.T, body string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/plan-purchase-quotes", body, f.server.URL, cookie, csrf)
}

func (f selfPurchaseFixture) buy(t *testing.T, operationID, token, password string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":%q,"quote_token":%q,"current_password":%q}`, operationID, token, password)
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions", body, f.server.URL, cookie, csrf)
}

func (f selfPurchaseFixture) quoteToken(t *testing.T) string {
	t.Helper()
	value := readSelfWalletResponse(t, f.quote(t, fmt.Sprintf(`{"plan_id":%q}`, f.planID), f.cookie, f.csrf), 201)
	if len(value) != 8 || value["plan_id"] != f.planID || value["currency"] != "USD" || value["interval"] != "one_time" || value["price_micro"] != "40" || value["credit_micro"] != "10" || value["revision"] != float64(1) {
		t.Fatalf("quote body=%+v", value)
	}
	token, ok := value["quote_token"].(string)
	if !ok || token == "" || len(token) > selfPlanQuoteMax {
		t.Fatalf("invalid quote token: %+v", value)
	}
	return token
}

func (f selfPurchaseFixture) operationCounts(t *testing.T, id string) (int, int, int, int) {
	t.Helper()
	queries := []string{
		`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?`,
		`SELECT COUNT(*) FROM financial_operations WHERE operation_id=?`,
		`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`,
		`SELECT COUNT(*) FROM financial_subscriptions WHERE id=(SELECT resource_id FROM financial_commercial_operations WHERE operation_id=?)`,
	}
	var counts [4]int
	for index, query := range queries {
		if err := f.app.store.db.QueryRow(query, id).Scan(&counts[index]); err != nil {
			t.Fatal(err)
		}
	}
	return counts[0], counts[1], counts[2], counts[3]
}

func selfLoginTest(t *testing.T, baseURL, employeeID, password string) (*http.Cookie, string) {
	t.Helper()
	body := fmt.Sprintf(`{"employee_id":%q,"password":%q}`, employeeID, password)
	response := selfRequestTest(t, http.MethodPost, baseURL+"/self/api/v1/sessions", body, baseURL, nil, "")
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatalf("self login status=%d body=%s", response.StatusCode, readBody(response))
	}
	value := readSelfWalletResponse(t, response, 200)
	csrf, ok := value["csrf_token"].(string)
	if !ok || csrf == "" {
		t.Fatalf("missing login csrf: %+v", value)
	}
	return response.Cookies()[0], csrf
}

func TestSelfPlanPurchaseGatesAndStrictBodies(t *testing.T) {
	if _, err := Open(context.Background(), Config{EmployeeSelfPlanPurchaseEnabled: true}); err == nil {
		t.Fatal("purchase flag accepted without prerequisites")
	}
	for _, cfg := range []Config{
		{EmployeeSelfServiceEnabled: true, EmployeeSelfPlanPurchaseEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfPlanPurchaseEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfPlanCatalogEnabled: true, EmployeeSelfPlanPurchaseEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfPlanCatalogEnabled: true}} {
		cfg.DataDir, cfg.Listen, cfg.Version = dir, "127.0.0.1:0", "test"
		app, err := Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		for _, path := range []string{"/self/api/v1/billing/plan-purchase-quotes", "/self/api/v1/billing/subscriptions"} {
			response := selfRequestTest(t, http.MethodPost, server.URL+path, `{}`, server.URL, nil, "")
			if response.StatusCode != 404 {
				t.Fatalf("disabled route %s status=%d", path, response.StatusCode)
			}
			response.Body.Close()
		}
		server.Close()
		_ = app.Close()
	}
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	for _, path := range []string{"/billing/plan-purchase-quotes", "/billing/subscriptions"} {
		for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
			response := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1"+path, `{}`, f.server.URL, cookie, f.csrf)
			readSelfWalletResponse(t, response, 401)
		}
		response := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1"+path, `{}`, "http://evil.invalid", f.cookie, f.csrf)
		readSelfWalletResponse(t, response, 403)
		response = selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1"+path, `{}`, f.server.URL, f.cookie, "wrong")
		readSelfWalletResponse(t, response, 403)
		response = selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1"+path+"?employee_id=other", `{}`, f.server.URL, f.cookie, f.csrf)
		readSelfWalletResponse(t, response, 400)
	}
	for _, body := range []string{`{}`, `{"plan_id":"x","currency":"USD"}`, `{"plan_id":"x","plan_id":"x"}`, `{"plan_id":3}`, `[]`, `{"plan_id":"x"} {}`} {
		readSelfWalletResponse(t, f.quote(t, body, f.cookie, f.csrf), 400)
	}
	for _, body := range []string{`{}`, `{"operation_id":"x","quote_token":"x"}`, `{"operation_id":"x","quote_token":"x","current_password":"x","employee_id":"other"}`, `{"operation_id":"x","quote_token":"x","current_password":"x","current_password":"x"}`} {
		readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions", body, f.server.URL, f.cookie, f.csrf), 400)
	}
	readSelfWalletResponse(t, f.quote(t, fmt.Sprintf(`{"plan_id":%q}`, f.planID), f.cookie, f.csrf), 409) // switch off
	f.setCommercial(t, true)
	readSelfWalletResponse(t, f.quote(t, fmt.Sprintf(`{"plan_id":%q}`, f.planID), f.cookie, f.csrf), 409) // no direct wallet
	f.fundWallet(t, "self-purchase-fund", 100)
	readSelfWalletResponse(t, f.quote(t, `{"plan_id":"missing"}`, f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, f.quote(t, fmt.Sprintf(`{"plan_id":%q}`, f.planID), f.cookie, "wrong"), 403)
}

func TestSelfPlanPurchaseCommitsAndReplaysAfterQuoteAndNewGatesExpire(t *testing.T) {
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	f.setCommercial(t, true)
	f.fundWallet(t, "self-purchase-fund", 100)
	issued := time.Now().UTC()
	f.app.selfPlanPurchaseNow = func() time.Time { return issued }
	token := f.quoteToken(t)
	readSelfWalletResponse(t, f.buy(t, "self-buy-one", token, "wrong-password", f.cookie, f.csrf), 401)
	if c, l, e, s := f.operationCounts(t, "self-buy-one"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("wrong-password facts=%d/%d/%d/%d", c, l, e, s)
	}
	first := readSelfWalletResponse(t, f.buy(t, "self-buy-one", token, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	if first["replay"] != false || first["price_micro"] != "40" || first["credit_micro"] != "10" {
		t.Fatalf("first=%+v", first)
	}
	if c, l, e, s := f.operationCounts(t, "self-buy-one"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("committed facts=%d/%d/%d/%d", c, l, e, s)
	}
	var accounts int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&accounts); err != nil || accounts != 1 {
		t.Fatalf("account count=%d err=%v", accounts, err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_plans SET enabled=0,price_micro=99,revision=2,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), f.planID); err != nil {
		t.Fatal(err)
	}
	f.setCommercial(t, false)
	f.app.selfPlanPurchaseNow = func() time.Time { return issued.Add(6 * time.Minute) }
	readSelfWalletResponse(t, f.buy(t, "self-buy-new", token, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	second := readSelfWalletResponse(t, f.buy(t, "self-buy-one", token, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if second["replay"] != true || second["subscription_id"] != first["subscription_id"] {
		t.Fatalf("replay=%+v first=%+v", second, first)
	}
	if c, l, e, s := f.operationCounts(t, "self-buy-one"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("replay facts=%d/%d/%d/%d", c, l, e, s)
	}
	if balance, err := financial.NewLedger(f.app.store.db).Balance(context.Background(), financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, "USD"); err != nil || balance.AmountMicro != 70 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
}

func TestSelfPlanPurchaseFinalAuthorizationAndUnknownCommit(t *testing.T) {
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	f.setCommercial(t, true)
	f.fundWallet(t, "self-purchase-fund", 100)
	token := f.quoteToken(t)
	f.app.selfPlanPurchaseBeforeTx = func() {
		if _, err := f.app.store.db.Exec(`DELETE FROM employee_self_sessions WHERE selector=?`, strings.Split(f.cookie.Value, ".")[0]); err != nil {
			t.Fatal(err)
		}
	}
	readSelfWalletResponse(t, f.buy(t, "self-buy-revoked", token, selfPurchaseTestPassword, f.cookie, f.csrf), 401)
	f.app.selfPlanPurchaseBeforeTx = nil
	if c, l, e, s := f.operationCounts(t, "self-buy-revoked"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("revoked facts=%d/%d/%d/%d", c, l, e, s)
	}
	newCookie, newCSRF := selfLoginTest(t, f.server.URL, f.id, selfPurchaseTestPassword)
	newToken := readSelfWalletResponse(t, f.quote(t, fmt.Sprintf(`{"plan_id":%q}`, f.planID), newCookie, newCSRF), 201)["quote_token"].(string)
	f.app.selfPlanPurchaseBeforeCommit = func(tx *sql.Tx) {
		if _, err := tx.Exec(`DELETE FROM employee_self_sessions WHERE selector=?`, strings.Split(newCookie.Value, ".")[0]); err != nil {
			t.Fatal(err)
		}
	}
	readSelfWalletResponse(t, f.buy(t, "self-buy-precommit", newToken, selfPurchaseTestPassword, newCookie, newCSRF), 401)
	f.app.selfPlanPurchaseBeforeCommit = nil
	if c, l, e, s := f.operationCounts(t, "self-buy-precommit"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("precommit facts=%d/%d/%d/%d", c, l, e, s)
	}
	f.app.selfPlanPurchaseCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic unknown commit")
	}
	readSelfWalletResponse(t, f.buy(t, "self-buy-uncertain", newToken, selfPurchaseTestPassword, newCookie, newCSRF), 503)
	f.app.selfPlanPurchaseCommit = nil
	if c, l, e, s := f.operationCounts(t, "self-buy-uncertain"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("unknown commit facts=%d/%d/%d/%d", c, l, e, s)
	}
	replay := readSelfWalletResponse(t, f.buy(t, "self-buy-uncertain", newToken, selfPurchaseTestPassword, newCookie, newCSRF), 200)
	if replay["replay"] != true {
		t.Fatalf("uncertain replay=%+v", replay)
	}
}

func TestSelfPlanPurchaseQuoteBindingTTLAndChangedPlan(t *testing.T) {
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	f.setCommercial(t, true)
	f.fundWallet(t, "self-purchase-fund", 120)
	issued := time.Now().UTC().Truncate(time.Second)
	f.app.selfPlanPurchaseNow = func() time.Time { return issued }
	token := f.quoteToken(t)
	readSelfWalletResponse(t, f.buy(t, "self-tampered", token+"x", selfPurchaseTestPassword, f.cookie, f.csrf), 400)
	readSelfWalletResponse(t, f.buy(t, "self-invalid", "x", selfPurchaseTestPassword, f.cookie, f.csrf), 400)
	otherCookie, otherCSRF := selfLoginTest(t, f.server.URL, f.id, selfPurchaseTestPassword)
	readSelfWalletResponse(t, f.buy(t, "self-cross-session", token, selfPurchaseTestPassword, otherCookie, otherCSRF), 400)
	if c, l, e, s := f.operationCounts(t, "self-cross-session"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("cross-session facts=%d/%d/%d/%d", c, l, e, s)
	}
	f.app.selfPlanPurchaseNow = func() time.Time { return issued.Add(selfPlanQuoteLifetime - time.Nanosecond) }
	readSelfWalletResponse(t, f.buy(t, "self-before-expiry", token, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	f.app.selfPlanPurchaseNow = func() time.Time { return issued.Add(selfPlanQuoteLifetime) }
	readSelfWalletResponse(t, f.buy(t, "self-at-expiry", token, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	replayed := readSelfWalletResponse(t, f.buy(t, "self-before-expiry", token, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if replayed["replay"] != true {
		t.Fatalf("boundary replay=%+v", replayed)
	}
	f.app.selfPlanPurchaseNow = func() time.Time { return time.Now().UTC() }
	newToken := f.quoteToken(t)
	if _, err := f.app.store.db.Exec(`UPDATE financial_plans SET price_micro=41,revision=2,updated_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), f.planID); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, f.buy(t, "self-stale-price", newToken, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	if c, l, e, s := f.operationCounts(t, "self-stale-price"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("stale quote facts=%d/%d/%d/%d", c, l, e, s)
	}
}

func TestSelfPlanPurchasePasswordRaceSchemaAndCommitFailure(t *testing.T) {
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	f.setCommercial(t, true)
	f.fundWallet(t, "self-purchase-fund", 100)
	token := f.quoteToken(t)
	f.app.selfPlanPurchaseBeforeTx = func() {
		if _, err := f.app.store.db.Exec(`UPDATE employee_self_credentials SET password_hash=? WHERE employee_id=?`, []byte(selfDummyHash), f.id); err != nil {
			t.Fatal(err)
		}
	}
	readSelfWalletResponse(t, f.buy(t, "self-password-race", token, selfPurchaseTestPassword, f.cookie, f.csrf), 401)
	f.app.selfPlanPurchaseBeforeTx = nil
	if c, l, e, s := f.operationCounts(t, "self-password-race"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("password race facts=%d/%d/%d/%d", c, l, e, s)
	}
	// A new fixture preserves a valid credential and isolates schema damage.
	g := newSelfPurchaseFixture(t)
	g.seedPlan(t, 40, 10, "one_time")
	g.setCommercial(t, true)
	g.fundWallet(t, "self-purchase-fund", 100)
	gToken := g.quoteToken(t)
	g.app.selfPlanQuoteCommit = func(*sql.Tx) error { return errors.New("synthetic read commit failure") }
	readSelfWalletResponse(t, g.quote(t, fmt.Sprintf(`{"plan_id":%q}`, g.planID), g.cookie, g.csrf), 503)
	g.app.selfPlanQuoteCommit = nil
	g.app.selfPlanPurchaseCommit = func(*sql.Tx) error { return errors.New("synthetic write commit failure") }
	readSelfWalletResponse(t, g.buy(t, "self-commit-fault", gToken, selfPurchaseTestPassword, g.cookie, g.csrf), 503)
	g.app.selfPlanPurchaseCommit = nil
	if c, l, e, s := g.operationCounts(t, "self-commit-fault"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("commit fault facts=%d/%d/%d/%d", c, l, e, s)
	}
	if _, err := g.app.store.db.Exec(`DROP INDEX financial_accounts_owner_idx`); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, g.quote(t, fmt.Sprintf(`{"plan_id":%q}`, g.planID), g.cookie, g.csrf), 503)
	readSelfWalletResponse(t, g.buy(t, "self-bad-schema", gToken, selfPurchaseTestPassword, g.cookie, g.csrf), 503)
	if c, l, e, s := g.operationCounts(t, "self-bad-schema"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("bad schema facts=%d/%d/%d/%d", c, l, e, s)
	}
}

func TestSelfPlanPurchaseConcurrentSameAndDistinctOperationIDs(t *testing.T) {
	f := newSelfPurchaseFixture(t)
	f.seedPlan(t, 40, 10, "one_time")
	f.setCommercial(t, true)
	f.fundWallet(t, "self-purchase-fund", 50)
	token := f.quoteToken(t)
	send := func(id string) int {
		body := fmt.Sprintf(`{"operation_id":%q,"quote_token":%q,"current_password":%q}`, id, token, selfPurchaseTestPassword)
		request, err := http.NewRequest(http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions", strings.NewReader(body))
		if err != nil {
			return 0
		}
		request.Header.Set("Origin", f.server.URL)
		request.Header.Set("X-Self-Request", "1")
		request.Header.Set("X-CSRF-Token", f.csrf)
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(f.cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return 0
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	var wait sync.WaitGroup
	statuses := make([]int, 2)
	for index := range statuses {
		wait.Add(1)
		go func(index int) { defer wait.Done(); statuses[index] = send("self-concurrent-same") }(index)
	}
	wait.Wait()
	if !((statuses[0] == 201 && statuses[1] == 200) || (statuses[0] == 200 && statuses[1] == 201)) {
		t.Fatalf("same ID statuses=%v", statuses)
	}
	if c, l, e, s := f.operationCounts(t, "self-concurrent-same"); c != 1 || l != 1 || e != 2 || s != 1 {
		t.Fatalf("same ID facts=%d/%d/%d/%d", c, l, e, s)
	}
	// First purchase leaves 20. A second, distinct ID cannot reuse the old 50.
	statuses = []int{0, 0}
	for index := range statuses {
		wait.Add(1)
		go func(index int) { defer wait.Done(); statuses[index] = send(fmt.Sprintf("self-distinct-%d", index)) }(index)
	}
	wait.Wait()
	if statuses[0] != 409 || statuses[1] != 409 {
		t.Fatalf("distinct insufficient statuses=%v", statuses)
	}
	if c, l, e, s := f.operationCounts(t, "self-distinct-0"); c != 0 || l != 0 || e != 0 || s != 0 {
		t.Fatalf("distinct facts=%d/%d/%d/%d", c, l, e, s)
	}
}
