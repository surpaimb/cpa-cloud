# Upstreams-only synthetic manifest provenance

2026-10-20. New independent package and public tests, authored from the approved
single-table contract and this project's frozen PR90 source
`dbe85f714628792f9a14254912c4a3deaf8c0017`.

The [source map](../../docs/upstreams-snapshot-manifest-sources.md) records exact
project blobs, fourteen columns, migration ancestry and provider/AAD facts.
The [contract](../../docs/upstreams-snapshot-manifest-contract.md) separates
static source facts, public synthetic-set consistency and unproved real DB/
cryptography/transaction/rollback behavior. PR90's existing model is unchanged.

No CLIProxyAPI/Sub2API/archived/reference-sibling implementation, fixture, SQL
migration, asset or documentation was copied, translated or ported. Earlier
research means no strict clean-room claim. This project's source was inspected
for facts, not used as a model implementation template.

`manifest.go` has no imports or I/O/crypto/secret-byte/activation code.
`manifest_test.go` imports only Go testing and this new package. Column/provider
oracles and positive/negative integer fixtures were independently handwritten;
no real database rows, configuration, credentials or backup captures are used.
The explicit coordinated-omission test is a limitation witness, not evidence of
actual snapshot completeness or a production recovery mechanism.

No new dependencies or copied third-party code/license obligations. Existing
Go, SQLite and dependency licenses are unchanged; no license/version/pin file
is edited and no project-wide MIT grant is claimed. No release authorization.
Draft / real migration and production NO-GO.
