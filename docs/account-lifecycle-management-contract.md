# 账号与模型生命周期管理契约

状态：2026-09-24 开发预览实现。本文是 CPA Cloud 独立规格；不引用或移植归档 CPA、CLIProxyAPI 或 Sub2API 实现。本批没有新增第三方依赖，也不改变既有公开模型协议。

## 数据与 API

- 模型增加正整数 `revision`、`archived` 和 `archived_at`。`PATCH /admin/api/v1/models/{id}` 以 `expected_revision` 修改默认上游、上游模型和启停状态；公开 ID 不可修改。
- `DELETE /admin/api/v1/models/{id}` 接收 `{expected_revision}` 并建立墓碑。归档项停用但保留模型 ID、员工策略引用、账号池配置及账本关联；同 ID 永久不能重建。重复归档返回 `archive_result=already_archived`。
- 上游增加 `archived` 和 `archived_at`。`DELETE /admin/api/v1/upstreams/{id}` 只允许归档未被未归档模型默认目标或账号池条目引用的上游，否则返回 `409 upstream_in_use`。
- 上游归档在同一事务停用对象、递增 revision、清空可恢复凭据、删除 OAuth 刷新绑定与出站代理绑定，并保留非秘密身份和历史账本关联。重复归档返回可识别的 `already_archived`。
- 模型和上游列表默认隐藏墓碑；唯一的可选查询参数 `include_archived=true` 显示墓碑。重复或非法参数返回 400。
- 所有写接口继续要求管理员会话、CSRF 和 Origin。`expected_revision < 1` 返回 `400 invalid_revision`；过期 revision 返回 `409 revision_conflict`。
- `features.account_lifecycle_management=true` 只在本批后端、网页入口和墓碑执行边界同时存在时返回。旧服务器缺少标志时网页不显示生命周期按钮。

## 并发与执行边界

上游变更沿用 `Codex account mutation lock → admission write lock → SQLite transaction`；模型变更使用 `admission write lock → SQLite transaction`。不得在事务或 admission 锁内等待网络、worker 或在途模型请求完成。

归档/停用前已经完成实际派发的请求可以结束并进入原账本结算。尚未派发的请求在路由选择、账号池租约持久化和出站代理冻结前重查模型/上游 `archived`、`enabled` 和 revision；墓碑不能进入 Chat Completions、Responses、Messages 或 Gemini 派发。Codex 后台刷新、手工刷新、重新导入、模型发现、手工健康测试和恢复流程都排除墓碑。

为定时测试批次预留两个包内接线点：归档事务中的 SQL-only hook，以及提交后的非阻塞取消 hook。前者必须与上游墓碑原子提交；后者不得在 SQLite 事务内等待 worker。

## 迁移与审计

迁移以一个 SQLite 事务添加列、检查约束、审计表和索引，并验证列类型、NOT NULL、默认值、CHECK、索引定义及已有数据。结构或数据不兼容会整体回滚，修复后可重试；不会以同名但错误结构冒充成功。

本批审计表最多保留最近 1000 条生命周期记录，字段为 actor、action、target type/ID、result 和 UTC time；不记录 Key、凭据、请求正文或模型响应。它是局部管理审计，不等于全局审计查询、导出或防篡改系统。

## 已测与未覆盖

仓库测试覆盖创建、修改、启停、归档、重复归档、不可重建 ID、默认目标/池引用冲突、CAS、无效 revision、凭据销毁、旧结构失败回滚与重试、四协议零墓碑派发、Codex 刷新/归档排序，以及网页能力降级和不可恢复确认。

真实供应商账号、供应商侧撤销、全局审计查询/导出、墓碑恢复、多节点一致性和正式发布不在本批范围内。定时测试的关联计划停用由其独立批次通过上述 hook 集成验收。
