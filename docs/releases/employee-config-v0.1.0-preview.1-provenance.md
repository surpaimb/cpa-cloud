# Employee client preview package provenance

The new release workflow, package checker, notice collector, pnpm shim
verifier and its synthetic tests, junction path tests, Vite module evidence
script, Tauri release overlay, and release notes are independently
authored CPA Cloud distribution work. They do not import or adapt archived CPA
implementation code.

Public interface sources used for packaging decisions:

- [Tauri macOS DMG distribution](https://v2.tauri.app/distribute/dmg/) for the
  universal target and DMG layout.
- [Tauri Windows installer distribution](https://v2.tauri.app/distribute/windows-installer/)
  for the NSIS installer, current-user install, WebView2 bootstrapper, and
  installer filename form.
- [Tauri macOS signing](https://v2.tauri.app/distribute/sign/macos/) and
  [Tauri GitHub pipeline guidance](https://v2.tauri.app/distribute/pipelines/github/)
  for the certificate-free ad-hoc signature and the distinction from Apple
  Developer signing and notarization.
- [Vite JavaScript build API](https://vite.dev/guide/api-javascript) and
  [Rolldown output chunks](https://rolldown.rs/reference/Interface.OutputChunk)
  for the one-build `chunk.modules` input ledger. This records JavaScript
  inputs and all `dist` file hashes; it is not a binary asset readback.
- [Rolldown virtual module convention](https://rolldown.rs/apis/plugin-api)
  for the NUL-prefixed module ID format. The observed exact
  `\0rolldown/runtime.js` input is attributed only to the locked
  `rolldown@1.2.13` package and requires its full license text. Query
  variants and unknown virtual IDs remain rejected.
- [Node.js native realpath](https://nodejs.org/api/fs.html#fsrealpathsyncnativepath-options)
  for canonicalizing only a newly created OpenClaw test fixture root before
  the path guard inspects it. The guard and full `pnpm test` suite remain.
- [pnpm install options](https://pnpm.io/cli/install),
  [symlinked node_modules layout](https://pnpm.io/symlinked-node-modules-structure),
  [configuration locations](https://pnpm.io/configuring), and the
  [pnpm 11.25.0 registry manifest](https://registry.npmjs.org/pnpm/11.25.0)
  for a same-runner,
  same-platform offline reference install using the exact package manifest and
  lockfile. The reference disables lifecycle scripts. The verifier requires
  identical physical package roots, complete nested entry sets, and complete
  generated-shim bytes after replacing the canonical workspace root on macOS
  or the three observed root spellings on Windows. It removes only individually
  rechecked regular shim files and then empty generated directories; the notice collector still
  rejects every remaining nested node_modules and every unexplained extra.
  The workflow downloads the exact registry tarball
  `pnpm-11.25.0.tgz`, checks its fixed SHA-512 integrity
  `XN6SW08HX3Jetx+64YpC/+eEUkeJ8ZthxzHLhyHsKKruFg4BqNWvT+2ypCzb8wDv4j2zVrDUoXtNY+EfirfJVg==`
  before extraction, rejects unsafe archive members, and checks the extracted
  entry set and every file against that archive before each pnpm invocation.
  The direct Node entry `bin/pnpm.mjs` and bundle `dist/pnpm.mjs` also have
  fixed SHA-256 pins. Node 24.11.0 and its resolved executable bytes are
  checked within each job. These checks do not establish a race-free
  filesystem snapshot or employee-device behavior.
- [Tauri configuration](https://v2.tauri.app/reference/config/) for the
  relative `frontendDist` and release-only empty `beforeBuildCommand`. The
  package job checks the same `dist` bytes again after Tauri bundling.
- [GitHub Releases REST API](https://docs.github.com/en/rest/releases/releases)
  and [workflow runs REST API](https://docs.github.com/en/rest/actions/workflow-runs)
  for the merged-source, CI, tag, draft asset, and published asset checks.
- [GitHub's `gh release` reference](https://cli.github.com/manual/gh_release)
  for draft creation, download, and publication.

A draft Release may precede its Git tag and is not reliably returned by the
REST get-by-tag endpoint. The release guard fully pages the authenticated
Release listing, requires a unique exact-tag match, and reads that Release
again by ID. It checks the draft's exact `target_commitish` and requires the
tag to be absent at that stage. An incomplete listing fails closed. After
publication it resolves either a lightweight or a single annotated tag to the
merged source commit, checks the published get-by-tag ID, and rechecks the
frozen asset identities. The separate GitHub API calls are not an atomic
snapshot; publication still requires their guarded readbacks to agree.

The repository's locked Cargo and pnpm dependencies retain their own licenses.
The distribution process must obtain the relevant full license and notice texts
from integrity-verified archives or independently verified upstream sources,
record each text's source and digest, and embed them in both installers. The
nineteen full license texts, two source declarations, and two upstream README
attribution-context files in
`desktop/employee-config-switcher/third_party/notice-fallbacks/` preserve
verified upstream license or source declarations for archives that omit those
texts. The notice collector binds their Git blobs, origin commits and hashes;
it labels them as supplements rather than as files shipped inside the
dependency archives. The Vite module ledger must prove missing-text npm build
dependencies are absent from executable frontend output before they may be
excluded from installer notices. The existing CC Switch-derived files retain
their recorded provenance and license
in `desktop/employee-config-switcher/PROVENANCE.md` and
`third_party/cc-switch/LICENSE`. A missing or unverified required notice
prevents publication.

For `block2@0.6.2`, the fixed upstream
[objc2 `LICENSE.md` at `b4167b582b2f75f9a1be75495c41b765344fd03c`](https://github.com/madsmtm/objc2/blob/b4167b582b2f75f9a1be75495c41b765344fd03c/LICENSE.md)
declares that `block2` is currently MIT licensed, but does not contain the
full MIT terms. The separate
[SPDX MIT text at `ab9b0904b43db6f4b67431efd8821feb821f1c8b`](https://github.com/spdx/license-list-data/blob/ab9b0904b43db6f4b67431efd8821feb821f1c8b/text/MIT.txt)
supplies the standard full-text template. Its literal `<year>` and
`<copyright holders>` placeholders remain unchanged: the project has not
identified a copyright holder and does not attribute one to upstream. The
collector requires both pinned files, records the upstream declaration as
`EXTERNAL_ORIGIN/UPSTREAM_DECLARATION.md` outside the authorization-name
check, and records the separate template as `EXTERNAL_ORIGIN/LICENSE-MIT.txt`.
Both outputs are checked against the committed source bytes during verification.

For `dispatch2@0.3.1`, the fixed official crate SHA-256 is
`1e0e367e4e7da84520dedcac1901e4da967309406d1e51017ae1abfb97adbd38`
(55,299 bytes, 26 regular members). The crate's `.cargo_vcs_info.json` must
identify [`objc2` commit `8852b424193ca41602281b3d7540d7c8ed51e49a`](https://github.com/madsmtm/objc2/commit/8852b424193ca41602281b3d7540d7c8ed51e49a)
and `crates/dispatch2`; Cargo metadata must identify the same repository and
the `Zlib OR Apache-2.0 OR MIT` license expression. That commit's root
[`LICENSE.md`](https://github.com/madsmtm/objc2/blob/8852b424193ca41602281b3d7540d7c8ed51e49a/LICENSE.md)
is byte-identical to the existing block2 upstream declaration. Its “All
other crates” clause includes dispatch2 and permits MIT. The collector uses
that declaration only as an origin statement and separately reuses the pinned
[SPDX MIT template](https://github.com/spdx/license-list-data/blob/ab9b0904b43db6f4b67431efd8821feb821f1c8b/text/MIT.txt)
as the complete license text. Literal `<year>` and `<copyright holders>`
placeholders remain; no upstream copyright holder is invented. Both output
files must match the committed source bytes during verification. The synthetic
tests override the archive hash/length with a small local fixture only to
exercise the branch; they do not authenticate the official crate.

For [`dlopen2@0.8.2`](https://static.crates.io/crates/dlopen2/dlopen2-0.8.2.crate)
and [`dlopen2_derive@0.4.3`](https://static.crates.io/crates/dlopen2_derive/dlopen2_derive-0.4.3.crate),
the separate official archive SHA-256 and size pins are respectively
`5e2c5bd4158e66d1e215c49b837e11d62f3267b30c92f1d171c4d3105e3dc4d4`
at 27,695 bytes and
`0fbbb781877580993a8707ec48672673ec7b81eeba04cfd2310bd28c08e47c8f`
at 7,032 bytes. The archives contain 43 and 10 ordinary files and no full
license member. Both `.cargo_vcs_info.json` files identify
[`OpenByteDev/dlopen2` commit `cc80e4a0a90d499b677fdf7743699b4b3a43a989`](https://github.com/OpenByteDev/dlopen2/commit/cc80e4a0a90d499b677fdf7743699b4b3a43a989)
with distinct paths `dlopen2` and `dlopen2-derive`. Both Cargo metadata labels
are MIT and name that repository. The root
[`LICENSE`](https://github.com/OpenByteDev/dlopen2/blob/cc80e4a0a90d499b677fdf7743699b4b3a43a989/LICENSE)
at that exact VCS commit is a complete MIT text with three recorded copyright
holders. Its 1,184 committed bytes are shared as one vendor source, but each
crate must independently pass its own archive, metadata, VCS, and installed
notice readback checks. Earlier review of a different upstream commit is not
used to claim the archives' VCS identity. Local synthetic tests replace
archive pins only to exercise refusal branches; the official archive bytes
and checksums are separate frozen source evidence.

For `cesu8@1.1.0`, the
[official crate archive](https://static.crates.io/crates/cesu8/cesu8-1.1.0.crate)
is pinned by its complete SHA-256 and exact seven regular members. Those seven
members match Git blobs at the
[upstream `cesu8-rs` release commit `f18962a9de5189f21600b7ff2898845157df4447`](https://github.com/emk/cesu8-rs/commit/f18962a9de5189f21600b7ff2898845157df4447).
The archive has no `.cargo_vcs_info.json` and no complete license text; the
legacy fallback does not invent either. The archive's Cargo metadata, README,
and COPYRIGHT declare Apache-2.0/MIT. Separate complete
[Apache](https://github.com/rust-lang/rust/blob/db2939409db26ab4904372c82492cd3488e4c44e/LICENSE-APACHE)
and [MIT](https://github.com/rust-lang/rust/blob/db2939409db26ab4904372c82492cd3488e4c44e/LICENSE-MIT)
texts are pinned byte for byte from Rust commit
`db2939409db26ab4904372c82492cd3488e4c44e`. That commit is a source
for the referenced standard terms, not a commit designated by the `cesu8`
author. The collector requires both complete texts and the exact archive
evidence before a notice can pass.

The [first native package run](https://github.com/surpaimb/cpa-cloud/actions/runs/44748163357)
stopped at the Python pin on both platforms. The
[second](https://github.com/surpaimb/cpa-cloud/actions/runs/44752206438)
stopped at the macOS OpenClaw fixture and Windows Rolldown virtual ID above.
The [third](https://github.com/surpaimb/cpa-cloud/actions/runs/44756426281)
stopped at the clean-worktree gate on both platforms. The
[fourth](https://github.com/surpaimb/cpa-cloud/actions/runs/44759255265)
had a macOS Rust nonblocking-lock WouldBlock failure and a Windows nested
node_modules refusal. The [fifth](https://github.com/surpaimb/cpa-cloud/actions/runs/44762314834)
stopped at nested node_modules on both platforms. The
[sixth](https://github.com/surpaimb/cpa-cloud/actions/runs/44764990004)
also failed in notice collection on both platforms; no installer build or
accepted asset followed. The lock holder and cause of the fourth-run Rust failure remain unknown;
its later passing instances do not erase the failure, and no manual rerun was
used.

The [seventh](https://github.com/surpaimb/cpa-cloud/actions/runs/44774713254)
failed before either installer was inspected. Windows step 6, frontend graph
installation, ended with the helper's generic `pnpm generated-shim verification
refused` message; the specific check is unknown. macOS step 8, generated-shim
reconciliation, stopped when two synthetic notice-path mocks did not raise the
expected refusal. A macOS temporary-root alias is a hypothesis, not an
established cause. Publication was skipped and no accepted package asset was
produced. The bounded phase/category diagnostic and fixture control in this
candidate seek better evidence without relaxing either gate.

The [eighth](https://github.com/surpaimb/cpa-cloud/actions/runs/44777377555)
also produced no accepted installer. Windows stopped at the helper's
`phase=config category=environment_config` preflight; an extra exported
`npm_config_` or `pnpm_config_` name was present, but its exact name and source
are unknown. macOS verified nine generated nested directories and ten shims,
then their removal, but the unchanged clean-worktree gate found one other
untracked entry and refused notice collection. Its path is unknown; a Python
bytecode cache is only a hypothesis. This candidate clears inherited
package-manager configuration names before each pnpm/helper step, restores
the two pinned config references, suppresses bytecode writes for the two
Python test invocations, and adds a fixed-category cache count. The next
natural run reached different failures; it did not produce installers.

The [ninth](https://github.com/surpaimb/cpa-cloud/actions/runs/44781497533)
stopped on Windows at `phase=source_inputs category=lockfile` and on macOS
during the second complete-notice block. Publishing was skipped and neither
installer was accepted. The physical Windows runner lockfile bytes were not
captured; CRLF line endings are a hypothesis, not an established cause. The
lockfile candidate accepts only the exact pinned version-line prefix with LF
or CRLF, while requiring source and reference files to match byte for byte.
The macOS notice failure was `Cargo package lacks full license text:
block2@0.6.2`. The separately pinned upstream declaration and standard MIT
template let the next natural macOS run reach a later missing-notice failure;
that run did not complete collection or accept an installer.

The [tenth](https://github.com/surpaimb/cpa-cloud/actions/runs/44789221309)
stopped on macOS at `Cargo package lacks full license text: cesu8@1.1.0`.
Windows failed two synthetic block2 declaration checks while reading the
physical checkout bytes. The production source pin remained a Git blob with
LF endings; the Windows physical line endings were not captured, so CRLF is
only a hypothesis. The isolated test fixture accepts either wholly LF or
wholly CRLF input only after canonical LF bytes pass the fixed length and
SHA-256 pin; production notice source checks are unchanged. Both jobs failed,
publication was skipped, and neither installer was accepted.

The [eleventh](https://github.com/surpaimb/cpa-cloud/actions/runs/44797802645)
also produced no installer. macOS passed the clean-tree and shim gates but
stopped at `Cargo package lacks full license text: dispatch2@0.3.1`;
Windows stopped after the offline reference install with `phase=reconcile
category=other_refusal`. The exact Windows comparison or cleanup branch and
whether a shim unlink began were not recorded. This candidate adds only fixed
stage, reason, and `mutation_possible` diagnostics to that fail-closed path;
it does not change the comparison, removal, or exit order.

The [twelfth](https://github.com/surpaimb/cpa-cloud/actions/runs/44806498260)
failed on both macOS and Windows at the generated-shim step. The old-head
synthetic test's exact captured-stderr assertion at line 260 failed on both;
the StringIO value was not printed, so the actual refusal fields and common
cause remain unknown. Real reconciliation, notice collection, and installer
build were not reached. This candidate preserves the strict assertion and
adds only bounded, fixed-field diagnostics for a future natural run.

The [thirteenth](https://github.com/surpaimb/cpa-cloud/actions/runs/44809859743)
again failed in that synthetic test on both platforms. The fixed diagnostic
was `phase=runner_root category=reparse length=76 mock_calls=0` on macOS and
`phase=runner_root category=root_alias length=79 mock_calls=0` on Windows.
Neither run recorded the physical path or reached the injected reconciliation
mock, real reconciliation, notice collection, or installer build. The exact
runner-path causes remain unknown. This candidate canonicalizes only the
synthetic temporary root and tests mocked junction and alias refusals; it
does not loosen the production root guard or erase the old failures.

Local evidence also retains the v25 sanitizer stress timeout, the first v26
allowlist-verifier error, the v27 empty-stderr summary error, and the v28
Windows-newline oracle harness error. Those were local tool or evidence stops,
not additional natural package runs or accepted product tests.
The later v31 source sealing attempt stopped on truncated patch-tool output;
v32 stopped on a JavaScript/PowerShell template parsing error before copying;
v33 stopped on a document patch context mismatch. The two official source
files were independently rechecked, but those stopped batches are not claimed
as completed seals or product tests. The v34 static-seal command stopped on
a Git line-ending warning; v37 stopped on truncated patch-tool output after
partial isolated edits. Neither stopped batch is retroactively accepted.
This v38 copy has only a static source seal; its new `cesu8` fallback has not
run a collector test or native Mac/Windows package workflow.
The v39 isolated notice test had a local 20/20 aggregate pass, but its AST
evidence verifier stopped; v40 and v41 evidence preparation also stopped.
The first #12 read-only/preflight batches stopped on truncated tool output,
and #13 stopped on a document patch context mismatch after partial isolated
edits. None of these stops is a completed seal or native package acceptance.
The #14 isolated candidate locally passed 24 notice and 23 shim synthetic
cases but stopped on a truncated read-only allowlist query before its full
static seal. #15 stopped on a document patch context mismatch after partial
isolated edits. Neither stopped batch is promoted into a completed source
seal or native package result. The real Mac and Windows installers remain
unverified by this candidate.

The [fourteenth](https://github.com/surpaimb/cpa-cloud/actions/runs/44811694164)
also produced no installer. macOS passed the shim step, then notice collection
stopped at Cargo package lacks full license text: jni-sys-macros@0.4.1.
Windows passed the synthetic shim tests but real reconciliation refused at
stage=byte_compare reason=shim_bytes_mismatch mutation_possible=false after
graph, shape, and shim-key checks. No shim unlink began. The normalized byte
difference remains unknown; the isolated diagnostic classifier only labels
already-read unequal bytes and never changes the comparison or refusal.

The jni-sys-macros@0.4.1 official crate archive is 3301 bytes with SHA-256
38c0b942f458fe50cdac086d2f946512305e5631e720728f2a61aabcd47a6264.
Its .cargo_vcs_info.json binds commit
64d77b7a5f119d7b55b4e2c169a4668067ff59e6 and path jni-sys-macros;
the Cargo metadata declares MIT OR Apache-2.0 and the jni-rs/jni-sys repository.
The two full-text fallback files come from the repository root at that fixed
commit: LICENSE-MIT Git blob 2008402b8a1299cf118ea38e02116bfcc72e53c3
(1071 bytes) and LICENSE-APACHE Git blob
8f71f43fee3f78649d238238cbde51e6d7055c82 (11358 bytes).
Each package notice and readback requires both pinned sources; the crate's
license label alone is not treated as full text. This isolated candidate and
its local tests do not establish a successful native macOS or Windows package.
The earlier isolated Windows diagnostic and evidence batches stopped on tool
or verifier failures; their partial results are not accepted as package CI.

The [fifteenth natural package run](https://github.com/surpaimb/cpa-cloud/actions/runs/44821766043)
again produced no installers. macOS notice collection refused because
`libappindicator-sys@0.9.0` lacked a complete license text in its crate.
Windows reached real generated-shim byte comparison and refused before unlink:
`stage=byte_compare reason=shim_bytes_mismatch mutation_possible=false
byte_difference=extra_root_spelling_only`. This fixed category detects an exact
additional Git Bash root spelling in already-read unequal normalized bytes; it
is diagnostic evidence, not a successful full comparison or a disclosure of
the runner shim contents. The native jobs failed and publication was skipped.

The [official libappindicator-sys archive](https://static.crates.io/crates/libappindicator-sys/libappindicator-sys-0.9.0.crate)
is 7,370 bytes with locked SHA-256
`6e9ec52138abedcc58dc17a7c6c0c00a2bdb4f3427c7f63fa97fd0d859155caf`.
Its five members are ordinary files, with no full license text; its VCS record
binds [`tauri-apps/libappindicator-rs` commit `eafd1e3682a1247f595410266091e9684021cb6f`](https://github.com/tauri-apps/libappindicator-rs/commit/eafd1e3682a1247f595410266091e9684021cb6f)
and `sys` path. Cargo declares `Apache-2.0 OR MIT` but does not provide a
repository field; only this exact archive, label, missing repository, VCS,
path, and regular-member inventory may use the fallback. The repository root
at that commit provides the complete [MIT license](https://github.com/tauri-apps/libappindicator-rs/blob/eafd1e3682a1247f595410266091e9684021cb6f/LICENSE-MIT)
with qDot and Tauri copyright notices, and the complete
[Apache license](https://github.com/tauri-apps/libappindicator-rs/blob/eafd1e3682a1247f595410266091e9684021cb6f/LICENSE-APACHE).
Both are pinned byte for byte and individually required in collection and
installed-notice readback. A local source-only check cannot replace a later
natural macOS and Windows packaging and installer inspection run.

The packages deliberately do not enable `apply_configuration`. Installation
and static package inspection do not prove that an employee can apply settings,
that the app launches on every machine, or that it is production ready.

The [sixteenth natural package run](https://github.com/surpaimb/cpa-cloud/actions/runs/44826423267)
also failed before publication. macOS notice collection refused locked
`ndk@0.9.0` because its archive omitted complete license text. Windows
refused at `stage=byte_compare reason=root_normalization` before unlinking a
generated pnpm shim; that diagnostic did not disclose the runner's actual
bytes or path. Both jobs failed and produced no installers. A private pinned
Node 24.11.0 / pnpm 11.25.0 Git Bash reconstruction found exact derived
`/proc/cygdrive/<drive>/<workspace-root>` segments in fixed generated
`NODE_PATH` assignments on both sides. The candidate parses only those
fixed shell/PowerShell assignment forms and three ordered root-derived path
segments before the unchanged full-byte comparison. Generic colon-prefixed
fields, wrong drives, URLs, and near-root spellings still refuse. The
sixteenth runner's raw shim bytes are unavailable, so this local result does
not prove that run's root cause or establish a native package.
The [same-head Code validation run](https://github.com/surpaimb/cpa-cloud/actions/runs/44826423198)
also failed inherited Go offline dependency checks and two server self-logout
race jobs reporting `SLOG21_UNEXPECTED_SNAPSHOT_QUERY`. These server failures
remain open and are outside the desktop package inspection gate.

The locked [ndk](https://static.crates.io/crates/ndk/ndk-0.9.0.crate),
[ndk-sys](https://static.crates.io/crates/ndk-sys/ndk-sys-0.6.0+11769913.crate),
and [ndk-context](https://static.crates.io/crates/ndk-context/ndk-context-0.1.1.crate)
archives identify fixed VCS commits and paths, but lack full license files.
Each fallback requires its own exact lock checksum, archive size and member
count, Cargo label/repository, and VCS path. Both complete MIT and Apache
texts are byte-pinned to the fixed upstream sources, carried once, and
individually checked for each package. The ndk-context metadata uses its
older repository spelling; its source URLs use today's rust-mobile location.

Sixteen locked `objc2` family archives likewise lack complete authorization
text. Each fallback pins its own checksum, size, member count, VCS commit and
path, repository, and published license label. The fixed
[upstream declaration](https://github.com/madsmtm/objc2/blob/d7d2fa23ceaa5e6096c923b081040e5d81b3b9df/LICENSE.md)
distinguishes MIT-only crates from crates that offer MIT among three options;
the declaration is not itself the full license. The separate complete
[SPDX MIT template](https://github.com/spdx/license-list-data/blob/ab9b0904b43db6f4b67431efd8821feb821f1c8b/text/MIT.txt)
is carried unchanged, including literal `<year>` and `<copyright holders>`
placeholders. It is not an upstream filled-in copyright notice. Two fixed
upstream README variants are also carried as attribution context, retaining
the project's acknowledgements and license pointer. The upstream workspace
author field and acknowledgements do not identify a copyright holder for
that template. The notice records distinguish standard terms, upstream
declaration, and README context; Apple SDK derivative terms remain in the
unmodified upstream declaration.

Locked `r-efi@5.3.0` and `r-efi@6.0.0` have different `AUTHORS` files.
Each archive's `AUTHORS` is byte-identical to its own fixed VCS source and
contains complete MIT terms with copyright and author notices. The candidate
carries the two files separately and selects MIT from each published
`MIT OR Apache-2.0 OR LGPL-2.1-or-later` label. It does not claim to ship
complete Apache or LGPL terms for those unselected options.

The two locked winapi GNU import-library archives have no
`.cargo_vcs_info.json` or full license file. Their exact official archive
checksums, sizes, regular member counts, original Cargo manifests,
repository, and `MIT/Apache-2.0` labels are bound by an explicit legacy
archive branch. A
[later source tree](https://github.com/retep998/winapi-rs/commit/9497609ef44cc9bcd16cd2411c0ee6ccaf5483aa)
matches their non-generated source members; it is not claimed as either
archive's publication VCS. An even
[later commit](https://github.com/retep998/winapi-rs/commit/fa4e2c5a6dc495c71e6031bdde2e52870ebf6fdf)
added complete MIT and Apache texts to both import-library directories.
The candidate carries those two later texts with exact Git blob, length,
and SHA-256 identities and records that chronology without inventing an
archive VCS record.
