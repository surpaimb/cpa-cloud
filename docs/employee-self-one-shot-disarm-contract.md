# ID-05 / BILL-03 员工自助查看并撤销本人一次性续购预约契约

状态：2026-10-02 独立设计合同，开发预览、默认关闭；本文件先于实现，不宣称功能或验收已完成。精确基线是已合并 `main` 的 `a24b4df749853fa781c7f7f28bd4995e8a6b46a3`。本批只给已开通且仍 active 的员工按需查看、撤销其直属钱包名下既有 monthly 订阅的**一次性**续购预约。它扩展[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[一次性续购预约](subscription-one-shot-renewal-contract.md)、[冻结月周期与到期](subscription-period-expiry-contract.md)、[本人取消订阅](employee-self-subscription-cancel-contract.md)和[财务 actor 来源](financial-actor-provenance-contract.md)。管理员仍是唯一预约者；本批不赋予员工预约、授权新扣款或购买的能力。GOV-02 仍开放。

## 独立 opt-in 与兼容性

新启动开关 `--employee-self-one-shot-renewal-disarm-enabled` / `EmployeeSelfOneShotRenewalDisarmEnabled` 默认 `false`，自助 session capability 为 `features.employee_self_one_shot_renewal_disarm`。显式开启必须同时开启 `--employee-self-service-enabled` 与 `--employee-self-subscription-status-enabled`；缺一在更改持久状态前启动失败。它不要求本人余额、钱包变动、套餐目录、本人购买、本人取消订阅或数据库 `financial_settings.enabled`；商业执行关闭时仍可读预约、撤销仍 `armed` 的预约并精确重放。新开关关闭时两条新路由均为 404、capability 为 false，旧订阅列表字段和 admin/worker 路径保持原义。只启用前置能力不会自动启用本批。自助总开关关闭时 `/self/` 仍整体 404。

本批优先不新增表、列、索引、迁移或第三方依赖。现存 `financial_subscription_one_shot_renewals.armed_by_admin_id` 是 `NOT NULL` 且受不可变触发器保护：员工撤销**只**写现有终态列，不将员工 ID 填入、覆盖或伪装为管理员 ID。现存 typed `financial_commercial_operations` 已有 `actor_kind='employee'`/`actor_employee_id` 与 `subscription.one_shot.disarm` action；新员工写入使用这组字段，原 `arm` receipt 仍只允许真实 admin actor。现有 admin disarm、worker、审计和旧行必须继续可读可验。当前只按 `actor_admin_id` 验证 disarm receipt 的代码须有界扩展为完整 typed actor 验证，不能顺便放宽 arm 或 system actor；员工 disarm 的 actor ID 还必须与前驱订阅的直属钱包 owner 一致。若实现时发现这些不变量无法在现有精确 schema 下保持，先报告具体 DDL/冲突阻断，不自行扩展范围。

## 按需 GET：单条预约的最小投影

`GET /self/api/v1/billing/subscriptions/{id}/one-shot-renewal` 只接受一个财务规则内的规范编码、不透明订阅 ID 路径段（1–256 UTF-8 字节），不接受 query（包括空 `?`）、请求体、额外路径段或歧义转义。已验证的 self 会话是唯一员工身份来源；管理员 cookie、模型 Bearer Key、匿名、未开通/停用员工均不可替代。读请求沿用 self 的可选同源 `Origin` 约束；错误和成功均 `Cache-Control: no-store`。不存在、非本人直属钱包、Key/资源账户、其他员工或非 monthly 目标统一 `404 subscription_not_found`，不透露目标类别；非法路径、query/body 固定 `400 invalid_request`。

成功 `200` JSON **恰好**含 `subscription_id`、`state`、`revision`、`due_at`、`reason`、`terminal_at` 六字段。`state` 为现有 `none|armed|disarmed|succeeded|failed|superseded|cancelled`；`none` 固定 `revision:0`，其 `due_at`、`reason`、`terminal_at` 均为 `null`。已有预约的 `revision` 是预约行的**存储版本**（当前 `armed:1`、终态 `2`），不是订阅 `revision` 或到期投影版本；`due_at` 为预约冻结的规范 UTC 时刻。`armed` 的 `reason`/`terminal_at` 为 `null`；终态的 `terminal_at` 是规范 UTC 时间，`reason` 使用现有固定代码（`disarmed`、`manual_renewal`、`predecessor_cancelled` 或现有失败原因），`succeeded` 的 `reason` 为 `null`。不得返回 successor、arm/disarm/execution operation、管理员/员工/账户/Key/资源 ID、plan、价格、额度、余额、账本或支付资料。`succeeded` 仅表示本地续购事务已提交，不是外部支付或模型权益保证；`armed` 不保证到期扣费成功。

每次 GET 是有界的单一只读 SQLite 快照：在该快照内核当前有效会话/员工、直属既有钱包账户、monthly 前驱订阅、现有预约/receipt/续购链和精确 schema、列类型、UTC 时间及外键；不得创建账户、改变状态或执行账本/上游调用。已有预约即使前驱后来 `expired`/`cancelled` 或产生 successor，仍可只读显示其真实终态；`none` 不虚构将来预约。相关行、孤儿 receipt、迭代/close/commit、取消或存储故障在首个成功字节前固定 `503 storage_unavailable`，不回传半个投影。GET 不要求商业执行开关开启，且不会触发自动刷新或 worker 执行。

## POST：严格输入、身份与响应

`POST /self/api/v1/billing/subscriptions/{id}/one-shot-renewal/disarm` 使用同样的规范路径，不接受任何 query。请求体不超过既有 self 上限 4096 字节：合法 UTF-8 的单个 JSON 对象，**恰好**包含 `operation_id`（1–128 UTF-8 字节的全局不透明 ID）、`expected_revision`（规范 JSON 整数，`1..9007199254740991`，指预约行存储版本）和 `current_password`（字符串）。缺失、重复、未知字段，尾随 JSON、数组、非法 UTF-8、非规范数字、错误类型或员工/owner/Key/账户/金额/时间选择器固定 `400 invalid_request`；缺失密码或密码不是字符串也是 400。密码字段存在且为字符串但不在既有 12–72 UTF-8 字节范围、内容错误或密码已变，固定脱敏 `401 invalid_credentials`，相应失败可达 `429`。新撤销只有 `armed` 的 stored revision `1 -> 2`；合法但过期的 `expected_revision` 返回 409，不把订阅的 revision 当预约版本。

每次请求，**包括精确重放**，必须重新通过当前有效、已开通且 active 的 self 会话，以及仅一个匹配的同源 `Origin`、`X-Self-Request: 1`、当前会话 `X-CSRF-Token`、当前密码和 peer/employee 双维失败限流；管理员 cookie、员工模型 Key、旧 receipt 或历史 `armed_by_admin_id` 都不能代替这些检查。沿用 self cookie 的 `HttpOnly`/`SameSite=Strict`/`Path=/self/` 与 TLS 下 `Secure`。身份只从已验证会话构造 `(actor_kind='employee', actor_employee_id=...)`，不从请求、钱包、预约者或 receipt 推断。Origin/CSRF 按既有固定 403 码，非本人目标 404，已终态、无预约、版本不符或竞争败者 `409 disarm_unavailable`；结构、取消、超时、忙和不确定提交 `503 storage_unavailable`，都不暴露内部状态/SQL/他人存在性。

首次提交与精确重放均返回 `200`，JSON **恰好**含 `operation_id`、`subscription_id`、`replay`、`state:'disarmed'`、`revision:2`、冻结 `due_at`、固定 `reason:'disarmed'` 和规范 UTC `terminal_at`。响应不含 receipt 对象、actor、余额、successor、plan、账本、授权素材或预约执行 ID。浏览器每次全新确认生成不可预测 `operation_id`；结果不确定时只允许用原路径、原预约 `expected_revision` 和原 ID，在重新认证当前密码后显式重试。

## Caller-owned 事务、精确重放与原子性

严格解析/会话初验后可在事务外读取 bcrypt hash 并验证输入密码，但 bcrypt 不持有跨服务写 admission 锁。最终阶段持本进程 admission 写锁，开启有界可取消、由 HTTP 调用方持有并提交的 `*sql.Tx`；不能把现有自行 `Begin/Commit` 的管理员 disarm 方法当成员工授权路径。事务内重验 self schema、cookie selector/常量时间 verifier、持久 CSRF、会话未登出/过期、员工仍 active 且已开通、当前密码 hash 与刚验过的字节一致；提交前在**同一事务**再次重验这些可变授权条件、目标直属 owner 与预约关联。改密、登出、停用、CSRF 变化、owner/关联异常、取消或外部写竞争使新操作和重放都失败关闭并回滚，绝不使用旧快照越权。该锁保持到 commit/rollback。

完成当前身份/密码校验后，在同一事务**先于预约当前状态、版本、到期及商业执行门禁**探测全局 `operation_id`。action 固定 `subscription.one_shot.disarm`，typed actor 为上述员工，服务端规范 payload 指纹绑定 `{predecessor_id:路径 ID, expected_revision:客户端确认的预约存储版本}`，与现有 admin disarm 的路径/版本指纹语义兼容；action 和 actor 另行精确比较。密码、CSRF、会话、观察时间与当前商业开关不进入摘要，但每次重放仍重新检查它们。同 ID 属于不同 actor/action/目标/版本/指纹，或只占用账本 ID，固定 409；不得把 admin/system/历史 unknown 操作提升成员工重放。匹配元数据后仍须读取并严格验证原 monthly 前驱、直属 owner、规范 arm/admin receipt、预约 `state='disarmed'`/revision `2`/`reason='disarmed'`、相同 `disarm_operation_id`、商业 receipt 的 `resource_kind='subscription_one_shot'`/目标/revision `2`、完整 typed employee actor、规范时间与 `terminal_at=updated_at=receipt.created_at`、无 successor/多余账本操作、预约/续购链和 FK。**元数据匹配但这些链缺失或损坏为 503**，绝不补写或伪造成功。精确已提交重放返回原结果，不因预约已终态、前驱后来到期/取消或商业执行关闭而变 409；但仍要求当前本人直属 owner、会话与新输入密码有效。

只有全局 ID 未占用的新请求才在同一事务读取目标：必须是既有 monthly 订阅，账户是当前员工直属、规范的 employee 钱包 owner（非 Key/resource、他人或畸形 owner），预约必须已有且 `state='armed'`、stored `revision=expected_revision=1`，arm receipt 必须保留真实 admin actor，冻结 due/原始订阅/续购链严格有效。不能因只读到期投影为 `expired` 就声称已续购，也不能仅凭到期时刻禁止撤销：若 worker 尚未提交预约终态/续购，`armed` 仍可被撤销；已提交的 worker 续购绝不可逆转。使用单一事务 UTC 业务时刻，按 `predecessor_id/state/revision` CAS 恰好一行到 `disarmed`、revision `2`、固定 reason、`terminal_at=updated_at`；同事务插入唯一 `subscription.one_shot.disarm` employee-actor 商业 receipt 并绑定 `disarm_operation_id`。预约身份列、`armed_by_admin_id`、冻结 due、订阅行、账户、钱包分录和余额均不改。插入/CAS 顺序须满足现有 FK，任一步失败全部回滚；提交错误或结果未知固定 503，后续同 ID/路径/版本加**当前**密码的请求按精确重放恢复，而不是另写一次。

管理员 disarm、one-shot worker、手动续购、本人/管理员取消订阅、到期 worker 和两个员工请求均需沿既有 SQLite 单写者与同事务 CAS/唯一约束重新判定：对同一预约的终态或 successor/金钱事实以先提交者为准，败者不产生第二 receipt 或扣费。普通到期 worker 单独提交订阅 `active -> expired` 并不等于预约执行；若预约仍 `armed`，仍按上述规则容许 disarm 竞争。worker 已成功时员工只能看见 `succeeded`，不能收到撤销成功；管理员/员工取消先使预约 `cancelled` 时 disarm 为 409；手动续购先使其 `superseded` 时亦然。终态不重新 arm，不撤回既有 successor、既有 `subscription_credit`，不退款，不产生员工新扣费授权、账本操作或分录。坏 schema/行/时间、迭代/close/commit/请求取消及持久化故障必须固定 503 且零部分；失败前后账户、订阅、续购链接、金额分录及无关预约不变。

## 浏览器、验收与交付边界

`/self/` 只有新 capability 与订阅状态 capability 同时为真时，才在员工**明确读取**的本人 monthly 订阅卡显示“查看续购预约”；不得在 mount、登录或仅加载订阅列表时自动 GET 预约。`none`、`armed` 与各终态须清楚区别；仅真实 `armed` 显示两步撤销：先展示本订阅 ID、预约 stored revision、冻结 due，并明确“仅撤销这一笔一次性预约；到期扣费可能失败；不取消当前订阅、不退款；同前驱不可再次预约”，再要求独立确认与 native `type=password` 当前密码。不能把它画成持续自动续费开关，不能从 GET 接收 successor/admin/operation/账户/价格等字段。

密码只在表单和请求内存中短暂存在，提交后即清空；操作 ID 与待决重试信封只在组件内存，不进入 URL、Web Storage、缓存或日志。切换员工/会话、登出、卸载、目标或预约版本变化、已知失败清空预约视图、确认与密码；AbortController 加代际校验阻止旧 GET/POST 晚响应回填。网络断线或不确定 503 的窄情形可以只保留原路径/版本/operation ID 待决信封，不自动重试、不宣称已撤销；员工须重新确认并输入当前密码后显式同 ID 重试。明确成功后清空待决信封与旧预约视图，重新 GET 服务端权威状态；刷新失败时保留已确认操作结果但不展示陈旧 `armed`。桌面与真实 Chrome 390 px 均检查能力门控、确认、失败、待决、成功、切账号及晚响应，无横向溢出。

合同阶段**只独立提交本文件**，核全部相对链接、`git diff --check`、精确 HEAD/tree/status/merge-base 并待主任务全文只读审；审完之前不实施、push 或建 PR。实现阶段新增独立 Go/HTTP/Web 测试，至少覆盖默认关与前置校验、商业关仍可 GET/POST/replay、六字段 GET/无 body-query/`none`/各终态、直属 owner 与 Key/resource/他人/one-time 隔离、匿名/admin/Bearer/未开通/停用、Origin/CSRF/密码/peer+employee 限流、bcrypt 与改密/登出竞态；admin arm 保持 admin-only、员工 receipt typed 与旧 admin disarm 兼容、同/异 ID/actor/action/目标/版本/账本冲突、精确重放优先、坏 receipt/预约/owner/schema、读写/迭代/close/commit/取消失败、未知提交/重启恢复；worker/admin/手动续购/取消/expiry 和双员工并发的一条终态/零双扣；UI 的两步/秘密清理/晚响应和桌面/390 px。专项 Go/vet、Web typecheck/test/build、动态非 8787 进程和 Chrome，与本批**精确 HEAD** GitHub 非缓存全 Go、四互斥实际 CGO 完整 service race、11 stateful race、双 CLI、Web、隔离 smoke 分别报告。独立 draft PR 经同 HEAD 固定 Git 源码/二进制双随机根黑盒和 CI 核验后才可 ready；旧 PR/CI/G 证据不得冒用。原生打包、tag、发布、部署、生产端口、真实供应商或支付调用均不在本批。

本合同及后续新增源码、测试由 CPA Cloud 基于上述自有需求与公开技术资料独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树；此前看过参考资料，因此不称严格 clean-room。2026-10-02 查阅的公开依据：[Go `database/sql` `BeginTx`/`Commit` 与取消语义](https://pkg.go.dev/database/sql#DB.BeginTx)、[Go HTTP request context](https://pkg.go.dev/net/http#Request.Context)、[SQLite 事务和单写者](https://www.sqlite.org/lang_transaction.html)、[SQLite 条件 `UPDATE`](https://www.sqlite.org/lang_update.html)。本批不新增第三方 SDK、依赖、素材或计划中的 DDL；现有 Go、modernc SQLite、React 与测试依赖保留原许可证。新增源码须明确标注本合同及实际使用的公开技术来源。
