# ID-05 员工本人上游估算成本摘要的路由边界契约

状态（2026-10-04）：原合同已先行本地提交；其后在原基线做过真实 TCP 修前红，跨路由家族优先级的合同澄清已记录，实施与验收尚未完成，不能据此声称修复或验收通过。本批从已合并 `main` 的 `40d53f20f10e5591b6876bf90e92b88b3b42a7c8`（tree `bcaa4a4e6da46534c725694943191aa1e4b0ba94`）起步。本文独立约定现有只读入口在 Go `ServeMux` **之前**如何辨认请求目标；原有[估算成本摘要合同](employee-self-upstream-estimated-cost-summary-contract.md)仍决定数据、披露和业务验收。项目边界另见[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览接口](preview-contract.md)及[员工自助基础](employee-self-service-foundation-contract.md)。

## 边界与待验证风险

唯一成功目标仍为区分大小写的 origin-form `GET /self/api/v1/usage/estimated-cost-summary`，没有查询串或正文。`--employee-self-upstream-estimated-cost-summary-enabled` 默认关闭，且只能在 `--employee-self-service-enabled` 已开启时启用。本批不新增 API、开关、capability、字段、价格或用量算法；不改 Web、CLI、DDL、账本、财务、provider、worker、管理员鉴权及其他员工自助路由。形状判定只负责把**本入口已确认归属**的畸形目标拦在 `ServeMux` 的路径清理、重定向和 WebDir/fallback 之前，绝不能把解码别名转送给业务 handler。

只读核对原基线 [`self_estimated_cost_summary.go`](../internal/service/self_estimated_cost_summary.go) 显示分类器从 `URL.Path` 出发，用 `strings.HasPrefix` 对清理和保留段视图匹配，并重复调用 `PathUnescape` 而没有明确次数或字节上限。随后以原基线的有效员工会话、动态回环端口和不跟随重定向的真实 TCP 请求证实：`P-other` 与 `P%2Dother` 被成本 guard 返回 JSON 400 `invalid_request`，普通 `/self/api/v1/usage/obviously-other` 为文本 404；深层编码的工作量风险仍为静态判断，不宣称已动态证实。现有测试已要求 `%3Fx`、`%3Bextra`、`%2Eextra` 在关态不进网页、开态成为固定路径错误；不能为修复邻居过捕获而悄悄放弃这三例。

## 目标归属：完整段、编码例外与有限判定

设 `P = /self/api/v1/usage/estimated-cost-summary`。分类器必须同时考虑服务端收到的 `RequestURI` 原始路径部分（只按**字面** `?` 分离 query）、`URL.Path`、`URL.EscapedPath()`，以及存在时的 `URL.RawPath`。后两者并不保证与原始目标逐字相同；`RawPath` 可能为空，不能以此跳过校验。仅当原始目标就是 `P`、`URL.Path` 和 `EscapedPath()` 也都是 `P`、非空 `RawPath` 不与 `P` 冲突时，才是可交给原有 GET 处理的字面路径。路径的字面正确不代表 query/body 合法；`P?` 与 `P?x` 都不是成功目标。

用于**拒绝而非执行**的视图可识别 ASCII 大小写变体、重复斜线、尾斜线、反斜线、点段、被编码的路径字母或分隔符及其有限层嵌套。每次先保留一个未清理的段视图，再考虑标准化后的视图：只要其中一个可证明经过完整的 `P` 路径段或落到 `P`/其后代，即成为**成本候选**；即使后续 `..` 会使普通清理离开 `P`，也不能让该候选直接进入 fallback。最终归属还须按下述跨家族优先级决定。匹配 `P` 的末段必须证明到达该段**末尾**，其后仅可为原目标结束或真正的 `/`、反斜线分隔符（包括其编码形式）。单纯字符串前缀绝不够：`P-other`、`Px`、`P_more`、`P%2Dother`、`P%252Dother`、其他 `/usage/` 或 `/self/` 目标均为邻居，交还既有路由；字面 `P.extra`、`P;extra` 同样不因前缀相同而被本 guard 占有。

唯一兼容性例外是原始路径或其有限解码副本**可证明**在完整 `P` 末段之后紧跟百分号编码的 `?`、`;`、`.`（分别为 `%3F`、`%3B`、`%2E`，十六进制大小写不限），后面可有额外字节。这些是既有 `%3Fx`、`%3Bextra`、`%2Eextra` 回归用例所代表的**编码尾缀畸形目标**，只能拒绝，不能视作新路径段或有效 query；未编码的同名标点尾缀不享有这个例外。`%25` 包裹上述转义也只能在预算内证明后认领。编码的 `-`、普通字母或无法证明的 `%` 后缀不能套用此例外。原始字面 `?` 才是查询分隔符，编码 `%3F` 不能先被拆作 query。

分类工作量必须有固定上界：每个来源视图最多检查前 **8192 字节**、最多执行 **16 轮**整串 `PathUnescape`，无变化或转义非法即停止。每轮及清理/保留段检查均受该字节预算约束，不构造无界的新串。第 16 轮后的剩余编码最多再做一次线性、只读的 ASCII 语法证明：允许按有限输入字节识别 `%25` 重复包裹的单个十六进制字节，并证明整个锚定末段及其真实分隔符或上述三种编码尾缀；它不是第 17 次全串解码，也不输出可路由的新路径。证明不了即为“未归属”，而非推测成本入口。实现若不能在此约束下兼顾已证明家族和邻居，须先报告设计阻断，不能扩大到整个 `/usage/` 或 `/self/`。

超过 8192 字节时，可仅凭已读前缀中**完整证明**的 `P` 及紧随的段分隔符或编码尾缀认领；截断不能被当作末段结束。若决定家族/邻居的字节在预算之外，例如超长共同前缀之后才出现 `-other` 或 `/extra`，两个目标都交给通用 HTTP/`ServeMux`/fallback；本合同不对这种不可判定目标承诺固定 400/404 或禁止通用层重定向。不同来源视图出现可疑但不充分的线索时也不得靠猜测认领。所有视图仅供拒绝性检查，绝不改写 `RequestURI`、`URL.Path`、`RawPath`、`RawQuery`、`PathValue`、方法、正文或上下文。多层解码只是有限的威胁识别，不宣称 HTTP 协议把这些目标视为相同 URL。

## 与其他员工自助路由的交叉形状优先级

保持 [`app.go`](../internal/service/app.go) 现有 guard 包裹顺序：monthly renewal、renewal-links、purchase-snapshot、cancel、one-shot 在成本 guard 外层，已认领的请求仍由它们处理；成本 guard 在 topup-credits、admin-adjustments、redemption-credits、entry-classifications、redemptions 内层 guard 之前。本节只裁决**已在成本自身预算内证明、但不是字面 canonical `P`** 的交叉候选。成本的 8192 字节/16 轮上限并非外层 guard 或整个 HTTP 链的统一工作量承诺；本批不重排或修改其他 guard。

对于这样的成本候选，只有 `URL.Path` 不超过 8192 字节，才检查内层 guard 在 `URL.Path` **首视图（第 0 轮，不再解码）**是否已经能证明自己的完整路径段。此首视图内层仲裁必须先于成本 feature-off 404 和任何成本读授权执行。按各自现有第一视图规则构造清理和保留段视图：topup-credits、admin-adjustments、redemption-credits、redemptions 将大小写折叠并把反斜线视作斜线；entry-classifications 折叠大小写但**不**转换反斜线。清理视图使用 `path.Clean`；保留段视图跳过空段和 `.`，保留 `..`，避免后续清理掩盖先前经过的家族路径。两种视图都须按完整段判断本 guard 的精确路径或其 `/` 后代，不能以字符串前缀占有邻居；redemptions 还遵循其既有 `?`/`#` 尾缀识别。即使 `URL.Path` 含 `%`，只要这一步已经证明内层家族，也不额外排除；若必须再解码一轮才显露内层家族，则不委派，仍由成本 guard 处理。超过上述 `URL.Path` 字节预算的已证明成本候选也不做内层委派。

若首视图有多个内层家族同时命中，按现有内层执行顺序 **topup-credits → admin-adjustments → redemption-credits → entry-classifications → redemptions** 选择唯一接收者，而不是让分类器的迭代先后改变归属。成本 guard 直接把原请求交给该 guard，但它的 `next` 必须是终止型成本拒绝处理：完整执行成本开关检查、读授权和固定路径 400 的现有优先级，绝不继续普通内层链或落入 `ServeMux`/WebDir。选中的 guard 若认领请求，沿用它自己的开关、授权及错误响应；若它意外未认领，终止型 `next` 接住该已证明成本候选。外层 guard 已优先认领的目标不进入此步骤；字面 canonical `P` 仍只走原成本成功/方法/query/body 逻辑。此委派不授权任何写操作、财务副作用或修改请求目标。

可证伪的交叉例子包括 `P/../../billing/subscriptions/id/cancel`（外层 cancel 优先）、`P/../../billing/redemptions`（内层 redemptions 首视图优先），以及 `/self/api/v1/billing/redemptions/../../usage/estimated-cost-summary`（成本清理视图虽命中 `P`，内层 redemptions 保留段视图优先）。把内层家族写成大写、重复斜线或反斜线时，应分别依上述实际首视图规则证明；entry-classifications 的反斜线不能被虚构为它的分隔符。redemptions 的编码 `%3F`/`%23` 尾缀，以及 17/18 层编码到**额外一轮**才显露内层家族的目标，要分别检验首视图可证明和不可证明的情况。仅因 `P` 与另一家族同在有限解码链里，不能绕过本节的首视图门槛。

## 处理优先级与响应

下表只针对已进入应用的合法 Go HTTP 请求；由 HTTP 解析器更早拒绝的畸形 request-target 不承诺应用错误格式。`Cache-Control: no-store` 覆盖所有应用响应，应用层拦截不发 `Location`，不得产生 301/302/307/308。判定不读取财务或 session 数据，也不得记录目标、cookie、Authorization、价格或模型内容。

| 已证明且未被上述兄弟 guard 优先认领的成本目标 | 在 `ServeMux` 前的处理 | 结果 |
| --- | --- | --- |
| 两个开关任一关闭；字面路径或上述畸形家族；任意方法 | 形状识别后立即停止，不查 self 身份/数据库，不触及 WebDir | 404、no-store；无 `Location`、无 `Allow`。不规定 404 正文格式。 |
| 两开关开启；字面 `P`、无 query、`GET` | 沿用现有 `requireSelfReleased(..., false)` 和摘要 handler | 读授权通过后才检查零正文并执行原有只读聚合；本合同不改摘要 JSON、快照、故障或最终会话重核。 |
| 两开关开启；字面路径 `P`、非 `GET`，含 `HEAD` | 先做同一读授权，再决定方法；沿用既有方法先于 query/body 校验的顺序 | 授权通过后 405，`Allow: GET`，无 `Location`、no-store；即使携带 query/body 也不成为可执行目标，不要求 HEAD 有 wire 正文。 |
| 两开关开启；已确认畸形家族，任意方法 | 先做同一读授权，再判路径非法 | 授权通过后固定 JSON 400 `invalid_request`，无 `Allow` 或 `Location`，no-store；未经授权沿用现有 401/403/503。 |
| 两开关开启；字面路径 `P` 的 `GET` 带裸 `?`/query 或正文 | 读授权之后按原“零 query、零正文”约束拒绝；不能调用聚合 | 固定 400 `invalid_request`、no-store；非 GET 仍按上一行原有方法优先级处理。 |

读授权先核可选 `Origin`：多个或异源/非法值为既有 403 `request_rejected`；随后才检查独立 self Cookie、当前有效会话与员工 active/已开通状态，失败为 401 `authentication_required`，存储故障为 503 `storage_unavailable`。因此错误 Origin 与缺少 Cookie 同时存在时可先返回 403。管理员 Cookie、员工模型 Bearer Key 和其他员工会话不能替代本人的 self 会话。此 GET 及所有边界错误的授权检查均**不要求**密码、CSRF 或 `X-Self-Request`；不得因方法或畸形路径误套写门禁。只有已证明且未优先归属兄弟 guard 的成本路径适用上表；邻居继续由原有路由自行处理，不能被本 guard 加上成本专属 404/400/405。

## 先红后修与验收门槛

原基线已用临时 data-dir/WebDir、动态回环端口、有效员工会话、原始 socket 请求目标和不跟随重定向的真实 TCP 请求完成邻居修前红：`GET P-other`、`GET P%2Dother` 得到成本专属 JSON 400 `invalid_request`，普通 `/self/api/v1/usage/obviously-other` 得到文本 404。该证据只证明邻居误捕获，不证明深层编码的工作量风险或本节新增的跨家族归属；后者仍须在主任务只读审核并明确放行后补修前红。继续使用临时目录和动态 `127.0.0.1:0`（绝不占用 8787）、带无秘密 marker 的 WebDir、脱敏原始目标和首个状态、`Location`、`Allow`、`Cache-Control`、Content-Type、错误码及 marker 是否外露作为证据。若新假设被修前行为证伪，先报告并暂停，不自行扩大路由范围。

获准实施后测试矩阵应覆盖关态 `GET/HEAD/POST/PUT` 的字面和已证明形状、开态 canonical/非法 query/body/方法与角色优先级、同源/异源 Origin、no-store/无重定向/精确 `Allow`，并验证 `%3Fx`、`%3Bextra`、`%2Eextra` 不落入 WebDir。要成对覆盖 `P-other`、`Px`、字面 `.extra`/`;extra`、编码 `-` 等邻居与 `/extra`、尾斜线、双斜线、反斜线、点段、大小写、编码字母/斜线/标点、双编码和第 16/17/18 层；另在 8192 字节附近测试已证明与不可判定的相邻目标。

交叉家族矩阵须以 `P/../../billing/subscriptions/id/cancel`、`P/../../billing/redemptions` 及反向 `/self/api/v1/billing/redemptions/../../usage/estimated-cost-summary` 为锚点，分别验证外层优先、内层首视图委派及保留段优先。对五个内层家族各造至少一个首视图交叉目标，并分别测大小写、重复斜线、反斜线；entry-classifications 的反斜线负例应保持成本归属。redemptions 的 `%3F`、`%23` 编码尾缀需按首视图证明确认归属；另造只有多解码一轮才显露兄弟家族的 17/18 层目标，证明它不会误委派。每个可委派例子用成本/兄弟开关相反的两组配置及有效会话辨认实际 owner，并检验被选 guard 未认领时的终止型成本兜底不会落入 `ServeMux`/WebDir；与预算边界、邻居例子一同检查首个 wire 响应无意外重定向。

不得只靠构造过的 `url.URL`、`httptest.ResponseRecorder` 或会自动跟随重定向的客户端证明 wire 行为。核对实际 `RequestURI`/`Path`/`EscapedPath`/`RawPath` 和 WebDir marker，确保未变更 `PathValue`、相邻 self API、原摘要零 query/body、金额/计数/价格冻结、存储失败与会话退出竞态；测试使用合成数据且不调用真实上游。实施开始后另补适用自动测试、build/lint，并把静态推断、修前红、修后绿和仍未验证事项分开报告。

## 来源、许可和发布界限

本合同由 CPA Cloud 项目规格与上列当前源码/测试的只读核对独立写成，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA、相邻参考仓库的实现或测试；既往研究意味着不能声称严格 clean-room。公开机制参考为 [Go `net/http.ServeMux`](https://pkg.go.dev/net/http#ServeMux)、[Go `http.Request`](https://pkg.go.dev/net/http#Request)、[Go `net/url.URL`](https://pkg.go.dev/net/url#URL)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 3986 §2.1](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1) 与 [RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)；这些只说明通用 URL/HTTP 机制，不证明本服务的动态行为。本文不引入第三方依赖、SDK、素材或许可证变更；Go 标准库沿用其 BSD-3-Clause 许可，既有依赖记录见[依赖许可清单](research/dependency-notices.md)。本次交叉家族合同澄清已记录，产品实施与验收尚未完成；实施、push、PR、GitHub Actions、native package、发布与部署均待主任务另行明确放行。
