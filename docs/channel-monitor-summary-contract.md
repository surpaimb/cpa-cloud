# 渠道监控已保留历史摘要契约（OBS-02 第二段）

状态：2026-09-30 开发预览实现契约，先于代码冻结；实现和通过的验收另记。本契约由本项目的[渠道监控首段契约](channel-monitor-contract.md)、[开发预览边界](preview-contract.md)及独立编写的测试推导，不移植旧 CPA、CLIProxyAPI 或 Sub2API。只读取本服务已有的 SQLite 计划和运行事实；不引入新供应商协议、第三方依赖、数据表或密钥材料。首段所列公开目录协议来源仍只适用于目录探测，不能据此推断生成能力或供应商健康。

## 接口与固定响应

`GET /admin/api/v1/channel-monitors/{id}/summary` 仅接受现有管理员会话及管理端 Origin 规则；员工 Bearer Key 不可调用。`id` 必须是现有计划 ID，已归档计划仍可读其历史。不接受任何查询参数或请求正文。成功状态 200，`Content-Type: application/json`，`Cache-Control: no-store`。响应严格包含以下字段，不返回计划名称、渠道、账号、路由、endpoint、凭据、operation ID 或原始运行内容：

```json
{
  "plan_id": "mon_example",
  "as_of": "2026-09-30T00:00:00Z",
  "through_sequence": 0,
  "retained_completed": 0,
  "retained_window_full": false,
  "running": 0,
  "earliest_finished_at": null,
  "latest_finished_at": null,
  "counts": {
    "local_credential": {
      "local_credential_ok": 0, "catalog_ok": 0, "authentication_failed": 0,
      "rate_limited": 0, "unsupported": 0, "timeout": 0,
      "invalid_response": 0, "configuration_changed": 0, "stale": 0,
      "cancelled": 0, "interrupted": 0, "test_in_progress": 0,
      "capacity_exceeded": 0, "storage_unavailable": 0, "internal_failure": 0
    },
    "catalog": {
      "local_credential_ok": 0, "catalog_ok": 0, "authentication_failed": 0,
      "rate_limited": 0, "unsupported": 0, "timeout": 0,
      "invalid_response": 0, "configuration_changed": 0, "stale": 0,
      "cancelled": 0, "interrupted": 0, "test_in_progress": 0,
      "capacity_exceeded": 0, "storage_unavailable": 0, "internal_failure": 0
    }
  }
}
```

`as_of` 是读取前固定一次的 UTC 时间（RFC 3339），不是最后一次探测时间。`through_sequence` 是同一快照中该计划所有状态运行行的最大 sequence，空历史为 0；它只是并发读取的高水位，不是连续计数，也不是可用性指标。`retained_completed` 是同一快照实际保留的 `completed` 行数，范围 0–200。`retained_window_full` 当且仅当该数等于 200；它只表示当前保留窗口已满，不证明更早是否有运行或其结果。`running` 是同一快照仍在运行的行数，正常范围 0–1，不计入完成结果。两个 scope 与全部固定结果码始终存在，整数非负；所有计数之和必须等于 `retained_completed`。空历史的计数全部为 0、时间均为 null。两个时间是已保留完成行的 `finished_at` UTC 最早/最晚实际值，按时间而非 sequence 求极值；有完成行时均非 null。

## 一致性、失败与边界

整个计划存在性、sequence 高水位、运行读取和聚合在**一个 SQLite 只读事务**完成。第一个数据库读取固定快照；之后提交的运行或归档/重绑不混入该响应。所有计划 revision 和旧绑定 revision 的已保留运行均计入，不能将历史归因到当前渠道或账号。不得按当前绑定、当前 scope 或当前 enabled 筛选历史。读取最多 200 个完成行及正常的一个 running 行；超界、未知 scope/结果码/state、坏时间、坏行/表结构、SQL/迭代/关闭/事务错误均以 503 `storage_unavailable` 关闭，不返回部分计数。不存在的计划返回 404 `not_found`；查询参数或无效 ID 返回 400 `invalid_request`。错误文案固定脱敏，响应不缓存；不暴露 SQL、文件路径、endpoint、凭据、请求或响应。

本接口只做即时读取，不写事实、不触发 worker/目录/生成请求、不增加后台聚合、告警或通知。摘要不表示当前渠道健康、供应商配额、模型生成成功率、可用率或窗口外总次数。页面只在独立能力 `features.channel_monitor_retained_summary=true` 时发请求；旧服务缺字段时不展示摘要入口，也不试探调用。页面明确标注“仅已保留历史”，展示窗口已满提示和历史跨绑定提示，不提供成功率或健康评级；桌面和 390px 均可读。

## 验收边界

独立测试覆盖旧数据库启动、空/200 条窗口、两个 scope 与全部结果码、running 行、归档/重绑旧历史、并发新写的一致快照、坏 schema/行/时间/SQL 失败关闭、管理员与员工分权、Origin 和脱敏、不支持能力的旧 Web 服务。真实 Go 进程与桌面/390px 浏览器验收使用动态非 8787 回环和合成数据。实现后分别报告精确 HEAD 的非缓存 Go/vet、实际 CGO race、双 CLI 构建、Web、进程和 GitHub CI；不得把计划项目写成已测项目。不开真实供应商请求，不部署、不打包、不发布。
