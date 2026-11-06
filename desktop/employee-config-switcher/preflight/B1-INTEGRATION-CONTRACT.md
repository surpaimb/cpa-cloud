# B1-I: frozen B0 + A3 combination baseline

2026-10-19. Independent integration slice only; Draft / production **NO-GO**.
This contract does not authorize B1 password input, collection provisioning or
persistent Recovery Key/provider implementation.

## Immutable sources and seven-file boundary

The sole parent is PR87 head `776d9652d0c4a85a58350befbfda0351f3262e22`
(itself stacked on PR84 `28154478429937117f938930fd564d1a85523417`).
The new Draft PR targets its branch `codex/secret-service-delete-a3`, with that
exact parent rechecked before publication and acceptance. No PR83/87 branch is
changed, rebased, merged, readied or closed by this slice.

The sole import source is parallel PR83 head
`fc81360d35e48f663978753e482c3cfb6746dd4e`, based on PR81
`bbf7ab82124d168f3f32fb2192429a9d6992b276`. Import these six project-authored
files verbatim; the Git blob object IDs (not SHA-256 claims) must match:

| Repository-relative path | Required Git blob |
| --- | --- |
| `.github/workflows/secret-service-persistent-b0.yml` | `1ef8d9cf6dd99584ba1d14716fe09905b35d0dc5` |
| `tests/secret-service-persistent-b0/CONTRACT.md` | `e4a652ecc2e22a571b7bf1b26badfae543bc8da4` |
| `tests/secret-service-persistent-b0/PROVENANCE.md` | `38cd444c0c68419d0ef99caba45d0b85bebbc3c7` |
| `tests/secret-service-persistent-b0/fixture.py` | `0704e03b63edf8a470765dd952ad5ef27ad16543` |
| `tests/secret-service-persistent-b0/owner.c` | `22ea1e65fb8cb21c956a4bdaa46d1dd19978315d` |
| `tests/secret-service-persistent-b0/test_fixture.py` | `b07b85095d104324c73090b884ff642de18549f9` |

This independently authored contract is the seventh and only other new file.
No existing source, test, policy, component pin or workflow is edited. Record
the resulting real combination SHA in the PR body after commit; do not invent
a self-referential SHA or treat either source SHA as the combined result.
Neither main-tree WIP/untracked files nor archived/reference implementations
are import sources. This is not a strict clean-room claim.

## Actual supervisor baseline

The combination uses PR87's [run.py](run.py), Git blob
`50ee84a8361515a1fdeb99068e5a03e11af24fd4`, not PR81's supervisor blob
`d50f9eebc06d4ecec440697ada046b15b9b499cc`. PR87's optional A3 evidence and
cleanup callbacks remain unchanged. B0 installs none of those callbacks.
Compatibility is a new-head dynamic gate, not an assumption from old B0 green.
The imported B0 documents retain their historical PR81 wording verbatim;
that wording is not a claim that this combination runs the old supervisor.
Existing [component pins](components.json), [no-key policy](bus.conf.in) and
[metadata observer](libsecret-observer.c) remain authoritative and unchanged.

## Separate evidence groups and natural triggers

The imported [B0 workflow](../../../.github/workflows/secret-service-persistent-b0.yml)
matches its own files and `desktop/employee-config-switcher/preflight/**`.
This substantive contract also matches the existing no-key preflight filter
and the broad `desktop/employee-config-switcher/**` filters of volatile, A2,
A3 and Employee validation. The non-Markdown B0 additions trigger normal Code.
No added workflow, filter edit, dependency graph, rerun or dispatch is needed.
Existing workflows run independently; jobs do not share bus, keyring, directory,
secret samples or recovery state. Do not claim a new B0-before-A3 ordering.

| Evidence group | What can pass here | What it cannot establish |
| --- | --- | --- |
| B0, separately no-secret | Public directory/sentinel, two owned daemon instances on one private bus, distinct PID/owner, guarded removal and outer cleanup | Password channel, persistent collection/item/Key or Key recovery after daemon restart |
| No-key preflight | Existing metadata/isolation and owned cleanup gates | Secret-bearing provider acceptance |
| Volatile / A2 / A3 | Existing isolated synthetic-secret gates; A3 retains 27 fresh fixtures, 18 actual SIGKILL/restart gates, five mutations and two cross-bound refusals | Persistent service reboot/recovery; A3 keeps its service alive across CPA crashes |

The combined round is **not wholly no-key/no-secret**. B0 still forbids password,
collection/item, control-protocol, Key, unlock/prompt or provisioning operations.
Its public sentinel is not a keyring file. B0's raw daemon/owner output stays
captured, with only its fixed markers and guarded cleanup accepted; no secret
content scan is invented for that no-secret job. Synthetic-secret jobs retain
their own content-clear AND completeness, registered logs/completions and
unit/cgroup/resource cleanup gates. Neither group's green substitutes for the
other or composes into persistent Key recovery evidence.

B0, preflight, volatile, A2 and A3 dedicated jobs must check out and assert the
raw PR head. Code's executed jobs retain their existing explicit-head checks.
Employee uses default PR checkout: record its merge-checkout limitation and
never label it raw-head daemon acceptance. List actual executed, skipped,
failed/cancelled and pending checks separately; skipped is not execution.

## Historical evidence stays historical

The seven earlier A3 natural reds remain preserved in PR87 and in the new PR
body, each tied to its own head/run; no later classification rewrites an older
run's cause. The source A3 #8 green is not combination-head acceptance.

| A3 history | Exact head | Natural run |
| --- | --- | --- |
| #1 red | `ce680eb6821d7b1d448f5095c70b25a0e09cd2e8` | `39214362852` |
| #2 red | `865270fa388cadfac3008d530818259fc7887adf` | `39220758904` |
| #3 red | `481add5d1a8c2e285bf364f620b6209f5be1503d` | `39225014449` |
| #4 red | `267d7bb337150f68e5c97cf470b7a4b2672811b4` | `39229081540` |
| #5 red | `4ff300da99265f2b627264a0096b20ef7000d991` | `39233270654` |
| #6 red | `6bcb2e7bf4549bf9b7613a69d5b9bc99f5f4d41f` | `39237411305` |
| #7 red | `0541492b45f08610b54039b3f15824c7ae5bdeca` | `39241554427` |
| #8 green | `776d9652d0c4a85a58350befbfda0351f3262e22` | `39244940158` |

Source Code #478/run39244940174 passed on the A3 #8 head. Source B0
run38943073231/job117115294974 and Code #458/run38943073089 passed on the exact
PR83 head above. B0's eight markers plus outer cleanup, public filesystem tests
and source E/F narrow reviews remain limited to their original scopes. Keep
these source histories and new-head results separate in the new PR body.

## Stop rule and later B1 gates

One bounded commit/push and one new Draft stacked PR. Freeze the actual combined
head/base and record natural PR attempt 1 results for B0, A2, A3, volatile,
no-key, Employee and Code, including scans/completeness and owned cleanup.
On the first dedicated red, retain the result, allow only safe owned cleanup and
read-only fixed-tag diagnosis, then stop: no patch, retry, dispatch, alternate
runner or acceptance borrowed from old heads. Do not cancel unrelated work.
Head/base drift expires the old evidence. Original visible E/F must formally
review the new exact head/base before any subsequent implementation slice.

Only after that independently bounded gate may B1a use the accepted combination
SHA for separately authorized protected nonempty password transport and native
persistent collection provisioning/restart tests. No password may enter argv,
environment, logs or artifacts; there must be a private owned pipe/fd, explicit
service target, bounded capture/deadline and cleanup, with no empty-password or
fallback path. Provider Locked remains fail-closed, not automatic unlocking.
Persistent Key/provider, crash/recovery/delete receipts and all other native
platform/tool/UI/bundle acceptance remain later separately authorized gates.
No real Key/config/session, new production constructor/switch, apply/UI, GC,
packaging, tag, Ready/merge or deploy is authorized by this contract.

## Provenance and licenses

The six imported files are this project's independently authored PR83 sources;
their [provenance](../../../tests/secret-service-persistent-b0/PROVENANCE.md) and
[B0 contract](../../../tests/secret-service-persistent-b0/CONTRACT.md) remain
verbatim. This new contract is independently authored from the coordinator's
bounded B1-I contract, original visible E/F comparisons, fixed Git objects and
the actual workflow filters. No third-party source/SDK, new dependency, protocol
implementation or license change is introduced; existing dependencies retain
their recorded licenses. Tested outcomes must be recorded separately from
future B1 plans. Production remains NO-GO even if all combination CI is green.
