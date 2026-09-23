package governance

import (
	"context"
	"database/sql"
)

const budgetClockDDL = `CREATE TABLE IF NOT EXISTS governance_budget_clock (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	last_effective_at TEXT NOT NULL
)`

const budgetReservationsDDL = `CREATE TABLE IF NOT EXISTS governance_budget_reservations (
	attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id),
	request_id TEXT NOT NULL REFERENCES governance_requests(id),
	account_id TEXT NOT NULL,
	provider TEXT NOT NULL CHECK(provider IN ('openai','openai-compatible','anthropic','gemini','codex')),
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	actual_model TEXT NOT NULL,
	account_revision INTEGER NOT NULL CHECK(typeof(account_revision)='integer' AND account_revision BETWEEN 1 AND 9007199254740991),
	pool_revision INTEGER NOT NULL CHECK(typeof(pool_revision)='integer' AND pool_revision BETWEEN 0 AND 9007199254740991),
	transform_revision TEXT NOT NULL,
	bounder_id TEXT NOT NULL,
	bounder_revision INTEGER NOT NULL CHECK(typeof(bounder_revision)='integer' AND bounder_revision BETWEEN 1 AND 9007199254740991),
	proof_type TEXT NOT NULL CHECK(proof_type IN ('four_buckets','mutually_exclusive_input')),
	upper_input INTEGER,
	upper_output INTEGER,
	upper_cache_read INTEGER,
	upper_cache_write INTEGER,
	upper_group_input INTEGER,
	upper_group_output INTEGER,
	token_upper INTEGER NOT NULL CHECK(typeof(token_upper)='integer' AND token_upper>=0),
	cost_upper INTEGER CHECK(cost_upper IS NULL OR (typeof(cost_upper)='integer' AND cost_upper>=0)),
	cost_currency TEXT NOT NULL,
	price_version TEXT,
	price_currency TEXT,
	price_input_rate INTEGER,
	price_output_rate INTEGER,
	price_cache_read_rate INTEGER,
	price_cache_write_rate INTEGER,
	lifecycle TEXT NOT NULL CHECK(lifecycle IN ('reserved','may_have_sent','settled','released_not_started','interrupted')),
	token_known INTEGER NOT NULL CHECK(typeof(token_known)='integer' AND token_known IN (0,1)),
	actual_tokens INTEGER CHECK(actual_tokens IS NULL OR (typeof(actual_tokens)='integer' AND actual_tokens>=0)),
	cost_known INTEGER NOT NULL CHECK(typeof(cost_known)='integer' AND cost_known IN (0,1)),
	actual_cost_micro INTEGER CHECK(actual_cost_micro IS NULL OR (typeof(actual_cost_micro)='integer' AND actual_cost_micro>=0)),
	token_overage INTEGER NOT NULL CHECK(typeof(token_overage)='integer' AND token_overage IN (0,1)),
	cost_overage INTEGER NOT NULL CHECK(typeof(cost_overage)='integer' AND cost_overage IN (0,1)),
	observed_reserved_at TEXT NOT NULL,
	effective_reserved_at TEXT NOT NULL,
	execution_expires_at TEXT NOT NULL,
	observed_marked_at TEXT,
	effective_marked_at TEXT,
	observed_settled_at TEXT,
	effective_settled_at TEXT,
	attribution_at TEXT,
	settlement_mode TEXT NOT NULL CHECK(settlement_mode IN ('','from_attempt','release_not_started','interrupt','recovery')),
	last_renew_expected_expires_at TEXT,
	last_renew_observed_at TEXT,
	last_renew_effective_at TEXT,
	CHECK(
		(proof_type='four_buckets' AND upper_input IS NOT NULL AND upper_output IS NOT NULL AND upper_cache_read IS NOT NULL AND upper_cache_write IS NOT NULL AND upper_group_input IS NULL AND upper_group_output IS NULL)
		OR (proof_type='mutually_exclusive_input' AND upper_input IS NULL AND upper_output IS NULL AND upper_cache_read IS NULL AND upper_cache_write IS NULL AND upper_group_input IS NOT NULL AND upper_group_output IS NOT NULL)
	),
	CHECK(
		(price_version IS NULL AND price_currency IS NULL AND price_input_rate IS NULL AND price_output_rate IS NULL AND price_cache_read_rate IS NULL AND price_cache_write_rate IS NULL AND cost_upper IS NULL AND cost_currency='')
		OR (price_version IS NOT NULL AND price_currency IS NOT NULL AND price_input_rate IS NOT NULL AND price_output_rate IS NOT NULL AND price_cache_read_rate IS NOT NULL AND price_cache_write_rate IS NOT NULL AND cost_upper IS NOT NULL AND length(cost_currency)=3 AND cost_currency GLOB '[A-Z][A-Z][A-Z]')
	),
	CHECK((observed_marked_at IS NULL AND effective_marked_at IS NULL) OR (observed_marked_at IS NOT NULL AND effective_marked_at IS NOT NULL)),
	CHECK((last_renew_expected_expires_at IS NULL AND last_renew_observed_at IS NULL AND last_renew_effective_at IS NULL) OR (last_renew_expected_expires_at IS NOT NULL AND last_renew_observed_at IS NOT NULL AND last_renew_effective_at IS NOT NULL)),
	CHECK(
		(lifecycle='reserved' AND observed_marked_at IS NULL AND observed_settled_at IS NULL AND settlement_mode='' AND token_known=0 AND actual_tokens IS NULL AND cost_known=0 AND actual_cost_micro IS NULL AND token_overage=0 AND cost_overage=0)
		OR (lifecycle='may_have_sent' AND observed_marked_at IS NOT NULL AND observed_settled_at IS NULL AND settlement_mode='' AND token_known=0 AND actual_tokens IS NULL AND cost_known=0 AND actual_cost_micro IS NULL AND token_overage=0 AND cost_overage=0)
		OR (lifecycle='settled' AND observed_settled_at IS NOT NULL AND effective_settled_at IS NOT NULL AND attribution_at IS NOT NULL AND settlement_mode='from_attempt' AND ((token_known=1 AND actual_tokens IS NOT NULL) OR (token_known=0 AND actual_tokens IS NULL)) AND ((cost_known=1 AND actual_cost_micro IS NOT NULL) OR (cost_known=0 AND actual_cost_micro IS NULL)))
		OR (lifecycle='released_not_started' AND observed_settled_at IS NOT NULL AND effective_settled_at IS NOT NULL AND attribution_at IS NOT NULL AND settlement_mode='release_not_started' AND token_known=0 AND actual_tokens IS NULL AND cost_known=0 AND actual_cost_micro IS NULL AND token_overage=0 AND cost_overage=0)
		OR (lifecycle='interrupted' AND observed_settled_at IS NOT NULL AND effective_settled_at IS NOT NULL AND attribution_at IS NOT NULL AND settlement_mode IN ('interrupt','recovery') AND token_known=0 AND actual_tokens IS NULL AND cost_known=0 AND actual_cost_micro IS NULL AND token_overage=0 AND cost_overage=0)
	)
)`

const budgetScopesDDL = `CREATE TABLE IF NOT EXISTS governance_budget_reservation_scopes (
	attempt_id TEXT NOT NULL REFERENCES governance_budget_reservations(attempt_id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK((scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991) OR (scope_kind IN ('employee','key') AND group_revision IS NULL)),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	hard_tpm INTEGER CHECK(hard_tpm IS NULL OR (typeof(hard_tpm)='integer' AND hard_tpm BETWEEN 1 AND 9007199254740991)),
	hard_cost_micro INTEGER CHECK(hard_cost_micro IS NULL OR (typeof(hard_cost_micro)='integer' AND hard_cost_micro>0)),
	hard_currency TEXT NOT NULL,
	hard_window TEXT NOT NULL,
	unknown_mode TEXT NOT NULL CHECK(unknown_mode IN ('shadow','deny_unknown')),
	PRIMARY KEY(attempt_id,scope_kind,scope_id),
	CHECK((hard_cost_micro IS NULL AND hard_currency='' AND hard_window='') OR (hard_cost_micro IS NOT NULL AND length(hard_currency)=3 AND hard_currency GLOB '[A-Z][A-Z][A-Z]' AND hard_window='rolling_24h'))
)`

const budgetQuarantineDDL = `CREATE TABLE IF NOT EXISTS governance_budget_profile_quarantine (
	provider TEXT NOT NULL,
	protocol TEXT NOT NULL,
	actual_model TEXT NOT NULL,
	transform_revision TEXT NOT NULL,
	bounder_id TEXT NOT NULL,
	bounder_revision INTEGER NOT NULL CHECK(typeof(bounder_revision)='integer' AND bounder_revision BETWEEN 1 AND 9007199254740991),
	reason TEXT NOT NULL CHECK(reason IN ('token_overage','cost_overage','token_and_cost_overage')),
	attempt_id TEXT NOT NULL REFERENCES governance_budget_reservations(attempt_id),
	quarantined_at TEXT NOT NULL,
	PRIMARY KEY(provider,protocol,actual_model,transform_revision,bounder_id,bounder_revision)
)`

const budgetScopesIndexDDL = `CREATE INDEX IF NOT EXISTS governance_budget_scopes_stable_idx
	ON governance_budget_reservation_scopes(scope_kind,scope_id,attempt_id)`

const budgetLifecycleIndexDDL = `CREATE INDEX IF NOT EXISTS governance_budget_lifecycle_idx
	ON governance_budget_reservations(lifecycle,effective_settled_at,attempt_id)`

func (b *Budget) MigrateTx(ctx context.Context, tx *sql.Tx) error {
	if b == nil || b.db == nil || ctx == nil || tx == nil {
		return ErrInvalid
	}
	if err := validateBudgetDependencies(ctx, tx); err != nil {
		return err
	}
	for _, statement := range []string{budgetClockDDL, budgetReservationsDDL, budgetScopesDDL, budgetQuarantineDDL, budgetScopesIndexDDL, budgetLifecycleIndexDDL} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrUnavailable
		}
	}
	if err := validateBudgetSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO governance_budget_clock(singleton,last_effective_at) VALUES(1,?)`, initialSettingsTime); err != nil {
		return ErrUnavailable
	}
	return validateBudgetStoredData(ctx, tx)
}

func validateBudgetDependencies(ctx context.Context, tx *sql.Tx) error {
	queries := []string{
		`SELECT enabled,budget_enabled,revision FROM governance_settings LIMIT 0`,
		`SELECT id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,expires_at,status FROM governance_requests LIMIT 0`,
		`SELECT request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode FROM governance_request_scopes LIMIT 0`,
		`SELECT id,employee_id,key_id,model_id,provider,status FROM accounting_requests LIMIT 0`,
		`SELECT id,request_id,account_id,provider,started_at,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate,cost_micro FROM accounting_attempts LIMIT 0`,
	}
	for _, query := range queries {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return ErrSchema
		}
		if err := rows.Close(); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func validateBudgetSchema(ctx context.Context, tx *sql.Tx) error {
	tables := map[string]string{
		"governance_budget_clock":              budgetClockDDL,
		"governance_budget_reservations":       budgetReservationsDDL,
		"governance_budget_reservation_scopes": budgetScopesDDL,
		"governance_budget_profile_quarantine": budgetQuarantineDDL,
	}
	for name, expected := range tables {
		var objectType, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &definition); err != nil {
			return ErrSchema
		}
		if objectType != "table" || normalizeDDL(definition) != normalizeDDL(storedDDL(expected)) {
			return ErrSchema
		}
	}
	indexes := map[string]struct{ table, definition string }{
		"governance_budget_scopes_stable_idx": {"governance_budget_reservation_scopes", budgetScopesIndexDDL},
		"governance_budget_lifecycle_idx":     {"governance_budget_reservations", budgetLifecycleIndexDDL},
	}
	for name, expected := range indexes {
		var objectType, table, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &table, &definition); err != nil {
			return ErrSchema
		}
		if objectType != "index" || table != expected.table || normalizeDDL(definition) != normalizeDDL(storedDDL(expected.definition)) {
			return ErrSchema
		}
	}
	indexRows, err := tx.QueryContext(ctx, `SELECT name,tbl_name FROM sqlite_master
		WHERE type='index' AND sql IS NOT NULL AND tbl_name IN ('governance_budget_clock','governance_budget_reservations','governance_budget_reservation_scopes','governance_budget_profile_quarantine')`)
	if err != nil {
		return ErrUnavailable
	}
	seen := make(map[string]string)
	for indexRows.Next() {
		var name, table string
		if err := indexRows.Scan(&name, &table); err != nil {
			indexRows.Close()
			return ErrUnavailable
		}
		seen[name] = table
	}
	iterationErr, closeErr := indexRows.Err(), indexRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if len(seen) != len(indexes) {
		return ErrSchema
	}
	for name, expected := range indexes {
		if seen[name] != expected.table {
			return ErrSchema
		}
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN ('governance_budget_clock','governance_budget_reservations','governance_budget_reservation_scopes','governance_budget_profile_quarantine')`).Scan(&triggers); err != nil {
		return ErrUnavailable
	}
	if triggers != 0 {
		return ErrSchema
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return ErrUnavailable
	}
	hasViolation := foreignRows.Next()
	iterationErr, closeErr = foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if hasViolation {
		return ErrSchema
	}
	return nil
}

func validateBudgetStoredData(ctx context.Context, tx *sql.Tx) error {
	var clock string
	if err := tx.QueryRowContext(ctx, `SELECT last_effective_at FROM governance_budget_clock WHERE singleton=1`).Scan(&clock); err != nil {
		return ErrSchema
	}
	if _, err := parseStoredTime(clock); err != nil {
		return ErrSchema
	}
	var clockRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_budget_clock`).Scan(&clockRows); err != nil {
		return ErrUnavailable
	}
	if clockRows != 1 {
		return ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id FROM governance_budget_reservations ORDER BY attempt_id`)
	if err != nil {
		return ErrUnavailable
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ErrUnavailable
		}
		ids = append(ids, id)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	for _, id := range ids {
		reservation, err := loadBudgetReservation(ctx, tx, id)
		if err != nil || !validLoadedBudgetReservation(reservation) {
			return ErrSchema
		}
	}
	return validateBudgetQuarantine(ctx, tx)
}

func validateBudgetQuarantine(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT provider,protocol,actual_model,transform_revision,bounder_id,bounder_revision,reason,attempt_id,quarantined_at FROM governance_budget_profile_quarantine`)
	if err != nil {
		return ErrUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var provider, protocol, model, transform, bounder, reason, attemptID, at string
		var revision int64
		if err := rows.Scan(&provider, &protocol, &model, &transform, &bounder, &revision, &reason, &attemptID, &at); err != nil {
			return ErrUnavailable
		}
		if !validBudgetProvider(provider) || !validProtocolValue(protocol) || !validBudgetText(model, 256) || !validBudgetText(transform, 128) ||
			!validBudgetText(bounder, 128) || !validRevision(revision) || !validMetadata(attemptID, 256) || !validQuarantineReason(reason) {
			return ErrSchema
		}
		if _, err := parseStoredTime(at); err != nil {
			return ErrSchema
		}
	}
	if err := rows.Err(); err != nil {
		return ErrUnavailable
	}
	return nil
}
