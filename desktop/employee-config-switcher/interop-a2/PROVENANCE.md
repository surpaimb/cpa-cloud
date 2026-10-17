# A2 sources and dependency licenses

Read 2026-10-17. New A2 Python/C/tests/workflow/docs and the small test-only Rust
extension are independently authored from this contract, CPA's own envelope
format and the public APIs below. Existing project-owned PR81 isolation and
positive-control utilities are reused explicitly, not third-party source ports.
No CLIProxyAPI/Sub2API/archived CPA code, test, asset or documentation was used;
no strict clean-room claim is made.

- [Secret Service Collection specification](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Collection.html):
  `CreateItem` properties, binary Secret, explicit replace boolean and item/prompt
  reply. A2 chooses false and rejects any prompt instead of invoking it.
- [libsecret 0.21.4 public path API declarations](https://github.com/GNOME/libsecret/blob/0.21.4/libsecret/secret-paths.h):
  session encoding, search paths, one-shot secret retrieval and item construction.
  Declarations were inspected for version compatibility; no implementation copied.
- [libsecret encoding API](https://gnome.pages.gitlab.gnome.org/libsecret/method.Service.encode_dbus_secret.html):
  binary Secret transport using the already opened libsecret session.
- [GIO synchronous D-Bus call API](https://docs.gtk.org/gio/method.DBusConnection.call_sync.html):
  explicit reply type, timeout and no-auto-start flag. The helper directly sends
  CreateItem to avoid high-level prompt-handling/replacement behavior.

Exact component versions remain in [components.json](../preflight/components.json):
dbus-daemon 1.14.10-4ubuntu4.1 (AFL-2.1 or GPL-2.0-or-later), GNOME Keyring
46.1-2ubuntu0.2 (GPL-2.0-or-later, component exceptions in package copyright),
libsecret runtime/dev 0.21.4-1build3 (LGPL-2.1-or-later). GLib/GIO retain
LGPL-2.1-or-later. Ubuntu package copyright records are required and hashed by
the unchanged supervisor. Python uses its standard library (PSF license), Rust
uses existing locked dependencies with their own licenses; no new runtime
dependency or vendored source. Compilation tools do not transfer their license
to independently authored fixture source. No distribution/release is authorized.
