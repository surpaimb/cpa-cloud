# Codex 会员直连适配器实现说明

状态：实验性内部实现；未接公开 API、数据库、调度器或真实会员账号

实现日期：2026-09-22

协议基线：OpenAI 官方 `openai/codex` commit `44b857c00e5803adedbc5b2e94c4a33574a157fe`，具体证据和逐字段出处见 [`codex-direct-protocol.md`](./codex-direct-protocol.md)。本实现依据该固定版本的公开协议规格独立编写，没有复制官方客户端实现。

## 实现范围

代码位于 `internal/membership`：

- `codex_auth.go` 只解析调用方明确提供的 `auth.json` 字节；不会查找 `~/.codex`。
- `codex_adapter.go` 把已导入的 ChatGPT OAuth 凭据候选映射到固定 Codex Responses 请求。
- `codex_sse.go` 解析有界 SSE，并产出中立的 started、text delta、usage、completed、failed/cancelled 事件。

生产构造器只能使用：

```text
POST https://chatgpt.com/backend-api/codex/responses
```

调用方不能传入目标 URL。测试使用包内未导出的构造器注入 `httptest` transport；普通测试和 CI 不访问 `chatgpt.com`。生产 transport 不使用环境代理，禁止跟随重定向，并要求 TLS 1.2 或更高，避免 Bearer token 被转发到其他目标。

实现只接受有 `access_token`、`account_id` 且 JWT `exp` 距当前时间超过五分钟的已导入凭据。`exp` 只用于本地调度，不验证签名，也不代表服务端已授权。缺少账号 ID、无法读取到期时间或即将过期时均在网络请求前失败。适配器不使用 `refresh_token`、不复制官方 client ID、不执行刷新，也不实现 Agent Identity；401 不重试并返回 `reauth_required`，管理员需从已认证的 Codex 客户端重新导入。

## 请求子集

当前接受按顺序排列的 `user`/`assistant` 纯文本消息：用户内容编码为 `input_text`，历史助手内容编码为 `output_text`。请求体固定包含基线中验证过的字段：

- `instructions: ""`
- `tool_choice: "auto"`
- `parallel_tool_calls: false`
- `reasoning: null`
- `store: false`
- `stream: true`
- `include: []`
- 随机 128 位十六进制 `prompt_cache_key`
- `access_programs: null`

`tools`、`stream_options`、`service_tier`、`text`、`client_metadata` 不发送。工具、图片、音频、system/developer prompt、response format、logprobs、seed 和非默认 tool choice 在映射边界显式返回 `unsupported_feature`，不会被静默删除后继续请求。请求体上限为 1 MiB。

## SSE 与终止语义

解析器支持固定基线的最小事件：

- `response.created`
- `response.output_text.delta`
- `response.output_item.done`（仅 assistant `output_text`）
- `response.completed`
- `response.incomplete`
- `response.failed`

只有 `response.completed` 构成成功。先收到的 delta 在 EOF、连接错误、超时、取消、failed 或 incomplete 后不会作为成功结果返回。非流式 `Complete` 仍消费上游 SSE 并在完成后聚合文本；流式 `Stream` 逐个发出中立事件。若上游没有 delta，但提供了受支持的最终 message item，聚合结果使用最终文本。

未知事件只计数和忽略，不能完成请求。`output_item.done` 中的工具或未知 content 明确失败。completed usage 的 input、cached input、output、reasoning output 和 total token 字段都使用可空值；缺失保持 unknown，不伪造为零。负 usage 视为协议错误。

默认边界为：单 SSE 行 1 MiB、单事件 1 MiB、整个响应 8 MiB、请求超时两分钟。调用方 context 的取消和 deadline 直接传播到 HTTP 请求。HTTP redirect 被拒绝；401 零重试；403 只有在短码明确为 Agent Identity 要求时映射为 `auth_mode_unsupported`；429 保留有界 Retry-After 元数据。

## 秘密处理

适配器不记录请求或响应。文本请求、消息、凭据、流事件、聚合结果和适配器错误的默认 `fmt`、JSON 与 `slog` 表示都只含安全元数据；提示文本、生成文本、Bearer token、refresh/id token、账号 ID、上游错误消息和 response ID 只能通过明确命名的访问器或字段读取，错误不会保留原始上游 body 或 transport 错误字符串。上游 provider code 只保留代码内固定列举的短枚举，其他值丢弃。

这些措施不能清除 Go runtime、HTTP 栈或调用方已经复制的字符串。持久化层仍必须负责静态加密、访问控制、轮换与删除；本实现没有新增任何数据库字段。

## 自动测试

`codex_adapter_test.go` 只使用注入 transport 和本地 `httptest`，覆盖：

- 固定 method/path/认证头和最小 JSON 快照，包括省略及 `null` 字段；
- 任意 TCP chunk 下的 SSE delta、最终 item、completed 和 usage；
- completed 缺少 usage 时保持 unknown；
- delta 后 EOF、failed、incomplete、未知事件、空 data 和未知输出 item；
- 缺少 account ID、JWT 无法调度、临近过期以及不支持输入均在联网前失败；
- 401 不重试、redirect 目的端未被访问、调用方取消和适配器超时；
- 单行、单事件和总响应大小限制；
- 默认格式化、JSON 和结构化日志中没有测试凭据、prompt、response 或 response ID 标记。

未执行真实会员请求，也未读取真实凭据。真实账号验证如后续获明确授权，应单独运行、使用临时账号，只保存状态码和脱敏枚举，并继续禁止保存 token、prompt 或 response。

## 明确限制

这是固定官方 commit 的源码可观察协议，不是 OpenAI 面向第三方承诺的稳定公共 API。当前实现没有模型目录校验、自动刷新、Agent Identity、FedRAMP 判断、工具调用、图像/音频或完整 Codex Agent 编排，也未接入 Runner、员工 API、数据库或生产开关。接入前还需要：模型目录和账号状态策略、凭据加密存储与 revision/CAS、协议漂移检测、总开关、受控真实账号验证，以及 CPA 自有或明确获准的 OAuth 刷新身份。
