# ChatGPT/Codex、Claude、Gemini 会员账号接入可行性

状态：研究结论（不含实现）

查阅日期：2026-09-22

范围：会员账号授权、凭据导入/刷新、模型调用；API Key 仅作为对照

来源限制：仅使用供应商官方公开文档与公开协议。未查阅 CLIProxyAPI、Sub2API 或归档 CPA 源码，也未处理真实用户凭据。

## 1. 结论摘要

目前没有一家供应商公开提供同时满足下列条件的通用方案：由 CPA Cloud 注册自己的 OAuth 客户端，获得用户会员订阅授权，将该授权作为标准模型 API 凭据托管在服务端，并直接实现 `/v1/chat/completions`、`/v1/responses` 或 `/v1/messages` 代理。

会员权益与开发者 API 权益必须分开：

- ChatGPT 订阅与 OpenAI API 分开计费。会员可以在 Codex 官方客户端中使用订阅权益；ChatGPT Business/Enterprise 另有仅限 Codex 程序化工作流的访问令牌，但官方明确说一般 OpenAI API 调用仍需 Platform API Key。
- Claude.ai 付费计划与 Claude Console/API 分开。会员可以在 Claude Code 中使用订阅权益，也可以为 Claude Code/SDK 自动化生成一年期 OAuth Token；公开文档没有把该 Token 定义为第三方服务可直接调用标准 Messages API 的通用凭据。
- Google AI Pro/Ultra 的较高额度适用于 Gemini CLI/Code Assist 等指定产品。Gemini API Key 走 AI Studio/Cloud 项目、配额和计费。Gemini CLI 官方 FAQ 明确禁止第三方软件、工具或服务收割或借用 Gemini CLI OAuth 来访问后端，并要求第三方代理使用 Vertex AI 或 AI Studio API Key。

因此，在 CPA Cloud 当前“单个 Go 服务进程直接执行上游模型请求”和“标准模型 API”架构下：

| 目标 | 当前结论 | 是否可进入实现 |
| --- | --- | --- |
| OpenAI Platform API Key | 官方、稳定、与 ChatGPT 会员分离 | 可以，按 API Key 上游实现 |
| ChatGPT Plus/Pro 会员直接作为通用模型上游 | 仅证实可登录官方 Codex；没有通用模型 API 授权 | 不可以；保持核心目标但标记阻塞 |
| ChatGPT Business/Enterprise 的 Codex 自动化 | 有官方 Codex Access Token 与 App Server/SDK 路径 | 条件可行，但属于 Codex 代理执行器，不是通用 API；须先批准架构变更 |
| Claude Console API Key / Platform OAuth / WIF | 官方、稳定、API 计费 | 可以，按开发者 API 上游实现 |
| Claude Pro/Max/Team/Enterprise 会员直接作为通用 Messages API 上游 | 官方支持 Claude Code/SDK 自动化 Token，但未公开会员 Token 的通用 Messages API 合同 | 当前直接实现不可以；官方 Runner 方案条件可行 |
| Gemini AI Studio API Key / Vertex AI | 官方、稳定、项目配额与计费 | 可以，按 API Key 或云 IAM 上游实现 |
| Google AI Pro/Ultra 会员经第三方代理调用 | 官方明确禁止借用 Gemini CLI OAuth | 不可以；不得实现或试探非公开端点 |

这里的“不可以”不是删除会员支持目标，而是明确供应商授权/协议阻塞。若供应商以后发布面向第三方服务的会员 OAuth、代理 API 或正式嵌入协议，应重新评估。

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
| `CODEX_ACCESS_TOKEN` | Business/Enterprise；Codex CLI、SDK/App Server 可信自动化 | 可作为未来 Codex Runner 的不透明秘密，不可作为通用 API Key |
| 整个 `auth.json` | 官方 Codex 在可信无头机/CI 中复制并自行刷新 | 只可由原版 Codex 原样消费；不得解析、改写或据此直连非公开接口 |
| 浏览器 Cookie、ChatGPT Session、手工提取的 Access/Refresh Token | 无公开导入合同 | 禁止接收 |

### 3.4 未知或阻塞

- Plus/Pro 会员没有面向第三方服务器的可注册 OAuth 应用、公开 Scope、回调和刷新合同。
- Codex 登录凭据没有被授权为通用 `/v1/responses` 或 `/v1/chat/completions` 凭据。
- 官方 App Server/SDK 是“代理工作流”语义，不等价于无状态标准模型 API；工具、审批、线程和本地执行都可能改变安全与产品语义。
- App Server 需要额外官方运行时进程，和当前“上游请求在一个 Go 服务进程中执行”的架构冲突。
- 对个人 Plus/Pro，虽然官方允许可信 CI 复制 `auth.json`，把个人凭据集中托管后再服务多个员工是否符合组织治理和许可，官方公开材料没有给出足够确认。

### 3.5 允许实现的下一步

1. 继续实现 Platform API Key 上游；UI 和状态必须明确显示“API 计费”，不得显示成 ChatGPT 会员。
2. 保留 `openai_codex_membership` 能力项，状态为 `blocked_current_architecture`，不要把它映射到普通 `openai-compatible` 上游。
3. 若主任务批准引入官方 Runner：只评估 Business/Enterprise Codex Access Token + 官方 Codex SDK/App Server；把它暴露为独立的 Codex Agent 能力，不宣称标准 OpenAI API 兼容。
4. 个人 Plus/Pro 只允许在独立实验环境验证官方 Codex 自身的无头登录流程；在取得 OpenAI 对第三方托管用途的明确确认前，不进入产品实现。

## 4. Anthropic：Claude 与 Claude Code

### 4.1 已证实

**Claude.ai 会员与 Console API 分离。** Anthropic 官方帮助中心说明，Claude.ai 付费计划不包含 Console API 使用；如需 API，必须另行设置 Console 访问和 API 计费。

**会员包含 Claude Code 使用。** Claude Code 支持 Claude Pro、Max、Team 和 Enterprise 账号登录；`/login` 的订阅 OAuth 凭据是这些计划的默认凭据。设置 `ANTHROPIC_API_KEY` 会覆盖会员登录，调用改走 API 计费。

**官方提供自动化用会员 Token。** `claude setup-token` 会通过浏览器授权生成一年期 OAuth Token，命令不把 Token 保存在本机；用户可把它作为 `CLAUDE_CODE_OAUTH_TOKEN` 提供给 CI、脚本、SDK 或自动化环境。该 Token 需要有效的 Pro、Max、Team 或 Enterprise 计划，只能进行模型请求，不能建立 Remote Control 或读取 claude.ai Connectors。到期后重新生成并重启使用它的进程。

**官方还文档化了刷新输入，但没有公开发放合同。** `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` 与 `CLAUDE_CODE_OAUTH_SCOPES` 可让 `claude auth login` 在自动化环境直接交换刷新令牌，示例 Scope 为 `user:profile user:inference user:sessions:claude_code`。公开文档没有说明任意第三方如何注册 OAuth 应用或从用户处取得这种 Refresh Token，因此这些变量不是 CPA Cloud 自行实现授权服务器流程的依据。

**开发者 API 有另一套正式认证。** Claude Platform 文档要求 Console API Key、Workload Identity Federation 或其他平台认证。标准 API 概览只把 API Key 和 WIF 获取的短期 Token 列为 Messages API 的认证方式。Console 的 `ant auth login` OAuth 可以为用户自己的脚本输出 API Access Token，但它属于 Claude Platform/Console 权益与 API 计费，不是 Claude.ai 会员额度。

### 4.2 OAuth 应用注册、作用域与第三方托管

- 未找到面向第三方产品的 Claude.ai 会员 OAuth Client 注册流程、Redirect URI 登记流程或正式授权端点合同。
- `claude setup-token` 是官方认可的会员自动化授权入口，输出是不透明的一年期 Token；公开支持面明确写为 Claude Code、SDK 和自动化环境。
- 未找到官方说明允许第三方 Go 服务把 `CLAUDE_CODE_OAUTH_TOKEN` 直接放入标准 `/v1/messages` 请求。不能仅根据 Token 名称、Scope 或客户端行为推断 Header、端点或刷新协议。
- 对企业集中托管多个员工会员 Token 的席位、共享和审计规则，公开文档不足；实现前应取得 Anthropic 的书面确认或采用明确面向组织的官方部署方式。

### 4.3 公开可支持的导入格式

| 格式 | 官方支持范围 | CPA Cloud 判定 |
| --- | --- | --- |
| `ANTHROPIC_API_KEY` / Claude Platform API Key | 标准 Claude API，独立计费 | 可作为 API Key 上游 |
| Platform OAuth/WIF Access Token | 标准 Claude API；Console/组织权益 | 可按 Platform 官方协议实现，不属于会员导入 |
| `CLAUDE_CODE_OAUTH_TOKEN` | 一年期、不透明；Claude Code、SDK、CI/脚本自动化；会员权益 | 可作为未来官方 Claude Runner 的不透明秘密，不可直接当作标准 API Token |
| `CLAUDE_CODE_OAUTH_REFRESH_TOKEN` + `CLAUDE_CODE_OAUTH_SCOPES` | 官方 Claude Code 自动化预配输入 | 只有来源由组织官方流程保证时才可交给原版 Claude Code；CPA Cloud 不自行签发或交换 |
| `.credentials.json`、浏览器 Cookie、网页 Session | 文档只说明官方客户端管理，未定义第三方导入格式 | 禁止接收或解析 |

### 4.4 未知或阻塞

- 没有面向 CPA Cloud 这种第三方服务的 Claude.ai OAuth 应用注册合同。
- 没有公开的“会员 Token 直接调用 Messages API”的请求合同。
- Claude Code/Agent SDK 是代理运行时而非透明 Messages API；直接映射会引入系统提示、工具、会话和权限语义差异。
- 使用官方 CLI/Agent SDK 需要额外进程或非 Go 运行时，和当前单进程约束冲突。
- 一年期 Token 的集中吊销、管理员列举和每员工审计能力，在面向个人计划时没有足够公开合同。

### 4.5 允许实现的下一步

1. 继续实现 Claude Platform API Key；明确标注它按 API 用量计费，不消耗 Claude.ai 会员额度。
2. 保留 `anthropic_claude_membership` 能力项，状态为 `blocked_current_architecture`。
3. 若批准官方 Runner 架构，可做最小原型：管理员在供应商官方浏览器流程中自行运行 `claude setup-token`；CPA Cloud 只把所得不透明值加密保存，并仅通过 `CLAUDE_CODE_OAUTH_TOKEN` 交给固定版本的官方 Claude Code/Agent SDK。不得记录、解析或回显 Token。
4. 原型只能宣称“Claude Code/Agent SDK 会员自动化”，不能宣称“Claude Messages API 会员代理”。上线前需取得 Anthropic 对企业内部集中托管和多员工转发用途的明确确认。

## 5. Google：Gemini

### 5.1 已证实

**Google AI Pro/Ultra 可提高 Gemini CLI/Code Assist 限额。** Gemini CLI 官方认证文档要求订阅用户用关联的 Google 账号选择“Sign in with Google”；凭据由 CLI 缓存在本地。个人订阅与组织许可证的项目要求不同。

**API Key/Vertex 是不同通道。** Gemini CLI 对 AI Studio API Key 和 Vertex AI 分别给出认证方式。Gemini API 的付费层通过 Google Cloud 项目和 Cloud Billing 管理；会员在 CLI/Code Assist 中获得的额度不是一个可导出的 Gemini API Key 配额池。计划可能另含 Cloud credits，但这仍是独立的 Cloud Billing 权益，不会把会员 OAuth 变成通用 API 凭据。

**第三方借用 OAuth 被明确禁止。** Gemini CLI 官方 FAQ 写明：第三方软件、工具或服务收割或借用 Gemini CLI OAuth 以访问其后端，直接违反适用条款与政策，可能导致账号立即暂停或终止。第三方编码代理受支持且安全的方式是 Vertex AI 或 Google AI Studio API Key。

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
| Google 浏览器 Cookie/Session | 无模型 API 导入合同 | 禁止接收 |

### 5.4 未知或阻塞

- 没有允许第三方服务使用 Google AI Pro/Ultra 会员额度的公开协议。
- 没有受支持的会员凭据导入/刷新格式。
- 官方政策已经给出否定答案，因此这不是通过更多逆向研究即可解除的技术阻塞。

### 5.5 允许实现的下一步

1. 只实现 Gemini API Key 和 Vertex AI，UI 明确显示项目、配额和 Cloud Billing 归属。
2. 保留 `google_gemini_membership` 能力项，状态为 `blocked_by_provider_policy`，向管理员显示官方 FAQ 链接。
3. 不提供 OAuth 缓存上传、Cookie 导入或 Gemini CLI 凭据扫描功能。
4. 只有在 Google 发布新的第三方会员授权产品或给出书面例外后，才重新开启实现评估。

## 6. 可实施协议规格（仅限官方 Runner 条件方案）

本节不是当前接口契约，也不授权修改 `/admin/api/v1/upstreams`。如主任务接受额外官方运行时并协调契约变更，可按以下供应商无关规格做原型。该规格只描述 CPA Cloud 自有边界，不包含任何现有产品实现细节。

### 6.1 能力类型

```text
provider_kind:
  openai_codex_agent
  anthropic_claude_code_agent

capability:
  agent_turn

explicitly_not:
  generic_openai_api
  generic_anthropic_messages_api
```

不能把这两类 Runner 注册成 `openai-compatible` 或 `anthropic-messages` 上游。客户端必须知道它调用的是有会话、工具和审批语义的代理执行器。

### 6.2 凭据封装

```json
{
  "version": 1,
  "provider_kind": "openai_codex_agent | anthropic_claude_code_agent",
  "credential_kind": "codex_access_token | codex_auth_cache_opaque | claude_code_oauth_token",
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
| Anthropic Pro/Max/Team/Enterprise | 用户运行官方 `claude setup-token` | 一年期 Token 到期后由用户重新生成；来源明确的 Refresh Token 只交给官方客户端 | 只通过官方文档化环境变量注入固定版本官方 Runner |
| Gemini AI Pro/Ultra | 无允许的第三方会员入口 | 不适用 | 不实现 |

### 6.4 调用和隔离

- 每个上游账号使用独立工作目录、独立凭据挂载和独立 Runner 进程；不能让一个员工任务读取另一账号的缓存或会话。
- CPA Cloud 与 Runner 只使用供应商公开的 CLI/SDK/App Server 接口；禁止直接调用观察到但未文档化的网络端点。
- 只把用户请求传给被选中的 Runner；不把员工 CPA Cloud Key 传给供应商。
- 流式输出、取消、工具审批、会话恢复、限额错误和重新登录必须按 Runner 的公开事件模型单独测试，不能假定等同 SSE Chat Completions。
- 版本固定；OpenAI App Server Schema 由同版本 CLI 官方命令生成。升级前做兼容测试和回滚。
- Runner 的许可证、再分发方式、自动更新、供应链和平台支持必须在实现前另行记录。

### 6.5 当前否决条件

只要“一个 Go 服务进程直接执行全部上游请求”仍是硬约束，官方 Runner 条件方案就不能进入实现。若坚持当前架构，三家会员接入的结论分别是 OpenAI 阻塞、Anthropic 阻塞、Gemini 被供应商政策禁止；API Key/Cloud IAM 路径不受影响。

## 7. 产品与验收建议

管理界面应把下列概念分别展示，避免误导：

- **API 上游**：API Key、Platform OAuth/WIF、Vertex IAM；显示“API/Cloud 单独计费”。
- **会员代理上游**：仅在供应商公开支持且架构批准后出现；显示“供应商官方 Agent Runner”，不能标成标准模型 API。
- **阻塞目标**：仍列出 ChatGPT Plus/Pro、Claude 会员直接代理、Gemini Pro/Ultra，并显示阻塞原因和官方来源。

会员能力的最小验收项应包括：授权来源、Token 到期与重新授权、供应商侧撤销、本地撤销、套餐/组织权限变化、限额耗尽、重启恢复、并发隔离、日志脱敏、流式取消以及官方 Runner 升级兼容。任何没有端到端证据的项都必须保持“未验证”。

## 8. 官方来源 URL / 查阅日期矩阵

全部来源于供应商官方站点；查阅日期均为 2026-09-22。网页会更新，实施前应重新核对。

| 提供商 | 官方来源 | 支持的结论 | 查阅日期 |
| --- | --- | --- | --- |
| OpenAI | https://help.openai.com/en/articles/9039756 | ChatGPT 与 API Platform 独立计费 | 2026-09-22 |
| OpenAI | https://help.openai.com/en/articles/20001275/ | Codex 使用 ChatGPT 登录走会员权益；API Key 走 API 价格 | 2026-09-22 |
| OpenAI | https://developers.openai.com/codex/auth | 登录方式、自动刷新、设备码、`auth.json` 复制、凭据存储 | 2026-09-22 |
| OpenAI | https://developers.openai.com/codex/non-interactive-mode | 可信 CI 可由官方 Codex 刷新 `auth.json`；API Key 仍是自动化默认选择 | 2026-09-22 |
| OpenAI | https://developers.openai.com/codex/enterprise/access-tokens | Business/Enterprise Codex Access Token、Scope、到期、CLI/App Server 用途；通用 API 需 Platform Key | 2026-09-22 |
| OpenAI | https://developers.openai.com/codex/app-server | 产品嵌入协议、JSON-RPC/JSONL、Schema、登录和实验性边界 | 2026-09-22 |
| OpenAI | https://developers.openai.com/api/docs/quickstart | 标准 OpenAI API 使用 Platform API Key | 2026-09-22 |
| Anthropic | https://support.anthropic.com/en/articles/9876003-i-subscribe-to-a-paid-claude-ai-plan-why-do-i-have-to-pay-separately-for-api-usage-on-console | Claude.ai 会员不含 Console API 使用 | 2026-09-22 |
| Anthropic | https://code.claude.com/docs/en/authentication | Claude Code 账号类型、凭据优先级、存储、`setup-token` 一年期会员 Token | 2026-09-22 |
| Anthropic | https://code.claude.com/docs/en/env-vars | `CLAUDE_CODE_OAUTH_TOKEN`、Refresh Token 与 Scope、API Key 覆盖会员登录 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/api/overview | 标准 Claude API 的认证前提与请求头 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/manage-claude/authentication | API Key、WIF、App Attest 的正式 API 认证范围 | 2026-09-22 |
| Anthropic | https://platform.claude.com/docs/en/cli-sdks-libraries/cli/scripting | Console OAuth Token 可用于用户自己的直接 API 脚本，并由 `ant` 刷新；属于 Platform 路径 | 2026-09-22 |
| Google | https://geminicli.com/docs/get-started/authentication/ | 个人/组织/Headless 认证选择，会员 Google 登录与 API Key/Vertex 的区别 | 2026-09-22 |
| Google | https://geminicli.com/docs/resources/faq/ | 明确禁止第三方借用 Gemini CLI OAuth；第三方应使用 Vertex 或 AI Studio API Key | 2026-09-22 |
| Google | https://geminicli.com/plans/ | Google AI Pro/Ultra 的 CLI 较高限额与 AI Studio API Key 路径 | 2026-09-22 |
| Google | https://ai.google.dev/gemini-api/docs/billing | Gemini API 的项目、层级与 Cloud Billing | 2026-09-22 |
| Google | https://developers.google.com/identity/protocols/oauth2/web-server | Google 通用 OAuth Client、Redirect URI 与 Scope 的含义；不等于 Gemini 会员授权 | 2026-09-22 |

## 9. 最终决策记录

- 三家会员支持继续保留为核心目标，不从路线图删除。
- 当前预览可以交付三家的 API Key/Cloud IAM 接入，但必须明确这不等于会员接入已完成。
- OpenAI Business/Enterprise 和 Claude 会员存在官方 Agent 自动化路径，可在批准多进程 Runner 架构后做独立原型。
- OpenAI Plus/Pro 的第三方集中托管仍缺正式授权合同；不以 `auth.json` 可复制为由宣称通用代理支持。
- Gemini 会员代理因官方明示政策禁止而不实现，直到供应商发布新的公开方案或书面授权。
- 不接收真实凭据用于研究，不实现 Cookie、网页 Session、非公开 Token 或非公开端点导入。
