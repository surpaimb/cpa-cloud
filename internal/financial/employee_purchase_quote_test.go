package financial

// Independently authored tests for docs/employee-self-plan-purchase-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func quoteEmployeePlan(t *testing.T, db *sql.DB, commercial *Commercial, input EmployeePurchaseInput) (ExpectedPurchasePlan, error) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	plan, err := commercial.ReadEmployeePurchaseQuoteTx(context.Background(), tx, input.Owner, input.Expected.PlanID)
	if err != nil {
		return ExpectedPurchasePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExpectedPurchasePlan{}, err
	}
	return plan, nil
}

func TestEmployeePurchaseQuoteRequiresCurrentOneTimePlanAndExistingWallet(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	if _, err := quoteEmployeePlan(t, db, commercial, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("default-off quote: %v", err)
	}
	enableEmployeePurchase(t, commercial)
	plan, err := quoteEmployeePlan(t, db, commercial, input)
	if err != nil || plan != input.Expected {
		t.Fatalf("quote=%+v err=%v", plan, err)
	}
	missingWallet := input
	missingWallet.Owner.EmployeeID = "employee-two"
	if _, err := quoteEmployeePlan(t, db, commercial, missingWallet); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing wallet: %v", err)
	}
	keyOwner := input
	keyOwner.Owner.Kind = OwnerKey
	keyOwner.Owner.KeyID = "key-one"
	if _, err := quoteEmployeePlan(t, db, commercial, keyOwner); !errors.Is(err, ErrInvalid) {
		t.Fatalf("key owner: %v", err)
	}
	if _, err := db.Exec(`UPDATE financial_plans SET interval='monthly',revision=2,updated_at=? WHERE id=?`, financialTestTime.Add(time.Hour).Format(time.RFC3339Nano), input.Expected.PlanID); err != nil {
		t.Fatal(err)
	}
	if _, err := quoteEmployeePlan(t, db, commercial, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("monthly quote: %v", err)
	}
}

func TestEmployeePurchaseProbeFindsExactCommittedReceiptBeforeNewGates(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	probe := func(candidate EmployeePurchaseInput) (EmployeePurchaseResult, bool, error) {
		t.Helper()
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		result, found, err := commercial.ProbeEmployeePurchaseReplayTx(context.Background(), tx, candidate)
		if err != nil {
			return result, found, err
		}
		if err := tx.Commit(); err != nil {
			return EmployeePurchaseResult{}, false, err
		}
		return result, found, nil
	}
	if _, found, err := probe(input); err != nil || found {
		t.Fatalf("uncommitted probe found=%t err=%v", found, err)
	}
	first, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0,revision=revision+1,updated_at=? WHERE singleton=1`, financialTestTime.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_plans SET enabled=0,price_micro=99,revision=revision+1,updated_at=? WHERE id=?`, financialTestTime.Add(time.Hour).Format(time.RFC3339Nano), input.Expected.PlanID); err != nil {
		t.Fatal(err)
	}
	input.ObservedAt = input.ObservedAt.Add(24 * time.Hour)
	replay, found, err := probe(input)
	if err != nil || !found || !replay.Replay || replay.Subscription.ID != first.Subscription.ID {
		t.Fatalf("replay=%+v found=%t err=%v", replay, found, err)
	}
	changed := input
	changed.Expected.PriceMicro++
	if _, _, err := probe(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed quote replay: %v", err)
	}
}
