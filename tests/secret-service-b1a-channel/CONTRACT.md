# B1a-C: independently isolated synthetic password channel

Status: test-only candidate, Draft / production NO-GO. This is **C**, not
provisioning **P**, GNOME acceptance, a collection, item, employee Key, provider,
configuration writer, persistent daemon restart or persistent recovery.

## Source and fixed boundary

Stacked on PR88 head `598845e71b54cfc6c8597d7493d2e061d339572c`, whose base is
PR87 `776d9652d0c4a85a58350befbfda0351f3262e22`. Existing files are immutable:
`preflight/run.py` keeps stdin=DEVNULL; it is **not imported** by this launcher.
B0/A2/A3, all seven old workflows, pins, policy, ACL predicates and product
Rust/provider/API/IPC/constructors are unchanged. The separate C workflow and
substantive [preflight contract](../../desktop/employee-config-switcher/preflight/B1A-CHANNEL-CONTRACT.md)
are the only additions outside this directory.

The sender's exact reusable path is `OneShot.send` in `channel.py`: outer-memory
bytes -> owned anonymous nonblocking CLOEXEC pipe -> systemd-run `--pipe` ->
the separately executed synthetic receiver's stdin. The independent launcher
passes the held read FD explicitly, never patches a global subprocess function,
stdin or metadata. This is **not evidence about the old shared supervisor**.

## Bytes, identity and one use

Each of the fifteen registered acceptance cases generates a fresh 68-byte
synthetic password (`B1AC` + 64 lower-case hexadecimal bytes), kept in outer
memory. Empty, NUL, CR, LF, truncated, over-limit and other encodings are rejected
without sending. No newline is appended; no shell/echo, alternate socket, real
session, disk sample, argv, environment, metadata, journal or artifact contains
the password. Core dumps are disabled; this synthetic test is not an mlock or
production password-memory-erasure implementation.

Before the first byte, outer verifies the newly created UID, PID/start time,
Python executable dev/inode, exact owned unit/cgroup, and receiver stdin's
dev/inode/owner/mode/access mode against both held 0600 pipe ends. The receiver
file is copied read-only into the root-owned source directory, then executed
with a minimal environment in the new unit. The unit launcher closes its own
stdin after spawning the receiver. All unrelated FDs are closed at exec.
The write end is non-inheritable and held only by outer. Bindings are checked
before and after the single write, before closing the writer to deliver EOF.
The attempt is consumed even on rejection. A full atomic-sized write is
required; short writes and write errors are not retried. Deadline <=5 seconds;
changed identity, duplicate purpose/use, wait or close failure cannot pass.

The receiver validates the complete nonempty frame **after EOF**, closes stdin,
then sends a fixed-length HMAC receipt over its **private captured binary
protocol**, not a public log. Outer compares this against its independent
in-memory oracle and purpose. The receipt itself is never relayed, written to
metadata/files or printed in errors. This test-only HMAC is proof for this
trusted synthetic receiver, not a GNOME protocol and not a future P acceptance
substitute. The reusable sender does not require a real daemon to emit it.

## Independent isolation and complete evidence

Acceptance refuses anything except a disposable GitHub-hosted Linux VM under
systemd. Every case creates a new unused UID/group/name and unit, private
HOME/config/data/cache/state/runtime/tmp, EXTERNAL-authenticated private D-Bus
(peer PID/UID, executable and cgroup checked), and fresh anonymous pipes.
No GNOME process is launched. PrivateNetwork, PrivateIPC/Devices/Tmp, isolated
`/dev/shm`, ProtectHome/System/KernelTunables/ControlGroups, empty capability
set, NoNewPrivileges, AF_UNIX-only and inaccessible host bus/user paths apply.
The exact new public host socket is reachable outside and refused inside;
host HOME, network/IPC namespaces, IP sockets and host-shm visibility are
checked. No existing user/session socket or real credential directory is read.

All receiver/bus stdout+stderr surfaces are registered before wait. Outer also
registers unit stdout+stderr, every helper's streams, current exact-unit journal
and owned files. Raw streams stay in memory; only fixed labels leave outer.
Bounded readers continue draining after overflow but mark it **incomplete**;
unknown file types, FIFO, symlink, hardlink, changing identity/content, excessive
files/bytes, missing surface, EOF/close/read failure or timeout cannot establish
completeness. Private unit work/tmp/shm files are collected before exit; owned
host-side source/metadata/work files are collected after stop. No scan expands
to host HOME, credentials or unrelated journals.

Outer retains the oracle through unit stop, cgroup-empty/absent and target-gone
checks, all child/helper waits, FD/stream close, journal sync and full exact-unit
journal capture, file checks, removal of owned user/group/directories/probes,
then content scanning. Content-clear and capture-complete are separate: raw,
hex and base64 oracle/candidate bytes (including actual injected short prefix)
are checked; neither a clear partial capture nor a green native test is a pass.
This is the declared representation set, not proof against arbitrary encodings.
No existing ACL predicate is changed or relaxed and no failed fixture receives
a permission repair. New-file modes are set before execution/password use;
mode alone is not advertised as proving absence of ACLs.

Cleanup attempts all owned actions and records failure without replacing an
earlier operation error. It never deletes an unrelated name: unique root
identity/prefix/parent and stopped unit are required. No public raw exception,
password, receipt, identity, path, numeric OS error or digest is emitted.

## Tests and result vocabulary

Fifteen independent VM cases: positive; empty/NUL/CR/LF/truncated/over-limit;
wrong UID/PID/executable/object; deadline; duplicate; early EOF; short write.
Only positive and duplicate's **first** attempt deliver a complete password.
Every rejected attempt verifies original target/public-neighbor identity and
contents unchanged. Short-write uses the explicit narrow test adapter; it is
**not an actual kernel I/O fault injection**. Early EOF closes a real pipe.

Public tests cover validation, purpose/identity/deadline/replay, actual anonymous
pipe bytes+EOF+close, a real synthetic receiver wait/receipt, capture overflow,
missing/extra inventory, regular-file/FIFO/symlink refusal, owned timeout and
close/first-error classification. Adapter tests are distinct from real Linux
process tests and VM isolation. On non-Linux systems Linux tests skip; skipped
is not execution.

Each accepted case emits a fixed `CHECK C_CASE_<registered-name>=true` plus
`c-content-clear`, `c-capture-complete`, `c-owned-cleanup-ok`. Any unexpected
dedicated CI red freezes this exact source: read-only fixed-label diagnosis and
safe cleanup only, no patch/rerun/dispatch without a new contract.

Only the new natural pull_request head/attempt1 and seven old same-head natural
regressions count. Code's skipped platforms are not execution; Employee's
default merge checkout is not raw-head daemon acceptance. New E/F formal review
is required after all gates; no source review or historical green grants it.

## Rollback / remaining P

No migration/product enablement. Revert these added files to remove C. P still
needs its own contract for GNOME 46.1 nonempty stdin consumption, protected
collection creation/unlock and persistent reopen. Later Key/provider/recovery,
real configurations and production remain unimplemented/unverified here.

## Bounded safety/diagnostic follow-up to C #1

The original exact head `3f0554a538d40d0621dca9d982bcf19f663f0824` remains a red
historical snapshot: natural C #1/run39262864298 failed with fixed `c-setup`,
content-clear=true, capture-complete=false, aggregate cleanup=false and zero
final case admissions. The original visible E/F independently found three P2
defects (incomplete directory enumeration, partial-acquisition cleanup, and
retrying a consumed FD number), plus coarse setup reporting. These static
findings **do not identify C #1's underlying setup cause or physical residue**.

This follow-up changes only channel.py, fixture.py, test_channel.py and necessary
C contract/provenance. Receiver, dedicated workflow, shared supervisor, the old
seven workflows, pins/policy, UID/permissions, payload/receipt protocol, fifteen
cases, send count and deadlines remain unchanged. No setup command, probe,
retry, permission repair or behavior fallback is added by diagnostics.

- Resource guards exist before the first directory/socket/Popen acquisition.
  Successful results are registered immediately, before chmod, the next
  directory, Capture allocation or thread start. Raw Popen streams have an
  owner before Capture is initialized; a retained Capture is registered before
  starting its reader. Partial failures stop/wait and close all confirmed-owned
  objects, continue other cleanup, and preserve the original first error.
  A side-effecting user-creation helper's **already observed successful exit**
  also registers its owned identity if capture initialization failed; this adds
  no lookup, command, retry or inference from raw stderr. Unconfirmed resources
  are not adopted or deleted.
- FD/stream records distinguish **consumed ownership** from **confirmed close**.
  Cleanup candidates are removed/consumed before close. Even if close reports
  failure after the kernel released/reused the number, no upper cleanup retries
  that old number; its unconfirmed state remains red. Constructor rejection,
  send rejection, success and duplicate all share this ownership record.
- Root lstat, nofollow held-directory FD, bounded explicit scandir enumeration
  and before/after identity/content checks replace os.walk's silent errors.
  Missing, wrong-type/symlink, unvisited, rebound, disappeared or enumeration-
  error roots/subtrees are incomplete; a real empty directory can be complete.
  FIFO, hardlink, overflow, inventory, read and close refusal gates remain.
- Diagnostics publish only a finite source-owned whitelist: setup
  entered/ok/rejected and acquired cleanup action/result/consumed-confirmation
  states. Existing return values/predicates are reused, not probed again. Bait,
  raw exception/stderr, path/identity/errno, password and derived values are not
  relayed. Not-acquired/missing capture cannot become green.

Public regressions were added before repair. Deterministic close-after-release
adapters use dup2 to reuse the exact number for a **public sentinel**, then run
full guard cleanup and prove that sentinel was not closed. They are synthetic
return-shape/interleaving tests, not actual kernel I/O-fault injection. Directory
rebound/removal/enumeration and partial constructor/thread-start cases are
likewise declared controlled public negatives. Original 42 tests and 15 VM
cases are retained. New natural exact-head CI and original E/F review are still
required; the first unexpected new dedicated red freezes source again.

## Sink-delivery and exit-class batch after C #2

This separate bounded batch has the sole parent
`38e3f96f16c1c17367e7d90d9415889fb20e7ffd`. Its natural C #2/run39271385533
attempt1 remains FAILURE: 65/65 Linux public tests passed, but setup reported
`c-setup-user-create-rejected`, content-clear=true, capture-complete=false,
aggregate cleanup=false, and 0/15 final admissions. The underlying cause and
possible partial helper effects remain unknown. Original E/F found one static
P2: throwing diagnostic output could skip an owned cleaner and later cleanup
or cover an earlier error. **That finding is not C #2's established cause.**

Only fixture.py, test_channel.py and these C contract/provenance notes change.
channel.py, receiver, C workflow, every PR88 file, product, helper arguments,
UID selection, permissions, success exit gate, success-only ownership and the
fifteen channel cases remain frozen. There are no new probes, lookups, deletion
targets, retries, fallback behavior, stderr parsing or runtime permission fixes.

- All diagnostic writes catch sink exceptions (including interruption-like
  exceptions) without formatting them, and latch `delivery_failed`. Restored
  output never clears this latch. Diagnostic labels and final reports have
  finite source-owned whitelists. Even terminal fixed-error output cannot
  expose a nested output exception or change its required nonzero outcome.
- Outside cleanup, lost setup/acquired delivery prevents further acquisition;
  an already acquired object is registered **before** this gate can reject.
  Both pipe ends are registered before their first report. Cleanup output is
  advisory: entered/result/consumed/acquired/release/exit and outer-finally
  sink failure cannot skip the current or any later owned cleaner. Nested
  helpers needed by an owned cleaner still run their unchanged operations;
  delivery failure remains sticky globally and is checked at admission.
- Ownership is still consumed before close. No consumed descriptor or stream
  is retried, even when reporting its consumed/unconfirmed state fails.
  Resources retains the original operation exception object at context exit,
  its first category and separate fixed cleaner errors. `Resources.ok` is the
  physical cleaner result; returned completeness and final admission also
  require delivery. Sink-only loss is fixed `c-capture`, not a claimed physical
  cleanup failure. An original operation error always takes precedence.
- The user-create helper only classifies its **already observed** returncode,
  after the unchanged successful-result ownership callback. Fixed classes
  follow the documented shadow 4.13 useradd exit vocabulary: success,
  password-file, syntax, option, uid-in-use, group-missing, name-in-use,
  group-file, home and selinux; unlisted/invalid forms are unknown, negative
  results are signal, and absent results are missing-result. No numeric exit,
  signal, errno, identity, path or raw stderr is emitted. A class is the
  helper's result vocabulary, **not verified root cause, absence of residue,
  or permission to adopt/delete an unconfirmed identity**.

The original 65 public tests and 15 VM cases are retained. New controlled public
regressions cover output loss at acquisition/setup/entered/result/consumed,
explicit release/context exit/outer finally, success and failed cleaners,
sticky recovered output, no case admission, primary+cleaner+sink errors,
both registered pipe ends and a reused public FD sentinel. Exit-class tests
cover every documented class, unlisted values, signal, absent results and bait,
and reuse a public helper's existing result without changing its call count,
arguments, exit gate or success-only ownership. These are deterministic public
adapters/return-shape tests, not actual output-device or kernel I/O faults,
not real user-creation tests, and not an explanation of either historical red.

Only the new natural exact-head C and seven old attempt1 regressions, followed
by original E/F formal new-head review, may establish the next narrow result.
The first unexpected dedicated red freezes this source again. All old C #1/#2
and A3 red evidence remains intact. Draft / production NO-GO continues.

## Independent raw-helper handoff batch after C #3

Sole parent `0ba876cae2320529d62b47e6ea3598ecd973d8ab`. Historical C #3
run39277844314 attempt1 remains FAILURE: 91/91 Linux public tests passed,
`c-user-create-exit-option` then `c-setup-user-create-rejected`, clear=true,
capture-complete=false, aggregate cleanup=false, 0/15 final admissions.
Neither this class nor the next static P2 establishes a root cause or the
absence of partial helper effects. Original E/F's independent P2 counterexample
was an acquired-report failure before ProcessOwner returned: the raw guard
could observe successful helper exit, yet Commands' missing full owner skipped
the existing success callback and leaked its side-effect ownership record.

Only fixture.py, test_channel.py and these C contract/provenance notes change.
channel.py, receiver, workflow, all PR88 files and product stay byte-frozen.
No useradd argument, UID, permission, success exit gate, exit-class vocabulary,
isolation property, unconfirmed-object treatment or setup behavior is repaired.
Argument compatibility, including the existing -K setting, is outside this
batch. No new poll/wait, helper, lookup, probe, retry or deletion target is added.

- A private ProcessRecord is retained independently of a complete owner.
  Immediately after factory success, its raw process and cleanup guard are
  registered before acquired output, full-owner construction or Capture.
  It retains only the existing returncode and results obtained by the original
  poll/wait sites (including the original forced-cleanup wait), not an extra
  observation. A failed full-owner construction keeps the same raw guard.
- An already observed integer success invokes the existing success callback
  once, before a subsequent report can reject. The callback's existing resource
  registration precedes its own output gate. Callback attempt is consumed
  before invocation; errors are retained privately, not retried or allowed to
  skip the original wait or any stream close. Nonzero, signal, unknown and
  absent results never authorize successful side-effect registration.
- Original primary exception object/category, callback error, diagnostic
  delivery loss and physical cleaner failure remain separate. A later absent
  result cannot erase a confirmed terminal result. Cleanup consumes the raw
  record before acting; consumed stream/FD ownership is not retried, even after
  close-after-release or a failed callback/report. Other owned cleanup proceeds.
  Raw-only capture stays incomplete; successful physical cleanup or a recovered
  diagnostic sink cannot wash delivery/capture red into admission.
- Existing exit-class reporting now reads the independent retained result,
  never infers missing solely because a complete owner was not returned.
  Classes retain their previous meaning and are not a cause/residue claim.
  Raw handles, numeric results, stderr, identity and secrets remain private.

Public negatives were introduced before repair and retain the original 91
test names plus 15 VM cases. Controlled processes/streams cover success before
acquired-output loss; success observed only by the original raw-guard wait;
full-owner construction failure; nonzero/signal/unknown/absent/creation failure;
joint acquired/cleanup/callback/report errors; consumed records; and exact
poll/wait/termination/callback/resource-cleaner/stream-close counts. A Linux
public FD sentinel is reused after a deliberately failing close, then verified
after all guard cleanup. These are declared controlled public regressions,
not real I/O faults, real identity provisioning, or a cause of any historical
red. New natural exact-head C plus seven old first-attempt regressions and
original E/F formal new-head review remain required. The first unexpected
dedicated red freezes the new source. C #1/#2/#3 and all A3 history is preserved;
Draft / production NO-GO remains.

## C-only identity evidence batch: intentional stop, never user creation

Sole parent `8f09f7f402e4acb38bcd4033787d31ac45d367e9`. C #4
run39283788825 attempt1 remains FAILURE: 100/100 Linux public tests passed,
fixed exit-option then user-create rejection, 0/15 admissions. Original E/F
closed its static handoff P2, not the runtime setup failure. Public -K versus
CREATE_MAIL_SPOOL configuration-domain mismatch is not an established cause.
This batch changes only fixture.py, test_channel.py and these two notes.
channel/receiver/workflow, every PR88/product/old-regression file remain frozen.

**This is an intentional evidence-collection stop, not C acceptance.** Both the
workflow main and direct legacy case entry unconditionally enter the new gate;
no path returns from that gate. It never invokes useradd, including help,
version or -D, creates/deletes users/groups, starts an isolated unit, reads or
changes defaults, executes hooks, installs/downloads/replaces helpers or starts
a new diagnostic process. Existing disposable-runner/root/systemd/VM guards
remain. Legacy argv, UID selection, no-home/nologin, exit-class/success gate,
raw record/ownership and fifteen scenario code are retained but unreachable.
No C_CASE or C PASS is emitted. A red with the declared stop is expected; a
public-test, sink/capture/cleanup or other unexpected red freezes source.

Trust boundary: disposable runner root and OS are trusted. This is not defense
against malicious root, remote attestation or a changed check-to-exec scheme.
The original SAFE PATH is used for the same POSIX executable lookup. The
selected absolute object is held read-only; each path component is nofollow,
root-owned, group/other nonwritable, directory or final regular executable.
Held/path dev/ino/mode/owner/group/nlink/size/mtime/ctime identities must remain
stable. Complete bounded bytes (8 MiB maximum, exact size and EOF) produce the
helper's full SHA-256; partial reads, replacement or ambiguous/unsafe objects
cannot produce approved evidence. The object is never executed or substituted
into the unchanged legacy argv, and no future execution binding is claimed.

No dpkg subprocess is launched. Bounded nofollow/stable reads of only
/var/lib/dpkg/status and installed-package .list metadata in its info directory
identify exactly one owning package, installed status, complete version and
architecture. Only that owner's .md5sums is read for the helper-byte comparison;
MD5 is internal dpkg metadata consistency, not a cryptographic approval or
source/build proof. SHA-256 is computed only over the selected helper, never
an arbitrary file/configuration. Limits: status 16 MiB; individual list/sums
4 MiB; aggregate lists 64 MiB; complete directory enumeration <=65,536 names.
Invalid/ambiguous/changed/incomplete metadata is unknown or mismatch and red.

There is **no pre-reviewed allowlist**. Observed path/package/version/hash does
not approve itself. Patched implementation/build conditions, no-mailbox branch
and complete hook-path set are therefore unknown; no guessed upstream-4.13
hook directories or defaults are inspected. The reusable empty-directory
predicate accepts only a completely checked empty held directory or absent
leaf under checked parents, using nofollow/stable FD checks. Symlink, rebound,
unreadable, enumeration failure, nonempty or unknown cannot pass. It is exercised
only by controlled public fixtures in this batch, never used to infer a complete
runtime hook inventory without a proven source binding.

### Narrow non-secret output exception

One `HELPER_EVIDENCE ` JSON line, <=768 ASCII characters, is expressly allowed:
path class only `/usr/sbin/useradd`, `other` or `unknown`; filtered package name
(<=64 ASCII), full version (<=128) and architecture (<=32); complete 64 lower-
hex helper SHA-256 or unknown; fixed stability/binding/source/hooks/precondition
enums. Invalid fields are unknown, never truncated or formatted from arbitrary
objects. Source/hooks/precondition are always unknown in this no-allowlist batch.
No raw path, inode/UID/PID/handle, config, hook name/content, stderr, exception,
user, credential/secret or arbitrary-file digest is exposed. This exception is
not permission to add data to old diagnostics. All other output remains fixed.
The single attempt is consumed before delivery; sink loss stays sticky and no
fallback/retry is attempted. FDs are consumed before close; every acquired
descriptor is attempted once, other closes continue, and first operation versus
cleanup/delivery errors stay separate. Failed close cannot certify evidence.

New public negatives precede implementation and retain original 100 test names.
They cover unknown/changed identity, package checksum mismatch/ambiguous owner,
unknown source/precondition, hook nonempty/error/symlink/rebound, post-check
drift, incomplete reads, failed sink/cleanup and public FD reuse. Every refusal
asserts no creation/admission; source AST checks both real entry gates. Linux
temp tests explicitly adapt ownership/directory metadata for public fixtures;
the real EvidenceOS does not alter these predicates. These are controlled
adapters, not real I/O faults or actual user provisioning. Existing scan/cleanup
bounds are not relaxed. New natural CI and original E/F review are evidence
only; a later parameter correction needs its own separate contract. Preserve
C #1-#4, all A3 reds and historical results; Draft / production NO-GO continues.
