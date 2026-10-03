# ID-05 / BILL-04 员工本人直属钱包管理员本地调整记录契约

状态（2026-10-03 本地预推送审核记录）：**本分支已有本地实施与下述验证；在该审核时点，本批实施差异尚待只读审核、尚未提交或 push，亦未建 PR、触发本批 GitHub CI 或部署。后续状态须以实际 PR、CI 和精确提交 HEAD 的记录为准。**契约基线是已合并的 `main` `f21de90812c6a785ca88a1b0f82ba7b8a3c47e77`。本合同从本项目的[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览契约](preview-contract.md)、[单实例财务契约](single-instance-billing-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[钱包余额](employee-self-wallet-balance-contract.md)、[最近钱包变动](employee-self-wallet-activity-contract.md)、[原始分录分类](employee-self-wallet-entry-classification-contract.md)及[财务 actor 来源](financial-actor-provenance-contract.md)制定。本批只增加一个员工显式读取、默认关闭的本地事实视图；不提供调整原因、付款证明、充值、退款、工资、补偿、账单或外部资金来源判断。

## 范围与能力开关

新增 CLI `--employee-self-admin-adjustment-history-enabled`、配置 `EmployeeSelfAdminAdjustmentHistoryEnabled` 和能力位 `features.employee_self_admin_adjustment_history`，默认均为 `false`。显式开启必须同时开启现有 `--employee-self-service-enabled`、`--employee-self-wallet-balance-enabled`、`--employee-self-wallet-activity-enabled`、`--employee-self-wallet-entry-classification-enabled` 四项；普通启动及 `--init` 必须在数据库、密钥、目录或其他持久写入前拒绝任何缺项，不得自动开启前置。四项前置开启本身不会开启本位。本位关闭时能力位为 false，新路由 404、网页无入口。

本位**不**依赖员工兑换、商业执行、当前 `financial_settings.enabled` 或管理员调整的当前可写状态；这些开关关闭后，以往已提交且证明链有效的本地调整仍可读。既有钱包余额、活动、分类、兑换入账历史的路由、JSON、游标 purpose、授权及能力语义不变；本视图绝不复用旧游标，也不使旧接口额外暴露 actor。没有 DDL、财务写入、后台 worker、provider/payment/权益行为或余额重算。

唯一显示对象是：当前 active、已开通自助的员工本人**直接拥有**的指定币种钱包中，现有管理员调整入口已提交的账本操作，其 `action='adjustment'` 且 `actor_kind='admin'`，并能独立验证为同一操作的一条本地正或负分录。`adjustment_credit`/`adjustment_debit` 的种类单独不足以说明来源：商业退款也可能使用 `adjustment_debit`；`legacy_unknown`、employee/system actor、其他 action、Key/resource 子账户和其他员工均不纳入。负值只表示本地钱包减少，正值只表示增加；不能据此推断为何调整或资金从何而来。

## 精确 GET、身份与最小响应

唯一新路由是 `GET /self/api/v1/billing/admin-adjustments?currency=USD[&limit=20][&cursor=...]`。大小写、斜杠和原始字面路径必须精确；无尾斜杠、附加段、转义路径、反斜线、双重转义或路径清理后的等价物。形似路径须在 `ServeMux`/WebDir 规范化、重定向前拒绝，不得以 301/307/308 绕过或回显 query。本位关闭优先 404；本位开启时，只有精确路径的非 GET 返回 405 与 `Allow: GET`。只认已开通、active 员工的有效 self Cookie session；匿名、管理员 Cookie、模型 Bearer Key、别人的员工身份均不可代替。可选 `Origin` 出现时必须同源，所有应用响应 `Cache-Control: no-store`。GET 不要求密码、CSRF 或 `X-Self-Request`，不得调用管理员授权接口。

原始 query 总长最多 2048 字节，仅允许字面 `currency`、`limit`、`cursor`：`currency` 恰一次，值为三位大写 ASCII `[A-Z]{3}`；首页缺省 `limit=20`，显式 limit 是 1–50 的规范十进制（无正负号、前导零或空值）；cursor 仅用于续页，至多 1024 字节且为规范 base64url，续页必须显式重复首页 limit。拒绝未知/重复参数、裸 `?`、编码后的参数名或值别名、空字段、不规范百分号编码和超限值；任何请求正文，包括 chunked 或未知长度，都固定为 `400 invalid_request`。不允许 employee/account/key/resource/actor、operation/entry、管理员或原因选择器，不提供批量或币种发现。

成功 JSON **恰好六个顶层字段**，每条 item **恰好两个字段**，示例只展示格式而非已实现响应：

```json
{
  "currency": "USD",
  "has_account": true,
  "window_start": "2026-09-02T12:00:00Z",
  "window_end": "2026-10-03T12:00:00Z",
  "items": [
    {"occurred_at": "2026-10-03T11:59:59Z", "delta_micro": "42"},
    {"occurred_at": "2026-10-03T11:59:58Z", "delta_micro": "-7"}
  ],
  "next_cursor": null
}
```

`delta_micro` 为经证明链验证、非零的有符号 `int64` 微单位，输出为规范十进制字符串；`occurred_at` 为一致的规范 UTC `RFC3339Nano` 本地入账时刻。没有直属该币钱包时 `has_account:false`、空 items、null cursor；有钱包而窗口内没有合格操作时 `has_account:true`、空 items、null cursor。两者都不代表余额为零。响应、网页和错误均不得输出或记录管理员/员工身份、account/operation/entry/resource ID、摘要、调整原因、余额、凭据或外部交易资料；加密游标中的位置不是公开明文字段。

## 窗口、页序与独立游标

首页收到请求时冻结 UTC `31 × 24h` 窗口：`window_end` 是下一整秒、排他，`window_start = window_end - 31 天`、包含。续页复用两端；每页各自为只读数据库快照，不保证并发写入时跨页完整枚举。合格候选按不可变 `(financial_entries.created_at TEXT, financial_entries.id TEXT)` 的 SQLite binary stored-key 降序；续页严格小于上次最后返回的位置，同存储时间靠 ID 稳定排序。混合精度 `RFC3339Nano` 的字典序在同秒内不保证纳秒时间顺序，本产品只承诺存储键稳定顺序；整秒窗口边界须保留带小数时间，不可因字符串比较漏掉窗口边界秒。

每页最多返回 limit 条，从**合格候选**取至多 `limit+1` 条以决定 `next_cursor`，选中与 lookahead 均做同等证明链验证。不得遇坏行后跳过、补页或输出部分数据；同一操作多分录是损坏而不是两条可显示调整。新游标使用已有安装密钥、每次新随机 nonce 的 AEAD 和独立 purpose/version `cpacloud/self-admin-adjustment-history-cursor/v1`。认证内容仅为安装、当前员工 ID、当前 self-session selector、币种、limit、冻结窗口及最后已返回的原始存储时间/ID；不含金额、管理员身份或余额。不持久化游标到数据库或浏览器存储。

在账本读取前验证编码、AEAD、purpose/version、安装、员工/session、币种、limit、窗口、位置合法性及从原始 `window_end` 起不超过 15 分钟。篡改、截断、旧接口游标、跨安装/员工/session/币种/页大小、过期或非法位置统一 `400 invalid_request`，不记录密文或解密原因。随机数/加密失败固定 `503 storage_unavailable`，成功头与正文必须在游标生成完成后输出。

## 单事务证明链与失败关闭

调用方以请求 context 派生最长 5 秒，持有**一个只读 SQLite 事务**完成本页。先核既有财务账户、操作、分录的精确 schema、索引、声明 FK 与不可变 trigger（包括新写入禁止 `legacy_unknown` 的 trigger）；不只按表名存在判断，也不为 GET 做迁移/修复/DDL。按当前 self session 员工、`owner_kind='employee'` 和显式币种查最多两条账户。唯一行仍须核存储类型、规范 owner key、员工 ID、`key_id IS NULL`、空 resource、币种与规范时间；重复/畸形账户整页 503，Key/resource 或异员工账户绝不聚合。无账户是正常空结果，不扫描别人的账户。

从该直属账户、冻结窗口和位置范围查询分录，联接/核对其唯一账本操作，以**`action='adjustment' AND actor_kind='admin'`**作为候选来源边界；不能仅凭分录 kind、正负号、`resource_kind` 或管理员页面文案分类。候选查询不得先用正负金额、分录 kind、摘要版本或 resource 过滤掉范围内的损坏调整；排除其他 action/actor 是产品边界，而不是对它们的有效性背书。选中和 lookahead 的每个候选都须在同一事务满足：

1. 分录 ID、operation/account ID、币种、`created_at`、金额与 SQLite 存储类型均合法；时间是规范 UTC `RFC3339Nano` 且真实位于窗口内。operation ID 对应**恰好一条**账本 operation；该 operation 对应**恰好一条**分录，且就是选中的同一个直属账户分录。operation 与 entry `created_at`、resource kind/ID 完全一致，`resource_kind='adjustment'`、`resource_id=operation_id`；entry 无 `original_entry_id`。`adjustment_credit` 对应正数，`adjustment_debit` 对应负数，金额不可为零或溢出。
2. operation 的 action 恰为 `adjustment`；actor 恰为 typed `admin`，仅 `actor_admin_id` 有效且存在于 `admins`，employee/system actor ID 必须为 NULL，`legacy_unknown` 一律不可冒充。operation 的 `payload_digest` 是恰好 32 字节 BLOB、`digest_version` 只能是 1 或 2。根据选中 account 的规范 direct-employee owner、币种、唯一有序分录、operation/resource、typed admin 身份和**`RequireNonNegative=true`**重构原始 Post 载荷：历史 v1 用既有 `postDigest`/`actor_admin_id` 语义，新 v2 用 `financial-post/v2` 的 typed actor `postDigestV2` 语义，逐字节核摘要；v1 有 admin ID 的迁移记录可读，v1 无身份的 `legacy_unknown` 不可读。不得拿 v2 规则验 v1，也不得因为现余额变化否定过去已提交记录；`RequireNonNegative` 是摘要载荷事实，并非额外的当前余额检查。
3. 完成时复核操作/分录/账户外键引用及不变更、不删除约束；不接受缺失 admin FK 目标、额外同 operation 分录、错账户/币种、错时间/resource、错摘要、错误 SQLite 动态类型或漂移 schema。管理员调整不需要商业 receipt、兑换码或付款记录，不能从这些表拼接原因，也不得推断任何外部付款事实。

SQL/query/scan/迭代/`Rows.Close`、schema/trigger/FK、选中或 lookahead 链、context 取消/超时、只读事务 commit、游标加密任一失败均返回固定 `503 storage_unavailable` 且零部分页面，不回退到活动或分类接口，不跳过坏候选。认证/Origin/路径/query/method 的错误沿用 self 固定脱敏错误面。成功输出前在 `admission.Lock` 下最终复核**同一个** self session 的有效期、员工 active 与 enrollment，并在锁下输出成功，使登出、改密与管理员禁用序列化；复核失败不可输出旧页。不得记录 Cookie、cursor、Key、auth header、上游 token、员工身份、金额、prompt 或模型响应。

## 浏览器、验证矩阵与治理边界

`/self/` 仅在本能力位为 true 时显示独立“管理员本地钱包调整记录”入口。员工输入**一个**币种并明确点击才 GET；mount、登录、打开余额/活动/分类/兑换历史、更改币种均不得自动请求或 fan-out，更不枚举币种。结果、游标只留组件内存；币种、账户、session 改变以及 logout、失败、卸载都清空旧页，用 abort 与请求 generation/身份比对丢弃晚响应。下一页由员工明确触发。正负号只表示本地钱包变化，不标为“充值/付款/退款/工资/补偿/账单”；缺账户和空合格记录须区别呈现。桌面及真实 Chrome 390px 不应水平溢出；除必要 API query，不把身份、结果、游标放进浏览器 URL、localStorage、sessionStorage、IndexedDB 或 service-worker 缓存。

实施后须以**新实现精确 HEAD**分别检验：默认关闭、四前置任一缺失在普通启动/`--init` 持久写前拒绝；新旧路由/JSON/cursor 不交叉；匿名/admin/Bearer/Origin/失效身份、严格路径和 405、query/body；缺账户/空历史、正负号及同币种；本人直属与 Key/resource/异员工、admin 与 employee/system/`legacy_unknown`；管理员调整与商业退款 `adjustment_debit` 的 action 区分；v1 迁移 admin 与 v2 新 admin 摘要、admin FK、唯一 operation/entry、schema/FK/trigger、错类型/时间/resource/摘要；同存储时间、混合精度、分页、跨目的/跨 session/过期游标和并发写；选中/lookahead 损坏、SQL/scan/Rows.Close/commit/context/RNG 故障皆整页 503；logout/改密/admin-disable 最终输出竞态；无财务写、无敏感日志或内部字段泄露；UI 显式点击、翻页、清理及桌面/390px。分别报告本地测试、构建/lint、隔离进程/浏览器与 GitHub CI 的实际证据；本合同完成不等于这些已测试。

本地实施差异须先经只读审核；审核放行后才可提交、push 独立 Draft PR，并在**新精确 HEAD**重钉 GitHub CI、固定二进制与独立验收，不能借用旧批次证据。`GOV-02` 的告知、角色、保留与恢复决策仍开放，不能生产默认开启；单实例预览不承诺多实例一致性。无 tag、package、native release、发布或部署。

## 本地验证与未覆盖

截至本地预推送审核时：新增财务、服务和 CLI 定向 Go 测试通过；财务回归额外注入孤立管理员 FK、跨账户同操作分录及按 `RequireNonNegative=false` 重算的有效格式错误摘要，均确认整页失败且无部分结果；最终输出与 logout 的序列化测试连续运行十次通过；`go vet ./...` 通过。Web 生产构建与全量 Vitest 通过（22 个文件、242 个测试）。本批新隔离进程 smoke 对本地最终重编二进制通过：四项前置缺失在正常启动和 `--init` 都不落盘，默认关为 404，启用后严格身份/路径/query、管理员正负调整、resource 子账户隔离、分页/旧游标隔离和重启后的历史读取均通过；首次启用进程退出后还核对了管理员密码/session/CSRF、员工 enrollment secret/密码/session 未出现在该进程输出中；工作流已接入该 smoke，CI 计划测试 12/12 通过。真实 Chrome 以合成员工在动态非 8787 端口验证：登录后无自动新 GET，明确点击后显示 `+42`/`-7`，改币种立即清旧结果，JPY 无账户；1280px 和 390px 的 `documentElement.scrollWidth == clientWidth`。控制台唯一错误为未登录时预期的 `/self/api/v1/session` 401。所有账号、金额和密码均为临时合成事实，没有真实凭据、外部请求或部署。

本地预推送审核时未完成/不能宣称：GitHub CI、PR 固定 HEAD、Linux/macOS 构建、正式发布及生产环境。后续 PR、CI 与固定二进制验收须逐项核对新精确 HEAD 的实际结果，不得由本段本地证据推定通过。Windows 上 `go test ./... -count=1 -timeout=10m` 的 service 包在现有 `TestAcquireCodexRecoveryUncertainOutcomePausesWithoutSecretOrReplay` 用例的 SQLite `FlushFileBuffers` 路径超时；其他已输出包通过，不能算全量 Go 通过。本机缺少 `gcc`，`go test -race` 在 `runtime/cgo` 编译前置处失败，未得到 race 结果。管理员禁用/改密与输出之间的独立竞态、并发写入时跨页覆盖范围、真浏览器失败态及各平台真实 TLS 仍待后续核验；现有定向 logout 测试不能代替这些证据。

## 来源与许可

本合同依据 CPA Cloud 自有功能规格和已合并仓库结构的只读核对独立撰写；曾看过参考代码，因此不宣称严格 clean-room，亦未复制、翻译、逐行改写或移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考仓库的代码、测试、迁移、素材和文档。2026-10-03 核对的公开技术资料：[Go `database/sql` 事务及 Rows](https://pkg.go.dev/database/sql)、[Go `cipher.AEAD`](https://pkg.go.dev/crypto/cipher#AEAD)、[SQLite 隔离](https://www.sqlite.org/isolation.html)、[SQLite 外键](https://www.sqlite.org/foreignkeys.html)、[SQLite 触发器](https://www.sqlite.org/lang_createtrigger.html)、[SQLite SELECT 排序](https://www.sqlite.org/lang_select.html)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。上述公开资料只支持通用机制，不提供本项目身份、分类和证明链的业务结论。本批未新增依赖、SDK 或素材；新文件及修改处已标本合同 provenance，已有依赖各守原许可证，见[依赖许可记录](research/dependency-notices.md)。
