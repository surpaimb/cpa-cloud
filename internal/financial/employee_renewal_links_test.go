package financial

// Independently authored tests for docs/employee-self-subscription-renewal-links-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func readRenewalLinksTest(t *testing.T, c *Commercial, employeeID, subscriptionID string, hooks purchaseSnapshotReadHooks) (EmployeeSubscriptionRenewalLinks, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := c.readEmployeeSubscriptionRenewalLinksTx(ctx, tx, employeeID, subscriptionID, hooks)
	if err != nil {
		return EmployeeSubscriptionRenewalLinks{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmployeeSubscriptionRenewalLinks{}, err
	}
	return result, nil
}

func TestEmployeeRenewalLinksRootMiddleLeafAndHistoricalSwitch(t *testing.T) {
	db := openFinancialTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	ledger, commercial := NewLedger(db), NewCommercial(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postEmployeeBalanceEntry(t, ledger, "links-fund", owner, "USD", EntryAdjustmentCredit, 100, start.Add(-time.Hour))
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "links-enable", "on", start.Add(-time.Hour)), 1, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "links-plan", "plan", start.Add(-time.Hour)), Name: "Monthly", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := commercial.PurchaseSubscription(ctx, PurchaseSubscription{Meta: testCommercialMeta(t, "links-buy", "buy", start), Owner: owner, PlanID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return *first.PeriodEndAt }
	second, _, err := commercial.RenewSubscription(ctx, renewalInput(t, first.ID, "links-renew-1", *first.PeriodEndAt))
	if err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return *second.PeriodEndAt }
	third, _, err := commercial.RenewSubscription(ctx, renewalInput(t, second.ID, "links-renew-2", *second.PeriodEndAt))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, prior, next string
	}{{first.ID, "", second.ID}, {second.ID, first.ID, third.ID}, {third.ID, second.ID, ""}} {
		got, err := readRenewalLinksTest(t, commercial, owner.EmployeeID, tc.id, purchaseSnapshotReadHooks{})
		if err != nil || got.SubscriptionID != tc.id || (got.PredecessorID == nil) != (tc.prior == "") || (got.SuccessorID == nil) != (tc.next == "") {
			t.Fatalf("links=%+v err=%v expected=%+v", got, err, tc)
		}
		if got.PredecessorID != nil && *got.PredecessorID != tc.prior || got.SuccessorID != nil && *got.SuccessorID != tc.next {
			t.Fatalf("links=%+v expected=%+v", got, tc)
		}
	}
	if _, err := db.Exec(`UPDATE financial_plans SET enabled=0,price_micro=12,revision=2 WHERE id=?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "links-disable", "off", third.StartedAt.Add(time.Hour)), 2, false); err != nil {
		t.Fatal(err)
	}
	if got, err := readRenewalLinksTest(t, commercial, owner.EmployeeID, second.ID, purchaseSnapshotReadHooks{}); err != nil || got.PredecessorID == nil || got.SuccessorID == nil {
		t.Fatalf("historical links=%+v err=%v", got, err)
	}
}

func TestEmployeeRenewalLinksOneTimeForeignAndBrokenLink(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	bought, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readRenewalLinksTest(t, commercial, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{})
	if err != nil || got.PredecessorID != nil || got.SuccessorID != nil {
		t.Fatalf("one-time links=%+v err=%v", got, err)
	}
	if _, err := readRenewalLinksTest(t, commercial, "employee-two", bought.Subscription.ID, purchaseSnapshotReadHooks{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read err=%v", err)
	}
	if _, err := db.Exec(`UPDATE financial_subscriptions SET started_at='broken-time' WHERE id=?`, bought.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := readRenewalLinksTest(t, commercial, "employee-two", bought.Subscription.ID, purchaseSnapshotReadHooks{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign malformed read err=%v", err)
	}
	if _, err := readRenewalLinksTest(t, commercial, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("owned malformed read err=%v", err)
	}
}

func TestEmployeeRenewalLinksCloseAndCancelFailure(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	bought, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readRenewalLinksTest(t, commercial, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{closeRows: func(rows *sql.Rows) error {
		_ = rows.Close()
		return errors.New("synthetic close failure")
	}})
	if !errors.Is(err, ErrUnavailable) || got.SubscriptionID != "" {
		t.Fatalf("close links=%+v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err = commercial.readEmployeeSubscriptionRenewalLinksTx(ctx, tx, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{afterRows: cancel})
	if !errors.Is(err, ErrUnavailable) || got.SubscriptionID != "" {
		t.Fatalf("cancel links=%+v err=%v", got, err)
	}
}
