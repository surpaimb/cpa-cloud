# Create-only lock observation contract after PR133's first red

Status: **DOCS_ONLY_OBSERVATION_CONTRACT / NEW OBSERVATIONS UNEXECUTED**.
This independently authored document is the sole new file in this local batch.
It authorizes no fixture implementation, Rust invocation, old-case execution,
production change, CI dispatch/rerun or remote transfer. Original E/F/D
read-only review preferred a contract before the deferred two-file fixture.

The immediately preceding root dispatch script failed while parsing JavaScript
backticks before actual task delivery or any write. That dispatch STOP remains
history; this is a fresh batch, not completion of that failed dispatch. Earlier
implementation/read/identity/worktree STOPs and the body-only generic job-read
tool STOP remain in [PR133's retained record](https://github.com/surpaimb/cpa-cloud/pull/133).
This document changes neither that PR body nor any earlier PR.

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

These records may require a test-only harness/observer that does not yet
exist. Caller-only result inspection cannot distinguish all internal error
sites or recover a descriptor already closed on return. If the necessary
observation requires editing a production lock path, exposing a new API,
reordering guard fields or altering lifetimes, STOP the fixture design for
separate source review and authorization. This contract supplies none of it.

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

    PINNED_RUST_1_90_0_BIN/cargo test --locked --offline -p employee-config-core --lib live::engine::tests::create_only_lock_observation_v1 -- --exact --nocapture

PINNED_RUST_1_90_0_BIN must be replaced in the reviewed manifest by an exact
absolute existing binary path with its hash. The named observation test is
currently absent/unimplemented; this is not an executable command for today's
tree. Freeze all actor argv, IPC protocol/version, source hashes, synthetic
root owner markers and artifact paths in the later reviewed manifest. An
unfilled pin blocks the run. No dependency download or toolchain fallback.

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

This one-file local batch permits only document-link/anchor and git diff/scope
checks, followed by one local commit. Results and the resulting tree/blob are
reported separately; no test PASS is asserted here. Stop before any push or
remote PR. Any later connector-equivalent transfer, fixture implementation or
single execution requires a fresh explicitly bounded batch and original
independent review. First unexpected tool/check error stops this batch without
hotfix, recovery/retry or a second commit.
