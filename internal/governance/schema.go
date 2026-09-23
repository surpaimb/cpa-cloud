package governance

import (
	"context"
	"database/sql"
	"strings"
)

const settingsDDL = `CREATE TABLE IF NOT EXISTS governance_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(revision>=1),
	last_effective_admission_at TEXT,
	updated_at TEXT NOT NULL
)`

const requestsDDL = `CREATE TABLE IF NOT EXISTS governance_requests (
	id TEXT PRIMARY KEY,
	employee_id TEXT NOT NULL,
	key_id TEXT NOT NULL,
	public_model TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	settings_revision INTEGER NOT NULL CHECK(settings_revision>=1),
	observed_started_at TEXT NOT NULL,
	effective_started_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	observed_finished_at TEXT,
	effective_finished_at TEXT,
	released_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
	CHECK(effective_started_at>=observed_started_at),
	CHECK(expires_at>effective_started_at),
	CHECK(effective_finished_at IS NULL OR effective_finished_at>=effective_started_at),
	CHECK(released_at IS NULL OR released_at>=effective_started_at),
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
	policy_revision INTEGER NOT NULL CHECK(policy_revision>=1),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR rpm_limit>0),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR concurrency_limit>0),
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
	defer rows.Close()
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
	if err := rows.Err(); err != nil {
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

func storedDDL(statement string) string {
	statement = strings.Replace(statement, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	return strings.Replace(statement, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX", 1)
}

func normalizeDDL(statement string) string {
	return strings.Map(func(character rune) rune {
		switch character {
		case ' ', '\t', '\r', '\n', '`', '"':
			return -1
		default:
			return character
		}
	}, strings.ToLower(statement))
}
