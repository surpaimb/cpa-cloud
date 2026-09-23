package accounting

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestBeginRequestCallerTransactionRollbackAndReplay(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "atomic-begin.db"))
	defer db.Close()
	ctx := context.Background()
	input := testRequest("atomic-request")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginRequestTx(ctx, tx, input); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback rows=%d err=%v", count, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ledger.BeginRequestTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginRequestTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.KeyID = "another-key"
	if err := ledger.BeginRequestTx(ctx, tx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting input err=%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginRequest(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay rows=%d err=%v", count, err)
	}
	if err := ledger.BeginRequestTx(ctx, nil, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction err=%v", err)
	}
}
