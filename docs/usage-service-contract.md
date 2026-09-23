# 服务用量账本协调契约

状态：源码开发预览，2026-09-23。协调器已接入 `App`、四种协议 handler 和转发器，
进程回归、HTTP 用量/故障专项及完整 Go 回归通过。价格版本、管理员统计和网页已按
[用量管理契约](usage-management-contract.md) 接入；预算和正式账单仍未实现。

## 生命周期接口

- `newUsageLedgerCoordinator(*sql.DB)` 复用服务拥有的 SQLite 连接，不自行打开或关闭数据库。
- `start(ctx)` 先调用 `accounting.Ledger.Migrate`，再以当前 UTC 时间调用
  `RecoverInterrupted`。任一步失败都返回固定脱敏错误，调用方必须把它视为用量账本
  启动失败，不能继续宣称恢复成功。
- `beginRequest(ctx, usageRequestStart)` 在路由已经选定、provider 已知、上游执行准备
  尚未开始时记录员工请求，并返回请求对象。输入只含稳定 request ID、employee ID、
  key ID、公开模型、固定 `provider_kind`、显式用量协议和开始时间。
- `request.beginAttempt(ctx, accountID, startedAt)` 只在选定的上游 adapter 即将执行时
  记录 `requestID:1` 尝试。进入 adapter 后发生的本地校验失败仍属于一次真实执行尝试；
  adapter 尚未执行前的路由或准备失败调用 `finishWithoutAttempt`，请求可以失败、取消或
  中断并保持零 attempt。
- 服务接线调用 `beginPricedAttempt(ctx, accountID, startedAt, price)`：先通过启动时注入的
  价格查询器读取实际账号与上游模型的当前不可变快照，再记录 attempt。旧 `beginAttempt`
  是无价格的内部包装接口。价格目录查询失败拒绝 dispatch，不把失败解释为未配置。
- `attempt.observe(dataJSON)` 只接受协议转发器已经验证和接受的完整 JSON 或单个 SSE
  `data:` JSON，交给 `accounting.UsageAccumulator`。协调层只保留四个 nullable 计数，
  不保留或输出正文、工具参数、提示词、响应或原始错误。
- `attempt.finish(ctx, status, finishedAt)` 固定先结束 attempt，再结束 request。
  `finishWithoutAttempt` 只结束无 attempt 的 request，且拒绝成功状态。

协调器没有 HTTP 接口或后台队列，不自行管理价格目录、预算或账单，也不执行上游请求。
一次请求对象最多创建一个上游 attempt，调度固定为 `primary`，不会重试或创建
`retry`/`failover` attempt。未配置或已停用时 `BeginAttempt.Price` 为 `nil`；
即使用量已知，成本仍为 SQL `NULL`。配置价格只提供内部估算，不能冒充供应商账单。

## 服务接线

服务启动先迁移价格目录，再迁移并恢复账本；路由和权限检查通过后记录 request，在调用 HTTP transport
或会员 adapter 前记录 attempt。`count_tokens` 是估算接口，不写入生成用量账本。
入口拒绝、未找到路由等尚未接纳的请求不计为上游尝试。

JSON 和 SSE 转发器仅向累计器传递已经通过协议验证的事件。无效用量保留未知，
不会把未知计数当成零。终结账本失败会返回固定错误；流式已经输出的内容不可撤回。
Chat、Responses 和 Messages 的正常结束帧在落盘之后发送；Gemini 从含 finishReason 的
候选帧起有界缓冲尾帧，EOF 验证和落盘成功后才发送。成功记录描述上游已经完成，不保证客户端最终
收到所有字节。进程在账本终结后、客户端接收前断开仍可能保留成功用量。

每个活跃 request 的内存关联在 handler 返回时清理；未终结路径用有界后台 context
补记 interrupted，保存失败则保留 pending 供重启恢复。此时不宣称落盘成功。

## Provider 与协议隔离

入口必须同时传入实际选中路由的 `provider_kind` 和员工入口所用的
`accounting.UsageProtocol`。允许关系固定如下：

| `provider_kind` | 账本 provider | 允许的用量协议 |
| --- | --- | --- |
| `openai-compatible` | `openai-compatible` | Chat Completions、Responses |
| `codex-membership` | `codex` | Chat Completions、Responses |
| `anthropic-api-key` | `anthropic` | Anthropic Messages |
| `gemini-api-key` | `gemini` | Gemini generateContent |

Codex 的转换结果可能使用 Chat Completions，因此不能仅根据 provider 推断协议。
Anthropic 与 Gemini 的协议固定。未知 provider、未知协议或不匹配组合在写 request 前
返回 `errUsageLedgerInvalid`。

## 终结、取消与故障

终态只允许 `succeeded`、`failed`、`cancelled`、`interrupted`。第一次 finish 会固定
状态、完成时间和 attempt 的用量副本。相同状态的重复 finish 忽略后来传入的时间，
复用第一次快照并幂等重放账本写入；不同状态重放返回固定冲突错误。这样正常路径与
defer 清理不会因各自取了不同时间而制造冲突，也不会在重放时重新取用量。

独立协调器接口可分别结束 attempt 和 request；服务 HTTP 执行路径通过同一 SQLite 事务
结束 attempt、accounting request 和旧 model_requests 元数据。任一写入失败全部回滚，
不允许账本已成功而请求状态仍 running。相同快照可幂等重试，只有整个事务提交成功
才标记内存关联已终结。数据库错误不会被当作成功吞掉。传入 context 已关闭时，协调器用
独立的 3 秒 background context 完成落盘；若原 context 在写入期间关闭且写入报错，
也以同一幂等数据做一次 background 重试。最终失败只返回以下固定包内错误，不拼接
SQL、数据库路径、标识符、凭据或上游正文：

- `errUsageLedgerInvalid`
- `errUsageLedgerConflict`
- `errUsageLedgerUnavailable`

启动恢复只把遗留 pending request/attempt 标为 `interrupted`，不会填写 Token、成本或
成功状态。失败事件未提供合法 usage 时，四个 Token 字段和成本都保持 SQL `NULL`。

## 本批合成验证

测试使用临时 SQLite 与自行编写的 JSON/SSE 数据，覆盖四种 provider 的隔离、
OpenAI-compatible 与 Codex 的双协议选择、中文公开模型、无价格 attempt、JSON
完整用量、Anthropic 跨 SSE 事件累计快照、重复终结、取消 context 的 3 秒落盘、
零 attempt 本地失败、失败事件未知用量、持久化失败显式返回，以及重启恢复
`interrupted`。测试不访问网络、不读取真实凭据或真实请求内容。

底层账本与协议字段依据分别见
[内部用量与尝试账本核心契约](usage-ledger-contract.md)和
[用量协议映射](usage-protocol-mapping.md)。本协调层是依据上述仓库规格独立编写，
没有读取或移植参考 CPA 产品实现，也没有新增第三方依赖。
