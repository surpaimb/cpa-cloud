package backup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemdKeyMaterialCreateVerifyRestore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openSyntheticDatabase(t, source)
	if _, err := db.Exec(`INSERT INTO durable_marker(value) VALUES('systemd-v3')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	material := testSystemdKeyMaterial()
	writeSystemdBinding(t, source, material, "installation-master-key")
	packagePath := filepath.Join(root, "systemd.cpacb")
	created, err := CreateWithKeyMaterial(context.Background(), source, packagePath, material, "systemd-v3-test")
	if err != nil {
		t.Fatal(err)
	}
	if created.FormatVersion != 3 || created.KeyProvider == nil || *created.KeyProvider != material.reference() || created.Files[1].Name != systemdBindingFilename {
		t.Fatalf("metadata=%+v", created)
	}
	verified, err := VerifyWithKeyMaterial(context.Background(), packagePath, material)
	if err != nil {
		t.Fatal(err)
	}
	if !equalKeyReference(created.KeyProvider, verified.KeyProvider) {
		t.Fatal("verified metadata lost binding")
	}
	target := filepath.Join(root, "restored")
	if _, err := RestoreWithKeyMaterial(context.Background(), packagePath, target, material); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, rootKeyFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy master key restored: %v", err)
	}
	if restoredBinding, err := os.ReadFile(filepath.Join(target, systemdBindingFilename)); err != nil || len(restoredBinding) == 0 {
		t.Fatalf("binding err=%v", err)
	}
	restored, err := sql.Open("sqlite", filepath.Join(target, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertCount(t, restored, `SELECT COUNT(*) FROM durable_marker WHERE value='systemd-v3'`, 1)
}

func TestSystemdKeyMaterialRejectsWrongAndMixedBindingBeforePublish(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openSyntheticDatabase(t, source)
	db.Close()
	material := testSystemdKeyMaterial()
	writeSystemdBinding(t, source, material, "installation-master-key")
	packagePath := filepath.Join(root, "systemd.cpacb")
	if _, err := CreateWithKeyMaterial(context.Background(), source, packagePath, material, "test"); err != nil {
		t.Fatal(err)
	}
	wrongInstance := material
	wrongInstance.InstanceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	wrongKeyID := material
	wrongKeyID.KeyID = "other-backup-key"
	wrongVersion := material
	wrongVersion.ProviderVersion++
	wrongKey := material
	wrongKey.WrappingKey[0] ^= 0xff
	for name, candidate := range map[string]KeyMaterial{"instance": wrongInstance, "key-id": wrongKeyID, "version": wrongVersion, "key": wrongKey} {
		t.Run(name, func(t *testing.T) {
			target := filepath.Join(root, "restore-"+name)
			if _, err := RestoreWithKeyMaterial(context.Background(), packagePath, target, candidate); !errors.Is(err, errKeyMaterialAuthentication) {
				t.Fatalf("error=%v", err)
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target exists: %v", err)
			}
		})
	}
	mixedSource := filepath.Join(root, "mixed")
	if err := os.Mkdir(mixedSource, 0o700); err != nil {
		t.Fatal(err)
	}
	db = openSyntheticDatabase(t, mixedSource)
	db.Close()
	writeSystemdBinding(t, mixedSource, material, material.KeyID)
	if _, err := CreateWithKeyMaterial(context.Background(), mixedSource, filepath.Join(root, "mixed.cpacb"), material, "test"); err == nil {
		t.Fatal("installation and backup key id reuse accepted")
	}
}

func testSystemdKeyMaterial() KeyMaterial {
	material := KeyMaterial{ProviderID: "bkp_linux_synthetic", ProviderKind: linuxSystemdCredentialKind, InstanceID: "01234567-89ab-cdef-0123-456789abcdef", KeyID: "backup-wrapping-key", ProviderVersion: 4}
	for index := range material.WrappingKey {
		material.WrappingKey[index] = byte(index + 1)
	}
	return material
}

func writeSystemdBinding(t *testing.T, directory string, material KeyMaterial, installationKeyID string) {
	t.Helper()
	binding := systemdMasterBinding{FormatVersion: 1, ProviderKind: linuxSystemdCredentialKind, InstanceID: material.InstanceID, KeyID: installationKeyID, KeyVersion: 9, CanaryNonce: base64.RawStdEncoding.EncodeToString(make([]byte, 12)), CanaryCiphertext: base64.RawStdEncoding.EncodeToString(make([]byte, 16))}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, systemdBindingFilename), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
