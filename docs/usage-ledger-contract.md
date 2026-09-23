# 内部用量与尝试账本核心契约

状态：源码开发预览，2026-09-23。`internal/accounting` 已通过[服务协调器](usage-service-contract.md)接入四协议模型请求及启动恢复；HTTP 用量、事务回滚和失败恢复专项通过。管理统计 API、网页和成本价格版本已按[用量管理契约](usage-management-contract.md)接入；预算与账单流程尚未实现，不能据此宣称正式计费。

## 范围与接口

- `NewLedger(*sql.DB)` 接受服务拥有的共享数据库；模块不自行打开数据库、改变连接池或接管服务迁移顺序。
- `Migrate(ctx)` 在一个数据库事务中创建 `accounting_requests`、`accounting_attempts` 及索引，并验证同名对象确为表、列集完全一致、关键 CHECK 约束存在且 attempt→request 外键正确。已有 view、缺约束或外键、缺列以及包含正文等额外列的伪兼容 schema 均拒绝；失败事务完整回滚并可在修复后重试。调用者负责在服务启动迁移中调用它。
- `BeginRequest` / `FinishRequest` 记录一次员工请求；`BeginAttempt` / `FinishAttempt` 记录该请求下每次真实上游执行。ID 全部由调用者稳定提供，模块不从时间或内容生成 ID。
- `FinishAttemptTx` / `FinishRequestTx` 复用相同终结校验，接受调用者事务且不自行提交或回滚；服务用它们与旧请求状态一起原子提交。
- `RecoverInterrupted` 由启动恢复流程显式调用，将仍为 `pending` 的请求和尝试一次性转为 `interrupted`。迁移本身不隐式改变运行记录。
- `SummarizeRequests` 只统计客户端请求状态和数量；`SummarizeAttempts` 另行统计上游尝试、已知用量和实际尝试成本。请求汇总不得把多次尝试的 Token 伪装成一次成功请求的 Token。

## 元数据与状态

请求只保存 `request ID`、员工 ID、Key ID、模型 ID、provider、开始/结束时间和状态。尝试只保存 `attempt ID`、父请求 ID、account ID、provider、调度原因、价格快照、nullable 用量、nullable 成本、开始/结束时间和状态。

provider 是固定枚举：`openai`、`openai-compatible`、`anthropic`、`gemini`、`codex`。调度原因是固定枚举：`primary`、`retry`、`failover`。状态是 `pending`、`succeeded`、`failed`、`cancelled`、`interrupted`。所有时间在写入前规范为 UTC RFC3339Nano。

表中没有请求正文、提示词、响应、Authorization、凭据、任意错误正文、任意日志字段或可扩展 JSON。请求、员工、Key、attempt、account ID 和价格版本只接受最多 256 bytes 的 ASCII 标识符字符。模型 ID 与服务公开模型约束一致：必须是 1 至 128 bytes 的有效 UTF-8，可以包含 `/` 和中文等标识文本，但拒绝所有 Unicode 空白与控制字符。错误只通过固定 Go sentinel 分类，不把传入值拼入错误消息。

## 幂等与父子一致性

- Begin 重放若 ID 及全部不可变输入相同则成功；同 ID 的任意不可变输入不同返回 `ErrConflict`。
- Finish 只允许 `pending` 转为一个终态。相同终态、完成时间、用量的重放成功；不同重放返回 `ErrConflict`，不会再次计费。
- 尝试必须引用已存在且仍为 `pending` 的请求，且 attempt provider 必须与父请求 provider 完全相同；不一致时整个事务回滚，不写 attempt。请求终态后拒绝新的 attempt ID；已有 attempt 的完全相同 Begin 重放仍保持幂等，包括父请求已经进入终态后的历史重放。
- 请求结束前不得有 `pending` 尝试，且请求结束时间不得早于任一终态尝试的结束时间。请求标为 `succeeded` 时，同一事务内必须至少存在一个 `succeeded` 尝试；失败、取消或中断请求可以没有上游尝试。
- BeginAttempt、FinishRequest、FinishAttempt 和恢复均通过写事务串行化相关状态转换。竞争只有一个转换生效，不产生终态请求下的新尝试，也不重复写成本。

## 用量、价格与缓存约定

用量字段为 nullable `int64`：普通输入、输出、缓存读取、缓存写入。缺失表示未知；明确的零必须由协议适配器传入零指针值。负数被拒绝。成功尝试也允许用量未知，因为上游成功不保证返回 usage。

价格是 `BeginAttempt` 时的可选快照。已配置时必须同时包含价格版本、三字母大写币种，以及四类每百万 Token 的非负整数费率；Finish 不接收价格，因此目录或配置的后续变更不能重定价已经开始的尝试。未配置时版本、币种和四类费率全部保存为 SQL NULL，调用方不需要伪造零费率或虚构币种。费率与成本均使用整数 microcurrency，不使用浮点数。

进入账本前，协议适配器必须把 Token 归一化为四个互斥计费桶：`input_tokens` 仅表示不属于缓存读取或缓存写入的普通输入；cache read 和 cache write 各自只出现一次。若供应商的 input 字段包含缓存 Token，适配器负责先扣除相应子集并验证不为负，账本不会猜测包含关系。

只有价格快照存在且四类用量全部已知时才计算成本：先对四个 `tokens × per-million rate` 的整数乘积求和，再除以 1,000,000 并向上取整为 microcurrency。实现使用检查过的 128 位乘加；中间和最终结果超出可表示范围时拒绝 Finish，尝试保持 `pending`。价格缺失或任一用量未知时 `cost_micro` 保持 NULL。

尝试汇总按币种分组；没有价格的尝试进入独立 `UNKNOWN` 桶。每组分别返回 `known_cost_micro` 总和和 `unknown_cost_attempts`。已知成本为零与未知成本通过后一个计数区分；各 Token 字段也分别返回已知总和与未知终态尝试数。pending 尝试单独计数，不提前列为未知终态成本。

## 恢复与限制

恢复只把未完成行标为 `interrupted` 并写恢复时间；不会填 Token、成本或成功状态。若调用方给出的恢复时间早于任一 pending 请求或尝试的开始时间，或早于 pending 请求下已结束尝试的结束时间，整次恢复返回 `ErrInvalid` 并回滚，不写出倒序时间。恢复与正常 Finish 竞争时，先提交者决定终态，后到的不同 Finish 返回冲突。重复恢复返回零变更。

本核心自身不依赖 HTTP，服务接线见服务协调契约。内存队列、本地持久日志、磁盘故障就绪降级、保留期清理、日汇总、配额和预算执行仍待实现；当前是内部明细子集，不是完整的 billing-grade 交付。

实现依据本仓 `core-design.md` 与 `acceptance-matrix.md` 的独立规格；测试只使用临时 SQLite 和合成元数据，不读取真实凭据或请求内容，也没有新增第三方依赖。
