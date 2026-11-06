# Bounded production-integration candidate, 2026-11-06

This is an integration Draft, not a release or a production-readiness claim.
The subsequent release scope is Linux amd64 server/Web only. Employee sources
are integrated, but employee installers and real apply are outside that release.
The user authorized integration toward a first release within implemented scope,
and excluded real M1-to-M2 rotation, old-backup anti-resurrection and actual
employee-client application acceptance from this batch's gates. Those capabilities
remain unverified/unimplemented as applicable; no test result is invented.

## Exact incoming history

Starting main: `c32a2c314f760ec32a242147a5c3ec5893917757`.

| Incoming leaf | Exact head |
| --- | --- |
| PR131 service fixes and consumer documentation | `78e084e7f1dba859dd8c6f18e31c3c4616f8d819` |
| PR121 administrator/self logout, backup diagnostics and diagnostic history | `e6d5151f10d6a368e751bd6ca764082d7ac504fa` |
| PR135 employee-client chain and observation contracts | `7c3946c1bdddb429f0b5275512374daa70439361` |
| PR100 independent Key-revocation confirmation and diagnostics | `153a57dbb71de8154ca0c1f5ec597c5736b4983d` |

Four local non-fast-forward merges preserve all four exact incoming commits and
their ancestor chains. PR83 is not an ancestor of PR135; its six changed blobs are
already byte-identically present through the B0 integration copy. That is content
coverage, not commit-ancestry coverage. No redundant PR83 merge was performed.
No old WIP, old PR body, old workflow run or old evidence was rewritten.

## Conflict resolution

The only textual conflict was `internal/service/employees.go` at `revokeKey`.
The employee-client route and protocol-source additions merged automatically.
The resolution retains PR100's request-owned private hooks and validation of
existing Key/slot state together with PR122's complete persisted Key/slot checks
and retained real-administrator lifecycle audit.

- Only the initial absent-Key lookup returns404; subsequent state/query failures
  return503.
- Pending/armed slots use the existing cancellation helper, then an owner-bound
  UPDATE with affected1 clears `armed_at`. The expected final slot also requires
  `armed_at=NULL`. Fingerprint and activation revision fields are cleared.
  Issued/cancelled slot history is preserved.
- An audit is written only for an unrevoked Key or pending/armed slot transition.
  It binds the authenticated administrator, and a successful no-op retry adds no
  audit. The unchanged retention bound remains1000.
- All writable private pre-Commit hooks run before the final full Key/slot reads.
  Both the retained-audit check and PR100's confirmation remain. No writes follow
  final confirmation before the one actual Commit.
- Explicit Commit success alone permits200 and notification. A real error cannot
  be erased by a reporting adapter. A post-success adapter error gives503 without
  notification and does not establish rollback or safe automatic retry.

The PR100 persistence test now asserts exactly one authenticated audit for the
first transition, unchanged unrelated audit/financial facts, and no audit change
on retry. The PR122 slot fixture now uses NULL activation snapshots for inactive
states, matching the actual API and retained PR100 validation; the final assertion
also checks cleared `armed_at`. These changes reconcile the combined contracts,
rather than discarding either side's rejection or notification assertions.

## Cross-platform fixture bytes

The first employee frontend run failed because Windows checkout produced488 bytes
for a committed463-byte OpenClaw public fixture. A first checkout-index attempt
did not rewrite the already-up-to-date worktree file; its repeated red is retained.
The integration adds an employee-local `.gitattributes` rule preserving raw fixture
blob bytes and explicitly restored all15 public fixtures from their indexed blobs.
All15 match Git object hashes; their committed content and fixed expected
size/SHA constants are unchanged. A subsequent complete frontend run passed15/15.

## Local validation on the combined source

Go1.26.9, Windows/amd64. SQLite tests use native Windows execution, one uncached
serial invocation per gate (`-p=1 -parallel=1`); no old-oracle flags are inherited.
Raw streams, native exits and SHA256 were sealed before acceptance parsing.

| Gate | Result |
| --- | --- |
| Go whole-module build, CGO0 | exit0 |
| Linux amd64 server cross-build, CGO0 | exit0; ELF cross-compilation, not native Linux execution |
| Key revocation and client-configuration tests, CGO1 | 30 parents +82 children =112, each run/pass once;0fail/skip; package pass |
| Administrator logout persistence and self-logout normal/empty | 2 parents +1 child =3, each run/pass once;0fail/skip |
| Web TypeScript/Vite build | exit0 |
| Web unit tests | 22 files /252 tests passed |
| Employee frontend TypeScript/Vite build | exit0 |
| Employee frontend tests after fixture-byte correction | 3 files /15 tests passed |
| Native Rust/Tauri compile | no completed result; excluded after Linux-only scope decision |
| Go vet | whole module, exit0 |
| Backup CLI unit tests | 13 parents +7 children =20, each run/pass once;0fail/skip |

The initial MSVC Rust check could not find `link.exe`; this was an environment
failure, not a successful native check. An existing GNU toolchain directory
contained Cargo without rustc, so its first invocation could not start rustc.
The complete pinned Rust1.90.0 Windows GNU toolchain was then installed and a fresh
GNU check was started. It was stopped before completion when the release scope
was explicitly narrowed to Linux server/Web, rather than blocking on Windows
employee tooling. These attempts remain in private raw evidence and are not
relabeled successful. No native Rust pass or target-application execution is claimed.

## Known reds and unfinished source retained

The full branches include diagnostic and experimental code; merging them does not
turn proposals into implemented features or old reds into green:

- PR102-119 self-logout race/snapshot failures, including Code513-515, remain
  historical unresolved observations. Later docs-only checks do not retest them.
- PR133's macOS engine lock-test failure remains unresolved. PR135 itself had no
  natural checks. No holder/root-cause inference or macOS execution is supplied.
- PR89's declared synthetic-channel pre-create evidence stop remains a red,
  not actual channel/application acceptance.
- PR100 Code501's original trigger-install failure still has UNKNOWN root cause;
  later diagnostic greens mean non-reproduction in those runs.
- PR97's original timely later-row-step cancellation limitation remains disclosed.

No real credentials, user configuration, production database/backup or official
client is accessed. No real rotation, external anti-revival authority, Windows
pre-image authenticated recovery/apply or employee provisioning is established.
No release tooling was changed and no Ready, main merge, tag, package, release,
deployment or old-CI rerun/dispatch is performed by this batch.

## Independent revocation-audit P2 batch, 2026-11-06

The preceding integration tree and its local passes remain historical facts.
Remote head c30d1c4c156de651cda060d3144481bf9f3ceecb has subsequently received
formal independent static E0/2/0 and F0/1/0 findings. Its audit baseline follows
Key/slot writes, and its last writable beforeCommit seam follows audit confirmation.
That old head is NO-GO; neither reviewer dynamically ran the counterexamples.

This separately authorized three-file batch first freezes old-source bytes and
tests two windows with synthetic owned native Windows SQLite and real authenticated
HTTP: genuine Key/armed-slot write triggers add an extra valid audit; a private
beforeCommit hook deletes the current audit. The latter is a private testing
interface case, not an HTTP-injectable hook. Expected old200 labels are emitted
only after durable facts, close/reopen equality and every fixture cleanup.

The candidate must capture the retained-row baseline before any transactional
writable seam or Key/slot write and confirm audits after the final writable hook.
A transition requires one retained new real-actor event; a no-op retry requires
zero new events and unchanged audit count. The original1000 trim and Commit/report/
notification semantics remain. Rowid bounds concern currently retained rows,
not a global immutable history or arbitrary hostile-schema execution trace.

New baseline/candidate evidence is separate from all prior CI and old static red.
No repair pass, new-head review, production readiness or release is claimed here
before the new directed and regression gates actually finish. Issuance files and
PR137 remain paused; no Ready/main merge/tag/release/deploy is authorized.

## Independent local P2 product candidate, 2026-11-06

All preceding STOPs, static findings and old-head CI failures remain historical.
The old-product oracle raw SHA256 is
`CAFCD2B538B3D05FB05B474BB01F07E407400EEBF191903980B4E38337AFA0D0`.
It proves both Key/armed-slot trigger modes returned200 while adding two audits,
and the private beforeCommit deletion returned200 without a new retained audit.
Those expected labels were emitted only after their complete fact and owner
cleanup gates. The hook remains a private test seam, not an HTTP-injectable input.

This local candidate changes only revokeKey's audit confirmations: capture rowid
and count before transaction-owned writable hooks or Key/slot writes; retain the
one new real-administrator event's identity/time; then, after the last writable
hook and full Key/slot confirmation, require exactly one retained matching new
event for a transition or zero added events and unchanged count for a no-op.
The shared lifecycle writer/1000-row trim and all other entry points are frozen.
Explicit Commit success, notification ordering and post-real-Commit reported
error503 semantics are unchanged; that503 never establishes rollback.

The existing strict close/read-only/reopen fixture also covers no-op hook tests.
It requires full typed equality after Close; complete Open permits only the
previously observed valid monotonic budget-clock value change. No table or other
typed cell is exempted. The one frozen offline Go1.26.9 Windows/native SQLite3.51.3
serial, noncached, failfast gate completed in
`integration-revoke-p2-product-20261106-2fe0b7a9`: 26 exact parent/child names each
ran and passed once, native exit0/package PASS, 0fail/skip/missing/duplicate/extra,
no old-oracle label or unexpected/cleanup output, empty stderr and complete raw EOF.
Raw stdout SHA256:
`C837852280D5D1FA58D0E2B7AB6A730A78DB52015F24E2CEF1EF878AED6FB2A3`.

The selection covers ordinary and both new503 P2 negatives (both Key/armed-slot
trigger modes), no-op hook insert/delete rejection with full rollback/no notify,
existing retry/restart, all pending/armed/issued/cancelled slot states with and
without previous revocation, retention999/1000/1001 and own-event eviction,
CAS affected0, pre-Commit refusal, actual deferred-FK Commit rejection and the
post-real-Commit reporting error. The last case still503/no-notify with committed
revocation/audit facts; it is not described as rollback. Six P2 fixture paths also
passed complete close/RO typed equality and the sole post-Open clock exception.

Highest result: `LOCAL_P2_CANDIDATE_VALIDATED`. No source changed after this gate
except this result text. The shared audit writer and other test/entry-point files
remain unchanged. Root separately reports the old head's Code528 core/four-race
failures and Employee synthetic-channel C failure: 35checks=26success+3conditional
skipped+6failure. Skipped means unexecuted. Those old failures are not superseded
by this local selected pass. No new commit/head, CI success, E/F GO or release is
claimed; toolchain/source-guard and other old CI work remains a separate batch.
