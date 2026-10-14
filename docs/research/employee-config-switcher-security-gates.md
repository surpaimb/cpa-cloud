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
hard link. Recovery removes a published file only if both stable file identity
and digest still match; replacement or modification becomes a blocking recovery
conflict. Tests inject crashes before and after publication and verify that the
journal itself contains no Key bytes.

This is not clearance for live writes. Native handle-relative traversal must
still replace the current path re-checks to close adversarial rename races;
Windows directory durability and the exact owner-only DACL must pass on-target
review; replacement of existing files still needs encrypted or non-persistent
rollback material. Consequently `apply_configuration` remains fail-closed.
