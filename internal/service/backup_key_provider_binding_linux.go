//go:build linux

package service

import (
	"crypto/sha256"
	"errors"

	"cpacloud.local/server/internal/keyprovider"
)

func backupKeyProviderInstanceBinding(cfg Config) (keyprovider.InstanceBinding, error) {
	if normalizeMasterKeyProvider(cfg.MasterKeyProvider) != masterKeyProviderLinuxSystemdCredentialV1 {
		return keyprovider.InstanceBinding{}, nil
	}
	encoded, err := readLinuxMasterKeyBinding(cfg.DataDir, masterKeyBindingFilename, masterKeyBindingMaxBytes)
	if err != nil {
		return keyprovider.InstanceBinding{}, errors.New("backup key provider rejected installation binding")
	}
	defer clearBytes(encoded)
	var binding masterKeyBinding
	if decodeStrictJSON(encoded, &binding) != nil || binding.FormatVersion != 1 || binding.ProviderKind != masterKeyProviderLinuxSystemdCredentialV1 || !validMasterKeyInstanceID(binding.InstanceID) || !validMasterKeyID(binding.KeyID) {
		return keyprovider.InstanceBinding{}, errors.New("backup key provider rejected installation binding")
	}
	material, err := loadLinuxSystemdMasterKeyCredential()
	if err != nil {
		return keyprovider.InstanceBinding{}, errors.New("backup key provider rejected installation credential")
	}
	defer clearBytes(material.Key[:])
	if verifyMasterKeyBinding(encoded, &material) != nil {
		return keyprovider.InstanceBinding{}, errors.New("backup key provider rejected installation credential")
	}
	return keyprovider.InstanceBinding{InstanceID: binding.InstanceID, ExcludedKeyID: binding.KeyID, ExcludedKeyDigest: sha256.Sum256(material.Key[:])}, nil
}
