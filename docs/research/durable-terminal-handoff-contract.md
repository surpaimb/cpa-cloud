# Durable terminal handoff contract

Status (2026-10-16): development-preview implementation contract. The fixed
stack base is `eb6023b5d3dd98ca4de9b48a258928ed95729214`. This slice is limited
to the transition from an already durable Linux synthetic replacement terminal
event through `CompletionTombstoneDurable`.

This contract extends the
[existing-file replacement contract](employee-config-replacement-transaction-contract.md)
and [employee config switcher security gates](employee-config-switcher-security-gates.md).
The product, independent-implementation, development and preview contracts
remain authoritative.

## Issuance boundary

Only `live/linux_replacement.rs` may issue `DurableTerminalHandoff`. Before
issuance it must:

1. require the in-memory and replayed replacement state to carry the same
   `Complete` or `RolledBack` outcome, transaction and complete recovery-key
   reference;
2. flush the replacement journal and replay its bounded bytes;
3. require the canonical workspace leaf `tx-<32 lowercase hex transaction>`;
4. revalidate the held root and workspace descriptors; and
5. bind replacement-journal version, outcome, transaction, root device/inode,
   workspace leaf and device/inode, and the complete recovery-key reference.

The handoff is crate-private and has no serialization implementation. Its
`Debug` representation must inherit recovery-reference redaction. Loose caller
parameters are not an alternative production constructor.

## Cleanup acceptance boundary

Cleanup-journal creation consumes the handoff as one capability. Every durable
cleanup event repeats the replacement-journal version and all existing root,
terminal, transaction, recovery-reference and workspace bindings. Restart must
reconstruct the same state, require the currently supported replacement-journal
version and derive the exact canonical workspace leaf from the transaction. It
must reject a changed prefix, legacy/generic or cross-transaction workspace,
unsupported version, duplicate initial journal, non-contiguous event or visible
pending event. Loose legacy workspace shapes are confined to `cfg(test)`
fixtures for the older downstream removal foundation.

The completion tombstone store returns a non-serializable durable receipt only
after the exact active record, its directory and the root have been flushed and
revalidated. An exact active duplicate may reproduce the same receipt after the
same durability repair; a conflicting terminal/reference or an existing
deleting/removed proof fails closed. Cleanup advances from `TerminalRecorded`
only when that receipt exactly matches terminal, transaction, complete
recovery-reference and root device/inode. A failed append restores the prior
in-memory phase for retry.

## Slice stop and safety invariants

The last permitted state in this slice is `CompletionTombstoneDurable`.

- The replacement workspace remains present and non-empty.
- No recovery-key provider deletion is called.
- No workspace-removal authorization is consumed.
- `apply_configuration` and every UI-backed real client write remain
  fail-closed.
- Linux tests use a mode-`0700` temporary synthetic root and fake provider.
  They do not access a real home directory, credential store, native client,
  key or port 8787.
- Windows and macOS exercise only pure state/compile paths; Linux filesystem
  behavior is explicitly unsupported there.
- The GitHub-hosted Windows Claude native isolation proposal is a separate
  blocked gate and is not executed by this slice.

## Required evidence

Automated evidence must cover successful and rolled-back handoff issuance,
restart reconstruction across the replacement/tombstone/cleanup phases,
cross-bound receipt refusal, exact duplicate conflicts, incomplete or failed
cleanup publication, redacted `Debug`, unchanged non-empty workspace and zero
provider deletes. Tested behavior must be reported separately from planned
workspace emptying, workspace deletion, key deletion, tombstone removal,
artifact retirement, production provider integration and real client writes.

## Provenance and licenses

The implementation and tests for this slice are newly authored CPA code from
this contract, the repository's existing synthetic-root state machines and the
public Linux filesystem contracts already recorded in
[`desktop/employee-config-switcher/PROVENANCE.md`](../../desktop/employee-config-switcher/PROVENANCE.md).
No CLIProxyAPI, Sub2API, archived CPA or adjacent reference implementation was
used as an implementation template. No dependency, SDK, asset or license is
introduced by this slice.
