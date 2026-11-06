# Backup CLI failure-diagnostic redaction

Date: 2026-10-21. Independent bounded implementation candidate; Draft /
production NO-GO. The frozen plan below is distinct from the executed results
recorded at the end; local results are not remote CI or independent review.

## Exact base, provenance and ownership

Start independently from frozen PR99 remote head
8280d3df2c59cd2280d39b25e788f05d39aab4ef, base PR98
d96e99ebef5942db1b585ff0042efc0872c67eca, complete base tree
658ac86aa8f494243271c06a4db70f104e35ef4c. New isolated worktree and
codex/backup-cli-failure-diagnostics branch; do not stack on PR100. PR100's
first Code #501 failure root cause remains unknown and its product narrow GO
has not been restored. PR100 source/body/evidence are not changed or used to
claim that failure was repaired; the existing PR99 CLI is inherited explicitly.

At most four files: this contract, cmd/cpa-cloud-backup/main.go,
cmd/cpa-cloud-backup/key_recovery.go and the new
cmd/cpa-cloud-backup/diagnostic_redaction_test.go. Freeze backup/recovery cores,
service/schema, dependencies, workflows and the systemd_v3 product/test files.
Do not widen scope to fix a discovered conflict; stop and report it.

Independently authored from the coordinator's bounded specification, this
project's [backup CLI](backup-restore.md), [preview contract](preview-contract.md),
[portable recovery ADR](adr/0004-portable-backup-recovery-material.md) and
[fixed-stage v3 contract](linux-systemd-offline-recovery-cli-contract.md).
No archived CPA/CLIProxyAPI/Sub2API source or test is copied/ported; no strict
clean-room claim. No new dependency; existing licenses and
[third-party notices](../THIRD_PARTY_NOTICES.md) are unchanged.

Public API sources consulted 2026-10-21: [Go flag](https://pkg.go.dev/flag)
(ContinueOnError, ErrHelp, SetOutput and Usage),
[Go slog](https://pkg.go.dev/log/slog) (default error logging), and
[Go errors](https://pkg.go.dev/errors) (Is/As/Join classification).
The actual local oracle uses the frozen Go1.26.8 executable, not a claim that
the documentation site's current toolchain version was executed.

## Threat statement and nonclaims

The coordinator's E/F/D pre-review established static argv-to-diagnostic paths,
not a dynamic real-secret incident. Unknown subcommands can enter returned errors;
the six legacy entry points create FlagSets whose diagnostics currently reach
stdout; returned parser/runtime errors can reach main's default stderr logger.
Filesystem error chains can carry explicit argv paths. No credential scan or
real-secret access is authorized.

Do not overstate the ordinary --password=CANARY case: an undefined flag normally
prints the flag name, not the supplied value. Conversely malformed ---CANARY and
--=CANARY can echo their whole token. The test uses one unique, publicly synthetic
ASCII canary only, never a real password, Key, session or production path.

## Frozen tests-first joint oracle

Before modifying either production file, freeze their bytes/hashes, the new test
source, exact selected name/arguments/workdir/runtime and the finite vector list.
One noncached serial invocation selects only
TestBackupCLIDiagnosticCanaryJointOracle. No exploratory reruns.

There are31 fixed vectors: one unknown command; for each of create, verify,
restore, key-export, key-verify and key-import, three invalid shapes (undefined
canary flag name, triple-dash token, empty flag-name token), one ordinary
--password=CANARY counterexample and one missing owned canary-path operation.
Each vector executes direct runCLI and an isolated child that invokes the actual
unchanged main function from the Go test binary, capturing both streams and the
actual exit. This is not a distributed/release-binary acceptance claim.

Direct parser stdout and returned errors, plus child stdout/stderr, remain
private in-memory captures. Only source-owned fixed tags and booleans may enter
the outer test log; never print canary/args/paths/errors or captured bodies. The
outer evidence checker rejects any literal or registered encoded canary in raw
test stdout/stderr. Unknown exit, child timeout, incomplete captures or unclear
cleanup is an unexpected failure, not the approved red.

Required old joint witness: the unknown command reaches the returned error and
main stderr; all18 undefined/malformed flag vectors reach parser stdout, returned
errors and main stderr; all six ordinary password-value counterexamples do not
contain the canary. On Windows all six controlled missing-path operations carry
the canary through real PathError chains to returned errors/main stderr. On
non-Windows key-export/key-import may reject unsupported custody before reading
the path: do not invent a path witness there. Other runtime path witnesses remain.
All errors have the existing exit2 argument /exit1 operation distinction;
invalid arguments do not read stdin, operations do; child stdout agrees with the
direct capture, and no output/target is created in the owned temporary root.

Only that complete private joint witness permits the sole expected red tag
BCLI_OLD_DIAGNOSTICS_ECHO_PUBLIC_CANARY. Partial/mixed leakage, wrong phase,
compiler/fixture errors or a canary reaching the outer log are unexpected: freeze,
do not patch, rerun or proceed to implementation. No old static review is
retroactively described as having executed this dynamic test.

## Required diagnostic behavior

For the six legacy commands and unknown/missing subcommands, every failed CLI
return is an allowlisted command + phase, not raw argv, parser error, PathError,
underlying error text/chain or an encoding of them. Fixed spelling:
BACKUP_CLI <command> <phase> failed. Commands are none, unknown, create, verify,
restore, key-export, key-verify and key-import. Phases are arguments, versions,
password and operation; unrecognized internal classification inputs collapse to
the fixed unknown-command /operation class, never their supplied value.

Legacy parser diagnostics are suppressed at their unsafe source and replaced on
their existing stdout channel with the fixed command/arguments line; main still
logs failures on stderr. Do not silence the entire logger. Help (-h/--help) must
still print the original static FlagSet usage/default descriptions and exit0;
do not replay a captured parser error. Values supplied before help must not enter
usage. Missing command/top-level unknown help retain their existing exit2/usage.

Keep all successful create/verify/restore/key output text, flags, validation and
0/1/2 semantics. Required items, extra positional arguments and invalid versions
still fail before stdin and filesystem operations; do not parse passwords from
argv, reorder operations or add retries. Input-precedence evidence combines the
unchanged linear control flow, a direct stdin counter and unchanged owned paths;
it is not represented as a filesystem syscall trace.

key-import's existing errors.Is(ErrPublishStateUncertain) is a separate fixed
publish_state_uncertain phase, retaining the safe instruction to inspect the
target without retrying or overwriting. Preserve this sentinel's classification
without retaining any other raw error/cause. Do not say the import was unexecuted,
rolled back or safe to retry: publication may already have happened. A private
pure classification test with joined opaque causes is not actual I/O injection
or a newly reproduced uncertain Commit. Frozen Windows core tests provide their
own existing synthetic export/import/restore and uncertain-publish facts.

systemd-v3-verify/restore continue directly to the frozen v3 implementation:
arguments/platform/unit/seed/identity/installation/wrapping/package/target/
restore/cleanup/cancelled and combined fixed results are not remapped. No stdin,
credential/provider, admission, lifecycle, cleanup, timeout or success change.

## Final finite validation and delivery

After only the complete expected old red, implement within the same four files.
Retain the joint oracle: new behavior must have no leak and exactly fixed return/
stdout diagnostics, not merely avoid the approved old-red label. Add direct/main
help, mandatory-item, missing-value, positional, version and password controls,
opaque error mapping and uncertainty-hint/classification assertions. All unknown
failures remain failures. Existing success and v3 regressions remain unmodified.

Freeze all final names/source hashes/arguments before the single noncached
Windows serial invocation (-count=1 -p=1 -parallel=1 -mod=readonly -timeout=15m
-failfast -json), selecting cmd/cpa-cloud-backup and internal/recoverymaterial.
Planned full selection is15 parents/7 children=22 full names, zero skips:

- New TestBackupCLIDiagnosticCanaryJointOracle,
  TestBackupCLIDiagnosticInputPrecedenceAndHelp,
  TestBackupCLIDiagnosticFixedErrorMapping and TestBackupCLIDiagnosticMainHelper
  have no outer subtests. The helper is inert without its isolated-child marker.
- Existing TestReadPassword children: line_feed, crlf, spaces_retained, empty,
  multiple_lines; TestRunCLICreateVerifyRestore children: create, verify.
- Existing childless TestRunCLIRequiresSubcommandWithoutReadingPassword,
  TestParseRecoveryVersions, TestRunCLIKeyVerifyReadsPasswordOnlyFromStdin,
  TestRecoveryCommandsValidateFlagsBeforeReadingStdin,
  TestOfflineCLIRejectsArgumentsWithoutEchoOrStdin,
  TestOfflineCLIExplicitModeFailsClosedOutsideRecoveryUnit,
  TestOfflineCLICannotRestoreIntoRetainedSeed.
- Frozen Windows core childless
  TestWindowsDistinctStoreExportImportRestoreAndPostImportRotation and
  TestWindowsImportConflictAndUncertainPublishNeverOverwrite.

The raw outer logs, exact exit, one run/pass per full name, no fail/skip/incomplete/
unexpected/parse error, source stability and canary-clear booleans are recorded
privately. Child actual main execution/captures are joint-oracle facts, not extra
Go outer test names. No local race/coverage/security-completeness claim.

First unexpected local or natural CI red freezes the batch with no hot patch,
rerun, dispatch or later gate. Only a complete final pass permits directed vet/
build, gofmt, local links, diff and exact four-file/frozen-source checks, then one
commit/push and a new Draft PR stacked on exact PR99. Re-read remote parent/tree/
every changed blob/mode and local clean state. Any connector transport must use
the exact expected-base non-force lease and disclose different commit metadata
SHAs without inventing parent/tree equivalence. No credential/trust/remote change.

Only natural new-head pull_request CI follows; root independently re-pins it and
arranges original E/F formal same-head read-only review. At most this batch's
diagnostic-source narrow candidate: no product-wide, recovery, GOV-02, real-host,
real-secret, real-I/O/power-loss or production acceptance. Do not alter PR100 or
old histories, use8787, scan credentials, Ready/merge/tag/package/deploy.

## Executed local results (2026-10-21)

The contract and joint oracle were frozen before either production file changed.
Exactly one old-source invocation exited1 with the complete approved joint tag
BCLI_OLD_DIAGNOSTICS_ECHO_PUBLIC_CANARY: one selected test run/fail and one
package failure, no other test, no skip, no panic/build/fixture failure. Unknown
command, all18 undefined/malformed parser vectors and all6 Windows missing-path
operations showed the private joint witness. All6 ordinary --password=VALUE
counterexamples were canary-clear; this does not claim their value leaked.
Direct stdin precedence, actual-main exit/channel agreement and empty owned
roots held. Raw outer stdout/stderr were canary-clear, stderr empty, frozen
source hashes unchanged. Old stdout SHA256:
3386a57bf7d9299fdf917dc3c42efaa9fd9f640105a29d987fd0d2504f12d235.

After the bounded change, exactly one noncached Go1.26.8 windows/amd64 directed
serial invocation selected the15 parents/7 children frozen above:22 full names
each ran/passed once,2 package passes, exit0,0 fail/skip/incomplete/unexpected/
parseerror, empty stderr and unchanged frozen source hashes. Final stdout SHA256:
36a5accd6628fc60599a7268f2f99e1ccc024578230aa92da06f3ddddacfaa63.
No exploratory rerun, local race or new coverage claim.

The unchanged31-vector joint oracle and85 controls each ran direct plus captured
actual-main subprocesses (116 paired vectors, not116 Go test names). The85
controls cover3 top-level,24 help,18 basic invalid arguments,14 individually
omitted required items,18 password and8 invalid-version cases. Static help bytes,
values-before-help, exit0/1/2, stdin precedence and owned-root invariance passed.
Fixed classifiers reject unknown classification input, do not format opaque
causes or preserve PathError chains, and retain only the safe uncertain-publication
sentinel and inspect-without-retry/overwrite instruction. That joined-cause test
is pure classification, not new I/O injection or uncertain-publish reproduction.

Unmodified CLI create/verify/restore, key-verify and fixed v3 tests passed. The two
unmodified Windows core tests passed their inherited synthetic distinct-store
export/import/restore/post-import-rotation and conflict/controlled publication-sync
uncertainty scenarios. These are not a new physical-fault, real-profile/credential,
M1-to-M2, GOV-02 or production claim. No test was rerun after this final gate.

Subsequent directed vet and backup-CLI build each exited0 with empty stdout/stderr;
gofmt,5 local document links, diff check and exact four-file scope passed. Source
comparison preserved main/logger, readPassword/usage/wipe, parseRecoveryVersions,
all flag registrations and success-output format strings. All other tracked files,
including v3 source/tests, old regressions, core/schema/dependencies/workflows,
remain unchanged. Remote natural CI and original E/F review are separate gates;
neither is asserted here. Draft / production NO-GO remains in force.
