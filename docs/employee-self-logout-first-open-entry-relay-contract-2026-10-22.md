# B1: original caller to real first Open entry only

Status: independent bounded implementation contract, 2026-10-22 (Asia/Shanghai).
Base is open/Draft [PR116](https://github.com/surpaimb/cpa-cloud/pull/116), exact
head `ef55ad47e12cb27451bbc3614a0c1120ba3f4d9f`, sole parent red PR115
`e8521edae3266750a7873e57690b5f1dd602fc7d`, complete tree
`9654dabe390be2d11edf6daf151ab9b192e5c152`. Its 12437-character body is frozen.
This contract precedes implementation and tests-first execution.

Read the [caller-only origin contract](employee-self-logout-initialize-driver-origin-caller-contract-2026-10-22.md),
[owner-only contract](employee-self-logout-first-app-owner-only-contract-2026-10-22.md)
and [A0 lifecycle evidence contract](employee-self-logout-code513-lifecycle-evidence-contract-2026-10-22.md).

## Exact six-file authority

Added: this document, `internal/service/initialize_first_open_entry_relay_20261022.go`
and its `_test.go`. Only local modifications to
`internal/service/initialize_driver_origin_caller_20261022.go`, its `_test.go`,
and `internal/service/expiry_invocation_owner_20261022.go` are allowed.
No seventh file. App, original fixture, owner `_test`, config/store/worker,
financial/driver/Gate4, dependencies/workflows, old contracts/PRs and monitors
remain byte-frozen. If actual wiring or exact guard projection needs more, stop.

The sole new fact is **OPEN_ENTRY_REFERENCE_ONLY / ENTRY_RECEIVED**: the actual
original Initialize caller's successful canonical receipt has reached its
pre-bound original first Open invocation. It is not Open success, a driver used
by an App store/opener, Conn/Tx/pass/raw provenance, physical release, a BUSY
cause or old-red repair. No Driver.Open or other driver method is invoked by B1.

## Existing actual chain, not a helper-only positive

The original constructor installs Init and first-owner plans, invokes actual
Initialize, calls `initializeOriginReturned(initializeErr)`, checks that actual
error and invokes original `f.open()`. The unchanged `f.open()` obtains startup
through original `firstOwnerStartup` and calls actual `Open(startup, Config...)`.
Open's unchanged first statement calls `beginExpiryInvocationOwner(ctx)`; its
unchanged two-result extractor then restores the original business context.

Before Initialize, the original `initializeOriginStartup` helper must pre-bind
the exact same fixture's Init plan and first-owner plan. Do not learn the target
from a receipt later or infer it from selection/driver equality, a path, t.Name,
stack, address, log, global plan or registry. Storage is not an Open capability.
The actual capability is still allocated only inside the existing Open-entry
helper at that real first entry, before validation/store construction.

The Init plan saves the **canonical receipt pointer** from its existing actual
caller-return association. A copied struct with equal attempt/selection/driver
is not that original pointer. Only actual nil return plus the unchanged prior
captured/prepared/caller-seen/delivered state can arm the pre-bound relay.
Failed/pending/unknown Init, foreign/copy/missing receipt or wrong target rejects.
Canonical metadata must not change the old seven-event history or return value.

After the existing owner helper creates and installs its actual fresh owner
capability and records its old entry event, B1 consumes only the matching armed
relay, target plan and actual installed capability, before that helper returns.
Copied/foreign capability, already claimed target, late arm, duplicate binding
or receipt use cannot overwrite the original association or promote rejection.
No new field or call is inserted in App/fixture/context-carrier source.

This boundary trusts the explicitly audited callee/caller source sites and the
real constructor tests. Private metadata helpers alone cannot authenticate
arbitrary same-package code fabricating an entire chain. Direct helper controls
are synthetic classification, never proof that actual Open was invoked.

## Independent bounded state and default semantics

One selected Init plan, one canonical receipt, one pre-bound target, one actual
Open capability, and exactly two value-event slots: ARMED then ENTRY_RECEIVED.
Prebinding allocates metadata, not an event/capability. Use explicit independent
numeric states/zero expectations; default is not positive. A rejection flag
latches separately, preserving immutable prior events and historical receipt.
Overflow/unknown/missing/foreign/duplicate/late rejects; no resize/wrap/overwrite
or post-failure retry. This is not A0's native-lifecycle/pass envelope.

Only the existing Auth parent and confirmation-query-error first-Open selections
are eligible. PR115 Code513 zero-rows and ignored-body-query remain unselected.
Nil/default helpers create no relay, event or extra context-method read; no
driver getter/method is repeated. Existing observer, owner and context behavior
remain unchanged. The original legitimate second Open carries no first-owner
plan/relay, is not a duplicate B1 failure, and cannot rewrite first-entry history.
Deliberately replaying the old selected carrier is different and rejects.

ENTRY_RECEIVED precedes Open's result. A later Open error cannot erase that
entry fact or retroactively claim an App/store success. New reports never issue
old-case GO; prior case/cleanup failures remain primary. Output is fixed enums,
booleans/counts and fixed labels only, not references, formatted drivers, raw
errors, SQL/arguments, DSNs/paths, addresses/handles, identities or secrets.

No business parameter, context extraction, cancellation, error/SQL/return,
store initialization, cleanup, stop/Wait/Close order or retry is changed. New
mutexes protect fixed metadata only. No IO, user callback, logging/testing,
driver call or wait occurs while held. Do not reorder old locks or create a
cross-plan lock cycle; inspect old metadata then release its lock before target
installation. The entry receiver uses the already-held owner lock and only its
own relay lock, never re-enters an old plan lock. Synchronization/retention can
perturb scheduling; no zero-disturbance claim is made.

## Exact inherited guard preservation

A new whitelist projection must reconstruct each modified PR116 file exactly
after removing only its enumerated B1 fields/calls, canonical-pointer cache and
precisely documented comment adjustment, with formatting restored. Frozen Git
blobs: Init ordinary `1c60fccf7269c89040c363ebea7d334249db9aaa`, Init `_test`
`1ec1ea179f78553066d92fdf309924f33e5edfc5`, owner ordinary
`13de95f8276eebc89bda79bffead527280d48b40`. Do not refresh an old hash to hide
another edit. Old Init audit/projection, eight source mutations and three old
owner mutations remain intact; new controls call their audit helpers, not old
Test functions.

App and original fixture are byte-unchanged, not broadened projections. Existing
old App/worker/fixture and Init/owner-guard hashes and independent state checks
remain unchanged. The owner `_test` and Gate4 file remain byte-unchanged.
All 22 numeric anchors therefore stay unchanged; all 54 raw literals retain
`ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860`.
Original rollback-defer / BEGIN_RETURN / cut / first-schema adjacency, route
indices, one arm/route/cut/report and cleanup tail remain untouched.

Register a separate B1 report before existing Init report registration, so LIFO
preserves old cleanup -> owner report -> Init report -> B1 report. Do not add a
direct cleanup literal to the fixture, flush an old collector or let a later
report fill an earlier view. On prior failure report only a fixed non-acceptance
summary, not case success or complete physical cleanup.

## Directed tests-first and scope of evidence

First compile/run one new source oracle against unchanged old source; it must
fail at fixed B1_SOURCE_OPEN_ENTRY_MISSING, not fail to compile. Then implement
only this contract and run only new TestInitializeFirstOpenEntryRelay20261022
names, serial non-cached Windows SQLite, plus directed service build/vet,
format/link/diff/six-file checks. No old TestSelfLogout case body or old workflow
is manually run, no full-repository/concurrent suite and no extra probe lifetime.

Two serial new positive subtests may each call the existing constructor once,
one per existing typed selection, preserving normal original lifetimes. The
first original cleanup completes before the second starts. Together with exact
source oracles they must confirm original Initialize -> original caller ->
original first Open entry and actual owner capability, not a test-only helper
counter or mirror Open. They are real synthetic SQLite constructor exercises,
not DB-free tests or executions of old Auth/confirmation case bodies.

Pure controls may reuse only those already captured store/driver witnesses;
they call no getter/driver/DB method or failed Initialize. Cover failed/pending,
foreign/copied receipt, same-driver/different-root wrong target, wrong plan and
actual capability, duplicate/late/missing, overflow/unknown, default zero events,
unchanged old histories, second-Open startup without relay, and later failed
Open classification without App success. Such negatives are synthetic metadata,
not real failed Init/Open/I/O injections. No extra actual second Open is added.

In-memory source mutations must reject deleted entry consumption, consumption
moved after store construction/return, helper-only replacement, copied canonical
receipt, wrong capability and forged caller association. Preserve and exercise
the inherited eight-plus-three mutation conditions through new named controls.
No mutated production code is executed. Helper classification PASS is not the
real positive oracle.

First unexpected directed-test or natural red freezes source/output and stops;
no hot patch, later case, rerun or dispatch to obtain green. A required extra
file, broken default/guard or changed business path stops without commit.

## Historical boundaries, provenance and handoff

PR115 Code513 run39659794029 remains FAILURE with race0 job119429570609
Boundaries/zero-rows and race2 job119429570545 Normal/ignored-body-query, App1
REOPEN/AFTER_APP_CLOSE, OBJECT_ENUMERATION/QUERY_CONTEXT/SQLite5/context NONE,
SLOG21_UNEXPECTED_SNAPSHOT_QUERY; 23=18success/3skipped/2failure. Its native/VM
successes do not cover those reds. PR113 Code512, PR111 Code511, F PR111 static
P3, separate E PR108 P3 and earlier PR108/106/100/102-105/local reds stay frozen.
No shared-cause, introduction, exoneration or BUSY repair is inferred.

PR116 A0 docs-only narrow GO does not grant B1 GO. Its 8-pass candidate is below
the old 12/17 observations; prospective lifecycle-envelope approval stays a
separate batch. B1 does not implement opener, App-store use, Driver.Open, Conn,
Tx/pass, raw cut, physical release, migration or product behavior.

New text/source/tests are independently authored from the root's six-file
authority, project contracts and frozen source, not archived CPA/CLIProxyAPI/
Sub2API or copied library implementations. No strict clean-room claim or new
dependency/protocol/license change; existing Go/SQLite licenses remain.

After allowed validation, one non-force commit and stacked Draft PR. First SSH
strict host-identity refusal permits only the authorized connector fallback
with exact sole parent/tree/six blob/mode equality; no trust/credential/remote
change, retry or local reset. Disclose different metadata SHA. Report actual
new-head natural CI initial state, never predeclare green or borrow old heads.
Root owns exact-head terminal verification and original E/F formal independent
read-only review. Keep Draft / dynamic-case, real migration, product and
production NO-GO; no Ready/merge/tag/package/deploy or monitor/old-PR change.

## Actual batch ledger

One Windows Go1.26.9 serial non-cached batch was performed; output artifacts
remain outside the repository. Only new TestInitializeFirstOpenEntryRelay20261022
names were selected. No old Test function/case/workflow was manually run.

- Tests-first compiled against unchanged prior source and failed exactly
  B1_SOURCE_OPEN_ENTRY_MISSING, exit1, empty stderr; one test run, no DB/Open.
- A pre-test formatting pass located two surplus trailing braces in the new
  test file; these were corrected before build/vet and before the implementation
  test command. This was not a runtime test/CI failure, and no failed test output
  was patched, discarded or rerun to obtain green.
- Service build/vet each exit0, empty stdout/stderr. Three enumerated modified
  files reconstruct their exact old blobs after projection; no old hash refresh.
  gofmt-l and diff-check passed; all three relative contract links resolve.
- The one final directed command ran/passed **48 names = 5 parents +43 children**
  exactly once, 0fail/skip/duplicate/unpaired, empty stderr. Groups: source11,
  inherited guards12, state values1, default/context1, real-seam23. Old case
  run events: zero. The unchanged old audit helpers and their eight-plus-three
  in-memory mutation conditions passed through these new named controls.
- Two serial existing-constructor invocations exercised the original actual
  Initialize, original caller and actual first Open entry, one per old typed
  selection. Original first cleanup finished before the second started. Each
  emitted B1_REAL_ORIGINAL_CALLER_FIRST_OPEN_ENTRY_CONFIRMED; fixed entry-only
  tags were emitted at inspection/report. Actual Init stores/DBs were distinct
  while implementation was the same singleton; wrong-root metadata still failed.
- The remaining foreign/copied/failed/pending/wrong-capability/late/duplicate/
  overflow controls are synthetic metadata using those retained references,
  not actual failed Init/Open/I/O or physical-release injections. The later-Open
  error control is explicitly synthetic; legitimate second-Open startup was
  checked without creating an extra actual Open lifetime. Helper classification
  does not replace real-constructor/source evidence.
- Old owner/Init cleanup tags arose from those constructors, not historical
  Auth/confirmation case execution or acceptance. Their ordinary synthetic
  SQLite/App/HTTP fixture lifetimes did execute; no DB-free or production/real
  credential/client claim is made. No local race/full-suite or new coverage claim.
- All Go source froze before the final directed command; only this documentary
  ledger was completed afterward. New submission/natural CI/formal E/F review
  must pin the actual new head; no terminal green or implementation GO is assumed.

| Preserved raw JSONL | SHA256 |
| --- | --- |
| Tests-first expected source red | `a3b9801c1ebd326d7fc7d637f9c393513c46768a337727dc5d1c6397cb922076` |
| Single final directed command | `f229c730d1ced89920cee160333571f7ac20005de06fea4fbbf16a2092ffb3e5` |
