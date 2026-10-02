package financial

// Independently authored transaction tests for
// docs/employee-purchase-transaction-primitive-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func employeePurchaseFixture(t *testing.T, funded int64) (*sql.DB, *Commercial, EmployeePurchaseInput) {
	t.Helper()
	db := openFinancialTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	ledger, commercial := NewLedger(db), NewCommercial(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "employee-plan-create", "one-time plan", financialTestTime), Name: "One time", Currency: "USD", Interval: "one_time", PriceMicro: 40, CreditMicro: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if funded > 0 {
		postEmployeeBalanceEntry(t, ledger, "employee-wallet-fund", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "USD", EntryAdjustmentCredit, funded, financialTestTime.Add(time.Second))
	}
	return db, commercial, EmployeePurchaseInput{
		OperationID: "employee-buy-one", Actor: Actor{Kind: ActorEmployee, ID: "employee-one"}, Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"},
		Expected:   ExpectedPurchasePlan{PlanID: plan.ID, Currency: "USD", Interval: "one_time", PriceMicro: 40, CreditMicro: 10, Revision: 1},
		ObservedAt: financialTestTime.Add(3 * time.Second),
	}
}

func enableEmployeePurchase(t *testing.T, c *Commercial) {
	t.Helper()
	if _, err := c.SetEnabled(context.Background(), testCommercialMeta(t, "employee-enable", "on", financialTestTime.Add(2*time.Second)), 1, true); err != nil {
		t.Fatal(err)
	}
}

func employeePurchaseCall(t *testing.T, db *sql.DB, c *Commercial, input EmployeePurchaseInput) (EmployeePurchaseResult, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, err := c.PurchaseEmployeeSubscriptionTx(ctx, tx, input)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmployeePurchaseResult{}, err
	}
	return result, nil
}

func countPurchaseRows(t *testing.T, db *sql.DB, operationID string) (commercial, ledger, entries int) {
	t.Helper()
	for _, item := range []struct {
		query string
		dest  *int
	}{
		{`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?`, &commercial},
		{`SELECT COUNT(*) FROM financial_operations WHERE operation_id=?`, &ledger},
		{`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, &entries},
	} {
		if err := db.QueryRow(item.query, operationID).Scan(item.dest); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestEmployeePurchaseCallerOwnedTransactionAndExactReplay(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	ctx := context.Background()
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("default-off purchase err=%v", err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("disabled purchase facts=%d/%d/%d", c, l, e)
	}
	enableEmployeePurchase(t, commercial)
	var beforeAccounts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&beforeAccounts); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	provisional, err := commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, input)
	if err != nil || provisional.Replay || provisional.CommercialReceipt.OperationID != input.OperationID || provisional.LedgerReceipt.OperationID != input.OperationID || provisional.LedgerReceipt.Charge.AmountMicro != -40 || provisional.LedgerReceipt.Credit.AmountMicro != 10 || provisional.Subscription.PeriodEndAt != nil {
		t.Fatalf("provisional=%+v err=%v", provisional, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("rolled-back facts=%d/%d/%d", c, l, e)
	}
	first, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 1 || l != 1 || e != 2 {
		t.Fatalf("committed facts=%d/%d/%d", c, l, e)
	}
	var actorKind, actorID string
	var version int
	if err := db.QueryRow(`SELECT actor_kind,actor_employee_id,digest_version FROM financial_operations WHERE operation_id=?`, input.OperationID).Scan(&actorKind, &actorID, &version); err != nil || actorKind != "employee" || actorID != "employee-one" || version != 2 {
		t.Fatalf("ledger actor=%q/%q version=%d err=%v", actorKind, actorID, version, err)
	}
	if err := db.QueryRow(`SELECT actor_kind,actor_employee_id FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&actorKind, &actorID); err != nil || actorKind != "employee" || actorID != "employee-one" {
		t.Fatalf("commercial actor=%q/%q err=%v", actorKind, actorID, err)
	}
	if balance, err := NewLedger(db).Balance(ctx, input.Owner, "USD"); err != nil || balance.AmountMicro != 70 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "employee-disable", "off", financialTestTime.Add(4*time.Second)), 2, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.UpdatePlan(ctx, UpdatePlan{Meta: testCommercialMeta(t, "employee-plan-update", "different", financialTestTime.Add(5*time.Second)), ID: input.Expected.PlanID, Name: "Changed", Currency: "USD", Interval: "monthly", PriceMicro: 90, CreditMicro: 20, Enabled: false, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	postEmployeeBalanceEntry(t, NewLedger(db), "employee-drain", input.Owner, "USD", EntryAdjustmentDebit, -70, financialTestTime.Add(6*time.Second))
	retry := input
	retry.ObservedAt = financialTestTime.Add(time.Hour)
	repeated, err := employeePurchaseCall(t, db, commercial, retry)
	if err != nil || !repeated.Replay || !repeated.CommercialReceipt.Replay || repeated.Subscription.ID != first.Subscription.ID || repeated.LedgerReceipt.Charge.ID != first.LedgerReceipt.Charge.ID || repeated.LedgerReceipt.Credit.ID != first.LedgerReceipt.Credit.ID {
		t.Fatalf("retry=%+v err=%v", repeated, err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 1 || l != 1 || e != 2 {
		t.Fatalf("replay duplicated facts=%d/%d/%d", c, l, e)
	}
	var afterAccounts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&afterAccounts); err != nil || afterAccounts != beforeAccounts {
		t.Fatalf("accounts before=%d after=%d err=%v", beforeAccounts, afterAccounts, err)
	}
	for _, changed := range []EmployeePurchaseInput{
		func() EmployeePurchaseInput { v := retry; v.Expected.PriceMicro++; return v }(),
		func() EmployeePurchaseInput { v := retry; v.Expected.CreditMicro++; return v }(),
		func() EmployeePurchaseInput { v := retry; v.Expected.Revision++; return v }(),
		func() EmployeePurchaseInput { v := retry; v.Expected.Currency = "EUR"; return v }(),
		func() EmployeePurchaseInput { v := retry; v.Expected.PlanID = "other-plan"; return v }(),
		func() EmployeePurchaseInput {
			v := retry
			v.Actor.ID = "employee-two"
			v.Owner.EmployeeID = "employee-two"
			return v
		}(),
		func() EmployeePurchaseInput { v := retry; v.Owner.EmployeeID = "employee-two"; return v }(),
		func() EmployeePurchaseInput { v := retry; v.Owner.Kind = OwnerKey; v.Owner.KeyID = "key-one"; return v }(),
		func() EmployeePurchaseInput {
			v := retry
			v.Actor.Kind = ActorAdmin
			v.Actor.ID = "admin-one"
			return v
		}(),
	} {
		if _, err := employeePurchaseCall(t, db, commercial, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed replay %+v err=%v", changed, err)
		}
	}
	otherID := retry
	otherID.OperationID = "employee-buy-new"
	if _, err := employeePurchaseCall(t, db, commercial, otherID); !errors.Is(err, ErrConflict) {
		t.Fatalf("new ID with switch off err=%v", err)
	}
}

func TestEmployeePurchaseRejectsOwnerWalletPlanAndOverflow(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	for _, changed := range []EmployeePurchaseInput{
		func() EmployeePurchaseInput { v := input; v.Actor.Kind = ActorAdmin; return v }(),
		func() EmployeePurchaseInput { v := input; v.Owner.Kind = OwnerKey; v.Owner.KeyID = "key-one"; return v }(),
		func() EmployeePurchaseInput {
			v := input
			v.Owner.Kind = OwnerResource
			v.Owner.ResourceKind = "response"
			v.Owner.ResourceID = "one"
			return v
		}(),
		func() EmployeePurchaseInput { v := input; v.Owner.EmployeeID = "employee-two"; return v }(),
		func() EmployeePurchaseInput { v := input; v.Expected.Interval = "monthly"; return v }(),
		func() EmployeePurchaseInput { v := input; v.Expected.PriceMicro = 0; return v }(),
		func() EmployeePurchaseInput { v := input; v.Expected.Currency = "usd"; return v }(),
	} {
		if _, err := employeePurchaseCall(t, db, commercial, changed); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid input %+v err=%v", changed, err)
		}
	}
	missingWallet := input
	missingWallet.Actor.ID, missingWallet.Owner.EmployeeID, missingWallet.OperationID = "employee-two", "employee-two", "missing-wallet"
	if _, err := employeePurchaseCall(t, db, commercial, missingWallet); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing direct wallet err=%v", err)
	}
	t.Run("key-or-resource-money-is-not-direct-wallet", func(t *testing.T) {
		db, commercial, input := employeePurchaseFixture(t, 0)
		enableEmployeePurchase(t, commercial)
		postEmployeeBalanceEntry(t, NewLedger(db), "key-only-funds", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "USD", EntryAdjustmentCredit, 1000, financialTestTime.Add(4*time.Second))
		postEmployeeBalanceEntry(t, NewLedger(db), "resource-only-funds", Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "resource-one"}, "USD", EntryAdjustmentCredit, 1000, financialTestTime.Add(5*time.Second))
		var accountsBefore, accountsAfter int
		if err := db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&accountsBefore); err != nil {
			t.Fatal(err)
		}
		if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrConflict) {
			t.Fatalf("key/resource-only funds err=%v", err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM financial_accounts`).Scan(&accountsAfter); err != nil || accountsAfter != accountsBefore {
			t.Fatalf("automatic account created before=%d after=%d err=%v", accountsBefore, accountsAfter, err)
		}
	})
	for _, change := range []func(*EmployeePurchaseInput){
		func(v *EmployeePurchaseInput) { v.Expected.PriceMicro++ },
		func(v *EmployeePurchaseInput) { v.Expected.CreditMicro++ },
		func(v *EmployeePurchaseInput) { v.Expected.Revision++ },
		func(v *EmployeePurchaseInput) { v.Expected.Currency = "EUR" },
		func(v *EmployeePurchaseInput) { v.Expected.PlanID = "missing" },
	} {
		candidate := input
		change(&candidate)
		if _, err := employeePurchaseCall(t, db, commercial, candidate); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale plan %+v err=%v", candidate.Expected, err)
		}
	}
	if _, _, err := commercial.UpdatePlan(context.Background(), UpdatePlan{Meta: testCommercialMeta(t, "employee-plan-monthly", "monthly", financialTestTime.Add(4*time.Second)), ID: input.Expected.PlanID, Name: "Monthly now", Currency: "USD", Interval: "monthly", PriceMicro: 40, CreditMicro: 10, Enabled: true, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("current monthly plan err=%v", err)
	}
	if _, _, err := commercial.UpdatePlan(context.Background(), UpdatePlan{Meta: testCommercialMeta(t, "employee-plan-disable", "disabled", financialTestTime.Add(5*time.Second)), ID: input.Expected.PlanID, Name: "One time", Currency: "USD", Interval: "one_time", PriceMicro: 40, CreditMicro: 10, Enabled: false, ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled plan err=%v", err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("rejected facts=%d/%d/%d", c, l, e)
	}

	t.Run("insufficient", func(t *testing.T) {
		db, commercial, input := employeePurchaseFixture(t, 39)
		enableEmployeePurchase(t, commercial)
		if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrInsufficient) {
			t.Fatalf("insufficient err=%v", err)
		}
	})
	t.Run("checked-overflow", func(t *testing.T) {
		db, commercial, input := employeePurchaseFixture(t, math.MaxInt64)
		enableEmployeePurchase(t, commercial)
		input.Expected.PriceMicro = 40
		input.Expected.CreditMicro = 10
		// The current plan's net delta is negative, so use a matching revised
		// plan whose net delta is positive and would overflow MaxInt64.
		if _, _, err := commercial.UpdatePlan(context.Background(), UpdatePlan{Meta: testCommercialMeta(t, "employee-overflow-plan", "overflow", financialTestTime.Add(4*time.Second)), ID: input.Expected.PlanID, Name: "Over", Currency: "USD", Interval: "one_time", PriceMicro: 1, CreditMicro: 2, Enabled: true, ExpectedRevision: 1}); err != nil {
			t.Fatal(err)
		}
		input.Expected.PriceMicro, input.Expected.CreditMicro, input.Expected.Revision = 1, 2, 2
		if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("overflow err=%v", err)
		}
		if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
			t.Fatalf("overflow partial facts=%d/%d/%d", c, l, e)
		}
	})
}

func TestEmployeePurchaseCancelBeforeCommitAndDistinctIDs(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := tx.Commit(); err == nil {
		t.Fatal("commit succeeded after cancellation")
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("cancelled facts=%d/%d/%d", c, l, e)
	}
	first, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.OperationID = "employee-buy-two"
	secondInput.ObservedAt = input.ObservedAt.Add(time.Minute)
	second, err := employeePurchaseCall(t, db, commercial, secondInput)
	if err != nil || second.Replay || second.Subscription.ID == first.Subscription.ID {
		t.Fatalf("distinct ID purchase=%+v err=%v", second, err)
	}
	if balance, err := NewLedger(db).Balance(context.Background(), input.Owner, "USD"); err != nil || balance.AmountMicro != 40 {
		t.Fatalf("two-purchase balance=%+v err=%v", balance, err)
	}
}

func TestEmployeePurchaseConcurrentSingleInstance(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 40)
	enableEmployeePurchase(t, commercial)
	db.SetMaxOpenConns(4)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err == nil {
				defer tx.Rollback()
				candidate := input
				if index == 1 {
					candidate.OperationID = "employee-concurrent-two"
				}
				_, err = commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, candidate)
				if err == nil {
					err = tx.Commit()
				}
			}
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInsufficient) && !errors.Is(err, ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected concurrent err=%v", err)
		}
	}
	if successes > 1 {
		t.Fatalf("overspent with %d successes", successes)
	}
	var subscriptionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_subscriptions`).Scan(&subscriptionCount); err != nil || subscriptionCount != successes {
		t.Fatalf("subscription count=%d successes=%d err=%v", subscriptionCount, successes, err)
	}
	if balance, err := NewLedger(db).Balance(context.Background(), input.Owner, "USD"); err != nil || balance.AmountMicro < 0 {
		t.Fatalf("concurrent balance=%+v err=%v", balance, err)
	}
}

func TestEmployeePurchaseReplayDetectsBrokenLedgerReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, drop, change, restore string
	}{
		{"changed credit", `DROP TRIGGER financial_entries_no_update`, `UPDATE financial_entries SET amount_micro=amount_micro+1 WHERE operation_id='employee-buy-one' AND kind='subscription_credit'`, entriesNoUpdateDDL},
		{"changed ledger digest", `DROP TRIGGER financial_operations_no_update`, `UPDATE financial_operations SET payload_digest=randomblob(32) WHERE operation_id='employee-buy-one'`, operationsNoUpdateDDL},
		{"changed commercial resource", `DROP TRIGGER financial_commercial_operations_no_update`, `UPDATE financial_commercial_operations SET resource_id='wrong-subscription' WHERE operation_id='employee-buy-one'`, commercialOperationsNoUpdateDDL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, commercial, input := employeePurchaseFixture(t, 100)
			enableEmployeePurchase(t, commercial)
			if _, err := employeePurchaseCall(t, db, commercial, input); err != nil {
				t.Fatal(err)
			}
			for _, sqlText := range []string{tc.drop, tc.change, tc.restore} {
				if _, err := db.Exec(sqlText); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrSchema) {
				t.Fatalf("corrupt dual receipt replay err=%v", err)
			}
		})
	}
}

func TestEmployeePurchaseLedgerFaultRollsBackWithCallerTransaction(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	commercial.employeePurchaseAfterLedgerPost = func() error { return errors.New("synthetic post-write failure") }
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("post-write fault err=%v", err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("post-write fault left facts=%d/%d/%d", c, l, e)
	}
	var subscriptions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_subscriptions`).Scan(&subscriptions); err != nil || subscriptions != 0 {
		t.Fatalf("post-write fault subscriptions=%d err=%v", subscriptions, err)
	}
	commercial.employeePurchaseAfterLedgerPost = nil
	if _, err := employeePurchaseCall(t, db, commercial, input); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestEmployeePurchaseConcurrentSameOperationID(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	db.SetMaxOpenConns(4)
	type outcome struct {
		result EmployeePurchaseResult
		err    error
	}
	results := make(chan outcome, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			var result EmployeePurchaseResult
			if err == nil {
				defer tx.Rollback()
				result, err = commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, input)
				if err == nil {
					err = tx.Commit()
				}
			}
			results <- outcome{result, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var successful, newPurchases int
	for item := range results {
		if item.err == nil {
			successful++
			if !item.result.Replay {
				newPurchases++
			}
		} else if !errors.Is(item.err, ErrUnavailable) && !errors.Is(item.err, context.DeadlineExceeded) {
			t.Fatalf("same-ID concurrency err=%v", item.err)
		}
	}
	if successful < 1 || newPurchases != 1 {
		t.Fatalf("same-ID outcomes success=%d new=%d", successful, newPurchases)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 1 || l != 1 || e != 2 {
		t.Fatalf("same-ID facts=%d/%d/%d", c, l, e)
	}
	if replay, err := employeePurchaseCall(t, db, commercial, input); err != nil || !replay.Replay {
		t.Fatalf("same-ID recovery replay=%+v err=%v", replay, err)
	}
}

func TestEmployeePurchaseAfterL1RestartPreservesOldFacts(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	installLegacyLedger(t, db)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	old := Post{OperationID: "before-actor-credit", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "before-actor-credit", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "before-actor-credit"}}}
	digest, err := postDigest(old)
	if err != nil {
		t.Fatal(err)
	}
	at := formatCommercialTime(financialTestTime)
	if _, err := db.Exec(`INSERT INTO financial_accounts(id,owner_kind,owner_key,employee_id,resource_kind,resource_id,currency,created_at) VALUES('old-employee-wallet','employee',?,'employee-one','','','USD',?)`, ownerKey(owner), at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) VALUES('before-actor-credit','adjustment','admin-one','adjustment','before-actor-credit',?,?)`, digest[:], at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) VALUES('old-credit-entry','before-actor-credit','old-employee-wallet','adjustment_credit',100,'adjustment','before-actor-credit',?)`, at); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatalf("L1 ledger migration: %v", err)
	}
	commercial := NewCommercial(db)
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatalf("commercial initialization: %v", err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "old-db-plan", "plan", financialTestTime.Add(time.Second)), Name: "One time", Currency: "USD", Interval: "one_time", PriceMicro: 40, CreditMicro: 10, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "old-db-enable", "on", financialTestTime.Add(2*time.Second)), 1, true); err != nil {
		t.Fatal(err)
	}
	input := EmployeePurchaseInput{OperationID: "after-actor-buy", Actor: Actor{Kind: ActorEmployee, ID: "employee-one"}, Owner: owner, Expected: ExpectedPurchasePlan{PlanID: plan.ID, Currency: "USD", Interval: "one_time", PriceMicro: 40, CreditMicro: 10, Revision: 1}, ObservedAt: financialTestTime.Add(3 * time.Second)}
	first, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatalf("ledger restart: %v", err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatalf("commercial restart: %v", err)
	}
	var priorDigest []byte
	var priorVersion int
	if err := db.QueryRow(`SELECT payload_digest,digest_version FROM financial_operations WHERE operation_id='before-actor-credit'`).Scan(&priorDigest, &priorVersion); err != nil || priorVersion != 1 || !equalBytes(priorDigest, digest[:]) {
		t.Fatalf("old digest changed version=%d err=%v", priorVersion, err)
	}
	if again, err := employeePurchaseCall(t, db, commercial, input); err != nil || !again.Replay || again.Subscription.ID != first.Subscription.ID {
		t.Fatalf("after restart replay=%+v err=%v", again, err)
	}
}

func TestEmployeePurchaseAdminEntryRemainsSeparate(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	admin := PurchaseSubscription{Meta: testCommercialMeta(t, "admin-purchase-stays", "admin", input.ObservedAt), Owner: input.Owner, PlanID: input.Expected.PlanID}
	if _, receipt, err := commercial.PurchaseSubscription(context.Background(), admin); err != nil || receipt.Replay {
		t.Fatalf("admin purchase receipt=%+v err=%v", receipt, err)
	}
	admin.Meta.ActorAdminID = ""
	admin.Meta.Actor = Actor{Kind: ActorEmployee, ID: "employee-one"}
	if _, _, err := commercial.PurchaseSubscription(context.Background(), admin); !errors.Is(err, ErrInvalid) {
		t.Fatalf("employee bypassed admin entry err=%v", err)
	}
	if result, err := employeePurchaseCall(t, db, commercial, input); err != nil || result.Replay {
		t.Fatalf("employee primitive after admin purchase=%+v err=%v", result, err)
	}
}

func TestEmployeePurchaseCommittedThenCallerCancelledStillReplays(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	ctx, cancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	cancel() // A dropped response cannot undo a committed immutable purchase.
	retry, err := employeePurchaseCall(t, db, commercial, input)
	if err != nil || !retry.Replay || retry.Subscription.ID != first.Subscription.ID || retry.LedgerReceipt.Charge.ID != first.LedgerReceipt.Charge.ID {
		t.Fatalf("retry after response cancellation=%+v err=%v", retry, err)
	}
}

func TestEmployeePurchaseRejectsMalformedSelectedPlanRow(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_plans SET enabled=2 WHERE id=?`, input.Expected.PlanID); err != nil {
		t.Fatal(err)
	}
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrSchema) {
		t.Fatalf("malformed selected plan err=%v", err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("malformed plan left facts=%d/%d/%d", c, l, e)
	}
}

func TestEmployeePurchaseRejectsWeakenedSchemaBeforeWriting(t *testing.T) {
	db, commercial, input := employeePurchaseFixture(t, 100)
	enableEmployeePurchase(t, commercial)
	if _, err := db.Exec(`DROP TRIGGER financial_entries_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := employeePurchaseCall(t, db, commercial, input); !errors.Is(err, ErrSchema) {
		t.Fatalf("weakened schema err=%v", err)
	}
	if c, l, e := countPurchaseRows(t, db, input.OperationID); c != 0 || l != 0 || e != 0 {
		t.Fatalf("weakened schema left facts=%d/%d/%d", c, l, e)
	}
}
