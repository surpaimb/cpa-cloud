package financial

// Independently authored for docs/employee-self-subscription-cancel-contract.md.
// The caller owns the SQLite transaction and performs employee authentication.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type EmployeeSubscriptionCancelInput struct {
	OperationID      string
	Actor            Actor
	SubscriptionID   string
	ExpectedRevision int64
	ObservedAt       time.Time
}

type EmployeeSubscriptionCancelResult struct {
	Subscription Subscription
	Receipt      CommercialReceipt
}

func validEmployeeSubscriptionCancel(input EmployeeSubscriptionCancelInput) bool {
	return validCommercialText(input.OperationID, 128) && validCommercialText(input.SubscriptionID, 256) &&
		input.Actor.Kind == ActorEmployee && validText(input.Actor.ID, 256) &&
		input.ExpectedRevision >= 1 && input.ExpectedRevision <= subscriptionRevisionMax &&
		!input.ObservedAt.IsZero() && input.ObservedAt.Location() == time.UTC
}

func employeeSubscriptionCancelMeta(input EmployeeSubscriptionCancelInput) (WriteMeta, error) {
	digest, err := DigestPayload(struct {
		Domain           string `json:"domain"`
		SubscriptionID   string `json:"subscription_id"`
		ExpectedRevision int64  `json:"expected_revision"`
	}{"employee-self-subscription-cancel/v1", input.SubscriptionID, input.ExpectedRevision})
	if err != nil {
		return WriteMeta{}, ErrInvalid
	}
	return WriteMeta{OperationID: input.OperationID, Actor: input.Actor, PayloadDigest: digest, ObservedAt: input.ObservedAt}, nil
}

func (c *Commercial) ProbeEmployeeSubscriptionCancelReplayTx(ctx context.Context, tx *sql.Tx, input EmployeeSubscriptionCancelInput) (EmployeeSubscriptionCancelResult, bool, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeSubscriptionCancel(input) {
		return EmployeeSubscriptionCancelResult{}, false, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionCancelResult{}, false, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	}
	meta, err := employeeSubscriptionCancelMeta(input)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	}
	receipt, found, err := existingCommercialOperation(ctx, tx, meta, "subscription.cancel")
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	}
	if !found {
		if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
			return EmployeeSubscriptionCancelResult{}, false, err
		} else if exists {
			return EmployeeSubscriptionCancelResult{}, false, ErrConflict
		}
		return EmployeeSubscriptionCancelResult{}, false, nil
	}
	if receipt.OperationID != input.OperationID || receipt.ResourceKind != "subscription" || receipt.ResourceID != input.SubscriptionID || receipt.Revision != input.ExpectedRevision+1 {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	if receipt.CreatedAt.Location() != time.UTC {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	var storedTime string
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&storedTime); err != nil || storedTime != formatCommercialTime(receipt.CreatedAt) {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	} else if exists {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	item, err := loadSubscriptionAt(ctx, tx, input.SubscriptionID, input.ObservedAt)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	if err := validateEmployeeCancelSubscriptionShape(ctx, tx, item); err != nil {
		return EmployeeSubscriptionCancelResult{}, false, err
	}
	ownerOK, ownerErr := employeeSubscriptionCancelOwner(ctx, tx, item, input.Actor.ID)
	if item.Status != "cancelled" || item.CancelledAt == nil || !item.CancelledAt.Equal(receipt.CreatedAt) || item.Revision != receipt.Revision || ownerErr != nil || !ownerOK {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	if _, err := readOneShotRecord(ctx, tx, item); err != nil {
		return EmployeeSubscriptionCancelResult{}, false, ErrSchema
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionCancelResult{}, false, ErrUnavailable
	}
	return EmployeeSubscriptionCancelResult{Subscription: item, Receipt: receipt}, true, nil
}

// CancelEmployeeSubscriptionTx does not commit. Its result is provisional.
func (c *Commercial) CancelEmployeeSubscriptionTx(ctx context.Context, tx *sql.Tx, input EmployeeSubscriptionCancelInput) (EmployeeSubscriptionCancelResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeSubscriptionCancel(input) {
		return EmployeeSubscriptionCancelResult{}, ErrInvalid
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	// This exported new-write primitive must preserve the global operation ID
	// invariant even when called without the HTTP layer's replay-first probe.
	var commercialUsed, ledgerUsed int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?),(SELECT COUNT(*) FROM financial_operations WHERE operation_id=?)`, input.OperationID, input.OperationID).Scan(&commercialUsed, &ledgerUsed); err != nil {
		return EmployeeSubscriptionCancelResult{}, ErrUnavailable
	}
	if commercialUsed != 0 || ledgerUsed != 0 {
		return EmployeeSubscriptionCancelResult{}, ErrConflict
	}
	item, err := loadSubscriptionAt(ctx, tx, input.SubscriptionID, input.ObservedAt)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	if err := validateEmployeeCancelSubscriptionShape(ctx, tx, item); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	ownerOK, err := employeeSubscriptionCancelOwner(ctx, tx, item, input.Actor.ID)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	if !ownerOK {
		return EmployeeSubscriptionCancelResult{}, ErrNotFound
	}
	if item.Status != "active" || item.Revision != input.ExpectedRevision || item.Revision >= subscriptionRevisionMax || input.ObservedAt.Before(item.StartedAt) || item.PeriodEndAt != nil && !input.ObservedAt.Before(*item.PeriodEndAt) {
		return EmployeeSubscriptionCancelResult{}, ErrConflict
	}
	if _, err := readOneShotRecord(ctx, tx, item); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	var end any
	if item.PeriodEndAt != nil {
		end = input.ObservedAt.Format(subscriptionEndLayout)
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscriptions SET status='cancelled',cancelled_at=?,revision=revision+1 WHERE id=? AND account_id=? AND status='active' AND revision=? AND (period_end_at IS NULL OR period_end_at>?)`, formatCommercialTime(input.ObservedAt), item.ID, item.AccountID, item.Revision, end)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return EmployeeSubscriptionCancelResult{}, ErrConflict
	}
	item.Status = "cancelled"
	item.Revision++
	at := input.ObservedAt
	item.CancelledAt = &at
	meta, err := employeeSubscriptionCancelMeta(input)
	if err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	receipt := CommercialReceipt{OperationID: input.OperationID, ResourceKind: "subscription", ResourceID: item.ID, Revision: item.Revision, CreatedAt: at}
	if err := insertCommercialOperation(ctx, tx, meta, "subscription.cancel", receipt); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	if err := settleOneShotAfterCancellation(ctx, tx, item.ID, at); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	if _, err := readOneShotRecord(ctx, tx, item); err != nil {
		return EmployeeSubscriptionCancelResult{}, err
	}
	return EmployeeSubscriptionCancelResult{Subscription: item, Receipt: receipt}, nil
}

func employeeSubscriptionCancelOwner(ctx context.Context, tx *sql.Tx, item Subscription, employeeID string) (bool, error) {
	var id, kind, ownerKeyText, ownerEmployeeID, resourceKind, resourceID, currency, created string
	var keyID sql.NullString
	var idType, kindType, ownerKeyType, employeeType, keyType, resourceKindType, resourceIDType, currencyType, createdType string
	err := tx.QueryRowContext(ctx, `SELECT id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency,created_at,typeof(id),typeof(owner_kind),typeof(owner_key),typeof(employee_id),typeof(key_id),typeof(resource_kind),typeof(resource_id),typeof(currency),typeof(created_at) FROM financial_accounts WHERE id=?`, item.AccountID).
		Scan(&id, &kind, &ownerKeyText, &ownerEmployeeID, &keyID, &resourceKind, &resourceID, &currency, &created, &idType, &kindType, &ownerKeyType, &employeeType, &keyType, &resourceKindType, &resourceIDType, &currencyType, &createdType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrSchema
	}
	if err != nil {
		return false, ErrUnavailable
	}
	owner := Owner{Kind: OwnerKind(kind), EmployeeID: ownerEmployeeID, KeyID: keyID.String, ResourceKind: resourceKind, ResourceID: resourceID}
	if idType != "text" || kindType != "text" || ownerKeyType != "text" || employeeType != "text" || (keyType != "null" && keyType != "text") || resourceKindType != "text" || resourceIDType != "text" || currencyType != "text" || createdType != "text" || !validCommercialText(id, 256) || id != item.AccountID || !validOwnerShape(owner) || ownerKey(owner) != ownerKeyText || !validCurrency(currency) || currency != item.Currency || !validPurchaseStoredTime(created) || (owner.Kind == OwnerEmployee && keyID.Valid) || (owner.Kind == OwnerKey && !keyID.Valid) || (keyID.Valid && keyID.String == "") {
		return false, ErrSchema
	}
	return owner.Kind == OwnerEmployee && owner.EmployeeID == employeeID, nil
}

func validateEmployeeCancelSubscriptionShape(ctx context.Context, tx *sql.Tx, item Subscription) error {
	var idType, accountType, planType, planRevisionType, priceType, creditType, currencyType string
	var intervalType, statusType, startedType, endType, cancelledType, revisionType string
	err := tx.QueryRowContext(ctx, `SELECT typeof(id),typeof(account_id),typeof(plan_id),typeof(plan_revision),typeof(price_micro),typeof(credit_micro),typeof(currency),typeof(interval),typeof(status),typeof(started_at),typeof(period_end_at),typeof(cancelled_at),typeof(revision) FROM financial_subscriptions WHERE id=?`, item.ID).
		Scan(&idType, &accountType, &planType, &planRevisionType, &priceType, &creditType, &currencyType, &intervalType, &statusType, &startedType, &endType, &cancelledType, &revisionType)
	if err != nil {
		return ErrUnavailable
	}
	if idType != "text" || accountType != "text" || planType != "text" || planRevisionType != "integer" || priceType != "integer" || creditType != "integer" || currencyType != "text" || intervalType != "text" || statusType != "text" || startedType != "text" || revisionType != "integer" ||
		(endType != "null" && endType != "text") || (cancelledType != "null" && cancelledType != "text") ||
		!validCommercialText(item.ID, 256) || !validCommercialText(item.AccountID, 256) || !validCommercialText(item.PlanID, 256) || !validCurrency(item.Currency) ||
		item.PlanRevision < 1 || item.PlanRevision > subscriptionRevisionMax || item.PriceMicro <= 0 || item.CreditMicro <= 0 || item.Revision < 1 || item.Revision > subscriptionRevisionMax ||
		item.StartedAt.Location() != time.UTC || !validPurchaseStoredTime(formatCommercialTime(item.StartedAt)) {
		return ErrSchema
	}
	return nil
}
