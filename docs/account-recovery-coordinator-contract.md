# 生成恢复后台协调器：实施契约

状态：源码已接线，本批故障回归、隔离进程/浏览器与完整 Linux race CI 验收通过；证据见[集成状态](integration-status.md)。本契约承接
`c9dc95f` 的[基础模块](system-probe-foundations-contract.md)，根据本仓独立规格实现。默认关闭，
preview.3 下载版不包含本批功能，合成验收不代表真实提供商或完整 Sub2API 功能对齐。

## 开关、权限与运行期

服务配置增加默认关闭的生成恢复许可；另有默认关闭、带 revision 的持久化管理员设置。
只有两者同时为真才创建一个后台 worker 和它的 timer。管理员设置要求 session、CSRF、Origin
和 expected revision，不能由员工 Key 启用。设置 API 不接受任意探测正文、URL、脚本或凭据。
CLI 许可关闭时启用设置必须明确拒绝，不能返回成功但实际未启用。

关闭时先取消当前工作，等待执行和一次有界元数据结算，再完成关闭；不能持有管理变更锁等待
需要该锁的执行器。关闭不删除历史、恢复隔离或 cooldown。重启也不自动开启许可，不能依赖
浏览器 localStorage 保存安全开关。主进程退出先停止协调器，再关闭账号运行时、刷新器和数据库。

新增 singleton 设置表必须有精确 schema、默认关闭初始化、非法存量拒绝及事务回滚测试。
本批之前的库没有该设置，升级必须保持零生成探测；不能从历史 cooldown 推断缺失的模型路径。

设置变更使用执行器不会获取的专用串行锁。关闭先阻止内存接收新工作，取消并在不持有执行器
所需锁时等待结算，再 CAS 保存 false；存储失败须明确返回并根据实际设置恢复一致运行状态。
开启先 CAS 保存 true，再启动 worker。MarkDispatch 的事务还须重新读取持久 enabled，作为
最后屏障。请求断线后查询实际设置，不推断关闭是否成功；不得并行存在两代 worker。

## 失败快照与冷却事件

自动捕获仅适用于显式账号池。维护探测使用实际失败请求的账号、公开模型、实际上游模型、协议、
provider、凭据来源/client binding、账号 revision 和池 revision；这些值不来自模型目录猜测。
legacy 请求不凭空创建账号池维护租约，但已有账号隔离仍阻止 legacy 使用该账号。

账号租约需要保存实际路由快照。协议在公共 preflight 入口绑定，首选与仅一次安全换号后的新租约
都必须绑定，避免四个 handler 各自遗漏。Codex 请求前刷新可能改变真正使用的账号 revision，
因此“初选 revision”不能冒充“实际执行 revision”；绑定最终路由时再次核对来源与版本。
preflight callback 返回最终 actual route 及错误。公共 preflight 在
初选或备用路径准备成功后统一绑定协议与最终路由；绑定失败必须在 MarkDispatch 前结束，不能
要求各 handler 自行记住回填 Codex 刷新后的版本。
count_tokens 不作为生成失败快照，不为客户端参数错误、存储错误或权限拒绝建立生成恢复任务。

员工 Release 在同一事务内更新 cooldown、恢复隔离及自身租约，使用以下规则：

1. 对可归属当前凭据的有效失败生成新 cooldown event；事务内确认账号 revision、provider 和
   来源/client binding 仍匹配实际执行快照。重导入后的旧请求不得冷却新凭据。
2. 新失败的截止时间更长时，失败类别、截止时间和完整探测路径一起采用新请求快照。
3. 旧截止时间不短于新截止时间时，保留旧类别和完整路径，只推进事件、操作及 recovery revision。
   旧 cooldown 没有匹配恢复快照时，不能拿较短新失败的模型去填充旧原因。
4. 开关关闭且此前没有隔离时，不创建隔离。此前已有隔离时，新事件仍必须与隔离原子推进，
   避免下次启动因锚点失配而失败；不能将关闭开关解释为删除隔离。
5. 同账号不同模型并发结束时，以最终保留的冷却原因选择唯一完整快照。从未有隔离、且开关
   关闭或保留的旧原因没有快照时，允许只提交 cooldown 并正常释放容量。已有隔离，或本次新
   原因应当建立隔离时，关系冲突或写失败不得只提交 cooldown 的一半结果，容量保守保持到 TTL。

锁顺序保持 `lease mutex → cooldown transition → 必要的 Codex mutation → admission → SQLite`。
等待容量、等待 worker 退出均在这些锁外。OAuth 网络刻意持该账号 mutation 锁，维持单次旋转
和重导入互斥，但不得持 lease、cooldown transition、admission 或 SQLite 事务。提交后使用同一数据库快照更新内存，
取消旧事件维护操作并通知等待者；取消不能反向获取 lease mutex。正常修改路由后，旧证据可
保留用于诊断，但不能用它对已经不同的模型路径发起探测。

## 可证明的 Codex 刷新转换

现有普通凭据入口可以返回另一个操作刚保存的凭据，不能仅因版本为 N+1 就更新恢复快照。
恢复专用入口应携带预期账号 revision、来源和 client ID，在账号 mutation 锁内首次读取时就验证：

- OAuth 创建的凭据必须与保存来源、client binding 及当前配置逐字匹配。
- 导入凭据必须没有 OAuth binding，不借用 client ID 刷新。
- 调用前 revision 已变化时，在任何 OAuth 请求前拒绝；不重新读取新版本并默认为本次刷新。

真正发生旋转时，在保存凭据的同一 SQL 事务中执行 adoption hook：以旧账号版本、事件、operation、
recovery revision 和来源作为条件，更新恢复记录到本次旋转的新版本。任一条件失配整笔回滚；
不得先返回转换证明、解锁后再单独改恢复表。提交后返回不含秘密的 from/to revision 证明。
随后维护准入和派发仍重新验证，阻止解锁后发生的重导入。

adoption hook 是严格的 SQL-only callback，只使用传入 `*sql.Tx`，不得获取外层 mutex、另开事务、
调用 NotifyChanged 或操作 scheduler。它仅在同 operation 仍为 required、且尚无维护租约时更新
恢复锚点，不修改 cooldown 表或内存；这是不取得 cooldown transition 的窄例外，依靠完整 CAS
与 SQLite 串行化处理 clear/Release 竞争。通知应在提交并释放 mutation 锁后执行。
若 adoption 递增 recovery revision，返回证明须包含该新值；执行桥据此重建维护请求，后续
Begin 账本使用新账号 revision。不能继续拿调用前的 ExpectedAccountRevision 进入 Acquire。

不增加外层 OAuth 重试。不确定的网络、响应读取或提交结果沿用持久暂停机制；只有明确 429
才沿用现有有界重试，每次尝试都重新验证 guard。不得因恢复任务重试而重放已暂停的 refresh token。

## 单 worker 状态机

worker 同步执行一次一个探测，使用有界扫描、公平游标及变更 epoch，避免忙轮询或被某个容量
长期不足的账号阻塞其他账号。调度等待可取消；无任何可执行项时等待通知或下一个已知时间。
每个 cooldown event 最多三条已持久化 attempt（包括尚未派发的取消或失败），独立历史仍有 10000 条总上限；不得为继续生成而
删除幂等键。每个新尝试冻结当时的价格版本，统计始终独立于员工账本。
需要同一事务的全局/事件计数接口及 recovery event 索引；轮换同时验证旧 attempt 终态、计数
和 state CAS。新的真实失败事件会开启新的事件预算，但仍受全局串行和历史总上限约束。
`created_at` 定义为账号本次连续隔离的起点，事件替换不重置；公平排序使用 next_probe_at/account_id，
不把首次隔离时间当作当前事件时间。

| 持久状态 | 允许动作 | 禁止动作 |
| --- | --- | --- |
| required | 到期且当前快照有效时，以当前 operation 等待容量；Begin 与租约原子提交 | 未获容量/派发标记前调用上游；反复更换未执行的 operation |
| in_progress | 活跃执行继续；若有 receipt，只重试元数据终结 | 以旧 ID 重发网络；生成新 ID 掩盖未决结果 |
| interrupted | 已有终态 attempt、到期且符合重试策略时，在短事务中分配新 UUID、递增 recovery revision | 未确认旧 attempt 终结就重试；成功后恢复旧隔离 |

仅 `rate_limited`、`upstream_unavailable`、`upstream_timeout`、`cancelled`、`interrupted` 可进入
有界下一次探测。认证失败、协议不支持/无效、配置变更、缺失快照、满额和达到次数上限需要人工处理。
不会立即重放未确认请求；重启先中断旧 pending 与 in_progress，再保守恢复维护容量，等待持久化
next_probe_at 及租约到期。receipt 使用原始结果和结束时间，重试不改变费用或调用上游。

准入失败尚未建立 attempt 时不伪造消耗；需要持久化下一次检查时间或可诊断的阻塞状态，避免
每轮扫描同一项。旧事件被手工 clear 或新失败取代后，旧 worker 不得重新建立隔离或 cooldown。
三态表新增有界 attention code/检查时间的持久化字段，绑定相同事件、operation 和 recovery
revision；迁移覆盖旧 schema 的升级和失败回滚。
不能把只存在于进程内的错误缓存当作重启后仍可信的管理状态，也不能把零派发失败混入用量。

持久接口应分别支持同 operation 延后、条件人工阻塞及已终态操作轮换，不滥用总是递增 revision
并要求新 UUID 的现有 helper。扫描索引需与 `(state, attention_code, next_probe_at, account_id)`
查询匹配，并同步扩展严格 schema/index 校验。容量准入采用独立短超时或非等待 Acquire，不能
让繁忙账号消耗整个全局 worker 的执行周期；网络执行仍有独立上限。

提交结果不确定与明确回滚分开处理。若 Commit 实际已完成但返回错误，receipt 重试可能看到
自身维护租约已消失；须通过只读事务核对同 operation 的完整账本终态及对应隔离/cooldown 状态，
证明原原子结算已经完成后才释放内存容量。不能仅凭“lease 不存在”宣称成功，也不补发网络。
无法证明时继续保守阻塞并显示 settlement_pending。本批通过只读结算核对接口处理已提交但
确认丢失的路径；未决账本、错误结束时间或仍存在的旧 cooldown 都不能作为释放容量的证明。

## 管理接口与网页投影

全局只读状态需要分别呈现 CLI 是否许可、持久 enabled/revision、worker 是否运行、下次唤醒、
历史计数/是否满额及 server time，不能合成一个含糊的“已启用”。上游列表需要区分管理员 enabled、
cooldown 是否到期、恢复隔离是否存在及本地/目录/生成观测。

每账号可返回事件、operation、recovery/account/pool revision、模型与协议、next_probe_at、最后
固定结果码/结束时间、尝试次数、是否允许自动重试及固定 attention code。不返回 source client、
凭据绑定细节、Token、正文或上游原始错误。`due` 只表示时间到了，不表示可以绕过权限/版本/容量。
网页暂不提供 retry-now；复用精确 revision/event 的 clear，明确清除不是认证或生成成功证明。
列表投影批量读取 state、当前 operation 结果及事件次数，不按每个账号另行发起数据库查询。

## 验收与分工

- 运行时：失败快照、长短 cooldown 原因一致性、开关关闭后锚点同步、四协议与一次换号、并发
  Release/clear/重导入、写失败全回滚；legacy/count_tokens 边界。
- 凭据任务：guard、同事务 adoption hook、暂停保护；调用前重导入零 OAuth，旋转后重导入零生成，
  hook 冲突回滚、明确 429、导入/来源未知不刷新，以及不含秘密的证明。
  用阻塞 OAuth 与并发 Release/clear/重导入的屏障验证无反向锁和死锁；不能在已经持有 SQLite
  事务后才等待 mutation 锁。
- 后台任务：默认关闭/设置 CAS、单 worker、到期退避/新 UUID、三次封顶、满额停止、receipt 无网络
  重试、无可执行项等待、关闭和崩溃恢复。
- 总协调：共享接口与迁移、App/CLI、管理/网页投影、四协议接线及隔离进程验收。Go fake clock
  与真实临时进程分别验证，不读取真实账号；最新完整 Linux race 通过后才记录该批验收。

普通提交仍只运行轻量 CI，达到可用里程碑再统一安排安装包。

## 已接线的管理入口

- 启动许可：`--allow-account-recovery`（默认 false）；网页入口：“系统状态 → 账号自动恢复”。
- `GET /admin/api/v1/account-recovery`：返回 `cli_allowed`、`enabled`、`setting_revision`、
  `running`、`next_wake_at`、`history_count`、`history_full`、`server_time`。
- `PUT /admin/api/v1/account-recovery`：仅接受 `enabled` 与正整数 `expected_revision`。
  不具备启动许可而启用返回 403，旧 revision 返回 409，无效参数返回 400；存储不确定返回 503，
  调用者应查询实际设置。关闭仍保留历史与隔离。
- `GET /admin/api/v1/account-recovery/accounts`：批量返回隔离快照、时间、尝试次数、固定诊断码。
  同一投影也作为上游列表的 `recovery` 字段返回。`auto_eligible` 不表示已获得容量或正在调用上游。

三个入口都需要管理员会话；PUT 另须 CSRF/Origin。没有手工 retry-now、任意输入或任意目标接口。
升级会在迁移事务中把旧恢复表复制到含诊断字段的新 schema，增加设置单行和查询索引；非法存量失败
会回滚。降级前应停止进程，并恢复升级前**完整数据目录**与匹配版本程序，不把新 schema 直接交旧版本读取。
