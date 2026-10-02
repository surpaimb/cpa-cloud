package service

// Independently authored tests for docs/employee-self-subscription-purchase-snapshot-contract.md.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfPurchaseSnapshotFixture(t *testing.T, enabled bool) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dir, Listen: "127.0.0.1:0", Version: "test",
		EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true,
		EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionPurchaseSnapshotEnabled: enabled,
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

func selfSnapshotMeta(t *testing.T, f selfWalletFixture, operation string, at time.Time) financial.WriteMeta {
	t.Helper()
	var adminID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	return financial.WriteMeta{OperationID: operation, Actor: financial.Actor{Kind: financial.ActorAdmin, ID: adminID}, PayloadDigest: sha256.Sum256([]byte(operation)), ObservedAt: at}
}

func seedSelfPurchaseSnapshot(t *testing.T, f selfWalletFixture) (string, string) {
	t.Helper()
	commercial := financial.NewCommercial(f.app.store.db)
	start := time.Date(2026, 10, 2, 8, 0, 0, 0, time.UTC)
	if _, err := commercial.SetEnabled(context.Background(), selfSnapshotMeta(t, f, "snapshot-enable", start.Add(-3*time.Hour)), 1, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(context.Background(), financial.CreatePlan{
		Meta: selfSnapshotMeta(t, f, "snapshot-plan", start.Add(-2*time.Hour)),
		Name: "Synthetic monthly", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	activityPost(t, f, "snapshot-fund", owner, "USD", financial.EntryAdjustmentCredit, 100, start.Add(-time.Hour))
	purchased, _, err := commercial.PurchaseSubscription(context.Background(), financial.PurchaseSubscription{
		Meta: selfSnapshotMeta(t, f, "snapshot-buy", start), Owner: owner, PlanID: plan.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return purchased.ID, plan.ID
}

func requestSelfPurchaseSnapshot(t *testing.T, f selfWalletFixture, suffix, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/billing/subscriptions/"+suffix, body, origin, cookie, "")
}

func TestSelfPurchaseSnapshotFlagAuthStrictnessAndHistoricalRead(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfSubscriptionPurchaseSnapshotEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionPurchaseSnapshotEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfSubscriptionPurchaseSnapshotEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfSubscriptionStatusEnabled: true, EmployeeSelfSubscriptionPurchaseSnapshotEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	disabled := newSelfPurchaseSnapshotFixture(t, false)
	disabledResponse := requestSelfPurchaseSnapshot(t, disabled, "missing/purchase-snapshot", "", "", disabled.cookie)
	disabledResponse.Body.Close()
	if disabledResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled endpoint status=%d", disabledResponse.StatusCode)
	}
	enabled := newSelfPurchaseSnapshotFixture(t, true)
	id, planID := seedSelfPurchaseSnapshot(t, enabled)
	route := id + "/purchase-snapshot"
	for _, cookie := range []*http.Cookie{nil, enabled.adminCookie} {
		readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, route, "", "", cookie), 401)
	}
	readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, route, "", "http://evil.invalid", enabled.cookie), 403)
	for _, suffix := range []string{route + "?", route + "?employee_id=x", route + "?x=1&x=2", route + "/extra", "missing/purchase-snapshot"} {
		want := 400
		if suffix == "missing/purchase-snapshot" {
			want = 404
		}
		readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, suffix, "", "", enabled.cookie), want)
	}
	readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, route, `{}`, "", enabled.cookie), 400)
	session := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, enabled.server.URL+"/self/api/v1/session", "", "", enabled.cookie, ""), 200)
	features := session["features"].(map[string]any)
	if features["employee_self_subscription_purchase_snapshot"] != true {
		t.Fatalf("missing capability: %+v", features)
	}
	var before int
	if err := enabled.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got := readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, route, "", "", enabled.cookie), 200)
	if len(got) != 9 || got["subscription_id"] != id || got["plan_id"] != planID || got["plan_revision"] != float64(1) ||
		got["currency"] != "USD" || got["interval"] != "monthly" || got["price_micro"] != "10" || got["credit_micro"] != "20" ||
		got["started_at"] != "2026-10-02T08:00:00Z" || got["period_end_at"] != "2026-11-02T08:00:00.000000000Z" {
		t.Fatalf("snapshot=%+v", got)
	}
	var after int
	if err := enabled.app.store.db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("read changed entries: before=%d after=%d", before, after)
	}
	if _, err := financial.NewCommercial(enabled.app.store.db).SetEnabled(context.Background(), selfSnapshotMeta(t, enabled, "snapshot-disable", time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)), 2, false); err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, enabled, route, "", "", enabled.cookie), 200)
}

func TestSelfPurchaseSnapshotFailsClosedBeforeResponse(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, true)
	id, _ := seedSelfPurchaseSnapshot(t, f)
	route := id + "/purchase-snapshot"
	f.app.selfPurchaseSnapshotCommit = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
	value := readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, f, route, "", "", f.cookie), 503)
	if _, leaked := value["subscription_id"]; leaked {
		t.Fatalf("partial subscription leaked: %+v", value)
	}
	f.app.selfPurchaseSnapshotCommit = nil
	if _, err := f.app.store.db.Exec(`UPDATE financial_subscriptions SET price_micro=11 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	value = readSelfWalletResponse(t, requestSelfPurchaseSnapshot(t, f, route, "", "", f.cookie), 503)
	if _, leaked := value["price_micro"]; leaked {
		t.Fatalf("broken chain leaked amount: %+v", value)
	}
}

func TestSelfPurchaseSnapshotResponseHasOnlyNineFields(t *testing.T) {
	f := newSelfPurchaseSnapshotFixture(t, true)
	id, _ := seedSelfPurchaseSnapshot(t, f)
	response := requestSelfPurchaseSnapshot(t, f, id+"/purchase-snapshot", "", "", f.cookie)
	defer response.Body.Close()
	var value map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil || len(value) != 9 {
		t.Fatalf("keys=%v err=%v", value, err)
	}
}
