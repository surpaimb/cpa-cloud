# Linux systemd installation master-key provider, first slice

Status: development-preview implementation contract, 2026-10-16.

This slice adds an explicit `linux-systemd-credential-v1` startup mode for a
single-instance Linux service. It is a startup custody gate, not a completed
production recovery design. Key rotation, portable recovery material, a second
machine rehearsal, TPM policy attestation, packaging, and production enablement
remain separate gates.

## Selection and compatibility

- `--master-key-provider=preview-file-v1` remains the default so existing
  preview data directories and tests retain their current `master.key` behavior.
- `--master-key-provider=linux-systemd-credential-v1` is explicit and supported
  only by Linux builds. It reads the fixed credential name
  `cpa-cloud-master-key` from the absolute directory named by systemd's
  `CREDENTIALS_DIRECTORY` interface.
- The credential bytes are never accepted from an argument, an environment
  variable value, stdin, the database, or `master.key`. The environment contains
  only the systemd-provided directory path.
- An explicitly selected systemd provider rejects any data directory that still
  contains `master.key`; it never falls back to that file.

The intended unit configuration uses `LoadCredentialEncrypted=` with a host,
TPM2, or combined policy selected by deployment. Plain `LoadCredential=`,
`SetCredential=`, and systemd null-key encryption are not production-approved.
After activation systemd intentionally exposes the decrypted credential through
the same runtime file interface, so this first application slice cannot attest
which unit directive or encryption policy produced it. Unit-file inspection and
real-host activation evidence are therefore still required before production.
This first slice assumes a fixed, dedicated, non-root, no-login
`User=cpa-cloud` service identity and does not support `DynamicUser=yes`.
For a fixed non-root user, systemd normally retains root ownership and grants
the service UID access with a POSIX ACL; that shape is admitted explicitly.

## Credential and binding

The credential is strict JSON, at most 4096 bytes, with no unknown or trailing
fields:

```json
{
  "format_version": 1,
  "provider_kind": "linux-systemd-credential-v1",
  "instance_id": "01234567-89ab-cdef-0123-456789abcdef",
  "key_id": "installation-root",
  "key_version": 1,
  "master_key": "BASE64-RAW-ENCODED-32-BYTES"
}
```

`instance_id` is a canonical UUID-shaped installation identity. `key_id` is a
1..64 byte ASCII identifier using letters, digits, `.`, `_`, or `-`.
`key_version` is in `1..9007199254740991`. The root key is exactly 32 bytes from
an OS CSPRNG; this code does not generate or rotate it in systemd mode.

Initialization persists only `master-key-binding.json` beside the database. The
0600, owner-only file contains the exact provider/instance/key/version metadata,
a random nonce, and an AES-256-GCM canary. It does not contain the root key. A
same-directory `.master-key-binding.json.staging` file may exist transiently or
after an interrupted first initialization.
The metadata is authenticated as AAD. A restart must match every metadata field
and decrypt the fixed canary before the root key may derive employee/session
digests, upstream-credential AEAD, or Responses state keys.

## Linux filesystem admission

The credential directory is opened with `O_DIRECTORY|O_NOFOLLOW`; the fixed
credential file is opened descriptor-relatively with `O_NOFOLLOW` and must be a
regular, single-link file. The provider accepts only the two layouts produced by
systemd's credential setup for a fixed non-root service user:

- On an ACL-capable credential filesystem, the directory is root:root 0550
  with exactly owner `r-x`, the effective service UID `r-x`, group none, mask
  `r-x`, and other none. The file is root:root 0440 with the corresponding
  exact read-only ACL. Extra named users/groups, write grants, malformed or
  duplicate entries, and a directory default ACL are rejected.
- When ACLs are unavailable and systemd uses its ownership fallback, the
  directory is service-UID/service-GID 0500 and the file is 0400 with no
  extended ACL. This layout is accepted only when `fstatfs` confirms that the
  credential filesystem is read-only, matching systemd's safety condition for
  the fallback.

The binding remains separate: it is read descriptor-relatively under an
effective-UID-owned 0700 application data directory and must be a regular,
single-link, effective-UID-owned 0600 file. Credential and binding device/inode,
size, mode, owner, and directory identity are rechecked after reading. The
credential access/default ACL bytes and read-only filesystem state are also
rechecked. Any mismatch fails closed. Running this provider as root is rejected.

## Startup ordering and failure behavior

Service startup validates the provider, credential, exact metadata, and AEAD
canary before opening SQLite. Therefore a missing, malformed, mismatched, linked,
symlinked, wrong-owner, or wrong-mode credential cannot run schema migration,
recovery, listeners, scheduled tests, backup workers, response workers, or model
workers. Errors use a fixed non-secret provider rejection and never include key
bytes or credential contents.

`--init` can create a new systemd-backed data directory while running inside an
appropriate systemd credential context. A missing final binding is accepted only
when the data directory is empty or contains only the fixed staging file; an
existing database, WAL/SHM/journal, or any other entry is rejected, so binding
loss cannot turn `--init` into a re-key operation. It refuses a legacy
`master.key`.

The binding is written to the fixed staging name, file-synced, revalidated, then
published with `renameat2(RENAME_NOREPLACE)` and followed by a directory fsync.
A staging file first has to pass the same no-follow, owner, mode, single-link,
size, and stable-identity admission as the final binding. Any admission or I/O
failure leaves it untouched and fails closed. Only safely read empty content or
a JSON object with an explicit end-of-input truncation can be discarded, and
only in an otherwise fresh directory. Other malformed JSON is retained and
rejected. A syntactically complete JSON value is never replaced: it is published
only when its strict schema, exact credential metadata, and canary validate, and
otherwise fails closed without mutation. A final binding encountered by `--init`
is revalidated and both file- and directory-synced again before SQLite is opened.
Empty or explicitly EOF-truncated staging is uncommitted scratch state: because
the directory is otherwise empty, initialization may discard it and restart with
the currently supplied credential. Once staging is syntactically complete, or a
final binding or any database state exists, a different key or metadata cannot
adopt that committed or existing installation.

## Tested and untested boundary

Automated tests cover strict parsing, wrong key/instance/key-id/version, canary
tampering, missing credential, unsafe modes, no `master.key` fallback, refusal to
rebind an existing database after binding loss, partial and complete staging
recovery, rejection of a complete staging object made by a different key,
fail-closed handling of wrong-mode, hardlinked, symlinked, and oversized staging,
directory-sync failure and retry, rejected startup leaving the data directory
unchanged, and a real CLI failure before listener/worker startup. Pure Linux
tests cover exact ACL parsing and both admitted ownership layouts.

Ordinary Linux CI also creates a disposable fixed system user and activates the
built CLI with a host-key-encrypted synthetic credential through
`LoadCredentialEncrypted=`. It records only stat/ACL metadata, verifies the
root-owned exact-ACL layout, initializes and reopens the data directory, rejects
wrong and missing credentials, and confirms that no `master.key` is created.
This CI VM evidence does not simulate a whole-machine power loss or establish a
production machine's host/TPM2 policy, null-key rejection, unit hardening, key
rotation, historical-version reads, recovery material, or second-machine
restore. SEC-01 remains incomplete until those gates are implemented and
rehearsed on the intended deployment host.

## Independent sources and licenses

The design was independently authored from CPA Cloud requirements and the
following public primary sources, reviewed 2026-10-16:

- systemd credentials overview and programming interface:
  <https://systemd.io/CREDENTIALS/>
- `systemd.exec` credential-directory and `LoadCredentialEncrypted=` behavior:
  <https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html>
- systemd's credential file and directory ACL/ownership fallback implementation:
  <https://github.com/systemd/systemd/blob/v258/src/core/exec-credential.c>
- `systemd-creds` security-state and encryption behavior:
  <https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html>
- Linux POSIX ACL xattr v2 wire format:
  <https://github.com/torvalds/linux/blob/master/include/uapi/linux/posix_acl_xattr.h>
- NIST SP 800-38D for AES-GCM:
  <https://csrc.nist.gov/pubs/sp/800/38/d/final>
- Go `crypto/aes`, `cipher.AEAD`, and `crypto/rand`:
  <https://pkg.go.dev/crypto>

The implementation adds no dependency. It uses the Go standard library and the
repository's existing `golang.org/x/sys` v0.36.0 dependency; its BSD-3-Clause
notice is already recorded in `THIRD_PARTY_NOTICES.md`.
