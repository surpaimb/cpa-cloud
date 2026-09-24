# 真实客户端兼容矩阵（2026-09-24）

本轮使用未经修改的 Codex CLI、Claude Code 和 Gemini CLI，连接真实 CPA Cloud 进程与任务内临时合成上游。它验证客户端实际发出的协议、SSE、工具回合、取消、服务重启和员工 Key 撤销；不访问真实供应商，不证明会员账号或供应商生产端点可用。

## 固定环境与来源

| 组件 | 固定版本 | SHA-256 / 来源 | 备注 |
| --- | --- | --- | --- |
| Windows | 10.0.19045 x64 | 本机运行环境 | 本轮唯一操作系统样本 |
| CPA Cloud | 临时组合树：`981ab83` + `1bdd3a4` | 测试二进制 `df1d76a18cf13df63561dbd608303e4b56df038596000839aac25744395fef08` | 两项 Gemini 兼容修复已进入 `869c1d7`；该集成提交还包含本轮二进制未覆盖的其他变化，不能据本轮结果宣称整个 `869c1d7` 已实测 |
| Codex CLI | `codex-cli 0.155.0-alpha.16.3` | `a19f8f6c3c9dd5b71b6b1e3eb1ec55d75aafb2fdfb686d9e1f7a5f47db07d0d2` | 本机已有官方 Codex 安装，不随仓库分发 |
| Claude Code | `2.1.266 (Claude Code)` | `d2c5f7b3b6a12819097ceb6efbce2a390157166003fcaee32dbde0e6d7b45ef7` | 本机已有官方 Claude 安装，不随仓库分发 |
| Gemini CLI | `0.61.0` | 入口 `gemini.js`：`0b6e283ae88682b0e27e8ef85a608ab74807a1513dc0c053e5aa80d5b80b29ab` | 仅装入任务临时目录；包声明 Apache-2.0，不加入产品依赖 |
| CC Switch | `3.20.3` | `75f89e422f8627959a82c7ac94eea5a44bdf0f0a8d885f689402060c8c750dd8` | 只核对已安装可执行文件元数据；见下方 SKIP |

客户端配置依据各自官方配置文档和 CC Switch 官方发布仓库；链接记录在[协议来源](protocol-sources.md)。测试脚本为本项目独立编写，不导入客户端 SDK、参考实现代码或其测试。

## 结果

PASS 表示本轮固定版本和固定平台上观察到预期结果；FAIL 是可复现的具体差异；SKIP 表示没有取得该项证据。

| 客户端 | 文本 SSE | 工具调用及结果回传 | 客户端终止后取消 | 服务重启后原 Key | 撤销后零上游派发 |
| --- | --- | --- | --- | --- | --- |
| Codex CLI | PASS | PASS：两次 Responses 请求，执行只读 `get_goal`，第二次带 `function_call_output` | **FAIL**：见取消证据 | PASS | PASS |
| Claude Code | PASS | PASS：1 次无工具辅助请求 + 2 次带合成 MCP `echo` 的工具对话请求，后续含 `tool_result` | **FAIL**：见取消证据 | PASS | PASS |
| Gemini CLI | PASS | PASS：两次原生 generation 请求，真实执行只读 `read_file`，后续含 `functionResponse` | PASS | PASS | PASS |
| CC Switch 3.20.3 图形化配置 | SKIP | SKIP | SKIP | SKIP | SKIP |

文本请求到达的真实入口分别是 `/v1/responses`、`/v1/messages` 和 `/v1beta/models/{model}:streamGenerateContent`。Gemini 0.61.0 实测要求员工 Key 接受严格单值 `x-goog-api-key`，工具回合还会在 `functionCall` Part 同级回传 `thoughtSignature`；对应最小严格兼容分别来自 `2a6aed3` 和 `1bdd3a4`，且未知字段、双认证头和非法签名仍拒绝。

### 取消证据

- Codex：终止脚本创建的完整客户端进程树后等待 10 秒，合成上游响应仍未关闭。账本只有 1 个员工请求、1 个上游 attempt，状态仍为 `pending`；`Get-NetTCPConnection` 显示连接由 CPA Cloud PID 持有，已终止的客户端 PID 不持有。因此这是 CPA Cloud 的上游取消传播失败，不是 CLI 重试。
- Claude：观察到 2 个不同员工请求，各只有 1 个 attempt，均最终为 `cancelled`，没有残留 TCP 连接。因此这是 Claude Code 在终止边界附近重发员工请求，不是 CPA Cloud 在单个员工请求内重试。当前兼容验收仍按“取消只产生一次逻辑请求”判为 FAIL。
- Gemini：1 个员工请求、1 个 attempt，状态为 `cancelled`，上游连接关闭，无重放，PASS。

## CC Switch 与真实会员边界

本轮电脑控制通道没有暴露可操作的原生应用表面，无法用实际 CC Switch 3.20.3 完成添加供应商、切换配置和还原配置。因此 CC Switch GUI 项保持 SKIP；没有读取或修改它的私有数据库，也没有用猜测的内部格式代替真实 UI 流程。后续复测必须记录点击流程、最终由哪个 CLI 发请求、原配置备份及恢复结果。

没有搜索本机真实凭据或账号配置，也没有获得一个明确授权的非秘密测试入口，所以真实会员验证保持未执行。继续验证至少需要：

1. ChatGPT/Codex：部署者自有且允许本应用回调地址的 client ID、专用测试账号、明确回调 URI，以及登录、刷新、撤销和重启恢复验收；不能借用其他应用 client ID。
2. Claude：供应商明确允许该部署模式的订阅凭据调用与生命周期合同，或改为未修改 Claude Code 中每位终端用户走 Anthropic 自有认证；API Key PASS 不等于订阅会员代理。
3. Gemini：CPA Cloud 自有应用可用的会员后端、scope、权益、刷新及撤销合同；AI Studio API Key PASS 不等于 Gemini 订阅额度。

完整条件见[会员接入条件与当前阻塞项](research/membership-provider-readiness.md)。

## 可复现脚本与隔离

[`scripts/smoke-real-clients.mjs`](../scripts/smoke-real-clients.mjs) 需要显式传入四个绝对路径：

```text
node scripts/smoke-real-clients.mjs \
  --server <cpa-cloud-binary> \
  --codex <codex-executable> \
  --claude <claude-executable> \
  --gemini-entry <gemini.js>
```

脚本为每次运行创建独立临时 HOME、配置目录、服务数据目录、随机端口、随机员工 Key 和合成上游凭据；不读取用户客户端配置。它只终止自己创建的客户端进程树，结束后校验临时根目录已删除。输出不包含客户端原始 stdout/stderr、提示词、Key 或 Token；服务日志和持久文件还会扫描合成秘密与提示词。员工 Key 未出现在上游请求，撤销后的三个客户端均为零上游派发。

由于当前矩阵包含两个明确 FAIL，脚本按设计返回非零；不能把其余 PASS 汇总成整体通过。
