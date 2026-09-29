# Gemini v1beta ↔ Responses SSE 独立验收（2026-09-29）

## 固定对象与边界

- 生产源码：`33ccc38398cd816d118f045948c77dcd4ccffa8b`
- Windows 验收二进制：`C:\Users\apple\.codex\tmp\cpa-cloud-pr11-33ccc383\cpa-cloud.exe`
- 二进制 SHA-256：`4365EBAA9A24113901B43B9490A095693EB2A9A7BB62BD9D07A3CDEB4EFE0F97`
- 系统：Windows 10.0.19045 x64
- 进程脚本：`scripts/verify-gemini-responses-stream.mjs`
- 每轮均使用全新临时 data-dir、通过 stdin 合成的随机管理员密码、随机非 8787 回环端口、随机员工及上游 key，结束时验证并清理临时目录。
- 本轮只访问合成 loopback 上游，没有访问 Google/OpenAI、真实凭据、真实会员、8787、部署、发布或打包，也没有修改生产代码。
- 只验证 PR11 的 Gemini v1beta `streamGenerateContent` ↔ OpenAI Responses SSE 增量；没有重跑 PR7/PR9 旧全矩阵。

脚本依据本仓库批次契约与公开协议资料独立编写，只使用 Node.js 内置模块，没有新增第三方依赖。协议依据是 Google 的
[GenerateContent / streamGenerateContent](https://ai.google.dev/api/generate-content)，以及 OpenAI 的
[Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses) 与
[Function calling](https://developers.openai.com/api/docs/guides/function-calling)。未复制、翻译或改写归档 CPA、
CLIProxyAPI、Sub2API 或其他实现代码和测试。

## 已测试行为

真实 CPA Cloud 进程与合成 loopback 上游之间完成 26 个允许派发的独立场景；每个场景均为 1 个父请求、1 个 attempt、
1 次出站调用，没有重放：

- Gemini 客户端固定从 `POST /v1beta/models/{public}:streamGenerateContent?alt=sse` 进入，Responses wire 固定调用
  `/v1/responses`；Responses 客户端固定从 `/v1/responses` 进入，Gemini wire 固定调用
  `/v1beta/models/{actual}:streamGenerateContent?alt=sse`。后者没有在 JSON 中伪造 `stream` 字段。
- 两个方向的文本均在生成期间交付首个语义事件，而不是等待完整 upstream EOF。完整 function call 只输出一次；Responses
  分段 arguments 在完成 JSON 对象验证后成为单个 Gemini `functionCall`。显式 call ID 双向保留，Gemini 缺少 call ID 时
  生成稳定非空 ID；下一次独立请求的 `functionResponse` / `function_call_output` 保持 call ID、名称和结构化结果。
- Gemini 上游在首个语义帧同时提供 `responseId` 与 `modelVersion` 时为 **LIVE**。身份晚到场景在身份齐备前交付 0 个
  Responses 语义事件；身份齐备后按原 wire 顺序恰好释放一次，并在 upstream EOF 前继续实时输出。终态仍缺身份时返回
  HTTP 502，不生成 `response.created`、供应商 ID、模型名或成功 terminal；账本记为 `failed`。
- 两个方向均验证成功 terminal 在 upstream terminal frame 到达后仍被扣留，只有 clean physical EOF 后才交付并结算成功。
- Responses `completed` 映射 Gemini `STOP`；Responses `incomplete/max_output_tokens` 映射 Gemini `MAX_TOKENS`；反方向
  `STOP` 映射 `response.completed`，`MAX_TOKENS` 映射 `response.incomplete/max_output_tokens`。incomplete 账本记为
  `interrupted`，不是成功。
- Responses failure、Gemini `SAFETY`、terminal 后坏尾、重复 terminal 和缺 terminal EOF 均不输出客户端成功 terminal。
  failure/bad-tail/duplicate 记为 `failed`，missing terminal 记为 `interrupted`；私有上游错误标记未进入客户端或日志。
- 两个方向的客户端主动关闭都取消唯一 upstream context 并记为 `cancelled`。原始 TCP 客户端停止读取时，30 秒逐事件写
  deadline 分别在 `30286 ms`（Gemini 客户端）和 `30404 ms`（Responses 客户端）后释放唯一上游调用，均无重放。
- Gemini-only Key 调 Responses 入口、Responses-only Key 调 Gemini 入口都在父请求、attempt 和网络调用之前返回 403；
  说明跨协议路由不会继承另一个 `ClientProtocol` 的权限。

## Usage 与协议分层

所有 attempt 的协议均来自实际 upstream wire，而不是客户端入口：Gemini 客户端场景记录 `openai-responses`，Responses
客户端场景记录 `gemini-generate-content`。

- Responses raw usage 为 input `8`、output `4`、cache-read `2`，但缺少 `cache_write_tokens`。转换后的 Gemini terminal
  正确呈现 `promptTokenCount=8`、`candidatesTokenCount=4`、`totalTokenCount=12`、`cachedContentTokenCount=2`；账本按可靠
  原始证据仅记录 ordinary input `NULL`、output `4`、cache-read `2`、cache-write `NULL`。不能把总 input 8 误记为普通 input。
- Gemini raw usage 为 prompt `9`、candidate `5`、cached `3`、total `14`。转换后的 Responses terminal 呈现 input `9`、
  output `5`、total `14`、cached `3`；账本按 Gemini 定义记录 ordinary input `6`、output `5`、cache-read `3`、
  结构上不适用的 cache-write `0`。
- 两种 raw wire 完全省略 usage 时，客户端 terminal 也不伪造 usage，账本四个桶全部保持 `NULL`。
- terminal 缺身份导致转换失败时，已由 raw Gemini 观察器取得的可靠 usage 仍可保留；转换失败不会反过来抹掉 raw 证据，
  也不会把该请求结算为成功。

## 凭据、正文与持久化边界

- 员工 key 不出现在 upstream URL、header 或 body；上游 key 只出现在各自预期的认证 header，不出现在 URL 或 body。
- 提示、函数结果和私有错误标记不进入 URL、header 或服务日志；员工 key、两类 upstream key、管理员密码和这些正文标记
  都不进入服务日志。
- 服务停止后递归检查临时 data-dir，没有发现上述 key、密码、提示、函数结果或私有错误的明文。

## 组件层定向复验

以下最小相关 Go 定向测试通过，没有运行旧 PR7/PR9 进程矩阵：

- `internal/protocolconv`：Gemini/Responses 请求选择、两个流转换器、身份、usage、函数及事件名严格性；
- `internal/accounting`：Responses/Gemini 累积 usage、未知值与 Gemini 派生计数；
- `internal/service`：两个显式 wire 真实 handler、raw observer 顺序、data-only Gemini 输出、late identity、不完整终态；
- 共享流桥的单派发、observer-before-write、terminal clean-EOF 扣留、非法 tail、write failure、drain timeout、取消与
  backpressure 确定性测试。

## Actual CLI 与保留限制

- 本机没有安装 Gemini CLI。存在 `codex-cli 0.158.0-alpha.2.1`（SHA-256
  `8F0554EDE25BBC5450921897C468B2E84635AA513C5017457997AF0954581F49`），但本轮没有已证明能强制其只发送该严格
  text/function 子集的安全调用方式，因此未把不可表示请求硬塞进转换器。实际 CLI 结论为 **UNTESTED**，不是 PASS 或
  UNSUPPORTED。
- 合成 identity-bearing Gemini 流只证明本文严格子集，不代表通用 Gemini stream、真实 Google/OpenAI provider、会员账号、
  SDK 或 CLI 兼容。
- thinking/thought signature、cache 写入语义、媒体、引用/grounding、安全评分成功路径、托管工具、并行或不匹配调用、
  state/background/previous/conversation、多候选及其他未列字段不在本轮支持或验收范围内。
- 真实网络代理、负载均衡器和云平台超时未测试；本机 TCP backpressure 只证明该 Windows 进程及回环链路行为。
- 完整 Linux Go、race、vet、前端构建和 CI 只以集成分支最终 run 为准。本轮未发现生产缺陷。

## 可复现命令

```powershell
node scripts/verify-gemini-responses-stream.mjs `
  --server C:\Users\apple\.codex\tmp\cpa-cloud-pr11-33ccc383\cpa-cloud.exe `
  --source-commit 33ccc38398cd816d118f045948c77dcd4ccffa8b
```
