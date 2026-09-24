# ADR 0002：自动备份、版本化密钥托管与恢复演练

状态：Accepted for implementation（F1/F2）

日期：2026-09-24
所有者：F 自动备份与密钥托管组件

## 1. 决策摘要

CPA Cloud 将在现有离线 `cpa-cloud-backup` 密码模式之外增加一条独立的无人值守路径：

- 自动 worker 默认关闭；只有显式启动开关、可用 key provider、成功恢复遗留运行记录且 coordinator
  已启动时，系统才报告 `automated_backups_running=true`。
- 自动计划不保存包密码或 wrapping key。计划只引用版本化 provider；每次运行在 claim 后冻结精确的
  provider ID、kind 和 key version。
- v1 内建 provider 仅为当前 Windows 服务账号的 `windows-dpapi-user`。Linux/macOS 明确返回
  `key_provider_unavailable`，不回退到明文文件、环境变量、命令行参数或数据库旁置密钥。
- provider 返回 32 字节 wrapping key。备份格式 v2 使用 HKDF-SHA-256，以每包随机 salt 做 extract，
  以格式版本、provider ID、kind 和 key version 的规范编码做 info，派生每包 AES-256-GCM key。
- 现有 password+scrypt 格式 v1 和 CLI 行为不变。自动化 API 只创建 v2 包。
- 输出根目录只来自本机运维配置。网页和 API 均不能提交绝对或相对文件路径。
- 每次成功创建后必须先做密码学 verify。计划启用 rehearsal 时，还必须在受限临时目录中恢复并通过完整
  临时 App 打开/迁移/关闭验证，才可记为 `succeeded`；未启用时明确记为 `skipped`，不能宣称恢复就绪。
  发布使用现有同目录临时文件、同步写入和 no-overwrite 原子语义。
- 保留清理只删除本服务数据库记录的、位于已解析输出根内且名称与该 run 精确匹配的普通文件；不扫描
  目录、不跟随链接、不接受 glob。清理失败保留文件并把运行记为带稳定错误码的失败。
- 全局一次只运行一个自动备份。进程关闭先停止 claim，再取消当前运行；启动时遗留 `running` 变为
  `interrupted`，绝不重放该次运行。计划的 `next_run_at` 在 claim 事务中已经推进。

本 ADR 不宣称完成异机灾备。DPAPI user-scope 材料通常与同一 Windows 用户和主机绑定；F3 仍需定义并
验收恢复材料导出、异机导入、双版本解密窗口和重新封装。缺少该流程时 UI 必须显示此限制。

## 2. 信任边界

### 2.1 管理平面

所有新接口都位于 `/admin/api/v1/backups`，复用管理员 session、Origin 和 CSRF 校验。响应只包含 provider
元数据、计划、运行状态和稳定错误码；不得返回 wrapping key、DPAPI blob、AAD、派生 key、主密钥摘要、
员工 Key、上游 Token、提示词或模型响应。

### 2.2 主机平面

集成层向组件传入已经解析的配置：

```go
type BackupAutomationConfig struct {
    Enabled           bool
    DataDir           string
    OutputRoot        string
    ProviderStoreRoot string
    SourceVersion     string
}
```

`OutputRoot` 与 `ProviderStoreRoot` 必须是不同目录；provider store 不能位于 data directory、输出根或将被
打包的文件中。组件对两者做绝对路径、既有目录链、符号链接/reparse point 和身份复核。启动开关建议为
`--automated-backups-enabled`；共享 `Config`、CLI、`App` 接线和能力总表由集成任务维护。

Windows provider store 每个版本只有一个 DPAPI ciphertext 文件和一个非秘密清单。文件采用当前用户保护，
调用 `CryptProtectData`/`CryptUnprotectData` 时设置无 UI 标志，并应用仅当前账号与本机管理员可读的保护
DACL。DPAPI ciphertext 不是可移植备份材料，也不能复制进自动备份包。

## 3. 组件接口

组件只新增同包文件，不直接修改集成任务拥有的 `store.go`、`app.go`、`cmd/cpa-cloud/main.go` 或系统能力
总表。需要集成层各调用一次的窄入口为：

```go
func migrateBackupAutomation(ctx context.Context, db *sql.DB) error
func newBackupAutomationCoordinator(
    ctx context.Context,
    app *App,
    cfg BackupAutomationConfig,
) (*backupAutomationCoordinator, error)
func (a *App) registerBackupAutomationHandlers(mux *http.ServeMux)

func (c *backupAutomationCoordinator) Start() error
func (c *backupAutomationCoordinator) Close()
func (c *backupAutomationCoordinator) Ready() bool
func (c *backupAutomationCoordinator) Running() bool
```

`newBackupAutomationCoordinator` 只验证依赖、恢复遗留状态并构造对象，不启动 goroutine。`Start` 在开关关闭
或 provider 不可用时成功返回但不 claim。`Close` 幂等，顺序为禁止新 claim、取消当前运行、等待退出。

备份核心新增导出接口：

```go
type KeyMaterial struct {
    ProviderID      string
    ProviderKind    string
    ProviderVersion uint64
    WrappingKey     [32]byte
}

func CreateWithKeyMaterial(ctx context.Context, dataDir, output string, material KeyMaterial, sourceVersion string) (Info, error)
func VerifyWithKeyMaterial(ctx context.Context, input string, material KeyMaterial) (Info, error)
func RestoreWithKeyMaterial(ctx context.Context, input, dataDir string, material KeyMaterial) (Info, error)
```

调用返回后 core 和 provider 都清除自己持有的可变临时副本；调用方不得记录 `KeyMaterial`。Go 数组按值复制
无法提供“所有副本已清零”的形式保证，因此 API 文档与代码审阅禁止跨 goroutine 缓存或长期保存该值。

## 4. 包格式 v2

v2 沿用同一个 16-byte package magic、大小上限、SQLite online snapshot、固定两条记录及 payload 校验，
但使用独立且固定的 v2 header layout 与有界 provider reference：

```text
magic[16]
format_version u16 = 2
header_size u16
kdf_id u16 = 1                 # HKDF-SHA-256
provider_id_length u16
provider_kind_length u16
reserved u16 = 0
provider_version u64
ciphertext_length u64
salt[16]
nonce[12]
provider_id[provider_id_length] # ASCII identifier, max 64
provider_kind[...]              # fixed allow-list value, max 48
ciphertext[...]                 # includes 16-byte GCM tag
```

整个 header（包括 provider reference、salt、nonce 和 ciphertext length）是 GCM AAD。规范 HKDF info 为：

```text
"cpa-cloud/backup/v2\x00" ||
u16be(len(provider_id)) || provider_id ||
u16be(len(provider_kind)) || provider_kind ||
u64be(provider_version)
```

解密前必须同时满足：header 尺寸/保留位/长度/总文件长度合法，kind 在 allow-list 中，版本不超过
`9007199254740991`（JSON safe integer），调用方 material 的
provider 三元组与 header 逐字相同。任何错 key、provider/version 不匹配、header/ciphertext 篡改或截断都
返回统一的非敏感认证失败，不区分可用于 oracle 的内部细节。v1 parser 不接受 v2，v2 parser 不猜 v1
字段；v1 parser 读到共同 magic 后若 version=2 只返回 unsupported version，绝不按 v1 layout 解读；v2
parser 同样先校验共同 magic 与 version=2，再解析 v2 layout。公共 Verify/Restore password API 继续只处理 v1。

## 5. provider 状态与版本迁移

provider 状态为 `ready | unavailable | degraded`。`reason_code` 是稳定非秘密值，例如
`unsupported_platform`、`protected_store_missing`、`protected_store_invalid`、`os_protection_failed`。

创建 provider 时生成 version 1 的随机 32 字节 key，先把 DPAPI blob 原子发布到 provider store，之后才在
SQLite 事务中插入 provider 元数据。若数据库提交失败，只允许删除本次调用创建且名称、目录身份和 nonce
均匹配的孤立文件。

轮换采用 prepare/commit：

1. 生成 `active_version+1` 的 key 和 DPAPI blob，原子发布为不可覆盖的新文件。
2. 读回并 unprotect，常量时间比较随机 key，失败则删除该精确新文件，数据库不变。
3. SQLite CAS 更新 `active_version` 与 provider revision；旧版本文件保留用于历史恢复。
4. CAS/提交失败删除精确新文件；成功后清零临时 key。

回滚只允许激活数据库已知且可成功 unprotect 的旧版本。先验证旧版本，再 CAS 切换 active version；不删除
新版本。这样轮换失败和人工 rollback 都不破坏历史包。F1/F2 不提供版本销毁接口。

## 6. 持久化与迁移

表名固定为 `backup_key_providers`、`backup_plans`、`backup_runs`。迁移在一个独立事务内创建并验证表、列、
CHECK、索引和外键；同名不兼容对象使整个事务失败。修复对象后重试必须成功。禁止以
`CREATE TABLE IF NOT EXISTS` 接受伪造 schema。

规范逻辑 schema：

```sql
backup_key_providers(
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK(kind IN ('windows-dpapi-user')),
  scope TEXT NOT NULL CHECK(scope = 'current-user'),
  status TEXT NOT NULL CHECK(status IN ('ready','unavailable','degraded')),
  reason_code TEXT,
  active_version INTEGER NOT NULL CHECK(active_version BETWEEN 1 AND 9007199254740991),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

backup_plans(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  key_provider_id TEXT NOT NULL REFERENCES backup_key_providers(id),
  interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 900 AND 2678400),
  retention_count INTEGER NOT NULL CHECK(retention_count BETWEEN 1 AND 365),
  rehearsal_enabled INTEGER NOT NULL CHECK(rehearsal_enabled IN (0,1)),
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  next_run_at TEXT,
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)

backup_runs(
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL REFERENCES backup_plans(id),
  plan_revision INTEGER NOT NULL CHECK(plan_revision BETWEEN 1 AND 9007199254740991),
  key_provider_id TEXT NOT NULL REFERENCES backup_key_providers(id),
  key_provider_kind TEXT NOT NULL,
  key_provider_version INTEGER NOT NULL CHECK(key_provider_version BETWEEN 1 AND 9007199254740991),
  scheduled_for TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed','cancelled','interrupted')),
  package_name TEXT NOT NULL,
  package_size INTEGER CHECK(package_size >= 0),
  verified_at TEXT,
  rehearsal_status TEXT NOT NULL CHECK(rehearsal_status IN ('pending','succeeded','failed','skipped')),
  rehearsed_at TEXT,
  error_code TEXT,
  created_at TEXT NOT NULL
)
```

必要索引：`backup_plans(enabled,next_run_at,id)`、`backup_runs(plan_id,started_at DESC,id DESC)`、
`backup_runs(status,started_at,id)`，以及仅允许一个 `status='running'` 的 partial unique index。运行历史有界：
每计划保留最近 1000 条记录；删除更老历史前先执行该计划的文件保留规则，无法安全判定归属的记录和文件
都保留并报告错误。

## 7. claim、执行与恢复

claim 在单个 `BEGIN IMMEDIATE` 事务中完成：选择最早到期的 enabled plan，确认 provider ready，冻结 plan
revision 与 provider active version，推进 `next_run_at` 到严格晚于当前时间的下一周期，并插入唯一
`running` run。事务提交后才读取 provider key 和进行磁盘操作。

所有进入 SQLite、JSON 或管理 API 的 version/revision 整数都限制在 `1..9007199254740991`；CAS 自增到上限
时返回稳定冲突，不溢出、不转换为浮点近似。文件名完全由服务生成，并沿用现有 `.cpacb` 扩展名：

```text
cpa-cloud-<plan-id>-<UTC basic timestamp>-<run-id>.cpacb
```

计划 ID 与 run ID 必须通过内部 ID validator；不得把 plan name 用作路径。执行顺序固定为：resolve exact
key version → create → cryptographic verify → optional rehearsal restore → 用关闭所有 worker 的临时配置完整
`Open`/迁移/`Close` → 删除 rehearsal 精确文件/目录 → finalize → retention。`CheckInitialized` 可作为更早的
浅层检查，但不能替代完整 rehearsal。取消在 snapshot、seal、verify、restore 和检查之间传播；已原子发布的
有效包不因晚到取消而被模糊删除，运行结果以实际持久事实终结。

F1 只要求所有包通过 cryptographic verify，允许计划将 rehearsal 明确记为 `skipped`。F2 的“恢复就绪”
声明要求计划启用 rehearsal 且最近运行通过完整临时 App 打开/迁移/关闭；没有该证据时能力仍只是已验证的
加密备份生成，不是恢复就绪。

启动恢复在 worker 启动前把所有遗留 `running` 原子改为 `interrupted`、`finished_at=now`、
`error_code='process_interrupted'`。不创建替代 run，不回退已经推进的 `next_run_at`。若恢复事务失败，App
启动失败。终结写失败时当前运行不得被自动重放；重启恢复会把它标为 interrupted。

## 8. 管理 API

JSON 使用严格未知字段拒绝、稳定错误 envelope、revision CAS 和分页上限：

- `GET|POST /admin/api/v1/backups/key-providers`
- `POST /admin/api/v1/backups/key-providers/{id}/rotate`
- `POST /admin/api/v1/backups/key-providers/{id}/activate-version`
- `GET|POST /admin/api/v1/backups/plans`
- `GET|PATCH|DELETE /admin/api/v1/backups/plans/{id}`
- `GET|POST /admin/api/v1/backups/runs`（POST body 只有 `plan_id` 与 plan `revision`，表示 run-now）
- `GET /admin/api/v1/backups/runs/{id}`
- `POST /admin/api/v1/backups/runs/{id}/cancel`

删除 plan 是软停用：取消尚未派发的本进程任务并禁止新 claim；历史和备份文件不随 API 删除。run-now 与
调度 claim 共用全局互斥和相同冻结/恢复语义。运行禁用时配置 CRUD 可用，run-now 返回稳定
`backup_worker_disabled`。provider 不可用返回 `key_provider_unavailable`。

## 9. 网页与能力降级

网页仅在 `automated_backups_configuration` 为 true 时显示备份设置；旧服务缺字段按 false。页面显示：
provider kind/scope/version/status/reason、计划开关/周期/保留/rehearsal、当前/历史运行、包名/大小、稳定
错误码及 F3 异机恢复限制。页面不显示或提交路径、密钥、DPAPI blob，也不提供“下载 provider secret”。

能力标志语义固定：

- `automated_backups_configuration`: schema 与 API 已接线。
- `backup_key_provider_ready`: 当前平台至少一个 configured provider 可解封 active version。
- `automated_backups_running`: 显式开关、provider ready、恢复成功且 worker 已启动。

## 10. 可执行验收

F1/F2 合并前至少覆盖：

1. v1 password 包回归字节级兼容；v2 round-trip、错 key、错 provider/version、header/ciphertext 损坏和截断。
2. Windows DPAPI user-scope 创建、重启解封、权限、无 UI、轮换、旧版本恢复、激活旧版本及失败回滚。
3. 非 Windows provider 明确 unavailable，worker 不运行且没有 plaintext fallback。
4. 迁移伪造对象整体回滚、修复后重试、重启幂等；旧员工 Key digest、上游 AEAD、OAuth provenance/paused
   state、用量/预算/ledger/tombstone 和 scheduled interrupted 状态保持；version/revision 的 JSON-safe 上限
   在 schema、header、输入校验和 CAS 中一致。
5. claim 原子、全局互斥、并发 run-now、取消、Close、遗留 running 恢复且不重放。
6. 并发 WAL 写入期间 snapshot 一致；发布失败、磁盘满/只读目录、verify/rehearsal/终结失败有稳定状态；
   启用 rehearsal 时覆盖临时 App 完整打开、迁移和关闭，未启用时只能报告 `skipped`。
7. retention 只删除精确 owned 普通文件；路径逃逸、符号链接/reparse point、文件替换和未知文件均拒绝。
8. API session/Origin/CSRF、严格 JSON、CAS、分页、无路径输入和响应/日志秘密扫描。
9. Web 单元测试、typecheck/build，以及桌面和 390×844 浏览器验收。
10. Windows 专项与跨平台 build；Linux CI `-race`。合成测试不冒充异机恢复、云 KMS 或真实生产演练。

## 11. 回滚

关闭 `AutomatedBackupsEnabled` 即停止新 claim，不影响离线 CLI 和正常模型请求。代码回滚保留三张表、v2 包
和 provider version 文件；旧二进制忽略它们且继续支持 v1。不得为回滚删除历史 key version，否则历史包
不可恢复。重新部署新版本时迁移验证现有结构并恢复 interrupted run。

## 12. 独立来源与许可证

本设计来自本仓功能规格与公开文档，没有使用禁止的参考实现作为模板。2026-09-24 核对：

- Microsoft `CryptProtectData`：<https://learn.microsoft.com/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata>
- RFC 5869 HKDF：<https://www.rfc-editor.org/rfc/rfc5869>
- NIST SP 800-38D AES-GCM：<https://csrc.nist.gov/pubs/sp/800/38/d/final>
- Go `golang.org/x/sys/windows` API：<https://pkg.go.dev/golang.org/x/sys/windows>

实现复用现有 `golang.org/x/sys` v0.36.0 与 Go 标准库，不新增第三方依赖；其 BSD-3-Clause 许可证已属于
现有依赖清单。公开文档描述协议和平台行为，不代表 Microsoft、IETF、NIST 或 Go 项目为本产品背书。
