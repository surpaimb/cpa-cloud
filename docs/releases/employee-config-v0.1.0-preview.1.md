# CPA Cloud 员工配置客户端 0.1.0 开发预览

本开发预览涵盖员工桌面客户端的两个安装包：macOS Universal DMG（Intel 与 Apple Silicon）和 Windows x64 NSIS Setup。它们不是 CPA Cloud Go 服务端安装包。每个安装包附独立 SHA-256 校验文件；GitHub Release 会注明精确源码提交。发行自动验收仅挂载或静默安装并检查包内文件与身份，不会启动客户端，也不会在员工电脑上验收实际使用。

## 当前可用范围

- 检测本机受支持的工具候选，输入 CPA 地址与员工 Key，并读取该 Key 可见的服务端配置预览。
- apply_configuration 当前故意失败关闭，不能把预览写入任何真实工具配置；安装包不能完成员工端实际应用。
- 现有配置规划、备份、恢复和 Key provider 的合成测试不构成三平台真实工具配置验收。

## 安装限制

- macOS App 使用 ad-hoc 签名，未采用 Apple Developer 身份签名，也未公证。Gatekeeper 可能阻止首次打开，用户可能需要在“隐私与安全性”中手动放行；本预览没有自动更新。
- Windows Setup 未签名。未安装 WebView2 Runtime 的机器需要联网获取默认 bootstrapper；离线安装未验收。Windows SmartScreen 可能提示风险。
- 完整第三方许可证文本及通知必须随安装包保留在应用资源的 release-notices 目录；发行工作流会解包或安装后逐文件回读并校验。不满足时不发布。
- 这两个预览包不代表服务端生产部署、真实 M1→M2 轮换、跨旧备份撤销防复活或员工端实际应用已完成。
- 已知 Code validation 中 Go 离线缓存校验失败，机制尚未查明；现有证据不能将其归因于本次桌面打包改动，预览包检查也不能覆盖这个失败。
- 与第 16 次打包同一源码头的 Code validation 还在两个服务端 self-logout race job 触发 `SLOG21_UNEXPECTED_SNAPSHOT_QUERY`；这属于未解决的服务端验收失败，桌面预览包不会消除它。
- 原生打包第 4 次自然 CI 的 macOS Rust 单测 `oversized_journal_fields_are_rejected_and_preserved` 曾因非阻塞锁返回 OS 35 WouldBlock 而失败；该测试在后续第 5、6 次运行通过，但锁持有者与根因未知，失败工作流未手动重跑。

- 第 10 至 13 次自然打包失败的记录仍保留。第 14 次自然打包在 macOS 因 jni-sys-macros@0.4.1 缺完整许可证文本而失败；Windows 的 pnpm shim 与独立参考安装字节不一致，校验在删除 shim 前拒绝，具体差异原因未知。第14次运行未产生可发布的双平台安装包。
- 第 15 次自然打包再次失败：macOS 的 `libappindicator-sys@0.9.0` 缺完整许可证文本；Windows 在删除 shim 前因字节不一致拒绝，固定诊断仅将差异归类为额外的 Git Bash 根路径写法，未证明整包可用。此次仍未产生安装包。
- 第 16 次自然打包也失败：macOS 的 `ndk@0.9.0` 归档缺完整许可证文本；Windows 在删除 shim 前因根路径规范化校验拒绝。私有 Git Bash 重建在固定的 `NODE_PATH` 字段中发现精确 `/proc/cygdrive/<盘符>/<工作区根>` 路径段，并仅对此固定语法作严格核对；第 16 次运行的原始 shim 字节不可见，不能据此确认其根因；该次没有安装包。
- 两个预览安装包及各自的 SHA-256 文件，仅在拟发布的精确源码提交经后续自然 CI 的 macOS 与 Windows 打包及校验均通过后发布。即使包内文件检查通过，也不能证明客户端已在员工设备上实际可用。

请先核对同名 .sha256 文件，再安装。此版本仅供开发预览与反馈。

## English summary

This prerelease covers one macOS Universal DMG and one Windows x64 NSIS installer for the employee desktop client. Configuration application is disabled and fails closed; the app can only detect clients and preview configuration. The macOS app is ad-hoc signed but not Apple Developer signed or notarized; Windows is unsigned and may need internet access to install WebView2. Automated release checks mount or silently install the packages and inspect identity and bundled notices; they do not launch the app or verify use on an employee device. This is not a production-ready client or server release. Full dependency notices must be bundled and checked before publication. The Go offline cache verification in Code validation is known to fail; its mechanism remains unknown, and current evidence does not attribute it to these desktop packaging changes. The same source head as package run 16 also failed two server self-logout race jobs with `SLOG21_UNEXPECTED_SNAPSHOT_QUERY`; a desktop preview package does not resolve those server failures. Package checks do not cover them. In the fourth natural package run, the macOS Rust test `oversized_journal_fields_are_rejected_and_preserved` failed with a nonblocking lock OS 35 WouldBlock; it passed in the fifth and sixth runs, but the lock holder and cause are unknown. The failed run was not manually rerun.

Failures in natural package runs 10 through 13 remain documented. Natural run 14 failed on macOS because complete license text for jni-sys-macros@0.4.1 was missing; on Windows, pnpm shim bytes differed from an independent reference installation, and verification refused before unlinking any shim. The cause of the Windows byte difference remains unknown. That run produced no publishable pair of installers. Natural package run 15 also failed: macOS lacked complete license text for libappindicator-sys@0.9.0, while Windows refused a generated shim byte mismatch before unlink; the fixed diagnostic classified only an extra Git Bash root spelling. Natural run 16 failed too: macOS lacked complete license text for ndk@0.9.0, while Windows refused root normalization before shim unlink. A private Git Bash reconstruction found exact `/proc/cygdrive/<drive>/<workspace-root>` segments in fixed generated `NODE_PATH` assignments. The candidate handles only that strict syntax; the original runner shim bytes are unavailable, so this does not prove the cause of run 16. Runs 15 and 16 produced no installers. Publication of the two preview installers and their SHA-256 files requires a later naturally triggered CI run for the exact release source commit to pass macOS and Windows packaging and validation. Inspection of mounted or installed package files does not establish actual use on an employee device.
