# 独立渠道监控首段契约（OBS-02）

状态：2026-09-30 开发预览实现契约；本文先冻结范围与验收，实际通过项另记。实现依据本项目的[账号池渠道契约](account-pool-service-contract.md)、[上游测试契约](upstream-health-contract.md)和独立编写的测试，不参考 CLIProxyAPI、Sub2API 或归档 CPA 代码。此段没有新供应商协议实现或第三方依赖；目录请求只复用现有受限 `catalog` runner。公开目录协议依据为 [OpenAI List Models](https://platform.openai.com/docs/api-reference/models/list)、[Anthropic List Models](https://platform.claude.com/docs/en/api/models/list) 和 [Gemini models.list](https://ai.google.dev/api/models)（2026-09-30 查阅）。这些资料只描述供应商目录接口，本项目的监控调度和结果语义是自定规则。

## 范围与非目标

一个计划仅绑定**现有** `account_channels.id`、一个公开 `model_id` 和该模型显式账号池中的一个 `upstream_id`。现有数据库没有单独的 route ID，因此路由身份是 `(model_id, upstream_id)` 加冻结的账号池配置 revision 和路由字段；不从渠道任选账号，不使用旧单路由的隐式兼容项，也不因路由变化自动换号。创建时模型、渠道、账号必须存在且未归档，模型与账号启用，显式 route 的 `channel_id` 必须正好等于计划渠道。计划不创建或改写渠道、账号池、模型、上游及员工权限。

只允许 `local_credential` 和 `catalog`。前者只证明本机可解密并解析凭据，零网络；后者只证明既有安全客户端在有界超时内读到供应商模型目录。固定结果码沿用[上游测试契约](upstream-health-contract.md)，并包含 `configuration_changed`、`stale`、`cancelled`、`interrupted`、`test_in_progress`、`capacity_exceeded`、`storage_unavailable` 等已定义的调度结果。成功 `catalog_ok` **不**证明该路由模型可生成、账号整体健康、实时渠道健康、供应商配额或聚合可用率；页面和 API 不使用这些词推断新事实。不得发送生成请求，改变账号 `enabled`/cooldown/recovery 状态，或引入新 URL、脚本、头、请求正文、定时任务插件。告警、通知、跨账号聚合可用率、供应商健康判断和通用 cron 另批。

`--channel-monitors-enabled` 默认关闭。关闭时管理员仍可管理计划与读历史，但不启动监控 worker、不 claim、不新建运行、不访问上游；迁移只做显式的启动结构检查/DDL，不代表 worker 自动开启。开启后一个服务进程最多两个监控执行，同一账号最多一个；调用既有上游测试协调器时还受其全局/账号并发上限约束。与其它检查竞争时用固定 `test_in_progress` 或 `capacity_exceeded`，不绕过协调器、不换 operation ID 重试。每次执行最长十秒，并受服务关闭和配置变化取消。

## 管理 API、网页与兼容

管理员会话与既有 `requireAdmin` 分权不变；所有写入严格验证同源 Origin、CSRF、重复/未知 JSON 键、类型与大小。员工 Bearer Key 不能调用。新增：

- `GET/POST /admin/api/v1/channel-monitors`：列表与创建。创建体严格为 `{name,channel_id,model_id,upstream_id,scope,interval_seconds,enabled}`；名称 1–120 字符，固定 UTC 间隔 300–86400 秒。创建事务捕获当前渠道、模型、显式池、路由和账号版本及必要路由字段；禁用计划的 `next_run_at=null`。
- `GET/PATCH/DELETE /admin/api/v1/channel-monitors/{id}`：读取、CAS 修改和软归档。PATCH 要求正整数 `expected_revision`，只改显式字段；更改绑定三 ID 或刷新已失效绑定必须显式 `rebind:true`，并在同一事务重新验证完整 route/账号快照。省略 `rebind` 的旧表单不能悄悄接受新映射；仅改名称、范围或间隔仍保留原绑定。启用失效绑定返回 409 `binding_changed`，不能暗中重绑定。DELETE 要求 revision、停用并保留历史，重复归档可识别。
- `GET /admin/api/v1/channel-monitors/{id}/runs?limit=50&cursor=...`：稳定 sequence 倒序只读分页，`limit` 1–100，无任意过滤 SQL。计划最多 100 个未归档，每计划只保留最近 200 个已完成结果；正在运行行不能因保留清理被删除。列表只返回当前计划 revision 与仍有效绑定的 `latest_result`；历史仍可看旧绑定的冻结事实。响应包含 `binding_state:valid|stale`，失效时不展示“当前渠道成功”。

计划响应显式给出 ID、绑定三 ID、scope、interval、enabled、revision、UTC `next_run_at`、`binding_state`、创建/更新时间和可空最近结果。运行响应只给固定结果码、state、operation ID、起止 UTC 时间、非负可空延迟，以及冻结的 channel/model/route/account/plan revision 与无秘密路由标识；不包含 endpoint、凭据、认证头、请求/响应正文或原始 SQL/网络错误。参数错误 400 `invalid_request`，不存在 404，CAS/绑定冲突 409，存储/结构故障统一 503 `storage_unavailable`，固定脱敏文案。结果分页错误不返回部分历史。

状态能力 `features.channel_monitor_configuration=true` 表示 API 已接线，`features.channel_monitor_running=true` 只表示本进程显式开启 worker。Web 只有前一能力存在时显示入口；缺字段的旧服务不尝试新 API。页面从现有渠道、模型、显式路由与账号只读 API 选择精确绑定，展示运行开关、scope 限制、UTC 下次运行、失效绑定/重绑确认和历史；不以浏览器时区或 `catalog_ok` 推断供应商健康。旧管理 API 和旧定时测试 JSON/行为不变。

## 冻结绑定、claim、结果与恢复

计划保存渠道 revision、模型 revision、显式池 revision、账号 revision，以及 route 的上游模型、wire 协议、position 等规范字段。每次 claim 的单一 SQLite 事务重查 `(channel_id,model_id,upstream_id)` route 仍属该渠道、上述版本与字段未变，模型/账号仍启用且未归档；CAS 推进 `next_run_at` 并插入唯一 `running` operation，快照冻结 channel、model、route、account 和 plan revision。停机漏过多个间隔只对当前到期计划 claim 一次，下一次从当前 UTC 加固定间隔计算，不按遗漏数重放。相同账号已有运行时不插入另一 operation。执行前再次做同等核对；失效时零上游请求，计划停用、revision 推进、清空下次运行，并记录固定 `configuration_changed`，要求管理员显式重绑。读路径也须显示 stale，不能等 worker 启动才暴露变化。

运行期间计划停用、编辑/归档、路由映射、模型/账号版本或启用状态改变，以及服务退出，应取消本进程拥有的执行；数据库快照和终结事务复核是跨进程/迟到结果的最终屏障。配置已变时旧结果只能作为带原快照的历史 `configuration_changed`/`stale`/`cancelled`，不能覆盖新 revision 的最近结果，更不能归因到新 route 或账号。对既有配置写路径，提交后尽力取消本机相应监控；不在 SQLite 写事务中等待网络，也不依赖这个通知保证安全。账号凭据共享刷新可能改变 revision；本段不自动跟随新版本，需显式重绑，不声称因此已完成会员监控。

启动迁移在一个事务内创建并严格核验监控计划/运行表、索引、外键与行状态，检查所依赖的既有渠道/显式池结构；坏类型、缺列、恶意额外列、坏 CHECK/索引、孤儿外键或部分建表失败关闭并回滚。旧库原有账号、渠道、路由、定时测试与历史不被重写；修复冲突后可重试。启动时遗留 `running` 终结为 `interrupted`，绝不重用或自动重发原 operation ID；其仍启用计划的下一次时间至少在当前 UTC 加一个完整间隔之后。其它停机到期计划最多做一次 catch-up。默认关闭时即使到期也不触发新运行或网络。

## 独立验收

测试须区分已测和待测：旧库迁移/坏 schema/存储提交故障，管理员与员工分权及 Origin/CSRF，CRUD/CAS/上限/分页/200 条保留，显式路由校验与映射/账号版本变化，重复 claim/全局两项/同账号排他，执行前变化零网络、运行中取消与迟到结果隔离，超时、关闭、默认关闭零出站、重启 interrupted 不重放，固定脱敏响应/日志/表结构与秘密扫描。Web 测旧服务能力门控、失效绑定、历史与错误保留；真实 Go + 浏览器桌面和 390px 用动态非 8787 回环、本地合成账号/目录，验证创建、启停、历史和没有页面异常。精确 HEAD 的非缓存全 Go/vet、Linux 实际 CGO service 和有状态包 race、双 CLI 编译、GitHub CI 与独立固定二进制进程验收另列证据；不使用真实供应商、生产进程、部署或发布。
