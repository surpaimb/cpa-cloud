# Administrator single-Key revocation transaction audit

Status: independently authored bounded development-preview increment, 2026-10-22.
Draft / real migration and production NO-GO. This contract is written before
the tests-first observation and implementation; acceptance below is a required
gate, not a claim that it has already run.

## Source and four-file boundary

The independent starting point is PR98 head
`d96e99ebef5942db1b585ff0042efc0872c67eca`, parent PR97
`2bd3c03d162476ec5616efd00945c4a58c45a076`, tree
`69118d96263639f80084bfb9746c5e283b064348`. D2 is already present and is not
implemented again. No later branch's source, tests or green evidence is imported.

Exactly these repository paths are permitted:

- `internal/service/employees.go`, only the existing `revokeKey` region;
- new `internal/service/admin_key_revocation_audit_test.go`;
- new `internal/service/admin_key_revocation_audit_commit_test.go`;
- this contract.

Shared authentication, slot/audit helpers, routes, schema/DDL, dependencies,
workflows, backup code and old tests are frozen. A fifth file or a shared
behavior change requires stopping for new authority. New tests/source derive
from this contract and CPA Cloud's own interfaces, not archived CPA,
CLIProxyAPI, Sub2API or adjacent reference implementations. Prior source
exposure is disclosed: this is not represented as strict clean-room work.
Existing dependencies and their licenses are unchanged.

Relevant own specifications: [preview API](preview-contract.md),
[self Key issuance](employee-self-key-issuance-contract.md),
[retirement transaction precedent](employee-credential-retirement-contract.md),
and [retained audit view](admin-audit-overview-contract.md).
Public semantics checked during preflight: [Go database/sql](https://pkg.go.dev/database/sql),
[Go driver interfaces](https://pkg.go.dev/database/sql/driver),
[SQLite changes](https://www.sqlite.org/c3ref/changes.html),
[SQLite triggers](https://www.sqlite.org/lang_createtrigger.html), and
[SQLite transactions](https://www.sqlite.org/lang_transaction.html).

Two earlier preparation errors remain historical failures: an unquoted
PowerShell `^{tree}` identity command, and a read-only search naming a nonexistent
`self_key_slot_issue.go`. Neither changed files or executed a Go test. This
independent continuation uses the verified real file inventory and exact tree;
it does not retroactively turn those errors into successful commands.

## Frozen HTTP and authority

Only existing `POST /admin/api/v1/keys/{id}/revoke` is affected. Authentication,
Origin/CSRF, JSON parsing and existing fixed errors remain unchanged. A confirmed
success remains HTTP 200 `{"ok":true}`; an absent Key remains 404. No employee
or API-Key credential becomes administrator authority. The actor is exclusively
the real `adminSession.AdminID` supplied by the existing middleware, never a
Key/employee ID, body field, plaintext, digest, cookie or default username.

No future-Key creation permission, self-service grant, ordinary K04 policy,
worker, other employee or model-request behavior is added. This operation does
not retroactively terminate already dispatched requests.

## Transition-only event policy

One request writes exactly one retained event if and only if this transaction
actually first revokes the target Key and/or cancels its pending/armed slot:

| Before | Confirmed result | New event |
| --- | --- | --- |
| unrevoked ordinary/issued/cancelled Key | revoked; slot unchanged | one |
| pending/armed slot | Key revoked and slot permanently cancelled | one |
| already revoked Key with pending/armed slot | slot actually cancelled; original revocation preserved | one |
| already revoked, no mutable slot | original timestamp and slot preserved; idempotent success | none |

No duplicate is added for a retry, concurrent loser, restart or an event pruned
from the retention window. There is no backfill or all-attempt log. This is
current-database-lineage idempotency, not an external operation receipt, a
cross-backup once-ever promise, or protection against direct database rewriting.

Event source is existing `account_lifecycle`, action `access_key.revoke`,
target_type `access_key`, target_id the existing Key ID, result `succeeded`.
Only the existing event ID, actual administrator ID, fixed metadata and UTC time
are stored. There is no request body, credential, token/digest, SQL error or
provider payload. The existing reader exposes this metadata without changes.

## Single transaction and final confirmation

Retain the existing `admission.Lock` and one SQLite transaction. Read before
state, perform the original Key/slot mutation, then call the existing
`recordLifecycleAudit` in that same transaction for a real transition only.
RowsAffected=1 alone does not establish a transition or final state.

The final in-transaction checks, after all mutation/INSERT/trim work, must
confirm the target Key is still revoked, its original owner and immutable
credential identity are preserved, its original revocation value is preserved
when already revoked, and the expected slot/tombstone remains. Unused-slot
cancellation clears the existing arm fingerprint/revisions and sets its existing
cancelled timestamp; issued/cancelled/no-slot facts do not become grants.
Checks must reject ignored or reversed writes rather than publish a false
success. These checks cover the contract's owned adversarial SQL fixtures, not
arbitrary malicious triggers or universal database integrity.

The writer's existing global lifecycle retention remains at most 1000 rows,
shared with other lifecycle events, ordered by its existing timestamp/id rule.
This is not a per-Key cap, a time retention guarantee, forensic storage, or a
new cap on other audit sources. The operation's own new matching event must
survive trimming and total retained rows must meet the cap. If clock ordering,
an ignored write or a trigger removes that event, fail rather than commit an
unaudited transition. Existing legitimate pruning is the only permitted change
to unrelated retained lifecycle facts.

Only an explicitly successful Commit permits the original notification and
HTTP success. Begin/read/write/scan/row-count/confirmation/INSERT/trim failures
return existing fixed 503 without exposing internals. Missing initial target
uses existing 404. Never automatically retry or make a second Commit. A Commit
error is not proof of rollback: an error returned after a real successful Commit
can coexist with both durable revocation and audit. Such a response is still
503; the existing handler's notification-on-confirmed-success semantics remain.
A subsequent independent retry sees committed state and adds no second event.

## Required independent tests-first and acceptance

Before implementation, a real administrator HTTP revoke on exact old PR98 must
confirm the Key became revoked while the new event is absent, failing solely
with fixed `ADMIN_KEY_REVOKE_AUDIT_MISSING`. This is an intentional local old-path
red, not a new CI red or an inherited later-branch failure.

Actual tests-first observation on the unchanged PR98 production source:
Go1.26.9 Windows, offline dependencies, serial non-cached single named test;
the HTTP/SQL prerequisite assertions passed, and only
`TestAdminKeyRevocationAuditFirstTransition` failed with exactly one
`ADMIN_KEY_REVOKE_AUDIT_MISSING`, no other `AKRA_` label and no JSON parse error.
Exit was 1. Captured JSON stdout (LF-joined lines) SHA256:
`c0d547d96a74c2b1cea8a5e34827412b412c023c3a849c93635651f07ceeb453`.
No raw credential, SQL error or fact dump is published.

All fixtures own temporary SQLite directories, synthetic secrets and dynamic
non-8787 loopback HTTP. Assertions never print secrets, headers, SQL errors or
fact dumps. Required tests include:

- ordinary and all four slot states; already-revoked unused slot and terminal
  retry; real administrator identity and existing audit HTTP reader;
- authorization/Origin/CSRF/body/missing-target rejection without new facts;
- concurrent requests, restart, and both issuance-versus-revocation orderings;
  the slot tombstone and unrelated employee/Key facts remain intact;
- real SQL INSERT/trim aborts and ignored/reversed Key, slot or audit writes,
  complete logical rollback comparisons, retention 999/1000/1001 and a new
  event evicted by future-dated facts, without backfilling a pruned event;
- an actual deferred-FK Commit refusal versus a test-only driver adapter which
  returns error after a real Commit, cancellation and fixed 503/no retry;
- synthesized canary exclusion from audit response/events and error responses.

Test-only driver/context adapters, if used, live entirely in the two new test
files and are described as controlled boundary simulations, not physical I/O,
real entropy failure, power loss or a database-driver root cause. No production
hook or shared fixture/driver alteration is authorized.

Windows SQLite tests run serially (`-p=1 -parallel=1`) and non-cached. Targeted
tests, build, vet, gofmt, local document links, exact four-file/single-handler
diff and `git diff --check` are gates. First unexpected test red, substantive
contract failure, dependency-download requirement or natural CI red stops the
batch with evidence; no hot patch, manual rerun or dispatch.

Only all local gates permit one commit/push and an independent Draft PR stacked
on exact PR98. Freeze source after that commit. New-head natural pull_request
attempt1 Code, native-six, CLI-VM and independent-VM evidence must be pinned and
counted from complete checks, not borrowed or pre-assumed; skipped is not
execution. The root arranges original E/F independent exact-head review. CI is
not that review, and neither grants production GO.

GOV-02/old-backup anti-resurrection, old Code513/514/515 reds, real M1-to-M2,
all-consumer coverage, real credentials and deployment acceptance remain open.
No Ready, merge, tag, package or deploy is authorized.

## Actual local gate record before the sole candidate commit

- Fixed Go1.26.9 Windows executable, `GOTOOLCHAIN=local`, `GOPROXY=off`,
  `GOSUMDB=off`, `GOFLAGS=-mod=readonly`; no dependency download.
- New targeted matrix: 12 main + 41 named subtests = 53 run/pass once each,
  exit 0, 0 fail/skip/JSON parse errors, no failure labels, 105.4145708 seconds.
  LF-joined captured JSON stdout SHA256:
  `7c46804c42702df5642f67a996abe84189bc521e253e8a6349a48b6bdee018b7`.
- Existing preview workflow, self-Key revocation and slot targeted regression:
  27 named run/pass once each, exit 0, 0 fail/skip/JSON parse errors. Stdout SHA256:
  `f9557d4e6a80bf604dd1f99ee84a513c571250e198c2f68c1acc09e0152b588a`.
- `go mod verify`, build of service and both CLIs, service vet and gofmt checks
  all exit 0. Four local document links resolve. The normalized production
  source prefix and suffix outside `revokeKey` are unchanged.
- Tests include real SQLite deferred-FK Commit refusal and its positive control.
  Controlled after-real-Commit error/cancellation return 503 with both facts
  persisted and no notification; a separate successful request adds no event.
  Begin-boundary cancellation is controlled; this does not prove timely
  row-step cancellation, real I/O/power-loss behavior or a driver root cause.
- Only the revoke-wins issuance case overlaps real requests at the existing
  before-transaction barrier. The issue-wins case first confirms real issuance
  and then revokes it; it is not advertised as a second concurrent ordering.
- No local race, full-repository Windows run, provider/real credential test or
  new-head remote CI/review is claimed. The original tests-first red remains
  above, and no old green/NO-GO conclusion is overwritten.

The subsequent SSH read-only base-ref preflight returned exit 128, without a
confirmed remote ref. Its underlying transport cause is not established here.
No trust/credential/remote setting was changed and no retry or substitute
transport is claimed. This local record does not claim a push, new PR or CI;
those require the root's transport disposition and exact remote verification.
