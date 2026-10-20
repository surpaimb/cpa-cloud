# Sealed synthetic SQLite upstreams reader

2026-10-20. Draft / real migration and production NO-GO. Sole parent PR91:
`e8be3fb8ca8d453e66206dd02d868c52e8e0c0d3`, based on PR90
`dbe85f714628792f9a14254912c4a3deaf8c0017`.

## Exact scope

At most five new files: this contract; `reader.go`, `schema.go`, `reader_test.go`
and `PROVENANCE.md` in `internal/upstreamfixturereader/`. The only existing-file
edit is the approved decrypt-side AAD locator correction in
[PR91's source map](upstreams-snapshot-manifest-sources.md), from 137/161/185
to 134/158/182. Everything else from PR90/PR91, product/schema/migrations,
service/CLI/provider/startup, dependencies, workflows and pins is frozen.

The new package has **no exported reader, path constructor, config binding or
production caller**. Its unexported capability is constructed only by tests
which create a private temporary directory and synthetic SQLite database,
finish all writes, close the writer, then record the file identity/size/time
and digest. No real database, backup, credential, user config or main WIP is
an input. The capability's visibility and controlled test factory are scope
controls, not authentication against a hostile same-process caller or host.

Only an existing, regular `fixture.sqlite` in that private directory is read.
Symlink/canonical-path or identity mismatch, unexpected directory entries,
WAL/SHM/journal companions, oversized/changed files and WAL-format headers
refuse before SQL; the seal and absence of companions are checked again after
all SQL resources close. These observations and a digest detect changes in
this controlled test lifecycle, **not** M1 identity, origin, atomic protection
against an adversarial path replacement, online writers or a production seal.
Online WAL/concurrent-writer snapshot acquisition is a separate future batch.

## Read-only lifecycle and same-transaction evidence

The pinned existing driver is `modernc.org/sqlite v1.38.2` (SQLite 3.50.1).
The code constructs its own escaped file URI with `mode=ro&cache=private`;
there is no writable fallback, caller DSN, `immutable=1`, `nolock` or custom
VFS. One dedicated `database/sql.Conn` holds one explicit read transaction.
`query_only` and `trusted_schema` are defense-in-depth, not the read-only or
origin proof. `TxOptions.ReadOnly` alone is not a read-only filesystem policy.

Within that same transaction, the reader checks ordinary `main.upstreams`,
the complete fixture schema object set, actual table metadata, all fourteen
column definitions/storage classes, exact CHECK/NULL/PK/UNIQUE constraints,
actual unique index origins/keys/collations, integrity and unfiltered row and
category counts. Column identity is by name, not physical ordinal. Views,
virtual/shadow tables, generated/hidden/extra/missing columns, extra objects,
unknown constraint/layout/index variants and malformed data refuse.

The supported fixture database contains just this one ordinary table and its
two SQLite-owned unique indexes. It is not a full product database reader.
DDL is parsed only as a strict frozen-profile token/column/check structure,
not evaluated as arbitrary SQL or logged. Actual schema metadata is cross-
checked; textual DDL, schema_version, data_version, a hash or query_only alone
are not accepted as sufficient evidence.

All fourteen columns are selected without a WHERE filter or truncating LIMIT.
Actual SQLite `typeof` and bounded raw values are checked before mapping to
PR91's public-symbol `upstreammanifest`. Index/identity/category expectations
come from actual queries in the transaction, not caller row lists or counts.
More than 256 rows or an 8 MiB fixture refuses; these are test-scope limits,
not production size limits. Raw IDs, URLs, ciphertext, DDL, paths and driver
errors stay in bounded process memory and are never diagnostic output.

Four providers, three live AAD descriptors, disabled rows, archived empty BLOB/
version-0 tombstones and NULL distinctions retain the existing mapper's policy.
Generation numbers remain **synthetic plan labels**, not read from key_version
or authenticated as M1/M2. Mapping does not decrypt/re-encrypt ciphertext,
authenticate a real root, dispatch work or enable a disabled/archived row.

Rows iteration, Rows.Err/Close, transaction rollback, connection return and
database close are checked. No manifest is returned until cleanup and post-
seal validation complete. Any primary or cleanup failure yields a fixed code
and nil result; primary failure is retained rather than overwritten by cleanup.
Cancellation also refuses, including a final context check. No underlying
error, input value or SQL text is interpolated. There is no test-injection
callback/driver selector in the reader.

## Public verification and evidence distinctions

Tests independently create fresh and rebuild-plus-archive-column schemas,
write only public synthetic rows and close writers before reading. They cover
four providers/three AAD forms, disabled/archived/empty data, metadata and storage
class defects, unknown/view/virtual/hidden/extra layouts, uniqueness/NULL/
tombstone failures, limits, corrupt input, cancellation and seal/companion
refusal. Diagnostics use only fixed labels, never raw fixture contents/errors.

Separate fixed-driver probes demonstrate `mode=ro` rejecting a write with
query_only disabled, refusing absent paths without creating files, and actual
SQLite execution/finalize or rollback errors used by the cleanup result logic.
These are **distinct from** an injected close failure in the reader's unfiltered
SELECT path. No production callback is added and no new disk I/O-fault/crash
injection is claimed. SQLite `close_v2` may defer destruction; a final successful
DB.Close alone is not treated as proof that earlier rows/transactions closed.

This closes only controlled sealed-fixture schema/enumeration evidence, not
trusted M1 provenance, real production completeness, any consumer family's
end-to-end acceptance, Key reissue, cross-table accounting/transactions,
backup-external revocation, crash/disaster recovery or production approval.
All prior remaining blockers remain. No service/helper/guest/8787 is started.

Only new-package targeted tests/vet/build and docs/diff checks run locally;
no Windows SQLite whole-repository parallel suite. One commit/push/new Draft
stacked PR, exact natural head/base attempt1 evidence. First unexpected natural
red freezes source for read-only diagnosis: preserve it, no patch/rerun/dispatch
or workflow/runner switch. Root arranges original E/F exact-head review afterward.
No Ready/merge/tag/package/deploy or subsequent implementation authorization.

## Sources, driver behavior and licenses

Consulted 2026-10-20; semantic/source verification only, not copied code:

- [SQLite URI](https://sqlite.org/uri.html): read-only URI mode and why immutable
  would bypass locking/change detection rather than establish a seal.
- [SQLite pragmas](https://sqlite.org/pragma.html): table_list/table_xinfo,
  index_list/index_xinfo, integrity and actual storage/schema observations.
- [SQLite finalize](https://sqlite.org/c3ref/finalize.html) and
  [close](https://sqlite.org/c3ref/close.html): statement errors may also be
  returned on finalization; close_v2 can defer destruction with live objects.
- [Pinned driver](https://pkg.go.dev/modernc.org/sqlite@v1.38.2): local pinned
  sqlite.go 207-212 (Rows.Close/finalize), 730-740 (ReadOnly only affects begin
  mode), 826-850 (file URI/open flags), 1282-1287 (finalize), 1455-1489 (close_v2).
- [Go database/sql](https://pkg.go.dev/database/sql): transaction connection
  binding, cancellation, Rows.Err/Close and Conn.Close returning to the pool.
- Project static sources and columns/AAD provenance: the unchanged
  [PR91 contract](upstreams-snapshot-manifest-contract.md) and corrected
  [source map](upstreams-snapshot-manifest-sources.md).

No new dependency/version/pin. modernc.org/sqlite remains BSD-3-Clause and
existing dependency licenses/NOTICES remain in force. New independent origin
and non-claims: [PROVENANCE](../internal/upstreamfixturereader/PROVENANCE.md).
