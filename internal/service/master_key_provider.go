package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	masterKeyProviderPreviewFileV1                   = "preview-file-v1"
	masterKeyProviderLinuxSystemdCredentialV1        = "linux-systemd-credential-v1"
	masterKeyCredentialName                          = "cpa-cloud-master-key"
	masterKeyBindingFilename                         = "master-key-binding.json"
	masterKeyCredentialMaxBytes                      = 4096
	masterKeyBindingMaxBytes                         = 4096
	maxJSONSafeInteger                        uint64 = 9007199254740991
)

var errMasterKeyProviderRejected = errors.New("master key provider rejected")

type masterKeyMaterial struct {
	ProviderKind string
	InstanceID   string
	KeyID        string
	KeyVersion   uint64
	Key          [32]byte
}

type systemdMasterKeyCredential struct {
	FormatVersion uint64 `json:"format_version"`
	ProviderKind  string `json:"provider_kind"`
	InstanceID    string `json:"instance_id"`
	KeyID         string `json:"key_id"`
	KeyVersion    uint64 `json:"key_version"`
	MasterKey     string `json:"master_key"`
}

type masterKeyBinding struct {
	FormatVersion    uint64 `json:"format_version"`
	ProviderKind     string `json:"provider_kind"`
	InstanceID       string `json:"instance_id"`
	KeyID            string `json:"key_id"`
	KeyVersion       uint64 `json:"key_version"`
	CanaryNonce      string `json:"canary_nonce"`
	CanaryCiphertext string `json:"canary_ciphertext"`
}

func normalizeMasterKeyProvider(kind string) string {
	if kind == "" {
		return masterKeyProviderPreviewFileV1
	}
	return kind
}

func validateMasterKeyProviderKind(kind string) error {
	switch normalizeMasterKeyProvider(kind) {
	case masterKeyProviderPreviewFileV1:
		return nil
	case masterKeyProviderLinuxSystemdCredentialV1:
		if !linuxSystemdMasterKeyProviderSupported() {
			return errors.New("linux systemd master key provider is unavailable on this platform")
		}
		return nil
	default:
		return errors.New("unsupported master key provider")
	}
}

func loadSecretsForConfig(cfg Config) (*secrets, error) {
	switch normalizeMasterKeyProvider(cfg.MasterKeyProvider) {
	case masterKeyProviderPreviewFileV1:
		return loadSecrets(cfg.DataDir)
	case masterKeyProviderLinuxSystemdCredentialV1:
		material, err := loadLinuxSystemdMasterKeyCredential()
		if err != nil {
			return nil, err
		}
		defer clearBytes(material.Key[:])
		if err := rejectLegacyRootKey(cfg.DataDir); err != nil {
			return nil, err
		}
		bindingBytes, err := readLinuxMasterKeyBinding(cfg.DataDir, masterKeyBindingFilename, masterKeyBindingMaxBytes)
		if err != nil {
			return nil, errMasterKeyProviderRejected
		}
		defer clearBytes(bindingBytes)
		if err := verifyMasterKeyBinding(bindingBytes, &material); err != nil {
			return nil, errMasterKeyProviderRejected
		}
		return newSecrets(material.Key[:])
	default:
		return nil, errors.New("unsupported master key provider")
	}
}

func initializeMasterKeyProvider(cfg Config) error {
	switch normalizeMasterKeyProvider(cfg.MasterKeyProvider) {
	case masterKeyProviderPreviewFileV1:
		rootPath := filepath.Join(cfg.DataDir, rootKeyFilename)
		if _, err := os.Stat(rootPath); errors.Is(err, os.ErrNotExist) {
			return createRootKey(cfg.DataDir)
		} else if err != nil {
			return fmt.Errorf("inspect root key: %w", err)
		}
		return nil
	case masterKeyProviderLinuxSystemdCredentialV1:
		material, err := loadLinuxSystemdMasterKeyCredential()
		if err != nil {
			return err
		}
		defer clearBytes(material.Key[:])
		if err := rejectLegacyRootKey(cfg.DataDir); err != nil {
			return err
		}
		bindingPath := filepath.Join(cfg.DataDir, masterKeyBindingFilename)
		if _, err := os.Lstat(bindingPath); err == nil {
			bindingBytes, readErr := readLinuxMasterKeyBinding(cfg.DataDir, masterKeyBindingFilename, masterKeyBindingMaxBytes)
			defer clearBytes(bindingBytes)
			if readErr != nil || verifyMasterKeyBinding(bindingBytes, &material) != nil {
				return errMasterKeyProviderRejected
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return errMasterKeyProviderRejected
		}
		bindingBytes, err := createMasterKeyBinding(&material)
		if err != nil {
			return errMasterKeyProviderRejected
		}
		defer clearBytes(bindingBytes)
		if err := createLinuxMasterKeyBinding(cfg.DataDir, masterKeyBindingFilename, bindingBytes); err != nil {
			return errMasterKeyProviderRejected
		}
		return nil
	default:
		return errors.New("unsupported master key provider")
	}
}

func rejectLegacyRootKey(dataDir string) error {
	_, err := os.Lstat(filepath.Join(dataDir, rootKeyFilename))
	if err == nil {
		return errMasterKeyProviderRejected
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errMasterKeyProviderRejected
	}
	return nil
}

func parseSystemdMasterKeyCredential(encoded []byte) (masterKeyMaterial, error) {
	var credential systemdMasterKeyCredential
	if err := decodeStrictJSON(encoded, &credential); err != nil {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	if credential.FormatVersion != 1 || credential.ProviderKind != masterKeyProviderLinuxSystemdCredentialV1 ||
		!validMasterKeyInstanceID(credential.InstanceID) || !validMasterKeyID(credential.KeyID) ||
		credential.KeyVersion == 0 || credential.KeyVersion > maxJSONSafeInteger {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	key, err := base64.RawStdEncoding.DecodeString(credential.MasterKey)
	if err != nil || len(key) != 32 {
		clearBytes(key)
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	material := masterKeyMaterial{
		ProviderKind: credential.ProviderKind,
		InstanceID:   strings.ToLower(credential.InstanceID),
		KeyID:        credential.KeyID,
		KeyVersion:   credential.KeyVersion,
	}
	copy(material.Key[:], key)
	clearBytes(key)
	return material, nil
}

func validMasterKeyID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validMasterKeyInstanceID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func createMasterKeyBinding(material *masterKeyMaterial) ([]byte, error) {
	block, err := aes.NewCipher(material.Key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, []byte("cpa-cloud-master-key-canary-v1"), masterKeyCanaryAAD(material))
	binding := masterKeyBinding{
		FormatVersion: 1, ProviderKind: material.ProviderKind, InstanceID: material.InstanceID,
		KeyID: material.KeyID, KeyVersion: material.KeyVersion,
		CanaryNonce: base64.RawStdEncoding.EncodeToString(nonce), CanaryCiphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	}
	return json.Marshal(binding)
}

func verifyMasterKeyBinding(encoded []byte, material *masterKeyMaterial) error {
	var binding masterKeyBinding
	if err := decodeStrictJSON(encoded, &binding); err != nil {
		return errMasterKeyProviderRejected
	}
	if binding.FormatVersion != 1 || binding.ProviderKind != material.ProviderKind ||
		binding.InstanceID != material.InstanceID || binding.KeyID != material.KeyID || binding.KeyVersion != material.KeyVersion {
		return errMasterKeyProviderRejected
	}
	nonce, nonceErr := base64.RawStdEncoding.DecodeString(binding.CanaryNonce)
	ciphertext, ciphertextErr := base64.RawStdEncoding.DecodeString(binding.CanaryCiphertext)
	defer clearBytes(nonce)
	defer clearBytes(ciphertext)
	block, err := aes.NewCipher(material.Key[:])
	if err != nil {
		return errMasterKeyProviderRejected
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || nonceErr != nil || ciphertextErr != nil || len(nonce) != aead.NonceSize() || len(ciphertext) < aead.Overhead() {
		return errMasterKeyProviderRejected
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, masterKeyCanaryAAD(material))
	if err != nil {
		return errMasterKeyProviderRejected
	}
	defer clearBytes(plaintext)
	if subtle.ConstantTimeCompare(plaintext, []byte("cpa-cloud-master-key-canary-v1")) != 1 {
		return errMasterKeyProviderRejected
	}
	return nil
}

func masterKeyCanaryAAD(material *masterKeyMaterial) []byte {
	return []byte("cpa-cloud/master-key-canary/v1\x00" + material.ProviderKind + "\x00" + material.InstanceID + "\x00" + material.KeyID + "\x00" + strconv.FormatUint(material.KeyVersion, 10))
}

func decodeStrictJSON(encoded []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}
