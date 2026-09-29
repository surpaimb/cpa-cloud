# 命名时区每日账号测试契约（OPS-04 第一段）

状态：2026-09-30 开发预览实现契约。本文先定义行为与验收；实际通过情况另记。此增量只扩展[持久定时测试首批](scheduled-tests-contract.md)，不改变其默认关闭开关、测试范围、并发/单账号排他、历史保留或旧 operation 不重放规则。实现依据本项目规格、下列公开官方时间资料和独立编写的测试，不参考其他代理项目代码。

## 输入、输出与旧客户端

管理员既有 `/admin/api/v1/scheduled-tests` 创建、列表、单项、PATCH、归档和运行历史路径不变。管理员会话、写请求 Origin/CSRF、`expected_revision` CAS、100 个未归档计划、`local_credential|catalog` 两类检查、200 条完成历史及固定脱敏错误边界不变。`--scheduled-tests-enabled` 仍默认关闭；关闭时可保存计划但不 claim、不写新运行、不访问上游。

计划增加 `schedule_mode:"interval"|"daily_local"`、`time_zone:string|null` 和 `local_time:string|null`，响应仍有 `next_run_at`（UTC RFC3339 或停用时 null）。`interval` 的 `interval_seconds` 继续为 300–86400 秒；`time_zone`/`local_time` 必须为 null。旧创建请求仍按原五字段解释为 `interval`；旧 PATCH 对 interval 计划只改其显式字段，不改模式。新 interval 客户端可显式传 `schedule_mode:"interval"` 和 `interval_seconds`。不会把已有 interval 计划转换成每日计划。

`daily_local` 创建必须显式提供 `schedule_mode:"daily_local"`、IANA 命名 `time_zone` 和严格 `HH:mm`（`00:00`–`23:59`），不接受 `interval_seconds`。由于旧 SQLite 列非空，每日行内部将 `interval_seconds` 固定为 86400，响应仍保留这一旧字段以维持 JSON 类型；它**不是**每日执行间隔，不得据此推算下一次。PATCH 模式切换须显式给出目标模式及该模式完整的时序字段；daily 计划的时序字段编辑也须显式给出 `schedule_mode`, `time_zone`, `local_time`。缺少模式的旧 PATCH 可改 daily 计划的名称、账号、范围或启停，但携带 `interval_seconds` 编辑 daily 计划必须 400，不能悄悄转换。非法字段组合、未知/非法时区或本地时间返回固定脱敏 400 `invalid_request`；存储或捆绑时区数据故障返回 503 `storage_unavailable`，不部分提交。

`features.scheduled_tests_daily_local=true` 表示新管理 API 和每日语义已接线；网页只有此标志为真才提供 interval/daily 选择。旧服务未返回此标志时，新网页继续只发送旧 interval JSON，零 daily 字段。旧网页/客户端读到新服务新增字段可忽略；它们只对 interval 计划有完整的时序编辑能力，不应把 daily 行中的 86400 占位值解释为真实固定间隔。

## 命名时区和 DST

时区必须是捆绑数据库中实际存在的 IANA 名称（如 `America/New_York`、`Asia/Shanghai`、`Etc/UTC`）；`Local`、数值 UTC 偏移、任意文件路径及空字符串均拒绝。不使用宿主机 `time.Local`、系统 zoneinfo 搜索顺序、进程 `TZ`、安装目录或前端浏览器时区计算调度。服务把经核准的命名区、当地日历日期与 `HH:mm` 转换成 UTC 瞬时，并持久化 `next_run_at`；网页同时显示原始时区/本地时间与服务端返回的 UTC 预定值。

逐当地日历日寻找**严格晚于基准 UTC 时刻**的下一次：春季跳时造成目标本地分钟不存在时跳过该日；秋季回拨造成同一本地分钟出现两次时只选择较早的 UTC 瞬时，即使较早瞬时已过去，也不在当日补取较晚瞬时。其它日期按当地年月日推进，不以 `24h` 加法代替日历日。实现须明确枚举时区偏移区间来判定不存在/重复，不依赖 Go `time.Date` 在 DST 歧义时未保证的选择。闰日、跨年、跨日及非整小时偏移均按同一规则；若在有界未来搜索中无法安全求值则失败关闭，不生成貌似有效的时间。

时区数据来源固定为 Go 1.26.8 官方发行包 `lib/time/zoneinfo.zip` 中编译的 IANA tzdb 2025c：SHA-256 `8F55634D05F8BCA1F7BC7C69C5933428C69357E0BDF565E5BA224E3F88FF12E8`，作为仓库资产嵌入服务并使用 Go 标准库 `time.LoadLocationFromTZData`，而非 `time.LoadLocation` 的宿主机优先搜索。IANA 官方声明 tzdb 数据为公有领域；Go 的生成工具/标准库保留 BSD-3-Clause 条款。资产来源、版本、哈希和发行时声明同时登记在 `THIRD_PARTY_NOTICES.md`。时区政治规则会变更；本批固定数据版本，升级 tzdb 必须另有显式兼容/重排审查，不能暗中改变已持久化预定时间。

公开依据（2026-09-30 查阅）：[IANA Time Zone Database](https://www.iana.org/time-zones)、[IANA tzdb theory](https://www.iana.org/time-zones/theory)、[Go `time` 的 `Date`/`ZoneBounds`/`LoadLocationFromTZData` 文档](https://pkg.go.dev/time)、[Go `time/tzdata` 文档](https://pkg.go.dev/time/tzdata)。其中 IANA 提供命名区和数据版权说明，Go 文档说明 DST 时间的歧义及标准库加载接口；本项目的“跳过春季、秋季取较早”是上述资料之上的自定业务规则，不冒充外部协议要求。

## 持久化、claim 与重启

旧库迁移须在一个 SQLite 事务内先验证既有计划/运行表、索引和数据，再增补模式/时区/本地时间列并逐行验证；所有旧行保持 `interval`、原 `interval_seconds`、revision、`next_run_at` 与历史不变。迁移失败回滚，原库不出现半升级；损坏或部分升级结构失败关闭。新安装生成同一最终结构。每日行的模式与字段组合在管理入口及启动/读取校验，非法旧库数据不能被悄悄改成 interval。迁移不触发网络，不改变旧运行记录和结果码。

创建/配置编辑/重新启用时，从该事务的当前 UTC 时钟求严格未来的 `next_run_at`；停用/归档置 null。worker 只读取持久 UTC 到期值，claim 在单事务内按 CAS 推进下一次并插入唯一 `running` operation；最多两个并行，同一账号排他，执行前再核对账号/计划 revision。停机错过多个当地日期只对当前 due claim **一次**，随后从 claim 时刻求严格未来一次，不逐日追赶；时钟回拨不会撤销已推进的时间或重复当日早一瞬时。旧 interval claim 仍按当前 UTC 加固定秒数。配置改动、停用、账号归档及服务关闭在提交后取消本进程正在运行的 operation，旧 revision 结果不能覆盖新计划。

重启将旧 `running` 历史标成 `interrupted`；仍启用的关联计划至多安排一次新 operation 补跑，绝不重用旧 operation ID；之后按所选模式计算严格未来一次。默认关闭时即使存在 due 也不启动 worker。新旧部署回退遵循完整数据目录与程序一起恢复升级前备份，旧程序不得直接打开含新模式列的库并静默忽略每日计划。

## 验收边界

独立 Go 测试覆盖：新旧 API JSON/CAS/CSRF/Origin、旧库原值迁移及故障回滚、坏/部分结构、时区名及数据失败、春季缺时/秋季双时取早、跨日/年/闰日/半小时偏移、停机多日只补一次、时钟回拨、claim 竞争、配置变更/停用/归档取消、重启不重放旧 operation、200 历史和默认关闭零出站。Web 测试覆盖能力门控、旧服务零 daily 字段、模式切换/校验、UTC 展示和错误保留；真实 Go + 浏览器桌面及 390px 验证交互和可读性。动态非 8787 回环进程仅使用合成账号/本地假目录；不使用真实供应商、凭据、生成请求、部署或发布。精确 HEAD 的 GitHub 全 Go、实际 CGO race、Web、双 CLI 构建和既有 smoke 另列证据，不以计划代替通过。
