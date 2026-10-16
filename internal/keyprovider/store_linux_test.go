//go:build linux

package keyprovider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func TestLinuxSystemdStoreBindsInstanceKeyAndVersion(t *testing.T) {
	original := linuxReadBackupCredential
	t.Cleanup(func() { linuxReadBackupCredential = original })
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/cpa-cloud.service")
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	credential := linuxCredential{FormatVersion: 1, ProviderKind: KindLinuxSystemdCredentialV1, ProviderID: "bkp_linux", InstanceID: "01234567-89ab-cdef-0123-456789abcdef", KeyID: "backup-key", KeyVersion: 7, WrappingKey: base64.RawStdEncoding.EncodeToString(key)}
	linuxReadBackupCredential = func(directory, name string, limit int) ([]byte, error) {
		if directory != "/run/credentials/cpa-cloud.service" || name != linuxBackupCredentialName || limit != linuxBackupCredentialMaxBytes {
			t.Fatal("unexpected credential request")
		}
		return json.Marshal(credential)
	}
	installationDigest := sha256.Sum256([]byte("synthetic installation key material"))
	store, err := OpenForInstance("unused", InstanceBinding{InstanceID: credential.InstanceID, ExcludedKeyID: "installation-key", ExcludedKeyDigest: installationDigest})
	if err != nil {
		t.Fatal(err)
	}
	if ready, reason := store.Ready(); !ready || reason != "" {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	reference, ok := store.ConfiguredReference()
	if !ok || reference.ProviderID != credential.ProviderID || reference.InstanceID != credential.InstanceID || reference.KeyID != credential.KeyID || reference.Version != credential.KeyVersion {
		t.Fatalf("reference=%+v ok=%v", reference, ok)
	}
	material, err := store.Resolve(context.Background(), credential.ProviderID, credential.KeyVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Destroy()
	if material.Key[0] != 1 || material.Key[31] != 32 {
		t.Fatal("wrong wrapping key")
	}
}

func TestLinuxSystemdStoreFailsClosedForMissingWrongOrMixedCredential(t *testing.T) {
	original := linuxReadBackupCredential
	t.Cleanup(func() { linuxReadBackupCredential = original })
	t.Setenv("CREDENTIALS_DIRECTORY", "/run/credentials/cpa-cloud.service")
	installationKey := make([]byte, 32)
	for index := range installationKey {
		installationKey[index] = 0xa5
	}
	binding := InstanceBinding{InstanceID: "01234567-89ab-cdef-0123-456789abcdef", ExcludedKeyID: "installation-key", ExcludedKeyDigest: sha256.Sum256(installationKey)}
	validKey := make([]byte, 32)
	for index := range validKey {
		validKey[index] = byte(index + 1)
	}
	base := linuxCredential{FormatVersion: 1, ProviderKind: KindLinuxSystemdCredentialV1, ProviderID: "bkp_linux", InstanceID: binding.InstanceID, KeyID: "backup-key", KeyVersion: 1, WrappingKey: base64.RawStdEncoding.EncodeToString(validKey)}
	for _, test := range []struct {
		name    string
		mutate  func(*linuxCredential)
		readErr error
	}{
		{"missing", nil, errors.New("missing")},
		{"wrong-instance", func(v *linuxCredential) { v.InstanceID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" }, nil},
		{"master-key-reuse", func(v *linuxCredential) { v.KeyID = binding.ExcludedKeyID }, nil},
		{"same-key-bytes", func(v *linuxCredential) { v.WrappingKey = base64.RawStdEncoding.EncodeToString(installationKey) }, nil},
		{"zero-key", func(v *linuxCredential) { v.WrappingKey = base64.RawStdEncoding.EncodeToString(make([]byte, 32)) }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := base
			if test.mutate != nil {
				test.mutate(&value)
			}
			linuxReadBackupCredential = func(string, string, int) ([]byte, error) {
				if test.readErr != nil {
					return nil, test.readErr
				}
				return json.Marshal(value)
			}
			store, err := OpenForInstance("unused", binding)
			if err != nil {
				t.Fatal(err)
			}
			if ready, _ := store.Ready(); ready {
				t.Fatal("invalid credential became ready")
			}
			if _, err := store.Resolve(context.Background(), "bkp_linux", 1); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("resolve=%v", err)
			}
		})
	}
}
