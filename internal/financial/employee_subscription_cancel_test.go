package financial

// Independently authored acceptance tests for
// docs/employee-self-subscription-cancel-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func employeeCancelInput(id, operation string, revision int64, at time.Time) EmployeeSubscriptionCancelInput {
	return EmployeeSubscriptionCancelInput{
		OperationID: operation, Actor: Actor{Kind: ActorEmployee, ID: "employee-one"},
		SubscriptionID: id, ExpectedRevision: revision, ObservedAt: at,
	}
}

func TestEmployeeSubscriptionCancelOwnOneTimeReplayAndCorruptChain(t *testing.T) {
	db, commercial, _, oncePlan := employeeSubscriptionFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	account := accountForSubscriptionTest(t, db, "employee", "employee-one")
	insertEmployeeSubscriptionTest(t, db, "self-cancel-once", account, oncePlan, "one_time", "active", start, nil)
	input := employeeCancelInput("self-cancel-once", "self-cancel-operation", 1, start.Add(time.Hour))
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := commercial.ProbeEmployeeSubscriptionCancelReplayTx(context.Background(), tx, input); err != nil || found {
		t.Fatalf("initial probe found=%v err=%v", found, err)
	}
	result, err := commercial.CancelEmployeeSubscriptionTx(context.Background(), tx, input)
	if err != nil || result.Subscription.Status != "cancelled" || result.Subscription.Revision != 2 || result.Receipt.OperationID != input.OperationID {
		t.Fatalf("provisional cancellation=%+v err=%v", result, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var commercialCount, ledgerCount, entryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&commercialCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_operations WHERE operation_id=?`, input.OperationID).Scan(&ledgerCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, input.OperationID).Scan(&entryCount); err != nil {
		t.Fatal(err)
	}
	if commercialCount != 1 || ledgerCount != 0 || entryCount != 0 {
		t.Fatalf("operation counts commercial=%d ledger=%d entries=%d", commercialCount, ledgerCount, entryCount)
	}
	input.ObservedAt = start.Add(48 * time.Hour)
	replayTx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	replayed, found, err := commercial.ProbeEmployeeSubscriptionCancelReplayTx(context.Background(), replayTx, input)
	_ = replayTx.Rollback()
	if err != nil || !found || !replayed.Receipt.CreatedAt.Equal(result.Receipt.CreatedAt) {
		t.Fatalf("replay=%+v found=%v err=%v", replayed, found, err)
	}
	other := input
	other.ExpectedRevision = 2
	conflictTx, _ := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	_, _, err = commercial.ProbeEmployeeSubscriptionCancelReplayTx(context.Background(), conflictTx, other)
	_ = conflictTx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed revision err=%v", err)
	}
	if _, err := db.Exec(`UPDATE financial_subscriptions SET cancelled_at='2026-02-02T08:00:00Z' WHERE id='self-cancel-once'`); err != nil {
		t.Fatal(err)
	}
	brokenTx, _ := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	_, _, err = commercial.ProbeEmployeeSubscriptionCancelReplayTx(context.Background(), brokenTx, input)
	_ = brokenTx.Rollback()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("broken committed chain err=%v", err)
	}
}

func TestEmployeeSubscriptionCancelIsolationExpiryAndLedgerID(t *testing.T) {
	db, commercial, monthPlan, oncePlan := employeeSubscriptionFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	end, err := monthlyPeriodEnd(start)
	if err != nil {
		t.Fatal(err)
	}
	insertEmployeeSubscriptionTest(t, db, "self-other", accountForSubscriptionTest(t, db, "employee", "employee-two"), oncePlan, "one_time", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "self-key", accountForSubscriptionTest(t, db, "key", "employee-one"), oncePlan, "one_time", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "self-month", accountForSubscriptionTest(t, db, "employee", "employee-one"), monthPlan, "monthly", "active", start, nil)
	for _, id := range []string{"self-other", "self-key"} {
		tx, _ := db.BeginTx(context.Background(), nil)
		_, err := commercial.CancelEmployeeSubscriptionTx(context.Background(), tx, employeeCancelInput(id, "attempt-"+id, 1, start.Add(time.Hour)))
		_ = tx.Rollback()
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("hidden %s err=%v", id, err)
		}
	}
	for _, observed := range []time.Time{end, end.Add(time.Nanosecond)} {
		tx, _ := db.BeginTx(context.Background(), nil)
		_, err := commercial.CancelEmployeeSubscriptionTx(context.Background(), tx, employeeCancelInput("self-month", "due-attempt", 1, observed))
		_ = tx.Rollback()
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("due at %s err=%v", observed, err)
		}
	}
	ledgerOnly := employeeCancelInput("self-month", "subscription-employee-one", 1, start.Add(time.Hour))
	tx, _ := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	_, _, err = commercial.ProbeEmployeeSubscriptionCancelReplayTx(context.Background(), tx, ledgerOnly)
	_ = tx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ledger-only ID err=%v", err)
	}
	writeTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = commercial.CancelEmployeeSubscriptionTx(context.Background(), writeTx, ledgerOnly)
	_ = writeTx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("direct new-write primitive reused ledger ID: %v", err)
	}
}

func TestEmployeeSubscriptionCancelClosesArmedOneShotWithoutCommercialGate(t *testing.T) {
	db, commercial, monthPlan, _ := employeeSubscriptionFixture(t)
	defer db.Close()
	ctx := context.Background()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	end, err := monthlyPeriodEnd(start)
	if err != nil {
		t.Fatal(err)
	}
	account := accountForSubscriptionTest(t, db, "employee", "employee-one")
	insertEmployeeSubscriptionTest(t, db, "self-armed-month", account, monthPlan, "monthly", "active", start, nil)
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	at := end.Add(-time.Hour)
	commercial.now = func() time.Time { return at }
	armed, _, err := commercial.ArmOneShotRenewal(ctx, oneShotArmInput(t, "self-armed-month", "self-arm-operation", 1, at))
	if err != nil || armed.State != "armed" {
		t.Fatalf("arm=%+v err=%v", armed, err)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := commercial.CancelEmployeeSubscriptionTx(ctx, tx, employeeCancelInput("self-armed-month", "self-cancel-armed", 1, at.Add(time.Minute)))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := commercial.OneShotRenewal(ctx, "self-armed-month")
	if err != nil || status.State != "cancelled" || status.Reason != "predecessor_cancelled" || status.TerminalAt == nil || !status.TerminalAt.Equal(*result.Subscription.CancelledAt) {
		t.Fatalf("terminal reservation=%+v result=%+v err=%v", status, result, err)
	}
	var ledgerCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_operations WHERE operation_id='self-cancel-armed'`).Scan(&ledgerCount); err != nil || ledgerCount != 0 {
		t.Fatalf("unexpected cancel ledger count=%d err=%v", ledgerCount, err)
	}
}

func TestEmployeeSubscriptionCancelRejectsMalformedDirectAccount(t *testing.T) {
	db, commercial, _, oncePlan := employeeSubscriptionFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	account := accountForSubscriptionTest(t, db, "employee", "employee-one")
	insertEmployeeSubscriptionTest(t, db, "self-bad-account", account, oncePlan, "one_time", "active", start, nil)
	if _, err := db.Exec(`DROP TRIGGER financial_accounts_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_accounts SET owner_key='malformed-owner' WHERE id=?`, account); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(accountsNoUpdateDDL); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = commercial.CancelEmployeeSubscriptionTx(context.Background(), tx, employeeCancelInput("self-bad-account", "bad-account-cancel", 1, start.Add(time.Hour)))
	_ = tx.Rollback()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("malformed owner err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='bad-account-cancel'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("malformed owner receipt count=%d err=%v", count, err)
	}
}
