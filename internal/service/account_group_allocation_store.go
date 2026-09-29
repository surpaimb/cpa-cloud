package service

// Independently implemented from docs/account-group-cost-allocation-contract.md.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const (
	accountGroupAllocationMigrationVersion = 1
	accountGroupAllocationMaxRevision      = int64(9_007_199_254_740_991)
)

var accountGroupAllocationTables = []string{
	"account_group_allocation_migration_state",
	"account_group_allocation_versions",
	"account_group_allocation_current",
	"account_group_allocation_legacy_attempts",
	"accounting_attempt_allocation_snapshots",
	"accounting_usage_allocation_events",
	"accounting_usage_allocation_corrections",
}

type accountGroupAllocationView struct {
	Version       string `json:"version"`
	MultiplierPPM string `json:"multiplier_ppm"`
	CreatedAt     string `json:"created_at"`
}

type accountGroupAllocationVersionView struct {
	GroupID       string `json:"group_id"`
	Version       string `json:"version"`
	Revision      int64  `json:"revision"`
	MultiplierPPM string `json:"allocation_multiplier_ppm"`
	CreatedAt     string `json:"created_at"`
}

type attemptAllocationSnapshot struct {
	GroupID    *string
	Version    *string
	Multiplier accounting.AllocationMultiplier
}

func migrateAccountGroupAllocation(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errors.New("invalid account group allocation migration")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := accountGroupAllocationObjectCount(ctx, tx)
	if err != nil {
		return err
	}
	if existing != 0 && existing != len(accountGroupAllocationTables) {
		return errors.New("partial account group allocation schema")
	}
	if existing == 0 {
		if err := createAccountGroupAllocationSchema(ctx, tx); err != nil {
			return err
		}
		if err := backfillAccountGroupAllocations(ctx, tx); err != nil {
			return err
		}
		var accountingAttemptsPresent int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='accounting_attempts'`).Scan(&accountingAttemptsPresent); err != nil {
			return err
		}
		if accountingAttemptsPresent == 1 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_allocation_legacy_attempts(attempt_id) SELECT id FROM accounting_attempts`); err != nil {
				return fmt.Errorf("mark pre-allocation accounting attempts: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_allocation_migration_state(singleton,version,completed_at) VALUES(1,?,?)`, accountGroupAllocationMigrationVersion, utcNow()); err != nil {
			return err
		}
	}
	if err := validateAccountGroupAllocationSchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func accountGroupAllocationObjectCount(ctx context.Context, tx *sql.Tx) (int, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(accountGroupAllocationTables)), ",")
	args := make([]any, len(accountGroupAllocationTables))
	for i, name := range accountGroupAllocationTables {
		args[i] = name
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN (`+placeholders+`)`, args...).Scan(&count)
	return count, err
}

func createAccountGroupAllocationSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE account_group_allocation_migration_state (
			singleton INTEGER PRIMARY KEY CHECK(singleton=1),
			version INTEGER NOT NULL CHECK(version=1),
			completed_at TEXT NOT NULL
		)`,
		`CREATE TABLE account_group_allocation_versions (
			version TEXT PRIMARY KEY,
			group_id TEXT NOT NULL REFERENCES account_groups(id) ON DELETE RESTRICT,
			group_revision INTEGER NOT NULL CHECK(group_revision BETWEEN 1 AND 9007199254740991),
			operation_id TEXT UNIQUE,
			expected_revision INTEGER,
			multiplier_ppm INTEGER NOT NULL CHECK(multiplier_ppm BETWEEN 1 AND 1000000000),
			created_at TEXT NOT NULL,
			UNIQUE(group_id,group_revision),
			UNIQUE(version,group_id),
			UNIQUE(version,group_id,multiplier_ppm),
			UNIQUE(version,group_id,group_revision),
			CHECK((operation_id IS NULL AND expected_revision IS NULL) OR
				(operation_id IS NOT NULL AND expected_revision BETWEEN 1 AND 9007199254740990 AND group_revision=expected_revision+1))
		)`,
		`CREATE TABLE account_group_allocation_current (
			group_id TEXT PRIMARY KEY REFERENCES account_groups(id) ON DELETE RESTRICT,
			version TEXT NOT NULL UNIQUE,
			group_revision INTEGER NOT NULL CHECK(group_revision BETWEEN 1 AND 9007199254740991),
			FOREIGN KEY(version,group_id,group_revision)
				REFERENCES account_group_allocation_versions(version,group_id,group_revision)
				DEFERRABLE INITIALLY DEFERRED
		)`,
		`CREATE TABLE account_group_allocation_legacy_attempts (
			attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id) ON DELETE RESTRICT
		)`,
		`CREATE TABLE accounting_attempt_allocation_snapshots (
			attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id) ON DELETE RESTRICT,
			account_group_id TEXT,
			multiplier_version TEXT,
			multiplier_ppm INTEGER NOT NULL CHECK(multiplier_ppm BETWEEN 1 AND 1000000000),
			CHECK((account_group_id IS NULL AND multiplier_version IS NULL AND multiplier_ppm=1000000) OR
				(account_group_id IS NOT NULL AND multiplier_version IS NOT NULL)),
			FOREIGN KEY(multiplier_version,account_group_id,multiplier_ppm)
				REFERENCES account_group_allocation_versions(version,group_id,multiplier_ppm) ON DELETE RESTRICT
		)`,
		`CREATE TABLE accounting_usage_allocation_events (
			event_id TEXT PRIMARY KEY REFERENCES accounting_usage_events(id) ON DELETE RESTRICT,
			attempt_id TEXT NOT NULL UNIQUE REFERENCES accounting_attempt_allocation_snapshots(attempt_id) ON DELETE RESTRICT,
			adjusted_cost_micro INTEGER CHECK(adjusted_cost_micro IS NULL OR adjusted_cost_micro>=0)
		)`,
		`CREATE TABLE accounting_usage_allocation_corrections (
			correction_id TEXT PRIMARY KEY REFERENCES accounting_usage_corrections(id) ON DELETE RESTRICT,
			attempt_id TEXT NOT NULL REFERENCES accounting_attempt_allocation_snapshots(attempt_id) ON DELETE RESTRICT,
			adjusted_cost_micro INTEGER CHECK(adjusted_cost_micro IS NULL OR adjusted_cost_micro>=0)
		)`,
		`CREATE INDEX account_group_allocation_versions_group_idx ON account_group_allocation_versions(group_id,group_revision)`,
		`CREATE INDEX accounting_attempt_allocation_group_idx ON accounting_attempt_allocation_snapshots(account_group_id,attempt_id)`,
		`CREATE INDEX accounting_usage_allocation_corrections_attempt_idx ON accounting_usage_allocation_corrections(attempt_id,correction_id)`,
		`CREATE TRIGGER account_group_allocation_versions_no_update
			BEFORE UPDATE ON account_group_allocation_versions BEGIN SELECT RAISE(ABORT,'allocation versions are immutable'); END`,
		`CREATE TRIGGER account_group_allocation_versions_no_delete
			BEFORE DELETE ON account_group_allocation_versions BEGIN SELECT RAISE(ABORT,'allocation versions are immutable'); END`,
		`CREATE TRIGGER accounting_attempt_allocation_snapshots_no_update
			BEFORE UPDATE ON accounting_attempt_allocation_snapshots BEGIN SELECT RAISE(ABORT,'allocation snapshots are immutable'); END`,
		`CREATE TRIGGER accounting_attempt_allocation_snapshots_no_delete
			BEFORE DELETE ON accounting_attempt_allocation_snapshots BEGIN SELECT RAISE(ABORT,'allocation snapshots are immutable'); END`,
		`CREATE TRIGGER accounting_attempt_allocation_snapshots_not_legacy
			BEFORE INSERT ON accounting_attempt_allocation_snapshots
			WHEN EXISTS(SELECT 1 FROM account_group_allocation_legacy_attempts WHERE attempt_id=NEW.attempt_id)
			BEGIN SELECT RAISE(ABORT,'legacy attempts cannot receive allocation snapshots'); END`,
		`CREATE TRIGGER account_group_allocation_legacy_attempts_no_update
			BEFORE UPDATE ON account_group_allocation_legacy_attempts BEGIN SELECT RAISE(ABORT,'legacy allocation markers are immutable'); END`,
		`CREATE TRIGGER account_group_allocation_legacy_attempts_no_delete
			BEFORE DELETE ON account_group_allocation_legacy_attempts BEGIN SELECT RAISE(ABORT,'legacy allocation markers are immutable'); END`,
		`CREATE TRIGGER account_group_allocation_legacy_attempts_not_snapshot
			BEFORE INSERT ON account_group_allocation_legacy_attempts
			WHEN EXISTS(SELECT 1 FROM accounting_attempt_allocation_snapshots WHERE attempt_id=NEW.attempt_id)
			BEGIN SELECT RAISE(ABORT,'snapshotted attempts cannot become legacy'); END`,
		`CREATE TRIGGER account_group_allocation_legacy_attempts_migration_only
			BEFORE INSERT ON account_group_allocation_legacy_attempts
			WHEN EXISTS(SELECT 1 FROM account_group_allocation_migration_state WHERE singleton=1)
			BEGIN SELECT RAISE(ABORT,'legacy allocation markers are migration-only'); END`,
		`CREATE TRIGGER accounting_usage_allocation_events_no_update
			BEFORE UPDATE ON accounting_usage_allocation_events BEGIN SELECT RAISE(ABORT,'allocation events are immutable'); END`,
		`CREATE TRIGGER accounting_usage_allocation_events_no_delete
			BEFORE DELETE ON accounting_usage_allocation_events BEGIN SELECT RAISE(ABORT,'allocation events are immutable'); END`,
		`CREATE TRIGGER accounting_usage_allocation_corrections_no_update
			BEFORE UPDATE ON accounting_usage_allocation_corrections BEGIN SELECT RAISE(ABORT,'allocation corrections are immutable'); END`,
		`CREATE TRIGGER accounting_usage_allocation_corrections_no_delete
			BEFORE DELETE ON accounting_usage_allocation_corrections BEGIN SELECT RAISE(ABORT,'allocation corrections are immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create account group allocation schema: %w", err)
		}
	}
	return nil
}

func backfillAccountGroupAllocations(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,revision,created_at FROM account_groups ORDER BY id`)
	if err != nil {
		return err
	}
	type group struct {
		id, created string
		revision    int64
	}
	items := make([]group, 0)
	for rows.Next() {
		var item group
		if err := rows.Scan(&item.id, &item.revision, &item.created); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		if item.revision < 1 || item.revision > accountGroupAllocationMaxRevision {
			return errors.New("invalid account group revision")
		}
		created, err := time.Parse(time.RFC3339Nano, item.created)
		if err != nil {
			return errors.New("invalid account group timestamp")
		}
		if _, err := createDefaultAccountGroupAllocationTx(ctx, tx, item.id, item.revision, created.UTC()); err != nil {
			return err
		}
	}
	return nil
}

func createDefaultAccountGroupAllocationTx(ctx context.Context, tx *sql.Tx, groupID string, revision int64, createdAt time.Time) (accountGroupAllocationView, error) {
	version, err := newID("agalloc")
	if err != nil {
		return accountGroupAllocationView{}, err
	}
	stamp := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_allocation_versions(version,group_id,group_revision,multiplier_ppm,created_at) VALUES(?,?,?,?,?)`, version, groupID, revision, accounting.AllocationMultiplierScale, stamp); err != nil {
		return accountGroupAllocationView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_group_allocation_current(group_id,version,group_revision) VALUES(?,?,?)`, groupID, version, revision); err != nil {
		return accountGroupAllocationView{}, err
	}
	return accountGroupAllocationView{Version: version, MultiplierPPM: accounting.DefaultAllocationMultiplier().CanonicalPPM(), CreatedAt: stamp}, nil
}

func validateAccountGroupAllocationSchema(ctx context.Context, tx *sql.Tx) error {
	checks := []struct {
		name      string
		columns   []string
		fragments []string
	}{
		{"account_group_allocation_migration_state", []string{"singleton", "version", "completed_at"}, []string{"check(singleton=1)", "check(version=1)"}},
		{"account_group_allocation_versions", []string{"version", "group_id", "group_revision", "operation_id", "expected_revision", "multiplier_ppm", "created_at"}, []string{"references account_groups(id) on delete restrict", "unique(group_id,group_revision)", "unique(version,group_id)", "unique(version,group_id,multiplier_ppm)", "check(multiplier_ppm between 1 and 1000000000)"}},
		{"account_group_allocation_current", []string{"group_id", "version", "group_revision"}, []string{"references account_groups(id) on delete restrict", "references account_group_allocation_versions(version,group_id,group_revision)"}},
		{"account_group_allocation_legacy_attempts", []string{"attempt_id"}, []string{"references accounting_attempts(id) on delete restrict"}},
		{"accounting_attempt_allocation_snapshots", []string{"attempt_id", "account_group_id", "multiplier_version", "multiplier_ppm"}, []string{"references accounting_attempts(id) on delete restrict", "references account_group_allocation_versions(version,group_id,multiplier_ppm) on delete restrict", "check(multiplier_ppm between 1 and 1000000000)"}},
		{"accounting_usage_allocation_events", []string{"event_id", "attempt_id", "adjusted_cost_micro"}, []string{"references accounting_usage_events(id) on delete restrict", "references accounting_attempt_allocation_snapshots(attempt_id) on delete restrict"}},
		{"accounting_usage_allocation_corrections", []string{"correction_id", "attempt_id", "adjusted_cost_micro"}, []string{"references accounting_usage_corrections(id) on delete restrict", "references accounting_attempt_allocation_snapshots(attempt_id) on delete restrict"}},
	}
	for _, check := range checks {
		if err := verifyAccountPoolTable(ctx, tx, check.name, check.columns, check.fragments); err != nil {
			return fmt.Errorf("invalid account group allocation schema: %w", err)
		}
	}
	for _, object := range []struct{ kind, name, fragment string }{
		{"index", "account_group_allocation_versions_group_idx", "on account_group_allocation_versions(group_id,group_revision)"},
		{"index", "accounting_attempt_allocation_group_idx", "on accounting_attempt_allocation_snapshots(account_group_id,attempt_id)"},
		{"index", "accounting_usage_allocation_corrections_attempt_idx", "on accounting_usage_allocation_corrections(attempt_id,correction_id)"},
		{"trigger", "account_group_allocation_versions_no_update", "before update on account_group_allocation_versions"},
		{"trigger", "account_group_allocation_versions_no_delete", "before delete on account_group_allocation_versions"},
		{"trigger", "accounting_attempt_allocation_snapshots_no_update", "before update on accounting_attempt_allocation_snapshots"},
		{"trigger", "accounting_attempt_allocation_snapshots_no_delete", "before delete on accounting_attempt_allocation_snapshots"},
		{"trigger", "accounting_attempt_allocation_snapshots_not_legacy", "before insert on accounting_attempt_allocation_snapshots"},
		{"trigger", "account_group_allocation_legacy_attempts_no_update", "before update on account_group_allocation_legacy_attempts"},
		{"trigger", "account_group_allocation_legacy_attempts_no_delete", "before delete on account_group_allocation_legacy_attempts"},
		{"trigger", "account_group_allocation_legacy_attempts_not_snapshot", "before insert on account_group_allocation_legacy_attempts"},
		{"trigger", "account_group_allocation_legacy_attempts_migration_only", "before insert on account_group_allocation_legacy_attempts"},
		{"trigger", "accounting_usage_allocation_events_no_update", "before update on accounting_usage_allocation_events"},
		{"trigger", "accounting_usage_allocation_events_no_delete", "before delete on accounting_usage_allocation_events"},
		{"trigger", "accounting_usage_allocation_corrections_no_update", "before update on accounting_usage_allocation_corrections"},
		{"trigger", "accounting_usage_allocation_corrections_no_delete", "before delete on accounting_usage_allocation_corrections"},
	} {
		var definition string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&definition); err != nil || !strings.Contains(strings.ToLower(strings.Join(strings.Fields(definition), " ")), object.fragment) {
			return errors.New("invalid account group allocation schema object")
		}
	}
	var singleton, version, markerCount int
	var completed string
	rows, err := tx.QueryContext(ctx, `SELECT singleton,version,completed_at FROM account_group_allocation_migration_state`)
	if err != nil {
		return err
	}
	for rows.Next() {
		markerCount++
		if err := rows.Scan(&singleton, &version, &completed); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if markerCount != 1 || singleton != 1 || version != accountGroupAllocationMigrationVersion {
		return errors.New("invalid account group allocation migration marker")
	}
	if _, err := time.Parse(time.RFC3339Nano, completed); err != nil {
		return errors.New("invalid account group allocation migration timestamp")
	}
	versions, err := tx.QueryContext(ctx, `SELECT version,operation_id,expected_revision,created_at FROM account_group_allocation_versions ORDER BY version`)
	if err != nil {
		return err
	}
	for versions.Next() {
		var versionID, createdAt string
		var operationID sql.NullString
		var expectedRevision sql.NullInt64
		if err := versions.Scan(&versionID, &operationID, &expectedRevision, &createdAt); err != nil {
			versions.Close()
			return err
		}
		if !validIdentifier(versionID, 128) || operationID.Valid != expectedRevision.Valid || operationID.Valid && !validGovernanceOperationID(operationID.String) {
			versions.Close()
			return errors.New("invalid account group allocation version metadata")
		}
		if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
			versions.Close()
			return errors.New("invalid account group allocation version timestamp")
		}
	}
	if err := versions.Err(); err != nil {
		versions.Close()
		return err
	}
	if err := versions.Close(); err != nil {
		return err
	}
	var mismatches int
	queries := []string{
		`SELECT COUNT(*) FROM account_groups g LEFT JOIN account_group_allocation_current c ON c.group_id=g.id WHERE c.group_id IS NULL`,
		`SELECT COUNT(*) FROM account_group_allocation_current c LEFT JOIN account_groups g ON g.id=c.group_id WHERE g.id IS NULL`,
		`SELECT COUNT(*) FROM account_group_allocation_current c LEFT JOIN account_group_allocation_versions v ON v.version=c.version AND v.group_id=c.group_id AND v.group_revision=c.group_revision WHERE v.version IS NULL`,
		`SELECT COUNT(*) FROM accounting_attempts a LEFT JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=a.id LEFT JOIN account_group_allocation_legacy_attempts l ON l.attempt_id=a.id WHERE s.attempt_id IS NULL AND l.attempt_id IS NULL`,
		`SELECT COUNT(*) FROM accounting_usage_allocation_events e LEFT JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=e.attempt_id WHERE s.attempt_id IS NULL`,
		`SELECT COUNT(*) FROM accounting_usage_allocation_corrections c LEFT JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=c.attempt_id WHERE s.attempt_id IS NULL`,
		`SELECT COUNT(*) FROM account_group_allocation_legacy_attempts l JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=l.attempt_id`,
	}
	for _, query := range queries {
		if err := tx.QueryRowContext(ctx, query).Scan(&mismatches); err != nil || mismatches != 0 {
			return errors.New("invalid account group allocation coverage")
		}
	}
	foreignKeys, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := foreignKeys.Next()
	iterationErr, closeErr := foreignKeys.Err(), foreignKeys.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if violated {
		return errors.New("account group allocation foreign key check failed")
	}
	return nil
}

func loadAccountGroupAllocationTx(ctx context.Context, tx *sql.Tx, groupID string) (accountGroupAllocationView, accounting.AllocationMultiplier, int64, error) {
	var view accountGroupAllocationView
	var ppm, revision int64
	err := tx.QueryRowContext(ctx, `SELECT v.version,v.multiplier_ppm,v.created_at,c.group_revision
		FROM account_group_allocation_current c JOIN account_group_allocation_versions v
		ON v.version=c.version AND v.group_id=c.group_id AND v.group_revision=c.group_revision
		WHERE c.group_id=?`, groupID).Scan(&view.Version, &ppm, &view.CreatedAt, &revision)
	if err != nil {
		return accountGroupAllocationView{}, accounting.AllocationMultiplier{}, 0, err
	}
	multiplier, err := accounting.NewAllocationMultiplier(ppm)
	if err != nil {
		return accountGroupAllocationView{}, accounting.AllocationMultiplier{}, 0, err
	}
	if _, err := time.Parse(time.RFC3339Nano, view.CreatedAt); err != nil {
		return accountGroupAllocationView{}, accounting.AllocationMultiplier{}, 0, errors.New("invalid allocation timestamp")
	}
	view.MultiplierPPM = multiplier.CanonicalPPM()
	return view, multiplier, revision, nil
}
