# ID-05 / BILL-03 本人购买时记录的路由边界补充契约

状态：2026-10-03，开发预览、合同先行；精确源码基线为已合并 `main` 的 `b8878865774a4d9539719a436826f3d3daade749`（tree `e83da74a9fbdcaea2c6b523eeefc5bdda5c7e3e3`）。本文仅规定下一实施批的路由兼容边界，不声称修复、浏览器验收或新 HEAD 的 CI 已通过。它补充[本人购买时记录原契约](employee-self-subscription-purchase-snapshot-contract.md)，沿用[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[本人钱包余额](employee-self-wallet-balance-contract.md)及[开发预览接口契约](preview-contract.md)。原契约规定独立默认关、路由关闭时 404、畸形路径不重定向、有效员工畸形输入 400、no-store、严格只读交易与九字段响应；它**没有逐字钉死**关闭状态下全部畸形形状/方法的优先级，也没有规定规范路径错误方法的 405/`Allow` 或 `ServeMux` 清理与身份授权的先后。本补充是新增目标，不可倒称旧实现已经满足。

## 精确范围与已观察缺口

唯一目标是现有 `GET /self/api/v1/billing/subscriptions/{id}/purchase-snapshot`。`--employee-self-subscription-purchase-snapshot-enabled` 仍独立、默认关闭；显式开启仍须自助总开关、本人订阅状态和本人钱包余额三个前置开关，缺一在持久化改变前拒绝启动。只开前置能力不暗开购买时记录；`features.employee_self_subscription_purchase_snapshot`、现有 UI 显式按需读取、成功 JSON 的九个字段及含义、直属 owner/Key/资源隔离、坏链失败关闭和单一只读事务保持原义，且不依赖商业执行开启。不新增 flag、capability、字段、UI、CSS、CLI、DDL、财务写入、供应商、支付或 worker；不得改其他订阅操作或内部交易逻辑。

当前 `internal/service/app.go` 仅在该 flag 开启时安装 `selfPurchaseSnapshotRouteGuard`，`internal/service/self_service.go` 也仅在开启时注册方法限定模式；`internal/service/self_subscription_purchase_snapshot.go` 的守卫只匹配既有订阅前缀，先判断方法再判畸形。`ServeMux` 可能在这些检查及 `/self/api/` 兜底之前清理点段/重复斜杠并重定向；`GET` 模式亦可匹配 `HEAD`。旧 `internal/service/self_subscription_purchase_snapshot_test.go` 已用不跟随重定向的请求锁定部分编码 ID，但关闭态只覆盖 `%2F`，未锁点段和前缀双斜杠。2026-10-03 以与上述 merge commit **相同 tree** 构建、但 VCS revision 标记为其 PR head `820557fe5a5936677e13566105c79b4fafed4d52` 的固定二进制，在随机 `127.0.0.1:52312`、合成 data-dir、开启 `WebDir`、该 flag 关闭且无 self 身份的真实进程上，用不跟随重定向的原样 GET 实测：`/self/api/v1/billing/subscriptions/id/../id/purchase-snapshot`、`/self//api/v1/billing/subscriptions/id/purchase-snapshot` 和 `/self/api/./v1/billing/subscriptions/id/purchase-snapshot` 均为 `307`、有规范路径 `Location`、`Cache-Control: no-store`。该探针不是本补充的修复验收；实施前仍须先以当前精确源码和不跟随客户端把原始状态、`Location`、`Allow`、no-store 固定成先红后绿测试。若该触发在实施环境不成立，先报告根任务，不得套用假设修改路由。

## 仅用于拒绝的路径形状

规范路径必须有完整固定前缀、一个非空且符合原处理器规则的 `{id}` 段（最多 256 UTF-8 字节）和完整 `/purchase-snapshot` 段，没有额外路径段；ID 的转义须无歧义且规范，不接受解码后斜杠、反斜杠、`.`/`..`、空 ID、编码操作别名或再次解码可造成分隔符/别名的双编码。不得把有效的不透明 ID 仅因其含无歧义编码字符而拒绝；具体可接受字节仍以现有 `selfPurchaseSnapshotPath` 的身份无关严格校验为准。query（包括空 `?`）、GET body 和未知长度 body 是原处理器的输入校验，不由本路由层解释或改变错误优先级。

“purchase-snapshot 形状”只指在订阅路径下，原始/转义与有界解码视图可按**完整路径段**辨认 `purchase-snapshot` 操作，或若未拦截会被路径清理重定向到该入口的歧义拼写。视图仅为拒绝服务；不得清理/改写 `URL`、`PathValue`、请求方法或将任何别名转发给成功处理器。应覆盖空 ID、extra/tail、原始及编码/双编码斜杠、反斜杠、`.`/`..`、重复斜杠、前缀清理（包括 `/self//api/` 与 `/self/api/./v1`）、编码操作名及 ID/操作段混合编码；解码次数有界。Go HTTP 解析器在交付应用前拒绝的非法 request-target（如原始 `%ZZ`）只属传输层 400，不承诺应用 JSON 或 no-store。

分类须以第一条可确定的操作段和段边界保护兄弟入口：`renew`、`renewal-quotes`、`renewal-links`、`cancel`、`one-shot-renewal`、其他明确兄弟操作及一般未知路径仍由原 flag/guard/handler 拥有。`/id/purchase-snapshot/extra` 属本入口畸形尾段，`/id/unknown/purchase-snapshot` 不因较后段出现同名操作就归本入口。`purchase-snapshots`、ID 或兄弟尾段里包含相同文字不能因子串命中而被本守卫抢走；编码斜杠不得让 ID 文本变成新的操作。只在路径清理将**本来属于本入口的歧义形状**重定向至规范购买时记录路径时，守卫须在 `ServeMux` 前拒绝该重定向；与现有其他前置守卫的优先级不得扩大兄弟路由权限或改变其响应。

## 开关、授权和响应优先级

本守卫在 `ServeMux`、`WebDir` 和通用兜底之前无条件参与形状判断，包括 flag 关闭时；它不做目标存在性探测、财务读取、交易、密码检查或商业执行。开启时沿用现有 `requireSelf(false)` 的可选同源 `Origin`、self Cookie、会话到期及员工状态检查顺序；管理员 Cookie 与员工模型 Bearer Key 不能替代 self 会话。匿名、过期/撤销/停用员工及错误或多值 `Origin` 的既有固定错误先于开启态的 400/405，不泄露订阅、owner 或链状态。规范错误方法和全部畸形形状均为**读授权**，不额外要求 `X-Self-Request`、CSRF 或密码。

| 状态及路径 | 方法与门禁 | 应用响应 |
| --- | --- | --- |
| flag 关闭；规范或上述形状 | 任意方法；在身份、Origin、DB、方法匹配和清理前 | `404`，无 `Allow`/`Location`；身份与方法不可区分 |
| flag 开启；规范路径 | `GET` 交原处理器；`HEAD`、`POST`、`DELETE` 等其他方法先经同一 self 读授权 | `GET` 保持旧合同；其他方法为 JSON `405 method_not_allowed`、精确 `Allow: GET`，不进入财务读取 |
| flag 开启；非规范形状 | 任意方法先经同一 self 读授权；路径错误优先于方法错误 | 有效员工为 JSON `400 invalid_request`，无 `Allow`/`Location`，不进入业务处理器或财务交易 |

表中所有应用响应（含 HEAD 的状态与头）均 `Cache-Control: no-store`，无 `Location`；HEAD 只断言状态与响应头，不要求响应体可见。只允许真正的 405 带 `Allow: GET`。不得将规范错误方法误升为写授权，也不得把畸形路径随请求方法变成 405。规范 GET 对 query/body、缺失或非本人目标、原业务链及存储错误的既有 400/404/503 顺序和九字段成功响应由原处理器负责，本守卫不可复制或替代。

## 分阶段独立验收门槛

本合同阶段**只新增本文并独立本地提交**，不修改实现、测试、脚本或旧合同；根任务全文只读审阅并明确放行前不 push、不开 PR、不触发 CI。实施阶段第一步须用不跟随重定向的 `httptest` 和随机非 `8787` 的隔离真实进程/WebDir 对当前基线先红后绿，逐项保存原始状态、JSON 错码、`Location`、`Allow` 与 no-store，而非使用默认自动 follow 或只测分类函数。

修复后自有表驱动测试须覆盖 flag 关/开 × 规范/畸形 × `GET`/`HEAD`/`POST`/`DELETE`；原始/编码/双编码 slash 和 backslash、dot/`..`、重复斜杠、前缀清理、空 ID、额外段、编码操作名、合法 ID 的无歧义编码，以及原规范 GET 的空 `?`/query/body。逐项比较匿名、管理员 Cookie、真实员工模型 Bearer Key、有效/过期/停用员工 self 会话、错误/重复 `Origin` 的授权优先级；证明拒绝请求不委托业务处理器、不运行财务读取/事务。兄弟操作及未知路径须分别在相关 flag 组合下保持既有归属，尤其不能让本守卫抢走 `renew`、`renewal-quotes`、`renewal-links`、`cancel` 或 `one-shot-renewal`。现有九字段、直属 owner/Key/资源/他人隔离、坏链 503 与商业执行关闭后的历史只读回归不能被路由测试替代。

实施批仍须定向 Go 测试、`go vet ./...`、Web typecheck/test/build、`git diff --check`、随机非 `8787` 进程与真实 Chrome 的桌面及 390px 显式读取回归；报告运行、跳过、计划和未验证项各自证据。根第二次只读审阅放行后才可一次提交/push 独立 Draft PR；新精确 HEAD 的必要 GitHub CI、普通非浅 tracked-clean 固定 Git 源和二进制、双随机根黑盒验收及 guarded merge 均须重新核验，不借上一批 PR/CI/二进制。保护本 worktree 原四类未跟踪文件和主目录受保护 `docs/research/membership-next-step.md`、`output/`；不触碰邻近参考仓库、真实 `8787`、凭据、部署、tag、发布或 native package workflow。

## 来源与许可

本文依据本仓功能合同、上述精确源码及自行拟定的拒绝/验收边界撰写，未复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考树的代码、测试、迁移、素材或文档；此前看过参考资料，因此不称严格 clean-room。2026-10-03 核对的公开技术依据为 [Go `net/http` `ServeMux` 方法模式、分段转义匹配与路径清理](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.PathValue`](https://pkg.go.dev/net/http#Request.PathValue)、[Go `URL.EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath) 和 [Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)。Go 官方文档说明标准库行为；404/400/405、认证和能力开关优先级是 CPA Cloud 自定的兼容目标，不归因于 Go。本文无新增依赖、SDK、素材或许可变更；既有第三方依赖各保留原许可证，未来新增源码与测试须显式标注本合同及实际使用的公开来源。
