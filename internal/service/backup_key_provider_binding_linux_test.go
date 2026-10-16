//go:build linux

package service

// Independently authored regression tests for ADR 0005 and PR78 review findings.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
)

func newLinuxBackupFixture(t *testing.T, rehearsal bool) (*backupAutomationFixture, string, string, masterKeyMaterial) {
	t.Helper()
	root := t.TempDir()
	credentialDir := t.TempDir()
	installation := testMasterKeyMaterial(0xa5)
	writeLinuxSystemdCredential(t, credentialDir, installation)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDir)
	cfg := Config{DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	worker, err := newBackupAutomationCoordinator(context.Background(), app, BackupAutomationConfig{
		Enabled: true, DataDir: cfg.DataDir, OutputRoot: filepath.Join(root, "backups"), ProviderStoreRoot: filepath.Join(root, "keys"), SourceVersion: "test", PrepareRehearsalConfig: prepareBackupRehearsalConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { worker.Close() })
	keys := newFakeBackupKeyStore()
	worker.keys = keys
	fixture := &backupAutomationFixture{app: app, worker: worker, keys: keys, root: root}
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&fixture.adminID); err != nil {
		t.Fatal(err)
	}
	providerID, planID := fixture.seedProviderAndPlan(t, 1, rehearsal)
	keys.configured = &keyprovider.Reference{ProviderID: providerID, Kind: keyprovider.KindLinuxSystemdCredentialV1, InstanceID: installation.InstanceID, KeyID: "separate-backup-key", Version: 1}
	for index := range keys.configuredKey {
		keys.configuredKey[index] = byte(index + 1)
	}
	if _, err := app.store.db.Exec(`UPDATE backup_key_providers SET kind=?,scope='systemd-service',instance_id=?,key_id=? WHERE id=?`, keys.configured.Kind, keys.configured.InstanceID, keys.configured.KeyID, providerID); err != nil {
		t.Fatal(err)
	}
	return fixture, planID, credentialDir, installation
}

func TestLinuxSystemdBackupWorkerRealV3RehearsalCleanupAndRetention(t *testing.T) {
	fixture, planID, _, _ := newLinuxBackupFixture(t, true)
	var previous backupRunView
	for iteration := 0; iteration < 2; iteration++ {
		claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
		if err != nil || claim == nil {
			t.Fatalf("claim=%v err=%v", claim, err)
		}
		// Execute synchronously, with the real v3 codec and real service Open/Close.
		fixture.worker.execute(context.Background(), *claim)
		run, err := loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
		if err != nil || run.Status != "succeeded" || run.RehearsalStatus != "succeeded" || run.ErrorCode != nil || !run.PackageRetained {
			t.Fatalf("run=%+v err=%v", run, err)
		}
		leftovers, err := filepath.Glob(filepath.Join(fixture.worker.output.path, ".cpa-cloud-rehearsal-*"))
		if err != nil || len(leftovers) != 0 {
			t.Fatalf("rehearsal leftovers=%v err=%v", leftovers, err)
		}
		if previous.ID != "" {
			old, err := loadBackupRun(context.Background(), fixture.app.store.db, previous.ID)
			if err != nil || old.PackageRetained || old.PackageDeletedAt == nil {
				t.Fatalf("retention=%+v err=%v", old, err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, previous.PackageName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old package remains: %v", err)
			}
		}
		previous = run
	}
}

func TestLinuxSystemdBackupWorkerRejectsPostStartupBindingChanges(t *testing.T) {
	for _, mutation := range []string{"canary", "key-id", "version", "authenticated-replacement"} {
		t.Run(mutation, func(t *testing.T) {
			fixture, planID, _, installation := newLinuxBackupFixture(t, false)
			path := filepath.Join(fixture.app.cfg.DataDir, masterKeyBindingFilename)
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var binding masterKeyBinding
			if err := json.Unmarshal(encoded, &binding); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "canary":
				binding.CanaryCiphertext = base64.RawStdEncoding.EncodeToString(make([]byte, 44))
			case "key-id":
				binding.KeyID = "different-installation-key"
			case "version":
				binding.KeyVersion++
			}
			encoded, err = json.Marshal(binding)
			if mutation == "authenticated-replacement" {
				encoded, err = createMasterKeyBinding(&installation)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
			if err != nil || claim == nil {
				t.Fatalf("claim=%v err=%v", claim, err)
			}
			fixture.worker.execute(context.Background(), *claim)
			run, err := loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
			if err != nil || run.Status != "failed" || run.ErrorCode == nil || *run.ErrorCode != "backup_create_failed" || run.PackageRetained {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, claim.PackageName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("corrupt backup published: %v", err)
			}
		})
	}
}

func TestLinuxSystemdBackupRestoreRejectsChangedOrMissingInstallationCredential(t *testing.T) {
	fixture, _, credentialDir, installation := newLinuxBackupFixture(t, false)
	ref := fixture.keys.configured
	material := backup.KeyMaterial{ProviderID: ref.ProviderID, ProviderKind: ref.Kind, InstanceID: ref.InstanceID, KeyID: ref.KeyID, ProviderVersion: ref.Version, WrappingKey: fixture.keys.configuredKey, ValidateInstallationBinding: fixture.worker.validateInstallationBinding}
	packagePath := filepath.Join(fixture.root, "valid.cpacb")
	if _, err := backup.CreateWithKeyMaterial(context.Background(), fixture.app.cfg.DataDir, packagePath, material, "test"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"wrong-key", "wrong-version", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := installation
			if mutation == "wrong-key" {
				candidate.Key[0] ^= 0xff
			}
			if mutation == "wrong-version" {
				candidate.KeyVersion++
			}
			writeLinuxSystemdCredential(t, credentialDir, candidate)
			if mutation == "missing" {
				if err := os.Remove(filepath.Join(credentialDir, masterKeyCredentialName)); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(fixture.root, "restore-"+mutation)
			if _, err := backup.RestoreWithKeyMaterial(context.Background(), packagePath, target, material); err == nil {
				t.Fatal("invalid installation credential restored")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target published: %v", err)
			}
		})
	}
}
