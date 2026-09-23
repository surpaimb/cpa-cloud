package accounting

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLedgerFinishTransactionRollsBackCallerFailureAndRetries(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-transaction")
	attempt := testAttempt("attempt-transaction", request.ID)
	mustBeginRequestAndAttempt(t, ledger, request, attempt)
	if _, err := db.Exec(`CREATE TABLE model_requests(id TEXT PRIMARY KEY,outcome TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_requests(id,outcome) VALUES(?,?)`, request.ID, "running"); err != nil {
		t.Fatal(err)
	}

	input, output, cacheRead, cacheWrite := int64(1_000_000), int64(2_000_000), int64(3_000_000), int64(4_000_000)
	attemptFinish := AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite,
	}}
	requestFinish := RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishAttemptTx(ctx, tx, attemptFinish); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequestTx(ctx, tx, requestFinish); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_requests SET outcome='succeeded' WHERE id=?`, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_requests(id,outcome) VALUES(?,?)`, request.ID, "duplicate"); err == nil {
		t.Fatal("caller failure injection unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertTransactionLedgerRows(t, db, request.ID, attempt.ID, StatusPending, "running", nil)

	commitFinishTransaction(t, ctx, db, ledger, attemptFinish, requestFinish, request.ID)
	assertTransactionLedgerRows(t, db, request.ID, attempt.ID, StatusSucceeded, "succeeded", transactionInt64(300))

	// An exact retry in a new caller-owned transaction remains idempotent.
	commitFinishTransaction(t, ctx, db, ledger, attemptFinish, requestFinish, request.ID)
	assertTransactionLedgerRows(t, db, request.ID, attempt.ID, StatusSucceeded, "succeeded", transactionInt64(300))
}

func TestLedgerFinishTransactionRejectsNilAndConflictingReplay(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-transaction-conflict")
	attempt := testAttempt("attempt-transaction-conflict", request.ID)
	mustBeginRequestAndAttempt(t, ledger, request, attempt)
	one := int64(1)
	attemptFinish := AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one,
	}}
	requestFinish := RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}

	if !errors.Is(ledger.FinishAttemptTx(ctx, nil, attemptFinish), ErrInvalid) {
		t.Fatal("FinishAttemptTx accepted nil transaction")
	}
	if !errors.Is(ledger.FinishRequestTx(ctx, nil, requestFinish), ErrInvalid) {
		t.Fatal("FinishRequestTx accepted nil transaction")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishAttemptTx(ctx, tx, attemptFinish); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequestTx(ctx, tx, requestFinish); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	conflictingAttempt := attemptFinish
	conflictingAttempt.Status = StatusFailed
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishAttemptTx(ctx, tx, conflictingAttempt); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting attempt error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	conflictingRequest := requestFinish
	conflictingRequest.Status = StatusFailed
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequestTx(ctx, tx, conflictingRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting request error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertTransactionLedgerRows(t, db, request.ID, attempt.ID, StatusSucceeded, "", transactionInt64(1))
}

func commitFinishTransaction(t *testing.T, ctx context.Context, db *sql.DB, ledger *Ledger, attempt AttemptFinish, request RequestFinish, legacyID string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ledger.FinishAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequestTx(ctx, tx, request); err != nil {
		t.Fatal(err)
	}
	if legacyID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE model_requests SET outcome='succeeded' WHERE id=?`, legacyID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertTransactionLedgerRows(t *testing.T, db *sql.DB, requestID, attemptID string, want Status, legacyOutcome string, wantCost *int64) {
	t.Helper()
	var requestStatus, attemptStatus Status
	var input, cost sql.NullInt64
	if err := db.QueryRow(`SELECT status FROM accounting_requests WHERE id=?`, requestID).Scan(&requestStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,input_tokens,cost_micro FROM accounting_attempts WHERE id=?`, attemptID).Scan(&attemptStatus, &input, &cost); err != nil {
		t.Fatal(err)
	}
	if requestStatus != want || attemptStatus != want || input.Valid != (wantCost != nil) || cost.Valid != (wantCost != nil) || wantCost != nil && cost.Int64 != *wantCost {
		t.Fatalf("stored request=%q attempt=%q input=%+v cost=%+v", requestStatus, attemptStatus, input, cost)
	}
	if legacyOutcome != "" {
		var got string
		if err := db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, requestID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != legacyOutcome {
			t.Fatalf("legacy outcome=%q want=%q", got, legacyOutcome)
		}
	}
}

func transactionInt64(value int64) *int64 { return &value }
