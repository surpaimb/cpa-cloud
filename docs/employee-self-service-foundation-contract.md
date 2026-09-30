# ID-05 employee self-service foundation contract

Status: development-preview proposal, default off. This contract precedes implementation. Passing its tests does not complete GOV-02, authorize production use, or claim compliance with any jurisdiction's privacy law or provider terms.

## Boundary and provenance

This is an independently specified CPA Cloud feature for single-tenant internal company use. It uses the existing `employees` identity and the Go service's own SQLite store and web console. No external identity provider, membership protocol, payment system, desktop client, or employee API Key is involved. The implementation must be newly authored from this contract and public Go, HTTP, SQLite, and browser behavior; it must not use archived CPA, CLIProxyAPI, Sub2API, or `../cpa-cloud-reference` as templates. Record any new dependency and its license; prefer existing dependencies. No third-party SDK is needed for self-service.

## Exposure and roles

- `--employee-self-service-enabled` is false by default. When false, all `/self/` documents and APIs return 404, and admin enrollment is unavailable. Static SPA fallback must not reveal a functional self-service screen.
- When true, an independent `/self/` browser page supports enrollment, login, a read-only personal profile, and logout. The page must work at desktop width and 390 px width.
- Only an existing admin may issue an enrollment secret to an active employee who has no self-service password. Admin creation and employee enable/disable remain admin-only. There is no self-registration, email, SSO, password recovery, password change, Key/usage/billing/membership/payment view, or new admin configuration UI in this stage.
- The admin issues a 32-byte cryptographically random one-time enrollment secret. The response displays its plaintext once. Storage contains only a purpose-separated keyed digest. Reissue replaces the old digest atomically and invalidates the old secret. Expiry is 15 minutes. A disabled employee cannot issue, redeem, log in, or use an old session.
- Successful redemption atomically consumes the enrollment digest, stores a cost-12 bcrypt password hash, and creates a separate self session. Exactly one concurrent redemption may succeed. A failed or uncertain database commit must not emit a cookie or success. If the password commit succeeds but the HTTP response is lost, the employee can log in using that password; no secret replay is permitted.
- Passwords are 12–72 UTF-8 bytes and are never stored or logged in plaintext. Employee id plus password is required for login. Login and redemption failures have uniform public error messages for nonexistent, disabled, unregistered, expired, consumed, or wrong credentials. Rate limiting applies independently to peer address and employee id, with bounded in-memory state; it cannot be bypassed by changing one dimension alone. Dummy bcrypt work masks nonexistent records. A rate-limit response may differ from an authentication failure.
- The self session is a new, random selector and 32-byte verifier. Store only a purpose-separated keyed verifier digest. Compare digests in constant time after selector lookup. The `cpa_self_session` cookie is `HttpOnly`, `SameSite=Strict`, `Path=/self/`, `Secure` when TLS is configured, and expires no later than 2 hours. Session expiry is absolute, not sliding. The admin cookie and employee API Key cannot authenticate `/self/`; the self cookie cannot authenticate `/admin/` or model APIs.
- All self writes require a matching `Origin` and a custom request header. Authenticated writes additionally require a per-session CSRF token compared in constant time. Reads reject a mismatching `Origin` if one is present. Responses are `Cache-Control: no-store`, and self pages disallow framing. Browser code does not persist any secret to local storage. TLS verification remains enabled; remote listener access still requires configured TLS per the base preview contract.
- Each authenticated self request checks the current employee status in the same store read as the session. Disabling an employee deletes their self credential, outstanding enrollment, and sessions in the same administrative transaction; re-enabling requires new enrollment. Failure of this transaction fails closed and must not leave a partially disabled identity. Existing admin/API Key behavior remains independently governed by the base contract.

## API and data shape

All bodies are bounded JSON with unknown fields rejected; malformed or duplicate security fields are rejected. Neither admin nor self endpoints return another employee's personal data.

| Method and path | Authorization | Input | Successful response |
| --- | --- | --- | --- |
| `POST /admin/api/v1/employees/{id}/self-enrollment` | Admin session, Origin, CSRF | Empty JSON object | `201` with `employee_id`, `enrollment_secret`, `expires_at` (only plaintext exposure) |
| `POST /self/api/v1/enroll` | Matching Origin, custom header | `employee_id`, `enrollment_secret`, `password` | `200` with CSRF token and own profile; self cookie |
| `POST /self/api/v1/sessions` | Matching Origin, custom header | `employee_id`, `password` | `200` with CSRF token and own profile; self cookie |
| `GET /self/api/v1/session` | Self session | CSRF token and own profile |
| `GET /self/api/v1/profile` | Self session | Profile only |
| `DELETE /self/api/v1/sessions` | Self session, Origin, CSRF | Empty | `204`, cleared self cookie |

Profile is exactly `id`, `name`, `department`, `status`. In particular it excludes `note`, model policy, admin metadata, other employees, upstream credentials, and API Keys. The UI must not render or cache those excluded fields.

New SQLite tables are additive and independent of existing admin `sessions` and `access_keys`. They hold a per-employee password hash and optional enrollment digest/expiry, plus self session selector, keyed verifier digest, CSRF token, and expiry. The migration is transactional and idempotent on an existing DB. New tables and indexes are schema-validated on startup; a malformed pre-existing table or index fails startup instead of silently accepting weaker storage. No existing row/table is replaced or dropped. Writes that cannot be durably committed return service unavailable without success material.

## Data inventory, access, and retention

The admin already holds employee id, name, department, and status. This feature adds only bcrypt password hash, keyed enrollment digest/expiry, keyed session verifier digest/expiry, and CSRF token in the service database. The one-time enrollment plaintext and login password exist transiently in the admin/employee browser and request memory; server responses never repeat them. The service must not log either, cookies, authorization headers, prompts, or model responses. The employee can read only their own four profile fields; the admin can issue enrollment but cannot read password hashes or session verifiers through an API.

Enrollment expires after 15 minutes and is removed on redemption, reissue, employee disable, and startup cleanup after expiry. Self sessions expire within 2 hours and are removed on logout, disable, and startup cleanup. Password hashes remain while the employee is active and registered; disable removes them, so re-enable requires re-enrollment. Existing employee records and backup snapshots are outside this new feature's deletion path and may outlive these credentials under the base retention policy. Operators must account for snapshot retention and restore before production use. This is a narrow internal-only rationale, not a claim of GOV-02 completion or legal/terms approval. If local notice, lawful basis, or deletion requirements cannot be established for a deployment, keep the flag off; admin-only employee management and API Keys remain the narrower alternative.

## Verification gate

Automated tests must cover default-off 404, role/cookie isolation, admin-only issuance, reissue invalidation, single-use and concurrent redemption, expiry, login uniformity/rate limits, Origin and CSRF rejection, disabled-session invalidation, persistence/commit failure, restart recovery, migration of an old DB without loss, and malformed schema fail-closed. Verify browser desktop and 390 px behavior against a dynamic non-8787 local process. Run Go tests, race tests for stateful paths, vet, CLI and web build/lint/tests, and the actual GitHub CI. Report tested behavior separately from planned behavior. Keep the PR draft until independent acceptance and CI pass; no deployment, release, tag, or package follows from this contract.
