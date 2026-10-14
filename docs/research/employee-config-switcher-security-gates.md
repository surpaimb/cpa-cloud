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
