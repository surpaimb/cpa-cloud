# B1a-C independently owned channel boundary

This substantive preflight contract addition intentionally matches the existing
seven regression path filters without editing them. It does not change the
shared supervisor's stdin=DEVNULL, hooks, metadata, policy, pins or runtime.
The separate [C contract](../../../tests/secret-service-b1a-channel/CONTRACT.md)
and [provenance](../../../tests/secret-service-b1a-channel/PROVENANCE.md) govern
the new, test-only standalone launcher and reusable `OneShot.send` path.

Source is PR88 head `598845e71b54cfc6c8597d7493d2e061d339572c`, stacked on PR87
`776d9652d0c4a85a58350befbfda0351f3262e22`. PR88's seven natural attempt1 green
runs are historical baseline, not this new head's acceptance. Its original
E/F narrow GO does not grant this new sender or launcher a GO.

C is outer-memory synthetic bytes -> an explicitly passed private anonymous
pipe -> the exact bound synthetic receiver's stdin -> complete bytes/EOF ->
wait/close and complete evidence. All target binding, deadlines, one-use,
failure closure and private receipt handling belong to new C files. No global
stdin/metadata monkey-patch and no broadening of old fixture hooks is allowed.
No password in argv/env/metadata/files/logs/artifacts; no shell echo or implicit
newline. Neither a receipt nor a password digest is published on success/error.

The new launcher must prove fresh UID/private bus/HOME/XDG/runtime and owned
unit/cgroup/FD/process isolation in a disposable Linux VM. Outer retains its
oracle until all writers/children stop, all registered streams/current-unit
journal/owned files are captured completely, scans finish and owned resources
are removed. Content-clear is separate from capture-complete; overflow, missing
capture or cleanup failure cannot be waived. First operation failure survives
cleanup failure. Public negatives keep target and neighbor unchanged.

Only C's natural exact-head attempt1 plus the seven unchanged natural regressions
count. New Code, Employee, no-key, B0, volatile, A2 and A3 results must each be
recorded with exact head/base/event/attempt. Skipped is not execution and
Employee default merge checkout is not raw-head daemon proof. The first
unexpected dedicated red freezes source for fixed-label read-only diagnosis,
not a patch or workflow rerun. Original visible E/F review follows green gates.

Evidence groups remain distinct: B0 is **no-secret** directory/daemon
replacement; C is **synthetic-password to a synthetic receiver**; A2/A3 are
**synthetic-secret** refusal/delete/recovery fixtures. PR87 #1-#7 red history and
#8 green, PR88 source green and all limitations must remain in the new PR body.

P is excluded: no GNOME, collection provisioning/unlock, item, persistent Key,
provider or true configuration. Actual GNOME 46.1 password acceptance and
persistent reopen need a later independent contract. Draft / production NO-GO;
no Ready, merge, tag, package or deployment.
