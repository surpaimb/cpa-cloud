package financial

// Independently authored tests for docs/employee-self-one-shot-disarm-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func employeeOneShotFixture(t *testing.T) (*sql.DB, *Commercial, time.Time) {
	t.Helper()
	db, commercial, monthPlan, oncePlan := employeeSubscriptionFixture(t)
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	due, err := monthlyPeriodEnd(start)
	if err != nil {
		t.Fatal(err)
	}
	owned := accountForSubscriptionTest(t, db, "employee", "employee-one")
	other := accountForSubscriptionTest(t, db, "employee", "employee-two")
	key := accountForSubscriptionTest(t, db, "key", "employee-one")
	insertEmployeeSubscriptionTest(t, db, "self-shot-owned", owned, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "self-shot-other", other, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "self-shot-key", key, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "self-shot-once", owned, oncePlan, "one_time", "active", start, nil)
	return db, commercial, due
}

func employeeOneShotInput(id, operation string, revision int64, at time.Time) EmployeeOneShotDisarmInput {
	return EmployeeOneShotDisarmInput{
		OperationID: operation, Actor: Actor{Kind: ActorEmployee, ID: "employee-one"},
		PredecessorID: id, ExpectedRevision: revision, ObservedAt: at,
	}
}

func TestEmployeeOneShotReadNoneAndDirectOwnerIsolation(t *testing.T) {
	db, commercial, due := employeeOneShotFixture(t)
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	status, err := commercial.ReadEmployeeOneShotRenewalTx(ctx, tx, "employee-one", "self-shot-owned", due.Add(-time.Hour))
	if err != nil || status.State != "none" || status.Revision != 0 || status.DueAt != nil || status.Reason != "" || status.TerminalAt != nil {
		t.Fatalf("none status=%+v err=%v", status, err)
	}
	for _, id := range []string{"self-shot-other", "self-shot-key", "self-shot-once", "self-shot-missing"} {
		if _, err := commercial.ReadEmployeeOneShotRenewalTx(ctx, tx, "employee-one", id, due.Add(-time.Hour)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign/nonmonthly %q err=%v", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeOneShotDisarmCommercialOffReplayAndNoLedger(t *testing.T) {
	db, commercial, due := employeeOneShotFixture(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return due.Add(-time.Hour) }
	if _, _, err := commercial.ArmOneShotRenewal(ctx, oneShotArmInput(t, "self-shot-owned", "self-shot-admin-arm", 1, due.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	input := employeeOneShotInput("self-shot-owned", "self-shot-employee-disarm", 1, due)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, tx, input); err != nil || found {
		t.Fatalf("fresh probe found=%v err=%v", found, err)
	}
	result, err := commercial.DisarmEmployeeOneShotRenewalTx(ctx, tx, input)
	if err != nil || result.Status.State != "disarmed" || result.Status.Revision != 2 || result.Status.TerminalAt == nil || !result.Status.TerminalAt.Equal(due) {
		t.Fatalf("provisional disarm=%+v err=%v", result, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var kind, employee string
	var admin sql.NullString
	if err := db.QueryRow(`SELECT actor_kind,actor_employee_id,actor_admin_id FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&kind, &employee, &admin); err != nil || kind != "employee" || employee != "employee-one" || admin.Valid {
		t.Fatalf("typed actor kind=%q employee=%q admin=%v err=%v", kind, employee, admin, err)
	}
	var ledgerOps, entries, disarmFacts int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM financial_operations WHERE operation_id=?),(SELECT COUNT(*) FROM financial_entries WHERE operation_id=?),(SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?)`, input.OperationID, input.OperationID, input.OperationID).Scan(&ledgerOps, &entries, &disarmFacts); err != nil || ledgerOps != 0 || entries != 0 || disarmFacts != 1 {
		t.Fatalf("counts ledger=%d entries=%d commercial=%d err=%v", ledgerOps, entries, disarmFacts, err)
	}
	input.ObservedAt = due.Add(24 * time.Hour)
	replayTx, _ := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	replayed, found, err := commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, replayTx, input)
	_ = replayTx.Rollback()
	if err != nil || !found || replayed.Status.State != "disarmed" || replayed.Status.TerminalAt == nil || !replayed.Status.TerminalAt.Equal(due) {
		t.Fatalf("replay=%+v found=%v err=%v", replayed, found, err)
	}
	changed := input
	changed.ExpectedRevision = 2
	conflictTx, _ := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	_, _, err = commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, conflictTx, changed)
	_ = conflictTx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed revision err=%v", err)
	}
	changed = input
	changed.Actor.ID = "employee-two"
	conflictTx, _ = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	_, _, err = commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, conflictTx, changed)
	_ = conflictTx.Rollback()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("changed actor err=%v", err)
	}
	if _, _, err := commercial.ArmOneShotRenewal(ctx, oneShotArmInput(t, "self-shot-owned", "self-shot-rearm", 1, due)); !errors.Is(err, ErrConflict) {
		t.Fatalf("rearm err=%v", err)
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("typed employee disarm restart validation=%v", err)
	}
}

func TestEmployeeOneShotDisarmGlobalCollisionAndBrokenReceipt(t *testing.T) {
	db, commercial, due := employeeOneShotFixture(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return due.Add(-time.Hour) }
	if _, _, err := commercial.ArmOneShotRenewal(ctx, oneShotArmInput(t, "self-shot-owned", "self-shot-collision-arm", 1, due.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	input := employeeOneShotInput("self-shot-owned", "self-shot-collision-arm", 1, due)
	tx, _ := db.BeginTx(ctx, nil)
	if _, _, err := commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, tx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("arm ID collision=%v", err)
	}
	if _, err := commercial.DisarmEmployeeOneShotRenewalTx(ctx, tx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct primitive global collision=%v", err)
	}
	_ = tx.Rollback()
	input.OperationID = "self-shot-broken-receipt"
	tx, _ = db.BeginTx(ctx, nil)
	if _, err := commercial.DisarmEmployeeOneShotRenewalTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_subscription_one_shot_renewals SET terminal_at='2026-11-01T08:00:01Z',updated_at='2026-11-01T08:00:01Z' WHERE predecessor_id='self-shot-owned'`); err != nil {
		t.Fatal(err)
	}
	replayTx, _ := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	_, _, err := commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, replayTx, input)
	_ = replayTx.Rollback()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("matching metadata with broken chain=%v", err)
	}
}

func TestEmployeeOneShotDisarmAndDueWorkerFirstCommitWins(t *testing.T) {
	for _, employeeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "employee-first", false: "worker-first"}[employeeFirst], func(t *testing.T) {
			db, commercial, due := employeeOneShotFixture(t)
			defer db.Close()
			ctx := context.Background()
			if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			commercial.now = func() time.Time { return due.Add(-time.Hour) }
			if _, _, err := commercial.ArmOneShotRenewal(ctx, oneShotArmInput(t, "self-shot-owned", "self-shot-race-arm", 1, due.Add(-time.Hour))); err != nil {
				t.Fatal(err)
			}
			commercial.now = func() time.Time { return due }
			input := employeeOneShotInput("self-shot-owned", "self-shot-race-disarm", 1, due)
			writeDisarm := func() error {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if _, err := commercial.DisarmEmployeeOneShotRenewalTx(ctx, tx, input); err != nil {
					return err
				}
				return tx.Commit()
			}
			if employeeFirst {
				if err := writeDisarm(); err != nil {
					t.Fatal(err)
				}
				if count, err := commercial.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 0 {
					t.Fatalf("worker after disarm count=%d err=%v", count, err)
				}
			} else {
				if count, err := commercial.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 1 {
					t.Fatalf("worker first count=%d err=%v", count, err)
				}
				if err := writeDisarm(); !errors.Is(err, ErrConflict) {
					t.Fatalf("late employee disarm err=%v", err)
				}
			}
			var disarmCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&disarmCount); err != nil || (employeeFirst && disarmCount != 1) || (!employeeFirst && disarmCount != 0) {
				t.Fatalf("employee disarm facts=%d err=%v", disarmCount, err)
			}
			if err := NewCommercial(db).Migrate(ctx); err != nil {
				t.Fatalf("restart after first-commit race: %v", err)
			}
		})
	}
}
