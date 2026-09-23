# Codex membership model catalog: observed wire contract

Status: implementation evidence, pinned 2026-09-23. This note describes an
observable protocol in OpenAI's public Codex source. It is not a claim that the
endpoint is a separately supported public API or that its wire contract is
stable.

## Evidence boundary

All links below point to OpenAI's `openai/codex` repository at commit
[`44b857c00e5803adedbc5b2e94c4a33574a157fe`](https://github.com/openai/codex/tree/44b857c00e5803adedbc5b2e94c4a33574a157fe).

- [`CHATGPT_CODEX_BASE_URL`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider-info/src/lib.rs#L77)
  fixes the ChatGPT Codex base at `https://chatgpt.com/backend-api/codex`.
- [`ModelsClient`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/endpoint/models.rs#L24-L51)
  issues `GET models` and appends `client_version=<whole semantic version>`.
- The OpenAI provider contributes the request header
  [`version: <client version>`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider-info/src/lib.rs#L512-L529).
- ChatGPT bearer auth contributes
  [`Authorization: Bearer ...` and, when present, `ChatGPT-Account-ID`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/bearer_auth_provider.rs#L31-L45).
- [`ModelsResponse` and `ModelInfo`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/protocol/src/openai_models.rs#L402-L511)
  define the response wrapper and model metadata. The wrapper contains only a
  `models` array; it has no cursor, page token, or continuation field.
- The official client bounds refresh to
  [five seconds](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/models_endpoint.rs#L43-L46).

## Request

```http
GET /backend-api/codex/models?client_version=0.0.0 HTTP/1.1
Host: chatgpt.com
Authorization: Bearer <access-token>
ChatGPT-Account-ID: <account-id>   # only when the imported credential has one
version: 0.0.0
Accept: application/json
```

`client_version` and `version` are supplied by the caller of this package and
must be a whole `major.minor.patch` version. The implementation fixes scheme,
host, and path; it does not accept a caller-controlled endpoint. It follows no
redirects and its production transport does not use process proxy settings.
There is no request body and no catalog pagination request parameter in the
pinned source.

## Response subset exposed by CPA Cloud

Successful status is `200` with JSON shaped as:

```json
{
  "models": [{
    "slug": "example-model",
    "display_name": "Example model",
    "description": "...",
    "visibility": "list",
    "supported_in_api": true,
    "default_reasoning_level": "medium",
    "supported_reasoning_levels": [
      {"effort": "medium", "description": "..."}
    ],
    "input_modalities": ["text", "image"],
    "context_window": 272000,
    "supports_search_tool": false,
    "support_verbosity": false,
    "supports_image_detail_original": false,
    "experimental_supported_tools": []
  }]
}
```

The client returns `slug` as the model ID and only forwards capability fields
that the server actually sent. Optional booleans and numbers use pointers in
the Go result so an omitted field is not silently reported as `false` or zero.
It does not infer tool calling, image support, API eligibility, or picker
visibility from a model name. `visibility` values observed in the schema are
`list`, `hide`, and `none`; the package returns all catalog entries and leaves
product filtering to its caller.

The source models missing `input_modalities` as a legacy default of text plus
image. This client deliberately preserves absence instead because its public
result is evidence data rather than a recreation of Codex picker behavior.

The endpoint may return an `ETag`; it is useful for catalog refresh detection,
but the pinned request does not send pagination or conditional-request fields.
This module returns a bounded ETag value as metadata and performs one request.

## Defensive local limits and errors

The following are CPA Cloud limits, not claims about provider limits:

- one five-second deadline covers transport and body reading;
- at most 1 MiB of response JSON and 512 model items;
- redirects are rejected, including redirects back to the same host;
- `401`, `403`, and `429` have distinct stable error codes;
- timeouts, oversized bodies, oversized catalogs, malformed JSON, unexpected
  status, and invalid catalog fields have stable redacted errors;
- response bodies, request URLs with query values, tokens, account IDs, and
  upstream error text are never included in returned errors.

Tests use an injected `http.RoundTripper` and synthetic credentials. They assert
the fixed target, headers and query, exact optional-field behavior, ETag,
redirect rejection, status mapping, deadline, byte and item limits, malformed
payload handling, and absence of credential text from error strings.
