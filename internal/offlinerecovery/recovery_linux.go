//go:build linux

package offlinerecovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/service"
	"golang.org/x/sys/unix"
)

// Input snapshots are private operation-owned ciphertext, not key exports.
const maxInput = (128 << 20) + (2 << 20)
const bindingName = "master-key-binding.json"

type pinned struct {
	file *os.File
	path string
	stat unix.Stat_t
}

func openNoLinks(path string, flags int) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, Failure("seed")
	}
	root, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	fd, err := unix.Openat2(root, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func sameObject(a, b unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }
func sameMetadata(a, b unix.Stat_t) bool {
	return sameObject(a, b) && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid && a.Nlink == b.Nlink &&
		a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func owned(stat unix.Stat_t, directory bool) bool {
	kind, mode := uint32(unix.S_IFREG), uint32(0o600)
	if directory {
		kind, mode = unix.S_IFDIR, 0o700
	}
	return stat.Mode&unix.S_IFMT == kind && stat.Mode&0o7777 == mode && stat.Uid == uint32(os.Geteuid()) &&
		stat.Gid == uint32(os.Getegid()) && (directory || stat.Nlink == 1)
}
func pin(path string, directory bool) (*pinned, error) {
	flags := unix.O_RDONLY
	if directory {
		flags |= unix.O_DIRECTORY
	}
	file, err := openNoLinks(path, flags)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || !owned(stat, directory) {
		file.Close()
		return nil, Failure("seed")
	}
	return &pinned{file, path, stat}, nil
}
func (p *pinned) check(directory bool) error {
	current, err := openNoLinks(p.path, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer current.Close()
	var held, named unix.Stat_t
	if unix.Fstat(int(p.file.Fd()), &held) != nil || unix.Fstat(int(current.Fd()), &named) != nil ||
		!owned(held, directory) || !owned(named, directory) || !sameObject(p.stat, held) || !sameObject(held, named) ||
		(!directory && (!sameMetadata(p.stat, held) || !sameMetadata(p.stat, named))) {
		return Failure("seed")
	}
	return nil
}
func (p *pinned) names() ([]string, error) {
	fd, err := unix.Openat(int(p.file.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	scan := os.NewFile(uintptr(fd), ".")
	defer scan.Close()
	return scan.Readdirnames(-1)
}
func (p *pinned) read(max int64) ([]byte, error) {
	if p.stat.Size <= 0 || p.stat.Size > max {
		return nil, Failure("seed")
	}
	data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(p.file, 0, max+1), max+1))
	if err != nil || int64(len(data)) != p.stat.Size || p.check(false) != nil {
		clear(data)
		return nil, Failure("seed")
	}
	return data, nil
}

func unitContext() error {
	account, err := user.Lookup(User)
	if err != nil || account.Uid != strconv.Itoa(os.Geteuid()) || account.Gid != strconv.Itoa(os.Getegid()) ||
		os.Geteuid() == 0 || os.Getegid() == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return Failure("unit")
	}
	groups, err := os.Getgroups()
	if err != nil {
		return Failure("unit")
	}
	for _, group := range groups {
		if group != os.Getegid() {
			return Failure("unit")
		}
	}
	pid1, err := os.ReadFile("/proc/1/comm")
	if err != nil || string(pid1) != "systemd\n" {
		return Failure("unit")
	}
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || string(cgroup) != "0::/system.slice/"+Unit+"\n" ||
		!regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(os.Getenv("INVOCATION_ID")) ||
		os.Getenv("CREDENTIALS_DIRECTORY") != "/run/credentials/"+Unit || os.Getenv("TMPDIR") != RuntimeDirectory ||
		os.Getenv("LISTEN_FDS") != "" || os.Getenv("LISTEN_PID") != "" {
		return Failure("unit")
	}
	// Root launching this fixed unit supplies the non-secret host namespace
	// identity. Non-root cannot reliably read /proc/1/ns/net on hardened hosts.
	host := os.Getenv("CPA_RECOVERY_HOST_NETNS")
	current, err := os.Readlink("/proc/self/ns/net")
	if err != nil || !regexp.MustCompile(`^net:\[[0-9]+\]$`).MatchString(host) || current == host {
		return Failure("unit")
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err == nil {
			unix.Close(fd)
			return Failure("unit")
		}
		if !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EAFNOSUPPORT) {
			return Failure("unit")
		}
	}
	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		data, err := os.ReadFile("/proc/self/net/" + table)
		if err != nil || len(strings.Split(strings.TrimSpace(string(data)), "\n")) != 1 {
			return Failure("unit")
		}
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return Failure("unit")
	}
	for _, fd := range fds {
		link, err := os.Readlink("/proc/self/fd/" + fd.Name())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Failure("unit")
		}
		if strings.HasPrefix(link, "socket:") {
			return Failure("unit")
		}
	}
	return nil
}

func targetAbsent(parent *pinned, target string) error {
	if parent.check(true) != nil {
		return Failure("target")
	}
	names, err := parent.names()
	if err != nil {
		return Failure("target")
	}
	for _, name := range names {
		if name == "cpa-cloud.db" || name == "master.key" || name == bindingName {
			return Failure("target")
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.file.Fd()), filepath.Base(target), &stat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return Failure("target")
	}
	return nil
}

func Run(ctx context.Context, o Options) (retErr error) {
	if ctx == nil || ctx.Err() != nil {
		return Failure("cancelled")
	}
	if err := unitContext(); err != nil {
		return err
	}
	var files []*pinned
	defer func() {
		for index := len(files) - 1; index >= 0; index-- {
			if files[index].file.Close() != nil {
				retErr = errors.Join(retErr, Failure("cleanup"))
			}
		}
	}()
	keep := func(p *pinned) *pinned { files = append(files, p); return p }
	runtimeDir, err := pin(RuntimeDirectory, true)
	if err != nil {
		return Failure("unit")
	}
	keep(runtimeDir)
	names, err := runtimeDir.names()
	if err != nil || len(names) != 0 {
		return Failure("unit")
	}
	// Report leftovers separately; never delete unknown temporary/foreign files.
	defer func() {
		names, err := runtimeDir.names()
		if err != nil || len(names) != 0 || runtimeDir.check(true) != nil {
			retErr = errors.Join(retErr, Failure("cleanup"))
		}
	}()
	seed, err := pin(o.Seed, true)
	if err != nil {
		return Failure("seed")
	}
	keep(seed)
	names, err = seed.names()
	if err != nil || len(names) != 1 || names[0] != bindingName {
		return Failure("seed")
	}
	binding, err := pin(filepath.Join(o.Seed, bindingName), false)
	if err != nil {
		return Failure("seed")
	}
	keep(binding)
	encoded, err := binding.read(4096)
	if err != nil {
		return Failure("seed")
	}
	defer clear(encoded)
	checkSeed := func() error {
		if seed.check(true) != nil || binding.check(false) != nil {
			return Failure("seed")
		}
		names, err := seed.names()
		if err != nil || len(names) != 1 || names[0] != bindingName {
			return Failure("seed")
		}
		data, err := binding.read(4096)
		defer clear(data)
		if err != nil || !bytes.Equal(data, encoded) {
			return Failure("seed")
		}
		return nil
	}
	defer func() {
		if checkSeed() != nil {
			retErr = errors.Join(retErr, Failure("seed"))
		}
	}()
	material, stage := service.ReadSystemdOfflineRecoveryMaterial(ctx, o.Seed, service.SystemdRecoveryIdentity{
		ProviderID: o.Provider, InstanceID: o.Instance, MasterID: o.MasterID, MasterVersion: o.MasterVersion,
		WrappingID: o.WrappingID, WrappingVersion: o.WrappingVersion,
	})
	defer clear(material.WrappingKey[:])
	if stage != "" {
		return Failure(stage)
	}
	if checkSeed() != nil {
		return Failure("seed")
	}
	validate := material.ValidateInstallationBinding
	material.ValidateInstallationBinding = func(data []byte) error {
		if checkSeed() != nil {
			return Failure("seed")
		}
		if err := validate(data); err != nil {
			return Failure("installation")
		}
		return checkSeed()
	}
	var parent *pinned
	if o.Restore {
		parent, err = pin(filepath.Dir(o.Target), true)
		if err != nil {
			return Failure("target")
		}
		keep(parent)
		if targetAbsent(parent, o.Target) != nil {
			return Failure("target")
		}
	}
	// Snapshot one stable no-link input before invoking the unchanged package API.
	// Both verification and publication consume this same operation-owned file.
	input, err := pin(o.Input, false)
	if err != nil {
		return Failure("package")
	}
	keep(input)
	data, err := input.read(maxInput)
	if err != nil {
		return Failure("package")
	}
	defer clear(data)
	fd, err := unix.Openat(int(runtimeDir.file.Fd()), "input.cpacb", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return Failure("package")
	}
	snapshot := os.NewFile(uintptr(fd), "input.cpacb")
	var snapshotStat unix.Stat_t
	if unix.Fstat(fd, &snapshotStat) != nil {
		snapshot.Close()
		return Failure("cleanup")
	}
	defer func() {
		var named unix.Stat_t
		if unix.Fstatat(int(runtimeDir.file.Fd()), "input.cpacb", &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameObject(snapshotStat, named) || !owned(named, false) {
			retErr = errors.Join(retErr, Failure("cleanup"))
		} else if unix.Unlinkat(int(runtimeDir.file.Fd()), "input.cpacb", 0) != nil {
			retErr = errors.Join(retErr, Failure("cleanup"))
		}
		if snapshot.Close() != nil {
			retErr = errors.Join(retErr, Failure("cleanup"))
		}
	}()
	if _, err = snapshot.Write(data); err != nil || snapshot.Sync() != nil {
		return Failure("package")
	}
	if input.check(false) != nil || runtimeDir.check(true) != nil {
		return Failure("package")
	}
	copyPath := filepath.Join(RuntimeDirectory, "input.cpacb")
	info, err := backup.VerifyWithKeyMaterial(ctx, copyPath, material)
	if err != nil {
		if ctx.Err() != nil {
			return Failure("cancelled")
		}
		return Failure("package")
	}
	if info.FormatVersion != 3 || info.SourceVersion != o.SourceVersion {
		return Failure("identity")
	}
	if o.Restore {
		if targetAbsent(parent, o.Target) != nil {
			return Failure("target")
		}
		_, err = backup.RestoreWithKeyMaterial(ctx, copyPath, o.Target, material)
		if err != nil {
			if errors.Is(err, backup.ErrSystemdRestoreCleanupIncomplete) {
				return Failure("cleanup")
			}
			if ctx.Err() != nil {
				return Failure("cancelled")
			}
			return Failure("restore")
		}
	}
	if ctx.Err() != nil {
		return Failure("cancelled")
	}
	if input.check(false) != nil {
		return Failure("package")
	}
	return nil
}
