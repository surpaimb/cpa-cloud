# Gemini Responses SSE and trusted-proxy source batch contract

Date: 2026-09-29  
Base: `main@d56e0cedf17e036b56c57473b99eef088912d1f5`

This contract freezes two development-preview milestones before implementation. It is independently authored from CPA Cloud's functional requirements, the public protocol sources below, and the repository's existing interfaces. It does not authorize use of archived CPA, CLIProxyAPI, Sub2API, `../cpa-cloud-reference`, or `../cpa` as an implementation source.

The two milestones ship as separate pull requests. The Gemini/Responses SSE pull request lands first. The trusted-proxy pull request is then rebased onto the exact new `main` before its final test and review pass. No tag, package, deployment, production credential, real membership claim, or listener on the real port `8787` is part of this batch.

## Public sources and provenance

- Google Gemini API, [`models.streamGenerateContent` and `GenerateContentResponse`](https://ai.google.dev/api/generate-content), v1beta endpoint and SSE response schema. The public reference says `:streamGenerateContent` returns a stream of `GenerateContentResponse` instances, documents `candidates`, `promptFeedback`, `usageMetadata`, `modelVersion`, `responseId`, `Part`, `FunctionCall`, `FunctionResponse`, and candidate finish reasons.
- Google Gen AI JavaScript SDK, [`GenerateContentResponse`](https://googleapis.github.io/js-genai/release_docs/classes/types.GenerateContentResponse.html), as corroborating public schema evidence that `modelVersion` and `responseId` are optional response properties rather than fields guaranteed in every streamed chunk.
- OpenAI, [Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses) and [Function calling](https://developers.openai.com/api/docs/guides/function-calling), for the Responses API event lifecycle and function-call argument events. This slice uses the Responses API only; it does not use or claim support for the newer Interactions API.
- IETF, [RFC 7239 section 8.1](https://www.rfc-editor.org/rfc/rfc7239.html#section-8.1), only for the security warning that forwarding data is mutable and must be bounded by explicit proxy trust. This slice does **not** implement the standardized `Forwarded` header.
- Go standard library, [`net/netip`](https://pkg.go.dev/net/netip), for numeric address/prefix parsing, IPv4-mapped IPv6 unmapping, canonical prefixes, and containment. `Addr.IsPrivate` is not an access-control primitive and must not be used to infer trust.

No new third-party dependency is planned. If implementation introduces one, its license and source provenance must be recorded before merge.

## Global invariants

1. Employee authentication and `ClientProtocol` authorization are decided from the incoming endpoint before route selection. An OpenAI-compatible upstream never changes a Gemini employee request into an OpenAI-authorized request, and a Gemini upstream never changes a Responses employee request into a Gemini-authorized request.
2. `UpstreamProtocol` selects raw response parsing, usage observation, pricing evidence, and hard-bound enforcement. Every non-sentinel upstream SSE JSON frame is observed as raw upstream evidence before conversion or downstream output.
3. Request conversion and validation happen before the durable dispatch barrier. Exactly one upstream network request may occur after that barrier. Unsupported or lossy input fails before an attempt or network call.
4. No employee key, authorization header, upstream token, prompt, model response, forwarding header, or full source chain is logged. Employee credentials are consumed locally and are never forwarded upstream. TLS verification remains enabled.
5. A converted stream is successful only after a valid protocol terminal, clean physical EOF, and the existing bounded post-terminal drain. Cancellation, conversion failure, observer failure, short write, flush/write failure, truncated SSE, duplicate terminal, data after terminal, or drain timeout is never success. A request is never replayed.
6. Existing native paths, six non-stream cross-protocol directions, Chat/Responses SSE, Messages/Responses SSE, revocation, accounting, and resource ownership behavior must remain covered and unchanged except where this contract explicitly adds trusted source resolution.

## Milestone A: Gemini v1beta `streamGenerateContent` and OpenAI Responses SSE

### Route boundary

Only these two converted streaming routes are added:

| Incoming employee endpoint | `ClientProtocol` | Upstream wire | Plan |
| --- | --- | --- | --- |
| `POST /v1beta/models/{model}:streamGenerateContent` with no query or exactly `alt=sse` | `gemini-generate-content` | OpenAI Responses SSE | `PlanGeminiToResponses` |
| `POST /v1/responses` with JSON `stream:true` | `openai-responses` | Gemini v1beta `:streamGenerateContent?alt=sse` | `PlanResponsesToGemini` |

Native Gemini and native Responses streaming retain their current executors. `:generateContent`, Responses without `stream:true`, Chat Completions, and Messages do not enter this new bridge.

`PrepareCrossProtocolStreamRequest` gains the two plan selections above. Gemini streaming is selected by the URL operation, so it does not require or synthesize a `stream` JSON field. Responses still requires exactly boolean `stream:true`. Request conversion reuses the existing strict Gemini/Responses request converters and feature gates. The outgoing Responses body contains `stream:true`; the outgoing Gemini body contains no invented streaming field because streaming is in the URL.

The representable feature set is text, system instruction already supported by the non-stream converter, function declarations, function calls, function results, usage, and finish reason. The slice rejects before dispatch any media, citations or grounding, thinking or thought signatures, code execution, hosted/server-side tools, refusals, annotations, stateful or background Responses fields, multiple candidates, parallel/unmatched calls, or other field that the existing strict request contract cannot faithfully represent.

### Frozen protocolconv interfaces and file ownership

Workflow D owns only these files for its component commit:

- `internal/protocolconv/gemini_stream.go` and `internal/protocolconv/gemini_stream_test.go` (new);
- the narrow Gemini cases and tests in `internal/protocolconv/capability.go`, `capability_test.go`, `execution.go`, `execution_test.go`, `stream_execution.go`, and `stream_execution_test.go`;
- `internal/service/protocol_runtime_gemini_stream_test.go` (new, tests only). D does not edit a service implementation file.

The integrator owns all handlers, URL construction, authentication, route selection, durable dispatch, accounting, shared service tests, docs, CI, and browser work.

The frozen pure types are:

```go
type GeminiToResponsesStream struct { /* private bounded state */ }
func (s *GeminiToResponsesStream) Feed(raw []byte) ([]SSEEvent, error)
func (s *GeminiToResponsesStream) EOF() error

type ResponsesToGeminiStream struct { /* private bounded state */ }
func (s *ResponsesToGeminiStream) Feed(raw []byte) ([]SSEEvent, error)
func (s *ResponsesToGeminiStream) EOF() error
```

They are zero-value ready, deterministic, pure, and perform no I/O. `crossProtocolStreamConverter` adds one private pointer for each. The existing public `StreamConverter`, `SSEFrame`, `SSEEvent`, and `StreamTerminalOutcome` interfaces do not change. `NewStreamConverter` instantiates the type that consumes the selected upstream: `PlanGeminiToResponses` consumes Responses SSE and emits Gemini SSE; `PlanResponsesToGemini` consumes Gemini SSE and emits Responses SSE.

Gemini input SSE accepts only a blank event name or `message`; its `data` must be one complete JSON `GenerateContentResponse`. Responses input SSE retains the existing requirement that a non-empty event name equal the JSON `type`. Output Responses events use the JSON `type` as the SSE event name. Output Gemini events are data-only SSE events with an empty event name.

### Gemini frame contract

`responseId` and `modelVersion` may be omitted from an individual Gemini frame because the public response schema does not guarantee them in every streamed chunk. The first non-empty string observed for each field is frozen; every later explicit value must match it. Explicit null or an empty string is invalid. Until both values have been observed, semantic text/function actions are retained in bounded converter state and nothing is emitted downstream. Once both are available, the converter emits `response.created` and releases the retained actions in original wire order exactly once. If the terminal frame is reached before both identities are available, conversion fails without inventing an ID or model version. The common path where the first semantic frame supplies both identities remains live. This is intentionally support for the identity-bearing subset of legal Gemini streams, not a claim that every Gemini stream is convertible.

A non-blocked frame has exactly one candidate, explicitly or implicitly at index `0`. Candidate role, when content is present, is exactly `model`. Multiple candidates, another index, changing identities, null identities, unknown root fields, or unknown candidate/part fields fail closed.

The only successful candidate parts are:

- `{"text":"..."}`; each frame's text is a single delta chunk, including an empty chunk only where the typed lifecycle permits it;
- `{"functionCall":{"id":"optional","name":"required","args":{...}}}`; the call is complete in that frame. Arguments must be a JSON object. Explicit call IDs must be non-empty and unique; absent IDs use the existing deterministic response-ID/output-order derivation.

`functionResponse` remains request-side function-result input. It is never valid as model output. Media, executable code, code results, `toolCall`, `toolResponse`, citations, grounding, log probabilities, URL context, token-count detail arrays, model status, thinking metadata, and other unrepresentable fields are rejected.

Content order is wire order: frame order, then part array order. Gemini frames are incremental, not cumulative output snapshots. The converter appends each text delta once and emits each complete function call once; it never retransmits accumulated text or prior parts.

`finishReason` is absent until the terminal candidate frame and appears once:

- `STOP` maps to `StreamTerminalCompleted` and a Responses `response.completed` event.
- `MAX_TOKENS` maps to `StreamTerminalIncomplete` and a Responses `response.incomplete` whose only accepted reason is `max_output_tokens`.
- `SAFETY`, `RECITATION`, `LANGUAGE`, `OTHER`, `BLOCKLIST`, `PROHIBITED_CONTENT`, `SPII`, `MALFORMED_FUNCTION_CALL`, image reasons, `UNEXPECTED_TOOL_CALL`, and any unknown reason are failed conversion, never completed or incomplete.

`promptFeedback.blockReason` is accepted only as a blocked terminal with no candidates; it is failed conversion. Safety ratings may be consumed only to classify a blocked/failed terminal and are never exposed or logged. Safety ratings on a successful candidate, a prompt feedback object without a real block reason, or a block combined with candidates is unsupported. Upstream error text is not copied to the employee response.

### Usage and event ordering

Gemini `usageMetadata`, when present, is a cumulative snapshot for the whole generation request, not a per-frame delta. A later snapshot replaces the prior snapshot; it is never summed. Counters must be non-negative, not decrease, and obey the existing strict representable shape: `promptTokenCount`, `candidatesTokenCount`, `totalTokenCount`, and optional `cachedContentTokenCount`, with `totalTokenCount == promptTokenCount + candidatesTokenCount`. Thinking/tool-use counters, modality detail arrays, service-tier data, cache-write data, or inconsistent totals are unsupported. Omission retains the last valid snapshot; explicit `null` is invalid. A terminal may omit usage, in which case the converted terminal omits usage rather than inventing zeroes.

For Gemini to Responses, the first valid generation frame emits `response.created` before semantic output. Consecutive text chunks share one output message until a function boundary or terminal; the converter emits the normal output-item/content-part lifecycle and one `response.output_text.delta` per Gemini text chunk. A complete Gemini function call emits one function-call item lifecycle, including one complete compact argument delta/done pair. Sequence numbers and output indexes are contiguous and deterministic. The terminal Responses object contains the accumulated output exactly once.

For Responses to Gemini, `response.created` freezes ID and model but emits no empty client chunk. Each `response.output_text.delta` emits exactly one Gemini candidate text chunk. Function arguments are buffered under existing stream limits and emitted as exactly one complete Gemini `functionCall` chunk only after the Responses arguments/item lifecycle proves a valid JSON object. `response.completed` emits one final Gemini chunk with `finishReason:STOP`; `response.incomplete` emits one with `finishReason:MAX_TOKENS`. That final chunk carries the last representable cumulative usage snapshot, if any, and does not replay prior content. Empty output is represented only by the terminal candidate with an empty parts array.

Responses `error`, `response.failed`, unsupported incomplete reasons, hosted-tool events, refusal/annotation events, malformed or inconsistent item lifecycles, duplicate/diminishing sequence numbers, and data after terminal cause a typed failure. Because Gemini has no faithful success-shaped error event, no synthetic `GenerateContentResponse` is emitted for such a failure; the service closes or returns its fixed redacted error according to whether downstream bytes were committed.

All stream state uses the repository's existing converted-stream limits: bounded total bytes, item counts, individual text/argument sizes, and SSE line/event/stream limits. Limit exhaustion is a failure.

### Service integration and acceptance

The existing `protocolRuntime.executeStream` remains the single converted SSE bridge. It observes raw JSON before `FeedFrame`, applies downstream backpressure, buffers the terminal batch, drains to clean EOF within the bounded timeout, and only then writes and flushes the terminal. Successful accounting settlement happens only after that terminal write and flush succeed. The handler must call the bridge for the two new cross-protocol routes; it must not route converted streaming through JSON conversion or native forwarding.

Acceptance tests cover, in both directions:

- text-only and mixed text/function streams, call ID preservation/generation, argument buffering, order, and no replay;
- omitted, late, or changing identity fields; bounded pre-identity buffering; omitted versus cumulative usage; decreasing/inconsistent counters; STOP and MAX_TOKENS;
- prompt block, candidate safety finish, multi-candidate, media/citation/thinking/hosted-tool/state fields, malformed lifecycle, duplicate terminal, post-terminal data, truncation, and over-limit input;
- observer-before-write ordering, durable-before-network, one upstream request, downstream short write/flush failure, cancellation, clean EOF, bounded drain, and failure settlement;
- preservation of Chat, Messages, six non-stream conversions, native Gemini/Responses, `ClientProtocol` policy checks, and `UpstreamProtocol` raw usage/pricing.

## Milestone B: KEY-02 trusted proxy first slice

### Trust and header boundary

The default remains socket-peer-only. A deployment administrator may opt in with one or more repeatable `--trusted-proxy-cidr` flags. There is no database or employee-facing switch for header trust in this slice. Empty configuration means all `Forwarded`, `X-Forwarded-For`, and `X-Real-IP` headers are ignored.

Only `X-Forwarded-For` is implemented. `Forwarded`, `X-Real-IP`, PROXY protocol, provider-specific headers, DNS names, dynamic proxy discovery, and automatic cloud/LAN/loopback/private trust are ignored or deferred. Loopback and private networks may be trusted only when the administrator explicitly lists their CIDRs. Header support is transport source resolution, not an employee authorization toggle; Key source CIDRs remain the authorization rule.

Invalid trusted-proxy configuration fails startup before `App` creation or network listen. Values use the same canonical numeric address/prefix rules as KEY-02: no whitespace or zones, IPv4-mapped IPv6 is unmapped, prefixes are masked, canonical duplicates fail, and the list is sorted. At most 64 entries, 64 UTF-8 bytes per entry, and 4096 total entry bytes are accepted. An explicitly configured `0.0.0.0/0` or `::/0` is allowed but must be documented as trusting every peer of that address family.

### Frozen pure interface and E ownership

Workflow E owns only:

- `internal/keypolicy/trusted_proxy.go` and `internal/keypolicy/trusted_proxy_test.go` (new, pure parsing/value logic);
- `web/src/api.ts`, the trusted-proxy explanation in `web/src/pages/EmployeesPage.tsx`, and focused assertions in `web/src/test/KeyPolicy.test.tsx`.

E does not edit config/flags, `App`, store or migrations, authentication, request handlers, background workers, dispatch transactions, docs, or shared service tests. The integrator owns those files and the final web merge.

The frozen pure API is:

```go
const (
    MaxTrustedProxyCIDRs      = 64
    MaxTrustedProxyCIDRBytes  = 64
    MaxTrustedProxyCIDRsBytes = 4096
    MaxXForwardedForBytes     = 4096
    MaxXForwardedForHops      = 64
)

var ErrInvalidTrustedProxyConfig error
var ErrInvalidForwardedSource error

type TrustedProxySet struct { /* private canonical prefixes and revision */ }
func NewTrustedProxySet(values []string) (TrustedProxySet, error)
func (s TrustedProxySet) Enabled() bool
func (s TrustedProxySet) CIDRs() []string
func (s TrustedProxySet) Revision() string

type ResolvedSource struct {
    SourceAddr       netip.Addr
    PeerAddr         netip.Addr
    TrustRevision    string
    ViaTrustedProxy  bool
}

func (s TrustedProxySet) Resolve(remoteAddr string, xForwardedFor []string) (ResolvedSource, error)
```

`CIDRs` returns a copy. `Revision` is lowercase 64-character SHA-256 hex over a domain-separated, length-framed canonical CIDR list; the empty set has a stable non-empty revision. It contains no secret or header data. A zero-value `TrustedProxySet` is invalid; callers construct it with `NewTrustedProxySet`, including for the empty list.

`Resolve` first parses the actual Go `RemoteAddr` with the existing numeric host:port, valid/unzoned, unmapped rules. If that peer is not in the trusted set, all supplied XFF values are ignored without validation and the source is the peer. If the peer is trusted, resolution fails closed unless there is exactly one physical `X-Forwarded-For` header value containing 1 to 64 comma-separated hops and at most 4096 UTF-8 bytes total.

For a trusted peer, each hop is a numeric `netip.Addr` only: optional ASCII SP/HTAB around a comma-delimited token is trimmed, but empty tokens, other whitespace, hostnames, `unknown`, quotes, brackets, ports, zones, invalid addresses, or tokens longer than 64 bytes fail. IPv4-mapped IPv6 is unmapped. Multiple physical header lines fail as ambiguous even if an HTTP stack could concatenate them.

The chain order is the conventional client-to-nearest-proxy order. Starting at the actual socket peer, walk declared hops from right to left. Skip only addresses contained in the explicit trusted set. The first untrusted hop is the effective `SourceAddr`; values farther left are untrusted assertions and are ignored. If every declared hop is trusted, there is no conservative client source and resolution fails closed. A trusted entry with a missing, malformed, ambiguous, oversized, or all-trusted XFF never falls back to the proxy IP.

### Service, persistence, and transaction contract

`Config` gains `TrustedProxyCIDRs []string`. The CLI repeatable flag populates it without comma expansion. `Open` constructs one immutable `TrustedProxySet` before opening workers/listeners and stores it on `App`. Status exposes `features.trusted_proxy_source` as informational `true` only when the canonical set is non-empty; it is not consulted when saving a Key policy.

All employee endpoints resolve source after employee-key authentication and before protocol/model admission. The four model entry families, model catalog, Responses resource create/read/continue/cancel/delete, and any other employee-key route using KEY-02 call the same resolver with `r.Header.Values("X-Forwarded-For")`. They never inspect `Header.Get` for this security decision and never pass `Forwarded` or `X-Real-IP`. Protocol-specific error envelopes remain fixed and redacted.

`employeeAuth` retains `SourceAddr` and adds `SourceTrustRevision string`. Source policy checks always use `SourceAddr`; dispatch invariants also require a valid 64-character trust revision equal to the immutable current `App` revision.

Background creation persists the canonical effective `source_addr`, the Key policy revision, and `source_trust_revision` in `background_task_policy_contexts`. The input fingerprint includes all three. Migration is additive, transactional, marked, restart-safe, validates the exact schema, and backfills existing rows with a distinct legacy sentinel that can never equal a current SHA-256 revision; therefore an old queued task fails closed rather than dispatching under new trust semantics.

Claim validates the stored address, Key policy revision, and trust revision before route preparation. A missing/legacy/mismatched trust revision interrupts the queued task with zero attempt and zero upstream call. The final durable dispatch transaction re-reads the current Key policy, requires the frozen Key policy revision, checks protocol/model/effective source again, and requires the frozen source trust revision to equal the current immutable `App` revision in the same transaction that creates/authorizes the attempt. Tightening Key source policy or restarting with changed trusted proxy CIDRs before dispatch therefore yields zero attempt and zero upstream call. Once dispatch is authorized, settlement uses the original frozen snapshot and is not reclassified by later policy/config changes.

Resource ownership is unchanged: a valid owner still reads/continues with current policy/source checks and may cancel/delete according to the existing lifecycle. Trusted-proxy support does not transfer ownership, make creation headers durable authorization for later requests, or expose the stored source/header chain.

The employee Key UI changes only its explanation. With no trusted proxies it says source is the actual socket peer and forwarding headers are ignored. With trusted proxies configured it says only a request whose actual peer is explicitly trusted can use one XFF chain, that the conservative first-untrusted hop becomes the source, and that this transport feature does not grant a Key access. It must not present a per-Key “trust header” toggle.

### Trusted-proxy acceptance

Tests cover:

- empty/default configuration, direct and untrusted peers ignoring spoofed or malformed headers and preserving the existing PR10 socket-peer behavior;
- valid single and multi-hop chains, right-to-left trust, first-untrusted selection, ignored farther-left spoofing, explicitly trusted loopback/private CIDRs, IPv4, IPv6, mapped IPv4, canonical prefix boundaries, and changed configuration revision;
- multiple header lines, missing header at trusted entry, empty members, whitespace variants, hostname/port/bracket/quote/`unknown`, zones, duplicates where ambiguous, too many hops, overlong tokens/header, all-trusted chains, and invalid startup CIDRs;
- every employee entry family and catalog, protocol-specific failures, no sensitive logging, source-policy tightening, policy/trust revision mismatch at claim and final transaction, zero-attempt/zero-upstream guarantees, restart recovery, concurrent claim, settlement after dispatch, and valid owner read/continue/cancel/delete;
- status/UI wording and the absence of an employee authorization toggle for header trust.

## Integration, verification, and reporting

The integrator is the sole owner of final branches, conflict resolution, combined validation, documentation calibration, pull-request creation/attachment, and merge readiness. In particular, `docs/product-plan.md`, `docs/development-plan.md`, `docs/protocol-conversion-contract.md`, and `docs/preview-contract.md` must stop calling `785f649` or the old Messages/socket-peer increment the “latest source”; tested behavior and planned behavior remain separate.

Each pull request must run `gofmt`, focused package tests, `go test ./...`, `go vet ./...`, `npm test`, `npm run build`, `git diff --check`, documentation link checks, and the repository CI equivalent. The untracked research note `docs/research/membership-next-step.md` is out of scope and must retain SHA-256 `650B116157AC4E5328EA79AC4DDEEDE71159F786132B5D87B531B0AB57F3CCC9`.

After the relevant exact PR head is fixed, workflow G verifies the built binary in a real temporary process using loopback ephemeral ports only. G covers converted Gemini/Responses text and function SSE, including late-identity and terminal-missing-identity cases, clean EOF/failure/cancellation/no-replay/accounting boundaries, and trusted-proxy direct/trusted/malformed/tightening/restart cases. A synthetic identity-bearing Gemini stream proves only this contracted subset and must not be described as general provider stream compatibility. G also runs the smallest actually representable CLI path for experimental Codex credential routing when feasible and labels it precisely as experimental CLI coverage, never as “real membership”. Any unavailable external credential or provider path is reported as untested, not inferred.
