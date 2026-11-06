# Code512 coverage and a future driver-implementation origin

Status: new independent **docs-only** contract, 2026-10-22 (Asia/Shanghai).
It records red-sample coverage and one static future source candidate; it does
not implement or dynamically verify acquisition, handoff, routing, an opener,
driver wrapper, Conn generation, pass/native Tx association or a BUSY repair.
Keep Draft / dynamic, real-migration, product and production NO-GO.

Sections: [authority](#1-frozen-source-and-one-document-authority),
[three samples](#2-three-red-samples-and-current-admission),
[candidate](#4-one-exact-future-implementation-reference-candidate),
[future conditions](#5-future-acceptance-conditions-not-work-to-execute),
[stops](#6-unready-gates-and-stop-rules).

## 1. Frozen source and one-document authority

Stacked on open/Draft [PR113](https://github.com/surpaimb/cpa-cloud/pull/113),
exact remote head `9239deb589d57096c53fa7c124b80db6cf111bba`, base/sole parent
PR112 `07565d9a1ae44b93c4097659ddc08ef85854b1ee`, complete source tree
`4c56f9b595762670950bcbfc9acd6fadc4eff2cc`. PR113 has one commit/six files,
+930/-28; its 16261-character terminal body and red source remain frozen.

The entire whitelist is **one newly added file**: this document. No Go, test,
old contract/document, PR113/111 body, dependency, workflow, monitor, SQL, DDL,
source guard, selector, collector or Git/network trust configuration changes.
No new implementation-shaped tests, extra fixture/App/DB or old-case rerun.
Source and future acceptance statements below are specifications, not executed
tests or implementation permission. A second file requires stopping for direction.

Read with the frozen [first-App decision contract](employee-self-logout-first-app-source-owner-contract-2026-10-21.md),
[owner-only contract](employee-self-logout-first-app-owner-only-contract-2026-10-22.md),
[earlier coverage contract](employee-self-logout-red-sample-tx-pass-binding-contract-2026-10-21.md),
[driver-boundary contract](employee-self-logout-driver-boundary-observation-contract-2026-10-21.md)
and [Gate4 installation contract](employee-self-logout-prequery-cut-installation-contract-2026-10-21.md).
Their own source baselines, historical exclusions and limited review results
are not rewritten by this new document.

The coverage section and origin-prerequisite section can coexist: recording an
excluded sample is not selecting it; naming a possible source is not proving
acquisition. They confer no mutual implementation authority. Any executable
capture/handoff, selection expansion, selected opener/Conn generation and later
native Tx/pass/cut integration require separately bounded future batches.

## 2. Three red samples and current admission

The fixed event is [Code validation #512, run39637428191](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428191),
natural **pull_request / attempt1 / completed/FAILURE**, on exactly PR113's
head/base above. Its two failed jobs contain the three samples below. Full
exact-head filter=all checks are **23=18success+3skipped+2failure+0pending+0other**.
The three skipped Code platform jobs were not executed and are not PASS.

| Fixed sample and failed job | Handoff stage | Snapshot cut | Source-flow App and current owner selection |
| --- | --- | --- | --- |
| `TestSelfLogout20261021NativeAbort`; [race-service(1), job119351725681](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428191/job/119351725681) | INSTALL | AFTER_APP_CLOSE | Original first Open/App1, unselected. Failure is in install's closedRaw snapshot before trigger DDL and the later DELETE. |
| `TestSelfLogout20261021Boundaries/confirmation-query-error`; [race-service(0), job119351725717](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428191/job/119351725717) | REOPEN | AFTER_APP_CLOSE | Original first Open/App1 is explicitly typed-selected. Its report is only NOT_ACCEPTED_PRIOR_FAILURE, not owner PASS or owner rejection. |
| `TestSelfLogout20261021Boundaries/post-commit-report`; same race-service(0) job119351725717 | REOPEN | AFTER_APP_CLOSE | Original first Open/App1, unselected; the legitimate later Open has not occurred at this cut. |

All three report the same fixed failure layer: OBJECT_ENUMERATION /
QUERY_CONTEXT, SQLite5/BUSY and `SLOG21_UNEXPECTED_SNAPSHOT_QUERY`. The first
job's failed step is Race-check service shard1; the other is shard0. Only fixed
public synthetic classifications are retained here, not raw log lines, SQL,
error strings, paths, addresses/handles, identity, credential or data values.

**AFTER_APP_CLOSE is only the snapshot cut name**, not a handoff stage, native
Conn identity, physical-release/join guarantee or cause. App1 is an ordinal
checked against frozen source flow, not an identity inferred from logging,
pool statistics, a pointer, elapsed time or a reused pass number. The actual
schema-query actor is the independent raw observer DB/read-only transaction,
not the closed App's store Conn or its expiry pass.

The [NativeAbort entry](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L790)
calls [install](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L598),
which calls closedRaw before constructing/executing trigger DDL. Its test name
does not mean this failed execution reached the native ABORT operation.
Both listed Boundaries leaves call
[expectedAfter](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L736),
which enters [reopen](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L426).
[closedRaw](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L389)
closes the original App, creates the original raw DB and snapshots at line409,
before openRaw could perform another Open.

The current [typed mapping](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/expiry_invocation_owner_20261022_test.go#L31)
selects only confirmation-query-error among these three. AuthBoundaries also
has its separate existing first-App selection, but it is not a fourth Code512
red sample. No selection of NativeAbort/post-commit-report is implied here.
The [owner cleanup](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/expiry_invocation_owner_20261022_test.go#L47)
logs `FAPP_OWNER_ONLY_NOT_ACCEPTED_PRIOR_FAILURE` after a prior failure; that
does not evaluate the owner as accepted or rejected. Do not turn this absence
of acceptance into either result or backfill it from a previous local PASS.

None of these three installs the old pre-commit-refusal Gate4 empty-H0 plan.
The actual [raw snapshot order](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L169)
remains rollback defer, BEGIN_RETURN, existing cut call, then first query.
A nil plan's [route/cut](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_prequery_cut_installation_20261021_test.go#L42)
does not produce an installed first-App observation. No new cut is installed.

Same error layer does not establish a common lock/driver/worker root cause,
physical lock owner, PR113 introduction, repair, blame or exoneration. The
NativeAbort installer failure and the two later REOPEN samples stay distinct.

## 3. Historical evidence is append-only

PR113's other three natural attempt1 workflows remain historical SUCCESS:
SQLite native#19 [run39637428152](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428152),
CLI#26 [run39637428125](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428125)
and independent VM#31 [run39637428147](https://github.com/surpaimb/cpa-cloud/actions/runs/39637428147).
Native's six successful jobs and both VM source-a/restore-b/relay-cleanup
successes do not override Code512's two failed jobs or prove this origin path.

Root-confirmed original E/F each P1/P2/P3=0/0/0 on PR113's exact head/base
is **owner-only source narrow GO**, not dynamic, product or production GO and
not review/implementation approval of this document's new future candidate.
PR113 stays dynamically red and frozen.

The following remain intact and unresolved; no new green result replaces them:

- PR111 head `c1b8606e81494f86666e57865050b707f7cc7733`, base PR110
  `18b00683a3572278b071d1820821a9c00216810d`, natural Code511
  [run39616103565](https://github.com/surpaimb/cpa-cloud/actions/runs/39616103565),
  attempt1 FAILURE, checks23=18success+3skipped+2failure. AuthBoundaries parent
  App1 FINAL_CLEAN failed in race2 job119277518410; confirmation-query-error
  App1 REOPEN failed in race0 job119277517930, both at the recorded schema
  query layer with SQLite5 and SLOG21_UNEXPECTED_SNAPSHOT_QUERY.
- F's formal PR111 P1/P2/P3=0/0/1, the reader state-constant distinctness/zero
  false-green risk, remains a static P3, not a dynamically executed mutation.
  PR113's private enum controls did not fix that reader or close its P3.
- E's original PR108 P3 remains distinct. PR108 Code509
  [run39582468254](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254),
  PR106 Code508 [run39562535012](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012),
  PR100 Code501 [run39500154209](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209)
  and PR102/103/104/105 reds are not resolved or rerun by this document.
- The earlier local SPQI_INVALID_INSTALLATION candidate/output remain frozen.
  PR110's empty-H0 Gate4 source narrow GO and PR112's docs-only narrow GO do
  not grant first-App driver capture, routing, Conn/Tx/pass/cut or product GO.

PR113's author's tests-first missing-entry red, 90 directed control PASS pairs,
and the two original cases' one local PASS execution each remain separate author
evidence, with their hashes/limits preserved in PR113. None is a Code512 rerun,
proof of a BUSY repair, or retrospective instrumentation of an older head.

## 4. One exact future implementation-reference candidate

The sole static candidate expression is **s.db.Driver() on the already existing
InitializeWithConfig store DB**, after its original successful openStore check
and after its original defer s.close has been installed, before the next
administrator query. This is a proposed expression: it is **not present or called
in the frozen Initialize function**, and has not been executed by this batch.

The checked [Initialize source](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/config.go#L90)
calls openStore at90, returns on error at91-93, installs defer s.close at94,
then begins the administrator query at96. The exact hypothetical insertion
boundary is after94 and before95, not before original store construction and
not a new/later acquisition DB. The existing cleanup/return/error order is not
changed or newly guaranteed by this document.

Fixed Go1.26.9 [DB.Driver](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L1930)
returns the connector's driver implementation. Its existing
[dsnConnector](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go#L808)
distinguishes Driver(), which returns the implementation, from Connect(),
which delegates an actual connection construction to Driver.Open.
Fixed modernc sqlite v1.46.2 [registration](https://gitlab.com/cznic/sqlite/-/blob/v1.46.2/sqlite.go#L56)
uses newDriver; [driver.go](https://gitlab.com/cznic/sqlite/-/blob/v1.46.2/driver.go#L27)
owns singleton d and newDriver returns it. The fixed driver lacks DriverContext.
These are static implementation/API facts, not a newly measured origin event.

An implementation reference is neither a driver.Conn nor an App/Conn generation,
pass, native Tx, public sql.Tx, physical release or lock owner. The same registered
implementation can serve several DBs, so object/address equality cannot identify
which DB/Conn produced a sample. No reference/address/DSN/handle is published.
New sqlite.Driver instances, registration lookup hacks, global hooks, private
unwrapping/reflection/linkname and an extra Driver.Open probe are not substitutes.

### Earlier SQL and later ownership are separate lifetimes

[openStore](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/store.go#L25)
already creates the original SQL DB and calls initialize before returning;
its [initialization loop](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/store.go#L150)
executes SQL before the candidate point can exist. Those original Initialize
connections/transactions cannot be retrospectively registered or observed by
later obtaining Driver(). The Initialize DB is not App1's store or the raw actor.

The original [fixture construction](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/self_logout_confirmation_20261021_test.go#L275)
allocates its selection plan before Initialize, calls Initialize at293, then
the original first f.open at296. Plan allocation is not an invocation owner.
The actual [Open entry](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/internal/service/app.go#L214)
creates that capability only at215, later than Initialize. Open's own store
construction at285 precedes App construction at302 and worker creation at439.
Obtaining a reference from that later App's DB cannot retrofit its earlier Conn.

Any future early origin ticket would therefore have to remain a distinct
unadmitted candidate until separately proved Initialize outcome, actual Open
invocation and correct selected-object association. No such capture/ticket/
handoff/consumer exists now. Failure after the candidate point, including an
Initialize error after store success, must not become successful-source admission.
Original deferred close still executes on its old paths; its method return is
not a new physical-release or background-opener-join proof.

### No current capture, handoff or selected opener

InitializeWithConfig currently returns only error and exposes no driver result
or capture callback. The startup owner carrier applies to the later Open, not
the earlier Initialize DB. store.go still hardcodes sql.Open and offers no
selected per-App opener before actual Conn construction. Public DB.Driver/Raw
afterward cannot replace a driverConn already constructed by that DB.

The old driver-boundary contract accepted an earlier donor App implementation
reference for later reopened Apps only. This proposed Initialize point is a new
first-App prerequisite candidate, not an assertion that the old donor path was
extended, executed or validated. Neither contract supplies missing code authority.
Capture, lifetime-safe one-shot handoff, route-before-first-Conn, actual successful
Conn generation and exact optional-interface/error/context preservation remain
separate unimplemented obligations, not consequences of choosing this source.

## 5. Future acceptance conditions, not work to execute

Everything in this section is a future specification requiring separate code
and dynamic-test authorization. No mirror implementation/test, new SQL, old-case
execution or acquisition API call is part of this docs-only batch.

### Positive obligations

- Use only each target fixture's original InitializeWithConfig lifetime and
  original successful store result, with the old defer already installed.
  Preserve all SQL, arguments, errors, returns, cancellation and cleanup order.
  A successful reference read proves implementation origin only.
- State a per-target typed scope decision before that original lifetime.
  Preserve the matrix's current selected/unselected distinction; listing a
  sample is not default opt-in. Any NativeAbort/post-commit-report expansion
  needs explicit later authority, not this document's coverage table.
- Separate candidate capture, actual Initialize success, later Open invocation,
  actual App construction and caller-return association. Pending/failed/unknown
  facts are immutable and cannot be rewritten by a later successful association.
- Prove a finite, local, one-shot, non-overwriting handoff to the exact intended
  original first Open before its original Conn construction. Budget adequacy,
  ticket issuer/lifetime and the missing API/seam remain unproved, not invented.
  Default/unselected operation must retain its original path and context.
- Preserve fixed Driver/Conn/Stmt/Rows method sets and standard retry/cancellation
  behavior if an independently approved later wrapper is proposed. In particular
  SessionResetter and Validator cannot be dropped or fabricated. A constructor
  must not first create and discard an unobserved default DB.

### Rejection and exclusion cases

- openStore failure, nil/malformed result, or any later Initialize failure:
  no successful origin admission, no inferred success from an earlier reference.
- Missing, foreign, duplicate, overwritten, reused, over-budget or late candidate;
  wrong fixture, DB role, App, source or generation; source guessed from name,
  stack, address, pool statistics, SQL similarity or output order: no admission.
- A raw observer DB, Initialize Conn, new driver instance, another invocation's
  implementation reference or post-construction DB.Driver/Raw offered as App1
  Conn provenance: reject that claim, not repair or reinterpret the old object.
- Any second Open seeking App1's one-shot origin admission: reject the reuse.
  The legitimate later Open must still execute its original unselected business
  path; diagnostic exclusion must not make that Open fail or invent a duplicate.
- Extra DB/Conn/Open/Ping/Raw/query/probe/worker, registry/global hook, timeout,
  wait, retry, connection setting or driver-instance fallback: outside authority.
  Only a future separately approved original delegated Driver.Open construction
  could acquire a Conn generation; never an extra call to test the reference.
- Interface loss, changed DSN/options/context/errors, a failed native Open
  admitted as a successful Conn, or a number/reader/publication token offered
  instead of an actual native object chain: reject the evidence claim and stop.

These are qualification rules, not permission to change product errors, cleanup,
worker start/stop/Wait, Background parent, passCtx, SQL, old assertions or failing
outputs. Unknown remains unknown; no later case/green may fill earlier gaps.

## 6. Unready gates and stop rules

Gate1 trusted pass-reader attribution remains unready; the current reader only
returns unattributed raw pass metadata and its F P3 remains open. Gate2 has only
the narrow PR113 owner source, not full Conn generation or this acquisition path.
Gate3 actual native Tx/proxy/public sql.Tx binding is unready. The three first-App
cuts are not supplied by the separate pre-commit-refusal Gate4 empty-H0 control.

Stop without enlarging scope if exact head/base/open/Draft changes, fixed run/
attempt evidence contradicts the matrix, a source/anchor cannot be verified,
or historical reds/review boundaries cannot be retained. Stop if this document
would need to call an unimplemented candidate verified/collected/routed, silently
admit the unselected samples, modify an old contract/guard, add a second file,
change a workflow/filter or create a test/probe to make its assertions true.

Any future executable batch must independently obtain its own exact whitelist
and first-unexpected-red freeze rules. This document authorizes no hot patch,
rerun/dispatch, new observed lifetime or claim that all phases can fit one code
batch. New evidence cannot retroactively instrument Code512 or earlier heads.
No physical-lock/common-cause, zero-disturbance, complete-tail, driver transparency,
GOV-02, real-credential/DB, migration or product/production acceptance is supplied.

## 7. Docs-only verification and natural CI expectations

Verify one-added-document scope and git diff --check. Check relative document
links and local heading anchors, and pin every cited project source line to the
exact PR113 SHA/blob rather than a moving branch or an invented future line.
Use only read-only source/file inspection for these checks; no Go test/build/
vet/race, SQL/App/DB/service/browser or implementation-shaped test is needed.

Under the frozen workflows, a new stacked PR whose diff is only this document
has these **static expectations**, not predeclared actual CI results:

- [Code validation](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/.github/workflows/ci.yml#L7)
  ignores Markdown/docs, so no natural Code run/check execution is expected.
- [CLI VM](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/.github/workflows/offline-v3-cli.yml#L4)
  and [independent VM](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/.github/workflows/cross-vm-backup.yml#L4)
  paths do not match this added document: untriggered/unexecuted, not green or
  a successful skipped execution. Do not dispatch/rerun or change their filters.
- [SQLite native](https://github.com/surpaimb/cpa-cloud/blob/9239deb589d57096c53fa7c124b80db6cf111bba/.github/workflows/sqlite-compat.yml#L5)
  has no pull_request path filter; one natural workflow with six configured
  native jobs is expected. Its frozen Go1.26.8 compatibility jobs cannot validate
  a new Go1.26.9 origin seam, driver observer or owner/Conn/Tx/pass binding.

Query the actual new head's natural event/attempt and complete paginated checks;
report real success/skipped/pending/failure/other counts without borrowing old
green or assuming a final total. First unexpected natural red freezes the batch;
no rerun or fix is authorized. A docs-only native green is not all-source CI green.
Root separately re-pins the new PR and arranges original E/F formal same-head
independent read-only document review. No review result is preissued here.

## 8. Sources and provenance

Independently authored from the root's 2026-10-22 one-document authority, the
exact project source/contracts and private read-only examination of Code512's
fixed synthetic diagnostics. Only fixed classifications are reproduced. No raw
log, archived CPA/CLIProxyAPI/Sub2API code/test/documentation, SDK, library
implementation or protected artifact is copied/ported. Prior exposure means
no strict clean-room claim. No new dependency or implementation is introduced.

The Go1.26.9 standard-library and modernc sqlite v1.46.2 public-source references
above explain the static method distinction only; their existing BSD-3-Clause/
BSD-style licensing and pinned repository graph remain unchanged. Native CI's
separate Go1.26.8 pin is not upgraded or conflated with those semantics.

## 9. Author evidence for this document

The author's read-only checks verified five relative document links, five local
heading anchors and 18 exact PR113 source-line anchors against their complete
base-tree blob hashes. Four dependency source-line references were checked
against the pinned local Go1.26.9/module sources; this is not a claim of executing
those APIs or testing external-site availability. Single-added-file scope and
git diff --check are required again on the final staged document.

Only document/source-link/heading/scope/whitespace inspection and Git metadata
operations are performed. No author-run Go/application/SQL/browser exercise or
old-case execution occurs. Natural CI, if triggered, is separate evidence and
must be reported for the actual new document head, not asserted from Code512.
Keep every PR Draft and all unresolved dynamic, real migration, product and
production gates NO-GO.
