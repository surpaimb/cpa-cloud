//go:build linux

package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLinuxSystemdMasterKeyProviderInitializesAndReopensWithoutLegacyKey(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x51)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dataDir, rootKeyFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy master key exists: %v", err)
	}
	bindingInfo, err := os.Lstat(filepath.Join(dataDir, masterKeyBindingFilename))
	if err != nil {
		t.Fatal(err)
	}
	if bindingInfo.Mode().Perm() != 0o600 || !bindingInfo.Mode().IsRegular() {
		t.Fatalf("binding mode=%v", bindingInfo.Mode())
	}
	initialized, err := CheckInitializedWithConfig(context.Background(), cfg)
	if err != nil || !initialized {
		t.Fatalf("initialized=%v err=%v", initialized, err)
	}
	app, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxSystemdMasterKeyProviderRejectsMetadataOrKeyMismatchBeforeDatabaseOpen(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x61)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*masterKeyMaterial)
	}{
		{"wrong key", func(value *masterKeyMaterial) { value.Key[0] ^= 0xff }},
		{"wrong instance", func(value *masterKeyMaterial) { value.InstanceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" }},
		{"wrong key id", func(value *masterKeyMaterial) { value.KeyID = "different-key" }},
		{"wrong version", func(value *masterKeyMaterial) { value.KeyVersion++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := material
			test.mutate(&candidate)
			writeLinuxSystemdCredential(t, credentialDirectory, candidate)
			before := snapshotMasterKeyProviderDirectory(t, dataDir)
			if app, err := Open(context.Background(), cfg); !errors.Is(err, errMasterKeyProviderRejected) {
				if app != nil {
					app.Close()
				}
				t.Fatalf("mismatch accepted: %v", err)
			}
			after := snapshotMasterKeyProviderDirectory(t, dataDir)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected startup mutated data directory\nbefore=%v\nafter=%v", before, after)
			}
			writeLinuxSystemdCredential(t, credentialDirectory, material)
		})
	}
}

func TestLinuxSystemdMasterKeyProviderRejectsMissingOrUnsafeCredentialWithoutFallback(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x71)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if _, err := loadSecretsForConfig(cfg); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("legacy fallback accepted: %v", err)
	}

	if err := os.Remove(filepath.Join(credentialDirectory, masterKeyCredentialName)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSecretsForConfig(cfg); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("missing credential accepted: %v", err)
	}
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	if err := os.Chmod(filepath.Join(credentialDirectory, masterKeyCredentialName), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLinuxSystemdMasterKeyCredential(); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("unsafe credential mode accepted: %v", err)
	}
}

func TestLinuxSystemdMasterKeyProviderRejectsBeforeCreatingOrMigratingDataDirectory(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x81)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "must-stay-absent")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if app, err := Open(context.Background(), cfg); !errors.Is(err, errMasterKeyProviderRejected) {
		if app != nil {
			app.Close()
		}
		t.Fatalf("missing binding accepted: %v", err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected startup created data directory: %v", err)
	}
}

func TestLinuxSystemdMasterKeyProviderRejectsHardlinkedCredentialAndUnsafeBinding(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x82)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	credentialPath := filepath.Join(credentialDirectory, masterKeyCredentialName)
	credentialAlias := filepath.Join(credentialDirectory, "credential-alias")
	if err := os.Link(credentialPath, credentialAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLinuxSystemdMasterKeyCredential(); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("hardlinked credential accepted: %v", err)
	}
	if err := os.Remove(credentialAlias); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	bindingPath := filepath.Join(dataDir, masterKeyBindingFilename)
	if err := os.Chmod(bindingPath, 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotMasterKeyProviderDirectory(t, dataDir)
	if app, err := Open(context.Background(), cfg); !errors.Is(err, errMasterKeyProviderRejected) {
		if app != nil {
			app.Close()
		}
		t.Fatalf("unsafe binding mode accepted: %v", err)
	}
	if after := snapshotMasterKeyProviderDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected binding mutated data directory\nbefore=%v\nafter=%v", before, after)
	}
}

func TestLinuxSystemdMasterKeyProviderInitializationCanRetryExactCredential(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x83)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("too-short\n")); err == nil {
		t.Fatal("invalid administrator password accepted")
	}
	if _, err := os.Stat(filepath.Join(dataDir, masterKeyBindingFilename)); err != nil {
		t.Fatalf("binding was not retained for exact retry: %v", err)
	}

	wrong := material
	wrong.Key[0] ^= 0xff
	writeLinuxSystemdCredential(t, credentialDirectory, wrong)
	before := snapshotMasterKeyProviderDirectory(t, dataDir)
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("different credential adopted partial initialization: %v", err)
	}
	if after := snapshotMasterKeyProviderDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected retry mutated data directory\nbefore=%v\nafter=%v", before, after)
	}

	writeLinuxSystemdCredential(t, credentialDirectory, material)
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatalf("exact retry failed: %v", err)
	}
}

func TestLinuxSystemdMasterKeyProviderMissingBindingCannotRebindExistingDatabase(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x84)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dataDir, masterKeyBindingFilename)); err != nil {
		t.Fatal(err)
	}
	wrong := material
	wrong.Key[0] ^= 0xff
	writeLinuxSystemdCredential(t, credentialDirectory, wrong)
	before := snapshotMasterKeyProviderDirectory(t, dataDir)
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("existing database rebound after binding loss: %v", err)
	}
	if after := snapshotMasterKeyProviderDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected rebinding mutated data directory\nbefore=%v\nafter=%v", before, after)
	}
}

func TestLinuxSystemdMasterKeyProviderRecoversInterruptedStaging(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x85)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)

	t.Run("partial staging is replaced only in an otherwise fresh directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, masterKeyBindingStagingFilename), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
		if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(dataDir, masterKeyBindingStagingFilename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial staging remains: %v", err)
		}
	})

	t.Run("complete file-synced staging is atomically published", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		binding, err := createMasterKeyBinding(&material)
		if err != nil {
			t.Fatal(err)
		}
		defer clearBytes(binding)
		stagingPath := filepath.Join(dataDir, masterKeyBindingStagingFilename)
		if err := os.WriteFile(stagingPath, binding, 0o600); err != nil {
			t.Fatal(err)
		}
		staged, err := os.OpenFile(stagingPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := staged.Sync(); err != nil {
			staged.Close()
			t.Fatal(err)
		}
		if err := staged.Close(); err != nil {
			t.Fatal(err)
		}
		cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
		if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(stagingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("published staging remains: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, masterKeyBindingFilename)); err != nil {
			t.Fatalf("binding was not published: %v", err)
		}
	})

	t.Run("complete staging from a different credential fails closed", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		wrong := material
		wrong.Key[0] ^= 0xff
		binding, err := createMasterKeyBinding(&wrong)
		if err != nil {
			t.Fatal(err)
		}
		defer clearBytes(binding)
		stagingPath := filepath.Join(dataDir, masterKeyBindingStagingFilename)
		if err := os.WriteFile(stagingPath, binding, 0o600); err != nil {
			t.Fatal(err)
		}
		before := snapshotMasterKeyProviderDirectory(t, dataDir)
		cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
		if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); !errors.Is(err, errMasterKeyProviderRejected) {
			t.Fatalf("different credential adopted complete staging: %v", err)
		}
		if after := snapshotMasterKeyProviderDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
			t.Fatalf("rejected complete staging mutated data directory\nbefore=%v\nafter=%v", before, after)
		}
		if _, err := os.Stat(filepath.Join(dataDir, masterKeyBindingFilename)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("final binding was published for wrong credential: %v", err)
		}
	})
}

func TestLinuxSystemdMasterKeyProviderRetriesDirectorySyncAfterPublish(t *testing.T) {
	credentialDirectory := t.TempDir()
	material := testMasterKeyMaterial(0x86)
	writeLinuxSystemdCredential(t, credentialDirectory, material)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDirectory)
	dataDir := filepath.Join(t.TempDir(), "data")
	cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}

	originalSync := linuxSyncDirectory
	linuxSyncDirectory = func(int) error { return errors.New("synthetic directory sync failure") }
	err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n"))
	linuxSyncDirectory = originalSync
	defer func() { linuxSyncDirectory = originalSync }()
	if !errors.Is(err, errMasterKeyProviderRejected) {
		t.Fatalf("directory sync failure accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, masterKeyBindingFilename)); err != nil {
		t.Fatalf("fully written binding not retained for sync retry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "cpa-cloud.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database opened after directory sync failure: %v", err)
	}
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatalf("exact retry did not repeat durability sync: %v", err)
	}
}

func writeLinuxSystemdCredential(t *testing.T, directory string, material masterKeyMaterial) {
	t.Helper()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, masterKeyCredentialName)
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, testSystemdCredentialJSON(t, material), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

type masterKeyProviderSnapshot struct {
	Mode   os.FileMode
	Size   int64
	Digest [32]byte
}

func snapshotMasterKeyProviderDirectory(t *testing.T, root string) map[string]masterKeyProviderSnapshot {
	t.Helper()
	result := make(map[string]masterKeyProviderSnapshot)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = masterKeyProviderSnapshot{Mode: info.Mode(), Size: info.Size(), Digest: sha256.Sum256(contents)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}
