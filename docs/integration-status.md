# 集成状态

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
