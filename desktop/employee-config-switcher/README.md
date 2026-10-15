# CPA 云端配置（开发预览）

这是 CPA Cloud 员工侧原生配置客户端的独立 Tauri 2 工作区。当前阶段提供：

- 仅 `CPA 地址` 与 `员工 Key` 两个输入；
- 本机十工具候选检测；Claude Desktop 在 Windows 只按官方
  `%APPDATA%\Claude\claude_desktop_config.json` 文件检测，旧的
  `%LOCALAPPDATA%\Claude[-3p]` 目录不再产生误报；
- 通过 `GET /v1/client-configuration` 读取当前 Key 真正可见的协议模型；
- OpenCode JSONC 保留式配置计划、compare-before-commit 摘要和 provider 所有权冲突拒绝；
- OpenCode、Claude Code、Gemini CLI、Codex、Grok Build、Hermes、MiniMax Code、OpenClaw 与 Pi 的隔离合成目录 create-only 事务测试；Gemini CLI 使用固定版启动校验接受的 `gemini-api-key` 选择配合自定义网关地址，并已用固定 `@google/gemini-cli@0.62.0` 在隔离 Windows 主目录和合成回环服务上验收规划器原始字节、v1beta 路径及 `x-goog-api-key`；`GEMINI_CLI_HOME` 按用户主目录根处理并追加 `.gemini`。Claude Code、Gemini CLI、Codex、Grok Build、Hermes、MiniMax Code 与 OpenClaw 会选择服务端默认模型；Grok Build 把当前员工 Key 可见的 Responses 模型注册到普通聊天模型选择器，并用 `allowed_models` 限制该选择器，但这不会约束其内部 `web_search`、图像、subagent/fork 或 fleet 模型路径；Hermes 注册固定 `chat_completions` 的具名自定义 provider，关闭运行时 `/models` 探测，并保持 `.env`、`auth.json` 与 `SOUL.md` 不变；MiniMax Code 注册固定 `openai-responses` 的 `custom_provider`，对任何已有或无法安全检查的 `config.yaml.lock` 条目都失败关闭，并保持登录、MCP、提示词与会话状态不变；这仍只是预检，真实写入继续等待兼容的原子锁获取与完整数据目录解析；Claude Code 不改 `~/.claude.json`，Gemini CLI 不改 `trustedFolders.json`，Codex/Grok Build/Pi 不改各自的 `auth.json`，Pi 也不改 `settings.json` 或默认模型；
- `detect_clients`、`discover_configuration`、`apply_configuration` 三条窄 IPC。

`apply_configuration` 当前故意失败关闭，不写真实配置。现有 create-only 引擎只服务于隔离合成根目录；既有配置替换仍需加密备份、handle-relative 路径约束、逐工具恢复与固定客户端验收后才会启用。

Claude Desktop 在本预览中明确只检测、不可配置。公开的
`claude_desktop_config.json` 合同用于 `mcpServers` 本地工具接入，官方远程
connectors 也用于 MCP 工具与数据；两者都不是可替换 Claude 模型端点或注入
员工模型 Key 的公共接口。是否仍把 Claude Desktop 纳入首版可配置目标，需要用户
在这一限制下决定；在有明确决定和新的公开协议证据前，它保持失败关闭，不能声称
模型路由可用。其余工具继续按各自固定版本和安全事务门槛推进。

首版本地威胁模型信任同一登录系统账号下的进程；恶意同账号进程读取工具配置中的员工 Key、绕过 advisory lock 或主动竞态替换目录项明确不在首版范围。跨账号访问、意外并发修改、崩溃恢复与对象身份变化仍在范围内，必须保留 CAS/稳定路径与权限、兼容锁、原子替换、加密备份/回滚、OS Key provider 与 Key 生命周期，以及逐工具原生验收。这一范围选择不启用 `apply_configuration`，也不代表十个工具已可真实写入。

Linux 合成根目录的 completion tombstone 现在用同一稳定目录句柄和兼容 advisory lock 覆盖枚举、容量/重复校验、发布、逻辑删除与 inactive-proof 验证，并用确定性并发测试覆盖第 64 条边界及发布/删除竞态。这只是后续 artifact retirement 的并发安全前置条件；跨命名空间退休与真实配置替换仍未实现。

Windows 合成根目录现在有一个独立的当前用户 DPAPI recovery-key provider。它使用无 UI、非 machine-scope 的 DPAPI，磁盘只保存带应用层完整性 envelope 的受保护 blob，provider reference 绑定 transaction 与 blob 摘要；owner-only protected DACL、稳定目录句柄、跨实例排他锁、create-only 发布、重启 reopen、删除和目录 flush 均失败关闭。Linux/macOS 在构造 provider 时明确返回 `UnsupportedPlatform`，没有环境变量、命令行、数据库、网络或明文文件回退。该 provider 尚未接入真实 replacement transaction，`apply_configuration` 仍保持失败关闭。

macOS 另有一个失败关闭的 Keychain 边界和纯内存 `SecItem` 合同测试：合同固定 Data Protection Keychain、非同步、专用 access group、`WhenUnlockedThisDeviceOnly`、无交互、persistent reference、transaction/current-user/access-group 绑定以及 exact-reference 删除语义。当前应用尚未固定 signing team、entitlement 和 access group，因此生产构造在查询 Keychain 前返回 unavailable；临时 file-based Keychain 不能替代正式 Data Protection Keychain 的重启、锁定、跨用户或持久性证据。此切片不访问真实 Keychain，也不启用真实配置写入。

Codex CLI 已用官方固定版 `0.160.1` Windows x64 二进制验收当前规划器的原始
`config.toml`：隔离 `CODEX_HOME` 中的 provider 级
`experimental_bearer_token` 被发送到合成回环 `/v1/responses`，模型选择匹配，
已有 `auth.json` 的字节、文件标识、长度和时间戳均未改变；合成员工 Key 只出现在
配置夹具，不出现在 Codex 状态数据库。官方 schema 明确不推荐直接 bearer 字段，
且该证据仅覆盖此版本/平台和故意失败的 HTTP 400 请求，因此真实写入和跨版本支持
继续保持关闭。

Grok Build 的只读检测现在复现固定官方提交的 `GROK_HOME` 非空覆盖优先级，
否则使用 `~/.grok`；环境值保持原样，不由 CPA 擅自规范化。真实写入仍必须证明
模型 ID 满足 CPA `validIdentifier` 约束或能安全处理 Grok glob 语义，并增加
固定原生客户端夹具；在这些门槛完成前，不声称覆盖 Grok 的内部模型选择路径。

## 本地验证

```text
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test
pnpm build
cargo test --locked -p employee-config-core
```

完整 Tauri 构建需要 Rust 1.90+ 和各平台原生依赖。本仓库不复用 CC Switch 的产品标识、更新渠道、数据库、代理或品牌资产。
