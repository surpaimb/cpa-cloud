# 治理准入快照核心契约

状态：治理核心已实现的独立内部契约，2026-09-23。本文件只描述
`internal/governance` 对准入快照的校验、持久化和幂等语义。服务层的员工/Key/组鉴权、策略查询、
管理 API、四协议接线、账本观测与 shadow 结果计算仍未由该包实现。

本实现依据 CPA Cloud 自有治理规格编写，没有读取或移植参考产品源码、迁移或测试。

## 调用边界

调用方在持有规定的授权读锁和 SQLite 事务时，把已经认证并完整解析的 `AdmissionStart` 交给
`Coordinator.AdmitTx`。核心会校验快照形状、employee/Key scope 与主体 ID 的绑定以及所有 scope 的原子
准入，但不会查询员工、Key、组成员或策略表。因此 `SnapshotComplete=true` 只是调用方关于查询完整性的
显式声明，不是该包自行完成鉴权的证明。

每个 `ScopeSnapshot` 包含稳定的 `(Kind, ID)`、策略 ID 与 `PolicyRevision`。group scope 还必须带独立的
`GroupRevision`，取值为 `1..9007199254740991`；employee 和 key scope 必须不带该字段。组成员关系的
revision 与策略 revision 各自进入请求的不可变快照，二者不能互相替代。

硬限制字段 `RPMLimit`、`ConcurrencyLimit` 和 shadow 字段 `ShadowTPM`、`ShadowCostMicro` 都是 nullable
正 `int64`。每个 scope 至少配置一个硬限制或 shadow 阈值。`ShadowCostMicro` 非空时，
`ShadowCurrency` 必须是三个大写 ASCII 字母，`ShadowWindow` 首批固定为 `rolling_24h`；成本阈值为空时，
币种和窗口也必须为空。未来 HTTP API 应把成本整数编码为十进制字符串，避免 JavaScript Number 精度损失。

## 幂等与计数

首次成功准入把全部 scope 字段原样保存。相同 request ID 只有在主体、公开模型、协议、观测开始时间、
settings revision、policy revision、group revision 及所有硬/shadow 字段完全一致时才是幂等重放；任一值
不同都返回冲突。scope 输入顺序不影响比较，重复的 `(Kind, ID)` 会被拒绝。

shadow-only scope 也会创建治理 request、租约和不可变 scope 行。它从不执行 TPM 或成本拒绝；当前核心
也不会产生 `below`、`exceeded`、`unknown` 或 would-block 结果。该行仍是稳定 scope 的已治理请求事件，
所以后续同一 `(Kind, ID)` 的 RPM 策略能看到它，策略 revision 或 group revision 变化不能清空窗口。

设置开关、持久有效时钟、续租、终结和重启恢复继续遵守主治理契约。关闭开关不删除已创建的 shadow
快照或租约；未知 Token 和成本不会由本包补零。本包不保存提示词、响应、凭据、Key 明文或任意错误正文。

## Schema 与兼容性

`governance_request_scopes` 为每个 scope 保存 nullable `group_revision`、`shadow_tpm`、
`shadow_cost_micro` 以及非空的内部字符串 `shadow_currency`、`shadow_window`。没有成本阈值时，后两个内部
字段使用空字符串；这是 SQLite 规范存储，不改变未来管理 API 的 nullable 表示。

治理核心尚未发布，当前迁移采用严格 schema 校验并直接定义新表形状。旧的 hard-only scope 表会以
`ErrSchema` 失败关闭，不提供就地升级保证；调用方不能把拒绝的旧 schema 当成空策略后继续流量。迁移还会
校验存量行的 group revision、正整数范围、成本币种/窗口组合、主体绑定和外键。所有 schema 或存量校验
失败都在迁移事务中回滚。

## 尚未实现

- 服务层治理组、成员和策略的 CRUD、operation ID 去重与审计；
- 把认证结果和管理表在同一授权锁内解析成 `ScopeSnapshot`；
- 从 accounting attempt 账本按稳定 scope、窗口和币种计算 shadow 三态；
- 四协议 handler 的准入、续租及与 accounting/model request 同事务终结；
- shadow TPM 或成本升级为 hard 限制所需的可信预留与 unknown 策略。

这些功能落地前，持久化 shadow 配置和快照不得被描述为已经执行预算限制或已经提供用量结论。
