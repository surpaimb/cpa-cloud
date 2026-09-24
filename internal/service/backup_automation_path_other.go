//go:build !windows

package service

// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.

import "os"

func backupPathIsReparsePoint(string) (bool, error) { return false, nil }

func hardenBackupPath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
