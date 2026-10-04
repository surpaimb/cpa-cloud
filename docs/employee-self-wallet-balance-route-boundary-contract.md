# ID-05 / BILL-03 员工本人钱包余额 GET/HEAD 路由形状边界合同

状态：2026-10-05，开发预览，**仅本地合同**。起点为已合并 `main` 提交 `9084d76135fd2cde65621a5320a41a71122f284e`（tree `9473a0968d96302d245c7a820e535043256adad4`）。本文件只规定默认关闭的本人钱包余额**读取入口的 pre-ServeMux 形状边界**；业务读取仍以[本人钱包余额合同](employee-self-wallet-balance-contract.md)和[员工自助基础合同](employee-self-service-foundation-contract.md)为准，预览接口见[接口合同](preview-contract.md)。整体独立实现、产品和开发约束见[独立实现规则](independent-implementation.md)、[产品计划](product-plan.md)及[开发计划](development-plan.md)。根审原合同后放行的固定旧 `main` 真 TCP 取证另记于下文；这不是本守卫已实施、修后验收或发布的声明。

## 现状、目标与不变量

令 `B = /self/api/v1/billing/balance`。静态检查本仓 [`self_service.go`](../internal/service/self_service.go) 与 [`app.go`](../internal/service/app.go)：只有 `--employee-self-service-enabled` 开启且独立、默认关闭的 `--employee-self-wallet-balance-enabled` 开启时才注册 `GET B`；钱包开关单独开启会被启动校验拒绝。Go `ServeMux` 的 `GET` 模式也匹配 `HEAD`，当前没有独立 HEAD handler。本批既不新增 HEAD handler，也不更改两个 flag、会话 capability、商业执行开关、原有 canonical 处理或余额计算；CLI、网页 UI、JSON 业务投影、DDL、供应商/支付接口均不在本批。员工模型 Bearer Key、管理员 Cookie 均不能替代当前有效的本人 self 会话。

Go 文档说明 `ServeMux` 在分派前会清理重复斜线和完整点段并可能重定向。原合同据此提出双斜线和点段首跳 `307`/`Location` 的**静态、可证伪预期**；随后从固定旧 `main` 独立动态复现，证据见下文，绝不借用套餐目录 PR #68、购买 PR #67 或其它路径的红/绿结果。旧服务还接受 absolute-form 请求目标：其 `RequestURI` 不是 origin-form，而已解析的 `URL.Path` 可直达余额 handler；因此不能将所有非 origin-form 一概判为不可归属。目标是：对本守卫**确已证明归属**的 GET/HEAD，关态不让 `ServeMux` 先清理或 WebDir 接管；开态只允许逐字 origin-form canonical 进入原读取，畸形别名经原读鉴权后拒绝，绝不把别名重写成成功请求。

canonical 读取保持既有规则：一个且仅一个三位大写 ASCII `currency`，无请求体；有效 self 会话的员工 ID 是唯一归属来源。`200` 仍只有 `currency`、`has_account` 和 canonical 字符串或 `null` 的 `amount_micro`；无账户与真实零不同。现有至多五秒单一只读财务快照、Key/资源子账户隔离、故障整响应 `503` 和零财务写入保持不变。HEAD 与对应 GET 使用相同匹配及状态/有关响应头，**wire 上不含正文**；不通过跳过余额读取、构造假 `200` 或额外的 HEAD 路由改变原语义。

## 先审原 owner，再定本守卫位置

当前 [`app.go`](../internal/service/app.go) 中月续购、关联、购买记录、取消、一次性预约、估算成本、top-up 历史、管理员调整历史、兑换历史、分类、兑换、购买 POST 与套餐目录守卫均在 `ServeMux` 外。未来余额守卫只能包住 `ServeMux`，放在这些既有守卫的**内侧**；外层兄弟先判自己的形状与方法，余额守卫不得替它们重新分类。特别是购买报价和订阅购买的 POST 专属守卫不因本 GET/HEAD 方案改变。旧 `GET /self/api/v1/billing/entries` 无专属前置形状守卫，仍由原 `ServeMux`/现有 handler 拥有；不能因为原路径出现 `balance` 字样而夺取该 owner。

| 原路径家族（包括各自旧畸形输入） | 本批归属规则 |
| --- | --- |
| `GET/HEAD B` 与有完整 `balance` 段、且经下面规则确属 B 的别名 | 仅这一家族由新守卫处理。 |
| [`entries`](employee-self-wallet-activity-contract.md)、[`entry-classifications`](employee-self-wallet-entry-classification-contract.md)、[`redemptions`](employee-self-redemption-route-boundary-contract.md)及兑换信用历史、管理员调整、top-up 信用历史 | 保留原守卫或原 `ServeMux`/handler 的开关、身份、方法与首响应；本批不实现 top-up 冲减来源历史或累计快照。 |
| [`plans`](employee-self-plan-catalog-route-boundary-contract.md)、[购买 POST](employee-self-plan-purchase-route-boundary-contract.md)、[`subscriptions` 及 ID 子路由](employee-self-subscription-status-contract.md) | 原目录、购买、订阅状态、购买记录、关联、预约、取消及月续购 owner 优先；订阅状态边界留待独立批次。 |
| `POST/PUT/DELETE/OPTIONS` 等所有非 GET/HEAD，包括字面 `B` | 新守卫无条件透传，不制造本路由的 `404/400/405` 或 `Allow`；原 owner 决定响应。 |

余额段必须是完整段，不能做字符串前缀匹配：`B`、`B/`、`B/{tail}` 是待判余额形状；`/self/api/v1/billing/balancex`、`balance-other`、`balance%2Dother` 则为明确同级邻居。`B%3Ffoo` 与 `B%23foo` 中的编码问号/井号仍是 path 数据，不是 query/fragment 或余额段分隔符；不能认领。`B%2Ftail`、`B%5Ctail` 只可在有界拒绝视图中把解码 `/` 或 `\` 视为段边界，绝非 literal canonical。`B/../entries`、`B/../plans`、`B/../subscriptions` 以及清理到任一非余额现有 owner 的路径，即便保留过完整 `balance` 段也交回原链；清理到 `B` 或仍位于 `B/` 子树的别名才可由本守卫认领。若旧外层守卫已先认领交叉目标，直接遵从旧 owner，不能再比较本守卫期望。未知目的地、含矛盾视图或不能证明最终归属的路径同样透传，不能把“看起来像”当授权或拒绝理由。

## 有界证据、literal 判定与拒绝

分类仅在服务器已交给应用的 GET/HEAD 请求上进行；HTTP 解析器在此之前拒绝的非法 request-target，不要求本合同生成项目 JSON。origin-form 从 `RequestURI` 取首个**字面** `?` 前的 path。对 Go 服务端已接受的非 origin-form，`RequestURI` 非空、`URL.Opaque` 为空且有界、完整、相互一致的路径视图证明最终属 `B` 时，同样认领为 `owned=true,literal=false`；不以原目标 `scheme://authority` 与解析后的 `URL.Scheme`/`URL.Host` **逐字相等**为前提，也不因 scheme 大小写规范化、`URL.User` 存在、解析后 `URL.Host` 为空或单独 `Host` 请求头不同而直接透传。只在预算内核验原目标与已解析路径的关系，不扫描无界 authority。两种形式的 `%3F` 都不切断 path，`%23` 都不创建 fragment。交叉检查完整的 `URL.Path`（Go 已解码）、可选 `URL.RawPath`（编码提示）、以及从有界 `Path` 副本独立生成的转义视图。只有原 `Path` 与 `RawPath` 均在预算内且可核一致时才可调用原 `URL.EscapedPath()` 作比较；不能单信某一个视图，也不能用它重写请求。opaque、预算外、缺失/不一致字段或测试构造的 `RequestURI`、`Path`、`RawPath` 相互矛盾时，仍保守判为不可归属；合成视图的矛盾不能凭一个看似 `B` 的 `URL.Path` 绕过一致性证明。

**literal canonical** 要求原目标确为 origin-form、原始 path 与完整 `Path` 均逐字等于 `B`、`RawPath` 为空或逐字等于 `B`、原 `EscapedPath()` 逐字等于 `B`，所有视图完整且互相一致。`B?currency=USD` 与 `B?` 的 path 都是 literal，query/body 正误留给现有 handler。**任何已接受的非 origin-form 都不是 literal**；即使其解析后的 path 逐字为 `B`，只要有界、相互一致的路径视图证明属于余额，就必须是 `owned=true,literal=false`，关态走前置 `404`、开态先读鉴权再 JSON `400`。Host 与 authority 不同本身不是路径归属否决，Origin 的校验仍由原 `requireSelf` 决定。大小写折叠、重复斜线清理、点段移除、任意百分号解码所得的 `B` 也一律只能是**拒绝候选**；不得修改 `RequestURI`、`URL`、`PathValue`、query 或方法来命中成功路由。

拒绝候选要有完整、可核的一条余额段及其真正终点或 `/`/`\` 边界：在原有界视图、逐轮 `url.PathUnescape` 后的有界视图、仅用于拒绝的 ASCII 大小写/反斜线折叠视图，以及完整路径的斜线/点段清理视图中证明最终为 `B` 或 `B/` 子树。`B/extra`、重复斜线、完整 `.`/`..`、编码字母、编码分隔符、多层编码等均可作为测试候选；解码与清理只是证据，不定义新的路由规范。只要完整视图证明目标最终是非余额 sibling、同级邻居，或不同视图无法一致确定归属，就不认领。不得以一个已截断的 `B` 前缀或编码残片伪造段终点。

本守卫的工程预算定为：**每个路径来源最多 8192 字节、每条候选路径最多 16 轮百分号解码**；非 origin-form 的原目标 path 之前部分另限 **1024 字节**，至多检查其第 1025 字节来判定是否超限，不在原目标中无界找 `/`，也不把原始 authority 的拼写当路径归属条件。这是审查现有外层兄弟守卫之后选取的路径上限，同时把新增前缀核验成本显式封顶；它不是 Go、HTTP 或钱包业务的协议限制，也不要求复用兄弟分类器算法。余额本身只有一个固定段、没有 ID/wildcard，因此本守卫反而要求完整一致的证据，不为了“覆盖”而扩大认领范围。对已界定的 origin-form 或非 origin-form path 最多探看到第 8193 个 path 字节：若该字节是字面 `?`，前 8192 字节是完整 path；若仍是路径字符或需再看后续字节才能判终点，记为不完整。先于预算遇到 `?` 时，后面的长 query 不消耗路径预算。`Path`、`RawPath`、派生转义结果及每轮解码/清理结果分别限长；前缀超限、path 超限、无效编码、缺失必要字段或第 17 轮才可能显露目标时，不把截断尾部当真实终点或分隔符，判**不可归属**，交原链。不得为了判断一个超长 `RawPath` 先调用无界 `EscapedPath()`。全局 HTTP 请求大小限制不属于本批。

## 首响应与认证优先级

下表只约束新守卫确已认领的余额 GET/HEAD；旧外层 guard 先认领、未知或其它方法由各原 owner 决定。两种有效配置是自助开/余额关（base-only）与自助开/余额开（balance-on）；自助关/余额关作为额外对照，余额开/自助关必须启动失败。

| 配置、路径 | 匿名或无效 self | 当前有效 self |
| --- | --- | --- |
| base-only 或两者均关；literal `B` 或已证明畸形别名 | 均在 Origin、Cookie/会话 DB、余额 DB、`ServeMux` 清理和 WebDir 之前直接 `404`。 | 同样直接 `404`，不因身份不同而改变。 |
| balance-on；literal `B` | 沿用原 `requireSelf(..., false)`：可选 `Origin` 不同源/重复先 `403 request_rejected`；缺/坏/过期/停用会话为 `401 authentication_required`，会话存储故障为 `503 storage_unavailable`。 | 原 handler 原样决定 query/body `400`、余额快照 `200` 或存储 `503`；不改变查询、事务、JSON 或 HEAD 语义。 |
| balance-on；已证明畸形别名 | **先**调用原 self **读**鉴权；Origin/session 错误与上一格一致，不提前给 `400`。 | 读鉴权通过后固定 JSON `400 invalid_request`；不解析余额 query/body，不调用余额读取或财务事务。 |

关态由新守卫产生的 `404`、开态畸形分支产生的 `400` 以及该分支经原读鉴权产生的 `401/403/503`，不得携带 `Location` 或 `Allow`；外层 `requestMiddleware` 保持 `Cache-Control: no-store`。`404` 不强制 JSON 正文。HEAD 在 wire 上无正文，但首响应状态、相关头与错误优先级须对应 GET，不能以空正文掩盖重定向。literal canonical 与透传的 sibling/wrong-method 继续由原实现决定其 `Location`/`Allow`；本合同不重定义通用方法错误。畸形别名只用 `requireSelf(..., false)`：不触发写请求的 `X-Self-Request`、CSRF、购买 `selfGate` 或 bcrypt；允许会话校验的既有持久读取，不允许钱包/余额读取、DDL、财务写入、上游调用或敏感日志。

## 固定旧 `main` 首响应证据与后续验收门槛

根审原合同后获准的**修前红**只针对固定旧 `main`，不是本守卫修后绿：独立源码克隆 `C:\Users\apple\.codex\tmp\wallet-balance-red-20261005-d84a194d00c84b7891b7e1b627c3f2ba\source` 的 HEAD 为 `9084d76135fd2cde65621a5320a41a71122f284e`、tree 为 `9473a0968d96302d245c7a820e535043256adad4`；所构建 Windows/amd64 Go 1.26.8 exe 的 SHA-256 为 `68038BC5D7575CBA172BDCB927DB681DEF1F8D4FBAA20C8FAFF1254F2E9587BD`。在隔离 data-dir/WebDir、非 `8787` 回环端口，用不跟随重定向的 raw TCP 捕获旧 exe 首响应；进程与监听器随后已清理。证据根目录为 `C:\Users\apple\.codex\tmp\wallet-balance-red-20261005-d84a194d00c84b7891b7e1b627c3f2ba`：`evidence.json`（66 例，SHA-256 `FA636921433E7B1468AD09B45CE418ACE67AC02FC66B4BE83CC6F2785932B046`）和 `evidence-supplement.json`（136 例，SHA-256 `89E14A93ECF2A070F3D3D5775D9E06CD57496D4F15264A2BC4790D155B570BF6`）共保存 202 个旧服务首响应；`parser-capture.json`（32 例，SHA-256 `B55A962A224A553609AF5D41C4EE4DA5129ED4E6BCB958B5F0FF025BECD5CD11`）仅为**独立** Go 1.26.8 `http.ReadRequest` 对合成请求字节的 `RequestURI`/`Path`/`RawPath`/`EscapedPath`/Host 视图，绝非旧 exe 埋点。

旧首跳事实：base-only/balance-on × 匿名/有效 self × GET/HEAD 的 origin-form `/self/api/v1/billing//balance` 与 `/self/api/v1/billing/./balance` 共 16 例均为 `307`，`Location: /self/api/v1/billing/balance?currency=USD`、无 `Allow`、`Cache-Control: no-store`；GET 重定向正文 77 字节，HEAD wire 正文 0。origin-form literal `B` 在 base-only 为 `404`，balance-on 匿名 `401`、有效 self `200`；balance-on 有效 self 的编码字母 `%62alance` 也为 `200`。absolute-form canonical 即使 `Host` 请求头与 URL authority 不同，在 balance-on 有效 self 仍为 `200`、匿名 `401`，base-only 为 `404`；absolute-form 双斜线/点段仍为 `307`。`B/` 后接 8200 字节的超预算路径旧响应为 `404`，不能据此把截断前缀认作余额。`B/../entries` 旧 `307` 到 entries，`B/../plans` 旧 `404` 由原目录守卫决定；同级邻居保持旧 owner。46 个 HEAD 捕获的 wire 正文均为 0；未见 WebDir marker。这些是旧行为，不是未来新守卫的预定状态。

非 GET/HEAD 必须保留原 owner，而不能以“错误方法必为 404/405”代替实测。`POST/PUT/DELETE/OPTIONS` × base-only/balance-on × 匿名/有效 self 的固定旧首跳签名如下；表中 JSON `code` 均不存在，`Cache-Control` 均为 `no-store`，所有 `Allow` 均为空：

| 请求路径 | 状态；`Location`；`Content-Type`；wire 正文长度；正文 SHA-256 |
| --- | --- |
| literal `B?currency=USD`、`balance-other?currency=USD`、`balance%2Dother?currency=USD` | `404`；无；`text/plain; charset=utf-8`；19；`B16E15764B8BC06C5C3F9F19BC8B99FA48E7894AA5A6CCDAD65DA49BBF564793` |
| `/self/api/v1/billing//balance?currency=USD`、`/self/api/v1/billing/./balance?currency=USD` | `307`；`/self/api/v1/billing/balance?currency=USD`；无；0；`E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855` |

实现另获准后，独立写分类单测与真实 wire 集成测试：上述配置/身份/方法矩阵，HEAD 无正文；关态零 Origin/会话/余额 DB 调用且无 `Location`/`Allow`、有 no-store；开态 Origin 先于 session、session 先于畸形 `400`，canonical 保留原 query/body/存储优先级；财务零写。覆盖 origin-form 与 absolute-form 的 canonical、双斜线、点段、URL authority/Host 不同、**大写 scheme**，以及解析器若接受的 **userinfo/空 Host** absolute-form；先在固定旧 exe 上追加不跟随重定向的真 TCP 首跳探针，再把相同输入纳入修后验收，不把独立 parser 捕获误报为旧服务结果。还须覆盖 opaque/矛盾解析视图和超预算前缀/path、`balancex`、`balance-other`、`balance%2Dother`、`B/{tail}`、编码 `/`/`\`/`?`/`#`、反向和交叉点段、所有旧 sibling owner 与未归属目标。对 literal `B`、`/self/api/v1/billing//balance`、`/self/api/v1/billing/./balance` 和明确邻居的 `POST/PUT/DELETE/OPTIONS`，逐项比对**旧**首跳状态、`Location`、`Allow`、no-store、`Content-Type`、JSON `code`、wire 正文长度与哈希，不能让新 GET/HEAD 守卫改写其 owner 或默认假定 `404/405`。预算测 8191/8192/8193 路径字节、恰在边界的 `?`、长 query/RawPath、16/17 轮解码、截断后的伪终点及不一致构造视图。另核 no WebDir marker、无敏感日志和不变的 canonical 钱包读。定向测试、vet/build、后续精确 HEAD CI 与固定 exe 验收须分别报告，不能用 PR #67/#68、旧红或本合同代替修后绿。

## 来源、许可与发布边界

本文为 CPA Cloud 基于自有功能规格、本仓当前 owner 的静态审查与上述独立固定旧 `main` 真 TCP 证据拟定；未复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA、相邻参考树及其测试/资产，也不宣称严格 clean-room。2026-10-05 查阅的公开机制资料：[Go `ServeMux` 模式与清理](https://pkg.go.dev/net/http#ServeMux)、[Go `Request.RequestURI`](https://pkg.go.dev/net/http#Request)、[Go `URL.Path`/`RawPath`/`EscapedPath`](https://pkg.go.dev/net/url#URL.EscapedPath)、[Go `PathUnescape`](https://pkg.go.dev/net/url#PathUnescape)、[RFC 3986 编码](https://www.rfc-editor.org/rfc/rfc3986.html#section-2.1)和[点段](https://www.rfc-editor.org/rfc/rfc3986.html#section-5.2.4)、[RFC 9110 HEAD](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.3.2)、[RFC 9111 响应 `no-store`](https://www.rfc-editor.org/rfc/rfc9111.html#section-5.2.2.5)。这些资料只支持协议/库机制；旧应用首响应另以固定 exe 的 raw TCP 证据为准。本文无新源码、SDK、依赖或素材；Go 标准库依[Go 许可](https://go.dev/LICENSE)为 BSD-3-Clause，现有第三方依赖各保留自身许可与[依赖许可记录](research/dependency-notices.md)。未来实现须显式记载新源码来源。本地修订仍待根二审；不据此实施、推送、PR、native 构建或发布。
