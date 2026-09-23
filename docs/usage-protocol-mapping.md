# Usage protocol mapping

Status: source preview, 2026-09-23. The service coordinator connects this parser
to four-protocol HTTP forwarding and request completion. Forwarders pass only
JSON accepted by their protocol handler and decide terminal status separately.
Pricing configuration, budgets, statistics APIs, and billing remain unimplemented.

## Small API

`accounting.NewUsageAccumulator(protocol)` creates a request-local accumulator.
`Observe(dataJSON)` accepts either a complete non-streaming response object or
one complete SSE `data:` JSON object. `Usage()` returns a defensive copy of the
four nullable ledger counters. `ParseUsage(protocol, fullJSON)` is the one-object
convenience form.

The supported protocol constants are:

- `ProtocolOpenAIChatCompletions`
- `ProtocolOpenAIResponses` (also used by Codex when it speaks Responses)
- `ProtocolAnthropicMessages`
- `ProtocolGeminiGenerateContent`

The accumulator retains only counters and its protocol enum. It does not retain,
return, or log input JSON, generated content, prompts, tool arguments, or error
bodies. It is request-local and is not safe for concurrent calls.

Any malformed JSON, negative/fractional/out-of-range counter, arithmetic
overflow, or contradictory usage relationship returns the fixed
`ErrInvalidUsage`. That error contains no upstream data. The accumulator is then
poisoned and all four counters remain `NULL`; later observations cannot restore
partial data. A missing usage object is not an error and leaves the previous
stream snapshot unchanged.

## Normalized buckets

| Protocol | `InputTokens` | `OutputTokens` | `CacheReadTokens` | `CacheWriteTokens` |
| --- | --- | --- | --- | --- |
| Chat Completions | `prompt_tokens - cached_tokens - cache_write_tokens` | `completion_tokens` | `prompt_tokens_details.cached_tokens` | `prompt_tokens_details.cache_write_tokens` |
| Responses / Codex | `input_tokens - cached_tokens - cache_write_tokens` | `output_tokens` | `input_tokens_details.cached_tokens` | `input_tokens_details.cache_write_tokens` |
| Anthropic Messages | `input_tokens` | `output_tokens` | `cache_read_input_tokens` | `cache_creation_input_tokens` |
| Gemini generateContent | `promptTokenCount - cachedContentTokenCount` | `candidatesTokenCount + thoughtsTokenCount` | `cachedContentTokenCount` | `0` |

All source fields are nullable in this internal contract. A normalized value is
calculated only when every value needed for that calculation is present. For
example, an OpenAI input count without both cache detail counters leaves ordinary
input unknown; it is not treated as an uncached request. A Gemini output can also
be derived as `totalTokenCount - promptTokenCount`; when both derivations are
available they must agree.

OpenAI defines cache reads and cache writes as mutually exclusive input pricing
categories and publishes the ordinary-input calculation above. Its reasoning
tokens are already included in the reported output count, so they are validated
as a subset and are not added again. Anthropic defines its three input fields as
independent parts whose sum is total input, so no subtraction is applied.
Gemini says its prompt count includes cached content and its total is prompt plus
thoughts plus response candidates. Therefore thought tokens are added to
candidates exactly once. Cache creation is a separate Gemini operation rather
than a generateContent usage category, so that one structurally inapplicable
bucket is the only synthesized zero.

Absent counters remain `NULL`, including absent cache read/write fields. This is
intentional: unknown usage is not free usage and must not silently become zero in
the ledger.

## Streaming behavior

OpenAI Chat final usage chunks, Responses terminal events, and Gemini response
chunks are cumulative snapshots. Each accepted usage object replaces the
previous four counters; repeated events are idempotent. Responses
`response.completed` and `response.incomplete` events read usage from their
nested `response` object. Other Responses stream events do not carry an accepted
billing snapshot here.

Anthropic `message_start.message.usage` establishes the snapshot. Each
`message_delta.usage` field overwrites that field because Anthropic documents
the delta usage counts as cumulative. Thus the final output count is not summed
across deltas, while the input/cache fields from `message_start` survive an
output-only delta. Repeated events are idempotent.

Protocol error objects, Anthropic `error` events, and Responses failed/cancelled
events are ignored as usage sources. They leave usage unknown when no earlier
valid snapshot exists and never expose their error body. Attempt status and the
decision to persist any earlier known counters belong to the future forwarding
integration.

## Public protocol sources

The implementation and synthetic tests were independently authored from these
provider documents, accessed 2026-09-23:

- OpenAI, [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching): cached and cache-write token fields, mutually exclusive input pricing, and the published ordinary-input subtraction.
- OpenAI, [Reasoning models](https://developers.openai.com/api/docs/guides/reasoning): reasoning tokens are included in output tokens.
- OpenAI, [Responses API create reference](https://developers.openai.com/api/reference/resources/responses/methods/create): Responses usage object and terminal response shape.
- Anthropic, [Messages API reference](https://platform.claude.com/docs/en/api/typescript/messages): usage fields and the statement that total input is the sum of ordinary, cache-creation, and cache-read tokens.
- Anthropic, [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming): event sequence and cumulative `message_delta.usage` counters.
- Google, [Generate content API / UsageMetadata](https://ai.google.dev/api/generate-content): prompt count includes cached content; total count is prompt plus thoughts plus candidates.
- Google, [Gemini thinking](https://ai.google.dev/gemini-api/docs/generate-content/thinking): output pricing uses response and thought tokens as separate counts.

No provider SDK, reference CPA implementation, real credential, captured user
traffic, or live upstream request was used.
