# B2 admission and evidence budget: docs-only contract

Status: independent documentation candidate, 2026-10-22 (Asia/Shanghai).
No B2 selector, observer, collector, native Conn/Tx or dynamic case is implemented.

Navigation: [scope](#frozen-scope), [observations](#observed-targets),
[admission](#prospective-source-admission), [capacity](#capacity-unproven),
[cut](#one-original-cut), [oracles](#planned-oracles-and-stop-gates).

## Frozen scope

Start only from open/Draft [PR117](https://github.com/surpaimb/cpa-cloud/pull/117),
exact head `bf3877aa4856434ed80fe63f26229b06963515c2`, base and sole parent PR116
`ef55ad47e12cb27451bbc3614a0c1120ba3f4d9f`, complete source tree
`1996f562be4b3381c2e0531fd194a315f9a89ae2`. PR117 has one commit/six files,
+905/-12; its 17874-character terminal body is frozen.

Only this new document is writable in the repository:
`docs/employee-self-logout-code513-b2-admission-budget-contract-2026-10-22.md`.
The new stacked Draft PR body is separately writable. All old files/PR bodies,
Go/tests/selectors/collectors, dependencies/workflows and monitors are frozen.
Current implementation and test whitelist is **empty**. A required second file,
changed guard/business path or incomplete one-document specification is a stop,
not implicit authority. This is neither a PR117 hot patch nor causal repair.

Read the frozen [A0 lifecycle contract](employee-self-logout-code513-lifecycle-evidence-contract-2026-10-22.md),
[B1 entry-reference contract](employee-self-logout-first-open-entry-relay-contract-2026-10-22.md),
[Init caller contract](employee-self-logout-initialize-driver-origin-caller-contract-2026-10-22.md)
and [owner-only contract](employee-self-logout-first-app-owner-only-contract-2026-10-22.md).
Their implementation/review/evidence boundaries are not enlarged by this text.

The original fixture, Gate4, 22 physical snapshot anchors and 54 ordered raw
literals stay byte-unchanged; no anchor relocation, source projection change
or old-hash refresh. Preserve Init/owner/B1 projection and mutation controls,
rollback-defer / BEGIN_RETURN / cut / first-query adjacency and cleanup order.
The raw-literal SHA256 remains
`ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860`.

## Observed targets

These are three distinct historical observations, not a shared causal result:

| Historical sample | Exact natural failure | Observed pass extent | B2 status |
| --- | --- | --- | --- |
| PR115 `TestSelfLogout20261021Boundaries/zero-rows` | Code513 run39659794029, race0 job119429570609 | 12 pass | Future proposed admission target only |
| PR115 `TestSelfLogout20261021Normal/ignored-body-query` | Code513 run39659794029, race2 job119429570545 | 17 pass | Future proposed admission target only |
| PR117 `TestSelfLogout20261021TwoAuthenticated` | Code514 run39677465532, race3 job119490997081 | 17 pass | **Historical only; excluded from this B2 admission** |

PR115's two failures belong to exact head
`e8521edae3266750a7873e57690b5f1dd602fc7d`, base PR114
`f32e8482da98878c2c3e5c553f11f498a05c7771`, natural pull_request attempt1,
completed/FAILURE. Its complete checks are 23=18success/3skipped/2failure.
PR117 Code514 belongs to the source head/base above, natural pull_request
attempt1, completed/FAILURE, 23=19success/3skipped/1failure. Skipped is unexecuted.

Both Code513 leaves and the separate Code514 sample report App1
REOPEN/AFTER_APP_CLOSE: App.Close returned OK, old pool remained in-use NONZERO,
raw Begin returned OK, then the first OBJECT_ENUMERATION/QUERY_CONTEXT returned
SQLite5 with fixed SLOG21_UNEXPECTED_SNAPSHOT_QUERY (context NONE). These are
sanitized classifications, not raw logs/errors, SQL, paths, identities or values.

TwoAuthenticated has two authenticated-request phases in **one fixture**; at
this reported failure it still concerns App1, before a second Open. It is not
two fixtures or an App2 observation. Do not reset its counters between phases,
reinterpret it as either Code513 target or silently admit it as a third sample.

The 12/17/17 entries are historical observed pass extents, **not** proven complete
tails, minimum operations, maximum concurrent activity, future upper bounds or
counts of native Conn/Tx. Equal pass counts/SQLite5/labels do not prove the same
mechanism, lock owner, Conn, PR117 introduction/irrelevance or repair.

PR115 native/CLI/VM successes and PR117 native23/CLI28/VM33 successes cannot
cover their respective Code reds. PR113 Code512, PR111 Code511, old F PR111 P3,
separate E PR108 P3 and earlier PR108/106/100/102-105/local reds remain frozen.
No shared-cause, inherited-red or PR117-causality claim follows from one log,
prior green, this specification or a helper result. Such claims need another
authorized controlled-evidence batch, not manual reruns of old cases/CI.

## Prospective source admission

Only the two named Code513 targets are proposed. A later independent batch must
introduce a **source-owned typed selection**, frozen for each target at its
original caller **before the original constructor and Initialize**. Use the
known source scenario/input and audited callsite; never t.Name, log order, stack,
address, later cut, query outcome or observed pass count. UNKNOWN/unselected
and any third sample must remain explicitly outside admission.

Do not alias zero-rows to existing Auth or ignored-body-query to existing
confirmation enum values merely to obtain a receipt. Existing owner/Init/B1
selection covers Auth parent and confirmation-query-error, **not these two
proposed leaves**. This docs-only batch does not change any selection or
establish even the B1 link for a newly proposed target.

B1's canonical pointer/target/actual-capability proof reaches only the actual
first Open entry for its existing selected paths. It does not prove the App
store uses that reference. For B2, the following links are separately
**UNPROVEN/UNKNOWN until future actual source evidence**, never inferred:

| Link / role | Required future source fact; not supplied by B1 |
| --- | --- |
| Intended target -> original fixture/Init caller/first Open | New explicit typed admission at the original source sites, no copied or borrowed fixture/receipt |
| First Open -> actual App store / implementation use | Audited real store construction/use, not canonical receipt or singleton equality alone |
| Actual store -> Driver.Open -> Conn | Successful original method return and that actual returned native object, no probe/alternate DB |
| That Conn -> BeginTx -> native Tx | Successful actual native method return; high-level sql.Tx/TX_DONE is not the relation |
| Worker -> pass -> DB/Conn/Tx | Independently source-bound actor and each actual association; no one-pass/one-Conn/one-Tx assumption |
| Raw snapshot -> its DB/Begin/cut | Separate raw role and actual original boundary; raw observer DB is not App/Init DB |

Init, App, worker and raw roles must be independent even if some execution
shares a DB/implementation. A same-driver/same-selection/same-pass match does
not authenticate any object/root. No global registry/current plan, closure
identity, public option or inferred address/path identity is acceptable.
Failed/retried Open attempts cannot mint a successful Conn generation; failed
Begin cannot mint a native Tx. Uninstrumented fallback/alternate interfaces
stay UNPROVEN, not a silently substituted source.

## Capacity unproven

Current result: **CAPACITY_UNPROVEN for both proposed Code513 targets**. This
contract supplies no complete a-priori bound and chooses no implementation
array size, pass limit or prefix length. A0's candidate 8-pass allowance is
below observed12/17; replacing it with17,32,128 or another convenient number
does not prove adequacy. A complete bound cannot be retrofitted from a run that
happened to fit, the last reported ID or a finite timeout alone.

### Units and scopes before numbers

Future budgeting must distinguish these units, not add incomparable counters:

| Unit | Definition / required independent finite interval |
| --- | --- |
| Sample/fixture | One pre-admitted typed target and its actual fixture capability; no cross-fixture pooling |
| Method event slot | One immutable ENTER or RETURN of one specified actual method invocation, including failed attempts; not one pass/Tx |
| Conn generation | One actual successful original Driver.Open return, local to fixture+DB role; UNKNOWN0 or 1..G for a prospectively proved/approved G |
| Native Tx ID | One actual successful native BeginTx return, local to that Conn generation; UNKNOWN0 or 1..T for its prospectively proved/approved T |
| Pass ID | One actual source-owned worker pass entry, local to fixture+actor; UNKNOWN0 or 1..P for a prospectively proved/approved P |
| Association/publication slot | A separately counted source-binding/arm/seal/control fact; never free or synthesized from a method event |
| Cut view | One value-only immutable view at the one preselected original cut for a target; not a second event stream or mutable live handle |

Finite sample/actor/DB/method/edge/result/source/publication vocabularies must be
frozen before any future execution. UNKNOWN, missing, rejected and overflow
must be explicit. Private capabilities may validate actual identity internally;
output is limited to safe finite enums and bounded local counts/IDs. No SQL or
parameters, DSN/path, address/handle, driver formatting, raw error/log, identity,
credential/Key/session or prompt/response may be output.

IDs never wrap, recycle, restart at request phases, or get renumbered to fit.
Identical local integers in different scopes remain different identities.
Failed attempts still consume event slots although they yield no successful
Conn/Tx ID. A missing RETURN consumes only actually observed slots and stays
missing; it is not evidence that the call remains executing. A RETURN pair is
method-boundary evidence, not physical release or putConn/SQLite unlock proof.

### A priori complete-envelope obligation

Before choosing an adequate complete buffer, a future batch must bound **every**
event-producing dimension up to the original cut, including failed/retried Open
and Begin attempts, successful generations/Txs, Close/Rollback invocations,
worker passes/edges and independent raw/binding/publication/control records.
One pass may involve zero/multiple methods, Conn generations or native Txs.
Logical TX_DONE, worker Wait, App.Close and Stats cannot discharge these terms.

A permitted reasoning form is an inequality over *proved* invocation bounds:

`required_slots <= fixed_control_slots + sum(2 * bounded_method_invocations)
                 + bounded_worker_records + bounded_raw_records
                 + bounded_binding_and_publication_records`.

Here two is only the maximum ENTER/RETURN edges of one enumerated invocation;
it is not proof of the invocation bound. Each term needs its own source model,
scope, finite interval and proof, including attempts that fail or never publish
a RETURN. No hidden one-Tx-per-pass, one-Conn-per-store, zero-retry or completed
worker-tail assumption. Methods/interfaces added later need prior vocabulary
and budget approval, not retrospective inclusion.

Document which source constraints bound workload, scheduling, repeats and all
actors before the cut, and why they cover both proposed targets. A wall-clock
timeout without a demonstrated operation-rate bound is not a cardinality proof.
An unbounded/unknown term makes the complete envelope CAPACITY_UNPROVEN. None
of these terms is proved by this document; no runtime adequate-capacity GO.

### Prefix-only fallback requires another batch

If a complete bound cannot be proved, only a **separately reviewed prospective
fixed finite prefix** may be approved for limited evidence. This document does
not choose its N or authorize that collector. Before execution, that other
batch must freeze N, actor/method units, ID intervals, required source edges,
one cut, acceptance limits and a reserved overflow/insufficiency status.

Retain the first N admitted records in original order. On attempt N+1, latch
OVERFLOW/EVIDENCE_INSUFFICIENT, preserve the existing prefix and stop the
evidence/acceptance claim for review. No last8 tail extraction, ring buffer,
cropping, overwrite, counter reset, ID renumbering or post-observation resizing.
No second run or later case is authorized to obtain a nicer prefix.

Exactly N records with no N+1 attempt is not automatically overflow, but also
does not prove a complete global bound or coverage of missing actors/edges.
The prefix can remain CAPACITY_UNPROVEN even when an execution fits. Missing
or late RETURN remains absent from the earlier sealed view; never fill it later.
Fail closed concerns evidence sufficiency, not suppression/replacement of the
original query error or a new business-path cancellation/retry/cleanup change.

## One original cut

For each proposed target, a future authorized batch must preselect its original
raw cut from source, not choose a convenient cut after seeing failure. Its only
admissible view position is: original raw Begin succeeded; original rollback
defer installed; original BEGIN_RETURN/OK published; **before the first original
OBJECT_ENUMERATION QueryContext**. Freeze one immutable value view there.

Do not move Begin/defer/BEGIN_RETURN/cut/query or the existing guards, insert a
probe, wait, query, timer, alternate DB/Tx, extra cleanup, second cut or late
RETURN backfill. Do not call a helper after the error and present its values as
the earlier cut. Failed Begin has no successful-cut/native-Tx positive evidence.
Unselected paths continue their original behavior without invented empty-H0 GO.

Only facts published by that point belong in the view; no pointer alias to a
mutable collector or cleanup-time reinterpretation. Existing context, SQL,
result/return values, cancellation and old cleanup/report priority remain.
The original query failure and fixed label remain primary. Insufficiency,
overflow or a secondary diagnostic report must not swallow, replace, clear,
retry or turn that error into success. Later method RETURN is only a later
fact, never a rewrite of the earlier view or physical-release proof.

## Planned oracles and stop gates

All rows are **future specification / NOT EXECUTED / NOT PROVED**. No code/test
file whitelist or manual old-case/old-CI run is authorized by this matrix.

| Planned oracle | Required distinction |
| --- | --- |
| Real target/source positive | Pre-constructor typed admission; actual fixture/caller/entry, store/Driver.Open/Conn/native BeginTx/Tx and independently bound worker/raw roles; not helper-only fabrication |
| Actual cut positive | Original Begin success, rollback defer and BEGIN_RETURN before one sealed view, first schema query immediately after; all required prior edges/publications independently verified |
| Unselected / excluded third sample | No admission/coverage claim for TwoAuthenticated or any unrelated leaf; same enum/driver/count cannot smuggle it in |
| Foreign / copied capability / wrong root or role | Reject even with equal selection, singleton, local IDs or timing; no receipt/capability copied from another fixture/actor |
| Missing edge / unknown ID | Explicit UNPROVEN/UNKNOWN, no zero-as-PASS, borrowed event, inferred native Tx or fabricated RETURN |
| Failed/retried Open or failed Begin | Preserve attempt/error evidence without phantom Conn/Tx success; retry does not erase a failed attempt or reuse an identity |
| Late RETURN / publication | Earlier cut stays incomplete; absence does not mean still running, late completion cannot backfill it |
| Capacity exactly full | Retain all N prefix slots without implicit resize or overflow-by-equality; no full-coverage claim merely because it fit |
| Capacity exceeded by one | Latch overflow/insufficiency, retain prefix/IDs/original error, no last8/crop/wrap/reset/renumber/posthoc expansion |
| H0 / pure-helper PASS / method RETURN | Insufficient actual-source/coverage evidence; neither physical release nor BUSY cause/old-case acceptance |

Any future implementation needs its own prior approved source/file whitelist,
typed selection, budget proof or limited-prefix contract, executable positive
and negative oracles, error priority and stop protocol. Do not weaken old
assertions/hashes, borrow selected Auth/confirmation evidence or expand samples
to resolve a failure. A capacity change after observation is a new batch, not
a permissive interpretation of this one.

This batch only reads frozen evidence/source and checks this document's relative
links/anchors and Git diff/single-file scope. No tests, service/process/browser,
old case, SQL, credentials, real8787 or manual CI/workflow is run. Planned dynamic
rows stay unexecuted. If a coherent one-document specification requires edits
elsewhere or actual execution, stop without expanding scope.

Submit one documentation commit/new stacked Draft PR. Preserve exact sole
parent/tree/blob/mode across any equivalent transport, disclose metadata SHA
differences; never change trust/credentials/remote, force history or retry old
CI. New-head natural workflows are recorded only as actually triggered or not
triggered; skipped/untriggered is not executed green, no inherited green.
First unexpected natural red freezes source/evidence and stops; no hot patch,
manual rerun/dispatch or green-seeking follow-up.

Root arranges new-head original E/F independent formal read-only review after
re-pinning. E/F/D pre-review preference, prior PR117 entry-reference narrow GO
and native/VM results cannot issue a new B2 implementation/dynamic GO.

This text is independently authored from the bounded authorization, frozen
project contracts/source roles and sanitized historical evidence, not archived
CPA/CLIProxyAPI/Sub2API or library implementation material. No strict clean-room
claim, dependency/protocol/license change or production proof. Existing
[independent-source rules](independent-implementation.md) continue to apply.

Keep all historical reds, UNKNOWN and CAPACITY_UNPROVEN. B2 is only a **docs-only
narrow candidate**; no selector/collector/native Conn/Tx/old-case implementation
is authorized. Draft / dynamic-case, real migration, product and production
**NO-GO** remain; no Ready/merge/tag/package/deploy or old-PR/monitor mutation.
