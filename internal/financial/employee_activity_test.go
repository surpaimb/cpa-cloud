// Independently authored tests for docs/employee-self-wallet-activity-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func financialActivityQuery(employee, currency string, limit int) EmployeeActivityQuery {
	return EmployeeActivityQuery{
		EmployeeID: employee, Currency: currency, Limit: limit,
		WindowStart: financialTestTime.Add(-15 * 24 * time.Hour),
		WindowEnd:   financialTestTime.Add(16 * 24 * time.Hour),
	}
}

func TestEmployeeActivityMissingEmptyAndOwnerIsolation(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := financialActivityQuery("employee-one", "USD", 10)
	if page, err := ledger.ReadEmployeeActivity(context.Background(), query); err != nil || page.HasAccount || len(page.Items) != 0 || page.NextPosition != nil {
		t.Fatalf("missing page=%+v err=%v", page, err)
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postEmployeeBalanceEntry(t, ledger, "activity-old", owner, "USD", EntryAdjustmentCredit, 5, query.WindowStart.Add(-time.Second))
	postEmployeeBalanceEntry(t, ledger, "activity-eur", owner, "EUR", EntryAdjustmentCredit, 7, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "activity-other", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}, "USD", EntryAdjustmentCredit, 111, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "activity-key", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "USD", EntryAdjustmentCredit, 222, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "activity-resource", Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "response-one"}, "USD", EntryAdjustmentCredit, 333, financialTestTime)
	if page, err := ledger.ReadEmployeeActivity(context.Background(), query); err != nil || !page.HasAccount || len(page.Items) != 0 || page.NextPosition != nil {
		t.Fatalf("present-empty page=%+v err=%v", page, err)
	}
	postEmployeeBalanceEntry(t, ledger, "activity-credit", owner, "USD", EntryAdjustmentCredit, 42, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "activity-debit", owner, "USD", EntryAdjustmentDebit, -42, financialTestTime.Add(time.Second))
	page, err := ledger.ReadEmployeeActivity(context.Background(), query)
	if err != nil || !page.HasAccount || page.NextPosition != nil || len(page.Items) != 2 || page.Items[0].DeltaMicro != -42 || page.Items[1].DeltaMicro != 42 {
		t.Fatalf("own activity page=%+v err=%v", page, err)
	}
	if balance, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD"); err != nil || !balance.HasAccount || balance.AmountMicro != 5 {
		t.Fatalf("balance unchanged=%+v err=%v", balance, err)
	}
	query.Currency = "usd"
	if _, err := ledger.ReadEmployeeActivity(context.Background(), query); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lowercase currency err=%v", err)
	}
}

func TestEmployeeActivitySameSecondStablePageAndWindowBounds(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := financialActivityQuery("employee-one", "USD", 1)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postEmployeeBalanceEntry(t, ledger, "window-start", owner, "USD", EntryAdjustmentCredit, 1, query.WindowStart)
	postEmployeeBalanceEntry(t, ledger, "whole-second", owner, "USD", EntryAdjustmentCredit, 2, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "fraction-100", owner, "USD", EntryAdjustmentCredit, 3, financialTestTime.Add(100*time.Millisecond))
	postEmployeeBalanceEntry(t, ledger, "fraction-900-a", owner, "USD", EntryAdjustmentCredit, 4, financialTestTime.Add(900*time.Millisecond))
	postEmployeeBalanceEntry(t, ledger, "fraction-900-b", owner, "USD", EntryAdjustmentCredit, 5, financialTestTime.Add(900*time.Millisecond))
	postEmployeeBalanceEntry(t, ledger, "window-end", owner, "USD", EntryAdjustmentCredit, 6, query.WindowEnd)

	var got []int64
	for i := 0; i < 6; i++ {
		page, err := ledger.ReadEmployeeActivity(context.Background(), query)
		if err != nil || !page.HasAccount || len(page.Items) != 1 {
			t.Fatalf("page %d=%+v err=%v", i, page, err)
		}
		got = append(got, page.Items[0].DeltaMicro)
		if page.NextPosition == nil {
			break
		}
		query.BeforeTime, query.BeforeID = page.NextPosition.Time, page.NextPosition.ID
	}
	if len(got) != 5 || got[0] != 2 || got[3] != 3 || got[4] != 1 {
		t.Fatalf("mixed precision stored-key order=%v", got)
	}
	if !reflect.DeepEqual(map[int64]bool{got[1]: true, got[2]: true}, map[int64]bool{4: true, 5: true}) {
		t.Fatalf("same-stored-time id tie lost=%v", got)
	}
}

func TestEmployeeActivityFailsClosedOnSchemaRowCommitAndCancel(t *testing.T) {
	for _, variant := range []string{"schema", "account", "row", "commit", "cancel"} {
		t.Run(variant, func(t *testing.T) {
			db := openFinancialTestDB(t)
			defer db.Close()
			ledger := NewLedger(db)
			if err := ledger.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			query := financialActivityQuery("employee-one", "USD", 2)
			postEmployeeBalanceEntry(t, ledger, "activity-seed", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, 1, financialTestTime)
			var hooks employeeActivityReadHooks
			switch variant {
			case "schema":
				if _, err := db.Exec(`DROP INDEX financial_entries_account_idx`); err != nil {
					t.Fatal(err)
				}
			case "account":
				if _, err := db.Exec(`INSERT INTO financial_accounts(id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency,created_at)
					VALUES('activity-duplicate-account','employee','malformed-owner-key','employee-one',NULL,'','','USD',?)`, financialTestTime.Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			case "row":
				query.Limit = 1 // The malformed row is the lookahead after the valid seed.
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) VALUES('activity-bad-op','adjustment','admin-one','adjustment','activity-bad',randomblob(32),?)`, financialTestTime.Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) VALUES('activity-bad','activity-bad-op',(SELECT id FROM financial_accounts WHERE owner_kind='employee' AND employee_id='employee-one' AND currency='USD'),'adjustment_credit','bad','adjustment','activity-bad',?)`, financialTestTime.Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			case "commit":
				hooks.commit = func(*sql.Tx) error { return errors.New("synthetic commit fault") }
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hooks.afterAccount = cancel
				page, err := ledger.readEmployeeActivity(ctx, query, hooks)
				if !errors.Is(err, ErrUnavailable) || page.HasAccount || len(page.Items) != 0 {
					t.Fatalf("cancel page=%+v err=%v", page, err)
				}
				return
			}
			page, err := ledger.readEmployeeActivity(context.Background(), query, hooks)
			if !errors.Is(err, ErrUnavailable) || page.HasAccount || len(page.Items) != 0 {
				t.Fatalf("%s page=%+v err=%v", variant, page, err)
			}
		})
	}
}

func TestEmployeeActivityOnePageSnapshotWithConcurrentWriter(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	query := financialActivityQuery("employee-one", "USD", 5)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postEmployeeBalanceEntry(t, ledger, "activity-snapshot-seed", owner, "USD", EntryAdjustmentCredit, 4, financialTestTime)
	page, err := ledger.readEmployeeActivity(context.Background(), query, employeeActivityReadHooks{afterAccount: func() {
		postEmployeeBalanceEntry(t, ledger, "activity-snapshot-later", owner, "USD", EntryAdjustmentCredit, 3, financialTestTime.Add(time.Second))
	}})
	if err != nil || len(page.Items) != 1 || page.Items[0].DeltaMicro != 4 {
		t.Fatalf("snapshot page=%+v err=%v", page, err)
	}
	page, err = ledger.ReadEmployeeActivity(context.Background(), query)
	if err != nil || len(page.Items) != 2 || page.Items[0].DeltaMicro != 3 {
		t.Fatalf("later page=%+v err=%v", page, err)
	}
}
