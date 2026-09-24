# Sub2API 功能差距复核：2026-09-24

**CPA Cloud 尚未完整对齐。预算接线、账号池、Codex 自动刷新已经进入主线，但完整会员覆盖、通用核算、运维和商业能力仍有明显缺口。** 本文按源码复核更新状态，不把有页面、有接口或有内部模块等同于全流程交付。

## 基线和证据范围

- CPA Cloud：`5532220fd79d6d8fb6cc222634296c7600431fc4`，包含已合并的预算批次；运行时代码与该批独立验收的 `ac4d820` 一致。
- Sub2API：固定 [`20a94fbb567b62208751292ed7786b24a7e7c0fe`](https://github.com/Wei-Shaw/sub2api/tree/20a94fbb567b62208751292ed7786b24a7e7c0fe)，没有把浮动主线的新功能混入基线。
- 获取未截断的 Git tree（4,345 个条目），读取并按 Git blob SHA-1 校验了 27 个路由、服务装配、后台任务、身份、计费及 schema 文件。参考源码只在仓库外的只读研究缓存中使用，没有移植到 CPA Cloud。
- CPA Cloud 检查了管理与员工路由、数据库结构、运行时调用、worker 启停、网页入口及已有验收记录。本文是定向源码审计，没有运行 Sub2API，也不声称逐行审完其全部文件；因此不提供“完成百分比”。

固定快照的入口证据见 [管理路由][s-admin]、[模型网关][s-gateway]、[身份入口][s-auth]、[用户入口][s-user]及[服务装配][s-wire-gen]。下文将入口证据与实际执行机制分开说明。

### `5532220` 后续交付增量

本节不改写上述 dated audit 在 `5532220` 的源码结论，只记录随后已经独立交付并验收的有限子集。实现代码固定在 `a5fffb31f4cad079a30fd1ead3827373847d53d3`；[Code validation 35954728428](https://github.com/surpaimb/cpa-cloud/actions/runs/35954728428) 对该 SHA 的全量 Go、Linux race、vet、双 CLI 构建和隔离进程 smoke 全部成功。验收只使用合成凭据、本地假上游、临时目录和随机端口。

| 原审计功能簇 | 后续已交付的有限子集 | 仍未对齐 |
| --- | --- | --- |
| 账号和模型日常管理 | 上游/模型 revision 与 CAS、墓碑归档、默认隐藏、ID 保留、上游凭据销毁、归档后四协议零派发；生命周期写入最多 1000 条的无内容有界审计。 | 加密导出、更完整批量管理、墓碑恢复、账号组到员工/Key 授权、模型路由完整 CRUD、全局审计查询/导出/防篡改与二次认证。 |
| 定时测试与渠道监控 | 默认关闭的固定 UTC 间隔计划，支持 CRUD/CAS、到期 claim、凭据/目录检查、每计划 200 条有界历史、取消、归档联动和重启不重放。 | cron、命名时区、通知/告警、收费生成测试，以及独立渠道监控、延迟/可用性聚合。 |
| 备份与生产恢复 | 离线 CLI 提供活动 WAL 一致快照、认证加密、只读校验和仅新目录恢复；错误密码/篡改拒绝、旧会话失效及中断任务启动恢复已验收。 | 计划备份、保留、对象存储/远程复制、密钥轮换/重封装、原地升级回滚、异机演练和生产灾备编排。 |

因此下文“尚未对齐”表中的对应行应按 `5532220` 基线阅读；本节只把这些行从“完全缺失”校正为“已有有限子集、总体仍未完成”，不把 ACCT-01、OPS-01、OPS-04 或 AUDIT-01 记为整项完成。

## 已经接线的能力，不再列为完全缺失

| 能力 | CPA Cloud 当前实现及边界 |
| --- | --- |
| 员工与 Key | 创建、命名、有效期、撤销、员工模型权限、摘要存储和鉴权；不是完整用户门户或 Key 独立权限体系。见 [路由](../internal/service/app.go)、[存储](../internal/service/store.go)。 |
| Codex 生命周期 | 文件导入、网页 OAuth、手动刷新、后台及请求前共享刷新、模型目录均已接线。仍为默认关闭的源码实验；未完成真实账号验证及供应商侧撤销。见 [刷新协调器](../internal/service/codex_refresh.go)、[OAuth](../internal/service/codex_oauth.go)。 |
| 原生协议 | Chat、Responses、Claude Messages/count_tokens、Gemini REST/SSE 与分页模型发现已有 API Key 子集；Codex Responses 支持部分 function tools 回合。不能描述为“完全没有工具”，也不能推导为完整协议兼容。 |
| 账号池与恢复 | 批量导入、分组/渠道、模型池、权重/优先级、账号容量、粘滞、租约、冷却、派发前安全换号及默认关闭的恢复探测已接线。见 [池运行时](../internal/service/account_pool_runtime.go)、[恢复协调器](../internal/service/account_recovery_coordinator.go)。 |
| 用量与治理 | 请求和尝试账本、未知用量、价格版本、查询网页、RPM/并发及固定模型硬预算预留/结算/恢复已有实现。预算范围见下一节，不能据此标为通用核算完成。 |
| 出站代理 | HTTPS CONNECT 及 API Key 四协议、目录、测试、恢复的绑定通路已有实现；Codex 全生命周期、其他代理协议和轮换仍缺。 |
| 管理审计与迁移 | 已有账号池、治理配置的事务审计，以及 SQLite 迁移、联合恢复和失败回滚测试。旧矩阵将这两项写成“待实现”过于笼统，现改为“部分实现”。 |

以上验收证据复用 [集成记录](integration-status.md)和[预算集成进度](budget-service-integration-progress.md)。合成上游通过不等于真实供应商通过，源码主线也不等于 preview.3 下载包包含这些功能。

## 尚未对齐的功能与机制

| 功能簇 | 还缺什么 | 源码依据与实际影响 |
| --- | --- | --- |
| 三家会员与更多提供商 | Claude、Gemini 会员导入/授权/刷新/调用未完成；Codex 真实兼容与供应商撤销待验收；Grok、Antigravity、Vertex 等专用接入未完成。 | CPA [provider 存储类型](../internal/service/store.go)只有四类。参考 [管理路由][s-admin]存在其他提供商入口；接入条件仍按[逐提供商记录](research/membership-provider-readiness.md)处理，不能用 API Key 通路替代会员需求。 |
| 通用预算和额度窗口 | 多模型、会员、SSE、工具、媒体预算；更实用的输入估算/预留策略；余额及 5 小时/1 日/7 日额度窗口。 | CPA [固定证明](../internal/accounting/bound_profile.go)只覆盖官方 `gpt-4.1-2025-04-14` 文本非流式，输入预留上界为 **1,047,576 token**、输出最多 32,768；短请求也可能被较小 TPM 策略拒绝。Sub2API [Key 模型][s-key]和[计费服务][s-billing]另有额度窗口、余额、订阅与缓存协调。CPA 当前预算安全闭环已接通，但适用性和计费机制尚未对齐。 |
| 供应商配额与调度 | 上游真实余额/剩余额度、重置时刻、配额感知选路、完整会话上限与可配置错误策略。 | 当前 [池运行时](../internal/service/account_pool_runtime.go)按可用性、优先级/权重、容量和固定冷却选路。参考 [余额检测服务][s-balance]及[错误规则运行时][s-errors]有独立机制。必须保留请求不确定或开始输出后不重放的安全边界，不能为了“对齐”任意换号。 |
| 账号和模型日常管理 | 账号删除、加密导出、更完整批量管理；模型路由编辑/停用/删除；账号组与员工/Key 的授权绑定和费率。 | CPA [管理路由](../internal/service/app.go)中的模型只有 GET/POST，[模型页](../web/src/pages/ModelsPage.tsx)已有创建和池编辑，但不是完整路由 CRUD。参考 [管理路由][s-admin]的账号、分组与用户操作更完整。 |
| Key 细粒度权限 | 独立 Key 的协议、模型/账号组、IP 白名单/黑名单，以及完整额度策略。 | CPA [access_keys/employee 结构](../internal/service/store.go)仍以员工模型权限为主；Sub2API [api_key.go][s-key]具有 GroupID、IP 规则、额度和多个金额窗口。已有 Key 撤销、有效期和治理限制不能替代这些能力。 |
| 协议与扩展模型接口 | Embeddings、WebSocket/Realtime、完整协议转换和能力协商；Responses 状态/后台执行属于已确认的后续范围；托管工具、媒体仍缺。 | CPA [Responses 校验](../internal/service/responses.go)明确拒绝 background/store 和 previous_response_id/conversation，网关无 Embeddings/WS 入口。参考 [gateway.go][s-gateway]明确接线 Embeddings、Responses WebSocket、Live、图片与其他媒体。这里不把参考某个路由的存在当成其所有字段、状态资源都已正确实现的证明。 |
| 完整代理能力 | HTTP/SOCKS、代理池轮换、出口 IP/延迟检查、Codex 授权/刷新/目录/生成统一出口。 | CPA [代理管理](../internal/service/outbound_proxy_admin.go)、[派发](../internal/service/outbound_proxy_dispatch.go)只是 HTTPS CONNECT 子集；参考 [管理入口][s-admin]有代理测试/质量检查等操作。新增出口不能绕过现有 SSRF、TLS、凭据隔离。 |
| 定时测试与渠道监控 | 持久化测试计划、时区与下次执行时间、历史保留；独立渠道监控计划、可用性/延迟历史及告警。 | CPA 已有手动目录/凭据测试和故障后恢复 worker，仍没有通用计划任务。Sub2API [测试 runner][s-scheduled]实际扫描到期计划、执行、保存结果和计算下次时间；[渠道 runner][s-monitor]启动加载启用项并按配置调度。CPA 的“渠道标签”不能算渠道监控。 |
| 全局审计与观测 | 管理操作统一覆盖、审计查询/导出/保留、敏感操作二次验证；实时监控、告警规则、静默、恢复通知。 | CPA 的 [account_pool_audit](../internal/service/account_pool_config.go)和 [governance_management_audit](../internal/service/governance_management.go)只是局部事务审计。参考 [审计服务][s-audit]有写队列/查询/保留 worker，[告警服务][s-alert]有启动循环，管理路由实际接入审计与二次验证。 |
| 身份与用户门户 | 可选注册、验证/找回密码、用户自助 Key/用量门户；多角色权限、OIDC/第三方登录、TOTP、Passkey、敏感操作再次认证。 | CPA [网页入口](../web/src/App.tsx)只有七类管理员页面，[管理员认证](../internal/service/admin_auth.go)已有基础安全会话。参考 [身份路由][s-auth]、[Passkey 服务][s-passkey]、[OIDC handler][s-oidc]具有实际流程，不能只补几个登录按钮。 |
| 备份与生产恢复 | 计划备份、加密备份包、恢复校验/演练、主密钥轮换与灾难恢复、升级回滚；数据保留与清理。 | CPA 有迁移回滚测试和预览安装包，尚无上述运维闭环。参考 [备份服务][s-backup]启动 cron、恢复中断记录、加载保存的计划；[清理服务][s-cleanup]负责定时清理。参考能备份不自动证明备份正文已加密；CPA 仍按自己的加密要求实现。 |
| 商业与规模扩展 | 套餐/订阅、余额/充值/兑换、支付/退款、到期/邮件通知、推广分佣；PostgreSQL/Redis/对象存储、媒体异步任务和插件。 | CPA 当前没有金额/订单/订阅状态机，也没有相应存储适配器。[参考用户入口][s-user]、[支付入口][s-payment]、[订阅到期 worker][s-subscription]和[订单到期 worker][s-order]有接线；不能只用用量成本表或 API 入口顶替商业闭环。商业功能继续默认关闭。 |

## 后台生命周期专项结论

Codex 刷新是**已经补上**的部分：CPA 在 App 启动时启动共享协调器，后台扫描、手动操作和请求前获取凭据共用协调逻辑；仍保留 client 来源绑定、账号锁、锁内重读、revision 条件更新及不确定刷新暂停。参考 [wire.go][s-wire]同样有 token refresh worker 与请求侧 provider 的共同装配。这里应验收边界和真实兼容，不应重新派发一个“从零实现自动刷新”的重复任务。

仍需补的是定时测试、渠道监控、告警、审计保留、备份计划、订阅与订单到期、通知及统计/数据清理的生命周期。每个交付项要同时检查：**管理入口 → 执行逻辑 → 持久化 → 启动/停止/重启 → 验收证据**。只存在 service 文件或开关不算接通。CPA 已有的租约回收、预算联合恢复和失败账号恢复也不应被误记为完全没有后台机制。

## 范围校正及下一步顺序

正文审计按用户决定明确排除，只记录无内容调用元数据。敏感错误正文不直接透传，不能照搬参考的任意正文规则。

严格组织级多租户继续保留为用户已确认的 CPA Cloud 扩展目标；本次核实的参考身份/分组 schema 和树清单不足以证明它提供了完整租户隔离，不能把“多用户+分组”直接写成“Sub2API 已有严格多租户”。扩展能力与已证实的参考差距应分别验收。

建议下一批先完善 **Key 权限与通用预算、供应商额度/重置感知**，同时补 **账号/路由完整管理、定时测试/渠道监控及统一管理审计**；随后推进备份、密钥恢复与生产运维。会员仍为核心主线，分别解决接入条件和真实兼容验证，不能由商业页面替代。商业、媒体和扩展存储留在总目标中按依赖继续交付。

本次只更新核查文档及[编号矩阵](feature-parity-plan.md)，没有实现表中缺失功能、启动新子任务或发布安装包。文档变更只检查链接和 diff，已有测试证据不重复执行或冒充本次新验收。

[s-admin]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/server/routes/admin.go
[s-gateway]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/server/routes/gateway.go#L215
[s-auth]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/server/routes/auth.go
[s-user]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/server/routes/user.go
[s-payment]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/server/routes/payment.go
[s-wire-gen]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/cmd/server/wire_gen.go
[s-wire]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/wire.go#L120
[s-key]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/api_key.go#L30
[s-billing]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/billing_service.go#L63
[s-balance]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/cn_provider_balance_check_service.go
[s-errors]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/error_passthrough_runtime.go#L31
[s-scheduled]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/scheduled_test_runner_service.go#L46
[s-monitor]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/channel_monitor_runner.go#L117
[s-audit]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/audit_log_service.go#L52
[s-alert]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/ops_alert_evaluator_service.go#L86
[s-passkey]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/passkey.go#L168
[s-oidc]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/handler/auth_oidc_oauth.go#L117
[s-backup]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/backup_service.go#L229
[s-cleanup]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/ops_cleanup_service.go#L92
[s-subscription]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/subscription_expiry_service.go#L74
[s-order]: https://github.com/Wei-Shaw/sub2api/blob/20a94fbb567b62208751292ed7786b24a7e7c0fe/backend/internal/service/payment_order_expiry_service.go#L57
