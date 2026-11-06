# Synthetic upstreams shadow candidate provenance

2026-10-20. Only `upstream_rotation_shadow_candidate.go` and its new test file
are covered by this record; it does not reattribute the rest of package service.

Independently authored from the approved
[contract](../../docs/rotation-upstreams-shadow-candidate-contract.md), this
project's frozen PR95 tree, and public Go/SQLite APIs cited in that contract.
The same-package placement permits ordinary calls to the existing unchanged
private `newSecrets` and three encryption/decryption families. No password,
KDF, AES-GCM, nonce generator or old reader implementation is copied, exported,
replaced or weakened. No archived/reference CPA, CLIProxyAPI or Sub2API code,
tests, migrations, assets or SDKs are imported. Prior research prevents a strict
clean-room claim.

Only the independently authored new test factory constructs the unexported
temporary-candidate capability and its reopen closure. Test schema literals
restate this project's current fourteen-column profile; data and roots are
explicit public synthetic fixtures. No captured DB/backup, live credential,
configuration, provider or service root is read. The kernel has no production
caller, constructor, route, CLI or config integration.

Default operations exercise the existing real AES-GCM functions and real
SQLite transactions. Private controlled-return boundaries distinguish simulated
encryption/random failure, nonce conflict and commit receipt uncertainty from
actual cryptographic authentication and SQLite execution. No global random
source is replaced. Actual entropy failure, real power loss, source trust,
production migration or root switching is not claimed.

No dependency/version/workflow change. Go and the pinned modernc.org/sqlite
v1.38.2 (BSD-3-Clause), and all existing transitive dependencies, retain their
own licenses. No new project license grant or publication authorization is
asserted. Synthetic-only / Draft / real migration and production NO-GO.
