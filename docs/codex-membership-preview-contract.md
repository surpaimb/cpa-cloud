# Codex 会员文件导入实验契约

状态：下一阶段实现规格，尚未提供网页导入或员工调用。2026-09-23。

沿用单 Go 服务与员工 Key，不引入第二个 HTTP 代理或官方 CLI 子进程。协议依据 `codex-direct-protocol.md` 与内部 `membership` 包。仅接受管理员主动上传的 Codex `auth.json`；不扫描本机配置。浏览器授权、自动刷新、Claude/Gemini 接入仍是后续目标。

## 开关与管理接口

- CLI `--experimental-codex-membership` 默认关闭。关闭时导入/替换/执行均返回固定 `feature_disabled`；列表仍可显示已有记录，API Key 上游不受影响。
- `GET /admin/api/v1/system/status` 增加 `features.codex_membership_import` 布尔值。网页据此显示可用入口，关闭时展示启动参数说明，不显示假授权按钮。
- `POST /admin/api/v1/upstreams/codex-import`，请求 `{name,auth_json,operation_id}`。`auth_json` 为文件完整字符串；`operation_id` 为客户端生成并在失败重试中复用的 UUID。管理员会话、CSRF、Origin、请求大小限制与其他写接口一致。解析器限制原始文件 1 MiB，外层 JSON 限 8 MiB 以容纳转义；错误不回显文件内容。
- 导入仅验证结构、account_id 和可调度的过期时间，不发真实上游请求，不将导入成功称为认证成功。响应为 upstream 对象，增加 `provider_kind: codex-membership`、`credential_state: imported_unverified`、`verified_at: null`；端点为服务端固定值，不接受客户端提供端点。禁止回传 Token、原文件、account_id、密文或 JWT 内容。
- 同一 operation_id 重试返回已有对象，不重复创建或替换凭据。其他唯一性冲突返回 409。失败写入不留下半条上游记录。
- `PUT /admin/api/v1/upstreams/{id}/codex-auth`，请求 `{expected_revision,auth_json}`；只用于该类型。成功原子替换凭据，revision 递增、状态回到 imported_unverified、verified_at 清空。失败或 revision 冲突保留旧凭据；旧在途请求不得覆盖新版本的状态。
- 原有启停 PATCH 可用于该类型；普通 `api_key` PATCH 不允许替换会员凭据。删除/供应商侧撤销流程另行设计，不将本地停用称为供应商撤销。

## 存储与状态

复用现有 AEAD 与安装根密钥，绑定上游 ID 和类型用途；原始文件只以密文落盘，employee key 摘要方案不变。账号状态为 imported_unverified、verified、reauth_required；只有真实上游请求成功完成后才可变为 verified，并记录 verified_at。本地过期或 401 变为 reauth_required；暂时限流/网络失败不伪造成过期。所有状态回写按读取凭据时的 revision 条件更新。

需要扩展现有 upstream provider_kind 约束；迁移必须事务化、可重试，保留已有上游、模型外键、员工 Key 和请求记录。失败回滚，不自动导出明文数据。测试迁移前后调用和重新打开数据库；保持旧 API Key 数据行为。

## 模型与员工请求

第一阶段会员模型手动创建路由，不伪造可用模型清单；该类型 discover-models 返回 `model_discovery_unsupported`。模型目录仅返回已配置且权限允许的路由，并继续遵守停用与撤销行为。

沿用 `/v1/chat/completions`，内部按 provider_kind 路由到 CodexDirectAdapter。仅实验性 user/assistant 纯文本与 stream 布尔子集；拒绝 system/developer、tools、图像/音频和尚未验证字段，返回 `unsupported_feature`，不能静默删掉参数。标准 API 错误不暴露上游消息或凭据；上游 401 转为明确的上游重新授权错误，不误表示员工 Key 无效。

非流式返回标准 assistant 内容；流式输出标准 Chat Completions SSE chunk 与正常完成边界。上游失败/取消不能发送伪成功 finish；已经发出 HTTP 200 时发送脱敏错误事件并结束。断连取消上游请求；按现有规则记录请求结果。未知 usage 省略或标记未知，不伪造精确 0。

## 网页与验收

上游页增加独立“导入 Codex 授权文件”流程：选择文件、名称、实验限制提示；不展示文件正文，失败重试不重复创建。会员行显示类型、状态、重新导入入口；同步模型入口提示当前需手动路由。员工仍只配置 CPA Cloud Key。

自动验收使用独立临时数据库、合成 JWT/凭据与注入假上游，覆盖加密落盘无秘密、幂等导入、替换失败保留/冲突、开关关闭、结构拒绝、迁移/重启、权限/撤销、文本/流式、401、取消和状态 revision 竞争。不能默认用真实账号联网，也不能凭假上游测试宣称真实会员已验证。

服务任务负责 cmd/internal 与服务测试，网页任务负责 web；主任务维护契约、集成测试和文档。服务与网页交付后再做端到端验收；本阶段不创建发布标签、不全平台打包。
