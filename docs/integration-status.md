# 集成状态

## 2026-09-24：显式 Wire 协议路由与跨协议非流式接线

- 集成提交 `8f948a4` 把纯转换模块接入共享 HTTP 执行路径。模型与账号池路由持久化显式
  `wire_protocol`，旧库迁移及旧管理客户端省略字段都保持 `legacy-native`；provider/wire 不匹配、账号池内
  wire 不一致、派发前 revision/wire 改变均失败关闭，不按 provider 猜测或试探端点。
- 合成上游验收覆盖 Chat↔Responses、Messages↔Responses、Gemini↔Responses 六个非流式方向。每个员工请求
  只有一个 parent 和一次实际派发，attempt 记录实际上游协议；原始上游 JSON 先做用量观察再转换。显式跨协议
  SSE 在派发和 attempt 创建前拒绝；state/background/previous/conversation 及 Messages token counting 仍只允许
  各自原生路由。Codex Chat 继续保留既有兼容路径，但其实际 Responses 出站现按 Responses usage/attempt 归因。
- 管理网页可创建、修改和查看 wire，并在账号池逐路由保存；切换 provider 会重置不兼容选择。全量网页
  17 文件/123 项测试、TypeScript 检查和生产构建通过。全仓非缓存 Go 测试通过：service 736.008s，
  governance 135.923s，accounting 85.290s，backup 44.573s，financial 22.175s，两个 CLI 及其余包通过；
  `go vet ./...` 与 `scripts/test-ci-plan.py` 通过。
- 上述只是随机临时目录、本地合成 HTTP 上游和组件测试证据，没有访问真实供应商、真实会员账号或既有 8787
  服务，不证明真实 provider 兼容。Linux race 与 CI 以本批 PR 新 HEAD 的实际结果为准；旧 25 分钟 race
  总命令已因 service 套件增长稳定超时，现保留全部覆盖并把 service 与其他有状态包分组执行。

## 2026-09-24：剩余能力首个可验收截止

- 从 main `19388be` 开始，本批集成默认关闭的加密有状态 Responses 与后台创建/查询/取消、可靠用量事实与更正、selector-aware 通用预算、单实例金额/套餐/充值/兑换/退款流程，以及自动加密备份。实现仍保持员工模型请求鉴权、上游执行和管理面在一个 Go 服务进程内；没有引入多租户、员工 SSO 或管理员密码重置，也未创建发布包、tag 或部署。
- Responses 后台任务使用持久 dispatch barrier，停机/重启把已派发而未完成的任务记为 `interrupted`，不自动重放；资源归员工所有并加密保存。当前不支持托管工具、后台 SSE 游标续传、WebSocket 或完整 Responses 输入面，相关 feature flag 默认关闭。通用预算策略可按 scope、协议和公开模型选择并取最严格结果，但可信请求上界仍只证明固定官方 `gpt-4.1-2025-04-14` 严格子集，不能外推到任意模型、媒体或流式工具回合。
- 财务采用定点整数和追加式流水，覆盖日/月汇总、有界 CSV、套餐 revision 快照、钱包购买/取消、待支付充值、摘要存储兑换码、部分/全额退款及冲正。商业总开关默认关闭；支付仅有本地通用 HMAC callback 契约、五分钟时间窗和事件反重放，没有 Stripe、Airwallex、二维码或其他生产 provider client，不能称为真实支付接入。
- 自动备份默认关闭，限定本地目标根，记录有界历史，支持 verify、保留清理和可选临时目录恢复演练。Windows 当前用户 DPAPI provider 可用且没有明文回退；跨机器恢复材料、Linux/macOS 密钥 provider、对象存储、远程副本和生产轮换/升级回滚仍未交付。
- 独立集成任务在 `1970b8b9cc9f655781506f0ea50b56d055b882bd` 上完成首轮本机回归：`go test ./... -count=1 -timeout 15m` 全部通过（service 690.479s，governance 134.732s，accounting 85.808s，backup 44.480s，financial 18.993s，两个 CLI 及其余包均通过），`go vet ./...` 通过；网页 TypeScript、17 文件/122 项测试和生产构建通过。最终程序通过 Responses 同步 smoke，以及生命周期、定时任务和加密备份组合进程 smoke；只使用临时目录、随机回环端口和合成凭据，未访问 8787 或真实提供商。
- 协调补验在 service HTTP/持久层级逐项通过：有状态 Responses 所有权/previous/delete，后台 create→poll→complete、queued/in-progress cancel、accounting 原子创建和重启 `interrupted` 不重放；通用预算 selector 匹配、命中最严格窗口、缺上界/超额派发前拒绝、已知与 unknown usage 独立窗口；自动备份 API、worker 实际 create→verify→临时副本 rehearsal→保留/历史、互斥/取消及 Windows DPAPI 版本生命周期。它们是定向 Go 测试证据，不冒充额外进程或真实 provider 验证。
- Playwright 首轮桌面与 390×844 移动验收覆盖 selector-aware 通用预算和自动备份的入口、默认关闭/未 ready/空历史状态、表单、导航与窄屏布局，两页均无横向溢出或可见控件截断。随后在真实 Windows 临时服务正向验收中创建 DPAPI 受保护密钥、保存启用且每次演练的计划、执行真实 worker，页面与磁盘均出现约 1.1 MiB 的加密包，历史显示“校验通过/恢复演练通过”；同一页面在进程重启后可重读密钥和计划。另创建员工、保存 `openai-responses`/1000 Token 的 strict selector，并从网页启用治理与预算。一次故意填写尚未配置的模型 selector 得到预期 409；除此以外只有认证前 session 探测的预期 401，没有页面异常。
- 上述正向 UI 首轮发现并修复两项生产单连接路径缺陷：随机备份 provider ID 含大写字符而被受保护存储拒绝，以及创建首条密钥/计划后列表在仍持有 rows 时嵌套查询导致永久等待。`178dad060d124a5570c3013c1652462cf23a9b55` 改用受保护存储允许的小写字母表，并在解封/查询最新运行前完整读取且关闭 rows；增加 100 次字母表检查和创建后两列表的两秒超时回归。修复后再次执行 `go test ./... -count=1 -timeout 15m` 全部通过（service 666.243s、accounting 84.929s、backup 46.540s、governance 132.680s、financial 19.347s，两个 CLI 及其余包均通过），全仓 vet 通过。所有浏览器、服务、备份包、DPAPI 测试材料和临时二进制均已清理。商业流程目前没有网页入口，本批只按 API/service 测试验收。
- 真实客户端隔离矩阵已有 Codex/Gemini 文本、工具和取消通过；Claude 官方客户端的严格取消场景失败：客户端发出了两个不同的员工请求，每次各一次，上游均收到取消且没有遗留 socket，因此不能把它描述为透明兼容。Claude/Gemini 会员授权及三家真实 provider/会员端到端仍受协议和凭据条件阻塞；API Key 能力不等于会员能力。Linux `-race` 和完整轻量 CI 结果以本批 PR 的实际运行记录为准，在结果出现前不借用旧 SHA 的 CI 证据。

## 2026-09-24：生命周期、持久定时测试与加密备份本地组合验收

- 账号/模型墓碑与 revision、默认关闭的定时凭据/目录测试、独立 `cpa-cloud-backup` 已按 A→B→C 顺序合入集成分支。调度运行期直接要求 `upstreams.archived=0`，缺字段或 SQL 错误失败关闭；上游归档在同一事务停用相关计划、增加 revision、清空 `next_run_at`，提交后取消本进程拥有的运行。
- 恢复会作废管理员及未完成 OAuth 会话、暂停不确定 refresh；恢复后的 App 启动把遗留 scheduled running 标为 `interrupted` 且不重放。员工 Key 摘要、OAuth binding、上游墓碑、价格/账本/治理/预算保留。备份只恢复到新目录，首批数据库硬上限 128 MiB。
- 独立集成任务最终串行 `go test -p 1 ./... -count=1 -timeout=15m` 通过：service 490.520s，backup 11.300s，accounting 17.125s，governance 40.583s，其余包及两个 CLI 均通过；`go vet -p 1 ./...` 通过。Windows/Linux/macOS amd64 的服务和备份 CLI 普通构建通过。Windows 本机 `CGO_ENABLED=0`，不声称本地 race 通过。
- 网页 typecheck、15 文件/116 项测试和生产构建通过。真实 Playwright 桌面与 390×844 验收覆盖上游生命周期入口、定时计划/历史、窄屏导航；body 无横向溢出，认证后控制台无错误/警告。三段独立进程 smoke 通过：归档后零派发；scheduler 默认关闭零调用、开启一次目录调用、重启不重放；加密 create/verify、错误密码/篡改拒绝、新目录 restore、旧会话失效、员工 Key 连续及源 durable 文件不变。
- 全部证据只使用临时目录、随机端口和合成凭据/上游，未访问既有 8787 服务或真实提供商。本批实现代码源为 `a5fffb31f4cad079a30fd1ead3827373847d53d3`；[Code validation 35954728428](https://github.com/surpaimb/cpa-cloud/actions/runs/35954728428) 已对该 SHA 完整通过：全量 Go、Linux `CGO_ENABLED=1` race、vet、服务/备份 CLI 构建及全部隔离进程 smoke 成功，web 的依赖锁定安装、typecheck、测试和生产构建成功，平台安装矩阵按路径规划跳过。其后的功能矩阵/差距复核收尾仅修改文档，代码树保持与该 SHA 相同。本批不创建 tag、安装包或部署，也不代表完整功能对齐。

## 2026-09-24：预算预留、结算与请求执行集成

- `ac4d820` 已集成持久预留、最终派发前确认、冻结价格、预算/账本/治理联合终结、严格迁移和启动恢复，
  管理端开关与策略网页同时就绪。默认关闭；只有治理和预算开关同时开启并命中 `deny_unknown` hard 策略才启用。
- 首个证明仅覆盖官方 `gpt-4.1-2025-04-14` 的严格文本、非流式请求；不把其他模型、SSE、会员、工具或媒体视为已获上界证明。
  两个提交阶段结果不确定均不派发，结算不确定按原 ID 核对而不重发；未知 Token/成本分别保留上界，超界持久隔离 profile。
- 根非缓存全量 Go、全项目 vet、网页 109 项测试/typecheck/build 及增强治理进程 smoke 通过；真实 Go + Playwright
  桌面 1440×1000 和手机 390×844 创建/编辑/重载/开关通过，超大成本字符串原样保留，0 页面异常。
  只用合成数据，临时服务/浏览器/数据均清理。详细证据见[预算集成进度](budget-service-integration-progress.md)。
- [Code validation 35927456340](https://github.com/surpaimb/cpa-cloud/actions/runs/35927456340) 全部通过。
  根读取 core `107405872942` 实际日志：普通 service 132.048s，race service 1323.013s、membership 1.870s、
  scheduling 1.086s、accounting 59.363s、egress 1.319s、governance 8.858s 全 PASS；vet/build 与六组隔离进程 smoke 全 PASS。
  web `107405872936` 的 typecheck、13 文件/109 测试及构建通过，三平台安装全部 skipped。
- [PR #1](https://github.com/surpaimb/cpa-cloud/pull/1) 已合入 main `d95b6a7`；合并后的程序与该 CI 验证的 `ac4d820` 一致。
  文档另行同步，preview.3 下载不含此增量，没有真实提供商兼容验证。

## 2026-09-24：预算事务基础（早期记录，后续接线见上文）

- 分支 `codex/budget-integration` 保存 caller-owned `BeginAttemptTx`、accounting/governance `RecoverInterruptedTx`、
  原始不可变价格的四桶/互斥输入组上界算术；根只读审查和修订三输入桶之和的 proof 条件，并补两包真实 SQLite
  共同提交/回滚测试。来源为本仓契约，没有新增第三方依赖、参考源码或真实凭据。
- 根独立非缓存 accounting 全包 17.271s、governance 全包 26.916s、service 用量账本/治理终结/取消/重启专项 7.868s
  均 PASS；三包 vet、实际 Go 编译及最终二进制上的观测进程 smoke PASS。详细证据见
  [算术与事务基础](budget-accounting-foundations.md)及[恢复基础](budget-recovery-foundations.md)。
- [下一批集成契约](budget-persistence-integration-contract.md)已按自有源码复核实际锁顺序、两个结算维度、严格升级、
  提交结果不确定、未知用量内部归因窗口和持久化超界隔离。它是实现输入，不是实现证据。
- 尚无 reservation 表、生产 bound profile、运行时预算拒绝或 App 联合恢复；该分支尚未运行自身 Linux race。
  不使用下方主线观测 CI 冒充本批验证，不新增 tag/安装包或自动部署。

## 2026-09-23：治理用量观测集成（轻量 CI 已通过）

- 按[只读观测契约](governance-observation-contract.md)独立实现核心 `7e015dc`、管理员 HTTP `1ff03c2`、网页 `e328acf`。根任务挂 App 路由并审查签名 cursor、稳定 scope 跨 revision 汇总、半开时间窗、整数溢出和索引迁移；不启用 TPM/成本硬拒绝，不新增收费或正文记录。
- 审查发现合法的“父请求 pending、已有 terminal attempt”会遗漏不确定性，修复 `4b35556` 增加两窗口 `pending_requests`，每个父请求只计一次。known usage 保留；有待完成父请求时不能返回 below，已知值严格超过阈值仍 exceeded。真实 Ledger 多 attempt 回归覆盖父请求终结前后变化；网页 `596ada8` 同步计数并明确读取失败时保留的是上次成功结果。
- 根独立非缓存专项 `Test(QueryObservations|Observation|GovernanceObservation)` PASS：governance 13.194s、service 1.755s。最终网页 TypeScript、治理相关 14 项测试和生产构建 PASS；子任务完整网页 106 项测试 PASS。根全量 Go/vet 与本批 Linux race 另列最终结果，不能复用下方不含观测的 CI。
- 根全仓非缓存 Go 与全仓 vet PASS：cmd 2.310s、accounting 14.666s、egress 1.659s、governance 21.566s、membership 0.394s、scheduling 0.148s、service 429.810s。该轮已经包含 pending 父请求修复及最终网页接线；随后发现的存量父子终态问题另做增量复验。
- 存量表可被写成 accounting succeeded 零成功 attempt，或 terminal 父仍有 pending child；表级约束不足以证明其合法。修复 `9326187` 在每请求汇总终结时验证 Ledger 同样的父子约束，固定 `ObservationSchema`，避免错误的零用量或宽松 unknown。回归先证明真实 Ledger 拒绝该状态，再注入坏库值；合法 failed 零 attempt、无 accounting 父的治理终结与 pending 父已有 terminal 子均保留。
- 根在最终修复后独立专项再次 PASS：governance 16.301s、service 1.568s，两包 vet、最终程序编译与观察进程 smoke 全 PASS。138 个相对文档链接和 6 个 CI 路径测试通过；双语 PowerShell 块与 `f55774d` 完全一致。本轮路径只选择 core/web，实际 Linux CI 结果如下，三平台安装均已跳过。
- 根新建隔离 Go 程序，实际运行 `scripts/smoke-governance-observations.mjs` 两种模式均 PASS：使用旧 `dist/governance-test.exe` 创建治理账本再升级，以及 CI 同版本新库模式。覆盖默认关闭不追溯、pending/revision 变化、稳定 scope 总计、unknown、多币种、派发时不可变价格、分页/HMAC 篡改拒绝、重启 cursor、管理员隔离、Key 撤销。最终每 scope known Token 600、unknown attempt 1；USD 748 micro、EUR 187 micro；不同历史阈值解释同一完整总计。临时库/日志未出现合成秘密或正文，测试进程和数据已清理。
- 子任务在根最终程序和 `web/dist` 上运行 `scripts/smoke-governance-observations-ui.cjs` PASS：随机端口与合成上游，3 次真实 HTTP 派发；21 条历史快照实际分页 20+1；三种 scope/kind-only 筛选、刷新首屏与空结果均通过。known Token 300、unknown attempt 1，EUR/USD 各 187 micro；0 页面异常、0 console error、无横向溢出，DOM/storage/logs 无合成密码/Key/正文。根查看桌面及手机截图，路径 `C:/Users/apple/AppData/Local/Temp/cpac-governance-observations-ui-20260923-231526/`，同目录 `result.json` 保存统计。未访问真实 8787 服务或真实上游凭据。
- 新增只读索引与现有治理迁移同事务；专项证明旧历史保留、同名 view/错误列序/partial index 拒绝并回滚、修复重试。每页只读事务；cursor 冻结窗口而非数据库历史快照，晚到结算与新快照必须刷新第一页才能重新取得完整结果。
- 最终源码 `60ea6f663b76350e4ac7bc7aaca9d97369761d37` 的 [Code validation 35881635695](https://github.com/surpaimb/cpa-cloud/actions/runs/35881635695) 已全部成功。根读取 core job `107251327905` 实际日志：普通 Linux service 116.868s；完整 race 的 service 1122.106s、membership 1.930s、scheduling 1.088s、accounting 52.858s、egress 1.311s、governance 4.747s 全 PASS；vet、编译及治理、治理观测、代理、上游健康、恢复基础、自动恢复六组隔离进程 smoke 均 PASS。web job `107251327829` 的 TypeScript、13 文件/106 项测试与生产构建通过；Windows/Linux/macOS 安装 job 全部 skipped。独立预算分支未包含在此证据范围内。
- 硬预算只有[待实现提案](budget-admission-proposal.md)，不能把 shadow 聚合当作派发前预留。下载版仍为 preview.3，本轮不创建 tag、安装包或部署。

## 2026-09-23：员工请求治理源码集成（轻量 CI 已通过）

- 原 GPT-5.6 Sol 任务交付独立治理核心、management `4066d18`、网页 `6650528`/`f9d4063` 及运行时修正 `5be1506`。根集成 App、四协议准入、用量四表原子终结和启动恢复。总开关持久默认关闭；硬限制只有 employee/key/group 的 RPM 与并发。TPM/成本目前只有配置和历史准入快照，没有观测统计或预算拒绝；不能把整项 LIMIT-01 标为完成。
- 根审查补组 revision/全量成员同事务读取、更新策略同事务重验多态 scope，避免并发读出混合版本或修改悬空策略。两项新专项 PASS（3.032s）。管理操作的主体配置、全局 UUID 回执与无正文审计同事务提交；慢请求正文和慢响应不持有 admission 锁。根最终管理/续租/取消/元数据组合测试非缓存 PASS（19.958s）。
- 终结声明早于冻结首个账本快照，续租在锁外停止并等待；续租失败、Close 或客户取消先赢时，不提交成功，也不让失败的 success 尝试阻止 cancelled 清理。终结先赢后按冻结时间/status 有界重试；四表任一失败全部回滚，租约不提前释放。错误 X-CPA-Session 在四协议治理前拒绝，零 RPM/请求/attempt/网络。
- 根构建真实 Go 程序并运行 `scripts/smoke-governance.mjs` 两种模式均 PASS：旧 `egress-final-test.exe` 创建没有治理表的库后升级，以及 CI 新库模式。验证原员工 Key 保留、默认关闭/开启、RPM/并发拒绝零派发、幂等/原回执/旧 revision 冲突、员工/Key/组策略、成本大整数字符串、shadow 不阻断、取消/释放、关闭后重启、撤销和数据库/日志秘密隔离。仅回环模拟上游，临时程序/数据均清理。
- 网页根独立 TypeScript、100 项测试和生产构建 PASS。子任务 `scripts/smoke-governance-ui.cjs` 在真实 Go 临时实例通过：已提交后丢响应只发一次组 POST；发送前失败的 Key 策略原回执 404 保持未知、同 UUID/内容重试；三个 scope、大整数成本、真实 r1→r2 冲突确认后 r3、最终关闭、0 页面异常。根已查看桌面与移动截图：`C:/Users/apple/.codex/visualizations/2026/09/22/01a0c7c1-71ab-7842-b70a-4558fc3360c8/governance-ui/`。完整四协议 HTTP 交叉验收与全量 Go 结果见下文；本批治理 Linux race 待实际 CI，不能复用不含治理代码的上一批结果。

根完整非缓存 `go test -p 1 ./... -count=1 -timeout=10m` 及全仓 `go vet -p 1 ./...` 已通过：service 412.398s、accounting 18.905s、egress 0.820s、governance 8.309s、membership 0.385s、scheduling 0.139s。该轮包含运行时 `43326a5`；稍后新增的四协议/Codex 交叉测试另列专项结果。121 个相对文档链接、6 项 CI 路径测试通过，中英文 PowerShell 命令与 `14be3c6` 完全一致。本机没有 CGO 工具链，Linux race 仍以真实 CI 结果为准。

- 四协议 HTTP 测试 `0db1a51` 与 Codex 专项 `d538db4` 已由根任务审查合入，根独立非缓存运行 `TestGovernance(HTTP|Codex)` PASS（19.921s），service vet PASS。覆盖 JSON/SSE 成功、employee/key/group 共享 RPM/并发、429 零派发、失败事件、取消传递与四表 cancelled、撤销、count_tokens 不占用，以及终结故障四表原子回滚后按同一冻结快照结算。Codex 首个 Chat 请求实际执行合成 Token 刷新 revision 1→2；四条 Chat/Responses JSON/SSE 请求共享计数，刷新后没有多建治理父请求，第五条 429 无执行器、刷新或 attempt 增量。

- 已推源码 `f55774d905527c098bfbb64176c1fe4fb09ef3cc` 的 [CI 35875922315](https://github.com/surpaimb/cpa-cloud/actions/runs/35875922315) 全部成功。根读取实际 core job `107231710807` 日志：普通 service 117.959s；完整 race service 1156.308s、membership 1.733s、scheduling 1.085s、accounting 51.050s、egress 1.334s、governance 2.372s 全 PASS；vet、编译与治理/代理/健康/恢复基础/自动恢复五组隔离进程 smoke 全 PASS。网页 job `107231710723` 的 TypeScript、100 项测试和生产构建通过。Windows/Linux/macOS 安装 job 均 skipped。本轮仍只有源码，未改 preview.3；后续观测聚合及网页不包含在该 CI 中。

### 出站代理 CI 修复结果

- `460b133` 的 [CI 35870865294](https://github.com/surpaimb/cpa-cloud/actions/runs/35870865294) 中网页通过、普通 Linux service 111.765s 通过，egress 在测试代理隧道清理超时。原因是测试夹具仅关闭客户端侧，反向 copy 仍等待持久上游连接；生产 transport 没有同类双连接 relay。
- `14be3c6` 仅修测试夹具：任一 copy 结束都关闭两端并等待另一方向，补双端关闭回归。子任务原场景及新用例连续 100 次通过、整个 egress 测试/vet 通过；根整个 egress 非缓存 5 次 PASS（1.637s）。已推 main，[CI 35872618889](https://github.com/surpaimb/cpa-cloud/actions/runs/35872618889) 全部成功。根读取实际 core job 日志：普通 service 112.795s；完整 race service 1089.602s、membership 1.856s、scheduling 1.089s、accounting 56.440s、egress 1.354s 全部通过，随后 vet、编译及代理存储、目录健康、恢复基础、自动恢复四组进程 smoke 全部 PASS。web 和三个安装 job skipped。该轮不含治理，治理仍需新 CI；没有提高超时、跳过测试或触发安装打包。

## 2026-09-23：API Key 出站代理与持久握手检查

- 首批按本仓[代理契约](outbound-proxy-contract.md)、[管理接口](outbound-proxy-admin-contract.md)和[握手契约](outbound-proxy-test-contract.md)独立实现。覆盖 HTTPS CONNECT、代理独立 AEAD 凭据、管理/连接 revision、绑定账号版本原子递增、管理员网页、四协议生成/目录/测试/恢复的共享出口。根任务负责最终派发屏障、Codex 变更锁兼容、取消记账及 App 生命周期；GPT-5.6 Sol 子任务分别负责组件与专项测试。
- 两层 TLS 均验证，CONNECT 使用本地校验的目标字面地址，代理认证不进入模型头，不读取环境代理。绑定失效、停用、坏密文/证书都不自动直连；预检失败仅沿用原账号池一次安全换号，最终派发后不重放。目录每页重查同一版本；Codex 暂拒绝绑定，仍保留原刷新和请求行为。
- `outbound_proxy_http_integration_test.go` 使用真实 App/HTTP handler 与进程内合成双层 TLS：四协议 JSON/SSE、count_tokens、三类分页目录/目录健康检查、实际恢复执行、凭据隔离、失效出口拒绝、在途停用仍用原连接。根独立执行相关 HTTP/最终派发/取消测试 PASS（7.276s），另独立运行实际刷新竞争与 Chat/Responses 刷新前派发回归 PASS（16.871s）。没有真实供应商凭据或调用。
- 握手 `ce0089b`、修订 `340458b`/`5dcd7dc`，根接线 `888ea51`。持久操作最多四活动/每代理一项/一万历史，重复 UUID 不重复连接；只做 CONNECT/目标 TLS、零目标 HTTP。完成配置变化、提交响应丢失、固定结算、Close 等实际 worker、启动 interrupted 均有专项。根初版独立专项 PASS（10.793s），修订子任务全组 PASS（11.045s）；修订纳入下面全量回归。Gemini 生产握手目标再次固定官方 origin，不能靠数据库地址漂移调用其他主机。
- 网页根独立执行 TypeScript、92 项测试和生产构建，均 PASS。子任务用真实隔离 Go 程序 + Playwright 验收创建响应丢失/同号恢复、PATCH/绑定核对、认证替换清除、停用保留绑定和主动解绑；握手 POST 响应丢失后只有一次提交/一次代理连接，查询原操作得到坏证书的固定 `internal_failure`，目标 HTTP 为零、页面异常为零，DOM/storage/logs 未见合成秘密。根已查看桌面、手机和握手结果截图：`C:/Users/apple/.codex/visualizations/2026/09/22/01a0c7c1-71ab-7842-b70a-4558fc3360c8/outbound-proxy-handshake/`。浏览器没有验证真实公网代理或成功证书；成功双 TLS 由 Go 子进程测试验证。
- 根完整 `go test -p 1 ./...` 首轮只有旧 Codex 迁移夹具失败：它直接创建 App，缺少新出口/授权协调器，不是 Open 启动路径。补齐同样的初始化和失败路径清理，未放宽产品校验；原迁移/回滚/重开/旧 Key 真实 HTTP 测试独立 PASS（2.071s）。其他包及 service 其余测试在该轮通过；最终完整组合交给 Linux CI 复验，不能把这次带失败的首轮称为全量成功。最终全仓 vet 与 Go 编译 PASS。
- 新 `scripts/smoke-outbound-proxy.mjs` 在根构建程序上实际 PASS：先用旧 `dist/recovery-smoke.exe` 创建不含代理表的数据库与员工 Key，升级后 Key/模型保留、创建幂等/冲突、CSRF/员工拒绝、加密落盘、绑定版本、停用不解绑、主动解绑及重启 pending→interrupted/原号查询零重连。另按 CI 无旧二进制模式实际 PASS。首轮合成夹具使用不可解析域名、随后非规范纳秒时间而被严格拒绝，已修成仅回环目标和规范时间；未修改验证规则。临时程序/数据均清理。
- 轻量 CI 已加入 egress race 和上述进程 smoke；本机没有 C 编译器，不宣称 Windows race 通过，Linux 结果待本批实际运行。不新建 tag/安装包，preview.3 保持不变。HTTP/SOCKS、自动代理轮换、出口 IP、Codex 全生命周期出口仍待实现；治理在独立分支集成，不属于本批完成范围。

## 2026-09-23：默认关闭的账号生成恢复协调器

- 本批按[协调器契约](account-recovery-coordinator-contract.md)独立实现，承接下面已验收的基础模块。员工实际失败快照 `a4ce167`、Codex 刷新保护 `f0c9b86`/`7cd485b` 与后台协调器由原 GPT-5.6 Sol 子任务负责；根任务集成 App/CLI、管理入口、维护结算、网页和进程验收。不是参考产品源码移植，不代表完整 Sub2API 能力已经对齐。
- 仅在 `--allow-account-recovery` 与持久管理员设置同时开启时运行一个后台 worker。显式账号池失败保存实际协议、模型、账号与版本；旧凭据的迟到结果不冷却新凭据。每事件最多三次持久尝试，下一次至少间隔五分钟；未知结算、认证/协议问题和历史满额停止自动尝试。默认关闭、重启、管理响应丢失和取消均保留持久隔离，不能把 cooldown 到期视为账号已经恢复。
- 根任务结算专项非缓存 PASS（service 40.572s）：完整账本与原子隔离状态证明实际提交后才释放内存容量；pending、错误完成时间、旧 cooldown 仍存在均拒绝释放；短容量等待不取消已获得的维护租约。Codex 子任务证明只有本次刷新可承接 N→N+1，刷新后重导入阻止生成；仅合成凭据与注入 transport。
- 根任务在最终源码编译真实 Go 程序并运行 `scripts/smoke-account-recovery.mjs`：默认关闭/双门控、管理员/CSRF/revision、员工失败捕获、隔离拒绝、一条成功生成探测、取消后保留隔离、关闭后重启零重放、两个独立探测和三个员工请求分别入账、无 pending 及落盘日志无秘密，均 PASS。既有 `smoke-recovery-foundations.mjs` 使用旧 `dist/health-smoke.exe` 初始化隔离库后升级，`smoke-upstream-health.mjs` 也均 PASS。临时进程、随机端口与模拟上游，测试数据已清理；这不是不确定 Commit 的进程故障证据，该路径由 Go 故障注入测试覆盖。
- 网页 TypeScript、74 项测试及生产构建通过。根任务用真实 Go + Playwright Chrome 验证开启/关闭、员工失败后隔离展示、服务器已保存但浏览器丢失响应后查询实际设置、桌面 1440×1080 和手机 390×844。修正手机隔离列表横向滚动后再次通过；0 页面 JavaScript 异常，仅预期 session401 与主动注入 ERR_FAILED。最终截图已查看：`C:/Users/apple/.codex/visualizations/2026/09/23/cpa-recovery/desktop.png`、`mobile.png`。所有模型调用只到合成回环上游。
- 后台子任务 `074a91a` 专项通过（恢复 service 29.912s、探测 accounting 7.502s），运行时 `a4ce167` 专项 44.922s 通过。根任务完整非缓存 `go test -p 1 ./... -count=1 -timeout=10m` PASS：service 404.179s、accounting 14.477s、membership 0.403s、scheduling 0.148s。最终全仓 `go vet -p 1 ./...` 和编译 PASS。补充时钟回拨后的固定结算时间测试通过，未改变重试的原始结果；网页文案收尾后相关 10 项测试、TypeScript/构建和真实浏览器流程再次 PASS。
- README PowerShell 块与基线保持一致，104 个本地文档链接、6 项 CI 路径测试及 diff 检查通过。当前变更只触发 core/web，三个安装平台不触发；本机无 C 编译器，完整 Linux race 待本批 CI，不冒充已经通过。
- 源码 `191db7c3edef77c807a86083b666bc480bbaa0fd` 的 [Code validation 35857351905](https://github.com/surpaimb/cpa-cloud/actions/runs/35857351905) 普通 Go 步骤通过（Linux service 145.946s），网页日志已核实 TypeScript、74 项测试及构建通过，Windows/Linux/macOS 安装 job 全部 skipped。完整 service race 在 25 分钟上限超时，堆栈停在 Responses 用量测试的真实 bcrypt 初始化；日志未出现 DATA RACE，但超时不能视为 race 通过，后续 vet/构建/进程 smoke 因该失败未执行。
- 针对初始化成本，`8ef53cf`/`d0bea39` 只调整 runtime 测试夹具：复用一个已经关闭的合成初始化模板，每个测试复制到独立目录和数据库；不共享 App/DB，不降低 bcrypt cost=12，认证与迁移测试保留真实初始化。子任务完整非缓存 service 回归 PASS 320.774s（墙钟 323.749s）；根任务审查后独立运行模板/会话边界 1.566s、7 项恢复协调器测试 5.058s 及全仓 vet，均 PASS。缺 Cookie、错误 Origin/CSRF、退出、过期及写隔离均有断言；等待相同范围的完整 Linux race 复验，不提高超时或删减套件。
- 最终源码 `e53d59132bec893d6c57d5199371a592f51432ea` 的 [Code validation 35860548646](https://github.com/surpaimb/cpa-cloud/actions/runs/35860548646) 已全部成功。根任务读取 core job `107179469464` 实际日志：普通 Linux service 109.854s；相同完整 race 范围 service 1078.025s、membership 1.802s、scheduling 1.087s、accounting 55.232s 全部 PASS；vet、编译、上游健康、恢复基础和自动恢复三组隔离进程 smoke 全部 PASS。三个安装 job 均 skipped；本轮未改网页，web job skipped，网页证据仍为上一轮 74 项及真实浏览器验收。此结果仅覆盖本批恢复源码，不覆盖尚在独立分支的出站代理和治理组件。
- 本批仍仅源码，preview.3 不含本功能；没有新 tag、安装包、真实供应商/会员账号或生产部署。预算、代理池、商业化及其他功能矩阵待办继续保留。

## 2026-09-23：生成恢复基础模块与独立探测账本

- 账本 `45bafe9`、维护租约/隔离 `3cf737e`、固定协议 runner `b54cb5d` 已由 GPT-5.6 Sol 子任务交付，根任务负责 App 初始化、管理员汇总、内部单次执行事务桥、legacy 路由隔离及进程验收。独立规格见[基础模块契约](system-probe-foundations-contract.md)。入口、执行、持久化、生命周期分别列明：只读管理入口已接线；内部执行有测试；三张独立表已迁移；启动中断与容量恢复已接线；自动创建隔离/触发探测的 worker 和配置仍未实现。
- 系统探测不创建员工/Key/普通模型请求，成本与用量独立汇总，未知值保持 NULL。Begin/派发/结算与维护租约、隔离修改共用调用方事务；MarkMayHaveSent 持久化失败时零网络。结束提交失败保留隔离及 receipt，后续只能重试元数据，不可重放 operation。真实版本失配仍结算实际消耗，但不清新事件。
- 根任务审阅发现并协调修复：取消/超时被当作配置漂移（`a0e3577` 加根结果分类）；重启墙钟回拨导致 pending 无法中断（`98fbad3` 加根隔离时间保护）；SQL CHECK 字面值空白被错误归一化（`7a17a39`）；手工 clear 未取消同事件活动探测（`a87a75a`）。根执行桥加入完整运行期计数，Close 先取消并等待执行/终结后才允许关库。屏障测试阻塞取消后的 transport 返回，确认不是只等待心跳。
- 根任务完整非缓存 `go test -p 1 ./... -count=1 -timeout=8m` PASS（service 351.646s、accounting 14.114s、membership 0.397s、scheduling 0.142s）。该全量运行之后的收尾修订在最终源码重新定向执行：service 的 SystemProbeAccounting/RecoveryExecution/RecoveryIsolation/CooldownClearCancelsOnlyMatchingMaintenanceEvent 19.797s、accounting 的 TestSystemProbe 8.624s，均 PASS；全仓 `go vet -p 1 ./...` PASS。本机没有 C 编译器，完整 Linux race 待本批轻量 CI，不能用普通测试替代。
- 根任务在最终源码编译 `dist/recovery-smoke.exe`，实际运行 `scripts/smoke-recovery-foundations.mjs` PASS。旧 `dist/health-smoke.exe` 初始化隔离库，脚本确认三张新表原先不存在，再升级验证员工 Key 与旧数据保留、独立汇总、启动 pending→interrupted、关闭自动探测时零重放、冷却已到期仍保持恢复隔离、事件 clear、保守恢复维护容量及 TTL 后员工可用。模拟凭据/临时目录/随机端口，数据库和日志无明文秘密，测试服务与数据已清理。
- 既有 `scripts/smoke-upstream-health.mjs` 在本批集成程序上 PASS，覆盖管理员/员工隔离、目录与分页、幂等、替换竞争、崩溃中断、冷却事件 CAS、重启和撤销；后续 clear 取消小修另有专项覆盖。没有浏览器页面改动，本批不新增 GUI 实测声明。
- 两份 README 的 PowerShell 块与基线一致，相关文档 85 个本地链接、6 项 CI 路径计划及 diff 检查通过。core/web=true、Windows/Linux/macOS 安装任务=false。根据前一批 race 的 1175 秒实测，将完整 race 上限 20→25 分钟、core job 30→35 分钟，保留完整套件与断言；CI 新增独立进程恢复 smoke。
- 源码 `c9dc95fcce9ef2ea7d7ff749b4d124119b69adf9` 已推 main，[Code validation 35848320519](https://github.com/surpaimb/cpa-cloud/actions/runs/35848320519) 全部成功。根任务读取 core job `107139774056` 日志核实：Linux 普通 service 117.542s；完整 race 的 service 1160.406s、membership 1.882s、scheduling 1.087s、accounting 54.230s 全部 PASS；vet、编译和两组隔离进程 smoke 均 PASS。读取 web job `107139774192` 核实 TypeScript、70 项测试及 Vite 构建通过，三个安装 job 均 skipped。之后只有文档修订，不另触发构建。
- 下一批[后台协调器契约](account-recovery-coordinator-contract.md)经过三个原任务只读复核，明确默认关闭开关、实际失败快照、长冷却原因一致性、SQL-only Codex adoption、持久人工阻塞和公平扫描、同事件次数上限，以及不确定 Commit 的终态核对。这是已审阅的后续规格，尚未实现。
- 本批尚未启用自动生成探测，没有新 tag/安装包，preview.3 不变。Codex 的可证明刷新 revision 采纳、失败路径快照、默认关闭开关、退避和后台协调器、网页控制继续按[执行计划](account-recovery-execution-plan.md)实现；模拟成功不等于真实会员或供应商兼容验收。

## 2026-09-23：上游凭据/目录测试与冷却管理

- 后台 `5501f51`、网络边界测试 `d7298f5`、冷却运行时 `9e69bba`、Codex 专项 `4290c68` 和网页 `4f9fbb0` 已交付。根任务负责 App 生命周期/路由、列表投影、契约及进程验收接线。实现依据本仓[上游测试与恢复契约](upstream-health-contract.md)，只允许本地凭据检查与模型目录请求，不新增生成请求或后台探测定时器。
- 操作编号稳定幂等；同号不同输入冲突，网络中断查询原号，服务重启记 interrupted、不自动重发。测试与当前 provider/凭据来源/revision 绑定；最终重读和写入处于同一事务及相应锁内，重导入只产生 stale。终结存储失败后，协调器辨别仍在执行的同一操作，将遗留记录恢复为 interrupted；存储仍不可用则返回固定 503。
- 后台任务专项通过：health 与既有 OpenAI/Gemini/Codex 目录回归 29.834s；网络边界专项 3.254s，元数据/私网在 transport 前拒绝、302 不跟随且密钥不抵达目标。Codex 专项由调度任务执行 3.430s：实验开关关闭零解密/刷新/List，本地检查不刷新，新目录操作绕过缓存，相同操作零重放，共享刷新 requested=1/tested=2，无 mutation 自锁，重导入 stale，目录401不另写 reauth。
- 冷却每次有效失败推进事件 ID，期限取最大值；数据库提交的事件/截止时间传入内存 scheduler。条件清除同时比较账号 revision 与事件 ID，不改 enabled、凭据或观测。Release、Clear 与最终准入共用变更锁；旧等待者保守终止，新失败不能被旧页面清除。专项覆盖同一时刻短/长冷却、最终派发屏障、并发清除及重启恢复。
- 根任务在 `eefb8ef`（含根任务接线）构建最终程序并运行 `scripts/smoke-upstream-health.mjs` 通过，使用旧 `dist/preflight-smoke.exe` 初始化的隔离数据库升级：管理员/CSRF/员工隔离、本地零网络、OpenAI/Anthropic/Gemini目录与两类分页、同号幂等、严格输入、在途替换 stale、进程中断恢复、目录成功不清冷却、事件 CAS、重启与内存清除、员工撤销和数据库/日志无秘密。包含早取消、终结失败恢复与严格迁移修订；强制崩溃在 Windows/Linux 均使用 SIGKILL，不以正常 SIGTERM 退出冒充崩溃。
- 严格迁移补丁 `b32a25e` 已审阅：旧/新 cooldown 表精确验证主键、type/nullability、外键、唯一约束及 expiry index，失败事务回滚并支持修复后重试。专项 12.590s 通过；根任务完整非缓存 `go test -p 1 ./... -count=1 -timeout=8m` 通过（service 307.515s），全仓 `go vet -p 1 ./...` 通过。随后 `eefb8ef` 的 Codex 无效/超大目录错误分类小修另由根任务专项非缓存验证 3.753s，通过后重建并执行上述最终 smoke。本机无 C 编译器，Linux race 留给本批轻量 CI。
- 根任务独立网页 TypeScript、70 项测试及 Vite 构建通过。真实 Go + Playwright Chrome（Browser plugin not available）在随机隔离地址验证：本地测试零网络、目录测试、保存响应丢失后关闭/重开并查询同一操作且上游只调用一次、手机导航和冷却清除。桌面 1440×1000、手机 390×844 均无横向溢出，0 JavaScript 页面异常；仅预期 session401 与主动注入的 ERR_FAILED。最终手机截图已查看，合成数据/服务进程已清理。截图目录：`C:/Users/apple/AppData/Local/Temp/cpac-health-ui-evidence-swW1fS`。
- 最后仅修订网页错误文案，避免把本地凭据失败说成上游已经拒绝、把静态配置无效说成刚发生变更；TypeScript、6 项健康入口测试和 Vite 构建再次通过，未改变布局或请求逻辑。两份 README PowerShell 块与基线保持一致，87 个本地 Markdown 目标及 CI 路径计划检查通过；core/web=true，三个安装平台=false。
- 收尾审阅补充 CHECK 字面值区分大小写：DDL 规范化只折叠引号外的关键词，不能把 `'PENDING'` 或 `'TRANSIENT'` 当成当前小写状态约束。健康记录及冷却迁移的拒绝/修复重试专项由根任务非缓存执行 5.016s 通过，全仓 vet 再次通过。该小修推送后由最新 CI 覆盖，不使用前一轮仍在执行的 CI 作为最终证据。
- 源码 `09c1862174e1d52f734e9eca233d9ba089769fa7` 已推 main，[Code validation 35840298371](https://github.com/surpaimb/cpa-cloud/actions/runs/35840298371) 全部成功。根任务已读取实际日志：Linux 普通 service 测试 108.593s，完整 race 的 service 1175.143s、membership 1.849s、scheduling 1.085s、accounting 4.264s；vet、编译及新增隔离进程 smoke 均通过。三个安装 job 均 skipped。此次仅 Go 小修，web 按路径 skipped；相同网页源码已在前一轮 [35839908002](https://github.com/surpaimb/cpa-cloud/actions/runs/35839908002) 的 web job 完成 TypeScript、70 项测试与 Vite 构建，已读取日志核实。
- 后续[生成恢复探测执行草案](account-recovery-execution-plan.md)选择独立系统账本、共享容量的精确账号维护租约和持久化恢复隔离；已做本仓源码只读复核，尚未实现。本批 race 已接近 20 分钟上限，下一批新增服务测试时应根据实测评估测试夹具成本与预算，不能省略并发断言或用更小套件冒充完整回归。
- 本批仅源码，preview.3 和已发布资产保持不变；生成恢复探测、代理池、预算、商业化及 Claude/Gemini 会员仍在总目标中。没有真实会员或供应商账号验收。

## 2026-09-23：派发前一次安全换号

- 账号运行时 `22c0300`、Claude/Gemini 接线 `17024df`、Chat/Responses/Codex 接线 `63c1988`、公共协调器与实际账号账本 `7ac7e5b` 已整合。实现依据本仓[安全换号契约](account-pool-failover-contract.md)独立编写，未复制参考产品代码。只有显式账号池中可证明尚未进入模型 HTTP/Codex 执行器的账号特定预检失败，才可选择一个不同账号一次；进入执行器后任何错误均不自动重放。
- 共享协调器专项非缓存通过（service 8.299s）：权限与池 revision 变化、取消、Release 存储失败、候选耗尽、实际第二模型价格、一父请求/一 attempt、请求和存储故障不错误冷却账号。新增执行阶段正向枚举，零值 `Unknown` 拒绝安全建议；旧布尔字段为 false 不足以授权换号。
- 原生专项及既有 Anthropic/Gemini/count_tokens 回归由协议任务执行通过（10.415s）；OpenAI/Codex 专项与既有 OAuth、Responses/Membership/OnDemand 回归由另一任务执行通过（56.809s）。Codex 新测试验证暂停写入失败直接终止、暂停成功但凭据保存失败也不换号、revision guard 失效时刷新端点只调用一次；合成凭据和 mock executor，不访问真实供应商。
- 主任务构建最终真实 Go 程序，实际运行新增 `scripts/smoke-safe-failover.mjs` 通过：四协议首账号合成密文损坏后仅备用账号收到一次模型调用；账本仅五个父请求和五个真实 attempt（四个成功、一个 429），每个成功采用备用账号及实际模型价格 23 micro；count_tokens 不计生成账本。429 不重放，重启后员工 Key 保留、撤销后零上游请求，数据库与日志无合成密钥/提示/私有错误正文。Node 22.22.0 使用 `node:sqlite`；夹具只在测试进程停止时修改自己的临时数据库，验收后目录与进程已清理。
- 最终程序另运行既有 `scripts/smoke-account-pool.mjs` 通过：四协议池映射、默认账号停用后的备用路由、目录一致性、429 不重放、cooldown 重启、排队 Key 撤销与会话元数据隔离。主任务完整 `go test -p 1 ./... -count=1 -timeout=8m` 非缓存通过（service 265.711s），`go vet -p 1 ./...` 与 Windows 本机编译通过。本机没有可用 C 编译器，race 由 Linux 轻量 CI 验证。
- 双语 README 和四份运行/用量契约已更新；README PowerShell 命令块与批次前一致，文档本地链接和 6 项 CI 计划测试通过。本批计划为 core/web=true、三个安装平台=false。上批完整 Linux race 已耗时 845 秒，因此仅将 race 时间预算由 15 增为 20 分钟、core job 由 25 增为 30 分钟，保持完整套件和全部断言。
- `40667835fb70d5281e2acccac3dc71d5b23b203a` 已推 main，[Code validation 35832659777](https://github.com/surpaimb/cpa-cloud/actions/runs/35832659777) 全部通过。主任务读取实际 job 日志核实：Linux 普通 Go service 90.601s；完整 race 的 service 940.426s、membership 1.934s、scheduling 1.083s、accounting 4.234s 全通过；vet 和编译通过。网页 TypeScript、64 项测试和 Vite 构建通过，Windows/Linux/macOS 三个安装 job 均 skipped。后续仅研究/契约文档提交，不另触发代码或安装构建。
- 已审阅并保存下一批[上游测试与恢复提案](upstream-health-contract.md)（`c6797d2`），明确待实现。研究修订 `b0af223` 更新 Claude/Gemini 当前官方接入限制并撤回管理员池化 `setup-token` Runner 候选，来源和具体出口见[会员接入条件](research/membership-provider-readiness.md)；用户的会员目标继续保留，API Key 子集不能替代会员交付。
- 本批源码验收完成，不发布新 tag 或安装包。账号测试/恢复探测、代理池、预算、商业化和 Claude/Gemini 会员仍未完成；合成上游验收不能替代真实供应商兼容验证。14 份本批 Markdown 的本地链接及两份 README PowerShell 块再次检查通过。

## 2026-09-23：用量查询、价格版本与网页管理

- 查询 `1393d81`、价格目录/API `6740804`、网页 `7066620`/`e33cc15` 和请求接线 `507c7d3` 已集成。按实际账号与上游模型锁定价格，保持三表原子终结；无价、停用或部分用量未知时成本为 NULL。接口见 [用量与价格契约](usage-management-contract.md)。
- 主任务独立 `TestUsagePricing*` 通过（3.861s），覆盖在途改价、公开别名与实际模型分离、查询失败不 dispatch、长模型兼容、迁移失败回滚/重试/重启；`TestPrice*` 通过（1.372s）。完整 `go test -p 1 ./... -count=1 -timeout=5m` 通过（service 248.820s）。价格后续 Rows.Err/严格 UTF-8 小修后再跑 `Test(PricingAdmin|UsagePricing)` 通过（8.500s），完整 `go vet -p 1 ./...` 与 Windows 编译通过。
- 新 `scripts/smoke-usage-management.mjs` 使用真实临时 Go 进程和合成上游：在途旧价格 187、新价格 374、备用账号实际模型价格 561，历史幂等重放不回退当前价格、CAS 冲突、停用/未知用量、分币种汇总、过滤与分页、管理员/员工隔离、CSRF、重启、撤销及数据库/日志无正文和秘密全部通过。
- 子任务网页 64 项测试及 TypeScript/Vite 构建通过。主任务使用真实 Go + Playwright Chrome（Browser plugin not available），隔离地址 `http://127.0.0.1:64038`，桌面 1440×1000、手机 390×844：价格创建→模型调用→0.000187 USD 汇总/尝试详情，真实保存响应丢失→原 payload/operation 重试仅追加一次，最大安全整数无损展示，后台竞争409→保留输入→重新加载→停用，员工筛选与未知费用全部通过。
- 浏览器发现同值上游选择导致加载状态不结束，已由 `e33cc15` 修复并重跑原操作路径通过。页面身份、非空内容、无构建错误覆盖层、截图与实际交互均核对；0 JavaScript 页面异常。控制台仅预期未登录401、注入的响应丢失 ERR_FAILED 和版本冲突409。两次模型请求只到合成回环上游；正常进程/浏览器验收临时数据已清理。
- 截图已查看：`C:/Users/apple/.codex/visualizations/2026/09/23/cpa-cloud-usage/usage-dashboard-desktop.png`、`usage-price-mobile.png`；同目录 `result.json` 保存无秘密的验收摘要，浏览器脚本在 `C:/TopC9-QA/cpa-cloud-usage-browser.cjs`。初次 Go 路由 panic 留下的合成测试目录清理被自动审批拒绝，未绕过该限制；不是用户数据或生产实例。
- 两份 README 新增配置/单位/限制说明，原有 PowerShell 命令块保持不变，本地文档链接检查通过。CI 路径计划及6项计划测试通过：core/web=true，windows/linux/macos=false。由于前次 race 已耗时554秒且新增服务测试，本批 race 超时预算从10增至15分钟、core job从20增至25分钟，保留完整 race 套件和所有断言。
- `4a2b52593a3ba3b4264b812802c2051ffbeb97cc` 已推 main，[Code validation 35827300394](https://github.com/surpaimb/cpa-cloud/actions/runs/35827300394) 全部通过。已读取实际 job 日志：Linux 全部 Go 普通测试（service 81.799s），完整 service/membership/scheduling/accounting race（service 845.135s），vet 和编译通过；网页 TypeScript、64 项测试及 Vite 构建通过。三个平台安装 job 均为 skipped。后续仅提交本文和契约说明，不触发新安装包构建。
- 已修正旧用量契约中的价格/管理页面状态；下一批[安全换号契约](account-pool-failover-contract.md)明确标为待实现，只允许模型 dispatch 前的账号特定预检失败切换一次，进入 HTTP/Codex 执行器后禁止自动重放。
- 此批为源码增量，不发布新 tag/安装包。成本为管理员配置的内部估算，不是供应商账单；售价、余额、预算、报表导出、支付、自动换号、代理池以及 Claude/Gemini 会员等仍在总计划内，尚未完成。

## 2026-09-23：账号池执行、网页配置与共享刷新

- 主任务在 `c8036d1` 构建真实 Go 进程并运行 `scripts/smoke-account-pool.mjs`：四协议映射、默认账号停用后的备用路由、目录一致性、429 不重放当前请求、冷却重启、排队 Key 撤销与会话元数据隔离全部通过。只使用随机临时目录与回环合成上游。
- 网页 `1e61003` 独立执行 58 项测试及 TypeScript/Vite 构建通过。真实 Go + Playwright 运行 `scripts/smoke-account-pool-ui.cjs`，验证分组/渠道、显式保存、revision 冲突保留编辑、重新加载、停用账号提示、权限不自动扩大及备用账号模型映射。桌面/手机截图已查看，0 页面 JavaScript 异常，模拟上游仅收到 1 个明确授权请求；数据和进程已清理。
- OAuth 竞争修复 `a5dee0f` 已复核：重导入、启停与刷新共用账号锁；交换前、明确 429 重试及旋转写入均检查当前来源、client ID 和 revision。主任务专项 `TestCodex(OAuth|Refresh|Catalog)` 非缓存通过（40.681s）。
- 新一轮接线包含权限变更唤醒排队请求和四协议账本 hook。主任务 `Test(AccountPool|ModelAdmission|UsageLedger)` 非缓存通过（69.847s）；最终 `go test -p 1 ./... -count=1 -timeout=5m` 全部通过（service 215.888s），`go vet -p 1 ./...` 和 Windows 本机编译通过。用量 HTTP 专项 `e822783`、流式终帧修复 `e824d7e` 均纳入这轮完整回归。
- 用量终结 `2244367`/`fe68ed1` 用同一事务更新 attempt、accounting request 和旧请求状态；SQL trigger 注入验证任一失败全部回滚、不返回成功正文/结束帧、重启后恢复 interrupted。四协议 JSON/SSE、重复快照、未知用量/价格 NULL、Codex 用量转换、取消、零 attempt、撤销、协议不匹配和内存关联清理已自动验收。Gemini 多候选从首个结束标记开始缓冲，Responses 的 failed/incomplete/EOF 分别归类并保留合法 incomplete 用量。
- 主任务对最终新编译程序执行 `smoke-native-providers.mjs`、`smoke-responses.mjs` 与 `smoke-account-pool.mjs` 均通过；此前账本接线版本的 `smoke-preview.mjs --discover-models`、`smoke-codex-import.mjs` 也通过。没有访问真实供应商凭据或模型端点。本机工具链没有可用 C 编译器，Linux race 已由下述轻量 CI 完成。
- 本批 CI 路径计划核对为 core/web=true，windows/linux/macos=false，6 项计划测试通过；两份 README 各 5 个 PowerShell 命令块与修改前一致，更新文档的本地链接检查通过。
- `aee9493d9414d599385e2317eae4ed9fbf6a9ad3` 已推 main，[Code validation 35821133704](https://github.com/surpaimb/cpa-cloud/actions/runs/35821133704) 全部通过。Linux 全部 Go 普通测试（service 60.910s）、service/membership/scheduling/accounting 的完整 race（service 554.160s）、vet 和编译通过；网页 TypeScript、58 项测试和 Vite 构建通过。已读取实际 job 日志核对，三个系统安装 job 均为 skipped。本条记录为文档收尾，不触发新打包。
- 此批仅源码；没有新 tag、安装包或真实供应商账号测试。Claude/Gemini 会员、自动换号、代理池、完整预算与运营功能继续未完成。

## 2026-09-22 16:20（Asia/Shanghai）

- 网页提交 `c220477` 已存在；主任务在 `web/` 独立重跑 `bun run test`，1 个文件、4 个测试通过。
- 主任务重跑 `bun run build`，TypeScript 检查及 Vite 生产构建通过。
- 以上是组件测试/静态构建证据，不代表真实服务端端到端验收通过。
- 服务端任务仍在定位 HTTP 集成测试等待问题；已要求设置明确测试超时并按堆栈修复。
- 研究提交 `c445e7a` 已阅读；已向原任务派发证据复审，区分公开协议缺失、架构限制及供应商明确条款，并修正“可以交付三家 API/Cloud IAM”的预览范围表述。
- 下一步：后端提交并完成测试后，启动隔离数据目录与模拟上游，验证网页/API 创建、请求、SSE、撤销和重启恢复。未启动生产部署。

本次协调没有修改运行任务拥有的实现文件；会员支持仍未实现。

## 2026-09-22：服务端与进程集成通过

- 服务端提交 `ed99d8d` 已核实，工作树干净。服务任务报告普通 Go 测试、go vet、Windows 构建通过；race 未运行（当前 CGO_DISABLED 工具链不支持）。
- 主任务独立执行 `node scripts/smoke-preview.mjs C:\workspace\cpa-cloud\dist\windows-amd64\cpa-cloud.exe`，退出码 0、PASS：CLI 初始化、网页入口、管理 API、默认永久 Key、非流式/SSE、上游凭据隔离、重启恢复和撤销持久化。
- 这是隔离数据目录与模拟上游的进程验收，不代表真实供应商或 CC Switch 实机兼容验收。
- 网页任务接续真实服务端浏览器流程验收；研究任务接续已引入依赖的许可证及声明盘点。均是具体剩余验收工作。
- 会员研究复审 `ba1f5a6` 已提交，会员能力尚未实现。当前预览仅支持 openai-compatible API Key 与 Chat Completions。
- 跨平台构建、完整备份/恢复、生产密钥托管和其他产品目标仍待完成；未发布、未部署。

## 2026-09-22 16:39 后续检查

- `scripts/build.ps1 -TargetOS linux -TargetArch amd64 -SkipWeb` 交叉编译通过，产物 `dist/linux-amd64/cpa-cloud`（16528684 bytes）。未在 Linux 主机运行验证。
- 同一终端会话 60245 正在继续 macOS arm64 构建，尚无完成结果；下轮先检查该会话及产物，避免重复编译。
- 网页真实后端验收正在修复错误文案；依赖许可证盘点仍运行中。
- 发现管理员密码声明最大 256 bytes 与 bcrypt 最大 72 bytes 不一致，已向原服务任务派发长度校验及边界测试修复。

## 2026-09-22：应用崩溃后的恢复核验

- 已确认 `e86ecea` 网页修复与 `dfe5599` 密码字节长度修复均已提交；恢复时工作树干净。
- 主任务重跑最新网页测试，5 项通过；TypeScript 与 Vite 构建通过。最新 Go 普通测试通过（service 5.204s）。
- 前轮会话 60245 已退出 0，Linux amd64 与 macOS arm64 均完成交叉编译；这些产物早于密码修复，未在目标操作系统运行。
- 研究盘点与网页收尾因应用中断，已向原任务发送恢复指令，没有创建重复任务。
- 最新 Windows 构建与主任务进程级 smoke 均通过（会话 67735 退出 0），覆盖默认永久 Key、非流式/SSE、凭据隔离、重启与撤销持久化。

## 2026-09-22：重复打包验收

- 修正 `scripts/build.ps1` 重复复制目录可能导致 `web/dist` 嵌套的问题；只替换已验证位于目标 dist 目录内的生成网页目录，拒绝链接目录。
- 主任务连续执行两次 Windows 完整构建，均通过；确认 `dist/windows-amd64/web/index.html` 存在且无嵌套 dist。
- `smoke-preview.mjs` 支持显式网页目录；使用打包后的 exe 与 web 目录执行，完整进程验收 PASS。
- 网页任务已经结束；依赖声明盘点仍在运行，不将未提交草稿当作最终交付。

## 2026-09-22：双语安装文档与发布准备

- 中英文 README 已完成安装、初始化、上游/模型/员工 Key 配置、HTTPS、维护、源码构建与故障排查；初稿已推送 GitHub。
- 主任务检查两份 README 本地链接与代码围栏通过；PowerShell 示例语法解析通过，用生成密码验证 stdin 初始化通过。
- 自动审批检查拒绝删除 `.git/doc-init-d4f4c05f74584cc39ec83a4760bf5754`，该目录保留随机测试数据，不在 Git 跟踪范围内。
- 用户要求参考 CC Switch 多平台 Release；首批范围为 Windows/Linux/macOS 各 amd64/arm64 便携包，不包含桌面安装器或自动更新。
- 发布打包工作由服务端任务负责，主任务审阅发现的 GitHub CLI 仓库上下文及中间网页产物误下载问题已反馈并修正。尚未推送预览 tag，不声称附件已可下载。

## 2026-09-22：首个六平台预览 Release 已发布

- 发布源提交 `c7655c5`，标签 `v0.1.0-preview.1`；工作流 https://github.com/surpaimb/cpa-cloud/actions/runs/35712559009 全部成功。
- Windows、Linux、macOS 的 amd64/arm64 六个对应架构 runner 均通过 Go 测试、构建与二进制帮助启动检查；网页测试/构建及发布作业成功。
- Release https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.1 已核实包含六个压缩包、六个单独SHA256文件及汇总 SHA256SUMS.txt。
- 主任务从 GitHub 实际下载 Windows amd64 附件，核对汇总 SHA256 后解压，使用包内程序和网页独立重跑 smoke，完整 PASS。
- 未对其余五种下载包执行本地端到端部署验证，不宣称会员或其他尚未实现协议支持。各包未签名/未公证。README main 分支新增六个已存在附件的直接下载链接。

## 2026-09-22：桌面启动器服务接口验收

- 服务接口提交 `a74d314`；主任务独立运行 `scripts/smoke-launcher-cli.mjs`，只读初始化检查、参数冲突、初始化、实例 UUID 就绪校验、stdin EOF 优雅退出及重启全部 PASS。
- 验收程序为 `dist/launcher-cli/cpa-cloud.exe`；同一程序配合 `web/dist` 运行原有 `scripts/smoke-preview.mjs`，网页入口、管理 API、永久员工 Key、非流式/SSE、凭据隔离、重启与撤销持久化全部 PASS。
- Windows/macOS 原生启动器与安装脚本仍在实现，以上结果仅证明服务接口和现有 API 回归通过，不代表安装器或桌面界面已经验收。

## 2026-09-22：桌面安装包进入原生 CI 验证

- Windows 启动器 `e999fe8` / `09c0ea6`，macOS 启动器 `fe1dbb2` / `c20537f`，安装流程 `690ace0` 已提交并推送 main。
- 主任务独立执行 Windows self-contained x64 启动器 `--self-test` 和 `--integration-test`，均退出 0；未进行桌面界面点击验收。
- 安装任务报告两种 Windows Setup 已成功编译，NSIS 文件清单卸载、目录所有权与重解析点检查已加入；这不等同于实际安装/升级/卸载验证。
- GitHub Actions 原生验证：https://github.com/surpaimb/cpa-cloud/actions/runs/35720289355 。启动时状态 in_progress，尚不能认定 Mac 构建或整轮验证通过。main 构建仅上传工作流附件，不发布 Release。

## 2026-09-22：桌面安装预览发布完成

- `v0.1.0-preview.2` 固定源提交 `bdc092fb7a939c2b4cc4a817948520021bae3cc5`；发布工作流 https://github.com/surpaimb/cpa-cloud/actions/runs/35726220674 全部成功。
- Windows amd64/arm64 在 GitHub 临时 runner 完成安装、同包升级重装、卸载、互斥拒绝、重解析点拒绝以及用户数据/非程序文件哨兵保留验收。Mac 两架构完成 Swift 测试、app 构建、DMG 挂载读回。未进行人工 GUI 点击或签名/公证验证。
- Release 包含六便携包、四安装包、十个 sidecar 和汇总 SHA256SUMS.txt，共 21 附件。主任务实际下载全部十个主要附件，与下载的汇总清单逐项核对 SHA256，全部 PASS；文件保存在忽略目录 `dist/preview2-download-verify`。
- main 中英文 README 更新为真实 preview.2 下载链接与安装说明；归档内 README 保留发布源码当时措辞，main 已明确此差异，不替换已发布附件。
- 本轮桌面安装预览交付完成。会员账号、其他原生协议及生产运维仍属于后续产品工作。

## 2026-09-22：上游模型发现服务端独立验收

- 服务端提交 `7863c74`，管理接口 `POST /admin/api/v1/upstreams/{id}/discover-models` 已实现；服务任务报告全量 Go 测试与 vet 通过。
- 主任务使用 Go 1.26.6 独立构建 `dist/cpa-cloud-discovery-verify.exe`，实际运行 `node scripts/smoke-preview.mjs C:/workspace/cpa-cloud/dist/cpa-cloud-discovery-verify.exe C:/workspace/cpa-cloud/web/dist --discover-models`，退出 0，PASS。
- 新增验收覆盖模拟上游模型发现、准确使用上游凭据、模型 ID 去重排序、无秘密返回、不自动创建路由；原有初始化、网页入口、管理 API、永久员工 Key、非流式/SSE、重启和撤销持久化同时通过。
- 本次网页入口检查使用已有 `web/dist`，不代表新的服务商选择及保存后自动同步界面已通过浏览器验收。新功能尚未发布，会员授权/导入也不属于本次已验证范围。

## 2026-09-22：模型同步网页真实服务集成验收

- 网页提交 `7a0b493`，网页任务报告 typecheck、16 项测试、构建通过。主任务复核源码并用新网页产物连接真实 Go 服务及本地模拟上游，全部使用临时目录、随机生成凭据和随机端口，不接触用户实例。
- Browser plugin 不可用，使用现有 Playwright + headless Chrome。页面 `http://127.0.0.1:52305` 的登录、服务商自动填 URL、切换服务商清 Key、保存后自动同步、首次 429 后重试、上游记录仍只有一条、去重显示两模型、只创建勾选的一条重命名路由均 PASS。测试结束已停止服务并清理测试数据。
- 页面身份、非空内容、无框架错误覆盖层通过；无 JavaScript 页面异常。控制台仅两个预期的 HTTP 资源错误（未登录 session 401 和模拟上游 429），没有其他告警或错误。
- 主任务查看 1440×1000 与 390×844 截图，弹窗模型行和操作按钮可见。证据位于仓库外 `C:/Users/apple/Documents/Codex/cpa-discovery-real-desktop.png`、`cpa-discovery-real-mobile.png`，临时验收脚本为同目录 `cpa-discovery-real-qa.mjs`。
- 这验证了当前源码的真实网页/服务衔接，不代表真实供应商账号验收或已发布下载包；会员授权/导入仍未实现。

## 2026-09-22：preview.3 发布与下载校验

- `v0.1.0-preview.3` 固定源 `82d536bb28e50cf4228944236ce899b529321f85`。主分支 CI `35742185549` 和发布 CI `35743039148` 均成功；发布地址 https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.3 。
- 本版包含服务商预设、保存上游后自动发现模型、选择模型创建路由，以及 Windows 初始化窗口布局修复。会员授权和授权文件导入尚未实现。
- 六便携包、两种 Windows Setup、两种 Windows MSI、Linux 双架构 AppImage/deb/rpm、macOS Universal DMG/ZIP，共 18 个主产物；加 18 个独立校验文件和汇总清单，共 37 个附件。
- 主任务从 Release 实际下载全部 37 个附件，逐项计算 18 个主产物的 SHA256 并核对 sidecar 与汇总清单，全部 PASS。下载副本位于忽略目录 `dist/preview3-download-verify`。
- Windows amd64/arm64 的 NSIS 与 MSI 生命周期验收日志均明确 PASS；Linux 原生构建和包结构读回、Mac Universal 原生构建/架构检查/DMG 校验通过。不代表所有发行版已进行安装运行或完整人工 GUI 验收。
- 包内 README 固定于源提交的发布前状态（仍可能指向 preview.2）；最新说明与下载入口以 main README 及本次 Release 实际资产为准，不修改已发布资产。


## 2026-09-23：Codex 文件导入源码实验集成

- 网页 `999b2cd` / `4cdd2b2`，服务端 `8bca20d`，进程级脚本 `180584c`。仅源码实验，未创建新标签、未重新打包或替换 preview.3。后续迁移读取错误处理小修另列。
- 主任务独立运行 `go test ./...`（本轮输出使用 Go 测试缓存）和 `go vet ./...` 均通过；审阅了服务分流、AEAD 用途/类型/上游 ID 绑定、事务迁移、revision 条件状态回写及脱敏错误处理。服务任务另报告完整 Go 测试与 vet 通过，包含旧库迁移失败回滚→重试→重开、旧 API Key 本地请求、取消、401、替换竞争和 SSE 部分失败。测试上游为注入假执行器，协议适配器由独立假 transport 测试覆盖。
- 主任务构建实际 Go 可执行程序，运行 `scripts/smoke-codex-import.mjs` PASS：随机临时数据目录及合成 JWT，CSRF 拒绝、幂等导入、无效替换保留、成功替换、旧 revision 冲突、数据库/WAL 与进程日志无测试秘密、重启保留、默认关闭后阻止导入/替换及员工模型请求、再开启后幂等重试不会恢复旧凭据。未启用真实会员模型请求；测试目录和进程均已清理。
- 主任务对最新网页运行 TypeScript 检查和 Vite 构建通过，并用 Playwright + headless Chrome 连接上述真实 Go 服务：管理员登录、浏览器文件上传导入、同一行重新导入 revision 增加、会员模型手动创建路由均 PASS。浏览器只访问隔离测试实例；没有真实会员调用。无 JavaScript 异常、无框架错误覆盖层；控制台仅首次未登录 session 的预期 HTTP 401。
- 主任务查看 1440×1000 桌面与 390×844 手机截图，导入按钮、文件选择及操作区域可见。截图等待响应式动画稳定后获取。证据在仓库外 `C:/Users/apple/Documents/Codex/cpa-membership-real-desktop.png`、`cpa-membership-real-mobile.png`，验收脚本 `cpa-membership-real-qa.mjs`。测试只使用合成文件，结束清理临时数据库。
- 功能默认关闭，需 `--experimental-codex-membership`。导入不代表在线认证；只支持短期凭据文件导入与 user/assistant 纯文本 Chat Completions/SSE 子集。到期重新导入；没有网页登录/自动刷新、会员模型发现、Claude/Gemini 会员、员工 Responses/Messages 协议或真实账号兼容性验证。

- 迁移与列表迭代错误处理修复 `01823c9`：主任务核实 `foreign_key_check` 在 Close 前检查迭代错误，`tableColumns` 保留 defer 确保早退释放；上游和员工模型列表读取失败不会返回部分成功列表。服务任务重新运行迁移回滚/重试/重启定向测试、`go test ./internal/service -count=1` 与 `go vet ./internal/service` 均通过。

## 2026-09-23：原生 Responses 与函数工具源码实验

- 会员适配器 `b1c60e5`、服务入口 `cfea502`、集成错误事件补丁 `25c5f3a`。`POST /v1/responses` 在同一进程内完成员工鉴权、模型权限/撤销、路由和上游执行；API Key 同协议转发，Codex 导入仍需默认关闭的实验开关。
- 支持范围和官方来源见 [实现说明](research/codex-responses-implementation.md) 与 [接口契约](responses-preview-contract.md)。首批包括文本、instructions、function 工具、call_id/参数/工具结果回合、已验证的 reasoning history 子集；不将完整 Responses 生命周期、媒体、托管工具或真实 Codex CLI 标成支持。共享账号尚无资源所有权表，明确拒绝 store=true/background/previous_response_id/conversation 引用。
- 主任务独立执行 `go test ./... -count=1 -timeout=2m` 与 `go vet ./...` 通过。最终错误写入器补丁后再次运行 `go test ./internal/service -run 'Test.*(Responses|Codex)' -count=1 -timeout=2m`（33.560s）和 service vet，通过；没有新增第三方依赖，未运行 Windows race 或不可用的 govulncheck。
- 审查修复了超大无换行 SSE 的有界读取、多行 data 重编码、事件名注入/不一致、终止对象缺失 output、verified 账号重复调用，以及 revision 改变后不能发送成功终止事件。模拟测试另覆盖 401→重新授权、取消传递和会员流式增量后原生 type:error 事件，旧 Chat 错误格式保持兼容。
- 主任务重新构建 `dist/cpa-cloud-responses-verify.exe`，实际执行 `scripts/smoke-responses.mjs` PASS：工具定义/结果双回合、拆包/CRLF/多行 SSE、失败/未完成/提前 EOF/畸形 completed、HTTP 401/429/坏 JSON 脱敏、状态引用拒绝、员工模型权限、上游头与凭据隔离、未知 usage 不伪造、重启与撤销持久化、数据/日志秘密扫描。全部使用随机端口、合成秘密及临时目录，结束清理。
- 同一程序配合现有 `web/dist` 运行 `scripts/smoke-preview.mjs ... --discover-models` 与 `scripts/smoke-codex-import.mjs ...` 均 PASS。后者首次调用因漏传必需的网页目录参数而在启动前退出，补齐参数后实际完成导入/CSRF/幂等/替换冲突/加密落盘/重启/关闭阻止调用验收。未改正在运行的用户实例或读取真实凭据。
- 中英文 README 与员工说明已更新，原有 PowerShell 命令块逐块比对未变，相关文档本地链接及 diff 检查通过。只推送源码并执行轻量 CI，不创建新 tag 或全平台包；preview.3 不包含本批功能。三家会员生命周期和其他能力仍按 [完整计划](feature-parity-plan.md) 继续，任务创建及所有权见 [分工记录](work-coordination.md)。

## 2026-09-23：Codex 后台 OAuth 与手动刷新集成

- 原独立任务提交 `c261adc`、`e8429d2`、修正 `62ec2f1`；集成分支对应 `5abad6a`、`37d992c`、`dabd415`。`app.go` 冲突已人工核对，保留主线 Responses 执行器、入口、能力标志和已实现范围说明。
- 授权码单次交换；刷新仅明确 429 可有界重试，不确定的网络/响应读取/5xx 失败不自动重放旧 Token。`Retry-After` 在乘法前截断，测试包含巨大整数，防止 duration 溢出。凭据来源使用 `codex_oauth_bindings` 同事务绑定 client ID；来源未知/不匹配拒绝刷新且不改状态。重导入原子删除来源绑定。授权会话的 AEAD 密文保存 client ID/redirect 快照，配置漂移时不消费 state。无效 revision 明确返回 400。
- 主任务独立执行 `go test ./... -count=1 -timeout=2m` PASS（service 72.538s，含新 OAuth 专项、Chat/Responses 刷新集成及现有取消/失败/revision 竞争测试）；`go vet ./...` PASS。没有新增第三方依赖。
- 新增 `TestCodexOAuthRefreshFeedsBothEmployeeProtocolsAcrossRestart`：真实管理 HTTP API + 注入模拟 Token 端点创建账号和刷新，检查新凭据进入 Chat/Responses 非流与流式执行器、上游模型映射、消息/工具参数保留、verified 状态、重启持久化，以及原员工 Key 的使用和撤销拒绝。每次 App/HTTP server 创建即登记失败路径清理。
- 主任务构建 `dist/cpa-cloud-oauth-verify.exe` 并实际运行四组脚本，全部 PASS：`smoke-codex-oauth.mjs`、`smoke-codex-import.mjs`、`smoke-responses.mjs`、`smoke-preview.mjs --discover-models`。需要网页参数的脚本使用 `C:/workspace/cpa-cloud/web/dist`；本批没有网页变更。
- 新 OAuth 进程脚本使用随机端口/临时数据、合成管理员密码，验证默认关闭、缺配置、CSRF、PKCE URL、会话幂等、无效 revision、同配置重启、改 client ID 拒绝及恢复原配置后 state 仍有效、数据库/日志无明文 state；不发送任何供应商授权交换或模型请求。全部临时进程和测试目录正常清理，未接触真实账号或当前用户运行实例。
- 本机缺少 gcc/clang，Windows race 未通过环境前置条件；原任务此前的 race PASS 报告已撤回。轻量 Linux `core` CI 新增显式 `CGO_ENABLED=1` 的 `go test -race ./internal/service ./internal/membership -count=1 -timeout=5m`，运行结果在下方另记，不能用普通测试替代 race 证据。
- 中英文 README、导入/生命周期契约、功能矩阵和分工说明同步为“源码实验、后台授权及手动刷新”。原 PowerShell 命令块逐块比对未变，文档本地链接、6 个 CI 路径分类测试及 diff 检查通过。默认关闭，无网页授权入口、后台自动刷新或真实会员验证，不新建 tag 或安装包，下载版仍为 preview.3。
- 集成与验收提交 `a71ae3e3535ad14cb8a48083820b4e9dfdda8237` 已推送 main。[Code validation 35802158197](https://github.com/surpaimb/cpa-cloud/actions/runs/35802158197) 全部成功：core 的全量 Go 测试、`CGO_ENABLED=1` race、vet 和构建均通过，web 检查通过，windows/linux/macos 安装构建均 skipped。主任务读取 core job `106994752057` 日志核实 race 实际执行：service `223.479s`、membership `1.667s` 均为 `ok`；没有用 Windows 环境失败或普通测试冒充 race 结果。

## 2026-09-23：Gemini Developer API 原生通路源码实现

- 新增 `gemini-api-key` 上游、固定 Google 生产端点、独立 AEAD 用途绑定、事务化 schema 扩展、管理员模型发现，以及员工 `GET /v1beta/models`、`generateContent`、`streamGenerateContent`。
- 独立假上游测试覆盖员工 Bearer Key 隔离、模型映射、contents/systemInstruction、函数声明与 functionCall/functionResponse 回合、明确 generationConfig 子集、finishReason/usage 原样返回、SSE、多帧、取消、错误脱敏、429、权限、撤销、凭据替换和重启恢复。没有访问真实 Google 账号或端点。
- Google 官方 Gemini CLI 条款与 FAQ 明确反对第三方复用 Gemini CLI OAuth 访问 Code Assist 后端，因此未实现会员 OAuth/缓存 Token 导入，且不把 AI Studio API Key 通路称为会员可用。该边界是当前官方材料下的产品阻塞，不代表普遍法律结论；未来如有适用的公开委托协议需另立契约。
- 本功能尚未由主任务集成、进程级验收或发布；测试结果与最终提交号以集成回报为准。

## 2026-09-23：原生协议与网页授权集成分支验收

- 集成分支 `codex/parity-integration` 已合入 Anthropic Messages/count_tokens、Gemini 原生协议与分页目录；共享 `app.go` 保留 Codex OAuth、Chat、Responses 和四种提供商能力。旧库测试保留 OAuth client 绑定、模型路由和员工 Key，覆盖迁移失败回滚、重试与重开。
- Gemini 流式错误正文泄漏由进程验收发现，修复 `109f3d1` 加入有界行/事件/整流读取、错误脱敏及结束确认。主任务构建实际 Go 程序，运行 `scripts/smoke-native-providers.mjs` PASS：Claude/Gemini 模型发现、分页、工具定义及结果回合、count_tokens、SSE/错误、员工权限、凭据隔离、重启和撤销。使用随机端口、本地假上游及临时数据，结束清理；没有真实供应商调用。
- 主任务在上述集成版本独立执行 `go test -p 1 ./... -count=1 -timeout=3m` PASS（service 88.194s、membership 0.390s、scheduling 0.145s），`go vet -p 1 ./...` PASS。本机没有 race 工具链，不能用本次普通测试替代 Linux race；CI 已加入 scheduling 并发测试，结果待实际运行。
- 网页 `02e2346` / `db923f0` 在实际生产网页产物上完成 Playwright + headless Chrome 验收，后台为独立模拟管理 API。桌面 1440×960、手机 390×844：OAuth 创建与轮询、失败隐藏旧链接和重新开始、保存后不自动建路由、Gemini 原生地址与清空 Key 均 PASS；无 JavaScript 异常或相关控制台错误。Browser plugin 不可用，使用已有 Playwright 和 Chrome；没有打开真实授权页或读取用户浏览器配置。
- root 查看稳定后的桌面/手机截图，按钮可见，无移动端水平溢出。截图及临时检查脚本位于仓库外 `C:/Users/apple/AppData/Local/Temp/cpac-parity-ui-KcjZh6/`。该项是模拟管理 API 的界面验收，仍需新凭据生命周期后端完成后的真实服务衔接验收。
- `scripts/smoke-codex-oauth.mjs` 已补会话状态、跨管理员登录会话拒绝、重启配置漂移和功能开关检查，目前仅语法检查通过，待生命周期服务交付后运行。独立调度核心已测试但尚未接员工请求；自动刷新、批量导入及账号池服务集成另列后续证据，不按文件存在标为完成。本批尚未推送或发布新安装包。
## 2026-09-23：批量导入与共享凭据目录集成验收

- 批量服务 `2b3fdc6` / `4b8bb5b`、根接线 `106d6e2`、网页 `f947ae0` / `5a81db0` 已在集成分支。`smoke-batch-import.mjs` 通过真实隔离 Go 进程验证四种上游、逐项结果、CSRF、幂等与冲突、重启后收据、关闭会员开关、加密落盘及零提供商调用。
- 根独立运行 `scripts/smoke-batch-ui.cjs`，真实 Go 服务和最新网页产物使用随机临时目录/端口、合成凭据。文件上传后部分成功，修复入口只携带失败项；服务端写入后人为丢弃 HTTP 响应，再用原编号和原内容重试，返回已存在。总共四次提交只创建三个账号，模型路由仍为空，浏览器存储为空，零提供商请求、零 JavaScript 页面异常。已查看 1440×1000 和 390×844 截图，手机底部操作按钮可见。证据位于忽略目录 `dist/batch-ui-verify`；测试进程及数据已清理。
- Codex 共享刷新交付已合入 `6299d2a`，仍在修正管理员重导入/启停竞争，尚未最终验收。根目录接线 `d9b7551` 已用专项测试验证模型发现采用旋转后的凭据与 revision 缓存；暂停刷新时有效 access token 仍可获取目录，过期拒绝且不会重放 refresh token。相关目录测试独立 PASS（10.365s），不代表真实会员验证。
- 账号池配置已接入管理 API 和事务持久化（`0ae26cf` / `8d06c44`），定向测试 PASS；运行时选路仍在整合。账本核心 `b2557c4` 独立测试 PASS（4.753s），尚未接入模型执行、HTTP 统计或预算。当前能力不等于完整账号池或线上账单。
