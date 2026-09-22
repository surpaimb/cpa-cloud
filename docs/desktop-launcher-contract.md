# 桌面启动器契约 v1

状态：2026-09-22 用户已授权实现轻量启动器、macOS app/DMG 与 Windows 安装版。当前首轮便携 Release 保留；新版本完成验收后再发布。

## 决策与边界（ADR：本机服务生命周期）

启动器是同用户权限的本机进程管理器，不代理模型请求，不新增远程管理权限。macOS 使用 Swift 原生应用/菜单，Windows 使用 C# WinForms 原生托盘应用；管理页面仍由 Go 服务提供。启动器收集首次管理员密码，经子进程 stdin 调用 --init，不引入无需凭据的网络初始化接口。不自动启动开机项，不自动绑定外部网卡，不删除或迁移旧数据，不自动下载更新。

## 共用行为

- 默认管理地址 http://127.0.0.1:8787，网页在进程就绪后打开。固定端口冲突时清晰报错，不终止占用者、不悄悄变端口或打开未知服务。
- 首次运行原生密码/确认输入（12–72 UTF-8字节）；取消则不初始化。启动器不得记录密码、Key或上游响应。初始化失败允许重试，不以数据库文件存在作为初始化成功判断。
- 同用户单实例；重复启动提示或打开已有实例后台，不启动第二个Go进程。只管理自己创建的子进程，不通过进程名杀其他服务。
- 菜单：状态、打开后台、启动服务、停止服务、退出。服务意外退出要显示停止/错误，避免无限自动重启。关闭网页不停止服务；退出启动器则停止其服务。
- 数据目录：Windows %LOCALAPPDATA%\CPACloud\data；macOS ~/Library/Application Support/CPACloud/data。与程序、安装目录分离，权限限定当前用户；不自动接管CLI原数据目录。
- 预览不签名/不公证，不宣称自动更新、会员接入或生产托管能力。

## 服务CLI扩展（供启动器，不改变默认CLI行为）

1. --check-initialized --data-dir PATH：只检查，不创建数据/不迁移。退出码0表示已初始化且可用，3表示未初始化；其他非零表示损坏/无法读取。无秘密输出，未初始化包含目录不存在/之前失败仅创建空数据库的情况。
2. --shutdown-on-stdin-eof：正常运行时监听stdin EOF并优雅关闭服务；启动器保留stdin管道，停止时关闭管道并等待有界退出，超时才终止自己拥有的子进程。父进程异常退出时管道关闭也应触发退出。与--init/--check-initialized互斥。不开此参数的CLI行为不变。
3. CLI退出行为须自动化测试；初始化状态判断不通过解析日志字符串。

## 包目录契约

- Windows启动器：desktop/windows/，发布主程序 CPACloud.Launcher.exe；同目录 cpa-cloud.exe 与 web/。启动器定位程序相对自身路径，不依赖cwd或PATH。self-contained .NET发布，不要求终端用户装SDK；打包保留.NET/WindowsDesktop运行时声明。
- macOS：desktop/macos/；CPA Cloud.app/Contents/MacOS/CPACloudLauncher，Contents/Resources/server/cpa-cloud，Contents/Resources/web/。Bundle ID com.surpaimb.cpa-cloud.launcher。按macOS原生要求管理单实例/菜单/退出。
- 新安装附件目标：Windows amd64/arm64 Setup.exe；macOS amd64/arm64 dmg。便携六包保留。安装器用户级默认安装，开始菜单/可选快捷方式；卸载只删除安装文件，不删除上述数据目录。
- GitHub Actions在对应runner构建，主任务完成整体验收后推新预览tag；并行任务不自行发布tag或改旧Release。

## 文件分工

- 服务端任务：cmd/、internal/ 的CLI扩展与测试；desktop/windows/ 的WinForms启动器及其测试/构建说明。不要改脚本、workflow、Mac或README。
- 网页任务本轮负责macOS启动器：仅desktop/macos/ 与相关测试说明；不要改web/、服务端、Windows或workflow。
- 研究任务本轮负责安装打包：仅scripts/release*、.github/workflows/、packaging/、docs/research/desktop-packaging.md，协调原文声明；不改服务端或启动器实现。
- 主任务：本契约、顶层中英文README、整体验收/协调。所有任务保持gpt-5.6-sol，明确尚未实测的UI/平台行为。仅提交自有路径，不提交其他并行目录。

## 就绪确认补充

为避免端口检查与启动之间的竞争导致打开其他本地服务，启动器每次启动生成新的UUID，通过 --instance-id UUID 传给自己创建的服务。配置该参数时 /healthz 额外返回 instance_id，启动器同时确认子进程存活与该值匹配后才能打开网页；默认CLI未设置时维持原来的仅status响应。instance_id是公开、短期的进程标识，不是密码或访问凭据，不影响员工鉴权。参数必须校验为UUID格式。重复启动只通过可信的同用户现有启动器实例打开网页，不根据匿名healthz成功擅自接管未知服务。
