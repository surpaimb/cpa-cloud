package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyMaterialCreateVerifyRestore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	if _, err := db.Exec(`INSERT INTO durable_marker(value) VALUES('key-material-v2')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	material := testKeyMaterial(7)
	packagePath := filepath.Join(root, "automatic.cpacb")
	created, err := CreateWithKeyMaterial(context.Background(), source, packagePath, material, "test-v2")
	if err != nil {
		t.Fatal(err)
	}
	if created.FormatVersion != 2 || created.KeyProvider == nil || *created.KeyProvider != material.reference() {
		t.Fatalf("unexpected metadata: %#v", created)
	}
	verified, err := VerifyWithKeyMaterial(context.Background(), packagePath, material)
	if err != nil {
		t.Fatal(err)
	}
	if verified.CreatedAt != created.CreatedAt || !equalKeyReference(verified.KeyProvider, created.KeyProvider) {
		t.Fatalf("verify metadata mismatch: %#v %#v", created, verified)
	}
	if _, err := Verify(context.Background(), packagePath, testPassword); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("password parser accepted v2 package: %v", err)
	}

	target := filepath.Join(root, "restored")
	if _, err := RestoreWithKeyMaterial(context.Background(), packagePath, target, material); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", filepath.Join(target, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertCount(t, restored, `SELECT COUNT(*) FROM durable_marker WHERE value='key-material-v2'`, 1)
}

func TestKeyMaterialRejectsWrongIdentityKeyDamageAndLimits(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	material := testKeyMaterial(3)
	packagePath := filepath.Join(root, "automatic.cpacb")
	if _, err := CreateWithKeyMaterial(context.Background(), source, packagePath, material, "test-v2"); err != nil {
		t.Fatal(err)
	}

	wrongKey := material
	wrongKey.WrappingKey[0] ^= 0xff
	wrongID := material
	wrongID.ProviderID = "bkp_other"
	wrongVersion := material
	wrongVersion.ProviderVersion++
	for name, candidate := range map[string]KeyMaterial{
		"key":     wrongKey,
		"id":      wrongID,
		"version": wrongVersion,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyWithKeyMaterial(context.Background(), packagePath, candidate); !errors.Is(err, errKeyMaterialAuthentication) {
				t.Fatalf("got %v", err)
			}
		})
	}

	original, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"ciphertext": func() []byte { value := bytes.Clone(original); value[len(value)-1] ^= 1; return value }(),
		"provider":   func() []byte { value := bytes.Clone(original); value[keyMaterialFixedHeader] ^= 1; return value }(),
		"kdf":        func() []byte { value := bytes.Clone(original); value[21] ^= 1; return value }(),
		"header-size": func() []byte {
			value := bytes.Clone(original)
			value[19] ^= 1
			return value
		}(),
		"ciphertext-size": func() []byte {
			value := bytes.Clone(original)
			value[43] ^= 1
			return value
		}(),
		"truncated": bytes.Clone(original[:len(original)-1]),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".cpacb")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyWithKeyMaterial(context.Background(), path, material); !errors.Is(err, errKeyMaterialAuthentication) {
				t.Fatalf("damage did not use the uniform authentication error: %v", err)
			}
		})
	}

	invalid := material
	invalid.ProviderVersion = maxPublicVersion + 1
	if _, err := CreateWithKeyMaterial(context.Background(), source, filepath.Join(root, "too-new.cpacb"), invalid, "test"); err == nil {
		t.Fatal("version above JSON safe integer was accepted")
	}
	zero := material
	zero.WrappingKey = [keySize]byte{}
	if _, err := CreateWithKeyMaterial(context.Background(), source, filepath.Join(root, "zero.cpacb"), zero, "test"); err == nil {
		t.Fatal("zero wrapping key was accepted")
	}
	invalidHeader := bytes.Clone(original)
	binary.BigEndian.PutUint64(invalidHeader[28:36], maxPublicVersion+1)
	invalidPath := filepath.Join(root, "invalid-version.cpacb")
	if err := os.WriteFile(invalidPath, invalidHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWithKeyMaterial(context.Background(), invalidPath, material); !errors.Is(err, errKeyMaterialAuthentication) {
		t.Fatalf("invalid header version did not use the uniform authentication error: %v", err)
	}
	if _, err := VerifyWithKeyMaterial(context.Background(), invalidPath, invalid); err == nil {
		t.Fatal("invalid header version was accepted")
	}
}

func TestPasswordAndKeyMaterialFormatsRemainSeparate(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	packagePath := filepath.Join(root, "password.cpacb")
	if _, err := Create(context.Background(), source, packagePath, testPassword, "test-v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWithKeyMaterial(context.Background(), packagePath, testKeyMaterial(1)); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("key material parser accepted v1 package: %v", err)
	}
	info, err := Verify(context.Background(), packagePath, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if info.FormatVersion != 1 || info.KeyProvider != nil {
		t.Fatalf("v1 metadata changed: %#v", info)
	}
}

func testKeyMaterial(version uint64) KeyMaterial {
	material := KeyMaterial{ProviderID: "bkp_synthetic", ProviderKind: windowsDPAPIUserKind, ProviderVersion: version}
	for index := range material.WrappingKey {
		material.WrappingKey[index] = byte(index + 1)
	}
	return material
}
