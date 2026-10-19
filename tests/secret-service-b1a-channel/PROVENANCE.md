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

## Independent raw-helper handoff batch, 2026-10-20

Project-authored from the bounded contract and original E/F static review of
sole parent `0ba876cae2320529d62b47e6ea3598ecd973d8ab`. This review is not proof
of C #3's runtime cause or absence of partial effects. Only fixture.py,
test_channel.py and these two notes change; channel.py, receiver/workflow,
all PR88 files, product, helper arguments/UID/permissions, exit vocabulary and
isolation remain frozen. No argument-compatibility repair is part of this batch.

The previously recorded Python subprocess returncode/poll/wait contract is
reused, not copied implementation: existing observations are retained in an
independent private record; no extra observation, helper or lookup is added.
Controlled public process/stream and public FD-reuse negatives are independently
written here, distinguish physical cleanup from incomplete capture/delivery,
and are not real kernel/output I/O-fault or real-user provisioning evidence.
No new dependency or license change. All old red evidence remains historical;
new source requires its own natural CI and formal E/F review.

## C-only helper identity evidence / intentional stop, 2026-10-20

Independently authored from the root-approved evidence-only contract on sole
parent `8f09f7f402e4acb38bcd4033787d31ac45d367e9`. No user is created by this
batch. Only fixture.py, test_channel.py and these notes change. Existing argv,
channel/receiver/workflow, product, PR88/old regressions and trust boundary stay
unchanged. Root/OS are trusted; observed identity is not source approval and
does not identify the cause of old exit-option failures. Prior public research
does not support a strict clean-room claim.

Primary documentation consulted 2026-10-20, used as interface facts, not copied
implementation:

- [Python 3.12 shutil.which](https://docs.python.org/3.12/library/shutil.html#shutil.which)
  and [Popen](https://docs.python.org/3.12/library/subprocess.html#subprocess.Popen):
  explicit POSIX PATH lookup for an unqualified executable and an independent
  read-only selection, not a new execution or check-to-exec guarantee.
- [Python 3.12 os](https://docs.python.org/3.12/library/os.html): nofollow held
  directory/file APIs, complete bounded reads and stable metadata snapshots.
- [dpkg-query database description](https://manpages.debian.org/bookworm/dpkg/dpkg-query.1.en.html)
  and [dpkg database/files](https://manpages.debian.org/bookworm/dpkg/dpkg.1.en.html):
  installed status, ownership lists and metadata consistency, not a package
  source/build or binary-authenticity assertion. No dpkg/helper process is run.

The contract's one bounded structured non-secret OS-helper identity exception
is new and explicit; it does not authorize config/hook/raw path/identity/secret
output or arbitrary-file digests. SHA-256 is the entire selected helper;
internal MD5 only compares its bytes to dpkg's recorded checksum, with no
self-approval. No pre-reviewed allowlist exists, so source/no-mailbox/hooks
remain unknown. No old implementation source was used as a template or ported.
No new dependency: Python stdlib remains PSF-2.0; installed OS component licenses
and existing package provenance retain their own terms. No package is added or
distributed. Public metadata adapters and FD-reuse tests are not real fault
injection or credential tests. Expected natural red is an intentional stop,
not retrospectively called PASS; old C/A3 red history remains distinct.
