# Administrator upstream archive audit retention

Status: independent fixture-correction-only batch, 2026-10-23. The prior stopped
implementation batch is not resumed. Product draft SHA256
`2F3BB16C48A528FB31CE833F35502923C68027E8A54152DFD1B598F0D3F9BF5C` is frozen.
Only this document and the new dedicated test may change in the current batch.
The implementation blueprint below is not product acceptance, CI, formal review,
permission to commit/transmit, or production readiness.

## Source and scope

Independently authored from CPA Cloud's [lifecycle contract](account-lifecycle-management-contract.md),
[preview API](preview-contract.md), [independent implementation rules](independent-implementation.md),
and [feature plan](feature-parity-plan.md), not archived CPA/CLIProxyAPI/Sub2API code.
Prior exposure prevents a strict clean-room claim. No new dependency or protocol.
Existing Go/modernc SQLite dependencies retain their licenses and notices.

Base: PR125 remote `0d7c167106e8f5c39bf8cf06226491a2a6e98bad`, tree
`0f94843edf7b2f6c44782377f49b7c6d8d0eed5a`. The local equivalent is
`c392079f57ae436f4a81b9587849dc0d1f1375ab`; metadata/ancestry are different.
The earlier implementation blueprint allowed only `archiveUpstream` in
`internal/service/account_lifecycle.go`, a new
`admin_upstream_archive_audit_test.go`, and this document may change. Shared
audit helpers, other mutators, routes, App fields, schemas, real plan hooks,
dependencies, workflows, old tests, PRs, and evidence remain frozen.

## First archive transaction

Real administrator session, required same Origin and CSRF, positive revision,
Codex account mutation lock, admission write lock, and one SQLite write Tx remain.
Any unarchived default/pool model reference blocks with 409 even if disabled.
Already archived returns 200 `already_archived` before revision comparison, with
no new audit, Commit, invalidation, notification, or cancellation.

For a first transition, snapshot complete upstream/association facts and, when
the production SQL hook is wired, complete scheduled/channel plan facts. Keep
the target's non-secret identity and unrelated rows; clear ciphertext (empty
BLOB), key version and verification, retaining Codex operation identity and
setting only Codex credential state to `reauth_required`. Delete exactly the
target's OAuth binding, refresh state, proxy binding, and recovery state.
Cooldowns, proxy objects, model/pool rows and run history are not these four links.

Execute the existing SQL-only plan functions, not replacement SQL or a nil stub.
All target plans with NULL archived_at (including disabled ones) become disabled,
revision+1, next_run NULL, with the actual admin and archive time. Their archived_at,
other fields, already archived plans and every peer plan remain unchanged.

Generate a private audit ID absent before this operation. INSERT must affect one
row. After the existing newest-1000 trim, exactly this ID, actual AdminID,
`upstream.archive`, `upstream`, target, `succeeded`, and generated UTC timestamp
must remain; an old matching record cannot substitute. Check the bounded count.
Then verify the complete expected upstream, four association sets and plan sets
in the same Tx, including peer preservation. Handle row iteration and Close
errors. No writes follow these final reads before the sole Commit.

Only confirmed Commit success permits HTTP200, catalog invalidation, account-pool
generation change, and the real scheduled/channel target cancellations/wakes.
SQL/IGNORE/retention/final-state/hook/Commit errors return fixed 503, with no
success signals. Demonstrate uncommitted facts rather than infer rollback from
the response. Do not change other endpoint semantics or add retry behavior.

## Tests-first and fault evidence

Use only temporary real SQLite, real migrations, a fixed DB owner assigned before
App publication, actual login/requireAdmin(true), and non-nil
newCodexRefreshCoordinator without Start. Construct workerless typed notification
receivers that execute original methods. Observe success signals before owner
Close; cleanup cancellations are not operation signals. Never App.Open, replace a
live DB, start a worker, or access a real credential/provider/DB/8787.

First prove an independent genuine DELETE200 control, including complete archive,
credentials, four populated Codex links, real plan effects, audit and signals.
Only then install target-scoped audit IGNORE, 1000 future records, or insert-then-
delete faults. On old product, each expected red requires real HTTP200, complete
archive/cleanup/plan effects, post-Commit target signals, and the exact INSERT's
captured private ID absent after owner Close/fresh read-only reopen. A 409,
fixture failure, nil lock, fake hook or cleanup cancellation is NOT the old red.

The corrected reopen uses a canonical absolute `file:` URI with `mode=ro` and
the fixed modernc driver. It does not use an ordinary filename, `immutable`, or
query_only as a substitute. After explicitly setting query_only OFF, an actual
CREATE must fail with primary SQLITE_READONLY (8). Opening an absent file must
fail with SQLITE_CANTOPEN (14), leaving no database or sidecar; Rows and DB Close
errors are checked. URI semantics follow the [SQLite primary documentation](https://www.sqlite.org/uri.html)
(read 2026-10-23); Windows drive paths are encoded as absolute URI paths.

Concurrent HTTP workers receive no testing.T and cannot FailNow. A return-only
HTTP helper reports reply/error; two buffered deferred deliveries precede worker
Done, and the parent waits for both exits before asserting. Context-bounded HTTP
requests and controlled one-error/two-error negative controls verify delivery,
unchanged facts when both requests fail, and bounded cleanup. This controlled
transport error is a fixture negative control, not a product I/O failure.

Candidate cases cover ordinary/Codex, enabled/disabled, no-op and concurrent dual
DELETE; auth/Origin/CSRF/revision/not-found/references/real lock cancellation;
audit SQL/IGNORE/trim/loss and final upstream/link/plan/peer reversal; hook failure;
actual deferred-FK Commit rejection; and request-owned controlled errors/cancel
returned only after a real successful Commit. The latter may persist all facts
despite 503, emits no success signals, and is NOT a real I/O fault or rollback.
Subsequent old-positive-revision already_archived200 does not repair audit/signals.
Adapter ownership excludes login, bootstrap and control transactions.

## Evidence and stop gates

Fixed offline Windows Go1.26.9; SQLite tests noncached and serial. Seal raw
stdout/stderr and tool/source identity outside the repository before parsing.
Predeclare exact targeted/regression/race names; verify every run/pass, no skip or
missing names. No cached success, borrowed PR125 green, or remote per-case claim.
First unexpected error/red/tool refusal stops this batch without repair or rerun.
Expected old reds and the earlier PATH-discovery exit1 remain historical facts.

No commit or transmission is permitted in this fixture-only batch. Only declared
fixture tests and, if declared, a new-fixture old-source control/three expected
reds may run. Old source must be pinned by hash using an external Go overlay;
the frozen product draft must not be replaced. Do not run the full candidate,
regression, race, vet/build, PR/CI/body work or infer their acceptance.

Historical facts remain separately attributed: the earlier optional dependency
Commit search had normal rg no-match exit1; the later nonexistent
channel_monitor_test.go caused OS error2 and stopped the implementation batch.
Its 5/5 control and three expected reds belong only to the original test SHA256
`F82C5D8B4D5B72D4403B0A93C87CA1C0E1AF5CD80D05C32BEE2414D7394BF664`.
That version's plain-DSN reopen was READWRITE|CREATE, not read-only, and did not
check DB Close. Its child goroutine could FailNow before delivering a result.
The stopped batch's collected 47 run/pass events are not accepted or repaired
retroactively. New fixture hashes/raw evidence are distinct; no old proof is
borrowed as a corrected-fixture result.

## Boundaries

This is local transaction confirmation, not tamper-proof audit, physical erasure,
old-backup anti-resurrection, provider revocation, real worker execution, real
entropy/I/O/power-loss proof, or multi-host consistency. SQL triggers are controlled
temporary database faults; post-Commit adapter returns are explicit simulations.
No production provider, real account, migration or deployment acceptance follows.
Old Code513–515, PR121 five old T, UNKNOWN/CAPACITY_UNPROVEN, M1-to-M2,
53 consumers, GOV-02, employee C resources and three-platform host gaps remain.
Draft / real migration / production NO-GO remains. No Ready/merge/tag/package/deploy.

Current results are sealed outside the repository after the two-file hash freeze.
At most FIXTURE_VALIDATED_ONLY may be reported if every declared narrow gate
passes. The product draft remains unaccepted and no new-head CI is claimed.
