# Code513 lifecycle evidence: A0 docs-only contract

Status: independent A0 documentation candidate, 2026-10-22 (Asia/Shanghai).
This is a new batch after PR115's two natural reds, not a PR115 hot patch or
causal repair. No lifecycle observer, source handoff or dynamic test is added.

## Frozen source and one-file authority

The exact starting point is open/Draft [PR115](https://github.com/surpaimb/cpa-cloud/pull/115):

- Head: `e8521edae3266750a7873e57690b5f1dd602fc7d`.
- Base and sole parent PR114: `f32e8482da98878c2c3e5c553f11f498a05c7771`.
- Complete tree: `a8fcea7225cbe5abd5d30d332d9f310591befb10`.
- One commit, six files, +1063/-23; its 14866-character terminal PR body is frozen.

This batch may add only this document:
`docs/employee-self-logout-code513-lifecycle-evidence-contract-2026-10-22.md`.
Only the new Draft PR's body is separately writable. All prior documents, Go,
tests, workflows, dependencies, PR115/older PR bodies and monitors are frozen.
The **current and future implementation/test whitelist authorized by A0 is
empty**. Describing a later interface or test never authorizes implementing or
running it. A second repository file or a needed scope expansion is a stop.

Read with the frozen [coverage/origin contract](employee-self-logout-code512-coverage-driver-origin-contract-2026-10-22.md),
[caller-only implementation contract](employee-self-logout-initialize-driver-origin-caller-contract-2026-10-22.md)
and [owner-only contract](employee-self-logout-first-app-owner-only-contract-2026-10-22.md).
The three evidence layers below must not be merged.

## Layer 1: actual existing natural evidence

Code validation #513, [run39659794029](https://github.com/surpaimb/cpa-cloud/actions/runs/39659794029),
belongs to the exact PR115 head/base above, natural `pull_request`, attempt1,
completed/FAILURE. Complete same-head `filter=all` checks are **23 = 18 success +
3 skipped + 2 failure + 0 pending + 0 other**. The conditional Code linux,
macos and windows jobs were skipped, not executed successfully.

| Actual failed job | Actual failed case | Source-owned stage / cut |
| --- | --- | --- |
| [race-service (0), job119429570609](https://github.com/surpaimb/cpa-cloud/actions/runs/39659794029/job/119429570609) | `TestSelfLogout20261021Boundaries/zero-rows` | App1, REOPEN / AFTER_APP_CLOSE |
| [race-service (2), job119429570545](https://github.com/surpaimb/cpa-cloud/actions/runs/39659794029/job/119429570545) | `TestSelfLogout20261021Normal/ignored-body-query` | App1, REOPEN / AFTER_APP_CLOSE |

Both failed step5. Private job-log lines223-232 contain the fixed observations:
App.Close CLOSE_RETURN/result=OK; old-pool open/in-use NONZERO and idle ZERO;
raw snapshot BEGIN_RETURN/result=OK; then the first schema
OBJECT_ENUMERATION / QUERY_CONTEXT returns SQLite5, context NONE, followed by
`SLOG21_UNEXPECTED_SNAPSHOT_QUERY`. This is a sanitized fact summary, not a copy
of raw logs, errors, SQL, paths, identities or data values. App1 is the source
fixture role, not a discovered lock owner or native-connection identity.

Neither failing leaf enters the existing owner/Init-origin selection. Those
selections cover AuthBoundaries parent first Open and confirmation-query-error
leaf first Open, not these two cases. No selected origin/owner result may be
borrowed from a different leaf, constructor, fixture or earlier green run.

The following same-head natural pull_request attempt1 runs completed/SUCCESS:

| Workflow | Exact run | Actual scope |
| --- | --- | --- |
| Linux systemd v3 offline CLI #27 | [39659793966](https://github.com/surpaimb/cpa-cloud/actions/runs/39659793966) | source-a, restore-b, relay-cleanup success |
| Independent Linux VM v3 synthetic recovery #32 | [39659793950](https://github.com/surpaimb/cpa-cloud/actions/runs/39659793950) | source-a, restore-b, relay-cleanup success |
| SQLite native compatibility #21 | [39659793901](https://github.com/surpaimb/cpa-cloud/actions/runs/39659793901) | six native platform jobs actually success |

These results do not cover Code's two reds. They are inherited synthetic
recovery / frozen compatibility evidence, not a new Conn/Tx lifecycle trace,
actual I/O fault injection, physical release or production migration evidence.

The original E/F formal independent read-only PR115 reviews each reported
P1/P2/P3=0/0/0, only caller-only source narrow GO. They do not turn these cases
green or grant dynamic, product or production GO. A0's root-reported E/F/D
pre-review preference is not formal review of a not-yet-submitted A0 head.

## Layer 2: deliberately UNKNOWN

No inference in this table is licensed by Layer1:

| Existing observation | Still unproven / UNKNOWN |
| --- | --- |
| Similar SQLite5/schema-query labels in two leaves or old runs | Shared mechanism, common lock owner, PR115 introduction, PR115 irrelevance or repair |
| App.Close result OK | Every Conn.Close entered/returned, putConn occurred, native connection physically released or SQLite lock released |
| Pool Stats show in-use NONZERO | Which native Conn/Tx/pass owns it or whether it causes this raw query failure |
| TX_DONE, worker pass end or worker Wait returned | The actual native Tx.Rollback RETURN, its result, release, or absence of other actors |
| raw Begin succeeded | Another DB's transaction state, later query success, isolation from every actor or a causal lock relation |
| DB.Driver implementation reference delivered to caller | Actual first Open's store/Conn origin, any Conn generation, native Tx or lock ownership |
| Same driver singleton, same pass number or close timestamps | Same store/DB/Conn/Tx, physical happens-before or lock identity |
| No RETURN was captured | The method is still executing; the record may be missing, unpublished, unbound or overflowed |
| Complete ENTER/RETURN pair, including OK | Physical release; only that method boundary/result was observed |

Init's original store, App store, worker execution and raw snapshot DB are
different roles. A worker may use the App DB, but a role/DB association requires
its own source binding; no separate worker DB is invented. The raw observer DB
does not become the observed App DB. Init SQL already executed before the
current getter cannot be registered retrospectively.

Do not infer a shared cause from one log, prior green, repeated label or this
contract. Testing a shared mechanism, inheriting an old red or attributing
PR115 causality needs another specifically authorized controlled-evidence batch.
Manual old-case/old-CI reruns are not authorized here or by that statement.

## Layer 3: planned minimum interface, not implemented

The following are obligations for a separately reviewed implementation, not
claims about current runtime coverage or permission to add wrappers now.

### Real-source chain and independent boundaries

1. Select a fixture and sample by a private source-owned typed selection. Do not
   infer selection from test names, stack/address/log lookup or a global plan.
   New selection of these two leaves requires separate authority: the existing
   Auth/confirmation selections still do not cover them.
2. Bind the actual selected store's implementation through its audited source
   chain. Record the original successful **Driver.Open RETURN's actual Conn**,
   not a probe connection, separately opened DB, helper argument or another
   store using the same registered singleton. An implementation reference alone
   is UNPROVEN for this relation.
3. Bind a native Tx only from the actual observed Conn's successful
   **Conn.BeginTx RETURN**. A high-level sql.Tx, TX_DONE or pass label is not
   that binding. If the real path uses an uninstrumented fallback/connector
   interface, mark coverage UNPROVEN and stop the acceptance claim; do not
   silently substitute Conn.Begin, Connect or a registry-wide hook.
4. Record actual **Conn.Close ENTER/RETURN** and actual native
   **Tx.Rollback ENTER/RETURN** around those original methods, retaining the
   original result. Missing RETURN is missing evidence, not an execution-state
   assertion. Completed RETURN is still not physical release/putConn proof.
5. Record worker pass/wait boundaries as independent actor facts. Do not
   manufacture Conn/Tx method edges from worker callbacks, a Wait, TX_DONE or
   elapsed-time proximity. An actual pass may use multiple connections/Txs.

No new DB/Conn/Open/Ping/Raw/SQL acquisition, retry, wait, timeout, cancellation,
worker, cleanup repair or driver registration override is permitted as a
shortcut to provenance. A future observer may perturb scheduling; it must not
claim zero effect or reproduce a cause merely because a symptom changes.

**B, caller-to-first-Open origin delivery, remains a later independent batch.**
PR115 stops at the original caller. A0 neither completes B nor treats B as
sufficient Conn/Tx/cut evidence. If B or explicit leaf selection is a missing
prerequisite, publish UNPROVEN/UNKNOWN instead of inventing a positive trace.

### Original immutable cut and primary error

The future view has exactly one authorized semantic position: the original
raw snapshot Begin has succeeded, the original rollback defer has been
installed, and the first original OBJECT_ENUMERATION QueryContext has not yet
been called. Freeze a value-only immutable view **there**, before that query.
Do not relocate Begin, defer, cut or query, add a different transaction/probe,
wait for a desired worker state, or sample only after observing SQLite5.

The view may contain only facts published by that point. Do not retain mutable
event/handle aliases, overwrite it during cleanup, use a later report to fill
missing earlier fields, or truncate evidence into an apparently complete prefix.
Later method RETURN can be a separate later fact, never backfilled into this
view. Preserve existing route indices, snapshot adjacency, source guards and
the original cleanup/report order; widening those guards needs new authority.

The original query error remains **primary**. Observation, overflow, unbound
origin or a secondary report cannot replace, clear, wrap into success or hide
the original error/fixed failure assertion. No retry or later successful query
may erase it. Diagnostic insufficiency freezes evidence/acceptance, not a
license to skip or alter original business work and cleanup.

### Finite record vocabulary and fixed candidate budget

All following vocabulary/budgets are a **future contract candidate**, not
allocated state, implemented output or evidence already collected by A0.
Zero/default values are UNKNOWN/unproven, never implicit positive acceptance.

| Field | Finite planned meaning |
| --- | --- |
| sample | UNKNOWN, BOUNDARIES_ZERO_ROWS, NORMAL_IGNORED_BODY_QUERY; an explicitly selected fixture-local role, not runtime name parsing |
| actor role | UNKNOWN, INIT, APP, WORKER, RAW_SNAPSHOT; independently source-bound |
| DB role | UNKNOWN, INIT_DB, APP_DB, RAW_DB; never inferred from driver equality |
| source state | UNPROVEN, VERIFIED, REJECTED, UNKNOWN; VERIFIED requires the real source chain, not a supplied Boolean alone |
| generation / Tx / pass | UNKNOWN=0 or a bounded local ID; generation scoped to fixture+DB role, Tx to that generation, pass to fixture+actor; IDs do not cross these scopes |
| method / edge | UNKNOWN or DRIVER_OPEN, CONN_BEGIN_TX, CONN_CLOSE, TX_ROLLBACK, WORKER_PASS, WORKER_WAIT; edge UNKNOWN, ENTER, RETURN |
| result class | UNKNOWN, ENTER, OK, ERROR; never err.Error or driver formatting |
| error / context class | error UNKNOWN, NONE, SQLITE5, OTHER; context UNKNOWN, NONE, DEADLINE, CANCELLED, OTHER; only independently classifiable public-safe facts, not inferred causes |
| publication state | UNKNOWN, ABSENT, CANDIDATE, BOUND, SEALED, REJECTED, OVERFLOW; no late transition rewrites a sealed view |

Proposed maximum envelope per selected fixture: **64 event slots, 8 successful
Conn generations per DB role, 8 native Tx IDs per generation, 8 pass IDs per
actor, and one cut view**. At most the two named sample roles are admitted;
each fixture remains separate. These bounds are chosen documentation limits,
not inferred from Code513 or a claim that real execution fits. They must be
explicit constants in any separately authorized implementation/review, not
configurable growth or permission to implement them now. A required larger
envelope needs a new approved bound before execution, not a runtime resize.

Failed/retried Open attempts also consume the fixed event budget; only the
matching real successful RETURN can bind a new generation. No provisional
failure ID is promoted/reused as success. Duplicate/foreign IDs or capabilities
reject rather than selecting the most recent object. IDs never wrap/recycle.

Unknown enum values, absent source, missing required edges and capacity excess
must remain explicit. On overflow, latch OVERFLOW, freeze the existing prefix,
mark the view insufficient and stop the evidence claim/report for review; do
not grow, wrap, evict, crop, overwrite or continue another case to seek green.
No empty all-zero H0, pure-helper success or absence of errors proves coverage.

Output is restricted to these finite classifications, bounded IDs/counts and
coverage/publication states. **Never output SQL/parameters, DSN/path, addresses,
handles, raw errors, formatted driver objects, employee/provider identities,
credentials, keys, sessions, prompts/responses or raw logs.** Source-only opaque
capabilities are not output fields. Do not create a process-global correlation
ID, global collector or exported/public diagnostic endpoint.

### Planned positive and negative obligations

Every row below is **planned / not executed by A0**. Pure helpers may test
classification later but cannot replace actual source-chain and original-cut
evidence. No current code/test filename whitelist is granted by this matrix.

| Planned control | Required evidence / rejection |
| --- | --- |
| Real source-chain positive | Original selected store -> actual successful Driver.Open Conn -> that Conn's successful BeginTx native Tx -> actual method edges, independently scoped worker facts, sealed original pre-query cut |
| Original-cut positive | Begin success and original rollback defer installed before view; first schema query still follows immediately; no cleanup/later event can rewrite the view |
| Failed / retried Open | Failure cannot bind a successful Conn; retry is a distinct bounded attempt, not erased failure or borrowed success |
| Foreign or duplicate binding | Reject foreign implementation/Conn/Tx capability and duplicate publication/consumption; no replacement with newest matching singleton |
| Cross-fixture / wrong DB / wrong actor | Reject even with equal local IDs, same singleton or timing; worker/raw/Init/App roles do not silently alias |
| Late publication / missing RETURN | Earlier view remains incomplete/UNKNOWN; no late fill, still-running inference or fabricated completed pair |
| Overflow / unknown state | Latch insufficiency with the original prefix and primary query failure intact; no wrap, crop or zero-as-PASS |
| Pure helper / empty H0 | Must not pass real-source/cut acceptance even if all synthetic fields look orderly or no event reports failure |
| Same-driver / same-pass / TX_DONE / near-time | None proves same Conn/Tx or lock ownership; require independent actual source binding |
| Complete RETURN without physical-release evidence | Accept only the observed method-return fact, never upgrade to putConn/physical-release/unlocked |

Before a future dynamic batch, freeze its own file whitelist, source identity,
specific positive/negative oracles, bounds, primary-error preservation and
stop rules. No copied library implementation, weakened old assertion, extra
probe lifetime or manual old-case rerun is authorized as an evidence shortcut.

## Permanent red history and independent provenance

PR115 Code513's two reds remain red on their own exact source. PR113 Code512
run39637428191 remains FAILURE: NativeAbort INSTALL/AFTER_APP_CLOSE and
confirmation-query-error plus post-commit-report REOPEN/AFTER_APP_CLOSE,
23 checks = 18 success / 3 skipped / 2 failure. PR111 Code511 run39616103565's
AuthBoundaries FINAL_CLEAN and confirmation REOPEN two reds, F PR111 static P3,
separate E PR108 P3, PR108 Code509, PR106 Code508, PR100 Code501, PR102-105 and
the earlier local SPQI_INVALID_INSTALLATION red remain unresolved. Shared
labels do not establish a common mechanism, introduction or repair.

This document is independently authored from the new root authorization,
frozen project contracts/source roles and private sanitized natural-CI evidence;
no archived CPA/CLIProxyAPI/Sub2API code, tests, migrations or documentation is
copied or ported. No strict clean-room claim, dependency, license change or
new provider/public protocol is introduced. Project requirements remain in the
[product plan](product-plan.md), [independent-source rules](independent-implementation.md),
[development plan](development-plan.md) and [preview contract](preview-contract.md).

## Actual A0 verification, submission and stop boundary

Only read-only source/identity/evidence inspection, document-link checks and
Git diff/scope checks belong to A0. No tests, old cases, app/service/browser,
SQL or real credential/database/client exercise is performed. There is no
dynamic result, coverage percentage or lifecycle/causal validation to report.

Submit one documentation commit and one stacked Draft PR from the exact PR115
head. On the first SSH host-identity refusal, do not retry or change trust,
credentials or remote. Only the authorized connector fallback may transmit
the exact same sole parent, whole tree and single blob/mode; different metadata
SHA must be disclosed. If equality cannot be established, stop.

Read natural workflows only as actually triggered/untriggered on the new head;
an untriggered/skipped check is not executed green. Do not dispatch/rerun any
workflow or borrow the parent's results. First unexpected natural red freezes
this batch and its evidence; no hot patch or green-seeking rerun. Root will
re-pin the new exact head and arrange original E/F independent formal read-only
review; this contract, pre-review preference or CI cannot issue that review.

A0 is only a **docs-only narrow candidate**. B and all dynamic interface/case
work remain future separate batches. Keep Draft / dynamic-case, real migration,
product and production **NO-GO**. No Ready/merge/tag/package/deploy or claim of
fixed BUSY cause, released Conn, completed Gate1/complete Gate2/Gate3 or restored
dynamic/product acceptance.
