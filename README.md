# CPA Cloud

**简体中文** | [English](README.en.md)

面向企业内部的自托管 AI 接入平台。管理员通过网页管理上游、模型和员工 Key；员工使用标准 API，无需微信或专用客户端。鉴权、权限检查和上游请求在同一个 Go 服务进程内完成。

> 当前为开发预览，尚非生产发行版。首个预览版的 6 平台包已在对应架构 GitHub runner 上通过 Go 测试、构建和启动帮助检查；下载后的 Windows amd64 包也已通过模拟上游进程验收。尚未完成所有目标主机的真实部署验收。

## 功能与边界

| 已实现 | 尚未实现或验证 |
| --- | --- |
| 网页后台、管理员会话、员工启停、模型权限 | 多租户、SSO、管理员密码重置命令 |
| 一人多个 Key、默认永久有效、可选到期、撤销 | ChatGPT/Codex、Claude、Gemini 会员授权或导入 |
| OpenAI-compatible API Key 上游、手动模型映射 | Responses、Anthropic Messages、Gemini 原生协议 |
| `/v1/models`、Chat Completions 非流式与 SSE | CC Switch 与各实际 AI 工具的完整兼容验收 |
| SQLite 持久化、上游凭据加密、重启恢复 | 账号池、可靠计费用量、自动备份/迁移、生产密钥托管 |

员工 Key 正常重启后仍有效；撤销、员工停用、可选到期时间及权限限制仍会生效。员工无需知道上游供应商 Key。

## 1. 下载安装

**当前下载版本：[v0.1.0-preview.1](https://github.com/surpaimb/cpa-cloud/releases/tag/v0.1.0-preview.1)**。下表链接直接下载该版本附件。

在 [GitHub Releases](https://github.com/surpaimb/cpa-cloud/releases) 查看预览版本及附件。仅在对应版本的 Assets 中存在的文件才是可下载交付；尚无附件时请使用下文源码构建。预览版本不一定出现在 GitHub 的 latest 链接中。

| 系统 | 附件文件名后缀 |
| --- | --- |
| Windows 普通 Intel/AMD 电脑 | [windows_amd64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_windows_amd64.zip) |
| Windows ARM 电脑 | [windows_arm64.zip](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_windows_arm64.zip) |
| Linux Intel/AMD 云服务器 | [linux_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_linux_amd64.tar.gz) |
| Linux ARM 服务器 | [linux_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_linux_arm64.tar.gz) |
| macOS Intel 芯片 | [macos_amd64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_macos_amd64.tar.gz) |
| macOS Apple Silicon（M 系列） | [macos_arm64.tar.gz](https://github.com/surpaimb/cpa-cloud/releases/download/v0.1.0-preview.1/cpa-cloud_v0.1.0-preview.1_macos_arm64.tar.gz) |

压缩包应包含程序、`web/` 网页目录和第三方声明。解压到独立目录，不需要安装 Go 或 Bun。保留所有声明文件。将数据放在压缩包目录以外，升级时更容易保留。

下载后使用 Release 中的 SHA256 清单核对文件：Windows 可运行 `Get-FileHash <下载文件> -Algorithm SHA256`；Linux 使用 `sha256sum <下载文件>`；macOS 使用 `shasum -a 256 <下载文件>`，与清单对应行比较。

后续命令均在**解压后的程序目录**执行。Windows 程序名为 `cpa-cloud.exe`；Linux/macOS 为 `cpa-cloud`。Unix 如缺执行权限，可执行 `chmod +x ./cpa-cloud`。目前不承诺 Windows 代码签名或 macOS 公证，按公司策略评估来源及签名要求，不要全局关闭系统安全功能。

### 桌面安装版（下一预览版）

Windows 安装程序和 macOS DMG 已通过 [GitHub 原生构建](https://github.com/surpaimb/cpa-cloud/actions/runs/35721996938)，尚未发布到 Release；当前上表仍提供 preview.1 便携包。下面说明新安装版的使用方式，待安装验收完成后提供下载链接。

| 系统 | 安装包 | 安装方式 |
| --- | --- | --- |
| Windows Intel/AMD | `windows_amd64_Setup.exe` | 运行安装程序，安装到当前用户，使用开始菜单启动 |
| Windows ARM | `windows_arm64_Setup.exe` | 使用 ARM64 安装程序，操作同上 |
| macOS Intel（13 或更新） | `macos_amd64.dmg` | 打开 DMG，将 `CPA Cloud.app` 拖入 Applications，再从应用程序目录启动 |
| macOS Apple Silicon（13 或更新） | `macos_arm64.dmg` | 使用 ARM64 DMG，操作同上 |

Windows 安装版自带 .NET 运行时，无需另外安装。首次启动在原生窗口设置并确认管理员密码（12–72 个 UTF-8 字节），服务就绪后自动打开 `http://127.0.0.1:8787`；用户名为 `admin`。使用安装版可跳过下文第 2、3 节，直接进行第 4 节网页配置。

Windows 托盘或 macOS 菜单栏提供打开后台、启动、停止和退出。关闭浏览器不会停止服务；退出启动器会停止它启动的服务。端口 8787 被占用时会报错，需要先处理端口冲突。安装版默认仅本机访问；云端或内网部署请使用便携包及下文 HTTPS 配置。

数据保存在 Windows `%LOCALAPPDATA%\CPACloud\data` 或 macOS `~/Library/Application Support/CPACloud/data`，与安装目录分离。升级前退出启动器并备份数据，再安装新版；卸载程序或删除 Mac 应用不会主动删除此数据目录。启动器不会自动导入既有 CLI 数据，也不自动添加开机启动或下载更新。

安装预览未进行发行者代码签名或 Apple 公证；系统可能阻止首次打开，应按组织策略核验来源。当前验证包含原生构建、自动化测试和 DMG 挂载读回，不代表已完成所有桌面交互验收。

## 2. 首次初始化（便携包）

初始用户名固定为 `admin`。密码通过 stdin 输入，长度为 **12–72 个 UTF-8 字节**（不是中文字符数）。初始化成功后程序退出，只需执行一次。

以下使用 `../cpa-cloud-data`，在程序目录旁创建独立数据目录。实际部署建议使用固定绝对路径；初始化与启动必须指向同一个目录。

### Windows PowerShell

交互读取密码，不把真实密码直接写进命令历史：

```powershell
$securePassword = Read-Host 'Administrator password (12-72 UTF-8 bytes)' -AsSecureString
$passwordPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($securePassword)
$previousOutputEncoding = $OutputEncoding
try {
    $OutputEncoding = [Text.UTF8Encoding]::new($false)
    [Runtime.InteropServices.Marshal]::PtrToStringBSTR($passwordPointer) |
        & .\cpa-cloud.exe --data-dir ..\cpa-cloud-data --init
    if ($LASTEXITCODE -ne 0) { throw 'Initialization failed' }
} finally {
    $OutputEncoding = $previousOutputEncoding
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($passwordPointer)
    $securePassword.Dispose()
}
```

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

Windows：

```powershell
.\cpa-cloud.exe --data-dir ..\cpa-cloud-data --listen 127.0.0.1:8787 --web-dir .\web
```

Linux / macOS：

```bash
./cpa-cloud --data-dir ../cpa-cloud-data --listen 127.0.0.1:8787 --web-dir ./web
```

打开 **http://127.0.0.1:8787**，使用 `admin` 登录。健康检查 `/healthz` 返回 `{"status":"ok"}`。程序前台运行，Ctrl+C 停止；关闭终端通常会停止服务。

## 4. 网页配置

### 添加上游

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

模型目录由管理员配置，不会自动从供应商同步。当前一个模型对应一条路由。

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

CC Switch 可用于配置工具，但最终调用工具必须支持当前协议。**不能把此地址当作已经兼容 Codex Responses、Claude Messages 或 Gemini 原生 API。**完整实机验收仍待完成，参见[员工接入说明](docs/employee-access.md)。

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
| Codex / Claude Code 调用失败 | 当前缺少 Responses / Messages 等协议，不一定是 Key 错误 |

## 10. 开发验证

```bash
go test ./... -count=1 -timeout=2m
go vet ./...
(cd web && bun run test && bun run build)
```

安装 Node.js 后运行模拟上游验收（参数须为绝对路径）：

```text
node scripts/smoke-preview.mjs <absolute-executable-path> <absolute-web-directory>
```

覆盖初始化、网页入口、管理 API、永久 Key、非流式/SSE、凭据隔离、重启与撤销持久化，不需要真实凭据。Race 测试需要支持 CGO 的工具链，当前 Windows 验证未运行 race。

## 文档与许可证

- [集成证据](docs/integration-status.md) · [开发计划](docs/development-plan.md) · [接口契约](docs/preview-contract.md)
- [产品规划](docs/product-plan.md) · [核心设计](docs/core-design.md) · [验收矩阵](docs/acceptance-matrix.md)
- [会员接入研究](docs/research/membership-feasibility.md) · [协议来源](docs/protocol-sources.md)
- [独立实现说明](docs/independent-implementation.md) · [贡献规则](CONTRIBUTING.md)
- [第三方声明](THIRD_PARTY_NOTICES.md) · [依赖盘点](docs/research/dependency-notices.md)

本项目依据公开协议独立编写。此前接触过参考源码，不宣称严格洁净室开发。自有代码暂拟 MIT，但尚未加入正式 LICENSE，当前不声明已授予 MIT 许可。第三方依赖适用各自许可证，分发时保留相应声明。
