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
