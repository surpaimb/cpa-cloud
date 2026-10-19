#!/usr/bin/env python3
"""Independent no-secret directory/service-restart B0. See CONTRACT.md."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
BASE = HERE / "run.py" if (HERE / "run.py").is_file() else (
    HERE.parents[1] / "desktop/employee-config-switcher/preflight/run.py")
spec = importlib.util.spec_from_file_location("no_key_preflight", BASE)
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)
require = preflight.require

PUBLIC = b"CPA B0 public directory sentinel; not a keyring or secret.\n"
LEAF = "public-b0.sentinel"
MARKERS = (
    "ISOLATION_OK", "B0_DIRECTORY_GUARDS_OK", "B0_FIRST_INSTANCE_OK",
    "B0_OLD_INSTANCE_GONE", "B0_NEW_PID_AND_OWNER_OK", "B0_SENTINEL_PRESERVED",
    "B0_DIRECTORY_REMOVED", "OWNED_PROCESSES_STOPPED",
)
FAILURES = (
    "b0-scope", "b0-directory", "b0-file", "b0-entry-changed", "b0-content",
    "b0-unexpected-entry", "b0-control-socket", "b0-old-instance-present",
    "b0-stop-timeout", "b0-stop-status", "b0-owner-probe", "b0-owner-output",
    "b0-instance-reused", "b0-observer", "b0-cleanup", "b0-unexpected",
)


def identity(info):
    return info.st_dev, info.st_ino


def directory_ok(info, uid, gid):
    return (stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700
            and info.st_uid == uid and info.st_gid == gid)


def file_ok(info, uid, gid, size):
    return (stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
            and info.st_uid == uid and info.st_gid == gid
            and info.st_nlink == 1 and info.st_size == size)


def open_directory(name, uid, gid, parent=None):
    descriptor = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC,
                         dir_fd=parent)
    try:
        require(directory_ok(os.fstat(descriptor), uid, gid), "b0-directory")
        return descriptor
    except BaseException:
        os.close(descriptor)
        raise


class PublicDirectory:
    """Only fixed public bytes, anchored directory fds and exact owned entries."""
    def __init__(self, work, uid, gid):
        self.uid, self.gid = uid, gid
        self.fds = []
        self.work = self.data = self.directory = None
        try:
            self.work = open_directory(str(work), uid, gid)
            self.fds.append(self.work)
            self.data = open_directory("data", uid, gid, self.work)
            self.fds.append(self.data)
            require(os.listdir(self.data) == [], "b0-unexpected-entry")
            os.mkdir("keyrings", mode=0o700, dir_fd=self.data)
            self.directory = open_directory("keyrings", uid, gid, self.data)
            self.fds.append(self.directory)
            self.directory_id = identity(os.fstat(self.directory))
            fd = os.open(LEAF, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC,
                         0o600, dir_fd=self.directory)
            try:
                require(file_ok(os.fstat(fd), uid, gid, 0), "b0-file")
                require(os.write(fd, PUBLIC) == len(PUBLIC), "b0-content")
                os.fsync(fd)
                require(file_ok(os.fstat(fd), uid, gid, len(PUBLIC)), "b0-file")
                self.file_id = identity(os.fstat(fd))
            finally:
                os.close(fd)
            os.fsync(self.directory)
            os.fsync(self.data)
            self.check()
        except BaseException:
            self.close()
            raise

    def check(self):
        for fd in self.fds:
            require(directory_ok(os.fstat(fd), self.uid, self.gid), "b0-directory")
        require(identity(os.stat("data", dir_fd=self.work, follow_symlinks=False)) ==
                identity(os.fstat(self.data)), "b0-entry-changed")
        require(os.listdir(self.data) == ["keyrings"], "b0-unexpected-entry")
        reopened = open_directory("keyrings", self.uid, self.gid, self.data)
        try:
            require(identity(os.fstat(reopened)) == self.directory_id, "b0-entry-changed")
            require(os.listdir(reopened) == [LEAF], "b0-unexpected-entry")
            fd = os.open(LEAF, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK,
                         dir_fd=reopened)
            try:
                info = os.fstat(fd)
                require(file_ok(info, self.uid, self.gid, len(PUBLIC)), "b0-file")
                require(identity(info) == self.file_id, "b0-entry-changed")
                require(os.read(fd, len(PUBLIC) + 1) == PUBLIC, "b0-content")
                entry = os.stat(LEAF, dir_fd=reopened, follow_symlinks=False)
                require(identity(entry) == self.file_id and
                        file_ok(entry, self.uid, self.gid, len(PUBLIC)), "b0-entry-changed")
            finally:
                os.close(fd)
        finally:
            os.close(reopened)

    def remove(self):
        self.check()  # Only after both owned service instances have exited.
        os.unlink(LEAF, dir_fd=self.directory)
        os.fsync(self.directory)
        require(os.listdir(self.directory) == [], "b0-cleanup")
        require(identity(os.stat("keyrings", dir_fd=self.data, follow_symlinks=False)) ==
                self.directory_id, "b0-entry-changed")
        os.rmdir("keyrings", dir_fd=self.data)
        os.fsync(self.data)
        require(os.listdir(self.data) == [], "b0-cleanup")

    def close(self):
        for fd in reversed(self.fds):
            os.close(fd)
        self.fds.clear()


def stop(process, strict=False):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)
            require(not strict, "b0-stop-timeout")
    if strict:
        require(process.returncode == 0, "b0-stop-status")
    require(process.poll() is not None and not Path("/proc", str(process.pid)).exists(),
            "b0-old-instance-present")


def control_socket(work, uid, gid):
    control = work / "runtime/keyring"
    require(directory_ok(control.lstat(), uid, gid), "b0-directory")
    info = (control / "control").lstat()
    require(stat.S_ISSOCK(info.st_mode) and info.st_uid == uid and info.st_gid == gid
            and stat.S_IMODE(info.st_mode) & 0o077 == 0, "b0-control-socket")
    # Metadata only. Do not connect or send a control packet.


def inside(metadata_path):
    metadata = json.loads(Path(metadata_path).read_text())
    preflight.verify_isolation(metadata)
    work = Path(metadata["work"])
    uid, gid = metadata["uid"], metadata["gid"]
    require(work.name == "work" and work.parent.parent == Path("/opt")
            and work.parent.name.startswith("cpa-ss-preflight.")
            and work.resolve() == work, "b0-scope")
    processes, logs = [], []
    directory = None
    try:
        directory = PublicDirectory(work, uid, gid)
        require(list((work / "home").iterdir()) == [], "b0-unexpected-entry")
        print("B0_DIRECTORY_GUARDS_OK", flush=True)

        def start(args, environment, name):
            log = (work / (name + ".log")).open("xb")
            logs.append(log)
            process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL,
                                       stdout=log, stderr=subprocess.STDOUT)
            processes.append(process)
            return process

        environment = dict(os.environ)
        bus = start(["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog",
                     "--config-file=" + metadata["bus_config"]], environment, "bus")
        bus_path = work / "runtime/bus"
        deadline = time.monotonic() + 10
        while not bus_path.exists() and time.monotonic() < deadline:
            require(bus.poll() is None, "private-bus-exited")
            time.sleep(0.05)
        require(bus_path.exists(), "private-bus-unavailable")
        preflight.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
        environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + str(bus_path)

        def probe(mode, value):
            require(bus.poll() is None, "private-bus-exited")
            preflight.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
            return preflight.run([metadata["b0_owner"], mode, str(bus.pid), str(value)],
                                 env=environment, timeout=10, check=False)

        def launch(name):
            service = start(["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets",
                             "--control-directory=" + str(work / "runtime/keyring")], environment, name)
            deadline = time.monotonic() + 15
            while True:
                require(service.poll() is None, "owned-daemon-exited")
                preflight.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)
                result = probe("owner", service.pid)
                if result.returncode == 0:
                    require(re.fullmatch(rb":[0-9]+\.[0-9]+\n", result.stdout) is not None
                            and len(result.stdout) <= 64, "b0-owner-output")
                    owner = result.stdout.decode("ascii").strip()
                    break
                require(result.returncode == 30 and time.monotonic() < deadline, "b0-owner-probe")
                time.sleep(0.1)
            observed = preflight.run([metadata["observer"], str(bus.pid), str(service.pid)],
                                     env=environment, timeout=5, check=False)
            require(observed.returncode == 0 and observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n", "b0-observer")
            control_socket(work, uid, gid)
            directory.check()
            return service, owner

        def gone(owner):
            deadline = time.monotonic() + 5
            while True:
                result = probe("absent", owner)
                if result.returncode == 0:
                    require(result.stdout == b"ABSENT\n", "b0-owner-output")
                    return
                require(result.returncode == 31 and time.monotonic() < deadline,
                        "b0-old-instance-present")
                time.sleep(0.05)

        first, first_owner = launch("first")
        print("B0_FIRST_INSTANCE_OK", flush=True)
        stop(first, strict=True)
        gone(first_owner)
        directory.check()
        print("B0_OLD_INSTANCE_GONE", flush=True)
        second, second_owner = launch("second")
        require(second.pid != first.pid and second_owner != first_owner, "b0-instance-reused")
        print("B0_NEW_PID_AND_OWNER_OK", flush=True)
        directory.check()
        require(list((work / "home").iterdir()) == [], "b0-unexpected-entry")
        print("B0_SENTINEL_PRESERVED", flush=True)
        stop(second, strict=True)
        gone(second_owner)
        directory.remove()
        print("B0_DIRECTORY_REMOVED", flush=True)
    finally:
        try:
            for process in reversed(processes):
                stop(process)
        finally:
            for log in logs:
                log.close()
            if directory is not None:
                directory.close()
    print("OWNED_PROCESSES_STOPPED", flush=True)


class Fixture:
    markers = MARKERS
    failures = FAILURES
    diagnostics = ()

    def prepare(self, root, metadata):
        # The shared harness's extension entrypoint is named interop.py; this
        # file is B0, not the secret-bearing A fixture. Shared policy unchanged.
        shutil.copyfile(HERE / "fixture.py", root / "interop.py")
        (root / "interop.py").chmod(0o444)
        observer = root / "b0-owner"
        flags = preflight.run(["pkg-config", "--cflags", "--libs", "libsecret-1"]).stdout.decode().split()
        preflight.run(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", str(HERE / "owner.c"),
                       "-o", str(observer), *flags])
        observer.chmod(0o555)
        metadata["b0_owner"] = str(observer)
        print("B0_OBSERVER source-sha256=" + hashlib.sha256((HERE / "owner.c").read_bytes()).hexdigest(), flush=True)
        print("B0_OBSERVER executable-sha256=" + hashlib.sha256(observer.read_bytes()).hexdigest(), flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 1:
            preflight.outer(Fixture())
            print("CHECK B0_OUTER_CLEANUP_OK=true", flush=True)
        else:
            raise preflight.PreflightFailure("invalid-invocation")
    except preflight.PreflightFailure as failure:
        print("FAIL: " + failure.args[0], file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("FAIL: b0-unexpected", file=sys.stderr)
        sys.exit(1)
