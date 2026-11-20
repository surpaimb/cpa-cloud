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
ten plain-text source supplements in
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

The packages deliberately do not enable `apply_configuration`. Installation
and static package inspection do not prove that an employee can apply settings,
that the app launches on every machine, or that it is production ready.
