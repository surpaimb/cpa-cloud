// Independently authored tests for docs/employee-self-wallet-balance-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"
)

func postEmployeeBalanceEntry(t *testing.T, ledger *Ledger, id string, owner Owner, currency string, kind EntryKind, amount int64, at time.Time) {
	t.Helper()
	_, err := ledger.Post(context.Background(), Post{
		OperationID: id, Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: id,
		ObservedAt: at, Entries: []EntryInput{{Owner: owner, Currency: currency, Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: id}},
	})
	if err != nil {
		t.Fatalf("post %s: %v", id, err)
	}
}

func TestEmployeeBalanceMissingZeroAndOwnerCurrencyIsolation(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD"); err != nil || got.HasAccount {
		t.Fatalf("missing balance=%+v err=%v", got, err)
	}
	emp := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postEmployeeBalanceEntry(t, ledger, "self-usd-credit", emp, "USD", EntryAdjustmentCredit, 50, financialTestTime)
	postEmployeeBalanceEntry(t, ledger, "self-usd-debit", emp, "USD", EntryAdjustmentDebit, -50, financialTestTime.Add(time.Second))
	postEmployeeBalanceEntry(t, ledger, "self-eur", emp, "EUR", EntryAdjustmentCredit, 7, financialTestTime.Add(2*time.Second))
	postEmployeeBalanceEntry(t, ledger, "other-usd", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}, "USD", EntryAdjustmentCredit, 123, financialTestTime.Add(3*time.Second))
	postEmployeeBalanceEntry(t, ledger, "key-usd", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "USD", EntryAdjustmentCredit, 999, financialTestTime.Add(4*time.Second))
	postEmployeeBalanceEntry(t, ledger, "resource-usd", Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "response-one"}, "USD", EntryAdjustmentCredit, 888, financialTestTime.Add(5*time.Second))
	for _, test := range []struct {
		employee, currency string
		amount             int64
	}{{"employee-one", "USD", 0}, {"employee-one", "EUR", 7}, {"employee-two", "USD", 123}} {
		got, err := ledger.ReadEmployeeBalance(context.Background(), test.employee, test.currency)
		if err != nil || !got.HasAccount || got.AmountMicro != test.amount {
			t.Fatalf("%s %s: balance=%+v err=%v", test.employee, test.currency, got, err)
		}
	}
	if _, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "usd"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("lowercase currency err=%v", err)
	}
}

func TestEmployeeBalanceFailsClosedOnBadSchemaRowOverflowAndCommit(t *testing.T) {
	t.Run("schema", func(t *testing.T) {
		db := openFinancialTestDB(t)
		defer db.Close()
		ledger := NewLedger(db)
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DROP INDEX financial_accounts_owner_idx`); err != nil {
			t.Fatal(err)
		}
		if got, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD"); !errors.Is(err, ErrUnavailable) || got != (EmployeeBalance{}) {
			t.Fatalf("schema balance=%+v err=%v", got, err)
		}
	})
	t.Run("row", func(t *testing.T) {
		db := openFinancialTestDB(t)
		defer db.Close()
		ledger := NewLedger(db)
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		postEmployeeBalanceEntry(t, ledger, "bad-row-seed", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, 1, financialTestTime)
		if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('bad-row-op','adjustment','admin','admin-one','adjustment','bad-row',randomblob(32),2,?)`, financialTestTime.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) VALUES('bad-row-entry','bad-row-op',(SELECT id FROM financial_accounts WHERE owner_kind='employee' AND employee_id='employee-one' AND currency='USD'),'adjustment_credit','not-a-number','adjustment','bad-row',?)`, financialTestTime.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if got, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD"); !errors.Is(err, ErrUnavailable) || got != (EmployeeBalance{}) {
			t.Fatalf("row balance=%+v err=%v", got, err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		db := openFinancialTestDB(t)
		defer db.Close()
		ledger := NewLedger(db)
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		postEmployeeBalanceEntry(t, ledger, "max-seed", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, math.MaxInt64, financialTestTime)
		if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('overflow-op','adjustment','admin','admin-one','adjustment','overflow',randomblob(32),2,?)`, financialTestTime.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) VALUES('overflow-entry','overflow-op',(SELECT id FROM financial_accounts WHERE owner_kind='employee' AND employee_id='employee-one' AND currency='USD'),'adjustment_credit',1,'adjustment','overflow',?)`, financialTestTime.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if got, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD"); !errors.Is(err, ErrUnavailable) || got != (EmployeeBalance{}) {
			t.Fatalf("overflow balance=%+v err=%v", got, err)
		}
	})
	t.Run("commit and cancellation", func(t *testing.T) {
		db := openFinancialTestDB(t)
		defer db.Close()
		ledger := NewLedger(db)
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := ledger.readEmployeeBalance(context.Background(), "employee-one", "USD", employeeBalanceReadHooks{commit: func(*sql.Tx) error { return errors.New("synthetic commit fault") }})
		if !errors.Is(err, ErrUnavailable) || got != (EmployeeBalance{}) {
			t.Fatalf("commit balance=%+v err=%v", got, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		got, err = ledger.readEmployeeBalance(ctx, "employee-one", "USD", employeeBalanceReadHooks{afterAccount: cancel})
		if !errors.Is(err, ErrUnavailable) || got != (EmployeeBalance{}) {
			t.Fatalf("cancel balance=%+v err=%v", got, err)
		}
	})
}

func TestEmployeeBalanceUsesOneSnapshot(t *testing.T) {
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
	postEmployeeBalanceEntry(t, ledger, "snapshot-seed", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, 4, financialTestTime)
	got, err := ledger.readEmployeeBalance(context.Background(), "employee-one", "USD", employeeBalanceReadHooks{afterAccount: func() {
		postEmployeeBalanceEntry(t, ledger, "snapshot-later", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, 3, financialTestTime.Add(time.Second))
	}})
	if err != nil || !got.HasAccount || got.AmountMicro != 4 {
		t.Fatalf("snapshot balance=%+v err=%v", got, err)
	}
	latest, err := ledger.ReadEmployeeBalance(context.Background(), "employee-one", "USD")
	if err != nil || latest.AmountMicro != 7 {
		t.Fatalf("latest balance=%+v err=%v", latest, err)
	}
}
