# ID-05 / BILL-04 员工本人直属钱包回调入账原始毛额历史契约

状态（2026-10-04，合同先行）：本文仅规定下一小批默认关闭的只读能力，尚无本能力的实现、测试、构建、GitHub CI、PR 或部署。本地合同基线固定为已合并 `main` 的 `03556265aeb1bbe1a02c24d14c80793e1108f503`（tree `d75ad17e3cec9f1c93737bf2cfa0ae4f753701bb`）。本文依据本项目的[产品计划](product-plan.md)、[独立实现边界](independent-implementation.md)、[开发计划](development-plan.md)、[预览契约](preview-contract.md)、[单实例财务契约](single-instance-billing-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[直属钱包余额](employee-self-wallet-balance-contract.md)、[最近钱包变动](employee-self-wallet-activity-contract.md)、[原始分录分类](employee-self-wallet-entry-classification-contract.md)及[财务 actor 来源](financial-actor-provenance-contract.md)制定。它与[本人自助兑换入账](employee-self-redemption-credit-history-contract.md)和[管理员本地调整](employee-self-admin-adjustment-history-contract.md)分属不同来源，不把它们合并成“充值记录”。

## 事实边界和开关

本能力只让已开通自助且 active 的员工，按本人**直接拥有**的单一币种钱包，查看本服务成功接受一条付款回调后实际提交的本地、原始、正向 `topup` 账本入账毛额。接受的证明必须同时来自 typed `system/payment_callback` 的 v2 账本操作、那一条不可变正向分录、不可变 top-up 身份、当前 payment 的 paid 指针，以及不可变 webhook event；仅见到 `topup` kind、已付款状态、金额相同或回调事件其中之一都不够。本页只陈述“本地回调入账”，不证明付款渠道确已收款，不证明可提现、余额、净入账、退款、发票、订阅权益或账单完整性。此前可能存在的 `topup` action、`legacy_unknown` 或无法完整重构来源的旧行不以猜测纳入；空页不能被解释为员工从未充值。

新增 CLI `--employee-self-topup-credit-history-enabled`、配置 `EmployeeSelfTopupCreditHistoryEnabled` 和独立会话能力位 `features.employee_self_topup_credit_history`，全部默认 `false`。显式开启时必须已开启 `--employee-self-service-enabled`、`--employee-self-wallet-balance-enabled`、`--employee-self-wallet-activity-enabled`、`--employee-self-wallet-entry-classification-enabled` 四项；正常启动和 `--init` 对每一种缺项都须在数据库、密钥、目录或其他持久写入前失败，不能隐式补开。四个前置能力自身不启用本位。本位关闭时不注册或暴露新 GET，任意形似新路由和任意方法在进入 mux、WebDir、认证或数据库前均为 404，带 `Cache-Control: no-store`，无 `Location`、`Allow`，会话能力位为 false，网页无入口。

读旧事实不以当前 `financial_settings.enabled`、付款 connector 的 `enabled`、其秘密是否可用，或任何商业写入开关为前置：这些后来关闭时，完整的已提交原始毛额仍可读。GET 绝不触发付款/回调、重验签名、退款、余额计算、订阅购买或财务修复；不调用管理员服务，也不创建账户、分录、事件、表、索引、迁移、后台任务。既有余额、活动、分类、兑换、管理员调整接口的授权、JSON、游标目的和错误面不变。单实例内部预览以外的多实例协调、外部渠道协议和商业对账不由本位承诺。

## 唯一 GET、授权与输出

唯一新接口为 `GET /self/api/v1/billing/topup-credits?currency=USD[&limit=20][&cursor=...]`。只接受字面精确路径；大小写、尾斜杠、额外段、编码斜杠/反斜杠、双重编码、重复斜杠和 `.`/`..` 清理所得的“等价”路径均不可接受。路由外层在 `ServeMux` 规范化/重定向和 WebDir 前，对原始、escaped、decoded、逐段及**有界**重复解码后的形似路径只做拒绝判断，不改写 `URL`、`PathValue`，不劫持相邻的其他 `/billing/` 路由。关闭时，任何形似路径和任何方法都先返回 404，不经认证或财务读取。开启时，任意方法的畸形/非规范形似路径都先经过 self 读认证和可选 `Origin` 同源检查，随后固定 JSON `400 invalid_request`，不做业务财务读取；只有**规范路径**上的非 GET 在同样认证后返回 `405 method_not_allowed` 与 `Allow: GET`。这些拒绝均无 `Location`；畸形路径的 400 也无 `Allow`，且不回显 query。所有应用响应均 `Cache-Control: no-store`。

身份唯一来自当前有效、未过期的 self Cookie session 及其 active、已开通员工；匿名、管理员 Cookie、员工模型 Bearer Key、别人的员工或失效 session 都不能代替。可选 `Origin` 一旦存在必须同源。本 GET 不要求当前密码、CSRF、`X-Self-Request` 或管理员会话，也不接收员工、account、Key、resource、payment、top-up、event、connector、operation、entry 选择器。认证与同源失败使用既有固定脱敏 self 错误，不读取账本。

原始 query 至多 2048 字节，仅允许恰好一次 `currency`（三位大写 ASCII `[A-Z]{3}`），以及可选 `limit`、`cursor`。首页 limit 缺省 20，显式 limit 为规范十进制 `1` 至 `50`，无符号/前导零/空值；续页必须显式重复首页的 limit 且与游标绑定值相同。cursor 仅在续页出现，为至多 1024 字节的规范无填充 base64url。重复/未知参数、编码别名、空值、裸 `?`、错误百分号编码、超限 query/token、以及任何请求 body（含未知长度或 Transfer-Encoding）统一 `400 invalid_request`；无币种发现、跨币批量和历史全量导出。

成功 JSON **恰好六个顶层字段**，item **恰好两个字段**：

```json
{
  "currency": "USD",
  "has_account": true,
  "window_start": "2026-09-03T12:00:00Z",
  "window_end": "2026-10-04T12:00:00Z",
  "items": [{"credited_at":"2026-10-04T11:59:59Z","amount_micro":"42"}],
  "next_cursor": null
}
```

`amount_micro` 只取已验证不可变原始分录的正 `int64` 微单位，以无前导零的规范十进制字符串输出；`credited_at` 只取同一分录的规范 UTC `RFC3339Nano` 入账时间。无直属该币钱包时 `has_account:false`、空 items、null cursor；有钱包但窗口内无合格回调入账时 `has_account:true`、空 items、null cursor。两者都不是“余额为零”。输出和页面不得提供余额、净额/已退额、退款或当前 payment 状态，不提供任何 employee/account/Key/resource、operation/entry/top-up/payment/event/connector ID、external reference、payload digest、签名、密钥或认证材料。加密游标所需内部位置只能作为不透明密文，不增加公开字段。

## 窗口、候选和独立游标

首页在收到请求时固定 UTC 半开 `31 × 24h` 窗口：`window_end` 是下一整秒且排他，`window_start = window_end - 31 天` 且包含。续页沿用原窗口，每页是单独的只读快照；并发新入账时不声称跨页完整或相同快照。候选按不可变 `(financial_entries.created_at TEXT, financial_entries.id TEXT)` 的 SQLite binary stored-key 降序，续页严格小于上页最后返回的位置。同一 stored time 以 ID 定序；混合精度 `RFC3339Nano` 的字典序在同一秒内不保证纳秒时间顺序，只承诺稳定存储键序。整秒窗口的索引比较须保留带小数的边界秒，不丢失实际落在窗口内的行。

每页最多 `limit` 条；查询至多 `limit+1` 条**合格来源候选**以确定 `next_cursor`。选中和 lookahead 都完整验证下节的同一证明链，不能跳过坏候选、补齐页面或返回部分结果。候选边界是直属账户、窗口/位置和账本操作 `action='payment_callback'` 且 `actor_kind='system'`、`actor_system_id='payment_callback'`；历史 `legacy_unknown`、管理员、员工 actor 和其他 action 不属于该来源。**候选 SQL 不得先按金额正负、entry kind、digest version、resource 或 payment status 排除**这条 typed 来源内的损坏记录，否则整页 503 会被错误地伪装成空页。为防止 inner join 把缺失 operation 的分录静默丢掉，同一只读事务须先在**当前直属账户和冻结的 31 天窗口**内以 `financial_entries(account_id,created_at,id)` 范围索引检查 `NOT EXISTS` 对应 `financial_operations` 的行，`LIMIT 1` 即可报错；检查不受续页位置或 entry kind 限制，任何命中整页 503。它只访问该员工这一个账户的窗口范围、最多返回一条异常并受五秒 context 限制，不扫描其他账户或全部历史；查询失败/超时同样 503。随后来源候选仍按 typed action/actor 选取，不能用此孤儿检查替代选中及 lookahead 的完整证明。

游标采用已有安装密钥与每次新随机 nonce 的 AEAD，专属 purpose/version `cpacloud/self-topup-credit-history-cursor/v1`，与钱包活动、分类、兑换和调整历史游标互不接受。认证载荷只含安装绑定、员工 ID、当前 self-session selector、币种、limit、冻结窗口及最后返回的**原始** stored time/ID，不含金额或 payment 信息；不存入数据库或浏览器持久存储。任何账本读之前检查规范编码/结构、AEAD、purpose/version、安装、员工/session、币种/limit、精确 31 天窗口、合法且位于窗口内的位置、原 `window_end` 后不超过 15 分钟。错误、截断、跨 purpose/安装/员工/session/币种/页大小、过期及非法位置统一 `400 invalid_request`，不记录密文或解密细节。随机数、序列化、加密或长度失败为固定 503，必须发生在成功头/正文发出前。

## 单事务证明链和整页失败

由调用方从请求 context 派生最长五秒的**一个只读 SQLite 事务**完成页面；成功之前检查 Rows 迭代、Close、context 和事务 commit。事务内核对相关账户、账本 operation/entry、top-up、payment、connector 与 webhook event 的**精确可接受 schema**、索引、声明 FK 和已有不可变 trigger；payment/connector 是设计上可变的，不能虚构不可变 trigger，也不能让读操作修复 schema。只按表名存在不够；不要求全历史扫描。按 session 员工、`owner_kind='employee'` 和币种最多取两条账户，唯一行还需验证 SQLite 存储类型、规范 owner key、相同 employee ID、`key_id IS NULL`、空 resource、规范币种/时间。重复或畸形账户整页 503，Key/resource 或异员工账户绝不并入；无账户是正常空页。

对每条选中及 lookahead 来源候选，在上述**同一事务快照**中证明：

1. 分录及唯一对应 operation 的 ID、SQLite 动态类型、规范 UTC 时间、直属账户/币种均合法，两个 `created_at` 完全一致且真实处于窗口。operation 必须是 typed `system/payment_callback`，其余 actor ID 列为 NULL、系统 ID 正确且 `digest_version=2`、`payload_digest` 恰为 32 字节 BLOB；entry 必须 `kind='topup'`、金额为正、`original_entry_id IS NULL`。该 operation 恰有一条分录，就是选中的这条；entry 和 operation 都以 `resource_kind='topup'` 指向**同一个** top-up ID。根据当前不可变分录的直属员工 owner、币种、金额、单条有序 entry、operation/action/actor/resource 与 `RequireNonNegative=false`，按本仓库 `financial-post/v2` 语义重构并逐字节核对账本 digest；不能用旧 v1 或其他业务摘要替代。
2. `financial_topups` 必须恰有一个该 resource ID 的不可变 top-up，且其 payment ID 唯一；top-up `created_at` 与 payment `created_at` 为同一规范 UTC 时间。当前 `financial_payments` 的 ID、connector ID、account ID、external reference、币种、正金额、revision、存储类型与 top-up/entry/直属账户逐项对应，不能用同金额或时间拼接另一付款。payment 状态可为 `paid`、`partially_refunded` 或 `refunded`，但不能是 `pending`；其 `paid_entry_id` 必须精确指向选中 entry，`paid_at` 必须与 entry/operation 时间相同且规范。`refunded_micro` 与当前状态须满足现有范围/状态不变量；退款不会倒改此原始毛额。读取不以 connector 当前 `enabled` 为门禁，也不解密其 secret；但 connector FK 目标和与 payment/event 一致的 connector ID 必须存在。
3. 从已核对的 payment connector ID 构造精确 `webhook:` + connector ID + `:` 前缀，要求选中 operation ID 以此前缀开始；剩余部分必须是非空、两端无空白、最长 256 字节且无 NUL 的**完整** event ID。用已知 `(connector_id,event_id)` 复合主键只定位**这一条** `financial_webhook_events`，并核对其 `payment_id` 正是当前 payment；不能以同金额、时间或另一事件补链。该 event 的 connector/event/payment ID、32 字节 BLOB `payload_digest`、规范 `signed_at`/`processed_at` 和 SQLite 类型均有效；`processed_at` 与 `paid_at`/entry/operation 时间完全一致，`signed_at` 和 `processed_at` 的绝对时间差不得超过五分钟，呼应当前回调接受窗口。此检查不重验 HMAC：原始 body/签名不在这条读取链中，已存 digest 只能证明本地持久化的摘要形态，不能证明外部渠道收款。当前 schema 仅对 `(connector_id,event_id)` 唯一，**没有** `payment_id` 唯一/index；本批无 DDL，也不扫描全事件历史，因此不证明全表每个 payment 只有一个 event。

`financial_payments` 的后续合法退款更新不影响已提交的原始正向 credit，页面始终只显示当时的正毛额，不展示或减去退款。无需在本视图追溯 `topup.create` 的商业 receipt、证明订单创建全局唯一、汇总全部退款、重算当前余额，或给 payment/event 历史之外的来源作推断；这些不是本读页所承诺的外部资金证据。相反，**本页实际选中的**支付链任一必需字段、已声明的一对一/主键约束、时间、关联、摘要、schema/FK/trigger 若不一致，不能作为“旧历史”默默忽略；不得把未声明的 event-per-payment 唯一性混入其中。

schema/索引/FK/trigger 漂移、重复/错属账户、选中或 lookahead 坏链、SQL/scan/Rows 迭代/Close、context 取消/超时、只读 commit、游标编码失败均固定 `503 storage_unavailable`，整页不返回任何历史条目，不回退旧活动接口或补页。成功前在 `admission.Lock` 下最终复核**同一个** self session 的 verifier/selector、expiry、enrollment 与员工 active 状态，并在该锁下写成功响应；logout、改密或管理员禁用获胜时不得漏出旧页面。认证/Origin、路径/method/query 各按上述边界返回固定脱敏错误；错误不能包含 SQL、内部 ID 或凭据。不得记录 Cookie、cursor、员工 ID、金额、auth header、上游 token、connector secret/签名、payload digest、prompt 或模型响应。

## 网页、验收与不承诺事项

`/self/` 仅在自己的能力位为 true 时提供单独“回调入账原始毛额”入口。员工输入一个币种并**明确点击**后才查询；mount、登录、打开余额/活动/分类/兑换/调整、币种改变均不自动请求本路由，也不枚举币种。翻页也由员工明确触发。结果和游标只留组件内存；币种、账户或 session 改变、失败、登出、卸载均立即清空旧页，并以 abort 加请求 generation/身份比对丢弃晚响应。页面区分无账户与有账户但无合格记录，只以正金额和“本地回调入账毛额”表述；不把它写成已付款、可用余额、净额或退款状态。桌面与真实 Chrome 390px 都须无水平溢出。除必需的 API query，身份、结果和游标不写入页面 URL、localStorage、sessionStorage、IndexedDB 或 service-worker 缓存。

后续实施必须以**该实现的精确新 HEAD**报告独立证据：默认关闭与四项前置逐项在正常启动/`--init` 持久写前拒绝；原始/escaped/decoded/多层编码路径、开态畸形路径任意方法先认证再 400、规范非 GET 认证后 405、关态先 404 且无 Location/Allow、Origin/query/body；缺账户/空历史和 Key/resource/异员工隔离，直属账户窗口内缺 operation 的有界孤儿检查；typed callback 与旧 `topup`/legacy/admin/employee 来源区别；selected/lookahead 的 kind、符号、actor、v2 digest、operation 唯一 entry、top-up/payment/精确复合键 event/connector 链、paid 指针与时间及 signed/processed 五分钟界、不同退款状态和当前开关关闭仍读原始毛额；同 stored time、混合精度、页边界、跨 purpose/session/币种/limit/安装与 15 分钟游标；schema/FK/trigger、SQL/scan/Rows.Close/commit/context/RNG 故障整页 503；logout/改密/admin-disable 最终输出竞态；财务表不被写入、敏感字段和日志不泄露。以独立合成事实运行定向测试、全量 Go build/vet/lint 与 Web 检查、动态**非 8787**隔离 HTTP 进程及真实 Chrome 桌面/390px；本合同提交不等于任何一项已通过。实施差异先留 unstaged 给只读审核，审核放行后才讨论提交、push、PR/CI 或固定二进制验收。

本批不引入 provider 客户端、付款请求、签名协议变化、资金写入、worker、DDL/迁移、invoice/税务/权益、全时代完整性、多实例一致性、tag、package、发布或部署。`GOV-02` 告知、角色、保留和恢复决策仍开放，不可生产默认启用。未来桌面客户端的配置备份、原子写入、验证及回滚仍是未来需求，不能把本页称作已提供。

## 来源、许可和当前验证状态

本合同是对 CPA Cloud 自有功能规格及上述基线本仓库 `internal/financial` 的只读结构核对后独立撰写；曾见过参考代码，不声称严格 clean-room，未复制、翻译、逐行改写或移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考项目的代码、测试、迁移、资产、文档。公开机制资料已于 2026-10-04 核对：[Go `net/http` 路由及 URL](https://pkg.go.dev/net/http)、[Go `database/sql` 事务与 Rows](https://pkg.go.dev/database/sql)、[Go `cipher.AEAD`](https://pkg.go.dev/crypto/cipher#AEAD)、[SQLite 事务隔离](https://www.sqlite.org/isolation.html)、[SQLite 外键](https://www.sqlite.org/foreignkeys.html)、[SQLite 触发器](https://www.sqlite.org/lang_createtrigger.html)、[SQLite `SELECT` 排序](https://www.sqlite.org/lang_select.html)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。这些公开资料只支持通用技术机制；回调/账本链、字段含义和失败政策均为本项目自身契约，而非外部 provider 协议。此合同不新增源码、依赖、SDK 或素材；后续若新增源码须显式标注本合同 provenance，既有第三方依赖保留各自许可证，见[依赖许可记录](research/dependency-notices.md)。

截至本文合同先行提交，仅核对了自有规格、当前 schema/写路径和上述公开来源，并执行文档链接及 Git diff 检查；未运行本能力的自动化测试、独立 HTTP/浏览器验收或新 HEAD 的 GitHub CI。已测试行为和计划行为必须继续分开报告。
