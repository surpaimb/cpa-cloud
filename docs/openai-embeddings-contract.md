# OpenAI Embeddings 文本子集契约

状态：2026-09-29 开发预览实施契约。本文只定义 `PROTO-08` 第一段：官方 OpenAI-compatible API Key 路径上的 `POST /v1/embeddings`、文本输入、非流式、float 向量。它不是完整 `PROTO-08`，不代表真实 OpenAI/provider/CLI/会员账号兼容，也不包含 token 数组、base64、可变 dimensions、`user`、多模态、流式、后台任务、跨协议转换或向量存储。

## 独立实现、来源与依赖

实现只依据 CPA Cloud 自有规格、独立测试和 OpenAI 官方 [Create embeddings](https://developers.openai.com/api/reference/resources/embeddings/methods/create) 文档（查阅日期 2026-09-29）。官方接口允许文本、文本数组、令牌数组或令牌数组列表输入；`encoding_format` 为 `float|base64`，`dimensions` 与 `user` 可选；响应为 `object=list`，每项带 `object=embedding`、`index` 和向量，并带 `model` 与 `usage.prompt_tokens/total_tokens`。本批只开放其中明确列出的更窄子集。

不得复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA、相邻参考工作区或其测试、迁移、资产和文档。本批不引入第三方依赖；现有依赖许可证记录继续适用。新源码头或包文档须记录独立来源边界。

## 公共 HTTP 子集

员工入口新增：

```text
POST /v1/embeddings
Authorization: Bearer cpac_...
Content-Type: application/json
```

只接受唯一 JSON object，且字段集合必须精确为：

```json
{
  "model": "public-embedding-model",
  "input": "text or non-empty array of text",
  "encoding_format": "float"
}
```

- `model` 必需，使用公开模型 ID；服务端只在最终 wire 中换成所选路由的 `upstream_model`。
- `input` 必需，只接受非空 UTF-8 string 或非空 string array；每个成员必须非空。string 与单成员 string array 保持各自 wire 形状，不重解释文本。
- `encoding_format` 可省略；省略按官方默认值规范化为 `float`。显式值只允许 `float`。
- token array、token-array batch、`base64`、`dimensions`、`user`、`stream` 和任何其他字段均返回固定脱敏的 `unsupported_feature` 或 `invalid_request_error`；重复 JSON key 一律非法。服务端不得静默删除或改写这些语义。
- 请求体最多 4 MiB；最多 2048 个文本项；另设单文本字节、JSON 深度和规范化输出上限。这些是本地资源防护，不是供应商 token 限额。服务端不内置或宣称 8192/300000 token 校验，因为本段没有受信 tokenizer；provider 的 token 拒绝只作为脱敏上游错误处理。
- 不接受 SSE、WebSocket、后台或 stored resource 语义。客户端断开会取消唯一上游请求，不产生重放。

成功响应使用官方形状：

```json
{
  "object": "list",
  "data": [
    {"object": "embedding", "embedding": [0.1, -0.2], "index": 0}
  ],
  "model": "public-embedding-model",
  "usage": {"prompt_tokens": 2, "total_tokens": 2}
}
```

上游响应先完整、有界读取和验证，再用公开模型 ID 生成客户端响应。验证要求：

- 顶层、data item 与 usage 都是唯一字段的严格 JSON object；不接受未知或重复字段；
- `object` 分别精确为 `list` 与 `embedding`；data 数量精确等于输入项数；
- index 必须唯一、覆盖 `0..N-1`，规范客户端输出按 index 升序；缺项、重复、越界均失败；
- 各向量非空、维数相同、每个数为有限 JSON number；维数、总标量和响应字节均有本地防御上限；
- 上游 `model` 必须等于最终选择的 `upstream_model`，防止价格和实际模型归因漂移；客户端响应改写为公开模型 ID；
- usage 为成功响应必需，两个计数均为非负整数且 `total_tokens >= prompt_tokens`。缺失或畸形 usage 使响应失败；账本不得把缺失事实补成 0。

非 2xx、非 JSON、压缩响应、超限、提前 EOF、取消、索引/维数/有限数/model/usage 失败都返回固定错误，不转发上游正文。日志、错误、审计、数据库和指标不得记录输入文本或向量。

## 协议、模型和 Key 授权

新增公共 `ClientProtocol`：

```text
openai-embeddings
```

新增实际 wire/usage protocol：

```text
openai-embeddings
```

两者同名但职责不同：ClientProtocol 只做员工 Key 权限；实际 wire 决定 URL、raw usage parser、价格、attempt 归因。不能用实际 wire 授权公共入口。

模型新增不可变 `model_kind=generation|embedding`。旧模型迁移为 `generation`；旧管理请求省略该字段仍创建 `generation`。只有管理员显式创建 `embedding` 模型，并在显式账号池 route 上选择 `wire_protocol=openai-embeddings`，才可进入 Embeddings 目录和执行。`embedding` 模型不得走 legacy direct route；generation 模型不得用于 embeddings，embedding 模型也不得进入四个旧生成入口或 Gemini 目录。

上游仅允许启用、未归档的 `openai-compatible` API Key 账号，route wire 必须精确为 `openai-embeddings`。不为 Codex membership、Anthropic、Gemini 或其他 provider 推断兼容；不做跨协议转换。目标 URL 由既有已验证 endpoint 规范追加 `/v1/embeddings`，继续禁止重定向、保留 TLS 验证并沿用 SSRF/DNS/代理保护。员工 Key、Authorization 和 CPA Cloud Key 不得向上游传递；只使用解密后的所选上游凭据。

## 旧 Key 不自动扩权

现有 `protocol_mode=all` 不能因新增 ClientProtocol 自动获得 Embeddings。启动迁移在单个事务中：

1. 把协议成员表 schema 扩展为五种协议；
2. 将迁移前每个 `all` 策略物化为 `selected` 加四种旧协议；已有 `selected` 原样保留；
3. 保持原 policy revision，因为迁移只固化已有授权集合且发生在服务接收请求前；
4. 写入独立、耐久、版本化迁移标记，并核验精确 schema、覆盖、约束、外键、成员和 `foreign_key_check`；
5. 部分 schema、标记损坏、孤儿/未知协议、覆盖不全或迁移失败均整笔回滚并拒绝启动。

迁移完成后，创建 Key 时省略整个 policy 仍使用显式的四种旧协议 `selected` 默认，不获得 Embeddings。管理员只有通过下列任一显式动作才授权：

- `selected` 列表加入 `openai-embeddings`；或
- 显式保存 `protocol_mode=all, protocols=[]`，此时含当前五种协议。

Key policy GET/PUT、创建幂等指纹、revision/CAS、审计和网页编辑沿用既有规则。网页必须把“全部协议（含 Embeddings）”说清楚；旧服务缺少 Embeddings 能力位时不显示、不发送也不假保存新枚举。任一 malformed 新字段失败关闭。

## 目录、候选和最终事务

`GET /v1/models` 返回当前 Key 可用的 generation 与 embedding 模型并集：generation 部分仍要求旧 OpenAI 客户端权限；embedding 部分要求 `openai-embeddings`。每个 embedding 模型必须同时满足员工模型策略、Key 模型/来源/账号组策略、`model_kind=embedding`、显式池 revision、至少一个当前合格 `openai-compatible + openai-embeddings` route、渠道/组映射、账号状态、恢复隔离与代理绑定。`GET /v1beta/models` 永不列 embedding 模型。

请求候选、排队、容量租约和最多一次的未派发预检换号沿用账号池协调器，但只遍历显式 embedding route；不存在 legacy fallback。失败、容量不足、冷却、代理不可用或组内无候选时不尝试其他协议/模型/组。

真正网络调用前，在现有 admission/lease/账号 mutation 锁顺序和同一个 `*sql.Tx` 中重核并提交：

- Key、员工、过期/撤销状态；最初冻结的 policy revision、五维权限与可信来源 revision；
- `model_kind=embedding`、模型 revision、员工与 Key 模型授权；
- pool revision、精确 route、`wire_protocol=openai-embeddings`、账号 revision、渠道/组关系、租约、容量、冷却、恢复隔离；
- 上游仍为同一启用的 `openai-compatible` API Key 账号，endpoint、凭据版本和代理 connection revision 未变；
- 实际账号、实际 upstream model、实际 `openai-embeddings` wire 的价格快照；
- 一条 parent request、一个 attempt、dispatch marker 与适用预算预留。

事务 commit 成功且结果确定后才允许恰好一次 `client.Do`。commit 错误或结果不确定、策略/路由/账号/代理/价格/预算 CAS 或 ABA、取消及任何存储失败都必须是 0 网络；在 durable dispatch 前拒绝必须是 0 attempt。已提交 dispatch 后不换号、不重放；网络错误、坏响应或取消按原 attempt 结算。

## 计量、价格和 strict 预算

parent 使用公共模型；attempt 固化实际账号、`ProviderOpenAICompatible`、实际 upstream model 与 `ProtocolOpenAIEmbeddings`。raw usage 在任何客户端改写前从已验证的实际 wire 响应读取：`prompt_tokens` 记为 input token，`total_tokens` 仅用于一致性；output/cache/reasoning 未提供时保持未知，不伪造 0。

价格在最终派发事务中按实际账号与 upstream model 读取不可变快照，Embeddings 只使用 input rate。output/cache/reasoning 没有上游事实时继续保存为 NULL，但不阻止已知 input 成本；例如 input=2、每百万 input token 价格为 3,000,000 micro 时原始成本为 6 micro。没有价格或 input 未知时成本保持未知；不能记为 0。价格变化、删除或币种变化按既有 snapshot/CAS 失败关闭或锁定原快照。四种生成协议仍按其各自规则要求完整 bucket，不能因 Embeddings 的 input-only 规则而放宽计算。

本段没有受信 tokenizer，也没有证明 input token 上界。任何命中 strict token 或 strict cost 预算的 Embeddings 请求必须在 durable dispatch 前返回 `budget_bound_unavailable`，并断言 0 attempt、0 网络；不能根据请求字节、字符数、官方营销 token 上限或上游事后 usage 预留。无 strict 命中时可执行，完成后用 raw usage/价格结算；shadow/未知事实保持未知。

## 管理 API、网页与能力位

`GET /system/status` 增加只读 `features.openai_embeddings=true`，只在路由、迁移、Key、模型 kind、账号池、最终事务、计量和响应验证全部接线后声明。管理端：

- 模型创建/读取展示 `model_kind`；旧省略输入默认为 generation，embedding 必须显式；kind 创建后不可变；
- 账号池 route 增加 `openai-embeddings`，只对 openai-compatible + embedding 模型开放；服务端仍独立校验，不能信任网页过滤；
- Key 协议编辑增加 Embeddings，并说明旧 Key 已锁定四协议、显式 all 才含新增协议；
- 定价仍按账号/upstream model 管理，不增加虚假默认价格；
- 桌面与 390px 视口均可完成 embedding 模型、route、Key 授权和价格配置。

功能位不存在时，新网页保持旧服务兼容，不发送 `model_kind` 或新协议/wire 值。新服务返回缺失、null、未知或互相矛盾的能力字段时网页失败关闭。

## D 纯 wire 包接口锁

D 独占新增目录 `internal/embeddingwire`，仅可修改：

```text
internal/embeddingwire/request.go
internal/embeddingwire/response.go
internal/embeddingwire/limits.go
internal/embeddingwire/*_test.go
```

包不得导入 `internal/service`、数据库、HTTP client、鉴权、路由、Key policy、accounting、price 或 budget。不得触碰任何现有文件。导出面锁定为：

```go
var ErrInvalidRequest, ErrUnsupportedFeature, ErrInvalidResponse, ErrLimitExceeded error

type EncodingFormat string
const EncodingFloat EncodingFormat = "float"

type Input struct { /* immutable-by-copy text/string-array value */ }
func (Input) Count() int

type Request struct {
    Model          string
    Input          Input
    EncodingFormat EncodingFormat
}
func DecodeRequest([]byte) (Request, error)
func ValidateRequest(Request) error
func MarshalRequest(Request, upstreamModel string) ([]byte, error)

type Embedding struct { Index int; Values []float64 }
type Usage struct { PromptTokens, TotalTokens int64 }
type Response struct {
    Model string
    Data  []Embedding
    Usage *Usage
}
func DecodeResponse([]byte) (Response, error)
func ValidateResponse(Response, expectedUpstreamModel string, expectedInputs int) (dimension int, err error)
func MarshalResponse(Response, publicModel string) ([]byte, error)
```

`Decode*` 拒绝重复/未知字段和错误类型；`Validate*` 执行本契约资源/结构规则；`Marshal*` 只生成规范官方子集并深拷贝调用方切片。省略 encoding 规范化为 float。`dimensions`、`user`、token arrays 与 base64 均拒绝。`Response.Usage=nil` 只表示未知事实，`ValidateResponse` 必须据此返回 `ErrInvalidResponse`，不能补零。`limits.go` 可导出供 HTTP 层复用的 `MaxRequestBytes` 与 `MaxResponseBytes`；其他限制保持包内或只读常量。任何接口变更先由集成任务更新本文并提交。

集成任务独占 `App/store/routes/keypolicy/accounting/price/budget/admin UI/flags/finalTx` 及所有现有文件；D 不挂 HTTP 路由、不改枚举/迁移/账本。

## 验收矩阵

自动化至少覆盖：

- 单 string、string batch、空值、空成员、过量项、UTF-8/字节上限、重复/未知字段、token arrays、base64、dimensions、user、stream；
- 响应 index 完整性、排序、同维、有限数、维数/总标量/字节上限、model mismatch、usage 缺失/负数/关系错误；
- 旧库 Key `all` 固化为四协议、selected 保留、默认创建不扩权、管理员显式授权、迁移中断/部分 schema/标记/覆盖/外键与重启；
- generation/embedding 模型和目录隔离，四个旧入口回归，旧 Key 对新模型零可见/零派发；
- Key 协议/模型/IP/可信代理/账号组收紧、revision/CAS/ABA，route/pool/account/proxy/price/budget ABA；所有 durable 前拒绝断言 0 attempt、0 上游；
- 多账号优先级/权重/容量、排队取消、预检换号仅限未派发、网络后绝不重放；
- raw usage、实际 wire/account/model 价格、input-only 已知成本且未报告 bucket 仍为 NULL、未知价格、strict budget 无上界拒绝、并发结算、重启恢复；
- 上游 4xx/5xx、非 JSON、压缩、超限、取消、TLS/SSRF/DNS/redirect 和日志/DB/WAL 脱敏扫描；
- 真实临时进程仅使用合成上游和动态非 8787 回环端口；浏览器覆盖桌面与 390px；不访问真实 provider；
- 最终精确 HEAD 的普通 Go、service 与有状态包 race、vet、两个 Go CLI build、Web typecheck/test/build、既有七项 smoke 与独立固定二进制 runner。

README、产品计划、开发计划、feature parity 和 integration status 必须只把本批称为“PROTO-08 第一段 OpenAI-compatible text/float API Key 子集”，保留 token arrays/base64/dimensions/user、真实 provider、会员、完整预算上界和其他 provider 的待办。不得为本批创建 tag、安装包、部署或 release；最终 PR 与 guarded merge 由协调流程按精确 HEAD 执行。
