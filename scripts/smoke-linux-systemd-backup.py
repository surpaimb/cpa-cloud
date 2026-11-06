#!/usr/bin/env python3
"""Independent synthetic same-VM acceptance; see the rehearsal contract.

Run only as root on a disposable GitHub-hosted Linux VM. No production key,
data directory, listening service, plaintext credential file, or artifact upload.
"""

import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


MASTER = "cpa-cloud-master-key"
BACKUP = "cpa-cloud-backup-wrapping-key"
USERS = ("cpa-backup-ci-source", "cpa-backup-ci-restore")
UNITS = tuple(user + ".service" for user in USERS)
INSTANCE = "01234567-89ab-cdef-0123-456789abcdef"
KEYS = tuple(bytes(range(start, start + 32)) for start in (1, 65, 129, 193))
SENTINELS = (b"synthetic-systemd-admin-only", b"synthetic-systemd-backup-record-only")


def clean_log(data):
    """Never print a match (even on failure); cover common key encodings."""
    needles = list(SENTINELS)
    for key in KEYS:
        needles.extend((key, key.hex().encode(), key.hex().upper().encode(),
                        base64.b64encode(key), base64.b64encode(key).rstrip(b"="),
                        base64.urlsafe_b64encode(key).rstrip(b"=")))
    if any(needle in data for needle in needles):
        raise RuntimeError("secret-log-rejected")


def command(args, *, payload=None, check=True, timeout=30):
    result = subprocess.run(args, input=payload, stdout=subprocess.PIPE,
                            stderr=subprocess.STDOUT, timeout=timeout, check=False)
    clean_log(result.stdout)
    if check and result.returncode != 0:
        raise RuntimeError("command-failed")
    return result


def fixtures():
    def master(key):
        return dict(format_version=1, provider_kind="linux-systemd-credential-v1",
                    instance_id=INSTANCE, key_id="installation-root", key_version=1,
                    master_key=base64.b64encode(key).decode().rstrip("="))

    def backup(key):
        return dict(format_version=1, provider_kind="linux-systemd-credential-v1",
                    instance_id=INSTANCE, provider_id="bkp_synthetic", key_id="backup-root",
                    key_version=1, wrapping_key=base64.b64encode(key).decode().rstrip("="))

    return {
        "master-good": (MASTER, master(KEYS[0])),
        "master-wrong": (MASTER, master(KEYS[2])),
        "master-swapped": (MASTER, backup(KEYS[1])),
        "backup-good": (BACKUP, backup(KEYS[1])),
        "backup-wrong": (BACKUP, backup(KEYS[3])),
        "backup-swapped": (BACKUP, master(KEYS[0])),
        "backup-same": (BACKUP, backup(KEYS[0])),
    }


def private_directory(path, identity):
    path.mkdir(mode=0o700)
    os.chown(path, *identity)


def copy_private(source, target, identity):
    # Only encrypted package bytes and nonsecret binding bytes cross UIDs.
    with source.open("rb") as reader, target.open("xb") as writer:
        shutil.copyfileobj(reader, writer)
    os.chmod(target, 0o600)
    os.chown(target, *identity)


def preflight():
    if (sys.platform != "linux" or os.geteuid() != 0
            or os.environ.get("GITHUB_ACTIONS") != "true"
            or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted"
            or os.environ.get("RUNNER_OS") != "Linux"
            or Path("/proc/1/comm").read_text().strip() != "systemd"):
        raise RuntimeError("disposable-vm-required")
    command(["systemd-detect-virt", "--vm"])
    import pwd
    import grp
    for user, unit in zip(USERS, UNITS):
        for lookup in (pwd.getpwnam, grp.getgrnam):
            try:
                lookup(user)
            except KeyError:
                pass
            else:
                raise RuntimeError("preexisting-identity-rejected")
        state = command(["systemctl", "show", unit, "--property=LoadState", "--value"], check=False)
        if state.stdout.strip() != b"not-found":
            raise RuntimeError("preexisting-unit-rejected")


def main(binary):
    preflight()
    import pwd
    binary = Path(binary).resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise RuntimeError("test-binary-required")
    os.umask(0o077)
    root = Path(tempfile.mkdtemp(prefix="cpa-cloud-systemd-backup.", dir="/var/tmp"))
    root.chmod(0o711)
    created_users, started_units = [], set()
    active_case = "setup"
    try:
        for user in USERS:
            command(["useradd", "--system", "--user-group", "--no-create-home",
                     "--shell", "/usr/sbin/nologin", user])
            created_users.append(user)
        identities = [(pwd.getpwnam(user).pw_uid, pwd.getpwnam(user).pw_gid) for user in USERS]
        if identities[0][0] == identities[1][0] or any(uid == 0 for uid, _ in identities):
            raise RuntimeError("distinct-nonroot-identities-required")
        executable = root / "acceptance.test"
        shutil.copyfile(binary, executable)
        executable.chmod(0o555)
        encrypted = root / "credentials"
        encrypted.mkdir(mode=0o700)
        command(["systemd-creds", "setup"])
        for name, (credential_name, document) in fixtures().items():
            # The only plaintext transport is an anonymous stdin pipe to the
            # real encryption tool; argv/env/files/logs contain no key values.
            command(["systemd-creds", "encrypt", "--with-key=host",
                     "--name=" + credential_name, "-", str(encrypted / name)],
                    payload=json.dumps(document).encode())
        host_netns = os.readlink("/proc/self/ns/net")
        source = root / "source"
        private_directory(source, identities[0])

        def run_case(case, work, role, master, backup):
            nonlocal active_case
            active_case = case
            unit, user = UNITS[role], USERS[role]
            state = command(["systemctl", "show", unit, "--property=LoadState", "--value"], check=False)
            if state.stdout.strip() != b"not-found":
                raise RuntimeError("unit-not-collected")
            private_directory(work / "tmp", identities[role])
            properties = ["User=" + user, "Group=" + user, "UMask=0077",
                          "NoNewPrivileges=yes", "PrivateNetwork=yes", "ProtectHome=yes",
                          "ProtectSystem=strict", "ReadWritePaths=" + str(work),
                          "RestrictAddressFamilies=AF_UNIX", "LimitCORE=0",
                          "RuntimeMaxSec=120", "TimeoutStopSec=10", "KillMode=control-group",
                          "WorkingDirectory=" + str(work)]
            # PrivateTmp is intentionally not enabled: work is in /var/tmp.
            # ReadWritePaths exempts this work directory from ProtectSystem's
            # read-only policy, not from a whole-host write/IPC sandbox. /dev,
            # /proc and /sys are excluded; shared /dev/shm is not isolated here.
            for name, variant in ((MASTER, master), (BACKUP, backup)):
                if variant:
                    properties.append("LoadCredentialEncrypted=" + name + ":" + str(encrypted / variant))
            environment = {
                "CPA_ACCEPTANCE_CASE": case, "CPA_ACCEPTANCE_ROOT": str(work),
                "CPA_ACCEPTANCE_UID": str(identities[role][0]),
                "CPA_ACCEPTANCE_HOST_NETNS": host_netns,
                "CPA_ACCEPTANCE_SOURCE_DB": str(source / "data" / "cpa-cloud.db"),
                "TMPDIR": str(work / "tmp"),
            }
            args = ["systemd-run", "--wait", "--collect", "--quiet", "--pipe", "--unit=" + unit]
            args += ["--property=" + item for item in properties]
            args += ["--setenv=" + key + "=" + value for key, value in environment.items()]
            args += [str(executable), "-test.run=^TestSystemdBackupActivation$", "-test.count=1",
                     "-test.v", "-test.timeout=90s"]
            started_units.add(unit)
            result = command(args, check=False, timeout=150)
            # --pipe captures service stdout/stderr; also check manager journal
            # messages for these exact transient unit names, without printing it.
            command(["journalctl", "--boot", "--unit=" + unit, "--no-pager", "--output=cat"])
            marker = ("SOURCE_V3_AND_BINDING_REJECTION_OK" if case == "source" else
                      "RESTORE_OPEN_CLOSE_OK" if case == "restore" else "EXPECTED_REJECTION " + case)
            if result.returncode != 0 or marker.encode() not in result.stdout or b"--- PASS: TestSystemdBackupActivation" not in result.stdout:
                # Only fixed independently authored diagnostic labels may leave
                # this harness. Never dump command output or exception details.
                for label in (b"source initialization failed", b"source service open failed",
                              b"source material failed at binding", b"source material failed at provider",
                              b"source backup failed", b"source verify failed", b"restore failed",
                              b"restored service open failed", b"restored service state or close failed",
                              b"unexpected preparation failure at binding", b"unexpected preparation failure at provider",
                              b"unexpected file type or permissions", b"unexpected socket rejection"):
                    if label in result.stdout:
                        print("diagnostic: " + label.decode(), flush=True)
                raise RuntimeError("acceptance-case-failed")
            print("PASS: " + case, flush=True)

        run_case("source", source, 0, "master-good", "backup-good")
        cases = (
            ("restore", "master-good", "backup-good"),
            ("missing-master", None, "backup-good"),
            ("wrong-master", "master-wrong", "backup-good"),
            ("swapped-master", "master-swapped", "backup-good"),
            ("swapped-both", "master-swapped", "backup-swapped"),
            ("missing-backup", "master-good", None),
            ("wrong-backup", "master-good", "backup-wrong"),
            ("swapped-backup", "master-good", "backup-swapped"),
            ("same-value", "master-good", "backup-same"),
            ("tampered-binding", "master-good", "backup-good"),
            ("tampered-canary", "master-good", "backup-good"),
        )
        for case, master, backup in cases:
            work = root / case
            private_directory(work, identities[1])
            private_directory(work / "seed", identities[1])
            copy_private(source / "output.cpacb", work / "input.cpacb", identities[1])
            binding = work / "seed" / "master-key-binding.json"
            copy_private(source / "data" / binding.name, binding, identities[1])
            if case in ("tampered-binding", "tampered-canary"):
                document = json.loads(binding.read_bytes())
                if case == "tampered-binding":
                    document["key_id"] = "installation-evil"
                else:
                    encoded = document["canary_ciphertext"]
                    document["canary_ciphertext"] = ("A" if encoded[0] != "A" else "B") + encoded[1:]
                binding.write_text(json.dumps(document), encoding="utf-8")
            run_case(case, work, 1, master, backup)
            if case != "restore" and os.path.lexists(work / "restored"):
                raise RuntimeError("negative-target-published")
        print("PASS: same-VM dual-credential different-UID v3 acceptance; logs screened", flush=True)
    except Exception:
        print("FAIL: case=" + active_case + "; raw diagnostics withheld", flush=True)
        raise
    finally:
        # Stop only names checked absent before this run and started by us.
        # Never remove users/data while a test unit may still be running.
        for unit in started_units:
            command(["systemctl", "stop", unit], check=False)
            state = command(["systemctl", "show", unit, "--property=ActiveState", "--value"], check=False)
            if state.stdout.strip() not in (b"inactive", b"failed"):
                raise RuntimeError("cleanup-unit-still-active")
        for user in reversed(created_users):
            command(["userdel", user])
        resolved = root.resolve(strict=True)
        if resolved != root or resolved.parent != Path("/var/tmp") or not resolved.name.startswith("cpa-cloud-systemd-backup.") or root.is_symlink():
            raise RuntimeError("cleanup-root-rejected")
        shutil.rmtree(resolved)


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise RuntimeError("test-binary-required")
        main(sys.argv[1])
    except Exception:
        print("FAIL: synthetic systemd acceptance (no raw logs emitted)", file=sys.stderr)
        sys.exit(1)
