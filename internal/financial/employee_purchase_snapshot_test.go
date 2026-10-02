package financial

// Independently authored tests for docs/employee-self-subscription-purchase-snapshot-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
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
