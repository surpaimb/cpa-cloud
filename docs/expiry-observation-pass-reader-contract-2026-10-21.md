# Unattributed expiry observation pass reader contract

Status: independent bounded implementation, 2026-10-21. Base/sole parent is
[PR110](https://github.com/surpaimb/cpa-cloud/pull/110), exact head
`18b00683a3572278b071d1820821a9c00216810d`, whose base is PR109
`dc776189f78ba90c75db8fddfba6d007d527a06a`. Keep Draft / product, real migration
and production NO-GO. This closes only Gate1's missing cross-package read API
sub-gap, not trusted Gate1 or real App/Conn/pass/native Tx correspondence.
Read with the frozen [binding contract](employee-self-logout-red-sample-tx-pass-binding-contract-2026-10-21.md)
and [Gate4 installation contract](employee-self-logout-prequery-cut-installation-contract-2026-10-21.md).

## Four added files only

1. `docs/expiry-observation-pass-reader-contract-2026-10-21.md`.
2. `internal/financial/expiry_observation_pass_reader_20261021.go`.
3. `internal/financial/expiry_observation_pass_reader_20261021_test.go`.
4. `internal/financial/passreadercheck/reader_20261021_test.go`.

Every inherited file remains byte/blob-identical: notably subscription_period,
all old tests/contracts, service/App/store/worker/driver/collector, dependencies
and workflows. No caller is added to any old business path. No new dependency,
DB/SQL, App, worker, driver, identity admission/registration or opener wiring.

## Frozen public interface

The regular (not `_test.go`) financial file defines:

- `type ExpiryPassReadState uint8` with exactly five named constants in this
  order: `ExpiryPassNoContext` (zero), `ExpiryPassAbsent`, `ExpiryPassInvalid`,
  `ExpiryPassInRangeUnattributed`, `ExpiryPassOverLimit`.
- `type ExpiryPassRead struct { State ExpiryPassReadState; RawPass uint16 }`.
- `func ReadExpiryObservationPass(ctx context.Context) ExpiryPassRead`.

No error/string/function/context/handle/pointer/address/observer is returned.
The scalar copy is metadata, not a token, authorization, receipt or identity.
Zero value cannot mean InRange. InRangeUnattributed explicitly does not mean
trusted, actual expiry execution, particular App, Conn, transaction or issuer.

## Exact state and precedence

| Input | State | RawPass |
| --- | --- | --- |
| nil context interface | NoContext | 0 |
| non-nil context, Value for the private key is nil | Absent | 0 |
| wrong dynamic value type, including typed-nil pointer | Invalid | 0 |
| concrete binding with nil observer or pass0 | Invalid | 0 |
| concrete binding, non-nil observer, pass1..128 | InRangeUnattributed | exact original value |
| concrete binding, non-nil observer, pass129..65535 | OverLimit | exact original value |

NoContext is the explicit nil-interface case; Invalid describes malformed
bindings in a non-nil context. The existing typed-nil binding negative is not
a promise to recognize arbitrary typed-nil implementations of context.Context.

For non-nil ctx, perform exactly one `ctx.Value(expiryObservationKey{})` and one
concrete binding type assertion. Invalid-shape checks precede range checks;
nil-observer/pass129 is Invalid/0, not OverLimit. Do not search an outer parent
after an invalid or over-limit inner value. Absent is not an invented pass0.

129 and 65535 must remain OverLimit/129 and OverLimit/65535. RawPass must never
be clipped to128, wrapped, remapped to0, reused or treated as an admitted ID.
No accepted-ID field/API is introduced. This batch adds no consumer or ID
admission; any future consumer must independently reject every non-InRange
state before considering any separately approved identity bridge.

The fixed worker saturates its uint16 counter at65535, rather than wrapping.
The reader cannot tell those repeated maxima apart, repair producer uniqueness
or recover information already lost before the typed writer was called. All
values above128 remain unadmitted. This limitation is not changed here.

## Strictly passive behavior

Only inspect the concrete binding's observer for nil and its copied uint16.
Never invoke, compare identity of, fingerprint or expose the observer. Do not
call Err/Done/Deadline/Cause, expiryScopeFor, SQL, reflection, a clock/timer,
probe or logging. Do not create a new context, cancellation, deadline, goroutine,
registry or global mutable state. Do not alter the old writer or its behavior.

Cancellation does not change stored metadata; an InRange result on cancelled
ctx is not permission to execute anything. Caller-supplied Value implementations
must obey context.Context; this reader delegates that one lookup, does not
recover arbitrary panics, and does not promise that hostile custom Value methods
are side-effect-free. No reflection/unsafe workaround for typed-nil contexts.
Bounded means finite scalar output and one delegated lookup, not a latency or
parent-chain-depth bound on context.Value. No new traversal/probe loop is added.

The existing nil-observer writer returns its input unchanged. On an already
bound outer ctx it therefore preserves the older binding, not erases it.
Nested malformed/zero/over-limit bindings shadow the outer one and remain
Invalid/OverLimit; no convenient fallback to a valid outer number is allowed.

## Tests-first registration and acceptance

Contract and complete new controls precede semantic implementation. First add
only the frozen public declarations and a temporary zero-result scaffold;
compile and run exactly `TestExpiryPassReader20261021TestsFirstOverLimit`.
Both129 and65535 must fail with fixed `EPR_TESTS_FIRST_OVERLIMIT_RAW`. Compiler
errors or other labels are NOT the expected RED. Never commit that scaffold.
Only after this expected gate replace the body with the specified reader.

New pure controls must cover nil/Absent, wrong-type and typed-nil bindings,
nil observer, pass0, 1/128/129/65535, immutable/repeated reads, one Value lookup
and no other context method, observer-called-is-failure, pre/post cancellation,
unrelated Value preservation, nesting with invalid/over-limit inner shadowing,
and nil-observer writer preserving the outer context/binding.

Two separate public-writer context chains with different observers and the
same pass must produce indistinguishable results. This is an explicit lack-of-
provenance witness; no actual or simulated App identity is invented and no App
is instantiated. Equality does not demonstrate cross-App isolation.

AST controls of the actual new regular file require context as its only import,
the exact scalar result shape, five constants, one reader function/no init,
no mutable globals, and exactly one call: the private-key Value lookup. That
control does not itself prove the implementation correct; value controls do.

The passreadercheck test lives in a distinct import path and uses only formal
`cpacloud.local/server/internal/financial` exports. Compile it separately with
`go test -c` into temporary artifact storage. Financial is then an ordinary
dependency, not the financial package-under-test augmented with private test
helpers. Also execute its new named pure controls; no DB/App/worker/SQL calls.
Inherited dependency initialization is not new driver integration evidence.

Use Windows Go1.26.9, serial `-p=1 -parallel=1 -count=1`. Run only the new named
pure controls plus existing `TestExpirySQLObservationPure20261021` (its pure
classification/context controls). Targeted vet/build, gofmt, relative links,
four-added-file/no-old-diff checks complete local acceptance. Do not run the
Windows SQLite whole repository or retry historical red cases. No local race
or real identity/security-completeness claim follows from these controls.

## Stop gates and history

The first unexpected local or natural CI red freezes the batch; retain original
output and fixed labels, do not hot-patch, retry, dispatch or rerun workflows.
Stop before submission if any old file/writer/worker/business path must change,
if four files cannot suffice, if App/Conn/Tx admission/DB/driver wiring or a
simulated identity claim is needed, or if exact raw-value semantics conflict.
Only a full local pass permits one commit/push and a new stacked Draft PR.
Root re-pins its actual head/base/files/natural CI and arranges original E/F
formal same-head independent read-only review; no GO is issued by this author.

[PR109](https://github.com/surpaimb/cpa-cloud/pull/109) remains docs-only narrow
GO and [PR110](https://github.com/surpaimb/cpa-cloud/pull/110) only Gate4 empty-H0
installation-control source narrow GO. Its preceding local
SPQI_INVALID_INSTALLATION red/output remain frozen.
[PR108 Code509](https://github.com/surpaimb/cpa-cloud/actions/runs/39582468254),
[PR106 Code508](https://github.com/surpaimb/cpa-cloud/actions/runs/39562535012),
[PR100 Code501](https://github.com/surpaimb/cpa-cloud/actions/runs/39500154209)
and PR102-105 original reds remain unresolved; E's original PR108 P3 is retained.
No new green washes those red. Gate2/3 and the remaining trusted Gate1 bridge,
driver/putConn/lock owner/SQLite BUSY cause, product and production remain NO-GO.
No Ready/merge/tag/package/deploy, old PR/monitor/main-WIP/real credential-data
operation is authorized.

## Author local evidence, 2026-10-21

- The temporary public-API zero-result scaffold compiled. Its129 and65535
  controls both produced only the pre-registered
  `EPR_TESTS_FIRST_OVERLIMIT_RAW` RED; JSON parse errors0. Saved JSONL SHA256
  `a8d04880e2e6178e6df39f681c31899aff2f02b31e584cfa4db9a808f7e8f0e7`.
  This is a new API scaffold witness, not an old business-path regression.
- After implementation, financial's six new pure parents/25 children and the
  existing observation pure parent/five children all passed:37 named run/pass
  pairs,0fail/skip. Saved JSONL SHA256
  `0579c32909e869e895301ed1856603f30ee13ee8fd65eb3a2052fda63c0ea435`.
- Distinct-path external package: standalone `go test -c` succeeded; dependency
  metadata contained ordinary `internal/financial` with empty ForTest, no
  financial test-augmented package. Its new parent/seven children then passed:
  8named run/pass pairs,0fail/skip. Saved JSONL SHA256
  `87292c758d397e83a9077452d8c80d18fc7f53835761ab90d011f48086e9cdf7`.
- Targeted financial/external-package vet, regular financial build, gofmt and
  two relative contract links passed. No inherited tracked-file diff was
  present; staged acceptance must still enforce exactly the four added files.

These are Windows Go1.26.9 serial non-cached targeted controls, not whole-suite,
race, actual App/driver/DB/SQL/worker execution, trusted scope or production
evidence. No old red was rerun/reclassified and no formal new-head E/F GO is
claimed. Natural CI must be reported on the new head separately.

## Provenance

New interface/implementation/controls independently authored from this contract,
the project's fixed private binding/writer and root's bounded permission;
no archived CPA/CLIProxyAPI/Sub2API code/tests, no strict clean-room claim.
Go context/AST semantics follow fixed
[Go1.26.9 context](https://github.com/golang/go/blob/go1.26.9/src/context/context.go)
and [Go1.26.9 go/ast](https://github.com/golang/go/tree/go1.26.9/src/go/ast),
reviewed2026-10-21, BSD-style license. No new third-party library; inherited
modernc sqlite retains BSD-3-Clause and is neither copied nor changed.
