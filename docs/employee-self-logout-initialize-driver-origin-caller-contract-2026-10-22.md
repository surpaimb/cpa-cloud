# Initialize driver implementation: caller-only delivery

Status: independent bounded implementation contract, 2026-10-22 (Asia/Shanghai).
Base is open/Draft [PR114](https://github.com/surpaimb/cpa-cloud/pull/114), exact
head `f32e8482da98878c2c3e5c553f11f498a05c7771`, sole parent red PR113
`9239deb589d57096c53fa7c124b80db6cf111bba`, base tree
`7ac77bb6bf0af8eded485f82529265e009f5eb6d`. This contract precedes code.
Read the [coverage/origin contract](employee-self-logout-code512-coverage-driver-origin-contract-2026-10-22.md)
and [owner-only contract](employee-self-logout-first-app-owner-only-contract-2026-10-22.md).

## Exact six-file authority

Only three added files: this contract, private
`internal/service/initialize_driver_origin_caller_20261022.go` and its `_test.go`.
Only three locally modified files: `internal/service/config.go`,
`internal/service/self_logout_confirmation_20261021_test.go` and
`internal/service/expiry_invocation_owner_20261022_test.go`.
A seventh file or an edit to app/worker/store/driver/financial/Gate4, dependencies,
workflows, old documents/PR bodies or monitors is a stop, not extra authority.

The sole result is a private caller-held driver implementation reference from
the original selected Initialize store's public Driver getter. It is delivered
once, only after the original caller receives actual Initialize error=nil.
**Nothing is handed to Open.** No opener, Conn generation, callCtx/pass/native
Tx/raw cut, physical-release or BUSY causal proof is implemented or claimed.

## Actual source and separate facts

The original fixture's existing typed selections are unchanged: AuthBoundaries
parent first Open and confirmation-query-error leaf first Open. Init acquisition
has a separate private role/type mapped explicitly from those same selections;
NativeAbort/post-commit-report and all other callers remain unselected. No t.Name,
stack/address/log lookup, global/current plan, Config/HTTP option or registry hook.

The existing Initialize wrapper and WithConfig public signatures remain error-only
and unchanged. The selected fixture passes a private Init startup carrier only
to its original Initialize call. WithConfig unwraps it at entry and restores the
exact original ctx before validation/IO/SQL. No Value/deadline/cancellation is
added, read for attribution or inherited by a worker. Existing expiryOpenBinding,
two-return observer extraction and subsequent f.open startup stay unchanged.

One non-reusable Init attempt capability is created only at that actual selected
WithConfig entry. Preallocated plan storage is not an attempt or success. After
the original openStore succeeds and original `defer s.close()` is installed,
first validate/bind that actual store and DB, then evaluate `s.db.Driver()` once
inside the selected guard. A nil/default path must not evaluate a getter as an
unconditional helper argument, allocate a carrier/attempt or emit an event.
A plan already invalid before carrier binding gets no new carrier/attempt/getter;
a later rejected attempt retains earlier immutable facts but produces no new
delivery. All old business paths still execute normally.

The getter result is only a candidate. All initialization SQL inside openStore
already happened; it cannot be retrospectively registered or observed. The
Initialize DB is not an App store/Conn or the raw observer DB. The fixed registered
implementation may be the same singleton across DBs; driver equality cannot
authenticate a DB role. Store/DB/capability checks and the exact source callsite
are separately required. No new driver instance, DB/Conn/Open/Ping/Raw/SQL/probe.

After all original initialization work, immediately before the original sole
return nil, a selected producer may record only **nil-result-prepared**. It is
not an actual return/cleanup-success event. No new defer, named error result,
recover or panic interception may announce success from a default nil error.
The original return/error expressions and original close defer remain intact.

The original fixture caller saves the actual Initialize error, associates it
immediately after that call, then performs its original error check and f.open.
Only that source-owned post-return site can issue the caller-return witness and
consume a delivery. Actual error=nil plus matching captured/prepared attempt is
required. A panic/non-return never reaches the caller association; a non-nil
error, pending or missing prepared/capture cannot deliver. Original error values
are neither replaced nor formatted/stored by diagnostics.

This trust boundary includes the audited callee/caller source sites. It is not
an API that authenticates arbitrary same-package code merely supplying nil.
Early association, a forged nil instead of the real local error, missing actual
Initialize invocation or a different getter receiver must fail source controls.

## Finite one-shot state and output

One plan, one entry capability, one store/DB binding, one candidate, one original
caller return and one delivery; at most seven immutable value events. No growth,
wrap, overwrite or reusable ID. Missing/foreign/duplicate/late/wrong store or DB,
nil/typed-nil driver, failed Initialize, unknown state or repeated consumption
latches rejection. Prior events and the historical first-delivery fact cannot
be rewritten; no later nil result may promote a failed attempt.

A private delivery contains the capability and implementation reference, not
an App or connection. Only fixed enums/booleans/counts are reported, never driver
formatting, addresses, handles, DSN/path, raw errors, identities or secrets.
Standard reflection may only classify a typed-nil interface; it may not inspect
private fields, retrieve registry objects or obtain/route the implementation.
The actual source remains the public getter above. No driver method is invoked
by a delivery/view or synthetic negative control.

The original legitimate second Open keeps its old unselected path. This batch
cannot feed a receipt, carrier or driver into any Open; duplicate use of the
current caller-delivery API is rejected. A later Open-entry handoff is another
batch, not a second consumer silently granted here.

Selected metadata synchronization/allocation/retention may perturb scheduling;
there is no zero-effect claim. The original defer's executed method boundary
does not prove physical release or ignored Close error=nil. No added wait,
timeout, cancellation, retry, worker or cleanup repair is permitted.

## Exact inherited guards

The old owner guard currently rejects every extra fixture edit. Its only allowed
adjustment is a call to an exact new whitelist projection: reconstruct the full
PR113 fixture (normalizing only numeric snapshot anchors), then run the unchanged
old owner stripping/hash and three source-mutant audits. Never refresh the old
App/worker/fixture hashes or weaken state-value assertions. New controls also
reconstruct the entire prior owner-guard file except that explicit call.

Gate4's file remains byte-unchanged. Keep the original snapshot rollback defer /
BEGIN_RETURN / cut / first-query adjacency; closedRaw route index2 and prepare
index3; Boundaries prequery allocation index1 and expectedAfter/arm/clean order.
The old fixture still has exactly one arm/route/cut/report/newSlogPrequery call.
Its 22 real snapshot callsites keep function/cut order and only migrate numeric
lines mechanically; its 54 ordered raw literals keep hash
`ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860`.

The original cleanup literal still ends flushSegments, flushExpirySQL,
prequery.report. Register any independent Init report before the existing owner
report registration so LIFO runs old cleanup, old owner report, then Init report.
Do not use a new direct Cleanup literal in the old fixture or borrow old reports
as origin proof. A later case failure does not rewrite an earlier genuine Init
delivery, but neither that delivery nor a report may turn the case/old red green.

## Directed evidence and stop rules

First add a compilable source-contract test against unchanged prior source and
retain its expected missing-source red, not a compile failure. Then implement
only this contract and run only new named directed tests plus necessary service
build/vet/format/link/diff checks. No old TestSelfLogout case function or old
PR113/PR111 workflow is manually run or rerun; no parallel Windows SQLite suite.

The two new real-seam subtests may each invoke the **existing newSlog21 constructor**
once with one existing typed selection. They exercise that constructor's original
Initialize-to-original-caller seam and its original normal lifetimes. They do
not invoke the historical AuthBoundaries/confirmation-query-error test bodies,
install triggers, call reopen/clean/raw, add an acquisition DB or reproduce old
failures. Existing constructor f.open remains unchanged. Old owner labels from
normal cleanup are not claimed as historical-case acceptance by these new tests.

Pure controls may retain the already captured references/store witnesses from
those two serial constructor executions; they must not call Driver again or
operate on those DBs. Synthetic foreign-store/DB, same-singleton, failed-return,
pending/prepared, missing/duplicate/late/second-consumption and typed-nil controls
test classification, not real failed Initialize/I/O/physical-close reproduction.
No additional failed Initialize/probe lifetime may be introduced for coverage.
Default helper allocation/context preservation and independent state-value/zero
expectations must be checked without a new DB. Static in-memory mutants must
reject early getter, wrong receiver, early caller association, forged nil and
lost old owner guards. No mutated production source is executed.

Pure helper positives alone are insufficient. If the real caller seam cannot
be exercised by the new directed constructor tests without manually running an
old case or adding a probe lifetime, stop. First unexpected directed-test or
natural red freezes source/output; do not hot patch, retry or run a later case
to obtain green. No mirror implementation replaces actual source validation.

Submit one non-force commit and a stacked Draft PR; on the first SSH host-identity
refusal, do not retry/change trust/credentials/remote. The root-authorized connector
fallback must reproduce exact parent/tree/six blobs/modes or stop. Freeze after
handoff; root independently pins natural CI and original E/F formal same-head
read-only review. CI cannot issue that review or any product/production GO.

## Permanent boundaries and provenance

PR113 Code512 run39637428191 remains natural attempt1 FAILURE on its own exact
head/base: race1 job119351725681 NativeAbort INSTALL/AFTER_APP_CLOSE and race0
job119351725717 confirmation-query-error plus post-commit-report REOPEN/
AFTER_APP_CLOSE. AFTER_APP_CLOSE is only the cut. The shared schema-query/
SQLite5/BUSY/fixed failure label does not prove a shared lock/driver/worker cause
or PR113 introduction/repair. Confirmation's NOT_ACCEPTED_PRIOR_FAILURE is
neither an owner PASS nor rejection. Historical checks23=18success3skipped2failure.

PR111 Code511 run39616103565, its AuthBoundaries FINAL_CLEAN and confirmation
REOPEN two reds, F PR111 static P3, E PR108 P3, PR108 Code509, PR106 Code508,
PR100 Code501, PR102-105 and the earlier local SPQI_INVALID_INSTALLATION red
remain unresolved. PR114 docs-only and PR113 owner-source narrow GO do not grant
this implementation or close Gate1/complete Gate2/Gate3. All old source/documents/
PR bodies and evidence remain frozen; no old green substitutes for this batch.

New contract/source/tests are independently authored from the root's six-file
authority, project source and frozen contracts, not archived CPA/CLIProxyAPI/
Sub2API or copied library implementations. No strict clean-room claim or new
dependency. Existing Go1.26.9/modernc sqlite v1.46.2 graph/licensing remains;
public API references are [DB.Driver](https://pkg.go.dev/database/sql#DB.Driver)
and [Context](https://pkg.go.dev/context). No library implementation is ported.
Keep Draft / real migration, dynamic-case, product and production NO-GO.

## Actual batch ledger

One serial non-cached Windows Go1.26.9 batch was performed. Raw output artifacts
remain outside the repository; no source/output was patched after a test failure.

- Tests-first: the new source control compiled against prior source and failed
  exactly INIT_ORIGIN_SOURCE_MISSING, exit1, stderr empty. No old case ran.
- Final directed command selected only TestInitializeDriverOriginCaller20261022
  names, with -p=1 -parallel=1 -count=1 -failfast. All 42 names (5 parents +37
  children) ran/passed once, 0fail/skip/duplicate/unpaired names, stderr empty.
  Run events for old TestSelfLogout case names: zero.
- Two serial new constructor subtests exercised the original real Initialize
  call and original caller association, one per existing typed selection, with
  the first constructor's original cleanup completed before the next began.
  Both produced INIT_ORIGIN_REAL_INITIALIZE_TO_CALLER_CONFIRMED and fixed
  caller-delivery tags. Distinct original Init stores had the same implementation;
  foreign-store/DB controls still rejected. No Driver getter/method was called by
  those metadata controls and no extra acquisition lifetime was constructed.
- The pending/prepared/failed/foreign/duplicate/late/unknown/typed-nil/capacity
  negatives are synthetic metadata/classification controls, not actual failed
  Initialize, I/O injection, physical-close or old-red reproductions. Old owner
  cleanup labels are constructor observations, not old-case execution/acceptance.
- Eight new static source mutations and the three inherited owner source
  mutations were rejected in memory. The unchanged Gate4 AST guard passed;
  actual 22 numeric anchors moved mechanically +5 and the 54 raw literals retain
  their frozen hash. Prior config, full fixture and owner guard projections match
  their exact old baselines, rather than refreshed hashes.
- Service vet/build exit0 with empty stderr, gofmt -l empty, two relative links
  valid and diff/check/six-file scope verified. No full-repository or local race
  suite, manual old case/workflow, production/user credential/database or browser
  exercise. The two authorized constructor tests did use real synthetic SQLite
  initialization/App lifetimes; they are not described as DB-free tests.

| Preserved raw JSONL | SHA256 |
| --- | --- |
| Tests-first expected source red | `27d14b38fe14c0ace1dbb9f1b0245c5a0c0425421c17e178966fed55628b9b01` |
| One directed command | `1f72d861edc4c60d50a44fb9218da3bd875e75245fe0d230e9ba9e32b3f69f14` |

These facts stop at Init-to-caller delivery. New natural CI and formal review
must use the actual new submitted head; no terminal green or GO is predeclared.
