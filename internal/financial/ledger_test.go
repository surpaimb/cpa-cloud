package financial

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var financialTestTime = time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

func TestLedgerImmutableFixedPointIdempotencyAndIsolation(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	credit := Post{OperationID: "operation-credit", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "credit-one", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "credit-one"}}}
	first, err := ledger.Post(context.Background(), credit)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := ledger.Post(context.Background(), credit)
	if err != nil || len(replay) != 1 || replay[0].ID != first[0].ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := credit
	changed.Entries = append([]EntryInput(nil), credit.Entries...)
	changed.Entries[0].AmountMicro = 101
	if _, err := ledger.Post(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry err=%v", err)
	}
	debit := Post{OperationID: "operation-debit", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "debit-one", ObservedAt: financialTestTime.Add(time.Second), RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentDebit, AmountMicro: -70, ResourceKind: "adjustment", ResourceID: "debit-one"}}}
	if _, err := ledger.Post(context.Background(), debit); err != nil {
		t.Fatal(err)
	}
	balance, err := ledger.Balance(context.Background(), owner, "USD")
	if err != nil || balance.AmountMicro != 30 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	overdraw := debit
	overdraw.OperationID, overdraw.ResourceID, overdraw.ObservedAt = "operation-overdraw", "overdraw", financialTestTime.Add(2*time.Second)
	overdraw.Entries = append([]EntryInput(nil), debit.Entries...)
	overdraw.Entries[0].AmountMicro, overdraw.Entries[0].ResourceID = -31, "overdraw"
	if _, err := ledger.Post(context.Background(), overdraw); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("overdraw err=%v", err)
	}
	var operations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_operations WHERE operation_id='operation-overdraw'`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("overdraw persisted count=%d err=%v", operations, err)
	}
	wrongOwner := credit
	wrongOwner.OperationID, wrongOwner.ResourceID = "operation-wrong-owner", "wrong-owner"
	wrongOwner.Entries = append([]EntryInput(nil), credit.Entries...)
	wrongOwner.Entries[0].Owner.EmployeeID, wrongOwner.Entries[0].ResourceID = "employee-two", "wrong-owner"
	if _, err := ledger.Post(context.Background(), wrongOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-employee key err=%v", err)
	}
	resourceOwner := Owner{Kind: OwnerResource, EmployeeID: "employee-one", KeyID: "key-one", ResourceKind: "response", ResourceID: "response-one"}
	resource := Post{OperationID: "operation-resource", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "response", ResourceID: "response-one", ObservedAt: financialTestTime.Add(3 * time.Second), Entries: []EntryInput{{Owner: resourceOwner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 9, ResourceKind: "response", ResourceID: "response-one"}}}
	if _, err := ledger.Post(context.Background(), resource); err != nil {
		t.Fatal(err)
	}
	if balance, err := ledger.Balance(context.Background(), resourceOwner, "USD"); err != nil || balance.AmountMicro != 9 {
		t.Fatalf("resource balance=%+v err=%v", balance, err)
	}
	if _, err := db.Exec(`UPDATE financial_entries SET amount_micro=999 WHERE id=?`, first[0].ID); err == nil {
		t.Fatal("immutable entry update succeeded")
	}
	if _, err := db.Exec(`DELETE FROM financial_entries WHERE id=?`, first[0].ID); err == nil {
		t.Fatal("immutable entry delete succeeded")
	}
}

func TestLedgerRejectsOverflowInvalidSignsAndLookalikeSchema(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if _, err := db.Exec(`CREATE TABLE financial_accounts(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("lookalike migrate err=%v", err)
	}
	for _, name := range []string{"financial_operations", "financial_entries"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial object %s count=%d err=%v", name, count, err)
		}
	}
	if _, err := db.Exec(`DROP TABLE financial_accounts`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	invalid := Post{OperationID: "invalid-sign", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "invalid-sign", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentDebit, AmountMicro: 1, ResourceKind: "adjustment", ResourceID: "invalid-sign"}}}
	if _, err := ledger.Post(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid sign err=%v", err)
	}
	maximum := invalid
	maximum.OperationID, maximum.ResourceID = "maximum", "maximum"
	maximum.Entries = []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: math.MaxInt64, ResourceKind: "adjustment", ResourceID: "maximum"}}
	if _, err := ledger.Post(context.Background(), maximum); err != nil {
		t.Fatal(err)
	}
	overflow := maximum
	overflow.OperationID, overflow.ResourceID, overflow.ObservedAt = "overflow", "overflow", financialTestTime.Add(time.Second)
	overflow.Entries = []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 1, ResourceKind: "adjustment", ResourceID: "overflow"}}
	if _, err := ledger.Post(context.Background(), overflow); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("overflow err=%v", err)
	}
}

func TestLedgerListEntriesFiltersAndStableCursor(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	first, err := ledger.Post(context.Background(), Post{
		OperationID: "list-credit", Action: "adjustment", ActorAdminID: "admin-one",
		ResourceKind: "adjustment", ResourceID: "list-credit", ObservedAt: financialTestTime,
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "list-credit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ledger.Post(context.Background(), Post{
		OperationID: "list-debit", Action: "adjustment", ActorAdminID: "admin-one",
		ResourceKind: "adjustment", ResourceID: "list-debit", ObservedAt: financialTestTime.Add(time.Second), RequireNonNegative: true,
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentDebit, AmountMicro: -25, ResourceKind: "adjustment", ResourceID: "list-debit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resourceOwner := Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "response-one"}
	third, err := ledger.Post(context.Background(), Post{
		OperationID: "list-resource", Action: "adjustment", ActorAdminID: "admin-one",
		ResourceKind: "response", ResourceID: "response-one", ObservedAt: financialTestTime.Add(2 * time.Second),
		Entries: []EntryInput{{Owner: resourceOwner, Currency: "EUR", Kind: EntryAdjustmentCredit, AmountMicro: 7, ResourceKind: "response", ResourceID: "response-one"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	filter := EntryFilter{OwnerKind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one", Currency: "USD", Limit: 1}
	page, err := ledger.ListEntries(context.Background(), filter)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first[0].ID || page.NextCursor != first[0].ID {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	filter.AfterID = page.NextCursor
	page, err = ledger.ListEntries(context.Background(), filter)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second[0].ID || page.NextCursor != "" {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	page, err = ledger.ListEntries(context.Background(), EntryFilter{ResourceKind: "response", ResourceID: "response-one", AccountID: third[0].AccountID, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != third[0].ID || page.Items[0].Owner != resourceOwner {
		t.Fatalf("resource page=%+v err=%v", page, err)
	}
	if _, err := ledger.ListEntries(context.Background(), EntryFilter{OwnerKind: OwnerKey, AfterID: third[0].ID, Limit: 10}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cursor outside filter err=%v", err)
	}
	for _, invalid := range []EntryFilter{{Limit: 0}, {Limit: 101}, {Currency: "usd", Limit: 10}, {OwnerKind: "unknown", Limit: 10}} {
		if _, err := ledger.ListEntries(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid filter %+v err=%v", invalid, err)
		}
	}
}

func openFinancialTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "financial.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`CREATE TABLE admins(id TEXT PRIMARY KEY)`,
		`CREATE TABLE employees(id TEXT PRIMARY KEY)`,
		`CREATE TABLE access_keys(id TEXT PRIMARY KEY,employee_id TEXT NOT NULL REFERENCES employees(id))`,
		`INSERT INTO admins(id) VALUES('admin-one')`,
		`INSERT INTO employees(id) VALUES('employee-one'),('employee-two')`,
		`INSERT INTO access_keys(id,employee_id) VALUES('key-one','employee-one')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}
