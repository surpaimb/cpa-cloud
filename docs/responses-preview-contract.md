# Responses 与函数工具首批实现契约

2026-09-23。属于完整功能对齐计划的第一批，不代表已达到 Sub2API 功能覆盖。独立实现，不复制参考代码。本契约扩展 preview-contract.md 与 Codex 实验契约，原 Chat Completions 行为继续兼容。

## 对外接口

- 新增 `POST /v1/responses`：同一 Go 进程完成员工 Key 鉴权、撤销/权限检查、模型映射和上游请求；不新增代理进程。输入需合法对象、model 字符串、stream 严格布尔（若提供）。请求体沿用 4 MiB 上限。
- API Key 上游转发同协议 `/responses`，仅替换模型，不静默删除 tools、instructions 等语义字段。复用 SSRF/DNS/TLS/重定向规则与凭据加密；员工头、Cookie 和 Key 不转发。此阶段不实现资源 GET/DELETE、background、WebSocket、跨协议降级或自动重试。
- 非流式必须是合法 Responses response 对象，失败/未完成不能记成功。SSE 按完整事件解析，支持拆包、多行 data、CRLF；仅 `response.completed` 成功。失败、incomplete、error、提前 EOF、超限、取消分别结束，不能使用 Chat Completions 的 `[DONE]` 作为成功依据。所有上游错误正文使用固定脱敏错误替代。工具名称/参数/输出属于模型内容，只交给调用者，不进入日志。
- 成功内容保留原生输出 items、function_call 与 function_call_output 的语义、call_id 和 usage；缺失 usage 不伪造 0。失败换号、用量账单和会话资源持久化仍是后续独立模块。

## Codex 会员适配器约定

- 继续使用默认关闭的 `--experimental-codex-membership`、固定官方 HTTPS 目标、同一加密导入凭据和 revision 状态更新。无真实凭据测试，不实现隐式 OAuth 刷新。
- 新增方法（保留旧 Complete/Stream）：

```go
func (a *CodexDirectAdapter) Responses(ctx context.Context, credential *CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, error)
```

- body 是已映射上游 model 的原生请求。consume 为 nil 时聚合最终 response 对象；非 nil 时回调单个事件 JSON（不包含 SSE framing），同时返回最终 response。回调不得缓存秘密用于日志。error 复用 CodexAdapterError，错误不含原文。服务侧新增独立可注入 Responses executor，不要求旧 chat mock 实现新增方法。
- 会员首批支持 input 字符串、user/assistant/system/developer 文本消息及 instructions、function 工具定义、function_call 历史与字符串 function_call_output；tool_choice auto/none/required/指定 function、parallel_tool_calls；依据固定官方协议实现 reasoning 和 text 配置的已验证子集。
- 必须校验形状和类型，未知或未实现的输入明确 unsupported_feature，不静默丢字段。image/audio、托管工具、custom tools、previous_response_id/服务端会话、background、store=true 暂不支持，返回明确错误。reasoning 历史只在明确可表达时接收，不得删除后伪装支持。
- 上游始终 stream=true、store=false；省略参数所加默认值要列入实现说明。终止对象和工具事件完整保留，不转换成纯文本。未知事件不允许冒充完成；失败事件不能透传原始错误。
- 服务层在成功完成后才标 verified；过期或 401 标 reauth_required，revision 竞争规则不变。流式成功完成事件须与状态持久化顺序协调，避免先向员工宣告成功再写失败。断连取消上游。

## 验收与文件分工

1. 适配器：只 internal/membership 与 docs/research/codex-responses-implementation.md；合成凭据、注入 transport 覆盖工具请求/结果回合、非流式/SSE、终止/取消/限额/错误/拒绝能力、日志脱敏和旧聊天回归。
2. 服务：只 internal/service 与必要 cmd；新增入口、API Key 同协议、会员 executor 接线、权限/撤销/开关/模型映射/请求结果状态、取消/失败/SSRF和凭据隔离。不要改 membership 文件。
3. 主任务：进程级独立 smoke、规格/README/集成证据；完整功能矩阵另有文档任务。所有提交只包含自身路径；不打 tag、不全平台打包、不改真实实例。

## 官方协议来源

- [Function calling](https://developers.openai.com/api/docs/guides/function-calling)：function 定义、call_id、function_call_output、参数增量事件。
- [Streaming responses](https://developers.openai.com/api/docs/guides/streaming-responses)：Responses 事件与完成语义。
- [固定 Codex 协议基线](research/codex-direct-protocol.md)：官方源码 commit 44b857c00e5803adedbc5b2e94c4a33574a157fe 的直接端点、输入 item 类型与 SSE。

完整 API reference 页面本次抓取过大失败，不声称逐字段全部支持。请求与响应子集需要在实现说明中列明并由自编测试约束。
