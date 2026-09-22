package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
			provider_kind TEXT NOT NULL CHECK(provider_kind = 'openai-compatible'),
			endpoint TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			credential_ciphertext BLOB NOT NULL,
			key_version INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL
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
	return nil
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
