package financial

// Independently authored for docs/employee-self-plan-purchase-contract.md.
// The caller owns the read-only transaction and the authenticated employee ID.

import (
	"context"
	"database/sql"
	"errors"
)

// ReadEmployeePurchaseQuoteTx validates the current one-time offer and an
// existing direct employee wallet in one caller-owned snapshot. It does not
// reserve funds, create accounts, or perform a purchase.
func (c *Commercial) ReadEmployeePurchaseQuoteTx(ctx context.Context, tx *sql.Tx, owner Owner, planID string) (ExpectedPurchasePlan, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || owner.Kind != OwnerEmployee || !validOwnerShape(owner) || !validCommercialText(planID, 256) {
		return ExpectedPurchasePlan{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return ExpectedPurchasePlan{}, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return ExpectedPurchasePlan{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return ExpectedPurchasePlan{}, err
	}
	if err := requireEmployeePurchaseEnabled(ctx, tx); err != nil {
		return ExpectedPurchasePlan{}, err
	}
	plan, err := loadEmployeePurchasePlan(ctx, tx, planID)
	if errors.Is(err, ErrNotFound) {
		return ExpectedPurchasePlan{}, ErrConflict
	}
	if err != nil {
		return ExpectedPurchasePlan{}, err
	}
	if !validEmployeePurchasePlan(plan) {
		return ExpectedPurchasePlan{}, ErrSchema
	}
	if !plan.Enabled || plan.Interval != "one_time" {
		return ExpectedPurchasePlan{}, ErrConflict
	}
	if _, err := employeePurchaseWallet(ctx, tx, owner, plan.Currency); err != nil {
		return ExpectedPurchasePlan{}, err
	}
	if ctx.Err() != nil {
		return ExpectedPurchasePlan{}, ErrUnavailable
	}
	return ExpectedPurchasePlan{PlanID: plan.ID, Revision: plan.Revision, Currency: plan.Currency, Interval: plan.Interval, PriceMicro: plan.PriceMicro, CreditMicro: plan.CreditMicro}, nil
}
