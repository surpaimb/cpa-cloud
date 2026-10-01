# Single-period monthly subscription expiry (BILL-03)

This independently authored development-preview increment extends the [single-instance billing contract](single-instance-billing-contract.md). It is a local subscription record lifecycle, not model entitlement, provider billing, renewal, refund, or a new payment flow. Commercial execution remains disabled by default. `one_time` subscriptions retain their existing behavior.

## Frozen period and state

- A successful `monthly` purchase freezes `started_at` and `period_end_at` in UTC in the same transaction as its existing wallet charge, credit grant, subscription row, and operation receipt. The period is half-open: active only at instants `started_at <= now < period_end_at`. Exactly at `period_end_at`, it is expired. The response includes the frozen end; retries never recompute it or post money again.
- One calendar month means the same UTC time of day and nanoseconds in the next calendar month, with the day clamped to that month's final day. Thus January 31 ends February 28 or 29, March 31 ends April 30, and December crosses into January. The start must have a representable end within Go/SQLite's supported year range; otherwise purchase fails before posting money. `period_end_at` is stored in fixed-width UTC RFC3339 with nine fractional digits so database ordering and due selection are chronological.
- The stored `monthly` state machine is `active -> cancelled` before the end, or `active -> expired` at/after the end. Both destinations are terminal; an already cancelled row remains cancelled after its end. Expiry increments the stored revision exactly once and leaves `cancelled_at` null. A `one_time` row has no period end and cannot become expired. Existing plan price, credit, currency, interval, and revision snapshots never change.
- Cancellation at/after the frozen end is a conflict, even if the background worker has not persisted `expired`. Replayed cancellation operation IDs retain existing exact-retry semantics; an unsuccessful cancellation creates no operation fact. No expiry operation ID is invented.

## Persistence and reads

- On opening an existing database, migration recognizes only the exact previous subscription schema. Within one SQLite transaction it validates every old row and computes the frozen end of each old monthly row solely from its stored UTC `started_at` and interval snapshot. Malformed timestamps, unrepresentable month ends, invalid state combinations, unexpected schema, or foreign-key failures abort the entire migration without changing the old database. `one_time` rows receive a null end. Reopening an already migrated database makes no changes.
- One bounded background pass selects at most 100 due active monthly IDs ordered by `(period_end_at,id)` and conditionally marks them expired in a transaction. It runs promptly on startup and periodically thereafter, including while the commercial execution switch is off. Repeated passes and restart recovery are idempotent. A full pass may be followed by another bounded pass; failures are retried later without a partial multi-row claim. Closing the app stops and joins the worker. No periodic action charges, renews, refunds, claws back granted credit, or changes the immutable ledger.
- Admin list and detail responses expose `period_end_at` (null for `one_time`) and an effective `status`. A still-stored `active` monthly row whose end is due is reported as `expired` immediately, even when the worker is delayed or disabled by process failure. `revision` remains the stored revision until persistence completes, after which it advances once. Purchase-retry and cancellation reads use the same effective rule. Storage/parse errors fail closed rather than returning an apparently active subscription. Re-enabling commercial execution does not reactivate or renew an expired row.
- All admin routes retain existing administrator session and Origin/CSRF policy. Detail is `GET /admin/api/v1/billing/subscriptions/{id}` with no body/query; absent IDs return 404. No employee-facing entitlement or access-control decision is derived from these subscription records.

## Verification

Automated tests cover month-end and leap boundaries, exact-end behavior, one-time isolation, purchased snapshot/retry/ledger invariants, cancellation races, worker batch/restart/idempotency, commercial switch off/on, migration rollback and recovery, corrupted stored data, and admin list/detail projection while the worker lags. Browser checks cover monthly status/end visibility and cancellation controls. Tests must distinguish directly exercised behavior from planned provider compatibility. Build, vet, race tests, and browser checks precede a draft PR; a green GitHub workflow is verified at its exact commit before readiness is claimed.

## Public sources and provenance

This contract and its implementation are newly authored for CPA Cloud from the product specifications above and publicly documented behavior; no archived CPA, CLIProxyAPI, Sub2API, or reference implementation is a source. No new third-party dependency is introduced; existing dependencies retain their respective licenses. Consulted 2026-10-01:

- [Go `time` package](https://pkg.go.dev/time): UTC representation and `AddDate` normalization, which requires explicit month-end clamping here.
- [SQLite ALTER TABLE](https://sqlite.org/lang_altertable.html): safe table reconstruction and schema constraints.
- [SQLite transactions](https://sqlite.org/lang_transaction.html): atomic migration and bounded state transitions.
- [Go `net/http`](https://pkg.go.dev/net/http): request context and administrative handler behavior.
