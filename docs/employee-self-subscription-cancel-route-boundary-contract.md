# ID-05 / BILL-03 员工本人取消订阅路由边界补充合同

状态：2026-10-03，开发预览的合同先行批次；本文件只规定待实现和待验证行为，不宣称路由修复或验收已经完成。基线为已合并 `main` 的 `00e7b655886932717c8ae54349fc601a78acdfe9`。本文补充[员工本人取消订阅合同](employee-self-subscription-cancel-contract.md)，继承[自助会话基础](employee-self-service-foundation-contract.md)和[本人订阅状态](employee-self-subscription-status-contract.md)，并遵守[开发预览合同](preview-contract.md)。原取消合同规定规范 POST 的身份、请求、交易和结果，但未单独钉死 ServeMux 未匹配的畸形路径状态码及规范路径其他方法的 405；以下是新的兼容性约束，不追称原合同已实现这些响应。

## 唯一范围与现状

唯一目标是独立、默认关闭的 `POST /self/api/v1/billing/subscriptions/{id}/cancel` 的 HTTP 路由入口：在 Go `ServeMux` 路径清理、重定向或 `/self/api/` 兜底之前，对取消形状作狭窄分类，再沿用既有 self 授权边界。当前只注册精确 POST；取消处理器在匹配成功及 self 校验之后才检查 `PathValue("id")` 和 `EscapedPath()`。因此畸形请求可能未到达其 `400 invalid_request` 检查。修复不能扩大既有取消业务可达范围，也不能使邻接路由变成取消请求。

不新增启动 flag、capability、API 成功字段、UI、财务写入、DDL、worker、供应商或支付行为。规范 POST 必须继续进入原处理器，保持当前密码的每次验证、精确重放优先、最终会话重验、admission/单事务/CAS/月到期 guard、并发 first-commit-wins、错误脱敏及不确定提交规则；本边界不得读财务目标、触发密码验证或启动取消事务来判断畸形路径。已有取消能力开关与前置条件不变。

## 路由形状、优先级与响应

“规范路径”只有此前述前缀、一个非空且符合既有财务 ID 语法（1–256 UTF-8 字节）的不透明 `{id}` 路径段，以及末尾**完整** `/cancel` 段；该 ID 的 URL 转义必须是无歧义的规范形式，不能是 `.`、`..` 或含斜杠、反斜杠、百分号的解码值。原取消合同对 POST query（含空 `?`）、请求体和业务错误的限制仍由现有处理器判断；路由边界不得通过改写路径、解码后转发或重构请求让非规范拼写成功。

“取消形状”只用于识别上述订阅前缀下、在原始/转义及必要的解码视图中以**完整 `/cancel` 路径段**为操作名的请求，包括该段前后的空段、附加尾段、错误的 ID 转义或编码操作名。分类是保守拦截，不是接受别名：`%2F`/`%2f`、双重转义、反斜杠、点段、重复斜杠、编码的 `cancel`、多段 ID 和尾段均不得被重定向、规范化成成功调用或悄悄落到另一业务。只在 Go HTTP 栈已接受并交给应用的请求上承诺应用级 JSON/no-store；HTTP 解析器在交付应用前拒绝的非法 request-target 不在本合同内。

| 能力与请求 | 路由/授权次序 | 应用响应 |
| --- | --- | --- |
| 取消能力关闭；规范或取消形状，任意方法 | 先按关闭能力处理，不查 self 身份、目标或方法 | `404`，无 `Location`、无 `Allow`、不暴露能力或目标；不得产生 `301`/`307`/`308` |
| 能力开启；规范 `POST` | 保持既有 self 写授权，原样交给取消处理器 | 既有合同的成功/错误/重放/密码/事务语义不变 |
| 能力开启；畸形取消形状 `POST` | 在 ServeMux 清理/兜底前捕获；先走既有 self 写授权（有效员工会话、同源 `Origin`、`X-Self-Request: 1`、当前 `X-CSRF-Token`），再判路径；不得财务读写 | 对通过授权者为 JSON `400 invalid_request`，不重定向；匿名、管理员、跨源或坏写头依既有 self 授权错误优先 |
| 能力开启；规范路径的 `GET`、`HEAD`、`DELETE` 及其他非 `POST` | 在 ServeMux 方法分派前捕获；先走既有 self **身份**边界，不要求写请求头/CSRF | 对有效员工会话为 JSON `405 method_not_allowed`、`Allow: POST`；身份或可选 `Origin` 校验失败时维持既有 self 错误优先 |

表中应用响应（包括 404、400、405 和 self 授权错误）均为 `Cache-Control: no-store`，不得泄露订阅存在性、owner、员工、密钥、密码、摘要或内部存储原因。`405` 不启动写授权、密码验证或财务操作；畸形 POST 的写授权失败也不得先以路径错误泄露取消能力。规范 POST 的非规范 query 仍由原处理器返回其既定 400，不改变此处的路径/方法优先级。

分类必须以操作名的**路径段边界**而非子串匹配；`/cancelled`、`/cancellation` 不是 `/cancel`。不得拦截 `renew`、`renewal-quotes`、`renewal-links`、`one-shot-renewal`、`purchase-snapshot` 或其他订阅兄弟操作，即使其 ID/路径中包含 `cancel` 文本或编码斜杠；这些路径保持各自已有 flag、guard、handler 与错误语义。没有可辨认 `/cancel` 操作段的任意陌生路径仍归普通路由处理。若解码视图彼此冲突或会指向兄弟操作，不能以宽泛子串搜索抢占兄弟路径；仅在明确取消形状且非规范时走上述畸形响应。

## 独立验收矩阵

实现批次须对 HTTP handler 及真实服务器请求分别编写本仓独立测试，并明确区分合同、已实现与测试结果：

- 开关关闭时，规范及取消形状的 `POST`、`GET`、`HEAD`、`DELETE` 都是 404、无 `Location`/`Allow`；开关开启时规范 POST 仍走原取消链。
- 开启时逐一发送原始与解码路径的额外段/尾段、空 ID/空段、`%2F` 与 `%2f`、双重编码、反斜杠、`.`/`..`、双斜杠、编码操作名；可交付应用的畸形取消 POST 通过写授权后均为 JSON 400，不出现 `301`/`307`/`308`，也不产生取消记录。
- 同一畸形 POST 分别使用匿名、管理员 cookie、非同源或歧义 `Origin`、缺失/错误 `X-Self-Request`、缺失/错误 CSRF；断言既有 self 写授权先于 400，且响应 no-store、无财务读写。
- 对规范路径用有效 self 会话发 `GET`、`HEAD`、`DELETE`，断言 405 和 `Allow: POST`；匿名/管理员不能凭方法差异获得目标信息，错误 `Origin` 保留原 self 拒绝规则。HEAD 的响应体按 HTTP 语义处理，状态、头部仍须验证。
- 明确回归 `renew`、`renewal-quotes`、`renewal-links`、`one-shot-renewal`、`purchase-snapshot`、`cancelled`、普通未知路径及各自开关组合，不得由取消 guard 接管。
- 原取消合同的成功、同 ID 精确重放、每次当前密码、登出/改密/停用/最终会话失效竞争、月到期最终 guard、同/不同 ID 并发和存储失败仍需专项回归；不以路由测试替代这些业务验收。

合同阶段只提交本文，核验相对链接、`git diff --check`、精确 HEAD/tree/parent/merge-base/status，等待主任务只读审阅。审阅放行后才实现及运行专项 Go/race/vet、Web、随机非 8787 隔离进程和桌面/390 px 浏览器验证；第二次主任务审阅后才可一次推送并开 Draft PR，再核本批精确 HEAD 的 GitHub CI。本文不授权预览发布、tag、部署、native package workflow 或对旧 CI 的结果复用。

## 来源与许可

本合同由 CPA Cloud 按本仓自有功能规格和测试目标独立撰写；未复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA、邻近参考仓库及其 SDK。此前曾阅读参考材料，故不称严格 clean-room。2026-10-03 核对的公开协议依据为 [Go `net/http` ServeMux 模式、路径清理和重定向](https://pkg.go.dev/net/http#ServeMux)、[Go `net/url` URL.EscapedPath](https://pkg.go.dev/net/url#URL.EscapedPath) 与 [Go `net/url` PathUnescape](https://pkg.go.dev/net/url#PathUnescape)。本合同不新增第三方依赖、SDK 或素材；现有依赖各保留原许可。后续新增源码和测试须标明本合同及实际使用的公开来源。
