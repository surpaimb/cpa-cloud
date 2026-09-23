# 上游账号测试与恢复契约

状态：**第一批已通过本地及 Linux CI 验收；后续生成恢复探测仍是提案**。截至 2026-09-23，源码已加入
凭据/目录测试记录、网页入口、cooldown 展示与事件条件清除。下文明确区分本批接口和后续探测，
实际测试与 CI 证据见[集成状态](integration-status.md)。preview.3 安装包不含本批功能。

本文依据本仓现有信任边界、账号池与用量契约独立编写，不以其他产品源码为实现模板。功能矩阵中的
ACCT-01、ACCT-03 与 ACCT-05 应拆成两个小批交付：先实现不产生模型生成用量的凭据/目录测试、
cooldown 展示和手工清除；真实生成恢复探测留到后续，并且默认关闭。

## 共同边界

全部接口仅向管理员会话开放；写请求沿用 Origin/CSRF 校验，员工 Key 不得调用。请求体、响应体、目录分页、
账号测试并发和耗时必须有界。测试结束不保留解密后的凭据；Codex 的可清零容器在使用后销毁。
现有 API Key 解密接口返回 Go string，只能限制其作用域和生命周期，不能保证即时擦除所有内存副本；
不能把这一限制描述成已完成安全内存擦除。不把本地检查变成秘密读取接口，明文不进入观测表、响应或日志。

账号的管理员启停状态、运行时 cooldown、测试观测和生成可用性是四种不同事实：

- `enabled` 只表达管理员是否允许调度；测试不得自行启用或停用账号。
- cooldown 是调度器根据固定失败分类产生的临时不可调度状态。
- 测试观测是带时间、账号 revision 和明确 scope 的历史事实，不替代管理员意图。
- 只有针对实际模型执行协议并完整成功的生成探测，才能产生 `generation_ok`。模型目录读取成功只能
  产生 `catalog_ok`，不能标记账号“健康”、解除恢复隔离或声称生成可用。

所有结果只保存有界元数据：账号 ID、provider、账号 revision、测试 scope、固定结果码、开始/结束时间、
可选延迟和操作 ID。不得保存 Authorization、API Key、OAuth token、请求或响应正文、原始上游错误、
管理员会话、SQL 错误或任意日志文本。错误响应和日志只使用固定脱敏文案与 reason code。

测试端点只能使用账号已经保存的 endpoint。不得接受调用方提供的 URL、头、请求正文或脚本；不得跟随
重定向。DNS 解析、连接时 IP 校验、TLS 下限和回环测试开关必须沿用现有上游客户端。测试不得访问
CLIProxyAPI Management API、云元数据地址、回环地址或私网地址；仅测试配置显式允许的本地合成环境例外。

## 第一批：凭据/目录测试与 cooldown 管理

### 本批冻结接口

- POST 测试同步等待，首次终结返回 200；相同操作仍在运行返回 202；GET 返回同一对象。
  对象字段为 `operation_id`、`upstream_id`、`provider_kind`、`requested_revision`、
  `tested_revision`（可为 null）、`scope`、`state`（pending/in_progress/completed）、
  `result_code`（未终结时 null）、`created_at`、`started_at`、`finished_at`、`latency_ms`（后三项可为 null）。
  所有终态，包括 stale/interrupted/cancelled，统一用 completed 加固定 result_code 表示。
- 同账号最多一个测试，全局最多四个测试，上游执行阶段最多十秒；认领及终结状态写入分别有三秒超时。
  未取得测试槽时返回 409 `test_in_progress`
  或 429 `test_capacity_exceeded`，不创建操作记录；客户端不得自动换 operation ID 重试。
- 每个安装最多保留 10000 条不可变操作记录。满额后拒绝新的操作，返回 409 `test_history_full`；
  已存在的操作仍可查询和幂等重放。不自动删除幂等键，也不自动重发中断操作。
- 输入无效返回 400 `invalid_request`；账号不存在为 404 `not_found`；账号 revision、输入或冷却事件
  冲突为 409 `revision_conflict`、`operation_conflict` 或 `cooldown_conflict`。存储故障统一 503。
- 上游列表增加顶层 `server_time`（RFC3339），每项增加可空 `latest_observation` 和 `cooldown`。
  观测字段为 `operation_id`、`scope`、`result_code`、`account_revision`、`checked_at`、`latency_ms`；
  冷却字段为 `event_id`、`failure_class`、`cooldown_until`、`updated_at`、`active`。
  `active` 由服务端按返回的 server_time 判断；前端不能自行认定恢复。
- 清除成功对象为 `{result: "cleared" | "already_clear", upstream_id, revision, server_time}`，
  不返回或修改凭据。即使当前没有冷却，也先校验账号 revision。
- Codex 实验开关关闭时该账号的测试返回固定 `unsupported` 观测，不读取其明文；本地检查不做刷新。
  目录测试只允许共享刷新服务已有的副作用，不另行写入 reauth/verified 状态。

后台真实生成探测不属于本批，不新增生成请求、探测开关或定时器。

### 凭据/目录测试

本批接口：

```text
POST /admin/api/v1/upstreams/{id}/tests
GET  /admin/api/v1/upstreams/{id}/tests/{operation_id}
```

POST 请求严格接受以下字段，拒绝重复键、未知字段、非整数 revision 和不合法 UTF-8：

```json
{
  "operation_id": "UUID",
  "expected_revision": 7,
  "scope": "local_credential"
}
```

第一批接受两个明确 scope：

- `local_credential` 只检查密文能否在本机解密并解析为该 provider 的预期凭据形态，不访问上游。它不能
  表示上游接受凭据、认证成功、目录可读或生成可用。
- `catalog` 可以验证本地凭据、固定 endpoint、上游认证和供应商目录协议，但不发送生成请求。OpenAI 兼容、
  Anthropic API Key 和 Gemini 使用各自固定模型目录协议。

Anthropic 目录依据[官方 List Models](https://platform.claude.com/docs/en/api/models/list)（2026-09-23 查阅）：
使用 `x-api-key` 和 `anthropic-version`，以 `has_more`、`last_id` 和 `after_id` 完成有界分页。
Gemini 使用现有 `nextPageToken` 分页。所有页合计受响应字节、模型数量和同一次超时约束；游标重复或
响应不完整不能记录为 `catalog_ok`。这只是目录协议规格，不改动员工 Messages 的本批实现范围。

Codex 会员目录测试不得使用一分钟目录缓存冒充新观测，也不得实现另一套 OAuth refresh。它可以通过现有
共享凭据获取入口触发正常 refresh，因此该 scope 可能产生已公开的凭据 revision/密文更新副作用；UI 与 API
说明必须明确这一点。测试自身仍不得改变 enabled、cooldown 或绕过现有刷新状态机。

Codex 测试必须使用独立的每账号 test in-flight 协调，不能先持有 refresh/mutation 锁再调用共享凭据获取，
否则会与凭据入口内部取得的同一把锁自锁。记录中的 `requested_revision` 是管理员发起时的 CAS，
`tested_revision` 必须取共享凭据入口实际返回并用于发出目录请求的 revision；发生 refresh 时两者可以不同。
目录请求结束后，再在账号 mutation 锁内重读 credential 来源、provider 和当前 revision，并用 CAS 确认结果
仍属于同一凭据版本。测试期间若发生重新导入、替换或其他 revision 变化，旧 operation 只能终结为 `stale`，
不得写成当前观测、不得覆盖新 revision 的记录，更不能据此写入任何 healthy 状态。

测试结果使用固定枚举 `local_credential_ok`、`catalog_ok`、`authentication_failed`、
`rate_limited`、`unsupported`、`timeout`、`invalid_response`、`configuration_changed`、`stale`、`cancelled`、
`interrupted` 和 `internal_failure`。成功响应必须回显 scope、requested/tested revision、结果码和时间，
UI 必须显示“本地凭据检查”或“目录测试”，不能显示“认证成功”或“生成健康”。

`operation_id` 由调用方生成并稳定重用。相同 operation ID 和相同不可变输入幂等返回已有记录；相同 ID
配不同账号、requested revision 或 scope 返回冲突。测试记录只能从 `pending` 进入一次 `in_progress`，再进入一个终态。
客户端断线或响应丢失时，调用方应以原 operation ID 查询或重试相同请求，服务不得生成新 ID 并再次访问
上游。进程启动时把遗留的 `pending`/`in_progress` 记录标为 `interrupted`；结果不确定时不得自动重发。

终结写入失败后，不能把已无人执行的操作永远作为运行中返回。查询或重复提交时，协调器应在锁内区分
同一个仍活跃的 operation 和遗留记录；遗留记录只做有界的元数据恢复，标记 `interrupted`，不补发上游调用。
如果存储仍不可写，返回固定 503；恢复成功前不得把未保存的结果冒充成功终态。

专用的 `upstream_test_operations` 表包含上述字段、`requested_revision`、nullable `tested_revision`、凭据来源
快照及可选非负 `latency_ms`。上游列表只能投影与当前账号 revision/来源仍匹配的最近完成观测；`stale`
记录可供审计但不能覆盖当前观测。不需要把任意正文复制到 `upstreams`。历史记录必须有明确的数量或时间
保留上限。迁移需要验证表类型、精确列、外键、CHECK、唯一约束和额外敏感列，并测试失败全事务回滚及
修复后重试。

### cooldown 展示

`GET /admin/api/v1/upstreams` 增加两个只读对象：

- `latest_observation`：scope、固定 result code、account revision 和 checked_at；
- `cooldown`：不透明 cooldown event ID、failure class、cooldown_until 和 updated_at。

前端必须按服务端时间展示 cooldown，不自行推断账号已经恢复。不存在记录表示“当前没有持久化 cooldown”，
不等于凭据、目录或生成健康。过期记录的清理仍以运行时契约为准。

### 手工清除 cooldown

本批接口：

```text
POST /admin/api/v1/upstreams/{id}/cooldown/clear
```

请求同时携带两个不同的 CAS 条件：

```json
{
  "expected_revision": 7,
  "expected_cooldown_event_id": "opaque-event-id"
}
```

`expected_revision` 比较账号配置 revision，防止管理员在凭据、启停或 endpoint 已变化后按旧页面操作；
`expected_cooldown_event_id` 比较本次看到的 cooldown 事件，防止清除并发 Release 写入的新失败。两者不能
互相替代。没有 cooldown 时可以返回明确的 `already_clear`；有新事件或账号 revision 改变时返回 409 并
保留新状态。清除 cooldown 不修改账号 revision、enabled、凭据状态或测试观测，也不访问上游。

`updated_at` 和 `cooldown_until` 是展示与调度时间，不是唯一版本。runtime 为每次 cooldown 生成
不透明 event ID，并把数据库提交的同一截止时间传入 scheduler；变更锁串行化 Release、Clear 与最终派发
准入检查。即使时间相同，两个并发失败也不能用时间戳证明是同一个事件，不以时间戳代替安全 CAS。

本批每次有效失败都推进 event ID，即使新失败发生在相同时刻、计算出的期限相同或更短。
截止时间取新旧期限的较大值；较早事件仍决定最长截止时间时，保留其 failure class 作为该期限的原因，
但 event ID 与 updated_at 对应本次失败，旧页面仍必须重新读取后才能清除。

该操作不能只执行一条 SQLite `DELETE`。当前 cooldown 同时存在于数据库、scheduler 内存和已经进入等待的
候选快照中；完整实现至少需要：

1. 以账号 revision 和 cooldown event ID 做事务内 CAS 删除；
2. 让 scheduler 只清除完全相同 event ID 的内存 cooldown，不能覆盖并发 Release 的更新；
3. 提交后推进账号池配置代际并唤醒等待者，使其重新读取候选，而不是继续使用旧 `CooldownUntil`；
4. 验证 Release 在删除前、事务中、提交后以及内存清除前后发生时，较新的 cooldown 始终保留。

数据库提交后的内存清除是无错误的条件操作；进程若在两者之间退出，重启以已提交数据库为准恢复。
最终派发还需在变更锁内复核配置代际和数据库 cooldown 事件，旧候选不能越过已经提交的新冷却。

当前等待中的 Acquire 会保留包含 cooldown 的池快照，`NotifyChanged` 后重新读取可能发现快照变化。
手工清除期间，旧等待请求必须保守终止为固定的 `configuration_changed`/`route_changed`，由客户端重新提交；
不得让它直接沿用清除前的候选继续派发。清除后的新请求可以读取新状态并重新参与调度。

## 后续批次：默认关闭的真实生成恢复探测

真实生成探测会消耗供应商配额并可能产生费用，因此必须由独立配置显式开启；默认配置下不得启动 goroutine、
定时器或任何探测网络请求。系统状态和网页应准确显示功能是否开启，并说明探测会使用真实账号和实际模型。

### 在实现主动探测前必须补齐的状态

当前 cooldown 到期后账号会直接重新进入可调度集合。若恢复策略要求“探测成功后再恢复”，必须新增持久化的
`recovery_required`/`scheduled`/`in_progress`/`interrupted` 状态，并让账号池在成功前继续排除该账号。
仅在后台发一个请求、同时允许员工请求先使用账号，不构成恢复探测协调。

当前 cooldown 保存账号、事件 ID、失败分类和时间，不能确定失败来自哪个公开模型、实际上游模型或协议。生成探测
必须记录触发 cooldown 的 provider、公开模型、实际上游模型和协议快照，或明确选择管理员指定且仍有效的
映射；不得随意挑一个目录模型成功后解除账号级隔离。配置 revision 或映射改变时，旧恢复任务终止为
`configuration_changed`，不得把旧结果应用到新凭据或模型。

后台协调器建议全局最多一个有界 worker，并按账号互斥、限频和退避。运行记录需要稳定 operation ID、
`next_probe_at` 和一次状态迁移；重启把执行中的记录标为 `interrupted` 并按后续时间重新排程，不得因重启
立即重复一个结果不确定的生成调用。关闭服务时取消等待和未派发任务；已经进入执行器的探测只记录取消或
不确定结果，不能换号重放。

### 租约与刷新协调

生成探测必须取得“精确账号”的维护租约，计入该账号的全局最小容量，并与员工请求共享 scheduler 容量。
它不能伪造 employee、Key 或 model policy 来调用现有员工 Acquire，也不能从账号池自动选择另一个账号。
维护租约需要持久化并参与重启恢复；调用 HTTP `Do` 或 Codex executor 前推进 `MayHaveSent`，之后任何连接
错误、超时、HTTP 状态、响应或取消都不得重放。

Codex 探测必须复用现有每账号刷新协调和 `acquireCodexCredential` 语义，不能另起 refresh 请求。刷新后应以
新 revision 取得并复核维护租约；revision 竞争时销毁明文凭据并终止。API Key 探测也必须在账号 mutation
锁与 revision 复核下取得凭据，避免凭据替换和探测交叉使用不同快照。

### 固定生成请求和结果

每个 provider 使用仓库内固定、最小的合成请求和有界响应，不接受管理员自定义 prompt、headers、tools、
URL 或输出长度。探测只针对已经配置的实际模型映射；请求和响应正文不保存、不返回、不记录。

只有同时满足以下条件才可记录 `generation_ok` 并解除对应 recovery 状态：

- 收到协议认可的完整成功终态，而不是 clean EOF、目录响应或部分 SSE；
- 没有客户端/服务关闭取消、超时、截断或未知执行结果；
- 探测用量元数据和最终状态按约定成功持久化；
- 账号、凭据和映射 revision 仍与派发快照一致。

认证、429、overload、协议错误或未知结果使用固定分类延长 cooldown/安排下一次探测，但不得永久停用账号。
手工 clear 与后台恢复状态需要使用同一事件 CAS；管理员清除后，旧 worker 的迟到结果不能重新覆盖新状态。

### 系统探测用量

现有员工用量账本要求真实 employee 和 Key。系统探测不得创建假员工、假 Key、`model_requests` 或普通员工
请求，也不得混入员工请求数、token 或成本汇总。实现前必须选择并写入契约的方案：扩展账本为明确的
`origin=system_probe`，或使用独立探测账本。两种方案都要按实际 account + upstream model 固定价格快照，
使用四个 nullable token 桶，未知用量保持 NULL，成本未知不能当 0，并能单独统计探测消耗。

探测的 attempt、恢复状态和运行结果需要一次原子终结，避免账本失败后把账号标为成功。持久化失败时返回
固定内部错误并维持保守隔离；不得为得到一个“健康”结果而跳过元数据账本。

## 可复用组件与文件边界

实现可以复用但不能直接把 handler 成功等同于健康的组件：

- `internal/service/upstreams.go` 的 `validateEndpoint`、`validateGeminiEndpoint` 和 `newUpstreamClient`；
- `internal/service/model_discovery.go`、`gemini_discovery.go`、`codex_model_discovery.go` 的协议解析思路；
  应抽出返回结构化结果的低层 runner，不能调用写 `ResponseWriter` 的 handler，也不能用 Codex 缓存作为新观测；
- `internal/service/codex_refresh.go` 的账号 mutation lock 和 `acquireCodexCredential`；
- `internal/service/account_pool_runtime.go` 的派发证据、持久化 Release 和 `NotifyChanged`；
- `internal/service/usage_ledger.go` 的定价快照、nullable usage 与规范化累积规则。

建议第一批文件边界：

- 新增 `internal/service/upstream_health.go`、`upstream_health_test.go`，包含迁移、测试 operation 和管理 API；
- `internal/service/upstreams.go` 只扩展列表投影；`app.go` 只注册路由并在 Open 时迁移；
- `internal/service/account_pool_runtime.go` 和 `internal/scheduling/scheduler.go` 只增加 cooldown 查询与条件清除；
- `web/src/api.ts`、`pages/UpstreamsPage.tsx` 及组件测试只展示 scope/结果/cooldown 并发起上述动作；
- 更新本契约、功能矩阵和集成证据，不改四协议员工请求 handler、预检换号或普通用量 hook。

后续主动探测另新增 `account_recovery.go` 及其测试，并按最终账本方案修改 accounting/service usage 文件。
Config、CLI 和系统状态只增加默认关闭的开关及生命周期接线。维护租约若需要新表，应留在账号池 runtime
迁移中并与普通租约共同恢复；不要把 probe 状态塞入 scheduler 纯算法包。

## 自动验收

第一批至少覆盖：本地凭据成功不显示认证成功、四类账号固定目录结果、Codex 缓存不冒充新检查、共享刷新
返回新 revision、测试不预持 refresh 锁、重导入竞态只写 stale、operation 幂等和输入冲突、断线后查询、
重启 interrupted 且零自动重发、响应/日志/表结构无正文和凭据、revision 竞争、cooldown event CAS、
DB/内存截止时间偏差、并发 Release、排队请求保守 route_changed、迁移失败回滚重试、SSRF/重定向拒绝，
以及测试不改变 enabled/cooldown；Codex 仅允许共享生命周期明确记录的 refresh 副作用。

主动探测批次另覆盖：开关关闭时零 goroutine/零网络、精确账号容量、与员工租约竞争、刷新锁和 revision、
固定请求、配额消耗单列、未知 token/cost、部分 SSE/EOF/取消/账本失败不解锁、迟到结果 CAS、重启退避、
无换号重放，以及 catalog 成功永远不能解除 generation recovery 状态。
