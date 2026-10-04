# ID-05 / BILL-03 员工套餐目录 GET 路由形状边界合同

状态：2026-10-04，开发预览、合同先行。起点固定为已合并 `main` `c03c9f7f81ffbe5dde006607ce8cc055277f42a7`（tree `a8026de908fa2a1f46ea75cec25c991a6eabdae3`）。本文只规定未来一处默认关闭的本人目录**读取路由形状**修复；目前没有据此完成修前动态证伪、实现、测试、GitHub 编译或发布。业务语义仍由[套餐目录合同](employee-self-plan-catalog-contract.md)规定，身份来自[员工自助基础合同](employee-self-service-foundation-contract.md)；[购买 POST 边界合同](employee-self-plan-purchase-route-boundary-contract.md)及[订阅状态合同](employee-self-subscription-status-contract.md)各自拥有原路由。整体约束见[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)和[预览接口](preview-contract.md)。

## 要保护的入口和静态理由

本合同把 `P = /self/api/v1/billing/plans` 作为唯一目录路径。既有 `--employee-self-plan-catalog-enabled` 默认关闭，且启用要求员工自助总开关；本批不改 flag、前置条件或 capability。开启时原注册为 Go `ServeMux` 的 `GET P`，其模式**也匹配 `HEAD P`**；不能另加 HEAD handler、把 HEAD 当作错误方法，或把 HEAD 响应伪装为完整 GET 正文。目录实际成功仍需当前有效的 self 会话，并遵从原 `currency`、`limit`、`cursor`、正文和商业开关处理。关闭时 canonical GET/HEAD 仍是 404。

只读静态核对发现，[`self_service.go`](../internal/service/self_service.go) 只在目录开关开启时注册 canonical `GET P`；[`app.go`](../internal/service/app.go) 的守卫链在 `ServeMux` 外，但没有目录专属的路径守卫。Go 的 `ServeMux` 会在分派前为点段和重复斜线发出清理重定向。因此“开关关闭时不注册路由”本身不足以证明畸形目录目标不会先得到 `Location`；这仅是**待动态证伪的风险**，不是已观察到的本基线行为，更不是越权读取证据。不得把旧购买 POST 的修前红套用到 GET/HEAD 目录。

## 路径归属：只用别名证据拒绝

新增分类器只考虑 `GET` 和 `HEAD`。从服务器接收的 `RequestURI` 取第一个**字面** `?` 前的 path；`%3F` 不切断 path，`%23` 不形成 fragment。还应分别检查 `URL.Path`、`URL.RawPath`、从有界 `Path` 副本生成的转义视图；只有原 Path/RawPath 与派生转义视图都完整且限内时，才可比较原 `URL.EscapedPath()`。`Path` 已解码，而 `EscapedPath()` 可选有效 `RawPath`；不能以其中单一视图替代原 request-target，也不能相信构造测试里相互矛盾的 URL 字段。`RequestURI` 缺失或必需字段不完整时，不凭猜测把目标升级为 canonical。

只有真实 origin-form 的原始 path、完整 `Path` 和完整原 `EscapedPath()` 都逐字为 `P`，且 `RawPath` 为空或完整且逐字为 `P`，才算 **literal canonical**。`P?currency=USD` 与 `P?` 的 path 仍是 literal；query/正文是否合法继续由原目录 handler 决定。任何解码、清理或大小写变换得到的 `P` 都只能作为**拒绝候选**，绝不能改写 `RequestURI`、`URL.Path`、`URL.RawPath`、`PathValue` 或 query 后交给成功 handler。

候选需有已证明的**完整 `plans` 段**，或经有界拒绝视图清理后落在该段/其子路径：`P` 后必须是实际 path 终点、`/` 或 `\` 分隔符（可由限内百分号解码得到）。`P/`、`P/extra`、`P//extra`、前置重复斜线或点段、大小写变化、编码字母、编码 `/`/`\`、编码点段、多层 `%25` 以及清理到 `P` 的别名，都在能证明完整段时属于候选；开态不得让它们触发目录读取。`P%3Ffoo`、`P%23foo`、`P.extra` 没有完整分隔符，不凭文本前缀认领。`plansx`、`plans-other`、`plans%2Dother` 是明确的同级邻居；`/self/api/v1/billing/plan-purchase-quotes`、`/self/api/v1/billing/subscriptions`、其 ID 子路由、redemptions 以及其余 billing 路径也不能因局部相似被认领。若一个原目标跨越多个家族，例如先含 `P` 段再通过 `..` 清理到既有兄弟路径，**外层已有兄弟守卫先决定归属**；目录分类器只处理它们放行的请求。清理后是否等于 `P` 只可作为拒绝证据，不产生新的全局路由规则。

为使新增分类成本可审计，每个源路径视图只查看前 **8192 字节**，每条有界路径最多做 **16 轮** `url.PathUnescape`；这个数值沿用当前项目购买守卫的工程预算量级，并非 Go 或 HTTP 协议自带限制。对 `RequestURI` 最多检查到第 **8193 个路径字节**：若第 8193 个字节恰是字面 `?`，前 8192 字节可证明为完整 path；若仍是路径字符，哪怕下一字节就是结尾，也必须标记不完整。更早遇到 `?` 即结束 path，后续很长的 query 不占路径预算。`URL.Path`、`RawPath` 先按各自字节长度界定 complete/incomplete，再处理；不能对超长 `RawPath` 先调用无界原 `EscapedPath()`。从已截断的 `Path` 派生转义视图，结果也截至 8192 字节并继承不完整标记。每轮解码、转义或清理若丢失尾部，该不完整性继续传递，不能把预算末端当成真正段终点。只有界内已看到完整 `plans` 段与其边界，或完整清理结果可证明归属时才能拒绝；必须依赖第 8193 个路径字符、第 17 次解码或未见尾部才成立的目标是**不可归属**，交给原链而非新建目录 400/404。全局 URL 大小限制不在本批范围内。

## 顺序与可观察响应

新增目录守卫将来应在**所有已有 self 兄弟守卫及购买 POST 守卫内侧、仅在 `ServeMux` 外侧**。因其只看 GET/HEAD，原购买 `POST /self/api/v1/billing/plan-purchase-quotes` 和 `POST /self/api/v1/billing/subscriptions` 的守卫、写授权、重放和错误优先级完全不变。canonical `POST P`、`PUT P`、`DELETE P` 等非 GET/HEAD 也直接交原链，不由本守卫制造目录专属 404/400/405 或 `Allow`；原 `ServeMux`、WebDir 和既有兄弟路由仍决定它们的首个响应。目录开关不能改变这些方法或邻居的 owner。

| 已解析且归属本目录的请求 | 必须保持/新增的行为 |
| --- | --- |
| 目录或自助总开关关闭；canonical 或已证明畸形的 GET/HEAD | 在 Origin、Cookie、数据库、`ServeMux` 清理、WebDir 之前直接 404；不要求 JSON 错误正文。 |
| 开启；literal canonical GET/HEAD | 原样进入现有 `requireSelf(..., false)` 和目录 handler。Go GET 模式原有的 HEAD 匹配、服务器对 HEAD 正文的抑制、原 query/body/cursor/商业读取顺序保持不变。 |
| 开启；已证明畸形的 GET/HEAD | 先沿用**读**授权：可选 Origin 必须唯一且同源，随后为当前有效的本人 session Cookie、未过期且 active 的员工；再固定 JSON `400 invalid_request`。Origin 失败先于 Cookie 为 403 `request_rejected`；缺/坏会话为 401 `authentication_required`，会话存储失败为 503 `storage_unavailable`。不要求写请求的 `X-Self-Request`、CSRF 或购买 `selfGate`，不解析目录 query/正文/游标，不读取财务目录。 |
| 邻居、不可归属或任何其他方法 | 新目录守卫透传；各原 owner 的开关、身份、方法、重定向和响应头顺序不由本合同重定。 |

上述状态只约束 Go 服务器已交给应用的合法请求目标；解析器在应用之前拒绝的非法 `%` 转义不要求项目 JSON。守卫**自己**产生的 404/400，以及其开态畸形分支经现有 self 读授权产生的 401/403/503，均不得有 `Location`、`Allow`；应用外层 `requestMiddleware` 对这些响应保持 `Cache-Control: no-store`。literal canonical 及非目录 owner 仍由原实现决定 `Location`/`Allow`（例如通用 `ServeMux` 方法或清理行为），不能为达到本守卫断言而重写它们；走本服务中间件的响应仍有原 no-store。HEAD 的 wire 响应不含正文，但状态、相关头和错误优先级须与对应 GET 保持一致，不用空正文掩盖意外重定向。

管理员 Cookie、员工模型 Bearer Key 和匿名请求不是 self 读会话。关态 404 早于这些身份及 Origin 失败；开态畸形形状的认证优先于 400。canonical 保持原 `requireSelf` 的先后顺序，不把“先形状后认证”的新增拒绝策略倒灌到业务 handler。新守卫不触发 bcrypt、限流、财务事务、写入、上游请求或日志中敏感字段；允许原 self 会话校验查询持久状态。目录成功、价格、分页与错误投影一律不改。

## 后续先证伪、再实现的门槛

根任务审完本地 doc-only 提交之前，不运行修前红、不启动服务/浏览器/测试、不改 Go/Web/脚本/测试、不 push、不开 PR/Ready、不手动触发 GitHub CI 或 native 包工作流。后续若获单独放行，先从**精确旧 main `c03c9f7...`** 的固定源码构建旧可执行文件，使用隔离 data-dir/WebDir、至少两个真实随机且非 `8787` 的回环监听端口；以原始 TCP 发送 origin-form 目标，客户端**不自动跟随重定向**。记录每个请求的首个 wire status、`Location`、`Allow`、`Cache-Control`、Content-Type、脱敏错误码与 WebDir marker，并在受控 Go 入口捕获同一请求的 `RequestURI`、`Path`、`RawPath`、`EscapedPath()`；不能把按规范推断的字段说成实际观测。所有进程结束后核对无遗留。

修前矩阵至少覆盖目录 flag 关/开 × 匿名/有效 self 读会话 × GET/HEAD，目标包括 canonical `P?currency=USD`、`/self/api/v1/billing//plans`、`/self/api/v1/billing/./plans`、`P/extra`、大小写/编码别名及 `plansx`、`plans-other`、`plans%2Dother`，并记录 POST P、购买 Q/S 与 GET 订阅路径的邻居对照；Origin 错/重复、管理员 Cookie/模型 Key、长 query、预算边缘、多层编码作为扩展证据。只有实际观察到至少一条可复现的**首个**不合约重定向，并保留对应开关、身份、方法对照，才可据此说明旧基线缺口；若旧行为、Go 字段或外层兄弟优先级与本文假设不符，应停下向根任务报告，不扩大归属或先写修复。静态推测和 `httptest` 单独都不算修前 wire 证明。

获准实现后，以独立撰写的分类单测和真实 wire 集成测试交叉覆盖上述矩阵、HEAD 无正文、开态 Origin/会话优先级、关态不进认证/DB、canonical 原 query/正文错误、wrong-method 原 owner、兄弟前后顺序、无 `Location`/`Allow`/有 no-store、无 WebDir 内容及财务读写。预算要专测 8191/8192/8193 字节 path、恰在边界的 `?`、长 query/`RawPath`、第 16/17 层才显露完整段、截断后伪终点，以及编码问号/井号和 percent dash 邻居。不可归属只能走原 owner。再分别报告定向 Go 测试、race/vet/build 与固定 HEAD 的 GitHub CI；不能把本合同、旧 PR67 的证据或未来计划写成已测试的结果。

## 来源与许可

本文为 CPA Cloud 基于自有功能规格、本仓当前路由的只读检查和公开协议机制独立拟定；没有复制、翻译、移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考树，也不称严格 clean-room（此前研究过参考代码）。2026-10-04 核对的公开资料：[Go `ServeMux` 模式及清理](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.RequestURI`](https://pkg.go.dev/net/http#Request)、[Go `URL.Path`/`RawPath`/`EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 3986 编码与保留字符](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1)、[RFC 3986 点段处理](https://www.rfc-editor.org/rfc/rfc3986.html#section-5.2.4)、[RFC 9111 响应 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。这些是机制依据，未替本项目给出动态测试结论。本文不引入源码、SDK、依赖或素材；Go 标准库仍为 BSD-3-Clause，现有第三方组件各保留其自身许可与[依赖许可记录](research/dependency-notices.md)。未来新增源码须显式记载来源。
