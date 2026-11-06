# B0 provenance and fixed-version research

Reviewed 2026-10-17. `fixture.py`, `owner.c`, `test_fixture.py`, CONTRACT.md and the
dedicated workflow are independently authored for this project's B0 contract.
The shared no-key Python supervisor, D-Bus policy and libsecret observer are this
project's existing PR80/81 sources, reused unchanged. No CLIProxyAPI, Sub2API,
archived CPA, official client implementation or SDK is used as a template.
This is not a strict clean-room claim. No third-party source is copied or ported.

## Runtime dependencies

The unchanged [component manifest](../../desktop/employee-config-switcher/preflight/components.json)
is authoritative: dbus-daemon `1.14.10-4ubuntu4.1`, gnome-keyring `46.1-2ubuntu0.2`,
libsecret runtime/dev `0.21.4-1build3`, Ubuntu 24.04. Missing exact packages fail.
The shared supervisor records installed package versions, package copyright-file
SHA-256 and daemon/observer hashes; B0 adds its observer source/executable hashes.

D-Bus retains AFL-2.1 OR GPL-2.0-or-later terms per file; GNOME Keyring retains
GPL-2.0-or-later / LGPL-2.1-or-later per-file notices; libsecret LGPL-2.1-or-later.
GLib/GIO (LGPL-2.1-or-later), Python (PSF), systemd (LGPL-2.1-or-later with
per-file exceptions), and GCC (GPL-3.0-or-later plus runtime exceptions where
applicable) are runner/compiler dependencies, not newly vendored sources.
The four primary runtime package versions are pinned; this is not a claim that
the entire runner toolchain/transitive dependency closure is pinned. Ubuntu
package copyright files remain authoritative for shipped package contents.
Nothing is packaged or redistributed by B0.

## Read-only GNOME 46.1 findings

Upstream fixed-tag source was inspected only to establish operating behavior and
license provenance, not translated/reimplemented. The installed Ubuntu patch
version is separately pinned above; B0 runtime checks validate only its own
no-secret contract, not every upstream implementation detail.

- [gkd-main.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/daemon/gkd-main.c)
  (GPL-2.0-or-later): `--login`/`--unlock` read stdin; newline bytes are input,
  not delimiters. Normal foreground startup avoids those paths. The daemon has
  discovery paths for other invocation modes; B0 never uses start/replace/login/
  unlock or trusts printed environment assignments. SIGTERM exits the main loop.
- [gkd-login.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/daemon/login/gkd-login.c)
  (LGPL-2.1-or-later): login unlock can create a token-backed login collection if
  absent. Therefore a fake-input unlock is not a no-collection probe and is not
  performed. Persistent provisioning and post-restart unlock remain B1 work.
- [gkd-control-client.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/daemon/control/gkd-control-client.c)
  and [gkd-control-server.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/daemon/control/gkd-control-server.c)
  (LGPL-2.1-or-later): control transport uses a Unix socket named `control` below
  the selected runtime control directory, with same-UID credential checks.
  B0 examines socket metadata only. No protocol encoding or request is ported.
- [gkm-util.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/pkcs11/gkm/gkm-util.c)
  (LGPL-2.1-or-later, with noted GLib-derived LGPL-2.0-or-later portions): the
  modern directory is XDG data home plus `keyrings`; legacy HOME/.gnome2/keyrings
  fallback is possible if the modern directory is absent. B0 creates the modern
  private directory first and requires the synthetic HOME to remain empty.
- [gkm-secret-module.c](https://raw.githubusercontent.com/GNOME/gnome-keyring/46.1/pkcs11/secret-store/gkm-secret-module.c)
  (LGPL-2.1-or-later): persistent tracking uses `*.keyring`; B0's public sentinel
  does not use that suffix. Service startup creates an intrinsic transient
  session collection; B0 does not operate on it or claim persistent provisioning.

## Public metadata API references

- [D-Bus bus methods](https://dbus.freedesktop.org/doc/dbus-specification.html#message-bus-messages)
  define GetNameOwner, NameHasOwner, GetConnectionUnixProcessID and
  GetConnectionUnixUser. B0's independent GIO probe calls only those bus methods,
  with NO_AUTO_START; it calls no Secret Service methods.
- [libsecret Service flags](https://gnome.pages.gitlab.gnome.org/libsecret/flags.ServiceFlags.html)
  distinguish metadata-only NONE from opening sessions/loading collections.
  B0 reuses the existing observer's NONE path for each instance.

Research supports a future noninteractive mechanism candidate, not a verified
password channel or persistent Key recovery. No upstream source review expands
the [B0 contract](CONTRACT.md) or authorizes B1/production use.
