# 账号池运行时适配契约

状态：独立运行时适配层，尚未接入 Chat、Responses、Messages 或 Gemini HTTP 处理器；2026-09-23。本文件不能用于宣称实时账号池调度已经上线。

## 接口与调用边界

根服务接线使用以下 package-private 接口：

```go
func (s *store) migrateAccountPoolRuntime(ctx context.Context) error
func newAccountPoolRuntime(app *App) (*accountPoolRuntime, error)

func (rt *accountPoolRuntime) Acquire(
    ctx context.Context,
    publicModel string,
    auth employeeAuth,
    allowedProviders []string,
    stickyOpaque string,
) accountPoolAcquireResult

func (l *accountPoolLease) Context() context.Context
func (l *accountPoolLease) Heartbeat(ctx context.Context) accountPoolRuntimeCode
func (l *accountPoolLease) Release(ctx context.Context, result scheduling.ReleaseResult) (bool, accountPoolReleaseResult)
func (rt *accountPoolRuntime) Close() error
```

`Acquire` 之前，协议处理器必须按现有方式完成员工 Key 认证，并用固定的协议能力生成 `allowedProviders`；不能从客户端输入扩大 provider 范围。`stickyOpaque` 只接受调用侧生成的有界 HMAC。运行时再加入 employee ID 与公开 model namespace；不会保存该值，也不会从请求正文、提示或响应生成粘滞键。

没有显式 `model_account_pool_configs` 记录时，结果为 `Legacy=true` 和 `Code=legacy_no_pool`，且没有租约。调用侧必须重新锁内检查 Key、员工状态、model policy 和旧单路由，然后继续原有行为。这样现有用户不会因为仅安装运行时表而获得并发或冷却限制。

显式池成功时，调用侧必须用 `Lease.Context()` 执行上游请求并 `defer Release`。运行时每个租约自动以 TTL 的三分之一为间隔续租；公开 `Heartbeat` 供显式控制与测试。续租或持久化失败会取消 `Lease.Context()`，调用侧不得继续执行请求。

## 选择与重查

初始查询只检查公开模型是否启用、员工/Key 是否仍有效、selected model policy、显式池 revision 和池内账号。它不依赖 `models.upstream_id` 指向的旧默认上游是否启用，因此显式池可以在旧默认路由停用时工作。

员工或 Key 失效返回 `authorization_changed`；员工的 selected model policy 不允许该模型返回 `model_not_allowed`；公开模型不存在或已停用返回 `no_compatible_account`。这三类检查相互独立，模型状态不会被误报为员工 Key 失效。

候选必须同时满足：

- provider 在调用侧的 `allowedProviders` 中；
- 池中配置的上游账号存在并启用；
- Codex membership feature 已开启且账号不是 `reauth_required`；
- 上游模型来自当前池成员，priority、weight 和并发来自已验证配置；
- 持久化 cooldown 已过期或由 scheduler 等待到期。

同一 `upstream_id` 的容量和 cooldown 在整个 runtime 内全局共享，跨 employee、公开模型和协议入口一致。账号出现在多个启用公开模型的显式池中时，有效容量取这些路由配置的最小 `max_concurrency`。例如 pool A 配 1、pool B 配 10，合计最多只有 1 个活跃租约。运行时只计算该保守值，不修改管理员保存的配置。

等待 scheduler 时不持有 `App.admission` 锁。取得内存租约后，运行时重新取得读锁并在一个 SQLite 事务中复核：

1. employee 仍 active，Key 未撤销且未过期，model policy 仍允许公开模型；
2. 公开模型和显式池仍存在，pool revision 未变化；
3. 选中账号仍启用，账号 revision、provider、upstream model 和 Codex 状态未变化；
4. 该账号跨模型的全局最小容量未变化；
5. 租约选择元数据成功持久化。

任一复核失败都会释放内存租约并返回固定 code；失败结果不带 route 或凭据密文。只有全部成功后才返回当前事务读取到的 route，因此不会把等待前的旧密文交给执行器。

## 持久化、恢复与故障

`account_pool_runtime_leases` 只保存 lease ID、account ID、公开 model、employee/key ID、pool/account revision、到期时间和创建时间。`account_pool_runtime_cooldowns` 只保存 account ID、固定 failure class、冷却截止时间和更新时间。两表不保存 Authorization、Key 明文、提示、响应、token、sticky 值或凭据密文。

启动时删除已过期的租约和 cooldown，并把尚未到期的租约按原截止时间恢复到单进程 scheduler。恢复租约会继续占用账号全局容量，防止进程重启后对仍可能执行的旧请求重复放行；不会把 TTL 从启动时间重新计算。

运行时表中的时间统一写为 UTC 固定九位小数格式 `2006-01-02T15:04:05.000000000Z`，使 SQLite 文本排序与时间顺序一致。启动恢复先用 Go 解析所有已有时间，再判定过期并把仍有效的旧格式值规范化；不会对可变小数位的 RFC3339 文本直接做字典序过期判断。

`Release` 只处理一次。它先在一个事务中写入需要的 cooldown 并删除持久化租约，提交后才释放内存容量。持久化失败时返回 `storage_unavailable`，取消请求并让内存与数据库租约保守地保持到原 TTL；重复 Release 返回 `already_released`。rate limit、overload 和 transient 只有在 `StreamCommitted=false` 且 `ExecutionUncertain=false` 时返回 `RetrySuggested=true`。该值只表示安全资格，本层不自动换号或重放请求。

cooldown 按 account ID 持久化，重启后仍生效。过期数据在启动时清理。当前实现仍是单服务进程 scheduler；SQLite 恢复是崩溃保守占位，不是多节点分布式租约协议。

`Close` 会阻止新 Acquire，取消正在等待的 Acquire 和所有租约 context，停止 heartbeat 并等待运行时 goroutine 退出。它不会提前删除活跃租约；重启仍按持久化的原到期时间保守恢复。

## 固定结果码

运行时只返回固定 code：`acquired`、`legacy_no_pool`、`invalid_request`、`model_not_allowed`、`no_compatible_account`、`capacity_unavailable`、`queue_full`、`cancelled`、`authorization_changed`、`configuration_changed`、`account_changed`、`storage_unavailable`、`closed`、`released` 和 `already_released`。这些 code 不包含 SQL、凭据、请求正文或上游响应。

## 根任务接线清单

1. 在 account-pool 配置迁移之后调用 `s.migrateAccountPoolRuntime(ctx)`。
2. `Open` 创建共享 `accountPoolRuntime`；`App.Close` 在关闭 store 前调用 runtime `Close`。
3. 四协议处理器完成现有认证后释放 admission 锁，再调用 `Acquire`。显式池成功时使用返回 route 和 `Lease.Context()`；legacy 结果继续旧单路由并重新锁内校验。
4. 所有协议和 count-tokens 路径都必须 Release。根据既有请求生命周期填写有限 `FailureClass`、`StreamCommitted` 和 `ExecutionUncertain`；不得在本批自动重试或换号。
5. HTTP 接线完成并通过四协议端到端测试之前，feature/status 必须继续显示 pool routing 未启用。

本实现依据本项目规格独立编写，没有复制 CLIProxyAPI、Sub2API 或归档 CPA 的实现、迁移或测试。
