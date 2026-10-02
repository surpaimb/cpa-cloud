package service

// Independently authored acceptance tests for
// docs/employee-self-one-shot-disarm-contract.md.

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
	"golang.org/x/crypto/bcrypt"
)

func newSelfOneShotFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", Version: "test", EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfOneShotRenewalDisarmEnabled: true})
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

func selfOneShotURL(f selfWalletFixture, id string) string {
	return f.server.URL + "/self/api/v1/billing/subscriptions/" + id + "/one-shot-renewal"
}

func selfOneShotGET(t *testing.T, f selfWalletFixture, id, suffix, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, selfOneShotURL(f, id)+suffix, body, origin, cookie, "")
}

func selfOneShotPOST(t *testing.T, f selfWalletFixture, id, body, origin string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodPost, selfOneShotURL(f, id)+"/disarm", body, origin, cookie, csrf)
}

func seedSelfOneShotMonth(t *testing.T, f selfWalletFixture, id string, owner financial.Owner) {
	t.Helper()
	createSelfSubscriptionPlan(t, f)
	activityPost(t, f, "one-shot-account-"+id, owner, "USD", financial.EntryAdjustmentCredit, 100, time.Now().UTC())
	var accountID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM financial_accounts WHERE owner_kind=? AND employee_id=? AND currency='USD' ORDER BY id DESC LIMIT 1`, owner.Kind, owner.EmployeeID).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	due := start.AddDate(0, 1, 0)
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?, 'self-sub-month-plan',1,10,20,'USD','monthly','active',?,?,1)`, id, accountID, start.Format(time.RFC3339Nano), due.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
}

func armSelfOneShotMonth(t *testing.T, f selfWalletFixture, id string) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	status, response := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/subscriptions/"+id+"/one-shot-renewal", `{"operation_id":"20000000-0000-4000-8000-000000000611","expected_revision":1}`, f.adminCookie, f.adminCSRF, f.server.URL, nil)
	if status != 200 {
		t.Fatalf("admin arm status=%d response=%s", status, response)
	}
	if _, err := f.app.store.db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
}

func TestSelfOneShotFlagStrictGETAndOwner(t *testing.T) {
	for _, cfg := range []Config{{EmployeeSelfOneShotRenewalDisarmEnabled: true}, {EmployeeSelfServiceEnabled: true, EmployeeSelfOneShotRenewalDisarmEnabled: true}, {EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfOneShotRenewalDisarmEnabled: true}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	disabledDir := t.TempDir()
	if err := Initialize(context.Background(), disabledDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	disabled, err := Open(context.Background(), Config{DataDir: disabledDir, Listen: "127.0.0.1:0", Version: "test", EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	disabledServer := httptest.NewServer(disabled.Handler())
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		suffix := ""
		if method == http.MethodPost {
			suffix = "/disarm"
		}
		response := selfRequestTest(t, method, disabledServer.URL+"/self/api/v1/billing/subscriptions/disabled/one-shot-renewal"+suffix, "", "", nil, "")
		if response.StatusCode != 404 {
			t.Fatalf("disabled %s status=%d", method, response.StatusCode)
		}
		response.Body.Close()
	}
	disabledServer.Close()
	if err := disabled.Close(); err != nil {
		t.Fatal(err)
	}
	f := newSelfOneShotFixture(t)
	seedSelfOneShotMonth(t, f, "self-shot-owned", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id})
	feature := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if feature["employee_self_one_shot_renewal_disarm"] != true {
		t.Fatalf("capability=%+v", feature)
	}
	none := readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-owned", "", "", "", f.cookie), 200)
	if len(none) != 6 || none["state"] != "none" || none["revision"] != float64(0) || none["due_at"] != nil || none["reason"] != nil || none["terminal_at"] != nil {
		t.Fatalf("none projection=%+v", none)
	}
	for _, suffix := range []string{"?", "?employee_id=x", "/extra"} {
		response := selfOneShotGET(t, f, "self-shot-owned", suffix, "", "", f.cookie)
		readSelfWalletResponse(t, response, 400)
	}
	readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-owned", "", `{}`, "", f.cookie), 400)
	readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-owned", "", "", "", nil), 401)
	readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-owned", "", "", "", f.adminCookie), 401)
	readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-owned", "", "", "http://evil.invalid", f.cookie), 403)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	seedSelfSubscription(t, f, "self-shot-other", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, "monthly", "active", false)
	key := createTestKey(t, f.server.URL, f.id, "self-shot-key-owner", f.adminCookie, f.adminCSRF)
	seedSelfSubscription(t, f, "self-shot-key", financial.Owner{Kind: financial.OwnerKey, EmployeeID: f.id, KeyID: key.ID}, "monthly", "active", false)
	seedSelfSubscription(t, f, "self-shot-once", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, "one_time", "active", false)
	for _, id := range []string{"self-shot-other", "self-shot-key", "self-shot-once", "self-shot-missing"} {
		readSelfWalletResponse(t, selfOneShotGET(t, f, id, "", "", "", f.cookie), 404)
	}
}

func TestSelfOneShotCommercialOffDisarmReplayAndUncertainCommit(t *testing.T) {
	f := newSelfOneShotFixture(t)
	seedSelfOneShotMonth(t, f, "self-shot-disarm", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id})
	armSelfOneShotMonth(t, f, "self-shot-disarm")
	armed := readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-disarm", "", "", "", f.cookie), 200)
	if len(armed) != 6 || armed["state"] != "armed" || armed["revision"] != float64(1) || armed["due_at"] == nil || armed["reason"] != nil || armed["terminal_at"] != nil {
		t.Fatalf("armed=%+v", armed)
	}
	body := selfCancelBody("self-shot-disarm-op", 1, selfPurchaseTestPassword)
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", selfCancelBody("bad-pass", 1, "incorrect-password"), f.server.URL, f.cookie, f.csrf), 401)
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", body, "http://evil.invalid", f.cookie, f.csrf), 403)
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", body, f.server.URL, f.cookie, "bad-csrf"), 403)
	for _, invalid := range []string{`{"operation_id":"x","expected_revision":1}`, `{"operation_id":"x","expected_revision":1.0,"current_password":"a-long-self-password"}`, `{"operation_id":"x","expected_revision":1,"current_password":"a-long-self-password","owner":"x"}`} {
		readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", invalid, f.server.URL, f.cookie, f.csrf), 400)
	}
	f.app.selfOneShotDisarmCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic uncertain commit")
	}
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", body, f.server.URL, f.cookie, f.csrf), 503)
	f.app.selfOneShotDisarmCommit = nil
	replay := readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", body, f.server.URL, f.cookie, f.csrf), 200)
	if len(replay) != 8 || replay["state"] != "disarmed" || replay["revision"] != float64(2) || replay["reason"] != "disarmed" || replay["replay"] != true || replay["operation_id"] != "self-shot-disarm-op" || replay["terminal_at"] == nil {
		t.Fatalf("replay=%+v", replay)
	}
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", selfCancelBody("different-id", 1, selfPurchaseTestPassword), f.server.URL, f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-disarm", selfCancelBody("self-shot-disarm-op", 2, selfPurchaseTestPassword), f.server.URL, f.cookie, f.csrf), 409)
	var kind, employee string
	var admin sql.NullString
	if err := f.app.store.db.QueryRow(`SELECT actor_kind,actor_employee_id,actor_admin_id FROM financial_commercial_operations WHERE operation_id='self-shot-disarm-op'`).Scan(&kind, &employee, &admin); err != nil || kind != "employee" || employee != f.id || admin.Valid {
		t.Fatalf("typed actor kind=%q employee=%q admin=%v err=%v", kind, employee, admin, err)
	}
	var receipts, entries int
	if err := f.app.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='self-shot-disarm-op'),(SELECT COUNT(*) FROM financial_entries WHERE operation_id='self-shot-disarm-op')`).Scan(&receipts, &entries); err != nil || receipts != 1 || entries != 0 {
		t.Fatalf("receipts=%d entries=%d err=%v", receipts, entries, err)
	}
}

func TestSelfOneShotPasswordRaceAndReadCommitFailure(t *testing.T) {
	f := newSelfOneShotFixture(t)
	seedSelfOneShotMonth(t, f, "self-shot-race", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id})
	armSelfOneShotMonth(t, f, "self-shot-race")
	f.app.selfOneShotReadCommit = func(tx *sql.Tx) error { _ = tx.Rollback(); return errors.New("synthetic read commit failure") }
	readSelfWalletResponse(t, selfOneShotGET(t, f, "self-shot-race", "", "", "", f.cookie), 503)
	f.app.selfOneShotReadCommit = nil
	var hookErr error
	f.app.selfOneShotDisarmBeforeTx = func() {
		var hash []byte
		hash, hookErr = bcrypt.GenerateFromPassword([]byte("new-self-password-long"), 12)
		if hookErr == nil {
			_, hookErr = f.app.store.db.Exec(`UPDATE employee_self_credentials SET password_hash=? WHERE employee_id=?`, hash, f.id)
		}
	}
	readSelfWalletResponse(t, selfOneShotPOST(t, f, "self-shot-race", selfCancelBody("self-shot-race-op", 1, selfPurchaseTestPassword), f.server.URL, f.cookie, f.csrf), 401)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	var receipts int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='self-shot-race-op'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("race receipts=%d err=%v", receipts, err)
	}
}
