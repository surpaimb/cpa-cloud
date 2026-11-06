# Administrator employee revision boundary

Status: new independently authored bounded batch, 2026-10-22. Draft / real
migration and production NO-GO. This contract and its tests-first matrix precede
the product edit; required gates below are not preclaimed test results.

## Exact source and ownership

Base is PR122 remote `38ab4bac525f11cdc5cc1fd7219d6d49ad2194d9`, itself based
on PR98 `d96e99ebef5942db1b585ff0042efc0872c67eca`. Equivalent local base is
`15908b1a01f7250d41cf96cb3f66fea56f6e715a`; different commit metadata/parents
are retained, not called identical. Both candidate trees are
`eeb2773a1c282ff1ac7c238420d36686321c4f7d`.

Only three files are authorized:

- `internal/service/employees.go`: split the two compound guards in
  `updateEmployee` and `updateModelPolicy`, nothing else;
- new `internal/service/admin_employee_revision_boundary_test.go`;
- this new contract.

Decoder, request types, routes, authorization, other mutators including PR122's
`revokeKey`, transaction/CAS/notification behavior, old tests/fixtures, DDL,
dependencies and workflows are frozen. A fourth file, unexpected read/tool/test
failure or natural CI red stops the batch, without hot repair or manual rerun.
One all-green candidate may be committed once and delivered as a new stacked
Draft PR. No old PR or old body is changed.

Sources are this authorization and CPA Cloud's own [preview API](preview-contract.md),
[lifecycle revision precedent](account-lifecycle-management-contract.md),
[self-state contract](employee-self-service-foundation-contract.md) and
[fixture isolation history](admin-key-revocation-audit-contract.md). No protocol
or dependency is added. Source/tests are independently authored, not copied
from archived CPA/CLIProxyAPI/Sub2API/reference trees; prior exposure means no
strict clean-room claim.

## Fixed HTTP change, unchanged precedence

Real `PATCH /admin/api/v1/employees/{id}` and
`PUT /admin/api/v1/employees/{id}/model-policy` keep the existing administrator
session -> required Origin -> CSRF -> handler chain. Employee/API Key or self
credentials do not authenticate management requests.

1. Failed `decodeJSON` retains exactly its existing 400 `invalid_request`
   envelope and fixed `Invalid request.` message; do not write a second error.
2. Successfully decoded `expected_revision < 1` (missing, null, zero or negative,
   including top-level JSON null and `{}`) returns HTTP 400 `invalid_revision`,
   fixed `A valid expected_revision is required.` message. This follows the
   adjacent existing `account_lifecycle.go` `updateModel` semantics.
3. Positive revisions keep existing behavior: stale CAS 409, absent employee
   404, no employee changes/invalid status or model policy 400, actual confirmed
   successful mutation 200 and actual notification. No transaction, lock,
   notification, persistence or authorization policy is redesigned.

Rejection is one exact JSON `{error:{code,message}}` envelope, JSON content type,
`Cache-Control: no-store`, no Location, no SQL/error/credential echo. With an
effective, unexpired administrator session, all typed SQLite table facts,
including employees, policy, Key/slot/self state and retained audit, and the
actual notification epoch/change context remain unchanged. This does not call
the existing expired-admin-session deletion a regression.

Do not broaden this batch to duplicate fields, JSON field casing, Content-Type,
query parameters or the maximum positive revision policy.

## Frozen tests-first and acceptance matrix

The new old-source oracle uses both real authenticated HTTP routes and real
SQLite. Each route has exactly six cases: omitted, null, zero, negative revision,
top-level null and empty object. PATCH otherwise attempts `status=disabled`;
PUT otherwise attempts valid `all/[]` from existing `selected/[erb-model]`, so
both are meaningful attempted changes. Before calling an old result expected,
the fixture/auth/unchanged facts/live notification checks must pass and the
response must be **exactly HTTP 200 with empty body**. The sole allowed old-state
red is `ADMIN_EMPLOYEE_INVALID_REVISION_OLD_EMPTY_200`, once per case. A different
status, fixture/transport/header/fact failure or any other red is unexpected and
stops; no product edit follows it. This is the inherited empty-response defect,
not proof of unauthorized mutation.

The new fixture fixes one actual `openStore` DB in a fresh App before publishing
HTTP. It uses real Initialize/secrets/login/requireAdmin/requestMiddleware and
real ServeMux PathValue; no App.Open/App.Handler, full runtime constructor,
background worker or DB replacement/restoration. A real non-nil
`accountPoolRuntime` has owned runtime/change contexts and empty lease maps;
actual NotifyChanged/Close are used. Confirmed valid HTTP controls must cancel
the old change context, increment the epoch and leave a new live context, so
negative notification assertions cannot pass merely because the runtime is nil.
Cleanup closes HTTP and joins handlers before closing notification contexts and
the fixed DB; partial construction is also owned and closed.

Fixture-only SQL seeds two employees, existing policy/model metadata, ordinary
and slotted Keys, an armed/cancelled slot, registered self state and outstanding
enrollment/session metadata, and one retained audit event. These are controlled
test records, not upstream decryption, Key-issuance or self-login acceptance.
Only administrator login is exercised for authentication. No real credentials,
provider, production database, backup or fixed port 8787 is involved.

Required post-fix tests cover the twelve invalid-revision cases, exact rejection
envelopes, malformed/empty/unknown/trailing/oversize/type/fraction/overflow JSON,
session/Origin/CSRF priority, valid 200 state changes and effective notification,
stale 409/target 404, no-change/invalid-status/mode/model-policy controls. Every
rejection compares all typed logical tables privately, never dumping values.
Successful controls permit only their documented employee/policy/self table
changes; Key/slot/audit/other-employee facts are checked unchanged.

Windows SQLite tests are serial/non-cached (`-p=1 -parallel=1`). Run only the new
matrix and relevant employee/Key/self/audit regressions, appropriate selected
local race where available, formatting, vet/build, local links and exact diff.
No full-repository parallel Windows SQLite run. Ordinary package CI output is
not a remote per-case trace. Natural pull_request CI belongs to the exact new
head; skipped is not execution. Root arranges original E/F same-head review.

## Preserved failure and production boundaries

Earlier independent preparation batches stopped on the wrong
`docs/feature-parity.md` path, output truncation, an extra in-memory blob
recomputation failure (single cause UNKNOWN), and nonexistent
`internal/service/auth.go`. None wrote product/test files or ran process tests.
Their incomplete/stopped conclusions are not rewritten as COMPLETE. Original
E/F's later independent read-only material-admission narrow GO explicitly
authorizes this new batch to reuse the 11-file/58-window/1591-line material;
it is not new product review or a reproduction of those tool failures.

PR122's old P2/NO-GO and later exact-head source-only narrow GO/green CI remain
historical, not this batch's acceptance. Old Code513/514/515 reds, PR121/T freeze,
UNKNOWN/CAPACITY_UNPROVEN, GOV-02/old-backup revival, real M1-to-M2, employee C,
all-consumer and production gates remain open. No Ready/merge/tag/package/deploy.

## New independent fixture-only correction and positive-control batch

This is a separately authorized fixture-only batch, not a continuation or hot
repair of the failed product-oracle batch. The preceding initial candidate
description remains historical. Its byte SHA256 values were contract
`442fb906e25e7794a1ccc00a96cd49118b5275cce30a80e26cf95b8b9b48a99c`
and test `9a92df3f3541347b0b99d85a7e77a5740d47ba050324a5b29d0bd287a1e73f12`.

The first failed old-oracle execution had Go exit1, one failed parent, zero
children and zero expected-red labels. Its LF-joined output hash was
`752c46c8434b1deb68648f2459df993fa4c21be163e0135d52b76d61ca263467`;
raw output was not preserved, and its actual first cause remains **UNKNOWN**.
Do not retroactively call it a SQL-error observation or expected product red.

A later, independent evidence-only execution preserved complete raw stdout and
stderr before parsing under the read-only evidence directory
`C:\Users\apple\.codex\erb-diagnostic-20261022-evidence-01`. Its stdout was
2094 bytes, SHA256
`40e4bd0c917a2e31ef530f76d3b18a916a3a65573627036fe5acd3af1d82ddbd`.
That new execution reported `AKRA_FIXTURE_SQL`, followed by TempDir deletion
failure with the DB occupied. Those are facts of that independent run only.
The evidence directory is not modified or reused here.

Original E/F/D independently identified a definite static fixture gap: merely
calling Initialize/openStore did not perform `migrateSelfService`, yet the
fixture inserted self credential/session rows before any test subcase. It also
registered resource cleanup before TempDir, which reverses the desired LIFO
release order. These static defects explain why the fixture needed correction;
they do not reconstruct the unavailable first run's raw error or establish the
underlying SQL error text of a fixed-label failure.

This batch may change only this contract and the new revision-boundary test.
The product `employees.go` remains exact blob
`1090d82b5be3bb3c28df4c04eb284b07d48f7d94`; all PR122 source, old fixtures,
DDL, dependencies, workflows, PR/body/monitor and commits remain frozen.
There is no commit, push, new PR or product revision fix in this batch.

### New fixture ownership and migration invariants

- Call `t.TempDir` first, then register resource cleanup, so LIFO closes all
  resources before deleting that directory.
- Once real openStore succeeds, immediately assign the store as the unique
  cleanup owner and retain one borrowed fixed DB pointer. Call the actual
  `s.migrateSelfService(t.Context())` and require success before any self seed;
  do not substitute DDL or a handwritten self migration.
- Build the fresh App and its actual valid accountPool notification runtime
  completely before handing ownership to App.Close or publishing HTTP. During
  earlier failures, the store alone owns close. Never hot-rebind the App DB.
- Cleanup blocks new HTTP work, closes HTTP and joins handlers, then uses the
  one selected owner: complete App.Close owns runtime/contexts then DB; partial
  construction uses store.close. Report close/context errors without skipping
  subsequent checks. Guard optional state; no nil panic or unconditional partial
  `changeSnapshot`. Idempotence prevents a second owner close.
- Limited controls deliberately return after owned-store construction, after
  complete runtime construction, or after full HTTP/login construction. After
  the child cleanup completes, assert exactly one owner close, closed DB and
  TempDir removal. These are controlled construction checkpoints, **not actual
  migration/close-error injection**, filesystem-failure proof or exhaustive
  resource-lifecycle coverage.

The new seed includes a third, registered non-target peer with a self session,
in addition to the original pending-enrollment non-target employee. Thus
non-target credential and session preservation both cover real populated rows.
These remain synthetic owned metadata, not self-login/provider acceptance.

### Strict success comparison, not a table-wide whitelist

Both valid-revision HTTP controls must produce the existing 200 and actual
notification epoch/context change, and the target's status/mode/revision must
be read back from SQLite. PATCH may change only the target status/revision and
remove that target's self rows. PUT may change only target mode/revision and
the target's model rows; the expected empty target policy is read back.

Every other employee row is compared in full, every target employee column
except the two explicitly permitted columns is compared in full, and every
non-target self/policy row retains all typed fields. All other tables, including
Keys, slots and retained audit, are compared without exclusions. There is no
whole-table employee or self-state whitelist. Rejection cases still compare the
entire logical database and actual notification snapshot unchanged.

### This batch's frozen validation boundary

Only one serial/non-cached Windows SQLite invocation is permitted, selecting
`TestAdminEmployeeRevisionBoundaryFixtureOwnership`,
`TestAdminEmployeeRevisionBoundaryDecodeAndAdmission` and
`TestAdminEmployeeRevisionBoundaryPositiveAndCAS`, with `-count=1 -p=1
-parallel=1 -failfast -timeout=5m -json`. Freeze both candidate byte hashes and
the command before execution. Capture raw stdout/stderr into a new repository-
external evidence directory; durably seal exit/time/command/environment and
file byte counts/hashes before deriving any summary.

Do **not** run `TestAdminEmployeeRevisionBoundaryInvalidRevision` or its twelve
old-red cases, other regressions, whole-repository tests or CI. A first migration,
fixture, cleanup, panic, positive-control or tool anomaly stops without repair
or another run. The outcome can at most be `FIXTURE_CANDIDATE_VALIDATED_ONLY`;
it neither repairs the product nor establishes its missing/null/zero/negative
revision behavior. Root must independently review this fixture candidate before
authorizing a new old-oracle or product batch. All existing NO-GO boundaries
above remain unchanged.

## New independent product-guard correction batch

This separately authorized product batch follows independent root/E/F admission
review. The earlier fixture-only/no-oracle/no-product authorizations above remain
historical boundaries of those completed batches, not permission retroactively
to continue their failures. The current batch may modify only the two guards in
`employees.go`, this contract and, only if needed, the new revision-boundary test.
It does not alter any PR122 source/old test outside those two guards or any old
PR/body/monitor. Existing complete material reads are reused under the explicit
independent E/F admission decision; prior read/tool failures are not erased.

Before the product edit, the fixed fixture candidate was independently validated
once: 3 parents + 68 children = 71/71 run/pass, zero failure/skip/parse errors.
Its raw stdout was 70283 bytes, SHA256
`319db0357e7a3f6aa599c28b2b88bd9b27bbd16ca4505e0574e27c43f6832c78`,
in `C:\Users\apple\.codex\erb-fixture-validation-20261022-evidence-01`.

The next independent old-product oracle then used the unchanged product and
frozen fixture exactly once, without failfast: 68 JSON lines, 1 parent + all 12
distinct named PATCH/PUT children ran/failed once, each child emitted exactly one
`ADMIN_EMPLOYEE_INVALID_REVISION_OLD_EMPTY_200`. There were zero pass/skip/parse
errors, unexpected AKRA/ERB labels, panic, timeout or cleanup errors; Go exit1
was the required old-product observation. Its complete raw stdout was 16037
bytes, SHA256
`40fd793acdccd1e19227a789a194a3efea4567e9f38696f17c50b8bba07d2c62`,
with empty stderr, under
`C:\Users\apple\.codex\erb-old-product-oracle-20261022-evidence-01`.
Both evidence directories are read-only historical inputs, not reused. This
does not reconstruct or establish the first failed run's UNKNOWN cause.

The product change is exactly the earlier fixed HTTP contract: decoder failure
returns without a second envelope; successful decode with revision below one
writes 400 `invalid_revision` and `A valid expected_revision is required.` before
any other field validation or database work. The remaining bytes of the file
must match the old file after substituting only those two compound guards.
Request types, decoder, routes/auth, other mutators/revokeKey, locks, Tx/CAS,
notifications, DDL, dependencies and workflows are unchanged. No duplicate-key,
case, Content-Type, query or maximum positive revision policy is added.

### Predeclared local gates and evidence

Freeze all three candidate byte hashes before execution. Existing Go1.26.9
Windows, `GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, readonly modules;
every SQLite invocation is non-cached and serial. A new repository-external
evidence root holds separate raw stdout/stderr and run/identity metadata for
each invocation, sealed before parsing. No prior green is substituted.

1. One full new matrix: `go test ./internal/service -run
   '^TestAdminEmployeeRevisionBoundary(InvalidRevision|FixtureOwnership|DecodeAndAdmission|PositiveAndCAS)$'
   -count=1 -p=1 -parallel=1 -failfast -timeout=5m -json`, CGO disabled. Expected
   4 parents + 80 children = 84/84 pass and zero old-red labels; actual output
   must be counted, not assumed.
2. One related regression invocation: `go test ./internal/service -run
   '^(TestAdminKeyRevocationAudit|TestPreviewWorkflowPersistenceStreamingAndRevocation|TestSelfKeySlot|TestSelfKeyRevoke)'
   -count=1 -p=1 -parallel=1 -failfast -timeout=10m -json`, CGO disabled.
   This keeps the existing preview employee lifecycle, self-Key/slot and audit
   tests; no old file is edited or whole-repository run added.
3. One selected local CGO race invocation, using the existing GCC, for the same
   four new matrix parents only: `go test ./internal/service -race -run
   '^TestAdminEmployeeRevisionBoundary(InvalidRevision|FixtureOwnership|DecodeAndAdmission|PositiveAndCAS)$'
   -count=1 -p=1 -parallel=1 -failfast -timeout=5m -json`. This is not a
   whole-application race or exhaustive concurrency/security proof.
4. `go mod verify`, `go vet ./internal/service`, and `go build
   ./internal/service ./cmd/cpa-cloud ./cmd/cpa-cloud-backup`, with offline
   readonly modules and CGO disabled; no dependency or license change.
5. gofmt check of the two candidate Go files, four local contract links, staged
   `git diff --check`, exact three-file manifest and exact two-guard substitution
   comparison. Staging does not authorize any additional source change.

Each gate runs once only, with no repair or rerun after an unexpected failure,
tool error, source/identity drift or scope expansion. If all gates pass, make
one local commit and freeze source. Local ancestry must be reported separately
from the remote PR122 ancestry. Equal candidate trees do not imply equal commit
SHAs/parents or a safe direct push. If exact remote stacking cannot be proved,
stop with parent/tree/three-blob/mode proof for separate root authorization of
an equivalent connector transfer; do not blindly push or change SSH trust,
credentials or remote configuration. No PR is created without the exact remote
base/parent proof. Natural CI and original E/F new-head formal review remain
separate root-coordinated acceptance, not local self-issued GO. Keep Draft /
real migration and production NO-GO and every earlier recorded red/UNKNOWN.
