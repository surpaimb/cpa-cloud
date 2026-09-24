package financial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type CommercialReceipt struct {
	OperationID  string
	ResourceKind string
	ResourceID   string
	Revision     int64
	CreatedAt    time.Time
	Replay       bool
}

type Plan struct {
	ID, Name, Currency, Interval      string
	PriceMicro, CreditMicro, Revision int64
	Enabled                           bool
	CreatedAt, UpdatedAt              time.Time
}
type Connector struct {
	ID, Name             string
	Enabled              bool
	Revision             int64
	CreatedAt, UpdatedAt time.Time
}
type TopUp struct {
	ID, PaymentID, ConnectorID, AccountID, ExternalReference, Currency, Status string
	AmountMicro, RefundedMicro, Revision                                       int64
	CreatedAt                                                                  time.Time
	PaidAt                                                                     *time.Time
	PaidEntryID                                                                string
}
type Subscription struct {
	ID, AccountID, PlanID, Currency, Interval, Status string
	PlanRevision, PriceMicro, CreditMicro, Revision   int64
	StartedAt                                         time.Time
	CancelledAt                                       *time.Time
}
type RedemptionCode struct {
	ID, Currency               string
	AmountMicro, MaxUses, Uses int64
	ExpiresAt                  *time.Time
	Enabled                    bool
	CreatedAt                  time.Time
}
type Refund struct {
	ID, PaymentID, EntryID string
	AmountMicro            int64
	CreatedAt              time.Time
}

type WriteMeta struct {
	OperationID, ActorAdminID string
	PayloadDigest             [32]byte
	ObservedAt                time.Time
}
type CreatePlan struct {
	Meta                     WriteMeta
	Name, Currency, Interval string
	PriceMicro, CreditMicro  int64
	Enabled                  bool
}
type UpdatePlan struct {
	Meta                                      WriteMeta
	ID, Name, Currency, Interval              string
	PriceMicro, CreditMicro, ExpectedRevision int64
	Enabled                                   bool
}
type CreateConnector struct {
	Meta             WriteMeta
	Name             string
	SecretCiphertext []byte
	Enabled          bool
}
type UpdateConnector struct {
	Meta             WriteMeta
	ID, Name         string
	SecretCiphertext []byte
	ExpectedRevision int64
	Enabled          bool
}
type CreateTopUp struct {
	Meta                  WriteMeta
	Owner                 Owner
	ConnectorID, Currency string
	AmountMicro           int64
}
type PurchaseSubscription struct {
	Meta   WriteMeta
	Owner  Owner
	PlanID string
}
type CancelSubscription struct {
	Meta             WriteMeta
	ID               string
	ExpectedRevision int64
}
type CreateRedemptionCode struct {
	Meta                 WriteMeta
	CodeDigest           [32]byte
	Currency             string
	AmountMicro, MaxUses int64
	ExpiresAt            *time.Time
}
type RedeemCode struct {
	Meta       WriteMeta
	CodeDigest [32]byte
	Owner      Owner
}
type ApplyPayment struct {
	ConnectorID, EventID, PaymentID, ExternalReference, Currency string
	AmountMicro                                                  int64
	PayloadDigest                                                [32]byte
	SignedAt, ObservedAt                                         time.Time
}
type CreateRefund struct {
	Meta        WriteMeta
	PaymentID   string
	AmountMicro int64
}

func DigestPayload(value any) ([32]byte, error) {
	encoded, err := jsonMarshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func (c *Commercial) Settings(ctx context.Context) (bool, int64, error) {
	if c == nil || c.db == nil || ctx == nil {
		return false, 0, ErrInvalid
	}
	var enabled int
	var revision int64
	if err := c.db.QueryRowContext(ctx, `SELECT enabled,revision FROM financial_settings WHERE singleton=1`).Scan(&enabled, &revision); err != nil {
		return false, 0, ErrUnavailable
	}
	return enabled == 1, revision, nil
}

func (c *Commercial) ListPlans(ctx context.Context, after string, limit int) ([]Plan, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at FROM financial_plans WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]Plan, 0)
	for rows.Next() {
		var item Plan
		var enabled int
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &item.Currency, &item.PriceMicro, &item.CreditMicro, &item.Interval, &enabled, &item.Revision, &created, &updated); err != nil {
			return nil, ErrUnavailable
		}
		item.Enabled = enabled == 1
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrSchema
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, ErrSchema
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (c *Commercial) ListConnectors(ctx context.Context, after string, limit int) ([]Connector, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id,name,enabled,revision,created_at,updated_at FROM financial_payment_connectors WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]Connector, 0)
	for rows.Next() {
		var item Connector
		var enabled int
		var created, updated string
		if err := rows.Scan(&item.ID, &item.Name, &enabled, &item.Revision, &created, &updated); err != nil {
			return nil, ErrUnavailable
		}
		item.Enabled = enabled == 1
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrSchema
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, ErrSchema
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (c *Commercial) ListTopUps(ctx context.Context, after string, limit int) ([]TopUp, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT t.id,p.id,p.connector_id,p.account_id,p.external_reference,p.amount_micro,p.currency,p.status,COALESCE(p.paid_entry_id,''),p.created_at,p.paid_at,p.refunded_micro,p.revision FROM financial_topups t JOIN financial_payments p ON p.id=t.payment_id WHERE t.id>? ORDER BY t.id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]TopUp, 0)
	for rows.Next() {
		item, err := scanTopUp(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (c *Commercial) ListSubscriptions(ctx context.Context, after string, limit int) ([]Subscription, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,cancelled_at,revision FROM financial_subscriptions WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]Subscription, 0)
	for rows.Next() {
		var item Subscription
		var started string
		var cancelled sql.NullString
		if err := rows.Scan(&item.ID, &item.AccountID, &item.PlanID, &item.PlanRevision, &item.PriceMicro, &item.CreditMicro, &item.Currency, &item.Interval, &item.Status, &started, &cancelled, &item.Revision); err != nil {
			return nil, ErrUnavailable
		}
		item.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return nil, ErrSchema
		}
		if cancelled.Valid {
			parsed, e := time.Parse(time.RFC3339Nano, cancelled.String)
			if e != nil {
				return nil, ErrSchema
			}
			item.CancelledAt = &parsed
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (c *Commercial) ListCodes(ctx context.Context, after string, limit int) ([]RedemptionCode, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id,amount_micro,currency,max_uses,uses,expires_at,enabled,created_at FROM financial_redemption_codes WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]RedemptionCode, 0)
	for rows.Next() {
		var item RedemptionCode
		var expires sql.NullString
		var enabled int
		var created string
		if err := rows.Scan(&item.ID, &item.AmountMicro, &item.Currency, &item.MaxUses, &item.Uses, &expires, &enabled, &created); err != nil {
			return nil, ErrUnavailable
		}
		item.Enabled = enabled == 1
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrSchema
		}
		if expires.Valid {
			parsed, e := time.Parse(time.RFC3339Nano, expires.String)
			if e != nil {
				return nil, ErrSchema
			}
			item.ExpiresAt = &parsed
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}
func (c *Commercial) ListRefunds(ctx context.Context, after string, limit int) ([]Refund, error) {
	if c == nil || c.db == nil || ctx == nil || limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	rows, err := c.db.QueryContext(ctx, `SELECT id,payment_id,entry_id,amount_micro,created_at FROM financial_refunds WHERE id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	items := make([]Refund, 0)
	for rows.Next() {
		var item Refund
		var created string
		if err := rows.Scan(&item.ID, &item.PaymentID, &item.EntryID, &item.AmountMicro, &created); err != nil {
			return nil, ErrUnavailable
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrSchema
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}

func (c *Commercial) SetEnabled(ctx context.Context, meta WriteMeta, expected int64, enabled bool) (CommercialReceipt, error) {
	if !validMeta(meta) || expected < 1 {
		return CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, meta, "settings.update"); err != nil || found {
		return receipt, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM financial_settings WHERE singleton=1`).Scan(&revision); err != nil {
		return CommercialReceipt{}, ErrUnavailable
	}
	if revision != expected {
		return CommercialReceipt{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_settings SET enabled=?,revision=revision+1,updated_at=? WHERE singleton=1 AND revision=?`, boolInt(enabled), formatCommercialTime(meta.ObservedAt), expected)
	if err != nil {
		return CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return CommercialReceipt{}, ErrConflict
	}
	receipt := CommercialReceipt{OperationID: meta.OperationID, ResourceKind: "settings", ResourceID: "singleton", Revision: expected + 1, CreatedAt: meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, meta, "settings.update", receipt); err != nil {
		return CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommercialReceipt{}, ErrUnavailable
	}
	return receipt, nil
}

func (c *Commercial) CreatePlan(ctx context.Context, input CreatePlan) (Plan, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.Name, 128) || !validCurrency(input.Currency) || input.PriceMicro < 1 || input.CreditMicro < 1 || input.Interval != "one_time" && input.Interval != "monthly" {
		return Plan{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "plan.create"); err != nil {
		return Plan{}, CommercialReceipt{}, err
	} else if found {
		plan, err := loadPlan(ctx, tx, receipt.ResourceID)
		return plan, receipt, err
	}
	id, err := randomID("plan")
	if err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	item := Plan{ID: id, Name: input.Name, Currency: input.Currency, Interval: input.Interval, PriceMicro: input.PriceMicro, CreditMicro: input.CreditMicro, Enabled: input.Enabled, Revision: 1, CreatedAt: input.Meta.ObservedAt, UpdatedAt: input.Meta.ObservedAt}
	_, err = tx.ExecContext(ctx, `INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.Currency, item.PriceMicro, item.CreditMicro, item.Interval, boolInt(item.Enabled), item.Revision, formatCommercialTime(item.CreatedAt), formatCommercialTime(item.UpdatedAt))
	if err != nil {
		return Plan{}, CommercialReceipt{}, ErrConflict
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "plan", ResourceID: item.ID, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "plan.create", receipt); err != nil {
		return Plan{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) UpdatePlan(ctx context.Context, input UpdatePlan) (Plan, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.ID, 256) || !validCommercialText(input.Name, 128) || !validCurrency(input.Currency) || input.PriceMicro < 1 || input.CreditMicro < 1 || input.ExpectedRevision < 1 || input.Interval != "one_time" && input.Interval != "monthly" {
		return Plan{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "plan.update"); err != nil {
		return Plan{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadPlan(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	stored, err := loadPlan(ctx, tx, input.ID)
	if err != nil {
		return Plan{}, CommercialReceipt{}, err
	}
	if stored.Revision != input.ExpectedRevision || stored.Currency != input.Currency {
		return Plan{}, CommercialReceipt{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_plans SET name=?,price_micro=?,credit_micro=?,interval=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.Name, input.PriceMicro, input.CreditMicro, input.Interval, boolInt(input.Enabled), formatCommercialTime(input.Meta.ObservedAt), input.ID, input.ExpectedRevision)
	if err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Plan{}, CommercialReceipt{}, ErrConflict
	}
	item := Plan{ID: input.ID, Name: input.Name, Currency: input.Currency, PriceMicro: input.PriceMicro, CreditMicro: input.CreditMicro, Interval: input.Interval, Enabled: input.Enabled, Revision: input.ExpectedRevision + 1, CreatedAt: stored.CreatedAt, UpdatedAt: input.Meta.ObservedAt}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "plan", ResourceID: item.ID, Revision: item.Revision, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "plan.update", receipt); err != nil {
		return Plan{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) CreateConnector(ctx context.Context, input CreateConnector) (Connector, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.Name, 128) || len(input.SecretCiphertext) < 1 {
		return Connector{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "connector.create"); err != nil {
		return Connector{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadConnector(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	id, err := randomID("connector")
	if err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	item := Connector{ID: id, Name: input.Name, Enabled: input.Enabled, Revision: 1, CreatedAt: input.Meta.ObservedAt, UpdatedAt: input.Meta.ObservedAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_payment_connectors(id,name,secret_ciphertext,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, item.ID, item.Name, input.SecretCiphertext, boolInt(item.Enabled), 1, formatCommercialTime(item.CreatedAt), formatCommercialTime(item.UpdatedAt)); err != nil {
		return Connector{}, CommercialReceipt{}, ErrConflict
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "connector", ResourceID: item.ID, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "connector.create", receipt); err != nil {
		return Connector{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) UpdateConnector(ctx context.Context, input UpdateConnector) (Connector, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.ID, 256) || !validCommercialText(input.Name, 128) || input.ExpectedRevision < 1 {
		return Connector{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "connector.update"); err != nil {
		return Connector{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadConnector(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	stored, err := loadConnector(ctx, tx, input.ID)
	if err != nil {
		return Connector{}, CommercialReceipt{}, err
	}
	if stored.Revision != input.ExpectedRevision {
		return Connector{}, CommercialReceipt{}, ErrConflict
	}
	var result sql.Result
	if len(input.SecretCiphertext) == 0 {
		result, err = tx.ExecContext(ctx, `UPDATE financial_payment_connectors SET name=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.Name, boolInt(input.Enabled), formatCommercialTime(input.Meta.ObservedAt), input.ID, input.ExpectedRevision)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE financial_payment_connectors SET name=?,secret_ciphertext=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, input.Name, input.SecretCiphertext, boolInt(input.Enabled), formatCommercialTime(input.Meta.ObservedAt), input.ID, input.ExpectedRevision)
	}
	if err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Connector{}, CommercialReceipt{}, ErrConflict
	}
	item := Connector{ID: input.ID, Name: input.Name, Enabled: input.Enabled, Revision: input.ExpectedRevision + 1, CreatedAt: stored.CreatedAt, UpdatedAt: input.Meta.ObservedAt}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "connector", ResourceID: item.ID, Revision: item.Revision, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "connector.update", receipt); err != nil {
		return Connector{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Connector{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) CreateTopUp(ctx context.Context, input CreateTopUp) (TopUp, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.ConnectorID, 256) || !validCurrency(input.Currency) || input.AmountMicro < 1 {
		return TopUp{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return TopUp{}, CommercialReceipt{}, err
	}
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "topup.create"); err != nil {
		return TopUp{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadTopUpByID(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	var connectorEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM financial_payment_connectors WHERE id=?`, input.ConnectorID).Scan(&connectorEnabled); errors.Is(err, sql.ErrNoRows) {
		return TopUp{}, CommercialReceipt{}, ErrNotFound
	} else if err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	} else if connectorEnabled != 1 {
		return TopUp{}, CommercialReceipt{}, ErrConflict
	}
	accountID, _, err := NewLedger(c.db).EnsureAccountTx(ctx, tx, input.Owner, input.Currency, input.Meta.ObservedAt)
	if err != nil {
		return TopUp{}, CommercialReceipt{}, err
	}
	paymentID, err := randomID("payment")
	if err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	topupID, err := randomID("topup")
	if err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	external, err := randomID("payref")
	if err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_payments(id,connector_id,account_id,external_reference,amount_micro,currency,status,created_at,refunded_micro,revision) VALUES(?,?,?,?,?,?,'pending',?,0,1)`, paymentID, input.ConnectorID, accountID, external, input.AmountMicro, input.Currency, formatCommercialTime(input.Meta.ObservedAt)); err != nil {
		return TopUp{}, CommercialReceipt{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_topups(id,payment_id,created_at) VALUES(?,?,?)`, topupID, paymentID, formatCommercialTime(input.Meta.ObservedAt)); err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "topup", ResourceID: topupID, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "topup.create", receipt); err != nil {
		return TopUp{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return TopUp{}, CommercialReceipt{}, ErrUnavailable
	}
	return TopUp{ID: topupID, PaymentID: paymentID, ConnectorID: input.ConnectorID, AccountID: accountID, ExternalReference: external, Currency: input.Currency, Status: "pending", AmountMicro: input.AmountMicro, Revision: 1, CreatedAt: input.Meta.ObservedAt}, receipt, nil
}

func (c *Commercial) PurchaseSubscription(ctx context.Context, input PurchaseSubscription) (Subscription, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.PlanID, 256) {
		return Subscription{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "subscription.create"); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadSubscription(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	plan, err := loadPlan(ctx, tx, input.PlanID)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if !plan.Enabled {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	id, err := randomID("subscription")
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	ledger := NewLedger(c.db)
	accountID, _, err := ledger.EnsureAccountTx(ctx, tx, input.Owner, plan.Currency, input.Meta.ObservedAt)
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
	posted, err := ledger.PostTx(ctx, tx, Post{OperationID: input.Meta.OperationID, Action: "subscription_purchase", ActorAdminID: input.Meta.ActorAdminID, ResourceKind: "subscription", ResourceID: id, ObservedAt: input.Meta.ObservedAt, RequireNonNegative: true, Entries: []EntryInput{{Owner: input.Owner, Currency: plan.Currency, Kind: EntrySubscriptionCharge, AmountMicro: -plan.PriceMicro, ResourceKind: "subscription", ResourceID: id}, {Owner: input.Owner, Currency: plan.Currency, Kind: EntrySubscriptionCredit, AmountMicro: plan.CreditMicro, ResourceKind: "subscription", ResourceID: id}}})
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if len(posted) != 2 {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	item := Subscription{ID: id, AccountID: posted[0].AccountID, PlanID: plan.ID, PlanRevision: plan.Revision, PriceMicro: plan.PriceMicro, CreditMicro: plan.CreditMicro, Currency: plan.Currency, Interval: plan.Interval, Status: "active", StartedAt: input.Meta.ObservedAt, Revision: 1}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,1)`, item.ID, item.AccountID, item.PlanID, item.PlanRevision, item.PriceMicro, item.CreditMicro, item.Currency, item.Interval, item.Status, formatCommercialTime(item.StartedAt)); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "subscription", ResourceID: id, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "subscription.create", receipt); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) CancelSubscription(ctx context.Context, input CancelSubscription) (Subscription, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.ID, 256) || input.ExpectedRevision < 1 {
		return Subscription{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "subscription.cancel"); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadSubscription(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	item, err := loadSubscription(ctx, tx, input.ID)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if item.Status != "active" || item.Revision != input.ExpectedRevision {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_subscriptions SET status='cancelled',cancelled_at=?,revision=revision+1 WHERE id=? AND status='active' AND revision=?`, formatCommercialTime(input.Meta.ObservedAt), input.ID, input.ExpectedRevision)
	if err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Subscription{}, CommercialReceipt{}, ErrConflict
	}
	item.Status = "cancelled"
	item.Revision++
	cancelled := input.Meta.ObservedAt
	item.CancelledAt = &cancelled
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "subscription", ResourceID: item.ID, Revision: item.Revision, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "subscription.cancel", receipt); err != nil {
		return Subscription{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Subscription{}, CommercialReceipt{}, ErrUnavailable
	}
	return item, receipt, nil
}

func (c *Commercial) CreateCode(ctx context.Context, input CreateRedemptionCode) (RedemptionCode, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCurrency(input.Currency) || input.AmountMicro < 1 || input.MaxUses < 1 || input.ExpiresAt != nil && (!input.ExpiresAt.After(input.Meta.ObservedAt) || input.ExpiresAt.Location() != time.UTC) {
		return RedemptionCode{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RedemptionCode{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "redemption_code.create"); err != nil {
		return RedemptionCode{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadCode(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	id, err := randomID("redeem")
	if err != nil {
		return RedemptionCode{}, CommercialReceipt{}, ErrUnavailable
	}
	expires := any(nil)
	if input.ExpiresAt != nil {
		expires = formatCommercialTime(*input.ExpiresAt)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_redemption_codes(id,code_digest,amount_micro,currency,max_uses,uses,expires_at,enabled,created_at) VALUES(?,?,?,?,?,0,?,1,?)`, id, input.CodeDigest[:], input.AmountMicro, input.Currency, input.MaxUses, expires, formatCommercialTime(input.Meta.ObservedAt)); err != nil {
		return RedemptionCode{}, CommercialReceipt{}, ErrConflict
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "redemption_code", ResourceID: id, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "redemption_code.create", receipt); err != nil {
		return RedemptionCode{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return RedemptionCode{}, CommercialReceipt{}, ErrUnavailable
	}
	return RedemptionCode{ID: id, Currency: input.Currency, AmountMicro: input.AmountMicro, MaxUses: input.MaxUses, Enabled: true, ExpiresAt: input.ExpiresAt, CreatedAt: input.Meta.ObservedAt}, receipt, nil
}

func (c *Commercial) Redeem(ctx context.Context, input RedeemCode) (Entry, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) {
		return Entry{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return Entry{}, CommercialReceipt{}, err
	}
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "redemption.redeem"); err != nil {
		return Entry{}, CommercialReceipt{}, err
	} else if found {
		entries, err := loadOperationEntries(ctx, tx, input.Meta.OperationID)
		if err != nil || len(entries) != 1 {
			return Entry{}, CommercialReceipt{}, ErrUnavailable
		}
		return entries[0], receipt, nil
	}
	var codeID, currency, expires string
	var amount, maxUses, uses int64
	var enabled int
	var expiresNull sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT id,amount_micro,currency,max_uses,uses,expires_at,enabled FROM financial_redemption_codes WHERE code_digest=?`, input.CodeDigest[:]).Scan(&codeID, &amount, &currency, &maxUses, &uses, &expiresNull, &enabled); errors.Is(err, sql.ErrNoRows) {
		return Entry{}, CommercialReceipt{}, ErrNotFound
	} else if err != nil {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	expires = expiresNull.String
	if enabled != 1 || uses >= maxUses {
		return Entry{}, CommercialReceipt{}, ErrConflict
	}
	if expires != "" {
		parsed, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return Entry{}, CommercialReceipt{}, ErrSchema
		}
		if !parsed.After(input.Meta.ObservedAt) {
			return Entry{}, CommercialReceipt{}, ErrConflict
		}
	}
	redemptionID, err := randomID("redemption")
	if err != nil {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	posted, err := NewLedger(c.db).PostTx(ctx, tx, Post{OperationID: input.Meta.OperationID, Action: "redemption", ActorAdminID: input.Meta.ActorAdminID, ResourceKind: "redemption", ResourceID: redemptionID, ObservedAt: input.Meta.ObservedAt, Entries: []EntryInput{{Owner: input.Owner, Currency: currency, Kind: EntryRedemption, AmountMicro: amount, ResourceKind: "redemption", ResourceID: redemptionID}}})
	if err != nil {
		return Entry{}, CommercialReceipt{}, err
	}
	if len(posted) != 1 {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_redemption_codes SET uses=uses+1 WHERE id=? AND uses=? AND enabled=1`, codeID, uses)
	if err != nil {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Entry{}, CommercialReceipt{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_redemptions(id,code_id,account_id,entry_id,created_at) VALUES(?,?,?,?,?)`, redemptionID, codeID, posted[0].AccountID, posted[0].ID, formatCommercialTime(input.Meta.ObservedAt)); err != nil {
		return Entry{}, CommercialReceipt{}, ErrConflict
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "redemption", ResourceID: redemptionID, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "redemption.redeem", receipt); err != nil {
		return Entry{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Entry{}, CommercialReceipt{}, ErrUnavailable
	}
	return posted[0], receipt, nil
}

func (c *Commercial) ApplyPaid(ctx context.Context, input ApplyPayment) (TopUp, error) {
	if c == nil || c.db == nil || ctx == nil || !validCommercialText(input.ConnectorID, 256) || !validCommercialText(input.EventID, 256) || !validCommercialText(input.PaymentID, 256) || !validCommercialText(input.ExternalReference, 256) || !validCurrency(input.Currency) || input.AmountMicro < 1 || input.SignedAt.Location() != time.UTC || input.ObservedAt.Location() != time.UTC {
		return TopUp{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return TopUp{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return TopUp{}, err
	}
	var storedDigest []byte
	var storedPayment, signed string
	err = tx.QueryRowContext(ctx, `SELECT payload_digest,payment_id,signed_at FROM financial_webhook_events WHERE connector_id=? AND event_id=?`, input.ConnectorID, input.EventID).Scan(&storedDigest, &storedPayment, &signed)
	if err == nil {
		if !equalBytes(storedDigest, input.PayloadDigest[:]) || storedPayment != input.PaymentID || signed != formatCommercialTime(input.SignedAt) {
			return TopUp{}, ErrConflict
		}
		item, err := loadTopUpByPayment(ctx, tx, input.PaymentID)
		return item, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return TopUp{}, ErrUnavailable
	}
	item, err := loadTopUpByPayment(ctx, tx, input.PaymentID)
	if err != nil {
		return TopUp{}, err
	}
	if item.ConnectorID != input.ConnectorID || item.ExternalReference != input.ExternalReference || item.AmountMicro != input.AmountMicro || item.Currency != input.Currency {
		return TopUp{}, ErrConflict
	}
	if item.Status != "pending" {
		return TopUp{}, ErrConflict
	}
	owner, err := ownerForAccount(ctx, tx, item.AccountID)
	if err != nil {
		return TopUp{}, err
	}
	operationID := "webhook:" + input.ConnectorID + ":" + input.EventID
	posted, err := NewLedger(c.db).PostTx(ctx, tx, Post{OperationID: operationID, Action: "payment_callback", ResourceKind: "topup", ResourceID: item.ID, ObservedAt: input.ObservedAt, Entries: []EntryInput{{Owner: owner, Currency: item.Currency, Kind: EntryTopUp, AmountMicro: item.AmountMicro, ResourceKind: "topup", ResourceID: item.ID}}})
	if err != nil {
		return TopUp{}, err
	}
	if len(posted) != 1 {
		return TopUp{}, ErrUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_payments SET status='paid',paid_entry_id=?,paid_at=?,revision=revision+1 WHERE id=? AND status='pending' AND revision=?`, posted[0].ID, formatCommercialTime(input.ObservedAt), item.PaymentID, item.Revision)
	if err != nil {
		return TopUp{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return TopUp{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_webhook_events(connector_id,event_id,payment_id,payload_digest,signed_at,processed_at) VALUES(?,?,?,?,?,?)`, input.ConnectorID, input.EventID, input.PaymentID, input.PayloadDigest[:], formatCommercialTime(input.SignedAt), formatCommercialTime(input.ObservedAt)); err != nil {
		return TopUp{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return TopUp{}, ErrUnavailable
	}
	item.Status = "paid"
	item.PaidEntryID = posted[0].ID
	item.Revision++
	paid := input.ObservedAt
	item.PaidAt = &paid
	return item, nil
}

func (c *Commercial) RefundPayment(ctx context.Context, input CreateRefund) (Refund, CommercialReceipt, error) {
	if c == nil || c.db == nil || ctx == nil || !validMeta(input.Meta) || !validCommercialText(input.PaymentID, 256) || input.AmountMicro < 1 {
		return Refund{}, CommercialReceipt{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := requireCommercialEnabled(ctx, tx); err != nil {
		return Refund{}, CommercialReceipt{}, err
	}
	if receipt, found, err := existingCommercialOperation(ctx, tx, input.Meta, "refund.create"); err != nil {
		return Refund{}, CommercialReceipt{}, err
	} else if found {
		item, err := loadRefund(ctx, tx, receipt.ResourceID)
		return item, receipt, err
	}
	item, err := loadTopUpByPayment(ctx, tx, input.PaymentID)
	if err != nil {
		return Refund{}, CommercialReceipt{}, err
	}
	if item.Status != "paid" && item.Status != "partially_refunded" || item.PaidEntryID == "" || input.AmountMicro > item.AmountMicro-item.RefundedMicro {
		return Refund{}, CommercialReceipt{}, ErrConflict
	}
	owner, err := ownerForAccount(ctx, tx, item.AccountID)
	if err != nil {
		return Refund{}, CommercialReceipt{}, err
	}
	id, err := randomID("refund")
	if err != nil {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	posted, err := NewLedger(c.db).PostTx(ctx, tx, Post{OperationID: input.Meta.OperationID, Action: "refund", ActorAdminID: input.Meta.ActorAdminID, ResourceKind: "refund", ResourceID: id, ObservedAt: input.Meta.ObservedAt, RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: item.Currency, Kind: EntryAdjustmentDebit, AmountMicro: -input.AmountMicro, OriginalEntryID: item.PaidEntryID, ResourceKind: "refund", ResourceID: id}}})
	if err != nil {
		return Refund{}, CommercialReceipt{}, err
	}
	if len(posted) != 1 {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	refunded := item.RefundedMicro + input.AmountMicro
	status := "partially_refunded"
	if refunded == item.AmountMicro {
		status = "refunded"
	}
	result, err := tx.ExecContext(ctx, `UPDATE financial_payments SET status=?,refunded_micro=?,revision=revision+1 WHERE id=? AND revision=?`, status, refunded, item.PaymentID, item.Revision)
	if err != nil {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Refund{}, CommercialReceipt{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_refunds(id,payment_id,entry_id,amount_micro,created_at) VALUES(?,?,?,?,?)`, id, item.PaymentID, posted[0].ID, input.AmountMicro, formatCommercialTime(input.Meta.ObservedAt)); err != nil {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	receipt := CommercialReceipt{OperationID: input.Meta.OperationID, ResourceKind: "refund", ResourceID: id, Revision: 1, CreatedAt: input.Meta.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, input.Meta, "refund.create", receipt); err != nil {
		return Refund{}, CommercialReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Refund{}, CommercialReceipt{}, ErrUnavailable
	}
	return Refund{ID: id, PaymentID: item.PaymentID, EntryID: posted[0].ID, AmountMicro: input.AmountMicro, CreatedAt: input.Meta.ObservedAt}, receipt, nil
}

func existingCommercialOperation(ctx context.Context, tx *sql.Tx, meta WriteMeta, expectedAction string) (CommercialReceipt, bool, error) {
	var receipt CommercialReceipt
	var digest []byte
	var action, actor, created string
	err := tx.QueryRowContext(ctx, `SELECT operation_id,action,COALESCE(actor_admin_id,''),payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations WHERE operation_id=?`, meta.OperationID).Scan(&receipt.OperationID, &action, &actor, &digest, &receipt.ResourceKind, &receipt.ResourceID, &receipt.Revision, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return CommercialReceipt{}, false, nil
	}
	if err != nil {
		return CommercialReceipt{}, false, ErrUnavailable
	}
	if action != expectedAction || actor != meta.ActorAdminID || !equalBytes(digest, meta.PayloadDigest[:]) {
		return CommercialReceipt{}, false, ErrConflict
	}
	receipt.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return CommercialReceipt{}, false, ErrSchema
	}
	receipt.Replay = true
	return receipt, true, nil
}
func insertCommercialOperation(ctx context.Context, tx *sql.Tx, meta WriteMeta, action string, receipt CommercialReceipt) error {
	actor := any(nil)
	if meta.ActorAdminID != "" {
		actor = meta.ActorAdminID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES(?,?,?,?,?,?,?,?)`, meta.OperationID, action, actor, meta.PayloadDigest[:], receipt.ResourceKind, receipt.ResourceID, receipt.Revision, formatCommercialTime(meta.ObservedAt)); err != nil {
		return ErrUnavailable
	}
	return nil
}
func requireCommercialEnabled(ctx context.Context, tx *sql.Tx) error {
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM financial_settings WHERE singleton=1`).Scan(&enabled); err != nil {
		return ErrUnavailable
	}
	if enabled != 1 {
		return ErrConflict
	}
	return nil
}
func validMeta(meta WriteMeta) bool {
	return validCommercialText(meta.OperationID, 128) && (meta.ActorAdminID == "" || validCommercialText(meta.ActorAdminID, 256)) && meta.ObservedAt.Location() == time.UTC
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func formatCommercialTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

type rowScanner interface{ Scan(...any) error }

func loadPlan(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Plan, error) {
	var item Plan
	var enabled int
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at FROM financial_plans WHERE id=?`, id).Scan(&item.ID, &item.Name, &item.Currency, &item.PriceMicro, &item.CreditMicro, &item.Interval, &enabled, &item.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, ErrUnavailable
	}
	item.Enabled = enabled == 1
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Plan{}, ErrSchema
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return item, err
}
func loadConnector(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Connector, error) {
	var item Connector
	var enabled int
	var created, updated string
	err := q.QueryRowContext(ctx, `SELECT id,name,enabled,revision,created_at,updated_at FROM financial_payment_connectors WHERE id=?`, id).Scan(&item.ID, &item.Name, &enabled, &item.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Connector{}, ErrNotFound
	}
	if err != nil {
		return Connector{}, ErrUnavailable
	}
	item.Enabled = enabled == 1
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Connector{}, ErrSchema
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return item, err
}
func loadTopUpByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (TopUp, error) {
	return scanTopUp(q.QueryRowContext(ctx, `SELECT t.id,p.id,p.connector_id,p.account_id,p.external_reference,p.amount_micro,p.currency,p.status,COALESCE(p.paid_entry_id,''),p.created_at,p.paid_at,p.refunded_micro,p.revision FROM financial_topups t JOIN financial_payments p ON p.id=t.payment_id WHERE t.id=?`, id))
}
func loadTopUpByPayment(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (TopUp, error) {
	return scanTopUp(q.QueryRowContext(ctx, `SELECT t.id,p.id,p.connector_id,p.account_id,p.external_reference,p.amount_micro,p.currency,p.status,COALESCE(p.paid_entry_id,''),p.created_at,p.paid_at,p.refunded_micro,p.revision FROM financial_topups t JOIN financial_payments p ON p.id=t.payment_id WHERE p.id=?`, id))
}
func scanTopUp(row rowScanner) (TopUp, error) {
	var item TopUp
	var created string
	var paid sql.NullString
	err := row.Scan(&item.ID, &item.PaymentID, &item.ConnectorID, &item.AccountID, &item.ExternalReference, &item.AmountMicro, &item.Currency, &item.Status, &item.PaidEntryID, &created, &paid, &item.RefundedMicro, &item.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return TopUp{}, ErrNotFound
	}
	if err != nil {
		return TopUp{}, ErrUnavailable
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return TopUp{}, ErrSchema
	}
	if paid.Valid {
		parsed, parseErr := time.Parse(time.RFC3339Nano, paid.String)
		if parseErr != nil {
			return TopUp{}, ErrSchema
		}
		item.PaidAt = &parsed
	}
	return item, nil
}
func loadSubscription(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Subscription, error) {
	var item Subscription
	var started string
	var cancelled sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,cancelled_at,revision FROM financial_subscriptions WHERE id=?`, id).Scan(&item.ID, &item.AccountID, &item.PlanID, &item.PlanRevision, &item.PriceMicro, &item.CreditMicro, &item.Currency, &item.Interval, &item.Status, &started, &cancelled, &item.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, ErrUnavailable
	}
	item.StartedAt, err = time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return Subscription{}, ErrSchema
	}
	if cancelled.Valid {
		parsed, e := time.Parse(time.RFC3339Nano, cancelled.String)
		if e != nil {
			return Subscription{}, ErrSchema
		}
		item.CancelledAt = &parsed
	}
	return item, nil
}
func loadCode(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (RedemptionCode, error) {
	var item RedemptionCode
	var expires sql.NullString
	var enabled int
	var created string
	err := q.QueryRowContext(ctx, `SELECT id,amount_micro,currency,max_uses,uses,expires_at,enabled,created_at FROM financial_redemption_codes WHERE id=?`, id).Scan(&item.ID, &item.AmountMicro, &item.Currency, &item.MaxUses, &item.Uses, &expires, &enabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return RedemptionCode{}, ErrNotFound
	}
	if err != nil {
		return RedemptionCode{}, ErrUnavailable
	}
	item.Enabled = enabled == 1
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return RedemptionCode{}, ErrSchema
	}
	if expires.Valid {
		parsed, e := time.Parse(time.RFC3339Nano, expires.String)
		if e != nil {
			return RedemptionCode{}, ErrSchema
		}
		item.ExpiresAt = &parsed
	}
	return item, nil
}
func loadRefund(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Refund, error) {
	var item Refund
	var created string
	err := q.QueryRowContext(ctx, `SELECT id,payment_id,entry_id,amount_micro,created_at FROM financial_refunds WHERE id=?`, id).Scan(&item.ID, &item.PaymentID, &item.EntryID, &item.AmountMicro, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Refund{}, ErrNotFound
	}
	if err != nil {
		return Refund{}, ErrUnavailable
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return Refund{}, ErrSchema
	}
	return item, nil
}
func ownerForAccount(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (Owner, error) {
	var owner Owner
	var key sql.NullString
	err := q.QueryRowContext(ctx, `SELECT owner_kind,employee_id,key_id,resource_kind,resource_id FROM financial_accounts WHERE id=?`, id).Scan(&owner.Kind, &owner.EmployeeID, &key, &owner.ResourceKind, &owner.ResourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return Owner{}, ErrNotFound
	}
	if err != nil {
		return Owner{}, ErrUnavailable
	}
	owner.KeyID = key.String
	return owner, nil
}

// Kept local to avoid maps in idempotency payloads while retaining stable JSON field order.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
