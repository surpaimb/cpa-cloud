package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestSourceMigrationBackfillsExistingPoliciesWithoutChangingRevision(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var mode string
	if err := db.QueryRow(`SELECT source_mode FROM access_key_policy_sources WHERE key_id='key-existing'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "all" {
		t.Fatalf("source mode=%q", mode)
	}
	var revision, marker, cidrs int
	if err := db.QueryRow(`SELECT revision FROM access_key_policies WHERE key_id='key-existing'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_source_migration_state WHERE singleton=1 AND version=1`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_source_cidrs WHERE key_id='key-existing'`).Scan(&cidrs); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || marker != 1 || cidrs != 0 {
		t.Fatalf("revision=%d marker=%d cidrs=%d", revision, marker, cidrs)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
}

func TestSourceMigrationFailsClosedWhenMarkedDatabaseLosesCoverage(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM access_key_policy_sources WHERE key_id='key-restricted'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("restart migration error=%v", err)
	}
	var sources int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_sources WHERE key_id='key-restricted'`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != 0 {
		t.Fatalf("restart recreated permissive source policy count=%d", sources)
	}
}

func TestSourceMigrationRejectsMissingMarkerWithoutRegrant(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE access_key_policy_sources SET source_mode='selected' WHERE key_id='key-restricted'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM access_key_policy_source_migration_state`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("missing source marker error=%v", err)
	}
	var mode string
	if err := db.QueryRow(`SELECT source_mode FROM access_key_policy_sources WHERE key_id='key-restricted'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "selected" {
		t.Fatalf("missing marker changed source mode=%q", mode)
	}
}

func TestSourceMigrationRejectsPartialUnmarkedSchemaAndRollsBack(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE access_key_policy_source_cidrs`,
		`DROP TABLE access_key_policy_sources`,
		`DROP TABLE access_key_policy_source_migration_state`,
		`CREATE TABLE access_key_policy_sources(key_id TEXT PRIMARY KEY, source_mode TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("partial source migration error=%v", err)
	}
	var marker int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, sourceMigrationStateTable).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != 0 {
		t.Fatal("partial source schema gained a durable marker")
	}
}

func TestSourceMigrationRejectsOrphanedCIDRWithoutChangingGrant(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE access_key_policy_sources SET source_mode='selected' WHERE key_id='key-restricted'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_source_cidrs(key_id,cidr) VALUES('key-restricted','192.0.2.0/24')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_source_cidrs(key_id,cidr) VALUES('missing-key','198.51.100.0/24')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("orphaned source CIDR error=%v", err)
	}
	var mode string
	var retained, orphaned int
	if err := db.QueryRow(`SELECT source_mode FROM access_key_policy_sources WHERE key_id='key-restricted'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_source_cidrs WHERE key_id='key-restricted'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_source_cidrs WHERE key_id='missing-key'`).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if mode != "selected" || retained != 1 || orphaned != 1 {
		t.Fatalf("restart changed source grant: mode=%q retained=%d orphaned=%d", mode, retained, orphaned)
	}
}

func TestSourceMigrationUpgradesV1WithoutChangingRestrictedPolicy(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	createV1PolicySchema(t, db)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at)
		VALUES('key-restricted',7,'selected','selected',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_migration_state(singleton,version,completed_at) VALUES(1,1,?)`, stamp); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var revision int
	var protocolMode, modelMode, sourceMode string
	if err := db.QueryRow(`SELECT p.revision,p.protocol_mode,p.model_mode,s.source_mode
		FROM access_key_policies p JOIN access_key_policy_sources s ON s.key_id=p.key_id
		WHERE p.key_id='key-restricted'`).Scan(&revision, &protocolMode, &modelMode, &sourceMode); err != nil {
		t.Fatal(err)
	}
	var protocols, models, cidrs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_protocols WHERE key_id='key-restricted'`).Scan(&protocols); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_models WHERE key_id='key-restricted'`).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_source_cidrs WHERE key_id='key-restricted'`).Scan(&cidrs); err != nil {
		t.Fatal(err)
	}
	if revision != 7 || protocolMode != "selected" || modelMode != "selected" || sourceMode != "all" || protocols != 0 || models != 0 || cidrs != 0 {
		t.Fatalf("upgrade changed policy: revision=%d protocol=%q model=%q source=%q protocols=%d models=%d cidrs=%d", revision, protocolMode, modelMode, sourceMode, protocols, models, cidrs)
	}
}

func TestSourceMigrationInterruptionRollsBackAndRetriesCleanly(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	createV1PolicySchema(t, db)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at)
		VALUES('key-existing',4,'selected','selected',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_migration_state(singleton,version,completed_at) VALUES(1,1,?)`, stamp); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic source migration interruption")
	if err := migrateWithHooks(context.Background(), db, nil, func(*sql.Tx) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted source migration error=%v", err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN (?,?,?,?)`, sourceMigrationStateTable, sourcesTable, sourceCIDRsTable, sourceCIDRIndex).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("interrupted source migration leaked objects=%d", leaked)
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM access_key_policies WHERE key_id='key-existing'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 4 {
		t.Fatalf("interrupted source migration changed revision=%d", revision)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after source rollback: %v", err)
	}
	var sourceMode string
	if err := db.QueryRow(`SELECT source_mode FROM access_key_policy_sources WHERE key_id='key-existing'`).Scan(&sourceMode); err != nil {
		t.Fatal(err)
	}
	if sourceMode != "all" {
		t.Fatalf("retry source mode=%q", sourceMode)
	}
}

func createV1PolicySchema(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE access_key_policy_migration_state (singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton=1), version INTEGER NOT NULL CHECK(version=1), completed_at TEXT NOT NULL)`,
		`CREATE TABLE access_key_policies (key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_keys(id) ON DELETE CASCADE, revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991), protocol_mode TEXT NOT NULL CHECK(protocol_mode IN ('all','selected')), model_mode TEXT NOT NULL CHECK(model_mode IN ('all','selected')), created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE access_key_policy_protocols (key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE, protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat','openai-responses','anthropic-messages','gemini-generate-content')), PRIMARY KEY(key_id,protocol))`,
		`CREATE TABLE access_key_policy_models (key_id TEXT NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE, model_id TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT, PRIMARY KEY(key_id,model_id))`,
		`CREATE INDEX access_key_policies_revision_idx ON access_key_policies(key_id,revision)`,
		`CREATE INDEX access_key_policy_protocols_protocol_idx ON access_key_policy_protocols(protocol,key_id)`,
		`CREATE INDEX access_key_policy_models_model_idx ON access_key_policy_models(model_id,key_id)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
