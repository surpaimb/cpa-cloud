//go:build linux

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

	wrong := material
	wrong.Key[0] ^= 0xff
	wrongBinding, err := createMasterKeyBinding(&wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer clearBytes(wrongBinding)
	unsafeCases := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"wrong mode", func(t *testing.T, stagingPath string) {
			if err := os.Chmod(stagingPath, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, stagingPath string) {
			if err := os.Link(stagingPath, filepath.Join(t.TempDir(), "staging-alias")); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, stagingPath string) {
			target := filepath.Join(t.TempDir(), "staging-target")
			if err := os.WriteFile(target, wrongBinding, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(stagingPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, stagingPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"oversized", func(t *testing.T, stagingPath string) {
			oversized := append(append([]byte(nil), wrongBinding...), bytes.Repeat([]byte(" "), masterKeyBindingMaxBytes)...)
			defer clearBytes(oversized)
			if err := os.WriteFile(stagingPath, oversized, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range unsafeCases {
		t.Run("unsafe complete staging fails closed: "+test.name, func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			stagingPath := filepath.Join(dataDir, masterKeyBindingStagingFilename)
			if err := os.WriteFile(stagingPath, wrongBinding, 0o600); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, stagingPath)
			before := snapshotMasterKeyProviderDirectory(t, dataDir)
			cfg := Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
			if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); !errors.Is(err, errMasterKeyProviderRejected) {
				t.Fatalf("unsafe staging accepted: %v", err)
			}
			if after := snapshotMasterKeyProviderDirectory(t, dataDir); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected unsafe staging mutated data directory\nbefore=%v\nafter=%v", before, after)
			}
			if _, err := os.Lstat(stagingPath); err != nil {
				t.Fatalf("rejected staging was removed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataDir, masterKeyBindingFilename)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final binding was published for unsafe staging: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataDir, "cpa-cloud.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("database opened for unsafe staging: %v", err)
			}
		})
	}
}

func TestTruncatedMasterKeyBindingJSONClassification(t *testing.T) {
	tests := []struct {
		name      string
		encoded   string
		truncated bool
	}{
		{"empty", "", true},
		{"open object", "{", true},
		{"missing value", `{"format_version":`, true},
		{"malformed value", `{"format_version":]}`, false},
		{"complete object", `{}`, false},
		{"complete non-object", `"value"`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := truncatedMasterKeyBindingJSON([]byte(test.encoded)); got != test.truncated {
				t.Fatalf("truncated=%v want=%v", got, test.truncated)
			}
		})
	}
}

func TestLinuxSystemdCredentialACLAdmissionIsExact(t *testing.T) {
	euid := uint32(os.Geteuid())
	valid := testLinuxSystemdACL(euid, linuxACLRead)
	if !validLinuxSystemdCredentialACL(valid, euid, linuxACLRead) {
		t.Fatal("exact systemd file ACL rejected")
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"malformed", func(value []byte) []byte { return value[:len(value)-1] }},
		{"wrong version", func(value []byte) []byte { binary.LittleEndian.PutUint32(value[:4], 3); return value }},
		{"extra named user", func(value []byte) []byte { return append(value, value[12:20]...) }},
		{"named group", func(value []byte) []byte { binary.LittleEndian.PutUint16(value[12:14], 0x08); return value }},
		{"write grant", func(value []byte) []byte {
			binary.LittleEndian.PutUint16(value[14:16], linuxACLRead|0x02)
			return value
		}},
		{"wrong user", func(value []byte) []byte { binary.LittleEndian.PutUint32(value[16:20], euid+1); return value }},
		{"group access", func(value []byte) []byte { binary.LittleEndian.PutUint16(value[22:24], linuxACLRead); return value }},
		{"other access", func(value []byte) []byte { binary.LittleEndian.PutUint16(value[38:40], linuxACLRead); return value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.mutate(append([]byte(nil), valid...))
			if validLinuxSystemdCredentialACL(candidate, euid, linuxACLRead) {
				t.Fatal("unsafe ACL accepted")
			}
		})
	}
}

func TestLinuxSystemdCredentialLayoutAdmission(t *testing.T) {
	euid, egid := uint32(os.Geteuid()), uint32(os.Getegid())
	rootDirectory := unix.Stat_t{Mode: unix.S_IFDIR | 0o550, Uid: 0, Gid: 0, Nlink: 2}
	rootFile := unix.Stat_t{Mode: unix.S_IFREG | 0o440, Uid: 0, Gid: 0, Nlink: 1}
	if got := classifyLinuxSystemdCredentialObject(&rootDirectory, testLinuxSystemdACL(euid, linuxACLRead|linuxACLExecute), true, true); got != linuxSystemdCredentialLayoutACL {
		t.Fatalf("root ACL directory layout=%v", got)
	}
	if got := classifyLinuxSystemdCredentialObject(&rootFile, testLinuxSystemdACL(euid, linuxACLRead), false, true); got != linuxSystemdCredentialLayoutACL {
		t.Fatalf("root ACL file layout=%v", got)
	}

	ownedDirectory := unix.Stat_t{Mode: unix.S_IFDIR | 0o500, Uid: euid, Gid: egid, Nlink: 2}
	ownedFile := unix.Stat_t{Mode: unix.S_IFREG | 0o400, Uid: euid, Gid: egid, Nlink: 1}
	if got := classifyLinuxSystemdCredentialObject(&ownedDirectory, nil, true, true); got != linuxSystemdCredentialLayoutOwnedReadOnly {
		t.Fatalf("read-only owner fallback directory layout=%v", got)
	}
	if got := classifyLinuxSystemdCredentialObject(&ownedFile, nil, false, true); got != linuxSystemdCredentialLayoutOwnedReadOnly {
		t.Fatalf("read-only owner fallback file layout=%v", got)
	}
	if got := classifyLinuxSystemdCredentialObject(&ownedFile, nil, false, false); got != linuxSystemdCredentialLayoutInvalid {
		t.Fatalf("owner fallback file on writable mount layout=%v", got)
	}
	if got := classifyLinuxSystemdCredentialObject(&ownedDirectory, nil, true, false); got != linuxSystemdCredentialLayoutInvalid {
		t.Fatalf("writable owner fallback layout=%v", got)
	}
	if got := classifyLinuxSystemdCredentialObject(&ownedFile, testLinuxSystemdACL(euid, linuxACLRead), false, true); got != linuxSystemdCredentialLayoutInvalid {
		t.Fatalf("owner fallback with extended ACL layout=%v", got)
	}
	rootFile.Mode = unix.S_IFREG | 0o400
	if got := classifyLinuxSystemdCredentialObject(&rootFile, testLinuxSystemdACL(euid, linuxACLRead), false, true); got != linuxSystemdCredentialLayoutInvalid {
		t.Fatalf("root ACL file with mismatched mode layout=%v", got)
	}
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
	originalReader := linuxReadSystemdCredential
	linuxReadSystemdCredential = func(directory, name string, maxBytes int) ([]byte, error) {
		return readLinuxPrivateFile(directory, name, maxBytes, 0o700, 0o400)
	}
	t.Cleanup(func() { linuxReadSystemdCredential = originalReader })
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

func testLinuxSystemdACL(euid uint32, permission uint16) []byte {
	entries := []struct {
		tag  uint16
		perm uint16
		id   uint32
	}{
		{linuxACLUserObj, permission, linuxACLUndefinedID},
		{linuxACLUser, permission, euid},
		{linuxACLGroupObj, 0, linuxACLUndefinedID},
		{linuxACLMask, permission, linuxACLUndefinedID},
		{linuxACLOther, 0, linuxACLUndefinedID},
	}
	encoded := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(encoded[:4], linuxACLXattrVersion)
	for index, entry := range entries {
		offset := 4 + index*8
		binary.LittleEndian.PutUint16(encoded[offset:offset+2], entry.tag)
		binary.LittleEndian.PutUint16(encoded[offset+2:offset+4], entry.perm)
		binary.LittleEndian.PutUint32(encoded[offset+4:offset+8], entry.id)
	}
	return encoded
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
