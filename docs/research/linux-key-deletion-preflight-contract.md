# Linux synthetic key-deletion preflight contract

Status (2026-10-16): development-preview implementation contract. This is a
narrow stacked extension of the
[Linux synthetic empty-workspace removal receipt contract](linux-workspace-removal-receipt-contract.md).
Its fixed base is PR 75 head `aed2aa41a6ff562594b328d2533ecc8261a403a0`.

The implementation remains limited to owner-private Linux synthetic roots
created by independently authored tests. It is not connected to a real home
directory, official client, Secret Service session, production credential
store, UI write, package, deployment, or port 8787.

## Durable precondition evidence

The `WorkspaceRemovalDurable` cleanup event records the exact Linux
device/inode identities of the replacement directory, removal-proof
directory, final removal-proof file, and retained empty workspace. The event
also binds the synthetic root, terminal outcome, transaction, recovery-key
reference, canonical workspace name, and original workspace device/inode.
Events before `WorkspaceRemovalDurable` must not contain the snapshot; that
phase and every later phase must contain it. The cleanup-journal format is
incremented so an older event without this evidence cannot authorize a later
provider deletion.

Before any provider `delete` call, the durable wrapper reopens the root and
all removal-proof objects relative to held directory file descriptors with
`O_NOFOLLOW`. It requires the effective user to own each object, exact
`0700` directory and `0600` file modes, single-link regular proof files, an
empty retained directory with link count two, and the recorded device/inode
snapshot. The original workspace name must remain absent under a no-follow
`fstatat` check.

Every opened root, directory, and proof file must have neither
`system.posix_acl_access` nor `system.posix_acl_default`. The check uses
`fgetxattr` on the already validated file descriptor. `ENODATA` is the only
accepted absence result; an ACL, an unsupported xattr check, or any other
error fails closed. This deliberately refuses deployment filesystems on which
the process cannot prove the ACL condition.

The existing workspace-removal proof and cleanup event are each published by
`renameat2(RENAME_NOREPLACE)` only after file flush, followed by directory and
synthetic-root `fsync`. A fresh-process test exits without unwinding at every
workspace-removal rename/flush checkpoint, then reloads and must converge to
the same exact receipt. In-process fault injection remains a separate
deterministic test.

## Provider boundary

Identity replacement with byte-identical proof contents, a proof hard link,
an unexpected ACL, wrong owner/mode, a reintroduced workspace, or any other
metadata mismatch must fail before the provider method is invoked. Synthetic
providers count calls to prove `delete=0` on these failures.

This slice does not call a real Linux Secret Service, approve a production
provider, or enable production apply or UI mutation. A later slice still
needs a fixed isolated native Secret Service fixture and a separate review
before real key deletion can be enabled.

## Public sources and licenses

Implementation behavior is based on Linux man-pages 6.19, accessed
2026-10-16:

- [`fgetxattr(2)`](https://man7.org/linux/man-pages/man2/fgetxattr.2.html)
- [`xattr(7)`](https://man7.org/linux/man-pages/man7/xattr.7.html)
- [`acl(5)`](https://man7.org/linux/man-pages/man5/acl.5.html)
- [`renameat2(2)`](https://man7.org/linux/man-pages/man2/rename.2.html)
- [`fsync(2)`](https://man7.org/linux/man-pages/man2/fsync.2.html)

No third-party dependency or copied implementation is introduced. The code
uses the repository's already pinned `libc` crate under its existing license.
