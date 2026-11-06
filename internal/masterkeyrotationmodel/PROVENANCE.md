# Symbolic application-root rotation model provenance

2026-10-20. New, independently authored policy model and public synthetic tests.
Sole frozen parent: PR86 `6056270c612bdc3741dfee5a6c0c338cf9926b0c`.

Sources: the user's explicit M1-to-independent-M2/revoke-and-reissue policy,
this project's [consumer inventory](../../docs/master-key-rotation-consumers.md),
and the public semantic references in the
[contract](../../docs/master-key-rotation-model-contract.md#public-sources).
The model is not copied or ported from CLIProxyAPI, Sub2API, archived CPA,
reference siblings, their tests, migrations, assets or documentation. Prior
research means no strict clean-room claim. Existing CPA Cloud source was read
only to enumerate dependencies and source formats, not used as model templates.

`model.go` contains no imports, secret bytes, cryptographic implementation,
callbacks, clock, randomness, environment, file/database/network or provider
access. `model_test.go` imports only Go's `testing` and this model; it constructs
public integer symbols and separately enumerates all 53 consumer expectations.
Provenance/completeness/authentication flags are assumptions, not production
evidence. Model acceptance proves neither entropy nor real re-encryption,
quiescence, financial reconciliation, physical cleanup or durable anti-rollback.

No new dependencies, copied third-party source or license obligations are
introduced. Go and all existing dependencies retain their existing licenses;
this batch changes no dependency/version/license or production source file.
Only the five files listed in the contract may change. Draft / production NO-GO.
