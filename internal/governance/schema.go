package governance

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const settingsDDL = `CREATE TABLE IF NOT EXISTS governance_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	last_effective_admission_at TEXT,
	updated_at TEXT NOT NULL
)`

const requestsDDL = `CREATE TABLE IF NOT EXISTS governance_requests (
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

const scopesDDL = `CREATE TABLE IF NOT EXISTS governance_request_scopes (
	request_id TEXT NOT NULL REFERENCES governance_requests(id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit>0)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit>0)),
	PRIMARY KEY(request_id,scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL)
)`

const scopesIndexDDL = `CREATE INDEX IF NOT EXISTS governance_request_scopes_scope_idx
	ON governance_request_scopes(scope_kind,scope_id,request_id)`

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
	for _, statement := range []string{settingsDDL, requestsDDL, scopesDDL, scopesIndexDDL} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrUnavailable
		}
	}
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO governance_settings(singleton,enabled,revision,updated_at) VALUES(1,0,1,?)`, initialSettingsTime); err != nil {
		return ErrUnavailable
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_settings`).Scan(&count); err != nil {
		return ErrUnavailable
	}
	if count != 1 {
		return ErrSchema
	}
	if err := validateStoredData(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
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
	var indexType, indexTable, indexDefinition string
	if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name='governance_request_scopes_scope_idx'`).Scan(&indexType, &indexTable, &indexDefinition); err != nil {
		return ErrSchema
	}
	if indexType != "index" || indexTable != "governance_request_scopes" || normalizeDDL(indexDefinition) != normalizeDDL(storedDDL(scopesIndexDDL)) {
		return ErrSchema
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
		if name != "governance_request_scopes_scope_idx" || normalizeDDL(definition) != normalizeDDL(storedDDL(scopesIndexDDL)) {
			return ErrSchema
		}
		count++
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if count != 1 {
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
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,employee_id,key_id,public_model,protocol,settings_revision,
		observed_started_at,effective_started_at,effective_lease_at,expires_at,
		observed_finished_at,effective_finished_at,released_at,status FROM governance_requests ORDER BY id`)
	if err != nil {
		return ErrUnavailable
	}
	requestScopes := make(map[string]int)
	var latestEffective *time.Time
	for rows.Next() {
		var id, employeeID, keyID, publicModel, protocol, observedStart, effectiveStart, effectiveLease, expires, status string
		var settingsRevision int64
		var observedFinish, effectiveFinish, released sql.NullString
		if err := rows.Scan(&id, &employeeID, &keyID, &publicModel, &protocol, &settingsRevision,
			&observedStart, &effectiveStart, &effectiveLease, &expires, &observedFinish, &effectiveFinish, &released, &status); err != nil {
			rows.Close()
			return ErrSchema
		}
		if !validMetadata(id, 256) || !validMetadata(employeeID, 256) || !validMetadata(keyID, 256) || !validMetadata(publicModel, 256) ||
			!validProtocol(accounting.UsageProtocol(protocol)) || !validRevision(settingsRevision) {
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

	scopeRows, err := tx.QueryContext(ctx, `SELECT s.request_id,s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,
		s.rpm_limit,s.concurrency_limit,r.employee_id,r.key_id
		FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
		ORDER BY s.request_id,s.scope_kind,s.scope_id`)
	if err != nil {
		return ErrUnavailable
	}
	for scopeRows.Next() {
		var requestID, kind, scopeID, policyID, employeeID, keyID string
		var policyRevision int64
		var rpm, concurrency sql.NullInt64
		if err := scopeRows.Scan(&requestID, &kind, &scopeID, &policyID, &policyRevision, &rpm, &concurrency, &employeeID, &keyID); err != nil {
			scopeRows.Close()
			return ErrSchema
		}
		if !validScopeKind(ScopeKind(kind)) || !validMetadata(scopeID, 256) || !validMetadata(policyID, 256) || !validRevision(policyRevision) ||
			!validStoredLimit(rpm) || !validStoredLimit(concurrency) || !rpm.Valid && !concurrency.Valid ||
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
