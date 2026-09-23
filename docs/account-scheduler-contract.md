# 账号池调度核心契约

状态：独立可测试核心，尚未接入员工模型请求；2026-09-23。本模块不能据此宣称账号池已经上线。

## 边界与接口

`internal/scheduling` 是线程安全的单进程租约调度器，不访问 HTTP、数据库、凭据、提示、响应或日志。调用侧先完成员工 Key、模型权限和账号可见性检查，再通过 `Request.AllowedAccountIDs` 显式交付允许集合。调度器绝不从候选全集扩大权限。

候选只含账号 ID、provider、兼容模型、启用状态、优先级、权重、并发容量和外部冷却截止时间。请求只含 provider、模型、显式 allowlist、可选粘滞键和候选快照。返回 `Lease` 与固定 `ReasonCode`，没有凭据或模型内容。

```go
lease, decision := scheduler.Acquire(ctx, scheduling.Request{...})
released, releaseDecision := lease.Release(scheduling.ReleaseResult{...})
```

选择顺序为：allowlist → enabled/provider/model → 冷却与容量 → 最高优先级 → 同优先级权重。粘滞账号只在当前请求允许且可用的最高优先级池内复用；禁用、越权、冷却或满载时不会强行粘滞。

## 租约、等待与恢复

- `Acquire` 在容量暂不可用时进入有界等待队列；队列满、context 取消和无允许/兼容账号分别返回固定 reason。
- 每个租约占一个账号容量并带 TTL。`Release` 只生效一次；重复释放不重复减容量或延长冷却。
- TTL 到期由后续 Acquire、Release 或 Snapshot 回收，并唤醒等待者。调用侧应及时持久化 `Snapshot()`；重启时把尚未过期的快照传入 `Config.RestoredLeases`。过期快照被忽略，因此崩溃不会永久占位。
- Clock 和 Random 可注入。生产默认实现仅使用进程时间与本地伪随机选择；本模块没有跨节点一致性。未来多节点接线必须用共享租约存储重新设计，不能把单进程互斥当作分布式锁。

## 冷却与重放建议

调用侧用有限 `FailureClass` 释放租约。配置可为 rate limit、overload、transient、authentication 等类别设置冷却；内部冷却与候选携带的外部冷却取较晚截止时间。fake clock 测试覆盖冷却恢复。

`RetrySuggested` 只在 rate limited、overloaded 或 transient 且明确尚未提交流、执行结果也不确定时为 true。`StreamCommitted` 或 `ExecutionUncertain` 任一为 true 时始终 false；认证和永久失败也不建议自动重放。该布尔值只是安全资格，不是自动重试命令；次数、幂等、请求生命周期和员工可见错误仍由未来服务接线决定。

## 当前未接线项

本批没有修改 service、store 或 web：没有账号池表、管理员 CRUD、员工请求分派、持久化事务、跨进程租约、用量归属或真实故障切换。下一批接线必须自动验收权限过滤、revision 竞争、租约快照持久化/恢复、成功与失败状态写入顺序、已提交流绝不换号，以及日志不含凭据和模型内容。
