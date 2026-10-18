#!/usr/bin/env python3
"""Synthetic two-VM acceptance only; docs/linux-cross-vm-backup-contract.md."""
import base64
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
import tempfile

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("same_vm", HERE / "smoke-linux-systemd-backup.py")
same = importlib.util.module_from_spec(spec)
spec.loader.exec_module(same)
ALLOWLIST = {"package.cpacb", "binding.json", "foreign-master.capsule", "vm.json"}
LIMITS = {"package.cpacb": 256 << 20, "binding.json": 4096, "foreign-master.capsule": 16384, "vm.json": 1024}
NEGATIVES = ("foreign-activation", "missing-master", "wrong-master", "swapped-master", "swapped-both",
             "master-instance", "master-id", "master-version", "binding-instance", "binding-id", "binding-version",
             "binding-canary", "missing-backup", "swapped-backup", "same-value", "backup-instance", "overlap-id",
             "wrong-backup", "backup-id", "backup-version", "header", "ciphertext", "truncate-header", "truncate-body",
             "publish-credential", "publish-parent", "target-exists", "publish-binding")
CASES = ("source", "restore", "reopen-one", "reopen-two") + NEGATIVES
STAGES = {"preflight", "provision", "relay", "operation", "scan-content", "scan-completeness",
          "unit-cleanup", "identity-cleanup", "root-cleanup", "local-relay-cleanup"} | set(CASES) | {
              prefix + case for prefix in ("journal-", "unit-") for case in CASES}
STATES = {"ok", "failed", "timeout", "not-reached", "clear", "matched", "complete", "incomplete", "not-created", "collected"}
CATEGORIES = {"guard", "command", "timeout", "scan-match", "evidence", "identity", "publication", "cleanup"}
SENTINELS = (b"synthetic-cross-vm-admin-only", b"synthetic-cross-vm-record-only",
             b"synthetic-cross-vm-admin-session-only", b"synthetic-cross-vm-csrf-only",
             b"synthetic-cross-vm-employee-secret-only", b"synthetic-cross-vm-oauth-only")
GO_DIAGNOSTICS = ("cross-identity-failed", "cross-root-failed", "cross-netns-failed", "cross-unexpected-socket",
                  "cross-socket-denial-failed", "cross-source-init-failed", "cross-source-open-failed",
                  "cross-source-record-failed", "cross-source-session-failed", "cross-source-material-failed",
                  "cross-source-create-failed", "cross-source-verify-failed", "cross-policy-seed-failed",
                  "cross-key-authentication-failed", "cross-policy-permission-failed", "cross-record-decrypt-failed",
                  "cross-preparation-stage-failed", "cross-invalid-installation-opened", "cross-invalid-credential-admitted",
                  "cross-invalid-package-admitted", "cross-restore-failed", "cross-publish-rejection-failed",
                  "cross-publish-rejection-stage-failed", "cross-parent-rejection-stage-failed", "cross-existing-target-changed",
                  "cross-session-cleanup-failed", "cross-admin-state-failed", "cross-reopen-failed", "cross-workers-enabled",
                  "cross-source-version-failed", "cross-stage-inspect-failed", "cross-binding-replace-failed")


class Failure(Exception):
    def __init__(self, category):
        self.category = category if category in CATEGORIES else "guard"


def require(ok, category="guard"):
    if not ok:
        raise Failure(category)


def forms(value):
    return (value, value.hex().encode(), value.hex().upper().encode(), base64.b64encode(value),
            base64.b64encode(value).rstrip(b"="), base64.urlsafe_b64encode(value).rstrip(b"="),
            "".join("\\x%02x" % byte for byte in value).encode(), str(list(value)).encode(),
            ",".join(str(byte) for byte in value).encode())


def journal_matches(data, unit):
    entries = [json.loads(line) for line in data.splitlines() if line.strip()]
    return any(isinstance(entry, dict) and (entry.get("UNIT") == unit or entry.get("_SYSTEMD_UNIT") == unit or
               entry.get("OBJECT_SYSTEMD_UNIT") == unit) for entry in entries)


def fixtures():
    result = same.fixtures()
    for role, prefix in (("master", same.MASTER), ("backup", same.BACKUP)):
        original = result[role + "-good"][1]
        for suffix, field, value in (("instance", "instance_id", "11234567-89ab-cdef-0123-456789abcdef"),
                                     ("id", "key_id", role + "-different-id"), ("version", "key_version", 2)):
            result[role + "-" + suffix] = (prefix, {**original, field: value})
    result["backup-overlap"] = (same.BACKUP, {**result["backup-good"][1], "key_id": "installation-root"})
    return result


def read_regular(path, maximum):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    with os.fdopen(descriptor, "rb") as stream:
        before = os.fstat(stream.fileno())
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and 0 < before.st_size <= maximum)
        data = stream.read(maximum + 1)
        after = os.fstat(stream.fileno())
        entry = os.stat(path, follow_symlinks=False)
        require(len(data) == before.st_size and before.st_size == after.st_size and
                (before.st_dev, before.st_ino) == (after.st_dev, after.st_ino) == (entry.st_dev, entry.st_ino))
        return data


def write_private(path, data, identity=None):
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        require(stat.S_IMODE(os.fstat(stream.fileno()).st_mode) == 0o600)
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    if identity:
        os.chown(path, *identity)


def private_dir(path, identity):
    path.mkdir(mode=0o700)
    os.chown(path, *identity)


def validate_relay(directory, sha):
    require(directory.is_dir() and not directory.is_symlink() and {p.name for p in directory.iterdir()} == ALLOWLIST)
    data = {name: read_regular(directory / name, LIMITS[name]) for name in ALLOWLIST}
    manifest = json.loads(data["vm.json"])
    require(set(manifest) == {"sha", "backup_format", "credential_format", "machine", "boot"} and manifest["sha"] == sha and
            manifest["backup_format"] == 3 and manifest["credential_format"] == 1)
    require(re.fullmatch(r"[a-f0-9]{32}", manifest["machine"]) and re.fullmatch(r"[a-f0-9-]{36}", manifest["boot"]))
    binding = json.loads(data["binding.json"])
    require(set(binding) == {"format_version", "provider_kind", "instance_id", "key_id", "key_version", "canary_nonce", "canary_ciphertext"})
    require(binding["format_version"] == 1 and binding["provider_kind"] == "linux-systemd-credential-v1" and
            binding["instance_id"] == same.INSTANCE and binding["key_id"] == "installation-root" and binding["key_version"] == 1)
    return data, manifest


def relay_location(path):
    path = Path(path)
    parent = Path(os.environ.get("RUNNER_TEMP", "")).resolve(strict=True)
    require(parent.is_absolute() and path.is_absolute() and path.parent == parent and path.name == "cpa-crossvm-relay" and
            not path.is_symlink() and path.resolve() == path)
    return path


def remove_local_relay(path):
    path = relay_location(path)
    if not path.exists():
        return
    require({p.name for p in path.iterdir()} <= ALLOWLIST)
    for entry in path.iterdir():
        require(entry.is_file() and not entry.is_symlink())
        entry.unlink()
    path.rmdir()


class Harness:
    def __init__(self, role, binary, relay, sha):
        require(role in ("a", "b") and re.fullmatch(r"[a-f0-9]{40}", sha))
        self.role, self.binary, self.relay, self.sha = role, Path(binary).resolve(strict=True), relay_location(relay), sha
        self.root, self.identity, self.user_created = None, None, False
        self.user = "cpa-crossvm-" + role
        self.units, self.stopped, self.stop_attempted = set(), set(), set()
        self.outputs, self.journals, self.first = [], set(), None
        self.materials = fixtures()
        self.oracles = list(same.KEYS) + list(SENTINELS) + list(same.SENTINELS)
        # Register the exact documents before the first encryption stdin write.
        self.payloads = {name: json.dumps(document, sort_keys=True).encode() for name, (_, document) in self.materials.items()}
        self.oracles.extend(self.payloads.values())
        self.needles = tuple(form for value in self.oracles for form in forms(value))
        self.content_match, self.capture_complete = False, True
        self.cleaning = False

    def emit(self, stage, state):
        require(stage in STAGES and state in STATES)
        print("CROSSVM " + self.role + " " + stage + " " + state, flush=True)

    def action(self, stage, function):
        try:
            result = function()
            require(not self.content_match, "scan-match")
            require(self.capture_complete, "evidence")
        except Exception as error:
            category = "timeout" if isinstance(error, subprocess.TimeoutExpired) else error.category if isinstance(error, Failure) else "guard"
            if self.first is None:
                self.first = (stage, category)
            self.emit(stage, "timeout" if category == "timeout" else "failed")
            raise
        self.emit(stage, "ok")
        return result

    def capture(self, data):
        self.outputs.append(data)
        if len(data) > 4 << 20:
            self.capture_complete = False
        if any(needle in data for needle in self.needles):
            self.content_match = True

    def command(self, args, payload=None, check=True, timeout=30):
        try:
            result = subprocess.run(args, input=payload, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout, check=False)
        except subprocess.TimeoutExpired as failure:
            self.capture(failure.stdout or b"")
            raise
        self.capture(result.stdout)
        require(not check or result.returncode == 0, "command")
        if not self.cleaning and result.returncode == 0:
            require(not self.content_match, "scan-match")
            require(self.capture_complete, "evidence")
        return result

    def preflight(self):
        require(sys.platform == "linux" and os.geteuid() == 0 and os.environ.get("GITHUB_ACTIONS") == "true" and
                os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted" and os.environ.get("RUNNER_OS") == "Linux")
        require(Path("/proc/1/comm").read_text().strip() == "systemd")
        self.command(["systemd-detect-virt", "--vm"])
        import pwd
        import grp
        for lookup in (pwd.getpwnam, grp.getgrnam):
            try:
                lookup(self.user)
            except KeyError:
                pass
            else:
                raise Failure("identity")
        require(not os.path.lexists("/var/lib/systemd/credential.secret"), "identity")
        require(self.binary.is_file() and os.access(self.binary, os.X_OK))
        if self.role == "a":
            require(not self.relay.exists())
        else:
            self.incoming, manifest = validate_relay(self.relay, self.sha)
            self.capture(b"".join(self.incoming.values()))
            require(manifest["machine"] != Path("/etc/machine-id").read_text().strip() and
                    manifest["boot"] != Path("/proc/sys/kernel/random/boot_id").read_text().strip(), "identity")

    def provision(self):
        import pwd
        os.umask(0o077)
        self.root = Path(tempfile.mkdtemp(prefix="cpa-cross-vm-v3.", dir="/opt"))
        self.root.chmod(0o711)
        self.command(["useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", self.user])
        self.user_created = True
        user = pwd.getpwnam(self.user)
        self.identity = (user.pw_uid, user.pw_gid)
        require(user.pw_uid != 0 and user.pw_gid != 0, "identity")
        self.executable = self.root / "cross-vm.test"
        shutil.copyfile(self.binary, self.executable)
        self.executable.chmod(0o555)
        self.encrypted = self.root / "credentials"
        self.encrypted.mkdir(mode=0o700)
        self.command(["systemd-creds", "setup"])
        names = ("master-good", "backup-good") if self.role == "a" else tuple(self.materials)
        for name in names:
            credential, _ = self.materials[name]
            self.command(["systemd-creds", "encrypt", "--with-key=host", "--name=" + credential, "-", str(self.encrypted / name)],
                         payload=self.payloads[name])
        if self.role == "b":
            write_private(self.encrypted / "foreign-master", self.incoming["foreign-master.capsule"])

    def stop_unit(self, unit):
        if unit in self.stop_attempted:
            require(unit in self.stopped, "cleanup")
            return
        self.stop_attempted.add(unit)
        self.command(["systemctl", "stop", unit], check=False)
        result = self.command(["systemctl", "show", unit, "--property=ActiveState", "--property=MainPID", "--property=ControlPID"], check=False)
        values = dict(line.split("=", 1) for line in result.stdout.decode().splitlines() if "=" in line)
        require(values.get("ActiveState") in ("inactive", "failed") and values.get("MainPID") == "0" and values.get("ControlPID") == "0", "cleanup")
        self.stopped.add(unit)
        self.command(["systemctl", "reset-failed", unit], check=False)

    def run_case(self, case, work, master="master-good", backup="backup-good"):
        require(case in CASES)
        unit = self.user + "-" + case + ".service"
        require(self.command(["systemctl", "show", unit, "--property=LoadState", "--value"], check=False).stdout.strip() == b"not-found", "identity")
        if not (work / "tmp").exists():
            private_dir(work / "tmp", self.identity)
        properties = ["User=" + self.user, "Group=" + self.user, "UMask=0077", "NoNewPrivileges=yes", "Restart=no",
                      "PrivateNetwork=yes", "PrivateIPC=yes", "PrivateDevices=yes", "PrivateTmp=yes", "ProtectHome=yes",
                      "ProtectSystem=strict", "CapabilityBoundingSet=", "RestrictAddressFamilies=AF_UNIX", "LimitCORE=0",
                      "TemporaryFileSystem=/dev/shm:rw,mode=1777", "ReadWritePaths=" + str(work),
                      "RuntimeMaxSec=120", "TimeoutStopSec=10", "KillMode=control-group", "WorkingDirectory=" + str(work)]
        for name, variant in ((same.MASTER, master), (same.BACKUP, backup)):
            if variant:
                properties.append("LoadCredentialEncrypted=" + name + ":" + str(self.encrypted / variant))
        environment = {"CPA_CROSS_CASE": case, "CPA_CROSS_ROOT": str(work), "CPA_CROSS_UID": str(self.identity[0]),
                       "CPA_CROSS_HOST_NETNS": os.readlink("/proc/self/ns/net"), "CPA_CROSS_SHA": self.sha, "TMPDIR": str(work / "tmp")}
        args = ["systemd-run", "--wait", "--quiet", "--pipe", "--unit=" + unit]
        args += ["--property=" + item for item in properties]
        args += ["--setenv=" + key + "=" + value for key, value in environment.items()]
        args += [str(self.executable), "-test.run=^TestCrossVMBackup$", "-test.count=1", "-test.v", "-test.timeout=90s"]
        self.units.add(unit)
        try:
            result = self.command(args, check=False, timeout=150)
            if case == "foreign-activation":
                state = self.command(["systemctl", "show", unit, "--property=ExecMainStatus", "--value"])
                require(result.returncode != 0 and state.stdout.strip() == b"243" and b"CROSS_ENTRY" not in result.stdout, "evidence")
            else:
                stage = rejection_stage(case)
                marker = ("CROSS_SOURCE_OK" if case == "source" else "CROSS_RESTORE_OK" if case == "restore" else
                          "CROSS_REOPEN_OK" if case.startswith("reopen-") else "CROSS_REJECT_" + stage)
                if result.returncode != 0:
                    tokens = set(re.findall(rb"cross-[a-z-]+", result.stdout))
                    for label in GO_DIAGNOSTICS:
                        if label.encode() in tokens:
                            print("CROSSVM_DIAGNOSTIC " + self.role + " " + case + " " + label, flush=True)
                require(result.returncode == 0 and marker.encode() in result.stdout and
                        b"--- PASS: TestCrossVMBackup" in result.stdout, "evidence")
        finally:
            # Journal, process stop and later root cleanup retain separate results.
            errors = []
            try:
                journal = self.command(["journalctl", "--boot", "--unit=" + unit, "--no-pager", "--output=json"])
                require(journal_matches(journal.stdout, unit), "evidence")
                self.journals.add(unit)
                self.emit("journal-" + case, "collected")
            except Exception as error:
                errors.append(error)
                self.capture_complete = False
                self.emit("journal-" + case, "failed")
            previous = self.cleaning
            self.cleaning = True
            try:
                self.stop_unit(unit)
                self.emit("unit-" + case, "ok")
            except Exception as error:
                errors.append(error)
                self.emit("unit-" + case, "failed")
            finally:
                self.cleaning = previous
            if errors and sys.exc_info()[0] is None:
                raise Failure("cleanup")

    def export(self, work):
        require(self.units == self.stopped, "cleanup")
        self.relay.mkdir(mode=0o700)
        runner = int(os.environ.get("SUDO_UID", "0"))
        require(runner > 0, "identity")
        import pwd
        identity = (runner, pwd.getpwuid(runner).pw_gid)
        values = {"package.cpacb": read_regular(work / "output.cpacb", LIMITS["package.cpacb"]),
                  "binding.json": read_regular(work / "data" / "master-key-binding.json", 4096),
                  "foreign-master.capsule": read_regular(self.encrypted / "master-good", 16384),
                  "vm.json": json.dumps({"sha": self.sha, "backup_format": 3, "credential_format": 1,
                      "machine": Path("/etc/machine-id").read_text().strip(),
                      "boot": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}, sort_keys=True).encode()}
        self.capture(b"".join(values.values()))
        for name, data in values.items():
            write_private(self.relay / name, data, identity)
        os.chown(self.relay, *identity)
        validate_relay(self.relay, self.sha)

    def case_work(self, case):
        work = self.root / case
        private_dir(work, self.identity)
        private_dir(work / "seed", self.identity)
        package = bytearray(self.incoming["package.cpacb"])
        if case == "header": package[16] ^= 1
        if case == "ciphertext": package[-1] ^= 1
        if case == "truncate-header": package = package[:32]
        if case == "truncate-body": package = package[:-1]
        write_private(work / "input.cpacb", package, self.identity)
        binding = json.loads(self.incoming["binding.json"])
        if case == "binding-instance": binding["instance_id"] = "11234567-89ab-cdef-0123-456789abcdef"
        if case == "binding-id": binding["key_id"] = "different-installation-id"
        if case == "binding-version": binding["key_version"] = 2
        if case == "binding-canary":
            value = binding["canary_ciphertext"]
            binding["canary_ciphertext"] = ("A" if value[0] != "A" else "B") + value[1:]
        encoded = self.incoming["binding.json"] if not case.startswith("binding-") else json.dumps(binding).encode()
        write_private(work / "seed" / "master-key-binding.json", encoded, self.identity)
        return work

    def receiver(self):
        positive = self.case_work("restore")
        self.action("restore", lambda: self.run_case("restore", positive))
        for case in ("reopen-one", "reopen-two"):
            self.action(case, lambda case=case: self.run_case(case, positive))
        baseline = fingerprint(positive / "restored")
        for case in NEGATIVES:
            def negative(case=case):
                work = self.case_work(case)
                master, backup = credentials_for(case)
                self.run_case(case, work, master, backup)
                require(fingerprint(positive / "restored") == baseline, "publication")
                if case != "target-exists":
                    require(not os.path.lexists(work / "parent" / "restored"), "publication")
            self.action(case, negative)

    def cleanup(self):
        self.cleaning = True
        complete = True
        for unit in sorted(self.units):
            try:
                self.stop_unit(unit)
            except Exception:
                complete = False
        self.emit("unit-cleanup", ("ok" if complete else "failed") if self.units else "not-created")
        identities_ok = complete
        if self.user_created and complete:
            try:
                self.command(["userdel", self.user])
                import grp
                try:
                    grp.getgrnam(self.user)
                except KeyError:
                    pass
                else:
                    self.command(["groupdel", self.user])
            except Exception:
                identities_ok = False
        self.emit("identity-cleanup", ("ok" if identities_ok else "failed") if self.user_created else "not-created")
        root_ok = complete
        if self.root is not None and complete:
            try:
                require(self.root.resolve() == self.root and self.root.parent == Path("/opt") and
                        self.root.name.startswith("cpa-cross-vm-v3.") and not self.root.is_symlink(), "cleanup")
                shutil.rmtree(self.root)
            except Exception:
                root_ok = False
        self.emit("root-cleanup", ("ok" if root_ok else "failed") if self.root is not None else "not-created")
        self.emit("scan-content", "matched" if self.content_match else "clear")
        scan_complete = self.capture_complete and self.units == self.journals and self.units == self.stopped
        self.emit("scan-completeness", "complete" if scan_complete else "incomplete")
        require(complete and identities_ok and root_ok and scan_complete and not self.content_match, "cleanup")

    def main(self):
        try:
            self.action("preflight", self.preflight)
            self.action("provision", self.provision)
            if self.role == "a":
                source = self.root / "source"
                private_dir(source, self.identity)
                self.action("source", lambda: self.run_case("source", source))
                self.action("relay", lambda: self.export(source))
            else:
                self.receiver()
        except Exception:
            if self.first is None:
                self.first = ("operation", "guard")
        finally:
            try:
                self.cleanup()
            except Exception:
                self.emit("operation", "failed")
                if self.first is None:
                    self.first = ("operation", "cleanup")
            if self.first:
                stage, category = self.first
                print("CROSSVM_FIRST " + self.role + " " + stage + " " + category, flush=True)
        require(self.first is None, "evidence")
        self.emit("operation", "ok")


def rejection_stage(case):
    if case.startswith("binding-") or case.startswith("master-") or case in ("missing-master", "wrong-master", "swapped-master", "swapped-both"):
        return "binding"
    if case in ("missing-backup", "swapped-backup", "same-value", "backup-instance", "overlap-id"):
        return "provider"
    if case == "publish-parent": return "parent"
    if case.startswith("publish-"): return "publish"
    if case == "target-exists": return "target"
    return "package"


def credentials_for(case):
    master, backup = "master-good", "backup-good"
    if case == "foreign-activation": master = "foreign-master"
    if case == "missing-master": master = None
    if case == "missing-backup": backup = None
    if case in ("wrong-master", "swapped-master"): master = "master-" + case.split("-")[0]
    if case in ("wrong-backup", "swapped-backup"): backup = "backup-" + case.split("-")[0]
    if case == "swapped-both": master, backup = "master-swapped", "backup-swapped"
    if case.startswith("master-"): master = case
    if case.startswith("backup-"): backup = case
    if case == "same-value": backup = "backup-same"
    if case == "overlap-id": backup = "backup-overlap"
    return master, backup


def fingerprint(path):
    require(path.is_dir() and not path.is_symlink())
    values = {}
    for item in path.iterdir():
        require(item.name in {"cpa-cloud.db", "master-key-binding.json"})
        info = item.stat(follow_symlinks=False)
        values[item.name] = (info.st_dev, info.st_ino, info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode), info.st_nlink,
                             hashlib.sha256(read_regular(item, 256 << 20)).digest())
    require(set(values) == {"cpa-cloud.db", "master-key-binding.json"})
    return values


if __name__ == "__main__":
    try:
        if len(sys.argv) == 3 and sys.argv[1] == "cleanup-relay":
            remove_local_relay(sys.argv[2])
            print("CROSSVM_RELAY_LOCAL_CLEANUP ok", flush=True)
        elif len(sys.argv) == 5:
            Harness(*sys.argv[1:]).main()
        else:
            raise Failure("guard")
    except Exception:
        print("CROSSVM_FAILED", file=sys.stderr)
        sys.exit(1)
