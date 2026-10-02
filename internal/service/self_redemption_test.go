package service

// Independently authored HTTP and transaction tests for
// docs/employee-self-redemption-contract.md. All codes and accounts are synthetic.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type selfRedemptionFixture struct{ selfWalletFixture }

func (f selfRedemptionFixture) setCommercial(t *testing.T, enabled bool) {
	t.Helper()
	value := 0
	if enabled {
		value = 1
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_settings SET enabled=?,revision=revision+1,updated_at=? WHERE singleton=1`,
		value, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func newSelfRedemptionFixture(t *testing.T) selfRedemptionFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfRedemptionEnabled: true})
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

func (f selfRedemptionFixture) issueCode(t *testing.T, currency string, amount, maxUses int, expires time.Time) string {
	t.Helper()
	operationID, err := newRecoveryOperationID()
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"operation_id":%q,"currency":%q,"amount_micro":%q,"max_uses":%d,"expires_at":%q}`,
		operationID, currency, fmt.Sprint(amount), maxUses, expires.UTC().Format(time.RFC3339Nano))
	status, raw := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/redemption-codes",
		body, f.adminCookie, f.adminCSRF, f.server.URL, nil)
	if status != 200 {
		t.Fatalf("issue code status=%d body=%s", status, raw)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	code, ok := value["code"].(string)
	if !ok || !strings.HasPrefix(code, "cpa_") {
		t.Fatalf("issued code missing: %v", value)
	}
	return code
}

func (f selfRedemptionFixture) redeem(t *testing.T, operationID, code, password string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"operation_id":%q,"code":%q,"current_password":%q}`, operationID, code, password)
	return selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath, body, f.server.URL, cookie, csrf)
}

func (f selfRedemptionFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.app.store.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSelfRedemptionPrerequisitesRolesAndInput(t *testing.T) {
	for _, cfg := range []Config{{EmployeeSelfRedemptionEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfRedemptionEnabled: true},
		{EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfRedemptionEnabled: true}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	disabled := selfRequestTest(t, http.MethodPost, server.URL+selfRedemptionPath, `{}`, server.URL, nil, "")
	if disabled.StatusCode != 404 {
		t.Fatalf("disabled route status=%d", disabled.StatusCode)
	}
	disabled.Body.Close()
	server.Close()
	_ = app.Close()
	f := newSelfRedemptionFixture(t)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_redemption"] != true {
		t.Fatalf("feature flag=%v", features)
	}
	wrongMethod := selfRequestTest(t, http.MethodGet, f.server.URL+selfRedemptionPath, "", f.server.URL, f.cookie, f.csrf)
	if wrongMethod.StatusCode != http.StatusMethodNotAllowed || wrongMethod.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("wrong method status=%d allow=%q", wrongMethod.StatusCode, wrongMethod.Header.Get("Allow"))
	}
	wrongMethod.Body.Close()
	alias := selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing//redemptions", `{}`, f.server.URL, f.cookie, f.csrf)
	if alias.StatusCode != http.StatusNotFound {
		t.Fatalf("alias status=%d", alias.StatusCode)
	}
	alias.Body.Close()
	readSelfWalletResponse(t, f.redeem(t, "op-anon", "invalid", selfPurchaseTestPassword, nil, f.csrf), 401)
	readSelfWalletResponse(t, f.redeem(t, "op-admin", "invalid", selfPurchaseTestPassword, f.adminCookie, f.csrf), 401)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath, `{}`, "http://evil.invalid", f.cookie, f.csrf), 403)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath, `{}`, f.server.URL, f.cookie, "wrong"), 403)
	for _, body := range []string{`{}`, `[]`, `{"operation_id":"x","code":"x"}`, `{"operation_id":"x","code":"x","current_password":"x","owner":"employee"}`,
		`{"operation_id":"x","code":"x","code":"y","current_password":"x"}`, `{"operation_id":"x","code":3,"current_password":"x"}`, `{"operation_id":"x","code":"x","current_password":"x"} {}`} {
		f.app.clearSelfFailures("127.0.0.1", f.id)
		readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath, body, f.server.URL, f.cookie, f.csrf), 400)
	}
	f.app.clearSelfFailures("127.0.0.1", f.id)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath+"?", `{}`, f.server.URL, f.cookie, f.csrf), 400)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfRedemptionPath+"?x=1", `{}`, f.server.URL, f.cookie, f.csrf), 400)
	readSelfWalletResponse(t, f.redeem(t, "op-password", "invalid", "wrong-password", f.cookie, f.csrf), 401)
}

func TestSelfRedemptionDirectWalletReplayAndCommercialOff(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.setCommercial(t, true)
	code := f.issueCode(t, "EUR", 37, 1, time.Now().UTC().Add(time.Hour))
	if accounts := f.count(t, `SELECT COUNT(*) FROM financial_accounts WHERE employee_id=?`, f.id); accounts != 0 {
		t.Fatalf("before redemption accounts=%d", accounts)
	}
	readSelfWalletResponse(t, f.redeem(t, "bad-code", "not-a-valid-code", selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	if accounts := f.count(t, `SELECT COUNT(*) FROM financial_accounts WHERE employee_id=?`, f.id); accounts != 0 {
		t.Fatalf("invalid code created account: %d", accounts)
	}
	first := readSelfWalletResponse(t, f.redeem(t, "self-redeem-one", code, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
	if len(first) != 5 || first["operation_id"] != "self-redeem-one" || first["replay"] != false ||
		first["currency"] != "EUR" || first["amount_micro"] != "37" || first["credited_at"] == nil {
		t.Fatalf("first result=%v", first)
	}
	readSelfWalletResponse(t, f.redeem(t, "self-redeem-other-id", code, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	f.setCommercial(t, false)
	second := readSelfWalletResponse(t, f.redeem(t, "self-redeem-one", code, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if len(second) != 5 || second["replay"] != true || second["credited_at"] != first["credited_at"] {
		t.Fatalf("replay=%v first=%v", second, first)
	}
	readSelfWalletResponse(t, f.redeem(t, "self-redeem-new", code, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, f.redeem(t, "self-redeem-one", "wrong-code", selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, f.redeem(t, "self-redeem-one", code, "wrong-password", f.cookie, f.csrf), 401)
	if n := f.count(t, `SELECT COUNT(*) FROM financial_accounts WHERE employee_id=? AND owner_kind='employee' AND currency='EUR'`, f.id); n != 1 {
		t.Fatalf("direct accounts=%d", n)
	}
	for query, expected := range map[string]int{
		`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='self-redeem-one' AND action='redemption.redeem' AND actor_kind='employee' AND actor_employee_id=?`:    1,
		`SELECT COUNT(*) FROM financial_operations WHERE operation_id='self-redeem-one' AND action='redemption' AND actor_kind='employee' AND actor_employee_id=? AND digest_version=2`: 1,
		`SELECT COUNT(*) FROM financial_entries WHERE operation_id='self-redeem-one' AND kind='redemption' AND amount_micro=37`:                                                         1,
		`SELECT COUNT(*) FROM financial_redemptions WHERE entry_id IN (SELECT id FROM financial_entries WHERE operation_id='self-redeem-one')`:                                          1,
	} {
		args := []any{}
		if strings.Contains(query, "actor_employee_id=?") {
			args = append(args, f.id)
		}
		if n := f.count(t, query, args...); n != expected {
			t.Fatalf("count %q = %d", query, n)
		}
	}
	if n := f.count(t, `SELECT uses FROM financial_redemption_codes WHERE id=(SELECT code_id FROM financial_redemptions LIMIT 1)`); n != 1 {
		t.Fatalf("uses=%d", n)
	}
}

func TestSelfRedemptionUnknownCommitReplayAfterExpiryAndDisable(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.setCommercial(t, true)
	expires := time.Now().UTC().Add(time.Hour)
	code := f.issueCode(t, "GBP", 29, 1, expires)
	f.app.selfRedemptionCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic lost commit acknowledgement")
	}
	readSelfWalletResponse(t, f.redeem(t, "unknown-commit", code, selfPurchaseTestPassword, f.cookie, f.csrf), 503)
	f.app.selfRedemptionCommit = nil
	if n := f.count(t, `SELECT COUNT(*) FROM financial_entries WHERE operation_id='unknown-commit'`); n != 1 {
		t.Fatalf("unknown commit entries=%d", n)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_redemption_codes SET enabled=0 WHERE code_digest=?`, f.app.secrets.digest(billingWebhookPurpose, code)); err != nil {
		t.Fatal(err)
	}
	f.setCommercial(t, false)
	f.app.selfRedemptionNow = func() time.Time { return expires.Add(time.Second) }
	replay := readSelfWalletResponse(t, f.redeem(t, "unknown-commit", code, selfPurchaseTestPassword, f.cookie, f.csrf), 200)
	if replay["replay"] != true || replay["currency"] != "GBP" || replay["amount_micro"] != "29" {
		t.Fatalf("unknown commit replay=%v", replay)
	}
	readSelfWalletResponse(t, f.redeem(t, "unknown-new", code, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
}

func TestSelfRedemptionAdminDirectAccountRemainsUnique(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.setCommercial(t, true)
	code := f.issueCode(t, "CAD", 13, 2, time.Now().UTC().Add(time.Hour))
	operationID, err := newRecoveryOperationID()
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"operation_id":%q,"owner":{"kind":"employee","employee_id":%q},"code":%q}`, operationID, f.id, code)
	status, raw := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/redemptions", body,
		f.adminCookie, f.adminCSRF, f.server.URL, nil)
	if status != 200 || !strings.Contains(string(raw), `"entry_id"`) {
		t.Fatalf("admin redemption status=%d body=%s", status, raw)
	}
	readSelfWalletResponse(t, f.redeem(t, "employee-after-admin", code, selfPurchaseTestPassword, f.cookie, f.csrf), 409)
	if n := f.count(t, `SELECT COUNT(*) FROM financial_entries WHERE kind='redemption' AND account_id=(SELECT id FROM financial_accounts WHERE employee_id=? AND currency='CAD')`, f.id); n != 1 {
		t.Fatalf("admin/direct redemption entries=%d", n)
	}
}

func TestSelfRedemptionLastUseConcurrencyAndFaultRollback(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.setCommercial(t, true)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, otherCSRF := selfRedeem(t, f.server.URL, other.ID, secret)
	code := f.issueCode(t, "USD", 19, 1, time.Now().UTC().Add(time.Hour))
	type actor struct {
		cookie   *http.Cookie
		csrf, id string
	}
	actors := []actor{{f.cookie, f.csrf, "employee-redemption-a"}, {otherCookie, otherCSRF, "employee-redemption-b"}}
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range actors {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			response := f.redeem(t, actors[i].id, code, selfPurchaseTestPassword, actors[i].cookie, actors[i].csrf)
			statuses[i] = response.StatusCode
			response.Body.Close()
		}(i)
	}
	wg.Wait()
	if !((statuses[0] == 201 && statuses[1] == 409) || (statuses[0] == 409 && statuses[1] == 201)) {
		t.Fatalf("last-use statuses=%v", statuses)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM financial_redemptions WHERE code_id=(SELECT id FROM financial_redemption_codes WHERE max_uses=1)`); n != 1 {
		t.Fatalf("redemptions=%d", n)
	}
	code2 := f.issueCode(t, "JPY", 11, 2, time.Now().UTC().Add(time.Hour))
	f.app.selfRedemptionCommit = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
	readSelfWalletResponse(t, f.redeem(t, "commit-fault", code2, selfPurchaseTestPassword, f.cookie, f.csrf), 503)
	f.app.selfRedemptionCommit = nil
	if n := f.count(t, `SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='commit-fault'`); n != 0 {
		t.Fatalf("commercial partial=%d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM financial_accounts WHERE employee_id=? AND currency='JPY'`, f.id); n != 0 {
		t.Fatalf("account partial=%d", n)
	}
	readSelfWalletResponse(t, f.redeem(t, "commit-fault", code2, selfPurchaseTestPassword, f.cookie, f.csrf), 201)
}

func TestSelfRedemptionPasswordRaceAndCorruptReplay(t *testing.T) {
	f := newSelfRedemptionFixture(t)
	f.setCommercial(t, true)
	code := f.issueCode(t, "USD", 17, 2, time.Now().UTC().Add(time.Hour))
	f.app.selfRedemptionBeforeTx = func() {
		if _, err := f.app.store.db.Exec(`UPDATE employee_self_credentials SET password_hash=? WHERE employee_id=?`, []byte(selfDummyHash), f.id); err != nil {
			t.Fatal(err)
		}
	}
	readSelfWalletResponse(t, f.redeem(t, "password-race", code, selfPurchaseTestPassword, f.cookie, f.csrf), 401)
	f.app.selfRedemptionBeforeTx = nil
	if n := f.count(t, `SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='password-race'`); n != 0 {
		t.Fatalf("password race fact=%d", n)
	}
	g := newSelfRedemptionFixture(t)
	g.setCommercial(t, true)
	code2 := g.issueCode(t, "USD", 23, 1, time.Now().UTC().Add(time.Hour))
	readSelfWalletResponse(t, g.redeem(t, "corrupt-replay", code2, selfPurchaseTestPassword, g.cookie, g.csrf), 201)
	if _, err := g.app.store.db.Exec(`DROP TRIGGER financial_entries_no_update`); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, g.redeem(t, "corrupt-replay", code2, selfPurchaseTestPassword, g.cookie, g.csrf), 503)
}
