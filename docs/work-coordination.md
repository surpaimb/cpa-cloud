# 开发任务记录

## 2026-09-23：继续由本地总协调集成

本地根任务保留路由/数据库/App 接线、审查、独立验收、文档与推送职责。复用三个 GPT-5.6 Sol 协作 agent；不建立重复任务或监控，不发布新包。

| 协作任务 | 当前独立范围 | 集成边界 |
| --- | --- | --- |
| batch_import_finish | 治理运行时与 Codex 交叉验收完成；实现 shadow 观测管理 HTTP 与签名分页 | 独立 worktree，仅新 service 读接口/测试；根任务挂路由 |
| scheduler_finish | 治理网页已验收；实现 TPM/成本只读聚合核心与必要索引 | 独立 core/迁移测试；不接 HTTP，不启用硬预算 |
| gemini_sse_finish | 治理四协议验收完成；实现只读观测网页与 UI 测试 | 仅 web；未知用量与分币种保持准确，根任务最终浏览器验收 |
| 本地总协调 | 代理 CI 完成；治理轻量 CI、观测接口接线与最终集成 | 默认关闭、未知用量不当零、不开新发布 |

这些是当前并行批次，不代表完整 Sub2API 功能已完成。代理范围与实测见[集成状态](integration-status.md)，治理管理、执行、持久化、续租恢复与浏览器本机验收已有证据，治理 Linux CI 待复验；观测正在独立实现，不能把契约或在途工作称为已完成。

## 2026-09-23 完整功能对齐首批

用户将 Sub2API 的完整产品能力纳入 CPA Cloud。分期与验收见 [功能矩阵](feature-parity-plan.md)，本批接口以 [Responses 契约](responses-preview-contract.md) 为准。原有三个独立任务已完成上一批；Responses 首批使用主任务内的 GPT-5.6 Sol 协作 agent。用户随后要求继续以本会话总协调、多个独立任务实现子功能，后续分工如下。

| 协作任务 | 文件所有权 | 当前交付目标 |
| --- | --- | --- |
| codex_responses | internal/membership、对应实现说明 | 原生 Responses 文本/函数工具/结果回传、事件与错误处理 |
| responses_service | internal/service、必要 cmd | 员工入口、API Key 同协议、会员执行器接线、权限与请求状态 |
| parity_inventory | docs/feature-parity-plan.md | 固定参考快照的功能需求、依赖、阶段与验收矩阵 |
| 主任务 | 顶层规格/README、scripts | 范围更新、独立进程假上游验收、代码审阅与集成证据 |

agent 执行状态不等于功能通过验收；后续阶段列表不代表持续运行的后台任务。当前不建立自动监控、不重新打包、不访问本机真实账号或更改当前用户服务数据。仅在相关测试完成后推送本仓库代码，下载版仍保持 preview.3。

## 后续三个独立任务

均属于 Codex 的 `cpa-cloud` 项目，模型为 `gpt-5.6-sol`、推理强度 high，使用独立 Git worktree。Codex 任务已取得正式 ID；另外两项保留创建标识，不能把创建请求当作功能完成。

| 任务 | 创建标识 | 交付范围 |
| --- | --- | --- |
| Codex 授权与手动刷新 | 01a0cb65-9940-7f93-9408-10c57904dbd2 | 可配置 OAuth、PKCE/state、手动轮换、来源绑定和会话配置快照；自动刷新另行交付 |
| Claude Messages 与账号接入 | client-new-thread:2a548610-cf73-4931-a752-44721ed024ae | 原生 Messages、函数工具与 SSE、API Key 及有协议依据的会员接入 |
| Gemini 协议与账号接入 | client-new-thread:dca21846-1bf5-4af5-b967-1d34be848ab5 | generateContent/streamGenerateContent、函数工具与模型发现、区分官方账号体系 |

本会话负责需求依赖、接口冲突、独立验证和合并。子任务提交代码与测试证据，不自行 push、发布、部署或打包。会员流程必须列明协议依据和实际可用条件，不将 API Key 接入或 mock 测试称为真实会员验证。后续账号池、配额、用量与运营模块按功能矩阵在依赖就绪后派发，避免多个任务重复重构同一执行路径。

Codex OAuth 当前集成分支为 `codex/oauth-integration`。原任务在 `codex-membership-lifecycle` 独立修正，主任务保留主线 Responses 执行器/路由/能力标志，并负责 `scripts/smoke-codex-oauth.mjs` 与跨协议刷新集成测试。合入后仅推轻量 CI，不新增 tag 或安装包。测试与提交证据记录在 [集成状态](integration-status.md)。

## 首轮任务（历史记录）

2026-09-22 启动，三个任务均使用 GPT-5.6 Sol / high。主任务负责集成。

| 任务 | Codex task ID | 文件范围 |
| --- | --- | --- |
| CPA Cloud 服务端核心 | 01a0c818-34a9-7cb1-8f79-608a1801ad29 | go.mod/go.sum、cmd/、internal/ |
| CPA Cloud 网页管理后台 | 01a0c818-3c67-77c3-802b-947649a2b78c | web/ |
| CPA Cloud 三家会员账号接入研究 | 01a0c818-42e6-7203-bfef-3893cf1e2b02 | docs/research/ |

项目尚未注册到 Codex 项目列表，三个任务以无项目任务启动，并明确要求所有实现写入 C:\workspace\cpa-cloud。
它们的默认输出目录不作为代码仓库。共同接口为 preview-contract.md。

任务启动不等于功能完成。集成时核对接口、依赖声明和测试证据；出现协议变更必须同步其他任务。
所有 commit 仅包括本人文件。主任务逐批集成与验收，不直接根据任务描述判定成功。

首轮模拟上游端到端验收在后端和网页可运行后进行。三家会员接入若存在官方协议条件不足，
研究任务须提供精确阻塞依据；不能以 API Key 开发预览替代完整产品目标。
