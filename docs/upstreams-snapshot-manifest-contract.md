# Current-upstreams symbolic manifest: bounded contract

2026-10-20. Draft / production NO-GO. Sole parent is PR90
`dbe85f714628792f9a14254912c4a3deaf8c0017`, based on PR86
`6056270c612bdc3741dfee5a6c0c338cf9926b0c`. This is a new, separate commit/PR,
not an amendment of PR90 or its accepted symbolic model.

## Scope and frozen boundary

Exactly five added files:

- `docs/upstreams-snapshot-manifest-contract.md`
- `docs/upstreams-snapshot-manifest-sources.md`
- `internal/upstreammanifest/manifest.go`
- `internal/upstreammanifest/manifest_test.go`
- `internal/upstreammanifest/PROVENANCE.md`

PR90's five files and every existing product source, schema/migration, workflow,
pin, dependency, provider, CLI, configuration and startup file remain unchanged.
No production package imports the new package. There is no database reader,
SQLite dependency, file/network/environment/clock access, cryptography, secret
bytes, service/helper/guest, worker, SQL executor, migration commit or activation
API. Compiler/test tooling reads source; the model itself performs no I/O.

This slice covers **only the current, already-migrated `upstreams` table**:
14 named columns, four provider kinds, three live credential AAD families and
the existing archived/empty-credential state. The archived state is not a fourth
encryption/AAD family. Source details and fixed blobs are in the
[source map](upstreams-snapshot-manifest-sources.md).

One table is not one PR90 consumer family. `upstream-api`, `gemini-api` and
`codex-auth` are row-classification references to the
[frozen inventory](master-key-rotation-consumers.md), not three accepted
end-to-end families. No 1-of-53 or 3-of-53 completion claim is made. All other
consumer obligations, table-external behavior of these three labels, external
cursor/quote state, backup/file carriers, Responses and employee Key retirement
and reissue remain blocked. The approved PR90 symbolic model is not imported,
changed or used as an actual migration authorizer.

Employee Key reissue eligibility requires its own later contract: disabled or
departed employees, historical revocation/expiry, pending/armed slots, delivery
and no privilege expansion. This package neither issues Keys nor makes those
authorization decisions. A manifest does not enable an upstream or a feature.

## Inputs and exact synthetic mapping

`Snapshot` identifies the fixed source commit, table and supported profile,
contains the fourteen-column name set, declared row count and public fixture
rows. Historical/pre-migration/unknown profiles or extra tables are refused.
Column **order** is not identity: fresh creation and legacy rebuild/add-column
paths can have different physical order. Names must occur exactly once in the
header and in every row, including explicit NULLs. Omitting a nullable column
is not equivalent to supplying NULL. Empty tables still require the entire
header and all four provider categories with explicit zero counts.

`Value` is a strict tagged union. IDs/names/endpoints/timestamps/ciphertext are
nonzero integer `Symbol`s, not real bytes, paths, configuration or identifiers.
Only the fixed public provider/state enum names are strings. NULL and empty
BLOB are distinct; hidden payload in another tag's fields is refused. Symbols
do not establish UUID syntax, URL safety, time parsing, plaintext validity,
nonce length/uniqueness, AEAD success or actual SQLite storage classes.

`Enumeration` is a **separately constructed public fixture index** of expected
object IDs/providers and four exact category counts. The mapper checks:

1. Exact scope/profile and fourteen-column sets, bounded count (at most 256
   fixture rows; not a product or database size limit).
2. Declared count equals the snapshot row count and independent index size;
   index IDs, row IDs and non-NULL operation IDs are unique. Each row matches
   exactly one expected object/provider; missing/extra/duplicate rows refuse.
3. Count categories are known, unique, complete and equal the index counts;
   row/index matching then gives the same actual synthetic counts.
4. Strict symbolic cell shapes and supported current application-state subset:
   provider, NULL relationships, boolean 0/1 flags, revision 1..2^53-1, live
   version/credential kind, and archived empty-BLOB/version-0/disabled state.
5. Each live provider has the exact source AAD prefix plus the same object-ID
   symbol, and its fixed format/version. Old/unknown/mismatched formats refuse.
6. Source generation is nonzero, the planned target is exactly source+1 without
   overflow, and every row has the same fixture source generation. These tags
   are **external synthetic metadata**, absent from the fourteen source columns;
   `key_version` is never interpreted as an M epoch.
7. Live/disabled unarchived rows require `PlanReencrypt`; archived rows require
   `KeepArchivedFacts` and no AAD. Unresolved or contradictory decisions refuse.

The revision and symbolic shape limits deliberately define a conservative
supported fixture subset; they are not a claim that the SQL DDL rejects every
other byte representation. No endpoint/auth-JSON/timestamp or generic SQL
parser exists. No real row is accepted or rejected by this offline package.

`Build` returns either a complete plan **relative to these supplied synthetic
inputs**, or a fixed finite error code and nil (never partial) output. It has
no mutable shared state. Output fields are copied into fixed catalog order;
entries follow the independently supplied index order. No input/output alias
can mutate another plan. No source value is silently replaced.

## Planned actions, not completed rotation

All fourteen original facts are recorded as `SourceValue`:

- Live `credential_ciphertext`: **plan** authenticated old decode and fresh
  independent-M2 encryption with the same AAD/object binding in a future batch.
  This mapper produces no new ciphertext, crypto proof, root or key material.
- `key_version`: preserve its **credential encoding** version (1, 2 or archived
  0), not invent an M-generation column or increment it for a root rotation.
- Other fields: preserve facts, including disabled/archived state, original
  object ID, revision, operation ID and creation/verification/archive symbols.
- Archived empty credentials: preserve tombstone facts only. Never reconstruct
  credentials, enable the row or turn version 0 into a live credential.

The source/target generation labels describe the requested plan, not an
authenticated root change. `PlanM2Ciphertext` is not a successful encryption
receipt. Actual root independence, key derivation, authenticated decrypt/reseal,
nonce handling, authorization and transaction/revision policy remain unproved.

## Completeness and production stop gates

There is **no caller-supplied completeness boolean**, but neither an index nor
count agreement attests a real database. Both are public fixture inputs. The
tests explicitly show that coherently omitting a row from BOTH inputs can
still produce an internally consistent smaller plan. No claim of real schema,
row enumeration or physical snapshot completeness follows from acceptance.

Not provided: reading schema/rows from a consistent real snapshot, schema
fingerprint attestation, historical-format parsing, decryption, cross-table FK
and operation/funding reconciliation, independent M2 generation, transaction or
filesystem commit, crash recovery, physical erasure/WAL cleanup, authoritative
backup-external revocation, restore or runtime activation. All remain blockers.
No true DB, credentials, backup, primary WIP, reference sibling or production
endpoint is read. Existing recovery VM CI is inherited regression evidence,
not application-root migration or new fault-injection acceptance.

## Verification and delivery

Public deterministic tests independently enumerate fourteen columns and the
four providers/three AAD forms. Positive cases include live, disabled, archived,
known-empty table/categories and reordered input; negatives cover scope,
column/row/index/count omissions, duplicates/extras, wrong tags, mixed/unknown
generations, version/provider/AAD disagreement, unresolved decisions and
archive resurrection. Alias checks and the completeness-limitation witness
remain separate from any operational evidence.

Run only this new package's tests/vet/build, formatting and docs/diff checks
locally. No Windows SQLite whole-repository parallel suite or local service.
One commit/push, new Draft PR stacked directly on exact PR90. Record natural
same-head/base `pull_request` attempt1 CI separately from local tests. First
unexpected natural red freezes source for read-only diagnosis: preserve it;
no patch, rerun, dispatch or workflow/runner switch. Original E/F exact-new-head
review is still required. No Ready/merge/tag/package/deploy or production GO.

Independent source and license record: [PROVENANCE](../internal/upstreammanifest/PROVENANCE.md).
