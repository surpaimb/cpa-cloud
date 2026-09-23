# 治理 Shadow 观测契约

状态：待实现草案，2026-09-23。补充[员工请求治理契约](governance-contract.md)与
[治理管理契约](governance-management-contract.md)。本文件只定义管理员只读观测，不表示当前服务、网页或发布包
已经提供 shadow 统计。TPM 与内部成本继续只做 shadow；本批不得据此拒绝员工请求、扣减余额、收费或产生供应商账单。

## 事实来源与归因

观测只关联本服务已经持久化的两类事实：

- `governance_requests` 与 `governance_request_scopes` 保存准入时的 employee、Key、公开模型、协议、
  settings revision，以及每个 employee/key/group scope 的 policy revision、group revision 和阈值快照；
- `accounting_requests` 与 `accounting_attempts` 保存真实派发、四个 usage 桶、不可变价格版本、币种和内部
  `cost_micro`。未进入执行器的候选没有 attempt，不能为它伪造用量或成本。

关联键固定为 request ID。一个真实 attempt 对请求命中的每个 scope 分别贡献一次观测，这是策略归因，不是把
attempt 在总账中重复计费。相同请求的派发前换号仍只有最终真实 attempt；未来存在多个真实 attempt 时，每个
attempt 都分别贡献。`count_tokens` 没有治理生成请求和真实 attempt，始终排除。

治理准入后、路由前终结可以合法地没有 `accounting_requests`；已经创建 accounting request 但尚未派发也可以
没有 attempt。查询必须把这两种情况与损坏关联区分开：terminal 零 attempt 是已证明的零，pending 零 attempt
仍可能派发而保持 unknown。

真实窗口计量的稳定身份固定为：

`scope_kind + scope_id`。

该 scope 在窗口内的总计必须包含所有带有该 scope 的适用历史请求，不受 settings、policy 或 group revision
变化影响。修改策略、治理组或总开关不能清零已有 scope 的窗口计量，也不能只取当前 revision 的子集来判断
`below`。

为了展示历史阈值如何解释同一个稳定 scope 总计，响应可以按准入快照列出解释行。解释行的最小身份为：

`settings_revision + scope_kind + scope_id + policy_id + policy_revision + nullable group_revision`。

每个解释行返回该快照保存的 shadow TPM、shadow cost、currency 和 window，但引用该稳定 scope 的完整窗口总计。
因此同一 scope 的不同 revision 行可以展示不同阈值状态，底层 Token、成本和 unknown counts 必须相同。状态只表示
“这个历史阈值如何解释当前冻结窗口中的 scope 总计”，不是该历史请求当时的 `would_block` 结论。若额外展示
revision contribution，它只能作为构成明细，不能用该子集直接判断 `below`。

管理员之后修改或停用策略、变更治理组成员、关闭总开关、撤销 Key 或停用员工，均不得用当前配置重写历史阈值
身份。组名称、员工名称等当前展示字段可由网页另行读取，但不是观测身份，也不能替代稳定 ID/revision。

若相同解释行键在库中出现不同阈值快照、accounting request 的 employee/Key/model 与 governance request 不一致，
或存在其他违反既有 schema 的关系，查询固定返回存储失败；不得任选一行、按当前策略修补或把异常值当成零。

## 时间口径

TPM 固定窗口为治理 effective time 的半开区间 `(window_end-60s, window_end]`；成本固定为
`(window_end-24h, window_end]`。attempt 归属使用其 governance request 的 `effective_started_at`，不使用客户端时间、
attempt 完成时间或可能回拨的墙钟字符串。长请求因此归入其准入窗口；这是首批稳定口径，后续若改为派发或结算
窗口必须新增版本化契约，不能静默改变历史解释。

首页查询在一个只读事务中读取
`window_end = max(server UTC now, governance_settings.last_effective_admission_at)`。这样系统时钟回拨不会漏掉已写入的
未来 effective time。分页 cursor 固定首页的 `window_end`，后续页不得重新取时钟。首版不接受客户端指定的历史
时间，只提供服务器当前窗口。

`window_end` 只固定准入时间过滤边界，不构成数据库的历史时点快照。晚到的 terminal attempt 可以改变同一个
`window_end` 下的总计；各页也在不同的只读事务中读取。每页另返回 `observed_at`，表示该页实际读取的服务器时间。
新写入的快照身份可能排在已使用 cursor 的 key 之前，因此跨页不保证完整复现某个历史数据库状态；需要最新或
完整结果时，管理员必须从第一页刷新。所有库内时间比较使用固定九位 UTC 排序键或既有 governance 固定九位格式，
不能依赖可变小数 RFC3339 的字典序，也不能用 `julianday` 丢失纳秒边界。

总开关关闭期间的新请求没有 governance scope，因此不会被追溯加入观测。关闭前的历史请求、仍在途的租约和
关闭后完成的 attempt 继续按原快照出现，直至各自查询窗口自然移出。重启不得清空窗口或把 interrupted 改成零用量。

## 已知量、未知量与状态

Token 只在一个 terminal attempt 的 ordinary input、output、cache read、cache write 四个桶全部非 NULL 时已知。
四桶相加必须做有符号 64 位逐步溢出检查；任一桶 NULL 时整个 attempt 进入 `unknown_token_attempts`，不能把已知
部分相加后称为 total。pending attempt 单列为 `pending_attempts`，也使结论保持 unknown。

成本只使用 attempt 在派发时冻结的 `cost_micro + currency`。价格缺失、任一 usage 桶未知或 cost 为 NULL 的
terminal attempt 进入 `unknown_cost_attempts`。已知成本按币种分别汇总；不同币种绝不相加或换算。对配置币种
之外的已知 attempt，返回各币种合计及 `incomparable_currency_attempts`，它们不是零，也不能抵扣配置币种阈值。

每个 TPM 或成本阈值只返回以下三态：

- `exceeded`：配置币种或 Token 的已知合计严格大于快照阈值；即使另有未知值仍是 exceeded；
- `unknown`：已知合计不超过阈值，但存在 pending request/attempt、terminal unknown attempt，或成本存在其他币种；
- `below`：窗口内所有会影响该阈值的请求均已终结且可比较，已知合计不超过阈值。

没有真实 attempt 的 terminal 请求是已证明的零上游用量，计入 `zero_attempt_requests`，不会单独制造 unknown。
尚未终结且没有 attempt 的请求计入 `pending_requests_without_attempt`，因为它以后仍可能派发，结论必须 unknown。
没有任何准入快照的当前策略不生成“below”记录；网页应显示“窗口内无观测”，不能伪造零流量证明。

成功、失败、取消或 interrupted 不决定 known/unknown：只看真实 attempt 是否 terminal 以及四桶/价格是否完整。
失败或取消前已经返回完整 usage 时可为 known；半帧、EOF、取消或重启恢复留下 NULL 时为 unknown。相同终结快照
幂等重试不能重复贡献；终结事务回滚时，新状态对观测不可见。

## 首批 HTTP 面

新增管理员只读接口：

`GET /admin/api/v1/governance/observations`

仅接受以下 query 参数，重复、未知、非法 `%` 转义或未编码分号均为 400：

- `limit=1..100`，默认 50；
- `cursor`，最大 2048 字节；
- 可选 `scope_kind=employee|key|group` 与 `scope_id`，`scope_id` 只能与 kind 同时提供且最大 200 字节；
- 可选 `policy_id`，最大 200 字节。

这些 filters 只选择返回哪些历史阈值解释行。某行的 `scope_totals` 始终覆盖该稳定 scope 在窗口内的全部适用请求；
`policy_id`、revision 或其他展示筛选不得下推成总计过滤条件。

响应形状：

```json
{
  "window_end": "2026-09-23T12:00:00.000000000Z",
  "observed_at": "2026-09-23T12:00:02.000000000Z",
  "tpm_from": "2026-09-23T11:59:00.000000000Z",
  "cost_from": "2026-09-22T12:00:00.000000000Z",
  "items": [{
    "snapshot": {
      "settings_revision": "4",
      "scope_kind": "group",
      "scope_id": "group-id",
      "policy_id": "policy-id",
      "policy_revision": "7",
      "group_revision": "3",
      "shadow_tpm": "120000",
      "shadow_cost_micro": "5000000",
      "shadow_currency": "USD",
      "shadow_window": "rolling_24h"
    },
    "scope_totals": {
      "tpm": {
        "known_tokens": "93000",
        "known_attempts": "12",
        "unknown_token_attempts": "1",
        "pending_attempts": "0",
        "pending_requests_without_attempt": "0",
        "zero_attempt_requests": "2"
      },
      "cost": {
        "known_attempts": "12",
        "unknown_cost_attempts": "1",
        "pending_attempts": "0",
        "pending_requests_without_attempt": "0",
        "zero_attempt_requests": "2",
        "by_currency": [
          {"currency":"EUR","known_cost_micro":"700000","attempts":"2"},
          {"currency":"USD","known_cost_micro":"3100000","attempts":"10"}
        ]
      }
    },
    "interpretation": {
      "tpm_state": "unknown",
      "cost_state": "unknown",
      "incomparable_currency_attempts": "2"
    }
  }],
  "next_cursor": null
}
```

不存在的 TPM 或成本阈值对应 interpretation state 为 JSON `null`，而不是 `below`。`scope_totals` 是
`scope_kind + scope_id` 的完整窗口总计；同一 scope 的多个快照解释行必须引用相同总计，不得按 revision 过滤。
aggregate counts、revision、Token 和金额全部用规范十进制字符串返回，避免浏览器安全整数损失；nullable group
revision 保持 null。数组固定按币种排序。
响应不包含策略名称、员工 Key、Authorization、请求正文、响应、原始错误、上游凭据或数据库路径。

cursor 使用版本、固定 `window_end`、最后一个完整解释行键和规范 filters fingerprint；解释行按
`scope_kind,scope_id,policy_id,policy_revision,group_revision,settings_revision` 升序 keyset 分页。cursor 形状、
filters 或时间不匹配均为 400，不能退回第一页。每页所有 items 必须来自同一个 SQLite 只读事务快照并共用该页的
`observed_at`。keyset 不会重复已经遍历的解释行键，但并发插入排在 cursor 之前的新键可能遗漏，且晚到结算可以使
不同页看到不同的 scope 总计；cursor 不得被描述为跨页数据库快照。

管理员 session 是唯一权限入口，员工 Key 返回 401/403 固定错误。请求 context 上限 5 秒；取消、DB busy、
扫描错误、SQL integer overflow、Go checked-add overflow 或 rows close 错误统一返回固定 503，不能返回部分页。

首批不返回 `would_block_requests`。仅凭终结后的聚合无法证明某个历史请求在其准入瞬间会被 shadow 策略拒绝；
当前 snapshot 的 `exceeded` 也不等于被拒请求数量。若以后需要该指标，必须另行定义逐请求事件时间、窗口前后顺序、
多 scope 去重和 unknown 传播，并用同步幂等投影或有界重放实现，不能把 exceeded snapshot 数量换名返回。

## 聚合实现选择

首批采用现有事实表的实时只读聚合，不新增异步投影：

1. 首页在只读事务中冻结 effective `window_end`，后续页从 cursor 读取同一个边界；
2. 先按窗口和解释行键选择最多 `limit+1` 个不同阈值快照；
3. 对本页涉及的每个唯一 `(scope_kind,scope_id)`，LEFT JOIN 该稳定 scope 在对应 60 秒或 24 小时窗口中的所有
   governance request、accounting request 和真实 attempts；不能按 settings/policy/group revision 限制总计；
4. SQL 分别 SUM 四个已知 Token 桶，再在 Go 中 checked-add，避免 SQL 行内四桶相加溢出或转成浮点；
5. 成本按 currency GROUP BY，SQLite SUM overflow 视为存储错误；在 Go 中把稳定 scope 总计与每个解释行保存的
   历史阈值组合，计算固定三态。

现有 usage summary 只能按 accounting request/attempt 聚合，缺少 policy/group/settings 快照，不能直接作为治理
结果或按当前策略二次归因。实时关联的优点是以 immutable ledger 为单一事实来源，天然继承相同 finish 事务与
幂等性，不产生 projection 已写而账本回滚、漏回填或重复贡献的问题。

若数据量以后使 5 秒查询无法满足，可新增同步幂等投影，但它必须：

- 以 `(attempt_id,scope_kind,scope_id)` 为稳定 scope contribution 唯一键；阈值快照身份另行关联，不能用 revision
  分区后的 contribution 子集计算总计；
- 在 accounting attempt、accounting request、model request 与 governance FinishTx 的同一事务写入；
- 为既有 ledger 提供可验证回填和水位，不能只投影上线后的成功请求；
- 单独表达 pending request 和 zero-attempt terminal request；
- 以原 attempt 的 immutable price/usage 为来源，投影失败必须使整个终结事务回滚。

异步 eventual-consistency 投影不满足首批要求，因为它可能把刚完成的未知 usage 显示为 below。投影也不能成为
第二套计费账本或改变现有 finish 状态机。

## 索引、迁移与边界

最小新增索引为 `governance_requests(effective_started_at,id)`；既有 request-scopes 主键以 request ID 开头，
既有 `governance_request_scopes(scope_kind,scope_id,request_id)` 支持 scope 过滤，既有
`accounting_attempts(request_id,started_at)` 支持从窗口内 governance requests 查 attempt。若查询计划证明需要
policy 过滤，再增加 `(policy_id,policy_revision,request_id)`，不能未经证据复制宽索引。

索引迁移必须与既有治理迁移同事务，精确验证名称、列顺序和非 partial 语义；同名 view、错误列或错误 partial
index 使启动失败并完整回滚，修复后可重试。首批不删除或重写 ledger 行，不回填伪造的 usage。

查询只扫描固定 24 小时最大窗口、每页最多 100 个快照，并受 5 秒 context 限制。不得采样、截断后仍返回 below，
也不得把 overflow 饱和到最大整数。超限时固定 503，管理员可缩小 scope/policy filter 后重试。

## 必须证明的测试

- 精确 60 秒与 24 小时半开边界、整秒与不同纳秒长度、时钟前跳后回拨、cursor 后续页冻结 `window_end`；
- employee、Key、多治理组同时归因；group/policy/settings revision 变化生成历史阈值解释行，但每个稳定 scope 的
  总计包含全部适用 revision，不因编辑清零，也不读取当前阈值倒推；
- 四桶全 known、任一桶 NULL、无价格、其他币种、已知部分已 exceeded 且仍有 unknown 的三态真值表；
- terminal 零 attempt、pending 零 attempt、失败/取消完整 usage、半帧/EOF、重启 interrupted 与保守未知；
- 派发前换号零失败候选、一个真实 attempt；未来多个真实 attempts 各贡献一次但逻辑 request 不重复；
- usage 重复快照与相同 FinishTx 重试不加倍，终结事务任一 sibling 写失败时观测保持旧快照；
- 总开关默认关闭无新行，关闭后旧 in-flight 正常结算、历史仍可查，重新启用不追溯关闭期请求；
- Token 四桶逐步相加、SQL SUM、counts 和成本溢出固定 503，无浮点或饱和；
- 单页只读事务一致；分页 keyset 不重复已遍历键，cursor/filter/window 绑定；构造并发新键排在 cursor 前和晚到
  terminal 时允许遗漏或页间总计变化，并要求刷新第一页；严格 query、limit、ID 长度和 5 秒取消；
- 管理员成功、员工 Key/无 session/错误 Origin 拒绝，响应、错误、日志和数据库不含秘密或正文；
- 新索引的坏 schema、同名 view、迁移回滚和修复后重试；普通旧库、OAuth、账号池及 usage 查询保持可用。

首批完成这些读路径与证明测试后，产品仍只能称为 shadow 观测。任何 hard TPM、hard 成本预算、余额、收费或
汇率功能必须另立派发前可证明预留契约，不能由本接口的历史聚合推断启用。
