# 开发任务记录

## 2026-09-23：继续由本地总协调集成

本地根任务保留路由/数据库/App 接线、审查、独立验收、文档与推送职责。复用三个 GPT-5.6 Sol 协作 agent；不建立重复任务或监控，不发布新包。

| 协作任务 | 当前独立范围 | 集成边界 |
| --- | --- | --- |
| batch_import_finish | shadow 管理 HTTP、签名分页及 pending 父请求修复已交付；复核查询失败关闭 | core/service observations 文件；根任务挂路由 |
| scheduler_finish | TPM/成本只读聚合与严格索引迁移已交付；补充硬预算待实现提案 | 核心交付后仅提案文档，不启用硬预算 |
| gemini_sse_finish | 只读观测网页、UI 测试及真实 Go 浏览器联调已通过 | 仅 web 与独立 UI smoke；不改核心 |
| 本地总协调 | 代理与治理 Linux CI 完成；观测全量回归、Linux race、六组进程验收和网页 CI 已通过 | 默认关闭、未知用量不当零、不开新发布 |

这些是当前并行批次，不代表完整 Sub2API 功能已完成。代理与治理管理、执行、持久化、续租恢复及 Linux race 已通过；新观测本地验收与成功的本轮 CI 分开记录于[集成状态](integration-status.md)。观测三个任务实际共享工作树，已按文件所有权隔离提交，根任务在独立集成树逐项 cherry-pick 和复验；后续任务应优先使用各自工作树。

### 下一批预算基础

观测源码 `60ea6f6` 已推 main 并通过 CI，主线文档收尾为 `2a1dca8`。预算基础代码独立保存在
[`codex/budget-integration`](https://github.com/surpaimb/cpa-cloud/tree/codex/budget-integration)，不纳入观测这轮 CI 的通过范围：

- `scheduler_finish` 在独立 budget-accounting 工作树交付 caller-owned `BeginAttemptTx` 与 checked 成本上界算术；
  根在 budget-integration 分支审查，并独立通过 accounting 全包、service 用量/治理四协议/Codex 专项、vet 与真实进程观测 smoke。
- `gemini_sse_finish` 在独立 budget-profile 工作树核实首个固定模型候选；根再次读取官方模型、context、Token 与缓存说明，
  收紧为有条件的工程推断。研究结论不是 profile 已实现，也不是实际供应商账单保证。
- 根协调器保留预算预留 schema、派发/结算/恢复接线和最终集成职责。当前没有 reservation 表、可调用的 bound profile、
  hard TPM/成本开关或真实供应商兼容证据，预算总目标仍未完成。独立分支尚未合入 main 或运行其自身 Linux CI；合并应与
  下一项完整预算增量统一安排，不为这组基础接口打新包。观测 CI 已于 2026-09-23 完成；独立分支的互斥输入组上界算术、两个恢复 Tx 原语与交叉验收现已完成。三个子任务均已交付，无后台运行任务。

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

## 2026-09-24 预算基础收尾与下一项边界

根继续承担总协调。`scheduler_finish` 交付互斥输入组算术并按审查补三桶同时非零证明；`gemini_sse_finish` 交付治理恢复
Tx 与真实 SQLite 测试；`batch_import_finish` 只读复核服务接口，识别并收紧两个提交/锁序歧义。根独立实现 accounting
恢复 Tx、两包联合事务测试，并审查所有提交；运行证据见[集成状态](integration-status.md)。各工作树独立且未争改主线。

下一项完整预算增量依[持久核心与接线契约](budget-persistence-integration-contract.md)执行，仍需实际实现严格迁移、
reservation/settlement、持久化 profile 隔离、共享派发和恢复，再统一轻量 CI。此次基础交付不能替代上述增量，也不表示
已经派发或持续后台执行全部后续任务。完整 Sub2API 对齐及 Claude/Gemini 会员等阻塞/待办仍保留。
