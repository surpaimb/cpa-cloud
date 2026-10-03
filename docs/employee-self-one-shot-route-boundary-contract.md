# ID-05 / BILL-03 员工 one-shot 状态与撤销的路由边界补充契约

状态：2026-10-03，开发预览、合同先行；本文只规定待实现及待验证行为，不声称修复、浏览器或 CI 已通过。精确基线为已合并 `main` 的 `e289c59ecec35e35a0a01cc8849990d58409c29f`（tree `2bafab1f091ed6b76e4dbfef57b0809a1d6e524d`）。本文补充[员工一次性预约查看与撤销契约](employee-self-one-shot-disarm-contract.md)，沿用[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)及[开发预览接口契约](preview-contract.md)。原 one-shot 合同已经规定默认关时两条路由 404、规范 GET/POST 的身份、输入、业务和交易语义以及畸形路径/输入的 400；它**没有逐字钉死**错误方法的 405/`Allow`，也没有规定异常路径在 `ServeMux` 方法匹配、清理/重定向和身份授权之间的顺序。以下是新的兼容性界定，不能倒称旧合同已实现这些响应。

## 唯一范围与当前入口

唯一目标是现有两个员工本人入口：`GET /self/api/v1/billing/subscriptions/{id}/one-shot-renewal` 的最小状态读取，以及 `POST /self/api/v1/billing/subscriptions/{id}/one-shot-renewal/disarm` 的撤销。现有 `--employee-self-one-shot-renewal-disarm-enabled` 默认关闭；显式开启仍要求员工自助总开关和本人订阅状态开关，缺一时启动在持久化变更前失败。只开启前置能力不能隐式开启 one-shot。本补充不要求钱包余额、购买、取消或商业执行开关；商业执行关闭时既有状态读取及合格撤销/精确重放保持原义。

当前 `internal/service/self_service.go` 仅在 one-shot 开关开启时注册方法限定的规范及尾段模式；`internal/service/self_one_shot_disarm.go` 的 `PathValue("id")`/`EscapedPath()` 检验发生在模式匹配与 self 授权之后。Go `ServeMux` 的 `GET` 模式也匹配 `HEAD`，并可能先清理点段、重复斜杠或重定向；不匹配的方法和 `/self/api/` 兜底也可能先于处理器回答。因此不能仅凭处理器现有 400 检查宣称所有畸形请求均已按 one-shot 合同处理。修复须在 `ServeMux`、`WebDir` 与兜底之前做窄路由分类，但不得把分类当成业务授权或可接受的新路径别名。

不新增 flag、capability、成功 JSON 字段、UI、DDL、账本/资金写入、worker、admin、供应商、支付或第三方依赖；不修改 one-shot arm、月度续购、套餐购买、trial、账单或财务事务。规范 GET 的六字段、只读事务、owner 隔离与故障语义，以及规范 POST 的当前密码、限流、精确重放、typed employee actor、最终 admission/会话重验、CAS、同事务 receipt 与不确定提交规则均交由原处理器，原样保留。本路由层不得为分类读取密码、运行 bcrypt、读取财务目标或开启财务事务。

## 形状与先后次序

“规范状态路径”和“规范撤销路径”分别只含上述完整固定前缀、**一个**非空且符合既有财务 ID 规则的 `{id}` 段（1–256 UTF-8 字节）及完整操作段；撤销路径再有完整 `/disarm` 段。ID 的 URL 转义须无歧义且规范，不接受解码后的斜杠、反斜杠、`.`/`..`、空 ID、重复/附加段，或百分号再解码会产生分隔符/操作别名的双编码；不把编码操作名当作别名。既有合法不透明 ID 不应仅因含无歧义的编码字符被新分类器拒绝。规范路径上的 query（含空 `?`）、GET body、POST JSON/密码等继续由原处理器按旧合同验证；路由分类不得清理、重写 URL/`PathValue` 或把另一拼写转发给规范处理器。

“one-shot 形状”是订阅前缀下，在原始/转义与有界解码视图中能明确定位完整 `one-shot-renewal` 操作段、其完整 `disarm` 子操作，或清理后会落到这两个入口的路径。只为**拒绝**歧义而识别；`one-shot-renewals`、`disarmed`、ID 中出现相同文字、一般未知路径和其他操作不属于此形状。可交给应用的空段、extra/tail、`%2F`/`%2f`、双重编码、反斜杠、`.`/`..`、重复斜杠、编码的操作名以及操作段或 ID 的混合编码均须在 `ServeMux` 清理/重定向和 Web 兜底前被分类为非规范，不能通过 301/307/308 或二次解码转成成功操作。HTTP 解析器在交付应用前拒绝的非法 request-target 不承诺应用级响应。分类必须有界，不能无限反复解码。

同一订阅下，`renew`、`renewal-quotes`、`renewal-links`、`purchase-snapshot`、`cancel` 及其他明确兄弟操作仍由各自 flag、guard 和处理器拥有。使用**路径段边界**及最少解码可确定的首个操作，而非子串搜索；编码斜杠、ID 文本或尾段中的 one-shot 字样不能抢走兄弟路径。只有未经拦截将由路径清理重定向进入 one-shot 入口的歧义形状，才由本 guard 拒绝该重定向；不得扩大兄弟操作的成功、错误或授权范围。

优先级固定如下。表中非规范 one-shot 形状的**路径错误优先于方法错误**：通过相应 self 授权后返回 400，而不是 405；规范路径的错误方法才返回 405。认证/Origin/写请求头失败又优先于开启状态下的 400/405。只有 405 带所列精确 `Allow`；下列所有应用响应均 `Cache-Control: no-store`，均无 `Location`，不能泄露目标存在性、owner、密码、财务状态或内部错误。`HEAD` 的状态与头须可验证，不依赖响应体。

| 开关及形状 | 方法与授权顺序 | 应用结果 |
| --- | --- | --- |
| one-shot 关闭；规范或 one-shot 形状 | 任意方法；先于 self 身份、DB、方法分派和清理 | 404，无 `Allow`，不重定向；能力形状不因方法、身份而暴露 |
| one-shot 开启；规范状态路径 | `GET` 沿用既有 self 读授权与原处理器；`HEAD`、`POST`、`DELETE` 等其他方法先经现有 self 身份及可选同源 `Origin`，不要求写头/CSRF | `GET` 保持旧响应；其他方法为 JSON `405 method_not_allowed`、`Allow: GET`，不进入财务读取（尤其 `HEAD`） |
| one-shot 开启；规范撤销路径 | `POST` 沿用既有 self 写授权与原处理器；`GET`、`HEAD`、`DELETE` 等其他方法先经现有 self 身份及可选同源 `Origin`，不要求写头/CSRF | `POST` 保持旧响应；其他方法为 JSON `405 method_not_allowed`、`Allow: POST`，不进入密码或财务路径 |
| one-shot 开启；非规范状态形状 | 任意方法先经现有 self 读身份及可选同源 `Origin`；不作财务读取 | 有效员工为 JSON `400 invalid_request`，无 `Allow`；包括畸形状态路径加错误方法 |
| one-shot 开启；非规范撤销形状 | `POST` 先经现有 self 写授权：当前员工会话、同源 `Origin`、`X-Self-Request: 1`、当前 `X-CSRF-Token`；其他方法先经现有 self 读身份/可选 `Origin` | 有效且满足所需授权者为 JSON `400 invalid_request`，无 `Allow`；包括畸形撤销路径加错误方法；不读密码、不运行 bcrypt、不触达财务 |

管理员 Cookie、员工模型 Bearer Key、匿名、停用/未开通或已失效员工均不得凭路由形状或方法得到目标信息。开启时，坏/歧义 `Origin`、缺/错写头和 CSRF 仍保留现有 self 固定错误优先级；规范错误方法不升级为写授权，畸形撤销 `POST` 也不能绕过写授权。关闭时先 404，不因这些认证差异改成 401/403/405。此前合法 GET/POST 的 query/body、非本人目标、密码、重放及存储错误不由本 guard 重新解释。

## 实现批次的独立验收门槛

本合同阶段不改实现或测试。根任务全文只读审阅并明确放行后，实现批须**先**以不跟随重定向的 `httptest`/HTTP 请求锁定当前真实触发：同时覆盖 `App.Handler()` 与开启 `WebDir` 的实际服务器，记录异常路径、`HEAD`、错误方法和关闭开关的原始状态、`Location`、`Allow`、no-store，再修复。不得只测纯分类函数或默认自动 follow 的客户端，更不得把规划行为写成已通过。

修复后以自有表驱动测试覆盖纯分类和 HTTP：flag 关闭/开启 × 状态/撤销规范与畸形 × `GET`/`HEAD`/`POST`/`DELETE`；原始与解码路径的空 ID、extra/tail、编码斜杠、双编码、反斜杠、点段、双斜杠、编码操作名和 query/body；匿名、admin Cookie、Bearer Key、有效/失效员工、坏或歧义 `Origin`、缺/错 `X-Self-Request` 和 CSRF。逐项断言 404/400/405、JSON 错码、精确 `Allow`、无 `Location`、no-store 及授权优先级；使用窄测试 hook 或等效观测证明畸形/错方法零 bcrypt、零财务读写/事务触达。测试普通兄弟路径及歧义清理、开关组合，确保 `renew`、`renewal-quotes`、`renewal-links`、`purchase-snapshot`、`cancel` 和未知路径不被抢占。

业务回归须保留既有 GET 恰六字段、无 body/query、`none`/`armed`/终态、直属 owner 和 Key/resource/他人隔离；POST 当前密码、两步授权、同 ID 精确重放、改密/登出/停用/会话失效、最终 admission、并发 first-commit-wins、typed actor、存储/取消/未知提交恢复。路由测试不能替代这些旧业务断言。以随机非 `8787` 的隔离进程和合成数据检查真实 socket/WebDir、无重定向及零财务触达；真实 Chrome 对既有员工 UI 在桌面和 390px 做能力门控、显式读取、撤销入口/失败与布局回归，不能把纯 HTTP 验收称为浏览器 DOM 验收。实施批仍需定向 Go 测试、`go vet ./...`、Web typecheck/test/build 与 `git diff --check`，并明确区分运行、跳过、计划和未验证。

合同阶段**只新增并独立本地提交本文**：核所有相对及官方链接、`git diff --check`、精确 HEAD/tree/parent/merge-base/status，保护此 worktree 既有四类未跟踪文件及主目录受保护文档、`output/`；根任务全文只读审阅前不实施、不 push、不开 PR、不触发 CI。若后续明确放行实施，先集中本地修复/成套验收并再经根第二次只读审阅，才可一次提交/推送独立 Draft PR；精确新 HEAD 的必要 GitHub CI、普通非浅 clean 固定 Git 源与二进制、双随机根 G 验收及 guarded merge 均另行核验。旧 PR/CI/G 不能借用。不得碰邻近参考仓库、真实 `8787`、凭据、部署、tag、发布、原生打包或 native package workflow。

## 来源与许可

本合同由 CPA Cloud 依据上述本仓功能合同、当前路由事实及独立拟定的验收目标撰写，未复制、翻译、移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树的代码、测试、迁移、资产或文档；此前看过参考资料，故不称严格 clean-room。2026-10-03 核对的公开技术依据为 [Go `net/http` `ServeMux` 的方法模式、分段转义匹配与路径清理](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.PathValue`](https://pkg.go.dev/net/http#Request.PathValue)、[Go `net/url` `URL.EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath) 与 [Go `net/url` `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)。这些文档说明标准库行为；本合同的 404/400/405 优先级和授权策略是 CPA Cloud 自定的兼容边界，不应归因于 Go 官方要求。无新增 SDK、依赖、素材或许可证变更；现有第三方依赖仍各保留其原许可证，后续新增源码和测试须显式标注本合同及实际使用的公开来源。
