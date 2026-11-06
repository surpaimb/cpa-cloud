# Self logout Close / observer handoff diagnostics — 2026-10-21

Independent diagnostic-only batch, not a PR103 follow-up or a product repair.
Frozen remote PR103 head is `53d0bd9e76065a2872f45586c79ff59b2e116c43`,
with base PR102 `f6f6e2d55991010f6c04fc1a7fb96410b83312d8`.

## Baseline, provenance and two-file boundary

The remote commit object is absent locally. The coordinator authorized the
same-tree local candidate `099cc4d343c3dd6b47ede591f731106c854ee7e3` as the
isolated working baseline. Its parent is `77317ed66eb09be0ab671dfe152d3bf8b5b9f67e`;
remote PR103's sole parent is PR102 above. These are different SHAs and parents,
not interchangeable commit identities. Both complete trees are
`174a9969a66d34a50c86e9d1a549069f3fab9c0c`; the existing test is blob
`59ea45858b2aac756742defaa18de86b667d7435`, mode 100644.

Only this new dated contract and
`internal/service/self_logout_confirmation_20261021_test.go` may change.
Product, old contracts, other tests, SQL/schema, dependencies and workflows are
frozen. New source is independently authored from this project's frozen fixture
and the coordinator's bounded specification; no reference-project implementation
is used and no strict clean-room claim is made. Dependency licenses are unchanged.
Primary API references checked 2026-10-21:
[DB.Stats](https://pkg.go.dev/database/sql#DB.Stats),
[DB.Close](https://pkg.go.dev/database/sql#DB.Close),
[CallersFrames](https://pkg.go.dev/runtime#CallersFrames),
[SQLite close semantics](https://www.sqlite.org/c3ref/close.html).

## Permanent red history and non-conclusions

PR103 remains open/Draft. Natural pull_request attempt1 Code #505 run39537799158
failed all four race-service shards. The 13 primary records (shards 0/1/2/3:
9/2/1/1) all classify AFTER_APP_CLOSE / OBJECT_ENUMERATION / QUERY_CONTEXT /
context NONE / SQLITE 5 / snapshot 1MS_LT100MS / query LT1MS / cleanup false.
Complete terminal checks are 23 = 16 success / 4 failure / 3 conditional skipped;
0 pending/other. Skipped is not execution. Native #9, independent VM #24 and CLI
#19 succeeded but do not cover those red cases. Lock holder and product root cause
remain unknown. Exact-head E/F 0/0/0 is pure-diagnostic-source narrow GO only;
PR103 CI acceptance, real migration and production remain NO-GO.

PR102's earlier unclassified red, the earlier stopped work, separate 23-PASS
clock diagnostic, tests-first A red and B local 40-PASS evidence stay unchanged.
This batch cannot retrospectively establish their unique cause or restore GO.

## Passive observation of the actual existing handoff

Instrument the existing fixture's App.Close -> existing raw sql.DB handle ->
existing BeginTx -> first existing object-enumeration QueryContext path.
Do not substitute a mirror fixture. Keep original test/oracle bodies, SQL bytes,
SQL count/order, pool parameters, ten-second snapshot context, assertions, fixed
failure tags and existing Close/cleanup positions. Existing PR103 diagnostics stay
intact; mechanically relocate their finite callsite line registrations as needed.

Select INSTALL / REOPEN / FINAL_CLEAN from the three existing fixture callers via
a finite exact-function lookup. Do not output runtime function/file/line/stack
information. Any unmapped runtime stage, unknown classification, invalid bucket,
missing or out-of-order handoff observation fails with a new fixed tag; it never
becomes a PASS. A Begin failure records enumeration NOT_ATTEMPTED and retains the
original Begin failure. A query failure retains the original query diagnostic
and original failure tag, then the existing cleanup still runs.

Observe Close enter/return and its returned error's fixed category/SQLite code
and elapsed bucket; old App and raw-pool DB.Stats OpenConnections/InUse/Idle as
ABSENT / ZERO / NONZERO / UNKNOWN, never exact counts; existing raw-handle create
and Close-call/Close-success booleans; Begin enter/return; first enumeration result.
The existing raw Close calls (including cleanup and idempotent repeated calls)
are observed in place, not added or forced. Preserve primary versus cleanup as
separate fields. No new SQL, Ping, connection, sleep, retry, timeout/busy handler,
checkpoint, GC, journal setting, product change or driver hook is permitted.

These are logical Go handle observations, not physical driver-close completion,
all-locks-released evidence or lock-owner identity. DB.Stats takes its ordinary
short internal lock; sampling/time/log calls add observer overhead. Do not hold a
new lock across the original operations or claim zero timing perturbation.

## Finite output / safe controls

Only SLOG21_CLOSE_HANDOFF_DIAGNOSTIC plus finite stage/event/result/error/SQLite/
elapsed/pool-bucket/boolean/cleanup fields may be emitted. Reuse PR103's safe
errors.Is/As classification and code/elapsed whitelist; never call Error or
String on uncontrolled input. Non-timed events use NA. Enumeration NOT_ATTEMPTED
is not success. Unknown labels are rendered UNKNOWN and fail the runtime gate.
Do not emit SQL, object/column names, paths, cookies/CSRF, identity, data/hash,
raw errors, argv or stacks. Pure controls exercise buckets, safe formatting,
unknown/missing/order rejection and error-Error-method-panic canaries without a
new database/connection. These are classifier controls, not native fault causes.

## One frozen local validation and independent delivery

Before execution, freeze both files, baseline/product/dependency identity, source
callsite registrations, the full-name manifest and exact arguments. The sole
Windows Go1.26.8 noncached serial directed test invocation is:

`go test ./internal/service -run '^(TestSelfLogout20261021.*|TestSelfLogoutSnapshotErrorDiagnostic20261021)$' -count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m -failfast -json`

The unchanged original cases and new pure handoff controls run together. Capture
complete stdout/stderr privately outside the tracked candidate and validate every
expected full name runs/passes once, with no fail/skip/incomplete/unexpected name,
unknown/missing event, leak or parser failure. No whole-repository test/race run,
second directed run or post-test patch. First unexpected red seals this candidate
and stops; no hotfix, retry, changed budget or runner repair. Formatting, document
links, diff and two-file/protected-source checks are separately read-only gates.

Only a complete PASS permits one local commit and controlled non-force connector
delivery. The remote commit must have exact remote PR103 as its sole parent and
match the complete local candidate tree / both changed blobs / 100644 modes.
Then open a new Draft with base PR103; do not update PR103 source/body/history.
If that transport identity cannot be guaranteed, stop. Root owns re-pinning,
natural CI and original E/F independent same-new-head formal read-only review.
New green means only non-reproduction in that attempt, not a cause, repair or GO.

No real DB/backup/credentials/Key/session, port8787, official client, UI, main-tree
WIP or other PR mutation. No manual workflow rerun/dispatch or release action.
Keep Draft / real migration and production NO-GO.
