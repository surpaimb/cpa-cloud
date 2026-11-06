# Lock observer consumer and binding contract

Status: **CONSUMER_BINDING_DOC_CANDIDATE_ONLY**.
Date: 2026-11-01. This is one independently authored local document candidate.
No consumer, session type, fixture, native actor or runner is implemented here.
No compilation, Rust invocation, test or lock observation is performed.

## 1. Frozen inputs and authority

This contract supplements the
[PR134 create-only lock observation contract](employee-config-create-only-lock-observation-contract.md),
without editing or replacing it. Its document blob is
0ba8ff3b58dd0e1a8d34f5c75c15ab5c6d8c49ac, mode 100644.
That contract's earlier transfer permissions describe earlier bounded batches;
they grant no publication or execution authority to this new local batch.

| Input | Frozen value / scope |
| --- | --- |
| PR134 | open/Draft, head ebaa70c02890ca772283e76c61c78e80e7d08548 |
| Stacked base PR133 | f9c22ad4e2d7936b70ee85fe1ed79b52d6d751db |
| PR134 body at admission | 9662 characters; read-only, not edited here |
| Clean local base HEAD | 2e3a85d7b4e9451d49881f8473ddad1c79e79541 |
| Local base parent | d0d2b1a10690b2920d71e59c7ea4f54d79e5008d |
| Complete local / remote base tree | 6aac965a8ce766e9c378b4f6b8801eaad81b460d |
| Separate uncommitted observer candidate, raw SHA-256 | c2da43f9b6db39b949360111689a8469bcf55528d1de77bdb977b94b6996b014 |

The local and remote commits have different metadata SHAs and parents, despite
the equal complete base tree. Do not represent them as one commit. The new
document is added in a fresh detached worktree from that clean local base.
The separate observer candidate is outside this checkout and is not modified,
cleaned, staged, committed or transported by this batch.

The [base engine source](../../desktop/employee-config-switcher/crates/employee-config-core/src/live/engine.rs)
is blob f00d5282499c00ffaca7f92dc7dacb31b9441715, mode 100644.
It does not contain the separate uncommitted observer additions. Primitive
facts below refer only to the previously frozen local candidate identified
above, not to an observer already merged or present in the PR134 tree.
Any primitive hash drift invalidates this consumer contract's input.

## 2. What the current primitive actually reports

The private cfg(all(test, unix)) candidate uses one thread-local Cell<State>,
initially Disabled. It records only a real failed-flock branch after open.
The original Io error is saved before descriptor metadata is queried; observer
errors do not replace the original returned operation error. The existing
caller discards record's control result. No existing caller arms the slot.

| Operation / state | Frozen behavior | Consumer consequence |
| --- | --- | --- |
| arm on Disabled | returns Ok and changes the current thread's slot to Armed | establishes only local arming, not actor or object binding |
| arm on Armed, Captured or Repeated | AlreadyArmedOrUndrained | reject; do not drain someone else's slot or rearm |
| record on Disabled | returns Ok without capturing | record Ok is not capture evidence |
| record on Armed | captures a Failure, possibly with MetadataError | Captured is not necessarily successful identity observation |
| record on Captured or Repeated | returns Ok, keeps the first Failure as Repeated | duplicate recording is rejected, not a usable first capture |
| take with TLS access | returns Ok with the prior state and replaces it with Disabled | take Ok is not capture evidence; the evidence is destructively consumed |
| arm, record or take TLS access failure | Unavailable | reject / incomplete, not a successful empty observation |

Failure contains only descriptor number, flock_result, saved flock_error
(kind and optional raw_os_error), and ObjectObservation. ObjectObservation is
either Identity { device, inode } or MetadataError(ErrorClass). State may be
Disabled, Armed, Captured(Failure) or Repeated(Failure). These copied values
do not own an FD, a guard, a session or an authorization to reuse the record.

A pure future classifier could reject bad shapes, but even
Captured(Identity { device, inode }) is only a necessary shape. It does not
prove the intended case, phase, actor, native thread or real open attempt.
A matching numeric descriptor or matching dev/inode alone is insufficient.
The current primitive cannot supply a full binding receipt by itself.

## 3. Expected failed-flock phase: single-session consumption

This section specifies only the expected CONTENDED_A failed-flock phase.
It is not a current executable API. Every step requires a separately reviewed
same-thread session and independently frozen expectations; these are NOT_READY.

1. Before arming, freeze the case, phase, source identity, synthetic root role,
   actor identity, expected lock object's independently held dev/inode and
   platform-specific expected error. Obtain object expectations from the real
   holder's still-owned descriptor, not by copying the prospective capture.
2. Establish exclusive ownership of the current thread's observer session.
   Call arm exactly once there and require success. An arm error consumes
   no permission to call the real recovery entry and is immediately STOP.
3. On that same actor thread, call the one sealed real recover_create_only
   entry exactly once for the planned root. No callback-selected substitute,
   helper retry, second recovery or unrelated lock operation may intervene.
   The session must not move to another thread, async task or process.
4. Save the actual typed recovery result without changing its error. After
   the sole call returns, call take exactly once on the same owning thread.
   Preserve that returned snapshot; a second take cannot repair or confirm it.
5. Apply the conjunction below. A missing clause or receipt is reject/STOP.
   No successful-looking subset may be promoted to a full observation.

Acceptance of this phase requires all of the following:

- The own arm succeeded, the real-entry call count is exactly one and the
  terminal take count is exactly one, with the prescribed same-thread order.
- take returned exactly Captured whose object is Identity, not MetadataError.
- The actual operation returned the exact planned TransactionError::Io
  category; its kind and raw OS error agree with the saved flock_error.
  Both must independently match the pinned native expectation. The frozen
  macOS profile expects WouldBlock / raw OS 35; no Linux numeric substitute
  or cross-platform inference is authorized. Native profiles remain NOT_READY.
- The captured real flock_result agrees with the independently frozen failed
  syscall result. Preserve its exact value; is_err, a lock-leaf Io path or
  WouldBlock alone does not identify a successful open followed by failed flock.
- The observation came from the borrowed real FD after that open and before
  its failure path closed it. Saved operation result/error precede metadata.
  The expected holder object and captured object match exactly by dev/inode.
- All case, role, process, native-thread, session, real-open and source bindings
  are complete and agree with the independently frozen expectation.
- The journal, target, barrier, native exit, complete raw capture and cleanup
  facts also satisfy the original observation contract for this same case.

The last binding clauses cannot currently be proved from Failure. Therefore
today's primitive plus this document cannot yield full CONTENDED_A admission.
Even a hypothetical successful shape classifier remains
CAPTURE_SHAPE_ONLY / BINDING_NOT_READY, never a native observation PASS.

Reject Disabled, Armed, Repeated, MetadataError and Unavailable for this
failed-flock phase. Also reject unexpected operation returns, absent saved
error components, wrong result/error/object, partial binding, duplicate or
foreign receipt, failed arming, incomplete cleanup, and an unobserved exit.
An observer control error must remain separate from the original operation
result; neither may be rewritten to make the other match.

## 4. Other phases require different predicates

Do not apply CONTENDED_A's Captured-only rule to every operation. The primitive
runs only on failed flock. A successful acquisition followed by journal
validation does not pass that branch and may legitimately leave an armed
slot as Armed. That is no failed-FD observation and is not by itself a
failure of the expected CorruptJournal phase.

| Phase | Independently required operation / evidence | Failed-flock slot boundary |
| --- | --- | --- |
| Preparation | exact InjectedCrash from AfterBegin, complete Begin journal, owned guards released | any Err is insufficient; no inference from an empty slot |
| HOLD_A | actual acquire_transaction_lock succeeds once; live owned guard and held-FD identity acknowledged | a failed-flock capture cannot prove holder success |
| CONTENDED_A | exact Io/WouldBlock, fully bound failed-opened-FD receipt, holder still held | strict section 3 predicate only |
| INDEPENDENT_B | real recovery once on independently prepared B, exact CorruptJournal; distinct root/lock identity and preserved journal | separate successful-acquisition / journal predicate; no forced Captured requirement |
| RELEASE_AND_JOIN | every owned/inherited/duplicated lock FD accounted for, holder released and actors reaped | a take/reset or requested drop is not lock release proof |
| UNCONTENDED_A | after verified release/join, real recovery once on the same malformed A journal, exact CorruptJournal | separate predicate; an Armed no-hit state is not a failed-FD capture |

The separate predicates and any observation they need remain NOT_READY.
If a later reviewed design arms a non-failed-flock phase, its one owned take
may return Armed as an expected no-hit fact, but must not call that successful
identity capture or borrow a previous Captured record. Disabled is not proof
that that phase was properly armed. Unexpected failed-flock evidence or an
unexpected return stops the phase rather than being relabelled as its success.

A same-process real holder also owns PROCESS_TRANSACTION_LOCK. Another thread
can wait at that blocking mutex before reaching flock, including for another
root. The original separate-process holder/contender/B topology remains
required; neither TLS nor a new label removes the process-mutex stage.
Different-root success cannot establish absence of historical contention.

## 5. Missing binding and the real-open boundary

The required fields below are logical future receipt requirements, not new
fields implemented by this document or by the frozen Failure type.

| Binding | Required origin / constraint | Current gap |
| --- | --- | --- |
| Case / root / phase | pre-frozen J01-J06 case and synthetic role, bound to the actual operation | not in Failure |
| Actor / process generation | controlled spawn ownership, native PID and parent/child/exit receipts | not in Failure; PID text added later is only asserted context |
| Native TID | platform-native measurement on the actual actor thread at the bound boundary | not in Failure; Rust ThreadId is not a native TID |
| Local session | exclusive owner, successful arm, one entry and one terminal take; no transfer or reuse | plain Cell<State> has no session token or owner |
| Actual open attempt | strictly increasing real lock-open sequence assigned at the actual open boundary, with actor/session binding | not in Failure; call ordinal and record count are not open ordinals |
| Descriptor / object | borrowed actual FD, number, held dev/inode, duplication/inheritance/close accounting | Failure has only a numeric FD and snapshot; no ownership or close receipt |
| Source / runner | exact fixture and primitive hashes, executable/tool/image/profile identity | not in Failure and not supplied by a docs-only source tree |

A controller can freeze ExpectedBinding before spawn and a future wrapper can
associate context with a call. Neither creates the missing internal witness.
Do not attach case/actor/PID/TID/open labels after return and describe them
as captured at the real failed FD. Do not compare a record against expectations
derived from that same record. Equality of self-supplied labels is not binding.

The real open sequence must cover actual opens, including any unexpected
intervening attempt, not merely count calls to recover or to record. Whether
and how it can be assigned without changing production behavior requires a
new reviewed boundary design. A number allocated after return is inadequate.

The numeric FD may be reused after close and has meaning only within its
bound process/open lifetime. Matching FD numbers in different processes do
not establish one opened object or one open-file-description relationship.
Caller-after-return pathname stat, reopen or metadata on a reused FD cannot
replace the closed original FD's observation. Matching dev/inode identifies
the measured object within the receipt's scope, not the actor or session.

Future binding work must preserve the original saved error and return, the
existing FD/guard lifetime, guard field order and acquisition/drop scope.
No dup, extra diagnostic open/flock/unlock, FD ownership escape, new held
guard, logging callback or blocking barrier at capture is admitted here.
A new real-open/context interface would change the frozen primitive input
and needs fresh source hash review; it cannot be hidden in a consumer label.

## 6. Session ownership, terminal handling and TLS isolation

A prospective LocalSession would be single-owner, non-copyable and bound to
one actor thread, with one terminal consumption. ExpectedBinding would be
immutable and independently constructed. A sealed once-only real caller and
a pure rejecting classifier would have distinct responsibilities. These
names describe requirements, not compilable Rust declarations or type proofs.
No !Send / !Sync property, panic path or Drop cleanup has been compiled or
executed. All session ownership and termination mechanisms remain NOT_READY.

The current take resets the slot even when it returns a bad state. Preserve
the first result and sticky rejection before any terminal disposal. Never
clear/rearm or take again to turn missing/Repeated evidence into a passing
record. An arm failure does not authorize taking or clearing a foreign
undrained session. Nested/reentrant consumers and thread handoff are blocked.

A later design must distinguish normal one-take completion from abnormal
termination. If the sole entry panics, times out or never returns, it must
not call recovery again to obtain evidence. Any exceptional owned drain can
occur at most once, only on the verified owning thread and only if no take
has already occurred; it is cleanup, not a new admission attempt. If safe
ownership/access cannot be proved, report UNKNOWN/incomplete and quarantine
that actor/session. Do not fabricate a terminal state or reuse its thread.
Automatic TLS destructors are not a complete termination/cleanup receipt.

Each controlled actor must arm and consume its own TLS on its own executing
thread. Arming in the controller does not arm a child process or another
thread. A child has no admission from a parent's copied state or receipt;
process creation, including any proposed fork-based path, needs its own
reviewed initialization and evidence protocol. Cross-process TLS is not IPC.
Parent collection of child records must preserve actor identity, sequence,
EOF and exit receipts without pretending to take the child's TLS slot.

Slot reset is independent of holder FD closure, kernel lock release, actor
termination and raw-stream EOF. The original owned-resource cleanup budget
and receipts remain necessary. Never search for or kill an unknown holder.
On first mismatch stop normal progression; preserve the first evidence and
perform only already authorized owned-resource cleanup, never a retry.

## 7. Runner and execution admission remain closed

This document does not add a test target, actor entrypoint, IPC protocol,
observation invocation, receipt parser or runner. It does not authorize
publication of a fixture-bearing source PR. The original default-ignore
proposal and exact single-test selection are still unimplemented admission
requirements; zero selected tests, ignored-only output or exit 0 is not PASS.

Before any later execution decision, root and original E/F must separately
review the actual bounded source changes and complete at least these pins:

- The precise primitive/consumer/fixture source and whole tree, and proof the
  only real entry and real-open boundary have the reviewed binding semantics.
- Native OS/version/architecture and runner image, exact existing cargo/rustc
  paths, versions and hashes; Rust 1.90.0 baseline without fallback/install.
- Actor topology, native PID/TID acquisition, owned FD and open-sequence
  receipts, independent holder object expectation, barriers and terminal paths.
- Frozen one-invocation argv, monotonic clock and absolute deadline, dual-stream
  raw byte capture/hashes, EOF/native exit, bounded schema and cleanup proof.
- The original contract's 900-second total envelope, bounded barriers/joins,
  8 MiB per stream / 16 MiB combined, event/receipt bounds and stop-only cleanup.

Every item is **NOT_READY** here. The real-open boundary, session ownership,
termination/cleanup, cross-process actor wiring, runner/tool/raw evidence and
single actual execution admission remain NOT_READY. Static document and link
checks cannot validate native errno, lock contention/release, descriptor
lifetime, TLS behavior, compilation or the one-test raw receipt.

An engine.rs source publication would naturally select the existing employee
config workflow's full old core suite. It is not an observation-only entry.
Do not change workflow paths, serialize the old suite, ignore old cases,
accept WouldBlock in place of CorruptJournal or alter production guard order
to bypass #410. No manual dispatch, rerun, old-case execution or actor is
authorized by this contract. This batch does not invoke cargo/rustc/rustfmt.

## 8. Retained red, unknowns and narrow review boundaries

The frozen first red remains Employee config switcher #410, run41282766745,
attempt 1, macos-15 job124777872298, Run platform core tests:
live::engine::tests::oversized_journal_fields_are_rejected_and_preserved
expected CorruptJournal but received lock-leaf Io / OS 35 / WouldBlock.
The harness reported 235 passed, 1 failed, 1 ignored, native exit 101.
PR133's exact-head checks remain 15 = 8 success + 6 skipped + 1 failure.
Skipped was not executed. New payload/parser successes and other successful
legs never cancel that failure. No historical raw log is fetched here.

The historical holder PID/TID/FD, which J01-J06 input was active, open-file
description relationship and acquisition/release schedule remain UNKNOWN.
Unchanged source does not prove a concurrency, guard-drop or parser cause.
A later controlled observation could establish its own measured relations
only; it cannot reconstruct #410 or retroactively supply missing evidence.
InjectedCrash is a controlled return boundary, not a real process crash;
synthetic malformed bytes or pure classification are not I/O fault injection.

Root's pinned PR134 current-head snapshot records zero natural runs and zero
checks: NOT_TRIGGERED / NOT_EXECUTED, not green. Its original E/F 0/0/0
conclusions apply only to DOCS_ONLY_OBSERVATION_CONTRACT static narrow review,
with neither reviewer running tests. This new consumer document has no new
formal E/F conclusion. Do not borrow review or run evidence from another
head, the separate primitive candidate or a stopped review batch.

Original D's earlier Git-ref-error STOP and the other retained tool/review
STOPs remain stopped; a separate later review cannot retroactively complete
them. No current D result is inferred here. PR133's CI/dynamic red and all
historical UNKNOWN facts remain unchanged.

All PRs remain Draft. Real configuration recovery, W3/W4, AEAD/source/host
proof, real migration, product and production remain **NO-GO**. The maximum
claim for this document is CONSUMER_BINDING_DOC_CANDIDATE_ONLY, pending root
and independent E/F review before any decision about publication.

## 9. Independent sources and local validation boundary

This contract is independently written from this project's frozen requirements,
the previously reviewed local primitive facts and official Rust documentation.
It copies no archived or third-party implementation, tests or fixtures.
No new dependency or license change is introduced; no strict clean-room
claim is made. The original observation contract retains its own sources.

| Official source, read-only verified 2026-11-01 | Limited use |
| --- | --- |
| [Rust ThreadId](https://doc.rust-lang.org/std/thread/struct.ThreadId.html) | Rust's opaque logical thread identity is not interchangeable with the platform thread identifier; it cannot stand in for required native TID evidence |
| [Rust LocalKey / try_with](https://doc.rust-lang.org/std/thread/struct.LocalKey.html#method.try_with) | thread-local access is local to the executing thread; access can fail; TLS destruction has platform caveats, so it is not a terminal cleanup receipt |

The served official std pages identify version 1.99.1 at this read. They are
semantic references, not an attestation that pinned Rust 1.90.0 compiled or
executed anything. No native PID/TID API or platform behavior is inferred
from these pages; those interfaces still need a reviewed native profile.

This local batch permits only this new file, document-link checks and read-only
Git scope/diff/identity checks. Validate the relative links against the frozen
base, retain raw UTF-8 byte count/SHA-256 and canonical Git blob identity, and
report the clean base tree separately from the uncommitted document delta.
Do not stage, commit, push, create a remote PR, edit any PR body, invoke CI
or alter the original contract, primitive, tests, dependencies or workflow.
The first unexpected tool/check error or identity/scope drift is STOP, with
no hotfix, retry or recovery mutation. Any unfinished gate stays NOT_READY.
