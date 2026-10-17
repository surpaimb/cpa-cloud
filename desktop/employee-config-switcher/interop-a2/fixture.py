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
                               "a2-cleanup-failed", "a2-scan-failed")


def scan(work, outputs):
    # Inspect every available pre-send sample, even if another sample is invalid.
    # Never derive a scanner sample from an accepted/rejected service response.
    secrets, complete = [], True
    for name, length in SAMPLES:
        try:
            value = volatile.read_sample(work / name, length)
            if value is None:
                complete = False
            else:
                secrets.append(value)
        except Exception:
            complete = False
    volatile.reject_secret_output(secrets, outputs)
    return complete


class Fixture(volatile.Fixture):
    markers = MARKERS
    failures = FAILURES

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


def bounded_call(args, environment, expected, label, outputs):
    try:
        result = base.run(args, env=environment, timeout=12, check=False)
    except subprocess.TimeoutExpired as failure:
        outputs.append(failure.stdout or b"")
        raise base.PreflightFailure(label) from None
    outputs.append(result.stdout)
    if result.returncode != 0 or expected not in result.stdout:
        for panic in volatile.diagnostic_labels(result.stdout):
            print("FIXTURE_DIAGNOSTIC " + panic, flush=True)
        raise base.PreflightFailure(label)


def cases(metadata, environment, outputs, identity):
    """One injection, independent two-item proof before either rejection."""
    deadline = time.monotonic() + 30

    def call(args, expected, label):
        # Reserve room for the full bounded child call and cleanup before the
        # unchanged supervisor's 90-second limit. Slow fixtures fail, not retry.
        require(time.monotonic() + 12 <= deadline, "fixture-case-failed")
        identity()
        bounded_call(args, environment, expected, label, outputs)

    def cpa(mode, expected, label):
        environment["CPA_SS_FIXTURE_MODE"] = mode
        call([metadata["cpa"], "--exact", volatile.TEST, "--ignored", "--nocapture", "--test-threads=1"], expected, label)

    call([metadata["verifier"], "setup"], b"LIBSECRET_FIXTURE_OK\n", "fixture-case-failed")
    for mode, marker in (("create", "A2_POSITIVE_CREATE_OK"), ("reopen", "A2_POSITIVE_REOPEN_OK")):
        cpa(mode, b"CPA_FIXTURE_OK\n", "fixture-case-failed")
        call([metadata["verifier"], "verify"], b"LIBSECRET_FIXTURE_OK\n", "fixture-case-failed")
        print(marker, flush=True)
    call([metadata["a2_helper"], "inject"], b"A2_HELPER_OK\n", "a2-inject-failed")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-two-items-failed")
    print("A2_TWO_REAL_ITEMS_OK", flush=True)
    cpa("duplicate-open", b"A2_OPEN_CORRUPT_OK\n", "a2-open-failed")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-open-unchanged-failed")
    print("A2_OPEN_CORRUPT_UNCHANGED_OK", flush=True)
    cpa("duplicate-create", b"A2_CREATE_UNAVAILABLE_NO_SEND_OK\n", "a2-create-failed")
    call([metadata["a2_helper"], "verify-two"], b"A2_HELPER_OK\n", "a2-create-unchanged-failed")
    print("A2_CREATE_UNAVAILABLE_UNCHANGED_OK", flush=True)


def stop(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=5)


def cleanup(work, processes, handles, outputs):
    # Attempt every owned-child stop and the scanner even if an earlier stop fails.
    stopped = True
    for process in reversed(processes):
        try:
            stop(process)
        except Exception:
            stopped = False
    for handle in handles:
        try:
            handle.close()
        except Exception:
            stopped = False
    stopped = stopped and all(process.poll() is not None for process in processes)
    if stopped:
        print("OWNED_PROCESSES_STOPPED", flush=True)
    logs_ok = True
    for name in ("bus.log", "keyring.log"):
        try:
            if (work / name).is_file():
                outputs.append((work / name).read_bytes())
        except Exception:
            logs_ok = False
    scanned = scan(work, outputs)
    require(scanned and logs_ok, "a2-scan-failed")
    print("A2_SECRET_LOG_SCAN_OK", flush=True)
    require(stopped, "a2-cleanup-failed")


def inside(metadata_path):
    metadata = json.loads(Path(metadata_path).read_text())
    base.verify_isolation(metadata)
    work, uid = Path(metadata["work"]), metadata["uid"]
    processes, handles, outputs = [], [], []

    def start(args, environment, name):
        handle = (work / (name + ".log")).open("xb")
        handles.append(handle)
        process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL,
                                   stdout=handle, stderr=subprocess.STDOUT)
        processes.append(process)
        return process

    try:
        environment = dict(os.environ)
        bus = start(["/usr/bin/dbus-daemon", "--nofork", "--nopidfile", "--nosyslog",
                     "--config-file=" + metadata["bus_config"]], environment, "bus")
        bus_path = work / "runtime" / "bus"
        deadline = time.monotonic() + 10
        while not bus_path.exists() and time.monotonic() < deadline:
            require(bus.poll() is None, "fixture-bus-exited")
            time.sleep(0.05)
        require(bus_path.exists(), "fixture-bus-unavailable")
        base.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
        environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + str(bus_path)
        environment["CPA_SS_FIXTURE_ROOT"] = str(work)
        service = start(["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets",
                         "--control-directory=" + str(work / "runtime" / "keyring")], environment, "keyring")

        def identity():
            require(bus.poll() is None and service.poll() is None, "fixture-daemon-exited")
            base.owned_process(bus.pid, "/usr/bin/dbus-daemon", uid)
            base.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)

        deadline = time.monotonic() + 15
        while True:
            identity()
            observed = base.run([metadata["observer"], str(bus.pid), str(service.pid)], env=environment, timeout=5, check=False)
            outputs.append(observed.stdout)
            if observed.returncode == 0 and observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n":
                break
            require(observed.returncode == 30 and time.monotonic() < deadline, "fixture-identity-failed")
            time.sleep(0.1)
        print("PRIVATE_BUS_SERVICE_UID_PID_OK", flush=True)
        cases(metadata, environment, outputs, identity)
    finally:
        cleanup(work, processes, handles, outputs)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 2:
            base.outer(Fixture(sys.argv[1]))
            # Only after the unchanged supervisor's finally block has succeeded.
            print("CHECK A2_OUTER_CLEANUP_OK=true", flush=True)
        else:
            raise base.PreflightFailure("invalid-invocation")
    except base.PreflightFailure as failure:
        label = failure.args[0] if failure.args and failure.args[0] in FAILURES else "secret-service-a2-failed"
        print("FAIL: " + label, file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("FAIL: secret-service-a2-failed", file=sys.stderr)
        sys.exit(1)
