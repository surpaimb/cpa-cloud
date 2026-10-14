# Employee config switcher write-safety gates

Status: development preview, writes disabled.

The Tauri command surface may detect clients and discover the authenticated
server configuration. `apply_configuration` must continue to fail closed until
all gates below have implementation and fault-injection evidence on each target
platform.

1. Private staging, backup and final-file permissions. Windows uses a protected
   user-only DACL and preserves the existing target security descriptor when
   replacing a file; new files receive the protected DACL before publication.
2. No durable plaintext-key backups. Rollback material is OS-encrypted or is
   removed immediately after durable completion, with a scan proving the old Key
   is absent.
3. Publication is journaled before post-write verification so a verification
   failure always has a rollback target.
4. Rollback compares the current digest and stable file identity with the bytes
   published by this operation. External edits produce a recovery conflict and
   are never overwritten or deleted.
5. Native handle-based path-chain validation rejects symlinks, reparse points,
   junction escapes and file-identity changes for target, staging, backup and
   journal paths.
6. A flushed, durable `planned -> published -> verified -> complete` journal
   supports startup recovery. A corrupt journal blocks new writes; completion is
   manifest-last and stale temporary material is cleaned without deleting
   unrelated files.
7. Format-specific strict validation rejects duplicate or ambiguous TOML, YAML,
   dotenv and JSON/JSONC definitions before any publication.
8. Existing `cpa-cloud` entries require an ownership record whose digest matches
   the live entry. Missing, corrupt or mismatched ownership fails closed.

The current OpenCode planner implements gate 8 at planning time and preserves
JSONC source through the fixed CC Switch parser. It does not publish bytes.

## Create-only transaction slice

The native branch now contains a deliberately narrow transaction primitive for
synthetic-root testing. It can create a file only when the target and every
target in the same transaction are absent. Existing client files are a hard
collision, so this slice never persists a plaintext backup of a previous Key.

The primitive writes secret-bearing bytes to a private `0600` staging file
inside a `0700` transaction directory (or a protected owner DACL on Windows),
flushes an append-only digest/identity journal, and publishes with an atomic
hard link. Because no previous file exists, recovery never performs a path-based
delete or replacement: once all private stages are durable it verifies parent,
stage and target identities plus digests, then rolls the create-only operation
forward. Missing stages before publication are cleaned manifest-last; a target
replacement or modification becomes a blocking recovery conflict. Tests inject
crashes around manifest, staging, publication and verification and confirm that
the journal itself contains no Key bytes.

A persistent owner-private lock file combines an in-process mutex with an OS
lock (`flock` on POSIX; an exclusive-sharing file handle on Windows), so a
second client process cannot recover an active transaction. Windows directories
are opened with backup semantics and write access before `FlushFileBuffers`;
the protected owner-only DACL is read back after assignment. These behaviors
must pass native runtime tests, not only cross-platform compilation.

This is not clearance for live writes. Native handle-relative traversal must
still replace the current path re-checks to close adversarial rename races, and
replacement of existing files still needs encrypted or non-persistent rollback
material. Consequently `apply_configuration` remains fail-closed.

The same synthetic-root primitive now also has a Pi projection slice adapted
from the fixed CC Switch MIT schema. It creates `models.json` and a non-secret
ownership record together only when both are absent. It deliberately leaves
Pi-owned `auth.json` and `settings.json` untouched, so it registers a provider
without claiming to select or activate a default model. Existing Pi
`models.json` files remain blocked until the replacement transaction contract
above is implemented. The create-only entry point accepts only canonical
`models.json` and `cpa-cloud.ownership.json` names in the same parent directory;
tests preserve sentinel bytes, file identity, and modification time for Pi-owned
`auth.json` and `settings.json`.

OpenClaw now has the same synthetic-root boundary. Its planner follows the
fixed signed `v2026.9.8` public custom-provider schema and a narrowly adapted
CC Switch MIT provider shape. When both files are absent, it creates
`openclaw.json` and a non-secret ownership record in one create-only
transaction, registers the CPA provider as `openai-responses`, and selects the
server-declared default model through `agents.defaults.model.primary`. Any
existing OpenClaw config remains a hard conflict until the replacement contract
above is implemented and validated against a fixed native client fixture. The
entry point accepts only canonical `openclaw.json` and
`cpa-cloud.ownership.json` names in the same parent directory.

Codex now has a separate synthetic-root create-only projection based on the
public OpenAI Codex configuration contract and a fixed local binary fixture. It
creates only canonical same-parent `config.toml` and
`cpa-cloud.ownership.json` files when both are absent, selects the
server-declared Responses default, disables client retries and WebSocket
transport, skips OpenAI login, and leaves Codex-owned `auth.json` unchanged.
The employee Key is written only into the private new config target through the
documented programmatic bearer-token field; it is absent from ownership,
journal and debug output. Any existing `config.toml` remains a hard conflict,
and the discouraged direct-token field plus missing cross-version native
fixtures remain explicit blockers for real writes.

Claude Code now has a separate synthetic-root create-only projection based on
the official Claude Code settings and LLM gateway contracts plus a fixed native
`2.1.266` parser fixture. It creates only canonical same-parent
`settings.json` and `cpa-cloud.ownership.json` files when both are absent,
places the CPA gateway URL and bearer employee Key in the documented `env`
block, and selects the server-declared Anthropic Messages default through the
top-level `model` setting. The separate Claude-owned `~/.claude.json` login and
project-state file remains byte-for-byte untouched, including file identity and
modification time in tests. Any existing `settings.json` remains a hard
conflict, and cross-version native fixtures plus the replacement transaction
contract remain blockers for real writes.

Gemini CLI now has a separate synthetic-root create-only projection based on
the fixed official `v0.62.0` configuration, authentication, and content-generator
sources. It creates only canonical same-parent `settings.json`, `.env`, and
`cpa-cloud.ownership.json` files when all three are absent, persists the accepted
`gemini-api-key` authentication selection, supplies the custom gateway through
`GOOGLE_GEMINI_BASE_URL`, stores the employee Key in the new private `.env`,
selects the server-declared Gemini default model, and disables telemetry and
prompt logging. The fixed `v0.62.0` CLI startup validator rejects `gateway` as a
persisted auth selection even though the core transport defines it internally;
a regression fixture locks this boundary. The ownership record, journal, and
debug output contain no employee Key, while unrelated Gemini state such as
`trustedFolders.json` remains byte-for-byte untouched, including file identity
and modification time in tests. Any existing managed file is a hard conflict,
with dedicated settings-, `.env`-, and ownership-only collision coverage. No
native Gemini CLI binary is available in the local test environment, so fixed
native parsing/transport evidence and the replacement transaction contract
remain blockers for real writes.

Grok Build now has a separate synthetic-root create-only projection based on a
fixed official source commit and its public custom-model contract. It creates
only canonical same-parent `config.toml` and `cpa-cloud.ownership.json` files
when both are absent, registers every employee-visible Responses model with
`api_backend = "responses"`, restricts the ordinary chat picker through
`allowed_models`, and selects the server-declared default. That setting does
not constrain Grok's internal web-search, image, subagent/fork, or fleet model
paths, so the adapter does not claim that every Grok request is restricted to
the employee-visible set. Each registered model uses the CPA `/v1` base URL and
the employee Key through Grok's documented bearer `api_key` field, with
`reasoning_summary = "none"` for gateway compatibility; telemetry and feedback
are disabled. The separate Grok-owned `auth.json` remains byte-for-byte
untouched, including identity and modification time in tests. Any existing
config or ownership file is a hard conflict. No native Grok Build binary is
available locally, and the service discovery contract does not yet provide
per-model context-window metadata, so fixed native evidence and the replacement
transaction contract remain blockers for real writes. Real detection or writes
also require either honoring or explicitly rejecting `GROK_HOME`, constraining
model IDs to CPA's `validIdentifier` contract or safely escaping Grok glob
semantics, and proving the result with a fixed native-client fixture. A future
live caller must derive the canonical Grok parent itself rather than accepting
an arbitrary parent from its caller.

Hermes Agent now has a separate synthetic-root create-only projection based on
the fixed official `0e427d788d253630d38a3a51cd76849e73cd6925`
configuration and provider contracts. It creates only canonical same-parent
`config.yaml` and `cpa-cloud.ownership.json` files when both are absent,
registers every employee-visible Chat Completions model under the named
`cpa-cloud` provider, explicitly selects `custom:cpa-cloud`, disables runtime
`/models` discovery, and selects the server-declared default. The employee Key
is confined to the private new config target through the documented inline
`api_key` field and remains absent from the ownership record, journal, and
debug output. Existing `.env`, `auth.json`, `SOUL.md`, and all other Hermes
state remain byte-for-byte untouched, including identity and modification time
for the tested sentinels. Any existing config or ownership file is a hard
conflict. No native Hermes binary is available locally, so fixed native
parsing/transport evidence and the replacement transaction contract remain
blockers for real writes. The fixed official source defines `~/.hermes` only
as the POSIX default; native Windows defaults to `%LOCALAPPDATA%\\hermes`.
`HERMES_HOME` wins after Hermes-style `~`/environment expansion, while
`HERMES_DATA_DIR_SUFFIX` is appended literally only to the platform default.
The current detector is best-effort and does not reproduce every one of those
native resolution rules. A future live caller must either implement and test
the exact fixed-version rules or fail closed when an override cannot be
resolved; it must never accept an arbitrary configuration parent from its
caller.

MiniMax Code now has a separate synthetic-root create-only projection based on
the official signed `v0.5.6` release and its public BYOK contract. It creates
only canonical same-parent `config.yaml` and `cpa-cloud.ownership.json` files
when both are absent, registers every employee-visible Responses model under
`custom_provider.cpa-cloud`, fixes the provider API to `openai-responses`, and
selects the server-declared default through the documented
`custom_provider:cpa-cloud/<model-id>` reference. The employee Key is confined
to the private new config target through `options.apiKey` and remains absent
from ownership, journal, and debug output. Existing login, MCP, prompt, plugin,
cache, and session state remain byte-for-byte untouched in synthetic tests.
The planner treats every visible `config.yaml.lock` entry, including an
unexpected file type, as a hard conflict and treats lock inspection errors as
conflicts. That preflight is still not a race-free acquisition of MiniMax
Code's `proper-lockfile` protocol. A future live writer must atomically acquire
a compatible native lock, coordinate it with the CPA transaction lock, and
hold both through publication. Fixed native parser/transport evidence and the
replacement transaction contract therefore remain blockers for real writes.
The current detector covers only `MINIMAX_DATA_DIR`, `MAVIS_DATA_DIR`, and the
plain `~/.minimax` default. The fixed client can also select `--data-dir`,
`--profile`, profile-scoped `~/.minimax-<profile>` roots, and migration-aware
`~/.mavis[-profile]` fallbacks. A future live caller must derive the exact
active directory or explicitly reject unsupported overrides; it must never
accept an arbitrary parent from its caller.

Claude Desktop remains detection-only. CPA now applies the official 32 MiB
request limit to both `/v1/messages` and `/v1/messages/count_tokens`, returns
Anthropic-shaped 413 `request_too_large` above that boundary, and keeps the
other model routes at their existing lower limit. Complete fixed-client
request/stream/tool/cancellation acceptance and the Claude Desktop
configuration transaction are still unresolved, so this preview does not
generate a third-party profile or claim ten-tool writable delivery.
The endpoint-level regression exercises the exact 32 MiB boundary and one
byte above it on both routes, and requires zero upstream calls, model-request
rows, and accounting-attempt rows for those malformed synthetic bodies. This
is size/error-shape evidence only: governance proof still uses the existing
smaller bounded request copy, so no general acceptance guarantee is inferred
for every otherwise-valid request below 32 MiB. Because body parsing currently
precedes authentication, production readiness also requires an explicit
concurrency/memory admission bound for unauthenticated large requests.
