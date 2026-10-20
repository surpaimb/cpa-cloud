# Administrator single-Key revocation confirmation

Date: 2026-10-21. New independent bounded product slice. Draft / production NO-GO.

## Baseline, provenance and frozen scope

Exact remote PR99 head 8280d3df2c59cd2280d39b25e788f05d39aab4ef, base PR98
d96e99ebef5942db1b585ff0042efc0872c67eca. Approved local equivalent base:
70ca37f77406f3e728f454ae8ee0b4abe9af8f63. Both full base trees are
658ac86aa8f494243271c06a4db70f104e35ef4c; SHA and parent identities differ.
A new clean isolated worktree is used; no main-tree or active-only WIP is input.

Independently authored from the coordinator's bounded revocation specification,
the [preview interface](preview-contract.md), current own-product SQLite schema
and [self slot contract](employee-self-key-issuance-contract.md). No archived
CPA, CLIProxyAPI, Sub2API or adjacent reference source/test is copied or ported;
no strict clean-room claim. No new dependency or external protocol. Existing
dependency licenses remain in [third-party notices](../THIRD_PARTY_NOTICES.md).

At most three files: this new contract, new
internal/service/admin_key_revocation_confirmation_test.go, and only revokeKey
plus necessary private helpers in internal/service/employees.go. Shared slot
helpers, createKey, routes/authentication/dispatch, schema/dependencies/workflows/
UI, PR99 source/body, frozen newKey four-file WIP and all old evidence freeze.
If implementation requires wider changes, stop and request a new scope.

## Static concern versus required dynamic reproduction

E/D's read-only concern is a potential false200 for ordinary or issued Keys when
an AFTER UPDATE OF revoked_at trigger restores NULL. It is not yet a dynamic
fact in this batch. A pending/armed slot normally becomes cancelled and remains
unauthenticatable even if its Key's revoked_at is NULL; never turn that NULL
alone into an authentication-bypass claim.

Before product edits, one tests-first invocation of
TestAdminKeyRevocationTriggerReversal must use a private temporary synthetic
SQLite profile. Initialize/Open/login, active employees and ordinary Keys use
legitimate existing product routes. Verify the exact original Key really gets
200 from /v1/models; include a sibling Key and another employee/Key as controls.
Close the App, add one actual target-limited AFTER UPDATE OF revoked_at trigger
that restores non-NULL to NULL, close its raw owned DB, then require real Open
to succeed before any revoke. Invoke the real administrator HTTP route. The
frozen expected red label ADMIN_KEY_REVOKE_OLD_TRIGGER_ACCEPTED_ACTIVE_KEY is
allowed only after jointly observing old200, final revoked_at NULL, the original
Key still authenticating, and the same facts after closing/reopening; both
controls must be preserved. No weakened schema or disabled validation.

Open rejection, missing joint oracle, compiler/fixture/HTTP error or another
failure is unexpected: freeze the exact raw evidence, do not weaken/repair/rerun.
Only the specified genuine old red authorizes the product implementation.

The single old invocation subsequently established that exact oracle: exit1,
one selected test/package failure, no skip, panic or JSON parse error, fixed label
ADMIN_KEY_REVOKE_OLD_TRIGGER_ACCEPTED_ACTIVE_KEY only; actual elapsed24.0338028s.
Original employees.go remained SHA256
da770ad076844a21edfad142ec40a5dcc6fd2be1e6ab9704a8937e6060488f0e.
Frozen raw stdout SHA256:
b0aaddc28d916f243c20fed2a51bddf731f48e70734b77be64cd2d2f650528cb.
This is this ordinary-Key synthetic dynamic reproduction, not a retrospective
claim about E/D testing, pending/armed authentication, physical faults or old PRs.

## Confirmed operation, unchanged authority

Keep POST /admin/api/v1/keys/{id}/revoke with its existing administrator session,
Origin/CSRF checks, empty-object protocol, exclusive admission lock and one
SQLite transaction. No new audit (separate batch), identity or request replay.
Determine genuine missing Key as404 before mutation; query/storage/constraint
anomalies are fixed redacted503. Require an existing Key owner and matching slot
employee ownership. Every operation during the transaction uses tx, not db.

Do not rely on a top-level RowsAffected alone. In the same transaction require
the final Key to have a valid non-NULL revocation time. Never re-sign an existing
revocation time: compare it byte-for-byte with the original value. Missing,
malformed or reverted final facts refuse success. Ordinary and issued paths
have the same final-Key confirmation; issued and already-cancelled slot records
remain tombstones and cannot be reclaimed or reissued.

For pending/armed, preserve the existing shared cancellation helper but confirm
the final cancelled state, valid cancelled_at, matching ownership and retained
Key/employee/created-at identity. The operation's private caller clears and
confirms all activation fields: arm_fingerprint, four arm_*_revision fields and
armed_at. Existing issued/cancelled tombstones are not rewritten. No shared
helper, schema or slot issuance behavior changes.

Only confirmed postconditions and an actual successful Commit permit notification
and {ok:true}/200. Any boundary, cancellation or Commit/reporting uncertainty is
fixed503 storage_unavailable / Service is temporarily unavailable., no Set-Cookie,
no automatic retry and no claim of rollback, target absence or completed revoke.
In particular a trigger-caused503 may leave the original Key usable: the safety
claim is no false successful200, not an invented successful revoke on failure.
An actual Commit followed by a controlled reporting failure can leave revoked
facts durable; distinguish it from a pre-Commit refusal.

Preserve other Keys/employees, policy/quota/accounting facts and already
MayHaveSent request semantics. This is only the current database lineage.

## Finite executable acceptance plan

All profiles/SQLite/HTTP/upstreams are owned temporary synthetic objects on random
loopback ports, never8787 or real credentials. No raw keys, cookies, passwords,
CSRF, digests, response bodies or SQL facts are dumped. Fixed result tags only.
Real initialization/Open, original manual Bearer credentials and real HTTP are
distinct from controlled request-context boundary adapters.

Required final cases (exact finite names/counts frozen before final command):

- The genuine trigger old red becomes new explicit503/no false200, with retained
  ordinary Key controls; no-trigger200 revokes durably and reopening rejects it.
- Real UPDATE ABORT/IGNORE, zero rows versus genuinely missing404, SQL/metadata
  errors, mismatched slot owner and malformed stored timestamps.
- Pending/armed cancellation restored by a real trigger must fail postcondition
  confirmation; ordinary/issued trigger reversal is not accepted as200.
- All four slot states, immutable issued/cancelled tombstones, idempotent repeated
  revoke with the original revocation time, and other Key/employee controls.
- Actual native deferred-constraint Commit refusal plus a subsequent read/write
  transaction on the same live App before reopen; no manual connection repair.
- Successful real Commit followed by controlled adapter error differs from
  before-Commit refusal; notify only after fully confirmed success.
- Self issuance and final dispatch in both serialized orders; no weakening of
  MayHaveSent or new quota/Key allocation behavior.
- Administrator auth/Origin/CSRF precedence and necessary frozen old regressions.

After the genuine old red, one complete targeted Windows SQLite noncached serial
final command (-p=1 -parallel=1), then only on complete success directed vet/build
and format/link/diff/scope gates. No parallel whole-repository SQLite test. First
unexpected Go/CI red, incomplete output, unknown exit or unconfirmed owned tool
activity freezes the batch; no patch/rerun or later gates. A yielded tool is not
an exit proof. go test -timeout bounds runtime, not compilation/tools/blocked IO
or a whole process tree. No global hard-wall/Job Object cleanup promise.


### Frozen final selection for this batch

Exactly15 parents/38 subtests (53 full names), zero approved skips. Ten new
parents/34 children plus five unchanged regression parents/four children:

- TestAdminKeyRevocationTriggerReversal (no children).
- TestAdminKeyRevocationPersistenceAndOldTime: fresh, already-revoked.
- TestAdminKeyRevocationNativeErrorsAndMissing: abort, ignore, zero-after-read,
  key-query-error, slot-query-error, missing-key, begin-real-after-auth,
  begin-adapter, old-time-empty, old-time-malformed, old-time-blob.
- TestAdminKeyRevocationCommitOutcomes: before-real-commit,
  after-real-commit-report, real-native-refusal.
- TestAdminKeyRevocationAuthPrecedence: anonymous, wrong-cookie, missing-origin,
  wrong-origin, missing-csrf, wrong-csrf, invalid-json.
- TestAdminKeyRevocationAllSlotStates: pending, armed, issued, cancelled.
- TestAdminKeyRevocationSlotOwnerMismatch (no children).
- TestAdminKeyRevocationSlotTriggerReversal: pending, armed, issued.
- TestAdminKeyRevocationSelfIssueBothOrders: revoke-first, issue-first.
- TestAdminKeyRevocationFinalDispatchBothOrders: revoke-first, dispatch-first.
- Existing TestRuntimeFixtureDirectSessionKeepsAuthorizationBoundaries,
  TestPreviewWorkflowPersistenceStreamingAndRevocation, TestSelfKeySlotCancel
  and TestAdminLogoutConfirmedPersistence have no children.
- Existing TestAdministratorPasswordLengthBoundaries has below_minimum, minimum,
  bcrypt_maximum, above_bcrypt_maximum.

Native compatibility/error cases distinguish actual ABORT/IGNORE, zero rows
after a real in-transaction deletion, actual closed-DB Begin and deferred-native
Commit refusal from controlled Begin/transactional-metadata/pre/post-Commit
adapters. Malformed stored timestamps verify fixed503 and unchanged facts; no
claim that such corrupt profiles must be accepted by startup is made. Private
request-context hooks have no HTTP representation, cannot erase actual Commit
failure and do not replace the real update/final reads/default Commit/notification.

Slot setup/enrollment/reservation/limits/arming/issuance uses actual own-product
HTTP paths. The finite dispatch-order case additionally uses the existing
private runtime fixture and real administrator HTTP revocation around actual
durable dispatch marking: it is not actual upstream I/O or a physical fault.
Quota/ledger controls include existing real synthetic financial Post facts and
unchanged snapshots; no new revocation audit is introduced.

final-plan.json records all53 full names, exact source hashes/arguments/workdir
before the one final Go command. Planned selection is not a passed result.

## Delivery and permanent nonclaims

Only after all required local gates pass: one commit/push, one new stacked Draft
PR with exact base PR99. Approved connector alternative requires complete base/
candidate tree, parent and every changed blob/mode proof, explicit local/remote
different SHA/parent disclosure and non-force exact-base lease; no trust,
credential or remote configuration change. New source then freezes; only natural
pull_request attempt1 CI and original E/F independent same-head review follow.

Retain PR99/PR98 histories, old preparation/final/tests-first reds and frozen
active-only/H2/H3 states. No GOV-02 cross-backup anti-resurrection, M1-to-M2, real
host/client, actual power-loss or production GO. Keep Draft. No Ready/merge/tag/
package/deploy, real DB/backup/Key/config/session/8787/password/client access.
