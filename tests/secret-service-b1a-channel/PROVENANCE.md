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

## Source-bound runtime prerequisites / forced pre-create stop, 2026-10-20

Independently authored from the root's bounded C-only contract on sole parent
`e589b3c6db012eebc141c1b01740ab09c1cb9627`. Only fixture.py, test_channel.py,
CONTRACT.md and this file change. No archived CPA/CLIProxyAPI/Sub2API or reference
implementation was consulted or ported. Official Ubuntu shadow sources below
were read solely to establish the installed OS-helper's effects and preconditions;
their implementation was not copied into this project. No binary/source archive
is vendored or redistributed. Prior research still precludes strict clean-room
claims. No new dependency/license change; Python stdlib remains PSF-2.0 and OS
components retain their own per-file/package copyright and license notices.

### Authenticated official publication and build association

Consulted and verified 2026-10-20; only public non-secret OS-package metadata.
Existing local verification/codec tools were used in a controlled temporary
directory outside the worktree; no downloaded binary, build script, maintainer
script or hook ran and nothing was installed. Expectations were fixed from
that audit before runtime observations; runtime cannot modify the profile.

- [Ubuntu archive integrity scheme](https://documentation.ubuntu.com/security/software-integrity/archive-verification/)
  and [official Noble archive-key fingerprint](https://ubuntu.com/chisel/docs/v1.3.0/reference/chisel-releases/chisel.yaml/):
  pinned full fingerprint `F6ECB3762474EDA9D21B7022871920D1991BC93C` matched the
  fetched key and successful gpgv VALIDSIG.
- [noble-updates InRelease](https://archive.ubuntu.com/ubuntu/dists/noble-updates/InRelease),
  signed Date `Sun, 18 Oct 2026 23:55:07 UTC`, authenticated both exact indices:
  [Packages.xz](https://archive.ubuntu.com/ubuntu/dists/noble-updates/main/binary-amd64/Packages.xz)
  SHA256 `1b79de5bbdc9bfffde72130dac32d60ce0a3c12d1c9d4295d9bf840462028c86`;
  [Sources.xz](https://archive.ubuntu.com/ubuntu/dists/noble-updates/main/source/Sources.xz)
  SHA256 `0373665f34fda560b1d88f84a9dc033ecf86abcee3d7b7d0028fc32aaec70866`.
- [Original passwd amd64 .deb](https://archive.ubuntu.com/ubuntu/pool/main/s/shadow/passwd_4.13+dfsg1-4ubuntu3.2_amd64.deb):
  exact version `1:4.13+dfsg1-4ubuntu3.2`, SHA256
  `139b51228c8065eefa3051f649d8480eb986f1cf629448e49d2eaf9813318412`, size
  and bytes matched authenticated Packages. Control fields independently matched
  passwd/version/amd64 and Source shadow. The unqualified Source field means
  the same source version under [Debian Policy](https://www.debian.org/doc/debian-policy/ch-controlfields.html#source).
  Regular archive members usr/sbin/useradd, userdel and groupdel were read and
  hashed mechanically without execution; their fixed expected SHA256 values
  are in CONTRACT.md and UBUNTU_HELPER_SHA256. Only useradd has historical C #5
  runtime evidence; userdel/groupdel observations must come from this new batch.
- Authenticated Sources verified all original source bytes and .dsc internal
  tarball hashes: [orig.tar.xz](https://archive.ubuntu.com/ubuntu/pool/main/s/shadow/shadow_4.13+dfsg1.orig.tar.xz)
  SHA256 `a8bb3a2aceff1cbe39d0f50687dcc1d7e7be0516a9d954d8e2eedb93f5906207`;
  [Ubuntu debian.tar.xz](https://archive.ubuntu.com/ubuntu/pool/main/s/shadow/shadow_4.13+dfsg1-4ubuntu3.2.debian.tar.xz)
  SHA256 `ff01a3e94111ca8c4dca097ebe54edbee912cbdeba042c214bfa4c34e7839f61`;
  [.dsc](https://archive.ubuntu.com/ubuntu/pool/main/s/shadow/shadow_4.13+dfsg1-4ubuntu3.2.dsc)
  SHA256 `0be17fd044f3e23f714a5b286a04bd040f246af1ac32fcc406b63756baa9c368`.
- [Official Launchpad build28578437](https://api.launchpad.net/1.0/ubuntu/+source/shadow/1:4.13+dfsg1-4ubuntu3.2/+build/28578437)
  records shadow/exact version/amd64/Successfully built. Its public .changes and
  .buildinfo both declare the same .deb SHA256 above; .changes also matches the
  .buildinfo SHA256 `81a262c6f7a29724b0d1ea6dc7b65070d9d05d2033be1f3bae870b3b7082c82d`.
  Build log line1796 records `--disable-account-tools-setuid`; line2095 says
  suid account management tools=no; lines2106-2108 say nscd/sssd/subordinate IDs=yes.
  Its 24 applied-patch names/order match strict no-fuzz mechanical reconstruction.
  These are official publication/build records, not an independently reproduced
  build or independent verification of uploader/build-record individual signatures.

### Exact patched source facts, not upstream-master assumptions

The authenticated 24-patch source view has useradd.c SHA256
`a31bf498bf8cafa669e73a78cac6875010cef817b15ec2e157261f434ebe488f` and run_part.c
SHA256 `ee2ef5c1601f76d0cb962e4b138194e0f3ec20fd900058e4a07cafc10219b018`.
Source-relative references refer to this precise reconstructed view:

- src/useradd.c:1232,1426-1427,2817-2820: --system sets rflg and bypasses only
  the built-in mailbox call; src/useradd.c:2587,2822 identify both unconditional
  pre/post hook paths and their order around account writes and later effects.
- lib/run_part.c:14-102: enumeration/alphasort/stat/execv, including following
  symlink targets, no executable-bit-only admission filter, and scandir<=0
  returning success. The new metadata predicate is independently written from
  this project's fail-closed contract, not ported from this implementation.
- src/userdel.c:1152,1391 identify its pre/post hook paths; 767-774 and1387-1388
  show USERDEL_CMD, 1286-1318 show UID>=1000 ZSys even without removal flag,
  discarded child output and no successful-child result gate. Actual cleanup
  helper/source association does not certify these external effects.
- src/useradd.c:2774-2784 and lib/nscd.c/lib/sssd.c identify cache/tally helpers;
  src/useradd.c:1632-1634,2803-2815 bounds no-home behavior, not all side effects.
  Runtime defaults/configuration, external helper/dependency identity and
  privileged drift/check-to-exec remain unknown; none is read/probed here.

The new fixed-role diagnostic exception is explicitly authorized by this batch,
bounded to three helper/four hook ASCII records after close attempts. It cannot
expand historical output, disclose actual paths/entries/configuration/secrets,
self-approve observed hashes, or turn the forced red into C PASS. Original 118
public test bodies remain; new public metadata/iterator/sink/close adapters are
controlled regressions rather than actual I/O faults or user/unit execution.
No old C/A3 red is erased or explained retroactively. Natural same-head evidence
and original E/F formal review are required; production remains NO-GO.

## Existing hook-result stage diagnostics only, 2026-10-20

Independently project-authored from the root's bounded contract on sole parent
`27627098d5da6d6c492913b519287fe5d9248880`. Only fixture.py, test_channel.py and
these two notes change. The earlier source profile, Ubuntu archive/build facts,
existing predicates/argv/UID/success/ownership, channel/receiver/workflow and
all PR88/product/pin/history files remain unchanged. No external implementation,
binary or new third-party dependency is added. Existing license terms remain.

Classification consumes the original component position, nofollow mode,
before/after comparison, enumeration outcome and consumed close confirmation.
It does not inspect error text/errno, look up paths again or add an I/O/config/
probe/retry/execution. Missing ancestors and missing verified leaves retain
different stages; scan-phase exceptions do not assert leaf absence. Unknown
remains unknown, and original operation/refusal survives later cleanup/output
loss. FD and iterator confirmations stay separate, never imply legacy aggregate
cleanup or a checked role that was unvisited. The output stays within four
existing bounded hook records with finite enums only; helper limits unchanged.

Interface basis remains the previously cited Python 3.12 nofollow/scandir/close
contracts and this project's explicit metadata-only fail-closed contract.
Public phase/iterator/metadata/sink adapters and old/new I/O-trace comparison
are independently authored controlled regressions, not a real kernel fault,
real user/unit operation or cause of historical C #1-#6 red. The old 140 test
bodies and 68 frozen functions are retained. A new natural head has its own
attempt1 evidence and formal original E/F review; no broader GO is inferred.
