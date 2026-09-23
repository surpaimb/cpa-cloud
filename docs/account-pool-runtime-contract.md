# 账号池运行时适配契约

状态：2026-09-23 源码已接入 Chat、Responses、Messages 和 Gemini HTTP 处理器；既有账号池接线的隔离进程与真实浏览器证据见 [集成状态](integration-status.md)。当前源码另实现下述有界预检换号，仍待本轮总验收；未提供包含该能力的新安装包，也不表示已经发布。执行后的自动换号、恢复探测和代理池仍未实现。

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

func (rt *accountPoolRuntime) AcquireWithOptions(
    ctx context.Context,
    publicModel string,
    auth employeeAuth,
    allowedProviders []string,
    stickyOpaque string,
    options accountPoolAcquireOptions, // exclusions + expected pool revision
) accountPoolAcquireResult

func (l *accountPoolLease) Context() context.Context
func (l *accountPoolLease) PoolRevision() int64
func (l *accountPoolLease) MarkDispatch()
func (l *accountPoolLease) MarkOutput()
func (l *accountPoolLease) Heartbeat(ctx context.Context) accountPoolRuntimeCode
func (l *accountPoolLease) Release(ctx context.Context, result scheduling.ReleaseResult) (bool, accountPoolReleaseResult)
func (rt *accountPoolRuntime) NotifyChanged()
func (rt *accountPoolRuntime) Close() error
```

`Acquire` 之前，协议处理器必须按现有方式完成员工 Key 认证，并用固定的协议能力生成 `allowedProviders`；不能从客户端输入扩大 provider 范围。`stickyOpaque` 只接受调用侧生成的有界 HMAC。运行时再加入 employee ID 与公开 model namespace；不会保存该值，也不会从请求正文、提示或响应生成粘滞键。

没有显式 `model_account_pool_configs` 记录时，结果为 `Legacy=true` 和 `Code=legacy_no_pool`，且没有租约。调用侧必须重新锁内检查 Key、员工状态、model policy 和旧单路由，然后继续原有行为。这样现有用户不会因为仅安装运行时表而获得并发或冷却限制。

显式池成功时，调用侧必须用 `Lease.Context()` 执行上游请求并 `defer Release`。运行时每个租约自动以 TTL 的三分之一为间隔续租；公开 `Heartbeat` 供显式控制与测试。续租或持久化失败会取消 `Lease.Context()`，调用侧不得继续执行请求。

服务协调层只在模型 HTTP 或 Codex executor 尚未进入时允许一次换号：首账号发生明确的账号特定本地预检失败后，先成功释放旧租约，再以首次 `PoolRevision()`、首账号排除集及原授权条件调用 `AcquireWithOptions`。revision 改变、池消失、权限变化、排除后无候选或第二个账号预检失败都会终止请求。sticky 选择不能越过排除集。

执行证据采用正向状态。零值 `DispatchUnknown` 拒绝换号；新租约从 `DispatchNotStarted` 开始，调用 HTTP `Do` 或 Codex executor 前必须调用 `MarkDispatch` 单调推进到 `MayHaveSent`，开始向客户端提交输出时可调用 `MarkOutput` 推进到 `OutputCommitted`。进入 `MayHaveSent` 后，无论连接错误、HTTP 状态、协议错误、取消或尚未输出，都绝不重放本次请求。

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

等待 scheduler 时不持有 `App.admission` 锁。管理员或后台生命周期任务提交可能改变授权、路由或凭据状态的事务后，必须调用 `NotifyChanged`。该调用推进配置代际并非阻塞地唤醒等待者；等待者取消当前 scheduler 等待，重新读取授权和池快照。Key 或员工已失效时返回 `authorization_changed`；已等待请求观察到池、账号 revision、凭据状态或容量变化时返回 `configuration_changed`，由调用方要求客户端重新提交，不在本层自动换号。与当前等待请求无关的通知会用新代际继续等待，且不占用额外 waiter 名额。通知不会取消已经返回给执行器的 lease 或已开始的 stream。

运行时在读取代际、查询 SQLite、注册通知取消和进入 scheduler 前后都检查同一代际，避免提交恰好发生在“查询完成、开始等待”之间时丢失唤醒。通知由已提交事务触发，不替代事务中的 revision compare-and-swap。没有通知的进程外数据库修改不在单进程运行时契约内。

取得内存租约后，运行时重新取得读锁并在一个 SQLite 事务中复核：

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

`Release` 只处理一次。它先在一个事务中写入需要的 cooldown 并删除持久化租约，提交后才释放内存容量。持久化失败时返回 `storage_unavailable`，取消请求并让内存与数据库租约保守地保持到原 TTL；重复 Release 返回 `already_released`。`RetrySuggested` 只有在显式 `DispatchNotStarted`、租约自身没有更晚执行证据、context 未取消、旧风险字段没有正向危险证据且失败类允许时才可能为 true；零值或两个旧布尔字段为 false 不能授权换号。该值只是运行时证据，服务协调层仍只允许上述一次预检换号。

cooldown 按 account ID 持久化，重启后仍生效。同一账号收到重叠 cooldown 时，数据库只保留截止时间更晚的完整事件；`failure_class`、`cooldown_until` 和 `updated_at` 始终来自同一次 Release，不把较短的新失败类型拼到较长的旧截止时间上。过期数据在启动时清理。当前实现仍是单服务进程 scheduler；SQLite 恢复是崩溃保守占位，不是多节点分布式租约协议。

`Close` 会阻止新 Acquire，取消正在等待的 Acquire 和所有租约 context，停止 heartbeat 并等待运行时 goroutine 退出。它不会提前删除活跃租约；重启仍按持久化的原到期时间保守恢复。

## 固定结果码

运行时只返回固定 code：`acquired`、`legacy_no_pool`、`invalid_request`、`model_not_allowed`、`no_compatible_account`、`capacity_unavailable`、`queue_full`、`cancelled`、`authorization_changed`、`configuration_changed`、`account_changed`、`storage_unavailable`、`closed`、`released` 和 `already_released`。这些 code 不包含 SQL、凭据、请求正文或上游响应。

## 源码接线约束

1. 在 account-pool 配置迁移之后调用 `s.migrateAccountPoolRuntime(ctx)`。
2. `Open` 创建共享 `accountPoolRuntime`；`App.Close` 在关闭 store 前调用 runtime `Close`。
3. 四协议处理器完成现有认证后释放 admission 锁，再调用 `Acquire`。显式池成功时使用返回 route 和 `Lease.Context()`；legacy 结果继续旧单路由并重新锁内校验。
4. 所有协议和 count-tokens 路径都必须 Release。只有本地账号预检失败可经共享协调层执行一次固定 revision、排除首账号的换号；`MarkDispatch` 后不得进入任何换号入口。
5. 下列写入成功提交后调用 `accountPool.NotifyChanged()`：撤销 Key、更新员工状态、更新员工模型权限、更新/停用上游、写入模型池、替换凭据，以及 OAuth 刷新导致 upstream revision、credential state、source 或 client binding 变化。回滚或提交失败时不得通知。
6. 当前源码及合成验收记录不能替代本轮总验收，也不能用来声明新安装包已经发布。

本实现依据本项目规格独立编写，没有复制 CLIProxyAPI、Sub2API 或归档 CPA 的实现、迁移或测试。
