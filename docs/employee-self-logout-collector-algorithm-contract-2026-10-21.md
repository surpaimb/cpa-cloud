# Self-logout collector algorithm A: synthetic test-only contract

Status: new independent A implementation contract, 2026-10-21. This contract
is frozen before implementing/running the controls below. It authorizes only
this new document and `internal/service/self_logout_collector_20261021_test.go`.
The latter is `package service`: the actual collector and its synthetic controls
share one file, so future same-package driver tests can reuse this implementation
without a shadow collector. Existing files, PR107 and PR106 remain unchanged.

Base/sole parent: [PR107](https://github.com/surpaimb/cpa-cloud/pull/107), exact
head `b78c85b5c1ff1399f7d10e101040205746423346`, itself based on PR106
`41fa9e3b35b981b23727d7d9d725aead065c462e`. The
[driver-boundary contract](employee-self-logout-driver-boundary-observation-contract-2026-10-21.md)
is preserved. A does not implement B's identity bridge or C's driver/opener seam.

## Authority and exclusions

Go **1.26.9** is required for executing these controls. The collector imports
no App/DB/driver/sqlite API, performs no SQL, connection acquisition, context
cancellation, timeout adjustment, retry, pooling, forced Close or GC. It does
not intercept any real invocation. Service-package testing necessarily links
existing service dependencies and their init functions; it is not an empty
process with no sqlite package initialization. No real DB, credentials, service,
fixture process or client is opened by the new controls.

Only `_test.go` contains new Go code. `go build` excludes that file. A later
same-package test can call the actual unexported collector directly. A separate
regular `.go` helper package would expand the source surface; a separate
`_test.go` package would not be normally importable. Neither is authorized here.

## Fixed storage and evidence budget

Maximums are C=8192 invocation slots and K=128 immutable cut views, together
with the existing symbol bounds: App16, Conn generation32, Tx128 and pass128.
Rollback requires all four nonzero; Conn.Close requires App/Conn only, with
Tx/pass NOT_APPLICABLE (zero). A validates these symbols, not real associations.

Constructor sizes N/K are selected once from [1,C]/[1,K] by a frozen control;
they cannot grow or be retuned after failure. Full future integration remains
subject to PR107's fixed ceilings and a separate adequacy review. Each collector
owns preallocated slots, views and one contiguous row backing array. No ring,
reuse, reset, wrapping counters, global registries or sync.Pool is allowed.

Define collector-owned object bytes as sizeof(collector) plus N*sizeof(slot),
K*sizeof(view), and N*K*sizeof(row), including slice headers in the collector.
At the maximum this must be **<=16 MiB** on the supported toolchain/architecture.
The storage control checks the actual layout using only `unsafe.Sizeof`, not
unsafe pointer access. Allocator rounding/metadata, GC/runtime stacks, test
harness/caller tokens and whole-process RSS are explicitly outside that number.
No bounded-memory or timing claim is made for an arbitrary number of fixtures.

At most 8192 slots and 128*8192=1,048,576 view rows exist per collector. Row
copies contain only fixed scalars/enums. There is no trace dump, raw error,
address, path, DSN, SQL, identity, credential or arbitrary string payload. Test
failures use fixed SCA_* labels; normal test framework output is not per-case
native/driver evidence. Construction may allocate; core operations contain no
allocation/growth, logging, blocking callback, channel, mutex, wait or drain.

## Admission state and finite strong-CAS proof

One atomic.Uint64 gate holds a low14-bit count n and high-bit SEALED. Other
bits are invalid. The only valid changes are (open,n)->(open,n+1), n<N, and
(open,n)->(sealed,n). Count never decreases, sealed never clears, slots never
reuse. Fault flags are twelve separate monotonic atomic bools, not gate bits.

Reserve and Seal each use at most **N+2** iterations, each with one gate Load
and at most one strong CompareAndSwap. Successful CAS is the linearization
point. Reserve wins a unique ordinal or observes sealed/full; no Add/rollback.
Seal preserves n and is idempotent. Post-seal Reserve only sets lateAdmissionSeen
and returns a no-record handle. Overflow sets a fixed hard failure without
changing the count. Suppressed handles make later publication a no-op.

Every failed strong CAS implies at least one intervening irreversible gate
change. There are at most N count advances and one seal, so N+2 iterations
permit a final terminal observation. No ABA or spurious-CAS-failure assumption
is introduced. Bound exhaustion is a hard invariant failure, not ordinary
contention. Single failed CAS does not fail acceptance or suppress a producer.

This bounds Go-level calls, not processor/OS/GC scheduling or elapsed time.
At C the conservative reservation/seal ceiling is 16389 atomic operations;
16k-scale work can perturb timing. This is not physical wait-freedom, zero
observer effect, or a claim that the old BUSY interleaving is unchanged.

## Unique phase writers and two-stage publication

Each slot has independent ENTER and RETURN atomic phases EMPTY->WRITING->READY.
A one-shot CAS obtains each phase's sole write right. Payload is written before
the READY Store and never afterward. Duplicate writers fail before payload
access. RETURN must first Load ENTER READY. Native panic/return handling is not
implemented here: the synthetic no-record/hard-failure status cannot authorize
changing a future observed invocation's inputs, calls, return or panic.

Tokens contain this collector's owner, reserved index and fixed metadata.
Receiver/owner/index/reservation and metadata checks reject mechanical misuse.
This is not an in-process unforgeable capability or proof of semantic ownership;
malicious same-package code is not a security boundary. Token/phase mutation in
explicit corruption controls is synthetic, quiescent and never a driver hook.

PublishEnter uses at most3 Go-level atomics; PublishReturn at most4. No-record
handles cannot touch a slot. The result enum is only OK/ERR_BAD_CONN/
CTX_CANCELLED/CTX_DEADLINE/SQLITE5/SQLITE_OTHER/OTHER; no real error classifier or
worker context bridge exists in A. Both future native methods take no context
argument; no worker context is guessed here.

## Cut, seal and happens-before

Cut IDs are preassigned from [0,K). A one-shot view claim prevents concurrent
duplicate writers. One gate Load selects H; only slots [0,H) are considered.
The actual scan loads RETURN phase **first**, ENTER phase **second**, exactly
once each, and copies only payload whose corresponding READY was observed.

| Return phase / Enter phase | Row |
| --- | --- |
| EMPTY / EMPTY or WRITING | RESERVED_AT_CUT; completion UNKNOWN |
| EMPTY or WRITING / READY | OPEN_AT_CUT; completion UNKNOWN |
| READY / READY | both published payloads observed |
| WRITING or READY / not READY; invalid phase | fixed hard failure, abort scan |

A READY Load observing its Store synchronizes after the payload write, so
ordinary immutable payload reads are safe. The producer orders ENTER READY
before any RETURN publication. Sequential consistency therefore makes the
fourth row impossible in a valid return-first scan. In contrast, ENTER-first
can see EMPTY, then a producer publishes both, then see RETURN READY: that is
a legal mixed observation, not a lost record. A direct counterexample control
must preserve this distinction. Return-first can legitimately see RETURN EMPTY
then ENTER READY; it reports OPEN, never reloads/drains toward completion.

Each view owns value copies, not pointers into live payloads. A READY view is
immutable; readers copy rows/summaries by value. An in-progress view is simply
not ready. Cut is a per-slot vector over an interval, not a global instantaneous
snapshot. Seal freezes admission only. A sealed scan may observe an existing
producer's publication after seal; it is not COMPLETED_AT_SEAL evidence.
Every ready sealed summary explicitly retains POST_SEAL_UNKNOWN, even if all
its rows are complete. A non-complete row's zero result is unavailable storage,
not evidence of an OK return; consumers must first check the row state.

At most2H+17 atomic calls and8H shared-slot payload scalar reads occur in Cut
(first hard error aborts; remaining rows stay NOT_SCANNED). The latter counts
shared payload acquisition, not all fixed-size local/return/view value copies
or compiler instructions. Those copies have constant-size types and no dynamic
length traversal. Seal+Cut is <=32790 atomic calls at C.
Late/each-fault flags are themselves read once, at their own observation points.
No all-clear-at-this-cut claim excludes later faults/publications.

Tokens and view handles retain the collector naturally. Seal never frees,
zeros or closes storage. Existing producers may publish late; post-seal new
attempts receive no slot and set only lateAdmissionSeen. Its false value never
excludes existing tails or future publication. Existing cuts remain unchanged.
Late callbacks never log, assert, call testing.T or use a newly added wait.

RESERVED/OPEN/POST_SEAL_UNKNOWN and legitimate contention are not hard failures.
Overflow, gate/bound corruption, invalid required symbols/enums, foreign/invalid
token, duplicate publication, return-before-enter, illegal phase and duplicate/
out-of-range cut are fixed hard failures. No evidence is silently overwritten.
Conn.Close without pass/Tx is intentionally valid. A hard fault does not grant
authority to skip or retry an observed operation; a synthetic delegate/panic
control must demonstrate that separation.

## Frozen controls and commands

Exactly these15 top-level tests (no generated subtests/fuzz) are planned:

1. TestSelfLogoutCollector20261021Admission
2. TestSelfLogoutCollector20261021SealInterleavings
3. TestSelfLogoutCollector20261021PublicationCuts
4. TestSelfLogoutCollector20261021ReadOrderWitness
5. TestSelfLogoutCollector20261021ConcurrentUnique
6. TestSelfLogoutCollector20261021ConcurrentSeal
7. TestSelfLogoutCollector20261021Capacity
8. TestSelfLogoutCollector20261021CutCapacity
9. TestSelfLogoutCollector20261021InvalidInputs
10. TestSelfLogoutCollector20261021DuplicatePublish
11. TestSelfLogoutCollector20261021TokenOwnership
12. TestSelfLogoutCollector20261021InvalidPhases
13. TestSelfLogoutCollector20261021HardFailureDoesNotIntercept
14. TestSelfLogoutCollector20261021StorageBounds
15. TestSelfLogoutCollector20261021StepBounds

Controls operate on the actual collector and the same private primitives used
by its public-to-same-package methods, not a second model. Frozen dimensions:
basic/staged/corruption cases N<=4,K<=4; concurrent cases N=256,K=2,8 producers
with32 calls each; full capacity N=8192,K=1; cut capacity N=1,K=128; storage
N=8192,K=128. No sleeps, timing oracle, fuzz or capacity changes after failure.
Harness barriers/joins have5-second deadlines and stay outside core methods.
No whole-Cut progress assertion depends on scheduling an uncontrolled race.

Once non-cached serial command, with the exact Go1.26.9 binary and readonly
module graph, working directory the new isolated tree:

```text
go test -json -p=1 -parallel=1 -run '^TestSelfLogoutCollector20261021' -count=1 -timeout=5m ./internal/service
```

If the local Windows amd64 CGO/GCC race environment is available (it was checked
before implementation), one separate serial necessary race command is frozen:

```text
go test -race -json -p=1 -parallel=1 -run '^TestSelfLogoutCollector20261021' -count=1 -timeout=5m ./internal/service
go vet -p=1 ./internal/service
go build -p=1 ./internal/service
```

Also gofmt only the new test file; verify document links, two-added-file scope
and git diff --check. No full Windows sqlite suite runs in parallel. Record
actual named run/pass/fail/skip counts and command outcomes; do not invent them
from planned controls. A compiler/race/proof/whitelist blocker stops before
commit; no environment expansion or obtaining green by altered capacities.

If accepted locally, one commit/push/new stacked Draft PR only. Record exact
head/base/tree and actual natural CI. Source then freezes. Any new natural red,
including inherited race failures, stops work; retain evidence, no manual
rerun/dispatch/hotfix. Root arranges original E/F independent same-head review.

## Provenance, evidence limits and historical reds

New code/tests are independently authored for this contract and public Go
semantics; no archived CPA/CLIProxyAPI/Sub2API implementation or tests are used.
Prior research means no strict clean-room claim. No new dependency is added.
Go retains its BSD-style license. Primary reviewed sources,2026-10-21:
[Go1.26.9 atomic semantics](https://github.com/golang/go/blob/go1.26.9/src/sync/atomic/doc.go)
and [typed atomics/noCopy/alignment](https://github.com/golang/go/blob/go1.26.9/src/sync/atomic/type.go).
Only signatures, requirements and independent algorithms/controls are authored
here; no standard-library implementation is vendored.

A synthetic PASS would establish only these collector controls. It would not
implement or prove the driver, opener, real Tx/pass bridge, complete late tail,
putConn, physical Close/lock release, BUSY owner/cause, production repair,
GOV-02 or M1-to-M2. Allocation/layout bounds are not whole-process RSS bounds.

[PR106 Code508](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012)
remains red:23=18success/2failure/3skipped, original
[race1](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012/job/119096992890)
and [race0](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012/job/119096993008).
[PR100's old Code501](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209)
and [PR102](https://github.com/surpaimb/cpa-cloud/pull/102),
[PR103](https://github.com/surpaimb/cpa-cloud/pull/103),
[PR104](https://github.com/surpaimb/cpa-cloud/pull/104),
[PR105](https://github.com/surpaimb/cpa-cloud/pull/105) histories stay unresolved
at their recorded exact heads; links in PR107's contract retain those details.
No old PR body is copied or changed. Keep Draft / production NO-GO; no Ready,
merge, tag, package, deploy, attachments or unrelated monitor operation.
