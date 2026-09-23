# Upstream batch import contract

Status: implementation contract for the development preview.

## Endpoint and authorization

`POST /admin/api/v1/upstreams/batch-import` uses the existing administrator
session cookie, required same-origin `Origin`, and `X-CSRF-Token` checks. The
route is registered by the service integration layer as a write route.

The request body is limited to 8 MiB:

```json
{
  "operation_id": "550e8400-e29b-41d4-a716-446655440000",
  "items": [{
    "item_id": "finance-openai",
    "name": "Finance OpenAI",
    "provider_kind": "openai-compatible",
    "endpoint": "https://gateway.example/v1",
    "api_key": "secret",
    "auth_json": ""
  }]
}
```

`operation_id` must be a UUID, `item_id` must be 1-120 characters, and the
array must contain 1-100 items with unique `item_id` values. Unknown fields,
duplicate `item_id` values, malformed top-level JSON, invalid operation IDs,
and item-count violations return `400` before any database write.

Supported item shapes are:

| `provider_kind` | credential | endpoint |
| --- | --- | --- |
| `openai-compatible` | non-empty `api_key`; no `auth_json` | required and validated by the existing endpoint policy |
| `anthropic-api-key` | non-empty `api_key`; no `auth_json` | required and validated by the existing endpoint policy |
| `gemini-api-key` | non-empty `api_key`; no `auth_json` | optional; empty means the fixed Google Gemini API endpoint |
| `codex-membership` | non-empty `auth_json`; no `api_key` | omitted; fixed to the observed ChatGPT Codex endpoint |

Codex items are accepted only when the existing experimental Codex membership
flag is enabled. Import validates the authorization file structure and local
schedulability, encrypts it, and marks it `imported_unverified`. Batch import
does not contact any upstream service, read a server-side file path, create an
OAuth binding, claim online authentication, discover models, or create model
routes.

Item validation failures do not abort valid siblings. A handled request returns
`200`:

```json
{
  "items": [
    {"item_id":"finance-openai","status":"created","upstream_id":"ups_..."},
    {"item_id":"prior","status":"existing","upstream_id":"ups_..."},
    {"item_id":"bad","status":"failed","error_code":"invalid_endpoint"}
  ]
}
```

The result order matches request order. `200` means every item was classified;
it does not mean every item was created. Stable item error codes are
`invalid_item`, `unsupported_provider`, `invalid_endpoint`,
`invalid_credential`, `invalid_codex_auth`, `feature_disabled`,
`service_unavailable`, and `storage_unavailable`. Results and errors never
contain credential values or raw parser/database/provider text.

## Idempotency and transactions

The database table `upstream_batch_items` is created by the idempotent
`migrateUpstreamBatchItems` migration. Its primary key is
`(operation_id,item_id)`. A keyed digest binds the normalized non-secret fields
and the supplied credential bytes without storing their plaintext.

For a valid item, upstream creation and its idempotency row commit in one SQLite
transaction. Repeating the same operation/item/input returns `existing` with
the original upstream ID. Reusing a recorded operation/item with different
input returns top-level `409 operation_conflict` before processing any new
items in that request. Concurrent identical requests converge on one upstream;
the loser re-reads the committed record.

Failed validation, encryption, transaction start, insert, or commit is not
written as an idempotency result. A transient failure can therefore be retried.
Successfully committed siblings remain idempotent across later item failures,
process restart, and request retry. The API deliberately provides per-item
transactions rather than all-or-nothing batch creation so mixed valid and
invalid input can make bounded progress.

The table stores only operation ID, item ID, keyed digest, upstream ID, and
creation time. API keys and authorization JSON exist only in the encrypted
`upstreams.credential_ciphertext` column. Batch import never inserts into
`models`, `codex_oauth_bindings`, or authorization-session tables.

## Integration points

The shared service files must add exactly these calls:

```go
// store.initialize, after the upstream schema and its membership migration exist
if err := s.migrateUpstreamBatchItems(ctx); err != nil {
    return fmt.Errorf("migrate upstream batch items: %w", err)
}

// App.Handler, alongside the other upstream administration routes
mux.HandleFunc("POST /admin/api/v1/upstreams/batch-import",
    a.requireAdmin(a.batchImportUpstreams, true))
```

No other initialization, route, permission, or model-routing change is needed.

## Implementation provenance

This contract and its implementation are independently authored from the
repository's preview contracts and existing local storage, encryption,
endpoint-validation, and Codex credential interfaces. The batch endpoint adds
no third-party dependency and does not implement or invoke a provider network
protocol.
