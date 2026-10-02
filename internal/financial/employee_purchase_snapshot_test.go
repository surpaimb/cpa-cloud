package financial

// Independently authored tests for docs/employee-self-subscription-purchase-snapshot-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func readPurchaseSnapshotTest(t *testing.T, c *Commercial, employeeID, subscriptionID string, hooks purchaseSnapshotReadHooks) (EmployeeSubscriptionPurchaseSnapshot, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	got, err := c.readEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, employeeID, subscriptionID, hooks)
	if err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	return got, nil
}

func TestEmployeePurchaseSnapshotMonthlyPredecessorSuccessorAndPlanMutation(t *testing.T) {
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
	postEmployeeBalanceEntry(t, ledger, "snapshot-monthly-fund", owner, "USD", EntryAdjustmentCredit, 100, start.Add(-time.Hour))
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "snapshot-monthly-enable", "on", start.Add(-time.Hour)), 1, true); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "snapshot-monthly-plan", "plan", start.Add(-time.Hour)), Name: "Monthly", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := commercial.PurchaseSubscription(ctx, PurchaseSubscription{Meta: testCommercialMeta(t, "snapshot-monthly-buy", "buy", start), Owner: owner, PlanID: plan.ID})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := readPurchaseSnapshotTest(t, commercial, owner.EmployeeID, first.ID, purchaseSnapshotReadHooks{})
	if err != nil || initial.Interval != "monthly" || initial.PeriodEndAt == nil || initial.PriceMicro != 10 {
		t.Fatalf("initial=%+v err=%v", initial, err)
	}
	if _, err := db.Exec(`UPDATE financial_plans SET price_micro=12,credit_micro=30,revision=2 WHERE id=?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return *first.PeriodEndAt }
	second, _, err := commercial.RenewSubscription(ctx, renewalInput(t, first.ID, "snapshot-monthly-renew", *first.PeriodEndAt))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := readPurchaseSnapshotTest(t, commercial, owner.EmployeeID, first.ID, purchaseSnapshotReadHooks{})
	if err != nil || prior.PlanRevision != 1 || prior.PriceMicro != 10 || prior.CreditMicro != 20 {
		t.Fatalf("prior=%+v err=%v", prior, err)
	}
	successor, err := readPurchaseSnapshotTest(t, commercial, owner.EmployeeID, second.ID, purchaseSnapshotReadHooks{})
	if err != nil || successor.PlanRevision != 2 || successor.PriceMicro != 12 || successor.CreditMicro != 30 || successor.PeriodEndAt == nil {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
}

func TestEmployeePurchaseSnapshotOwnOneTimeChainAndIsolation(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	bought, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readPurchaseSnapshotTest(t, commercial, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID != bought.Subscription.ID || got.PlanID != input.Expected.PlanID || got.PlanRevision != 1 ||
		got.Currency != "USD" || got.Interval != "one_time" || got.PriceMicro != 40 || got.CreditMicro != 10 ||
		got.StartedAt != formatCommercialTime(input.ObservedAt) || got.PeriodEndAt != nil {
		t.Fatalf("snapshot=%+v", got)
	}
	for _, employee := range []string{"employee-two", "missing-employee"} {
		if _, err := readPurchaseSnapshotTest(t, commercial, employee, bought.Subscription.ID, purchaseSnapshotReadHooks{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign employee=%s err=%v", employee, err)
		}
	}
	if _, err := readPurchaseSnapshotTest(t, commercial, "employee-one", "missing-subscription", purchaseSnapshotReadHooks{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	if _, err := commercial.SetEnabled(context.Background(), testCommercialMeta(t, "snapshot-disable", "off", input.ObservedAt.Add(1)), 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readPurchaseSnapshotTest(t, commercial, "employee-one", bought.Subscription.ID, purchaseSnapshotReadHooks{}); err != nil {
		t.Fatalf("commercial-off historical read err=%v", err)
	}
}

func TestEmployeePurchaseSnapshotForeignAndKeyRowsHideMalformedDates(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	for _, caseValue := range []struct {
		name  string
		owner Owner
	}{
		{"foreign_employee", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}},
		{"key_owned", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}},
	} {
		t.Run(caseValue.name, func(t *testing.T) {
			operationID := "snapshot-foreign-" + caseValue.name
			postEmployeeBalanceEntry(t, NewLedger(db), operationID+"-fund", caseValue.owner, "USD", EntryAdjustmentCredit, 100, input.ObservedAt.Add(-time.Second))
			purchased, _, err := commercial.PurchaseSubscription(context.Background(), PurchaseSubscription{
				Meta: testCommercialMeta(t, operationID, caseValue.name, input.ObservedAt), Owner: caseValue.owner, PlanID: input.Expected.PlanID,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE financial_subscriptions SET started_at='malformed-time' WHERE id=?`, purchased.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := readPurchaseSnapshotTest(t, commercial, "employee-one", purchased.ID, purchaseSnapshotReadHooks{}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s leaked malformed chain status: %v", caseValue.name, err)
			}
		})
	}
}

func TestEmployeePurchaseSnapshotBrokenChainAndReadFailure(t *testing.T) {
	for _, variant := range []string{"missing_receipt", "wrong_charge", "wrong_actor", "wrong_plan", "schema", "close", "cancel"} {
		t.Run(variant, func(t *testing.T) {
			db, commercial, input := employeePurchaseFixture(t, 100)
			enableEmployeePurchase(t, commercial)
			bought, err := employeePurchaseCall(t, db, commercial, input)
			if err != nil {
				t.Fatal(err)
			}
			var hooks purchaseSnapshotReadHooks
			switch variant {
			case "missing_receipt":
				if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_delete`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(commercialOperationsNoDeleteDDL); err != nil {
					t.Fatal(err)
				}
			case "wrong_charge":
				if _, err := db.Exec(`DROP TRIGGER financial_entries_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE financial_entries SET amount_micro=-39 WHERE operation_id=? AND kind='subscription_charge'`, input.OperationID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(entriesNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
			case "wrong_actor":
				if _, err := db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE financial_operations SET actor_employee_id='employee-two' WHERE operation_id=?`, input.OperationID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(operationsNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
			case "wrong_plan":
				if _, err := db.Exec(`UPDATE financial_subscriptions SET plan_id='absent-plan' WHERE id=?`, bought.Subscription.ID); err == nil {
					t.Fatal("foreign-key corruption unexpectedly accepted")
				}
				if _, err := db.Exec(`UPDATE financial_subscriptions SET plan_revision=0 WHERE id=?`, bought.Subscription.ID); err == nil {
					t.Fatal("check corruption unexpectedly accepted")
				}
				// A different, valid plan revision is not independently provable
				// from the amount chain; the contract labels this limit.
				if _, err := db.Exec(`UPDATE financial_subscriptions SET plan_revision=2 WHERE id=?`, bought.Subscription.ID); err != nil {
					t.Fatal(err)
				}
				got, err := readPurchaseSnapshotTest(t, commercial, "employee-one", bought.Subscription.ID, hooks)
				if err != nil || got.PlanRevision != 2 {
					t.Fatalf("controlled-row trust boundary got=%+v err=%v", got, err)
				}
				return
			case "schema":
				if _, err := db.Exec(`DROP TRIGGER financial_entries_no_update`); err != nil {
					t.Fatal(err)
				}
			case "close":
				hooks.closeRows = func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("synthetic close failure") }
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				hooks.afterRows = cancel
				got, err := commercial.readEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, "employee-one", bought.Subscription.ID, hooks)
				if !errors.Is(err, ErrUnavailable) || got.SubscriptionID != "" {
					t.Fatalf("cancel got=%+v err=%v", got, err)
				}
				return
			}
			got, err := readPurchaseSnapshotTest(t, commercial, "employee-one", bought.Subscription.ID, hooks)
			if !errors.Is(err, ErrUnavailable) || got.SubscriptionID != "" {
				t.Fatalf("%s got=%+v err=%v", variant, got, err)
			}
		})
	}
}
