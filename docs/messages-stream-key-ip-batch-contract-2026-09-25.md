# Messages↔Responses SSE 与 Key 来源 IP/CIDR 批次契约

状态：2026-09-25 实施前共同接口。基线是已合入 PR #7、PR #8 的 `main`
`785f6497cc8873a8223436f1d49bdf9f7c05f485`。本批只交付两个可独立审阅和回滚的小里程碑：

- `PROTO-07`：在已有六方向非流式转换和 Chat↔Responses SSE 之上，增加 Messages↔Responses
  两方向的文本与客户端 function tool SSE；
- `KEY-02` 第二段：在已有每 Key 协议/公开模型策略中增加真实 socket peer 的 IP/CIDR 来源限制，
  复用同一个 policy revision、CAS、管理 API 和管理员 Key 页面。

本批不增加 Gemini 跨协议 SSE、thinking、prompt cache 控制、媒体、citations、服务端托管工具、
Responses state/background/previous/conversation 的跨协议转换、可信代理链、分组策略、通用硬配额、员工
自助或支付。实现继续遵守[独立实现规则](independent-implementation.md)，只使用本项目规格、官方公开协议和
独立测试。

## 1. 共同不变量

1. 员工鉴权、来源限制、策略、路由、预算、持久派发和上游执行仍在一个 Go 服务进程内；不增加逐请求
   管理服务 HTTP 调用。
2. 公共入口使用 `ClientProtocol` 做员工与 Key 治理；`UpstreamProtocol` 只表示实际 wire，并决定原始
   usage 观察和价格归因。转换不能用 wire 权限替代公共入口权限。
3. 协议、公开模型与来源地址三项取交集。Key 策略只能收紧员工权限和当前全局可用路由，不能恢复过期/
   撤销 Key、停用员工、归档模型或扩大员工 grant。
4. 请求准备、严格预算证明、最终策略 revision/内容重核必须在 attempt 创建和任何上游网络之前完成。
   不可表达或无法证明上界的转换仍为零 attempt、零网络。
5. 最终派发事务重核初始冻结的 policy revision、`ClientProtocol`、公开模型和可信 socket peer。任何变化，
   包括 `A → B → A`，都失败关闭。已经持久派发的唯一尝试按原快照完成、取消或中断，不重放。
6. 原始上游 SSE JSON 在转换前按实际 `UpstreamProtocol` 观察；未知 usage 保持未知。日志、策略、错误、
   审计和页面不保存员工 Key、上游凭据、认证头、提示词、回复或工具参数正文。

## 2. PROTO-07：Messages↔Responses 跨协议 SSE

### 2.1 开放范围

只有显式持久化 `wire_protocol` 的以下方向开放：

| ClientProtocol → UpstreamProtocol | 首批可表达范围 |
| --- | --- |
| `anthropic-messages` → `openai-responses` | 文本、Messages system/user/assistant、客户端 `tool_use`/`tool_result`、function 定义与选择、基础采样/输出上限、流式文本与 function 参数 |
| `openai-responses` → `anthropic-messages` | 无状态文本/instructions、function 定义/调用/输出、既有非流式转换已支持参数、流式文本与 function 参数 |

请求转换复用现有 `MessagesRequestToResponses` / `responsesRequestToMessages` 的严格子集，不新开第二套宽松
请求语义。`store`、background、`previous_response_id`、conversation、provider/hosted/MCP/computer/
shell tool、thinking/reasoning、prompt cache 控制、图片/音频/文件、citations、structured output 和未知字段均在
durable dispatch 前返回 `unsupported_feature`。原生 Messages、原生 Responses、Chat↔Responses SSE 和六个
非流式方向不得回退。

### 2.2 官方事件与可转换状态机

Messages wire 按官方顺序接受：一个 `message_start`；零个或多个按连续 `index` 打开的
`content_block_start`，对应同 index 的若干 `content_block_delta` 与一个 `content_block_stop`；一个携带
或多个携带累计 usage 的 `message_delta`；最后一个 `message_stop`。每个 `message_delta.usage.output_tokens`
均为必需累计值，出现的 input/cache 字段覆盖先前累计事实但不得下降，省略字段保留既有值而不求和；
`stop_reason` 可在首个 delta 从 null 变为合法终因，后续只能省略、保持 null 或重复相同值，冲突值失败关闭。
任意数量的合法 `ping`
可以穿插，验证 `event` 与 JSON `type` 一致后作为非语义元数据忽略。官方可能新增的未知事件不能假定为
无语义；除本契约明确列出的 ping 外一律失败关闭。

首批只接受 `text` block 的 `text_delta` 和客户端 `tool_use` block 的 `input_json_delta`：

- block index 必须从 0 连续递增，不能重开、交错关闭、重复关闭或在关闭后继续 delta；
- tool id 非空且唯一，name 满足既有 function 名称规则；partial JSON 按原片段增量输出，但在 block stop
  必须拼成一个完整 JSON object，不能把数组、标量或截断 JSON 作为成功工具参数；
- `message_start.usage` 的 input 事实和 `message_delta.usage` 的累计 output 事实由 raw observer 保留；转换给
  员工的基础 input/output/total 仅使用目标协议可表达字段。cache-specific 计数不伪造到 Responses 字段，
  仍可由实际 Messages usage 账本保存；带 cache 控制的请求不在本批；
- `end_turn`、`stop_sequence` 和 `tool_use` 是成功终止原因；`max_tokens` 映射为
  `response.incomplete`/`max_output_tokens`，是已验证但非成功的终态；其他 stop reason 失败关闭；
- Messages `error` 映射为固定脱敏的 Responses failure，不转发上游 message。缺少 `message_delta`、缺少或
  重复 `message_stop`、终态后数据、物理 EOF 前不关闭以及提前 EOF 均不得生成成功终态。

Responses wire 只接受本项目现有严格 decoder 已支持的 `response.created`/`response.in_progress`、message
output item、output text 和 function call arguments 增量/完成事件，以及一个最终
`response.completed`、`response.incomplete` 或 `response.failed`。输出 item/content index、response/item/
call ID、function name 和 arguments 必须前后一致；任意拆包、多行 `data` 和注释只由有界 SSE decoder
重组，不改变事件顺序。

- text item 映射为 Messages `text` block；function call 映射为 `tool_use` block，保持一个稳定 tool id、
  name 和完整 object 参数；并行工具按 Responses `output_index` 的稳定顺序输出；
- `response.completed` 映射为携带 `end_turn` 或 `tool_use` 的 `message_delta` 与 `message_stop`；
- 只有 `response.incomplete` 且 reason 为 `max_output_tokens` 才映射为 Messages `max_tokens` 非成功终态，
  其他 incomplete reason 失败关闭；`response.failed`/error 映射为固定脱敏 Messages error；
- Responses 官方顶层字符串 `obfuscation` 继续只作为非语义元数据接受；未知事件或字段若可能承载输出
  语义则失败关闭，不能静默丢弃。

Anthropic 目标 wire 要求 `message_start.message.usage` 和 `message_delta.usage` 均存在，转换不得为未知事实
伪造 0，也不得省略必需对象。若 `response.created` 或较早的 `response.in_progress` 已提供完整可表达 usage，
可在上游生成期间实时释放转换事件；若直到 terminal 才得到完整累计 usage，转换器只能在既有字节/事件
上限内有界暂存，并在 terminal 后按合法顺序整体释放，属于全流延迟而非生成期间实时。若到成功或
incomplete terminal 仍缺少目标必需 usage，则在未输出非法终态的前提下失败关闭。原生 SSE 与
Chat↔Responses SSE 不受此限制。

### 2.3 纯转换与 bridge 接口

D 只扩展 `internal/protocolconv` 的既有 `StreamConverter`，增加独立的
`MessagesToResponsesStream` 与 `ResponsesToMessagesStream` 状态机和测试。`SSEEvent` 增加可枚举终态结果，
使 bridge 能区分成功、incomplete 和 failed，而不是把任意合法终态都记成成功：

```go
type StreamTerminalOutcome string

const (
    StreamTerminalNone       StreamTerminalOutcome = ""
    StreamTerminalCompleted  StreamTerminalOutcome = "completed"
    StreamTerminalIncomplete StreamTerminalOutcome = "incomplete"
    StreamTerminalFailed     StreamTerminalOutcome = "failed"
)

type SSEEvent struct {
    Name            string
    Data            json.RawMessage
    Semantic        bool
    Terminal        bool
    TerminalOutcome StreamTerminalOutcome
}
```

`Terminal=false` 必须配 `StreamTerminalNone`；终态必须配非空 outcome。Chat↔Responses 的既有成功终态标为
`completed`，行为不变。bridge 仍只执行一次 `client.Do`，同步写形成背压，在原始事件观察成功后才转换；
终态继续暂存到物理 EOF、尾部状态机和 drain deadline 验证完成后再写。`protocolStreamResult` 同样报告
`TerminalOutcome`；只有 `completed` 且终态 write/flush 成功才 `Completed=true` 和结算 `succeeded`。
`incomplete`/`failed` 可把对应的脱敏客户端终态写完，但账本分别结算 `interrupted`/`failed`，绝不伪造
成功。下游 write/flush/deadline 失败和取消沿既有路径结算 `cancelled`。

读取继续限制单行、单事件和全流字节；写入继续使用 30 秒 deadline。短写、`n>0,err!=nil`、flush error、
终态 flush failure 或不能设置/清除 deadline 都立即取消唯一上游请求；下游一旦可能观察到任何 SSE 字节，
不再补普通 JSON、第二组 header、重试或换号。

### 2.4 四环证据

两方向验收必须同时证明四层没有断裂：

1. 请求语义：同一公开模型和 function/tool 回合经现有严格 request converter 到唯一实际 wire；
2. 原始事实：usage observer 在转换前按实际 `UpstreamProtocol` 收到原始 frame，价格/attempt 使用实际 wire；
3. 员工增量：员工看到自己协议的文本和 function 参数增量、稳定 ID/index 和正确终态；
4. 持久终态：parent/attempt 只有一个，completed/incomplete/failed/cancelled/interrupted 与真实状态一致。

专项测试覆盖任意字节拆包、LF/CRLF/CR、多行 data、注释、ping、行/事件/全流上限、文本、多个/并行
function、空参数片段、非法 JSON、ID/index/顺序冲突、未知事件、error/failed/incomplete、缺/重复/坏终态、
EOF/drain、写/flush失败、30 秒停止读取客户端和取消无重放。真实进程只跑本批两个可表达方向和最小实际
CLI 场景；CLI 固有不支持项标为 `UNSUPPORTED`，不通过放宽服务端语义伪造成兼容。

## 3. KEY-02：真实 socket peer 的 IP/CIDR 第一段

### 3.1 策略对象与管理 API

已有策略对象增加：

```json
{
  "revision": 4,
  "protocol_mode": "selected",
  "protocols": ["openai-responses"],
  "model_mode": "all",
  "models": [],
  "source_mode": "selected",
  "source_cidrs": ["10.20.0.0/16", "2001:db8::/48"]
}
```

`source_mode` 仅为 `all|selected`。`all` 要求 `source_cidrs=[]`；`selected` 要求数组显式存在，空数组表示拒绝
所有来源。协议、模型和来源共享同一个只增 `revision` 和 CAS；不创建第二个可能竞争的 IP revision。

- 创建 Key 时整个 `policy` 省略仍是 `all/all/all`。提供现有四个协议/模型字段的旧管理客户端可省略
  `source_mode` 与 `source_cidrs`，等价于新 Key 的 `all/[]`；若出现其中一个，两个必须同时出现。
- `PUT /admin/api/v1/keys/{id}/policy` 仍全量替换协议与模型。IP 两字段都省略时保留当前 IP 策略，避免旧
  客户端无意清除限制；只出现一个是 `400 invalid_key_policy`；显式 `source_mode:"all",source_cidrs:[]`
  才重置来源限制。无论保留还是替换，成功写入都按既有 CAS 递增同一 revision。
- GET、Key 列表和创建响应总是返回规范化后的 `source_mode` 与 `source_cidrs`；不增加会误导为另一层
  授权交集的 `effective_source_cidrs`。页面明确说明限制针对 TCP
  socket peer；在反向代理后通常看到代理地址，不承诺最终用户地址。

### 3.2 输入规范化

每个 `source_cidrs` 成员接受裸 IPv4/IPv6 地址或 CIDR：裸 IPv4 规范为 `/32`，裸 IPv6 规范为 `/128`；
CIDR 使用 masked network address 的 `net/netip` 标准字符串保存。规则固定如下：

- 最多 64 项，每个原始 UTF-8 字符串最多 64 字节且数组总计最多 4096 字节；空串、首尾空白、内部空白、无效地址、无效 prefix
  或配置值中的 IPv6 zone (`%...`) 全部拒绝，不截断、不忽略；
- IPv4-mapped IPv6 裸地址先 `Unmap` 为 IPv4 `/32`。mapped prefix 只有 prefix length `>=96` 才允许，
  转为对应 IPv4 prefix（减 96）；更宽的 mapped prefix 拒绝；
- 规范化后按地址族、网络地址和 prefix 排序；规范化后重复的成员返回 `invalid_key_policy`，不静默去重；
- IPv4 peer 只匹配 IPv4 prefix，规范 IPv6 peer只匹配 IPv6 prefix。socket peer 的 mapped IPv4 同样先
  `Unmap`，因此可匹配 IPv4 policy。

### 3.3 可信来源和 header 边界

本段可信来源只有 Go HTTP server 提供的 `Request.RemoteAddr` 对应真实 socket peer。服务端只接受
`host:port`（含 bracketed IPv6）；无端口裸值不是服务端 socket 地址，测试也必须提供端口。peer 自带
IPv6 zone、RemoteAddr 缺失、主机不是 IP、端口非法或地址无法规范化时全部失败关闭。首批因此不支持
带 zone 的 link-local peer。

`Forwarded`、`X-Forwarded-For`、`X-Real-IP` 及任何类似 header 在本批全部忽略，即使请求来自回环或已知
反向代理也不读取。代理部署看到并判断的是代理 peer；可信代理列表和链式来源解析必须作为后续独立安全项，
不能在配置、UI 或文档中冒充已支持终端用户真实 IP。

来源不允许时，四模型入口和两个模型目录都复用现有 `model_not_allowed` code 与不区分来源/协议/模型的
固定 403 授权文案，不能通过目录空列表、feature
flag、页面或伪造 forwarding header 绕过。拒绝发生在治理、预算、租约、parent/attempt 和网络之前。

### 3.4 迁移、最终事务和后台资源

既有 v1 policy schema 与 marker 保持原样；本批新增 `access_key_policy_sources`（每个 policy 恰一行、
`source_mode` CHECK）和 CIDR 成员表/索引，以及独立 durable source migration marker。新库在同一总迁移
事务创建 v1 与 source schema。合法 v1 库必须先完整验证原 marker/schema/外键/索引/每 Key 覆盖，再在
同一事务为每个现有 policy 写入 `source_mode=all`、空 CIDR、保留原 policy revision，验证一一覆盖后才写
source marker。任一步失败整体回滚。

若原 policy 已有任意对象却缺原 marker，或 source 已有任意对象却缺 source marker，或任一 marker/schema
不符、某个现有 Key 缺 policy、某个 policy 缺 source row、成员孤立或迁移不完整，启动失败关闭；不得
通过重新 backfill `all` 来恢复权限。新 Key 仍与默认 policy/source 同事务创建。

初始认证把规范 socket peer 和 policy revision 冻结在内部授权上下文。最终已有 `*sql.Tx` 必须重读同一
policy，验证 revision、`ClientProtocol`、公开模型及 `AllowsSource(current,capturedPeer)` 后才创建 attempt
和标记派发；策略 ABA、来源被收紧、旧 snapshot 或缺 policy 均为零 attempt、零上游。

Responses 资源规则：

- 创建有状态/后台资源使用当前请求的真实 peer，并持久化必要的规范 `source_addr` 与 policy revision；不
  保存 forwarding headers 或请求正文作为来源证据；
- 尚未派发的后台任务在 claim 和最终 dispatch 均用创建时捕获的 peer 与新 revision 重核，绝不把 worker
  自己的 loopback 当员工来源，也不能跳过 IP 限制；收紧后固定终结且零 attempt/零网络；
- 已经持久派发的任务按原快照结算，不因收紧重放；
- 读取资源正文/状态或继续生成属于数据读取/新执行，要求当前有效 Key、精确所有权、当前请求 peer 以及
  当前协议/模型/IP 策略全部允许；收紧后拒绝；
- cancel/delete 不返回新正文、不创建模型派发，只要求仍有效的同一 Key 精确拥有资源。即使协议、模型或
  IP 已收紧，也保留该 Key 停止或删除自己已授权任务的能力；撤销/过期 Key 不因本规则恢复权限。

迁移、旧库、注入失败回滚、重启、CAS/ABA、IPv4、IPv6、mapped IPv4、zone、无效/重复/超限输入、
RemoteAddr 失败、三个伪造 forwarding header、跨 Key 资源、目录/四入口零 attempt 拒绝和后台 claim 都要
有自动化覆盖。管理员页面必须在真实临时服务中提供桌面和移动证据，并明确展示 socket peer/代理限制。
Messages↔Responses 真实临时进程验收必须分别记录：较早 usage 已知时的生成期间增量；仅 terminal usage
已知时的有界全流延迟；成功/incomplete terminal usage 仍未知时的失败关闭；以及 raw upstream usage 入账、
completed/incomplete/failed 终态、取消、背压与无重放。纯转换测试或结构等价累积器不能描述成官方 SDK/CLI
实际成功。

## 4. 文件与符号所有权

### D 组件

- `internal/protocolconv/messages_stream.go`、`messages_stream_test.go`；
- `SSEEvent` 当前所在的 `internal/protocolconv/stream.go` 可做增加 `TerminalOutcome` 及把既有 Chat 成功终态
  标为 `completed` 的最小修改，并补相应既有 stream 专项；
- 为注册两个新 plan 可最小修改 `internal/protocolconv/stream_execution.go` 及其测试；
- 明确分配的窄 bridge 仅为 `internal/service/protocol_runtime_stream.go` 与
  `protocol_runtime_stream_test.go`：传播/验证 `TerminalOutcome`，不做 handler 或账本结算；
- 不修改 handler、auth、accounting、feature flag、store、background、网页或现有 native 实现。

### E 组件

- `internal/keypolicy/source.go`、`source_test.go`，以及 `policy.go`/专项测试中的纯值对象、规范化、匹配和
  CIDR 成员存取；
- `internal/service/key_policy_admin.go` 与专项测试的窄 DTO/省略保留/CAS adapter；
- `web/src/api.ts`、`web/src/pages/EmployeesPage.tsx`、`web/src/styles.css` 和 Key policy 页面测试；
- 不修改 App/store migration orchestration、员工认证、入口 handler、最终派发、response resource 或
  background worker。

### 集成组件

- 拥有 `internal/keypolicy/migration.go` v1→v2 严格迁移；
- 拥有 `internal/service/app.go`、`store.go`、公开入口与目录、employee auth/context、
  `key_policy_runtime.go`、最终 transaction、`protocol_runtime_http.go`、
  usage/accounting、Responses resource/background 的共享接线与组合测试；
- 拥有 `docs/**`、`scripts/**`、`.github/workflows/**`、真实进程/浏览器验收和最终 PR/CI。

两个组件各自从基线 `785f6497cc8873a8223436f1d49bdf9f7c05f485` 建新 `codex/` 分支，保留旧交付分支。
组件不得用放宽旧测试或 native 路径换取通过。集成时分别形成独立 PR；第二个 PR 必须在第一个合入后的新
main 上重基并重跑交叉边界，不能让两个 PR 共享未合入提交。

## 5. 明确排除与计划校准

严格组织级多租户、租户域名、员工 SSO/OIDC/第三方登录、管理员密码重置命令，以及提示词/响应正文审计
是用户明确排除项，不应继续出现在 M3 或存储/运维出口。普通注册、自助门户、可选商业能力和其他单实例
目标仍保留。恢复证据只表示同一 Windows 用户、不同 store/新目录的合成恢复，不写成异机或真实第二环境
验证。

PR #7 已交付 Chat↔Responses SSE，PR #8 已交付每 Key 协议/公开模型策略；它们是本批基线，不再列为待
实施。本批仍不访问真实供应商/会员账号、不操作现有 8787 服务、不创建包、tag、部署或发布。

## 6. 公开协议来源与依赖

2026-09-25 核对：

- OpenAI [Responses streaming events](https://platform.openai.com/docs/api-reference/responses-streaming)；
- Anthropic [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)，用于事件顺序、
  ping、error、text delta、tool `input_json_delta` 与累计 usage；
- WHATWG [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)，用于 framing。

本契约阶段不引入第三方运行时依赖；Go `net/netip` 来自标准库。后续若新增依赖，必须同时记录版本、许可
证和用途。
