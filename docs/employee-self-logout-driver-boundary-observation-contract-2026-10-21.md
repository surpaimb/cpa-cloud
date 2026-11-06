# Employee self-logout driver-boundary observation contract

Status: independent docs-only design, 2026-10-21. No observer, opener seam,
driver wrapper, test or shutdown repair is implemented by this document.
Keep Draft / unresolved dynamic acceptance / real migration and production
NO-GO. Approval of this design would not authorize its implementation.

## 1. Exact baseline and one-file authority

The stacked source is [PR106](https://github.com/surpaimb/cpa-cloud/pull/106),
exact remote head `41fa9e3b35b981b23727d7d9d725aead065c462e`, whose base and
sole parent are PR105 `259a5d69a70b7596583bf4d27179024d494cb615`.
Its complete tree is `db287aab6ca76d9b3e2726fc4dd5ef55ef897d3c`.
This batch may add only this document. It may not change any existing file,
old PR body, dependency, workflow, driver, worker, SQL, DDL or product API.
It is a new independent design batch, not a hotfix to PR106.

The only candidate is observation of the fixed driver's actual `Tx.Rollback`
and `Conn.Close` invocation boundaries for explicitly selected, later reopened
synthetic Apps. It is not instrumentation of every driver/native operation.
Other methods would only be delegated or used for explicitly approved object
registration; they are not additional SQL probes or claimed event coverage.
The earlier App from which the driver is obtained is explicitly unobserved.

The interpretation is fixed to Go **1.26.9**, `modernc.org/sqlite` **v1.46.2**,
and this source baseline. It does not change the module's Go directive or any
workflow's Go pin. Inherited native jobs using Go1.26.8 cannot establish the
Go1.26.9 observer's behavior. Other versions need a new interface/source audit.

Project requirements remain [the independent-source rules](independent-implementation.md),
[the product plan](product-plan.md), [the development plan](development-plan.md)
and [the preview contract](preview-contract.md). The existing
[application SQL lifecycle diagnostic](employee-self-logout-expiry-sql-lifecycle-diagnostic-2026-10-21.md)
is preserved, not silently extended by this design.

## 2. Gate A: registered driver origin without a probe DB

In the fixed sqlite source, `driver.go:27-34` owns singleton `d`, and
`sqlite.go:56-57` registers `newDriver()` under `"sqlite"`. `newDriver()` returns
that singleton. `(*sql.DB).Driver()` returns its connector's driver without
acquiring a connection. The accepted candidate obtains that object from the
**already existing, earlier App's DB**, retains only the driver reference and
uses it for designated later reopened Apps. No additional App or DB may be
opened merely to obtain it. No call to `Conn`, `Ping`, `Raw` or SQL is needed
or permitted for acquisition. The donor App's lifetime is unchanged.

The following are not interchangeable acquisition paths:

| Path | Decision / disturbance |
| --- | --- |
| Existing donor App's `db.Driver()` | Candidate; first/donor App remains unobserved, including any already-running tail |
| New `sql.Open("sqlite", ...)` only to obtain Driver | Rejected here: although lazy and not itself a native connection, it creates an extra DB, cancellation context, channel/map and `connectionOpener` goroutine |
| Close that probe DB immediately | Does not erase its perturbation; fixed `DB.Close` cancels the opener but has no synchronous opener-join |
| `new(sqlite.Driver)` / `&sqlite.Driver{}` | Different instance, not the registered singleton or proof of identical hook/function/module state |
| `sql.Drivers()` | Names only, not instances |
| Re-register, global hook, unsafe/reflection/linkname, registry patch, extra Initialize/App/query | Rejected; not a substitute for the donor object's public method |

The fixed driver lacks `DriverContext`. The standard `dsnConnector.Connect`
ignores its context and delegates to that driver's `Open` with the original
DSN. A future per-App connector must retain that behavior, return the original
driver from `Driver()`, and not invent connector `io.Closer`, a new cancellation
guard, DSN normalization, extra native opens or connection settings.
Replacing a targeted DB must not first construct a discarded default DB.

There is currently **no injected opener**: `internal/service/store.go:25`
hardcodes `sql.Open`; Initialize opens its own unexposed store. A nil-default,
per-App opener is only a next-batch candidate, not an existing capability or
authority to edit Config/Open/store. Its call graph can affect other callers,
startup initialization, error cleanup and test setup. A future implementation
requires a separately approved file whitelist and proof that nil retains the
exact old path, only selected reopened Apps receive observation, and no global
opener/registry/hook is changed. `Conn.Raw` cannot retrofit a DB's driverConn.

This avoids an *extra acquisition DB*; it does not claim zero observer effect.
Wrapper objects, atomics, retained recorder memory and scheduling can perturb
execution. Initial App coverage is deliberately sacrificed rather than hidden.

## 3. Gate B: exact fixed interface shape and nontransparent boundaries

The following standard interface matrix is from actual fixed method sets, not
only the sqlite source's compile-time assertion list. Base methods keep their
Go driver signatures. A future wrapper must neither omit an existing optional
interface nor advertise an absent one with a fabricated fallback.

| Object | Base methods | Present optional interfaces | Absent optional interfaces |
| --- | --- | --- | --- |
| Driver | `Open` | none | `DriverContext` |
| Conn | `Prepare`, `Close`, `Begin` | `ConnPrepareContext`, `ConnBeginTx`, `Pinger`, `Execer`, `Queryer`, `ExecerContext`, `QueryerContext`, `SessionResetter`, `Validator` | `NamedValueChecker` |
| Tx | `Commit`, `Rollback` | none | no additional standard Tx interface |
| Stmt | `Close`, `NumInput`, `Exec`, `Query` | `StmtExecContext`, `StmtQueryContext` | `NamedValueChecker`, `ColumnConverter` |
| Rows | `Columns`, `Close`, `Next` | `RowsColumnTypeDatabaseTypeName`, `RowsColumnTypeLength`, `RowsColumnTypeNullable`, `RowsColumnTypePrecisionScale`, `RowsColumnTypeScanType` | `RowsNextResultSet` |

In particular, Conn has `ResetSession(context.Context) error` and
`IsValid() bool`. Fixed Go `beginDC` tests the presence of **both** to determine
its cancellation-rollback connection-retention branch. Dropping either or
returning a constant changes behavior, not just telemetry. Merely embedding
`driver.Conn`, `driver.Stmt` or `driver.Rows` does not preserve optional methods.

The fixed Conn also publicly exposes these nonstandard method signatures;
they are not all standard driver interfaces:

```go
Serialize() ([]byte, error)
Deserialize([]byte) error
NewBackup(string) (*sqlite.Backup, error)
NewRestore(string) (*sqlite.Backup, error)
IsReadOnly(string) (bool, error)
FileControlPersistWAL(string, int) (int, error)
RegisterPreUpdateHook(sqlite.PreUpdateHookFn)
RegisterCommitHook(sqlite.CommitHookFn)
RegisterRollbackHook(sqlite.RollbackHookFn)
Lock()
TryLock() bool
Unlock()
```

The last three are promoted by the native Conn's embedded Mutex, not a license
to substitute a new proxy mutex. Driver additionally exposes
`RegisterConnectionHook(sqlite.ConnectionHookFn)`; this design does not call it.
Any later claim of extension-shape preservation requires deliberate exact
forwarding, not accidental loss through a base-interface field.

### Repository Raw / direct-driver inventory at the exact baseline

All 496 tracked Go source files were included in the read-only inventory;
their LF-normalized Git blobs matched the frozen tree. The three Raw callsites
and one separate direct-driver construction are:

| Source location | Existing requirement / limit |
| --- | --- |
| `internal/backup/backup.go:551` | Raw asserts `NewBackup(string) (*sqlite.Backup,error)`; the caller uses concrete Backup Step/Finish |
| `internal/sqlitecompat/sqlite_compat_test.go:457` | Raw captures identity for same-connection reuse and replacement comparisons |
| `internal/sqlitecompat/sqlite_compat_test.go:851` | Raw returns `driver.ErrBadConn`; later calls must retain discard behavior |
| `internal/upstreamfixturereader/reader_test.go:456` | Separate `(&sqlite.Driver{}).Open`, followed by a QueryerContext assertion; not singleton acquisition or an App-opener caller |

No repository `DB.Driver`, `sql.OpenDB`, `sql.Drivers`, `sqlite.Limit`, or the
other listed Conn extensions was found in that inventory. This is a bounded
source result, not a guarantee about future callers or dynamically loaded code.
Do not globally wrap compatibility/backup/reader paths to widen observation.

The following nontransparency is mandatory, even if standard interfaces match:

- A proxy cannot keep Raw's original concrete dynamic type. One stable proxy
  per native Conn can preserve relative reuse comparisons, **not** native type
  identity. Do not allocate a fresh proxy on each Raw/borrow or infer identity
  from a printed address. Raw's panic/error/release semantics remain unchanged.
- `sqlite.Limit` (`sqlite.go:880-889`) asserts private `*conn`; an external
  proxy cannot satisfy it. An unexpected such caller blocks acceptance; it is
  not repaired with reflection, unsafe, unwrapping conventions or extra queries.
- `NewBackup/NewRestore` return concrete `*sqlite.Backup` and directly call
  private `newConn`, bypassing the outer connector. Backup Step/Finish/Commit
  and its internal connection lifecycle are not completely observable by this
  candidate. Exact forwarding of NewBackup is not full backup compatibility
  or full driver-call coverage.
- Native Tx.Commit can privately execute a cleanup rollback on failure. That
  is not a proxy `Tx.Rollback` event. Native Exec/Query also perform internal
  statement work; no proxy may invent missing internal events.
- `DB.Driver()` exposes the original Driver; a caller directly opening through
  it can bypass the proxy. This is another excluded path, not hidden coverage.

Delegation must preserve original contexts, options, arguments, results, row
alias lifetimes, nil/error combinations and error objects. Do not wrap errors,
invent ErrSkip/ErrBadConn, eagerly read columns/rows, add Ping/SQL, consume
arguments, install hooks or change pooling. Ordinary Result objects can remain
unwrapped. If preserving shape requires an additional object proxy, its methods
remain pure delegation except the separately approved two event families.

## 4. Gate C: bounded multi-producer publication without new waits

Worker return/Wait does not join all producers: cancellation rollback, rows
cleanup, connection-opener activity and Close may run on other goroutines.
The recorder must therefore support multiple concurrent producers, not assume
that the worker owning the original snapshot is the only writer.

### Identities and fixed budget

All identity and event storage is preallocated per existing fixture before its
observed phase. No global map, address registry, new worker or recorder goroutine
is permitted. Proposed evidence ceilings per fixture are fixed at 16 selected
App slots, 32 Conn generations per App, 128 Tx registrations per App, 128 pass
tokens per App, 8192 invocation slots and 128 cut views. These are design caps,
not measured usage, permission to create objects, or proof of sufficient coverage.
The next batch must prove adequacy for its frozen cases without tuning after a
failure. No growth, ring overwrite, token reuse, wrapping counter or silent drop.

- App slot is explicitly fixture-assigned, never derived from a path or pointer.
- Conn generation is assigned once to a successful delegated Open and remains
  attached to that object through its tail. Failed Open is not a fake Conn.
- Tx identity is registered only from the actual successful Begin/BeginTx result;
  no synthetic transaction or double registration is allowed.
- Pass identity requires a separately reviewed explicit, instance-local
  association with the actual Tx. The present code has no proved driver-level
  association seam. A mutable "current pass", print order, nearest timestamp,
  SQL text, goroutine ID or stack cannot supply it. This unresolved seam is a
  hard gate before implementation, not something this document implements.
- Conn.Close records App/Conn generation **only**. Tx/pass are NOT_APPLICABLE,
  not guessed from the last transaction or worker. The donor App is excluded.

Every invocation obtains a unique slot with separately immutable ENTER and
RETURN payloads. The only event families are `DRIVER_TX_ROLLBACK` and
`DRIVER_CONN_CLOSE`. Payloads contain bounded local IDs, method/phase and fixed
result classes `OK`, `ERR_BAD_CONN`, `CTX_CANCELLED`, `CTX_DEADLINE`, `SQLITE5`,
`SQLITE_OTHER`, `OTHER`. These describe the original returned error only: nil
first, then BadConn/context matching, then the fixed sqlite Error.Code (exact
5 versus other), otherwise OTHER. No err.Error or numeric data is recorded.
Both native methods take no context argument: their event context is
NOT_APPLICABLE, not a guessed worker context. Native panic propagates normally;
no recover/re-panic or fabricated RETURN is added. SQL, DSN/path, raw error,
addresses, credentials, session/Key IDs, row values and arbitrary strings are
never payloads.

### Reservation, publication, cuts and seal

1. Before delegation, reserve a slot, fill ENTER and atomically publish its
   ready flag. After the native method returns, fill RETURN and atomically
   publish its independent ready flag. Published payloads are never changed.
   A reader must not touch a payload whose ready flag it did not observe.
2. Reservation and publication are distinct. Preemption can occur before ENTER
   publication, inside the native call, or after native return but before
   RETURN publication. No missing-at-cut publication proves a lost return.
3. At each **existing** schema-probe cut, read reservation high water and ready
   states once into a bounded immutable view. No SQL, DB.Stats polling, flush,
   channel receive, sleep, join, quiescence wait, retry or added cut is allowed.
   A scan is a per-slot observation vector, not a fictitious single linearized
   timestamp. A later read cannot retroactively rewrite the earlier view.
4. A reserved unpublished entry is `RESERVED_AT_CUT`; published ENTER without
   observed RETURN is `OPEN_AT_CUT`. Both carry UNKNOWN completion. A published
   RETURN establishes only that delegation returned before its publication;
   it supplies neither physical timing nor a standard-library release barrier.
5. Seal freezes a **report view/admission generation**, not producer lifetime.
   Already reserved producers may safely publish to their dedicated cells.
   Post-seal invocations still execute their original calls; they only set a
   monotonic atomic late-seen flag, with explicit UNKNOWN detail and no new
   slot reservation. No test callback, log/Fail call,
   channel close, storage reset/reuse/free or forced GC occurs in a late callback.
6. Callback/object references keep recorder memory alive until naturally
   unreachable. No forced wait/GC closes that lifetime. A final `lateSeen=false`
   means only "not seen at this read"; future publication remains UNKNOWN.
   No-new-wait + possible future producers cannot imply a complete final trace.

This specifies publication safety and reporting semantics, not an already
implemented lock-free algorithm. The next design review must verify a bounded
multi-producer reservation/seal algorithm, atomic publication happens-before,
no partial-payload reads, and memory ownership without adding synchronization
waits. Atomics do not make scheduling or wall-clock duration unperturbed.

### Incomplete evidence versus recorder failure

| Condition | Required interpretation |
| --- | --- |
| Legitimate RESERVED/OPEN at an original cut; possible later callback | UNKNOWN / OPEN_AT_CUT, not lost RETURN, not an automatic new diagnostic failure |
| Finite late details unavailable at final read | POST_SEAL_UNKNOWN; no complete-tail claim |
| Capacity exceeded, duplicate/reused token, forbidden mutable payload, unsupported interface or version | Fixed recorder hard failure; trace cannot be accepted as complete |
| Missing/ambiguous required App/Conn/Tx/pass association | Fixed identity hard failure; do not choose an owner to fill the field |
| Conn.Close has no pass/Tx | Intentional NOT_APPLICABLE, not an identity failure |

The original method must still execute exactly once with its original inputs,
return values/errors and panic propagation even if recording fails. Hard failure
invalidates evidence; it must not skip/repair/retry original work. Existing
failure labels and first-primary failure remain intact. Recorder faults are
reported only at original safe reporting points, never by late callbacks.

## 5. What these boundaries cannot prove

Fixed Go's `Tx.done` can become true before its driver rollback completes;
`ErrTxDone` is not such a completion signal. Driver Rollback RETURN precedes
possible prepared-statement cleanup and `tx.close`/release. Validator RETURN
does not complete `putConn`. Native Conn.Close RETURN precedes standard-library
driverConn bookkeeping. App.Close RETURN, worker Wait and an old pool counter
sample likewise do not name physical lock ownership or a release barrier.

There is no public `putConn-complete` callback supplied by this design. No Go
internal patch/hook or forced driver close is permitted to manufacture one.
Even all observed RETURNs do not imply all native/internal operations were
observed, that every lock was released, or that a later schema query must pass.
Cross-App tail attribution is limited to explicit local object identity.

No new waiting/query/retry, busy_timeout, checkpoint, early Rollback, timeout or
cancellation adjustment, forced Close, GC, runner change or substituted driver
may be used to obtain green. No lock owner, unique BUSY cause, production repair,
GOV-02 backup-revival protection or M1-to-M2 result follows from this candidate.

## 6. Historical failures stay failures

Do not repeat/embed prior PR bodies. Retain links to their exact evidence:

| Historical batch / exact head | Preserved natural Code result |
| --- | --- |
| [PR102](https://github.com/surpaimb/cpa-cloud/pull/102), `f6f6e2d55991010f6c04fc1a7fb96410b83312d8` | [Code504 /39530063577](https://github.com/surpaimb/cpa-cloud/actions/runs/39530063577) FAILURE |
| [PR103](https://github.com/surpaimb/cpa-cloud/pull/103), `53d0bd9e76065a2872f45586c79ff59b2e116c43` | [Code505 /39537799158](https://github.com/surpaimb/cpa-cloud/actions/runs/39537799158) FAILURE |
| [PR104](https://github.com/surpaimb/cpa-cloud/pull/104), `a2ddc1fa00789817f791bafd5062a2fbb31e6b21` | [Code506 /39543430504](https://github.com/surpaimb/cpa-cloud/actions/runs/39543430504) FAILURE |
| [PR105](https://github.com/surpaimb/cpa-cloud/pull/105), `259a5d69a70b7596583bf4d27179024d494cb615` | [Code507 /39551936173](https://github.com/surpaimb/cpa-cloud/actions/runs/39551936173) FAILURE; 23=17 success/3 failure/3 skipped |
| [PR106](https://github.com/surpaimb/cpa-cloud/pull/106), `41fa9e3b35b981b23727d7d9d725aead065c462e` | [Code508 /39562535012](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012) FAILURE; complete checks 23=18 success/2 failure/3 skipped/0 pending/0 other |

PR106's original race1 [job119096992890](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012/job/119096992890)
and race0 [job119096993008](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012/job/119096993008)
remain red at FINAL_CLEAN, retaining SLOG21_UNEXPECTED_SNAPSHOT_QUERY.
Their independent raw schema queries returned SQLITE5/BUSY with context NONE;
their application expiry Query cancellation / Rollback TxDone records are
different calls. Logical old-pool NONZERO samples and these method records do
not establish the owner, release completion or causal chain. Root cause stays
UNKNOWN. Source-only E/F narrow review is not dynamic acceptance or product GO.

The separate [PR100 historical Code501 /39500154209](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209),
old head `f393797dbfa062a983bb09ef2c8183a0a571581b`, and
[failed job118895347179](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209/job/118895347179)
also remain unresolved. Neither this document nor another green workflow
repairs those histories or restores withdrawn/suspended product conclusions.

## 7. Review, verification and stop gates

This batch performs only document-link validation, one-file diff/scope checks
and `git diff --check`. It runs no local Go test, build, SQL, service or fixture.
No behavior, race-safety, recorder bound or new per-case execution is claimed.
The current workflow filters predict no Code/offline-CLI/independent-VM run for
this one Markdown path; SQLite native has an unfiltered pull_request trigger.
Actual natural runs/checks must be queried for the new exact head. If zero,
write **not triggered / not executed**, never green. Skipped is not execution.
Native compatibility would remain inherited regression, not observer evidence.
Do not dispatch/rerun or edit workflow filters. Stop on the first unexpected red.

Before a later implementation batch, the original independent E/F reviewers
must review this new document at its actual head/base; earlier pre-review is
not that formal review. They must explicitly accept all three gates, the donor
coverage gap, concrete-type/backup escape limits, no-complete-tail semantics,
capacity/identity rules, and a separately scoped opener/correlation design.
Unproved pass association, unsafe reservation/seal behavior, interface drift,
or a demand for full zero-perturbation/transparency/completeness blocks code.

One authorized commit/push and a new stacked Draft PR are the publication limit.
Root re-pins head/base, records actual natural CI and arranges formal review.
No attachments or old-body copies are needed. No Ready, merge, tag, package,
deploy, production credentials/data, archived implementation, UI/ACL or main
WIP operation is authorized. PR106 and all older files/PRs remain unchanged.

## 8. Sources and provenance

This is independently authored requirement/observation design from the project
specifications, exact project source and public standard/third-party API source,
not copied implementation from an archived CPA/CLIProxyAPI/Sub2API tree. Prior
research means it is not represented as strict clean-room development. No new
dependency, copied library implementation or license change is introduced.

Reviewed 2026-10-21:

- [Go1.26.9 database/sql source](https://github.com/golang/go/blob/go1.26.9/src/database/sql/sql.go):
  dsnConnector 803-814; OpenDB 832-843; Close 926-958; Driver 1930;
  beginDC 1899-1926; awaitDone 2209-2222; rollback 2326-2354;
  putConn 1481-1531; driverConn.finalClose 677-705.
- [Go1.26.9 driver interface definitions](https://github.com/golang/go/blob/go1.26.9/src/database/sql/driver/driver.go).
  Go retains its BSD-style license; no standard-library source is vendored here.
- [sqlite v1.46.2 driver source](https://gitlab.com/cznic/sqlite/-/blob/v1.46.2/driver.go)
  and [registration / Limit source](https://gitlab.com/cznic/sqlite/-/blob/v1.46.2/sqlite.go).
  Semantics were read from the fixed local module source, including conn.go,
  tx.go, stmt.go, rows.go, backup.go, fcntl.go and pre_update_hook.go; GitLab's
  web shell is a link target, not substituted runtime evidence.
- The cached v1.46.2 ziphash matches [go.sum](../go.sum):
  `h1:gkXQ6R0+AjxFC/fTDaeIVLbNLNrRoOK7YYVz5BKhTcE=`. This comparison did not
  re-hash the entire archive or dynamically probe interfaces. sqlite retains its
  BSD-3-Clause license, Copyright 2017 The Sqlite Authors; other dependencies
  retain their own licenses.
- Repository locations in sections 2-3 refer to the exact PR106 baseline, not
  an arbitrary later main branch. Version changes invalidate these shape facts.
