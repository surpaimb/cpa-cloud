# CPA Cloud working rules

This is a new independent implementation, now beginning the development preview. Read docs/product-plan.md, docs/independent-implementation.md, docs/development-plan.md and docs/preview-contract.md before implementation.

- Implement from our functional specifications, public protocol documentation, and independently authored tests.
- Do not copy, translate, paraphrase line by line, or port CLIProxyAPI, Sub2API, or archived CPA implementation code, tests, migrations, assets, or documentation. Do not import their SDKs into the execution core.
- Do not use ../cpa-cloud-reference or ../cpa as implementation templates. Previously viewed code means this work must not be represented as strict clean-room development.
- Record public protocol sources and third-party dependency licenses. Dependencies retain their own licenses. Newly introduced source needs explicit provenance.
- Target single-tenant internal company use, cloud/LAN/personal-machine deployment on Linux/Windows/macOS, a browser admin console, and standard API access with per-employee keys. Default to loopback; remote access requires TLS. No WeChat/Gate dependency or mandatory desktop client. Keys default to no expiry. Membership account import/authorization for ChatGPT/Codex, Claude and Gemini remains a core goal subject to provider-specific protocol verification.
- Authenticate employee model requests and execute upstream requests in one Go service process. Admin endpoints require separate authorization. No per-request HTTP call to an admin service.
- Store employee keys only as keyed digests; show plaintext only at creation. Use constant-time verification. Encrypt recoverable upstream credentials; define Linux key management and recovery before production deployment.
- Never log keys, auth headers, upstream tokens, prompts, or model responses. Preserve TLS verification. Do not forward employee credentials upstream.
- Test streaming, cancellation, tool calls, revocation, persistence failure, restart recovery, and concurrent usage accounting. Report tested behavior separately from planned behavior.
- Keep desktop config backups, atomic writes, validation and rollback as requirements for a future independently implemented client; they are not already provided here.
- Do not reuse old product IDs, update channels, production infrastructure, or deployment secrets. Do not publish a repository, package, or deployment without authorization.
- For docs-only planning work, verify document links and git diff checks. Add relevant automated tests and build/lint checks when implementation begins.
