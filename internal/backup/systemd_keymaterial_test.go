package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
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
	material.ValidateInstallationBinding = func(encoded []byte) error {
		var binding systemdMasterBinding
		if json.Unmarshal(encoded, &binding) != nil || binding.KeyID != "installation-master-key" || binding.KeyVersion != 9 {
			return errors.New("synthetic installation binding rejected")
		}
		nonce, err := base64.RawStdEncoding.DecodeString(binding.CanaryNonce)
		if err != nil || len(nonce) != 12 {
			return errors.New("invalid nonce")
		}
		sealed, err := base64.RawStdEncoding.DecodeString(binding.CanaryCiphertext)
		if err != nil {
			return err
		}
		plaintext, err := syntheticInstallationAEAD().Open(nil, nonce, sealed, syntheticInstallationAAD(binding.InstanceID, binding.KeyID))
		if err != nil || !bytes.Equal(plaintext, []byte("cpa-cloud-master-key-canary-v1")) {
			return errors.New("synthetic installation canary rejected")
		}
		return nil
	}
	return material
}

// Fixed synthetic installation credential, separate from the wrapping key.
func syntheticInstallationAEAD() cipher.AEAD {
	block, _ := aes.NewCipher(bytes.Repeat([]byte{0xa5}, 32))
	aead, _ := cipher.NewGCM(block)
	return aead
}

func syntheticInstallationAAD(instanceID, keyID string) []byte {
	return []byte("cpa-cloud/master-key-canary/v1\x00" + linuxSystemdCredentialKind + "\x00" + instanceID + "\x00" + keyID + "\x009")
}

func writeSystemdBinding(t *testing.T, directory string, material KeyMaterial, installationKeyID string) {
	t.Helper()
	nonce := bytes.Repeat([]byte{0x42}, 12)
	sealed := syntheticInstallationAEAD().Seal(nil, nonce, []byte("cpa-cloud-master-key-canary-v1"), syntheticInstallationAAD(material.InstanceID, installationKeyID))
	binding := systemdMasterBinding{FormatVersion: 1, ProviderKind: linuxSystemdCredentialKind, InstanceID: material.InstanceID, KeyID: installationKeyID, KeyVersion: 9, CanaryNonce: base64.RawStdEncoding.EncodeToString(nonce), CanaryCiphertext: base64.RawStdEncoding.EncodeToString(sealed)}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, systemdBindingFilename), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdKeyMaterialRequiresInstallationAuthenticationBeforePublication(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openSyntheticDatabase(t, source)
	db.Close()
	material := testSystemdKeyMaterial()
	writeSystemdBinding(t, source, material, "installation-master-key")
	packagePath := filepath.Join(root, "valid.cpacb")
	if _, err := CreateWithKeyMaterial(context.Background(), source, packagePath, material, "test"); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"missing-verifier", "missing-credential", "changed-before-publish"} {
		t.Run(failure, func(t *testing.T) {
			candidate := material
			calls := 0
			candidate.ValidateInstallationBinding = func(encoded []byte) error {
				calls++
				if failure == "changed-before-publish" && calls == 1 {
					return material.ValidateInstallationBinding(encoded)
				}
				return errors.New("installation credential unavailable")
			}
			if failure == "missing-verifier" {
				candidate.ValidateInstallationBinding = nil
			}
			target := filepath.Join(root, "restore-"+failure)
			if _, err := RestoreWithKeyMaterial(context.Background(), packagePath, target, candidate); err == nil {
				t.Fatal("restore accepted unavailable installation credential")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore published: %v", err)
			}
			calls = 0
			output := filepath.Join(root, failure+".cpacb")
			if _, err := CreateWithKeyMaterial(context.Background(), source, output, candidate, "test"); err == nil {
				t.Fatal("create accepted unavailable installation credential")
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup published: %v", err)
			}
		})
	}
	// Corrupt a syntactically valid canary, including a re-authenticated package
	// forged with the backup wrapping key but without the installation key.
	encoded, err := os.ReadFile(filepath.Join(source, systemdBindingFilename))
	if err != nil {
		t.Fatal(err)
	}
	var binding systemdMasterBinding
	if err := json.Unmarshal(encoded, &binding); err != nil {
		t.Fatal(err)
	}
	binding.CanaryCiphertext = base64.RawStdEncoding.EncodeToString(make([]byte, 44))
	corrupted, _ := json.Marshal(binding)
	if err := os.WriteFile(filepath.Join(source, systemdBindingFilename), corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWithKeyMaterial(context.Background(), source, filepath.Join(root, "corrupted.cpacb"), material, "test"); err == nil {
		t.Fatal("corrupt canary accepted")
	}
	// Construct a malicious package without using the guarded create API.
	dbBytes, err := os.ReadFile(filepath.Join(source, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	records := []record{{name: databaseFilename, data: dbBytes, sum: sha256.Sum256(dbBytes)}, {name: systemdBindingFilename, data: corrupted, sum: sha256.Sum256(corrupted)}}
	ref := material.reference()
	payload, err := encodePayload(newInfoForFormat(3, "test", records, &ref), records)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := sealSystemdPackage(&material, payload)
	if err != nil {
		t.Fatal(err)
	}
	forgedPath := filepath.Join(root, "forged.cpacb")
	if err := os.WriteFile(forgedPath, forged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWithKeyMaterial(context.Background(), forgedPath, material); err == nil {
		t.Fatal("forged canary verified")
	}
	target := filepath.Join(root, "forged-restore")
	if _, err := RestoreWithKeyMaterial(context.Background(), forgedPath, target, material); err == nil {
		t.Fatal("forged canary restored")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forged restore published: %v", err)
	}
}
