# Sealed-fixture reader provenance

2026-10-20. Independently authored from the approved sealed-test-only contract,
public SQLite/Go APIs, inspected existing pinned modernc.org/sqlite v1.38.2
semantics, and this project's PR91 frozen source
`e8be3fb8ca8d453e66206dd02d868c52e8e0c0d3`.

The [contract](../../docs/sealed-upstreams-reader-contract.md) records sources,
exact scope, driver observations and evidence limits. New code does not port
reference/archived CPA/CLIProxyAPI/Sub2API code, tests, migrations or assets.
Prior research means no strict clean-room claim. Test schema literals restate
this project's current schema facts; test rows and assertions are independently
authored public synthetic inputs, not captured credentials/config/DB/backup.

Only tests construct the private fixture capability. There is no exported
reader/constructor, production integration, credential/provider adapter or
startup path. The prior pure mapper is imported unchanged. Raw data and driver
errors are never logs/diagnostics; public tests report fixed labels only.
Controlled seal observations do not prove host trust, M1 origin or production
snapshot integrity. Driver error probes are distinct from reader I/O injection.

No dependency added or upgraded; the existing pinned driver retains its
BSD-3-Clause license and transitive dependencies retain their licenses.
No third-party implementation is copied and no new project license grant or
release authorization is asserted. Draft / real migration and production NO-GO.
