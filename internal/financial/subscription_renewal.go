package financial

// Independently authored for docs/subscription-manual-renewal-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const subscriptionRenewalsDDL = `CREATE TABLE IF NOT EXISTS financial_subscription_renewals (
	predecessor_id TEXT NOT NULL PRIMARY KEY REFERENCES financial_subscriptions(id) ON DELETE RESTRICT,
	successor_id TEXT NOT NULL UNIQUE REFERENCES financial_subscriptions(id) ON DELETE RESTRICT,
	operation_id TEXT NOT NULL UNIQUE REFERENCES financial_commercial_operations(operation_id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	CHECK(predecessor_id<>successor_id)
)`
const subscriptionRenewalsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_subscription_renewals_no_update BEFORE UPDATE ON financial_subscription_renewals BEGIN SELECT RAISE(ABORT,'financial subscription renewals are immutable'); END`
const subscriptionRenewalsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_subscription_renewals_no_delete BEFORE DELETE ON financial_subscription_renewals BEGIN SELECT RAISE(ABORT,'financial subscription renewals are immutable'); END`

func commercialOperationLegacySchema(ctx context.Context, tx *sql.Tx) (bool, error) {
	var actual string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='financial_commercial_operations'`).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	if normalize(actual) == normalize(storedDDL(commercialOperationsDDL)) {
		return false, nil
	}
	if normalize(actual) != normalize(storedDDL(commercialOperationsLegacyDDL)) {
		return false, ErrSchema
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='financial_commercial_operations' AND type<>'table'`).Scan(&count); err != nil || count != 3 {
		return false, ErrSchema // automatic PK index and two immutable triggers
	}
	for name, ddl := range map[string]string{
		"financial_commercial_operations_no_update": commercialOperationsNoUpdateDDL,
		"financial_commercial_operations_no_delete": commercialOperationsNoDeleteDDL,
	} {
		var stored string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&stored); err != nil || normalize(stored) != normalize(storedDDL(ddl)) {
			return false, ErrSchema
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='financial_subscription_renewals'`).Scan(&count); err != nil || count != 0 {
		return false, ErrSchema
	}
	return true, nil
}

func migrateLegacyCommercialOperations(ctx context.Context, tx *sql.Tx) error {
	// No pre-renewal table references this operation table. Keep the stage inside
	// this transaction so any failed copy restores the old table and triggers.
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE commercial_renewal_stage AS SELECT * FROM financial_commercial_operations`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE financial_commercial_operations`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, commercialOperationsDDL); err != nil {
		return ErrSchema
	}
	columns := `operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at`
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_commercial_operations(`+columns+`) SELECT `+columns+` FROM commercial_renewal_stage`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE commercial_renewal_stage`); err != nil {
		return ErrSchema
	}
	for _, ddl := range []string{commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return ErrSchema
		}
	}
	return nil
}

type RenewSubscription struct {
	Meta WriteMeta
	ID   string
}

func (c *Commercial) RenewSubscription(ctx context.Context, input RenewSubscription) (Subscription, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.ID, 256) {
		return Subscription{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "subscription.renew"); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadSubscription(ctx, tx, receipt.ResourceID)
		if err != nil || item.PredecessorID != input.ID {
			return Subscription{}, CommercialReceipt{}, ErrSchema
		}
		return item, receipt, nil
	}
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	clock := time.Now
	if c.now != nil {
		clock = c.now
	}
	now := clock().UTC()
	predecessor, err := loadSubscriptionAt(ctx, tx, input.ID, now)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if predecessor.Interval != "monthly" || predecessor.Status != "expired" || predecessor.PeriodEndAt == nil || predecessor.SuccessorID != "" {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	plan, err := loadPlan(ctx, tx, predecessor.PlanID)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if !plan.Enabled || plan.Interval != "monthly" {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	end, err := monthlyPeriodEnd(now)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrInvalid
	}
	owner, err := subscriptionOwner(ctx, tx, predecessor)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	ledger := NewLedger(c.db)
	accountID, _, err := ledger.EnsureAccountTx(ctx, tx, owner, plan.Currency, now)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	available, err := ledger.BalanceTx(ctx, tx, accountID)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if available < plan.PriceMicro {
		return Subscription{}, CommercialReceipt{}, ErrInsufficient
	}
	id, err := randomID("subscription")
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscriptions SET status='expired',revision=revision+1 WHERE id=? AND status='active' AND interval='monthly' AND period_end_at<=? AND revision<?`, predecessor.ID, now.Format(subscriptionEndLayout), subscriptionRevisionMax)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed != 0 && changed != 1 {
		return Subscription{}, CommercialReceipt{}, ErrSchema
	}
	// If the worker already persisted expiry, no update is needed; otherwise a
	// due active row must be the one conditionally expired above.
	if changed == 0 {
		var storedStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM financial_subscriptions WHERE id=?`, predecessor.ID).Scan(&storedStatus); err != nil {
			return Subscription{}, CommercialReceipt{}, ErrUnavailable
		}
		if storedStatus != "expired" {
			return Subscription{}, CommercialReceipt{}, ErrConflict
		}
	}
	posted, err := ledger.PostTx(ctx, tx, Post{OperationID: input.Meta.OperationID, Action: "subscription_purchase", ActorAdminID: input.Meta.ActorAdminID, ResourceKind: "subscription", ResourceID: id, ObservedAt: now, RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: plan.Currency, Kind: EntrySubscriptionCharge, AmountMicro: -plan.PriceMicro, ResourceKind: "subscription", ResourceID: id}, {Owner: owner, Currency: plan.Currency, Kind: EntrySubscriptionCredit, AmountMicro: plan.CreditMicro, ResourceKind: "subscription", ResourceID: id}}})
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if len(posted) != 2 || posted[0].AccountID != accountID || posted[1].AccountID != accountID {
		return Subscription{}, CommercialReceipt{}, ErrSchema
	}
	item := Subscription{ID: id, AccountID: accountID, PlanID: plan.ID, PlanRevision: plan.Revision, PriceMicro: plan.PriceMicro, CreditMicro: plan.CreditMicro, Currency: plan.Currency, Interval: "monthly", Status: "active", StartedAt: now, PeriodEndAt: &end, Revision: 1, PredecessorID: predecessor.ID}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, item.ID, item.AccountID, item.PlanID, item.PlanRevision, item.PriceMicro, item.CreditMicro, item.Currency, item.Interval, item.Status, formatCommercialTime(now), end.Format(subscriptionEndLayout)); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	meta := input.Meta
	meta.ObservedAt = now
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "subscription", ResourceID: id, Revision: 1, CreatedAt: now}
	if err := insertCommercialOperation(ctx, tx, meta, "subscription.renew", receipt); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscription_renewals(predecessor_id,successor_id,operation_id,created_at) VALUES(?,?,?,?)`, predecessor.ID, item.ID, input.Meta.OperationID, formatCommercialTime(now)); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func subscriptionOwner(ctx context.Context, tx *sql.Tx, item Subscription) (Owner, error) {
	var owner Owner
	var kind, currency string
	var key sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT owner_kind,employee_id,key_id,resource_kind,resource_id,currency FROM financial_accounts WHERE id=?`, item.AccountID).Scan(&kind, &owner.EmployeeID, &key, &owner.ResourceKind, &owner.ResourceID, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, ErrSchema
	}
	if err != nil {
		return Owner{}, ErrUnavailable
	}
	owner.Kind = OwnerKind(kind)
	if key.Valid {
		owner.KeyID = key.String
	}
	if currency != item.Currency || !validOwnerShape(owner) {
		return Owner{}, ErrSchema
	}
	return owner, nil
}

func validateRenewalLinks(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) error {
	rows, err := q.QueryContext(ctx, `SELECT r.predecessor_id,r.successor_id,r.operation_id,r.created_at,p.plan_id,s.plan_id,p.interval,s.interval,p.status,p.period_end_at,s.started_at,pa.owner_key,sa.owner_key,o.action,o.resource_kind,o.resource_id,o.created_at FROM financial_subscription_renewals r JOIN financial_subscriptions p ON p.id=r.predecessor_id JOIN financial_subscriptions s ON s.id=r.successor_id JOIN financial_accounts pa ON pa.id=p.account_id JOIN financial_accounts sa ON sa.id=s.account_id JOIN financial_commercial_operations o ON o.operation_id=r.operation_id`)
	if err != nil {
		return ErrUnavailable
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		seen++
		var predecessor, successor, operation, created, oldPlan, newPlan, oldInterval, newInterval, oldStatus, oldEnd, newStart, oldOwner, newOwner, action, resourceKind, resourceID, operationTime string
		if err := rows.Scan(&predecessor, &successor, &operation, &created, &oldPlan, &newPlan, &oldInterval, &newInterval, &oldStatus, &oldEnd, &newStart, &oldOwner, &newOwner, &action, &resourceKind, &resourceID, &operationTime); err != nil {
			return ErrSchema
		}
		end, endErr := parseSubscriptionEnd(oldEnd)
		start, startErr := parseSubscriptionStart(newStart)
		if predecessor == successor || operation == "" || created != newStart || operationTime != created || oldPlan != newPlan || oldInterval != "monthly" || newInterval != "monthly" || oldStatus != "expired" || oldOwner != newOwner || action != "subscription.renew" || resourceKind != "subscription" || resourceID != successor || endErr != nil || startErr != nil || end.After(start) {
			return ErrSchema
		}
	}
	if err := rows.Err(); err != nil {
		return ErrUnavailable
	}
	if err := rows.Close(); err != nil {
		return ErrUnavailable
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_subscription_renewals`).Scan(&total); err != nil {
		return ErrUnavailable
	}
	if seen != total {
		return ErrSchema
	}
	return nil
}
