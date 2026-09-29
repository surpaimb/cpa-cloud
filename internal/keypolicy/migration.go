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
	policiesTable                     = "access_key_policies"
	protocolsTable                    = "access_key_policy_protocols"
	modelsTable                       = "access_key_policy_models"
	migrationStateTable               = "access_key_policy_migration_state"
	migrationStateVersion             = 1
	sourceMigrationStateTable         = "access_key_policy_source_migration_state"
	sourceMigrationStateVersion       = 1
	sourceCIDRIndex                   = "access_key_policy_source_cidrs_cidr_idx"
	accountGroupMigrationStateTable   = "access_key_policy_account_group_migration_state"
	accountGroupMigrationStateVersion = 1
	accountGroupPoliciesTable         = "access_key_policy_account_groups"
	accountGroupMembersTable          = "access_key_policy_account_group_members"
	accountGroupMemberIndex           = "access_key_policy_account_group_members_group_idx"
)

func Migrate(ctx context.Context, db *sql.DB) error {
	return migrateWithAllHooks(ctx, db, nil, nil, nil)
}

func migrate(ctx context.Context, db *sql.DB, afterSchema func(*sql.Tx) error) error {
	return migrateWithAllHooks(ctx, db, afterSchema, nil, nil)
}

func migrateWithHooks(ctx context.Context, db *sql.DB, afterSchema, beforeSourceMarker func(*sql.Tx) error) error {
	return migrateWithAllHooks(ctx, db, afterSchema, beforeSourceMarker, nil)
}

func migrateWithAccountGroupHook(ctx context.Context, db *sql.DB, beforeAccountGroupMarker func(*sql.Tx) error) error {
	return migrateWithAllHooks(ctx, db, nil, nil, beforeAccountGroupMarker)
}

func migrateWithAllHooks(ctx context.Context, db *sql.DB, afterSchema, beforeSourceMarker, beforeAccountGroupMarker func(*sql.Tx) error) error {
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
	anyPresent, markerPresent, err := migrationObjectsPresent(ctx, tx)
	if err != nil {
		return err
	}
	if anyPresent && !markerPresent {
		return fmt.Errorf("%w: unmarked or partial key policy schema", ErrInvalidSchema)
	}
	if markerPresent {
		if err := verifySchema(ctx, tx); err != nil {
			return err
		}
		if err := verifyMigrationState(ctx, tx); err != nil {
			return err
		}
		if err := verifyCoverage(ctx, tx); err != nil {
			return err
		}
		if err := migrateSourcePolicy(ctx, tx, beforeSourceMarker); err != nil {
			return err
		}
		if err := migrateAccountGroupPolicy(ctx, tx, beforeAccountGroupMarker); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit key policy migration verification: %w", err)
		}
		return nil
	}
	statements := []string{
		`CREATE TABLE access_key_policy_migration_state (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton=1),
			version INTEGER NOT NULL CHECK(version=1),
			completed_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_key_policies (
			key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_keys(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
			protocol_mode TEXT NOT NULL CHECK(protocol_mode IN ('all','selected')),
			model_mode TEXT NOT NULL CHECK(model_mode IN ('all','selected')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_key_policy_protocols (
			key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat','openai-responses','anthropic-messages','gemini-generate-content')),
			PRIMARY KEY(key_id,protocol)
		)`,
		`CREATE TABLE access_key_policy_models (
			key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			model_id TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT,
			PRIMARY KEY(key_id,model_id)
		)`,
		`CREATE INDEX access_key_policies_revision_idx ON access_key_policies(key_id,revision)`,
		`CREATE INDEX access_key_policy_protocols_protocol_idx ON access_key_policy_protocols(protocol,key_id)`,
		`CREATE INDEX access_key_policy_models_model_idx ON access_key_policy_models(model_id,key_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create key policy schema: %w", err)
		}
	}
	if err := verifySchema(ctx, tx); err != nil {
		return err
	}
	if afterSchema != nil {
		if err := afterSchema(tx); err != nil {
			return err
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at)
		SELECT id,1,'all','all',?,? FROM access_keys WHERE NOT EXISTS(SELECT 1 FROM access_key_policies p WHERE p.key_id=access_keys.id)`, stamp, stamp); err != nil {
		return fmt.Errorf("backfill key policies: %w", err)
	}
	if err := verifyCoverage(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_migration_state(singleton,version,completed_at) VALUES(1,?,?)`, migrationStateVersion, stamp); err != nil {
		return fmt.Errorf("write key policy migration marker: %w", err)
	}
	if err := verifyMigrationState(ctx, tx); err != nil {
		return err
	}
	if err := migrateSourcePolicy(ctx, tx, beforeSourceMarker); err != nil {
		return err
	}
	if err := migrateAccountGroupPolicy(ctx, tx, beforeAccountGroupMarker); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit key policy migration: %w", err)
	}
	return nil
}

func migrateSourcePolicy(ctx context.Context, tx *sql.Tx, beforeMarker func(*sql.Tx) error) error {
	anyPresent, markerPresent, err := sourceMigrationObjectsPresent(ctx, tx)
	if err != nil {
		return err
	}
	if anyPresent && !markerPresent {
		return fmt.Errorf("%w: unmarked or partial key policy source schema", ErrInvalidSchema)
	}
	if markerPresent {
		if err := verifySourceSchema(ctx, tx); err != nil {
			return err
		}
		if err := verifySourceMigrationState(ctx, tx); err != nil {
			return err
		}
		return verifySourceCoverage(ctx, tx)
	}
	for _, statement := range []string{
		`CREATE TABLE access_key_policy_source_migration_state (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton=1),
			version INTEGER NOT NULL CHECK(version=1),
			completed_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_key_policy_sources (
			key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			source_mode TEXT NOT NULL CHECK(source_mode IN ('all','selected'))
		)`,
		`CREATE TABLE access_key_policy_source_cidrs (
			key_id TEXT NOT NULL REFERENCES access_key_policy_sources(key_id) ON DELETE CASCADE,
			cidr TEXT NOT NULL CHECK(length(cidr) BETWEEN 1 AND 64),
			PRIMARY KEY(key_id,cidr)
		)`,
		`CREATE INDEX access_key_policy_source_cidrs_cidr_idx ON access_key_policy_source_cidrs(cidr,key_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create key policy source schema: %w", err)
		}
	}
	if err := verifySourceSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_sources(key_id,source_mode)
		SELECT key_id,'all' FROM access_key_policies`); err != nil {
		return fmt.Errorf("backfill key policy sources: %w", err)
	}
	if err := verifySourceCoverage(ctx, tx); err != nil {
		return err
	}
	if beforeMarker != nil {
		if err := beforeMarker(tx); err != nil {
			return err
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_source_migration_state(singleton,version,completed_at) VALUES(1,?,?)`, sourceMigrationStateVersion, stamp); err != nil {
		return fmt.Errorf("write key policy source migration marker: %w", err)
	}
	return verifySourceMigrationState(ctx, tx)
}

func sourceMigrationObjectsPresent(ctx context.Context, tx *sql.Tx) (anyPresent bool, markerPresent bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE name IN (
		'access_key_policy_source_migration_state','access_key_policy_sources','access_key_policy_source_cidrs',
		'access_key_policy_source_cidrs_cidr_idx'
	)`)
	if err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy source migration objects", ErrInvalidSchema)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, false, fmt.Errorf("%w: inspect key policy source migration objects", ErrInvalidSchema)
		}
		anyPresent = true
		if name == sourceMigrationStateTable {
			markerPresent = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy source migration objects", ErrInvalidSchema)
	}
	return anyPresent, markerPresent, nil
}

func verifySourceMigrationState(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT singleton,version,completed_at FROM access_key_policy_source_migration_state`)
	if err != nil {
		return fmt.Errorf("%w: read key policy source migration marker", ErrInvalidSchema)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var singleton, version int
		var completedAt string
		if err := rows.Scan(&singleton, &version, &completedAt); err != nil || singleton != 1 || version != sourceMigrationStateVersion {
			return fmt.Errorf("%w: invalid key policy source migration marker", ErrInvalidSchema)
		}
		if _, err := time.Parse(time.RFC3339Nano, completedAt); err != nil {
			return fmt.Errorf("%w: invalid key policy source migration timestamp", ErrInvalidSchema)
		}
	}
	if err := rows.Err(); err != nil || count != 1 {
		return fmt.Errorf("%w: invalid key policy source migration marker count", ErrInvalidSchema)
	}
	return nil
}

func verifySourceCoverage(ctx context.Context, tx *sql.Tx) error {
	var missing, orphaned, orphanedCIDRs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policies p LEFT JOIN access_key_policy_sources s ON s.key_id=p.key_id WHERE s.key_id IS NULL`).Scan(&missing); err != nil {
		return fmt.Errorf("verify key policy source coverage: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policy_sources s LEFT JOIN access_key_policies p ON p.key_id=s.key_id WHERE p.key_id IS NULL`).Scan(&orphaned); err != nil {
		return fmt.Errorf("verify key policy source ownership: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policy_source_cidrs c LEFT JOIN access_key_policy_sources s ON s.key_id=c.key_id WHERE s.key_id IS NULL`).Scan(&orphanedCIDRs); err != nil {
		return fmt.Errorf("verify key policy source CIDR ownership: %w", err)
	}
	if missing != 0 || orphaned != 0 || orphanedCIDRs != 0 {
		return fmt.Errorf("%w: incomplete key policy source coverage", ErrInvalidSchema)
	}
	return nil
}

func verifySourceSchema(ctx context.Context, tx *sql.Tx) error {
	expectedColumns := map[string]map[string]columnSpec{
		sourceMigrationStateTable: {
			"singleton": {"INTEGER", true, 1}, "version": {"INTEGER", true, 0}, "completed_at": {"TEXT", true, 0},
		},
		sourcesTable: {
			"key_id": {"TEXT", true, 1}, "source_mode": {"TEXT", true, 0},
		},
		sourceCIDRsTable: {
			"key_id": {"TEXT", true, 1}, "cidr": {"TEXT", true, 2},
		},
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
			if got, ok := actual[name]; !ok || got != want {
				return fmt.Errorf("%w: invalid column %s.%s", ErrInvalidSchema, table, name)
			}
		}
	}
	checks := map[string][]string{
		sourceMigrationStateTable: {"check(singleton=1)", "check(version=1)"},
		sourcesTable:              {"check(source_modein('all','selected'))"},
		sourceCIDRsTable:          {"check(length(cidr)between1and64)"},
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
	if err := verifyForeignKeys(ctx, tx, sourceMigrationStateTable, []foreignKeySpec{}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, sourcesTable, []foreignKeySpec{{"key_id", policiesTable, "key_id", "CASCADE"}}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, sourceCIDRsTable, []foreignKeySpec{{"key_id", sourcesTable, "key_id", "CASCADE"}}); err != nil {
		return err
	}
	return verifyIndex(ctx, tx, sourceCIDRIndex, []string{"cidr", "key_id"})
}

func migrateAccountGroupPolicy(ctx context.Context, tx *sql.Tx, beforeMarker func(*sql.Tx) error) error {
	anyPresent, markerPresent, err := accountGroupMigrationObjectsPresent(ctx, tx)
	if err != nil {
		return err
	}
	if anyPresent && !markerPresent {
		return fmt.Errorf("%w: unmarked or partial key policy account group schema", ErrInvalidSchema)
	}
	if markerPresent {
		if err := verifyAccountGroupSchema(ctx, tx); err != nil {
			return err
		}
		if err := verifyAccountGroupMigrationState(ctx, tx); err != nil {
			return err
		}
		if err := verifyAccountGroupCoverage(ctx, tx); err != nil {
			return err
		}
		return verifyAccountGroupForeignKeys(ctx, tx)
	}
	for _, statement := range []string{
		`CREATE TABLE access_key_policy_account_group_migration_state (
			singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton=1),
			version INTEGER NOT NULL CHECK(version=1),
			completed_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_key_policy_account_groups (
			key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,
			account_group_mode TEXT NOT NULL CHECK(account_group_mode IN ('all','selected'))
		)`,
		`CREATE TABLE access_key_policy_account_group_members (
			key_id TEXT NOT NULL REFERENCES access_key_policy_account_groups(key_id) ON DELETE CASCADE,
			account_group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE RESTRICT,
			PRIMARY KEY(key_id,account_group_id)
		)`,
		`CREATE INDEX access_key_policy_account_group_members_group_idx ON access_key_policy_account_group_members(account_group_id,key_id)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create key policy account group schema: %w", err)
		}
	}
	if err := verifyAccountGroupSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_account_groups(key_id,account_group_mode)
		SELECT key_id,'all' FROM access_key_policies`); err != nil {
		return fmt.Errorf("backfill key policy account groups: %w", err)
	}
	if err := verifyAccountGroupCoverage(ctx, tx); err != nil {
		return err
	}
	if err := verifyAccountGroupForeignKeys(ctx, tx); err != nil {
		return err
	}
	if beforeMarker != nil {
		if err := beforeMarker(tx); err != nil {
			return err
		}
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_account_group_migration_state(singleton,version,completed_at) VALUES(1,?,?)`, accountGroupMigrationStateVersion, stamp); err != nil {
		return fmt.Errorf("write key policy account group migration marker: %w", err)
	}
	return verifyAccountGroupMigrationState(ctx, tx)
}

func accountGroupMigrationObjectsPresent(ctx context.Context, tx *sql.Tx) (anyPresent bool, markerPresent bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE name IN (
		'access_key_policy_account_group_migration_state','access_key_policy_account_groups',
		'access_key_policy_account_group_members','access_key_policy_account_group_members_group_idx'
	)`)
	if err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy account group migration objects", ErrInvalidSchema)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, false, fmt.Errorf("%w: inspect key policy account group migration objects", ErrInvalidSchema)
		}
		anyPresent = true
		if name == accountGroupMigrationStateTable {
			markerPresent = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy account group migration objects", ErrInvalidSchema)
	}
	return anyPresent, markerPresent, nil
}

func verifyAccountGroupMigrationState(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT singleton,version,completed_at FROM access_key_policy_account_group_migration_state`)
	if err != nil {
		return fmt.Errorf("%w: read key policy account group migration marker", ErrInvalidSchema)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var singleton, version int
		var completedAt string
		if err := rows.Scan(&singleton, &version, &completedAt); err != nil || singleton != 1 || version != accountGroupMigrationStateVersion {
			return fmt.Errorf("%w: invalid key policy account group migration marker", ErrInvalidSchema)
		}
		if _, err := time.Parse(time.RFC3339Nano, completedAt); err != nil {
			return fmt.Errorf("%w: invalid key policy account group migration timestamp", ErrInvalidSchema)
		}
	}
	if err := rows.Err(); err != nil || count != 1 {
		return fmt.Errorf("%w: invalid key policy account group migration marker count", ErrInvalidSchema)
	}
	return nil
}

func verifyAccountGroupCoverage(ctx context.Context, tx *sql.Tx) error {
	var missing, orphaned, orphanedMembers, unknownGroups int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policies p LEFT JOIN access_key_policy_account_groups g ON g.key_id=p.key_id WHERE g.key_id IS NULL`).Scan(&missing); err != nil {
		return fmt.Errorf("verify key policy account group coverage: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policy_account_groups g LEFT JOIN access_key_policies p ON p.key_id=g.key_id WHERE p.key_id IS NULL`).Scan(&orphaned); err != nil {
		return fmt.Errorf("verify key policy account group ownership: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policy_account_group_members m LEFT JOIN access_key_policy_account_groups g ON g.key_id=m.key_id WHERE g.key_id IS NULL`).Scan(&orphanedMembers); err != nil {
		return fmt.Errorf("verify key policy account group member ownership: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policy_account_group_members m LEFT JOIN account_groups g ON g.id=m.account_group_id WHERE g.id IS NULL`).Scan(&unknownGroups); err != nil {
		return fmt.Errorf("verify key policy account group references: %w", err)
	}
	if missing != 0 || orphaned != 0 || orphanedMembers != 0 || unknownGroups != 0 {
		return fmt.Errorf("%w: incomplete key policy account group coverage", ErrInvalidSchema)
	}
	return nil
}

func verifyAccountGroupSchema(ctx context.Context, tx *sql.Tx) error {
	var accountGroupObjectType string
	if err := tx.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name='account_groups'`).Scan(&accountGroupObjectType); err != nil || accountGroupObjectType != "table" {
		return fmt.Errorf("%w: account_groups table is unavailable", ErrInvalidSchema)
	}
	expectedColumns := map[string]map[string]columnSpec{
		accountGroupMigrationStateTable: {
			"singleton": {"INTEGER", true, 1}, "version": {"INTEGER", true, 0}, "completed_at": {"TEXT", true, 0},
		},
		accountGroupPoliciesTable: {
			"key_id": {"TEXT", true, 1}, "account_group_mode": {"TEXT", true, 0},
		},
		accountGroupMembersTable: {
			"key_id": {"TEXT", true, 1}, "account_group_id": {"TEXT", true, 2},
		},
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
			if got, ok := actual[name]; !ok || got != want {
				return fmt.Errorf("%w: invalid column %s.%s", ErrInvalidSchema, table, name)
			}
		}
	}
	checks := map[string][]string{
		accountGroupMigrationStateTable: {"check(singleton=1)", "check(version=1)"},
		accountGroupPoliciesTable:       {"check(account_group_modein('all','selected'))"},
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
	if err := verifyForeignKeys(ctx, tx, accountGroupMigrationStateTable, []foreignKeySpec{}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, accountGroupPoliciesTable, []foreignKeySpec{{"key_id", policiesTable, "key_id", "CASCADE"}}); err != nil {
		return err
	}
	if err := verifyForeignKeys(ctx, tx, accountGroupMembersTable, []foreignKeySpec{
		{"key_id", accountGroupPoliciesTable, "key_id", "CASCADE"},
		{"account_group_id", "account_groups", "id", "RESTRICT"},
	}); err != nil {
		return err
	}
	return verifyIndex(ctx, tx, accountGroupMemberIndex, []string{"account_group_id", "key_id"})
}

func verifyAccountGroupForeignKeys(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{accountGroupPoliciesTable, accountGroupMembersTable} {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return fmt.Errorf("%w: check foreign keys on %s", ErrInvalidSchema, table)
		}
		violated := rows.Next()
		iterationErr := rows.Err()
		closeErr := rows.Close()
		if iterationErr != nil {
			return fmt.Errorf("%w: check foreign keys on %s", ErrInvalidSchema, table)
		}
		if closeErr != nil {
			return fmt.Errorf("%w: check foreign keys on %s", ErrInvalidSchema, table)
		}
		if violated {
			return fmt.Errorf("%w: foreign key violation on %s", ErrInvalidSchema, table)
		}
	}
	return nil
}

func migrationObjectsPresent(ctx context.Context, tx *sql.Tx) (anyPresent bool, markerPresent bool, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE name IN (
		'access_key_policy_migration_state','access_key_policies','access_key_policy_protocols','access_key_policy_models',
		'access_key_policies_revision_idx','access_key_policy_protocols_protocol_idx','access_key_policy_models_model_idx'
	)`)
	if err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy migration objects", ErrInvalidSchema)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, false, fmt.Errorf("%w: inspect key policy migration objects", ErrInvalidSchema)
		}
		anyPresent = true
		if name == migrationStateTable {
			markerPresent = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, false, fmt.Errorf("%w: inspect key policy migration objects", ErrInvalidSchema)
	}
	return anyPresent, markerPresent, nil
}

func verifyMigrationState(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT singleton,version,completed_at FROM access_key_policy_migration_state`)
	if err != nil {
		return fmt.Errorf("%w: read key policy migration marker", ErrInvalidSchema)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var singleton, version int
		var completedAt string
		if err := rows.Scan(&singleton, &version, &completedAt); err != nil || singleton != 1 || version != migrationStateVersion {
			return fmt.Errorf("%w: invalid key policy migration marker", ErrInvalidSchema)
		}
		if _, err := time.Parse(time.RFC3339Nano, completedAt); err != nil {
			return fmt.Errorf("%w: invalid key policy migration timestamp", ErrInvalidSchema)
		}
	}
	if err := rows.Err(); err != nil || count != 1 {
		return fmt.Errorf("%w: invalid key policy migration marker count", ErrInvalidSchema)
	}
	return nil
}

func verifyCoverage(ctx context.Context, tx *sql.Tx) error {
	var missing, orphaned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_keys k LEFT JOIN access_key_policies p ON p.key_id=k.id WHERE p.key_id IS NULL`).Scan(&missing); err != nil {
		return fmt.Errorf("verify key policy coverage: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_key_policies p LEFT JOIN access_keys k ON k.id=p.key_id WHERE k.id IS NULL`).Scan(&orphaned); err != nil {
		return fmt.Errorf("verify key policy ownership: %w", err)
	}
	if missing != 0 || orphaned != 0 {
		return fmt.Errorf("%w: incomplete key policy coverage", ErrInvalidSchema)
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
		migrationStateTable: {
			"singleton": {"INTEGER", true, 1}, "version": {"INTEGER", true, 0}, "completed_at": {"TEXT", true, 0},
		},
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
		migrationStateTable: {"check(singleton=1)", "check(version=1)"},
		policiesTable:       {"check(revisionbetween1and9007199254740991)", "check(protocol_modein('all','selected'))", "check(model_modein('all','selected'))"},
		protocolsTable:      {"check(protocolin('openai-chat','openai-responses','anthropic-messages','gemini-generate-content'))"},
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
	if err := verifyForeignKeys(ctx, tx, migrationStateTable, []foreignKeySpec{}); err != nil {
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
