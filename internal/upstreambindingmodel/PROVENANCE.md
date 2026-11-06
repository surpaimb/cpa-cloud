# Synthetic upstreams binding-model provenance

2026-10-20. Independently authored from the separately approved pure-model batch
and this project's PR93 at `80635d63fa227ff123de9be6053f716f0009ff5e`:

- [External source-binding contract](../../docs/rotation-upstreams-source-binding-contract.md).
- [Public case specification](../../docs/rotation-upstreams-source-binding-cases.md).
- [This implementation's bounded contract](../../docs/rotation-upstreams-binding-model-contract.md).

All input symbols, role types, finite enums, policy guards, literal test anchors
and expected outcomes are independently authored public synthetic data. The
expected baseline/checkpoints are not constructed from candidate observations.
No old CPA/CLIProxyAPI/Sub2API implementation, tests, migrations, assets or
documentation are copied, translated or ported. Earlier research means there
is no strict clean-room claim. Existing PR92/91/90 source is not modified,
imported or used as an adapter/production caller.

The model has no imports, exported identifiers, mutable global state, file/
network/clock/OS/DB/crypto access, callback or credential input. Standard Go
test/compiler tooling is separate from the model's pure execution. Tests import
only `testing` and `reflect`; fixture mutator functions are test harness code,
not callbacks accepted by the model.

Public semantic references: [Go language specification](https://go.dev/ref/spec),
[Go testing](https://pkg.go.dev/testing), and the role/freshness references
already recorded in PR93. No source code or document passage is extracted.
The [Go license](https://go.dev/LICENSE) applies to Go's own tooling/library;
no dependency, version, project-license grant or NOTICE is added/changed.
Existing third-party dependencies retain their licenses; none is imported by
this model. This is neither a cryptographic implementation nor a compliance
assessment.

Unexported role types and the `syntheticOnly` result do not authenticate external
origins. The tests explicitly witness coherent replacement of all assumptions
and repeated unused checkpoints succeeding. No real source authentication,
atomic authorization consumption, M1 provenance, M2 migration, Key reissue or
production approval is asserted. Draft / production NO-GO remains.
