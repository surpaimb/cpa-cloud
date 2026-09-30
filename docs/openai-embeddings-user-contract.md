# OpenAI Embeddings 显式 user 提示增量契约

状态：2026-09-30 开发预览实施契约，`PROTO-08` 第四段。本文仅扩展现有 [文本/float 子集](openai-embeddings-contract.md)、[token-array 输入增量](openai-embeddings-token-input-contract.md)和[显式 dimensions 增量](openai-embeddings-dimensions-contract.md)中的 `POST /v1/embeddings` 请求字段。此前的 Key/模型/来源/账号组授权、四种输入形状、可选 dimensions、非流式 float 响应、strict 预算、用量、取消和一次派发规则均保留。与前三份契约冲突时，仅 `user` 的拒绝规则改为本文列出的窄接受规则；base64 仍明确拒绝。

## 独立来源与范围

公开协议依据为 OpenAI 官方 [Create embeddings API reference](https://developers.openai.com/api/reference/resources/embeddings/methods/create)，查阅日期 2026-09-30。其 `user` 是可选 string，描述为代表终端用户的唯一标识，可协助上游监测滥用。**这不是 CPA Cloud 对该字符串真实性、唯一性或安全性的证明。** 以下字符和长度限制是本项目的本地隐私与资源收窄，不宣称为 OpenAI 官方限额。实现只依据公开协议、自有契约和独立编写的测试，不复制、翻译或移植参考项目/归档实现，也不把其 SDK 加入执行核心。本增量不增加第三方依赖；既有许可证记录不变。新源码注释须记录独立来源。

本段只覆盖已合格的 `openai-compatible` API Key、显式 `embedding` 模型和 `openai-embeddings` 实际 wire。没有 DDL、管理配置、会员凭据、真实上游调用、`base64`、流式、跨协议转换或 `user` 管理 UI。旧 Key 不自动获得 Embeddings 权限；已有员工 Key、模型和路由授权必须先成立，`user` 值不得改变其结果。

## 输入、wire 和旧请求兼容

`user` 可省略；若提供，必须是 JSON string，解码后的值长度为 **1..128 个 ASCII 字节**，每个字符严格属于 `A-Z`、`a-z`、`0-9`、`_`、`-`。例如 `team_member-7` 合法；空、空白、非 ASCII、姓名中的空格、邮箱中的 `@`、其他符号、null、数字、数组、对象、布尔、超长值以及重复 JSON key 全部拒绝。Unicode 转义按 JSON 解码后的字符验证，不能借转义绕过限制。非法值返回固定脱敏 `invalid_request_error`，既有请求体/规范化 wire 4 MiB 上限继续适用；不返回或记录值。

四种现有 `input` 形状和显式 `dimensions` 均可与合法 `user` 同时使用；`encoding_format` 仍只允许省略或显式 `float`。在已选且最终重核的合格实际 wire 上，客户端显式提供时，上游 JSON 仅增加一个语义值完全相同的 `user` 字符串；不使用员工资料填补、修改、哈希或去重它。省略时上游 wire **不出现** `user`，其余 JSON 形状和旧结果完全不变。现有模型 ID 替换、input 保真、float 规范化与 dimensions 资格仍独立生效。派发前拒绝必须是 0 durable attempt、0 上游网络。

## 隐私与信任边界

`user` 只是员工 Key 持有人自行选择、未经本服务验证的上游提示。它**不是** CPA Cloud 的员工 ID、管理员 ID、Key ID、审计 actor 或已核验安全标识；不得用于鉴权、模型/账号/渠道选择、治理、预算、用量账本、计费或审计身份。服务不得从员工记录自动生成该字段，不得在日志、错误、指标、数据库、WAL、审计或客户端响应中保存/回显它。仅在本次请求内短暂持有并转交最终选中上游；上游收到后如何处理不由本服务保证。文档和管理说明建议调用方使用非个人可识别的随机或哈希代号，不传姓名、邮箱或其他个人信息；字符白名单不能自行证明某值不是个人信息。

## 响应、失败与回滚

上游响应仍必须是既有严格 float/list/data/index/model/usage 形状；若上游额外回显 `user` 或返回错误正文，不透传给客户端。成功时 raw usage 和价格仍只按实际账号、实际模型与 wire 计量，不含 `user`。非 2xx、坏响应、取消、存储故障、未知提交、重启与撤销均保持前三段的固定脱敏错误、一次 attempt 和不重放语义。`user` 不参与持久化、恢复或结算，因此不能成为跨请求的隐式会话键。

测试至少覆盖：四种输入形状及有/无 `user` 的上游 wire、显式 float/dimensions 组合；边界字符和 1/128 字节、JSON 转义、空/非法类型/非 ASCII/超长/重复字段；旧请求逐字段 wire 保真、base64 继续拒绝；Key/模型授权、strict 预算、取消、撤销、存储失败和重启的一次派发回归；上游响应回显或故障正文不外泄，日志/数据库/WAL 不留 `user` 明文。只使用合成上游与动态非 8787 回环端口，分别报告本地、GitHub CI 和独立进程验收，不把真实 provider/客户端或未测试故障写成已验证。

本批无 schema 或配置迁移。回滚到上一已验证版本后，显式 `user` 请求重新返回 `unsupported_feature`；省略字段的旧调用和已存账本无需转换。回滚前仍要停服并保留完整数据目录及旧程序，不让新旧进程并发写同一库。本批不创建 tag、安装包、部署或 release。
