# Codex 原生 Responses 与 function tools 实现说明

状态：开发预览；2026-09-23。实现基于本仓库固定的官方 Codex 协议基线 `44b857c00e5803adedbc5b2e94c4a33574a157fe`，并参考 OpenAI 官方 [Function calling](https://developers.openai.com/api/docs/guides/function-calling) 与 [Streaming Responses](https://developers.openai.com/api/docs/guides/streaming-responses) 文档。测试只使用合成凭据和本地 TLS mock transport，没有访问真实账号、Token 或参考项目实现。

## 请求支持范围

`CodexDirectAdapter.Responses` 接收已经完成模型映射的 Responses JSON。它严格要求顶层对象与非空 `model`，拒绝未知顶层字段，并始终向固定 Codex HTTPS `/responses` 发送 `stream:true`、`store:false`。客户端省略这两个字段时也应用这两个值；客户端给出 `stream` 时只验证布尔类型，给出 `store:true` 时明确拒绝。

当前接受：

- `input` 字符串，转换为单个 user `input_text` message；或原生 input item 数组。
- user、assistant、system、developer 文本 message；content 仅限 `input_text`、`output_text`，保留已知的 `annotations` 数组。也接受官方常见简写 `{role,content:"text"}`，补为 message 和对应的 input/output text part。
- function 工具定义的 `name`、`description`、`parameters`、`strict`，不改写 JSON Schema。
- `function_call` 历史的 `call_id`、`name`、字符串 `arguments`；`function_call_output` 的 `call_id` 和字符串 `output`，包括合法的空字符串结果。
- `tool_choice` 的 `auto`、`none`、`required` 或 `{type:"function",name}`，以及布尔 `parallel_tool_calls`。
- `reasoning.effort` 的 `minimal`、`low`、`medium`、`high`、`xhigh`；`reasoning.summary` 的 `auto`、`concise`、`detailed`。
- `text.verbosity` 的 `low`、`medium`、`high`。
- 可回放 reasoning history item：必须含非空字符串 `encrypted_content`；可保留 `id`、`status` 以及由 `{type,text}` 组成的 `summary`/`content`。`include` 仅接受 `reasoning.encrypted_content`，用于取得下一工具回合所需的 opaque reasoning item。

当前明确拒绝 image/audio、托管工具、custom tools、`previous_response_id`、conversation/服务端会话、background、`store:true`，以及未列出的请求字段。结构化文本 format 尚未完成 Codex 端逐字段验证，因此 `text.format` 也拒绝。reasoning item 没有 `encrypted_content` 时不假定可安全重放；调用方应在首回合请求 `include:["reasoning.encrypted_content"]` 并把完整 reasoning output item 连同 function call 和 function output 放入下一回合 input。本适配器不是完整 Codex CLI agent 编排器。

## 响应与失败规则

上游响应必须是 `text/event-stream`。解析器按空行分隔完整事件，支持任意读取拆包、多行 `data:`、LF/CRLF、注释行以及既有响应、单行和单事件大小限制。原生 callback 得到每个完整事件的 JSON，不含 `event:`/`data:` framing；内容与工具参数只交给调用者，不写日志。仅 `response.completed` 成功，其 `response` 对象作为方法返回值，完整保留 output items、function call、call_id、arguments、usage 和未知响应字段。

`response.failed`、`response.incomplete`、`error`、`[DONE]`、提前 EOF、取消、超时和超限均返回 `CodexAdapterError`。失败终止事件不回调，从而不会把原始上游错误正文交给员工；错误只保留既有 allowlist 中的 provider code。已在失败前回调的正常增量不能被当作成功结果。生产传输继续使用固定 HTTPS 目标、TLS 1.2 下限、禁环境代理、禁重定向、Bearer/account credential 本地验证和零自动重试。

## 自动验证

自编测试覆盖函数工具请求和完整输出、带 encrypted reasoning 的工具结果回合、字符串 input 默认映射、SSE 多行与 CRLF、completed/failed/incomplete/error/EOF/`[DONE]`、消费者停止、取消、超大行、未知请求字段与未支持能力在网络前拒绝，以及失败消息不泄漏上游正文。旧 `Complete`/`Stream` 测试继续作为回归套件运行。
