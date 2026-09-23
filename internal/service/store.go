package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type store struct {
	db *sql.DB
}

func openStore(dataDir string) (*store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	dbPath := filepath.Join(dataDir, "cpa-cloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS admins (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash BLOB NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			token_digest BLOB NOT NULL UNIQUE,
			csrf_token TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_expires_idx ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS codex_oauth_sessions (
			id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			admin_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			state_digest BLOB NOT NULL UNIQUE,
			secret_ciphertext BLOB NOT NULL,
			name TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS codex_oauth_sessions_expiry_idx ON codex_oauth_sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS employees (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			department TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('active','disabled')),
			model_mode TEXT NOT NULL CHECK(model_mode IN ('all','selected')),
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS employee_models (
			employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
			model_id TEXT NOT NULL,
			PRIMARY KEY(employee_id, model_id)
		)`,
		`CREATE TABLE IF NOT EXISTS access_keys (
			id TEXT PRIMARY KEY,
			employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			selector TEXT NOT NULL UNIQUE,
			digest BLOB NOT NULL,
			digest_version INTEGER NOT NULL,
			operation_id TEXT NOT NULL,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT NOT NULL,
			UNIQUE(employee_id, operation_id)
		)`,
		`CREATE INDEX IF NOT EXISTS access_keys_employee_idx ON access_keys(employee_id)`,
		`CREATE TABLE IF NOT EXISTS upstreams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
			endpoint TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			credential_ciphertext BLOB NOT NULL,
			key_version INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required')),
			verified_at TEXT,
			operation_id TEXT UNIQUE,
			CHECK(
				(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL)
				OR
				(provider_kind = 'codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL)
			)
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			upstream_id TEXT NOT NULL REFERENCES upstreams(id),
			upstream_model TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_requests (
			id TEXT PRIMARY KEY,
			employee_id TEXT NOT NULL REFERENCES employees(id),
			key_id TEXT NOT NULL REFERENCES access_keys(id),
			model_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			outcome TEXT NOT NULL CHECK(outcome IN ('running','succeeded','failed','cancelled','interrupted')),
			upstream_status INTEGER
		)`,
		`UPDATE model_requests SET outcome='interrupted', finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE outcome='running'`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize database: %w", err)
		}
	}
	if err := s.migrateUpstreamsForCodexMembership(ctx); err != nil {
		return fmt.Errorf("migrate upstreams: %w", err)
	}
	if err := s.migrateCodexOAuthBindings(ctx); err != nil {
		return fmt.Errorf("migrate Codex OAuth bindings: %w", err)
	}
	if err := s.migrateCodexOAuthLifecycle(ctx); err != nil {
		return fmt.Errorf("migrate Codex OAuth lifecycle: %w", err)
	}
	if err := s.migrateUpstreamBatchItems(ctx); err != nil {
		return fmt.Errorf("migrate upstream batch items: %w", err)
	}
	if err := s.migrateAccountPools(ctx); err != nil {
		return fmt.Errorf("migrate account pools: %w", err)
	}
	if err := s.migrateAccountPoolRuntime(ctx); err != nil {
		return fmt.Errorf("migrate account pool runtime: %w", err)
	}
	return nil
}

const codexOAuthRefreshStateTable = "codex_oauth_refresh_states"

func (s *store) migrateCodexOAuthLifecycle(ctx context.Context) error {
	columns, err := tableColumns(ctx, s.db, "codex_oauth_sessions")
	if err != nil {
		return err
	}
	var refreshTableCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, codexOAuthRefreshStateTable).Scan(&refreshTableCount); err != nil {
		return err
	}
	if refreshTableCount != 0 {
		refreshColumns, err := tableColumns(ctx, s.db, codexOAuthRefreshStateTable)
		if err != nil {
			return err
		}
		var schema string
		if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, codexOAuthRefreshStateTable).Scan(&schema); err != nil {
			return err
		}
		constraints, err := tableColumnConstraints(ctx, s.db, codexOAuthRefreshStateTable)
		if err != nil {
			return err
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(schema), " "))
		if !refreshColumns["upstream_id"] || !refreshColumns["state"] || !refreshColumns["reason_code"] ||
			!refreshColumns["attempt_revision"] || !refreshColumns["updated_at"] ||
			constraints["upstream_id"].primaryKey != 1 || !constraints["state"].notNull || !constraints["updated_at"].notNull ||
			!strings.Contains(normalized, "references upstreams(id) on delete cascade") ||
			!strings.Contains(normalized, "check(state in ('ready','in_progress','paused','reauth_required'))") {
			return errors.New("existing Codex OAuth refresh state table has an incompatible schema")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, migration := range []struct {
		name string
		sql  string
	}{
		{"status", `ALTER TABLE codex_oauth_sessions ADD COLUMN status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','exchanging','succeeded','failed','cancelled','expired'))`},
		{"upstream_id", `ALTER TABLE codex_oauth_sessions ADD COLUMN upstream_id TEXT REFERENCES upstreams(id) ON DELETE SET NULL`},
		{"error_code", `ALTER TABLE codex_oauth_sessions ADD COLUMN error_code TEXT`},
	} {
		if !columns[migration.name] {
			if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE codex_oauth_sessions
		SET status='failed',error_code='legacy_unknown_outcome'
		WHERE status='pending' AND used_at IS NOT NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS codex_oauth_refresh_states (
		upstream_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
		state TEXT NOT NULL CHECK(state IN ('ready','in_progress','paused','reauth_required')),
		reason_code TEXT,
		attempt_revision INTEGER,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO codex_oauth_refresh_states(upstream_id,state,reason_code,attempt_revision,updated_at)
		SELECT b.upstream_id,
			CASE WHEN u.credential_state='reauth_required' THEN 'reauth_required' ELSE 'ready' END,
			NULL,u.revision,?
		FROM codex_oauth_bindings b JOIN upstreams u ON u.id=b.upstream_id
		WHERE NOT EXISTS(SELECT 1 FROM codex_oauth_refresh_states r WHERE r.upstream_id=b.upstream_id)`, utcNow()); err != nil {
		return err
	}
	return tx.Commit()
}

type tableColumnConstraint struct {
	notNull    bool
	primaryKey int
}

func tableColumnConstraints(ctx context.Context, db *sql.DB, table string) (map[string]tableColumnConstraint, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	constraints := make(map[string]tableColumnConstraint)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		constraints[name] = tableColumnConstraint{notNull: notNull != 0, primaryKey: primaryKey}
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return constraints, nil
}

const codexOAuthBindingTable = "codex_oauth_bindings"

func (s *store) migrateCodexOAuthBindings(ctx context.Context) error {
	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, codexOAuthBindingTable).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		columns, err := tableColumns(ctx, s.db, codexOAuthBindingTable)
		if err != nil {
			return err
		}
		var schema string
		if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, codexOAuthBindingTable).Scan(&schema); err != nil {
			return err
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(schema), " "))
		if !columns["upstream_id"] || !columns["client_id"] || !columns["source"] || !columns["created_at"] ||
			!strings.Contains(normalized, "references upstreams(id) on delete cascade") || !strings.Contains(normalized, "check(source = 'authorization_code')") {
			return errors.New("existing Codex OAuth binding table has an incompatible schema")
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE codex_oauth_bindings (
		upstream_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
		client_id TEXT NOT NULL,
		source TEXT NOT NULL CHECK(source = 'authorization_code'),
		created_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if violated {
		return errors.New("foreign key check failed")
	}
	if closeErr != nil {
		return closeErr
	}
	return tx.Commit()
}

const upstreamMigrationTable = "_upstreams_membership_migration"

func (s *store) migrateUpstreamsForCodexMembership(ctx context.Context) error {
	var schema string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='upstreams'`).Scan(&schema); err != nil {
		return err
	}
	columns, err := tableColumns(ctx, s.db, "upstreams")
	if err != nil {
		return err
	}
	lowerSchema := strings.ToLower(schema)
	if columns["credential_state"] && columns["verified_at"] && columns["operation_id"] && strings.Contains(lowerSchema, "codex-membership") && strings.Contains(lowerSchema, "anthropic-api-key") && strings.Contains(lowerSchema, "gemini-api-key") {
		return nil
	}

	// SQLite cannot widen a CHECK constraint in place. Foreign-key enforcement
	// is disabled only around one transaction that rebuilds this table; the
	// transaction runs foreign_key_check before commit and every exit restores
	// enforcement, so a failed migration leaves the old schema and rows intact.
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	restoreForeignKeys := func() error {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, restoreErr := s.db.ExecContext(cleanupContext, `PRAGMA foreign_keys = ON`); restoreErr != nil {
			return restoreErr
		}
		var enabled int
		if restoreErr := s.db.QueryRowContext(cleanupContext, `PRAGMA foreign_keys`).Scan(&enabled); restoreErr != nil {
			return restoreErr
		}
		if enabled != 1 {
			return errors.New("foreign key enforcement was not restored")
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		_ = restoreForeignKeys()
		return err
	}
	committed := false
	foreignKeysRestored := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
		if !foreignKeysRestored {
			_ = restoreForeignKeys()
		}
	}()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE `+upstreamMigrationTable+` (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
		endpoint TEXT NOT NULL,
		enabled INTEGER NOT NULL,
		credential_ciphertext BLOB NOT NULL,
		key_version INTEGER NOT NULL,
		revision INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required')),
		verified_at TEXT,
		operation_id TEXT UNIQUE,
		CHECK(
			(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL)
			OR
			(provider_kind = 'codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL)
		)
	)`); err != nil {
		return err
	}
	stateExpr, verifiedExpr, operationExpr := "NULL", "NULL", "NULL"
	if columns["credential_state"] {
		stateExpr = "credential_state"
	}
	if columns["verified_at"] {
		verifiedExpr = "verified_at"
	}
	if columns["operation_id"] {
		operationExpr = "operation_id"
	}
	copySQL := fmt.Sprintf(`INSERT INTO %s(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) SELECT id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,%s,%s,%s FROM upstreams`,
		upstreamMigrationTable, stateExpr, verifiedExpr, operationExpr)
	if _, err := tx.ExecContext(ctx, copySQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE upstreams`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+upstreamMigrationTable+` RENAME TO upstreams`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if violated {
		return errors.New("foreign key check failed")
	}
	if closeErr != nil {
		return closeErr
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if err := restoreForeignKeys(); err != nil {
		return err
	}
	foreignKeysRestored = true
	return nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return columns, nil
}

func (s *store) close() error { return s.db.Close() }

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func isConflict(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr interface{ Code() int }
	return errors.As(err, &sqliteErr) && (sqliteErr.Code() == 19 || sqliteErr.Code() == 2067 || sqliteErr.Code() == 1555)
}
