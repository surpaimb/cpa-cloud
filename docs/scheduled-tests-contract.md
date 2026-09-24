# 持久定时测试首批契约

状态：2026-09-24 已实现开发预览首批。实现依据
[下一批独立交付](parity-next-batch-2026-09-24.md)与既有
[上游测试契约](upstream-health-contract.md)，为本仓独立编写。

## 能力边界

- `--scheduled-tests-enabled` 默认关闭。关闭时管理 API 与网页仍可保存计划，但 worker 不 claim、不创建运行记录，也不访问上游。
- 首批仅复用现有 `local_credential` 与 `catalog` 健康测试路径。前者只检查本地可恢复凭据；后者只验证模型目录可达。两者均不证明模型生成可用，也不会替代有成本的账号恢复探测。
- 间隔为 300–86400 秒固定 UTC 调度；cron、时区、通知、渠道聚合监控、生成测试和告警不在本批。
- 单进程最多同时运行两个计划，同一上游账号不重叠。服务重启把旧 `running` 记录终结为 `interrupted`，为仍启用的计划最多安排一次新 operation 补跑，不重放旧 operation。

## 管理接口

- `GET/POST /admin/api/v1/scheduled-tests`
- `GET/PATCH/DELETE /admin/api/v1/scheduled-tests/{id}`
- `GET /admin/api/v1/scheduled-tests/{id}/runs?limit=50&cursor=...`

写请求使用既有管理员会话、Origin 与 CSRF 校验。PATCH/DELETE 使用正整数 `expected_revision`；过期版本返回 `revision_conflict`。DELETE 归档并停用计划，保留历史。最多 100 个未归档计划；运行历史以稳定不透明游标倒序分页，每页最多 100 条。

系统状态能力：

- `scheduled_tests_configuration=true`：管理 API 已接线。
- `scheduled_tests_running=true`：本进程用显式 CLI 开关启动了 worker。

旧服务器缺少能力字段时，网页显示不支持状态，不推断开关已启用。

## 持久化与竞态

计划 claim 在一个 SQLite 事务内推进 `next_run_at` 并写入 `running` 记录；下一次到期时间按当前 UTC 加固定间隔计算，不追赶全部积压。执行前再次核对账号启用、归档和 revision，再调用既有健康测试协调器。

修改、停用、归档或服务关闭会取消本进程拥有的运行。运行结果只写回其原始 plan revision 的历史；列表的 `latest_result` 仅选择当前 plan revision，因此旧结果不能覆盖新配置。每计划只保留最近 200 条完成历史，清理与结果终结在同一有界事务中。

账号归档集成使用 caller-owned transaction hook：归档事务内停用关联计划、递增 revision 并清空 `next_run_at`；提交后再取消本进程运行，不在数据库锁内等待网络。

运行记录只保存计划 revision、operation ID、固定结果码、UTC 起止时间和耗时。不会保存凭据、认证头、响应正文、提示词或模型响应。

## 已验证与限制

自动测试覆盖 CRUD/CAS/CSRF、默认关闭零出站、fake clock 到期、全局并发、同账号排他、变更取消、claim/关闭竞态、存储错误分类、旧结果隔离、重启恢复、迁移失败回滚/修复重试、200 条历史保留和稳定分页。

真实临时子进程使用随机端口、合成 API Key 与本地假目录验证：默认关闭时到期计划零请求；开启后只访问 `/v1/models` 一次并记录 `catalog_ok`；再次重启没有重放。浏览器验收覆盖桌面、390×844 手机视口、创建/编辑/启停入口、历史错误恢复与不可恢复归档确认。未使用真实供应商凭据或收费请求。
