# CPA Cloud 员工配置客户端 0.1.0 开发预览

本次仅提供员工桌面客户端的两个安装包：macOS Universal DMG（Intel 与 Apple Silicon）和 Windows x64 NSIS Setup。它们不是 CPA Cloud Go 服务端安装包。每个安装包附独立 SHA-256 校验文件；GitHub Release 会注明精确源码提交。自动验收仅挂载或静默安装并检查包内文件与身份，未启动客户端，也未在员工电脑上验收实际使用。

## 当前可用范围

- 检测本机受支持的工具候选，输入 CPA 地址与员工 Key，并读取该 Key 可见的服务端配置预览。
- apply_configuration 当前故意失败关闭，不能把预览写入任何真实工具配置；安装包不能完成员工端实际应用。
- 现有配置规划、备份、恢复和 Key provider 的合成测试不构成三平台真实工具配置验收。

## 安装限制

- macOS App 使用 ad-hoc 签名，未采用 Apple Developer 身份签名，也未公证。Gatekeeper 可能阻止首次打开，用户可能需要在“隐私与安全性”中手动放行；本预览没有自动更新。
- Windows Setup 未签名。未安装 WebView2 Runtime 的机器需要联网获取默认 bootstrapper；离线安装未验收。Windows SmartScreen 可能提示风险。
- 完整第三方许可证文本及通知必须随安装包保留在应用资源的 release-notices 目录；发行工作流会解包或安装后逐文件回读并校验。不满足时不发布。
- 这两个预览包不代表服务端生产部署、真实 M1→M2 轮换、跨旧备份撤销防复活或员工端实际应用已完成。

请先核对同名 .sha256 文件，再安装。此版本仅供开发预览与反馈。

## English summary

This prerelease contains one macOS Universal DMG and one Windows x64 NSIS installer for the employee desktop client. Configuration application is disabled and fails closed; the app can only detect clients and preview configuration. The macOS app is ad-hoc signed but not Apple Developer signed or notarized; Windows is unsigned and may need internet access to install WebView2. Automated checks mount or silently install the packages and inspect identity and bundled notices; they do not launch the app or verify use on an employee device. This is not a production-ready client or server release. Full dependency notices are bundled and checked before publication.
