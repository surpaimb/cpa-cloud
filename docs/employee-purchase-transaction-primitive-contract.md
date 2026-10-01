# P0-B 员工购买事务原语契约

状态：2026-10-02 独立设计合同；本文件本身不宣称实现或验收完成。精确基线是 `main` 合并提交 `4a07576957a90aa166f39c5cc8c001aef7383aa8`。本批只增加 Go 服务内部、由调用方持有事务的员工购买原语，并保证现有管理员购买及其他商业路径无回归。**本批没有员工自助购买 HTTP 路由、网页按钮、报价 API、真实支付或部署；不能称员工已经可以购买。**

本合同扩展[单实例账务契约](single-instance-billing-contract.md)、[财务 actor provenance 契约](financial-actor-provenance-contract.md)和[员工自助套餐只读目录契约](employee-self-plan-catalog-contract.md)。当前目录给出的价格仅是可变读快照，不是授权或持久报价；下一批才验证 5 分钟 AEAD 报价、密码确认、有效自助会话及 active 员工，并在最终同一事务内重验。不能把本批原语直接暴露给未经该边界认证的 HTTP 输入。

## 调用边界与建议内部接口

调用方从同一服务数据库以有界、可取消的上下文取得 `*sql.Tx`，负责 `BeginTx`、`Commit`、`Rollback` 与最终成功响应。原语只在传入的事务上查询和写入，**绝不自行开启或提交事务，也不调用另一个数据库连接完成财务步骤**。它返回的成功只是“事务内已准备好”的临时结果；提交失败或结果不确定时，调用方不得对外声称购买成功，只能返回不可用并允许按同一 operation ID 查询/重试。提交成功后若请求已取消，也不能伪造已送达的成功响应；持久事实仍须由同 ID 重试识别。调用方不得在原语返回错误后提交该事务。

建议公开于 `internal/financial` 的内部 Go 形状为：

```go
type ExpectedPurchasePlan struct {
    PlanID, Currency, Interval string
    PriceMicro, CreditMicro, Revision int64
}
type EmployeePurchaseInput struct {
    OperationID string
    Actor Actor
    Owner Owner
    Expected ExpectedPurchasePlan
    ObservedAt time.Time
}
type PurchaseLedgerReceipt struct {
    OperationID string
    Charge Entry
    Credit Entry
}
type EmployeePurchaseResult struct {
    Subscription Subscription
    CommercialReceipt CommercialReceipt
    LedgerReceipt PurchaseLedgerReceipt
    Replay bool
}
func (c *Commercial) PurchaseEmployeeSubscriptionTx(
    ctx context.Context, tx *sql.Tx, input EmployeePurchaseInput,
) (EmployeePurchaseResult, error)
```

`Actor` 必须显式为 `(employee, employeeID)`，`Owner` 必须精确为 `(OwnerEmployee, 同一 employeeID, 空 Key/resource 字段)`；ID 来自未来调用方已验证的自助会话，不得从请求体、员工模型 Key 或钱包行推断。原语再次检查相等关系与员工 FK，但本批没有 HTTP 会话、active 状态、Origin/CSRF 或密码授权入口；这些不是 `Actor` 字段本身能证明的事。管理员 `PurchaseSubscription` 保持原有独立入口、请求摘要、自动开户、月套餐与内部提交行为；不以员工限制改变旧管理员操作。员工原语不得借管理员 `ActorAdminID` 别名或 `legacy_unknown` 兼容分支。错误继续用既有 `ErrInvalid`、`ErrConflict`、`ErrInsufficient`、`ErrUnavailable`、`ErrSchema` 的固定脱敏类别，不返回原 SQL、凭据或内部摘要。

## 精确输入与重放身份

输入只接受有效且有界的 operation ID、规范 UTC 观察时间、有效员工 ID，以及可信预期套餐快照的**全部**字段：`plan_id`、正安全整数 `revision`、三位大写 ASCII `currency`、正 `int64` `price_micro` 与 `credit_micro`、精确 `interval="one_time"`。不接受浮点、负数/零、溢出、隐式币种转换、空快照或 monthly。观察时间不参与幂等身份；它只决定首次提交的不可变时间。快照由将来的受信报价/会话边界提供；本批测试直接构造可信内部输入，不把当前只读目录响应当作报价凭证。

原语用有域分隔、稳定字段顺序的规范编码自行计算员工购买指纹/32 字节摘要，至少绑定 `employee-purchase/v1`、operation ID、action `subscription.create`、完整 typed actor、完整直接 owner、上述全部预期快照；调用方不能另传未核实摘要覆盖它。商业 `financial_commercial_operations.payload_digest` 保存这个摘要；账本 `financial_operations` 仍使用现行 actor-aware v2 摘要，绑定同 operation ID、actor、实际新订阅 ID、扣价/入账和 owner。两个摘要职责不同，不能相互替代或改写旧 v1 摘要。全局 operation ID 与已存在的任何管理员、系统、其他员工或其他 action 相撞均固定冲突；同 ID 换 owner、plan、快照或摘要也固定冲突。

事务内顺序是：先验证输入结构/actor-owner 关系及已存在事实的结构，再按全局 operation ID 查商业 receipt。若存在，必须同时精确匹配 action、typed actor 与员工指纹，加载**原**订阅和账本操作、两条分录，核对它们的 owner/账户、计划冻结值、v2 actor、operation ID、资源 ID、金额和唯一性；不完整、孤儿或互相矛盾的已提交事实失败关闭，绝不重造。完全一致才返回原订阅及商业和账本两份不可变 receipt，`Replay=true`。此已提交重放发生在仅约束**新**购买的商业执行开关、套餐当前启用/revision/价格与当前余额检查之前；因此开关后关、套餐随后改价/停用、余额随后减少，不妨碍同一 operation ID 的只读重放。它不补写分录、账户、订阅或 actor，且不得把历史 `legacy_unknown` 当员工事实。旧管理员幂等链、手动/一次性续订和支付回调各走原入口，不被新指纹替换。

仅当没有已提交的该 ID 时，原语在同一事务内验证 `financial_settings.enabled=1`，读取**当前** `financial_plans` 行并要求存在、启用、`one_time`，且 ID、revision、currency、price、credit、interval 与可信预期逐字段严格相同。任一变化都冲突，不用旧报价替代当前配置。执行开关默认关闭；关闭时不得产生新账户、receipt、订阅或分录。只允许员工直接拥有、同币种、**已经存在**的 `financial_accounts` 钱包账户：按规范 owner key 精确定位并核对 `owner_kind`、`employee_id`、NULL Key、空 resource 字段与币种。无账户返回固定业务拒绝，不调用 `EnsureAccountTx`/自动开户；若复用的 `PostTx` 内部有开户分支，必须由同一事务内已验证的不可变既有账户保证它不触发，并测试购买前后账户主键集合不变。Key/resource 子账户、他人账户或混币种账户绝不能代付。

从不可变分录以 checked `int64` 加法求可用余额，要求 `balance >= price_micro`；`-price_micro` 与 `balance - price + credit` 的计算均须防溢出。余额检查与 `Ledger.PostTx(RequireNonNegative=true)` 的最终非负验证都保留，不能只靠一次预检。一次新购买在同一事务内写：一个 `one_time`、revision 1 的订阅（冻结可信且已与当前计划匹配的全部快照）；同 operation ID、`ActorEmployee` 的 v2 账本操作和精确两条同账户 `subscription_charge=-price`、`subscription_credit=+credit`；同 operation ID、同 typed actor 的 `subscription.create` 商业 receipt。新 `one_time` 不产生月度终止时间或续订预约，也不推断模型权益。返回的账本 receipt 指向实际两条不可变分录；商业 receipt 与账本操作、订阅必须互相指向同一资源。零条、多条、错账户、错 actor 或缺任一侧都不是成功。

## 不同 ID、并发与事务失败

幂等保证只针对**已经提交**的相同 operation ID 和完全相同输入指纹。尚未提交的第一次调用对别的连接不可见；同 ID 并发可以等待 SQLite 单写者或得到 `SQLITE_BUSY`/序列化失败，失败方只返回不可用并回滚，随后以同 ID 重试查询最终提交结果。第一次若回滚，同 ID 可作为新的首次尝试；不得把未提交的临时订阅 ID 承诺给调用方。一个成功提交只会有一套商业/账本操作、一个订阅和一对分录。

**不同** operation ID 即使员工、plan 与预期完全相同，当前 schema 没有“每员工每 one-time 套餐只买一次”的唯一约束；它们是两次独立购买。第二次仍须以执行时的开关、当前套餐和钱包余额重新验收。单实例 SQLite 写事务与同事务余额/非负检查确保不能用相同旧余额使两个并发扣款都提交；失去写锁升级的读快照必须回滚并重试全事务，绝不在旧快照上部分继续。若第一笔入账使余额仍足以支付，第二个不同 ID **可能**合法成功；本批不暗示跨 ID、跨设备或跨实例的无限防重复，也不引入限购策略。跨实例一致性仍在项目范围外。

任何输入、schema、FK、金额溢出、业务门禁、SQL/触发器、上下文取消或持久化错误须使调用方回滚整个事务；不可留下单侧 receipt、仅扣款、仅入账、仅订阅或新账户。若提交返回错误/结果不确定，外层只报告不可用，不做自动二次扣款；重启后以同 ID 重放决议。若提交已成功但客户端取消或响应中断，不声称客户端收到成功；持久记录仍保留，下一次精确重试返回原双 receipt。任何取消检查不得在已提交后试图“补偿”删除不可变事实。

本批不增 DDL、依赖或数据迁移：使用 PR #44 已有 L2/C4 typed actor 结构和 v2 账本，启动时沿用精确 schema/行/FK 校验以及 L1/C1–C3 到 L2/C4 的受控旧代迁移。不能在购买事务中静默修补或升级 schema。旧库启动、迁移中故障回滚、重启复验后再调用原语；旧 v1 操作/余额/订阅保持逐字不变，新员工操作只写 v2。未知或畸形 schema、已有 receipt 链损坏、缺触发器或 FK 错误失败关闭，而非绕过旧代校验。生产中若现有 `PostTx` 自动开户或其它不变量使“既有账户限定 + 同事务原子性”无法保证，应停止并报告具体阻断，不放宽本合同。

## 独立验收与明确未做

先提交本合同并做相对链接与 `git diff --check`；主任务只读核基线、范围与接口后再实施。实现测试至少覆盖：管理员购买/续购/支付回调与历史 v1 重放不变；员工 typed actor/直属 owner/既有账户，Key/resource/他人/缺账户拒绝且零新账户；商业默认关及开关后关的精确重放；`one_time` 与 monthly、停用/删除/改价/币种/credit/interval/revision 套餐；相同 ID 原双 receipt 重启重放与所有输入字段/actor/摘要冲突；余额不足、负值/零/最大整数与总和溢出；单实例同 ID/不同 ID 并发、未提交回滚重试；故障注入、取消、提交失败/不确定以及提交后取消；旧 L1+C1/C2/C3 迁移/回滚/重启与坏 L2/C4 fail-closed。测试直接核对行数、主键、摘要版本、actor、账户主键集合及两条分录，不只看返回错误。Go 专项、vet、适用 Web 回归、动态非 8787 隔离进程，以及精确 HEAD 的 CI/独立黑盒证据分别报告，不借用 PR #44 的通过记录。

下一批才实现员工自助写入口、同一最终事务内会话/active/CSRF 重验、5 分钟 AEAD 报价与密码确认和网页流程。本批没有员工权限授予或模型请求放行、管理员定价 UI 改动、真实支付/provider、跨实例余额协调、限购/退款/税票、部署、tag、package 或 native 安装包。即使 Go 原语通过，员工仍不可通过 HTTP 自购。

## 来源与许可

本合同及后续代码/测试依据本仓库上述功能规格独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或旁路参考仓库代码；此前看过参考代码，因此不称严格 clean-room。公开技术资料核对日期 2026-10-02：[SQLite 事务与单写者/忙错误](https://www.sqlite.org/lang_transaction.html)、[SQLite WAL 快照隔离](https://www.sqlite.org/isolation.html)、[Go `database/sql` 的 `BeginTx`/`Commit` 与取消语义](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go `context`](https://pkg.go.dev/context)。现有 Go、modernc SQLite、Web 与测试依赖保留各自许可证；本批不引入新第三方依赖、SDK 或素材。后续新增源码须注明本合同与实际使用的公开文档 provenance。
