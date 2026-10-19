package backup

// Independently authored Linux v3 publication. See docs/linux-cross-vm-backup-contract.md.
// No file is renamed from a re-resolved staged pathname into the target.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var errSystemdRestoreCleanup = ErrSystemdRestoreCleanupIncomplete

type systemdPublishOps struct {
	// Per-operation seams for deterministic tests, never global mutable hooks.
	handoff, afterTarget func() error
	sync                 func(int) error
	copy                 func(*os.File, io.Reader, int64) error
}
type systemdRestoreStage struct {
	path, parentPath, name, target string
	parent, dir                    *os.File
	identity                       unix.Stat_t
	owned                          map[string]unix.Stat_t
	ops                            systemdPublishOps
	cleanupDone                    bool
	cleanupErr                     error
}
type systemdPinnedFile struct {
	file *os.File
	stat unix.Stat_t
}

func systemdSameObject(a, b *unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }
func systemdPrivateFile(s *unix.Stat_t, max int64) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Mode&07777 == 0600 &&
		s.Uid == uint32(os.Geteuid()) && s.Gid == uint32(os.Getegid()) && s.Nlink == 1 && s.Size >= 0 && s.Size <= max
}
func systemdStableFile(a, b *unix.Stat_t) bool {
	return systemdSameObject(a, b) && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid &&
		a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func systemdDirStat(file *os.File) (unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstat(int(file.Fd()), &stat)
	if err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&07777 != 0700 ||
		stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid())) {
		err = errors.New("restore directory is not private and owned")
	}
	return stat, err
}
func systemdSafeParent(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	// A non-sticky writable namespace would let another UID exchange even
	// private directory names. Root/current-UID sticky parents remain usable.
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) ||
		(stat.Mode&0022 != 0 && stat.Mode&unix.S_ISVTX == 0) {
		return errors.New("restore parent namespace is not protected")
	}
	return nil
}
func systemdOpenDirAt(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "restore directory"), nil
}
func newSystemdRestoreStage(parent, target string, identity os.FileInfo) (*systemdRestoreStage, error) {
	p, err := systemdOpenDirAt(unix.AT_FDCWD, parent)
	if err != nil {
		return nil, err
	}
	info, err := p.Stat()
	if err != nil || !os.SameFile(identity, info) {
		p.Close()
		return nil, errors.New("restore parent changed before staging")
	}
	if err := systemdSafeParent(p); err != nil {
		p.Close()
		return nil, err
	}
	name := ".cpa-cloud-restore-stage-" + rand.Text()
	if err := unix.Mkdirat(int(p.Fd()), name, 0700); err != nil {
		p.Close()
		return nil, err
	}
	d, err := systemdOpenDirAt(int(p.Fd()), name)
	if err != nil {
		p.Close()
		return nil, errors.Join(err, errSystemdRestoreCleanup)
	}
	stat, err := systemdDirStat(d)
	if err != nil {
		cleanupErr := systemdRemoveOwned(p, d, name, &stat, nil)
		return nil, errors.Join(err, cleanupErr, d.Close(), p.Close())
	}
	return &systemdRestoreStage{path: filepath.Join(parent, name), parentPath: parent, name: name,
		target: filepath.Base(target), parent: p, dir: d, identity: stat, owned: make(map[string]unix.Stat_t)}, nil
}
func (s *systemdRestoreStage) stageFile(name string, data []byte) error {
	return s.write(s.dir, name, bytes.NewReader(data), int64(len(data)), s.owned)
}
func (s *systemdRestoreStage) directories() error {
	if err := systemdSafeParent(s.parent); err != nil {
		return err
	}
	if err := validateExistingDirectoryChain(s.parentPath); err != nil {
		return err
	}
	current, err := os.Lstat(s.parentPath)
	pinned, statErr := s.parent.Stat()
	if err != nil || statErr != nil || !os.SameFile(current, pinned) {
		return errors.New("restore parent changed during publication")
	}
	var entry unix.Stat_t
	if err := unix.Fstatat(int(s.parent.Fd()), s.name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	stat, err := systemdDirStat(s.dir)
	if err != nil || !systemdSameObject(&s.identity, &entry) || !systemdSameObject(&s.identity, &stat) {
		return errors.New("restore stage changed during publication")
	}
	return nil
}
func (s *systemdRestoreStage) pin(name string, max int64) (*systemdPinnedFile, error) {
	fd, err := unix.Openat(int(s.dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	p := &systemdPinnedFile{file: os.NewFile(uintptr(fd), "staged restore file")}
	if err := unix.Fstat(fd, &p.stat); err != nil {
		p.file.Close()
		return nil, err
	}
	owned, exists := s.owned[name]
	if !exists || !systemdSameObject(&owned, &p.stat) || !systemdPrivateFile(&p.stat, max) {
		p.file.Close()
		return nil, errors.New("staged restore file changed before authentication")
	}
	return p, nil
}
func (s *systemdRestoreStage) check(name string, p *systemdPinnedFile) error {
	var stat, entry unix.Stat_t
	if err := unix.Fstat(int(p.file.Fd()), &stat); err != nil {
		return err
	}
	if err := unix.Fstatat(int(s.dir.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !systemdStableFile(&p.stat, &stat) || !systemdStableFile(&p.stat, &entry) {
		return errors.New("staged restore file changed during authentication")
	}
	return nil
}
func (s *systemdRestoreStage) binding(p *systemdPinnedFile, expected []byte) ([]byte, error) {
	data, err := io.ReadAll(io.NewSectionReader(p.file, 0, maxSystemdBindingBytes+1))
	if err == nil {
		err = s.check(systemdBindingFilename, p)
	}
	if err == nil && (int64(len(data)) != p.stat.Size || len(data) > maxSystemdBindingBytes || subtle.ConstantTimeCompare(data, expected) != 1) {
		err = errors.New("actual staged binding differs from authenticated package")
	}
	if err != nil {
		wipe(data)
		return nil, err
	}
	return data, nil
}
func (s *systemdRestoreStage) sync(fd int) error {
	if s.ops.sync != nil {
		return s.ops.sync(fd)
	}
	return unix.Fsync(fd)
}
func (s *systemdRestoreStage) write(dir *os.File, name string, data io.Reader, size int64, owned map[string]unix.Stat_t) (retErr error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "published restore file")
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if !systemdPrivateFile(&stat, 0) {
		return errors.New("new restore file is not private and exclusive")
	}
	owned[name] = stat
	if s.ops.copy != nil {
		err = s.ops.copy(f, data, size)
	} else {
		_, err = io.CopyN(f, data, size)
	}
	if err != nil {
		return err
	}
	if err := s.sync(fd); err != nil {
		return err
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if !systemdPrivateFile(&stat, size) || stat.Size != size {
		return errors.New("published restore file has invalid metadata")
	}
	owned[name] = stat
	return nil
}

func systemdCheckPublished(dir *os.File, owned map[string]unix.Stat_t, binding []byte) (retErr error) {
	for name, expected := range owned {
		var entry unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if !systemdStableFile(&expected, &entry) {
			return errors.New("published restore object changed")
		}
	}
	fd, err := unix.Openat(int(dir.Fd()), systemdBindingFilename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "published binding")
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	var before, after, entry unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return err
	}
	expected := owned[systemdBindingFilename]
	if !systemdStableFile(&expected, &before) {
		return errors.New("published binding identity changed")
	}
	data, err := io.ReadAll(io.NewSectionReader(f, 0, maxSystemdBindingBytes+1))
	defer wipe(data)
	if err != nil {
		return err
	}
	if err := unix.Fstat(fd, &after); err != nil {
		return err
	}
	if err := unix.Fstatat(int(dir.Fd()), systemdBindingFilename, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !systemdStableFile(&before, &after) || !systemdStableFile(&before, &entry) || subtle.ConstantTimeCompare(data, binding) != 1 {
		return errors.New("published binding content or identity changed")
	}
	return nil
}
func systemdRemoveOwned(parent, dir *os.File, name string, identity *unix.Stat_t, owned map[string]unix.Stat_t) error {
	var problems []error
	for file, expected := range owned {
		var current unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), file, &current, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil || !systemdSameObject(&expected, &current) {
			problems = append(problems, errors.New("restore cleanup refused a replaced file"))
			continue
		}
		if err := unix.Unlinkat(int(dir.Fd()), file, 0); err != nil {
			problems = append(problems, err)
		}
	}
	var current unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil || systemdSafeParent(parent) != nil || !systemdSameObject(identity, &current) {
		problems = append(problems, errors.New("restore cleanup refused a replaced directory"))
	} else if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		problems = append(problems, err)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		problems = append(problems, err)
	}
	if len(problems) != 0 {
		return errors.Join(errSystemdRestoreCleanup, errors.Join(problems...))
	}
	return nil
}
func (s *systemdRestoreStage) cleanup() error {
	if s.cleanupDone {
		return s.cleanupErr
	}
	s.cleanupDone = true
	err := errors.Join(systemdRemoveOwned(s.parent, s.dir, s.name, &s.identity, s.owned), s.dir.Close(), s.parent.Close())
	if err != nil {
		s.cleanupErr = errors.Join(errSystemdRestoreCleanup, err)
	}
	return s.cleanupErr
}
func (s *systemdRestoreStage) publish(ctx context.Context, expected []byte, material *KeyMaterial) (retErr error) {
	if err := s.directories(); err != nil {
		return err
	}
	binding, err := s.pin(systemdBindingFilename, maxSystemdBindingBytes)
	if err != nil {
		return err
	}
	bindingClosed := false
	defer func() {
		if !bindingClosed {
			retErr = errors.Join(retErr, binding.file.Close())
		}
	}()
	database, err := s.pin(databaseFilename, maxDatabaseBytes)
	if err != nil {
		return err
	}
	databaseClosed := false
	defer func() {
		if !databaseClosed {
			retErr = errors.Join(retErr, database.file.Close())
		}
	}()
	actual, err := s.binding(binding, expected)
	if err != nil {
		return err
	}
	defer wipe(actual)
	// This is the original final installation/canary validator, now given the
	// actual bounded staged bytes. Its number/order of calls is unchanged.
	if err := validateSystemdBinding(actual, material); err != nil {
		return err
	}
	after, err := s.binding(binding, expected)
	wipe(after)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("binding validator changed its input")
	}
	if err := s.check(databaseFilename, database); err != nil {
		return err
	}
	if s.ops.handoff != nil {
		if err := s.ops.handoff(); err != nil {
			return err
		}
	}
	if err := s.directories(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Mkdirat(int(s.parent.Fd()), s.target, 0700); err != nil {
		return err
	}
	target, err := systemdOpenDirAt(int(s.parent.Fd()), s.target)
	if err != nil {
		return errors.Join(err, errSystemdRestoreCleanup)
	}
	identity, err := systemdDirStat(target)
	owned := make(map[string]unix.Stat_t)
	// Roll back only entries with held identities, never replacement objects.
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, systemdRemoveOwned(s.parent, target, s.target, &identity, owned))
		}
		if closeErr := target.Close(); closeErr != nil {
			retErr = errors.Join(retErr, errSystemdRestoreCleanup, closeErr)
		}
	}()
	if err != nil {
		return err
	}
	if s.ops.afterTarget != nil {
		if err := s.ops.afterTarget(); err != nil {
			return err
		}
	}
	checkTarget := func() error {
		var entry unix.Stat_t
		if err := unix.Fstatat(int(s.parent.Fd()), s.target, &entry, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		current, err := systemdDirStat(target)
		if err != nil || !systemdSameObject(&identity, &current) || !systemdSameObject(&identity, &entry) {
			return errors.New("restore target changed during publication")
		}
		return s.directories()
	}
	if err := checkTarget(); err != nil {
		return err
	}
	if err := s.write(target, databaseFilename, io.NewSectionReader(database.file, 0, database.stat.Size), database.stat.Size, owned); err != nil {
		return err
	}
	if err := s.check(databaseFilename, database); err != nil {
		return err
	}
	if err := s.write(target, systemdBindingFilename, bytes.NewReader(actual), int64(len(actual)), owned); err != nil {
		return err
	}
	// A handoff mutation cannot change the copied binding, and is rejected as
	// well. Do not rebuild the database from the unsanitized package bytes.
	after, err = s.binding(binding, expected)
	wipe(after)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkTarget(); err != nil {
		return err
	}
	if err := systemdCheckPublished(target, owned, actual); err != nil {
		return err
	}
	closeErr := errors.Join(binding.file.Close(), database.file.Close())
	bindingClosed, databaseClosed = true, true
	if closeErr != nil {
		return closeErr
	}
	if err := s.sync(int(target.Fd())); err != nil {
		return err
	}
	if err := s.sync(int(s.parent.Fd())); err != nil {
		return err
	}
	if err := systemdCheckPublished(target, owned, actual); err != nil {
		return err
	}
	return checkTarget()
}
