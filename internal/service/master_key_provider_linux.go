//go:build linux

package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var linuxSyncDirectory = unix.Fsync

func linuxSystemdMasterKeyProviderSupported() bool { return true }

func loadLinuxSystemdMasterKeyCredential() (masterKeyMaterial, error) {
	directory := os.Getenv("CREDENTIALS_DIRECTORY")
	if directory == "" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	encoded, err := readLinuxPrivateFile(directory, masterKeyCredentialName, masterKeyCredentialMaxBytes, 0o700, 0o400)
	if err != nil {
		return masterKeyMaterial{}, errMasterKeyProviderRejected
	}
	defer clearBytes(encoded)
	return parseSystemdMasterKeyCredential(encoded)
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
		staged, readErr := readLinuxPrivateFileAt(directoryFD, directoryStat, stagingName, masterKeyBindingMaxBytes, 0o600)
		var stagedBinding masterKeyBinding
		complete := readErr == nil && decodeStrictJSON(staged, &stagedBinding) == nil
		valid := complete && verifyMasterKeyBinding(staged, material) == nil
		clearBytes(staged)
		if valid {
			if err := syncLinuxPrivateFileAt(directoryFD, directoryStat, stagingName, 0o600); err != nil {
				return err
			}
			return publishLinuxMasterKeyBinding(directoryFD, stagingName, name)
		}
		if complete {
			return errors.New("staged binding does not match credential")
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
	if err := unix.Fstat(int(file.Fd()), &first); err != nil || !validLinuxPrivateFile(&first, fileMode) || first.Size <= 0 || first.Size > int64(maxBytes) {
		return nil, errors.New("invalid private file")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxBytes {
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
