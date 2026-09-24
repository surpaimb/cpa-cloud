//go:build windows

package keyprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	protectedFileVersion = uint16(1)
	protectedHeaderSize  = 28
	maxProtectedFileSize = 64 << 10
)

var protectedFileMagic = [12]byte{'C', 'P', 'A', '-', 'K', 'E', 'Y', '-', 'D', 'P', 'A', 'P'}

type Store struct {
	root         string
	rootIdentity os.FileInfo
}

type PreparedVersion struct {
	store     *Store
	path      string
	digest    [sha256.Size]byte
	committed bool
}

func Open(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil || root == "" {
		return nil, errors.New("key provider store path is invalid")
	}
	absolute = filepath.Clean(absolute)
	if err := validateLocalFixedVolume(absolute); err != nil {
		return nil, err
	}
	if filepath.Dir(absolute) == absolute {
		return nil, errors.New("key provider store cannot be a filesystem root")
	}
	if err := ensureProtectedDirectory(absolute); err != nil {
		return nil, fmt.Errorf("prepare key provider store: %w", err)
	}
	identity, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect key provider store: %w", err)
	}
	return &Store{root: absolute, rootIdentity: identity}, nil
}

func validateLocalFixedVolume(path string) error {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
		return errors.New("key provider store must be on a local fixed volume")
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !((volume[0] >= 'A' && volume[0] <= 'Z') || (volume[0] >= 'a' && volume[0] <= 'z')) {
		return errors.New("key provider store must use a local drive path")
	}
	rootPointer, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || windows.GetDriveType(rootPointer) != windows.DRIVE_FIXED {
		return errors.New("key provider store must be on a local fixed volume")
	}
	return nil
}

func (s *Store) Ready() (bool, string) {
	if s == nil || s.validateRoot() != nil {
		return false, "protected_store_invalid"
	}
	return true, ReasonReady
}

func (s *Store) PrepareVersion(ctx context.Context, providerID string, version uint64) (_ *PreparedVersion, returnErr error) {
	if err := validateReference(providerID, version); err != nil {
		return nil, err
	}
	if err := s.validateRoot(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, errors.New("generate backup wrapping key")
	}
	defer wipe(secret)
	protected, err := protect(secret, providerID, version)
	if err != nil {
		return nil, err
	}
	defer wipe(protected)
	encoded := encodeProtectedFile(version, protected)
	defer wipe(encoded)
	path := filepath.Join(s.root, versionFilename(providerID, version))
	if err := writeNewProtectedFile(s.root, path, encoded); err != nil {
		return nil, err
	}
	prepared := &PreparedVersion{store: s, path: path, digest: sha256.Sum256(encoded)}
	defer func() {
		if returnErr != nil {
			_ = prepared.Rollback()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved, err := s.Resolve(ctx, providerID, version)
	if err != nil {
		return nil, fmt.Errorf("verify protected backup key: %w", err)
	}
	defer resolved.Destroy()
	if subtle.ConstantTimeCompare(secret, resolved.Key[:]) != 1 {
		return nil, errors.New("protected backup key verification failed")
	}
	return prepared, nil
}

func (s *Store) Resolve(ctx context.Context, providerID string, version uint64) (*Material, error) {
	if err := validateReference(providerID, version); err != nil {
		return nil, err
	}
	if err := s.validateRoot(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, versionFilename(providerID, version))
	encoded, err := readProtectedFile(path)
	if err != nil {
		return nil, err
	}
	defer wipe(encoded)
	storedVersion, protected, err := decodeProtectedFile(encoded)
	if err != nil || storedVersion != version {
		return nil, errors.New("protected backup key file is invalid")
	}
	secret, err := unprotect(protected, providerID, version)
	if err != nil {
		return nil, err
	}
	defer wipe(secret)
	if len(secret) != 32 {
		return nil, errors.New("protected backup key has an invalid length")
	}
	material := &Material{ProviderID: providerID, Kind: KindWindowsDPAPIUser, Version: version}
	copy(material.Key[:], secret)
	return material, nil
}

func (p *PreparedVersion) Commit() {
	if p != nil {
		p.committed = true
	}
}

func (p *PreparedVersion) Rollback() error {
	if p == nil || p.store == nil || p.committed {
		return nil
	}
	if err := p.store.validateRoot(); err != nil {
		return err
	}
	encoded, err := readProtectedFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	wipe(encoded)
	if subtle.ConstantTimeCompare(digest[:], p.digest[:]) != 1 {
		return errors.New("prepared key-provider file changed; refusing rollback")
	}
	if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove prepared key-provider file: %w", err)
	}
	p.committed = true
	return syncDirectory(p.store.root)
}

func validateReference(providerID string, version uint64) error {
	if !validProviderID(providerID) {
		return errors.New("backup key provider id is invalid")
	}
	if !validVersion(version) {
		return errors.New("backup key provider version is invalid")
	}
	return nil
}

func versionFilename(providerID string, version uint64) string {
	return fmt.Sprintf("%s-v%016x.dpapi", providerID, version)
}

func encodeProtectedFile(version uint64, protected []byte) []byte {
	encoded := make([]byte, protectedHeaderSize+len(protected))
	copy(encoded[:12], protectedFileMagic[:])
	binary.BigEndian.PutUint16(encoded[12:14], protectedFileVersion)
	binary.BigEndian.PutUint16(encoded[14:16], protectedHeaderSize)
	binary.BigEndian.PutUint64(encoded[16:24], version)
	binary.BigEndian.PutUint32(encoded[24:28], uint32(len(protected)))
	copy(encoded[protectedHeaderSize:], protected)
	return encoded
}

func decodeProtectedFile(encoded []byte) (uint64, []byte, error) {
	if len(encoded) < protectedHeaderSize || len(encoded) > maxProtectedFileSize || !bytes.Equal(encoded[:12], protectedFileMagic[:]) {
		return 0, nil, errors.New("protected backup key file is invalid")
	}
	if binary.BigEndian.Uint16(encoded[12:14]) != protectedFileVersion || binary.BigEndian.Uint16(encoded[14:16]) != protectedHeaderSize {
		return 0, nil, errors.New("protected backup key file version is unsupported")
	}
	length := binary.BigEndian.Uint32(encoded[24:28])
	if length == 0 || uint64(length) != uint64(len(encoded)-protectedHeaderSize) {
		return 0, nil, errors.New("protected backup key file length is invalid")
	}
	return binary.BigEndian.Uint64(encoded[16:24]), encoded[protectedHeaderSize:], nil
}

func protect(secret []byte, providerID string, version uint64) ([]byte, error) {
	return cryptProtect(secret, providerID, version, false)
}

func unprotect(ciphertext []byte, providerID string, version uint64) ([]byte, error) {
	return cryptProtect(ciphertext, providerID, version, true)
}

func cryptProtect(input []byte, providerID string, version uint64, decrypt bool) ([]byte, error) {
	entropy := providerEntropy(providerID, version)
	defer wipe(entropy)
	inputBlob := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	entropyBlob := windows.DataBlob{Size: uint32(len(entropy)), Data: &entropy[0]}
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&inputBlob, nil, &entropyBlob, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		description, descriptionErr := windows.UTF16PtrFromString("CPA Cloud automated backup key")
		if descriptionErr != nil {
			return nil, errors.New("encode backup key description")
		}
		err = windows.CryptProtectData(&inputBlob, description, &entropyBlob, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	runtime.KeepAlive(input)
	runtime.KeepAlive(entropy)
	if err != nil {
		return nil, errors.New("Windows data protection failed")
	}
	if output.Data == nil || output.Size == 0 || output.Size > maxProtectedFileSize {
		if output.Data != nil {
			_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
		}
		return nil, errors.New("Windows data protection returned invalid output")
	}
	allocated := unsafe.Slice(output.Data, int(output.Size))
	result := bytes.Clone(allocated)
	if decrypt {
		wipe(allocated)
	}
	_, freeErr := windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	if freeErr != nil {
		wipe(result)
		return nil, errors.New("release Windows data protection output")
	}
	return result, nil
}

func providerEntropy(providerID string, version uint64) []byte {
	value := make([]byte, 0, 64+len(providerID))
	value = append(value, "cpa-cloud/key-provider/windows-dpapi-user/v1\x00"...)
	value = binary.BigEndian.AppendUint16(value, uint16(len(providerID)))
	value = append(value, providerID...)
	value = binary.BigEndian.AppendUint64(value, version)
	digest := sha256.Sum256(value)
	wipe(value)
	return bytes.Clone(digest[:])
}

func readProtectedFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= protectedHeaderSize || info.Size() > maxProtectedFileSize {
		return nil, errors.New("protected backup key path is not a supported regular file")
	}
	reparse, err := isReparsePoint(path)
	if err != nil {
		return nil, err
	}
	if reparse {
		return nil, errors.New("protected backup key path cannot be a reparse point")
	}
	return os.ReadFile(path)
}

func writeNewProtectedFile(root, target string, data []byte) (returnErr error) {
	temp, err := os.CreateTemp(root, ".cpa-cloud-key-provider-")
	if err != nil {
		return fmt.Errorf("create protected key temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := hardenPath(tempPath, false); err != nil {
		return fmt.Errorf("protect key temporary file permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write protected key temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync protected key temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close protected key temporary file: %w", err)
	}
	if err := os.Link(tempPath, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return errors.New("backup key provider version already exists")
		}
		return fmt.Errorf("publish protected key file: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		_ = os.Remove(target)
		return fmt.Errorf("sync protected key provider directory: %w", err)
	}
	return nil
}

func ensureProtectedDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("key provider store must be a real directory")
		}
		if reparse, err := isReparsePoint(path); err != nil || reparse {
			if err != nil {
				return err
			}
			return errors.New("key provider store cannot be a reparse point")
		}
		return hardenPath(path, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := validateExistingDirectoryChain(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if err := hardenPath(path, true); err != nil {
		_ = os.Remove(path)
		return err
	}
	return syncDirectory(parent)
}

func (s *Store) validateRoot() error {
	if s == nil || s.rootIdentity == nil {
		return ErrUnavailable
	}
	info, err := os.Lstat(s.root)
	if err != nil || !os.SameFile(s.rootIdentity, info) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("key provider store identity changed")
	}
	reparse, err := isReparsePoint(s.root)
	if err != nil {
		return err
	}
	if reparse {
		return errors.New("key provider store became a reparse point")
	}
	return nil
}

func validateExistingDirectoryChain(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(absolute, current)
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("key provider path contains a link or non-directory component")
		}
		reparse, err := isReparsePoint(current)
		if err != nil {
			return err
		}
		if reparse {
			return errors.New("key provider path contains a reparse point")
		}
	}
	return nil
}

func hardenPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	inheritance := uint32(0)
	if directory {
		mode = 0o700
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, 3)
	for _, trustee := range []struct {
		sid  *windows.SID
		kind windows.TRUSTEE_TYPE
	}{
		{user.User.Sid, windows.TRUSTEE_IS_USER},
		{system, windows.TRUSTEE_IS_USER},
		{admins, windows.TRUSTEE_IS_GROUP},
	} {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  trustee.kind,
				TrusteeValue: windows.TrusteeValueFromSID(trustee.sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func isReparsePoint(path string) (bool, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

func syncDirectory(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	err = windows.FlushFileBuffers(handle)
	if err == windows.ERROR_ACCESS_DENIED || err == windows.ERROR_INVALID_HANDLE || err == windows.ERROR_INVALID_FUNCTION {
		return nil
	}
	return err
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
