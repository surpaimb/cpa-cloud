# CPA 云端配置（开发预览）

这是 CPA Cloud 员工侧原生配置客户端的独立 Tauri 2 工作区。当前阶段提供：

- 仅 `CPA 地址` 与 `员工 Key` 两个输入；
- 本机十工具候选检测；
- 通过 `GET /v1/client-configuration` 读取当前 Key 真正可见的协议模型；
- OpenCode JSONC 保留式配置计划、compare-before-commit 摘要和 provider 所有权冲突拒绝；
- `detect_clients`、`discover_configuration`、`apply_configuration` 三条窄 IPC。

`apply_configuration` 当前故意失败关闭，不写真实配置。Windows 私有 DACL、持久 journal、发布后验证、并发安全回滚和启动恢复全部通过测试与安全复核后，才会启用写盘。

## 本地验证

```text
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test
pnpm build
cargo test -p employee-config-core
```

完整 Tauri 构建需要 Rust 1.85+ 和各平台原生依赖。本仓库不复用 CC Switch 的产品标识、更新渠道、数据库、代理或品牌资产。
