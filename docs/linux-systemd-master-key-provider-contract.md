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
This first slice assumes a fixed, dedicated, no-login `User=cpa-cloud` service
identity and does not support `DynamicUser=yes`: dynamic-user credentials may be
root-owned 0440 files with a service-user ACL instead of owner-0400 files.

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

The credential directory is opened with `O_DIRECTORY|O_NOFOLLOW` and must be
owned by the effective service UID with mode 0700. The fixed credential file is
opened descriptor-relatively with `O_NOFOLLOW`; it must be a regular, single-link
file owned by the same UID with mode 0400. The binding is read through an
equivalent descriptor-relative path under an owner-0700 data directory and must
be a regular, single-link owner-0600 file. Device/inode, size, mode, owner, and
directory identity are rechecked after reading. Any mismatch fails closed.

This slice does not inspect POSIX ACL extended attributes. The real-host gate
must verify that the fixed service UID is the only identity granted credential
and data-directory access. This is deliberately not inferred from a synthetic
filesystem, and it avoids rejecting systemd credential mounts whose filesystem
does not implement the relevant xattr queries.

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
directory-sync failure and retry, read-only startup failure, and a real CLI
failure before listener/worker startup. Linux tests use only synthetic temporary
roots and credentials; they do not simulate a whole-machine power loss.

This slice does not prove a real `LoadCredentialEncrypted=` activation, host or
TPM2 binding, null-key rejection, fixed service UID isolation, ACL behavior, key
rotation, historical-version reads, recovery material, second-machine restore,
or production systemd hardening. SEC-01 remains incomplete until those gates are
implemented and rehearsed.

## Independent sources and licenses

The design was independently authored from CPA Cloud requirements and the
following public primary sources, reviewed 2026-10-16:

- systemd credentials overview and programming interface:
  <https://systemd.io/CREDENTIALS/>
- `systemd.exec` credential-directory and `LoadCredentialEncrypted=` behavior:
  <https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html>
- `systemd-creds` security-state and encryption behavior:
  <https://www.freedesktop.org/software/systemd/man/latest/systemd-creds.html>
- NIST SP 800-38D for AES-GCM:
  <https://csrc.nist.gov/pubs/sp/800/38/d/final>
- Go `crypto/aes`, `cipher.AEAD`, and `crypto/rand`:
  <https://pkg.go.dev/crypto>

The implementation adds no dependency. It uses the Go standard library and the
repository's existing `golang.org/x/sys` v0.36.0 dependency; its BSD-3-Clause
notice is already recorded in `THIRD_PARTY_NOTICES.md`.
