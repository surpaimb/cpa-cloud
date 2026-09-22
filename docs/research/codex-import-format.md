# Codex `auth.json` 导入格式与实现边界

状态：已实现结构解析核心；未实现加密存储、刷新、在线验证或会员模型请求

查阅日期：2026-09-22

本实现仅依据 OpenAI 官方文档与 `openai/codex` 官方仓库。没有读取操作者的 `~/.codex`、系统凭据库或任何真实 Token，也没有查阅参考产品或归档 CPA 源码。测试数据全部为本项目自行构造的假字符串。

## 已实现的输入边界

入口 `membership.ParseCodexAuthJSON([]byte)` 只解析调用方明确传入的字节，不解析路径，也不读取 `HOME`、`CODEX_HOME` 或当前用户配置。文件上限为 1 MiB；无效 UTF-8、无效 JSON、重复 JSON key、多值 JSON、非对象顶层和已知字段类型错误均被拒绝。

明确支持的候选格式为 ChatGPT OAuth 缓存：

```json
{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "access_token": "<secret>",
    "refresh_token": "<secret>",
    "id_token": "<optional-unverified-secret>",
    "account_id": "<optional-unverified-identifier>"
  },
  "last_refresh": "<optional-provider-value>"
}
```

`access_token` 和 `refresh_token` 必须为非空字符串。`id_token` 与 `account_id` 若存在必须为字符串，但当前解析器不验证 JWT 签名、claims、受众、过期时间、账号或套餐。未知字段原样保留在完整文件副本中，供后续加密封存；解析器不会根据未知字段推导权限。

官方 Codex 的兼容解析在 `auth_mode` 缺失且 `OPENAI_API_KEY` 缺失或为 `null` 时按 ChatGPT 模式处理，因此本实现接受这一旧式形态。以下输入被明确区分：

| 情况 | 固定错误码 |
| --- | --- |
| 超过 1 MiB | `codex_auth_too_large` |
| 非法/重复 key JSON 或非法 UTF-8 | `codex_auth_invalid_json` |
| JSON 类型或结构不符 | `codex_auth_invalid_type` |
| `auth_mode="apikey"`，或无 mode 但存在 `OPENAI_API_KEY` | `codex_api_key_not_membership` |
| 其他认证 mode | `codex_auth_mode_unsupported` |
| 缺少或空 `access_token` | `codex_auth_missing_access_token` |
| 缺少或空 `refresh_token` | `codex_auth_missing_refresh_token` |

错误文案固定且不包含解析位置、字段值、Token 或原始 JSON。

## 成功结果不等于认证成功

成功只返回 `CodexAuthCredential` 候选对象，其状态恒为 `requires_online_verification`。对象不提供 `authenticated=true` 一类状态，也不解析未验证 JWT 来推断套餐、邮箱、workspace 或账号有效性。

敏感字段均为私有字段；默认 JSON、文本、`fmt` 与 `slog` 输出只包含：

```json
{"credential_kind":"codex_chatgpt_oauth","verification_state":"requires_online_verification"}
```

显式命名为 `*Secret` 的方法才会复制秘密给未来的加密存储或验证层。`Destroy` 会清除对象仍持有的 byte buffer，但无法清除调用方输入、已返回副本或 JSON 解码过程的临时分配，因此它不是安全内存保证。

## 官方依据

以下均为 OpenAI 官方来源，查阅于 2026-09-22：

1. [Codex authentication](https://learn.chatgpt.com/docs/auth) 说明 Codex 可把登录缓存存储在 `$CODEX_HOME/auth.json`，文件包含访问 Token，应像密码一样保护；ChatGPT 登录会在使用期间刷新 Token；官方也描述了把完整缓存复制到可信无头环境的回退流程。
2. [`openai/codex` `storage.rs`](https://github.com/openai/codex/blob/main/codex-rs/login/src/auth/storage.rs) 定义官方 `AuthDotJson`，包括 `auth_mode`、重命名为 `OPENAI_API_KEY` 的 API Key 字段、`tokens` 和 `last_refresh`，并显示文件存储读写完整 JSON。
3. [`openai/codex` `token_data.rs`](https://github.com/openai/codex/blob/main/codex-rs/login/src/token_data.rs) 定义 Token 数据中的 `access_token`、`refresh_token`、`id_token` 与可选 `account_id`。该实现会解析 JWT claims，但本项目没有复制这一行为，因为本地解析不能证明签名或在线账号状态。
4. [`openai/codex` protocol `auth.rs`](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/auth.rs) 区分 `chatgpt`、`apikey`、外部 ChatGPT Token、header、Agent Identity、Personal Access Token 和 Bedrock 等认证模式。本阶段只接受受管的 `chatgpt` 文件模式。
5. [`openai/codex` local ChatGPT auth tests](https://github.com/openai/codex/blob/main/codex-rs/tui/src/local_chatgpt_auth.rs) 明确拒绝 API Key auth 作为本地 ChatGPT 登录，并从 Token 数据读取 access token 与 account id。它是文件分类证据，不是 CPA 可直接调用会员 HTTP 后端的合同。

官方仓库 `main` 会变化；实现测试固定的是上述公开结构所需的最小字段，不宣称复制或跟随所有 Codex 内部认证模式。

## 单 Go 进程直接会员请求仍缺的公开证据

解析出 Token 不代表 CPA 已获得使用它发起模型请求的公开合同。若下一步仍要求“单个 Go 服务进程直接执行会员请求”，至少还缺：

1. 面向 CPA 这类第三方内部网关的 OAuth client 注册、允许的 redirect URI、scope、账号类型和授权授予合同；不能借用 Codex、桌面端或其他产品的 OAuth client。
2. ChatGPT 会员 Token 可由第三方 Go 服务调用的公开模型 HTTP endpoint、Authorization/account headers、请求/响应与流式 schema、模型目录和版本稳定性承诺。
3. 面向第三方托管的 refresh grant、Token rotation/reuse、并发副本、撤销、重新授权及 workspace 切换合同。
4. access/id Token 的公开 audience、issuer、签名校验与账号绑定规则，以及在线验证成功的权威响应；本地 JWT claims 不能替代这些证据。
5. 多员工经一个会员身份访问时的允许范围、额度/限流归属、审计与服务条款边界。

官方 Codex 仓库公开其自身实现细节，不等于 OpenAI 向第三方注册或授权了相同 OAuth client 与内部 HTTP 调用。现阶段不得从这些实现细节推导直连能力，也不得引入 Runner 或其他产品的 OAuth client 来绕过证据缺口。

## 后续实现顺序

1. 用现有安装主密钥设计独立 AEAD 信封，把完整原始文件加密持久化；读取 API 永不回显密文或原文。
2. 取得上节缺失的官方公开合同后，实现独立的在线验证与刷新状态机；验证前状态始终为 `requires_online_verification`。
3. 完成标准 API 请求、流式、取消、错误、模型目录、刷新轮换与供应商撤销的单 Go 闭环测试后，才可把账号标记为可用。

这三个步骤未完成前，CPA Cloud 不能宣称 ChatGPT/Codex 会员功能完整可用。
