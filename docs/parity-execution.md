# 功能对齐执行批次

2026-09-23，参考功能基准固定为 Sub2API `20a94fbb567b62208751292ed7786b24a7e7c0fe`。实现来自 CPA Cloud 自有规格、公开协议和独立测试，参考代码不作为移植模板。完整范围沿用 [功能矩阵](feature-parity-plan.md)。

用户明确选择只保留请求元数据审计。提示词和响应正文审计不对齐，不保存这些正文，也不通过“调试日志”绕过这一约束。普通自助注册、员工门户和可选商业计费仍保留，默认内部企业模式关闭相应入口。严格多租户、SSO、租户域名、管理员密码重置命令以及提示词/响应正文审计已从本实现范围排除，不再作为这些目标的前置条件。

## 批次一：会员生命周期和原生协议

本批已合入 `main`，2026-09-25 基线为 `481f8b788aba2b2f159808b2ddabe8f09eaa526a`：

- 保留主线 Codex OAuth、Chat 和 Responses；整合 Anthropic Messages/count_tokens 与 Gemini v1beta 的 API Key 分支，统一上游类型约束和员工 Key 验证。
- Claude SSE 必须完整读取有界事件再判断并转发，不能原样转发上游错误正文。Gemini 发现必须完整读取有界分页，失败不能伪造完整列表。
- Codex 自动刷新和请求侧刷新共用一个进程内服务，账号互斥、锁内重读、revision 条件保存。轮换前记录持久化状态，轮换结果不确定或进程中断时暂停后续刷新；原有效 access token 可用至过期。重新授权或重导入解除旧凭据的暂停。
- OAuth 会话创建返回 `session_id`；新增同管理员登录会话限定的 `GET /admin/api/v1/upstreams/codex-oauth-sessions/{id}`，返回 `{session_id,status,expires_at,upstream_id?,error_code?}`，状态为 pending/exchanging/succeeded/failed/cancelled/expired。状态接口不返回授权 URL、state、code、token 或账号标识。
- 上游视图增加可选 `oauth_refresh: {eligible,state,reason_code?}`，state 为 ready/refreshing/paused/reauth_required/unavailable；系统状态增加 `features.codex_membership_auto_refresh`。原客户端可忽略新增字段，网页遇到旧服务器缺字段时不假定功能可用。
- 网页提供授权、轮询、手动刷新、失败恢复、Gemini 原生预设和会员模型同步。候选目录不自动创建路由或扩大员工权限。
- Codex 模型目录采用固定官方客户端观察协议，记录协议版本、实际字段和限额。Claude、Google 会员接入与 API Key 支持分别记录；未具备的会员路径不能被原生 API Key 路由替代。

每项必须分开记录“提交存在”“已集成”“模拟验收”“真实供应商验证”。本批只用合成凭据与隔离测试数据，不读取真实账号。

## 2026-09-25 下一执行批次

账号池、请求/尝试账本、版本化成本、固定模型预算、余额与日/月结算首批、管理计费网页、加密备份恢复材料，以及 Chat↔Responses、Messages↔Responses、Gemini↔Responses 六个非流式显式 wire 方向均已进入上述主线基线。它们各自的限制仍以[集成状态](integration-status.md)为准；模拟验收不等于真实供应商、真实第二环境恢复或生产支付验收。

本轮只并行推进两项，完整字段、事务点和文件所有权见[流式与 Key 策略批次契约](stream-key-policy-batch-contract-2026-09-25.md)：

1. 将已有 Chat↔Responses 文本/function SSE 状态机接到显式 wire 的真实执行链，严格限制读取、提交、取消和终止语义。Messages/Gemini 跨协议 SSE、媒体、托管工具以及有状态/后台转换不在本轮范围。
2. 交付 KEY-02 首批：每 Key 客户端协议策略与公开模型 allowlist、CAS 管理接口和管理网页。最终可用集合是员工策略、Key 策略与当前全局有效路由的交集；IP/CIDR、组策略、通用 TPM 上界、自助门户及真实支付仍属后续工作。

仅明确安全且尚未输出的请求可按策略重试或换号，不重放执行结果不确定的请求；流式输出开始后不换号。计数区分请求和尝试，未知用量保留为空。

## 验收与发布

主任务负责共享路由、迁移和跨模块验收；协作任务按独立工作树或文件所有权开发。相关测试、Linux race、静态检查、网页检查、真实进程与模拟上游验收通过后再集成。新增后台服务必须验证启动、关闭、取消、重启恢复，不以存在类或接口代替已接线。

普通提交只运行轻量 CI，本批不创建 tag 或安装包。下载版 preview.3 与源码实验能力分开说明；其他待办不因一批通过而标记完成。
