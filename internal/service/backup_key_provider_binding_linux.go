//go:build linux

package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"cpacloud.local/server/internal/keyprovider"
)

func backupKeyProviderInstanceBinding(cfg Config) (keyprovider.InstanceBinding, func([]byte) error, error) {
	if normalizeMasterKeyProvider(cfg.MasterKeyProvider) != masterKeyProviderLinuxSystemdCredentialV1 {
		return keyprovider.InstanceBinding{}, nil, nil
	}
	encoded, err := readLinuxMasterKeyBinding(cfg.DataDir, masterKeyBindingFilename, masterKeyBindingMaxBytes)
	if err != nil {
		return keyprovider.InstanceBinding{}, nil, errors.New("backup key provider rejected installation binding")
	}
	defer clearBytes(encoded)
	var binding masterKeyBinding
	if decodeStrictJSON(encoded, &binding) != nil || binding.FormatVersion != 1 || binding.ProviderKind != masterKeyProviderLinuxSystemdCredentialV1 || !validMasterKeyInstanceID(binding.InstanceID) || !validMasterKeyID(binding.KeyID) {
		return keyprovider.InstanceBinding{}, nil, errors.New("backup key provider rejected installation binding")
	}
	material, err := loadLinuxSystemdMasterKeyCredential()
	if err != nil {
		return keyprovider.InstanceBinding{}, nil, errors.New("backup key provider rejected installation credential")
	}
	defer clearBytes(material.Key[:])
	if verifyMasterKeyBinding(encoded, &material) != nil {
		return keyprovider.InstanceBinding{}, nil, errors.New("backup key provider rejected installation credential")
	}
	// Retain only the digest of bytes authenticated during coordinator startup.
	// Every use verifies the exact bytes being packed/restored against a freshly
	// loaded installation credential, including immediately before publication.
	authenticatedDigest := sha256.Sum256(encoded)
	validate := func(candidate []byte) error {
		digest := sha256.Sum256(candidate)
		if subtle.ConstantTimeCompare(digest[:], authenticatedDigest[:]) != 1 {
			return errMasterKeyProviderRejected
		}
		current, err := loadLinuxSystemdMasterKeyCredential()
		if err != nil {
			return errMasterKeyProviderRejected
		}
		defer clearBytes(current.Key[:])
		return verifyMasterKeyBinding(candidate, &current)
	}
	return keyprovider.InstanceBinding{InstanceID: binding.InstanceID, ExcludedKeyID: binding.KeyID, ExcludedKeyDigest: sha256.Sum256(material.Key[:])}, validate, nil
}
