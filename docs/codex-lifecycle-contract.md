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
  "authorization_url":"https://auth.openai.com/oauth/authorize?...",
  "expires_at":"RFC3339"
}
```

The URL carries an unpredictable state and an S256 PKCE challenge. The state
digest, encrypted verifier, initiating administrator session, name and expiry
are persisted; plaintext state and verifier are never returned by later APIs.
The operation ID is idempotent for the same active administrator session. An
authorization session expires after ten minutes.

`GET /admin/api/v1/codex/oauth/callback?code=...&state=...`

The browser callback requires the same administrator login session that
started authorization. It deliberately does not require `Origin`, because an
OAuth redirect is a cross-site top-level navigation; one-time state, session
binding and PKCE provide callback CSRF protection. It accepts either a code or
an allowlisted provider error, consumes state once, exchanges a code once, and
returns a small no-store HTML success/failure page. It never places a token,
authorization code, upstream body, account identifier or provider description
in that page or a log.

`POST /admin/api/v1/upstreams/{id}/codex-refresh`

```json
{"expected_revision":3}
```

Requests one explicit refresh. The response is the ordinary redacted upstream
view. Missing OAuth configuration returns `codex_oauth_not_configured` and
names the required process flags without exposing secret material. Only
`codex-membership` upstreams are accepted.

The status response adds `features.codex_membership_oauth` and a limitation
when the experiment is enabled but the client configuration is incomplete.
This backend batch does not claim a complete web UI; the web client may add a
button and callback status view in a later batch.

## Token exchange and persisted credential

- Authorization-code exchange sends JSON with `grant_type=authorization_code`,
  configured `client_id`, one-time code, exact redirect URI, and PKCE verifier.
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
- Exchange failure creates no upstream and leaves no reusable state. Refresh
  failure does not update ciphertext, revision, verification timestamp or
  account state, except that an allowlisted `invalid_grant` or HTTP 401 changes
  the state to `reauth_required` under the same revision condition.

## Concurrency, retries, and re-import races

- Refreshes are serialized per upstream within the service process. A waiter
  reads the newest revision after acquiring the lock rather than reusing an
  earlier credential snapshot.
- The token response is saved only with `WHERE revision = <revision read before
  refresh>`. Administrator re-import/replace therefore wins; a stale refresh
  never overwrites it. Success increments the upstream revision once and clears
  `verified_at` to `imported_unverified` because the rotated credential has not
  completed an inference request yet.
- One HTTP operation makes at most three network attempts. HTTP 429 and
  temporary transport/server failure use bounded exponential backoff and honor
  a capped `Retry-After`. Authorization errors and `invalid_grant` are not
  retried. There is no recursive or background retry generator in this batch.
- The refresh endpoint uses the caller's expected revision as an admission
  check. If another refresh or administrator change wins first, the caller gets
  `revision_conflict`; the winning credential remains intact.

## Persistence and recovery

A transactionally created `codex_oauth_sessions` table stores only state
digests, AEAD ciphertext for PKCE verifiers, administrator/session bindings,
expiry, use time and non-secret metadata. Startup deletes expired/used sessions.
Schema initialization and migration are idempotent; any failing statement rolls
back without modifying existing upstream credentials, routes, employees, keys,
or request history.

Process-local refresh locks intentionally do not claim multi-process safety;
the current product contract is one Go process and one SQLite database.

## Acceptance boundary

Automated tests use synthetic JWTs and an injected mock transport. They cover
missing configuration, fixed target construction, PKCE/state, session binding,
CSRF/Origin enforcement, TTL, callback replay, one-time exchange, AEAD at rest,
token rotation, redaction, invalid-grant/401 reauthorization, bounded 429 and
network retries, concurrent refresh serialization, re-import races, failed
save rollback, restart recovery and the existing import path. They do not send
real credentials or requests to OpenAI.

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
