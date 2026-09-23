# Codex membership OAuth lifecycle contract

Status: development preview contract, 2026-09-23. This extends the import-only
experiment in `codex-membership-preview-contract.md`; it does not claim that a
real ChatGPT membership account or a third-party OAuth registration has been
verified.

## Safety and configuration boundary

- The feature remains behind `--experimental-codex-membership` and OAuth is
  additionally unavailable unless an administrator supplies
  `--codex-oauth-client-id` and `--codex-oauth-redirect-uri` at process start.
  CPA Cloud does not ship or borrow a client ID from Codex, ChatGPT, a desktop
  application, or another product.
- The redirect URI must be an absolute HTTPS URI, except that literal loopback
  HTTP is permitted for local development. Its path must be
  `/admin/api/v1/codex/oauth/callback`, with no user-info, query, or fragment.
- Authorization and token exchange use fixed official HTTPS targets:
  `https://auth.openai.com/oauth/authorize` and
  `https://auth.openai.com/oauth/token`. Neither an API request nor imported
  data can override these targets. Tests inject an HTTP transport, not a target
  URL.
- The initial implementation requests the fixed scopes `openid profile email
  offline_access`. A deployment must confirm that its own registered client is
  entitled to those scopes and redirect URI. Absence of public third-party
  registration documentation is an unverified provider condition, not a
  general legal conclusion.

## Management API

All JSON management endpoints require the existing administrator session,
same-origin check, and CSRF token.

`POST /admin/api/v1/upstreams/codex-oauth-sessions`

```json
{"name":"Team Codex","operation_id":"uuid"}
```

Creates an authorization session and returns only:

```json
{
  "session_id":"oauth_opaque_id",
  "authorization_url":"https://auth.openai.com/oauth/authorize?...",
  "expires_at":"RFC3339"
}
```

The URL carries an unpredictable state and an S256 PKCE challenge. The state
digest, an encrypted session secret containing state and verifier, initiating
administrator session, name and expiry are persisted; plaintext state and
verifier are never exposed by a separate read API.
The operation ID is idempotent for the same active administrator session. An
authorization session expires after ten minutes.

`GET /admin/api/v1/upstreams/codex-oauth-sessions/{session_id}` is readable
only by the exact administrator login session that created it. It returns only
`session_id`, `status`, `expires_at`, and optional `upstream_id` or fixed
`error_code`. Status is one of `pending`, `exchanging`, `succeeded`, `failed`,
`cancelled`, or `expired`; authorization URLs, state, codes, tokens and account
identifiers are never returned. A different login session receives 404. A
configuration mismatch is projected as a redacted failure without consuming
the pending authorization, so restoring the snapshot configuration restores
the pending view. Startup converts a stranded `exchanging` session to failed
with `authorization_result_unknown`; `used_at` alone is never interpreted as
success.

The encrypted session secret also snapshots the configured client ID and exact
redirect URI. Idempotent retries and callbacks must match that snapshot. A
restart with different OAuth configuration returns
`codex_oauth_configuration_changed`; it neither rebuilds a different
authorization URL nor consumes state nor calls the token endpoint. Sessions
created by an older schema without this snapshot must be abandoned and
recreated. The JSON session endpoint returns that error code in its normal
error envelope; the browser callback returns the same code in
`X-CPA-Error-Code` with the fixed failure page.

`GET /admin/api/v1/codex/oauth/callback?code=...&state=...`

The browser callback requires the same administrator login session that
started authorization. It deliberately does not require `Origin`, because an
OAuth redirect is a cross-site top-level navigation; one-time state, session
binding and PKCE provide callback CSRF protection. It accepts either a code or
an allowlisted provider error, consumes state once, exchanges a code once, and
returns a small no-store HTML success/failure page. It never places a token,
authorization code, upstream body, account identifier or provider description
in that page or a log.

The administrator cookie uses `SameSite=Lax` to accompany that top-level GET
redirect. It remains HttpOnly and Secure on the supported server-TLS deployment;
JSON mutations retain Origin and CSRF-token validation. The existing global
middleware already sets `Cache-Control: no-store` and `Referrer-Policy:
no-referrer`, including on callback responses.

`POST /admin/api/v1/upstreams/{id}/codex-refresh`

```json
{"expected_revision":3}
```

Requests one explicit refresh. The response is the ordinary redacted upstream
view. Missing, null, zero, or negative `expected_revision` returns HTTP 400
with `invalid_request`; it never produces an empty successful response.
Missing OAuth configuration returns `codex_oauth_not_configured` and
names the required process flags without exposing secret material. Only
`codex-membership` upstreams are accepted.

Only an upstream created by this service's successful authorization-code flow
and still bound to the currently configured client ID can be refreshed. A
manually imported credential, a legacy row without provenance, a client-ID
mismatch, or an administrator credential replacement returns fixed `409
codex_refresh_not_bound` before decrypting a refresh token or contacting the
token endpoint. Administrator replacement atomically deletes any previous
OAuth binding.

The status response adds `features.codex_membership_oauth`,
`features.codex_membership_auto_refresh`, and a limitation
when the experiment is enabled but the client configuration is incomplete.
The integrated web client provides authorization, status polling, manual refresh,
and reimport entry points. These remain source experiments and are not in preview.3.

## Token exchange and persisted credential

- Authorization-code exchange sends JSON with `grant_type=authorization_code`,
  configured `client_id`, one-time code, exact redirect URI, and PKCE verifier.
- An authorization code causes exactly one token HTTP request. Network errors,
  timeouts, response read failures, HTTP 429 and server errors are ambiguous
  because the provider may already have consumed the code, so none are retried.
- Refresh sends JSON with `grant_type=refresh_token`, configured `client_id`,
  and the latest decrypted refresh token. It uses the same fixed token target.
- A successful response must contain non-empty access, ID and refresh tokens.
  Refresh-token rotation is mandatory: the returned refresh token replaces the
  previous value. OAuth-created credentials are normalized into the existing
  Codex auth JSON envelope and stored with the existing upstream-bound AEAD.
  Tokens, codes, verifier, state and account ID are never returned.
- The account ID is read only from the unverified ID-token payload so the
  existing direct adapter can form its observed request. This is a structural
  extraction, not signature or account verification. Online inference success
  remains the transition that marks an account `verified`.
- Exchange failure creates no upstream and leaves no reusable code. A refresh
  network/read/5xx failure, malformed success response, or failure to save a
  received rotation is an uncertain outcome: ciphertext and revision remain
  unchanged and a durable pause marker prevents reuse of the old refresh token.
  An allowlisted `invalid_grant` or HTTP 401 instead changes the state to
  `reauth_required` under the same revision condition.

## Concurrency, retries, and re-import races

- Manual, background, Chat, Responses and discovery acquisition share one
  per-upstream cancellable lock. A waiter reads the newest revision and
  ciphertext after acquiring it rather than reusing an earlier route snapshot.
- Codex credential replacement and revision-changing administrator updates
  acquire that same lock before the admission lock, then re-read
  `expected_revision`. If refresh wins, a stale administrator mutation
  conflicts; if replacement wins, the later refresh observes the new revision
  and removed provenance without sending the old refresh token.
- The token response is saved only with `WHERE revision = <revision read before
  refresh>`. Administrator re-import/replace therefore wins; a stale refresh
  never overwrites it. Success increments the upstream revision once and clears
  `verified_at` to `imported_unverified` because the rotated credential has not
  completed an inference request yet.
- Before any refresh HTTP request the service persists `in_progress` with the
  credential revision. Network errors, timeouts, response read failures and
  HTTP 5xx are sent once and transition that marker to `paused`; they are never
  replayed automatically or manually. Only an explicit HTTP 429 response,
  excluding `invalid_grant` and 401, uses bounded backoff with a capped
  `Retry-After`, for at most three total attempts, then returns to `ready`.
- The background worker scans at most 16 eligible rows per pass, runs at most
  two refreshes concurrently, and advances a stable cursor so later rows cannot
  be starved by long-lived earlier credentials. Disabled, imported, unknown-
  source and client-mismatched rows are not refreshed or mutated. `Start` and
  `Close` own worker cancellation; request waiters honor their own context.
- While refresh is paused, an access token remains usable until its actual
  expiry. No paused path sends the old refresh token again. Re-import deletes
  both provenance and lifecycle state; a new authorization creates fresh
  provenance and a `ready` marker.
- The refresh endpoint uses the caller's expected revision as an admission
  check. If another refresh or administrator change wins first, the caller gets
  `revision_conflict`; the winning credential remains intact.

## Persistence and recovery

A transactionally migrated `codex_oauth_sessions` table stores only state
digests, AEAD ciphertext for state/PKCE verifier and the client/redirect
snapshot, administrator/session bindings, expiry, use time and non-secret
metadata plus the redacted lifecycle result. Startup marks expired pending
sessions and stranded exchanges; it does not infer success from use time.
`codex_oauth_bindings` is created in a checked transaction and stores the
non-secret client ID plus `authorization_code` provenance under an upstream
foreign key. Legacy credentials intentionally receive no inferred binding.
`codex_oauth_refresh_states` stores only `ready`, `in_progress`, `paused`, or
`reauth_required`, a fixed reason code, attempt revision and update time.
Startup turns every persisted `in_progress` row into `paused` before the worker
starts, closing the process-crash replay window.
Schema initialization and migration are idempotent; any failing statement rolls
back without modifying existing upstream credentials, routes, employees, keys,
or request history. An existing refresh-state table is accepted only when its
upstream key is a real primary key, required columns are `NOT NULL`, the foreign
key cascades, and the complete four-state check constraint is present.

Process-local refresh locks intentionally do not claim multi-process safety;
the current product contract is one Go process and one SQLite database.

## Acceptance boundary

Automated tests use synthetic JWTs and an injected mock transport. They cover
missing configuration, fixed target construction, PKCE/state, session binding,
CSRF/Origin enforcement, TTL, callback replay, single-attempt code exchange,
configuration drift without state consumption, AEAD at rest, OAuth provenance,
token rotation, redaction, invalid-grant/401 reauthorization, bounded 429-only
retry, durable ambiguous-outcome pause, concurrent refresh serialization,
cancellable wait, re-import binding removal, failed-save pause, migration
rollback, fair bounded scanning, restart recovery and on-demand refresh through
Chat and Responses. They do not send real
credentials or requests to OpenAI.

The implemented product surface is backend authorization, redacted session
status, explicit refresh, request-side refresh and a bounded in-process
background scheduler, with matching web-console entry points. Real-account
compatibility and provider-side revocation remain unverified or unimplemented.

## Public protocol sources

Observed implementation details are pinned to OpenAI Codex commit
`44b857c00e5803adedbc5b2e94c4a33574a157fe` and were reviewed on 2026-09-23:

- OAuth authorization flow and PKCE behavior:
  <https://github.com/openai/codex/tree/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login>
- OAuth JSON exchange and refresh wire behavior:
  <https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/oauth/client.rs>
- Refresh orchestration and expiry behavior:
  <https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/auth/manager.rs>
- Official authentication and auth.json safety guidance:
  <https://learn.chatgpt.com/docs/auth>

These are fixed official-client observations, not a promise that CPA Cloud is a
registered or supported third-party OAuth client. A deployment must supply a
client registration whose provider terms and redirect/scopes match this flow.
