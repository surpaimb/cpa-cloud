# Employee retirement final-state candidate — current local stage

Status (2026-10-23): pending local candidate, not a new committed/transported head.
This ERF-24 batch corrects this document's stage wording only; it does not
change product/tests or authorize more execution, commit, transport, PR or CI.
The current frozen helper and test SHA256 are respectively
`AAF50C5C0787B18FFE29BF166B25445C345CBB2D48341485EB66741B87ADA720` and
`DA15DA650E2CCB939098A4528361D56DCF6787B3C25509195560DE0DAE7BCBCC`.

Accepted independent local author evidence, not rerun by this document batch:
- ERF-14: FORMAT_STATIC_PROOF_ONLY, exhaustive formatting-byte proof;
  ERF-13 remains STOP and is not retroaccepted.
- ERF-17: COMPILE_ONLY_VALIDATED, one test-package compile without execution.
- ERF-19: DIRECTED_VALIDATED_ONLY, exact new 12+69=81 names, each run/pass once.
- ERF-22: REGRESSION_VALIDATED_ONLY, exact old 13+54=67 names, each run/pass once.
- ERF-23: RACE_DIRECTED_VALIDATED_ONLY, fixed Go1.26.9/GCC16.2.0, Windows
  CGO_ENABLED=1, one noncached limited -race run of the exact new 81 names;
  each run/pass once, package pass once, no detected DATA RACE/fail/skip/extra.

Those records keep their own frozen snapshots, including the earlier contract
SHA256 `FEB0A85CCF32DF9D3FD10A3BDC1C98A659AB82F984EBE339BFAF86EB0DCEDA75`.
This document-only correction does not rerun or retarget them, supply remaining
static gates, or establish product/formal new-head E/F/production GO.
Audit confirmation remains the inherited PR95 rowid lower bound plus
actor/action/target/result check; no private operation-ID binding is added or
claimed. PR126's old green CI is not evidence for a new ERF candidate head.

All prior red/STOP/interruption records, including ERF-07/-08/-09/-11/-12/-13/
-15/-16/-18/-20, retain their original conclusions; none is resumed or washed
green. Draft / real migration / production NO-GO remains, with no
Ready/merge/tag/package/deploy.

Everything below the historical ERF-10 heading retains its original phase
meaning, including the then-current D639/unformatted/unexecuted statements
and the verbatim -03 history. It is not this batch's current authorization.

## Historical ERF-10 fixture candidate — preserved original phase

Status: new independent **test/contract-only candidate batch (-10)**,
2026-10-23. Only the two existing untracked candidate paths may change:
`internal/service/employee_retirement_final_state_test.go` and this document.
The already modified, unformatted and unexecuted product helper is frozen at
SHA256 `D63920BFC79289D6B2FFFC5C74284EBD4A42E41A5AF588B002A06D79E430BB6B`.
This batch does not edit or validate that helper, its handler/hooks/schema,
other source, dependencies or workflows.

Highest permitted outcome is **FIXTURE_CANDIDATE_READY** after static checks.
No Go, gofmt, compiler, vet, build, race, SQLite/HTTP test or service execution,
commit, push, PR/CI or monitor work is authorized here. A new independently
authorized execution batch is required before any runtime or product conclusion.
This is not a claim that the retirement P2 is fixed. Draft / real migration /
production **NO-GO** remains.

## Candidate seams, not execution evidence

The normal control and the old-oracle AuditRestore function bodies remain exact.
CommitOutcomes also remains exact: pre-Commit rejection/no wake is distinct from
the existing advisory wake on Commit error. Refused Commit, actual deferred-FK
Commit refusal, real Commit followed by controlled error, and real Commit
followed by controlled cancellation must remain distinct when later executed.
No blanket inference that every HTTP503 means zero writes or zero wake is valid.

The new private fixture seeder references real supporting model/group rows and
populates model members, source CIDRs and account-group members for an already
revoked target Key when present and for a peer Key. Ordinary live target Keys
retain their existing default policy. The peer has a nonempty selected employee
model list. No upstream is contacted; only synthetic metadata is constructed.

Reversals retains the original cases and adds seven named candidates:
policy_models, policy_source, policy_cidr, policy_group, policy_group_member,
key_name_storage_class and key_digest_real. Member deletions start from explicit
nonempty rows; header changes include the matching member cleanup needed for a
legal all/empty policy. The employee-model case now deletes a populated member
rather than adding an unrelated identifier. Nonempty baseline assertions are
required before installing each reversal trigger. BLOB-vs-TEXT with equal name
bytes and REAL-vs-BLOB digest changes test storage-class preservation, not a
count-only or digest-only terminal predicate.

LastHook adds employee, key_policy and self_session candidates at the last
mutable audit hook. The worker returns only fixed seam labels; it receives no
testing.T and never calls FailNow. A parent-owned atomic witness is set only
after successful SQL and exactly one affected row, so an unrelated hook SQL
failure cannot stand in for the intended mutation. The parent requires fixed503,
no operation wake, complete rollback facts and checked genuine mode=ro reopen
after owner close. These are synthetic trigger/hook controls, not actual I/O,
power-loss or production fault evidence.

The static future-selection inventory is 12 parent functions plus 69 child
names = **81 full names**, including 32 Reversals children and 3 LastHook children.
It is not a run/pass count, Go syntax/compile proof or authority to launch that
selection. Formatting, exact per-name noncached runs, selected legacy/race,
vet/build and new formal same-candidate review remain later separate gates.

## Preserved history and review boundary

- -01 combined identity failure remains UNKNOWN; -02 discovery STOP is unchanged.
- -03 is fixture-only evidence; -04 is OLD_PRODUCT_CONTROL_ONLY.
- -05 remains empty; -06 remains preparation STOP with no Go invocation.
- -07 is OLD_PRODUCT_ORACLE_EVIDENCE_ONLY for the frozen old source, not this
  product candidate. Its terminal label/source binding and nine native events
  are not independently emitted per-milestone HTTP traces.
- -08 stopped at truncated preparation output/JSON parsing, before product edit.
- -09 stopped at test patch context verification after the partial helper edit;
  no formatting, Go/test or commit ran. Its original/stopped snapshots and all
  STOP records remain frozen; -10 neither reuses nor resumes those directories.
- Root-reported independent E/F source-only review of the -09 draft is not
  compilation/runtime acceptance, a product GO or production approval.

The complete earlier -03 document below is retained verbatim as historical scope
and independent requirements. Its old frozen hash, fixture-only prohibitions and
future-tense product blueprint are not the current -10 authorization. Old source
and CI reds, GOV-02, M1-to-M2, custody/consumer and production gaps stay open.

---

## Historical -03 document (verbatim)

# Employee retirement: pre-Commit final-state confirmation

Status: new independent **fixture-only correction/admission batch (-03)**,
2026-10-23. Only the new untracked test and this document may change. The
product helper stays frozen at SHA256
`B1D274045BD5D0EDA0959A7E91D254F78A0A497AB2A099B08FD186B1AABCAC2A`.
No retirement business/old oracle/product gate or commit/transport is authorized
by this fixture batch. Highest possible acceptance is FIXTURE_VALIDATED_ONLY;
Draft / real migration / production NO-GO remains.
Base PR126 remote `cb5444e77a533345f2e617876a027cde69b1632f`, sole parent PR125
`0d7c167106e8f5c39bf8cf06226491a2a6e98bad`, complete base tree
`a642b2578cfbdc10512d0243020a73133ab03020`. Local equivalent base commit is
`8dd0e4f5e6702a460c539f1f4d610f94f7def45d`; ancestry/metadata are not identical.

## Independent source / future three-file product boundary

Independently authored from this project's [retirement contract](employee-credential-retirement-contract.md),
[preview API](preview-contract.md), [independent implementation rules](independent-implementation.md)
and the coordinator's bounded terminal-confirmation requirements. No archived
CPA, CLIProxyAPI or Sub2API source/tests/schema/docs are templates. Earlier
exposure prevents a strict clean-room claim. No new dependency or protocol;
existing dependency licenses and [third-party notices](../THIRD_PARTY_NOTICES.md)
remain applicable.

In a separately authorized future product batch, only `retireEmployeeCredentialsTx`
and necessary operation-private fact readers
in `internal/service/employee_retire_credentials.go`, a new independent
`employee_retirement_final_state_test.go`, and this incremental document may
may change. This is an implementation blueprint, not current product-edit
authority or acceptance. Handler/parser/error classes/revision ceiling/hooks interface and stages,
shared lifecycle audit, routes/auth, schema/migrations, other mutators, old tests
and old contracts remain frozen. Stop before a fourth repository file is needed.

## Preserved STOP history / exact current identity

The original -01 batch stopped in its first old-control child with
`ERF_NATIVE_SQLITE_IDENTITY`, native exit1 and empty stderr. It never established
the old positive control or expected business old red and never edited product.
Its combined short-circuit label did not record runtime version/FK values.
Original test SHA256 was
`3B6ABB47814047E582B70CC5CC99DDA23306F856A3DEA0EE6BCE3F56A42E8C65`;
its stdout SHA256 remains
`86CD3DEE19C465362A2F10C3624BE9E1970BA26E62936352C16389E1E203A286`.

The separate -02 fixture-only attempt stopped before edits/execution when its
presumed `*windows_amd64*` source-discovery glob found no required generated
file. It did not prove a missing module or run a new SQL/Go control. Both STOPs
and their raw/returned-tool evidence stay frozen, not resumed or retroaccepted.

This -03 batch uses the exact read-only generated `sqlite_windows.go`, not that
failed glob. Its build tag is `windows && (amd64 || arm64)`, length10466376,
SHA256 `F5C1BBA7A82EA2DCAA956B04A5AA8CDC2042065A62C3CA91D453DF834A3F75D5`.
The unchanged module/go.sum and [PR97 compatibility contract](sqlite-wal-reset-compat-contract.md)
pin modernc.org/sqlite **v1.46.2 / SQLite3.51.3**, numeric version3051003.
The complete expected source ID from that exact driver source is:

`2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6d618`

The old hardcoded 3.50.3 is statically incompatible with the fixed current graph;
this does not retroactively identify the old combined runtime/FK failure branch.
No arbitrary version, prefix-only source ID, or removed FK gate is accepted.

## Current -03 fixture-only admission gates

Read native sqlite_version()/sqlite_source_id() and PRAGMA foreign_keys through
one explicitly held physical sql.Conn before App/HTTP publication. Separately
return only fixed safe labels for identity-query error, runtime-version mismatch,
full-source-ID mismatch, FK-query error and FK OFF. Do not echo err.Error, SQL,
observed values, credentials or paths. Release/check that Conn before publishing
the single fixed DB owner; no App.Open, worker Start or live DB replacement.

Freeze these fourteen full names before the sole noncached serial fixture run:

- TestEmployeeRetirementFinalFixtureIdentity and native_on, version_mismatch,
  source_mismatch, identity_query_error, foreign_query_error, foreign_off_real.
- TestEmployeeRetirementFinalFixtureAuthentication and correct_secret,
  wrong_secret, missing_authorization.
- TestEmployeeRetirementFinalReadOnly and existing_no_write, missing_no_create.

The wrong version and last-source-ID-digit controls are explicit test-local
returned-value simulations after a real native read, not a swapped engine or I/O
fault. Query-failure controls use a missing SQL function on the same owned Conn;
the helper must classify/reject the actual SQLite query error without echoing it.
FK OFF is an actual PRAGMA on an exclusive unpublished physical Conn, followed
by rejection and explicit FK ON restoration/revalidation on that same Conn.
Each identity control starts/ends at the exact native identity/FK ON baseline.

Authentication uses actual production employee Key/digest/policy authentication
through a test-only HTTP probe (204 success,401 rejection), not model/upstream
execution. Correct secret, wrong secret and genuinely absent Authorization must
execute; all database facts/notification generation remain unchanged and no
retirement audit/business operation occurs. Real administrator login only sets
up the fixture. Owner/Conn/DB Close results are checked.

The read-only controls use genuine canonical file: URI mode=ro without immutable;
query_only OFF still cannot write (primary READONLY=8). A missing mode=ro file
returns CANTOPEN=14 and creates no DB/sidecar. No retirement HTTP positive,
AuditRestore/old oracle, reversal/target matrix, regression or race runs here.

Use fixed absolute Go1.26.9 and offline -count=1 -p=1 -parallel=1 -failfast,
Go timeout6m / wall420s. Seal native stdout/stderr/exit/package/cleanup before
separate exact per-name parsing: 3 parents+11 children=14 run/pass once,
0fail/skip/missing/unexpected/incomplete/parse/runtime/cleanup error. Tool identity
and mechanical formatting are preparation only. Source/module/tool/runner and
all previous evidence hashes remain frozen. First unexpected error stops without
hot repair/rerun; success is fixture-only, never product GO or old-gate acceptance.

## Same-transaction baseline, expected transformation and final proof

The remaining product sections are future requirements, not work executed or
authorized by this -03 fixture-only batch.

Keep the initial employee validity/not-found/revision-conflict checks in their
existing order. After matching the requested revision but before the first
write, capture complete typed rows in the caller's single write transaction.
Do not read through the parent DB, open a second connection or infer the expected
state from the writes' observed result. Build an independent expected snapshot
from the pre-write facts and the operation's supplied UTC stamp.

The protected row sets are the complete employees, access_keys,
employee_self_key_slots, employee_self_credentials, employee_self_sessions,
employee_models, access_key_policies, access_key_policy_protocols,
access_key_policy_models, access_key_policy_sources, access_key_policy_source_cidrs,
access_key_policy_account_groups and access_key_policy_account_group_members.
All rows/columns, including peers, participate; key/slot ownership cannot escape
the final predicate by being reassigned. Ordering is explicit and stable.
SQLite NULL/TEXT/INTEGER/REAL/BLOB values remain distinct; do not collapse NULL,
empty values, storage types or binary data into a count/digest/revision-only
projection. Driver byte buffers are copied before advancing Rows. Check Columns,
Scan, Rows.Err and Close, including early error paths; private facts are never
logged or exposed in HTTP errors.

Exactly these differences are allowed:

1. Target employee status becomes disabled and revision increments exactly one;
   identity/name/department/note/model_mode/created_at and every peer stay exact.
2. The exact target Key set remains. Each previously NULL revoked_at becomes the
   supplied stamp, including expired/dormant/slot Keys. Preserve every already
   non-NULL revocation time byte-for-byte, plus IDs, ownership, selectors, digest
   bytes/version, operation ID/fingerprint, expiry, name, creation and raw policy
   rows/revisions/members. No inserted, missing, reassigned or foreign Key is
   silently accepted.
3. Validate target-slot and reverse Key ownership against pre-write facts. A
   foreign slot referencing a target Key or target slot referencing a foreign or
   absent Key fails closed. At most one related slot exists. Pending/armed becomes
   cancelled, with NULL arm fingerprint and all arm revision fields, and this
   stamp as cancelled_at. Preserve all other columns/history. Issued/cancelled
   tombstones remain completely unchanged; their Keys still participate above.
4. Remove all target self-session/credential rows, including password/enrollment
   facts; preserve peers and every other protected row. Preserve employee-model
   and Key policy rows exactly.

Keep all original writes, affected-row validation, hook calls and the shared
audit/new-retained-event check. Only after audit insertion/pruning and the final
audit hook, re-read every protected fact in the same transaction and compare with
the precomputed expected snapshot. Any read/type/iteration/Close/set/value
mismatch returns the existing storage_unavailable failure. After this last proof,
there is no write or mutable hook before returning to the original handler's
sole Commit. This confirms the named local transaction facts, not arbitrary
hostile-schema authentication or global tamper-proof audit completeness.

## Compatibility and Commit exception

An already disabled employee is eligible, not an idempotent no-op: with the
current revision it still increments and audits; the old revision returns 409.
Preserve zero-Key behavior, safe ceiling, strict input/auth/Origin/CSRF, ordinary
PATCH/K04 and future Key creation semantics. Explicitly re-enabling an employee
must not revive Keys retired by this operation; ordinary unrevoked PATCH controls
retain their existing behavior. No replay receipt or automatic retry is added.

A helper/final-proof rejection precedes Commit: fixed 503, no operation wake,
and unchanged persistent facts must be demonstrated, not inferred from 503.
Confirmed Commit permits 200 and the existing account-pool advisory wake. The
frozen handler also wakes on Commit error: a real Commit followed by a controlled
error/cancellation may have persisted everything despite 503. A refused Commit
and an actually committed uncertain result must be recorded separately; old
revision 409 follows only the latter. Do not assert all 503 responses imply zero
writes or zero wake. Adapter returns are explicit simulations, not physical I/O
or power-loss injection. Real deferred-FK Commit rejection is a separate case.

## Future business tests-first proof (not executed by -03)

Use one temporary real SQLite DB, the actual store/Key-policy/self migrations and
real administrator password/session login. Construct a workerless App bound once
before HTTP publication; no App.Open/Handler, worker Start or live DB replacement.
Register minimal production handlers/requireAdmin(true), real Origin/CSRF and
real employee Key authentication. Assert valid ordinary unexpired Key success
and wrong/missing Key failure before any old oracle. No real provider/8787/data.
Check native SQLite version and foreign_keys. Capture operation notifications
before shutdown; cleanup cancellation is not an operation notification.

Before changing product, establish a genuine successful retirement control, then
a target-scoped native audit AFTER INSERT trigger that preserves the new audit
but restores the target's active status/old revision and an ordinary unexpired
Key's revoked_at=NULL. Expected old red requires real HTTP200, retained new audit,
actual old-Key authentication again, owner Close and persistent corrupted facts
on a fresh genuine read-only reopen. Fixture/auth/409/cleanup errors are not old
red. Freeze the exact old product/test identities and fixed red label separately.

The reopen uses a canonical absolute file: URI with mode=ro (not immutable or an
ordinary filename/query_only substitute). With query_only OFF, an actual write
must still fail with primary READONLY=8. A mode=ro missing-file open must fail
with CANTOPEN=14 and create no DB/sidecar. Check Rows and DB Close.

New-state gates cover the old reversal and legal employee/Key/policy/slot/self/
peer reversals, unchanged facts after pre-Commit 503, notifications, zero/multiple
Keys, preserved old revocations, all four slots, active/disabled/current/stale
revision, same-revision two-request barriers and ordinary PATCH/K04. Exercise
Commit refusal, real deferred-FK rejection, and real Commit followed by controlled
error/cancellation distinctly. HTTP workers never receive testing.T/FailNow and
always deliver buffered outcomes before parent join under fixed bounds.

## Evidence, acceptance and handoff

Fix the absolute Go1.26.9 Windows executable/hash; offline, noncached serial
SQLite gates. Freeze exact target, old-red, related regression, selected CGO-race,
format/vet/build/link/diff names/argv/environment/time budgets before execution.
Seal native stdout/stderr, exit/timeout/package outcomes and source/tool identity
outside the repository before separate per-name parsing. Expected old-red exit1
is bounded explicitly; every other unexpected error/fail/skip/missing/cleanup or
tool refusal stops without repair/retry. Never use PATH discovery or cached green.

Only after all declared gates pass may one local three-file commit be made.
Check unique parent/tree/blobs/100644 modes/clean. If local parent SHA differs
from the exact PR126 remote head, stop before transport for new root authority;
same tree is not same ancestry and does not authorize a blind push. New remote
CI and original E/F same-head formal review are separate future evidence.

Old Code513-515 red, PR121 five old T cases, UNKNOWN/CAPACITY_UNPROVEN, M1-to-M2,
53 consumers, GOV-02 across old backups, employee C/three-platform host/custody
and real migration/production gaps remain open. No provider revocation, physical
erasure, backup anti-resurrection or production acceptance follows. Keep Draft /
production NO-GO; no Ready/merge/tag/package/deploy.
