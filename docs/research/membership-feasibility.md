# ChatGPT/Codex、Claude、Gemini 会员账号接入可行性

状态：会员接入研究结论；当前源码实现状态以[接入条件](membership-provider-readiness.md)、[直接调用协议](codex-direct-protocol.md)和[OAuth 生命周期](../codex-lifecycle-contract.md)为准

查阅日期：2026-09-22；Claude 认证政策与 Gemini FAQ 复核日期：2026-09-23

当前源码已有默认关闭的 Codex 文件导入、网页 OAuth、共享刷新、模型目录及 Chat / Responses 子集，并通过合成验收；没有真实账号或供应商侧撤销验证。具体边界以顶部链接为准，本文后续的历史 Runner 研究不覆盖或替代当前实现契约。

范围：会员账号授权、凭据导入/刷新、模型调用；API Key 仅作为对照

来源限制：本轮证据复审仅使用供应商官方公开文档与公开协议，未查阅 CLIProxyAPI、Sub2API 或归档 CPA 源码，也未处理真实用户凭据。项目文档已记录此前接触相关参考实现或材料的历史；这里的来源限制只说明本轮检索范围，不构成也不声称“洁净室”开发。

## 1. 结论摘要

截至查阅日，本次官方公开资料检索没有找到任何一家供应商同时公开提供下列通用方案：由 CPA Cloud 注册自己的 OAuth 客户端，获得用户会员订阅授权，将该授权作为标准模型 API 凭据托管在服务端，并直接实现 `/v1/chat/completions`、`/v1/responses` 或 `/v1/messages` 代理。这表示缺少可据以实现的公开合同，不等同于认定该类行为普遍违法。另有两个范围明确的供应商限制：Anthropic 不允许第三方产品收集、保存或中介 Claude.ai 凭据来代用户路由当前账号池请求；Google 不允许第三方借用 Gemini CLI OAuth 访问其后端。结论不外推到双方另行约定或未来官方流程。

会员权益与开发者 API 权益必须分开：

- ChatGPT 订阅与 OpenAI API 分开计费。会员可以在 Codex 官方客户端中使用订阅权益；ChatGPT Business/Enterprise 另有仅限 Codex 程序化工作流的访问令牌，但官方明确说一般 OpenAI API 调用仍需 Platform API Key。
- Claude.ai 付费计划与 Claude Console/API 分开。会员可以在 Claude Code 中使用订阅权益，也可以为 Claude Code/SDK 自动化生成一年期 OAuth Token；公开文档没有把该 Token 定义为第三方服务可直接调用标准 Messages API 的通用凭据。
- Google AI Pro/Ultra 的较高额度适用于 Gemini CLI/Code Assist 等指定产品。Gemini API Key 走 AI Studio/Cloud 项目、配额和计费。Gemini CLI 官方 FAQ 明确禁止第三方软件、工具或服务收割或借用 Gemini CLI OAuth 来访问后端，并要求第三方代理使用 Vertex AI 或 AI Studio API Key。

因此，在 CPA Cloud 当前“单个 Go 服务进程直接执行上游模型请求”和“标准模型 API”架构下：

| 目标 | 证据边界 | 研究结论 / 下一步 |
| --- | --- | --- |
| OpenAI Platform API Key | 官方、稳定、与 ChatGPT 会员分离 | `openai-compatible` API Key 通路已实现；它按 API 用量计费，不是会员接入 |
| ChatGPT Plus/Pro 会员直接作为通用模型上游 | 仅证实可登录官方 Codex；未找到通用模型 API 授权合同 | 保持目标但阻塞直接实现；这不是普遍法律结论 |
| ChatGPT Business/Enterprise 的 Codex 自动化 | 有官方 Codex Access Token 与 CLI/App Server 路径 | 条件可行，但属于 Codex 代理执行器，不是通用 API；须先批准架构变更 |
| Claude Console API Key / Platform OAuth / WIF | 官方、稳定、API 计费 | API Key 原生 Messages / count_tokens 子集已通过合成验收；Platform OAuth/WIF 仍待实现 |
| Claude Pro/Max/Team/Enterprise 会员直接作为通用 Messages API 上游 | 官方支持用户在 Claude Code 中认证，但限制第三方产品中介 Claude.ai 凭据；也未公开会员 Token 的通用 Messages API 合同 | 当前管理员池化模式受认证政策限制；只有双方另行约定或新的官方第三方流程出现后才重评 |
| Gemini AI Studio API Key / Vertex AI | 官方、稳定、项目配额与计费 | Gemini API Key 原生子集已通过合成验收；Vertex 认证仍待实现 |
| 借用 Gemini CLI OAuth 的第三方会员代理 | FAQ 对该具体机制有明确政策限制 | 不实现该机制；第三方代理改用 Vertex AI 或 AI Studio API Key |

表中的阻塞不删除会员支持目标。若供应商以后发布面向第三方服务的会员 OAuth、代理 API 或正式嵌入协议，应重新评估。

### 1.1 证据结论的三种类型

| 类型 | 本报告中的含义 | 对实现的影响 |
| --- | --- | --- |
| 公开合同缺失 | 本次官方资料检索未找到受支持的注册、作用域、导入、刷新或调用合同 | 阻塞直接实现和兼容性承诺；不能由此推导普遍违法 |
| 当前架构不支持 | 供应商提供了官方 CLI、SDK 或 App Server 路径，但它需要独立运行时、进程或代理语义，超出当前单 Go 进程与 `openai-compatible` 预览契约 | 需要先做契约和架构决策；不代表供应商禁止 |
| 供应商明确限制 | 供应商官方文本明确说某个具体做法违反适用条款或政策 | 不实现该具体做法；结论不得外推到文本未覆盖的其他集成 |

## 2. 判定标准

本报告把四类能力分别判定，避免把“官方 CLI 能登录”误写成“第三方代理能托管会员账号”。

1. **订阅权益**：会员费用是否覆盖目标调用，还是调用仍按开发者 API 单独计费。
2. **授权主体**：授权是否发给供应商官方客户端，还是第三方应用可以注册自己的 OAuth Client。
3. **凭据合同**：是否有公开、受支持的导入格式、作用域、刷新方式、撤销方式和有效期。
4. **调用合同**：凭据是否被公开允许用于标准模型 API，或只允许用于供应商指定的 CLI、SDK、App Server/Agent Runner。

仅当四项都有官方依据，才判定为可由 CPA Cloud 直接实现。浏览器 Cookie、网页 Session、抓包得到的请求、未文档化端点、从本地数据库或 Keychain 中提取 Token，均不构成公开协议。

## 3. OpenAI：ChatGPT 与 Codex

### 3.1 已证实

**订阅与 API 分离。** OpenAI 官方说明 ChatGPT 与 API Platform 是独立计费系统，API 使用单独付费。Codex 官方认证文档把两种登录方式明确区分为：使用 ChatGPT 登录以使用订阅权益，或使用 API Key 按 API 用量计费。

**官方 Codex 客户端支持会员登录和自动刷新。** ChatGPT 桌面端、Codex CLI 和 IDE 扩展可通过浏览器登录 ChatGPT；Codex 会缓存登录，并在使用期间自动刷新临近过期的 Token。远程或无头环境优先使用设备码登录；官方还说明可把整个 `~/.codex/auth.json` 复制到可信无头主机，并由 Codex 自己刷新。文档把该文件视为密码，未给出供第三方解析的稳定字段模式。

**Business/Enterprise 有正式程序化令牌。** Codex Access Token 当前面向 ChatGPT Business 和 Enterprise 工作区。令牌由管理员许可后在 ChatGPT 管理界面创建，权限范围为 Codex，可供可信的 Codex CLI 或 App Server 非交互工作流使用。官方明确：一般 OpenAI API 调用仍需 Platform API Key；Codex Access Token 不是通用 API Key。

**关键原文定位。** OpenAI《Access tokens》的 “How access tokens work” 小节写道：“general OpenAI API calls require Platform API keys.” 这句话只划定 Codex Access Token 与通用 API 的边界，不否定文档明确支持的 Codex CLI/App Server 自动化。

**App Server 是公开的产品嵌入协议。** 官方将 Codex App Server 定义为把 Codex 深度嵌入产品的接口，使用省略 `jsonrpc` 字段的 JSON-RPC 2.0，默认 stdio 为 JSONL，支持认证、线程、审批和流式代理事件。CLI 可以生成与当前版本严格匹配的 JSON Schema。WebSocket 传输和 App Server 命令仍标为实验性、不支持生产工作负载；标准 stdio 仍要求独立的 Codex 进程。

### 3.2 OAuth 应用注册、作用域与第三方托管

- 未找到面向任意第三方服务的“注册 ChatGPT/Codex OAuth Client”公开流程，也未找到允许第三方自行调用授权端点、换取并刷新个人 Plus/Pro 会员 Token 的稳定协议。
- 浏览器登录、设备码登录和 `auth.json` 切换均由官方 Codex 客户端管理。它们证明官方客户端可使用会员权益，不证明 CPA Cloud 可以把同一 Token 当作通用上游 Authorization Header。
- Business/Enterprise 的 Codex Access Token 是公开支持的第三方产品集成凭据，但作用域限制在 Codex CLI/App Server 工作流。官方文档明确区分它与通用 OpenAI API Key。
- App Server 文档确有实验性的 `chatgptAuthTokens` 主机托管模式及 Token 刷新回调；由于它是实验 API，且 CPA Cloud 没有独立、公开的 Token 获取合同，不应据此自行实现 OAuth 端点或导入任意 JWT。

### 3.3 公开可支持的导入格式

| 格式 | 官方支持范围 | CPA Cloud 判定 |
| --- | --- | --- |
| `OPENAI_API_KEY` / Platform API Key | 通用 OpenAI API、独立计费 | 可作为 API Key 上游 |
| `CODEX_ACCESS_TOKEN` | Business/Enterprise；Codex CLI/App Server 可信自动化 | 可作为未来 Codex Runner 的不透明秘密，不可作为通用 API Key |
| 整个 `auth.json` | 官方 Codex 在可信无头机/CI 中复制并自行刷新 | 只可由原版 Codex 原样消费；不得解析、改写或据此直连非公开接口 |
| 浏览器 Cookie、ChatGPT Session、手工提取的 Access/Refresh Token | 无公开导入合同 | 本项目不接收（公开合同缺失） |

### 3.4 未知或阻塞

- Plus/Pro 会员没有面向第三方服务器的可注册 OAuth 应用、公开 Scope、回调和刷新合同。
- Codex 登录凭据没有被授权为通用 `/v1/responses` 或 `/v1/chat/completions` 凭据。
- 官方 Codex CLI/App Server 是“代理工作流”语义，不等价于无状态标准模型 API；工具、审批、线程和本地执行都可能改变安全与产品语义。
- App Server 需要额外官方运行时进程，和当前“上游请求在一个 Go 服务进程中执行”的架构冲突。
- 对个人 Plus/Pro，虽然官方允许可信 CI 复制 `auth.json`，把个人凭据集中托管后再服务多个员工是否符合组织治理和许可，官方公开材料没有给出足够确认。

### 3.5 允许实现的下一步

1. `openai-compatible` API Key 与默认关闭的 Codex 会员实验必须保持独立标识；前者显示 API 计费，后者的源码和合成验收状态以本页顶部链接为准，不能因历史研究文字降级或升级支持声明。
2. 保留 `openai_codex_membership` 能力项，状态为 `blocked_current_architecture`，不要把它映射到普通 `openai-compatible` 上游。
3. 若主任务批准引入官方 Runner：只评估 Business/Enterprise Codex Access Token + 官方 Codex CLI/App Server；把它暴露为独立的 Codex Agent 能力，不宣称标准 OpenAI API 兼容。
4. 个人 Plus/Pro 只允许在独立实验环境验证官方 Codex 自身的无头登录流程；在取得 OpenAI 对第三方托管用途的明确确认前，不进入产品实现。

## 4. Anthropic：Claude 与 Claude Code

### 4.1 已证实

**Claude.ai 会员与 Console API 分离。** Anthropic 官方帮助中心说明，Claude.ai 付费计划不包含 Console API 使用；如需 API，必须另行设置 Console 访问和 API 计费。

**会员包含 Claude Code 使用。** Claude Code 支持 Claude Pro、Max、Team 和 Enterprise 账号登录；`/login` 的订阅 OAuth 凭据是这些计划的默认凭据。设置 `ANTHROPIC_API_KEY` 会覆盖会员登录，调用改走 API 计费。

**官方客户端提供自动化 Token。** `claude setup-token` 会通过 Anthropic 浏览器流程生成一年期 OAuth Token，供用户自己的 Claude Code CI、脚本或自动化环境使用。技术上可生成 Token 不代表第三方产品可以收集、保存或中介它；Anthropic 的认证政策对这种产品用法另有明确限制。

**关键边界。** Anthropic《Authentication》的 “Generate a long-lived token” 小节说明该 Token 只能发起模型请求；[《Legal and compliance》认证条款](https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use)同时要求开发者产品使用 API 认证，并限制第三方提供 Claude.ai 登录或收集、保存、中介 Claude.ai 凭据。托管未修改 Claude Code 时，每个终端用户仍须通过 Anthropic 自有流程使用自己的凭据。

**官方还文档化了刷新输入，但没有公开发放合同。** `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` 与 `CLAUDE_CODE_OAUTH_SCOPES` 可让 `claude auth login` 在自动化环境直接交换刷新令牌，示例 Scope 为 `user:profile user:inference user:sessions:claude_code`。公开文档没有说明任意第三方如何注册 OAuth 应用或从用户处取得这种 Refresh Token，因此这些变量不是 CPA Cloud 自行实现授权服务器流程的依据。

**开发者 API 有另一套正式认证。** Claude Platform 文档要求 Console API Key、Workload Identity Federation 或其他平台认证。标准 API 概览只把 API Key 和 WIF 获取的短期 Token 列为 Messages API 的认证方式。Console 的 `ant auth login` OAuth 可以为用户自己的脚本输出 API Access Token，但它属于 Claude Platform/Console 权益与 API 计费，不是 Claude.ai 会员额度。

### 4.2 OAuth 应用注册、作用域与第三方托管

- 未找到面向第三方产品的 Claude.ai 会员 OAuth Client 注册流程、Redirect URI 登记流程或正式授权端点合同。
- `claude setup-token` 是用户在官方客户端自动化环境中的授权入口，不是 CPA Cloud 收集并池化会员凭据的产品接入合同。
- 未找到官方说明允许第三方 Go 服务把 `CLAUDE_CODE_OAUTH_TOKEN` 直接放入标准 `/v1/messages` 请求。不能仅根据 Token 名称、Scope 或客户端行为推断 Header、端点或刷新协议。
- 当前认证政策明确限制第三方产品收集、保存或中介 Claude.ai 凭据。管理员集中托管会员 Token 并向其他员工转发请求，须先取得覆盖该模式的双方约定；不能先实现再以隔离措施替代接入许可。

### 4.3 公开可支持的导入格式

| 格式 | 官方支持范围 | CPA Cloud 判定 |
| --- | --- | --- |
| `ANTHROPIC_API_KEY` / Claude Platform API Key | 标准 Claude API，独立计费 | 可作为 API Key 上游 |
| Platform OAuth/WIF Access Token | 标准 Claude API；Console/组织权益 | 可按 Platform 官方协议实现，不属于会员导入 |
| `CLAUDE_CODE_OAUTH_TOKEN` | 一年期、不透明；用户自己的 Claude Code CI/脚本自动化；会员权益 | 当前不接收、不保存、不池化；只有双方约定或新的官方第三方流程明确覆盖该模式后重评 |
| `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` + `CLAUDE_CODE_OAUTH_SCOPES` | 官方 Claude Code 自动化预配输入 | 当前不接收或交换；环境变量存在不构成第三方产品授权合同 |
| `.credentials.json`、浏览器 Cookie、网页 Session | 文档只说明官方客户端管理，未定义第三方导入格式 | 本项目不接收或解析（公开合同缺失） |

### 4.4 未知或阻塞

- 没有面向 CPA Cloud 这种第三方服务的 Claude.ai OAuth 应用注册合同。
- 没有公开的“会员 Token 直接调用 Messages API”的请求合同。
- Anthropic 当前认证政策不允许第三方产品按 CPA Cloud 的管理员账号池模式收集、保存或中介 Claude.ai 凭据。
- Claude Code/Agent SDK 是代理运行时而非透明 Messages API；直接映射会引入系统提示、工具、会话和权限语义差异。
- 使用官方 CLI/Agent SDK 需要额外进程或非 Go 运行时，和当前单进程约束冲突。
- 托管未修改 Claude Code 的公开条件要求每个终端用户通过 Anthropic 自有流程使用自己的凭据，与管理员账号池复用给员工的产品模型不同。

### 4.5 允许实现的下一步

1. Claude API Key 原生 Messages / count_tokens 子集已通过合成上游验收，必须继续标明 API 独立计费；Platform OAuth/WIF 仍是单独待办，不属于会员接入。
2. 保留 `anthropic_claude_membership` 目标，当前管理员池化模式标记为 `blocked_by_provider_auth_policy`，不提供 Token 导入或虚假授权入口。
3. 只有 Anthropic 与部署方的双方约定明确允许集中托管和多员工转发，并提供调用与生命周期合同，才重新设计该模式。
4. 若产品改为每个终端用户在未修改 Claude Code 中通过 Anthropic 自有流程认证，仍须先做 Runner 架构决策和协议验收；该模式不能宣称为 Messages 会员代理，也不满足现有管理员账号池目标。

## 5. Google：Gemini

### 5.1 已证实

**Google AI Pro/Ultra 可提高 Gemini CLI/Code Assist 限额。** Gemini CLI 官方认证文档要求订阅用户用关联的 Google 账号选择“Sign in with Google”；凭据由 CLI 缓存在本地。个人订阅与组织许可证的项目要求不同。

**API Key/Vertex 是不同通道。** Gemini CLI 对 AI Studio API Key 和 Vertex AI 分别给出认证方式。Gemini API 的付费层通过 Google Cloud 项目和 Cloud Billing 管理；会员在 CLI/Code Assist 中获得的额度不是一个可导出的 Gemini API Key 配额池。计划可能另含 Cloud credits，但这仍是独立的 Cloud Billing 权益，不会把会员 OAuth 变成通用 API 凭据。

**对“借用 Gemini CLI OAuth”有明确政策限制。** Gemini CLI 官方 FAQ 的 “Why can’t I use third-party software like Claude Code, OpenClaw, or OpenCode with Gemini CLI?” 小节明确点名第三方“harvest or piggyback on Gemini CLI’s OAuth authentication”以访问后端，并说明这违反适用条款与政策、可能导致账号暂停或终止。FAQ 给出的第三方编码代理支持方式是 Vertex AI 或 Google AI Studio API Key。该结论只覆盖借用 Gemini CLI OAuth 的做法，不外推为对所有未来第三方 Gemini 会员集成的普遍禁止。

**无头首次认证不提供会员 OAuth。** 官方认证表对 Headless 场景推荐 Gemini API Key 或 Vertex AI。无头模式可以继续使用已经缓存的官方 CLI 登录，但若尚未登录，必须用环境变量配置 API Key 或 Vertex AI。

### 5.2 OAuth 应用注册、作用域与第三方托管

- Google 通用 OAuth 2.0 文档允许开发者在 Cloud Console 注册 Web 应用、Redirect URI 和 Scope；这只授予指定 Google API 的用户数据访问，不自动授予 Gemini CLI/Code Assist 后端或会员额度。
- 未找到面向第三方产品的 Gemini CLI 会员 OAuth Client、允许 Scope 或后端调用合同。
- 即使能从官方 CLI 缓存中看到 OAuth 数据，也不得导入、刷新或用于 CPA Cloud，因为官方 FAQ 已明确禁止这种第三方借用。
- 不得以通用 Google OAuth Client 替代 Gemini CLI 官方 Client，也不得请求未公开的 Scope 来试探后端。

### 5.3 公开可支持的导入格式

| 格式 | 官方支持范围 | CPA Cloud 判定 |
| --- | --- | --- |
| `GEMINI_API_KEY` | Google AI Studio/Gemini API，项目配额与计费 | 可作为 API Key 上游 |
| Vertex AI ADC / Service Account / Google Cloud API Key | Vertex AI，Cloud IAM 与计费 | 可按官方云认证实现 |
| Gemini CLI OAuth 缓存 | 只供官方 CLI；第三方借用被明确禁止 | 禁止接收、复制、解析或使用 |
| Google 浏览器 Cookie/Session | 无模型 API 导入合同 | 本项目不接收（公开合同缺失） |

### 5.4 未知或阻塞

- 没有允许第三方服务使用 Google AI Pro/Ultra 会员额度的公开协议。
- 没有受支持的会员凭据导入/刷新格式。
- 对“第三方借用 Gemini CLI OAuth”这一具体机制，官方政策已经给出否定答案，因此这不是通过更多逆向研究即可解除的技术阻塞；其他未来官方集成仍应按届时公开合同重新评估。

### 5.5 允许实现的下一步

1. Gemini API Key 原生子集已通过合成上游验收，UI 必须明确显示 AI Studio 项目、配额和计费归属；Vertex 认证仍是独立待办，不属于会员接入。
2. 保留 `google_gemini_membership` 能力项；对借用 CLI OAuth 的方案标记为 `blocked_by_provider_policy_for_cli_oauth`，向管理员显示官方 FAQ 链接。
3. 不提供 OAuth 缓存上传、Cookie 导入或 Gemini CLI 凭据扫描功能。
4. 只有在 Google 发布新的第三方会员授权产品或给出书面例外后，才重新开启实现评估。

## 6. 可实施协议规格（仅限仍获官方支持的 Runner 条件方案）

本节不是当前接口契约，也不授权修改 `/admin/api/v1/upstreams`。2026-09-23 复核后，管理员池化 Claude `setup-token` 的候选方案已撤回；下列规格只保留另有官方支持依据的 Runner 候选。任何新增提供商都要重新通过认证政策和架构门。

### 6.1 能力类型

```text
provider_kind:
  openai_codex_agent

capability:
  agent_turn

explicitly_not:
  generic_openai_api
```

不能把该 Runner 注册成 `openai-compatible` 上游。客户端必须知道它调用的是有会话、工具和审批语义的代理执行器。

### 6.2 凭据封装

```json
{
  "version": 1,
  "provider_kind": "openai_codex_agent",
  "credential_kind": "codex_access_token | codex_auth_cache_opaque",
  "secret": "<opaque, write-only>",
  "account_label": "<admin supplied, non-secret>",
  "expires_at": "<RFC3339 or null>",
  "source": "provider_official_flow"
}
```

约束：

- `secret` 只在管理员经 TLS 提交时出现一次，写入前加密；读取接口永不返回。
- 不解析 JWT Claim，不从秘密推断邮箱、套餐、组织或到期时间；这些元数据由官方 Runner 状态接口确认，或标记未知。
- 不接受 Cookie、密码、浏览器存储目录、整机 Keychain 导出或来源不明的 Refresh Token。
- `codex_auth_cache_opaque` 只能原样挂载给隔离的官方 Codex 进程；不得由 Go 服务解析。该种类只适合受控实验，不作为生产导入格式。
- 管理员可撤销本地副本；供应商侧撤销仍必须按官方控制面执行。状态需区分 `active`、`expired`、`revoked_local`、`provider_reauth_required` 和 `unknown`。

### 6.3 授权与刷新

| 提供商 | 授权入口 | 刷新责任 | CPA Cloud 的责任 |
| --- | --- | --- | --- |
| OpenAI Business/Enterprise | 管理控制台创建 Codex Access Token，或官方 App Server 设备码登录 | Access Token 由管理员轮换；普通登录缓存由官方 Codex 刷新 | 只保存不透明令牌/缓存并启动固定版本官方 Runner |
| OpenAI Plus/Pro | 官方 Codex 浏览器/设备码；可信无头环境可复制 `auth.json` | 官方 Codex 刷新 | 仅实验；不承诺第三方集中托管 |

### 6.4 调用和隔离

- 每个上游账号使用独立工作目录、独立凭据挂载和独立 Runner 进程；不能让一个员工任务读取另一账号的缓存或会话。
- CPA Cloud 与 Runner 只使用供应商公开的 CLI/SDK/App Server 接口；禁止直接调用观察到但未文档化的网络端点。
- 只把用户请求传给被选中的 Runner；不把员工 CPA Cloud Key 传给供应商。
- 流式输出、取消、工具审批、会话恢复、限额错误和重新登录必须按 Runner 的公开事件模型单独测试，不能假定等同 SSE Chat Completions。
- 版本固定；OpenAI App Server Schema 由同版本 CLI 官方命令生成。升级前做兼容测试和回滚。
- Runner 的许可证、再分发方式、自动更新、供应链和平台支持必须在实现前另行记录。

### 6.5 当前否决条件

只要“一个 Go 服务进程直接执行全部上游请求”仍是硬约束，任何官方 Runner 条件方案都不能进入实现。Claude 管理员池化 Runner 还受到当前 Anthropic 认证政策限制；Gemini 借用 CLI OAuth 受到 Google 针对该机制的明确限制。API Key/Platform/Cloud IAM 的可行性不受这些会员结论影响，但每种认证方式仍须单独实现和验收。

## 7. 产品与验收建议

以下是后续产品规划建议，不表示当前预览已经实现对应上游。管理界面应把下列概念分别展示，避免误导：

- **API 上游**：API Key、Platform OAuth/WIF、Vertex IAM；显示“API/Cloud 单独计费”。
- **会员代理上游**：仅在供应商公开支持且架构批准后出现；显示“供应商官方 Agent Runner”，不能标成标准模型 API。
- **阻塞目标**：仍列出 ChatGPT Plus/Pro、Claude 会员直接代理、Gemini Pro/Ultra，并显示阻塞原因和官方来源。

会员能力的最小验收项应包括：授权来源、Token 到期与重新授权、供应商侧撤销、本地撤销、套餐/组织权限变化、限额耗尽、重启恢复、并发隔离、日志脱敏、流式取消以及官方 Runner 升级兼容。任何没有端到端证据的项都必须保持“未验证”。

## 8. 官方来源 URL / 查阅日期矩阵

全部来源于供应商官方站点；原始查阅日期为 2026-09-22，Claude 认证政策于 2026-09-23 复核。网页会更新，实施前应重新核对。

| 提供商 | 官方来源 | 支持的结论 | 查阅日期 |
| --- | --- | --- | --- |
| OpenAI | https://help.openai.com/en/articles/9039756 | ChatGPT 与 API Platform 独立计费 | 2026-09-22 |
| OpenAI | https://help.openai.com/en/articles/20001275/ | Codex 使用 ChatGPT 登录走会员权益；API Key 走 API 价格 | 2026-09-22 |
| OpenAI | https://learn.chatgpt.com/docs/auth | 登录方式、自动刷新、设备码、`auth.json` 复制、凭据存储 | 2026-09-22 |
| OpenAI | https://learn.chatgpt.com/docs/non-interactive-mode | 可信 CI 可由官方 Codex 刷新 `auth.json`；API Key 仍是自动化默认选择 | 2026-09-22 |
| OpenAI | https://learn.chatgpt.com/docs/enterprise/access-tokens | “How access tokens work”：Business/Enterprise Codex Access Token、Scope、到期、CLI/App Server 用途；通用 API 需 Platform Key | 2026-09-22 |
| OpenAI | https://learn.chatgpt.com/docs/app-server | “Codex App Server”：产品嵌入协议、JSON-RPC/JSONL、Schema、登录和实验性边界 | 2026-09-22 |
| OpenAI | https://developers.openai.com/api/docs/quickstart | 标准 OpenAI API 使用 Platform API Key | 2026-09-22 |
| Anthropic | https://support.anthropic.com/en/articles/9876003-i-subscribe-to-a-paid-claude-ai-plan-why-do-i-have-to-pay-separately-for-api-usage-on-console | Claude.ai 会员不含 Console API 使用 | 2026-09-22 |
| Anthropic | https://code.claude.com/docs/en/authentication | “Authentication precedence” 与 “Generate a long-lived token”：账号类型、`setup-token` 一年期会员 Token、适用范围和限制 | 2026-09-22 |
| Anthropic | https://code.claude.com/docs/en/legal-and-compliance#authentication-and-credential-use | 第三方产品的认证、凭据中介限制，以及托管未修改 Claude Code 时的终端用户认证条件 | 2026-09-23 |
| Anthropic | https://code.claude.com/docs/en/env-vars | `CLAUDE_CODE_OAUTH_TOKEN`、Refresh Token 与 Scope、API Key 覆盖会员登录 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/api/overview | 标准 Claude API 的认证前提与请求头 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/manage-claude/authentication | API Key、WIF、App Attest 的正式 API 认证范围 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/cli-sdks-libraries/cli/scripting | Console OAuth Token 可用于用户自己的直接 API 脚本，并由 `ant` 刷新；属于 Platform 路径 | 2026-09-22 |
| Google | https://geminicli.com/docs/get-started/authentication/ | 个人/组织/Headless 认证选择，会员 Google 登录与 API Key/Vertex 的区别 | 2026-09-22 |
| Google | https://geminicli.com/docs/resources/faq/ | “Why can’t I use third-party software ... with Gemini CLI?”：明确限制第三方借用 Gemini CLI OAuth；第三方应使用 Vertex 或 AI Studio API Key | 2026-09-22 |
| Google | https://geminicli.com/plans/ | Google AI Pro/Ultra 的 CLI 较高限额与 AI Studio API Key 路径 | 2026-09-22 |
| Google | https://ai.google.dev/gemini-api/docs/billing | Gemini API 的项目、层级与 Cloud Billing | 2026-09-22 |
| Google | https://developers.google.com/identity/protocols/oauth2/web-server | Google 通用 OAuth Client、Redirect URI 与 Scope 的含义；不等于 Gemini 会员授权 | 2026-09-22 |

## 9. 最终决策记录

- 三家会员支持继续保留为核心目标，不从路线图删除。
- Claude API Key 原生 Messages / count_tokens 子集与 Gemini API Key 原生子集已完成合成上游验收；它们分别使用 API/AI Studio 配额，不是会员接入。Claude Platform OAuth/WIF 与 Google Vertex 认证仍待实现。
- Claude 管理员池化 `setup-token` Runner 候选已撤回。只有取得覆盖集中托管和多员工转发的双方约定或新的官方第三方流程后才重评；每个终端用户登录未修改 Claude Code 是另一种产品和架构，不等于当前账号池目标。
- OpenAI Plus/Pro 的第三方集中托管仍缺公开支持合同；这是实现阻塞，不以 `auth.json` 可复制为由宣称通用代理支持，也不据此断言普遍违法。
- Gemini 不实现第三方借用 Gemini CLI OAuth 的会员代理；限制只针对这一明确机制，其他未来官方方案按新合同重新评估。
- 不接收真实凭据用于研究，不实现 Cookie、网页 Session、非公开 Token 或非公开端点导入。
