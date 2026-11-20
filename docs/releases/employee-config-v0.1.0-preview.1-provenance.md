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
six plain-text source supplements in
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

The packages deliberately do not enable `apply_configuration`. Installation
and static package inspection do not prove that an employee can apply settings,
that the app launches on every machine, or that it is production ready.
