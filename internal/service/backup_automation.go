package service

// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	backupMinInterval  = int64(900)
	backupMaxInterval  = int64(2678400)
	backupMaxRetention = int64(365)
	backupMaxPlans     = int64(32)
	backupMaxHistory   = int64(1000)
	backupMaxRevision  = int64(9007199254740991)
)

const backupKeyProviderDDL = `CREATE TABLE backup_key_providers (
	id TEXT NOT NULL PRIMARY KEY,
	kind TEXT NOT NULL CHECK(kind IN ('windows-dpapi-user')),
	scope TEXT NOT NULL CHECK(scope='current-user'),
	status TEXT NOT NULL CHECK(status IN ('ready','unavailable','degraded')),
	reason_code TEXT,
	active_version INTEGER NOT NULL CHECK(active_version BETWEEN 1 AND 9007199254740991),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	created_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	updated_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK((status='ready' AND reason_code IS NULL) OR (status!='ready' AND reason_code IS NOT NULL))
)`

const backupPlanDDL = `CREATE TABLE backup_plans (
	id TEXT NOT NULL PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	key_provider_id TEXT NOT NULL REFERENCES backup_key_providers(id) ON DELETE RESTRICT,
	interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 900 AND 2678400),
	retention_count INTEGER NOT NULL CHECK(retention_count BETWEEN 1 AND 365),
	rehearsal_enabled INTEGER NOT NULL CHECK(rehearsal_enabled IN (0,1)),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	next_run_at TEXT,
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	created_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	updated_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK((enabled=0 AND next_run_at IS NULL) OR (enabled=1 AND next_run_at IS NOT NULL))
)`

const backupRunDDL = `CREATE TABLE backup_runs (
	id TEXT NOT NULL PRIMARY KEY,
	plan_id TEXT NOT NULL REFERENCES backup_plans(id) ON DELETE RESTRICT,
	plan_revision INTEGER NOT NULL CHECK(plan_revision BETWEEN 1 AND 9007199254740991),
	key_provider_id TEXT NOT NULL REFERENCES backup_key_providers(id) ON DELETE RESTRICT,
	key_provider_kind TEXT NOT NULL CHECK(key_provider_kind IN ('windows-dpapi-user')),
	key_provider_version INTEGER NOT NULL CHECK(key_provider_version BETWEEN 1 AND 9007199254740991),
	trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('scheduled','manual')),
	requested_by_admin_id TEXT REFERENCES admins(id) ON DELETE RESTRICT,
	scheduled_for TEXT NOT NULL,
	started_at TEXT NOT NULL,
	finished_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed','cancelled','interrupted')),
	package_name TEXT NOT NULL,
	package_size INTEGER CHECK(package_size IS NULL OR package_size>=0),
	package_retained INTEGER NOT NULL CHECK(package_retained IN (0,1)),
	package_deleted_at TEXT,
	verified_at TEXT,
	rehearsal_status TEXT NOT NULL CHECK(rehearsal_status IN ('pending','succeeded','failed','skipped')),
	rehearsed_at TEXT,
	error_code TEXT,
	created_at TEXT NOT NULL,
	CHECK((status='running' AND finished_at IS NULL AND package_size IS NULL AND package_retained=0 AND package_deleted_at IS NULL AND verified_at IS NULL AND rehearsal_status='pending' AND rehearsed_at IS NULL AND error_code IS NULL)
	 OR (status!='running' AND finished_at IS NOT NULL)),
	CHECK((package_retained=1 AND package_size IS NOT NULL AND package_deleted_at IS NULL) OR package_retained=0),
	CHECK((rehearsal_status='pending' AND rehearsed_at IS NULL) OR (rehearsal_status!='pending')),
	CHECK((trigger_kind='scheduled' AND requested_by_admin_id IS NULL) OR (trigger_kind='manual' AND requested_by_admin_id IS NOT NULL))
)`

const (
	backupPlanDueIndexDDL = `CREATE INDEX backup_plans_due_idx ON backup_plans(enabled,next_run_at,id)`
	backupRunPlanIndexDDL = `CREATE INDEX backup_runs_plan_idx ON backup_runs(plan_id,started_at DESC,id DESC)`
	backupSingleRunDDL    = `CREATE UNIQUE INDEX backup_runs_single_running_idx ON backup_runs(status) WHERE status='running'`
)

type backupKeyProviderView struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	Scope            string  `json:"scope"`
	Status           string  `json:"status"`
	ReasonCode       *string `json:"reason_code"`
	ActiveVersion    int64   `json:"active_version"`
	Revision         int64   `json:"revision"`
	CreatedByAdminID string  `json:"created_by_admin_id"`
	UpdatedByAdminID string  `json:"updated_by_admin_id"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

type backupPlanView struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	KeyProviderID    string         `json:"key_provider_id"`
	IntervalSeconds  int64          `json:"interval_seconds"`
	RetentionCount   int64          `json:"retention_count"`
	RehearsalEnabled bool           `json:"rehearsal_enabled"`
	Enabled          bool           `json:"enabled"`
	NextRunAt        *string        `json:"next_run_at"`
	Revision         int64          `json:"revision"`
	CreatedByAdminID string         `json:"created_by_admin_id"`
	UpdatedByAdminID string         `json:"updated_by_admin_id"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
	LatestRun        *backupRunView `json:"latest_run,omitempty"`
}

type backupRunView struct {
	ID                 string  `json:"id"`
	PlanID             string  `json:"plan_id"`
	PlanRevision       int64   `json:"plan_revision"`
	KeyProviderID      string  `json:"key_provider_id"`
	KeyProviderKind    string  `json:"key_provider_kind"`
	KeyProviderVersion int64   `json:"key_provider_version"`
	TriggerKind        string  `json:"trigger_kind"`
	RequestedByAdminID *string `json:"requested_by_admin_id"`
	ScheduledFor       string  `json:"scheduled_for"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         *string `json:"finished_at"`
	Status             string  `json:"status"`
	PackageName        string  `json:"package_name"`
	PackageSize        *int64  `json:"package_size"`
	PackageRetained    bool    `json:"package_retained"`
	PackageDeletedAt   *string `json:"package_deleted_at"`
	VerifiedAt         *string `json:"verified_at"`
	RehearsalStatus    string  `json:"rehearsal_status"`
	RehearsedAt        *string `json:"rehearsed_at"`
	ErrorCode          *string `json:"error_code"`
	CreatedAt          string  `json:"created_at"`
}

func migrateBackupAutomation(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	objects := []struct {
		name string
		kind string
		ddl  string
	}{
		{"backup_key_providers", "table", backupKeyProviderDDL},
		{"backup_plans", "table", backupPlanDDL},
		{"backup_runs", "table", backupRunDDL},
		{"backup_plans_due_idx", "index", backupPlanDueIndexDDL},
		{"backup_runs_plan_idx", "index", backupRunPlanIndexDDL},
		{"backup_runs_single_running_idx", "index", backupSingleRunDDL},
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('backup_key_providers','backup_plans','backup_runs','backup_plans_due_idx','backup_runs_plan_idx','backup_runs_single_running_idx')`).Scan(&existing); err != nil {
		return err
	}
	if existing == 0 {
		for _, object := range objects {
			if _, err := tx.ExecContext(ctx, object.ddl); err != nil {
				return fmt.Errorf("migrate backup automation: %w", err)
			}
		}
	}
	for _, object := range objects {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, object.name).Scan(&kind, &actual); err != nil {
			return errors.New("backup automation schema is incomplete or incompatible")
		}
		if kind != object.kind || normalizeBackupDDL(actual) != normalizeBackupDDL(object.ddl) {
			return errors.New("backup automation schema is incomplete or incompatible")
		}
	}
	if err := validateBackupAutomationColumns(ctx, tx); err != nil {
		return err
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := foreignRows.Next()
	iterationErr, closeErr := foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if violated {
		return errors.New("backup automation migration foreign key check failed")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE backup_runs SET status='interrupted',finished_at=?,rehearsal_status='failed',error_code='process_interrupted' WHERE status='running'`, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

func validateBackupAutomationColumns(ctx context.Context, tx *sql.Tx) error {
	wanted := map[string][]string{
		"backup_key_providers": {"id", "kind", "scope", "status", "reason_code", "active_version", "revision", "created_by_admin_id", "updated_by_admin_id", "created_at", "updated_at"},
		"backup_plans":         {"id", "name", "key_provider_id", "interval_seconds", "retention_count", "rehearsal_enabled", "enabled", "next_run_at", "revision", "created_by_admin_id", "updated_by_admin_id", "created_at", "updated_at"},
		"backup_runs":          {"id", "plan_id", "plan_revision", "key_provider_id", "key_provider_kind", "key_provider_version", "trigger_kind", "requested_by_admin_id", "scheduled_for", "started_at", "finished_at", "status", "package_name", "package_size", "package_retained", "package_deleted_at", "verified_at", "rehearsal_status", "rehearsed_at", "error_code", "created_at"},
	}
	for table, names := range wanted {
		columns, err := schemaColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if len(columns) != len(names) {
			return errors.New("backup automation table has unexpected columns")
		}
		for _, name := range names {
			if _, ok := columns[name]; !ok {
				return errors.New("backup automation table has incompatible columns")
			}
		}
	}
	return nil
}

func normalizeBackupDDL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), ""))
}

func loadBackupKeyProvider(ctx context.Context, query schemaQueryer, id string) (backupKeyProviderView, error) {
	var item backupKeyProviderView
	err := query.QueryRowContext(ctx, `SELECT id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at FROM backup_key_providers WHERE id=?`, id).
		Scan(&item.ID, &item.Kind, &item.Scope, &item.Status, &item.ReasonCode, &item.ActiveVersion, &item.Revision, &item.CreatedByAdminID, &item.UpdatedByAdminID, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func loadBackupPlan(ctx context.Context, query schemaQueryer, id string) (backupPlanView, error) {
	var item backupPlanView
	var rehearsal, enabled int
	err := query.QueryRowContext(ctx, `SELECT id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at FROM backup_plans WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &item.KeyProviderID, &item.IntervalSeconds, &item.RetentionCount, &rehearsal, &enabled, &item.NextRunAt, &item.Revision, &item.CreatedByAdminID, &item.UpdatedByAdminID, &item.CreatedAt, &item.UpdatedAt)
	item.RehearsalEnabled = rehearsal == 1
	item.Enabled = enabled == 1
	return item, err
}

func loadBackupRun(ctx context.Context, query schemaQueryer, id string) (backupRunView, error) {
	var item backupRunView
	var retained int
	err := query.QueryRowContext(ctx, `SELECT id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at FROM backup_runs WHERE id=?`, id).
		Scan(&item.ID, &item.PlanID, &item.PlanRevision, &item.KeyProviderID, &item.KeyProviderKind, &item.KeyProviderVersion, &item.TriggerKind, &item.RequestedByAdminID, &item.ScheduledFor, &item.StartedAt, &item.FinishedAt, &item.Status, &item.PackageName, &item.PackageSize, &retained, &item.PackageDeletedAt, &item.VerifiedAt, &item.RehearsalStatus, &item.RehearsedAt, &item.ErrorCode, &item.CreatedAt)
	item.PackageRetained = retained == 1
	return item, err
}
