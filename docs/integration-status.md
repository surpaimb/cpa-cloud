# 集成状态

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
