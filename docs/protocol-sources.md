# 协议来源记录

查阅日期：2026-09-22。只记录官方文档，不纳入参考产品实现。

| 来源 | 用途 | 本轮核实情况 |
| --- | --- | --- |
| https://developers.openai.com/api/reference/resources/chat | Chat Completions 协议入口 | 可读取；实现前需逐项冻结请求、流事件与用量字段 |
| https://platform.openai.com/docs/api-reference/responses | Responses 协议入口 | 本轮工具读取因页面过大失败，不视为协议已核实 |
| https://platform.claude.com/docs/en/api/overview | Messages 与 Token Counting 端点 | 2026-09-23 已核实 `POST /v1/messages` 和 `POST /v1/messages/count_tokens` |
| https://platform.claude.com/docs/en/manage-claude/authentication | Anthropic API Key 认证 | 2026-09-23 已核实 Bearer 为当前推荐，`x-api-key` 为兼容方式 |
| https://platform.claude.com/docs/en/build-with-claude/streaming | Messages SSE 事件 | 2026-09-23 已核实事件序列、工具 JSON 增量、思考/签名增量、error 与 `message_stop` |
| https://platform.claude.com/docs/en/api/messages/count_tokens | Token Counting 请求与响应 | 2026-09-23 已核实独立端点及对 messages/tools/images/documents 的计数用途 |
| https://support.claude.com/en/articles/9876003-i-have-a-paid-claude-subscription-pro-max-team-or-enterprise-plans-why-do-i-have-to-pay-separately-to-use-the-claude-api-and-console | Claude 订阅与 API 边界 | 2026-09-23 已核实订阅不包含 Claude API/Console 访问，本批不将 API Key 称为会员支持 |
| https://sqlite.org/wal.html | 单机 WAL 运维边界 | 已读取；备份不可遗漏活跃 WAL，WAL 不适合网络共享文件系统 |

核心设计中的 API 路径和支持批次是产品目标，不代表所有官方接口字段已经验证。
后续新增来源应记录具体章节、版本/日期、支持子集与独立测试证据；不抄录大段原文。
