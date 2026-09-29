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
