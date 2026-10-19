//go:build !linux

package backup

import (
	"context"
	"os"
	"path/filepath"
)

// Preserve existing non-Linux fixture behavior; this correction hardens Linux
// v3 only, without refactoring password v1 or Windows key-material v2.
type systemdRestoreStage struct{ path, target string }

func newSystemdRestoreStage(parent, target string, _ os.FileInfo) (*systemdRestoreStage, error) {
	path, err := makeSecureTempDir(parent, ".cpa-cloud-restore-stage-")
	return &systemdRestoreStage{path: path, target: target}, err
}
func (s *systemdRestoreStage) stageFile(name string, data []byte) error {
	return writeExclusiveFile(filepath.Join(s.path, name), data, 0600)
}
func (s *systemdRestoreStage) cleanup() error {
	removeKnownDirectory(s.path, databaseFilename, systemdBindingFilename)
	return nil
}
func (s *systemdRestoreStage) publish(ctx context.Context, binding []byte, material *KeyMaterial) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSystemdBinding(binding, material); err != nil {
		return err
	}
	return publishRestoreDirectoryFiles(s.path, s.target, databaseFilename, systemdBindingFilename)
}
