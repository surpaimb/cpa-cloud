# B2 pre-constructor admission: source-only contract

## Provenance and frozen starting point

Independently authored from this repository's functional specifications and
prospective B2 budget contract. No archived implementation, external execution
core, SDK, or reference repository is an implementation template. No dependency
is introduced; existing dependency licenses remain unchanged.

Starting point: PR118 `cbd8643910d44474a8f789edaa16a89babf7ddad`, with sole
parent PR117 `bf3877aa4856434ed80fe63f26229b06963515c2` and tree
`191c0d34d9ae1bae8ac5694b2762cdc44e414d2c`. PR118 and earlier PR bodies,
contracts, CI, and review histories are frozen.

Related contracts:

- [Prospective B2 budget](employee-self-logout-code513-b2-admission-budget-contract-2026-10-22.md)
- [A0 lifecycle evidence](employee-self-logout-code513-lifecycle-evidence-contract-2026-10-22.md)
- [Development preview](preview-contract.md)

## Sole permitted claim

`PRE_CONSTRUCTOR_ADMISSION_ONLY` means an independent, selected, test-only
metadata admission was recorded before one direct call of the original
`newSlog21` constructor. It is not a receipt from inside that constructor and
does not certify constructor entry, Initialize success, a B1 chain, App, Conn,
Tx, pass, worker, raw cut, physical release, root cause, or repair.

The two selected source scenarios are Normal `ignored-body-query` and Boundaries
`zero-rows`. They have new independent family and selection types. They are not
aliases for the old Auth/confirmation or owner selections. TwoAuthenticated is
excluded and byte-frozen. No attempt is made to generalize the old 12/17-pass
observations, or to claim a lifecycle capacity; full capacity remains
`CAPACITY_UNPROVEN`.

## Exact five-file budget

1. This new contract.
2. New `internal/service/self_logout_constructor_admission_20261022_test.go`.
3. Two same-line call substitutions in the original self-logout fixture test.
4. One strict inverse-projection shim in the Init-origin caller test.
5. One strict inverse-projection shim in the B1 entry-relay test.

No regular Go, owner test, Gate4, dependency, workflow, old documentation, state
constant, hash constant, or numeric snapshot anchor is changed. The original
constructor signature/body, original Initialize bytes and error handling,
Auth/confirmation forwarding, cleanup order, all 22 actual snapshot lines, and
all 54 raw literals remain frozen. No line directives or anchor relocation.

## Real source bridge

Only the original Normal and Boundaries call expressions use the new test-only
bridge. It records admission first, then unconditionally makes exactly one
direct original constructor call, returning that original fixture unchanged.
No callback, fake constructor, local shadow, substitute fixture, duplicate
constructor, or case-body replacement is allowed.

Normal forwards the original empty variadic owner selection. Boundaries forwards
the exact existing `firstOwnerBoundarySelection(scenario)` expression unchanged.
For the new selected scenarios the legacy owner, Init-origin and B1 chain remain
unselected. Other Normal/Boundaries scenarios retain their old owner behavior;
unselected admission is zero event and zero new cleanup report.

## Independent bounded admission metadata

Each selected choice has its own plan and canonical, non-zero-size token.
Family and sample are fixed before admission. A plan admits only its canonical
token and exact family/sample pair, once, before its admission window is closed.
A copied token, foreign plan/token, wrong family, third/unknown selection,
duplicate, late call, unknown phase or exhausted one-fact slot is rejected.
Rejection is latched; the first fact is never overwritten, erased or promoted.
Views contain values only. Zero metadata is not positive evidence.

Closing this metadata window still occurs before the original constructor call;
it must not be described as constructor entry. Admission does not inspect a
context, error, DB, driver, stack, test name, path, address or old diagnostic log.
It does not activate a legacy chain or route a capability into the product.
There is no global registry. A copied canonical pointer does not grant a second
admission: the same plan still rejects duplicate use.

The selected report is registered before the old constructor's cleanup, so old
cleanup executes first. If an old primary failure exists, the new report emits
only a fixed prior-failure/no-GO label; it cannot replace that primary failure
or turn it into success. Metadata rejection never skips the original call.
Unselected preparation returns before registering cleanup or touching testing
helpers. New metadata and scheduling can perturb execution; no zero-effect or
root-cause exoneration claim is made.

## Exact inverse projection, not guard refresh

The fixture projection accepts only the exact frozen fixture or its exact two
prescribed call substitutions. Reversing those substitutions must restore Git
blob `f6240407202ea487efdc68cb6034c7d970627619` byte for byte (LF source).
The new top oracle additionally requires both real wiring sites: reverting both
to baseline cannot make the new oracle green.

Only the exact newly inserted shims may be removed from the two edited guard
tests. Their restored blobs must be Init test
`84d470203f9f5cbcadef2d96bc49b49ec9efbd17` and B1 test
`2ee46df3d7031605bb95c6ddbb7c1a80b2d1c1f0`. Old audits then run unchanged.
Gate4 is applied to the actual new fixture AST, not the inverse-projected AST.
Old mutation/state oracles and frozen owner/Gate4 bytes cannot be weakened by
projection or a refreshed expected hash.

## Prospective local verification envelope

First create a compilable source-only expected red with fixed label
`B2_ADMISSION_SOURCE_MISSING`, before adding the bridge. No old case runs for
this red. Final directed execution uses only newly named
`TestSelfLogoutConstructorAdmission20261022*` tests, serial and non-cached.

Pure controls cover exact mapping, default/unselected, third/unknown/wrong
family, foreign/copied tokens, duplicate/late admission, immutable value facts,
original parameter/context/error/cleanup preservation, and strict projections.
In-memory source mutants cover missing/late admission, helper-only counts,
shadow/substitute/duplicate constructor and old-source boundary edits.

Exactly two serial real original constructor executions are permitted: one
selected Normal bridge and one selected Boundaries bridge, without invoking
old case bodies, snapshots, Auth/confirmation requests, old race tests or the
full red suite. Each original lifetime and its cleanup must finish before the
next. These constructors use controlled synthetic SQLite/App/HTTP fixtures;
they are not DB-free metadata tests. Their only bridge evidence is
`B2_REAL_PRE_CONSTRUCTOR_BRIDGE_CONFIRMED` and the admission-only report.

Directed build/vet, gofmt, local links, diff, five-file scope, actual anchors,
raw literals, and byte restoration are separate checks. Coverage is not a
security-completeness claim. No local race or manual CI rerun is authorized.

The first unexpected local or natural CI red freezes this batch, with evidence
preserved: no hot patch, retry, rerun or dispatch. If formatting, projection or
Gate4 needs a wider edit or anchor movement, stop before commit.

## Preserved edit failure and independent continuation

The first source-only tests-first execution compiled and ran exactly one new
test. It produced only the expected `B2_ADMISSION_SOURCE_MISSING` red, with empty
stderr; stdout SHA256 was
`f3098e4a059d5bd49dc10d4fa5847cd017518d429d597fd34350e293f549fd1f`.
No original case ran. This expected red is retained, not rerun or erased.

The next patch was rejected before application because one patch attempted
Delete File and Add File for the same new test path (`multiple operations
target`). This was an editing-tool failure, not a product test or natural CI
failure. The batch stopped without a retry, commit, push or PR. Readback found
only this new contract and the 19-line tests-first file; the three old file
blobs and PR118 HEAD were unchanged.

The root then independently re-pinned PR118 and authorized a separate bounded
continuation in the same worktree. This continuation uses only in-place Update
File edits of the existing new test, preserving the failure evidence here and
in the prospective PR body. It must not repeat the known tests-first red.
After each edit it reads back the file and verifies the five-file boundary and
the old source blobs or their exact prescribed projections. Another rejected
edit, uncertain content, wider scope, or the first unexpected local/natural CI
red stops the continuation without hot patching or manual rerun.

That continuation successfully applied the in-place new test and the exact
three old-test seams. Its next read-only verification request was refused with
`JavaScript execution exceeds the 64000-byte strict auto-review limit` before
execution. This was a request-size limit, not an editing, product-test or CI
red. It stopped with exactly five uncommitted whitelist files; no final test,
gofmt, build/vet, commit, push or new PR had run. The Git diff already showed
only Init +4, B1 +4 and the two same-line fixture substitutions.

A third independently authorized continuation retains both refusals and the
original expected red. It verifies each file/boundary in small read-only
requests, each strictly below 64KB, returning only targeted facts or digests.
It does not reapply the seams or rerun tests-first. A further refusal is reported
with its request size and original refusal; it is not retried as a large call.

## Executed local ledger for the third continuation

Windows Go1.26.9, one non-cached serial directed invocation of only
`TestSelfLogoutConstructorAdmission20261022*`, with `-p=1 -parallel=1 -failfast`:
5 parents + 67 children = 72 distinct names, each run/pass exactly once,
0 failure/skip/incomplete/unexpected name, exit 0, stderr empty. Package elapsed
6.798 seconds. Stdout SHA256:
`e053993fdd119401bb971bfa241b7ec2e3158e7cb705af71df23fc4acb65331c`.

The 67 children comprise 29 in-memory source mutants, 18 exact/default mapping
cases, 18 metadata positive/rejection cases and 2 real bridge cases. Additional
invalid-pair, value-copy, close-without-record, duplicate-close and zero/default
allocation checks execute inside the new parents. The actual fixture passes
the unchanged Gate4 audit. Old Init/owner/B1 audit helpers execute through the
strict new projections; exact whole-file restoration preserves all old hash,
state and mutation-control bodies. This is not a rerun of the old test cases or
their complete dynamic test matrices.

Exactly two real original constructor lifetimes completed serially, each old
cleanup finishing before the next child's run. Two
`B2_REAL_PRE_CONSTRUCTOR_BRIDGE_CONFIRMED` and two
`PRE_CONSTRUCTOR_ADMISSION_ONLY` reports were recorded. Old owner/Init/B1 plans
remained nil for both targets; no legacy admission report was observed. This
does not certify constructor internals, Initialize, B1 receipt or physical
resource release. No original self-logout case body, local race or full-suite
execution was performed.

Directed service build/vet both exit 0 with empty stderr. Gofmt leaves the new
test unchanged; old three edited blobs remain exactly their prescribed seams.
Diff checks and 3 relative document links pass. Inverse fixture blob is exactly
`f6240407202ea487efdc68cb6034c7d970627619`; all 22 actual snapshot lines and the
54 raw-literal sequence are identical. Entire Init/B1 test inverses restore
their pinned blobs. Owner, Gate4, regular Go, dependencies, workflows,
TwoAuthenticated and previous contracts remain unchanged. No new coverage,
zero-effect, diagnosis, repair or capacity-completeness result is claimed.

## Prospective publication boundary

If all authorized local checks pass, one commit/push and one new stacked Draft
PR are allowed. Natural CI is recorded at the actual new remote head, with
skipped/untriggered checks distinguished from execution. CI terminal handling
and original E/F independent exact-head review belong to the root coordinator.
No source-only test or green CI independently issues GO.

PR115 Code513's two natural reds and PR117 Code514's TwoAuthenticated red stay
open evidence. This batch neither reproduces old races nor repairs or diagnoses
them. All older static/dynamic failures, including GOV-02 limitations, remain
unchanged. No shared root cause is inferred.

Draft and dynamic/product/real migration/production NO-GO remain. No Ready,
merge, tag, package, deployment, service, browser, real credentials, DB, backup,
Key/config/session, password, official client, root switch or M1-to-M2 action.
No other PR, attachment, monitor or main worktree WIP is changed.
