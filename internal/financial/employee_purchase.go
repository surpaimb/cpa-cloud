package financial

// Independently authored for docs/employee-purchase-transaction-primitive-contract.md.
// This is an internal transaction primitive, not an employee HTTP purchase API.
// It uses the existing SQLite ledger and adds no third-party dependency.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type ExpectedPurchasePlan struct {
	PlanID      string `json:"plan_id"`
	Currency    string `json:"currency"`
	Interval    string `json:"interval"`
	PriceMicro  int64  `json:"price_micro"`
	CreditMicro int64  `json:"credit_micro"`
	Revision    int64  `json:"revision"`
}

type EmployeePurchaseInput struct {
	OperationID string
	Actor       Actor
	Owner       Owner
	Expected    ExpectedPurchasePlan
	ObservedAt  time.Time
}

type PurchaseLedgerReceipt struct {
	OperationID string
	Charge      Entry
	Credit      Entry
}

type EmployeePurchaseResult struct {
	Subscription      Subscription
	CommercialReceipt CommercialReceipt
	LedgerReceipt     PurchaseLedgerReceipt
	Replay            bool
}

// PurchaseEmployeeSubscriptionTx never begins, commits, or rolls back tx. A
// successful return is provisional until the caller successfully commits.
// The caller must roll back the whole transaction after every error.
func (c *Commercial) PurchaseEmployeeSubscriptionTx(ctx context.Context, tx *sql.Tx, input EmployeePurchaseInput) (EmployeePurchaseResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeePurchaseInput(input) {
		return EmployeePurchaseResult{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeePurchaseResult{}, ErrUnavailable
	}
	// Purchases are rare and must not proceed on a lookalike or weakened
	// financial schema, including a missing immutable-operation trigger.
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeePurchaseResult{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeePurchaseResult{}, err
	}
	digest, err := employeePurchaseDigest(input)
	if err != nil {
		return EmployeePurchaseResult{}, ErrInvalid
	}
	meta := WriteMeta{OperationID: input.OperationID, Actor: input.Actor, PayloadDigest: digest, ObservedAt: input.ObservedAt}

	// A committed exact retry must precede gates that only apply to a new buy.
	receipt, found, err := existingCommercialOperation(ctx, tx, meta, "subscription.create")
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	if found {
		result, err := replayEmployeePurchase(ctx, tx, input, receipt)
		if err != nil || ctx.Err() != nil {
			if err != nil {
				return EmployeePurchaseResult{}, err
			}
			return EmployeePurchaseResult{}, ErrUnavailable
		}
		return result, nil
	}
	// A changed actor or owner under a committed ID conflicts above. Only a
	// genuinely new operation may reach the employee-only purchase boundary.
	if input.Actor.Kind != ActorEmployee || input.Owner.Kind != OwnerEmployee || input.Owner.EmployeeID != input.Actor.ID {
		return EmployeePurchaseResult{}, ErrInvalid
	}
	// A ledger-only row cannot be completed by silently creating the missing
	// commercial receipt. It also makes global operation-ID collisions fail closed.
	if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
		return EmployeePurchaseResult{}, err
	} else if exists {
		return EmployeePurchaseResult{}, ErrConflict
	}
	if err := requireEmployeePurchaseEnabled(ctx, tx); err != nil {
		return EmployeePurchaseResult{}, err
	}
	plan, err := loadEmployeePurchasePlan(ctx, tx, input.Expected.PlanID)
	if errors.Is(err, ErrNotFound) {
		return EmployeePurchaseResult{}, ErrConflict
	}
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return EmployeePurchaseResult{}, ErrUnavailable
		}
		return EmployeePurchaseResult{}, ErrSchema
	}
	if !validCommercialText(plan.ID, 256) || !validCommercialText(plan.Name, 128) || !validCurrency(plan.Currency) || plan.Revision < 1 || plan.Revision > subscriptionRevisionMax || plan.PriceMicro < 1 || plan.CreditMicro < 1 || (plan.Interval != "one_time" && plan.Interval != "monthly") {
		return EmployeePurchaseResult{}, ErrSchema
	}
	if !plan.Enabled || plan.ID != input.Expected.PlanID || plan.Revision != input.Expected.Revision || plan.Currency != input.Expected.Currency || plan.PriceMicro != input.Expected.PriceMicro || plan.CreditMicro != input.Expected.CreditMicro || plan.Interval != input.Expected.Interval {
		return EmployeePurchaseResult{}, ErrConflict
	}
	accountID, err := employeePurchaseWallet(ctx, tx, input.Owner, plan.Currency)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	ledger := NewLedger(c.db)
	available, err := ledger.BalanceTx(ctx, tx, accountID)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	if available < plan.PriceMicro {
		return EmployeePurchaseResult{}, ErrInsufficient
	}
	id, err := randomID("subscription")
	if err != nil {
		return EmployeePurchaseResult{}, ErrUnavailable
	}
	post := employeePurchasePost(input, id)
	posted, err := ledger.PostTx(ctx, tx, post)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	if len(posted) != 2 || posted[0].AccountID != accountID || posted[1].AccountID != accountID {
		return EmployeePurchaseResult{}, ErrSchema
	}
	if c.employeePurchaseAfterLedgerPost != nil {
		if err := c.employeePurchaseAfterLedgerPost(); err != nil {
			return EmployeePurchaseResult{}, ErrUnavailable
		}
	}
	item := Subscription{ID: id, AccountID: accountID, PlanID: plan.ID, PlanRevision: plan.Revision, PriceMicro: plan.PriceMicro, CreditMicro: plan.CreditMicro, Currency: plan.Currency, Interval: plan.Interval, Status: "active", StartedAt: input.ObservedAt, Revision: 1}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,1)`, item.ID, item.AccountID, item.PlanID, item.PlanRevision, item.PriceMicro, item.CreditMicro, item.Currency, item.Interval, item.Status, formatCommercialTime(item.StartedAt)); err != nil {
		return EmployeePurchaseResult{}, ErrUnavailable
	}
	receipt = CommercialReceipt{OperationID: input.OperationID, ResourceKind: "subscription", ResourceID: id, Revision: 1, CreatedAt: input.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, meta, "subscription.create", receipt); err != nil {
		return EmployeePurchaseResult{}, err
	}
	if ctx.Err() != nil {
		return EmployeePurchaseResult{}, ErrUnavailable
	}
	return EmployeePurchaseResult{Subscription: item, CommercialReceipt: receipt, LedgerReceipt: PurchaseLedgerReceipt{OperationID: input.OperationID, Charge: posted[0], Credit: posted[1]}}, nil
}

func validEmployeePurchaseInput(input EmployeePurchaseInput) bool {
	actor, ok := effectiveActor(input.Actor, "")
	expected := input.Expected
	return ok && actor == input.Actor && validText(input.OperationID, 128) && validOwnerShape(input.Owner) && validText(expected.PlanID, 256) && validCurrency(expected.Currency) && expected.Interval == "one_time" && expected.PriceMicro > 0 && expected.CreditMicro > 0 && expected.Revision >= 1 && expected.Revision <= subscriptionRevisionMax && !input.ObservedAt.IsZero() && input.ObservedAt.Location() == time.UTC
}

func employeePurchaseDigest(input EmployeePurchaseInput) ([32]byte, error) {
	return DigestPayload(struct {
		Domain      string               `json:"domain"`
		OperationID string               `json:"operation_id"`
		Action      string               `json:"action"`
		Actor       Actor                `json:"actor"`
		Owner       Owner                `json:"owner"`
		Expected    ExpectedPurchasePlan `json:"expected"`
	}{"employee-purchase/v1", input.OperationID, "subscription.create", input.Actor, input.Owner, input.Expected})
}

func employeePurchasePost(input EmployeePurchaseInput, subscriptionID string) Post {
	return Post{OperationID: input.OperationID, Action: "subscription_purchase", Actor: input.Actor, ResourceKind: "subscription", ResourceID: subscriptionID, ObservedAt: input.ObservedAt, RequireNonNegative: true, Entries: []EntryInput{
		{Owner: input.Owner, Currency: input.Expected.Currency, Kind: EntrySubscriptionCharge, AmountMicro: -input.Expected.PriceMicro, ResourceKind: "subscription", ResourceID: subscriptionID},
		{Owner: input.Owner, Currency: input.Expected.Currency, Kind: EntrySubscriptionCredit, AmountMicro: input.Expected.CreditMicro, ResourceKind: "subscription", ResourceID: subscriptionID},
	}}
}

func requireEmployeePurchaseEnabled(ctx context.Context, tx *sql.Tx) error {
	var enabled, revision int64
	var enabledType, revisionType, updatedType, updated string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,revision,typeof(enabled),typeof(revision),typeof(updated_at),updated_at FROM financial_settings WHERE singleton=1`).Scan(&enabled, &revision, &enabledType, &revisionType, &updatedType, &updated); err != nil {
		return ErrUnavailable
	}
	if enabledType != "integer" || revisionType != "integer" || updatedType != "text" || (enabled != 0 && enabled != 1) || revision < 1 || revision > subscriptionRevisionMax || !validPurchaseStoredTime(updated) {
		return ErrSchema
	}
	if enabled == 0 {
		return ErrConflict
	}
	return nil
}

func loadEmployeePurchasePlan(ctx context.Context, tx *sql.Tx, id string) (Plan, error) {
	plan, err := loadPlan(ctx, tx, id)
	if err != nil {
		return Plan{}, err
	}
	var idType, nameType, currencyType, priceType, creditType, intervalType, enabledType, revisionType, createdType, updatedType, created, updated string
	var enabledValue int64
	err = tx.QueryRowContext(ctx, `SELECT typeof(id),typeof(name),typeof(currency),typeof(price_micro),typeof(credit_micro),typeof(interval),typeof(enabled),typeof(revision),typeof(created_at),typeof(updated_at),created_at,updated_at,enabled FROM financial_plans WHERE id=?`, id).Scan(&idType, &nameType, &currencyType, &priceType, &creditType, &intervalType, &enabledType, &revisionType, &createdType, &updatedType, &created, &updated, &enabledValue)
	if err != nil {
		return Plan{}, ErrUnavailable
	}
	if idType != "text" || nameType != "text" || currencyType != "text" || priceType != "integer" || creditType != "integer" || intervalType != "text" || enabledType != "integer" || revisionType != "integer" || createdType != "text" || updatedType != "text" || (enabledValue != 0 && enabledValue != 1) || !validPurchaseStoredTime(created) || !validPurchaseStoredTime(updated) || plan.UpdatedAt.Before(plan.CreatedAt) {
		return Plan{}, ErrSchema
	}
	return plan, nil
}

func validPurchaseStoredTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && formatCommercialTime(parsed) == value
}

func employeePurchaseWallet(ctx context.Context, tx *sql.Tx, owner Owner, currency string) (string, error) {
	if _, err := resolveOwner(ctx, tx, owner); err != nil {
		return "", err
	}
	var id, kind, key, employee, resourceKind, resourceID, storedCurrency string
	var keyID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency FROM financial_accounts WHERE owner_key=? AND currency=?`, ownerKey(owner), currency).Scan(&id, &kind, &key, &employee, &keyID, &resourceKind, &resourceID, &storedCurrency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", ErrUnavailable
	}
	if !validText(id, 256) || kind != string(OwnerEmployee) || key != ownerKey(owner) || employee != owner.EmployeeID || keyID.Valid || resourceKind != "" || resourceID != "" || storedCurrency != currency {
		return "", ErrSchema
	}
	return id, nil
}

func replayEmployeePurchase(ctx context.Context, tx *sql.Tx, input EmployeePurchaseInput, receipt CommercialReceipt) (EmployeePurchaseResult, error) {
	if receipt.ResourceKind != "subscription" || !validText(receipt.ResourceID, 256) || receipt.Revision != 1 {
		return EmployeePurchaseResult{}, ErrSchema
	}
	item, err := loadSubscription(ctx, tx, receipt.ResourceID)
	if err != nil {
		return EmployeePurchaseResult{}, ErrSchema
	}
	expected := input.Expected
	if item.ID != receipt.ResourceID || item.PlanID != expected.PlanID || item.PlanRevision != expected.Revision || item.PriceMicro != expected.PriceMicro || item.CreditMicro != expected.CreditMicro || item.Currency != expected.Currency || item.Interval != "one_time" || item.PeriodEndAt != nil || item.PredecessorID != "" || item.SuccessorID != "" || !item.StartedAt.Equal(receipt.CreatedAt) {
		return EmployeePurchaseResult{}, ErrSchema
	}
	accountID, err := employeePurchaseWallet(ctx, tx, input.Owner, expected.Currency)
	if err != nil || accountID != item.AccountID {
		return EmployeePurchaseResult{}, ErrSchema
	}
	stored, found, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	if !found || stored.Version != 2 || stored.Actor != input.Actor {
		return EmployeePurchaseResult{}, ErrSchema
	}
	var action, kind, id, created string
	if err := tx.QueryRowContext(ctx, `SELECT action,resource_kind,resource_id,created_at FROM financial_operations WHERE operation_id=?`, input.OperationID).Scan(&action, &kind, &id, &created); err != nil {
		return EmployeePurchaseResult{}, ErrSchema
	}
	started, err := time.Parse(time.RFC3339Nano, created)
	if err != nil || formatCommercialTime(started) != created || !started.Equal(item.StartedAt) || action != "subscription_purchase" || kind != "subscription" || id != item.ID {
		return EmployeePurchaseResult{}, ErrSchema
	}
	post := employeePurchasePost(input, item.ID)
	wantDigest, err := postDigestV2(post, input.Actor)
	if err != nil || !equalBytes(stored.Digest, wantDigest[:]) {
		return EmployeePurchaseResult{}, ErrSchema
	}
	entries, err := loadOperationEntries(ctx, tx, input.OperationID)
	if err != nil {
		return EmployeePurchaseResult{}, err
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, input.OperationID).Scan(&total); err != nil || total != 2 || len(entries) != 2 {
		return EmployeePurchaseResult{}, ErrSchema
	}
	var ledgerReceipt PurchaseLedgerReceipt
	ledgerReceipt.OperationID = input.OperationID
	for _, entry := range entries {
		if entry.OperationID != input.OperationID || entry.AccountID != accountID || entry.Owner != input.Owner || entry.Currency != expected.Currency || entry.ResourceKind != "subscription" || entry.ResourceID != item.ID || entry.OriginalEntryID != "" || !entry.CreatedAt.Equal(item.StartedAt) {
			return EmployeePurchaseResult{}, ErrSchema
		}
		switch entry.Kind {
		case EntrySubscriptionCharge:
			if ledgerReceipt.Charge.ID != "" || entry.AmountMicro != -expected.PriceMicro {
				return EmployeePurchaseResult{}, ErrSchema
			}
			ledgerReceipt.Charge = entry
		case EntrySubscriptionCredit:
			if ledgerReceipt.Credit.ID != "" || entry.AmountMicro != expected.CreditMicro {
				return EmployeePurchaseResult{}, ErrSchema
			}
			ledgerReceipt.Credit = entry
		default:
			return EmployeePurchaseResult{}, ErrSchema
		}
	}
	if ledgerReceipt.Charge.ID == "" || ledgerReceipt.Credit.ID == "" {
		return EmployeePurchaseResult{}, ErrSchema
	}
	return EmployeePurchaseResult{Subscription: item, CommercialReceipt: receipt, LedgerReceipt: ledgerReceipt, Replay: true}, nil
}
