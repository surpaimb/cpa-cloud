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
写入未来的预算 reservation；已确定回滚时，attempt 与 sibling 都不可见。这个接口不承诺所有 Commit 错误都代表
没有提交：提交结果不确定时，调用者必须以原 attempt ID 查询或幂等核对，不能改用新 ID 重试，也不能提前释放预算。
同事务保证两个写入共同持久化或共同回滚，但提交结果是否已确认由调用者的生命周期协调器处理。

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

## 已证明互斥输入组的组合算术

```go
type MutuallyExclusiveInputUpperUsage struct {
    InputMax  int64
    OutputMax int64
}

func CalculateMutuallyExclusiveInputUpperBound(
    usage MutuallyExclusiveInputUpperUsage,
    price PriceSnapshot,
) (tokenUpper int64, costUpper int64, err error)
```

该接口仅适用于调用者已经证明 ordinary input、cache read 和 cache write 是互斥计量分类的 profile。`InputMax` 是三种
输入分类中实际出现那一类的共同总上界，而不是每类各自可同时达到的上界；`OutputMax` 独立。函数使用 checked-add 得到
`InputMax + OutputMax`，成本使用原始不可变 `PriceSnapshot` 计算：

`ceil((InputMax*max(input_rate,cache_read_rate,cache_write_rate) + OutputMax*output_rate) / 1_000_000)`。

函数验证并直接读取传入价格的 version、currency 和全部费率，不改写价格身份，也不构造一份以最大费率替换原费率的
伪快照。它与四桶 `CalculateUpperCost` 共享 checked 128 位乘加和向上取整实现，但不改变四桶接口的独立相加语义。
负数、Token 总和溢出、成本溢出和非法价格均返回 `ErrInvalid`。

类型名与函数名故意保留互斥条件。函数不会验证 provider 的 usage 语义，不会生成 bound proof，也不会决定某个真实
provider、协议、模型或转换版本能否使用该公式。生产 profile、reservation、HTTP 和预算开关仍未实现。

## 已验证边界

专项测试覆盖 caller sibling 回滚、提交前隔离、成功提交、终结后精确重放与冲突、nil/无效输入、context 取消导致的
确定性 Commit 失败及回滚后重试，以及以 `math/big` 独立 oracle 验证四桶、向上取整、零费率、`MaxInt64` 边界和溢出拒绝。
本测试没有模拟“数据库已提交、调用者却收到错误”的结果不确定情形，未来预留运行时必须补该故障验收。该验证只证明上述
事务与算术基础，不证明普通模型请求已有可信 Token 上界或预算执行。

2026-09-23 根任务在 `codex/budget-integration` 独立复核：accounting 全包非缓存测试 PASS（15.844s）；
service 的 Accounting/Usage/Ledger、治理 HTTP 四协议和 Codex 交叉测试 PASS（67.637s）；两包 vet、实际 Go 编译及
`scripts/smoke-governance-observations.mjs` 真进程模拟上游验收 PASS。代码为 `bfeddcf` 的独立 cherry-pick。
验证包含派发时冻结价格、事务故障、取消、未知用量、重启和员工权限；所有数据为合成测试数据，不调用真实供应商。
本批尚未推送 Linux CI，不借用观测主线 `60ea6f6` 的 CI 结果，也没有发布安装包。

互斥输入组专项另以 `math/big` oracle 覆盖三种输入费率分别为最大值、向上取整、Token checked-add、`MaxInt64`、
成本溢出、非法价格和原始快照不变；同时回归四桶 helper 仍分别累计三类输入桶。该算术验收不等于生产 bound profile。
