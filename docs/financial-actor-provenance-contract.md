# 财务操作 actor provenance v1 契约

状态：2026-10-02 独立设计合同；本提交仅为文档，不声称实现、迁移或验收完成。基线为 `main` 的 `f36b9a4d46b35f23de2aab986c9edff65f22b61c`。本段为后续员工本人钱包购买提供可信操作归属，但**不**增加员工购买 HTTP/UI、扣款授权、权益发放、支付回调能力或任何新商业开关。既有管理员商业写入、支付回调和一次性续订 worker 的业务结果必须保持不变。

## 现有事实与适用边界

`financial_operations` 是金额账本的不可变操作表，当前只有 `internal/financial/ledger.go` 的一代精确 DDL：`actor_admin_id` 可空、引用 `admins(id) ON DELETE RESTRICT`。它的 `payload_digest` 是当前 `postDigest` 的 SHA-256/JSON v1，JSON 中只有可选 `actor_admin_id`；`ObservedAt` 不参与摘要。`financial_entries.operation_id` 引用该表；分录自身及 `financial_payments.paid_entry_id`、`financial_redemptions.entry_id`、`financial_refunds.entry_id` 等还构成依赖链。操作 ID、摘要、分录 ID、金额、时间和所有关系都不得因为本迁移而改变。

`financial_commercial_operations` 是另一张不可变商业 receipt 表，`payload_digest` 是业务载荷摘要，actor 当前通过可空的 `actor_admin_id` **单独**参与同 ID 重放比较，不可把该摘要误当成已绑定 actor 的摘要。可接受的旧操作表 DDL 只有下表三代；商业模块的订阅表另有独立旧版迁移，不得把二者混为一代：

| 代 | 源码中的精确 DDL | 行为差异 |
| --- | --- | --- |
| C1 | `commercialOperationsLegacyDDL` | 手动续订之前，无 `subscription.renew` 与一次性 arm/disarm action |
| C2 | `commercialOperationsBeforeOneShotDDL` | 有 `subscription.renew`，无一次性 arm/disarm action |
| C3 | `commercialOperationsDDL` | 含手动续订及一次性 arm/disarm；本基线现行结构 |

旧账本操作表记为 L1，新结构记为 L2；新商业结构记为 C4。空库可以缺两张表；仅有 L1、尚无商业表的旧库也可以存在。启动预检只允许 `(无,无)`、`(L1,无/C1/C2/C3)`、`(L2,无/C4)`，并逐项验证该代对应的 companion 表、索引、触发器及存储数据。商业表在账本表不存在时、L1/C4、L2/C1–C3、额外列/索引/触发器、视图/伪表替代或任何无法精确归类的组合均失败关闭，不得猜测修补。旧 C1/C2 到 C3 的既有合法升级以及订阅旧结构升级，可作为前置、可重启阶段；actor 转换本身必须一次事务同时覆盖已存在的 L1 与 C3，不能提交只有一张表已转换的状态。新库直接创建 L2/C4。不得把此合同当成其他未知历史财务 DDL 的导入许可。

## 不可变 actor 身份

两张新操作表分别增加 `actor_kind TEXT NOT NULL`、`actor_employee_id TEXT REFERENCES employees(id) ON DELETE RESTRICT`、`actor_system_id TEXT`，保留 `actor_admin_id TEXT REFERENCES admins(id) ON DELETE RESTRICT`。在表级 `CHECK` 中仅允许以下互斥形状；非空 ID 还需满足既有 ID 字符/长度规则，不能是空字符串。不存在一个同时可指向管理员或员工表的无约束泛用 `actor_id` 列。

| `actor_kind` | `actor_admin_id` | `actor_employee_id` | `actor_system_id` | 含义 |
| --- | --- | --- | --- | --- |
| `admin` | 存在的管理员 ID | NULL | NULL | 经管理员会话授权的操作 |
| `employee` | NULL | 存在的员工 ID | NULL | 将来经员工本人会话授权的操作；本批无 HTTP 写入口 |
| `system` | NULL | NULL | 固定调用方 ID | 受信服务内部执行器 |
| `legacy_unknown` | NULL | NULL | NULL | 仅迁移旧行，无法逐行证明原始执行者 |

系统 ID 首版只允许 `payment_callback` 和 `subscription_one_shot_worker`，且此限制写入两张表的 SQL `CHECK`，不能只由 Go 校验。账本 `system` 还须满足 `(action='payment_callback' AND actor_system_id='payment_callback') OR (action='subscription_purchase' AND actor_system_id='subscription_one_shot_worker')`；商业表的 `system` 仅允许 `action='subscription.renew' AND actor_system_id='subscription_one_shot_worker'`。启动存储验证重复检查这些对应关系与已有业务链。不得接收客户端提供的系统 actor；扩容此枚举或 action 对应须另有契约和迁移。管理员 ID 必须来自已验证的管理员会话，未来员工 ID 必须来自已验证的员工本人会话，不能从请求体、员工 API Key、所操作钱包的 owner 或历史 `armed_by_admin_id` 推断执行者。尤其一次性续订由 worker 执行，arm 人仅保留在预约记录，不是续订扣款操作的 actor。`legacy_unknown` 绝不用于新插入，也不能被授权为 employee/system/admin：迁移回填完成后，两张表各有一条精确验证的 `BEFORE INSERT` 拒绝触发器，专门禁止再次插入该 kind。两表的 `actor_kind` 与其唯一 ID 列一旦写入，跟操作行一起保持不可变；管理员/员工 FK 删除限制继续生效。

历史 `actor_admin_id IS NOT NULL` 行逐字保留该 ID 并标为 `admin`。历史 NULL 行只在**同一行**满足可验证业务链证据时可标为 `system`：回调账本操作须同时匹配 `payment_callback` action、稳定 webhook operation ID、对应 `financial_webhook_events`、付款/充值、实际 paid 分录、同一事务可重构的旧摘要与资源/金额；一次性续订的商业和账本操作须同时匹配预约 `execution_operation_id`、成功态和 predecessor/successor 续订链、相同操作 ID/资源、预期 payload/旧账本摘要及两条金额分录。仅凭 action、ID 前缀、NULL 或时间接近不构成证明；任一环缺失或不能可靠重构即标 `legacy_unknown`，不伪造 `system`。已有结构/业务不变量被破坏则迁移失败，不得以 unknown 掩盖腐败。迁移记录每类计数供运维审查，但不记录密钥、正文或个人敏感载荷。

## 写入、重放与同事务一致性

新的 `Ledger.Post`/`PostTx` 和商业 `WriteMeta` 必须携带经调用方可信边界确定的规范化 `(kind,id)`。仅新操作可用 admin、employee、system；缺失/冲突/多 ID 或传入 `legacy_unknown` 均拒绝，且不能产生新表行。现有管理员 HTTP 路径应显式传 admin；支付回调的账本操作传 `(system,payment_callback)`；一次性续订 worker 在商业 receipt 与账本扣款两侧都传 `(system,subscription_one_shot_worker)`。同一业务事务内共享操作 ID 的商业与账本记录必须一致，失败/回滚时不得留下单侧 receipt、金额分录或预约终态。启动 stored validation 对两表所有相同 `operation_id` 的行比较完整 `(actor_kind,actor_admin_id,actor_employee_id,actor_system_id)`；新行不相同即失败。历史例外只允许**两侧都**由原 NULL 迁为 `legacy_unknown`、所有 actor ID 都 NULL，且原操作/receipt/业务关联均有效；一侧 unknown、另一侧 admin/system/employee，或两个不同的 system/admin/employee ID，均失败关闭。仅存在于单表的 ID 不伪造另一侧记录。

账本新增行使用有域分隔的 v2 actor-aware 摘要，规范输入包含操作 ID、action、actor kind/ID、资源、非负约束和有序分录；表内显式保存摘要版本 `1|2`。现存 L1 行的 32 字节摘要和 v1 版本逐字保留，**不得**用新格式重算覆盖；历史重放按其版本、原有载荷及存储 actor 身份比较。新 v2 重放同时比较存储的完整 actor、v2 摘要及原有业务条件；相同 operation ID 但 actor kind/ID 不同，即使载荷相同，必须 `ErrConflict`，无新余额或 receipt。商业表的既有 payload digest 不改，`existingCommercialOperation` 在 action/摘要之外比较完整 actor kind/ID；新旧 receipt 都保持原 operation ID、resource、revision 和 `Replay` 语义。

历史 v1 的 `legacy_unknown` 行允许一个**仅重放、不可插入**的兼容分支：它只能由受信内部旧调用路径在已查到既有 `operation_id` 后使用，原先无 actor 的同一调用载荷生成原 v1 摘要时才可返回既有结果；不存在该 ID 即拒绝。不得从 HTTP/员工自助/未来系统新调用公开该入口，也不得让新调用通过省略 actor 伪装旧调用或把 unknown 升格。逐行证明的历史 system 行也保留 v1 摘要并可通过其对应的内部调用方重放；旧回调先查 webhook event 的原有去重路径不变。对需要按旧无 actor 形态重放的历史 system 行，只有对应业务链严格匹配时才可走只读兼容；不同 actor、不同摘要或不同业务目标仍冲突。重放从不补写 actor，也不因新格式改变旧余额/订阅链。

## 迁移与验证

迁移仅在服务开始接客、worker 启动前运行；先用精确 `sqlite_master` DDL/类型、隐式与显式索引和不可变触发器清单识别旧代，运行已有存储、业务链、时间和 `PRAGMA foreign_key_check` 校验，再复制。`CREATE TABLE IF NOT EXISTS` 不能充当验证；未知 schema、坏 FK、重复/孤儿关联、缺触发器、摘要/actor 证据矛盾、读写/持久化错误都必须报 schema/storage 错误并停止启动。新 L2/C4 的严格验证同样检查新增列的 `CHECK`、两类 FK、账本摘要版本、触发器/索引**总数**和存储行形状；账本原 3 个显式索引不变、财务模块触发器总数由 6 增至 7，商业原 6 个显式索引不变、触发器总数由 14 增至 15，额外的均是相应操作表 `legacy_unknown` 插入禁令。管理员审计源须同步精确验证 C4 及其 3 条操作表触发器，不能因其 DDL 已变而静默跳过审计。

因为账本操作被分录引用，分录又被付款/退款/兑换引用，商业操作也被续订和一次性预约引用，不能依赖 `ON DELETE RESTRICT` 的延迟行为直接 `DROP` 父表。采用 SQLite 官方重建表流程的受控变体：在**独占的同一 `sql.Conn`** 上、事务开始前临时关闭 FK，仅限启动迁移；开始独占事务，精确预检，分阶段复制包括原 `rowid`、原 operation ID/摘要/receipt/时间/金额关联，重建两张最终同名表及其原不可变触发器，用明确列清单回填，完整校验新 DDL/行、关系、`PRAGMA foreign_key_check` 后才提交。提交或回滚后，在释放连接前恢复并确认 `PRAGMA foreign_keys=ON`；恢复失败则关闭整个 DB/拒绝启动，不把 FK-off 连接还给服务池。不得在事务内部切换 FK，也不得使用 `defer_foreign_keys` 规避 `RESTRICT`。无接客/worker 并发、busy timeout、上下文取消、进程崩溃和写盘故障均以事务边界处理；任一失败后重启只会看到可识别的旧代或完整 L2/C4，不会有半张新表或临时 staging 对象。新结构重复启动仅验证、不重写历史行。只有 L2 检查摘要版本；C4 的原业务载荷摘要没有新增版本列。

迁移前后对操作、分录、商业 receipt、订阅续订/预约、付款/退款/兑换的主键集合和不可变值做独立对比；不可默默删行、重分配 ID/rowid 或改金额。`rowid` 保留也用于避免现有管理员审计水位在迁移时意外重指；但审计投影语义改变，旧游标仍按下节明确升级处理。测试覆盖空库、L1+无商业表、L1+C1/C2/C3、C3 含真实依赖链和 NULL 混合行、L2/C4 重启、故障注入回滚及恶意 schema 失败关闭。

## 管理员只读审计与呈现

现有第五源 `financial_commercial` 不再把源表列名 `actor_admin_id` 直接等同于所有事件的执行者。JSON 每条事件新增明确的 `actor_kind`：旧四源固定 `admin`；第五源来自 C4 的真实枚举，并按 kind 投影对应 ID，`legacy_unknown` 的 `actor_id:null`。CSV 在 `actor_id` 前增加 `actor_kind` 列，未知 ID 为空；网页同时显示 actor 类型和 ID，未知显示“历史执行者不明”，不能把 employee/system/unknown 标成管理员或显示为当前登录管理员。金额、payload digest、凭据、提示词和响应正文依旧不进入审计、CSV 或日志；`result=succeeded` 仍只表示事务提交，不代表支付最终成功。

`actor_id` 旧筛选语义保持“管理员 ID 精确匹配”，不得在无提示下扩成跨类型 ID 匹配；本段暂不增加 employee/system 的筛选语法。当前审计查询复用 `actorExpr` 做投影与筛选，实现时须拆开：投影按 actor kind 选择对应 ID，而 `actor_id=?` 的 SQL 谓词只比较 `actor_kind='admin' AND actor_admin_id=?`（旧四源照旧）；employee/system 的投影 ID 即使与某管理员 ID 字面相同也不得被筛中。Web 的查询参数和筛选标签继续称管理员 ID，JSON/CSV 则一致显示完整 actor kind/对应 ID。由于旧商业表重建且响应/CSV 字段改变，审计游标升级为 v3、独立 HMAC purpose，旧 v1/v2 游标返回 400 并要求重新取首页；五源排序、窗口、水位和结果边界不变。`financial_commercial` 新 schema、actor 身份列、触发器与异常行必须在首页/续页验证，任一失败整页 503，无部分输出。网页 capability 门控与旧服务兼容照现有审计契约处理，新网页不能把缺 `actor_kind` 的旧响应当成新版本完整事实。

## 独立验收与不在本段

测试至少覆盖 admin/employee/system/legacy_unknown 的 DB 形状和 FK、非法混合/空 ID、不可变触发器、非管理员新写入的拒绝、管理员原路径、回调与 worker 的系统归属、商业/账本同事务一致性、原摘要/receipt/分录逐字保留、旧 v1 精确重放、新 v2 改 actor 冲突、撤销/持久化失败/取消/重启、并发同 ID 与不同 actor、三代旧商业结构以及恶意 schema。审计需覆盖五源投影、旧 v1/v2 游标拒绝、actor_id 管理员筛选、CSV/网页未知标签与故障整页失败。运行全 Go 非缓存测试、`go vet`、相关 race、Web 测试/typecheck/build、隔离进程验收、CI，并分别报告本地与 GitHub 已验证行为；本文件本身只做链接和 `git diff --check` 验证。

不在本段：员工购买 API/按钮、实际 employee debit、员工权益发放、价格报价锁定、真实支付与供应商验收、跨实例账务、新密钥/生产发布或桌面客户端。此合同若遇到无法逐行证明的历史 actor，使用 `legacy_unknown`；若无法保证原子性或 FK/业务链不变量，则停止迁移并上报阻塞，不放宽约束继续写入。

来源与许可：依据本项目[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览接口契约](preview-contract.md)、[单实例账务契约](single-instance-billing-contract.md)、[财务审计来源契约](admin-audit-financial-source-contract.md)与当前仓库独立编写的 Go/SQLite 行为。SQLite 迁移/FK 依据官方 [ALTER TABLE 重建流程](https://www.sqlite.org/lang_altertable.html)、[外键与 `RESTRICT` 语义](https://www.sqlite.org/foreignkeys.html)、[`PRAGMA foreign_key_check` 与 `foreign_keys`](https://www.sqlite.org/pragma.html)。不引入第三方依赖；原有 Go/SQLite 依赖保留各自许可证。新源码在实现提交中须明确写明本合同及所用公开技术文档的 provenance，不引用或移植 CLIProxyAPI、Sub2API、归档 CPA 或旁路参考仓库代码。
