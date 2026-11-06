#!/usr/bin/env python3
"""No-key disposable Linux preflight. Independent source; see CONTRACT.md."""
import errno
import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import stat
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
USER = "cpa-ss-preflight"
UNIT = "cpa-ss-preflight.service"
SAFE_ENV = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}
MARKERS = ("ISOLATION_OK", "PRIVATE_BUS_SERVICE_UID_PID_OK", "LIBSECRET_METADATA_ONLY_OK", "OWNED_PROCESSES_STOPPED")
BUS_DIAGNOSTICS = {
    "permission": b"Permission denied", "operation-not-permitted": b"Operation not permitted",
    "address-family": b"Address family", "read-only": b"Read-only file system",
    "config-parse": b"parse", "config-attribute": b"attribute", "config-rule": b"rule",
    "apparmor": b"AppArmor", "socket-bind": b"bind", "missing-path": b"No such file",
    "machine-id": b"machine-id", "unsupported": b"not supported",
    "config-member": b"send_member", "config-interface": b"send_interface", "config-path": b"send_path",
    "usage": b"Usage", "requires": b"requires", "xml-element": b"Element", "startup-failed": b"Failed",
    "bus-exit-success": b"", "bus-exit-error": b"", "bus-exit-signal": b"", "bus-log-empty": b"",
}


class PreflightFailure(Exception):
    """Only source-defined labels, never external diagnostics, reach stdout."""


def require(condition, label):
    if not condition:
        raise PreflightFailure(label)


def run(args, *, env=None, timeout=30, check=True):
    result = subprocess.run(args, env=env, stdin=subprocess.DEVNULL,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                            timeout=timeout, check=False)
    if check:
        require(result.returncode == 0, "command-failed")
    return result


def private(path, uid, gid):
    path.mkdir(mode=0o700)
    os.chown(path, uid, gid)


def owned_process(pid, expected_executable, uid):
    proc = Path("/proc") / str(pid)
    require(proc.stat().st_uid == uid, "process-owner")
    require((proc / "exe").resolve() == Path(expected_executable).resolve(), "process-executable")
    require(UNIT in (proc / "cgroup").read_text(), "process-cgroup")


def connect(path):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(1)
        client.connect(str(path))


def denied_socket(path):
    try:
        connect(path)
    except OSError as error:
        require(error.errno in (errno.EACCES, errno.ENOENT), "socket-boundary-wrong-error")
    else:
        raise PreflightFailure("host-socket-reachable")


def verify_isolation(metadata):
    """Shared prerequisite: all assertions finish before any secret operation."""
    uid, work = metadata["uid"], Path(metadata["work"])
    require(os.geteuid() == uid and uid != 0 and os.getegid() == metadata["gid"], "nonroot-required")
    require(not any(name.lower().endswith("_proxy") for name in os.environ), "proxy-inherited")
    for name in ("DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY",
                 "GNOME_KEYRING_CONTROL", "SSH_AUTH_SOCK"):
        require(name not in os.environ, "session-inherited")
    for name, leaf in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
                       ("XDG_CACHE_HOME", "cache"), ("XDG_STATE_HOME", "state"),
                       ("XDG_RUNTIME_DIR", "runtime"), ("TMPDIR", "tmp")):
        path = work / leaf
        info = path.lstat()
        require(os.environ.get(name) == str(path) and info.st_uid == uid
                and stat.S_IMODE(info.st_mode) == 0o700 and stat.S_ISDIR(info.st_mode), "private-directory")
    require(os.readlink("/proc/self/ns/net") != metadata["host_netns"], "netns-shared")
    interfaces = {line.split(":")[0].strip() for line in Path("/proc/net/dev").read_text().splitlines() if ":" in line}
    require(interfaces == {"lo"}, "network-not-loopback-only")
    for family in (socket.AF_INET, socket.AF_INET6):
        try:
            probe = socket.socket(family, socket.SOCK_STREAM)
        except OSError as error:
            require(error.errno in (errno.EAFNOSUPPORT, errno.EPERM), "ip-socket-wrong-error")
        else:
            probe.close()
            raise PreflightFailure("ip-socket-permitted")
    for path in (metadata["host_home"], "/run/user"):
        try:
            descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
        except OSError as error:
            require(error.errno in (errno.EACCES, errno.ENOENT), "home-wrong-error")
        else:
            os.close(descriptor)
            raise PreflightFailure("host-home-visible")
    for path in metadata["host_sockets"]:
        denied_socket(path)
    require(not Path(metadata["shm_host"]).exists(), "host-shm-visible")
    # A marker in the new shm mount must not become visible on the host.
    Path(metadata["shm_unit"]).write_text("public-unit-marker", encoding="ascii")
    local = work / "runtime" / "local.sock"
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as server:
        server.bind(str(local))
        server.listen(1)
        connect(local)
        connection, _ = server.accept()
        connection.close()
    local.unlink()
    print("ISOLATION_OK", flush=True)


def inside(metadata_path):
    metadata = json.loads(Path(metadata_path).read_text())
    verify_isolation(metadata)
    uid, work = metadata["uid"], Path(metadata["work"])
    processes, logs, observed_ok = [], [], False
    try:
        def start(args, environment, name):
            log = (work / (name + ".log")).open("xb")
            logs.append(log)
            process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL,
                                       stdout=log, stderr=subprocess.STDOUT)
            processes.append(process)
            return process

        environment = dict(os.environ)
        bus = start(["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog", "--config-file=" + metadata["bus_config"]], environment, "bus")
        bus_path = work / "runtime" / "bus"
        deadline = time.monotonic() + 10
        while not bus_path.exists() and time.monotonic() < deadline:
            require(bus.poll() is None, "private-bus-exited")
            time.sleep(0.05)
        require(bus_path.exists(), "private-bus-unavailable")
        owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
        environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + str(bus_path)
        service = start(["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets",
                         "--control-directory=" + str(work / "runtime" / "keyring")], environment, "keyring")
        deadline = time.monotonic() + 15
        while True:
            require(bus.poll() is None and service.poll() is None, "owned-daemon-exited")
            owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)
            observed = run([metadata["observer"], str(bus.pid), str(service.pid)], env=environment, timeout=5, check=False)
            if observed.returncode == 0:
                require(observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n", "observer-output-rejected")
                break
            require(observed.returncode == 30 and time.monotonic() < deadline, "metadata-observer-failed")
            time.sleep(0.1)
        print("PRIVATE_BUS_SERVICE_UID_PID_OK", flush=True)
        print("LIBSECRET_METADATA_ONLY_OK", flush=True)
        observed_ok = True
    finally:
        for process in reversed(processes):
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for log in logs:
            log.close()
        if processes and not observed_ok:
            output = (work / "bus.log").read_bytes()
            code = processes[0].returncode
            category = "bus-exit-success" if code == 0 else "bus-exit-signal" if code is not None and code < 0 else "bus-exit-error"
            print("BUS_DIAGNOSTIC " + category, flush=True)
            if not output:
                print("BUS_DIAGNOSTIC bus-log-empty", flush=True)
            for label, fragment in BUS_DIAGNOSTICS.items():
                if fragment and fragment.lower() in output.lower():
                    print("BUS_DIAGNOSTIC " + label, flush=True)
    require(all(process.poll() is not None for process in processes), "owned-process-remains")
    print("OWNED_PROCESSES_STOPPED", flush=True)


def package_manifest():
    manifest = json.loads((HERE / "components.json").read_text())
    for package, expected in manifest.items():
        actual = run(["dpkg-query", "-W", "-f=${Version}", package]).stdout.decode().strip()
        require(actual == expected["version"], "component-version-mismatch")
        copyright_path = Path("/usr/share/doc") / package / "copyright"
        require(copyright_path.is_file(), "component-license-missing")
        digest = hashlib.sha256(copyright_path.read_bytes()).hexdigest()
        print("COMPONENT " + package + " " + actual + " copyright-sha256=" + digest, flush=True)
    require(run(["pkg-config", "--modversion", "libsecret-1"]).stdout.strip() == b"0.21.4", "libsecret-api-version")


def diagnostic_cleanup(actions, diagnostic):
    """Optional fixture reporting: attempt existing cleanup actions once each."""
    failed = False
    for stage, action in actions:
        try:
            action()
            diagnostic(stage, "ok")
        except subprocess.TimeoutExpired:
            diagnostic(stage, "timeout")
            failed = True
        except Exception:
            diagnostic(stage, "failed")
            failed = True
    diagnostic("cleanup", "failed" if failed else "ok")
    require(not failed, "cleanup-diagnostic-failed")


def outer(fixture=None):
    # The no-argument CLI remains metadata-only. Only the separately invoked
    # synthetic fixture harness supplies this in-process extension.
    diagnostic = getattr(fixture, "outer_diagnostic", None)
    require(sys.platform == "linux" and os.geteuid() == 0, "linux-root-harness-required")
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted"
            and os.environ.get("RUNNER_OS") == "Linux", "disposable-runner-required")
    require(Path("/proc/1/comm").read_text().strip() == "systemd", "systemd-required")
    run(["systemd-detect-virt", "--vm"])
    import pwd
    import grp
    for lookup in (pwd.getpwnam, grp.getgrnam):
        try:
            lookup(USER)
        except KeyError:
            pass
        else:
            raise PreflightFailure("identity-already-exists")
    require(run(["systemctl", "show", UNIT, "--property=LoadState", "--value"], check=False).stdout.strip() == b"not-found", "unit-already-exists")
    runner_uid = int(os.environ.get("SUDO_UID", "0"))
    require(runner_uid != 0, "runner-uid-required")
    host_home = Path(pwd.getpwuid(runner_uid).pw_dir)
    require(host_home.is_dir() and str(host_home).startswith("/home/"), "runner-home-unexpected")
    package_manifest()
    os.umask(0o077)
    root = Path(tempfile.mkdtemp(prefix="cpa-ss-preflight.", dir="/opt"))
    root.chmod(0o755)
    host = Path(tempfile.mkdtemp(prefix="cpa-ss-host.", dir="/var/tmp"))
    host.chmod(0o755)
    shm_host = Path("/dev/shm") / (root.name + ".host")
    shm_unit = Path("/dev/shm") / (root.name + ".unit")
    sockets, created_user, started = [], False, False
    unit_capture, unit_capture_complete = b"", False
    try:
        run(["useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", USER])
        created_user = True
        identity = pwd.getpwnam(USER)
        require(identity.pw_uid != 0 and identity.pw_gid != 0, "fixture-identity")
        uid, gid = identity.pw_uid, identity.pw_gid
        work = root / "work"
        private(work, uid, gid)
        for leaf in ("home", "config", "data", "cache", "state", "runtime", "tmp"):
            private(work / leaf, uid, gid)
        private(work / "runtime" / "keyring", uid, gid)
        for name in ("run.py", "bus.conf.in"):
            shutil.copyfile(HERE / name, root / name)
            (root / name).chmod(0o444)
        observer = root / "observer"
        flags = run(["pkg-config", "--cflags", "--libs", "libsecret-1"]).stdout.decode().split()
        run(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", str(HERE / "libsecret-observer.c"), "-o", str(observer), *flags])
        observer.chmod(0o555)
        print("OBSERVER cpa-no-key-preflight-v1 source-sha256=" + hashlib.sha256((HERE / "libsecret-observer.c").read_bytes()).hexdigest(), flush=True)
        for executable in (Path("/usr/bin/dbus-daemon"), Path("/usr/bin/gnome-keyring-daemon"), observer):
            print("EXECUTABLE " + executable.name + " sha256=" + hashlib.sha256(executable.read_bytes()).hexdigest(), flush=True)
        bus_config = root / "bus.conf"
        bus_config.write_text((root / "bus.conf.in").read_text().replace("@USER@", USER).replace("@BUS_PATH@", str(work / "runtime" / "bus")))
        bus_config.chmod(0o444)
        # Public, nonsecret probes. No existing user/session socket is contacted.
        host_socket = host / "session.sock"
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sockets.append(server)
        server.bind(str(host_socket))
        host_socket.chmod(0o666)
        server.listen(4)
        with shm_host.open("x") as stream:
            stream.write("public-host-marker")
        shm_host.chmod(0o644)
        require(not shm_unit.exists(), "shm-unit-probe-exists")
        metadata = dict(uid=uid, gid=gid, work=str(work), host_home=str(host_home), host_netns=os.readlink("/proc/self/ns/net"),
                        host_sockets=[str(host_socket)], shm_host=str(shm_host), shm_unit=str(shm_unit),
                        observer=str(observer), bus_config=str(bus_config))
        if fixture is not None:
            fixture.prepare(root, metadata)
        metadata_path = root / "metadata.json"
        metadata_path.write_text(json.dumps(metadata))
        metadata_path.chmod(0o444)
        # Prove these public probes are reachable by this UID outside the unit.
        run(["/usr/sbin/runuser", "-u", USER, "--", "/usr/bin/python3", str(root / "run.py"), "--baseline", str(metadata_path)], env=SAFE_ENV)
        properties = ["User=" + USER, "Group=" + USER, "UMask=0077", "NoNewPrivileges=yes",
                      "PrivateNetwork=yes", "PrivateIPC=yes", "PrivateDevices=yes", "PrivateTmp=yes",
                      "TemporaryFileSystem=/dev/shm:rw,mode=1777", "ProtectHome=yes", "ProtectSystem=strict",
                      "ProtectKernelTunables=yes", "ProtectControlGroups=yes", "CapabilityBoundingSet=",
                      "RestrictAddressFamilies=AF_UNIX", "ReadWritePaths=" + str(work),
                      "InaccessiblePaths=-/run/dbus -/run/user", "LimitCORE=0", "RuntimeMaxSec=90",
                      "TimeoutStopSec=10", "KillMode=control-group", "WorkingDirectory=" + str(work)]
        environment = dict(SAFE_ENV)
        for name, leaf in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
                           ("XDG_CACHE_HOME", "cache"), ("XDG_STATE_HOME", "state"), ("XDG_RUNTIME_DIR", "runtime"), ("TMPDIR", "tmp")):
            environment[name] = str(work / leaf)
        args = ["systemd-run", "--wait", "--collect", "--quiet", "--pipe", "--unit=" + UNIT]
        args += ["--property=" + item for item in properties]
        args += ["/usr/bin/env", "-i"] + [key + "=" + value for key, value in environment.items()]
        script = root / ("run.py" if fixture is None else "interop.py")
        args += ["/usr/bin/python3", str(script), "--inside", str(metadata_path)]
        if fixture is not None and hasattr(fixture, "before_unit"):
            fixture.before_unit(metadata)
        started = True
        result = run(args, timeout=110, check=False)
        unit_capture, unit_capture_complete = result.stdout, True
        # Never relay raw daemon, D-Bus, GLib, journal or child process output.
        always_diagnostics = fixture is not None and getattr(fixture, "diagnostics_always", False)
        if always_diagnostics:
            for label in fixture.diagnostics:
                if ("FIXTURE_DIAGNOSTIC " + label + "\n").encode() in result.stdout:
                    print("FIXTURE_DIAGNOSTIC " + label, flush=True)
        if result.returncode != 0:
            if fixture is not None:
                for label in (() if always_diagnostics else fixture.diagnostics):
                    if ("FIXTURE_DIAGNOSTIC " + label + "\n").encode() in result.stdout:
                        print("FIXTURE_DIAGNOSTIC " + label, flush=True)
                for label in fixture.failures:
                    if ("FAIL: " + label + "\n").encode() in result.stdout:
                        print("CHECK rejected=" + label, flush=True)
                for label in ("setup", "verify", "create", "reopen", "wrong-transaction", "wrong-namespace", "missing-item", "no-service"):
                    if ("CASE_FAILURE " + label + "\n").encode() in result.stdout:
                        print("CHECK case-failed=" + label, flush=True)
            for label in BUS_DIAGNOSTICS:
                if ("BUS_DIAGNOSTIC " + label + "\n").encode() in result.stdout:
                    print("BUS_DIAGNOSTIC " + label, flush=True)
            for marker in (MARKERS if fixture is None else fixture.markers):
                print("CHECK " + marker + "=" + str(marker.encode() in result.stdout).lower(), flush=True)
            for label in ("nonroot-required", "proxy-inherited", "session-inherited", "private-directory",
                          "netns-shared", "network-not-loopback-only", "ip-socket-wrong-error", "ip-socket-permitted",
                          "home-wrong-error", "host-home-visible", "host-socket-reachable", "socket-boundary-wrong-error",
                          "host-shm-visible", "private-bus-exited", "private-bus-unavailable", "process-owner",
                          "process-executable", "process-cgroup", "owned-daemon-exited", "metadata-observer-failed"):
                if ("FAIL: " + label + "\n").encode() in result.stdout:
                    print("CHECK rejected=" + label, flush=True)
        markers = MARKERS if fixture is None else fixture.markers
        for marker in markers:
            require(result.returncode == 0 and marker.encode() in result.stdout, "unit-preflight-failed")
        require(shm_host.read_text() == "public-host-marker" and not shm_unit.exists(), "host-shm-changed")
        connect(host_socket)
        if fixture is None:
            print("PASS: fixed components; nonroot private bus/service; tested namespace boundaries; no key operations", flush=True)
        else:
            # Extension output remains captured. Publish only its static cases.
            for marker in markers:
                print("CHECK " + marker + "=true", flush=True)
        if diagnostic:
            diagnostic("operation", "ok")
    except subprocess.TimeoutExpired as failure:
        unit_capture = failure.stdout or b""
        if diagnostic:
            diagnostic("operation", "timeout")
        raise
    except Exception:
        if diagnostic:
            diagnostic("operation", "failed")
        raise
    finally:
        actions = []
        unit_stopped = not started
        if started:
            def unit_cleanup():
                nonlocal unit_stopped
                run(["systemctl", "stop", UNIT], check=False)
                state = run(["systemctl", "show", UNIT, "--property=ActiveState", "--value"], check=False).stdout.strip()
                require(state in (b"inactive", b"failed"), "cleanup-unit-active")
                if fixture is not None and hasattr(fixture, "verify_unit_stopped"):
                    fixture.verify_unit_stopped(metadata)
                unit_stopped = True
            actions.append(("unit", unit_cleanup))
            if fixture is not None and hasattr(fixture, "after_unit"):
                def unit_evidence():
                    require(unit_stopped, "cleanup-unit-active")
                    fixture.after_unit(metadata, unit_capture, unit_capture_complete)
                actions.append(("unit-evidence", unit_evidence))
        for server in sockets:
            actions.append(("sockets", server.close))
        if created_user:
            def user_cleanup():
                require(not diagnostic or unit_stopped, "cleanup-unit-active")
                run(["userdel", USER])
            actions.append(("user", user_cleanup))
        def shm_cleanup():
            if shm_host.exists():
                shm_host.unlink()
        actions.append(("shm", shm_cleanup))
        for directory, parent, prefix in ((root, Path("/opt"), "cpa-ss-preflight."), (host, Path("/var/tmp"), "cpa-ss-host.")):
            def directory_cleanup(directory=directory, parent=parent, prefix=prefix):
                require(not diagnostic or unit_stopped, "cleanup-unit-active")
                require(directory.resolve() == directory and directory.parent == parent and directory.name.startswith(prefix)
                        and not directory.is_symlink(), "cleanup-path-rejected")
                shutil.rmtree(directory)
            actions.append(("work-root" if directory == root else "host-root", directory_cleanup))
        if diagnostic:
            diagnostic_cleanup(actions, diagnostic)
        else:
            for _, action in actions:
                action()


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 3 and sys.argv[1] == "--baseline":
            data = json.loads(Path(sys.argv[2]).read_text())
            require(os.geteuid() == data["uid"], "baseline-uid")
            for path in data["host_sockets"]:
                connect(path)
            require(Path(data["shm_host"]).read_text() == "public-host-marker", "baseline-shm")
        elif len(sys.argv) == 1:
            outer()
        else:
            raise PreflightFailure("invalid-invocation")
    except PreflightFailure as failure:
        print("FAIL: " + failure.args[0], file=sys.stderr)
        sys.exit(1)
    except Exception:
        # Deliberately no exception text, raw output, environment or bus body.
        print("FAIL: no-key Secret Service preflight", file=sys.stderr)
        sys.exit(1)
