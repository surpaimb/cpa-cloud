# ID-05 / BILL-03 员工本人订阅一跳续购关联契约

状态：2026-10-02 独立设计合同，开发预览、默认关闭；本文件先于实现，**不**宣称路由、网页或验收已经完成。精确基线为已合并的 `main` `1cc08fce3f8f947eb6c160301737698472315f76`。本批只让当前员工按已知 ID 读取本人一条既有订阅的直接前驱与直接后继 ID，扩展[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[冻结月周期与到期](subscription-period-expiry-contract.md)、[管理员即时续购](subscription-manual-renewal-contract.md)、[一次性预约续购](subscription-one-shot-renewal-contract.md)、[本人月度即时续购](employee-self-monthly-renewal-contract.md)、[本人购买时记录](employee-self-subscription-purchase-snapshot-contract.md)及[财务 actor 来源](financial-actor-provenance-contract.md)。它不是完整链枚举、账单、付款凭证、可用额度、模型权益或新的续购动作；GOV-02 仍开放。

## 独立开关与身份

新增启动开关 `--employee-self-subscription-renewal-links-enabled` / `EmployeeSelfSubscriptionRenewalLinksEnabled`，默认 `false`；独立能力为 `features.employee_self_subscription_renewal_links`。显式开启只依赖 `--employee-self-service-enabled` 与 `--employee-self-subscription-status-enabled`，缺任一前置应在持久状态变更前拒绝启动。钱包余额、购买时记录、员工即时续购、一次性预约、套餐目录和当前商业执行开关均**不是**前置；只开启任何前置或邻近能力不会自动开启本批。关闭时新路由为 404、能力为 false、网页无入口；旧管理员、worker、员工自助及模型请求保持原义。即使 `financial_settings` 已关闭商业执行或当前套餐被禁用、改价、换币，已提交的历史关联仍可按本开关读取。

仅当前已开通且 `active` 员工的有效独立 self session 可读。身份只来自服务端核验的 session，不能由路径以外的参数、管理员 Cookie、员工模型 Bearer Key 或另一员工的 session 指定。GET 沿用 self 读入口：若带 `Origin`，必须单一且匹配同源；不要求当前密码、CSRF、`X-Self-Request`、操作 ID 或写侧限流。登出、停用、session 到期/撤销后不能继续读取。应用生成的所有成功与错误响应均 `Cache-Control: no-store`，不记录 Cookie、授权头、原始 SQL、账本内容或响应体。

## 唯一接口与最小响应

仅注册 `GET /self/api/v1/billing/subscriptions/{id}/renewal-links`。`{id}` 是一条既有订阅 ID，只能是 1–256 字节的非空不透明**单一路径段**，按既有 ID 语法校验；不得接受解码斜线/反斜线、点段、双重编码百分号、额外段、末尾斜线、大小写变体或自动重定向后的等价路径。拒绝任何 query（包括裸 `?`）、任何请求体、未知长度或 `Transfer-Encoding` 传输体。有效 self session 下可达应用的畸形路径/请求为固定 `400 invalid_request`；精确路径的其他方法为 `405 method_not_allowed` 且 `Allow: GET`；开关关闭仍为 404。Go HTTP 解析器在构造请求前拒绝的非法原始转义（如 `%ZZ`）属于传输层 400，不承诺应用 JSON/header。匿名或无效 self session 为 401，错误 Origin 为 403；不存在、非本人以及 Key/resource 子账户目标统一 `404 not_found`，不以其账务损坏程度区分。相关 schema、行、链、SQL、迭代、关闭、提交或取消异常均为固定 `503 storage_unavailable`，成功前不得输出部分字段。

成功 `200` 的 JSON **恰好**三个字段：

```json
{"subscription_id":"subscription_example","predecessor_id":null,"successor_id":"subscription_next"}
```

`subscription_id` 等于路径目标；`predecessor_id` 与 `successor_id` 各为存在且经核验的一跳订阅 ID 字符串，或 JSON `null`。两个 null 是合法结果，例如本人直属 `one_time` 订阅，或尚无续购关联的月度根订阅。目标可为根、中间或末端；后继后来取消、到期或再次被续购，不会擦除本次读取的一跳历史。不得递归展开或返回完整链、关联计数、operation/actor/account/entry、计划、币种、金额、余额、预约、状态、时间、摘要、员工身份或当前可购性。关联 ID 只表示本地已记录的相邻事实，不证明外部付款、当前权益或财务订阅对模型访问的控制。

## 单快照、归属先行与关联证明

HTTP 外层持有并负责结束一个不超过五秒、可取消的 SQLite **只读**事务；整份结果在提交明确成功且请求上下文仍可用后才写成功头。启动前已有的精确财务 schema、索引、FK/不可变触发器按现有校验规则验证；GET 不运行 DDL/迁移、不补链、不触发到期或预约 worker、不调用自动开户、账本写入、provider、支付连接器或 admin HTTP 服务。若现有验证无法在本接口边界以有界读证明下列关联，须报告具体阻断，不能凭两列 link 就返回看似可信的 200。

1. 在同一事务中先按目标 ID 查订阅的账户归属，**先**确认账户是当前 session 员工直接拥有的 `owner_kind='employee'`，才校验目标可变订阅字段或读取其关联/回执细节。缺目标、他人或 Key/resource 账户统一 404；本人账户再核规范 owner key、NULL Key ID、空 resource 字段、账户与目标币种一致、有效 ID/存储类型/UTC 时间、冻结周期与状态形状、取消时间、计划 FK。本人坏行/坏账户为 503；他人的坏业务链不能经错误码或字段泄漏。不能把账户 owner 猜成执行 actor。
2. 对目标分别按 `successor_id=?` 查 incoming、按 `predecessor_id=?` 查 outgoing，各取 `LIMIT 2` 并检查迭代、`Rows.Err` 与 `Close`，以发现重复/不一致而非静默取首行。无关联时也须核目标的创建回执与 incoming 形状相容：`subscription.create` 不能有 incoming，`subscription.renew` 不能缺其唯一 incoming；`one_time` 不能出现在任何续购边。只验证目标的一跳，不沿前驱或后继继续遍历；不得全表物化或按员工批量枚举。单实例写事务与本只读 WAL 快照并发时，只能看到同一完整提交之前或之后的状态，不能拼接半条链。关联后续变化由下一次请求的新快照体现。
3. 每条存在的 link 须精确验证其唯一前驱、后继、operation ID 与规范 UTC link 时间；两端 ID 互异，均为有效 `monthly` 订阅、同一冻结 plan ID，双方账户各自都是**当前员工直属** owner。允许合法历史换币、不同同员工账户；不得错误要求两端账户 ID/币种相同。前驱存储状态须为终态 `expired`，冻结 `period_end_at <=` 后继 `started_at`；后继可以后来 `cancelled`、`expired` 或再有后继，不能把当前状态误当购买时状态。link 时间须等于后继开始时间。破链或边端转成他人/Key/resource 均为已授权目标的 503，绝不返回未经核验的邻居 ID。
4. 用 link 的全局 operation ID 核后继的**唯一** `subscription.renew` 商业创建回执（资源是该后继、revision 1、规范时间与 link/后继开始一致、32 字节业务摘要）及同 ID `subscription_purchase` 账本操作（同资源/时间、有效摘要版本与相同的规范 actor）。有界检查恰好两条分录：同一后继直属账户、operation、币种、资源和开始时间，一条负 `subscription_charge` 与一条正 `subscription_credit`，金额与后继冻结正整数价格/授额一致、无原始分录引用；缺、重复、额外或混账户均失败关闭。后台 status/cancel/预约回执不能冒充创建回执。对合法 admin、`subscription_one_shot_worker`、PR #50 typed employee，以及既有双方同为 `legacy_unknown` 的历史链，分别保持[购买时记录](employee-self-subscription-purchase-snapshot-contract.md)和[actor 来源](financial-actor-provenance-contract.md)已支持的版本/摘要/归属规则；employee 续购两侧 actor 必须是当前员工且核 v2 账本与员工业务摘要。不能把 unknown 推断为当前员工或把任意 system/混合 actor 放行，也不宣称 `financial_subscriptions` 整行数据库不可变。

外层在事务内再次确认当前 Cookie verifier/session 未过期未退出、员工仍 active 且已开通、可选 Origin 仍合法，且在成功写头前显式 `Commit` 并检查 context。与本进程 self session 撤销/停用/登出之间须有序列化屏障或等价证明，不能仅依赖过时 WAL 读快照作最终授权。所有目标相关读取有固定上界；schema/扫描/迭代/关闭/提交失败、超时或请求取消都整响应 503 且**零部分输出**。读取成功、拒绝或故障均不改变任何财务、session、预约或业务行；商业开关和当前计划不参与历史关联判定。

## 网页、隐私及验收边界

`/self/` 仅当 `features.employee_self_subscription_renewal_links=true` 且员工**已经显式读取**本人订阅列表后，为其中一条目标提供独立“查看直接续购关联”控制。挂载、登录、列表读取、悬停、翻页均不能自动读取关联；点击只请求当前一条，不 fan-out，不递归。结果只留组件内存，不写 localStorage/sessionStorage、URL 或 service-worker 缓存。切换目标、列表页、账号/session、登出、卸载或任何失败时清空原结果并取消在途请求；AbortController 加身份/目标/请求 generation 阻止晚响应复活。桌面与真 Chrome 390px 显示双 null、单向和双向关联及“一跳记录、不等于完整链/账单/付款/权益”的提示，长 ID 不得造成水平溢出。旧列表只是候选，服务端事务始终重新核所有权和链。

本合同提交阶段**仅**新增本文、验证相对链接与 `git diff --check`，报告合同 commit 的精确 HEAD/tree、与 `main` `1cc08fce3f8f947eb6c160301737698472315f76` 的 merge-base 和工作树状态；交集成负责人全文只读审过前不开始实现、不 push/PR/CI。后续独立测试须覆盖默认关/缺前置、商业关和当前计划变化仍可读、匿名/admin/Bearer/跨员工/禁用/登出/到期/Origin；严格方法/路径/query/body；本人根/中间/末端、无 link 的 `one_time`、取消/到期及再续购、不同币同 owner；admin/worker/employee/合法 legacy 链；重复/孤儿/交叉 owner/link/receipt/ledger/entry/摘要损坏与外人坏行的 404/503 隔离；并发写前后快照、schema/扫描/迭代/关闭/commit/cancel 故障下零部分输出与零副作用、重启；UI 默认关、显式单条点击、切换/失败/晚响应及桌面/390px。核返回字段集合、财务主键/行数和无网络调用，分别报告实际测到与仍计划的行为。

合同审过后的实现先集中做本地路径/授权/链/UI 审计与定向 Go/vet/Web、随机非 8787 合成进程、真 Chrome 桌面/390px，再一次提交/推送触发完整精确 HEAD CI；独立固定二进制验收与 CI 实际 PASS 前 PR 保持 Draft。本批无新 DDL、财务写入、员工 arm/取消/购买、退款、完整账单、支付、模型权益、第三方依赖、tag、package、部署或真实 8787/真实 provider 凭据。生产启用仍受 GOV-02 告知/保留/恢复及单实例边界约束。

本规格及后续新源码/测试只依据上述 CPA Cloud 功能规格、精确 `main` 基线和公开技术文档独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树；此前看过参考代码，故不称严格 clean-room。2026-10-02 核对：[Go `database/sql` 事务与取消](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go `net/http` 路由](https://pkg.go.dev/net/http#ServeMux)、[SQLite WAL 快照隔离](https://www.sqlite.org/isolation.html)、[SQLite `SELECT`/`LIMIT`](https://www.sqlite.org/lang_select.html)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。新关联输出是 CPA Cloud 内部协议，不引用供应商私有实现；既有 Go、modernc SQLite、React 和测试依赖保留各自许可证，本合同不引入新 SDK、依赖或素材。
