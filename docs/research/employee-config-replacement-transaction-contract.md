# Employee configuration replacement transaction contract

Status: security contract for a synthetic-root development preview; live writes
remain disabled.

This contract defines the minimum transaction that may eventually replace one
or more existing employee-tool configuration files. It does not authorize the
Tauri command to write a real client directory. The current implementation
prepares and reloads encrypted rollback material for one existing Linux leaf,
exercises single-leaf publication and rollback only behind a test-only
synthetic-root approval, and persists a separate bounded Linux completion
tombstone. Multi-target policy, native tool validation/locking, operating-
system key custody, integrated cleanup and every production write path remain
incomplete.

## First-release local threat model

The first release trusts processes running as the same logged-in operating-
system account. A deliberately malicious same-account process that reads the
employee Key from a native tool configuration, retains a pre-opened writer,
ignores advisory locking, restores owner-write permission or actively swaps a
directory entry is outside this contract. Administrator, root, kernel and OS
compromise are also outside this contract.

Different-account access, accidental concurrent switcher launches, ordinary
overlapping edits, stale or replaced filesystem objects, crashes, restart and
storage failures remain in scope. The transaction must still use private
permissions or protected DACLs, stable directory handles and identities,
compare-and-swap checks, compatible locks, atomic replacement, encrypted
rollback material, durable recovery and a reviewed OS key provider with an
explicit Key lifecycle. Every tool also needs fixed native acceptance and its
own path, data-directory and lock semantics before real writes. This scope
decision does not authorize the Tauri `apply_configuration` command or claim
that any planned tool is ready for live mutation.

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
The same-user Unix leaf-entry and pre-opened-writer races documented in
[the write-safety gates](employee-config-switcher-security-gates.md) remain
known limitations even if this synthetic state machine passes. Linux
`unlinkat(..., AT_REMOVEDIR)` still resolves a leaf name at its own operation
boundary, while `flock` is advisory; extra `fstat`/`openat2` checks or a
quarantine rename only move, rather than close, the final same-UID exchange
window. The first-release threat model explicitly accepts that limitation by
trusting processes under the same login account; it does not claim that
ordinary same-UID DAC authenticates one same-account process from another.
Cross-account protection, accidental-concurrency locking and all identity/CAS
checks remain mandatory. A later threat model that includes malicious
same-account processes would require a separately owned broker/service or an
equivalent mandatory kernel-enforced boundary.

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
  a recoverable stop, not a fallback to disk storage. The application must use
  the Data Protection Keychain with a fixed dedicated access group and verify
  its own signing entitlement before the first query; otherwise an unauthorized
  query could be indistinguishable from a missing item. The reference must bind
  the transaction, current-user UUID commitment, access-group commitment and
  exact persistent reference. A temporary file-based Keychain is not evidence
  for this production contract.
- Linux desktop: a Secret Service item located by stable case-sensitive,
  non-secret lookup attributes rather than a persisted object path. The current
  Linux-only candidate is synthetic/integration-only: tests alone can approve
  its owner-private reconciliation-lock directory. The private lock file
  durably establishes a random provider namespace carried by lookup attributes,
  references and envelopes, so two independently approved roots cannot share a
  search namespace without sharing a lock. It serializes create under the
  namespace's cross-process `flock`, always calls `CreateItem` with
  `replace=false`, returns the same authenticated key/reference on retry, and
  refuses duplicate matches. It never invokes Unlock or Prompt; locked items or
  prompt requirements are explicit `locked` states. A fixed isolated native
  keyring restart/lock/delete fixture and encrypted transfer session remain
  unproved, so headless and production Linux remain unsupported.

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
5. the documented same-login-account trust assumption, accidental-concurrency
   behavior and explicit unsupported filesystems;
6. the complete fault matrix and evidence-preservation behavior.

## Current implemented subset

`live/backup.rs` remains a non-publishing Linux-only foundation. Format version 3
authenticates the exact nonzero transaction ID, recovery-key provider
kind/version and SHA-256 commitment of its opaque reference together with the
original owner UID, group GID and mode, root and target identities, digest and
length. The raw opaque reference is not copied into the backup manifest.
Preparation requires owner-read access
and refuses group/world bits or special permission bits before creating backup
material. Backup root/workspace directories are set and read back as exact
owner-only `0700`; ciphertext and manifest files are set and read back as exact
owner-only, single-link `0600` objects. Reload rejects a target identity change
or a backup-artifact type, link-count, permission or owner change without
modifying the target; backup contents remain authenticated by digest and
AES-GCM, but artifact inode identities are not persisted. The public receipt
exposes no content digest. Preparation still accepts only one UTF-8 leaf and a
caller-supplied in-memory key. Restart reload has no public raw-key bypass: it
first validates the durable path, private metadata, manifest binding, root and
target identities and ciphertext digest, then calls `RecoveryKeyProvider::open`
once with the exact durable transaction/reference. It then reopens the current
root-to-artifact namespace chain and requires the root, backup directory,
workspace, manifest, target and ciphertext identities to match the held objects
before decrypting. Every non-available provider outcome blocks without
calling `create` or `delete`. The slice does not implement a production Linux
key provider, desired stage, journal decision, publication, rollback, cleanup or
Windows/macOS backup support. macOS returns `UnsupportedPlatform` before
creating the backup directory, including for a private-mode target carrying an
extended ACL.

Format version 3 is intentionally discontinuous in this development preview.
Version 1 and version 2 manifests both fail closed and have no automatic
migration; they contain no published target mutation and require operator
removal or an explicitly reviewed offline inspection path. A future format
change must define recovery before any new writer is enabled.

`live/replacement.rs` now makes only the redacted event ordering executable in
memory. It enforces the 16-target sorted unique UTF-8 leaf set with a 4 KiB
per-name bound, the requirement that all backups precede every desired stage,
the durable-decision boundary,
publish-before-verify, every published target's rollback stage before any
restore, and terminal completion predicates. It is not serializable, carries no
identities/digests/security metadata, performs no I/O, and does not implement
the durable journal, recovery-blocked handling, publication, tool-format
validation, rollback or cleanup.

`live/linux_replacement.rs` now makes one smaller filesystem slice executable
only for a Linux synthetic root whose approval has no production constructor.
It accepts exactly one existing UTF-8 leaf, bounded to 4 MiB, owned by the
effective user, single-linked and private, under an owner-private `0700` root.
The module creates an owner-private transaction workspace and a bounded,
flushed JSON-lines journal containing identities, digests,
lengths, original UID/GID/mode, the non-secret provider reference and durable
phase events, but no configuration bytes or DEK. Before the commit decision it
physically allocates the complete journal plus a fixed owner-private pending
slot for every bounded event. Post-commit append reuses one slot exactly once;
restart verifies its length, checksum and event sequence, then overwrites or
extends only the incomplete journal suffix without truncating reserved extents.
A key-create intent is durable before the provider call, and the provider trait
must explicitly advertise transaction-idempotent creation before this slice
will call it.
Before `CommitPrepared` it
requires the version-3 encrypted pre-image, a flushed desired stage, a current
old-target comparison, physically allocated journal capacity and rollback-stage
inode, and a successful exact provider reopen/decrypt of the
pre-image.

Publication and restoration hold one synthetic-root target lock and use
`renameat2(RENAME_EXCHANGE)` between held workspace/root descriptors after
rechecking the expected current inode, digest, length, link count and security
metadata. Both post-exchange names are verified; a check-to-syscall mismatch is
undone only while the target remains the exact replacement and the displaced
entry retains the raw no-follow device/inode/type/metadata observed in the
workspace. The same bounded undo runs after restart if a crash occurs before
live verification, including for a symlink, public-mode file or multiply-linked
entry. The displaced entry remains durable recovery evidence until `Published`
or `Restored` is flushed. Other stale identity, content or metadata changes are
preserved as a recovery conflict. Synthetic validation
rejection authenticates the complete pre-image and records a rollback-stage
intent before rewriting the preallocated pending inode, flushing it and
publishing it no-replace. Terminal recovery is idempotent and revalidates the
exact desired or restored inode.

Tests interrupt after each key-intent/create/backup/stage/decision/publication/
verification/rollback-stage/restore/terminal boundary, repair torn journal
tails from preallocated slots after mutation, exercise deterministic
check-to-exchange edits including crash/restart with symlink, public-mode and
hard-linked entries, and overlap two real transactions at the shared target
lock. Tests also cover owner-read-only `0400`/`0500` rollback, physical journal/
slot allocation, stale targets plus symlink and hard-link changes,
preserve a post-publication external edit, scan durable
metadata for the original Key and DEK, and repeat terminal recovery. Before the
commit decision, restart returns a non-mutating `Uncommitted` blocker and
retains evidence; this slice does not yet define abandoned-workspace/key
cleanup. It also omits multi-target rollback, native tool-format validation and
tool lock compatibility, journal/artifact retirement, a production Linux key
provider, Windows/macOS primitives, automatic startup orchestration and all
real client paths. `apply_configuration` and the UI remain fail-closed.

`live/recovery_key.rs` fixes the provider-call boundary and redacted outcome
taxonomy. Recovery opens one exact non-zero transaction ID and
bounded provider reference and maps `locked`, `missing`, `wrong_identity`,
`corrupt` and `unavailable` to distinct blocking results. The recovery helper
has no create or delete path, so a failed open cannot silently generate a new
key or remove evidence. Provider references and key material are redacted from
`Debug`. `live/windows_dpapi.rs` now implements one Windows-only synthetic-root
provider using current-user DPAPI, `CRYPTPROTECT_UI_FORBIDDEN`, transaction-bound
optional entropy, a checked plaintext envelope, an exact protected-blob digest
reference, owner-only protected DACLs, stable directory handles and a compatible
exclusive lock. Create is no-replace and read-back verified; open after a fresh
provider instance never creates or replaces a missing value. Creation writes
and flushes an owner-private `.pending` blob, performs one same-directory
`NtSetInformationFile(FileRenameInformation)` operation on that file handle
with `RootDirectory` null, a fixed simple final leaf and replacement disabled,
verifies the staged/final 64-bit volume serial and complete 128-bit
`FILE_ID_INFO` plus the retained store-directory identity, flushes
the directory, and authenticates the final value. An exact create retry may
remove only a bounded private unpublished stage when no final exists, or return
the authenticated already-published value only after revalidating and flushing
the retained store directory; repeated flush failure remains unavailable.
Simultaneous final/stage state is preserved and refused. Tests inject
interruption after stage creation, write,
flush, publication, and directory flush, and prove no-overwrite conflict
preservation plus restart reopen/delete behavior. The provider rejects
`GetFileInformationByHandleEx(FileIdInfo)` failure and an all-zero unsupported
128-bit ID instead of falling back to the ReFS-unsafe legacy 64-bit file index;
tests cover an upper-half collision invisible to that legacy index, missing-ID
refusal, two distinct live objects, same-path object replacement, and same-
provider post-publish retry under persistent directory-flush failure. Delete
authenticates the exact
existing value first and flushes its directory. DPAPI plaintext output
and intermediate key arrays are zeroized, and neither key nor DPAPI blob enters
the journal, receipt, `Debug`, environment, command line, database or network.
Linux and macOS return `UnsupportedPlatform` before namespace creation. This
provider is not wired to a durable replacement transaction or real client write.
A separate macOS boundary now keeps production construction unavailable until
signing/access-group identity can be verified and exercises the intended
Data-Protection-Keychain request/state contract against an in-memory fake only.
The Linux Secret Service candidate similarly requires a test-only synthetic
approval, uses stable non-secret transaction/UID/lock-namespace/item attributes plus a
checksummed identity-bound envelope/reference, and exercises no-prompt,
no-replace create/open/delete behavior against a fake service fault matrix.
Its typed plain-session negotiation requires an exact empty output string and
non-root session path. The session-bus transport compiles only on Linux, but no fixed native keyring
fixture has yet established restart, locked-session or deletion behavior; it is
not a production provider.

The same module includes a provider-agnostic terminal-cleanup ordering gate. It
accepts only an already durable `Complete` or `RolledBack` decision, requires
the completion tombstone before workspace removal, refuses to call provider
deletion before both are recorded durable, and permits tombstone removal only
after deletion is confirmed as `deleted` or already `missing`. Locked,
wrong-identity, corrupt or unavailable deletion leaves the tombstone phase
retryable. The completion-tombstone transition remains a caller assertion at
this pure layer; the workspace transition now requires the concrete removal
receipt described below. A
separate Linux-only synthetic-root module now stages a versioned non-secret
completion tombstone as an owner-private single-link file, flushes it, and
publishes it under the final name with no-replace `renameat2`. A crash before
publication leaves an explicitly blocked `.pending` record rather than a
partial final tombstone. Restart revalidates and flushes every visible complete
record plus its directory and root before returning it as durable, binds it to
the held root identity and exact transaction/provider reference, and
reconstructs a bounded sorted set. It reopens the named file and directory to
reject replacement during the operation. A stable directory descriptor plus a
shared/exclusive advisory lock now spans enumeration and every duplicate or
capacity decision through publication, authorized logical removal, or inactive-
proof verification. Deterministic concurrency tests cover the 63-to-64 boundary
and a duplicate publisher racing an authorized removal. After the provider-
agnostic cleanup
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
fully missing records still fail closed.

The separate Linux-only synthetic-root cleanup journal now persists the five
provider-agnostic cleanup phases as immutable, bounded, owner-private event
files. Every event repeats and validates the root, terminal decision,
transaction, provider-reference, and expected workspace leaf/device/inode
binding; restart requires a contiguous
phase prefix and reconstructs the exact cleanup sequence. Event publication is
stage-and-flush followed by no-replace rename and directory/root flush. One
stable directory descriptor and advisory lock cover namespace enumeration,
phase-zero duplicate/capacity validation or append-prefix validation, and the
publication itself; concurrent 63-to-64 creation cannot publish a 65th
transaction, and concurrent duplicate appends cannot leave a pending record. A
visible `.pending` event blocks recovery rather than being inferred complete.
The durable wrapper records provider deletion only after the provider returns
`deleted` or `missing`, and an append failure rolls the in-memory sequence back
to its preceding durable phase so a restart can retry. The completion-tombstone
transition remains a caller assertion. The workspace transition no longer is:
a separate Linux-only synthetic-root store accepts only one exact empty `0700`
transaction workspace while holding the existing transaction lock, flushes a
private pending proof before descriptor-relative removal, flushes the
transaction directory and root, then atomically publishes and flushes a bounded
`.removed.json` proof. The receipt is non-serializable and is the only production
path into `WorkspaceRemovalDurable`. Restart and every key-deletion attempt
revalidate the exact root, transaction-directory and proof identity bindings
plus absence of the original workspace name. A crash after directory removal
but before proof publication leaves `.pending` and blocks automatic cleanup
rather than inferring success from absence. Renamed, replaced, non-empty,
missing, cross-workspace or mismatched state likewise fails closed. The
authorization, caller path and proof must all match the workspace leaf and
device/inode already recorded in the durable cleanup journal. This bounded
slice intentionally has no production constructor for the initial trusted
workspace-target evidence; until the durable replacement transaction supplies
that binding, initial cleanup-journal creation remains fail-closed. Synthetic
tests alone mint target evidence under `cfg(test)`. The store does not delete
transaction contents; its reviewed slice begins only after another component
has safely removed every known child and left the exact workspace empty. It
does not remove completed journals/proofs, authenticate against a hostile
same-user process, or orchestrate startup automatically. The separate Windows
DPAPI synthetic provider is not integrated with this Linux cleanup path. This
does not implement cleanup/proof garbage collection, automatic restart
orchestration or real client writes.
As the first bounded prerequisite for later garbage collection, a durably
`Complete` cleanup sequence can now authorize a non-mutating revalidation of
the exact inactive completion-tombstone proof. Only the resulting exact receipt
can produce an opaque artifact-retirement authorization carrying the same
terminal/transaction/provider and workspace leaf/device/inode binding. No code
accepts that authorization for unlink yet: a future slice must define a
crash-resumable cross-namespace deletion order, revalidation/locking at the
mutation boundary, transaction-ID non-reuse, and recovery from every partial
retirement state. Therefore all 64-entry journal/proof limits still apply.
The owner-private filesystem boundary is not authentication against another
process running as the same user. That deliberate-adversary case is an accepted
first-release non-goal, not a security property of this implementation.
Cross-account isolation, accidental-concurrency handling and every other live-
write gate remain unresolved requirements.

## Public platform references

- [Microsoft `CryptProtectData`](https://learn.microsoft.com/en-us/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata)
- [Microsoft `SetFileInformationByHandle`](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfileinformationbyhandle)
- [Microsoft `NtSetInformationFile`](https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntifs/nf-ntifs-ntsetinformationfile)
- [Microsoft `FileRenameInformationEx`](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-fscc/4217551b-d2c0-42cb-9dc1-69a716cf6d0c)
- [Apple: Using the keychain to manage user secrets](https://developer.apple.com/documentation/security/using-the-keychain-to-manage-user-secrets)
- [freedesktop.org Secret Service: collections and items](https://specifications.freedesktop.org/secret-service/latest/ch03.html)
- [freedesktop.org Secret Service: lookup attributes](https://specifications.freedesktop.org/secret-service/latest/lookup-attributes.html)
- [freedesktop.org Secret Service: service methods](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html)
- [freedesktop.org Secret Service: collection methods](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Collection.html)
- [freedesktop.org Secret Service: item methods](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Item.html)
- [freedesktop.org Secret Service: plain session](https://specifications.freedesktop.org/secret-service/latest/ch07s02.html)
- [freedesktop.org Secret Service: locking and unlocking](https://specifications.freedesktop.org/secret-service/latest/unlocking.html)
- [freedesktop.org Secret Service: prompts](https://specifications.freedesktop.org/secret-service/latest/prompts.html)
- [freedesktop.org Secret Service: standard errors](https://specifications.freedesktop.org/secret-service/latest/errors.html)
- [Linux `unlinkat(2)`](https://man7.org/linux/man-pages/man2/unlinkat.2.html)
- [Linux `flock(2)`](https://man7.org/linux/man-pages/man2/flock.2.html)
- [Linux `renameat2(2)`](https://man7.org/linux/man-pages/man2/renameat2.2.html)
- [Linux `openat2(2)`](https://man7.org/linux/man-pages/man2/openat2.2.html)
