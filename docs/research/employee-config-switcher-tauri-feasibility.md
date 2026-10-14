# Employee configuration client: Tauri feasibility and extraction boundary

Status: design gate; no release claim, 2026-10-14.

## Decision

The formal employee client will be a new, slim Tauri 2 desktop application. The CPA Cloud Go service keeps the read-only `GET /v1/client-configuration` endpoint. The experimental Go loopback/browser helper is validation material only and must not be packaged, published, or described as the ten-tool client.

This decision follows the requested native CC Switch-like experience and the preference for direct reuse. It also avoids continuing a second independent implementation of ten changing client configuration formats.

## Fixed upstream and source acquisition

The only permitted CC Switch source baseline is MIT commit `c21106ff20e351d40f412cb51d1f24da177b113d` from `farion1231/cc-switch`. GitHub's authenticated contents API successfully returned files at that exact ref, including blob identities, so extraction is feasible without relying on the incomplete local partial clone. Every copied file must retain the upstream copyright/license notice and be listed with its original path, blob SHA, destination path, and CPA modifications.

The extraction candidate set is:

- Common file write and device-state code: `src-tauri/src/config.rs`, `live/engine.rs`, `mode/operation.rs`, and `mode/state.rs`.
- Client adapters: `claude_desktop_config.rs`, `codex_config.rs`, `gemini_config.rs`, `grok_config.rs`, `opencode_config.rs`, `openclaw_config.rs`, `hermes_config.rs`, `pi_config/mod.rs`, and `mcode_config.rs`.
- Narrow schema/path dependencies: `app_config.rs`, `provider.rs`, `settings.rs`, `jsonc_document.rs`, the required `live/patch/**` modules, and four direct writers under `src-tauri/src/services/provider/` (`claude_direct.rs`, `codex_direct.rs`, `gemini_direct.rs`, `grok_direct.rs`).

These files are not an independent crate. Imports show coupling to CC Switch `Database`, `Provider`, live patching, device-state, settings, and mode recovery. Extraction therefore means preserving proven adapter and transaction code while replacing database-backed provider input with one in-memory CPA profile; it does not mean copying the whole application.

## Deliberately excluded upstream surfaces

The client must not include CC Switch proxy/network server code, provider/account databases, OAuth/login flows, usage/statistics, sessions, MCP, Skills, cloud sync, updater, deep links, tray behavior, clipboard helpers, old product identifiers, update keys, icons, or brand assets. The frontend is independently authored and has only two editable values: CPA Cloud URL and employee Key.

The Tauri command surface is limited to:

```text
detect() -> ten read-only tool statuses
discover(root_url, employee_key) -> authorized protocol/default-model catalog
apply(root_url, employee_key) -> per-tool configured|blocked|failed|not_installed results
```

The Key remains in memory until a target client requires it. It is never stored in a CC Switch-style provider row, application database, URL, logs, crash metadata, clipboard, web storage, or updater state.

## Security gate before any writable preview

Direct reuse does not waive CPA's stronger transaction requirements. The extracted client remains blocked until all of the following are demonstrated with fault injection:

1. Windows backup, staging, journal, and new target files receive a protected private DACL; replacing an existing file preserves or deliberately restores its security descriptor.
2. Persistent backups do not retain plaintext previous employee Keys. Transaction pre-images are OS-protected or are short-lived rollback material deleted only after a durable success marker.
3. A file becomes rollback-eligible immediately after publication, before post-write verification.
4. Rollback compares the published digest and file identity and never overwrites/deletes a concurrent external edit.
5. The complete path chain, including home, backup root, staging root, and target parents, is verified against symlink/junction/reparse substitution using native handles/file identities where required.
6. Multi-file tools use a durable planned/published/verified journal with startup recovery. Backups are flushed and published with a manifest-last completion marker.
7. TOML, YAML, JSON5/JSONC, and dotenv are parsed at syntax level; duplicate keys, unsafe aliases, ambiguous ownership, and unsupported versions fail closed.
8. An existing `cpa-cloud` provider is changed only when an ownership marker/digest proves it belongs to this client.

Atomicity is per tool. A failed tool must not leave a half-configured file set. Tools completed earlier may remain configured, and the UI must report every tool separately.

## Protocol corrections that must survive extraction

- Claude Code uses `ANTHROPIC_AUTH_TOKEN` so it sends one Bearer credential; CPA rejects `X-API-Key` employee authentication.
- Grok Build uses `[models] default` plus `[model.<id>]` and the required `model`, `name`, `api_backend`, and `context_window` fields.
- Hermes uses the singular selected `model` and its native map-shaped `models` declaration.
- Codex custom-provider authentication must be proven end to end. Merely declaring `env_key = "OPENAI_API_KEY"` while writing only `auth.json` is insufficient.
- Protocols are fixed by client contract, never inferred from model names: Claude clients use `anthropic-messages`; Codex, Grok Build, OpenCode, OpenClaw, Pi, and MiniMax Code use `openai-responses`; Gemini CLI uses `gemini-generate-content`; Hermes uses `openai-chat`.

## Dependency and build plan

The minimal Rust crate should begin with Tauri 2, `serde`, `serde_json`, `toml`, `toml_edit`, `serde_yaml`, `json-five`, `indexmap`, `sha2`, `url`, and platform filesystem/locking APIs actually required by the extracted modules. Dependencies from the upstream proxy/database/updater stack are excluded unless a copied module proves they are required and the security review accepts them.

Current Windows host evidence:

- Node and npm are installed; a task-local pnpm is available.
- WebView2 Runtime is installed.
- `rustup`, `rustc`, and `cargo` are absent.
- Visual Studio C++ Build Tools (`cl`, `link`, `msbuild`, and `vswhere`) are absent.

Rust can be installed into task-local `RUSTUP_HOME`/`CARGO_HOME` without changing global configuration, but this host still cannot link a Windows Tauri binary without the MSVC build tools. The first trustworthy compile matrix must therefore run on native Windows, macOS, and Linux CI runners with pinned Rust/Node/pnpm versions and Tauri prerequisites. No installer, signing, notarization, package, tag, or deployment belongs to this design gate.

## Implementation sequence

1. Commit and review the Go discovery endpoint independently; do not include the browser helper.
2. Create a separate Tauri client subtree and provenance manifest at the fixed upstream SHA.
3. Extract the common private-write, lock, staging, pending/recovery, JSONC/JSON5/TOML/YAML patching code with its upstream tests before adding UI.
4. Remove database coupling and accept one ephemeral CPA profile; prove the eight security gates above.
5. Add adapters in risk order, with upstream-schema fixtures and synthetic HOME/APPDATA/XDG/PATH: single-file JSON first, then native locks/revisions, then multi-file Codex/Gemini/Claude Desktop.
6. Add the independently authored Simplified Chinese single-page UI and narrow IPC.
7. Run native three-platform CI. Unsupported/unverified adapters remain visibly blocked and are never reported as configured.

Platform support follows the fixed adapter source plus native acceptance tests, not the discarded Go helper catalog. In particular, the fixed CC Switch revision contains a Claude Desktop Linux path; the Tauri client must test that path rather than carrying forward the helper's blanket Linux block.
