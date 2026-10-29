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
