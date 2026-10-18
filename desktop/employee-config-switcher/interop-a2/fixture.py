#!/usr/bin/env python3
"""Independent duplicate-only slice, reusing unchanged PR81 isolation. See CONTRACT.md."""
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
volatile_path = HERE / "volatile.py" if (HERE / "volatile.py").is_file() else HERE.parent / "interop" / "fixture.py"
spec = importlib.util.spec_from_file_location("a2_volatile", volatile_path)
volatile = importlib.util.module_from_spec(spec)
spec.loader.exec_module(volatile)
base, require = volatile.base, volatile.require
MARKERS = ("ISOLATION_OK", "PRIVATE_BUS_SERVICE_UID_PID_OK", "A2_POSITIVE_CREATE_OK",
           "A2_POSITIVE_REOPEN_OK", "A2_TWO_REAL_ITEMS_OK", "A2_OPEN_CORRUPT_UNCHANGED_OK",
           "A2_CREATE_UNAVAILABLE_UNCHANGED_OK", "OWNED_PROCESSES_STOPPED", "A2_SECRET_LOG_SCAN_OK")
SAMPLES = (("sent.key", 32), ("sent.envelope", 148),
           ("duplicate.sent.key", 32), ("duplicate.sent.envelope", 148))
FAILURES = volatile.FAILURES + ("a2-inject-failed", "a2-two-items-failed", "a2-open-failed",
                               "a2-open-unchanged-failed", "a2-create-failed", "a2-create-unchanged-failed",
                               "a2-cleanup-failed", "a2-scan-failed", "a2-evidence-failed")
OPERATIONS = ("metadata", "isolation", "bus-start", "bus-ready", "service-start", "service-ready", "setup",
              "create", "create-verify", "reopen", "reopen-verify", "inject", "two-items", "open",
              "open-verify", "duplicate-create", "create-verify-after", "runtime")
HELPER_STAGES = ("guard", "directory", "reference", "original-envelope", "original-key", "original-path",
                 "original-valid", "session", "random", "second-valid", "capture-key", "capture-envelope",
                 "encode", "owner", "create-call", "create-reply", "capture-path", "pair-envelope",
                 "pair-key", "pair-path", "pair-valid", "search", "cardinality", "pair-items")
HELPER_PHASES = ("inject", "two-items", "open-verify", "create-verify-after")
READ_CATEGORIES = ("unobserved", "not-reached", "ok", "open-failed", "metadata-failed",
                   "length-read-failed", "post-read-changed", "close-failed")
PATH_CATEGORIES = ("unobserved", "not-reached", "ok", "metadata-failed", "read-failed", "format-failed")
SAMPLE_ROLES = ("original-key", "original-envelope", "duplicate-key", "duplicate-envelope")
OP_STATUSES = ("not-reached", "ok", "failed", "timeout", "budget", "child-failed", "marker-missing")
SCHEMA = {**{"operation-" + phase: OP_STATUSES for phase in OPERATIONS},
          **{"helper-" + phase + "-" + stage: ("unobserved", "not-reached", "ok", "failed")
             for phase in HELPER_PHASES for stage in HELPER_STAGES},
          **{"helper-" + phase + "-original-read": READ_CATEGORIES for phase in HELPER_PHASES},
          **{"helper-" + phase + "-original-path-read": PATH_CATEGORIES for phase in HELPER_PHASES},
          **{"sample-" + role: ("not-reached", "valid", "missing", "invalid", "read-failed") for role in SAMPLE_ROLES},
          **{"log-" + role: ("not-created", "collected", "missing", "read-failed") for role in ("bus", "keyring")},
          **{"child-" + role: ("not-created", "stopped", "failed", "timeout") for role in ("bus", "keyring")},
          **{"handle-" + role: ("not-created", "closed", "failed") for role in ("bus", "keyring")},
          **{"outer-" + role: ("not-reached", "ok", "failed", "timeout")
             for role in ("operation", "unit", "sockets", "user", "shm", "work-root", "host-root", "cleanup")},
          "scan-content": ("not-reached", "clear", "matched", "failed"),
          "scan-completeness": ("not-reached", "complete", "incomplete"),
          "cleanup": ("not-reached", "ok", "failed")}
DIAGNOSTICS = tuple("a2-" + key + "-" + status for key, statuses in SCHEMA.items() for status in statuses) + tuple(
    "a2-first-operation-" + phase + "-" + status for phase in OPERATIONS for status in OP_STATUSES[2:]) + ("a2-first-operation-none",)


class CaseFailure(Exception):
    def __init__(self, category):
        self.category = category if category in OP_STATUSES[2:] else "failed"


class Report:
    def __init__(self):
        self.states = {key: statuses[0] for key, statuses in SCHEMA.items()}
        self.attempted = set()
        self.first = None

    def set(self, key, status):
        if key not in SCHEMA or status not in SCHEMA[key]:
            raise ValueError("a2-invalid-diagnostic-category")
        self.states[key] = status

    def failure(self, phase, error):
        status = ("timeout" if isinstance(error, subprocess.TimeoutExpired) else
                  error.category if isinstance(error, CaseFailure) else "failed")
        self.set("operation-" + phase, status)
        if self.first is None:
            self.first = (phase, status)

    def run(self, phase, action):
        self.attempted.add(phase)
        try:
            result = action()
        except Exception as error:
            self.failure(phase, error)
            raise
        self.set("operation-" + phase, "ok")
        return result

    def helper(self, phase, output):
        if phase in HELPER_PHASES:
            lines = set(output.splitlines())
            for stage in HELPER_STAGES:
                for status in ("not-reached", "ok", "failed"):
                    if ("A2_HELPER_STAGE " + stage + " " + status).encode() in lines:
                        self.set("helper-" + phase + "-" + stage, status)
            for category in READ_CATEGORIES[1:]:
                if ("A2_ORIGINAL_ENVELOPE_READ " + category).encode() in lines:
                    self.set("helper-" + phase + "-original-read", category)
            for category in PATH_CATEGORIES[1:]:
                if ("A2_ORIGINAL_PATH_READ " + category).encode() in lines:
                    self.set("helper-" + phase + "-original-path-read", category)

    def emit(self, outer=False):
        for key, status in self.states.items():
            if key.startswith("outer-") == outer:
                print("FIXTURE_DIAGNOSTIC a2-" + key + "-" + status, flush=True)
        if not outer:
            suffix = "-".join(self.first) if self.first else "none"
            print("FIXTURE_DIAGNOSTIC a2-first-operation-" + suffix, flush=True)


def scan(work, outputs, report=None):
    # Inspect every available pre-send sample, even if another sample is invalid.
    # Never derive a scanner sample from an accepted/rejected service response.
    report = report or Report()
    secrets, complete = [], True
    for (name, length), role in zip(SAMPLES, SAMPLE_ROLES):
        try:
            value = volatile.read_sample(work / name, length)
            if value is None:
                complete = False
                reached = "create" in report.attempted if role.startswith("original-") else (
                    "inject" in report.attempted and report.states["helper-inject-capture-" +
                    ("key" if role == "duplicate-key" else "envelope")] != "not-reached")
                report.set("sample-" + role, "missing" if reached else "not-reached")
            else:
                secrets.append(value)
                report.set("sample-" + role, "valid")
        except base.PreflightFailure:
            report.set("sample-" + role, "invalid")
            complete = False
        except Exception:
            report.set("sample-" + role, "read-failed")
            complete = False
    try:
        volatile.reject_secret_output(secrets, outputs)
        report.set("scan-content", "clear")
    except base.PreflightFailure:
        report.set("scan-content", "matched")
    except Exception:
        report.set("scan-content", "failed")
    return complete


class Fixture(volatile.Fixture):
    markers = MARKERS
    failures = FAILURES
    diagnostics = volatile.PANICS + DIAGNOSTICS
    diagnostics_always = True

    def __init__(self, binary, report=None):
        super().__init__(binary)
        self.report = report or Report()

    def outer_diagnostic(self, stage, status):
        key = "outer-" + stage
        # Repeated resources in one category must not erase an earlier failure.
        if self.report.states[key] not in ("failed", "timeout"):
            self.report.set(key, status)

    def prepare(self, root, metadata):
        super().prepare(root, metadata)  # Exact unchanged binary, bus policy and isolation.
        # These root-owned scripts are prepared before the synthetic UID runs.
        shutil.copyfile(volatile_path, root / "volatile.py")
        (root / "volatile.py").chmod(0o444)
        (root / "interop.py").chmod(0o600)
        shutil.copyfile(HERE / "fixture.py", root / "interop.py")
        (root / "interop.py").chmod(0o444)
        helper = root / "a2-helper"
        flags = base.run(["pkg-config", "--cflags", "--libs", "libsecret-1"]).stdout.decode().split()
        base.run(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", str(HERE / "duplicate.c"), "-o", str(helper), *flags])
        helper.chmod(0o555)
        metadata["a2_helper"] = str(helper)


def bounded_call(args, environment, expected, label, outputs, report=None, phase=None):
    try:
        result = base.run(args, env=environment, timeout=12, check=False)
    except subprocess.TimeoutExpired as failure:
        outputs.append(failure.stdout or b"")
        if report:
            report.helper(phase, failure.stdout or b"")
        raise CaseFailure("timeout") from None
    outputs.append(result.stdout)
    if report:
        report.helper(phase, result.stdout)
        if result.returncode == 0 and phase in HELPER_PHASES and (any(
                report.states["helper-" + phase + "-" + stage] == "unobserved" for stage in HELPER_STAGES) or
                report.states["helper-" + phase + "-original-read"] == "unobserved" or
                report.states["helper-" + phase + "-original-path-read"] == "unobserved"):
            raise CaseFailure("marker-missing")
    if result.returncode != 0 or expected not in result.stdout:
        for panic in volatile.diagnostic_labels(result.stdout):
            print("FIXTURE_DIAGNOSTIC " + panic, flush=True)
        raise CaseFailure("child-failed" if result.returncode != 0 else "marker-missing")


def cases(metadata, environment, outputs, identity, report=None):
    """One injection, independent two-item proof before either rejection."""
    deadline = time.monotonic() + 30
    report = report or Report()

    def call(args, expected, label, phase):
        # Reserve room for the full bounded child call and cleanup before the
        # unchanged supervisor's 90-second limit. Slow fixtures fail, not retry.
        def action():
            if time.monotonic() + 12 > deadline:
                raise CaseFailure("budget")
            identity()
            bounded_call(args, environment, expected, label, outputs, report, phase)
        report.run(phase, action)

    def cpa(mode, expected, label, phase):
        environment["CPA_SS_FIXTURE_MODE"] = mode
        call([metadata["cpa"], "--exact", volatile.TEST, "--ignored", "--nocapture", "--test-threads=1"], expected, label, phase)

    call([metadata["verifier"], "setup"], b"LIBSECRET_FIXTURE_OK\n", "fixture-case-failed", "setup")
    for mode, marker in (("create", "A2_POSITIVE_CREATE_OK"), ("reopen", "A2_POSITIVE_REOPEN_OK")):
        cpa(mode, b"CPA_FIXTURE_OK\n", "fixture-case-failed", mode)
        call([metadata["verifier"], "verify"], b"LIBSECRET_FIXTURE_OK\n", "fixture-case-failed", mode + "-verify")
        print(marker, flush=True)
    call([metadata["a2_helper"], "inject"], b"A2_HELPER_OK\n", "a2-inject-failed", "inject")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-two-items-failed", "two-items")
    print("A2_TWO_REAL_ITEMS_OK", flush=True)
    cpa("duplicate-open", b"A2_OPEN_CORRUPT_OK\n", "a2-open-failed", "open")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-open-unchanged-failed", "open-verify")
    print("A2_OPEN_CORRUPT_UNCHANGED_OK", flush=True)
    cpa("duplicate-create", b"A2_CREATE_UNAVAILABLE_NO_SEND_OK\n", "a2-create-failed", "duplicate-create")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-create-unchanged-failed", "create-verify-after")
    print("A2_CREATE_UNAVAILABLE_UNCHANGED_OK", flush=True)


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def cleanup(work, processes, handles, outputs, report, logs_created):
    # Attempt every owned-child stop and the scanner even if an earlier stop fails.
    stopped = True
    for role, process in reversed(tuple(processes.items())):
        try:
            stop(process)
            ok = process.poll() is not None
            report.set("child-" + role, "stopped" if ok else "failed")
            stopped = stopped and ok
        except subprocess.TimeoutExpired:
            report.set("child-" + role, "timeout")
            stopped = False
        except Exception:
            report.set("child-" + role, "failed")
            stopped = False
    handles_ok = True
    for role, handle in handles.items():
        try:
            handle.close()
            report.set("handle-" + role, "closed")
        except Exception:
            report.set("handle-" + role, "failed")
            handles_ok = False
    if stopped:
        print("OWNED_PROCESSES_STOPPED", flush=True)
    logs_ok = True
    for role in ("bus", "keyring"):
        if role not in logs_created:
            continue
        try:
            # This is the existing log-content read. Missing expected logs now
            # fail completeness; no additional read or existence-only fallback.
            outputs.append((work / (role + ".log")).read_bytes())
            report.set("log-" + role, "collected")
        except FileNotFoundError:
            report.set("log-" + role, "missing")
            logs_ok = False
        except Exception:
            report.set("log-" + role, "read-failed")
            logs_ok = False
    try:
        scanned = scan(work, outputs, report)
    except Exception:
        report.set("scan-content", "failed")
        scanned = False
    complete = scanned and logs_ok and stopped and handles_ok
    report.set("scan-completeness", "complete" if complete else "incomplete")
    if complete and report.states["scan-content"] == "clear":
        print("A2_SECRET_LOG_SCAN_OK", flush=True)
    report.set("cleanup", "ok" if stopped and handles_ok else "failed")


def finish_inside(report, operation, finalizer):
    # Keep the first operation failure independently of scanning and cleanup.
    try:
        operation()
    except Exception as error:
        if report.first is None:
            report.failure("runtime", error)
    finally:
        try:
            finalizer()
        except Exception:
            report.set("cleanup", "failed")
        report.emit()
    require(report.first is None and report.states["cleanup"] == "ok" and
            report.states["scan-completeness"] == "complete" and report.states["scan-content"] == "clear",
            "a2-evidence-failed")


def inside(metadata_path):
    report = Report()
    try:
        metadata = report.run("metadata", lambda: json.loads(Path(metadata_path).read_text()))
    except Exception:
        report.emit()
        raise
    work, uid = Path(metadata["work"]), metadata["uid"]
    processes, handles, outputs, logs_created = {}, {}, [], set()

    def start(args, environment, name):
        handle = (work / (name + ".log")).open("xb")
        handles[name] = handle
        logs_created.add(name)
        process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL,
                                   stdout=handle, stderr=subprocess.STDOUT)
        processes[name] = process
        return process

    def operation():
        report.run("isolation", lambda: base.verify_isolation(metadata))
        environment = dict(os.environ)
        bus = report.run("bus-start", lambda: start(["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog",
                     "--config-file=" + metadata["bus_config"]], environment, "bus"))
        bus_path = work / "runtime" / "bus"

        def bus_ready():
            deadline = time.monotonic() + 10
            while not bus_path.exists() and time.monotonic() < deadline:
                require(bus.poll() is None, "fixture-bus-exited")
                time.sleep(0.05)
            require(bus_path.exists(), "fixture-bus-unavailable")
            base.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
        report.run("bus-ready", bus_ready)
        environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + str(bus_path)
        environment["CPA_SS_FIXTURE_ROOT"] = str(work)
        service = report.run("service-start", lambda: start(["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets",
                         "--control-directory=" + str(work / "runtime" / "keyring")], environment, "keyring"))

        def identity():
            require(bus.poll() is None and service.poll() is None, "fixture-daemon-exited")
            base.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
            base.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)

        def service_ready():
            deadline = time.monotonic() + 15
            while True:
                identity()
                observed = base.run([metadata["observer"], str(bus.pid), str(service.pid)], env=environment, timeout=5, check=False)
                outputs.append(observed.stdout)
                if observed.returncode == 0 and observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n":
                    break
                require(observed.returncode == 30 and time.monotonic() < deadline, "fixture-identity-failed")
                time.sleep(0.1)
        report.run("service-ready", service_ready)
        print("PRIVATE_BUS_SERVICE_UID_PID_OK", flush=True)
        cases(metadata, environment, outputs, identity, report)
    finish_inside(report, operation, lambda: cleanup(work, processes, handles, outputs, report, logs_created))


def outer(binary):
    report = Report()
    try:
        base.outer(Fixture(binary, report))
    except Exception:
        if report.states["outer-operation"] == "not-reached":
            report.set("outer-operation", "failed")
        raise
    finally:
        report.emit(outer=True)
    print("CHECK A2_OUTER_CLEANUP_OK=true", flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 2:
            outer(sys.argv[1])
        else:
            raise base.PreflightFailure("invalid-invocation")
    except base.PreflightFailure as failure:
        label = failure.args[0] if failure.args and failure.args[0] in FAILURES else "secret-service-a2-failed"
        print("FAIL: " + label, file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("FAIL: secret-service-a2-failed", file=sys.stderr)
        sys.exit(1)
