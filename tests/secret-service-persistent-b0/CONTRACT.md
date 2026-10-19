# Secret Service B0: no-secret directory and service restart

Development-preview prerequisite only; 2026-10-17. Stacked on PR81 exact
`bbf7ab82124d168f3f32fb2192429a9d6992b276`. Production remains NO-GO.

## Scope and isolation

This is not persistent collection provisioning or recovery. It creates only a
private directory and a fixed **public, non-keyring** sentinel, then restarts the
real service in one disposable Linux VM. No application/provider/UI changes.
No password, collection/item API, Key generation/read/delete, prompt, unlock,
alias, session-opening or control-socket request. GNOME itself initializes an
intrinsic transient session collection on startup; this fixture neither invokes
nor uses it, and does not claim the daemon has no internal collection objects.

Reuse the unchanged [no-key harness](../../desktop/employee-config-switcher/preflight/run.py)
and [deny policy](../../desktop/employee-config-switcher/preflight/bus.conf.in).
The root supervisor requires a GitHub-hosted Linux VM, systemd, exact package
versions, a newly created dedicated non-root UID/GID and synthetic HOME/XDG dirs.
Before either daemon, verify loopback-only private netns, IP socket denial,
cleared proxies/session variables, denied runner home and /run/user, fixture-owned
host socket isolation and private /dev/shm. Only AF_UNIX is permitted. No service
activation directories, inherited bus, host fallback or GUI. This is not a claim
of complete filesystem/IPC containment against a hostile service or root.

## Directory and restart acceptance

1. Open owned mode-0700 work/data directories with no-follow directory fds.
   Require empty data and synthetic HOME; create only `data/keyrings` mode 0700.
   Exclusively create `public-b0.sentinel` mode 0600 with the fixed public bytes
   in [fixture.py](fixture.py). Check owner/group/type/exact mode, one hard link,
   length and inode identity. Sync the file and directories. No repair/chmod of
   unexpected objects. The filename is deliberately not `*.keyring`.
2. Start one private bus and GNOME daemon in foreground, components=secrets,
   explicit synthetic runtime control directory, stdin DEVNULL. Verify owned
   executable, UID and unit cgroup; bus metadata binds its unique owner to the
   exact service PID/UID. Existing libsecret metadata-only observer must pass.
   Inspect control socket metadata only; do not connect to it.
3. SIGTERM and wait for the first daemon to exit successfully (bounded wait;
   forced kill is cleanup, never successful restart evidence). Require its /proc
   entry absent, plus both service name and old unique bus owner absent, while
   the exact private bus remains alive. Recheck directory/sentinel unchanged.
4. Launch the second foreground daemon with the same HOME/XDG/runtime and bus.
   Require a different PID **and** unique bus owner; bind both to the dedicated
   UID and verify libsecret metadata again. Reopen the exact persisted directory
   and sentinel with no-follow, match original identities/content/permissions,
   and reject any additional data/keyrings entry or legacy HOME directory.
5. Stop and verify the second instance absent. Revalidate before unlinking only
   the owned public sentinel and removing its empty directory via held fds.
   Stop owned bus/children and close logs/fds. The shared supervisor stops its
   unit, removes its dedicated account, and cleans only validated temporary roots.

The same unit/VM stays alive: this does not test reboot, mount persistence,
collection encryption, password transport, actual Key recovery, lock/unlock,
concurrency, item deletion or production cleanup-state-machine integration.

## Fixed outputs and failure behavior

Only source-defined booleans/labels and component/source/binary hashes leave the
harness. Daemon, GLib, D-Bus and owner-helper output is captured, never relayed or
uploaded. Owner strings are internal metadata, not acceptance-log content.

Success requires all eight `MARKERS` in [fixture.py](fixture.py), successful unit
exit, host boundary checks and supervisor cleanup; only then print
`CHECK B0_OUTER_CLEANUP_OK=true`. A missing dependency, unsupported isolation,
unexpected path/permission/type/content, missing control socket, timeout or
uncertain instance identity fails closed. No skips, empty-password provisioning,
unprotected fallback, alternate daemon, repair, automatic rerun or dispatch.
On failure, `CHECK rejected=<fixed-label>` plus achieved marker booleans identify
the last proven boundary. No success is inferred from an unvisited stage.

## Test entry and evidence

Nonprivileged portable static/predicate/link tests:
`python3 -B tests/secret-service-persistent-b0/test_fixture.py`.
Linux public-file negative cases require explicit `--linux-fs` and fail on other
platforms (not silently skipped). They cover symlink parent/sentinel, hardlink,
FIFO, unexpected directory/entry, permissions, replacement and corrupt content.
They do not start a service or create secrets.

The [dedicated workflow](../../.github/workflows/secret-service-persistent-b0.yml)
checks out and asserts the exact PR head, runs both guard suites and Linux public
file tests, installs the exact existing component pins, then invokes B0 once.
Its fixtures live outside the broad employee desktop path so this B0-only diff
does not trigger PR81's secret-bearing volatile fixture. Existing workflows and
test assertions are not changed or disabled. Normal Code validation may also run;
its status is recorded separately from B0. No packaging or deployment.

Local Windows evidence is limited to portable tests/syntax/link/diff checks;
Linux compilation, filesystem and real restart evidence must come from this
exact head's ordinary dedicated CI, recorded in the Draft PR body. Old PR80/81
results are prerequisites, not evidence for this new head. Original visible E/F
review is arranged by the coordinator after CI, not spawned by this fixture task.

## Noninteractive provisioning research boundary

See [PROVENANCE.md](PROVENANCE.md) for pinned source facts. GNOME 46.1 offers a
stdin password path and a private same-UID control socket; unlocking a missing
login keyring can create a persistent collection. That supports a **candidate**
future B1 mechanism, not B0 runtime verification or authorization to use it.
B0 does not execute even a fake-password input probe because it could provision
a collection. It checks only the private socket's existence/type/owner/mode.

A separately authorized B1 must establish nonempty protected password handling
through an owned stdin pipe/private fd, explicit private control target with no
fallback, bounded input/output and deadlines, interception/no raw logs, channel
closure, same-service identity and cleanup. Passwords must never enter argv,
environment, artifacts or logs. No usable channel or unsupported fixed version
must block B1; empty-password or unprotected stores are not alternatives.
