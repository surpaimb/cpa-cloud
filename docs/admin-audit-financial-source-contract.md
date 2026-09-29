# 管理员只读审计总览第二段：财务商业事实源

状态：AUDIT-01 第二段开发契约，2026-09-30。基于已合并的[第一段契约](admin-audit-overview-contract.md)。本文件只规定增量与冲突处；第一段其余鉴权、Origin、无缓存、脱敏、31 天窗口、每页 1–100 条、固定排序、五秒超时及“不等于完整审计”的边界继续生效。

## 范围与来源

将现有 `financial_commercial_operations` 作为第五个且最后一个固定来源，token 为 `financial_commercial`，排在旧四源之后。它是财务模块已存在的同事务、不可变商业操作 receipt；本批只读，不修改写路径、回填、保留规则、账本、支付或授权。不把它称为全部账务/支付审计，不纳入 `financial_operations`、`financial_webhook_events`、会话、备份或其他表。`result="succeeded"` 仅表示该行随其业务事务提交，不表示外部支付已成功、没有后续退款或完整商业流程已经完成。

只投影 `operation_id→event_id`、`action`、`actor_admin_id→actor_id`、`resource_kind→target_type`、`resource_id→target_id`、`revision`、`created_at→occurred_at` 和上述常量 `result`。绝不读取或返回 `payload_digest`、金额、正文、密钥、支付凭据或其它财务表。`actor_admin_id IS NULL` 是源表允许的事实，例如没有关联管理员的处理路径；API `actor_id` 为 JSON `null`，网页显示“未关联管理员”，既不丢弃该行也不把它归给当前登录管理员或臆造系统管理员。`actor_id` 精确筛选只匹配非空管理员 ID；要查看未关联行可筛选第五来源，但本段不增加“仅空 actor”查询语法。旧四源 `actor_id` 仍为非空字符串。

五源全局顺序仍为实际 UTC 时刻降序、源顺序升序、同源 `event_id` 降序，采用归一化纳秒键处理混合时间精度。五源首页在同一只读事务中验证全部源结构并捕获全部五个 `rowid` 水位；续页重复验证结构并使用首页水位。任一表、必需索引/触发器缺失，视图或伪表代替，DDL/列不符，扫描/解析的候选行不合法，或 SQL/事务失败，整页返回脱敏 503，绝不输出其它源的部分条目。财务表必须匹配财务模块既有严格 DDL，两个不可变触发器必须匹配；不得增加财务表索引或触发器，因为该模块目前严格验证其显式索引和触发器总数。旧库仍由财务模块已有迁移建立/验证该表；总览不自动补表或改变该模块 schema 计数。

游标升级为 v2、独立 HMAC purpose，固定五水位。第一段 v1 的四水位游标一律返回 400 `invalid_request`，不补猜第五水位；管理员重新请求首页即可。v2 游标继续绑定窗口、源集、所有筛选、limit、水位和末项键。旧四源 token、精确筛选、顺序和分页语义不变；省略 `sources` 现在表示五源。新写入（包括时钟回退或同刻写入）不得进入既有页面链。原表保留/删除仍可使续页缩短，不能承诺历史快照持久化。

## 网页兼容与安全

`features.admin_audit_overview` 继续门控整个入口；新增 `features.admin_audit_financial_source=true` 只表示第五源可用。新网页面对旧服务缺少新能力时仍显示原四源，不发送 `financial_commercial`，不把四源响应当作五源数据。只有两项能力均为 true 才呈现第五源筛选和五源范围说明。页面严格验证允许的 source、字段、`actor_id` 仅第五源可为 `null`、时间、revision、顺序、重复与 cursor；畸形响应整页失败关闭。桌面和 390px 宽度均需显示明确来源及未关联管理员状态。范围说明须区分“现存成功事务事实”和外部支付/账务最终结果，不声称完整财务、统一审计或统一保留。

## 独立验收

1. 五源混排、同刻 tie-break、无小数至纳秒时间、精确筛选、第五源有/无管理员两类事实、`null` 页面显示，以及 `payload_digest`/金额/正文/凭据不进入审计响应和日志。
2. 首页和续页在仅筛选旧来源时也检测第五表缺失、视图/错 DDL/坏触发器；候选坏行、SQL/取消/持久化故障均零部分输出，修复后可重试。
3. 旧 v1 四水位游标拒绝，v2 篡改/错水位拒绝；第五源水位隔离新写入，旧四源分页和 31 天/100 上界不退化。
4. 财务模块旧库迁移与严格 schema/计数测试保持通过；无新索引、迁移或商业写路径变更。
5. 管理员会话、同源/错误 Origin、no-store；旧服务/新服务网页能力门控；桌面及 390px；临时动态非 8787 进程、真实浏览器和固定 HEAD 独立进程验收。全 Go 非缓存测试、`go vet`、实际 CGO service/有状态 race、Web 测试/typecheck/build、CI、Markdown 链接和 `git diff --check` 均对最终 HEAD 记录。

实现所有权：本批独占 `internal/service/admin_audit*.go`、必要的 `internal/financial` 只读 schema 验证入口、`web/src/api.ts`/`web/src/App.tsx`/`web/src/pages/AuditPage.tsx` 及对应测试、上列审计文档和状态矩阵。任何财务操作写函数、DDL、迁移、其它功能模块均不在修改范围。

来源：本项目独立撰写的第一段契约、现有 `internal/financial` 持久化约束和 Go/SQLite 标准接口；没有新第三方依赖或外部协议。此前参考代码的研究背景按[独立实现规则](independent-implementation.md)披露，不宣称严格洁净室。
