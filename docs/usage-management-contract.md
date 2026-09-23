# 用量查询与成本价格管理

2026-09-23，BILL-01/BILL-02/OBS-01 的源码增量。依据本仓账本与权限规格独立实现；
不复制参考产品源码。单管理员、单 Go/SQLite 进程不变，管理查询不进入模型转发链路。
本批不实现员工收费、余额、预算、支付、自动换号或代理；这些保留在完整功能矩阵。
成本是管理员配置价格计算的内部估算，不是供应商账单。

## 价格目录

以 **upstream_id + 实际 upstream_model** 为价格键。账号池切换到不同账号时使用实际
选中账号的价格，不能只按公开模型名称定价。四种提供商共用已有 PriceSnapshot 结构。
无配置或显式停用时 Price=nil，成本保持 NULL。不会内置或抓取真实供应商价格。

版本只能追加，旧版本不可修改或删除；每个键 revision 从 1 开始单调增加。金额以
microcurrency 整数表达，每百万 Token 费率均非负，最大 9007199254740991；不使用浮点数。
价格币种为三个大写英文字母。已开始 attempt 固定保存完整价格快照，新版本不重定价
历史 attempt。调用准备失败、目录查询失败时不能悄悄按免费价格调用上游。

持久化需要版本表、当前键/revision 或等价可并发结构及 operation_id 幂等记录。
迁移整体事务、验证已有 schema、失败回滚/修复重试/重启；保持原有员工 Key、凭据、
OAuth binding 和账本。相同 operation_id 与相同规范请求重放返回原版本且不改变当前
指针；相同 operation_id 改内容返回 409。首次 expected_revision=0，后续必须精确匹配。
停用通过追加 price=null 的版本实现，也需 revision 和 operation_id。

### 管理 API（已有 session，写操作另需 CSRF/Origin）

`GET /admin/api/v1/upstreams/{id}/prices` 返回当前配置：

```json
{"items":[{"upstream_id":"up_x","upstream_model":"model-a","version":"price_x","revision":1,"created_at":"2026-09-23T01:00:00Z","price":{"currency":"USD","input_per_million_micro":"1000000","output_per_million_micro":"2000000","cache_read_per_million_micro":"100000","cache_write_per_million_micro":"1500000"}}]}
```

每次最多 1000 个键；超限明确拒绝而非截断成完整结果。id 不存在返回 404。
`POST` 同路径，返回当前次创建/幂等命中的版本对象：

```json
{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"USD","input_per_million_micro":"1000000","output_per_million_micro":"2000000","cache_read_per_million_micro":"100000","cache_write_per_million_micro":"1500000"}}
```

所有费率 JSON 值都是规范十进制字符串（0 或无前导零正整数），禁止负数、小数、指数。
price 字段必须显式存在，可为 null。expected_revision 必须显式存在，为非负安全整数。
实际模型为 1–256 UTF-8 字节，拒绝控制字符与首尾空白，允许内部空格；不同于公开模型的
128 字节限制。Current 读取兼容已有实际模型（1–256 字节、无 NUL），旧路由无价时仍返回
nil，不能因新增价格能力收紧现有模型请求范围。未知字段、缺字段、重复 JSON 键应拒绝 400。
invalid_request 400、not_found 404、revision_conflict/operation_conflict 409、storage_unavailable 503；
固定脱敏错误。管理员可为停用账号预配置价格，不改变账号启用或模型权限。

## 用量查询 API

均只允许管理员会话；员工 Key 不得读取；不返回任何正文、凭据、任意错误文本或请求头。
所有计数和费率/费用在 JSON 中使用十进制字符串，未知计数/费用用 null，避免 JS 精度丢失。

过滤参数：`from` 与 `to` 为 RFC3339 整秒时间，规范到 UTC，区间 [from,to)，最大31天；
不提供时默认最近24小时，提供时必须成对。可选 employee_id、key_id、model_id、upstream_id、
provider、status。provider 为账本枚举 openai/openai-compatible/anthropic/gemini/codex；status
为 pending/succeeded/failed/cancelled/interrupted。未知/重复参数、过长ID、错误日期返回400。
上游过滤只保留匹配账号的 attempts，requests 用 EXISTS 去重；其他过滤作用于父request，
status 按父request状态过滤。不把一个请求的多个attempt当成多个员工请求。
所有 SQL 参数绑定，查询采用有界 context（最多5秒），汇总在同一只读事务快照内完成。
SQL整数溢出或存储错误固定503，不能悄悄返回部分/四舍五入数字。

`GET /admin/api/v1/usage/summary` 返回：

```json
{"from":"2026-09-22T01:00:00Z","to":"2026-09-23T01:00:00Z","requests":{"total":"2","pending":"0","succeeded":"1","failed":"1","cancelled":"0","interrupted":"0"},"attempts":[{"currency":"USD","total":"1","pending":"0","succeeded":"1","failed":"0","cancelled":"0","interrupted":"0","known_cost_micro":"3","unknown_cost_attempts":"0","input_tokens":{"known_total":"10","unknown_attempts":"0"},"output_tokens":{"known_total":"2","unknown_attempts":"0"},"cache_read_tokens":{"known_total":"0","unknown_attempts":"0"},"cache_write_tokens":{"known_total":"0","unknown_attempts":"0"}}]}
```

attempts 按币种分组，无价格为 UNKNOWN。pending不计入unknown终态计数；同币种已知值与
未知次数分开，不能把未知显示为已知零；不同币种绝不相加。字段语义复用 accounting summaries。

`GET /admin/api/v1/usage/requests` 使用同样过滤，另有 limit=1..100（默认50）与 cursor：

```json
{"items":[{"id":"req_x","employee_id":"emp_x","key_id":"key_x","model_id":"company-a","provider":"codex","status":"succeeded","started_at":"2026-09-23T00:00:00Z","finished_at":"2026-09-23T00:00:01Z","attempt_count":"1"}],"next_cursor":null,"from":"2026-09-22T01:00:00Z","to":"2026-09-23T01:00:00Z"}
```

稳定按 started_at/id 倒序 keyset 分页，正确比较 RFC3339Nano（含整秒与分数位不同长度）。
游标有严格长度/形状限制并绑定规范过滤条件；改变过滤时不能继续用旧cursor。网页翻页要
沿用首屏返回from/to固定窗口，不能每页重算当前时间。游标不授予权限。

`GET /admin/api/v1/usage/requests/{id}/attempts` 返回 `{items:[...]}`，不存在父request为404。
每条字段：id、request_id、account_id、provider、dispatch、status、started_at、finished_at、
price_version（nullable string）、currency（nullable string）、input_tokens、output_tokens、
cache_read_tokens、cache_write_tokens、cost_micro（最后五个nullable十进制string）。最多100项，
超限明确错误。详情用于区分请求与尝试，不暴露上游凭据或模型正文。

## 模型执行接线

App 启动先迁移价格目录，再启用业务路由。`usageLedgerCoordinator` 可获得价格查找器；
请求记住选中账号的实际模型，并在 BeginAttempt 时读取不可变PriceSnapshot。
查找和 BeginAttempt 的线性化点为成功读取当前价格快照；之后管理员改价不影响该attempt。
无价格为nil；查询失败则拒绝本次dispatch。保留已有三表原子终结、取消和重启恢复。
count_tokens仍不写生成费用。价格版本配置不改变员工访问权限。

## 网页与验收

新增“用量与成本”导航。默认最近24小时，可选时间范围、员工、模型、上游、provider、状态；
显示请求状态计数、分币种已知估算成本与未知次数、四类Token及未知次数、分页请求和尝试详情。
不能靠创建假数据填满页面。价格管理同页或独立弹窗：选账号、填实际模型、四类费率、币种；
明确单位。编辑追加版本，显式停用；409保留表单并要求reload，不覆盖后台新版本。
网络结果未确认时保留同一operation_id/原payload重试，或reload核对，不能悄悄重建操作。
切换过滤或详情忽略旧响应；大整数无精度损失；loading/empty/error独立展示；移动端可操作。

验收覆盖：管理员/CSRF/员工Key隔离，revision及operation并发，改价前后在途snapshot不变，
账号池选中价格，无价/停用/部分未知费用NULL，查询失败不dispatch，成本溢出失败，精确日期
边界/分页/过滤一致性，多attempt请求去重，跨币种不混算，迁移失败回滚与重启；真实临时Go+
浏览器配置价格→合成上游请求→查看记录和费用。只轻量CI，不新tag/安装包，不读真实凭据。

分工：价格任务拥有 internal/accounting/pricing*.go 与 internal/service/pricing_admin*.go；
查询任务拥有 internal/service/usage_admin*.go；网页任务仅web；根负责App/协调器接线、
接口契约、进程/浏览器脚本和顶层文档。共享index commit --only 自身文件，不单独push。
