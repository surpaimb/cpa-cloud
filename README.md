# CPA Cloud

**简体中文** | [English](README.en.md)

面向企业内部的自托管 AI 接入平台。管理员通过网页管理上游、模型和员工 Key；员工使用标准 API，无需微信或专用客户端。鉴权、权限检查和上游请求在同一个 Go 服务进程内完成。

> 当前为开发预览，尚非生产发行版。preview.3 的 18 个主包已由对应架构的 GitHub runner 完成测试、构建及各格式的自动验收；尚未完成所有目标主机上的真实 GUI 操作和部署验收。

## 功能与边界

| 已实现 | 尚未实现或验证 |
| --- | --- |
| 网页后台、管理员会话、员工启停、模型权限 | 多租户、SSO、管理员密码重置命令 |
| 一人多个 Key、默认永久有效、可选到期、撤销 | 网页会员授权入口、自动刷新、Claude/Gemini 会员接入及真实账号验证 |
| OpenAI-compatible API Key 上游、服务商预设、模型同步；源码增加 Claude/Gemini 原生 API Key 通路 | 跨协议自动转换和未列出的协议字段 |
| `/v1/models`、Chat Completions 非流式与 SSE | CC Switch 与各实际 AI 工具的完整兼容验收 |
| 最新源码：`POST /v1/responses`、函数工具调用/结果回传、非流式/SSE | Responses 有状态会话、后台任务、托管工具与完整客户端兼容性 |
| SQLite 持久化、上游凭据加密、重启恢复 | 账号池、可靠计费用量、自动备份/迁移、生产密钥托管 |

员工 Key 正常重启后仍有效；撤销、员工停用、可选到期时间及权限限制仍会生效。员工无需知道上游供应商 Key。

## Codex 会员文件导入实验（仅最新源码）

此功能已接入最新源码，**不在 v0.1.0-preview.3 下载包中**。从源码构建后，在现有启动命令末尾添加 `--experimental-codex-membership`。默认关闭；桌面启动器目前不会自动开启它。初始化与启动继续使用同一个数据目录。

1. 管理员登录网页，在“上游连接”选择“导入 Codex auth.json”，主动选择自己提供的授权文件；系统不会扫描本机配置。
2. 文件须包含可用的短期 access token、账号 ID 和所需结构。导入后显示“已导入，未验证”；文件由服务端加密保存，导入本身不会联网验证账号。
3. 点击“同步模型”获取候选目录，再明确选择模型建立路由；也可手动填写模型 ID 和对外名称。目录有短时缓存，不保证所有列出的模型都可调用，不自动扩大员工权限。
4. 员工仍使用 CPA Cloud Key，通过 `/v1/chat/completions` 调用。当前只接收 `user`/`assistant` 字符串文本，以及可选的 `stream` 布尔字段；支持文本非流式与 SSE。system/developer、工具、图片及其他未支持参数会明确拒绝。
5. 上游请求成功完成后状态变为“已验证”；凭据过期或上游返回 401 时提示重新导入。使用现有上游行的“重新导入”替换文件，员工 Key 不变。关闭实验开关会阻止会员导入及调用，普通 API Key 上游不受影响。

会员实验另支持下文的后台 OAuth 授权与手动刷新、原生 Responses 子集；Claude/Gemini 会员接入仍未完成。自动验收使用合成凭据和假上游，**尚未用真实会员账号验证**；不应据此认定 Codex CLI、Claude Code 或 CC Switch 所配置的所有工具均可使用。Messages 使用独立的 Anthropic API Key 上游，不支持把 Codex 账号转换成 Claude 会员。

升级前停止服务并备份整个数据目录（包含数据库及主密钥），保护备份访问权限。源码首次打开旧数据库会事务化扩展上游表；迁移失败会回滚。回退旧程序时应同时恢复升级前的整份备份，勿让新旧进程同时使用同一目录。

## Codex 后台授权与手动刷新（源码实验）

此批提供管理 API，**没有网页授权按钮或自动刷新，不在 preview.3 下载包中**。默认关闭；需同时配置 `--experimental-codex-membership`、`--codex-oauth-client-id` 和 `--codex-oauth-redirect-uri`。管理员须提供自己获准使用的 OAuth client ID，并确认其回调地址与 scope 获供应商支持；项目不内置其他产品的 client ID，也未验证真实第三方注册是否可用。

回调路径必须为 `/admin/api/v1/codex/oauth/callback`，使用部署的完整 HTTPS 地址；仅本机开发允许字面回环地址的 HTTP。创建授权会话和手动刷新均要求管理员会话、同源请求与 CSRF Token；授权回调要求发起授权的同一个管理员浏览器会话。

| 管理 API | 请求及行为 |
| --- | --- |
| `POST /admin/api/v1/upstreams/codex-oauth-sessions` | `{ "name": "Team Codex", "operation_id": "uuid" }`，返回授权 URL 和有效期；会话有效期 10 分钟，重试复用同一 operation ID |
| `GET /admin/api/v1/codex/oauth/callback` | 接收供应商回调；验证 state、会话与 PKCE，授权码最多交换一次，凭据加密保存 |
| `POST /admin/api/v1/upstreams/{id}/codex-refresh` | `{ "expected_revision": 3 }`，显式刷新并原子替换凭据；员工 Key 保持不变 |

- 仅本服务通过 OAuth 创建、且创建时 client ID 与当前配置一致的账号允许刷新。来源不明的导入文件或 client ID 错配返回 `409 codex_refresh_not_bound`，不修改现有凭据和状态。重新导入文件会清除此前 OAuth 来源绑定。
- 授权会话保存创建时的 client ID 和回调地址；重启更改配置后，旧会话要求重新发起授权，不会用新配置消费旧会话。
- 刷新网络失败、响应丢失或服务器错误时不会自动重放旧 Token；仅明确收到 429 才有界退避重试。失败结果未确认时不能据此认定旧 Token 仍有效。无效的 `expected_revision` 返回 400；并发版本冲突返回 409。
- 授权或刷新成功仅表示凭据已保存，不等于模型可用或账号已在线验证。模型仍需手动配置；过期账号可重新授权，原文件导入路径可继续重新导入。后台定时刷新、网页授权体验与供应商侧撤销另行交付。

接口与失败语义详见 [OAuth 生命周期契约](docs/codex-lifecycle-contract.md)。

## 原生 Responses 与函数工具（仅最新源码）

`POST /v1/responses` 使用同一个员工 Key 和模型路由，**不在 preview.3 下载包中**。API Key 上游需提供同协议 `/responses` 接口；Codex 文件导入上游需开启上述会员实验。已有 Chat Completions 的文本限制不变。

- 支持非流式 JSON、SSE、函数工具定义、调用参数与 `call_id`，以及下一回合的 `function_call_output`；网关不替员工执行工具。
- Codex 子集支持文本 `input`、system/developer/user/assistant 消息、`instructions`、函数工具选择及已验证的 reasoning/text 参数。工具回合需要推理历史时，请请求 `include:["reasoning.encrypted_content"]` 并回传对应完整 output items。详见[实现范围](docs/research/codex-responses-implementation.md)。
- 当前为无状态请求：拒绝 `store:true`、后台任务和引用服务端会话；没有资源读取/删除或 WebSocket 接口。Codex 图片、音频、托管工具及未支持字段会明确拒绝。
- 流式只有完整 `response.completed` 才表示成功；上游失败、提前断流或凭据状态保存失败不能当作完成。上游错误正文不会直接返回。

这不等于已经通过真实 Codex CLI 或会员账号验收。完整功能的阶段与待办见[功能对齐计划](docs/feature-parity-plan.md)。

## Claude / Gemini 原生 API 与批量导入（仅最新源码）

这些功能**不在 preview.3 下载包中**：

| 上游类型 | 员工入口 | 当前范围 |
| --- | --- | --- |
| `anthropic-api-key` | `POST /v1/messages`、`POST /v1/messages/count_tokens` | Messages、SSE、函数工具及结果回合；详见 [Claude 契约](docs/claude-messages-contract.md) |
| `gemini-api-key` | `GET /v1beta/models`、`POST /v1beta/models/{model}:generateContent`、`:streamGenerateContent` | Gemini Developer API 文本、函数工具与 SSE；详见 [Gemini 契约](docs/gemini-native-contract.md) |

管理员选择对应上游类型、保存供应商 API Key 并同步模型。Gemini 原生预设自动填写固定 Google 地址，兼容模式是另一种预设；供应商模型列表分页读取后才返回结果。员工请求使用 CPA Cloud Key 和管理员配置的公开模型名称。不同协议不会自动相互转换。

批量管理 API `POST /admin/api/v1/upstreams/batch-import` 支持最多 100 条、总计 8 MiB 的 JSON，请求包含 UUID `operation_id` 和带唯一 `item_id` 的 `items`。四种上游逐项返回 `created`、`existing` 或 `failed`；网络结果未确认时应使用相同操作 ID 与原内容重试。导入成功不等于在线认证，也不会自动创建模型路由。格式见 [批量导入契约](docs/upstream-batch-contract.md)。

Claude/Gemini 订阅会员不属于上述 API Key 能力；各自接入条件仍见 [会员接入记录](docs/research/membership-provider-readiness.md)。账号池、完整账本和后续范围继续按 [功能矩阵](docs/feature-parity-plan.md) 实现。

## 1. 下载安装

**当前下载版本：[v0.1.0-preview.3](https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.3)**。下表链接直接下载该版本附件。

在 [GitHub Releases](https://github.com/surpaimb/cpa-cloud/releases) 查看预览版本及附件。仅在对应版本的 Assets 中存在的文件才是可下载交付；尚无附件时请使用下文源码构建。预览版本不一定出现在 GitHub 的 latest 链接中。

| 系统 | 附件文件名后缀 |
| --- | --- |
| Windows 普通 Intel/AMD 电脑 | [windows_amd64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64.zip) |
| Windows ARM 电脑 | [windows_arm64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64.zip) |
| Linux Intel/AMD 云服务器 | [linux_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.tar.gz) |
| Linux ARM 服务器 | [linux_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.tar.gz) |
| macOS Intel 芯片 | [macos_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_amd64.tar.gz) |
| macOS Apple Silicon（M 系列） | [macos_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_arm64.tar.gz) |

压缩包应包含程序、`web/` 网页目录和第三方声明。解压到独立目录，不需要安装 Go 或 Bun。保留所有声明文件。将数据放在压缩包目录以外，升级时更容易保留。

下载后使用 Release 中的 [SHA256SUMS.txt](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/SHA256SUMS.txt) 核对文件：Windows 可运行 `Get-FileHash <下载文件> -Algorithm SHA256`；Linux 使用 `sha256sum <下载文件>`；macOS 使用 `shasum -a 256 <下载文件>`，与清单对应行比较。

后续命令均在**解压后的程序目录**执行。Windows 程序名为 `cpa-cloud.exe`；Linux/macOS 为 `cpa-cloud`。Unix 如缺执行权限，可执行 `chmod +x ./cpa-cloud`。目前不承诺 Windows 代码签名或 macOS 公证，按公司策略评估来源及签名要求，不要全局关闭系统安全功能。

### 桌面安装版

本版共有 18 个主包：上面的 6 个便携包、下面的 6 个 Windows/macOS 桌面包和 6 个 Linux 桌面包。它们均由 [GitHub Actions 运行 35743039148](https://github.com/surpaimb/cpa-cloud/actions/runs/35743039148) 自动构建；Release 另附每个主包的独立校验文件和汇总清单。Windows 两种架构均通过安装、同包重装、卸载、NSIS/MSI 互斥拒绝与数据保留验收。

| 系统 | 安装包 | 安装方式 |
| --- | --- | --- |
| Windows Intel/AMD（NSIS） | [windows_amd64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64_Setup.exe) | 运行当前用户安装程序，再从开始菜单启动 |
| Windows ARM（NSIS） | [windows_arm64_Setup.exe](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64_Setup.exe) | 使用 ARM64 Setup，操作同上 |
| Windows Intel/AMD（MSI） | [windows_amd64.msi](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_amd64.msi) | 使用 Windows Installer 安装到当前用户，再从开始菜单启动 |
| Windows ARM（MSI） | [windows_arm64.msi](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_windows_arm64.msi) | 使用 ARM64 MSI，操作同上 |
| macOS Universal（13 或更新） | [macos_universal.dmg](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_universal.dmg) | Intel 与 Apple Silicon 通用；打开 DMG，将 `CPA Cloud.app` 拖入 Applications |
| macOS Universal ZIP（13 或更新） | [macos_universal.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_macos_universal.zip) | Intel 与 Apple Silicon 通用；解压后将 `CPA Cloud.app` 移入 Applications |

Windows 的 Setup.exe 与 MSI 是同一应用的两种替代安装方式，都会使用 `%LOCALAPPDATA%\Programs\CPA Cloud`，并会检测、拒绝另一种安装器管理的现有安装；它们不能并排安装。需要切换格式时，先卸载当前安装。两者都不会把用户数据放进安装目录。

Windows 安装版自带 .NET 运行时，无需另外安装。首次启动在原生窗口设置并确认管理员密码（12–72 个 UTF-8 字节），服务就绪后自动打开 `http://127.0.0.1:8787`；用户名为 `admin`。preview.3 已修复放大文字时初始化按钮可能不可见的问题。使用安装版可跳过下文第 2、3 节，直接进行第 4 节网页配置。

Windows 托盘或 macOS 菜单栏提供打开后台、启动、停止和退出。关闭浏览器不会停止服务；退出启动器会停止它启动的服务。端口 8787 被占用时会报错，需要先处理端口冲突。安装版默认仅本机访问；云端或内网部署请使用便携包及下文 HTTPS 配置。

数据保存在 Windows `%LOCALAPPDATA%\CPACloud\data` 或 macOS `~/Library/Application Support/CPACloud/data`，与安装目录分离。升级前退出启动器并备份数据，再安装新版；卸载程序或删除 Mac 应用不会主动删除此数据目录。启动器不会自动导入既有 CLI 数据，也不自动添加开机启动或下载更新。

安装预览未进行发行者代码签名或 Apple 公证；系统可能阻止首次打开，应按组织策略核验来源。当前验证包含 Windows 安装生命周期、各原生构建、Linux 包内容读回、自动化测试和 macOS DMG 挂载读回，不代表已完成所有系统上的真实 GUI 交互验收。

preview.3 包内 README 来自发布提交 `82d536b`，仍把 preview.2 写作当前下载并把部分 preview.3 功能称为“当前源码”或“下一预览版”。这是包生成时固定的旧措辞；最新下载状态与功能说明以本页和 Release 附件为准。

### Linux 桌面包

| 架构 / 发行版 | 安装包 | 安装或启动 |
| --- | --- | --- |
| Intel/AMD 通用 | [linux_amd64.AppImage](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.AppImage) | `chmod +x` 后直接运行 AppImage，不写入系统安装目录 |
| ARM64 通用 | [linux_arm64.AppImage](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.AppImage) | 在 ARM64 Linux 上 `chmod +x` 后直接运行 |
| Intel/AMD Debian/Ubuntu | [linux_amd64.deb](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.deb) | `sudo apt install ./cpa-cloud_v0.1.0-preview.3_linux_amd64.deb` |
| ARM64 Debian/Ubuntu | [linux_arm64.deb](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.deb) | `sudo apt install ./cpa-cloud_v0.1.0-preview.3_linux_arm64.deb` |
| Intel/AMD RPM 系发行版 | [linux_amd64.rpm](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_amd64.rpm) | `sudo dnf install ./cpa-cloud_v0.1.0-preview.3_linux_amd64.rpm` |
| ARM64 RPM 系发行版 | [linux_arm64.rpm](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.3/cpa-cloud_v0.1.0-preview.3_linux_arm64.rpm) | `sudo dnf install ./cpa-cloud_v0.1.0-preview.3_linux_arm64.rpm` |

AppImage 可从终端运行 `./cpa-cloud_v0.1.0-preview.3_linux_<架构>.AppImage`。deb/rpm 把程序装到 `/usr/lib/cpa-cloud` 并添加 “CPA Cloud” 桌面入口；也可运行 `/usr/lib/cpa-cloud/cpa-cloud-launcher`。这些桌面入口会在终端中完成首次密码设置、运行仅监听 `127.0.0.1:8787` 的服务并用 `xdg-open` 打开浏览器；关闭该终端或按 Ctrl+C 会停止服务。deb/rpm 依赖 `bash`、`curl`、`util-linux` 和 `xdg-utils`。

Linux 桌面包的数据目录是 `${XDG_CONFIG_HOME:-$HOME/.config}/cpa-cloud`，日志是该目录下的 `server.log`。它位于 AppImage 和 deb/rpm 管理的文件之外，替换 AppImage 或卸载系统包不会主动删除数据；升级前仍应停止服务并备份完整数据目录。

## 2. 命令行首次初始化

初始用户名固定为 `admin`。密码通过 stdin 输入，长度为 **12–72 个 UTF-8 字节**（不是中文字符数）。初始化成功后程序退出，只需执行一次。

安装版通常通过首次启动窗口初始化。需要使用命令行时，必须沿用安装版数据目录。便携包示例使用程序目录旁的 `../cpa-cloud-data`；初始化与启动始终指向同一个目录。

### Windows PowerShell

**Windows 安装版（Setup.exe）**：下面整段可从任意 PowerShell 目录运行。先关闭初始化窗口；不要复制终端提示符或 Markdown 围栏。程序不存在时会在询问密码前停止。

```powershell
& {
    $exe = "$env:LOCALAPPDATA\Programs\CPA Cloud\cpa-cloud.exe"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe. Check the installation or portable path."
    }
    $secret = Read-Host 'Administrator password (12-72 UTF-8 bytes)' -AsSecureString
    $ptr = [IntPtr]::Zero
    $savedEncoding = $OutputEncoding
    try {
        $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
        $OutputEncoding = [Text.UTF8Encoding]::new($false)
        [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) |
            & $exe --data-dir $data --init
        if ($LASTEXITCODE -ne 0) { throw 'Initialization failed; see the message above.' }
    } finally {
        $OutputEncoding = $savedEncoding
        if ($ptr -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
        }
        if ($null -ne $secret) { $secret.Dispose() }
    }
}
```

**Windows 便携包（ZIP）**：将上面代码中的 `$exe` 和 `$data` 两行替换为下面示例，并将程序路径改为你实际解压的位置，然后执行修改后的整段代码。不会自动搜索或切换到其他数据目录。

```powershell
$exe = "C:\Tools\CPACloud\cpa-cloud.exe"
$data = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $exe) "..\cpa-cloud-data"))
```

成功后，安装版从开始菜单重新打开 CPA Cloud；便携版按第 3 节启动。安装版的数据目录是 `%LOCALAPPDATA%\CPACloud\data`，不要使用便携包的相对目录。

### Linux / macOS Bash

```bash
umask 077
read -r -s -p 'Administrator password (12-72 UTF-8 bytes): ' CPA_ADMIN_PASSWORD
printf '\n'
printf '%s' "$CPA_ADMIN_PASSWORD" | ./cpa-cloud --data-dir ../cpa-cloud-data --init
unset CPA_ADMIN_PASSWORD
```

成功提示：`Initialized administrator admin.`。若已初始化，使用已有目录启动；不要为重置密码删除数据。当前没有管理员密码重置命令。

## 3. 本机启动

**Windows 安装版**：通常从开始菜单打开 CPA Cloud 即可。如果需要前台命令行运行，请先退出托盘启动器，避免端口冲突；以下完整代码可从任意目录执行，使用安装版原有数据。

```powershell
& {
    $appDir = "$env:LOCALAPPDATA\Programs\CPA Cloud"
    $data = "$env:LOCALAPPDATA\CPACloud\data"
    $exe = Join-Path $appDir 'cpa-cloud.exe'
    $web = Join-Path $appDir 'web'
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
        throw "CPA Cloud executable not found: $exe"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $web 'index.html') -PathType Leaf)) {
        throw "CPA Cloud web files not found: $web"
    }
    & $exe --data-dir $data --listen 127.0.0.1:8787 --web-dir $web
    if ($LASTEXITCODE -ne 0) { throw 'CPA Cloud failed to start; see the message above.' }
}
```

**Windows 便携包**：把上面 `$appDir` 和 `$data` 两行换为下面的路径，修改为你实际解压的位置后执行整段。数据目录必须与初始化时一致。

```powershell
$appDir = "C:\Tools\CPACloud"
$data = [IO.Path]::GetFullPath((Join-Path $appDir "..\cpa-cloud-data"))
```

Linux / macOS：

```bash
./cpa-cloud --data-dir ../cpa-cloud-data --listen 127.0.0.1:8787 --web-dir ./web
```

服务启动成功后，点击 [打开本机管理后台](http://127.0.0.1:8787/)，使用 `admin` 登录。

也可以复制下面的地址到浏览器地址栏：

```text
http://127.0.0.1:8787/
```

此地址访问当前电脑，请在运行 CPA Cloud 的电脑上打开，并保持服务运行。健康检查 `/healthz` 返回 `{"status":"ok"}`。命令行模式在前台运行，Ctrl+C 停止；关闭终端通常会停止服务。

## 4. 网页配置

### 添加上游

preview.3 可选择 DeepSeek、OpenAI、Groq、Mistral 或 OpenRouter，自动填写已核实的官方 API 地址与显示名称；也可选择自定义服务。名称可以修改，切换服务商或修改地址后需重新填写 API Key。

点击“保存并同步模型”会先保存上游，再读取模型列表。若同步失败，上游仍已保存，点击“重试同步”即可，不要重复添加。已有上游可点击“同步模型”。API Key 无效、上游限流、不支持模型列表或超时会分别提示；不支持自动同步时，可在“模型路由”中手动输入。

| 字段 | 示例 / 说明 |
| --- | --- |
| 名称 | `Company API`，便于识别的名称 |
| 类型 | 当前仅 `openai-compatible` |
| Endpoint | `https://api.example.com/v1`，替换成供应商实际基础地址 |
| API Key | 供应商签发的上游 Key，不能填写员工 Key |

不要填完整 `/chat/completions` URL。以 `/v1` 结尾的 Endpoint 会追加 `/chat/completions`，其他基础路径会追加 `/v1/chat/completions`。使用可从服务主机访问的公网 HTTPS 地址；当前拒绝私网、链路本地、CGNAT 地址和重定向，也不使用 `HTTP_PROXY` / `HTTPS_PROXY`。

**平台部署在内网**不等于**支持内网模型上游**。内网员工可访问本平台，上游限制仍生效。`--allow-loopback-upstream` 只用于本机模拟测试，不是任意内网地址放行开关。

### 添加模型

1. 设置员工可见的模型 ID，例如 `company-chat`。
2. 选择刚添加的上游。
3. 填写该供应商实际支持、当前供应商 Key 有权访问的上游模型 ID。

preview.3 会自动读取上游模型作为候选；勾选所需模型，可修改默认的对外模型 ID，再点击“创建所选路由”。“添加模型路由”页面也支持从同步结果选择或手动输入。同步列表不会自动给员工开放所有模型，员工可用模型仍由已创建路由与员工权限决定。当前一个对外模型 ID 对应一条路由。

### 创建员工和 Key

1. 创建员工，填写姓名及可选部门、备注。
2. 选择全部模型或指定模型权限。
3. 创建 Key，为用途命名；不填到期时间即永久有效。
4. 立即复制只显示一次的 Key，通过公司认可的安全渠道交付。
5. 同时交付服务 Base URL 和模型 ID。Key 丢失后创建新 Key，再撤销旧 Key。

撤销后新请求被拒绝；不要假定已经开始的流式请求一定立即中断。

## 5. 员工配置与 API 验证

使用支持 **OpenAI Chat Completions** 的客户端：

| 配置项 | 本机示例 | 公司部署示例 |
| --- | --- | --- |
| Base URL | `http://127.0.0.1:8787/v1` | `https://ai.example.com:8787/v1` |
| API Key | 管理员生成的员工 Key | 管理员生成的员工 Key |
| Model | `company-chat` | 管理员公布的模型 ID |

远程员工不能用 `127.0.0.1` 连接管理员电脑，该地址指向员工自己的电脑。

CC Switch 可用于配置工具，但最终调用工具必须支持当前协议。**preview.3 下载包仅提供 Chat Completions；最新源码增加上述 Responses、Claude Messages 和 Gemini 原生子集。**完整实机兼容验收仍待完成，参见[员工接入说明](docs/employee-access.md)。

Bash + curl 测试示例（Key 交互输入，请求头经 stdin 传入）：

```bash
CPA_BASE_URL='http://127.0.0.1:8787'
read -r -s -p 'Employee Key: ' CPA_EMPLOYEE_KEY
printf '\n'
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- "$CPA_BASE_URL/v1/models"
printf 'Authorization: Bearer %s\n' "$CPA_EMPLOYEE_KEY" |
  curl --fail-with-body -H @- -H 'Content-Type: application/json' \
  "$CPA_BASE_URL/v1/chat/completions" \
  --data '{"model":"company-chat","messages":[{"role":"user","content":"Hello"}],"stream":false}'
unset CPA_EMPLOYEE_KEY
```

流式调用将 `stream` 改为 `true` 并给 curl 添加 `-N`。真实上游调用可能产生供应商费用。

## 6. 云服务器与公司内网 HTTPS

服务无需图形桌面，管理员通过浏览器远程操作。**非回环监听必须同时设置 TLS 证书与私钥**。当前没有自动申请证书功能。

准备匹配服务域名、被员工设备信任的证书，配置 DNS 与相应端口的防火墙规则。先安装文件、使用同一数据目录初始化，再启动。Linux 示例：

```bash
/opt/cpa-cloud/cpa-cloud \
  --data-dir /var/lib/cpa-cloud \
  --web-dir /opt/cpa-cloud/web \
  --listen 0.0.0.0:8787 \
  --tls-cert /etc/cpa-cloud/fullchain.pem \
  --tls-key /etc/cpa-cloud/privkey.pem
```

访问 `https://ai.example.com:8787`，员工 Base URL 为 `https://ai.example.com:8787/v1`。Windows/macOS 使用同样参数并替换路径。

服务账户需能读程序、网页、证书，并能写数据目录；保护 TLS 私钥和数据目录不被其他普通用户读取。一个实例只使用一个数据目录，不启动多个进程共享同一 SQLite 数据库。

这里使用服务自身提供 TLS。当前没有可信反向代理配置；不要仅在代理终止 HTTPS 后转明文 HTTP，否则 Origin 校验与安全 Cookie 可能不匹配。仓库暂未提供 systemd、Windows 服务或 Docker Compose 安装方案；配置常驻运行前先完成前台启动验收。

## 7. 参数、数据与维护

| 参数 | 默认 / 用途 |
| --- | --- |
| `--data-dir` | 系统用户配置目录下的 `cpa-cloud`；建议显式指定绝对路径 |
| `--listen` | `127.0.0.1:8787` |
| `--web-dir` | 空；不设置则没有网页 |
| `--init` | 读取 stdin 初始化，然后退出 |
| `--tls-cert` / `--tls-key` | 必须成对设置；非回环监听必需 |
| `--experimental-codex-membership` | 仅最新源码；默认关闭 Codex 文件导入、会员请求及 OAuth 实验 |
| `--codex-oauth-client-id` / `--codex-oauth-redirect-uri` | 仅最新源码；同时设置才启用后台 OAuth 授权与手动刷新，另需开启会员实验 |
| `--allow-loopback-upstream` | 默认关闭，仅本机开发测试 |

用 `cpa-cloud --help` 查看二进制参数。

数据目录包含 `cpa-cloud.db`、可能存在的 WAL/SHM 文件和 **`master.key`**。员工 Key 保存为带密钥摘要，上游凭据加密保存；主机管理员仍能访问运行中的秘密。丢失或替换 `master.key` 会破坏已有凭据的可用性。

当前没有自动备份、恢复命令或升级迁移保证。手动升级前停止服务，复制**完整数据目录**到受保护位置，并保留旧程序和网页。恢复时停服务，还原同一份完整数据快照和对应程序版本，并先在隔离环境检查。不要仅复制运行中的主数据库文件。此手动维护流程尚无完整自动恢复验收。

## 8. 从源码构建

需要 Git、Go 1.26 或更新兼容版本、Bun，加入 PATH。当前验证工具版本为 Go 1.26.6、Bun 1.3.14。

```sh
git clone https://github.com/surpaimb/cpa-cloud.git
cd cpa-cloud
```

Windows PowerShell：

```powershell
Push-Location web
bun install --frozen-lockfile
Pop-Location
.\scripts\build.ps1
cd .\dist\windows-amd64
```

在该输出目录可按前文初始化和启动。脚本支持 `-GoExecutable`、`-BunExecutable` 指定路径，以及 `-TargetOS windows|linux|darwin`、`-TargetArch amd64|arm64`。`-SkipWeb` 只构建服务端，不打包网页。

Linux / macOS Bash（从仓库根目录构建本机架构）：

```bash
(cd web && bun install --frozen-lockfile && bun run build)
mkdir -p dist/local
go build -trimpath -o dist/local/cpa-cloud ./cmd/cpa-cloud
mkdir -p dist/local/web
cp -R web/dist/. dist/local/web/
cd dist/local
```

源码构建不等于可分发包；对外分发还须包含第三方原文声明和对应版本信息。

## 9. 常见问题

| 问题 | 检查方式 |
| --- | --- |
| 页面 404 | `--web-dir` 应直接包含 `index.html`，先构建网页 |
| 要求初始化 / 找不到密钥 | 初始化与启动的数据目录及运行账户是否一致 |
| 密码被拒绝 | 12–72 UTF-8 字节，末尾换行被移除 |
| 远程监听失败 | 提供成对有效的 TLS 证书与私钥 |
| 登录或写入失败 | 检查浏览器 Origin 与服务域名、端口、协议是否一致 |
| 上游地址被拒绝 | 检查 HTTPS、DNS、公网地址和路径；测试开关只开放回环 |
| 模型列表为空 | 检查模型路由、上游启用状态和员工权限 |
| 员工 401 / 403 | 检查 Key、撤销/到期、员工状态与模型权限 |
| 上游失败 | 检查供应商 Key、额度、模型 ID、网络和证书；报障时不粘贴秘密 |
| Codex / Claude Code 调用失败 | 核对源码与下载版本、协议、上游类型和支持字段；preview.3 无 Responses/Messages，不一定是 Key 错误 |

## 10. 开发验证

GitHub Actions 按改动范围执行：仅修改文档不触发构建；服务端和网页提交分别运行测试与编译检查，不生成安装包。修改某个平台的启动器或打包脚本时，只验证该平台；修改共用打包代码时验证所有受影响平台。相同分支的新提交会取消过时的日常检查。

完整安装包仅在推送预览版本标签或手动运行构建工作流时生成。`Native package validation` 可手动选择 Windows、Linux、macOS 或全部平台，只上传 CI 附件；`Preview release` 仅在预览标签推送时发布 Release。日常开发无需反复创建版本标签。

```bash
go test ./... -count=1 -timeout=2m
go vet ./...
(cd web && bun run test && bun run build)
```

安装 Node.js 后运行模拟上游验收（参数须为绝对路径）：

```text
node scripts/smoke-preview.mjs <absolute-executable-path> <absolute-web-directory>
```

覆盖初始化、网页入口、管理 API、永久 Key、非流式/SSE、凭据隔离、重启与撤销持久化，不需要真实凭据。Race 测试需要支持 CGO 的工具链；服务与会员模块已通过 [Linux CI race 验证](https://github.com/surpaimb/cpa-cloud/actions/runs/35802158197)，本机 Windows 未运行 race。

源码新增的 Responses 可独立验收；脚本启动临时服务和假上游，验证工具结果回合、失败事件、员工权限与撤销，并在退出时清理测试数据：

```bash
node scripts/smoke-responses.mjs <absolute-executable-path>
```

后台 OAuth 的进程级验收只检查开关、CSRF、授权会话幂等、配置变更与重启、无效 revision 及加密落盘；不访问供应商授权或 Token 端点。模拟授权交换、刷新与员工 Chat/Responses 调用由 Go 测试覆盖：

```bash
node scripts/smoke-codex-oauth.mjs <absolute-executable-path>
```

## 文档与许可证

- [集成证据](docs/integration-status.md) · [开发计划](docs/development-plan.md) · [接口契约](docs/preview-contract.md)
- [完整功能对齐计划](docs/feature-parity-plan.md) · [Responses 契约](docs/responses-preview-contract.md) · [任务分工](docs/work-coordination.md)
- [产品规划](docs/product-plan.md) · [核心设计](docs/core-design.md) · [验收矩阵](docs/acceptance-matrix.md)
- [会员接入研究](docs/research/membership-feasibility.md) · [协议来源](docs/protocol-sources.md)
- [独立实现说明](docs/independent-implementation.md) · [贡献规则](CONTRIBUTING.md)
- [第三方声明](THIRD_PARTY_NOTICES.md) · [依赖盘点](docs/research/dependency-notices.md)

本项目依据公开协议独立编写。此前接触过参考源码，不宣称严格洁净室开发。自有代码暂拟 MIT，但尚未加入正式 LICENSE，当前不声明已授予 MIT 许可。第三方依赖适用各自许可证，分发时保留相应声明。
