# Code515 cancelled-query evidence ledger

## Contract, provenance and one-file budget

This is an independently authored docs-only safety ledger, not implementation,
repair, a new runtime probe or a dynamic experiment. Its sole file budget is
this new document. All prior files, source, tests, dependencies, workflows,
contracts and PR bodies remain byte-frozen. No dependency or copied external
implementation is introduced. Public Go sources below are evidence references,
not an implementation template.

Frozen base is open/Draft PR119, exact head
`8a9807e50abe35704d4ac86ef14a4c1cedbe261b`, sole parent/base PR118
`cbd8643910d44474a8f789edaa16a89babf7ddad`, tree
`67e10e5198602ff7ab23b7a647e0620d6a9b9186`. Its terminal body had 17944
characters at the read-only pin. No old PR body is edited by this batch.

Acceptance is limited to exact identity and public-source/version checks,
relative links and source anchors, a single added-file diff and
`git diff --check`. No test, build, SQL, product process, browser, old case,
manual CI rerun or dispatch is part of this docs-only verification. Natural CI
on the new documentation head must be recorded separately; untriggered or
skipped work is not successful execution. A Draft PR or this ledger issues no
implementation, dynamic, product or production GO.

This document publishes only fixed safe classifications, repository/public
source locations, immutable identifiers and the two job links. It contains no
raw log excerpt, runtime filesystem path, account, credential, raw error,
request/response content or database value.

Related frozen contracts:

- [Pre-constructor admission](employee-self-logout-constructor-admission-contract-2026-10-22.md)
- [Prospective B2 budget](employee-self-logout-code513-b2-admission-budget-contract-2026-10-22.md)
- [Lifecycle evidence boundary](employee-self-logout-code513-lifecycle-evidence-contract-2026-10-22.md)

## Two separate natural failures, not a shared diagnosis

Code validation #515 run39693167020 completed FAILURE at the frozen PR119
head, natural pull_request attempt1. Native compatibility and both VM workflows
succeeded, but do not erase Code's two red jobs. Complete terminal checks were
23 = 18 success + 3 skipped + 2 failure + 0 pending + 0 other. The three Code
conditional platform skips were unexecuted, not successful execution.

| Independent evidence identity | Safe worker/API observation | Inference limit |
| --- | --- | --- |
| [race-service (2), job119544097942](https://github.com/surpaimb/cpa-cloud/actions/runs/39693167020/job/119544097942); Normal/ignored-body-query; B2 selected | Last observed App1 pass12 QUERY_ENTER context=NONE; QUERY_RETURN error=CANCELLED and context=CANCELLED | Driver query entry for this call is UNKNOWN. |
| [race-service (0), job119544097940](https://github.com/surpaimb/cpa-cloud/actions/runs/39693167020/job/119544097940); Boundaries/replacement-employee; B2 unselected | Last observed App1 pass19 QUERY_ENTER context=CANCELLED; QUERY_RETURN error=CANCELLED and context=CANCELLED | Only the same-context and fixed-Go proof below permits a source-implied early return for this one Tx.QueryContext. |

Both foreground snapshot failures retain the fixed REOPEN/AFTER_APP_CLOSE,
OBJECT_ENUMERATION/QUERY_CONTEXT, SQLite5 and
`SLOG21_UNEXPECTED_SNAPSHOT_QUERY` classifications. Those foreground snapshot
records and the worker's last query observations are different evidence lanes.
Their coexistence does not establish a causal edge.

QUERY_ENTER here is the worker/financial API boundary before
`tx.QueryContext`; it is not driver entry, driver method completion or native
SQLite entry. The observed pass12 and pass19 values are sample observations,
not a sufficient prefix, capacity upper bound or lifecycle-completeness proof.
Selection merely identifies whether B2 metadata was enabled for the source
scenario. The two failures must not be combined into one root cause, attributed
to PR119's introduction, or described as repaired by it.

## Exact same-context source chain

The following references all pin the immutable PR119 head, rather than a moving
branch or a later implementation:

1. The [worker pass call](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/service/subscription_expiry_worker.go#L40)
   creates passCtx using WithTimeout, derives callCtx using
   `financial.WithExpiryObservation`, then passes callCtx directly to
   `ExpireDueSubscriptions`.
2. [WithExpiryObservation and expiryScopeFor](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/financial/subscription_period.go#L395)
   add only a WithValue binding and store the received ctx interface unchanged
   as scope.ctx. The binding introduces no new cancellation/deadline or
   WithoutCancel context. It carries observer/pass values, not a replacement
   query context.
3. The [financial query call site](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/financial/subscription_period.go#L169)
   constructs the scope from ctx. At lines 182 and 183, enter(ExpiryQuery) is
   followed directly by tx.QueryContext with that unchanged ctx. There is no
   intervening ctx reassignment, background context or alternate query seam.
4. [enter and returned](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/financial/subscription_period.go#L413)
   classify scope.ctx; the [context classifier](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/financial/subscription_period.go#L490)
   obtains its state from ctx.Err. Thus QUERY_ENTER's ctx is the same input
   interface as that passed to the next Tx.QueryContext, not merely a related
   ancestor context.
5. The [fixture observer sink](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/service/self_logout_confirmation_20261021_test.go#L2152)
   stores the value observation without rewriting its context classification.
   The [fixed formatter](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/service/self_logout_confirmation_20261021_test.go#L2385)
   renders phase/edge/context from that saved value. No context reference or
   mutable error value is reconstructed from the diagnostic output.

This is a source identity argument, not new runtime pointer instrumentation.
If the source pin, trace attribution or same-ctx chain cannot be verified, the
early-return inference must be withdrawn to UNKNOWN rather than supplemented
with an assumption.

## Fixed Go1.26.9 version and cancellation path

Both retained job logs report Go1.26.9. The read-only preflight checked the
installed fixed toolchain VERSION and compared the three full source blob
identities with the official go1.26.9 tag:

| Official source path | Whole-file Git blob |
| --- | --- |
| src/database/sql/sql.go | `4be450ca87687728797bafdb10e976adc5a71f58` |
| src/database/sql/ctxutil.go | `4dbe6af6d2bee38112ebbf1061903d15998b6d1c` |
| src/context/context.go | `d00ac67e38296573d20520dbd7aceb2fd1a52906` |

No local toolchain filesystem location or original version-log line is
published. The version tag and exact source blobs bound this argument; it is
not a proof about arbitrary custom contexts, other Go versions or a native
driver implementation.

### Already cancelled at QUERY_ENTER: pass19 only

1. The real chain uses WithTimeout plus WithValue. Go's
   [WithValue/valueCtx](https://github.com/golang/go/blob/go1.26.9/src/context/context.go#L727)
   forwards cancellation methods to the embedded parent;
   [WithDeadlineCause/timerCtx](https://github.com/golang/go/blob/go1.26.9/src/context/context.go#L632)
   uses cancelCtx for those methods.
2. Go1.26.9 [cancelCtx.Err](https://github.com/golang/go/blob/go1.26.9/src/context/context.go#L463)
   ensures Done is closed before returning a non-nil cancellation error.
   [Cancellation is latched](https://github.com/golang/go/blob/go1.26.9/src/context/context.go#L549),
   not later cleared. Therefore the saved CANCELLED observation precedes the
   same ctx's next query call with Done already closed.
3. [Tx.QueryContext](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L2529)
   first calls grabConn(ctx), returning immediately if it returns an error.
4. [grabConn](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L2245)
   first selects ctx.Done versus default. With Done already closed, that ready
   receive is selected; it returns ctx.Err before the transaction lock/check
   or returning a driver connection. The default is not an alternative when
   the receive is ready.
5. Consequently this particular pass19 Tx.QueryContext cannot reach
   [queryDC](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L1771)
   or its driver query paths. This is a source-implied API early return, not a
   newly captured driver-entry trace.

This narrow conclusion does not apply to preceding BeginTx, previous or other
passes, other calls or goroutines, driver/native transaction state, physical
connection identity, concurrent rollback, pool return or physical resource
release. It does not prove rollback completed or that a resource was freed.

### NONE at QUERY_ENTER, CANCELLED later: pass12 remains UNKNOWN

The NONE classification only says cancellation was not observed by Err at that
sampling point. Cancellation can happen before grabConn, after its initial
check, or later in the query path. Those possibilities are not distinguished
by the saved QUERY_ENTER/RETURN pair.

In particular, queryDC can dispatch through
[ctxDriverQuery](https://github.com/golang/go/blob/go1.26.9/src/database/sql/ctxutil.go#L46).
For QueryerContext that helper directly calls the driver's QueryContext;
there is no universal second database/sql cancellation gate before that call.
The legacy Queryer fallback has its own Done check, but that does not identify
which path or timing occurred here. A classified cancellation return can be
consistent both with no driver query and with driver query entry followed by
cancellation. Driver query entry for pass12 therefore remains UNKNOWN.

## B2 report is not cause, success or capacity

[prepareConstructorAdmission cleanup](https://github.com/surpaimb/cpa-cloud/blob/8a9807e50abe35704d4ac86ef14a4c1cedbe261b/internal/service/self_logout_constructor_admission_20261022_test.go#L168)
emits `B2_ADMISSION_PRIOR_FAILURE_NO_GO` only after t.Failed is already true,
then returns. It is not a new failure cause or an admission-success report and
does not identify the earlier failure's cause. The old primary failure is not
replaced by this label. Its presence in the selected sample and absence from
the unselected sample do not establish a causal comparison.

PR119's original E/F formal independent read-only reviews each reported
P1/P2/P3=0/0/0, only PRE_CONSTRUCTOR_ADMISSION_ONLY source narrow GO; neither
reviewer ran tests. That bounded source result does not cover Code515's two
dynamic failures or capacity. This new ledger is not a fresh E/F review.

## Historical reds, unknowns and stop conditions

PR115 Code513's two natural reds, PR117 Code514's TwoAuthenticated red,
PR113 Code512 and PR111 Code511 dynamic reds, earlier P3/static concerns and
prior local failures remain retained historical evidence. Neither native/VM
green nor this API cancellation proof washes them green. No common root cause,
introduction, repair or closure is inferred. The expected B2 tests-first red
and the separately recorded patch/read-only-tool refusals also remain in the
frozen prior contract and PR119 history; no old history is rewritten here.

CAPACITY_UNPROVEN remains. Observed final pass numbers do not establish an
adequate full-run budget, overflow absence in future samples, completeness of
another lifecycle, native Tx/physical Conn attribution or actual release.
GOV-02, real migration/M1-to-M2 and dynamic/product/production NO-GO remain.

Stop rather than broaden this batch if exact head/base, fixed Go source/version
or trace attribution differs; if the same-ctx proof is incomplete; if a link or
single-file boundary fails; if raw/sensitive evidence would be needed; or if
verification requires new tests, runtime probes, a wider edit or bypassing a
transport/review refusal. Incomplete conclusions stay UNKNOWN. Any unexpected
natural CI red is retained without rerun, dispatch or hot patch.

Only a separately authorized single documentation commit/equivalent transport
and stacked Draft PR may follow successful docs-only checks. Source remains
frozen afterwards for the root's independent re-pin, natural CI accounting and
original E/F exact-head pure read-only review. No Ready, merge, tag, package,
deployment, product process/browser or real-system operation is authorized.
