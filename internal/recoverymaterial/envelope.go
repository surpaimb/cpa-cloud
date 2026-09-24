// Package recoverymaterial implements the independently designed portable
// key-recovery envelope from docs/adr/0004-portable-backup-recovery-material.md.
package recoverymaterial

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	FormatVersion = uint16(1)
	PurposeID     = uint16(1)

	kdfScrypt        = uint16(1)
	aeadAES256GCM    = uint16(1)
	fixedHeaderSize  = 70
	saltSize         = 16
	nonceSize        = 12
	keySize          = 32
	tagSize          = 16
	maxPasswordSize  = 1024
	maxProviderID    = 64
	maxProviderKind  = 48
	maxVersions      = 512
	maxEnvelopeSize  = 128 << 10
	maxPublicVersion = uint64(9007199254740991)

	scryptN = 32768
	scryptR = 8
	scryptP = 1

	windowsDPAPIUserKind = "windows-dpapi-user"
)

var (
	envelopeMagic = [16]byte{'C', 'P', 'A', 'C', 'L', 'O', 'U', 'D', '-', 'K', 'E', 'Y', 'R', 'E', 'C', 0}
	payloadMagic  = [16]byte{'C', 'P', 'A', 'C', 'L', 'O', 'U', 'D', '-', 'K', 'E', 'Y', 'S', 'E', 'T', 0}

	ErrAuthentication = errors.New("recovery material authentication failed")
)

type Reference struct {
	EnvelopeVersion uint16
	ProviderID      string
	ProviderKind    string
	Versions        []uint64
}

type Entry struct {
	Version uint64
	Key     [keySize]byte
}

// Bundle contains short-lived plaintext recovery keys. Destroy must be called
// as soon as the caller has completed re-protection or backup restoration.
type Bundle struct {
	Reference       Reference
	InstanceBinding [32]byte
	Entries         []Entry
}

func (b *Bundle) Destroy() {
	if b == nil {
		return
	}
	for index := range b.InstanceBinding {
		b.InstanceBinding[index] = 0
	}
	for index := range b.Entries {
		wipe(b.Entries[index].Key[:])
	}
}

func Seal(password []byte, bundle *Bundle) ([]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	salt := make([]byte, saltSize)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, errors.New("generate recovery material salt")
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("generate recovery material nonce")
	}
	plaintext := encodePayload(bundle)
	defer wipe(plaintext)
	header := encodeHeader(bundle.Reference, len(plaintext)+tagSize, salt, nonce)
	derived, err := derivePasswordKey(password, salt)
	if err != nil {
		return nil, err
	}
	defer wipe(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, errors.New("initialize recovery material encryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize recovery material authentication")
	}
	sealed := aead.Seal(nil, nonce, plaintext, header)
	result := append(header, sealed...)
	if len(result) > maxEnvelopeSize {
		wipe(result)
		return nil, errors.New("recovery material exceeds the supported size limit")
	}
	return result, nil
}

func Open(password, encoded []byte) (*Bundle, error) {
	return openWithDeriver(password, encoded, derivePasswordKey)
}

type passwordDeriver func([]byte, []byte) ([]byte, error)

func openWithDeriver(password, encoded []byte, derive passwordDeriver) (*Bundle, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	reference, headerSize, ciphertextLength, err := parseHeader(encoded)
	if err != nil {
		return nil, err
	}
	header := encoded[:headerSize]
	derived, err := derive(password, header[42:58])
	if err != nil {
		return nil, err
	}
	defer wipe(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, errors.New("initialize recovery material decryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize recovery material authentication")
	}
	plaintext, err := aead.Open(nil, header[58:70], encoded[headerSize:headerSize+ciphertextLength], header)
	if err != nil {
		return nil, ErrAuthentication
	}
	defer wipe(plaintext)
	bundle, err := decodePayload(reference, plaintext)
	if err != nil {
		return nil, ErrAuthentication
	}
	return bundle, nil
}

func encodeHeader(reference Reference, ciphertextLength int, salt, nonce []byte) []byte {
	headerSize := fixedHeaderSize + len(reference.ProviderID) + len(reference.ProviderKind) + len(reference.Versions)*8
	header := make([]byte, headerSize)
	copy(header[:16], envelopeMagic[:])
	binary.BigEndian.PutUint16(header[16:18], FormatVersion)
	binary.BigEndian.PutUint16(header[18:20], uint16(headerSize))
	binary.BigEndian.PutUint16(header[20:22], PurposeID)
	binary.BigEndian.PutUint16(header[22:24], kdfScrypt)
	binary.BigEndian.PutUint16(header[24:26], aeadAES256GCM)
	binary.BigEndian.PutUint16(header[26:28], uint16(len(reference.ProviderID)))
	binary.BigEndian.PutUint16(header[28:30], uint16(len(reference.ProviderKind)))
	binary.BigEndian.PutUint16(header[30:32], uint16(len(reference.Versions)))
	binary.BigEndian.PutUint16(header[32:34], 0)
	binary.BigEndian.PutUint64(header[34:42], uint64(ciphertextLength))
	copy(header[42:58], salt)
	copy(header[58:70], nonce)
	offset := fixedHeaderSize
	copy(header[offset:], reference.ProviderID)
	offset += len(reference.ProviderID)
	copy(header[offset:], reference.ProviderKind)
	offset += len(reference.ProviderKind)
	for _, version := range reference.Versions {
		binary.BigEndian.PutUint64(header[offset:offset+8], version)
		offset += 8
	}
	return header
}

func parseHeader(encoded []byte) (Reference, int, int, error) {
	if len(encoded) < fixedHeaderSize+tagSize || len(encoded) > maxEnvelopeSize {
		return Reference{}, 0, 0, ErrAuthentication
	}
	fixed := encoded[:fixedHeaderSize]
	if !bytes.Equal(fixed[:16], envelopeMagic[:]) || binary.BigEndian.Uint16(fixed[16:18]) != FormatVersion || binary.BigEndian.Uint16(fixed[20:22]) != PurposeID || binary.BigEndian.Uint16(fixed[22:24]) != kdfScrypt || binary.BigEndian.Uint16(fixed[24:26]) != aeadAES256GCM || binary.BigEndian.Uint16(fixed[32:34]) != 0 {
		return Reference{}, 0, 0, ErrAuthentication
	}
	idLength := int(binary.BigEndian.Uint16(fixed[26:28]))
	kindLength := int(binary.BigEndian.Uint16(fixed[28:30]))
	count := int(binary.BigEndian.Uint16(fixed[30:32]))
	headerSize := int(binary.BigEndian.Uint16(fixed[18:20]))
	expectedHeader := fixedHeaderSize + idLength + kindLength + count*8
	if idLength < 1 || idLength > maxProviderID || kindLength < 1 || kindLength > maxProviderKind || count < 1 || count > maxVersions || headerSize != expectedHeader || headerSize > len(encoded)-tagSize {
		return Reference{}, 0, 0, ErrAuthentication
	}
	ciphertextLength64 := binary.BigEndian.Uint64(fixed[34:42])
	if ciphertextLength64 < tagSize || ciphertextLength64 > maxEnvelopeSize || ciphertextLength64 != uint64(len(encoded)-headerSize) {
		return Reference{}, 0, 0, ErrAuthentication
	}
	offset := fixedHeaderSize
	providerID := string(encoded[offset : offset+idLength])
	offset += idLength
	providerKind := string(encoded[offset : offset+kindLength])
	offset += kindLength
	versions := make([]uint64, count)
	for index := range versions {
		versions[index] = binary.BigEndian.Uint64(encoded[offset : offset+8])
		offset += 8
	}
	reference := Reference{EnvelopeVersion: FormatVersion, ProviderID: providerID, ProviderKind: providerKind, Versions: versions}
	if err := validateReference(reference); err != nil {
		return Reference{}, 0, 0, ErrAuthentication
	}
	return reference, headerSize, int(ciphertextLength64), nil
}

func encodePayload(bundle *Bundle) []byte {
	plaintext := make([]byte, 52+len(bundle.Entries)*keySize)
	copy(plaintext[:16], payloadMagic[:])
	binary.BigEndian.PutUint16(plaintext[16:18], uint16(len(bundle.Entries)))
	binary.BigEndian.PutUint16(plaintext[18:20], 0)
	copy(plaintext[20:52], bundle.InstanceBinding[:])
	offset := 52
	for index := range bundle.Entries {
		copy(plaintext[offset:offset+keySize], bundle.Entries[index].Key[:])
		offset += keySize
	}
	return plaintext
}

func decodePayload(reference Reference, plaintext []byte) (*Bundle, error) {
	if len(plaintext) != 52+len(reference.Versions)*keySize || !bytes.Equal(plaintext[:16], payloadMagic[:]) || binary.BigEndian.Uint16(plaintext[16:18]) != uint16(len(reference.Versions)) || binary.BigEndian.Uint16(plaintext[18:20]) != 0 {
		return nil, ErrAuthentication
	}
	bundle := &Bundle{Reference: reference, Entries: make([]Entry, len(reference.Versions))}
	copy(bundle.InstanceBinding[:], plaintext[20:52])
	offset := 52
	for index, version := range reference.Versions {
		bundle.Entries[index].Version = version
		copy(bundle.Entries[index].Key[:], plaintext[offset:offset+keySize])
		offset += keySize
	}
	if err := validateBundle(bundle); err != nil {
		bundle.Destroy()
		return nil, ErrAuthentication
	}
	return bundle, nil
}

func validateBundle(bundle *Bundle) error {
	if bundle == nil {
		return errors.New("recovery material is required")
	}
	if err := validateReference(bundle.Reference); err != nil {
		return err
	}
	if len(bundle.Entries) != len(bundle.Reference.Versions) {
		return errors.New("recovery material entries do not match versions")
	}
	var bindingNonzero byte
	for _, value := range bundle.InstanceBinding {
		bindingNonzero |= value
	}
	if subtle.ConstantTimeByteEq(bindingNonzero, 0) == 1 {
		return errors.New("recovery material instance binding is invalid")
	}
	for index := range bundle.Entries {
		if bundle.Entries[index].Version != bundle.Reference.Versions[index] {
			return errors.New("recovery material entry version mismatch")
		}
		var nonzero byte
		for _, value := range bundle.Entries[index].Key {
			nonzero |= value
		}
		if subtle.ConstantTimeByteEq(nonzero, 0) == 1 {
			return errors.New("recovery material contains an invalid key")
		}
	}
	return nil
}

func validateReference(reference Reference) error {
	if reference.EnvelopeVersion != FormatVersion {
		return errors.New("recovery material version is unsupported")
	}
	if !validProviderID(reference.ProviderID) || reference.ProviderKind != windowsDPAPIUserKind {
		return errors.New("recovery material provider is invalid")
	}
	if len(reference.Versions) < 1 || len(reference.Versions) > maxVersions {
		return errors.New("recovery material version count is invalid")
	}
	var previous uint64
	for _, version := range reference.Versions {
		if version == 0 || version > maxPublicVersion || version <= previous {
			return errors.New("recovery material versions must be unique and strictly increasing")
		}
		previous = version
	}
	return nil
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

func validatePassword(password []byte) error {
	if len(password) < 1 || len(password) > maxPasswordSize {
		return fmt.Errorf("recovery password must be between 1 and %d bytes", maxPasswordSize)
	}
	return nil
}

func derivePasswordKey(password, salt []byte) ([]byte, error) {
	key, err := scrypt.Key(password, salt, scryptN, scryptR, scryptP, keySize)
	if err != nil {
		return nil, errors.New("derive recovery material key")
	}
	return key, nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
