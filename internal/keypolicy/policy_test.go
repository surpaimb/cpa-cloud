// Independently authored KEY-02 policy persistence tests.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var testTime = time.Date(2026, 9, 25, 1, 2, 3, 0, time.UTC)

func TestMigrateBackfillsExistingKeysAndIsRetryable(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-old", "employee-one")
	insertModel(t, db, "public-a", false)
	migratePolicyAndInstallSourceSchema(t, db)
	restricted, err := Replace(context.Background(), db, "key-old", 1, Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{},
		ModelMode: ModeSelected, Models: []string{"public-a"},
		SourceMode: ModeSelected, SourceCIDRs: []string{},
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	policy, err := LoadTx(context.Background(), tx, "key-old")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != restricted.Revision || policy.ProtocolMode != ModeSelected || policy.ModelMode != ModeSelected || policy.Protocols == nil || len(policy.Protocols) != 0 || !slices.Equal(policy.Models, []string{"public-a"}) {
		t.Fatalf("retry changed restricted policy=%#v", policy)
	}
}

func TestMigrateFailsClosedWhenMarkedDatabaseLosesPolicy(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	migratePolicyAndInstallSourceSchema(t, db)
	if _, err := Replace(context.Background(), db, "key-restricted", 1, Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{}, ModelMode: ModeSelected, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{},
	}, testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM access_key_policies WHERE key_id='key-restricted'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("restart migration error=%v", err)
	}
	var policies int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policies WHERE key_id='key-restricted'`).Scan(&policies); err != nil {
		t.Fatal(err)
	}
	if policies != 0 {
		t.Fatalf("restart recreated permissive policy count=%d", policies)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := LoadTx(context.Background(), tx, "key-restricted"); !errors.Is(err, ErrPolicyMissing) {
		t.Fatalf("missing policy did not fail closed: %v", err)
	}
}

func TestMigrateRejectsInvalidPersistentMarkerWithoutChangingPolicy(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-restricted", "employee-one")
	migratePolicyAndInstallSourceSchema(t, db)
	if _, err := Replace(context.Background(), db, "key-restricted", 1, Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{}, ModelMode: ModeSelected, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{},
	}, testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM access_key_policy_migration_state`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("empty marker migration error=%v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	policy, err := LoadTx(context.Background(), tx, "key-restricted")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 2 || policy.ProtocolMode != ModeSelected || policy.ModelMode != ModeSelected || len(policy.Protocols) != 0 || len(policy.Models) != 0 {
		t.Fatalf("invalid marker changed restricted policy=%#v", policy)
	}
}

func TestMigrateRejectsMissingMarkerAndRollsBackInterruptedFirstUpgrade(t *testing.T) {
	t.Run("missing marker", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.Exec(`CREATE TABLE access_key_policies(key_id TEXT PRIMARY KEY, revision INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("unmarked schema error=%v", err)
		}
		var marker int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, migrationStateTable).Scan(&marker); err != nil {
			t.Fatal(err)
		}
		if marker != 0 {
			t.Fatal("unmarked partial schema gained a migration marker")
		}
	})

	t.Run("interrupted first upgrade", func(t *testing.T) {
		db := openTestDB(t)
		insertKey(t, db, "key-old", "employee-one")
		interrupted := errors.New("synthetic migration interruption")
		if err := migrate(context.Background(), db, func(*sql.Tx) error { return interrupted }); !errors.Is(err, interrupted) {
			t.Fatalf("interrupted migration error=%v", err)
		}
		var leaked int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN (?,?,?,?)`, migrationStateTable, policiesTable, protocolsTable, modelsTable).Scan(&leaked); err != nil {
			t.Fatal(err)
		}
		if leaked != 0 {
			t.Fatalf("interrupted migration leaked objects=%d", leaked)
		}
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("retry after rollback: %v", err)
		}
	})
}

func TestMigrateRejectsMalformedExistingSchemaWithoutPartialBackfill(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-old", "employee-one")
	if _, err := db.Exec(`CREATE TABLE access_key_policies(key_id TEXT PRIMARY KEY, revision INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("migration error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policies`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("malformed schema was partially backfilled: %d", count)
	}
	for _, table := range []string{protocolsTable, modelsTable} {
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("migration did not roll back %s", table)
		}
	}
}

func TestCreateAndReplaceTxEnforceExactPolicyAndCAS(t *testing.T) {
	db := openTestDB(t)
	migratePolicyAndInstallSourceSchema(t, db)
	insertModel(t, db, "public-a", false)
	insertKey(t, db, "key-new", "employee-one")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateTx(context.Background(), tx, "key-new", Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{ProtocolOpenAIResponses},
		ModelMode: ModeSelected, Models: []string{"public-a"},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || !Allows(created, ProtocolOpenAIResponses, "public-a") || Allows(created, ProtocolOpenAIChat, "public-a") || Allows(created, ProtocolOpenAIResponses, "public-b") {
		t.Fatalf("created policy=%#v", created)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := ReplaceTx(context.Background(), tx, "key-new", 1, Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{},
		ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || Allows(updated, ProtocolOpenAIResponses, "public-a") {
		t.Fatalf("deny-all update=%#v", updated)
	}
	_, err = Replace(context.Background(), db, "key-new", 1, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime.Add(2*time.Minute))
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale replacement error=%v", err)
	}
	reset, err := Replace(context.Background(), db, "key-new", 2, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reset.Revision != 3 || !Allows(reset, ProtocolAnthropicMessages, "later-public-model") {
		t.Fatalf("reset policy=%#v", reset)
	}
	if _, err := Replace(context.Background(), db, "key-new", 1, Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime.Add(4*time.Minute)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("ABA stale revision error=%v", err)
	}
}

func TestStrictPolicyValidationRejectsDuplicatesUnknownAndArchivedModels(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-one", "employee-one")
	insertModel(t, db, "active-model", false)
	insertModel(t, db, "archived-model", true)
	migratePolicyAndInstallSourceSchema(t, db)
	tests := []Replacement{
		{ProtocolMode: ModeAll, Protocols: nil, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeAll, Protocols: []ClientProtocol{ProtocolOpenAIChat}, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeSelected, Protocols: []ClientProtocol{"unknown"}, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeSelected, Protocols: []ClientProtocol{ProtocolOpenAIChat, ProtocolOpenAIChat}, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeSelected, Models: []string{"active-model", "active-model"}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeSelected, Models: []string{"missing-model"}, SourceMode: ModeAll, SourceCIDRs: []string{}},
		{ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeSelected, Models: []string{"archived-model"}, SourceMode: ModeAll, SourceCIDRs: []string{}},
	}
	for index, replacement := range tests {
		if _, err := Replace(context.Background(), db, "key-one", 1, replacement, testTime.Add(time.Duration(index)*time.Minute)); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("case %d error=%v", index, err)
		}
	}
}

func TestNormalizeReturnsDeterministicDetachedReplacement(t *testing.T) {
	input := Replacement{
		ProtocolMode: ModeSelected, Protocols: []ClientProtocol{ProtocolGeminiGenerate, ProtocolOpenAIChat},
		ModelMode: ModeSelected, Models: []string{"z-model", "a-model"},
		SourceMode: ModeSelected, SourceCIDRs: []string{"2001:db8::1", "192.0.2.1"},
	}
	normalized, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Protocols[0] = ProtocolAnthropicMessages
	input.Models[0] = "changed"
	input.SourceCIDRs[0] = "2001:db9::1"
	if got, want := normalized.Protocols, []ClientProtocol{ProtocolOpenAIChat, ProtocolGeminiGenerate}; !slices.Equal(got, want) {
		t.Fatalf("protocols=%v want=%v", got, want)
	}
	if got, want := normalized.Models, []string{"a-model", "z-model"}; !slices.Equal(got, want) {
		t.Fatalf("models=%v want=%v", got, want)
	}
	if got, want := normalized.SourceCIDRs, []string{"192.0.2.1/32", "2001:db8::1/128"}; !slices.Equal(got, want) {
		t.Fatalf("source CIDRs=%v want=%v", got, want)
	}
	if _, err := Normalize(Replacement{ProtocolMode: ModeAll, Protocols: nil, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeAll, SourceCIDRs: []string{}}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("missing protocol array error=%v", err)
	}
}

func TestMissingPolicyFailsClosedAndDefaultRequiresExistingKey(t *testing.T) {
	db := openTestDB(t)
	migratePolicyAndInstallSourceSchema(t, db)
	insertKey(t, db, "key-missing-policy", "employee-one")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTx(context.Background(), tx, "key-missing-policy"); !errors.Is(err, ErrPolicyMissing) {
		t.Fatalf("missing policy error=%v", err)
	}
	if err := CreateDefaultTx(context.Background(), tx, "does-not-exist", testTime); err == nil {
		t.Fatal("default policy unexpectedly accepted a missing key")
	}
	_ = tx.Rollback()
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "key-policy.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE employees(id TEXT PRIMARY KEY)`,
		`CREATE TABLE access_keys(id TEXT PRIMARY KEY,employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE CASCADE)`,
		`CREATE TABLE models(id TEXT PRIMARY KEY,archived INTEGER NOT NULL CHECK(archived IN (0,1)))`,
		`INSERT INTO employees(id) VALUES('employee-one'),('employee-two')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertKey(t *testing.T, db *sql.DB, keyID, employeeID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO access_keys(id,employee_id) VALUES(?,?)`, keyID, employeeID); err != nil {
		t.Fatal(err)
	}
}

func insertModel(t *testing.T, db *sql.DB, id string, archived bool) {
	t.Helper()
	value := 0
	if archived {
		value = 1
	}
	if _, err := db.Exec(`INSERT INTO models(id,archived) VALUES(?,?)`, id, value); err != nil {
		t.Fatal(err)
	}
}
