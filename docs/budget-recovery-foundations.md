# 预算恢复事务基础

状态：**内部事务基础已实现，联合恢复尚未接入服务生命周期，hard budget 尚未完成。**

本文记录 CPA Cloud 独立规格中的恢复事务边界。实现不读取请求正文、响应、错误正文、员工密钥或上游凭据，也不执行或重放任何上游请求。

## 目的和范围

治理请求与用量账本将来需要由一个服务协调器在同一 SQLite 事务中恢复。当前批次只提供包级事务原语：

```go
func (c *governance.Coordinator) RecoverInterruptedTx(
    ctx context.Context,
    tx *sql.Tx,
    at time.Time,
) (governance.RecoveryResult, error)
```

调用方拥有 `tx`。方法不会提交或回滚；返回成功仅说明变更已写入当前事务，不能说明已经持久化。调用方必须处理 `Commit`，而提交结果不确定时不得声称“没有落盘”，也不得换一个恢复 ID 或重放任何上游工作。旧的 `RecoverInterrupted` 是便利包装：它自行开始事务，调用同一内部恢复逻辑，然后提交。

本批次没有修改 schema、App 启动/关闭、HTTP、管理开关或预算策略，也没有把治理恢复与 accounting 恢复接入同一个生产事务。服务当前仍分别调用各包恢复；因此不能把这些原语描述为联合原子恢复已经上线。

## 治理恢复语义

事务开始后，治理恢复先锁定并严格读取唯一 settings 行，再以调用方提供的合法 UTC 观测时刻计算恢复时刻：

- effective 恢复时刻是 `max(at, last_effective_admission_at, request.effective_lease_at)`；系统时钟回拨不会写出更早的 effective 结束时间。
- 每个仍为 `pending` 的请求变为 `interrupted`。终态请求保持不变；重复恢复返回零变更。
- `observed_finished_at` 保存调用方提供的 `at`，`effective_finished_at` 保存上述保守 effective 时刻。
- 原 `expires_at` 永不延长或缩短。若原租约在 effective 恢复时刻尚未到期，`released_at` 保持 `NULL`，并发占用继续到原 TTL。若已经到期，`released_at` 固定为原 `expires_at`，不能写成较晚的启动恢复时刻。
- 恢复不填写 Token、成本或成功状态，不创建 attempt，不修改 scope 快照，也不把进程重启当成上游已停止的证明。

非法 coordinator、nil context、nil transaction 或非法/非 UTC 时间返回固定 `ErrInvalid`。取消、已关闭事务和 SQLite 读写失败返回固定存储错误，不泄露 SQL、路径或行内容。读取结果的迭代或关闭失败会使事务原语失败，由调用方回滚整体事务。

## accounting 对应原语与时间差异

用量账本在根集成分支提供对应的调用方事务原语：

```go
func (l *accounting.Ledger) RecoverInterruptedTx(
    ctx context.Context,
    tx *sql.Tx,
    at time.Time,
) (accounting.RecoveryResult, error)
```

两包的时间规则有意不同，联合协调器不能借一方规则弱化另一方：

- accounting 保持现有 `validateRecoveryTime` 规则，严格拒绝早于任一 pending 父请求或 pending attempt 的起始时刻，或早于该 pending 父请求下已经终结的 child attempt `finished_at` 的恢复时间；它不会自行把调用方的 `at` 抬高。
- governance 保持持久 effective clock 的 `max` 钳制，允许观测时钟回拨但只会保守延后 effective 结束和释放判定。
- 未来联合协调器必须在同一个联合写事务内读取完整相关 stored facts，确定并校验一个不早于这些事实的统一恢复时刻，再把同一个时刻传给两个包；不能在事务外先读后假定数据未变化。不能捕获 accounting 的时间错误后改用治理钳制结果，也不能跳过 governance 的持久时钟。

下一批联合接线以[持久核心与服务契约](budget-persistence-integration-contract.md)为准：启动恢复发生在 App ready 和后台 worker 启动前，不凭空要求服务 admission 锁；在一个 SQLite 写事务内调用两个包的 `RecoverInterruptedTx`、写入必要的 sibling 状态，最后只提交一次。任一包或 sibling 写失败都回滚全部变更。事务提交未知时只能用相同输入检查持久状态；不得假定失败、重复上游请求或伪造成功。此启动恢复边界也不能泛化为普通派发锁序；普通派发现有顺序是 account lease、mutation、admission、SQLite 事务，后续实现必须遵守对应集成契约。

## 已验证边界

真实 SQLite 合成测试覆盖：

- 调用方回滚，以及恢复后 sibling 写失败再整体回滚；
- 提交前其他连接只能看到旧状态，提交后能看到准确 interrupted 数量；
- 重复恢复无操作，既有终态不变；
- 未到期租约继续占用，已到期租约在原到期时刻释放；
- 持久 effective clock 在系统时钟回拨时保持单调；
- nil、非法时间、取消 context 和已关闭事务使用固定错误。

这些测试只证明事务原语和治理元数据语义。它们不证明 hard TPM/成本预算、预算预留/结算、联合 App 恢复或生产账单已经实现。

### 根任务组合验收

2026-09-24 根任务在同一隔离 SQLite 中分别按 accounting-first 与 governance-first 调用两个 Tx 接口。
插入受约束的 sibling 回执失败后，三个真实表均保持 pending；共同提交后全部 interrupted，未知 Token/cost 仍为 NULL，
未到期治理 lease 的原 expires/released 状态保持。测试明确只证明可组合原语，不运行或声称 App 已联合恢复预算。

根 governance 全包（含该交叉测试）非缓存 26.916s、accounting 全包 17.271s、service 用量/治理取消与重启专项 7.868s
均 PASS；三包 vet、最终程序编译及真实临时进程观测 smoke PASS。所有数据和上游均为合成；没有读取真实凭据。
此独立分支尚未运行 Linux race，后续完整预算增量统一进行轻量 CI。
