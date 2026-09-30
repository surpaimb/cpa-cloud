# OpenAI Embeddings token-array 输入增量契约

状态：2026-09-30 开发预览实施契约，`PROTO-08` 第二段。本文只扩展已实现的 [OpenAI Embeddings 文本子集](openai-embeddings-contract.md) 的 `input`；原契约的鉴权、模型和账号池路由、非流式 float 响应、用量、价格、strict 预算、取消及失败语义继续适用。本文与原契约冲突时，仅 token-array 输入部分以本文为准。不是完整 Embeddings，也不是对真实 OpenAI、其他 provider、会员或客户端的验证。

## 来源、独立性和范围

独立协议依据为 OpenAI 官方 [Create embeddings API reference](https://developers.openai.com/api/reference/resources/embeddings/methods/create)，查阅日期 2026-09-30。官方将 `input` 列为 string、string array、integer token array 或 integer token-array batch。本地 ID 和数量上限是 CPA Cloud 自行定义的资源防护，不宣称是 tokenizer 有效性、供应商模型限额或配额证明。实现仅依据本文、既有 CPA Cloud 自有契约和独立编写的测试；不得复制、翻译或移植参考项目或归档实现。本增量不增加第三方依赖，既有第三方许可证记录不变。新增源码或修改后的包注释须标注独立协议来源。

只覆盖 `openai-compatible` API Key、显式 `embedding` 模型与 `openai-embeddings` wire 的 `POST /v1/embeddings`。不加 base64、`dimensions`、`user`、流式、生成协议转换、管理配置、数据库迁移或真实供应商调用。不改变员工 Key 协议集合：旧 Key 和省略 policy 新建的 Key 仍只获旧四协议，管理员必须显式授权 `openai-embeddings`。

## 输入形状与保真

`input` 精确接受以下四种互斥形状，顶层数组按第一项的 JSON 类型判别，之后每一项必须同类：

| JSON 形状 | 意义 | 输入数 `Count()` | 上游 `input` |
| --- | --- | ---: | --- |
| `"text"` | 单文本 | 1 | string |
| `["a","b"]` | 文本批次 | 2 | string array |
| `[12,34]` | 单个 token 序列 | 1 | number array |
| `[[12,34],[56]]` | token 序列批次 | 2 | array of number arrays |

文本规则不变。空顶层数组、空文本、空 token 序列、空批次成员均非法；不得把 `[]` 推断成任何一种形状。字符串和数字、数字和子数组、字符串和子数组，以及布尔、null、object 或更深嵌套的混合输入均失败关闭。单 token 数组永远是**一个**输入，不按 token 数量生成多个向量；token-array 批次的输入数为外层长度。上游 JSON 仅用选中路由的 `upstream_model` 替换 `model`、将省略的 `encoding_format` 规范化为 `float`，必须保留四种 `input` 的 JSON 类型、层级、顺序和值，不把 token ID 拼成字符串、转成文本或拆成多请求。

每个 token ID 必须是十进制 JSON 整数词法，值在 `0..2147483647`（含端点）。`-0` 和所有负数、`1.0` 等小数、`1e0` 等指数、超范围值、布尔值及字符串形式的数字均拒绝；不经过 float64 转换以免舍入或溢出。整数零合法，但本地结构校验不代表该 ID 被所选模型的 tokenizer 接受。任一错误均在网络派发前拒绝，且不得在响应、日志或数据库中回显 token ID。

既有请求体上限 4 MiB、JSON 深度上限 16、最多 2048 个输入项和规范化上游请求体上限 4 MiB 继续适用。另为 token 形状设每个序列最多 2048 个 ID、整批最多 65536 个 ID；这些均为本地防护，不是官方 token 上下文或批次限额。超限返回固定脱敏 `request_too_large`；畸形形状、重复 JSON key、缺字段、非法数值及不允许的类型返回固定脱敏 `invalid_request_error`。未知字段、base64、`dimensions`、`user`、`stream` 继续按旧契约返回固定 `unsupported_feature`；不因新输入形状默默接受它们。任何拒绝都不产生上游 HTTP 请求或 durable attempt。

## 执行、响应与安全边界

Key/模型/来源/账号组授权、候选与最终派发事务、一次请求一次上游调用、不重放、取消和撤销的当前规则完全不变。仅输入项数进入响应校验：上游 `data` 的 index 必须精确覆盖 `0..Count()-1`，可乱序接收但按 index 输出；长度不符、重复或缺失均失败。float 向量、`model` 和 `usage.prompt_tokens/total_tokens` 必需与旧契约相同。上游 raw usage 仍在客户端模型改写前计量，未知 bucket 不伪造为零。请求中的 ID 数量不充当 usage，也不决定价格或预算预留。

由于没有受信 tokenizer 和可证明的 provider 输入 token 上界，命中 strict token/cost 预算的 token-array 请求仍返回 `budget_bound_unavailable`，断言 0 durable attempt、0 上游网络。非 strict 请求可执行并在成功时据经过验证的上游 usage 结算。正文、token ID、员工 Key、认证头、上游 token 和向量不得进入日志、错误、审计、数据库或指标。持久化失败、未知提交、重启恢复、断连和取消沿用旧契约，不因 token 形状改变派发时机。

## 验收与回滚

独立测试至少覆盖四种输入的解码/编码形状和顺序、单 token 数组计数、batch N 个 index/usage、合法边界值、空/混合/负数/小数/指数/溢出/重复 key/层级和大小限制、旧文本回归；HTTP 合成上游验证授权、旧 Key 零派发、strict 预算零派发、取消、撤销、持久化失败、重启、响应 index/usage 和秘密不外泄。运行非缓存 Go 测试、race、vet、双 CLI、Web 与进程回归，并让精确提交进入 GitHub CI 编译。仅合成凭据与动态非 8787 回环端口，不访问真实 provider。

无新持久化结构或配置；回滚到本增量之前的服务会重新拒绝 token-array 输入，既有文本、Key 和账本数据不需转换。回滚前仍须停服并保留完整数据目录与对应旧程序；不得让新旧进程同时写同一库。本批不发包、不部署、不创建 tag。
