# 功能对齐执行批次

2026-09-23，参考功能基准固定为 Sub2API `20a94fbb567b62208751292ed7786b24a7e7c0fe`。实现来自 CPA Cloud 自有规格、公开协议和独立测试，参考代码不作为移植模板。完整范围沿用 [功能矩阵](feature-parity-plan.md)。

用户明确选择只保留请求元数据审计。提示词和响应正文审计不对齐，不保存这些正文，也不通过“调试日志”绕过这一约束。注册、多租户、商业计费等其他目标仍保留，默认内部企业模式关闭相应入口。

## 批次一：会员生命周期和原生协议

当前集成分支 `codex/parity-integration`：

- 保留主线 Codex OAuth、Chat 和 Responses；整合 Anthropic Messages/count_tokens 与 Gemini v1beta 的 API Key 分支，统一上游类型约束和员工 Key 验证。
- Claude SSE 必须完整读取有界事件再判断并转发，不能原样转发上游错误正文。Gemini 发现必须完整读取有界分页，失败不能伪造完整列表。
- Codex 自动刷新和请求侧刷新共用一个进程内服务，账号互斥、锁内重读、revision 条件保存。轮换前记录持久化状态，轮换结果不确定或进程中断时暂停后续刷新；原有效 access token 可用至过期。重新授权或重导入解除旧凭据的暂停。
- OAuth 会话创建返回 `session_id`；新增同管理员登录会话限定的 `GET /admin/api/v1/upstreams/codex-oauth-sessions/{id}`，返回 `{session_id,status,expires_at,upstream_id?,error_code?}`，状态为 pending/exchanging/succeeded/failed/cancelled/expired。状态接口不返回授权 URL、state、code、token 或账号标识。
- 上游视图增加可选 `oauth_refresh: {eligible,state,reason_code?}`，state 为 ready/refreshing/paused/reauth_required/unavailable；系统状态增加 `features.codex_membership_auto_refresh`。原客户端可忽略新增字段，网页遇到旧服务器缺字段时不假定功能可用。
- 网页提供授权、轮询、手动刷新、失败恢复、Gemini 原生预设和会员模型同步。候选目录不自动创建路由或扩大员工权限。
- Codex 模型目录采用固定官方客户端观察协议，记录协议版本、实际字段和限额。Claude、Google 会员接入与 API Key 支持分别记录；未具备的会员路径不能被原生 API Key 路由替代。

每项必须分开记录“提交存在”“已集成”“模拟验收”“真实供应商验证”。本批只用合成凭据与隔离测试数据，不读取真实账号。

## 后续依赖

1. 账号池：批量导入/逐项结果、分组/渠道/多账号路由，权限与能力筛选、会话绑定、冷却、优先级和权重、容量租约与恢复。先交付调度核心，再接持久化和真实执行路径；独立核心测试不能算账号池上线。
2. 治理运维：员工请求与上游尝试双层幂等账本、token/缓存/版本化成本、预算/RPM/TPM、代理池、管理审计、监控告警、定时测试、渠道监控、错误配置、加密备份和恢复/升级回滚。
3. 扩展能力：租户隔离和金额账本先于注册/套餐/订阅/充值/支付；继续媒体、Realtime、托管工具、其他提供商、插件与可选扩展存储。

仅明确安全且尚未输出的请求可按策略重试或换号，不重放执行结果不确定的请求；流式输出开始后不换号。计数区分请求和尝试，未知用量保留为空。

## 验收与发布

主任务负责共享路由、迁移和跨模块验收；GPT-5.6 Sol 任务按独立工作树或文件所有权开发。相关测试、Linux race、静态检查、网页检查、真实进程与模拟上游验收通过后再集成。新增后台服务必须验证启动、关闭、取消、重启恢复，不以存在类或接口代替已接线。

普通提交只运行轻量 CI，本批不创建 tag 或安装包。下载版 preview.3 与源码实验能力分开说明；其他待办不因一批通过而标记完成。
