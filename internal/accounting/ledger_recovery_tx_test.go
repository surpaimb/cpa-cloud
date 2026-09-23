package accounting

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// These fixtures are independently authored for caller-owned startup recovery.
func TestLedgerRecoverInterruptedTxAtomicVisibilityAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	db, ledger := openTestLedger(t, path)
	defer db.Close()
	ctx := context.Background()
	for _, id := range []string{"active", "finished-child"} {
		mustBeginRequestAndAttempt(t, ledger, testRequest(id), testAttempt(id+":1", id))
	}
	one := int64(1)
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: "finished-child:1", Status: StatusSucceeded, FinishedAt: testFinish,
		Usage: Usage{InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE recovery_sibling(value INTEGER CHECK(value=1))`); err != nil {
		t.Fatal(err)
	}
	observer := openRawTestDB(t, path)
	defer observer.Close()
	for _, commit := range []bool{false, true} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := ledger.RecoverInterruptedTx(ctx, tx, testFinish.Add(time.Second))
		if err != nil || result.Attempts != 1 || result.Requests != 2 {
			t.Fatalf("tentative recovery=%+v err=%v", result, err)
		}
		var inside int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE status='interrupted'`).Scan(&inside); err != nil || inside != 2 {
			t.Fatalf("transaction requests=%d err=%v", inside, err)
		}
		assertPendingRecovery(t, observer, 2, 1)
		if !commit {
			if _, err := tx.Exec(`INSERT INTO recovery_sibling(value) VALUES(2)`); err == nil {
				t.Fatal("sibling failure was not injected")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertPendingRecovery(t, db, 2, 1)
			continue
		}
		if _, err := tx.Exec(`INSERT INTO recovery_sibling(value) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertPendingRecovery(t, observer, 0, 0)
	}
	var status string
	var input, cost sql.NullInt64
	if err := db.QueryRow(`SELECT status,input_tokens,cost_micro FROM accounting_attempts WHERE id='finished-child:1'`).Scan(&status, &input, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || !input.Valid || input.Int64 != 1 || !cost.Valid || cost.Int64 != 1 {
		t.Fatalf("terminal child changed: %s %+v %+v", status, input, cost)
	}
	if err := db.QueryRow(`SELECT status,input_tokens,cost_micro FROM accounting_attempts WHERE id='active:1'`).Scan(&status, &input, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || input.Valid || cost.Valid {
		t.Fatalf("interrupted usage must stay unknown: %s %+v %+v", status, input, cost)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := ledger.RecoverInterruptedTx(ctx, tx, testFinish.Add(2*time.Second))
	if err != nil || result != (RecoveryResult{}) {
		t.Fatalf("repeated recovery=%+v err=%v", result, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLedgerRecoverInterruptedTxFailureRequiresCallerRollback(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "failure.db"))
	defer db.Close()
	ctx := context.Background()
	mustBeginRequestAndAttempt(t, ledger, testRequest("active"), testAttempt("active:1", "active"))
	// Inject failure after attempt updates, before parent updates. A failed Tx
	// operation is never permission for its caller to commit partial recovery.
	if _, err := db.Exec(`CREATE TRIGGER fail_parent_recovery BEFORE UPDATE ON accounting_requests BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ledger.RecoverInterruptedTx(ctx, tx, testFinish); err == nil || result != (RecoveryResult{}) {
		t.Fatalf("injected failure=%+v err=%v", result, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertPendingRecovery(t, db, 1, 1)
	if _, err := db.Exec(`DROP TRIGGER fail_parent_recovery`); err != nil {
		t.Fatal(err)
	}
	result, err := ledger.RecoverInterrupted(ctx, testFinish)
	if err != nil || result.Attempts != 1 || result.Requests != 1 {
		t.Fatalf("wrapper retry=%+v err=%v", result, err)
	}
}

func TestLedgerRecoverInterruptedTxValidationCancellationAndRollbackClock(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "validation.db"))
	defer db.Close()
	ctx := context.Background()
	var nilLedger *Ledger
	for _, tc := range []struct {
		ledger *Ledger
		ctx    context.Context
		tx     *sql.Tx
		at     time.Time
	}{
		{nilLedger, ctx, &sql.Tx{}, testFinish},
		{NewLedger(nil), ctx, &sql.Tx{}, testFinish},
		{ledger, nil, &sql.Tx{}, testFinish},
		{ledger, ctx, nil, testFinish},
		{ledger, ctx, &sql.Tx{}, time.Time{}},
	} {
		if _, err := tc.ledger.RecoverInterruptedTx(tc.ctx, tc.tx, tc.at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid argument error=%v", err)
		}
	}
	mustBeginRequestAndAttempt(t, ledger, testRequest("active"), testAttempt("active:1", "active"))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.RecoverInterruptedTx(ctx, tx, testStart.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("clock rollback must fail unchanged: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ledger.RecoverInterruptedTx(cancelled, tx, testFinish); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertPendingRecovery(t, db, 1, 1)
}

func assertPendingRecovery(t *testing.T, db *sql.DB, requests, attempts int) {
	t.Helper()
	var actualRequests, actualAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE status='pending'`).Scan(&actualRequests); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE status='pending'`).Scan(&actualAttempts); err != nil {
		t.Fatal(err)
	}
	if actualRequests != requests || actualAttempts != attempts {
		t.Fatalf("pending requests/attempts %d/%d want %d/%d", actualRequests, actualAttempts, requests, attempts)
	}
}
