# A3 sources / dependency boundary

New A3 Rust/Python/C/tests/policy/workflow/docs are independently authored from
the F preflight contract, CPA's current provider, synthetic replacement/store
APIs and its own envelope format, not archived/adjacent reference projects.
Existing project-owned preflight/A2 reporting, private writers and libsecret
verifier utilities are explicitly reused. No strict clean-room claim is made.

Public sources read 2026-10-19:

- [Item.Delete and its prompt reply](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Item.html)
- [D-Bus daemon policy ordering/header matching](https://dbus.freedesktop.org/doc/dbus-daemon.1.html)
- [Collection Items](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Collection.html)
- [Linux kill](https://man7.org/linux/man-pages/man2/kill.2.html),
  [fsync](https://man7.org/linux/man-pages/man2/fsync.2.html)

Rust 1.90 and existing Cargo.lock dependencies are unchanged, including dbus
0.9.12 (Apache-2.0/MIT), zeroize 1.8.2 and libc 0.2.190. The existing component
pins and package copyright evidence remain authoritative: dbus-daemon
1.14.10-4ubuntu4.1, GNOME Keyring 46.1-2ubuntu0.2, libsecret 0.21.4-1build3.
GLib/GIO/libsecret retain LGPL-2.1-or-later; daemon components retain their
existing package-specific licenses. Python uses its standard library. No new
dependency, third-party implementation or distribution is introduced.

## Independently bounded private-log writer batch — 2026-10-19

The common private-log writer and its public dummy regressions are independently
authored from the original visible E/F writer-only contract and
[Python 3.12 os.open/write/fstat](https://docs.python.org/3.12/library/os.html#os.open),
[Python raw I/O](https://docs.python.org/3.12/library/io.html#io.RawIOBase) and
[Linux open(2)](https://man7.org/linux/man-pages/man2/open.2.html).
Only the three A3 log creation sites use this helper. The reader, fixed labels,
registration sets, business and owned-cleanup gates remain unchanged. Neither
historical red is used to infer an actual numeric file mode or its origin.
Tests exercise this actual writer rather than a substitute log creator.
Only Python's existing standard library is used; no dependency or license
change, copied third-party implementation or strict clean-room claim is added.
