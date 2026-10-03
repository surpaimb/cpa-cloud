# ID-05 / BILL-03 员工本人月度即时续购路由边界补充契约

状态：2026-10-04，开发预览、合同先行。精确基线是已合并 `main` `c344e89c4adffac773eadbb7bae53a580bb88bd2`（tree `bd3b396a05f26978c8ea2770390b1903650e2300`）。本文仅规定下一实施批的兼容目标；本次文档提交不代表路由修复、先红复现、测试、浏览器验收或新提交的 CI 已通过。它补充[员工本人月度即时续购原契约](employee-self-monthly-renewal-contract.md)，沿用[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[本人钱包余额](employee-self-wallet-balance-contract.md)及[开发预览接口](preview-contract.md)，并须保持[续购关联](employee-self-subscription-renewal-links-route-boundary-contract.md)、[取消订阅](employee-self-subscription-cancel-route-boundary-contract.md)、[一次性预约](employee-self-one-shot-route-boundary-contract.md)和[购买时记录](employee-self-subscription-purchase-snapshot-route-boundary-contract.md)的兄弟路由归属。

原契约已规定默认关闭时两路 404、严格路径与有效 self 会话下的畸形语法 400、精确路径的其他方法 405，以及现有报价/提交业务；但没有逐字锁定**全部**清理/编码形状在关态的身份与方法优先级、ServeMux 和兄弟守卫之前的归属，也没有完整列出畸形路径按 POST/非 POST 分别使用写/读授权的矩阵。下文是新增兼容约束，不倒称当前精确基线已经满足。

## 唯一范围与待实测风险

唯一目标是现有的 `POST /self/api/v1/billing/subscriptions/{id}/renewal-quotes` 和 `POST /self/api/v1/billing/subscriptions/{id}/renew`。独立 `--employee-self-subscription-renewal-enabled` 仍默认关闭；显式开启仍要求 `--employee-self-service-enabled`、`--employee-self-subscription-status-enabled`、`--employee-self-wallet-balance-enabled` 三个现有前置开关，缺一须在持久状态改变前拒绝启动。现有 `features.employee_self_subscription_renewal`、网页两步确认与晚响应保护、CLI/启动选项、报价 token/purpose/五分钟规则、提交时当前密码及精确重放优先级、当前商业执行门禁、单一财务事务及错误脱敏全部保持原义。不得新增 flag、capability、成功 JSON 字段、UI/CSS、CLI、DDL、资金写入、provider、支付或 worker；本补充不开放任意月度购买、自动续费或其他订阅操作。

在此基线，`internal/service/app.go` 约 524–526 行只在续购 flag 开启时安装 `selfMonthlyRenewalRouteGuard`，且该守卫位于多个既有兄弟守卫内侧；`internal/service/self_monthly_renewal.go` 约 51–117 行以固定订阅前缀及后缀/包含判断识别形状。前缀重复斜杠或点段可能在守卫辨认前被 `ServeMux` 清理、重定向，编码视图也可能与兄弟守卫竞争。Go 标准库具有相应清理行为，但**当前基线出现 307 和 `Location` 仅是待实测风险**，不能把静态阅读写成已复现事实。实施第一步须按下文先红探针实证；若预期重定向不成立，先报告根任务，不套用假设修复。

## 两个完整操作段、拒绝视图与兄弟归属

规范路径须保留完整字面前缀、一个按现有 `selfMonthlyRenewalPath` 规则有效且不超过 256 UTF-8 字节的非空不透明单段 ID，以及恰好一个**完整字面量** `renewal-quotes` 或 `renew` 操作段；不带尾段、末尾斜杠或路径别名。ID 的无歧义规范转义可保留，不能仅因合法 ID 含编码字节就拒绝；空 ID、`.`/`..`、解码后斜杠/反斜杠/百分号、非规范或双编码分隔符、编码操作名、重复斜杠、额外段和可产生别名的拼写均不得进入原成功处理器。大小写不折叠，`renew` 不按 `renewal-quotes`、`renewal-links` 或相似后缀的子串匹配。

“月度续购形状”是仅供**拒绝**的分类：订阅路径下可在原始 request-target、`URL.EscapedPath()`、`URL.Path` 与有界逐段解码视图中以独立完整段确定这两种操作的请求，以及若交给 `ServeMux` 会清理重定向到其中一个规范入口的明确歧义请求。实现须保持字面斜杠段边界，对各段至多作覆盖一次与双重编码的固定有界解码；处理字节数和段数亦须固定有界。清理视图仅决定是否提前拒绝，**不得**清理或改写传给既有处理器的 `URL`、`RawPath`、`PathValue`、方法或 request-target，也不得把任何别名转发为成功 POST。ID 内 `%2F`/双编码斜杠解出的文本不能凭空制造一个操作段。Go HTTP 解析器在应用接收前拒绝的非法 request-target（如 `%ZZ`）只按传输层处理，不要求应用 JSON/no-store。

分类只认第一个可确定的实际操作段，或字面清理确会重定向至本入口的明确情形；`/id/renewal-quotes/extra` 和 `/id/renew/extra` 是本入口的畸形尾段，`/id/unknown/renew` 不能仅凭后段同名被认领。`/id/renewal-links`、`/id/cancel`、`/id/one-shot-renewal`、`/id/purchase-snapshot`、`/id/renewal-quotes-extra`、ID 中包含 `renew` 文本及一般未知路径仍归各自旧 flag、guard、handler 或兜底。对跨视图可能产生不同兄弟操作的路径，须在实际原始段、字面清理目标与有界编码证据之间维持明确归属，不以宽泛尾段搜索抢占兄弟，也不让外层兄弟守卫的解码清理先吞掉已明确属于本入口的编码畸形。不同兄弟 flag 组合不得扩大任何一方的成功可达性。

## 开关、授权和响应优先级

即使本功能 flag 关闭，分类也须无条件置于 `ServeMux`/`WebDir` 清理及可能抢占同一形状的兄弟守卫之前；分类本身不查订阅、owner、计划、报价、钱包、账本，不开启财务事务，不验证当前密码。以下**应用**响应均为 `Cache-Control: no-store`、无 `Location`；只在真正的 405 返回精确 `Allow: POST`。`HEAD` 仅断言状态和头，不要求可见响应体。

| 状态与路径 | 授权、分派顺序 | 对通过相应授权的请求 |
| --- | --- | --- |
| flag 关闭；两路规范路径或本入口形状 | 在身份、`Origin`、`X-Self-Request`、CSRF、DB、方法及 ServeMux 清理之前 | 任意方法均 404，无 `Allow`/`Location`；不泄漏员工、资源或哪一路存在 |
| flag 开启；规范 `POST` 任一路 | 沿用原 self **写**授权，原样委托原报价或提交处理器 | 原报价/提交的 query、body、token、当前密码、重放、事务、成功及错误语义不变 |
| flag 开启；规范路径的 `GET`、`HEAD`、`DELETE` 等非 POST | 先经现有 self **读**授权及可选同源 `Origin` 校验，不要求写侧头/CSRF/密码 | JSON 405 `method_not_allowed`、`Allow: POST`；不进入报价、提交或财务处理 |
| flag 开启；非规范但属任一路形状的 `POST` | 先经现有 self **写**授权（有效 self、匹配 `Origin`、`X-Self-Request: 1`、当前 CSRF），再判路径；路径错误先于业务输入 | JSON 400 `invalid_request`，无 `Allow`；不进入原业务处理器、密码/bcrypt 或财务读取/写入 |
| flag 开启；非规范但属任一路形状的非 POST | 先经现有 self **读**授权和可选同源 `Origin`，路径错误先于方法错误 | JSON 400 `invalid_request`，无 `Allow`；不升级为写授权或财务处理 |

匿名、失效/过期/退出的 self 会话、停用员工、管理员 Cookie 与员工模型 Bearer Key 均不能绕过相应 self 身份；错误或重复 `Origin`、POST 缺失/错误 `X-Self-Request` 或 CSRF 的既有 401/403 等授权错误先于开启态的 400/405。规范非 POST 与畸形非 POST 不得因路由名是写操作而要求写侧头。畸形 POST 的路径拒绝不得碰报价、当前密码或财务目标；认证本身可能读取持久会话/员工状态，不能把“零业务财务读取”误写成“零 DB 访问”。规范 POST 上的裸 `?`、query、报价 body、提交 body/token/重放及所有业务 400/404/409/503 仍归原处理器，守卫不复制、不改序。

## 合同阶段与实施验收门槛

本轮**只新增并本地独立提交本文**，校验相对链接和 `git diff --check`；根任务全文只读审阅并明确放行前，不修改 Go、Web、测试或脚本，不 push、不开 PR/CI，也不启动服务或浏览器。后续实施的**第一步**须在当时精确基线上，用不跟随重定向的 `httptest` 与随机非 `8787`、启用 `WebDir` 的隔离真实 HTTP 进程，对两路的前缀双斜杠、点段及其他高风险原样 request-target 锁定原始 status、`Location`、`Allow`、no-store 和正文类型，先红后绿；不可用默认自动 follow 或仅调用分类函数代替。若预期 307/`Location` 没有复现，先将实际原始证据报告根任务，不作预设路由修改。

审阅放行后的独立表驱动测试至少交叉覆盖 flag 关/开、报价/提交两路、规范/畸形与 `GET`/`HEAD`/`POST`/`DELETE`；原始及单/双编码的 slash、backslash、`.`/`..`，双斜杠、前缀清理、空 ID、extra/tail、编码操作名、合法 ID 的无歧义转义，以及规范 POST 的裸 `?`/query/body。分别验证匿名、管理员 Cookie、员工模型 Bearer Key、有效/过期/撤销/停用员工、同源/异源/重复 `Origin`、POST 的 CSRF 与 `X-Self-Request`；断言身份/头错误先于 400/405，关态统一 404，非 POST 无写侧门禁。兄弟 `renewal-links`、`cancel`、`one-shot-renewal`、`purchase-snapshot`、一般 unknown 和相似操作名须在不同开关组合下保留原归属，证明拒绝请求不委托报价/提交处理器、不触当前密码/bcrypt 或财务事务/读取；不把认证自身的 DB 访问算作业务读取。

原月度续购的冻结报价、token/session 绑定与失效、当前密码和精确重放、直属钱包/Key-resource/他人隔离、唯一后继、月末/闰年、armed 预约 superseded、商业关闭后重放、并发、失败回滚及购买时记录等必须回归；不能以路由状态码测试替代。实施批还需定向 Go/race/vet、Web typecheck/test/build、`git diff --check`、随机非 `8787` 真进程与真实 Chrome 桌面/390px 的两步显式操作和晚响应验证；运行、跳过、计划分别报告。根第二次只读审阅后才可独立提交/推送 Draft PR；精确新 HEAD 的必要 GitHub CI、普通非浅 tracked-clean 固定 Git 源/二进制及双随机根黑盒 G 须重新核验，不借用 PR62、旧二进制或旧 G。Ready、合并、部署另行授权；不跑 native package，不使用真实凭据或参考树。保护主目录未跟踪研究文档/`output/` 和本 worktree 原四类未跟踪文件。

## 来源与许可

本文由上述 CPA Cloud 自有功能规格、精确 `main` 的自有源码及自行拟定的路由拒绝矩阵撰写，不复制、翻译、移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树的代码、测试、迁移、素材与文档；此前看过参考资料，因此不称严格 clean-room。2026-10-04 核对的公开技术依据：[Go `ServeMux` 的方法模式、逐段转义匹配与路径清理](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.PathValue`](https://pkg.go.dev/net/http#Request.PathValue)、[Go `URL.EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)及[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。Go/RFC 文档描述标准行为；404/400/405、授权和 flag 的先后是本项目自定的兼容要求，不归因于标准库。本文不引入第三方依赖、SDK、素材或许可变更；现有 Go 标准库和第三方依赖保留各自许可证，后续新增源码/测试须注明本合同及实际公开来源。
