package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

const (
	systemdKeyMaterialFormatVersion = uint16(3)
	systemdKeyMaterialFixedHeader   = 76
	systemdBindingFilename          = "master-key-binding.json"
	maxSystemdBindingBytes          = 4096
	maxKeyIDBytes                   = 64
	maxInstanceIDBytes              = 36
)

var systemdKeyMaterialInfoPrefix = []byte("cpa-cloud/backup/v3/linux-systemd-credential/v1\x00")

type systemdMasterBinding struct {
	FormatVersion    uint64 `json:"format_version"`
	ProviderKind     string `json:"provider_kind"`
	InstanceID       string `json:"instance_id"`
	KeyID            string `json:"key_id"`
	KeyVersion       uint64 `json:"key_version"`
	CanaryNonce      string `json:"canary_nonce"`
	CanaryCiphertext string `json:"canary_ciphertext"`
}

func createWithSystemdKeyMaterial(ctx context.Context, dataDir, output string, material KeyMaterial, sourceVersion string) (Info, error) {
	defer wipe(material.WrappingKey[:])
	if err := validateSystemdKeyMaterial(&material); err != nil {
		return Info{}, err
	}
	source, err := validateSourceDirectory(dataDir)
	if err != nil {
		return Info{}, err
	}
	if _, err := os.Lstat(filepath.Join(source, rootKeyFilename)); !errors.Is(err, os.ErrNotExist) {
		return Info{}, errors.New("systemd-backed instance must not contain a legacy master key file")
	}
	output, err = cleanExplicitFilePath(output)
	if err != nil {
		return Info{}, fmt.Errorf("output path: %w", err)
	}
	if _, err := os.Lstat(output); err == nil {
		return Info{}, errors.New("output file already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}
	parent := filepath.Dir(output)
	if err := validateExistingDirectoryChain(parent); err != nil {
		return Info{}, fmt.Errorf("output directory: %w", err)
	}
	bindingBefore, err := readRegularFile(filepath.Join(source, systemdBindingFilename), maxSystemdBindingBytes)
	if err != nil {
		return Info{}, fmt.Errorf("read systemd master-key binding: %w", err)
	}
	defer wipe(bindingBefore)
	if err := validateSystemdBinding(bindingBefore, &material); err != nil {
		return Info{}, err
	}
	stage, err := makeSecureTempDir(parent, ".cpa-cloud-backup-create-")
	if err != nil {
		return Info{}, err
	}
	defer removeKnownDirectory(stage, databaseFilename)
	snapshot := filepath.Join(stage, databaseFilename)
	if err := onlineSnapshot(ctx, filepath.Join(source, databaseFilename), snapshot); err != nil {
		return Info{}, fmt.Errorf("create SQLite snapshot: %w", err)
	}
	if err := verifySQLite(ctx, snapshot); err != nil {
		return Info{}, fmt.Errorf("verify SQLite snapshot: %w", err)
	}
	bindingAfter, err := readRegularFile(filepath.Join(source, systemdBindingFilename), maxSystemdBindingBytes)
	if err != nil {
		return Info{}, err
	}
	defer wipe(bindingAfter)
	if subtle.ConstantTimeCompare(bindingBefore, bindingAfter) != 1 {
		return Info{}, errors.New("systemd master-key binding changed while the backup was being created")
	}
	dbBytes, err := readRegularFile(snapshot, maxDatabaseBytes)
	if err != nil {
		return Info{}, err
	}
	defer wipe(dbBytes)
	records := []record{{name: databaseFilename, data: dbBytes, sum: sha256.Sum256(dbBytes)}, {name: systemdBindingFilename, data: bindingBefore, sum: sha256.Sum256(bindingBefore)}}
	reference := material.reference()
	info := newInfoForFormat(int(systemdKeyMaterialFormatVersion), sourceVersion, records, &reference)
	plaintext, err := encodePayload(info, records)
	if err != nil {
		return Info{}, err
	}
	defer wipe(plaintext)
	encoded, err := sealSystemdPackage(&material, plaintext)
	if err != nil {
		return Info{}, err
	}
	defer wipe(encoded)
	if int64(len(encoded)) > maxPackageBytes {
		return Info{}, errors.New("backup package exceeds the supported size limit")
	}
	if err := validateSystemdBinding(bindingBefore, &material); err != nil {
		return Info{}, err
	}
	if err := writeNewSyncedFile(output, encoded); err != nil {
		return Info{}, fmt.Errorf("write backup package: %w", err)
	}
	return info, nil
}

func verifyWithSystemdKeyMaterial(ctx context.Context, input string, material KeyMaterial) (Info, error) {
	defer wipe(material.WrappingKey[:])
	decoded, cleanup, err := openAndValidateSystemdIn(ctx, input, &material, os.TempDir(), "verify")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Info{}, err
	}
	defer decoded.destroy()
	return decoded.info, nil
}

func restoreWithSystemdKeyMaterial(ctx context.Context, input, dataDir string, material KeyMaterial) (Info, error) {
	defer wipe(material.WrappingKey[:])
	target, parent, err := validateNewRestoreTarget(dataDir)
	if err != nil {
		return Info{}, err
	}
	parentIdentity, err := os.Lstat(parent)
	if err != nil {
		return Info{}, err
	}
	decoded, cleanup, err := openAndValidateSystemdIn(ctx, input, &material, parent, "restore")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Info{}, err
	}
	defer decoded.destroy()
	stage, err := makeSecureTempDir(parent, ".cpa-cloud-restore-stage-")
	if err != nil {
		return Info{}, err
	}
	defer removeKnownDirectory(stage, databaseFilename, systemdBindingFilename)
	if err := writeExclusiveFile(filepath.Join(stage, databaseFilename), decoded.db, 0o600); err != nil {
		return Info{}, fmt.Errorf("stage database: %w", err)
	}
	if err := writeExclusiveFile(filepath.Join(stage, systemdBindingFilename), decoded.rootKey, 0o600); err != nil {
		return Info{}, fmt.Errorf("stage master-key binding: %w", err)
	}
	if err := sanitizeRestoredDatabase(ctx, filepath.Join(stage, databaseFilename)); err != nil {
		return Info{}, err
	}
	if err := verifySQLite(ctx, filepath.Join(stage, databaseFilename)); err != nil {
		return Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	if err := validateExistingDirectoryChain(parent); err != nil {
		return Info{}, err
	}
	currentParent, err := os.Lstat(parent)
	if err != nil || !os.SameFile(parentIdentity, currentParent) {
		return Info{}, errors.New("restore parent changed during validation")
	}
	if _, err := os.Lstat(target); err == nil {
		return Info{}, errors.New("restore target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}
	if err := validateSystemdBinding(decoded.rootKey, &material); err != nil {
		return Info{}, err
	}
	if err := publishRestoreDirectoryFiles(stage, target, databaseFilename, systemdBindingFilename); err != nil {
		return Info{}, fmt.Errorf("publish restored data directory: %w", err)
	}
	stage = ""
	return decoded.info, nil
}

func openAndValidateSystemdIn(ctx context.Context, input string, material *KeyMaterial, tempParent, purpose string) (*decodedPackage, func(), error) {
	if err := validateSystemdKeyMaterial(material); err != nil {
		return nil, nil, err
	}
	input, err := cleanExplicitFilePath(input)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := readRegularFile(input, maxPackageBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read backup package: %w", err)
	}
	defer wipe(encoded)
	plaintext, err := openSystemdPackage(material, encoded)
	if err != nil {
		return nil, nil, err
	}
	defer wipe(plaintext)
	decoded, err := decodeSystemdPayload(plaintext, material)
	if err != nil {
		return nil, nil, err
	}
	stage, err := makeSecureTempDir(tempParent, ".cpa-cloud-backup-"+purpose+"-")
	if err != nil {
		decoded.destroy()
		return nil, nil, err
	}
	cleanup := func() { removeKnownDirectory(stage, databaseFilename) }
	tempDB := filepath.Join(stage, databaseFilename)
	if err := writeExclusiveFile(tempDB, decoded.db, 0o600); err != nil {
		cleanup()
		decoded.destroy()
		return nil, nil, err
	}
	if err := verifySQLite(ctx, tempDB); err != nil {
		cleanup()
		decoded.destroy()
		return nil, nil, fmt.Errorf("backup database failed integrity validation: %w", err)
	}
	return decoded, cleanup, nil
}

func sealSystemdPackage(material *KeyMaterial, plaintext []byte) ([]byte, error) {
	salt, nonce := make([]byte, saltSize), make([]byte, nonceSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	headerSize := systemdKeyMaterialFixedHeader + len(material.ProviderID) + len(material.ProviderKind) + len(material.InstanceID) + len(material.KeyID)
	header := make([]byte, headerSize)
	copy(header[:16], packageMagic[:])
	binary.BigEndian.PutUint16(header[16:18], systemdKeyMaterialFormatVersion)
	binary.BigEndian.PutUint16(header[18:20], uint16(headerSize))
	binary.BigEndian.PutUint16(header[20:22], keyMaterialKDFHKDFSHA256)
	lengths := []int{len(material.ProviderID), len(material.ProviderKind), len(material.InstanceID), len(material.KeyID)}
	for index, length := range lengths {
		binary.BigEndian.PutUint16(header[22+index*2:24+index*2], uint16(length))
	}
	binary.BigEndian.PutUint16(header[30:32], 0)
	binary.BigEndian.PutUint64(header[32:40], material.ProviderVersion)
	binary.BigEndian.PutUint64(header[40:48], uint64(len(plaintext)+tagSize))
	copy(header[48:64], salt)
	copy(header[64:76], nonce)
	offset := systemdKeyMaterialFixedHeader
	for _, value := range []string{material.ProviderID, material.ProviderKind, material.InstanceID, material.KeyID} {
		copy(header[offset:], value)
		offset += len(value)
	}
	key, err := deriveSystemdKey(material, salt)
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
	return append(header, aead.Seal(nil, nonce, plaintext, header)...), nil
}

func openSystemdPackage(material *KeyMaterial, encoded []byte) ([]byte, error) {
	if len(encoded) < systemdKeyMaterialFixedHeader+tagSize || !bytes.Equal(encoded[:16], packageMagic[:]) || binary.BigEndian.Uint16(encoded[16:18]) != systemdKeyMaterialFormatVersion {
		return nil, errors.New("unsupported backup package version")
	}
	fixed := encoded[:systemdKeyMaterialFixedHeader]
	if binary.BigEndian.Uint16(fixed[20:22]) != keyMaterialKDFHKDFSHA256 || binary.BigEndian.Uint16(fixed[30:32]) != 0 {
		return nil, errKeyMaterialAuthentication
	}
	lengths := []int{int(binary.BigEndian.Uint16(fixed[22:24])), int(binary.BigEndian.Uint16(fixed[24:26])), int(binary.BigEndian.Uint16(fixed[26:28])), int(binary.BigEndian.Uint16(fixed[28:30]))}
	headerSize := int(binary.BigEndian.Uint16(fixed[18:20]))
	if lengths[0] < 1 || lengths[0] > maxProviderIDBytes || lengths[1] < 1 || lengths[1] > maxProviderKindBytes || lengths[2] != maxInstanceIDBytes || lengths[3] < 1 || lengths[3] > maxKeyIDBytes || headerSize != systemdKeyMaterialFixedHeader+lengths[0]+lengths[1]+lengths[2]+lengths[3] || headerSize > len(encoded)-tagSize {
		return nil, errKeyMaterialAuthentication
	}
	cipherLength := binary.BigEndian.Uint64(fixed[40:48])
	if cipherLength < tagSize || cipherLength > maxPackageBytes || cipherLength != uint64(len(encoded)-headerSize) {
		return nil, errKeyMaterialAuthentication
	}
	offset := systemdKeyMaterialFixedHeader
	values := make([]string, 4)
	for index, length := range lengths {
		values[index] = string(encoded[offset : offset+length])
		offset += length
	}
	if values[0] != material.ProviderID || values[1] != material.ProviderKind || !strings.EqualFold(values[2], material.InstanceID) || values[3] != material.KeyID || binary.BigEndian.Uint64(fixed[32:40]) != material.ProviderVersion {
		return nil, errKeyMaterialAuthentication
	}
	key, err := deriveSystemdKey(material, fixed[48:64])
	if err != nil {
		return nil, err
	}
	defer wipe(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errKeyMaterialAuthentication
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errKeyMaterialAuthentication
	}
	plaintext, err := aead.Open(nil, fixed[64:76], encoded[headerSize:], encoded[:headerSize])
	if err != nil {
		return nil, errKeyMaterialAuthentication
	}
	return plaintext, nil
}

func deriveSystemdKey(material *KeyMaterial, salt []byte) ([]byte, error) {
	info := append([]byte(nil), systemdKeyMaterialInfoPrefix...)
	for _, value := range []string{material.ProviderID, material.ProviderKind, strings.ToLower(material.InstanceID), material.KeyID} {
		info = binary.BigEndian.AppendUint16(info, uint16(len(value)))
		info = append(info, value...)
	}
	info = binary.BigEndian.AppendUint64(info, material.ProviderVersion)
	defer wipe(info)
	reader := hkdf.New(sha256.New, material.WrappingKey[:], salt, info)
	key := make([]byte, keySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		wipe(key)
		return nil, errors.New("derive backup encryption key")
	}
	return key, nil
}

func decodeSystemdPayload(plaintext []byte, material *KeyMaterial) (*decodedPackage, error) {
	reader := bytes.NewReader(plaintext)
	magic := make([]byte, len(payloadMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || !bytes.Equal(magic, payloadMagic[:]) {
		return nil, errors.New("backup payload is invalid")
	}
	var metadataLength uint32
	var count, reserved uint16
	if binary.Read(reader, binary.BigEndian, &metadataLength) != nil || binary.Read(reader, binary.BigEndian, &count) != nil || binary.Read(reader, binary.BigEndian, &reserved) != nil || metadataLength == 0 || metadataLength > maxMetadataBytes || count != 2 || reserved != 0 {
		return nil, errors.New("backup payload structure is invalid")
	}
	metadata := make([]byte, metadataLength)
	if _, err := io.ReadFull(reader, metadata); err != nil {
		return nil, errors.New("backup metadata is truncated")
	}
	var info Info
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	reference := material.reference()
	if decoder.Decode(&info) != nil || decoder.Decode(&struct{}{}) != io.EOF || info.FormatVersion != int(systemdKeyMaterialFormatVersion) || info.SourceProgram != "cpa-cloud" || len(info.Files) != 2 || !equalKeyReference(info.KeyProvider, &reference) {
		return nil, errors.New("backup metadata is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, info.CreatedAt); err != nil {
		return nil, errors.New("backup metadata is invalid")
	}
	expected := []string{databaseFilename, systemdBindingFilename}
	records := make([]record, 0, 2)
	transferred := false
	defer func() {
		if !transferred {
			for _, item := range records {
				wipe(item.data)
			}
		}
	}()
	for index := 0; index < 2; index++ {
		item, err := decodeRecord(reader)
		if err != nil {
			return nil, err
		}
		if item.name != expected[index] || info.Files[index].Name != item.name || info.Files[index].Size != int64(len(item.data)) || info.Files[index].Integrity != "authenticated-sha256" {
			wipe(item.data)
			return nil, errors.New("backup contains unexpected or inconsistent records")
		}
		records = append(records, item)
	}
	if reader.Len() != 0 || int64(len(records[0].data)) > maxDatabaseBytes || len(records[1].data) > maxSystemdBindingBytes {
		return nil, errors.New("backup contains trailing or oversized data")
	}
	if err := validateSystemdBinding(records[1].data, material); err != nil {
		return nil, err
	}
	transferred = true
	return &decodedPackage{info: info, db: records[0].data, rootKey: records[1].data}, nil
}

func validateSystemdKeyMaterial(material *KeyMaterial) error {
	if material == nil || material.ValidateInstallationBinding == nil || !validProviderIdentifier(material.ProviderID, maxProviderIDBytes) || material.ProviderKind != linuxSystemdCredentialKind || !validUUID(material.InstanceID) || !validKeyIdentifier(material.KeyID) || material.ProviderVersion == 0 || material.ProviderVersion > maxPublicVersion {
		return errors.New("backup key material reference is invalid")
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

func validateSystemdBinding(encoded []byte, material *KeyMaterial) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var binding systemdMasterBinding
	if decoder.Decode(&binding) != nil || decoder.Decode(&struct{}{}) != io.EOF || binding.FormatVersion != 1 || binding.ProviderKind != linuxSystemdCredentialKind || !strings.EqualFold(binding.InstanceID, material.InstanceID) || !validKeyIdentifier(binding.KeyID) || binding.KeyID == material.KeyID || binding.KeyVersion == 0 || binding.KeyVersion > maxPublicVersion {
		return errors.New("systemd master-key binding does not match backup credential")
	}
	nonce, nonceErr := base64.RawStdEncoding.DecodeString(binding.CanaryNonce)
	ciphertext, cipherErr := base64.RawStdEncoding.DecodeString(binding.CanaryCiphertext)
	defer wipe(nonce)
	defer wipe(ciphertext)
	if nonceErr != nil || cipherErr != nil || len(nonce) != nonceSize || len(ciphertext) < tagSize {
		return errors.New("systemd master-key binding is invalid")
	}
	if material.ValidateInstallationBinding == nil || material.ValidateInstallationBinding(encoded) != nil {
		return errors.New("systemd master-key binding authentication failed")
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, ch := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}
func validKeyIdentifier(value string) bool {
	if value == "" || len(value) > maxKeyIDBytes {
		return false
	}
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}
