# Third-party notices and provenance

Status: third-party runtime, build, and test dependencies are present. This file is an inventory and release checklist; it is not legal advice, a warranty, or a complete substitute for the exact license texts shipped by each dependency.

The evidence review dated 2026-09-22 is recorded in `docs/research/dependency-notices.md`. Versions below come from `go.mod` and `web/bun.lock`; license identifiers were checked against the locally installed module/package license files and metadata.

## Material included in runtime distributions

### Go server

The server source directly imports `golang.org/x/crypto` and `modernc.org/sqlite`. Go dependency code selected by a target build is compiled into the server binary. A binary distribution must reproduce the applicable copyright notices, license conditions, and disclaimers in its documentation or other accompanying material; the final selected graph still has to be verified per target.

| Component | Version | Source | License | Included material / required notice |
| --- | --- | --- | --- | --- |
| `golang.org/x/crypto` | `v0.42.0` | https://pkg.go.dev/golang.org/x/crypto@v0.42.0 | BSD-3-Clause | Compiled code; include the module `LICENSE` |
| `modernc.org/sqlite` | `v1.38.2` | https://pkg.go.dev/modernc.org/sqlite@v1.38.2 | BSD-3-Clause; SQLite public-domain statement | Compiled code; include `LICENSE` and `SQLITE-LICENSE` |
| `github.com/dustin/go-humanize` | `v1.0.1` | https://pkg.go.dev/github.com/dustin/go-humanize@v1.0.1 | MIT | Indirect module; if linked, include `LICENSE` |
| `github.com/google/uuid` | `v1.6.0` | https://pkg.go.dev/github.com/google/uuid@v1.6.0 | BSD-3-Clause | Indirect module; if linked, include `LICENSE` |
| `github.com/mattn/go-isatty` | `v0.0.20` | https://pkg.go.dev/github.com/mattn/go-isatty@v0.0.20 | MIT | Indirect module; if linked, include `LICENSE` |
| `github.com/ncruces/go-strftime` | `v0.1.9` | https://pkg.go.dev/github.com/ncruces/go-strftime@v0.1.9 | MIT | Indirect module; if linked, include `LICENSE` |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | https://pkg.go.dev/github.com/remyoudompheng/bigfft@v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | Indirect module; if linked, include `LICENSE` |
| `golang.org/x/exp` | `v0.0.0-20250620022241-b7579e27df2b` | https://pkg.go.dev/golang.org/x/exp@v0.0.0-20250620022241-b7579e27df2b | BSD-3-Clause | Indirect module; if linked, include `LICENSE` |
| `golang.org/x/sys` | `v0.36.0` | https://pkg.go.dev/golang.org/x/sys@v0.36.0 | BSD-3-Clause | Indirect module; if linked, include `LICENSE` |
| `modernc.org/libc` | `v1.66.3` | https://pkg.go.dev/modernc.org/libc@v1.66.3 | BSD-3-Clause plus embedded MIT/BSD-3-Clause material | Indirect module; if linked, include `LICENSE`, `LICENSE-GO`, and `honnef.co/go/netdb/LICENSE` when the corresponding platform code is present |
| `modernc.org/mathutil` | `v1.7.1` | https://pkg.go.dev/modernc.org/mathutil@v1.7.1 | BSD-3-Clause | Indirect module; if linked, include `LICENSE` and `mersenne/LICENSE` |
| `modernc.org/memory` | `v1.11.0` | https://pkg.go.dev/modernc.org/memory@v1.11.0 | BSD-3-Clause | Indirect module; if linked, include `LICENSE`, `LICENSE-GO`, and `LICENSE-MMAP-GO` |

The `modernc.org/memory` logo license and `modernc.org/libc` test-data license are not runtime notices unless those source assets are separately redistributed. Re-evaluate this statement for source or vendor distributions.

### Browser administration console

The production JavaScript bundle contains React, React DOM, and Scheduler code. The inspected minified bundle did not retain an `@license` or copyright marker, so their notices must accompany the distribution outside the bundle.

| Component | Version | Source | License | Included material / required notice |
| --- | --- | --- | --- | --- |
| `react` | `19.3.0` | https://registry.npmjs.org/react/19.3.0 | MIT | Bundled runtime code; include the package `LICENSE` |
| `react-dom` | `19.3.0` | https://registry.npmjs.org/react-dom/19.3.0 | MIT | Bundled runtime code; include the package `LICENSE` |
| `scheduler` | `0.28.0` | https://registry.npmjs.org/scheduler/0.28.0 | MIT | Bundled transitive runtime code; include the package `LICENSE` |

## Build and test dependencies

`web/bun.lock` contains 151 locked package entries. On the inspected Windows x64 installation, 107 locked package versions were materialized under `web/node_modules`; TypeScript also carries `vscode-jsonrpc@9.0.0` as vendored material. These dependencies are not expected in the server binary or static web output, except for the three browser runtime packages above.

If a release includes `node_modules`, a build container/toolchain, or source-vendor archives, it must also carry every applicable upstream license file. In particular:

- TypeScript `7.0.2` and its native package are Apache-2.0 and include both `LICENSE` and `NOTICE.txt`; retain both when redistributing them.
- `lightningcss` and its native package declare MPL-2.0. They are build-time material in the inspected installation, not code identified in the current static output. Redistributing the tool or a modified covered file requires an MPL-specific review.
- The installed dependency set also contains MIT, MIT-0, BSD-2-Clause, BSD-3-Clause, ISC, BlueOak-1.0.0, and CC0-1.0 material. Exact package/version groupings are in `docs/research/dependency-notices.md`.

## Release gate and unresolved evidence

This repository does not yet contain a generated, release-ready bundle of all full third-party license texts. Before publishing any binary, web bundle, installer, container, or source/vendor archive:

1. Resolve the final Go module graph with the release Go toolchain, and generate notices from the modules actually linked for each target OS/architecture. The review machine did not have `go` on `PATH`, so `go list -m all` and binary-level linkage were not verified.
2. Reinstall from the committed frontend lock for every supported target and inspect the 44 platform-specific optional packages not materialized on the review machine: 14 Rolldown bindings, 19 TypeScript native packages, 10 Lightning CSS native packages, and `fsevents@2.3.3`.
3. Obtain exact upstream license text for the six installed packages whose manifests declared a license but whose package directories lacked a license file: `@rolldown/binding-win32-x64-msvc@1.2.9`, `css.escape@1.5.1`, `is-potential-custom-element-name@1.0.1`, `punycode@2.3.1`, `saxes@6.0.0`, and `stackback@0.0.2`.
4. Build the final artifact, identify what it actually contains, copy the exact license/notice texts into an accompanying third-party-license bundle, and fail the release if any component has an unknown license or missing required notice.
5. Repeat the inventory whenever `go.mod`, `go.sum`, `web/package.json`, or `web/bun.lock` changes.

CLIProxyAPI, Sub2API, and CC Switch were discussed as product references. They are not dependencies listed by the current Go or frontend manifests, are not bundled by this notice inventory, and their names do not imply affiliation or endorsement.

This document records the evidence available for the pinned dependency set. It does not guarantee that no additional license, patent, trademark, export, or contractual obligation applies.
