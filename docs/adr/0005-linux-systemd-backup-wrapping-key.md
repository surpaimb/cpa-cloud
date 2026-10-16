# ADR 0005: Linux systemd backup wrapping-key prerequisite

Status: development-preview candidate; production enablement remains blocked

Date: 2026-10-16

## Decision

Linux headless automated backup uses a second systemd credential named
`cpa-cloud-backup-wrapping-key`. It is independent from the installation
credential `cpa-cloud-master-key`; neither secret is accepted in argv, a
plaintext environment variable, the database, logs, or the provider-store
directory. Desktop Secret Service is not a server root of trust.

The backup credential is strict JSON, limited to 4096 bytes:

```json
{
  "format_version": 1,
  "provider_kind": "linux-systemd-credential-v1",
  "provider_id": "bkp_example",
  "instance_id": "01234567-89ab-cdef-0123-456789abcdef",
  "key_id": "backup-wrapping-key",
  "key_version": 1,
  "wrapping_key": "BASE64_RAW_32_BYTES"
}
```

The service first authenticates the installation credential against
`master-key-binding.json`. The backup provider then requires the same
lower-cased instance UUID, a different key ID, and a wrapping key whose
SHA-256 digest differs in constant time from the installation key digest.
The systemd credential reader retains the fixed-user `LoadCredentialEncrypted=`
rules from ADR 0003: non-root execution, no-follow descriptor-relative open,
bounded regular file, exact root-owned ACL or independently read-only
service-owned fallback, same device and `STATX_MNT_ID`, and before/after
identity, ACL, mount and size checks.

The externally provisioned Linux provider is read-only. The management API
may bind its authenticated non-secret reference into the database, but may
not generate, rotate, activate, export, or delete its key. Rotation and a
multi-version recovery window require a later contract.

## Linux backup format v3

Windows DPAPI automated packages remain v2 byte-for-byte. Linux systemd
credentials use authenticated format v3. Its header binds provider ID, kind,
instance ID, key ID, and key version as AES-GCM AAD and HKDF-SHA-256 context.
The encrypted metadata repeats the exact reference. Wrong, missing, changed,
or mixed identity/key material therefore fails before a final restore target
is created.

The two fixed records are:

1. `cpa-cloud.db`
2. `master-key-binding.json`

`master.key` is rejected on this path and is never copied into the package.
Create validates the authenticated installation binding before the SQLite
snapshot and rechecks its bytes afterward. Verify/restore authenticate the v3
header and ciphertext, require the same reference in encrypted metadata, then
validate the restored binding. Restore publishes only a new directory; it
retains the existing session/OAuth sanitization and zero-network rehearsal.

Durable provider/run metadata freezes the provider kind, provider ID,
instance ID, key ID and version. This is recoverability evidence for the exact
credential required by a retained package; it is not evidence that an
off-host copy, another service account, or another machine can recover it.

## Failure and rollout boundary

- Missing/malformed systemd credential, installation binding failure,
  mismatched instance/key ID/version, equal installation and backup key bytes,
  or changed credential makes the provider unavailable. There is no fallback.
- Existing Windows rows migrate with null instance/key fields; schema changes
  are transactional and exact legacy DDL is required.
- The feature remains behind the existing default-off automated-backup switch.
- This slice does not provide external rotation, old-version retention after
  credential replacement, Linux key export/import, second-machine restore,
  object storage, RTO/RPO, packaging, deployment, or production enablement.

## Verification and sources

Tests use only fixed synthetic credentials and databases. Required evidence is
v3 create/verify/restore, wrong/missing/mixed binding rejection without a
published target, legacy migration, Linux compilation and credential-reader
tests, ordinary CI, and a synthetic fixed-user systemd activation smoke when
available. Results must be recorded separately from planned work.

Public sources consulted 2026-10-16; no prohibited reference implementation
was used:

- systemd credentials: <https://systemd.io/CREDENTIALS/>
- `LoadCredentialEncrypted=`: <https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html#LoadCredentialEncrypted=>
- Linux `statx(2)`: <https://man7.org/linux/man-pages/man2/statx.2.html>
- Linux POSIX ACL: <https://man7.org/linux/man-pages/man5/acl.5.html>
- RFC 5869 HKDF: <https://www.rfc-editor.org/rfc/rfc5869>
- NIST SP 800-38D AES-GCM: <https://csrc.nist.gov/pubs/sp/800/38/d/final>

No dependency was added. Go standard-library crypto is used with the existing
`golang.org/x/crypto` and `golang.org/x/sys` dependencies and their already
recorded BSD licenses.
