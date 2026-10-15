# Employee config switcher write-safety gates

Status: development preview, writes disabled.

The Tauri command surface may detect clients and discover the authenticated
server configuration. `apply_configuration` must continue to fail closed until
all gates below have implementation and fault-injection evidence on each target
platform.

## First-release local threat model

The first release treats processes running as the same logged-in operating-
system account as trusted principals. An intentionally malicious same-account
process that reads the employee Key from a native tool configuration, retains a
pre-opened writer, ignores an advisory lock, restores owner-write permission or
actively races a directory entry is explicitly out of scope. Administrator,
root, kernel and operating-system compromise are also out of scope.

The following remain in scope and mandatory:

- preventing another OS account from reading or modifying configuration,
  staging, backup, journal, proof and recovery-key material;
- coordinating accidental concurrent switcher processes and ordinary
  overlapping tool edits;
- detecting stale, changed, renamed or replaced objects with compare-and-swap
  checks, stable directory handles and file identities;
- recovering safely from crashes, restart, storage errors and partial durable
  transitions.

Owner-private permissions are therefore a cross-account boundary, not an
authentication boundary between processes of the same account. This threat-
model decision removes resistance to a deliberately hostile same-account
process as a first-release gate only. It does not relax private permissions or
DACL/ACL validation, locking for accidental concurrency, atomic replacement,
encrypted backup and rollback, OS key-provider and Key-lifecycle requirements,
restart recovery, tool-specific path/lock handling or fixed native-tool
acceptance. It does not enable `apply_configuration` or establish that any of
the ten planned tools is ready for real writes.

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
the journal itself contains no Key bytes. Separate pre-begin fault injection
covers crashes after workspace creation, after creation of the empty journal,
and after `Begin` but before the first `Planned` event. Recovery treats all
three as uncommitted and removes only the known empty/metadata-only shapes. A
zero-length journal is cleaned only when it is the workspace's sole entry; any
additional entry makes the orphan ambiguous, blocks recovery, and remains
untouched for operator inspection. A separate ordinary I/O fault after `Begin`
proves the same-process recovery path returns the original error rather than a
false success receipt.

Recovery now bounds the untrusted journal before deserialization: one transaction
may describe at most 64 files, the journal is limited to 512 KiB, each newline-
terminated event is limited to 8 KiB, and at most 258 events are accepted. A
target field is limited to 4096 UTF-8 bytes, stage names must exactly match the
engine-generated index, and SHA-256 digests must be exactly 64 hexadecimal
characters. Unknown fields are rejected at both the event and nested identity
levels. Before creating a workspace, the writer budgets the entire worst-case
journal; before recovery publishes any target, it reserves space and event slots
for every missing `Published` and `Verified` event plus `Prepared`. Oversized,
unterminated, excessive, sparse, padded or malformed metadata therefore blocks
recovery before any target publication and remains untouched for operator
inspection. Native-independent negative tests cover the file-count, total-size,
line-size, event-count, field-size, JSON-escaping and post-publication append-
budget boundaries.

Initial application and restart recovery now share one publication path bound
to one open journal handle. That path verifies every stage, parent and existing
target first, then durably appends all remaining `Published` and `Verified`
records followed by a fixed-size `Prepared` roll-forward marker before creating
a missing target. The engine reads the bounded journal twice to obtain a stable
byte snapshot and, immediately before each first publication and again
afterward, checks the held file identity, the path identity, length and exact
bytes. `Prepared` is itself the durable decision: after every target verifies,
the engine cleans the known workspace without another journal write. A crash
before or during cleanup therefore replays the same prepared roll-forward
instead of depending on an in-place terminal-marker rewrite. A regression test
also removes a stage after its prepared target hard link exists and proves that
restart recovery verifies the target and completes cleanup. Parsed cleanup
branches revalidate their initial snapshot before deleting known state.
The prepared journal is sealed before the first target publication. Unix
changes the held single-link journal to owner-read-only mode while retaining an
exclusive advisory lock; Windows holds the journal with write sharing denied.
A new fault hook in the former snapshot-check-to-hard-link interval proves that
a newly opened append writer is refused on Linux, macOS and Windows before the
target is created. A writer that already held a Unix descriptor before sealing
can still ignore the advisory lock; the post-publication snapshot detects that
mutation, but cannot undo the already-created target. This same-user adversarial
case is a known limitation accepted under the first-release threat model; the
post-publication check remains required for accidental interference.

Publication and cleanup now retain a stable directory chain from the configured
root through every target parent and transaction workspace. Linux and macOS open
children with `openat(..., O_NOFOLLOW)`, publish with descriptor-relative
`linkat`, enumerate through the held workspace descriptor, and clean known
children/workspaces with `unlinkat`. Windows holds every directory without
delete sharing, opens the journal and stage with write/delete sharing denied,
and marks the exact verified file or directory handle for deletion with
`SetFileInformationByHandle`. A target parent on another volume/device is
rejected before publication, so a mount point or junction cannot silently move
the hard-link destination across a filesystem boundary. Fault tests rename a
target parent after the sealed snapshot and rename a workspace immediately
before cleanup; both paths stop without publishing into or deleting the
replacement object. Unknown workspace entries are enumerated from the stable
directory and block deletion before any known entry is removed. Pre-`Begin`
cleanup keeps the same zero-length journal handle from validation through
deletion and locks that handle before enumeration or deletion; it never closes
and reopens the journal name. A replacement-window fault test preserves both
the verified evidence object and the foreign entry.

The remaining Unix boundary is explicit: an equally privileged process running
as the same OS user can ignore `flock`, restore owner-write permission, and race
an individual directory entry between the final `fstatat` identity comparison
and `linkat`/`unlinkat`. POSIX descriptors keep ancestor rename, symlink and
mount redirection from changing the base directory, but portable Linux/macOS
APIs cannot unlink a regular file solely by its already-open descriptor. This
same-user adversarial case is explicitly outside the first-release threat model.
Stable handles, identity checks and the transaction lock remain required for
cross-account isolation and accidental concurrency.

Before scanning recovery state, the engine now requires the transaction root
and each workspace to retain their stable file identity and private directory
boundary: exact `0700` ownership by the effective user on Unix, with no extended
ACL entries on macOS, or both ownership by the current process user SID and the
verified protected owner-only DACL on Windows. Workspace names
must exactly match the engine-generated `tx-<pid>-<nonce>-<counter>` form. Unknown names or weakened
permissions block recovery and preserve the workspace. These checks remain
path read-backs, but subsequent publication and cleanup are anchored to the
retained native handles described above.

A persistent owner-private lock file combines an in-process mutex with an OS
lock (`flock` on POSIX; an exclusive-sharing file handle on Windows), so a
second client process cannot recover an active transaction. Windows directories
are opened with backup semantics and write access before `FlushFileBuffers`;
the owner is first set to the process user SID, then that SID and the protected
owner-only DACL are read back after assignment.
macOS checks the already-open object with `acl_get_fd_np`, rejects inherited or
manually added extended ACL entries instead of treating BSD mode bits alone as
proof of privacy, and requires the lock path still to name the locked inode.
A Windows-native production-predicate test rejects a foreign owner SID, while a
separate end-to-end recovery test rejects a world-accessible workspace DACL and
preserves the journal. An end-to-end owner-mismatch recovery test remains open.
Native negative recovery tests also reject a macOS workspace ACL and a removed
macOS lock path. These behaviors must pass native runtime tests, not only
cross-platform compilation.

This is not clearance for live writes. Although the same-user Unix entry race
described above is an accepted out-of-scope limitation, replacement of existing
files still needs encrypted or non-persistent rollback material and every other
gate in this document. Consequently `apply_configuration` remains fail-closed.

## Encrypted rollback-material foundation

The bounded replacement state machine, restart-key custody interface and
platform review gates are specified separately in the
[replacement transaction contract](employee-config-replacement-transaction-contract.md).

The next bounded synthetic-root slice prepares, but does not yet consume, a
durable encrypted pre-image for one existing leaf file on Linux. Format version
3 authenticates the exact nonzero replacement transaction ID, recovery-key
provider kind/version and SHA-256 commitment of the opaque provider reference,
in addition to the original owner UID, group GID and private mode. The raw
opaque reference is not copied into the backup manifest. The slice requires
owner-read access and refuses group/world access or special permission bits
before creating backup material.
The caller
supplies a 256-bit recovery key that remains in zeroizing memory; the slice uses
AES-256-GCM with a fresh random nonce and binds the ciphertext to the root and
target identities, target name, original digest, length, Linux security
metadata and exact transaction/provider reference commitment. The ciphertext is
flushed before a non-secret manifest is published last. Tests reload the
pre-image using only durable state and the supplied key, scan the transaction
workspace for the old employee-Key substring and recovery key, reject the wrong
key and ciphertext tampering, and refuse a replaced target identity without
overwriting the external edit. The encrypted-backup directory has a separate
namespace from the create-only transaction engine; a restart/coexistence
regression runs create-only recovery and publication while the durable backup
remains reloadable.

Restart loading can now receive the exact durable transaction ID and provider
reference. It validates the durable workspace, private artifact metadata,
manifest binding, root/target identities and ciphertext digest before calling
only `RecoveryKeyProvider::open`, then reopens the current root, backup
directory, workspace, manifest, target and ciphertext names and requires their
identities to match the held objects before decrypting. There is no public
raw-key restart loader and no create/delete
fallback. Missing or corrupt durable state, binding/identity conflicts, and
locked, missing, wrong-identity, corrupt or unavailable provider outcomes all
block recovery. Synthetic provider tests prove invalid durable state performs
zero provider opens, a successful restart performs one exact open and zero
creates/deletes, and every blocked provider outcome also performs zero
creates/deletes. The zero-open claim is limited to missing/malformed state,
binding or identity conflicts, and ciphertext-digest mismatch detectable before
key access; a post-validation rename/replace or AEAD authentication failure can
block after one open. This is interface wiring, not acceptance of a production
Linux key provider.

On Linux, the synthetic root is opened by anchoring at `/` for absolute paths or
`.` for relative paths, then opening every normal root-path component relative
to the held parent descriptor with no-follow semantics. Parent traversal and
symbolic-link ancestors are refused. The target, separate backup directory,
workspace, ciphertext, and manifest are likewise opened relative to
already-held directory descriptors with no-follow semantics. A newly created
backup-directory entry is made durable by flushing the root descriptor before
success. Regression tests refuse both a symbolic-link root ancestor and an
explicit parent-traversal component without creating backup material. The slice
deliberately accepts only one UTF-8 leaf name owned by the current user, with
one hard link and at most 4 MiB. Backup root/workspace directories are enforced
and read back as exact `0700`; ciphertext and manifest files are enforced and
read back as current-owner, single-link `0600` objects on restart. Windows and
macOS return an explicit unsupported-platform error before backup-directory
creation until equivalent native handle-relative, ACL/security-descriptor and
protected-key integration exists. A macOS-native negative test attaches an
extended ACL to a `0600` target and proves this fail-closed boundary.

`live/backup.rs` itself remains non-publishing, but a separate bounded
`live/linux_replacement.rs` slice now consumes its version-3 material for one
existing leaf under a test-only approved Linux synthetic root. That slice uses
a private bounded journal whose physically allocated per-event slots repair an
exact torn tail without shrinking reserved extents, and it persists key-create
intent before requiring an explicit transaction-idempotent provider capability.
It serializes synthetic transactions with one shared target lock; two real
transactions overlap in the lock test. Journal, pending slots and rollback-stage
space are allocated before `CommitPrepared`. Descriptor-relative
`RENAME_EXCHANGE` keeps the displaced entry available for live or restart undo;
raw no-follow identity/type evidence covers unsafe-mode, hard-linked and symlink
entries without treating them as valid configuration. Before a
synthetic validation rejection is made durably rollback-directed, it
authenticates the pre-image; rollback then persists a stage intent, rewrites the
preallocated inode, flushes it and publishes it no-replace. Every restart after
`RollbackPrepared` remains on the rollback path. Stale, symlinked, hard-linked
or externally replaced targets are preserved as conflicts, and terminal
recovery is idempotent.

The approval has no production constructor. Pre-commit interruption currently
retains evidence and returns an `Uncommitted` blocker; abandoned-workspace/key
cleanup, multi-target atomic policy, fixed native tool validation and locking,
production Linux recovery-key custody, full ACL support, Windows/macOS
primitives, startup orchestration and transaction retirement remain open. The
Tauri command, UI and all real client writes therefore remain fail-closed.

A separate pure in-memory replacement sequence gate now exercises the reviewed
ordering without defining a journal encoding or touching the filesystem. It
requires every encrypted pre-image before any desired stage, every desired
stage before `CommitPrepared`, publication before verification, and every
published target's rollback stage before the first restore. It accepts only a
sorted unique set of at most 16 UTF-8 leaf names bounded to 4 KiB each and
refuses events after a terminal state. Identities, digests, security metadata,
durable I/O, `recovery_blocked`, publication and rollback remain unimplemented.

The recovery-key boundary defines bounded transaction/provider metadata and
redacted create/open/delete
outcomes, and an open-existing helper that can only return an available
zeroizing key or one of the five blocking recovery results. Tests prove that a
missing key invokes neither create nor delete and that keys and opaque provider
references stay out of `Debug`. A separate Windows-only synthetic-root provider
now uses current-user DPAPI without `CRYPTPROTECT_LOCAL_MACHINE`, forbids UI,
binds the transaction into optional entropy and a checked plaintext envelope,
and stores only a bounded protected blob behind owner-only DACLs, stable
directory handles and an exclusive cross-instance lock. Creation now writes an
owner-private `.pending` blob, flushes it, calls the documented user-mode
`NtSetInformationFile(FileRenameInformation)` path on that handle with
`RootDirectory` null, a fixed simple final leaf and replacement disabled,
verifies the stage/final 64-bit volume serial and complete 128-bit
`FILE_ID_INFO` plus the retained store directory identity, flushes that
revalidated directory object, and reads
the final blob back. On an exact create retry, a bounded private stage is
removed only when no final exists; a published blob is authenticated and
returned idempotently only after the retained store directory is revalidated
and flushed, while repeated flush failure remains unavailable and simultaneous
final/stage state is preserved and refused. Native tests cover every create
interruption boundary, same-provider post-publish retry under persistent
directory-flush failure, no-overwrite
conflict preservation, fresh-instance reopen, exact key equality,
missing/wrong transaction behavior, tampered blob/reference refusal,
idempotent deletion, lock classification and concurrent idempotent creation.
`GetFileInformationByHandleEx(FileIdInfo)` failure and an all-zero unsupported
128-bit ID fail closed without the ReFS-unsafe legacy 64-bit file-index
fallback; tests distinguish an upper-half ReFS collision and two distinct live
objects, detect same-path object replacement, and reject the missing-ID form.
DPAPI plaintext and temporary key arrays are
zeroized. Linux/macOS construction returns `UnsupportedPlatform` without
creating a namespace; this Windows provider has no environment/CLI/database/
plaintext fallback and is not wired to the durable transaction or real client
writes.

The macOS Keychain slice now records a pure, fake-`SecItem` contract without
touching Security.framework. It fixes Data Protection Keychain selection,
`synchronizable=false`, a dedicated access group,
`WhenUnlockedThisDeviceOnly`, no-interaction authentication, persistent-
reference read-back, transaction/current-user/access-group commitments,
duplicate-add reuse and exact-reference deletion with post-delete verification.
Only `interaction-not-allowed`, `interaction-required` and `not-logged-in` map
to `locked`; broad authentication, entitlement and unknown failures remain
`unavailable`. Wrong user or transaction is rejected from the reference before
any backend query. The real constructor remains unavailable on macOS because
the preview has no fixed signing team/access-group entitlement to verify.
Temporary file-based Keychains are explicitly not accepted as evidence for
Data Protection Keychain restart, locked-session, cross-user or durability
behavior, so native Keychain custody and real writes remain open gates.

The Linux Secret Service candidate is also synthetic-only. Its approval has no
production constructor and holds an already-open owner-private directory used
for a descriptor-relative, single-link `0600` reconciliation lock. That lock
file durably holds a magic/versioned checksummed record binding a random seed,
UID and the lock device/inode; the same fields derive the provider-namespace
identity, while corruption or copying the record to another inode fails closed.
Different approved roots therefore cannot share one Secret Service search namespace without also
sharing one lock. Explicit one-time fresh provisioning alone may create the
record with create-exclusive; normal reopen refuses missing, empty or truncated
records instead of minting a new namespace. Every operation acquires the held
inode and then reopens the directory entry no-follow to require the same
device/inode, private metadata and namespace bytes, so lock-entry replacement
fails closed. Stable non-secret lookup attributes bind application,
purpose, format, transaction, effective UID, lock namespace and random item ID;
checksummed reference and secret envelopes repeat the identity. Create is serialized across processes, uses
`CreateItem(replace=false)`, reconciles a prior exact item and refuses
duplicates. Open and delete re-search attributes instead of trusting a stored
object path. The client never calls Unlock, Prompt or SetSecret and maps prompt
requirements to `locked`; missing, wrong-identity, corrupt and unavailable are
kept distinct without create/overwrite fallback. Fake-service tests inject
search/create/get/delete faults and unknown post-create completion. No test
touches a real user keyring. The current D-Bus adapter uses the specification's
plain session within the accepted same-login-user threat boundary, accepts only
the typed exact empty output string and a non-root session path; encrypted
session negotiation plus a fixed isolated native keyring restart/lock/delete
fixture remain required before production Linux custody or real writes.

A provider-agnostic cleanup sequence gate now also refuses provider deletion
until a terminal decision, completion tombstone, and workspace removal have
each been recorded durable in that order. Workspace durability is no longer a
free-form caller assertion: the Linux synthetic-root path requires the exact
receipt from a bounded owner-private proof store after it removes one stable,
empty transaction workspace and flushes the transaction directory and root.
The durable cleanup journal repeats the expected workspace leaf and
device/inode in every event; authorization, caller path, proof, restart and
provider deletion must all match that binding and the exact workspace name must
remain absent. Pending, cross-workspace, renamed, replaced, non-empty or
mismatched state blocks cleanup. No production constructor currently issues
the initial trusted workspace-target evidence, so cleanup-journal creation is
fail-closed until the durable replacement transaction provides it; only
`cfg(test)` synthetic fixtures mint that evidence. The slice does not remove
transaction contents and remains gated on a producer that safely empties the
exact workspace first. It retains the exact
transaction/reference pair, treats
`deleted` and already-`missing` as idempotent completion, preserves the tombstone
phase on every blocking deletion outcome, and permits tombstone removal last.
It performs none of those filesystem operations itself and cannot prove
provider-side deletion behavior. A separate Linux-only synthetic-root
foundation now persists and reloads versioned completion tombstones with exact
`0700`/`0600` owner-private modes, no-follow descriptor-relative opens, private
staging plus no-replace atomic publication, bounded restart enumeration,
file/directory/root flushes and root/transaction/provider-reference binding.
One stable directory descriptor and a compatible advisory lock now cover
restart enumeration, duplicate/capacity validation, publication, logical
removal and inactive-proof verification. Deterministic concurrent tests prove
that two contenders for the 64th entry cannot publish a 65th record and that a
duplicate publisher cannot race an authorized removal into two active states.
Restart refuses a pre-publication `.pending` artifact and repairs durability of
a structurally complete final record before returning it. This tombstone store
remains Linux-only and unsupported on Windows/macOS before namespace creation;
the separate Windows DPAPI synthetic provider is not integrated with it. No
transaction-content cleanup integration, automatic crash-resume orchestration,
or real client write is enabled. Resistance to a hostile same-user process is
not claimed under the first-release threat model; cross-account isolation and
accidental-concurrency checks remain mandatory. A separate
Linux-only synthetic-root journal now makes the five cleanup phases durable as
immutable private files, rejects non-contiguous or `.pending` state on restart,
and reconstructs the exact transaction/provider-bound cleanup sequence. Its
stable directory descriptor and advisory lock cover enumeration, the 64-entry
capacity/duplicate or append-prefix decision, and no-replace publication;
concurrent boundary tests cover both phase-zero capacity and duplicate append.
Its workspace phase now requires and revalidates the exact durable removal
proof described above; it still does not remove completed journal or proof
records.
The first retirement prerequisite is now explicit but non-mutating: only a
durably `Complete` cleanup sequence may request revalidation of the exact
inactive completion proof, and only that exact receipt can produce an opaque
authorization bound to the same transaction/provider and workspace
leaf/device/inode. No unlink API consumes this authorization. Crash-resumable
cross-namespace deletion, mutation-boundary revalidation/locking,
transaction-ID non-reuse and partial-retirement recovery remain required, so
the 64-entry lifecycle limits are not yet reclaimed.
The pure cleanup gate can issue a non-serializable removal authorization only
after provider deletion is confirmed; the Linux store then revalidates the
exact transaction/reference, renames the final tombstone to a flushed
`.deleting` intent, then renames and flushes it as an inactive `.removed`
completion proof. A fresh exact authorization can resume either durable state
after restart, and the proof can reproduce only its exact receipt. The receipt
is constructible only inside the tombstone store after the inactive proof and
namespace/root flush succeed; only that exact receipt can advance the cleanup
gate to `Complete`. Public enumeration validates but omits inactive proofs,
while a fully missing or mismatched record is never accepted as successful
removal. The bounded proof remains on disk because safe proof garbage
collection is not implemented by this slice.

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

The fixed `2026.9.8` native-evidence slice remains preflight-only. Its manifest
is derived as `guarded-final/manifest.json` beneath an externally trusted probe
root before any JSON is read; the manifest cannot nominate the real profile.
The preflight rejects real `.openclaw` ancestor/descendant overlap,
symlink/junction and hardlink aliases, non-exact fixture bytes, and drift in the
pinned Node, package metadata, OpenClaw entry, or observed dependency-tree
hashes. A pure test verifies the fresh allowlisted environment mapping without
launching any helper, and the production probe module exports no general
process launcher and imports no child-process module. OpenClaw itself is blocked
before spawn because proxy environment variables are advisory and no OS-level
egress-isolation receipt issuer is available. Consequently there is no accepted post-run file
inventory, client-owned sentinel, parser result, or transport result. A
discarded `--version`/`--help` probe accidentally resolved the real profile;
the later metadata audit found no persistent write, but cannot establish that
the real configuration was not read, so read status remains indeterminate and
the run is excluded from acceptance.

Codex now has a separate synthetic-root create-only projection based on the
public OpenAI Codex configuration contract and fixed native Codex CLI `0.160.1`
Windows x86_64 evidence. It creates only canonical same-parent `config.toml` and
`cpa-cloud.ownership.json` files when both are absent, selects the
server-declared Responses default, disables client retries and WebSocket
transport, skips OpenAI login, and leaves Codex-owned `auth.json` unchanged.
The employee Key is written only into the private new config target through the
documented programmatic bearer-token field; it is absent from ownership,
journal and debug output. The fixed native CLI accepted the exact planner bytes
under an isolated `CODEX_HOME`, sent the synthetic provider bearer and model to
the loopback `/v1/responses` path, and preserved the existing `auth.json` bytes,
identity, length and timestamps. The loopback returned an intentional HTTP 400,
so no successful model response or broader transport compatibility is claimed.
The synthetic token appeared only in `config.toml`, not the CLI state databases.
Any existing `config.toml` remains a hard conflict, and the officially
discouraged direct-token field, single Windows fixture, missing cross-version
coverage, and incomplete production replacement/key-lifecycle gates remain
explicit blockers for real writes.

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
with dedicated settings-, `.env`-, and ownership-only collision coverage. The
fixed native `@google/gemini-cli@0.62.0` package accepted the exact planner bytes
in an isolated Windows home and completed a synthetic loopback streaming request
using the expected v1beta route and `x-goog-api-key`, while the unrelated
`trustedFolders.json` sentinel remained byte-, digest-, and mtime-identical.
System settings/defaults, the working directory, ancestor `.env` search, and
credential variables were isolated; a rejected-auth negative control exited
before any request, independently proving that the user settings file was read.
`GEMINI_CLI_HOME` is treated as a home root and `.gemini` is appended, matching
that fixed CLI contract. Cross-version acceptance and the replacement
transaction contract remain blockers for real writes.

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

The official v0.5.6 release asset is pinned by URL, exact `12707059`-byte size,
and SHA-256 in the fixed-source fixture. Its release notes explicitly exclude
upstream Windows and live-service acceptance. A bounded local HTTPS attempt on
2026-10-15 timed out before producing the asset, so this preview does not claim
native config parsing, native writer/read behavior, compatible lock acquisition,
or loopback Responses transport. Those gates remain false until the exact asset
passes both size and digest checks and runs only with isolated home, application
data, XDG, npm, PATH, working-directory and MiniMax data-directory state.

Claude Desktop remains detection-only. On Windows, detection is limited to the
official `%APPDATA%\Claude\claude_desktop_config.json` file; directory-only
`%LOCALAPPDATA%\Claude` and `Claude-3p` layouts are not accepted as evidence.
The public file contract configures local MCP `mcpServers`, while Anthropic's
remote custom connectors are likewise MCP tool/data integrations. Neither is a
public custom-model endpoint or employee-key routing contract, so Claude Desktop
remains fail-closed and cannot be described as model-routing capable. Whether it
remains in the first-release configurable target set is a pending user decision,
not an implementation assumption; the other tools continue through their
separate gates. CPA now applies the official 32 MiB
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

Grok Build read-only detection now reproduces the fixed official source rule:
a non-empty `GROK_HOME` value wins verbatim, otherwise `<home>/.grok` is used.
This closes only the detector's home-selection gap. Native parsing and transport,
model-ID/glob safety, configuration locking, replacement recovery, and real
writes remain blocked.
