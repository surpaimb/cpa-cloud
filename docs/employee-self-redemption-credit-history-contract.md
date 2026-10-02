# ID-05 / BILL-04 员工本人自助兑换入账已验证历史契约

状态：2026-10-03，**仅设计合同，尚无本功能实现、测试或 CI 证据**。精确基线为已合并的 `main` `11d285693b03e10ad983fb911b86102271644142`。本批只增加一个默认关闭、员工显式读取的“本人自助兑换码本地入账”窄片，不是通用钱包来源、余额、账单、外部付款证明或兑换码查询。依据本项目的[产品边界](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览接口契约](preview-contract.md)、[单实例财务契约](single-instance-billing-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[员工自助兑换写入](employee-self-redemption-contract.md)、[钱包余额](employee-self-wallet-balance-contract.md)、[最近钱包变动](employee-self-wallet-activity-contract.md)、[原始分录分类](employee-self-wallet-entry-classification-contract.md)及[财务 actor 来源](financial-actor-provenance-contract.md)独立制定。

## 范围、启用与历史可见性

新增 `--employee-self-redemption-credit-history-enabled` / `EmployeeSelfRedemptionCreditHistoryEnabled`，默认 `false`；独立能力位精确为 `features.employee_self_redemption_credit_history`。显式开启本位时，`--employee-self-service-enabled`、`--employee-self-wallet-balance-enabled`、`--employee-self-wallet-activity-enabled` 和 `--employee-self-wallet-entry-classification-enabled` **四项都必须已开启**。普通启动和 `--init` 均须在任何数据库、密钥、目录或其他持久写入前拒绝缺少任一前置的配置；不得悄悄开启前置。本位关闭时不注册新路由，返回 404，能力位为 false，网页不出现入口。仅开启四项基础能力也不会自动开启本位。

本位不以 `--employee-self-redemption-enabled`、当前 `financial_settings.enabled` 或商业执行开关作为启动或读取前置。它们关闭后，**已提交且在本页证明链完整的历史仍可读取**；关闭它们只影响各自新的业务写入，不抹除过去的本地入账事实。新 GET 与现有 `POST /self/api/v1/billing/redemptions` 严格分路由、分 guard：GET 不复用 POST 的码、密码、CSRF、`X-Self-Request` 或失败限流逻辑，也不改变 POST 的授权、精确重放和返回形状。旧余额、活动和分类接口及其 JSON、游标 purpose、能力语义均不变；新游标与两种旧游标互不接受。

唯一纳入的是：**当前员工本人经有效 self session，以 typed `employee` actor 通过现有 self redemption 路径，向该员工直接拥有的同币钱包完成的一笔本地兑换正向入账**。管理员替员工兑换、历史 `legacy_unknown`、系统 actor、Key/resource 子账户、其他员工、没有两侧完整可验证事实的普通 `redemption` 分录，以及其他账本种类都不纳入。即使一条管理员入账与本人账户、时间和金额吻合，也不能推断为“本人自助”。本页不说明兑换码目前是否可用，不证明外部付款、退款、用量收费、订阅权益或完整账单。

## 独立严格 GET 与最小响应

只接受精确 `GET /self/api/v1/billing/redemption-credits?currency=USD[&limit=20][&cursor=...]`。字面路径大小写、转义形式和斜杠必须精确；无尾斜杠、额外段、路径清理/重定向替代。对形似路径须在 `ServeMux` 规范化重定向前拦截，不能以重定向绕过严格路径或泄露查询。仅本位有效且精确路径的非 GET 返回 405 和 `Allow: GET`；关闭时优先 404。现有已开通、active 员工的有效 self session 是唯一身份来源；匿名、管理员 Cookie、员工模型 Bearer Key、其他员工或失效 session 不可替代。沿用 self Cookie/同源边界：可选 `Origin` 若出现必须同源，所有应用响应 `Cache-Control: no-store`。此 GET **不**要求当前密码、CSRF 或 `X-Self-Request`，不得读取或验证兑换码明文。

原始 query 最多 2048 字节；必须且仅能出现一次三位大写 ASCII `currency`（`[A-Z]{3}`）。首页 `limit` 可省略，默认 20；显式值必须为 1–50 的规范十进制（无符号、前导零或空值）。`cursor` 只用于续页，最多 1024 字节的规范 base64url；续页必须显式重复首页的 `limit`，与游标内的值完全一致。重复或未知参数、裸 `?`、空值、非法百分号/编码、大小写变体、过长 query/token、请求正文（含未知长度或 Transfer-Encoding）统一固定 `400 invalid_request`；不接受员工、账户、Key、资源、operation、entry 或兑换码选择器。无币种发现、批量查询和按照码搜索接口。

成功 JSON **恰好六个顶层字段**，每条 item **恰好两个字段**：

```json
{
  "currency": "USD",
  "has_account": true,
  "window_start": "2026-09-02T12:00:00Z",
  "window_end": "2026-10-03T12:00:00Z",
  "items": [{"credited_at":"2026-10-03T11:59:59Z","amount_micro":"42"}],
  "next_cursor": null
}
```

`amount_micro` 是经链验证的**正** `int64` 微单位，输出为无前导零的规范十进制字符串；`credited_at` 是一致的、规范 UTC `RFC3339Nano` 入账时间。页面标题和提示必须称“自助兑换码本地入账”，不能把它泛称为充值/付款/退款/账单。没有直属该币钱包时返回 `has_account:false`、空 items、`next_cursor:null`；有钱包但窗口内无合格自助入账时返回 `has_account:true` 和同样空 items。两者均不表示余额为零。成功响应不得回显兑换码明文或摘要、管理员身份、任何 account/entry/operation/redemption/receipt ID、当前余额、码使用次数、来源账户、套餐或支付资料。游标内的存储位置仅作为认证加密的不透明密文传输，不作为明文字段公开。

## UTC 窗口、稳定分页与专用游标

首页在收到请求时冻结 `31 × 24h` 的 UTC 窗口：`window_end` 为下一整秒，排他；`window_start = window_end - 31 天`，包含。续页沿用完全相同的两个边界，但每页使用新的只读数据库快照；不宣称跨页是同一快照或并发写入下枚举完整。按不可变 `(financial_entries.created_at TEXT, financial_entries.id TEXT)` 以 SQLite binary stored-key 降序，续页用严格小于上页最后返回位置的谓词；同一存储时间的 ID 不重复/跳过。混合精度 `RFC3339Nano` 的字典序在同秒内未必等于纳秒时间顺序，故产品只承诺稳定存储键顺序。整秒窗口边界须沿用旧活动接口的索引比较语义，不能漏掉带小数的边界秒。

每页最多返回 `limit` 条，并读取至多 `limit+1` 条**合格候选**决定 `next_cursor`；返回条目与 lookahead 条目均须完成下述同等验证。不得在校验失败后跳过坏候选、补齐页面或发出部分列表。后续新增的旧位置之前/之后入账可能按 stored-key 位置出现或缺席，响应不能冒充审计导出或固定快照。

新游标使用现有安装秘密、每次新随机 nonce 的 AEAD，但必须有独立 purpose/version，例如 `cpacloud/self-redemption-credit-history-cursor/v1`。认证内容只包括当前员工 ID、当前 self-session selector、币种、limit、冻结窗口和最后一个已返回的原始存储 time/ID 位置；不含码、金额或当前余额。任何账本读取前验证规范编码、AEAD、purpose/version、安装、员工/session、币种/limit、31 天窗口、位置范围和原始 `window_end` 后不超过 15 分钟的时限。截断、篡改、跨安装/旧接口/员工/session/币种/页大小、过期或非法位置均为相同 `400 invalid_request`；解密失败不得把密文或原因写入日志。游标生成/随机数失败为 503，且不能先发成功响应。游标不持久化到数据库或浏览器存储。

## 单事务证明链，不做通用来源推断

使用调用者持有、由请求 context 派生最长五秒的**单个只读 SQLite 事务**。先核现有相关账户、账本操作/分录、商业操作、兑换码和兑换关联表的精确可接受 schema、索引、声明 FK 和不可变 trigger；不能只按表名存在即信任，不引入 DDL、修复或写入。不要求扫描全部历史。按 session 员工、`owner_kind='employee'` 和显式币种定位最多两条 `financial_accounts`；唯一匹配行还须核规范 owner key、同一员工 ID、`key_id IS NULL`、空 resource 字段、币种与类型。重复/畸形账户失败关闭；Key/resource 或别人的账户绝不聚合。

只从该直属账户的窗口和位置中选取 `kind='redemption'` 分录，且关联账本 operation 的 typed `employee` actor ID **等于当前 self session 员工**。这是候选过滤而非证明完成；不能在查询谓词中先排除负金额、错 action 或错摘要版本，让这些已选中范围内的坏链静默消失。管理员和 `legacy_unknown` 不进入候选集。选中及 lookahead 的每条候选都必须在同一事务重构以下**双侧一致**链，任一失配即整页 503：

1. 分录的存储类型、ID、规范 UTC 时间、正金额、`kind='redemption'`、无 `original_entry_id`、直属账户/币种、`resource_kind='redemption'` 与 resource ID 有效；同一 `operation_id` 必须**恰好一条**分录。唯一账本 operation 的 action、resource、时间、typed employee actor/FK、`digest_version=2` 和 32 字节 payload digest 必须与分录及当前员工一致。
2. `financial_redemptions` 必须恰有一条以该分录为 `entry_id` 的不可变关联行；其 ID、account ID、code ID、`created_at` 与分录的账户、resource ID、时间一致。不得把同 operation 的另一个兑换、同账户的另一笔分录或仅金额/时间相同的行拼接为证明。
3. 同 `operation_id` 的唯一 `financial_commercial_operations` receipt 必须是 `redemption.redeem`、revision 1、`resource_kind='redemption'`/同一 redemption ID、同一规范入账时间和相同 typed employee actor/FK。管理员、system 或 `legacy_unknown` receipt 即使其账本侧相似也不能通过。缺一侧、错 actor、错 action、错 resource、错时间或单侧孤儿均失败关闭，不自动补写。
4. 关联 `financial_redemption_codes` 只交叉核对 32 字节 code digest、三位大写币种、正金额、`0 <= uses <= max_uses` 且 `max_uses >= 1`，以及**同码全部** `financial_redemptions` 的 `COUNT(*) = uses`。分录/账户/码三者币种与金额必须相同。用码行摘要而非明文码，按现有 `employee-redemption/v1` 域及字段重构商业 payload digest；按现有 `financial-post/v2` actor-aware 有序单条分录载荷重构账本 payload digest，并与两侧存储的 32 字节值逐一比较。两种摘要互不替代，且完整 typed actor、owner、operation、action、resource、币种、金额、时间分别一致。

当前码 `enabled`、`expires_at`、`uses < max_uses` 和当前商业执行开关**不是历史读取门禁**；码停用、过期、用尽或商业执行关闭后，已提交且完整的本人历史仍显示。只核上述必要码不变量，不通过现存管理员代兑数量或当前可兑状态推断员工自助来源。商业摘要及账本摘要由现有本仓库语义重构，不导入第三方 SDK，也不读取或反推明文码。现有 `financial_redemptions` 的不变更/不删除约束及 operation/entry 关系须在验证中明确覆盖。

schema/FK/trigger 漂移、所选或 lookahead 链缺失/畸形、类型/时间/摘要/计数不一致、SQL/scan/Rows 迭代/Close、context 取消/超时、只读事务 commit 错误均为固定 `503 storage_unavailable`，整页零部分数据。游标编码也在成功头/正文前完成。成功前在 `admission.Lock` 下最终复核**同一** self session、expiry、enrollment/active 身份，并在该锁下输出成功，从而与 logout、改密及管理员 disable 线性化；最终复核失败不得输出旧页。认证、Origin、路径、参数和方法错误分别沿用现有固定脱敏 self 错误面；应用响应均 no-store，不透传 SQL、actor ID、码摘要或其他内部材料。不得记录 cookie、cursor、员工 ID、金额、凭据、auth header、提示词或模型响应。

## 网页、验收与发布边界

`/self/` 只在**本能力位**为 true 时显示独立“自助兑换入账历史”入口；员工输入一个币种并明确点击才读取。mount、登录、打开活动/分类或更改币种均不得自动 fan-out 到新路由，也不得枚举币种。页面在组件内存中保留结果和游标；失败、币种变化、账户/session 切换、退出、卸载均清空旧结果和续页，并用 abort、请求 generation/身份比较丢弃晚响应。缺账户与有账户但没有合格记录须区别显示；金额只显示正数和本地入账标签，不能暗示余额或已付款。桌面与真实 Chrome 390px 应无水平溢出。除了必需的 API query，身份、结果与游标不得进入浏览器 URL、localStorage、sessionStorage、IndexedDB 或 service-worker 缓存。

合同审阅通过后才可实施。独立自动化验收至少覆盖：默认关、四前置的每一种缺失在正常启动和 `--init` 持久写前拒绝；GET 与 POST 路由/guard 独立，POST 及旧余额/活动/分类 JSON 和游标不变；匿名/admin/Bearer、Origin、失效/禁用/登出/改密及最终输出竞态；严格路径/query/body/method、跨目的游标、15 分钟、同 stored time/mixed precision、页边界与并发写；缺账户/空历史/正值；本人自助与管理员代兑、`legacy_unknown`、Key/resource、异员工、其他 entry kind 的隔离；商业执行/自助兑换写开关后来关闭及码过期/停用/用尽仍读旧事实；receipt/ledger v2 双摘要、actor、FK、schema/trigger、code `COUNT=uses`、唯一 operation/entry、选中/lookahead 损坏及 scan/Rows/Close/commit/context 故障的整页 503；无财务写入、无明文码/内部 ID/余额泄露；UI 显式点击/晚响应清理及桌面/390px。实施阶段再做定向 Go、vet、Web、动态非 8787 隔离进程和真 Chrome；任何新代码提交的完整 CI、固定二进制和独立验收须按**该精确新 HEAD**重钉，不能借前批证据。

本阶段**仅本文件及独立本地 commit**；不 push、不建 PR、不触发 CI、不写实现。后续也无本批 DDL、资金写、worker、provider、payment、权益、tag、native package dispatch 或部署。旧管理员/`legacy_unknown` 历史、通用钱包来源、全量账单、外部支付/退款/用量费用证明均留后续独立范围。`GOV-02` 告知、角色、保留和恢复决策仍开放，此 flag 不得生产默认启用；单实例预览不暗示多实例一致性。

## 来源与许可

本合同依据上述 CPA Cloud 自有规格和本仓库已合入结构的只读核对独立撰写；此前看过参考代码，不宣称严格 clean-room，且未复制、翻译、逐行改写或移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考仓库的代码、测试、迁移、资产和文档。2026-10-03 核对的公开技术资料：[Go `database/sql` 的事务、迭代与关闭](https://pkg.go.dev/database/sql)、[Go `cipher.AEAD`](https://pkg.go.dev/crypto/cipher#AEAD)、[SQLite WAL 隔离](https://www.sqlite.org/isolation.html)、[SQLite 外键](https://www.sqlite.org/foreignkeys.html)、[SQLite 触发器](https://www.sqlite.org/lang_createtrigger.html)、[SQLite `SELECT` 排序](https://www.sqlite.org/lang_select.html)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。身份、证明链、字段和失败政策是本项目自身约定，不是这些资料提供的兑换协议。本阶段未引入依赖、SDK 或素材；后续实现仍须记录新增源码 provenance，既有 Go、SQLite 驱动、React 等依赖保留各自许可证，见[依赖许可记录](research/dependency-notices.md)。
