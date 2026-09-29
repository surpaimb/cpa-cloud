# KEY-02 trusted-proxy X-Forwarded-For 独立验收（2026-09-29）

## 固定对象与边界

- 生产源码：`202b62280bd6c61ce5091ee449a52db61cf79c56`，其历史包含本批指定基线
  `main@3cfba759e6ebc71f5951956586f17496949dd50f`。
- Windows 验收二进制：`C:\Users\apple\.codex\tmp\cpa-cloud-pr12-202b622\cpa-cloud.exe`。
- 二进制 SHA-256：`6508D7F7255D6307C45F8F2D64831896EA7D2D7718B494182411328C9E331ABB`。
- 系统：Windows 10.0.19045 x64；Node.js `v22.22.0`。
- 进程脚本：`scripts/verify-trusted-proxy-xff.mjs`。
- 每轮只使用随机非 8787 回环端口、全新临时 data-dir、stdin 提供的随机管理员密码、随机员工 Key、随机上游凭据和合成
  loopback 上游；结束时检查并清理临时数据。
- 没有访问真实反向代理、真实供应商、真实凭据、会员账号或现有 8787 服务，没有部署、发布、打包或修改生产代码。

脚本依据[本批契约](gemini-stream-trusted-proxy-batch-contract-2026-09-29.md)、
[KEY-02 socket-peer 契约](messages-stream-key-ip-batch-contract-2026-09-25.md)和当前预览契约独立编写，只使用 Node.js
内置模块。没有读取或使用归档 CPA、CLIProxyAPI、Sub2API、`../cpa-cloud-reference` 或 `../cpa` 的实现、测试或资产。

## 真实进程已测试行为

### 启动、默认值与不可信 peer

- 重复规范化为同一网段的 `--trusted-proxy-cidr 127.0.0.1/32` 与 `127.0.0.1` 使进程启动失败；尚不存在的 data-dir
  保持不存在，证明校验发生在状态创建和监听之前。
- 空可信集合时，`features.trusted_proxy_source=false`。真实 socket peer `127.0.0.1` 是唯一来源；伪造的合法 XFF、畸形
  XFF、`Forwarded` 和 `X-Real-IP` 均被忽略，三次 Chat 请求正常派发。
- 只显式信任 `10.0.0.0/8` 时，实际回环 peer 仍不可信。畸形逗号链和两个物理 XFF 行均不参与校验；请求继续按
  `127.0.0.1` 授权。非空配置只使信息性 feature 变为 `true`，没有自动信任回环或私网。

### 显式可信回环与保守链解析

可信集合 A 为 `127.0.0.1/32`、`10.0.0.0/8`、`2001:db8:1::/48`。实际 peer 命中后，进程层验证：

- 单 hop `198.51.100.22` 成为来源；多 hop
  `203.0.113.66, 198.51.100.22, 10.0.0.7` 从右向左跳过可信 `10/8`，选择第一个不可信
  `198.51.100.22`。只允许更左侧 `203.0.113.0/24` 的 Key 被 403 拒绝，证明更左的伪造值不会被当作来源。
- 混合链 `198.51.100.22, 2001:db8:1::7` 跳过显式可信 IPv6 hop。IPv4 `/24` 的末地址、IPv6 `/48` 内地址和
  `::ffff:192.0.2.44` 映射 IPv4 均匹配；相邻 IPv4 `/24` 与 IPv6 `/48` 外地址均被拒绝。
- 同时携带有效 XFF 和恶意 `Forwarded` / `X-Real-IP` 时仍只按 XFF 解析。可信入口只有后两种 header 而缺少 XFF 时
  失败关闭。

可信入口的下列输入均返回固定 403，且没有上游调用：缺失 XFF、两个物理 XFF 行、hostname/畸形 token、空成员、65 hop、
超过 4096 字节但仍只有 64 个数值 hop 的 header，以及全链均可信。测试客户端不会把超限值规范化掉；超限样例使用成员间
内部 SP 填充，在线上保持超过 4096 字节。

### 入口、目录、资源与错误封装

同一个畸形可信链分别进入四个模型入口：

- Chat Completions 与 Responses 返回 OpenAI 形状的 `model_not_allowed`；
- Messages 返回 Anthropic 形状的 `permission_error`；
- Gemini `generateContent` 返回 Google RPC 形状的 `PERMISSION_DENIED`；
- 所有错误文案均固定为 `This request is not allowed for this key.`，没有回显畸形 header。

OpenAI `/v1/models`、Gemini `/v1beta/models`，以及 Responses resource 的 read、cancel、delete 同样先执行统一来源解析。
本轮共对 20 个带 request ID 的来源/CIDR 拒绝逐一查询 SQLite，均为零 `model_requests`、零
`accounting_requests`、零 `accounting_attempts`；两个物理 header 的 raw HTTP 场景另由零上游计数证明，没有将被 HTTP
客户端合并后的逗号值冒充物理重复行。

### Key CIDR 收紧、同 revision/CAS 与最终派发边界

- Key 的 protocol/model/source 初始共享 revision 1。显式把来源从原网段收紧到 `203.0.113.0/24` 后，同一 policy revision
  变为 2；再次用旧 revision 1 更新返回 `409 revision_conflict`。
- 收紧后，原 `198.51.100.22` 请求在治理、父请求、attempt 和上游之前返回 403；数据库对该 request ID 为零父请求、
  零 attempt，loopback mock 调用数不变。
- 当前源码的最终派发事务专项覆盖 `source_narrowing`、policy revision/ABA 和 trust revision 变化：已通过
  初始认证/路由准备的旧快照在 transaction 内失败关闭，零 attempt、零上游。真实 HTTP 进程没有测试专用的 barrier hook，
  因此没有把不可确定注入的进程层竞态伪装为确定性结果。

### 后台 claim、信任 revision 变化与不重放

同一 worker 创建两个 background Responses 资源：

1. 第一项通过最终 durable dispatch barrier，数据库为 `dispatch_authorized` 或 `in_progress`，已有一个 attempt 与一个
   dispatch marker；合成 `/v1/responses` 上游随后保持连接不返回。
2. worker 被第一项占用时创建第二项，数据库仍为 `queued`，`attempt_id=NULL`，没有 legacy `model_requests` 执行行或 attempt。
3. 强制结束进程后，用同一 data-dir 和不同可信集合 B（`127.0.0.1/32`、`192.168.0.0/16`）重启。

重启结果：

- 第一项由 restart recovery 固定为 `interrupted`，仍只有原来的一个 attempt/dispatch；上游总调用数保持 1，没有重放。
- 第二项因持久的 `source_trust_revision` 与当前集合不一致，在 claim 时固定为 `interrupted`，保持零 legacy
  `model_requests`、零 attempt、零 dispatch、零上游。
- 两项创建时持久的 trust revision 相同且是 64 位小写 SHA-256。资源 read 在集合 B 下使用新的有效 XFF，只看到各自
  `interrupted` 状态，没有暴露持久来源或 header 链。

校准：background create 本身按设计已经创建 durable `accounting_requests` 父记录；因此第二项的严格结论是“claim 后零执行行/
attempt/dispatch/上游”，不能错误写成“资源创建后零 durable accounting parent”。上文 20 个初始来源/CIDR 拒绝才是完整的
零父请求、零 attempt、零上游证据。

## 凭据、正文、日志与持久化

- 每次合成上游调用只收到预期的 upstream `Authorization`，未收到员工 Key、cookie、CSRF、XFF、`Forwarded` 或
  `X-Real-IP`。员工凭据从未上送。
- 服务日志不包含管理员密码、四个员工 Key、上游凭据、普通提示、两个 background 正文、畸形来源标记或完整多 hop 链。
- 服务停止后递归扫描临时 data-dir，没有发现管理员密码、员工 Key、上游凭据或三个请求正文标记的明文。

## 源码与仓库检查

- 固定二进制完整进程脚本：PASS。
- `node --check scripts/verify-trusted-proxy-xff.mjs`：PASS。
- `go test ./internal/keypolicy ./internal/service -run
  '^(TestTrustedProxy.*|TestCrossProtocolStreamDispatchRejectsStaleKeyPolicyRevisionAndABA)$' -count=1 -timeout=5m`：PASS
  （`internal/keypolicy 0.123s`，`internal/service 7.709s`）。
- `go vet ./...`：PASS。
- 非缓存 `go test ./... -count=1 -timeout=10m` 不是 PASS：accounting、backup、egress、financial、governance、keypolicy、
  keyprovider、membership、protocolconv、recoverymaterial、scheduling 和两个 CLI 包先通过；`internal/service` 在 600 秒包上限
  到达时仍运行无关的 `TestResponsesStoredResourceOwnershipContinuationAndDelete`，因此整条命令以 timeout 失败，没有测试断言
  失败。本轮没有以部分结果或更长重跑冒充全量成功；最终全量以集成工作流结果为准。
- `web` 的 `npm test`：19 个文件、137 项 PASS；`npm run build`：PASS。依赖来自与当前 `package.json` 和
  `bun.lock` SHA-256 均完全相同的现有本机缓存，临时 junction 已移除，未安装或改写依赖锁。
- `git diff --check` 与本文相对文档链接检查：PASS。

最终精确集成 HEAD 的 Linux race、CI 与固定二进制重建仍由集成工作流复验；本页不把当前 G 分支结果代替最终 PR CI。

## 未覆盖与保留限制

- 未测试真实反向代理、TLS 终止器、公网/LAN 链、真实 IPv6 socket peer、PROXY protocol 或云厂商专有 header。IPv6 与
  mapped IPv4 的本轮结论只针对可信回环收到的 XFF 数值 hop。
- `Forwarded` 与 `X-Real-IP` 的结论是按契约忽略，不是支持；没有 employee 级“信任 header”授权开关。
- 未访问真实 OpenAI、Anthropic、Google、ChatGPT/Codex 会员或任何真实 CLI。真实 provider、会员导入和 CLI 结论均为
  **UNTESTED**，不是 PASS 或 UNSUPPORTED。
- 没有重跑 PR7/PR9/PR11 旧协议/CLI 全矩阵，也没有声称通用代理兼容、部署或生产就绪。

本轮未发现生产缺陷。

## 可复现命令

```powershell
node scripts/verify-trusted-proxy-xff.mjs `
  C:\Users\apple\.codex\tmp\cpa-cloud-pr12-202b622\cpa-cloud.exe `
  6508d7f7255d6307c45f8f2d64831896ea7d2d7718b494182411328c9e331abb
```
