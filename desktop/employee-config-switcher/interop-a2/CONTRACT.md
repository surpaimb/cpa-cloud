# A2: duplicate-item rejection, synthetic volatile collection only

Independent slice on PR81 `bbf7ab82124d168f3f32fb2192429a9d6992b276`,
not based on PR83/B0. Draft; production **NO-GO**. Sources and licenses:
[PROVENANCE.md](PROVENANCE.md). Isolation is the unchanged
[no-key supervisor](../preflight/CONTRACT.md) and
[volatile contract](../interop/CONTRACT.md). No official clients or real sessions.

## Required sequence

1. Exact pinned Linux VM/private bus/dedicated UID/synthetic HOME and XDG,
   loopback-only network namespace, IP denial, host-home/socket boundaries and
   owned-process identity checks must pass before any synthetic Key operation.
2. CPA creates one item. Independent libsecret verifies the unique unlocked
   item, its seven attributes, item path, original binary envelope and Key.
   A different CPA process reopens the original Key; libsecret verifies again.
3. Independent helper constructs a second identity-consistent envelope, changing
   only item-id and synthetic Key within the same base query identity. It creates
   private, exclusive, no-follow, fd-validated pre-send samples of the second Key
   and envelope, fsyncs both and their directory **before** encoding/sending.
   No sample may be recreated or obtained from a response. It invokes exactly
   one raw `CreateItem(replace=false)` through libsecret's session encoding;
   item must be a distinct session path and returned prompt must be `/`.
4. Independent libsecret query must prove **exactly two unlocked real matches**,
   zero locked matches, both expected distinct paths and all seven attributes,
   original bytes, Key and envelope checksum. Injection failure is a failure,
   never evidence of rejection. No retry, skip or policy relaxation.
5. Fresh CPA process `open(original reference)` must be exactly `Corrupt`.
   Independent verification repeats the same two-item checks immediately after.
6. Another fresh CPA process `create(same transaction)` must be exactly
   `Unavailable`, with zero attempts reaching the test-only pre-send hook.
   This rules out an exclusive-oracle-write failure masquerading as rejection.
   Independent verification again requires the same two items unchanged; no
   third item, replacement or substitute Key. Counter is not a general RNG audit.
7. On success or failure stop only owned children, capture their private output,
   scan all available actual pre-send Key/envelope samples in raw/hex/base64/
   escaped/decimal forms, and perform supervisor cleanup. Any missing sample,
   scan/identity/cleanup failure is nonzero; only fixed whitelisted labels leave
   the unit. Success requires all checkpoints, scan and final outer cleanup.

No Delete, SetSecret, Prompt, Unlock, collection provisioning, production
provider/apply/UI/key-deletion changes, bus-policy widening, secret diagnostic
reads or manual rerun/dispatch. The two items disappear only with the owned
volatile service/fixture teardown; this is **not** exact item-deletion acceptance.
Private temporary samples are test-only and removed by the unchanged supervisor.

## Evidence boundaries

Portable tests check orchestration, scanner and source guards. Linux-only helper
self-tests exercise independent envelope predicates and private-file guards with
public dummy bytes, no bus/service/secret generation. Dedicated natural PR CI
checks out/asserts the exact PR head and runs the real fixture separately.
No local Windows claim of Linux runtime verification; ordinary CI and original
volatile fixture results remain separate. No persistent Key recovery, B1,
restart/reboot durability, production custody or arbitrary hostile concurrent
service behavior is established. No Ready, merge, package or deploy.
