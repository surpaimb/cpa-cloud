// Package keyprovider provides host-protected, versioned wrapping keys for
// unattended CPA Cloud backups. It never stores plaintext wrapping keys.
package keyprovider

import (
	"crypto/subtle"
	"errors"
	"unicode/utf8"
)

const (
	KindWindowsDPAPIUser         = "windows-dpapi-user"
	KindLinuxSystemdCredentialV1 = "linux-systemd-credential-v1"
	ScopeCurrentUser             = "current-user"
	ScopeSystemdService          = "systemd-service"
	ReasonUnsupported            = "unsupported_platform"
	ReasonReady                  = ""

	MaxPublicVersion = uint64(9007199254740991)
	maxProviderID    = 64
)

var ErrUnavailable = errors.New("backup key provider is unavailable")

// Material is plaintext key material owned by the caller. Destroy must be
// called as soon as the backup core has taken its own short-lived copy.
type Material struct {
	ProviderID string
	Kind       string
	InstanceID string
	KeyID      string
	Version    uint64
	Key        [32]byte
}

// Reference is the non-secret identity of one exact wrapping key.
type Reference struct {
	ProviderID string
	Kind       string
	InstanceID string
	KeyID      string
	Version    uint64
}

// InstanceBinding ties a headless backup credential to the already verified
// installation credential without sharing either secret.
type InstanceBinding struct {
	InstanceID        string
	ExcludedKeyID     string
	ExcludedKeyDigest [32]byte
}

func (m *Material) Destroy() {
	if m == nil {
		return
	}
	for index := range m.Key {
		m.Key[index] = 0
	}
}

func validProviderID(value string) bool {
	if value == "" || len(value) > maxProviderID || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func validVersion(version uint64) bool {
	return version >= 1 && version <= MaxPublicVersion
}

func validateMaterial(material *Material) error {
	if material == nil || !validProviderID(material.ProviderID) || !validVersion(material.Version) {
		return errors.New("backup key material reference is invalid")
	}
	switch material.Kind {
	case KindWindowsDPAPIUser:
		if material.InstanceID != "" || material.KeyID != "" {
			return errors.New("backup key material reference is invalid")
		}
	case KindLinuxSystemdCredentialV1:
		if !validInstanceID(material.InstanceID) || !validKeyID(material.KeyID) {
			return errors.New("backup key material reference is invalid")
		}
	default:
		return errors.New("backup key material reference is invalid")
	}
	var nonzero byte
	for _, value := range material.Key {
		nonzero |= value
	}
	if subtle.ConstantTimeByteEq(nonzero, 0) == 1 {
		return errors.New("backup key material is invalid")
	}
	return nil
}

func validInstanceID(value string) bool {
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

func validKeyID(value string) bool {
	if value == "" || len(value) > 64 {
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

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
