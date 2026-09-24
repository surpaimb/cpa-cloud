# ADR 0004：可移植备份恢复材料与 Windows DPAPI 重新封装

状态：Proposed for F3 implementation

日期：2026-09-24

所有者：F3 离线异机恢复组件

本决策扩展 [ADR 0002：自动备份、版本化密钥托管与恢复演练](0002-automated-backup-key-custody.md) 的
custody model，不改变其 v2 自动备份包格式、provider 运行时语义或 F1/F2 验收结论。

## 1. 决策摘要

F3 第一段在现有独立离线命令 `cpa-cloud-backup` 增加三个 recovery 子命令，不增加 HTTP/API、网页入口、
数据库迁移或后台 worker。它提供：

- `export`：在源 Windows 身份下解封管理员明确列出的 provider 历史版本，把这些 32-byte wrapping keys
  写入一个受恢复口令保护、带认证的恢复材料文件；
- `verify`：只认证并解析恢复材料，不创建 provider store、数据目录或网络请求；
- `import`：同时读取一个自动备份 v2 包和恢复材料，在当前目标 Windows 身份下重新 DPAPI 封装所需历史
  key versions，恢复数据库到新目录，并把两者作为一个全新 recovery root 原子发布。

恢复口令只从标准输入读取，不接受 argv flag、环境变量默认值或交互回显；wrapping key、口令、DPAPI blob、
根密钥、员工 Key、上游凭据和数据库内容均不得输出或记录。恢复材料只包含明确选择的自动备份 wrapping
keys，不包含 `master.key` 或数据库；自动备份包仍是恢复业务数据的唯一来源。

本阶段唯一重新封装目标仍是 `windows-dpapi-user`。Linux/macOS 只允许编译并明确返回 unsupported，不提供
明文 fallback。外部 KMS、托管密钥、非 Windows provider、远程上传和在线 API 均留待后续。

## 2. 信任与威胁边界

### 2.1 源身份与导出操作者

`export` 必须在能够解封源 provider store 的 Windows 用户身份下运行。CLI 的成功只证明当前进程可读取并
认证所选版本，不证明操作者在组织流程中已获授权；文件系统 ACL、管理员登录和离线介质管理仍是部署责任。

源 provider store、源 CPA Cloud data directory 和现有自动备份包全部只读。导出不轮换、不激活、不删除
provider version，也不打开 CPA Cloud 服务。恢复材料输出必须不存在，并使用受限同目录临时文件、完整
file sync、no-overwrite 原子发布及目录 sync。

恢复材料是高价值离线 ciphertext。获得文件的攻击者可以离线猜测恢复口令；因此继续使用已审查的固定
scrypt 参数和 AES-256-GCM，文档要求高熵独立口令，并不得把恢复材料与口令保存在同一介质。

### 2.2 目标身份与导入操作者

`import` 必须在最终运行 CPA Cloud 的目标 Windows 用户身份下执行；重新封装的 DPAPI blob 绑定该身份。
输入的自动备份包和恢复材料均视为不可信文件，先做有界结构解析和 AEAD 认证，再创建任何最终目标。

目标参数是一个不存在的 `--target-root`。成功布局固定为：

```text
<target-root>/
  data/             # 恢复后的 cpa-cloud.db 与 master.key
  provider-store/   # 当前目标身份重新封装的版本文件
```

运维随后以 `--data-dir <target-root>/data` 与
`--backup-key-provider-dir <target-root>/provider-store` 启动服务。单一新根允许先在同一父目录下完成全部
staging，再用一次 no-overwrite rename 发布。现存目标、文件系统根、UNC/device namespace、非固定本地卷、
symlink/junction/reparse parent、父目录身份变化和跨卷发布全部拒绝。

### 2.3 网络与应用状态

三个子命令都必须零网络；不启动 App、worker、定时测试、账号恢复或上游请求。恢复数据库继续复用已验收的
`backup.RestoreWithKeyMaterial` 清理语义：管理员 sessions 和未完成 OAuth 授权失效，`in_progress` refresh
变为 paused，未知任务不重放。导入后第一次启动仍由正常 App migration/recovery 处理 interrupted 状态。

## 3. Provider/version 绑定和历史窗口

导出要求显式 `provider-id` 与严格递增、去重的 version 列表。每个版本必须：

1. 在源 store 中存在并成功 DPAPI unprotect；
2. 返回相同 provider ID、`windows-dpapi-user` kind 和 version；
3. 位于 `1..9007199254740991`。

恢复材料 header 认证固定 purpose、provider ID、kind 和完整 version 列表；payload 中按该列表顺序存放固定
32-byte keys，并包含加密的 source-instance binding。
任何错 provider、错 version、重排、重复、遗漏、header 篡改、ciphertext 篡改或截断统一返回非敏感认证/格式
错误，不暴露哪个 key 或字段接近正确。

运维必须导出：

- 要恢复的 v2 包 header 引用的精确 key version；
- 该包内数据库 `backup_key_providers.active_version` 引用的版本；
- 恢复后仍需读取的所有保留历史包版本。

`import` 最低限度强制前两项都在恢复材料中，并验证恢复数据库 provider row 的 ID/kind。其余历史窗口由显式
version 列表保留；导入不会删除非 active 版本。缺少某个历史版本只影响该版本的旧包，不能被描述为完整
灾备。恢复材料不接受 wildcard、范围或“扫描所有文件”；源版本选择和目标写入均以精确列表驱动。

源端在 export 后继续轮换不会改变既有恢复材料；新版本必须通过新的显式 export 纳入。目标 import 不推进
active version、不生成额外随机版本，也不销毁旧版本；恢复数据库中的 active version 保持权威，后续轮换
继续使用 F1/F2 的 CAS/prepare/commit 流程。

## 4. 恢复材料格式 v1

格式复用项目已经审查的 scrypt + AES-256-GCM 构造，不引入新依赖或自定义密码原语：

- scrypt：`N=32768, r=8, p=1, keyLen=32`；
- salt：每个文件 `crypto/rand` 生成 16 bytes；
- AEAD：AES-256-GCM；nonce 每文件随机 12 bytes；
- password：1..1024 bytes；version count：1..512；文件最大 128 KiB；
- KDF/AEAD 参数固定，parser 在运行 scrypt 前拒绝未知或非默认参数。

大端 header：

```text
magic[16] = "CPACLOUD-KEYREC\0"
format_version u16 = 1
header_size u16
purpose_id u16 = 1             # automated backup key portability
kdf_id u16 = 1                 # scrypt fixed profile
aead_id u16 = 1                # AES-256-GCM
provider_id_length u16
provider_kind_length u16
version_count u16
reserved u16 = 0
ciphertext_length u64
salt[16]
nonce[12]
provider_id[...]               # ASCII identifier, max 64
provider_kind[...]             # "windows-dpapi-user"
versions[version_count] u64     # strictly increasing
ciphertext[...]                 # includes 16-byte GCM tag
```

完整 header（含 provider/version 列表、salt、nonce 和 ciphertext length）作为 GCM AAD。认证 plaintext 为：

```text
payload_magic[16] = "CPACLOUD-KEYSET\0"
version_count u16
reserved u16 = 0
instance_binding[32]
keys[version_count][32]
```

`instance_binding = HMAC-SHA-256(key = source master.key file bytes,
"cpa-cloud/recovery-instance/v1")`。它只存在于 AEAD ciphertext 内，不打印、不进入 argv；import 在恢复数据
stage 后从其 `master.key` 重新计算并常量时间比较。这样同 provider/version 名称来自另一实例时也不能被混用。

解密后再次核对 purpose、count、固定长度、payload magic，并为每个 key 建立 header 中的精确引用。错误口令与通过基本
结构检查后的 AEAD/header/ciphertext 损坏统一返回 `recovery material authentication failed`。所有可变 key、
派生 key、plaintext 和 password 副本在最短生命周期后清零；Go 值复制无法承诺所有历史副本物理清零，
因此 API 禁止缓存、跨 goroutine 传递或日志格式化 material。

## 5. 离线核心和 CLI 边界

新增核心包建议为 `internal/recoverymaterial`，窄接口为：

```go
type Reference struct {
    ProviderID   string
    ProviderKind string
    Versions     []uint64
}

func Export(ctx context.Context, sourceStore, sourceDataDir, output, providerID string, versions []uint64, password []byte) (Reference, error)
func Verify(ctx context.Context, input string, password []byte) (Reference, error)
func Import(ctx context.Context, input, backupPackage, targetRoot string, password []byte) (Reference, error)
```

`keyprovider.Store` 可新增一个只接受完整 `keyprovider.Material` 的 prepare/verify 原语，用于把已认证的 raw key
重新 DPAPI 封装；它沿用现有 protected DACL、固定卷、无 UI、原子 no-overwrite 和 rollback 逻辑。该原语
不接受文件路径、不写数据库、不激活 provider。

CLI flags 仅为非秘密，并作为现有 `cpa-cloud-backup` 的新子命令：

```text
cpa-cloud-backup key-export --provider-store DIR --data-dir DIR --provider-id ID --versions 1,2 --output FILE
cpa-cloud-backup key-verify --input FILE
cpa-cloud-backup key-import --input FILE --backup FILE --target-root NEW_DIR
```

恢复口令始终从 stdin 读取一行并有 1024-byte 上限。stdout 只输出格式版本、provider ID、version count 和
成功目标布局；不输出 key、salt、nonce、ciphertext、DPAPI blob、摘要、数据库字段或真实凭据。错误为固定
非敏感文本。CLI 不接受 `--password`、`--key`、provider secret 或任意目标子路径。

共享 `cmd/cpa-cloud` flags、服务 schema、管理 API 和能力标志不在本提交修改范围；若集成需要改名或统一
二进制入口，由独立集成任务决定。

## 6. 导入事务和失败回滚

导入顺序固定：

1. 解析并认证恢复材料（含固定 purpose 和 instance binding）到短生命周期内存；
2. 只读解析自动备份 v2 header，确认 provider ID/kind/version 在材料中；
3. 在 target parent 下创建随机受限 staging root；
4. 使用引用 version 恢复包到 `<stage>/data`，沿用 session/OAuth/refresh sanitization；
5. 从 staged `master.key` 重算 instance binding；只读查询 staged DB 的 `backup_key_providers`，确认同一
   provider、kind 和 active version，且 active version也在材料中；
6. 创建 `<stage>/provider-store`，将材料中的每个版本重新 DPAPI 封装、读回认证并常量时间比较；
7. 复核 target parent 身份与 target 仍不存在；
8. 一次 no-overwrite rename `<stage>` → `<target-root>`，同步 parent；
9. 清零内存材料。

任一步失败只清理本次随机 stage 中已知的 `data` 文件/sidecars、精确 provider version 文件与固定子目录。
不递归删除、不过滤扫描、不删除输入、源 store、backup package、recovery material 或任何既有 target。若安全
清理本身失败，返回 cleanup failure 并保留受限 stage 供人工检查；不能发布 target。最终 rename 或 parent
sync 失败时，仅在能够证明 target 是本次 stage 身份时回滚该精确目录，否则拒绝猜测删除。

成功 import 不是可覆盖的幂等 upsert：同一命令再次指向已经存在的 target 一律返回 conflict，且不比较、
合并或修改现有内容。失败且 target 从未发布时可安全原样重试；如果最终 rename 已发生但 parent sync 返回
不确定，命令返回 `publish_state_uncertain`，保留目标供人工验证，绝不自动删除或再次覆盖。

## 7. 运维恢复步骤与声明限制

1. 在源 Windows 服务身份下停止会改变 provider 版本集合的运维动作，记录当前 active version 和所有仍需
   保留包引用的历史 versions。
2. 使用独立高熵恢复口令运行 `key-export`；离线运行 `key-verify`，再把恢复材料、自动备份包和口令分别保管。
3. 在目标 Windows 最终服务身份下选择不存在的新 target root，运行 `key-import`；不要预建 data/provider
   子目录。
4. import 成功后离线检查布局，以新 `data/` 和 `provider-store/` 启动 CPA Cloud；先保持自动 worker 与
   流量关闭，完成应用迁移、管理员重新登录、历史包 verify/rehearsal 和业务检查。
5. 只有所需全部历史版本都已导出、目标重新封装已验证、目标服务能打开且保留包逐一验证后，才能声明该
   范围的恢复窗口可用。

本功能不声明自动发现历史版本、在线切换、第二台主机身份迁移、跨 OS、云 KMS、远程介质完整性、口令托管
或生产 RTO/RPO。恢复材料丢失、口令丢失、目标 DPAPI profile 丢失和未导出的轮换版本均不可由本实现恢复。

## 8. 验收边界

必须覆盖：

1. recovery v1 round-trip、随机 salt/nonce、错口令、错 purpose/instance/provider/version、重复/乱序/超限版本、header/
   ciphertext 损坏、截断、尾随数据和非默认参数在 scrypt 前拒绝；
2. export 只读取显式版本，缺版本整体失败且不产生输出，输出 no-overwrite；
3. Windows 临时自造 provider keys → export → 全新 target root import → 新 store Resolve 所有版本 → 使用包引用
   版本 Verify/Restore 成功；随后通过现有 prepare/commit/CAS 流程轮换到新 active version，并同时验证轮换前
   历史包仍可解密、新 active version 可创建/验证新包；
4. import 缺 backup version、缺 staged DB active version、provider/kind 不匹配均失败且没有最终 target；
5. existing target、root、UNC/device、可移动/网络卷、symlink/junction/reparse parent、父身份变化、路径覆盖和
   文件替换拒绝；
6. 失败注入覆盖 stage 创建、restore、DB validation、每个版本 re-seal/readback、final publish 和 cleanup；
7. 恢复数据库 session/OAuth/refresh sanitization 保持，零网络且不重放任务；stdout/stderr/argv/DB/输出目录
   扫描没有口令或明文 key；
8. Linux/macOS 明确 unsupported；Windows/Linux/macOS 编译；Go vet；必要专项测试。

没有第二台 Windows 主机或第二个真实用户 profile 时，验收只能称为“同一 Windows 主机、全新目录、当前
profile 下重新封装模拟”。它不证明真实异机/跨 profile 运维流程，更不证明跨 OS、云 KMS 或生产密钥托管。

## 9. 独立来源与许可证

本设计来自 CPA Cloud 规格和公开标准，没有使用禁止的参考实现作为模板。2026-09-24 核对：

- Microsoft DPAPI `CryptProtectData` / `CryptUnprotectData`：
  <https://learn.microsoft.com/windows/win32/api/dpapi/nf-dpapi-cryptprotectdata>
- RFC 7914 scrypt：<https://www.rfc-editor.org/rfc/rfc7914>
- NIST SP 800-38D AES-GCM：<https://csrc.nist.gov/pubs/sp/800/38/d/final>
- Go `crypto/aes`, `cipher.AEAD`, `crypto/rand`：<https://pkg.go.dev/crypto>

实现复用现有 Go 标准库、`golang.org/x/crypto` v0.42.0 和 `golang.org/x/sys` v0.36.0，不新增依赖；其现有
许可证记录继续适用。
