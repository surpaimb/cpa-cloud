# CPA Cloud encrypted backup CLI / CPA Cloud 加密备份命令行

Status / 状态：development preview / 开发预览，2026-09-24。

`cpa-cloud-backup` is an offline administration CLI with `create`, `verify`, and `restore` subcommands. It has no HTTP endpoint and does not give the web console filesystem access. Passwords are read only from standard input; they are never accepted as command-line flags or printed.

`cpa-cloud-backup` 是独立的离线运维命令，提供 `create`、`verify` 和 `restore`。它不增加 HTTP 接口，也不让网页后台读取文件系统。密码只从标准输入读取，不接受密码命令行参数，也不会打印密码。

## Usage / 使用方法

Build locally from the repository; this preview does not publish a binary attachment:

从仓库本地构建；本预览不发布二进制附件：

```powershell
go build -o .\cpa-cloud-backup.exe .\cmd\cpa-cloud-backup
```

PowerShell 7 examples use `Read-Host -MaskInput`, so the password is not a process argument:

PowerShell 7 示例使用 `Read-Host -MaskInput`，密码不会成为进程参数：

```powershell
Read-Host 'Backup password' -MaskInput | .\cpa-cloud-backup.exe create --data-dir C:\CPACloud\data --output D:\Backups\cpa-2026-09-24.cpacb
Read-Host 'Backup password' -MaskInput | .\cpa-cloud-backup.exe verify --input D:\Backups\cpa-2026-09-24.cpacb
Read-Host 'Backup password' -MaskInput | .\cpa-cloud-backup.exe restore --input D:\Backups\cpa-2026-09-24.cpacb --data-dir C:\CPACloud\restored-2026-09-24
```

On POSIX shells, avoid putting the password in shell history:

在 POSIX shell 中不要把密码写入命令历史：

```sh
read -rsp 'Backup password: ' CPA_BACKUP_PASSWORD; printf '\n'
printf '%s\n' "$CPA_BACKUP_PASSWORD" | ./cpa-cloud-backup create --data-dir /var/lib/cpa-cloud --output /srv/backup/cpa-2026-09-24.cpacb
unset CPA_BACKUP_PASSWORD
```

`create` requires an initialized CPA Cloud data directory and a new output filename. `verify` authenticates the package, checks the fixed two-record structure, root-key encoding, SQLite integrity, foreign keys, and core CPA Cloud tables. `restore` only accepts a nonexistent new directory; it never performs in-place restore.

`create` 要求源目录是已经初始化的 CPA Cloud 数据目录，输出文件必须不存在。`verify` 会认证包、检查固定的两个记录、根密钥编码、SQLite 完整性、外键和 CPA Cloud 核心表。`restore` 只接受不存在的新目录，绝不原地覆盖恢复。

After restore, start CPA Cloud against the new directory and perform application-level checks before changing traffic. A successful `verify` confirms this format version and database snapshot; it does not promise compatibility with every future CPA Cloud version.

恢复后应使用新目录启动 CPA Cloud，并在切换流量前执行应用级检查。`verify` 成功只确认当前格式版本和数据库快照，不承诺兼容所有未来 CPA Cloud 版本。

## Security and format / 安全与格式

- Creation uses SQLite's online backup API through a read-only source connection. It includes committed WAL state without copying an active database/WAL pair and does not call `service.Open`, run migrations, or start workers.
- The package is a versioned CPA Cloud binary container, not tar/zip. It accepts exactly `cpa-cloud.db` and `master.key`, in that order, and rejects duplicate, extra, truncated, or trailing records.
- A random 16-byte salt and fixed scrypt parameters (`N=32768`, `r=8`, `p=1`, 32-byte key; approximately 32 MiB scrypt working memory) derive the AES-256-GCM key. A random 12-byte nonce is used once. The fixed header is authenticated as additional data; encrypted payload metadata records source program/version, creation time, file sizes, and the authenticated checksum method. Root-key content and digest are not returned or printed.
- Import refuses non-default KDF parameters before running scrypt. Passwords are limited to 1024 bytes, metadata to 64 KiB, SQLite snapshots to 128 MiB, and packages to 130 MiB. The 128 MiB database limit is a deliberate first-preview memory bound, not a recommended production database size.
- Plaintext snapshots exist only in newly created restricted temporary directories. Known files and SQLite sidecars are removed on every normal error/cancellation exit. Output uses a same-directory restricted temporary file, file sync, an atomic no-overwrite hard link, and directory sync where the OS supports it. A failed commit removes the newly linked output.
- Restore rejects an existing target, filesystem roots, symlink/reparse target parents, and parent identity changes observed during verification. It verifies before creating the target and publishes only the two known files. Unix mode is `0700` for directories and `0600` for files. Windows applies a protected DACL granting full access only to the current user, Local System, and built-in Administrators—stricter than the existing root-key creation method's inherited ACL.
- Restore deletes administrator sessions and unfinished OAuth authorization sessions in the staged copy. A Codex refresh recorded as `in_progress` is changed to `paused` with reason `backup_restore_uncertain_refresh`; restore never calls an upstream or replays a refresh token. Completed OAuth bindings, employee-key digests, encrypted upstream credentials, pools, prices, ledgers, governance, and budget data remain in the snapshot.

- 创建通过只读源连接使用 SQLite online backup API，包含 WAL 中已经提交的状态，不直接复制活动数据库/WAL 文件对；它不会调用 `service.Open`、执行迁移或启动 worker。
- 包是带版本的 CPA Cloud 自有二进制容器，不是 tar/zip。只接受按固定顺序出现的 `cpa-cloud.db` 和 `master.key`，拒绝重复、多余、截断和尾随记录。
- 随机 16 字节 salt 与固定 scrypt 参数（`N=32768`、`r=8`、`p=1`、32 字节密钥，scrypt 工作内存约 32 MiB）派生 AES-256-GCM 密钥，每个包使用随机 12 字节 nonce。固定头作为附加认证数据；加密元数据记录来源程序/版本、创建时间、文件大小和受认证的校验算法。根密钥内容和摘要不会返回或打印。
- 导入在运行 scrypt 前拒绝非默认 KDF 参数。密码上限 1024 字节，元数据 64 KiB，SQLite 快照 128 MiB，包 130 MiB。128 MiB 是首批预览的明确内存保护限制，不是生产数据库容量建议。
- 明文快照只短暂存在于新建的受限临时目录。正常错误/取消出口会清理已知文件和 SQLite sidecar。输出使用同目录受限临时文件、文件同步、原子无覆盖硬链接提交，并在操作系统支持时同步目录；提交失败会删除本次新链接的输出。
- 恢复拒绝已有目标、文件系统根、父路径中的 symlink/reparse，以及验证期间发生的父目录身份变化。先验证，再创建目标，只发布两个已知文件。Unix 目录/文件权限为 `0700`/`0600`；Windows 使用受保护 DACL，只向当前用户、Local System 和内置 Administrators 授予完全访问，严格于现有根密钥创建方式的继承 ACL。
- 恢复会在暂存副本中删除管理员会话和未完成 OAuth 授权会话；`in_progress` Codex 刷新会改成 `paused`，原因为 `backup_restore_uncertain_refresh`。恢复不访问上游、不重放刷新令牌。已完成 OAuth 绑定、员工 Key 摘要、加密上游凭据、池、价格、账本、治理和预算数据保留。

On Windows, Go/Win32 does not provide a portable guarantee that flushing a directory handle succeeds on every filesystem. The implementation always flushes every completed file and requests a directory flush; `ERROR_ACCESS_DENIED`, `ERROR_INVALID_HANDLE`, or `ERROR_INVALID_FUNCTION` from directory-only flushing is treated as a documented platform limitation. This does not weaken no-overwrite behavior or file-content syncing, but sudden-power-loss durability of the final directory entry remains filesystem-dependent.

Windows 上并非每种文件系统都允许通过 Go/Win32 成功刷新目录句柄。实现始终同步每个完整文件并请求目录刷新；如果仅目录刷新返回 `ERROR_ACCESS_DENIED`、`ERROR_INVALID_HANDLE` 或 `ERROR_INVALID_FUNCTION`，则按已记录的平台限制处理。这不降低无覆盖保护或文件内容同步，但突然断电时最终目录项的持久性仍取决于文件系统。

## Scope not yet delivered / 尚未交付范围

Scheduled backups, retention, object storage, remote upload, key rotation/re-wrapping, in-place upgrade rollback, and orchestration are not included. This CLI is not completion of OPS-01. Operators remain responsible for protecting package passwords, backup files, host administrator access, and tested off-host copies.

计划备份、保留策略、对象存储、远程上传、密钥轮换/重新封装、原地升级回滚和编排尚未包含。本命令不代表 OPS-01 已完成。运维人员仍需保护包密码、备份文件、主机管理员权限，并维护经过恢复演练的异机副本。

## Independent implementation sources / 独立实现来源

Consulted 2026-09-24; no reference-product source was used:

2026-09-24 查阅；未使用参考产品源码：

- SQLite Online Backup API: <https://sqlite.org/backup.html>
- SQLite WAL documentation: <https://sqlite.org/wal.html>
- RFC 7914, scrypt: <https://www.rfc-editor.org/rfc/rfc7914>
- NIST SP 800-38D, GCM: <https://csrc.nist.gov/pubs/sp/800/38/d/final>
- `modernc.org/sqlite` v1.38.2 public API and BSD-3-Clause license: <https://pkg.go.dev/modernc.org/sqlite@v1.38.2>
- `golang.org/x/crypto` v0.42.0 scrypt package and BSD-3-Clause license: <https://pkg.go.dev/golang.org/x/crypto/scrypt@v0.42.0>
