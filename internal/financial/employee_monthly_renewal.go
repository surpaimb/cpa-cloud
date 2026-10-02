package financial

// Independently authored for docs/employee-self-monthly-renewal-contract.md.
// These helpers keep the employee authority and existing-wallet constraint in
// the caller's transaction; they do not open or commit a transaction.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type EmployeeMonthlyRenewalQuote struct {
	PredecessorID  string
	PredecessorEnd string
	Expected       ExpectedPurchasePlan
}

type EmployeeMonthlyRenewalInput struct {
	OperationID    string
	Actor          Actor
	Owner          Owner
	PredecessorID  string
	PredecessorEnd string
	Expected       ExpectedPurchasePlan
	ObservedAt     time.Time
}

type EmployeeMonthlyRenewalResult struct {
	Subscription Subscription
	Receipt      CommercialReceipt
	Replay       bool
}

// ReadEmployeeMonthlyRenewalQuoteTx checks one directly owned due predecessor,
// current monthly offer, and an existing wallet without reserving or writing.
func (c *Commercial) ReadEmployeeMonthlyRenewalQuoteTx(ctx context.Context, tx *sql.Tx, employeeID, predecessorID string, asOf time.Time) (EmployeeMonthlyRenewalQuote, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validText(employeeID, 256) || !validCommercialText(predecessorID, 256) || asOf.IsZero() || asOf.Location() != time.UTC {
		return EmployeeMonthlyRenewalQuote{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeeMonthlyRenewalQuote{}, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if err := snapshotOwnershipBoundary(ctx, tx, employeeID, predecessorID); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	predecessor, err := loadSubscriptionAt(ctx, tx, predecessorID, asOf)
	if err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if err := validateEmployeeCancelSubscriptionShape(ctx, tx, predecessor); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	owned, err := employeeSubscriptionCancelOwner(ctx, tx, predecessor, employeeID)
	if err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if !owned {
		return EmployeeMonthlyRenewalQuote{}, ErrNotFound
	}
	if predecessor.Interval != "monthly" || predecessor.Status != "expired" || predecessor.PeriodEndAt == nil || predecessor.SuccessorID != "" {
		return EmployeeMonthlyRenewalQuote{}, ErrConflict
	}
	if err := requireEmployeePurchaseEnabled(ctx, tx); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	plan, err := loadEmployeePurchasePlan(ctx, tx, predecessor.PlanID)
	if errors.Is(err, ErrNotFound) {
		return EmployeeMonthlyRenewalQuote{}, ErrConflict
	}
	if err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if !validEmployeePurchasePlan(plan) {
		return EmployeeMonthlyRenewalQuote{}, ErrSchema
	}
	if !plan.Enabled || plan.Interval != "monthly" {
		return EmployeeMonthlyRenewalQuote{}, ErrConflict
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: employeeID}
	if _, err := employeePurchaseWallet(ctx, tx, owner, plan.Currency); err != nil {
		return EmployeeMonthlyRenewalQuote{}, err
	}
	if ctx.Err() != nil {
		return EmployeeMonthlyRenewalQuote{}, ErrUnavailable
	}
	return EmployeeMonthlyRenewalQuote{
		PredecessorID:  predecessor.ID,
		PredecessorEnd: predecessor.PeriodEndAt.Format(subscriptionEndLayout),
		Expected: ExpectedPurchasePlan{
			PlanID: plan.ID, Revision: plan.Revision, Currency: plan.Currency, Interval: "monthly",
			PriceMicro: plan.PriceMicro, CreditMicro: plan.CreditMicro,
		},
	}, nil
}

func validEmployeeMonthlyRenewal(input EmployeeMonthlyRenewalInput) bool {
	actor, actorOK := effectiveActor(input.Actor, "")
	end, endErr := parseSubscriptionEnd(input.PredecessorEnd)
	expected := input.Expected
	return actorOK && actor == input.Actor && actor.Kind == ActorEmployee &&
		input.Owner.Kind == OwnerEmployee && input.Owner.EmployeeID == actor.ID && validOwnerShape(input.Owner) &&
		validText(input.OperationID, 128) && validCommercialText(input.PredecessorID, 256) &&
		endErr == nil && end.Format(subscriptionEndLayout) == input.PredecessorEnd &&
		validCommercialText(expected.PlanID, 256) && validCurrency(expected.Currency) && expected.Interval == "monthly" &&
		expected.PriceMicro > 0 && expected.CreditMicro > 0 && expected.Revision >= 1 && expected.Revision <= subscriptionRevisionMax &&
		!input.ObservedAt.IsZero() && input.ObservedAt.Location() == time.UTC
}

func employeeMonthlyRenewalDigest(input EmployeeMonthlyRenewalInput) ([32]byte, error) {
	return DigestPayload(struct {
		Domain         string               `json:"domain"`
		OperationID    string               `json:"operation_id"`
		Action         string               `json:"action"`
		Actor          Actor                `json:"actor"`
		Owner          Owner                `json:"owner"`
		PredecessorID  string               `json:"predecessor_id"`
		PredecessorEnd string               `json:"predecessor_period_end_at"`
		Expected       ExpectedPurchasePlan `json:"expected"`
	}{"employee-monthly-renewal/v1", input.OperationID, "subscription.renew", input.Actor, input.Owner, input.PredecessorID, input.PredecessorEnd, input.Expected})
}

func employeeMonthlyRenewalMeta(input EmployeeMonthlyRenewalInput) (WriteMeta, error) {
	digest, err := employeeMonthlyRenewalDigest(input)
	if err != nil {
		return WriteMeta{}, err
	}
	return WriteMeta{OperationID: input.OperationID, Actor: input.Actor, PayloadDigest: digest, ObservedAt: input.ObservedAt}, nil
}

// ProbeEmployeeMonthlyRenewalReplayTx resolves a global operation ID before
// gates that only apply to a new renewal. It verifies the entire old result.
func (c *Commercial) ProbeEmployeeMonthlyRenewalReplayTx(ctx context.Context, tx *sql.Tx, input EmployeeMonthlyRenewalInput) (EmployeeMonthlyRenewalResult, bool, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeMonthlyRenewal(input) {
		return EmployeeMonthlyRenewalResult{}, false, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeeMonthlyRenewalResult{}, false, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeMonthlyRenewalResult{}, false, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeMonthlyRenewalResult{}, false, err
	}
	meta, err := employeeMonthlyRenewalMeta(input)
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, false, ErrInvalid
	}
	receipt, found, err := existingCommercialOperation(ctx, tx, meta, "subscription.renew")
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, false, err
	}
	if !found {
		if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
			return EmployeeMonthlyRenewalResult{}, false, err
		} else if exists {
			return EmployeeMonthlyRenewalResult{}, false, ErrConflict
		}
		return EmployeeMonthlyRenewalResult{}, false, nil
	}
	result, err := c.replayEmployeeMonthlyRenewalTx(ctx, tx, input, receipt)
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, false, err
	}
	return result, true, nil
}

func (c *Commercial) replayEmployeeMonthlyRenewalTx(ctx context.Context, tx *sql.Tx, input EmployeeMonthlyRenewalInput, receipt CommercialReceipt) (EmployeeMonthlyRenewalResult, error) {
	if receipt.ResourceKind != "subscription" || !validCommercialText(receipt.ResourceID, 256) || receipt.Revision != 1 || receipt.CreatedAt.Location() != time.UTC {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	item, err := loadSubscriptionAt(ctx, tx, receipt.ResourceID, input.ObservedAt)
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	plan := input.Expected
	if item.PredecessorID != input.PredecessorID || item.PlanID != plan.PlanID || item.PlanRevision != plan.Revision ||
		item.PriceMicro != plan.PriceMicro || item.CreditMicro != plan.CreditMicro || item.Currency != plan.Currency ||
		item.Interval != "monthly" || item.PeriodEndAt == nil || !item.StartedAt.Equal(receipt.CreatedAt) {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	if err := snapshotOwnershipBoundary(ctx, tx, input.Actor.ID, input.PredecessorID); err != nil {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	predecessor, err := loadSubscriptionAt(ctx, tx, input.PredecessorID, input.ObservedAt)
	if err != nil || predecessor.PeriodEndAt == nil || predecessor.PeriodEndAt.Format(subscriptionEndLayout) != input.PredecessorEnd || predecessor.SuccessorID != item.ID {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	if _, err := c.ReadEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, input.Actor.ID, item.ID); err != nil {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	stored, found, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil || !found || stored.Version != 2 || stored.Actor != input.Actor {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	post := employeePurchasePost(EmployeePurchaseInput{OperationID: input.OperationID, Actor: input.Actor, Owner: input.Owner, Expected: input.Expected, ObservedAt: item.StartedAt}, item.ID)
	want, err := postDigestV2(post, input.Actor)
	if err != nil || !equalBytes(stored.Digest, want[:]) || ctx.Err() != nil {
		return EmployeeMonthlyRenewalResult{}, ErrSchema
	}
	receipt.Replay = true
	return EmployeeMonthlyRenewalResult{Subscription: item, Receipt: receipt, Replay: true}, nil
}

// RenewEmployeeMonthlySubscriptionTx uses the same financial transition as
// admin/worker renewal but requires an already existing direct employee wallet.
func (c *Commercial) RenewEmployeeMonthlySubscriptionTx(ctx context.Context, tx *sql.Tx, input EmployeeMonthlyRenewalInput) (EmployeeMonthlyRenewalResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeMonthlyRenewal(input) {
		return EmployeeMonthlyRenewalResult{}, ErrInvalid
	}
	if result, found, err := c.ProbeEmployeeMonthlyRenewalReplayTx(ctx, tx, input); err != nil {
		return EmployeeMonthlyRenewalResult{}, err
	} else if found {
		return result, nil
	}
	meta, err := employeeMonthlyRenewalMeta(input)
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, ErrInvalid
	}
	item, receipt, err := c.renewSubscriptionWithRestrictionsTx(ctx, tx, meta, input.PredecessorID, input.ObservedAt, renewalRestrictions{
		employeeID: input.Actor.ID, predecessorEnd: input.PredecessorEnd, expectedPlan: &input.Expected,
	})
	if err != nil {
		return EmployeeMonthlyRenewalResult{}, err
	}
	if err := settleOneShotAfterManualRenewal(ctx, tx, input.PredecessorID, item.ID, input.ObservedAt); err != nil {
		return EmployeeMonthlyRenewalResult{}, err
	}
	if ctx.Err() != nil {
		return EmployeeMonthlyRenewalResult{}, ErrUnavailable
	}
	return EmployeeMonthlyRenewalResult{Subscription: item, Receipt: receipt}, nil
}
