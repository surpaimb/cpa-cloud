//go:build linux && cpacloud_systemd_acceptance

package service

// Independently authored for docs/linux-systemd-backup-rehearsal-contract.md.
// Only the disposable-VM harness runs this test. Neither credential reader is
// replaced: both read the actual systemd activation mount in every invocation.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
	"golang.org/x/sys/unix"
)

const activationRecord = "synthetic-systemd-backup-record-only"

func TestSystemdBackupActivation(t *testing.T) {
	uid, err := strconv.Atoi(os.Getenv("CPA_ACCEPTANCE_UID"))
	if err != nil || uid == 0 || os.Geteuid() != uid || os.Getenv("INVOCATION_ID") == "" {
		t.Fatal("activation identity rejected")
	}
	netns, err := os.Readlink("/proc/self/ns/net")
	if err != nil || netns == os.Getenv("CPA_ACCEPTANCE_HOST_NETNS") || os.Getenv("CPA_ACCEPTANCE_HOST_NETNS") == "" {
		t.Fatal("network namespace isolation missing")
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM, 0)
		if err == nil {
			unix.Close(fd)
			t.Fatal("network socket isolation missing")
		}
		if !errors.Is(err, unix.EAFNOSUPPORT) && !errors.Is(err, unix.EPERM) {
			t.Fatal("unexpected socket rejection")
		}
	}
	root := os.Getenv("CPA_ACCEPTANCE_ROOT")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("invalid acceptance root")
	}
	assertActivationPrivate(t, root, true)
	mode := os.Getenv("CPA_ACCEPTANCE_CASE")
	if mode == "source" {
		activationCreate(t, root)
		return
	}
	wantStage, known := map[string]string{
		"restore": "", "missing-master": "binding", "wrong-master": "binding",
		"swapped-master": "binding", "swapped-both": "binding", "tampered-binding": "binding", "tampered-canary": "binding",
		"missing-backup": "provider", "swapped-backup": "provider",
		"same-value": "provider", "wrong-backup": "restore",
	}[mode]
	if !known {
		t.Fatal("unknown acceptance case")
	}
	// The second UID must not be able to read the first UID's live database.
	if _, err := os.ReadFile(os.Getenv("CPA_ACCEPTANCE_SOURCE_DB")); !errors.Is(err, os.ErrPermission) {
		t.Fatal("source UID isolation missing")
	}
	target := filepath.Join(root, "restored")
	assertActivationAbsent(t, target)
	material, stage := activationMaterial(filepath.Join(root, "seed"))
	defer clearBytes(material.WrappingKey[:])
	if stage != "" {
		if stage != wantStage {
			t.Fatalf("unexpected preparation failure at %s", stage)
		}
		assertActivationAbsent(t, target)
		t.Log("EXPECTED_REJECTION " + mode)
		return
	}
	if wantStage == "binding" || wantStage == "provider" {
		t.Fatal("invalid credential or binding admitted")
	}
	input := filepath.Join(root, "input.cpacb")
	info, verifyErr := backup.VerifyWithKeyMaterial(context.Background(), input, material)
	if wantStage == "restore" {
		if verifyErr == nil {
			t.Fatal("wrong wrapping key verified")
		}
		if _, err := backup.RestoreWithKeyMaterial(context.Background(), input, target, material); err == nil {
			t.Fatal("wrong wrapping key restored")
		}
		assertActivationAbsent(t, target)
		t.Log("EXPECTED_REJECTION " + mode)
		return
	}
	if verifyErr != nil {
		t.Fatal("package verification failed")
	}
	assertActivationV3(t, info)
	info, err = backup.RestoreWithKeyMaterial(context.Background(), input, target, material)
	if err != nil {
		t.Fatal("restore failed")
	}
	assertActivationV3(t, info)
	assertActivationPrivate(t, target, true)
	for _, name := range []string{"cpa-cloud.db", masterKeyBindingFilename} {
		assertActivationPrivate(t, filepath.Join(target, name), false)
	}
	assertActivationAbsent(t, filepath.Join(target, rootKeyFilename))
	// Exercise actual service startup, encrypted data access, shutdown and reopen
	// under the new UID. No HTTP listener or network worker is started.
	for attempt := 0; attempt < 2; attempt++ {
		app, err := Open(context.Background(), activationConfig(target))
		if err != nil {
			t.Fatal("restored service open failed")
		}
		var ciphertext []byte
		readErr := app.store.db.QueryRow(`SELECT credential FROM systemd_backup_acceptance WHERE id=1`).Scan(&ciphertext)
		plaintext, decryptErr := app.secrets.decryptCredential("synthetic-record", ciphertext)
		var admins int
		adminErr := app.store.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&admins)
		closeErr := app.Close()
		if readErr != nil || decryptErr != nil || plaintext != activationRecord || adminErr != nil || admins != 1 || closeErr != nil {
			t.Fatal("restored service state or close failed")
		}
	}
	t.Log("RESTORE_OPEN_CLOSE_OK")
}

func activationConfig(data string) Config {
	cfg := Config{DataDir: data, MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1, Version: "systemd-acceptance"}
	prepareBackupRehearsalConfig(&cfg)
	return cfg
}

func activationMaterial(data string) (backup.KeyMaterial, string) {
	binding, validate, err := backupKeyProviderInstanceBinding(activationConfig(data))
	if err != nil {
		return backup.KeyMaterial{}, "binding"
	}
	store, err := keyprovider.OpenForInstance("", binding)
	if err != nil {
		return backup.KeyMaterial{}, "provider"
	}
	ref, ok := store.ConfiguredReference()
	if !ok {
		return backup.KeyMaterial{}, "provider"
	}
	key, err := store.Resolve(context.Background(), ref.ProviderID, ref.Version)
	if err != nil {
		return backup.KeyMaterial{}, "provider"
	}
	defer key.Destroy()
	return backup.KeyMaterial{ProviderID: ref.ProviderID, ProviderKind: ref.Kind,
		InstanceID: ref.InstanceID, KeyID: ref.KeyID, ProviderVersion: ref.Version,
		WrappingKey: key.Key, ValidateInstallationBinding: validate}, ""
}

func activationCreate(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	cfg := activationConfig(filepath.Join(root, "data"))
	if InitializeWithConfig(ctx, cfg, strings.NewReader("synthetic-systemd-admin-only\n")) != nil {
		t.Fatal("source initialization failed")
	}
	app, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal("source service open failed")
	}
	closed := false
	defer func() {
		if !closed {
			_ = app.Close()
		}
	}()
	ciphertext, err := app.secrets.encryptCredential("synthetic-record", activationRecord)
	if err != nil {
		t.Fatal("synthetic record encryption failed")
	}
	if _, err = app.store.db.Exec(`CREATE TABLE systemd_backup_acceptance(id INTEGER PRIMARY KEY, credential BLOB NOT NULL)`); err != nil {
		t.Fatal("synthetic table failed")
	}
	if _, err = app.store.db.Exec(`INSERT INTO systemd_backup_acceptance(id,credential) VALUES(1,?)`, ciphertext); err != nil {
		t.Fatal("synthetic record failed")
	}
	material, stage := activationMaterial(cfg.DataDir)
	defer clearBytes(material.WrappingKey[:])
	if stage != "" {
		t.Fatalf("source material failed at %s", stage)
	}
	output := filepath.Join(root, "output.cpacb")
	info, err := backup.CreateWithKeyMaterial(ctx, cfg.DataDir, output, material, cfg.Version)
	if err != nil {
		t.Fatal("source backup failed")
	}
	assertActivationV3(t, info)
	info, err = backup.VerifyWithKeyMaterial(ctx, output, material)
	if err != nil {
		t.Fatal("source verify failed")
	}
	assertActivationV3(t, info)
	assertActivationAbsent(t, filepath.Join(cfg.DataDir, rootKeyFilename))
	// Pinning was established with real credentials above. A later file change
	// must not publish a package even while the real source database is open.
	bindingPath := filepath.Join(cfg.DataDir, masterKeyBindingFilename)
	original, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal("read source binding failed")
	}
	changed := strings.Replace(string(original), "installation-root", "installation-evil", 1)
	if changed == string(original) || os.WriteFile(bindingPath, []byte(changed), 0o600) != nil {
		t.Fatal("binding mutation failed")
	}
	badOutput := filepath.Join(root, "must-not-publish.cpacb")
	_, createErr := backup.CreateWithKeyMaterial(ctx, cfg.DataDir, badOutput, material, cfg.Version)
	if os.WriteFile(bindingPath, original, 0o600) != nil || createErr == nil {
		t.Fatal("changed binding accepted or reset failed")
	}
	assertActivationAbsent(t, badOutput)
	if app.Close() != nil {
		t.Fatal("source service close failed")
	}
	closed = true
	t.Log("SOURCE_V3_AND_BINDING_REJECTION_OK")
}

func assertActivationAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected published target")
	}
}

func assertActivationPrivate(t *testing.T, path string, directory bool) {
	t.Helper()
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal("unexpected file owner")
	}
	want := uint32(unix.S_IFREG | 0o600)
	if directory {
		want = unix.S_IFDIR | 0o700
	}
	if stat.Mode != want {
		t.Fatal("unexpected file type or permissions")
	}
}

func assertActivationV3(t *testing.T, info backup.Info) {
	t.Helper()
	if info.FormatVersion != 3 || len(info.Files) != 2 || info.KeyProvider == nil || info.KeyProvider.Kind != keyprovider.KindLinuxSystemdCredentialV1 {
		t.Fatal("unexpected package format")
	}
	if info.KeyProvider.ID != "bkp_synthetic" || info.KeyProvider.InstanceID != "01234567-89ab-cdef-0123-456789abcdef" || info.KeyProvider.KeyID != "backup-root" || info.KeyProvider.Version != 1 {
		t.Fatal("unexpected package key reference")
	}
	files := map[string]bool{}
	for _, file := range info.Files {
		files[file.Name] = true
	}
	if !files["cpa-cloud.db"] || !files[masterKeyBindingFilename] {
		t.Fatal("unexpected package records")
	}
}
