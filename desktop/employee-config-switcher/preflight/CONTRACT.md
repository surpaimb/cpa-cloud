# Linux Secret Service no-key environment preflight

Status: independent development-preview candidate, stacked on PR76
`70ea9716905c980bb2c830a8fa129c640b00fa29`. No production enablement.

This slice establishes only the prerequisites for a later separately authorized
synthetic provider test. It never invokes CPA's recovery-key provider, changes
the test-only approval constructor, starts an official client, or enables
`apply_configuration` / UI writes. There is no Key creation, read, deletion,
collection unlock, password input, recovery envelope, or persistent item test.

## Fixed real components and provenance

[components.json](components.json) pins Ubuntu package versions and official
source URLs for dbus-daemon 1.14.10-4ubuntu4.1, GNOME Keyring 46.1-2ubuntu0.2 and
libsecret runtime/development files 0.21.4-1build3. A missing exact package or
version mismatch fails; it is not skipped or silently upgraded/downgraded.
The locally authored libsecret observer is `cpa-no-key-preflight-v1`, with its
source and executable SHA-256 recorded by the job. Runtime daemon executable
hashes, installed versions and package copyright-file hashes are also recorded.
These pins do not freeze the whole Ubuntu image or all transitive libraries.

The Python orchestrator, C observer, bus policy, workflow and guard tests are
independently authored from this contract and public documentation. No prohibited
reference implementation, fixture or asset is used. They are test infrastructure,
not copied into the Rust execution core or shipped application.

- D-Bus: AFL-2.1 OR GPL-2.0-or-later, with package per-file notices.
- GNOME Keyring: GPL-2.0-or-later / LGPL-2.1-or-later per component/file.
- libsecret: LGPL-2.1-or-later, with package per-file notices.
- Python, compiler, GLib and systemd retain their distro licenses. Installed
  `/usr/share/doc/<package>/copyright` files are retained; no binary/library
  artifact is redistributed by this workflow.

Official references consulted 2026-10-16:

- [D-Bus daemon configuration/policy](https://dbus.freedesktop.org/doc/dbus-daemon.1.html)
- [GNOME Keyring daemon options](https://manpages.debian.org/unstable/gnome-keyring/gnome-keyring-daemon.1.en.html)
- [libsecret Service initialization flags](https://gnome.pages.gitlab.gnome.org/libsecret/flags.ServiceFlags.html)
- [libsecret get_sync](https://gnome.pages.gitlab.gnome.org/libsecret/type_func.Service.get_sync.html)
- [libsecret 0.21.4 license](https://github.com/GNOME/libsecret/blob/0.21.4/COPYING)
- [GNOME Keyring 46.1 license](https://github.com/GNOME/gnome-keyring/blob/46.1/COPYING)
- [systemd v255 mount/network settings](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml)

## Required gates, before daemon startup

The root harness requires a GitHub-hosted disposable Linux VM with systemd PID 1.
It refuses preexisting test accounts/groups/units and creates one dedicated fixed
non-root UID. All HOME/XDG/runtime/temp directories are fresh, owner-private and
under a newly generated root in `/opt`. The unit is invoked through `env -i` with
an allowlist; inherited session addresses, display/agent handles and all proxy
variables are absent. No existing session bus or keyring is contacted.

Before starting dbus-daemon or GNOME Keyring, the non-root unit requires:

- A different network namespace containing only `lo`; IPv4/IPv6 socket creation
  is denied. No listener or external network probe is used.
- Opening the real runner home directory and `/run/user` is denied/absent; no
  home content is read. Real session sockets are not enumerated or contacted.
- A public fixture-owned Unix socket, first proven connectable by the same UID
  outside the unit, is inaccessible under the unit's private `/var/tmp` mount.
  This tests a representative host filesystem session-socket boundary, not every
  possible host Unix socket. `/run/user` and `/run/dbus` are additionally hidden.
- A public host `/dev/shm` marker is absent in the unit's dedicated shm tmpfs.
  A unit-created public marker must remain absent on the host after exit, and
  the host marker and listening socket must remain intact.
- A unit-owned filesystem Unix socket remains usable inside its private root.
  AF_UNIX is intentionally allowed; this is not full host IPC isolation.

The unit additionally uses PrivateIPC/PrivateDevices/PrivateTmp, ProtectHome,
ProtectSystem=strict, NoNewPrivileges, an empty capability bounding set, bounded
runtime and disabled core dumps. These settings are not claimed to make the
work directory the only writable location or to prove production hardening.

## Metadata-only real service checks

The custom bus has no service activation directories or helper. Authentication
is EXTERNAL for the fixture user. Policy denies all Secret.Service, Collection,
Item and Prompt interface calls and Properties.Set. The same prohibited method
names are also denied without an interface field.
The harness launches the
real dbus-daemon and GNOME Keyring in foreground mode with `--components=secrets`,
without login/unlock/start/replace or passwords. Their processes must remain in
the test unit's cgroup, have the expected executable and dedicated UID.

The independent C observer asks the bus for its own and the service's PID/UID,
then creates a libsecret proxy with `SECRET_SERVICE_NONE`. It does not request a
secret-transfer session or collections, and verifies that the proxy's unique
owner is the exact launched service process. It prints only a fixed boolean
success label. It has no item/secret/prompt APIs; the bus additionally rejects
these interfaces. This is not CPA DbusBackend interoperability evidence.

All subprocess output is captured, not relayed. Only source-defined failure
labels, allowlisted success markers and nonsecret component hashes/versions
reach CI. Raw daemon/GLib/D-Bus output, journal bodies and environment contents
are not published. There are no credentials to scan or upload in this slice.
The inner harness terminates/waits for its children; the outer harness stops
only its owned unit/cgroup before removing its account and exact validated
temporary roots and public shm probe. A still-active unit stops cleanup.

## Evidence limits

Local static checks on Windows are not Linux runtime evidence. The new ordinary
PR workflow checks out and verifies the exact head; the Draft PR records that
head's actual results and original E/F review separately. Dependency/permission
failures are explicit failures, never successful skips. No manual dispatch,
release, deployment, Ready/merge, production keyring, secret-bearing fixture,
Windows/macOS acceptance or ten-tool write support is part of this slice.
Persistence/restart, duplicate items, corrupt secrets, exact deletion and
concurrent creation all remain untested and require a separate authorization.
