//go:build windows

// Independently authored Windows acceptance for docs/adr/0004-portable-backup-recovery-material.md.
package recoverymaterial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
	"cpacloud.local/server/internal/service"
	_ "modernc.org/sqlite"
)

const recoveryTestPassword = "portable recovery test password"

type recoveryFixture struct {
	root, dataDir, sourceStore, packagePath, materialPath, providerID string
	versions                                                          map[uint64][32]byte
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	fixture := &recoveryFixture{root: root, dataDir: filepath.Join(root, "source-data"), sourceStore: filepath.Join(root, "source-store"), packagePath: filepath.Join(root, "historical.cpacb"), materialPath: filepath.Join(root, "recovery.cpakr"), providerID: "bkp_portable", versions: make(map[uint64][32]byte)}
	if err := service.Initialize(ctx, fixture.dataDir, strings.NewReader("administrator-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := service.Open(ctx, service.Config{DataDir: fixture.dataDir, Listen: "127.0.0.1:0", Version: "recovery-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := keyprovider.Open(fixture.sourceStore)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint64{1, 2} {
		prepared, err := store.PrepareVersion(ctx, fixture.providerID, version)
		if err != nil {
			t.Fatal(err)
		}
		prepared.Commit()
		material, err := store.Resolve(ctx, fixture.providerID, version)
		if err != nil {
			t.Fatal(err)
		}
		fixture.versions[version] = material.Key
		material.Destroy()
	}
	database := openRecoveryTestDB(t, filepath.Join(fixture.dataDir, "cpa-cloud.db"))
	defer database.Close()
	var adminID string
	if err := database.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,'current-user','ready',NULL,2,1,?,?,?,?)`, fixture.providerID, keyprovider.KindWindowsDPAPIUser, adminID, adminID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	key := backup.KeyMaterial{ProviderID: fixture.providerID, ProviderKind: keyprovider.KindWindowsDPAPIUser, ProviderVersion: 1, WrappingKey: fixture.versions[1]}
	if _, err := backup.CreateWithKeyMaterial(ctx, fixture.dataDir, fixture.packagePath, key, "recovery-test"); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestWindowsDistinctStoreExportImportRestoreAndPostImportRotation(t *testing.T) {
	fixture := newRecoveryFixture(t)
	sourceBefore := snapshotFiles(t, fixture.sourceStore)
	reference, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, fixture.materialPath, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword))
	if err != nil {
		t.Fatal(err)
	}
	if reference.ProviderID != fixture.providerID || len(reference.Versions) != 2 {
		t.Fatalf("reference=%+v", reference)
	}
	if after := snapshotFiles(t, fixture.sourceStore); !equalDigests(sourceBefore, after) {
		t.Fatal("export modified source provider store")
	}
	target := filepath.Join(fixture.root, "fresh-target")
	if _, err := Import(context.Background(), fixture.materialPath, fixture.packagePath, target, []byte(recoveryTestPassword)); err != nil {
		t.Fatal(err)
	}
	targetStore, err := keyprovider.Open(filepath.Join(target, "provider-store"))
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range fixture.versions {
		material, err := targetStore.Resolve(context.Background(), fixture.providerID, version)
		if err != nil {
			t.Fatalf("resolve imported version %d: %v", version, err)
		}
		if material.Key != want {
			t.Fatalf("imported version %d changed", version)
		}
		material.Destroy()
	}
	oldKey := backup.KeyMaterial{ProviderID: fixture.providerID, ProviderKind: keyprovider.KindWindowsDPAPIUser, ProviderVersion: 1, WrappingKey: fixture.versions[1]}
	if _, err := backup.VerifyWithKeyMaterial(context.Background(), fixture.packagePath, oldKey); err != nil {
		t.Fatalf("historical package not decryptable after import: %v", err)
	}
	prepared, err := targetStore.PrepareVersion(context.Background(), fixture.providerID, 3)
	if err != nil {
		t.Fatal(err)
	}
	database := openRecoveryTestDB(t, filepath.Join(target, "data", "cpa-cloud.db"))
	result, err := database.Exec(`UPDATE backup_key_providers SET status='ready',reason_code=NULL,active_version=3,revision=revision+1 WHERE id=? AND active_version=2 AND revision=1`, fixture.providerID)
	if err != nil {
		prepared.Rollback()
		database.Close()
		t.Fatal(err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		prepared.Rollback()
		database.Close()
		t.Fatal("post-import rotation CAS did not commit")
	}
	prepared.Commit()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.VerifyWithKeyMaterial(context.Background(), fixture.packagePath, oldKey); err != nil {
		t.Fatalf("historical package lost after rotation: %v", err)
	}
	active, err := targetStore.Resolve(context.Background(), fixture.providerID, 3)
	if err != nil {
		t.Fatal(err)
	}
	newPackage := filepath.Join(fixture.root, "post-rotation.cpacb")
	newKey := backup.KeyMaterial{ProviderID: active.ProviderID, ProviderKind: active.Kind, ProviderVersion: active.Version, WrappingKey: active.Key}
	active.Destroy()
	if _, err := backup.CreateWithKeyMaterial(context.Background(), filepath.Join(target, "data"), newPackage, newKey, "post-import-rotation"); err != nil {
		t.Fatal(err)
	}
	active, err = targetStore.Resolve(context.Background(), fixture.providerID, 3)
	if err != nil {
		t.Fatal(err)
	}
	verifyKey := backup.KeyMaterial{ProviderID: active.ProviderID, ProviderKind: active.Kind, ProviderVersion: active.Version, WrappingKey: active.Key}
	active.Destroy()
	if _, err := backup.VerifyWithKeyMaterial(context.Background(), newPackage, verifyKey); err != nil {
		t.Fatalf("new active-version package failed: %v", err)
	}
}

func TestWindowsImportFailuresAreAtomicAndRetryable(t *testing.T) {
	fixture := newRecoveryFixture(t)
	if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, fixture.materialPath, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword)); err != nil {
		t.Fatal(err)
	}
	t.Run("wrong-password", func(t *testing.T) {
		target := filepath.Join(fixture.root, "wrong-password")
		if _, err := Import(context.Background(), fixture.materialPath, fixture.packagePath, target, []byte("wrong")); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error=%v", err)
		}
		assertAbsent(t, target)
	})
	t.Run("missing-package-version", func(t *testing.T) {
		material := filepath.Join(fixture.root, "missing-version.cpakr")
		if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, material, fixture.providerID, []uint64{2}, []byte(recoveryTestPassword)); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.root, "missing-version-target")
		if _, err := Import(context.Background(), material, fixture.packagePath, target, []byte(recoveryTestPassword)); err == nil {
			t.Fatal("missing backup version was accepted")
		}
		assertAbsent(t, target)
	})
	t.Run("missing-restored-active-version", func(t *testing.T) {
		material := filepath.Join(fixture.root, "missing-active-version.cpakr")
		if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, material, fixture.providerID, []uint64{1}, []byte(recoveryTestPassword)); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.root, "missing-active-target")
		if _, err := Import(context.Background(), material, fixture.packagePath, target, []byte(recoveryTestPassword)); err == nil || !strings.Contains(err.Error(), "active key version") {
			t.Fatalf("error=%v", err)
		}
		assertAbsent(t, target)
	})
	t.Run("backup-provider-mismatch", func(t *testing.T) {
		mismatchedPackage := filepath.Join(fixture.root, "provider-mismatch.cpacb")
		key := backup.KeyMaterial{ProviderID: "another_provider", ProviderKind: keyprovider.KindWindowsDPAPIUser, ProviderVersion: 1, WrappingKey: fixture.versions[1]}
		if _, err := backup.CreateWithKeyMaterial(context.Background(), fixture.dataDir, mismatchedPackage, key, "provider-mismatch"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.root, "provider-mismatch-target")
		if _, err := Import(context.Background(), fixture.materialPath, mismatchedPackage, target, []byte(recoveryTestPassword)); err == nil {
			t.Fatal("mismatched backup provider was accepted")
		}
		assertAbsent(t, target)
	})
	t.Run("restored-database-provider-mismatch", func(t *testing.T) {
		alteredData := filepath.Join(fixture.root, "altered-provider-data")
		copyRecoveryData(t, fixture.dataDir, alteredData)
		database := openRecoveryTestDB(t, filepath.Join(alteredData, "cpa-cloud.db"))
		if _, err := database.Exec(`UPDATE backup_key_providers SET id='different_db_provider' WHERE id=?`, fixture.providerID); err != nil {
			database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		mismatchedPackage := filepath.Join(fixture.root, "database-provider-mismatch.cpacb")
		key := backup.KeyMaterial{ProviderID: fixture.providerID, ProviderKind: keyprovider.KindWindowsDPAPIUser, ProviderVersion: 1, WrappingKey: fixture.versions[1]}
		if _, err := backup.CreateWithKeyMaterial(context.Background(), alteredData, mismatchedPackage, key, "database-provider-mismatch"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.root, "database-provider-mismatch-target")
		if _, err := Import(context.Background(), fixture.materialPath, mismatchedPackage, target, []byte(recoveryTestPassword)); err == nil || !strings.Contains(err.Error(), "does not contain the recovery key provider") {
			t.Fatalf("error=%v", err)
		}
		assertAbsent(t, target)
	})
	t.Run("instance-mismatch", func(t *testing.T) {
		otherData := filepath.Join(fixture.root, "other-data")
		if err := service.Initialize(context.Background(), otherData, strings.NewReader("administrator-password\n")); err != nil {
			t.Fatal(err)
		}
		material := filepath.Join(fixture.root, "other-instance.cpakr")
		if _, err := Export(context.Background(), fixture.sourceStore, otherData, material, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword)); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(fixture.root, "instance-mismatch-target")
		if _, err := Import(context.Background(), material, fixture.packagePath, target, []byte(recoveryTestPassword)); err == nil || !strings.Contains(err.Error(), "different CPA Cloud instance") {
			t.Fatalf("error=%v", err)
		}
		assertAbsent(t, target)
	})
	t.Run("failure-after-staging", func(t *testing.T) {
		target := filepath.Join(fixture.root, "injected-target")
		_, err := importWithHooks(context.Background(), fixture.materialPath, fixture.packagePath, target, []byte(recoveryTestPassword), importHooks{beforePublish: func(string) error { return errors.New("injected interruption") }, syncParent: syncRecoveryDirectory})
		if err == nil {
			t.Fatal("injected failure was ignored")
		}
		assertAbsent(t, target)
		matches, err := filepath.Glob(filepath.Join(fixture.root, ".cpa-cloud-key-import-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("staging leftovers=%v err=%v", matches, err)
		}
		if _, err := Import(context.Background(), fixture.materialPath, fixture.packagePath, target, []byte(recoveryTestPassword)); err != nil {
			t.Fatalf("safe retry failed: %v", err)
		}
	})
	t.Run("failure-after-first-reprotected-version", func(t *testing.T) {
		target := filepath.Join(fixture.root, "partial-reprotect-target")
		var stage string
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := importWithHooks(ctx, fixture.materialPath, fixture.packagePath, target, []byte(recoveryTestPassword), importHooks{
			afterProtect: func(version uint64) error {
				if version != 1 {
					t.Fatalf("first protected version=%d", version)
				}
				matches, globErr := filepath.Glob(filepath.Join(fixture.root, ".cpa-cloud-key-import-*"))
				if globErr != nil || len(matches) != 1 {
					t.Fatalf("stage=%v err=%v", matches, globErr)
				}
				stage = matches[0]
				if _, statErr := os.Stat(filepath.Join(stage, "provider-store", "bkp_portable-v0000000000000001.dpapi")); statErr != nil {
					t.Fatalf("first re-protected version missing: %v", statErr)
				}
				cancel()
				return nil
			},
			syncParent: syncRecoveryDirectory,
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		assertAbsent(t, target)
		assertAbsent(t, stage)
	})
}

func TestWindowsCleanupRefusalPreservesUnknownStage(t *testing.T) {
	fixture := newRecoveryFixture(t)
	if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, fixture.materialPath, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.root, "cleanup-refusal-target")
	var stage string
	_, err := importWithHooks(context.Background(), fixture.materialPath, fixture.packagePath, target, []byte(recoveryTestPassword), importHooks{
		beforePublish: func(value string) error {
			stage = value
			data := filepath.Join(stage, "data")
			if err := os.Rename(data, filepath.Join(stage, "data-unrecognized")); err != nil {
				return err
			}
			if err := os.Mkdir(data, 0o700); err != nil {
				return err
			}
			return errors.New("injected failure after path replacement")
		},
		syncParent: syncRecoveryDirectory,
	})
	if err == nil || !strings.Contains(err.Error(), "recovery import cleanup failed") {
		t.Fatalf("error=%v", err)
	}
	assertAbsent(t, target)
	if _, err := os.Stat(filepath.Join(stage, "data-unrecognized", "master.key")); err != nil {
		t.Fatalf("restricted stage was not preserved: %v", err)
	}
}

func TestWindowsExportIsAllOrNothingAndNeverOverwrites(t *testing.T) {
	fixture := newRecoveryFixture(t)
	missingOutput := filepath.Join(fixture.root, "missing-version.cpakr")
	if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, missingOutput, fixture.providerID, []uint64{1, 3}, []byte(recoveryTestPassword)); err == nil {
		t.Fatal("missing source version was accepted")
	}
	assertAbsent(t, missingOutput)

	existingOutput := filepath.Join(fixture.root, "existing-material.cpakr")
	if err := os.WriteFile(existingOutput, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, existingOutput, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword)); err == nil {
		t.Fatal("existing recovery material was overwritten")
	}
	value, err := os.ReadFile(existingOutput)
	if err != nil || string(value) != "unchanged" {
		t.Fatalf("existing output changed: %q err=%v", value, err)
	}
}

func TestWindowsImportConflictAndUncertainPublishNeverOverwrite(t *testing.T) {
	fixture := newRecoveryFixture(t)
	if _, err := Export(context.Background(), fixture.sourceStore, fixture.dataDir, fixture.materialPath, fixture.providerID, []uint64{1, 2}, []byte(recoveryTestPassword)); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(fixture.root, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(existing, "marker")
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), fixture.materialPath, fixture.packagePath, existing, []byte(recoveryTestPassword)); err == nil {
		t.Fatal("existing target was overwritten")
	}
	if value, err := os.ReadFile(marker); err != nil || string(value) != "unchanged" {
		t.Fatalf("existing target changed: %q err=%v", value, err)
	}
	raced := filepath.Join(fixture.root, "actor-created-target")
	_, err := importWithHooks(context.Background(), fixture.materialPath, fixture.packagePath, raced, []byte(recoveryTestPassword), importHooks{
		beforePublish: func(string) error {
			if err := os.Mkdir(raced, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(raced, "actor-marker"), []byte("preserve"), 0o600)
		},
		syncParent: syncRecoveryDirectory,
	})
	if err == nil {
		t.Fatal("actor-created target was overwritten")
	}
	if value, readErr := os.ReadFile(filepath.Join(raced, "actor-marker")); readErr != nil || string(value) != "preserve" {
		t.Fatalf("actor-created target changed: %q err=%v", value, readErr)
	}
	stages, globErr := filepath.Glob(filepath.Join(fixture.root, ".cpa-cloud-key-import-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("staging leftovers=%v err=%v", stages, globErr)
	}
	uncertain := filepath.Join(fixture.root, "uncertain")
	_, err = importWithHooks(context.Background(), fixture.materialPath, fixture.packagePath, uncertain, []byte(recoveryTestPassword), importHooks{syncParent: func(string) error { return errors.New("injected sync failure") }})
	if !errors.Is(err, ErrPublishStateUncertain) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(uncertain, "provider-store")); err != nil {
		t.Fatalf("uncertain published target was removed: %v", err)
	}
	if _, err := Import(context.Background(), fixture.materialPath, fixture.packagePath, uncertain, []byte(recoveryTestPassword)); err == nil {
		t.Fatal("uncertain target was overwritten on retry")
	}
}

func copyRecoveryData(t *testing.T, source, target string) {
	t.Helper()
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cpa-cloud.db", "master.key"} {
		value, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, name), value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func openRecoveryTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}

func snapshotFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte, len(entries))
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = sha256.Sum256(content)
	}
	return result
}

func equalDigests(left, right map[string][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for name, digest := range left {
		if right[name] != digest {
			return false
		}
	}
	return true
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path should not exist: %s err=%v", path, err)
	}
}
