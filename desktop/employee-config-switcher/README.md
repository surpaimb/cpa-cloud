# CPA 云端配置（开发预览）

这是 CPA Cloud 员工侧原生配置客户端的独立 Tauri 2 工作区。当前阶段提供：

- 仅 `CPA 地址` 与 `员工 Key` 两个输入；
- 本机十工具候选检测；
- 通过 `GET /v1/client-configuration` 读取当前 Key 真正可见的协议模型；
- OpenCode JSONC 保留式配置计划、compare-before-commit 摘要和 provider 所有权冲突拒绝；
- OpenCode、Claude Code、Gemini CLI、Codex、Grok Build、Hermes、OpenClaw 与 Pi 的隔离合成目录 create-only 事务测试；Gemini CLI 使用固定版启动校验接受的 `gemini-api-key` 选择配合自定义网关地址，Claude Code、Gemini CLI、Codex、Grok Build、Hermes 与 OpenClaw 会选择服务端默认模型；Grok Build 把当前员工 Key 可见的 Responses 模型注册到普通聊天模型选择器，并用 `allowed_models` 限制该选择器，但这不会约束其内部 `web_search`、图像、subagent/fork 或 fleet 模型路径；Hermes 注册固定 `chat_completions` 的具名自定义 provider，关闭运行时 `/models` 探测，并保持 `.env`、`auth.json` 与 `SOUL.md` 不变；Claude Code 不改 `~/.claude.json`，Gemini CLI 不改 `trustedFolders.json`，Codex/Grok Build/Pi 不改各自的 `auth.json`，Pi 也不改 `settings.json` 或默认模型；
- `detect_clients`、`discover_configuration`、`apply_configuration` 三条窄 IPC。

`apply_configuration` 当前故意失败关闭，不写真实配置。现有 create-only 引擎只服务于隔离合成根目录；既有配置替换仍需加密备份、handle-relative 路径约束、逐工具恢复与固定客户端验收后才会启用。

Grok Build 的真实检测或写入还必须先支持或明确拒绝 `GROK_HOME` 覆盖，证明模型 ID 满足 CPA `validIdentifier` 约束或能安全处理 Grok glob 语义，并增加固定原生客户端夹具；在这些门槛完成前，不声称覆盖 Grok 的内部模型选择路径。

## 本地验证

```text
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test
pnpm build
cargo test --locked -p employee-config-core
```

完整 Tauri 构建需要 Rust 1.90+ 和各平台原生依赖。本仓库不复用 CC Switch 的产品标识、更新渠道、数据库、代理或品牌资产。
