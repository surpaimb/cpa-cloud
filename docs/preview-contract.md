# 开发预览接口契约 v1

Go module: cpacloud.local/server；Go 服务入口 cmd/cpa-cloud；网页目录 web。
入口参数约定 --data-dir、--listen（默认 127.0.0.1:8787）、--web-dir、可选 --tls-cert/--tls-key，以及可重复的 --trusted-proxy-cidr。
全新 data-dir 支持 --init，仅初始化并退出：管理员密码通过 stdin 交付（不写日志），初始用户名 admin。
服务启动不得向日志输出秘密。数据加密根密钥单独保存在受限文件，明确主机管理员信任边界。

管理路径 /admin/api/v1；除 session 外要求 HttpOnly session Cookie；非回环运行要求 Secure Cookie。
登录 POST /sessions {username,password} → {csrf_token}；退出 DELETE /sessions。
GET /session → {username,csrf_token}；写请求 X-CSRF-Token，服务端验证 Origin。
管理错误 {error:{code,message}}，message 为固定脱敏文案。列表统一 {items:[]}，单对象直接返回。
对象 id 为不透明字符串；时间 RFC3339；业务变更冲突 409。

| 路径 | 方法 | 请求/响应核心 |
| --- | --- | --- |
| /employees | GET/POST | POST {name,department?,note?}; employee {id,name,department,note,status,model_mode,models,revision} |
| /employees/{id} | PATCH | {expected_revision,name?,department?,note?,status?}; status active/disabled |
| /employees/{id}/model-policy | PUT | {expected_revision,mode,models}; mode all/selected |
| /employees/{id}/keys | GET/POST | POST {name,operation_id,expires_at?,policy?}; 默认 null；返回 {id,name,key?,expires_at,revoked_at,policy?}，只有首次创建有 key |
| /keys/{id}/policy | GET/PUT | GET 返回 Key 策略；PUT 使用 expected_revision 全量替换 |
| /keys/{id}/revoke | POST | {}；返回 {ok:true} |
| /upstreams | GET/POST | POST {name,provider_kind,endpoint?,api_key}; kind openai-compatible 或 gemini-api-key；Gemini 生产端点固定为 Google 官方地址；列表绝不返回 api_key/ciphertext |
| /upstreams/{id} | PATCH | {expected_revision,name?,enabled?,api_key?} |
| /models | GET/POST | POST {id,upstream_id,upstream_model}; 暂一模型一路由；返回 {id,upstream_id,upstream_model,enabled} |
| /system/status | GET | {version,ready,storage,limitations:[]} |

普通 upstream 对象 {id,name,provider_kind,endpoint,enabled,revision}。

### 员工本人钱包余额（ID-05/BILL-03 开发预览增量）

`GET /self/api/v1/billing/balance?currency=USD` 仅在 `--employee-self-service-enabled` 与独立的 `--employee-self-wallet-balance-enabled` 同时显式开启时注册；后者默认关闭且不能单独启用。已开通、active 员工的自助会话是唯一身份来源，管理员 Cookie 与员工模型 Key 不可替代。查询必须指定一个三位大写 ASCII 币种，响应仅含 `currency`、`has_account`、canonical 字符串或 `null` 的 `amount_micro`；不混入本人 Key/资源子账户，亦不返回账户 ID、分录、套餐或支付资料。单一有界只读快照在故障时整响应 503。自助网页由独立 capability 门控且只在明确点击时读取；GOV-02 与完整账单仍待处理。详见[独立契约](employee-self-wallet-balance-contract.md)。

`GET /self/api/v1/billing/entries?currency=USD[&limit=20][&cursor=...]` 另由默认关闭的 `--employee-self-wallet-activity-enabled` 注册，并要求上述两个开关先启用。当前 active 员工自助会话是唯一归属来源；单币种、每页 1–50 条、首读冻结近 31 天 UTC 整秒窗口，后续独立快照用目的隔离的认证加密游标按稳定 `(created_at TEXT,id)` 位置继续。响应只有币种、账户是否存在、窗口、每条时间/有符号 micro 变动和下一游标，不泄露 ID/Key/资源/套餐/支付元数据。缺账户与本窗口无条目有别；同秒混精度文本顺序不宣称严格纳秒排名，页间并发不宣称完整快照。故障整页 503，网页必须显式按需打开且由独立 capability 门控；不产生财务写入或完整账单。详见[最近钱包变动契约](employee-self-wallet-activity-contract.md)。

`GET /self/api/v1/billing/subscriptions[?limit=20][&cursor=...]` 由另一默认关闭的 `--employee-self-subscription-status-enabled` 注册，只要求员工自助总开关。当前 active 员工会话是唯一归属来源；每页 1–50 条，按不可变订阅 ID 逆序，用绑定员工、会话和页大小的认证加密游标继续。仅显示本人直接拥有的现存订阅 ID、冻结 interval、只读有效状态及开始/终止/取消时间，不返回套餐、金额、权益、支付或账户资料；商业执行关闭仍可读历史。页间不是同一数据库快照，故障整页 503；网页只有独立 capability 和明确点击才读取，不改变订阅或账本。详见[本人订阅状态契约](employee-self-subscription-status-contract.md)。

`GET /self/api/v1/billing/subscriptions/{id}/purchase-snapshot` 是独立、默认关闭的本人直属钱包单条订阅购买时记录读取，要求自助总开关、本人订阅状态与本人钱包余额三个现有开关。仅当前 active 员工会话可按已知订阅 ID 显式读取；不存在、非本人或 Key/资源子账户订阅统一 404。成功响应只有订阅/套餐 ID、套餐版本、币种、周期、购买时扣费和授予额度的 canonical micro 字符串、开始与冻结期限结束时间。单一只读事务交叉核对商业回执、账本操作、两条分录及适用的续购链接；链异常或存储故障整响应 503。商业执行关闭或当前套餐变化不抹除历史记录。订阅行并非全字段不可变，故这不是防直接 SQL 篡改的凭证，也不是当前价格、余额、外部账单、真实付款或使用权益。网页由独立 capability 门控，仅点击列表中一条订阅才读取。详见[本人购买时记录契约](employee-self-subscription-purchase-snapshot-contract.md)。

`GET /self/api/v1/billing/subscriptions/{id}/one-shot-renewal` 与 `POST .../{id}/one-shot-renewal/disarm` 是另一个默认关闭的员工自助 opt-in，要求自助总开关和本人订阅状态开关。只有当前已开通且 active 的员工可按需查看本人直属钱包 monthly 订阅的一次性预约最小状态，或用当前密码、CSRF、预约存储 revision 和全局 operation ID 撤销尚未执行的预约；商业执行关闭后仍可读取及精确重放。员工不能 arm、购买、退款或取消本期订阅。GET 不触发 worker 或财务写入；POST 只以 typed employee actor 在同一事务记录一次 disarm receipt 和预约终态，不动钱包/账本。详见[员工一次性预约查看与撤销契约](employee-self-one-shot-disarm-contract.md)；真实生产启用与完整付款验收尚未完成。

`GET /self/api/v1/billing/plans?currency=USD[&limit=20][&cursor=...]` 由默认关闭的 `--employee-self-plan-catalog-enabled` 注册，只要求员工自助总开关。当前 active 员工须明确指定单一三位大写币种并点击；仅当商业执行开关在本页读取快照中开启，返回该币种当前已启用套餐的 ID、名称、周期、价格/额度 micro 字符串和 revision，按 ID 升序分页。商业执行关闭时 `available=false`、空列表且无下一页；认证加密游标绑定员工、会话、币种、页大小和 15 分钟时限。它不是个人订阅、权益、固定报价或购买入口；无财务写入、支付或上游调用。详见[员工自助套餐目录契约](employee-self-plan-catalog-contract.md)。

`POST /self/api/v1/billing/subscriptions/{id}/renewal-quotes` 与 `POST .../{id}/renew` 由独立默认关闭的 `--employee-self-subscription-renewal-enabled` 注册，要求员工自助、本人订阅状态和本人钱包余额三个开关。只允许当前 active 且已开通的员工，对本人直属、已到期的单月订阅先获取五分钟冻结报价，再以当前密码和全局操作 ID，从已有本人直属同币钱包即时购买一个后继月周期。当前商业执行、计划快照、旧周期/唯一后继和余额在新购事务内重新核验；完全匹配的已提交操作在当前密码/会话/CSRF 后可于报价过期或商业执行关闭时精确重放。新购与到期状态、两条账本分录、商业回执、唯一续购链接及已有 armed 预约的 superseded 终态同事务提交。本入口不自动开户、不自动续费、不支付、不赋予模型权益；详见[员工本人月度续购契约](employee-self-monthly-renewal-contract.md)。

### 管理员账号池本机容量快照（OBS-01 第一段）

`GET /admin/api/v1/models/{id}/pool-runtime` 仅接受管理员会话、匹配的可选 Origin 和无查询参数；响应包含模型/池 revision、UTC `as_of`、明确的 `explicit_pool|legacy_no_pool|model_disabled` 状态，以及最多 64 条不含凭据或身份信息的路由本机容量/持久预留/阻塞原因。读取在单个 SQLite 只读事务内完成，结构、时间或存储错误统一 503 且不返回部分结果。网页必须经 `features.account_pool_runtime_observation=true` 门控，仅手动刷新。该投影不是供应商配额、实时请求数或派发承诺；详见[本机观测契约](account-pool-runtime-observation-contract.md)。

### 管理员只读审计总览（AUDIT-01 第二段）

`GET /admin/api/v1/audit/events` 只读投影五类现存事务事实；完整范围、参数、游标升级与失败关闭规则见[第一段契约](admin-audit-overview-contract.md)、[第五源增量契约](admin-audit-financial-source-contract.md)和后续[财务 actor provenance 契约](financial-actor-provenance-contract.md)。`financial_commercial` 对应不可变 `financial_commercial_operations`；响应明确给出 `actor_kind`，`actor_id` 是相应管理员/员工/系统 ID，只有历史 `legacy_unknown` 为 `null`，不伪称管理员操作。`actor_id` 筛选仍仅匹配管理员 ID。`features.admin_audit_overview=true` 门控入口；`features.admin_audit_financial_source=true` 单独门控第五源，旧服务仅有前一能力时网页仍只请求四源。升级后 v3 游标不接收旧 v1/v2 游标，须重新从首页查询。本接口不返回金额、operation digest、凭据或正文，也不宣称完整财务/统一审计或外部支付成功。

`GET /admin/api/v1/audit/events/export.csv` 独立导出同一五源现存元数据，接受首页的时间、来源和精确字段筛选，不接受 `cursor`/`limit`；使用单一只读事务、1000 行及 2 MiB 上限，超限 413 而非截断。CSV 在 `actor_id` 前明确给出 `actor_kind`；历史未知 ID 留空。成功返回固定 UTF-8 CSV 类型和附件文件名；失败在任何 CSV 响应头/首字节前返回脱敏 JSON。网页另由 `features.admin_audit_csv_export=true` 与总览能力共同门控。公式注入转义、权限/Origin 和不完整覆盖边界见[导出契约](admin-audit-export-contract.md)及[财务 actor provenance 契约](financial-actor-provenance-contract.md)。

### 每 Key 协议与模型策略（2026-09-25 批次契约）

只有 `features.key_access_policy=true` 才表示服务器已实现协议/模型策略；来源编辑另要求只读能力 `features.key_source_policy=true`。完整迁移、事务屏障、后台资源语义和验收要求见[流式与 Key 策略批次契约](stream-key-policy-batch-contract-2026-09-25.md)。

策略对象为 `{protocol_mode,protocols,model_mode,models,revision,effective_protocols,effective_models}`。`protocol_mode` 和 `model_mode` 均为 `all|selected`；`all` 必须配空数组，`selected` 使用显式数组且空数组表示全部拒绝。客户端协议枚举固定为 `openai-chat`、`openai-responses`、`anthropic-messages`、`gemini-generate-content`；模型数组只接受公开模型 ID，不接受上游模型名。`effective_*` 是只读快照。

创建 Key 时省略 `policy` 等价于 `all/all`；提供 `policy` 时四个 mode/list 字段必须齐全。`PUT /admin/api/v1/keys/{id}/policy` 使用 `{expected_revision,protocol_mode,protocols,model_mode,models}` 全量替换；revision 冲突返回 409，非法策略返回 400 `invalid_key_policy`，不存在返回 404。旧库 Key 迁移为 `all/all` revision 1，只继承现有四种客户端协议与员工/全局路由能力，不产生新授权。

最终模型集合取员工策略、Key 策略和当前有效路由的交集。`GET /v1/models` 只有 Key 允许至少一种 OpenAI 客户端协议时才返回过滤目录，否则返回已鉴权空列表；`GET /v1beta/models` 要求 Gemini 客户端协议，否则同样返回已鉴权空列表。四种前台入口必须在治理、预算、租约、尝试和网络派发之前检查客户端协议与公开模型，并在最终派发事务中重查冻结的策略 revision。

### 每 Key 来源策略与显式可信代理（2026-09-29 源码增量）

完整边界见[Messages↔Responses SSE 与 Key 来源 IP/CIDR 契约](messages-stream-key-ip-batch-contract-2026-09-25.md)。策略对象增加 `source_mode` 与 `source_cidrs`，并与协议/模型共用同一个 revision/CAS。`source_mode=all` 必须配空数组；`selected` 的空数组拒绝所有来源。成员接受裸 IP 或 CIDR，服务端保存规范 masked prefix；重复、zone、无效或超限输入均拒绝。

创建 policy 的旧四字段客户端省略来源字段时默认 `all/[]`。PUT 中来源两字段同时省略表示保留当前限制；只出现一个无效；显式 `source_mode:"all",source_cidrs:[]` 才清除限制。默认来源只取 `Request.RemoteAddr` 的真实 `host:port` socket peer，所有转发头均忽略。管理员可用可重复的 `--trusted-proxy-cidr` 显式配置数值 IP/CIDR；不会因 loopback、私网或地址类型自动建立信任，非法、重复或超限配置在启动写入状态前拒绝。

实际 socket peer 未命中可信集合时，即使 `X-Forwarded-For` 恶意或畸形也完全忽略，来源仍是 peer。peer 命中时必须恰有一个物理 `X-Forwarded-For` 值，包含 1–64 个逗号分隔的纯数值 IP，整值不超过 4096 UTF-8 字节；从右向左跳过显式可信 hop，第一个不可信 hop 才是来源。缺失、多物理行、空/非法/超限成员或全链可信均失败关闭，不回退到代理地址。`Forwarded` 与 `X-Real-IP` 始终忽略；`0.0.0.0/0` 或 `::/0` 只按管理员原样显式信任整个地址族，不是安全默认值。

目录与四模型入口在治理、预算、parent/attempt 和上游网络前检查解析后的来源；最终派发事务用初始捕获的来源、policy revision 和规范可信集合的 SHA-256 revision 重核。尚未派发的后台任务持久化并复用创建来源及信任 revision，不能把 worker loopback 当员工来源；重启时改变可信集合会让旧排队任务在零 attempt、零上游请求下中断。资源读取/继续要求当前来源仍获权；仍有效且精确拥有资源的 Key 即使策略收紧，仍可 cancel/delete 自有任务，但可信 peer 的畸形转发链仍会在进入资源操作前拒绝。

### Messages↔Responses SSE（2026-09-25 源码增量）

显式 wire 只支持契约列出的 text/function 子集、严格事件顺序与累计 usage；Chat↔Responses、Messages↔Responses 及 Gemini v1beta `streamGenerateContent`↔Responses SSE 已接入共享单派发执行链，thinking、cache-control、媒体、引用、托管工具、有状态/后台跨协议转换仍拒绝。completed/incomplete/failed 分别结算为成功/中断/失败。Client Messages + wire Responses 在 usage 只于 terminal 可知时有界全流延迟。Gemini 身份可晚到，但语义动作只在 `responseId` 与 `modelVersion` 均冻结后按原序单次释放；终态缺身份、显式空值或冲突不会合成成功事件。合成上游测试不等于真实 Anthropic/Gemini SDK、CLI 或供应商账号兼容验收。

### 命名时区每日定时测试（开发预览增量）

默认关闭的账号定时测试在既有固定 UTC 间隔之外增加 `schedule_mode:"daily_local"`、IANA `time_zone` 和 `HH:mm` `local_time`，以 `features.scheduled_tests_daily_local` 门控网页入口。服务端按命名区的当地日历日计算，春季不存在的分钟跳过、秋季重复分钟只执行较早一次；`next_run_at` 始终是 UTC。旧创建 JSON 仍为 interval，旧 interval PATCH 语义不变；每日行保留的 `interval_seconds=86400` 只是数据库兼容占位值。完整输入、迁移、重启和验收边界见[命名时区每日测试契约](scheduled-tests-daily-timezone-contract.md)。此增量不是通用 cron，也不开放真实供应商或收费生成测试。

### 独立渠道监控首段（源码开发预览）

`features.channel_monitor_configuration` 门控管理员 API 和网页入口；`--channel-monitors-enabled` 默认关闭，开启后 `features.channel_monitor_running=true`。计划只绑定一个现有渠道、公开模型和该模型显式池中的一个具体上游路由，支持固定 UTC 间隔的本地凭据或目录检查、有界历史和人工重绑；配置变化不得把旧结果归因于新路由。完整权限、迁移、并发、取消和恢复边界见[独立渠道监控契约](channel-monitor-contract.md)。此段不含生成请求、聚合可用率、通知、告警或真实供应商/会员验收，也不在 preview.3 下载包中。

第二段以独立 `features.channel_monitor_retained_summary` 门控[每计划已保留历史只读摘要](channel-monitor-summary-contract.md)：同一 SQLite 快照内统计最多 200 条已完成结果、当前运行数和两 scope/固定结果码，不写新事实。已归档或重绑计划的旧绑定历史仍计入；满 200 条只表示保留窗口已满，不能推断更早结果或当前渠道健康。旧服务缺能力时网页不请求摘要。此段同样不在 preview.3 下载包中。

### 账号与模型生命周期（开发预览增量）

`PATCH/DELETE /admin/api/v1/models/{id}`、`DELETE /admin/api/v1/upstreams/{id}`、墓碑列表和 CAS 语义见[账号与模型生命周期管理契约](account-lifecycle-management-contract.md)。归档不是物理删除：模型 ID 不可重建，上游可恢复凭据被销毁，历史账本关联保留。网页只在 `features.account_lifecycle_management=true` 时显示入口。

### 上游模型同步（新增）

`POST /admin/api/v1/upstreams/{id}/discover-models` 使用管理员会话及写请求的 CSRF/Origin 校验，返回 `{items:[{id:string}]}`。服务端使用已保存的上游凭据读取 OpenAI-compatible models 接口，复用模型请求的安全连接与地址校验，不向浏览器回传上游 Key 或原始错误响应。发现操作有超时、响应大小与条目数量上限；停用上游不能发现模型。

网页添加上游成功后自动调用一次，已有上游可手动重新同步。失败不回滚已保存上游，也不能因重试重复创建上游。同步结果只作为配置候选列表，不自动创建员工可访问的路由；管理员选择模型后沿用现有模型创建 API。列表可为空，失败时保留手动填写入口。切换上游时忽略旧请求结果。

服务商预设仅辅助填写名称和端点，后端仍为 `openai-compatible`。自定义地址继续支持；切换服务商或目标地址时清空未保存 Key，避免将凭据发送给错误目标。不得通过携带 Key 的试探请求猜测服务商地址。
GET /healthz 只返回 {status}，不暴露员工或上游详情。
模型入口 GET /v1/models 和 POST /v1/chat/completions 使用 Bearer 员工 Key。
Gemini 原生入口 GET /v1beta/models、POST /v1beta/models/{model}:generateContent 和 POST /v1beta/models/{model}:streamGenerateContent 使用同一 Bearer 员工 Key；字段范围、错误、SSE、固定端点与 Google 会员边界见 [Gemini 原生契约](gemini-native-contract.md)。
默认不自动重试；鉴权和上游执行同进程，员工秘密不向上游传递。
开发测试可显式允许回环模拟上游（仅测试配置），不能默认允许任意内部地址或重定向。

文件范围：服务任务拥有 go.mod/go.sum、cmd/、internal/ 和自己的 Go 测试；网页任务拥有 web/；主任务拥有 scripts/、deploy/、集成测试和顶层文档；研究任务仅 docs/research/。
本契约是预览最小面，不承诺包含完整产品所有接口；新增必要参数应与主任务协调。
