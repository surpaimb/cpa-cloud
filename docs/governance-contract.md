# 员工请求治理契约

状态：待实现的独立规格，2026-09-23。本文件定义下一批员工、Key 与专用治理分组的请求治理边界，
不表示源码、管理 API 或网页已经具备这些能力。当前已实现的员工鉴权、账号池、用量账本和成本价格
继续以各自契约为准；本批不得用治理功能改变上游原生路由或把内部成本估算称为正式账单。

本规格对应[完整功能对齐计划](feature-parity-plan.md)中的 KEY-02、LIMIT-01 和 BILL-02 子集，
依据本仓业务规格独立编写，没有读取或移植参考产品源码、迁移或测试。

## 目标与首批边界

首批只对以下主体提供硬限制：

- employee；
- 单个 employee access key；
- 独立的 employee governance group。

硬限制只有每分钟请求数（RPM）和活跃请求并发数。Token 每分钟数（TPM）及内部估算成本预算只做
shadow 观测：计算“若启用会否超过配置阈值”，但绝不拒绝请求。shadow 结果必须同时展示已知合计和未知
请求或尝试数，不能把未知用量、未知价格或不同币种折算成零后比较。

首批不实现租户限额、会话数、IP allowlist、售价、余额、扣款、套餐、支付、多节点协调或员工自助入口。
这些需求仍保留在总计划中。内部估算成本保护也不是员工收费或供应商账单。

`governance_settings.enabled` 是持久总开关，初始值固定为 `false`。普通治理不增加额外 CLI 双开关：
管理员以 revision 条件更新总开关即可。总开关关闭或没有任何适用的已启用策略时，请求保持无限制，
且不得仅因安装新表改变现有流量。关闭总开关只停止新治理准入；已经创建的治理租约、RPM 事件、
终结快照和 shadow 账本仍按原契约完成或保守过期，不能删除、提前释放或遗失。

员工的 `department` 仍只用于展示和统计，不产生权限或限额继承。上游 `account_groups` 是账号路由配置，
也不能兼作员工治理组。治理分组必须使用独立实体和显式员工成员关系。

## 策略作用与组合

每条策略只绑定一个 `employee`、`key` 或 `governance_group`。一次请求可以同时命中员工策略、当前 Key
策略及该员工加入的多个治理组策略；所有硬策略都必须通过。实现必须在一个 SQLite 事务中为全部适用
scope 完成检查和预留，任一失败则全部不写，不能依赖遍历顺序挑选一个较宽松策略。

员工、Key 和组之间不设隐式覆盖优先级。策略更新只影响新准入请求；在途请求继续使用准入时保存的
策略 revision 快照。Key 撤销、员工停用及模型权限变更仍遵守[核心设计](core-design.md)的撤销语义：
管理操作成功返回后，所有新准入必须看到新状态；已经准入并派发的流默认允许完成。撤销不删除 RPM
事件、不退回预算观察值，也不把已经产生的 attempt 改成未派发。

首批 RPM 窗口固定为 60 秒，以服务端 UTC 时钟和半开区间 `(now-60s, now]` 计算。并发是已经治理
准入、尚未持久化释放且租约仍未过期的逻辑员工请求数，包括等待账号、执行预检和正在流式输出的请求。
首批不排队：超过硬限制立即返回固定的限流结果。以后若加入等待队列，必须另行规定配置变更通知和
公平性，不能复用账号 scheduler 的等待队列。

这里的 `now` 是事务内持久化的单调有效时间，不直接使用可能回拨的系统时钟。每次治理准入读取服务端
观测 UTC 和上一次已提交的 effective admission time，并计算
`effective_time = max(observed_utc, last_effective_admission_time)`。首次准入把 observed 和 effective 两个时间
一起保存；相同 request ID 重试复用已保存值，不重新取时钟。RPM 窗口、事件排序、租约截止及重启恢复都
使用 effective time。系统时钟回拨会保守延长窗口或占位，绝不能让此前写入的“未来”事件漏计或让租约
提前释放；实现不尝试把该保守延长伪装成真实墙钟时间。

RPM 和并发统计的稳定维度是 `(scope_kind, scope_id)`，不是 policy ID 或 policy revision。修改阈值、停用再
启用策略或产生新 revision 都不能清空该 scope 最近 60 秒的已治理事件，也不能释放其旧治理租约。总开关
关闭期间通过的请求没有治理记录，重新启用时不得追溯为它们建立 RPM 事件或并发预留；重新启用只约束
之后的新准入。不过，关闭前已经存在的治理 RPM 事件在有效窗口内仍参与统计，未释放旧租约仍占并发。

一个客户端生成请求只计一次 RPM 和并发。派发前安全换号仍属于同一 request ID，不重复计数；未真正
进入执行器的失败候选不产生 attempt。未来若允许多个真实上游 attempt，请求数仍为一，但每个真实
attempt 的用量和内部成本都必须分别进入 shadow 统计。

`count_tokens` 是估算接口，首批不计生成 RPM、并发、TPM 或预算；该排除必须在管理界面和测试中明确，
不能把其结果当成随后生成请求的可靠预留。

## 建议核心接口

治理核心应与 HTTP 和协议转发器解耦，接受服务层已经验证的固定元数据：

```go
type Subject struct {
    EmployeeID string
    KeyID      string
    GroupIDs   []string
    PublicModel string
    Protocol   accounting.UsageProtocol
}

type AdmissionStart struct {
    RequestID string
    Subject   Subject
    StartedAt time.Time
}

type Decision struct {
    Allowed bool
    Code    string
    RetryAt *time.Time
}

type Finish struct {
    RequestID  string
    Status     accounting.Status
    FinishedAt time.Time
}

func (c *Coordinator) AdmitTx(
    ctx context.Context,
    tx *sql.Tx,
    input AdmissionStart,
) (*Lease, Decision, error)

func (c *Coordinator) FinishTx(
    ctx context.Context,
    tx *sql.Tx,
    input Finish,
) error

func (c *Coordinator) RecoverInterrupted(
    ctx context.Context,
    at time.Time,
) (RecoveryResult, error)
```

`AdmissionStart.StartedAt` 是调用方观测 UTC，仅用于首次请求的不可变输入；`AdmitTx` 必须在事务内自行读取并
推进持久 effective time，调用方不能提供或覆盖 effective time。

`RequestID` 使用服务已有稳定 request ID。相同 ID 与完全相同的不可变主体、模型、协议、开始时间和策略
快照是幂等重放；相同 ID 的任一不可变输入不同则返回固定冲突。首次 `FinishTx` 冻结状态和时间，后续
相同快照幂等，不同状态或时间不得覆盖。数据库提交结果不确定时，调用方只能以同一 ID 和原快照重试，
不能生成新 ID 重新预留。

`Decision.Code` 至少区分 `allowed`、`rpm_exceeded`、`concurrency_exceeded`、`policy_changed`、
`authorization_changed`、`storage_unavailable` 和 `cancelled`。员工协议层可以把前两者映射为 429；
只有服务端能证明窗口截止时才返回安全的 `Retry-After`。错误不得泄露策略所属组、阈值、其他员工、SQL、
数据库路径或任何请求正文。

## 持久化模型

首批建议使用下列独立表；名称可在实现前调整，但字段语义和约束不能弱化：

| 表 | 必要字段与语义 |
| --- | --- |
| `governance_settings` | singleton 主键、`enabled`、`revision >= 1`、nullable `last_effective_admission_at`、`updated_at`；新库固定 `enabled=false` |
| `governance_groups` | `id`、唯一 `name`、`revision >= 1`、`created_at`、`updated_at` |
| `governance_group_members` | `group_id`、`employee_id` 联合主键和外键；成员事务必须同时增加 group revision |
| `governance_policies` | `id`、唯一 `(scope_kind,scope_id)`、`enabled`、revision、nullable 正整数 RPM/并发阈值、nullable shadow TPM/预算阈值及预算币种/窗口、时间字段 |
| `governance_requests` | request ID 主键、employee/key、公开模型、固定协议、observed/effective 开始时间、结束时间、终态、租约截止、实际释放时间；不含正文 |
| `governance_request_scopes` | request ID + scope kind + scope ID 主键，另存 policy ID、policy revision 和准入时阈值快照；统计键始终是稳定 scope，用于 RPM、并发及历史解释 |

`scope_kind` 只允许 `employee`、`key`、`group`。多态 scope 必须在管理写事务中显式确认目标存在，
不能接受悬空 ID。Key 策略不能扩大员工模型权限或绕过员工停用；组成员关系也只增加治理约束。

shadow TPM 固定使用 `accounting.Usage` 的四个互斥桶：ordinary input、output、cache read、cache write。
只有四个桶全部已知时才计算一次 attempt 的 total tokens；任一桶为 SQL `NULL`，该 attempt 就进入
`unknown_token_attempts`，不能部分相加后称为 total。shadow 成本直接使用 attempt 的不可变价格快照和
`cost_micro`；价格缺失或任一用量桶未知时成本保持 `NULL` 并计入 `unknown_cost_attempts`。金额只按同一
ISO 币种分别汇总，跨币种绝不相加。

每个 shadow 阈值判断只允许三态：`exceeded`、`below`、`unknown`。已知部分本身已经超过阈值时，即使还有
未知值也判为 `exceeded`；已知部分没有超过且存在任一未知值时判为 `unknown`；只有所有相关值都已知且
合计不超过阈值时才可判为 `below`。`unknown` 不能计入 would-block，也不能显示为余量充足。成本判断按
每个策略币种独立应用同一规则。

治理表不保存 Authorization、Key 明文或 digest、IP 原文、上游凭据、提示词、响应、工具参数、原始错误
或任意日志正文。公开模型、固定协议、主体 ID、策略 revision、固定状态、时间和 nullable 数值已足够。

## 准入、路由和价格顺序

四协议入口必须遵守同一顺序，不能在各 forwarder 中自行实现策略：

1. 有界读取并验证协议请求、公开模型和必要字段；认证员工 Key，不把员工认证头传给上游。
2. 取得 `App.admission.RLock`，在同一最终治理准入事务中重新验证 employee active、Key 未撤销且未过期、
   员工模型权限和治理组/策略 revision，然后调用 `AdmitTx`。这样账号池等待也占用员工侧并发，且未授权
   请求不会消耗 RPM。提交后释放读锁；治理拒绝时不进入账号池。
3. 按现有账号池流程选择 route。等待账号池时不持有 `App.admission` 锁；账号池仍须在发布 route 前按其
   契约再次验证授权和模型策略。无可用 route、等待取消或配置变化时，以同一 request ID 终结治理请求，
   RPM 保留、并发在终结提交后释放。
4. 首次 route 已选定且 provider 已知后，以同一稳定 request ID 幂等写 `model_requests` 和 accounting
   request。两者应使用调用方事务版本的 BeginRequest 原子创建；它们与较早提交的治理准入无法处于同一
   事务，因此启动恢复必须允许“有 governance request、无 accounting request”的合法路由前失败状态，
   不能伪造一个上游 attempt。
5. 账号特定本地预检可以按既有契约在派发前换号一次。换号不重建治理 request 或重复 RPM；最终实际
   route 确定后，按实际 `account_id + upstream_model` 查询价格并创建唯一真实 attempt。价格查询失败拒绝
   dispatch，不能按无价格继续。
6. 成功持久化 attempt 和价格快照后才调用账号租约 `MarkDispatch`，随后进入 HTTP transport 或 executor。
   首批治理在这里没有 hard TPM/cost 预留；未来预留必须加入同一个派发事务边界，见后文。
7. JSON/SSE 只把协议层已接受的 usage 事件交给现有 `UsageAccumulator`。成功终帧仍须等待持久化终结。
8. 最终用一个 SQLite 事务提交 accounting attempt、accounting request、旧 `model_requests` 和
   `governance.FinishTx`。任一写失败全部回滚，治理并发不得提前释放，调用方不得发送成功终帧。

账号 scheduler 只管理上游账号容量和 cooldown；治理 coordinator 只管理员工侧 request scope。两者不得
共用 lease、计数器或锁。治理失败不能触发上游换号，账号换号也不能避开 employee/Key/group 策略。

## 锁顺序与配置变更

首批不维护第二套内存授权缓存，也不在治理层排队。SQLite 是治理计数与租约事实来源。锁顺序固定为：

1. `App.admission` 读锁或写锁；
2. SQLite 事务；
3. 提交并释放事务和 admission 锁；
4. 如未来存在等待者，再在锁外发布配置变化通知。

不得在持有账号 scheduler 内部锁、治理通知锁或 SQLite 事务时反向获取 `App.admission`。治理准入提交并
释放 admission 读锁后才可等待账号池；账号池发布 route 前按自身契约重新取得读锁并复核授权。管理写操作
在 `App.admission.Lock` 内以 expected revision 做事务 CAS；提交失败不改变内存状态或发送通知，提交成功并
发布后才返回。治理策略更新不取消已准入请求的快照；尚未完成治理准入的请求若读到不同
policy/group/settings revision，应保守返回 `policy_changed` 并要求客户端重新提交，不能沿用旧 scope 列表。

Key 撤销和员工停用不主动取消已派发流。若将来提供“停止当前请求”，它是独立操作，并且仍要正常终结
治理、accounting 和账号租约，不能通过删除治理行释放容量。

## 取消、关闭与重启

- 治理准入前取消：不写 RPM 或并发记录。
- 准入提交后、派发前取消或本地预检失败：RPM 事件保留，请求以 `cancelled` 或 `failed` 终结，零 attempt，
  并发只在终结事务提交后释放。
- `MarkDispatch` 后取消、超时、错误、半帧或 EOF：按协议记录 failed/cancelled/interrupted；缺少完整 usage 时
  Token 和成本保持 `NULL`。不得因客户端离开而退回已发生的 RPM 或假定上游没有消耗。
- 终结写入失败：用首次冻结快照做有界 background 重试；仍失败则保留未释放租约直到恢复或原 TTL，
  不能返回成功或在内存中先减并发。
- 关闭总开关：不再为新请求创建治理预留，但继续接受在途租约的 FinishTx，并保留历史 RPM/shadow 行。
- `App.Close`：停止新治理准入，取消内部等待和续租任务，但不删除活跃持久租约。

每个治理租约在准入时保存固定 `expires_at`。正常 handler 运行时可按既有请求生命周期有界续租；续租失败
取消请求并保守占用原 TTL。启动恢复把遗留 `pending` 请求标为 `interrupted`，但不能凭进程重启证明上游
已经停止：其并发占位保留到原 `expires_at`，不会从启动时重新延长。到期后才可清理占位。恢复不填写
Token、成本或成功状态，也不重新发送上游请求。

RPM 依据已经提交的稳定 request scope 事件和 effective time 计算，重启不会清空当前窗口。启动必须先读取
持久化的 `last_effective_admission_at`，不能因进程内时钟状态丢失而漏计未来 timestamp。系统时钟回拨时不得
写早于 effective started_at 的 finished/released 时间；状态转换使用不早于该行 effective start 和当前持久
effective time 的值。任何钳制只会保守延长窗口或占位，不能提前释放。

## 未来 hard TPM 与成本预算

hard TPM 和成本预算仍是产品总需求，但只有在可证明预留上界后才能启用。未来接口应采用两阶段：

1. 逻辑请求准入时预留 RPM 和并发；
2. 最终实际 route、价格快照和协议请求上界已经验证后，在 `MarkDispatch` 前通过
   `ReserveDispatchTx(requestID, attemptID, bounds, priceSnapshot)` 原子预留 Token/金额。

不能用字符数估算输入 Token，不能把 recovery probe 的固定输出上限套到普通员工请求，也不能假设四协议
都有相同或必填的输出上限。缺少可信输入计数、输出上界、价格或币种时，hard 策略必须显式配置
`deny_unknown` 才能在派发前拒绝；否则该策略只能保持 shadow。严禁把未知默认为零或悄悄 fail-open。

取消只有在存在 `DispatchNotStarted` 正向证据时才可退回派发预留。`MayHaveSent`、半帧、取消、EOF 或提交
不确定都不能证明零消耗；保守预留应维持到有界结算/过期。实际消耗超过预留仍要如实记录 overage，不能
截断账本。多个真实 attempt 分别结算；请求成功与否不改变已经发生的 attempt 成本。

未来 hard 预算仍按币种独立执行。没有价格或未知 usage 的 attempt 不能加入 known cost，也不能从其他币种
余额抵消。任何从 shadow 升级为 hard 的 schema/API 变更都需新增规格、迁移与四协议失败模式测试。

## 管理 API 与版本语义提案

首批管理 API 可使用 `/admin/api/v1/governance` 前缀，并沿用管理员 session、Origin 和 CSRF 规则：

- `GET/PUT /settings`：读取或以 `expected_revision + operation_id` 更新总开关；
- `GET/POST/PUT /groups`：创建和 CAS 更新专用治理组；成员变更和 group revision 同事务；
- `GET/POST/PUT /policies`：按 scope 创建或 CAS 更新策略；
- `GET /observations`：只返回固定窗口、known totals、unknown counts 和 would-block 计数。

相同 operation ID 与相同 payload 返回首次结果；相同 ID 不同 payload 冲突。网络结果不确定时，网页保留原
operation ID 和原 payload，由管理员查询或同 ID 重试，不自动生成新操作。策略停用或总开关关闭不删除历史
数据。所有变更均保存独立 revision；access key 当前没有 revision，因此 Key 策略 revision 必须属于策略记录，
不能借 revoked_at 或员工 revision 充当策略 CAS。

## 迁移与验收

迁移必须在单个事务中创建并严格验证表、列类型、主键、外键、NOT NULL、CHECK、唯一约束和索引语义。
已有同名 view、缺列/多列、错误类型/nullability、缺少 CAS 唯一约束、额外改变语义的 UNIQUE 或 partial index
都应使启动失败并完整回滚；修复 schema 后同一迁移可重试。新表不回填历史请求，新库默认关闭，不改变
已有员工流量。迁移和运行时都不得读取真实凭据或请求正文。

实现批次至少自动覆盖：

- 默认关闭、无策略无限制、关闭期间在途租约仍可终结；关闭期请求不追溯，重新启用只限制新准入；
- employee/key/多个治理组策略组合的全有或全无预留；
- 精确 60 秒窗口边界、并发 finish 与相同 request ID 幂等；系统时钟前跳后回拨及重启仍不漏计未来事件；
- 同一 scope 跨 policy revision、阈值修改、策略停用/启用及总开关关闭/开启不清空窗口或旧并发占位；
- 相同 ID 不同主体/时间/策略快照冲突，管理 operation ID 不确定重试；
- 派发前换号仍只有一个 RPM/request，零失败候选 attempt 和一个实际 attempt；
- 撤销/停用/策略 revision 与准入并发，成功返回后新请求拒绝、已准入流按约定完成；
- 四协议 JSON/SSE 正常、失败、取消、半帧和 EOF 的 known/unknown shadow 结果，以及 exceeded/below/unknown
  三态边界；
- 无价格、部分 Token、溢出、跨币种不相加，未知值始终为空；
- 终结事务任一写失败全回滚，不发送成功终帧，重试不重复释放或计费；
- 进程关闭、重启 interrupted、原 TTL 保守占位、时钟回拨；
- 严格迁移失败回滚及修复后重试；
- 错误、日志、数据库和管理响应不含 Key、Authorization、上游凭据、提示词或响应正文。

首批实现仍是单 Go 服务进程和单机 SQLite 一致性域。多进程、多节点或 Redis/PostgreSQL 协调必须另立
一致性方案，不能把本契约的进程锁与 SQLite 事务描述成分布式保证。
