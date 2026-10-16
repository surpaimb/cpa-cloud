//go:build linux

package service

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var linuxSyncDirectory = unix.Fsync

var linuxReadSystemdCredential = readLinuxSystemdCredential

const (
	linuxACLXattrVersion = 0x0002
	linuxACLUserObj      = 0x01
	linuxACLUser         = 0x02
	linuxACLGroupObj     = 0x04
	linuxACLMask         = 0x10
	linuxACLOther        = 0x20
	linuxACLUndefinedID  = ^uint32(0)
	linuxACLRead         = 0x04
	linuxACLExecute      = 0x01
)

type linuxSystemdCredentialLayout uint8

const (
	linuxSystemdCredentialLayoutInvalid linuxSystemdCredentialLayout = iota
	linuxSystemdCredentialLayoutACL
	linuxSystemdCredentialLayoutOwnedReadOnly
)

func linuxSystemdMasterKeyProviderSupported() bool { return true }

func loadLinuxSystemdMasterKeyCredential() (masterKeyMaterial, error) {
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	encoded, err := linuxReadSystemdCredential(directory, masterKeyCredentialName, masterKeyCredentialMaxBytes)
	if err != nil {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	defer clearBytes(encoded)
	return parseSystemdMasterKeyCredential(encoded)
}

func readLinuxSystemdCredential(directory, name string, maxBytes int) ([]byte, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("systemd credential provider requires a non-root service user")
	}
	directoryFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directoryFD)

	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil || uint32(directoryStat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return nil, errors.New("invalid credential directory")
	}
	directoryACL, err := readLinuxACLXattr(directoryFD, "system.posix_acl_access")
	if err != nil {
		return nil, err
	}
	defaultACL, err := readLinuxACLXattr(directoryFD, "system.posix_acl_default")
	if err != nil || defaultACL != nil {
		return nil, errors.New("invalid credential directory ACL")
	}
	directoryReadOnly, err := linuxFileSystemReadOnly(directoryFD)
	if err != nil {
		return nil, err
	}
	directoryMountID, err := linuxMountID(directoryFD)
	if err != nil {
		return nil, err
	}
	layout := classifyLinuxSystemdCredentialObject(&directoryStat, directoryACL, true, directoryReadOnly)
	if layout == linuxSystemdCredentialLayoutInvalid {
		return nil, errors.New("invalid credential directory")
	}

	fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
	if err := unix.Fstat(fileFD, &first); err != nil || first.Size <= 0 || first.Size > int64(maxBytes) || first.Nlink != 1 {
		return nil, errors.New("invalid credential")
	}
	fileReadOnly, err := linuxFileSystemReadOnly(fileFD)
	if err != nil {
		return nil, err
	}
	fileMountID, err := linuxMountID(fileFD)
	if err != nil || first.Dev != directoryStat.Dev || fileMountID != directoryMountID {
		return nil, errors.New("invalid credential mount")
	}
	fileACL, err := readLinuxACLXattr(fileFD, "system.posix_acl_access")
	if err != nil || classifyLinuxSystemdCredentialObject(&first, fileACL, false, fileReadOnly) != layout {
		return nil, errors.New("invalid credential ACL")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxBytes {
		clearBytes(encoded)
		return nil, errors.New("read credential")
	}

	var second, named, currentDirectory unix.Stat_t
	secondFileACL, fileACLErr := readLinuxACLXattr(fileFD, "system.posix_acl_access")
	secondDirectoryACL, directoryACLErr := readLinuxACLXattr(directoryFD, "system.posix_acl_access")
	secondDefaultACL, defaultACLErr := readLinuxACLXattr(directoryFD, "system.posix_acl_default")
	secondDirectoryReadOnly, directoryReadOnlyErr := linuxFileSystemReadOnly(directoryFD)
	secondFileReadOnly, fileReadOnlyErr := linuxFileSystemReadOnly(fileFD)
	secondDirectoryMountID, directoryMountErr := linuxMountID(directoryFD)
	secondFileMountID, fileMountErr := linuxMountID(fileFD)
	if err := unix.Fstat(fileFD, &second); err != nil ||
		unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(directoryFD, &currentDirectory) != nil ||
		fileACLErr != nil || directoryACLErr != nil || defaultACLErr != nil ||
		directoryReadOnlyErr != nil || fileReadOnlyErr != nil || directoryMountErr != nil || fileMountErr != nil ||
		!sameLinuxCredentialObject(&first, &second) || !sameLinuxCredentialObject(&first, &named) ||
		!sameLinuxCredentialObject(&directoryStat, &currentDirectory) || int64(len(encoded)) != second.Size ||
		!bytes.Equal(fileACL, secondFileACL) || !bytes.Equal(directoryACL, secondDirectoryACL) ||
		secondDefaultACL != nil || directoryReadOnly != secondDirectoryReadOnly || fileReadOnly != secondFileReadOnly ||
		directoryMountID != secondDirectoryMountID || fileMountID != secondFileMountID ||
		second.Dev != currentDirectory.Dev || secondFileMountID != secondDirectoryMountID ||
		classifyLinuxSystemdCredentialObject(&second, secondFileACL, false, secondFileReadOnly) != layout ||
		classifyLinuxSystemdCredentialObject(&currentDirectory, secondDirectoryACL, true, secondDirectoryReadOnly) != layout {
		clearBytes(encoded)
		return nil, errors.New("credential changed")
	}
	return encoded, nil
}

func classifyLinuxSystemdCredentialObject(stat *unix.Stat_t, acl []byte, directory, readOnly bool) linuxSystemdCredentialLayout {
	if stat == nil || !directory && stat.Nlink != 1 || directory && uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || !directory && uint32(stat.Mode)&unix.S_IFMT != unix.S_IFREG {
		return linuxSystemdCredentialLayoutInvalid
	}
	euid, egid := uint32(os.Geteuid()), uint32(os.Getegid())
	mode, permission := uint32(stat.Mode)&0o7777, uint16(linuxACLRead)
	aclMode, ownedMode := uint32(0o440), uint32(0o400)
	if directory {
		permission = linuxACLRead | linuxACLExecute
		aclMode, ownedMode = 0o550, 0o500
	}
	if stat.Uid == 0 && stat.Gid == 0 && mode == aclMode && validLinuxSystemdCredentialACL(acl, euid, permission) {
		return linuxSystemdCredentialLayoutACL
	}
	if stat.Uid == euid && stat.Gid == egid && mode == ownedMode && acl == nil && readOnly {
		return linuxSystemdCredentialLayoutOwnedReadOnly
	}
	return linuxSystemdCredentialLayoutInvalid
}

func validLinuxSystemdCredentialACL(encoded []byte, euid uint32, permission uint16) bool {
	if len(encoded) != 4+5*8 || binary.LittleEndian.Uint32(encoded[:4]) != linuxACLXattrVersion {
		return false
	}
	expected := []struct {
		tag  uint16
		perm uint16
		id   uint32
	}{
		{linuxACLUserObj, permission, linuxACLUndefinedID},
		{linuxACLUser, permission, euid},
		{linuxACLGroupObj, 0, linuxACLUndefinedID},
		{linuxACLMask, permission, linuxACLUndefinedID},
		{linuxACLOther, 0, linuxACLUndefinedID},
	}
	for index, entry := range expected {
		offset := 4 + index*8
		if binary.LittleEndian.Uint16(encoded[offset:offset+2]) != entry.tag ||
			binary.LittleEndian.Uint16(encoded[offset+2:offset+4]) != entry.perm ||
			binary.LittleEndian.Uint32(encoded[offset+4:offset+8]) != entry.id {
			return false
		}
	}
	return true
}

func readLinuxACLXattr(fd int, name string) ([]byte, error) {
	size, err := unix.Fgetxattr(fd, name, nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
		return nil, nil
	}
	if err != nil || size <= 0 || size > 4096 {
		return nil, errors.New("read credential ACL")
	}
	encoded := make([]byte, size)
	read, err := unix.Fgetxattr(fd, name, encoded)
	if err != nil || read != size {
		return nil, errors.New("read credential ACL")
	}
	return encoded, nil
}

func linuxFileSystemReadOnly(fd int) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return false, err
	}
	return stat.Flags&unix.ST_RDONLY != 0, nil
}

func linuxMountID(fd int) (uint64, error) {
	// STATX_MNT_ID is required so a bind-mounted credential leaf cannot borrow
	// the directory mount's read-only result. Linux exposed it starting in 5.8.
	var stat unix.Statx_t
	if err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_STATX_SYNC_AS_STAT, unix.STATX_MNT_ID, &stat); err != nil || stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("read credential mount identity")
	}
	return stat.Mnt_id, nil
}

func sameLinuxCredentialObject(left, right *unix.Stat_t) bool {
	return sameLinuxIdentity(left, right) && left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid && left.Nlink == right.Nlink && left.Size == right.Size
}

func readLinuxMasterKeyBinding(dataDir, name string, maxBytes int) ([]byte, error) {
	return readLinuxPrivateFile(dataDir, name, maxBytes, 0o700, 0o600)
}

func createLinuxMasterKeyBinding(dataDir, name, stagingName string, encoded []byte, material *masterKeyMaterial) error {
	directoryFD, directoryStat, err := openLinuxPrivateDirectory(dataDir, 0o700)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	entries, err := linuxDirectoryEntryNames(directoryFD)
	if err != nil {
		return err
	}
	if len(entries) > 1 || len(entries) == 1 && entries[0] != stagingName {
		return errors.New("data directory is not fresh")
	}
	if len(entries) == 1 {
		staged, readErr := readLinuxStagedBindingAt(directoryFD, directoryStat, stagingName, masterKeyBindingMaxBytes)
		if readErr != nil {
			return readErr
		}
		if !json.Valid(staged) {
			truncated := truncatedMasterKeyBindingJSON(staged)
			clearBytes(staged)
			if !truncated {
				return errors.New("malformed staged binding")
			}
			if err := unix.Unlinkat(directoryFD, stagingName, 0); err != nil {
				return err
			}
			if err := linuxSyncDirectory(directoryFD); err != nil {
				return err
			}
			entries, err = linuxDirectoryEntryNames(directoryFD)
			if err != nil || len(entries) != 0 {
				return errors.New("data directory changed")
			}
		} else {
			var stagedBinding masterKeyBinding
			if decodeStrictJSON(staged, &stagedBinding) != nil || verifyMasterKeyBinding(staged, material) != nil {
				clearBytes(staged)
				return errors.New("staged binding does not match credential")
			}
			clearBytes(staged)
			if err := syncLinuxPrivateFileAt(directoryFD, directoryStat, stagingName, 0o600); err != nil {
				return err
			}
			return publishLinuxMasterKeyBinding(directoryFD, stagingName, name)
		}
	}
	if err := writeLinuxStagedBinding(directoryFD, stagingName, encoded); err != nil {
		return err
	}
	return publishLinuxMasterKeyBinding(directoryFD, stagingName, name)
}

func writeLinuxStagedBinding(directoryFD int, name string, encoded []byte) error {
	fileFD, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		unix.Close(fileFD)
		return errors.New("open staged binding")
	}
	removeIncomplete := true
	defer func() {
		_ = file.Close()
		if removeIncomplete {
			_ = unix.Unlinkat(directoryFD, name, 0)
			_ = linuxSyncDirectory(directoryFD)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil || !validLinuxPrivateFile(&fileStat, 0o600) {
		return errors.New("invalid binding file")
	}
	if err := file.Close(); err != nil {
		return err
	}
	var named unix.Stat_t
	if unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		!sameLinuxIdentity(&fileStat, &named) || !validLinuxPrivateFile(&named, 0o600) {
		return errors.New("staged binding changed")
	}
	removeIncomplete = false
	return nil
}

func publishLinuxMasterKeyBinding(directoryFD int, stagingName, name string) error {
	if err := unix.Renameat2(directoryFD, stagingName, directoryFD, name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	return linuxSyncDirectory(directoryFD)
}

func syncLinuxMasterKeyBinding(dataDir, name string) error {
	directoryFD, directoryStat, err := openLinuxPrivateDirectory(dataDir, 0o700)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	if err := syncLinuxPrivateFileAt(directoryFD, directoryStat, name, 0o600); err != nil {
		return err
	}
	return linuxSyncDirectory(directoryFD)
}

func syncLinuxPrivateFileAt(directoryFD int, directoryStat unix.Stat_t, name string, mode uint32) error {
	fileFD, err := unix.Openat(directoryFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		unix.Close(fileFD)
		return errors.New("open binding file")
	}
	var first unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &first); err != nil || !validLinuxPrivateFile(&first, mode) {
		file.Close()
		return errors.New("invalid binding file")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	var named, currentDirectory unix.Stat_t
	if unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(directoryFD, &currentDirectory) != nil ||
		!sameLinuxIdentity(&first, &named) || !validLinuxPrivateFile(&named, mode) ||
		currentDirectory.Dev != directoryStat.Dev || currentDirectory.Ino != directoryStat.Ino {
		return errors.New("binding changed")
	}
	return nil
}

func readLinuxPrivateFile(directory, name string, maxBytes int, directoryMode, fileMode uint32) ([]byte, error) {
	directoryFD, directoryStat, err := openLinuxPrivateDirectory(directory, directoryMode)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directoryFD)
	return readLinuxPrivateFileAt(directoryFD, directoryStat, name, maxBytes, fileMode)
}

func readLinuxPrivateFileAt(directoryFD int, directoryStat unix.Stat_t, name string, maxBytes int, fileMode uint32) ([]byte, error) {
	return readLinuxPrivateFileAtOptions(directoryFD, directoryStat, name, maxBytes, fileMode, false)
}

func readLinuxStagedBindingAt(directoryFD int, directoryStat unix.Stat_t, name string, maxBytes int) ([]byte, error) {
	return readLinuxPrivateFileAtOptions(directoryFD, directoryStat, name, maxBytes, 0o600, true)
}

func readLinuxPrivateFileAtOptions(directoryFD int, directoryStat unix.Stat_t, name string, maxBytes int, fileMode uint32, allowEmpty bool) ([]byte, error) {
	fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		unix.Close(fileFD)
		return nil, errors.New("open private file")
	}
	defer file.Close()
	var first unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &first); err != nil || !validLinuxPrivateFile(&first, fileMode) || first.Size < 0 || first.Size > int64(maxBytes) || !allowEmpty && first.Size == 0 {
		return nil, errors.New("invalid private file")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(encoded) > maxBytes || !allowEmpty && len(encoded) == 0 {
		clearBytes(encoded)
		return nil, errors.New("read private file")
	}
	var second, named, currentDirectory unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &second); err != nil ||
		unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		unix.Fstat(directoryFD, &currentDirectory) != nil ||
		!sameLinuxIdentity(&first, &second) || !sameLinuxIdentity(&first, &named) ||
		!validLinuxPrivateFile(&named, fileMode) || int64(len(encoded)) != second.Size ||
		currentDirectory.Dev != directoryStat.Dev || currentDirectory.Ino != directoryStat.Ino {
		clearBytes(encoded)
		return nil, errors.New("private file changed")
	}
	return encoded, nil
}

func truncatedMasterKeyBindingJSON(encoded []byte) bool {
	trimmed := bytes.TrimSpace(encoded)
	if len(trimmed) == 0 {
		return true
	}
	var value any
	err := json.Unmarshal(trimmed, &value)
	var syntaxError *json.SyntaxError
	return errors.As(err, &syntaxError) && syntaxError.Offset == int64(len(trimmed)) && syntaxError.Error() == "unexpected end of JSON input"
}

func linuxDirectoryEntryNames(directoryFD int) ([]string, error) {
	scanFD, err := unix.Openat(directoryFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	scan := os.NewFile(uintptr(scanFD), ".")
	if scan == nil {
		unix.Close(scanFD)
		return nil, errors.New("open directory scan")
	}
	defer scan.Close()
	return scan.Readdirnames(-1)
}

func openLinuxPrivateDirectory(path string, mode uint32) (int, unix.Stat_t, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || uint32(stat.Mode)&0o7777 != mode {
		unix.Close(fd)
		return -1, unix.Stat_t{}, errors.New("invalid private directory")
	}
	return fd, stat, nil
}

func validLinuxPrivateFile(stat *unix.Stat_t, mode uint32) bool {
	return uint32(stat.Mode)&unix.S_IFMT == unix.S_IFREG && stat.Uid == uint32(os.Geteuid()) && uint32(stat.Mode)&0o7777 == mode && stat.Nlink == 1
}

func sameLinuxIdentity(left, right *unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}
