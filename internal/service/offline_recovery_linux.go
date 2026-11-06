//go:build linux

package service

import (
	"context"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
)

// ReadSystemdOfflineRecoveryMaterial reuses the production readers and canary
// validator without initializing a seed, opening SQLite, or starting an App.
// The caller must admit/pin the read-only seed and destroy the returned key.
// The second result is a fixed stage, never an underlying error or input value.
func ReadSystemdOfflineRecoveryMaterial(ctx context.Context, seed string, expected SystemdRecoveryIdentity) (backup.KeyMaterial, string) {
	encoded, err := readLinuxMasterKeyBinding(seed, masterKeyBindingFilename, masterKeyBindingMaxBytes)
	if err != nil {
		return backup.KeyMaterial{}, "seed"
	}
	defer clearBytes(encoded)
	var installed masterKeyBinding
	if decodeStrictJSON(encoded, &installed) != nil || installed.FormatVersion != 1 || installed.ProviderKind != masterKeyProviderLinuxSystemdCredentialV1 {
		return backup.KeyMaterial{}, "seed"
	}
	if installed.InstanceID != expected.InstanceID || installed.KeyID != expected.MasterID || installed.KeyVersion != expected.MasterVersion {
		return backup.KeyMaterial{}, "identity"
	}
	binding, validate, err := backupKeyProviderInstanceBinding(Config{DataDir: seed, MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1})
	if err != nil || validate == nil || validate(encoded) != nil {
		return backup.KeyMaterial{}, "installation"
	}
	store, err := keyprovider.OpenForInstance("", binding)
	if err != nil {
		return backup.KeyMaterial{}, "wrapping"
	}
	ref, ok := store.ConfiguredReference()
	if !ok {
		return backup.KeyMaterial{}, "wrapping"
	}
	if ref.Kind != keyprovider.KindLinuxSystemdCredentialV1 || ref.ProviderID != expected.ProviderID || ref.InstanceID != expected.InstanceID || ref.KeyID != expected.WrappingID || ref.Version != expected.WrappingVersion {
		return backup.KeyMaterial{}, "identity"
	}
	key, err := store.Resolve(ctx, ref.ProviderID, ref.Version)
	if err != nil {
		return backup.KeyMaterial{}, "wrapping"
	}
	defer key.Destroy()
	return backup.KeyMaterial{ProviderID: ref.ProviderID, ProviderKind: ref.Kind, InstanceID: ref.InstanceID,
		KeyID: ref.KeyID, ProviderVersion: ref.Version, WrappingKey: key.Key, ValidateInstallationBinding: validate}, ""
}
