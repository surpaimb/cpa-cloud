# Admin model update: operation-private retained audit boundary

Status: new independent bounded candidate, stacked on exact Draft PR123
`f1b824effbf81ae3cc30d701c270dcd51cd9f4d2` (base PR122
`38ab4bac525f11cdc5cc1fd7219d6d49ad2194d9`, tree
`4b17101ac84ff905cf3f5240dc98d9e60d731a57`). No new-head CI or formal
review is claimed here. Draft / real migration / production remain NO-GO.

## Source and frozen scope

Independently authored from the CPA Cloud functional contract, the root's bounded
authorization, and public SQLite / Go interfaces. This is not strict clean-room
development. No archived CPA, CLIProxyAPI, Sub2API or sibling logout implementation
is used. Existing dependencies and their licenses are unchanged.

Only `updateModel` in [account_lifecycle.go](../internal/service/account_lifecycle.go),
the new [dedicated test](../internal/service/admin_model_update_audit_test.go), and
this contract may change. Shared `recordLifecycleAudit`, archiveModel,
archiveUpstream, all other mutators, routes, authentication, migrations / DDL,
dependencies, workflows and previous tests stay frozen.
The [lifecycle contract](account-lifecycle-management-contract.md) and
[preview contract](preview-contract.md) remain the API boundaries.

The preceding independent batch stopped at `Get-Command go` because PATH did not
contain Go. It made no implementation or test progress and is not retrospectively
successful. This new batch uses only the explicit Go1.26.9 executable approved by
the root; its file identity and version were independently checked before edits.

## Required product behavior

The unchanged authenticated PATCH path keeps authentication -> Origin -> CSRF,
decode errors, positive expected_revision, target / tombstone / CAS checks,
provider / wire constraints and nonempty-pool target-change restrictions.
An authenticated valid same-value PATCH keeps its existing meaning: one revision
increment, one audit and one post-Commit notification.

Within the existing single transaction, create one operation-private audit ID,
prove it was absent before insertion, insert the exact real authenticated actor,
`model.update`, `model`, target ID, `succeeded`, and the operation's UTC timestamp.
Check INSERT affected exactly one row; preserve the existing newest-1000
`occurred_at DESC,id DESC` trimming semantics. Then confirm exactly one newly
retained row and the exact operation ID and fields. An old matching row, unchanged
table size, a generated-but-not-retained ID, or RowsAffected alone is insufficient.
Read the target model back after all audit / retention writes and require the
complete intended row including preserved kind, archive and creation facts.
No further DB writes follow these final checks before the single Commit.

Only explicit Commit success allows HTTP 200, account-pool notification and target
model-monitor cancellation. Any write / identity / retention / final-row failure
returns the existing fixed `503 storage_unavailable`; the transaction is not
committed and successful notifications / monitor cancellation do not occur.
This is an operation-local confirmation, not hostile-schema authentication,
global audit completeness, multi-node coordination, or external rollback defense.

## Frozen tests-first oracle and controls

Before the product edit, run the positive fixture / HTTP / notification controls,
then `TestAdminModelUpdateAuditRetention` with `CPA_MUA_OLD_ORACLE=1` only.
Its three frozen children are `ignore`, `future_window`, `delete_inserted`.
The sole expected old-product label is
`MODEL_UPDATE_AUDIT_OLD_COMMITTED_WITHOUT_CURRENT_EVENT`.
It is emitted only after proving authenticated real HTTP 200, persisted target
revision and enabled change, no new target audit compared to the exact previous
IDs, and exactly one real runtime notification / target cancellation with the
peer untouched. IGNORE and deletion keep an old matching audit row as a negative
control; the future window contains exactly 1000 future matching audit rows.
Any prerequisite, SQL, orchestration or cleanup failure has a distinct `MUA_*`
label and is unexpected: stop and preserve raw evidence, never backfill success.
The oracle is independently executed in this new batch, not attributed to earlier
E/F static reviews or to the stopped PATH batch.

In the candidate run the same three children, without the oracle environment,
must receive fixed 503 and preserve the complete pre-request protected facts.
All case names and assertions are frozen before the oracle; no hot repair after
an unexpected failure or manual workflow rerun is permitted.

## Fixture and acceptance matrix

The new test file owns a real migrated temporary SQLite DB and real administrator
login / requireAdmin / HTTP PATCH. It never calls App.Open / Handler or starts a
background worker. A non-nil real accountPoolRuntime observes epoch / cancellation;
a workerless typed monitor coordinator observes target and peer contexts. These
are notification and cancellation controls, not worker execution acceptance.
One immutable DB is assigned before HTTP publication; Commit-adapter tests close
the bootstrap DB before constructing their fresh App. No live DB replacement.
TempDir is created before registering the sole resource owner. Stop HTTP, await
handlers, close runtime / DB, verify contexts and closed handles before TempDir
cleanup. Positive restart checks open the store only after the old App has closed.

Required positives: ordinary update, legitimate same-value PATCH, changed default
target / wire, 999 / 1000 prior audit rows with correct trimming, retained actor /
identity, matching returned and persisted model, unchanged peer / employee / Key
facts, and close / reopen equality.

Required negatives: INSERT IGNORE, insert-then-delete, wrong actor / target,
duplicate new events, changed audit identity, retention DELETE ignored or rejected,
future timestamps dropping the current event, INSERT SQL rejection, audit-trigger
model reversal, and final creation-time mutation. All reject zero-current-audit
success. Existing input / authorization / CAS / pool / provider behavior stays
distinct from these 503 failures. Each real fixture first proves a separate
control-model PATCH, real notification and target-only cancellation.

Commit coverage separates native deferred-FK refusal (with FK-on and satisfying
parent positive control) from a test-only adapter returning a fixed error or
cancellation *after real Commit*. The latter is a controlled return-boundary
simulation: HTTP 503 and no success notification, while the model + audit may
already be durably committed. Do not claim rollback or automatically retry. A
later explicit request with the old revision must conflict, not repeat the write.

## Verification and evidence discipline

Use fixed offline Go1.26.9, GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off,
GOFLAGS=-mod=readonly, GOWORK=off. Windows SQLite tests are serial (-p=1,
-parallel=1), uncached. Seal stdout.raw and stderr.raw before parsing, preserving
all bytes, command / environment / source hashes and exit status outside the repo.
The frozen runner refuses duplicate gate directories. Run directed cases, related
regressions, selected race, gofmt, vet, build, document links and diff / scope gates.
Selected race is not proof of all concurrency; native SQL triggers are controlled
synthetic fixtures, not real I/O faults, real credentials, or production recovery.
Any first unexpected result stops this batch with its original raw evidence.

One local commit is permitted only after all gates pass. A different local parent
SHA requires a stop before transport and separate root approval for equivalent
tree connector transport; never reset, blindly push, or alter trust / credentials.
Old PR123 green and Code513-515 red / UNKNOWN histories, GOV-02, employee C,
real M1->M2 and production NO-GO remain unchanged. No Ready / merge / tag /
package / deploy. New-head CI and original E/F independent review are separate.

## Public interface sources

Consulted 2026-10-22: [SQLite trigger RAISE](https://www.sqlite.org/lang_createtrigger.html#the_raise_function),
[SQLite changes](https://www.sqlite.org/c3ref/changes.html),
[Go transactions](https://go.dev/doc/database/execute-transactions).
RAISE(IGNORE) does not roll earlier changes back. A top-level affected-row count
does not prove that AFTER-trigger effects retained the row. Commit errors must
not be represented as verified success or verified rollback without evidence.
