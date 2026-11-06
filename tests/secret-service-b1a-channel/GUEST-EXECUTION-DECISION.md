# C guest execution decision package — route A selected, environment unapproved

状态：**路线 A 与技术基线已选择；实际环境未批准；不是 guest/C 执行合同。**

日期：2026-10-20。初版的精确父提交为
`418fd207506b00a0abd225dabd0bf26306e99e04`，stacked base PR88 为
`598845e71b54cfc6c8597d7493d2e061d339572c`。本次决策落版的精确父提交为
`6c9684b4cd815fdec89753293651cbd45d68650b`；base 不变。本 docs-only 候选的精确提交和
自然 CI 另记于 PR89 正文，不能用父提交的绿色结果替代。

本决策包初版只新增本文，本批仅修改本文；[C 合同](CONTRACT.md)、[来源记录](PROVENANCE.md)、
[fixture](fixture.py)、[channel](channel.py)、[receiver](receiver.py)、
[公开测试](test_channel.py)、[C workflow](../../.github/workflows/secret-service-b1a-channel.yml)、
PR88/旧七回归/pins/产品代码全部不改。不实现 ancestor-negative-observed，
不改变任何缺失/空目录判据、argv、UID、权限或创建前无条件停止 gate。

**PR89 继续 open/Draft；C 必停、0/15、无 C_CASE；生产 NO-GO。**
本文件不批准启动 guest、运行账号 helper/hook、C payload、unit、GNOME、
Secret Service provisioning、真实 Key/config/session/provider 或真实服务。

## 1. 决策输入和不应推导的结论

以下 C #7、Code #486 和旧 E/F 取向为初版父提交的历史记录，不回溯改写。

- 父提交 C #7/run39310503621 的 154/154 Linux 公开测试通过，但 dedicated C
  仍按预声明在创建前 FAILURE。首阶段是 useradd-pre / parent-missing；已取得
  FD 关闭 confirmed、iterator not-acquired；其余三 hook 为 unvisited，0/15。
- 根协调者已核父提交 Code #486/run39310503732 及其他六旧回归 SUCCESS。
  这不是 C PASS，不替代本 docs-only 新头的自然 CI。
- 根转述原 E/F 对冻结实现本轮 P1/P2/P3=0/0/0，范围不是未来执行环境批准。
  E 倾向 A+B，但要求显式修改 fresh/归属/回收合同；F 倾向保留原合同走 A，
  A+B 须得到明确接受。两案在初版地位并列；现行选择见 1.1，保留此旧评议
  不代表新头或未来环境已获 E/F GO。
- 本次父提交 `6c9684b4cd815fdec89753293651cbd45d68650b` 的自然 C #8/
  run39319004000/attempt1 仍为 FAILURE：154 项 Linux 公开测试无 skip；
  useradd-pre / parent-missing，已取得只读 FD cleanup confirmed、iterator
  not-acquired，其余三 hook unvisited，固定失败 c-scan，0/15、无 C_CASE。
  同头 Code #487/run39319004183 及其他六旧回归 SUCCESS，不是 C PASS。
- parent-missing 只是局部查询阶段，不是安全 absent。两次负查询也不能证明
  未来 helper 的绝对路径、执行时无 hooks 或持续不变。本案不沿此观察补丁
  路线解锁执行，也不倒推 C #1–#6 或更早红因。
- D 是本包的组织/平台事实确认接口，不被视为所有基础设施的默认责任人。
  未提供真实责任人、平台能力或批准记录的格子一律为 **unknown**。

### 1.1 从本次起生效的选择；不等于实际环境批准

- 用户直接选择 **A：全程受控一次性 guest**，保留每 case 运行期创建此前
  不存在的 OS 账号/身份、原 fresh/归属门及账号删除验收；不采用 A+B，
  不以预置 case 身份或最后销毁 guest 取代原创建/删除合同。
- 用户把技术环境选型委托根协调者。根依据 I/D/E/F 四份纯只读预审，选择
  **公司可控 amd64 原生硬件 KVM/QEMU＋libvirt 宿主、受信外层控制器、
  独立跨故障域回收者**为技术基线。选择技术类别不是证明实际宿主、版本、
  权限、资源、地域、镜像或回收机制已经存在/合格；这些事实仍 unknown。
- 后续 C 的目标粒度为 **每 case 独占一台一次性 guest**，不得跨 case/run
  复用可写 guest。首个生命周期批则最多只创建 **一台 guest、一次**，仅做
  no-C-payload/no-account-helper 验证；两者都仍须各自明确的后续执行合同。
- GitHub-hosted runner 内 nested VM 不是 GitHub 正式支持的环境；TCG 不具
  QEMU 安全隔离资格，均不作为本基线或失败回退。具备官方 nested KVM 支持
  的云 VM 仅为另行审批备选，不选具体供应商/实例，也不自动切换。
- PR 仓库 public：现有 pull_request workflow **永不直接接入**拥有
  libvirt、宿主或云权限的自托管 runner。受信控制器只执行固定受审程序；
  PR 精确 SHA/摘要仍是不可信输入，不授予执行 PR 脚本、workflow 或 domain
  XML 的权限。不用 pull_request_target/workflow_run 携特权执行不可信
  checkout/artifact；摘要匹配、触发链或人工批准本身不把输入变成受信代码。
  未来 C 载荷执行须另行审核/授权，并严格留在获批 guest 内。

本批只把选择落版，不启动平台、构建映像、运行入口或修改现 C gate。
路线 A 不豁免 helper hooks/cache/tally/ZSys/USERDEL_CMD、argv 兼容、动态
依赖或 check-to-exec 的原闭包要求；不能用外层 VM 洗绿其 unknown/硬阻断。

## 2. 历史并列比较：A 与 A+B（现行仅选择 A）

下表及 2.1 的 A+B 列保留初版比较与语义差异，**不是当前候选或执行授权**。

共同底线：C 的 oracle、sender、匿名管道、receiver 和内层 systemd unit
处于同一个受控 guest。guest 是覆盖 setup、测试、cleanup 的外层边界，
不是仅为 receiver 加一个沙箱。CI/controller 不接触 oracle 或私有 receipt。

| 语义 | A：全程一次性受控 guest＋运行期新建身份 | A+B：同类 guest＋预置每 case 独占身份 |
| --- | --- | --- |
| 原 C 核心 | 拟保留精确合成字节、匿名管道、EOF、单次发送和十五场景 | 同样拟保留这些通道目标；不是 OS 账号工具验收 |
| fresh UID | 在该 guest 的 case 创建前确认账号/UID/GID 未被占用，再由受审入口创建；不承诺数字在所有机器上从未使用 | 身份已存在于固定映像；fresh 只能重新定义为该 guest/运行代次内尚未使用、且分配给该 case 的独占 lease，不能沿用“创建前账号不存在” |
| 身份归属 | 控制器拥有 guest；guest launcher 仅在原成功/归属门满足后认领其新建身份 | 映像构建者拥有预置记录；测试端只能使用已批准 manifest 明确授予的 case lease，不能因查到同名用户就认领 |
| 运行期账号命令 | 仍须独立审查创建、lookup/baseline、删除及传递调用的完整闭包和配置；不是保留旧 argv 就已兼容 | 禁止运行期账号创建/删除及其等价 provisioning；预置工作移入受控映像构建，不自动免除构建期审查 |
| per-case 回收 | 必须分别证明原身份/文件/进程等清理；最终 guest 销毁是额外一门 | 停止 case 进程、清理其获授资源并封存 lease；账号可保留至 guest 销毁，不能称原 userdel/groupdel 成功或 per-case 账号已消失 |
| guest 销毁 | 不能洗绿失败的内部捕获或身份清理 | 作为不同的身份终止边界，必须明确取代哪一项旧回收要求；不能冒称等价账号删除证据 |
| 主要批准点 | 执行平台与原身份路径的独立安全/兼容合同 | 同样的平台合同，加上 fresh/归属/账号回收的明确语义修订 |

初版曾并列每 case 一台 guest 与单 guest 十五 slot；现已选择前者为后续 C
目标，不采用十五 slot。禁止跨 case/run 重用可写 guest/overlay/lease，
不得以最后整机销毁冒称原 per-case 账号删除成功。以下 A+B 的假设要求仅为
历史比较；任何将来重选均须新的人类决定和明确语义修订，不能自动降级。

### 2.1 原十五场景逐项映射（均未执行或接受）

下表的“保留”是下一合同的要求，不是本次实现结果。每行仍须保留目的绑定、
实际 PID/start/UID/executable/cgroup/pipe 检查、邻居不变和完整捕获/关闭门。

| 场景 | 原通道判据拟保留 | A 的身份语义 | A+B 的身份语义 |
| --- | --- | --- | --- |
| positive | 一次完整非空写入、EOF、私有 receipt、owned waits/closes | 使用该 case 运行期新建身份 | 使用 manifest 授予、此前未消费的 case 身份 |
| empty | 拒绝且零发送 | 新建身份不放宽零写门 | 预置身份不放宽零写门 |
| nul | 拒绝且零发送 | 同上 | 同上 |
| cr | 拒绝且零发送 | 同上 | 同上 |
| lf | 拒绝且零发送 | 同上 | 同上 |
| truncated | 原长度/编码拒绝，零发送 | 同上 | 同上 |
| over-limit | 有界拒绝，零发送 | 同上 | 同上 |
| wrong-uid | 拒绝错误身份，目标/邻居不变 | 错误值不能变成认领其他新建/既有用户的依据 | 错误值不能消费其他 slot；不得凭相同数字跨 guest/代次认领 |
| wrong-pid | 拒绝错误进程身份 | 对该新建身份的实际目标绑定 | 对该 lease 中实际目标绑定，不能只凭 manifest |
| wrong-exe | 拒绝错误 executable | 实际对象绑定不变 | 实际对象绑定不变，映像声明不代替运行对象检查 |
| wrong-object | 拒绝错误 stdin/pipe 对象 | held pipe 绑定不变 | held pipe 绑定不变 |
| deadline | 超过原通道期限后拒绝 | guest 内单调时钟与外层生命周期期限分开 | 同左，不能以分配 lease 的时间替代发送期限 |
| duplicate | 第一次符合条件才可完整发送，第二次不再写 | 同一 case 的一次性 purpose，不另建身份规避重复门 | 同一 lease 内的一次性 purpose，不换 slot 规避重复门 |
| early-eof | 真实 EOF/短帧拒绝，不伪造成功 receipt | 不变 | 不变 |
| short-write | 原显式适配器的短写拒绝、不重试 | 仍是受控返回形态，不称真实 I/O 故障注入 | 同左 |

A+B 需要新的身份/回收合同版本；原十五场景名称可保留，但不能把合同变化
藏在同名绿色 marker 中。两案都不证明 GNOME 密码接受、collection/item、
持久 Key、跨 daemon/整机重启恢复或共享 supervisor 的行为。

## 3. CI/controller、guest、unit 三层信任边界

| 层 | 可拥有的能力/职责（提案） | 不得隐含授予或借用的证明 |
| --- | --- | --- |
| CI/controller | 核精确 head/base/attempt，验证批准的映像/入口；先登记资源 lease 再创建；外层网络/设备/挂载策略；独立生命周期与回收审计 | 不把 CI 环境变量当 guest 身份证明；不把 GitHub token、平台凭据、宿主 HOME/workspace/control socket 交给 guest；不得把“job 已完成”当实例/盘/快照已销毁 |
| guest | 仅本次获授可写环境；受控 root 负责批准的 setup/内部取证/cleanup；oracle、sender、管道、receiver 和私有 bus 全在 guest 内 | guest root 不能成为外层隔离/销毁证明的唯一签发者；不把 VM 当零副作用或安全擦除保证 |
| unit | 使用 case 身份和私有 HOME/XDG/bus/namespace，保留 C 的最小权限、AF_UNIX、资源/进程/FD 检查 | unit 沙箱不覆盖 guest root 的前后 setup/cleanup；不把 unit success、cgroup empty 或 UID 释放当整个 guest 清理成功 |

VM 的 guest root 和 unit 内非特权身份不是同一信任层。外层控制器/虚拟化
后端/平台管理员仍被信任；guest 逃逸、宿主/平台内存保留或物理介质擦除
不由 C 证明。不得以普通容器、chroot 或不受支持的模拟模式默默替代 guest。

### 3.1 外层不可由 guest root 解除的限制

下列均为待批准并取证的要求，当前有效配置全部 unknown：

- guest 运行期无虚拟 NIC/网络后端且无出网；云 metadata/身份服务、控制平面、真实会话网络
  不可达。控制必须位于 guest 外的虚拟网络/平台层；guest root 自己设置的
  firewall、PrivateNetwork 或环境变量不是充分证明。
- 不向 guest 暴露宿主可写目录、控制 socket、agent/SSH 凭据、任意设备直通。
  源码输入仅为精确版本的受控只读输入，不等同整个 checkout/HOME 挂载。
- 禁用宿主共享文件系统/共享写入、guest-agent、vsock 和设备直通；仅列明的
  虚拟块设备和受限无秘密结果出口可待审，serial 不得成为任意交互 shell。
  不允许挂载传播回宿主；guest 内各 unit 的 namespace/传播另列门。
- 仅启用经批准的虚拟设备、CPU/内存/磁盘/时间配额。实际后端、加速能力、
  支持的机型及宿主隔离机制必须由平台负责人确认，不能从 runner label 推定。
- CI agent/平台生命周期凭据留在控制器信任域。guest 固定结果出口没有任意
  控制命令、日志上传、文件读取或资源删除能力。

### 3.2 任何实际 guest 启动前的实证门

以下每项必须由具名负责人提交可核实且绑定版本/范围的证据；当前均 unknown，
任一缺失即不创建，不允许先启动再以观察补授权：

- 实际公司可控 amd64 原生宿主、宿主/kernel/QEMU/libvirt 版本与来源、KVM
  可用性和受支持机型、资源/费用/权限，以及有效的非 root QEMU＋MAC 强制
  隔离与资源上限；不能只凭 label、安装成功或配置声明认定生效。
- 不可变映像/启动输入、独立签名/摘要验证、构建与 init/first-boot/服务/命令/
  配置/依赖闭包及许可证；首个生命周期批连委托 provisioning 都不得调用
  账号 helper。源码发布关联或映像摘要不证明所有启动行为已经获批。
- guest 外不可解除的无 NIC/出网/metadata、无共享写入、无 guest-agent/
  vsock/设备直通策略；受限无秘密结果 schema 与捕获/完整性/关闭规则。
  不为验证这些门访问真实 metadata、凭据或生产网络。
- **创建前登记**的精确 lease/资源归属 inventory；独立跨故障域回收者在原
  控制器失效后仍能查证和回收的受审实证，以及不误删、失败升级和隔离不复用
  规则。只启动同 job 子进程、finally 或 guest watchdog 不满足此门；没有
  既有合格证据时，须先另行批准基础设施验证，不能拿本批首台 guest 冒险补证。
- 实例、可写盘/overlay、快照、保存态及其他获授可写资源逐项终态；基底映像
  与明确保留的审计日志另列。不以实例消失或 runner 注销代替盘/快照销毁。
- compute、映像/可写存储与日志各自地域、168h 日志政策、具名负责人与后备
  联系人、费用和最小权限批准；区域/保留/回收 unknown 不能因“无 C”豁免。

## 4. 固定映像、来源和外部命令闭包

映像名称/摘要/签名信任根、构建配方、供应方、OS/kernel/systemd/Python/D-Bus/
NSS/动态库版本、有效配置、地区及实际资源后端目前均 **unknown**。1.1 已选
原生硬件 KVM/QEMU＋libvirt 技术基线，不等于选择了实际宿主/云、映像或套餐；
不得从一次启动观察生成 allowlist。下表 A+B 差异列仅保留历史比较。

批准前须提供不可变映像引用、独立核验方法、源码 head 绑定、组件来源与
第三方许可证清单。构建期间允许的网络、包源和命令也需独立受控；运行期
不临时安装/更新依赖，不以 mutable latest 或包名代替字节/配置绑定。

| 执行域 | 必须闭合的来源/命令/配置范围 | A 与 A+B 的差异 |
| --- | --- | --- |
| CI/controller | checkout/action、源码输入生成、虚拟化/平台 API、网络/设备配置、结果验证、销毁及日志投递 | 两案都须批准；特权平台 API 不进入 guest |
| 映像构建 | OS/包管理、init/first-boot、服务默认启动、账号准备、依赖/配置/许可证和映像封存 | A+B 的预置账号只允许在获批构建闭包内产生；不是把风险搬出记录范围 |
| guest 启动和 launcher | init/systemd/journal、NSS、私有 bus、Python、配置加载、委托服务及所有子进程 | 两案都不允许未知启动链自批准；下一生命周期批尤其不得借 first-boot 绕过“无账号 helper” |
| case 身份 | useradd/userdel/groupdel、runuser、相关 cache/tally/ZSys/USERDEL_CMD、hooks、库及配置的传递调用 | A 须逐项批准；A+B 运行期账号创建/删除闭包须为空，但仍需审查其他 lookup/manager/服务作用 |

当前源码 publication profile 的可信发布关联不等于未来映像/配置/所有外部
作用都已批准。原 argv 兼容性也不能由 A 自动解决。cache/tally/ZSys、
USERDEL_CMD、动态依赖及 check-to-exec 在现 C 中继续 unknown/硬阻断；本文
不豁免、不更改、不执行它们，不读取任何当前真实 defaults/configuration。

## 5. 同 guest 的通道与无秘密固定出口

oracle 的生成、保存、sender、匿名管道、systemd-run --pipe、receiver 的 stdin
和私有 receipt 比较必须在同一 guest 中闭合。不得经平台 API、guest agent、
vsock/serial、argv/env、磁盘或日志传输 oracle 后仍称为原匿名管道路径。

外层只能接受预先审查的、有界固定 schema/enum 结果；未知字段、自由文本、
截断、输出失败、重复/错代次/错 head/错 guest 结果全部拒绝。不回传原始
guest/helper/journal/file 内容、异常、真实路径/身份、oracle、receipt 或其
派生值。任何新增公开生命周期字段都须下一合同先行批准，不由本文扩展当前
三 helper/四 hook、每行最多 768 ASCII 的诊断权限。

guest 内 oracle 保留到原规定的进程停止、wait/close、完整捕获、扫描和内部
cleanup 完成。外层销毁后不能声称 oracle 又扫描过被销毁内存/存储；控制器
仅证明其拥有的生命周期资源达到经批准的终态。两种证据不得互相代替。

## 6. 内部证据与外层销毁分门

| 门 | 必要证据 | 不能洗绿的失败 |
| --- | --- | --- |
| 内部捕获 | 所有指定 stdout/stderr/journal/文件/完成 surface 已有界完整取得；未知、缺失、溢出和关闭失败保持不完整 | 局部扫描 clear 不等于 complete；公开测试绿不等于 VM 场景绿 |
| 内部进程/FD/资源清理 | 对确实获授对象完成 stop/wait/close 和相应归属检查；首错保持；消费后不重试关闭 | 物理清理成功不消除捕获/输出丢失，不能认领未知部分副作用 |
| 身份结束 | A：原账号删除/归属门另核；A+B：lease 封存与 guest 终止，按修订合同报告 | A+B 不产生 userdel 成功证明；UID 不再可见不等于持久恢复或全部副作用已消失 |
| 外层实例结束 | 由 guest 外的权威控制面核对精确 lease 的停止/删除状态；不是仅凭 guest 自报或 CI 完成 | 缺失内部结果仍为红；只停机/断开连接不能称实例销毁 |
| 可写盘/overlay/快照/保存态等结束 | 对本 run 确认拥有的逐项资源核对删除/释放；预先批准的只读基础映像和审计日志另有保留策略 | 删除实例不自动等于删除所有盘/快照/备份；资源删除也不是物理安全擦除 |
| 外层结果/审计交付 | 固定结果绑定 run/head/base/attempt/guest 代次；交付完整、去重且来源受控 | 结果丢失或身份不明不能根据 guest 消失补造 PASS |

## 7. 生命周期状态机和失败门（提案，未实现）

正常路径：UNAPPROVED -> LEASED -> CREATING -> IDENTIFIED -> CONTAINED ->
LIFECYCLE_PROBE_DONE -> INTERNAL_FINALIZED -> DESTROY_REQUESTED ->
DESTROY_VERIFIED -> LIFECYCLE_EVIDENCE_ONLY。

UNAPPROVED 只能在平台事实、具名批准和新明确合同全部齐备后离开。
LIFECYCLE_EVIDENCE_ONLY 不是 C_CASE、C PASS、环境全局认证或生产 Ready。

| 事件/阶段 | 所需行为和保留状态 | 失败门 |
| --- | --- | --- |
| 创建前 | 先固定 run/head/base/attempt、映像、入口和私有资源 lease/归属记录 | 映像、授权、配额、隔离策略或回收者 unknown：不创建 |
| CREATING 确认丢失/部分成功 | 保留“可能已创建”及原错；交独立回收者按先前 lease 查证 | 不盲目重复创建，不凭名称收养/删除无关资源；未闭合则 ORPHANED_UNKNOWN |
| IDENTIFIED/CONTAINED | 实例与映像/输入/代次精确绑定；仅批准的公开隔离见证可进入下一阶段 | 错对象、错头、共享写入、可解除外层限制或身份不明：FAILED_CLOSED |
| 取消/超时 | 停止后续工作，保留 CANCELLED/FAILED 首状态，仍进入 DESTROY_REQUESTED | 不因随后销毁成功改成成功；销毁证据缺失则 ORPHANED_UNKNOWN |
| guest 异常退出/内部捕获或关闭失败 | 保留原内部错误和完整性丢失，继续全部确实拥有的清理 | 不运行下一 case，不借外层删除补齐内部证据 |
| 控制器失联/CI 被杀 | 独立跨故障域 lease/期限回收者继续核对并回收所属资源 | 不能只依赖原 controller/宿主存活、该 job 的 finally、webhook 及时到达或 guest 内 watchdog |
| 结果丢失/截断/重复/错代次 | 记录 RESULT_LOST/FAILED_CLOSED；继续外层清理 | 不从 exit/实例消失推造结果；不执行第二次测试来补造第一次结果 |
| 销毁调用失败或状态含糊 | 保留首错和未确认资源 inventory；独立回收并升级具名负责人 | 不对可复用旧 handle/名称盲删；失败资源不得进入下一 run 的复用池 |
| 孤儿资源 | ORPHANED_UNKNOWN，隔离该 lease 并阻止复用，直到独立权威证据关闭 | 不能按“未发现”或时间已过改称 destroyed；未确认不成功 |
| DESTROY_VERIFIED | 分别核实例、可写存储、快照/保存态和结果交付 | 任一门 unknown/failed 均不进入成功汇总，原始错误不被清理错误替换 |

创建/测试不重试；独立回收如何进行权威查询、幂等删除、期限和人工升级，
须另定平台合同及具体上限，当前为 unknown。资源销毁的重复操作不得等同于
对消费后的 FD 重试；两者均须有明确所有权和不误删的判据。

## 8. 给 D 的具名待确认责任矩阵

以下是待填真实姓名/团队的责任席位，不是假造的人员、账号、平台或地域。
D 负责汇总既定路线与待确认平台事实；实际权限归属必须由相应负责人确认。每项批准
记录须绑定范围、版本、时限和撤销方式；未闭合前保持 unknown/不运行。
每个 unknown 都须补真实姓名或获认可的负责团队及后备联系人；单填“D”
不构成平台、映像或安全负责人的具名批准。

| 责任席位 | 具名 accountable owner | D 必须取得的确认/证据 | 当前状态 |
| --- | --- | --- | --- |
| 组织路线/技术基线决定 | 用户直接选择 A；根协调者受托技术选型；D 为汇总接口 | A 保留运行期新建身份与原账号删除；公司可控 amd64 原生硬件 KVM/QEMU＋libvirt、受信外层控制器、独立跨故障域回收者；后续 C 每 case guest，首生命周期批仅一台无载荷 guest | 路线/技术类别已选择；实际环境未批准，后续执行合同未授权 |
| 平台与 CI/controller | unknown | 后端/租户/runner 权限、加速与受支持隔离能力、配额/费用批准、控制面身份与 lease 机制 | unknown |
| 映像与供应链 | unknown | 构建者、不可变映像引用、验证信任根、来源/许可证/组件和启动/命令/配置闭包、更新与撤销流程 | unknown |
| guest root 授权 | unknown | 谁可成为 guest root、固定入口/命令与禁止宿主控制能力；未来 A 身份操作须另批，首生命周期批不含账号 helper/provisioning | unknown |
| 网络/metadata/挂载/设备安全 | unknown | guest 外不可解除的策略、无秘密公开负例见证、无共享写入/直通/控制 socket、例外审批 | unknown |
| 地域/数据治理 | unknown | compute、映像、可写盘/快照和日志各自允许地域/服务商、跨地域限制和保留/删除职责 | unknown |
| 日志 168h 保管与访问 | unknown | 外部固定生命周期/runner 运维审计的 168h 提案窗口；计时起点、最低时长或固定到期、访问/完整性/脱敏/删除/例外留置规则 | unknown |
| 独立回收与孤儿值守 | unknown | 跨故障域、不依赖 guest/原 CI controller/宿主存活的回收者，创建前 lease/私有 inventory，取消/失联/销毁失败实证、期限、升级和不误删证明 | unknown |
| 结果/失败门审核 | unknown | 私有绑定与公开 schema、无秘密出口、各清理门不互相洗绿、结果丢失保持失败 | unknown |

168h 是本提案待确认的保留窗口，不是现有配置或 GitHub 强制时长的声明。
日志存储/地域/保管人/删除人均 unknown。禁止以此为由导出 oracle、receipt、
原始 C/guest/helper 内容、凭据或真实配置。控制器运维日志另需预审无秘密
投影；不能无审查地开启 debug 或把任意原始日志当固定结果出口。保留的
审计记录是明确列出的外部资源，不得声称“guest 销毁后所有数据均不存在”。

## 9. DynamicUser 暂非首选

它改变身份分配时点及 UID 生命周期，而非原账号路径的等价开关。原 unit
启动前目录/归属准备和 baseline、动态 UID 复用/静态同名回退、管理目录之外
的残留及 D-Bus 策略兼容均须新合同。释放动态 UID 不是原账号删除证明，
不能沿用跨 unit/daemon 重启的持久身份或 Secret Service/Key 恢复证据。

即使将来另选 DynamicUser，它也不自动覆盖 unit 外 setup/cleanup 或提供
guest 隔离。本文不作该第三路线的实现选择或兼容性承诺。

## 10. 下一可执行批的最大范围——仍未授权

路线 A 已选择；只有 3.2 的平台事实/具名责任实证经独立评议闭合，并取得
**后续明确合同**之后，才可考虑：**仅一台 guest、一次创建的无 C payload、
无账号 helper 生命周期验证批**。它不是十五个 C case，也不批准多 guest、
第二次创建、创建重试或销毁后重建；这些都需要新合同。
它不是执行原 C、修参数或创建测试身份的授权。

候选范围仅包括零创建的固定输入/映像拒绝与失败状态模型，以及一条事先固定
的单 guest 创建/识别/公开非秘密见证/回收生命周期路径。取消、超时、控制器
失联、结果丢失、部分创建及孤儿回收须逐项区分模型与实际观察；单台 guest
没有覆盖的实际分支继续 unknown，不把适配器返回形态称真实平台 I/O 注入。
不得调用 useradd/userdel/groupdel/runuser/hooks 或等价 provisioning，不能
以 first-boot/包脚本/服务委托绕过限制；启动闭包无法证明满足则不运行。
不安装运行期依赖，不生成/传送 C oracle、password/receipt，不建 C case
unit，不接真实 metadata/凭据、真实服务或生产网络。允许的生命周期 API、
公开见证目标、调用/时间/资源上限也必须由新合同固定，当前全部 unknown。

后续 A 的 C 身份路径、一个正向 C、其余十四场景，均需再各自
明确授权与验证；不得从生命周期 evidence-only 结果自动推进。首个非预期红
冻结对应源码/合同，停止后续 case，只做已授权的确实 owned 清理和只读取证。

## 11. 权威来源与独立撰写 provenance

本文由本项目 Codex 集成任务于 2026-10-20 独立撰写，依据项目 C 合同、
根提供的历史评议摘要、I/D/E/F 四份只读预审汇总、用户路线决定与根受托技术
选型和下列公开接口文档。已选路线/技术类别与待批准实施要求分开记录，不是对当前
组织能力/配置的实测事实。不复制或移植 CLIProxyAPI、Sub2API、归档 CPA、
参考树的实现/测试/文档；既有研究不支持严格 clean-room 声明。

没有新增运行依赖、vendored 实现或许可证变更；任何未来映像/依赖仍须保留
各自许可证和来源。本批不构建、发布映像/软件包或部署，也不读取真实平台
配置、凭据或用户环境。以下来源只支持其接口事实，不批准本平台使用它们：

- [GitHub-hosted runners](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)：
  标准 runner 的 VM/容器区别及平台能力；fresh job 本身不证明本文的全程边界。
- [GitHub hosted runner concepts](https://docs.github.com/en/actions/concepts/runners/github-hosted-runners)：
  nested VM 虽技术上可能，但不获正式稳定性/性能/兼容支持，不作为本基线。
- [GitHub secure use](https://docs.github.com/en/actions/reference/security/secure-use)：
  public PR 与特权自托管 runner 的风险；触发器/环境审批不构成不可信代码隔离。
- [GitHub custom images](https://docs.github.com/en/actions/how-tos/manage-runners/larger-runners/use-custom-images)：
  自定义映像的组织/套餐、策略与管理权限要求；本项目是否具备仍 unknown。
- [GitHub ephemeral self-hosted runners](https://docs.github.com/en/actions/reference/runners/self-hosted-runners)：
  单 job 注册生命周期、外部日志建议与组织自有回收责任；自动注销不是擦除。
- [QEMU security model](https://www.qemu.org/docs/master/system/security.html)：
  受支持虚拟化隔离的边界/条件；不把不受支持模拟自动当安全 guest。
- [libvirt domain lifecycle](https://libvirt.org/html/libvirt-libvirt-domain.html)：
  transient domain 与外部控制生命周期接口；不表示实际资源或销毁证据已具备。
- [libvirt QEMU driver](https://libvirt.org/drvqemu.html)、
  [domain XML](https://libvirt.org/formatdomain.html)：
  非 root/MAC 与资源配置、关联存储/保存态边界；不批准执行 PR 提供的 XML。
- [Google Cloud nested KVM](https://docs.cloud.google.com/compute/docs/instances/nested-virtualization/overview)：
  官方支持的 nested KVM 仍有实例/架构条件，仅说明另审备选存在，不选择该云。
- [VM 与关联资源删除](https://learn.microsoft.com/en-us/azure/virtual-machines/delete)：
  实例、磁盘、网络资源和备份的不同保留/删除语义，仅作权威平台实例说明。
- [systemd 255 execution](https://www.freedesktop.org/software/systemd/man/255/systemd.exec.html)：
  unit namespace/传播及 DynamicUser 生命周期/兼容限制；实际映像版本未选定。
- [systemd-run 255](https://www.freedesktop.org/software/systemd/man/255/systemd-run.html)：
  --pipe 属于 systemd-run；本文仅修正文案，不运行或改变该通道。
- [Linux open/openat](https://man7.org/linux/man-pages/man2/open.2.html)、
  [close](https://man7.org/linux/man-pages/man2/close.2.html)、
  [Python 3.12 os/scandir](https://docs.python.org/3.12/library/os.html#os.scandir)：
  FD/目录枚举/关闭语义不能替代执行隔离或未来无变更证明。

本文没有实施/运行上述验证。提交后的自然同头 CI 只证明其实际执行范围：
C 仍必须在创建前停止，旧绿/公开测试绿不覆盖 C 红；skipped 不算执行。
保留旧 C #1–#8/A3/B0/PR88 红绿与评议历史，不回溯改写旧 CI，也不宣称本次
新头已获 E/F GO。**A 与技术基线已选择；实际环境未批准；C/生产 NO-GO。**
