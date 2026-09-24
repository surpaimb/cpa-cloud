package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"golang.org/x/crypto/hkdf"
)

const (
	keyMaterialFormatVersion = uint16(2)
	keyMaterialKDFHKDFSHA256 = uint16(1)
	keyMaterialFixedHeader   = 72
	maxProviderIDBytes       = 64
	maxProviderKindBytes     = 48
	maxPublicVersion         = uint64(9007199254740991)

	windowsDPAPIUserKind = "windows-dpapi-user"
)

var keyMaterialInfoPrefix = []byte("cpa-cloud/backup/v2\x00")

var errKeyMaterialAuthentication = errors.New("backup authentication failed (wrong key material or damaged package)")

// KeyMaterial is a short-lived wrapping key and its immutable provider
// identity. Callers must not persist or log this value.
type KeyMaterial struct {
	ProviderID      string
	ProviderKind    string
	ProviderVersion uint64
	WrappingKey     [keySize]byte
}

// CreateWithKeyMaterial writes a version 2 backup package. output must not
// already exist. The password-based version 1 format remains a separate API.
func CreateWithKeyMaterial(ctx context.Context, dataDir, output string, material KeyMaterial, sourceVersion string) (Info, error) {
	defer wipe(material.WrappingKey[:])
	if err := validateKeyMaterial(&material); err != nil {
		return Info{}, err
	}
	source, err := validateSourceDirectory(dataDir)
	if err != nil {
		return Info{}, err
	}
	output, err = cleanExplicitFilePath(output)
	if err != nil {
		return Info{}, fmt.Errorf("output path: %w", err)
	}
	if _, err := os.Lstat(output); err == nil {
		return Info{}, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, fmt.Errorf("inspect output file: %w", err)
	}
	parent := filepath.Dir(output)
	if err := validateExistingDirectoryChain(parent); err != nil {
		return Info{}, fmt.Errorf("output directory: %w", err)
	}

	rootBefore, err := readRootKey(filepath.Join(source, rootKeyFilename))
	if err != nil {
		return Info{}, err
	}
	defer wipe(rootBefore)
	stage, err := makeSecureTempDir(parent, ".cpa-cloud-backup-create-")
	if err != nil {
		return Info{}, fmt.Errorf("create restricted temporary directory: %w", err)
	}
	defer removeKnownDirectory(stage, databaseFilename)
	snapshot := filepath.Join(stage, databaseFilename)
	if err := onlineSnapshot(ctx, filepath.Join(source, databaseFilename), snapshot); err != nil {
		return Info{}, fmt.Errorf("create SQLite snapshot: %w", err)
	}
	if err := verifySQLite(ctx, snapshot); err != nil {
		return Info{}, fmt.Errorf("verify SQLite snapshot: %w", err)
	}
	rootAfter, err := readRootKey(filepath.Join(source, rootKeyFilename))
	if err != nil {
		return Info{}, err
	}
	defer wipe(rootAfter)
	if !bytes.Equal(rootBefore, rootAfter) {
		return Info{}, errors.New("root key changed while the backup was being created")
	}
	dbBytes, err := readRegularFile(snapshot, maxDatabaseBytes)
	if err != nil {
		return Info{}, fmt.Errorf("read SQLite snapshot: %w", err)
	}
	defer wipe(dbBytes)
	records := []record{
		{name: databaseFilename, data: dbBytes, sum: sha256.Sum256(dbBytes)},
		{name: rootKeyFilename, data: rootBefore, sum: sha256.Sum256(rootBefore)},
	}
	reference := material.reference()
	info := newInfoForFormat(int(keyMaterialFormatVersion), sourceVersion, records, &reference)
	plaintext, err := encodePayload(info, records)
	if err != nil {
		return Info{}, err
	}
	defer wipe(plaintext)
	packageBytes, err := sealKeyMaterialPackage(&material, plaintext)
	if err != nil {
		return Info{}, err
	}
	defer wipe(packageBytes)
	if int64(len(packageBytes)) > maxPackageBytes {
		return Info{}, errors.New("backup package exceeds the supported size limit")
	}
	if err := writeNewSyncedFile(output, packageBytes); err != nil {
		return Info{}, fmt.Errorf("write backup package: %w", err)
	}
	return info, nil
}

// VerifyWithKeyMaterial authenticates and validates a version 2 package.
func VerifyWithKeyMaterial(ctx context.Context, input string, material KeyMaterial) (Info, error) {
	defer wipe(material.WrappingKey[:])
	decoded, cleanup, err := openAndValidateKeyMaterialIn(ctx, input, &material, os.TempDir(), "verify")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Info{}, err
	}
	defer decoded.destroy()
	return decoded.info, nil
}

// RestoreWithKeyMaterial authenticates a version 2 package and restores it
// into a new directory using the same safety state transitions as Restore.
func RestoreWithKeyMaterial(ctx context.Context, input, dataDir string, material KeyMaterial) (Info, error) {
	defer wipe(material.WrappingKey[:])
	target, parent, err := validateNewRestoreTarget(dataDir)
	if err != nil {
		return Info{}, err
	}
	parentIdentity, err := os.Lstat(parent)
	if err != nil {
		return Info{}, fmt.Errorf("inspect restore parent identity: %w", err)
	}
	decoded, cleanup, err := openAndValidateKeyMaterialIn(ctx, input, &material, parent, "restore")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Info{}, err
	}
	defer decoded.destroy()

	stage, err := makeSecureTempDir(parent, ".cpa-cloud-restore-stage-")
	if err != nil {
		return Info{}, fmt.Errorf("create restore staging directory: %w", err)
	}
	defer removeKnownDirectory(stage, databaseFilename, rootKeyFilename)
	dbPath := filepath.Join(stage, databaseFilename)
	keyPath := filepath.Join(stage, rootKeyFilename)
	if err := writeExclusiveFile(dbPath, decoded.db, 0o600); err != nil {
		return Info{}, fmt.Errorf("stage database: %w", err)
	}
	if err := writeExclusiveFile(keyPath, decoded.rootKey, 0o600); err != nil {
		return Info{}, fmt.Errorf("stage root key: %w", err)
	}
	if err := sanitizeRestoredDatabase(ctx, dbPath); err != nil {
		return Info{}, fmt.Errorf("prepare restored database: %w", err)
	}
	if err := verifySQLite(ctx, dbPath); err != nil {
		return Info{}, fmt.Errorf("verify prepared database: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	if err := validateExistingDirectoryChain(parent); err != nil {
		return Info{}, fmt.Errorf("restore parent changed during validation: %w", err)
	}
	currentParent, err := os.Lstat(parent)
	if err != nil || !os.SameFile(parentIdentity, currentParent) {
		return Info{}, errors.New("restore parent changed during validation")
	}
	if _, err := os.Lstat(target); err == nil {
		return Info{}, errors.New("restore target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, fmt.Errorf("recheck restore target: %w", err)
	}
	if err := publishRestoreDirectory(stage, target); err != nil {
		return Info{}, fmt.Errorf("publish restored data directory: %w", err)
	}
	stage = ""
	return decoded.info, nil
}

func openAndValidateKeyMaterialIn(ctx context.Context, input string, material *KeyMaterial, tempParent, purpose string) (*decodedPackage, func(), error) {
	if err := validateKeyMaterial(material); err != nil {
		return nil, nil, err
	}
	input, err := cleanExplicitFilePath(input)
	if err != nil {
		return nil, nil, fmt.Errorf("input path: %w", err)
	}
	packageBytes, err := readRegularFile(input, maxPackageBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read backup package: %w", err)
	}
	defer wipe(packageBytes)
	plaintext, err := openKeyMaterialPackage(material, packageBytes)
	if err != nil {
		return nil, nil, err
	}
	defer wipe(plaintext)
	reference := material.reference()
	decoded, err := decodePayloadForFormat(plaintext, int(keyMaterialFormatVersion), &reference)
	if err != nil {
		return nil, nil, err
	}
	stage, err := makeSecureTempDir(tempParent, ".cpa-cloud-backup-"+purpose+"-")
	if err != nil {
		decoded.destroy()
		return nil, nil, fmt.Errorf("create restricted temporary directory: %w", err)
	}
	cleanup := func() { removeKnownDirectory(stage, databaseFilename) }
	tempDB := filepath.Join(stage, databaseFilename)
	if err := writeExclusiveFile(tempDB, decoded.db, 0o600); err != nil {
		cleanup()
		decoded.destroy()
		return nil, nil, fmt.Errorf("stage database for verification: %w", err)
	}
	if err := verifySQLite(ctx, tempDB); err != nil {
		cleanup()
		decoded.destroy()
		return nil, nil, fmt.Errorf("backup database failed integrity validation: %w", err)
	}
	return decoded, cleanup, nil
}

func sealKeyMaterialPackage(material *KeyMaterial, plaintext []byte) ([]byte, error) {
	salt := make([]byte, saltSize)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate backup salt: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate backup nonce: %w", err)
	}
	headerSize := keyMaterialFixedHeader + len(material.ProviderID) + len(material.ProviderKind)
	header := make([]byte, headerSize)
	copy(header[:16], packageMagic[:])
	binary.BigEndian.PutUint16(header[16:18], keyMaterialFormatVersion)
	binary.BigEndian.PutUint16(header[18:20], uint16(headerSize))
	binary.BigEndian.PutUint16(header[20:22], keyMaterialKDFHKDFSHA256)
	binary.BigEndian.PutUint16(header[22:24], uint16(len(material.ProviderID)))
	binary.BigEndian.PutUint16(header[24:26], uint16(len(material.ProviderKind)))
	binary.BigEndian.PutUint16(header[26:28], 0)
	binary.BigEndian.PutUint64(header[28:36], material.ProviderVersion)
	binary.BigEndian.PutUint64(header[36:44], uint64(len(plaintext)+tagSize))
	copy(header[44:60], salt)
	copy(header[60:72], nonce)
	copy(header[72:72+len(material.ProviderID)], material.ProviderID)
	copy(header[72+len(material.ProviderID):], material.ProviderKind)
	key, err := deriveKeyMaterialKey(material, salt)
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize backup encryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize backup authentication")
	}
	sealed := aead.Seal(nil, nonce, plaintext, header)
	return append(header, sealed...), nil
}

func openKeyMaterialPackage(material *KeyMaterial, packageBytes []byte) ([]byte, error) {
	if len(packageBytes) < 18 {
		return nil, errors.New("backup package is truncated")
	}
	if !bytes.Equal(packageBytes[:16], packageMagic[:]) {
		return nil, errors.New("unknown backup package format")
	}
	if binary.BigEndian.Uint16(packageBytes[16:18]) != keyMaterialFormatVersion {
		return nil, errors.New("unsupported backup package version")
	}
	if len(packageBytes) < keyMaterialFixedHeader+tagSize {
		return nil, errKeyMaterialAuthentication
	}
	fixed := packageBytes[:keyMaterialFixedHeader]
	if binary.BigEndian.Uint16(fixed[20:22]) != keyMaterialKDFHKDFSHA256 {
		return nil, errKeyMaterialAuthentication
	}
	headerSize := int(binary.BigEndian.Uint16(fixed[18:20]))
	idLength := int(binary.BigEndian.Uint16(fixed[22:24]))
	kindLength := int(binary.BigEndian.Uint16(fixed[24:26]))
	if headerSize != keyMaterialFixedHeader+idLength+kindLength || idLength < 1 || idLength > maxProviderIDBytes || kindLength < 1 || kindLength > maxProviderKindBytes || binary.BigEndian.Uint16(fixed[26:28]) != 0 || headerSize > len(packageBytes)-tagSize {
		return nil, errKeyMaterialAuthentication
	}
	header := packageBytes[:headerSize]
	ciphertextLength := binary.BigEndian.Uint64(header[36:44])
	if ciphertextLength < tagSize || ciphertextLength > maxPackageBytes || ciphertextLength != uint64(len(packageBytes)-headerSize) {
		return nil, errKeyMaterialAuthentication
	}
	reference := KeyReference{
		ID:      string(header[72 : 72+idLength]),
		Kind:    string(header[72+idLength : headerSize]),
		Version: binary.BigEndian.Uint64(header[28:36]),
	}
	if !validProviderIdentifier(reference.ID, maxProviderIDBytes) || reference.Kind != windowsDPAPIUserKind || reference.Version == 0 || reference.Version > maxPublicVersion {
		return nil, errKeyMaterialAuthentication
	}
	if reference != material.reference() {
		return nil, errKeyMaterialAuthentication
	}
	key, err := deriveKeyMaterialKey(material, header[44:60])
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("initialize backup decryption")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("initialize backup authentication")
	}
	plaintext, err := aead.Open(nil, header[60:72], packageBytes[headerSize:], header)
	if err != nil {
		return nil, errKeyMaterialAuthentication
	}
	return plaintext, nil
}

func validateKeyMaterial(material *KeyMaterial) error {
	if material == nil {
		return errors.New("backup key material is required")
	}
	if !validProviderIdentifier(material.ProviderID, maxProviderIDBytes) {
		return errors.New("backup key provider id is invalid")
	}
	if material.ProviderKind != windowsDPAPIUserKind {
		return errors.New("backup key provider kind is unsupported")
	}
	if material.ProviderVersion == 0 || material.ProviderVersion > maxPublicVersion {
		return errors.New("backup key provider version is invalid")
	}
	var nonzero byte
	for _, value := range material.WrappingKey {
		nonzero |= value
	}
	if subtle.ConstantTimeByteEq(nonzero, 0) == 1 {
		return errors.New("backup wrapping key is invalid")
	}
	return nil
}

func validProviderIdentifier(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func deriveKeyMaterialKey(material *KeyMaterial, salt []byte) ([]byte, error) {
	info := make([]byte, 0, len(keyMaterialInfoPrefix)+2+len(material.ProviderID)+2+len(material.ProviderKind)+8)
	info = append(info, keyMaterialInfoPrefix...)
	info = binary.BigEndian.AppendUint16(info, uint16(len(material.ProviderID)))
	info = append(info, material.ProviderID...)
	info = binary.BigEndian.AppendUint16(info, uint16(len(material.ProviderKind)))
	info = append(info, material.ProviderKind...)
	info = binary.BigEndian.AppendUint64(info, material.ProviderVersion)
	reader := hkdf.New(sha256.New, material.WrappingKey[:], salt, info)
	key := make([]byte, keySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		wipe(key)
		return nil, errors.New("derive backup encryption key")
	}
	return key, nil
}

func (material *KeyMaterial) reference() KeyReference {
	return KeyReference{ID: material.ProviderID, Kind: material.ProviderKind, Version: material.ProviderVersion}
}
