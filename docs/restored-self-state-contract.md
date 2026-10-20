# Restored employee self state — D2

Status: independent bounded implementation candidate; Draft / production NO-GO.

## Frozen source and provenance

The remote starting point is PR97 `2bd3c03d162476ec5616efd00945c4a58c45a076`,
tree `e913134807f743dcfc99a41f274ea45453cf4128`, parent
`2e33c8cf00266770b825241448f75aff954c3370`. The approved local starting commit
`27038d890e10bb1ec6e54fce1848c2416c10c8f6` has that exact tree and parent, but
different commit metadata. The initial direct Git fetch did not complete; the
remote object was absent locally. This was a transport prerequisite, not a source
or test failure. No metadata reconstruction candidate was accepted or written.

The new remote branch must start at the exact remote PR97 head. Its sole new
commit must have that remote parent, the complete local candidate tree and every
changed blob/mode/file exactly. Local and remote commit SHAs need not be equal;
their different parents must never be represented as equal. PR97 stays frozen.

Implementation and tests are independently authored from this contract and the
existing independently authored self foundation and backup behavior. No archived
CPA implementation or external implementation template is used. This is not a
strict clean-room claim. No dependency, license, codec, workflow or pin changes.
Public API references: [SQLite schema](https://sqlite.org/schematab.html),
[PRAGMA](https://sqlite.org/pragma.html), [foreign keys](https://sqlite.org/foreignkeys.html),
and [Go database/sql](https://pkg.go.dev/database/sql). Existing dependency notices
remain in [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES.md).

## Only product effect

All three actual Restore formats use the existing staging database sanitizer.
In its existing single transaction, before publication, delete **all** rows from
`employee_self_sessions` and set both `enrollment_digest` and
`enrollment_expires_at` to SQL NULL in `employee_self_credentials`. This is
independent of the self-service feature flag. Keep credential rows, password hash
bytes, `updated_at`, all employee/API-Key facts, all Key-slot states, money,
ledger facts and revisions unchanged. Existing admin/OAuth/refresh cleanup stays.

No new migration, object creation, schema repair, root switch, Key creation or
revocation algorithm is included. Startup Open must not conceal a failed gate.

## Fail-closed pre-DML admission

On the same SQLite connection verify foreign_keys=ON **before** BeginTx. Inside
the sanitizer transaction, before any cleanup DML, enumerate main.sqlite_master
and required PRAGMAs completely, checking Scan, Rows.Err, Close and context.
Only these profiles are admitted: all three reserved objects absent; or exactly
the frozen two tables and expiry index. The three original DDL strings have one
dependency-free authority in internal/selfschema; service creation/migration DDL
bytes and behavior stay unchanged. Only outer whitespace is ignored, exactly as
in the existing service gate; no quoted-NULL or general SQL normalization.

Use SQLite ASCII NOCASE discovery then exact original name/type/owner/DDL and
column/PK/automatic-index/user-index/FK facts. Reject six partial profiles, case
aliases, weak constraints, views/virtual objects, wrong or additional user
indexes, triggers on either self target or any existing sanitizer DML target,
and every inbound FK to either self target throughout main (case variants,
quoted names, empty child tables, CASCADE and SET NULL included). The all-absent
profile still rejects dangling inbound references. Preserve PK automatic indexes
only as exactly expected. No statements are tried as schema probes.

## Transaction and publication boundaries

Gate, old cleanup and D2 cleanup are in one existing transaction. Validate D2
postconditions before Commit. Cleanup/read/validation/Commit errors, uncertain
Commit outcomes and observed pre-publication cancellation never return success
or proceed to publication, and never automatically retry. A Commit error is not
proof of rollback. Staging commit and directory publication are distinct
linearization points. An already valid published v3 target may coexist with a
cleanup error; any error does not imply target absence. Late cancellation does
not prove timely zero writes/no target. Production link rejection is unchanged.
PR97's original row-step timely-cancellation FAIL remains open and is not
reclassified by this work.

## Acceptance and evidence taxonomy

Tests first should witness the old Restore accepting both the unexpired old
self cookie and valid unconsumed enrollment, using only owned synthetic fixtures.
Each v1/v2/v3 package must actually Restore. Inspect restored DB before first
Open, then use actual App HTTP on random loopback: old source cookie initially
200; restored old cookie 401 with no new cookie; snapshotted enrollment 401 with
no new password/session; source redeem after snapshot is its positive control;
old password login/new-cookie profile succeed; admin can issue new enrollment;
valid old API Key actually lists models, revoked Key remains rejected; old admin
session fails and fresh login works. Compare complete ordered typed row/NULL
facts, including slots, monetary/ledger/revision controls, not just row counts.
Source database/package facts must remain unchanged by Restore.

v2 synthetic KeyMaterial proves codec/Restore/HTTP, **not** Windows DPAPI custody.
v3 uses real synthetic installation credential/binding/canary validation and the
corresponding service.Open, not an unconditional validator or a v1 downgrade.
Linux v3 acceptance must actually execute in natural CI; skip/unavailable is not
PASS. Windows SQLite tests run serially, not a parallel repository-wide run.

Adversarial schema, partial writes, real SQLite Commit refusal, controlled
post-real-Commit error, cancellation and reopen facts must be identified
separately. Adapter injections are not actual I/O faults. Required regression:
backup, financial, self auth; targeted vet/build, gofmt, links and diff checks.
First unexpected required local/natural CI red freezes this batch: no hotfix,
rerun or dispatch. Intentional tests-first old-behavior red is separate evidence.

## Budget and non-goals

Seven baseline files, hard maximum nine: this document, internal/selfschema/schema.go,
internal/service/self_store.go, internal/backup/backup.go,
internal/backup/backup_test.go, internal/backup/backup_app_test.go,
internal/service/backup_key_provider_binding_linux_test.go. Only the two expressly
approved test-only fixture files may be added if necessary, with reason recorded.
No third production edit, publishing-algorithm change or scope expansion.

GOV-02/old-backup anti-resurrection is **not** implemented: old bcrypt, API Keys,
slots and disabled status can still reappear from snapshots. No real DB, backup,
credential, port 8787, provider or official client is touched. Source/synthetic
tests and CI are not independent E/F review, real migration or production GO.
One local commit and one controlled connector transfer/new Draft PR only after
required local gates. No Ready/merge/tag/package/deploy.

## Execution record

The contract was written before implementation. Actual local evidence on
2026-10-20, Go 1.26.8 Windows, serial `-p=1 -parallel=1`:

- Tests first: old actual v1 and v2 Restore both accepted the unexpired cookie
  and snapshotted unconsumed enrollment; first-Open DB was not clean. Fixed
  witness `D2_OLD_STATE_ACCEPTED`, both booleans true. This intentional old-path
  red is not an unexpected post-fix gate failure and is not a v3 old-path claim.
- Final D2: 5 main + 55 subtests PASS, 0 fail/skip. Schema admission covers 39
  negative profiles plus all-absent/exact-complete positives, including empty
  and populated case/quoted inbound-FK controls. Four slot states and nonempty
  real financial ledger facts are preserved by ordered storage-typed comparisons.
- Full backup and financial regression: 98 main + 152 subtests PASS, 0 fail/skip.
  That run preceded the final four extra populated-FK subtests; final D2 above
  separately executes those added controls and profile-owner assertions.
- Existing self-auth regression (service/enrollment/schema/login/password/
  sign-out and selected retirement names): 17 main + 25 subtests PASS, 0 fail/skip.
- Targeted Windows vet/build, Linux service vet and Linux service test-binary
  cross-compilation PASS. Cross-compilation is **not** Linux test execution.
  New actual v3 Restore/Open/HTTP still requires natural Linux CI execution.
- The three extracted DDL strings match the original service literals exactly;
  gofmt and diff checks pass. The document's one relative link resolves; four
  public reference links are recorded separately, not credential-scanned.
- Real SQLite deferred-FK Commit refusal and reopen facts are distinct from
  injected metadata/query/Rows/Close/partial-write/post-real-Commit errors.
  A real successful staging commit followed by an adapter error or observed
  cancellation is explicitly **not** called a rollback or real I/O failure.

No local race, native Linux execution, real credential/DPAPI custody, physical
power-loss, backup-external revocation, M1-to-M2 or production acceptance is
claimed. No unexpected local required gate has failed. Natural CI and original
independent E/F review of the new exact remote head remain separate evidence;
neither is fabricated from parent green runs. The original PR97 timely row-step
cancellation FAIL and GOV-02 remain open. No optional fixture file was needed:
exactly the seven baseline files are changed.
