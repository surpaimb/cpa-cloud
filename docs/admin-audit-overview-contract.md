# 管理员只读审计总览第一段契约

状态：AUDIT-01 开发契约，2026-09-29。

本段只把 CPA Cloud 已经存在的四类同事务管理事实投影为一个有界、只读的管理员视图：

- `account_pool_audit`；
- `account_lifecycle_audit`；
- `governance_management_audit`；
- `governance_general_budget_audit`。

它不建立新的“统一写审计”，不修改、回填、复制或延长原审计事实，也不宣称所有管理变更已被
覆盖。账务、登录/会话、备份、支付和其他 mutator 的缺失审计，导出、可配置保留、防篡改、独立角色与敏感操作
二次认证都留待后续。提示词、模型响应、管理请求正文和任何凭据正文始终排除。

## 1. 不可变的产品边界

1. 只读查询不写新的审计行，不更改四个来源的写入路径、事务语义、表内容或保留策略。
2. 查询是“当前仍存在的四类事实视图”，不是完整历史证明。`account_lifecycle_audit` 继续保持现有最多
   1000 条规则；其他三个来源保持各自现有行为。本段不将它们暗中统一为新保留期。
3. 只有已有独立管理员会话可读。接口使用现有 `requireAdmin(..., false)` 会话验证；若请求携带
   `Origin`，还必须与当前 HTTP/TLS host 同源。只读 GET 不要求 CSRF token，但错误 Origin 必须 403。
4. 所有响应继承 `Cache-Control: no-store`，错误不包含 SQL、表定义或行内容。
5. 不引入新第三方依赖或外部协议。实现依据本项目自有功能规格与现有 SQLite/HTTP 契约。

## 2. 规范化事件

管理 API 只返回下列固定形状：

```json
{
  "source": "account_pool",
  "event_id": "aud_...",
  "actor_id": "admin_...",
  "action": "account_group.update",
  "target_type": "account_group",
  "target_id": "grp_...",
  "result": "succeeded",
  "revision": null,
  "occurred_at": "2026-09-29T08:30:00.123456789Z"
}
```

| 源 token | 表 | event_id | target | result | revision | time |
| --- | --- | --- | --- | --- | --- | --- |
| `account_pool` | `account_pool_audit` | `id` | `target_type` / `target_id` | 存储值 | NULL | `occurred_at` |
| `account_lifecycle` | `account_lifecycle_audit` | `id` | `target_type` / `target_id` | 存储值 | NULL | `occurred_at` |
| `governance_management` | `governance_management_audit` | `operation_id` | `resource_kind` / `resource_id` | `succeeded` | 存储值 | `created_at` |
| `governance_general_budget` | `governance_general_budget_audit` | `operation_id` | 常量 `budget` / `policy_id` | `succeeded` | 存储值 | `created_at` |

治理两表只在变更事务成功时与 operation receipt 同事务写入，因此统一视图对它们显式投影
`result=succeeded`；不借此伪造失败尝试。`revision` 在源事实没有该字段时必须为 JSON `null`，有值时使用
JSON number，并严格位于 `1..9_007_199_254_740_991`。

服务只选取上表字段。不 JOIN 管理员密码/会话、Key、上游凭据、operation payload digest、请求或响应正文。

## 3. HTTP 查询契约

`GET /admin/api/v1/audit/events`

首页可接受以下参数，未列出、重复、空字符串或超长参数都返回 400 `invalid_request`：

- `from` 与 `to`：必须同时省略或同时提供。省略时以服务当前 UTC 为 `to`，`from=to-24h`；提供时必须是
  RFC3339/RFC3339Nano，解析后满足 `from < to` 且窗口不超过 31 天。区间为 `[from,to)`。
- `sources`：可选的逗号分隔 token，只允许上表四值，不允许重复或空 token。省略表示全部四源。
- `actor_id`、`action`、`target_type`、`target_id`、`result`：可选精确匹配，不接受 SQL pattern/正则或模糊扩展。
  各值必须是可见、非空、无控制字符的 UTF-8，且不超过 256 bytes。
- `limit`：默认 50，范围 `1..100`，只允许无前导零十进制数字。

续页请求只能提供一个非空 `cursor`；不能再提供任何筛选或 `limit`。游标由安装密钥派生 HMAC 签名，
绑定规范化时间窗口、源列表、全部筛选、limit、首页水位与末项排序键。解码、版本、MAC、长度、字段或水位验证
任意失败统一返回 400，不暴露解码细节。游标不是跨安装、跨恢复或跨主密钥的持久 API。

响应：

```json
{
  "from": "2026-09-28T08:30:00Z",
  "to": "2026-09-29T08:30:00Z",
  "snapshot_at": "2026-09-29T08:30:00.123456789Z",
  "sources": ["account_pool", "account_lifecycle", "governance_management", "governance_general_budget"],
  "items": [],
  "next_cursor": null
}
```

`items` 始终是 JSON array，`next_cursor` 是字符串或 `null`。续页保持首页的 `from/to/snapshot_at/sources`。

## 4. 顺序、时间精度与快照水位

1. 源时间必须解析为 RFC3339，输出为 UTC `RFC3339Nano`。无小数、毫秒、微秒和纳秒表示必须按实际时刻比较，
   不得直接用可变长字符串比较伪造时序。
2. 全局顺序固定为：`occurred_at DESC`，然后按上文表格的源顺序 ASC，最后 `event_id DESC`。同一实际时刻即使有
   不同小数精度，也必须进入 source/event_id tie-break。
3. 首页在同一只读事务中先严格验证四张必需源表，再捕获每张表当时的最大 SQLite `rowid` 作为不透明水位。
   即使用户只筛选一个源，四源都必须健康，不允许以筛选绕过组合完整性。
4. 所有续页只能读取 `rowid <= 首页水位` 的事实。首页之后并发新增的事实不进入该页面链，即使新行时钟回退或
   与旧行同时；签名 keyset cursor 确保已返回项不重复、不因新写入重排。
5. 现有保留逻辑可在两次 HTTP 请求之间删除旧行；查询不锁定或复制这些事实。因此续页可缩短，但不能因并发新写入
   重复或重排；页面必须显示“现存事实视图”边界。

## 5. 失败关闭与旧库

查询不新建 schema。它依赖现有各组件迁移已建立的四张表，并在每次请求的只读事务内验证：

- 对象类型必须为 table；
- 列集合、类型、NOT NULL/PRIMARY KEY 属性必须与已有契约一致；
- 治理两表必须与其已有严格 DDL 一致；账号池表的管理员外键/result 约束以及生命周期时间索引必须存在；
- 所有选中行的时间、revision 和字符串元数据必须可解析且在界限内。

任一源表缺失、被 view/伪表替换、schema 不兼容、游标水位不可用、查询超时/取消、扫描或解析失败，都在输出任何
条目前返回 503 `storage_unavailable`。不允许从其余表静默返回部分结果。现有旧库仍由四个所属组件自己的严格迁移升级；
本功能不实施宽松的自动补表。

单次查询使用 5 秒 context timeout。每个选中源最多取 `limit+1` 个候选，再在 Go 中按全局键合并；不允许无界加载整张表。

## 6. 管理网页与能力协商

`GET /admin/api/v1/system/status` 新增 `features.admin_audit_overview=true`。

- 旧服务省略该能力时，网页不显示审计导航或发送审计请求。
- 能力为 `true` 时，网页对响应做运行时严格验证：未知 source/result、缺字段、错类型、无效 UTC 时间、非法 revision、
  无序/重复项或畸形 cursor 都整页失败关闭，不展示部分结果。
- 首屏默认显示过去 24 小时和全部四源。可按源、actor、action、target type/ID 和 result 精确筛选；应显示时间、
  来源、操作人、动作、目标、结果和可选 revision。
- 分页使用服务器不透明 cursor，不在前端猜测排序键。换筛选必须废弃旧页链并重新请求首页。
- 桌面和 390px 宽度下均不水平溢出；窄屏条目改为有标签的纵向元数据，不靠颜色区分结果。
- 页面固定显示：这只是四类现存成功管理事实，不代表全部管理操作或完整历史。

## 7. 安全与隐私

允许字段固定为 `source,event_id,actor_id,action,target_type,target_id,result,revision,occurred_at`。测试和进程验收必须
扫描审计 HTTP 响应与服务日志，确认生成的敏感值和下列正文/内部字段不从审计读路径泄露：

- 管理员密码、session cookie、CSRF token；
- 员工 Key 明文/摘要、Authorization/Proxy-Authorization；
- 上游 API/OAuth 凭据、恢复材料或支付 secret；
- prompt、请求正文、模型响应、工具参数/输出；
- operation payload digest 或原始 SQL 错误。

持久化验收另核四源事实未因本只读功能被改写，并扫描临时数据库及 WAL 中合成的管理员密码、员工 Key 明文、
上游凭据明文和 session cookie。既有员工 Key 摘要及治理 operation payload digest 在其他持久化表中有其原有用途，
不能把“整库无摘要”作为本接口承诺。现有 `sessions.csrf_token` 在数据库中明文持久化；本段不改动会话存储，
也不声称整库没有 CSRF token。验收须确认审计响应与服务日志不暴露该 token，且审计读路径不新增其持久化副本。

actor/action/target 本身是既有管理元数据；管理员不得把秘密或正文放入 ID/名称字段。本页不额外解析、扩展或链接
target 所指资源的正文。

## 8. 验收矩阵

### Go 与管理 API

1. 四源各至少一条，验证规范化字段、精确筛选、固定源 allowlist 与全局顺序；
2. 无小数/毫秒/微秒/纳秒、相同实际时刻的不同表示、相同时间多源与多 ID；
3. 首页后并发插入更新/同时/回退时钟的行，续页不重排、不重复、不纳入新行；
4. cursor 篡改、截断、超长、旧版、未知源、越界水位以及 cursor 与其他参数同现；
5. 未登录 401，错误 Origin 403，同源/无 Origin 的已登录 GET 成功，不要求 CSRF；
6. 未知/重复/空/恶意参数、时间反转、31 天越界、limit 越界与过长元数据；
7. 逐一删除四源、用 view/错列/错约束替换、注入查询失败/取消，均为 503 且零部分 items；修复后可重试；
8. 旧库由现有严格迁移升级后可查，不额外改写审计行；已有保留数量与事务回滚测试不退化；
9. 按第 7 节区分审计响应/日志与既有数据库字段的敏感值扫描，以及超时、取消、并发查询和 SQLite 连接池回归。

### 网页、进程与 CI

1. 旧服务无 capability 时零审计请求；声称新能力但响应畸形时整页失败关闭；
2. 桌面和 390px 宽度完成四源列表、筛选重置、续页/上一页、空态、失败重试与范围说明；
3. 从临时目录启动精确 HEAD 二进制，使用动态非 8787 回环端口和合成管理员/事实，验证重启、越权、跨站、分页水位、
   敏感扫描和无真实供应商请求；
4. `gofmt`、相关包测试、非缓存完整 Go、`go vet`、service 与有状态包 CGO race、Web typecheck/test/build、
   `git diff --check`、本地 Markdown 链接、仓库 CI 及独立固定二进制窄矩阵都钉定到最终精确 HEAD。

所有验收只使用合成元数据与回环请求，不访问真实 provider/会员账号，不使用 8787，不创建部署、tag、安装包或 release。
