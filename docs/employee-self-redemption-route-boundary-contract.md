# ID-05 / BILL-04 员工本人兑换写路由的多层编码边界契约

状态（2026-10-04）：**仅合同、本地待审；本边界增量尚未实现或动态验收。** 基线是已合并 `main` 的 `cb39f1f7b763836a2441095ab4697a31ef48ff51`，tree `34e83c608505811076c4310def4c1a99ceebe730`。本合同只收窄既有默认关闭的本人兑换写路由在 Go `ServeMux` 之前的 request-target 形状判定，不增加兑换能力。它以[本人兑换契约](employee-self-redemption-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[预览接口](preview-contract.md)、[产品计划](product-plan.md)和[独立实现规则](independent-implementation.md)为项目事实来源；其中本人兑换合同的路径、身份、写门禁、幂等和财务语义继续有效。

## 待证实的静态风险与唯一范围

现有 [`self_redemption.go`](../internal/service/self_redemption.go) 的 guard 在 [`app.go`](../internal/service/app.go) 中包住 `ServeMux`，但形状分类只取已经解码一次的 `r.URL.Path`，再做大小写折叠、`path.Clean` 和保留段的词法判断。`r.URL.Path` 可能仍含 `%2f` 之类的文字：例如原始目标 `/self/api/v1/billing/redemptions%252fextra` 经一层 URL 解析后可能成为 `.../redemptions%2fextra`。它是否绕过该 guard、落入 [`self_service.go`](../internal/service/self_service.go) 的 `/self/api/` fallback 404，**目前只是源码推断，不是已测试的漏洞结论**。不得以单元构造请求或自动跟随重定向的客户端把它写成实证。

未来任何实现必须先在此精确基线做真实 HTTP、动态 `127.0.0.1:0`、客户端**禁止跟随重定向**的红测试：在功能开启、有效 self 写身份和头齐全时，至少一个多层编码 shape 应按本合同期待 JSON 400，而旧服务实际给出不同结果；同时记录原始目标、状态、`Location`、`Allow`、`Cache-Control`、内容类型和脱敏错误码。如果实际旧服务已满足期待，先向主任务报告证伪及原始结果，**停止本批，不自行扩展到其他路由、退款历史或 UI**。本次合同提交不运行该测试，不启动默认 8787，不修改产品代码。

本批不加 flag、配置、capability、API、响应字段、UI、CLI、DDL、财务写入、密码策略、provider 或 worker；不改管理员兑换、本人兑换码的业务/账本事务、既有 query/JSON/body 约束及其他 self 路由。只允许把已属于本人兑换路径家族的异常 request-target 在 `ServeMux`、WebDir、fallback 和业务处理前拦住，不能把编码别名解开后转发到 canonical POST。

## 路径家族与原样 canonical

唯一可执行业务的目标是字面精确、无 query 的 origin-form `POST /self/api/v1/billing/redemptions`。路径每个 ASCII 字节、大小写、斜线数量和末尾都必须精确；`URL.Path` 与 `URL.EscapedPath()` 也须与该字面路径相同，原始 request-target 不能藏有编码别名。`RawPath` 是可选编码提示，不能因它为空就排除检查，也不能把它当作唯一真相。已有 canonical POST 继续由原 `requireSelfRedemption` 和 `selfRedeemCode` 执行；新分类器不读正文、不解析 query、不检查密码、不触发财务读取/写入。

“本人兑换 shape”包括两路**仅用于拒绝**的视图，而不是新的路由匹配或 URL 等价定义：

1. 清理/折叠视图会落到字面 canonical 或其路径段后代，例如大小写变体、重复 `/`、末尾 `/`、编码的路由字母或分隔符，以及清理后才显现的目标。
2. 保留段视图含已锚定的 `redemptions` 路径段及其后代，即使后面的 `.`、`..`、重复分隔符或反斜线使普通 `path.Clean` 离开目标；不能让 `/redemptions/..` 或编码版本被 `ServeMux` 重定向，亦不能把 `/redemptions/extra` 送到普通 fallback。

判定锚点是 `/self/api/v1/billing/` 下的完整 `redemptions` **路径段边界**，允许在拒绝视图中用 ASCII 大小写折叠及分隔符识别；`redemption-credits`、`redemptions-other`、其他 `/billing/` 路径和相邻订阅/钱包路由绝非此家族。原始形状与必要的副本视图须覆盖 `RequestURI` 的原始 request-target 路径（只在**字面** `?` 划开 query）、`URL.Path`、`URL.EscapedPath()` 和可用的 `URL.RawPath`；对 `%3f` 不得先解码成 query 分隔符。测试须记录四者在真实请求下的值，不能假定 `RawPath` 总非空，或假定 `Path` 能区分原始 `/` 与 `%2f`。

拒绝视图还须覆盖 `%2f`/`%252f`/更深层编码的 `/`，`%5c`/`%255c` 的反斜线，`%2e`/`%252e` 的点段，编码的路由字母、大小写变体、双斜线、尾斜线及其组合；十六进制大小写均可出现。分类预算固定为**每个视图最多读取其前 8192 字节、最多 16 轮整串 `PathUnescape`**，遇到无变化或非法转义立即停止；不能把截断处当作路径段结尾，也不能生成第 17 个解码路径。对长度不超过 8192 字节的视图，轮数耗尽后只可对残余字节做一次 O(n)、固定 canonical 路径 ASCII 字节与完整段边界的语法证明（字面字节或 `%` 后跟零个或多个 `25` 再跟两位十六进制字节），不再解码整串、清理新路径或转发别名。完整 `redemptions` 段及末尾、`/`、反斜线、`?`、`#` 边界必须得到证明；`-other`（包括深编码的 `%2d`）、`redemptionsx`、`redemption-credits` 和其他 `/billing/` 路径不得误判。若在此预算内无法同时证明应拦的本家族与应放的邻居，先向主任务报告设计阻断，不能以全局 `/billing/` 拦截代替。

对超过 8192 字节的视图，只有**本家族完整 stem 和其分隔符边界都在已读取的前缀内得到证明**，才按异常 shape 拒绝；仅见到 `/billing/`、`redemptions` 的部分字节或尚未证明边界的 `%`，均不能据此声称归属。若本家族与 `redemptions-other` 等邻居的区分字节位于第 8192 字节之后，两者都不由此 guard 声称归属，交由现有通用 HTTP/`ServeMux`/fallback 处理。本合同不保证这种超限尾部经过 self 写门禁、返回固定 JSON 400，或没有通用 HTTP 层重定向；这不构成“所有编码绕过已封闭”的声明。此有限保证不放松原双编码缺口及 8192 字节内已证明 shape 的门禁要求。分类只返回“此家族且是否原样 canonical”，**从不**改写 `RequestURI`、`URL.Path`、`URL.RawPath`、`RawQuery`、`PathValue`、请求方法、正文或上下文，也不把解码后的副本交给下游。RFC 3986 对 URI 的通常处理不允许重复解码同一串；这里的多层副本仅作拒绝性威胁识别，不声称这些 URL 在协议上等价。

## 顺序与唯一响应面

以下只规定已经由 Go HTTP 解析器构造并进入应用的请求；解析器在此之前拒绝的非法原始 request-target 不承诺应用 JSON 或应用响应头。外层通用中间件继续设置 `Cache-Control: no-store`；本路由边界不得泄露码、密码、cookie、CSRF、请求目标或内部错误到日志/响应。

| 状态与原始目标 | `ServeMux` 前顺序 | 应用结果 |
| --- | --- | --- |
| 本能力关闭；canonical、上述两路 shape，**任何方法** | shape 判定后立即拒绝；不进入 WebDir、fallback、self 身份或数据库 | 404，`Cache-Control: no-store`，无 `Location`、无 `Allow`；不承诺 404 正文格式。 |
| 本能力开启；字面 canonical、无 query、`POST` | 原样交给既有 `requireSelfRedemption` → `selfRedeemCode` | 沿用旧写入口和业务结果；本边界不新增成功/错误码。 |
| 本能力开启；字面 canonical、无 query、非 `POST`（含 `GET`、`HEAD`） | 先按既有 `requireSelf(..., false)` 做可选 Origin 与有效 self session 读授权；之后才判断方法 | 授权通过才 405，`Allow` **恰为** `POST`，无 `Location`，no-store；未授权先按既有 401/403/503。`HEAD` 的 wire 正文可由 HTTP 语义抑制。 |
| 本能力开启；本家族的任何非 canonical shape，**任何方法**（含 `POST` 与非 `POST`） | 先按既有 `requireSelfRedemption(..., true)` 做可选 Origin、有效 self session、必需同源 `Origin`、`X-Self-Request: 1`、持久 CSRF；再经现有 peer/员工失败限流和失败计数，最后才报路径错误 | 门禁通过为固定 JSON 400 `invalid_request`，no-store，无 `Location`、无 `Allow`；前置拒绝仍按现有 401 `authentication_required`、403 `request_rejected`、429 `login_limited` 或存储故障 503。不得先向匿名/错误 Origin/CSRF 请求透露路径或方法错误。 |

上表的“本家族 shape”只指按上述固定预算**已证明归属**的目标；超限且区分字节在已读前缀之外的目标不适用该表的 404、写门禁或固定 400 保证，亦不得为套用该表误拦邻居。

现有写门禁的精确优先级是可选 `Origin` 语法/同源检查、self Cookie 与活动会话、必需的单一 `Origin` 和 `X-Self-Request`、唯一 CSRF、随后失败限流；因此同时缺身份且 Origin 错的请求可先得 403，不能把上表误读成一律先 401。畸形**非 POST** 与畸形 POST 一样走写门禁后 400，不能因为 HTTP 方法不是 POST 就绕到可枚举的 405；只有**真正字面 canonical** 的非 POST 使用读门禁。canonical 上的裸 `?`/非空 query 仍按现有 guard/handler 的畸形应用请求规则，在写门禁后 400；正文长度、JSON 字段、密码、兑换与幂等仍由旧路径负责，不能借本批改变它们的顺序或响应。所有应用层 shape 拒绝都不能发 301/302/307/308，不能设置 `Location`，不能让敏感 POST 因客户端跟随 redirect 而换方法或目标。

## 红测试、回归和放行门

合同经主任务只读审核并明确放行后，先独立编写真实 HTTP no-follow 红测试，再考虑最小实现。测试使用临时数据目录和动态 loopback 端口（绝不占默认 8787），记录所测基线 SHA/Go 版本；真实 socket 发出的原始目标与收到的首个响应均要保留脱敏证据，不能只靠 `httptest.ResponseRecorder`、`mux.Handler`、浏览器自动重定向或构造过的 `url.URL` 推论。至少一条双编码 `POST /self/api/v1/billing/redemptions%252fextra` 在开态且完整写门禁下先红；若该假设证伪，按上文停止并报告。

实施后同一 no-follow 矩阵至少覆盖：关闭时 canonical/清理视图/保留段视图 × `POST`、`GET`、`HEAD`、`PUT` 的 404/no-store/无 `Location`/无 `Allow` 与零 self/财务读取；开态 canonical POST 不漂移、canonical 非 POST 的读授权后 405/精确 `Allow: POST`；开态畸形 shape 的匿名、管理员 Cookie、Bearer Key、错误/缺失/多重 Origin、缺失/错误 `X-Self-Request` 和 CSRF、已限流、完整门禁下 400，确认鉴权与失败限流优先。对 HEAD 断言状态与头，不要求实际响应正文。包括 `%2f`、`%252f`、三层及更深编码、`%5c`/`%255c`、点段、编码路由字母、大小写、`//`、尾斜线、编码问号、query 和过预算形状；测试须区分原始深度 16/17/18 的家族与邻居、大小写及多字母/分隔符编码、接近 8192 字节的已证明边界，以及超限且区分字节位于第 8192 字节之后的不可归属成对目标。对已证明的应用层 shape 拒绝禁 redirect，400 使用固定脱敏 JSON，不回显目标；对上述不可归属目标只断言不误称家族，不套用固定 400/404 期待。

另须同一进程断言旧账本/兑换事实、账户、码使用量和密码状态没有因 shape 探测改变；邻居 `redemption-credits`、`redemptions-other`、其他 `/billing/`、订阅及网页各自保持原有归属和开关，不被此 guard 捕获。原 canonical POST 的参数、密码、重放、成功/冲突和财务故障回归仍通过。实现开始后补自动化测试及相应构建/lint，再分别报告“静态推断、先红证据、修复后证据、未验证事项”；本合同本身不声称测试通过、真实 provider 或生产部署。

## 来源、许可与发布边界

本文为 CPA Cloud 自行撰写的功能规格，依据本仓库上述合同与现有源码的只读核对；没有复制、翻译或移植任何参考/归档项目代码、测试、迁移、素材或文档，也不宣称严格 clean-room。2026-10-04 查阅的公开协议资料：[Go `net/http` 的 ServeMux 匹配与清理](https://pkg.go.dev/net/http#ServeMux)、[Go `net/http.Request.RequestURI`](https://pkg.go.dev/net/http#Request)、[Go `net/url.URL` 的 Path/RawPath/EscapedPath](https://pkg.go.dev/net/url#URL)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 3986 百分号编码与解码时机](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1)、[RFC 9111 响应 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。Go 标准库保留其 BSD-3-Clause 许可；本合同无新增第三方依赖、SDK 或素材，既有依赖许可见[项目记录](research/dependency-notices.md)。本批仅新增此文档并本地提交；未经另行授权不 push、不建 PR、不触发 CI/安装包、不部署。
