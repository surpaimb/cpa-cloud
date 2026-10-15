# Employee configuration replacement transaction contract

Status: security contract for a synthetic-root development preview; live writes
remain disabled.

This contract defines the minimum transaction that may eventually replace one
or more existing employee-tool configuration files. It does not authorize the
Tauri command to write a real client directory. The current implementation
prepares and reloads encrypted rollback material for one existing Linux leaf
and persists a separate bounded Linux completion tombstone in a synthetic root.
Publication, rollback, operating-system key custody and integrated
crash-recoverable tombstone cleanup remain incomplete.

## Scope and non-goals

The first implementation is deliberately bounded to one canonical root, at
most 16 existing regular files, 4 MiB per file and 16 MiB total plaintext. Every
target must be owned by the current OS user, have one link, reside on the same
volume as its transaction workspace and have private permissions. A transaction
may modify only the exact target set recorded before its durable decision.

The following are not part of this slice:

- creating missing files or mixing create/delete/replace operations;
- following symlinks, junctions, reparse points or mount changes;
- replacing a file changed by another process after planning;
- accepting group/world-readable Unix files, extended macOS ACL entries, or a
  Windows owner/DACL that is not the current user-only policy;
- storing a recovery key in a file, environment variable, command line,
  journal, log, receipt or application database;
- running against a real `HOME`, `CODEX_HOME`, `APPDATA`, `XDG_CONFIG_HOME` or
  tool directory.

## Required platform primitives

All opens, publication and cleanup are relative to retained directory handles.
The implementation must preserve the existing file's security metadata while
ensuring that the resulting secret-bearing file remains private.

- Linux uses `openat`/`fstatat`/`renameat2`-class descriptor-relative
  operations. A live implementation must prove the exact atomic replacement
  primitive on every supported filesystem. Falling back from a missing atomic
  primitive to path-based remove-and-rename is forbidden.
- macOS uses descriptor-relative opens and an atomic swap/replace primitive
  whose behavior is covered by a fixed native test. Mode, owner and the complete
  ACL/security metadata must be compared and preserved.
- Windows opens every component without following reparse points, verifies the
  current-user owner and protected owner-only DACL on the opened handles, and
  uses a documented atomic replace primitive. The original security descriptor
  is applied to the replacement and read back before success.

If a platform or filesystem cannot provide the required primitive, replacement
returns `unsupported_platform_or_filesystem` before backup or target mutation.
The unresolved same-user Unix leaf-entry and pre-opened-writer races documented
in [the write-safety gates](employee-config-switcher-security-gates.md) remain a
live-write blocker even if this synthetic state machine passes.

## Recovery-key custody interface

Each transaction receives a fresh random 256-bit data-encryption key (DEK). The
DEK is held in zeroizing memory and is never serialized in plaintext. Durable
metadata contains only a random transaction identifier, a non-secret provider
kind/version and a provider-specific opaque key reference or wrapped DEK. The
recovery interface is logically:

```text
create(transaction_id) -> {key_reference, dek}
open(transaction_id, key_reference) -> dek
delete(transaction_id, key_reference)
```

`open` must work after process restart without reading target contents or
contacting CPA Cloud. It must return exactly one of `available`, `locked`,
`missing`, `wrong_identity`, `corrupt` or `unavailable`; none may silently
generate a new key. Any result other than `available` preserves all transaction
and target evidence, blocks new writes for that root and reports a redacted
recovery-required condition.

Planned platform providers are gated separately:

- Windows: current-user DPAPI without `CRYPTPROTECT_LOCAL_MACHINE`; the wrapped
  DEK may be stored beside the encrypted backup, but decryption must remain tied
  to the same user credentials and normally the same machine.
- macOS: a non-synchronizing Keychain generic-password item addressed by stable
  service plus transaction attributes. Locked or unavailable Keychain state is
  a recoverable stop, not a fallback to disk storage.
- Linux desktop: a Secret Service item located by stable lookup attributes.
  Locked collections and prompt requirements are explicit `locked` states.
  Headless Linux has no accepted provider yet and therefore remains unsupported.

Before production, each provider needs native restart, locked-store, deleted-
item, wrong-user, corrupted-reference and cleanup-crash tests. Machine/profile
loss is disaster recovery, not automatic rollback; deployment documentation
must state that encrypted rollback material may become unrecoverable and give a
manual evidence-preserving procedure.

The key item is deleted last, after a durable terminal decision and durable
workspace removal. Before workspace removal, the engine writes and flushes a
non-secret completion tombstone outside that workspace containing the
transaction ID, provider kind and key reference. A crash can therefore leak an
orphan key item, but cannot leave an incomplete transaction without its
recovery key. Startup uses the tombstone to retry key deletion, then removes and
flushes the tombstone. Orphan key garbage collection must require this durable
completed-transaction tombstone and must never infer completion from a missing
workspace alone.

## Durable state machine

The journal is bounded, append-only, private and flushed after each transition.
It contains no configuration bytes, employee Key, DEK or decrypted backup.

1. `Begin`: records format version, transaction ID, root identity, key-provider
   metadata and the complete sorted target-name set.
2. `BackedUp(target)`: the authenticated encrypted pre-image, original digest,
   stable identity, length and security metadata are durable. Every target must
   reach this state before any desired stage is written.
3. `Staged(target)`: a private desired-content stage is durable and its digest,
   length, identity and intended security metadata are recorded. The platform's
   fixed native test must prove that this identity is the expected published
   identity; otherwise that platform/filesystem is unsupported.
4. `CommitPrepared`: the complete old and desired target sets have been
   revalidated, the recovery key has been reopened through the provider, and
   enough journal budget remains for every terminal event. This is the durable
   commit decision.
5. `Published(target)`: atomic replacement has occurred and the new target
   identity/digest/security metadata are recorded.
6. `Verified(target)`: the exact opened target matches the published record and
   strict tool-format validation succeeds.
7. `RollbackPrepared`: strict validation failed, or a publication failure was
   classified as permanently non-retryable without observing an external
   identity. The redacted failure class, authenticated pre-image set and
   reserved worst-case journal budget are durable. This marker changes restart
   policy from roll-forward to rollback.
8. `RollbackStaged(target)`: a private rollback stage has been created from the
   authenticated pre-image, flushed, and its new stage identity, original bytes
   digest and intended security metadata are durable. Every published target
   must have this record before any restore begins.
9. `Restored(target)`: a published target was atomically replaced by its recorded
   rollback stage. The resulting `restored_identity` must equal the recorded
   `rollback_stage_identity`; original bytes and security metadata are verified.
10. `Complete` or `RolledBack`: every target is respectively verified as desired
    or restored with original bytes and security metadata. Cleanup may proceed.

Before `CommitPrepared`, restart recovery removes only exact known stages after
snapshot revalidation; publication is forbidden in that phase. From
`CommitPrepared` until `RollbackPrepared`, recovery rolls forward: an old target
may be atomically replaced by its desired stage, and an already published target
may be verified. A retryable publication failure remains in this state and
stops the current attempt. A confirmed permanently non-retryable publication
failure after any publication may transition to rollback only after every
required pre-image is available and enough space remains to record and stage
the complete rollback. An external identity or a failure that prevents safe
rollback preparation preserves all evidence and enters a non-terminal
`recovery_blocked` condition; it never overwrites the target or reports success.
If strict validation fails, recovery likewise prepares rollback. It then
persists `RollbackPrepared`, creates and durably records every
`RollbackStaged(target)`, and only then restores the published set. After
`RollbackPrepared`, every restart continues rollback and never returns to
roll-forward.

For every recovery action, the live target must match either the recorded old
identity/digest/security metadata or this transaction's recorded staged/expected
published identity, desired digest and security metadata. This lets recovery
recognize the narrow crash window after atomic replacement but before the
`Published` append without accepting a same-content third-party inode. Missing
targets, third identities, digest or permission changes, extra hard links and
unrecognized workspace entries are
`recovery_conflict`; the engine preserves evidence and never overwrites or
deletes the external state.

`original_identity` is only a compare-before-replace fact for the pre-image.
Reconstructing encrypted bytes necessarily creates a new inode/file ID. The
durable `rollback_stage_identity` identifies that new object before restore,
and `restored_identity` must equal it after an atomic restore. Rollback restores
the original bytes and complete UID/GID/mode/ACL/DACL metadata, not the obsolete
`original_identity`.

## Ordering and crash requirements

- The recovery key must be durably reopenable before the first encrypted backup
  is accepted as durable.
- Every encrypted pre-image uses a fresh nonce, is authenticated and flushed
  before its manifest is published. The manifest binds root, target, identity,
  digest, length, security metadata, transaction ID and intended desired digest.
- All pre-images are durable before the first plaintext desired stage is
  created, limiting the interval in which both old and new employee Keys are on
  disk.
- `CommitPrepared` is durable before the first target mutation.
- A publication record is durable before strict post-write validation.
- A validation failure never reports success. Before the durable
  `RollbackPrepared` marker, rollback reopens and authenticates every required
  pre-image and reserves worst-case journal space. After that marker, it creates
  and flushes all private rollback stages, appends every `RollbackStaged` record,
  and revalidates the full set before the first restore. It then rechecks each
  published target, atomically replaces it, and records the new restored
  identity plus verification of original bytes and metadata.
- Directory durability is required after creating/removing workspaces and after
  every atomic replacement. Unsupported directory flush semantics fail closed.
- After `Complete` or `RolledBack`, stages and encrypted backups are removed and
  directory removals flushed; only then is the key item deleted.

## Fault-injection and review interface

The security review interface is a pure synthetic-root API with an injected key
provider and an injected fault after every durable boundary. It must expose
only redacted receipts: transaction ID, target names and state; never bytes,
digests derived from a secret for logging purposes, keys or provider blobs.

Required negative tests cover crashes before/after every state above; wrong,
missing and locked recovery keys; ciphertext/manifest/journal tampering; target,
parent and workspace rename/replacement; pre-opened writers; permission, owner,
ACL, DACL, hard-link and volume changes; one target failing in a multi-target
transaction; rollback failure; cleanup failure; restart from every partially
published prefix; and concurrent recovery attempts. Tests must prove that an
external edit is preserved, no plaintext pre-image survives durable completion,
and no success receipt is produced before every target is verified.

Review should approve these interfaces before publication code is added:

1. key-provider outcome taxonomy and deletion ordering;
2. per-platform atomic replacement and directory-flush primitives;
3. exact security metadata captured, authenticated and restored;
4. decision point and roll-forward versus rollback policy;
5. same-user race assumptions and explicit unsupported filesystems;
6. the complete fault matrix and evidence-preservation behavior.

## Current implemented subset

`live/backup.rs` remains a non-publishing Linux-only foundation. Format version 2
authenticates the original owner UID, group GID and mode together with the root
and target identities, digest and length. Preparation requires owner-read access
and refuses group/world bits or special permission bits before creating backup
material. Backup root/workspace directories are set and read back as exact
owner-only `0700`; ciphertext and manifest files are set and read back as exact
owner-only, single-link `0600` objects. Reload rejects a target identity change
or a backup-artifact type, link-count, permission or owner change without
modifying the target; backup contents remain authenticated by digest and
AES-GCM, but artifact inode identities are not persisted. The public receipt
exposes no content digest. It still accepts
only one UTF-8 leaf and a caller-supplied in-memory key; it does not implement a
key provider, desired stage, journal decision, publication, rollback, cleanup or
Windows/macOS support. macOS returns `UnsupportedPlatform` before creating the
backup directory, including for a private-mode target carrying an extended ACL.

Format version 2 is intentionally discontinuous in this development preview.
Version 1 manifests fail closed and have no automatic migration; they contain no
published target mutation and require operator removal or an explicitly reviewed
offline inspection path. A future format change must define recovery before any
new writer is enabled.

`live/replacement.rs` now makes only the redacted event ordering executable in
memory. It enforces the 16-target sorted unique UTF-8 leaf set with a 4 KiB
per-name bound, the requirement that all backups precede every desired stage,
the durable-decision boundary,
publish-before-verify, every published target's rollback stage before any
restore, and terminal completion predicates. It is not serializable, carries no
identities/digests/security metadata, performs no I/O, and does not implement
the durable journal, recovery-blocked handling, publication, tool-format
validation, rollback or cleanup.

`live/recovery_key.rs` now fixes only the pure provider-call boundary and
redacted outcome taxonomy. Recovery opens one exact non-zero transaction ID and
bounded provider reference and maps `locked`, `missing`, `wrong_identity`,
`corrupt` and `unavailable` to distinct blocking results. The recovery helper
has no create or delete path, so a failed open cannot silently generate a new
key or remove evidence. Provider references and key material are redacted from
`Debug`. No DPAPI, Keychain, Secret Service, filesystem, environment-variable
or network provider is implemented, and no durable journal calls this interface.
The trait boundary cannot prove that a future provider's own `open`
implementation does not mint or replace a key; fixed native restart and
identity-binding tests remain mandatory.

The same module includes a provider-agnostic terminal-cleanup ordering gate. It
accepts only an already durable `Complete` or `RolledBack` decision, requires
the completion tombstone before workspace removal, refuses to call provider
deletion before both are recorded durable, and permits tombstone removal only
after deletion is confirmed as `deleted` or already `missing`. Locked,
wrong-identity, corrupt or unavailable deletion leaves the tombstone phase
retryable. The caller assertions alone do not prove filesystem durability. A
separate Linux-only synthetic-root module now stages a versioned non-secret
completion tombstone as an owner-private single-link file, flushes it, and
publishes it under the final name with no-replace `renameat2`. A crash before
publication leaves an explicitly blocked `.pending` record rather than a
partial final tombstone. Restart revalidates and flushes every visible complete
record plus its directory and root before returning it as durable, binds it to
the held root identity and exact transaction/provider reference, and
reconstructs a bounded sorted set. It reopens the named file and directory to
reject replacement during the operation. After the provider-agnostic cleanup
gate reaches `KeyDeletionConfirmed`, it can issue a non-serializable
authorization for the Linux store to revalidate the exact
transaction/reference, rename that one final tombstone to a `.deleting` intent,
flush it, then rename the intent to an inactive `.removed` completion proof and
flush the namespace and root again. A restart with fresh exact authorization
can resume either durable state, and an already durable proof can reproduce the
exact receipt without treating an absent record as success. The receipt has no
production constructor outside the tombstone store; only the exact receipt
returned after the inactive proof and both final flushes can advance the cleanup
gate to `Complete`. Public restart enumeration validates but excludes inactive
proofs, and transaction IDs represented by a proof cannot be reused. Proofs are
bounded by the same 64-entry namespace limit and deliberately retained because
this slice does not implement safe proof garbage collection. Mismatched or
fully missing records still fail closed. This does not implement a provider
deletion backend, general durable cleanup journal, workspace removal,
operating-system key provider, proof garbage collection, automatic restart
orchestration or real client writes.
The owner-private filesystem boundary is not authentication against another
process running as the same user; the documented same-user race remains a
blocker.

## Public platform references

- [Microsoft `CryptProtectData`](https://learn.microsoft.com/en-us/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata)
- [Apple: Using the keychain to manage user secrets](https://developer.apple.com/documentation/security/using-the-keychain-to-manage-user-secrets)
- [freedesktop.org Secret Service: collections and items](https://specifications.freedesktop.org/secret-service/latest/ch03.html)
- [freedesktop.org Secret Service: locking and unlocking](https://specifications.freedesktop.org/secret-service/latest/unlocking.html)
