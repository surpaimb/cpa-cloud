"""Independent B1a-C launcher. Disposable GitHub Linux VM only; no GNOME.

All raw captures and the synthetic oracle stay in outer memory. Only fixed
labels leave this program. Existing preflight/run.py is neither imported nor
modified. See CONTRACT.md for the narrower acceptance claim.
"""
import base64
import errno
import hashlib
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
                     OwnedFD, OwnedStream, check_receipt, close_checked, observe, pipe_identity,
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
SETUP_STAGES = frozenset({"root-create", "root-mode", "host-create", "host-mode", "socket-create",
    "socket-bind", "socket-mode", "socket-listen", "identity-absent", "unit-absent", "user-create",
    "user-identity", "private-dir", "source-copy", "source-mode", "public-file", "host-shm",
    "baseline", "bus-config", "metadata", "input-pipe", "unit-launch", "target-hello", "target-bind",
    "isolation", "bus-launch", "bus-ready", "receiver-launch", "helper-capture",
    "runner-guard", "systemd-guard", "vm-check"})
CLEANUP_STAGES = frozenset({"root", "host", "socket", "user", "host-shm", "input-read", "input-write",
    "helper-process", "unit-runner", "receiver-process", "bus-process", "unit", "runner-wait",
    "journal", "public-state", "outer-files"})
USER_CREATE_EXIT_CLASSES = {0: "success", 1: "password-file", 2: "syntax", 3: "option",
    4: "uid-in-use", 6: "group-missing", 9: "name-in-use", 10: "group-file", 12: "home", 14: "selinux"}
RESULT_DIAGNOSTICS = frozenset({"c-content-clear", "c-capture-complete", "c-owned-cleanup-ok"})
DIAGNOSTICS = frozenset(
    ["c-setup-" + stage + "-" + status for stage in SETUP_STAGES for status in ("entered", "ok", "rejected")]
    + ["c-cleanup-" + stage + "-" + status for stage in CLEANUP_STAGES
       for status in ("acquired", "entered", "ok", "rejected", "not-acquired", "consumed-confirmed", "consumed-unconfirmed")]
    + ["c-user-create-exit-" + name for name in (*USER_CREATE_EXIT_CLASSES.values(), "unknown", "signal", "missing-result")]
    + list(LABELS | RESULT_DIAGNOSTICS)
    + ["c-helper-evidence-stop", "c-helper-observation-unknown", "c-helper-source-unknown",
       "c-helper-readonly-cleanup-ok", "c-helper-readonly-cleanup-rejected",
       "c-runtime-evidence-stop", "c-runtime-prerequisite-unknown", "c-runtime-source-match",
       "c-runtime-source-rejected", "c-runtime-source-unknown", "c-runtime-external-unknown",
       "c-runtime-readonly-cleanup-ok", "c-runtime-readonly-cleanup-rejected"])
REPORTS = frozenset(["CHECK " + name + "=" + value
    for name in ("C_CONTENT_CLEAR", "C_CAPTURE_COMPLETE", "C_OUTER_CLEANUP") for value in ("true", "false")]
    + ["CHECK C_CASE_" + kind.upper().replace("-", "_") + "=true" for kind in CASES]
    + ["PASS: synthetic receiver bytes/EOF; owned waits/closes; clear AND complete; no GNOME"])


def user_create_exit_class(result):
    """Only the already-observed useradd return result, never stderr or a probe.

    Names describe documented result classes, NOT an independently verified
    underlying cause or the absence of partially completed side effects.
    """
    if result is None:
        name = "missing-result"
    elif type(result) is not int:
        name = "unknown"  # no conversion, formatting or equality on bait objects
    elif result < 0:
        name = "signal"
    else:
        name = USER_CREATE_EXIT_CLASSES.get(result, "unknown")
    return "c-user-create-exit-" + name


class Diagnostics:
    def __init__(self, sink=None, report_sink=None):
        self.sink = sink or (lambda value: print("FIXTURE_DIAGNOSTIC " + value, flush=True))
        self.report_sink = report_sink or (lambda value: print(value, flush=True))
        self.delivery_failed = False
        self.cleanup_depth = 0
        self.helper_evidence_attempted = False
        self.runtime_helper_attempts, self.runtime_hook_attempts = set(), set()

    def _deliver(self, sink, value):
        try:
            sink(value)
        except BaseException:
            # Do not retain/format a raw sink exception. Loss is sticky even
            # when the next write succeeds; delivery never controls a cleaner.
            self.delivery_failed = True

    def emit(self, value):
        require(value in DIAGNOSTICS, "c-protocol")
        self._deliver(self.sink, value)

    def report(self, value):
        require(value in REPORTS, "c-protocol")
        self._deliver(self.report_sink, value)

    def require_delivery(self):
        require(not self.delivery_failed, "c-capture")

    def setup_gate(self):
        if not self.cleanup_depth:
            self.require_delivery()

    def cleaning(self, action):
        self.cleanup_depth += 1
        try:
            return action()
        finally:
            self.cleanup_depth -= 1

    def run(self, stage, action):
        require(stage in SETUP_STAGES, "c-protocol")
        self.emit("c-setup-" + stage + "-entered")
        self.setup_gate()  # refuse further acquisition after lost diagnostics
        try:
            result = action()
        except Exception:
            self.emit("c-setup-" + stage + "-rejected")
            raise
        self.emit("c-setup-" + stage + "-ok")
        self.setup_gate()
        return result


class Resources:
    """Register successful acquisition before any subsequent operation."""
    def __init__(self, diag=None):
        self.diag = diag or Diagnostics()
        self.entries = []
        self.ok, self.first = True, None
        self.primary = None
        self.cleaner_errors = []

    def __enter__(self):
        return self

    def __exit__(self, kind, error, traceback):
        self.primary = error
        first = error.args[0] if isinstance(error, Failure) else ("c-internal" if error else None)
        first, ok = self.cleanup(first)
        if error is None and (not self.ok or (not ok and not self.diag.cleanup_depth)):
            raise Failure(first)
        return False  # Never replace the original exception with cleanup failure.

    def own(self, stage, value, cleaner):
        require(stage in CLEANUP_STAGES, "c-protocol")
        self.entries.append((stage, value, cleaner))
        self.diag.emit("c-cleanup-" + stage + "-acquired")
        self.diag.setup_gate()
        return value

    def acquire(self, stage, factory, cleaner):
        require(stage in CLEANUP_STAGES, "c-protocol")
        self.diag.setup_gate()
        value = factory()
        return self.own(stage, value, cleaner)

    def process(self, stage, factory, capture_factory=None, record=None, owner_factory=None):
        require(stage in CLEANUP_STAGES, "c-protocol")
        self.diag.setup_gate()
        record = record or ProcessRecord()
        record.process = factory()  # retain before any report or full owner
        index = len(self.entries)
        self.entries.append((stage, record, lambda value: value.cleanup()))
        # returncode is an existing observation, not an added poll/wait. The
        # guard is already installed before the success callback can fail.
        record.observe(record.process.returncode)
        record.raise_handoff()
        self.diag.emit("c-cleanup-" + stage + "-acquired")
        self.diag.setup_gate()
        owner = (owner_factory or ProcessOwner)(record.process, capture_factory, record)
        record.owner = owner
        self.entries[index] = (stage, owner, lambda value: value.cleanup())
        return owner

    def pipe_pair(self, factory):
        self.diag.setup_gate()
        raw = factory()
        read, write = OwnedFD(raw[0]), OwnedFD(raw[1])
        self.entries.extend((("input-read", read, lambda value: value.close()),
                             ("input-write", write, lambda value: value.close())))
        self.diag.emit("c-cleanup-input-read-acquired")
        self.diag.emit("c-cleanup-input-write-acquired")
        self.diag.setup_gate()
        return read, write

    def _release(self, entry):
        stage, value, action = entry
        if isinstance(value, (OwnedFD, OwnedStream)) and value.consumed:
            self.diag.emit("c-cleanup-" + stage + ("-consumed-confirmed" if value.confirmed else "-consumed-unconfirmed"))
            if not value.confirmed:
                self.ok = False
                self.first = self.first or "c-close"
                self.cleaner_errors.append("c-close")
            return  # never even call the closer with a consumed descriptor
        self.diag.emit("c-cleanup-" + stage + "-entered")
        try:
            self.diag.cleaning(lambda: action(value))
        except Exception as e:
            self.ok = False
            fixed = e.args[0] if isinstance(e, Failure) else "c-cleanup"
            self.cleaner_errors.append(fixed)
            self.first = self.first or fixed
            self.diag.emit("c-cleanup-" + stage + "-rejected")
        else:
            self.diag.emit("c-cleanup-" + stage + "-ok")

    def release(self, value, first=None):
        self.first = first or self.first
        for index, entry in enumerate(self.entries):
            if entry[1] is value:
                del self.entries[index]
                self._release(entry)
                return self.result()
        raise Failure("c-cleanup")

    def cleanup(self, first=None):
        self.first = first or self.first
        while self.entries:
            self._release(self.entries.pop())
        return self.result()

    def result(self):
        # self.ok is the physical cleaner result; delivery is independent.
        return (self.first or ("c-capture" if self.diag.delivery_failed else None),
                self.ok and not self.diag.delivery_failed)


class ProcessRecord:
    """Private raw acquisition/result ledger; never a probe or public output."""
    def __init__(self, owned_success=None):
        self.process = self.owner = None
        self.result = self.primary = None
        self.owned_success = owned_success
        self.handoff_attempted, self.handoff_error = False, None
        self.cleanup_consumed, self.cleanup_failure = False, None

    def observe(self, result):
        # Only a result already obtained by the existing process operations.
        # A later absent result must not erase a confirmed terminal result.
        if result is not None or self.result is None:
            self.result = result
        if type(result) is int and result == 0 and not self.handoff_attempted and self.owned_success is not None:
            self.handoff_attempted = True  # callback failure never permits retry
            try:
                self.owned_success()
            except BaseException as error:
                # Defer this error so the original wait and every owned stream
                # close still run. Keep the object private and separate.
                self.handoff_error = error

    def raise_handoff(self):
        if self.handoff_error is not None:
            raise self.handoff_error

    def cleanup(self):
        if self.cleanup_consumed:
            require(self.cleanup_failure is None, self.cleanup_failure or "c-cleanup")
            return
        self.cleanup_consumed = True
        try:
            ProcessOwner.raw_cleanup(self.process, self)
        except Exception as error:
            self.cleanup_failure = error.args[0] if isinstance(error, Failure) else "c-cleanup"
            raise


class ProcessOwner:
    def __init__(self, process, capture_factory=None, record=None):
        self.process = process
        self.record = record
        self.streams = {"out": OwnedStream(process.stdout), "err": OwnedStream(process.stderr)}
        self.captures, self.results, self.attempted = {}, {}, set()
        self.capture_factory = capture_factory or Capture
        self.consumed, self.failure = False, None

    def capture(self, name, limit=65536):
        require(name not in self.attempted, "c-capture")
        self.attempted.add(name)
        capture = self.capture_factory(self.streams[name], limit=limit, start=False)
        self.captures[name] = capture  # retained before thread start can fail
        try:
            capture.start()
        except Exception:
            capture.error = True
            raise
        return capture

    @staticmethod
    def raw_cleanup(process, record=None):
        first = None
        try:
            stop_child(process, record)
        except Exception:
            first = "c-wait"
        for stream in (process.stdout, process.stderr):
            try:
                OwnedStream(stream).close()
            except Exception:
                first = first or "c-close"
        if first:
            raise Failure(first)

    def cleanup(self):
        if self.consumed:
            require(self.failure is None, self.failure or "c-capture")
            return
        self.consumed = True
        try:
            stop_child(self.process, self.record)
        except Exception:
            self.failure = "c-wait"
        for name, owner in self.streams.items():
            try:
                if name in self.captures:
                    self.results[name] = self.captures[name].finish()
                else:
                    owner.close()
                    self.results[name] = (b"", False)  # unvisited capture is not complete
                if not self.results[name][1]:
                    self.failure = self.failure or "c-capture"
            except Exception:
                self.results[name] = (b"", False)
                self.failure = self.failure or "c-close"
        if self.failure:
            raise Failure(self.failure)


def cleanup_action(first, action):
    try:
        action()
        return first, True
    except Exception as e:
        return first or (e.args[0] if isinstance(e, Failure) else "c-cleanup"), False


def diagnosed_cleanup(diag, first, action, stage):
    """Physical cleanup proceeds despite sink loss; no raw diagnostic escapes."""
    require(stage in CLEANUP_STAGES, "c-protocol")
    diag.emit("c-cleanup-" + stage + "-entered")
    first, ok = cleanup_action(first, lambda: diag.cleaning(action))
    diag.emit("c-cleanup-" + stage + ("-ok" if ok else "-rejected"))
    return first, ok


def finish_case(diag, kind, first, clear, complete, cleanup_ok, unit_stopped):
    require(kind in CASES, "c-protocol")
    complete &= not diag.delivery_failed
    cleanup_ok &= complete
    first = first or ("c-content" if not clear else None)
    first = first or ("c-capture" if not complete else None)
    first = first or ("c-cleanup" if not cleanup_ok or not unit_stopped else None)
    if first:
        diag.emit(first if first in LABELS else "c-internal")
        diag.report("CHECK C_CONTENT_CLEAR=" + str(clear).lower())
        diag.report("CHECK C_CAPTURE_COMPLETE=" + str(complete and not diag.delivery_failed).lower())
        diag.report("CHECK C_OUTER_CLEANUP=" + str(cleanup_ok and unit_stopped and not diag.delivery_failed).lower())
        raise Failure(first)
    diag.emit("c-content-clear")
    diag.emit("c-capture-complete")
    diag.emit("c-owned-cleanup-ok")
    diag.require_delivery()  # no case admission after any earlier lost report
    diag.report("CHECK C_CASE_" + kind.upper().replace("-", "_") + "=true")
    diag.require_delivery()  # a failed final case write also remains nonzero


def failure_output(label, sink=None):
    # Even the top-level fallback must not leak a nested BrokenPipe traceback.
    try:
        fixed = "FAIL: " + (label if label in LABELS else "c-internal")
        (sink or (lambda value: print(value, file=sys.stderr, flush=True)))(fixed)
    except BaseException:
        pass


def stop_child(process, record=None):
    try:
        polled = process.poll()
        if record is not None:
            record.observe(polled)
        if polled is None:
            process.terminate()
        waited = process.wait(timeout=3)
        if record is not None:
            record.observe(waited)
    except Exception:
        # A later forced cleanup must not turn an earlier wait failure green.
        try:
            process.kill()
            waited = process.wait(timeout=3)
            if record is not None:
                record.observe(waited)
        except Exception:
            pass
        raise Failure("c-wait") from None


class Commands:
    def __init__(self, diag=None, process_factory=None, capture_factory=None, owner_factory=None):
        self.inventory = {}
        self.number = 0
        self.diag = diag or Diagnostics()
        self.process_factory = process_factory or subprocess.Popen
        self.capture_factory = capture_factory
        self.owner_factory = owner_factory
        self.last_record = None

    def run(self, args, timeout=10, check=True, owned_success=None, user_create=False):
        # Every helper's two streams are registered before waiting. Names and
        # commands contain only public fixture metadata, never the oracle.
        self.number += 1
        name = "helper-" + str(self.number)
        self.inventory[name + "-out"] = (b"", False)
        self.inventory[name + "-err"] = (b"", False)
        record = ProcessRecord(owned_success)
        self.last_record = record  # independent of Resources.process returning
        try:
            with Resources(self.diag) as owned:
                owner = owned.process("helper-process", lambda: self.process_factory(
                    args, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE, env=SAFE, close_fds=True), self.capture_factory,
                    record=record, owner_factory=self.owner_factory)
                out = self.diag.run("helper-capture", lambda: owner.capture("out"))
                err = self.diag.run("helper-capture", lambda: owner.capture("err"))
                expired = False
                try:
                    code = owner.process.wait(timeout=timeout)
                except subprocess.TimeoutExpired:
                    expired = True
                    owner.process.kill()
                    code = owner.process.wait(timeout=3)
                record.observe(code)
                record.raise_handoff()
                self.inventory[name + "-out"] = out.finish()
                self.inventory[name + "-err"] = err.finish()
                require(not expired, "c-wait")
                require(all(self.inventory[k][1] for k in (name + "-out", name + "-err")), "c-capture")
                require(not check or code == 0, "c-setup")
        finally:
            pending = sys.exc_info()[1]
            record.primary = pending
            try:
                if record.owner is not None:
                    for stream, result in record.owner.results.items():
                        self.inventory[name + "-" + stream] = result
                if pending is None and record.handoff_error is not None:
                    record.primary = record.handoff_error
                    record.raise_handoff()
            finally:
                reporting_pending = sys.exc_info()[1]
                if user_create:
                    self.diag.emit(user_create_exit_class(record.result))
                    if reporting_pending is None:
                        self.diag.setup_gate()
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


APPROVED_HELPER_SOURCES = ()  # Historical C #5 interface never approves observations.
# Pre-reviewed official publication profile, NOT a runtime execution allowlist.
# Original .deb SHA256 139b51228c8065eefa3051f649d8480eb986f1cf629448e49d2eaf9813318412;
# signed noble-updates Packages/Sources and build28578437: see PROVENANCE.md.
# userdel/groupdel were mechanically hashed from that same authenticated .deb,
# not learned from the runner. No binary/source implementation is imported.
UBUNTU_SOURCE_VERSION = "1:4.13+dfsg1-4ubuntu3.2"
UBUNTU_HELPER_SHA256 = (
    ("useradd", "cf687323ab26e097237f5e106bf2a461f699840489ffe97871ca8111a2a220bd"),
    ("userdel", "c7b1b5a0df79fefc5f758fef348c351370f6aa4e03bd4c89378d7464368034cc"),
    ("groupdel", "70a1f44dc280ae371557dff117d1e64b6aed74aa55d2ac80dea618634970669b"))
UBUNTU_HOOK_PATHS = tuple((role, "/etc/shadow-maint/" + role + ".d") for role in
    ("useradd-pre", "useradd-post", "userdel-pre", "userdel-post"))
RUNTIME_HOOK_ENTRY_LIMIT, RUNTIME_HOOK_SECONDS = 256, 2.0


class EvidenceOS:
    open, stat, fstat = staticmethod(os.open), staticmethod(os.stat), staticmethod(os.fstat)
    read, close = staticmethod(os.read), staticmethod(close_checked)
    scandir, which = staticmethod(os.scandir), staticmethod(shutil.which)
    now = staticmethod(time.monotonic)

    @staticmethod
    def flags(directory):
        return os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC | (os.O_DIRECTORY if directory else 0)


class EvidenceFiles:
    """Root/OS trusted; nofollow read snapshots, not a check-to-exec claim."""
    def __init__(self, ops=None):
        self.ops, self.held, self.seen, self.absent = ops if ops is not None else EvidenceOS(), [], {}, set()
        self.clean, self.first = True, None
        self.edges, self.missing_edges = [], {}

    def hold(self, path, directory=False, missing=False):
        require(self.clean, "c-close")  # no new acquisition after unconfirmed close
        require(type(path) is str and path.startswith("/") and not any(p in ("", ".", "..") for p in path[1:].split("/")), "c-scan")
        parent, current = None, ""
        parts = ["/"] + path[1:].split("/")
        for index, part in enumerate(parts):
            current = "/" if index == 0 else current.rstrip("/") + "/" + part
            is_dir = index < len(parts) - 1 or directory
            kwargs = {} if parent is None else {"dir_fd": parent.fileno()}
            try:
                before = self.ops.stat(part, follow_symlinks=False, **kwargs)
            except FileNotFoundError:
                if missing and index == len(parts) - 1:
                    self.absent.add(current)
                    self.missing_edges[current] = (parent, part)
                    return None
                raise
            require((stat.S_ISDIR(before.st_mode) if is_dir else stat.S_ISREG(before.st_mode))
                    and before.st_uid == 0 and not before.st_mode & 0o022, "c-scan")
            flags = self.ops.flags(is_dir)
            owner = OwnedFD(self.ops.open(part, flags, **kwargs))
            self.held.append((owner, stable(before)))  # before any subsequent failure
            self.edges.append((owner, parent, part, stable(before)))
            require(stable(self.ops.fstat(owner.fileno())) == stable(before), "c-scan")
            require(current not in self.seen or self.seen[current] == stable(before), "c-scan")
            self.seen[current] = stable(before)
            parent = owner
        return parent

    def bytes(self, owner, limit):
        before = self.ops.fstat(owner.fileno())
        require(stat.S_ISREG(before.st_mode) and 0 <= before.st_size <= limit, "c-scan")
        chunks, count = [], 0
        while True:
            raw = self.ops.read(owner.fileno(), min(65536, limit - count + 1))
            require(type(raw) is bytes, "c-scan")
            if not raw:
                break
            chunks.append(raw)
            count += len(raw)
            require(count <= limit, "c-scan")
        require(count == before.st_size and stable(self.ops.fstat(owner.fileno())) == stable(before), "c-scan")
        return b"".join(chunks)

    def close_to(self, mark=0):
        while len(self.held) > mark:
            owner, _ = self.held.pop()  # consumption precedes OS close; never retry
            self.edges = [edge for edge in self.edges if edge[0] is not owner]
            self.missing_edges = {path: edge for path, edge in self.missing_edges.items() if edge[0] is not owner}
            try:
                owner.close(self.ops.close)
            except BaseException as error:
                self.clean, self.first = False, self.first or error

    def read(self, path, limit):
        mark = len(self.held)
        try:
            return self.bytes(self.hold(path), limit)
        finally:
            self.close_to(mark)

    def names(self, path):
        mark = len(self.held)
        try:
            owner = self.hold(path, directory=True)
            names = []
            with self.ops.scandir(owner.fileno()) as entries:
                for entry in entries:
                    require(len(names) < 65536, "c-scan")
                    names.append(entry.name)
            return names
        finally:
            self.close_to(mark)

    def recheck(self):
        # Recheck held edges relative to the original held parents, never by
        # following a replacement ancestor. Closed metadata snapshots below
        # are also rechecked; no future check-to-exec guarantee is claimed.
        for owner, parent, part, before in self.edges:
            kwargs = {} if parent is None else {"dir_fd": parent.fileno()}
            require(stable(self.ops.stat(part, follow_symlinks=False, **kwargs)) == before, "c-scan")
        for path, before in self.seen.items():
            require(stable(self.ops.stat(path, follow_symlinks=False)) == before, "c-scan")
        for path in self.absent:
            edge = self.missing_edges.get(path)
            try:
                if edge is None:
                    self.ops.stat(path, follow_symlinks=False)
                else:
                    parent, part = edge
                    self.ops.stat(part, dir_fd=parent.fileno(), follow_symlinks=False)
            except FileNotFoundError:
                continue
            raise Failure("c-scan")
        for owner, before in self.held:
            require(stable(self.ops.fstat(owner.fileno())) == before, "c-scan")

    def empty_directory(self, path):
        mark = len(self.held)
        try:
            owner = self.hold(path, directory=True, missing=True)
            state = "absent"
            if owner is not None:
                state = "empty"
                with self.ops.scandir(owner.fileno()) as entries:
                    for _ in entries:
                        state = "nonempty"
                        break  # refusal needs no successful empty-directory claim
            self.recheck()
        except BaseException as error:
            self.first = self.first or error
            state = "unknown"
        finally:
            self.close_to(mark)
        return state if self.clean else "unknown"


def dpkg_identity(files, path, digest):
    status = files.read("/var/lib/dpkg/status", 16777216).decode("utf-8")
    owners, total = [], 0
    for name in files.names("/var/lib/dpkg/info"):
        if not name.endswith(".list"):
            continue
        require(re.fullmatch(r"[a-z0-9][a-z0-9+.-]{0,63}(?::[a-z0-9-]{1,32})?\.list", name) is not None, "c-scan")
        raw = files.read("/var/lib/dpkg/info/" + name, 4194304)
        total += len(raw)
        require(total <= 67108864, "c-scan")
        if path.encode("utf-8") in raw.splitlines():
            owners.append(name[:-5])
    require(len(owners) == 1, "c-scan")
    stem = owners[0]
    package, _, qualifier = stem.partition(":")
    records = []
    for block in status.split("\n\n"):
        fields = {}
        for line in block.splitlines():
            if line and not line[0].isspace() and ": " in line:
                key, value = line.split(": ", 1)
                require(key not in fields, "c-scan")
                fields[key] = value
        if fields.get("Package") == package and (not qualifier or fields.get("Architecture") == qualifier):
            records.append(fields)
    require(len(records) == 1 and records[0].get("Status") == "install ok installed", "c-scan")
    version, arch = records[0].get("Version"), records[0].get("Architecture")
    require(type(version) is str and re.fullmatch(r"[0-9][A-Za-z0-9.+:~\-]{0,127}", version)
            and type(arch) is str and re.fullmatch(r"[a-z0-9][a-z0-9-]{0,31}", arch), "c-scan")
    sums = files.read("/var/lib/dpkg/info/" + stem + ".md5sums", 4194304).decode("ascii")
    expected = [line[:32] for line in sums.splitlines()
                if re.fullmatch(r"[a-f0-9]{32}  " + re.escape(path[1:]), line)]
    require(len(expected) == 1, "c-scan")
    return package, version, arch, "match" if expected[0] == digest else "mismatch"


class HelperProbe:
    def __init__(self, ops=None, package_reader=None, role="useradd"):
        require(type(role) is str and role in dict(UBUNTU_HELPER_SHA256), "c-protocol")
        self.role = role
        self.files = EvidenceFiles(ops)
        self.package_reader = package_reader if package_reader is not None else dpkg_identity
        self.result, self.first, self.clean = {}, None, True

    def collect(self):
        try:
            path = self.files.ops.which(self.role, path=SAFE["PATH"])
            require(path is not None, "c-scan")
            owner = self.files.hold(path)
            require(self.files.ops.fstat(owner.fileno()).st_mode & 0o111, "c-scan")
            raw = self.files.bytes(owner, 8388608)
            self.result.update(path_class="/usr/sbin/" + self.role if path == "/usr/sbin/" + self.role else "other",
                               sha256=hashlib.sha256(raw).hexdigest(), stability="stable")
            package, version, arch, binding = self.package_reader(self.files, path,
                hashlib.md5(raw, usedforsecurity=False).hexdigest())
            require(self.files.clean, "c-close")
            self.result.update(package=package, version=version, arch=arch, package_binding=binding)
            # This collector observes only. The outer batch separately compares
            # the pre-reviewed profile; never approve or update from observation.
        except BaseException as error:
            self.first = self.first or error
        return self.result

    def recheck(self):
        require(self.files.clean, "c-close")
        self.files.recheck()

    def close(self):
        self.files.close_to()
        self.clean, self.first = self.files.clean, self.first or self.files.first


def evidence_payload(data):
    data = data if type(data) is dict else {}
    result = {key: "unknown" for key in ("path_class", "package", "version", "arch", "sha256",
        "stability", "package_binding", "source", "hooks", "precondition")}
    patterns = {"package": r"[a-z0-9][a-z0-9+.-]{0,63}", "version": r"[0-9][A-Za-z0-9.+:~\-]{0,127}",
                "arch": r"[a-z0-9][a-z0-9-]{0,31}", "sha256": r"[a-f0-9]{64}"}
    enums = {"path_class": ("/usr/sbin/useradd", "other"), "stability": ("stable", "changed"),
             "package_binding": ("match", "mismatch")}
    for key, value in data.items():
        if type(key) is str and type(value) is str and ((key in patterns and re.fullmatch(patterns[key], value)) or
                                  (key in enums and value in enums[key])):
            result[key] = value
    return result  # source/precondition always unknown; no self-approval path


def emit_helper_evidence(diag, data):
    require(not diag.helper_evidence_attempted and not diag.runtime_helper_attempts
            and not diag.runtime_hook_attempts, "c-protocol")
    diag.helper_evidence_attempted = True
    line = "HELPER_EVIDENCE " + json.dumps(evidence_payload(data), sort_keys=True, separators=(",", ":"))
    require(len(line) <= 768, "c-protocol")
    diag._deliver(diag.report_sink, line)  # narrowly approved non-secret exception


def helper_evidence_preflight(diag, probe=None):
    if probe is None:
        require(sys.platform == "linux" and os.geteuid() == 0 and os.environ.get("GITHUB_ACTIONS") == "true"
                and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted", "c-setup")
        # Real entry points use the new pre-reviewed runtime-metadata batch.
        # The optional old probe interface remains only for retained C #5
        # controlled public tests, and still unconditionally refuses execution.
        return runtime_prerequisites_preflight(diag)
    data, first = {}, None
    try:
        data = probe.collect()
        first = probe.first
        probe.recheck()
    except BaseException as error:
        first = first or error
        data.update(stability="changed", sha256="unknown")
    finally:
        try:
            probe.close()
        except BaseException as error:
            first, probe.clean = first or error, False
    first = first or probe.first
    if not probe.clean:
        data.update(stability="unknown", sha256="unknown")
    emit_helper_evidence(diag, data)
    diag.emit("c-helper-readonly-cleanup-" + ("ok" if probe.clean else "rejected"))
    diag.emit("c-helper-source-unknown")
    if first:
        diag.emit("c-helper-observation-unknown")
    diag.emit("c-helper-evidence-stop")
    if isinstance(first, Failure):
        raise first
    diag.require_delivery()
    raise Failure("c-scan" if first or not probe.clean else "c-setup")


def helper_source_status(role, data):
    """Compare fixed expectations; observed source/approval fields are ignored."""
    if type(data) is not dict or type(role) is not str or role not in dict(UBUNTU_HELPER_SHA256):
        return "unknown"
    keys = ("path_class", "package", "version", "arch", "sha256", "stability", "package_binding")
    if any(type(data.get(key)) is not str or data[key] == "unknown" for key in keys):
        return "unknown"
    expected = ("/usr/sbin/"+role, "passwd", UBUNTU_SOURCE_VERSION, "amd64",
                dict(UBUNTU_HELPER_SHA256)[role], "stable", "match")
    return "published-match" if tuple(data[key] for key in keys) == expected else "rejected"


class HookProbe:
    """Metadata only, complete bounded enumeration; all descriptors retained."""
    def __init__(self, role, ops=None):
        require(type(role) is str and role in dict(UBUNTU_HOOK_PATHS), "c-protocol")
        self.role, self.path = role, dict(UBUNTU_HOOK_PATHS)[role]
        self.files, self.owner = EvidenceFiles(ops), None
        self.state, self.first, self.clean = "unknown", None, True

    def scan(self):
        entries, first, count = None, None, 0
        deadline = self.files.ops.now() + RUNTIME_HOOK_SECONDS
        try:
            entries = self.files.ops.scandir(self.owner.fileno())
            for _ in entries:
                count += 1  # never inspect, format, read or execute an entry
                require(count <= RUNTIME_HOOK_ENTRY_LIMIT and self.files.ops.now() <= deadline, "c-scan")
            require(self.files.ops.now() <= deadline, "c-scan")  # EOF, not an early empty claim
        except BaseException as error:
            first = error
        finally:
            if entries is not None:
                closing, entries = entries, None  # consume before close, no retry
                try:
                    closing.close()
                except BaseException as error:
                    self.files.clean = False
                    self.files.first = self.files.first or error
                    first = first or error
        if first is not None:
            raise first
        return "nonempty" if count else "empty"

    def collect(self):
        try:
            self.owner = self.files.hold(self.path, directory=True, missing=True)
            self.state = "absent" if self.owner is None else self.scan()
            self.files.recheck()
        except BaseException as error:
            self.first, self.state = self.first or error, "unknown"
        return self.state

    def recheck(self):
        try:
            require(self.first is None and self.files.clean, "c-scan")
            self.files.recheck()
            if self.owner is not None:
                require(self.scan() == self.state, "c-scan")
                self.files.recheck()
        except BaseException as error:
            self.first, self.state = self.first or error, "unknown"
        return self.state

    def close(self):
        self.files.close_to()
        self.clean, self.first = self.files.clean, self.first or self.files.first
        if not self.clean:
            self.state = "unknown"


class RuntimePrerequisiteBatch:
    """No acquisition can authorize execution; this batch always stops."""
    def __init__(self, helper_factory=None, hook_factory=None):
        self.helper_factory = helper_factory if helper_factory is not None else lambda role: HelperProbe(role=role)
        self.hook_factory = hook_factory if hook_factory is not None else HookProbe
        self.helpers, self.hooks = {}, {}
        self.helper_results = {role: {} for role, _ in UBUNTU_HELPER_SHA256}
        self.hook_results = {role: "unknown" for role, _ in UBUNTU_HOOK_PATHS}
        self.attempted, self.closed, self.clean, self.first = False, False, True, None

    def collect(self):
        if self.attempted:
            return  # no second acquisition, fallback or self-updating profile
        self.attempted = True
        try:
            for role, _ in UBUNTU_HELPER_SHA256:
                probe = self.helper_factory(role)
                self.helpers[role] = probe  # retain before any collect failure
                self.helper_results[role] = probe.collect()
                if probe.first is not None:
                    raise probe.first
                try:
                    probe.recheck()  # before any source-specific hook inspection
                except BaseException:
                    self.helper_results[role].update(stability="changed", sha256="unknown")
                    raise
                require(helper_source_status(role, self.helper_results[role]) == "published-match", "c-scan")
            # Never inspect guessed hook paths before every actual candidate
            # is bound to the exact independently pre-reviewed publication.
            for role, _ in UBUNTU_HOOK_PATHS:
                probe = self.hook_factory(role)
                self.hooks[role] = probe
                self.hook_results[role] = probe.collect()
                if probe.first is not None:
                    raise probe.first
                require(self.hook_results[role] in ("absent", "empty"), "c-scan")
        except BaseException as error:
            self.first = self.first or error

    def recheck(self):
        require(not self.closed, "c-scan")
        for role, probe in self.helpers.items():
            try:
                probe.recheck()
            except BaseException as error:
                self.first = self.first or error
                self.helper_results[role].update(stability="changed", sha256="unknown")
        for role, probe in self.hooks.items():
            self.hook_results[role] = probe.recheck()
            self.first = self.first or probe.first

    def close(self):
        if self.closed:
            return
        self.closed = True  # consumed even after a close failure or sink loss
        for role, probe in reversed(tuple(self.hooks.items())):
            try:
                probe.close()
            except BaseException as error:
                probe.clean, self.first = False, self.first or error
            if not probe.clean:
                self.clean, self.hook_results[role] = False, "unknown"
            self.first = self.first or probe.first
        for role, probe in reversed(tuple(self.helpers.items())):
            try:
                probe.close()
            except BaseException as error:
                probe.clean, self.first = False, self.first or error
            if not probe.clean:
                self.clean = False
                self.helper_results[role].update(stability="unknown", sha256="unknown")
            self.first = self.first or probe.first


def emit_runtime_helper(diag, role, data):
    require(not diag.helper_evidence_attempted and type(role) is str and role in dict(UBUNTU_HELPER_SHA256)
            and role not in diag.runtime_helper_attempts, "c-protocol")
    diag.runtime_helper_attempts.add(role)  # consumed before formatting/delivery
    data = data if type(data) is dict else {}
    record = {"role": role, "version": "unknown", "arch": "unknown", "sha256": "unknown",
              "stability": "unknown", "package_binding": "unknown",
              "source": helper_source_status(role, data), "precondition": "unknown"}
    patterns = {"version": r"[0-9][A-Za-z0-9.+:~\-]{0,127}", "arch": r"[a-z0-9][a-z0-9-]{0,31}",
                "sha256": r"[a-f0-9]{64}"}
    enums = {"stability": ("stable", "changed"), "package_binding": ("match", "mismatch")}
    for key in (*patterns, *enums):
        value = data.get(key)
        if type(value) is str and ((key in patterns and re.fullmatch(patterns[key], value)) or
                                  (key in enums and value in enums[key])):
            record[key] = value
    line = "RUNTIME_HELPER_EVIDENCE " + json.dumps(record, sort_keys=True, separators=(",", ":"))
    require(line.isascii() and len(line) <= 768, "c-protocol")
    diag._deliver(diag.report_sink, line)


def emit_hook_evidence(diag, role, state):
    require(not diag.helper_evidence_attempted and type(role) is str and role in dict(UBUNTU_HOOK_PATHS)
            and role not in diag.runtime_hook_attempts, "c-protocol")
    diag.runtime_hook_attempts.add(role)
    state = state if type(state) is str and state in ("absent", "empty", "nonempty") else "unknown"
    line = "HOOK_EVIDENCE " + json.dumps({"role": role, "state": state, "precondition": "unknown"},
                                        sort_keys=True, separators=(",", ":"))
    require(line.isascii() and len(line) <= 768, "c-protocol")
    diag._deliver(diag.report_sink, line)


def runtime_prerequisites_preflight(diag, batch=None):
    if batch is None:
        require(sys.platform == "linux" and os.geteuid() == 0 and os.environ.get("GITHUB_ACTIONS") == "true"
                and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted", "c-setup")
        batch = RuntimePrerequisiteBatch()
    try:
        diag.require_delivery()
        batch.collect()
        batch.recheck()
    except BaseException as error:
        batch.first = batch.first or error
    finally:
        try:
            batch.close()
        except BaseException as error:
            batch.clean, batch.first = False, batch.first or error
    # Only after all closes are attempted. A failed sink cannot skip cleanup,
    # retry a consumed role/descriptor, expose a raw error or authorize a case.
    for role, _ in UBUNTU_HELPER_SHA256:
        emit_runtime_helper(diag, role, batch.helper_results[role])
    for role, _ in UBUNTU_HOOK_PATHS:
        emit_hook_evidence(diag, role, batch.hook_results[role])
    sources = [helper_source_status(role, batch.helper_results[role]) for role, _ in UBUNTU_HELPER_SHA256]
    diag.emit("c-runtime-source-" + ("match" if all(value == "published-match" for value in sources)
                                  else "rejected" if "rejected" in sources else "unknown"))
    diag.emit("c-runtime-readonly-cleanup-" + ("ok" if batch.clean else "rejected"))
    diag.emit("c-runtime-external-unknown")  # caches/tally/ZSys/USERDEL_CMD/libs/exec binding
    diag.emit("c-runtime-prerequisite-unknown")
    diag.emit("c-runtime-evidence-stop")
    if isinstance(batch.first, Failure):
        raise batch.first
    if batch.first is not None or not batch.clean:
        raise Failure("c-scan") from None
    diag.require_delivery()
    raise Failure("c-setup")  # unconditional predeclared stop, NEVER C PASS


class ScanOps:
    lstat = staticmethod(os.lstat)
    stat = staticmethod(os.stat)
    fstat = staticmethod(os.fstat)
    open = staticmethod(os.open)
    read = staticmethod(os.read)
    close = staticmethod(close_checked)
    scandir = staticmethod(os.scandir)


def snapshot_private(roots, uid, ops=None):
    """Read only newly created private namespaces; no host credential scan.

    Unknown type, symlink, hardlink, over-limit or read/identity failure means
    incomplete. No truncate-and-clear result is possible.
    """
    data, count = bytearray(), 0
    ops = ops or ScanOps()

    def visit(path, before, parent=None, name=None):
        nonlocal count
        require(stat.S_ISDIR(before.st_mode), "c-scan")
        fd = OwnedFD(ops.open(path if parent is None else name,
                             os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC,
                             **({} if parent is None else {"dir_fd": parent})))
        try:
            require(stable(ops.fstat(fd.fileno())) == stable(before), "c-scan")
            names = []
            with ops.scandir(fd.fileno()) as entries:
                for entry in entries:
                    names.append(entry.name)
                    require(len(names) < 128, "c-scan")
            for child in names:
                require(child not in (".", "..") and "/" not in child, "c-scan")
                s = ops.stat(child, dir_fd=fd.fileno(), follow_symlinks=False)
                child_path = path / child
                if stat.S_ISDIR(s.st_mode):
                    require(s.st_uid == uid, "c-scan")
                    visit(child_path, s, fd.fileno(), child)
                elif stat.S_ISSOCK(s.st_mode):
                    require(child == "bus" and s.st_uid == uid, "c-scan")
                else:
                    require(stat.S_ISREG(s.st_mode) and s.st_uid == uid and s.st_nlink == 1
                            and s.st_size <= 32768, "c-scan")
                    leaf = OwnedFD(ops.open(child, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC,
                                            dir_fd=fd.fileno()))
                    try:
                        held = ops.fstat(leaf.fileno())
                        require(stable(held) == stable(s), "c-scan")
                        chunk = ops.read(leaf.fileno(), 32769)
                        require(len(chunk) == held.st_size and not ops.read(leaf.fileno(), 1), "c-scan")
                        require(stable(ops.fstat(leaf.fileno())) == stable(held)
                                and stable(ops.stat(child, dir_fd=fd.fileno(), follow_symlinks=False)) == stable(s), "c-scan")
                        data.extend(chunk)
                        count += 1
                        require(count <= 128 and len(data) <= 32768, "c-scan")
                    finally:
                        leaf.close(ops.close)
            require(stable(ops.fstat(fd.fileno())) == stable(before)
                    and stable(ops.lstat(path)) == stable(before), "c-scan")
        finally:
            fd.close(ops.close)

    try:
        roots = tuple(roots)
        require(bool(roots), "c-scan")
        for root in roots:
            root = Path(root)
            visit(root, ops.lstat(root))
        return bytes(data), True
    except Exception:
        return bytes(data), False


def isolation(metadata, diag=None):
    diag = diag or Diagnostics(sink=lambda value: print("FIXTURE_DIAGNOSTIC " + value, file=sys.stderr, flush=True))
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
            with Resources(diag) as probes:
                probes.acquire("socket", lambda: socket.socket(family, socket.SOCK_STREAM), lambda value: value.close())
                raise Failure("c-isolation")
        except OSError as e:
            require(e.errno in (errno.EAFNOSUPPORT, errno.EPERM, errno.EACCES), "c-isolation")
    for path in (metadata["host_socket"], "/run/user/" + str(uid) + "/bus"):
        with Resources(diag) as probes:
            sock = probes.acquire("socket", lambda: socket.socket(socket.AF_UNIX, socket.SOCK_STREAM), lambda value: value.close())
            sock.settimeout(.5)
            try:
                sock.connect(path)
            except OSError as e:
                require(e.errno in (errno.ENOENT, errno.EACCES, errno.EPERM, errno.ECONNREFUSED), "c-isolation")
            else:
                raise Failure("c-isolation")


def inside(path):
    metadata = json.loads(Path(path).read_bytes())
    inventory = {k: (b"", False) for k in INNER_SURFACES}
    diag = Diagnostics(sink=lambda value: print("FIXTURE_DIAGNOSTIC " + value, file=sys.stderr, flush=True))
    resources = Resources(diag)
    bus_owner = child_owner = None
    bus = child = None
    bus_out = bus_err = child_out = child_err = None
    first = None
    code, hello = -1, b""
    try:
        diag.run("isolation", lambda: isolation(metadata, diag))
        bus_owner = diag.run("bus-launch", lambda: resources.process("bus-process", lambda: subprocess.Popen(
            ["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog", "--config-file=" + metadata["bus_config"]],
            env=dict(os.environ), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)))
        bus = bus_owner.process
        bus_out, bus_err = bus_owner.capture("out"), bus_owner.capture("err")
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
        with Resources(diag) as probes:
            probe = probes.acquire("socket", lambda: socket.socket(socket.AF_UNIX, socket.SOCK_STREAM), lambda value: value.close())
            probe.settimeout(1)
            probe.connect(str(socket_path))
            peer = struct.unpack("3i", probe.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            require(peer[:2] == (bus.pid, metadata["uid"]), "c-bus")
        environment = dict(os.environ, DBUS_SESSION_BUS_ADDRESS="unix:path=" + str(socket_path))
        child_owner = diag.run("receiver-launch", lambda: resources.process("receiver-process", lambda: subprocess.Popen(
            ["/usr/bin/python3", "-B", str(Path(path).parent / "receiver.py"), metadata["purpose"]],
            env=environment, stdin=0, stdout=subprocess.PIPE, stderr=subprocess.PIPE, close_fds=True)))
        child = child_owner.process
        close_checked(0)  # The launcher is not a second reader of the password.
        child_err = child_owner.capture("err")
        hello = read_line(child.stdout.fileno(), time.monotonic() + 5)
        # Private protocol only: the outer oracle consumes this, never relays it.
        require(os.write(1, hello) == len(hello), "c-protocol")
        child_out = child_owner.capture("out")
        code = child.wait(timeout=25)
        require(code in (0, 1), "c-wait")
    except Failure as e:
        first = e.args[0]
    except Exception:
        first = "c-internal"
    finally:
        first, _ = resources.cleanup(first)
        first = first or ("c-capture" if diag.delivery_failed else None)
        for prefix, owner in (("receiver", child_owner), ("bus", bus_owner)):
            if owner:
                for name, result in owner.results.items():
                    inventory[prefix + "-" + name] = result
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


def public_file(path, content, uid, gid, resources=None):
    fd = OwnedFD(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600))
    try:
        if resources is not None:
            resources.own("host-shm", path, lambda value: value.unlink())
        require(os.write(fd.fileno(), content) == len(content), "c-setup")
        os.fchown(fd.fileno(), uid, gid)
        s = os.fstat(fd.fileno())
        require(stat.S_IMODE(s.st_mode) == 0o600 and s.st_nlink == 1, "c-setup")
    finally:
        fd.close()
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
    helper_evidence_preflight(Diagnostics())  # mandatory C-only stop; no user creation
    import grp
    import pwd
    diag = Diagnostics()
    resources = Resources(diag)
    commands = Commands(diag)
    oracle = b"B1AC" + secrets.token_hex(32).encode("ascii")
    candidate = variant(kind, oracle)
    purpose = secrets.token_hex(16)
    user = "cpc" + secrets.token_hex(5)
    unit = "cpa-b1ac-" + secrets.token_hex(8) + ".service"
    root = host = host_socket = None
    uid = gid = None
    first = None
    created = started = unit_stopped = cleanup_ok = False
    read_fd = write_fd = None
    process = runner_owner = sender = out = err = None
    hello_raw = b""
    expected_group = None
    receiver_pid = None
    inventory = {k: (b"", False) for k in ("unit-out", "unit-err", "journal", "outer-files")}
    inventory.update({"inner-" + k: (b"", False) for k in INNER_SURFACES})
    public = {}

    def remove_owned(value, parent, prefix):
        path = Path(value)
        require(unit_stopped and path.parent == parent and path.name.startswith(prefix)
                and path.resolve() == path and not path.is_symlink(), "c-cleanup")
        shutil.rmtree(path)
        require(not path.exists(), "c-cleanup")

    def remove_user(_):
        require(unit_stopped, "c-unit")
        commands.run(["userdel", user])
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
        require((gid is None or group.gr_gid == gid) and not group.gr_mem, "c-cleanup")
        commands.run(["groupdel", user])
        try:
            grp.getgrnam(user)
        except KeyError:
            return
        raise Failure("c-cleanup")

    try:
        root_name = diag.run("root-create", lambda: resources.acquire("root",
            lambda: tempfile.mkdtemp(prefix="cpa-b1ac.", dir="/opt"),
            lambda value: remove_owned(value, Path("/opt"), "cpa-b1ac.")))
        root = Path(root_name)
        diag.run("root-mode", lambda: root.chmod(0o755))
        host_name = diag.run("host-create", lambda: resources.acquire("host",
            lambda: tempfile.mkdtemp(prefix="cpa-b1ac-host.", dir="/var/tmp"),
            lambda value: remove_owned(value, Path("/var/tmp"), "cpa-b1ac-host.")))
        host = Path(host_name)
        diag.run("host-mode", lambda: host.chmod(0o755))
        host_socket = diag.run("socket-create", lambda: resources.acquire("socket",
            lambda: socket.socket(socket.AF_UNIX, socket.SOCK_STREAM), lambda value: value.close()))
        def absent(lookup):
            try:
                lookup(user)
            except KeyError:
                pass
            else:
                raise Failure("c-setup")
        for lookup in (pwd.getpwnam, grp.getgrnam):
            diag.run("identity-absent", lambda lookup=lookup: absent(lookup))
        diag.run("unit-absent", lambda: require(get_unit(commands, unit).get("LoadState") == "not-found", "c-unit"))
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
        diag.run("user-identity", lambda: require(selected_uid is not None, "c-setup"))
        USED_UIDS.add(selected_uid)
        diag.run("user-create", lambda: commands.run(
            ["useradd", "--system", "--uid", str(selected_uid), "--user-group", "--no-create-home",
             "--shell", "/usr/sbin/nologin", "-K", "CREATE_MAIL_SPOOL=no", user],
            owned_success=lambda: resources.own("user", user, remove_user), user_create=True))
        created = True
        ident = diag.run("user-identity", lambda: pwd.getpwnam(user))
        diag.run("user-identity", lambda: require(ident.pw_uid == selected_uid and ident.pw_gid != 0, "c-setup"))
        uid, gid = ident.pw_uid, ident.pw_gid
        work = root / "work"
        diag.run("private-dir", lambda: private_dir(work, uid, gid))
        for leaf in ("home", "config", "data", "cache", "state", "runtime", "tmp"):
            diag.run("private-dir", lambda leaf=leaf: private_dir(work / leaf, uid, gid))
        for name in ("fixture.py", "receiver.py", "channel.py"):
            diag.run("source-copy", lambda name=name: shutil.copyfile(HERE / name, root / name))
            diag.run("source-mode", lambda name=name: (root / name).chmod(0o444))
        for name in ("target.public", "neighbor.public"):
            public[work / name] = diag.run("public-file", lambda name=name: public_file(work / name, b"public-C-unchanged", uid, gid))
        socket_path = host / "probe.sock"
        diag.run("socket-bind", lambda: host_socket.bind(str(socket_path)))
        diag.run("socket-mode", lambda: socket_path.chmod(0o666))  # public probe
        diag.run("socket-listen", lambda: host_socket.listen(16))
        host_shm = Path("/dev/shm") / (root.name + ".public")
        public[host_shm] = diag.run("host-shm", lambda: public_file(host_shm, b"public-C-host-shm", 0, 0, resources))
        diag.run("host-shm", lambda: host_shm.chmod(0o644))
        public[host_shm] = (host_shm.lstat(), b"public-C-host-shm")
        # This UID can reach only our synthetic host probe outside the unit.
        diag.run("baseline", lambda: commands.run(["runuser", "-u", user, "--", "/usr/bin/python3", "-B", str(root / "fixture.py"), "--baseline", str(socket_path)]))
        bus_config = root / "bus.conf"
        diag.run("bus-config", lambda: bus_config.write_text('<busconfig><type>session</type><listen>unix:path=' + str(work / "runtime" / "bus")
                              + '</listen><auth>EXTERNAL</auth><policy user="' + str(uid)
                              + '"><allow own="*"/><allow send_destination="*"/><allow receive_sender="*"/></policy></busconfig>', encoding="ascii"))
        diag.run("bus-config", lambda: bus_config.chmod(0o444))
        host_uid = int(os.environ["SUDO_UID"])
        metadata = dict(uid=uid, gid=gid, purpose=purpose, work=str(work), host=str(host),
                        host_home=pwd.getpwuid(host_uid).pw_dir, host_shm=str(host_shm), host_socket=str(socket_path),
                        netns=os.readlink("/proc/self/ns/net"), ipcns=os.readlink("/proc/self/ns/ipc"), bus_config=str(bus_config))
        path = root / "metadata.json"
        diag.run("metadata", lambda: path.write_text(json.dumps(metadata), encoding="ascii"))
        diag.run("metadata", lambda: path.chmod(0o444))
        environment = dict(SAFE)
        for variable, leaf in (("HOME", "home"), ("XDG_CONFIG_HOME", "config"), ("XDG_DATA_HOME", "data"),
                               ("XDG_CACHE_HOME", "cache"), ("XDG_STATE_HOME", "state"),
                               ("XDG_RUNTIME_DIR", "runtime"), ("TMPDIR", "tmp")):
            environment[variable] = str(work / leaf)
        read_fd, write_fd = diag.run("input-pipe", lambda: resources.pipe_pair(lambda: os.pipe2(os.O_CLOEXEC | os.O_NONBLOCK)))
        identity = pipe_identity(write_fd)
        require(pipe_identity(read_fd) == identity, "c-pipe")
        args = ["systemd-run", "--quiet", "--wait", "--collect", "--pipe", "--unit=" + unit]
        args += ["--property=" + p for p in unit_properties(user, work, host)]
        args += ["/usr/bin/env", "-i"] + [key + "=" + value for key, value in environment.items()]
        args += ["/usr/bin/python3", "-B", str(root / "fixture.py"), "--inside", str(path)]
        def launch():
            nonlocal process, started
            process = subprocess.Popen(args, stdin=read_fd.fileno(), stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       env=SAFE, close_fds=True, bufsize=0)
            started = True
            return process
        runner_owner = diag.run("unit-launch", lambda: resources.process("unit-runner", launch))
        closing = read_fd
        read_fd = None
        release_first, release_ok = resources.release(closing)
        require(release_ok, release_first or "c-close")
        err = runner_owner.capture("err")
        hello_raw = diag.run("target-hello", lambda: read_line(process.stdout.fileno(), time.monotonic() + 15))
        out = runner_owner.capture("out", limit=600000)
        hello = diag.run("target-hello", lambda: json.loads(hello_raw))
        diag.run("target-bind", lambda: require(set(hello) == {"pid", "uid", "pipe"} and hello["uid"] == uid
                  and tuple(hello["pipe"]) == identity, "c-identity"))
        receiver_pid = hello["pid"]
        require(type(receiver_pid) is int and receiver_pid > 1, "c-identity")
        properties = get_unit(commands, unit)
        expected_group = properties.get("ControlGroup")
        require(expected_group is not None and expected_group.startswith("/system.slice/" + unit), "c-unit")
        target = observe(receiver_pid)
        executable = Path("/usr/bin/python3").stat()
        diag.run("target-bind", lambda: require(target.uid == uid and target.executable == (executable.st_dev, executable.st_ino)
                and target.pipe == identity and target.cgroup == ("0::" + expected_group + "\n").encode(), "c-identity"))
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
            closing = write_fd
            write_fd = None
            release_first, release_ok = resources.release(closing)
            require(release_ok, release_first or "c-close")
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
                closing = write_fd
                write_fd = None
                if closing is not None:
                    release_first, release_ok = resources.release(closing)
                    if not release_ok:
                        raise Failure(observed_error or release_first or "c-close")
        if observed_error != EXPECTED[kind]:
            raise Failure(observed_error or "c-protocol")
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
        def cleanup(action, stage):
            nonlocal first, cleanup_ok
            first, ok = diagnosed_cleanup(diag, first, action, stage)
            cleanup_ok &= ok
        pending_fds = (read_fd, write_fd)
        read_fd = write_fd = None
        for fd in pending_fds:
            if fd is not None:
                first, ok = resources.release(fd, first)
                cleanup_ok &= ok
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
        if started:
            cleanup(stop_unit, "unit")
        else:
            unit_stopped = True
            diag.emit("c-cleanup-unit-not-acquired")
        def wait_runner():
            if process:
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
                    raise Failure("c-wait")
        if process:
            cleanup(wait_runner, "runner-wait")
        if runner_owner:
            if "out" not in runner_owner.attempted:
                # Only a never-attempted tail capture; failed constructors are
                # sticky and never retried. Ownership precedes this start.
                cleanup(lambda: runner_owner.capture("out", limit=600000), "unit-runner")
            first, ok = resources.release(runner_owner, first)
            cleanup_ok &= ok
            raw, complete = runner_owner.results.get("out", (b"", False))
            inventory["unit-out"] = (hello_raw + raw, complete)
            inventory["unit-err"] = runner_owner.results.get("err", (b"", False))
        def journal():
            require(unit_stopped, "c-unit")
            commands.run(["journalctl", "--sync"])
            _, raw = commands.run(["journalctl", "--unit=" + unit, "--all", "--no-pager", "--output=export"])
            inventory["journal"] = (raw, True)
        if started:
            cleanup(journal, "journal")
        else:
            inventory["journal"] = (b"", False)
        def verify_public():
            require(unit_stopped, "c-unit")
            for path, (before, content) in public.items():
                now = path.lstat()
                require((now.st_dev, now.st_ino, now.st_mode, now.st_uid, now.st_gid, now.st_size)
                        == (before.st_dev, before.st_ino, before.st_mode, before.st_uid, before.st_gid, before.st_size)
                        and path.read_bytes() == content, "c-public-state")
        if public:
            cleanup(verify_public, "public-state")
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
                    fd = OwnedFD(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC))
                    try:
                        require(stable(os.fstat(fd.fileno())) == stable(s), "c-scan")
                        raw = os.read(fd.fileno(), 65536)
                        require(len(raw) == s.st_size and not os.read(fd.fileno(), 1) and stable(os.fstat(fd.fileno())) == stable(s), "c-scan")
                        collected += raw
                    finally:
                        fd.close()
            require(len(collected) < 300000, "c-scan")
            inventory["outer-files"] = (bytes(collected), complete)
        if root is not None:
            cleanup(outer_files, "outer-files")
        first, resources_ok = resources.cleanup(first)
        cleanup_ok &= resources_ok
        for line in inventory["unit-err"][0].splitlines():
            prefix = b"FIXTURE_DIAGNOSTIC "
            if line.startswith(prefix):
                try:
                    fixed = line[len(prefix):].decode("ascii")
                except UnicodeError:
                    continue
                if fixed in DIAGNOSTICS:
                    diag.emit(fixed)
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
        # Oracle is retained until ALL stop/wait/capture/scan/cleanup actions.
        finish_case(diag, kind, first, clear, complete, cleanup_ok, unit_stopped)


def main():
    diag = Diagnostics()
    diag.run("runner-guard", lambda: require(sys.platform == "linux" and os.geteuid() == 0, "c-setup"))
    diag.run("runner-guard", lambda: require(os.environ.get("GITHUB_ACTIONS") == "true"
            and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted" and os.environ.get("RUNNER_OS") == "Linux"
            and int(os.environ.get("SUDO_UID", "0")) != 0, "c-setup"))
    diag.run("systemd-guard", lambda: require(Path("/proc/1/comm").read_text().strip() == "systemd", "c-setup"))
    import resource
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    os.umask(0o077)
    commands = Commands(diag)
    diag.run("vm-check", lambda: commands.run(["systemd-detect-virt", "--vm"]))
    helper_evidence_preflight(diag)  # this batch cannot enter any of the 15 cases


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            sys.exit(inside(sys.argv[2]))
        elif len(sys.argv) == 3 and sys.argv[1] == "--baseline":
            baseline_diag = Diagnostics(sink=lambda value: print("FIXTURE_DIAGNOSTIC " + value, file=sys.stderr, flush=True))
            with Resources(baseline_diag) as probes:
                probe = probes.acquire("socket", lambda: socket.socket(socket.AF_UNIX, socket.SOCK_STREAM), lambda value: value.close())
                probe.settimeout(1)
                probe.connect(sys.argv[2])
        else:
            require(len(sys.argv) == 1, "c-setup")
            main()
    except Failure as e:
        failure_output(e.args[0])
        sys.exit(1)
    except Exception:
        failure_output("c-internal")
        sys.exit(1)
