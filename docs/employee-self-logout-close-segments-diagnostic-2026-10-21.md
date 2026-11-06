# Employee self logout: passive App.Close segments (2026-10-21)

## Scope, source and immutable baseline

This is an independently authored diagnostic batch, not a shutdown fix. Its only
sources are this project's frozen implementation and its owned synthetic tests.
No archived implementation, new dependency, protocol, secret or production data
is used. It is not a strict clean-room claim.

The exact remote baseline is Draft PR104 head
`a2ddc1fa00789817f791bafd5062a2fbb31e6b21`, based on PR103
`53d0bd9e76065a2872f45586c79ff59b2e116c43`, tree
`db89d5f32bbdae768c5d87513396c67741428af7`. The isolated local fallback is
`909fd72198f21e6564d2d22a33bb8e15e0ecab6b`, parent
`099cc4d343c3dd6b47ede591f731106c854ee7e3`, with that same whole tree.
The local and remote identities/parents are different, not silently equated.
The frozen app/test blobs are respectively
`894702fbe7197fde307b0e2c9407dcaa48a4e0f4` and
`c2fb2b672062fb25ed001b22813ce2ebd01f9ab3`, both mode `100644`.
A published new commit must have sole parent equal to the exact remote PR104
head and precisely the local candidate tree and three file blobs/modes.

Only this new dated contract, [app.go](../internal/service/app.go), and the
[existing owned test](../internal/service/self_logout_confirmation_20261021_test.go)
may change. Worker, financial, store, driver, schema, dependency, workflow and
configuration files remain frozen. No old PR body is changed. The prior
[handoff contract](employee-self-logout-close-handoff-diagnostic-2026-10-21.md)
and its existing diagnostic/error labels remain intact.

PR102/103/104 natural red history remains red. PR104 Code #506 had two failed
race jobs; its terminal checks were 18 success, 2 failure, 3 skipped. The safe
SQLITE_BUSY classifications and post-Close NONZERO Stats observations do not
identify the physical connection/lock owner, prove the cancellation rollback
tail, or establish a product fix. Prior source-only E/F narrow GO is not CI,
product, migration or production GO. A new green run would only be that run's
non-reproduction, never retrospective repair of old red evidence.

## Private passive callback

Add one private, per-App, default-nil callback with only finite phase/edge enums
and no result. It has no product configuration entry or exported interface.
Production construction never installs it. A small notifier invokes it only
around the actual existing App.Close calls; the shutdown flow is not copied.

The exact phase order is:

1. oneShot
2. expiry
3. backupAutomation
4. backgroundResponses
5. scheduledTests
6. channelMonitors
7. governance
8. proxyTests
9. recovery
10. healthTests
11. accountPool
12. refresh
13. proxyClients
14. store

Edges are only ENTER, RETURN, SKIP_NIL. Preserve each original nil condition,
call order and call count. An existing nonnil segment gets ENTER immediately
before its existing call and RETURN immediately after. An existing nullable nil
segment gets SKIP_NIL and no call. Store remains unconditional: call store.close
exactly once, bind its returned error locally and return that same error without
wrapping, aggregation or early return. The accountPool error remains ignored.
No added defer, recover, forced close, wait, retry, SQL, connection acquisition,
busy_timeout, checkpoint or garbage collection is allowed in App.Close.

RETURN means only the Go method returned. It does not mean a driver physical
close, connection release, lock release or completion of Go-internal async
rollback. Segment records do not observe error results. Existing outer
CLOSE_RETURN retains its original actual safe error classification unchanged.

## Pre-pinned independent fixture expectation

Each successful original/reopened fixture App receives its own recorder and
fixed expected sequence. The original Config enables only employee self service
and uses its existing synthetic local fixture. In this frozen construction,
backgroundResponses alone is nil; the other thirteen segments are present.
Disabled coordinators may still be constructed, so their SKIP_NIL cannot be
inferred from their enable flag. Pin the literal 27-event sequence independently
of actual callback events or App field pointer values: thirteen ENTER/RETURN
pairs plus backgroundResponses SKIP_NIL. Zero events cannot pass.

The recorder has a fixed 32-record buffer, per-phase timestamps, and a bounded
per-fixture census of eight recorders (the frozen maximum is three). It does not
share mutable recorder state between Apps. A nonblocking atomic guard latches
reentry; it never spins or waits. Unknown/missing/duplicate/out-of-order events,
overflow, invalid timing/stage or zero/missing recorder evidence latch invalid.
An overflow does not stop or alter the original shutdown/observer path.

The callback only synchronously records fixed enum values, time buckets and
Stats from the already-owned *sql.DB. It does not accept testing.T or an error,
format/log, Fatal/FailNow/panic, execute SQL, acquire Conn/Ping, wait/retry,
force Close, or mutate product state. Stats itself takes an internal mutex and
can perturb timing; observations are not claimed to be zero-overhead.

The stage is pre-set from the original INSTALL/REOPEN/FINAL_CLEAN observer or
the original final fixture cleanup. The callback never learns stage from SQL.
Only after the original test body/observation path and original fixture Close
have completed does the existing fixture cleanup format records and use Error
for invalid evidence. Original failure labels remain first; no diagnostic gate
cuts off a later original reopen, observer, cleanup or oracle assertion.
Later-registered original raw-handle cleanups keep their original LIFO order.

Output is only the fixed SLOG21_APP_CLOSE_SEGMENT label, finite stage, one of the
fourteen phase labels, finite edge, ZERO/NONZERO/ABSENT/UNKNOWN open/in-use/idle
buckets, existing finite elapsed buckets (NA for ENTER/SKIP_NIL), and cleanup
boolean. Unknown inputs map to fixed UNKNOWN, never raw values. No SQL, paths,
identity, error text, connection IDs or secret/canary values are emitted. A
NONZERO Stats bucket is not a new quiescence assertion or proven lock owner.

## One pre-registered local acceptance

Before starting the single local acceptance, freeze the source hashes and all
selected test names in an external evidence record. Use the already available
Windows Go1.26.8 executable; no environment/dependency repair or race claim.
Exactly one serial, noncached invocation is authorized:

```text
go test ./internal/service -run '^(TestSelfLogout20261021.*|TestSelfLogoutSnapshotErrorDiagnostic20261021)$' -count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m -failfast -json
```

The original 45 selected names (9 parents, 36 children) must remain. Add one
pure diagnostic parent with seven children: fixed-presence,
fixed-capacity-overflow, unknown-duplicate-order, missing-zero, reentry-latch,
per-app-independent, safe-delayed-format. Expected total: 53 names (10 parents,
43 children), each run/pass once, no failure/skip/incomplete/unexpected name.
Pure controls use fixed synthetic records, not a new SQL fixture/connection.

The frozen original scenarios imply 44 successful App constructions/closings:
INSTALL 3, REOPEN 20, FINAL_CLEAN 21. Each must supply its own 27-event sequence:
1,188 segment records in the passing selected invocation. The original handoff
records and their assertions stay unchanged (44 handoffs, 352 records on a
complete passing run). These totals are pinned before the run, not inferred
from returned observations. Original snapshots/SQL, ten-second context,
connection settings, status/body assertions, triggers and all old labels remain.
Mechanically relocate only the existing diagnostic source-site line numbers.

First unexpected local failure, BUSY, parser/name/cleanup/incomplete-evidence
failure seals this candidate: stop, report safe evidence, no hot patch or rerun,
and no commit/publication. A local pass alone does not diagnose the old root
cause. No physical-close, automatic-rollback, ownership or security-completeness
claim follows. No new full-suite, race, Linux or I/O-fault-injection claim.

Before that invocation, ordinary format, source comparison, file-scope, link and
diff checks may correct implementation mistakes. After it starts the candidate
is frozen. No sleep, wait-for-zero, changed timeout, forced cleanup or old oracle
relaxation may be used to obtain green.

## Bounded delivery and boundary

Only after the unique local acceptance passes, make one local commit and use
controlled non-force connector transport with an exact expected-old-head lease.
Verify remote sole parent, full tree, all three blobs/modes and new Draft PR
based on PR104; preserve the entire PR104 terminal body/history verbatim in the
new description. Do not retry/delete old attachments at the 100-item cap.
Report exact identity, manifest, local evidence and new natural CI initial
state. Natural CI is not manually rerun or dispatched. Root owns fresh exact
pinning and the original E/F independent same-head formal read-only review.

No real DB, backup, Key, config, session, password, official client, port8787,
UI/ACL, main-tree WIP or archived implementation is touched. No new agents or
other-chat messages, Ready/merge/tag/package/deploy. Keep Draft and real
migration/production NO-GO; this batch has no authority to fix shutdown policy.
