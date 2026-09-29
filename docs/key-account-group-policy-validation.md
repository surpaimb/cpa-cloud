# Key account-group policy validation

This report records independent process-level validation of the development-preview key account-group policy against a fixed Windows stage binary. It supplements the [policy contract](key-account-group-policy-contract.md); it is not a production-readiness or provider-compatibility claim.

## Artifact and runner

- Stage binary SHA-256: `64c1f1c641a01de3d380c7e39a6acb7cd57fa88b9042eea97b8f2dffbc26a7d3`.
- Repository runner: `scripts/verify-key-account-groups.mjs`.
- The runner accepts an absolute executable path and the expected SHA-256, verifies the hash before launch, and uses only Node.js built-ins.
- The stage binary was built from a dirty development workspace whose then-current HEAD was `f46eea9c580324c741ff36a02d663f3e7a139172`. The evidence is therefore bound to the binary hash, not represented as an exact clean-commit build.

Run it with:

```powershell
node scripts/verify-key-account-groups.mjs <absolute-binary-path> 64c1f1c641a01de3d380c7e39a6acb7cd57fa88b9042eea97b8f2dffbc26a7d3
```

## Boundary

The runner creates a fresh random data directory, supplies a random administrator password over standard input, and uses random employee keys, upstream credentials, and prompt markers. It binds the service and two synthetic upstream implementations to dynamically assigned loopback ports and explicitly rejects port `8787`. The upstream fixtures model OpenAI-compatible Responses and native Gemini A/B pools; no real provider, membership account, CLI import, deployment, package, or publication is involved. The data directory is removed in a guarded `finally` block.

The runner checks service logs and persisted files for the generated password, employee keys, upstream credentials, and prompt marker. It does not print or retain those values. This document contains no runtime credentials or runtime data-directory contents.

## Two consecutive stage runs

The same fixed binary and hash-pinned runner completed twice with identical summaries:

```text
PASS key account-group fixed-binary verification sha256=64c1f1c641a01de3d380c7e39a6acb7cd57fa88b9042eea97b8f2dffbc26a7d3
PASS allowed=9 denied_zero_attempt=10 upstream_A=9 upstream_B=0 pool_revision=3
```

Both runs covered:

- feature discovery plus OpenAI-compatible and Gemini model-directory filtering;
- allowed selected-group routing across Chat Completions, Responses, Anthropic Messages, and Gemini `generateContent`;
- an existing cross-group selection and an empty selection failing with `503` before any upstream attempt;
- no fallback when the only permitted upstream is disabled while an out-of-policy upstream remains available;
- a queued request rechecking route-to-channel-to-group membership after a mapping revision, with no replay of the already-dispatched request;
- restart recovery of the saved policy, effective-model view, directories, and four protocol entry points;
- exactly one accounting attempt for each allowed request and zero attempts for each denied request;
- all nine upstream calls reaching group A and none reaching group B; and
- absence of generated plaintext credentials and prompt markers from captured logs and persisted files.

Node.js 22 emitted its expected experimental warning for `node:sqlite`; it did not affect either result.

## Exact tracked-source rerun

After the production integration commit, the same runner was executed against a fresh binary built from a clean temporary clone of tracked source commit `419035e964ab569af20380c1c6661e6fee70c717`. `go version -m` reported that exact `vcs.revision` and `vcs.modified=false`.

- Clean-source binary SHA-256: `bff4854b9b7cac7b2e957668a67431684b01418b2f1c8c7813cd77cf4e791c47`.
- The hash-pinned runner completed twice with the same `allowed=9 denied_zero_attempt=10 upstream_A=9 upstream_B=0 pool_revision=3` summary.
- The existing `smoke-key-policy.mjs` process test also passed its four-protocol denial, directory intersection, CAS, plaintext redaction, and restart checks.
- The existing `smoke-preview.mjs` process test passed initialization, web entry, permanent Key, synchronous/SSE execution, credential isolation, restart, and revocation persistence.
- A serial, uncached `go test -p 1 ./... -count=1 -timeout=20m` passed from the same clean clone (`internal/service` 672.630s), followed by a passing `go vet -p 1 ./...`.

All four runs used independent temporary data directories and dynamic non-8787 loopback ports. The binary and temporary clean clone are local validation artifacts, not release packages.

## Remaining acceptance work

The exact production-source process rerun is complete, but the later evidence-only documentation commit is not represented as a release binary. The final pull-request HEAD still requires its own CI result, including Linux race. Packaging, deployment, and real-provider or membership-flow validation remain separate responsibilities and are not authorized by this batch.
