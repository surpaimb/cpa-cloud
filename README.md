# CPA Cloud

**简体中文** | [English](README.en.md)

面向企业内部的自托管 AI 接入平台。管理员通过网页管理上游、模型和员工 Key；员工使用标准 API，无需微信或专用客户端。鉴权、权限检查和上游请求在同一个 Go 服务进程内完成。

> 当前为开发预览，尚非生产发行版。preview.3 的 18 个主包已由对应架构的 GitHub runner 完成测试、构建及各格式的自动验收；尚未完成所有目标主机上的真实 GUI 操作和部署验收。

## 功能与边界

| 已实现 | 尚未实现或验证 |
| --- | --- |
| 网页后台、管理员会话、员工启停、模型权限 | 多租户、SSO、管理员密码重置命令 |
| 一人多个 Key、默认永久有效、可选到期、撤销；源码提供 Codex 网页授权和自动刷新 | Claude/Gemini 会员接入及真实账号验证 |
| OpenAI-compatible API Key 上游、服务商预设、模型同步；源码增加 Claude/Gemini 原生 API Key 通路 | 跨协议自动转换和未列出的协议字段 |
| `/v1/models`、Chat Completions 非流式与 SSE | CC Switch 与各实际 AI 工具的完整兼容验收 |
| 最新源码：`POST /v1/responses`、函数工具调用/结果回传、非流式/SSE；默认关闭的加密有状态资源与后台任务 | 托管工具、后台流续传/游标与完整客户端兼容性 |
| SQLite 持久化、上游凭据加密、多账号路由；可靠用量/通用预算、单实例财务账本、Windows DPAPI 自动备份 | 生产支付渠道、跨机/非 Windows 密钥托管与对象存储 |

员工 Key 正常重启后仍有效；撤销、员工停用、可选到期时间及权限限制仍会生效。员工无需知道上游供应商 Key。

## Codex 会员文件导入实验（仅最新源码）

此功能已接入最新源码，**不在 v0.1.0-preview.3 下载包中**。从源码构建后，在现有启动命令末尾添加 `--experimental-codex-membership`。默认关闭；桌面启动器目前不会自动开启它。初始化与启动继续使用同一个数据目录。

1. 管理员登录网页，在“上游连接”选择“导入 Codex auth.json”，主动选择自己提供的授权文件；系统不会扫描本机配置。
2. 文件须包含可用的短期 access token、账号 ID 和所需结构。导入后显示“已导入，未验证”；文件由服务端加密保存，导入本身不会联网验证账号。
3. 点击“同步模型”获取候选目录，再明确选择模型建立路由；也可手动填写模型 ID 和对外名称。目录有短时缓存，不保证所有列出的模型都可调用，不自动扩大员工权限。
4. 员工仍使用 CPA Cloud Key，通过 `/v1/chat/completions` 调用。当前只接收 `user`/`assistant` 字符串文本，以及可选的 `stream` 布尔字段；支持文本非流式与 SSE。system/developer、工具、图片及其他未支持参数会明确拒绝。
5. 上游请求成功完成后状态变为“已验证”；凭据过期或上游返回 401 时提示重新导入。使用现有上游行的“重新导入”替换文件，员工 Key 不变。关闭实验开关会阻止会员导入及调用，普通 API Key 上游不受影响。

会员实验另支持下文的网页 OAuth 授权、共享自动/手动刷新与原生 Responses 子集；Claude/Gemini 会员接入仍未完成。自动验收使用合成凭据和假上游，**尚未用真实会员账号验证**；不应据此认定 Codex CLI、Claude Code 或 CC Switch 所配置的所有工具均可使用。Messages 使用独立的 Anthropic API Key 上游，不支持把 Codex 账号转换成 Claude 会员。

升级前停止服务并备份整个数据目录（包含数据库及主密钥），保护备份访问权限。源码首次打开旧数据库会事务化扩展上游表；迁移失败会回滚。回退旧程序时应同时恢复升级前的整份备份，勿让新旧进程同时使用同一目录。

## Codex 网页授权与凭据刷新（源码实验）

最新源码提供网页授权、状态查询、手动刷新和共享自动刷新，**不在 preview.3 下载包中**。默认关闭；需同时配置 `--experimental-codex-membership`、`--codex-oauth-client-id` 和 `--codex-oauth-redirect-uri`。管理员须提供自己获准使用的 OAuth client ID，并确认其回调地址与 scope 获供应商支持；项目不内置其他产品的 client ID，也未验证真实第三方注册是否可用。

回调路径必须为 `/admin/api/v1/codex/oauth/callback`，使用部署的完整 HTTPS 地址；仅本机开发允许字面回环地址的 HTTP。创建授权会话和手动刷新均要求管理员会话、同源请求与 CSRF Token；授权回调要求发起授权的同一个管理员浏览器会话。

| 管理 API | 请求及行为 |
| --- | --- |
| `POST /admin/api/v1/upstreams/codex-oauth-sessions` | `{ "name": "Team Codex", "operation_id": "uuid" }`，返回授权 URL 和有效期；会话有效期 10 分钟，重试复用同一 operation ID |
| `GET /admin/api/v1/upstreams/codex-oauth-sessions/{id}` | 仅发起授权的管理员登录会话可查询；返回进度，不返回凭据 |
| `GET /admin/api/v1/codex/oauth/callback` | 接收供应商回调；验证 state、会话与 PKCE，授权码最多交换一次，凭据加密保存 |
| `POST /admin/api/v1/upstreams/{id}/codex-refresh` | `{ "expected_revision": 3 }`，显式刷新并原子替换凭据；员工 Key 保持不变 |

- 仅本服务通过 OAuth 创建、且创建时 client ID 与当前配置一致的账号允许刷新。来源不明的导入文件或 client ID 错配返回 `409 codex_refresh_not_bound`，不修改现有凭据和状态。重新导入文件会清除此前 OAuth 来源绑定。
- 授权会话保存创建时的 client ID 和回调地址；重启更改配置后，旧会话要求重新发起授权，不会用新配置消费旧会话。
- 刷新网络失败、响应丢失或服务器错误时不会自动重放旧 Token；仅明确收到 429 才有界退避重试。结果不确定会持久化暂停刷新，阻止后续定时任务重放旧 Token；现有未过期 access token 可继续尝试请求，到期后需重新授权或导入。无效的 `expected_revision` 返回 400；并发版本冲突返回 409。
- 授权或刷新成功仅表示凭据已保存，不等于模型可用或账号已在线验证。同步目录只提供候选，需明确选择建立路由；过期账号可重新授权或重新导入。供应商侧撤销仍待实现。

网页“上游连接”可发起授权、查看状态、手动刷新或重新导入。自动任务每分钟扫描最多 16 个候选、最多同时刷新 2 个账号，在到期前 5 分钟尝试刷新。Chat、Responses 和模型目录共用凭据获取服务；账号级锁、锁内重读和 revision 条件更新协调刷新与凭据替换。

接口与失败语义详见 [OAuth 生命周期契约](docs/codex-lifecycle-contract.md)。

## 原生 Responses 与函数工具（仅最新源码）

`POST /v1/responses` 使用同一个员工 Key 和模型路由，**不在 preview.3 下载包中**。API Key 上游需提供同协议 `/responses` 接口；Codex 文件导入上游需开启上述会员实验。已有 Chat Completions 的文本限制不变。

- 支持非流式 JSON、SSE、函数工具定义、调用参数与 `call_id`，以及下一回合的 `function_call_output`；网关不替员工执行工具。
- Codex 子集支持文本 `input`、system/developer/user/assistant 消息、`instructions`、函数工具选择及已验证的 reasoning/text 参数。工具回合需要推理历史时，请请求 `include:["reasoning.encrypted_content"]` 并回传对应完整 output items。详见[实现范围](docs/research/codex-responses-implementation.md)。
- 默认仍是无状态请求。源码可通过 `--responses-stateful-resources` 启用员工/Key 所有权绑定的加密 `store:true`、`previous_response_id`、`GET/DELETE /v1/responses/{id}`；再显式增加 `--responses-background-tasks` 才启用创建、轮询和取消后台任务。已派发但崩溃结果未知的任务记为 `interrupted`，不会自动重放。
- 后台请求只接受已经完整持久化的文本、instructions 和 function-call 结果子集；额外字段会在入队前拒绝。托管/hosted tools 的白名单仍为空，`managed_tools=false`，没有隐藏启用开关；没有后台流续传/游标或 WebSocket 接口。Codex 图片、音频及未支持字段会明确拒绝。
- 流式只有完整 `response.completed` 才表示成功；上游失败、提前断流或凭据状态保存失败不能当作完成。上游错误正文不会直接返回。

这不等于已经通过真实 Codex CLI 或会员账号验收。完整功能的阶段与待办见[功能对齐计划](docs/feature-parity-plan.md)。

## Claude / Gemini 原生 API 与批量导入（仅最新源码）

这些功能**不在 preview.3 下载包中**：

| 上游类型 | 员工入口 | 当前范围 |
| --- | --- | --- |
| `anthropic-api-key` | `POST /v1/messages`、`POST /v1/messages/count_tokens` | Messages、SSE、函数工具及结果回合；详见 [Claude 契约](docs/claude-messages-contract.md) |
| `gemini-api-key` | `GET /v1beta/models`、`POST /v1beta/models/{model}:generateContent`、`:streamGenerateContent` | Gemini Developer API 文本、函数工具与 SSE；详见 [Gemini 契约](docs/gemini-native-contract.md) |

管理员选择对应上游类型、保存供应商 API Key 并同步模型。Gemini 原生预设自动填写固定 Google 地址，兼容模式是另一种预设；供应商模型列表分页读取后才返回结果。员工请求使用 CPA Cloud Key 和管理员配置的公开模型名称。不同协议不会自动相互转换。

批量管理 API `POST /admin/api/v1/upstreams/batch-import` 支持最多 100 条、总计 8 MiB 的 JSON，请求包含 UUID `operation_id` 和带唯一 `item_id` 的 `items`。四种上游逐项返回 `created`、`existing` 或 `failed`；网络结果未确认时应使用相同操作 ID 与原内容重试。导入成功不等于在线认证，也不会自动创建模型路由。格式见 [批量导入契约](docs/upstream-batch-contract.md)。

网页批量导入可预览逐项结果，只修复失败项；响应丢失后用原操作编号和原内容重试，不重复创建已成功的账号。

Claude/Gemini 订阅会员不属于上述 API Key 能力；各自接入条件仍见 [会员接入记录](docs/research/membership-provider-readiness.md)。完整账本和后续范围继续按 [功能矩阵](docs/feature-parity-plan.md) 实现。

## 账号池调度（仅最新源码）

在网页“模型路由”选择“编辑账号池”，配置同一提供商的多个账号及模型映射、优先级、权重和并发容量；“分组与渠道”管理对应目录。已有单路由保持原行为，只有点击“保存账号池”才启用调度；版本冲突会保留编辑并要求重新加载。接口见 [账号池管理 API](docs/account-pool-service-contract.md)。

Chat、Responses、Messages（含 count_tokens）和 Gemini 共用调度。排队结束会重新检查 Key、员工权限和配置 revision。同一账号跨池共享容量，配置不同时取所有启用模型池中的最小值。

可选请求头 `X-CPA-Session` 接受 1–256 字节会话标识；仅使用绑定员工、Key、模型和协议的 HMAC 做短时粘滞，不保存或转发原值。租约会续期、释放并在重启后保守恢复。显式账号池中，凭据解密等账号预检失败且模型请求尚未派发时，最多切换一个不同账号，并重新检查权限和池版本；账本使用实际发送账号及模型的价格。**进入上游 HTTP 调用或 Codex 执行器后不自动重放**，包括连接错误、429 和流式错误。429、认证或暂时故障的冷却影响后续独立请求。默认关闭的恢复探测见下文；出站代理首批见下文；供应商配额采集仍待实现；selector-aware 通用预算与固定模型硬预算见下文。见 [运行时契约](docs/account-pool-runtime-contract.md)和[安全换号契约](docs/account-pool-failover-contract.md)。以上为源码功能，不包含在 preview.3 下载包中。

用量账本将员工请求与上游尝试分开记录，只保存调用元数据及供应商明确返回的 Token 计数；不记录提示词、回复或工具参数。未知用量和未配置价格的成本保持为空，不能视为零费用。详见 [用量接线契约](docs/usage-service-contract.md)。

## 请求治理（默认关闭，仅最新源码）

在网页“请求治理”中，为员工、单个 Key 或明确选择成员的治理组设置每分钟请求数（RPM）和并发上限，再开启总开关。多条适用策略必须同时满足，超过任一上限返回 429，不向上游发送该请求。账号池等待期间也占员工并发；一次安全换号仍只算一个员工请求。

RPM 使用滚动 60 秒窗口；修改策略不会清空原窗口。Chat、Responses、Claude Messages 和 Gemini 生成请求共用治理，`count_tokens` 暂不计入。关闭开关只影响新请求，已开始的请求仍正常结算。异常重启后旧并发占用保守保留到原租约到期，不重放请求。

治理组独立于部门和上游账号组，不能扩大模型权限或恢复已撤销 Key。网络中断后先核对原保存操作；版本冲突时读取最新配置并确认后再保存。**Shadow TPM 和成本不限制请求或扣费。** 最新源码新增默认关闭的独立硬预算开关：治理与预算开关都开启、策略为 `deny_unknown` 时才预留并限制请求。

最新源码另提供 `/admin/api/v1/budgets` 与网页 selector-aware 通用预算：employee、Key、治理组可再按协议和公开模型收窄，多条命中策略取最严格结果。策略框架和额度窗口已通用化，但安全上界证明目前仍只覆盖官方 OpenAI `gpt-4.1-2025-04-14` 的固定参数文本非流式请求；其他模型、工具、会员和 SSE 请求在 strict `deny_unknown` 下会因无法证明上界而拒绝。成本限制还要求实际上游模型配置同币种价格。未知用量保守保留上界，不充作零，也不是供应商账单保证。集成证据和后续范围见[预算进度](docs/budget-service-integration-progress.md)；未包含在 preview.3 下载包中。租户限额不在本轮范围。

<details>
<summary>固定模型预算实验的请求条件</summary>

先将一个员工可访问的公开模型映射到官方 OpenAI 上游的 `gpt-4.1-2025-04-14`，并使用员工 Key 调用 CPA Cloud 的 `/v1/chat/completions`。下面的 `model` 替换为该公开模型名；全部七个顶层字段必须完整保留，不增加其他参数。消息只能包含 `role` 和字符串 `content`。

```json
{
  "model": "gpt-4.1-2025-04-14",
  "messages": [{"role": "user", "content": "Hello"}],
  "max_completion_tokens": 256,
  "n": 1,
  "modalities": ["text"],
  "store": false,
  "stream": false
}
```

`max_completion_tokens` 允许 1–32768。当前证明按输入上界 1,047,576 加输出上限预留；例如上例会先占用最多 1,047,832 Token，而不是按短消息估算。低于此预留量的剩余 TPM 会返回 `429 budget_exceeded`，即使实际输入很短。此保守方式仍需后续优化。

无符合条件的请求上界，或成本策略缺少同币种价格时，返回 `503 budget_bound_unavailable`。这不是账号认证失败。当前请求条件已通过合成上游测试，尚无真实供应商验证。

</details>

在同一页面点击“查看用量观测”，按员工、Key、治理组或策略筛选：Token 查看最近 60 秒，成本查看最近 24 小时，并按币种分别显示。未知用量、处理中请求和其他币种会保留不确定性；“已超过”仅表示已知部分超过历史阈值。同一范围跨策略版本共用完整总计，各卡片不能相加。翻页固定窗口边界，晚到的结算仍可能改变统计，需要最新完整结果时点击“刷新首屏”。接口细节见[观测契约](docs/governance-observation-contract.md)。

升级前备份完整数据目录；需要回退时恢复同一份升级前数据及程序，不把旧程序指向已升级数据库。接口和边界见[治理契约](docs/governance-contract.md)及[管理契约](docs/governance-management-contract.md)。验收使用临时数据库和合成上游；**preview.3 下载包不包含此功能。**

## 出站代理（仅最新源码）

在网页“出站代理”添加 HTTPS CONNECT 代理，填写主机、端口、地址范围及可选 Basic 认证，再为已保存的 HTTPS API Key 账号明确绑定。OpenAI-compatible、Anthropic 和 Gemini 的生成、模型目录、目录测试和生成恢复共用该出口；员工继续使用原 CPA Cloud Key。代理凭据加密保存，网页不读回用户名或密码。

“公网”范围只允许公网代理地址；“公司内网”范围允许 RFC1918/IPv6 ULA 的代理地址，但不会放宽模型目标地址限制。代理与目标都验证 TLS 证书，目标 DNS 每次拨号重新校验。首批没有 HTTP/SOCKS、自动代理轮换、出口 IP 查询或 Codex 绑定。

停用、无效或凭据损坏的代理不会自动变为直连。需要直连时，在账号绑定中点击“明确解绑为直连”。修改地址、认证或启停会递增连接版本及绑定账号版本；旧的排队请求不得沿用新出口，已经派发的请求可使用原连接结束。代理修改不会自动清除账号冷却或恢复隔离。

“仅握手检查”只对明确绑定的 HTTPS API Key 账号建立 CONNECT 与目标 TLS，不发送模型请求、目录请求或上游 API Key。结果带被测版本；握手成功不代表模型生成可用。提交响应丢失后查询原操作，页面不会自动重复检查；服务重启将未完成操作记为中断，不重新联网。

本批为源码功能，**preview.3 下载包不包含**。升级前保留完整数据目录及匹配的旧程序；如需回退，恢复这份完整备份，不能让旧程序直接读取已有代理绑定的新库，否则旧程序可能忽略绑定并直连。接口见[代理管理契约](docs/outbound-proxy-admin-contract.md)和[握手检查契约](docs/outbound-proxy-test-contract.md)。

## 上游账号测试与冷却管理（仅最新源码）

此批功能已通过本地验收，**不在 preview.3 下载包中**。在“上游连接”点击“账号测试”，选择本地凭据检查或目录测试：前者只在服务端解密并检查格式，后者读取提供商模型目录。两者都不发送模型生成请求，也不能据此认定模型生成可用。Codex 目录测试可能通过共享刷新流程更新凭据版本；本地检查不会刷新。

每次测试都有固定操作编号。网络中断时查询原操作或重试相同编号；相同编号不会重新调用上游。服务重启会将未完成操作记为中断，不自动重发。每账号最多一个、全局最多四个测试，上游执行最多十秒，状态写入另有有界超时；安装内保留最多 10000 条记录，满额后拒绝新操作，已保存操作仍可查询。

列表分别显示管理员启停状态、测试观测和冷却状态。“清除冷却”按账号版本与冷却事件双重校验；旧页面不能清除并发产生的新冷却。它不会启用账号、替换凭据或证明故障已经恢复。目录测试成功也不会自动清除冷却。发生版本冲突时刷新列表后再操作。

这些管理操作仅接受管理员会话与 CSRF 校验，员工 Key 不可调用。接口与失败语义见[上游测试与恢复契约](docs/upstream-health-contract.md)。

### 账号自动恢复（默认关闭，仅最新源码）

在现有服务启动命令中增加 `--allow-account-recovery`，再到网页“系统状态 → 账号自动恢复”启用。两层开关都开启才运行后台任务；仅增加启动参数不会开始探测。此功能会向真实上游发送固定短输入并消耗用量，请先配置账号和价格。未使用该参数的实例无法在网页开启，但仍可关闭以前保存的设置。

显式账号池的失败请求会保存实际账号、模型、协议和版本快照。冷却到期后，后台在共享账号容量内逐个探测；完整生成成功且快照仍匹配才解除隔离。每个冷却事件最多三次持久尝试，瞬时失败至少等待五分钟再创建新的操作，安装内最多保留 10000 条探测记录。认证或协议错误、配置变化和达到上限会要求人工核对。冷却到期、目录读取成功或管理员启用账号均不会绕过恢复隔离。

关闭开关会取消当前探测并保留隔离与历史。请求结果或结算不确定时不会重发同一操作；设置响应丢失后先用“重新读取状态”核对。上游列表可以按当前账号版本和事件“清除冷却与隔离”，但清除本身不证明生成可用。异常重启后，尚未到期的请求占用仍保守保持至租约到期，清除隔离不会提前释放该容量。旧库升级默认关闭，不根据旧冷却推测缺失路径。

系统探测独立记账，管理员可读取 `GET /admin/api/v1/system-probes/summary`；未知用量和成本保持为空，不混入员工统计。管理接口、故障边界及迁移说明见[自动恢复契约](docs/account-recovery-coordinator-contract.md)，验收使用合成凭据和模拟上游，尚无真实会员账号验证。**preview.3 下载包不包含此功能。**

## 用量与成本管理（仅最新源码）

此功能**不在 preview.3 下载包中**。管理员打开“用量与成本”，按时间、员工、Key、公开模型、上游、提供商和状态查询请求，点“查看尝试”查看实际执行账号、Token 和价格版本。请求与尝试分别计数；不同币种分开汇总。时间窗口最多 31 天，翻页固定当前窗口，“最近24小时”重新查询最新记录。

在同页“价格管理”选择上游账号，再添加**实际上游模型名**的价格；不是对员工展示的模型别名。四项费率分别为普通输入、输出、缓存读取、缓存写入，单位是 **microcurrency / 百万 Token**：1,000,000 microcurrency 等于 1 个所填币种单位。例如输入费率填 `1000000` 表示每百万普通输入 Token 为 1 USD（币种填 USD 时）。这些均由管理员配置，项目不内置供应商报价。

保存会追加不可修改的新版本，每个上游尝试在派发前固定价格；后续改价不修改在途或历史记录。账号池使用实际选中的账号与模型价格。取消“启用价格”会追加停用版本，后续成本为未知。其他管理员改价引发版本冲突时保留表单，先重新加载再确认保存；网络结果未确认时可用同一操作编号重试。

页面显示的是内部**估算成本**，不是供应商账单或向员工收费。最新源码新增可靠派发/usage 事实、日/月汇总、有界 CSV 导出与追加更正；部分 Token 未知时不会编造完整成本，未定价和未知用量次数单列。接口和精度规则见 [用量与价格契约](docs/usage-management-contract.md)。

### 单实例账本与商业流程（默认关闭）

`/admin/api/v1/billing` 提供 employee、Key 或员工资源所有者范围内的定点余额、套餐快照、订阅购买/取消、待支付充值、兑换码和部分/全额退款。金额与已接受操作是不可变追加事实，相同 operation ID 只允许相同 actor/action/payload 幂等重放。商业执行在数据库中默认关闭，管理员可先配置套餐和加密 connector，再显式开启。

支付回调只是本地通用 HMAC 接口，覆盖五分钟时间窗、常量时间验签、事件反重放以及回调/支付/账本原子提交；它**不是 Stripe、Airwallex、二维码或任何真实供应商的生产接入**，也没有自动续费、税务、发票、通知或多实例协调。见[单实例计费契约](docs/single-instance-billing-contract.md)。

### 自动加密备份（默认关闭）

源码增加 `--automated-backups-enabled`、受限输出根、计划/运行历史、保留与恢复演练。当前只有 Windows 当前用户 DPAPI key provider 能进入 ready/running；Linux/macOS 不会降级到明文密钥，跨机器恢复材料、对象存储、远程复制和生产轮换仍未交付。离线密码 CLI 继续可用，细节见[备份与恢复](docs/backup-restore.md)。

## 1. 下载安装

**当前下载版本：[v0.1.0-preview.3](https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.3)**。下表链接直接下载该版本附件。

在 [GitHub Releases](https://github.com/surpaimb/cpa-cloud/releases) 查看预览版本及附件。仅在对应版本的 Assets 中存在的文件才是可下载交付；尚无附件时请使用下文源码构建。预览版本不一定出现在 GitHub 的 latest 链接中。

| 系统 | 附件文件名后缀 |
| --- | --- |
| Windows 普通 Intel/AMD 电脑 | [windows_amd64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64.zip) |
| Windows ARM 电脑 | [windows_arm64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64.zip) |
| Linux Intel/AMD 云服务器 | [linux_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.tar.gz) |
| Linux ARM 服务器 | [linux_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.tar.gz) |
| macOS Intel 芯片 | [macos_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_amd64.tar.gz) |
| macOS Apple Silicon（M 系列） | [macos_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_arm64.tar.gz) |

压缩包应包含程序、`web/` 网页目录和第三方声明。解压到独立目录，不需要安装 Go 或 Bun。保留所有声明文件。将数据放在压缩包目录以外，升级时更容易保留。

下载后使用 Release 中的 [SHA256SUMS.txt](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/SHA256SUMS.txt) 核对文件：Windows 可运行 `Get-FileHash <下载文件> -Algorithm SHA256`；Linux 使用 `sha256sum <下载文件>`；macOS 使用 `shasum -a 256 <下载文件>`，与清单对应行比较。

后续命令均在**解压后的程序目录**执行。Windows 程序名为 `cpa-cloud.exe`；Linux/macOS 为 `cpa-cloud`。Unix 如缺执行权限，可执行 `chmod +x ./cpa-cloud`。目前不承诺 Windows 代码签名或 macOS 公证，按公司策略评估来源及签名要求，不要全局关闭系统安全功能。

### 桌面安装版

本版共有 18 个主包：上面的 6 个便携包、下面的 6 个 Windows/macOS 桌面包和 6 个 Linux 桌面包。它们均由 [GitHub Actions 运行 35743039148](https://github.com/surpaimb/cpa-cloud/actions/runs/35743039148) 自动构建；Release 另附每个主包的独立校验文件和汇总清单。Windows 两种架构均通过安装、同包重装、卸载、NSIS/MSI 互斥拒绝与数据保留验收。

| 系统 | 安装包 | 安装方式 |
| --- | --- | --- |
| Windows Intel/AMD（NSIS） | [windows_amd64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64_Setup.exe) | 运行当前用户安装程序，再从开始菜单启动 |
| Windows ARM（NSIS） | [windows_arm64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64_Setup.exe) | 使用 ARM64 Setup，操作同上 |
| Windows Intel/AMD（MSI） | [windows_amd64.msi](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64.msi) | 使用 Windows Installer 安装到当前用户，再从开始菜单启动 |
| Windows ARM（MSI） | [windows_arm64.msi](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64.msi) | 使用 ARM64 MSI，操作同上 |
| macOS Universal（13 或更新） | [macos_universal.dmg](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_universal.dmg) | Intel 与 Apple Silicon 通用；打开 DMG，将 `CPA Cloud.app` 拖入 Applications |
| macOS Universal ZIP（13 或更新） | [macos_universal.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_universal.zip) | Intel 与 Apple Silicon 通用；解压后将 `CPA Cloud.app` 移入 Applications |

Windows 的 Setup.exe 与 MSI 是同一应用的两种替代安装方式，都会使用 `%LOCALAPPDATA%\Programs\CPA Cloud`，并会检测、拒绝另一种安装器管理的现有安装；它们不能并排安装。需要切换格式时，先卸载当前安装。两者都不会把用户数据放进安装目录。

Windows 安装版自带 .NET 运行时，无需另外安装。首次启动在原生窗口设置并确认管理员密码（12–72 个 UTF-8 字节），服务就绪后自动打开 `http://127.0.0.1:8787`；用户名为 `admin`。preview.3 已修复放大文字时初始化按钮可能不可见的问题。使用安装版可跳过下文第 2、3 节，直接进行第 4 节网页配置。

Windows 托盘或 macOS 菜单栏提供打开后台、启动、停止和退出。关闭浏览器不会停止服务；退出启动器会停止它启动的服务。端口 8787 被占用时会报错，需要先处理端口冲突。安装版默认仅本机访问；云端或内网部署请使用便携包及下文 HTTPS 配置。

数据保存在 Windows `%LOCALAPPDATA%\CPACloud\data` 或 macOS `~/Library/Application Support/CPACloud/data`，与安装目录分离。升级前退出启动器并备份数据，再安装新版；卸载程序或删除 Mac 应用不会主动删除此数据目录。启动器不会自动导入既有 CLI 数据，也不自动添加开机启动或下载更新。

安装预览未进行发行者代码签名或 Apple 公证；系统可能阻止首次打开，应按组织策略核验来源。当前验证包含 Windows 安装生命周期、各原生构建、Linux 包内容读回、自动化测试和 macOS DMG 挂载读回，不代表已完成所有系统上的真实 GUI 交互验收。

preview.3 包内 README 来自发布提交 `82d536b`，仍把 preview.2 写作当前下载并把部分 preview.3 功能称为“当前源码”或“下一预览版”。这是包生成时固定的旧措辞；最新下载状态与功能说明以本页和 Release 附件为准。

### Linux 桌面包

| 架构 / 发行版 | 安装包 | 安装或启动 |
| --- | --- | --- |
| Intel/AMD 通用 | [linux_amd64.AppImage](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.AppImage) | `chmod +x` 后直接运行 AppImage，不写入系统安装目录 |
| ARM64 通用 | [linux_arm64.AppImage](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.AppImage) | 在 ARM64 Linux 上 `chmod +x` 后直接运行 |
| Intel/AMD Debian/Ubuntu | [linux_amd64.deb](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.deb) | `sudo apt install ./cpa-cloud_v0.1.0-preview.3_linux_amd64.deb` |
| ARM64 Debian/Ubuntu | [linux_arm64.deb](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.deb) | `sudo apt install ./cpa-cloud_v0.1.0-preview.3_linux_arm64.deb` |
| Intel/AMD RPM 系发行版 | [linux_amd64.rpm](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.rpm) | `sudo dnf install ./cpa-cloud_v0.1.0-preview.3_linux_amd64.rpm` |
| ARM64 RPM 系发行版 | [linux_arm64.rpm](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.rpm) | `sudo dnf install ./cpa-cloud_v0.1.0-preview.3_linux_arm64.rpm` |

AppImage 可从终端运行 `./cpa-cloud_v0.1.0-preview.3_linux_<架构>.AppImage`。deb/rpm 把程序装到 `/usr/lib/cpa-cloud` 并添加 “CPA Cloud” 桌面入口；也可运行 `/usr/lib/cpa-cloud/cpa-cloud-launcher`。这些桌面入口会在终端中完成首次密码设置、运行仅监听 `127.0.0.1:8787` 的服务并用 `xdg-open` 打开浏览器；关闭该终端或按 Ctrl+C 会停止服务。deb/rpm 依赖 `bash`、`curl`、`util-linux` 和 `xdg-utils`。

Linux 桌面包的数据目录是 `${XDG_CONFIG_HOME:-$HOME/.config}/cpa-cloud`，日志是该目录下的 `server.log`。它位于 AppImage 和 deb/rpm 管理的文件之外，替换 AppImage 或卸载系统包不会主动删除数据；升级前仍应停止服务并备份完整数据目录。

## 2. 命令行首次初始化

初始用户名固定为 `admin`。密码通过 stdin 输入，长度为 **12–72 个 UTF-8 字节**（不是中文字符数）。初始化成功后程序退出，只需执行一次。

安装版通常通过首次启动窗口初始化。需要使用命令行时，必须沿用安装版数据目录。便携包示例使用程序目录旁的 `../cpa-cloud-data`；初始化与启动始终指向同一个目录。

### Windows PowerShell

**Windows 安装版（Setup.exe）**：下面整段可从任意 PowerShell 目录运行。先关闭初始化窗口；不要复制终端提示符或 Markdown 围栏。程序不存在时会在询问密码前停止。

```powershell
& {
    $exe = "$env:LOCALAPPDATA\Programs\CPA Cloud\cpa-cloud.exe"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe. Check the installation or portable path."
    }
    $secret = Read-Host 'Administrator password (12-72 UTF-8 bytes)' -AsSecureString
    $ptr = [IntPtr]::Zero
    $savedEncoding = $OutputEncoding
    try {
        $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
        $OutputEncoding = [Text.UTF8Encoding]::new($false)
        [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) |
            & $exe --data-dir $data --init
        if ($LASTEXITCODE -ne 0) { throw 'Initialization failed; see the message above.' }
    } finally {
        $OutputEncoding = $savedEncoding
        if ($ptr -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
        }
        if ($null -ne $secret) { $secret.Dispose() }
    }
}
```

**Windows 便携包（ZIP）**：将上面代码中的 `$exe` 和 `$data` 两行替换为下面示例，并将程序路径改为你实际解压的位置，然后执行修改后的整段代码。不会自动搜索或切换到其他数据目录。

```powershell
$exe = "C:\Tools\CPACloud\cpa-cloud.exe"
$data = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $exe) "..\cpa-cloud-data"))
```

成功后，安装版从开始菜单重新打开 CPA Cloud；便携版按第 3 节启动。安装版的数据目录是 `%LOCALAPPDATA%\CPACloud\data`，不要使用便携包的相对目录。

### Linux / macOS Bash

```bash
umask 077
read -r -s -p 'Administrator password (12-72 UTF-8 bytes): ' CPA_ADMIN_PASSWORD
printf '\n'
printf '%s' "$CPA_ADMIN_PASSWORD" | ./cpa-cloud --data-dir ../cpa-cloud-data --init
unset CPA_ADMIN_PASSWORD
```

成功提示：`Initialized administrator admin.`。若已初始化，使用已有目录启动；不要为重置密码删除数据。当前没有管理员密码重置命令。

## 3. 本机启动

**Windows 安装版**：通常从开始菜单打开 CPA Cloud 即可。如果需要前台命令行运行，请先退出托盘启动器，避免端口冲突；以下完整代码可从任意目录执行，使用安装版原有数据。

```powershell
& {
    $appDir = "$env:LOCALAPPDATA\Programs\CPA Cloud"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    $exe = Join-Path $appDir 'cpa-cloud.exe'
    $web = Join-Path $appDir 'web'
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $web 'index.html') -PathType Leaf)) {
        throw "CPA Cloud web files not found: $web"
    }
    & $exe --data-dir $data --listen 127.0.0.1:8787 --web-dir $web
    if ($LASTEXITCODE -ne 0) { throw 'CPA Cloud failed to start; see the message above.' }
}
```

**Windows 便携包**：把上面 `$appDir` 和 `$data` 两行换为下面的路径，修改为你实际解压的位置后执行整段。数据目录必须与初始化时一致。

```powershell
$appDir = "C:\Tools\CPACloud"
$data = [IO.Path]::GetFullPath((Join-Path $appDir "..\cpa-cloud-data"))
```

Linux / macOS：

```bash
./cpa-cloud --data-dir ../cpa-cloud-data --listen 127.0.0.1:8787 --web-dir ./web
```

服务启动成功后，点击 [打开本机管理后台](http://127.0.0.1:8787/)，使用 `admin` 登录。

也可以复制下面的地址到浏览器地址栏：

```text
http://127.0.0.1:8787/
```

此地址访问当前电脑，请在运行 CPA Cloud 的电脑上打开，并保持服务运行。健康检查 `/healthz` 返回 `{"status":"ok"}`。命令行模式在前台运行，Ctrl+C 停止；关闭终端通常会停止服务。

## 4. 网页配置

### 添加上游

preview.3 可选择 DeepSeek、OpenAI、Groq、Mistral 或 OpenRouter，自动填写已核实的官方 API 地址与显示名称；也可选择自定义服务。名称可以修改，切换服务商或修改地址后需重新填写 API Key。

点击“保存并同步模型”会先保存上游，再读取模型列表。若同步失败，上游仍已保存，点击“重试同步”即可，不要重复添加。已有上游可点击“同步模型”。API Key 无效、上游限流、不支持模型列表或超时会分别提示；不支持自动同步时，可在“模型路由”中手动输入。

| 字段 | 示例 / 说明 |
| --- | --- |
| 名称 | `Company API`，便于识别的名称 |
| 类型 | 当前仅 `openai-compatible` |
| Endpoint | `https://api.example.com/v1`，替换成供应商实际基础地址 |
| API Key | 供应商签发的上游 Key，不能填写员工 Key |

不要填完整 `/chat/completions` URL。以 `/v1` 结尾的 Endpoint 会追加 `/chat/completions`，其他基础路径会追加 `/v1/chat/completions`。使用可从服务主机访问的公网 HTTPS 地址；当前拒绝私网、链路本地、CGNAT 地址和重定向，也不使用 `HTTP_PROXY` / `HTTPS_PROXY`。

**平台部署在内网**不等于**支持内网模型上游**。内网员工可访问本平台，上游限制仍生效。`--allow-loopback-upstream` 只用于本机模拟测试，不是任意内网地址放行开关。

### 添加模型

1. 设置员工可见的模型 ID，例如 `company-chat`。
2. 选择刚添加的上游。
3. 填写该供应商实际支持、当前供应商 Key 有权访问的上游模型 ID。

preview.3 会自动读取上游模型作为候选；勾选所需模型，可修改默认的对外模型 ID，再点击“创建所选路由”。“添加模型路由”页面也支持从同步结果选择或手动输入。同步列表不会自动给员工开放所有模型，员工可用模型仍由已创建路由与员工权限决定。当前一个对外模型 ID 对应一条路由。

### 创建员工和 Key

1. 创建员工，填写姓名及可选部门、备注。
2. 选择全部模型或指定模型权限。
3. 创建 Key，为用途命名；不填到期时间即永久有效。
4. 立即复制只显示一次的 Key，通过公司认可的安全渠道交付。
5. 同时交付服务 Base URL 和模型 ID。Key 丢失后创建新 Key，再撤销旧 Key。

撤销后新请求被拒绝；不要假定已经开始的流式请求一定立即中断。

## 5. 员工配置与 API 验证

使用支持 **OpenAI Chat Completions** 的客户端：

| 配置项 | 本机示例 | 公司部署示例 |
| --- | --- | --- |
| Base URL | `http://127.0.0.1:8787/v1` | `https://ai.example.com:8787/v1` |
| API Key | 管理员生成的员工 Key | 管理员生成的员工 Key |
| Model | `company-chat` | 管理员公布的模型 ID |

远程员工不能用 `127.0.0.1` 连接管理员电脑，该地址指向员工自己的电脑。

CC Switch 可用于配置工具，但最终调用工具必须支持当前协议。**preview.3 下载包仅提供 Chat Completions；最新源码增加上述 Responses、Claude Messages 和 Gemini 原生子集。**最新源码已完成三种 CLI 的固定 Windows/合成上游实机矩阵，但 CC Switch GUI、真实供应商和会员账号仍未验证，且 Claude 取消仍有一个明确 FAIL；参见[员工接入说明](docs/employee-access.md)和[真实客户端兼容矩阵](docs/client-compatibility-matrix-2026-09-24.md)。

Bash + curl 测试示例（Key 交互输入，请求头经 stdin 传入）：

```bash
CPA_BASE_URL='http://127.0.0.1:8787'
read -r -s -p 'Employee Key: ' CPA_EMPLOYEE_KEY
printf '\n'
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- "$CPA_BASE_URL/v1/models"
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- -H 'Content-Type: application/json' \
  "$CPA_BASE_URL/v1/chat/completions" \
  --data '{"model":"company-chat","messages":[{"role":"user","content":"Hello"}],"stream":false}'
unset CPA_EMPLOYEE_KEY
```

流式调用将 `stream` 改为 `true` 并给 curl 添加 `-N`。真实上游调用可能产生供应商费用。

## 6. 云服务器与公司内网 HTTPS

服务无需图形桌面，管理员通过浏览器远程操作。**非回环监听必须同时设置 TLS 证书与私钥**。当前没有自动申请证书功能。

准备匹配服务域名、被员工设备信任的证书，配置 DNS 与相应端口的防火墙规则。先安装文件、使用同一数据目录初始化，再启动。Linux 示例：

```bash
/opt/cpa-cloud/cpa-cloud \
  --data-dir /var/lib/cpa-cloud \
  --web-dir /opt/cpa-cloud/web \
  --listen 0.0.0.0:8787 \
  --tls-cert /etc/cpa-cloud/fullchain.pem \
  --tls-key /etc/cpa-cloud/privkey.pem
```

访问 `https://ai.example.com:8787`，员工 Base URL 为 `https://ai.example.com:8787/v1`。Windows/macOS 使用同样参数并替换路径。

服务账户需能读程序、网页、证书，并能写数据目录；保护 TLS 私钥和数据目录不被其他普通用户读取。一个实例只使用一个数据目录，不启动多个进程共享同一 SQLite 数据库。

这里使用服务自身提供 TLS。当前没有可信反向代理配置；不要仅在代理终止 HTTPS 后转明文 HTTP，否则 Origin 校验与安全 Cookie 可能不匹配。仓库暂未提供 systemd、Windows 服务或 Docker Compose 安装方案；配置常驻运行前先完成前台启动验收。

## 7. 参数、数据与维护

| 参数 | 默认 / 用途 |
| --- | --- |
| `--data-dir` | 系统用户配置目录下的 `cpa-cloud`；建议显式指定绝对路径 |
| `--listen` | `127.0.0.1:8787` |
| `--web-dir` | 空；不设置则没有网页 |
| `--init` | 读取 stdin 初始化，然后退出 |
| `--tls-cert` / `--tls-key` | 必须成对设置；非回环监听必需 |
| `--experimental-codex-membership` | 仅最新源码；默认关闭 Codex 文件导入、会员请求及 OAuth 实验 |
| `--codex-oauth-client-id` / `--codex-oauth-redirect-uri` | 仅最新源码；同时设置才启用网页 OAuth 与自动/手动刷新，另需开启会员实验 |
| `--allow-loopback-upstream` | 默认关闭，仅本机开发测试 |
| `--scheduled-tests-enabled` | 默认关闭；启用已保存的本地凭据/目录定时测试 worker |

用 `cpa-cloud --help` 查看二进制参数。

数据目录包含 `cpa-cloud.db`、可能存在的 WAL/SHM 文件和 **`master.key`**。员工 Key 保存为带密钥摘要，上游凭据加密保存；主机管理员仍能访问运行中的秘密。丢失或替换 `master.key` 会破坏已有凭据的可用性。

最新源码提供独立的 `cpa-cloud-backup create|verify|restore` 加密备份命令，可从活动 WAL 数据库取得一致快照，并只恢复到不存在的新目录；用法、安全边界和 128 MiB 首批上限见[加密备份文档](docs/backup-restore.md)。它不包含自动计划、保留、远程上传或原地回退。升级前仍应保留匹配的旧程序和网页，并先在隔离环境验证恢复结果；不要仅复制运行中的主数据库文件。

## 8. 从源码构建

需要 Git、Go 1.26 或更新兼容版本、Bun，加入 PATH。当前验证工具版本为 Go 1.26.6、Bun 1.3.14。

```sh
git clone https://github.com/surpaimb/cpa-cloud.git
cd cpa-cloud
```

Windows PowerShell：

```powershell
Push-Location web
bun install --frozen-lockfile
Pop-Location
.\scripts\build.ps1
cd .\dist\windows-amd64
```

在该输出目录可按前文初始化和启动。脚本支持 `-GoExecutable`、`-BunExecutable` 指定路径，以及 `-TargetOS windows|linux|darwin`、`-TargetArch amd64|arm64`。`-SkipWeb` 只构建服务端，不打包网页。

Linux / macOS Bash（从仓库根目录构建本机架构）：

```bash
(cd web && bun install --frozen-lockfile && bun run build)
mkdir -p dist/local
go build -trimpath -o dist/local/cpa-cloud ./cmd/cpa-cloud
mkdir -p dist/local/web
cp -R web/dist/. dist/local/web/
cd dist/local
```

源码构建不等于可分发包；对外分发还须包含第三方原文声明和对应版本信息。

## 9. 常见问题

| 问题 | 检查方式 |
| --- | --- |
| 页面 404 | `--web-dir` 应直接包含 `index.html`，先构建网页 |
| 要求初始化 / 找不到密钥 | 初始化与启动的数据目录及运行账户是否一致 |
| 密码被拒绝 | 12–72 UTF-8 字节，末尾换行被移除 |
| 远程监听失败 | 提供成对有效的 TLS 证书与私钥 |
| 登录或写入失败 | 检查浏览器 Origin 与服务域名、端口、协议是否一致 |
| 上游地址被拒绝 | 检查 HTTPS、DNS、公网地址和路径；测试开关只开放回环 |
| 模型列表为空 | 检查模型路由、上游启用状态和员工权限 |
| 员工 401 / 403 | 检查 Key、撤销/到期、员工状态与模型权限 |
| 上游失败 | 检查供应商 Key、额度、模型 ID、网络和证书；报障时不粘贴秘密 |
| Codex / Claude Code 调用失败 | 核对源码与下载版本、协议、上游类型和支持字段；preview.3 无 Responses/Messages，不一定是 Key 错误 |

## 10. 开发验证

GitHub Actions 按改动范围执行：仅修改文档不触发构建；服务端和网页提交分别运行测试与编译检查，不生成安装包。修改某个平台的启动器或打包脚本时，只验证该平台；修改共用打包代码时验证所有受影响平台。相同分支的新提交会取消过时的日常检查。

完整安装包仅在推送预览版本标签或手动运行构建工作流时生成。`Native package validation` 可手动选择 Windows、Linux、macOS 或全部平台，只上传 CI 附件；`Preview release` 仅在预览标签推送时发布 Release。日常开发无需反复创建版本标签。

```bash
go test ./... -count=1 -timeout=2m
go vet ./...
(cd web && bun run test && bun run build)
```

安装 Node.js 后运行模拟上游验收（参数须为绝对路径）：

```text
node scripts/smoke-preview.mjs <absolute-executable-path> <absolute-web-directory>
```

覆盖初始化、网页入口、管理 API、永久 Key、非流式/SSE、凭据隔离、重启与撤销持久化，不需要真实凭据。Race 测试需要支持 CGO 的工具链；服务与会员模块已通过 [Linux CI race 验证](https://github.com/surpaimb/cpa-cloud/actions/runs/35802158197)，本机 Windows 未运行 race。

源码新增的 Responses 可独立验收；脚本启动临时服务和假上游，验证工具结果回合、失败事件、员工权限与撤销，并在退出时清理测试数据：

```bash
node scripts/smoke-responses.mjs <absolute-executable-path>
```

后台 OAuth 的进程级验收只检查开关、CSRF、授权会话幂等、配置变更与重启、无效 revision 及加密落盘；不访问供应商授权或 Token 端点。模拟授权交换、刷新与员工 Chat/Responses 调用由 Go 测试覆盖：

```bash
node scripts/smoke-codex-oauth.mjs <absolute-executable-path>
```

## 文档与许可证

- [集成证据](docs/integration-status.md) · [真实客户端兼容矩阵](docs/client-compatibility-matrix-2026-09-24.md) · [开发计划](docs/development-plan.md) · [接口契约](docs/preview-contract.md)
- [完整功能对齐计划](docs/feature-parity-plan.md) · [Responses 契约](docs/responses-preview-contract.md) · [任务分工](docs/work-coordination.md)
- [产品规划](docs/product-plan.md) · [核心设计](docs/core-design.md) · [验收矩阵](docs/acceptance-matrix.md)
- [会员接入研究](docs/research/membership-feasibility.md) · [协议来源](docs/protocol-sources.md)
- [独立实现说明](docs/independent-implementation.md) · [贡献规则](CONTRIBUTING.md)
- [第三方声明](THIRD_PARTY_NOTICES.md) · [依赖盘点](docs/research/dependency-notices.md)

本项目依据公开协议独立编写。此前接触过参考源码，不宣称严格洁净室开发。自有代码暂拟 MIT，但尚未加入正式 LICENSE，当前不声明已授予 MIT 许可。第三方依赖适用各自许可证，分发时保留相应声明。
