# Windows local-test executable contract

Status: development-preview artifact only; API/CLI-only, no bundled web console.
This independently authored contract and its dedicated workflow are the only
changes. No production, migration, recovery, provider or security-readiness GO
is granted. Existing open findings and prior STOP evidence remain unchanged.

## Source boundary

The authorized remote baseline is PR127 head
`349afaf31a35e6446a357651f07e71d34d666f2a`, based on PR126
`cb5444e77a533345f2e617876a027cde69b1632f`.
Local authoring starts at `8719204607bbae03d767e62315764627fb58dcd9`.
These commits have the same frozen tree
`b5b5521f4e2f3f626db3b11876cc4564370143db`, not the same commit identity.
The original untracked createModel work is excluded. Remote equivalent-tree
transport and a new stacked Draft PR require separate authorization; no push,
SSH trust change, tag, Release, installer or deployment is authorized here.

## One natural build, one downloadable file

[The dedicated workflow](../.github/workflows/windows-local-test-exe.yml) accepts
only natural `pull_request` opened/synchronize/reopened events touching itself
or this contract. Only same-repository heads and attempt 1 are eligible. There
is no push, manual dispatch, reusable-workflow entry or manual rerun procedure.

One `windows-2025` job checks out and verifies the exact PR head, not the default
merge checkout. Actions are full-SHA pinned; token permissions are contents-read,
checkout does not persist credentials, and no secrets are used. Go 1.26.9 is
selected explicitly (the unchanged go.mod requires Go 1.26); the actual toolchain
is recorded. GOOS=windows, GOARCH=amd64 and CGO_ENABLED=0 are mandatory.
`GOFLAGS=-mod=readonly` prevents module-manifest changes, not runner-cache writes.
Build only `./cmd/cpa-cloud`, with trimpath and an explicit
`local-test-<source-head>` main.version. Incompatibility fails; do not add DLLs,
change dependencies or relax the build to obtain an artifact.

Before upload, verify PE32+/AMD64 headers, nonzero bounded size, SHA256, a successful
`--help` invocation and absence of its dedicated temporary data directory. This
is CLI/help evidence, not an HTTP, persistence, provider or full regression test.
Assert that the output directory contains exactly one regular, non-linked
`cpa-cloud.exe`. Upload that exact file through the repository-pinned
[upload-artifact v7](https://github.com/actions/upload-artifact), with
`archive: false`, `retention-days: 7` and missing-file failure. No checksum,
manifest, web directory or other artifact file is produced. Source head, size,
SHA256, toolchain and test-only limitations go to logs and Job Summary instead.

## Test boundary and validation

The executable is a service with command-line flags, not an installer or desktop
UI. [Web assets](../web/README.md) normally require a separate `--web-dir`;
this build neither builds nor embeds them. The independent backup CLI is absent.
The artifact is unsigned; do not disable Windows security protections to run it.
One distributed file does not mean stateless operation: later service tests need
a new private data directory and an isolated loopback port, not existing user
data, credentials or port 8787. This batch does not start that service locally.

[CI planning](../scripts/ci-plan.py) classifies these paths as core/web validation,
not windows/linux/macos package jobs. Existing workflows remain untouched;
actual natural runs and artifacts must be checked against the new remote head.
Local checks are file-scope/link/diff and strict YAML structure plus PowerShell
syntax review, not a claimed full YAML-parser or GitHub-runtime acceptance.
No local Go build/test, workflow dispatch or rerun belongs to this authoring batch.
