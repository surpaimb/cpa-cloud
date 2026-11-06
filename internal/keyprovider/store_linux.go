//go:build linux

package keyprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	linuxBackupCredentialName     = "cpa-cloud-backup-wrapping-key"
	linuxBackupCredentialMaxBytes = 4096
	linuxACLVersion               = 0x0002
	linuxACLUserObj               = 0x01
	linuxACLUser                  = 0x02
	linuxACLGroupObj              = 0x04
	linuxACLMask                  = 0x10
	linuxACLOther                 = 0x20
	linuxACLUndefinedID           = ^uint32(0)
	linuxACLRead                  = 0x04
	linuxACLExecute               = 0x01
)

var linuxReadBackupCredential = readLinuxCredential

type linuxCredential struct {
	FormatVersion uint64 `json:"format_version"`
	ProviderKind  string `json:"provider_kind"`
	ProviderID    string `json:"provider_id"`
	InstanceID    string `json:"instance_id"`
	KeyID         string `json:"key_id"`
	KeyVersion    uint64 `json:"key_version"`
	WrappingKey   string `json:"wrapping_key"`
}

type Store struct {
	binding   InstanceBinding
	reference Reference
	reason    string
}

type PreparedVersion struct{}

func Open(string) (*Store, error) {
	return &Store{reason: ReasonUnsupported}, nil
}

func OpenExisting(string) (*Store, error) { return Open("") }

func OpenForInstance(_ string, binding InstanceBinding) (*Store, error) {
	store := &Store{binding: binding, reference: Reference{Kind: KindLinuxSystemdCredentialV1}, reason: "systemd_credential_rejected"}
	var digestNonzero byte
	for _, value := range binding.ExcludedKeyDigest {
		digestNonzero |= value
	}
	if !validInstanceID(binding.InstanceID) || !validKeyID(binding.ExcludedKeyID) || subtle.ConstantTimeByteEq(digestNonzero, 0) == 1 {
		return store, nil
	}
	material, err := loadLinuxMaterial(binding)
	if err != nil {
		return store, nil
	}
	defer material.Destroy()
	store.reference = Reference{
		ProviderID: material.ProviderID, Kind: material.Kind, InstanceID: material.InstanceID,
		KeyID: material.KeyID, Version: material.Version,
	}
	store.reason = ReasonReady
	return store, nil
}

func (s *Store) ProviderKind() string { return KindLinuxSystemdCredentialV1 }

func (s *Store) Ready() (bool, string) {
	if s == nil || s.reason != ReasonReady {
		if s == nil || s.reason == "" {
			return false, ReasonUnsupported
		}
		return false, s.reason
	}
	material, err := s.Resolve(context.Background(), s.reference.ProviderID, s.reference.Version)
	if err != nil {
		return false, "systemd_credential_rejected"
	}
	material.Destroy()
	return true, ReasonReady
}

func (s *Store) ConfiguredReference() (Reference, bool) {
	if ready, _ := s.Ready(); !ready {
		return Reference{}, false
	}
	return s.reference, true
}

func (s *Store) Resolve(ctx context.Context, providerID string, version uint64) (*Material, error) {
	if s == nil || s.reason != ReasonReady || ctx == nil {
		return nil, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if providerID != s.reference.ProviderID || version != s.reference.Version {
		return nil, ErrUnavailable
	}
	material, err := loadLinuxMaterial(s.binding)
	if err != nil {
		return nil, ErrUnavailable
	}
	current := Reference{ProviderID: material.ProviderID, Kind: material.Kind, InstanceID: material.InstanceID, KeyID: material.KeyID, Version: material.Version}
	if current != s.reference {
		material.Destroy()
		return nil, ErrUnavailable
	}
	return material, nil
}

func (s *Store) PrepareVersion(context.Context, string, uint64) (*PreparedVersion, error) {
	return nil, ErrUnavailable
}

func (s *Store) PrepareMaterial(context.Context, Material) (*PreparedVersion, error) {
	return nil, ErrUnavailable
}

func (s *Store) DiscardVersion(context.Context, string, uint64) error { return ErrUnavailable }

func (p *PreparedVersion) Commit()         {}
func (p *PreparedVersion) Rollback() error { return errors.New("no key-provider version was prepared") }

func loadLinuxMaterial(binding InstanceBinding) (*Material, error) {
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrUnavailable
	}
	encoded, err := linuxReadBackupCredential(directory, linuxBackupCredentialName, linuxBackupCredentialMaxBytes)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer wipe(encoded)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var credential linuxCredential
	if decoder.Decode(&credential) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, ErrUnavailable
	}
	if credential.FormatVersion != 1 || credential.ProviderKind != KindLinuxSystemdCredentialV1 ||
		!validProviderID(credential.ProviderID) || !validInstanceID(credential.InstanceID) ||
		!validKeyID(credential.KeyID) || !validVersion(credential.KeyVersion) ||
		!strings.EqualFold(credential.InstanceID, binding.InstanceID) || credential.KeyID == binding.ExcludedKeyID {
		return nil, ErrUnavailable
	}
	key, err := base64.RawStdEncoding.DecodeString(credential.WrappingKey)
	if err != nil || len(key) != 32 {
		wipe(key)
		return nil, ErrUnavailable
	}
	digest := sha256.Sum256(key)
	if subtle.ConstantTimeCompare(digest[:], binding.ExcludedKeyDigest[:]) == 1 {
		wipe(key)
		return nil, ErrUnavailable
	}
	material := &Material{
		ProviderID: credential.ProviderID, Kind: credential.ProviderKind,
		InstanceID: strings.ToLower(credential.InstanceID), KeyID: credential.KeyID, Version: credential.KeyVersion,
	}
	copy(material.Key[:], key)
	wipe(key)
	if validateMaterial(material) != nil {
		material.Destroy()
		return nil, ErrUnavailable
	}
	return material, nil
}

func readLinuxCredential(directory, name string, maxBytes int) ([]byte, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("systemd credential requires a non-root service user")
	}
	dirFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	var dirStat unix.Stat_t
	if unix.Fstat(dirFD, &dirStat) != nil || uint32(dirStat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return nil, errors.New("invalid credential directory")
	}
	dirACL, err := linuxACL(dirFD, "system.posix_acl_access")
	if err != nil {
		return nil, err
	}
	defaultACL, err := linuxACL(dirFD, "system.posix_acl_default")
	if err != nil || defaultACL != nil {
		return nil, errors.New("invalid credential directory ACL")
	}
	dirRO, err := linuxReadOnly(dirFD)
	if err != nil {
		return nil, err
	}
	dirMount, err := linuxCredentialMountID(dirFD)
	if err != nil || !linuxCredentialObject(&dirStat, dirACL, true, dirRO) {
		return nil, errors.New("invalid credential directory")
	}
	fileFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		unix.Close(fileFD)
		return nil, errors.New("open credential")
	}
	defer file.Close()
	var first unix.Stat_t
	if unix.Fstat(fileFD, &first) != nil || first.Size <= 0 || first.Size > int64(maxBytes) || first.Nlink != 1 {
		return nil, errors.New("invalid credential")
	}
	fileACL, err := linuxACL(fileFD, "system.posix_acl_access")
	if err != nil {
		return nil, err
	}
	fileRO, err := linuxReadOnly(fileFD)
	if err != nil {
		return nil, err
	}
	fileMount, err := linuxCredentialMountID(fileFD)
	if err != nil || first.Dev != dirStat.Dev || fileMount != dirMount || !linuxCredentialObject(&first, fileACL, false, fileRO) {
		return nil, errors.New("invalid credential")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxBytes {
		wipe(encoded)
		return nil, errors.New("read credential")
	}
	var second, named, currentDir unix.Stat_t
	secondFileACL, fileACLErr := linuxACL(fileFD, "system.posix_acl_access")
	secondDirACL, dirACLErr := linuxACL(dirFD, "system.posix_acl_access")
	secondDefaultACL, defaultErr := linuxACL(dirFD, "system.posix_acl_default")
	secondDirRO, dirROErr := linuxReadOnly(dirFD)
	secondFileRO, fileROErr := linuxReadOnly(fileFD)
	secondDirMount, dirMountErr := linuxCredentialMountID(dirFD)
	secondFileMount, fileMountErr := linuxCredentialMountID(fileFD)
	if unix.Fstat(fileFD, &second) != nil || unix.Fstatat(dirFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(dirFD, &currentDir) != nil || fileACLErr != nil || dirACLErr != nil || defaultErr != nil ||
		dirROErr != nil || fileROErr != nil || dirMountErr != nil || fileMountErr != nil ||
		!sameLinuxObject(&first, &second) || !sameLinuxObject(&first, &named) || !sameLinuxObject(&dirStat, &currentDir) ||
		int64(len(encoded)) != second.Size || !bytes.Equal(fileACL, secondFileACL) || !bytes.Equal(dirACL, secondDirACL) ||
		secondDefaultACL != nil || dirRO != secondDirRO || fileRO != secondFileRO || dirMount != secondDirMount ||
		fileMount != secondFileMount || second.Dev != currentDir.Dev || secondFileMount != secondDirMount ||
		!linuxCredentialObject(&second, secondFileACL, false, secondFileRO) || !linuxCredentialObject(&currentDir, secondDirACL, true, secondDirRO) {
		wipe(encoded)
		return nil, errors.New("credential changed")
	}
	return encoded, nil
}

func linuxCredentialObject(stat *unix.Stat_t, acl []byte, directory, readOnly bool) bool {
	if stat == nil || (directory && uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR) || (!directory && (uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1)) {
		return false
	}
	perm, rootMode, ownedMode := uint16(linuxACLRead), uint32(0o440), uint32(0o400)
	if directory {
		perm, rootMode, ownedMode = linuxACLRead|linuxACLExecute, 0o550, 0o500
	}
	mode := uint32(stat.Mode) & 0o7777
	if stat.Uid == 0 && stat.Gid == 0 && mode == rootMode && validLinuxACL(acl, uint32(os.Geteuid()), perm) {
		return true
	}
	return stat.Uid == uint32(os.Geteuid()) && stat.Gid == uint32(os.Getegid()) && mode == ownedMode && acl == nil && readOnly
}

func validLinuxACL(encoded []byte, euid uint32, permission uint16) bool {
	if len(encoded) != 44 || binary.LittleEndian.Uint32(encoded[:4]) != linuxACLVersion {
		return false
	}
	expected := [][3]uint64{{linuxACLUserObj, uint64(permission), uint64(linuxACLUndefinedID)}, {linuxACLUser, uint64(permission), uint64(euid)}, {linuxACLGroupObj, 0, uint64(linuxACLUndefinedID)}, {linuxACLMask, uint64(permission), uint64(linuxACLUndefinedID)}, {linuxACLOther, 0, uint64(linuxACLUndefinedID)}}
	for index, item := range expected {
		offset := 4 + index*8
		if uint64(binary.LittleEndian.Uint16(encoded[offset:])) != item[0] || uint64(binary.LittleEndian.Uint16(encoded[offset+2:])) != item[1] || uint64(binary.LittleEndian.Uint32(encoded[offset+4:])) != item[2] {
			return false
		}
	}
	return true
}

func linuxACL(fd int, name string) ([]byte, error) {
	size, err := unix.Fgetxattr(fd, name, nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil || size <= 0 || size > 4096 {
		return nil, errors.New("read credential ACL")
	}
	value := make([]byte, size)
	read, err := unix.Fgetxattr(fd, name, value)
	if err != nil || read != size {
		return nil, errors.New("read credential ACL")
	}
	return value, nil
}

func linuxReadOnly(fd int) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return false, err
	}
	return stat.Flags&unix.ST_RDONLY != 0, nil
}

func linuxCredentialMountID(fd int) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_STATX_SYNC_AS_STAT, unix.STATX_MNT_ID, &stat); err != nil || stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("read credential mount identity")
	}
	return stat.Mnt_id, nil
}

func sameLinuxObject(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid && left.Nlink == right.Nlink && left.Size == right.Size
}
