# Linux synthetic empty-workspace removal receipt contract

Status (2026-10-16): development-preview implementation contract. This is a
narrow stacked extension of the
[Linux workspace content-retirement contract](linux-workspace-retirement-contract.md).
Its fixed base is 80fb3c1b2bf7cc6186f42845c7d4fb3e1f07b30a.

The implementation is limited to owner-private Linux synthetic roots created
by independently authored tests. It is not connected to a real home
directory, official client, production credential store, UI write, package,
deployment, or port 8787.

## Exact authorization

The mutation entry point accepts no caller-supplied workspace path. It derives
the sole candidate as
.cpa-cloud-employee-config-replacements/tx-<transaction-id> from the durable
terminal handoff. Authorization is issued only while cleanup is exactly
CompletionTombstoneDurable and only after consuming the exact
WorkspaceContentsRetiredReceipt. The receipt must repeat the terminal
outcome, transaction, recovery-key reference, workspace leaf, and workspace
device/inode. The authorization also binds the terminal handoff's root
device/inode.

The implementation holds the replacement namespace's existing
single-target.lock, opens every path relative to validated directory
descriptors with no-follow semantics, and requires owner-private metadata.
The active workspace must be the authorized inode and have no entries.
Unknown entries, external edits, root/workspace replacement, links, wrong
metadata, or identity drift are preserved and fail closed.

## Logical removal and durable receipt

There is deliberately no pathname rmdir. After durably writing an exact
pending proof, Linux performs one descriptor-relative
renameat2(RENAME_NOREPLACE) from the live replacement namespace into
.cpa-cloud-employee-config-workspace-removals/<transaction>.workspace.
The retained directory must still be the authorized inode, owner-private, and
empty. A replacement or newly inserted unknown entry in the final window may
be moved into this private evidence namespace, but post-move identity and
emptiness checks reject it and preserve it; no receipt is issued.

Success means the exact workspace is absent from the active replacement
namespace and the exact empty inode is retained as evidence. It does not claim
physical inode deletion, secure erasure, or capacity reclamation.

The implementation flushes the retained-evidence directory, replacement
directory, and synthetic root after the move. It then reopens and revalidates
the pending proof, publishes it without replacement, flushes the proof
directory and root, and reopens the final proof with the same identity and
bytes. Only this state can issue WorkspaceRemovalReceipt.

## Restart and fail-closed states

Recovery accepts only these exact combinations:

1. source present, no pending/final/retained evidence: validate and begin;
2. source present with matching pending proof and no retained evidence:
   resume the no-replace move;
3. source absent with matching pending proof and exact retained evidence:
   resume proof publication; or
4. source absent with matching final proof and exact retained evidence:
   reproduce the receipt.

All conflicting combinations fail closed. Fault injection covers pending-file
flush, proof-directory flush, root flush, logical-removal rename, both
post-rename directory flushes, the second root flush, final-proof rename, and
final proof/root directory flushes. Restart must converge to one exact receipt
or preserve evidence and block.

## Stop boundary

The cleanup journal consumes the exact receipt and durably records only
WorkspaceRemovalDurable. This slice never calls the recovery-key provider's
delete operation. Provider delete count is therefore zero at its acceptance
boundary. Key deletion, tombstone removal, production apply, UI mutation,
deployment and packaging remain outside this contract.

## Public sources and licenses

The implementation relies on the public Linux contracts for
[openat(2)](https://man7.org/linux/man-pages/man2/openat.2.html),
[renameat2(2)](https://man7.org/linux/man-pages/man2/rename.2.html),
[flock(2)](https://man7.org/linux/man-pages/man2/flock.2.html), and
[fsync(2)](https://man7.org/linux/man-pages/man2/fsync.2.html), accessed
2026-10-16. No third-party dependency is added.
