#!/usr/bin/env python3
"""Independent A3 supervisor extension. Explicit disposable Linux invocation only."""
import importlib.util
import ctypes
import errno
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
JOURNAL_FAILURES = (
    "a3-journal-family-unsupported-platform", "a3-journal-family-invalid-root", "a3-journal-family-invalid-metadata",
    "a3-journal-family-corrupt", "a3-journal-family-incomplete-write", "a3-journal-family-missing",
    "a3-journal-family-already-exists", "a3-journal-family-too-many", "a3-journal-family-recovery-conflict",
    "a3-journal-family-cleanup", "a3-journal-family-workspace-removal", "a3-journal-family-workspace-retirement",
    "a3-journal-family-completion-tombstone", "a3-journal-family-io",
    "a3-journal-io-not-found", "a3-journal-io-permission-denied", "a3-journal-io-already-exists",
    "a3-journal-io-would-block", "a3-journal-io-not-a-directory", "a3-journal-io-is-a-directory",
    "a3-journal-io-directory-not-empty", "a3-journal-io-read-only-filesystem", "a3-journal-io-invalid-input",
    "a3-journal-io-invalid-data", "a3-journal-io-timed-out", "a3-journal-io-write-zero", "a3-journal-io-storage-full",
    "a3-journal-io-quota-exceeded", "a3-journal-io-file-too-large", "a3-journal-io-resource-busy", "a3-journal-io-deadlock",
    "a3-journal-io-crosses-devices", "a3-journal-io-too-many-links", "a3-journal-io-interrupted", "a3-journal-io-unsupported",
    "a3-journal-io-unexpected-eof", "a3-journal-io-out-of-memory", "a3-journal-io-other", "a3-journal-io-unlisted",
)
RUST_FAILURES += JOURNAL_FAILURES
JOURNAL_POINT_FAILURES = (
    "a3-journal-point-root-binding", "a3-journal-point-final-object-present",
    "a3-journal-point-published-file-rebound", "a3-journal-point-published-directory-rebound",
    "a3-journal-point-loaded-file-rebound", "a3-journal-point-namespace-snapshot",
    "a3-journal-point-loaded-directory-rebound", "a3-journal-point-private-directory-admission",
    "a3-journal-point-posix-acl-present", "a3-journal-point-private-file-admission",
)
RUST_FAILURES += JOURNAL_POINT_FAILURES
JOURNAL_ACL_FAILURES = (
    "a3-acl-role-root-directory", "a3-acl-role-cleanup-directory",
    "a3-acl-role-pending-file", "a3-acl-role-event-file",
    "a3-acl-attribute-access", "a3-acl-attribute-default",
    "a3-acl-result-zero", "a3-acl-result-nonzero",
)
RUST_FAILURES += JOURNAL_ACL_FAILURES
STORE_ACL_ATTRIBUTES = (b"system.posix_acl_access", b"system.posix_acl_default")
STORE_INIT_DIAGNOSTICS = ("a3-store-init-transaction-ok", "a3-store-init-other-transaction-ok", "a3-store-init-failed")
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

# New bounded evidence-reader batch, 2026-10-19. Independently authored from
# E/F's contract and Python 3.12 os.open / Linux open(2), not old-run inference:
# https://docs.python.org/3.12/library/os.html#os.open
# https://man7.org/linux/man-pages/man2/open.2.html
SAMPLE_ROLES = {"sent.key": "sample-original-key", "sent.envelope": "sample-original-envelope",
    "duplicate.sent.key": "sample-duplicate-key", "duplicate.sent.envelope": "sample-duplicate-envelope",
    **{role + "/" + name: "sample-" + role + "-" + kind for role in ("neighbor", "target")
       for name, kind in (("sent.key", "key"), ("sent.envelope", "envelope"), ("old.sample", "old"), ("new.sample", "new"))}}
LOG_ROLES = {"bus.log": "bus", "keyring.log": "keyring",
             **{"child-" + label + ".log": "child-" + label for label in LABELS}}
EVIDENCE_ROLES = tuple(SAMPLE_ROLES.values()) + tuple(prefix + role for prefix in ("log-", "completion-")
    for role in LOG_ROLES.values()) + ("pipe-unit", "journal-unit", "content-scan")
OPEN_RESULTS = ("ok", "missing", "denied", "nofollow", "not-directory", "failed")
IO_STAGES = ("parent-stat", "parent-close", "leaf-stat", "buffer-open", "read", "post-stat", "buffer-close", "leaf-close")
CHECK_STAGES = ("parent-kind", "parent-owner", "parent-group", "parent-mode", "leaf-kind", "leaf-owner",
    "leaf-group", "leaf-links", "leaf-mode", "leaf-size", "leaf-length", "read-length", "post-binding",
    "marker-value", "inner-marker", "journal-nonempty")
EVIDENCE_PAIRS = tuple((stage, result) for stage in ("parent-open", "leaf-open") for result in OPEN_RESULTS) + tuple(
    (stage, result) for stage in IO_STAGES for result in ("ok", "failed")) + tuple(
    (stage, result) for stage in CHECK_STAGES for result in ("ok", "rejected")) + (
    ("reader", "ok"), ("reader", "missing"), ("reader", "denied"), ("reader", "failed"),
    ("integrity", "complete"), ("integrity", "incomplete"), ("pipe-capture", "ok"), ("pipe-capture", "incomplete"),
    ("journal-cursor", "ok"), ("journal-cursor", "missing"), ("journal-command", "ok"), ("journal-command", "failed"),
    ("journal-capture", "ok"), ("journal-capture", "incomplete"),
    ("content", "clear"), ("content", "matched"), ("content", "failed"))
INTEGRITY_CATEGORIES = ("samples", "logs", "completions", "pipe", "journal", "inner")
EVIDENCE_SCOPES = ("inner", "outer")
EVIDENCE_LABELS = tuple("a3-evidence-" + scope + "-" + role + "-" + stage + "-" + result
    for scope in EVIDENCE_SCOPES for role in EVIDENCE_ROLES for stage, result in EVIDENCE_PAIRS)
EVIDENCE_FIRST_LABELS = tuple("a3-first-evidence-" + scope + "-" + role + "-" + stage + "-" + result
    for scope in EVIDENCE_SCOPES for role in EVIDENCE_ROLES for stage, result in EVIDENCE_PAIRS
    if result not in ("ok", "complete", "clear")) + tuple("a3-first-evidence-" + scope + "-none" for scope in EVIDENCE_SCOPES)
DIAGNOSTICS += EVIDENCE_LABELS + EVIDENCE_FIRST_LABELS + tuple(
    "a3-evidence-summary-" + scope + "-" + category + "-" + result for scope in EVIDENCE_SCOPES
    for category in INTEGRITY_CATEGORIES for result in ("not-reached", "complete", "incomplete"))

# Independent bounded parent crash-result batch, 2026-10-19. Classify only
# existing in-memory observations; never output raw status, text or identity.
# https://docs.python.org/3.12/library/subprocess.html#subprocess.Popen.returncode
CRASH_SCHEMA = {
    "ready": ("seen", "not-seen"), "boundary": ("seen", "not-reached"),
    "kill": ("returned", "not-completed"), "wait": ("returned", "not-completed"),
    "capture": ("eof-wait-complete", "incomplete"),
    "exit": ("not-observed", "clean", "nonzero", "sigkill", "other-signal"),
    "result": ("expected-sigkill", "boundary-not-reached", "early-exit", "kill-not-completed",
               "ready-not-seen", "other-exit", "wait-failed", "capture-failed"),
    "rust-labels": ("not-screened", "present", "absent"),
}
CRASH_DIAGNOSTICS = tuple("a3-crash-" + field + "-" + value for field, values in CRASH_SCHEMA.items() for value in values)
DIAGNOSTICS += CRASH_DIAGNOSTICS


def crash_result(ready_seen, boundary_seen, killed, status, fully_captured, failed_phase=None):
    require(failed_phase in (None, "capture", "kill", "wait"), "a3-category-invalid")
    exit_class = ("not-observed" if status is None else "sigkill" if status == -signal.SIGKILL else
                  "clean" if status == 0 else "other-signal" if status < 0 else "nonzero")
    if failed_phase == "wait":
        result = "wait-failed"
    elif failed_phase == "kill":
        result = "kill-not-completed"
    elif failed_phase == "capture":
        result = "capture-failed"
    elif killed and ready_seen and status == -signal.SIGKILL:
        result = "expected-sigkill"  # Same proof predicate as the unchanged gate.
    elif not boundary_seen:
        result = "boundary-not-reached" if status is None else "early-exit"
    elif not killed:
        result = "kill-not-completed"
    elif not ready_seen:
        result = "ready-not-seen"
    else:
        result = "other-exit"
    return {"ready": "seen" if ready_seen else "not-seen", "boundary": "seen" if boundary_seen else "not-reached",
            "kill": "returned" if killed else "not-completed", "wait": "returned" if status is not None else "not-completed",
            "capture": "eof-wait-complete" if fully_captured else "incomplete", "exit": exit_class, "result": result}


def fixed_child_failures(content):
    # Reuse only captured bytes and the pre-existing source whitelist.
    # Keep the digit inside existing a3-* labels; never widen the whitelist.
    tokens = set(re.findall(rb"[a-z][a-z0-9-]*", content))
    return tuple(fixed for fixed in RUST_FAILURES + volatile.PANICS if fixed.encode() in tokens)


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


# Independent writer-only batch, 2026-10-19: Python 3.12 os.open/write/fstat
# and Linux open(2), not an inference about either historical red's mode value.
# https://docs.python.org/3.12/library/os.html#os.open
# https://man7.org/linux/man-pages/man2/open.2.html
# Standard library only; no new dependency or third-party implementation.
class PrivateLogWriter:
    """Exclusive private, unbuffered log; never repair rejected metadata.

    The caller still owns EOF/wait, flush/fsync, close, then completion signing.
    Only existing source-owned failure labels leave this boundary.
    """
    def __init__(self, path, uid, gid):
        self._fd, self._binding, self._started, self._failed = None, None, False, False
        self._uid, self._gid = uid, gid
        try:
            self._fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        except Exception:
            self._abort("a3-private-create")
        self._verify()

    @property
    def closed(self):
        return self._fd is None

    def _abort(self, label):
        self._failed = True
        descriptor, self._fd = self._fd, None
        if descriptor is not None:
            try:
                os.close(descriptor)  # Never retry an fd after a close error.
            except Exception:
                pass
        raise base.PreflightFailure(label) from None

    def _verify(self):
        if self.closed or self._failed:
            self._abort("a3-private-write")
        try:
            info = os.fstat(self._fd)
        except Exception:
            self._abort("a3-private-metadata")
        if not (stat.S_ISREG(info.st_mode) and info.st_uid == self._uid and info.st_gid == self._gid
                and stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1
                and (self._started or info.st_size == 0)):
            self._abort("a3-private-metadata")
        binding = (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid, info.st_nlink)
        if self._binding is not None and binding != self._binding:
            self._abort("a3-private-metadata")
        self._binding = binding

    def write(self, value):
        self._verify()
        try:
            count = os.write(self._fd, value)
        except Exception:
            self._abort("a3-private-write")
        if count != len(value):
            self._abort("a3-private-write")  # No retry or completion on a short write.
        self._started = True
        self._verify()
        return count

    def flush(self):
        # os.write has no Python buffer; the existing caller fsync is retained.
        self._verify()

    def fileno(self):
        self._verify()  # Also before Popen receives this exact fd.
        self._started = True
        return self._fd

    def close(self):
        if self.closed:
            if self._failed:
                raise base.PreflightFailure("a3-private-write") from None
            return
        self._verify()
        descriptor, self._fd = self._fd, None
        try:
            os.close(descriptor)
        except Exception:
            self._failed = True
            raise base.PreflightFailure("a3-private-write") from None

    def __enter__(self):
        self._verify()
        return self

    def __exit__(self, kind, value, traceback):
        self.close()


def private_log(path, uid, gid):
    return PrivateLogWriter(path, uid, gid)


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
    def __init__(self, evidence_scope="inner"):
        require(evidence_scope in EVIDENCE_SCOPES, "a3-category-invalid")
        self.states = {key: "not-reached" for key in FIELDS}
        self.first = None
        self.evidence_scope = evidence_scope
        self.evidence_events = {}
        self.first_evidence = None
        self.integrity = {category: "not-reached" for category in INTEGRITY_CATEGORIES}

    def evidence(self, role, stage, result):
        require(role in EVIDENCE_ROLES and (stage, result) in EVIDENCE_PAIRS, "a3-category-invalid")
        key = (role, stage)
        if self.evidence_events.get(key, "ok") in ("ok", "complete", "clear"):
            self.evidence_events[key] = result
        if result not in ("ok", "complete", "clear") and self.first_evidence is None:
            self.first_evidence = (role, stage, result)

    def category(self, category, complete):
        require(category in INTEGRITY_CATEGORIES, "a3-category-invalid")
        if self.integrity[category] != "incomplete":
            self.integrity[category] = "complete" if complete else "incomplete"

    def emit_evidence(self):
        for (role, stage), result in self.evidence_events.items():
            print("FIXTURE_DIAGNOSTIC a3-evidence-" + self.evidence_scope + "-" + role + "-" + stage + "-" + result, flush=True)
        for category, result in self.integrity.items():
            print("FIXTURE_DIAGNOSTIC a3-evidence-summary-" + self.evidence_scope + "-" + category + "-" + result, flush=True)
        suffix = "-".join(self.first_evidence) if self.first_evidence else "none"
        print("FIXTURE_DIAGNOSTIC a3-first-evidence-" + self.evidence_scope + "-" + suffix, flush=True)

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
            self.emit_evidence()


class EvidenceReadFailure(base.PreflightFailure):
    def __init__(self, stage, result):
        self.stage, self.result = stage, result
        super().__init__("a3-evidence-read-failed")


def private_read(path, uid, gid, length=None, *, trace=None):
    """One open/read per registered artifact; fixed diagnostics reuse results.

    O_NONBLOCK fixes the independent FIFO-before-fstat static defect. It is not
    evidence about the historical red and is not a regular-file I/O deadline.
    """
    def event(stage, result):
        if trace is not None:
            trace(stage, result)

    def failure(stage, result):
        event(stage, result)
        raise EvidenceReadFailure(stage, result) from None

    def call(stage, action):
        try:
            value = action()
        except Exception as error:
            result = "failed"
            if stage in ("parent-open", "leaf-open"):
                if isinstance(error, FileNotFoundError):
                    result = "missing"
                elif isinstance(error, PermissionError):
                    result = "denied"
                elif isinstance(error, OSError) and error.errno == errno.ELOOP:
                    result = "nofollow"
                elif isinstance(error, OSError) and error.errno == errno.ENOTDIR:
                    result = "not-directory"
            failure(stage, result)
        event(stage, "ok")
        return value

    def check(stage, predicate):
        if not predicate:
            failure(stage, "rejected")
        event(stage, "ok")

    parent = descriptor = stream = None
    primary, value = None, None
    try:
        parent = call("parent-open", lambda: os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC))
        info = call("parent-stat", lambda: os.fstat(parent))
        check("parent-kind", stat.S_ISDIR(info.st_mode))
        check("parent-owner", info.st_uid == uid)
        check("parent-group", info.st_gid == gid)
        check("parent-mode", stat.S_IMODE(info.st_mode) == 0o700)
        descriptor = call("leaf-open", lambda: os.open(path.name, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent))
        owned_parent, parent = parent, None  # A close error never retries a possibly reused fd.
        call("parent-close", lambda: os.close(owned_parent))
        a = call("leaf-stat", lambda: os.fstat(descriptor))
        check("leaf-kind", stat.S_ISREG(a.st_mode))  # Reject FIFO before wrap/read.
        check("leaf-owner", a.st_uid == uid)
        check("leaf-group", a.st_gid == gid)
        check("leaf-links", a.st_nlink == 1)
        check("leaf-mode", stat.S_IMODE(a.st_mode) == 0o600)
        check("leaf-size", 0 <= a.st_size <= 4 * 1024 * 1024)
        check("leaf-length", length is None or a.st_size == length)
        # Explicit descriptor ownership makes failed wrap/close paths close
        # each fd once, without another open, stat, secret read or close retry.
        stream = call("buffer-open", lambda: os.fdopen(descriptor, "rb", closefd=False))
        value = call("read", lambda: stream.read(a.st_size + 1))
        b = call("post-stat", lambda: os.fstat(descriptor))
        check("read-length", len(value) == a.st_size)
        check("post-binding", (a.st_dev, a.st_ino, a.st_mode, a.st_uid, a.st_gid, a.st_size, a.st_nlink)
              == (b.st_dev, b.st_ino, b.st_mode, b.st_uid, b.st_gid, b.st_size, b.st_nlink))
    except EvidenceReadFailure as error:
        primary = error
    finally:
        for stage, owned, close in (("buffer-close", stream, lambda: stream.close()),
                                    ("leaf-close", descriptor, lambda: os.close(descriptor)),
                                    ("parent-close", parent, lambda: os.close(parent))):
            if owned is not None:
                try:
                    call(stage, close)
                except EvidenceReadFailure as error:
                    if primary is None:
                        primary = error
    if primary is not None:
        raise primary from None
    event("reader", "ok")
    return value


def scan_all(work, case, uid, gid, extra, report):
    secrets, outputs = [], list(extra)
    samples_ok = logs_ok = completions_ok = True

    def read(role, path, length=None):
        try:
            value = private_read(path, uid, gid, length, trace=lambda stage, result: report.evidence(role, stage, result))
        except EvidenceReadFailure as error:
            report.evidence(role, error.stage, error.result)
        except FileNotFoundError:
            report.evidence(role, "reader", "missing")
        except PermissionError:
            report.evidence(role, "reader", "denied")
        except Exception:
            report.evidence(role, "reader", "failed")
        else:
            return value, True
        report.evidence(role, "integrity", "incomplete")
        return None, False

    for name, length in samples(case):
        role = SAMPLE_ROLES[name]
        value, ok = read(role, work / name, length)
        samples_ok = samples_ok and ok
        if ok:
            secrets.append(value)
            report.evidence(role, "integrity", "complete")
    for name in log_names(case):
        role = "log-" + LOG_ROLES[name]
        value, ok = read(role, work / name)
        logs_ok = logs_ok and ok
        if ok:
            outputs.append(value)
            report.evidence(role, "integrity", "complete")
        role = "completion-" + LOG_ROLES[name]
        value, ok = read(role, work / (name + ".complete"), 9)
        if ok:
            ok = value == b"complete\n"
            report.evidence(role, "marker-value", "ok" if ok else "rejected")
            report.evidence(role, "integrity", "complete" if ok else "incomplete")
        completions_ok = completions_ok and ok
    report.category("samples", samples_ok)
    report.category("logs", logs_ok)
    report.category("completions", completions_ok)
    try:
        volatile.reject_secret_output(secrets, outputs)
        report.set("scan-content", "clear")
        report.evidence("content-scan", "content", "clear")
    except base.PreflightFailure:
        report.set("scan-content", "matched")
        report.evidence("content-scan", "content", "matched")
    except Exception:
        report.set("scan-content", "failed")
        report.evidence("content-scan", "content", "failed")
    complete = samples_ok and logs_ok and completions_ok
    report.set("scan-completeness", "complete" if complete else "incomplete")
    return complete and report.states["scan-completeness"] == "complete" and report.states["scan-content"] == "clear"


def require_a3_store_acl_absent(descriptor):
    """Zero-buffer fd queries only: never read or report an ACL value/length."""
    library = ctypes.CDLL(None, use_errno=True)
    query = library.fgetxattr
    query.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_void_p, ctypes.c_size_t]
    query.restype = ctypes.c_ssize_t
    for attribute in STORE_ACL_ATTRIBUTES:
        result = query(descriptor, attribute, None, 0)
        require(result == -1 and ctypes.get_errno() == errno.ENODATA, "a3-store-init-failed")


def initialize_a3_store(work, name, uid, gid):
    """Only an exclusively created, still-empty A3 store may be initialized.

    This runs before the unit/targets/samples/secrets/journal exist. No existing
    object is adopted, no ancestor ACL changes, and no failure is retried here.
    The existing outer owned-tree cleanup is responsible for any failed setup.
    Independently authored; public fd/xattr sources are in PROVENANCE.md.
    """
    parent = descriptor = None
    failed = False
    try:
        require(sys.platform == "linux" and name in ("transaction", "other-transaction"), "a3-store-init-failed")
        flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC
        parent = os.open(work, flags)
        parent_info = os.fstat(parent)
        require(stat.S_ISDIR(parent_info.st_mode) and stat.S_IMODE(parent_info.st_mode) == 0o700
                and (parent_info.st_uid, parent_info.st_gid) == (uid, gid), "a3-store-init-failed")

        def parent_binding(info):
            return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid)

        expected_parent = parent_binding(parent_info)
        require(parent_binding(os.stat(work, follow_symlinks=False)) == expected_parent, "a3-store-init-failed")
        # mkdir has no exist_ok: a reused name, even empty/private, is rejected.
        os.mkdir(name, mode=0o700, dir_fd=parent)
        created = os.stat(name, dir_fd=parent, follow_symlinks=False)
        require(stat.S_ISDIR(created.st_mode) and stat.S_IMODE(created.st_mode) == 0o700
                and (created.st_uid, created.st_gid) == (os.geteuid(), os.getegid())
                and created.st_nlink == 2, "a3-store-init-failed")
        identity = (created.st_dev, created.st_ino)
        descriptor = os.open(name, flags, dir_fd=parent)

        def verify(expected_uid, expected_gid):
            held = os.fstat(descriptor)
            named = os.stat(name, dir_fd=parent, follow_symlinks=False)
            require(stat.S_ISDIR(held.st_mode) and stat.S_IMODE(held.st_mode) == 0o700
                    and held.st_nlink == 2 and (held.st_uid, held.st_gid) == (expected_uid, expected_gid)
                    and (held.st_dev, held.st_ino) == identity
                    and (named.st_dev, named.st_ino, named.st_mode, named.st_uid, named.st_gid, named.st_nlink)
                    == (held.st_dev, held.st_ino, held.st_mode, held.st_uid, held.st_gid, held.st_nlink), "a3-store-init-failed")
            require(parent_binding(os.fstat(parent)) == expected_parent
                    and parent_binding(os.stat(work, follow_symlinks=False)) == expected_parent, "a3-store-init-failed")
            require(not os.listdir(descriptor), "a3-store-init-failed")

        verify(os.geteuid(), os.getegid())
        os.fchown(descriptor, uid, gid)  # Ownership handoff of this new object only.
        verify(uid, gid)
        for attribute in STORE_ACL_ATTRIBUTES:
            try:
                os.removexattr(descriptor, attribute)
            except OSError as error:
                require(error.errno == errno.ENODATA, "a3-store-init-failed")
        verify(uid, gid)
        require_a3_store_acl_absent(descriptor)
        verify(uid, gid)  # Rebind and recheck emptiness after the absence queries.
    except Exception:
        failed = True
    finally:
        for owned in (descriptor, parent):
            if owned is not None:
                try:
                    os.close(owned)  # Close each once; never retry a possibly reused fd.
                except Exception:
                    failed = True
    if failed:
        print("FIXTURE_DIAGNOSTIC a3-store-init-failed", flush=True)
        raise base.PreflightFailure("a3-store-init-failed") from None
    print("FIXTURE_DIAGNOSTIC " + STORE_INIT_DIAGNOSTICS[0 if name == "transaction" else 1], flush=True)


class Fixture(a2.Fixture):
    markers = MARKERS
    failures = a2.FAILURES + ("a3-failed",)
    diagnostics = DIAGNOSTICS + RUST_FAILURES + VERIFIER_LABELS + a2.DIAGNOSTICS + volatile.PANICS + STORE_INIT_DIAGNOSTICS
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
        for name in ("transaction", "other-transaction"):
            initialize_a3_store(work, name, metadata["uid"], metadata["gid"])
        for name in ("target", "neighbor"):
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
        evidence = Report("outer")
        journal_bytes, journal_ok = b"", False
        evidence.evidence("journal-unit", "journal-cursor", "ok" if self.cursor is not None else "missing")
        if self.cursor is not None:
            try:
                journal = base.run(["journalctl", "--no-pager", "--all", "--output=cat", "--unit=" + base.UNIT,
                                    "--after-cursor=" + self.cursor], check=False)
            except subprocess.TimeoutExpired as error:
                journal_bytes = error.stdout or b""
                evidence.evidence("journal-unit", "journal-capture", "incomplete")
            except Exception:
                evidence.evidence("journal-unit", "journal-command", "failed")
            else:
                journal_bytes = journal.stdout
                evidence.evidence("journal-unit", "journal-capture", "ok")
                evidence.evidence("journal-unit", "journal-command", "ok" if journal.returncode == 0 else "failed")
                evidence.evidence("journal-unit", "journal-nonempty", "ok" if bool(journal_bytes.strip()) else "rejected")
                journal_ok = journal.returncode == 0 and bool(journal_bytes.strip())
        ok = scan_all(Path(metadata["work"]), self.case, metadata["uid"], metadata["gid"],
                      (captured, journal_bytes), evidence)
        # Complete process pipe capture and an actual nonempty current-unit
        # journal are mandatory, not content-clear fallbacks.
        evidence.evidence("pipe-unit", "pipe-capture", "ok" if complete else "incomplete")
        inner_ok = b"A3_SCAN_OK\n" in captured
        evidence.evidence("pipe-unit", "inner-marker", "ok" if inner_ok else "rejected")
        evidence.category("pipe", complete)
        evidence.category("journal", journal_ok)
        evidence.category("inner", inner_ok)
        ok = ok and complete and journal_ok and inner_ok
        evidence.emit_evidence()
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
            handle = private_log(path, self.m["uid"], self.m["gid"])
            self.handles[label] = handle
            process = subprocess.Popen(args, env=environment, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                       stderr=subprocess.STDOUT)
            self.processes[label] = process
            content, ready_seen, killed, fully_captured = bytearray(), False, False, False
            boundary_seen, status, phase, crash_emitted = False, None, "capture", False

            def emit_crash(failed_phase=None):
                nonlocal crash_emitted
                if not crash or crash_emitted:
                    return
                crash_emitted = True  # Cleanup or the following gate never overwrites this first result.
                values = crash_result(ready_seen, boundary_seen, killed, status, fully_captured, failed_phase)
                unexpected = values["result"] != "expected-sigkill"
                fixed = fixed_child_failures(content) if unexpected else ()
                values["rust-labels"] = ("present" if fixed else "absent") if unexpected else "not-screened"
                for field, value in values.items():
                    print("FIXTURE_DIAGNOSTIC a3-crash-" + field + "-" + value, flush=True)
                for value in fixed:
                    print("FIXTURE_DIAGNOSTIC " + value, flush=True)

            try:
                selector = selectors.DefaultSelector()
                selector.register(process.stdout, selectors.EVENT_READ)
                deadline = time.monotonic() + 30
            except Exception:
                emit_crash("capture")
                raise
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
                        boundary_seen, phase = True, "kill"
                        base.owned_process(process.pid, self.m["cpa"], self.m["uid"])
                        process.kill()  # SIGKILL, exact owned child only.
                        killed = True
                        phase = "capture"
                phase = "wait"
                status = process.wait(timeout=max(0.1, deadline - time.monotonic()))
                fully_captured = True  # EOF was drained and wait completed.
                phase = "result"
                if status != 0 and not crash:
                    for fixed in fixed_child_failures(content):
                        print("FIXTURE_DIAGNOSTIC " + fixed, flush=True)
                    if args[0] == self.m["a3_verifier"]:
                        print("FIXTURE_DIAGNOSTIC a3-verifier-" + VERIFIER_FAILURES.get(status, "unknown") + "-failed", flush=True)
                if crash:
                    emit_crash()
                    require(killed and ready_seen and status == -signal.SIGKILL, "a3-crash-not-observed")
                else:
                    require(status == 0 and expected in content, "a3-child-failed")
                require(not ready or ready_seen, "a3-ready-missing")
                self.contents[label] = bytes(content)
                return bytes(content)
            except Exception:
                emit_crash(phase if phase in ("capture", "kill", "wait") else None)
                raise
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
        with private_log(path, m["uid"], m["gid"]) as log:
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
            handle = private_log(work / (name + ".log"), m["uid"], m["gid"])
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
