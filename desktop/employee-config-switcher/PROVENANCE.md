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
| `crates/tests/config/claudeKeyFields.json` | `tests/config/claudeKeyFields.json` | `15afe94d5c828715b7606a7bdcc79023678ce51b` | `fc107056946a4290ef1fd12c9ce597329025906b5ffe7371d1a63b4b0ec76059` |

These modules are the parser/patch substrate only. `live/engine.rs` in this
workspace is newly authored CPA code containing the digest helper and the
create-only transaction experiment. It is not CC Switch's transaction engine;
no upstream transaction or recovery implementation was copied or translated.

## Files adapted with source edits

| Destination | Upstream path | Git blob OID | Upstream SHA-256 | Retained boundary |
| --- | --- | --- | --- | --- |
| `crates/employee-config-core/src/pi.rs` | `src-tauri/src/pi_config/mod.rs` | `947e1b1d43eb18ddc4d4b31d72a6c2fa161a32bc` | `f660fa5678d990dd3d14e0fcca546ec3302f029daa9b8f1fac33717aabe0e25d` | Pi `models.json.providers.<id>` node shape and the rule that `auth.json`/`settings.json` remain Pi-owned |
| `crates/employee-config-core/src/openclaw.rs` | `src-tauri/src/openclaw_config.rs` | `976fcd1e5214f0c7b2b69f9ab3c2a155ce8c60e8` | `a02b5bf9536ac1fc28e58634989180924dcf945c0f709fe824738ec45874d6d3` | OpenClaw `models.providers.<id>` node shape and `agents.defaults.model.primary` selection path |

The Pi file does not adopt CC Switch's writer, lock, revision, backup, settings,
or authentication code. CPA protocol selection, URL normalization, ownership
record, secret handling, and create-only transaction integration are newly
authored. The adapter registers a provider only; it does not select a default
provider or model.

## Newly authored CPA code

- `discovery.rs`: HTTPS/loopback validation and authenticated discovery client.
- `detection.rs`: read-only client location detection with documented home-directory overrides.
- `live/engine.rs`: CPA-authored create-only staging, journal, locking and recovery experiment.
- `opencode.rs`: CPA profile projection, JSONC planning, fail-closed ownership proof, and synthetic-root create-only integration.
- `plan.rs`: shared redacted file-plan and create-only transaction helpers.
- `pi.rs`: CPA profile mapping and ownership/transaction integration around the narrowly adapted Pi provider schema.
- `openclaw.rs`: CPA profile mapping, validation, ownership, and transaction integration around the narrowly adapted OpenClaw provider schema.
- Tauri command shell and the React interface.

The app icon is a newly generated CPA brand asset created for this client with
the OpenAI image generation tool on 2026-10-14, then resized and encoded with
ImageMagick. It does not reuse CC Switch artwork or identifiers:

| Asset | SHA-256 |
| --- | --- |
| `src-tauri/icons/icon.png` | `05f489d02e3eed53663169da87c300a1c6317018b1e0fb20ff1b7bc489087766` |
| `src-tauri/icons/icon.ico` | `ed5a72971c51f09d079010c094f043be9c0c089bc0eef180d3c57c94cd685788` |

The upstream `config.rs`, `live/engine.rs`, `mode/*`, database, provider store,
proxy, OAuth, usage, session, MCP, skills, cloud sync, updater, deep-link, tray,
and brand assets are excluded. In particular, the upstream transaction engine
is not adopted because its Windows ACL and recovery boundaries do not satisfy
the CPA write-safety contract.

## Public protocol sources

- OpenCode provider configuration: <https://opencode.ai/docs/providers/> (accessed 2026-10-14). It specifies `@ai-sdk/openai-compatible` for an OpenAI-compatible custom provider.
- Vercel AI SDK OpenAI-compatible provider: <https://ai-sdk.dev/providers/openai-compatible-providers> (accessed 2026-10-14). Its default/chat model uses `/v1/chat/completions`; the distinct completion model is opt-in.
- Pi release `v1.0.4` resolves to official commit `7c10bd4337495ee613f2224843ecdf349b80d1df`: <https://github.com/earendil-works/pi/releases/tag/v1.0.4> (verified 2026-10-14).
- Pi `models.json` compatible-endpoint contract at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/coding-agent/docs/models.md> (accessed 2026-10-14). It documents provider `baseUrl`, `api`, `apiKey`, and `models` entries, including `openai-responses` support.
- Pi configuration ownership at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/coding-agent/docs/configuration.md> (accessed 2026-10-14). It assigns compatible endpoints to `models.json`, credentials to `auth.json`, and user defaults to `settings.json` under the agent directory.
- Pi Responses transport at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/ai/src/api/openai-responses.ts> (accessed 2026-10-14). It constructs the OpenAI client from the model `baseUrl` and sends streaming Responses requests with the resolved API key.
- OpenClaw signed tag `v2026.9.8` resolves to commit `fc23bc864e4553c2d215e479eeec47b67a0bf943`: <https://github.com/openclaw/openclaw/releases/tag/v2026.9.8> (verified 2026-10-14).
- OpenClaw custom-provider configuration at that commit: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/docs/concepts/model-providers/custom-providers.md> (accessed 2026-10-14). It defines `models.providers`, `baseUrl`, `apiKey`, `api`, model entries, and `provider/model` selection.

These sources support the current OpenCode `openai-chat`, Pi
`openai-responses`, and OpenClaw `openai-responses` projections. Pi and
OpenClaw remain synthetic create-only planners rather than live-write support;
all client adapters remain blocked from real configuration writes until fixed
native fixtures and the replacement transaction contract are recorded and
tested.

## Rust dependency resolution

`Cargo.lock` was initially generated by GitHub Actions run `38533429363` with
Rust 1.90. The create-only transaction later promoted already-locked `libc`
and `windows-sys` packages to direct target-specific dependencies; the current
lock has SHA-256
`a42ac763bcbf878b146026fe1eba86bac8486b53fbb96d80857399ae30fb479d`.
`DEPENDENCY-LICENSES.md` records the SPDX-style license expression reported by
every registry package in that locked workspace graph. Packaging remains
disabled; any later distribution must also retain the applicable full license
texts and notices.
