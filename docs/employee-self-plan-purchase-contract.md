# P1 员工自助本人钱包购买 one-time 套餐契约

状态：2026-10-02 独立设计合同，开发预览、默认关闭；本文件先于实现，不宣称功能或验收已完成。精确基线为 `main` 的 `df3094acca541fa36139e50490dce858c8fd0ab9`。本批只为已开通的员工自助会话增加本人钱包购买当前 `one_time` 套餐的报价、确认和网页流程，不增加支付、模型权益、完整会员闭环或生产部署。合同扩展[自助会话基础](employee-self-service-foundation-contract.md)、[本人钱包余额](employee-self-wallet-balance-contract.md)、[自助套餐目录](employee-self-plan-catalog-contract.md)和[员工购买事务原语](employee-purchase-transaction-primitive-contract.md)；目录现价不是可执行报价，P0-B 原语本身不是 HTTP 授权边界。

## 启用与身份边界

新启动开关 `--employee-self-plan-purchase-enabled` / `EmployeeSelfPlanPurchaseEnabled` 默认 `false`。显式开启要求既有 `--employee-self-service-enabled`、`--employee-self-wallet-balance-enabled`、`--employee-self-plan-catalog-enabled` 同时开启；缺任一前置条件须在启动改动存储前失败。它不自动开启这些开关，也不自动开启既有 `financial_settings.enabled` 商业执行开关；订阅状态只读开关不是前置条件。关闭时两个新 POST 路由均为 404，`features.employee_self_plan_purchase=false`，网页完全不显示购买入口且不发报价/购买请求。自助总开关关闭时 `/self/` 继续整体 404。

商业执行开关是可变的**新购**门禁：关闭时目录显示不可用，网页不展示新购确认/报价按钮，报价请求被拒且无财务写入。购买 POST 路由在本功能 opt-in 开启时仍须能处理已提交的精确同 ID 重放；不能因商业开关随后关闭而在路由或中间件层提前拦截这种重放。

两条接口只接受当前有效、已开通且 `active` 员工的独立 `cpa_self_session`。管理员 Cookie、员工模型 Bearer Key、匿名请求、其他员工的会话都不能代替。员工 ID 只来自已认证会话，不接受请求中任何 employee/owner/account/Key/resource 选择器。每次 POST 必须有且仅有一个同源 `Origin`、`X-Self-Request: 1` 和与当前会话匹配的 `X-CSRF-Token`；无效、歧义、跨源请求在读取或写入财务事实前拒绝。沿用自助 Cookie 的 `HttpOnly`、`SameSite=Strict`、`Path=/self/` 和 TLS 下 `Secure` 边界；全部响应 `Cache-Control: no-store`，不允许跨权限域复用会话。

## 第一步：冻结报价

`POST /self/api/v1/billing/plan-purchase-quotes` 没有 query 参数，JSON 请求体**恰好**为 `{"plan_id":"opaque-plan-id"}`。请求体最多 4096 字节，要求合法 UTF-8、单一 JSON 对象、唯一且非空的 `plan_id` 字符串；缺失、重复、未知字段、尾随 JSON、数组、非法类型、过长 ID、查询参数或选择器均为固定 `400 invalid_request`。计划 ID 使用现有财务层的 1–256 字节不透明 ID 规则；不接收金额、币种、revision、员工、账户、operation ID、支付资料或客户端时间。

服务端以不超过五秒的同一只读 SQLite 事务，重核当前会话/员工与本功能能力，验证财务 schema/商业单例，要求商业执行已开启、选中的套餐**当前**存在且启用、`interval="one_time"`、revision/币种/正整数微单位价格与额度及时间字段均有效，并要求该员工有同币种、直属 employee owner 的**既有**钱包。Key/resource 子账户、他人钱包和不同币种不能代替；无账户不能自动创建。报价只验证钱包存在，不预留余额也不承诺最终足额。读、迭代、关闭和提交任一步失败都不能返回部分报价；报价生成、随机数或 AEAD 故障同样固定 503。事务成功且上下文仍有效后才生成并发出报价；此 POST 不写任何财务事实、DDL 或持久报价行。

`201` 响应仅含 `quote_token`、`plan_id`、`revision`、`currency`、`interval`、`price_micro`、`credit_micro`、`expires_at`。价格/额度是规范的正十进制微单位**字符串**，币种为三位大写 ASCII，周期固定 `one_time`，失效时间是规范 UTC RFC3339Nano；不返回账户 ID、余额、其他员工、密码、摘要、分录或管理配置。报价内服务器冻结同一 plan ID、revision、currency、price、credit、interval，以及员工 ID、当前 self-session selector、独立 purpose/version、UTC 签发时间和恰好五分钟的失效时间。使用安装现有持久 AEAD 密钥、每次新取的密码学随机 nonce 和独立 AAD `cpacloud/employee-self-plan-purchase-quote/v1`；它不能与目录/钱包游标或其他令牌互换。token 为规范 URL-safe 编码且不超过 2048 字节，不在服务端持久化；篡改、截断、非规范编码、未知字段/version/purpose、错误安装密钥或跨员工/跨会话均固定拒绝，不回退到客户端字段。它不是一次性券：同报价配不同 operation ID 是受当前门禁与余额重新约束的不同新购。只用服务器 UTC 时间判断：新购在 `issued_at <= now < expires_at` 时有效，恰好到期即失效；不存在续期、宽限或客户端时钟授权。

## 第二步：密码确认和同事务购买

`POST /self/api/v1/billing/subscriptions` 无 query 参数，请求体最多 4096 字节，JSON **恰好**有三个字符串：`operation_id`、`quote_token`、`current_password`。`operation_id` 遵守 P0-B 的 1–128 字节非空不透明 ID 规则，浏览器每次新购生成新的不可预测 ID；同 ID 用于不确定提交后的精确重试。密码遵守既有 12–72 UTF-8 字节规则。所有缺失、重复、额外字段、尾随内容、非法类型、超限/非法 UTF-8 或传入 plan、金额、币种、员工、账户、Key、resource 等选择器均为 `400 invalid_request`，不悄悄忽略。没有管理员代买或模型 Key 购买的别名入口。

接受请求的顺序必须满足以下可测试不变量：

1. 先执行自助会话、Origin、custom header 和 CSRF 初验，严格解析身体，并**只对 token 做结构、AEAD 认证、purpose、安装/员工/当前 session 绑定检查**；此阶段不把报价年龄、当前商业开关、当前套餐或当前余额当成已提交重放的门禁。token 的五分钟字段必须结构自洽，但时间是否过期留到“新购”分支。
2. 每次提交（**包括精确重放**）都使用当前输入的 `current_password` 与该员工既有 bcrypt hash 验证，沿用按 peer 与员工的有界失败限流和统一脱敏凭据错误。耗时 bcrypt 可以在事务外执行，但不得因此信任旧会话、旧 CSRF 或旧密码行。校验期间不持有跨整个服务的写 admission 锁。
3. bcrypt 后才进入最终临界区：取得同进程的 admission 写锁，开启有界、可取消、由 HTTP 外层持有的 `*sql.Tx`；**在这同一事务内**重新验证 self cookie 的 selector/verifier、未过期且未退出的 session、与请求 header 相同的持久 CSRF、员工仍 `active` 且已开通、直属员工归属，以及密码 hash 与刚验证的 hash 完全一致。在任何成功提交前再于同一事务检查这些条件和当时的 session 失效时间；过期或变化则回滚。任何一次退出、停用、改密、过期或会话切换都使购买和重放失败，不能因初验或 bcrypt 完成而绕过。锁持续到该事务提交/回滚，避免本进程内登出、停用或改密在最终核验与结果确认间穿插；SQLite 忙、快照升级或其他写入竞争须回滚为固定 503，不在旧快照上继续。
4. 在最终授权通过后，**先**用与 P0-B 相同的员工指纹和原双 receipt 验证，在该事务内做只读的全局 operation ID 探测。可为 P0-B 增加内部只读 replay probe，但不得复制出第二套不一致的摘要/receipt 判定。若已提交且 actor、直属 owner、完整认证报价快照与 operation ID 精确一致，返回原订阅及原 receipt 的安全投影，不读当前报价 TTL、商业开关、套餐价格/启停或当前余额，也不追加财务行；同 ID 的任何不同 actor/报价/快照、旧管理员或损坏/单侧 receipt 必须冲突或失败关闭，绝不“补齐”。**过期报价仍可用于这样的精确已提交重放。**
5. 只有探测确认没有已提交操作，才以**此事务内的服务器时间**检查报价未过期且未来自未来，再调用已合并的 `PurchaseEmployeeSubscriptionTx`。该原语在同一事务重新验证商业执行、当前套餐启用及完整 plan ID/revision/币种/价格/额度/interval、直属既有钱包与足额余额，并原子准备一条 `one_time` 订阅、同 operation ID 的员工 typed 商业/v2 账本两份 operation/receipt，以及恰好两条同钱包的扣价和额度分录。探测与原语之间不能换连接/事务；原语保留自己的重放/冲突检查作为防御。不能自动开户、使用 Key/resource 钱包、让金额由请求覆盖，或以目录旧快照代替报价。
6. 外层在任一错误、取消或持久化失败时回滚，所有财务表和账户主键集合均无部分变化；只在 `Commit` 明确成功且响应上下文仍可用后发送成功头/字节。提交失败或结果不确定固定 `503 storage_unavailable`，不自动换 ID 或二次扣款；若事实上已提交，随后携原 operation ID、原认证 token 和**重新输入的当前密码**可精确重放。若事实上未提交而报价已过期，新购拒绝，用户须另取报价/新 ID；不把未知结果伪报成功。提交后连接中断不能删不可变事实。

首次提交返回 `201`，精确重放返回 `200`，两者仅返回 `operation_id`、`subscription_id`、`replay` 及原冻结 `plan_id`、`revision`、`currency`、`interval`、`price_micro`、`credit_micro`；无账户/分录/摘要/密码/token 明文。业务不可购或报价过期为固定脱敏 409，错误 operation ID/输入报价冲突为固定 409，凭据失败为固定 401，Origin/CSRF 为 403，存储/schema/迭代/关闭/提交不确定/上下文故障为固定 503 且**无部分成功响应**。错误文案不泄露其他员工、账户、原摘要、SQL、价格历史或内部异常。业务错误不提交事务。

幂等只绑定**已经提交**的同 operation ID 和完整员工报价指纹；两个不同 ID 是两笔独立新购，都重新受当前钱包余额和商业/套餐门禁约束。SQLite 单写者、同事务余额及 `RequireNonNegative` 防止两个不同 ID 用同一旧余额双重透支；若余额足够，两笔可以合法成功，本批不暗示“每员工每套餐仅一次”或跨实例防重复。第一笔回滚后，同 ID 不占位。无跨实例协调承诺。

## 网页、安全数据与边界

`/self/` 只在 `features.employee_self_plan_purchase=true` 显示本人购买区域；目录、钱包余额都由员工明确操作才读，不在 mount/login 自动读取。仅从当前目录明确选一个 `one_time` plan ID 请求新报价；页面必须把**报价冻结**的币种、价格、额度、五分钟失效和“从本人直属钱包扣款”显著显示，并要求员工主动确认及在 native `type=password` 字段输入**当前**密码。目录值不直接充当报价，monthly 只可浏览不可买；商业执行关闭/目录不可用时不显示新购确认按钮。提交后只展示自身结果，不推断模型权益或供应商/支付状态。

报价、密码、op ID 和待决重试 token 只在组件内存，不写 `localStorage`、`sessionStorage`、页面 URL、service-worker 缓存或日志。切换币种、账号/session、登出、卸载或任何失败时，清空当前可购买报价展示、钱包金额和密码；AbortController 加会话/币种/请求 generation 校验阻止晚响应回填。对**提交结果不确定**这一窄情形，UI 可暂留仅内存的原 operation ID + 原 token 作为独立、不可直接提交的待决重试信封：不回填旧报价，必须重新输入密码并显式重试；一旦切账号/币种/登出/取消即销毁。其他失败销毁信封并要求新报价。成功或确定冲突后销毁密码与 token。桌面及 390px 真 Chrome 检查布局、确认文案、密码清理、晚响应和能力门控。

本批不新增 DDL、第三方依赖、真实支付/provider、退款/税票、订阅权益、管理员定价 UI 或跨实例结算。新持久事实仅为既有财务表中的本人购买；报价令牌不入库。现有 WAL/备份保留期仍适用，GOV-02 的员工告知、访问、保留和恢复决策未完成，生产启用前须单独处理。任何新代码须注明本合同及实际使用的公开资料来源；不复制/翻译/移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考仓库。此前已看过参考代码，因此不称严格 clean-room。

## 验收与发布边界

合同先独立提交并核相对链接、`git diff --check`、精确 HEAD/tree/merge-base；主任务审阅通过后才实现。实现测试至少覆盖默认关闭和不满足前置 flag 的启动失败；匿名/admin/Bearer/禁用/未开通/登出/过期/跨员工/跨 session、Origin/CSRF 与最终事务内重核；报价伪造/篡改/目的串用/错误安装/非法时间、五分钟临界点、套餐改价/停用/monthly、商业后关、本人缺钱包/低余额/Key-resource 余额不代付；每次密码错误、限流、bcrypt 与登出/改密/停用竞争；新购及**过期报价下精确重放先于新购门禁**、同 ID 改报价/actor 冲突、同/不同 ID 并发、提交前取消及结果不确定、重启恢复；坏 schema/行/双 receipt、读迭代关闭和提交故障、账户主键集合及行数零部分。测试须核财务事实而非只核 HTTP 状态，旧管理员购买/续订/支付回调与历史 v1/v2 重放不得退化。

分别报告 Go 专项/race/vet、Web 测试/typecheck/build、动态随机非 8787 进程与真 Chrome 桌面/390px，以及精确 HEAD GitHub 的非缓存全 Go、CGO service race 分片、有状态包 race、双 CLI build、Web 和隔离冒烟结果；原 P0-B/PR #45 证据**不**替代本批。独立 draft PR 在同 HEAD 固定实体 Git 源码与二进制双随机根黑盒验收和 CI 全通过之前不得 ready；不得使用真实 8787、部署、tag、package 或 native 工作流。若现有锁/事务或 P0-B replay 接口无法维持以上顺序与同事务不变量，先报告具体阻断，不放宽门禁或扩批。

## 公开技术依据与许可

2026-10-02 核对：[Go `database/sql` 事务与取消](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go AEAD `Seal`/`Open`](https://pkg.go.dev/crypto/cipher#AEAD)、[Go bcrypt 验证](https://pkg.go.dev/golang.org/x/crypto/bcrypt)、[SQLite 单写者与 WAL 快照隔离](https://www.sqlite.org/isolation.html)。报价和金额是 CPA Cloud 内部协议，不调用第三方会员或支付协议。现有 Go、modernc SQLite、React 与测试依赖保留各自已有许可证；本批合同不引入第三方 SDK、依赖或素材。
