package financial

// Independently authored for docs/subscription-manual-renewal-contract.md.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func renewalFixture(t *testing.T) (*Commercial, *Ledger, time.Time, string) {
	t.Helper()
	db, ledger, commercial, accountID, planID := subscriptionTestFixture(t)
	t.Cleanup(func() { db.Close() })
	start := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	end, err := monthlyPeriodEnd(start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('renew-old',?,?,?,?,?,?,?,?,?,?,1)`, accountID, planID, 1, 10, 20, "USD", "monthly", "active", formatCommercialTime(start), end.Format(subscriptionEndLayout)); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return end }
	return commercial, ledger, end, planID
}

func renewalInput(t *testing.T, id, operation string, at time.Time) RenewSubscription {
	t.Helper()
	return RenewSubscription{Meta: testCommercialMeta(t, operation, struct{ ID string }{id}, at), ID: id}
}

func TestManualRenewalAtomicSnapshotLinksAndExactRetry(t *testing.T) {
	commercial, ledger, due, planID := renewalFixture(t)
	ctx := context.Background()
	input := renewalInput(t, "renew-old", "renew-once", due.Add(-time.Hour))
	if _, _, err := commercial.RenewSubscription(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("default-off renewal: %v", err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_plans SET price_micro=12,credit_micro=30,revision=2 WHERE id=?`, planID); err != nil {
		t.Fatal(err)
	}
	item, receipt, err := commercial.RenewSubscription(ctx, input)
	if err != nil || receipt.Replay || item.PredecessorID != "renew-old" || item.PlanRevision != 2 || item.PriceMicro != 12 || item.CreditMicro != 30 || !item.StartedAt.Equal(due) || item.PeriodEndAt == nil || !item.PeriodEndAt.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("renewal item=%+v receipt=%+v err=%v", item, receipt, err)
	}
	old, err := commercial.GetSubscription(ctx, "renew-old")
	if err != nil || old.Status != "expired" || old.Revision != 2 || old.SuccessorID != item.ID || old.PlanRevision != 1 || old.PriceMicro != 10 || old.CreditMicro != 20 {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return due.Add(24 * time.Hour) }
	replayed, retryReceipt, err := commercial.RenewSubscription(ctx, input)
	if err != nil || !retryReceipt.Replay || replayed.ID != item.ID || !replayed.StartedAt.Equal(due) {
		t.Fatalf("retry=%+v receipt=%+v err=%v", replayed, retryReceipt, err)
	}
	changed := input
	changed.Meta.PayloadDigest[0] ^= 1
	if _, _, err := commercial.RenewSubscription(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry=%v", err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.RenewSubscription(ctx, renewalInput(t, "renew-old", "renew-other", due)); !errors.Is(err, ErrConflict) {
		t.Fatalf("branch renewal=%v", err)
	}
	var entries, links int
	if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, input.Meta.OperationID).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("entries=%d err=%v", entries, err)
	}
	if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals WHERE predecessor_id='renew-old'`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 1018 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if err := NewCommercial(commercial.db).Migrate(ctx); err != nil {
		t.Fatalf("restart migration=%v", err)
	}
}

func TestManualRenewalDueBoundaryIsolationAndRollback(t *testing.T) {
	commercial, _, due, planID := renewalFixture(t)
	ctx := context.Background()
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	input := renewalInput(t, "renew-old", "renew-boundary", due)
	commercial.now = func() time.Time { return due.Add(-time.Nanosecond) }
	if _, _, err := commercial.RenewSubscription(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("before end=%v", err)
	}
	commercial.now = func() time.Time { return due }
	if _, err := commercial.db.Exec(`UPDATE financial_plans SET price_micro=2000 WHERE id=?`, planID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.RenewSubscription(ctx, input); !errors.Is(err, ErrInsufficient) {
		t.Fatalf("insufficient=%v", err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_plans SET price_micro=10 WHERE id=?`, planID); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`CREATE TRIGGER renewal_fail_entry BEFORE INSERT ON financial_entries WHEN NEW.kind='subscription_charge' BEGIN SELECT RAISE(ABORT,'synthetic renewal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.RenewSubscription(ctx, input); err == nil {
		t.Fatal("injected ledger failure succeeded")
	}
	var status string
	var revision, links, receipts int
	if err := commercial.db.QueryRow(`SELECT status,revision FROM financial_subscriptions WHERE id='renew-old'`).Scan(&status, &revision); err != nil || status != "active" || revision != 1 {
		t.Fatalf("rollback status=%s revision=%d err=%v", status, revision, err)
	}
	if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals`).Scan(&links); err != nil || links != 0 {
		t.Fatalf("rollback links=%d err=%v", links, err)
	}
	if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?`, input.Meta.OperationID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("rollback receipts=%d err=%v", receipts, err)
	}
	if _, err := commercial.db.Exec(`DROP TRIGGER renewal_fail_entry`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.RenewSubscription(ctx, input); err != nil {
		t.Fatalf("recovered retry=%v", err)
	}
}

func TestManualRenewalConcurrentDifferentOperationsOneSuccess(t *testing.T) {
	commercial, _, due, _ := renewalFixture(t)
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	inputs := []RenewSubscription{renewalInput(t, "renew-old", "renew-race-a", due), renewalInput(t, "renew-old", "renew-race-b", due)}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, input := range inputs {
		wg.Add(1)
		go func(input RenewSubscription) {
			defer wg.Done()
			_, _, err := commercial.RenewSubscription(context.Background(), input)
			results <- err
		}(input)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrUnavailable) {
			t.Fatalf("loser=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successes=%d", success)
	}
	var links int
	if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals`).Scan(&links); err != nil || links != 1 {
		t.Fatalf("links=%d err=%v", links, err)
	}
}

func TestManualRenewalMigratesExactOldOperationSchema(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{commercialOperationsLegacyDDL, commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('old-operation','plan.create','admin-one',zeroblob(32),'plan','old',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("migration=%v", err)
	}
	var action string
	if err := db.QueryRow(`SELECT action FROM financial_commercial_operations WHERE operation_id='old-operation'`).Scan(&action); err != nil || action != "plan.create" {
		t.Fatalf("preserved action=%s err=%v", action, err)
	}
	if _, err := db.Exec(`DELETE FROM financial_commercial_operations WHERE operation_id='old-operation'`); err == nil {
		t.Fatal("operation delete bypassed immutable trigger")
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("second migration=%v", err)
	}
}

func TestManualRenewalMigrationRollsBackMalformedLegacyFact(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{commercialOperationsLegacyDDL, commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('bad-operation','plan.create','admin-one',zeroblob(32),'plan','bad',1,'not-a-time')`); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); !errors.Is(err, ErrSchema) {
		t.Fatalf("malformed migration=%v", err)
	}
	var actual string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='financial_commercial_operations'`).Scan(&actual); err != nil || normalize(actual) != normalize(storedDDL(commercialOperationsLegacyDDL)) {
		t.Fatalf("old schema not restored: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='financial_subscription_renewals'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial renewal table count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM financial_commercial_operations WHERE operation_id='bad-operation'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(commercialOperationsNoDeleteDDL); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("repaired migration=%v", err)
	}
}

func TestManualRenewalUsesCurrentCurrencyAndDoesNotBackfillGap(t *testing.T) {
	commercial, ledger, due, planID := renewalFixture(t)
	ctx := context.Background()
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	if _, err := ledger.Post(ctx, Post{OperationID: "renew-eur-seed", Action: "adjustment", ResourceKind: "adjustment", ResourceID: "renew-eur-seed", ObservedAt: due, Entries: []EntryInput{{Owner: owner, Currency: "EUR", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "renew-eur-seed"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_plans SET currency='EUR',price_micro=11,credit_micro=21,revision=2 WHERE id=?`, planID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2027, 1, 31, 8, 0, 0, 0, time.UTC)
	commercial.now = func() time.Time { return now }
	item, _, err := commercial.RenewSubscription(ctx, renewalInput(t, "renew-old", "renew-eur", now))
	if err != nil || item.Currency != "EUR" || item.PlanRevision != 2 || item.PeriodEndAt == nil || !item.PeriodEndAt.Equal(time.Date(2027, 2, 28, 8, 0, 0, 0, time.UTC)) || !item.StartedAt.Equal(now) {
		t.Fatalf("new currency and month-end item=%+v err=%v", item, err)
	}
	old, err := commercial.GetSubscription(ctx, "renew-old")
	if err != nil || old.Currency != "USD" || old.PriceMicro != 10 || old.PeriodEndAt == nil || !old.PeriodEndAt.Equal(due) {
		t.Fatalf("old snapshot=%+v err=%v", old, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "EUR"); err != nil || balance.AmountMicro != 110 {
		t.Fatalf("EUR balance=%+v err=%v", balance, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 1000 {
		t.Fatalf("USD balance=%+v err=%v", balance, err)
	}
}

func TestManualRenewalRejectsDisabledPlanCancelledAndOneTime(t *testing.T) {
	for _, mode := range []string{"disabled-plan", "cancelled", "one-time"} {
		t.Run(mode, func(t *testing.T) {
			commercial, _, due, planID := renewalFixture(t)
			if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "disabled-plan":
				if _, err := commercial.db.Exec(`UPDATE financial_plans SET enabled=0 WHERE id=?`, planID); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if _, err := commercial.db.Exec(`UPDATE financial_subscriptions SET status='cancelled',cancelled_at=?,revision=2 WHERE id='renew-old'`, formatCommercialTime(due.Add(-time.Second))); err != nil {
					t.Fatal(err)
				}
			case "one-time":
				if _, err := commercial.db.Exec(`UPDATE financial_subscriptions SET interval='one_time',period_end_at=NULL WHERE id='renew-old'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := commercial.RenewSubscription(context.Background(), renewalInput(t, "renew-old", "renew-reject-"+mode, due)); !errors.Is(err, ErrConflict) {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			var links int
			if err := commercial.db.QueryRow(`SELECT COUNT(*) FROM financial_subscription_renewals`).Scan(&links); err != nil || links != 0 {
				t.Fatalf("mode=%s links=%d err=%v", mode, links, err)
			}
		})
	}
}

func TestManualRenewalCorruptLinkFailsClosed(t *testing.T) {
	commercial, _, due, _ := renewalFixture(t)
	if _, err := commercial.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.RenewSubscription(context.Background(), renewalInput(t, "renew-old", "renew-corrupt", due)); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`DROP TRIGGER financial_subscription_renewals_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.db.Exec(`UPDATE financial_subscription_renewals SET created_at='not-a-time' WHERE predecessor_id='renew-old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.GetSubscription(context.Background(), "renew-old"); !errors.Is(err, ErrSchema) {
		t.Fatalf("detail corrupt link=%v", err)
	}
	if _, err := commercial.ListSubscriptions(context.Background(), "", 20); !errors.Is(err, ErrSchema) {
		t.Fatalf("list corrupt link=%v", err)
	}
}
