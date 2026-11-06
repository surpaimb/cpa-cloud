#!/usr/bin/env python3
"""Independently authored synthetic actual-CLI acceptance; see its contract.

Runs the unchanged PR85 sequence before the additional CLI sequence. No secret,
raw output, database, binary or scanner oracle is transferred between A and B.
"""
import ctypes
import importlib.util
import json
import os
from pathlib import Path
import re
import select
import shutil
import stat
import struct
import subprocess
import sys
import threading
import time

spec = importlib.util.spec_from_file_location("cross_vm", Path(__file__).with_name("cross-vm-backup.py"))
cross = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cross)
UNIT = "cpa-cloud-backup-recovery.service"
USER = "cpa-cloud-recovery"
RUNTIME = Path("/run/cpa-cloud-backup-recovery")
FAILURE_STAGES = {"arguments", "platform", "unit", "seed", "identity", "installation", "wrapping", "package", "target", "restore", "cleanup", "cancelled"}
CLI_NEGATIVES = tuple(case for case in cross.NEGATIVES if not case.startswith("publish-")) + (
    "seed-missing", "seed-unsafe", "seed-file-unsafe", "seed-link", "seed-hardlink", "seed-parent-link",
    "input-link", "target-link", "parent-link", "parent-replaced", "expected-provider", "expected-instance",
    "expected-master", "expected-wrapping", "expected-master-version", "expected-wrapping-version", "m2-impersonation")
RESTORE_ONLY = {"target-exists", "target-link", "parent-link", "parent-replaced"}
CLI_CASES = ("cli-verify", "cli-restore", "cli-reopen-one", "cli-reopen-two") + tuple(
    "cli-" + mode + "-" + case for case in CLI_NEGATIVES for mode in (("restore",) if case in RESTORE_ONLY else ("verify", "restore")))
CLI_STAGES = set(CLI_CASES) | {prefix + case for case in CLI_CASES for prefix in ("journal-", "unit-", "barrier-")} | {"cli-invocation-completeness", "cli-runtime-cleanup"}


def expected_stage(case):
    if case == "foreign-activation": return "manager"
    if case.startswith("expected-") or case in {"binding-instance", "binding-id", "binding-version", "backup-id", "backup-version"}: return "identity"
    if case.startswith("seed-"): return "seed"
    if case in RESTORE_ONLY: return "target"
    if case == "m2-impersonation" or case == "binding-canary" or case.startswith("master-") or case in {"missing-master", "wrong-master", "swapped-master", "swapped-both"}: return "installation"
    if case in {"missing-backup", "swapped-backup", "same-value", "backup-instance", "overlap-id"}: return "wrapping"
    return "package"


class ParentBarrier:
    """Root-side Linux permission event, never a test hook in the CLI binary."""
    def __init__(self, package, parent, executable, identity):
        self.package = package
        self.parent, self.executable, self.identity = parent, executable, identity
        self.libc = ctypes.CDLL(None, use_errno=True)
        self.libc.fanotify_init.argtypes = [ctypes.c_uint, ctypes.c_uint]
        self.libc.fanotify_mark.argtypes = [ctypes.c_int, ctypes.c_uint, ctypes.c_uint64, ctypes.c_int, ctypes.c_char_p]
        self.fd = self.libc.fanotify_init(1 | 2 | 4, os.O_RDONLY | os.O_CLOEXEC)
        cross.require(self.fd >= 0, "guard")
        if self.libc.fanotify_mark(self.fd, 1, 0x10000, -100, os.fsencode(package)) != 0:
            os.close(self.fd)
            raise cross.Failure("guard")
        self.injected, self.failed = False, False
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def run(self):
        try:
            deadline = time.monotonic() + 145
            while not self.injected:
                remaining = deadline - time.monotonic()
                cross.require(remaining > 0 and select.select([self.fd], [], [], remaining)[0], "timeout")
                events = os.read(self.fd, 4096)
                cross.require(len(events) > 0 and len(events) % 24 == 0, "evidence")
                for offset in range(0, len(events), 24):
                    length, version, _, metadata, mask, event_fd, pid = struct.unpack("=IBBHQii", events[offset:offset + 24])
                    cross.require(length == 24 and version == 3 and metadata == 24 and mask & 0x10000 and event_fd >= 0 and pid > 1, "evidence")
                    allowed = False
                    try:
                        # systemd may open the path while setting up a read-only
                        # bind mount. Only the real CLI process is our barrier.
                        if Path(os.readlink("/proc/%d/exe" % pid)) == self.executable:
                            cross.require(not self.injected, "evidence")
                            uid_line = next(line for line in Path("/proc/%d/status" % pid).read_text().splitlines() if line.startswith("Uid:"))
                            cross.require(set(map(int, uid_line.split()[1:])) == {self.identity[0]}, "identity")
                            os.rename(self.parent, str(self.parent) + "-before")
                            cross.private_dir(self.parent, self.identity)
                            cross.write_private(self.parent / "public.sentinel", b"public-parent-replacement-sentinel", self.identity)
                            # Only the first real CLI open is blocked. Remove the
                            # mark before allowing it so later identity checks do
                            # not wait on an already-completed barrier thread.
                            cross.require(self.libc.fanotify_mark(self.fd, 2, 0x10000, -100, os.fsencode(self.package)) == 0, "evidence")
                            self.injected = True
                        allowed = True
                    finally:
                        os.write(self.fd, struct.pack("=iI", event_fd, 1 if allowed else 2))
                        os.close(event_fd)
        except Exception:
            self.failed = True

    def finish(self):
        self.thread.join(1)
        os.close(self.fd)
        cross.require(not self.thread.is_alive() and self.injected and not self.failed, "evidence")


def source_fingerprint(work):
    values = {}
    for path in (work / "input.cpacb", work / "seed", work / "seed" / "master-key-binding.json"):
        if not os.path.lexists(path):
            values[str(path.relative_to(work))] = None
            continue
        info = path.lstat()
        value = (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode, info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)
        if stat.S_ISREG(info.st_mode):
            # Fixture fingerprinting deliberately admits the negative's own
            # hardlink; production CLI admission must reject it.
            with path.open("rb") as stream:
                data = stream.read((256 << 20) + 1)
                cross.require(len(data) <= 256 << 20 and len(data) == info.st_size)
                value += (data,)
        if stat.S_ISLNK(info.st_mode): value += (os.readlink(path),)
        values[str(path.relative_to(work))] = value
    return values


def private_restored(path, identity):
    directory = path.lstat()
    cross.require(stat.S_ISDIR(directory.st_mode) and stat.S_IMODE(directory.st_mode) == 0o700 and
                  (directory.st_uid, directory.st_gid) == identity, "publication")
    result = cross.fingerprint(path)
    cross.require(all((value[2], value[3]) == identity and value[4] == 0o600 and value[5] == 1 for value in result.values()), "publication")
    return result


class Harness(cross.Harness):
    def __init__(self, role, binary, relay, sha, cli):
        super().__init__(role, binary, relay, sha)
        self.user = USER
        self.cli_binary = Path(cli).resolve(strict=True)
        self.cli_started, self.cli_journals = set(), set()

    def command(self, args, payload=None, check=True, timeout=30):
        try:
            return super().command(args, payload, check, timeout)
        except subprocess.TimeoutExpired:
            # Output after a timed-out pipe capture cannot be claimed complete,
            # even when the exact unit journal and later stop were collected.
            self.capture_complete = False
            raise

    def emit(self, stage, state):
        cross.require(stage in cross.STAGES | CLI_STAGES and state in cross.STATES)
        print("OFFLINEVM " + self.role + " " + stage + " " + state, flush=True)

    def preflight(self):
        super().preflight()
        cross.require(self.cli_binary.is_file() and os.access(self.cli_binary, os.X_OK) and not os.path.lexists(RUNTIME), "identity")
        cross.require(self.command(["systemctl", "show", UNIT, "--property=LoadState", "--value"], check=False).stdout.strip() == b"not-found", "identity")

    def provision(self):
        super().provision()
        self.cli = self.root / "cpa-cloud-backup"
        shutil.copyfile(self.cli_binary, self.cli)
        self.cli.chmod(0o555)

    def new_work(self, case):
        original = self.root
        try:
            self.root = original / "offline"
            return super().case_work(case)
        finally:
            self.root = original

    def cli_args(self, work, restore, overrides=None):
        values = {"input": str(work / "input.cpacb"), "binding-seed": str(work / "seed"), "expect-provider": "cross-vm-backup",
                  "expect-instance": cross.same.INSTANCE, "expect-master-id": "installation-root", "expect-master-version": "1",
                  "expect-wrapping-id": "backup-wrapping", "expect-wrapping-version": "1", "expect-source-version": self.sha}
        # Match the unchanged constrained source fixture's public wrapping IDs.
        public = self.materials["backup-good"][1]
        values["expect-provider"], values["expect-wrapping-id"] = public["provider_id"], public["key_id"]
        if restore: values["data-dir"] = str(work / "restored")
        values.update(overrides or {})
        args = [str(self.cli), "systemd-v3-restore" if restore else "systemd-v3-verify"]
        for key, value in values.items(): args += ["--" + key, value]
        return args

    def run_cli(self, label, work, restore=False, want=None, master="master-good", backup="backup-good", overrides=None, barrier=None):
        cross.require(label in CLI_CASES and label not in self.cli_started)
        cross.require(self.command(["systemctl", "show", UNIT, "--property=LoadState", "--value"], check=False).stdout.strip() == b"not-found", "identity")
        cross.require(not os.path.lexists(RUNTIME), "cleanup")
        cursor_output = self.command(["journalctl", "--boot", "-n1", "--show-cursor", "--no-pager", "--output=json"]).stdout
        cursor = re.search(rb"^-- cursor: ([^\r\n]+)$", cursor_output, re.MULTILINE)
        cross.require(cursor is not None, "evidence")
        properties = ["User=" + USER, "Group=" + USER, "UMask=0077", "NoNewPrivileges=yes", "Restart=no",
                      "PrivateNetwork=yes", "PrivateIPC=yes", "PrivateDevices=yes", "PrivateTmp=yes", "ProtectHome=yes",
                      "ProtectSystem=strict", "CapabilityBoundingSet=", "RestrictAddressFamilies=AF_UNIX", "LimitCORE=0",
                      "TemporaryFileSystem=/dev/shm:rw,mode=1777", "ReadWritePaths=" + str(work),
                      "ReadOnlyPaths=" + str(work / "input.cpacb") + " " + str(work / "seed"),
                      "RuntimeDirectory=cpa-cloud-backup-recovery", "RuntimeDirectoryMode=0700",
                      "RuntimeMaxSec=120", "TimeoutStopSec=10", "KillMode=control-group", "WorkingDirectory=" + str(work)]
        for name, variant in ((cross.same.MASTER, master), (cross.same.BACKUP, backup)):
            if variant: properties.append("LoadCredentialEncrypted=" + name + ":" + str(self.encrypted / variant))
        # Missing/linked seeds are CLI admission tests, not manager mount errors.
        if not (work / "seed").is_dir() or (work / "seed").is_symlink() or (work / "input.cpacb").is_symlink():
            properties = [item for item in properties if not item.startswith("ReadOnlyPaths=")]
        args = ["systemd-run", "--wait", "--quiet", "--pipe", "--unit=" + UNIT]
        if want != "manager": args.append("--collect")
        args += ["--property=" + item for item in properties]
        args += ["--setenv=TMPDIR=" + str(RUNTIME), "--setenv=CPA_RECOVERY_HOST_NETNS=" + os.readlink("/proc/self/ns/net")]
        args += self.cli_args(work, restore, overrides)
        self.units.add(UNIT)
        self.stopped.discard(UNIT)
        self.stop_attempted.discard(UNIT)
        self.cli_started.add(label)
        try:
            result = self.command(args, check=False, timeout=150)
            failures = re.findall(rb"OFFLINE_V3 ([a-z]+) failed", result.stdout)
            if (want and want != "manager" and failures != [want.encode()]) or (not want and result.returncode != 0):
                for stage in sorted(FAILURE_STAGES):
                    if stage.encode() in failures:
                        print("OFFLINEVM_DIAGNOSTIC " + self.role + " " + label + " " + stage, flush=True)
            if want == "manager":
                state = self.command(["systemctl", "show", UNIT, "--property=ExecMainStatus", "--value"])
                cross.require(result.returncode != 0 and state.stdout.strip() == b"243" and not failures and b"OFFLINE_V3" not in result.stdout, "evidence")
            elif want:
                cross.require(result.returncode != 0 and failures == [want.encode()] and b" ok" not in result.stdout, "evidence")
            else:
                marker = b"OFFLINE_V3 " + (b"restore" if restore else b"verify") + b" ok\n"
                cross.require(result.returncode == 0 and result.stdout == marker, "evidence")
        finally:
            errors = []
            try:
                journal = self.command(["journalctl", "--boot", "--unit=" + UNIT, "--after-cursor=" + cursor[1].decode(), "--no-pager", "--output=json"])
                cross.require(cross.journal_matches(journal.stdout, UNIT), "evidence")
                self.journals.add(UNIT)
                self.cli_journals.add(label)
                self.emit("journal-" + label, "collected")
            except Exception as error:
                errors.append(error)
                self.capture_complete = False
                self.emit("journal-" + label, "failed")
            previous, self.cleaning = self.cleaning, True
            try:
                self.stop_unit(UNIT)
                cross.require(not os.path.lexists(RUNTIME), "cleanup")
                self.emit("unit-" + label, "ok")
            except Exception as error:
                errors.append(error)
                self.emit("unit-" + label, "failed")
            finally:
                self.cleaning = previous
                if barrier is not None:
                    try:
                        barrier.finish()
                        self.emit("barrier-" + label, "ok")
                    except Exception as error:
                        errors.append(error)
                        self.capture_complete = False
                        self.emit("barrier-" + label, "failed")
            if errors and sys.exc_info()[0] is None: raise cross.Failure("cleanup")

    def receiver(self):
        super().receiver()  # Original PR85 sequence and all 28 negatives unchanged.
        cross.private_dir(self.root / "offline", self.identity)
        positive = self.new_work("positive")
        original = source_fingerprint(positive)
        self.action("cli-verify", lambda: self.run_cli("cli-verify", positive))
        self.action("cli-restore", lambda: self.run_cli("cli-restore", positive, True))
        cross.require(source_fingerprint(positive) == original, "publication")
        private_restored(positive / "restored", self.identity)
        for case in ("reopen-one", "reopen-two"):
            self.unit_prefix = USER + "-cli"
            self.action("cli-" + case, lambda case=case: self.run_case(case, positive))
            private_restored(positive / "restored", self.identity)
            del self.unit_prefix
        baseline = private_restored(positive / "restored", self.identity)
        for case in CLI_NEGATIVES:
            for restore in ((True,) if case in RESTORE_ONLY else (False, True)):
                label = "cli-" + ("restore" if restore else "verify") + "-" + case
                def negative(case=case, restore=restore, label=label):
                    # Separate fixture per actual command; no retry of a failed command.
                    work = self.new_work(case + ("-restore" if restore else "-verify"))
                    # Apply only the existing independently authored mutations to this copy.
                    if case in {"header", "ciphertext", "truncate-header", "truncate-body"}:
                        package = bytearray(self.incoming["package.cpacb"])
                        if case == "header": package[16] ^= 1
                        if case == "ciphertext": package[-1] ^= 1
                        if case == "truncate-header": package = package[:32]
                        if case == "truncate-body": package = package[:-1]
                        (work / "input.cpacb").unlink()
                        cross.write_private(work / "input.cpacb", package, self.identity)
                    if case.startswith("binding-"):
                        binding = json.loads(self.incoming["binding.json"])
                        if case == "binding-instance": binding["instance_id"] = "11234567-89ab-cdef-0123-456789abcdef"
                        if case == "binding-id": binding["key_id"] = "different-installation-id"
                        if case == "binding-version": binding["key_version"] = 2
                        if case == "binding-canary":
                            value = binding["canary_ciphertext"]
                            binding["canary_ciphertext"] = ("A" if value[0] != "A" else "B") + value[1:]
                        (work / "seed" / "master-key-binding.json").unlink()
                        cross.write_private(work / "seed" / "master-key-binding.json", json.dumps(binding).encode(), self.identity)
                    overrides, barrier, barrier_parent = {}, None, None
                    if case == "seed-missing": (work / "seed" / "master-key-binding.json").unlink()
                    if case == "seed-unsafe": (work / "seed").chmod(0o755)
                    if case == "seed-file-unsafe": (work / "seed" / "master-key-binding.json").chmod(0o644)
                    if case in {"seed-link", "seed-hardlink"}:
                        path = work / "seed" / "master-key-binding.json"
                        saved = work / "retained-binding"
                        path.rename(saved)
                        if case == "seed-link": path.symlink_to(saved)
                        else: os.link(saved, path)
                    if case == "seed-parent-link":
                        (work / "seed").rename(work / "seed-before")
                        (work / "seed").symlink_to(work / "seed-before", target_is_directory=True)
                    if case == "input-link":
                        (work / "input.cpacb").rename(work / "input-before")
                        (work / "input.cpacb").symlink_to(work / "input-before")
                    if case == "target-exists":
                        cross.private_dir(work / "restored", self.identity)
                        cross.write_private(work / "restored" / "public.sentinel", b"public-existing-target-sentinel", self.identity)
                    if case == "target-link":
                        cross.private_dir(work / "existing", self.identity)
                        (work / "restored").symlink_to(work / "existing", target_is_directory=True)
                    if case in {"parent-link", "parent-replaced"}:
                        parent = work / "parent"
                        cross.private_dir(parent, self.identity)
                        overrides["data-dir"] = str(parent / "restored")
                        if case == "parent-link":
                            parent.rename(work / "parent-before")
                            parent.symlink_to(work / "parent-before", target_is_directory=True)
                        else: barrier_parent = parent
                    public_fields = {"expected-provider": ("expect-provider", "wrong-provider"),
                        "expected-instance": ("expect-instance", "11234567-89ab-cdef-0123-456789abcdef"),
                        "expected-master": ("expect-master-id", "wrong-master"), "expected-wrapping": ("expect-wrapping-id", "wrong-wrapping"),
                        "expected-master-version": ("expect-master-version", "2"), "expected-wrapping-version": ("expect-wrapping-version", "2")}
                    if case in public_fields:
                        key, value = public_fields[case]
                        overrides[key] = value
                    # Compute source identity before arming the permission barrier.
                    before = source_fingerprint(work)
                    if barrier_parent is not None: barrier = ParentBarrier(work / "input.cpacb", barrier_parent, self.cli, self.identity)
                    master, backup = cross.credentials_for(case)
                    if case == "m2-impersonation": master = "master-wrong"
                    self.run_cli(label, work, restore, expected_stage(case), master, backup, overrides, barrier)
                    cross.require(source_fingerprint(work) == before, "publication")
                    if case not in {"target-exists", "target-link"}: cross.require(not os.path.lexists(work / "restored"), "publication")
                    if case == "target-exists":
                        cross.require({p.name for p in (work / "restored").iterdir()} == {"public.sentinel"} and
                            (work / "restored" / "public.sentinel").read_bytes() == b"public-existing-target-sentinel", "publication")
                    if case == "target-link": cross.require(not list((work / "existing").iterdir()), "publication")
                    if case == "parent-replaced":
                        cross.require(not os.path.lexists(work / "parent" / "restored") and not os.path.lexists(work / "parent-before" / "restored") and
                            {p.name for p in (work / "parent").iterdir()} == {"public.sentinel"} and not list((work / "parent-before").iterdir()), "publication")
                    cross.require(cross.fingerprint(positive / "restored") == baseline and source_fingerprint(positive) == original, "publication")
                self.action(label, negative)

    def cleanup(self):
        self.emit("cli-invocation-completeness", "complete" if self.cli_started == self.cli_journals else "incomplete")
        if self.cli_started != self.cli_journals: self.capture_complete = False
        runtime_ok = not self.cli_started or not os.path.lexists(RUNTIME)
        self.emit("cli-runtime-cleanup", ("ok" if runtime_ok else "failed") if self.cli_started else "not-created")
        super().cleanup()
        cross.require(runtime_ok, "cleanup")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 6: raise cross.Failure("guard")
        Harness(*sys.argv[1:]).main()
    except Exception:
        print("OFFLINEVM_FAILED", file=sys.stderr)
        sys.exit(1)
