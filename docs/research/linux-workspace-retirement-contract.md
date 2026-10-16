# Linux synthetic workspace content-retirement contract

Status (2026-10-16): development-preview implementation contract. This is a
narrow stacked extension of the
[durable terminal handoff contract](durable-terminal-handoff-contract.md). Its
fixed base is `d6173d8b9698251c4bc0b4ec1bedc8c4cb913457`.

The implementation is limited to owner-private Linux synthetic roots created
by the independently authored replacement test harness. It is not connected to
a real home directory, native client configuration, production credential
store, package, deployment or port 8787.

## Authorization and fixed objects

Only a cleanup sequence already in `CompletionTombstoneDurable` may derive the
opaque retirement authorization. The authorization repeats the exact terminal
outcome, transaction, recovery-key reference and the immutable retirement plan
captured by `DurableTerminalHandoff`.

The plan binds root, replacement parent, replacement workspace, backup parent
and backup workspace device/inode identities. Its only removable file names
are:

- replacement `journal.jsonl`, `.journal-pending-ready`, and the 24 fixed
  `.journal-pending-00` through `.journal-pending-23` slots; and
- backup `rollback.aes256gcm` and `rollback.json`, followed by their exact empty
  backup workspace directory.

The replacement transaction normalizes its fixed desired/rollback stage names
before issuing the handoff. A recovered terminal event may finish that same
bounded normalization only after the stage content, length, private mode,
owner and group match its journal-derived state. An unknown name, link, type,
identity, metadata value, digest, manifest commitment or external edit is
preserved and fails closed.

## Durable retirement protocol

Linux retirement holds the existing replacement target lock and a separate
retirement-namespace lock. All source opens, moves and removals are relative to
already validated directory descriptors and use no-follow or no-replace
operations.

Before mutating a source, the protocol durably publishes the exact immutable
plan beneath `.cpa-cloud-employee-config-workspace-retirements/<transaction>`.
For each fixed object it:

1. revalidates identity, type, single-link status, private metadata, length and
   SHA-256 content digest;
2. moves the object without replacement to its transaction-scoped quarantine;
3. flushes the source and quarantine directories;
4. durably publishes an exact plan-bound step marker;
5. removes only that verified quarantine name; and
6. flushes the quarantine directory again.

Plan, marker and completion publication uses an exact private pending file,
file flush, directory flush, no-replace rename and another directory flush.
Restart may finish an exact pending publication; conflicting final/pending
bytes or simultaneous names are preserved and rejected. Test-only injection
can interrupt after every removal and every directory flush. A fresh restart
must either resume the same plan to the single completed state or fail closed.

Completion requires the backup workspace to be absent and the original
replacement workspace descriptor to remain present and empty. The completion
record is retained; the replacement workspace directory is not removed.

## Stop boundary

This slice does not advance the cleanup phase beyond
`CompletionTombstoneDurable`. Its content-retired receipt is not the durable
workspace-removal receipt required by the later `WorkspaceRemoved` transition.
Consequently it cannot reach provider deletion. It also does not delete a
production key, remove a real directory, garbage-collect retirement evidence,
or enable a real client write.

Windows and macOS contain only pure state or compile coverage for this slice.
The separate GitHub-hosted Windows Claude native proposal remains blocked and
has `executed=false`.

## Required evidence

Automated Linux evidence covers exact retirement and idempotent restart,
unknown-name and edited-object preservation, every injected removal/directory
flush boundary, an empty retained workspace, absent backup workspace, unchanged
cleanup phase and zero provider deletions. Results from GitHub Actions must be
reported as tested behavior; workspace-directory removal, key deletion and real
client integration remain planned behavior only.

## Provenance and licenses

The implementation and tests are newly authored CPA code from this contract,
the repository's synthetic state machines and the public Linux
[`openat(2)`](https://man7.org/linux/man-pages/man2/openat.2.html),
[`renameat2(2)`](https://man7.org/linux/man-pages/man2/renameat2.2.html),
[`unlinkat(2)`](https://man7.org/linux/man-pages/man2/unlink.2.html),
[`flock(2)`](https://man7.org/linux/man-pages/man2/flock.2.html), and
[`fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html) contracts
(accessed 2026-10-16). No CLIProxyAPI, Sub2API, archived CPA or adjacent
reference implementation was used as an implementation template. No new
dependency, SDK, asset or license is introduced.
