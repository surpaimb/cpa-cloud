# 管理、定时测试与备份批次：独立集成验收计划

状态：集成准备，2026-09-24。基线为 `b24cd12`，包含下一批规格提交
`a0ff209`。本计划只定义本批三项增量的组合验收，不把它们描述为完整功能对齐、
真实供应商认证、通用预算、三家会员、商业系统或完整生产灾备。

依据为 [下一批规格](parity-next-batch-2026-09-24.md)、
[差距审计](sub2api-gap-audit-2026-09-24.md)、
[预算持久接线契约](budget-persistence-integration-contract.md)、
[Codex 生命周期契约](codex-lifecycle-contract.md)、
[账号池运行时契约](account-pool-runtime-contract.md)、
[安全换号契约](account-pool-failover-contract.md)、
[上游健康契约](upstream-health-contract.md)及
[恢复协调器契约](account-recovery-coordinator-contract.md)。

## 隔离与证据规则

- 只使用随机端口、合成凭据和操作系统临时目录；不读取用户数据目录，不连接真实供应商，
  不访问或停止 `127.0.0.1:8787` 的既有服务。
- 每个进程验收阶段获得自己的临时根目录；只清理由该阶段创建且已验证位于系统临时目录下的路径。
  备份输出和恢复目标都必须显式传入，不能使用服务默认目录。
- 先审查源码和迁移，再运行定向测试；定向测试通过后才运行全量 Go、vet、网页测试/构建、
  Linux race 和真实临时进程。相同提交不重复运行完全相同的昂贵测试。
- “测试通过”分为 Go 单元/故障注入、真实本地进程、浏览器、Linux race 四类分别记录。
  合成上游结果不得写成真实提供商验证。
- 日志、API、数据库元数据和加密包扫描均不得出现管理员密码、员工 Key、上游 Key、OAuth token、
  Authorization、请求正文、响应正文或正文摘要。

批次入口为 `node scripts/verify-next-batch.mjs`。它要求显式传入服务、备份 CLI 和网页构建的绝对路径，
按顺序运行账号生命周期、定时测试、备份恢复三个独立 smoke，并为每项设置临时根目录、超时和输出上限。
各 smoke 必须使用 `CPA_CLOUD_ACCEPTANCE_TEMP_ROOT`，不得退回默认数据目录。

## 合并与启动顺序

建议先合并 A（基础 `models`/`upstreams` 生命周期与墓碑），再合并 B（引用 upstream 的计划和运行记录），
最后合并 C（独立 CLI/package）。这只是降低文本冲突的顺序；最终源码仍必须满足以下运行顺序：

1. 基础 store 打开、旧 `upstreams`/`models` 兼容迁移与严格验证；
2. 账号池、冷却、恢复、健康测试、定时计划等引用表迁移；
3. accounting、治理、预算迁移及一次联合恢复；
4. 将遗留健康/定时运行标为 interrupted，完成恢复目录的会话/OAuth/refresh 收敛；
5. 创建运行时，最后才启动刷新、恢复、定时测试等 worker，并置 ready。

迁移任一步失败都必须使 App 不 ready；修复数据库后重试可成功。不得先提交一半基础表重建，
再让后续迁移在新旧混合 schema 上运行。升级保留员工 Key 摘要、OAuth 来源绑定、价格版本、账号池、
用量、治理请求、预算预留/结算和所有 operation receipt。

## 必须共同保持的并发边界

- 普通最终派发沿用既有顺序：`lease mutex → 必要的 cooldown transition → 必要的 Codex mutation
  → admission → SQLite`。预算在最终实际 route/价格/证明冻结后预留；已有 reservation 后不得换号。
- upstream 凭据替换、归档及 Codex 相关版本变更先取得同账号 mutation lock，再取得 admission 写锁，
  在一个事务内 CAS；提交并释放锁后才通知账号池/worker。不得持 SQLite transaction 等待 mutation lock。
- model 修改/归档在 admission 写锁内 CAS。尚未真正派发的请求必须重查 model/upstream 墓碑和 revision；
  已进入执行器的请求可完成并用原冻结预算/价格结算，绝不重放。
- 定时任务 claim、运行记录和 next-run 推进使用短事务；网络调用不持 admission、账号 mutation、
  scheduler/lease mutex 或 SQLite transaction。Codex 目录测试只通过既有共享刷新入口取得凭据。
- 计划编辑/停用/归档、upstream 停用/归档和 App Close 必须取消自己拥有的运行；迟到结果同时比较
  plan revision、run operation 和 upstream revision，不能覆盖新配置或复活墓碑。

## A：账号与模型路由管理阻断项

1. `models` 的 revision/archived 迁移支持旧库、完整回滚和重试；墓碑 ID 永久占位。
   默认列表隐藏墓碑，`include_archived=true` 才投影；员工模型目录和四协议永不投影或派发墓碑。
2. model PATCH 在显式池非空时不得暗改默认 target。归档保留 employee policy、池历史、价格、账本引用，
   但新准入必须失败；在途 attempt 仍以冻结实际账号/模型结算。
3. upstream 归档仅在没有未归档默认 route 或池引用时允许；凭据变为不可恢复，enabled=false，
   OAuth binding/refresh 可执行状态不得再被后台扫描、导入、替换、刷新、目录、手工测试或恢复 worker 使用。
4. A 与 B 组合时，upstream 归档在同一事务停用其未归档计划、增加计划 revision 并清空 `next_run_at`；
   计划及历史保留。提交后取消该 upstream 当前定时运行；迟到结果只允许完成自己已建立的运行历史，
   不得覆盖新 revision 的 `latest_result`，也不能留下 enabled 计划周期性测试墓碑。
5. 所有写入口验证 session、Origin、CSRF、正 expected revision、409 CAS；重复墓碑操作给出可识别结果。
   有界审计只保存 actor/action/target/result/time，不保存配置正文或秘密。

## B：定时测试阻断项

1. `--scheduled-tests-enabled` 默认 false；关闭时可以管理计划，但启动、重启及到期均为零出站。
   status 分开表达 configuration 与 running，旧网页缺字段时降级隐藏。
2. 计划范围只允许 `local_credential|catalog`，间隔 300–86400 秒，最多 100 个活动计划；
   全局最多两个 scheduled run，同 upstream 不重叠，并继续受既有健康测试的同账号/全局并发约束。
3. 到期 claim 与 run row 原子提交后才调用既有健康协调器。目录请求必须沿用 endpoint/SSRF/TLS、
   代理、Codex 刷新、响应大小和分页上限；不得复制一套网络实现。
4. 崩溃把 scheduled running 和对应健康 operation 收敛为 interrupted，不重发原 operation。
   最多补跑一次并从当前时间计算下一次；不能按遗漏次数追赶。
5. 修改、停用、归档和退出取消当前运行；旧 completion 必须因 plan/upstream revision 或 operation 不匹配而失效。
   每计划只保留最近 200 条 completed 历史，running 不被清理；清理和终结同事务失败时无半状态。
6. 进程 smoke 必须先在默认关闭状态把任务置为已到期并证明零调用，再启用 CLI 开关证明只发生一次
   合成目录调用；浏览器覆盖桌面/窄屏 CRUD、历史、错误恢复及零非预期 console/page error。

## C：加密备份与恢复阻断项

1. `create` 不调用 `service.Open`，不触发迁移、恢复或 worker。它从 live WAL 数据库取得 SQLite 一致快照，
   只纳入 `cpa-cloud.db` 快照与 `master.key`；root key 在快照前后逐字核对但不输出内容或摘要。
2. 包头/KDF 参数在分配前做硬上限校验。salt/nonce 随机，scrypt 参数固定在实现可审阅上限内，
   元数据和两项内容均受 AES-GCM 认证。未知版本、重复/多余记录、错误密码、截断和篡改全部拒绝。
3. 输出只创建新文件：同目录受限临时文件、完整 write/sync、无覆盖发布；失败不留下成功命名文件。
   明文快照临时文件的所有出口均清理，但绝不递归清理输入目录、已有目标或解析出的外部路径。
4. `verify` 只读认证、结构和 SQLite integrity，不改源实例。`restore` 只接受不存在的新目录，拒绝根目录、
   symlink/junction/reparse 和任何已有目标；验证、收敛恢复状态后才原子发布目录。
5. 恢复发布前作废管理员 sessions 与未完成 OAuth authorization；`refresh in_progress` 保持暂停语义，
   不重放 refresh token。已完成 OAuth binding、员工 Key 摘要、加密上游、池、价格、账本、治理与预算保留。
6. 完整进程闭环为：活跃 WAL 合成库 → create → verify → 新目录 restore → 真实 App 启动 → 旧管理员
   cookie 失效 → 重新登录 → 原员工 Key 可执行。原目录在成功及所有失败路径前后保持不变。

## 提交级验收顺序

1. `git diff --check`、新增来源注释/第三方许可证核对、链接检查和秘密静态扫描；
2. 三项新增包/handler/迁移/故障注入定向测试；
3. 四协议、Codex 生命周期、账号池/安全换号、恢复、用量、治理和预算回归；
4. `go test -p 1 ./... -count=1`、`go vet -p 1 ./...`、两个 CLI 的跨平台普通构建；
5. `web` typecheck/test/build，桌面与窄屏真实浏览器流程；
6. 三段独立进程 smoke 及组合的归档—定时取消、备份—恢复闭环；
7. Linux `-race` 覆盖 service、membership、scheduling、accounting、egress、governance 和新增 backup package。

轻量 CI 只构建程序和运行合成验收，不创建 tag、安装包或部署。若任一组合阻断项未满足，PR 保持未合并，
并把精确失败、提交 SHA 和可复现命令发回对应实现任务。
