# Single-instance billing contract

This component is an independently authored, single-instance commercial ledger. It does not implement tenant boundaries and must not be described as multi-tenant billing. Its isolation keys are employee, employee Key, or an explicitly employee-owned resource. It introduces no third-party dependency and does not implement or claim compatibility with a provider payment protocol.

## Money and ownership invariants

- Amounts are signed `int64` micro-units and currencies are three uppercase ASCII letters. Floating-point values are never accepted or stored.
- `financial_entries`, their accounts, operation facts, accepted webhook events, top-up identities, redemptions, and refunds are append-only through database triggers. Balances are derived with checked addition; a stored aggregate is not authoritative.
- Employee, Key, and resource owners are validated against the base identity tables. A Key or resource cannot be attributed to a different employee. Currency is part of the immutable account identity.
- Exact `operation_id` retries return the first resource. A changed payload, action, or actor under the same ID is a conflict. Server observation time is not part of the retry identity.
- A refund of a debit is a positive `refund` entry. An external refund of a paid top-up is a negative adjustment linked to the original top-up entry. Both paths enforce same-account attribution and cumulative reversal no greater than the original entry. External refunds also fail if the wallet no longer has enough balance.

## Lifecycle

Commercial execution is disabled in `financial_settings` by default. Plan and connector configuration can be prepared while disabled; top-up creation, settlement, redemption, subscription purchase, and refund require the switch to be enabled.

- Plans snapshot price, granted credit, currency, interval, and revision into each subscription. Later plan edits do not rewrite subscriptions.
- Subscription purchase first proves the wallet can pay the snapshotted price, then atomically appends the charge and granted credit with the subscription record. Cancellation is revisioned and does not silently refund.
- A top-up starts as a pending payment with a stable external reference. No balance changes until a valid paid callback commits the immutable webhook event, payment transition, and top-up ledger credit in one transaction.
- Redemption codes are stored only as keyed digests. Plaintext is returned only on the first successful creation response. A code has a bounded use count, optional UTC expiry, and may be used at most once per financial account.
- Refunds atomically append the reversal, advance the payment's refunded total/status, and append an immutable refund fact.

## Callback envelope

The synthetic connector callback is `POST /admin/api/v1/billing/payment-callbacks/{connector_id}` and does not use an administrator session. It requires:

- `X-Billing-Event-ID`: connector-scoped replay identifier;
- `X-Billing-Timestamp`: canonical Unix seconds within five minutes of server time;
- `X-Billing-Signature`: lowercase hex HMAC-SHA256 over `timestamp + "\n" + event_id + "\n" + exact_body`.

Connector secrets are encrypted with the existing local root-key material. The accepted event stores only a SHA-256 payload digest and metadata, not the secret or arbitrary provider response. A repeated event must have the same payment, timestamp, and payload digest; a changed replay is rejected. A different event ID cannot reuse the signature because the event ID is signed, and an already-paid payment cannot be credited again.

## API surface

All configuration and manual money operations require the existing administrator session, Origin/CSRF checks, bounded unique-key JSON, and canonical decimal strings for micro amounts.

- settings: `GET/PUT /admin/api/v1/billing/settings`
- balance and adjustments: `GET /admin/api/v1/billing/balances`, `POST /admin/api/v1/billing/adjustments`
- plans: `GET/POST /admin/api/v1/billing/plans`, `PUT /admin/api/v1/billing/plans/{id}`
- connectors: `GET/POST /admin/api/v1/billing/payment-connectors`, `PUT /admin/api/v1/billing/payment-connectors/{id}`
- top-ups: `GET/POST /admin/api/v1/billing/topups`
- subscriptions: `GET/POST /admin/api/v1/billing/subscriptions`, `POST /admin/api/v1/billing/subscriptions/{id}/cancel`
- redemption: `GET/POST /admin/api/v1/billing/redemption-codes`, `POST /admin/api/v1/billing/redemptions`
- refunds: `GET/POST /admin/api/v1/billing/refunds`

## Delivery state

Implemented and tested: strict schema rollback/retry, default-off execution, fixed-point overflow rejection, append-only guards, exact retry/change conflict, ownership isolation, non-negative debits, plan snapshots and cancellation, encrypted connector secrets, signed callback timestamp/replay checks, top-up settlement, one-time code disclosure, redemption limits, subscription purchase, partial/full refund bounds, and bounded list reads.

Integration still owns App/store route and migration ordering plus the `single_instance_billing` status flag. That flag must remain false until migration, handler registration, secret material, and the complete test set are wired together. No outbound payment-provider client, production connector, automated renewal/expiry worker, email, tax, invoice, or multi-instance coordination is claimed by this component.
