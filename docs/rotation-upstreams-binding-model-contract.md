# Upstreams external-binding policy: pure synthetic model

2026-10-20. Draft / real source acceptance and production **NO-GO**.
Sole parent/stacked base is PR93
`80635d63fa227ff123de9be6053f716f0009ff5e`, based on PR92
`5e8533470b95420645c99d7e2390527c45d1cdb6`.

## Exact four-file scope and status

Only these four new files belong to this independent batch:

- `internal/upstreambindingmodel/model.go`
- `internal/upstreambindingmodel/model_test.go`
- `internal/upstreambindingmodel/PROVENANCE.md`
- this document, `docs/rotation-upstreams-binding-model-contract.md`

The PR93 [source-binding contract](rotation-upstreams-source-binding-contract.md)
and [case specification](rotation-upstreams-source-binding-cases.md), all PR92/
91/90 readers/models/tests/product source, schema, dependencies, workflows,
pins and old PR bodies remain unchanged. The older docs-only PR93's **zero
natural runs/checks means not triggered/not executed**, not green; this batch
does not rewrite that history. Parent review does not approve this new code.

This package is test-private: every model identifier/function/type is
**unexported**, it has **no imports** and no production package imports it.
`evaluateSynthetic` evaluates assumptions; it is not a real verifier, exported
receipt constructor, trusted-DB capability or migration activation interface.
No caller path, SQL, database bytes, string credential, file/network I/O, time,
environment, OS provider, randomness, hash/signature/crypto operation, callback,
service/helper/guest or runtime startup is accepted or invoked. Go compilation
and the ordinary test harness are separate tooling, not model runtime I/O.

## Distinct roles, closed public symbols

`evaluateSynthetic(O, B, C, A, JStart, JFinal, Completion)` returns either a
private `syntheticOnlyDecision` plus `SB_SYNTHETIC_ONLY`, or a fixed refusal
plus **nil**. There is no manifest, database handle, secret, cryptographic
receipt, M2 material, action dispatcher or authorization consumer output.

| Role type | Symbolic meaning, never verified real evidence |
| --- | --- |
| `candidateObservation` | Proposed scope/root/generation/watermark and assumed observed digest coverage, profile/columns, rows and category counts; a valid-old-canary tag is explicitly not proof |
| `expectedBaseline` | Independently handwritten expected scope/profile/inventory, allowed generation-to-root/version mappings and an external-origin **assumption** |
| `captureAssumption` | Assumed external capture tuple, approved collector/collection authority, offline method, quiescence, sealing and custody |
| `authorizationAssumption` | Assumed independently approved one-use purpose/audience and exact scope/profile/root/generation/watermark tuple |
| `checkpointAssumption` | Before/after assumed external current scope/baseline, allowed root-generation set, exact watermark, replay revision and authorization disposition |
| `completionAssumption` | Symbolic enumeration/cleanup/post-object/object-stability outcomes and start/late cancellation flags |

Shared scalar value structs do not merge these role types. There is no model
helper that turns `O` into `B`. Type separation **does not prove independent
origin**: a same-package caller can choose or coordinate every assumption.
`assumedExternal` is deliberately labelled an assumption, not `trusted=true`
or authentication. Unknown, candidate-derived, M1-derived and snapshot-local
origins fail closed; no real mechanism is supplied by selecting the enum.

All identities, digest/length/inventory/profile-descriptor/root-reference and
key-management-version values are **public integer symbols**, not real values
or byte representations. Symbols are nonzero, at most 4095. Policy version,
purpose and digest-algorithm tags have one closed model value; they implement
no wire format or hashing. Counters/generations/revisions are 1..1048576,
inventories at most 256 unique objects, permitted root-generation sets 1..16
unique generations. These are model bounds, not product/protocol size limits.

Four provider enum labels correspond to OpenAI-compatible, Anthropic, Gemini
and Codex; all four count categories including zeros are mandatory and must
match each inventory independently. The candidate and independent expected
inventory must then agree by object/provider, not order. Allowed generations
are explicit sets/mappings, not lower bounds. The frozen PR92 schema/source,
descriptor and fourteen unique column symbols are checked as **assumptions**;
fresh/rebuilt order is accepted, but no SQLite/schema/parser work occurs.
Credential `key_version` is not an input and cannot be treated as an M epoch.

Each role's `recordShape` requires the eight distinct closed authority-field
group tags and the `assumedUnambiguous` encoding tag. Missing, duplicate, extra
or unknown groups refuse. **This is a symbolic policy input**, not a parser,
canonical encoding or R24 wire-ambiguity detection implementation.

## Evaluation, failure and ownership boundary

The fixed guard order is: start-cancel; external-origin/collection-authority
assumptions; closed scope/mapping/shape bounds; initial checkpoint; collector/
capture authorization; exact candidate/capture/authorization vs baseline scope
and full-digest assumption; exact generation/mapping/root/watermark; offline
capture assumptions; frozen schema/columns; independent inventories; object
stability and completion assumptions; final checkpoint and unchanged watermark/
replay revision; late-cancel. Only then is a synthetic-only result allocated.

Checkpoint validation checks origin and known freshness/disposition assumptions,
closed fields/mappings, exact current baseline/scope and allowed set, nonzero
bounded floor/replay revisions, then unused authorization. Final stale scope,
floor or replay-revision changes refuse; an externally supplied used/expired/
revoked/superseded disposition refuses. No clock is read and no checkpoint is
fetched. A signature, collector identity, fresh checkpoint or replay store is
not actually authenticated.

Refusals are exactly `SB_UNKNOWN_TRUST`, `SB_BINDING`, `SB_SCHEMA`, `SB_CAPTURE`,
`SB_STALE`, `SB_REPLAY`, `SB_IO` or `SB_CANCELLED`. Codes have no interpolation;
the model emits no logs/errors with candidate values. Every failure, including
late failure, has nil output. An earlier guard failure is not overwritten by a
later cleanup/cancel assumption; these are policy precedence rules, not an
observation of concurrent real failures. No partial rows/counts/decision are
returned, cached, activated or handed to a consumer.

Inputs are read only; result scalar fields are copied by value and rows/counts
are independently copied. Input mutation cannot rewrite a prior result, result
mutation cannot rewrite inputs or another result, and caller ordering is not
an authority signal. The function has no mutable global state or side effects.
Evaluation assumes caller-owned inputs are not concurrently mutated; it is
not a concurrent coordinator, atomic publication or cross-process proof.

R27 I/O/cleanup outcomes and R28 cancellation are **only symbolic inputs**.
No real close, rollback, disk fault, cancellation context or SELECT-path failure
is injected. Existing PR92 driver-probe limitations and evidence remain distinct.

## Executed-policy tests versus required limitations

New-package tests independently author P01--P04 and R01--R30, with exact fixed
codes and nil for every refusal. R12 authenticated-coherent replacement vs
forged self-report, R20 expired/revoked/superseded/late expiry, and R30 old floor
vs current-used variants are named separately. Additional tests cover closed
fields for every role, finite bounds, duplicate/unknown inventory/profile/
mapping values, empty-table complete gates, reordered/fresh/rebuilt controls,
late failure precedence and ownership. The model imports neither the PR92
reader nor PR91 mapper and does not borrow their tests as runtime evidence.

Two explicit **limitation witnesses**, not security successes, are mandatory:

1. If candidate and **all supposedly external assumptions** are coherently
   replaced together, the model can return a synthetic-only decision. It has
   no trusted reference outside its inputs and cannot determine actual truth.
   Types/enum names must not be advertised as authenticating `B/C/A/J`.
2. Repeating identical unused checkpoints yields repeated synthetic-only
   decisions. The pure function cannot claim/consume an authorization, persist
   a replay fence or establish backup-external rollback protection. It rejects
   only when a supplied checkpoint assumption says used/stale/etc.; it does not
   make the real state become used or prove that supplied state is current.

**Unknown and still blocking real acceptance:** approved acquisition/custody,
complete external inventory, instance-to-M1 reference/version/generation mapping,
approval responsibility, trusted issuer and key lifecycle, signing/verification/
canonical encoding, latest revocation/freshness source, atomic claim-consume,
out-of-backup anti-rollback and real publication/receipt construction. No model
acceptance resolves any of them. There is no real source authentication, online
WAL, M2 generation/re-encryption, consumer completeness, Key reissue/retirement,
GOV-02 non-resurrection or production/serving approval.

## Validation, natural CI and stop

Run only this new package's targeted `go test`, `go vet`, `go build`, gofmt and
document link/diff/file-boundary checks locally. No parallel Windows SQLite
whole-repository suite, real DB/credential read/scan, service, helper or guest.
Report tests of policy assumptions separately from real parser/crypto/storage
verification. Exact counts and same-head CI belong in the new PR body.

Pre-edit inspection at exact PR93 found `internal/**` is outside Code's docs
ignore and is core in `scripts/ci-plan.py`; it also matches both offline CLI
and retained recovery VM allowlists. All three natural PR workflows are thus
expected for this four-file change, unlike docs-only PR93. Do not modify any
workflow/pin or force execution. Record actual exact head/base, event, attempt,
run terminal state and full check counts; skipped jobs are not execution and
inherited VM recovery is not new source-binding/authentication evidence.

One commit/push/new stacked Draft PR. First unexpected natural red freezes
source: read-only diagnosis and accurate evidence only, no patch/rerun/dispatch.
Root arranges original E/F formal review of the new exact head. No prior-head
GO is borrowed; no Ready/merge/tag/package/deploy. Production remains NO-GO.

## Sources, provenance and licenses

Independently authored from the approved task and frozen PR93 contracts, not
ported from other implementations. Public semantic references are the
[Go specification](https://go.dev/ref/spec), [Go testing](https://pkg.go.dev/testing)
and PR93's evidence/reference/freshness sources. No RFC, Go or third-party code
passage is extracted. Prior research means no strict clean-room claim.
New code/test and license boundaries are recorded in
[PROVENANCE](../internal/upstreambindingmodel/PROVENANCE.md).
No dependency, SDK, version, pin, license grant or NOTICE change; existing
dependencies retain their licenses. This is not a compliance assessment or
authorization to publish a package/deployment.
