# Real Linux Secret Service: volatile-collection interoperability A

Status: isolated development-preview fixture, based on PR80 exact head
`b3a4caeb7ee157c0cddcd6b814da0837c5563071`. Not production custody.

This first independent slice deliberately uses GNOME Keyring's volatile session
collection, explicitly assigned to the default alias in a fresh private bus.
It is **not persistence or service-restart recovery**. No collection password,
unlock, prompt, official client, configuration replacement or production apply
is permitted. No new production approval constructor or provider API is added.

## Cases required for this slice

1. Reuse every PR80 isolation assertion before daemon startup or Key operations:
   exact packages, non-root fixture account, synthetic private HOME/XDG/temp,
   env allowlist, private loopback-only netns and IP socket denial, inaccessible
   host home/runtime, representative host Unix socket, separate `/dev/shm`, and
   working local Unix sockets. A failure aborts; there is no fallback or skip.
2. Verify real private bus and real service executable/cgroup/PID/UID using the
   unchanged metadata observer before allowing the CPA test process to run.
3. Actual `LinuxSecretServiceRecoveryKeyProvider` / `DbusBackend` creates one
   synthetic recovery Key and repeats create without replacement. Its test-only
   caller uses the existing private approval constructor; runtime code is unchanged.
4. Independent C/libsecret verifier searches all fixture CPA recovery items and
   requires exactly one unlocked item, exact seven identity attributes, binary
   content type, envelope identity/checksum and the original 32-byte Key. It
   returns only a fixed boolean result; it never prints an item path or secret.
5. A separate CPA process opens the durable reference and obtains the same Key.
6. Wrong transaction, separate lock namespace and valid-but-missing item requests
   fail closed. The independent verifier rechecks the same original item path,
   identity and contents after each case; no replacement is allowed.
7. Stop only the owned service, keep the private bus alive, and require a fresh
   CPA process to report unavailable. No service activation directories exist.
8. Stop owned child processes, scan captured output for Key/envelope and common
   encodings, then clean only the owned unit/account and validated temporary roots.

The test oracle consists of newly created owner-private files containing the
synthetic Key, opaque reference and initial item path. They are available only
inside the disposable fixture, never uploaded or printed, never a provider
fallback. The provider's `open` reads Secret Service, not these oracle files;
the test caller compares the returned Key afterward. This is explicitly test
infrastructure, not a proposed custody format. No password exists in slice A.
Raw daemon, libsecret, Rust-test and D-Bus diagnostics remain private; only fixed
labels and non-secret source/executable digests reach CI. No secret digest is
published. A scanner failure is a failure even though raw output is withheld.

### Diagnostic-only failure evidence

Before the first CreateItem sends any synthetic secret, a `cfg(test)` hook saves
the actual randomly generated Key and actual encoded envelope as `sent.key` and
`sent.envelope`. Exclusive descriptor-relative creation uses the approved private
directory, no-follow and mode 0600; write/sync failure prevents sending. The
production RNG is unchanged. Both success and failure output scans use only these
pre-send samples, never accepted/rejected response bytes or reconstruction from
a reference. Missing samples fail scanner completeness. The post-acceptance
`expected.*` and reference files remain independent verifier inputs, not scanner
inputs. No sample, secret digest or raw child output is uploaded or printed.

At the original GetSecret call, test-only diagnostics classify the same typed
response: decode success/failure; matching/mismatching session; empty/nonempty
parameters; octet-stream/text-plain/empty/other content type; exactly 148/short/long
value; and envelope valid/format/checksum/identity/not-checked. Envelope identity
includes the original outgoing Key. Only fixed labels leave the fixture; no
raw MIME, path, parameters, value, digest or D-Bus error text is emitted. The
previous post-failure SearchItems/GetSecret diagnostic reread is removed.

These hooks neither mutate the response nor change the original production
rejection conditions. MIME compatibility and production returned-value zeroizing
are not implemented by this diagnostic change. Owned processes are stopped and
their status recorded before scanning, so scanner failure cannot hide a completed
process cleanup marker. This marker does not by itself certify outer root cleanup.
The dedicated run may remain FAIL/NO-GO; field classification is not acceptance.

## Policy and prompt boundary

The separate fixture bus policy permits only the service calls required here,
plus metadata and session close. It continues to deny Unlock, Prompt, SetSecret,
property writes, collection creation, Lock and Delete, including omitted-interface
method names. Only fixture setup binds `default` to the existing session collection.
CPA does not call SetAlias. CPA always sends `replace=false`; that argument is
statically guarded and idempotent create has an independent unchanged-item check.
D-Bus policy cannot inspect the replace boolean and is not claimed to do so.
The libsecret verifier overrides its synchronous prompt handler to refuse and
uses no unlock/search-unlock/store APIs; the bus also denies Prompt for all peers.
Runtime rejection of every forbidden method is not claimed by static policy tests.

## Explicitly unfinished, not skipped or counted as acceptance

**NOT IMPLEMENTED / NO-GO in this PR:** real persistent collection and service
restart reopen; locked collection, duplicate item, corrupt/empty secret negative
cases; exact item deletion with sentinel retention; two CPA processes racing to
create the same transaction; full PR76 cleanup state-machine integration. They
require subsequent narrowly specified fixture slices. This PR never deletes an
item through Secret Service; disposing its owned volatile service is teardown,
not evidence for exact provider deletion.

Existing fake-backend failure injection remains separate unit-test evidence, not
a claim that those failures were induced in a real daemon. The plain CPA transfer
session is **not encrypted**. No whole-host IPC isolation, Linux production
custody, Windows/macOS custody or ten-tool write support is established. All
exact-head ordinary CI and original visible E/F review must pass before this
slice is accepted; keep Draft, no manual dispatch/Ready/merge/release/deployment.

## Independent provenance and dependencies

New Python/Rust test/C verifier/policy/workflow source is independently authored
from this contract, the existing CPA provider format and public APIs. No archived
CPA, CLIProxyAPI, Sub2API, or CC Switch implementation is used. Runtime component
pins and licenses are unchanged from [PR80 manifest](../preflight/components.json)
and its [contract](../preflight/CONTRACT.md). Rust 1.90.0 and the existing locked
crate dependencies retain their notices; no new crate is introduced.
The verifier explicitly acknowledges libsecret's unstable path API at the pinned
0.21.4 version; it does not assume compatibility with later libsecret releases.

Official references consulted 2026-10-16:

- [Secret Service collection/item protocol](https://specifications.freedesktop.org/secret-service/latest/ch03.html)
- [Service methods and aliases](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html)
- [D-Bus policy](https://dbus.freedesktop.org/doc/dbus-daemon.1.html)
- [GNOME Keyring options](https://manpages.debian.org/unstable/gnome-keyring/gnome-keyring-daemon.1.en.html)
- [libsecret independent service instance](https://gnome.pages.gitlab.gnome.org/libsecret/type_func.Service.open_sync.html)
- [libsecret search paths](https://gnome.pages.gitlab.gnome.org/libsecret/method.Service.search_for_dbus_paths_sync.html)
- [libsecret read without unlock](https://gnome.pages.gitlab.gnome.org/libsecret/method.Service.get_secret_for_dbus_path_sync.html)
- [libsecret refuse prompt override](https://gnome.pages.gitlab.gnome.org/libsecret/vfunc.Service.prompt_sync.html)
- [libsecret 0.21.4 public header/API feature guard](https://github.com/GNOME/libsecret/blob/0.21.4/libsecret/secret.h)
