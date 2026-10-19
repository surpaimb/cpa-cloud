#!/usr/bin/env python3
"""Independent A3 supervisor extension. Explicit disposable Linux invocation only."""
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
a2_path = HERE / "a2.py" if (HERE / "a2.py").is_file() else HERE.parent / "interop-a2" / "fixture.py"
spec = importlib.util.spec_from_file_location("a3_a2", a2_path)
a2 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a2)
volatile, base, require = a2.volatile, a2.base, a2.require
TEST = "live::linux_secret_service::a3_fixture::isolated_item_delete_and_cleanup"
RUST_FAILURES = (
    "a3-blocked-tombstone-cardinality",
    "a3-blocked-tombstone-identity",
    "a3-blocked-tombstone-load",
    "a3-blocked-tombstone-reference",
    "a3-boundary-flush",
    "a3-boundary-unexpected-release",
    "a3-bus",
    "a3-case",
    "a3-cgroup",
    "a3-completion-proof",
    "a3-completion-record",
    "a3-corrupt-outcome",
    "a3-corrupt-reference",
    "a3-create",
    "a3-cross-bound-accepted",
    "a3-cross-phase",
    "a3-cross-tombstone",
    "a3-delete-send-count",
    "a3-deleted-key-open",
    "a3-directory-invalid",
    "a3-directory-metadata",
    "a3-duplicate-delete",
    "a3-durable-complete",
    "a3-early-delete-accepted",
    "a3-early-delete-sent",
    "a3-early-tombstone-authorized",
    "a3-exact-outcome",
    "a3-gap",
    "a3-home",
    "a3-journal-cardinality",
    "a3-journal-create",
    "a3-journal-load",
    "a3-key-delete",
    "a3-lock-approval",
    "a3-mode-invalid",
    "a3-mode",
    "a3-not-complete",
    "a3-private-changed",
    "a3-private-create",
    "a3-private-invalid",
    "a3-private-long",
    "a3-private-metadata",
    "a3-private-named-changed",
    "a3-private-named",
    "a3-private-parent-sync",
    "a3-private-parent",
    "a3-private-postwrite",
    "a3-private-prewrite",
    "a3-private-read",
    "a3-private-sync",
    "a3-private-write",
    "a3-proof-link",
    "a3-proof-read",
    "a3-proof-retain",
    "a3-provider",
    "a3-random",
    "a3-ready-flush",
    "a3-ready-release",
    "a3-real-replacement",
    "a3-reference",
    "a3-remove-authorization",
    "a3-replay-create-forbidden",
    "a3-replay-extra-delete",
    "a3-retire",
    "a3-root-identity",
    "a3-root-required",
    "a3-tamper-case",
    "a3-tampered-delete-accepted",
    "a3-tampered-phase",
    "a3-tampered-tombstone",
    "a3-tombstone-persist",
    "a3-tombstone-record",
    "a3-tombstone-remove",
    "a3-transaction",
    "a3-workspace-create",
    "a3-workspace-mode",
    "a3-workspace-remove",
    "a3-wrong-identity",
)
VERIFIER_FAILURES = {10: "identity", 11: "bus", 12: "invocation", 13: "work", 14: "directory", 15: "mode",
    20: "session", 21: "policy", 30: "reference", 31: "search", 32: "absence-cardinality",
    33: "collection-absence", 34: "present-cardinality", 35: "item", 36: "attributes", 37: "key-oracle",
    38: "secret-type", 39: "envelope", 40: "snapshot-write", 41: "unchanged"}
VERIFIER_LABELS = tuple("a3-verifier-" + v + "-failed" for v in tuple(VERIFIER_FAILURES.values()) + ("unknown",))
BOUNDARIES = ("terminal", "tombstone", "contents-retired", "workspace-removed", "deleted-before-confirm",
              "key-confirmed", "tombstone-intent", "inactive-proof", "complete")
PRESENT_BOUNDARIES = BOUNDARIES[:4]
TAMPERS = ("pending", "gap", "proof-inode", "proof-hardlink", "workspace-reappeared")
CASES = ("exact", "duplicate") + tuple(outcome + "-" + boundary for outcome in ("complete", "rollback")
                                      for boundary in BOUNDARIES) + tuple("tamper-" + v for v in TAMPERS) + (
    "cross-root", "cross-reference")
MARKERS = ("ISOLATION_OK", "PRIVATE_BUS_SERVICE_UID_PID_OK", "A3_CASE_OK", "OWNED_PROCESSES_STOPPED", "A3_SCAN_OK")
COMMON = ("setup", "policy")
EXACT = ("neighbor-create", "snapshot-neighbor", "exact-create", "snapshot-target", "exact-negatives",
         "target-after-negatives", "neighbor-after-negatives", "exact-delete", "target-after-delete",
         "neighbor-after-delete", "exact-missing", "target-after-missing", "neighbor-after-missing")
DURABLE = ("neighbor-create", "snapshot-neighbor", "start", "snapshot-target", "neighbor-at-ready",
           "target-after-crash", "neighbor-after-crash", "resume", "target-after-resume", "neighbor-after-resume",
           "complete-reentry", "final-target", "final-neighbor")
NEGATIVE = DURABLE[:5] + ("negative-target", "negative-neighbor")
DUPLICATE = ("duplicate-sequence", "duplicate-delete", "duplicate-unchanged")
LABELS = tuple(dict.fromkeys(COMMON + EXACT + DURABLE + NEGATIVE + DUPLICATE))
STATES = ("not-reached", "ok", "failed", "timeout", "missing", "incomplete", "clear", "matched", "complete")
FIELDS = tuple("operation-" + v for v in ("metadata", "isolation", "bus-start", "bus-ready", "service-start",
    "service-ready", "sequence", "runtime") + LABELS) + ("scan-content", "scan-completeness", "children", "handles") + tuple(
        "outer-" + v for v in ("operation", "unit", "unit-evidence", "sockets", "user", "shm", "work-root", "host-root", "cleanup"))
DIAGNOSTICS = tuple("a3-" + key + "-" + value for key in FIELDS for value in STATES) + tuple(
    "a3-first-" + v for v in ("metadata", "isolation", "bus-start", "bus-ready", "service-start", "service-ready",
                             "sequence", "runtime") + LABELS) + ("a3-first-none",)


def labels(case):
    if case == "exact":
        return COMMON + EXACT
    if case == "duplicate":
        return COMMON + DUPLICATE
    return COMMON + (NEGATIVE if case.startswith("tamper-") or case.startswith("cross-") else DURABLE)


def samples(case):
    if case == "duplicate":
        return a2.SAMPLES
    result = tuple((role + "/" + name, length) for role in ("neighbor", "target")
                   for name, length in (("sent.key", 32), ("sent.envelope", 148)))
    return result if case == "exact" else result + (("target/old.sample", 64), ("target/new.sample", 64))


def log_names(case):
    return ("bus.log", "keyring.log") + tuple("child-" + v + ".log" for v in labels(case))


def mark_complete(work, name):
    descriptor = os.open(work / (name + ".complete"), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and info.st_gid == os.getegid()
                and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1 and info.st_size == 0,
                "a3-capture-marker")
        stream.write(b"complete\n")
        stream.flush()
        os.fsync(stream.fileno())
    directory = os.open(work, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


class Report:
    def __init__(self):
        self.states = {key: "not-reached" for key in FIELDS}
        self.first = None

    def set(self, key, value):
        require(key in FIELDS and value in STATES, "a3-category-invalid")
        if self.states[key] not in ("failed", "timeout", "incomplete", "matched", "missing"):
            self.states[key] = value

    def run(self, label, action):
        try:
            result = action()
        except Exception as error:
            self.set("operation-" + label, "timeout" if isinstance(error, (TimeoutError, subprocess.TimeoutExpired)) else "failed")
            if self.first is None:
                self.first = label
            raise
        self.set("operation-" + label, "ok")
        return result

    def emit(self, outer=False):
        for key, value in self.states.items():
            if key.startswith("outer-") == outer:
                print("FIXTURE_DIAGNOSTIC a3-" + key + "-" + value, flush=True)
        if not outer:
            print("FIXTURE_DIAGNOSTIC a3-first-" + (self.first or "none"), flush=True)


def private_read(path, uid, gid, length=None):
    # Reject a replaced/symlink role directory before opening any leaf. The
    # work root itself cannot be renamed by the fixture UID (root-owned parent).
    parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        info = os.fstat(parent)
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == uid and info.st_gid == gid
                and stat.S_IMODE(info.st_mode) == 0o700, "a3-private-parent")
        descriptor = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent)
    finally:
        os.close(parent)
    with os.fdopen(descriptor, "rb") as stream:
        a = os.fstat(stream.fileno())
        require(stat.S_ISREG(a.st_mode) and a.st_uid == uid and a.st_gid == gid and a.st_nlink == 1
                and stat.S_IMODE(a.st_mode) == 0o600 and 0 <= a.st_size <= 4 * 1024 * 1024
                and (length is None or a.st_size == length), "a3-private-file")
        value = stream.read(a.st_size + 1)
        b = os.fstat(stream.fileno())
        require(len(value) == a.st_size and (a.st_dev, a.st_ino, a.st_mode, a.st_uid, a.st_gid, a.st_size, a.st_nlink)
                == (b.st_dev, b.st_ino, b.st_mode, b.st_uid, b.st_gid, b.st_size, b.st_nlink), "a3-private-changed")
        return value


def scan_all(work, case, uid, gid, extra, report):
    secrets, outputs, complete = [], list(extra), True
    for name, length in samples(case):
        try:
            secrets.append(private_read(work / name, uid, gid, length))
        except Exception:
            complete = False
    for name in log_names(case):
        try:
            outputs.append(private_read(work / name, uid, gid))
        except Exception:
            complete = False
        try:
            require(private_read(work / (name + ".complete"), uid, gid, 9) == b"complete\n", "a3-incomplete-log")
        except Exception:
            complete = False
    try:
        volatile.reject_secret_output(secrets, outputs)
        report.set("scan-content", "clear")
    except Exception:
        report.set("scan-content", "matched")
    report.set("scan-completeness", "complete" if complete else "incomplete")
    return complete and report.states["scan-completeness"] == "complete" and report.states["scan-content"] == "clear"


class Fixture(a2.Fixture):
    markers = MARKERS
    failures = a2.FAILURES + ("a3-failed",)
    diagnostics = DIAGNOSTICS + RUST_FAILURES + VERIFIER_LABELS + a2.DIAGNOSTICS + volatile.PANICS
    diagnostics_always = True

    def __init__(self, binary, case):
        super().__init__(binary)
        self.case, self.report, self.cursor = case, Report(), None

    def outer_diagnostic(self, stage, status):
        self.report.set("outer-" + stage, status)

    def prepare(self, root, metadata):
        super().prepare(root, metadata)
        # All scripts/policy/binaries remain root-owned before the UID runs.
        shutil.copyfile(a2_path, root / "a2.py")
        (root / "a2.py").chmod(0o444)
        (root / "interop.py").chmod(0o600)
        shutil.copyfile(HERE / "fixture.py", root / "interop.py")
        (root / "interop.py").chmod(0o444)
        config = Path(metadata["bus_config"])
        config.chmod(0o600)
        config.write_text((HERE / "bus.conf.in").read_text().replace("@USER@", base.USER)
                          .replace("@BUS_PATH@", str(Path(metadata["work"]) / "runtime/bus")))
        config.chmod(0o444)
        helper = root / "a3-verifier"
        flags = base.run(["pkg-config", "--cflags", "--libs", "libsecret-1"]).stdout.decode().split()
        base.run(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", str(HERE / "verifier.c"), "-o", str(helper), *flags])
        helper.chmod(0o555)
        for name, path in (("a3-verifier-source", HERE / "verifier.c"), ("a3-verifier-executable", helper),
                           ("a3-policy", HERE / "bus.conf.in"), ("a3-supervisor", HERE / "fixture.py")):
            print("FIXTURE " + name + " sha256=" + hashlib.sha256(path.read_bytes()).hexdigest(), flush=True)
        work = Path(metadata["work"])
        for name in ("target", "neighbor", "transaction", "other-transaction"):
            base.private(work / name, metadata["uid"], metadata["gid"])
        metadata.update(a3_verifier=str(helper), a3_case=self.case)
        # Registration is source-derived, before daemon startup/send/write.
        metadata["a3_samples"] = list(samples(self.case))
        metadata["a3_logs"] = list(log_names(self.case))
        metadata["a3_completions"] = [name + ".complete" for name in log_names(self.case)]

    def before_unit(self, metadata):
        result = base.run(["journalctl", "--no-pager", "--lines=0", "--show-cursor", "--output=cat"])
        match = re.search(rb"^-- cursor: ([^\r\n]+)$", result.stdout, re.M)
        require(match is not None, "a3-journal-cursor")
        self.cursor = match.group(1).decode("ascii")

    def verify_unit_stopped(self, metadata):
        # This is part of the supervisor's unit-stopped gate: failure also
        # prevents account/root removal, not merely a later scan failure.
        cgroup = Path("/sys/fs/cgroup/system.slice") / (base.UNIT + "/cgroup.procs")
        require(not cgroup.exists() or not cgroup.read_bytes().strip(), "a3-cgroup-writers")

    def after_unit(self, metadata, captured, complete):
        # Called only AFTER the exact owned unit AND cgroup are stopped.
        self.verify_unit_stopped(metadata)
        require(self.cursor is not None, "a3-journal-cursor")
        journal = base.run(["journalctl", "--no-pager", "--all", "--output=cat", "--unit=" + base.UNIT,
                            "--after-cursor=" + self.cursor], check=False)
        evidence = Report()
        ok = scan_all(Path(metadata["work"]), self.case, metadata["uid"], metadata["gid"],
                      (captured, journal.stdout), evidence)
        # Complete process pipe capture and an actual nonempty current-unit
        # journal are mandatory, not content-clear fallbacks.
        ok = ok and complete and journal.returncode == 0 and bool(journal.stdout.strip()) and b"A3_SCAN_OK\n" in captured
        print("FIXTURE_DIAGNOSTIC a3-scan-content-" + evidence.states["scan-content"], flush=True)
        print("FIXTURE_DIAGNOSTIC a3-scan-completeness-" + ("complete" if ok else "incomplete"), flush=True)
        require(ok, "a3-unit-evidence-incomplete")
        print("CHECK A3_UNIT_EVIDENCE_OK=true", flush=True)


class Children:
    def __init__(self, metadata, environment, identity, report):
        self.m, self.env, self.identity, self.report = metadata, environment, identity, report
        self.work = Path(metadata["work"])
        self.processes, self.handles, self.contents = {}, {}, {}

    def command(self, label, args, expected=b"A3_LIBSECRET_OK\n", *, mode=None, crash=None, ready=None):
        def action():
            self.identity()
            require(label in labels(self.m["a3_case"]) and label not in self.processes, "a3-child-registration")
            environment = dict(self.env)
            if mode:
                environment.update(CPA_A3_MODE=mode, CPA_A3_CASE=self.m["a3_case"], CPA_A3_CRASH=crash or "")
            path = self.work / ("child-" + label + ".log")
            handle = path.open("xb")
            self.handles[label] = handle
            process = subprocess.Popen(args, env=environment, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT)
            self.processes[label] = process
            content, ready_seen, killed, fully_captured = bytearray(), False, False, False
            selector = selectors.DefaultSelector()
            selector.register(process.stdout, selectors.EVENT_READ)
            deadline = time.monotonic() + 30
            try:
                while selector.get_map():
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise TimeoutError("a3-child-timeout")
                    events = selector.select(remaining)
                    if not events:
                        raise TimeoutError("a3-child-timeout")
                    block = os.read(process.stdout.fileno(), 65536)
                    if not block:
                        selector.unregister(process.stdout)
                        break
                    handle.write(block)
                    handle.flush()
                    content.extend(block)
                    require(len(content) <= 4 * 1024 * 1024, "a3-output-bound")
                    if ready and not ready_seen and b"A3_SNAPSHOT_READY\n" in content:
                        ready_seen = True
                        ready()
                        process.stdin.write(b"c")
                        process.stdin.flush()
                    if crash and not killed and ("A3_BOUNDARY " + crash + "\n").encode() in content:
                        base.owned_process(process.pid, self.m["cpa"], self.m["uid"])
                        process.kill()  # SIGKILL, exact owned child only.
                        killed = True
                status = process.wait(timeout=max(0.1, deadline - time.monotonic()))
                fully_captured = True  # EOF was drained and wait completed.
                if status != 0 and not crash:
                    tokens = set(re.findall(rb"[a-z][a-z-]*", content))
                    for fixed in RUST_FAILURES + volatile.PANICS:
                        if fixed.encode() in tokens:
                            print("FIXTURE_DIAGNOSTIC " + fixed, flush=True)
                    if args[0] == self.m["a3_verifier"]:
                        print("FIXTURE_DIAGNOSTIC a3-verifier-" + VERIFIER_FAILURES.get(status, "unknown") + "-failed", flush=True)
                if crash:
                    require(killed and ready_seen and status == -signal.SIGKILL, "a3-crash-not-observed")
                else:
                    require(status == 0 and expected in content, "a3-child-failed")
                require(not ready or ready_seen, "a3-ready-missing")
                self.contents[label] = bytes(content)
                return bytes(content)
            finally:
                selector.close()
                try:
                    if process.poll() is None:
                        a2.stop(process)
                except Exception:
                    self.report.set("children", "failed")
                    fully_captured = False
                for stream in (process.stdin, process.stdout):
                    try:
                        stream.close()
                    except Exception:
                        self.report.set("handles", "failed")
                        fully_captured = False
                try:
                    handle.flush()
                    os.fsync(handle.fileno())
                except Exception:
                    self.report.set("handles", "failed")
                    fully_captured = False
                try:
                    handle.close()
                except Exception:
                    self.report.set("handles", "failed")
                    fully_captured = False
                if fully_captured:
                    try:
                        mark_complete(self.work, path.name)
                    except Exception:
                        self.report.set("handles", "failed")
                        self.report.set("scan-completeness", "incomplete")
                        raise
        return self.report.run(label, action)

    def cpa(self, label, mode, crash=None, ready=None):
        return self.command(label, [self.m["cpa"], "--exact", TEST, "--ignored", "--nocapture", "--test-threads=1"],
                            b"A3_DRIVER_OK\n", mode=mode, crash=crash, ready=ready)

    def verify(self, label, state, role):
        return self.command(label, [self.m["a3_verifier"], state, role])


def sequence(m, environment, identity, report, children):
    case = m["a3_case"]
    children.command("setup", [m["verifier"], "setup"], b"LIBSECRET_FIXTURE_OK\n")
    children.command("policy", [m["a3_verifier"], "policy"])
    if case == "duplicate":
        path = Path(m["work"]) / "child-duplicate-sequence.log"
        with path.open("xb") as log:
            class Capture(list):
                def append(self, value):
                    super().append(value)
                    log.write(value)
                    log.flush()
            trace = a2.Report()
            try:
                report.run("duplicate-sequence", lambda: a2.cases(m, environment, Capture(), identity, trace))
            finally:
                os.fsync(log.fileno())
                trace.emit()
        mark_complete(Path(m["work"]), path.name)
        value = children.cpa("duplicate-delete", "duplicate-delete")
        require(b"A3_DELETE_SEND_ZERO\n" in value and b"A3_BACKEND_DELETE_SEND\n" not in value, "a3-duplicate-send")
        children.command("duplicate-unchanged", [m["a2_helper"], "verify-two"], b"A2_HELPER_OK\n")
        return
    children.cpa("neighbor-create", "neighbor-create")
    children.verify("snapshot-neighbor", "snapshot", "neighbor")
    if case == "exact":
        children.cpa("exact-create", "exact-create")
        children.verify("snapshot-target", "snapshot", "target")
        children.cpa("exact-negatives", "exact-negatives")
        children.verify("target-after-negatives", "present", "target")
        children.verify("neighbor-after-negatives", "present", "neighbor")
        deleted = children.cpa("exact-delete", "exact-delete")
        require(deleted.count(b"A3_BACKEND_DELETE_SEND\n") == 1 and b"A3_DELETE_SEND_ONE\n" in deleted, "a3-delete-count")
        children.verify("target-after-delete", "absent", "target")
        children.verify("neighbor-after-delete", "present", "neighbor")
        missing = children.cpa("exact-missing", "exact-missing")
        require(b"A3_DELETE_OUTCOME_MISSING\n" in missing and b"A3_BACKEND_DELETE_SEND\n" not in missing, "a3-missing-count")
        children.verify("target-after-missing", "absent", "target")
        children.verify("neighbor-after-missing", "present", "neighbor")
        return

    def ready():
        children.verify("snapshot-target", "snapshot", "target")
        children.verify("neighbor-at-ready", "present", "neighbor")

    negative = case.startswith("tamper-") or case.startswith("cross-")
    boundary = None if negative else case.split("-", 1)[1]
    start = children.cpa("start", "durable-start", boundary, ready)
    if negative:
        require(b"A3_BACKEND_DELETE_SEND\n" not in start and b"A3_DELETE_SEND_ZERO\n" in start, "a3-negative-send")
        children.verify("negative-target", "present", "target")
        children.verify("negative-neighbor", "present", "neighbor")
        return
    children.verify("target-after-crash", "present" if boundary in PRESENT_BOUNDARIES else "absent", "target")
    children.verify("neighbor-after-crash", "present", "neighbor")
    resumed = children.cpa("resume", "durable-resume")
    require(start.count(b"A3_BACKEND_DELETE_SEND\n") + resumed.count(b"A3_BACKEND_DELETE_SEND\n") == 1,
            "a3-total-delete-count")
    if boundary == "deleted-before-confirm":
        require(b"A3_DELETE_OUTCOME_MISSING\n" in resumed and b"A3_DELETE_SEND_ZERO\n" in resumed,
                "a3-unconfirmed-delete-replay")
    children.verify("target-after-resume", "absent", "target")
    children.verify("neighbor-after-resume", "present", "neighbor")
    again = children.cpa("complete-reentry", "durable-resume")
    require(b"A3_DELETE_SEND_ZERO\n" in again and b"A3_BACKEND_DELETE_SEND\n" not in again, "a3-complete-delete")
    children.verify("final-target", "absent", "target")
    children.verify("final-neighbor", "present", "neighbor")


def inside(metadata_path):
    report = Report()
    m = report.run("metadata", lambda: json.loads(Path(metadata_path).read_text()))
    work, case = Path(m["work"]), m["a3_case"]
    require(case in CASES and m["a3_samples"] == [list(v) for v in samples(case)] and m["a3_logs"] == list(log_names(case))
            and m["a3_completions"] == [name + ".complete" for name in log_names(case)],
            "a3-registration-mismatch")
    processes, handles = {}, {}
    children = None
    try:
        report.run("isolation", lambda: base.verify_isolation(m))
        environment = dict(os.environ)

        def start(name, args):
            handle = (work / (name + ".log")).open("xb")
            handles[name] = handle
            process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL, stdout=handle, stderr=subprocess.STDOUT)
            processes[name] = process
            return process

        bus = report.run("bus-start", lambda: start("bus", ["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog", "--config-file=" + m["bus_config"]]))

        def bus_ready():
            deadline = time.monotonic() + 10
            while not (work / "runtime/bus").exists():
                require(bus.poll() is None and time.monotonic() < deadline, "a3-bus-start")
                time.sleep(0.05)  # Startup polling only, never a crash window.
            base.owned_process(bus.pid, "/usr/bin/dbus-daemon", m["uid"])
        report.run("bus-ready", bus_ready)
        environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + str(work / "runtime/bus")
        environment["CPA_SS_FIXTURE_ROOT"] = str(work)
        service = report.run("service-start", lambda: start("keyring", ["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets", "--control-directory=" + str(work / "runtime/keyring")]))

        def identity():
            require(bus.poll() is None and service.poll() is None, "a3-daemon-exited")
            base.owned_process(bus.pid, "/usr/bin/dbus-daemon", m["uid"])
            base.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", m["uid"])

        # Observer startup output is captured into the pre-registered service
        # log, including failures; no unregistered output escapes the scanner.
        def service_ready():
            deadline = time.monotonic() + 15
            while True:
                identity()
                observed = base.run([m["observer"], str(bus.pid), str(service.pid)], env=environment, timeout=5, check=False)
                handles["keyring"].write(observed.stdout)
                handles["keyring"].flush()
                if observed.returncode == 0 and observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n":
                    break
                require(observed.returncode == 30 and time.monotonic() < deadline, "a3-observer")
                time.sleep(0.1)
        report.run("service-ready", service_ready)
        print("PRIVATE_BUS_SERVICE_UID_PID_OK", flush=True)
        children = Children(m, environment, identity, report)
        report.run("sequence", lambda: sequence(m, environment, identity, report, children))
        identity()
        print("A3_CASE_OK", flush=True)
    except Exception:
        if report.first is None:
            report.first = "runtime"
            report.set("operation-runtime", "failed")
    finally:
        stopped, closed = True, True
        if children:
            for process in children.processes.values():
                try:
                    a2.stop(process)
                    stopped = stopped and process.poll() is not None
                    for stream in (process.stdin, process.stdout):
                        if stream and not stream.closed:
                            stream.close()
                except Exception:
                    stopped = False
            for handle in children.handles.values():
                try:
                    handle.close()
                except Exception:
                    closed = False
        for process in reversed(tuple(processes.values())):
            try:
                a2.stop(process)
                stopped = stopped and process.poll() is not None
            except Exception:
                stopped = False
        for handle in handles.values():
            try:
                handle.flush()
                os.fsync(handle.fileno())
                handle.close()
            except Exception:
                closed = False
        report.set("children", "ok" if stopped else "failed")
        report.set("handles", "ok" if closed else "failed")
        if stopped and closed:
            for name in handles:
                if name in processes and processes[name].poll() is not None:
                    try:
                        mark_complete(work, name + ".log")
                    except Exception:
                        report.set("handles", "failed")
                        closed = False
        if stopped:
            print("OWNED_PROCESSES_STOPPED", flush=True)
        ok = scan_all(work, case, m["uid"], m["gid"], (), report)
        if ok and stopped and closed and report.states["children"] == "ok" and report.states["handles"] == "ok":
            print("A3_SCAN_OK", flush=True)
        report.emit()
    require(report.first is None and ok and stopped and closed and report.states["children"] == "ok"
            and report.states["handles"] == "ok", "a3-failed")


def outer(binary):
    # Sequential independent VMs' units on one disposable runner. Each crash
    # case retains the same bus/keyring across ALL its CPA child restarts.
    # Stop on the first red; only that case's safe cleanup is attempted.
    for case in CASES:
        fixture = Fixture(binary, case)
        try:
            base.outer(fixture)
        finally:
            fixture.report.emit(outer=True)
        print("CHECK A3_CASE_" + case.upper().replace("-", "_") + "=true", flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 2:
            outer(sys.argv[1])
        else:
            raise base.PreflightFailure("invalid-invocation")
    except Exception:
        print("FAIL: a3-failed", file=sys.stderr)
        sys.exit(1)
