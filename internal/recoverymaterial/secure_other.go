//go:build !windows

package recoverymaterial

import (
	"os"
)

func hardenRecoveryPath(path string, directory bool) error {
	if directory {
		return os.Chmod(path, 0o700)
	}
	return os.Chmod(path, 0o600)
}

func recoveryPathIsReparsePoint(string) (bool, error) { return false, nil }

func syncRecoveryDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
