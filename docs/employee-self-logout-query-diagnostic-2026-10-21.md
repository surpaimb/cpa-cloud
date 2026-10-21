# Self logout snapshot-error diagnostics — 2026-10-21

Independent **diagnostic-only** batch; not a product fix or continuation commit
on PR102. Frozen remote PR102 head is
`f6f6e2d55991010f6c04fc1a7fb96410b83312d8`, base PR101
`ac51026c816d12c4b0bdf088cab88bfb131f7d33`.

## Authorized baseline / two-file scope

The exact remote Git object was unavailable locally and public HTTPS fetch was
unavailable. The coordinator explicitly authorized a new isolated worktree from
local `77317ed66eb09be0ab671dfe152d3bf8b5b9f67e`: same sole parent PR101 and
complete tree `6709a129e90f1200689f17d05422bbcb13b41951` as remote PR102. This
metadata SHA is **not** the remote baseline. A future remote diagnostic commit
must have exact remote PR102 as its **sole parent**, using connector transport,
and have the same complete candidate tree / two changed blobs /100644 modes as
the local candidate. Read back exact identity before opening a new Draft PR.

Only this new dated contract and direct instrumentation of existing
`internal/service/self_logout_confirmation_20261021_test.go` may change. Product
handler, original contract, all other source/tests, schemas, dependencies,
workflows and old isolated worktrees/evidence remain frozen. No third file in the
candidate. No trust/credential/remote configuration change or another HTTPS fetch.

Independently authored from the coordinator's bounded diagnostic specification
and this project's frozen test. No reference-project code is used; prior research
precludes a strict clean-room claim. Existing SQLite dependency/license unchanged.
Primary API references checked 2026-10-21:
[errors.Is/As](https://pkg.go.dev/errors#Is),
[runtime.CallersFrames](https://pkg.go.dev/runtime#CallersFrames),
[SQLite driver Error.Code](https://pkg.go.dev/modernc.org/sqlite#Error.Code).

## Immutable first-red record

PR102 remains open/Draft. Its natural pull_request attempt1 Code #504
run39530063577 ended FAILURE. First failed job118993703420 race-service(1):
`TestSelfLogout20261021NativeAbort`, original file:699,
`SLOG21_UNEXPECTED_SNAPSHOT_QUERY`. Failed job118993703456 race-service(0):
`TestSelfLogout20261021Boundaries/rows-error` and `/verifier-predicate`, original
file:781, same tag. These are not the registered Stage A expected red.

Complete terminal checks:23 =18 success /2 failure /3 conditional skipped /
0 pending /0 other. Skipped did not execute. Other VM/native green does not cover
these failures. No data-race detector report was observed; root cause is unknown.
Original E/F formal exact-head read-only P1/P2/P3=0/0/0 is source/controlled
synthetic narrow GO only; overall CI acceptance and production remain NO-GO.
The existing PR102 terminal body, its A red/B40-pass history, earlier unexpected
`SLOG_PROTECTED_CHANGED` stop and separate23-pass clock diagnostic are not edited
or reclassified by this batch.

## Direct same-path observation, not a mirror

Instrument the actual `slog21Read` / `slog21Snapshot` used by the original named
tests. Do not add a mirror fixture/test as proof of those paths. Keep all other
existing function bodies, fixture and old oracle text exact. Snapshot callsites
remain exact; a finite frozen source-callsite lookup uses CallersFrames solely
to select a registered cut enum. Runtime file/path/function names/line numbers
are not output. Unmapped locations produce UNKNOWN, not a guessed phase.

Register cuts for existing controls before/after, close boundary, before/after
Open, trigger DDL before/after, DELETE before/after, post-response durable
verification and post-reopen target, cleanup and each existing test baseline.
The exact source lookup is frozen with the final source and covered by pure
mapping unit controls. Its purpose is observation only; it never chooses SQL,
assertions, cleanup, connections, retries or success/failure.

Keep the original same10-second snapshot context, SQL text/order/count,
read-only Tx, connection/pool configuration, row/Tx close positions, guards,
returned errors and failfast semantics. No sleeps, larger budget, retries, new
connections, cancellation changes or environment repair. Bind a previously
discarded Scan/Commit error only to classify it; the original nil/non-nil predicate
and fixed failure tag remain. Commit's short-circuit guard remains identical.

## Fixed diagnostic grammar

Only original error branches emit `SLOG21_SNAPSHOT_DIAGNOSTIC`, followed by finite
fields: `cut`, `query`, `op`, `context`, `error`, `sqlite`, `snapshot_elapsed`,
`query_elapsed`, `cleanup`, `primary`. No uncontrolled error formatting, SQL,
object/column names, arguments, Cookie/CSRF, identity, row values/hash or paths.
In particular, no diagnostic code calls an error's Error method.

Query enums: NONE, OBJECT_ENUMERATION, COLUMN_DISCOVERY, COLUMN_DECLARATION,
TYPED_ROWS, UNKNOWN. Operation enums cover actual BEGIN/QUERY_CONTEXT/COLUMNS/
WIDTH/SCAN/VALUE_TYPE/SQLITE_TYPE/ROWS_ERR/ROWS_CLOSE/OBJECT_TYPE/COLUMN_METADATA/
COMMIT/ROLLBACK. Runtime context is sampled before cancellation or cleanup:
NONE, DEADLINE, CANCELLED, OTHER. Error categories use errors.Is and errors.As
with the concrete existing SQLite driver Error type: NONE, CONTEXT_DEADLINE,
CONTEXT_CANCELLED, TX_DONE, CONN_DONE, SQLITE, UNKNOWN.

Only SQLite result codes NONE, UNKNOWN,1,4,5,6,9,10,19,261,517,773,262,518,4106
are rendered; no arbitrary integer or driver message. Elapsed buckets use only
INVALID, LT1MS,1MS_LT100MS,100MS_LT1S,1S_LT10S,10S_LT60S,GE60S.
Every enum has a fixed UNKNOWN fallback for an unmapped/invalid enum; no numeric
or string input is rendered through that fallback.

Capture Rows.Err and its context **before** the original explicit Close. Capture
Close context before calling it; separately record its returned error without
overwriting the earlier sample. Deferred Rows.Close and Tx.Rollback remain at
their original deferred positions and are observed separately. Expected
ErrTxDone from deferred rollback is not promoted to a new error. Previously
ignored cleanup returns are not made a new assertion or allowed to erase a
primary error; any unexpected cleanup diagnostic blocks delivery even if the
original test's pass/fail status was unchanged. Original UNKNOWN errors still
reach their unchanged fixed failure branch.

Classification only narrows an observation, not a root cause. Observer CPU/time
sampling has overhead; the original10-second limit is not adjusted for it.

## Single frozen local validation / publication gate

Before running, freeze both file contents, product/original contract/dependency
hashes, exact full-name manifest, source lookup and arguments. One noncached
Go1.26.8 Windows serial directed invocation only, with original10-second internal
snapshot limit and `-count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m -failfast
-json`. Select the original self-logout cases (including all three naturally red
names), the same relevant frozen regressions and pure added classifier/formatter/
lookup canary controls in this same file. No parallel whole-repository SQLite run.

Pure controls include wrapped deadline/cancellation, nil/unknown/SQL sentinels,
SQLite-code whitelist/bucket boundaries, mixed primary/cleanup precedence and
formatter shape. Synthetic canary and Error-method-panic values must never be
rendered. They are not native database failures. No failure is turned into PASS.

First unexpected local red, unknown/error diagnostic, leak, capture/cleanup
failure, scope/identity drift: stop and retain evidence, no hot patch, rerun,
changed runner or timeout. Only complete local PASS permits format/link/diff/scope
checks and one commit/controlled connector transport/new isolated Draft PR. No
post-PR source changes or manual workflow rerun/dispatch. Root owns natural CI and
original E/F formal independent new-head review. A green new run says only that
this attempt did not reproduce; it does not close the old cause or issue GO.

No real DB/backup/credential/Key/session, port8787, official client, UI, main-tree
WIP, other PR or frozen evidence mutation. Keep Draft / real migration and
production NO-GO.
