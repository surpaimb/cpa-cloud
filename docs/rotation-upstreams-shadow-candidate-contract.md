# Synthetic upstreams shadow candidate

2026-10-20. Draft / real migration and production NO-GO. Exact parent PR95:
`84c165b543f00318aed1d8e7b466eef5cd1e4095`.

## Four-file boundary

Only this contract, `internal/service/upstream_rotation_shadow_candidate.go`,
its independently authored `_test.go`, and
[UPSTREAM_ROTATION_SHADOW_PROVENANCE](../internal/service/UPSTREAM_ROTATION_SHADOW_PROVENANCE.md)
are added. The kernel and tests belong to `package service` so that the kernel
can call the unchanged private `newSecrets`, `encrypt/decryptCredential`,
`encrypt/decryptGeminiAPIKey`, and `encrypt/decryptCodexAuth` implementations.
No old function is copied or exported. There is no App/store/startup/provider,
HTTP/CLI/config/route integration, product migration, dependency or workflow
change. PR95/94/93/92 and all existing source remain frozen.

The unexported capability has no production constructor. Only the new test
factory creates a temporary SQLite database and supplies its already-open
handle and a closure which can reopen that same owned candidate. There is no
kernel argument for a path, DSN, existing database, backup, system provider or
running root. Private visibility and the test factory limit this batch's
callers; they do not authenticate a malicious same-package caller or host.
The capability is single-use and serializes calls on itself.

## Input and supported rows

Inputs are a context, the test-owned capability and two different nonzero
32-byte synthetic in-memory roots called M1/M2. These names are harness labels,
not authenticated installation identities or epochs. No root is loaded from a
file, credential provider, configuration or a service process.

The candidate contains the current fourteen-column `main.upstreams` profile.
Columns are identified by name, never physical ordinal. The supported subset
has ordinary TEXT primary/unique keys, no SQL triggers, inbound references to
upstreams, or hidden/generated
columns, at most 256 rows, bounded UTF-8 text/timestamps and at most 8 MiB total
ciphertext. A test-owned unrelated table may coexist and must remain unchanged.
This is not a generic hostile-database reader or a replacement for PR92's
[sealed reader](sealed-upstreams-reader-contract.md). That reader's private
capability and symbolic-only result are not exported, called or modified.

All fourteen values and SQLite storage classes are read without filtering or
truncating LIMIT; row/identity/operation/category totals are cross-checked inside
the write transaction. Supported provider and encoding/AAD selection is frozen
by the [source map](upstreams-snapshot-manifest-sources.md):

| Provider | Live key_version | AAD prefix, followed by the unchanged object ID |
| --- | --- | --- |
| openai-compatible | 1 | `cpacloud/upstream-api-key/v1` + NUL |
| anthropic-api-key | 1 | same shared API prefix + NUL |
| gemini-api-key | 2 | `cpacloud/upstream-credential/v2` + NUL + `gemini-api-key` + NUL + `api-key` + NUL |
| codex-membership | 2 | `cpacloud/upstream-credential/v2` + NUL + `codex-membership` + NUL + `auth-json` + NUL |

The unchanged code derives the AES-256 key using HMAC-SHA256 with purpose
`cpacloud/upstream-credentials/v1`; ciphertext is 12-byte nonce followed by
GCM-sealed bytes, including its 16-byte tag. Live empty plaintext is outside
this bounded subset. Plaintext is otherwise opaque and preserved byte for
byte: there is no provider login, JSON credential validity, refresh or network
acceptance claim. OpenAI and Anthropic deliberately share AAD; changing between
those labels at the same ID is a limitation witness, not an AEAD rejection
claim. `key_version` remains encoding metadata, never an M2 generation counter.

Archived rows preserve non-NULL empty BLOB, key_version 0, disabled status,
archive time and all remaining facts. Archived Codex keeps reauth_required,
NULL verified_at and its operation identity. No archived row is decrypted,
reenabled, assigned new credentials or resurrected.

## Transaction and post-commit verification

The kernel owns one dedicated connection and one write transaction on the
private shadow database. It validates schema/rows/counts, decrypts every live
row under M1 using the original ID and provider family, and calls the existing
matching encrypt function under M2. New encoded output is length-checked,
authenticated again under M2 and compared to the original plaintext before
writing. A repeated candidate nonce across any provider refuses, since all
four providers share the same M-derived encryption key. Nonce checks cover
this candidate invocation, not all historical uses of a root.

Only live credential_ciphertext is updated. All other thirteen columns,
archived ciphertext and unrelated tables are unchanged. Every update must
affect exactly one original row. Any authentication, enumeration, validation,
write or cancellation failure yields a fixed code and nil, never partial
success. Pre-commit failures roll back the owned transaction; cleanup failures
cannot publish success and do not overwrite an earlier primary failure.

Commit is attempted at most once. Any returned commit error is always a fixed
uncertain result with nil and no retry: it does not prove rollback, including
when a test adapter really commits then returns a synthetic error. A successful
commit is followed by checked connection/database close and reopening through
the test-owned closure. A separate read-only verification transaction checks
the complete row set, all non-ciphertext facts, M2 plaintext equality, archived
tombstones and M1 rejection for every live row. No success result is returned
before verification and resource cleanup finish. Reopen or verification failure
does not undo or deny the earlier committed shadow data; it still returns nil.

The only success output is fixed synthetic-only classification and aggregate
counts. No plaintext, root, ID, endpoint, ciphertext, raw SQL/driver error,
production receipt or migration authorization is output. Transient byte
buffers are cleared where owned; Go strings and opaque cipher state mean this
is not a verified comprehensive memory-erasure claim.

## Fault boundaries and independent tests

Default execution calls the original real AES-GCM functions. Only the new
private capability's call boundaries may substitute fixed errors or controlled
encoded returns. Tests must label simulated random-source failure and duplicate
nonce returns as boundary/classification simulations. They do not change
crypto/rand.Reader, old security.go, global state, unsafe/linkname, dependencies
or workflows. Go 1.26.8 crypto/rand.Read does not return an error; an underlying
source failure terminates the process. Actual entropy-source failure and real
power loss are not injected or claimed here.

The new tests cover four providers/three AADs, enabled/disabled rows, all-provider
tombstones, fresh and differently ordered current schemas, empty sets, wrong
M1, equal/short/zero M2, wrong ID, cross-family AAD, nonce/tag/body tamper and
truncation, unknown provider/version, invalid states/storage classes, limits,
duplicate identities, actual late authentication failure after earlier writes,
actual SQLite write failure and zero-row update, enumeration/cancellation
failure, commit refusal and simulated post-commit error, candidate reopen and
post-commit verification failure. They independently reopen and compare
non-secret facts/unrelated tables and decrypt only public synthetic fixtures.
Diagnostics use fixed labels only; underlying values/errors are not printed.

Only new-test-name targeted local tests run serially (`-p=1 -parallel=1`), with
an optional existing-toolchain CGO race run. No parallel full Windows SQLite
suite, service/provider process or real 8787 endpoint is used. Natural Code
and inherited dual-VM results are recorded separately, with skipped jobs not
counted as executed tests. First unexpected natural red freezes source and
preserves read-only evidence; no patch/rerun/dispatch. Root arranges original
E/F formal review of the new exact remote head/base afterward.

## Remaining hard gates

This batch only generates and verifies a synthetic shadow candidate. It does
not collect or authenticate a real M1 source, custody/inventory, instance/root
mapping, source completeness, a backup-external revocation anchor or atomic
authorization consumption. It does not switch any root, migrate all consumers,
reissue Keys, protect old backups against authority resurrection, establish
production M/W custody, or prove disaster/power-loss recovery.

The existing driver pin embeds SQLite 3.50.3 in Windows/Linux amd64 generated
sources. SQLite's [WAL-reset advisory](https://sqlite.org/wal.html#the_wal_reset_bug)
identifies the rare multi-connection concurrent write/checkpoint corruption
condition in versions through 3.51.2, fixed in 3.51.3 with a 3.50.7 backport;
3.50.3 is not that fix. Successful private fixtures use rollback-journal DELETE
mode; an isolated WAL-mode negative control only demonstrates pre-transaction
refusal. There is no online writer or WAL acquisition. These tests do not resolve the pinned engine's WAL risks,
attest a safe online snapshot or authorize a dependency upgrade. See the
[source-binding contract](rotation-upstreams-source-binding-contract.md).
Draft / real migration and production NO-GO remains; no Ready/merge/tag/package/
deploy authorization follows from tests, coverage or inherited VM success.

## Public sources and licenses

Consulted 2026-10-20 for semantics, not copied implementation:

- [Go 1.26.8 AES](https://pkg.go.dev/crypto/aes@go1.26.8#NewCipher),
  [HMAC](https://pkg.go.dev/crypto/hmac@go1.26.8#New),
  [AEAD/GCM](https://pkg.go.dev/crypto/cipher@go1.26.8#AEAD) and
  [randomness](https://pkg.go.dev/crypto/rand@go1.26.8#Read).
- [Go transactions](https://go.dev/doc/database/execute-transactions) and
  [database/sql](https://pkg.go.dev/database/sql@go1.26.8): owned transaction,
  context, checked iteration/close and no result after failed commit.
- [SQLite transactions](https://sqlite.org/lang_transaction.html),
  [storage classes](https://sqlite.org/datatype3.html),
  [table metadata](https://sqlite.org/pragma.html#pragma_table_xinfo) and
  [WAL](https://sqlite.org/wal.html).
- [Pinned modernc driver](https://pkg.go.dev/modernc.org/sqlite@v1.38.2),
  BSD-3-Clause; existing dependencies/transitives retain their own licenses.

No new dependency, third-party code, reference/archived implementation, SDK,
license grant or strict clean-room claim is introduced.
