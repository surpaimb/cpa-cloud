# 账号池调度核心契约

状态：源码开发预览，2026-09-23。独立核心已通过[账号池运行时](account-pool-runtime-contract.md)接入四协议员工模型请求、持久化租约和网页配置，并通过隔离进程验收；没有发布新安装包。

## 边界与接口

`internal/scheduling` 是线程安全的单进程租约调度器，不访问 HTTP、数据库、凭据、提示、响应或日志。调用侧先完成员工 Key、模型权限和账号可见性检查，再通过 `Request.AllowedAccountIDs` 显式交付允许集合。调度器绝不从候选全集扩大权限。

候选只含账号 ID、provider、兼容模型、启用状态、优先级、权重、并发容量和外部冷却截止时间。请求只含 provider、模型、显式 allowlist、排除账号集合、可选粘滞键和候选快照。返回 `Lease` 与固定 `ReasonCode`，没有凭据或模型内容。

```go
lease, decision := scheduler.Acquire(ctx, scheduling.Request{...})
released, releaseDecision := lease.Release(scheduling.ReleaseResult{...})
```

选择顺序为：allowlist 且不在排除集合 → enabled/provider/model → 冷却与容量 → 已有粘滞绑定 → 新选择的最高优先级 → 同优先级权重。已有粘滞账号只要仍在当前请求允许、兼容、未冷却且有容量的集合中就优先复用；priority 只影响没有可复用绑定时的新选择。禁用、越权、排除、冷却或满载时不会强行粘滞。未冷却不代表真实供应商生成健康已验证。

粘滞绑定有独立 TTL 和容量上限；复用或重新选择会刷新该键的 TTL，过期绑定会清理，达到上限时淘汰最早到期的绑定。因此，来自大量会话键的输入不会让进程内映射无限增长。候选数量、单账号权重和粘滞键长度也有界；权重累加在调用随机选择前检查整数溢出，越界请求返回 `invalid_request`。

## 租约、等待与恢复

- `Acquire` 在容量暂不可用时进入有界等待队列；队列满、context 取消和无允许/兼容账号分别返回固定 reason。
- 每个租约占一个账号容量并带 TTL。调用侧必须在长请求仍活跃时周期调用 `Lease.Renew()`，心跳间隔应短于租约 TTL；续租把有效期延到当前时间加 TTL。已经到期或释放的租约不能续租，也不会重新取得容量。`Release` 只生效一次；重复释放不重复减容量或延长冷却。
- TTL 到期由后续 Acquire、Release 或 Snapshot 回收，并唤醒等待者。调用侧应及时持久化 `Snapshot()`；重启时把尚未过期的快照传入 `Config.RestoredLeases`。过期快照被忽略，因此崩溃不会永久占位。
- Clock 和 Random 可注入。生产默认实现仅使用进程时间与本地伪随机选择；本模块没有跨节点一致性。未来多节点接线必须用共享租约存储重新设计，不能把单进程互斥当作分布式锁。

## 冷却与重放建议

调用侧用有限 `FailureClass` 释放租约。配置可为 rate limit、overload、transient、authentication 等类别设置冷却；内部冷却与候选携带的外部冷却取较晚截止时间。fake clock 测试覆盖冷却恢复。

`RetrySuggested` 只在显式 `Phase=DispatchNotStarted`、失败类为 rate limited、overloaded、transient、authentication 或 permanent，且两个旧风险字段没有正向危险证据时可能为 true。`DispatchUnknown` 是零值，默认拒绝；`MayHaveSent` 和 `OutputCommitted` 永远拒绝。旧 `StreamCommitted=false` 与 `ExecutionUncertain=false` 不能单独授权换号；任一为 true 仍然拒绝。该建议只是核心提供的资格，运行时还检查已观察到的执行阶段、取消、过期与持久化结果，服务协调器最后限制为[模型派发前一次账号预检换号](account-pool-failover-contract.md)。

## 服务集成与剩余范围

服务已有账号池表、管理 API、网页编辑、四协议请求分派、租约持久化/重启恢复，以及请求与尝试的用量归属；权限过滤、revision 竞争和撤销已自动验收。源码已增加一次有界账号预检换号，具体本批验证记录见[集成状态](integration-status.md)。跨进程调度、执行后的故障重放、恢复探测、配额和代理仍未实现；进入模型 HTTP/Codex 执行器后绝不换号，日志不含凭据和模型内容。
