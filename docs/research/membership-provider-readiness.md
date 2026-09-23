# 会员接入条件与当前阻塞项

核查时间：2026-09-23。本文描述提供商接入条件和 CPA Cloud 实现边界，不将 API Key 通路当作会员通路，不给出普遍法律结论。

| 提供商 | 当前代码与验证 | 未完成的接入条件 | 继续实现的出口 |
| --- | --- | --- | --- |
| ChatGPT / Codex | 默认关闭的文件导入、后端 OAuth 与手动刷新、Chat / Responses 文本及工具子集；网页、共享刷新、模型目录正在本批整合 | 部署者需提供适用于本应用及回调地址的 client ID；本批没有真实账号验证 | 完成生命周期故障测试、协议字段验收；真实账号结果单独记录，模拟通过不升级为生产支持 |
| Claude 订阅 | Messages / count_tokens 的 API Key 路径正在整合；未实现订阅文件导入、刷新或网页登录 | 尚无供 CPA Cloud 托管订阅凭据并直连模型的已确认接入方案；官方对第三方托管 Claude.ai 凭据有明确限制 | 取得适用于该部署方式的提供商接入安排、授权应用配置和请求合同，再单独实现并验证；不借用官方客户端身份 |
| Gemini 订阅 | Gemini 原生 API Key 路径正在整合；未实现订阅导入、刷新或网页登录 | 普通 Google OAuth 登录不等于取得 Gemini CLI 会员后端调用权；借用 CLI OAuth 的方式受官方明确限制 | 确認 CPA Cloud 自有应用可用的会员后端、scope、权益与刷新合同后实施；不把 AI Studio / Vertex API 用量计费称为会员额度 |

## 官方来源与结论范围

- [Claude Code：认证与凭据使用](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use) 区分官方应用订阅登录与开发者 API 接入，并限制第三方收集、存储或中介 Claude.ai 凭据。其托管未修改 Claude Code 的条件也不等同于允许单 Go 网关集中托管订阅凭据。这里是当前接入方案的产品阻塞，不是“所有 OAuth 都违法”的结论。
- [Claude API 认证](https://platform.claude.com/docs/en/manage-claude/authentication) 所列 API Key、工作负载联合身份、App Attest 是 API 认证方案；不能据此推导 Claude 订阅额度已经接通。
- [Gemini CLI FAQ](https://geminicli.com/docs/resources/faq/#why-cant-i-use-third-party-software-like-claude-code-openclaw-or-opencode-with-gemini-cli) 明确针对第三方采集或借用 CLI OAuth 的机制，并给出 AI Studio / Vertex API 接入路径。此说明不能自动外推到未来的提供商批准方案。
- Codex 固定协议证据沿用 [直接调用协议](codex-direct-protocol.md)、[模型目录](codex-model-catalog.md) 与 [OAuth 生命周期](../codex-lifecycle-contract.md)。客户端配置不内置第三方 client ID，导入凭据的来源不能由文件自行宣称。

## 状态要求

Claude 和 Gemini 会员保持“接入条件未闭合，未实现”，仍在完整目标中。不能创建看似可用的会员入口，只保存一个无执行路径的 Token，也不能通过启动官方客户端子进程绕过已确认的单 Go 进程架构。若接入条件改变，更新此记录、单独提供协议规格、来源绑定、加密迁移、刷新竞争与请求验收，再开放对应能力标志。
