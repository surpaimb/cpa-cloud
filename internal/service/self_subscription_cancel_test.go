package service

// Independently authored HTTP and transaction tests for
// docs/employee-self-subscription-cancel-contract.md.

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

	"golang.org/x/crypto/bcrypt"
)

func newSelfCancelFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true,
		EmployeeSelfSubscriptionCancelEnabled: true,
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

func selfCancelRequest(t *testing.T, f selfWalletFixture, id, body string, cookie *http.Cookie, csrf string) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/"+id+"/cancel", body, f.server.URL, cookie, csrf)
}

func selfCancelBody(operation string, revision int64, password string) string {
	return fmt.Sprintf(`{"operation_id":%q,"expected_revision":%d,"current_password":%q}`, operation, revision, password)
}

func TestSelfSubscriptionCancelFlagAndStrictRequest(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfSubscriptionCancelEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionCancelEnabled: true},
		{EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionCancelEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	f := newSelfCancelFixture(t)
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "cancel-strict", selfPurchaseOwner(f.id), "one_time", "active", false)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_subscription_cancel"] != true {
		t.Fatalf("cancel capability=%+v", features)
	}
	page := readSelfWalletResponse(t, selfSubscriptionRequest(t, f.server.URL, "", "", "", f.cookie), 200)
	items := page["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("status storage revision=%+v", page)
	}
	for _, body := range []string{
		`{"operation_id":"missing-password","expected_revision":1}`,
		`{"operation_id":"typed-password","expected_revision":1,"current_password":null}`,
		`{"operation_id":"decimal","expected_revision":1.0,"current_password":"a-long-self-password"}`,
	} {
		readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-strict", body, f.cookie, f.csrf), 400)
	}
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-strict", selfCancelBody("short-password", 1, "short"), f.cookie, f.csrf), 401)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-strict", selfCancelBody("anonymous", 1, selfPurchaseTestPassword), nil, ""), 401)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-strict", selfCancelBody("admin", 1, selfPurchaseTestPassword), f.adminCookie, f.adminCSRF), 401)
}

func TestSelfSubscriptionCancelCommitReplayAndBoundaries(t *testing.T) {
	f := newSelfCancelFixture(t)
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "cancel-owned", selfPurchaseOwner(f.id), "one_time", "active", false)
	first := readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-owned", selfCancelBody("cancel-commit", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 200)
	if len(first) != 6 || first["subscription_id"] != "cancel-owned" || first["status"] != "cancelled" || first["revision"] != float64(2) || first["replay"] != false {
		t.Fatalf("first cancel=%+v", first)
	}
	replay := readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-owned", selfCancelBody("cancel-commit", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 200)
	if len(replay) != 6 || replay["replay"] != true || replay["cancelled_at"] != first["cancelled_at"] {
		t.Fatalf("replay=%+v", replay)
	}
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-owned", selfCancelBody("cancel-new-id", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 409)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-owned", selfCancelBody("cancel-commit", 2, selfPurchaseTestPassword), f.cookie, f.csrf), 409)
	var commercialCount, ledgerCount, entryCount int
	for index, query := range []string{
		`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='cancel-commit'`,
		`SELECT COUNT(*) FROM financial_operations WHERE operation_id='cancel-commit'`,
		`SELECT COUNT(*) FROM financial_entries WHERE operation_id='cancel-commit'`,
	} {
		switch index {
		case 0:
			_ = f.app.store.db.QueryRow(query).Scan(&commercialCount)
		case 1:
			_ = f.app.store.db.QueryRow(query).Scan(&ledgerCount)
		case 2:
			_ = f.app.store.db.QueryRow(query).Scan(&entryCount)
		}
	}
	if commercialCount != 1 || ledgerCount != 0 || entryCount != 0 {
		t.Fatalf("cancel facts commercial=%d ledger=%d entries=%d", commercialCount, ledgerCount, entryCount)
	}
}

func TestSelfSubscriptionCancelMonthCrossesEndAndUnknownCommit(t *testing.T) {
	f := newSelfCancelFixture(t)
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "cancel-account-seed", selfPurchaseOwner(f.id), "one_time", "active", false)
	var accountID string
	if err := f.app.store.db.QueryRow(`SELECT account_id FROM financial_subscriptions WHERE id='cancel-account-seed'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month()+1, 1, 8, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	if _, err := f.app.store.db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('cancel-month',?,'self-sub-month-plan',1,10,20,'USD','monthly','active',?,?,1)`, accountID, start.Format(time.RFC3339Nano), end.Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	beforeEnd := end.Add(-time.Second)
	f.app.selfSubscriptionCancelNow = func() time.Time { return beforeEnd }
	f.app.selfSubscriptionCancelBeforeCommit = func(*sql.Tx) { f.app.selfSubscriptionCancelNow = func() time.Time { return end } }
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-month", selfCancelBody("cancel-cross-end", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 409)
	var status string
	var receipts int
	if err := f.app.store.db.QueryRow(`SELECT status FROM financial_subscriptions WHERE id='cancel-month'`).Scan(&status); err != nil || status != "active" {
		t.Fatalf("cross-end status=%q err=%v", status, err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='cancel-cross-end'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("cross-end receipts=%d err=%v", receipts, err)
	}
	f.app.selfSubscriptionCancelBeforeCommit = nil
	f.app.selfSubscriptionCancelNow = func() time.Time { return beforeEnd }
	f.app.selfSubscriptionCancelCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("synthetic uncertain commit")
	}
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-month", selfCancelBody("cancel-unknown", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 503)
	f.app.selfSubscriptionCancelCommit = nil
	f.app.selfSubscriptionCancelNow = func() time.Time { return end.Add(time.Hour) }
	replay := readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-month", selfCancelBody("cancel-unknown", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 200)
	if replay["replay"] != true || replay["status"] != "cancelled" {
		t.Fatalf("unknown commit recovery=%+v", replay)
	}
}

func TestSelfSubscriptionCancelOwnerOriginCSRFAndPasswordRace(t *testing.T) {
	f := newSelfCancelFixture(t)
	createSelfSubscriptionPlan(t, f)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	seedSelfSubscription(t, f, "cancel-other-owner", selfPurchaseOwner(other.ID), "one_time", "active", false)
	seedSelfSubscription(t, f, "cancel-auth-race", selfPurchaseOwner(f.id), "one_time", "active", false)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-other-owner", selfCancelBody("cancel-other-attempt", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 404)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/cancel-auth-race/cancel", selfCancelBody("cancel-bad-origin", 1, selfPurchaseTestPassword), "http://evil.invalid", f.cookie, f.csrf), 403)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-auth-race", selfCancelBody("cancel-bad-csrf", 1, selfPurchaseTestPassword), f.cookie, "wrong-csrf"), 403)
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-auth-race", selfCancelBody("cancel-bad-password", 1, "wrong-password"), f.cookie, f.csrf), 401)
	var hookErr error
	f.app.selfSubscriptionCancelBeforeTx = func() {
		var hash []byte
		hash, hookErr = bcrypt.GenerateFromPassword([]byte("new-self-password-long"), 12)
		if hookErr == nil {
			_, hookErr = f.app.store.db.Exec(`UPDATE employee_self_credentials SET password_hash=? WHERE employee_id=?`, hash, f.id)
		}
	}
	readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-auth-race", selfCancelBody("cancel-credential-race", 1, selfPurchaseTestPassword), f.cookie, f.csrf), 401)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	var count int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='cancel-credential-race'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("credential race receipt count=%d err=%v", count, err)
	}
}

func TestSelfSubscriptionCancelReplayAfterRestart(t *testing.T) {
	f := newSelfCancelFixture(t)
	createSelfSubscriptionPlan(t, f)
	seedSelfSubscription(t, f, "cancel-restart", selfPurchaseOwner(f.id), "one_time", "active", false)
	body := selfCancelBody("cancel-restart-id", 1, selfPurchaseTestPassword)
	first := readSelfWalletResponse(t, selfCancelRequest(t, f, "cancel-restart", body, f.cookie, f.csrf), 200)
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", Version: "test", EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionCancelEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	server := httptest.NewServer(reopened.Handler())
	t.Cleanup(server.Close)
	replay := readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, server.URL+"/self/api/v1/billing/subscriptions/cancel-restart/cancel", body, server.URL, f.cookie, f.csrf), 200)
	if replay["replay"] != true || replay["cancelled_at"] != first["cancelled_at"] || replay["revision"] != first["revision"] {
		t.Fatalf("restart replay=%+v first=%+v", replay, first)
	}
}

func TestSelfSubscriptionCancelConcurrentSameAndDistinctIDs(t *testing.T) {
	for _, sameID := range []bool{true, false} {
		t.Run(fmt.Sprintf("same=%v", sameID), func(t *testing.T) {
			f := newSelfCancelFixture(t)
			createSelfSubscriptionPlan(t, f)
			seedSelfSubscription(t, f, "cancel-concurrent", selfPurchaseOwner(f.id), "one_time", "active", false)
			statuses := make(chan int, 2)
			start := make(chan struct{})
			var workers sync.WaitGroup
			for index := 0; index < 2; index++ {
				operation := "cancel-concurrent-id"
				if !sameID {
					operation = fmt.Sprintf("cancel-concurrent-id-%d", index)
				}
				workers.Add(1)
				go func(operation string) {
					defer workers.Done()
					<-start
					request, err := http.NewRequest(http.MethodPost, f.server.URL+"/self/api/v1/billing/subscriptions/cancel-concurrent/cancel", strings.NewReader(selfCancelBody(operation, 1, selfPurchaseTestPassword)))
					if err != nil {
						statuses <- 0
						return
					}
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Origin", f.server.URL)
					request.Header.Set("X-Self-Request", "1")
					request.Header.Set("X-CSRF-Token", f.csrf)
					request.AddCookie(f.cookie)
					response, err := http.DefaultClient.Do(request)
					if err != nil {
						statuses <- 0
						return
					}
					response.Body.Close()
					statuses <- response.StatusCode
				}(operation)
			}
			close(start)
			workers.Wait()
			first, second := <-statuses, <-statuses
			if sameID && (first != 200 || second != 200) || !sameID && !((first == 200 && second == 409) || (first == 409 && second == 200)) {
				t.Fatalf("sameID=%v statuses=%d,%d", sameID, first, second)
			}
			var count int
			if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE action='subscription.cancel' AND resource_id='cancel-concurrent'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("sameID=%v receipt count=%d err=%v", sameID, count, err)
			}
		})
	}
}
