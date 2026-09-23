# 生成恢复探测：执行边界

状态：**基础模块已通过源码与 Linux CI 验收，默认关闭的后台协调器已接入源码并完成本地进程验收**。
独立账本、维护租约/隔离、固定 runner 和内部事务桥见[基础模块契约](system-probe-foundations-contract.md)。
后台开关、失败快照、Codex adoption 和重试接口见[细化实施契约](account-recovery-coordinator-contract.md)；
最终回归与最新 CI 结果见[集成状态](integration-status.md)。下载版仍不包含本批功能，合成测试不等于真实供应商验收。
依据本仓[上游测试与恢复契约](upstream-health-contract.md)、账号运行时和用量源码独立设计；不复制参考产品实现。

## 已确定的实现选择

保留单 Go 进程，生成恢复探测默认关闭。CLI 配置与系统状态必须分别表示是否允许生成探测；开关关闭时
不创建探测 worker、定时器或探测网络请求。仅迁移/读取本地状态不等于启动探测。关闭开关也不能自动删除
既有恢复隔离；管理员仍可按当前事件明确清除。

选择独立 `system_probe_attempts` 账本，不向员工账本加入假 employee、Key 或普通模型请求。现有
`accounting_requests`、`model_requests` 的身份外键、统计口径和迁移保持不变。四个 nullable token 桶、
不可变价格版本和整数成本算法可复用；探测统计只读取新表，不混入员工统计。

选择独立维护租约表，与员工租约使用**同一个** `scheduling.Scheduler` 容量计数。不新建独立容量池，
不调用要求 employee/key 的 `accountPoolRuntime.Acquire`，不从多个账号中自动择一。纯 scheduler 的
单候选 Acquire 可作为底层组件，但 runtime 必须先证明这一次维护操作获当前恢复配置授权。

恢复状态与历史操作分开持久化。当前隔离按账号唯一，绑定 cooldown event、操作 ID、账号/provider/来源、
公开模型、实际上游模型、协议以及池版本；历史探测记录不会随隔离清除而删除。目录测试记录不承担此职责。

## 第一小批：独立账本与事务接口

实现 `internal/accounting/system_probe.go` 及测试。固定 operation ID 表示一次探测 attempt，存储有界
元数据、派发阶段、起止时间、固定结果码、四桶用量、价格快照及 nullable cost；不保存正文或凭据。

账本提供接收调用方 `*sql.Tx` 的 Begin、MarkMayHaveSent、Finish 和 Interrupt 接口，避免在 service
已经持有单连接事务时再次从 DB 取连接。价格查询增加同样接受事务的 `CurrentTx` 路径。Begin 时按实际
账号和实际上游模型冻结价格，缺价、停用或用量未知时费用为 NULL，不能用 0 代替。

同 operation、同不可变输入返回现有状态；不同输入冲突。终结重试比较相同元数据快照，不重复计费。
账本 schema 严格验证对象、列类型/约束、索引及已有非法行；错误事务回滚、修复后可重试。
各现有模块独立迁移仍是独立事务，不能宣称全库升级原子。

## 第二小批：精确账号维护租约与恢复隔离

实现独立的 `accountMaintenanceLease`，持久化表不含 employee/key 字段。请求只提供 operation、账号、
事件及期望版本；模型、协议、endpoint 和实际映射从已保存恢复快照读取，不接受任意 URL 或请求正文。

复用跨所有启用模型映射的 `MIN(max_concurrency)` 作为账号全局容量。没有有效映射时拒绝，不能临时补一个
容量为 1 的账号。等待 scheduler 时不持有数据库事务、admission 或凭据 mutation 锁；选中后重新检查所有
版本、精确映射、enabled、恢复事件和操作状态，再落盘维护租约。

`MarkDispatch(ctx)` 必须先把 `DispatchNotStarted → MayHaveSent` 持久化提交，成功后才能调用 HTTP Do
或 Codex executor。它不能复用目前只更新内存阶段的员工 lease.MarkDispatch。任意提交失败均零派发。
心跳、取消、TTL 和进程关闭应保守释放；重启恢复员工和维护租约的共享容量，不能因来自不同表而漏计。
恢复前必须拒绝跨表重复 lease ID；不能让 scheduler 的重复项跳过行为低估实际占用。

员工候选读取必须排除持久化恢复隔离中的账号，即使 cooldown 截止时间已经过去。恢复状态引用的 cooldown
事件不能被普通过期清理提前删除，避免失去手工 clear 与 worker 的共同 CAS 条件。列表分别展示冷却是否到期
和恢复隔离是否存在；“到期”不代表已经通过生成探测。

Codex 在取得维护租约前、transition/mutation/admission 锁之外调用共享凭据入口。只有同一事件、操作、
来源/client binding 和明确的刷新转换仍匹配时，才可采纳返回的新 tested revision；重导入不属于此例外。
取得维护租约及 MarkDispatch 时再次核对 tested revision，失败即销毁可清零凭据并停止。

共享锁修改前必须逐条审阅现有路径。候选等待在锁外；最终准入使用 cooldown transition、必要的账号 mutation、
admission、SQLite 的固定顺序；终结使用 lease mutex 后进入同一变更序列。任何现有 mutation/admission
持有者都不能反向取得 transition。实现须用屏障测试证明无自锁和死锁，不能只依靠此草案的顺序说明。

## 第三小批：后台协调器与固定协议 runner

新增 `internal/service/account_recovery.go`，默认关闭，单个全局 worker、有界超时和退避。触发事件必须带实际
失败路径的模型和协议快照；旧记录没有这些字段时等待明确配置，不能随机挑目录模型。Release 保留较长
cooldown 的原因时，也必须保留与该原因一致的探测路径，不能把不同模型的新失败信息拼成一个虚假快照。

runner 只发送仓库中固定的最小合成输入，复用现有协议解析和凭据获取，不调用员工 HTTP handler。API Key、
Codex、Claude Messages 与 Gemini 的完整成功终态分别验证；部分 SSE、失败事件、EOF、取消或未知执行结果
都不能产生 generation_ok。未知/不支持的协议明确阻塞，不以另一种协议请求成功替代。

终结时在**同一事务**写探测 attempt/费用、CAS 恢复状态、条件处理当前 cooldown 并删除维护租约。
只有完整成功且全部元数据落盘成功，才能解除匹配事件的隔离。提交后再更新 scheduler 内存并 NotifyChanged。
CAS 已失配时，仍记录实际探测用量和 stale/configuration_changed，但只能释放自身容量，不能覆盖新事件。

clear 必须在现有事务内清除同一事件的恢复隔离并取消旧操作；迟到 worker 不得重新建立 cooldown。
只有 cooldown 和恢复隔离都不存在时才可返回 already_clear；提交后先推进配置代际，再开放内存候选。
存储故障整体回滚、保持隔离。重启将遗留操作标为 interrupted，不重用原 operation 调用上游；允许的后续
周期只能在持久化 next_probe_at 之后，以新操作明确记录一次新的探测消耗，不能重启后立即重放不确定请求。

## 自动验收与文件所有权

- 账本任务：仅 accounting 新模块及必要的价格事务接口；旧员工统计逐字不变、版本化价格、NULL 用量/成本、
  幂等、失败回滚与修复重试。服务层数据库和路由由总协调统一接线。
- 运行时任务：仅独立维护租约、恢复隔离与必要 scheduler 接口；精确账号、跨模型全局容量、最终版本屏障、
  MarkDispatch 写失败零网络、取消/心跳/TTL、重启共享容量及 clear/迟到 Release 竞争。
- 协议任务：先固定无正文 runner 契约再实现四协议；合成 transport 验证终态、失败/SSE/取消、输出界限和零换号。
- 总协调：迁移顺序、App 生命周期、CLI 默认关闭配置、管理状态/统计接口、网页开关说明和真实隔离进程验收。
  功能矩阵同时记录入口、执行、持久化、后台生命周期及证据，不把单个模块完成当成恢复探测已交付。

全部开发验收使用合成凭据和模拟上游。普通提交只跑轻量 CI；本草案不安排真实生成调用、不读取用户账号，
不发布新 tag 或安装包。代理池、预算、治理运维及商业能力继续保留在[完整功能矩阵](feature-parity-plan.md)中。
