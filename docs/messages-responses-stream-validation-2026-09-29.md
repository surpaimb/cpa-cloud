# Messages ↔ Responses SSE 独立验收（2026-09-29）

## 固定对象与边界

- 生产源码：`d63da4c2c58fe3c27b1645b4370d864f88edb12e`
- Windows 验收二进制：`C:\Users\apple\.codex\tmp\cpa-cloud-messages-sse-d63da4c\cpa-cloud.exe`
- 二进制 SHA-256：`5BBE3E8159D08A0D0D6CB7B1260F3B1C25555CE7563B55F54709BC465A794822`
- 系统：Windows 10.0.19045 x64
- 进程脚本：`scripts/verify-messages-responses-stream.mjs`
- 上游、员工和管理员凭据全部随机生成；数据目录、Claude HOME、端口和服务进程全部隔离且在结束时清理。
- 本轮只访问合成 loopback 上游，没有访问真实供应商、真实凭据、8787、部署、发布或打包，也没有修改生产代码。

脚本依据本仓库协议合同与公开协议资料独立编写，只使用 Node.js 内置模块，没有新增第三方依赖。协议依据是 OpenAI 的
[Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses) 与
[Function calling](https://developers.openai.com/api/docs/guides/function-calling)，以及 Anthropic 的
[Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)。未复制、翻译或改写归档 CPA、
CLIProxyAPI、Sub2API 或其他实现代码。

## 已测试行为

真实 CPA Cloud 进程与合成 loopback 上游之间完成以下验收：

- Messages 客户端到 Responses 上游固定走 `/v1/responses`；Responses 客户端到 Messages 上游固定走
  `/v1/messages`。除刻意在进入 CPA 前被拒绝的实际 Claude Code 请求外，每个独立场景都是 1 个父请求、1 个
  attempt、1 次上游调用，没有重放。
- Messages → Responses 在 usage 已由 `response.created` 或 `response.in_progress` 确定时为 **LIVE**：首个目标语义事件
  在上游 EOF 前交付。usage 只在 terminal 才确定时为 **DELAYED**：clean EOF 前没有目标语义字节泄漏，随后整流有界释放；
  此结果不声称生成期间实时流式。
- 两个方向的文本、工具调用、分段工具参数和下一次独立请求中的工具结果均完成转换，call ID 与工具名保持稳定。
- Messages 的多个 `message_delta.usage` 快照按累积值替换而非求和；省略的可选 usage 桶保留先前值。显式 `null` 的可选
  usage 桶也保留先前已知值，最终账本为 input `12`、output `5`、cache-read `3`、cache-write `2`。
- Responses usage 转为 Messages 后，普通 input、output、cache-read、cache-write 分别为 `7/5/3/2`。
- Responses `incomplete` 和 Messages 转换得到的 incomplete 都记为 `interrupted`；Responses `failed`、Messages `error`
  及上游 HTTP 503 都记为 `failed`，固定客户端错误不泄漏上游秘密。
- usage 从始至终未知时返回 HTTP 502，目标 Messages 语义事件为 0，但实际仍有 1 个父请求、1 个 attempt 和 1 次
  upstream dispatch；账本四个 usage 桶保持 `NULL`，没有未知转零。
- 已发送 `message_start` 和文本后，若 terminal usage 将必需字段变为 `null`，先前目标事件确实已交付，但不发送成功
  terminal；请求/attempt 记为 `failed`，usage 保持未知。
- 两个方向的 malformed event、未知 Messages event、重复 terminal、缺 terminal EOF 和 terminal 后不关闭均不输出客户端
  成功 terminal。malformed/unknown/duplicate 记为 `failed`，missing/hang 记为 `interrupted`；terminal 后不关闭在 2 秒
  drain 上限后释放资源。
- 客户端主动关闭会取消唯一上游调用并将请求/attempt 记为 `cancelled`，没有重放。
- 原始 TCP 客户端停止读取时，30 秒逐事件写 deadline 生效；收紧证据断言后的复验观测为 `30242 ms`，随后上游 context 被取消，
  请求/attempt 均为 `cancelled`，仍只有一次 dispatch。
- 每个合成场景都检查员工 key 不在上游 URL、任一 header、原始 body 或服务日志中；员工 key、两类上游 key、管理员
  密码、提示和工具结果标记均不在服务日志中。

组件层另外通过：

- `internal/protocolconv` 的 Messages/Responses 顺序、工具、usage、EOF 和 terminal 定向测试；
- `internal/accounting` 的 Messages 累积 usage、显式 `null` 保留和非法 delta 丢弃测试；
- `internal/service` 的两个方向显式 wire/runtime、单 dispatch、结果结算、clean EOF、非法 tail、terminal drain、取消与
  backpressure 定向测试；
- native Messages JSON/SSE、工具/结果、count-tokens、取消、错误脱敏、撤销和 native preflight 回归。

终端事件 write/flush 失败需要可注入 writer，因此只以确定性组件测试验证；真实进程层覆盖普通 clean EOF、客户端关闭和
TCP 慢读，没有把不可注入的进程层 flush 故障表述为已测试。

## 发现并闭环的缺陷

首次固定的生产源码 `45ae1b733d476f82dd0ba1425e672bb7bd1be91a` 与二进制
`C:\Users\apple\.codex\tmp\cpa-cloud-messages-sse-45ae1b7\cpa-cloud.exe`（SHA-256
`8C6609146C06BD0AE76A7347CE40EED6C581D3D73AE874551080872C882249D6`）暴露了一个真实账本缺陷：合法的累积
`message_delta.usage` 若对可选字段显式给出 `null`，流转换会保留旧值，但账本会错误清空 input 和 cache-read。

生产提交 `d63da4c2c58fe3c27b1645b4370d864f88edb12e` 修复该行为并新增组件回归。本页完整真实进程矩阵在该固定提交与新二进制上
重新执行，所有合成场景通过，显式 `null` 场景账本为预期的 `12/5/3/2`。

## Actual Claude Code

实际 `Claude Code 2.1.266`（SHA-256
`D2C5F7B3B6A12819097CEB6EFBCE2A390157166003FCAEE32DBDE0E6D7B45EF7`）在隔离 HOME、禁用工具、遥测、更新和重试的
条件下发出两次 `/v1/messages` 请求。请求包含当前转换边界无法表示的字段组合（包括 `metadata`、`output_config`，其中一次还
包括 `context_management` 和 `thinking`），CPA 均在转换/dispatch 前返回 HTTP 400。证据为 0 父请求、0 attempt、0 上游调用；
此项结论是 **UNSUPPORTED**，不是 PASS，也不代表 Claude CLI、Anthropic 供应商或会员账号兼容性已通过。

## Native smoke 说明

旧的组合脚本 `scripts/smoke-native-providers.mjs` 在 Anthropic model discovery 阶段仍断言 `Authorization: Bearer`，而当前
生产实现及其定向测试在 discovery 阶段使用 `X-Api-Key`，因此该旧脚本会由自身 mock 返回 500，并在管理端表现为 502。没有修改该旧
脚本，也没有把这个 harness mismatch 计为生产失败；对应 model discovery 定向测试与 native Messages 执行测试均通过。

## 未覆盖与保留限制

- 未访问真实 Anthropic/OpenAI 网络、API key 或会员授权；实际 Claude Code 只证明当前请求形状被安全拒绝。
- 未覆盖 Messages/Gemini 其他方向、旧三 CLI 全矩阵、托管工具、媒体、background、state、previous response 或 conversation。
- 本机 TCP backpressure 不等同于跨公网代理、负载均衡器或各云平台超时行为。
- 本页记录 Windows 实际结果；完整 Linux Go、race、vet、构建与 CI 结果只以集成分支最终 run 为准。

## 可复现命令

```powershell
node scripts/verify-messages-responses-stream.mjs `
  --server C:\Users\apple\.codex\tmp\cpa-cloud-messages-sse-d63da4c\cpa-cloud.exe `
  --source-commit d63da4c2c58fe3c27b1645b4370d864f88edb12e `
  --claude C:\Users\apple\AppData\Local\Packages\Claude_pzs8sxrjxfjjc\LocalCache\Roaming\Claude\claude-code\2.1.266\claude.exe
```
