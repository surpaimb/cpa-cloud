# 预算持久核心与服务接线契约

状态：**核心及服务接线已进入独立集成分支，完整验收待完成**，2026-09-24。见[实际进度](budget-service-integration-progress.md)。
补充 [预算准入提案](budget-admission-proposal.md)，以当前自有源码为依据；不是 Sub2API 实现的移植。
冲突时本文件中的接口、锁顺序和结算维度决定优先。基础接口的实际交付见
[Accounting 基础](budget-accounting-foundations.md)与[恢复事务基础](budget-recovery-foundations.md)。

## 本批与完成条件

本地总协调保留 App、共享派发路径、数据库升级和最终验收责任。持久核心与运行时作为下一项完整增量提交；
不能把算术接口或可组合 Tx 接口标为预算可用。默认不开预算，不修改 shadow 字段含义，不发布新安装包。

预算依赖三个独立事实：管理员明确开启的 hard 策略、最终 payload 的版本化上界证明、原子持久预留。
已实现一个严格固定模型的实验 bound profile；不代表通用生产预算或真实提供商兼容已验证。

## 配置与迁移

1. `governance_settings` 添加 `budget_enabled`，旧库固定 false；仍与现有 enabled 共用 settings revision。
   两个开关同时为 true 才执行预算。旧 `SetEnabledTx` 只修改 enabled 并保留 budget 位；新管理整对象写使用同一 CAS、
   UUID 回执和审计，不允许两个并行 settings revision。
2. 管理 policy 和 immutable request scope 同时增加 nullable `hard_tpm`、`hard_cost_micro`、对应币种/窗口及
   `unknown_mode=shadow|deny_unknown`。hard 成本窗口首版只允许 rolling_24h；旧数据 hard=NULL、mode=shadow。
   hard-only policy 必须可以形成合法准入快照，不能通过造一个 RPM/shadow 值绕过旧 CHECK。
3. core 与管理层均校验精确 DDL，新增字段需一次 caller-owned 升级事务：只接受已知完整旧 schema 或完整新 schema；
   验证旧值、重建、复制、索引/FK 校验和新元数据写入共同提交。混合版本、触发器、同名 view、非法旧数据失败回滚。
   不能先提交 core 再单独提交 management，也不能重置已发员工 Key、OAuth 来源绑定、operation receipt 或历史 revision。
4. 预算关闭/策略不命中时不追溯创建 reservation。已创建的 reservation 不因开关或策略 revision 改变而释放；重新开启时
   仍计入尚有效的同 scope 历史占用。关闭期间的无证明流量不能被声称已经受预算保护。

## 预留 API 与唯一派发边界

核心由 `internal/governance` 中独立预算组件承载，与既有 RPM/并发协调器共享 SQLite；不得执行网络、刷新凭据、
获取 App admission 或 scheduler/lease 锁。API 最少包括 caller-owned `ReserveTx`、`MarkMayHaveSentTx`、
`RenewTx`、`SettleTx`、`RecoverTx` 和原 attempt ID 的只读查询。

`ReserveTx` 输入只包含 request/attempt ID、已冻结 route/proof 身份及 observed time；阈值、组成员、scope 和价格不得
由 HTTP 或 handler 重传。核心自行读取已提交的治理请求快照和当前事务内刚写入的 accounting attempt，验证 employee、
Key、provider、协议、实际账号/模型、revision、price 和父子关系。scope 总计按 `(kind,id)` 跨 revision 聚合。

根需要将 `dispatchModelRoute` 的最终校验事务从 read/rollback 改为：

- 重查权限、route、账号/池、出口和恢复隔离；
- `PriceCatalog.CurrentTx` 锁定最终账号及实际模型价格；
- `Ledger.BeginAttemptTx` 和 `ReserveTx` 共同写入；任一拒绝回滚两者；
- 提交确认后，持久推进 may_have_sent，最后才进入现有模型 HTTP/Codex 执行器。

当前实际外层锁顺序是 `accountPoolLease.mu -> Codex mutation lock（如适用） -> App.admission.RLock -> SQLite`。
不能照旧提案简写从 admission 反向获取 lease。外层锁连续持有到最终校验、Reserve 提交确认、mark 提交确认和
现有 lease 内存 phase 推进完成，再释放；每个 SQLite transaction 都必须在下一步前关闭。更新 phase 沿用当前已持锁
路径，不能递归调用再获取同一 mutex 的公开方法。预算核心不获取外层锁；不持有数据库事务调用 scheduler 或网络。
如异常路径无法保留上述串行边界，本请求直接保守终止；首版不实现释放锁再沿旧 route 继续发送的替代路径。

legacy 单路由与账号池共享此路径；count_tokens 排除。reserve 后不得换号。尚未建立 attempt 的本地预检失败仍沿用
既有最多一次安全换号。service 中当前 `requestID + ':1'` 的单 attempt 能力不能在新接口文档中冒充多次网络尝试。

## 提交结果不确定

Reserve、mark 和 settle 必须支持完整输入精确幂等以及原 ID 查询；不能仅靠内存指针判断落盘成功。
新 mutation 的原 route、price、proof、时间与 revision 在开始前冻结。Commit 返回错误后，fresh read 对比完整持久
receipt；必要时只按原 ID/原输入重放数据库操作，禁止产生新 attempt ID、重放网络或提前结束为 no-attempt。

正常流程可在确认 mark 提交后发送一次。只要一次 Mark Commit 返回结果不确定，该请求永久失去发送资格；无论后续
查到 reserved 或 may_have_sent，均不能重放 mark 后重新获得 dispatch permit。后续数据库操作只按原 attempt 收敛为
interrupted，Token/cost 均 unknown；第一次终结时冻结时间，按下方归因规则保守持有上界，不能遗留永远 active 的行。
收敛写失败保留原状态并在同输入核对/启动恢复时处理。重启也绝不重新发送。Settle 不确定只核对元数据终态，不再次
生成。无法确认数据库结果时固定 storage unavailable；不返回正常 success 终帧。

## 生命周期、两个结算维度与事实表

reservation lifecycle 与用量知识分别持久化。生命周期固定为 reserved、may_have_sent、settled、released_not_started、
interrupted；另有 token/cost 各自的 known 标记和 nullable actual 值。四桶全 known 而没有价格时，Token 可以 known、
cost 仍 unknown，不能用单一 settled_known 枚举把两者绑死。

- reservation 以 attempt ID 为主键，保存完整 route/proof 类型与版本、不可变上界、时间、执行截止及 settlement；
- reservation scopes 保存其全部命中策略快照、settings/policy/group revision；稳定查询键为 kind/id；
- budget clock 单例保存所有 Reserve/mark/renew/settle/recovery 推进的 effective time，不只保存 dispatch 时钟；
- profile quarantine 按 provider/protocol/actual model/transform/bounder ID+revision 持久化，保存脱敏 overage 原因；
  与实际结算同事务写入。不同价格版本不应该使一个已经越界的同版本 profile 重新可用。

四桶上界与互斥输入组上界是显式不同 proof 类型。后者约束三个可同时非零输入桶的**总和**，不是每桶各自一个 C。
上界计算读原始 PriceSnapshot，不改写价格费率再沿用原版本。纯 hard TPM 可以不要求价格；有 hard cost 时必须有
合法价格，所有命中 hard cost 的币种一致且等于该价格币种，否则零派发。shadow 成本币种不能阻断纯 hard TPM。

所有时间规范化为九位纳秒 UTC。effective clock 不后退；活跃预留不因超过一分钟或租约 TTL 自动释放。
known 维度按 effective settlement 时间归入本系统窗口，TPM 60 秒、成本 24 小时；到期边界为 `now >= end` 才移出。
unknown 维度持有完整上界，归因时间取 `max(effective settlement/recovery, execution_expires_at)`，再经过对应窗口才移出。
首版明确采用本系统的归因滚动窗口，不提供供应商计费时间模式：本地取消/TTL 不能证明供应商已停止，不能宣称该规则
保证供应商实际执行或滚动账单的上限。活跃行与已中断/终结行必须分开处理；单纯 lease 到期不能把活跃行按零清掉。

窗口求和逐行 checked int64 加法，不使用 SQL SUM、浮点或饱和数。合法已验证 proof/price 的算术溢出映射固定 storage/
overflow 类，不冒充客户端参数错误或 unsupported。发现损坏父子状态、孤儿、漏 reservation 时失败关闭，无部分结果。

## 结算、联合恢复与续租

正常终结沿用冻结快照与现有 claimTerminal 顺序。在同一个事务中执行：
`FinishAttemptTx -> Budget.SettleTx -> FinishRequestTx -> model_requests -> governance.FinishTx`。
Settle 自行读持久 attempt 的 usage/price/cost，不接受 caller 填 known 或费用。实际值超过采用上界仍完整保存并隔离
profile；不截断 actual，不重新发请求。所有 sibling 故障一并回滚。

App 必须先完成全部 schema 升级，再进行一次联合恢复事务，最后才启动 worker 和 ready。恢复先核对完整关联关系、
freeze统一时间，再修改 accounting、预算和治理。旧会计校验不允许结束早于 pending 子/父时间，治理可取有效时钟 max；
联合协调器应先求合法全局恢复时刻，不删掉某个包的校验。失败不能先把 accounting 改为 interrupted 再忽略预算错误。

App 已接入联合恢复事务。续租在同一事务双 CAS 治理和
预算，Reserve 初值取当前治理 expiry，避免“续租完成但 reservation 尚未 attach”竞态。续租失败取消本地执行，但保留
旧持久截止与上界；终结先赢时不反向续租。

## 自动验收与拆分

- 两种已知旧库升级、所有 sibling 迁移/恢复写失败的回滚、修复重试及重启；不可借新版初始化代替旧库升级。
- 多 scope / 多历史 revision 同时预留；恰好阈值允许，溢出/币种/证明缺失零网络；更改策略不清零旧占用。
- Reserve、mark、settle 的数据库已提交但调用者收到错误；原 receipt 查询、原输入幂等、零上游重放。
- 四协议/legacy/账号池同一路径；过期、取消、重导入、Key 撤销、价格变更与预留屏障竞争。
- Token-known/cost-unknown、已知费用、真实 overage、profile quarantine 重启保留、unknown 窗口语义。
- 终结/续租/Close/重启联合事务，JSON 与 SSE 错误终帧，以及没有秘密/正文/正文hash落盘。

持久核心与测试可并行于严格迁移、服务集成夹具，但 App/共享派发/数据库迁移由根合并。全部仍需 Linux race 与隔离
进程验收；合成测试与真实 provider 验证分开。现有 preview.3 和主线用量观测 CI 不覆盖本批独立预算分支。
