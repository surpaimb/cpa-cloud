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
