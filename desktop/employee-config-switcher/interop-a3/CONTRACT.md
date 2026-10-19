# A3: exact volatile Item.Delete and durable cleanup

Independent bounded slice on PR84 `28154478429937117f938930fd564d1a85523417`,
base PR81 `bbf7ab82124d168f3f32fb2192429a9d6992b276`. PR83/B0 is a parallel
branch, not integrated. Draft / production **NO-GO**. Original F's read-only
preflight contract (2026-10-19) is the gate, not an implementation approval.

Reuse the unchanged no-key supervisor isolation, exact components, private
non-root UID/bus/HOME/XDG/netns, daemon identity checks and owned cleanup.
Old A/A2 bus policies remain byte-for-byte unchanged. A3 has a separate policy:
only the isolated service's Item.Delete is added; interface-less Delete,
Collection.Delete, Prompt/Unlock/Dismiss, SetSecret, attribute writes and
collection provisioning remain denied. No real configuration/Key/client,
UI/apply, B1, persistent service recovery, daemon reboot, power-loss or custody
claim. No general permission/approval constructor is enabled.

Use the actual LinuxSecretServiceRecoveryKeyProvider -> DbusBackend ->
Item.Delete. Keep the six-attribute uniqueness query, seven attributes, exact
reference/envelope binding, no replacement and no prompt execution. Independent
libsecret snapshots the actual target and a different-transaction neighbor,
then verifies exact target absence and unchanged neighbor path/attributes/
envelope/Key while the same collection, bus and keyring are still alive.
Repeated deletion in a fresh CPA process must return Missing with zero Delete
sends. Wrong identity/corrupt reference and an independently proved duplicate
pair must remain unchanged; duplicates return Corrupt with zero Delete sends.
Retain A2 open=Corrupt/create=Unavailable and zero pre-send rejection evidence.

The durable test must consume real store-issued capabilities:

`durable Complete/RolledBack -> TerminalRecorded -> CompletionTombstoneDurable
-> exact content retirement -> WorkspaceRemovalDurable -> provider.delete
-> KeyDeletionConfirmed -> inactive tombstone proof -> cleanup Complete`.

No handcrafted phase/handoff/receipt is acceptance. Early deletes, including
after content retirement but before durable workspace removal, must fail before
the backend send. Every delete revalidates actual workspace-removal evidence.
Pending/gapped journals, replaced/hardlinked proofs, a reappearing workspace
and cross-root/reference receipts must block and preserve the synthetic state.

Crash tests use fixed test-only handshakes and SIGKILL of the exact owned CPA
child (not drop/reload or sleep guesses). A single private bus/keyring/neighbor
survives the whole case. Cover each durable phase, content-retired before
removal, actual delete success before its confirmation append, durable deletion
confirmation, the tombstone deleting intent, inactive proof before Complete,
and completed-state re-entry. After unconfirmed actual deletion, a new process
must reload WorkspaceRemovalDurable and confirm Missing using the original
reference, with no extra backend Delete. No provider.create during replay.

All generated recovery Keys/envelopes and synthetic old/new employee values
are privately captured and flushed before send/write. All planned samples and
daemon/child/unit output are registered before use. Available evidence is scanned
even on failure; content-clear and complete capture are independent. Only fixed
source-owned labels leave CI. No raw output, secret digest or oracle is uploaded.
Stop owned processes/handles, verify inactive unit and empty/absent owned cgroup,
then scan the exact unit journal and remove only validated owned resources.

Static/source guards, fake-backend failures, actual daemon checks and actual
process crashes are separate evidence. The first natural dedicated failure
stops further cases after safe cleanup: no patch/rerun/dispatch/runner switch.
Fresh exact head/base CI and original E/F review are required. No Ready/merge,
tag/package/deploy, proof GC, capacity reclamation or transaction-ID reuse.

The dedicated matrix is 27 sequential fresh isolated fixtures: exact deletion,
duplicate refusal, nine actual kill/restart boundaries for each of Complete and
RolledBack (18 crashes), five evidence mutations and two cross-bound receipt
refusals. Each crash fixture keeps its own service alive throughout replay;
different cases do not reuse a bus or root. Backend send attempts are observed
at the real DbusBackend call site; success additionally requires independent
libsecret absence and unchanged neighbor checks. Invalid-header bus denial
probes use only a nonexistent fixture path before Key operations, and must be
rejected by the bus without dispatching the forbidden service method.

The no-key supervisor gains only optional A3 evidence hooks. Its no-argument
entry point and old fixtures do not use them. The A3 cgroup-empty check is part
of the unit-stopped cleanup gate, before user/root removal. A journal cursor is
recorded before launching the unit; only that unit's records after this cursor
are scanned after stop alongside the complete systemd-run pipe and all private
daemon/child logs. Missing/truncated/oversized capture is incomplete, not clear
acceptance. Fixed native failure labels classify existing return codes/panics
without further reads or diagnostic service calls.
Every preregistered log also requires a private exclusive durable completion
marker. Child markers are issued only after drained EOF, process wait and
successful pipe/file closes; daemon markers require owned stop and close.
A timeout or merely existing partial log cannot obtain capture acceptance.

Sources and licenses: [PROVENANCE.md](PROVENANCE.md). Tested results remain
pending until exact-head natural CI; local Windows checks are not Linux runtime.

## Separately bounded fresh-store initialization

Only A3's new `transaction` and `other-transaction` stores may be initialized,
before targets, samples, secrets, journals or the unit exist. Exclusive mkdir,
held nofollow parent/child FDs, name/FD identity, 0700 ownership/link and empty
checks must precede the new object's fd ownership handoff and ACL operations.
Only that held new-directory FD may lose access/default POSIX ACL attributes;
only ENODATA means already absent. Recheck the same object's mode/owner/links,
identity/name binding, both attributes' absence and emptiness before success.
No existing/nonempty/rebound object is adopted, no ancestor/shared helper or
product predicate changes, no chmod repair, no hot cleanup-journal ACL repair
or retry. Failure and close errors stay red; existing owned cleanup applies.
Public fresh dummy ACL/known inheritance and injected-error tests are mechanism
and rejection evidence only, never an explanation of historical A3 red runs.
All old red records remain; one new natural attempt and fresh E/F review gate
any later acceptance. Production remains NO-GO.
