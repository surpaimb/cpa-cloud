# Claude Messages 独立实现契约

状态：开发预览，2026-09-23。本契约先于实现编写，仅使用 Anthropic 公开文档和本项目独立测试作为协议依据。

## 本批交付边界

- 新增上游类型 `anthropic-api-key`，只表示 Claude Console 发放的 Anthropic API Key，不表示 Claude Pro、Max、Team 或 Enterprise 会员凭据。
- 员工调用 `POST /v1/messages` 和 `POST /v1/messages/count_tokens`；员工权限、Key 撤销、模型目录和路由沿用现有同进程实现。
- 客户端可用 `Authorization: Bearer cpac_...` 或单一 `x-api-key: cpac_...` 传递员工 Key；同时出现、重复或格式非法时统一拒绝。员工凭据永不转发上游。
- 上游 URL 由管理员配置的受 SSRF/TLS 规则约束的 base endpoint 派生：`/v1/messages` 和 `/v1/messages/count_tokens`。官方默认为 `https://api.anthropic.com`。
- 上游认证使用当前官方文档首选的 `Authorization: Bearer <Anthropic API Key>`；同时发送 `anthropic-version`。入站的 `x-api-key`/`Authorization`不会被复制。
- 请求体作为 Anthropic JSON 原生负载处理，只把对外模型 ID 替换成路由中的上游模型 ID。`system`、`messages`、`tools`、`tool_choice`、`tool_use`、`tool_result`、`thinking`、签名/加密思考块与其他未解析字段保持 JSON 语义，不做跨协议转换。
- `anthropic-version` 由客户端给出并转发；开发预览要求 `YYYY-MM-DD` 格式。`anthropic-beta` 可选转发，使用有界可打印 ASCII 校验。不转发其他客户端头。
- 本批未在上游记录中增加 `anthropic-workspace-id`，因此只能使用已绑定单一 Workspace、可省略该头的 API Key；多 Workspace Key 需等管理员可配置的工作区字段与权限模型实现后才支持。

## 响应、流和状态

- 非流式成功响应在有界大小内保留 Anthropic JSON 响应，包括 `content` 中的文本、工具使用和思考块，以及上游提供的 `usage`。
- 原生 `stream:true` 只接受 `text/event-stream`，按既有白名单与脱敏规则转发。显式 Messages↔Responses wire 另支持严格文本/function 子集；thinking、签名、cache-control、媒体和未知事件不做跨协议转换。Responses→Messages 若 usage 直到终态才完整可知，会在既有上限内有界暂存并于终态后整体释放，不属于上游生成期间实时。
- 看到 `message_stop` 才记为流式成功；看到 `event: error` 记为失败；上游 EOF 时未看到终止事件记为 `interrupted`；客户端断开会取消上游 context 并记为 `cancelled`。不合成伪 `message_stop`。
- 在尚未发送 HTTP 200 时，本地和上游错误返回 Anthropic 风格的脱敏 error envelope，不回显上游正文或凭据。已经开始 SSE 后，上游 `error` 事件也替换为固定的本地 Anthropic error 事件并记录失败；不转发上游错误正文、错误类型或其他非白名单细节。
- `count_tokens` 是独立调用，它不生成 Message，不记为成功模型生成请求，不将未知用量填成 0。

## 安全与可验收行为

- API Key 使用现有 AEAD 加密落盘，AAD 绑定上游 ID；列表、错误、请求记录和网页不返回明文或密文。
- 禁止跟随上游重定向，保留 TLS 校验，默认禁止回环/内网地址；仅测试配置可允许字面回环 HTTP。
- mock 上游验收覆盖：两种员工认证形式、模型替换、请求字段保留、员工 Key 不泄露、工具/思考 SSE、正常终止、中断、上游错误、取消、员工撤销、模型权限、重启恢复和 token counting。
- 本批不使用真实 Anthropic 账号或凭据；mock 通过不等于真实上游兼容验证。

## Claude 订阅/会员边界

Anthropic 公开资料确认 Claude Code 可使用 Claude App Pro/Max 账号登录，但帮助中心同时明确：付费 Claude 订阅不包含 Claude API/Console 访问；为他人构建产品时应使用 Console API Key 或受支持的云平台。本批因此不实现、不声明 Claude 会员池路由。

若将来获得适用于第三方服务的官方授权依据，预留的独立凭据类型至少需要冻结并验证以下字段，在此之前不入库：

- 授权方式、客户端身份与回调/设备流程；
- access token 类型、有效期、授权范围、受众与发行方；
- refresh token 是否存在、轮换/重放规则与失效错误；
- 账号/组织/订阅选择标识以及可用模型发现方式；
- 请求端点、必需头、客户端识别要求、限额/冷却信号；
- 官方允许的第三方代理/内部企业共享边界。

任何实验实现仍必须默认关闭，不扫描本机文件，仅接受管理员主动导入，并对加密、刷新、取消、重启、状态竞争和重新授权做独立验收。

## 官方来源

查阅日期：2026-09-23。

- https://platform.claude.com/docs/en/api/overview
- https://platform.claude.com/docs/en/manage-claude/authentication
- https://platform.claude.com/docs/en/build-with-claude/working-with-messages
- https://platform.claude.com/docs/en/build-with-claude/streaming
- https://platform.claude.com/docs/en/api/messages/count_tokens
- https://platform.claude.com/docs/en/build-with-claude/tool-use/overview
- https://platform.claude.com/docs/en/build-with-claude/thinking
- https://docs.anthropic.com/en/docs/claude-code/getting-started
- https://support.claude.com/en/articles/9876003-i-have-a-paid-claude-subscription-pro-max-team-or-enterprise-plans-why-do-i-have-to-pay-separately-to-use-the-claude-api-and-console
- https://support.claude.com/en/articles/13189465-log-in-to-your-claude-account
