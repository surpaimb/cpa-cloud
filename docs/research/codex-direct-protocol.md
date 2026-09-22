# Codex ChatGPT 会员直接协议：官方源码证据与实验规格

状态：有界技术证据验证；不是稳定兼容承诺，也不表示已调用真实账号

查阅日期：2026-09-22

固定来源：OpenAI 官方公开仓库 [`openai/codex`](https://github.com/openai/codex)，commit [`44b857c00e5803adedbc5b2e94c4a33574a157fe`](https://github.com/openai/codex/tree/44b857c00e5803adedbc5b2e94c4a33574a157fe)，提交时间 2026-09-22T15:05:27Z。

本轮只读该官方仓库的代码、测试、schema 和官方 Codex 文档。未读取本机或用户凭据，未发起 OAuth、Token 刷新或模型请求，未使用官方 Codex 内置 client_id 发起授权，未查看参考产品、CLIProxyAPI、Sub2API 或旧 CPA 实现。下面的 JSON 均为自行编写的脱敏规格，不复制实现代码。

## 1. 结论

固定 commit 已提供足以实现一个**实验性的、只消费已导入 `auth.json`、单 Go 进程直接请求**的推理协议：

- ChatGPT Codex 基址是 `https://chatgpt.com/backend-api/codex`；HTTP Responses 路径追加 `/responses`。
- Bearer 回退认证使用 `Authorization: Bearer <access_token>` 和 `ChatGPT-Account-ID: <account_id>`。
- 请求体字段、SSE framing、文本 delta、最终 item、完成、失败和 usage 结构均可从官方类型与测试确定。
- `401` 后官方客户端会尝试认证恢复并重试；普通 OAuth 刷新是 `POST https://auth.openai.com/oauth/token` 的 JSON 请求。

这比“没有直接 HTTP 技术证据”更进一步：**短期 access token 的直接推理实验可以独立实现和用假上游自动测试。**仍未闭合的是持续运行所需的认证所有权和当前 Agent Identity 路径：

1. OAuth 刷新体必须包含签发该 refresh token 的 `client_id`。源码含官方 Codex 自己使用的 client_id 选择逻辑，但没有给 CPA 注册或拥有 client_id 的协议；本项目不应复制或借用官方 client_id 发起授权。
2. 当前源码对 ChatGPT auth 可先尝试注册 Agent Identity，并用 `Authorization: AgentAssertion ...`；Bearer 是注册不可用时的 session fallback。公开源码给出了行为，但 Agent Identity 的注册、密钥生命周期和服务端稳定保证不是本文准备独立实现的最小协议。
3. `chatgpt.com/backend-api/codex` 是官方客户端源码中可观察到的后端，不等于 OpenAI 面向任意第三方承诺的稳定公共 API。端点、字段、头和认证策略可能随 Codex commit 变化。

因此最小实验可闭环“导入有效 auth.json → 列模型/选择已知模型 → 单次 Responses SSE → 输出/usage”；不能闭环“CPA 自己授权新账号并永久自动刷新”。后者的具体缺失不是推理请求字段，而是 CPA 自有 OAuth client registration 与相应授权/刷新保证。

## 2. 证据等级

本文给每一项标注：

- **S（公开稳定文档）**：官方文档明确面向集成者说明。
- **O（官方源码可观察协议）**：固定 commit 的生产代码或官方测试明确使用，但没有独立稳定性承诺。
- **H（实验假设）**：由多个 O 级证据组合出的最小实现，需要隔离账号实测。

直接 HTTP 细节主要是 O；App Server JSON-RPC 才有 S 级产品嵌入文档。不能把 O 级写成永久兼容，但也不应把它说成无法独立实现。

## 3. 请求端点和认证头

### 3.1 推理端点

O 级证据：

- [`model-provider-info/src/lib.rs#L74-L77`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider-info/src/lib.rs#L74-L77) 定义 `CHATGPT_CODEX_BASE_URL = https://chatgpt.com/backend-api/codex`。
- [`codex-api/src/endpoint/responses.rs#L76-L86`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/endpoint/responses.rs#L76-L86) 对 provider 的 `/responses` 发 POST，并要求 `Accept: text/event-stream`。
- [`codex-api/src/provider.rs#L52-L60`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/provider.rs#L52-L60) 以单个 `/` 拼接 base URL 与 path。

组合后的 URL：

```text
POST https://chatgpt.com/backend-api/codex/responses
```

模型目录的 O 级证据包括官方测试使用的：

```text
GET https://chatgpt.com/backend-api/codex/models
```

来源：[`codex-api/src/api_bridge_tests.rs#L596`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/api_bridge_tests.rs#L596)。模型响应的完整稳定性应另由固定 commit 的模型目录类型和假服务器测试约束，不能仅凭这个 URL 宣称所有字段稳定。

### 3.2 Bearer 回退头

O 级证据：[`model-provider/src/bearer_auth_provider.rs#L33-L44`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/bearer_auth_provider.rs#L33-L44)。

最小脱敏请求头：

```http
Authorization: Bearer <auth.json.tokens.access_token>
ChatGPT-Account-ID: <auth.json.tokens.account_id>
Accept: text/event-stream
Content-Type: application/json
```

官方 provider 还注入 `version: <Codex crate version>`，见 [`model-provider-info/src/lib.rs#L512-L529`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider-info/src/lib.rs#L512-L529)。会话层可附加：

```http
session-id: <opaque session id>
thread-id: <opaque thread id>
x-client-request-id: <thread id>
originator: <client originator>
```

`session-id`/`thread-id` 来自 [`codex-api/src/requests/headers.rs#L5-L13`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/requests/headers.rs#L5-L13)；`x-client-request-id` 来自 Responses endpoint；`originator` 在 core client 中加入。首个实验应把前四个头分为：认证两项必需候选，Accept/Content-Type 传输必需；会话、版本和 originator 用官方假服务器做 A/B，服务端拒绝时再升为必需。不得猜造设备、浏览器 Cookie 或网页 Session 头。

FedRAMP 账号还会加 `X-OpenAI-Fedramp: true`。普通实验不应从未知 JWT 猜测该状态；无法确定时拒绝导入该账号类型。

### 3.3 当前 Agent Identity 分支

固定 commit 不总是只发 Bearer。O 级证据：

- [`model-provider/src/auth.rs`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/auth.rs) 的 ChatGPT auth 路径可以先 bootstrap Agent Identity。
- 同文件测试 `chatgpt_bootstrap_unavailable_uses_session_bearer_fallback` 明确验证注册失败后回退到 Bearer + Account ID。
- Agent Identity 成功时使用 `Authorization: AgentAssertion <signed assertion>`、`ChatGPT-Account-ID`，而不是 access token Bearer。

这意味着 Bearer 直连有官方回退路径证据，但“所有账号/所有时刻都接受 Bearer”仍是 H 级，需要真实临时账号验证。首个直接适配器不应实现 Agent Identity；收到表明需要该机制的响应时返回 `codex_auth_mode_unsupported`，而不是伪造 assertion 或私钥。

## 4. Responses 请求体

O 级权威类型：[`codex-api/src/common.rs#L259-L285`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/common.rs#L259-L285)。输入 item 类型来自 [`protocol/src/models.rs#L1009`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/protocol/src/models.rs#L1009)，content 类型来自 [`protocol/src/models.rs#L876-L895`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/protocol/src/models.rs#L876-L895)。

### 4.1 最小文本实验体

```json
{
  "model": "<model selected from verified catalog>",
  "instructions": "",
  "input": [
    {
      "type": "message",
      "role": "user",
      "content": [
        {"type": "input_text", "text": "<user text>"}
      ]
    }
  ],
  "tool_choice": "auto",
  "parallel_tool_calls": false,
  "reasoning": null,
  "store": false,
  "stream": true,
  "include": [],
  "prompt_cache_key": "<random opaque thread id>",
  "access_programs": null
}
```

注意：官方 struct 对 `tools=None`、`stream_options=None`、`service_tier=None`、`text=None` 和 `client_metadata=None` 会省略字段；`reasoning` 与 `access_programs` 在固定 commit 没有相同的省略标注，因此序列化为 `null`。实现应按该 commit 的实际 JSON 快照写测试，而不是用“更简洁”的自创 body。

### 4.2 CC Switch `/v1/chat/completions` 最小映射

可先实现并明确标为实验的子集：

| Chat Completions 输入 | Codex Responses 输入 |
| --- | --- |
| `model` | `model`，必须先在账号模型目录验证 |
| 单个或多个 `user`/`assistant` 文本 message | 按顺序转成 `type=message`、同 role、`input_text`/历史 assistant 对应官方 ResponseItem 能表达的内容 |
| `stream=true` | 固定 `stream=true`，转发 SSE delta |
| `stream=false` | 仍向上游流式读取，服务端聚合到终止事件后返回一个响应 |

首个子集明确拒绝 `tools`、`tool_choice`（非默认值）、客户端 tool result、图片、音频、`response_format`、任意 system/developer 角色、logprobs、seed 和未知字段。原因不是这些永远不可实现，而是当前尚未完成逐字段语义验证。返回 400 `unsupported_feature`，不得删除字段后继续。

官方 Codex 请求会携带自己的 instructions、工具和推理策略；如果直接适配器仅转发标准聊天文本，它不应假装等价于完整 Codex Agent。反过来，如果复刻 Codex 的系统 prompt 和工具编排，就超出本文的直接模型适配范围。

## 5. SSE 响应

传输为标准 SSE 块：

```text
event: <event type>\n
data: <one JSON object>\n
\n
```

官方 parser 主要按 `data` JSON 中的 `type` 分派。O 级来源：[`codex-api/src/sse/responses.rs#L353-L511`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/sse/responses.rs#L353-L511)。

最小成功夹具：

```text
event: response.created
data: {"type":"response.created","response":{"id":"resp_test"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_test","delta":"hello"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_test","usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":4}}}

```

处理规则：

- `response.output_text.delta.delta` 追加为文本；对 Chat Completions SSE 输出 `choices[0].delta.content`。
- `response.output_item.done.item` 是 item 最终结构；message content 可校验聚合文本，函数/自定义工具 item 在首个子集返回明确不支持。
- 只有 `response.completed` 才算成功终止；EOF、超时或连接错误先于它时结果失败，不能把已收到的部分文本记成成功。
- `response.completed.response.usage` 可映射 input/output/total/cached/reasoning token；字段缺失保持 unknown，不填零冒充。
- `response.incomplete` 是失败终止，其 reason 来自 `response.incomplete_details.reason`。
- `response.failed` 是失败终止；错误位于 `response.error`，至少保留 HTTP 状态、脱敏 `code`、固定本地 message 和可选 retry-after，不把原始错误体直接回传员工。
- 未知事件忽略内容并计数；未知事件不能替代 `response.completed`。

官方测试还覆盖 commentary/final phase、reasoning summary、custom tool input delta、web search 等事件。这些足以以后扩展，但首个标准聊天子集不应暴露未验证的工具语义。

## 6. OAuth 刷新

### 6.1 可观察请求

O 级来源：

- endpoint：[`login/src/auth/manager.rs#L203-L216`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/auth/manager.rs#L203-L216)；
- refresh 调用：[`login/src/auth/manager.rs#L1609-L1633`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/auth/manager.rs#L1609-L1633)；
- JSON 编码：[`login/src/oauth/client.rs#L76-L125`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/oauth/client.rs#L76-L125)。

脱敏 wire 规格：

```http
POST https://auth.openai.com/oauth/token
Content-Type: application/json

{
  "grant_type": "refresh_token",
  "client_id": "<the client that originally received this refresh token>",
  "refresh_token": "<auth.json.tokens.refresh_token>"
}
```

成功响应至少被官方代码读取为：

```json
{
  "id_token": "<new id token>",
  "access_token": "<new access token>",
  "refresh_token": "<rotated refresh token>"
}
```

官方客户端在 access token JWT 距到期不超过 5 分钟时主动刷新；无法解析过期时间时，还以 `last_refresh` 超过 8 天作为后备判断，见 [`login/src/auth/manager.rs#L2963-L2976`](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/auth/manager.rs#L2963-L2976)。401 路径也会触发一次恢复与重试。

### 6.2 真正缺失的字段/权利边界

刷新请求的 wire 字段已经齐全；缺的不是 JSON schema，而是 CPA 可合法、稳定使用的 `client_id` 和对应 OAuth 客户端身份：

- `auth.json` 没有保证携带签发它的 client_id。
- 固定 commit 的 Codex 源码有官方默认 client_id 和覆盖逻辑；那是官方客户端实现细节，不是 CPA 自有 client registration。
- 本任务明确不使用他人的 client_id 发起授权。本报告也不建议把该常量复制进实现。
- 即使对已导入 refresh token 发送刷新在技术上可能成功，它仍把 CPA 绑定到官方客户端未承诺的身份和行为；应作为未授权实验项关闭，而不是悄悄尝试。

因此，第一阶段直接适配器的具体行为应是：

1. 只消费导入时有效的 access token 和 account id。
2. 根据 access token 的 `exp` 做本地到期预警；不把 JWT claim 当作服务端授权证明。
3. 到期前停止分配新请求，状态变为 `reauth_required`；管理员重新导入由官方 Codex 刷新后的完整 auth.json。
4. 401 只重试零次或在确认磁盘凭据版本已被另一个受信刷新器更新后重试一次；不能拿同一失效 token 循环重试。

这能验证直接推理协议，但不是无人值守生产闭环。若以后 OpenAI 提供第三方 client registration，或公开允许使用某个 client_id 和 refresh flow，再补自动刷新。

## 7. 是否足以实现实验性直接适配器

| 能力 | 证据 | 判断 |
| --- | --- | --- |
| 从现有 auth.json 取 access token/account id | 已有独立 parser；官方源码类型和 auth provider 可交叉验证 | 足以做内存凭据对象；不得记录或回显 |
| 构造会员推理 URL/头 | 固定 commit 的生产常量和 auth provider | 足以实现 H 级请求 |
| 文本 Responses 请求 | 官方序列化 struct、输入 item 类型 | 足以实现严格最小 body |
| 文本 SSE | 官方 parser 和测试夹具 | 足以实现增量、最终、失败和 EOF 检测 |
| usage | completed 类型与 parser | 足以读取存在的字段；缺失值保持未知 |
| 模型目录 | 官方测试确认 `/models`，App Server 也公开 `model/list` | URL 足够做实验；直接 HTTP 目录字段还需固定 commit 类型测试 |
| 工具调用 | 官方有丰富 item 类型 | 不能直接等同 CC Switch 工具合同；需另做双向 tool call/result 规格 |
| 自动刷新 | wire body 可观察 | 不足以产品化：缺 CPA 自有/获准 client_id；第一阶段 re-import |
| 所有账号均可 Bearer | 官方有 bearer fallback 测试 | 仍需临时账号验证；Agent Identity 可能成为必需路径 |
| 稳定长期兼容 | 仅固定 commit 源码 | 不足；必须版本钉住、漂移检测和快速关闭开关 |

结论是：可以进入**无真实凭据的实现和假上游测试**，随后在用户明确批准的临时测试账号上做一次受控验证；不需要把“供应商书面确认”设为自动停止条件。生产启用仍需处理刷新和协议漂移，否则只能标为实验性且要求周期性重新导入。

## 8. 后端输入输出与测试格式

建议内部接口使用明确类型，不把 auth.json 原始 JSON传入请求层：

```json
{
  "credential_version": "opaque database revision",
  "access_token": "write-only secret",
  "account_id": "opaque provider account id",
  "expires_at": "RFC3339 or null"
}
```

请求层输出中立事件：

```json
{"type":"response.started","response_id":"resp_test"}
{"type":"text.delta","text":"hello"}
{"type":"usage","input_tokens":3,"cached_input_tokens":0,"output_tokens":1,"reasoning_tokens":0,"total_tokens":4}
{"type":"response.completed","response_id":"resp_test"}
```

失败格式：

```json
{
  "type": "response.failed",
  "category": "unauthorized | rate_limited | usage_exhausted | invalid_request | upstream | protocol | cancelled",
  "provider_code": "<short allowlisted code or null>",
  "retry_after_ms": null
}
```

普通 CI 使用自建 `httptest.Server`，不要连接 `chatgpt.com`：

1. 断言 URL path 为 `/responses`、方法 POST、认证头精确存在，员工 CPA Key 不出现在上游头。
2. 捕获并比较最小请求 JSON；使用结构比较，不依赖对象键顺序。
3. 将第 5 节成功夹具拆成任意 TCP chunk，验证 SSE parser 不依赖 chunk 边界。
4. 覆盖 delta 后 EOF、failed、incomplete、completed 无 usage、未知事件、超大行、空 data、慢流和取消。
5. 401 时断言不会向日志写 token/请求体，也不会用同一凭据无限重试。
6. 伪造即将过期 JWT 只测试调度状态，不测试签名或把 claim 当授权。
7. auth.json 更新使用 revision compare-and-swap；请求期间版本变化时，下一请求取新凭据，当前请求不得混用两版头。
8. 日志扫描查找完整 access/refresh/id token、Authorization、account id、prompt 和 response 文本，结果必须为空。

受控真实验证（不属于普通 CI）只需要回答以下具体问题：Bearer + Account ID 是否对该测试账号接受；最小 body 是否接受；模型目录实际 schema；是否强制 Agent Identity；401/额度耗尽的状态与安全错误头。测试捕获只能保存字段名、状态码和脱敏枚举，不能保存 Token、提示或响应。

## 9. 协议漂移防护

- 在代码中记录兼容基线 commit，不宣称跟随任意 `main`。
- 定期只读比较上述官方文件的 blob SHA；变化时将 provider 标为 `needs_review`，不自动吸收新字段。
- 所有直接会员账号有总开关；未知 auth 模式、未知终止事件或连续 401 立即停止该账号。
- 保留 App Server 作为官方行为 oracle：相同固定版本的 schema/假上游测试用于发现请求字段变化，但不复制其实现。
- 管理 UI 显示 `experimental_source_observed_protocol`、基线 commit、最近验证时间、是否支持自动刷新；不能显示“官方公共 API”。

## 10. 来源索引

所有链接均固定到 commit `44b857c00e5803adedbc5b2e94c4a33574a157fe`：

- [ChatGPT Codex base URL 与 provider 配置](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider-info/src/lib.rs)
- [Bearer、Account ID 与 Agent Identity auth](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/auth.rs)
- [Bearer header provider](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/model-provider/src/bearer_auth_provider.rs)
- [Responses HTTP endpoint](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/endpoint/responses.rs)
- [Responses request types](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/common.rs)
- [Response item/content types](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/protocol/src/models.rs)
- [Responses SSE parser 与测试](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/codex-api/src/sse/responses.rs)
- [OAuth refresh orchestration](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/auth/manager.rs)
- [OAuth JSON/form transport](https://github.com/openai/codex/blob/44b857c00e5803adedbc5b2e94c4a33574a157fe/codex-rs/login/src/oauth/client.rs)
- [Codex App Server 稳定嵌入文档](https://learn.chatgpt.com/docs/app-server)
- [Codex auth.json 官方安全与刷新说明](https://learn.chatgpt.com/docs/auth)
