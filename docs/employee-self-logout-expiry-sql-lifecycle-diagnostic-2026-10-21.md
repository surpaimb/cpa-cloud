# Expiry-worker application SQL lifecycle diagnostic (2026-10-21)

## Independent source, frozen baseline and six-file boundary

This new bounded diagnostic is independently authored from this project's frozen
implementation, owned tests and the coordinator's specification. It is not a
PR105 hotfix, shutdown repair, strict clean-room claim or restoration of product
GO. Repository licensing continues to apply; no new dependency/protocol/license
is introduced. Existing modernc SQLite dependencies retain their own licenses.

Remote baseline: Draft [PR105](https://github.com/surpaimb/cpa-cloud/pull/105),
head `259a5d69a70b7596583bf4d27179024d494cb615`, sole parent/base PR104
`a2ddc1fa00789817f791bafd5062a2fbb31e6b21`, complete tree
`d83a3e73631e8bc74ee982488aa8f566e3458227`. Authorized local same-tree fallback
`f884df7df08b75eea333f32bd8baff9e7971a383` has different parent
`909fd72198f21e6564d2d22a33bb8e15e0ecab6b`; identities are not equated.
New remote delivery must have sole parent equal to exact PR105 head, and exact
local candidate whole tree and six blobs/modes. Otherwise stop before delivery.

Only this new contract and these five existing files may change:

- [app.go](../internal/service/app.go): private startup binding only.
- [expiry worker](../internal/service/subscription_expiry_worker.go).
- [subscription_period.go](../internal/financial/subscription_period.go): only
  ExpireDueSubscriptions plus the necessary finite internal bridge/classifiers.
- [owned service test](../internal/service/self_logout_confirmation_20261021_test.go).
- [owned financial test](../internal/financial/subscription_period_test.go): only
  appended pure observer/context/classification/sanitization controls.

No store, driver, second worker, schema, dependency, workflow, old contract or
old PR body changes. No copied fixture, new SQL/connection, wait/retry,
busy_timeout, early Rollback, forced Close or GC. All old SQL, 10-second snapshot
contexts, connection settings, oracle/body assertions and failure labels remain.
Only mechanically relocate the existing exact source-site numeric map.

PR102/103/104 red history and PR105 Code507/run39551936173 remain immutable red.
Its race(0)/job119062904183, race(1)/119062904185 and race(3)/119062904225 each
failed the original SLOG21_UNEXPECTED_SNAPSHOT_QUERY. Callback/Close RETURN and
logical NONZERO Stats did not identify a lock owner or physical Close completion.
The new description must reference their exact URLs/SHAs, not embed PR105's
64,811-character body. Old local green/source-only E/F narrow GO cannot wash red.

## Binding and immutable real lifecycle

Each test App gets a distinct default-nil private sink, supplied before Open.
A service-private startup context carrier exposes no diagnostic Value; Open
extracts it and restores the exact original context at entry, before any old
initialization. No startup binding is forwarded into initialization or a second
worker. Worker construction copies the sink before launching its goroutine.
No product Config, public HTTP endpoint, global registry, string key, reflection
lookup or DB-pointer lookup is introduced.

The cross-package financial observer types/binder are a **new internal Go API**,
not claimed to be wholly private. Payloads contain only the finite scalar fields
below, no raw error, string, SQL, DB/Tx/Rows pointer or business values. Only a
private typed key attaches an immutable scope to the original passCtx using
WithValue. Nil sink returns that exact original context (including nil) without
wrapping. Done/Deadline/Err/Cause and existing Values must be unchanged.

Keep original Background -> WithCancel worker parent, immediate first pass,
one-second ticker, each five-second pass timeout, original stop/select and
cancel propagation. The Open context is never the worker parent. Only the real
expiry call receives the observed passCtx; count/error remain product-ignored.

Keep each original BeginTx, QueryContext, Scan, Rows.Err, Rows.Close, at most100
conditional UPDATE/RowsAffected, Commit and original-position single deferred
Rollback call/count/order/error mapping. Empty batches still Commit. Early
Scan/validation branches retain their existing explicit Close and ignored close
error; no extra Rows.Err or Close is added. Capture actual returned errors before
their original mapping. No financial function RETURN is recorded before its
deferred Rollback: the worker PASS RETURN is after the call actually returned.
ErrTxDone does not prove an internal automatic rollback/driver release finished.

Keep ticker.Stop before finished.Done in original defer order. Replace the
original Done defer only with a wrapper whose last action is that original Done;
emit DONE_READY before it, never record after Done or add a wait for logging.

## Frozen payload, events and two-stream grammar

Payload: Pass uint16, Item uint8, and uint8 enums Phase, Edge, Error, SQLite,
Context, Elapsed, Count. No raw timestamp or address is rendered. Pass IDs start
at1 and increase without wrap (saturation produces duplicate-invalid evidence);
Item is1..100 only for Scan/validation/UPDATE/RowsAffected, otherwise0. Pass0 is
reserved for binding/start/control/done. Count is NA/ZERO/ONE/MANY/INVALID.

Phases: BIND, START, PASS, STOP, CANCEL, WAIT, DONE_READY, BEGIN, QUERY, SCAN,
VALIDATE, ROWS_ERR, ROWS_CLOSE, EXEC, ROWS_AFFECTED, COMMIT, ROLLBACK.
Edges: POINT/ENTER/RETURN. BIND/START/DONE_READY/VALIDATE are POINT only.
Every other phase is an actual ENTER/RETURN pair; only PASS RETURN is actual
financial-call return, not a return-value-selection point inside that function.

Worker stream: START, one or more numbered passes, DONE_READY. Each pass is:

1. PASS ENTER; BEGIN pair. Begin failure goes directly to PASS RETURN.
2. Successful Begin: QUERY pair. Query failure goes to original ROLLBACK pair.
3. Successful Query: zero..100 SCAN pairs. Scan error or VALIDATE failure takes
   the original ROWS_CLOSE pair then ROLLBACK. Normal iteration end takes the
   original ROWS_ERR pair then ROWS_CLOSE pair, in that evaluation order.
4. If those succeeded: one EXEC then ROWS_AFFECTED pair for each scanned item;
   original failure/count mismatch returns through ROLLBACK. No item IDs leak.
5. All successful items (including zero): COMMIT pair then original deferred
   ROLLBACK pair, regardless of Commit result. PASS RETURN then STOP pair.

Cancellation/ErrTxDone/OTHER are possible recorded branches, not automatic new
business failures. Registered OTHER is honest classification, not success;
unregistered enum values or unrecognized SQLite codes are invalid evidence.

Control stream: BIND POINT, CANCEL ENTER/RETURN, WAIT ENTER/RETURN. These and
worker/financial events are **legally concurrent**, not one global total order.
Use separate fixed buffers, each single-writer, with only per-lane nonblocking
reentry latches. BIND must precede START; DONE_READY must precede WAIT RETURN via
the existing WaitGroup. Validate those flags without adding synchronization
waits. Allow cancel before START, cancel/ticker competition, multiple passes and
passes after cancel. Do not require a fixed pass count or time-derived ordering.

Errors: NONE/CANCELLED/DEADLINE/TX_DONE/CONN_DONE/SQLITE/OTHER. Context:
NONE/CANCELLED/DEADLINE/OTHER, sampled separately before cleanup changes it.
Use only errors.Is and a direct concrete SQLite error whitelist, never Error().
Wrapped/non-whitelisted underlying errors remain OTHER, not invented SQL codes.
SQLite codes: NONE, UNKNOWN,1,4,5,6,9,10,19,261,517,773,262,518,4106.
UNKNOWN is retained but invalidates the diagnostic gate. Elapsed: NA for
POINT/ENTER; RETURN buckets LT1MS/1MS_LT100MS/100MS_LT1S/1S_LT10S/10S_LT60S/GE60S;
negative/unknown timing is invalid. These timings include instrumentation cost.

Per App capacities are frozen at **4096 worker events and32 control events**,
never overwrite or grow. Finite record scalar storage is at most16 bytes per
record by field layout; buffers plus counters/atomic state are bounded by72KiB
per App. Fixture census is eight Apps (at most576KiB buffer/state allowance plus
small census metadata). Each delayed rendered record is at most256 ASCII bytes,
so diagnostic text payload is at most1,056,768 bytes per App. Framework/log/GC
overheads are not claimed to have an exact whole-process heap bound.

Callback only records finite fields and atomic invalid/bind/done flags. No
testing.T, Fatal/FailNow/panic, formatting/logging, SQL/Conn/Ping, wait/retry,
forced cleanup or product mutation. Buffer overflow only latches failure and
returns; original worker/SQL/Close continue. After original Wait and the entire
old observer/oracle/cleanup path, validate and emit at the original fixture's
final cleanup. Old first-failure tags retain priority. Zero/missing binding or
records, missing/duplicate events, impossible per-lane order, unsupported enums,
overflow/reentry/census failure seal this batch; never raise limits and rerun.

## Pre-registered verification and stopping rule

Before execution freeze six source hashes, complete test names and commands in
external evidence. Candidate is immutable once the first test starts.

One Windows Go1.26.8 serial noncached pure-control/compile invocation:

```text
go test ./internal/financial ./internal/service -run '^TestExpirySQL(Observation|Recorder)Pure20261021$' -count=1 -p=1 -parallel=1 -mod=readonly -timeout=5m -failfast -json
```

Financial pure parent TestExpirySQLObservationPure20261021 has context-equivalence,
error-classes, sqlite-whitelist, finite-values, synthetic-canary. Service pure
parent TestExpirySQLRecorderPure20261021 has startup-binding, legal-interleaving,
multiple-passes, missing-zero, overflow-latch, illegal-sequence, finite-format.
These14 full names include2 parents/12 children. They perform no DB/fixture,
worker start or new SQL/connection; synthetic classifiers are not DB validation.

Only after pure PASS, at most one original53-name directed Windows acceptance:

```text
go test ./internal/service -run '^(TestSelfLogout20261021.*|TestSelfLogoutSnapshotErrorDiagnostic20261021)$' -count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m -failfast -json
```

All original53 names/assertions (10 parents/43 children) remain; each must
run/pass once with0fail/skip/incomplete/unexpected/parse error, known exit0,
stderr empty and unchanged frozen hashes. Original44 App fixtures and1188
Close-segment/352 handoff records are retained. New SQL pass totals are not
pre-guessed; every one of44 distinct bound Apps must pass its finite grammar.
The self fixtures mostly have empty batches: do not claim real Scan/UPDATE/error
branch coverage from them or from the pure controls. No local race/Linux or
whole-suite/physical I/O fault/lock-owner/root-cause/security-completeness claim.

First unexpected pure/compile/local acceptance or natural CI red stops this
batch immediately, retaining evidence; no hot patch, extra test, rerun, dispatch,
commit or publication after failure. Ordinary pre-test static format/link/scope
inspection precedes this freeze; no post-test source change. No sleep or runner
replacement to obtain green. A green run means only that attempt did not recur.

After full local PASS only: one local commit and controlled non-force connector
transport, verify remote sole parent/tree/six blobs/modes and create new Draft
base PR105. Report real initial natural pull_request CI; root owns terminal CI
and original E/F exact-head formal independent read-only review. Do not retry
100-cap attachments or touch other automations/threads. No real DB/backup/Key/
config/session/password/client/8787/UI/ACL/main WIP or archived implementation.
No Ready/merge/tag/package/deploy; real migration and production NO-GO remain.
