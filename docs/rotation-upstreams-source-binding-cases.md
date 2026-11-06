# Upstreams source binding: public synthetic case specification

2026-10-20. **Documentation only; no cases below have been executed as tests.**
Draft / production NO-GO. Exact sole parent PR92:
`5e8533470b95420645c99d7e2390527c45d1cdb6`. Only this file and the
[binding contract](rotation-upstreams-source-binding-contract.md) are added.
No real DB, credential, M1 key, backup, account or service is an input.

## 1. Independent fixture construction rule

`O` is the candidate's observations. `B`, `C`, `A`, `J` have the distinct
external roles defined in the contract. All identifiers below are **public
symbol names**, not bytes, real IDs, URLs, paths, hashes, secrets or signatures.
For a future synthetic model, write `B0`/`J0` as separate literal expected
fixtures first; never call `expected = candidate`, copy a candidate-generated
index into `B0`, recompute the expected digest from a mutated candidate, or make
the expected root/generation/floor follow candidate metadata.

The independent anchor must remain unchanged while candidate inputs mutate.
External-state mutation cases instead explicitly supply a **separately authored
new trusted checkpoint**; the candidate does not choose or update it. A fixture
tag like `approved` is an explicit synthetic assumption, not a real verifier,
signature, authoritative operator approval or trusted-source implementation.

| Independent fixture | Literal public assumptions for the sole positive control |
| --- | --- |
| `B0` | Approved baseline `BASE-A/rev-3`; instance `INST-A`; object/capture `SNAP-A/CUT-A`; length `LEN-A`; SHA-256 symbol `DIGEST-A`; frozen PR92 profile `PROFILE-A`; inventory `ROW-A:openai-compatible`, `ROW-B:codex-membership`; counts OpenAI=1, Anthropic=0, Gemini=0, Codex=1; M1 non-secret reference/version `ROOT-A/RV-A`; allowed source generation set exactly `{GEN-7}` |
| `J0` | Independently current instance `INST-A` checkpoint `EVAL-A`; baseline revision 3; exact authorized capture watermark `REVOC-9`; current explicit generation/M1 mapping agrees with `B0`; authorization `AUTH-A` unused, unrevoked, within independently approved bounds |
| `C0` | Assumed-authenticated external capture evidence ties `SNAP-A`, `DIGEST-A`, `LEN-A`, `PROFILE-A`, expected inventory, `INST-A`, `CUT-A`, `ROOT-A/RV-A`, `GEN-7`, `REVOC-9` and authorization `AUTH-A`; supported quiesced/offline/finalized acquisition, no unaccounted companions or late mutation |
| `A0` | Assumed independently authorized one-use purpose `upstreams-offline-source-binding`, audience `VERIFIER-A`, exact `B0/C0/SNAP-A` tuple and `EVAL-A`; it is not issued with M1 and cannot authorize another capture, purpose or audience |
| Candidate `S0/O0` | Independently authored candidate object/observations agree with those literals; ordinary full PR92 single-table schema, two public synthetic legal rows, complete observed inventory, all four counts; no derived M epoch in `key_version`; all cleanup and post-object observations succeed |

Equal public symbols mean equal hypothetical bindings only. They do not prove
an actual digest, signature, CSPRNG, collector, clock, quiescence, object identity
or external anti-rollback storage. `LEN-A` and `GEN-7` are notation, not a new
wire format or product data limit. A real candidate with these string labels
and no independently verified external mechanisms must be rejected.

## 2. Positive and limitation controls

| ID | Input/control | Proposed future model expectation | Evidence limit |
| --- | --- | --- | --- |
| P01 | Independent `S0/O0`, `B0`, `C0`, `A0`, `J0`, all final checks complete | One complete explicitly synthetic-only receipt for this tuple | Not real authentication, authorization consumption or rotation |
| P02 | Separately authored `B-EMPTY/C-EMPTY/A-EMPTY/J-EMPTY` approve one empty snapshot; all fourteen schema columns and all four zero counts independently present | Synthetic-only receipt with zero rows, never skip binding/freshness/schema gates | Empty data does not prove source trust or absence of other product consumers |
| P03 | PR92 supported fresh/rebuilt physical column order differs, but named schema, independent index and tuple agree with separately authored approved control | Same bounded synthetic policy outcome | Not arbitrary layout compatibility or a schema parser test in this docs batch |
| P04 | Unknown real collector/issuer/baseline/verification mechanism, even with otherwise equal public fields | `SB_UNKNOWN_TRUST`; nil receipt/manifest | Unknown is not a production-positive default |

No positive control grants runtime serving, M2 encryption, Key reissue, backup
restore or employee authorization. A narrow receipt cannot be used as a generic
`trusted=true` capability. All real external mechanisms remain unknown.

## 3. Required independent-anchor rejection table

Proposed labels below are not existing runtime codes. Each row is an
independently constructed single-fault example unless a coordinated attack is
explicitly named. Preserve `B0/J0` except where the row declares a new external
checkpoint. Every rejection means **nil receipt and nil manifest, no partial
results emitted or used**, including zero output before late failures.

| ID | Candidate/evidence or explicit external-state change | Anchor that must stay independent | Required outcome / guard |
| --- | --- | --- | --- |
| R01 wrong instance | `S/O/C` claim `INST-B` while internally agreeing | `B0/A0/J0` still authorize `INST-A` | `SB_BINDING`; instance is not learned from the candidate |
| R02 wrong snapshot | Present `SNAP-B/CUT-B` with even byte-identical `DIGEST-A` | `B0/C0/A0` authorize only `SNAP-A/CUT-A` | `SB_BINDING`; byte equality does not authorize another object/capture |
| R03 changed bytes | Object identity label unchanged, actual observed digest becomes `DIGEST-B` | Authenticated expected digest remains `DIGEST-A` | `SB_BINDING`; no rehash-and-reapprove shortcut |
| R04 length/coverage | Candidate reports a prefix digest, shortened length or sidecar-only digest | Exact full-object `LEN-A/DIGEST-A` | `SB_BINDING`; whole object required |
| R05 schema mismatch | Plausible header/profile claim but actual schema has extra table, column, index or weak quoted-NULL CHECK | Approved full `PROFILE-A` from frozen PR92 | `SB_SCHEMA`; matching declared profile/digest is not actual schema evidence |
| R06 old source generation | Capture proposes `GEN-6`, even with an intact old database | Allowed set is exactly `{GEN-7}` | `SB_STALE`; no implicit old-generation fallback |
| R07 invented future generation | Capture proposes `GEN-8` to satisfy a numeric minimum | `B0/J0` explicitly map only `GEN-7` | `SB_STALE`; unknown higher generation is not automatically safe |
| R08 old revocation watermark | Capture is at `REVOC-8` | Current external `J0` requires exactly `REVOC-9` for this cut | `SB_STALE`; database/backup cannot lower the floor |
| R09 fabricated newer watermark | Candidate sidecar claims `REVOC-10` without an approved external cut | `J0/C0/A0` authorize `REVOC-9` | `SB_STALE`; greater-than comparison is insufficient |
| R10 replayed authorization | Reuse `AUTH-A`; separately authored current `J-USED` records it already used | External replay history, not a set restored with the candidate | `SB_REPLAY`; no second receipt, activation or fallback |
| R11 old valid canary, wrong DB | A valid old canary associated with `ROOT-A` accompanies `SNAP-B` | `B0/C0/A0` bind only `SNAP-A/DIGEST-A` | `SB_BINDING`; successful canary/decryption cannot repair source mismatch |
| R12 coherent replacement | Replace snapshot, observed inventory/digest and self-reported sidecar together so all three agree | `B0`, independent issuer trust and current `J0` remain unchanged | `SB_BINDING` or, for an unauthenticated replacement `C`, `SB_UNKNOWN_TRUST`; agreement inside attacker-controlled inputs is not authority |
| R13 omitted row in both untrusted lists | Remove `ROW-B` from snapshot and candidate-generated index/counts | `B0` still lists both rows and complete categories | `SB_BINDING`; a self-consistent smaller manifest is not complete |
| R14 wrong provider | `ROW-B` changes provider and candidate metadata follows | Independent `B0` still binds `ROW-B` to `codex-membership` | `SB_BINDING`; no provider expectation learned from rows |
| R15 wrong M1 relationship | Capture claims `ROOT-B/RV-B`, or correct label with unapproved key-management version | Approved external `ROOT-A/RV-A` to `GEN-7/INST-A/CUT-A` mapping | `SB_BINDING`; encoding `key_version` cannot substitute |
| R16 M1 self-approval | A candidate and matching canary/sidecar are signed/MACed using M1 under investigation | Approved independent external issuer/verifier trust, not M1 | `SB_UNKNOWN_TRUST`; a potentially leaked root cannot vouch for itself |
| R17 incomplete WAL | Main file opens; committed WAL state is missing, unaccounted or capture completion unknown | External approved offline capture method/completeness requirement | `SB_CAPTURE`; successful open/integrity/digest does not cure omitted state |
| R18 copied main file only | `COPY_MAIN_ONLY` operation with plausible clean header and no companion seen afterward | `C0/A0` require approved quiesced/finalized consistent acquisition and custody evidence | `SB_CAPTURE`; absence of companions later is not evidence of a complete capture |
| R19 unauthorized collection | Correct bytes came from an unapproved collector, or capture authorization is absent/mismatched | External approved collector role and exact `AUTH-A` collection scope | `SB_UNKNOWN_TRUST`; equal bytes do not grant collection authority |
| R20 expired/revoked/superseded approval | Separately authored current `J-EXPIRED`, `J-REVOKED` or `J-SUPERSEDED` disallows `A0` | Fresh external decision, not the snapshot clock | `SB_REPLAY`; an old authentic authorization is not current permission |
| R21 wrong purpose/audience | Authorization is for another verifier or backup restore/Key reissue | Exact purpose/audience in `A0/B0` | `SB_BINDING`; no cross-purpose receipt reuse |
| R22 stale external checkpoint replay | Replay an authentic old baseline/checkpoint alongside an old matching snapshot | Independently fresh external lineage has advanced to another revision/watermark | `SB_STALE`; matching signed-old inputs do not establish latest state |
| R23 missing independent source | `B/J` are absent, unknown, candidate-derived or merely caller `trusted=true` | No trusted expected anchor may be synthesized from `O` | `SB_UNKNOWN_TRUST`; never treat equality or flags as verification |
| R24 authority-field ambiguity | Duplicate/unknown authority-bearing fields, ambiguous profile encoding or omitted binding field | Future closed approved record encoding/policy | `SB_BINDING`; no permissive interpretation or default scope |
| R25 object swap or late mutation | Bytes/path target change between digest, enumeration and final object check | Same exact authorized sealed object throughout | `SB_CAPTURE`; discard buffered results; do not retry as a new object |
| R26 floor changes before publication | Current external baseline, revocation or replay revision changes during validation | Final external state must still authorize this exact cut and one-use decision | `SB_STALE`; no stale receipt or partial manifest |
| R27 final cleanup / I/O failure | Enumeration looked valid, then close, rollback, resource-return or post-object check fails | Whole-result publication gate | `SB_IO`; nil result; cleanup cannot turn failure into success |
| R28 cancellation | Cancel before verification or after rows but before final publication | Whole-result publication gate | `SB_CANCELLED`; no receipt, partial manifest or migration work |
| R29 schema-profile self-hash | Candidate DDL, profile descriptor and reported profile hash are replaced together | Frozen external profile descriptor reference and PR92 semantic profile | `SB_SCHEMA`; candidate-defined schema is not the approved profile |
| R30 restored replay ledger | Old snapshot carries a matching old unused-authorization/revocation database | Replay and freshness state must be outside that backup's rollback domain | `SB_STALE` / `SB_REPLAY` as the violated external gate dictates; never resurrect approval |

R12's labels distinguish two explicit attack variants: authenticated evidence
for another object fails binding; forged/self-reported evidence fails trust.
R30 likewise requires independent replay and stale-floor variants. A future
test must name the chosen single guard, not allow arbitrary error success.
R27 is a **future failure specification**, not new actual disk/SELECT-path I/O
injection evidence. Existing PR92 driver-probe limits remain unchanged.

## 4. Common assertions and evidence discipline

Any later implementation proposal must independently check:

1. Literal expected anchors do not alias candidate observations or mutable
   caller slices/maps. Input mutation after verification cannot rewrite a
   receipt; receipt mutation cannot rewrite baseline or another result.
2. Every refusal has nil output, including errors after a complete private
   enumeration. No prefix rows, counts, digest, key reference or reusable
   authorization token is exposed or used; no accepted subset is cached.
3. Diagnostics use fixed labels only. Public test case IDs may identify the
   scenario, but runtime logs must not interpolate actual instance/provider/
   object identities, paths, digest, SQL, ciphertext, signatures, M1 references
   or underlying errors. No credential or production scan is requested here.
4. Same-context success is bounded to the precise synthetic tuple; no M2
   generation/encryption, Key issuance, dispatch, schema/product write or
   restore occurs on either success or failure. No old-root serving fallback.
5. All missing real mechanisms and the distinction between model assumptions
   and verified real evidence remain visible. A model's `approved` flag cannot
   be exported as a real source-authorization API.

The next proposed implementation inputs/output and acceptance gates are in
[contract section 6](rotation-upstreams-source-binding-contract.md#6-next-implementation-proposal-and-acceptance-gates).
These tables specify intended tests only. Document link/diff checks do not
execute these scenarios, authenticate a source, or establish production GO.

## 5. Provenance, sources and stop boundary

Independently authored public symbolic controls and attack cases from the
approved task and this project's frozen PR92/91/90 contracts; no archived CPA,
CLIProxyAPI or Sub2API implementation, tests, migrations, assets or documentation
are reused. No strict clean-room claim, third-party code extraction, new
dependency, license or NOTICE change. Primary public semantic references and
existing third-party license boundaries are recorded in
[contract section 8](rotation-upstreams-source-binding-contract.md#8-sources-provenance-and-license-boundary).

Exactly two new docs, one commit/push, independent stacked Draft PR. Inherited
files and old PR bodies stay frozen. Existing docs-only workflow filters are
recorded in [contract section 7](rotation-upstreams-source-binding-contract.md#7-this-docs-only-batch-validation-and-natural-ci);
no-run is not green or an executed/skipped test. First unexpected natural red
means source freeze/read-only diagnosis only, not a patch or rerun/dispatch.
Original E/F exact-new-head review is still required. No implementation, online
WAL, Key reissue, Ready/merge/tag/package/deploy or production approval follows.
