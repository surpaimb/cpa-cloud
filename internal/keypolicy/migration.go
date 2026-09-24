// Independently authored KEY-02 SQLite migration and schema validation.
package keypolicy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	policiesTable  = "access_key_policies"
	protocolsTable = "access_key_policy_protocols"
	modelsTable    = "access_key_policy_models"
)

func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return ErrInvalidSchema
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin key policy migration: %w", err)
	}
	defer tx.Rollback()
	var foreignKeys int
	if err := tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return fmt.Errorf("%w: foreign keys must be enabled", ErrInvalidSchema)
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS access_key_policies (
			key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_keys(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
			protocol_mode TEXT NOT NULL CHECK(protocol_mode IN ('all','selected')),
			model_mode TEXT NOT NULL CHECK(model_mode IN ('all','selected')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS access_key_policy_protocols (
			key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat','openai-responses','anthropic-messages','gemini-generate-content')),
			PRIMARY KEY(key_id,protocol)
		)`,
		`CREATE TABLE IF NOT EXISTS access_key_policy_models (
			key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			model_id TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT,
			PRIMARY KEY(key_id,model_id)
		)`,
		`CREATE INDEX IF NOT EXISTS access_key_policies_revision_idx ON access_key_policies(key_id,revision)`,
		`CREATE INDEX IF NOT EXISTS access_key_policy_protocols_protocol_idx ON access_key_policy_protocols(protocol,key_id)`,
		`CREATE INDEX IF NOT EXISTS access_key_policy_models_model_idx ON access_key_policy_models(model_id,key_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create key policy schema: %w", err)
		}
	}
	if err := verifySchema(ctx, tx); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at)
		SELECT id,1,'all','all',?,? FROM access_keys WHERE NOT EXISTS(SELECT 1 FROM access_key_policies p WHERE p.key_id=access_keys.id)`, stamp, stamp); err != nil {
		return fmt.Errorf("backfill key policies: %w", err)
	}
	var missing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_keys k LEFT JOIN access_key_policies p ON p.key_id=k.id WHERE p.key_id IS NULL`).Scan(&missing); err != nil || missing != 0 {
		if err != nil {
			return fmt.Errorf("verify key policy coverage: %w", err)
		}
		return fmt.Errorf("%w: access key without policy", ErrInvalidSchema)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit key policy migration: %w", err)
	}
	return nil
}

type columnSpec struct {
	kind    string
	notNull bool
	pk      int
}

func verifySchema(ctx context.Context, tx *sql.Tx) error {
	expectedColumns := map[string]map[string]columnSpec{
		policiesTable: {
			"key_id": {"TEXT", true, 1}, "revision": {"INTEGER", true, 0},
			"protocol_mode": {"TEXT", true, 0}, "model_mode": {"TEXT", true, 0},
			"created_at": {"TEXT", true, 0}, "updated_at": {"TEXT", true, 0},
		},
		protocolsTable: {"key_id": {"TEXT", true, 1}, "protocol": {"TEXT", true, 2}},
		modelsTable:    {"key_id": {"TEXT", true, 1}, "model_id": {"TEXT", true, 2}},
	}
	for table, expected := range expectedColumns {
		actual, err := readColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if len(actual) != len(expected) {
			return fmt.Errorf("%w: unexpected columns on %s", ErrInvalidSchema, table)
		}
		for name, want := range expected {
			got, ok := actual[name]
			if !ok || got != want {
				return fmt.Errorf("%w: invalid column %s.%s", ErrInvalidSchema, table, name)
			}
		}
	}
	checks := map[string][]string{
		policiesTable:  {"check(revisionbetween1and9007199254740991)", "check(protocol_modein('all','selected'))", "check(model_modein('all','selected'))"},
		protocolsTable: {"check(protocolin('openai-chat','openai-responses','anthropic-messages','gemini-generate-content'))"},
	}
	for table, fragments := range checks {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&raw); err != nil {
			return fmt.Errorf("%w: read table %s", ErrInvalidSchema, table)
		}
		normalized := normalizeDDL(raw)
		for _, fragment := range fragments {
			if !strings.Contains(normalized, fragment) {
				return fmt.Errorf("%w: missing constraint on %s", ErrInvalidSchema, table)
			}
		}
	}
	if err := verifyForeignKeys(ctx, tx, policiesTable, []foreignKeySpec{{"key_id", "access_keys", "id", "CASCADE"}}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, protocolsTable, []foreignKeySpec{{"key_id", policiesTable, "key_id", "CASCADE"}}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, modelsTable, []foreignKeySpec{{"key_id", policiesTable, "key_id", "CASCADE"}, {"model_id", "models", "id", "RESTRICT"}}); err != nil {
		return err
	}
	for name, columns := range map[string][]string{
		"access_key_policies_revision_idx":         {"key_id", "revision"},
		"access_key_policy_protocols_protocol_idx": {"protocol", "key_id"},
		"access_key_policy_models_model_idx":       {"model_id", "key_id"},
	} {
		if err := verifyIndex(ctx, tx, name, columns); err != nil {
			return err
		}
	}
	return nil
}

func readColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]columnSpec, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect %s", ErrInvalidSchema, table)
	}
	defer rows.Close()
	result := map[string]columnSpec{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("%w: inspect %s", ErrInvalidSchema, table)
		}
		result[name] = columnSpec{strings.ToUpper(kind), notNull == 1, pk}
	}
	return result, rows.Err()
}

type foreignKeySpec struct{ from, table, to, onDelete string }

func verifyForeignKeys(ctx context.Context, tx *sql.Tx, table string, expected []foreignKeySpec) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return fmt.Errorf("%w: inspect foreign keys on %s", ErrInvalidSchema, table)
	}
	defer rows.Close()
	actual := make([]foreignKeySpec, 0)
	for rows.Next() {
		var id, seq int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return fmt.Errorf("%w: inspect foreign keys on %s", ErrInvalidSchema, table)
		}
		actual = append(actual, foreignKeySpec{from, target, to, strings.ToUpper(onDelete)})
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: unexpected foreign keys on %s", ErrInvalidSchema, table)
	}
	for _, want := range expected {
		found := false
		for _, got := range actual {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: missing foreign key on %s", ErrInvalidSchema, table)
		}
	}
	return rows.Err()
}

func verifyIndex(ctx context.Context, tx *sql.Tx, name string, expected []string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA index_info(`+name+`)`)
	if err != nil {
		return fmt.Errorf("%w: inspect index %s", ErrInvalidSchema, name)
	}
	defer rows.Close()
	actual := make([]string, 0)
	for rows.Next() {
		var seq, cid int
		var column string
		if err := rows.Scan(&seq, &cid, &column); err != nil {
			return fmt.Errorf("%w: inspect index %s", ErrInvalidSchema, name)
		}
		actual = append(actual, column)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%w: invalid index %s", ErrInvalidSchema, name)
	}
	for index := range expected {
		if actual[index] != expected[index] {
			return fmt.Errorf("%w: invalid index %s", ErrInvalidSchema, name)
		}
	}
	return rows.Err()
}

func normalizeDDL(value string) string {
	replacer := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "", "`", "", `"`, "")
	return strings.ToLower(replacer.Replace(value))
}
