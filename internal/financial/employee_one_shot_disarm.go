package financial

// Independently authored for docs/employee-self-one-shot-disarm-contract.md.
// The service owns employee authentication, the SQLite transaction and commit.

import (
	"context"
	"database/sql"
	"time"
)

type EmployeeOneShotDisarmInput struct {
	OperationID      string
	Actor            Actor
	PredecessorID    string
	ExpectedRevision int64
	ObservedAt       time.Time
}

type EmployeeOneShotDisarmResult struct {
	Status  OneShotRenewalStatus
	Receipt CommercialReceipt
}

func validEmployeeOneShotDisarm(input EmployeeOneShotDisarmInput) bool {
	return validCommercialText(input.OperationID, 128) && validCommercialText(input.PredecessorID, 256) &&
		input.Actor.Kind == ActorEmployee && validText(input.Actor.ID, 256) &&
		input.ExpectedRevision >= 1 && input.ExpectedRevision <= subscriptionRevisionMax &&
		!input.ObservedAt.IsZero() && input.ObservedAt.Location() == time.UTC
}

func employeeOneShotDisarmMeta(input EmployeeOneShotDisarmInput) WriteMeta {
	return WriteMeta{
		OperationID: input.OperationID, Actor: input.Actor,
		PayloadDigest: oneShotPayload(input.PredecessorID, input.ExpectedRevision),
		ObservedAt:    input.ObservedAt,
	}
}

// ReadEmployeeOneShotRenewalTx returns a provisional minimal status. For none,
// the subscription period end is deliberately not exposed as a reservation due.
func (c *Commercial) ReadEmployeeOneShotRenewalTx(ctx context.Context, tx *sql.Tx, employeeID, predecessorID string, asOf time.Time) (OneShotRenewalStatus, error) {
	_, record, err := c.employeeOneShotRecordTx(ctx, tx, employeeID, predecessorID, asOf)
	if err != nil {
		return OneShotRenewalStatus{}, err
	}
	status := record.OneShotRenewalStatus
	if status.State == "none" {
		status.DueAt = nil
		status.Revision = 0
	}
	return status, nil
}

func (c *Commercial) employeeOneShotRecordTx(ctx context.Context, tx *sql.Tx, employeeID, predecessorID string, asOf time.Time) (Subscription, oneShotRecord, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validText(employeeID, 256) || !validCommercialText(predecessorID, 256) || asOf.IsZero() || asOf.Location() != time.UTC {
		return Subscription{}, oneShotRecord{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return Subscription{}, oneShotRecord{}, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	sub, err := loadSubscriptionAt(ctx, tx, predecessorID, asOf)
	if err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	if err := validateEmployeeCancelSubscriptionShape(ctx, tx, sub); err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	ownerOK, err := employeeSubscriptionCancelOwner(ctx, tx, sub, employeeID)
	if err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	if !ownerOK || sub.Interval != "monthly" {
		return Subscription{}, oneShotRecord{}, ErrNotFound
	}
	record, err := readOneShotRecord(ctx, tx, sub)
	if err != nil {
		return Subscription{}, oneShotRecord{}, err
	}
	if ctx.Err() != nil {
		return Subscription{}, oneShotRecord{}, ErrUnavailable
	}
	return sub, record, nil
}

// ProbeEmployeeOneShotDisarmReplayTx checks global identity before current
// reservation-state gates. A matching receipt is not success until its entire
// reservation and direct-owner chain has been validated.
func (c *Commercial) ProbeEmployeeOneShotDisarmReplayTx(ctx context.Context, tx *sql.Tx, input EmployeeOneShotDisarmInput) (EmployeeOneShotDisarmResult, bool, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeOneShotDisarm(input) {
		return EmployeeOneShotDisarmResult{}, false, ErrInvalid
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeOneShotDisarmResult{}, false, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeOneShotDisarmResult{}, false, err
	}
	receipt, found, err := existingCommercialOperation(ctx, tx, employeeOneShotDisarmMeta(input), "subscription.one_shot.disarm")
	if err != nil {
		return EmployeeOneShotDisarmResult{}, false, err
	}
	if !found {
		if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
			return EmployeeOneShotDisarmResult{}, false, err
		} else if exists {
			return EmployeeOneShotDisarmResult{}, false, ErrConflict
		}
		return EmployeeOneShotDisarmResult{}, false, nil
	}
	if receipt.OperationID != input.OperationID || receipt.ResourceKind != "subscription_one_shot" || receipt.ResourceID != input.PredecessorID || receipt.Revision != 2 || input.ExpectedRevision != 1 || receipt.CreatedAt.Location() != time.UTC {
		return EmployeeOneShotDisarmResult{}, false, ErrSchema
	}
	var storedTime string
	if err := tx.QueryRowContext(ctx, `SELECT created_at FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).Scan(&storedTime); err != nil || storedTime != formatCommercialTime(receipt.CreatedAt) {
		return EmployeeOneShotDisarmResult{}, false, ErrSchema
	}
	if _, exists, err := operationDigest(ctx, tx, input.OperationID); err != nil {
		return EmployeeOneShotDisarmResult{}, false, err
	} else if exists {
		return EmployeeOneShotDisarmResult{}, false, ErrSchema
	}
	_, record, err := c.employeeOneShotRecordTx(ctx, tx, input.Actor.ID, input.PredecessorID, input.ObservedAt)
	if err != nil {
		return EmployeeOneShotDisarmResult{}, false, ErrSchema
	}
	if record.State != "disarmed" || record.Revision != 2 || record.Reason != "disarmed" || record.disarmOperationID != input.OperationID || record.TerminalAt == nil || !record.TerminalAt.Equal(receipt.CreatedAt) || record.SuccessorID != "" {
		return EmployeeOneShotDisarmResult{}, false, ErrSchema
	}
	if ctx.Err() != nil {
		return EmployeeOneShotDisarmResult{}, false, ErrUnavailable
	}
	return EmployeeOneShotDisarmResult{Status: record.OneShotRenewalStatus, Receipt: receipt}, true, nil
}

// DisarmEmployeeOneShotRenewalTx never commits and never arms a reservation.
// It is a new-write primitive; callers must probe first to accept exact replay.
func (c *Commercial) DisarmEmployeeOneShotRenewalTx(ctx context.Context, tx *sql.Tx, input EmployeeOneShotDisarmInput) (EmployeeOneShotDisarmResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeOneShotDisarm(input) {
		return EmployeeOneShotDisarmResult{}, ErrInvalid
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeOneShotDisarmResult{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeOneShotDisarmResult{}, err
	}
	var commercialUsed, ledgerUsed int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id=?),(SELECT COUNT(*) FROM financial_operations WHERE operation_id=?)`, input.OperationID, input.OperationID).Scan(&commercialUsed, &ledgerUsed); err != nil {
		return EmployeeOneShotDisarmResult{}, ErrUnavailable
	}
	if commercialUsed != 0 || ledgerUsed != 0 {
		return EmployeeOneShotDisarmResult{}, ErrConflict
	}
	_, record, err := c.employeeOneShotRecordTx(ctx, tx, input.Actor.ID, input.PredecessorID, input.ObservedAt)
	if err != nil {
		return EmployeeOneShotDisarmResult{}, err
	}
	if record.State != "armed" || record.Revision != input.ExpectedRevision || input.ExpectedRevision != 1 {
		return EmployeeOneShotDisarmResult{}, ErrConflict
	}
	if input.ObservedAt.Before(record.armedAt) {
		return EmployeeOneShotDisarmResult{}, ErrUnavailable
	}
	receipt := CommercialReceipt{OperationID: input.OperationID, ResourceKind: "subscription_one_shot", ResourceID: input.PredecessorID, Revision: 2, CreatedAt: input.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, employeeOneShotDisarmMeta(input), "subscription.one_shot.disarm", receipt); err != nil {
		return EmployeeOneShotDisarmResult{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscription_one_shot_renewals SET state='disarmed',revision=2,reason='disarmed',disarm_operation_id=?,terminal_at=?,updated_at=? WHERE predecessor_id=? AND state='armed' AND revision=?`, input.OperationID, formatCommercialTime(input.ObservedAt), formatCommercialTime(input.ObservedAt), input.PredecessorID, input.ExpectedRevision)
	if err != nil {
		return EmployeeOneShotDisarmResult{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return EmployeeOneShotDisarmResult{}, ErrConflict
	}
	_, updated, err := c.employeeOneShotRecordTx(ctx, tx, input.Actor.ID, input.PredecessorID, input.ObservedAt)
	if err != nil {
		return EmployeeOneShotDisarmResult{}, err
	}
	if updated.State != "disarmed" || updated.Revision != 2 || updated.disarmOperationID != input.OperationID || updated.TerminalAt == nil || !updated.TerminalAt.Equal(input.ObservedAt) {
		return EmployeeOneShotDisarmResult{}, ErrSchema
	}
	if ctx.Err() != nil {
		return EmployeeOneShotDisarmResult{}, ErrUnavailable
	}
	return EmployeeOneShotDisarmResult{Status: updated.OneShotRenewalStatus, Receipt: receipt}, nil
}
