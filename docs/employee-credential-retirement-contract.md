# Single-employee current-credential retirement

Status: bounded implementation contract, development preview, production NO-GO.
Baseline: PR94 `52e5ed3d426d95bbca7e0414c0c72a8a7cc48047`.
This contract authorizes neither changes to ordinary employee PATCH/K04 nor
production enablement, backup non-resurrection, or master-key rotation.

## Independent provenance and licenses

This document, new handler and tests are independently authored from the
coordinator's frozen single-employee retirement requirements and CPA Cloud's own
[preview contract](preview-contract.md), [K04 acceptance requirement](acceptance-matrix.md),
[self-service foundation](employee-self-service-foundation-contract.md),
[single-slot issuance contract](employee-self-key-issuance-contract.md),
[self revocation contract](employee-self-key-revocation-contract.md), and
[existing audit projection contract](admin-audit-overview-contract.md).
Public API references consulted 2026-10-20: [Go HTTP ServeMux](https://pkg.go.dev/net/http#ServeMux),
[Go SQL transactions](https://pkg.go.dev/database/sql#Tx), and
[SQLite transactions](https://www.sqlite.org/lang_transaction.html).
No archived CPA, CLIProxyAPI, Sub2API, adjacent reference implementation, SDK,
test, migration or asset is copied or ported. Earlier reference exposure means
this is not represented as strict clean-room development.

No new dependency, DDL, migration or license grant is introduced. Existing Go
standard-library and module licenses remain their own; the pinned SQLite driver
and dependency notices remain in [third-party notices](../THIRD_PARTY_NOTICES.md).
This batch does not resolve the project's separate licensing/release gates or
authorize publication of a package or deployment.

## Exclusive four-file boundary

Only this new document, `internal/service/employee_retire_credentials.go`, its
new `_test.go`, and the independent route registration in existing
`internal/service/app.go` may change. Operation-specific private request-context
test seams live in the new Go file, are nil in production, and cannot be selected
by HTTP input. No shared authentication, PATCH, Key creation/issuance, dispatch,
schema, workflow, UI or CLI primitive changes. If a fifth file is needed, stop
before expanding product edits and ask the coordinator.

## HTTP authority and exact input

`POST /admin/api/v1/employees/{id}/retire-credentials` is an independent
administrator operation, available irrespective of the employee self-service
feature flag. It uses the existing administrator session, exact Origin and CSRF
write guard. An employee model Key or self cookie is not administrator authority.
The actor is the authenticated `adminSession.AdminID`, never request data or a
synthetic/hardcoded actor. The target is one bounded employee identifier.

No query string is accepted. The body is at most 1024 bytes, valid UTF-8, exactly
one JSON object with exactly one case-sensitive field `expected_revision` and no
duplicate/unknown fields or trailing JSON. Its value is a canonical positive
decimal JSON integer in `1..9007199254740990`, leaving room for revision + 1.
Strings, null, booleans, fractions and exponent forms are rejected. There is no
`operation_id`, arbitrary Key selector, password, confirmation payload or receipt.

Confirmed success is `200` with only `{id,status:"disabled",revision}` and
`Cache-Control: no-store`. Missing employees return fixed `404 not_found`;
stale revisions return fixed `409 revision_conflict`; invalid input returns fixed
`400 invalid_request`. Storage, malformed stored state, row-count, audit or commit
failure returns fixed `503 storage_unavailable`, never raw SQL or private values.
Existing administrator authentication/Origin/CSRF error classes remain unchanged.

## One atomic local transaction

After parsing, acquire `admission.Lock`, begin one write transaction owned by the
handler, and defer rollback. The transaction helper receives that same caller-owned
transaction; it never opens another connection or commits independently.

1. Re-read the employee and current valid status/revision. Both active and already
   disabled employees are eligible, including employees with no Key. Compare the
   expected revision and CAS exactly one employee to disabled/revision + 1.
2. Enumerate all existing Key rows belonging to that employee, including expired
   and slot Keys. Capture one server UTC timestamp. Set only null `revoked_at`
   values to that timestamp; preserve every pre-existing revocation time. Key IDs,
   selectors/digests, operation IDs, policies, usage, financial facts and unrelated
   employees are not deleted, reset or rewritten.
3. Validate any slot's employee/Key owner relationship. A cross-owner/malformed
   relationship fails the whole transaction; it never retires a foreign Key.
   Only pending/armed slots become cancelled. Clear their arm fingerprint and all
   arm revision fields, set cancelled_at, and retain the row and history. Issued
   and cancelled tombstones retain their state and existing timestamps. The issued
   Key is nevertheless included in Key revocation. Never delete/recycle a slot.
4. Delete all target self-session and self-credential rows. The latter also removes
   outstanding enrollment digest/expiry and password hash. Do not touch admin
   sessions, another employee, usage, wallets, subscriptions or response resources.
5. Call existing `recordLifecycleAudit` with the same transaction, authenticated
   administrator ID, fixed `employee.retire_credentials` action, `employee` target
   type, target ID and `succeeded` result. Any audit insertion/pruning failure rolls
   back the employee, Keys, slot and self cleanup together.

The new audit event must still be present in the bounded retained set at success.
If existing retention would immediately evict it (for example because of a clock
rollback against future-dated old facts), fail and roll back the whole operation;
do not change the shared retention rule or claim an unretained audit as evidence.

Check database results and row counts. Zero Keys/sessions/credentials is legitimate;
an unexpected count or inconsistent ownership is not silently accepted. Do not
load policy or employee-model projections through another DB connection in this
transaction or after commit merely to build a success response.

Only a confirmed commit allows the success response. Notify existing account-pool
waiters to revalidate after confirmed commit. A refused or uncertain commit emits
fixed 503, does not auto-retry and does not claim zero writes. An advisory wake on
uncertainty is permitted solely to force current-store revalidation. If the commit
actually happened, retrying the old revision returns 409, not replayed success.
If it did not happen, a later authorized request may still match that revision.
This is CAS, not a persistent operation receipt or exactly-once delivery protocol.

## Compatibility, cutoff and hard limits

“Current credentials” means rows present in this local transaction's serialized
database state. A competing administrator Key creation that commits first is
included; creation after retirement is a new allocation. The existing admin path
can create a new Key for a disabled employee and this batch does not change that.
Disabled status still blocks its use; later explicit re-enable/new issuance is a
new administrator authorization, never revival of a retired old Key.

Ordinary PATCH disabled -> active keeps K04: ordinary unrevoked Keys recover;
explicitly revoked Keys do not. This new endpoint does not rewrite that behavior.
Self enrollment/login/issue and final dispatch use their existing serialization
and final checks. A dispatch committed into MayHaveSent before retirement may
complete, including a stream. Later final dispatch must reject retired authority
before a new upstream attempt. Notification is advisory, not authorization or
proof of termination; no data retrieval or upstream request is undone.

Retirement applies only within the current local database lineage. Old backups
can restore old keys, self credentials, grants and sessions. WAL/backups/digests
are not forensically erased. This is not cross-backup GOV-02, a terminal employment
offboarding guarantee, cross-instance consistency, M1-to-M2 rotation, automatic
reissuance or production acceptance. Existing lifecycle audit is capped at 1000
retained rows, lacks revision/count receipts and is not permanent/tamper-proof.
Real notice, jurisdiction/retention, key custody, high-risk audit and recovery
non-resurrection remain separate production hard gates. Keep Draft/production
NO-GO; no Ready, merge, tag, package, deployment or real-provider operation.

## Independently authored verification matrix

Use only synthetic temporary SQLite and dynamic loopback HTTP ports, never 8787,
real credentials/data or the main worktree. Report observed tests separately from
planned guarantees; PR94's CI/reviews do not validate this new feature.

- Zero/multiple Keys; expired and pre-revoked Keys; the four slot states; shared
  timestamp for newly revoked Keys; preserved old timestamps, tombstones, IDs,
  digests/policies/usage/financial facts, other employee and administrator sessions.
- Anonymous/model-Key/self-cookie role isolation; Origin/CSRF; missing employee;
  stale revision; strict/case-sensitive/duplicate/oversized/trailing input; disabled
  target and no-Key operation still increment revision and record an audit.
- Inject failures at employee, Key, slot, self-session, self-credential and audit
  writes; prove no partial transaction. Inject refused commit and real commit
  followed by an error; no false success, no raw errors, old-revision 409 only
  when the database actually committed. Confirm the actual authenticated actor.
- Same-revision two-request race: one confirmed success and one 409. Exercise both
  serialized orders with Key creation, self issue, ordinary PATCH and final
  dispatch; use operation-owned seams and existing counterpart seams, not sleeps
  as the sole ordering evidence. Wake a queued model and prove no extra upstream
  attempt. Already dispatched work keeps the existing completion semantics.
- Reopen the same synthetic database and prove retired Keys/passwords/sessions/
  enrollment cannot authenticate, slot cannot recycle, and K04 control remains.
- Run targeted noncached Go tests, vet/build, formatting, document links and diff
  boundary checks. Optional full Windows SQLite testing must be serial. Natural
  PR workflows match `internal/**`: Code validation and both VM workflows run;
  docs alone would not. VM regression is inherited synthetic recovery, not new
  retirement acceptance or backup non-resurrection. First unexpected natural red
  freezes source: no patch, rerun or manual dispatch. Root owns subsequent exact
  head/base E/F formal review. No existing PR body/history is changed.
