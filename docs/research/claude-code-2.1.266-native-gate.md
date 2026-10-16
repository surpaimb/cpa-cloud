# Claude Code 2.1.266 employee configuration native gate

Status: blocked before native execution, 2026-10-16.

## Purpose

This gate is limited to the employee configuration switcher's proposed Claude
Code projection. It asks whether the exact official Windows x64 Claude Code
`2.1.266` binary accepts the independently authored `settings.json`, sends the
synthetic employee Key as a bearer credential to a numeric-loopback CPA Cloud
endpoint, and uses the server-declared Anthropic Messages default model.

It does not authorize real configuration writes, replace an existing file, use
a real employee Key, contact Anthropic, reuse a Claude subscription credential,
or establish cross-version compatibility.

## Public contract and fixed inputs

The official [settings documentation](https://code.claude.com/docs/en/settings)
defines the user `~/.claude/settings.json`, its strict JSON form, the top-level
`model`, and the separate Claude-owned `~/.claude.json`. The official
[environment-variable reference](https://code.claude.com/docs/en/env-vars)
defines `CLAUDE_CONFIG_DIR` as the override for settings, credentials, session
history, and plugins. The official
[LLM gateway contract](https://code.claude.com/docs/en/llm-gateway-connect)
maps `ANTHROPIC_BASE_URL` plus `ANTHROPIC_AUTH_TOKEN` to
`Authorization: Bearer` requests at `/v1/messages`.

The fixed local binary was inspected without executing it:

- platform and version: Windows x64 `2.1.266` (`FileVersion` and
  `ProductVersion` both `2.1.266.0`);
- size: `218971808` bytes;
- SHA-256:
  `d2c5f7b3b6a12819097ceb6efbce2a390157166003fcaee32dbde0e6d7b45ef7`;
- Authenticode: `Valid`, signer organization `Anthropic, PBC`.

The committed planner fixture is
`desktop/employee-config-switcher/crates/employee-config-core/tests/fixtures/claude-code-2.1.266/settings.json`,
SHA-256
`a0091bfb4c7c4b60bfd9c0d6f8ed441a055924cf757294fd93b5ff287ec2d4ff`.
`static-evidence.json` binds these identities and explicitly records that the
native gate was not executed.

## Required isolation boundary

A native run is permitted only in a disposable Windows guest that proves all
of the following before the binary starts:

1. The guest has a distinct OS identity and cannot mount or read the host user
   profile, credential stores, registry hive, source checkout, SSH material, or
   browser state.
2. The exact hash-pinned binary and independently authored harness are copied
   into the guest through a byte-verified one-way staging step. No host
   configuration directory is mapped into the guest.
3. `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA`, every relevant XDG path,
   `TEMP`, `TMP`, `CLAUDE_CONFIG_DIR`, the working directory, and `PATH` all
   point at newly created guest-local directories. `PATH` contains only the
   explicitly required guest binaries; it never inherits the host path.
4. The hypervisor disconnects every network adapter, or an equivalent
   independently verifiable system policy blocks all non-loopback egress. The
   synthetic CPA Cloud and capture server listen inside the same guest on
   numeric `127.0.0.1` addresses. Environment variables, proxy settings, DNS
   changes, and client telemetry flags are defense in depth, not the network
   boundary.
5. The process environment is constructed from an allowlist and contains no
   real Anthropic, cloud-provider, proxy, telemetry-export, MCP, hook, IDE, or
   updater credentials. The test uses only a synthetic employee Key.
6. The guest is destroyed or restored to its clean snapshot after bounded logs
   and non-secret request metadata are exported. Prompts, request bodies,
   responses, and credentials are not retained.

## Current host observation

The current Windows session does not satisfy that boundary:

- the agent and any child process use the same medium-integrity user token as
  the real `C:\Users\apple` profile, so environment redirection cannot prove
  that absolute-path reads of real user data are impossible;
- Windows Sandbox and WSL are unavailable;
- Docker is installed but its service is stopped and it does not establish a
  Windows guest boundary for this executable;
- the existing powered-off Hyper-V VM is an unrelated test asset and was not
  authorized for this task;
- the current token is not elevated, so it cannot install and independently
  audit a temporary Windows Filtering Platform policy. A per-process firewall
  rule alone would also not prove that every helper child is contained.

Consequently Claude Code was not started. The existing historical
environment-variable-only probes are not accepted as evidence for this gate.

## Tested static behavior

The Rust regression generates the planner output from a synthetic discovery
response and checks it byte-for-byte against the fixed fixture. It also checks
that:

- `model` equals the server-declared `anthropic-messages` default;
- the gateway URL is normalized to the loopback origin;
- the synthetic employee Key is carried only by `ANTHROPIC_AUTH_TOKEN`;
- the fixture hash and fixed binary identity match `static-evidence.json`;
- `native_gate.status` remains `blocked` and `executed` remains `false`.

This is source/schema evidence only. A successful request, native parser
acceptance, header observation, model selection, or credential isolation is not
claimed until the disposable-guest boundary above is available and the exact
fixture is rerun there.
