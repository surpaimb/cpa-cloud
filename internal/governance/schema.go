package governance

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const legacySettingsDDL = `CREATE TABLE IF NOT EXISTS governance_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	last_effective_admission_at TEXT,
	updated_at TEXT NOT NULL
)`

const settingsDDL = `CREATE TABLE IF NOT EXISTS governance_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	budget_enabled INTEGER NOT NULL CHECK(typeof(budget_enabled)='integer' AND budget_enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	last_effective_admission_at TEXT,
	updated_at TEXT NOT NULL
)`

const legacyRequestsDDL = `CREATE TABLE IF NOT EXISTS governance_requests (
	id TEXT PRIMARY KEY,
	employee_id TEXT NOT NULL,
	key_id TEXT NOT NULL,
	public_model TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	observed_started_at TEXT NOT NULL,
	effective_started_at TEXT NOT NULL,
	effective_lease_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	observed_finished_at TEXT,
	effective_finished_at TEXT,
	released_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
	CHECK(effective_started_at>=observed_started_at),
	CHECK(effective_lease_at>=effective_started_at),
	CHECK(expires_at>effective_lease_at),
	CHECK(effective_finished_at IS NULL OR effective_finished_at>=effective_lease_at),
	CHECK(released_at IS NULL OR released_at>=effective_lease_at),
	CHECK(
		(status='pending' AND observed_finished_at IS NULL AND effective_finished_at IS NULL AND released_at IS NULL)
		OR (status='interrupted' AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL)
		OR (status IN ('succeeded','failed','cancelled') AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL AND released_at IS NOT NULL)
	)
)`

const requestsDDL = `CREATE TABLE IF NOT EXISTS governance_requests (
	id TEXT PRIMARY KEY,
	employee_id TEXT NOT NULL,
	key_id TEXT NOT NULL,
	public_model TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	budget_enabled INTEGER NOT NULL CHECK(typeof(budget_enabled)='integer' AND budget_enabled IN (0,1)),
	observed_started_at TEXT NOT NULL,
	effective_started_at TEXT NOT NULL,
	effective_lease_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	observed_finished_at TEXT,
	effective_finished_at TEXT,
	released_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
	CHECK(effective_started_at>=observed_started_at),
	CHECK(effective_lease_at>=effective_started_at),
	CHECK(expires_at>effective_lease_at),
	CHECK(effective_finished_at IS NULL OR effective_finished_at>=effective_lease_at),
	CHECK(released_at IS NULL OR released_at>=effective_lease_at),
	CHECK(
		(status='pending' AND observed_finished_at IS NULL AND effective_finished_at IS NULL AND released_at IS NULL)
		OR (status='interrupted' AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL)
		OR (status IN ('succeeded','failed','cancelled') AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL AND released_at IS NOT NULL)
	)
)`

const legacyScopesDDL = `CREATE TABLE IF NOT EXISTS governance_request_scopes (
	request_id TEXT NOT NULL REFERENCES governance_requests(id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK(
		(scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991)
		OR (scope_kind IN ('employee','key') AND group_revision IS NULL)
	),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit>0)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit>0)),
	shadow_tpm INTEGER CHECK(shadow_tpm IS NULL OR (typeof(shadow_tpm)='integer' AND shadow_tpm>0)),
	shadow_cost_micro INTEGER CHECK(shadow_cost_micro IS NULL OR (typeof(shadow_cost_micro)='integer' AND shadow_cost_micro>0)),
	shadow_currency TEXT NOT NULL,
	shadow_window TEXT NOT NULL,
	PRIMARY KEY(request_id,scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL OR shadow_tpm IS NOT NULL OR shadow_cost_micro IS NOT NULL),
	CHECK(
		(shadow_cost_micro IS NULL AND shadow_currency='' AND shadow_window='')
		OR (shadow_cost_micro IS NOT NULL AND length(shadow_currency)=3 AND shadow_currency GLOB '[A-Z][A-Z][A-Z]' AND shadow_window='rolling_24h')
	)
)`

const scopesDDL = `CREATE TABLE IF NOT EXISTS governance_request_scopes (
	request_id TEXT NOT NULL REFERENCES governance_requests(id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK(
		(scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991)
		OR (scope_kind IN ('employee','key') AND group_revision IS NULL)
	),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit>0)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit>0)),
	hard_tpm INTEGER CHECK(hard_tpm IS NULL OR (typeof(hard_tpm)='integer' AND hard_tpm BETWEEN 1 AND 9007199254740991)),
	hard_cost_micro INTEGER CHECK(hard_cost_micro IS NULL OR (typeof(hard_cost_micro)='integer' AND hard_cost_micro>0)),
	hard_currency TEXT NOT NULL,
	hard_window TEXT NOT NULL,
	unknown_mode TEXT NOT NULL CHECK(unknown_mode IN ('shadow','deny_unknown')),
	shadow_tpm INTEGER CHECK(shadow_tpm IS NULL OR (typeof(shadow_tpm)='integer' AND shadow_tpm>0)),
	shadow_cost_micro INTEGER CHECK(shadow_cost_micro IS NULL OR (typeof(shadow_cost_micro)='integer' AND shadow_cost_micro>0)),
	shadow_currency TEXT NOT NULL,
	shadow_window TEXT NOT NULL,
	PRIMARY KEY(request_id,scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL OR hard_tpm IS NOT NULL OR hard_cost_micro IS NOT NULL OR shadow_tpm IS NOT NULL OR shadow_cost_micro IS NOT NULL),
	CHECK(
		(hard_cost_micro IS NULL AND hard_currency='' AND hard_window='')
		OR (hard_cost_micro IS NOT NULL AND length(hard_currency)=3 AND hard_currency GLOB '[A-Z][A-Z][A-Z]' AND hard_window='rolling_24h')
	),
	CHECK(
		(shadow_cost_micro IS NULL AND shadow_currency='' AND shadow_window='')
		OR (shadow_cost_micro IS NOT NULL AND length(shadow_currency)=3 AND shadow_currency GLOB '[A-Z][A-Z][A-Z]' AND shadow_window='rolling_24h')
	)
)`

const scopesIndexDDL = `CREATE INDEX IF NOT EXISTS governance_request_scopes_scope_idx
	ON governance_request_scopes(scope_kind,scope_id,request_id)`

const requestsEffectiveIndexDDL = `CREATE INDEX IF NOT EXISTS governance_requests_effective_idx
	ON governance_requests(effective_started_at,id)`

const initialSettingsTime = "1970-01-01T00:00:00.000000000Z"

func (c *Coordinator) Migrate(ctx context.Context) error {
	if c == nil || c.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	if err := c.MigrateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

// SchemaVersionTx returns 0 for an empty core schema, 1 for the exact legacy
// schema, and 2 for the exact budget-aware schema. Partial or altered schemas
// fail closed so an App can reject mixed sibling upgrades before mutation.
func (c *Coordinator) SchemaVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil {
		return 0, ErrInvalid
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN (
		'governance_settings','governance_requests','governance_request_scopes',
		'governance_request_scopes_scope_idx','governance_requests_effective_idx')`).Scan(&count); err != nil {
		return 0, ErrUnavailable
	}
	if count == 0 {
		return 0, nil
	}
	matched, matchErr := schemaMatches(ctx, tx, settingsDDL, requestsDDL, scopesDDL)
	if matchErr != nil {
		return 0, matchErr
	}
	if matched {
		return 2, nil
	}
	matched, matchErr = schemaMatches(ctx, tx, legacySettingsDDL, legacyRequestsDDL, legacyScopesDDL)
	if matchErr != nil {
		return 0, matchErr
	}
	if matched {
		return 1, nil
	}
	return 0, ErrSchema
}

// MigrateTx upgrades or validates the governance schema in a caller-owned
// transaction. It never commits or rolls back the transaction.
func (c *Coordinator) MigrateTx(ctx context.Context, tx *sql.Tx) error {
	version, err := c.SchemaVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	if version == 1 {
		if err := validateStoredDataVersion(ctx, tx, 1); err != nil {
			return err
		}
		if err := migrateLegacySchema(ctx, tx); err != nil {
			return err
		}
	} else if version == 0 {
		for _, statement := range []string{settingsDDL, requestsDDL, scopesDDL, scopesIndexDDL, requestsEffectiveIndexDDL} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return ErrUnavailable
			}
		}
	}
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO governance_settings(singleton,enabled,budget_enabled,revision,updated_at) VALUES(1,0,0,1,?)`, initialSettingsTime); err != nil {
		return ErrUnavailable
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_settings`).Scan(&count); err != nil {
		return ErrUnavailable
	}
	if count != 1 {
		return ErrSchema
	}
	return validateStoredDataVersion(ctx, tx, 2)
}

func schemaMatches(ctx context.Context, tx *sql.Tx, settings, requests, scopes string) (bool, error) {
	objects := map[string]string{
		"governance_settings": settings, "governance_requests": requests, "governance_request_scopes": scopes,
	}
	for name, expected := range objects {
		var objectType, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &definition); err != nil {
			if err == sql.ErrNoRows {
				return false, nil
			}
			return false, ErrUnavailable
		}
		if objectType != "table" || normalizeDDL(definition) != normalizeDDL(storedDDL(expected)) {
			return false, nil
		}
	}
	expectedIndexes := map[string]string{
		"governance_request_scopes_scope_idx": scopesIndexDDL,
		"governance_requests_effective_idx":   requestsEffectiveIndexDDL,
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='index'
		AND tbl_name IN ('governance_settings','governance_requests','governance_request_scopes') AND sql IS NOT NULL`)
	if err != nil {
		return false, ErrUnavailable
	}
	seen := 0
	mismatch := false
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			rows.Close()
			return false, ErrUnavailable
		}
		expected, ok := expectedIndexes[name]
		if !ok || normalizeDDL(definition) != normalizeDDL(storedDDL(expected)) {
			mismatch = true
		}
		seen++
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return false, ErrUnavailable
	}
	if mismatch {
		return false, nil
	}
	if seen != len(expectedIndexes) {
		return false, nil
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger'
		AND tbl_name IN ('governance_settings','governance_requests','governance_request_scopes')`).Scan(&triggers); err != nil {
		return false, ErrUnavailable
	}
	return triggers == 0, nil
}

func migrateLegacySchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`DROP INDEX governance_request_scopes_scope_idx`,
		`DROP INDEX governance_requests_effective_idx`,
		`ALTER TABLE governance_request_scopes RENAME TO governance_request_scopes_legacy_budget`,
		`ALTER TABLE governance_requests RENAME TO governance_requests_legacy_budget`,
		`ALTER TABLE governance_settings RENAME TO governance_settings_legacy_budget`,
		settingsDDL, requestsDDL, scopesDDL, scopesIndexDDL, requestsEffectiveIndexDDL,
		`INSERT INTO governance_settings(singleton,enabled,budget_enabled,revision,last_effective_admission_at,updated_at)
			SELECT singleton,enabled,0,revision,last_effective_admission_at,updated_at FROM governance_settings_legacy_budget`,
		`INSERT INTO governance_requests(id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,observed_started_at,
			effective_started_at,effective_lease_at,expires_at,observed_finished_at,effective_finished_at,released_at,status)
			SELECT id,employee_id,key_id,public_model,protocol,settings_revision,0,observed_started_at,effective_started_at,effective_lease_at,
			expires_at,observed_finished_at,effective_finished_at,released_at,status FROM governance_requests_legacy_budget`,
		`INSERT INTO governance_request_scopes(request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,rpm_limit,
			concurrency_limit,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window)
			SELECT request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,rpm_limit,concurrency_limit,NULL,NULL,'','','shadow',
			shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window FROM governance_request_scopes_legacy_budget`,
		`DROP TABLE governance_request_scopes_legacy_budget`,
		`DROP TABLE governance_requests_legacy_budget`,
		`DROP TABLE governance_settings_legacy_budget`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func validateSchema(ctx context.Context, tx *sql.Tx) error {
	objects := map[string]string{
		"governance_settings":       settingsDDL,
		"governance_requests":       requestsDDL,
		"governance_request_scopes": scopesDDL,
	}
	for name, expected := range objects {
		var objectType, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &definition); err != nil {
			return ErrSchema
		}
		if objectType != "table" || normalizeDDL(definition) != normalizeDDL(storedDDL(expected)) {
			return ErrSchema
		}
	}
	indexes := map[string]struct {
		table      string
		definition string
	}{
		"governance_request_scopes_scope_idx": {table: "governance_request_scopes", definition: scopesIndexDDL},
		"governance_requests_effective_idx":   {table: "governance_requests", definition: requestsEffectiveIndexDDL},
	}
	for name, expected := range indexes {
		var indexType, indexTable, indexDefinition string
		if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name=?`, name).Scan(&indexType, &indexTable, &indexDefinition); err != nil {
			return ErrSchema
		}
		if indexType != "index" || indexTable != expected.table || normalizeDDL(indexDefinition) != normalizeDDL(storedDDL(expected.definition)) {
			return ErrSchema
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master
		WHERE type='index' AND tbl_name IN ('governance_settings','governance_requests','governance_request_scopes') AND sql IS NOT NULL`)
	if err != nil {
		return ErrUnavailable
	}
	count := 0
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			return ErrUnavailable
		}
		expected, ok := indexes[name]
		if !ok || normalizeDDL(definition) != normalizeDDL(storedDDL(expected.definition)) {
			return ErrSchema
		}
		count++
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if count != len(indexes) {
		return ErrSchema
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
		WHERE type='trigger' AND tbl_name IN ('governance_settings','governance_requests','governance_request_scopes')`).Scan(&triggers); err != nil {
		return ErrUnavailable
	}
	if triggers != 0 {
		return ErrSchema
	}
	return nil
}

func validateStoredData(ctx context.Context, tx *sql.Tx) error {
	return validateStoredDataVersion(ctx, tx, 2)
}

func validateStoredDataVersion(ctx context.Context, tx *sql.Tx, version int) error {
	settings, err := readSettingsVersion(ctx, tx, version)
	if err != nil {
		return ErrSchema
	}
	requestQuery := `SELECT id,employee_id,key_id,public_model,protocol,settings_revision,0,
		observed_started_at,effective_started_at,effective_lease_at,expires_at,
		observed_finished_at,effective_finished_at,released_at,status FROM governance_requests ORDER BY id`
	if version == 2 {
		requestQuery = `SELECT id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,
			observed_started_at,effective_started_at,effective_lease_at,expires_at,
			observed_finished_at,effective_finished_at,released_at,status FROM governance_requests ORDER BY id`
	}
	rows, err := tx.QueryContext(ctx, requestQuery)
	if err != nil {
		return ErrUnavailable
	}
	requestScopes := make(map[string]int)
	var latestEffective *time.Time
	for rows.Next() {
		var id, employeeID, keyID, publicModel, protocol, observedStart, effectiveStart, effectiveLease, expires, status string
		var settingsRevision int64
		var budgetEnabled int
		var observedFinish, effectiveFinish, released sql.NullString
		if err := rows.Scan(&id, &employeeID, &keyID, &publicModel, &protocol, &settingsRevision, &budgetEnabled,
			&observedStart, &effectiveStart, &effectiveLease, &expires, &observedFinish, &effectiveFinish, &released, &status); err != nil {
			rows.Close()
			return ErrSchema
		}
		if !validMetadata(id, 256) || !validMetadata(employeeID, 256) || !validMetadata(keyID, 256) || !validMetadata(publicModel, 256) ||
			!validProtocol(accounting.UsageProtocol(protocol)) || !validRevision(settingsRevision) || budgetEnabled < 0 || budgetEnabled > 1 {
			rows.Close()
			return ErrSchema
		}
		observedStartedAt, err := parseStoredTime(observedStart)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		effectiveStartedAt, err := parseStoredTime(effectiveStart)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		effectiveLeaseAt, err := parseStoredTime(effectiveLease)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		expiresAt, err := parseStoredTime(expires)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		observedFinishedAt, err := pointerTime(observedFinish)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		effectiveFinishedAt, err := pointerTime(effectiveFinish)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		releasedAt, err := pointerTime(released)
		if err != nil {
			rows.Close()
			return ErrSchema
		}
		if effectiveStartedAt.Before(observedStartedAt) || effectiveLeaseAt.Before(effectiveStartedAt) || !expiresAt.After(effectiveLeaseAt) ||
			effectiveFinishedAt != nil && effectiveFinishedAt.Before(effectiveLeaseAt) || releasedAt != nil && releasedAt.Before(effectiveLeaseAt) ||
			!validStoredState(accounting.Status(status), observedFinishedAt, effectiveFinishedAt, releasedAt) {
			rows.Close()
			return ErrSchema
		}
		if latestEffective == nil || effectiveStartedAt.After(*latestEffective) {
			value := effectiveStartedAt
			latestEffective = &value
		}
		requestScopes[id] = 0
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}

	scopeQuery := `SELECT s.request_id,s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,s.group_revision,
		s.rpm_limit,s.concurrency_limit,NULL,NULL,'','','shadow',s.shadow_tpm,s.shadow_cost_micro,s.shadow_currency,s.shadow_window,r.employee_id,r.key_id
		FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
		ORDER BY s.request_id,s.scope_kind,s.scope_id`
	if version == 2 {
		scopeQuery = `SELECT s.request_id,s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,s.group_revision,
			s.rpm_limit,s.concurrency_limit,s.hard_tpm,s.hard_cost_micro,s.hard_currency,s.hard_window,s.unknown_mode,
			s.shadow_tpm,s.shadow_cost_micro,s.shadow_currency,s.shadow_window,r.employee_id,r.key_id
			FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
			ORDER BY s.request_id,s.scope_kind,s.scope_id`
	}
	scopeRows, err := tx.QueryContext(ctx, scopeQuery)
	if err != nil {
		return ErrUnavailable
	}
	for scopeRows.Next() {
		var requestID, kind, scopeID, policyID, hardCurrency, hardWindow, unknownMode, currency, window, employeeID, keyID string
		var policyRevision int64
		var groupRevision, rpm, concurrency, hardTPM, hardCost, shadowTPM, shadowCost sql.NullInt64
		if err := scopeRows.Scan(&requestID, &kind, &scopeID, &policyID, &policyRevision, &groupRevision, &rpm, &concurrency,
			&hardTPM, &hardCost, &hardCurrency, &hardWindow, &unknownMode,
			&shadowTPM, &shadowCost, &currency, &window, &employeeID, &keyID); err != nil {
			scopeRows.Close()
			return ErrSchema
		}
		storedScope := ScopeSnapshot{Kind: ScopeKind(kind), ID: scopeID, PolicyID: policyID, PolicyRevision: policyRevision,
			HardCurrency: hardCurrency, HardWindow: hardWindow, UnknownMode: unknownMode, ShadowCurrency: currency, ShadowWindow: window}
		setOptionalInt64(&storedScope.GroupRevision, groupRevision)
		setOptionalInt64(&storedScope.RPMLimit, rpm)
		setOptionalInt64(&storedScope.ConcurrencyLimit, concurrency)
		setOptionalInt64(&storedScope.HardTPM, hardTPM)
		setOptionalInt64(&storedScope.HardCostMicro, hardCost)
		setOptionalInt64(&storedScope.ShadowTPM, shadowTPM)
		setOptionalInt64(&storedScope.ShadowCostMicro, shadowCost)
		if !validScopeKind(ScopeKind(kind)) || !validMetadata(scopeID, 256) || !validMetadata(policyID, 256) || !validRevision(policyRevision) ||
			!validScopeGroupRevision(storedScope) || !validStoredLimit(rpm) || !validStoredLimit(concurrency) ||
			!validStoredSafeLimit(hardTPM) || !validStoredLimit(hardCost) || !validHardCost(storedScope) || !validUnknownMode(unknownMode) ||
			!validStoredLimit(shadowTPM) || !validStoredLimit(shadowCost) || !validShadowCost(storedScope) ||
			!rpm.Valid && !concurrency.Valid && !hardTPM.Valid && !hardCost.Valid && !shadowTPM.Valid && !shadowCost.Valid ||
			kind == string(ScopeEmployee) && scopeID != employeeID || kind == string(ScopeKey) && scopeID != keyID {
			scopeRows.Close()
			return ErrSchema
		}
		requestScopes[requestID]++
	}
	iterationErr = scopeRows.Err()
	closeErr = scopeRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	for _, count := range requestScopes {
		if count == 0 {
			return ErrSchema
		}
	}
	if latestEffective != nil && (settings.LastEffectiveAdmissionTime == nil || settings.LastEffectiveAdmissionTime.Before(*latestEffective)) {
		return ErrSchema
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(governance_request_scopes)`)
	if err != nil {
		return ErrUnavailable
	}
	foreignViolation := foreignRows.Next()
	iterationErr = foreignRows.Err()
	closeErr = foreignRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if foreignViolation {
		return ErrSchema
	}
	return nil
}

func validStoredState(status accounting.Status, observed, effective, released *time.Time) bool {
	switch status {
	case accounting.StatusPending:
		return observed == nil && effective == nil && released == nil
	case accounting.StatusInterrupted:
		return observed != nil && effective != nil
	case accounting.StatusSucceeded, accounting.StatusFailed, accounting.StatusCancelled:
		return observed != nil && effective != nil && released != nil
	default:
		return false
	}
}

func validStoredLimit(value sql.NullInt64) bool { return !value.Valid || value.Int64 > 0 }

func validStoredSafeLimit(value sql.NullInt64) bool {
	return !value.Valid || value.Int64 > 0 && value.Int64 <= MaxRevision
}

func readSettingsVersion(ctx context.Context, tx *sql.Tx, version int) (Settings, error) {
	if version == 2 {
		return readSettings(ctx, tx)
	}
	var enabled int
	var revision int64
	var last sql.NullString
	var updated string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,revision,last_effective_admission_at,updated_at FROM governance_settings WHERE singleton=1`).
		Scan(&enabled, &revision, &last, &updated); err != nil {
		return Settings{}, ErrUnavailable
	}
	if enabled < 0 || enabled > 1 || !validRevision(revision) {
		return Settings{}, ErrSchema
	}
	updatedAt, err := parseStoredTime(updated)
	if err != nil {
		return Settings{}, err
	}
	lastAt, err := pointerTime(last)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Enabled: enabled == 1, Revision: revision, LastEffectiveAdmissionTime: lastAt, UpdatedAt: updatedAt}, nil
}

func storedDDL(statement string) string {
	statement = strings.Replace(statement, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	return strings.Replace(statement, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX", 1)
}

func normalizeDDL(statement string) string {
	var normalized strings.Builder
	normalized.Grow(len(statement))
	var quote byte
	for index := 0; index < len(statement); index++ {
		character := statement[index]
		if quote != 0 {
			normalized.WriteByte(character)
			if character == quote {
				if index+1 < len(statement) && statement[index+1] == quote {
					index++
					normalized.WriteByte(statement[index])
				} else {
					quote = 0
				}
			}
			continue
		}
		switch character {
		case '\'', '"', '`':
			quote = character
			normalized.WriteByte(character)
		case ' ', '\t', '\r', '\n':
			continue
		default:
			if character >= 'A' && character <= 'Z' {
				character += 'a' - 'A'
			}
			normalized.WriteByte(character)
		}
	}
	return normalized.String()
}
