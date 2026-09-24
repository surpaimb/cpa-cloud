# 协议来源记录

查阅日期：2026-09-24。只记录官方文档，不纳入参考产品实现。

| 来源 | 用途 | 本轮核实情况 |
| --- | --- | --- |
| https://developers.openai.com/api/reference/resources/chat | Chat Completions 协议入口 | 可读取；实现前需逐项冻结请求、流事件与用量字段 |
| https://platform.openai.com/docs/api-reference/responses | Responses 协议入口 | 本轮工具读取因页面过大失败，不视为协议已核实 |
| https://platform.claude.com/docs/en/api/overview | Messages 与 Token Counting 端点 | 2026-09-23 已核实 `POST /v1/messages` 和 `POST /v1/messages/count_tokens` |
| https://platform.claude.com/docs/en/manage-claude/authentication | Anthropic API Key 认证 | 2026-09-23 已核实 Bearer 为当前推荐，`x-api-key` 为兼容方式 |
| https://platform.claude.com/docs/en/build-with-claude/streaming | Messages SSE 事件 | 2026-09-23 已核实事件序列、工具 JSON 增量、思考/签名增量、error 与 `message_stop` |
| https://platform.claude.com/docs/en/api/messages/count_tokens | Token Counting 请求与响应 | 2026-09-23 已核实独立端点及对 messages/tools/images/documents 的计数用途 |
| https://support.claude.com/en/articles/9876003-i-have-a-paid-claude-subscription-pro-max-team-or-enterprise-plans-why-do-i-have-to-pay-separately-to-use-the-claude-api-and-console | Claude 订阅与 API 边界 | 2026-09-23 已核实订阅不包含 Claude API/Console 访问，本批不将 API Key 称为会员支持 |
| https://learn.chatgpt.com/docs/config-file/config-reference | Codex CLI 配置参考 | 2026-09-24 已核实自定义 `model_providers`、`base_url`、`env_key` 和 `wire_api="responses"` |
| https://code.claude.com/docs/en/env-vars | Claude Code 环境变量 | 2026-09-24 已核实 `ANTHROPIC_BASE_URL`、`ANTHROPIC_API_KEY` 及非交互客户端配置边界 |
| https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/configuration.md | Gemini CLI 配置参考 | 2026-09-24 已核实 CLI 配置入口；实际 0.61.0 请求另由真实客户端合成上游脚本固定 |
| https://github.com/google-gemini/gemini-cli/blob/main/docs/get-started/authentication.mdx | Gemini CLI 鉴权参考 | 2026-09-24 已核实 API Key 模式；不据此宣称 Google 会员额度 |
| https://github.com/farion1231/cc-switch/releases | CC Switch 官方发布记录 | 2026-09-24 核对本机 3.20.3；GUI 自动化不可用，未取得配置流程证据 |
| https://sqlite.org/wal.html | 单机 WAL 运维边界 | 已读取；备份不可遗漏活跃 WAL，WAL 不适合网络共享文件系统 |
| https://sqlite.org/backup.html | SQLite 一致在线备份 | 2026-09-24 已核实 online backup API 用于活动 WAL 数据库的一致快照；实现范围见 [加密备份命令行](backup-restore.md) |
| https://www.rfc-editor.org/rfc/rfc7914 | scrypt 密钥派生 | 2026-09-24 已核实参数语义；首批格式固定并限制 `N/r/p`，不接受包提升成本 |
| https://csrc.nist.gov/pubs/sp/800/38/d/final | AES-GCM 认证加密 | 2026-09-24 用于自有备份格式的随机 nonce、密文认证与头部 AAD 设计 |

核心设计中的 API 路径和支持批次是产品目标，不代表所有官方接口字段已经验证。
后续新增来源应记录具体章节、版本/日期、支持子集与独立测试证据；不抄录大段原文。

## 跨协议转换、Responses 状态与工具

查阅日期：2026-09-24。支持矩阵见 [跨协议转换能力契约](protocol-conversion-contract.md)，状态/后台/
工具的实现门禁见 [ADR 0003](adr/0003-responses-state-background-managed-tools.md)。

- Chat/Responses message、item 和工具结果映射：<https://developers.openai.com/api/docs/guides/migrate-to-responses>
- Function tools、call ID、工具结果及流增量：<https://developers.openai.com/api/docs/guides/function-calling>
- Responses create/output/usage：<https://developers.openai.com/api/reference/resources/responses/methods/create>
- Responses SSE：<https://developers.openai.com/api/docs/guides/streaming-responses>
- Conversation state 与 previous response：<https://developers.openai.com/api/docs/guides/conversation-state>
- Background 轮询、取消和恢复流：<https://developers.openai.com/api/docs/guides/background>
- 托管工具总览：<https://developers.openai.com/api/docs/guides/tools>
- Anthropic Messages 的 `tool_use`、`tool_result` 与客户端执行回合：<https://platform.claude.com/docs/en/agents-and-tools/tool-use/how-tool-use-works>
- Gemini `functionDeclarations`、`functionCall`、`functionResponse` 与多回合函数调用：<https://ai.google.dev/gemini-api/docs/function-calling>

## Gemini Developer API 原生通路

查阅日期：2026-09-23。

- Gemini API 总览、原生端点与 `x-goog-api-key`：<https://ai.google.dev/api>
- GenerateContent、SSE、Content、函数调用、生成配置与用量：<https://ai.google.dev/api/generate-content>
- Models list/get：<https://ai.google.dev/api/models>
- v1/v1beta 版本边界：<https://ai.google.dev/gemini-api/docs/api-versions>
- Gemini CLI 官方鉴权说明：<https://github.com/google-gemini/gemini-cli/blob/main/docs/get-started/authentication.mdx>
- Gemini CLI 官方条款/隐私与第三方 OAuth 边界：<https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/tos-privacy.md>
- Gemini CLI FAQ 的第三方 OAuth 边界：<https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/faq.md>

实现范围与不能据 API Key 宣称会员已可用的边界见 [Gemini 原生 API 开发预览契约](gemini-native-contract.md)。
