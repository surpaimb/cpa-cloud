# 治理管理与准入快照契约

状态：已进入源码集成，管理 API、网页与运行时接线已有隔离验收，完整结果见[集成状态](integration-status.md)。补充 [治理总契约](governance-contract.md)。
来源为 CPA Cloud 自有需求，2026-09-23。首批硬限制只有 RPM、并发；TPM 与成本保存 shadow 配置及准入快照，只读统计由[观测契约](governance-observation-contract.md)定义。
管理配置本身不能推导“将会拦截”或可用余额；默认总开关为关闭。

## HTTP 面

以下路径均在 `/admin/api/v1/governance` 下；需要管理员 session，写入需同源 Origin 与 CSRF。
员工 Key 无权访问。列表仅接受 `limit=1..100`、`after_id`，按 ID 升序，返回 `{items,next_cursor}`。
未知、重复 query 参数或 JSON 字段、非安全整数、错误类型均为 400；正文最大 64 KiB。

| 方法、路径 | 数据 |
| --- | --- |
| GET `/settings` | `{enabled,revision,updated_at}` |
| PUT `/settings` | `{operation_id,expected_revision,enabled}` |
| GET/POST `/groups` | 创建 `{operation_id,name,employee_ids}` |
| GET/PUT `/groups/{id}` | 更新 `{operation_id,expected_revision,name,employee_ids}` |
| GET/POST `/policies` | 创建 `{operation_id,scope_kind,scope_id,enabled,hard,shadow}` |
| GET/PUT `/policies/{id}` | 更新 `{operation_id,expected_revision,enabled,hard,shadow}`；scope 创建后不可改 |
| GET `/operations/{operation_id}` | 查询原管理操作回执，仅元数据 |

`hard={rpm,concurrency}`，两个字段均必填，可以为 null 或 1..9007199254740991 的整数。
`shadow={tpm,cost_micro,currency,window}` 全部必填：tpm 为 null 或上述正整数；cost_micro 为 null 或规范十进制
正整数字符串，最大 9223372036854775807。currency/window 在无 cost 时必须为 null；有 cost 时 currency
为三个大写 ASCII 字母、window 固定 `rolling_24h`。这是币种标识的形状检查，不代表该币种已有定价或汇率支持。
至少配置一个 hard 或 shadow 阈值；所有 shadow 字段均不进入硬拒绝。后续支持其他窗口需另行明确边界。

组 DTO `{id,name,employee_ids,revision,created_at,updated_at}`；员工 ID 排序且唯一。每组最多 1000 员工，
单员工最多加入 64 组，以约束准入快照成本；空组允许。名称 trim 后 1..128 UTF-8 字节，不含控制字符，
按规范化后完整名称唯一，不做隐式大小写或 Unicode 折叠。部门和上游账号组不能隐式建立治理成员。

策略 DTO `{id,scope_kind,scope_id,enabled,hard,shadow,revision,created_at,updated_at}`。
scope 为 `employee|key|group`；每个稳定 scope 最多一个策略。创建或更新必须在同一事务确认目标存在。
停用/撤销主体可保留策略，但运行时仍须重验其授权；策略不会恢复 Key 或扩大模型权限。首批没有删除入口。

## 幂等、版本与审计

所有写操作返回首次提交的固定回执 `{operation_id,resource_kind,resource_id,revision,created_at}`，HTTP 200。
`resource_kind=settings|group|policy`，settings 的 resource_id 固定 `singleton`。
网页收到后读取资源当前状态；回执版本可能已经落后于当前版本，不能伪装为最新对象。

operation_id 为规范 UUID，跨三类操作全局唯一。领域分隔的规范载荷摘要同时绑定 action、目标 ID 和管理员 ID，
只处理无秘密配置；成员排序去重后参与摘要，观察时间不属于用户载荷。重复请求先检查原操作，再检查当前 CAS：
同 ID 同输入返回原回执，不增加 revision 或审计；同 ID 异输入/管理员/action 返回 409 `operation_conflict`。
网络结果未知保留原 ID/输入，可 GET 原回执或明确用同 ID 重试，不自动创建新操作。

revision 为 1..9007199254740991。成功的新更新操作即使字段相同也增加一次 revision；达到上限拒绝且全部回滚。
组名与全量成员替换共享一次 CAS。策略与组 revision 独立，不修改历史 RPM/租约/策略快照。
settings 复用核心唯一表，不能创建第二份总开关。管理有效更新时间取不早于原 created/updated 的服务器 UTC。

独立表 `governance_management_operations` 保存 operation/action/载荷摘要/管理员 ID/资源/首次 revision/时间；
`governance_management_audit` 每操作唯一，保存 actor/action/资源/revision/时间，不保存配置 JSON、员工清单或正文。
资源修改、操作回执、审计在一个事务提交；注入任一失败全部回滚。锁顺序为 admission 写锁 → DB → 提交 → 解锁；
通知必须在锁外。不能仅有 CAS 而声称支持网络幂等。

## 准入服务边界

服务层一个共享入口负责在 admission 读锁及同一 SQLite 事务中重验 employee active、Key 归属/撤销/过期、
公开模型存在/启用及模型权限，读取 settings、员工/Key/所有治理组的已启用策略，构造完整不可变 scope，然后调用
核心 AdmitTx。各 forwarder 不得自己组装策略。不开关或无策略不增加治理行；不能在库读失败时按无策略放行。

group scope 必须另外保存 group revision；employee/key scope 不带该字段，不能把组成员版本借用为 policy revision。
shadow-only 策略同样要保存准入时 policy/shadow/group 快照，以便未来从账本进行历史归因，不能用当前配置倒推。
同 request ID 与原 scope 的任何不可变字段不同均冲突；原版本相同重复准入不续租或重复计数。
请求开始/ObservedAt 来自服务时钟，不能从 HTTP 载荷取得；有效时钟、RPM 窗口及终结/续租仍由核心保证。

最终执行时已经生效的策略快照不因在途管理更新被替换；员工撤销与现有最终派发的重验规则保留。
四协议接线、账号池等待期间续租、关闭取消、账本同事务终结、shadow 统计属于运行时集成，管理表存在不代表它们完成。

## 迁移与验收

groups/members/policies/operations/audit 的 DDL、列、约束、CHECK 字面量、唯一性、普通/partial 索引和外键须严格校验；
多态 scope 和存量成员关系在启动时验证。各成员引用使用 RESTRICT，避免删除员工静默改变组版本。
不可把额外未知表/属性引入为配置。新建表及验证在同一事务，失败不留下部分对象，修复后重试可用。

自动验收包括：默认关闭；三个 scope 的悬空拒绝；成员上限与独立版本；operation 同 ID 跨后续更新返回原回执；
scope/actor/action 不同冲突；版本溢出；资源/receipt/audit 任一失败回滚；旧用户/Key/OAuth 来源保持；
时钟回拨后重启；严格坏 schema 与坏存量拒绝；管理无权限；取消、未知结果恢复和列表分页。
测试只使用临时目录和合成数据。首批不触发安装构建、发布或真实请求。
