# Application master-key rotation: symbolic model only

2026-10-20. Draft / production NO-GO. Sole parent and stacked base:
PR86 `6056270c612bdc3741dfee5a6c0c338cf9926b0c` (itself based on PR85
`347efbacd1d63fa8ada92c05f4b31c24bf3ff3a5`). This is not an operational
rotation command, migration, KEK-only rewrap, or environment approval.

## Exact file boundary and independent origin

Only these five new files belong to this batch:

- `docs/master-key-rotation-model-contract.md`
- `docs/master-key-rotation-consumers.md`
- `internal/masterkeyrotationmodel/model.go`
- `internal/masterkeyrotationmodel/model_test.go`
- `internal/masterkeyrotationmodel/PROVENANCE.md`

Existing service/security/backup code, schema, migrations, OS providers, CLI,
App startup, workflows, pins, configuration and switches remain byte-for-byte
unchanged. No production package imports the new model. No existing service
constructor, database, credential reader, callback or worker is invoked.
No guest/helper/browser/service is started locally; no real secret, environment,
data file or network is read by the model or its tests. Ordinary compiler/test
tooling reads source; source review and GitHub metadata are not runtime model I/O.

The model and tests are independently authored from the user-approved rotation
policy, this project's frozen source and the public sources below. No archived
CPA/CLIProxyAPI/Sub2API implementation/test/documentation is imported or ported;
earlier research means no strict clean-room claim. There are no new dependencies.

## Policy: change the application root, not just its wrapper

This is the user's explicit exceptional security rotation policy, not a change
to ordinary restart/upgrade/upstream-credential continuity:

1. M2 is independently generated, not M1, a rename of M1, or a derivative of M1.
   All four M-derived purpose keys are new M2 derivatives with separate purposes.
2. Every old employee API Key, including pending/armed issuance slots, is revoked.
   Each modelled reissue is a new independent random Key with a new M2 digest;
   neither the old plaintext nor its digest may be transformed/reused. Real
   authorization, delivery and randomness remain unimplemented.
3. Old administrator/self sessions, enrollment codes, OAuth state and temporary
   secrets, MAC/encrypted cursors, quotes and sticky sessions are invalidated.
   Financial redemption codes are explicitly retired, not rehashed. Unfulfilled
   obligations/reissuance require separately approved reconciliation; unknown
   obligations block a real migration, not silent deletion of financial facts.
4. Retained upstream/Codex/proxy/webhook credentials require authenticated old
   decode and fresh M2 AEAD encryption with the same object/purpose binding.
   The external upstream/webhook plaintext need not change merely because M
   changes; the application encryption key and ciphertext must change.
5. This bounded policy retires old Responses DEKs, wrapped DEKs, encrypted state,
   operations, jobs and managed-tool work. It does not merely rewrap the old DEK.
   Completed operations retain non-executable replay fences/ledger facts; pending
   or unknown outcomes are explicitly retired/rejected, never replayed as new.
   No actual business state is deleted or dispatched by this batch.
6. New M2 canary and binding/seed must agree on instance, purpose, root and epoch.
   Existing formats lack a global authoritative M epoch; an actual adapter must
   prove the complete snapshot and generation mapping before any migration.
7. Old v1/v2/v3 packages and portable recovery bindings are old-root carriers.
   They cannot reactivate old Keys/sessions. W1 is independent of M: do not call
   W rotation, password-KDF changes or package rewrap application-root rotation.
   Retaining historical ciphertext is not permission to restore its authority.
8. bcrypt, unkeyed hashes/checksums, external webhook HMAC keys, independently
   random DEKs/tokens and W have separate classifications; do not blindly rekey
   them as M derivatives. Some are retired because their application state is
   invalidated, not because their generation algorithm used M.

See the complete frozen-source [consumer inventory](master-key-rotation-consumers.md).
Default-off features with retained data are included. Unknown/missing/extra
consumer families, unknown formats, unaccounted records or unresolved policy
dispositions fail closed. A future consumer requires a new reviewed inventory,
not silently ignoring it because its feature is disabled.

## Symbolic inputs, not cryptographic or storage evidence

`Symbol` is an integer denoting identity of a **synthetic** value, not bytes,
an employee identifier, a key, digest or ciphertext. Equal symbols mean the
same hypothetical value. `Independent` / `DerivedFromTarget` provenance and
authentication/completeness flags are explicit model assumptions. The model
cannot establish them for real material; no parser or production adapter exists.
Different symbols do not prove CSPRNG entropy, cryptographic independence,
successful AEAD, memory erasure, or a complete real database inventory.

Each closed-catalog source family has a known format label, snapshot epoch,
bounded declared record count and aggregate value symbol. Format labels in the
catalog are audit/model labels for the source formats in the inventory, not new
on-disk encodings. Zero records must be explicitly observed, not omitted.
Concretely, `source605/<consumer-id>` selects that frozen inventory row;
`rotation/v1/<consumer-id>` is the model's role binding, not a replacement
for the real cryptographic derivation purpose/AAD documented in that row.
Neither label parses or authenticates real records or their historical formats.
Each verification receipt must account for the same count, with zero unresolved
records, an exact role/purpose, the prescribed disposition and the correct M2
material lineage. Actual row enumeration, decrypt/re-encrypt and cleanup remain
unknown until separately implemented and verified.

Old employee Keys and operations are independently enumerated in the synthetic
input. Missing/extra/duplicate reissues or operation decisions are rejected.
Old successful and uncertain operation IDs remain fenced across the switch.
The bounded model does not admit arbitrary new operations or restore packages;
it only demonstrates rejection of old replay/restore and admission of declared
fresh synthetic employee Keys after commit.
The new-Key material origin and digest origin are separate: independent new
material plus a digest explicitly bound to the verified new M2 digest-domain
symbol. Symbol inequality/provenance still cannot establish real randomness or
the mathematical relation of a real digest to a real Key.

## State machine and refusal rules

`Prepare -> Verify -> Commit`, or `Prepare/Verify -> Abort`.

- Prepare checks the rotation purpose, source snapshot/floor, fresh target root,
  full catalog/formats/counts, complete old-Key/operation sets and exact new-Key
  reissues. All source data are defensively copied. There is no serving phase
  before Commit, including Abort; no implicit old-root fallback.
- Verify requires exact plan/instance/root/epoch/canary/seed bindings, complete
  per-consumer receipts, new independent M2 purpose keys and the precise new-root
  or retirement policy. Wrong purpose, preserved M1 purpose material, mixed
  epochs, old ciphertext/Key reuse, unfinished work and unknown proofs refuse.
- Commit is allowed once after Verify. It establishes only an in-memory model
  floor at M2. Repeated commit, verify/abort after commit, automatic M1 rollback,
  and old Key/session admission are rejected. No production service is enabled.
- Abort is terminal and non-serving. Errors do not advance a phase or replace
  the original immutable plan. Methods are sequential model transitions, not a
  concurrent or distributed coordinator.
- Every old backup/unknown restoration is refused. A source snapshot older than
  the caller's declared minimum epoch and a repeated rotation ID are refused.
  That minimum/history is an **assumed input**, not a durable out-of-backup
  revocation watermark. Reconstructing a process from an old backup, physical
  removal from WAL/snapshots, and cross-process rollback protection are unproved.

Errors are finite fixed `Code` strings with no input interpolation. No raw
symbol, key/digest, path, SQL or provider error is an output diagnostic.

## Verification and stop boundary

Public tests include an independently enumerated positive catalog fixture, a
complete Prepare/Verify/Commit case with new Key admission and old rejection,
Abort, and individual mutations for every refusal above. Snapshot and proof
alias mutation must not alter accepted state. Tests use no I/O, randomness,
clock, provider, service or subprocess. Model transitions are not real fault
injection, disk/SQLite atomicity, crash recovery, generation persistence or
production deletion evidence.

Run only the new package's public tests locally, plus formatting/vet and document
link/diff checks. No Windows SQLite full-suite run. One commit/push and one new
Draft PR stacked directly on PR86; record exact head/base and natural
pull_request attempt1 results. Existing workflow filters may naturally execute
Code and the two retained server VM workflows; no workflow is changed or
manually dispatched. Their old M1/W1 recovery evidence does not accept rotation.
On the first unexpected natural red, freeze source, preserve evidence, perform
only read-only diagnosis; no post-red patch, rerun, dispatch or runner switch.
Original E/F must review the new exact head independently. No Ready/merge/tag/
package/deploy, production constructor, real migration or production GO.

## Public sources

Consulted 2026-10-20, semantic references only, not copied implementation:

- [Go HMAC](https://pkg.go.dev/crypto/hmac): a MAC is verified with the same key
  and message; an old digest is not a new-key input/credential recovery method.
- [Go AEAD](https://pkg.go.dev/crypto/cipher#AEAD): nonce and authenticated-data
  bindings belong to the actual future cryptographic implementation, not flags.
- [NIST SP 800-57 part 1 rev.5](https://csrc.nist.gov/pubs/sp/800/57/pt1/r5/final):
  key lifecycle/purpose distinctions; this model is not a compliance assessment.
- [Go testing](https://pkg.go.dev/testing): public deterministic test harness.

No new runtime license/dependency. Existing Go/systemd/SQLite dependencies retain
their licenses. New files have explicit [provenance](../internal/masterkeyrotationmodel/PROVENANCE.md).
