# 管理员只读审计 CSV 导出第一段契约

状态：AUDIT-01 第三段开发契约，2026-09-30。沿用[只读总览契约](admin-audit-overview-contract.md)及[财务第五源增量](admin-audit-financial-source-contract.md)的事实范围、鉴权、筛选、时间、排序、schema 校验和隐私边界；本文件只规定导出增量。实现依据本仓自有规格、Go 标准库 HTTP/CSV 与现有 SQLite 接口，无新依赖、外部协议或参考项目代码。

## 范围与 HTTP

`GET /admin/api/v1/audit/events/export.csv` 是独立的管理员会话只读接口；员工 Key、匿名请求无权访问。沿用管理员 GET 的可选同源 `Origin` 检查，错误 Origin 为 403；不要求 CSRF token。所有成功与错误响应均 `Cache-Control: no-store`，错误为既有固定脱敏 JSON，不包含 SQL、行内容或秘密。新增 `features.admin_audit_csv_export=true` 仅表示本接口可用；网页必须同时具备 `admin_audit_overview=true` 才显示导出控件，缺失时不发送导出请求。第五源筛选仍由独立的 `admin_audit_financial_source` 能力决定，旧服务能力缺失时不得发送第五源 token。

请求允许总览首页的 `from`/`to`（同时提供或同时省略，默认过去 24 小时）、`sources`、`actor_id`、`action`、`target_type`、`target_id`、`result` 精确筛选；规则完全沿用总览，包括 `[from,to)`、最多 31 天、固定五源 allowlist、重复/空/未知参数拒绝、时间和 256-byte UTF-8 元数据限制。导出不接受 `cursor` 或 `limit`，不复用或改变现有 `/audit/events` 的 v2 游标及每页 1–100 规则。所选源的顺序规范化为固定五源顺序；省略 `sources` 表示全部五源。无匹配结果仍返回只有标题行的 CSV。

成功为 `200`，固定 `Content-Type: text/csv; charset=utf-8`、`Content-Disposition: attachment; filename="cpa-cloud-admin-audit.csv"`，响应正文为 UTF-8、无 BOM、CRLF 行尾的 RFC 4180 风格 CSV。固定列序为 `source,event_id,actor_id,action,target_type,target_id,result,revision,occurred_at`，不添加任意源字段。`actor_id` 的空单元格仅表示第五源事实没有关联管理员；旧四源不允许空 actor。`revision` 空单元格表示源事实没有该版本字段，绝不是数值 0。其它非空元数据沿用 API 的精确值；时间为规范 UTC RFC3339Nano。财务 `succeeded` 仅表示本地业务事务提交，不代表外部支付成功。

## 原子性与上限

整个导出使用一次五秒超时的只读 SQLite 事务：先验证全部五源（即使只筛选旧源），捕获全部五张表的 `MAX(rowid)` 水位，在相同事务快照内仅用现有元数据投影按筛选查询，按 `occurred_at DESC`、源顺序 ASC、同源 `event_id DESC` 合并。首页后并发新写、时钟回退或同刻行不得进入此次导出；原始事实、保留规则、财务写入和 events API 均不改变。每个选中源最多读 1001 个候选，以判断总行数是否超过 1000；不无界扫描或静默截断。

导出最多 1000 个数据行，最终 CSV 最多 2 MiB（2,097,152 bytes，含标题及 CRLF）。这是内部管理员在内存完整预生成的有界下载：1000 行足以覆盖常规窗口，双限制避免将 31 天大范围变成无界数据库/内存/网络工作。任一上限超出返回 413 `export_too_large`，提示管理员缩小窗口或筛选；绝不返回前 1000 行冒充完整结果。参数错误返回 400 `invalid_request`。任何源缺失/错 schema/触发器、SQL/扫描/时间/元数据错误、事务提交失败、超时或响应开始前取消均返回 503 `storage_unavailable`。所有行须先经校验、CSV 序列化和字节上限检查，且只读事务成功提交；检查请求 context 后才能设置 CSV 响应头并写首字节，因此这些失败不产生部分 CSV。客户端在响应已经开始后断线不可能回滚已发送字节，不承诺网络层原子下载。

## CSV 安全

所有单元格必须为有效 UTF-8；既有元数据不允许 CR/LF、控制字符、首尾空白。导出进一步拒绝 Unicode 格式控制字符（含 BOM/双向控制）以防隐藏前缀或误读，不替换为貌似正常值。CSV 编码器负责引号、逗号和 CRLF 转义；单元格若以 ASCII `= + - @` 开头，前置 ASCII 单引号，使 Excel/表格软件按文本处理。原值前置的引号是明确的导出安全转义，不是数据库事实的改写；测试覆盖四种危险前缀、引号/逗号、非法 UTF-8、控制/换行，以及无前缀的普通值。空 actor 与空 revision 不套用公式转义。绝不选取/返回财务金额、`payload_digest`、正文、凭据、Key、管理会话或其它表。

## 验收与边界

测试旧库迁移后导出、五源与四源选择、精确筛选/混合精度排序、单事务水位下并发新写、1000/1001 行与 2 MiB 边界、非法/重复/未知/越窗参数、匿名/员工 Key/错误 Origin、no-store、源缺失/坏 DDL/坏触发器/候选坏行/SQL 故障/取消的零部分 CSV、公式注入和敏感值扫描。网页验证新旧能力组合、真实下载、桌面和 390px；临时动态非 8787 回环服务与合成事实做进程验收。最终精确 HEAD 需非缓存全 Go、`go vet`、实际 CGO service 与有状态包 race、Web typecheck/test/build、双 CLI 构建、全 CI、Markdown 链接、`git diff --check` 和独立固定二进制进程核验。

本段只导出现存五源事务元数据，不建立完整统一写审计，不覆盖登录、请求正文、账务最终结果、备份或其它 mutator；没有真实 provider、会员或支付验收，也不部署、发布包或延长保留期。来源研究背景仍按[独立实现规则](independent-implementation.md)披露，不宣称严格洁净室。
