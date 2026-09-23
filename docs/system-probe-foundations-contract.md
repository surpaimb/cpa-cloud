# 生成恢复探测的基础模块

状态：本地源码集成验收与 Linux CI（含完整 race）通过。本文描述本批底层实现，不代表自动生成恢复已启用。整个功能仍按
[执行计划](account-recovery-execution-plan.md)推进；后续后台策略、开关、失败快照采集和网页入口现已接线，见[协调器契约](account-recovery-coordinator-contract.md)与最新集成证据。
实现依据本仓独立规格和已记录的公开协议，没有复制参考项目代码。

## 当前可用入口与边界

`GET /admin/api/v1/system-probes/summary` 仅允许管理员会话；员工 Key 不可调用。接口不接受查询参数，
查询最多五秒，返回安装内独立系统探测账本的累计币种汇总：

```json
{"scope":"system_probes","currencies":[]}
```

每项沿用用量汇总的字段：`currency`、`total`、`pending`、`succeeded`、`failed`、`cancelled`、
`interrupted`、`known_cost_micro`、`unknown_cost_attempts` 和四个 token 桶。
计数及金额使用十进制字符串，token 桶包含 `known_total` 和 `unknown_attempts`。
没有员工身份、原始上游错误、提示或响应正文。空数组表示没有探测记录，不表示账号生成可用。

本基础批次原无创建探测的入口；后续已增加默认关闭的启动许可与管理员设置，以及单 worker。两门都开启后才允许自动探测。
系统状态的 `system_probe_accounting` 只表示独立账本已初始化，不表示后台恢复已完成。

## 独立账本与迁移

`system_probe_attempts` 与员工的 `accounting_requests`、`accounting_attempts`、`model_requests`
完全分开，不创建假员工或假 Key。UUID operation ID 表示一次不可重放的探测，绑定账号/恢复事件、
账号和池 revision、公开模型、实际模型、提供商和协议。只保存有界元数据和可空用量。

Begin、MayHaveSent、Finish 和启动中断处理接受调用方 SQL 事务。Begin 在该事务中按账号及实际模型冻结
价格版本；在途改价不改变已开始的记录。四桶不全、缺价或停价时成本为 NULL，不能将未知使用量当成零。
未持久化 MayHaveSent 的 attempt 不能记录 generation_ok 或已消耗 token。记录上限为 10000；满额拒绝
新 operation，旧 ID 查询、幂等完成仍可用，不自动删除幂等键。

启动迁移校验精确 schema、约束、索引、外键和存量格式，拒绝额外秘密字段。各模块使用自己的迁移事务，
不是全库升级的一个大事务。启动会把遗留 pending attempt 和 in_progress 隔离标为 interrupted，并保留
未来重试时间；不会根据这些记录补发请求。维护租约仍按 TTL 保守恢复容量。

## 共享容量与隔离

维护租约只有账号、实际路由和操作版本，不含员工身份。它与员工租约使用同一个账号容量计数，容量仍取
所有启用模型映射的最小配置值。取得容量前后及持久派发时，重新核对精确账号、事件、操作、凭据来源、
模型映射和版本；等待容量不持有数据库事务或凭据锁。跨表重复租约 ID 在启动时拒绝，防止漏算容量。

`account_recovery_states` 的存在始终阻止员工选择该账号，包括旧单路由入口；冷却到期不能绕过隔离。
正常重导入或路由变化允许旧快照继续存在、保持隔离，不能因此使整个服务无法启动。
手工 clear 同时按账号 revision 和事件删除匹配隔离，提交后取消匹配的活动操作并更新内存调度器。
服务关闭会取消探测并等待执行及元数据终结完成，再关闭数据库。

内部单次执行桥把探测账本开始与租约创建、派发标记、结果结算与隔离更新分别放在同一个事务中。
完整成功且当前版本匹配才解除隔离；过期、取消、重导入、清除竞争和提交失败不会误放行。
失败后的 settlement receipt 只允许重试元数据提交，不调用上游；在租约和版本仍有效时可以提交已经确认的
完整成功结果。取消与内部超时分别记录 cancelled、upstream_timeout，不能误记为配置变化；这两种结果仍保留
隔离。租约过期或心跳失败时保守结算，旧 operation 不能再次生成请求。

Codex 使用共享凭据入口，且在维护租约及变更锁之外获取。当前桥不自动采纳 revision 变化，因为该入口
也可能返回并发重导入后的凭据；证明“属于本次刷新”的 revision 转换及后续恢复配置仍待协调器完成。
此限制不会被绕过，也不意味着完整会员恢复已验收。

## 固定协议执行器

内部 runner 支持 Chat Completions、Responses、Anthropic Messages、Gemini generateContent 以及已有
Codex Chat/Responses 执行器。它只使用保存的实际模型和 endpoint，固定合成提示，不接受外部正文/头或
脚本，不重试、不换号，沿用 TLS、SSRF、连接时地址校验和拒绝重定向的客户端。

API Key 请求明确使用非流式 JSON 和 64 个输出 token 的固定上限；该 payload 是能力子集，并不保证所有
兼容模型都接受同一参数。意外 SSE、截断、工具项、错误对象、空正文或非法用量不能产生 generation_ok。
Codex 使用既有原生执行器及其完整 SSE 终态验证，没有新增未核实的 token 上限参数；它仍受执行超时和
响应字节限制。探测的用量未知时保持 NULL。

## 验收与回滚

只使用合成凭据、临时数据库和本机假上游。Go 集成覆盖原子结算、旧版本成功结果失效、派发写入失败零请求、
结束写入失败后保持隔离和禁止重放。`scripts/smoke-recovery-foundations.mjs` 可接收旧版可执行文件，
验证实际数据库升级、重启中断、默认无探测、隔离/clear、维护容量到期及员工 Key 保留。
最终结果见[集成状态](integration-status.md)，不能将这些模拟证据当成真实提供商验收。

旧二进制不认识新增隔离和维护租约。需要降级时应停止当前进程，恢复升级前完整数据目录备份后使用匹配的
旧程序；不能把旧程序直接指向存在恢复隔离的新库。没有自动备份或生产回滚工具的交付声明。

基础模块单独交付时尚无启用配置、失败路径快照、后台调度、Codex 刷新版本承接或网页控制；这些环节
现已由[后台协调器批次](account-recovery-coordinator-contract.md)接线，验收状态以[集成记录](integration-status.md)为准。
该批 receipt 增加“实际提交成功但确认丢失”的在线终态证明；无法证明时保持隔离，不再次调用上游。
崩溃后恢复的未到期租约仍保守保留容量至 TTL；管理员清除隔离只解除策略阻塞，不证明崩溃前的上游
请求已经停止，因此不会提前释放该容量。
