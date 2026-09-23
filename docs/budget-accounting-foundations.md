# 预算准入的 Accounting 基础构件

状态：**基础构件已实现，硬预算仍未实现**，2026-09-23。本文件记录
[硬 TPM 与成本预算准入提案](budget-admission-proposal.md)所需的两个 accounting 接口。它们不增加 reservation 表、
运行时准入、Token 上界生成器、管理员 API、网页开关或任何生产 hard TPM/成本预算能力。

实现依据本仓规格与既有 accounting 账本独立完成，没有读取参考产品源码或真实数据。没有新增第三方依赖。

## Caller-owned attempt 事务

```go
func (l *Ledger) BeginAttemptTx(ctx context.Context, tx *sql.Tx, input AttemptStart) error
```

`BeginAttemptTx` 与原 `BeginAttempt` 使用相同的输入校验、精确幂等比较、父 request 存在且 pending 校验、provider 一致性、
时间顺序和不可变价格快照规则。它在调用者提供的 SQLite 事务中写入 attempt，不提交也不回滚。调用者可以在同一事务
写入未来的预算 reservation；任一 sibling 写入或最终 commit 失败时，attempt 与 sibling 一起回滚。

相同 attempt ID 的完整相同重放返回成功，包括该 attempt 和父 request 已终结后的查询重放。相同 ID 的 route、provider、
dispatch、时间或价格快照不同返回 `ErrConflict`。父 request 不存在返回 `ErrNotFound`；父 request 已终结且不是既有 attempt
的精确重放返回 `ErrConflict`。无效输入、nil ledger、nil context 或 nil transaction 返回 `ErrInvalid`。数据库错误和 context
取消保持原错误，不映射成成功或冲突。

原 `BeginAttempt` 仍自行创建事务、调用同一内部写入路径并提交，因此外部行为不变。

## 已证明上界的成本算术

```go
type UpperUsage struct {
    InputTokens      int64
    OutputTokens     int64
    CacheReadTokens  int64
    CacheWriteTokens int64
}

func CalculateUpperCost(usage UpperUsage, price PriceSnapshot) (int64, error)
```

`CalculateUpperCost` 只计算调用者已经证明的四桶 Token 上界。它不会解析请求、调用 tokenizer、读取模型能力或判断这些值
是否覆盖最终 payload。四桶必须显式提供非负整数；零表示调用者证明该桶上界为零，不表示 unknown。价格必须包含合法
version、三位大写币种和价格目录允许范围内的四项非负费率。

计算与 attempt 结算共享同一套 128 位整数乘加与向上取整实现：

`ceil((input*input_rate + output*output_rate + cache_read*cache_read_rate + cache_write*cache_write_rate) / 1_000_000)`。

结果超过 `int64`、中间加法溢出、负数、非法币种、缺少价格身份或费率超出目录上限均返回 `ErrInvalid`。计算不使用
浮点数，不做汇率换算，也不把不完整 usage 当作零。是否允许某个 provider/protocol/model 使用 hard budget，仍由未来固定
版本的 bound profile 和 reservation 运行时决定。

## 已验证边界

专项测试覆盖 caller sibling 回滚、提交前隔离、成功提交、终结后精确重放与冲突、nil/无效输入、context 取消、commit
失败后重试，以及以 `math/big` 独立 oracle 验证四桶、向上取整、零费率、`MaxInt64` 边界和溢出拒绝。该验证只证明上述
事务与算术基础，不证明普通模型请求已有可信 Token 上界或预算执行。

2026-09-23 根任务在 `codex/budget-integration` 独立复核：accounting 全包非缓存测试 PASS（15.844s）；
service 的 Accounting/Usage/Ledger、治理 HTTP 四协议和 Codex 交叉测试 PASS（67.637s）；两包 vet、实际 Go 编译及
`scripts/smoke-governance-observations.mjs` 真进程模拟上游验收 PASS。代码为 `bfeddcf` 的独立 cherry-pick。
验证包含派发时冻结价格、事务故障、取消、未知用量、重启和员工权限；所有数据为合成测试数据，不调用真实供应商。
本批尚未推送 Linux CI，不借用观测主线 `60ea6f6` 的 CI 结果，也没有发布安装包。
