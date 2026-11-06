# Frozen application-master-key consumer inventory

2026-10-20. Source is exclusively PR86
`6056270c612bdc3741dfee5a6c0c338cf9926b0c`. No real database, credential,
environment, user directory, data file or production network was inspected.
This is a static source inventory plus a symbolic policy, not an executed
migration. Source locations below are line numbers at that exact parent.

The audit followed all non-test `deriveKey`, `digest`/`equalDigest`, direct
`hmac.New`, `secrets.aead`, Responses wrap/fingerprint and direct M canary uses
under `internal/` and `cmd/`, then their durable writers/readers. It explicitly
includes the direct proxy HMAC, not merely callers of the common digest helper.
New source/callers or unknown historical formats require re-audit and block a
real migration. Global root epochs, exhaustive row enumeration, retirement
transactions and an external anti-restore watermark do not exist in this batch.

Legend: D=persistent database/file, T=transient/client-carried, H=historical
package/operation. `fresh` means a new M2 purpose key; `reencrypt` requires old
authentication plus new M2 key/ciphertext; `retire` preserves required facts and
denies old authority/re-execution, not silent physical deletion; `keep` is
explicit non-M handling. Catalog format labels classify these source encodings;
they do not introduce a production parser or disk format.

## Closed catalog (IDs also occur in the independent model)

| Model ID | Exact source location / actual purpose or format | Residence | Required M2 policy / remaining blocker |
| --- | --- | --- | --- |
| domain-digest | `internal/service/security.go:65,99,105`; HMAC-SHA256 root derivation `cpacloud/employee-and-session-digests/v1` | T | fresh M2 derivative; no M1 key retained |
| domain-aead | `security.go:66-75`; `cpacloud/upstream-credentials/v1`, AES-256-GCM | T | fresh M2 derivative; all below consumers accounted |
| domain-response-wrap | `security.go:76-85`; `cpacloud/response-state-wrap/v1` | T | fresh M2 derivative, not just a new key ID |
| domain-response-fingerprint | `security.go:86`; `cpacloud/response-state-operation-fingerprint/v1` | T | fresh M2 derivative; old operation MACs retired/fenced |
| employee-key | `employees.go:480`, `modelapi.go:123-141`, `self_key_slot_admin.go:227`, `self_key_issue.go:188`; selector + HMAC digest, `digest_version=1`, including pending/armed slots | D/T | revoke every old Key/slot; independent new random Key and new M2 digest; no plaintext/digest transformation |
| admin-session | `admin_auth.go:85,114`; `admin-session/v1`, session digest + CSRF | D/T | retire sessions and associated CSRF authority; bcrypt login hash is separate |
| self-session | `self_service.go:339,537`, `self_one_shot_disarm.go:130`, `self_plan_purchase.go:398`; selector-bound verifier digest | D/T | retire all old self sessions and dependent tokens |
| enrollment | `self_service.go:238,391`; `employee-self-enrollment/v1`, enrollment digest/expiry | D/T | retire all outstanding enrollment grants |
| oauth-state | `codex_oauth.go:203,332`; `codex-oauth-state/v1` digest | D/T | retire OAuth state; never resume an old authorization |
| audit-cursor | `admin_audit.go:35,335,359`; JSON payload + HMAC, `admin-audit-cursor/v3` | T | retire old v1/v2/v3 cursors, never guess a legacy epoch |
| governance-cursor | `governance_observations.go:22,175,205`; payload + HMAC | T | retire old cursor, preserve observations as facts |
| request-cursor | `self_request_history.go:25,140,159`; request cursor payload + MAC | T | retire, do not re-sign unverifiable client tokens |
| key-request-cursor | `self_request_history.go:26,190,209`; Key request cursor payload + MAC | T | retire |
| pool-session | `model_admission.go:192-206`; `pool-session/v1` + employee/Key/model/path, hex HMAC | D/T | retire sticky affinity/reservations safely; no request replay |
| proxy-operation | `outbound_proxy_store.go:679-697,202-240`; direct digestKey HMAC `cpacloud/outbound-proxy-create/v1`, persisted input fingerprint | D/H | retire old input comparison, retain operation-ID success fence; no duplicate create |
| batch-input | `upstream_batch.go:213`; `upstream-batch-input/v1` keyed input fingerprint | D/H | retire old comparisons and fence completed/uncertain operations |
| batch-operation | `upstream_batch.go:317`; `upstream-batch-codex-operation/v1` HMAC | D/H | retire old keyed operation material; preserve no-reexecution facts |
| redemption-code | `billing_commercial.go:23,448,474`, `self_redemption.go:363`; `billing-redemption/v1` digest | D/T | retire old redeem authority; unresolved funded obligations/reissue policy block real commit; do not delete ledger facts |
| upstream-api | `security.go:118-139`, `upstreams.go:209,318`, `upstream_batch.go:241`; nonce||GCM, object-bound OpenAI/Anthropic API credential | D | reencrypt under new M2 AEAD; same legitimate upstream plaintext may remain |
| gemini-api | `security.go:142-163`, `upstreams.go:207,316`, `upstream_batch.go:239`; provider/kind/object AAD | D | authenticated reencrypt under M2 |
| codex-auth | `security.go:166-187`, `codex_membership.go:140,199`, `codex_oauth.go:415`, `codex_refresh.go:404`, `upstream_batch.go:237`; Codex auth JSON AEAD | D | authenticated reencrypt; no refresh/request during rotation |
| oauth-secret | `security.go:190-209`, `codex_oauth.go:193`; temporary verifier/state secret JSON, session-ID AAD | D/T | retire, not migrate an unfinished OAuth flow |
| proxy-credential | `outbound_proxy_credentials.go:67-79`, `outbound_proxy_store.go:234`; bounded credential payload + proxy-ID AAD | D | reencrypt; credential_key_version is not proof of a root epoch |
| webhook-secret | `billing_commercial.go:688-705`; nonce||GCM, `cpacloud/billing-webhook-secret/v1` | D | reencrypt M protection; external webhook HMAC key is independently classified |
| wallet-cursor | `self_wallet_activity.go:27,164-191`; AEAD client cursor | T | retire `cpacloud/self-wallet-activity-cursor/v1` |
| classification-cursor | `self_wallet_entry_classification.go:23,177-204`; AEAD cursor | T | retire `cpacloud/self-wallet-entry-classifications-cursor/v1` |
| subscription-cursor | `self_subscription_status.go:26,141-166`; AEAD cursor | T | retire `cpacloud/self-subscription-status-cursor/v1` |
| catalog-cursor | `self_plan_catalog.go:26,153-178`; AEAD cursor | T | retire `cpacloud/self-plan-catalog-cursor/v1` |
| purchase-quote | `self_plan_purchase.go:31,92-124`; authenticated plan-purchase quote | T | retire old quote; preserve committed purchase receipt/no replay |
| renewal-quote | `self_monthly_renewal.go:27,215-247`; monthly-renewal quote | T | retire quote; do not buy a second successor |
| redemption-cursor | `self_redemption_history.go:25,192-219`; AEAD credit-history cursor | T | retire; monetary facts preserved |
| adjustment-cursor | `self_admin_adjustment_history.go:24,203-234`; AEAD cursor | T | retire; adjustment facts preserved |
| topup-cursor | `self_topup_credit_history.go:23,171-200`; AEAD cursor | T | retire; credit facts preserved |
| response-dek | `response_resources_state.go:351-356,413`, `response_resource_coordinator.go:132`; independently random 32-byte DEK | T/D association | retire old resource DEK authority; this policy does not keep/re-wrap an old DEK and call it full rotation |
| response-wrap | `response_resources_state.go:344,359-378`; wrapped_dek(48) + nonce(12), schema v1/object AAD | D | retire old wrappers with resources; new resources later use new M2 wrap key |
| response-state | `response_resources_state.go:55-64,348,380-419`; item nonce/ciphertext + sequence/type AAD | D | explicit retirement with retained facts; no plaintext/state recovery claimed |
| response-operation | `response_resource_coordinator.go:544-576,645`; length-framed M-derived HMAC | D/H | retire unverifiable old MAC; retain stable operation-ID fence, never re-execute old success |
| response-background | `response_resources_state.go:66-96`, `background_responses.go:101,252,350,368`; queued/authorized/in-progress/terminal task metadata and encrypted input | D/T | retire/quiesce with explicit known outcome; uncertain/in-flight work blocks real commit; never resume on rotation |
| response-managed-tool | `response_resources_state.go:118+`; managed tool run and attempt links | D/H | preserve facts and fence old work; no duplicate execution |
| installation-canary | `master_key_provider.go:225-267`; direct-M AES-GCM canary + provider/instance/key/version AAD | D | regenerate/authenticate under M2, not rename metadata |
| binding-seed | `master_key_provider.go:46-58`, `backup_key_provider_binding_linux.go:37-50`, `internal/offlinerecovery/recovery_linux.go`; exact binding bytes/seed and pinned digest | D/H | coherent new M2 binding; old digest/seed cannot authenticate a new root |
| legacy-root-file | `security.go:18,27-57`; `master.key` base64 raw 32-byte key carrier | D/H | active Linux systemd snapshot must declare zero; legacy-file conversion is not implemented/authorized |
| portable-binding | `internal/recoverymaterial/operations.go:110-130`; HMAC over literal serialized master.key bytes, not the decoded root; inside recovery ciphertext | H | retire old-root portable association, no Windows import/export in this batch |
| backup-v1 | `internal/backup/backup.go:153-154,316,397`; password/scrypt encrypted DB + master.key | H | quarantine old generation; password KDF is independent, package carries old authority |
| backup-v2 | `internal/backup/keymaterial.go:112-113,290,346,404`; W/HKDF DB + master.key | H | quarantine old root; no KEK rewrite or automatic restore |
| backup-v3 | `internal/backup/systemd_keymaterial.go:108,278,320,343,430`; W1/HKDF package, DB + installation binding, repeated role/version identities | H | quarantine M1-bound packages; W1 alone cannot create M2 canary or erase restored old authentication |
| backup-generation | `backup_automation_worker.go`, `backup_automation.go`, `internal/offlinerecovery/recovery.go`; retained run/provider metadata and source-version checks | D/H | preserve run facts, retire old authority, no uncertain run replay; new authoritative epoch/anti-restore floor unimplemented |
| independent-w | `internal/keyprovider/store_linux.go:170`, `internal/backup/systemd_keymaterial.go:343`, `internal/backup/keymaterial.go:404`; independent wrapping key, role exclusion, HKDF | credential/H | keep independent material classification; do not rotate W or admit old M1 restore implicitly |
| bcrypt-password | `config.go:114`, `admin_auth.go:58`, `self_service.go:365,456,638` and self_* password guards | D | keep bcrypt bytes; invalidate sessions separately; restoration/offboarding policy still requires an external fence |
| unkeyed-ledger | `employees.go:514-524`, `self_key_slot_snapshot.go:115`, `governance_management.go:739`, `general_budget.go:767`, `usage_admin.go:502`, `billing_commercial.go:273,316,558` | D/H | preserve unkeyed hashes/immutable financial/operation facts; retire old auth grants and enforce no-reexecution separately |
| independent-checksum | `internal/backup/backup.go:153,500-511`, `internal/keyprovider/store_windows.go:168,238`, `backup_key_provider_binding_linux.go:37`, `scheduled_test_timezone.go:38` | D/T/H | do not rekey SHA-256 integrity fingerprints as M derivatives; refresh binding-associated pins only by the new binding contract |
| pkce | `codex_oauth.go:304`; SHA-256 PKCE challenge of independent verifier | T/D | not M-derived, but retire with old OAuth state; no authorization resumption |
| independent-transient | `security.go:212+`, `admin_auth.go`, `self_service.go`, `background_responses.go`; CSPRNG tokens/CSRF/IDs/claims; webhook HMAC `billing_commercial.go:535` uses external secret | T/D | not blind M derivation; retire old grant/claim authority, keep necessary fact IDs and external webhook secret semantics; no actual random generation in model |

Unless prefixed with another directory, source filenames in this table are in
`internal/service/`. A source line/range is a frozen audit locator, not a claim
that every historical database row uses that format. The catalog deliberately
requires even known-empty families; unsupported old formats and undeclared
families block the model. Duplicate/missing/extra records and pending policy
outcomes cannot be converted into a successful commit.

## Remaining hard unknowns

- Actual full row/caller coverage, feature-off retained state, canary/root epoch
  authenticity, immutable generation manifest, real key generation and ownership.
- Opaque old HMAC and completed/uncertain operation reconciliation without lost
  original inputs. Stable operation facts must survive even when old MACs retire.
- Funding/redemption obligations, old Key delivery/revocation receipts, active
  request/job quiescence, response/financial retention and safe physical removal.
- Transactional database/WAL migration, filesystem commit, process/power-loss
  recovery, credential provisioning, real M1 destruction and M2 activation.
- Authoritative revocation floor outside restored backups, historical-key custody,
  old package retention/retirement, source-version compatibility and real restore.
- Production host/region/owners/permissions, actual deployment and disaster recovery.

These are not accepted by symbolic counters or test provenance flags. They block
actual migration and production. No existing source, schema or operational
contract is silently amended by this inventory. See the [model contract](master-key-rotation-model-contract.md).
