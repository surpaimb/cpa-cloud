# Create-only lock observation contract after PR133's first red

Status: **DOCS_ONLY_OBSERVATION_CONTRACT / NEW OBSERVATIONS UNEXECUTED**.
The initial docs-only local batch added only this independently authored file
in commit d0d2b1a10690b2920d71e59c7ea4f54d79e5008d, then stopped before transfer.
A separately authorized connector batch created Draft PR134, remote commit
024e047626c588fa42e16e70875f03e340be1f79, sole parent PR133
f9c22ad4e2d7936b70ee85fe1ed79b52d6d751db. Its complete candidate tree was
41b1efeae72d9a9029b6422bcb54e37545eaa034 and original document blob was
bdb3f2e090b3575037c330a82d639b8f514a8f62, mode 100644. Those historical batch
boundaries and distinct local/remote metadata SHAs remain part of the record.

This fresh admission-contract revision changes only the same existing document.
It authorizes no fixture implementation, Rust invocation, native actor or
old-case execution, production change or CI dispatch/rerun. Its only conditional
transport permission is the separately bounded same-PR connector fast-forward
in section 7. Original E/F/D read-only review preferred a contract before the
deferred two-file fixture; that fixture remains deferred.

Before the initial document batch, a root dispatch script failed while parsing
JavaScript backticks before actual task delivery or any write. That dispatch
STOP remains history; neither the initial document nor this revision completes
that stopped dispatch. Earlier
implementation/read/identity/worktree STOPs and the body-only generic job-read
tool STOP remain in [PR133's retained record](https://github.com/surpaimb/cpa-cloud/pull/133).
This document changes neither that PR body nor any earlier PR.

At the old PR134 head 024e047626c588fa42e16e70875f03e340be1f79, original F and
a fresh independent E batch each completed formal pure read-only review with
P1/P2/P3 = 0/0/0, only DOCS_ONLY_OBSERVATION_CONTRACT static narrow conclusions.
E's earlier wide-output truncation STOP issued no conclusion and remains STOP.
Subsequent D/F independent read-only pre-review identified future admission
gaps addressed below; it supplied neither execution authority nor a root cause.
E's later official-Rust-document tool-error batch also stopped without a formal
new conclusion. That separate E STOP is retained; this revision does not
retroactively complete either stopped E batch or claim a new formal E/F result.

PR134's existing 6525-character body is intentionally not edited by this
revision. Its old-head terminal facts are history, not CI or review evidence
for a changed head. Root must freshly pin natural CI and arrange original E/F
new independent formal same-head review after any authorized head update.

## 1. Exact historical evidence and source boundary

| Item | Fixed evidence |
| --- | --- |
| PR133 | open/Draft, head f9c22ad4e2d7936b70ee85fe1ed79b52d6d751db |
| Stacked base PR132 | 1081e7c8c30f3ca875c66ce46ddfb986e18fd430 |
| Candidate source tree | ef5baa2980163ed7feefb0e1a8a9e91eb2cfc061 |
| Local source commit | cbbc85d7aa476f4b9b3b80e33e802105c48e6b49 |
| Local source parent | 651d4a482c85def7ca940242b2f3889929725af1 |
| Shared engine.rs blob, mode | f00d5282499c00ffaca7f92dc7dacb31b9441715, 100644 |
| First red | Employee config switcher #410, run41282766745, attempt 1 |
| Failed leg/step | macos-15, job124777872298, Run platform core tests |
| Failed test | live::engine::tests::oversized_journal_fields_are_rejected_and_preserved |
| Observed mismatch | expected CorruptJournal; lock leaf .cpa-cloud-employee-config.lock, OS 35 / WouldBlock |
| Harness result | 235 passed / 1 failed / 1 ignored, native exit 101 |

Root and original D independently read the
[failed job evidence](https://github.com/surpaimb/cpa-cloud/actions/runs/41282766745/job/124777872298).
PR132 and PR133 have the same
[engine source](../../desktop/employee-config-switcher/crates/employee-config-core/src/live/engine.rs)
blob. D also checked that the CI merge tree equalled the PR133 head tree;
this is merge-checkout evidence, not a raw-head executable attestation.
The local and remote source commits/parents have different SHAs; their
equivalent source trees must not be represented as identical commits.

All 24 new payload and 13 existing parser tests passed on that macOS leg.
Linux and Windows legs succeeded. Those facts do not cover the old failing
test or cancel its red; the ignored case was not executed. Exact-head checks
were 15 = 8 success + 6 skipped + 1 failure + 0 pending + 0 other. Skipped
checks were not executed. E/F each reported same-head P1/P2/P3 = 0/0/0 only
for BYTE_PAYLOAD_CHECK_SOURCE_ONLY static review and did not run tests.
Fixed-head CI, dynamic acceptance and production remain **NO-GO**.

The historical log does not identify the lock-holding PID/TID/FD, which of the
six malformed cases was active, an open-file-description relationship, or a
precise acquisition/release schedule. All remain **UNKNOWN**. No evidence here
attributes the red to PR133, parallel tests, a particular guard drop window,
a stale lock-file name, or a journal parser defect. A later controlled result
cannot retrospectively establish those historical facts.

## 2. Existing source facts, without changing behavior

The reviewed source order is:

1. recover_create_only checks the root and calls acquire_transaction_lock.
2. acquire_transaction_lock first takes PROCESS_TRANSACTION_LOCK with blocking
   Mutex::lock; poisoning has a separate process-mutex error path.
3. It checks the lock pathname, opens the lock file, attempts the Unix
   nonblocking exclusive flock, and validates the opened lock/path identity
   and private permissions.
4. Only after acquisition succeeds does recovery inspect workspace/journal
   state and reach journal validation.

An Io error naming the lock leaf is not by itself a measured flock call site:
open, flock and lock-path validation can return errors associated with it.
Preserve their distinct stages and full typed categories. Mutex acquisition,
poisoning or waiting is another stage and must not be labelled file-lock denial.
A lock-file name existing on disk proves neither a current holder nor a stale
kernel lock. A pathname stat does not prove which object a failed actor opened.

The current TransactionGuard declares _process before _file. This document
does not change that order, add early unlock, leak a guard, change mutex scope,
or assert which ordering caused #410. Any behavior change is a separately
reviewed repair, not observation instrumentation authorized by this document.

The old assert_corrupt_journal_is_preserved helper prepares with AfterBegin
but accepts any Err through expect_err. That does not prove the intended
InjectedCrash boundary. The later assertion still strictly requires
CorruptJournal and exact journal preservation. Neither assertion nor the six
existing cases may be weakened, ignored, removed or made to accept WouldBlock.

## 3. Frozen future preparation and six named cases

Everything below is a **planned, unimplemented protocol**, not a measured PASS.
A new fixture/source review and explicit one-execution authorization are required.
Use fresh owner-private synthetic roots A and B for each named case; never a
real configuration, home, backup, credential store or running service directory.
Retain root/workspace/journal/lock object identities, not just canonical names.

Before any malformed bytes are written, each root's real preparation call must:

- use the actual create-only preparation path with AfterBegin;
- return exactly TransactionError::InjectedCrash, not merely is_err or any Io;
- leave exactly the expected private workspace and complete Begin journal,
  with recorded bytes/length/SHA-256 and held-object identity;
- leave the synthetic target absent and no successful publication/stage claim;
- finish its scope with owned preparation guards/FDs released before actors start.

A preparation mismatch is the first unexpected result: STOP before inserting
a malformed event or calling recovery. After successful preparation, freeze
the independently specified Begin + one malformed event bytes for that root.
Record journal bytes, length, digest and identity immediately before recovery.
Do not choose an arbitrary first directory entry as proof of the intended
workspace; duplicates, an unexpected entry or missing identity block execution.

Preserve all six existing malformed-input assertions with explicit event IDs:

| Case | Malformed field represented by the existing test |
| --- | --- |
| J01 | target length MAX_JOURNAL_TARGET_BYTES + 1 |
| J02 | stage length MAX_JOURNAL_STAGE_BYTES + 1 |
| J03 | digest width SHA256_HEX_BYTES + 1 |
| J04 | non-hex digest of the expected width |
| J05 | index 1 as the first planned event, stage-0001.bin |
| J06 | unexpected member in parent_identity |

The future fixture freezes literal input bytes and digests before its run.
It must not use a newly permissive serializer/parser as its oracle. Each case
gets its own fresh roots and journal pair. Which case failed historically
remains UNKNOWN even if one future case behaves differently.

## 4. Known-holder control and independent-root negative control

The controller owns no transaction guard or flock. For each J01-J06, freeze
this event/barrier protocol, with bounded blocking IPC and no sleep or retry:

1. **HOLD_A:** a distinct native holder process calls the real
   acquire_transaction_lock for A exactly once, retains the returned guard,
   records the held FD's identity and acknowledges holder_ready only after
   acquisition. It does not recover or alter the malformed journal.
2. **CONTENDED_A:** a separate native actor calls real recover_create_only(A)
   exactly once while H's guard is held. Require the typed Io/WouldBlock result
   (raw OS 35 on the frozen macOS profile), same opened lock object evidence,
   and unchanged journal bytes/identity with no target publication.
3. **INDEPENDENT_B:** while H still holds A, another independent actor calls
   real recover_create_only(B) exactly once. B has the same chosen malformed
   input but different root/lock (dev,inode) pairs; the device may be shared.
   Require CorruptJournal for B,
   its unchanged journal, and no accidental A/object or global-mutex coupling.
4. **RELEASE_AND_JOIN:** release H through the planned barrier, close every
   owned or duplicated/inherited lock FD, record completion/errors, reap/join
   H and the first recovery actors. A requested drop or sent message alone is
   not proof that the file lock has been released.
5. **UNCONTENDED_A:** only after successful release/join, a fresh actor calls
   real recover_create_only(A) exactly once on the very same malformed journal.
   Require exactly CorruptJournal, unchanged bytes/digest/identity and absent
   target. A second WouldBlock is an unexpected red, not a retry instruction.

Thus each case budgets one contended A recovery, one different-root B recovery
and one post-release A recovery. Preparation calls and holder acquisition are
separate named operations, never hidden repeats of those recovery calls.
An expected controlled WouldBlock is not an unexpected failure; every other
mismatch stops normal progression before the next call. Never sleep-and-retry
until CorruptJournal appears.

A same-process holder of the real guard also holds PROCESS_TRANSACTION_LOCK;
another thread may wait there before reaching flock. Such waiting cannot be
used as a WouldBlock oracle. The distinct-process topology above separates
that process-local mutex from the intended file-lock control. It does not
prove that historical CI used this topology or had a particular contender.

A hand-written flock control, if later separately approved, is a different
labelled control. It must not replace real acquire/recover or be counted as
production-path observation. No extra probing flock, duplicate descriptor,
diagnostic lock guard or inherited FD may silently keep A locked. fstat-style
metadata observation is not permission to acquire/unlock a lock for diagnosis.

## 5. Required event schema and unknowns

Use bounded fixed-schema records, not raw Debug/Display errors or arbitrary
paths/content. Preserve all returned categories, including unexpected ones:

| Field group | Required record |
| --- | --- |
| Binding | case ID, synthetic root role, operation/actor role, source head/tree |
| Order | per-actor strictly increasing event sequence and monotonic elapsed time; collector receipt sequence separately |
| Native actor | actual PID/TID, parent/child relationship and spawn/join/exit outcomes |
| Descriptor | FD numeric value plus strictly increasing open sequence, duplication/inheritance/close events |
| Object | root/workspace/journal/lock dev/inode from the relevant held descriptor, not a later path substitute |
| Phase | preparation, process_mutex, path_precheck, open, flock, path_validation, journal_validation, release, close, join, capture |
| Result | exact TransactionError variant; for Io, io::ErrorKind and raw_os_error or explicit absence |
| Journal | before/after length, digest, identity and byte equality; target absent/published state |

FD numbers can be reused; the open sequence and held-object identity are
required together. PID reuse or process-local clocks must not be silently
treated as global identity or a cross-process total order. Barrier acknowledgments
establish the claimed happens-before relations; stdout interleaving does not.
Record the actual clock source/units and any cross-process comparability limit.

Mark an unobserved stage, lock holder or descriptor identity UNKNOWN rather
than infer it from an Io path, errno, elapsed bucket or later inode. For the
known-holder same-object control, missing required FD/object evidence makes
that control incomplete, not a PASS. The controller must not search for or
kill unrelated host holders.

### 5.1 Failed-open-attempt descriptor identity: future borrowed observation

The future test-only observer interface is NOT_READY and requires a separately
reviewed fixture/source batch. Its logical record binds case/actor/stage,
attempt open sequence, the borrowed real FD, held-object dev/inode, saved
operation result/error and a separate observation outcome. This is an interface
contract, not an implemented Rust API or a new production admission type.

For a successful real open followed by a failed flock, the required identity
must be observed through that exact FD after open and before the failure path
closes it. A caller-side path stat after acquire/recover returns cannot prove
that closed FD's object, even if its later inode happens to match. If open
itself failed, record no opened FD and the saved open error; do not invent an
identity or call metadata operations on an invalid descriptor.

Immediately save the real flock return value and, on failure, its errno/typed
Io error before any observer metadata read, clock call, logging or capture.
Observation can itself change errno or fail. Record that observation error
separately; it must not replace, clear, relabel or otherwise change the
original returned TransactionError variant, Io kind or saved OS error.
Missing/failed identity observation makes the control incomplete/STOP, not a
different production result and not a reason to repeat the operation.

The observer may only borrow within the real FD's existing lifetime. No dup,
extra diagnostic open/flock/unlock, descriptor inheritance, ownership escape,
extra lock guard, blocking barrier/log write at the observation point or
extension of the FD/guard/lock lifetime is authorized. Preserve _process/_file
guard field order, acquisition/drop scope and the original return decision.
Do not use a saved errno from a successful operation as a failure result.
Instrumentation's influence on scheduling is not proven absent and cannot
explain #410's historical timing.

Caller-only inspection cannot supply all internal stages. If obtaining these
receipts needs a production-path edit, new API, lifetime/guard change or
test-only observation hook in the real path, stop for a fresh explicit source
review and authorization before implementation. The observer/actor interface,
native wiring and complete receipt proof remain NOT_READY; this document
does not install the hook or authorize a fixture-bearing source PR.

## 6. Pre-frozen runner, command and single-execution envelope

The following are proposed frozen limits, not evidence that an executor is ready.
The runner label is macos-15, matching the first red's label, not its historical
machine identity. Exact image/version, native architecture, cargo/rustc paths
and hashes, observation-actor entrypoints and the future fixture head/tree
are still UNKNOWN. A new read-only preflight and E/F review must bind them
before execution; the floating label alone is insufficient. Use the repository's
[employee-config CI](../../.github/workflows/employee-config-switcher-ci.yml)
Rust 1.90.0 baseline, no replacement toolchain or automatic installation.

The top-level planned argv is frozen as follows, from the employee-config
workspace in an approved detached source checkout:

    PINNED_RUST_1_90_0_BIN/cargo test --locked --offline -p employee-config-core --lib live::engine::tests::create_only_lock_observation_v1 -- --ignored --exact --nocapture

PINNED_RUST_1_90_0_BIN must be replaced in the reviewed manifest by an exact
absolute existing binary path with its hash. The named observation test is
currently absent/unimplemented; this is not an executable command for today's
tree. Freeze all actor argv, IPC protocol/version, source hashes, synthetic
root owner markers and artifact paths in the later reviewed manifest. An
unfilled pin blocks the run. No dependency download or toolchain fallback.

### 6.1 Default-ignore gate and proof of exactly one actual execution

Only the future new create_only_lock_observation_v1 fixture must be marked
#[test] and #[ignore] by default, so ordinary full-suite CI does not execute
its controlled actors. Do not add ignore to the old six malformed-journal
cases, the payload/parser tests or any other existing assertion. The fixture
must have no build-script, global initializer, non-ignored wrapper or other
automatic path that starts its observation outside the ignored test body.

The [official rustc test-harness documentation](https://doc.rust-lang.org/nightly/rustc/tests/index.html)
describes default ignore, --ignored selection and full-path --exact matching.
The [Cargo test reference](https://doc.rust-lang.org/cargo/commands/cargo-test.html)
distinguishes Cargo options from harness arguments after --. These references
are selection semantics, not proof of the pinned Rust 1.90 binary executing.
The approved single command above must explicitly use both --ignored and
--exact; a name match, test list or default ignored result is not execution.

The same sole native invocation's complete raw receipts must establish:

| Admission field | Required actual native result |
| --- | --- |
| Top-level target | the unique fully qualified live::engine::tests::create_only_lock_observation_v1 |
| Started/executed | exactly one top-level test, with actual run evidence |
| Native per-test result | the exact target passed once, not ignored or merely discovered |
| Native summary | passed = 1, failed = 0, ignored = 0; record measured/filtered counts separately |
| Case protocol | J01-J06 all present with complete required holder/contender/B/release/join/post-release events and FD/object bindings |
| Envelope | native exit, complete dual-stream raw bytes/hashes, EOF, deadlines and cleanup receipts all bound to that invocation |

For the frozen normal libtest output, require a unique running-one-test record,
one exact-target ok result and the corresponding one-passed/zero-failed/
zero-ignored summary. Bind the top-level harness PID and executable receipt;
actor records/streams are separately identified and cannot impersonate a
top-level libtest success. Extra/unassigned harness summaries or ambiguous
actor/parent output make admission incomplete. Do not replace native counts
with a fixture-authored success JSON or borrow counts from another invocation.

Native exit 0 alone is insufficient: zero selected tests can still have a
successful-looking result. Running zero tests, an ignored target, only matching
or listing a name, absent per-case events, a missing/partial raw stream or
unobserved exit is STOP, not a PASS. Do not change flags/filter or run again
to repair that stopped attempt. The one-run budget is consumed; any later
attempt needs a fresh explicitly bounded batch and review. No such execution
has occurred in this documentation revision.

### 6.2 Natural full-suite CI is not the controlled observation entry

The frozen employee-config workflow selects pull_request changes under
desktop/employee-config-switcher/**. An engine.rs source change therefore
naturally runs the old full core suite in the core and platform jobs through
cargo test --locked -p employee-config-core. It is not an observation-only
entry. Default ignore prevents the new fixture's body running there; it does
not prevent the existing old suite, or establish that #410 cannot recur.

Do not modify workflow paths/jobs/commands, add ignore/skip/accept-WouldBlock
to old cases, serialize the old suite or relax any assertion to bypass #410.
Natural suite output is not the dedicated observation's one-run receipt.
Source publication can itself trigger this full suite, independently of a
later manually proposed observation command.

Until an independently controlled execution entry, runner/image/tool pins,
reviewed native actor/borrowed-observer interfaces, clock/deadline and full
raw-receipt/cleanup protocol are frozen and separately authorized, all are
NOT_READY: no fixture-bearing source PR and no actor/test execution is
authorized. This document revision and its conditional docs-only transfer
do not cross that gate. Root must assess any changed head's actual natural
CI and new formal E/F review; old-head zero runs or static conclusions do
not transfer to a new head.

### 6.3 Single-execution resource limits remain frozen proposals

- At most **one** top-level cargo compile/test invocation and one controller
  harness execution, plus only its frozen protocol actor instances. Actor entry
  dispatch must not run additional test suites, oracles or unlisted helpers.
  Freeze auto-install and network off; no warm-up test, second version of the
  command, manual workflow rerun, matrix expansion or automatic restart.
- No --test-threads=1, serializing the old suite to seek green, skipped assertion,
  relaxed six-case expectation or changed production guard ordering. The planned
  exact observation test has controlled concurrent actors; it is not a replacement
  full-suite run and cannot close #410 or claim original scheduling reproduced.
- One monotonic 900-second deadline from immediately before spawn, including
  launch, build, test, polling, capture and normal joins. Record T0 and the UTC
  absolute deadline before launch. No deadline extension or accepted late success.
- Actor barriers/joins are bounded by the earlier of 30 seconds or the remaining
  overall time. Missing acknowledgment, exit observation or native exit value
  is incomplete/STOP, never inferred success from empty output.
- Capture stdout and stderr separately as raw bytes, each <=8 MiB, combined
  <=16 MiB; event records <=4 KiB each, <=4096 total. Keep native exit status,
  signal/termination/timeout separately from supervisor status. Hash/count both
  raw streams and mark whether EOF/readers/join were fully observed.
- On output-limit breach, stop execution; retain the bounded raw prefix with
  limit_hit/incomplete flags, never call it a complete capture or silently
  truncate/reparse/retry it. Supervisor/parse/read errors are sticky STOP.
- After stopping, allow at most 30 additional seconds solely for verified owned
  actor release/termination, reaping, FD/reader closure and evidence finalization.
  This is not extra test time. Never terminate an unidentified holder.
- Retain raw evidence privately outside roots scheduled for removal. Report only
  fixed summaries, counts and hashes publicly; no real credentials, paths or
  arbitrary error strings. Evidence receipts are bounded to 64 KiB.
- On a successful observation, verify all owned actors joined, native exits,
  owned FD closure, reader EOF and journal/target final facts before removing
  only resolved, marker/identity-checked synthetic roots under the approved
  scratch directory. Preserve evidence separately. Any cleanup gap is STOP.
- On failure, freeze normal actions and preserve journals/lock/object evidence;
  close owned resources and reap actors within the stop budget. Do not repair,
  reopen-and-recover again, overwrite, erase evidence or recursively delete an
  unchecked path. Record any residual owned artifacts and UNKNOWN cleanup facts.

No Rust/toolchain, actor, lock, malformed journal or service is executed by this
document batch. These limits are a contract for a later reviewed supervisor,
not a claim that such a supervisor or its native cleanup proof exists.

## 7. Exit and review boundaries

A later observation may establish only the measured controlled preparation,
lock-denial, release and same-journal validation relations on its frozen runner.
InjectedCrash is a controlled return boundary, not a real process crash;
scripted malformed bytes are not random I/O failure. A deliberately held
native lock, if actually observed, is a controlled kernel-lock result. None
would identify #410's historical holder, cause, timing or malformed case.

Keep [the payload source boundary](employee-config-replacement-transaction-contract.md#w2p-private-unauthenticated-payload-byte-check-source-candidate)
and real apply failure-closed as described by the
[client README](../../desktop/employee-config-switcher/README.md).
No AEAD/source/host proof, W3/W4, nonce/journal production capability, real
configuration recovery or old failure closure follows. PR133 fixed-head CI,
dynamic acceptance and production remain NO-GO; all PRs stay Draft.

The initial one-file local batch permitted document-link/anchor and git
diff/scope checks plus one local commit, then stopped before remote transfer.
Its separately authorized connector batch subsequently created Draft PR134
as recorded above. The later old-head E/F narrow reviews and retained E STOPs
do not authorize fixture code or execution and are not new-head review evidence.

This fresh revision permits only this document's admission-contract edits,
document facts/links/anchors and single-file diff checks, then one local commit.
No Rust, test, native actor, old case or manual CI is run. No other file,
workflow, old PR or PR body may change; no test PASS or observed fix is claimed.

A conditional same-PR GitHub connector transfer is allowed only after proving:
the new local commit's parent is the pinned local d0d2b1a10690b2920d71e59c7ea4f54d79e5008d;
its full parent tree equals remote head 024e047626c588fa42e16e70875f03e340be1f79's
41b1efeae72d9a9029b6422bcb54e37545eaa034; remote PR134 remains open/Draft at
that exact head, base f9c22ad4e2d7936b70ee85fe1ed79b52d6d751db and original body;
and the new complete tree plus sole document blob/mode 100644 are identical
between local and connector objects. Local/remote parent SHAs remain different.

Only then may an equivalent remote commit use that exact old remote head as
sole parent and update the existing PR134 branch once with expected-old-head
lease and force=false. No new branch/PR, blind Git push, trust/credential change,
reset, second local commit or body update is authorized. If any proof or lease
cannot be guaranteed, stop and hand the preserved candidate/evidence to root.
Root must freshly pin the changed head's natural CI and arrange original E/F
new independent formal same-head review. All PRs remain Draft and production
NO-GO. First unexpected tool/check error or scope drift stops without hotfix,
recovery/retry or further mutation.
