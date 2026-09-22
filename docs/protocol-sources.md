# 协议来源记录

查阅日期：2026-09-22。只记录官方文档，不纳入参考产品实现。

| 来源 | 用途 | 本轮核实情况 |
| --- | --- | --- |
| https://developers.openai.com/api/reference/resources/chat | Chat Completions 协议入口 | 可读取；实现前需逐项冻结请求、流事件与用量字段 |
| https://platform.openai.com/docs/api-reference/responses | Responses 协议入口 | 本轮工具读取因页面过大失败，不视为协议已核实 |
| https://docs.anthropic.com/en/api/messages | Messages 协议入口 | 本轮读取失败，具体认证与事件细节待核实 |
| https://sqlite.org/wal.html | 单机 WAL 运维边界 | 已读取；备份不可遗漏活跃 WAL，WAL 不适合网络共享文件系统 |

核心设计中的 API 路径和支持批次是产品目标，不代表所有官方接口字段已经验证。
后续新增来源应记录具体章节、版本/日期、支持子集与独立测试证据；不抄录大段原文。
