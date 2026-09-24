package financial

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type Commercial struct{ db *sql.DB }

func NewCommercial(db *sql.DB) *Commercial { return &Commercial{db: db} }

const commercialSettingsDDL = `CREATE TABLE IF NOT EXISTS financial_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	updated_at TEXT NOT NULL
)`

const commercialOperationsDDL = `CREATE TABLE IF NOT EXISTS financial_commercial_operations (
	operation_id TEXT PRIMARY KEY,
	action TEXT NOT NULL CHECK(action IN ('settings.update','plan.create','plan.update','subscription.create','subscription.cancel','connector.create','connector.update','topup.create','redemption_code.create','redemption.redeem','refund.create')),
	actor_admin_id TEXT REFERENCES admins(id) ON DELETE RESTRICT,
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	resource_kind TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
)`

const plansDDL = `CREATE TABLE IF NOT EXISTS financial_plans (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	currency TEXT NOT NULL CHECK(length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
	price_micro INTEGER NOT NULL CHECK(typeof(price_micro)='integer' AND price_micro>0),
	credit_micro INTEGER NOT NULL CHECK(typeof(credit_micro)='integer' AND credit_micro>0),
	interval TEXT NOT NULL CHECK(interval IN ('one_time','monthly')),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(updated_at>=created_at)
)`

const subscriptionsDDL = `CREATE TABLE IF NOT EXISTS financial_subscriptions (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES financial_accounts(id) ON DELETE RESTRICT,
	plan_id TEXT NOT NULL REFERENCES financial_plans(id) ON DELETE RESTRICT,
	plan_revision INTEGER NOT NULL CHECK(typeof(plan_revision)='integer' AND plan_revision BETWEEN 1 AND 9007199254740991),
	price_micro INTEGER NOT NULL CHECK(typeof(price_micro)='integer' AND price_micro>0),
	credit_micro INTEGER NOT NULL CHECK(typeof(credit_micro)='integer' AND credit_micro>0),
	currency TEXT NOT NULL CHECK(length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
	interval TEXT NOT NULL CHECK(interval IN ('one_time','monthly')),
	status TEXT NOT NULL CHECK(status IN ('active','cancelled')),
	started_at TEXT NOT NULL,
	cancelled_at TEXT,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	CHECK((status='active' AND cancelled_at IS NULL) OR (status='cancelled' AND cancelled_at IS NOT NULL))
)`

const connectorsDDL = `CREATE TABLE IF NOT EXISTS financial_payment_connectors (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	secret_ciphertext BLOB NOT NULL CHECK(typeof(secret_ciphertext)='blob'),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(updated_at>=created_at)
)`

const paymentsDDL = `CREATE TABLE IF NOT EXISTS financial_payments (
	id TEXT PRIMARY KEY,
	connector_id TEXT NOT NULL REFERENCES financial_payment_connectors(id) ON DELETE RESTRICT,
	account_id TEXT NOT NULL REFERENCES financial_accounts(id) ON DELETE RESTRICT,
	external_reference TEXT NOT NULL,
	amount_micro INTEGER NOT NULL CHECK(typeof(amount_micro)='integer' AND amount_micro>0),
	currency TEXT NOT NULL CHECK(length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
	status TEXT NOT NULL CHECK(status IN ('pending','paid','partially_refunded','refunded')),
	paid_entry_id TEXT REFERENCES financial_entries(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	paid_at TEXT,
	refunded_micro INTEGER NOT NULL CHECK(typeof(refunded_micro)='integer' AND refunded_micro>=0 AND refunded_micro<=amount_micro),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	UNIQUE(connector_id,external_reference),
	CHECK((status='pending' AND paid_entry_id IS NULL AND paid_at IS NULL AND refunded_micro=0) OR
		(status IN ('paid','partially_refunded','refunded') AND paid_entry_id IS NOT NULL AND paid_at IS NOT NULL))
)`

const topupsDDL = `CREATE TABLE IF NOT EXISTS financial_topups (
	id TEXT PRIMARY KEY,
	payment_id TEXT NOT NULL UNIQUE REFERENCES financial_payments(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL
)`

const redemptionCodesDDL = `CREATE TABLE IF NOT EXISTS financial_redemption_codes (
	id TEXT PRIMARY KEY,
	code_digest BLOB NOT NULL UNIQUE CHECK(typeof(code_digest)='blob' AND length(code_digest)=32),
	amount_micro INTEGER NOT NULL CHECK(typeof(amount_micro)='integer' AND amount_micro>0),
	currency TEXT NOT NULL CHECK(length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
	max_uses INTEGER NOT NULL CHECK(typeof(max_uses)='integer' AND max_uses BETWEEN 1 AND 9007199254740991),
	uses INTEGER NOT NULL CHECK(typeof(uses)='integer' AND uses>=0 AND uses<=max_uses),
	expires_at TEXT,
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	created_at TEXT NOT NULL
)`

const redemptionsDDL = `CREATE TABLE IF NOT EXISTS financial_redemptions (
	id TEXT PRIMARY KEY,
	code_id TEXT NOT NULL REFERENCES financial_redemption_codes(id) ON DELETE RESTRICT,
	account_id TEXT NOT NULL REFERENCES financial_accounts(id) ON DELETE RESTRICT,
	entry_id TEXT NOT NULL UNIQUE REFERENCES financial_entries(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	UNIQUE(code_id,account_id)
)`

const refundsDDL = `CREATE TABLE IF NOT EXISTS financial_refunds (
	id TEXT PRIMARY KEY,
	payment_id TEXT NOT NULL REFERENCES financial_payments(id) ON DELETE RESTRICT,
	entry_id TEXT NOT NULL UNIQUE REFERENCES financial_entries(id) ON DELETE RESTRICT,
	amount_micro INTEGER NOT NULL CHECK(typeof(amount_micro)='integer' AND amount_micro>0),
	created_at TEXT NOT NULL
)`

const webhookEventsDDL = `CREATE TABLE IF NOT EXISTS financial_webhook_events (
	connector_id TEXT NOT NULL REFERENCES financial_payment_connectors(id) ON DELETE RESTRICT,
	event_id TEXT NOT NULL,
	payment_id TEXT NOT NULL REFERENCES financial_payments(id) ON DELETE RESTRICT,
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	signed_at TEXT NOT NULL,
	processed_at TEXT NOT NULL,
	PRIMARY KEY(connector_id,event_id)
)`

const subscriptionAccountIndexDDL = `CREATE INDEX IF NOT EXISTS financial_subscriptions_account_idx ON financial_subscriptions(account_id,status,id)`
const paymentAccountIndexDDL = `CREATE INDEX IF NOT EXISTS financial_payments_account_idx ON financial_payments(account_id,status,id)`
const paymentConnectorIndexDDL = `CREATE INDEX IF NOT EXISTS financial_payments_connector_idx ON financial_payments(connector_id,status,id)`
const refundPaymentIndexDDL = `CREATE INDEX IF NOT EXISTS financial_refunds_payment_idx ON financial_refunds(payment_id,id)`
const redemptionAccountIndexDDL = `CREATE INDEX IF NOT EXISTS financial_redemptions_account_idx ON financial_redemptions(account_id,id)`
const commercialOperationsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_commercial_operations_no_update BEFORE UPDATE ON financial_commercial_operations BEGIN SELECT RAISE(ABORT,'financial commercial operations are immutable'); END`
const commercialOperationsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_commercial_operations_no_delete BEFORE DELETE ON financial_commercial_operations BEGIN SELECT RAISE(ABORT,'financial commercial operations are immutable'); END`
const topupsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_topups_no_update BEFORE UPDATE ON financial_topups BEGIN SELECT RAISE(ABORT,'financial topups are immutable'); END`
const topupsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_topups_no_delete BEFORE DELETE ON financial_topups BEGIN SELECT RAISE(ABORT,'financial topups are immutable'); END`
const redemptionsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_redemptions_no_update BEFORE UPDATE ON financial_redemptions BEGIN SELECT RAISE(ABORT,'financial redemptions are immutable'); END`
const redemptionsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_redemptions_no_delete BEFORE DELETE ON financial_redemptions BEGIN SELECT RAISE(ABORT,'financial redemptions are immutable'); END`
const refundsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_refunds_no_update BEFORE UPDATE ON financial_refunds BEGIN SELECT RAISE(ABORT,'financial refunds are immutable'); END`
const refundsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_refunds_no_delete BEFORE DELETE ON financial_refunds BEGIN SELECT RAISE(ABORT,'financial refunds are immutable'); END`
const webhookEventsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_webhook_events_no_update BEFORE UPDATE ON financial_webhook_events BEGIN SELECT RAISE(ABORT,'financial webhook events are immutable'); END`
const webhookEventsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_webhook_events_no_delete BEFORE DELETE ON financial_webhook_events BEGIN SELECT RAISE(ABORT,'financial webhook events are immutable'); END`

func (c *Commercial) Migrate(ctx context.Context) error {
	if c == nil || c.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, prerequisite := range []string{"financial_accounts", "financial_operations", "financial_entries", "admins"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, prerequisite).Scan(&count); err != nil || count != 1 {
			return ErrSchema
		}
	}
	statements := []string{commercialSettingsDDL, commercialOperationsDDL, plansDDL, subscriptionsDDL, connectorsDDL, paymentsDDL, topupsDDL, redemptionCodesDDL, redemptionsDDL, refundsDDL, webhookEventsDDL,
		subscriptionAccountIndexDDL, paymentAccountIndexDDL, paymentConnectorIndexDDL, refundPaymentIndexDDL, redemptionAccountIndexDDL,
		commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL, topupsNoUpdateDDL, topupsNoDeleteDDL, redemptionsNoUpdateDDL, redemptionsNoDeleteDDL,
		refundsNoUpdateDDL, refundsNoDeleteDDL, webhookEventsNoUpdateDDL, webhookEventsNoDeleteDDL}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrSchema
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO financial_settings(singleton,enabled,revision,updated_at) VALUES(1,0,1,?)`, time.Unix(0, 0).UTC().Format(time.RFC3339Nano)); err != nil {
		return ErrSchema
	}
	if err := validateCommercialSchema(ctx, tx); err != nil {
		return err
	}
	if err := validateCommercialStored(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func validateCommercialSchema(ctx context.Context, tx *sql.Tx) error {
	tables := map[string]string{
		"financial_settings": commercialSettingsDDL, "financial_commercial_operations": commercialOperationsDDL, "financial_plans": plansDDL,
		"financial_subscriptions": subscriptionsDDL, "financial_payment_connectors": connectorsDDL, "financial_payments": paymentsDDL,
		"financial_topups": topupsDDL, "financial_redemption_codes": redemptionCodesDDL, "financial_redemptions": redemptionsDDL,
		"financial_refunds": refundsDDL, "financial_webhook_events": webhookEventsDDL,
	}
	for name, ddl := range tables {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "table" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	indexes := map[string]string{
		"financial_subscriptions_account_idx": subscriptionAccountIndexDDL, "financial_payments_account_idx": paymentAccountIndexDDL,
		"financial_payments_connector_idx": paymentConnectorIndexDDL, "financial_refunds_payment_idx": refundPaymentIndexDDL,
		"financial_redemptions_account_idx": redemptionAccountIndexDDL,
	}
	for name, ddl := range indexes {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "index" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	triggers := map[string]string{
		"financial_commercial_operations_no_update": commercialOperationsNoUpdateDDL, "financial_commercial_operations_no_delete": commercialOperationsNoDeleteDDL,
		"financial_topups_no_update": topupsNoUpdateDDL, "financial_topups_no_delete": topupsNoDeleteDDL,
		"financial_redemptions_no_update": redemptionsNoUpdateDDL, "financial_redemptions_no_delete": redemptionsNoDeleteDDL,
		"financial_refunds_no_update": refundsNoUpdateDDL, "financial_refunds_no_delete": refundsNoDeleteDDL,
		"financial_webhook_events_no_update": webhookEventsNoUpdateDDL, "financial_webhook_events_no_delete": webhookEventsNoDeleteDDL,
	}
	for name, ddl := range triggers {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "trigger" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	var explicitIndexes, triggerCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND tbl_name IN ('financial_settings','financial_commercial_operations','financial_plans','financial_subscriptions','financial_payment_connectors','financial_payments','financial_topups','financial_redemption_codes','financial_redemptions','financial_refunds','financial_webhook_events')`).Scan(&explicitIndexes); err != nil || explicitIndexes != 5 {
		return ErrSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('financial_settings','financial_commercial_operations','financial_plans','financial_subscriptions','financial_payment_connectors','financial_payments','financial_topups','financial_redemption_codes','financial_redemptions','financial_refunds','financial_webhook_events')`).Scan(&triggerCount); err != nil || triggerCount != 10 {
		return ErrSchema
	}
	return nil
}

func validateCommercialStored(ctx context.Context, tx *sql.Tx) error {
	var rows, revision int64
	var enabled int
	var updated string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(enabled),-1),COALESCE(MAX(revision),0),COALESCE(MAX(updated_at),'') FROM financial_settings`).Scan(&rows, &enabled, &revision, &updated); err != nil || rows != 1 || enabled < 0 || enabled > 1 || revision < 1 {
		return ErrSchema
	}
	if _, err := time.Parse(time.RFC3339Nano, updated); err != nil {
		return ErrSchema
	}
	var mismatches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_payments WHERE (status='paid' AND refunded_micro<>0) OR (status='partially_refunded' AND (refunded_micro=0 OR refunded_micro>=amount_micro)) OR (status='refunded' AND refunded_micro<>amount_micro)`).Scan(&mismatches); err != nil || mismatches != 0 {
		return ErrSchema
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return ErrUnavailable
	}
	violated := foreignRows.Next()
	iterationErr, closeErr := foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if violated {
		return ErrSchema
	}
	return nil
}

func validCommercialText(value string, limit int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= limit && !strings.ContainsRune(value, 0)
}
