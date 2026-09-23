# 固定模型 hard budget bound profile 可行性

状态：**研究结论，功能待实现且默认关闭**，2026-09-23。本文只评估一个极窄的 OpenAI API Key
文本生成子集，不表示当前服务、网页或发布包已经提供 hard TPM、hard cost、余额或收费。研究没有调用真实账号，
没有读取真实凭据，也不覆盖 Codex membership、Claude、Gemini 或任意第三方 `openai-compatible` 服务。

## 结论

可以为一个严格固定的 profile 提供不依赖预估 tokenizer 的安全上界：

| 维度 | 唯一候选值 |
| --- | --- |
| 上游 | OpenAI 官方 API Key；最终 HTTPS 目标固定为 `api.openai.com:443` |
| 路径 | `POST /v1/chat/completions`；禁止重定向 |
| 协议 | `accounting.ProtocolOpenAIChatCompletions` |
| 实际模型 | 只能是 `gpt-4.1-2025-04-14`，不接受 `gpt-4.1` alias |
| 输入/输出 | 纯文本、单 choice、无工具、无图片/音频/文件、无 predicted/structured output |
| 状态 | 显式 `store:false`，无隐式会话、后台任务或服务端上下文引用 |
| 输出上限 | 显式 `max_completion_tokens`，范围 `1..32768` |

OpenAI 的固定 GPT-4.1 页面列出 `1,047,576` context window、`32,768` max output tokens、文本输入/输出、
Chat Completions 支持和固定 snapshot `gpt-4.1-2025-04-14`；页面同时把 GPT-4.1 描述为没有 reasoning step 的
non-reasoning model。[GPT-4.1 model page](https://developers.openai.com/api/docs/models/gpt-4.1)

官方上下文说明把 context window 定义为单次请求可使用的最大 Token 数，并明确它包含 input、output，以及适用模型的
reasoning Token；同一页面也提示超出 context window 的生成内容可能在响应中被截断。
[Conversation state: managing the context window](https://developers.openai.com/api/docs/guides/conversation-state#managing-the-context-window)
这些文字支持把 `C` 当成输入容量的保守工程上界，但不是供应商对每种错误、截断、计费字段和未来后端行为作出的形式化
不变量。首版不依赖 `prompt_tokens + completion_tokens <= C` 这一更紧的共同约束，而独立预留：

```text
C = 1,047,576
InputMax = C
1 <= OutputMax = M <= 32,768
TPMUpper = checked_add(InputMax, OutputMax) = C + M
```

这仍不需要估算员工正文会被哪个 tokenizer 分成多少 Token。代价是极其保守：即使请求很短，也会预留
`1,080,344` Token。它只能支持阈值至少能容纳这项独立上界的策略；较小 hard TPM 仍必须等待经过验证的精确
bounder 或 tokenizer，不能因“通常提示很短”降低预留。该 profile 的成立还依赖真实响应 usage 与上述官方容量和
输出上限语义一致；这项兼容性尚未用真实 provider 验证。

这项结论是**有条件可上线的工程子集**，不是当前代码已经可上线。上线前仍须实现预算总开关、reservation/settlement、
本文的 strict profile parser、组合上界成本算法，以及下文所述 usage parser 修正和专项故障测试。

## 输出上界覆盖什么

Chat Completions 参考把 `max_completion_tokens` 定义为模型可生成 Token 的上界，包含可见输出和 reasoning Token。
[Create chat completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
官方 Token 计数指南进一步说明，reported completion/output 包含所有模型生成的 Token，包括不可见格式、通道分隔、
tool-call 等消息结构；`max_completion_tokens` 限制这些全部生成 Token，而不只限制可见文本。
[Counting tokens: output token counts](https://developers.openai.com/api/docs/guides/token-counting#understand-output-token-counts)

所以，profile 的 `M` 可以覆盖普通文本、不可见格式 Token，以及模型实现以后出现但仍计入 `completion_tokens` 的
非可见 Token。本文仍禁用工具，原因是 hard cost 不只涉及模型输出 Token：托管工具可能另有每次调用费用或额外模型
循环，当前内部价格快照只覆盖四个 Token 桶，不能证明这些非 Token 费用和循环次数。

`n` 必须显式为 `1`。Chat 参考允许最多 128 个 choices，并说明所有 choices 的生成 Token 都会收费；文档没有给出
可以安全依赖的“一个 `max_completion_tokens` 是跨所有 choices 的共享总额”保证。
[Chat `n` parameter](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
因此缺少 `n`、`n != 1` 或字段类型错误都不进入该 profile，不能依赖服务端默认值。

GPT-4.1 当前被列为 non-reasoning model，但安全性不依赖 reasoning 一定为零：即使将来响应中出现官方计入
`completion_tokens` 的不可见 Token，显式 `M` 仍覆盖它们。`prediction` 被禁止，因为 rejected prediction Token 也按
completion Token 计费，虽然原则上 `M` 可能覆盖，首版没有必要扩大允许字段。
[Predicted Outputs usage](https://developers.openai.com/api/docs/guides/predicted-outputs)

## 输入、缓存桶和成本上界

Chat usage 返回 `prompt_tokens`、`completion_tokens` 和二者之和；细分字段可包含 `cached_tokens` 与
`cache_write_tokens`。[Chat usage schema](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
官方 Prompt Caching 指南给出的成本关系为：

```text
ordinary_input = input_tokens - cached_tokens - cache_write_tokens
```

并说明一个 input Token 使用 ordinary、cache-read 或 cache-write 费率之一，cache-write 不是叠加费用。
[Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
在这个固定 profile 中，只有经过专项测试确认 usage 遵守该公式后，才能把三个输入类别作为 `prompt_tokens` 的互斥
分区使用。缓存会复用完整 rendered context 的前缀，包括 OpenAI 提供的隐藏指令、developer messages、工具定义和
对话历史；缓存不是额外追加的会话输入。隐藏内容不适合由本地 tokenizer 估算，所以首版只把官方 context 容量作为
`InputMax=C` 的保守工程推断，并在运行时核对真实 usage。

当前 [budget admission proposal](../budget-admission-proposal.md) 建议在 proof 中分别保存 ordinary input、cache read、
cache write、output 四个独立上界。这个通用 helper 必须保留，未知 profile 不能擅自假设输入桶互斥。固定 profile
通过公式与专项验收后，可以额外使用不可混淆的输入 group bound，例如：

```go
type DispatchBoundProof struct {
    // 既有 route/profile/revision 字段省略。
    InputTokensMax  int64 // C；三个输入收费桶的组合上界
    OutputTokensMax int64 // M；独立输出上界
    InputBucketMode string // 只能由固定 profile 设置的版本化 exclusive-group 证明
}
```

hard TPM 的 reservation 使用 checked `C+M`。成本上界不能先猜命中哪个缓存桶，而应从本次冻结的管理员价格快照取：

```text
Rin = max(input_rate, cache_read_rate, cache_write_rate)
cost_upper_micro = ceil((C * Rin + M * output_rate) / 1,000,000)
```

乘法、加法、比较和向上取整必须复用 accounting 的宽整数 checked helper。公式把 input 与 output 当成两个独立最大值，
不依赖共同 context 的更紧约束；三个输入桶只有在该 profile 的 `exclusive-group` 证据成立时才共用 `C`。否则回退到
proposal 的通用四桶独立上界 helper，不能为了减少预留而启用 group bound。管理员配置的 price snapshot 仍是唯一
内部成本来源，不能把网页上的 OpenAI 当前标价硬编码进账本。

OpenAI 当前说明 GPT-4.1 所属的 earlier models 没有额外 cache-write charge；GPT-4.1 model page也只列 ordinary input、
cached input 和 output 三类价格。这个事实可用于管理员价格模板，但不能改变上述取最高费率的安全公式，也不能当作
未来所有模型的规则。[Prompt caching model differences](https://developers.openai.com/api/docs/guides/prompt-caching#summary-of-model-differences)

## 冻结请求 allowlist

bound 必须针对将要送入 transport 的最终 JSON，而不是员工原始正文或随后仍会修改的中间对象。解析时拒绝重复键、
trailing JSON、未知字段、错误类型和超出已有正文大小限制的请求。允许集合固定为：

- `model` 必须精确等于 `gpt-4.1-2025-04-14`；public model 映射后的 actual model 再校验一次；
- `messages` 必须是非空数组；只允许 `developer|system|user|assistant`，每项只含 `role` 与非空或空的 JSON string
  `content`；禁止 content-part 数组、`name`、tool/function call、tool result、refusal、audio 等其他字段；
- `max_completion_tokens` 必须显式为安全整数 `1..32768`；拒绝 deprecated `max_tokens`；
- `n` 必须显式为整数 `1`；
- `modalities` 必须显式为唯一值 `['text']`；
- `store` 必须显式为 `false`；
- `stream` 必须显式为 `false`。SSE 与 JSON 理论上共享相同上界，但首个 profile 不需要把丢失最终 usage chunk的额外
  状态带入验收。官方参考明确指出 stream 中断时可能收不到最终 usage chunk。

顶层除此之外的字段全部拒绝，包括 `tools`、`tool_choice`、legacy `functions/function_call`、`parallel_tool_calls`、
`web_search_options`、`prediction`、`response_format`、`audio`、`metadata`、`prompt_cache_key`、
`prompt_cache_retention`、`service_tier`、`user`、`seed`、logprobs 和采样参数。部分字段本身不一定扩大 Token 上界；首版
拒绝是为了让 `profile_id + transform_revision` 真正描述唯一且可专项验证的发送形状，而不是暗示它们危险或永久不可支持。

HTTP 目标也属于 proof：scheme 只能是 `https`，ASCII host 必须精确为 `api.openai.com`，effective port 为 `443`，
path 精确为 `/v1/chat/completions`，没有 userinfo、fragment 或 query。代理只可传输到这个已验证目标，不能改变 Host/SNI；
任何 redirect 固定拒绝。该 profile 不适用于 Azure、自建域名、区域别名或一般 `openai-compatible` 账号。

## 隐式上下文和工具的拒绝边界

OpenAI 的迁移指南说明 Chat Completions 的会话状态由调用方手动管理；Responses 才提供 conversation 或
`previous_response_id` 形式的持久上下文。指南也指出两个 API 的存储默认值可能取决于账号，并建议用 `store:false`
显式禁用。[Migrate to Responses: statefulness](https://developers.openai.com/api/docs/guides/migrate-to-responses#decide-when-to-use-statefulness)

因此首个 profile：

- 不接受 `previous_response_id`、`conversation`、`prompt`/prompt template、`input` item 引用、
  `context_management`/compaction 或 `truncation`；这些都是 Responses 形状或服务端状态入口；
- 不接受 `background`、deferred loading 或异步 retrieve；一次员工 HTTP 请求只对应一次同步上游 attempt；
- 要求 `store:false`，不能依赖账号默认值；
- 不接受 file ID、URL、图片、音频或服务端 prompt；所有模型输入都必须作为当前冻结 `messages[].content` 文本显式存在；
- 不接受 built-in tools、function tools 或 legacy function 字段。Responses 被官方描述为可在一次请求内运行多个工具的
  agentic loop；本文特意选择 Chat 严格子集，不把这种循环的工具次数或额外费用塞进 Token 预算。

Prompt caching 仍可能由 OpenAI 对 eligible prefix 自动执行。它不是隐式对话追加：官方说明它复用本次 rendered
context 的匹配前缀，并通过 usage 的输入细分报告实际复用。在 profile 互斥证据成立的前提下，成本上界对三个输入
费率取最大值，因此 cache hit、miss 或 write 的收费分类不会突破 reservation；证据不成立时不得使用该结论。

## 当前 usage parser 的上线阻塞

本仓 `internal/accounting/usage_parser.go` 已把 Chat 的 `completion_tokens` 整体记为 output，因此不会重复加
`reasoning_tokens`；这与官方“reported completion 包含所有生成 Token”的口径一致。它还验证
`prompt_tokens = ordinary + cached + cache_write` 后才给 ordinary input。

但当前实现只有在 `cached_tokens` 和 `cache_write_tokens` **同时存在**时才给出 ordinary input 和完整四桶。官方 Chat
默认示例只展示 `prompt_tokens_details.cached_tokens`，并不承诺 GPT-4.1 每次都返回 `cache_write_tokens`。因此仅实现
bounder 仍不足以上线：常见成功响应可能被结算为 unknown，导致完整 `C` reservation 一直保守占用。

上线前必须增加只受该固定 profile 调用的结算规则，并用保存的 actual model/profile 验证来源：

1. `prompt_tokens`、`completion_tokens` 必须存在、非负，且 `total_tokens`（若存在）与二者一致；
2. `cached_tokens` 缺失时保持 unknown，不能猜零；
3. 对 `gpt-4.1-2025-04-14`，若 `cache_write_tokens` 缺失，可以提出版本化的 effective billing 归一化：把
   `prompt_tokens - cached_tokens` 全部归入 ordinary 收费桶。这里的 effective `cache_write=0` 只表示内部收费分类，
   **不表示物理上没有写入缓存，也不声称知道真实写入 Token 数**；该规则尚未实现，不得改变通用 OpenAI/GPT-5.6
   parser；
4. 若响应显式给出 `cache_write_tokens`，继续按三桶互斥关系验证并保存真实值；
5. 任一负数、溢出、子桶大于 prompt、协议错误、半响应或持久化失败仍 settlement unknown，并保留上界。

第 3 条依赖官方对 earlier models “no additional cache-write charge”的当前说明，并且必须由 profile revision 与专项
provider 兼容测试绑定。真实 provider 兼容目前未测。若工程审阅或测试认为该说明不足以把剩余部分归入 effective
ordinary，则 profile 仍可保守运行，但只能按 bound 做 unknown settlement；在这种模式通过 24 小时成本窗口前，它不
具备实用性，不能称为首个可上线 profile。

## 必须实现和证明的最小批次

1. 新 profile registry 只注册上述 exact host/path/model/protocol/transform revision；unknown profile fail closed。
2. strict JSON parser 和冻结 payload；proof 后任何字段修改、route/account/model revision 改变都取消派发。
3. 预算总开关仍默认 `false`；只有 proposal 规定的 budget 开关与 `deny_unknown` 双门控才执行 hard 拒绝。
4. reservation schema 保存独立 `InputMax=C`、`OutputMax=M`、输入 group-bound 版本、profile/bounder revision、实际 route
   和不可变 price version；不保存正文、正文 hash、Authorization、响应或原始错误。
5. hard TPM 使用 checked `C+M`；hard cost 使用 `C*max(三个输入费率)+M*output_rate` 的 checked 计算。价格缺失、
   币种不符、溢出、profile 不匹配或 DB 失败时零网络调用。
6. `may_have_sent`、取消、timeout、未知终态、重启、续租和结算沿用 proposal 的保守 reservation 状态机；不能因上游返回
   4xx/5xx 或员工断开就提前释放可能已消耗的上界。
7. profile-aware GPT-4.1 usage 归一化完成后，实际四桶替换上界；解析无法证明时保留上界，不把 NULL 当零。
8. 任何真实 usage 超过已记录的 `InputMax`、`OutputMax` 或其 checked 总上界，都必须如实保存 overage 事实、保守结算并
   自动停用该 profile；不能截断、饱和或改写 usage 来维持 proof 成立。停用后的新请求固定 fail closed，直到管理员升级
   profile revision 并通过重新验收。

专项测试至少覆盖 exact snapshot/alias、host 大小写规范化与伪后缀、端口、redirect、所有拒绝字段、重复 JSON key、
`M=1/32768/越界`、`n=1/2/缺失`、多模态、工具、store/background/state 引用、缓存三桶每种分配、缺失与显式
cache-write、最高费率分别落在四个桶、宽整数溢出、stream 拒绝，以及 proof 后 payload/route 变化。测试使用合成
transport，不调用 OpenAI。上线验收还需用无真实员工数据的受控官方账号验证 usage 字段兼容；本文没有执行该步骤。

## 不在结论中的保证

- 固定 snapshot 页面说明它锁定模型版本的行为与性能，但不保证任意账号已有访问权限、固定价格、永久可用或当前 rate
  limit；这些失败由正常上游错误与保守 settlement 处理。
- 把 `C` 用作 `InputMax` 是依赖官方容量语义的工程推断，不是无条件供应商计费保证，也不证明请求一定会被上游接受；
  过长输入可在上游拒绝或截断。独立 `C+M` 预留与 overage 停用是对这种不确定性的保守处理。
- 本结论不扩展到 GPT-4.1 alias、其他 snapshot、Responses、Azure、第三方兼容服务、工具、图片、文件、音频、多个
  choices、streaming 或后台执行。每次扩展都必须新增 profile revision 和独立官方证据。
- 本结论只证明内部 Token/价格快照的最坏情况 reservation，不等于供应商账单、余额、收费或汇率功能。

综上，官方固定 context window 加显式输出上限支持构造一个有条件、极保守的 hard budget profile 工程推断。首版必须
独立预留 `InputMax=C` 与 `OutputMax=M`，只在固定 profile 证据成立时把三个输入桶作为 group bound；它不能改变通用
四桶 helper，也不能绕过 GPT-4.1 cache-write 缺失字段的结算问题。完成 strict allowlist、profile-aware usage 归一化、
真实 provider 兼容验收和 overage 自动停用前，应继续保持计划状态与默认关闭。
