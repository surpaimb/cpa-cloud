// Independently authored KEY-02 account-pool group migration tests.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAccountGroupMigrationBackfillsOldPoliciesWithoutChangingRevision(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	installPreAccountGroupPolicySchema(t, db, "key-existing", 7)

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var revision, marker, members int
	var mode Mode
	if err := db.QueryRow(`SELECT p.revision,g.account_group_mode FROM access_key_policies p JOIN access_key_policy_account_groups g ON g.key_id=p.key_id WHERE p.key_id='key-existing'`).Scan(&revision, &mode); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_account_group_migration_state WHERE singleton=1 AND version=1`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_account_group_members WHERE key_id='key-existing'`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if revision != 7 || mode != ModeAll || marker != 1 || members != 0 {
		t.Fatalf("revision=%d mode=%q marker=%d members=%d", revision, mode, marker, members)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	assertStoredAccountGroups(t, db, "key-existing", 7, ModeAll, []string{})
}

func TestAccountGroupMigrationRejectsMissingOrDamagedMarkerWithoutRegrant(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage string
	}{
		{"missing row", `DELETE FROM access_key_policy_account_group_migration_state`},
		{"invalid timestamp", `UPDATE access_key_policy_account_group_migration_state SET completed_at='invalid'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := migratedRestrictedAccountGroupDB(t)
			if _, err := db.Exec(test.damage); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
				t.Fatalf("damaged marker migration error=%v", err)
			}
			assertStoredAccountGroups(t, db, "key-restricted", 1, ModeSelected, []string{"grp-a"})
		})
	}
}

func TestAccountGroupMigrationRejectsPartialUnmarkedSchema(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	installPreAccountGroupPolicySchema(t, db, "key-existing", 4)
	if _, err := db.Exec(`CREATE TABLE access_key_policy_account_groups(key_id TEXT PRIMARY KEY,account_group_mode TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("partial migration error=%v", err)
	}
	var marker int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, accountGroupMigrationStateTable).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != 0 {
		t.Fatal("partial account group schema gained a durable marker")
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM access_key_policies WHERE key_id='key-existing'`).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("partial migration changed revision=%d error=%v", revision, err)
	}
}

func TestAccountGroupMigrationFailsClosedOnMissingCoverageAndBrokenIndex(t *testing.T) {
	t.Run("missing coverage", func(t *testing.T) {
		db := migratedRestrictedAccountGroupDB(t)
		if _, err := db.Exec(`DELETE FROM access_key_policy_account_groups WHERE key_id='key-restricted'`); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("missing coverage migration error=%v", err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_account_groups WHERE key_id='key-restricted'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration recreated permissive coverage count=%d error=%v", count, err)
		}
	})

	t.Run("broken index", func(t *testing.T) {
		db := migratedRestrictedAccountGroupDB(t)
		if _, err := db.Exec(`DROP INDEX access_key_policy_account_group_members_group_idx`); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("broken index migration error=%v", err)
		}
		assertStoredAccountGroups(t, db, "key-restricted", 1, ModeSelected, []string{"grp-a"})
		if _, err := db.Exec(`CREATE INDEX access_key_policy_account_group_members_group_idx ON access_key_policy_account_group_members(account_group_id,key_id)`); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("migration after index repair: %v", err)
		}
	})
}

func TestAccountGroupMigrationRejectsOrphanedMembersAndUnknownGroups(t *testing.T) {
	t.Run("missing key policy group row", func(t *testing.T) {
		db := migratedRestrictedAccountGroupDB(t)
		setForeignKeys(t, db, false)
		if _, err := db.Exec(`DELETE FROM access_key_policy_account_groups WHERE key_id='key-restricted'`); err != nil {
			t.Fatal(err)
		}
		setForeignKeys(t, db, true)
		if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("orphan member migration error=%v", err)
		}
		var members int
		if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_account_group_members WHERE key_id='key-restricted'`).Scan(&members); err != nil || members != 1 {
			t.Fatalf("orphan member changed count=%d error=%v", members, err)
		}
	})

	t.Run("unknown account group", func(t *testing.T) {
		db := migratedRestrictedAccountGroupDB(t)
		setForeignKeys(t, db, false)
		if _, err := db.Exec(`DELETE FROM account_groups WHERE id='grp-a'`); err != nil {
			t.Fatal(err)
		}
		setForeignKeys(t, db, true)
		if err := Migrate(context.Background(), db); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("unknown group migration error=%v", err)
		}
		var members int
		if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policy_account_group_members WHERE key_id='key-restricted' AND account_group_id='grp-a'`).Scan(&members); err != nil || members != 1 {
			t.Fatalf("unknown group migration changed member count=%d error=%v", members, err)
		}
	})
}

func TestAccountGroupMigrationInterruptionRollsBackAndRetries(t *testing.T) {
	db := openTestDB(t)
	insertKey(t, db, "key-existing", "employee-one")
	installPreAccountGroupPolicySchema(t, db, "key-existing", 9)
	interrupted := errors.New("synthetic account group migration interruption")
	if err := migrateWithAccountGroupHook(context.Background(), db, func(*sql.Tx) error { return interrupted }); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted migration error=%v", err)
	}
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN (?,?,?,?)`, accountGroupMigrationStateTable, accountGroupPoliciesTable, accountGroupMembersTable, accountGroupMemberIndex).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("interrupted migration leaked objects=%d", leaked)
	}
	var revision int
	if err := db.QueryRow(`SELECT revision FROM access_key_policies WHERE key_id='key-existing'`).Scan(&revision); err != nil || revision != 9 {
		t.Fatalf("interrupted migration changed revision=%d error=%v", revision, err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	assertStoredAccountGroups(t, db, "key-existing", 9, ModeAll, []string{})
}

func installPreAccountGroupPolicySchema(t *testing.T, db *sql.DB, keyID string, revision int64) {
	t.Helper()
	createV1PolicySchema(t, db)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO access_key_policies(key_id,revision,protocol_mode,model_mode,created_at,updated_at) VALUES(?,?,'selected','selected',?,?)`, keyID, revision, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_migration_state(singleton,version,completed_at) VALUES(1,1,?)`, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSourcePolicy(context.Background(), tx, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func migratedRestrictedAccountGroupDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.Exec(`INSERT INTO account_groups(id) VALUES('grp-a')`); err != nil {
		t.Fatal(err)
	}
	insertKey(t, db, "key-restricted", "employee-one")
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE access_key_policy_account_groups SET account_group_mode='selected' WHERE key_id='key-restricted'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_key_policy_account_group_members(key_id,account_group_id) VALUES('key-restricted','grp-a')`); err != nil {
		t.Fatal(err)
	}
	return db
}

func setForeignKeys(t *testing.T, db *sql.DB, enabled bool) {
	t.Helper()
	value := "OFF"
	if enabled {
		value = "ON"
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=` + value); err != nil {
		t.Fatal(err)
	}
}
