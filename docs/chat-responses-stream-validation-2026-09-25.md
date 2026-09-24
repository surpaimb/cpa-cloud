# Chat ↔ Responses SSE 独立验收（2026-09-25）

## 固定对象与边界

- 生产源码：`f2d461559c21fcd862c167d63d3eb5b0a672fd34`
- Windows 验收二进制：`C:\Users\apple\.codex\tmp\cpa-cloud-stream-f2d4615.exe`
- 二进制 SHA-256：`1555A2CAEAE55975688CEC515294E2E915BD49383B1C98BC252812FA7330FF50`
- 系统：Windows 10.0.19045 x64
- 进程脚本：`scripts/verify-chat-responses-stream.mjs`
- 上游、员工和管理员凭据全部随机生成；数据目录、Codex HOME、端口和服务进程全部隔离且在结束时清理。
- 本轮没有访问真实供应商、真实凭据、8787、部署、发布或打包，也没有修改生产代码。

脚本是依据本仓库协议合同与公开协议资料独立编写的验收代码，只使用 Node.js 内置模块。协议依据是 OpenAI 的
[Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses) 与
[Function calling](https://developers.openai.com/api/docs/guides/function-calling)；未复制或改写归档 CPA、CLIProxyAPI 或 Sub2API 实现。

## 已测试行为

真实 CPA Cloud 进程与合成 loopback 上游之间完成以下验收：

- Chat Completions 客户端到 Responses 上游固定走 `/v1/responses`；Responses 客户端到 Chat 上游固定走
  `/v1/chat/completions`。两个方向每次独立请求都是 1 个父请求、1 个 attempt、1 次上游调用。
- 两个方向都在上游完整 body 和 EOF 到达前向客户端交付了首个语义事件，证明不是整包缓冲。
- 文本、function call、分段 function arguments 和下一次独立请求中的 function result 均完成双向转换。
- 员工 key 未出现在上游 URL、任一 header 或原始 body，也未出现在服务日志；上游只收到其自身凭据。
- Responses 原始 usage `7/3/10` 转换后可靠账本保留 output `3`，普通 input 与两个 cache 桶保持 `NULL`；Chat 原始
  usage `9/4/13` 转换后 output 为 `4`，其余未知桶保持 `NULL`。无 usage 的工具流四个桶全部保持 `NULL`，没有未知转零。
- 两个方向的 malformed event、重复 terminal、缺 terminal EOF 和 terminal 后不关闭均不输出客户端成功 terminal；
  malformed/duplicate 记为 `failed`，missing/hang 记为 `interrupted`。terminal 后不关闭在 2 秒 drain 上限后释放资源。
- 客户端主动关闭会取消唯一上游调用并将请求/attempt 记为 `cancelled`，没有重放。
- 原始 TCP 客户端停止读取时，30 秒逐事件写 deadline 生效；一次观测为 `30345 ms`，随后上游 context 被取消，
  请求/attempt 均为 `cancelled`，没有第二次 dispatch。
- 生产自带的最小 native Chat JSON/SSE、重启、撤销和凭据隔离 smoke 通过。

组件层另外通过：

- `internal/protocolconv` 的 Chat/Responses stream 顺序、工具、usage、EOF 和 terminal 测试；
- `internal/service` 的单 dispatch、clean EOF、非法 tail、terminal drain、取消、backpressure 测试；
- 终端事件 write/flush 失败不能完成，以及 30 秒逐事件 write/flush deadline 的确定性测试。

## Key 策略合并后的组合复验

PR #8 合入后的 `main` 为 `4090130e59f972951db1a9f1b32d8a4e9b8ee3fa`。流式分支合并该基线后，生产与组合测试提交固定为 `5b74d9be716a22615e85ab5e7326c33d60b77c0a`；Windows 二进制为 `C:\Users\apple\.codex\tmp\cpa-cloud-combined-5b74d9b.exe`，SHA-256 为 `5AE310AC3E9C66BEA61AF03FE6C931032756811883C133D42BED86DA761EDDD5`。

同一二进制重新通过本页完整真实进程矩阵；停止读取在 `30279 ms` 后取消唯一上游请求。另以创建时的显式 Key `selected` 策略运行双向 SSE：只允许 `openai-chat`/`openai-responses` 和三个指定公开模型，两个允许方向各自成功且保持一 parent、一 attempt、原 wire output usage 和未知 input；未列入 Key 策略的第四个模型以及策略收窄后不再允许的 Responses 入口都返回 `403`，上游调用数不变且不新增 parent。允许的 Chat 取消仍只派发一次并取消唯一上游请求。

服务层新增最终事务组合断言：跨协议流完成准入后，Key policy revision 改变会在 attempt/网络前拒绝；策略从允许变为拒绝、再恢复相同允许内容的 ABA 也因 revision 从 1 增至 3 而拒绝。两种情况均为零 attempt、零上游。原 KEY-02 四协议拒绝/目录交集/CAS/重启进程 smoke 也在该组合二进制上再次通过。

组合分支的完整 Linux Go、`CGO_ENABLED=1` race、vet、构建与隔离进程结果只以最终 PR HEAD 的实际 CI 为准；本节不借用合并前两个 PR 的绿色 run。

## Actual Codex CLI

实际 `codex-cli 0.155.0-alpha.16.3`（SHA-256
`a19f8f6c3c9dd5b71b6b1e3eb1ec55d75aafb2fdfb686d9e1f7a5f47db07d0d2`）发出的 Responses 流请求包含
`function`、`namespace` 和 `web_search` 三类 tool。Chat wire 不能无损表示后两类，因此 CPA 在转换/dispatch 前返回 HTTP 400
`invalid_request_error`。证据为 0 父请求、0 attempt、0 上游调用；此项结论是 **UNSUPPORTED**，不是 PASS，也未为客户端放宽生产约束。

## 未覆盖与保留限制

- 本轮没有重新执行 Claude 严格取消、Claude/Gemini 跨协议 SSE、旧三 CLI 全矩阵、会员授权、托管工具、媒体、
  background/state/previous/conversation。
- 真实网络慢读由本机 TCP backpressure 验证，不等同于跨公网代理或负载均衡器验证。
- terminal flush 故障需要可注入 writer，因而使用确定性组件测试验证；真实进程层覆盖的是普通 clean EOF、客户端关闭和
  TCP 慢读，不虚构不可注入的进程层 flush 故障。

## 可复现命令

```powershell
node scripts/verify-chat-responses-stream.mjs `
  --server C:\Users\apple\.codex\tmp\cpa-cloud-stream-f2d4615.exe `
  --source-commit f2d461559c21fcd862c167d63d3eb5b0a672fd34 `
  --codex C:\Users\apple\AppData\Local\OpenAI\Codex\bin\80f78947ad880e6e\codex.exe
```
