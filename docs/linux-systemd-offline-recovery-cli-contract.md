# Linux systemd v3 离线恢复 CLI 独立契约

状态：2026-10-19，先规格后实现；基线 PR85 `347efbacd1d63fa8ada92c05f4b31c24bf3ff3a5`。
本片仅建立正式离线入口，Draft / 生产 NO-GO；不借用旧 CI 或旧复审。
继承 [v3 双 VM 契约](linux-cross-vm-backup-contract.md)、[主密钥契约](linux-systemd-master-key-provider-contract.md)及 [备份格式](backup-restore.md)。

## 输入与执行环境

新增 `systemd-v3-verify` 和 `systemd-v3-restore`，原 `create/verify/restore/key-*` 的口令行为不变。
两入口均显式要求 `--input`、`--binding-seed`、`--expect-provider`、`--expect-instance`、
`--expect-master-id`、`--expect-master-version`、`--expect-wrapping-id`、`--expect-wrapping-version`、
`--expect-source-version`；restore 另需 `--data-dir`（不存在的新目录）。
所有身份均非秘密，source-version 必须等于运行二进制的构建版本及包内认证版本。
不接受 Key argv、Key 明文环境、credential 路径参数、`--init`、导出、轮换、原地恢复或自动回退；不读 stdin。

入口仅允许 systemd 系统管理器下固定 unit `cpa-cloud-backup-recovery.service` 和固定本机用户/组
`cpa-cloud-recovery`（真实/有效 UID、GID 均一致且非 0）。要求 systemd 为 PID1、统一 cgroup v2 的精确 unit、
有效 invocation ID、独立网络命名空间和 IPv4/IPv6 stream socket 创建被拒绝。
root 启动者以 `CPA_RECOVERY_HOST_NETNS` 交付真实宿主 netns 的非秘密 `net:[inode]` 身份；
命令核对自身不同，不依赖非 root 通常无法读取的 `/proc/1/ns/net`。这不是用户提供密钥的环境通道。
不允许 socket activation 或继承的 socket；命令不调用业务 App、监听器、worker、HTTP 或上游。
只读取 `/run/credentials/cpa-cloud-backup-recovery.service` 内两个固定名称
`cpa-cloud-master-key` / `cpa-cloud-backup-wrapping-key`；复用生产 reader、角色独立性、canary validator，
不读活跃在线服务 runtime credential。root 管理器配置、恢复授权与保管责任仍是运维信任前提，应用不证明 host-only/TPM/null-key 来源。

unit 以 `RuntimeDirectory=cpa-cloud-backup-recovery`、0700、固定非秘密
`TMPDIR=/run/cpa-cloud-backup-recovery` 提供临时空间；命令前后均须为空，残留独立报 cleanup，禁止自动重试。
需要 Linux openat2（内核 >=5.6）、systemd >=255 及上述沙箱能力；不支持时失败关闭，不降级普通文件 reader。
运维只给目标父目录写权限，包及 seed 显式只读；不得包含真实在线数据目录。

## seed / 身份 / 发布

seed 是事先保留的绑定目录，只含 `master-key-binding.json`；目录 0700、文件 0600、当前 UID/GID、
普通单链接文件、大小 1..4096。拒绝任一级符号/魔术链接，保持目录/文件 FD 和稳定身份、元数据、内容，
生产 validator 前后及操作结束重核。缺失、遗失或不安全即拒绝，不初始化或更改 seed。
精确检查 provider/instance/M1 ID+version/W1 ID+version；M1 与 W1 的 ID 和字节必须独立。
M2 不可通过伪装旧身份绕过原 M1 canary；这不是历史轮换实现。

恢复前先 pin 不存在目标的私有父目录，将无链接/普通单链接/0600/当前 UID+GID 的稳定输入包
复制到该次 unit 私有临时目录；verify 与 restore 只消费同一个受限密文快照，结束按创建对象身份删除。
验证包和 source-version 后再次重核父目录/目标。拒绝目标位于 seed/runtime 下或包含源/seed，
并拒绝目标父目录直接含 `cpa-cloud.db`、`master.key` 或安装 binding 的现有数据目录。
仅调用既有 v3 核心与 PR85 实际对象发布器，不更换最终 canary 检查、净化数据库或回调顺序。
恢复删除旧管理员会话/OAuth 临时会话、旧会话拒绝、未知刷新不自动重放。
不改源包、seed、现有目录或在线 credential；不会启动服务来完成恢复。
发布/回滚仍为 PR85 的 FD 和创建对象身份限定，非电源故障原子性或持续失陷 same-UID/root 对手防护。
发布提交后仅清理失败可能保留有效目标；固定 cleanup 错误不能当恢复未提交而重试。

## 固定结果与停止

新入口 stdout 只有 `OFFLINE_V3 verify ok` 或 `OFFLINE_V3 restore ok`。
错误仅为 `OFFLINE_V3 <stage> failed`；不回显 flag 值、路径、原始错误、密钥、包元数据或数据库内容。
阶段冻结为 `arguments/platform/unit/seed/identity/installation/wrapping/package/target/restore/cleanup/cancelled`。
解析/必需值或版本格式错误归 arguments；不支持平台 platform；unit 身份/隔离/临时目录入场 unit；
seed 元数据/结构/稳定性 seed；显式身份不符 identity；安装 reader/canary installation；
wrapping reader/角色独立性 wrapping；包认证/完整性 package；目标/父目录入场及前置重核 target；
发布器拒绝 restore；关闭/残留/已提交清理异常 cleanup；上下文取消 cancelled。
cleanup 可与先前失败共同发生，输出独立固定 cleanup 行，不掩盖首个失败；成功行须在全部清理后才输出。

任一正例失败、负例错误阶段、意外监听/发布、扫描命中/不完整或清理失败：保留第一次自然专项红灯，
停止后续案例，不修补/重跑/dispatch/换 runner。回报新 head/base、首红及独立清理状态供原 E/F 只读诊断。
全绿也仅停 Draft，等待原可见 E/F 对新精确 head/base 正式复审；无 Ready/merge/tag/package/deploy。

## 验收（计划，不是已测）

- 本地串行检查旧口令 CLI、参数脱敏、无 stdin/服务启动、平台拒绝、seed/目标身份和固定错误；不并行 Windows SQLite 全仓。
- 新自然专项 A=ubuntu-24.04 amd64、B=ubuntu-24.04-arm arm64。各自无共享 cache/二进制，原生编译实际 CLI；
  A 由既有受限夹具生成 v3。B 独立 host key，machine/boot 必须均不同，真实双 credential，CLI verify/restore，
  服务两次独立 reopen 证实记录/管理员/员工权限、旧会话 401、所有权/权限及来源不变。
- CLI 负例：缺失/错误/互换 credential、M2 伪装、身份/版本、canary/包损坏、seed 缺失/不安全/链接、
  已有目标、目标链接及父目录替换。父目录替换由 Linux fanotify permission event 在实际 CLI 第一次包 open 时
  阻塞并替换，确认事件 PID 后放行；不得 sleep 猜竞态、替换 reader 或使用带测试后门的 CLI。
  fanotify 权限事件能力缺失即门禁失败，不跳过。
- 保留原 PR85 28 例真实负例与九组原生发布回归（含 staged 替换、取消、I/O/sync/身份受限清理）。
- 捕获实际 CLI stdout/stderr 与该次准确 invocation journal，扫描实际预登记合成材料的多种编码；
  操作/内容扫描/完整性/unit/身份/目录/relay 清理分别记录，停止 unit 后才删除私有测试对象。
- relay 仍仅四个合成密文/公开文件，不传 host key、明文 credential、数据库、二进制、raw log 或扫描 oracle。

## 来源 / 许可证 / 回滚

本片为依据本项目规格独立编写；未使用归档 CPA/CLIProxyAPI/Sub2API 实现，不声称严格 clean-room。
公开文档于 2026-10-19 查阅：[systemd.exec 255](https://www.freedesktop.org/software/systemd/man/255/systemd.exec.html)
（User/Group、credential、RuntimeDirectory 与隔离属性）、[systemd-creds 255](https://www.freedesktop.org/software/systemd/man/255/systemd-creds.html)
（host 加密交付，不证明生产保管）、[Linux openat2](https://man7.org/linux/man-pages/man2/openat2.2.html)
（禁止路径链接解析）、[Linux fanotify](https://man7.org/linux/man-pages/man7/fanotify.7.html)
（仅验收的真实权限事件 barrier）。公开 API 行为是依据，不复制文档/实现代码。
无新增依赖：Go 标准库及既有 x/sys v0.36.0、x/crypto v0.42.0（BSD-3-Clause）和 modernc SQLite v1.38.2
（BSD-3-Clause；嵌入 SQLite 保留其自身 public-domain 声明）；既有间接依赖与仓库第三方声明不变。

回滚只停用新恢复 unit/入口，回到 PR85；不覆盖或删除源、seed、旧数据及已发布目标，不自动重新恢复。
失败目标/残留须先独立审查并确认所有权、是否提交及后续新写入，再由授权运维处理；CLI 不做自动人工决策。
任何恢复错误（含提交后观察到取消）均不得自动重试；先独立判断目标是否已经有效发布。
生产仍缺真实保管/恢复授权、主机策略、RTO/RPO、演练与 M1→M2 连续性及切换回滚合同。

## 运维执行形态（示意，不执行、不配置生产）

由已授权 root 运维事先创建固定非登录用户/组，确认源包与保留 seed 是交给该身份的独立只读副本，
目标父目录为该身份 0700 且新目标不存在。不要把活跃服务数据目录当 seed。
用 `systemd-run --unit=cpa-cloud-backup-recovery.service --wait --pipe --collect` 短暂执行该实际二进制，
不安装/启用常驻服务、不用 shell 包装密钥、不从业务进程复制 runtime credential。

需要的 unit 属性：`User=cpa-cloud-recovery`、`Group=cpa-cloud-recovery`、`UMask=0077`、
`Restart=no`、`NoNewPrivileges=yes`、`PrivateNetwork=yes`、`PrivateIPC=yes`、`PrivateDevices=yes`、
`PrivateTmp=yes`、`ProtectHome=yes`、`ProtectSystem=strict`、空 `CapabilityBoundingSet=`、
`RestrictAddressFamilies=AF_UNIX`、`LimitCORE=0`、`TemporaryFileSystem=/dev/shm:rw,mode=1777`、
`RuntimeDirectory=cpa-cloud-backup-recovery`、`RuntimeDirectoryMode=0700`、`RuntimeMaxSec=120`、
`TimeoutStopSec=10`、`KillMode=control-group`。
`ReadWritePaths=` 只允许独立目标父目录；`ReadOnlyPaths=` 包及 seed 副本。
root 将同一次启动前读取的宿主 netns 身份交给 `CPA_RECOVERY_HOST_NETNS`，另设上述固定 `TMPDIR`；
不要用此值代替 systemd 创建的真实隔离网络。credential 根路径由 systemd 自动设置，不能用户覆盖。
以两个 `LoadCredentialEncrypted=` 属性分别把经过授权取回的 M1/W1 本机密文 capsule 映射至上述固定名称，
参数只有 capsule 路径引用，不是密钥。组织须另定 capsule 的 host/TPM 策略，不由此 CLI 选择。

ExecStart 形态是 `cpa-cloud-backup systemd-v3-verify [上述必需参数]` 或 `systemd-v3-restore [上述必需参数]`。
标准输入不使用，stdout/stderr 经 `--pipe` 交给授权运维并按固定结果判定；普通 journal socket 输出会被
继承 socket 入场检查拒绝。运维仍须保存该次准确 unit journal、核实停止及 runtime 目录移除，
再处理交付目标；不能仅凭 CLI 退出码启动业务。此说明不构成真实主机执行或生产验收。
