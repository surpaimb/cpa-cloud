# Source provenance

Fixed upstream revision: `farion1231/cc-switch@c21106ff20e351d40f412cb51d1f24da177b113d`.

Acquisition: GitHub codeload archive for the exact commit. Downloaded archive SHA-256:
`91fddeb795220dd8d333fb8e7c66a53d18967cf7346ca98562b169b2aee9fa9b`.

License: MIT, copyright 2025 Jason Young. The upstream license is retained at
`third_party/cc-switch/LICENSE`.

## Files copied without source edits

| Destination | Upstream path | Git blob OID | SHA-256 |
| --- | --- | --- | --- |
| `crates/employee-config-core/src/jsonc_document.rs` | `src-tauri/src/jsonc_document.rs` | `23ff676a544019074dcaae9b949c1b66e1e91858` | `db13a3a07522d921b9cf8afe24489a2265af1074e610956d95e988d8f1be5280` |
| `crates/employee-config-core/src/live/floor.rs` | `src-tauri/src/live/floor.rs` | `04baabf9bc4e51cf11ea7bb400856459b890063c` | `873c1bfd90a1bc67f778703321f0850f613b627ca220dc232084053a355b13c3` |
| `crates/employee-config-core/src/live/residue.rs` | `src-tauri/src/live/residue.rs` | `3e9c4eb6c065daca08bc0e7ee713f11d22291415` | `e6cc032c2c7901b8ca2e2dafb56ccfcfd0bd9a47aa04800776c036aa3a852825` |
| `crates/employee-config-core/src/live/patch/mod.rs` | `src-tauri/src/live/patch/mod.rs` | `9c07109d6d1208798b08269bc77cab0d226e87ea` | `34d867a197a9ca50fa79159bdf3b8120d9b46cd3a7947a78757c759262a8ec2e` |
| `crates/employee-config-core/src/live/patch/dotenv.rs` | `src-tauri/src/live/patch/dotenv.rs` | `43f246cdb22ab3c9127e15c814ce8c5e2fed2345` | `f8d931536323fd1a4f23055732e1de8f5894378a138ccefc249f06c8b3f18cb4` |
| `crates/employee-config-core/src/live/patch/json.rs` | `src-tauri/src/live/patch/json.rs` | `46e0571b03aa781570b086ffbd64f87db4bc9ad3` | `b392b14a795eec9cca766a10e45f822c660bec52e3a0de09a79a88cdb6908ed9` |
| `crates/employee-config-core/src/live/patch/toml.rs` | `src-tauri/src/live/patch/toml.rs` | `630153d77838deb52e97c0f62bf9163cd9f4792a` | `1abf777fe8a9169a318793da2ded9b37c9f96d55ac0ad12ebdbb6b50eee139b8` |

These modules are the parser/patch substrate only. `live/engine.rs` in this
workspace is newly authored CPA code containing only the digest helper required
by the vendored `Guarded` patch type. It is not CC Switch's transaction engine.

## Newly authored CPA code

- `discovery.rs`: HTTPS/loopback validation and authenticated discovery client.
- `detection.rs`: deliberately small, read-only client location detection.
- `opencode.rs`: CPA profile projection, JSONC planning, and fail-closed ownership proof.
- Tauri command shell and the React interface.

The upstream `config.rs`, `live/engine.rs`, `mode/*`, database, provider store,
proxy, OAuth, usage, session, MCP, skills, cloud sync, updater, deep-link, tray,
and brand assets are excluded. In particular, the upstream transaction engine
is not adopted because its Windows ACL and recovery boundaries do not satisfy
the CPA write-safety contract.

## Public protocol sources

- OpenCode provider configuration: <https://opencode.ai/docs/providers/> (accessed 2026-10-14). It specifies `@ai-sdk/openai-compatible` for an OpenAI-compatible custom provider.
- Vercel AI SDK OpenAI-compatible provider: <https://ai-sdk.dev/providers/openai-compatible-providers> (accessed 2026-10-14). Its default/chat model uses `/v1/chat/completions`; the distinct completion model is opt-in.

These sources support the current OpenCode `openai-chat` projection. Other
client adapters remain blocked until their own public protocol contracts and
native fixtures are recorded and tested.
