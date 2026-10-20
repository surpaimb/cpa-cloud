# Frozen upstreams source / column / AAD map

2026-10-20. Sole source commit: PR90
`dbe85f714628792f9a14254912c4a3deaf8c0017`. All locations below are at that
commit, not a moving branch. No real SQLite database/configuration was opened.
Static source facts and the public fixture mapping are distinct from real
schema/cryptographic evidence. Scope is defined by the
[contract](upstreams-snapshot-manifest-contract.md).

## Source identities and migration paths

| Project source | Git blob at frozen head | Relevant source facts |
| --- | --- | --- |
| `internal/service/store.go` | `f2854e60ad8460d5cf20d858f2b2c957ed4a2ba2` | current upstreams DDL 106-126; archive add-column 238-239 and validation 306-333,377; pre-membership rebuild/copy 604-645 |
| `internal/service/security.go` | `d3d4356ab4d23498c2091fe1a2480a46fc802ac5` | M-derived AEAD purpose 66-74; three live encoding/AAD functions 118-187 |
| `internal/service/upstreams.go` | `2abfc283203c1dbb18219b2c5a05d82a6e2af553` | API/Gemini creation/version choice 203-222; update 310-326 |
| `internal/service/codex_membership.go` | `0481482d93e802aff36d53e8f3211e554ff71058` | Codex auth parse/encrypt/create 132-159; replacement/version 244 |
| `internal/service/account_lifecycle.go` | `c14ccdf3e61cf3734cc76013e48dad0699397e01` | archive writes empty non-NULL BLOB, version 0, disabled, archive timestamp; Codex reauth and verified NULL 287-294 |

Current DDL has fourteen columns. The rebuild copies twelve then the lifecycle
migration adds archive columns; name-set matching must not assume identical
physical ordinals. This batch accepts only the resulting current profile,
**not** the twelve-column intermediate/old schema or arbitrary historical
layouts. It does not invoke those migrations. References to migration code
explain the current shape; they do not authorize conversion of historical rows.

## All fourteen columns

Source locators below are `internal/service/store.go` unless stated otherwise.
`S` denotes an opaque nonzero public fixture symbol, not actual TEXT/BLOB bytes.
Only the fixed enum values and bounded integers are interpreted by the mapper.

| Column | Frozen DDL / identity / current source format | Symbolic rule and planned M2 action |
| --- | --- | --- |
| id | 107 TEXT PRIMARY KEY; object identity for credential AAD at security.go 123/137/147/161/171/185 | nonzero TextSymbol, unique across index/rows; preserve same object ID, never create upstream |
| name | 108 TEXT NOT NULL | TextSymbol S; preserve source fact; no real string-length/parser claim |
| provider_kind | 109 TEXT NOT NULL, four-provider CHECK and state relationship 122-125 | exact fixed enum; select source format/consumer label, preserve |
| endpoint | 110 TEXT NOT NULL; creation validates before storing, upstreams.go 188-198 | TextSymbol S; preserve, no URL/network validation or use |
| enabled | 111 INTEGER NOT NULL | integer 0/1 supported subset; preserve, never enable disabled/archived rows |
| credential_ciphertext | 112 BLOB NOT NULL; security.go 118-187 nonce-prefix GCM; lifecycle archive 292-293 empty BLOB | live BlobSymbol S: plan M2 re-encryption; archived EmptyBlob: preserve; NULL invalid; no byte parsing/AEAD evidence |
| key_version | 113 INTEGER NOT NULL; upstreams.go 203-210 and codex_membership.go 159 choose 1/2; lifecycle 292 chooses 0 | encoding 1 API, 2 Gemini/Codex, 0 destroyed; preserve encoding, **not M epoch** |
| revision | 114 INTEGER NOT NULL; update upstreams.go 325 and lifecycle 292 | integer 1..2^53-1 conservative fixture subset; preserve fact, no CAS/transaction execution |
| archived | 115 INTEGER NOT NULL DEFAULT 0 CHECK(0/1); additive 238 and lifecycle validator 323-333 | integer 0/1; preserve; cross-check timestamp, enabled, ciphertext/version shape |
| archived_at | 116 nullable TEXT; additive 239, validation 377 | NULL for live; TimestampSymbol S for archived; preserve, no wall-clock parsing |
| created_at | 117 TEXT NOT NULL | TimestampSymbol S; preserve creation fact |
| credential_state | 118 nullable TEXT with imported_unverified/verified/reauth_required; 122-125 provider relationship | API/Gemini NULL; Codex exact enum; archived Codex reauth_required from lifecycle 289-291; preserve |
| verified_at | 119 nullable TEXT; API state relationship 122-123; archive clears at lifecycle 292 | TimestampSymbol or NULL for live Codex; otherwise NULL; preserve |
| operation_id | 120 nullable TEXT UNIQUE; Codex required/API NULL by 122-125 | nonzero TextSymbol for Codex, globally unique when present; API/Gemini NULL; preserve stable fact, no replay/refresh dispatch |

The mapper's `ColumnSpec.DeclaredNotNull` records the explicit DDL, so `id` is
false despite PRIMARY KEY. SQLite's ordinary TEXT PRIMARY KEY is not implicitly
NOT NULL; the **supported fixture contract** nevertheless requires nonzero IDs.
SQLite type affinity is not equivalent to these stricter tagged fixture kinds.
The mapper is not a generic SQLite constraint evaluator or schema introspector.

## Four providers, three live AAD encodings

All live formats use the M-derived domain
`cpacloud/upstream-credentials/v1` (`security.go:66`). Source serialization is
nonce followed by GCM sealed bytes; this package carries only a BlobSymbol and
an AAD descriptor. It never reconstructs actual nonce/ciphertext/AAD bytes.

| Provider / PR90 consumer label | Live key_version | Exact AAD prefix, followed by upstream object ID | Source |
| --- | --- | --- | --- |
| openai-compatible / upstream-api | 1 | `cpacloud/upstream-api-key/v1` + NUL + ID | security.go 118-139; upstreams.go 203-210 |
| anthropic-api-key / upstream-api | 1 | same shared API prefix + NUL + ID | same source and provider creation check upstreams.go 182 |
| gemini-api-key / gemini-api | 2 | `cpacloud/upstream-credential/v2` + NUL + `gemini-api-key` + NUL + `api-key` + NUL + ID | security.go 142-163; upstreams.go 206-208 |
| codex-membership / codex-auth | 2 | `cpacloud/upstream-credential/v2` + NUL + `codex-membership` + NUL + `auth-json` + NUL + ID | security.go 166-187; codex_membership.go 140,159 |

Format labels in the mapper classify these source forms, not new storage
versions. No Gemini/Codex v1 fallback, unknown encoding or mismatched AAD is
accepted. An archived row of any known provider instead has EmptyBlob/version 0,
no AAD, disabled state and a timestamp: it has no live encryption operation.
Encryption metadata can be checked symbolically without authenticating bytes;
real AEAD/body authenticity is explicitly not inferred from these descriptors.

No source column authenticates a global M1/M2 epoch. Nonzero/consistent fixture
generation tags are not recovered or attested from key_version, revision,
provider state, AAD or a table profile label. A future actual snapshot adapter
and trustworthy generation binding require a separate contract.

## Public semantic references and licenses

Consulted 2026-10-20; no implementation copied:

- [SQLite CREATE TABLE](https://sqlite.org/lang_createtable.html): column,
  PRIMARY KEY/NULL and UNIQUE semantics; not a claim that fixture acceptance
  executes SQL constraints.
- [SQLite datatypes](https://sqlite.org/datatype3.html): dynamic typing and
  affinity differ from strict symbolic tags.
- [Go testing](https://pkg.go.dev/testing): deterministic public test harness.

No new dependency or copied third-party implementation; Go/SQLite and existing
dependencies retain their licenses. No new project license grant is asserted.
Independent authorship and limits: [PROVENANCE](../internal/upstreammanifest/PROVENANCE.md).
