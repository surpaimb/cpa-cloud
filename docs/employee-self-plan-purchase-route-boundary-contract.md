# ID-05 / BILL-03 员工本人套餐购买 POST 路由边界补充契约

状态：2026-10-04，开发预览、合同先行；精确起点是已合并 `main` `5295e1edf205adc291d4864e320c682389ffb652`（tree `146d8f34fc23c5f68a59d3077f7ac86da715dd65`）。原合同已独立本地提交，后续隔离旧基线修前红已观察到两条清理重定向；本次未暂存澄清只规定后续修复的兼容目标，不代表守卫已实施、修后测试或 GitHub 编译已通过。它补充[本人 one-time 套餐购买合同](employee-self-plan-purchase-contract.md)；会话及写授权依据[员工自助基础合同](employee-self-service-foundation-contract.md)，GET 订阅列表和套餐目录分别仍由[本人订阅状态合同](employee-self-subscription-status-contract.md)与[套餐目录合同](employee-self-plan-catalog-contract.md)决定。项目范围另见[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)和[预览接口](preview-contract.md)。

## 唯一范围与静态风险

本批只保护两个**现有 POST**：`Q = /self/api/v1/billing/plan-purchase-quotes`（取得冻结报价）和 `S = /self/api/v1/billing/subscriptions`（确认购买）。成功路径必须是相应字面原始路径，方法必须为 `POST`，且仍需现有 `--employee-self-plan-purchase-enabled`，其默认值及三个前置开关保持不变。`GET S` 恰好与购买 POST 共用路径，却归独立的本人订阅状态开关；本守卫不得把它改成购买专属 404、400、405 或写授权。本文不增 flag、capability、API、成功字段、UI/CLI、DDL、财务写入、provider、支付或 worker；报价、密码、重放、购买事务和商业执行门禁均交给原 handler。

只读核对本基线的 [`self_service.go`](../internal/service/self_service.go) 表明两条购买 POST 仅在功能开启时以 canonical 模式注册于 `ServeMux`；[`app.go`](../internal/service/app.go) 的现有外层守卫链没有购买守卫。随后从普通、非浅且 tracked-clean 的同一 `main` 构建固定旧 exe，在两个随机非 8787 回环端口、隔离 data-dir/WebDir、无自动跟随的原始 TCP 下，`POST /self/api/v1/billing//plan-purchase-quotes` 与 `POST /self/api/v1/billing/./subscriptions` 均在购买 flag 关/开、匿名/有效 self 写会话四组返回首个 `307`，`Location` 分别指向字面 Q/S，`Cache-Control: no-store`、无 `Allow`、无 WebDir marker。对应 canonical 关态为 404；开态匿名为 401、有效会话配空对象为原 handler 的 JSON 400。此证据只证明这两种清理形状的路由边界差异，不证明其他编码路径、越权购买或财务副作用。

## 路径证据、归属和有限预算

仅对 `POST` 做购买归属判断。按下述预算取 `RequestURI` 中第一个**字面** `?` 之前的 path；同时独立核对有界 `URL.Path`、`URL.RawPath` 和从有界 Path 副本取得的 escaped 视图，只有各源完整且不超预算时才可比较原 `URL.EscapedPath()`。`Path` 已由 Go 解码，`EscapedPath()` 可能选用经验证的 `RawPath`，二者都不能替代原始 request-target；`%3F` 不当作 query 分隔符，`%23` 不当作 fragment。真实 origin-form 请求只有在原始 path、`Path`、原 `EscapedPath()` 均**完整**且逐字等于 `Q` 或 `S`，`RawPath` 为空或完整且逐字相等时才是购买的 **literal canonical**。`Q?`、`S?x` 的 path 仍可 canonical；裸问号和 query 是否非法仍由原 POST handler 原顺序判断。`RequestURI` 缺失、任何必需视图不完整或 URL 视图矛盾的构造请求不能据单一视图进入成功 handler；测试须提供真实或等价的完整字段。

非 canonical 视图只可用于**拒绝**，绝不作为业务路由。分段匹配须消耗完整 ASCII 段；为识别拒绝形状，可以有界地逐轮 `PathUnescape`，在每轮保留未清理段，再仅为拒绝判断考虑重复 `/`、`\`、`.`、`..`、大小写和清理目标。编码字母、`%2F`/`%5C`、编码点段与多层 `%25` 必须进入该证据过程；不能对 Unicode 做宽松大小写折叠，不能把 URL 解码后的别名改写成 `Q`/`S` 再交给购买 handler。任一视图可证明完整 `Q` 段，后面为目标结束或真正的斜线/反斜线段分隔符（含有界解码所得）时，是报价候选；其 `/extra`、尾斜线或清理别名都只能拒绝。现有兄弟守卫均放行后，完整字面 `S` 目标或有界拒绝视图清理后恰为 `S` 的 POST 是购买候选，非字面者只能拒绝；清理后仍为 `S/{id}` 或其子路由的是邻居，不得仅凭 `S` 前缀认领。

完整段邻居必须保留原 owner：`Q-other`、`Qx`、`Q%2Dother`、`Sx`、`S-other`、`/self/api/v1/billing/plans`、`S/{id}`、`S/{id}/cancel`、`S/{id}/renew`、`S/{id}/renewal-quotes`、`S/{id}/renewal-links`、`S/{id}/one-shot-renewal`、`S/{id}/purchase-snapshot` 及未知 `/billing/` 路径均不因文本前缀成为本购买候选。没有完整段证明的 `Q%3Ffoo`、`Q%23foo`、`Q.extra` 和类似拼接也属邻居；编码问号/井号不能偷换为真实 query/fragment。相反，`/self/api/v1/billing//plan-purchase-quotes`、`Q/extra`、`Q/`、`/self/api/v1/billing/./subscriptions`、`S/./` 与字面路径的大小写/编码字母别名是应在预算内证明的拒绝候选。兄弟守卫均放行后，`S/id/..` 的清理目标恰为 `S`，归本购买畸形 POST；`S//id` 清理后仍为 `S/{id}`，保持 ID 邻居，不被本守卫抢占。

本购买守卫必须置于**所有现有 self 兄弟守卫内侧、只包住 `ServeMux`**。既有月度续购、renewal-links、purchase-snapshot、cancel、one-shot、estimated-cost、topup-credits、admin-adjustments、redemption-credits、entry-classifications、redemptions 等守卫先按当前链条运行；任何一个已认领的请求不再进入购买守卫，不能为本批创造新的全局“原始第一业务段”规则，也不能改其开关、授权和错误优先级。只有它们全部放行，购买分类才用本节的完整段和有界清理证据：清理恰为 `Q` 或 `S` 的 POST 可以认领为畸形；清理仍为 `S/{id}` 或其子路由的请求是邻居；不能把任何别名转给成功 handler。可证伪交叉目标：`Tcancel = S/id/cancel/../..` 清理到 `S` 但原 cancel 守卫先认领；`Tred = /self/api/v1/billing/redemptions/../plan-purchase-quotes` 清理到 `Q` 但原 redemptions 守卫先认领；反向 `Q/../subscriptions/id/cancel` 虽有原始 Q 段，原 cancel 守卫若已认领仍必须优先，不能被新购买守卫强夺。对各例须用实际原守卫的可观察响应与开关组合确认，不以纯字符串先后替代原代码行为。现有兄弟守卫自身的优先级不由本文改写。

预算**仅约束新增购买分类器**：每个路径视图最多检查 **8192 字节**，原视图后最多做 **16 次** `PathUnescape`；不宣称限制或重排外层现有守卫。8192/16 明取本基线 estimated-cost 守卫的已有量级，便于证明新增分类成本有界，而非 Go/HTTP 的默认上限。对 `RequestURI` 从头至多察看路径候选的前 **8193 字节**：字面 `?` 若在前 8192 字节之后的紧邻边界出现，仍能证实恰为 8192 字节完整 path；更早的 `?` 立即截断 path，后面再长的 query 不计入；若没有在界内看到字面 `?` 或真实末尾，则只保留 8192 字节前缀并标记 incomplete。对 `URL.Path` 与 `URL.RawPath` 各自先以长度检查、截成 8192 字节并分别保留 complete/incomplete；不得先对超长 `RawPath` 调用原 `URL.EscapedPath()`。从**有界 Path 副本**派生 escaped 视图，派生结果也须截断到 8192 字节，并继承 Path 的 incomplete；只有 Path/RawPath 等必需原视图都 complete 时才可调用原 `EscapedPath()` 做 literal 比较。解码或转义轮次中的任何截断继续传播 incomplete，不能把前缀误认成完整末段。只有已检查前缀足以证明完整段及边界的候选才能拒绝；证明依赖第 8193 个路径字节、第 17 次解码或未见的末尾时是不可归属，不猜测隐藏购买路径、不新增购买专属 400/404。不可归属目标继续原 owner；全局请求大小上限须另立合同。测试须区分 8191/8192/8193 字节 path、长 query、长 `RawPath`，以及第 16/17 层才显出 `Q`/`S` 的两种情况。

这里的“真实末尾”仅指 path 长度不超过 8192 字节；第 8193 个被检查字节若仍是路径字符，即使随后立即结束，也必须是 incomplete。只有该字节恰为字面 `?`，才能证明前 8192 字节恰好构成完整 path。完整段的终止边界也必须位于已检查范围内，不能把截断处假定为路径末尾。

## 开关、授权及响应顺序

下表仅约束被 Go HTTP 解析器交给应用的、已证明归属本购买 POST 家族的请求；解析器提前拒绝的非法 request-target（例如不合法百分号转义）不要求应用 JSON。所有应用响应保持 `Cache-Control: no-store`；本守卫产生的 404/400 均无 `Location`、无 `Allow`，不能以 301/302/307/308 替代。

| 条件 | 处理与可观察结果 |
| --- | --- |
| 购买 flag 关闭，或自助总开关关闭；`POST` canonical/已证明畸形 `Q` 或 `S` | 在 self 身份、Origin、限流、数据库、`ServeMux`、WebDir 和购买方法/正文判断之前立即 404；无 `Location`/`Allow`。不要求 404 正文格式。 |
| flag 开启；`POST` literal canonical `Q` 或 `S` | 原样交给现有 `requireSelfReleased(..., true)` 及原报价/购买 handler；不在守卫重做 query、body、token、密码或业务校验。`Q` 原有写授权后报价语义、`S` 原有写授权和 `selfGate` 后购买/精确重放语义不变。 |
| flag 开启；`POST` 已证明畸形 `Q` 或 `S` | 先沿用现有 self 写授权：可选 Origin 初验、当前有效且已开通/active 的 self Cookie、唯一同源 Origin、`X-Self-Request: 1`、当前会话 CSRF；再走现有 peer/员工 `selfGate` 限流检查，最后固定 JSON 400 `invalid_request`。授权或限流失败保留原 401/403/429/503 优先级。形状错误本身不算密码失败、不调用 bcrypt、不解析报价或请求体、不进入报价读取或财务事务。 |
| `GET`/`HEAD`/其他非 POST，或 POST 完整段邻居/不可归属 | 不进入本购买守卫的 flag/写授权/专属错误分支；保持本基线各原 owner 的路由、开关、身份、方法、query/body 及响应头行为。 |

对畸形 POST 先查 self 会话及其持久状态是允许的；“不进财务”不等于“零 DB 查询”。管理员 Cookie、员工模型 Bearer Key 或匿名请求不能代替 self 会话。`Q` 的 canonical handler 原本不调用 `selfGate`，本合同不改这一点；上述限流检查只用于本批新增的**畸形 POST 拒绝路径**，不得倒灌改变 canonical 报价的成功或错误签名，也不得凭畸形路径增加密码失败计数。关态的 404 则必须比这些认证/限流错误更早。原购买合同的 409、提交不确定 503、密码和精确重放优先级全部保留。

尤其应以功能开关关/开两组比较下列原 owner 的**首个**状态、`Location`、`Allow`、`Cache-Control`、Content-Type 与安全正文类别：`GET S` 在本人订阅状态开/关两组、`GET S/{id}` 及已注册的 ID 子路由、`GET /self/api/v1/billing/plans` 在目录开/关两组；`POST Q-other`、`POST Sx` 以及一般未知路径。开购买开关不得单独启用订阅读取或套餐目录，也不得让这些邻居因购买专属 400/404/405 泄露 flag。规范 `POST Q?x`、`POST S?` 和两种业务 body 仍让原 handler 先执行原写授权再按原合同处理；不借本守卫把 query 变成路径错误。

## 修前证伪与后续验收

原合同本地提交后、根任务单独放行的**仅修前红**已在固定旧 exe 上完成上述两种形状和 canonical/邻居对照：保留了无秘密原始 request-target 与首个 wire status/`Location`/`Allow`/no-store/Content-Type/错误码/marker 的终端记录，两个进程均已停止。旧 exe 无请求字段诊断接口，故此次不能把按 Go 解析规则**推断**的 `RequestURI`/`Path`/`EscapedPath()`/`RawPath` 冒充服务实际观测；下一获准的测试阶段须用同版本 Go 的受控请求捕获补足该项。根任务另审本次未暂存澄清之前，不改 Go/Web/脚本/测试，不启动更多进程或浏览器，不跑 CI，不 push、不开 PR/Ready。

获准修复后，以独立编写的表驱动测试交叉覆盖 Q/S、flag 关/开、canonical/畸形/邻居、POST 与 GET/HEAD/DELETE，及匿名、管理员、模型 Key、有效/过期/退出/停用 self 会话、错/多 Origin、缺/错 `X-Self-Request`、缺/错 CSRF、限流先于畸形 400。形状用原始真实请求目标证明重复 slash、dot、大小写、backslash、编码字母/分隔符、单/双/多层编码、完整段邻居、跨兄弟优先级和预算边界；专门覆盖 `Tcancel`、`Tred`、`Q/../subscriptions/id/cancel`、`S/id/..`、`S//id`、超长 `RawPath` 与长 query。尤其证明 `GET S`、ID 子路由、`GET plans` 的原 owner 在开关组合下不变。断言畸形 POST 不触密码/bcrypt、报价读取、财务事务/写入，不把原请求或 `PathValue` 改写后转发。Go `httptest` 或纯分类函数不能单独证明首个 wire 状态；不跟随重定向的 TCP 验收是必要补充。此批后续还须另报定向测试/build/lint 与固定提交 CI，不能借用本合同或旧 PR 证据。

## 来源、许可与边界

本文基于 CPA Cloud 自有购买规格、本基线自有路由/守卫的只读核对和独立拟定的拒绝矩阵撰写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树；既往研究使本工作不能称为严格 clean-room。2026-10-04 查阅的公开机制资料为 [Go `ServeMux`](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.RequestURI` / `PathValue`](https://pkg.go.dev/net/http#Request)、[Go `URL.Path` / `RawPath` / `EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 3986 百分号编码](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1)与[点段移除](https://www.rfc-editor.org/rfc/rfc3986.html#section-5.2.4)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。标准资料说明通用机制，不证明本项目动态表现；上述 404/400/授权优先级是项目要求。本文没有新增第三方依赖、SDK 或素材；Go 标准库保留 BSD-3-Clause，现有依赖各保留自身许可与[许可清单](research/dependency-notices.md)。后续若引入源码或依赖，须另记明确来源及相应许可。
