# 账号池安全换号契约

状态：**源码已接线，正在完成本批整体验收**。截至 2026-09-23，四协议的账号预检、一次安全切换、租约和实际账号账本已接入；公共协调器及隔离真实 Go 进程验收通过，完整测试与 CI 结果见[集成状态](integration-status.md)。不包含在 preview.3 安装包中。
本文定义本批最小范围，依据本仓协议、租约和账本约束独立编写，不参考原产品源码。

## 范围与安全边界

同一请求最多切换一次：首个账号发生账号特定、且能证明模型请求尚未派发的预检失败后，才可选择
一个不同账号。第二个账号失败、池已耗尽或没有显式账号池时直接返回固定错误；旧单路由保持原行为。

执行证据使用正向枚举，不能从“尚未观察到输出”推断安全：

- `Unknown`：零值；证据不足，拒绝换号。
- `DispatchNotStarted`：尚未进入任何模型请求执行器，允许继续评估换号。
- `MayHaveSent`：已经进入 HTTP `Do` 或 Codex executor；即使返回错误也可能已发送，禁止换号。
- `OutputCommitted`：已向客户端写入状态、正文或 Flush 流事件，绝对禁止换号。

只有显式 `DispatchNotStarted` 才可能换号。调用 `http.Client.Do`、`codex.Complete`、
`codex.Stream` 或 `codex.Responses` 前必须先把状态推进为 `MayHaveSent`，之后不可回退。

## 可换号与必须终止的失败

可换号的预检失败必须由所选账号自身造成，例如：

- provider、Key 版本或凭据状态与协议能力不兼容；
- 该账号凭据无法在本地解密；
- 该账号 endpoint 或目标 URL 无效；请求/协议模型映射错误直接终止，不以切换账号绕过校验；
- 无法为该账号构造上游 HTTP request；
- Codex 凭据获取或刷新在模型 executor 调用前确认失败，并且所需的凭据状态或刷新暂停标记已成功提交；
  不确定的刷新结果不能误标为明确撤销，也不能重放该账号的旧 refresh token。

请求正文、协议字段或 Codex mapping 无效属于请求级错误，不换号。员工/Key 撤销、模型权限变化、
客户端取消、配置代际变化也直接终止。

SQLite、价格目录、账本 Begin/Finish、审计或租约持久化错误属于全局一致性错误，不能通过换账号绕过。
未配置或显式停用价格返回 `nil`，不是错误；价格查询返回错误时不得 dispatch，也不得换号。

进入 HTTP `Do` 或 Codex executor 后，以下结果全部禁止重放：连接错误、超时、EOF、取消、任意
HTTP 状态（包括 401、429、503）、响应头或正文、JSON/SSE 协议错误，以及已经收到上游结果但尚未
写给客户端的情况。429、overload 和 transient cooldown 只影响后续独立请求，不代表当次可安全换号。

## 选择、租约与并发

首次 Acquire 返回的 pool revision 是本请求的初始 revision。后续选择必须同时传入：

- 初始 pool revision；revision 改变时返回 `configuration_changed`；
- 已失败账号的排除集；sticky 绑定不得越过排除集重新选择同一账号；
- 原 employee、Key、公开模型和协议允许的 provider 集合。

每次 Acquire 都重新检查 employee、Key 到期/撤销、model policy、公开模型状态、账号 enabled/revision、
provider、Codex 状态和全局最小容量。不得复用首次读取的凭据或 route。

旧 lease 必须先以账号特定 failure class 和当前执行证据 Release。只有 cooldown 与租约删除在同一事务
成功提交、内存容量成功释放后，才能 Acquire 第二个账号。Release 返回 `storage_unavailable`、租约已过期
或状态不确定时拒绝换号，沿用现有租约保守恢复逻辑；不能同时持有两个可执行 lease。

`RetrySuggested` 只能由显式安全证据产生。旧的 `StreamCommitted=false` 与
`ExecutionUncertain=false` 负向布尔组合不得继续作为授权，避免零值被误判为安全。

## 父请求、attempt 与价格

员工可见父 request、`model_requests` 和 `accounting_requests` 每个客户端请求只创建一次。首个账号的
预检失败不得提前结束父 request；全部候选耗尽后才以最终失败结束。

尚未派发模型请求的候选不创建 `accounting_attempts`，也不产生 token 或成本。实际 dispatch 时才创建
attempt：直接使用首个账号时为 `primary`；预检换号后实际派发的账号标为 `failover`。价格必须用该次
实际选择的 `account_id + upstream_model` 查询并把不可变快照写入 attempt，不能沿用首个 route 的模型。

服务层账本接口不得继续把实际模型固定在父 request；入口应接收当前 route 和 dispatch 类型。本批
最多产生一个已派发 attempt，可保留现有 `:1` ID 规则，不增加虚假尝试。只有未来获得可审计
`NeverSent` 证据、需要多 attempt 时，才另行实现尝试序号和“结束 attempt、父 request 仍 pending”。

最终 attempt、父 accounting request 和 legacy `model_requests` 仍需在一个事务中结束。账本终结失败时
不得发送正常 JSON 或成功流终帧，也不得通过换号重放已经执行的请求。

## 共享文件分工

- `internal/scheduling/scheduler.go`：正向执行证据、排除集及安全建议，不处理 HTTP 或凭据。
- `internal/service/account_pool_runtime.go`：初始 revision、排除集、逐次重查、持久化 Release 和第二次 Acquire。
- `internal/service/model_admission.go`：父 request 一次建立、最终 lease 清理；`model_preflight.go`：结构化账号预检、一次换号及失败终结。
- `internal/service/usage_hooks.go`、`usage_ledger.go`：按实际 route 定价、dispatch 类型和原子终结。
- 四协议处理器：`modelapi.go`、`responses.go`、`anthropic_messages.go`、`gemini_native.go`、
  `codex_chat.go` 只返回结构化预检结果；不得自行扩大安全边界。
- 只有未来要在 executor 返回后换号时，才允许修改 `internal/membership` 适配器以提供可审计发送证据。

## 验收

自动测试必须证明：首个账号预检失败只切换一次且不会重选；pool revision、Key、权限或账号状态变化会
终止；Release 持久化失败不会取得第二个 lease；父 request 只有一条；未派发候选没有 attempt；实际
attempt 使用第二个账号和实际模型的价格快照；价格/账本失败零上游调用。

四协议和 count-tokens 均需覆盖。测试还必须证明：`Unknown`、任意 HTTP `Do` 错误/状态、Codex executor
错误、收到上游数据、SSE 首次 Flush、客户端取消和终结账本失败都不会调用第二个账号。日志、错误、
租约和账本不得保存 Authorization、员工 Key、上游凭据、sticky 原值、提示或响应正文。
