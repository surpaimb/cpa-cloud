# First-App sample scope and actual source/owner decisions

Status: independent **docs-only decision contract**, 2026-10-21, stacked on
frozen red [PR111](https://github.com/surpaimb/cpa-cloud/pull/111), exact head
`c1b8606e81494f86666e57865050b707f7cc7733`, base/sole parent PR110
`18b00683a3572278b071d1820821a9c00216810d`, complete PR111 tree
`93690ae7d8b9260ddf8df62a31c6b4472b2f777e`. PR111 stays open/Draft and CI NO-GO.
This new batch may add **only this document**, not an implementation or test.
Every inherited file, old PR body, failure and review boundary remains frozen.

Read with the frozen [driver-boundary contract](employee-self-logout-driver-boundary-observation-contract-2026-10-21.md),
[coverage/binding contract](employee-self-logout-red-sample-tx-pass-binding-contract-2026-10-21.md),
[Gate4 installation contract](employee-self-logout-prequery-cut-installation-contract-2026-10-21.md)
and [unattributed reader contract](expiry-observation-pass-reader-contract-2026-10-21.md).
These remain historical authority for their own limited batches. The decisions
below select a future first-App evidence target; they do not silently modify
those contracts or authorize edits to their source/test scope.

## 1. Decision: exactly two first-App samples

The current evidence is natural pull_request/attempt1
[Code511 run39616103565](https://github.com/surpaimb/cpa-cloud/actions/runs/39616103565),
completed/FAILURE on the PR111 head/base above. Full filter=all checks were
**23=18success+3skipped+2failure+0pending+0other**. Skipped was not execution.

| Selected fixed sample | Source-owned lifetime and original raw cut | Fixed red |
| --- | --- | --- |
| Entire `TestSelfLogout20261021AuthBoundaries` parent | One constructor/first Open; its ten authentication subtests share that first App. No reopen occurs. Original final clean closes App1; raw cut is **FINAL_CLEAN** | [race2 job119277518410](https://github.com/surpaimb/cpa-cloud/actions/runs/39616103565/job/119277518410), OBJECT_ENUMERATION /QUERY_CONTEXT /SQLite5, `SLOG21_UNEXPECTED_SNAPSHOT_QUERY` |
| `TestSelfLogout20261021Boundaries/confirmation-query-error` leaf only | expectedAfter enters reopen; closedRaw observes App1 before openRaw could perform the second Open. Original raw cut is **REOPEN** | [race0 job119277517930](https://github.com/surpaimb/cpa-cloud/actions/runs/39616103565/job/119277517930), OBJECT_ENUMERATION /QUERY_CONTEXT /SQLite5, same fixed failure label |

AuthBoundaries is a parent-level sample, not ten independently created Apps or
a guessed failing authentication leaf. FINAL_CLEAN is a handoff stage, **not an
App creation ordinal**. The confirmation-query-error failure precedes any App2
in that fixture. App1/App2 labels here describe the checked source call graph,
not native identity tokens, pointer observations or a cause inferred from logs.

Fixed-source anchors:
[constructor first Open](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L293),
[expectedAfter/reopen](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L732),
[reopen ordering](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L422),
[AuthBoundaries](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L935)
and [confirmation-query-error](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L852).

Decision: future first-App evidence is limited to these two samples. Do not
select the entire Boundaries table, all historical failures, another leaf,
new fixture or extra App. The prior selected second-App pre-commit-refusal
candidate remains a different scope. Second-App Conn evidence covers neither
of the two current first-App reds and cannot be substituted for them.

## 2. Decision: explicit selection before the original first Open

Any later authorized implementation must receive a typed, fixture-local opt-in
before the original fixture's first Open executes. Selection at Open return,
after expectedAfter, by test-name inspection, log order, stack/address, DSN/path,
pool count or nearest event is rejected. No global/current-App registry or
automatic selection of all callers is an alternative.

The existing constructor performs Initialize and its first Open internally;
the current API has no such first-App selection route. A future constructor
argument/carrier or other explicit seam requires a separate bounded source
authorization. This document does not add it. Other fixtures/cases must keep
the exact nil/unselected old path. No extra Initialize/Open/DB/fixture is allowed.
If a selected driver-origin candidate requires earlier access to the existing
Initialize lifetime, opt-in must precede that lifetime too, without adding one.

## 3. Decision: actual Open invocation is the owner source

The selected App owner's source must be the **actual original first Open call
boundary**, not a fixture ordinal relabelled as proof. Source order is:

1. Establish immutable invocation-owner metadata on entry to that actual selected
   Open, before its original store/connection construction can begin.
2. Carry that metadata through the actual App construction and into its actual
   expiry worker before the worker's first pass. No independently synthesized
   context/observer/worker may stand in for that path.
3. Distinguish an Open invocation from a **successfully returned App identity**.
   A failed Open remains unbound to a successful App, even if it constructed
   internal objects before failing. It must not manufacture a successful owner
   or erase those original objects' normal cleanup work.

The fixed [Open entry](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/app.go#L212)
unwraps the existing startup carrier. Store construction occurs at line282,
App construction at299, and the expiry worker starts at434, before Open returns
at437. Post-return fixture registration therefore cannot prove the first pass's
source. A newly stored App field alone cannot cover earlier native connections.

Invocation metadata may identify only that real invocation until a separately
proved successful-App association exists. Any later success fact must be a
distinct immutable association, not a rewrite of earlier payloads/cuts, a replay,
or a retroactive claim that a failed/pending invocation had already been admitted
as a successful App. Do not delay worker startup or wait for Open return to make
the bookkeeping convenient. If this distinction cannot be preserved under the
original ordering, the owner implementation is blocked rather than redefined.

The actual [worker constructor/pass source](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/subscription_expiry_worker.go#L21)
uses a fresh Background-derived parent and the actual app.store.db; its existing
passCtx is then bound by financial.WithExpiryObservation. A future owner carrier
must copy only immutable provenance metadata along that real path. **Do not make
the worker inherit the startup context or its cancellation/deadline**, replace
its parent, or change pass creation, cancellation, timeout or scheduling.

The finite reader returns only unattributed raw pass metadata; it cannot read
trusted App/Conn/native Tx identity, authenticate the public writer's issuer,
or distinguish two owners that use the same number. Observer-function identity,
closure/address matching, last-pass state and source-code proximity are not
runtime ownership checks. The reader's existing F P3 is not fixed by this design.

## 4. Decision: Conn generation comes only from App1's real store connection

The sole proposed Conn source is **the successful original native Driver.Open
that constructs a Conn for the selected App1's actual store DB**. The invocation
source must be available before the first relevant Conn can be produced. A
future generation is attached once to that actual successful returned object,
not to pool statistics, a DB pointer, a test counter or a later Raw acquisition.

The fixed [store opener](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/store.go#L20)
hardcodes sql.Open and performs initialization before returning. The current
interfaces do not expose the required selected per-App opener/source proof.
After-the-fact DB.Driver/Raw cannot retrofit already-created driverConn objects.
Constructing a default DB and discarding it for a wrapped replacement is also
an extra lifetime, not an acceptable zero-extra-DB implementation.

The existing [InitializeWithConfig DB](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/config.go#L90)
is only a possible source of the original registered **driver implementation**
through its actual DB.Driver, during that already-existing lifetime. Such a
driver reference is not App1's Conn, a Conn generation, App identity or a
validated pass. The current Initialize API does not expose that reference;
this document neither captures it nor permits an initialization edit.

No additional acquisition DB/probe/Open/Conn/Ping/query, replacement sqlite
Driver instance, registration/global hook, private unwrapping or new driver
method is authorized. Do not call the acquired driver's Open as an extra probe;
any future delegation must be the original selected DB's own construction.
If the origin cannot be obtained and routed before that construction within a
separately approved whitelist, **Conn registration remains blocked**.

Preserve original inputs, returned objects/errors, failed attempts and interface
shape. Failed native Open does not acquire a successful Conn generation. An
Open invocation that ultimately fails cannot gain successful-App ownership
merely because a native Conn was constructed inside it. No duplicate admission,
reuse, clipping/wrapping, synthetic Conn or guessed association is allowed.
The inherited interface/nontransparency constraints remain prerequisites.

## 5. Decision: raw query actor is separate; two exact cuts are frozen targets

The failing schema query belongs to the **independent raw observer DB/Tx**, not
to the closed App1's store connection or its expiry pass. A future handoff may
carry the closed App's proved source as the object being observed, while the
query actor retains a separate RAW_SCHEMA_OBSERVER role. Same driver instance,
file, number or nearby event cannot turn those roles into one Conn/pass/Tx.

Future cut targets are exactly AuthBoundaries **FINAL_CLEAN** and the selected
confirmation-query-error leaf's **REOPEN**. In each original raw snapshot the
only target is [after raw Begin success](https://github.com/surpaimb/cpa-cloud/blob/c1b8606e81494f86666e57865050b707f7cc7733/internal/service/self_logout_confirmation_20261021_test.go#L169),
after the original rollback defer and BEGIN_RETURN observation, immediately
before the first OBJECT_ENUMERATION QueryContext. Raw Begin failure is
NOT_REACHED; a live-App snapshot is not this raw cut.

These two case routes are **not currently installed** by the second-App Gate4
empty-H0 control. In particular, arming only after expectedAfter returns cannot
cover the REOPEN failure inside expectedAfter. A future first-App selector,
handoff/cut routing and any numerical relocation of the original22 actual
source anchors need their own explicit bounded permission. No change to those
calls/anchors, no23rd snapshot and no cut execution is authorized here.

The cut must not wait, flush, poll, add SQL, retry, seal producer lifetime or
change original errors. Keep query failure primary; missing source/cut is not
backfilled after an error. Metadata/view work can extend the existing raw
transaction window and consume its unchanged10-second budget; not zero effect.
Inherited bounded immutable publication and OPEN/UNKNOWN/POST_SEAL_UNKNOWN
limits remain; a complete tail or physical release cannot be promised.

## 6. Phase order and future acceptance, not implementation permission

The next implementation authorities must be separate and ordered:

1. Actual Open-to-App-to-worker owner source, including failed/pending Open and
   the two explicit first-App selections.
2. Same-owner successful original native Conn generation and exact opener/
   driver-origin preservation, before the first relevant connection.
3. Actual call-context pass plus successful native Tx/proxy/public Tx binding;
   the finite reader alone does not provide this association.
4. The two original raw cuts reading only approved mechanical evidence.

This is an authority/proof dependency order, not a runtime retrofit sequence.
A later separately approved candidate must install all its required sources
before that candidate's original first Open/store construction, never attach
them to objects already created by an earlier evidence batch.

Earlier stages do not claim later-stage completion. A second-App Conn batch
may have separate value, but contributes zero coverage of these two fixed
first-App samples. No historical red is relabelled by a new stage passing.

Future positive evidence must traverse the **original** selected fixture's first
Open and actual worker/pass path. Pure scope helpers or fabricated App events
cannot replace it. Both selected first-App cuts must remain separate; default
and all unselected old paths retain original behavior and lifetimes.

Required negative controls include: missing/foreign owner; same pass under a
different source; failed Open; first pass preceding post-return registration;
owner introduced only after store initialization; duplicate/reused scope or
Conn; failed native Open; wrong DB/Conn; raw actor presented as App/pass; wrong
case/stage; pre-arm/duplicate/missing cut; and loss of an existing driver optional
interface. Broken actual source propagation must fail even if synthetic helper
controls still pass. No filling unknown fields from print order or pool counts.

Stop before code when actual origin/owner cannot be proved, an inherited file
change lacks separate permission, a probe/replacement/global hook/retrofit is
needed, or original SQL/parameters/errors/cancellation/timeouts/retry/waits
must change. Preserve original execution even if evidence recording fails.
Finite-budget adequacy for these newly selected samples is still unproved;
no growth or tuning after a red. Every first unexpected local/natural red
freezes its implementation batch without hot patch, rerun or dispatch.

## 7. What this document does not resolve

It does not supply a scope carrier, Initialize driver capture, selected opener,
native Conn/Tx wrapper, first-App cut installation or proof that their combination
can satisfy all constraints. Likely source surfaces for a later proposal include
App's actual Open/startup carrier, the existing worker's pass binding and the
original fixture/handoff. Store/Initialize source access and any new helper/test
files require separately enumerated authority, not a whitelist granted here.

Even all permitted future method returns cannot identify SQLite's physical lock
owner, prove putConn complete, establish a common BUSY cause, or exonerate/blame
the new reader. Auth requests not entering the logout transaction hook do not
exclude other database activity. Initialize/donor and other unobserved lifetimes
are not erased by selecting App1. No shutdown/credential/GOV-02/product repair.

## 8. Frozen reds, reviews and document-only verification

PR111's two Code511 reds remain terminal and CI NO-GO, with different stages as
recorded above. Its three other natural workflows being green do not override
them. E's PR111 source-only0/0/0 is only limited-reader narrow GO; F's
PR1110/0/1 retains the state-constant distinctness/zero-value P3. Current source
uses iota correctly; the static iota-to-zero false-green risk was not dynamically
executed. No remedy for that finding is implemented or authorized here.

[PR108 Code509](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254),
[PR106 Code508](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012),
[PR100 Code501](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209)
and [PR102](https://github.com/surpaimb/cpa-cloud/pull/102)/
[103](https://github.com/surpaimb/cpa-cloud/pull/103)/
[104](https://github.com/surpaimb/cpa-cloud/pull/104)/
[105](https://github.com/surpaimb/cpa-cloud/pull/105) remain red/unresolved.
E's original PR108 P3 is retained, separately from F's PR111 P3. The earlier
local SPQI_INVALID_INSTALLATION candidate/output remain frozen, as do PR109's
docs-only and PR110's Gate4 empty-H0 source narrow GO. None approves this document
or first-App implementation, and no missing review detail is invented.

This batch verifies document/heading/source anchors, one-added-file scope and
git diff/check only. No Go test/build/vet/race, SQL, App/service/fixture or browser
execution. Natural CI must be recorded for the actual new head; zero/path-filtered
is **not triggered/not executed**, not green, and skipped is not execution. Do
not alter workflow filters or dispatch/rerun them. Root re-pins the new head/base
and arranges original E/F formal same-head independent read-only document review.
Keep Draft / all unresolved dynamic gates / product and production NO-GO.
No Ready/merge/tag/package/deploy, other PR/body/monitor, real credentials/data,
main-WIP or unrelated source operation is authorized.

## 9. Provenance

Independently authored decisions from the fixed project source/contracts and
root's explicit one-document authority, reviewed2026-10-21. No archived CPA/
CLIProxyAPI/Sub2API implementation/tests, copied library code, new dependency
or strict clean-room claim. Fixed Go1.26.9 and modernc sqlite v1.46.2 semantics
remain those of the inherited driver contract, not new execution evidence.
Their BSD-style/BSD-3-Clause licenses remain unchanged.
