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
the supervisor. The 2026-10-18 diagnostic extension adds only independently
authored fixed-category reporting, optional cleanup result hooks and public
dummy/mock tests to the existing harness; its isolation and protocol checks
remain the sources above. Python uses its standard library (PSF license), Rust
uses existing locked dependencies with their own licenses; no new runtime
dependency or vendored source. Compilation tools do not transfer their license
to independently authored fixture source. No distribution/release is authorized.

The 2026-10-19 bounded correction independently authors
`../interop/private-envelope.h` for the existing actual-response sample writer,
fixed read-failure categories and public writer/reader compatibility tests.
It uses the existing POSIX file operations and project-owned strict private-file
contract; no new dependency or third-party implementation is imported. The same
test-only writer is included by the existing libsecret verifier and A2's public
dummy self-test; production provider code and protocol calls are unchanged.

The subsequent 2026-10-19 path-only correction independently authors
`../interop/private-path.h` from the same project-owned private-file contract
and POSIX file operations. It writes the existing libsecret search-result path,
not a replacement path, and is shared only with the public dummy self-test.
Fixed path-check categories reuse the existing checks; no new protocol source,
dependency, production change or additional real path/secret read is introduced.
