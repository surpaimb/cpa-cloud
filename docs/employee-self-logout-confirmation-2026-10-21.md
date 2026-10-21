# Current self logout confirmation — independent 2026-10-21 batch

Exact base PR101 `ac51026c816d12c4b0bdf088cab88bfb131f7d33`, parent PR99
`8280d3df2c59cd2280d39b25e788f05d39aab4ef`. This is a new three-file batch;
the stopped `self-logout-confirmation` batch and the separate snapshot diagnostic
remain immutable evidence, not helpers imported by these tests.

Only this dated contract, new self-contained
`internal/service/self_logout_confirmation_20261021_test.go`, and narrowly scoped
`selfLogout` / operation-private helpers in `internal/service/self_service.go`
may change. Shared auth/routes, old tests/timeouts, DDL, dependencies and workflows
remain frozen. Independently authored from project specifications and public
SQLite/Go semantics, not ported reference-project code. Prior research precludes
a strict clean-room claim. No dependency or license change.

## Pre-registered A → B gate

**No product edit before A.** Freeze the oracle, helpers, contract, original source
hashes, exact names and arguments. Run it once, noncached and serial, on old PR101.
Use two independent owned synthetic databases: native IGNORE and AFTER-reinsert.
Do not fail after the first *expected* old observation; only after both complete
witnesses and successful cleanup emit the one registered expected-red label:
`SELF_LOGOUT_20261021_OLD_204_DURABLE_REPLAY`.

A permits B only if native exit is known1, the exact registered test ran/failed
once, both complete-case/cleanup booleans are true, and there is no other failure,
skip, timeout, parse error, incomplete capture or unknown evidence. Any fixture,
DDL, snapshot, HTTP, Open/Close, cleanup, mixed or partial outcome is unexpected:
stop, preserve evidence, no hot patch, rerun or runner/timeout change. The older
`SLOG_PROTECTED_CHANGED` failure is never retrospectively accepted as A's red.

### A's independent phase windows

Initialize real Apps and real HTTP admin/employee enrollment/login. Retain the
original Cookie manually (no CookieJar). Nonempty controls include three self
sessions across two employees, an admin session, a real admin-created model Key,
password credentials and a completed synthetic accounting record.

Each snapshot reads schema, column declarations/order, values and SQLite
`typeof` through **one bounded read-only transaction**. Close all Rows and the
transaction before HTTP/Open. Exclude only the actual reserved `sqlite_` prefix
using a literal prefix check, not `LIKE 'sqlite_%'` wildcard filtering. Every
user object/table remains in scope. Runtime kinds and SQLite types are retained
privately; NULL, integers, float bits, text and blob remain distinct.

1. Controls-only, Close-only and DDL-only windows are strictly row-invariant,
   including the governance clock and all six current-session cells. DDL may add
   exactly the one registered persistent target-limited trigger; all existing
   objects and rows/columns/types must remain exact.
2. Before/after **each actual Open**, compare complete typed snapshots. Only the
   existing clock singleton's unique time cell may change: exact two columns,
   declarations/schema, one INTEGER singleton1 row, TEXT/string time in canonical
   nine-fraction UTC-Z, nondecreasing and within the measured Open window. Do not
   omit the clock table or any other row/column/type.
3. After Open and successful controls establish a fresh DELETE-only baseline.
   Old DELETE must return204, empty body, exactly one accurate clearing Cookie;
   the complete six-column current row remains exact. All other protected facts,
   schema and **clock** are strictly unchanged in this window.
4. The manually held original Cookie returns200 with the correct employee both
   live and after another actual reopen. Sibling/other employee/admin/Key controls
   also authenticate correctly, with their protected facts unchanged in separate
   strict control windows. Finish owned response/server/App/raw-DB cleanup.

Only both complete counterexamples authorize the expected red. No trigger or
snapshot rejection can stand in for it. The same frozen oracle is retained for
B: both trigger cases must instead be fixed503/zero Set-Cookie with target and
all protected facts preserved.

## B's narrow handler rule

Keep `requireSelf(..., true)` and its inherited `admission.RLock`. No upgrade,
writer or recursive reader lock. Preserve auth priority, Origin, X-Self-Request,
CSRF, default-off, no-store, cookie flags/path, empty body and ignored body/query
semantics. Do not add request validation.

Use one request-context transaction and only `tx` SQL while it owns the single
connection. DELETE exact authenticated selector + employeeID + copied stored
verifier. Require nonnil result and RowsAffected exactly1. In that transaction,
independently count by **selector alone** and require zero, including replacements
with a different owner or verifier. Only real successful Commit plus successful
reporting permits unchanged clearing Cookie / empty204.

All failures/uncertainty use existing fixed self503 `storage_unavailable` and
`Request could not be completed.` with **zero Set-Cookie**. No retry or universal
rollback claim. Private request-context adapters cannot erase real SQL/Commit
errors; a real Commit followed by a controlled reporting error may have deleted
the session already. Adapters are synthetic boundaries, not actual I/O failures.
Previously authenticated in-flight requests may finish; this is not a global
in-flight barrier. New authentication after committed deletion rejects the Cookie.

## Frozen final gate

Only after A, append independent B cases and narrowly implement. Freeze final
source/names/arguments before one noncached Windows Go1.26.8 serial directed run:
`-count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m -failfast -json`.

Required: retained dual trigger503, normal durable logout, ABORT, actual deferred
FK Commit refusal followed by same-live-App read/write without connection repair,
pre-Commit refusal vs post-real-Commit reporting error, Begin/result/confirmation
failures, exact tuple predicates, selector-only replacement checks, deterministic
two-already-authenticated204+503 and later front401, plus frozen auth/Origin/CSRF/
default-off and password/sign-out-others concurrency regressions. No sleep guesses.
Every failure is first-failure stopping. Only complete green permits targeted
vet/build, format, links, diff and scope checks; no hot fixes/reruns after red.

Public semantic sources (checked 2026-10-21):
[SQLite RAISE](https://www.sqlite.org/lang_createtrigger.html#the_raise_function),
[SQLite changes](https://www.sqlite.org/c3ref/changes.html),
[Go Tx](https://pkg.go.dev/database/sql#Tx),
[Go RWMutex](https://pkg.go.dev/sync#RWMutex).

## Evidence / delivery boundaries

Runtime output is source-fixed tags, booleans, test names/counts only, never raw
Cookie/CSRF/SQL/body/error/identity/credential facts. Freeze hashes privately.
The independent diagnostic observed controls unchanged / reopen clock-only; it
supports a possible old false-red explanation, not the historical unique cause,
not an old joint witness and not a logout fix.

After full local gates, at most one commit/push/new Draft PR, exact remote PR101
parent/base; read back tree/blob/mode if connector metadata differs. Stop source
changes after PR creation. Root owns natural CI and original E/F same-head review.
No self-issued GO, Ready/merge/tag/package/deploy. No real DB/backup/credential,
port8787, official client, UI, main-tree WIP or other-PR changes. Current-lineage
one-session confirmation is not old-backup anti-resurrection, all in-flight work,
all sessions, Key/password retirement, GOV-02, M1-to-M2 or production acceptance.
Keep Draft / real migration and production NO-GO.
