package service

// Independently authored migration tests for docs/adr/0002-automated-backup-key-custody.md.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackupAutomationMigrationRetryRestartAndNoReplay(t *testing.T) {
	db := openBackupMigrationDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := migrateBackupAutomation(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupAutomation(ctx, db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	stamp := "2026-09-24T01:00:00Z"
	next := "2026-09-24T02:00:00Z"
	if _, err := db.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES('bkp_test','windows-dpapi-user','current-user','ready',NULL,1,1,'adm_test','adm_test',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_plans(id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES('bpl_test','Nightly','bkp_test',3600,7,1,1,?,1,'adm_test','adm_test',?,?)`, next, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES('brn_test','bpl_test',1,'bkp_test','windows-dpapi-user',1,'scheduled',NULL,?,?,NULL,'running','cpa-cloud-bpl_test-20260924T010000Z-brn_test.cpacb',NULL,0,NULL,NULL,'pending',NULL,NULL,?)`, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupAutomation(ctx, db); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	var status, errorCode, rehearsalStatus, nextAfter string
	if err := db.QueryRow(`SELECT status,error_code,rehearsal_status FROM backup_runs WHERE id='brn_test'`).Scan(&status, &errorCode, &rehearsalStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT next_run_at FROM backup_plans WHERE id='bpl_test'`).Scan(&nextAfter); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || errorCode != "process_interrupted" || rehearsalStatus != "failed" || nextAfter != next {
		t.Fatalf("status=%q error=%q rehearsal=%q next=%q", status, errorCode, rehearsalStatus, nextAfter)
	}
}

func TestBackupAutomationMigrationRejectsLookalikeAndRollsBack(t *testing.T) {
	db := openBackupMigrationDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE backup_plans(id TEXT PRIMARY KEY,name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupAutomation(context.Background(), db); err == nil {
		t.Fatal("migration accepted incompatible backup table")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('backup_key_providers','backup_runs','backup_plans_due_idx','backup_runs_plan_idx','backup_runs_single_running_idx')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration leaked objects count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DROP TABLE backup_plans`); err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupAutomation(context.Background(), db); err != nil {
		t.Fatalf("migration after repair: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('backup_key_providers','backup_plans','backup_runs','backup_plans_due_idx','backup_runs_plan_idx','backup_runs_single_running_idx')`).Scan(&count); err != nil || count != 6 {
		t.Fatalf("repaired objects count=%d err=%v", count, err)
	}
}

func TestBackupAutomationSchemaEnforcesSafeVersionsAndSingleRun(t *testing.T) {
	db := openBackupMigrationDB(t)
	defer db.Close()
	if err := migrateBackupAutomation(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	stamp := "2026-09-24T01:00:00Z"
	if _, err := db.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES('bkp_bad','windows-dpapi-user','current-user','ready',NULL,9007199254740992,1,'adm_test','adm_test',?,?)`, stamp, stamp); err == nil {
		t.Fatal("provider version above JSON safe integer was accepted")
	}
	if _, err := db.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES('bkp_ok','windows-dpapi-user','current-user','ready',NULL,1,1,'adm_test','adm_test',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO backup_plans(id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES('bpl_ok','Plan','bkp_ok',900,1,0,1,?,1,'adm_test','adm_test',?,?)`, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	insertRun := `INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES(?,?,?,?,?,?,'scheduled',NULL,?, ?,NULL,'running',?,NULL,0,NULL,NULL,'pending',NULL,NULL,?)`
	if _, err := db.Exec(insertRun, "brn_one", "bpl_ok", 1, "bkp_ok", "windows-dpapi-user", 1, stamp, stamp, "one.cpacb", stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertRun, "brn_two", "bpl_ok", 1, "bkp_ok", "windows-dpapi-user", 1, stamp, stamp, "two.cpacb", stamp); err == nil {
		t.Fatal("second global running backup was accepted")
	}
}

func openBackupMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "backup-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE admins(id TEXT PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO admins(id) VALUES('adm_test')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
