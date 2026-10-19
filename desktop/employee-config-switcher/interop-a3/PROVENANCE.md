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

## Independently bounded parent crash-result diagnostics — 2026-10-19

This batch is independently authored from the original visible E/F diagnostic
contract and [Python 3.12 subprocess status/wait documentation](https://docs.python.org/3.12/library/subprocess.html#subprocess.Popen.returncode).
It classifies only existing in-memory ready/boundary/kill/wait/capture results.
Unexpected crash output is screened against the unchanged existing fixed Rust
label whitelist; digit-bearing A3 labels are kept whole. A matched label is an
observation, not by itself proof of a particular underlying product fault.
No raw output/status, new secret read, service call, dependency, license change,
business fix or changed SIGKILL/cleanup acceptance predicate is introduced.
Public dummy control-flow tests do not perform a real CPA crash experiment.

## Independently bounded journal error-family diagnostics — 2026-10-19

The existing cfg(test) A3 call site classifies only its single returned
CleanupJournalError, using fixed error-family tags and a predeclared whitelist
of [Rust io::ErrorKind categories](https://doc.rust-lang.org/std/io/enum.ErrorKind.html).
Unlike [Result::expect](https://doc.rust-lang.org/std/result/enum.Result.html#method.expect),
the error path does not format the error's Debug/Display, paths or raw OS codes;
it retains a fixed panic and unchanged failure gate. The actual Result is not
retried or replaced; the business journal implementation is unchanged.
Non-crash parent capture reuses the existing fixed whitelist helper so existing
digit-bearing A3 tags remain intact; unknown text is never relayed.
The change and public dummy tests are independently authored from the original
visible E/F bounded contract and these standard-library API references. No new
dependency/license change, production capability or strict clean-room claim.
