# ID-05 / BILL-04 员工自助兑换码入本人直属钱包契约

状态：2026-10-02，**先行设计合同，尚未实现或验收**。精确基线为已合并的 `main` `1aba407f3607e62e3221b0d6cf509a8c7092e24c`。本批只允许已开通员工用管理员既有一次展示的兑换码，为**本人直属、该码币种的钱包**增加一笔定点余额；没有公开注册、真实付款、促销/推荐/分佣、退款、订阅购买或模型权益。它遵循[产品边界](product-plan.md)、[独立实现边界](independent-implementation.md)、[开发预览](preview-contract.md)、[单实例财务契约](single-instance-billing-contract.md)、[财务 actor 来源](financial-actor-provenance-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)和[本人钱包余额](employee-self-wallet-balance-contract.md)。员工自助兑换不同于现有管理员代指定 owner 兑换；后者不改变。

## 精确现状与迁移界线

当前 `financial_redemption_codes` 只存 32 字节 keyed digest、币种/正微单位金额、使用上限/次数、可选 UTC 失效时刻及启用位；`financial_redemptions` 的 `(code_id,account_id)` 唯一约束，账本 operation/entry 与商业 receipt 已有 typed actor 及不可变链。管理员发码使用安装密钥的现有 `billing-redemption/v1` HMAC purpose，明文只在首次创建结果显示；管理员兑换可指向 employee/Key/resource owner。新入口须对**同一批已发码**计算相同 purpose 的 HMAC，不改变码格式、摘要密钥或既有管理员路由。账本账户按 `(owner_key,currency)` 唯一，直属 employee owner 与 Key/resource 子账户不可互换。上述现有结构足以承载本批，**不计划 DDL、历史重写或新第三方依赖**；如实现中发现无法用现有约束维持此合同，应先报告具体阻断，不能悄悄放宽边界。

现有 `Commercial.Redeem` **不能直接用于自助路由**：它自行 `BeginTx/Commit`，且先检查 `financial_settings.enabled` 才探测重放。员工路径必须由 HTTP 外层持有可取消写事务、先做已提交精确重放探测，再做仅针对新兑换的商业门禁；可提取受同一测试约束的内部财务原语，但管理员公开行为、已提交管理员事实及其重放语义保持原状。金额为正 `int64` 微单位，币种为三位大写 ASCII，余额由不可变分录有界求和，不使用客户端声称的金额或余额。

## 独立启用、身份与路由

新增 `--employee-self-redemption-enabled` / `EmployeeSelfRedemptionEnabled`，默认 `false`；能力位精确为 `features.employee_self_redemption`。无论正常启动还是 `--init`，显式开启本位都必须同时开启 `--employee-self-service-enabled` 和 `--employee-self-wallet-balance-enabled`；缺任一前置须在**任何持久写入前**拒绝，不能自动开启前置。本人订阅、购买、目录、钱包活动等开关都不是前置，只开前置也不会开启兑换。关闭时新路由一律 404、能力位 false、网页无入口，旧管理员兑换及其他自助接口不变。可变 `financial_settings.enabled` **不是启动前置**，只禁止新兑换；它关闭以后仍允许本开关下已提交的完全匹配重放。

只注册 `POST /self/api/v1/billing/redemptions`，不加 GET、预检码有效性、按码查询或管理员代理接口。只接受当前有效且已开通、`active` 的独立 self session；员工 ID 仅从该 session 导出，匿名、管理员 Cookie、员工模型 Bearer Key 或他人 session 都不能替代。每一次调用（含重放）都要求现有 self 写入口的唯一同源 `Origin`、`X-Self-Request: 1`、唯一且与持久 session 一致的 `X-CSRF-Token`，并经过按 peer/员工有界失败限流。沿用 self Cookie 的 HttpOnly、SameSite、路径、默认 loopback 和非回环 TLS 规则，不调用 admin HTTP 服务。

请求目标必须是上述字面路径；拒绝任何 query（包括裸 `?`）、额外路径段、大小写变体或重定向等价路径。只接受已知且不超过 4096 字节的 UTF-8 JSON 请求体，拒绝未知长度/`Transfer-Encoding`、重复键、未知键、尾随正文及非字符串字段；对象**恰好**含 `operation_id`、`code`、`current_password` 三个唯一字段。`operation_id` 是现有全局 1–128 字节非空不透明 ID；`code` 为最多 256 字节的原样字符串，不修剪、不改大小写、不进行 Unicode 归一化，空值或未命中仅作兑换不可用；密码仍为现有 12–72 UTF-8 字节规则。客户端不得提交 owner/account/employee/Key/resource、币种、金额、兑换码 ID、使用次数或时间。已通过 self 身份门禁的畸形应用请求为固定 `400 invalid_request`；关闭路由时优先 404。只有开关开启且路径精确时，其他方法为 405 并带 `Allow: POST`。

新兑换成功为 `201`，同一 `operation_id`、同一 typed employee、同一 HMAC 码摘要及同一直属 owner 的**已提交完整事实**重放为 `200`。成功 JSON **恰好五个字段**：`operation_id`、布尔 `replay`、`currency`、规范正十进制**字符串** `amount_micro`、规范 UTC RFC3339Nano `credited_at`；重放使用原提交的币种、金额和入账时刻。不得附码明文/摘要、码 ID、账户/分录/receipt ID、余额、付款或权益。所有应用成功和错误响应设 `Cache-Control: no-store`，且在明确提交成功前不发送成功头或正文。Go HTTP 解析器在构造应用请求前拒绝的非法原始请求目标不属于上述应用 JSON/header 契约。

## 统一失败面与密钥处理

先完成 self session、Origin/CSRF 和限流检查，再在有界请求内解析字段并验证**每次**提交的当前密码，包括重放。密码沿用 bcrypt cost 12 与现有统一凭据错误；在外层事务前比较可降低写锁占用，但事务内必须重读并确认同一密码 hash、当前 active/enrolled 员工及未过期/未退出 session，提交前再核一次。登录态失效为 `401 authentication_required`，密码错误为 `401 invalid_credentials`，Origin/CSRF 为 `403 request_rejected`，限流为 429。不得用码有效性、使用次数或商业开关来区分未通过身份/密码的请求。

在有效身份和语法下，未命中、已失效、已停用、用尽、同一员工直属账户已兑换而换新操作 ID、商业执行关闭后的**新**尝试，以及同 ID 不同码/actor/owner/action 指纹冲突，统一 `409 redemption_unavailable`；不得返回“码不存在/已使用/剩余次数”、某员工是否有账户或其他可枚举细节。schema、查询/迭代/关闭、HMAC 密钥不可用、随机数、账本溢出、SQLite busy/快照升级、事务/提交结果不确定、上下文取消均固定 `503 storage_unavailable`，不透传 SQL/触发器、码、密码或摘要。若数据库显现同一操作的单侧或相互矛盾的 expected-action 财务事实，作为损坏/不确定性 503，绝不自动补齐；占用相同全局操作 ID 的不同 actor/action/业务指纹保持统一 409。不能以公开响应或时序刻意做兑换码探测接口。

码只在内存中按原始字节计算既有 purpose 的 keyed digest；以常量时间比较 32 字节摘要，持久层从不新增明文或可恢复密文。HMAC 密钥不是员工输入、也不向上游转发。敏感材料不得进入 URL、日志、trace、错误、审计正文、浏览器持久存储、指标标签或成功响应；服务端可记录脱敏状态码/操作种类和不含秘密的计数。不得关闭 TLS 验证或把员工凭据转发给 provider。

## 一个调用者持有的财务事务

在密码初验后，外层拿当前进程的 `admission.Lock`，用 HTTP 请求上下文派生最长约五秒的可取消 SQLite 写事务，并确保同一事务中对相关行读取、必要开户、码使用 CAS、账本、商业 receipt、兑换事实和提交具有一个原子边界。锁外不写财务事实；SQLite 写者串行化及唯一约束是最后防线。若延迟事务在 WAL 读快照后无法取得写锁，整笔回滚返回 503，不能在陈旧快照上继续，也不能用第二个事务补写。任何步骤失败都回滚；提交报错或断连后的结果未知统一 503，调用者只能以**原 ID、原码**显式重试，不能自动换 ID。结果已提交而响应丢失时，不撤销入账。

事务开始及提交前均重核 session selector/verifier、持久 CSRF、员工 active/enrollment、当前密码 hash 与事务外刚验证的 hash；logout、改密、禁用、过期或切换身份不能穿插成功确认。先按全局 `operation_id` 检查商业 receipt **和**账本 operation 的已提交占用情况，此时不检查码有效期、启用位、使用余量、当前商业开关或现有账户余额。业务指纹使用独立域/version、`redemption.redeem` action、typed employee actor、直属 employee owner 和现有 purpose 的 HMAC 码摘要；不包含 session secret、密码、客户端时间或码明文。商业 `payload_digest` 与账本 v2 actor 摘要各按其格式存储，不能混为一个摘要或退化为仅 ID 相等。

若该 ID 已提交，只有 commercial receipt 的 `redemption.redeem`/typed employee/指纹/资源与 ledger `redemption`/v2 typed employee、唯一正 `redemption` entry、`financial_redemptions` 行、码行及直属 employee 币种账户可从同一事务重构为**同一**码摘要、owner、币种、金额、操作 ID、entry ID 和 `credited_at`，才返回原结果且 `replay=true`；同时验证码与账户/分录的关系，不因码后来过期、停用、用尽或商业开关关闭而拒绝精确重放。不同 actor、码、owner、action 或全局 ID 占用不得借重放跨人领取；缺失或损坏 expected-action 链不能当作“未发生”再兑换。未知提交经重启也遵守同一重放判定。

只有商业和账本两侧均**确无该 ID**时，才在同一事务、用捕获的服务器 UTC 检查 `financial_settings.enabled=1`，按 HMAC digest 查询现有码，确认码 enabled、`uses < max_uses`，且可选 `expires_at` 严格晚于此刻（恰在失效时刻不可新兑）。从码行读取正金额与三位大写币种，不信任客户端。验证该员工在此币种下尚无该码的直属兑换记录；同员工对同一码换新 ID 不再入账。完成所有这些拒绝门禁后才可以同事务创建**必要时**缺失的直属 employee 币种钱包；无效码、过期码、关闭商业开关或已使用码不得留下新账户。不得创建、选择或写入 Key/resource 账户，不能拿其他员工或其他币种账户代替。

同事务生成兑换 ID，以 typed employee actor、全局操作 ID 和直接 owner 通过共享 ledger 转移记**恰好一条**正 `redemption` 分录；账户余额采用有界求和，溢出失败关闭。码 `uses` 以先前读到的次数做有界 compare-and-swap，条件包括仍启用及 `uses < max_uses`，仅成功更新一行才继续；可选到期用已解析的服务器 UTC 核验，不依赖变长 RFC3339 文本的字典序比较。插入一条不可变 redemption 关联行和同 actor、`redemption.redeem` 的不可变 commercial receipt；受现有 `(code_id,account_id)` 唯一约束、全局 operation 主键与事务提交保护。候选账户、次数、分录、operation、redemption 或 receipt 任一失败均全部回滚。并发两个员工争最后一次使用量只允许一个成功；同员工双操作争同一码只允许一个入账；管理员和员工同时兑换继续共享同一库存/唯一约束，不复制相互漂移的财务链。提交明确成功且请求上下文仍可用后才返回 `201`。

## 网页体验、隐私与不确定结果

`/self/` 只在 `features.employee_self_redemption=true` 时展示独立“兑换码充值本人钱包”入口，说明码由管理员发放、按码载明的币种和金额入**本人直属钱包**，并非支付凭证、订阅或模型权益。员工主动输入码和当前密码，明确确认一次；不在浏览器客户端预验码、显示库存或把码写入链接/分享文本。结果仅显示本次币种、金额和入账时刻，不假装返回最新余额；如需余额，另调用既有本人钱包只读接口。桌面及真实 Chrome 390px 检查长输入/错误/重复提交和无水平溢出。

码、密码和待提交 `operation_id` 只在当前组件的短寿命内存中；不写 localStorage/sessionStorage、IndexedDB、service-worker 缓存或 URL，不打印 console。切换员工/session、登出、页面目标、卸载、确定成功或确定 400/401/403/409/429 时清空码/密码/待决结果并中止旧请求。每次请求有 AbortController 与当前身份/session/generation 判定，晚响应不能复活旧码或把其他员工的结果显示在新页面。只有网络断连、超时或 503 等**结果不确定**时，可以在同一员工/session、短时间内仅内存保留原 `operation_id` 与原码供员工**显式**重试；密码立即清空，重试须重新输入当前密码，身份/页面切换立即销毁信封。不得在失败后静默换 ID 重发、后台自动无限重试或持久化待决码；若上下文已丢失，应请管理员通过安全审计核对而不是猜测重新兑换。

## 验收矩阵与发布边界

合同审完后才实现；本阶段**只新增本文件、本地 commit，不 push、不 PR、不 CI/打包/部署**。实现阶段至少补独立自动化测试并按风险完成构建/lint、合成进程和浏览器验证，分别报告已测试与仅规划行为：

- 默认关、只开前置、缺任一前置的普通启动和 `--init` 均检查零持久写；商业执行开关关时新尝试 409、已提交原 ID/原码 200；管理员发码/兑换和旧权限/返回值回归。
- 严格方法/路径/query/未知长度/超长/重复与未知 JSON 字段，匿名、admin Cookie、Bearer、跨员工、禁用/登出/过期/改密、Origin/CSRF、错误当前密码和每次重放密码；限流 429、所有应用响应 no-store、不泄露码或财务内部 ID。
- 未知/过期/停用/用尽码、同员工换 ID 二次使用、码到期边界、错误币种/金额/账户 schema、无效码零开户、有效码自动建本人直属币种钱包；Key/resource 或别人的钱包永不入账，其他币种钱包不被修改。
- 同 ID 精确重放，码后来过期/用尽/停用、商业执行关闭、重启及未知 commit 后精确重放；同 ID 换码/跨 actor/跨 owner/action 冲突，receipt/ledger/redemption/entry/码关联单侧或摘要链损坏失败关闭。断言原币种、金额、`credited_at` 不漂移。
- 多员工最后一次使用量并发、同员工双 ID/同 ID 并发、管理员与员工争同码；注入随机数、schema、查询/Rows 迭代/Close、HMAC、账本溢出、CAS、插入、取消、busy/WAL 快照、commit 失败，核主键集合、账户数、码 `uses`、operation/receipt/redemption/entry 数量和余额全部原子，不只核 HTTP 状态。
- UI 以合成码/密码检查不入 URL、日志、localStorage/sessionStorage/IndexedDB、失败清理、未知结果短寿命显式重试、切身份/卸载的晚响应和桌面/390px；动态选择非 8787 端口，不接真实 provider、支付或员工凭据。

本批仍是默认关闭的单实例开发预览；GOV-02 告知、保留与恢复义务仍是生产启用阻断，多实例库存/账本协调与真实支付亦不在本合同内。桌面配置备份、原子写、校验和回滚仍是未来独立客户端要求，不由本网页入口声称完成。不得借本批使用旧产品 ID、更新频道、生产基础设施或发布包。相邻参考仓库和归档实现不作模板；此前看过参考代码，因此不宣称严格 clean-room。

本合同基于本仓库功能规格、上述已实现结构的只读核对和公开协议资料独立撰写，不复制、翻译或逐行移植 CLIProxyAPI、Sub2API 或归档 CPA 的代码、测试、迁移、资产或文档。2026-10-02 核对的公开资料：[Go `database/sql` 事务与上下文](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go `crypto/hmac.Equal`](https://pkg.go.dev/crypto/hmac#Equal)、[SQLite 事务](https://www.sqlite.org/lang_transaction.html)、[SQLite 隔离与 WAL 快照](https://www.sqlite.org/isolation.html)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。新增域分隔指纹和 HTTP 字段是 CPA Cloud 自有契约，不借用供应商私有协议。现有 Go、modernc SQLite、React 和测试依赖保留各自许可证，现有[依赖许可记录](research/dependency-notices.md)继续适用；本阶段没有新增依赖、SDK 或素材。
