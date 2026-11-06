# Red-sample coverage and real Tx/pass binding contract

Status: new independent **docs-only** contract, 2026-10-21. Only this new
document is authorized. Every inherited source, test, contract, dependency,
workflow, old PR body and historical red remains unchanged. This document
implements neither B's identity bridge nor C's driver/opener integration.
All four gates below are **NOT READY**; publication does not clear them.

Frozen source/base is [PR108](https://github.com/surpaimb/cpa-cloud/pull/108),
head `439829eae2ca112e8056e44c6d69394c47ff26da`, sole parent PR107
`b78c85b5c1ff1399f7d10e101040205746423346`, full tree
`d28d16c26d2fcbc4473f81c081796a5cd01d37c4`. PR108 remains open/Draft and red;
this is not a repair, replacement acceptance run or restored product GO.

The [fixed driver-boundary contract](employee-self-logout-driver-boundary-observation-contract-2026-10-21.md)
and [collector A contract](employee-self-logout-collector-algorithm-contract-2026-10-21.md)
remain unchanged. Interpretation is limited to Go1.26.9 and modernc sqlite
v1.46.2; no toolchain/dependency/workflow pin is changed by this document.

Sections: [coverage](#coverage-matrix), [four gates](#four-unready-gates),
[stop rules](#stop-before-implementation), [acceptance](#acceptance-and-evidence-limits).

## Coverage matrix

The historical sample is Code509, natural pull_request/attempt1
[run39582468254](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254),
failed [race-service(0), job119163201036](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254/job/119163201036),
on exactly the PR108 head/base above. Full checks were
**23=19 success+3 skipped+1 failure+0 pending+0 other**. Skipped is not execution.

Seven failing leaf cases are distinguished below. Phase facts come from that
fixed red evidence; App creation ordinal is checked against the fixed fixture
call graph, not inferred from output order, pool counts, addresses or elapsed
buckets. A fixture-local App ordinal is not a native Conn identity or lock owner.

| Historical leaf case | Failed handoff stage | Closed App in the fixed flow | Coverage decision |
| --- | --- | --- | --- |
| Boundaries/nil-result | FINAL_CLEAN | later App, second successful Open | matrix only; not selected |
| Boundaries/rows-error | FINAL_CLEAN | later App, second successful Open | matrix only; not selected |
| Boundaries/zero-rows | FINAL_CLEAN | later App, second successful Open | matrix only; not selected |
| Boundaries/employee-predicate | FINAL_CLEAN | later App, second successful Open | matrix only; not selected |
| Boundaries/pre-commit-refusal | FINAL_CLEAN | later App, second successful Open | **sole future observation candidate** |
| TestSelfLogout20261021DeferredFK | FINAL_CLEAN | later App, second successful Open | matrix only; not selected |
| Boundaries/confirmation-query-error | REOPEN | first App/donor; failure precedes a successful later Open | **excluded blind spot** |

Boundaries names have parent `TestSelfLogout20261021Boundaries`. Source anchors:
[fixture first Open](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L269),
[reopen](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L416),
[expectedAfter](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L726),
[clean](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L666),
[boundary cases](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L802)
and [DeferredFK](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L878).

For the sole candidate, the pre-commit-refusal DELETE/hook executes in the
**first App**. expectedAfter later reopens; the final clean closes that later
App. Future observation would concern that later App's expiry tail, not the
original logout Tx or an observation of its refusal hook. This restriction
does not explain the historical failure and does not eliminate the donor's
unobserved tail as a possible concurrent actor.

Only the exact leaf `TestSelfLogout20261021Boundaries/pre-commit-refusal` may
be proposed later. Selecting the entire Boundaries table, DeferredFK, all seven
cases or a replacement fixture would be an expansion requiring new authority.
No old case is changed or executed by this document. A new run, if separately
authorized, would be new evidence, never retroactive instrumentation of Code509.

The first App remains a donor only: its already existing DB may supply the
registered sqlite singleton through DB.Driver without a probe DB/connection
or extra lifetime. No retroactive first-App coverage is claimed. The REOPEN
confirmation-query-error sample is therefore not covered by this candidate;
adding an earlier Initialize/probe/Open to cover it is forbidden.

## Four unready gates

### Gate 1: actual pass source and a separately reviewed read-only API

The fixed [expiry worker](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/subscription_expiry_worker.go#L35)
creates the pass context and calls financial.WithExpiryObservation with its
observer/pass. [ExpireDueSubscriptions](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/financial/subscription_period.go#L165)
passes that actual context to DB.BeginTx. However the
[binding key and reader](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/financial/subscription_period.go#L386)
are financial-private; the exported writer is not an exported reader.
The existing observation callback carries finite scalars, not ctx/Tx/DB.

There is no existing public-to-service API that supplies this binding. A future
financial-package read-only accessor is only a separately reviewed cross-package
API candidate. It must expose bounded identity metadata/presence only, not SQL,
raw error, observer-function identity, DB/Tx handles, credentials or arbitrary
values; it must not add context/deadline/cancellation, call the observer, execute
SQL or perform a probe. This document authorizes no accessor file or edit.

A missing marker is not an invented pass0. Legitimately unmarked non-expiry
transactions must remain outside expiry attribution. A required marked-pass
association cannot be filled from a mutable current-pass variable, last event,
worker count, SQL matching, goroutine/stack/address or print order. Existing
pass counters can exceed A's128 limit; over-limit must not clip, wrap or reuse.

### Gate 2: trusted App scope and actual Conn generation

A pass-only reader is insufficient to claim mechanical App-scope validation.
App ownership must come from an explicitly approved per-App source, and Conn
generation from the actual delegated connection construction, with no global
pointer/function registry. The fixed worker-to-Commercial(app.store.db) source
relationship is useful evidence, not an invented scope token or a dynamic check
against a foreign App's marked context. The design must state which provenance
it proves and reject ambiguity; it cannot silently substitute source assumptions
for a claimed runtime scope check.

The per-App opener/Conn wrapper is still C, not implemented or authorized here.
DB.Driver on the donor supplies origin only; it does not replace an existing
driverConn, attach a pass or install a wrapper. No extra DB, db.Conn acquisition,
Raw/Ping/query, different sqlite.Driver instance or first-App extension is a
fallback. No existing old-test/worker/Initialize change is approved to obtain
the scope or route an opener. If such a change is required, stop for direction.

### Gate 3: real successful native Tx, proxy and sql.Tx correspondence

Fixed sqlite supports ConnBeginTx. Go's
[ctxDriverBegin](https://github.com/golang/go/blob/go1.26.9/src/database/sql/ctxutil.go#L97)
passes the actual ctx/options to it, and
[beginDC](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L1899)
stores the returned driver.Tx in the private sql.Tx.txi field before starting
awaitDone. Public sql.Tx has no Raw/DriverTx getter; Conn.Raw yields a Conn,
not this transaction. A's synthetic Tx numbers are not this correspondence.

The only proposed capture point is a future approved ConnBeginTx boundary:

1. Retain the original native Conn receiver and its approved App/Conn identity.
2. Obtain the marked pass from the actual call context through the still-unready
   bridge, without changing ctx/options or choosing the current/latest pass.
3. Delegate the original call exactly once. Only a non-nil native Tx with nil
   error may acquire a fresh bounded Tx ID; failure does not manufacture a Tx.
   Unexpected nil/error combinations are evidence failures, not repair authority.
4. An immutable proxy would hold **that returned native object** and its proven
   tuple before exposing the proxy to Go. native Tx, proxy Tx and public sql.Tx
   are three different objects, not identical pointers. Registration is not a
   serialization of native addresses or a new raw-handle observation API.
5. Go's [DB.BeginTx retry path](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L1869)
   may make failed attempts on different Conn generations. Preserve that original
   behavior, add no retry, and distinguish attempted begins from the single
   successful logical result. Duplicate successful tuple registration, foreign
   scope, wrong Conn or unresolved identity cannot be resolved by picking one.

The native [Tx constructor](https://gitlab.com/cznic/sqlite/-/blob/v1.46.2/tx.go#L19)
ties its Tx to the actual Conn. It is not publicly recoverable from sql.Tx
after the fact. Whether the new identity bridge can prove this whole chain
under the frozen source constraints remains unready until separately reviewed;
this document is not C implementation authority.

Application events and driver events must remain separate. Go can set Tx.done
before driver Rollback returns. Application Rollback=ErrTxDone can also follow
Commit/another finalizer; it is not proof that automatic rollback occurred or
completed. The fixed native Commit may execute private cleanup rollback on an
error; that is not necessarily a proxy Tx.Rollback event.

An actual driver Rollback RETURN would report that delegated method boundary
only. Same-Conn Close RETURN would use App/Conn generation only, never a guessed
Tx/pass. Matching Conn generation does not make a pass the cause of the close
or owner of a physical SQLite lock. Do not require one Close per Rollback,
or assume MaxOpenConns1 means one physical Conn throughout the App lifetime.
ResetSession/Validator and all other fixed optional interfaces remain subject
to PR107's preservation gate; loss of those interfaces changes cancellation
disposition rather than merely observing it.

### Gate 4: the actual pre-query cut installation point

The intended query cut is **after the original raw observer BeginTx succeeds,
before its first OBJECT_ENUMERATION QueryContext invocation**. This is the
original [snapshot](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L148)
and [slog21Read entry](https://github.com/surpaimb/cpa-cloud/blob/439829eae2ca112e8056e44c6d69394c47ff26da/internal/service/self_logout_confirmation_20261021_test.go#L48).
The current diagnostic capture follows QueryContext's return. Neither its
AFTER_APP_CLOSE label nor later log emission is an existing collector pre-query
callback. No currently approved installation route at this point exists.

Wrapping only the selected App does not automatically intercept the independent
raw observer DB/Tx. Do not wrap that path, change old tests, introduce SQL-text
recognition or add another observer DB merely to obtain this cut. A cut at App
Close return or before raw Begin is earlier and must be labelled as such; it
cannot be renamed the exact query-entry cut. A post-error cut cannot backfill it.
If a forbidden edit/acquisition is needed to install the cut, stop before code.

A future cut, only if separately authorized, follows A: one reservation high
water, RETURN phase before ENTER phase, copy only READY payload to an immutable
bounded view, no flush/poll/wait/drain. C8192/K128 and finite App16/Conn32/Tx128/
pass128 ceilings are not evidence of real-case adequacy. RESERVED/OPEN and
POST_SEAL_UNKNOWN remain legitimate; late publication does not alter old cuts.
Missing-at-cut RETURN is not lost evidence or proof of no native return.

The raw schema query's context and the cancelled expiry pass context are distinct
actors. Neither matching error vocabulary nor proximity joins their calls.
Driver Rollback RETURN still precedes possible standard-library release work;
driver Conn.Close RETURN precedes bookkeeping. No cut, TxDone, Wait, App.Close
return, old pool counter or ready row proves putConn completion, physical lock
release, lock owner or SQLite5/BUSY causality.

## Stop before implementation

All four gates remain unready. This docs-only batch needs none of the forbidden
actions and does not grant them to a next batch. An implementation proposal
must stop if it requires old-test/worker/Initialize edits, extra DB/probe/Open,
SQL/Ping, new waiting/retry/GC, timeout/cancellation changes, a global hook or
registry, wider samples, donor/first-App coverage, or unsupported type/interface
substitution. An inability to state honest provenance or install the exact cut
is a blocker, not permission to redefine coverage or use a synthetic substitute.

No future code whitelist is approved here. A separately scoped read-only API
proposal is not an already approved implementation, real identity proof or
promise that C can meet its constraints. If zero-change current APIs are required
to produce real Tx/pass association and the exact cut now, the answer is NO-GO.

## Acceptance and evidence limits

This document is acceptable only if the seven rows and sole candidate remain
exact, the first-App exclusion is explicit, all four missing gates stay visible,
and native/proxy/public Tx identities and application/driver/Conn events are
separate. It must not assert tested behavior or a completed identity bridge.

Before any later implementation is authorized, original independent E/F review
must address the actual new head/base and, at minimum, require proof/negative
controls for missing/foreign scope, wrong Conn, failed/retried Begin, duplicate
registration, over-limit IDs, unmarked non-expiry calls, cancellation racing
successful Begin, application TxDone without driver return, same-Conn Close
without pass attribution, and late publication across the exact approved cut.
Those are future acceptance requirements, not tests performed by this document.

The current batch verifies relative links/heading and source-line anchors plus
one-added-file git diff/check only. It runs no Go test/build/race, SQL, App,
service, fixture process or browser. Its natural CI must be recorded on its own
head: zero/path-filtered means not triggered/not executed, never green; skipped
is not execution. Native compatibility, if triggered, is inherited regression,
not B/C validation. No workflow change/dispatch/rerun. Any first unexpected
natural red stops work; root handles same-head terminal CI and formal review.

## Historical reds and review boundaries

Do not embed old PR bodies or long logs. Preserve these fixed references:

| Historical evidence | Retained status |
| --- | --- |
| [PR108](https://github.com/surpaimb/cpa-cloud/pull/108) head439829eae2ca112e8056e44c6d69394c47ff26da /[Code509](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254) | FAILURE;23=19success/3skipped/1failure; this seven-leaf sample, overall NO-GO |
| [PR106](https://github.com/surpaimb/cpa-cloud/pull/106) head41fa9e3b35b981b23727d7d9d725aead065c462e /[Code508](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012) | FAILURE;23=18success/3skipped/2failure; original two race reds unresolved |
| [PR100 old Code501](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209), headf393797dbfa062a983bb09ef2c8183a0a571581b | historical natural red unresolved; no restored product narrow GO |
| [PR102](https://github.com/surpaimb/cpa-cloud/pull/102), [PR103](https://github.com/surpaimb/cpa-cloud/pull/103), [PR104](https://github.com/surpaimb/cpa-cloud/pull/104), [PR105](https://github.com/surpaimb/cpa-cloud/pull/105) | original natural reds/source/history remain linked in the frozen earlier contracts; not repaired |

Root-confirmed original E/F static/source-only narrow reviews of A retain their
scope. E's original P3 is not closed or reclassified by this document. No absent
finding text/count is invented; the unchanged PR108 red body retains that status.
Static review and A's local synthetic controls do not make PR108 overall green,
validate B/C or establish a cause. This new document still needs its own original
E/F formal same-head read-only review after root re-pins its actual head/base.

Keep Draft / PR108 overall NO-GO / real migration and production NO-GO. No code,
test, dependency, workflow, product wiring, old body, main WIP, credential/data,
Ready, merge, tag, package or deployment operation is authorized.

## Provenance

Independently authored from this project's fixed specifications/source and
public Go/sqlite APIs, reviewed2026-10-21; not copied archived CPA/CLIProxyAPI/
Sub2API implementation or tests. Prior research prevents a strict clean-room
claim. No code, dependency or library implementation is introduced. Go keeps
its BSD-style license and modernc sqlite its BSD-3-Clause license. Public source
links above support static API/call-order inspection only, not new execution.
