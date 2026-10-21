# Test-only raw pre-query cut installation contract

Status: independent Gate4-only implementation contract, 2026-10-21. Root
explicitly permits the bounded old-test edits below; this is not permission
to implement Gate1/2/3, driver interception or product behavior. Base is
[PR109](https://github.com/surpaimb/cpa-cloud/pull/109), exact head/sole parent
`dc776189f78ba90c75db8fddfba6d007d527a06a`. Its docs-only E/F narrow GO remains
docs-only. The inherited [binding contract](employee-self-logout-red-sample-tx-pass-binding-contract-2026-10-21.md)
and [collector A contract](employee-self-logout-collector-algorithm-contract-2026-10-21.md)
are frozen. Keep Draft / real migration and production NO-GO.

## Exact whitelist and installation permission

Exactly three files are permitted:

1. This new contract.
2. Local edits in `internal/service/self_logout_confirmation_20261021_test.go`:
   fixture-local typed plan/carrier; sole-case allocation and arm; raw cut;
   report after both existing cleanup flushes; numerical relocation of the
   original 22 source-line anchors, preserving function/cut identity.
3. New `internal/service/self_logout_prequery_cut_installation_20261021_test.go`:
   bounded test-only plan and independently authored installation controls.

A collector, product/regular Go, driver, financial, dependencies, workflows,
other old files/contracts and old PR bodies remain unchanged. No new DB,
Initialize/Open, fixture, snapshot invocation, SQL, argument, retry, cancellation,
timeout or original assertion/error mapping is allowed. No global hook/registry,
test-name detection, SQL recognition, goroutine/stack/address-derived identity,
`//line`, extra query or probe. Original 54 raw string literals stay byte-exact
and ordered (joined with LF, SHA256
`ceaa64a733c2b53af1111d34cd6b22c55d57bb3a47b97977beaac6ca5d104860`).

## Sole candidate and passive route

Only `TestSelfLogout20261021Boundaries/pre-commit-refusal` opts in. Allocate
its typed plan and existing A storage after the existing fixture constructor,
before its original snapshots/operation; arm only after the original
`expectedAfter` returns and immediately before the original `f.clean`.
Thus a later failure before arm/cut is NOT_REACHED, not a fabricated empty cut.
No additional App or DB is created. Original DELETE/refusal remains in the
first App; expectedAfter's existing reopen produces the later App. This plan
does not observe the first App, its original logout Tx, or donor expiry tail.

The fixture owns the plan. The original clean first takes its live-App snapshot
without a handoff/plan and must NOT invoke the cut. Only original
`clean -> closedRaw -> handoff -> snapshot` carries the armed plan, and only
the existing FINAL_CLEAN handoff may route it once. REOPEN/INSTALL selected
routing, duplicate arm/selected-route/cut and strict use before arm latch an
anomaly. The qualification rule and preserved failed batch are below. There is no
fallback or interception: original close/query/error behavior proceeds.

The exact call is after raw BeginTx succeeds, after the original rollback defer
and successful BEGIN_RETURN observation, immediately before the original first
OBJECT_ENUMERATION `slog21Read` (which calls QueryContext). Begin failure does
not call the cut. Later queries and live-App snapshots have no installation.

The cut takes no testing.T, context, SQL, DB, App, driver or callback. All storage
is allocated outside the raw transaction window. It performs exactly one
existing A `cut(0)` into a private, immutable bounded view, with H=0/scanned=0,
no fabricated App/Conn/pass/Tx admissions, no seal, log, timer, wait, drain,
retry or allocation. Its private pointer is never published to workers.
This is an empty installation witness, NOT a real identity/event observation.
The added work extends the original raw transaction window and consumes its
unchanged 10-second context budget. It is not zero perturbation.

## Reporting and failure precedence

Only the existing fixture cleanup, after both `flushSegments` and
`flushExpirySQL`, reports the new fixed result. The cut never fails/logs through
testing.T, skips the original query or changes original error priority. If
original cleanup itself cannot complete, do not claim that deferred reporting
or original cleanup completed; its original failure remains primary.

Fixed installation results are `SPQI_REACHED_EMPTY_H0`, `SPQI_NOT_REACHED`,
`SPQI_INVALID_INSTALLATION`, and `SPQI_INVALID_VIEW`. NOT_REACHED is a failed
installation acceptance, not proof that a database event was absent. A view
cannot establish native return, putConn completion, physical lock release,
lock ownership or SQLite5/BUSY causality. No original red is repaired/explained.

## Independent source controls and anchors

Before installation, the complete new source control must compile on the
unchanged baseline and fail specifically with `SPQI_AST_INSTALLATION_MISSING`.
A compiler error is not that expected RED. After installation, require source
controls and pure plan controls before executing the selected original case.
Mutation controls must reject misplaced/duplicate/missing cut, too-early arm,
live-App entry, wrong route, early reporting, a 23rd snapshot and altered raw
literal. Each of the 22 numerical anchor mutations must independently fail.

Parse the actual old test with Go AST/token positions. Independently enumerate
the original ordered 22 enclosing-function/cut identities below, match each
actual `slog21Snapshot` call's physical source line to `slog21DiagSites`, and
reject missing/extra/reordered identities. Do not merely round-trip that table.

| Enclosing source function | Frozen ordered cuts |
| --- | --- |
| closedRaw | AfterClose |
| openRaw | BeforeOpen, AfterOpen |
| reopen | ReopenBeforeClose |
| controlWindow | ControlsBefore, ControlsAfter |
| install | TriggerBeforeClose, DDLBefore, DDLAfter |
| clean | CleanupBefore |
| JointOracle | DeleteBefore, DeleteAfter, OracleTarget |
| expectedAfter | PostResponse, PersistedTarget |
| Normal | NormalBefore |
| NativeAbort | AbortBefore |
| Boundaries | BoundaryBefore |
| DeferredFK | FKBefore |
| AuthBoundaries | AuthBefore, AuthAfter |
| TwoAuthenticated | DualBefore |

Keep historical links pinned to their historical commit/line. New physical
anchors are new source evidence, not rewritten historical observations.

## Execution and stop gates

Fixed interpretation: Windows Go1.26.9, modernc sqlite v1.46.2; no pin changes.
Run SQLite-bearing checks serially (`-p=1 -parallel=1 -count=1`). The original
selected case is run exactly once, by an anchored parent/leaf expression.
Do not run the whole Boundaries table, replace the fixture, repeat to obtain
green, or manually rerun/dispatch natural CI. Pure source mutations do not
execute App/DB/SQL and are not new database fault injection.

The first non-expected local or natural red freezes the batch. Retain original
output and fixed labels, report honestly, do not hot-patch or retry. Stop before
submission if the three-file boundary, original semantics or no-extra-lifetime
constraints cannot be proved. Verify source scope, formatting, links, diff,
targeted vet/build, and report actual test results separately from this plan.
No race result is implied by Windows serial success. Root owns exact new
head/base/file/CI re-pinning and original E/F formal same-head read-only review.

## Unimplemented gates and permanent historical boundaries

Gate2 still needs trusted App scope before the selected App opens and Conn
generation from actual delegated construction. This fixture selector is not
that provenance. Gate1 still needs a separately approved finite reader for
financial's actual observer/pass binding. Gate3 still needs success-only native
Tx registration before the proxy returns to Go; check err before interpreting
a possibly typed-nil failure interface and preserve failed/retried Begin pairs.
No reader/opener/worker/driver wiring or real Tx association is installed here.

[PR108 Code509](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254)
remains FAILURE, 23=19success+3skipped+1failure; seven historical leaf reds,
overall NO-GO, and E's original P3 remain unchanged.
[PR106 Code508](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012)
and [PR100 Code501](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209),
[PR102](https://github.com/surpaimb/cpa-cloud/pull/102),
[PR103](https://github.com/surpaimb/cpa-cloud/pull/103),
[PR104](https://github.com/surpaimb/cpa-cloud/pull/104) and
[PR105](https://github.com/surpaimb/cpa-cloud/pull/105) retain their reds and
unknown causes. New green cannot wash them green. No product GO, real migration,
GOV-02, real credential/data, Ready/merge/tag/package/deployment authority.

## Separate qualification repair batch, 2026-10-21

The previous uncommitted candidate is frozen in its original isolated tree;
its original case ran once and failed with `SPQI_INVALID_INSTALLATION`.
The saved `candidate-once.jsonl` has SHA256
`ee6db9eeccdd3a86d25b2591432db991ce9e89ec250a5353fb50b00994dde850`.
It contained no `SLOG21_UNEXPECTED_*` label; FINAL_CLEAN schema/raw-close OK
records do not make the failed installation acceptance pass. Original E/F
independent read-only checks confirmed qualification/strict-route conflation,
not an explanation or repair of any historical SQLite BUSY failure.

This new isolated branch starts at the same PR109 head. Before editing, all
three candidate files were copied byte-for-byte and hashes compared:

| Frozen previous-candidate file | Original and copied SHA256 |
| --- | --- |
| This contract | `2f8131aa55029f3335f167b8c0e653ec6f1744a75e8a5e4f3441cc08784745c2` |
| Original confirmation test | `58197193e7af72900268f41e15a54d83d0b5a8449768fab72ae3687dc20fd8c5` |
| Installation control test | `6545fcc8a91512a1d1489f31550a4c1926e08bed0f7cb04b41990a09f5e4510c` |

First add a deterministic, database-free lifecycle control to that erroneous
copied candidate: allocate -> legitimate unarmed REOPEN -> arm -> FINAL_CLEAN
-> cut. It must compile and fail with `SPQI_LIFECYCLE_PREARM_REOPEN`; this is
not another execution of the frozen original candidate and not a compiler RED.

The bounded repair separates qualification (`route`, called identically by
original closedRaw and lifecycle controls) from `strictRoute`. Nil/unarmed
qualification returns nil without changing flags, acquiring a view or routing.
After arm, qualification delegates to the unchanged strict rejection checks.
Strict pre-arm invocation, wrong selected stage, duplicate route/cut, cut
before route and duplicate arm remain invalid. Arm never clears invalid;
report never suppresses it. A missed arm is NOT_REACHED, not success.

Require the full-prefix control plus proof that a true strict anomaly survives
later arm, missed arm stays NOT_REACHED, and bypassing/deleting qualification
would reproduce the prefix RED. This does not weaken selected strict negatives.
Original confirmation source, 22 anchors and 54 raw literals remain as copied;
only this new control file and this contract change relative to the frozen
candidate. All three remain the complete scope relative to PR109.

Only after all pure/source/boundary gates pass may this new isolated candidate
receive its own once-only original-case acceptance. Preserve the old red
forever, irrespective of its result. Any first unexpected new red stops this
batch without hot patch/retry/commit/push. Only all-local-success permits one
commit/push/new Draft PR, stacked on PR109, then root re-pinning and original
E/F same-head formal review. Gate1/2/3, driver/putConn/BUSY cause, product and
production remain unimplemented/NO-GO.

### New isolated batch local evidence

Windows Go1.26.9 serial/non-cached execution on 2026-10-21:

- The full-prefix control compiled against the copied erroneous runtime and
  produced the expected `SPQI_LIFECYCLE_PREARM_REOPEN` RED. Saved JSONL SHA256
  `363c5a3376a3bdd7e856140cd5aeb02d000d0b38d95cf564433a8ea6bab7e01e`.
- After qualification separation, five pure/source parent tests and 46 children
  (51 named run/pass pairs) passed, no fail/skip. This includes all 22 physical
  anchor mutations and the qualification-deletion mutation. Saved JSONL SHA256
  `f058f2d506ca4ae4ffbf1c95c14eac129672e4e2249c348d743faeb4a00431a8`.
- The new isolated candidate's original pre-commit-refusal case ran once only:
  one parent/one leaf each run/pass once, no fail/skip, exit0, fixed delayed
  `SPQI_REACHED_EMPTY_H0`. Saved JSONL SHA256
  `18e8fa03aa7480e55dccedd2b37ca462a1f06d08c648b04cd7bbfeb23b6bee1b`.
- Targeted service vet/build, gofmt, two relative document links and diff check
  passed. The original confirmation file remains byte-equal to the previous
  failed candidate. Removing the authorized 13 inserted lines and normalizing
  only the 22 physical line numbers reconstructs the exact PR109 baseline.
  The strict route body, arm, cut, status and report retain their old bodies;
  the strict pre-arm negative now directly invokes strictRoute, with its old
  invalid assertion unchanged. All 54 original raw literals remain unchanged.

This is new Windows serial installation evidence, not race coverage, a restored
old-batch acceptance, driver/pass/Conn identity evidence or a SQLite BUSY cause.
No old red candidate was edited or rerun. No Gate1/2/3 or product GO follows.

## Provenance

New contract and test controls are independently authored from this project's
fixed source/contracts and root's explicit bounded permission. No archived
CPA/CLIProxyAPI/Sub2API implementation or tests were used; no strict clean-room
claim. No new dependency. Go AST/parser/token/format semantics follow the
[fixed Go1.26.9 standard library](https://github.com/golang/go/tree/go1.26.9/src/go)
(BSD-style); inherited modernc sqlite retains BSD-3-Clause. Existing A remains
unmodified and is reused, not reimplemented.
