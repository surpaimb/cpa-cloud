# Admin model archive: first-transition retained audit boundary

Status: new independent bounded candidate, stacked on exact Draft PR124
`f85c6b5edecbcb409038fb237bafd9d7069ade2f`, base / sole parent PR123
`f1b824effbf81ae3cc30d701c270dcd51cd9f4d2`, base tree
`cd5584355455692c57f192914f91348174a2271c`. No new-head CI or independent
formal-review result is claimed. Draft / real migration / production remain NO-GO.

## Independent provenance and frozen scope

New source and tests are independently authored from CPA Cloud specifications,
the root's archiveModel-only authorization and the public SQLite / Go interfaces
listed below. This is not represented as strict clean-room development. No
archived CPA, CLIProxyAPI, Sub2API, sibling logout implementation, new dependency
or license change is included. Prior PR124 supplies design ideas, not archive
dynamic evidence or a transferable acceptance conclusion.

At most three files may change: only `archiveModel` in
[account_lifecycle.go](../internal/service/account_lifecycle.go), the new
[dedicated test](../internal/service/admin_model_archive_audit_test.go), and this
contract. Shared recordLifecycleAudit, updateModel, archiveUpstream, every other
mutator, routes / auth, production schema / migrations, dependencies / workflows,
and all previous tests stay frozen. The broader archiveUpstream gap remains open.
Existing semantics come from the [lifecycle contract](account-lifecycle-management-contract.md)
and [preview contract](preview-contract.md).

## API and transaction invariants

Use the real DELETE /admin/api/v1/models/{id} path and preserve administrator
session -> Origin -> CSRF, JSON decoding, invalid revision400, missing target404,
and unarchived stale CAS409. Valid input still uses the existing admission write
lock and one SQLite transaction. A disabled but unarchived model is a genuine
first transition: enabled remains0 but archived/time/revision must change.

The already_archived branch stays BEFORE CAS for any positive expected_revision.
It returns 200 already_archived without new audit, revision/time change, Commit,
notification or monitor cancellation. Invalid/nonpositive revision is rejected
before this branch. A no-op does not authenticate or repair a missing historical
audit and must not be presented as a new first-transition success.

For the first real transition only, use an operation-private audit ID, prove its
absence, INSERT exactly one event with the real administrator actor,
model.archive / model / exact target / succeeded / known operation UTC time.
Check affected-row success, preserve the original newest-1000
occurred_at DESC,id DESC trimming, and confirm exactly one new retained event
with that exact identity and fields and at most1000 total events. Old matching
rows, unchanged table size and INSERT RowsAffected alone are insufficient.

After all audit / retention writes, confirm the complete target tombstone row:
enabled0, archived1, exact archived_at, revision+1, and preserved ID, kind,
upstream target/model, wire and creation time. No writes follow final reads before
the single Commit. Only explicit Commit success allows 200 archived, notification
and target monitor cancellation. Audit write / identity / retention / model
confirmation errors return the fixed 503 storage_unavailable, with verified
failed-transaction facts unchanged and no success signals.

## Frozen tests-first oracle

Before product changes, run independent ordinary controls, then the three
frozen old-product children ignore / future_window / delete_inserted using only
the explicit old-oracle environment. The sole expected label is
MODEL_ARCHIVE_AUDIT_OLD_COMMITTED_WITHOUT_CURRENT_EVENT.

Each fixture first proves a different control-model real admin HTTP DELETE,
retained archive audit, actual account-pool epoch change and control-only monitor
cancellation. Before the target request it proves unarchived state, correct CAS,
the exact audit ID/field baseline and the scoped SQL fault. IGNORE and deletion
include an old matching target event; future_window has1000 future-time matching
events. The expected label is permitted ONLY after old HTTP200 archived, exact
persisted tombstone/revision, no current audit compared to prior IDs, one real
notification/target cancellation and untouched peer are verified. Close all
owners, then use a fresh connection to the same file to recheck the complete
committed facts and the missing current audit. Reader and fixture cleanup must
finish successfully BEFORE emitting the expected old-product label.

Do not infer a disabled first transition from an enabled flip. Do not confuse
already_archived200 with first-transition200. Any fixture/auth/SQL/orchestration,
unexpected assertion or cleanup failure has a distinct AMA_* label and stops the
batch, without hot repair or rerun. The source/test/contract and case names are
hashed/frozen before this oracle. Raw bytes are sealed before parsing.

## Fixed fixture and candidate acceptance

The new test owns real initialization/store migrations, real administrator login,
and the actual requireAdmin(...,true) DELETE handler on an ephemeral loopback
HTTP server. One fixed DB is selected before App publication. Never call App.Open,
App.Handler, worker constructors or replace a live App DB. Adapter tests close
the bootstrap owner before constructing the fresh fixed-DB App. There is one
resource owner, registered after TempDir: stop HTTP, join handlers, close contexts
and DB, verify closure, then let TempDir remove the files.

A non-nil real accountPoolRuntime supplies epoch/change-context observations.
A disabled, workerless typed monitor coordinator owns target/peer/control contexts;
its cancellation wrappers count calls while still cancelling real contexts.
Assertions occur before cleanup cancels all contexts. Never manually notify or
cancel to manufacture a positive control. These are signal controls, not real
worker execution acceptance. After owner close, a separate temporary reader
reopens the same SQLite file; this is not a hot DB replacement or full App.Open
restart acceptance. Protected facts snapshot all user tables without logging data.

Candidate positives: enabled and disabled first archive,999/1000 audit boundaries
including exact original trimming order, close/reopen persistence, retained real
actor/current identity, preserved pool/employee/Key/peer/upstream facts. Repeated
positive stale revisions on a tombstone return already_archived200 with no writes
or signals, including when no matching historical event exists.

Candidate negatives: IGNORE/future_window/insert-then-delete, wrong actor/target/
action/type/result/time/identity, duplicate events, INSERT or trimming SQL errors,
ignored trimming, model reversal and creation-time mutation. Audit-boundary
failures must be503 with pre-request facts unchanged and no success signals.
Existing auth/Origin/CSRF/decode/revision404/409 ordering remains distinct. A
model UPDATE affecting zero rows retains the existing CAS409 boundary.

Two simultaneous real HTTP DELETEs with the same revision must yield exactly one
archived and one already_archived response, one retained new event, one revision
increment, one notification and one target cancellation, with peer unchanged.
This is one-App/admission-lock concurrency, not external writers or all-process
consistency. Concurrent requests join before facts/signals are inspected.

Native deferred-FK Commit refusal has FK-on and a satisfying-parent positive
control. A request-owned test-only adapter separately delegates real Commit then
returns a controlled fixed error/cancellation. This is NOT real I/O fault
injection. In that uncertain scenario503/no success signals may coexist with a
persisted tombstone+audit; never claim rollback or auto-retry. A later explicit
DELETE with the old positive revision returns already_archived200, with no extra
Commit/audit/notification/cancel: do not copy updateModel's later409 assertion.
The possible lack of a post-uncertain-Commit notification is preserved, not claimed
repaired by this narrow batch.

## Verification, transport and unchanged NO-GO

Use the approved absolute Go1.26.9 executable after independent ordinary-file,
hash and version checks. Offline GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off,
GOFLAGS=-mod=readonly, GOWORK=off. Windows SQLite runs are uncached and serial
(-count=1,-p=1,-parallel=1). Capture stdout.raw/stderr.raw, exact command/env,
source hashes and exit before parsing. Gate directories cannot be reused.
Run directed tests, related frozen regressions, selected race, gofmt/vet/build,
document links and diff/three-file/function scope checks. Selected race is not
all concurrency, storage/power-loss fault injection or production readiness.

The first unexpected tool/test/fixture/cleanup/scope result stops with its raw
record; no hot fix, rerun or manual workflow dispatch. Only after all gates pass
may one local commit be created. If its local parent differs from exact remote
PR124 but the base trees match, stop before transport for separate root connector
authorization; do not reset, blindly push or alter credentials/SSH trust.
Old PR124 green and author45/144/27 are not this candidate's evidence. Preserve
old Code513-515 red, UNKNOWN/CAPACITY_UNPROVEN, real M1->M2/53 consumers,
GOV-02 old-backup revival, employee C host/custody and all production NO-GO.
No Ready/merge/tag/package/deploy; independent new-head E/F review is separate.

## Public interface sources

Consulted2026-10-22: [SQLite RAISE](https://www.sqlite.org/lang_createtrigger.html#the_raise_function),
[SQLite changes](https://www.sqlite.org/c3ref/changes.html),
[Go transactions](https://go.dev/doc/database/execute-transactions).
IGNORE does not roll earlier writes back; affected-row counts do not confirm
retention after triggers. Commit error is neither verified success nor verified
rollback without an independent state observation.
