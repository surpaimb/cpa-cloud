# Secret Service v1: exact read MIME compatibility

Development-preview provider-only increment, based on PR80 exact head
`b3a4caeb7ee157c0cddcd6b814da0837c5563071`, not the red PR81 fixture branch.
No production approval constructor, apply/UI wiring, deletion implementation,
official client, real session, password or new dependency is introduced.

## Contract

- GetSecret accepts only the exact strings `application/octet-stream` and
  `text/plain`. Empty, unknown, case changes, leading/trailing whitespace,
  charset parameters, embedded NUL and combined types are rejected. There is
  no brand/version exception or MIME normalization.
- Writes remain canonical `application/octet-stream`; no replacement or write
  behavior changes. Both read types carry the same opaque binary bytes, with
  no Unicode decoding, text conversion or NUL truncation.
- The returned session must match and transfer parameters must be empty.
  Metadata compatibility grants no trust to the payload: the existing exact
  148-byte envelope, magic, checksum, transaction, UID, lock namespace and item
  identity checks remain mandatory. Existing exact attribute-set, reference
  provider/version/length/magic/checksum/identity/attribute-digest checks remain.
- A successfully decoded returned value is immediately owned by
  `Zeroizing<Vec<u8>>`, before metadata checks. Ownership remains guarded through
  the private backend return and envelope validation. Metadata rejection and
  subsequent rejection drop the guard and invoke the existing zeroize library;
  there is no additional secret clone or logging in the production read path.
- This is not a claim that D-Bus library buffers, partial decode allocations,
  remote service memory, OS copies or the plain session transport are erased or
  encrypted. No unsafe read-after-free test is used to claim memory sanitization.
- The SHA-256 checksum is **not a MAC** and does not authenticate a malicious
  same-account service. A recomputed checksum must not bypass identity checks.
  Existing same-login trust limitations and production-custody gates remain.

## Independently authored test boundaries

Pure transport-helper tests exercise both exact accepted types, arbitrary binary
bytes including NUL/non-UTF-8, wrong sessions/nonempty parameters and rejected
MIME variants. Both allowed types are composed with the real envelope decoder
for exact length, magic, checksum and independently changed transaction/UID/
namespace/item fields with recomputed checksums.

In-memory provider tests exercise both types through the shared production
transport validator and original provider open path: canonical write assertion,
same-Key reopen, unknown MIME rejection, every missing/wrong attribute, extra
attribute, wrong expected item ID, corrupted/recomputed reference fields,
provider/version and reference-length rejection. No case invokes a real bus.
Existing fake-backend/lock tests remain separate evidence, including deletion
regressions; this increment does not change deletion code or claim native delete
acceptance. Rust tests require Linux CI on this Windows editing host.

This change does not import, modify or restack PR81's fixture. PR81 head
`1304448c3863d5598552d2751e47fc108421522a` retains its failed
[diagnostic run](https://github.com/surpaimb/cpa-cloud/actions/runs/38921390424):
at the original GetSecret rejection point it classified matching session, empty
parameters, `text/plain`, length 148 and valid envelope. Pre-send-oracle secret
scanning and owned-process stopping passed, but interoperability did not. Those
observations motivate this bounded change, not acceptance of this new head.
Real interoperability, persistence, concurrency and full cleanup integration
need separately authorized fixture work. Keep Draft pending exact-head ordinary
CI and original E/F review; no Ready/merge/package/deploy or manual dispatch.

## Sources and licenses

Independently authored from this contract, CPA's own v1 format and public APIs.
No archived CPA/CLIProxyAPI/Sub2API implementation or tests are used.

- [GNOME Keyring 46.1 secret serialization](https://github.com/GNOME/gnome-keyring/blob/46.1/daemon/dbus/gkd-secret-secret.c),
  inspected 2026-10-16 only to confirm the fixed `text/plain` response behavior;
  LGPL-2.1-or-later source is not copied, translated or ported.
- Existing Secret Service/dbus source references and dependency licenses remain
  in [PROVENANCE.md](PROVENANCE.md) and
  [DEPENDENCY-LICENSES.md](DEPENDENCY-LICENSES.md). The existing locked zeroize
  dependency provides the ownership guard; no dependency or lockfile change.
