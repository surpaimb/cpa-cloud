# M1 to M2 consumer delta: PR86 to PR129

Status: DOCS-ONLY SOURCE CONTRACT CANDIDATE. No migration or runtime acceptance
was executed in this batch. Draft; real M1 to M2 and production NO-GO.

## Fixed source and counting boundary

| Name | Exact commit | Full tree |
| --- | --- | --- |
| O: PR86 audit baseline | `6056270c612bdc3741dfee5a6c0c338cf9926b0c` | `7daea58dabfcaf65ea0036b48a1085335d5ebd0d` |
| N: PR129 candidate | `df8ad1cf78bfd9b01615d6c5293c01672a411139` | `dfa3f186142619a7973f8e2d07252a0fc4d9657e` |

[PR86](https://github.com/surpaimb/cpa-cloud/pull/86) and
[PR129](https://github.com/surpaimb/cpa-cloud/pull/129) were independently
re-pinned open/Draft. PR129's base is PR128
`5747cb304296f56e99b6ede8b3dc05fb74651108`.
O to N is 22 commits / 64 changed blob paths, including 15 non-test Go paths:
7 existing / 8 NEW. Recursive, non-truncated tree comparison independently
matches the compare endpoint's path set. These counts describe source changes,
not consumers migrated, tests executed, records enumerated or approvals.
This new document alone is stacked on exact N; none of those Go files changes.

The [old catalog](master-key-rotation-consumers.md) is retained at N as blob
`44f3434904b4cfd59737616d6565b73961b4b26e`; it describes O source positions.
The document itself did not exist in O's tree. Its 53 IDs are policy families,
not 53 files or a complete current database inventory. Static string extraction
finds the same 53 IDs in order in N's symbolic Catalog; no model was executed.
Old source positions must not be silently read as N line positions.

## Existing ID aliases used below

These are reading aliases, not new catalog IDs, schemas or admitted formats.

| Alias | Exact existing catalog IDs | Persistent/material boundary |
| --- | --- | --- |
| K | employee-key; domain-digest | selectors and M-derived verifier digests; revoke old grants, independently reissue when separately authorized |
| S | self-session | self-session verifier and dependent CSRF authority; retire |
| E | enrollment | enrollment digest/expiry; retire |
| P | bcrypt-password | bcrypt bytes are not M-derived; old rotation policy keeps them, separate offboarding may delete current rows |
| AS | admin-session | administrator session/CSRF, separate from administrator bcrypt |
| U | upstream-api, gemini-api, codex-auth; domain-aead | authenticated upstream plaintext/ciphertext; existing key_version is not an authenticated external M generation |
| C | independent-checksum | unkeyed integrity/change observations; never evidence of source provenance or an M-derived key |

The backup paths also touch existing backup-v1/v2/v3, backup-generation,
binding-seed and independent-w families. Audit/model revision facts relate to
the catalog's unkeyed-ledger fact-preservation concern; that analogy does not
silently admit a new audit format or prove exhaustive caller coverage.
All remaining catalog families keep their existing policy and UNKNOWN coverage.

## Fifteen-path source ledger

All entries are blob mode `100644`. Every N link below is pinned to exact N.
NEW means absent in O, not an empty old blob. Functions are material-path
locators, not an exhaustive function list or execution evidence.

| # | Frozen N source locator | O blob | N blob | Scope, existing ID association and changed surface |
| --- | --- | --- | --- | --- |
| 1 | [internal/backup/backup.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/backup/backup.go#L702) | `c3d54ea3af923ed6e246b5ab125dea50e4698d0f` | `69dcbfd7f140fcd172a2327fca3ad38024899d6b` | real restore path; AS/S/E/P/K/C; H packages. sanitizeRestoredDatabase[WithOps], restoredRowsMatch, admitRestoredSelfState. |
| 2 | [internal/financial/actor_migration.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/financial/actor_migration.go#L153) | `f3478c89d2b1bf1c39a2fc63c4590b7a83615389` | `36f4200557f1a55e7d6ba28c5aefc9fc3ed63d01` | comment only; no new M consumer; dependency caveat. beginActorWriteTx version comment only. |
| 3 | [internal/masterkeyrotationmodel/model.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/masterkeyrotationmodel/model.go#L69) | NEW: absent in O | `021b34c0377ff46ade245f3ffff4e4429d3bd7a2` | symbolic only; all 53 policy IDs; not actual material. Catalog, Prepare, Verify, Commit, Admit*. |
| 4 | [internal/selfschema/schema.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/selfschema/schema.go#L6) | NEW: absent in O | `163433de9146d51a2ebadc9bfa32464176f32fb9` | shared DDL constants; S/E/P schema, no crypto. Credentials, Sessions, ExpiryIndex. |
| 5 | [internal/service/account_lifecycle.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/account_lifecycle.go#L67) | `c14ccdf3e61cf3734cc76013e48dad0699397e01` | `bdcf9da5bcc4959c3ad73315df09ca3e21a5039f` | real admin path; U and OAuth cleanup; unkeyed audit facts. updateModel, archiveModel, archiveUpstream. |
| 6 | [internal/service/app.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/app.go#L490) | `3925e9100d2bd3afda06d776a8a158306723c6bc` | `894702fbe7197fde307b0e2c9407dcaa48a4e0f4` | real HTTP wiring; K/S/E/P through retirement handler. Handler: retire-credentials route. |
| 7 | [internal/service/employee_retire_credentials.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/employee_retire_credentials.go#L132) | NEW: absent in O | `ce0f777bef481716838070ebaad7a7917e37fce1` | real retirement path; current DB K/S/E/P; no AS/global epoch. retireEmployeeCredentials[Tx], final fact helpers. |
| 8 | [internal/service/employees.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/employees.go#L551) | `c52b8a4d0911cca9fd1e79bc7e87f613716dffa6` | `6c06acb85bf41e96c3c51809a4f9cb5d44650fbe` | real admin path; K; existing disable S/E/P, C fingerprint. updateEmployee/updateModelPolicy error response; revokeKey. |
| 9 | [internal/service/self_store.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/self_store.go#L17) | `5cd81589823a8c27d4b8fe702405e336b5a56fd2` | `fff18d9020e7f932b8acb4871f2574473f7ef4b9` | DDL reference extraction; S/E/P, original semantics retained. selfSchema uses selfschema constants. |
| 10 | [internal/service/upstream_rotation_shadow_candidate.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/upstream_rotation_shadow_candidate.go#L86) | NEW: absent in O | `347c3df1fb5970d9e9fe6e1d830ce79f3c95306f` | synthetic fixture only; U surrogate ciphertext in temporary candidate. upstreamShadowRun/Open/Encrypt/CheckSchema/VerifyRows. |
| 11 | [internal/service/upstreams.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/upstreams.go#L434) | `2abfc283203c1dbb18219b2c5a05d82a6e2af553` | `8a2e8141b9bcc8c8003e89b7fea7e2af3a6f02f0` | real model-create path; U reference, unkeyed model/audit facts. createModel, modelCreateAudit* snapshots/readback. |
| 12 | [internal/upstreambindingmodel/model.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/upstreambindingmodel/model.go#L327) | NEW: absent in O | `27343bfba15590e26040bf0021ce2e93cd98f055` | symbolic only; U expectations/authorization assumptions. evaluateSynthetic, checkCheckpoint. |
| 13 | [internal/upstreamfixturereader/reader.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/upstreamfixturereader/reader.go#L111) | NEW: absent in O | `e3b9732e10f610d11f1e20626d7b4add32389f07` | test-owned sealed fixture; U/C observations; not M1 provenance. readFixture, verifySeal, scanTable. |
| 14 | [internal/upstreamfixturereader/schema.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/upstreamfixturereader/schema.go#L240) | NEW: absent in O | `9283dd8b05ff4dcdfd1fa5132bdaa275522ddee5` | test-owned schema validator; U schema admission, no M key. tokens, knownDDL, checkSchema/checkIntegrity. |
| 15 | [internal/upstreammanifest/manifest.go](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/upstreammanifest/manifest.go#L291) | NEW: absent in O | `24d6e7d448605fa95231fe3538f94f312ec3062b` | symbolic mapper only; U disposition plan, no ciphertext rewrite. Build, rowValues; frozen SourceHead/profile. |

The ledger partitions as six synthetic/model/fixture paths (3, 10, 12-15),
two DDL extraction/reference paths (4, 9), one comment-only Go change (2),
and six real runtime paths (1, 5-8, 11). "Real path" means existing Go service
or restore control flow, not real credentials/data, successful execution or
permission to activate a root. Synthetic private capabilities and declared
interfaces are scope evidence; they do not authenticate the host or issuer.

## Durable effects and what does not follow

1. **Restore / S-E versus P-K.**
   [The shared sanitizer](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/backup/backup.go#L713) opens the
   staged restored database, admits the expected self schema, checks foreign
   keys, and transactionally deletes self sessions and nulls enrollment fields.
   This D2 increment does not delete or rekey bcrypt, access-key verifiers,
   Key/slot state or financial facts. Existing administrator/OAuth session
   cleanup and pausing uncertain OAuth refresh remain separate pre-existing
   behavior, not a new root-rotation implementation.
   The unchanged [v2 caller](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/backup/keymaterial.go#L190)
   and [v3 caller](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/backup/systemd_keymaterial.go#L176)
   reach that same sanitizer; v1 Restore also calls it. Their blobs are
   `a4b02208d0ea17692efec7e0aebdece18d5831c0` and
   `a72a447c4891b45b119b66389c454bad2427b47e`, unchanged from O.
   An old snapshot can therefore restore old bcrypt, Key/slot or disabled-state
   facts. Clearing S/E does not supply a revocation floor outside backups.
   Commit/close errors can leave uncertainty; do not infer rollback or retry.
   Private test adapters are not proof of real I/O or power-loss faults.

2. **Current-DB retirement / K-S-E-P.**
   [The added admin-authorized route](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/app.go#L490)
   calls [the retirement handler](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/employee_retire_credentials.go#L80).
   Under admission lock and a DB transaction it checks expected employee
   revision, disables/increments the employee, enumerates/revokes every current
   unrevoked Key including expired/dormant entries, validates reverse slot
   ownership, cancels pending/armed slots and removes their arm facts, deletes
   that employee's self sessions and entire self credential row, then checks
   final facts and the retained audit before commit. Issued/cancelled slots are
   not described as newly cancelled. Deleting a credential row also deletes
   its current bcrypt/enrollment, unlike general M2 rotation's P-keep policy.
   This operates on one current DB lineage; it does not erase old backups,
   provision future Keys, revoke AS globally or establish an external epoch.
   No new M derivation or activation is introduced by this transaction.

3. **Single-Key revoke and employee updates.**
   [revokeKey](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/employees.go#L551) adds transactional
   Key/slot and audit/final-state checks. Existing selector/digest material is
   not transformed into M2; current grant authority is revoked. The other two
   hunks give invalid revisions explicit HTTP 400 responses in updateEmployee
   and updateModelPolicy. Disabled-employee deletion of current self sessions
   and credentials already exists in O; do not count it as newly introduced.
   Existing keyCreateFingerprint is unkeyed SHA-256, not a new M consumer.

4. **Upstream/model lifecycle and retained facts.**
   [updateModel/archiveModel/archiveUpstream](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/account_lifecycle.go#L67)
   and [createModel and modelCreateAudit helpers](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/upstreams.go#L434)
   strengthen transaction-visible audit/model/upstream/OAuth final-state checks.
   Upstream archival's ciphertext clearing, key_version reset and OAuth
   binding/refresh deletion are already present in O; these are not a new
   M1-to-M2 re-encryption loop. Ordinary API credential create/update continues
   to use the existing secrets object. [newSecrets](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/internal/service/security.go#L61)
   is unchanged blob `d3d4356ab4d23498c2091fe1a2480a46fc802ac5`;
   digest, upstream AEAD, response-wrap and response-fingerprint derivation
   domains do not become new externally authenticated generations here.
   Checkpoint-visible tuples do not prove absence of transient trigger writes,
   physical row identity, all concurrent consumers or crash durability.

5. **DDL and synthetic surfaces.**
   The three selfschema literal byte strings each occur exactly once in O's
   self_store.go and are unchanged when extracted; N's selfSchema references
   those constants. This source comparison is not a new migration/DDL change.
   Catalog/Prepare/Verify/Commit are symbolic policy operations, not SQL/root
   transactions. The private shadow fixture may use real crypto/SQL on its
   temporary candidate, but has no production constructor/cutover; controlled
   return/nonce/commit simulations are not actual entropy/I/O failures.
   Binding evaluateSynthetic takes external assumptions, not verified issuers
   or atomic consume. readFixture is an unexported sealed test-owned reader;
   its hash/identity checks observe change, not M1 authenticity or custody.
   Schema tokens/knownDDL are frozen fixture admission, not a production
   historical-format migration. Manifest Build maps public symbols with its
   own frozen SourceHead/profile; it does not establish N production readiness,
   read real DB credentials, authenticate M generations or activate anything.
   No new synthetic API is silently inserted into the old closed catalog.

## Actual dependency delta

This is an actual dependency selection change, even though actor_migration.go
itself changes only its modernc version comment. Compare
[N go.mod](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/go.mod#L5) with
[O go.mod](https://github.com/surpaimb/cpa-cloud/blob/6056270c612bdc3741dfee5a6c0c338cf9926b0c/go.mod#L5).
Five requirements change; golang.org/x/crypto remains v0.42.0.

| Module | O requirement | N requirement |
| --- | --- | --- |
| `golang.org/x/sys` | `v0.36.0` | `v0.42.0` |
| `modernc.org/sqlite` | `v1.38.2` | `v1.46.2` |
| `github.com/ncruces/go-strftime` | `v0.1.9` | `v1.0.0` |
| `golang.org/x/exp` | `v0.0.0-20250620022241-b7579e27df2b` | `v0.0.0-20251023183803-a4bb9ffd2546` |
| `modernc.org/libc` | `v1.66.3` | `v1.70.0` |

go.mod blobs: O `13f5f902cab490c7fe36d914120577f220557bc8`,
N `9eb72d43d174ad11507e819c19ced2a9c1c1a7d1`.
[go.sum](https://github.com/surpaimb/cpa-cloud/blob/df8ad1cf78bfd9b01615d6c5293c01672a411139/go.sum) blobs: O
`894a8c05a53ea39da467fe1ebeeaf057e382f985`, N
`7892f615d32d888476838c9a148e912c819d8c04`; corresponding N module
and go.mod checksum entries are present for all five. This is not an executed
resolver, binary/SQLite-engine-version check, compatibility or cancellation
acceptance. Upgraded native jobs cannot silently close a prior row-step
cancellation failure. Dependencies retain their licenses; this batch introduces
no dependency, copied vendor source or new license/audit-completeness claim.

## Blocking unknowns and acceptance contract

- Actual durable row/feature-off/caller coverage for all 53 families; unknown
  historical encodings, new SQL readers/writers and unclassified materials.
  Any undeclared family/format/caller blocks migration pending a fresh audit;
  never silently append it to the closed catalog or treat "no rows observed"
  as known empty.
- Trusted real M1 origin, externally authenticated M2 generation/issuer,
  source-to-generation binding, custody, latest revocation floor, atomic
  authorization consumption and backup-external anti-rollback/anti-revival
  (GOV-02). Public symbolic labels and key_version do not provide these facts.
- Cross-consumer quiescence, in-flight requests/background work and operation
  no-reexecution fences; preserved ledger/audit facts; physical commit/WAL,
  power loss, restart, real Key delivery and historical backup disposition.
- Prior red/STOP records remain. User-supplied coordination history includes
  PR89 C#9 / 0-15, PR97 timely row-step cancellation FAIL and PR100's first
  Code501 / AKR_SLOT_TRIGGER_INSTALL root cause unknown. None is re-run,
  repaired, reclassified or cleared by this document or another green job.
  These are retained historical constraints, not fresh live CI queries.

This batch performs source/ID/DDL/link/whitespace/diff checks only. No Go,
product, SQL, restore, credentials, live ports, test executable or workflow
dispatch is run. Documentation checks are not native or end-to-end tests.
No real M1-to-M2, full 53-consumer migration, trusted external anchor, GOV-02,
product or production GO is claimed. Original E/F formal same-head read-only
review belongs to root after publication; no such approval is created here.
A docs-only natural workflow observation, if zero runs/checks, is
NOT_TRIGGERED/NOT_EXECUTED, never CI green.

## Provenance and unchanged scope

Independently authored from this repository's fixed Git trees/blobs and its
[working rules](../AGENTS.md), [README](../README.md),
[product plan](product-plan.md), [independent implementation rules](independent-implementation.md),
[development plan](development-plan.md), [preview contract](preview-contract.md)
and [feature-parity plan](feature-parity-plan.md). Governance reading used
bounded relevant excerpts; this is not a claim to have re-audited every README
paragraph, unchanged caller or historical format. No archived CPA,
CLIProxyAPI or Sub2API implementation/tests/docs were copied or ported;
previous exposure means no strict clean-room claim is made.
Only docs/master-key-rotation-consumers-delta-pr86-pr129.md may be added.
The old catalog, all source/DDL/dependencies/workflows, PR129 body, main-tree
WIP and Windows worktrees remain unchanged. Earlier stopped inventory batches
remain STOP and are not retrospectively made successful by this fresh batch.
Draft only; no Ready, merge, tag, package, deployment or real migration.
