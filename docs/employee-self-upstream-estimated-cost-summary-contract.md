# 员工本人近 24 小时上游内部估算成本摘要契约

状态：2026-10-03，独立设计合同与同分支实施范围记录；合同先于实施单独提交。本地定向 Go、Web、隔离二进制与真实 Chrome 验收已有通过记录；Windows 全仓 Go 在 15 分钟上限超时，本机无 C 编译器而未运行 race，不能据此声称全仓或生产验收通过。实施提交仍须核验其精确 HEAD 的 GitHub CI 结果；本合同不授权生产开启、发布或部署。精确设计基线为已合并 `main` `d7a997a20bc7e0977b635fb67c513436ee587cd0`，tree `a2513949c255d33d002950e47c536146d60960bf`。本合同依照本项目[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览契约](preview-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[现有本人 Token 摘要](employee-self-token-summary-contract.md)、[用量管理](usage-management-contract.md)、[内部账本](usage-ledger-contract.md)、[协议用量映射](usage-protocol-mapping.md)和[账号组内部成本分摊](account-group-cost-allocation-contract.md)制定；没有参考相邻或归档项目实现。

## 语义、开关与披露政策

只展示**服务内按上游尝试冻结价格估算的原始成本已知部分**。它不是供应商账单/发票、真实支付、员工收费或售价、员工钱包扣款、预算、配额、套餐、订阅或模型权益；管理员配置的内部价格也不证明供应商实际收款。不同币种绝不换汇或合计，未知不能当作免费，已知零不能当作未知。一次员工请求可有多次上游尝试；重试/换号分别计数，不把尝试数伪称为员工请求数。系统探测与后台维护事实不混入本人模型请求。

独立命令行开关 `--employee-self-upstream-estimated-cost-summary-enabled` 默认为 false。唯一前置是既有 `--employee-self-service-enabled`；前者为 true 而后者为 false 时，普通启动和 `--init` 都必须在任何持久写入、迁移或服务监听前拒绝。钱包、商业执行、管理员用量与本能力不互相隐式开启。仅完成后端、网页和故障验收才在 self session 中声明 `features.employee_self_upstream_estimated_cost_summary=true`；关闭时新路由为 404，旧服务缺能力位时网页不探测。无新 DDL、账本/财务写入、provider 调用、worker 或依赖。

[README](../README.md) 的本人总览和单 Key 总览两处说明已同步修订为默认不展示成本；仅独立显式开启后展示此有限估算摘要，仍不展示 Key 策略、账单或售价。本合同定义的是**默认关闭的可选内部成本披露政策变更**，并不替代治理批准。`GOV-02` 的员工告知、角色授权、留存、备份/恢复和内部成本可见性政策仍未批准；生产必须保持本开关关闭。关闭页面不删除既有账本、WAL 或备份事实。

## 唯一 HTTP 面与最小响应

唯一入口是字面、区分大小写的 `GET /self/api/v1/usage/estimated-cost-summary`，**零 query、零正文**：连裸 `?`、编码别名、重复/未知参数、非零 `Content-Length`、chunked 或未知长度正文也拒绝。关位时精确及形似路径都 404；开启时在 `ServeMux`/WebDir 清理、重定向或静态 fallback 前截获形似路径，转义、反斜线、双斜杠、尾斜杠和附加段不得变成成功或 301/307/308；精确路径非 GET 返回 405 和 `Allow: GET`，其余非法形状/输入固定 400 `invalid_request`。使用现有 self session 的有效期、员工 active/已开通状态与可选同源 `Origin`；匿名、管理员 Cookie、员工模型 Bearer Key 或其他员工 session 不可代替。GET 不要求密码、CSRF 或 `X-Self-Request`。所有成功与错误均 `Cache-Control: no-store`，不经管理员 HTTP API。

服务在首页接收时冻结 `to = UTC now` 的**下一整秒**，`from = to - 24h`，按本人父 `accounting_requests.started_at` 的真实时间半开 `[from,to)` 定界；没有客户端时间、员工/Key、币种、模型、账号、状态选择器，也没有分页或游标。成功 JSON **恰好**如下顶层和嵌套字段；示例仅说明字段形状与含义，不代表实时返回值或供应商账单：

```json
{
  "from": "2026-10-02T12:00:00Z",
  "to": "2026-10-03T12:00:00Z",
  "attempts": {"total": "4", "pending": "1", "terminal": "3"},
  "costs": [
    {"price_currency": "USD", "attempts": "2", "known_estimated_cost_micro": "7", "unknown_cost_attempts": "1"},
    {"price_currency": null, "attempts": "1", "known_estimated_cost_micro": "0", "unknown_cost_attempts": "1"}
  ]
}
```

全部计数和金额均为 `int64` 范围内的规范**非负十进制字符串**（零写 `"0"`，无前导零、浮点数或指数），时间为规范 UTC RFC3339 整秒。`attempts.total = pending + terminal`；`terminal` 包含 `succeeded|failed|cancelled|interrupted`，只表示已经终结的尝试，不代表成功请求。`costs` **仅**容纳 terminal 尝试；每个 terminal 恰入一组，故 `sum(costs[*].attempts) = attempts.terminal`。`price_currency` 来自该 attempt 派发前冻结的价格快照：有价须为三位大写 ASCII 币种，无价才是 JSON `null`，不能用字符串 `UNKNOWN` 冒充。各币种按代码升序，null 最后；无 terminal 时 `costs:[]`。组内 `known_estimated_cost_micro` 只加可信且已知的原始估算成本，`unknown_cost_attempts` 计终态但成本不可信/不可算的尝试。已知零且未知数为零才是本组完整零；有未知时已知和只是下界而非完整账单。pending 不入 unknown，不得用零价格或零用量补缺值。

响应与网页不得返回/持久化 request、attempt、员工、Key、模型、provider、上游账号、价格版本/费率、token、V2 event、correction actor、allocation 倍率/组、钱包/账本 ID、余额、错误正文、提示词、模型回复、凭据或任意内部行。`from/to` 与分币金额是本能力唯一可见的时间/成本信息；不得写浏览器 URL、localStorage、sessionStorage、IndexedDB 或 service-worker 缓存，也不得记录在服务日志。

## 单快照读取与可信成本判定

服务从已认证 session 得到**唯一** EmployeeID，派生最长 5 秒 context，持有一个**调用方拥有的只读 SQLite 事务**完成 schema 校验、候选读取、V2 证明链、更正重算和聚合；所有后续查询都用该事务，不在读请求迁移/修复，不以另一次 `db.Query` 拼接快照。使用规范九位小数 UTC 存储键或等价的精确时间比较来筛选父 `started_at`，并逐行解析校验规范 RFC3339Nano 与真实 `[from,to)` 边界；可变小数宽度的裸文本比较不能漏掉边界秒。按父员工归属联结其所有 attempt（包括 pending），最多读取 `250000+1` 条：第 250001 条即固定 503，**不截断伪装成完整摘要**。零 attempt 正常返回零计数与空组，不凭同窗口零尝试推断员工没有请求。

事务先核本批依赖的既有 accounting 表、列、CHECK、索引、声明 FK 和 V2 不可变 UPDATE/DELETE trigger 的精确预期结构；仅存在同名对象不够。对每个合格父/子校验 SQLite 存储类型、ID/员工归属、provider/状态、父子 FK、开始/完成时间、价格快照整体空或整体合法，以及相关 context/dispatch/event/correction 引用；跨员工 attempt、孤儿、错币/价格、重复、矛盾时间或状态都不得被连接过滤后静默忽略。与本员工范围无关的其他员工行不纳入响应，但不能以访问别人明细来补页或计算费用。计数、每组费用、修正后的非负 Token 和 `int64` 算术均检查溢出；不使用浮点或 SQLite `total()`。

pending 只计总数与 pending，不读取它的未来费用，也不计未知终态。对 terminal：仅在相关 V2 attempt context、可靠 usage event、必要的 durable dispatch 事实及其一致性可验证时才产生**已知**成本。event 的 attempt/request ID、status、evidence、protocol、终结时刻、价格版本/币种必须与父子及冻结快照一致；有 dispatch 时须满足时序，无 dispatch 仅接受既有系统终结/恢复规则允许的未派发路径。没有完整 V2 证据的合法旧记录保守计入该冻结价格币种或 null 组的 `unknown_cost_attempts`，即使旧 `accounting_attempts.cost_micro` 非 NULL 也不直接当作可信费用；经既有规则可证的未派发恢复中断也为 unknown。**部分 V2 链**、多 event、缺必要 dispatch、错引用或矛盾状态则是损坏，整页 503，不能降级为旧记录或简单 unknown。必须明确检验合法 legacy/未派发恢复与真正 partial 的区别，不通过猜测创建“零成本”。

有完整 V2 链时，从该 attempt **冻结**的 `price_version/currency/四类每百万 Token 费率` 与 base event 的规范化计数重算 base 原始 `estimated_cost_micro`，并与已存值的 NULL/整数语义核对。随后按唯一、连续、最多 200 条的 correction `sequence` 顺序，在同一事务核 target event、attempt、operation 幂等身份、actor/reason 形状、严格递增规范时间、同币种、`*_set` 与 `*_delta` 排他、非负结果及可适用的 reasoning 约束；逐次从冻结费率重算更正前后成本，核每条 `estimated_cost_delta_micro` 的可空/数值一致性。最终费用来自**有效更正后的用量重新计算**，不是把任意 delta 相加、取旧 `cost_micro`、查当前价格目录或取账号组 `adjusted_allocation_cost_micro`。没有冻结价格或协议必需用量未知时最终成本仍未知；确切零须由完整证据得出。中间/最终溢出、错更正或超 200 条整页 503。

适用协议范围与现有适配器一致：[OpenAI Chat Completions](https://developers.openai.com/api/reference/resources/chat)、[Responses/Codex](https://developers.openai.com/api/reference/resources/responses/methods/create)、[Anthropic Messages](https://platform.claude.com/docs/en/api/messages/create)、[Gemini generateContent](https://ai.google.dev/api/generate-content)及[OpenAI Embeddings](https://developers.openai.com/api/docs/guides/embeddings)。Chat/Responses/Codex 的缓存读/写不得与普通输入重复计价，reasoning 已计入输出不再叠加；Messages 的普通输入、cache creation、cache read 分桶；Gemini 的 cached prompt 与 thought/candidate 依现有规范归一化。以上四种生成协议需要四类互斥桶均已知才可按 `ceil(sum(tokens × frozen_rate)/1,000,000)` 得已知成本；Embeddings 是 **input-only**：有可信 input 和冻结价格即可已知，output/cache/reasoning 为 NULL 不使它自动未知。失败、取消、中断但有可靠终态用量仍可计已知；反之成功但 usage 缺失仍未知。不同协议的流式累计快照只采用最终可信状态，重放/幂等更正不得重复计数；不为未支持的收费类别推断额外费用。

SQL/scan/迭代/`Rows.Close`、事务 commit、schema/FK/index/trigger、V2/更正证明、context 取消或超时、任何算术错误、行数超限均在写任何成功头/字节前返回固定 `503 storage_unavailable` 且零部分摘要。并发终结或更正以同一 SQLite 快照为准：读要么见提交前，要么见提交后，不拼接两种状态；下次显式查询可变化，不承诺历史值冻结。读取完成后在 `admission.Lock` 下最终复核**同一个** self session、员工 active/已开通与绝对失效时间，并在锁下输出成功，使 logout、改密、管理员禁用与输出序列化；失效不能输出旧快照。该锁不替代上述只读事务，服务不对管理员端点发每请求 HTTP 调用。

## 浏览器、验收与治理门槛

`/self/` 只有 capability 为 true 才显示独立“上游内部估算成本”入口。仅员工明确点击才发送新 GET；mount、登录、查看旧 `/self/api/v1/usage/summary`、Key/钱包/订阅导航均不得自动 fan-out。响应只留组件内存；开始新读、登录/登出/换 session、能力消失、失败、卸载时立即清旧数据，abort 与 generation/身份比对拒绝晚响应。明确标“已知估算部分/未知尝试”，不标“应付金额/账单/消费/扣款”；按币种独立展示，null 为未配置价格。旧 Token 摘要的 JSON、授权、窗口、自动读取行为及其他自助能力不变。桌面和真实 Chrome 390px 不横向溢出。

实施后用独立编写的自动测试与隔离进程验证：默认关/四类角色和会话隔离、唯一前置缺失在正常启动与 `--init` 写前拒绝、严格 literal path/Origin/no-store/零 query-body/405、旧接口兼容、父窗口整秒与纳秒边界、零 attempt、多次重试、pending 与四终态、同币/跨币/null 分组、已知零/未知、改价前后冻结快照、四生成协议加 Embeddings input-only、流式与失败用量、合法 legacy 和未派发恢复、V2 partial/orphan/错状态、base 与多次更正/重放、错误 delta/超过 200、更正与读并发、`250000+1` 上限、坏类型/schema/FK/index/trigger、Rows/Close/commit/context/overflow 与无部分响应、logout/改密/admin-disable 最终输出竞态及敏感日志。固定二进制在动态非 8787 端口以合成上游和临时员工复现多币种、重试、unknown、重启；真实 Chrome 验证显式点击、能力门控、失败/晚响应清理、桌面/390px 与无浏览器持久化。再分别报告定向 Go、非缓存全 Go/race/vet、Web typecheck/tests/build、精确 HEAD GitHub CI 与独立二进制验收；**本合同和本地文档提交均不是这些证据**。

本位增加员工可见内部成本，须由 `GOV-02` 明确告知、角色、内部价格敏感性、保留/删除、WAL/备份恢复及审计责任后才可讨论生产开启。没有真实供应商发票核对、资金链、汇率、员工收费或跨实例一致性保证。本合同不授权 tag、native package、发布或部署。

## 来源、许可与独立性

上述协议链接和 [OpenAI 缓存说明](https://developers.openai.com/api/docs/guides/prompt-caching)、[Anthropic 流式说明](https://platform.claude.com/docs/en/build-with-claude/streaming)、[Google Gemini thinking](https://ai.google.dev/gemini-api/docs/generate-content/thinking)于 2026-10-03 按官方公开文档核对；[Go `database/sql` 事务](https://pkg.go.dev/database/sql#DB.BeginTx)、[SQLite 事务](https://www.sqlite.org/lang_transaction.html)、[SQLite 外键](https://www.sqlite.org/foreignkeys.html)及 [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339.html)只支持通用机制。它们不证明 CPA Cloud 的内部定价、员工披露权或供应商实际结算。新源码与测试须显式标本合同 provenance，不能复制、翻译、逐行改写或移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考仓库；此前曾阅读参考资料，故不宣称严格 clean-room。本能力的合同与实施未引入新的依赖、SDK、素材或许可证变更；既有第三方依赖各守原许可证，见[依赖许可记录](research/dependency-notices.md)。
