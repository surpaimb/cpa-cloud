# 剩余能力统一交付契约：2026-09-24

状态：实施前共同接口，基线为 `origin/main` 的
`19388be0a9da53684971550cf5aa5c29959bbe79`。本文由独立集成任务维护，供 D/E/F/G
四条可见实现任务直接协作；不是完成声明，也不创建隐藏子任务。

本文依据本仓[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、
[开发计划](development-plan.md)、[完整功能矩阵](feature-parity-plan.md)和
[开发预览契约](preview-contract.md)独立编写。不得把参考项目的源码、测试、迁移、素材或文档
作为实现模板。

## 1. 范围决定

用户本轮明确暂不需要以下三项，状态记为“当前实施范围外”，而不是误写为已经实现：

- `ID-04` 严格组织级多租户；默认及目标部署继续是一家企业一套实例。
- `ID-05` 员工 SSO/OIDC/第三方登录；Codex 上游 OAuth 不属于员工 SSO，不受此排除影响。
- 管理员密码重置命令；不得通过删除数据目录、替换数据库或增加隐藏后门来代替。

余额、用量、预算、价格、套餐、订阅、充值、兑换、支付和退款不因暂不实现多租户而删除。
这些能力改以**单实例内 employee、Key、资源所有者和管理员权限**为隔离边界；后续若重新引入
多租户，必须另立迁移和跨租户攻击验收，不得提前伪造 tenant 字段或把单实例结果称为租户隔离。

正文审计继续排除。功能性 Responses 上下文若确需持久化，必须满足第 5 节 ADR 门禁；任何日志、
通用审计、用量事件或错误记录仍不得包含提示词、响应、工具正文、认证头、Key 或上游 Token。

## 2. 共同安全与执行不变量

1. 管理员接口继续使用 `/admin/api/v1`、管理员会话、Origin 与 CSRF；员工模型接口继续使用
   Bearer 员工 Key。管理员权限不能替代员工资源所有权检查，员工 Key 也不能调用管理接口。
2. 每个同步请求、后台任务、Responses 资源、工具调用、用量尝试和可删除资源都携带不可变的
   `employee_id` 与 `key_id`。不会存员工 Key 明文。恢复、续跑、工具派发和结果读取前重新验证
   employee 状态、Key 撤销/到期、模型权限和资源所有权。
3. 停用员工或撤销 Key 后，不再发生新的上游、工具或后台派发。已经明确派发的单次尝试只允许
   结束或被取消；未知派发结果记为 `interrupted`/`unknown`，不得自动重放。
4. 跨协议不可表达字段以稳定、脱敏的 4xx 明确拒绝。不得静默丢字段、改写工具语义、伪造
   usage，或用“兼容”概括没有逐项验证的协议。
5. 未知 Token、成本、余额或供应商配额保持未知。严格硬预算只有在存在经过版本化证明的上界时
   才能派发；估算、历史平均或 tokenizer 近似不能冒充安全上界。
6. 所有新 worker 默认关闭；必须先完成迁移、恢复和依赖装配，再开始 claim。Close 先阻止新 claim，
   再取消本进程任务，最后关闭数据库。网络等待不得发生在 SQLite 事务或 admission 写锁内。
7. 只使用合成数据、临时配置、随机端口和本地假上游；不得读取本机真实凭据、访问既有 8787
   服务、借用官方 CLI client 身份、部署、打包、打 tag 或合并 main。

## 3. 五环节交付矩阵

每个可合并里程碑同时报告五个环节；缺一项只能称模块或实验，不能称功能完成。

| 环节 | 必须回答的问题 | 共同出口 |
| --- | --- | --- |
| 管理/员工入口 | 谁能创建、查询、取消、删除或调用？ | 鉴权、CSRF/Origin、employee/Key 资源所有权、能力降级均有测试 |
| 执行逻辑 | 何时派发，哪些字段支持，失败是否可重试？ | 取消贯穿；不可表达字段拒绝；未知派发不重放 |
| 持久化 | 状态、revision、幂等键、秘密和历史如何存？ | 事务迁移可重试；秘密加密；正文不进日志/审计/用量表 |
| 生命周期 | 启停、重启、中断、撤销、到期、删除和恢复如何工作？ | claim 原子；恢复不重放不确定工作；资源可有界清理 |
| 验收证据 | 哪些是测试、进程、浏览器、Linux race 或真实外部证据？ | 已测与计划分开；合成上游不冒充真实供应商/客户端 |

## 4. D/E/F/G 工作流与可合并里程碑

| 流 | 负责人 | 首个增量 | 后续里程碑 | 依赖与验收 |
| --- | --- | --- | --- | --- |
| D 协议与任务 | D 可见任务 `01a0d1f8-d89c-7683-bc26-325015de9589` | D1：协议能力表、跨协议转换最小闭环及逐字段拒绝；Responses 资源只读/删除骨架默认关闭 | D2：有状态 Responses 与后台执行；D3：经白名单的托管工具 | 依赖 KEY-02、E1 用量事件；覆盖流式、工具回合、取消、Key 撤销、未知派发、TTL/删除和重启 |
| E 核算与商业账本 | E 可见任务 `01a0d1f9-40b8-7162-96ef-472ce1315bf1` | E1：四协议及 D 任务共用的可靠用量事件、价格快照和对账差异 | E2：通用预算与额度窗口；E3：单实例余额/不可变金额账本；再接套餐、充值、兑换、支付 | 不依赖多租户；依赖 employee/Key/资源隔离、审计和恢复。未知值不为零，金额定点，回调幂等/验签/反重放 |
| F 自动备份与密钥托管 | F 可见任务 `01a0d0ab-6a56-75a2-b017-411a2596a94c` | F1：OS 保护的本地 key provider、默认关闭的计划与运行历史、现有备份核心的 KeyMaterial 入口 | F2：保留、失败恢复与恢复演练；F3：异机恢复材料、轮换/重封装和生产密钥运行手册 | 依赖现有离线 CLI；错密钥/损坏/符号链接、并发 WAL、重启不重复、权限和 Linux race |
| G 真实兼容与会员准备 | G 可见任务 `01a0d1f9-cf89-7cb1-bff4-2b030f399cd8` | G1：CC Switch/实际 CLI 的合成兼容矩阵和可重复 harness | G2：Codex 真实验证条件清单；Claude/Gemini 保持 provider-specific 阻塞 | 不读取本机凭据、不内置/借用官方 client ID、不把 API Key 当会员；缺少非秘密条件时继续可独立工作并精确报告阻塞 |

每个里程碑可单独审阅、合并和回滚；不等待 D/E/F/G 全部大功能一次性交付。

## 5. 共享身份、资源和用量接口

### 5.1 请求与资源上下文

新能力统一使用不可变上下文，字段语义如下；实现可以复用已有结构，但不得各自发明不兼容含义：

```text
OperationContext
  request_id       本次员工入口请求 ID
  operation_id     调用方提供或服务生成的幂等 ID
  employee_id      资源所有员工
  key_id           创建或触发资源的员工 Key
  protocol         openai-chat | openai-responses | anthropic-messages | gemini
  public_model     员工请求看到的模型
  account_id       实际上游账号；派发前可为空，派发后冻结
  attempt_id       每次实际上游/工具尝试唯一 ID
  response_id?     有状态 Responses 资源
  task_id?         后台任务
  tool_run_id?     托管工具运行
```

数据库只存内部 ID，不存 Bearer Key。后台任务每次从持久 ID 重建权限快照并重查当前权限；创建时有权
不代表恢复后仍有权。管理员可按职责管理资源，但读取功能性正文必须经过明确接口和审计元数据，不能
借通用审计或数据库导出绕过员工资源边界。

### 5.2 统一用量事件

E 拥有规范化用量 schema 与结算实现；D/F/G 不直接写 accounting 表。D 的同步、后台、工具尝试通过
一个窄接口提交事件，字段至少包含：

```text
UsageEvent
  request_id, attempt_id, employee_id, key_id
  protocol, public_model, effective_model, account_id
  dispatch_kind, started_at, dispatched_at?, finished_at?
  status = pending | succeeded | failed | cancelled | interrupted
  input_tokens?, output_tokens?, cache_read_tokens?, cache_write_tokens?
  cost_micro?, currency?, price_version?
  upper_bound_profile?, response_id?, task_id?, tool_run_id?
```

- `?` 表示未知可为 NULL，不是零。原始 provider JSON、SSE 事件、提示或响应正文不进入事件。
- 一个员工请求只有一个 request 事实；重试、换号、后台恢复和工具调用是独立 attempt，不能重复计算
  员工请求数。
- `dispatched_at` 只在实际派发屏障后写入。启动恢复把 pending 且已派发/状态不明的 attempt 标为
  interrupted/unknown，不创建替代 attempt。
- 价格在最终派发前冻结；结束时以相同 price version 原子结算。没有安全上界的请求不能进入严格硬
  预算路径，只能按明确策略拒绝或留在非硬限制路径。

在 E1 合入前，D 可以依赖测试 fake 接口，但不得新增平行账本或把占位值写入现有成本列。

共同 Go 边界使用 `internal/accounting` 中的导出接口，E 延续现有 `Ledger`，不复制一套服务层账本：

```go
type EventRecorder interface {
	BeginRequest(context.Context, RequestStart) error
	BeginAttempt(context.Context, AttemptStart) error
	MarkAttemptDispatched(context.Context, AttemptDispatch) error
	FinishAttempt(context.Context, AttemptFinish) error
	FinishRequest(context.Context, RequestFinish) error
	AppendCorrection(context.Context, Correction) error
}
```

`AttemptDispatch` 只含 attempt ID 与派发时间。`Correction` 是追加事实，至少含 correction ID、目标
attempt/event ID、operation ID、管理员 actor、固定 reason code、币种、各 token/cost 的有符号定点差额
和 UTC 时间；不得原地改历史 attempt，也不得跨币种抵消。相同 operation ID 重试必须幂等，冲突内容
返回稳定 conflict。日/月汇总和导出由 base event 加 corrections 派生，保留未知计数；导出有时间窗、
行数/字节上限和稳定游标，不包含正文、秘密或原始 provider 响应。

通用预算策略使用 `BudgetScope{kind,id,protocol?,model?}`，`kind` 首批只允许 employee、key、group。
多层策略组合必须定义最严格结果、revision 和冻结时点；D 不自行解释预算。没有证明上界的请求对 strict
策略 fail closed，但不能把估算写成 reservation 上界。

### 5.3 功能性 Responses 上下文 ADR 门禁

持久化提示/响应只允许作为默认关闭的 Responses 功能性状态，不属于审计。D2 实现前必须提交 ADR，
至少定义：

- 需要持久化的最小 item 类型和拒绝的媒体/未知类型；默认 `store=false`，没有显式 opt-in 不落正文。
- 使用独立 AEAD purpose 并绑定 `employee_id`、`key_id`、`response_id`、item sequence 和 schema version；
  数据库中仅存密文和有界元数据。
- 员工/Key 授权、管理员运维权限、TTL、显式删除、员工停用/Key 撤销后的读取与续跑语义。
- 备份包含/排除、恢复后 session 作废、后台不确定任务中断、密钥丢失和版本升级边界。
- 日志、错误、用量、管理审计和普通导出永不包含正文；测试扫描数据库、WAL、日志和错误。

未通过 ADR 与迁移审阅时，`store=true`、`background=true`、`previous_response_id` 和 conversation 引用
继续明确拒绝。

## 6. 共享 SQLite 迁移顺序和文件所有权

`internal/service/store.go`、`internal/service/app.go`、`internal/service/config.go`、`cmd/cpa-cloud/main.go`、系统能力总表以及最终 worker
顺序由独立集成任务拥有。组件任务不得同时直接修改这些共享接线文件；组件以独立文件暴露下列窄入口，
由集成任务按固定顺序调用：

1. 现有 `openStore` 基础表、Codex、账号池、账号生命周期迁移。
2. D：Responses 资源迁移，表名前缀保留为 `response_`、`background_`、`managed_tool_`。
3. E：accounting/price/budget/financial ledger 迁移；现有 `accounting_` 与 `account_price_` 表由 E 延续，
   金额账本新增表使用 `financial_` 前缀。
4. F：备份自动化迁移，表名固定为 `backup_key_providers`、`backup_plans`、`backup_runs`。
5. G 默认不增加生产 schema；真实兼容证据存测试产物/文档，不把真实凭据写入数据库。
6. 所有迁移成功后，在一个启动恢复阶段终结 accounting、budget、D background、F backup 的遗留 running
   记录；任何一项恢复失败则 App 不启动 worker。

每个组件迁移必须在独立事务中验证表、列、约束、索引和外键，结构不符整体回滚，修复后可重试；不得
以 `CREATE TABLE IF NOT EXISTS` 接受同名伪造表。禁止组件通过 init、全局变量或第二个数据库连接隐式迁移。

共享文件的冲突解决只由独立集成任务提交。组件任务可以新增同包文件和专项测试；若发现接口不足，先在
任务回报中提出最小签名变更，不抢先改共享接线。

组件应提供、集成任务将调用的签名固定为：

```go
// D: pure conversion lives in internal/protocolconv; HTTP/persistence adapters live in new internal/service files.
func migrateResponseResources(ctx context.Context, db *sql.DB) error
func newResponseResourceCoordinator(app *App, events accounting.EventRecorder) (*responseResourceCoordinator, error)
func (a *App) registerResponseResourceHandlers(mux *http.ServeMux)

// E: implementation extends the existing internal/accounting ledger and service adapters.
func migrateAccountingV2(ctx context.Context, db *sql.DB) error
func (a *App) registerAccountingV2Handlers(mux *http.ServeMux)

// F: crypto/container code remains internal/backup; scheduling adapters live in new internal/service files.
func migrateBackupAutomation(ctx context.Context, db *sql.DB) error
func newBackupAutomationCoordinator(app *App, provider backup.KeyProvider) (*backupAutomationCoordinator, error)
func (a *App) registerBackupAutomationHandlers(mux *http.ServeMux)
```

若最终 Go 类型为避免 import cycle 需要把构造参数缩成窄依赖结构，可以改签名但不得改变责任边界；先在
任务回报中列出差异，由独立集成任务一次性调整 `App`。

## 7. App、路由、worker 与能力标志

### 7.1 注册入口

组件在独立文件提供注册函数，集成任务只在 `App.Handler` 各调用一次：

上述三个 `register...Handlers(*http.ServeMux)` receiver 方法即唯一集成入口；组件不得自行创建第二个 mux。

路径保留：

- D：官方兼容资源路径位于 `/v1/responses/{id}` 及其明确支持的子资源；管理观测位于
  `/admin/api/v1/background-tasks` 和 `/admin/api/v1/managed-tools`。
- E：延续 `/admin/api/v1/usage` 与上游价格路径；新通用预算使用 `/admin/api/v1/budgets`，单实例财务
  资源使用 `/admin/api/v1/billing`。日/月汇总和有界导出位于 `/admin/api/v1/usage/daily`、
  `/admin/api/v1/usage/monthly`、`/admin/api/v1/usage/export`；correction 写入口属于 billing 管理域。
  不得再造第二套 usage summary。
- F：`/admin/api/v1/backups/key-providers`、`/admin/api/v1/backups/plans`、
  `/admin/api/v1/backups/runs`。网页不接收服务器任意路径读取请求；输出目录来自受限运维配置。
- G：没有生产管理路径；兼容性测试通过外部 harness 调用公开协议入口。

### 7.2 初始化和关闭顺序

App 打开顺序固定为：全部迁移 → 价格/用量/预算装配 → session 与跨模块恢复 → 创建所有 coordinator →
注册事务/取消 hooks → 启动现有 refresh/recovery → 启动 D background（默认关闭）→ 启动 F backup
（默认关闭）→ 启动其他 worker。HTTP handler 只能在 `Open` 成功后取得。

Close 反序停止 F backup、D background，再停止现有 scheduled/governance/recovery/refresh，最后关闭共享
客户端和数据库。构造器不得自行启动 goroutine；`Start` 只在所有依赖和恢复成功后调用。

### 7.3 能力标志

`GET /admin/api/v1/system/status` 的 feature 名称固定如下，旧服务器缺字段时网页按不支持处理：

- D：`responses_stateful_resources`、`responses_background_tasks`、`managed_tools`。
- E：`reliable_usage_accounting`、`general_budget_enforcement`、`single_instance_billing`。
- F：`automated_backups_configuration`、`automated_backups_running`、`backup_key_provider_ready`。
- G 不设置“真实兼容已完成”布尔值；真实证据按客户端/版本/OS 列入版本化兼容矩阵，避免过期真值。

configuration 能力只表示 API/schema 存在；running 必须同时满足显式开关、provider ready、恢复成功和
worker 已启动。实验或单模型子集不能把 general/reliable 标志置 true。

## 8. F 首个无人值守边界

F1 采用以下决定，避免任务之间反复改格式和配置：

- 现有 `cpa-cloud-backup create|verify|restore` 的 stdin password 流程与已有包兼容性保持不变。
- 自动计划只保存 `key_provider_id`、revision、调度、保留和目标引用；绝不保存包密码、wrapping key
  或可解密秘密。管理 API 只返回 provider kind、scope、version、ready/status/reason_code。
- 备份核心新增显式 `KeyMaterial` 入口：provider 返回 32 字节 wrapping key，核心用带 package salt、
  provider ID/version 和格式版本绑定的 HKDF-SHA-256 派生每包 AES-256-GCM key。不得把 wrapping key
  当命令行密码、写日志或落数据库；旧 password+scrypt 格式继续由原入口处理。
- Windows v1 首先支持 `windows-dpapi-user`。`windows-dpapi-machine` 只有在显式选择、受保护 DACL 和
  风险说明齐备时才能启用，不能与 user scope 共用同一 kind 或静默切换。
- Linux/macOS 在没有经过验证的系统安全存储 provider 时返回稳定 `key_provider_unavailable`，
  `automated_backups_running=false`；不得回退到明文文件、环境变量、命令行参数或数据库密文旁放密钥。
- provider secret 文件属于主机管理员信任边界，但必须最小权限、原子写、版本化和可轮换。F1 不等于
  完成异机灾备；F2/F3 必须定义恢复材料导出、双版本解密窗口、轮换失败回滚和丢失密钥演练。

共享 Config 字段由集成任务接线：`AutomatedBackupsEnabled bool`；CLI 开关建议
`--automated-backups-enabled`，默认 false。provider 和输出根由受限 data-dir 配置/管理员本机配置确定，
不接受网页提交任意绝对路径。F 组件只提供解析后的配置和 coordinator 构造器，不直接改 main/App。

## 9. G 会员与真实客户端证据规则

- 保留[会员接入阻塞记录](research/membership-provider-readiness.md)中的逐提供商条件。同一日期且官方来源
  无变化时不重复空研究；新结论记录 URL、查阅日期和具体变化。
- 不扫描 CC Switch、Codex、Claude 或 Gemini 的本机目录，不读取用户浏览器、Keychain、注册表或环境
  中的真实凭据。合成 harness 使用临时配置和随机端口。
- 不内置或借用官方 CLI 的 OAuth client ID/secret，不模拟官方 client 身份。Codex 真实 OAuth 需要用户
  自有且适用的 client 配置；Claude/Gemini 会员只有在 provider-specific 接入条件满足后单独验证。
- API Key 原生通路只证明 API Key 能力，不能标记 MEM-03/MEM-04 完成，也不能据当前阻塞作普遍法律判断。
- 真实账号/客户端测试使用单独出口。若缺专用测试账号、自有 client ID、允许的回调域或客户端版本，
  只报告这些非秘密条件；其余合成兼容、错误、取消、工具和配置工作继续推进。

## 10. 后续有编号依赖队列

以下不塞入 D/E/F/G 首个增量，但仍在产品范围；每项继续按五环节拆成可合并里程碑：

- `KEY-02` Key 独立协议/模型/账号组/IP/额度策略；必须先于广泛后台和商业自助授权。
- `LIMIT-02`、`OBS-02` 供应商配额/重置感知、渠道监控、延迟/可用性历史和告警。
- `AUDIT-01`、`AUDIT-02` 统一无正文管理/请求元数据审计、查询、导出、保留与敏感操作二次认证。
- `PROTO-06`、`PROTO-08`、`MEDIA-01/02`、`STORE-01` Realtime、Embeddings、媒体任务、对象存储和清理。
- `ACCT-01/02/03` 的加密导出、完整路由 CRUD、费率、账号组授权、配额感知调度；`PROXY-01`
  的 HTTP/SOCKS、轮换与出口质量。
- `BILL-03/04`、`PAY-01`、`EXT-01` 套餐、订阅、余额、充值、兑换、退款、支付和通知；依赖 E3 的
  单实例金额账本、employee/Key/资源隔离、权限分离、回调安全和恢复，不依赖 `ID-04` 多租户。
- `STORE-02`、`DATA-01` 的保留/清理、PostgreSQL/Redis、多进程一致性继续后置；不得破坏单进程
  SQLite 默认部署。

## 11. 审阅、测试与集成节奏

1. 组件任务先按本文提交接口/ADR和专项测试，只跑自身必要包、静态检查及最小进程验收。
2. 独立集成任务按里程碑审阅来源、迁移、App 接线、资源所有权、失败语义和测试，不等待全部任务结束。
3. 文档、能力矩阵、来源和依赖声明在最终代码 CI 前收齐，避免代码绿灯后追加文档再次触发累计 PR 全量 CI。
4. 集中阶段依次运行：定向组合测试 → 全量 Go/vet → web typecheck/test/build → 随机端口进程验收 →
   桌面与 390×844 浏览器验收 → PR Linux race。已测行为与计划行为分开报告。
5. 不自动合并 main；不发布安装包、tag 或部署。每个 PR 明确列出仍未完成的 D/E/F/G 后续里程碑和
   第 10 节依赖队列。
