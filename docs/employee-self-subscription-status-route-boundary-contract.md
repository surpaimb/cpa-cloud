# ID-05 / BILL-03 员工本人订阅状态集合 GET/HEAD 路由形状边界合同

状态：2026-10-05，开发预览、**仅本地合同**。精确起点为已合并 `main` 提交 `c32a2c314f760ec32a242147a5c3ec5893917757`（tree `f7a73394d0b6ed5324ff0cdc99ee4a91ec5a5fa1`）。本文件提出下一小批的可证伪兼容目标，**不表示守卫已实现、旧服务已动态复现或修后已验收**。业务、身份、游标和存储规则继续以[本人订阅状态合同](employee-self-subscription-status-contract.md)、[员工自助基础合同](employee-self-service-foundation-contract.md)和[预览接口合同](preview-contract.md)为准；产品范围与独立实现约束见[产品计划](product-plan.md)、[开发计划](development-plan.md)、[功能对齐计划](feature-parity-plan.md)及[独立实现规则](independent-implementation.md)。

## 当前归属与唯一目标

令 `S = /self/api/v1/billing/subscriptions`。静态核对本仓 [`self_service.go`](../internal/service/self_service.go)、[`app.go`](../internal/service/app.go) 与 [`self_subscription_status.go`](../internal/service/self_subscription_status.go)：只在默认关闭的 `--employee-self-subscription-status-enabled` 和自助总开关同时开启时，`ServeMux` 注册 `GET S`；Go 的 GET 模式也匹配 HEAD，没有单独的 HEAD 处理器。现有外层守卫链尚无订阅**状态集合**形状守卫。当前处理器已经负责 query/body、会话后的游标和只读订阅查询；既有 CLI、capability 和网页显式读取已存在。静态阅读只说明代码结构，不是畸形 request-target 的旧首响应证据。

本批仅拟在 `ServeMux` 前为**已证明属于状态集合的 GET/HEAD**增加拒绝形状边界。逐字 origin-form `S`（可带原有 query）在开启时仍交给原 `GET S` 注册及原处理器，绝不重写为另一条成功路径，也不新增 HEAD 处理器。关态的规范集合和已归属别名不先经 `ServeMux` 清理、WebDir、Origin、会话或数据库而泄露另一响应；开态的已归属非逐字别名先经原 self **读**认证，再固定拒绝。订阅状态开关仍独立、默认关闭且仅依赖自助总开关；余额、商业执行、购买和子路由各自开关不因本合同变化。

本合同不改变 `RequestURI`、`URL`/`RawPath`、`PathValue`、方法、query、请求体、会话、员工身份、游标、订阅行、财务读取/写入、CLI、capability、UI、DDL、provider、支付或 worker。规范读取仍只投影本人直属现存订阅的最小状态，月到期仍是只读计算；不是权益、完整账单或付款证明。GOV-02 与生产启用边界照旧。

## 先保护其他 owner

未来守卫只能在既有自助兄弟守卫的**内侧**包住 `ServeMux`，让已有外层 owner 先处理；守卫自身也必须先检查方法、完整段和跨视图归属，不能凭字符串包含 `subscriptions` 夺路。此位置是拟实施约束，不声称当前已有这一层。

| 输入家族 | 归属约束 |
| --- | --- |
| origin-form 逐字 `GET/HEAD S`，以及最终确认为**集合本身**、但原路径非逐字的 GET/HEAD | 仅此为本合同目标；query 不参与路由归属。`S/`、重复分隔或点段只在清理结果确为 `S` 且无非空 ID/其他 owner 时才可能成为集合别名。 |
| `POST S`，包含可能的畸形写请求 | 始终透传现有[套餐购买](employee-self-plan-purchase-contract.md)及其[路径守卫](employee-self-plan-purchase-route-boundary-contract.md)。购买的自助读/写认证、Origin、CSRF、`selfGate`、开关、首响应与财务结果不得由集合读守卫替代。 |
| `S/{id}` 下的购买记录、续购关联、取消、报价/续购、一次性预约及其尾段；其他非空 ID 或未知子路径 | 保留各 ID 子路由及原兜底 owner。即使编码分隔符、点段或多轮解码使某一视图看似回到 `S`，有可识别的非空 ID/子操作时也不得由集合守卫认领。 |
| `plans`、`balance`、`entries`、分类、兑换、信用历史和其他 `/self/api/v1/billing/` 同级路径；`subscriptionsx`、`subscriptions-old`、`subscriptions%2Dold`、`subscription` | 原兄弟守卫、`ServeMux` 或兜底优先；`subscriptions` 必须是大小写敏感的完整路径段，不做前缀或子串匹配。清理到这些邻居或从已归属邻居穿越而来的歧义路径也不认领。 |
| POST/PUT/DELETE/OPTIONS 等一切非 GET/HEAD（包括逐字 `S`） | 无条件透传。不得替原 owner 生成 `400/404/405`、`Allow`、`Location` 或新的写鉴权。 |

集合守卫既不试探 ID 是否存在，也不以某个子路由 flag 关闭为理由接管该路由。`S%3Ffoo` 和 `S%23foo` 中的编码问号/井号仍为路径数据，不能伪装 query/fragment 或集合段边界；`S%2Fid`、`S%5Cid` 不能把 ID 当作集合。反向点段如 `S/../plans` 清理为邻居时必须否决集合归属。若视图矛盾、原 owner 不明或存在多个合理归属，结果是**不归属本守卫并透传**，不是猜测性拒绝或授权。

## 有界、跨视图一致的形状判定

只分类 Go HTTP 服务器**已经交给应用**的 GET/HEAD；传输解析器先拒绝的请求目标不承诺本应用 JSON。origin-form 以原始 `RequestURI` 第一个**字面** `?` 以前的字节为路径；编码 `%3F` 不是分隔符。仅在原始路径及 `URL.Path`、非空 `URL.RawPath`、`URL.EscapedPath()` 和从 `URL.Path` 独立导出的转义路径相互可核对时，才继续判断。必须核查原始路径一次反转义确为 `URL.Path`、`RawPath` 若存在也确为同一路径，并拒绝转义视图与原目标矛盾的输入。`PathValue` 不用于先验分类，也不得设置或修改。

每一候选视图仅为**拒绝判据**，最多进行 16 轮百分号反转义，直到稳定；第 16 轮后仍有可能再解码的 `%`、非法转义或任一轮出现不一致，就不可归属。只为判定最终目的，可把反斜线当分隔符、按完整点段/重复斜线清理；这些步骤绝不回写请求，也不能让一个编码 ID 变成成功的集合请求。所有视图都必须得到同一最终目的 `S`，且不得包含上述非空 ID、兄弟 owner 或歧义；否则透传。路径的大小写仍逐字比较，不能把 `Subscriptions` 折为 `subscriptions`。

预算均按**原始字节**而非 rune 计：路径及每个解码/转义视图至多 8192 字节；非 origin-form 的 scheme/authority 到路径前缀至多 1024 字节；反转义至多 16 轮。从已确定的路径起点最多检查 8193 字节（origin-form 时即 request-target 起点）：若字面 `?` 出现在第 8193 字节，前 8192 字节就是完整路径，仍可按其他归属规则判定；若第 8193 字节仍是路径字符，则路径超限，即使更后面才出现 `?` 也不可归属。路径恰在 8192 字节结束同样完整；更早出现的字面 `?` 之后均为 query。query 不占路径预算，也不由形状层解析；超限、未找到完整路径、需截断才似乎命中 `S` 的输入一概**不可归属**并透传，不得用截断前缀判定。后续测试须分别覆盖 8191/8192/8193、1024/1025、16/17 轮以及长 query/RawPath。

不得把所有非 origin-form 当作邻居或逐字路径。Go 服务端若实际接受 `scheme:/path` 或 `scheme://authority/path`，需分别在固定旧 `main` 实测其解析视图与首响应；只有上述有界、完整且一致的路径证明确属 `S` 时才能认领，但**一律 `literal=false`**，即使解析后 `URL.Path == S`。大写 scheme、userinfo、空 `URL.Host`、请求行 authority 与 `Host` 头不同均为必测变量，不依赖二者逐字相等来推断归属。opaque、authority-form、asterisk-form、无法分出路径或不一致视图不能因某个字段偶然等于 `S` 被认领。逐字 literal 仅限 origin-form 原始路径恰为 `S` 且相关路径视图也逐字一致；编码字母、额外斜线、点段、非 origin-form 均不是 literal。

## 开关、身份及首响应优先级

下表仅约束**本守卫已证明归属**的集合输入。若既有外层守卫先认领、或者本守卫因预算/owner 歧义透传，保留旧 owner 的状态、`Location`、`Allow`、正文和方法语义，不以本表倒推它们。现有 `requestMiddleware` 的 `Cache-Control: no-store` 仍适用。

| 状态与输入 | 先后顺序及目标首响应 |
| --- | --- |
| 状态功能关闭（自助总开关任意）；literal 或已归属非 literal GET/HEAD | 在 Origin、self Cookie、会话 DB、订阅 DB、`ServeMux`/WebDir 之前 `404`；无 `Location`、无 `Allow`。身份、Origin、query 和方法 GET/HEAD 不使关闭的入口显露；不要求 JSON 错码。 |
| 状态功能开启；origin-form literal GET/HEAD | 原 `GET S` 模式/`requireSelf(..., false)`/handler 原样运行。可选 Origin 不同源或多值先 `403 request_rejected`；无/坏/过期/撤销/停用会话 `401 authentication_required`；会话存储故障 `503 storage_unavailable`。认证后仍由原 handler 决定 query/body/cursor `400`、订阅只读页 `200` 或存储 `503`，不复制业务逻辑。 |
| 状态功能开启；已归属非 literal GET/HEAD | 先走同一个 self **读**认证（故 Origin/会话错误仍先于形状错误，可有原有会话 DB 读取）；认证通过后固定 JSON `400 invalid_request`，无 `Location`/`Allow`。不解析集合 query/body/cursor，不运行订阅读取或财务事务，不转发至 `ServeMux`。 |

上述守卫自生成的 `404/400` 与认证分支不得带 `Location` 或 `Allow`；HEAD 的首响应状态和有关头需与对应 GET 一致，wire 上无正文，不能借空正文掩盖重定向。规范 HEAD 保持现有 GET-pattern handler 路径，不以“HEAD 不需要内容”为由跳过其原鉴权、参数或只读事务。守卫不增加 `405` 或新 `Allow`；POST 购买及其他错误方法的旧签名必须单独实测与保持。管理员 Cookie、员工模型 Bearer Key 不能代替 self 会话；畸形读别名不得触发写请求的 CSRF、购买 gate、密码或任何财务写入。不得记录 Key、Cookie、Origin 原值、cursor、提示词、响应或上游秘密。

## 修前红的独立门槛与后续验收

公开 Go `ServeMux` 文档提示重复斜线和完整点段可被清理/重定向；因此对旧 `main` 上的 `GET/HEAD /self/api/v1/billing//subscriptions`、`/self/api/v1/billing/./subscriptions` 可提出 3xx/`Location` 的**静态可证伪预期**，绝不能写成已经观察到的旧服务事实，更不能把上一批钱包、购买或关联路由的红/绿结果借来使用。

任何实施前，先从精确 `c32a2c314f760ec32a242147a5c3ec5893917757` 建立普通非浅、tracked-clean、无 alternates 的固定源码/可追溯二进制，记录 tree、构建信息、exe 哈希；在隔离 data-dir/WebDir 和随机非 `8787` 回环端口，以 raw TCP 原样发送请求、**不跟随重定向**，保存旧服务首个完整 wire 响应、进程/端口清理证据。独立 parser 观测可解释 `RequestURI`/`URL` 视图，但必须与旧 exe 的动态响应分开标记。若预期的真实旧服务触发不存在、固定身份无法证明或 owner 不明，立即报告根任务并**停止实施**，不为了得到修复而捏造红测试。

修前矩阵至少覆盖自助总开关及状态开关 off/on × 匿名、无效、有效 self 会话 × GET/HEAD；缺/同源/冲突或重复 Origin；literal、前缀双斜线、点段、编码字母/分隔符、跨视图冲突、预算边界、同级邻居和所有已注册订阅 ID 子路由。路径预算必须成对检验：8192 字节路径无 query 与同一路径加 `?x` 的集合归属相同；第 8193 字节仍为路径字符时不可归属；8193 字节路径之后才出现 `?` 仍不可归属；短路径加长 query 不消耗路径预算。对 Go 真正接受的 `scheme:/path`、`scheme://authority/path`，连同大写 scheme、userinfo、空 URL Host、Host/authority 不同，分别测首跳而不把 parser 推测写成旧服务事实。`POST S` 必须在购买开关关/开、匿名/无效/有效 self 与 Origin/CSRF 组合下保存旧购买首响应签名；POST/PUT/DELETE/OPTIONS 对 canonical、畸形、邻居也要证明透传。每项记录状态、`Location`、`Allow`、`Cache-Control`、`Content-Type`、JSON `error.code`（若有）、wire 正文长度和哈希，HEAD 特别核对零 wire 正文；避免自动 redirect 客户端吞掉第一跳。

根任务全文审阅并明确放行后，实施批才可写独立分类单测和真实进程先红后绿。修后还须证明关闭态零 Origin/会话/财务 DB/Mux 委托、开启别名的 Origin→会话→固定 400 顺序、规范读取原身份与分页/故障语义、ID/兄弟及 POST 购买签名不变、财务零写、无 WebDir marker/敏感日志；定向构建/测试、精确 HEAD CI、固定 exe 和隔离验收分别报告，不能把本合同、静态预期或邻批 CI 算作绿。本合同阶段**只提交本文**，不启动服务、浏览器或测试，不推送、不建 PR、不触发 CI/native、不发布。

## 来源、许可与适用边界

本文为 CPA Cloud 根据本仓自有功能合同、上述精确源码的静态归属检查以及公开协议机制独立撰写；未复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA、相邻参考树的代码、测试、迁移、素材或文档。此前看过参考资料，故不宣称严格 clean-room。2026-10-05 查阅：[Go `ServeMux` 方法模式/转义段/清理](https://pkg.go.dev/net/http#ServeMux)、[Go `RequestURI`/`PathValue`](https://pkg.go.dev/net/http#Request)、[Go `URL.Path`/`RawPath`/`EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 9112 request-target 形式](https://www.rfc-editor.org/rfc/rfc9112.html#section-3.2)、[RFC 3986 百分号编码](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1)与[点段](https://www.rfc-editor.org/rfc/rfc3986.html#section-5.2.4)、[RFC 9110 HEAD](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.3.2)、[RFC 9111 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。它们说明协议/库机制；本合同的 404/400、owner 与鉴权优先级是项目自定目标，旧应用首响应仍待独立实测。

本文不增加依赖、SDK、资产、许可变更或新源码。Go 标准库保留 [Go BSD-3-Clause 许可](https://go.dev/LICENSE)；现有第三方依赖各保留原许可证，记录见[依赖许可清单](research/dependency-notices.md)。未来新增源码和测试须另记本合同与实际公开来源，不以本文代替实现验收或发布授权。
