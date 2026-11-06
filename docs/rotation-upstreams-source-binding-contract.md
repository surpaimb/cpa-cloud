# Upstreams offline snapshot: external source-binding contract

2026-10-20. **Documentation only. Draft / real migration and production NO-GO.**
Sole stacked parent is PR92 at
`5e8533470b95420645c99d7e2390527c45d1cdb6`, itself based on PR91
`e8be3fb8ca8d453e66206dd02d868c52e8e0c0d3`. This batch adds only this document
and the [public synthetic case specification](rotation-upstreams-source-binding-cases.md).
All inherited source, tests, schemas, dependencies, workflows, pins and PR bodies
are frozen. No binding verifier, collector, signer, migration or Key reissue is
implemented here. This contract does not authorize their implementation.

## 1. Purpose and non-claims

Define what a future verifier would need before binding **one offline upstreams
snapshot** to an approved instance, M1 root reference/generation and current
external revocation cut. The order is source-binding semantics first; online
WAL acquisition and employee Key reissue are separate later decisions.

The [PR92 reader](sealed-upstreams-reader-contract.md) proves only controlled
sealed synthetic single-table schema/enumeration consistency. The
[PR91 mapper](upstreams-snapshot-manifest-contract.md) has synthetic generation
labels, not authenticated M epochs. Neither provides external source authority.
The [rotation policy](master-key-rotation-model-contract.md) remains symbolic;
accepting this document does not complete any consumer family's M1-to-M2 work.

A digest, intact SQLite file, matching row counts, schema match, successful
canary decryption or AEAD operation is not, alone or together, permission to
trust this snapshot's origin or freshness. A valid old canary may accompany the
wrong database. A suspected-compromised M1 must not sign/MAC its own replacement
source approval. A file and its adjacent metadata can be replaced together.

## 2. Separate observation from expected authority

Use four conceptually different inputs. Names describe responsibilities, not
existing APIs, formats, accounts or deployed trust services.

| Input | Origin and meaning | Cannot establish |
| --- | --- | --- |
| `S`, candidate object; `O(S)`, observations | Exact candidate bytes and observations actually measured from that same object/read lifecycle: length, SHA-256, actual schema/profile and complete upstreams enumeration | Expected instance/provider, M1 identity, allowed source generation, latest revocation watermark or permission to collect |
| `B`, expected baseline | Snapshot-external, explicitly approved trusted source: expected instance, exact allowed object/provider inventory, frozen schema profile, M1 reference/version-to-source-generation mapping and permitted source generations | Its own trust merely because it agrees with `S` or was placed beside it |
| `C`, external capture evidence | Independently authenticated approved collector's statement binding the exact object and digest to the source instance, capture cut, supported acquisition method, M1 relationship and revocation cut | The expected baseline or its own authorization merely by asserting them |
| `A` and `J`, authorization and current external state | Approved purpose/audience-scoped one-use authorization plus independently current policy/revocation/replay state outside the snapshot and its restorable backup | Freshness merely from an unexpired-looking timestamp, local filename, old signature or database counter |

`O(S)` must not generate `B`, `A` or `J`. `B` must not be recomputed from the
candidate rows after a mismatch. `C` must be checked against `B` and `A`, not
treated as a new baseline. Their transport independence is insufficient: their
approval/trust lineage must also be independent of the candidate and M1 under
investigation. A caller boolean such as `trusted=true` is not authentication.

Expected provider binding is per object, not a claim that all rows use one
provider. `B` requires a complete approved object/provider index and all four
provider-category counts, including explicit zeros. The fixed vocabulary is
`openai-compatible`, `anthropic-api-key`, `gemini-api-key`, `codex-membership`.
The observed index/counts come from the snapshot; the expected index/counts come
from the external approved baseline. Coherent omission from both untrusted
inputs must not manufacture authority. A real method for approving a complete
external inventory is **unknown**, not supplied by this document.

There is no M1 epoch column in the fourteen-column profile. `key_version` is
credential encoding, not the M generation. A proposed M1 label read from a
sidecar or supplied by a caller is untrusted. Only an approved external mapping
and verified `C` can relate this exact capture to a non-secret M1 identifier,
its key-management version and a source generation. No M1 bytes are an input
to this documentation exercise or the proposed synthetic model.

## 3. Indivisible binding tuple

`A`, authenticated `C` and `B` must agree on the following tuple, with each
observed field independently checked against `O(S)` where observable:

- Contract/policy version, purpose `upstreams-offline-source-binding`, intended
  verifier/audience, approval ID and external baseline ID/revision.
- Instance identity and exact snapshot object/capture ID. Object identity is
  not a path or basename; identical bytes in a differently authorized capture
  do not authorize that other capture.
- Exact byte length and SHA-256 over **all bytes of the final sealed snapshot**;
  no SQL/text normalization, partial-file hash or checksum of just the sidecar.
  The digest detects byte mismatch relative to an authenticated expected value;
  it is not an authenticator, source identity or completeness proof by itself.
- Frozen schema profile ID/version, source commit
  `5e8533470b95420645c99d7e2390527c45d1cdb6`, and authenticated reference to the
  exact profile descriptor. The profile includes the ordinary upstreams table,
  all fourteen columns, quoted-token/NULL CHECK semantics, PK/UNIQUE/index and
  storage-class requirements; a hash of candidate DDL is not a replacement.
  Fresh/rebuilt layouts remain the PR92 closed profile, not arbitrary SQL.
- Approved full object/provider inventory and category counts, including zero
  and archived/disabled facts; no new authority for any row is conferred.
- Non-secret M1 identifier, key-management version, explicitly allowed source
  generation and the approved external mapping tying all of these to this
  instance and capture. Encoding version is never used as this mapping.
- Capture authorization ID, approved collector identity/role and method,
  immutable capture cut, source quiescence/completion evidence, and the exact
  instance-scoped revocation watermark at that cut.
- External evaluation cut/checkpoint, baseline/revocation/replay revisions,
  authorization nonce, validity bounds and single-use scope. The current
  checkpoint must approve this exact capture cut and tuple, not just its age.

All fields must be covered by the future approved authentication/authorization
mechanism with purpose/version separation and an unambiguous closed encoding.
Missing, unknown, duplicate, ambiguous or extra authority-bearing fields refuse.
The concrete wire encoding, profile-descriptor encoding, signing/verification
scheme and trust-anchor distribution are **unknown**; a real adapter cannot
accept until separately specified, approved and tested. No signature envelope,
issuer key, PKI, TPM, KMS or algorithm upgrade is selected or provisioned here.

## 4. Offline acquisition and freshness gates

The proposed source scope is one supported **offline, quiesced, finalized**
single-table snapshot, not a live product database. No existing arbitrary
database path, caller DSN, online writer or WAL reader becomes an accepted input.
Approved acquisition and custody must establish the cut and exact final object
before hashing, and bind subsequent schema/enumeration to the same object.
Writer state unknown, missing completion proof, object substitution or mutation
before/during/after examination refuses. Existing PR92 seal observations do not
prove protection against a hostile host or eliminate TOCTOU attacks.

An active/incomplete WAL, unaccounted SHM/journal, or simply copying the main
file without approved consistent-capture evidence refuses even if that file
opens and has a plausible digest/schema. An absent companion at inspection is
not proof that no committed WAL content was omitted. SQLite's backup/WAL
documentation explains the acquisition problem; it does not select or approve
our collector. Online WAL acquisition remains outside this batch and contract's
accepted source scope. A real offline acquisition procedure is **unknown**.

`J` must establish the latest authorized instance-scoped revocation watermark
at the explicitly approved evaluation cut from a source outside the snapshot
and its rollback domain. `C`'s watermark must equal the one authorized for that
cut by current `J`; neither a lower nor a fabricated higher watermark passes.
The source generation must belong to `B`/`J`'s explicit allowed set and its M1
mapping, not merely be a large integer. A restored database/backup or signed
old checkpoint cannot lower either requirement.

An offline checkpoint is acceptable only if independently authenticated and
explicitly approved as current for this evaluation, with a bounded validity and
rollback-protected lineage. A stale local cache or the snapshot's clock cannot
claim "latest now". Unavailable freshness, changed baseline/watermark during
verification, ambiguous clock/expiry or unknown authoritative state refuses;
do not silently fall back to an older floor. Acquisition/approval timestamps
alone do not establish revocation coverage.

`A` must authorize this purpose, verifier, instance, object/digest/profile,
capture/M1/generation/revocation tuple and one attempt. Replay checks and any
final one-use decision require external rollback-protected state. A signed
authorization that has been used, expired, revoked, superseded or replayed to a
different object/audience refuses. Durable atomic claim/consume and concurrency
semantics are **unknown**; an in-memory set or a ledger restored with `S` cannot
be represented as real replay protection. No authorization is consumed here.

These gates do not prove employee-Key retirement, GOV-02 backup non-resurrection,
other tables' completeness, financial reconciliation or any root rotation.
Those obligations remain separately blocked even after a future source binding.

## 5. Fail-closed result boundary

A future real adapter must verify approved trust/freshness/authorization before
publishing any candidate result. It must then verify the full tuple, capture
completeness, actual schema and complete same-object enumeration, all resource
cleanup/post-object checks, cancellation and the final external state/one-use
decision. A late failure discards everything. No partial manifest, accepted-row
prefix, resumable authority token, temporary activation or old-M1 fallback may
be returned, logged, cached for use, or passed to a migration consumer.

Success would be a bounded **source-binding receipt**, not ciphertext, M2 keys,
Key reissues, migration commit or serving permission. It must name the exact
object/tuple/policy/cut and its narrow evidence scope; no generic "verified DB"
flag. Success cannot be manufactured by passing arbitrary `O`, `B` and booleans
to an exported constructor. Concrete trusted receipt construction and consumers
require a separate approved implementation contract.

Proposed fixed diagnostic classes for later review are `SB_UNKNOWN_TRUST`,
`SB_BINDING`, `SB_SCHEMA`, `SB_CAPTURE`, `SB_STALE`, `SB_REPLAY`, `SB_IO` and
`SB_CANCELLED`. These are **documented proposed labels, not implemented codes**.
Every refusal returns nil receipt/manifest; never interpolate IDs, providers,
paths, digests, M1 references, SQL, ciphertext, signatures or underlying errors.
Primary and cleanup failures remain fail-closed; no failure can publish a
partially accepted object. Test-only public fixtures may identify a case number.

## 6. Next implementation proposal and acceptance gates

No automatic implementation authorization follows. The next proposed bounded
batch is a **pure public-synthetic binding model**, not a real collector or
adapter and not a modification of PR92's reader:

- Inputs: an untrusted synthetic candidate/observation tuple; an independently
  authored, immutable synthetic expected baseline; external capture/approval
  and current floor/replay-state fixtures. Separate types and constructors must
  keep observations from populating expectations. No real paths, DB, M1 bytes,
  provider access, signer, environment, clock, helper, guest or service.
- Output: either an explicit `synthetic-only` bounded receipt relative to those
  assumptions, or one fixed refusal and nil output. It must not be accepted by
  a production constructor or trigger migration. Authentication/freshness
  assumptions in fixtures are not real cryptographic or durable-state evidence.
- Acceptance: all [specified controls and rejection cases](rotation-upstreams-source-binding-cases.md),
  independent expected anchors, coherent substitution witnesses, nil on every
  late/unknown failure, immutable input/output ownership and no partial-result
  use. A source index and candidate omitted together must not change the fixed
  expected anchor. Synthetic acceptance cannot be named source authentication.
- Before any later real adapter: resolve and independently review **all** real
  collector/custody, approving responsibility, external baseline provenance,
  current revocation service/checkpoint, issuer/verifier/trust-key lifecycle,
  canonical encoding, replay/rollback and transactional publication mechanisms.
  Their status today is **unknown / real acceptance blocked**. Do not fill them
  with M1/canary/self-reported metadata or undocumented human approval.

Documentation review, synthetic policy checks and future cryptographic/storage
tests must be reported separately. Original E/F must review the exact new
document head; no review or CI result is inherited from PR92 for these new texts.

## 7. This docs-only batch: validation and natural CI

Check the two documents' local links and Git diff/file boundary only. No account
helper, guest, service, real DB/backup/credential read, full SQLite test suite,
production scan or runtime test is part of this batch. Case tables are not
executed test evidence. One docs-only commit/push and a new stacked Draft PR;
first unexpected natural red freezes source for read-only diagnosis, with no
patch, rerun, dispatch or runner/workflow switch.

Pre-edit inspection at the frozen parent found:

| Existing workflow | Filter / event | Expected for only these two docs |
| --- | --- | --- |
| [Code validation](../.github/workflows/ci.yml) | PR paths-ignore `**.md`, `docs/**`; push only main with the same ignore | No natural run |
| [Offline CLI two-VM](../.github/workflows/offline-v3-cli.yml) | PR paths allowlisted code/module/scripts/workflow; no docs path | No natural run |
| [Retained two-VM recovery](../.github/workflows/cross-vm-backup.yml) | PR paths allowlisted internal/module/scripts/workflow; no docs path | No natural run |
| [Native package validation](../.github/workflows/preview-release.yml) | workflow_call / workflow_dispatch | No automatic docs-PR run |
| [Preview release](../.github/workflows/publish-preview.yml) | preview-tag push / workflow_dispatch | No automatic docs-PR run |

Do not change workflows or add a dummy code change to force CI. After PR
creation, recheck the exact head/base, run/check inventories and any natural
terminal results. Record absent runs as **not triggered / not executed**, not
green, successful tests or skipped jobs. Record actual skipped jobs separately
if any unexpected natural workflow does run. Prior PR92 green code/VM runs are
historical baseline context only, not new-head execution or source-auth proof.

No Ready, merge, tag, package, deploy, online WAL or Key reissue. Draft and
production NO-GO remain until separately authorized gates, not just this contract.

## 8. Sources, provenance and license boundary

Independently authored on 2026-10-20 from the approved task, frozen project
contracts above and these primary semantic references; no third-party code,
tests, schemas, diagrams or documentation are copied or ported. Prior research
means this is not represented as strict clean-room development. The case tables
are new public symbolic examples, not captured production or reference data.

- [RFC 9334](https://www.rfc-editor.org/rfc/rfc9334.html), sections 4, 7, 8 and 10:
  conceptual separation of evidence, reference values, policy and freshness.
  This is an analogy for roles, **not** a RATS implementation/compliance claim.
- [FIPS 180-4](https://csrc.nist.gov/pubs/fips/180-4/upd1/final): SHA-256 digest
  reference; this contract does not claim validated cryptographic implementation.
- [SQLite backup](https://www.sqlite.org/backup.html) and
  [WAL](https://www.sqlite.org/wal.html): consistent-copy and WAL-state concerns;
  no acquisition code is adopted or executed.
- [GitHub workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax):
  path/event filters; actual repository filters were read before editing.

No dependency, SDK, license grant or NOTICE change is introduced. Referenced
publishers retain their own copyright/license terms; no RFC code component is
extracted. The inherited pinned modernc.org/sqlite remains BSD-3-Clause and
other existing dependencies retain their licenses. No publication of a package
or deployment, production trust authority, or new product security guarantee is
asserted by these documents.
