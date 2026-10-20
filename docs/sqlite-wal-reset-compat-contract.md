# SQLite WAL-reset dependency compatibility contract

Date: 2026-10-20. Status: **blocked before commit/publication by the strengthened
post-first-row cancellation gate**. This is an uncommitted nine-file candidate,
not a published PR97 or a completed compatibility result. Production NO-GO remains.

## Purpose, source and frozen parent

This independently authored compatibility batch starts from PR96 remote head
`453473f1d18f21ea820b610760591ae2515c9dea` (tree
`a79a8e03e43a1bf452a47f88df7f260dadcdfb76`). It upgrades only the SQLite
driver and minimum selected dependency closure, plus isolated tests/notices/CI.
It does not change business logic, product schema, backup algorithms, the PR96
shadow kernel, previous workflows, release configuration or credentials.

The pinned target is `modernc.org/sqlite v1.46.2`, upstream tag commit
`d7c9932b311af8741de8fa03011de3704940d210`, embedding SQLite **3.51.3**.
The driver requires Go 1.25.0; this batch uses the existing Go **1.26.8**
toolchain. Source metadata alone is not native runtime evidence.

Primary sources inspected for this batch:

- [SQLite WAL reset advisory](https://sqlite.org/wal.html#the_wal_reset_bug):
  fixed in 3.51.3, with 3.44.6/3.50.7 backports. Multiple connections to one
  WAL database and concurrent writes/checkpoints are relevant. This batch does
  **not** reproduce the rare corruption bug or prove elimination of all risk.
- [Exact driver module](https://gitlab.com/cznic/sqlite/-/raw/d7c9932b311af8741de8fa03011de3704940d210/go.mod)
  and [exact changelog](https://gitlab.com/cznic/sqlite/-/raw/d7c9932b311af8741de8fa03011de3704940d210/CHANGELOG.md).
  v1.46.1 added rollback/reset after failed Commit. Tests below exercise actual
  refusal and same-physical-connection reuse, not just close/reopen. `_texttotime`
  is not enabled: default TEXT temporal column metadata reports string while
  the formatted DATETIME fixture value scans as time.Time, as in the old typed
  facts. These two kinds of evidence must not be conflated.
- [GitHub native runner labels](https://docs.github.com/en/actions/how-tos/write-workflows/choose-where-workflows-run/choose-the-runner-for-a-job).
  Published labels are configuration evidence, not proof that a job executed.

All new test/contract/workflow content is independently authored from these
public sources and this repository's functional contracts. No archived CPA,
CLIProxyAPI or Sub2API implementation/template is used. Dependencies retain
their own licenses. Coverage or compatibility is not security certification.

## Exact file budget and stop conditions

At most these ten paths may change (the optional binary fixture can be omitted
when auditable old-engine fixture bytes are embedded in the test source):

1. `go.mod`
2. `go.sum`
3. `internal/financial/actor_migration.go`: verified driver-version comment only.
4. `internal/service/upstream_rotation_shadow_candidate_test.go`: only the
   hardcoded `sqlite_version()` expectation 3.50.3 -> 3.51.3.
5. `internal/sqlitecompat/sqlite_compat_test.go`: new test-only package.
6. `internal/sqlitecompat/testdata/legacy-v1.38.2.db`: optional synthetic fixture.
7. `.github/workflows/sqlite-compat.yml`: new independent natural-PR workflow.
8. `THIRD_PARTY_NOTICES.md`: current selected versions and original licenses.
9. `docs/research/dependency-notices.md`: append current evidence only; all
   historical dates, claims and tables remain unchanged.
10. This contract.

Any additional path, business/schema/kernel/backup algorithm change, untrusted
module resolution or missing required test capability stops implementation for
direction. Never amend old PR bodies or borrow their green CI. One bounded
commit/push and one stacked Draft PR are authorized. After publication source
is frozen: the first unexpected natural CI red freezes the batch; no hot patch,
manual rerun, dispatch, runner substitution or second push follows implicitly.

## Module acquisition and provenance gate

Resolve actual selected graph and record exact module/go.mod sums. Minimum
changes expected from the target's go.mod include libc 1.70.0, x/sys 0.42.0,
strftime 1.0.0 and x/exp `v0.0.0-20251023183803-a4bb9ffd2546`; selected graph,
not this expectation, controls the final inventory. Do not upgrade x/crypto or
unrelated modules merely to latest. Verify original license files for changed
selected modules and retain publication notice gates.

Default proxy IPv6 was unavailable locally. A replacement GOPROXY is allowed
only in the current child process. Keep TLS validation and official
`GOSUMDB=sum.golang.org`; do not edit trust, persisted Go settings, remote or
credentials. The downloaded v1.46.2 module was verified by the Go downloader:

| Object | Go checksum |
| --- | --- |
| v1.46.2 module | `h1:gkXQ6R0+AjxFC/fTDaeIVLbNLNrRoOK7YYVz5BKhTcE=` |
| v1.46.2 go.mod | `h1:hWjRO6Tj/5Ik8ieqxQybiEOUXy0NJFNp2tpvVpKlvig=` |

## Traceable old-engine fixture gate

Before changing go.mod, execute the new test source's explicit fixture-generation
mode under the unchanged **v1.38.2** graph. Refuse generation unless build-info
module identity and actual `sqlite_version()` are v1.38.2 / 3.50.3. Record full
`sqlite_source_id()`, toolchain, module checksum and SHA-256 of actual database
and backup bytes. Generation uses only public synthetic values inside an owned
temporary directory. No real DB, backup, key, configuration or session is read.

Embed the generated database and encrypted backup bytes with hash checks in
the test source (or use the one optional binary path). On the new engine, read
those exact bytes and restore the **actual old-created backup** through the
unchanged backup API. Compare complete ordered schema/row/type facts with a new
engine-created equivalent fixture, including NULL/blob/Unicode/integer values,
integrity, FK facts and existing restore safety-state behavior. Executing old
DDL under the new engine is not old-file compatibility evidence. Public test
passphrases and synthetic root material are not production secrets.

## Deterministic runtime gates

Tests are bounded, serial and independent of real services. They must prove:

- Native runtime 3.51.3/full source ID, target GOOS/GOARCH and exact driver pin.
- Independent connections: real WAL committed writes, held reader checkpoint
  BUSY result, writer contention, cancellation, later checkpoint, independent
  reopen, integrity/FK and complete committed facts. No random stress/reproducer
  claim and no assumption that cancellation means commit did not happen.
- Real deferred-FK Commit refusal and real rollback-journal BUSY Commit refusal;
  driver cleanup and reuse of the **same held physical sql.Conn**, not a fresh
  pool connection. No leaked partial row/transaction. FK ON must be restored
  and verified; unusable connections are discarded fail closed. No automatic
  retry or successful receipt is manufactured for an uncertain Commit.
- Prepared statements: error and cancelled execution followed by reuse, with
  distinct parameters and no parameter bleed or partial durable writes.
- `mode=ro` missing-file failure creates no database; writes are rejected;
  `query_only` rejects writes on a read/write handle; cleanup releases handles.
- Existing PR92 quoted-NULL, PR95 retirement, PR96 case-sensitive inbound-FK,
  financial migrations, backup/restore and read-only cancellation regressions
  actually execute, with local and remote evidence reported separately.

## Six native natural-PR legs

The new workflow triggers on `pull_request` only, uses contents-read permission,
fixed action commit SHAs, raw PR head checkout and explicit HEAD assertion.
No production secret, sudo, deployment, signing or packaging operation is added.

| Runtime | Native runner |
| --- | --- |
| linux/amd64 | ubuntu-24.04 |
| linux/arm64 | ubuntu-24.04-arm |
| windows/amd64 | windows-2022 |
| windows/arm64 | windows-11-arm |
| darwin/amd64 | macos-15-intel |
| darwin/arm64 | macos-15 |

Each leg verifies actual Go host/target and SQLite runtime version/source ID,
runs non-cached targeted compatibility tests and natively builds both
`cmd/cpa-cloud` and `cmd/cpa-cloud-backup`. Windows execution is serial
(`-p=1 -parallel=1`), never a parallel repository-wide test. Unavailable,
skipped, timed-out or wrong-architecture legs are not passes. Cross-compilation
is not native execution. Existing ci-plan/workflows remain unchanged; their
core-only selection does not replace these six independent legs.

## Evidence ledger and acceptance boundary

The old fixture generator actually ran with Go 1.26.8 windows/amd64 under the
unchanged v1.38.2 graph, before the upgrade. Its SQLite source ID was
`2025-07-17 13:25:10 3ce993b8657d6d9deda380a93cdd6404a8c8ba1b185b2bc423703e41ae5f2543`.
The old driver module sum is
`h1:Aclu7+tgjgcQVShZqim41Bbw9Cho0y/7WzYptXqkEek=`.

| Generated old-engine evidence | SHA-256 |
| --- | --- |
| Actual database bytes | `47280e3495ad814d4eacb939ae173e239de51b978f9c868d58d36f7b6cae271b` |
| Actual old-created encrypted backup | `455171c741a375c4954e86d8be876cd8a6f5bf1f7ec7db16afc151ee58aa8a61` |
| Ordered complete typed facts | `4d6303754563d5eb4288b88202c4ad2ef44f1e262859422822f13e8ed8cf2a6f` |

Actual selected versions/checksums and changed license texts were verified;
the append-only dependency record contains the exact five changed module pairs.
The official signed sumdb lookup for sqlite v1.46.2 is record 51279105. Local
GOSUMDB is sum.golang.org with no GOPRIVATE/GONOSUMDB exemption. No persistent
proxy/trust/credential/remote setting changed.

Completed local Windows/amd64 evidence, Go 1.26.8, all test execution serial:

| Scope | Pass | Fail | Skip |
| --- | --- | --- | --- |
| Financial package | 93 main + 104 sub | 0 | 0 |
| Sealed fixture reader, including quoted-NULL/read-only/cancel/cleanup | 19 main + 77 sub | 0 | 0 |
| Backup package | 15 main + 20 sub | 0 | 2 |
| Selected service retirement/shadow/backup groups | 47 main + 149 sub | 0 | 0 |

The two existing backup skips were the unavailable Windows symlink case
`TestRestoreRejectsLinkedParent` and Unix-mode permission case
`TestRestoredPermissionsAreRestricted`; neither is counted as execution/pass.
Targeted vet and both native CLI builds passed. Binary build info confirmed
sqlite v1.46.2 / libc v1.70.0 / x/sys v0.42.0 / strftime v1.0.0 actually linked
on Windows; module graph membership alone is not per-target linkage proof.
New workflow static matrix/trigger/permission/SHA checks, its four Windows
PowerShell script parses, frozen comment/version-only scope checks, three local
document links, gofmt and git diff checks passed. This is not native CI evidence.

The initial eight-test compatibility draft passed before adding the stronger
post-first-row cancellation barrier. That earlier green is **not** the current
candidate's compatibility result. In the strengthened full run, old fixture /
runtime identity / deferred-FK Commit / BUSY Commit / FK restore-discard passed;
the WAL cancellation probe failed, and its then-missing transaction teardown
also prevented that full run from finishing normally. The owned process was
interrupted. A test-only deferred rollback was added, and one bounded isolated
probe repeated the failure and completed cleanup normally (exit 1):

`SQLITE_COMPAT_CANCEL_PROBE second_row=true context_cancelled=true row_error=true`

`SQLITE_COMPAT_RUNNING_QUERY_NOT_CANCELLED`

This is a real synthetic multi-connection WAL database. SQL returned a first row
before cancellation was armed; the later recursive row step still returned a
row after context cancellation, with Rows.Err also indicating error. It does
not demonstrate that cancellation is entirely absent, nor safe successful data
acceptance. Source inspection shows stmt.query's interrupt watcher is deferred
until QueryContext returns, while later rows.Next calls step without that
watcher. That is a source-based explanation, not proof of every cancellation
path. This extra probe was not run under v1.38.2, so it is **not** established as
a regression introduced by this upgrade.

Do not weaken the assertion to a pre-cancelled context, replace this observation
with the earlier eight-test green, or silently change product/driver/reader
logic. The current prepared-statement cancellation variant has not completed
in the strengthened full run. Direction is required on whether this later-step
limit can be explicitly deferred in a revised bounded contract, or requires a
separate driver-level fix contract. No commit/push/PR was made; no natural CI
has started; no old PR body or automation was changed by this implementation.

Pending after that decision: the final complete new package result, six natural
native CI legs and ordinary same-head checks. Do not preselect a final check
count. Full filter=all pagination, raw head/base, attempt1 jobs and exact package
evidence remain required at closeout.

Root original E/F must independently review the new exact head/base; neither
PR96 review nor CI automatically grants PR97 GO. At most this establishes a
bounded dependency-compatibility result. Real root rotation, old-backup
anti-resurrection/GOV-02, employee C, M1->M2 product migration and production
remain NO-GO. No Ready/merge/tag/package/deploy is authorized.

## Independent bounded continuation R1 — 2026-10-20 17:42 +08:00

This is a separately authorized scope revision **after** the local FAIL above,
not a hot patch or a claim that the original contract passed. The entire
preceding contract/evidence is historical and retained unchanged. Root reports
independent E/F source-only agreement that both old and new drivers end the
initial query interrupt watcher before later Rows.Next steps; Go database/sql
can expose an in-flight Next=true together with a subsequent cancellation error.
No old-engine dynamic equivalence had been established at that authorization.

The original **timely post-first-row interruption / no row delivery after cancel
gate remains FAIL**, and is explicitly removed from this batch's *revised narrow*
compatibility acceptance. Its original assertion/helper and original WAL probe
body remain in the test source as named historical, non-active gate functions;
its exact executed output remains above and must also be retained in the new PR
body. Excluding this gate is an explicit contract decision, not deleting the
second-row assertion, skipping a test, or replacing execution with pre-cancel.
The original log established only second_row=true, context_cancelled=true and
row_error=true. It did **not** establish errors.Is, the concrete error type,
cancellation latency, or old-engine dynamic equivalence. Do not retroactively
upgrade those observations or claim timely interruption/no post-cancel row.

Active R1 checks retain a real first-row execution barrier and cancellation
during later iteration. They must independently check errors.Is(Rows.Err(),
context.Canceled), explicit Rows.Close and rejection of partial/cancelled
iteration as a complete successful result. They then verify the same held
physical sql.Conn and prepared statement can be reused without parameter bleed,
FK remains ON, transactions and read locks are released, WAL checkpoint succeeds,
and close/reopen preserves complete committed facts. Cancelling a read is not
proof of zero writes or rollback: the separate writer's committed facts must
remain visible. Prepared-statement refusal/cancel, checkpoint, backup and all
previously unexecuted negative paths must actually finish before publication.

Perform a same-Go/same-OS old/new comparison if feasible. The original legacy
fixture was generated in the isolated checkout before go.mod changed, not in a
separate temporary module. For this comparison reconstruct that exact frozen
v1.38.2 go.mod/go.sum from parent 453473f1d18f21ea820b610760591ae2515c9dea in
an owned temporary alternate-modfile directory; do not downgrade the candidate.
Run the identical bounded WAL probe serially with Go 1.26.8 windows/amd64 and
verify actual driver/runtime identities. If old clearly does not reproduce while
new stably does, or any remaining active gate fails, stop before committing.
Even matching observations are only a controlled comparison, never a general
proof of non-regression. The original pre-comparison statement remains historical.

This continuation retains the same <=10 repository paths (nine used), pinned
dependency/minimum graph, provenance/license requirements and six native natural
PR legs. No business/schema/kernel/old workflow or real resource changes are
authorized. After all local gates, one commit/transport and one stacked Draft PR
are authorized; source then freezes. Any first unexpected natural CI red stops
the batch with no rerun/dispatch/hot patch. New exact-head formal E/F review
remains separate. Production and real migration remain NO-GO.

R1 evidence: pending at the time this revision was written. Do not interpret
the historical FAIL as passing because a later, differently scoped suite passes.

### R1 completed local evidence — 2026-10-20, before publication

The controlled old/new comparison actually executed the identical three-case
WAL R1 probe with Go 1.26.8 windows/amd64, once under the frozen v1.38.2 graph
in the alternate-modfile directory and once under the candidate v1.46.2 graph.
Runtime guards confirmed each module and sqlite_version/source_id. All three
old cases and all three new cases observed:

`second_row=true context_is_canceled=true rows_error_is_canceled=true close_succeeded=true complete=false original_timely_gate_would_fail=true`

Both sides passed only the **R1** exact-error/Close/same-physical reuse,
transaction/FK, checkpoint and independent-reopen facts. There was no opposite
old-not-reproduced/new-reproduced observation. This is a controlled comparison,
not universal non-regression, and does not rewrite the original log's unverified
error type/latency or its then-unknown old dynamics. No cancellation latency
guarantee, timely interruption or absence of row delivery is established.

The complete revised new compatibility package then actually finished:
**8 main + 3 subtests PASS, 0 fail, 0 skip**. This includes old-created DB/backup
verify and restore, real deferred-FK and BUSY Commit refusal on the same physical
connection, verified FK restoration or fail-closed discard, all three R1 WAL
cases through final checkpoint/close/reopen, prepared UNIQUE refusal and actual
post-first-row cancellation followed by Rows.Close and same-statement distinct
parameter reuse, and read-only/missing/query_only cleanup negatives. The prepared
case also observed the excluded timely-gate limitation; cancelled partial data
was discarded, not accepted as a complete result. The separate WAL writer's
successful commit remains intact; cancellation is not asserted to imply zero
writes or rollback. Targeted vet passed again after the R1 test-source change.

The earlier financial/reader/backup/service local results and both native CLI
builds remain separate evidence; their production sources and dependency graph
did not change during R1. Two existing backup skips remain skips. Original
cancelAfterFirstRow helper and original WAL gate body were compared verbatim
(only the latter's name now explicitly marks it historical/non-active).
Historical contract prefix SHA-256 remains
`48b7440633ae87f06d62372fcb33464392f8f4120325e16bf087d16ec0d1dbf3`.
All nine repository paths remain within the original budget. This local result
does not anticipate six-native-job CI, ordinary CI or new-head formal E/F review.
