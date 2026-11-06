# Administrator current-session logout confirmation

Date: 2026-10-21. Independent product slice; Draft / production NO-GO.

Fourth independent batch: fixture-only repair after the frozen third-batch FAIL.
The production admin_auth.go SHA256 remains locked to
5e13a2cc4c552ab40f6682cc9c49d2fe21ed5564b4f5b02bb50f9ff39cccf162.
Only this test file and necessary contract statements change from that candidate;
the old tree and all old raw evidence are read-only. This is not a fifth batch,
a retry of the third batch, or permission to repair production after a new red.

Forward-only ordinary validation, effective only for this logout slice: on 2026-10-21
the root coordinator explicitly withdrew the earlier self-added 2400s hard-wall
and Windows Job Object acceptance promises. Ordinary single-attempt verification
with explicit actual results replaces them here. This is not a retrospective
approval, rerun or relaxation of frozen active-only or old hard-gate batches.
The two preparation FAILs remain unchanged: JavaScript write-wrapper SyntaxError
(failure SHA256 32157e86d5dcb3f01903b3764eaff089eb906094ec489fbd0b64f9fd6f058c28)
and optional Windows wildcard read-query failure (failure SHA256
155c7f2f84c1eccb794f84a946a6224ac4f90ee0c6a55b34ef7328dace2a84b8).
Neither ran Go, reproduced the defect or completed its old2400s contract.

### Frozen third-batch history, not current acceptance

The third batch's one old-symbol-only tests-first invocation really exited1,
after privately observing the joint old204/clear-cookie/persistent-row/manual
old-cookie200 oracle and the fixed ADMIN_LOGOUT_OLD_DELETE_ABORT_ACCEPTED label.
It is referenced here, not rerun or recounted as fourth-batch acceptance. Its raw
stdout SHA256 is 064e0d2aa42719b300cf456cc51dd970c192fd9f28458ee79b18b21529e410ac.

The third final invocation unexpectedly exited1: nine new parents and fourteen
children passed; TestAdminLogoutOwnerPredicate failed at ALOG_REOPEN_OPEN with a
subsequent panic marker. The four old parents/eleven children did not execute
under failfast. Raw stdout SHA256 is
893f075533bacaa7a281a1bb41e24d176409ceb5284b9fffec6c70cf54ac5c53;
the evidence summary SHA256 is
b0dd9f5b47a7ef5c71f602f1f2b4d42aa5d8c6f3cb31a9dea2da9227b88c5fcf.
No subsequent vet/build/commit/push/PR was performed for that failed batch.

Original E and F independently read the frozen three hashes/evidence: E recorded
P1/P2/P3=0/2/1, F=0/1/1. Both identified the fixture's out-of-transaction second
administrator as conflicting with the existing singleton Open invariant, and a
nil-reference cleanup risk after failed Open. Static review cannot establish the
sole later panic stack source. Neither proved a production logout defect or
issued source GO. Nine passed parents do not complete the missing matrix.

Baseline PR98 head d96e99ebef5942db1b585ff0042efc0872c67eca, base PR97
2bd3c03d162476ec5616efd00945c4a58c45a076. Approved local same-tree source:
bab43fe0fdf61efef5b819aa2e9ba309004cb9e5, full tree
69118d96263639f80084bfb9746c5e283b064348. Local/remote ancestry differs.

## Independent provenance and scope

Independently authored from the coordinator's logout confirmation requirements,
the [preview interface](preview-contract.md), CPA Cloud's own session schema and
independently authored synthetic tests. No archived/reference code or tests are
copied/ported; no strict clean-room claim. No new dependency, protocol or license
grant; existing licenses remain in [notices](../THIRD_PARTY_NOTICES.md).

Exactly three product files: internal/service/admin_auth.go, new
internal/service/admin_logout_commit_test.go and this contract. Only the existing
DELETE /admin/api/v1/sessions handler changes. login, requireAdmin, routes, DDL,
OAuth cascade definitions, employee/Key behavior, workflows and old bodies freeze.
The separate active-only four-file candidate and all old evidence remain frozen.
The earlier clean-tree preparation SyntaxError is retained as preparation FAIL,
not a tests-first reproduction or source/test success.

## Confirmed transaction, unchanged authority

After unchanged authentication, Origin and CSRF checks, begin one request-context
transaction, defer rollback, and delete only SessionID AND AdminID authenticated
by requireAdmin. Every operation while holding the single-connection transaction
uses tx, not a.store.db. Require RowsAffected exactly one and a transaction query
confirming that original session ID is absent. Only a successful real Commit
permits the unchanged cookie-clearing attributes and 204.

Begin/SQL/result/confirmation/Commit error, zero rows (including RAISE IGNORE or
the second already-authenticated concurrent request), or abnormal result count
returns fixed 503 storage_unavailable, without clearing the success cookie or
retrying. A request rejected by authentication first can instead return 401.
Existing codex_oauth_sessions ON DELETE CASCADE remains; current-session children
may be removed, while another session and its OAuth rows stay unchanged.

503 does not promise rollback, session presence or absence. A real Commit followed
by a controlled reporting error still returns503 although persistent rows can be
gone. Deferred rollback does not undo a successful Commit. No global revocation
barrier is provided to previously authenticated in-flight requests. Existing
requireAdmin database-error and expired-session cleanup semantics do not change.

Private operation-owned request-context seams are absent in production and have
no HTTP representation. They cover only Begin, result/confirmation and Commit
boundaries. Actual SQL/confirmation/default Commit remain real SQLite. Adapters
may inject failure or invalid result evidence, never replace SQLite success.
Controlled adapters are not real IO/power-loss failure injection.

## Fixed finite acceptance matrix

Owned temporary synthetic SQLite and random loopback HTTP only; never real data,
credentials or port8787. Requests use finite client timeouts, manual original
cookies and cancellable barriers, not cookie-jar state or sleep ordering. No
cookie, CSRF, secret, response-body or SQL-fact dump is allowed in evidence.

Planned new parents and cases:

- TestAdminLogoutDeleteAbortFailsClosed: real BEFORE DELETE RAISE ABORT. The one
  frozen third-batch old-symbol-only tests-first command emitted the expected label
  ADMIN_LOGOUT_OLD_DELETE_ABORT_ACCEPTED only after privately checking old204,
  clearing cookie, persistent target and manual old-cookie authentication. A
  fixture/compiler failure does not count; preserve the original red raw bytes.
  This fourth batch does not execute another old-symbol tests-first command.
- TestAdminLogoutConfirmedPersistence: nonempty OAuth rows for both sessions,
  normal204 and real reopen, target/OAuth
  absence, manual original-cookie401, other-session/OAuth preservation and auth.
- TestAdminLogoutNativeIgnoreZero: real RAISE IGNORE with zero-row503.
- TestAdminLogoutNativeReinsertConfirmation: real AFTER DELETE reinsert defeats
  RowsAffected-only success; transaction rollback preserves controlled facts.
- TestAdminLogoutRealCommitRefusal: actual SQLite deferred-FK Commit refusal,
  separate from a test adapter merely reporting failure. Before any reopen,
  require the same still-alive App to perform a new real transaction, read its FK
  setting/guard, write the owned guard and Commit, then verify both session/OAuth
  snapshots. No manual ROLLBACK, pool reset or reopen substitutes for this control;
  it does not independently identify the physical pooled connection.
- TestAdminLogoutCommitUncertainty: before-real-Commit refusal and successful
  real Commit followed by reporting error, with distinct persistent facts.
- TestAdminLogoutBoundaryErrors: Begin, RowsAffected error/zero/two and confirmation
  query error; clearly label controlled result/metadata adapters.
- TestAdminLogoutAuthPrecedence: anonymous/wrong cookie and missing/wrong Origin
  or CSRF are rejected before the transaction boundary.
- TestAdminLogoutConcurrentZeroRows: two handlers already authenticated; cancellable
  barrier, one204 and zero-row503, no sleep ordering.
- TestAdminLogoutOwnerPredicate: controlled transaction owner change proves the
  authenticated AdminID predicate. Insert the temporary second administrator and
  update the owner only through the same afterBegin tx. After the expected503,
  first require one administrator, no temporary administrator, original owner,
  unchanged target/other sessions and both nonempty OAuth snapshots; only then
  reopen and repeat. Never delete the extra administrator afterward to get green.
  The same parent also owns a separate persistent two-administrator negative
  profile: real Open must reject the existing singleton constraint. Failed Open
  must retain the last owned reference; cleanup must safely handle live, closed
  and nil states. No additional parent/subtest is used for these controls.

Unchanged selected baseline regressions:
TestRuntimeFixtureDirectSessionKeepsAuthorizationBoundaries,
TestAdministratorPasswordLengthBoundaries,
TestPreviewWorkflowPersistenceStreamingAndRevocation,
TestEmployeeRetirementPatchRaceK04AndRestart.

Frozen new child domains: CommitUncertainty has before-real-commit and
after-real-commit; BoundaryErrors has begin-real-after-auth, begin-adapter,
rows-error, rows-zero, rows-two, confirmation-query-error; AuthPrecedence has
anonymous, wrong-cookie, missing-origin, wrong-origin, missing-csrf, wrong-csrf.
The other seven new parents have no children. Existing selected password-boundary
children are below_minimum, minimum, bcrypt_maximum, above_bcrypt_maximum; existing
K04 children are K04-retired-false, K04-retired-true, PATCH-wins-stale-retirement,
retirement-wins-stale-PATCH, restart-enrolled-true, restart-enrolled-false,
self-feature-off. The other two selected baseline parents have no children.
Total planned: 14 parents and 25 subtests; no approved skip in this local selection.

Native Commit refusal uses an owned deferred foreign-key guard, with observation
of the actual Commit error; a nil reporting result cannot erase it. The Begin
failure control closes only the owned DB after authentication at beforeBegin,
then reopens before checking persistence. Confirmation-query failure is induced
by a controlled transactional table-rename adapter; it is not product DDL or a
real IO fault. Result-count adapters never bypass the genuine RowsAffected check.

## Fourth-batch ordinary single-attempt validation

Only reference the frozen tests-first red; run one final Windows noncached
serial targeted test command (-p=1 -parallel=1), selected affected regressions,
directed vet/build, formatting/diff/link/scope checks. Freeze the complete finite
parent/subcase oracle before the final command. Record exact source identities,
arguments, workdir, real exit and actual results separately for each gate.
No automatic retry or attempt to obtain green by repeated tests.

go test -timeout limits the test runtime, not compilation, tools, blocked IO or
arbitrary child processes. vet/build have no equivalent test timeout. This batch
does not promise a whole-command/global upper bound, Job Object termination,
full process-tree stop or verified cleanup merely because a tool yielded.
If a command hangs, output is incomplete, tool connection or exit status is
unknown, or an owned child is not confirmed exited: stop NO-GO, start no later
gate and report possible activity instead of assuming cleanup/termination.

The earlier production implementation is copied but hash-frozen. This batch may
only repair the specified fixture/contract before its one final command.
Fixture/compiler/HTTP error is not reproduction. A read-only preparation query typo can be recorded/corrected,
but writing/identity/safety anomalies or first unexpected Go/CI red freeze the
batch without hot repair/retry. Old hard-gate contracts and H2/H3 remain frozen.

All new503 cases must verify the exact fixed JSON code/message and no Set-Cookie
at all, not merely absence of one recognized clearing cookie. Both sessions have
nonempty synthetic OAuth rows before each new product test; snapshot comparisons
and row-presence/auth assertions remain private and never dump SQL facts.

## Delivery and nonclaims

Only after local gates actually pass: one commit/push and new independent Draft
PR stacked on exact PR98. Approved connector transport must prove remote parent,
full candidate tree and three blob/modes, disclosing different local/remote SHA
and ancestry. No force, trust/credential/remote configuration change.
Natural exact-head CI must actually execute core/required race jobs; skipped is
not execution. Root owns original E/F same-head formal read-only review. No self
GO or source repair/manual rerun/dispatch after publication.

Old Windows/R1/V2/D1 red states, H2/H3 NOT_ENABLED and active-only four-file WIP
remain frozen, not supplemented by this slice. No physical-fault, backup
anti-resurrection, real migration or production claim; no Ready/merge/tag/package/
deploy. Planned tests and later actual evidence remain separate.
