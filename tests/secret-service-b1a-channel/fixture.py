"""Independent B1a-C launcher. Disposable GitHub Linux VM only; no GNOME.

All raw captures and the synthetic oracle stay in outer memory. Only fixed
labels leave this program. Existing preflight/run.py is neither imported nor
modified. See CONTRACT.md for the narrower acceptance claim.
"""
import base64
import errno
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time

from channel import (Capture, Failure, LABELS, OneShot, SystemIO, Target,
                     check_receipt, close_checked, observe, pipe_identity,
                     read_line, require, scan)

HERE = Path(__file__).resolve().parent
SAFE = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1"}
INNER_SURFACES = {"receiver-out", "receiver-err", "bus-out", "bus-err", "private-files"}
CASES = ("positive", "empty", "nul", "cr", "lf", "truncated", "over-limit",
         "wrong-uid", "wrong-pid", "wrong-exe", "wrong-object", "deadline",
         "duplicate", "early-eof", "short-write")
EXPECTED = {"empty": "c-empty", "nul": "c-encoding", "cr": "c-encoding", "lf": "c-encoding",
            "truncated": "c-length", "over-limit": "c-over-limit", "wrong-uid": "c-identity",
            "wrong-pid": "c-identity", "wrong-exe": "c-identity", "wrong-object": "c-pipe",
            "deadline": "c-deadline", "duplicate": "c-reused", "early-eof": None,
            "short-write": "c-short-write", "positive": None}
USED_UIDS = set()


def cleanup_action(first, action):
    try:
        action()
        return first, True
    except Exception:
        return first or "c-cleanup", False


def stop_child(process):
    try:
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=3)
    except Exception:
        # A later forced cleanup must not turn an earlier wait failure green.
        try:
            process.kill()
            process.wait(timeout=3)
        except Exception:
            pass
        raise Failure("c-wait") from None


class Commands:
    def __init__(self):
        self.inventory = {}
        self.number = 0

    def run(self, args, timeout=10, check=True):
        # Every helper's two streams are registered before waiting. Names and
        # commands contain only public fixture metadata, never the oracle.
        self.number += 1
        name = "helper-" + str(self.number)
        self.inventory[name + "-out"] = (b"", False)
        self.inventory[name + "-err"] = (b"", False)
        process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, env=SAFE, close_fds=True)
        out, err = Capture(process.stdout), Capture(process.stderr)
        expired = False
        try:
            code = process.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            expired = True
            process.kill()
            code = process.wait(timeout=3)
        self.inventory[name + "-out"] = out.finish()
        self.inventory[name + "-err"] = err.finish()
        require(not expired, "c-wait")
        require(all(self.inventory[k][1] for k in (name + "-out", name + "-err")), "c-capture")
        require(not check or code == 0, "c-setup")
        return code, self.inventory[name + "-out"][0]


def proc_group(pid):
    return (Path("/proc") / str(pid) / "cgroup").read_bytes()


def same_process(process, executable, uid):
    root = Path("/proc") / str(process.pid)
    require(process.poll() is None and root.stat().st_uid == uid, "c-identity")
    a, b = (root / "exe").stat(), Path(executable).stat()
    require((a.st_dev, a.st_ino) == (b.st_dev, b.st_ino), "c-identity")
    require(proc_group(process.pid) == proc_group(os.getpid()), "c-unit")


def stable(s):
    # Reading can update atime. It must not change object identity/content.
    return (s.st_dev, s.st_ino, s.st_uid, s.st_gid, s.st_mode, s.st_nlink,
            s.st_size, s.st_mtime_ns, s.st_ctime_ns)


def snapshot_private(roots, uid):
    """Read only newly created private namespaces; no host credential scan.

    Unknown type, symlink, hardlink, over-limit or read/identity failure means
    incomplete. No truncate-and-clear result is possible.
    """
    data, count = bytearray(), 0
    try:
        for root in roots:
            for directory, dirs, files in os.walk(root, followlinks=False):
                require(len(dirs) + len(files) < 128, "c-scan")
                for name in dirs:
                    s = (Path(directory) / name).lstat()
                    require(stat.S_ISDIR(s.st_mode) and s.st_uid == uid, "c-scan")
                for name in files:
                    path = Path(directory) / name
                    before = path.lstat()
                    # The one private bus socket has no persisted content.
                    if stat.S_ISSOCK(before.st_mode):
                        require(path.name == "bus" and before.st_uid == uid, "c-scan")
                        continue
                    require(stat.S_ISREG(before.st_mode) and before.st_uid == uid
                            and before.st_nlink == 1 and before.st_size <= 32768, "c-scan")
                    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
                    try:
                        held = os.fstat(fd)
                        require((held.st_dev, held.st_ino) == (before.st_dev, before.st_ino), "c-scan")
                        chunk = os.read(fd, 32769)
                        require(len(chunk) == held.st_size and not os.read(fd, 1), "c-scan")
                        require(stable(os.fstat(fd)) == stable(held) and stable(path.lstat()) == stable(before), "c-scan")
                        data += chunk
                        count += 1
                        require(count <= 128 and len(data) <= 32768, "c-scan")
                    finally:
                        close_checked(fd)
        return bytes(data), True
    except Exception:
        return bytes(data), False


def isolation(metadata):
    uid, gid, work = metadata["uid"], metadata["gid"], Path(metadata["work"])
    require(uid != 0 and os.getuid() == os.geteuid() == uid and os.getgid() == gid, "c-isolation")
    require(os.readlink("/proc/self/ns/net") != metadata["netns"], "c-isolation")
    require(os.readlink("/proc/self/ns/ipc") != metadata["ipcns"], "c-isolation")
    require(not Path(metadata["host_shm"]).exists(), "c-isolation")
    require(not os.access(metadata["host_home"], os.R_OK), "c-isolation")
    for path in ("/run/user", "/run/dbus", metadata["host"]):
        require(not os.access(path, os.R_OK), "c-isolation")
    require(not any(k in os.environ for k in ("DBUS_SESSION_BUS_ADDRESS", "DBUS_SYSTEM_BUS_ADDRESS",
                                               "GNOME_KEYRING_CONTROL", "HTTP_PROXY", "HTTPS_PROXY",
                                               "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy")), "c-isolation")
    for variable, leaf in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
                           ("XDG_CACHE_HOME", "cache"), ("XDG_STATE_HOME", "state"),
                           ("XDG_RUNTIME_DIR", "runtime"), ("TMPDIR", "tmp")):
        expected = work / leaf
        s = expected.lstat()
        require(os.environ.get(variable) == str(expected) and stat.S_ISDIR(s.st_mode)
                and stat.S_IMODE(s.st_mode) == 0o700 and (s.st_uid, s.st_gid) == (uid, gid), "c-isolation")
    for family in (socket.AF_INET, socket.AF_INET6):
        try:
            sock = socket.socket(family, socket.SOCK_STREAM)
        except OSError as e:
            require(e.errno in (errno.EAFNOSUPPORT, errno.EPERM, errno.EACCES), "c-isolation")
        else:
            sock.close()
            raise Failure("c-isolation")
    for path in (metadata["host_socket"], "/run/user/" + str(uid) + "/bus"):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            sock.settimeout(.5)
            try:
                sock.connect(path)
            except OSError as e:
                require(e.errno in (errno.ENOENT, errno.EACCES, errno.EPERM, errno.ECONNREFUSED), "c-isolation")
            else:
                raise Failure("c-isolation")
        finally:
            sock.close()


def inside(path):
    metadata = json.loads(Path(path).read_bytes())
    inventory = {k: (b"", False) for k in INNER_SURFACES}
    bus = child = None
    bus_out = bus_err = child_out = child_err = None
    first = None
    code, hello = -1, b""
    try:
        isolation(metadata)
        bus = subprocess.Popen(["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog",
                                "--config-file=" + metadata["bus_config"]], env=dict(os.environ),
                               stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               close_fds=True)
        bus_out, bus_err = Capture(bus.stdout), Capture(bus.stderr)
        socket_path = Path(metadata["work"]) / "runtime" / "bus"
        deadline = time.monotonic() + 5
        while not socket_path.exists() and time.monotonic() < deadline:
            require(bus.poll() is None, "c-bus")
            time.sleep(.02)
        same_process(bus, "/usr/bin/dbus-daemon", metadata["uid"])
        s = socket_path.lstat()
        # D-Bus controls its socket mode; privacy is the verified 0700 runtime
        # ancestor plus EXTERNAL policy and exact peer credentials, not chmod.
        require(stat.S_ISSOCK(s.st_mode) and s.st_uid == metadata["uid"], "c-bus")
        probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            probe.settimeout(1)
            probe.connect(str(socket_path))
            peer = struct.unpack("3i", probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            require(peer[:2] == (bus.pid, metadata["uid"]), "c-bus")
        finally:
            probe.close()
        environment = dict(os.environ, DBUS_SESSION_BUS_ADDRESS="unix:path=" + str(socket_path))
        child = subprocess.Popen(["/usr/bin/python3", "-B", str(Path(path).parent / "receiver.py"), metadata["purpose"]],
                                 env=environment, stdin=0, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                 close_fds=True)
        close_checked(0)  # The launcher is not a second reader of the password.
        child_err = Capture(child.stderr)
        hello = read_line(child.stdout.fileno(), time.monotonic() + 5)
        # Private protocol only: the outer oracle consumes this, never relays it.
        require(os.write(1, hello) == len(hello), "c-protocol")
        child_out = Capture(child.stdout)
        code = child.wait(timeout=25)
        require(code in (0, 1), "c-wait")
    except Failure as e:
        first = e.args[0]
    except Exception:
        first = "c-internal"
    finally:
        for process in (child, bus):
            if process is not None:
                try:
                    stop_child(process)
                except Exception:
                    first = first or "c-wait"
        for name, capture in (("receiver-out", child_out), ("receiver-err", child_err),
                              ("bus-out", bus_out), ("bus-err", bus_err)):
            if capture:
                inventory[name] = capture.finish()
        inventory["private-files"] = snapshot_private([Path(metadata["work"]), Path("/tmp"),
                                                        Path("/var/tmp"), Path("/dev/shm")], metadata["uid"])
        packet = {"captures": {k: base64.b64encode(v[0]).decode("ascii") for k, v in inventory.items()},
                  "complete": {k: v[1] for k, v in inventory.items()}, "label": first,
                  "receiver_code": code, "children_waited": all(p is not None and p.poll() is not None for p in (child, bus))}
        encoded = json.dumps(packet, sort_keys=True).encode("ascii") + b"\n"
        try:
            require(len(encoded) < 600000 and os.write(1, encoded) == len(encoded), "c-protocol")
        except Exception:
            first = first or "c-protocol"
    return 1 if first else 0


def private_dir(path, uid, gid):
    path.mkdir(mode=0o700)
    os.chown(path, uid, gid)
    s = path.lstat()
    require(stat.S_ISDIR(s.st_mode) and stat.S_IMODE(s.st_mode) == 0o700
            and (s.st_uid, s.st_gid) == (uid, gid), "c-setup")


def public_file(path, content, uid, gid):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        require(os.write(fd, content) == len(content), "c-setup")
        os.fchown(fd, uid, gid)
        s = os.fstat(fd)
        require(stat.S_IMODE(s.st_mode) == 0o600 and s.st_nlink == 1, "c-setup")
    finally:
        close_checked(fd)
    return path.lstat(), content


def unit_properties(user, work, host):
    return ["Type=exec", "User=" + user, "Group=" + user, "UMask=0077", "NoNewPrivileges=yes",
            "PrivateNetwork=yes", "PrivateIPC=yes", "PrivateDevices=yes", "PrivateTmp=yes",
            "TemporaryFileSystem=/dev/shm:rw,mode=1777", "ProtectHome=yes", "ProtectSystem=strict",
            "ProtectKernelTunables=yes", "ProtectControlGroups=yes", "CapabilityBoundingSet=",
            "RestrictAddressFamilies=AF_UNIX", "ReadWritePaths=" + str(work),
            "InaccessiblePaths=-/run/dbus -/run/user -" + str(host), "LimitCORE=0",
            "RuntimeMaxSec=40", "TimeoutStopSec=5", "KillMode=control-group", "WorkingDirectory=" + str(work)]


def get_unit(commands, unit):
    _, raw = commands.run(["systemctl", "show", unit, "--property=ActiveState,LoadState,ControlGroup,InvocationID"], check=False)
    return dict(line.split("=", 1) for line in raw.decode("ascii").splitlines() if "=" in line)


def variant(kind, oracle):
    return {"empty": b"", "nul": oracle[:-1] + b"\0", "cr": oracle[:-1] + b"\r",
            "lf": oracle[:-1] + b"\n", "truncated": oracle[:-1], "over-limit": oracle + b"a" * 129}.get(kind, oracle)


class ShortIO(SystemIO):
    @staticmethod
    def write(fd, value):
        # Deliberate synthetic short-write adapter, NOT an OS fault claim.
        return os.write(fd, value[:-1])


def one_case(kind):
    import grp
    import pwd
    commands = Commands()
    oracle = b"B1AC" + secrets.token_hex(32).encode("ascii")
    candidate = variant(kind, oracle)
    purpose = secrets.token_hex(16)
    user = "cpc" + secrets.token_hex(5)
    unit = "cpa-b1ac-" + secrets.token_hex(8) + ".service"
    root = Path(tempfile.mkdtemp(prefix="cpa-b1ac.", dir="/opt"))
    root.chmod(0o755)
    host = Path(tempfile.mkdtemp(prefix="cpa-b1ac-host.", dir="/var/tmp"))
    host.chmod(0o755)
    first = None
    created = started = unit_stopped = cleanup_ok = False
    read_fd = write_fd = None
    process = sender = out = err = None
    hello_raw = b""
    expected_group = None
    receiver_pid = None
    inventory = {k: (b"", False) for k in ("unit-out", "unit-err", "journal", "outer-files")}
    inventory.update({"inner-" + k: (b"", False) for k in INNER_SURFACES})
    host_socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    public = {}
    try:
        for lookup in (pwd.getpwnam, grp.getgrnam):
            try:
                lookup(user)
            except KeyError:
                pass
            else:
                raise Failure("c-setup")
        require(get_unit(commands, unit).get("LoadState") == "not-found", "c-unit")
        selected_uid = None
        for _ in range(32):
            number = 20000 + secrets.randbelow(20000)
            if number in USED_UIDS:
                continue
            try:
                pwd.getpwuid(number)
            except KeyError:
                pass
            else:
                continue
            try:
                grp.getgrgid(number)
            except KeyError:
                selected_uid = number
                break
        require(selected_uid is not None, "c-setup")
        USED_UIDS.add(selected_uid)
        commands.run(["useradd", "--system", "--uid", str(selected_uid), "--user-group", "--no-create-home",
                      "--shell", "/usr/sbin/nologin", "-K", "CREATE_MAIL_SPOOL=no", user])
        created = True
        ident = pwd.getpwnam(user)
        require(ident.pw_uid == selected_uid and ident.pw_gid != 0, "c-setup")
        uid, gid = ident.pw_uid, ident.pw_gid
        work = root / "work"
        private_dir(work, uid, gid)
        for leaf in ("home", "config", "data", "cache", "state", "runtime", "tmp"):
            private_dir(work / leaf, uid, gid)
        for name in ("fixture.py", "receiver.py", "channel.py"):
            shutil.copyfile(HERE / name, root / name)
            (root / name).chmod(0o444)
        for name in ("target.public", "neighbor.public"):
            public[work / name] = public_file(work / name, b"public-C-unchanged", uid, gid)
        socket_path = host / "probe.sock"
        host_socket.bind(str(socket_path))
        socket_path.chmod(0o666)  # A newly made PUBLIC isolation probe only.
        host_socket.listen(16)
        host_shm = Path("/dev/shm") / (root.name + ".public")
        public[host_shm] = public_file(host_shm, b"public-C-host-shm", 0, 0)
        host_shm.chmod(0o644)
        public[host_shm] = (host_shm.lstat(), b"public-C-host-shm")
        # This UID can reach only our synthetic host probe outside the unit.
        commands.run(["runuser", "-u", user, "--", "/usr/bin/python3", "-B", str(root / "fixture.py"), "--baseline", str(socket_path)])
        bus_config = root / "bus.conf"
        bus_config.write_text('<busconfig><type>session</type><listen>unix:path=' + str(work / "runtime" / "bus")
                              + '</listen><auth>EXTERNAL</auth><policy user="' + str(uid)
                              + '"><allow own="*"/><allow send_destination="*"/><allow receive_sender="*"/></policy></busconfig>', encoding="ascii")
        bus_config.chmod(0o444)
        host_uid = int(os.environ["SUDO_UID"])
        metadata = dict(uid=uid, gid=gid, purpose=purpose, work=str(work), host=str(host),
                        host_home=pwd.getpwuid(host_uid).pw_dir, host_shm=str(host_shm), host_socket=str(socket_path),
                        netns=os.readlink("/proc/self/ns/net"), ipcns=os.readlink("/proc/self/ns/ipc"), bus_config=str(bus_config))
        path = root / "metadata.json"
        path.write_text(json.dumps(metadata), encoding="ascii")
        path.chmod(0o444)
        environment = dict(SAFE)
        for variable, leaf in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
                               ("XDG_CACHE_HOME", "cache"), ("XDG_STATE_HOME", "state"),
                               ("XDG_RUNTIME_DIR", "runtime"), ("TMPDIR", "tmp")):
            environment[variable] = str(work / leaf)
        read_fd, write_fd = os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)
        identity = pipe_identity(write_fd)
        require(pipe_identity(read_fd) == identity, "c-pipe")
        args = ["systemd-run", "--quiet", "--wait", "--collect", "--pipe", "--unit=" + unit]
        args += ["--property=" + p for p in unit_properties(user, work, host)]
        args += ["/usr/bin/env", "-i"] + [key + "=" + value for key, value in environment.items()]
        args += ["/usr/bin/python3", "-B", str(root / "fixture.py"), "--inside", str(path)]
        started = True
        process = subprocess.Popen(args, stdin=read_fd, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   env=SAFE, close_fds=True, bufsize=0)
        close_checked(read_fd)
        read_fd = None
        err = Capture(process.stderr)
        hello_raw = read_line(process.stdout.fileno(), time.monotonic() + 15)
        out = Capture(process.stdout, limit=600000)
        hello = json.loads(hello_raw)
        require(set(hello) == {"pid", "uid", "pipe"} and hello["uid"] == uid and tuple(hello["pipe"]) == identity, "c-identity")
        receiver_pid = hello["pid"]
        require(type(receiver_pid) is int and receiver_pid > 1, "c-identity")
        properties = get_unit(commands, unit)
        expected_group = properties.get("ControlGroup")
        require(expected_group is not None and expected_group.startswith("/system.slice/" + unit), "c-unit")
        target = observe(receiver_pid)
        executable = Path("/usr/bin/python3").stat()
        require(target.uid == uid and target.executable == (executable.st_dev, executable.st_ino)
                and target.pipe == identity and target.cgroup == ("0::" + expected_group + "\n").encode(), "c-identity")
        from dataclasses import replace
        if kind == "wrong-uid":
            target = replace(target, uid=0)
        elif kind == "wrong-pid":
            target = replace(target, pid=0)
        elif kind == "wrong-exe":
            target = replace(target, executable=(-1, -1))
        elif kind == "wrong-object":
            target = replace(target, pipe=(-1, *target.pipe[1:]))
        observed_error = None
        if kind == "early-eof":
            close_checked(write_fd)
            write_fd = None
        else:
            try:
                sender = OneShot(write_fd, target, purpose, time.monotonic() + (-1 if kind == "deadline" else 5),
                                 ShortIO() if kind == "short-write" else None)
                sender.send(candidate, purpose)
                if kind == "duplicate":
                    sender.send(candidate, purpose)
            except Failure as e:
                observed_error = e.args[0]
            finally:
                if sender and sender.closed:
                    write_fd = None
                if write_fd is not None:
                    close_checked(write_fd)
                    write_fd = None
        require(observed_error == EXPECTED[kind], "c-protocol")
        require(process.wait(timeout=35) == 0, "c-unit")
        raw_out, complete_out = out.finish()
        raw_err, complete_err = err.finish()
        out = err = None
        inventory["unit-out"] = (hello_raw + raw_out, complete_out)
        inventory["unit-err"] = (raw_err, complete_err)
        packet = json.loads(raw_out)
        require(set(packet["captures"]) == INNER_SURFACES and set(packet["complete"]) == INNER_SURFACES
                and packet["label"] is None and packet["children_waited"] is True, "c-capture")
        for name in INNER_SURFACES:
            inventory["inner-" + name] = (base64.b64decode(packet["captures"][name], validate=True), packet["complete"][name] is True)
        if kind in ("positive", "duplicate"):
            require(packet["receiver_code"] == 0 and not inventory["inner-receiver-err"][0], "c-receipt")
            check_receipt(inventory["inner-receiver-out"][0], oracle, purpose)
        else:
            require(packet["receiver_code"] == 1 and not inventory["inner-receiver-out"][0], "c-receipt")
            expected_receiver = "c-length" if kind == "short-write" else "c-empty"
            require(inventory["inner-receiver-err"][0] == ("FAIL: " + expected_receiver + "\n").encode(), "c-receipt")
    except Failure as e:
        first = e.args[0]
    except Exception:
        first = "c-internal"
    finally:
        cleanup_ok = True
        def cleanup(action):
            nonlocal first, cleanup_ok
            first, ok = cleanup_action(first, action)
            cleanup_ok &= ok
        for fd in (read_fd, write_fd):
            if fd is not None:
                cleanup(lambda fd=fd: close_checked(fd))
        def stop_unit():
            nonlocal unit_stopped
            if started:
                commands.run(["systemctl", "stop", unit], check=False)
                state = get_unit(commands, unit)
                require(state.get("ActiveState") in ("inactive", "failed") and state.get("LoadState") in ("loaded", "not-found"), "c-unit")
                if expected_group:
                    group = Path("/sys/fs/cgroup") / expected_group.lstrip("/")
                    require(not group.exists() or (group / "cgroup.events").read_text().split("populated ", 1)[1].split()[0] == "0", "c-unit")
                if receiver_pid:
                    require(not (Path("/proc") / str(receiver_pid)).exists(), "c-unit")
            unit_stopped = True
        cleanup(stop_unit)
        def wait_runner():
            if process:
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
                    raise Failure("c-wait")
        cleanup(wait_runner)
        if out:
            raw, complete = out.finish()
            inventory["unit-out"] = (hello_raw + raw, complete)
        elif process and not inventory["unit-out"][1]:
            # Failure before the hello: still drain and scan the entire tail.
            capture = Capture(process.stdout, limit=600000)
            raw, complete = capture.finish()
            inventory["unit-out"] = (hello_raw + raw, complete)
        if err:
            inventory["unit-err"] = err.finish()
        def journal():
            require(unit_stopped, "c-unit")
            commands.run(["journalctl", "--sync"])
            _, raw = commands.run(["journalctl", "--unit=" + unit, "--all", "--no-pager", "--output=export"])
            inventory["journal"] = (raw, True)
        if started:
            cleanup(journal)
        else:
            inventory["journal"] = (b"", False)
        def verify_public():
            require(unit_stopped, "c-unit")
            for path, (before, content) in public.items():
                now = path.lstat()
                require((now.st_dev, now.st_ino, now.st_mode, now.st_uid, now.st_gid, now.st_size)
                        == (before.st_dev, before.st_ino, before.st_mode, before.st_uid, before.st_gid, before.st_size)
                        and path.read_bytes() == content, "c-public-state")
        cleanup(verify_public)
        # Root-owned sources/metadata and unit-owned work files are both read;
        # no scan of a real HOME, credential directory or unrelated journal.
        def outer_files():
            collected = bytearray()
            complete = True
            for path in root.iterdir():
                if path.is_dir() and not path.is_symlink():
                    raw, ok = snapshot_private([path], uid)
                    collected += raw
                    complete &= ok
                else:
                    s = path.lstat()
                    require(stat.S_ISREG(s.st_mode) and s.st_uid == 0 and s.st_nlink == 1 and s.st_size < 65536, "c-scan")
                    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
                    try:
                        require(stable(os.fstat(fd)) == stable(s), "c-scan")
                        raw = os.read(fd, 65536)
                        require(len(raw) == s.st_size and not os.read(fd, 1) and stable(os.fstat(fd)) == stable(s), "c-scan")
                        collected += raw
                    finally:
                        close_checked(fd)
            require(len(collected) < 300000, "c-scan")
            inventory["outer-files"] = (bytes(collected), complete)
        cleanup(outer_files)
        cleanup(host_socket.close)
        if created:
            cleanup(lambda: require(unit_stopped, "c-unit"))
            if unit_stopped:
                cleanup(lambda: commands.run(["userdel", user]))
                def verify_identity_removed():
                    try:
                        pwd.getpwnam(user)
                    except KeyError:
                        pass
                    else:
                        raise Failure("c-cleanup")
                    try:
                        group = grp.getgrnam(user)
                    except KeyError:
                        return
                    require(group.gr_gid == gid and not group.gr_mem, "c-cleanup")
                    commands.run(["groupdel", user])
                    try:
                        grp.getgrnam(user)
                    except KeyError:
                        return
                    raise Failure("c-cleanup")
                cleanup(verify_identity_removed)
        for path in tuple(public):
            if path.parent == Path("/dev/shm"):
                cleanup(lambda path=path: path.unlink())
        def remove_owned(path, parent, prefix):
            require(unit_stopped and path.parent == parent and path.name.startswith(prefix)
                    and path.resolve() == path and not path.is_symlink(), "c-cleanup")
            shutil.rmtree(path)
            require(not path.exists(), "c-cleanup")
        cleanup(lambda: remove_owned(root, Path("/opt"), "cpa-b1ac."))
        cleanup(lambda: remove_owned(host, Path("/var/tmp"), "cpa-b1ac-host."))
        inventory.update(commands.inventory)
        expected_inventory = {"unit-out", "unit-err", "journal", "outer-files"} | {"inner-" + k for k in INNER_SURFACES} | set(commands.inventory)
        clear, complete = scan(oracle, inventory, expected_inventory)
        cleanup_ok &= complete
        if candidate and len(candidate) >= 8:
            second_clear, second_complete = scan(candidate, inventory, expected_inventory)
            clear &= second_clear
            complete &= second_complete
        if kind == "short-write":
            partial_clear, partial_complete = scan(oracle[:-1], inventory, expected_inventory)
            clear &= partial_clear
            complete &= partial_complete
        if not clear:
            first = first or "c-content"
        if not complete:
            first = first or "c-capture"
        if not cleanup_ok:
            first = first or "c-cleanup"
        # Oracle is retained until ALL stop/wait/capture/scan/cleanup actions.
        if first:
            print("FIXTURE_DIAGNOSTIC " + (first if first in LABELS else "c-internal"), flush=True)
            print("CHECK C_CONTENT_CLEAR=" + str(clear).lower(), flush=True)
            print("CHECK C_CAPTURE_COMPLETE=" + str(complete).lower(), flush=True)
            print("CHECK C_OUTER_CLEANUP=" + str(cleanup_ok and unit_stopped).lower(), flush=True)
            raise Failure(first)
        print("CHECK C_CASE_" + kind.upper().replace("-", "_") + "=true", flush=True)
        print("FIXTURE_DIAGNOSTIC c-content-clear", flush=True)
        print("FIXTURE_DIAGNOSTIC c-capture-complete", flush=True)
        print("FIXTURE_DIAGNOSTIC c-owned-cleanup-ok", flush=True)


def main():
    require(sys.platform == "linux" and os.geteuid() == 0, "c-setup")
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted"
            and os.environ.get("RUNNER_OS") == "Linux" and int(os.environ.get("SUDO_UID", "0")) != 0, "c-setup")
    require(Path("/proc/1/comm").read_text().strip() == "systemd", "c-setup")
    import resource
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    os.umask(0o077)
    commands = Commands()
    commands.run(["systemd-detect-virt", "--vm"])
    # The dedicated workflow installs D-Bus only, never GNOME/Secret Service.
    for kind in CASES:
        one_case(kind)
    print("PASS: synthetic receiver bytes/EOF; owned waits/closes; clear AND complete; no GNOME", flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            sys.exit(inside(sys.argv[2]))
        elif len(sys.argv) == 3 and sys.argv[1] == "--baseline":
            probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            probe.settimeout(1)
            probe.connect(sys.argv[2])
            probe.close()
        else:
            require(len(sys.argv) == 1, "c-setup")
            main()
    except Failure as e:
        print("FAIL: " + e.args[0], file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("FAIL: c-internal", file=sys.stderr)
        sys.exit(1)
