# Gate2 first-App invocation owner only

Status: independently authored bounded implementation contract, 2026-10-22
(Asia/Shanghai). Base is frozen open/Draft [PR112](https://github.com/surpaimb/cpa-cloud/pull/112),
head `07565d9a1ae44b93c4097659ddc08ef85854b1ee`, sole parent red PR111
`c1b8606e81494f86666e57865050b707f7cc7733`. This contract precedes code.
Read the [first-App decision contract](employee-self-logout-first-app-source-owner-contract-2026-10-21.md)
and [Gate4 installation contract](employee-self-logout-prequery-cut-installation-contract-2026-10-21.md).

## Authority and hard boundary

Only six files are authorized: this added contract, added
`internal/service/expiry_invocation_owner_20261022.go`, added
`internal/service/expiry_invocation_owner_20261022_test.go`, and local changes in
`app.go`, `subscription_expiry_worker.go`, and the original
`self_logout_confirmation_20261021_test.go`, all three under `internal/service/`.
A seventh file or a wider existing-file edit is a stop, not implicit authority.
No edits to PR112/111, store/config, financial, driver, collector A, Gate4's
installation-control test, dependencies, workflows, SQL, or old assertions.

This is the narrower owner-only phase selected by F. It does **not** bind an
owner into callCtx, witness a pass, identify Conn/raw/native/public Tx, install
a first-App raw cut, capture a driver, add a DB/probe/global hook, or repair BUSY.
No extra Initialize/Open/fixture, SQL, wait, timeout, retry or worker is allowed.
The existing financial pass reader stays unattributed and unchanged.

## Exactly two explicit selections

The entire original `TestSelfLogout20261021AuthBoundaries` parent selects its
original first Open, whose original raw handoff is FINAL_CLEAN. Its ten auth
children do not independently select ten Apps. Only the original
`TestSelfLogout20261021Boundaries/confirmation-query-error` leaf selects its
original first Open, whose original handoff is REOPEN before any second Open.
No other Boundaries leaf is selected. A later legitimate second Open is
unselected, not a duplicate owner-claim failure.

A typed constructor argument establishes a fixture-local one-attempt plan
before its original first Open. It preallocates storage, not an invocation or
successful App. The Boundaries selector is an explicit mapping of its existing
scenario input, never testing.T.Name, stack, address, log order or a later cut.
All old one-argument constructor calls remain valid and unselected.

## Separate immutable facts, actual production path

1. At the actual selected Open entry, before the original observer extraction,
   configuration validation or store construction, claim the plan exactly once
   and allocate a private non-zero-size invocation capability. No fixture or
   synthetic observer pre-mints that capability. Reusing the plan is rejected.
2. The actual App literal copies that capability. Immediately after actual App
   construction, associate exactly that App. Copy it from the actual App into
   the actual expiry worker literal; associate exactly that constructed worker.
3. The original worker goroutine separately records that it actually started.
   This says nothing about a native pass/transaction or successful Open return.
4. Only the original f.open caller, immediately after actual Open returns its
   actual `(a, err)`, records a separate return association. Successful means
   non-nil App, nil error, and exact constructed-object/source match. An error
   is Failed; nil/nil or inconsistent objects are Unknown/rejected, never success.

The immutable private capability, not an address-derived identifier, is the
source. Internal object equality only checks the actual App/worker relationship;
no address, handle, pointer, path, credential or raw error enters an output view.
There is no global/current-owner registry, random source, reusable numeric owner
ID, observer-closure identity or successful App synthesized by fixture ordinal.

Worker-start-before-caller-return and caller-return-before-worker-start are both
legal. Each event copies the return state **at that event**, initially Pending.
Later success appends a distinct fact; it never rewrites earlier Pending events
or claims they were already associated with a successful returned App. Failed
Open can preserve earlier construction facts without gaining success. Original
returns, errors and cleanup remain primary and are not replaced by diagnostics.

## Finite record and fail-closed qualification

One selected fixture owns one plan, one invocation, one actual App, one actual
worker, one caller-return record and at most five immutable events: entry, App
construction, worker construction, goroutine start and caller return. No growth,
wrapping, overwrite or tuning after a failure. A short plan-local mutex protects
only fixed metadata; no SQL, callback, testing/log output, sleep, channel wait,
timer or external code is called while held. This synchronization/allocation can
perturb scheduling and is not a zero-disturbance or lock-owner claim.

Missing/foreign capability, invalid selection, duplicate claim/construction/
start/return, wrong App/worker, impossible order or exhausted capacity latches
rejection. Rejection cannot be cleared by a later valid fact. Earlier immutable
records remain unchanged. Pending/Failed/Unknown are not accepted as success.
The exported-to-tests value view contains only fixed enums, booleans, counts and
five value records, never live objects. State values have independent explicit
numeric expectations and zero/distinctness controls; not a self-referential
iota comparison. This does not fix or erase F's separate PR111 P3.

Nil/unselected helpers return the original startup context and allocate/publish
nothing. No context Value is introduced. The selected route adds a private field
to the **existing** expiryOpenBinding carrier. Existing
takeExpiryOpenObserver retains its two-return signature and original context
identity; normal initialization receives the exact original context. No owner
data enters callCtx or changes any context passed to business calls.

## Frozen order and old test guard

Worker parent remains `context.WithCancel(context.Background())`. Preserve
passCtx, five-second timeout, observer binding, pass counter/saturation, ticker,
commercial call and all cancellation/stop/Wait/Close sequencing. No new wait
forces a start event to occur. Existing Close/join precedes final inspection;
missing start remains missing and fails qualification.

The original fixture keeps all 22 snapshot callsites and their function/cut
order. Only physical line-number anchors migrate mechanically. It keeps all
54 backtick literals in order, joined-LF UTF-8 SHA256
`ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860`.
No new backtick literal, snapshot or line directive is added to that file.

Gate4's guard remains byte-unchanged and must pass: Boundaries prequery allocation
is body statement1; selection is nested in the original first constructor
expression, not a preceding statement. closedRaw route stays statement2 and
prepareSegments statement3. slog21Snapshot rollback defer / BEGIN_RETURN / cut /
first OBJECT_ENUMERATION query retain exact adjacency. Existing arm/route/cut/
report/newSlogPrequery calls each occur exactly once in the old file.

The constructor's old cleanup literal still ends exactly with flushSegments,
flushExpirySQL, prequery.report. A separate owner cleanup is registered through
the new test helper **before** that old cleanup, so LIFO reports owner last.
It neither flushes the old collectors nor substitutes for their completion.
If the test/old cleanup has failed, report only a fixed NOT_ACCEPTED_PRIOR_FAILURE
summary: no owner acceptance or claim that old cleanup succeeded.

Source controls reconstruct the frozen prior app/worker/fixture after deleting
only enumerated owner additions and mechanically normalizing anchor numbers.
Gate4 additionally checks the actual new numeric anchors. Static in-memory
mutations drop App-to-worker propagation, insert premature success, or replace
Background parent and must be rejected. They are source mutations, not DB tests.

## Validation and stop protocol

First execute a new source-contract test against unchanged old source and retain
the expected fixed missing-entry red. Then implement only this owner phase.
Pure controls cover both start/return orders, immutable historical state,
Pending/Failed/Unknown, missing/foreign/duplicate capability, object mismatch,
invalid selection/capacity, default context/no-allocation route, carrier
compatibility, later success and unselected second Open. Pure fabricated events
are labelled synthetic and cannot stand in for the two actual selected paths.

Run appropriate directed controls, formatting, build/vet/link/diff checks. Run
the original AuthBoundaries parent and confirmation-query-error leaf locally
at most once each, serially. First unexpected red freezes the entire candidate
and raw output immediately; no hot patch, rerun, later case, commit/push or green
substitution. Record the unrun case as NOT_RUN. Do not run concurrent Windows
SQLite whole-repository tests. No new SQL/service/browser exercise is authorized.

Only after permitted local validation succeeds: one non-force commit/push and a
new stacked Draft PR. Report exact URL/head/base/tree, six-file manifest, tests
actually performed and natural CI initial state, then freeze. No dispatch/rerun
or waiting for a later green after the first natural red. Root separately pins
the head/base, monitors natural CI and arranges original E/F same-head formal
independent read-only review. CI and this implementation do not issue a GO.

## Historical failures and limits

PR111 Code511 run39616103565 stays FAILURE: AuthBoundaries parent FINAL_CLEAN
race2 job119277518410 and confirmation-query-error REOPEN race0 job119277517930,
both OBJECT_ENUMERATION/QUERY_CONTEXT/SQLite5 with
SLOG21_UNEXPECTED_SNAPSHOT_QUERY. Its full checks remain historical
23=18success+3skipped+2failure. No common cause, exoneration or BUSY fix is inferred.
PR108 Code509, PR106 Code508, PR100 Code501 and PR102-105 reds remain unresolved.
E PR108 P3, F PR111 P3, the earlier local SPQI_INVALID_INSTALLATION red and all
their source/output remain frozen. PR110 Gate4 empty-H0 narrow GO and PR112
docs-only narrow GO do not approve owner/pass/Conn/cut or production behavior.

This batch has no physical-lock attribution, complete tail guarantee, new
driver transparency, native I/O injection, real account/credential/DB test,
GOV-02 repair or migration. All product/production/real migration gates remain
NO-GO. Keep Draft. No Ready/merge/tag/package/deploy or other PR/body/monitor work.

## Provenance and dependencies

New contract/code/tests are independently authored from the root's 2026-10-22
six-file authority and this project's frozen contracts/source, not copied from
CPA/CLIProxyAPI/Sub2API, archived tests, SDKs or library implementations. Prior
exposure means no strict clean-room claim. No new dependency or protocol client;
Go1.26.9 and modernc SQLite v1.46.2 remain pinned. Existing licenses are retained:
Go BSD-3-Clause and SQLite module BSD-style. Public API references reviewed
2026-10-22: [Go context](https://pkg.go.dev/context) and
[Go sync](https://pkg.go.dev/sync). Only API contracts were consulted; no library
implementation was ported. Existing repository licensing applies to new source.

## Actual evidence ledger

One Windows Go1.26.9 serial, non-cached batch, with artifact files outside the
repository. No whole-repository or local race test was run.

- Tests-first: the new source-contract test compiled against unchanged prior
  App/worker/fixture source and failed exactly FAPP_SOURCE_OPEN_ENTRY_MISSING,
  exit1, stderr empty. This was an intentional missing-wiring oracle, not an
  execution of either old red or a DB mutation.
- Directed controls: 90 distinct named run/pass pairs, each once, 0fail/skip,
  no incomplete/unpaired/duplicate names, stderr empty. This is 31 new names
  (7 parents +24 children), 51 unchanged Gate4 control names and 8 unchanged
  expiry-recorder pure names. The three in-memory source mutations were rejected;
  no mutated source or DB was executed.
- Original AuthBoundaries parent: exactly once, 11 names (1 parent +10 children)
  allPASS, 0fail/skip, stderr empty, fixed
  FAPP_OWNER_ONLY_ACCEPTED_AUTH_FIRST_FINAL_CLEAN.
- Original Boundaries/confirmation-query-error: exactly once, 2 names (the parent
  and selected leaf) allPASS, 0fail/skip, stderr empty, fixed
  FAPP_OWNER_ONLY_ACCEPTED_CONFIRM_FIRST_REOPEN. Its later legitimate Open
  remained unselected. No other Boundaries leaf was run locally by this batch.
- Directed service vet/build exit0 with empty stderr; gofmt -l empty; two relative
  document links valid; diff/check and six-file whitelist verified. Actual
  22 snapshot anchors moved mechanically by +4 lines; 54 raw literals retain
  the frozen hash above. The original Gate4 source guard itself was unchanged.

Raw JSONL SHA256 evidence (not copied into production or repository files):

| One execution | SHA256 |
| --- | --- |
| Expected missing-entry red | `367f7de131b637f05000156c787ab42a24f681cde2ec2d1440c87477ed677122` |
| Directed controls | `adcea40a758ae515f705664d7b0b2fad1d34f3b10655af6c8d6a7a30cb9dbe63` |
| Original AuthBoundaries once | `bb7feadace68d8eb020ee1b4159799bf7d8cb7522c0e9cec4f5861d50801c33d` |
| Original confirmation leaf once | `3dbe4a230ab634cec6f63d0604a013a51602bc1a3f84bd00492ab7dc59362f21` |

The two actual successes establish only this invocation/App/worker/start/return
association under these executions. The synthetic order controls establish the
metadata state machine, not real native pass/Conn/Tx provenance, physical lock
release, a BUSY cause or a repair. Historical reds and both P3 findings remain.
Natural CI has not been substituted for local evidence or predeclared green;
record the actual new commit's initial workflows/checks separately in its PR.
Root owns terminal CI verification and original E/F same-head formal review.
