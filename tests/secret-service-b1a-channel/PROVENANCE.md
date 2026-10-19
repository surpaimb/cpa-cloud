# B1a-C provenance and dependencies

Written independently for the CPA Cloud B1a-C contract on 2026-10-19 by this
project's Codex integration task. All four Python files, this contract/provenance,
the separate workflow and preflight document are newly authored project source.
No CLIProxyAPI, Sub2API, archived CPA, reference-tree implementation, tests,
migrations, assets or SDKs were used. Prior project research prevents a strict
clean-room claim. Existing project preflight isolation requirements and PR88
scope were read as contracts; its supervisor was not copied, imported or patched.

Public primary sources consulted 2026-10-19 (behavioral facts, not implementation
templates):

- [GNOME Keyring 46.1 upstream manual](https://github.com/GNOME/gnome-keyring/blob/46.1/docs/gnome-keyring-daemon.xml):
  `--login` consumes stdin including newlines; `--unlock` reads a password from
  stdin. This motivates exact bytes + EOF and rejecting CR/LF, **not** a claim
  that C executes GNOME. No GNOME implementation source was ported.
- [systemd-run manual](https://www.freedesktop.org/software/systemd/man/systemd-run.html):
  transient exec service, `--pipe` hands original FDs through without reopening,
  synchronous wait and explicit properties. Only switches supported by Ubuntu
  24.04 systemd are used, not newly added options from later manuals.
- [systemd execution isolation](https://www.freedesktop.org/software/systemd/man/systemd.exec.html):
  private namespaces, filesystem restrictions, minimal environment and UID.
- [Python 3.12 os](https://docs.python.org/3.12/library/os.html),
  [subprocess](https://docs.python.org/3.12/library/subprocess.html),
  [HMAC](https://docs.python.org/3.12/library/hmac.html): anonymous CLOEXEC/nonblocking
  pipes, fstat/read/write/close, explicit stdin FD, close_fds, bounded wait and
  constant-time receipt comparison.
- [D-Bus daemon configuration](https://dbus.freedesktop.org/doc/dbus-daemon.1.html):
  a new local session bus address, EXTERNAL authentication and private policy.

No new Python package dependency or product runtime linkage. Python standard
library retains PSF-2.0; systemd tools retain their upstream LGPL/GPL licenses;
D-Bus retains its upstream AFL-2.1/GPL-2.0-or-later licensing. Installed binaries
are invoked on the disposable runner and are not copied into repository/package
outputs. Ubuntu package copyright/license files remain authoritative. The new
workflow pins `dbus-daemon=1.14.10-4ubuntu4.1` (the existing project manifest's
version, without modifying that manifest), verifies the version and package
copyright file, and does not execute/install GNOME as a C component;
the unchanged old regressions have their own pinned component provenance.

The checkout action is the same existing audited project pin, newly referenced
without changing any old workflow. This is not release, package or deployment
authorization. No real credential/configuration, official client or provider is
used. Report B0 no-secret, C synthetic-password and A2/A3 synthetic-secret as
separate evidence classes; do not erase PR87's historical seven reds.

## Safety/diagnostic follow-up, 2026-10-19

The bounded independently authored repair touches only channel.py, fixture.py,
test_channel.py and these C contract/provenance notes. Original E/F static
findings are project review inputs, not borrowed implementation and not proof
of C #1's original runtime cause. No new third-party dependency or old-file,
receiver/workflow/pin/policy/product change is introduced.

Additional primary behavior references, consulted 2026-10-19:

- [Linux close(2)](https://man7.org/linux/man-pages/man2/close.2.html): a failure
  does not justify retrying a descriptor number that may already be reused.
  This Linux-only acceptance records consumption separately from confirmation;
  it does not assert a universal non-Linux close-error rule.
- [Python 3.12 directory APIs](https://docs.python.org/3.12/library/os.html#os.scandir):
  explicit scandir/held-directory operations and exceptions, rather than
  os.walk's default ignored enumeration failures. No manual example or upstream
  implementation was copied or translated.

Controlled public adapters, public sentinel reuse, and deterministic directory
mutation tests are not presented as real kernel fault injection, production
credential tests or the cause of the old red. Historical C #1 and PR87 red/green
records remain distinct from every new-head natural run and formal review.

## Sink-delivery and exit-class batch, 2026-10-19

Independently authored from the bounded project contract and original E/F's
static P2 review of parent `38e3f96f16c1c17367e7d90d9415889fb20e7ffd`.
Only fixture.py, test_channel.py and these two notes change. channel.py,
receiver/workflow and all PR88/product files stay byte-frozen. The finding is
not asserted to be C #2's user-create rejection cause or proof about residue.

Additional public primary documentation consulted:

- [shadow 4.13 useradd exit-value manual](https://github.com/shadow-maint/shadow/blob/4.13/man/useradd.8.xml):
  finite documented return classes, used only to label an already-observed
  result; no implementation source, example or command was copied. Unmapped
  distro/version values retain an unknown fallback rather than inferred cause.
- [Python 3.12 Popen.returncode](https://docs.python.org/3.12/library/subprocess.html#subprocess.Popen.returncode):
  absent result and POSIX negative signal-result semantics. No extra poll,
  wait, process lookup or helper is introduced for this classification.

New sink-failure, joint-error, resource/FD-sentinel and return-class public
negatives are project-authored controlled tests, not real I/O fault injection,
real identity provisioning, GNOME/Key acceptance or the cause of old failures.
No new dependency or license change. Old test counts and red evidence remain
historical; each new head requires fresh natural CI and formal review.
