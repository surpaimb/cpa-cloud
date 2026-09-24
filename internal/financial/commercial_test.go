package financial

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestCommercialBillingPaymentRedemptionSubscriptionAndRefund(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger, commercial := NewLedger(db), NewCommercial(db)
	ctx := context.Background()
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if enabled, revision, err := commercial.Settings(ctx); err != nil || enabled || revision != 1 {
		t.Fatalf("settings enabled=%v revision=%d err=%v", enabled, revision, err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	connector, _, err := commercial.CreateConnector(ctx, CreateConnector{Meta: testCommercialMeta(t, "connector-create", map[string]any{"name": "Synthetic"}, financialTestTime), Name: "Synthetic", SecretCiphertext: []byte("synthetic-ciphertext"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	disabledTopup := CreateTopUp{Meta: testCommercialMeta(t, "disabled-topup", map[string]any{"amount": "50"}, financialTestTime), Owner: owner, ConnectorID: connector.ID, Currency: "USD", AmountMicro: 50}
	if _, _, err := commercial.CreateTopUp(ctx, disabledTopup); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled topup err=%v", err)
	}
	settingsMeta := testCommercialMeta(t, "enable", map[string]any{"expected": 1, "enabled": true}, financialTestTime)
	if receipt, err := commercial.SetEnabled(ctx, settingsMeta, 1, true); err != nil || receipt.Revision != 2 {
		t.Fatalf("enable receipt=%+v err=%v", receipt, err)
	}
	retryMeta := settingsMeta
	retryMeta.ObservedAt = financialTestTime.Add(time.Hour)
	if receipt, err := commercial.SetEnabled(ctx, retryMeta, 1, true); err != nil || !receipt.Replay {
		t.Fatalf("enable replay=%+v err=%v", receipt, err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "plan-create", map[string]any{"name": "Team", "price": "10", "credit": "100"}, financialTestTime.Add(time.Second)), Name: "Team", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 100, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	topup, receipt, err := commercial.CreateTopUp(ctx, CreateTopUp{Meta: testCommercialMeta(t, "topup-create", map[string]any{"amount": "50", "connector": connector.ID}, financialTestTime.Add(2*time.Second)), Owner: owner, ConnectorID: connector.ID, Currency: "USD", AmountMicro: 50})
	if err != nil || receipt.Replay || topup.Status != "pending" {
		t.Fatalf("topup=%+v receipt=%+v err=%v", topup, receipt, err)
	}
	payload := sha256.Sum256([]byte("synthetic-paid-event"))
	paidInput := ApplyPayment{ConnectorID: connector.ID, EventID: "event-one", PaymentID: topup.PaymentID, ExternalReference: topup.ExternalReference, Currency: "USD", AmountMicro: 50, PayloadDigest: payload, SignedAt: financialTestTime.Add(3 * time.Second), ObservedAt: financialTestTime.Add(3 * time.Second)}
	paid, err := commercial.ApplyPaid(ctx, paidInput)
	if err != nil || paid.Status != "paid" || paid.PaidEntryID == "" {
		t.Fatalf("paid=%+v err=%v", paid, err)
	}
	if replay, err := commercial.ApplyPaid(ctx, paidInput); err != nil || replay.PaidEntryID != paid.PaidEntryID {
		t.Fatalf("callback replay=%+v err=%v", replay, err)
	}
	changedEvent := paidInput
	changedEvent.PayloadDigest = sha256.Sum256([]byte("changed"))
	if _, err := commercial.ApplyPaid(ctx, changedEvent); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed callback err=%v", err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 50 {
		t.Fatalf("paid balance=%+v err=%v", balance, err)
	}
	refund, refundReceipt, err := commercial.RefundPayment(ctx, CreateRefund{Meta: testCommercialMeta(t, "refund-one", map[string]any{"payment": topup.PaymentID, "amount": "20"}, financialTestTime.Add(4*time.Second)), PaymentID: topup.PaymentID, AmountMicro: 20})
	if err != nil || refund.AmountMicro != 20 || refundReceipt.Replay {
		t.Fatalf("refund=%+v receipt=%+v err=%v", refund, refundReceipt, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 30 {
		t.Fatalf("refund balance=%+v err=%v", balance, err)
	}
	if _, _, err := commercial.RefundPayment(ctx, CreateRefund{Meta: testCommercialMeta(t, "refund-too-much", map[string]any{"payment": topup.PaymentID, "amount": "31"}, financialTestTime.Add(5*time.Second)), PaymentID: topup.PaymentID, AmountMicro: 31}); !errors.Is(err, ErrConflict) {
		t.Fatalf("over-refund err=%v", err)
	}
	codeDigest := sha256.Sum256([]byte("synthetic-redemption-code"))
	code, _, err := commercial.CreateCode(ctx, CreateRedemptionCode{Meta: testCommercialMeta(t, "code-create", map[string]any{"amount": "15"}, financialTestTime.Add(6*time.Second)), CodeDigest: codeDigest, Currency: "USD", AmountMicro: 15, MaxUses: 2})
	if err != nil || code.Uses != 0 {
		t.Fatalf("code=%+v err=%v", code, err)
	}
	entry, _, err := commercial.Redeem(ctx, RedeemCode{Meta: testCommercialMeta(t, "redeem-one", map[string]any{"code": code.ID}, financialTestTime.Add(7*time.Second)), CodeDigest: codeDigest, Owner: owner})
	if err != nil || entry.AmountMicro != 15 {
		t.Fatalf("redeem entry=%+v err=%v", entry, err)
	}
	if _, _, err := commercial.Redeem(ctx, RedeemCode{Meta: testCommercialMeta(t, "redeem-same-owner", map[string]any{"code": code.ID, "again": true}, financialTestTime.Add(8*time.Second)), CodeDigest: codeDigest, Owner: owner}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate owner redeem err=%v", err)
	}
	subscription, _, err := commercial.PurchaseSubscription(ctx, PurchaseSubscription{Meta: testCommercialMeta(t, "subscribe", map[string]any{"plan": plan.ID}, financialTestTime.Add(9*time.Second)), Owner: owner, PlanID: plan.ID})
	if err != nil || subscription.Status != "active" {
		t.Fatalf("subscription=%+v err=%v", subscription, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 135 {
		t.Fatalf("subscription balance=%+v err=%v", balance, err)
	}
	updatedPlan, _, err := commercial.UpdatePlan(ctx, UpdatePlan{Meta: testCommercialMeta(t, "plan-update", map[string]any{"id": plan.ID, "revision": 1}, financialTestTime.Add(10*time.Second)), ID: plan.ID, Name: "Team revised", Currency: "USD", Interval: "monthly", PriceMicro: 12, CreditMicro: 110, Enabled: false, ExpectedRevision: 1})
	if err != nil || updatedPlan.Revision != 2 || updatedPlan.Enabled {
		t.Fatalf("updated plan=%+v err=%v", updatedPlan, err)
	}
	updatedConnector, _, err := commercial.UpdateConnector(ctx, UpdateConnector{Meta: testCommercialMeta(t, "connector-update", map[string]any{"id": connector.ID, "revision": 1}, financialTestTime.Add(11*time.Second)), ID: connector.ID, Name: "Synthetic paused", Enabled: false, ExpectedRevision: 1})
	if err != nil || updatedConnector.Revision != 2 || updatedConnector.Enabled {
		t.Fatalf("updated connector=%+v err=%v", updatedConnector, err)
	}
	cancelled, _, err := commercial.CancelSubscription(ctx, CancelSubscription{Meta: testCommercialMeta(t, "subscription-cancel", map[string]any{"id": subscription.ID, "revision": 1}, financialTestTime.Add(12*time.Second)), ID: subscription.ID, ExpectedRevision: 1})
	if err != nil || cancelled.Status != "cancelled" || cancelled.Revision != 2 {
		t.Fatalf("cancelled subscription=%+v err=%v", cancelled, err)
	}
	if _, err := db.Exec(`UPDATE financial_webhook_events SET event_id='rewritten' WHERE event_id='event-one'`); err == nil {
		t.Fatal("immutable webhook event update succeeded")
	}
	if _, err := db.Exec(`DELETE FROM financial_refunds WHERE id=?`, refund.ID); err == nil {
		t.Fatal("immutable refund delete succeeded")
	}
}

func TestCommercialMigrationRejectsLookalikeWithoutPartialSchema(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE financial_settings(singleton INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); !errors.Is(err, ErrSchema) {
		t.Fatalf("lookalike migration err=%v", err)
	}
	for _, name := range []string{"financial_plans", "financial_payments", "financial_webhook_events"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial object %s count=%d err=%v", name, count, err)
		}
	}
}

func TestLedgerRefundCreditAndCumulativeLimit(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ledger := NewLedger(db)
	ctx := context.Background()
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	if _, err := ledger.Post(ctx, Post{OperationID: "seed-credit", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "seed", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "seed"}}}); err != nil {
		t.Fatal(err)
	}
	charge, err := ledger.Post(ctx, Post{OperationID: "usage", Action: "usage_charge", ResourceKind: "usage", ResourceID: "usage-one", ObservedAt: financialTestTime.Add(time.Second), RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryUsageCharge, AmountMicro: -20, ResourceKind: "usage", ResourceID: "usage-one"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Post(ctx, Post{OperationID: "refund-credit", Action: "refund", ActorAdminID: "admin-one", ResourceKind: "refund", ResourceID: "refund-one", ObservedAt: financialTestTime.Add(2 * time.Second), Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryRefund, AmountMicro: 10, OriginalEntryID: charge[0].ID, ResourceKind: "refund", ResourceID: "refund-one"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Post(ctx, Post{OperationID: "refund-over", Action: "refund", ActorAdminID: "admin-one", ResourceKind: "refund", ResourceID: "refund-two", ObservedAt: financialTestTime.Add(3 * time.Second), Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryRefund, AmountMicro: 11, OriginalEntryID: charge[0].ID, ResourceKind: "refund", ResourceID: "refund-two"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("over-refund err=%v", err)
	}
}

func testCommercialMeta(t *testing.T, operation string, payload any, at time.Time) WriteMeta {
	t.Helper()
	digest, err := DigestPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return WriteMeta{OperationID: operation, ActorAdminID: "admin-one", PayloadDigest: digest, ObservedAt: at.UTC()}
}
