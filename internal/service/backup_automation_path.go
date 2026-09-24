package service

// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type backupOutputRoot struct {
	path     string
	identity os.FileInfo
}

func openBackupOutputRoot(path string) (*backupOutputRoot, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("automated backup output root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("automated backup output root is invalid")
	}
	absolute = filepath.Clean(absolute)
	if filepath.Dir(absolute) == absolute {
		return nil, errors.New("automated backup output root cannot be a filesystem root")
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("automated backup output root must be a real directory")
		}
		if reparse, err := backupPathIsReparsePoint(absolute); err != nil || reparse {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("automated backup output root cannot be a reparse point")
		}
		if err := hardenBackupPath(absolute, true); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		parent := filepath.Dir(absolute)
		if err := validateBackupDirectoryChain(parent); err != nil {
			return nil, err
		}
		if err := os.Mkdir(absolute, 0o700); err != nil {
			return nil, err
		}
		if err := hardenBackupPath(absolute, true); err != nil {
			_ = os.Remove(absolute)
			return nil, err
		}
	}
	identity, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	return &backupOutputRoot{path: absolute, identity: identity}, nil
}

func (root *backupOutputRoot) validate() error {
	if root == nil || root.identity == nil {
		return errors.New("automated backup output root is unavailable")
	}
	current, err := os.Lstat(root.path)
	if err != nil || !os.SameFile(root.identity, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("automated backup output root identity changed")
	}
	if reparse, err := backupPathIsReparsePoint(root.path); err != nil || reparse {
		if err != nil {
			return err
		}
		return errors.New("automated backup output root became a reparse point")
	}
	return nil
}

func (root *backupOutputRoot) packagePath(name string) (string, error) {
	if err := root.validate(); err != nil {
		return "", err
	}
	if name == "" || filepath.Base(name) != name || filepath.Ext(name) != ".cpacb" || strings.ContainsAny(name, `/\`) {
		return "", errors.New("automated backup package name is invalid")
	}
	path := filepath.Join(root.path, name)
	relative, err := filepath.Rel(root.path, path)
	if err != nil || relative != name || strings.HasPrefix(relative, "..") {
		return "", errors.New("automated backup package escaped the output root")
	}
	return path, nil
}

func (root *backupOutputRoot) removeOwnedPackage(planID, runID, name string) error {
	wantPrefix := "cpa-cloud-" + planID + "-"
	wantSuffix := "-" + runID + ".cpacb"
	if !strings.HasPrefix(name, wantPrefix) || !strings.HasSuffix(name, wantSuffix) {
		return errors.New("backup package ownership cannot be proven")
	}
	path, err := root.packagePath(name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("owned backup path is not a regular file")
	}
	if reparse, err := backupPathIsReparsePoint(path); err != nil || reparse {
		if err != nil {
			return err
		}
		return errors.New("owned backup path is a reparse point")
	}
	if err := root.validate(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove owned backup package: %w", err)
	}
	return nil
}

func sameBackupPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func validateBackupDirectoryChain(path string) error {
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
			return errors.New("automated backup path contains a link or non-directory component")
		}
		if reparse, err := backupPathIsReparsePoint(current); err != nil || reparse {
			if err != nil {
				return err
			}
			return errors.New("automated backup path contains a reparse point")
		}
	}
	return nil
}
