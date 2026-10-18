# Two-VM synthetic v3 recovery acceptance

2026-10-19. Independent acceptance slice on PR79 exact
`286ba8f94cfdd9e90b9597e936a731e62d36bd76`, with its
[same-VM contract](linux-systemd-backup-rehearsal-contract.md) and
[ADR 0005](adr/0005-linux-systemd-backup-wrapping-key.md). Draft; production NO-GO.
This adds tests, a restricted harness and natural PR CI only. It adds no
production rotation, custody, recovery interface, dependency or feature enablement.

## Frozen experiment

Two separate fresh GitHub-hosted Ubuntu 24.04 VM jobs build the same asserted
full source SHA. Neither job restores caches or shares a disk, service, user
directory, credential store, host encryption key, TPM state or system image.
Both require systemd PID 1, VM detection, a previously absent fixed test
user/group and unit names, and a previously absent systemd host key. Each invokes
`systemd-creds setup` locally and uses explicit `--with-key=host` encryption.
The manager's host key is never read/exported/copied/replaced by the fixture.

`H_A` and `H_B` are different machine trust roots. `M1` is the same application
installation key needed to decrypt the original database, and `W1` is its
different backup wrapping key. A and B independently construct the same
**public synthetic test vectors**, then pipe strict credential v1 JSON to their
own local encryption tool. No plaintext credential file, secret argv/env,
runtime credential export or reader override is used. This is a simulation of
independent recovery provisioning, not a production recovery escrow mechanism.

Each transient unit uses a fixed non-root identity, genuine
`LoadCredentialEncrypted=`, a private network namespace, AF_UNIX-only socket
creation, read-only system mounts except its owned temporary root, home
protection, no capabilities/new privileges, core dumps disabled and bounded
runtime. IPv4/IPv6 socket creation is tested for denial and the unit netns TCP,
TCP6, UDP and UDP6 tables must contain no sockets. No listener, network worker
or HTTP server is started; in-memory HTTP handlers do not bind sockets.
API filesystems `/dev`, `/proc`, `/sys` and filesystem Unix sockets retain the
same limitations described in PR79; no whole-host filesystem/IPC isolation,
TPM/null-key policy or unswappable-memory guarantee is claimed.

A initializes the real service, stores an encrypted synthetic record, an actual
synthetic admin-session digest and a restricted employee/key policy, creates and
verifies v3, then closes the service. Its unit must be stopped before relay
publication. The source application version is the full asserted commit SHA;
credential/binding format is v1 and backup format v3.

The exact relay allowlist is `package.cpacb`, `binding.json`,
`foreign-master.capsule`, `vm.json`. The first two are the synthetic encrypted
package and non-secret installation binding. The capsule is A's encrypted
installation credential, used **only** for B's activation-rejection negative.
`vm.json` contains the source SHA, format versions and non-secret machine/boot
identities; B must differ in both identities. No raw logs, database, host key,
plaintext material, runtime directory or scanner oracle is uploaded. Every
relay file must be a bounded single-link regular file; extra/missing files,
symlinks, unexpected JSON fields, source/format drift or secret-encoding matches
are failures. The transfer is a run-scoped artifact with one-day maximum
retention, deleted and independently rechecked by a final cleanup job even when
A/B fails. B downloads only that exact artifact from the same workflow run.

B first restores into a new private directory using its locally encapsulated
M1/W1 and unchanged production readers. Two further independent activations
reopen the restored service, decrypt the original record, verify administrator
state, file ownership/modes, restricted employee/key policy and rejection of
the old admin session. Database session/OAuth rows must be cleared. These
positives precede every negative. The closed positive directory is fingerprinted
privately and must remain byte-identical after each negative.

## Negative cases and required stages

- A's capsule on B: manager `EXIT_CREDENTIALS` (243), application entry marker
  absent; a generic application failure cannot pass this case.
- Missing/wrong/swapped installation material; mismatched installation
  instance/id/version; binding instance/id/version/canary mutations: installation
  binding rejection, before creating/migrating a database or starting an App.
- Missing/swapped wrapping material; equal installation/wrapping bytes under
  different IDs; overlapping role IDs or wrong instance: provider admission
  rejection before restore. Wrong wrapping bytes/id/version: package rejection.
- Package header/ciphertext mutation and header/body truncation: package
  authentication/format rejection before staging/publication.
- At the existing final installation validator, replace staged binding bytes
  on disk, or replace the credential-directory reference with an empty owned
  directory. Delegate to the original validator; do not substitute a fake
  credential reader or accept a fabricated mount. Binding-file replacement must
  be rejected rather than publishing a changed file. The credential case proves
  rejection of a changed credential source reference, not manager-mount mutation.
- Replace the restore parent after initial binding validation: parent identity
  rejection before the final validator/publication. Existing target: reject
  without changing its public sentinel or any positive restored data.

Test-only wrappers instrument existing validator boundaries and always delegate
to the captured production validator. They never turn an invalid result into
success. Every negative requires its exact reached stage, no final directory
(except the preexisting sentinel case), no active worker/listener, and unchanged
positive data. An unexpected successful publication is a hard failure, not a
reason to weaken the test or repair production during this slice.

## Evidence and stop rules

Only fixed source-owned stage/status categories leave the harness. Capture
stdout/stderr and the exact unit invocation journal, including failed activation.
Scan actual locally provisioned JSON/key bytes and public fixture sentinels in
raw, hex, base64, escaped and decimal forms. Missing/failed journal collection,
incomplete scan, unknown process/resource cleanup or relay deletion is failure.
Original operation failure, content-match/completeness and A/B/relay cleanup are
independent; a finally error must not erase the first failure. Failure still
attempts all safe owned cleanup, without removing identities/data while a unit
may be active. No raw diagnostics, paths, UIDs, key hashes, canary values or
matched fragments are evidence output.

Any failed positive, wrong negative stage, identity drift, unexpected socket or
publication, incomplete scan or cleanup failure stops later cases. No automatic
retry, weaker configuration, manual rerun/dispatch or secret regeneration after
failure. Record the first natural run's red evidence and stop. Ordinary CI and
original visible E/F review must refer to the exact new head/base, separately
from prior PR79 evidence. Keep Draft; no merge, Ready, package or deployment.

This proves at most synthetic same-version application recovery on independent
VMs/host keys. M1→M2 rotation, historical-key retention/retirement, production
custody, real escrow retrieval/disaster recovery, TPM/null-key attestation,
scheduled retention, power-loss durability, RTO/RPO and rollout rollback remain
separate, unverified gates.

## Sources and provenance

Independently authored 2026-10-19 from the project contracts and original F's
read-only first-slice design. Project-owned PR79 test utilities are explicitly
reused. No prohibited implementation, tests or documentation were copied/ported;
no strict clean-room claim. Public references inspected:

- [systemd v255 credential encryption semantics](https://github.com/systemd/systemd/blob/v255/man/systemd-creds.xml)
- [systemd v255 execution/credential loading](https://github.com/systemd/systemd/blob/v255/man/systemd.exec.xml)
- [systemd v255 exit status definitions](https://github.com/systemd/systemd/blob/v255/src/shared/exit-status.h)
- [GitHub workflow artifact transfer](https://docs.github.com/en/actions/tutorials/store-and-share-data)

No new runtime dependency. systemd retains LGPL-2.1-or-later/component notices;
Python standard library retains PSF terms. Existing Go libraries retain their
licenses in [third-party notices](../THIRD_PARTY_NOTICES.md). Actions are pinned
to immutable revisions. This fixture does not authorize release/distribution.
