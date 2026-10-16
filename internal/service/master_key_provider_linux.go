//go:build linux

package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

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

func createLinuxMasterKeyBinding(dataDir, name string, encoded []byte) error {
	directoryFD, directoryStat, err := openLinuxPrivateDirectory(dataDir, 0o700)
	if err != nil {
		return err
	}
	defer unix.Close(directoryFD)
	fileFD, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fileFD), name)
	if file == nil {
		unix.Close(fileFD)
		return errors.New("open binding file")
	}
	removeIncomplete := true
	defer func() {
		_ = file.Close()
		if removeIncomplete {
			_ = unix.Unlinkat(directoryFD, name, 0)
			_ = unix.Fsync(directoryFD)
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
	var currentDirectory unix.Stat_t
	if err := unix.Fstat(directoryFD, &currentDirectory); err != nil || currentDirectory.Dev != directoryStat.Dev || currentDirectory.Ino != directoryStat.Ino {
		return errors.New("binding directory changed")
	}
	// The fully written and file-synced binding is safe to validate on retry even
	// if the directory fsync below reports an error. Removing it at that point
	// would introduce a second, likewise-unsynced namespace operation.
	removeIncomplete = false
	if err := unix.Fsync(directoryFD); err != nil {
		return err
	}
	return nil
}

func readLinuxPrivateFile(directory, name string, maxBytes int, directoryMode, fileMode uint32) ([]byte, error) {
	directoryFD, directoryStat, err := openLinuxPrivateDirectory(directory, directoryMode)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directoryFD)
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
