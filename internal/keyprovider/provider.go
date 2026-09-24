// Package keyprovider provides host-protected, versioned wrapping keys for
// unattended CPA Cloud backups. It never stores plaintext wrapping keys.
package keyprovider

import (
	"crypto/subtle"
	"errors"
	"unicode/utf8"
)

const (
	KindWindowsDPAPIUser = "windows-dpapi-user"
	ScopeCurrentUser     = "current-user"
	ReasonUnsupported    = "unsupported_platform"
	ReasonReady          = ""

	MaxPublicVersion = uint64(9007199254740991)
	maxProviderID    = 64
)

var ErrUnavailable = errors.New("backup key provider is unavailable")

// Material is plaintext key material owned by the caller. Destroy must be
// called as soon as the backup core has taken its own short-lived copy.
type Material struct {
	ProviderID string
	Kind       string
	Version    uint64
	Key        [32]byte
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
	if material == nil || !validProviderID(material.ProviderID) || material.Kind != KindWindowsDPAPIUser || !validVersion(material.Version) {
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
