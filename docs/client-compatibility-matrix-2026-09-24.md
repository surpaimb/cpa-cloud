# 真实客户端兼容矩阵（2026-09-24）

本轮使用未经修改的 Codex CLI、Claude Code 和 Gemini CLI，连接真实 CPA Cloud 进程与任务内临时合成上游。它验证客户端实际发出的协议、SSE、工具回合、取消、服务重启和员工 Key 撤销；不访问真实供应商，不证明会员账号或供应商生产端点可用。

## 固定环境与来源

| 组件 | 固定版本 | SHA-256 / 来源 | 备注 |
| --- | --- | --- | --- |
| Windows | 10.0.19045 x64 | 本机运行环境 | 本轮唯一操作系统样本 |
| CPA Cloud | 集成提交 `b997206` | 测试二进制 `a3a72255aa6945e4f594ed5319ab9baa194a6efc0282728fde915f04614bcf92` | 临时 Go 1.26.8 工具链构建；包含 `f7f1e91` Responses 空闲流断连探测；服务自报版本 `dev` |
| Codex CLI | `codex-cli 0.155.0-alpha.16.3` | `a19f8f6c3c9dd5b71b6b1e3eb1ec55d75aafb2fdfb686d9e1f7a5f47db07d0d2` | 本机已有官方 Codex 安装，不随仓库分发 |
| Claude Code | `2.1.266 (Claude Code)` | `d2c5f7b3b6a12819097ceb6efbce2a390157166003fcaee32dbde0e6d7b45ef7` | 本机已有官方 Claude 安装，不随仓库分发 |
| Gemini CLI | `0.61.0` | 入口 `gemini.js`：`0b6e283ae88682b0e27e8ef85a608ab74807a1513dc0c053e5aa80d5b80b29ab` | 仅装入任务临时目录；包声明 Apache-2.0，不加入产品依赖 |
| CC Switch | `3.20.3` | `75f89e422f8627959a82c7ac94eea5a44bdf0f0a8d885f689402060c8c750dd8` | 只核对已安装可执行文件元数据；见下方 SKIP |

客户端配置依据各自官方配置文档和 CC Switch 官方发布仓库；链接记录在[协议来源](protocol-sources.md)。测试脚本为本项目独立编写，不导入客户端 SDK、参考实现代码或其测试。

## 结果

PASS 表示本轮固定版本和固定平台上观察到预期结果；FAIL 是可复现的具体差异；SKIP 表示没有取得该项证据。

| 客户端 | 文本 SSE | 工具调用及结果回传 | 客户端终止后取消 | 服务重启后原 Key | 撤销后零上游派发 |
| --- | --- | --- | --- | --- | --- |
| Codex CLI | PASS | PASS：两次 Responses 请求，执行只读 `get_goal`，第二次带 `function_call_output` | PASS | PASS | PASS |
| Claude Code | PASS | PASS：1 次无工具辅助请求 + 2 次带合成 MCP `echo` 的工具对话请求，后续含 `tool_result` | **FAIL**：见取消证据 | PASS | PASS |
| Gemini CLI | PASS | PASS：两次原生 generation 请求，真实执行只读 `read_file`，后续含 `functionResponse` | PASS | PASS | PASS |
| CC Switch 3.20.3 图形化配置 | SKIP | SKIP | SKIP | SKIP | SKIP |

文本请求到达的真实入口分别是 `/v1/responses`、`/v1/messages` 和 `/v1beta/models/{model}:streamGenerateContent`。Gemini 0.61.0 实测要求员工 Key 接受严格单值 `x-goog-api-key`，工具回合还会在 `functionCall` Part 同级回传 `thoughtSignature`；对应最小严格兼容分别来自 `2a6aed3` 和 `1bdd3a4`，且未知字段、双认证头和非法签名仍拒绝。

### 取消证据

- Codex：`f7f1e91` 修复后，终止脚本创建的完整客户端进程树会在 10 秒界限内关闭上游响应。账本只有 1 个员工请求、1 个上游 attempt，状态为 `cancelled`，没有残留 TCP 连接，PASS。相同二进制上的普通文本 SSE 和两请求工具回合也保持 PASS，空闲心跳没有破坏 Codex 事件解析。
- Claude：观察到 2 个不同员工请求，各只有 1 个 attempt，均最终为 `cancelled`，没有残留 TCP 连接。因此这是 Claude Code 在终止边界附近重发员工请求，不是 CPA Cloud 在单个员工请求内重试。当前兼容验收仍按“取消只产生一次逻辑请求”判为 FAIL。
- Gemini：1 个员工请求、1 个 attempt，状态为 `cancelled`，上游连接关闭，无重放，PASS。

## 显式 Wire 路由专项复测

协议路由集成后在精确提交 `13b3612965629a097ddce014335b99f716db7226` 追加了一轮窄范围复测。Windows 测试二进制 SHA-256 为 `7d0779b029ad71af0b87b5e3254e01f870585fe74a90ce1ad2b5fafb0bc0a69c`；三个客户端版本和哈希与上表相同。该轮仍只使用随机回环端口、临时数据目录、随机员工 Key 和合成上游。

原始协议客户端通过真实 CPA Cloud 进程发出六条显式 `wire_protocol` 非流式路线；每条都分别执行文本、function call 和 function result：

| 员工入口 → 实际 Wire | 实际上游路径 | 文本 | 工具调用 | 工具结果 | parent / attempt |
| --- | --- | --- | --- | --- | --- |
| Chat → Responses | `/v1/responses` | PASS | PASS | PASS | 每次 `1 / 1` |
| Responses → Chat | `/v1/chat/completions` | PASS | PASS | PASS | 每次 `1 / 1` |
| Messages → Responses | `/v1/responses` | PASS | PASS | PASS | 每次 `1 / 1` |
| Gemini `generateContent` → Responses | `/v1/responses` | PASS | PASS | PASS | 每次 `1 / 1` |
| Responses → Messages | `/v1/messages` | PASS | PASS | PASS | 每次 `1 / 1` |
| Responses → Gemini `generateContent` | `/v1beta/models/{model}:generateContent` | PASS | PASS | PASS | 每次 `1 / 1` |

Gemini 两侧的正向工具回合均省略 `functionCall` / `functionResponse` 的可选 ID；CPA Cloud 为无 ID 调用生成确定的内部 call ID。有显式 ID 时严格按 ID 关联；缺 ID 的结果仅在存在唯一未完成同名调用时关联，不按顺序猜配，也不改写任何显式 ID。重复显式 ID 或同名多待处理调用下无法唯一匹配的无 ID 结果仍由集成测试失败关闭，本脚本不把歧义情形当作可用能力。

可靠用量按实际 Wire 记录，不按员工响应格式猜测。上游不返回 usage 时四类 Token 均保持未知；OpenAI Chat/Responses 仅能独立证明 output 时，input/cache 保持未知；Messages 的 input/output 可分别证明；Gemini 在缺 `cachedContentTokenCount` 时 input/cache-read 保持未知、output 已知，而 generateContent 不存在的 cache-write 类别记为已知 `0`。员工 Key 未出现在任何上游请求。

本节记录的 2026-09-24 二进制尚未实现跨协议 SSE：六条原始协议流请求都返回明确 `400`，且上游调用和 attempt 都是 `0`。未经修改的三个 CLI 也分别实测了实际请求形状：Codex `/v1/responses`（含 `function`、`namespace`、`web_search` 工具类型）、Claude `/v1/messages`、Gemini `:streamGenerateContent`。捕获代理只保留安全响应元数据，并核实 CPA 分别返回 Codex `400/unsupported_feature`、Claude `400/invalid_request_error`、Gemini `400/INVALID_ARGUMENT`，三者均被固定分类为 `route_not_representable`；因此非零退出确由跨协议流路由拒绝产生，而不是鉴权或其他字段提前失败。三者仍为 `0` 上游调用、`0` attempt。parent 可能在预检拒绝前创建，也可能不创建，因此验收不把 parent 数固定为安全边界。2026-09-25 后续源码只新增 Chat Completions ↔ Responses 两个文本 SSE 方向，边界和独立证据见[流式转换契约](chat-responses-stream-contract-2026-09-25.md)；本历史矩阵的原结果不据此改写。

同一最终二进制又运行了实际客户端的 `--scope core` 原生/`legacy-native` 回归：Codex、Claude、Gemini 文本 SSE 全部 PASS；Codex 两请求 `get_goal`、Claude 三请求（含一次辅助请求）MCP `echo`、Gemini 两请求 `read_file` 工具回合全部 PASS。专项过程中曾发现 provider-managed tools 被生命周期校验过早拒绝、导致 Codex 原生流零派发的回归；`13b3612` 将该校验限制到服务拥有的 stateful/background/previous 生命周期，恢复无状态原生透明转发，同时保持跨协议和有状态请求失败关闭。

这轮没有重跑取消、服务重启或 Key 撤销；这些结果仍只属于前述 `b997206` 矩阵，Claude 严格取消仍为 FAIL。它也没有访问真实供应商、会员账号或 CC Switch GUI，因此不能把本节的 PASS 外推为真实 provider 或会员兼容。

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
  --scope core \
  --server <cpa-cloud-binary> \
  --codex <codex-executable> \
  --claude <claude-executable> \
  --gemini-entry <gemini.js>
```

显式 Wire 路由专项使用独立脚本，并要求调用方把完整源提交写入输出：

```text
node scripts/smoke-protocol-routes.mjs \
  --source-commit <full-source-commit> \
  --server <cpa-cloud-binary> \
  --codex <codex-executable> \
  --claude <claude-executable> \
  --gemini-entry <gemini.js>
```

脚本为每次运行创建独立临时 HOME、配置目录、服务数据目录、随机端口、随机员工 Key 和合成上游凭据；不读取用户客户端配置。它只终止自己创建的客户端进程树，结束后校验临时根目录已删除。输出不包含客户端原始 stdout/stderr、提示词、Key 或 Token；服务日志和持久文件还会扫描合成秘密与提示词。员工 Key 未出现在上游请求，撤销后的三个客户端均为零上游派发。

不带 `--scope core` 的完整旧矩阵仍包含 Claude 取消这一项明确 FAIL，脚本按设计返回非零；不能把其余 PASS 汇总成整体通过。`--scope core` 只运行本节记录的原生文本与工具回归。
