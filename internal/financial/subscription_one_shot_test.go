package financial

// Independently authored for docs/subscription-one-shot-renewal-contract.md.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func oneShotArmInput(t *testing.T, id, operation string, revision int64, at time.Time) ArmOneShotRenewal {
	t.Helper()
	return ArmOneShotRenewal{Meta: testCommercialMeta(t, operation, struct {
		ID       string
		Expected int64
	}{id, revision}, at), PredecessorID: id, ExpectedRevision: revision}
}

func oneShotDisarmInput(t *testing.T, id, operation string, revision int64, at time.Time) DisarmOneShotRenewal {
	t.Helper()
	return DisarmOneShotRenewal{Meta: testCommercialMeta(t, operation, struct {
		ID       string
		Expected int64
	}{id, revision}, at), PredecessorID: id, ExpectedRevision: revision}
}

func TestOneShotArmDisarmFinalAndSuccessorIndependent(t *testing.T) {
	c, _, due, _ := renewalFixture(t)
	ctx := context.Background()
	c.now = func() time.Time { return due.Add(-time.Second) }
	arm := oneShotArmInput(t, "renew-old", "one-shot-arm", 1, due.Add(-time.Second))
	if _, _, err := c.ArmOneShotRenewal(ctx, arm); !errors.Is(err, ErrConflict) {
		t.Fatalf("default-off arm=%v", err)
	}
	if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	status, receipt, err := c.ArmOneShotRenewal(ctx, arm)
	if err != nil || status.State != "armed" || status.Revision != 1 || receipt.Replay {
		t.Fatalf("arm=%+v receipt=%+v err=%v", status, receipt, err)
	}
	status, receipt, err = c.ArmOneShotRenewal(ctx, arm)
	if err != nil || status.State != "armed" || !receipt.Replay {
		t.Fatalf("arm retry=%+v receipt=%+v err=%v", status, receipt, err)
	}
	changed := arm
	changed.Meta.PayloadDigest[0] ^= 1
	if _, _, err := c.ArmOneShotRenewal(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed arm=%v", err)
	}
	disarm := oneShotDisarmInput(t, "renew-old", "one-shot-disarm", 1, due)
	status, receipt, err = c.DisarmOneShotRenewal(ctx, disarm)
	if err != nil || status.State != "disarmed" || status.Revision != 2 || receipt.Replay {
		t.Fatalf("disarm=%+v receipt=%+v err=%v", status, receipt, err)
	}
	if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-rearm", 1, due)); !errors.Is(err, ErrConflict) {
		t.Fatalf("rearm after terminal disarm=%v", err)
	}
	c.now = func() time.Time { return due }
	if count, err := c.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 0 {
		t.Fatalf("disarmed pass count=%d err=%v", count, err)
	}
	successor, _, err := c.RenewSubscription(ctx, renewalInput(t, "renew-old", "manual-after-disarm", due))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := c.OneShotRenewal(ctx, "renew-old"); err != nil || state.State != "disarmed" {
		t.Fatalf("old reservation=%+v err=%v", state, err)
	}
	if state, err := c.OneShotRenewal(ctx, successor.ID); err != nil || state.State != "none" {
		t.Fatalf("successor inherited reservation=%+v err=%v", state, err)
	}
	if state, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, successor.ID, "one-shot-successor-arm", 1, due)); err != nil || state.State != "armed" {
		t.Fatalf("successor may be independently armed=%+v err=%v", state, err)
	}
}

func TestOneShotDueCurrentPlanAtomicAndRestart(t *testing.T) {
	c, ledger, due, planID := renewalFixture(t)
	ctx := context.Background()
	if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due.Add(-time.Second) }
	if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-execute-arm", 1, due)); err != nil {
		t.Fatal(err)
	}
	if count, err := c.ProcessDueOneShotRenewals(ctx, due.Add(-time.Nanosecond)); err != nil || count != 0 {
		t.Fatalf("early count=%d err=%v", count, err)
	}
	if _, err := c.db.Exec(`UPDATE financial_plans SET price_micro=13,credit_micro=29,revision=2 WHERE id=?`, planID); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due }
	count, err := c.ProcessDueOneShotRenewals(ctx, due)
	if err != nil || count != 1 {
		t.Fatalf("due count=%d err=%v", count, err)
	}
	status, err := c.OneShotRenewal(ctx, "renew-old")
	if err != nil || status.State != "succeeded" || status.SuccessorID == "" || status.Revision != 2 {
		t.Fatalf("result=%+v err=%v", status, err)
	}
	successor, err := c.GetSubscription(ctx, status.SuccessorID)
	if err != nil || successor.PriceMicro != 13 || successor.CreditMicro != 29 || successor.PlanRevision != 2 || !successor.StartedAt.Equal(due) || successor.PeriodEndAt == nil || !successor.PeriodEndAt.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
	var operationID string
	if err := c.db.QueryRow(`SELECT execution_operation_id FROM financial_subscription_one_shot_renewals WHERE predecessor_id='renew-old'`).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, operationID).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("money entries=%d err=%v", entries, err)
	}
	for _, table := range []string{"financial_commercial_operations", "financial_operations"} {
		var kind, actorID string
		var adminID, employeeID any
		if err := c.db.QueryRow(`SELECT actor_kind,actor_admin_id,actor_employee_id,actor_system_id FROM `+table+` WHERE operation_id=?`, operationID).Scan(&kind, &adminID, &employeeID, &actorID); err != nil || kind != "system" || actorID != "subscription_one_shot_worker" || adminID != nil || employeeID != nil {
			t.Fatalf("worker %s actor kind=%q id=%q admin=%v employee=%v err=%v", table, kind, actorID, adminID, employeeID, err)
		}
	}
	if count, err := c.ProcessDueOneShotRenewals(ctx, due.Add(time.Hour)); err != nil || count != 0 {
		t.Fatalf("replay count=%d err=%v", count, err)
	}
	if balance, err := ledger.Balance(ctx, Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "USD"); err != nil || balance.AmountMicro != 1016 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if err := NewCommercial(c.db).Migrate(ctx); err != nil {
		t.Fatalf("restart migration=%v", err)
	}
}

func TestOneShotBusinessFailuresTerminalWithoutLaterRetry(t *testing.T) {
	for _, mode := range []string{"commercial_disabled", "plan_unavailable", "insufficient_balance"} {
		t.Run(mode, func(t *testing.T) {
			c, _, due, planID := renewalFixture(t)
			ctx := context.Background()
			if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			c.now = func() time.Time { return due.Add(-time.Second) }
			if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-fail-arm", 1, due)); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "commercial_disabled":
				_, err := c.db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`)
				if err != nil {
					t.Fatal(err)
				}
			case "plan_unavailable":
				_, err := c.db.Exec(`UPDATE financial_plans SET enabled=0 WHERE id=?`, planID)
				if err != nil {
					t.Fatal(err)
				}
			case "insufficient_balance":
				_, err := c.db.Exec(`UPDATE financial_plans SET price_micro=2000 WHERE id=?`, planID)
				if err != nil {
					t.Fatal(err)
				}
			}
			c.now = func() time.Time { return due }
			if count, err := c.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 1 {
				t.Fatalf("failure pass count=%d err=%v", count, err)
			}
			status, err := c.OneShotRenewal(ctx, "renew-old")
			if err != nil || status.State != "failed" || status.Reason != mode {
				t.Fatalf("terminal=%+v err=%v", status, err)
			}
			if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			if _, err := c.db.Exec(`UPDATE financial_plans SET enabled=1,price_micro=10 WHERE id=?`, planID); err != nil {
				t.Fatal(err)
			}
			if count, err := c.ProcessDueOneShotRenewals(ctx, due.Add(time.Hour)); err != nil || count != 0 {
				t.Fatalf("surprise retry count=%d err=%v", count, err)
			}
			var links, entries int
			if err := c.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals WHERE predecessor_id='renew-old'`).Scan(&links); err != nil {
				t.Fatal(err)
			}
			if err := c.db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=(SELECT execution_operation_id FROM financial_subscription_one_shot_renewals WHERE predecessor_id='renew-old')`).Scan(&entries); err != nil {
				t.Fatal(err)
			}
			if links != 0 || entries != 0 {
				t.Fatalf("links=%d entries=%d", links, entries)
			}
		})
	}
}

func TestOneShotManualAndCancelWinWithoutScheduledMoney(t *testing.T) {
	for _, mode := range []string{"manual", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			c, _, due, _ := renewalFixture(t)
			ctx := context.Background()
			if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			c.now = func() time.Time { return due.Add(-time.Second) }
			if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-compete-arm", 1, due)); err != nil {
				t.Fatal(err)
			}
			if mode == "manual" {
				c.now = func() time.Time { return due }
				if _, _, err := c.RenewSubscription(ctx, renewalInput(t, "renew-old", "one-shot-compete-manual", due)); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, _, err := c.CancelSubscription(ctx, CancelSubscription{Meta: testCommercialMeta(t, "one-shot-compete-cancel", struct {
					ID       string
					Expected int64
				}{"renew-old", 1}, due.Add(-time.Second)), ID: "renew-old", ExpectedRevision: 1}); err != nil {
					t.Fatal(err)
				}
				c.now = func() time.Time { return due }
			}
			status, err := c.OneShotRenewal(ctx, "renew-old")
			want := "superseded"
			if mode == "cancel" {
				want = "cancelled"
			}
			if err != nil || status.State != want {
				t.Fatalf("status=%+v err=%v", status, err)
			}
			if count, err := c.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 0 {
				t.Fatalf("worker count=%d err=%v", count, err)
			}
		})
	}
}

func TestOneShotConcurrentWorkersAtMostOneSuccess(t *testing.T) {
	c, _, due, _ := renewalFixture(t)
	ctx := context.Background()
	if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due.Add(-time.Second) }
	if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-race-arm", 1, due)); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due }
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.ProcessDueOneShotRenewals(ctx, due) }()
	}
	wg.Wait()
	if _, err := c.ProcessDueOneShotRenewals(ctx, due); err != nil {
		t.Fatal(err)
	}
	status, err := c.OneShotRenewal(ctx, "renew-old")
	if err != nil || status.State != "succeeded" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	var links int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals WHERE predecessor_id='renew-old'`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestOneShotMixedGenerationMigrationFailsClosed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		name := "valid"
		if malformed {
			name = "malformed_link_rollback"
		}
		t.Run(name, func(t *testing.T) {
			c, _, due, _ := renewalFixture(t)
			ctx := context.Background()
			if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			successor, _, err := c.RenewSubscription(ctx, renewalInput(t, "renew-old", "one-shot-prior-manual", due))
			if err != nil {
				t.Fatal(err)
			}
			// Reconstruct the exact immediately preceding DDL around the existing
			// link. The production migrator must keep foreign keys enabled.
			tx, err := c.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, statement := range []string{
				`CREATE TEMP TABLE prior_ops AS SELECT operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations`,
				`CREATE TEMP TABLE prior_links AS SELECT * FROM financial_subscription_renewals`,
				`DROP TABLE financial_subscription_one_shot_renewals`,
				`DROP TABLE financial_subscription_renewals`,
				`DROP TABLE financial_commercial_operations`,
				commercialOperationsBeforeOneShotDDL,
				`INSERT INTO financial_commercial_operations SELECT * FROM prior_ops`,
				commercialOperationsNoUpdateDDL,
				commercialOperationsNoDeleteDDL,
				subscriptionRenewalsDDL,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					t.Fatalf("construct prior schema: %v", err)
				}
			}
			if malformed {
				if _, err := tx.ExecContext(ctx, `UPDATE prior_links SET created_at='not-a-time'`); err != nil {
					t.Fatal(err)
				}
			}
			for _, statement := range []string{
				`INSERT INTO financial_subscription_renewals SELECT * FROM prior_links`,
				subscriptionRenewalsNoUpdateDDL,
				subscriptionRenewalsNoDeleteDDL,
				`DROP TABLE prior_links`,
				`DROP TABLE prior_ops`,
			} {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					t.Fatalf("construct prior links: %v", err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			err = NewCommercial(c.db).Migrate(ctx)
			if !errors.Is(err, ErrSchema) {
				t.Fatalf("L2/C2 mixed generation accepted: %v", err)
			}
			var schema string
			if err := c.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='financial_commercial_operations'`).Scan(&schema); err != nil || normalize(schema) != normalize(storedDDL(commercialOperationsBeforeOneShotDDL)) {
				t.Fatalf("migration changed old schema on failure: %v", err)
			}
			var reservations int
			if err := c.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='financial_subscription_one_shot_renewals'`).Scan(&reservations); err != nil || reservations != 0 {
				t.Fatalf("partial reservation table count=%d err=%v", reservations, err)
			}
			var linkedSuccessor string
			if err := c.db.QueryRow(`SELECT successor_id FROM financial_subscription_renewals WHERE predecessor_id='renew-old'`).Scan(&linkedSuccessor); err != nil || linkedSuccessor != successor.ID {
				t.Fatalf("failed migration changed successor=%q err=%v", linkedSuccessor, err)
			}
		})
	}
}

func TestOneShotOrphanArmFactFailsClosed(t *testing.T) {
	c, _, due, _ := renewalFixture(t)
	input := oneShotArmInput(t, "renew-old", "one-shot-orphan", 1, due.Add(-time.Second))
	if _, err := c.db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_kind,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES(?,'subscription.one_shot.arm','admin',?,?,'subscription_one_shot','renew-old',1,?)`, input.Meta.OperationID, input.Meta.ActorAdminID, input.Meta.PayloadDigest[:], formatCommercialTime(due.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.OneShotRenewal(context.Background(), "renew-old"); !errors.Is(err, ErrSchema) {
		t.Fatalf("orphan arm fact was shown as armable: %v", err)
	}
	if err := NewCommercial(c.db).Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("orphan arm fact survived migration: %v", err)
	}
}

func TestOneShotWorkerReadFailureIsUnavailable(t *testing.T) {
	c, _, due, _ := renewalFixture(t)
	ctx := context.Background()
	if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due.Add(-time.Second) }
	if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "one-shot-read-failure-arm", 1, due)); err != nil {
		t.Fatal(err)
	}
	// A missing durable table causes QueryRow.Scan to fail with a storage
	// error, not sql.ErrNoRows. It must not look like a skipped candidate.
	if _, err := c.db.Exec(`DROP TABLE financial_subscription_one_shot_renewals`); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due }
	changed, err := c.processOneShotRenewal(ctx, "renew-old")
	if changed || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("storage read was swallowed: changed=%v err=%v", changed, err)
	}
}
