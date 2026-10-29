# Admin model creation: committed model and owned audit

This independent batch starts from the complete PR128 candidate tree
`e521e257504bab161ce96e19add2cec5d1646fa4`. Remote PR128 is Draft at
`5747cb304296f56e99b6ede8b3dc05fb74651108`, based on PR127
`349afaf31a35e6446a357651f07e71d34d666f2a`. The clean local starting commit
`864b6db8ae804063f0f1fa5d211391c503abf279` has that same complete tree but a
different parent and commit identity. These identities are not interchangeable.
Earlier stopped batches, including the bounded read whose output was truncated,
made no implementation changes and are not retrospectively completed here.

## Scope and behavior

The only repository changes permitted in this batch are this new contract, the
independent `internal/service/admin_model_create_audit_test.go`, and `createModel`
with any necessary private helpers in `internal/service/upstreams.go`. Shared
`recordLifecycleAudit`, other mutators, schema/DDL, dependencies, and workflows
remain frozen. Existing create-model WIP in another checkout is not input code.

An authenticated `POST /admin/api/v1/models` may return 201 only when the same
transaction establishes all of these facts:

1. The model INSERT reports exactly one affected row, with no RowsAffected error.
2. A fresh private audit ID did not already exist. The audit INSERT reports one
   affected row and uses this request's actor, `model.create`, `model`, target ID,
   `succeeded`, and operation timestamp.
3. The existing newest-1000 policy runs. Its result contains exactly this new
   audit row with every intended field, without an extra newly inserted event.
4. After the final database write, a full-column re-read matches the intended
   model: ID, kind, upstream ID/model, wire protocol, enabled=true, revision=1,
   archived=false, NULL archived_at, and the original creation timestamp.
5. Commit explicitly reports success. No later database write precedes 201.

Observed mismatches and storage failures return 503 `storage_unavailable`.
Existing request validation remains: absent, missing, or archived upstream is
400; a disabled but unarchived upstream is allowed; duplicate model ID is 409.
Creation adds no change notification or background work. A Commit error is not
proof of rollback or non-publication; callers must not infer safe retry from it.

## Frozen tests-first plan

All fixtures use a newly migrated, synthetic SQLite directory and one fixed
store established before App and HTTP publication. They construct only an App,
its login state and a workerless notification sentinel, and a private HTTP mux
for real login and create-model routes. They do not use App.Open, Handler,
background constructors, or hot-swapping. Every fixture first makes an
independent real authenticated HTTP creation and verifies its persisted model
and audit. The subsequent target request uses the real admin cookie, Origin,
and CSRF token. Synthetic peer models, upstreams, employees, bindings and an
opaque employee access-key digest are protected by exact snapshots.

Target families, fixed before execution:

- `TestAdminModelCreateAuditPositive`: generation defaults, embedding, disabled
  upstream, and newest-1000 retention with older events.
- `TestAdminModelCreateAuditFaults`: the 23 cases listed below.
- `TestAdminModelCreateAuditRejections`: no session, rejected Origin, rejected
  CSRF, malformed JSON, absent upstream ID, missing upstream, archived upstream,
  and duplicate model ID; no target or audit side effects.
- `TestAdminModelCreateAuditStorageErrors`: audit INSERT and retention DELETE
  aborts through real SQLite triggers, with 503 and unchanged database facts.

The 23 old false-201 cases are `model_ignore`, `audit_ignore`, `future_window`,
`audit_delete`, `audit_actor`, `audit_action`, `audit_type`, `audit_target`,
`audit_result`, `audit_time`, `audit_id`, `audit_extra`, `model_delete`,
`model_kind`, `model_upstream`, `model_name`, `model_wire`, `model_enabled`,
`model_revision`, `model_archived`, `model_archived_at`, `model_created_at`, and
`trim_ignore`. Model/audit IGNORE use BEFORE INSERT RAISE(IGNORE); audit deletion,
substitution and model changes use real AFTER INSERT triggers. `future_window`
seeds exactly 1000 future events so trimming removes the current event.
`trim_ignore` seeds 1000 older events and suppresses their retention deletion.
Stale matching model.create audit rows in the other cases cannot stand in for
the operation's own event.

Only `CPA_MCA_OLD_ORACLE=1`, set explicitly for the old-source run, enables the
old oracle. It demands real HTTP 201, an exact persisted counterexample, peer
preservation, stable facts after closing all owners and reopening read-only,
and successful fixture removal, before failing with the fixed label
`MODEL_CREATE_AUDIT_OLD_FALSE_201`. Any fixture, SQL, HTTP, evidence, or cleanup
failure has a separate `MCA_*` label and is an unexpected STOP. Under new source,
the same cases require 503 and exact transaction-state preservation. A selected
case cannot pass by not running or skipping. There is no production test hook.

## Execution and evidence budget

Use the pinned local Windows Go1.26.9 executable. Do not parallelize SQLite
tests. Freeze native stdout/stderr to separate raw files, close them, hash them,
and mark them read-only before inspecting JSON events. Store evidence outside
the repository. Do not print payloads, database values, key digests, raw errors,
or raw logs; report fixed labels, counts and booleans only.

The one old-source invocation selects Positive, Faults, Rejections, and
StorageErrors with `-p=1 -parallel=1 -count=1 -json -timeout=15m`. All positive,
rejection and SQL-abort controls must pass. Only the 23 predeclared Faults
children and their parent may fail, with the old label, after verified cleanup.
Only after that exact expected result may the production function change.

The one new-source invocation uses the same selection, budget and serialization
with the oracle environment unset; all selected names must run/pass exactly
once with zero fail/skip/incomplete/unexpected output. Related regression is
the existing `TestAdminModel(Update|Archive)Audit` families, serial, noncached,
15-minute budget. Since a local C compiler is available, a separate limited
race run selects only the new Positive and Rejections families, serial,
noncached, 15-minute budget. It does not claim full-suite race coverage.

Then check gofmt on the two Go files, vet/build of `./internal/service`, this
document's five local links, diff whitespace, and the exact three-file boundary.
First unexpected error, red, truncation or cleanup failure stops this batch;
no hotfix, rerun, dispatch, or broad test expansion is authorized. Successful
gates permit one local commit. Because local and remote base commit identities
differ, stop before any transfer for a separately approved connector equivalence
batch. No blind push, reset, trust or credential changes.

## Evidence limits and release boundary

The real SQLite triggers prove these controlled transactional counterexamples;
they are not real I/O failures, power loss, or a dynamic explanation of old CI
failures. The notification sentinel proves no creation notification in the
selected executions; it is not a worker or provider acceptance test. Ordinary
package logs are not per-case trace. Fresh local results do not replace formal
E/F review at a new exact remote head. Code513–515, M1→M2, GOV-02, employee C,
and three-platform custody remain open. All PRs stay Draft, with production
NO-GO and no Ready, merge, tag, package, deploy or real credential operations.

## Recorded local execution

Results below were recorded after execution, using the fixed Windows Go1.26.9
binary and the commands/budgets above. Every named test had one run and one
terminal event. All raw streams were closed, SHA256-hashed and made read-only
before JSON analysis; all four stderr streams were empty.

| Invocation | Named results | Go-reported package elapsed |
| --- | --- | --- |
| Old source, explicit oracle | 41 run: 17 pass; 23 expected child failures plus their parent; exactly 23 old labels; zero skip/unexpected/missing | 67.671s |
| New source | 4 parents + 37 children, 41/41 pass; zero fail/skip/unexpected/missing | 65.711s |
| Existing update/archive audit regression | 12 parents + 80 children, 92/92 pass; zero fail/skip/incomplete | 139.093s |
| Limited race: Positive and Rejections | 2 parents + 12 children, 14/14 pass; zero fail/skip/race report | 102.645s |

Old, new, related and limited-race native stdout SHA256, respectively:

- `1c7767bbb26342989d73db16974e58c152fda0f9e0990d444492a7173c6674f6`
- `51171e91d9807026595a71fcc90580c787e03f7961952d83c8bedc193c149e57`
- `586be4376a24ee2097e54147c10ec33c3ade16e0247293c9d54519989e1f95e3`
- `f45ef3af6d6f32eabca0569050351e6e81acfb748c8a8766889a909502250599`

The old oracle's 23 failures are the predeclared counterexamples, not fixture or
cleanup failures. The same cases on new source returned 503 with unchanged
transaction facts. Real positive/default/embedding/disabled-upstream/retention,
Origin/CSRF/session/duplicate and SQL-abort controls passed. Read-only reopening,
peer checks and complete temporary-directory removal preceded every old label.
No Commit-acknowledgement fault adapter, real I/O failure, power loss, whole
service suite, remote CI, provider or production acceptance was executed here.
gofmt reported no files; narrow vet and build both exited 0 with empty streams.
The five linked files exist. Original WIP hashes and the prior local branch ref
were preserved. These results do not grant E/F or production GO.

Related independent specifications: [repository rules](../AGENTS.md),
[product plan](product-plan.md), [independent implementation](independent-implementation.md),
[model update audit](admin-model-update-audit-contract.md), and
[model archive audit](admin-model-archive-audit-contract.md).

## New independent PR129 P2 batch: typed checkpoint sets

Everything above is preserved history of the previous implementation batch.
The frozen remote PR129 head is `7bb15d9326af23fcbbee65ffbc61cf8f54bd23d4`,
sole parent/base PR128 `5747cb304296f56e99b6ede8b3dc05fb74651108`, complete
tree `11619f0ce0da691193363bf0da1b737800152d56`. The clean local starting
commit `8ba97665fe8b3b0cf75399c85ed31e61384cf051` has the same complete tree
and a different commit/parent identity. Neither identity is rewritten.

That old exact head permanently remains NO-GO: original E and F each formally
reported static P1/P2/P3=0/1/0 for the same P2. A model AFTER INSERT trigger can
insert an extra audit before the old rowid baseline, so the subsequent new-row
count misses it. The original E compare/store tooling STOP had no conclusion;
the later formal reviews do not retrospectively complete that preparation.
Those static reviews did not dynamically execute this counterexample. All four
natural first-attempt CI runs at that old head completed SUCCESS, with complete
23 checks =20 success+3 skipped; skipped did not execute. That green CI and the
old local green runs do not close the P2 or establish this new candidate's GO.

Only the same three files may change in this new batch. The shared helper,
other mutators, DDL, dependencies, workflows, old PR/body and original WIP stay
frozen. New code is independently authored from this contract and public Go/SQL
interfaces, not copied from a reference implementation.

### Required transaction protocol

After existing read-only admission/route validation, under the existing
admission lock and the same transaction, before its first write:

1. Read complete baseline B, including actual integer rowid and the seven
   business fields id/actor/action/type/target/result/time with their actual
   SQLite storage classes and exact values. Initial B allows at most 1000
   rows; a 1001st sentinel rejects with 503 before the model INSERT.
2. Model INSERT must affect exactly one row with no RowsAffected error. Read
   the complete audit set again (1000 cap/1001 sentinel); it must still equal B.
3. Generate private A.id absent from B. Construct all seven A values and their
   TEXT classes from this operation; insert A with affected1, and obtain its
   integer rowid from the INSERT result's checked LastInsertId. Rowids may be
   reused: neither positivity nor a rowid high-water mark proves ownership.
4. Before trimming, read the complete set (1001 cap/1002 sentinel). It must
   equal B union exactly the constructed A, comparing every rowid, storage
   class and field value. Counts/digests/high-water marks cannot substitute.
5. With no intervening write, query the original SQLite ordering
   `ORDER BY occurred_at DESC,id DESC LIMIT 1000` over that verified state to
   freeze expected E. Check its expected size, full-tuple membership and exactly
   one A. If A is outside E, reject without trimming.
6. Run the original retention DELETE. Read the complete post-trim set
   (1000 cap/1001 sentinel), requiring exact equality with E and exactly one A.
7. After the last write, retain the original full-column intended model
   re-read. No further write occurs before an explicitly successful Commit
   permits 201. Failures return 503; Commit errors do not prove rollback,
   non-publication or safe retry.

Typed set equality includes all fields, not just rowid keys. It establishes
checkpoint state only, not physical row identity or the absence of short-lived
trigger writes that leave exactly the same tuples at every checkpoint.
SQLite ordering remains native; no Go date parsing, NOCASE or locale ordering.

### Reader types, cursor handling and budget

Each business field must have runtime SQLite type TEXT and at most 4096 bytes.
Each snapshot allows at most 4 MiB (4194304 bytes) of business values, excluding
rowid/type/length metadata. Length means `length(CAST(field AS BLOB))`, including
embedded NUL bytes. Non-TEXT, NULL, inconsistent lengths/types and over-budget
data fail closed with 503. No production schema change is introduced.

A snapshot uses a bounded metadata query first: rowid/type plus each field's
`typeof` and byte length. Only after its full Scan/Rows.Err/Close succeeds and
all budgets pass may a second bounded query read values. That query also guards
each value with SQL CASE/type/length and verifies the same metadata and exact
byte lengths. Thus oversized business values are not fetched into Go merely to
discover they are too large. Driver strings/bytes are accepted only for values
already verified as SQLite TEXT; integers/BLOBs/NULL are not silently coerced.
Every cursor checks Scan, Rows.Err and Close, including early/error paths, and
closes before another query or write. Partial rows are never a successful set.

There are four complete checkpoint reads and one SQL Top1000 read, each with
at most two bounded cursors and at most 1002 metadata rows per cursor. No extra
background process, store replacement or unbounded COUNT/MAX scan is added.
All queries use the existing request context. These are row/business-byte
budgets, not proof of a hard SQLite execution-time or allocator bound, timely
row-step cancellation, or real I/O-failure behavior.

Normal valid B retains duplicate-model-ID 409, absent/missing/archived upstream
400, disabled-upstream acceptance, and no notification. An invalid/over-budget
initial B may return 503 before duplicate-ID handling; that priority is explicit.

### New independent tests-first selector and gates

First add only `TestAdminModelCreateP2PrewriteOracle`. Its independent switch is
`CPA_MCA_P2_PREWRITE_ORACLE=1`, never `CPA_MCA_OLD_ORACLE`. In a fixed synthetic
fixture with real authenticated admin HTTP, an AFTER model INSERT trigger for
only the target creates one fixed extra peer audit from empty B. On the frozen
old source require real 201, the exact intended model, two persisted events
(extra plus the operation's own event), unchanged peer facts, read-only reopen
and complete cleanup, then exactly one expected label
`MODEL_CREATE_P2_PREWRITE_OLD_FALSE_201`. Every fixture/SQL/HTTP/cleanup failure
uses a separate MCA_P2_* or existing MCA_* label and immediately stops the batch.
The old 23-case oracle, its selector and history remain untouched.

Only after sealing/validating that single expected red may production change.
The default form of the same test then requires 503 with exact pre-call facts.
Additional test families, frozen before the first new-source execution:

- `TestAdminModelCreateP2Positive`: empty/one/999/1000 baselines, a transient
  event removed before the checkpoint, and identical-tuple rowid replacement.
  The latter two demonstrate the explicit checkpoint/identity limitations.
- `TestAdminModelCreateP2CheckpointFaults`: model-trigger extra/change/delete/
  rowid reuse; audit-trigger change/delete/rowid reuse, an extra old event that
  retention would remove, trim-time mutation/addition, storage-class rewrite,
  B1001 (including duplicate priority), baseline BLOB/field/total overflow and
  pre-trim total overflow. They are checkpoint guards; a transient event is not
  mislabeled as a final persisted error or given the old 23-case label.
- `TestAdminModelCreateP2ReaderBounds`: 1000/1001 full limits, legal pre-trim
  1001 and rejected 1002, every field's 4096/4097-byte and BLOB boundaries,
  NULL ID, exact 4 MiB/+1 totals, and native same-time ID/BINARY Top1000 order.
- `TestAdminModelCreateP2ReaderErrors`: metadata/value Next and Close controlled
  driver failures. A real SQLite store is bootstrapped and closed before the
  unit reader's driver is fixed; no App or running store is swapped. These
  adapters are synthetic return-boundary evidence, not real I/O faults.

New-source small selection is only the PrewriteOracle (one name, switch unset).
Then run the original four Audit families plus the four additional P2 families
above, excluding the already checked PrewriteOracle. Related regression remains
the original update/archive audit families. Limited race selects only original
Positive/Rejections plus the new PrewriteOracle. Use pinned Go1.26.9,
GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off, serial `-p=1 -parallel=1`,
noncached `-count=1 -json -timeout=15m`. Seal each separate native raw stdout/
stderr outside the repository, close/hash/make read-only before per-name JSON
analysis. Expected old exit1/one label is distinct from any unexpected red.

First unexpected tool/native/fixture/cleanup error or truncation ends this
independent batch; no hotfix, rerun or manual CI. Finish with gofmt, narrow
vet/build, the same five local links and exact three-file diff checks. Only
passing gates authorize one local commit. Since its local parent differs from
the remote PR129 SHA, stop before transfer and report full parent/tree/three
blob/mode identities, clean state and raw hashes for a separately authorized
connector-equivalence batch. No remote commit, push, new PR, body update or
dispatch belongs to this local batch. All old E/F/CI evidence stays permanent;
Draft and real-migration/production NO-GO remain in force.

### Recorded execution of the new independent P2 batch

All following native runs used the pinned offline Windows Go1.26.9 binary and
serial, noncached selections above. Each raw stream was closed, SHA256-hashed
and made read-only before per-name analysis. Each selected name had exactly
one run and its expected terminal event; no skip, missing, duplicate or extra
name occurred. All five stderr streams were empty.

| Selection | Observed named result | Go package elapsed |
| --- | --- | --- |
| Old-source independent singleton | 1 run, 1 expected failure, exactly 1 new P2 label; no fixture/cleanup/other error | 3.001s |
| New-source small singleton | 1/1 pass | 2.412s |
| New-source functional selection | 8 parents + 93 children =101/101 pass | 222.349s |
| Related update/archive audit regression | 12 parents + 80 children =92/92 pass | 215.809s |
| Limited race | 3 parents + 12 children =15/15 pass; no race report | 135.453s |

The broader new-source, related and race invocations also used failfast. The
new small selection was excluded from the broader functional invocation; the
race selection intentionally exercises its own configuration. The old global
23-case switch was unset throughout this batch. The 23 existing fault cases
passed their normal new-source expectations; their earlier red history remains
unchanged. New persistence fact snapshots additionally include actual rowids
and runtime storage classes. Nothing retroactively strengthens earlier runs.

Native stdout SHA256, in the table's order:

- `56aec891dcd12d86cf2e18441d040229afde9f491b557735ba43dd95cea19f22`
- `3ea85966f709aafd3fbdd7ccf39574e65de9419c2495881d7b978a8acc2b9da3`
- `a35f42331b4841e7f39515592a1e58f0d5acf689b6cacd46b1c6f01684aa57e6`
- `bfcf667351b74a045f33a0108b32e2cb7b2fa162e2c018709e49204a4f4882cf`
- `4b4e91bb884807c6d47d3766b01d0503e58889c871cdfd3b416d2ee2620d1b34`

Every empty stderr SHA256 is
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
The production and test input hashes were unchanged across the new-source
functional/related/race runs. gofmt reported no files; narrow vet and build both
exited 0 with empty streams. The original 160 contract lines were preserved.

Only the new old-source singleton dynamically establishes the old false 201
and the extra plus own persisted events, followed by read-only reopening and
complete cleanup. This evidence belongs to this new batch, not the original
E/F static reviews. Other checkpoint guards do not claim their short-lived
events were final persisted corruption. The transient-event and identical-
tuple/rowid-replacement positive witnesses passed, preserving the stated
checkpoint-state/physical-identity limitations. Controlled reader Next/Close
failures remain synthetic adapter evidence, not actual I/O-failure injection.
No full-suite race, real migration, new-head CI, remote commit or new formal E/F
GO is claimed. The old PR129 head and production remain NO-GO.

Public interface references used for the independent design:
[SQLite runtime types and ordering](https://www.sqlite.org/datatype3.html),
[Go SQL rows and scanning](https://pkg.go.dev/database/sql#Rows).
