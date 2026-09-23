# 账号分组、渠道与多账号路由配置契约

状态：独立持久化与管理 API 批次，尚未接入模型请求执行；2026-09-23。本批不能据此宣称账号池或实时调度已经上线。

## 边界

本模块只保存账号分组、渠道和公开模型到上游账号的路由配置。管理员请求沿用现有 session、同源 Origin 和 CSRF 校验。模块不读取或探测上游凭据，不修改员工的 `model_mode` 或 `employee_models`，也不改变每个员工看到的公开模型 ID。

公开模型仍由 `models.id` 唯一标识。没有 `model_account_pool_configs` 记录时，`revision` 为 0，读取接口返回 `models` 中的旧单路由作为兼容项；模型执行必须继续走原单路由，不得因为迁移表存在而改变行为。首次成功 PUT 才创建 revision 1 的显式账号池。后续请求执行接线必须单独引入 `internal/scheduling`，本批不修改执行器。

## 管理 API

所有路径位于 `/admin/api/v1`。GET 只要求有效管理员 session；POST 和 PUT 还要求现有 `requireAdmin(..., true)` 提供的同源 Origin 与 `X-CSRF-Token` 校验。

| 方法与路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `GET /account-groups` | 无 | `{items:[{id,name,revision}]}` |
| `POST /account-groups` | `{name}` | 201 `{id,name,revision:1}` |
| `PUT /account-groups/{id}` | `{expected_revision,name}` | `{id,name,revision}` |
| `GET /channels` | 无 | `{items:[{id,name,group_id?,revision}]}` |
| `POST /channels` | `{name,group_id?}` | 201 `{id,name,group_id?,revision:1}` |
| `GET /models/{id}/accounts` | 无 | `{model_id,revision,items}` |
| `PUT /models/{id}/accounts` | `{expected_revision,items}` | `{model_id,revision,items}` |

账号池的 `items` 为 1 到 64 项，每项为：

```json
{
  "upstream_id": "ups_example",
  "upstream_model": "provider-model",
  "priority": 0,
  "weight": 1,
  "max_concurrency": 4,
  "channel_id": "chn_optional"
}
```

- `upstream_id` 必须存在，同一池中不能重复。
- `upstream_model` 为 1 到 256 字节，不能有首尾空白、NUL 或控制字符；Gemini 继续使用现有 Gemini 模型名校验。
- `priority` 范围为 -1,000,000 到 1,000,000，`weight` 为 1 到 10,000，`max_concurrency` 为 1 到 1,024。
- `channel_id` 可省略；提供时必须指向现有渠道。
- 同一池的所有上游必须具有相同 `provider_kind`，防止在一个调度池中混用协议。关闭 `ExperimentalCodexMembership` 时，任何 Codex membership 池配置都返回 `unsupported_feature`。

首次 PUT 要求 `expected_revision: 0`。更新要求提交当前正 revision。revision 比较、配置 revision 更新、旧成员删除、新成员插入及成功审计在同一事务中完成；冲突返回 409 `revision_conflict`，不得留下部分成员。更新映射不增删 `employee_models`。

列表读取上限分别为 1,000 个分组和 1,000 个渠道；账号池读取上限为 64 项。API 同时阻止通过正常写接口超过分组和渠道上限。所有错误都使用固定、脱敏的管理错误，不回传 SQL、请求正文、凭据或上游响应。

## 持久化与迁移

`migrateAccountPools(ctx)` 在一个事务内创建并核验以下表和索引：

- `account_groups`：名称、revision 和创建时间。
- `account_channels`：可选分组外键、名称、revision 和创建时间。
- `model_account_pool_configs`：公开模型的配置 revision。
- `model_account_pool_routes`：上游账号、上游模型、priority、weight、并发和可选渠道；数据库约束重复账号、范围和外键。
- `account_pool_audit`：只记录管理员 ID、固定 action、目标类型、目标 ID、成功结果和时间。

迁移对已存在表核验精确列集合、关键约束和外键，并在提交前执行 `PRAGMA foreign_key_check`。不兼容表或外键错误会回滚本次创建；修复冲突后可重试。重复调用和正常重启不会重写现有配置。

审计表没有请求正文、提示、响应、token、Authorization、Cookie、上游 Key 或配置快照字段。拒绝和存储错误也只返回固定错误，不把内部错误写入响应。

## 根任务接线清单

1. 在 `store.initialize` 完成现有基础表及上游迁移后调用 `s.migrateAccountPools(ctx)`；失败时返回带固定阶段名称的启动错误。
2. 在 `App.Handler` 创建私有 mux 后调用一次 `a.registerAccountPoolHandlers(mux)`。
3. 暂不修改 Chat、Responses、Messages 或 Gemini 执行器。后续执行接线读取 revision 大于 0 的配置，经员工公开模型权限过滤后再交给调度器；revision 0 继续使用旧单路由。
4. 接线执行器时补充禁用账号、协议能力、冷却、租约续期、revision 竞争、流已提交不换号及用量归属的端到端验收。

实现依据本项目规格独立编写，没有复制 CLIProxyAPI、Sub2API 或归档 CPA 的实现、迁移或测试。
