// Package backup implements the independently authored CPA Cloud backup format.
// It intentionally accepts only the CPA Cloud database and root-key filenames.
package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
	"modernc.org/sqlite"
)

const (
	databaseFilename = "cpa-cloud.db"
	rootKeyFilename  = "master.key"

	formatVersion = uint16(1)
	headerSize    = 68
	saltSize      = 16
	nonceSize     = 12
	keySize       = 32
	tagSize       = 16

	scryptN = 32768
	scryptR = 8
	scryptP = 1

	maxPasswordBytes = 1024
	maxRootKeyBytes  = 4096
	maxDatabaseBytes = 128 << 20
	maxPackageBytes  = maxDatabaseBytes + (2 << 20)
	maxMetadataBytes = 64 << 10
)

var (
	packageMagic = [16]byte{'C', 'P', 'A', 'C', 'L', 'O', 'U', 'D', '-', 'B', 'A', 'C', 'K', 'U', 'P', 0}
	payloadMagic = [16]byte{'C', 'P', 'A', 'C', 'L', 'O', 'U', 'D', '-', 'D', 'A', 'T', 'A', '-', '1', 0}
)

// Info is authenticated package metadata. Verification says that this package
// is structurally and cryptographically valid for this format version; it does
// not promise compatibility with every future CPA Cloud version.
type Info struct {
	FormatVersion int           `json:"format_version"`
	SourceProgram string        `json:"source_program"`
	SourceVersion string        `json:"source_version"`
	CreatedAt     string        `json:"created_at"`
	Files         []FileInfo    `json:"files"`
	KeyProvider   *KeyReference `json:"key_provider,omitempty"`
}

// KeyReference identifies the exact non-secret provider version required to
// open a key-material backup. It never contains the wrapping key.
type KeyReference struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Version uint64 `json:"version"`
}

type FileInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Integrity string `json:"integrity"`
}

type record struct {
	name string
	data []byte
	sum  [sha256.Size]byte
}

type decodedPackage struct {
	info    Info
	db      []byte
	rootKey []byte
}

// Create writes a new encrypted package. output must not already exist.
func Create(ctx context.Context, dataDir, output string, password []byte, sourceVersion string) (Info, error) {
	if err := validatePassword(password); err != nil {
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
	info := newInfo(sourceVersion, records)
	plaintxt, err := encodePayload(info, records)
	if err != nil {
		return Info{}, err
	}
	defer wipe(plaintxt)
	packageBytes, err := sealPackage(password, plaintxt)
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

// Verify authenticates and validates a package without changing an instance.
func Verify(ctx context.Context, input string, password []byte) (Info, error) {
	decoded, cleanup, err := openAndValidate(ctx, input, password, "verify")
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return Info{}, err
	}
	defer decoded.destroy()
	return decoded.info, nil
}

// Restore authenticates a package and restores it into a new directory. It
// invalidates administrator/OAuth sessions and pauses uncertain refresh work.
func Restore(ctx context.Context, input, dataDir string, password []byte) (Info, error) {
	target, parent, err := validateNewRestoreTarget(dataDir)
	if err != nil {
		return Info{}, err
	}
	parentIdentity, err := os.Lstat(parent)
	if err != nil {
		return Info{}, fmt.Errorf("inspect restore parent identity: %w", err)
	}
	decoded, cleanup, err := openAndValidateIn(ctx, input, password, parent, "restore")
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

func newInfo(sourceVersion string, records []record) Info {
	return newInfoForFormat(int(formatVersion), sourceVersion, records, nil)
}

func newInfoForFormat(version int, sourceVersion string, records []record, keyProvider *KeyReference) Info {
	files := make([]FileInfo, 0, len(records))
	for _, item := range records {
		files = append(files, FileInfo{Name: item.name, Size: int64(len(item.data)), Integrity: "authenticated-sha256"})
	}
	return Info{
		FormatVersion: version, SourceProgram: "cpa-cloud", SourceVersion: sourceVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Files: files, KeyProvider: keyProvider,
	}
}

func encodePayload(info Info, records []record) ([]byte, error) {
	metadata, err := json.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("encode backup metadata: %w", err)
	}
	if len(metadata) > maxMetadataBytes {
		return nil, errors.New("backup metadata exceeds the supported size limit")
	}
	var buf bytes.Buffer
	buf.Write(payloadMagic[:])
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(metadata)))
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(records)))
	_ = binary.Write(&buf, binary.BigEndian, uint16(0))
	buf.Write(metadata)
	for _, item := range records {
		if len(item.name) > 255 {
			return nil, errors.New("backup record name is too long")
		}
		_ = binary.Write(&buf, binary.BigEndian, uint16(len(item.name)))
		buf.WriteString(item.name)
		_ = binary.Write(&buf, binary.BigEndian, uint64(len(item.data)))
		buf.Write(item.sum[:])
		buf.Write(item.data)
	}
	return buf.Bytes(), nil
}

func sealPackage(password, plaintext []byte) ([]byte, error) {
	salt := make([]byte, saltSize)
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate backup salt: %w", err)
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate backup nonce: %w", err)
	}
	header := make([]byte, headerSize)
	copy(header[:16], packageMagic[:])
	binary.BigEndian.PutUint16(header[16:18], formatVersion)
	binary.BigEndian.PutUint16(header[18:20], headerSize)
	binary.BigEndian.PutUint32(header[20:24], scryptN)
	binary.BigEndian.PutUint32(header[24:28], scryptR)
	binary.BigEndian.PutUint32(header[28:32], scryptP)
	copy(header[32:48], salt)
	copy(header[48:60], nonce)
	binary.BigEndian.PutUint64(header[60:68], uint64(len(plaintext)+tagSize))
	key, err := scrypt.Key(password, salt, scryptN, scryptR, scryptP, keySize)
	if err != nil {
		return nil, errors.New("derive backup encryption key")
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

func openAndValidate(ctx context.Context, input string, password []byte, purpose string) (*decodedPackage, func(), error) {
	return openAndValidateIn(ctx, input, password, os.TempDir(), purpose)
}

func openAndValidateIn(ctx context.Context, input string, password []byte, tempParent string, purpose string) (*decodedPackage, func(), error) {
	if err := validatePassword(password); err != nil {
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
	plaintext, err := openPackage(password, packageBytes)
	if err != nil {
		return nil, nil, err
	}
	defer wipe(plaintext)
	decoded, err := decodePayload(plaintext)
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

func openPackage(password, packageBytes []byte) ([]byte, error) {
	if len(packageBytes) < headerSize+tagSize {
		return nil, errors.New("backup package is truncated")
	}
	header := packageBytes[:headerSize]
	if !bytes.Equal(header[:16], packageMagic[:]) {
		return nil, errors.New("unknown backup package format")
	}
	if binary.BigEndian.Uint16(header[16:18]) != formatVersion || binary.BigEndian.Uint16(header[18:20]) != headerSize {
		return nil, errors.New("unsupported backup package version")
	}
	if binary.BigEndian.Uint32(header[20:24]) != scryptN || binary.BigEndian.Uint32(header[24:28]) != scryptR || binary.BigEndian.Uint32(header[28:32]) != scryptP {
		return nil, errors.New("unsupported backup key-derivation parameters")
	}
	ciphertextLength := binary.BigEndian.Uint64(header[60:68])
	if ciphertextLength > maxPackageBytes || ciphertextLength < tagSize || ciphertextLength != uint64(len(packageBytes)-headerSize) {
		return nil, errors.New("backup package length is invalid")
	}
	key, err := scrypt.Key(password, header[32:48], scryptN, scryptR, scryptP, keySize)
	if err != nil {
		return nil, errors.New("derive backup decryption key")
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
	plaintext, err := aead.Open(nil, header[48:60], packageBytes[headerSize:], header)
	if err != nil {
		return nil, errors.New("backup authentication failed (wrong password or damaged package)")
	}
	return plaintext, nil
}

func decodePayload(plaintext []byte) (*decodedPackage, error) {
	return decodePayloadForFormat(plaintext, int(formatVersion), nil)
}

func decodePayloadForFormat(plaintext []byte, expectedVersion int, expectedKeyProvider *KeyReference) (*decodedPackage, error) {
	reader := bytes.NewReader(plaintext)
	magic := make([]byte, len(payloadMagic))
	if _, err := io.ReadFull(reader, magic); err != nil || !bytes.Equal(magic, payloadMagic[:]) {
		return nil, errors.New("backup payload is invalid")
	}
	var metadataLength uint32
	var count, reserved uint16
	if binary.Read(reader, binary.BigEndian, &metadataLength) != nil || binary.Read(reader, binary.BigEndian, &count) != nil || binary.Read(reader, binary.BigEndian, &reserved) != nil {
		return nil, errors.New("backup payload is truncated")
	}
	if metadataLength == 0 || metadataLength > maxMetadataBytes || count != 2 || reserved != 0 {
		return nil, errors.New("backup payload structure is invalid")
	}
	metadata := make([]byte, metadataLength)
	if _, err := io.ReadFull(reader, metadata); err != nil {
		return nil, errors.New("backup metadata is truncated")
	}
	var info Info
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&info); err != nil || info.FormatVersion != expectedVersion || info.SourceProgram != "cpa-cloud" {
		return nil, errors.New("backup metadata is invalid")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("backup metadata contains trailing data")
	}
	if _, err := time.Parse(time.RFC3339Nano, info.CreatedAt); err != nil || len(info.Files) != 2 || !equalKeyReference(info.KeyProvider, expectedKeyProvider) {
		return nil, errors.New("backup metadata is invalid")
	}
	expected := []string{databaseFilename, rootKeyFilename}
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
	if reader.Len() != 0 {
		return nil, errors.New("backup contains trailing records or data")
	}
	if int64(len(records[0].data)) > maxDatabaseBytes || len(records[1].data) > maxRootKeyBytes {
		return nil, errors.New("backup record exceeds the supported size limit")
	}
	if err := validateRootKeyBytes(records[1].data); err != nil {
		return nil, err
	}
	transferred = true
	return &decodedPackage{info: info, db: records[0].data, rootKey: records[1].data}, nil
}

func equalKeyReference(left, right *KeyReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && left.Kind == right.Kind && left.Version == right.Version
}

func decodeRecord(reader *bytes.Reader) (record, error) {
	var nameLength uint16
	if err := binary.Read(reader, binary.BigEndian, &nameLength); err != nil || nameLength == 0 || nameLength > 255 {
		return record{}, errors.New("backup record header is invalid")
	}
	name := make([]byte, nameLength)
	if _, err := io.ReadFull(reader, name); err != nil {
		return record{}, errors.New("backup record name is truncated")
	}
	var size uint64
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil || size > maxDatabaseBytes {
		return record{}, errors.New("backup record size is invalid")
	}
	var expected [sha256.Size]byte
	if _, err := io.ReadFull(reader, expected[:]); err != nil {
		return record{}, errors.New("backup record checksum is truncated")
	}
	if size > uint64(reader.Len()) {
		return record{}, errors.New("backup record content is truncated")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(reader, data); err != nil {
		return record{}, errors.New("backup record content is truncated")
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(actual[:], expected[:]) {
		wipe(data)
		return record{}, errors.New("backup record integrity check failed")
	}
	return record{name: string(name), data: data, sum: actual}, nil
}

func (d *decodedPackage) destroy() {
	if d == nil {
		return
	}
	wipe(d.db)
	wipe(d.rootKey)
}

func onlineSnapshot(ctx context.Context, source, destination string) error {
	if err := requireRegularFile(source); err != nil {
		return fmt.Errorf("source database: %w", err)
	}
	if info, err := os.Lstat(source); err != nil {
		return err
	} else if info.Size() > maxDatabaseBytes {
		return errors.New("source database exceeds the supported size limit")
	}
	db, err := sql.Open("sqlite", sqliteURI(source, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	type backuper interface {
		NewBackup(string) (*sqlite.Backup, error)
	}
	err = conn.Raw(func(driverConn any) error {
		b, ok := driverConn.(backuper)
		if !ok {
			return errors.New("SQLite driver does not expose online backup support")
		}
		backup, err := b.NewBackup(sqliteURI(destination, "rwc"))
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		var busySince time.Time
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(128)
			if err != nil {
				if !sqliteBusy(err) {
					return err
				}
				if busySince.IsZero() {
					busySince = time.Now()
				} else if time.Since(busySince) >= 5*time.Second {
					return errors.New("source database remained busy during online backup")
				}
				timer := time.NewTimer(25 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			busySince = time.Time{}
			if info, statErr := os.Stat(destination); statErr == nil && info.Size() > maxDatabaseBytes {
				return errors.New("SQLite snapshot exceeds the supported size limit")
			} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			if !more {
				break
			}
		}
		if err := backup.Finish(); err != nil {
			return err
		}
		finished = true
		return nil
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func sqliteBusy(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded) && (coded.Code() == 5 || coded.Code() == 6)
}

func sqliteURI(path, mode string) string {
	abs, _ := filepath.Abs(path)
	slash := filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && filepath.VolumeName(abs) != "" {
		slash = "/" + slash
	}
	u := url.URL{Scheme: "file", Path: slash}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

func verifySQLite(ctx context.Context, path string) error {
	if err := requireRegularFile(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteURI(path, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		count++
		if result != "ok" {
			return errors.New("SQLite integrity check failed")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("SQLite integrity check returned an invalid result")
	}
	foreignRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	foreignViolation := foreignRows.Next()
	iterationErr, closeErr := foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if foreignViolation {
		return errors.New("SQLite foreign key check failed")
	}
	for _, table := range []string{"admins", "sessions", "employees", "access_keys", "upstreams"} {
		var present int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
			return err
		}
		if present != 1 {
			return errors.New("database is not a recognized CPA Cloud instance")
		}
	}
	var admins int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&admins); err != nil {
		return err
	}
	if admins != 1 {
		return errors.New("database does not contain exactly one initialized CPA Cloud administrator")
	}
	return nil
}

func sanitizeRestoredDatabase(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", sqliteURI(path, "rw"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=DELETE`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if exists, err := tableExists(ctx, tx, "sessions"); err != nil {
		return err
	} else if exists {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return err
		}
	}
	if exists, err := tableExists(ctx, tx, "codex_oauth_sessions"); err != nil {
		return err
	} else if exists {
		if _, err := tx.ExecContext(ctx, `DELETE FROM codex_oauth_sessions`); err != nil {
			return err
		}
	}
	if exists, err := tableExists(ctx, tx, "codex_oauth_refresh_states"); err != nil {
		return err
	} else if exists {
		if _, err := tx.ExecContext(ctx, `UPDATE codex_oauth_refresh_states
			SET state='paused',reason_code='backup_restore_uncertain_refresh',updated_at=? WHERE state='in_progress'`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil && !strings.Contains(strings.ToLower(err.Error()), "not a wal") {
		return err
	}
	return nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func tableExists(ctx context.Context, q queryRower, name string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count)
	return count == 1, err
}

func validatePassword(password []byte) error {
	if len(password) == 0 {
		return errors.New("backup password is required")
	}
	if len(password) > maxPasswordBytes {
		return errors.New("backup password exceeds 1024 bytes")
	}
	return nil
}

func validateSourceDirectory(path string) (string, error) {
	abs, err := cleanExplicitFilePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect source data directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("source data directory must be a real directory")
	}
	if reparse, err := isReparsePoint(abs); err != nil {
		return "", err
	} else if reparse {
		return "", errors.New("source data directory cannot be a reparse point")
	}
	return abs, nil
}

func validateNewRestoreTarget(path string) (string, string, error) {
	target, err := cleanExplicitFilePath(path)
	if err != nil {
		return "", "", fmt.Errorf("restore data directory: %w", err)
	}
	volume := filepath.VolumeName(target)
	root := volume + string(os.PathSeparator)
	if filepath.Clean(target) == filepath.Clean(root) || filepath.Dir(target) == target {
		return "", "", errors.New("restore target cannot be a filesystem root")
	}
	if _, err := os.Lstat(target); err == nil {
		return "", "", errors.New("restore target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect restore target: %w", err)
	}
	parent := filepath.Dir(target)
	if err := validateExistingDirectoryChain(parent); err != nil {
		return "", "", fmt.Errorf("restore parent: %w", err)
	}
	return target, parent, nil
}

func validateExistingDirectoryChain(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	current := volume + string(os.PathSeparator)
	rel := strings.TrimPrefix(abs, current)
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("path contains a link or non-directory component")
		}
		reparse, err := isReparsePoint(current)
		if err != nil {
			return err
		}
		if reparse {
			return errors.New("path contains a reparse point")
		}
	}
	return nil
}

func cleanExplicitFilePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func readRootKey(path string) ([]byte, error) {
	data, err := readRegularFile(path, maxRootKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("read root key: %w", err)
	}
	if err := validateRootKeyBytes(data); err != nil {
		wipe(data)
		return nil, err
	}
	return data, nil
}

func validateRootKeyBytes(data []byte) error {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != 32 {
		wipe(decoded)
		return errors.New("root key is invalid")
	}
	wipe(decoded)
	return nil
}

func readRegularFile(path string, maximum int64) ([]byte, error) {
	if err := requireRegularFile(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() < 0 || info.Size() > maximum {
		return nil, errors.New("file exceeds the supported size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		wipe(data)
		return nil, errors.New("file exceeds the supported size limit")
	}
	return data, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("path must name a regular file")
	}
	reparse, err := isReparsePoint(path)
	if err != nil {
		return err
	}
	if reparse {
		return errors.New("path cannot be a reparse point")
	}
	return nil
}

func makeSecureTempDir(parent, pattern string) (string, error) {
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	if err := hardenPath(dir, true); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	return dir, nil
}

func writeExclusiveFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := hardenPath(path, false); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeNewSyncedFile(output string, data []byte) error {
	return writeNewSyncedFileWithSync(output, data, syncDirectory)
}

func writeNewSyncedFileWithSync(output string, data []byte, syncDir func(string) error) error {
	parent := filepath.Dir(output)
	temp, err := os.CreateTemp(parent, ".cpa-cloud-backup-output-")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	outputCreated := false
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		if outputCreated && !committed {
			_ = os.Remove(output)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict temporary output mode: %w", err)
	}
	if err := hardenPath(tempPath, false); err != nil {
		return fmt.Errorf("restrict temporary output access: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temporary output: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary output: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary output: %w", err)
	}
	if err := os.Link(tempPath, output); err != nil {
		return fmt.Errorf("commit new file without overwrite: %w", err)
	}
	outputCreated = true
	if err := syncDir(parent); err != nil {
		return fmt.Errorf("sync output directory: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("remove temporary output name: %w", err)
	}
	if err := syncDir(parent); err != nil {
		return fmt.Errorf("sync committed output directory: %w", err)
	}
	committed = true
	return nil
}

func publishRestoreDirectory(stage, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return errors.New("restore target already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	createdInfo, err := os.Lstat(target)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if success {
			return
		}
		current, err := os.Lstat(target)
		if err == nil && os.SameFile(createdInfo, current) {
			removeKnownDirectory(target, databaseFilename, rootKeyFilename)
		}
	}()
	if err := hardenPath(target, true); err != nil {
		return err
	}
	for _, name := range []string{databaseFilename, rootKeyFilename} {
		from, to := filepath.Join(stage, name), filepath.Join(target, name)
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if err := hardenPath(to, false); err != nil {
			return err
		}
	}
	if err := syncDirectory(target); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	success = true
	return nil
}

func removeKnownDirectory(dir string, names ...string) {
	if dir == "" {
		return
	}
	for _, name := range names {
		if name == databaseFilename {
			_ = os.Remove(filepath.Join(dir, name+"-wal"))
			_ = os.Remove(filepath.Join(dir, name+"-shm"))
			_ = os.Remove(filepath.Join(dir, name+"-journal"))
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
	_ = os.Remove(dir)
}

func wipe(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
