# ID-05 / BILL-03 本人一跳续购关联的路由边界补充契约

状态：2026-10-04，开发预览，合同先行。精确基线是已合并 `main` `155eb6ed216161548d99ca4b079f08596022514f`（tree `eacca015a7546e1321b67c2ba927914e26200d2e`）。本文只规定下一实施批的路由兼容目标；本次合同提交不代表修复、测试、浏览器验收或新提交的 CI 已通过。它补充[本人一跳续购关联原契约](employee-self-subscription-renewal-links-contract.md)，并沿用[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[开发预览接口](preview-contract.md)、[本人购买时记录路由边界](employee-self-subscription-purchase-snapshot-route-boundary-contract.md)与[本人月度即时续购](employee-self-monthly-renewal-contract.md)。原契约已有默认关 404、畸形路径不重定向、有效 self 会话 400、规范错方法 405/`Allow: GET` 和只读交易要求；它没有逐字锁定**所有**畸形形状在关态的身份/方法优先级，亦未详列 ServeMux 清理前的兄弟路由归属。下文是兼容补充，不能倒称旧实现已满足全部新增矩阵。

## 仅此入口及当前缺口

唯一目标是既有 `GET /self/api/v1/billing/subscriptions/{id}/renewal-links`。`--employee-self-subscription-renewal-links-enabled` 仍为独立、默认关闭的开关；显式开启仍只要求员工自助总开关和本人订阅状态开关，缺前置必须在持久状态改变前拒绝启动。既有 `features.employee_self_subscription_renewal_links`、成功 JSON **恰好** `subscription_id`、`predecessor_id`、`successor_id` 三字段、本人直属 owner/Key 与资源子账户隔离、先归属后验链、坏链 503、单一只读事务、商业执行关闭后历史可读、网页显式单条点击和晚响应保护均保持原义。不得新增 flag、capability、JSON 字段、UI/CSS、CLI、DDL、财务写入、provider、支付或 worker；不得扩至月度续购 POST、top-up 历史或其他订阅操作。

在该精确基线，`internal/service/app.go` 的续购关联守卫仅在 flag 开启时安装（约 524–526 行），而 `internal/service/self_subscription_renewal_links.go` 的形状判断以固定前缀和 `Contains` 子串为主（约 15–21 行）。`ServeMux` 可在路由处理前对点段和重复斜杠发清理重定向；`GET` 模式还匹配 `HEAD`。据此，关态的前缀清理歧义可能越过本功能的 404 边界，子串也可能误收兄弟/未知路径。这是待实施前实测的具体风险，不把阅读代码当作当前精确基线已经复现 307 的证据。

## 拒绝专用的形状分类与兄弟归属

规范路径须保留完整固定前缀、一个按既有规则有效的非空不透明单段 ID（至多 256 UTF-8 字节）、完整的字面量 `renewal-links` 操作段，且无尾段；ID 转义应规范且无歧义。路径段边界必须完整匹配，不以 `Contains`、相似前后缀或大小写折叠识别操作。不能仅因合法 ID 含无歧义编码字节就拒绝。空 ID、点段、重复斜杠、解码斜杠/反斜杠、双编码分隔符、额外段、末尾斜杠、编码操作名或可产生别名的双编码均不是成功处理器的另一种拼写。

“renewal-links 形状”包括订阅路径下在原始、转义及有界解码视图中可按**独立完整段**确认本操作的请求，以及若交给 `ServeMux` 清理会重定向至该规范入口的歧义请求。分类应在既有路由/WebDir 之前进行；解码次数和处理字节/段数须固定有界，至少覆盖一次与双重编码，清理视图只用于判断是否拒绝。不得修改 `URL`、`RawPath`、`PathValue`、方法或请求目标，也不得把别名改写后转发成功处理器。编码分隔符不得凭 ID 中的文本凭空制造一个可被本守卫认领的操作段。Go HTTP 解析器先行拒绝的非法 request-target（例如 `%ZZ`）是传输层 400，不要求本应用的 JSON/no-store。

只认第一个实际操作段或会清理到本入口的明确歧义；`/id/renewal-links/extra` 是本入口畸形尾段，但 `/id/unknown/renewal-links` 不能仅因后段同名被认领。`renewal-links-extra`、含该字样的 ID、兄弟操作的尾段及一般未知路径不得被子串误收。尤其保留 `renew`、`renewal-quotes`、`cancel`、`one-shot-renewal`、`purchase-snapshot` 的原开关、守卫、权限和响应；本守卫不得借“安全清理”扩大其中任何入口的可达性。若一个异常原始路径确会被清理重定向到规范续购关联入口，应在重定向前拒绝，而非让浏览器跟随至新资源。

## 开关、读授权与响应优先级

守卫无条件参与分类，即使本功能 flag 关闭；它不得查目标存在性、会话业务资料、订阅、账本或开启财务事务。所有以下**应用**响应均为 `Cache-Control: no-store`，无 `Location`；仅真正的 405 带精确 `Allow: GET`。`HEAD` 验证状态与头即可，不以可见响应体为断言。

| 本功能状态与路径 | 处理优先级 | 应用结果 |
| --- | --- | --- |
| 关闭；规范路径或本入口形状 | 在身份、Origin、DB、方法分派和 ServeMux 清理之前 | 任意方法均 `404`，无 `Allow`/`Location`，不泄漏员工或方法信息 |
| 开启；规范路径 `GET` | 原 self 读授权后交给原处理器 | 原三字段成功、query/body 校验、404/503 及交易语义不变 |
| 开启；规范路径 `HEAD`、`POST`、`DELETE` 等非 GET | 先沿用 self **读**授权与可选同源 Origin 校验，不进入写侧认证或财务处理 | 有效 self 员工为 JSON `405 method_not_allowed`，`Allow: GET` |
| 开启；非规范但属本入口形状，任意方法 | 同样先经 self 读授权；路径错误先于方法错误 | 有效 self 员工为 JSON `400 invalid_request`，无 `Allow`，不进入业务/财务读取 |

开启态的匿名、过期/撤销/停用员工、管理员 Cookie 或员工模型 Bearer Key 不能绕过 self 会话；有问题的 Origin 继续遵守原同源读策略。其现有 401/403 授权错误先于表中 400/405，且不因错误路径读取订阅归属或关联。不能把错方法误升格为写侧密码/CSRF/`X-Self-Request` 入口。规范 GET 的裸 `?`、query、body、缺失/非本人、坏链与存储故障仍归原处理器，不在守卫中复制或改序。

## 合同阶段及未来验收门槛

本轮**只新增并本地独立提交本文**，检查相对链接和 `git diff --check`；根任务全文只读审阅并明确放行前，不修改 Go/网页/测试，不 push、不开 PR、不触发 CI，也不启动服务或浏览器。实施阶段的第一步必须在当时精确基线上用不跟随重定向的 `httptest` 和随机非 `8787` 隔离真实进程（启用 `WebDir`）锁住前缀双斜杠、点段等原始状态、`Location`、`Allow`、no-store，先红后绿。若预期 307/`Location` 在该基线不成立，先报告根任务，不套用假设修复。

放行实施后的自有表驱动测试至少交叉覆盖关/开、规范/畸形、`GET`/`HEAD`/`POST`/`DELETE`；原始、单/双编码斜杠与反斜杠、`.`/`..`、双斜杠、前缀清理、空 ID、extra/tail、编码操作名、合法 ID 的无歧义转义，以及规范 GET 的裸 `?`/query/body。分开验证匿名、管理员 Cookie、Bearer Key、有效/失效 self、停用员工及 Origin；不同兄弟 flag 组合与一般 unknown 路由不得被抢。三字段、直属归属、根/中/末一跳、商业关历史、坏链 503、单快照只读与零财务副作用要做回归，不能以纯路径测试替代；拒绝请求须证明零业务财务读/事务，不把认证自身的持久读取误称为零 DB 访问。

实施批还需定向 Go 测试、`go vet ./...`、Web typecheck/test/build、`git diff --check`、随机非 `8787` 真进程及真实 Chrome 桌面/390px 的显式点击和晚响应回归。运行、跳过、计划须分别报告；根再次只读审阅后方可按独立批次提交/推送 Draft PR。精确新 HEAD 的 GitHub CI、普通非浅 tracked-clean 固定 Git 源/二进制与双随机根黑盒 G 验收须重做，不沿用本次合同或上一 PR 的通过记录；Ready、合并、部署另行授权和验证。保护本工作树原四类未跟踪文件及主目录受保护资料，不使用真实 `8787`、真实凭据、参考树、部署、tag、发布或 native package workflow。

## 来源与许可

本文由上述 CPA Cloud 自有功能规格、精确 `main` 的自有源码和自行设计的路由拒绝矩阵撰写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树的代码、测试、迁移、素材、文档；此前曾阅读参考资料，因此不宣称严格 clean-room。2026-10-04 核对的公开技术依据：[Go `ServeMux` 的方法模式、逐段转义匹配与清理重定向](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.PathValue`](https://pkg.go.dev/net/http#Request.PathValue)、[Go `URL.EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)和[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。404/400/405、认证和开关的具体优先级是本项目的兼容要求，不是 Go 标准库承诺。本文不引入 SDK、依赖、素材或许可变更；既有 Go 标准库及第三方依赖保留各自许可证，后续新增源码/测试须注明本合同和实际公开来源。
