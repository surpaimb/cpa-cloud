# 跨协议转换能力契约

状态：D1 实现模块，2026-09-24。本文只描述已进入源码并由合成黄金测试约束的转换能力；HTTP
路由接线、真实供应商验证、有状态 Responses、后台任务和托管工具不因本模块存在而完成。

实现位于 `internal/protocolconv`。它是纯转换模块，不做网络 I/O、不保存正文、不运行工具，也不改变
员工鉴权、Key 撤销、模型权限、账号池、出站代理、预算或归档规则。服务接线必须在既有准入和持久派发
屏障之后调用它，并且输出开始后不得换号、重放或改换协议。

## D1 已实现能力

| 方向 | 请求 | 非流式响应 | SSE |
| --- | --- | --- | --- |
| Chat Completions → Responses | 文本 message、developer/system/user/assistant、function tools、tool choice、并行工具、assistant tool calls、字符串 tool result | 单 choice 的文本和 function calls；usage 与 cache/reasoning 子计数 | 文本及函数参数增量；生成 Responses item/content/done/completed 事件；只有显式 `[DONE]` 且 finish reason 一致才完成 |
| Responses → Chat Completions | 字符串或文本 message input、instructions、function tools、相邻并行 function calls、字符串 function_call_output | completed response 的文本和 function calls；usage 与 cache/reasoning 子计数 | 严格验证 sequence、item ID、output/content index、累计文本/参数、done item 和 terminal output；只在 `response.completed` 后生成 Chat finish chunk 与 `[DONE]` |

共同支持 `model`、严格布尔 `stream`/`parallel_tool_calls`、正整数 token 上限、范围为 0–2 的
`temperature` 和范围为 0–1 的 `top_p`。Chat 流使用 `stream_options.include_usage=true`；Responses
终态本身携带 usage。总 Token 与输入/输出不一致时拒绝，缺失 usage 保持缺失，不能合成零。

转换器返回固定类别和字段路径，不在错误中带字段值：

- `invalid_request`：调用方形状或取值非法；
- `unsupported_feature`：源字段合法但目标协议不能在本批无损表达；
- `invalid_upstream`：上游响应、事件或终态自相矛盾；
- `interrupted`：Chat 未收到 `[DONE]` 或 Responses 未收到 `response.completed` 就 EOF。

## 明确拒绝的字段和语义

下列能力不静默删除，也不退化成字符串：

- 图片、音频、文件、多模态 content、引用/annotations、logprobs；
- reasoning item、encrypted reasoning、custom tool、hosted tool、MCP、computer、shell、code interpreter、
  file search、web search 和 tool search；
- structured output / response format、`include`、预测内容、音频输出和多候选 `n`；
- Chat 的多个 choices、不能表示的 finish reason；
- Responses 的 `store=true`、`background=true`、`previous_response_id`、`conversation`；
- 任一未列请求字段、未知 output item 或未知 SSE 事件。

`store=false`、`background=false` 和 null 状态引用可通过；转换出的 Responses 请求固定
`store=false`。这只是无状态转换，不是有状态能力的替代。

## 流式终态与执行边界

两方向都先累计最小状态，只保留转换所需的文本、函数名、call ID、参数和 Token 计数；调用者不得把这些
内容写入日志或用量表。Responses → Chat 要求每个终态 output item 已由同 ID/index 的 added/delta/done
序列完整声明，并与 terminal response 全量相等；终态新增、遗漏或改写内容均失败。Chat → Responses
按 output item 首次出现顺序固定 output index，工具先于文本或非顺序 tool index 不会改变终态顺序。

取消由调用者 context 贯穿上游；转换器不会把取消改成成功。失败、incomplete、error 或提前 EOF 不能
生成成功终止事件。已经向客户端发送语义输出后，任何错误只允许结束本次尝试，不得换账号重放。

可靠用量和后台任务接入 `accounting.EventRecorder`：一个员工 HTTP/后台操作只有一个 request 事实，
每次实际派发是 attempt。`MarkAttemptDispatched` 必须在同一事务内完成权限、账号 revision、价格、预算
和 attempt 状态重核并成功提交，随后才允许网络 I/O。提交后崩溃的未知结果恢复为 `interrupted`、usage
未知且不自动重放。

## Gemini CLI 0.61.0 同协议兼容增量

Gemini 原生通路仍是同协议透传，不属于跨协议转换。本批根据合成实际客户端失败样例新增：

- `generationConfig.thinkingConfig.includeThoughts`，只接受布尔值；未知 thinkingConfig 字段在派发前
  返回明确 `UNIMPLEMENTED`；
- `functionDeclarations[].parametersJsonSchema` 对象，保持 JSON Schema 原样；它和旧
  `parameters` 同时出现时因语义不明确返回 `INVALID_ARGUMENT`；
- 合法字段无损转发，测试覆盖未知嵌套字段零上游派发。

这证明 Gemini Developer API Key 原生入口的合成客户端兼容性，不代表 Gemini 会员接入或真实 Google
账号验证。

## 后续交付队列

1. D1 集成：由独立集成任务将纯转换器接入共享路由，复用现有员工 Key、模型权限、池、出站代理、
   归档、预算和 usage 生命周期，并做随机端口进程验收。
2. D2：按 ADR 0003 完成默认关闭的 Responses 资源、`previous_response_id`、读取/删除/取消和后台任务；
   先合迁移、所有权和恢复，再启 worker。
3. D3：仅对管理员白名单且上游官方支持的托管工具透传；不在 CPA Cloud 任意执行用户代码。
4. Messages/Gemini 跨协议：逐字段证明可表达语义后扩表；当前只有各自原生路径，不能用文本转换冒充完成。
5. 图片/音频、reasoning、structured output、conversation 对象和其他 Responses item 另立契约与黄金测试。

## 官方来源

查阅日期：2026-09-24。

- OpenAI，Chat → Responses 迁移和 message/item 映射：
  <https://developers.openai.com/api/docs/guides/migrate-to-responses>
- OpenAI，function call、call ID、工具结果和两类流增量：
  <https://developers.openai.com/api/docs/guides/function-calling>
- OpenAI，Responses 创建字段和 output/usage：
  <https://developers.openai.com/api/reference/resources/responses/methods/create>
- OpenAI，Responses 流事件：
  <https://developers.openai.com/api/docs/guides/streaming-responses>
- Google，GenerateContent、ThinkingConfig、FunctionDeclaration 与 UsageMetadata：
  <https://ai.google.dev/api/generate-content>

实现与测试没有使用 CLIProxyAPI、Sub2API、归档 CPA、真实凭据或捕获的用户正文。

响应侧的字段策略与请求侧不同：请求字段代表调用方要求的语义，未列字段继续拒绝；响应对象中官方定义、
但不改变本批文本/function output 的已知 envelope 元数据可省略；其中会改变无状态语义的关键字段仍会
单独校验：`store`/`background` 只能为 false 或 null，`previous_response_id`/`conversation` 只能为 null，
`error`/`incomplete_details` 在 completed 响应中必须为 null，`completed_at` 不得早于 `created_at`。
output item、content、不可映射的 phase、非空 logprobs/refusal/audio 及未知 SSE 事件仍不能省略或降级，
必须明确失败。
