# 本机账号池预留快照契约（OBS-01 第一段）

状态：开发预览契约，2026-09-30。实现和验收结果须另行记录；本文不把计划写成已交付能力。

## 目的与边界

管理员在现有公开模型账号池编辑入口附近查看一次本机 SQLite 持久预留快照，以解释显式池的容量和部分本地阻断条件。它不探测供应商、不派发请求、不改变路由、租约、冷却或恢复状态，也不提供员工、Key 或普通模型 API 入口。服务仍为单进程调度；本接口不是实时 HTTP 请求计数、供应商额度/余额、账号健康或任何员工 Key 的最终可用性判断。

来源仅为本项目的[账号池配置契约](account-pool-service-contract.md)、[运行时契约](account-pool-runtime-contract.md)与当前独立实现的本地模式；没有新增第三方依赖或外部协议。第三方依赖及许可证仍按仓库现有清单管理。不得使用参考 CPA/CLIProxyAPI/Sub2API 代码或测试作为模板。

## 管理 API

`GET /admin/api/v1/models/{id}/pool-runtime` 只接受不带查询参数的 GET。必须有管理员会话；若携带错误 `Origin` 返回 403 `origin_rejected`，没有管理员会话返回 401，员工 Bearer Key 不构成授权。GET 不要求 CSRF。未知或已归档模型返回 404 `not_found`；非法查询返回 400 `invalid_request`。存储、结构、时间或事务失败返回固定脱敏 503 `storage_unavailable`，不发送成功对象的一部分。响应始终 `Cache-Control: no-store`。

成功对象：

```json
{
  "model_id": "public-model",
  "model_revision": 3,
  "pool_revision": 2,
  "pool_status": "explicit_pool",
  "as_of": "2026-09-30T00:00:00Z",
  "items": [{
    "upstream_id": "ups_example",
    "account_revision": 4,
    "configured_max_concurrency": 4,
    "global_max_concurrency": 2,
    "request_reservations": 1,
    "maintenance_reservations": 0,
    "remaining_local_slots": 1,
    "block_reasons": [],
    "cooldown_until": null
  }]
}
```

`pool_status` 固定为 `explicit_pool`、`legacy_no_pool` 或 `model_disabled`。无显式 `model_account_pool_configs` 的旧单路由返回 `pool_revision:0`、`legacy_no_pool`、`items:[]`；不借旧路由推算并发。显式池的模型停用时仍可展示至多 64 个已保存路由，但状态为 `model_disabled`；每路由的 `global_max_concurrency` 与 `remaining_local_slots` 为 `null`，并有 `model_disabled` 阻断原因。已归档模型不展示。

显式、启用模型的路由按保存的 `position` 顺序返回，必须有 1–64 个且无重复上游。`configured_max_concurrency` 是该路由配置值；`global_max_concurrency` 是同一 `upstream_id` 在**所有 `enabled=1` 公开模型**显式路由中的 `max_concurrency` 最小值，与调度器当前的全局容量 SQL 语义一致，不按当前模型单独计；若存量出现已归档却仍启用的矛盾状态，整体 503。请求与维护预留分别是对应两张租约表中同账号、在 `as_of` 严格未到期的持久记录数，跨模型计；过期记录只在本读路径忽略，不删除。重启后尚未到期的记录仍保守占槽至原到期时刻，读路径不延长 TTL。`remaining_local_slots = max(0, global_max_concurrency - request_reservations - maintenance_reservations)`；这是“持久记录尚未预留的槽位”，不表示新请求必能立即获得内存 scheduler 租约。

`block_reasons` 为固定枚举数组，可同时包含 `upstream_disabled`、`cooldown_active`、`recovery_isolated`、`membership_disabled`、`reauth_required`、`capacity_reserved`；停用模型另含 `model_disabled`。它只表达本机已知条件：API Key 账号无会员凭据状态字段；Codex `imported_unverified` 不被推断为已验证，只有 `reauth_required` 阻断。`cooldown_until` 仅在冷却截止时间晚于 `as_of` 时给出 UTC 时间，否则为 null。恢复隔离的存在独立于冷却是否到期。账号归档或路由/提供商/wire 数据失配属于损坏配置，整体 503，不把它伪装成可用或零余额。容量满时包含 `capacity_reserved`，即使其它阻断原因也同时存在。普通员工/Key 策略、协议兼容、预检与供应商实际状态未在此接口逐人评估。

不返回 endpoint、凭据密文、来源绑定、上游模型名、employee_id、key_id、租约 ID、请求正文、提示、响应或原始 SQL/上游错误；日志也不得包含这些内容。`upstream_id` 是管理员现有配置中的账号标识，不是员工身份。

## 快照与失败关闭

使用一个只读 SQLite 事务。首先在事务内读模型/池 revision 以锚定数据库快照，随即捕获一次 UTC `as_of`；所有配置、全局容量、租约、冷却和隔离读取都来自该事务，并以同一 `as_of` 比较时间。时间用 Go 的 RFC3339Nano 解析而不是直接比较不同小数位宽的 SQLite 文本；异常时间、空值、非法计数/状态、缺表或不兼容表结构、SQL 错误、取消、超时或提交失败均整体 503，不用零填补。处理上限为 64 路由和有界请求上下文；响应仅在读事务提交后编码。

并发写入可以在读事务锚定后改变真实调度状态；本接口只说明所读版本，不提供预留或一致性屏障。内存 scheduler 在选中候选到持久化之间也可能暂时占槽却不在数据库中出现；故剩余数绝不承诺为立即可派发数。管理员手动刷新可获取新快照，不自动轮询。

## 网页与验收

网页仅在 `features.account_pool_runtime_observation === true` 且现有账号池配置入口可用时请求和显示；旧服务没有该 flag 时零请求。模型切换/关闭时取消旧读；保存配置后重新获取，不把未保存编辑值混入服务端快照。桌面和 390px 清晰显示来源、时间、路由配置、两类预留、剩余槽位及阻断原因，并明确“非供应商额度/实时请求数”。读失败显示可重试错误，不留下上一模型或过期数据。

独立测试覆盖启停/冷却/恢复隔离、0/1/满载、跨模型最小容量、两类预留、到期与重启、并发写入后的快照隔离及取消、无池旧单路由、缺/坏表和 SQL 故障的整体 503、权限/Origin、无秘密响应及日志。验证分列本地 Go/Web/浏览器/隔离进程、固定 VCS 二进制独立验收和精确提交 GitHub CI；没有真实提供商探测或 8787 生产进程操作。
