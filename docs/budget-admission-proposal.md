# Hard TPM 与成本预算准入提案

状态：**待实现设计草案**，2026-09-23。本文件补充[员工请求治理契约](governance-contract.md)、
[治理 Shadow 观测契约](governance-observation-contract.md)和[用量与价格管理契约](usage-management-contract.md)。
它不表示当前服务、网页或发布包已经提供 hard TPM、余额、收费或成本预算。
具体接线以[持久核心与服务契约](budget-persistence-integration-contract.md)为准，特别是当前实际锁顺序、
Token/成本分维度结算、提交结果不确定、未知用量的本系统归因窗口和联合恢复。下文保留最初提案推导，不能覆盖新契约。

本提案依据 CPA Cloud 当前 `internal/governance`、`internal/accounting`、`internal/scheduling` 和模型执行接线独立编写，
没有读取参考产品源码或真实凭据。现有 shadow 观测是事后事实查询，不能直接搬到派发前做硬拒绝。

本分支已完成[账本事务与算术基础](budget-accounting-foundations.md)，尚无 reservation 或运行时接线。
[首个固定模型候选研究](research/budget-bound-profile-feasibility.md)给出了一种极保守的容量上界方案；它依赖官方容量
语义与响应一致性，不是无条件计费保证，仍需专项实现及验收。下文的四桶 proof 是一种上界形式，不得据此假定所有
提供商或模型已经有可用 profile。

## 安全目标与兼容默认

硬限制只能在模型执行前证明本次真实 attempt 的最大 Token 和最大内部估算成本时预留。核心不变量是：

`稳定 scope 当前仍有效的保守占用 + 本次可证明上界 <= 本次准入快照阈值`。

稳定 scope 仍是 `(scope_kind,scope_id)`。employee、Key 和每个治理组全部通过才允许派发；policy、group 或 settings
revision 变化不清零该 scope 的窗口占用。一个逻辑父 request 仍只计一次 RPM/并发，但每个真正可能派发的 attempt
必须分别预留和结算。

兼容默认如下：

- 新库和旧库升级后的 budget 总开关固定关闭；现有 RPM、并发、路由和用量行为不变；
- 新 hard 字段默认为 `null`，旧 shadow TPM/成本继续只观测；
- 管理员必须同时开启 budget 总开关，并为策略显式选择 `deny_unknown`，才能使 hard TPM/成本拒绝请求；
- hard 策略缺少任一证明条件时固定拒绝派发，不能按零、shadow 值或历史平均值放行；
- 不提供 hard `allow_unknown`。需要兼容放行时，该策略只能保持 shadow；
- `count_tokens` 继续排除。它不是与随后生成请求绑定的持久证明，也不能提前消耗或释放预算。

这里的成本始终是管理员价格目录算出的内部估算，不是余额、售价、供应商账单或扣款。首批不增加充值、汇率、
跨币种抵扣或多租户额度。

## 当前源码能证明什么

当前请求生命周期已经提供以下可复用事实：

- 治理准入在账号池之前保存完整 employee/Key/group scope 和 policy revision 快照；
- 账号池安全换号只发生在 `DispatchNotStarted` 的本地账号预检阶段，父 request 不重建；
- 最终 route 包含实际 `account_id`、`upstream_model`、provider 和账号 revision；
- 价格目录以实际账号和实际模型为键，attempt 保存不可变价格版本和四类费率；
- `MarkDispatch` 后不会自动重放；usage 终结、父 accounting request、旧 `model_requests` 和治理释放共享事务；
- 四协议的 usage accumulator 只在四个桶全部可信时给出已知用量，取消、半帧、EOF 和重启可以合法留下 NULL。

当前源码**没有**普通员工请求的可信派发前 Token 上界：没有按实际模型固定版本的本地 tokenizer，也没有证明工具、
图片、缓存、候选数量、协议转换和隐藏推理 Token 均被覆盖的 bounder。客户端给出的数字不可信；字符数、UTF-8
字节数、历史 usage 或 shadow 聚合也不是证明。因此当前可诚实实现的子集是：

1. 持久 reservation/settlement 核心、配置双门控、unknown fail-closed 和合成 bounder 的故障测试；
2. 所有费率均为零时，可证明 cost 上界为零，但这不会产生有意义的预算拒绝；
3. 在至少一个真实 `(provider_kind, protocol, actual_model, transform_revision)` bound profile 通过协议专项验收前，
   生产 hard TPM 不可启用，非零 hard cost 也不可启用。

第一个有意义的协议子集应另行提交一个固定模型族的 text-only bounder。它必须基于最终实际发送的已冻结 payload，
覆盖 system/messages/tools 序列化开销，并要求显式输出上限。任意 unsupported 字段或未知模型直接返回“无法证明”。
不能先开放通用 `openai-compatible`，再假设所有兼容端点使用同一 tokenizer。

## 派发上界证明

服务内部增加不可由 HTTP 客户端构造的 `DispatchBoundProof`，建议最小字段为：

```go
type DispatchBoundProof struct {
    ProviderKind       string
    Protocol           accounting.UsageProtocol
    AccountID          string
    ActualModel        string
    AccountRevision    int64
    TransformRevision  string
    BounderID           string
    BounderRevision     int64
    OrdinaryInputMax   int64
    OutputMax          int64
    CacheReadMax       int64
    CacheWriteMax      int64
}
```

四个上界均为非负 `int64`，使用 checked-add 得到 TPM 上界。成本上界使用本次实际 route 的不可变 `PriceSnapshot`，
沿用 accounting 的 128 位乘加和向上取整规则；不得在 service 再写一套浮点公式。若 bounder 只能证明输入类 Token 的
总上界而不能证明 ordinary/cache read/cache write 的划分，它必须为各桶给出各自安全上界，即使结果保守；不能假设
供应商分类互斥，除非该 profile 的协议验收明确证明。

后续 proof 应以显式版本/类型区分“独立四桶上界”与“互斥输入组上界”，不以缺失字段或零值猜类型。
固定模型候选可以采用独立 `InputMax=C`、`OutputMax=M`：Token 上界 checked-add 为 `C+M`，成本为
`ceil((C*max(三项输入费率)+M*output_rate)/1_000_000)`。这项公式只在输入三桶互斥关系有该 profile 的证据时适用，
不改变通用四桶算术接口。新增组合算术接口应接受原始不可变价格快照，不能把费率改写后仍伪装成同一价格版本；
独立预算分支已补组合算术接口（见 Accounting 基础），仍没有生产 profile 或预算运行时。实际 usage 超出任一采用的 bound 必须如实保存 overage 并停用该 profile，
不能截断用量以维持预算表面成立。

proof 只绑定已经解析并冻结的内存 payload。数据库保存模型、协议、bounder 版本和数值，不保存 prompt、response、
工具参数、Authorization，也不保存正文摘要；正文 hash 仍可能泄露低熵内容。reservation 提交后不得再增加消息、工具、
候选数、输出上限或改变协议转换。任何后续转换都使 proof 失效并禁止派发。

输出上限必须按协议实际语义处理：

- Chat Completions 的 `max_tokens`/`max_completion_tokens`、多 choices 及模型差异需由 profile 明确覆盖；字段缺失不是无穷
  预算内的零，而是无法证明；
- Responses 的 `max_output_tokens` 只有在 profile 证明它同时覆盖该模型的可计费用量时才可使用；
- Anthropic `max_tokens` 不能解决输入和缓存上界；现有 `/count_tokens` 网络调用不与生成 reservation 原子绑定；
- Gemini 的 `maxOutputTokens`、`candidateCount`、多模态输入均需共同计入；字段缺失或媒体 Token 未知时拒绝；
- Codex membership 会经过共享凭据刷新和 Chat/Responses 转换。首批不支持 hard bound，直到对最终 executor payload、
  实际模型和隐藏推理用量有独立证明；不能套用固定 64-token recovery probe；
- SSE 与 JSON 使用同一上界。流式只改变结果到达方式，不放宽预留。

工具调用后的客户端下一轮是新的 HTTP 生成请求，使用新的父 request 和 reservation。单次响应中的 tool-call Token
仍属于本 attempt 的输出上界。

## 配置与不可变快照

建议扩展现有 `governance_settings` 快照，增加独立布尔字段 `budget_enabled`，初始为 false。管理写继续复用同一
settings operation/CAS 并递增现有 settings revision，不创建第二套可能漂移的治理设置；budget 有效时钟只是运行时
字段，不是第二个管理员 revision。`budget_enabled` 不能因现有 `governance_settings.enabled` 已开启而自动变 true。
策略增加 nullable `hard_tpm`、nullable `hard_cost_micro + currency + rolling_24h` 以及
`unknown_mode=shadow|deny_unknown`。管理写操作继续使用 operation UUID、expected revision、审计和同事务回执。

治理准入快照必须保存这些 hard 字段。budget reservation 从已提交的 `governance_request_scopes` 读取全部 scope，
不得接受 handler 重新传入的 scope 或阈值。管理员在途修改只影响之后的治理请求；已准入请求使用自己的不可变阈值，
但窗口内既有 contribution 仍按稳定 scope 全部相加。

若任一命中的 hard cost scope 币种与实际价格币种不同、价格为 nil、价格读取失败或多个 hard cost scope 要求不同币种，
本次 proof 不完整。`deny_unknown` 时拒绝，shadow 时不做 hard cost。不同币种绝不换算、相抵或挑一个较宽松 scope。

## 持久 reservation

建议新增两张事实表和一个有效时钟单例：

| 表 | 关键语义 |
| --- | --- |
| `governance_budget_clock` | 单例 `last_effective_dispatch_at`，防系统时钟回拨；与治理 admission clock 分开 |
| `governance_budget_reservations` | attempt ID 主键；父 request、实际账号/模型/协议、价格和 bounder revision、四桶上界、TPM/cost 上界、状态、有效时间、执行 lease 截止、settlement |
| `governance_budget_reservation_scopes` | attempt + scope 主键；保存准入时 policy/group/settings revision 和 hard 阈值；稳定统计键仍是 scope kind/ID |

状态至少区分 `reserved`、`may_have_sent`、`settled_known`、`settled_unknown`、`released_not_started` 和
`interrupted_unknown`。相同 attempt ID 与完全相同 route、price、proof、scope 快照是幂等重放；任一字段不同固定冲突。
同一个父 request 未来若有多个真实 attempts，每个 attempt 单独一条 reservation；派发前失败候选没有 attempt，也没有
reservation。

窗口使用 budget 的单调 effective dispatch/finish 时间：

- 新 reservation 在事务中取 `max(server UTC now,last_effective_dispatch_at)`；
- 活跃 `reserved/may_have_sent` 始终按完整上界占用，不因请求运行超过 60 秒而提前移出；
- known settlement 用实际 Token/成本替换上界，并从保守 effective finish 起保留 TPM 60 秒、成本 24 小时；
- unknown settlement 或重启 interrupted 继续保留完整上界，直至保守执行截止后再经过对应 60 秒/24 小时窗口；
- `released_not_started` 的 contribution 为零，但原治理 RPM 事件仍保留；
- 请求心跳必须在同一协调路径续治理 lease 与 reservation 执行截止。续租失败取消执行，并保守保留原截止和上界。

对每个 scope，事务按上述规则汇总尚有效的 known actual 或保守 upper bound，再 checked-add 本次 upper bound；结果严格
大于本次 scope 快照阈值时拒绝，等于阈值允许。所有 scope 的读取、比较和插入在一个 SQLite 写事务完成；任一失败
全部回滚。SQL SUM 或 Go 运算溢出固定 storage failure，不能饱和、转浮点或返回部分预留。

## 与 route、attempt 和 `MarkDispatch` 的顺序

共享四协议 helper 应采用以下顺序：

1. 完成协议结构校验、员工鉴权和现有治理 RPM/并发准入；等待账号池时只占治理并发，不占 budget；
2. 进行现有本地账号预检。账号特定、明确 `DispatchNotStarted` 的失败可以按既有规则最多换号一次；此时尚无 attempt
   和 budget reservation；
3. 最终 route 确定后，在 `App.admission.RLock` 下重验 employee、Key、模型权限、route/account/pool revision 和恢复
   隔离，并对**最终实际 payload**生成 bound proof；
4. 在一个 SQLite 事务中读取实际 `account_id + upstream_model` 的当前不可变价格、调用新增
   `accounting.BeginAttemptTx`、比较全部治理 scope 并写 budget reservation；价格目录更新与本事务串行，保存实际读到的
   version。事务提交前不得取得 scheduler 内部锁；
5. 先把 budget reservation 持久推进 `may_have_sent`，再调用账号 lease `MarkDispatch`，最后才进入 HTTP transport 或
   Codex executor。budget mark 成功而后续步骤失败时保守持有上界；不能为减少占用而假称未发送；
6. 一旦 reservation 存在，不再进入安全换号分支。价格、bounder 或 reservation 写失败属于本次全局派发失败，不创建
   新 attempt 或新账号重试；
7. legacy 单路由也走相同 reservation helper，不能绕过。`count_tokens` 不走该 helper。

推荐锁顺序为 `App.admission RLock -> SQLite transaction -> commit/unlock -> budget mark -> account lease MarkDispatch -> network`。
不得持有 scheduler、lease 或 usage mutex 再反向获取 admission/DB。SQLite 提交未知时只能用同一 attempt ID 和原 proof
查询或幂等重放，绝不能生成新 ID 以“再试一次”。

`may_have_sent` 在实际网络调用之前持久化会产生保守占用：进程若在两者之间崩溃，重启仍按 unknown 上界处理。这是
可接受的安全余量。只有状态仍为 `reserved`，且代码路径或 scheduler 给出正向 `DispatchNotStarted` 证据时，才能在事务
中改为 `released_not_started`。HTTP `Do`、Codex executor、连接建立后的错误或零值/Unknown phase 均禁止退回。

## 结算、取消与恢复

现有 `finishWithModelRequest` 事务应同时结算 budget reservation、accounting attempt/request、旧 `model_requests` 和
`governance.FinishTx`：

- 四桶和价格完整时写 actual Token/cost；actual 小于上界后释放差额，actual 大于上界仍如实写 overage，并把该
  bound profile 标为异常，后续请求 fail closed，不能截断账本；
- 结算只由四桶 usage 与价格是否完整可信决定，不由请求结果名称决定。即使结果为 failed、incomplete、取消、EOF 或
  响应丢失，只要已取得完整可信的四桶 usage 和价格，仍按 actual 结算 known；任一项缺失或不可信才结算 unknown 并
  保留上界；
- 401/429/5xx 已进入 transport，同样不能推断零 usage；首批不为任何上游状态码建立“必定零消耗”例外；
- 派发前本地失败没有 reservation；若已 reserve 但尚未 `may_have_sent`，必须凭正向阶段证据原子释放；
- 终结事务失败时，attempt、父 request、治理释放和 budget settlement 全部保持旧状态。JSON 返回固定 503；SSE 不发送
  正常 success 终帧。已有语义输出绝不重放；后台仅以冻结快照重试持久化；
- 客户端取消不等于供应商停止。只要可能已发送就保留上界；完整 usage 仍可正常结算 known。

启动恢复联合读取 accounting pending attempt、治理 pending request 和 budget reservation。只有 reserve、durable
`may_have_sent` 标记、scheduler `MarkDispatch`、transport 这一固定顺序，以及相关数据库行的完整一致关系，能正向证明
崩溃点仍在 durable mark 之前时，`reserved` 才能释放。仅仅查不到 may-have-sent 证据不足以证明没有进入 transport。
缺行、孤儿行、状态矛盾或无法验证顺序时不得按零释放：可完整保守表示时转 `interrupted_unknown` 并保留上界；连保守
关系也无法建立时启动失败且服务不 ready。`may_have_sent` 一律转 `interrupted_unknown`，不调用上游、不重放。恢复事务
失败同样不 ready，不能只恢复 accounting 而丢预算。正常 `App.Close` 停止新预留，等待有界终结；超时后持久状态留给
重启恢复。

## 员工错误与管理员可见性

员工错误固定且不泄露 scope、组名、阈值、余额或其他员工：`budget_exceeded` 可映射 429；显式 hard 策略但 bound/price
无法证明时返回固定 `budget_bound_unavailable`；存储/溢出返回 503；上下文取消沿用协议取消语义。不得为了把请求归类为
普通上游错误而先派发。

管理员页面必须区分 configured、enforcement off、bound unsupported、reserved、settled known、settled unknown 和
overage。显示的预算占用来自 reservation 事实表，不从 shadow observation 的分页结果推导。金额保持十进制字符串并按
币种隔离；页面不得显示 prompt、payload digest、凭据或原始上游错误。

## 最小实现增量与验收门槛

建议拆成三个可独立回滚的增量：

1. **持久核心（可立即实现，仍默认关闭）**：budget schema/严格迁移、stable-scope 原子 Reserve/MarkMayHaveSent/
   Settle/Renew/Recover API、`accounting.BeginAttemptTx` 和共享 checked cost-upper helper。只接合成 bounder，不开放网页 hard
   开关；
2. **运行时接线（仍无生产 hard profile）**：四协议统一在最终 route 后 reserve；终结事务与重启恢复原子化；unknown
   一律保守。用测试配置注入 bound profile，证明所有 handler 不能绕过；
3. **首个真实 profile**：选择一个固定 provider/protocol/model/transform 组合，记录公开协议依据，引入并固定 tokenizer/
   bounder 版本，覆盖最终 payload 的所有允许字段。只有该 profile 的专项、进程和故障测试通过后，才允许管理员为对应
   route 开启 hard budget；其他路由继续 unsupported 或 shadow。

自动验收至少包括：

- 默认关闭和旧库升级不改变现有请求；开启需 settings + policy 双门控；
- employee、Key、多组同时预留，任一 scope 超限时 attempt/reservation 全不写；revision 编辑不清零稳定 scope；
- 账号池首候选本地失败后换号，只按最终账号/实际模型/实际价格预留一次；legacy 同路径；
- 价格更新竞争冻结一个实际 version；无价、错币种、bounder/DB 失败零网络调用；
- 四协议 JSON/SSE、必填/缺失输出上限、多 choices、多模态/unsupported 字段；Codex 明确 unsupported，不借 recovery probe；
- 已知结算释放差额，unknown/取消/半帧/响应丢失/HTTP `Do` 或 executor 进入后保留上界；输出后绝不换号；
- `DispatchNotStarted` 才释放；Unknown、MayHaveSent、存储提交不确定和重启均不释放；
- pending reservation 心跳、TTL、时钟回拨、崩溃各阶段和重复恢复；相同 ID 幂等、改 route/proof/revision 冲突；
- 多 attempt 各自预留而父 request/RPM 一次；`count_tokens` 不预留；
- Token、费率、成本、counts 的逐步溢出，跨币种隔离，actual overage 不截断且使 profile fail closed；
- 终结 sibling 写入故障时 accounting/model/governance/budget 全部回滚，JSON/SSE 不发送正常成功终帧；
- 响应、日志、审计和数据库没有正文、Authorization、员工 Key、上游 token 或正文 hash。

在第 3 步之前，产品只能称为“hard budget 基础构件”或“unsupported 时拒绝的实验接线”，不能宣称普通四协议已经有
可用 hard TPM/成本预算。任何把 shadow 事后总计直接用于派发拒绝、把 NULL 当零或以客户端声明替代 proof 的实现均不
满足本提案。
