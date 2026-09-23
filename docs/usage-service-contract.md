# 服务用量账本协调契约

状态：内部协调层开发预览，2026-09-23。本批只新增
`internal/service` 内的 package-private 协调器和合成测试；尚未把协调器接入
`App`、HTTP handler、各协议转发器或价格配置，不能据此宣称请求计量已经上线。

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
- `attempt.observe(dataJSON)` 只接受协议转发器已经验证和接受的完整 JSON 或单个 SSE
  `data:` JSON，交给 `accounting.UsageAccumulator`。协调层只保留四个 nullable 计数，
  不保留或输出正文、工具参数、提示词、响应或原始错误。
- `attempt.finish(ctx, status, finishedAt)` 固定先结束 attempt，再结束 request。
  `finishWithoutAttempt` 只结束无 attempt 的 request，且拒绝成功状态。

协调器没有 HTTP 接口、后台队列、价格目录、预算或计费入口，也不执行上游请求。
一次请求对象最多创建一个上游 attempt，调度固定为 `primary`，不会重试或创建
`retry`/`failover` attempt。价格尚未配置，因此 `BeginAttempt.Price` 固定为 `nil`；
即使用量已知，成本仍为 SQL `NULL`，不能伪造零价格或精确账单。

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

若 attempt 已经写入但 request 写入失败，再次调用相同 finish 会先幂等重放 attempt，
然后重试 request。数据库错误不会被当作成功吞掉。传入 context 已关闭时，协调器用
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
