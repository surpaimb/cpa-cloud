package backup

// Independently authored deterministic Linux v3 publication regressions. Public
// synthetic material only; the validator delegates to the existing canary test.
import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func linuxSystemdPackage(t *testing.T) (string, string, string, KeyMaterial) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	db := openSyntheticDatabase(t, source)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	material := testSystemdKeyMaterial()
	writeSystemdBinding(t, source, material, "installation-master-key")
	input := filepath.Join(root, "input.cpacb")
	if _, err := CreateWithKeyMaterial(context.Background(), source, input, material, "deterministic-v3"); err != nil {
		t.Fatal(err)
	}
	return root, source, input, material
}
func linuxStagePath(t *testing.T, parent string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".cpa-cloud-restore-stage-") {
			if result != "" {
				t.Fatal("multiple restore stages")
			}
			result = filepath.Join(parent, entry.Name())
		}
	}
	if result == "" {
		t.Fatal("restore stage not found")
	}
	return result
}
func linuxMutation(t *testing.T, name, path, root string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	switch name {
	case "same-length-in-place":
		data[0] ^= 1
		err = os.WriteFile(path, data, 0600)
	case "same-bytes-new-inode", "changed-new-inode", "symlink":
		saved := filepath.Join(root, "original-staged-binding")
		if err = os.Rename(path, saved); err != nil {
			t.Fatal(err)
		}
		if name == "symlink" {
			err = os.Symlink(saved, path)
		} else {
			if name == "changed-new-inode" {
				data[0] ^= 1
			}
			err = os.WriteFile(path, data, 0600)
		}
	case "missing":
		err = os.Remove(path)
	case "truncated":
		err = os.Truncate(path, int64(len(data)-1))
	case "oversized":
		err = os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxSystemdBindingBytes+1), 0600)
	case "public-mode":
		err = os.Chmod(path, 0644)
	case "special-mode":
		err = unix.Chmod(path, 04600)
	case "multiple-links":
		err = os.Link(path, filepath.Join(root, "second-link"))
	case "wrong-owner":
		if os.Geteuid() != 0 {
			t.Skip("actual foreign-owner mutation requires root; metadata predicate is always tested")
		}
		err = os.Chown(path, 65534, os.Getegid())
	default:
		t.Fatal("unknown public mutation")
	}
	if err != nil {
		t.Fatal("public mutation failed")
	}
}
func TestLinuxSystemdRestoreRejectsActualBindingMutationInsideValidator(t *testing.T) {
	for _, mutation := range []string{"changed-new-inode", "same-length-in-place", "same-bytes-new-inode", "symlink", "missing", "truncated", "oversized", "public-mode", "special-mode", "multiple-links", "wrong-owner"} {
		t.Run(mutation, func(t *testing.T) {
			root, source, input, material := linuxSystemdPackage(t)
			original, _ := os.ReadFile(filepath.Join(source, systemdBindingFilename))
			packageBefore, _ := os.ReadFile(input)
			validator := material.ValidateInstallationBinding
			calls, injected := 0, false
			material.ValidateInstallationBinding = func(encoded []byte) error {
				calls++
				if calls == 2 {
					stage := linuxStagePath(t, root)
					actual, err := os.ReadFile(filepath.Join(stage, systemdBindingFilename))
					if err != nil || !bytes.Equal(actual, encoded) {
						t.Fatal("validator did not receive actual staged bytes")
					}
					linuxMutation(t, mutation, filepath.Join(stage, systemdBindingFilename), root)
					injected = true
				}
				return validator(encoded)
			}
			target := filepath.Join(root, "restored")
			_, err := RestoreWithKeyMaterial(context.Background(), input, target, material)
			if !injected || calls != 2 || err == nil {
				t.Fatal("actual staged mutation was not rejected at the final validator")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected restore published a target")
			}
			current, _ := os.ReadFile(filepath.Join(source, systemdBindingFilename))
			packageAfter, _ := os.ReadFile(input)
			if !bytes.Equal(original, current) || !bytes.Equal(packageBefore, packageAfter) {
				t.Fatal("restore changed original authenticated inputs")
			}
			if mutation == "changed-new-inode" || mutation == "same-bytes-new-inode" || mutation == "symlink" {
				saved, _ := os.ReadFile(filepath.Join(root, "original-staged-binding"))
				if !bytes.Equal(original, saved) || !errors.Is(err, errSystemdRestoreCleanup) {
					t.Fatal("original object or replaced-file cleanup evidence lost")
				}
			}
		})
	}
}
func TestLinuxSystemdRestorePublishesSanitizedDatabaseAndKeepsValidatorCalls(t *testing.T) {
	root, source, input, material := linuxSystemdPackage(t)
	validator, calls := material.ValidateInstallationBinding, 0
	material.ValidateInstallationBinding = func(encoded []byte) error { calls++; return validator(encoded) }
	target := filepath.Join(root, "restored")
	if _, err := RestoreWithKeyMaterial(context.Background(), input, target, material); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("installation validator call count changed")
	}
	db, err := sql.Open("sqlite", filepath.Join(target, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, `SELECT COUNT(*) FROM sessions`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM codex_oauth_sessions`, 0)
	assertCount(t, db, `SELECT COUNT(*) FROM employees`, 1)
	assertCount(t, db, `SELECT COUNT(*) FROM codex_oauth_refresh_states WHERE state='paused' AND reason_code='backup_restore_uncertain_refresh'`, 1)
	want, _ := os.ReadFile(filepath.Join(source, systemdBindingFilename))
	got, _ := os.ReadFile(filepath.Join(target, systemdBindingFilename))
	if !bytes.Equal(want, got) {
		t.Fatal("published binding is not the authenticated binding")
	}
}
func linuxPublication(t *testing.T) (*systemdRestoreStage, []byte, KeyMaterial) {
	t.Helper()
	parent := t.TempDir()
	identity, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSystemdRestoreStage(parent, filepath.Join(parent, "restored"), identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cleanup() })
	material := testSystemdKeyMaterial()
	// Use the same exclusive descriptor-bound staging as production.
	fixture := t.TempDir()
	writeSystemdBinding(t, fixture, material, "installation-master-key")
	binding, err := os.ReadFile(filepath.Join(fixture, systemdBindingFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.stageFile(systemdBindingFilename, binding); err != nil {
		t.Fatal(err)
	}
	if err := s.stageFile(databaseFilename, []byte("public-post-sanitization-database")); err != nil {
		t.Fatal(err)
	}
	return s, binding, material
}
func TestLinuxSystemdPublicationRejectsBindingChangedBeforeFinalRead(t *testing.T) {
	for _, mutation := range []string{"same-length-in-place", "same-bytes-new-inode", "symlink", "missing", "truncated", "public-mode", "multiple-links"} {
		t.Run(mutation, func(t *testing.T) {
			s, expected, material := linuxPublication(t)
			linuxMutation(t, mutation, filepath.Join(s.path, systemdBindingFilename), s.parentPath)
			if err := s.publish(context.Background(), expected, &material); err == nil {
				t.Fatal("pre-read staged mutation accepted")
			}
			if _, err := os.Lstat(filepath.Join(s.parentPath, s.target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pre-read rejection published target")
			}
		})
	}
}
func TestLinuxSystemdPublicationDeterministicHandoffs(t *testing.T) {
	for _, point := range []string{"before-target", "after-target", "during-copy", "after-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			s, expected, material := linuxPublication(t)
			mutate := func() error {
				linuxMutation(t, "changed-new-inode", filepath.Join(s.path, systemdBindingFilename), s.parentPath)
				return nil
			}
			switch point {
			case "before-target":
				s.ops.handoff = mutate
			case "after-target":
				s.ops.afterTarget = mutate
			case "during-copy":
				s.ops.copy = func(dst *os.File, src io.Reader, size int64) error {
					s.ops.copy = nil
					if err := mutate(); err != nil {
						return err
					}
					_, err := io.CopyN(dst, src, size)
					return err
				}
			case "after-directory-sync":
				s.ops.sync = func(fd int) error {
					if fd == int(s.parent.Fd()) {
						s.ops.sync = nil
						return mutate()
					}
					return unix.Fsync(fd)
				}
			}
			err := s.publish(context.Background(), expected, &material)
			if point == "after-directory-sync" {
				// This mutation is after the final source check: publication can
				// consume only the authenticated snapshot, never the replacement.
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := os.ReadFile(filepath.Join(s.parentPath, s.target, systemdBindingFilename))
				if readErr != nil || !bytes.Equal(data, expected) {
					t.Fatal("handoff published unauthenticated bytes")
				}
			} else {
				if err == nil {
					t.Fatal("handoff mutation accepted")
				}
				if _, err := os.Lstat(filepath.Join(s.parentPath, s.target)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("handoff error left a target")
				}
			}
		})
	}
}
func TestLinuxSystemdPublicationTargetAndParentInterleavings(t *testing.T) {
	for _, point := range []string{"existing-target", "parent-replaced", "target-replaced", "stage-replaced"} {
		t.Run(point, func(t *testing.T) {
			s, binding, material := linuxPublication(t)
			target := filepath.Join(s.parentPath, s.target)
			sentinel := filepath.Join(target, "foreign.sentinel")
			switch point {
			case "existing-target":
				s.ops.handoff = func() error {
					if err := os.Mkdir(target, 0700); err != nil {
						return err
					}
					return os.WriteFile(sentinel, []byte("public-existing"), 0600)
				}
			case "target-replaced":
				s.ops.afterTarget = func() error {
					if err := os.Rename(target, target+"-owned-before"); err != nil {
						return err
					}
					if err := os.Mkdir(target, 0700); err != nil {
						return err
					}
					return os.WriteFile(sentinel, []byte("public-existing"), 0600)
				}
			case "stage-replaced":
				sentinel = filepath.Join(s.path, "foreign.sentinel")
				s.ops.handoff = func() error {
					if err := os.Rename(s.path, s.path+"-owned-before"); err != nil {
						return err
					}
					if err := os.Mkdir(s.path, 0700); err != nil {
						return err
					}
					return os.WriteFile(sentinel, []byte("public-existing"), 0600)
				}
			case "parent-replaced":
				sentinel = filepath.Join(s.parentPath, "foreign.sentinel")
				s.ops.handoff = func() error {
					if err := os.Rename(s.parentPath, s.parentPath+"-owned-before"); err != nil {
						return err
					}
					t.Cleanup(func() { _ = s.cleanup(); _ = os.RemoveAll(s.parentPath + "-owned-before") })
					if err := os.Mkdir(s.parentPath, 0700); err != nil {
						return err
					}
					return os.WriteFile(sentinel, []byte("public-existing"), 0600)
				}
			}
			publishErr := s.publish(context.Background(), binding, &material)
			if publishErr == nil {
				t.Fatal("publication interleaving accepted")
			}
			cleanupErr := s.cleanup()
			if point == "target-replaced" && !errors.Is(publishErr, errSystemdRestoreCleanup) {
				t.Fatal("target cleanup failure lost")
			}
			if point == "stage-replaced" && !errors.Is(cleanupErr, errSystemdRestoreCleanup) {
				t.Fatal("stage cleanup failure lost")
			}
			if point == "parent-replaced" && cleanupErr != nil {
				t.Fatal("held-parent cleanup failed")
			}
			value, err := os.ReadFile(sentinel)
			if err != nil || string(value) != "public-existing" {
				t.Fatal("foreign object was changed or removed")
			}
		})
	}
}
func TestLinuxSystemdPublicationIOFailureAndCleanupAreIndependent(t *testing.T) {
	for _, point := range []string{"copy", "file-sync", "directory-sync", "parent-sync", "cancellation"} {
		t.Run(point, func(t *testing.T) {
			s, binding, material := linuxPublication(t)
			injected := errors.New("public injected I/O failure")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.ops.copy = func(dst *os.File, src io.Reader, size int64) error {
				if point == "copy" {
					_, _ = dst.Write([]byte("partial"))
					return injected
				}
				if point == "cancellation" {
					cancel()
				}
				_, err := io.CopyN(dst, src, size)
				return err
			}
			s.ops.sync = func(fd int) error {
				var st unix.Stat_t
				if err := unix.Fstat(fd, &st); err != nil {
					return err
				}
				if point == "file-sync" && st.Mode&unix.S_IFMT == unix.S_IFREG || point == "parent-sync" && fd == int(s.parent.Fd()) || point == "directory-sync" && st.Mode&unix.S_IFMT == unix.S_IFDIR && fd != int(s.parent.Fd()) {
					return injected
				}
				return unix.Fsync(fd)
			}
			err := s.publish(ctx, binding, &material)
			if point == "cancellation" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost")
				}
			} else if !errors.Is(err, injected) {
				t.Fatal("original I/O error lost")
			}
			if errors.Is(err, errSystemdRestoreCleanup) {
				t.Fatal("owned rollback unexpectedly incomplete")
			}
			if _, err := os.Lstat(filepath.Join(s.parentPath, s.target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("I/O failure left a target")
			}
		})
	}
}
func TestLinuxSystemdPublicationRejectsChangedOutputAtFinalSync(t *testing.T) {
	for _, mutation := range []string{"same-length-in-place", "same-bytes-new-inode", "symlink", "public-mode", "multiple-links"} {
		t.Run(mutation, func(t *testing.T) {
			s, expected, material := linuxPublication(t)
			injected := false
			s.ops.sync = func(fd int) error {
				if err := unix.Fsync(fd); err != nil {
					return err
				}
				if fd == int(s.parent.Fd()) {
					linuxMutation(t, mutation, filepath.Join(s.parentPath, s.target, systemdBindingFilename), s.parentPath)
					injected = true
				}
				return nil
			}
			err := s.publish(context.Background(), expected, &material)
			if !injected || err == nil {
				t.Fatal("changed published object accepted")
			}
			if mutation == "same-bytes-new-inode" || mutation == "symlink" {
				if !errors.Is(err, errSystemdRestoreCleanup) {
					t.Fatal("foreign output cleanup refusal lost")
				}
				if _, err := os.Lstat(filepath.Join(s.parentPath, s.target, systemdBindingFilename)); err != nil {
					t.Fatal("foreign output entry was deleted")
				}
			} else if _, err := os.Lstat(filepath.Join(s.parentPath, s.target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("owned output rollback failed")
			}
		})
	}
}
func TestLinuxSystemdPrivateMetadataPredicates(t *testing.T) {
	base := unix.Stat_t{Mode: unix.S_IFREG | 0600, Uid: uint32(os.Geteuid()), Gid: uint32(os.Getegid()), Nlink: 1, Size: 20}
	if !systemdPrivateFile(&base, 20) {
		t.Fatal("private metadata rejected")
	}
	for _, change := range []func(*unix.Stat_t){func(s *unix.Stat_t) { s.Uid++ }, func(s *unix.Stat_t) { s.Gid++ }, func(s *unix.Stat_t) { s.Nlink++ }, func(s *unix.Stat_t) { s.Mode |= 0040 }, func(s *unix.Stat_t) { s.Mode |= 04000 }, func(s *unix.Stat_t) { s.Mode = unix.S_IFDIR | 0600 }, func(s *unix.Stat_t) { s.Size = 21 }} {
		stat := base
		change(&stat)
		if systemdPrivateFile(&stat, 20) {
			t.Fatal("invalid private metadata accepted")
		}
	}
}
func TestLinuxSystemdPublicationParentNamespaceProtection(t *testing.T) {
	parent := t.TempDir()
	identity, err := os.Lstat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := newSystemdRestoreStage(parent, filepath.Join(parent, "restored"), identity); err == nil {
		t.Fatal("unprotected parent accepted")
	}
	if err := unix.Chmod(parent, 01777); err != nil {
		t.Fatal(err)
	}
	s, err := newSystemdRestoreStage(parent, filepath.Join(parent, "restored"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
}
