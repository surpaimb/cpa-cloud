// Independently authored tests for docs/employee-self-wallet-entry-classification-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func classificationPost(t *testing.T, ledger *Ledger, operation, action string, kind EntryKind, amount int64, original string, at time.Time) Entry {
	t.Helper()
	resource := "classification-" + operation
	items, err := ledger.Post(context.Background(), Post{
		OperationID: operation, Action: action, Actor: Actor{Kind: ActorAdmin, ID: "admin-one"},
		ResourceKind: action, ResourceID: resource, ObservedAt: at,
		Entries: []EntryInput{{Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, Currency: "USD",
			Kind: kind, AmountMicro: amount, OriginalEntryID: original, ResourceKind: action, ResourceID: resource}},
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("post %s entries=%v err=%v", operation, items, err)
	}
	return items[0]
}

func TestEmployeeClassificationKindsAndReversals(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := financialActivityQuery("employee-one", "USD", 20)
	if page, err := ledger.ReadEmployeeClassifications(context.Background(), query); err != nil || page.HasAccount || len(page.Items) != 0 {
		t.Fatalf("missing=%+v err=%v", page, err)
	}
	base := financialTestTime
	classificationPost(t, ledger, "kind-adjust-credit", "adjustment", EntryAdjustmentCredit, 200, "", base)
	classificationPost(t, ledger, "kind-adjust-debit", "adjustment", EntryAdjustmentDebit, -5, "", base.Add(time.Second))
	topup := classificationPost(t, ledger, "kind-topup", "topup", EntryTopUp, 100, "", base.Add(2*time.Second))
	classificationPost(t, ledger, "kind-redemption", "redemption", EntryRedemption, 6, "", base.Add(3*time.Second))
	classificationPost(t, ledger, "kind-charge", "subscription_purchase", EntrySubscriptionCharge, -10, "", base.Add(4*time.Second))
	classificationPost(t, ledger, "kind-credit", "subscription_purchase", EntrySubscriptionCredit, 12, "", base.Add(5*time.Second))
	usage := classificationPost(t, ledger, "kind-usage", "usage_charge", EntryUsageCharge, -7, "", base.Add(6*time.Second))
	classificationPost(t, ledger, "kind-refund-credit", "refund", EntryRefund, 3, usage.ID, base.Add(7*time.Second))
	classificationPost(t, ledger, "kind-refund-debit", "refund", EntryAdjustmentDebit, -40, topup.ID, base.Add(8*time.Second))
	page, err := ledger.ReadEmployeeClassifications(context.Background(), query)
	if err != nil || !page.HasAccount || len(page.Items) != 9 || page.NextPosition != nil {
		t.Fatalf("classified=%+v err=%v", page, err)
	}
	if page.Items[0].EntryKind != EntryAdjustmentDebit || page.Items[0].DeltaMicro != -40 || page.Items[1].EntryKind != EntryRefund ||
		page.Items[2].EntryKind != EntryUsageCharge || page.Items[8].EntryKind != EntryAdjustmentCredit {
		t.Fatalf("stored kinds=%+v", page.Items)
	}
	query.Limit = 1
	first, err := ledger.ReadEmployeeClassifications(context.Background(), query)
	if err != nil || len(first.Items) != 1 || first.NextPosition == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	query.BeforeTime, query.BeforeID = first.NextPosition.Time, first.NextPosition.ID
	second, err := ledger.ReadEmployeeClassifications(context.Background(), query)
	if err != nil || len(second.Items) != 1 || second.Items[0].EntryKind != EntryRefund {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestEmployeeClassificationFailsClosedOnLedgerDrift(t *testing.T) {
	for _, variant := range []string{"schema", "operation-action", "operation-time", "operation-resource", "digest-length", "actor-shape", "orphan-admin-actor", "orphan-employee-actor", "entry-kind", "reversal-over", "commit", "cancel"} {
		t.Run(variant, func(t *testing.T) {
			db := openFinancialTestDB(t)
			defer db.Close()
			ledger := NewLedger(db)
			if err := ledger.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			query := financialActivityQuery("employee-one", "USD", 10)
			seed := classificationPost(t, ledger, "classification-seed", "topup", EntryTopUp, 100, "", financialTestTime)
			var hooks employeeClassificationReadHooks
			change := func(statement string, args ...any) {
				t.Helper()
				if _, err := db.Exec(statement, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch variant {
			case "schema":
				change(`DROP TRIGGER financial_operations_no_delete`)
			case "operation-action", "operation-time", "operation-resource", "digest-length", "actor-shape":
				change(`DROP TRIGGER financial_operations_no_update`)
				switch variant {
				case "operation-action":
					change(`UPDATE financial_operations SET action='redemption' WHERE operation_id='classification-seed'`)
				case "operation-time":
					change(`UPDATE financial_operations SET created_at=? WHERE operation_id='classification-seed'`, financialTestTime.Add(time.Second).Format(time.RFC3339Nano))
				case "operation-resource":
					change(`UPDATE financial_operations SET resource_id='different' WHERE operation_id='classification-seed'`)
				case "digest-length":
					change(`PRAGMA ignore_check_constraints=ON`)
					change(`UPDATE financial_operations SET payload_digest=x'00' WHERE operation_id='classification-seed'`)
				case "actor-shape":
					change(`PRAGMA ignore_check_constraints=ON`)
					change(`UPDATE financial_operations SET actor_kind='legacy_unknown',digest_version=2 WHERE operation_id='classification-seed'`)
				}
				change(operationsNoUpdateDDL)
			case "entry-kind":
				change(`DROP TRIGGER financial_entries_no_update`)
				change(`UPDATE financial_entries SET kind='usage_charge' WHERE id=?`, seed.ID)
				change(entriesNoUpdateDDL)
			case "orphan-admin-actor", "orphan-employee-actor":
				change(`PRAGMA foreign_keys=OFF`)
				change(`DROP TRIGGER financial_operations_no_update`)
				if variant == "orphan-admin-actor" {
					change(`UPDATE financial_operations SET actor_admin_id='missing-admin' WHERE operation_id='classification-seed'`)
				} else {
					change(`UPDATE financial_operations SET actor_kind='employee',actor_admin_id=NULL,actor_employee_id='missing-employee' WHERE operation_id='classification-seed'`)
				}
				change(operationsNoUpdateDDL)
				change(`PRAGMA foreign_keys=ON`)
			case "reversal-over":
				classificationPost(t, ledger, "classification-refund-one", "refund", EntryAdjustmentDebit, -40, seed.ID, financialTestTime.Add(time.Second))
				change(`INSERT INTO financial_operations(operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('classification-over','refund','admin','admin-one','refund','classification-over',randomblob(32),2,?)`, financialTestTime.Add(2*time.Second).Format(time.RFC3339Nano))
				change(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,original_entry_id,resource_kind,resource_id,created_at) VALUES('classification-over-entry','classification-over',?,'adjustment_debit',-70,?,'refund','classification-over',?)`, seed.AccountID, seed.ID, financialTestTime.Add(2*time.Second).Format(time.RFC3339Nano))
			case "commit":
				hooks.commit = func(*sql.Tx) error { return errors.New("synthetic classification commit fault") }
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hooks.afterAccount = cancel
				page, err := ledger.readEmployeeClassifications(ctx, query, hooks)
				if !errors.Is(err, ErrUnavailable) || page.HasAccount || len(page.Items) != 0 {
					t.Fatalf("cancelled page=%+v err=%v", page, err)
				}
				return
			}
			page, err := ledger.readEmployeeClassifications(context.Background(), query, hooks)
			if !errors.Is(err, ErrUnavailable) || page.HasAccount || len(page.Items) != 0 {
				t.Fatalf("%s page=%+v err=%v", variant, page, err)
			}
		})
	}
}

func TestEmployeeClassificationRejectsOrphanActorsInReversalLinks(t *testing.T) {
	for _, operation := range []string{"classification-original", "classification-older-sibling"} {
		t.Run(operation, func(t *testing.T) {
			db := openFinancialTestDB(t)
			defer db.Close()
			ledger := NewLedger(db)
			if err := ledger.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			original := classificationPost(t, ledger, "classification-original", "topup", EntryTopUp, 100, "", financialTestTime)
			classificationPost(t, ledger, "classification-older-sibling", "refund", EntryAdjustmentDebit, -10, original.ID, financialTestTime.Add(time.Second))
			classificationPost(t, ledger, "classification-selected", "refund", EntryAdjustmentDebit, -20, original.ID, financialTestTime.Add(2*time.Second))
			if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE financial_operations SET actor_admin_id='missing-admin' WHERE operation_id=?`, operation); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(operationsNoUpdateDDL); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
			start := financialTestTime.Add(2 * time.Second)
			query := EmployeeActivityQuery{EmployeeID: "employee-one", Currency: "USD", Limit: 1, WindowStart: start, WindowEnd: start.Add(31 * 24 * time.Hour)}
			page, err := ledger.ReadEmployeeClassifications(context.Background(), query)
			if !errors.Is(err, ErrUnavailable) || page.HasAccount || len(page.Items) != 0 {
				t.Fatalf("orphan %s page=%+v err=%v", operation, page, err)
			}
		})
	}
}
