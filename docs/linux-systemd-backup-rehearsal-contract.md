# Synthetic systemd backup activation rehearsal

Status: independent development-preview acceptance candidate; production remains blocked.

This slice extends [ADR 0005](adr/0005-linux-systemd-backup-wrapping-key.md)
with a same-disposable-VM acceptance test, not a deployment or recovery command.
It introduces no production runtime code, migrations, API, dependency, or feature enablement.

## Required experiment

Ordinary pull-request CI builds a Linux test binary with the dedicated
`cpacloud_systemd_acceptance` tag and runs only `TestSystemdBackupActivation`.
The root harness requires a GitHub-hosted Linux VM with systemd PID 1, refuses
preexisting test users/groups/units, and provisions two fixed non-root users:
`cpa-backup-ci-source` and `cpa-backup-ci-restore`, with matching transient units.
They have distinct UIDs and private 0700 work directories. The second UID must
be denied access to the first UID's live database.

Each positive activation uses two genuine `LoadCredentialEncrypted=` properties
and the unchanged production credential readers. The harness feeds synthetic
JSON to `systemd-creds encrypt --with-key=host` via stdin, never plaintext files,
argv, environment, or logs. It does not override the readers or forge the
systemd mount/ACL. This is host-key encryption in one disposable VM, not TPM,
null-key policy attestation, production unit hardening, or off-host recovery.

The source unit initializes a synthetic database, opens the real service, stores
an encrypted synthetic record in an acceptance-only table, and creates/verifies
a v3 package. A post-startup binding change must fail before publishing a second
package. The root harness copies only the encrypted package and the nonsecret
binding into the second UID's private seed directory. This explicit fixture
transfer is not a user-facing recovery bootstrap feature. The second activation
authenticates that seed with the real installation credential, resolves the
independent backup credential, and restores into a new directory. Two actual
service Open/Close cycles verify the administrator row and decrypt the restored
synthetic record. The package contains only the database and binding; no
`master.key` is created. Target owner and 0700/0600 permissions are checked.

Negative activations require stage-specific failures: missing/wrong/swapped
installation credential (including swapping both roles), or seed binding identity/
canary tampering at binding authentication;
missing/swapped backup credential or equal installation/wrapping key bytes at
provider admission; wrong wrapping-key bytes at package verification and restore.
Every negative case requires the final restore target to remain absent.
Positive activations precede these cases so general activation failure is not
mistaken for successful rejection.

## Isolation and evidence boundaries

- `PrivateNetwork=yes` plus `RestrictAddressFamilies=AF_UNIX`; test checks a
  distinct network namespace and denial of IPv4/IPv6 socket creation. No listener
  is started, and port 8787, real providers and existing services are untouched.
  These assertions cover direct IP sockets, not all host IPC; AF_UNIX remains
  allowed and filesystem-path Unix sockets are not generally isolated here.
- Rehearsal configuration disables network workers and the backup coordinator.
  This exercises actual provider/binding/codec/service Open/Close, not autonomous
  scheduler dispatch, retention or the full management API.
- `ProtectSystem=strict`, `ProtectHome=yes`, `NoNewPrivileges=yes`, core dumps
  disabled, and bounded runtime. `ReadWritePaths` explicitly exempts the per-case
  work directory from the read-only mount policy. This does not establish that
  it is the only writable location: `ProtectSystem=strict` excludes `/dev`,
  `/proc` and `/sys`, and this harness does not establish that shared `/dev/shm`
  is unwritable or isolated. No whole-host filesystem or IPC isolation is claimed.
  `PrivateTmp` is intentionally absent because the test root is under `/var/tmp`;
  `TMPDIR` instead points to a private per-case writable directory.
- Stdout/stderr and the exact unit journals are screened for synthetic secrets
  (raw, hex and base64 key values and record/password sentinels). Raw output is
  never printed on failure. Only fixed test labels appear in CI; no credential,
  package, database, or log artifacts are uploaded.
- Cleanup stops the owned units before removing owned users and the validated
  temporary root. A still-active unit stops cleanup instead of removing its data.
  Host credential-key setup affects only the disposable VM; no existing host key
  is exported, replaced or removed.

Local cross-compilation/static checks are not Linux runtime evidence. Exact-head
GitHub CI and E/F review results belong in the Draft PR, separately from this
contract. No manual workflow dispatch, merge, release, deployment, second-machine
recovery, RTO/RPO, production enablement or true credentials are part of this slice.

## Provenance and public references

The tagged Go acceptance test, Python harness and its five nonprivileged tests are independently authored from
this project's contracts and public systemd documentation, consulted 2026-10-16:

- [systemd credentials](https://systemd.io/CREDENTIALS/)
- [systemd v255 execution documentation](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml)
- [systemd v255 transient-unit documentation](https://github.com/systemd/systemd/blob/v255/man/systemd-run.xml)

No prohibited reference code, tests, fixtures or documentation were used. There
is no new dependency: Python standard library, existing Go packages and the VM's
systemd tools retain their existing licenses; see [third-party notices](../THIRD_PARTY_NOTICES.md).
