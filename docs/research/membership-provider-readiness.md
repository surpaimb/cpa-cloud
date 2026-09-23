# 会员接入条件与当前阻塞项

核查时间：2026-09-23。本文描述提供商接入条件和 CPA Cloud 实现边界，不将 API Key 通路当作会员通路，不给出普遍法律结论。

| 提供商 | 当前代码与验证 | 未完成的接入条件 | 继续实现的出口 |
| --- | --- | --- | --- |
| ChatGPT / Codex | 默认关闭的文件导入、网页 OAuth、共享自动/请求前/手动刷新、模型目录及 Chat / Responses 子集已接入源码并通过合成验收；预检切换不会掩盖刷新落盘失败 | 部署者需提供适用于本应用及回调地址的 client ID；没有真实账号或供应商侧撤销验证 | 完成真实账号、撤销和协议字段验收；模拟通过不升级为生产支持 |
| Claude 订阅 | Messages / count_tokens 的 API Key 原生子集已接入并通过合成上游验收，按 API 用量计费；未实现订阅导入、刷新或网页登录 | Anthropic 当前明确限制第三方产品收集、保存或中介 Claude.ai 凭据；管理员池化 `setup-token` 后为员工转发不属于受支持路径 | 取得覆盖该部署模式的双方约定及调用、生命周期合同，或改为每个终端用户在未修改 Claude Code 中走 Anthropic 自有认证；后者仍需架构决策且不等于 Messages 会员代理 |
| Gemini 订阅 | Gemini API Key 原生子集已接入并通过合成上游验收，使用 AI Studio 项目配额；Vertex 认证仍待实现；未实现订阅导入、刷新或网页登录 | 普通 Google OAuth 登录不授予 Gemini CLI 会员后端的第三方调用权；借用 CLI OAuth 的方式受官方明确限制 | 取得 CPA Cloud 自有应用可用的会员后端、scope、权益及刷新/撤销合同后实施；不把 AI Studio / Vertex 用量计费称为会员额度 |

## 官方来源与结论范围

- [Claude Code：认证与凭据使用](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use) 区分官方应用订阅登录与开发者 API 接入，并限制第三方产品收集、保存或中介 Claude.ai 凭据。托管未修改 Claude Code 时，每个终端用户仍须用自己的凭据通过 Anthropic 流程认证；这不允许单 Go 网关集中池化订阅凭据。该结论只针对当前接入模式，不是对所有 OAuth 的普遍法律结论。
- [Claude API 认证](https://platform.claude.com/docs/en/manage-claude/authentication) 所列 API Key、工作负载联合身份、App Attest 是 API 认证方案；不能据此推导 Claude 订阅额度已经接通。
- [Gemini CLI FAQ](https://geminicli.com/docs/resources/faq/#why-cant-i-use-third-party-software-like-claude-code-openclaw-or-opencode-with-gemini-cli) 明确针对第三方采集或借用 CLI OAuth 的机制，并给出 AI Studio / Vertex API 接入路径。此说明不能自动外推到未来的提供商批准方案。
- Codex 固定协议证据沿用 [直接调用协议](codex-direct-protocol.md)、[模型目录](codex-model-catalog.md) 与 [OAuth 生命周期](../codex-lifecycle-contract.md)。客户端配置不内置第三方 client ID，导入凭据的来源不能由文件自行宣称。

## 状态要求

Claude 和 Gemini 会员保持“接入条件未闭合，未实现”，仍在完整目标中。不能创建看似可用的会员入口，只保存一个无执行路径的 Token，也不能用子进程绕过架构或供应商认证政策。若取得双方约定或新的官方第三方流程，先更新此记录并完成架构决策、独立协议规格、来源绑定、加密迁移、刷新竞争与请求验收，再开放对应能力标志。
