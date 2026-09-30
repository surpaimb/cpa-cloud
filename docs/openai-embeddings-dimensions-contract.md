# OpenAI Embeddings 显式 dimensions 增量契约

状态：2026-09-30 开发预览实施契约，`PROTO-08` 第三段。本文仅扩展已实现的 [文本/float 子集](openai-embeddings-contract.md)和 [token-array 输入增量](openai-embeddings-token-input-contract.md)中的 `POST /v1/embeddings` 请求；此前的员工 Key 授权、显式 embedding 账号池、非流式 float 响应、用量、strict 预算、取消和一次派发规则均保留。与前两份契约冲突时，仅显式 `dimensions` 的接收、转发和响应维数以本文为准。省略 `dimensions` 的请求与既有 wire 和结果语义完全相同。

## 独立来源和范围

依据 OpenAI 官方 [Create embeddings API reference](https://developers.openai.com/api/reference/resources/embeddings/methods/create)与 [Embeddings guide](https://developers.openai.com/api/docs/guides/embeddings)，查阅日期 2026-09-30。官方说明 `dimensions` 指定输出维数，只适用于 `text-embedding-3` 及后续模型；指南列出 `text-embedding-3-small` 默认 1536 维、`text-embedding-3-large` 默认 3072 维，并说明可通过该参数缩短输出。本地白名单及上下界是 CPA Cloud 对当前可核验模型的有意收窄，不表示兼容所有“及后续”模型，也不代表已验证真实 provider。

实现仅依据上述公开协议、自有契约和独立编写的测试；不得复制、翻译或移植参考项目或归档实现。本增量不增加第三方依赖；既有依赖许可证记录不变。新增源码注释须记录独立来源。不引入 base64、`user`、流式、tokenizer、通用预算上界、DDL、管理配置、会员账号或真实供应商调用。

## 请求和选路

原有四种 `input` 形状（单文本、文本批次、单 token 序列、token 序列批次）均可搭配显式 `dimensions`。字段必须是 JSON 十进制正整数字面量：`1`、`1536` 等合法；`0`、`-0`、负数、小数、指数、溢出、布尔、字符串、null 与重复 key 均非法。词法校验不得先转成浮点数。非法数值返回固定脱敏 `invalid_request_error`，重复 key 按原规则失败；请求体和规范化输出仍受 4 MiB 上限约束。

显式维数的资格必须依据**最终实际 `upstream_model`**，不得依据公共模型别名、请求的 `model` 或账号池中其他候选：

| 实际 `upstream_model` | 显式 `dimensions` 合法范围 |
| --- | ---: |
| `text-embedding-3-small` | `1..1536` |
| `text-embedding-3-large` | `1..3072` |

其他实际模型（包括名称相似或有前后缀的模型）一律不得接收显式维数；越界或不合格路由在 durable dispatch 前返回固定脱敏 `unsupported_feature`，断言 0 attempt、0 上游网络。未带字段的请求不受这项模型白名单限制。预检换号只可选择同样合格的候选，不得向不合格候选退化。最终 durable dispatch 的同一数据库事务必须在既有 Key、模型、池 revision、精确 route、账号和 egress 重核后再次确认该资格；配置变更、ABA、撤销或失败仍遵循 0 网络/0 attempt 规则。

仅当客户端显式提供 `dimensions` 时，上游 JSON 必须带原整数值；省略时上游 JSON 不能出现该字段，原有 `model` 替换、四种 `input` 类型/顺序和值、规范化 `encoding_format: "float"` 不变。不能在本地截断、补齐或归一化向量，也不能静默忽略请求参数。

## 响应、故障和验收

上游成功响应仍须通过旧的严格 object、索引、数量、有限 float、实际模型及 usage 校验。对显式维数请求，每一条向量的长度还必须精确等于请求值；维数不符按既有固定脱敏 `upstream_protocol_error` 和失败 attempt/账本处理，不能把错误向量返回客户端或算作成功。省略字段时维数只遵循旧的非空/一致/资源上限规则。上游 4xx/5xx、取消、存储故障和未知提交语义不变；不记录输入、向量、Key 或上游凭据。

独立测试至少覆盖：四种输入形状与省略字段的 wire 保真；两个模型的最小值和最大值；公共别名与实际模型分离；所有非法词法/类型、重复 key、上下界、相似模型；账号池预检换号和最终路由 mutation/ABA；每个向量的精确维数、坏响应账本；旧 Key 权限、strict 预算、取消、存储失败、重启及一次派发回归。验证分别报告本地 Go/vet/双 CLI/Web、GitHub Linux CGO race 和固定二进制合成流量；不得把计划中的 provider、会员或部署行为写成已测。
