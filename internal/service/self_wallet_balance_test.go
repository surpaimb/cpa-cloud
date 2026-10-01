// Independently authored tests for docs/employee-self-wallet-balance-contract.md.
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

type selfWalletFixture struct {
	app         *App
	server      *httptest.Server
	dir         string
	id          string
	cookie      *http.Cookie
	csrf        string
	adminCookie *http.Cookie
	adminCSRF   string
}

func newSelfWalletFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, Version: "test"})
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

func selfWalletRequest(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/billing/balance"+query, body, origin, cookie, "")
}

func readSelfWalletResponse(t *testing.T, r *http.Response, status int) map[string]any {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != status || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q want=%d", r.StatusCode, r.Header.Get("Cache-Control"), status)
	}
	var value map[string]any
	if err := json.NewDecoder(r.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSelfWalletBalanceGatesRolesAndStrictInput(t *testing.T) {
	if _, err := Open(context.Background(), Config{EmployeeSelfWalletBalanceEnabled: true}); err == nil {
		t.Fatal("wallet flag without self service was accepted")
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, selfEnabled := range []bool{false, true} {
		app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: selfEnabled, Version: "test"})
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		r := selfWalletRequest(t, server.URL, "?currency=USD", "", "", nil)
		if r.StatusCode != 404 {
			t.Fatalf("self=%t wallet=off status=%d", selfEnabled, r.StatusCode)
		}
		r.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfWalletFixture(t)
	r := selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", nil)
	readSelfWalletResponse(t, r, 401)
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", f.adminCookie)
	readSelfWalletResponse(t, r, 401)
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/balance?currency=USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	key := createTestKey(t, f.server.URL, f.id, "wallet-role-key", f.adminCookie, f.adminCSRF)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 401)
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", "", "http://evil.invalid", f.cookie)
	readSelfWalletResponse(t, r, 403)
	for _, query := range []string{"", "?currency=", "?currency=usd", "?currency=Usd", "?currency=US", "?currency=USDD", "?currency=%EF%BC%B5SD", "?currency=USD&currency=EUR", "?currency=USD&employee_id=other", "?currency=USD&account_id=other", "?currency=USD&key_id=other", "?currency=USD&owner_kind=key", "?currency=USD&bad=1", "?currency=%00SD", "?currency=USD;owner=other", "?currency=" + strings.Repeat("A", 70)} {
		r = selfWalletRequest(t, f.server.URL, query, "", "", f.cookie)
		value := readSelfWalletResponse(t, r, 400)
		if value["error"] == nil || value["amount_micro"] != nil {
			t.Fatalf("query %q leaked data: %+v", query, value)
		}
	}
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", `{}`, "", f.cookie)
	readSelfWalletResponse(t, r, 400)
	req, err = http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/billing/balance?currency=USD", io.NopCloser(strings.NewReader(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(f.cookie)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 400)
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie)
	value := readSelfWalletResponse(t, r, 200)
	if len(value) != 3 || value["currency"] != "USD" || value["has_account"] != false || value["amount_micro"] != nil {
		t.Fatalf("missing account response=%+v", value)
	}
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, "")
	value = readSelfWalletResponse(t, r, 200)
	features, ok := value["features"].(map[string]any)
	if !ok || features["employee_self_wallet_balance"] != true {
		t.Fatalf("missing self capability: %+v", value)
	}
	r = selfRequestTest(t, http.MethodDelete, f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
	if r.StatusCode != 204 {
		t.Fatalf("logout status=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie)
	readSelfWalletResponse(t, r, 401)
	r = selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(f.id)+`,"password":"a-long-self-password"}`, f.server.URL, nil, "")
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("fresh login status=%d cookies=%d", r.StatusCode, len(r.Cookies()))
	}
	expiringCookie := r.Cookies()[0]
	r.Body.Close()
	selector := strings.Split(expiringCookie.Value, ".")[0]
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), selector); err != nil {
		t.Fatal(err)
	}
	r = selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", expiringCookie)
	readSelfWalletResponse(t, r, 401)
}

func TestSelfWalletBalanceOwnerCurrencyZeroRestartAndDisable(t *testing.T) {
	f := newSelfWalletFixture(t)
	ledger := financial.NewLedger(f.app.store.db)
	post := func(id, employeeID, currency string, owner financial.Owner, kind financial.EntryKind, amount int64, seconds int) {
		t.Helper()
		_, err := ledger.Post(context.Background(), financial.Post{OperationID: id, Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: employeeID}, ResourceKind: "adjustment", ResourceID: id, ObservedAt: time.Date(2026, 10, 1, 0, 0, seconds, 0, time.UTC), Entries: []financial.EntryInput{{Owner: owner, Currency: currency, Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: id}}})
		if err != nil {
			t.Fatalf("post %s for %s: %v", id, employeeID, err)
		}
	}
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	post("wallet-own-credit", f.id, "USD", owner, financial.EntryAdjustmentCredit, 42, 1)
	post("wallet-own-debit", f.id, "USD", owner, financial.EntryAdjustmentDebit, -42, 2)
	post("wallet-own-eur", f.id, "EUR", owner, financial.EntryAdjustmentCredit, 7, 3)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	post("wallet-other", other.ID, "USD", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, financial.EntryAdjustmentCredit, 91, 4)
	key := createTestKey(t, f.server.URL, f.id, "wallet-key-op", f.adminCookie, f.adminCSRF)
	post("wallet-key", f.id, "USD", financial.Owner{Kind: financial.OwnerKey, EmployeeID: f.id, KeyID: key.ID}, financial.EntryAdjustmentCredit, 500, 5)
	post("wallet-resource", f.id, "USD", financial.Owner{Kind: financial.OwnerResource, EmployeeID: f.id, ResourceKind: "response", ResourceID: "resource-one"}, financial.EntryAdjustmentCredit, 600, 6)
	var priorAccounts, priorEntries int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&priorAccounts); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&priorEntries); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		currency   string
		amount     any
		hasAccount bool
	}{{"USD", "0", true}, {"EUR", "7", true}, {"JPY", nil, false}} {
		r := selfWalletRequest(t, f.server.URL, "?currency="+test.currency, "", "", f.cookie)
		value := readSelfWalletResponse(t, r, 200)
		if len(value) != 3 || value["currency"] != test.currency || value["amount_micro"] != test.amount || value["has_account"] != test.hasAccount {
			t.Fatalf("%s response=%+v", test.currency, value)
		}
	}
	otherResponse := selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", otherCookie)
	otherValue := readSelfWalletResponse(t, otherResponse, 200)
	if otherValue["amount_micro"] != "91" || otherValue["has_account"] != true {
		t.Fatalf("other employee balance=%+v", otherValue)
	}
	var accounts, entries int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&accounts); err != nil || accounts != priorAccounts {
		t.Fatalf("accounts changed: %d -> %d err=%v", priorAccounts, accounts, err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&entries); err != nil || entries != priorEntries {
		t.Fatalf("entries changed: %d -> %d err=%v", priorEntries, entries, err)
	}
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	r := selfWalletRequest(t, server.URL, "?currency=EUR", "", "", f.cookie)
	value := readSelfWalletResponse(t, r, 200)
	if value["amount_micro"] != "7" {
		t.Fatalf("restart balance=%+v", value)
	}
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r = requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable: %d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	r = selfWalletRequest(t, server.URL, "?currency=EUR", "", "", f.cookie)
	readSelfWalletResponse(t, r, 401)
}

func TestSelfWalletBalanceStorageFailureIsSanitized(t *testing.T) {
	f := newSelfWalletFixture(t)
	if _, err := f.app.store.db.Exec(`DROP INDEX financial_accounts_owner_idx`); err != nil {
		t.Fatal(err)
	}
	r := selfWalletRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie)
	defer r.Body.Close()
	if r.StatusCode != 503 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "financial_") || strings.Contains(string(body), f.id) || strings.Contains(string(body), f.cookie.Value) || strings.Contains(string(body), "amount_micro") {
		t.Fatalf("storage error leaked details: %s", body)
	}
}
