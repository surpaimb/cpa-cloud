package financial

// Independently authored transaction and race checks for
// docs/employee-self-redemption-contract.md. All credentials are synthetic.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func employeeRedemptionFixture(t *testing.T, maxUses int64) (*sql.DB, *Commercial, [32]byte) {
	t.Helper()
	db := openFinancialTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	c := NewCommercial(db)
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetEnabled(ctx, testCommercialMeta(t, "redemption-enable", "on", financialTestTime), 1, true); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("synthetic-employee-redemption-code"))
	if _, _, err := c.CreateCode(ctx, CreateRedemptionCode{Meta: testCommercialMeta(t, "redemption-issue", "synthetic code", financialTestTime.Add(time.Second)),
		CodeDigest: digest, Currency: "EUR", AmountMicro: 43, MaxUses: maxUses}); err != nil {
		t.Fatal(err)
	}
	return db, c, digest
}

func employeeRedemptionInput(id, employee string, digest [32]byte) EmployeeRedemptionInput {
	return EmployeeRedemptionInput{OperationID: id, Actor: Actor{Kind: ActorEmployee, ID: employee},
		Owner: Owner{Kind: OwnerEmployee, EmployeeID: employee}, CodeDigest: digest,
		ObservedAt: financialTestTime.Add(2 * time.Second)}
}

func callEmployeeRedemptionTx(ctx context.Context, db *sql.DB, c *Commercial, input EmployeeRedemptionInput) (EmployeeRedemptionResult, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	defer tx.Rollback()
	result, err := c.RedeemEmployeeCodeTx(ctx, tx, input)
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	return result, nil
}

func assertEmployeeRedemptionFacts(t *testing.T, db *sql.DB, uses, facts, receipts int) {
	t.Helper()
	queries := map[string]int{
		`SELECT uses FROM financial_redemption_codes`:                                            uses,
		`SELECT COUNT(*) FROM financial_accounts WHERE owner_kind='employee' AND currency='EUR'`: facts,
		`SELECT COUNT(*) FROM financial_operations WHERE action='redemption'`:                    facts,
		`SELECT COUNT(*) FROM financial_entries WHERE kind='redemption'`:                         facts,
		`SELECT COUNT(*) FROM financial_redemptions`:                                             facts,
		`SELECT COUNT(*) FROM financial_commercial_operations WHERE action='redemption.redeem'`:  receipts,
	}
	for query, want := range queries {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d want=%d err=%v", query, got, want, err)
		}
	}
}

func TestEmployeeRedemptionPrimitiveLastUseWALSnapshot(t *testing.T) {
	db, c, digest := employeeRedemptionFixture(t, 1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	ctx := context.Background()
	inputs := []EmployeeRedemptionInput{
		employeeRedemptionInput("wal-employee-one", "employee-one", digest),
		employeeRedemptionInput("wal-employee-two", "employee-two", digest),
	}
	txs := make([]*sql.Tx, 2)
	for i := range txs {
		var count int
		var err error
		txs[i], err = db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer txs[i].Rollback()
		// Both WAL readers pin the same pre-credit snapshot before either writer.
		if err := txs[i].QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_redemptions`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("snapshot count=%d err=%v", count, err)
		}
	}
	results := make([]EmployeeRedemptionResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range txs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = c.RedeemEmployeeCodeTx(ctx, txs[i], inputs[i])
			if errs[i] == nil {
				errs[i] = txs[i].Commit()
			}
		}(i)
	}
	wg.Wait()
	success := 0
	for i, err := range errs {
		if err == nil {
			success++
			if results[i].Replay || results[i].AmountMicro != 43 {
				t.Fatalf("winner=%+v", results[i])
			}
		} else if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrConflict) {
			t.Fatalf("loser error=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("WAL winners=%d errors=%v", success, errs)
	}
	assertEmployeeRedemptionFacts(t, db, 1, 1, 1)
}

func TestEmployeeRedemptionPrimitiveSameEmployeeAndAdminCompetition(t *testing.T) {
	for _, scenario := range []string{"same-employee", "admin-vs-employee"} {
		t.Run(scenario, func(t *testing.T) {
			db, c, digest := employeeRedemptionFixture(t, 2)
			if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(4)
			start := make(chan struct{})
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range errs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					if i == 1 && scenario == "admin-vs-employee" {
						_, _, errs[i] = c.Redeem(context.Background(), RedeemCode{Meta: WriteMeta{OperationID: "admin-competitor", Actor: Actor{Kind: ActorAdmin, ID: "admin-one"},
							PayloadDigest: sha256.Sum256([]byte("admin-competitor")), ObservedAt: financialTestTime.Add(2 * time.Second)},
							CodeDigest: digest, Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}})
						return
					}
					id := "self-competitor-one"
					if i == 1 {
						id = "self-competitor-two"
					}
					_, errs[i] = callEmployeeRedemptionTx(context.Background(), db, c, employeeRedemptionInput(id, "employee-one", digest))
				}(i)
			}
			close(start)
			wg.Wait()
			success := 0
			for _, err := range errs {
				if err == nil {
					success++
				} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrUnavailable) {
					t.Fatalf("competitor error=%v", err)
				}
			}
			if success != 1 {
				t.Fatalf("competitor winners=%d errors=%v", success, errs)
			}
			assertEmployeeRedemptionFacts(t, db, 1, 1, 1)
		})
	}
}

func TestEmployeeRedemptionPrimitiveConcurrentSameOperationID(t *testing.T) {
	db, c, digest := employeeRedemptionFixture(t, 1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	input := employeeRedemptionInput("same-operation", "employee-one", digest)
	start := make(chan struct{})
	results := make([]EmployeeRedemptionResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = callEmployeeRedemptionTx(context.Background(), db, c, input)
		}(i)
	}
	close(start)
	wg.Wait()
	newCredits := 0
	for i, err := range errs {
		if err == nil {
			if !results[i].Replay {
				newCredits++
			}
		} else if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("same-ID contender error=%v", err)
		}
	}
	if newCredits != 1 {
		t.Fatalf("new credits=%d results=%v errors=%v", newCredits, results, errs)
	}
	assertEmployeeRedemptionFacts(t, db, 1, 1, 1)
}

func TestEmployeeRedemptionPrimitiveInsertFailureRollsBack(t *testing.T) {
	db, c, digest := employeeRedemptionFixture(t, 1)
	c.employeeRedemptionBeforeInsert = func(ctx context.Context, tx *sql.Tx) error {
		// Created only after schema validation; the failed transaction rolls
		// this synthetic fault trigger back along with every financial write.
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER employee_redemption_test_abort BEFORE INSERT ON financial_redemptions BEGIN SELECT RAISE(ABORT,'synthetic insert fault'); END`)
		return err
	}
	input := employeeRedemptionInput("insert-fault", "employee-one", digest)
	if _, err := callEmployeeRedemptionTx(context.Background(), db, c, input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("insert failure classification=%v", err)
	}
	assertEmployeeRedemptionFacts(t, db, 0, 0, 0)
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='employee_redemption_test_abort'`).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatalf("fault trigger persisted=%d err=%v", triggers, err)
	}
	c.employeeRedemptionBeforeInsert = nil
	if _, err := callEmployeeRedemptionTx(context.Background(), db, c, input); err != nil {
		t.Fatalf("retry after rolled-back insert fault=%v", err)
	}
	assertEmployeeRedemptionFacts(t, db, 1, 1, 1)
}

func TestEmployeeRedemptionPrimitiveRollbackAndGlobalSingleSide(t *testing.T) {
	db, c, digest := employeeRedemptionFixture(t, 1)
	input := employeeRedemptionInput("rollback-redemption", "employee-one", digest)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RedeemEmployeeCodeTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertEmployeeRedemptionFacts(t, db, 0, 0, 0)
	if _, err := callEmployeeRedemptionTx(ctx, db, c, input); err != nil {
		t.Fatal(err)
	}
	assertEmployeeRedemptionFacts(t, db, 1, 1, 1)
	var triggerDDL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='financial_commercial_operations_no_delete'`).Scan(&triggerDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM financial_commercial_operations WHERE operation_id='rollback-redemption'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(triggerDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := callEmployeeRedemptionTx(ctx, db, c, input); !errors.Is(err, ErrSchema) {
		t.Fatalf("ledger-only replay error=%v", err)
	}
	assertEmployeeRedemptionFacts(t, db, 1, 1, 0)
}
