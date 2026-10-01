package financial

// Independently authored for docs/subscription-one-shot-renewal-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const oneShotRenewalsDDL = `CREATE TABLE IF NOT EXISTS financial_subscription_one_shot_renewals (
	predecessor_id TEXT NOT NULL PRIMARY KEY REFERENCES financial_subscriptions(id) ON DELETE RESTRICT,
	arm_operation_id TEXT NOT NULL UNIQUE REFERENCES financial_commercial_operations(operation_id) ON DELETE RESTRICT,
	execution_operation_id TEXT NOT NULL UNIQUE,
	armed_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	predecessor_revision INTEGER NOT NULL CHECK(typeof(predecessor_revision)='integer' AND predecessor_revision BETWEEN 1 AND 9007199254740991),
	due_at TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('armed','disarmed','succeeded','failed','superseded','cancelled')),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	reason TEXT,
	successor_id TEXT UNIQUE REFERENCES financial_subscriptions(id) ON DELETE RESTRICT,
	disarm_operation_id TEXT UNIQUE REFERENCES financial_commercial_operations(operation_id) ON DELETE RESTRICT,
	armed_at TEXT NOT NULL,
	terminal_at TEXT,
	updated_at TEXT NOT NULL,
	CHECK((state='armed' AND revision=1 AND reason IS NULL AND successor_id IS NULL AND disarm_operation_id IS NULL AND terminal_at IS NULL) OR
		(state='disarmed' AND revision=2 AND reason='disarmed' AND successor_id IS NULL AND disarm_operation_id IS NOT NULL AND terminal_at IS NOT NULL) OR
		(state='succeeded' AND revision=2 AND reason IS NULL AND successor_id IS NOT NULL AND disarm_operation_id IS NULL AND terminal_at IS NOT NULL) OR
		(state='superseded' AND revision=2 AND reason='manual_renewal' AND successor_id IS NOT NULL AND disarm_operation_id IS NULL AND terminal_at IS NOT NULL) OR
		(state='cancelled' AND revision=2 AND reason='predecessor_cancelled' AND successor_id IS NULL AND disarm_operation_id IS NULL AND terminal_at IS NOT NULL) OR
		(state='failed' AND revision=2 AND reason IN ('commercial_disabled','plan_unavailable','insufficient_balance','owner_unavailable','period_unrepresentable') AND successor_id IS NULL AND disarm_operation_id IS NULL AND terminal_at IS NOT NULL))
)`

const oneShotRenewalsDueIndexDDL = `CREATE INDEX IF NOT EXISTS financial_subscription_one_shot_due_idx ON financial_subscription_one_shot_renewals(state,due_at,predecessor_id)`
const oneShotRenewalsImmutableUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_subscription_one_shot_immutable_update BEFORE UPDATE OF predecessor_id,arm_operation_id,execution_operation_id,armed_by_admin_id,predecessor_revision,due_at,armed_at ON financial_subscription_one_shot_renewals BEGIN SELECT RAISE(ABORT,'one-shot renewal identity is immutable'); END`
const oneShotRenewalsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_subscription_one_shot_no_delete BEFORE DELETE ON financial_subscription_one_shot_renewals BEGIN SELECT RAISE(ABORT,'one-shot renewal history is immutable'); END`

type OneShotRenewalStatus struct {
	PredecessorID string
	State         string
	Revision      int64
	DueAt         *time.Time
	Reason        string
	SuccessorID   string
	TerminalAt    *time.Time
}

type oneShotRecord struct {
	OneShotRenewalStatus
	armOperationID, executionOperationID, armedByAdminID, disarmOperationID string
	predecessorRevision                                                     int64
	armedAt                                                                 time.Time
}

type ArmOneShotRenewal struct {
	Meta             WriteMeta
	PredecessorID    string
	ExpectedRevision int64
}

type DisarmOneShotRenewal struct {
	Meta             WriteMeta
	PredecessorID    string
	ExpectedRevision int64
}

func oneShotPayload(id string, revision int64) [32]byte {
	digest, _ := DigestPayload(struct {
		ID       string
		Expected int64
	}{id, revision})
	return digest
}

func (c *Commercial) OneShotRenewal(ctx context.Context, id string) (OneShotRenewalStatus, error) {
	if c == nil || c.db == nil || ctx == nil || !validCommercialText(id, 256) {
		return OneShotRenewalStatus{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OneShotRenewalStatus{}, ErrUnavailable
	}
	defer tx.Rollback()
	sub, err := loadSubscription(ctx, tx, id)
	if err != nil {
		return OneShotRenewalStatus{}, err
	}
	record, err := readOneShotRecord(ctx, tx, sub)
	return record.OneShotRenewalStatus, err
}

func (c *Commercial) ArmOneShotRenewal(ctx context.Context, input ArmOneShotRenewal) (OneShotRenewalStatus, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || input.Meta.ActorAdminID == "" || !validCommercialText(input.PredecessorID, 256) || input.ExpectedRevision < 1 {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "subscription.one_shot.arm"); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	} else if found {
		sub, err := loadSubscription(ctx, tx, input.PredecessorID)
		if err != nil || receipt.ResourceID != input.PredecessorID {
			return OneShotRenewalStatus{}, CommercialReceipt{}, ErrSchema
		}
		record, err := readOneShotRecord(ctx, tx, sub)
		if err != nil || record.armOperationID != input.Meta.OperationID {
			return OneShotRenewalStatus{}, CommercialReceipt{}, ErrSchema
		}
		return record.OneShotRenewalStatus, receipt, nil
	}
	now := c.clockNow()
	sub, err := loadSubscriptionAt(ctx, tx, input.PredecessorID, now)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	if sub.Interval != "monthly" || sub.Status != "active" || sub.PeriodEndAt == nil || sub.SuccessorID != "" || sub.Revision != input.ExpectedRevision {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrConflict
	}
	previous, err := readOneShotRecord(ctx, tx, sub)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	if previous.State != "none" {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrConflict
	}
	executionID, err := randomID("one_shot_renewal")
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	meta := input.Meta
	meta.ObservedAt = now
	receipt := CommercialReceipt{OperationID: meta.OperationID, ResourceKind: "subscription_one_shot", ResourceID: sub.ID, Revision: 1, CreatedAt: now}
	if err := insertCommercialOperation(ctx, tx, meta, "subscription.one_shot.arm", receipt); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscription_one_shot_renewals(predecessor_id,arm_operation_id,execution_operation_id,armed_by_admin_id,predecessor_revision,due_at,state,revision,armed_at,updated_at) VALUES(?,?,?,?,?,?,'armed',1,?,?)`, sub.ID, meta.OperationID, executionID, meta.ActorAdminID, sub.Revision, sub.PeriodEndAt.Format(subscriptionEndLayout), formatCommercialTime(now), formatCommercialTime(now)); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	return OneShotRenewalStatus{PredecessorID: sub.ID, State: "armed", Revision: 1, DueAt: sub.PeriodEndAt}, receipt, nil
}

func (c *Commercial) DisarmOneShotRenewal(ctx context.Context, input DisarmOneShotRenewal) (OneShotRenewalStatus, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || input.Meta.ActorAdminID == "" || !validCommercialText(input.PredecessorID, 256) || input.ExpectedRevision < 1 {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "subscription.one_shot.disarm"); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	} else if found {
		sub, err := loadSubscription(ctx, tx, input.PredecessorID)
		if err != nil || receipt.ResourceID != input.PredecessorID {
			return OneShotRenewalStatus{}, CommercialReceipt{}, ErrSchema
		}
		record, err := readOneShotRecord(ctx, tx, sub)
		if err != nil || record.disarmOperationID != input.Meta.OperationID {
			return OneShotRenewalStatus{}, CommercialReceipt{}, ErrSchema
		}
		return record.OneShotRenewalStatus, receipt, nil
	}
	sub, err := loadSubscription(ctx, tx, input.PredecessorID)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	record, err := readOneShotRecord(ctx, tx, sub)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	if record.State != "armed" || record.Revision != input.ExpectedRevision {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrConflict
	}
	now := c.clockNow()
	meta := input.Meta
	meta.ObservedAt = now
	receipt := CommercialReceipt{OperationID: meta.OperationID, ResourceKind: "subscription_one_shot", ResourceID: sub.ID, Revision: 2, CreatedAt: now}
	if err := insertCommercialOperation(ctx, tx, meta, "subscription.one_shot.disarm", receipt); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscription_one_shot_renewals SET state='disarmed',revision=2,reason='disarmed',disarm_operation_id=?,terminal_at=?,updated_at=? WHERE predecessor_id=? AND state='armed' AND revision=1`, meta.OperationID, formatCommercialTime(now), formatCommercialTime(now), sub.ID)
	if err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return OneShotRenewalStatus{}, CommercialReceipt{}, ErrUnavailable
	}
	return OneShotRenewalStatus{PredecessorID: sub.ID, State: "disarmed", Revision: 2, DueAt: record.DueAt, Reason: "disarmed", TerminalAt: &now}, receipt, nil
}

func (c *Commercial) clockNow() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}

func readOneShotRecord(ctx context.Context, tx *sql.Tx, sub Subscription) (oneShotRecord, error) {
	record := oneShotRecord{OneShotRenewalStatus: OneShotRenewalStatus{PredecessorID: sub.ID, State: "none", DueAt: sub.PeriodEndAt}}
	var armCount, disarmCount int
	var armID, disarmOperationID string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(operation_id),'') FROM financial_commercial_operations WHERE action='subscription.one_shot.arm' AND resource_id=?`, sub.ID).Scan(&armCount, &armID); err != nil {
		return oneShotRecord{}, ErrUnavailable
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(operation_id),'') FROM financial_commercial_operations WHERE action='subscription.one_shot.disarm' AND resource_id=?`, sub.ID).Scan(&disarmCount, &disarmOperationID); err != nil {
		return oneShotRecord{}, ErrUnavailable
	}
	var dueText, armedText, updatedText string
	var reason, successor, disarmID, terminal sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT arm_operation_id,execution_operation_id,armed_by_admin_id,predecessor_revision,due_at,state,revision,reason,successor_id,disarm_operation_id,armed_at,terminal_at,updated_at FROM financial_subscription_one_shot_renewals WHERE predecessor_id=?`, sub.ID).Scan(&record.armOperationID, &record.executionOperationID, &record.armedByAdminID, &record.predecessorRevision, &dueText, &record.State, &record.Revision, &reason, &successor, &disarmID, &armedText, &terminal, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		if armCount != 0 || disarmCount != 0 {
			return oneShotRecord{}, ErrSchema
		}
		return record, nil
	}
	if err != nil {
		return oneShotRecord{}, ErrUnavailable
	}
	if sub.Interval != "monthly" || sub.PeriodEndAt == nil || sub.Revision < record.predecessorRevision || !validCommercialText(record.armOperationID, 128) || !validCommercialText(record.executionOperationID, 128) || !validCommercialText(record.armedByAdminID, 256) || record.Revision < 1 || record.Revision > 2 {
		return oneShotRecord{}, ErrSchema
	}
	if armCount != 1 || armID != record.armOperationID || disarmCount > 1 {
		return oneShotRecord{}, ErrSchema
	}
	due, dueErr := parseSubscriptionEnd(dueText)
	armed, armedErr := parseSubscriptionStart(armedText)
	updated, updatedErr := parseSubscriptionStart(updatedText)
	if dueErr != nil || armedErr != nil || updatedErr != nil || !due.Equal(*sub.PeriodEndAt) || !armed.Before(due) || updated.Before(armed) {
		return oneShotRecord{}, ErrSchema
	}
	record.DueAt, record.armedAt = &due, armed
	if reason.Valid {
		record.Reason = reason.String
	}
	if successor.Valid {
		record.SuccessorID = successor.String
	}
	if disarmID.Valid {
		record.disarmOperationID = disarmID.String
	}
	if (disarmCount == 0) != (record.disarmOperationID == "") || disarmCount == 1 && disarmOperationID != record.disarmOperationID {
		return oneShotRecord{}, ErrSchema
	}
	if terminal.Valid {
		value, err := parseSubscriptionStart(terminal.String)
		if err != nil || value.Before(armed) || !value.Equal(updated) {
			return oneShotRecord{}, ErrSchema
		}
		record.TerminalAt = &value
	} else if !updated.Equal(armed) {
		return oneShotRecord{}, ErrSchema
	}
	if err := validateOneShotRecordShape(sub, record); err != nil {
		return oneShotRecord{}, err
	}
	if err := validateOneShotOperation(ctx, tx, record.armOperationID, "subscription.one_shot.arm", record.armedByAdminID, sub.ID, record.predecessorRevision, armed, 1); err != nil {
		return oneShotRecord{}, err
	}
	if record.disarmOperationID != "" {
		if err := validateOneShotDisarmOperation(ctx, tx, record.disarmOperationID, sub.ID, *record.TerminalAt); err != nil {
			return oneShotRecord{}, err
		}
	}
	if record.State == "succeeded" || record.State == "superseded" {
		var operationID string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM financial_subscription_renewals WHERE predecessor_id=? AND successor_id=?`, sub.ID, record.SuccessorID).Scan(&operationID); err != nil {
			return oneShotRecord{}, ErrSchema
		}
		if record.State == "succeeded" {
			if operationID != record.executionOperationID {
				return oneShotRecord{}, ErrSchema
			}
			digest, _ := DigestPayload(struct{ ID string }{sub.ID})
			var action, resourceKind, resourceID string
			var actor sql.NullString
			var storedDigest []byte
			if err := tx.QueryRowContext(ctx, `SELECT action,actor_admin_id,payload_digest,resource_kind,resource_id FROM financial_commercial_operations WHERE operation_id=?`, operationID).Scan(&action, &actor, &storedDigest, &resourceKind, &resourceID); err != nil || action != "subscription.renew" || actor.Valid || !equalBytes(storedDigest, digest[:]) || resourceKind != "subscription" || resourceID != record.SuccessorID {
				return oneShotRecord{}, ErrSchema
			}
		} else if operationID == record.executionOperationID {
			return oneShotRecord{}, ErrSchema
		}
	}
	return record, nil
}

func validateOneShotRecordShape(sub Subscription, record oneShotRecord) error {
	switch record.State {
	case "armed":
		if record.Revision != 1 || record.Reason != "" || record.SuccessorID != "" || record.disarmOperationID != "" || record.TerminalAt != nil || sub.Status == "cancelled" || sub.SuccessorID != "" {
			return ErrSchema
		}
	case "disarmed":
		if record.Revision != 2 || record.Reason != "disarmed" || record.SuccessorID != "" || record.disarmOperationID == "" || record.TerminalAt == nil {
			return ErrSchema
		}
	case "succeeded", "superseded":
		if record.Revision != 2 || record.TerminalAt == nil || record.SuccessorID == "" || record.SuccessorID != sub.SuccessorID || record.disarmOperationID != "" || record.State == "succeeded" && record.Reason != "" || record.State == "superseded" && record.Reason != "manual_renewal" {
			return ErrSchema
		}
	case "cancelled":
		if record.Revision != 2 || record.Reason != "predecessor_cancelled" || record.SuccessorID != "" || record.disarmOperationID != "" || record.TerminalAt == nil || sub.Status != "cancelled" {
			return ErrSchema
		}
	case "failed":
		if record.Revision != 2 || record.SuccessorID != "" || record.disarmOperationID != "" || record.TerminalAt == nil {
			return ErrSchema
		}
		switch record.Reason {
		case "commercial_disabled", "plan_unavailable", "insufficient_balance", "owner_unavailable", "period_unrepresentable":
		default:
			return ErrSchema
		}
	default:
		return ErrSchema
	}
	return nil
}

func validateOneShotOperation(ctx context.Context, tx *sql.Tx, operationID, expectedAction, expectedActor, predecessorID string, expectedRevision int64, expectedAt time.Time, receiptRevision int64) error {
	var action, actor, kind, resourceID, created string
	var digest []byte
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT action,COALESCE(actor_admin_id,''),payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations WHERE operation_id=?`, operationID).Scan(&action, &actor, &digest, &kind, &resourceID, &revision, &created); err != nil {
		return ErrSchema
	}
	expectedDigest := oneShotPayload(predecessorID, expectedRevision)
	if action != expectedAction || actor != expectedActor || !equalBytes(digest, expectedDigest[:]) || kind != "subscription_one_shot" || resourceID != predecessorID || revision != receiptRevision || created != formatCommercialTime(expectedAt) {
		return ErrSchema
	}
	return nil
}

func validateOneShotDisarmOperation(ctx context.Context, tx *sql.Tx, operationID, predecessorID string, at time.Time) error {
	var actor string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(actor_admin_id,'') FROM financial_commercial_operations WHERE operation_id=?`, operationID).Scan(&actor); err != nil || !validCommercialText(actor, 256) {
		return ErrSchema
	}
	return validateOneShotOperation(ctx, tx, operationID, "subscription.one_shot.disarm", actor, predecessorID, 1, at, 2)
}

func validateOneShotRenewals(ctx context.Context, tx *sql.Tx) error {
	var orphaned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_commercial_operations o LEFT JOIN financial_subscription_one_shot_renewals r ON r.predecessor_id=o.resource_id WHERE o.action IN ('subscription.one_shot.arm','subscription.one_shot.disarm') AND (o.resource_kind<>'subscription_one_shot' OR r.predecessor_id IS NULL OR (o.action='subscription.one_shot.arm' AND o.operation_id<>r.arm_operation_id) OR (o.action='subscription.one_shot.disarm' AND o.operation_id<>r.disarm_operation_id))`).Scan(&orphaned); err != nil {
		return ErrUnavailable
	}
	if orphaned != 0 {
		return ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT predecessor_id FROM financial_subscription_one_shot_renewals ORDER BY predecessor_id`)
	if err != nil {
		return ErrUnavailable
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ErrSchema
		}
		ids = append(ids, id)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	for _, id := range ids {
		sub, err := scanSubscription(tx.QueryRowContext(ctx, subscriptionSelect+` WHERE s.id=?`, id), time.Now().UTC())
		if err != nil {
			return ErrSchema
		}
		if _, err := readOneShotRecord(ctx, tx, sub); err != nil {
			return err
		}
	}
	return nil
}

func settleOneShotAfterManualRenewal(ctx context.Context, tx *sql.Tx, predecessorID, successorID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE financial_subscription_one_shot_renewals SET state='superseded',revision=2,reason='manual_renewal',successor_id=?,terminal_at=?,updated_at=? WHERE predecessor_id=? AND state='armed' AND revision=1`, successorID, formatCommercialTime(now), formatCommercialTime(now), predecessorID)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func settleOneShotAfterCancellation(ctx context.Context, tx *sql.Tx, predecessorID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE financial_subscription_one_shot_renewals SET state='cancelled',revision=2,reason='predecessor_cancelled',terminal_at=?,updated_at=? WHERE predecessor_id=? AND state='armed' AND revision=1`, formatCommercialTime(now), formatCommercialTime(now), predecessorID)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

// ProcessDueOneShotRenewals performs at most 16 independent, bounded candidate
// transactions. It does not turn a transient storage error into a business
// terminal result; a subsequent pass reconciles the same stable operation ID.
func (c *Commercial) ProcessDueOneShotRenewals(ctx context.Context, asOf time.Time) (int, error) {
	if c == nil || c.db == nil || ctx == nil || asOf.Location() != time.UTC {
		return 0, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT predecessor_id FROM financial_subscription_one_shot_renewals WHERE state='armed' AND due_at<=? ORDER BY due_at,predecessor_id LIMIT 16`, asOf.Format(subscriptionEndLayout))
	if err != nil {
		return 0, ErrUnavailable
	}
	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, ErrSchema
		}
		ids = append(ids, id)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return 0, ErrUnavailable
	}
	completed := 0
	for _, id := range ids {
		if ctx.Err() != nil {
			return completed, ErrUnavailable
		}
		changed, err := c.processOneShotRenewal(ctx, id)
		if err != nil {
			return completed, err
		}
		if changed {
			completed++
		}
	}
	return completed, nil
}

func (c *Commercial) processOneShotRenewal(ctx context.Context, id string) (bool, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return false, ErrUnavailable
	}
	defer tx.Rollback()
	var state, dueText, executionID string
	err = tx.QueryRowContext(ctx, `SELECT state,due_at,execution_operation_id FROM financial_subscription_one_shot_renewals WHERE predecessor_id=?`, id).Scan(&state, &dueText, &executionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	if state != "armed" {
		return false, nil
	}
	now := c.clockNow()
	due, err := parseSubscriptionEnd(dueText)
	if err != nil || !validCommercialText(executionID, 128) {
		return false, ErrSchema
	}
	if now.Before(due) {
		return false, nil
	}
	sub, err := loadSubscriptionAt(ctx, tx, id, now)
	if err != nil {
		return false, err
	}
	// Reconcile a prior uncertain commit or a competing explicit renewal before
	// any attempt to post money under the internal identity.
	if sub.SuccessorID != "" {
		var linkOperation string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id FROM financial_subscription_renewals WHERE predecessor_id=?`, id).Scan(&linkOperation); err != nil {
			return false, ErrSchema
		}
		state, reason := "superseded", "manual_renewal"
		if linkOperation == executionID {
			state, reason = "succeeded", ""
		}
		if err := markOneShotTerminal(ctx, tx, id, state, reason, sub.SuccessorID, now); err != nil {
			return false, err
		}
		if _, err := readOneShotRecord(ctx, tx, sub); err != nil {
			return false, err
		}
		return true, commitOneShotTx(tx)
	}
	if sub.Status == "cancelled" {
		if err := markOneShotTerminal(ctx, tx, id, "cancelled", "predecessor_cancelled", "", now); err != nil {
			return false, err
		}
		if _, err := readOneShotRecord(ctx, tx, sub); err != nil {
			return false, err
		}
		return true, commitOneShotTx(tx)
	}
	record, err := readOneShotRecord(ctx, tx, sub)
	if err != nil || record.State != "armed" || !record.DueAt.Equal(due) || record.executionOperationID != executionID {
		return false, ErrSchema
	}
	digest, _ := DigestPayload(struct{ ID string }{id})
	meta := WriteMeta{OperationID: executionID, PayloadDigest: digest, ObservedAt: now}
	if _, found, err := existingCommercialOperation(ctx, tx, meta, "subscription.renew"); err != nil {
		return false, err
	} else if found {
		return false, ErrSchema // a committed operation without its unique link is corrupt
	}
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		if !errors.Is(err, ErrConflict) {
			return false, err
		}
		return finishOneShotFailure(ctx, tx, sub, "commercial_disabled", now)
	}
	plan, err := loadPlan(ctx, tx, sub.PlanID)
	if errors.Is(err, ErrNotFound) || err == nil && (!plan.Enabled || plan.Interval != "monthly") {
		return finishOneShotFailure(ctx, tx, sub, "plan_unavailable", now)
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT one_shot_attempt`); err != nil {
		return false, ErrUnavailable
	}
	successor, _, err := c.renewSubscriptionTx(ctx, tx, meta, id, now)
	if err != nil {
		var reason string
		switch {
		case errors.Is(err, ErrInsufficient):
			reason = "insufficient_balance"
		case errors.Is(err, ErrNotFound):
			reason = "owner_unavailable"
		case errors.Is(err, ErrInvalid):
			reason = "period_unrepresentable"
		default:
			return false, err
		}
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO one_shot_attempt`); rollbackErr != nil {
			return false, ErrUnavailable
		}
		if _, releaseErr := tx.ExecContext(ctx, `RELEASE one_shot_attempt`); releaseErr != nil {
			return false, ErrUnavailable
		}
		return finishOneShotFailure(ctx, tx, sub, reason, now)
	}
	if _, err := tx.ExecContext(ctx, `RELEASE one_shot_attempt`); err != nil {
		return false, ErrUnavailable
	}
	if err := markOneShotTerminal(ctx, tx, id, "succeeded", "", successor.ID, now); err != nil {
		return false, err
	}
	return true, commitOneShotTx(tx)
}

func finishOneShotFailure(ctx context.Context, tx *sql.Tx, sub Subscription, reason string, now time.Time) (bool, error) {
	if err := markOneShotTerminal(ctx, tx, sub.ID, "failed", reason, "", now); err != nil {
		return false, err
	}
	if _, err := readOneShotRecord(ctx, tx, sub); err != nil {
		return false, err
	}
	return true, commitOneShotTx(tx)
}

func markOneShotTerminal(ctx context.Context, tx *sql.Tx, id, state, reason, successorID string, now time.Time) error {
	var reasonValue, successorValue any
	if reason != "" {
		reasonValue = reason
	}
	if successorID != "" {
		successorValue = successorID
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscription_one_shot_renewals SET state=?,revision=2,reason=?,successor_id=?,terminal_at=?,updated_at=? WHERE predecessor_id=? AND state='armed' AND revision=1`, state, reasonValue, successorValue, formatCommercialTime(now), formatCommercialTime(now), id)
	if err != nil {
		return ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func commitOneShotTx(tx *sql.Tx) error {
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}
