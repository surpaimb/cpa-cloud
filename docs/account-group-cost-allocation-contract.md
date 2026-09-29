# 账号组内部成本分摊倍率契约

状态：2026-09-29 开发预览实施契约，ACCT-02/BILL-02 第一段。本文定义既有上游账号池
`account_groups` 的版本化内部成本分摊倍率，以及它与可靠用量账本的连接边界。实现从本项目
规格、既有账本契约和公开协议文档独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API 或
归档 CPA 的源码、测试、迁移、资产或文档。

本批没有新增供应商协议、真实账号流程或第三方依赖。四种生成协议和 OpenAI-compatible
Embeddings 继续遵守各自已有的公开协议来源与支持矩阵；倍率只处理供应商响应已经形成的
内部金额事实，不改变线上的请求或响应。

## 1. 产品语义与非目标

管理员可为既有账号组配置一个正数、有界、版本化的内部成本分摊倍率。它用于回答“这次已
发生的供应商估算成本，在公司内部应分摊多少”，不是供应商账单、员工售价、员工钱包扣款、
预算消耗、余额、税费或多币种换算。

- 倍率按百万分之一保存：`1_000_000 ppm = 1x`。
- 合法范围为 `1..1_000_000_000 ppm`，即 `0.000001x..1000x`；零、负数和更大的值均拒绝。
- 旧数据库中的所有既有账号组在迁移中获得精确 `1x` 的首个不可变版本；新建账号组也以
  精确 `1x` 开始。
- 显式账号池路由只有在实际选中路由的渠道指向账号组时才使用该组的当前倍率。渠道无组、
  路由无渠道和旧单路由均使用内建 `1x`，其 `multiplier_version` 为 `null`。
- `account_groups` 是上游路由配置，不能与 Key 治理组、员工身份组、department 或未来员工
  费率组混用。现有 Key 对账号池组的只读收窄只决定是否允许候选，不改变本契约的金额。
- 原始 `PriceSnapshot`、`price_version`、四类原始 token、`cost_micro` /
  `estimated_cost_micro`、硬预算预留与结算、财务余额和员工钱包保持原值和原逻辑。新增字段
  一律使用 `allocation_` 前缀并在界面标成“内部调整后分摊成本”。

本批不实现员工收费、售价倍率、供应商发票核对、汇率、多币换算、分组预算、媒体计量、
普通员工自助查看或真实供应商验收。

## 2. 定点表示与计算

管理 API 使用 `allocation_multiplier_ppm` 的规范十进制字符串。只接受 ASCII 数字，禁止空
字符串、空白、符号、小数点、指数和前导零；`"0"` 仍因下界被拒绝。响应也返回同一规范
字符串，避免 JavaScript 浮点和安全整数问题。SQLite 保存有界 `INTEGER`。

纯计算接口由 `internal/accounting` 提供：

```go
const AllocationMultiplierScale int64 = 1_000_000
const MinAllocationMultiplierPPM int64 = 1
const MaxAllocationMultiplierPPM int64 = 1_000_000_000

type AllocationMultiplier struct { /* opaque ppm */ }

func NewAllocationMultiplier(ppm int64) (AllocationMultiplier, error)
func ParseAllocationMultiplierPPM(text string) (AllocationMultiplier, error)
func DefaultAllocationMultiplier() AllocationMultiplier
func (m AllocationMultiplier) PartsPerMillion() int64
func (m AllocationMultiplier) CanonicalPPM() string
func (m AllocationMultiplier) Apply(base *int64) (*int64, error)
```

`Apply` 只接受非负 base，使用整数运算计算
`ceil(base * ppm / 1_000_000)`。这与既有 token→microcurrency 成本向上取整规则一致，不使用
浮点数。计算必须等价于对无限精度整数做上述乘法、加 `scale-1` 和除法；64 位中间乘积溢出
本身不是失败条件。只要最终数学结果不大于 `math.MaxInt64` 就必须成功，包括接近
`math.MaxInt64` 的 `1x` base；可使用商余分解、`math/bits` 的完整宽度运算或 `big.Int`。
`base == nil` 表示未知并返回 `nil`；指向零的 base 是已知零并返回指向零的结果，不能和未知
混淆。非法零值倍率、负 base 或最终数学结果大于 `math.MaxInt64` 返回
`accounting.ErrInvalid`，绝不饱和、截断、回绕或回退到 `1x`。此接口不接收币种；调用者保留
原始价格快照的三个大写字母币种，且不同币种永不相加。

更正账本不直接对可能为负的 delta 乘倍率。每次更正先按现有规则得到非负的有效原始成本
总额，再用 attempt 已冻结的倍率计算新的有效调整后总额并持久化。这样舍入只作用于每个
账本状态的总额，不会因把一次更正拆成若干 delta 而改变结果。

## 3. 版本、CAS 与管理 API

既有 `GET /admin/api/v1/account-groups` 每项增加：

```json
{
  "id": "grp_example",
  "name": "生产组",
  "revision": 4,
  "allocation": {
    "version": "agalloc_example",
    "multiplier_ppm": "1250000",
    "created_at": "2026-09-29T01:00:00Z"
  }
}
```

新建组和只改名称的成功响应也返回当前 `allocation`。旧网页可忽略新响应字段；旧服务不声明
能力时，新网页不得发送或假保存倍率。

新增写接口：

`POST /admin/api/v1/account-groups/{id}/allocation`

```json
{
  "operation_id": "550e8400-e29b-41d4-a716-446655440000",
  "expected_revision": 4,
  "allocation_multiplier_ppm": "1250000"
}
```

成功返回本次不可变版本：

```json
{
  "group_id": "grp_example",
  "version": "agalloc_example",
  "revision": 5,
  "allocation_multiplier_ppm": "1250000",
  "created_at": "2026-09-29T01:00:00Z"
}
```

- GET 要求现有管理员 session；POST 另要求现有同源 Origin 与 CSRF 校验。员工 Key 无权访问。
- JSON 必须为单个对象，拒绝未知字段、重复键、缺字段、`null`、错误类型和超限正文。
- `operation_id` 使用现有 UUID 形状；`expected_revision` 必须为显式非负安全整数，并精确匹配
  当前 `account_groups.revision`。倍率更新和名称更新共享同一单调 revision，因而任何一种并发
  更新都会使另一种旧 revision 失败，不存在删值再写回造成的 ABA。
- 成功事务同时追加不可变倍率版本、移动 current 指针、把组 revision 加一并写成功审计。
  `expected_revision == 9_007_199_254_740_991` 或加一超过 JSON 安全整数上界时失败关闭。
- 相同 `operation_id` 与相同规范化 group、expected revision、倍率重放时返回原版本，不增加
  revision；同一 operation ID 改任一内容返回 409 `operation_conflict`。未知组返回 404；旧
  revision 返回 409 `revision_conflict`。
- 400 `invalid_request`、404 `not_found`、409 固定冲突和 503 `storage_unavailable` 均使用固定
  脱敏消息，不回传 SQL、请求正文、凭据、prompt、响应或上游秘密。

`GET /system/status` 仅在迁移、API、最终派发快照、账本终结查询和网页全部接线后声明独立
能力位 `account_group_cost_allocation: true`。

## 4. 持久化与迁移

迁移在一个 caller-owned SQLite 事务中创建并精确核验以下伴随表，不修改旧金额列的语义：

- `account_group_allocation_migration_state`：单例、版本和完成时间；
- `account_group_allocation_versions`：不可变 version、group ID、写入时组 revision、operation
  ID（迁移/新建的默认版本可为 NULL）、ppm 和创建时间；同一管理员 operation ID 全局唯一；
- `account_group_allocation_current`：每组唯一 current version，并以复合外键绑定同组版本；
- `account_group_allocation_legacy_attempts`：只记录首次迁移事务开始时已经存在的 attempt ID，
  不保存或猜测组、倍率和金额；
- `accounting_attempt_allocation_snapshots`：attempt 的可选 group ID、可选 multiplier version
  和必有 ppm；显式组版本必须三者一致，内建 `1x` 必须是 group/version NULL 与 ppm 1,000,000；
- `accounting_usage_allocation_events`：每个可靠 base usage event 的 nullable 调整后成本；
- `accounting_usage_allocation_corrections`：每个更正后的 nullable 有效调整后成本快照。

版本表、历史 attempt 标记、attempt 倍率快照及 allocation event/correction 伴随事实有禁止
UPDATE/DELETE 的触发器；同一 attempt 不能同时拥有历史标记和倍率快照。快照的
`version + group_id + multiplier_ppm` 复合外键必须精确匹配不可变版本，不能只绑定版本后另写
一个 ppm。倍率更新 current 指针使用延迟复合外键；所有 ppm、revision、时间、唯一键、外键
和“未知或已知非负金额”均有数据库约束。查询所需的 group、attempt 和时间索引有固定名称并
经过核验。

首次迁移为事务开始时存在的每个账号组追加 `1_000_000 ppm` 默认版本并建立 current 覆盖；
同时仅把事务开始时已经存在的 attempt ID 写入历史标记表，绝不为它们推测组或倍率。
同一事务核验每组恰有 current、current 指向同组版本、所有 attempt/usage/correction 伴随行无
孤儿，并执行 `PRAGMA foreign_key_check`。已有部分对象、错误列/约束/触发器、错误 marker、
缺失覆盖或任意 SQL 失败都使启动失败并完整回滚；修复冲突后可重试。正常重启不得重建版本
或改写历史。旧 attempt 不回填猜测的组或成本，相关新字段在查询中保持 NULL。

为保留 Embeddings 的 input-only 原始成本，旧版 `accounting_attempts` 与
`accounting_usage_events` 中“只有四类 token 全部已知才允许非 NULL 成本”的约束必须在启动时
升级。升级先按旧约束严格核验源表，在固定连接的原子事务中保留全部行、索引和引用关系，执行
全库 `foreign_key_check`，并在提交或回滚后恢复外键检查；保留对象名冲突、坏 schema、孤儿或
恢复外键失败都拒绝启动。新约束只允许“有价格且 input token 已知”的非 NULL 成本，具体哪些
bucket 为协议必需仍由协议感知的账本计算和回归测试强制；不能借此把生成协议缺失 bucket 计为 0。

新建账号组时，组、默认倍率版本、current 指针和审计同事务提交。任何一步失败都不留下可见
账号组。回滚应用版本时这些附加表可由旧程序忽略；在未明确授权的迁移回退方案前不得删除
表或重写历史。

## 5. 最终派发线性化点

真正的倍率选择只发生在已经取得候选、即将允许出站的最终 durable dispatch 事务中。实现
必须在现有权限、Key 账号组策略、模型/账号/pool revision、租约、恢复状态和 egress 复核之后，
从数据库重新读取实际选中：

```text
public model + selected upstream
  -> model_account_pool_routes.channel_id
  -> account_channels.group_id
  -> account_group_allocation_current
  -> immutable allocation version
```

随后在同一事务写入 attempt、dispatch marker 和 `accounting_attempt_allocation_snapshots`，最后
一次提交。预算路径与非预算路径都必须调用同一个快照 helper；倍率不能因为请求是否受预算
保护而缺失。

- 管理员在候选加载后改变渠道组映射、组倍率、池 revision 或 Key 允许组时，最终事务要么观察
  并冻结新状态，要么按既有配置变化/授权变化语义拒绝，不能使用候选阶段的陈旧倍率。
- SQL/约束/计算失败必须回滚 attempt、dispatch marker、预算预留和倍率快照，且不得调用上游。
- durable dispatch 提交结果不确定时沿用现有“可能已派发”保护；不能为了补倍率而重放已派发
  attempt。已经派发的 attempt 永远使用其冻结版本，后续更新渠道或倍率不得重价。
- 显式池切换或安全的派发前 failover 使用最终实际账号的实际 route/group；被放弃的候选没有
  attempt，也没有倍率快照。执行开始或可能发出后不换号。

## 6. 终结、未知、更正与恢复

attempt 终结仍先以冻结 `PriceSnapshot` 和原始 usage 生成原始估算成本。随后在同一个终结事务
读取 attempt 倍率快照并生成调整后金额；可靠 base usage event 与其 allocation event 原子提交。

- 无价格、任一协议必需 usage 未知、系统中断或其他原始成本未知时，调整后成本也为 NULL。四种
  生成协议仍要求四类 token bucket 全部已知；Embeddings 按其既有契约只要求 input token，
  output/cache/reasoning 保持 NULL 也能按 input rate 形成已知原始成本。
- 原始成本为已知 0 时，调整后成本必须为已知 0。
- 完整 App 启动成功后把 allocation schema 视为必需组合；运行中全部或部分表消失、非历史
  attempt 缺少快照、可靠原始事件缺少 allocation event，或原始更正缺少 allocation correction
  都失败关闭，不能静默退回旧可选投影。仅未组合该迁移的 package 隔离夹具可继续不启用投影。
- 计算溢出或 allocation 表写失败使整个终结事务失败；不能把原始事件提交而丢掉调整后事实。
- 更正使用 attempt 冻结的倍率和更正后的有效原始总额，原始 correction 与 allocation correction
  快照同事务提交；重放继续遵守原 operation ID，不读当前组倍率。
- 重启恢复只为既有 durable dispatch 写现有 unknown system-terminal 事实，并原子写 NULL 的
  allocation event；不建立新 attempt、不访问上游、不读取当前倍率替换快照。
- 只有首次迁移时写入不可变历史标记的旧 attempt 才可没有快照，并显示“倍率未知/调整后成本
  未知”；迁移后的 attempt 缺快照属于持久化损坏，不能假装 `1x` 或普通旧历史。

## 7. 查询、汇总与网页

管理员 usage summary、request attempt 详情、Accounting V2 导出/日报/月报在原始字段旁增加：

- `account_group_id`：nullable string；
- `allocation_multiplier_version`：nullable string；
- `allocation_multiplier_ppm`：nullable 规范十进制字符串；旧历史为 NULL，内建 `1x` 为
  `"1000000"`；
- `adjusted_allocation_cost_micro`：nullable 十进制字符串。

原始 `cost_micro` / `estimated_cost_micro` 保留“供应商价格快照估算成本”标签。调整后字段明确
标成“内部调整后分摊成本”，不得写入员工钱包、余额或预算。汇总按币种分别累计原始与调整后
已知金额，并分别给出 unknown attempt 数；SQL 或 Go 求和溢出返回固定 503，不返回部分结果。
CSV/报表保留两套独立列和倍率版本，跨币种不合并。

网页只在 `account_group_cost_allocation` 能力为 true 时展示账号组倍率编辑器和调整后成本列。
旧服务下保留原账号组和用量功能，不发送新字段；新服务返回畸形/缺失已声明字段时失败关闭。
输入以十进制字符串处理，展示可额外格式化成 `1.25x`，但保存和比较不得经过浮点数。409 保留
用户输入并要求 reload；网络结果不确定时以同一 operation ID 和同一 payload 重试或 reload
核对。桌面和窄屏都要区分 loading、empty、error、unknown 和已知零。

## 8. 协议与任务适用边界

OpenAI Chat Completions、Responses、Anthropic Messages、Gemini generateContent（含现有流式
入口）和 OpenAI-compatible Embeddings 的实际派发 attempt 都适用。协议转换仍只形成一个
durable dispatch 和一个 attempt，不能重复分摊。

`GET /v1/models`、Anthropic `count_tokens`、提供商模型目录、管理员凭据/目录测试、系统探针和
账号恢复探测不产生员工用量 attempt，因而不产生分摊记录。Responses 后台任务在实际领取并
通过最终 dispatch barrier 时冻结倍率；排队中或派发前取消没有快照，重启后已派发任务只按
冻结版本终结为 completed/failed/cancelled/interrupted，绝不重放。

## 9. 验收与安全门槛

自动化测试至少覆盖：

1. 规范 ppm 解析、上下界、`1x`、最小倍率、1000x、nil、已知零、向上取整和 int64 溢出；
2. 旧库/空库/新组默认 1x、旧 attempt 显式历史标记、迁移失败回滚、部分 schema 拒绝、重试
   和重启版本不变；
3. 管理员 session、CSRF/Origin、严格 JSON、revision 并发、name 与倍率并发、operation 重放/
   冲突、审计脱敏和安全整数边界；
4. 候选后渠道改组、倍率更新、Key 组权限收紧、池切换和派发前 failover 的最终事务重核；
5. 四生成协议与 Embeddings 的 known/unknown/known-zero 成本、预算启停两条 dispatch 路径、
   price version 与 multiplier version 各自冻结、混合币种不合并；至少包含 input=2、input rate
   3 micro/token、1.5x 时原始 6/调整后 9，且 output/cache 仍为 NULL 的 Embeddings 回归；
6. 流式取消、持久化失败、运行时全部表丢失、非历史快照丢失、SQLite 整数/求和溢出、并发
   终结、重启 interrupted 和更正重放，均无部分提交或已派发重放；重启必须在同一事务补
   `system_terminal` base event 与 NULL allocation event，任一写入失败时 attempt 仍保持 pending；
7. 目录、count_tokens、测试/探针/恢复探测和派发前取消不产生 allocation attempt；
8. 管理 API、日志、数据库、WAL、导出和浏览器响应不含 Key、Authorization、Cookie、上游
   token、prompt、模型响应或任意 SQL 错误。

验收只使用合成凭据、回环模拟上游和动态非 8787 端口。完整 Go 测试、相关 race、vet、构建、
隔离进程与真实浏览器通过后，测试行为和仍规划行为分别记录。没有单独授权不得发布仓库、
tag、包、部署或生产配置。

## 10. 实施所有权

纯计算任务只拥有 `internal/accounting/allocation.go` 和
`internal/accounting/allocation_test.go`，并且只能实现第 2 节列出的常量、值对象和方法；不得
修改现有 `ledger.go`、价格、迁移、service、web 或共享测试。

集成任务拥有 App/store/config/migration/admin/routes、最终 dispatch 事务、终结/恢复/更正
账本、查询与汇总、能力位、网页、跨模块测试、完整验证和 PR。若纯计算接口需要变化，必须先
修改本文并重新锁定所有权，不能由并行任务自行扩展共享接口。
