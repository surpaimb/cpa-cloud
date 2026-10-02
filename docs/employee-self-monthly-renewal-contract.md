# ID-05 / BILL-03 员工自助已到期月订阅即时续购契约

状态：2026-10-02 独立设计合同，开发预览、默认关闭；本文件先于实现，**不**宣称功能或验收已完成。精确基线为已合并的 `main` `f247ec7d4c63137c3ed6d05dc4975d07344dd6cf`。本批只允许已开通员工以**本人直属、已经存在的钱包**，对本人一条已到期的 `monthly` 订阅前驱即时购买恰好一个后继周期。它扩展[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[本人钱包余额](employee-self-wallet-balance-contract.md)、[冻结月周期与到期](subscription-period-expiry-contract.md)、[管理员手工续购](subscription-manual-renewal-contract.md)、[一次性预约](subscription-one-shot-renewal-contract.md)、[本人购买事务原语](employee-purchase-transaction-primitive-contract.md)、[本人购买时记录](employee-self-subscription-purchase-snapshot-contract.md)及[财务 actor 来源](financial-actor-provenance-contract.md)。本批不是任意 `monthly` 根订阅新购、自动续费、员工预约、退款、旧额度回收、模型权益或真实付款；GOV-02 仍开放。

## 独立启用、身份与兼容性

新启动开关 `--employee-self-subscription-renewal-enabled` / `EmployeeSelfSubscriptionRenewalEnabled` 默认 `false`，独立能力为 `features.employee_self_subscription_renewal`。显式开启须同时开启现有 `--employee-self-service-enabled`、`--employee-self-subscription-status-enabled` 和 `--employee-self-wallet-balance-enabled`；少一个应在修改持久状态前拒绝启动。本人购买时记录、套餐目录、one-time 自购、取消订阅和一次性预约开关**不是**启动前置。只开启前置能力不能自动开启本批。新开关关闭时两条新路由均为 404、能力为 false、网页无续购入口；旧管理员/worker/自助读写保持原义。本批不要求 `financial_settings.enabled` 在启动时已开启：该可变商业执行开关是报价和**新续购**的事务门禁；关闭后仍可处理已提交、完全匹配的同 ID 重放，不能在路由层提前拒绝。

两条接口只接受当前已开通且 `active` 员工的有效独立 self session。员工身份只来自该 session；匿名、管理员 Cookie、员工模型 Bearer Key、其他员工的 session 均不能替代。每次 POST（包括精确重放）都要求单一匹配的 `Origin`、`X-Self-Request: 1` 和与当前 session 一致的 `X-CSRF-Token`；同源/CSRF 错误先于财务读取和写入拒绝。沿用自助 Cookie 的 HttpOnly、SameSite、路径及非回环 TLS 约束。所有应用成功和错误响应均 `Cache-Control: no-store`；Go HTTP 解析器在构造请求前拒绝的非法原始请求目标不属于应用 JSON/header 契约。

## 严格路径、请求与错误

仅注册 `POST /self/api/v1/billing/subscriptions/{id}/renewal-quotes` 与 `POST /self/api/v1/billing/subscriptions/{id}/renew`。`{id}` 是**既有前驱订阅 ID**，只能是一个不超过 256 字节的非空不透明路径段；不接受解码斜线/反斜线、双重编码百分号、点段、额外段、模糊大小写或重定向后的等价路径。两路均不接受 query（包括裸 `?`）、不接受未知长度传输体；报价请求**没有请求体**，`Content-Length` 须为零且不得有 `Transfer-Encoding`。提交请求只接受最多 4096 字节 UTF-8、单个 JSON 对象，**恰好**三个唯一字符串字段：`operation_id`、`quote_token`、`current_password`。拒绝重复/未知字段、尾随 JSON、非法类型或超限输入。`operation_id` 为现有 1–128 字节非空不透明全局操作 ID；密码沿用现有 12–72 UTF-8 字节规则。两路均不接收客户端员工/owner/account/Key/resource、计划或金额/币种/period、successor、时间或报价 revision 选择器。有效 self session 下可到达应用的畸形语法固定 `400 invalid_request`、不跳转；其他方法在开关开启且路径精确时为 `405`、`Allow: POST`，不开关仍为 404。

不存在、非本人或属于 Key/resource 子账户的前驱统一 `404 not_found`；在本人的有效行上，未到期、取消、`one_time`、已有后继、当前计划不可用/变价、报价失效、缺直属同币钱包或余额不足等可购性失败使用固定脱敏 `409 renewal_unavailable`，不暴露其他人的存在或余额。认证失败为 401，Origin/CSRF 错误为 403，既有限流返回 429；schema/SQL/随机数/AEAD/超时/取消/提交不确定为固定 `503 storage_unavailable`。全程不返回原始 SQL、密码、token、摘要或部分成功字段。

## 第一步：只读冻结报价

员工在本人订阅状态列表**明确选择**一条投影为 `monthly` 且 `expired` 的记录后，请求上述 `renewal-quotes` 路由。服务端在不超过五秒的单个可取消只读 SQLite 事务中重核当前 session/active 员工、财务 schema、直属 employee owner 与现有账户形状，要求该前驱是有效 `monthly`、未取消、`period_end_at <=` 捕获的事务 UTC、尚无 successor；即使到期 worker 尚未把存储 `active` 写成 `expired`，也按有效到期判断。已经存储 `expired` 亦可报价；管理员/worker/其他自助续购先产生后继时不得报价。报价须验证当前商业执行已开启、同一 plan ID 的**当前**计划仍启用且 `monthly`，并读取其完整当前 `revision`、三位大写币种、正整数 `price_micro` 和 `credit_micro`。币种可以与前驱原币种不同，但员工在当前报价币种下必须已有直属钱包；Key/resource、他人或其他币种账户不能替代，**不得调用自动开户**。报价不预留余额、不保证提交时足额，也不冻结当前计划后续管理变更。

事务对相关行、schema、迭代/关闭和提交结果失败关闭，且在成功前不发送响应字节。它不写报价行、账户、订阅、分录、预约或任何财务事实。事务完成后用既有安装持久 AEAD、每次新随机 nonce、独立 purpose/AAD `cpacloud/employee-self-monthly-renewal-quote/v1` 签发规范 URL-safe、最多 2048 字节的五分钟 token。认证明文绑定版本/purpose、员工 ID、**当前 session selector**、前驱 ID、前驱原冻结 `period_end_at`，以及当前 plan ID、revision、currency、price、credit 和固定 `monthly` interval、规范 UTC 签发/失效时间；不绑定会被到期 worker 正常推进的前驱存储 `status` 或 `revision`。错误安装密钥、篡改、截断、非规范编码、未知字段/版本/目的、跨员工/跨 session 均固定拒绝。token 不入库、不是余额预留或一次性券；只有服务器时钟决定新购在 `issued_at <= now < expires_at` 有效，恰好到期即失效。

成功 `201` 响应**仅**含 `quote_token`、`predecessor_id`、`predecessor_period_end_at`、`plan_id`、`revision`、`currency`、`interval:"monthly"`、`price_micro`、`credit_micro`、`expires_at`。两个 micro 值为规范正十进制**字符串**，两个时间为规范 UTC RFC3339Nano。不给账户 ID、余额、后继 ID、ledger/operation ID、密码、付款或权益。报价展示须明确“当前钱包价格的短期快照，尚未扣款；最终以提交时再次核验为准”。

## 第二步：当前密码与同事务即时续购

提交前按现有 self 写入口对 peer/员工做有界失败限流；严格解析路径和请求，认证 token 的结构、AEAD、目的和当前员工/session 绑定。**先不**用报价年龄、当前开关、当前计划/余额、前驱当前状态或 successor 作已提交重放门禁。每次提交、**包括精确重放**，都要求当次 `current_password` 与员工现有 bcrypt hash 相符；失败计入限流并返回统一凭据错误。bcrypt 可在事务外做，但期间不能持有跨整个服务的 admission 写锁，也不能相信其前读取的 session/密码行持续有效。

随后外层持有当前进程 admission 写锁，开启不超过五秒、由 HTTP 外层**持有并负责提交/回滚**的可取消写事务。在同一事务内重新核 cookie selector/verifier、未过期且未退出的 self session、持久 CSRF 与请求 header、员工仍 active 且已开通、密码 hash 与刚验证的 hash 完全相同；提交前再次重核。登出、改密、停用、过期或切 session 与结果确认不能穿插；SQLite 忙或旧 WAL 读快照无法升级写锁时整事务回滚为 503，不在旧快照上继续。任何成功头/字节必须在 `Commit` 明确成功、上下文仍可用后才发送。

最终授权后，**先于任何仅针对新续购的门禁**，按全局 `operation_id` 在该事务内探测已提交事实。服务端规范业务指纹必须以独立域/version、操作 ID、`subscription.renew` action、typed employee actor、直属 owner、路径前驱 ID、前驱冻结结束时间和**完整报价计划快照**组成；不把 token 字节/随机 nonce、session secret、密码或观察/签发时间当成幂等身份。探测同一 ID 的商业 receipt 与 v2 ledger operation/两分录、前驱→后继唯一 link、新订阅冻结字段和直属 owner；所有 actor、action、指纹、资源、金额/币种、时间和链必须精确一致。已提交精确匹配时返回原后继，不重新扣款、不检查报价是否过期或当前商业开关/计划/余额/前驱到期门禁；仍须本次有效 session、CSRF、当前密码及 token 认证。不同 actor/前驱/报价、管理员/worker 同 ID、单侧或损坏 receipt 一律冲突或 503，绝不“补齐”事实。另取同业务快照的新 token 可对已提交同 ID 做精确重放，但不能把变价或不同前驱伪装成相同业务指纹。

只有确无已提交同 ID 操作时，才在**同一事务和当时服务器 UTC**检查报价未过期/未来签发、前驱仍本人直属月订阅且有效到期、未取消且无 successor、原冻结 end 与 token 一致、当前商业执行开启、当前计划仍启用且与 token 的 ID/revision/币种/价格/额度/`monthly` **逐项相同**。须验证该员工在当前计划币种下有**既存**直属钱包及足额余额；Key/resource、其他员工或缺账户不可代付。此员工专用路径复用现有 `renewSubscriptionTx` 的财务链和唯一 link 语义，但不得调用其自行 `BeginTx/Commit` 的管理员包装，也不得沿用会经 `EnsureAccountTx` 自动新建钱包的分支；应在共享财务转移内增加“仅既有直属账户”策略或等价的同事务强制验证，而非复制一套易分歧的续购账本。若当前实现无法在共享链内确保零新账户，先报告具体阻断，不放宽本要求。

成功的新购与现有管理员/worker 续购有同一财务含义：捕获提交事务 UTC，必要时仅一次把到期前驱持久化 `expired`、revision 加一；冻结当前计划快照，生成一条新的 active 月订阅，按现有**从成交 UTC 起**的一日历月月末夹取规则确定独占结束时间；同一既有直属钱包中以 typed employee actor 和同一全局 ID 记一条 `subscription_purchase` v2 ledger operation、恰好两条扣价/授额分录、一条 `subscription.renew` typed employee commercial receipt、唯一前驱→后继 link，并把仍 `armed` 的一次性预约在同事务标为 `superseded/manual_renewal`。旧周期不倒填、不追回旧授额、不退款；新后继不继承预约。管理员即时续购、worker 和员工续购在单实例 SQLite 写事务及唯一 link 下**先提交者胜**：输家回滚全部候选事实，不能产生第二个 successor 或第二笔扣/授额。每个前驱至多一个后继；**不**承诺同员工同套餐在不同根/链上的全局无重叠。

首次提交 `201`，精确已提交重放 `200`。响应只含 `operation_id`、`predecessor_id`、`subscription_id`（后继）、`replay`、冻结 `plan_id`/`revision`/`currency`/`interval`/`price_micro`/`credit_micro`、`started_at` 与 `period_end_at`；两个金额为规范微单位字符串、时间为 UTC，不含账户/entry/摘要/token/密码。输入、权限、业务门禁、schema、SQL、触发器、余额溢出、取消或持久化失败使外层回滚；不能留下仅扣款、仅授额、仅后继/receipt/link、半个预约终态或新钱包。提交报错/结果不确定固定 503，不自动换 ID 或二次扣款；若实际已提交，同业务指纹与 ID、再次当前密码可在过期报价/关闭商业开关后恢复原结果。若实际未提交且报价已过期，拒绝新购，须重新报价并使用新 ID。提交后断连不删除已提交不可变事实。

## 购买时记录与现有路径的精确兼容

本批必须把[本人购买时记录契约](employee-self-subscription-purchase-snapshot-contract.md)目前对 employee `subscription.renew` 的拒绝，**仅**扩展到本批合法的 employee 续购：commercial 与 ledger 两侧均为同一个 typed employee ID（且 ledger digest v2），与当前读取者/前驱和后继的**直属 employee owner**一致，确有同 operation 的唯一 incoming link、当前 successor 快照与两条分录完整一致。错误员工 ID、admin/employee 跨侧错配、Key/resource owner、缺/多 link、损坏摘要或条目仍 503 或既有统一 404，不因 actor 类型放宽成任意续购。既有 admin、`subscription_one_shot_worker` 及合法历史 `legacy_unknown` 的链验证、读取和测试必须保持原义；不要求购买快照 flag 才能启动或执行本次续购。

现有管理员即时续购、到期 worker、one-shot worker、自助 one-time 购买、历史查询和取消/撤销接口不得因本批改变授权或金额含义。新路由不调用 provider、支付连接器或每请求 admin HTTP 服务。无 DDL、新第三方依赖或持久报价；不能借本批增加自动续费、员工 arm、任意月度根购买、退款/回收、发票/税、余额兑换、模型权益或跨实例结算。

## 网页、隐私与晚响应

`/self/` 只在 `features.employee_self_subscription_renewal=true` 为本人**已经显式读取**的订阅列表中 `monthly + expired` 项显示续购入口；列表只是候选，旧列表项的 successor、owner、计划和余额均以服务端事务核验为准。员工明确点击第一步报价，看到当前冻结币种、价格、额度、旧周期结束、五分钟失效和“从本人现有钱包扣款；新周期从成交事务捕获的服务器 UTC 开始”的提示，再做第二步主动确认并输入**当前**密码。订阅到期不等于模型权益失效，报价不是付款凭证；不显示自动续费开关或外部付款承诺。商业开关/计划变化、旧列表 stale 或钱包不足用脱敏错误和重新报价引导，不默许提交旧快照。

token、当前密码、操作 ID、报价和待决结果只在组件内存，不写 localStorage/sessionStorage、页面 URL、service-worker 缓存或日志。报价/提交请求各有 AbortController 与当前员工/session/前驱/报价 generation 校验；切换账号/session、目标前驱、报价、列表页、登出、卸载或确定失败会清空旧报价、密码与结果，并阻止晚响应复活。唯有提交超时、断连或 503 等**未知结果**可在同一员工/session/前驱/报价上下文暂留原 ID 与原 token 的仅内存待决重试信封，不回填旧“可购买”报价；员工须重新输入密码并**显式**重试。切换上述任一上下文立即销毁信封；精确成功或确定冲突亦销毁。桌面与真 Chrome 390px 须检查两步确认、长 ID/金额、能力门控、失败清空与晚响应，无水平溢出。

## 验收、来源与发布边界

先只提交本合同，检查相对链接和 `git diff --check`，报告精确 HEAD/tree/merge-base/status，待集成负责人只读审完再实现。后续独立测试至少覆盖：默认关/缺前置 flag 拒启与商业后关重放；匿名/admin/Bearer/跨员工/禁用/登出/过期/改密、Origin/CSRF、每次重放密码及限流、bcrypt 与会话状态竞态；无 body/查询/路径编码/重复字段等严格语法；报价 purpose/安装/session/员工绑定、五分钟边界、到期 worker 改存储 revision 不使报价失效、旧冻结 period end 与计划全字段变化；本人直营账户/缺账户/Key-resource 账户、余额不足/溢出、不同币种现有账户；管理员/worker/员工三向并发唯一后继、armed 一次性预约 superseded、旧额度不回收与月末/闰年；同 ID 精确重放先于新购门禁、不同 quote 业务指纹/actor/前驱冲突、未知 commit/重启；schema、读迭代/关闭、账本、事务/commit/取消故障下零部分写入；购买时记录新 employee actor 与旧 admin/system/legacy 链回归；UI 桌面/390px、迟到响应和待决显式重试。核账务主键集合、行数、owner、actor、receipt/link、余额及费用，不只核 HTTP 状态。

合同审过后的实现先集中做本地路径/鉴权/数据链/UI/竞态审计与定向 Go/vet/Web、随机非 8787 合成进程和真 Chrome 验证，再**一次**提交/推送触发完整 CI；不以小修多次提交或并行 Windows SQLite 全仓代替。最终精确 HEAD 另提供普通实体、非浅、clean Git 源与固定 binary path/hash/Go VCS 身份，供独立双随机根黑盒验收。Draft PR 在同 HEAD CI 必要实际 PASS 和独立验收前不得 ready；本批不跑 native/package、不部署、不 tag、不发布，不使用真实 8787 或真实 provider/支付凭据。生产启用仍须解决 GOV-02 告知/保留/恢复与跨实例结算边界。

本合同及后续新增代码/测试依据上述 CPA Cloud 功能规格和本仓库精确基线独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树；此前看过参考代码，故不称严格 clean-room。2026-10-02 核对的公开技术资料：[Go `database/sql` 事务与取消](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go AEAD `Seal`/`Open`](https://pkg.go.dev/crypto/cipher#AEAD)、[Go `net/http` 路由](https://pkg.go.dev/net/http#ServeMux)、[SQLite WAL 快照隔离与写者序列化](https://www.sqlite.org/isolation.html)。新增令牌和金额规则是 CPA Cloud 内部协议，不引用供应商私有实现。现有 Go、modernc SQLite、React 与测试依赖保留各自许可证；本合同不引入新 SDK、依赖或素材。
