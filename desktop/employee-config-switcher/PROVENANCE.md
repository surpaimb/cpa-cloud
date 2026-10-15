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

- `claude_code.rs`: independently authored Claude Code gateway mapping, strict create-only path boundary, ownership record, and synthetic transaction integration.
- `codex.rs`: independently authored Codex profile mapping, strict create-only path boundary, ownership record, and synthetic transaction integration.
- `discovery.rs`: HTTPS/loopback validation and authenticated discovery client.
- `detection.rs`: read-only client location detection with documented home-directory overrides.
- `gemini.rs`: independently authored Gemini CLI gateway mapping, strict three-file create-only path boundary, non-secret ownership record, and synthetic transaction integration.
- `grok_build.rs`: independently authored Grok Build Responses-model mapping, strict create-only path boundary, non-secret ownership record, and synthetic transaction integration.
- `hermes.rs`: independently authored Hermes Agent named custom-provider mapping, strict create-only path boundary, non-secret ownership record, and synthetic transaction integration.
- `live/engine.rs`: CPA-authored create-only staging, bounded journal parsing, locking, private recovery-directory validation, pre-begin orphan cleanup, and recovery experiment.
- `live/backup.rs`: CPA-authored Linux synthetic-root encrypted rollback-material foundation using the [`ring` 0.17.14 AEAD API](https://docs.rs/ring/0.17.14/ring/aead/index.html); format version 2 binds private Linux owner/group/mode metadata, public receipts omit content digests, backup artifacts are revalidated as owner-private single-link objects, and the module still does not publish or replace client files. Windows and macOS remain explicitly unsupported.
- `live/replacement.rs`: CPA-authored pure in-memory sequencing gate for the reviewed existing-file replacement state machine; it performs no filesystem I/O, defines no durable journal encoding, and cannot publish or restore files.
- `minimax_code.rs`: independently authored MiniMax Code BYOK provider mapping, strict create-only path boundary, native-lock refusal, non-secret ownership record, and synthetic transaction integration.
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

- Windows security-descriptor ownership and access-token APIs: Microsoft Learn documentation for [`SetNamedSecurityInfoW`](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-setnamedsecurityinfow), [`GetSecurityDescriptorOwner`](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-getsecuritydescriptorowner), [`OpenProcessToken`](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken), and [`GetTokenInformation`](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-gettokeninformation) (accessed 2026-10-15). Creation sets the owner to the current process-token user SID; recovery compares the file owner with that SID in addition to verifying the protected owner-rights DACL.
- macOS extended ACL enumeration: Apple `acl_get_fd_np(3)` and `acl_get_entry(3)` manual pages (accessed 2026-10-15). The private-directory boundary queries the already-open object, rejects any extended ACL entry rather than relying only on BSD owner/mode metadata, and separately confirms that a lock path still names the locked inode.
- Windows recovery-key planning source: Microsoft [`CryptProtectData`](https://learn.microsoft.com/en-us/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata) (accessed 2026-10-15). The replacement contract permits only current-user protection and explicitly forbids `CRYPTPROTECT_LOCAL_MACHINE`; no DPAPI implementation is present yet.
- macOS recovery-key planning source: Apple [Using the keychain to manage user secrets](https://developer.apple.com/documentation/security/using-the-keychain-to-manage-user-secrets) (accessed 2026-10-15). The replacement contract requires a non-synchronizing generic-password item and treats a locked or unavailable Keychain as fail-closed; no Keychain implementation is present yet.
- Linux desktop recovery-key planning source: freedesktop.org Secret Service [collections and items](https://specifications.freedesktop.org/secret-service/latest/ch03.html) and [locking and unlocking](https://specifications.freedesktop.org/secret-service/latest/unlocking.html) (accessed 2026-10-15). The contract requires stable lookup attributes and an explicit locked outcome; headless Linux has no accepted provider and no Secret Service implementation is present yet.

- Claude Code settings files and precedence: <https://code.claude.com/docs/en/settings> (accessed 2026-10-14). It defines the user-level `~/.claude/settings.json` file, strict JSON syntax, the `env` block, the `model` setting, and the separate Claude-owned `~/.claude.json` login/project-state file.
- Claude Code LLM gateway connection contract: <https://code.claude.com/docs/en/llm-gateway-connect> (accessed 2026-10-14). It documents `ANTHROPIC_BASE_URL`, bearer-token `ANTHROPIC_AUTH_TOKEN`, persistence in the user settings `env` block, precedence over a saved Claude login, and Anthropic Messages requests at `/v1/messages`.
- Fixed local Claude Code parser fixture: native Claude Code `2.1.266` (`eb01d6090964`) for `win32-x64`, executable SHA-256 `d2c5f7b3b6a12819097ceb6efbce2a390157166003fcaee32dbde0e6d7b45ef7`, validated 2026-10-14 with an isolated `CLAUDE_CONFIG_DIR`, a closed numeric-loopback synthetic gateway, and nonessential traffic disabled. `claude doctor` loaded the custom base URL without contacting Anthropic managed settings or organization policy; the reported warnings concerned only the local installation method and an unrelated missing launcher. The committed synthetic fixture is `crates/employee-config-core/tests/fixtures/claude-code-2.1.266/settings.json`. This evidence applies only to that exact binary and does not authorize real writes or claim cross-version compatibility.
- Gemini CLI release `v0.62.0` resolves to official commit `b460678f3db508407554afd604cc9d6635becb2a`: <https://github.com/google-gemini/gemini-cli/releases/tag/v0.62.0> (verified 2026-10-14).
- Gemini CLI configuration contract at that tag: <https://github.com/google-gemini/gemini-cli/blob/v0.62.0/docs/reference/configuration.md> (accessed 2026-10-14). It documents the user-level `~/.gemini/settings.json` file, project and user `.env` loading, `GEMINI_API_KEY`, `GEMINI_MODEL`, `GOOGLE_GEMINI_BASE_URL`, `GOOGLE_GENAI_API_VERSION`, telemetry environment controls, and `security.auth.selectedType`.
- Gemini CLI startup authentication validation at that tag: <https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/cli/src/config/auth.ts> (accessed 2026-10-14). `validateAuthMethod` accepts `gemini-api-key` when `GEMINI_API_KEY` is loaded, but does not accept `gateway` as a persisted `security.auth.selectedType`. The independently authored regression fixture at `crates/employee-config-core/tests/fixtures/gemini-cli-0.62.0/auth-validation.json` records that exact fixed-version boundary.
- Gemini CLI content-generator transport at that tag: <https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/core/contentGenerator.ts> (accessed 2026-10-14). It passes `GOOGLE_GEMINI_BASE_URL` to the Google GenAI client for `gemini-api-key` authentication and supplies `GEMINI_API_KEY` to the SDK, whose default API-key mechanism is `x-goog-api-key`, matching CPA Cloud's documented strict Gemini employee-key boundary. Although the same file defines an internal `gateway` auth type and environment auto-detection, the fixed CLI startup validator above rejects that value when persisted. No native `gemini` binary was available in the local environment, so this adapter has only fixed-source and synthetic Rust evidence; the missing fixed native fixture remains a blocker for real writes and no cross-version compatibility is claimed.
- Grok Build official source snapshot `xai-org/grok-build@2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8` (verified 2026-10-14): <https://github.com/xai-org/grok-build/tree/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8>. The repository is Apache-2.0 licensed; no Grok source code is copied into this workspace.
- Grok Build custom-model contract at that commit: <https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-pager/docs/user-guide/11-custom-models.md> (accessed 2026-10-14). It defines `~/.grok/config.toml`, `[models].default`, `allowed_models`, `[model.<name>]`, the `responses` backend, `base_url`, inline `api_key`, bearer authentication, and the `reasoning_summary = "none"` compatibility setting used by the synthetic projection. The independently authored fixed-source fixture is `crates/employee-config-core/tests/fixtures/grok-build-2bdd1d6/custom-models-contract.json`.
- Grok Build shell guide at that commit: <https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/README.md> (accessed 2026-10-14). It records the separate Grok-owned `~/.grok/auth.json` browser-login state. No local `grok` binary was available, so fixed native parsing/transport evidence remains a blocker for real writes and no cross-version compatibility is claimed.
- Hermes Agent official source snapshot `NousResearch/hermes-agent@0e427d788d253630d38a3a51cd76849e73cd6925` (verified 2026-10-14): <https://github.com/NousResearch/hermes-agent/tree/0e427d788d253630d38a3a51cd76849e73cd6925>. The repository is MIT licensed; no Hermes implementation code is copied into this workspace.
- Hermes model configuration contract at that commit: <https://github.com/NousResearch/hermes-agent/blob/0e427d788d253630d38a3a51cd76849e73cd6925/website/docs/user-guide/configuring-models.md> (accessed 2026-10-14). It defines the POSIX-default `~/.hermes/config.yaml` example, the `model` mapping, named `providers`, model candidates, `discover_models`, and inline `api_key` support.
- Hermes provider contract at that commit: <https://github.com/NousResearch/hermes-agent/blob/0e427d788d253630d38a3a51cd76849e73cd6925/website/docs/integrations/providers.md> (accessed 2026-10-14). It documents `custom:<name>` selection, OpenAI-compatible `/v1/chat/completions`, `api`, `transport = chat_completions`, `default_model`, and the current provider-map field names. The independently authored fixed-source fixture is `crates/employee-config-core/tests/fixtures/hermes-agent-0e427d7/config-contract.json`.
- Hermes configuration-directory contract at that commit: <https://github.com/NousResearch/hermes-agent/blob/0e427d788d253630d38a3a51cd76849e73cd6925/website/docs/reference/environment-variables.md> and <https://github.com/NousResearch/hermes-agent/blob/0e427d788d253630d38a3a51cd76849e73cd6925/website/docs/user-guide/windows-native.md> (accessed 2026-10-14). It defines `HERMES_HOME` expansion and precedence, `HERMES_DATA_DIR_SUFFIX` for default homes, the POSIX `~/.hermes` default, and the native-Windows `%LOCALAPPDATA%\\hermes` default. The general configuration guide at <https://github.com/NousResearch/hermes-agent/blob/0e427d788d253630d38a3a51cd76849e73cd6925/website/docs/user-guide/configuration.md> records the separate `.env`, `auth.json`, and other Hermes-owned state beneath the resolved Hermes home. The current detector is best-effort and does not yet reproduce all native override-expansion semantics. No local `hermes` binary was available, so exact home resolution, fixed native parsing/transport evidence, and cross-version compatibility remain blockers for real writes.
- MiniMax Code signed release `v0.5.6` resolves to official commit `836580efb27632e288b5e0ae26ec120a61e907f5`: <https://github.com/MiniMax-AI/minimax-code/releases/tag/v0.5.6> (verified 2026-10-14). First-party code is MIT licensed; no MiniMax Code implementation code is copied into this workspace.
- MiniMax Code BYOK contract at that commit: <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/README.md> and <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/docs/examples.md> (accessed 2026-10-14). They document the active data-directory `config.yaml`, `MINIMAX_DATA_DIR`/`MAVIS_DATA_DIR` precedence, `custom_provider`, `openai-responses`, plaintext `options.apiKey`, `options.baseURL`, Bearer authentication, model registration, and default-model selection after a successful `--use` test.
- MiniMax Code data-directory resolution at that commit: <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/config/src/config.ts>, <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/config/src/data-dir.ts>, and <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/tui/src/runtime/data-dir.ts> (accessed 2026-10-14). They define `--data-dir`/`--profile`, public environment overrides, profile-scoped `.minimax-<profile>` roots, and migration-aware `.mavis[-profile]` fallbacks. The preview detector implements only the public environment/default subset; unsupported resolution modes must be rejected before live writes.
- MiniMax Code fixed-source provider construction and writer at that commit: <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/local-runtime-v2/src/service/model-system/management/service-context.ts>, <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/local-runtime-v2/src/service/model-system/resolution/model-key.ts>, and <https://github.com/MiniMax-AI/minimax-code/blob/836580efb27632e288b5e0ae26ec120a61e907f5/packages/config/src/local-model-provider-write.ts> (accessed 2026-10-14). They define `kind = custom`, `enabled`, `authMode = api-key`, `custom_provider:<name>/<model-id>` references, and a `proper-lockfile` lock held across read/modify/atomic publication with a 10-second stale threshold. The independently authored fixture is `crates/employee-config-core/tests/fixtures/minimax-code-0.5.6/provider-contract.json`. The synthetic planner now fails closed for every visible lock-path entry and inspection error, but it does not acquire the compatible native lock. No local `mcode` binary was available, so fixed native parsing/transport and compatible native-lock acquisition remain blockers for real writes and no cross-version compatibility is claimed.
- OpenCode provider configuration: <https://opencode.ai/docs/providers/> (accessed 2026-10-14). It specifies `@ai-sdk/openai-compatible` for an OpenAI-compatible custom provider.
- Vercel AI SDK OpenAI-compatible provider: <https://ai-sdk.dev/providers/openai-compatible-providers> (accessed 2026-10-14). Its default/chat model uses `/v1/chat/completions`; the distinct completion model is opt-in.
- Pi release `v1.0.4` resolves to official commit `7c10bd4337495ee613f2224843ecdf349b80d1df`: <https://github.com/earendil-works/pi/releases/tag/v1.0.4> (verified 2026-10-14).
- Pi `models.json` compatible-endpoint contract at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/coding-agent/docs/models.md> (accessed 2026-10-14). It documents provider `baseUrl`, `api`, `apiKey`, and `models` entries, including `openai-responses` support.
- Pi configuration ownership at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/coding-agent/docs/configuration.md> (accessed 2026-10-14). It assigns compatible endpoints to `models.json`, credentials to `auth.json`, and user defaults to `settings.json` under the agent directory.
- Pi Responses transport at that commit: <https://github.com/earendil-works/pi/blob/7c10bd4337495ee613f2224843ecdf349b80d1df/packages/ai/src/api/openai-responses.ts> (accessed 2026-10-14). It constructs the OpenAI client from the model `baseUrl` and sends streaming Responses requests with the resolved API key.
- OpenClaw signed tag `v2026.9.8` resolves to commit `fc23bc864e4553c2d215e479eeec47b67a0bf943`: <https://github.com/openclaw/openclaw/releases/tag/v2026.9.8> (verified 2026-10-14).
- OpenClaw custom-provider configuration at that commit: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/docs/concepts/model-providers/custom-providers.md> (accessed 2026-10-14). It defines `models.providers`, `baseUrl`, `apiKey`, `api`, model entries, and `provider/model` selection.
- OpenClaw model API vocabulary at that commit: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/packages/llm-core/src/model-data.ts> (accessed 2026-10-14). Its immutable `MODEL_DATA_APIS` list includes `openai-responses`.
- OpenClaw Zod model/provider schema at that commit: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/src/config/zod-schema.core.ts> (accessed 2026-10-14). `ModelApiSchema` is derived from that fixed API vocabulary and is used for both provider-level and model-level `api`; the same strict provider schema accepts `baseUrl`, `apiKey`, and `models`.
- OpenClaw custom-endpoint onboarding contract at that commit: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/docs/cli/onboard.md> (accessed 2026-10-14). It requires `openai-responses` for compatible endpoints that expose `/v1/responses` rather than `/v1/chat/completions`.
- OpenClaw fixed-version custom-provider E2E fixture: <https://github.com/openclaw/openclaw/blob/fc23bc864e4553c2d215e479eeec47b67a0bf943/src/commands/onboard-guided.custom-provider.e2e.test.ts> (accessed 2026-10-14). It maps the `openai-responses` compatibility mode to `/v1/responses` and exercises a custom base URL ending in `/v1`.
- OpenAI Codex configuration reference: <https://developers.openai.com/codex/config-reference> (accessed 2026-10-14). It defines the user-level `config.toml`, `model_provider`, custom `model_providers`, `base_url`, bearer credential, `wire_api = "responses"`, retry, WebSocket, and `requires_openai_auth` fields used by the synthetic Codex projection.
- OpenAI Codex configuration schema: <https://developers.openai.com/codex/config-schema.json> (accessed 2026-10-14), UTF-8 size 236751 bytes and SHA-256 `7933a70506e753a6e98c16800d2a76770b2af2b7295491f8f0b8e713e952027e`. Its `ModelProviderInfo` schema marks direct `experimental_bearer_token` use as discouraged in favor of `env_key`, while documenting that it may be necessary for programmatic use.
- OpenAI Codex app-server Responses provider example: <https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server> (accessed 2026-10-14). It demonstrates `model_provider`, `base_url`, bearer-token environment input, `wire_api = "responses"`, `requires_openai_auth = false`, `supports_websockets = false`, and requests to `/v1/responses`.
- Fixed local Codex parser fixture: `codex-cli 0.162.0-alpha.17.2` for `windows-x86_64`, executable SHA-256 `d13914ced6c7af174d8d638db938284231227312104b06a25e9608c632a7daec`, validated 2026-10-14 with an isolated temporary `CODEX_HOME` and a loopback-only synthetic provider. `codex doctor --strict-config` reported the config loaded, OpenAI auth not required, and Responses WebSocket disabled; the expected closed-loopback reachability check failed without contacting a production model endpoint. This local fixture is evidence for the exact tested binary only and is not a redistributable artifact or cross-version compatibility claim.

These sources support the current OpenCode and Hermes `openai-chat`, Claude
Code `anthropic-messages`, Gemini CLI `gemini-generate-content`, and Codex,
Grok Build, MiniMax Code, Pi and OpenClaw `openai-responses` projections. Claude
Code, Gemini CLI, Codex, Grok Build, Hermes, MiniMax Code, Pi and OpenClaw
remain synthetic create-only planners rather than live-write support; all client adapters remain blocked from real
configuration writes until fixed native fixtures and the replacement transaction
contract are recorded and tested.

## Rust dependency resolution

`Cargo.lock` was initially generated by GitHub Actions run `38533429363` with
Rust 1.90. The transaction experiments later promoted already-locked `libc`,
`windows-sys`, and `ring` packages to direct dependencies; the current lock has
SHA-256
`b9cc39b69071f266fe79a16235b22762c69417f119c1c92c5c43ebc9550ebb1f`.
`DEPENDENCY-LICENSES.md` records the SPDX-style license expression reported by
every registry package in that locked workspace graph. Packaging remains
disabled; any later distribution must also retain the applicable full license
texts and notices.
