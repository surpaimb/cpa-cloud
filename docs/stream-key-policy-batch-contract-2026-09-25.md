# Chat↔Responses SSE 与独立 Key 访问策略批次契约

状态：2026-09-25 实施前共同接口。基线是 `main`
`481f8b788aba2b2f159808b2ddabe8f09eaa526a`。本批只交付两个可独立审阅和回滚的小里程碑：

- `PROTO-07`：把现有 Chat Completions ↔ Responses 的文本与 function SSE 状态机接入显式
  `wire_protocol` 真实执行链；
- `KEY-02` 第一段：为每个员工 Key 增加独立的公共入口协议和公开模型 allowlist、CAS 管理 API 与
  管理员网页。

Messages/Gemini 跨协议 SSE、媒体、托管工具、跨协议有状态/后台转换、IP/CIDR、账号组绑定、通用
TPM 硬上界、员工自助和生产支付不进入本批。实现继续遵守[独立实现规则](independent-implementation.md)，
只使用本项目规格、公开协议和独立测试。

## 1. 共同不变量

1. 员工鉴权、策略判定、路由、预算、持久派发和上游执行仍在同一 Go 服务进程；不得为策略增加逐请求
   管理服务 HTTP 跳转。
2. Key 策略只会收紧员工权限和当前全局模型可用性，不能恢复已撤销/过期 Key、启用停用员工、启用
   归档模型或扩大员工权限。管理网页不是安全边界，四个公开协议入口和后台派发均由服务端重核。
3. 管理写接口继续要求管理员会话、Origin 与 CSRF。员工 Key 明文仍只在创建成功响应展示一次；策略表、
   日志、错误、审计和网页列表不得保存或回显 Key、上游凭据、提示词或响应正文。
4. 路由的 `wire_protocol` 是实际上游协议；Key 策略的 `ClientProtocol` 是员工调用的公共入口协议。转换
   请求始终按公共入口授权，不能因实际 wire 被允许而绕过，也不能因转换后的模型名与公开 ID 不同而放行。
5. 任何最终派发前的 Key 策略 revision/内容变化都在现有 `*sql.Tx` 派发屏障内失败关闭；过期快照不产生
   上游 I/O 或 accounting attempt。已经持久派发的尝试按原快照完成、取消或中断并结算，不换号重放。
6. 原始上游 usage 在转换前按实际 wire 观察；未知值保持未知。SSE 缺终态、畸形、超限或未知事件不得
   伪造 `response.completed`、Chat finish chunk 或 `[DONE]`。

## 2. KEY-02 第一段：数据和 API

### 2.1 ClientProtocol 枚举

策略只接受以下 v1 公共入口值：

```text
openai-chat
openai-responses
anthropic-messages
gemini-generate-content
```

`openai-chat` 对应 `/v1/chat/completions`；它显式映射到 accounting/governance 已有的
`openai-chat-completions` 值。其余三个值与现有公开入口同名。以上枚举不表示实际上游 wire。
`protocol_mode=all` 在 v1 只表示这四个已知入口；未来增加新入口必须经过 schema/契约迁移，不能让旧
`all` 策略静默获得新协议权限。

### 2.2 策略对象

每个 `access_keys.id` 恰有一个策略对象：

```json
{
  "revision": 1,
  "protocol_mode": "all",
  "protocols": [],
  "model_mode": "all",
  "models": [],
  "effective_protocols": ["openai-chat", "openai-responses", "anthropic-messages", "gemini-generate-content"],
  "effective_models": ["public-model-id"]
}
```

- `all` 模式要求对应数组恰为空；`selected` 模式要求数组显式存在，允许空数组。`selected + []` 明确
  表示禁止该 Key 使用所有协议或所有模型，不退化为 `all`。
- 数组必须是去重后的有效值；服务端拒绝重复、空白、未知协议、未知/归档公共模型和真实上游模型名，
  不静默去重或忽略。`models` 保存的是 `models.id` 公共 ID。
- `effective_*` 是只读快照，不参与写入指纹或 CAS，可能因员工权限、模型启停/归档或系统能力变化而在
  policy revision 不变时缩小。协议有效集是 v1 支持集与 Key 协议策略的交集；模型有效集是当前可用公开
  模型、员工模型策略与 Key 模型策略的交集。
- Key `model_mode=all` 会随员工的有效模型范围扩大；`selected` 只允许保存的集合。管理员只能把当前员工
  有权且未归档的公共模型放入 `selected`，因此不能预埋从未授予员工的未来 grant。若员工权限先收紧后
  恢复，仍在 Key 已保存集合内的模型可重新生效；员工收紧始终立即缩小 effective 集。
- Key `protocol_mode=all` 只覆盖上列 v1 四值；`selected` 不因后续系统能力扩大而增加新枚举。

### 2.3 迁移与创建

新增严格验证的 `access_key_policies`、`access_key_policy_protocols` 和
`access_key_policy_models` 表；外键均指向 `access_keys`，策略 revision 从 1 开始且只递增。迁移必须验证
表、列、约束、索引和外键，结构不符整体回滚并允许修复后重试。

每个既有 Key 在同一迁移事务内得到 `protocol_mode=all`、`model_mode=all`、revision 1 和两个空成员表，
等价于升级前行为，不新增 v1 以外 grant。新 Key 与默认策略必须在一个事务创建；不能出现可认证但缺策略
的 Key，读取到缺策略时 fail closed。

`POST /admin/api/v1/employees/{employee_id}/keys` 新增可选 `policy`：

```json
{
  "name": "CI",
  "operation_id": "opaque-id",
  "expires_at": null,
  "policy": {
    "protocol_mode": "selected",
    "protocols": ["openai-responses"],
    "model_mode": "selected",
    "models": ["public-model-id"]
  }
}
```

整个 `policy` 省略时创建 v1 `all/all`，保持旧客户端行为；一旦出现 `policy`，四个字段必须全部出现。
这不是部分更新。相同 `operation_id` 的重试必须核对包含 policy 在内的原始语义；变化内容返回冲突，且
不会再次显示 Key 明文。

### 2.4 查询和全量替换

- `GET /admin/api/v1/employees/{employee_id}/keys` 的每个 Key 增加可选 `policy`。升级后的服务总是返回；
  网页面对旧服务缺字段时显示“不支持独立 Key 策略”，不得乐观假定 `all`。
- `GET /admin/api/v1/keys/{key_id}/policy` 返回上述策略对象。
- `PUT /admin/api/v1/keys/{key_id}/policy` 是全量替换：

```json
{
  "expected_revision": 3,
  "protocol_mode": "selected",
  "protocols": [],
  "model_mode": "all",
  "models": []
}
```

字段不得省略；显式切回 `all` 且数组为空就是重置。成功 revision 加一。过期 revision，包括
`A → B → A` 的 ABA，返回 `409 revision_conflict`；不存在或不属于任何有效员工的 Key 返回脱敏 404；
未知 mode/协议、重复成员、空白或未知/归档模型返回 `400 invalid_key_policy`。撤销 Key 不被策略更新恢复。

系统状态增加 `features.key_access_policy=true`，只在迁移、管理 API、四入口鉴权、目录过滤、后台/资源
规则和最终派发重核全部接线后置 true。

### 2.5 目录、前台、后台与资源语义

- `/v1/models` 只有当 Key 允许 `openai-chat` 或 `openai-responses` 至少一个入口时才列模型；结果再按
  employee ∩ Key model policy ∩ 当前全局可用模型过滤。它是 OpenAI 公共目录，不把实际 wire 当权限。
- `/v1beta/models` 要求 Key 允许 `gemini-generate-content`，并应用相同模型交集。禁止目录协议时对已认证
  Key 返回成功空列表，不泄露未授权模型。Anthropic 当前没有独立模型目录。
- Chat、Responses、Messages 和 Gemini 的 native/显式转换请求都在治理、预算、账号池租约、attempt 和
  网络之前按公共入口协议与公共模型拒绝。统一内部拒绝为 Key policy denial；员工可见错误保持各协议
  envelope，HTTP 403，且不说明是员工、协议还是模型哪一层拒绝。
- 初次准入冻结 policy revision。现有 `dispatchModelRoute` 的同一事务在价格/预算、attempt 创建和
  `MarkAttemptDispatched` 之前重读 Key policy、员工权限和路由；revision 或授权不一致时零 attempt、零
  上游。策略提交后通知现有账号池/准入等待者重读。
- 创建 `store`/`previous_response_id`/background Responses 资源前要求当前 Key 允许
  `openai-responses` 和公共模型。读取自有资源正文/状态也重查当前 employee 与 Key policy；权限收紧后
  不再读取或续接。删除只按仍有效 Key 的精确所有权允许，不需要模型继续获权，以便清除自有数据。
- 已授权创建但尚未派发的后台任务在 claim 和最终 dispatch 都重查策略；收紧后以固定授权变化结果终结，
  零 attempt、零上游。已经派发的任务按冻结快照结算，不因收紧换号或重放。
- 取消自有任务只要求请求仍由同一有效 Key 认证且资源所有权匹配；协议/模型权限收紧不能阻止取消。
  Key 撤销后的管理性取消与统一撤销 worker 不在本批新增，不能借策略放宽恢复撤销 Key。

## 3. PROTO-07：Chat↔Responses 跨协议 SSE

### 3.1 开放范围

只开放：

| ClientProtocol → upstream wire | 请求/响应范围 |
| --- | --- |
| `openai-chat` → `openai-responses` | 文本、developer/system/user/assistant、function definitions/calls/results、既有支持参数与 usage |
| `openai-responses` → `openai-chat` | 无状态文本/instructions、function definitions/calls/results、既有支持参数与 usage |

必须是显式 `wire_protocol`。Messages/Gemini 跨协议流、`store/background/previous_response_id/conversation`、
媒体、reasoning、structured output、hosted/MCP/computer/shell 等仍在派发前返回 `unsupported_feature`。
native SSE 和六方向非流式行为不得改变。

### 3.2 纯转换接口

D 在 `internal/protocolconv` 提供以下窄接口；名称可按 Go 可见性微调，但语义和责任不得变化：

```go
type SSELimits struct {
	MaxLineBytes   int
	MaxEventBytes  int
	MaxStreamBytes int64
}

type SSEFrame struct {
	Event string
	Data  []byte
}

type SSEEvent struct {
	Name     string
	Data     json.RawMessage
	Semantic bool
	Terminal bool
}

type StreamConverter interface {
	FeedFrame(SSEFrame) ([]SSEEvent, error)
	EOF() error
}

func NewSSEDecoder(io.Reader, SSELimits) *SSEDecoder
func (d *SSEDecoder) Next() (SSEFrame, error)
func NewStreamConverter(PreparedRequest) (StreamConverter, error)
func EncodeSSE(SSEEvent) ([]byte, error)
```

解码器必须在任意字节拆分下处理 LF/CRLF/CR、注释与多行 `data`，并同时限制单行、单事件和全流字节。
不接受 `id`/`retry` 等本批不需要的语义字段。空注释可忽略；空数据事件、未知字段/事件、事件名与
Responses JSON `type` 不一致、重复终态或终态后数据均失败关闭。`Data` 不含 SSE framing。

唯一的未知 JSON 字段例外是 OpenAI 官方流式规格中的顶层 `obfuscation`：只在已支持的 Chat chunk 或
Responses delta 事件上接受字符串值，验证类型后作为无语义元数据丢弃；该字段本身绝不令
`Semantic=true`，也不放宽其他未知字段、其他事件位置、嵌套值或非字符串值。D 在测试/实现说明中记录
核对的官方来源和精确事件层级。

`Semantic=true` 只用于已经验证的文本或 function 身份/参数/结果输出；纯 envelope 元数据如
`response.created` 不算语义输出。无论是否已有语义输出，只要 durable dispatch 已提交就禁止重放。
function 参数终态必须是完整 JSON object；不能把截断或非 object 参数以成功终态交给客户端。

### 3.3 服务 bridge 接口

D 只新增 `internal/service/protocol_runtime_stream.go` 与
`internal/service/protocol_runtime_stream_test.go`，并提供：

```go
type protocolStreamLimits struct {
	MaxLineBytes   int
	MaxEventBytes  int
	MaxStreamBytes int64
	TerminalDrainTimeout time.Duration
}

type protocolStreamWriteResult struct {
	DownstreamCommitted bool
	SemanticCommitted   bool
}

type protocolStreamResult struct {
	StatusCode           int
	Header               http.Header
	DownstreamCommitted  bool
	SemanticCommitted    bool
	Completed            bool
}

func (p *protocolRuntime) executeStream(
	ctx context.Context,
	client upstreamHTTPDoer,
	request *http.Request,
	limits protocolStreamLimits,
	observeRaw func(protocolconv.Protocol, []byte) error,
	writeClient func(protocolconv.SSEEvent) (protocolStreamWriteResult, error),
) (protocolStreamResult, error)
```

- bridge 只在集成 handler 已完成 `dispatchModelRoute` durable barrier 后调用；它恰好执行一次
  `client.Do`，不选择账号、不创建 attempt、不结算账本、不重试。
- 先验证 2xx 与 `text/event-stream`。非 2xx 返回现有 `protocolUpstreamStatusError` 并保留有界安全 header
  （尤其 `Retry-After`）；在验证上游状态/content-type 与首个转换事件前不提交客户端 200/SSE header。
- 每个完整 JSON data frame 先以实际 `UpstreamProtocol` 调用 `observeRaw`，成功后才交转换器；`[DONE]`
  是 Chat wire 终止标记，不送 usage observer。observer 失败时不得输出未观察的转换事件。
- `writeClient` 同步调用并自然形成背压。bridge 无论 callback 是否返回错误，都先把返回的 commit 位 OR 入
  结果；callback 必须由真实 ResponseWriter 追踪 header、成功字节和 `n>0,err!=nil` 的部分写，并对任何
  可能已被客户端观察的语义字节保守返回 `SemanticCommitted=true`。一旦 callback 报错，bridge 立即取消
  请求并关闭 upstream body，handler 不得再补普通 JSON、第二组 header 或另一个 SSE error。
- converter 识别到客户端成功终态时，bridge 暂存所有 `Terminal=true` 事件而不写出，继续读取并验证尾部。
  只有在 `TerminalDrainTimeout` 内读到物理 EOF、`EOF()` 成功且没有重复终态/终态后 frame，才顺序写出
  暂存终态并令 `Completed=true`。终态后非法 frame 必须可观测地失败；不得看到终态就停止读取。终态后
  连接不结束则取消上游并按 interrupted 处理，不向客户端伪造 completed/[DONE]。
- `Completed=true` 只在严格状态机确认成功终态、尾部验证且终态实际写入成功后返回。提前 EOF 使用可 `errors.Is` 的 interrupted 错误；
  context/下游写失败区分 cancelled/downstream；畸形、未知、超限或 provider failure 保留 typed error。
  任何错误都不得由 bridge 生成成功终态。

### 3.4 集成与结算

集成 handler 复用现有 request/attempt、实际 wire usage、预算、代理、租约、取消和最终事务：

1. 请求完整验证和转换必须在 strict budget 证明及 durable dispatch 之前完成；无法证明上界仍为零派发。
2. `dispatchModelRoute` 成功后才能调用 bridge。发送首个已验证 client event 时设置 SSE header；之后的错误
   只能结束同一 attempt，可发送固定脱敏的协议失败 envelope，但不得发送 success terminal、换号或重放。
3. `Completed` 才结算 `succeeded`。客户端取消/写失败结算 `cancelled`；缺终态/EOF 结算
   `interrupted`；畸形、未知事件、上游显式 failure/incomplete 或非 2xx 结算 `failed`。原始 usage 仅按
   已观察证据累计，缺项保持未知。
4. 响应写入与读取都受 context 控制；慢客户端不得无界缓存。关闭或撤销沿现有取消路径传播到唯一上游
   请求并释放租约。

专项测试至少覆盖任意拆包、CR/LF/CRLF、多行 data、注释、行/事件/总量上限、文本、并行 function、
usage、严格终态、重复/未知事件、畸形参数、EOF、非 2xx/Retry-After、客户端首写部分成功后报错、终态
后非法 frame、重复终态、终态后连接不结束、客户端终态写失败、取消、背压和 observer-before-conversion。
组合测试覆盖两方向真实 HTTP、一次 dispatch/attempt、实际 wire usage、Key
策略、strict budget 零派发、native/非流式回归和员工 Key 不向上游泄露。

## 4. 文件与符号所有权

### D 任务独占

- `internal/protocolconv/**`：SSE decoder、stream converter、状态机及单元测试；
- 新文件 `internal/service/protocol_runtime_stream.go`、
  `internal/service/protocol_runtime_stream_test.go`：上述窄 bridge；
- 不修改 App、store、公开 handler、路由选择、accounting/governance、background 或网页。

### E 任务独占

- 新目录 `internal/keypolicy/**`：严格迁移、策略值对象、默认策略创建、全量替换、读取、事务内 Key 层
  `ClientProtocol + public model` 判定及专项测试；
- 新文件 `internal/service/key_policy_admin.go`、`internal/service/key_policy_admin_test.go`：
  `GET/PUT /admin/api/v1/keys/{id}/policy` 的窄管理 adapter；
- `web/src/pages/EmployeesPage.tsx`、`web/src/api.ts`、`web/src/styles.css`、新 Key 策略网页测试；为保持
  旧 fixture 编译可只做必要的 `web/src/test/App.test.tsx` / `GovernancePage.test.tsx` 类型兼容；
- 不修改 store/App 注册、现有 Key 创建/列表 handler、员工鉴权、模型目录、四协议 handler、账号池、
  governance/accounting、最终 dispatch、Responses 资源或 background worker。

`internal/keypolicy` 的服务边界固定为等价于：

```go
func Migrate(context.Context, *sql.DB) error
func CreateDefaultTx(context.Context, *sql.Tx, keyID string, at time.Time) error
func CreateTx(context.Context, *sql.Tx, keyID string, Replacement, time.Time) (Policy, error)
func LoadTx(context.Context, *sql.Tx, keyID string) (Policy, error)
func ReplaceTx(context.Context, *sql.Tx, keyID string, expected int64, Replacement, time.Time) (Policy, error)
func Replace(context.Context, *sql.DB, keyID string, expected int64, Replacement, time.Time) (Policy, error)
func Allows(Policy, ClientProtocol, publicModel string) bool
```

E 的 `Allows` 只判定 Key 层，不复制员工/路由可用性。服务 adapter 可调用现有只读 helper 形成
`effective_*`，但最终交集与派发屏障由集成任务拥有。非默认策略创建必须通过 `CreateTx` 与 Key INSERT
同事务；管理员 replacement 必须由 adapter/集成层在同一个 `*sql.Tx` 内验证员工当前授权及模型未归档，
再调用 `ReplaceTx` 做 revision CAS 和成员替换。禁止先查授权、再由 `Replace` 另开事务提交；`Replace` 只可
作为已经不需要外部授权交集验证的低层便利包装或专项测试入口。

### 集成任务独占

- `internal/service/app.go`、`store.go`、`modelapi.go`、`model_admission.go`、`model_preflight.go`、
  `outbound_proxy_dispatch.go`、`usage_hooks.go`、`responses.go`、`anthropic_messages.go`、
  `gemini_native.go`、`background_responses.go`、`response_resource_coordinator.go`、
  `route_wire_protocol.go`、`protocol_runtime.go`、`protocol_runtime_http.go`；
- 迁移/handler 注册、Key 创建与列表 DTO、四入口与目录过滤、policy snapshot/context、同事务最终重核、
  background/资源规则、SSE handler/error/ledger 接线及组合测试；
- `docs/**`、`scripts/**`、`.github/workflows/**` 和最终 PR/CI。

任务若发现必须跨越所有权，只报告最小签名/文件，不抢先修改。集成任务在 D/E 各自专项通过后合并组件，
集中完成组合回归和轻量 CI；不要求两个组件同时完成才可审阅。

## 5. 明确排除与矩阵校准

以下是用户明确的产品排除，不是“待本批实现”：`ID-04` 严格组织级多租户、`ID-05` 中员工 SSO/OIDC/
第三方登录、`DATA-02` 租户级数据域、管理员密码重置命令，以及提示词/响应正文审计。`ID-05` 的普通员工
自助门户、`ID-03` 注册/找回流程及其他单实例目标没有因此删除，仍按依赖队列规划。

截至本基线，PR #4 已交付有口令恢复材料、双版本轮换/回退，以及同一 Windows 用户下用不同 store/新目录
完成的可移植合成恢复；不等于跨 profile、真实第二台机器或 Linux/macOS 系统密钥 provider 验收。PR #5 已交付默认关闭的管理员商业管理网页；不等于员工
自助、自动续期或真实支付。PR #6 已交付六方向跨协议非流式执行；本批只增加 Chat↔Responses SSE。

本批不访问真实供应商/会员账号，不操作既有 8787 服务，不创建包、tag、部署或生产发布。合成上游和
真实 CLI 的本地结果必须与真实 provider 兼容声明分开。

## 6. 公开协议来源与依赖

- OpenAI [Responses streaming events](https://platform.openai.com/docs/api-reference/responses-streaming)；
- OpenAI 官方 Python SDK 的 [Responses create/stream options](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_create_params.py)，用于核对 `include_obfuscation` 与顶层 `obfuscation` 流式元数据语义。

本契约阶段不引入第三方运行时依赖；后续若新增依赖，必须同时记录许可证和用途。
