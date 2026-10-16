#!/usr/bin/env python3
"""Independent volatile-collection fixture. No production entry point; see CONTRACT.md."""
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
base_path = HERE / "run.py" if (HERE / "run.py").is_file() else HERE.parent / "preflight" / "run.py"
spec = importlib.util.spec_from_file_location("no_key_preflight", base_path)
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
require = base.require
TEST = "live::linux_secret_service::real_fixture::volatile_collection_interop"
MARKERS = ("ISOLATION_OK", "PRIVATE_BUS_SERVICE_UID_PID_OK", "CREATE_AND_LIBSECRET_OK",
           "NEW_PROCESS_REOPEN_OK", "WRONG_TRANSACTION_UNCHANGED_OK", "WRONG_NAMESPACE_UNCHANGED_OK",
           "MISSING_ITEM_UNCHANGED_OK", "MISSING_SERVICE_OK", "SECRET_LOG_SCAN_OK", "OWNED_PROCESSES_STOPPED")
FAILURES = ("fixture-binary-path", "fixture-binary-missing", "fixture-bus-exited", "fixture-bus-unavailable",
            "fixture-daemon-exited", "fixture-identity-failed", "fixture-case-failed", "fixture-oracle-invalid",
            "fixture-secret-output", "fixture-child-remains", "fixture-scan-incomplete")
PANICS = ("fixture-check-failed", "fixture-root-required", "fixture-mode-required", "fixture-root-metadata",
          "fixture-cgroup", "fixture-approval-failed", "fixture-provider-failed", "fixture-create-failed",
          "fixture-retry-failed", "oracle-create-failed", "oracle-write-failed", "oracle-flush-failed",
          "oracle-metadata-failed", "oracle-read-failed", "oracle-reference-failed", "fixture-reopen-failed",
          "fixture-other-approval-failed", "fixture-other-provider-failed", "fixture-missing-reference-failed",
          "fixture-mode-invalid", "native-access-denied", "native-unknown-method", "native-unknown-object",
          "native-invalid-args", "native-locked", "native-missing", "native-no-session", "native-other-error",
          "diagnostic-root-failed", "pre-send-oracle-failed", "native-decode-ok", "native-decode-failed",
          "native-session-match", "native-session-mismatch", "native-parameters-empty", "native-parameters-nonempty",
          "native-type-octet-stream", "native-type-text-plain", "native-type-empty", "native-type-other",
          "native-length-exact", "native-length-short", "native-length-long", "native-envelope-valid",
          "native-envelope-identity", "native-envelope-format", "native-envelope-checksum", "native-envelope-not-checked")


def secret_forms(value):
    return (value, value.hex().encode(), value.hex().upper().encode(), base64.b64encode(value),
            base64.urlsafe_b64encode(value), base64.b64encode(value).rstrip(b"="),
            base64.urlsafe_b64encode(value).rstrip(b"="),
            "".join("\\x%02x" % byte for byte in value).encode(),
            str(list(value)).encode(), ",".join(str(byte) for byte in value).encode())


def reject_secret_output(secrets, outputs):
    for output in outputs:
        for value in secrets:
            require(all(form not in output for form in secret_forms(value)), "fixture-secret-output")


def read_sample(path, length):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    except FileNotFoundError:
        return None
    with os.fdopen(descriptor, "rb") as sample:
        info = os.fstat(sample.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid() and
                stat.S_IMODE(info.st_mode) == 0o600 and info.st_nlink == 1 and info.st_size == length,
                "fixture-oracle-invalid")
        value = sample.read(length + 1)
        require(len(value) == length, "fixture-oracle-invalid")
        return value


def scan(work, outputs):
    # Only actual outgoing samples, captured before CreateItem. No accepted or
    # rejected response and no reconstruction may supply a scanner oracle.
    secrets = []
    for name, length in (("sent.key", 32), ("sent.envelope", 148)):
        value = read_sample(work / name, length)
        if value is not None:
            secrets.append(value)
    reject_secret_output(secrets, outputs)
    return len(secrets) == 2


class Fixture:
    markers = MARKERS
    failures = FAILURES
    diagnostics = PANICS

    def __init__(self, binary):
        self.binary = Path(binary).resolve(strict=True)

    def prepare(self, root, metadata):
        expected = (HERE.parent / "target" / "debug" / "deps").resolve()
        require(self.binary.parent == expected and self.binary.name.startswith("employee_config_core-"), "fixture-binary-path")
        require(self.binary.is_file() and os.access(self.binary, os.X_OK), "fixture-binary-missing")
        work = Path(metadata["work"])
        for name in ("lock", "other-lock"):
            base.private(work / name, metadata["uid"], metadata["gid"])
        for source, target, mode in ((HERE / "fixture.py", root / "interop.py", 0o444),
                                     (self.binary, root / "cpa-fixture", 0o555)):
            shutil.copyfile(source, target)
            target.chmod(mode)
            print("FIXTURE " + target.name + " sha256=" + hashlib.sha256(target.read_bytes()).hexdigest(), flush=True)
        verifier = root / "libsecret-verifier"
        flags = base.run(["pkg-config", "--cflags", "--libs", "libsecret-1"]).stdout.decode().split()
        base.run(["cc", "-std=c11", "-Wall", "-Wextra", "-Werror", str(HERE / "verifier.c"), "-o", str(verifier), *flags])
        verifier.chmod(0o555)
        print("FIXTURE verifier-source sha256=" + hashlib.sha256((HERE / "verifier.c").read_bytes()).hexdigest(), flush=True)
        print("FIXTURE verifier-executable sha256=" + hashlib.sha256(verifier.read_bytes()).hexdigest(), flush=True)
        # The root owns this fresh config; no live bus exists yet.
        config = Path(metadata["bus_config"])
        config.chmod(0o600)
        config.write_text((HERE / "bus.conf.in").read_text().replace("@USER@", base.USER)
                          .replace("@BUS_PATH@", str(work / "runtime" / "bus")))
        config.chmod(0o444)
        metadata.update(cpa=str(root / "cpa-fixture"), verifier=str(verifier))


def inside(metadata_path):
    metadata = json.loads(Path(metadata_path).read_text())
    base.verify_isolation(metadata)  # Unmodified assertions, before daemon/Key operations.
    work, uid = Path(metadata["work"]), metadata["uid"]
    processes, handles, outputs = [], [], []

    def start(args, environment, name):
        handle = (work / (name + ".log")).open("xb")
        handles.append(handle)
        process = subprocess.Popen(args, env=environment, stdin=subprocess.DEVNULL,
                                   stdout=handle, stderr=subprocess.STDOUT)
        processes.append(process)
        return process

    def call(args, environment, expected, label):
        # Never relay errors/panics; each command has a bounded wall-clock timeout.
        try:
            result = base.run(args, env=environment, timeout=12, check=False)
        except subprocess.TimeoutExpired as failure:
            outputs.append(failure.stdout or b"")
            raise base.PreflightFailure("fixture-case-failed") from None
        outputs.append(result.stdout)
        if result.returncode != 0 or expected not in result.stdout:
            # Both label and exit category are from fixed local source, not output.
            print("CASE_FAILURE " + label, flush=True)
            for panic in PANICS:
                if panic.encode() in result.stdout:
                    print("FIXTURE_DIAGNOSTIC " + panic, flush=True)
            raise base.PreflightFailure("fixture-case-failed")

    def stop(process):
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)

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
        service = start(["/usr/bin/gnome-keyring-daemon", "--foreground", "--components=secrets",
                         "--control-directory=" + str(work / "runtime" / "keyring")], environment, "keyring")
        deadline = time.monotonic() + 15
        while True:
            require(bus.poll() is None and service.poll() is None, "fixture-daemon-exited")
            base.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)
            observed = base.run([metadata["observer"], str(bus.pid), str(service.pid)], env=environment, timeout=5, check=False)
            outputs.append(observed.stdout)
            if observed.returncode == 0 and observed.stdout == b"PREFLIGHT_LIBSECRET_OK\n":
                break
            require(observed.returncode == 30 and time.monotonic() < deadline, "fixture-identity-failed")
            time.sleep(0.1)
        print("PRIVATE_BUS_SERVICE_UID_PID_OK", flush=True)
        call([metadata["verifier"], "setup"], environment, b"LIBSECRET_FIXTURE_OK\n", "setup")
        environment["CPA_SS_FIXTURE_ROOT"] = str(work)
        for action, marker in (("create", "CREATE_AND_LIBSECRET_OK"), ("reopen", "NEW_PROCESS_REOPEN_OK"),
                               ("wrong-transaction", "WRONG_TRANSACTION_UNCHANGED_OK"),
                               ("wrong-namespace", "WRONG_NAMESPACE_UNCHANGED_OK"),
                               ("missing-item", "MISSING_ITEM_UNCHANGED_OK")):
            require(bus.poll() is None and service.poll() is None, "fixture-daemon-exited")
            base.owned_process(service.pid, "/usr/bin/gnome-keyring-daemon", uid)
            environment["CPA_SS_FIXTURE_MODE"] = action
            call([metadata["cpa"], "--exact", TEST, "--ignored", "--nocapture", "--test-threads=1"],
                 environment, b"CPA_FIXTURE_OK\n", action)
            call([metadata["verifier"], "verify"], environment, b"LIBSECRET_FIXTURE_OK\n", "verify")
            print(marker, flush=True)
        stop(service)
        require(service.poll() is not None and bus.poll() is None, "fixture-child-remains")
        environment["CPA_SS_FIXTURE_MODE"] = "no-service"
        call([metadata["cpa"], "--exact", TEST, "--ignored", "--nocapture", "--test-threads=1"],
             environment, b"CPA_FIXTURE_OK\n", "no-service")
        print("MISSING_SERVICE_OK", flush=True)
    finally:
        for process in reversed(processes):
            stop(process)
        for handle in handles:
            handle.close()
        require(all(process.poll() is not None for process in processes), "fixture-child-remains")
        print("OWNED_PROCESSES_STOPPED", flush=True)
        for name in ("bus.log", "keyring.log"):
            if (work / name).is_file():
                outputs.append((work / name).read_bytes())
        require(scan(work, outputs), "fixture-scan-incomplete")
        print("SECRET_LOG_SCAN_OK", flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "--inside":
            inside(sys.argv[2])
        elif len(sys.argv) == 2:
            base.outer(Fixture(sys.argv[1]))
        else:
            raise base.PreflightFailure("invalid-invocation")
    except base.PreflightFailure as failure:
        print("FAIL: " + failure.args[0], file=sys.stderr)
        sys.exit(1)
    except Exception:
        print("FAIL: secret-service-fixture", file=sys.stderr)
        sys.exit(1)
