# 开发预览接口契约 v1

Go module: cpacloud.local/server；Go 服务入口 cmd/cpa-cloud；网页目录 web。
入口参数约定 --data-dir、--listen（默认 127.0.0.1:8787）、--web-dir、可选 --tls-cert/--tls-key。
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
| /employees/{id}/keys | GET/POST | POST {name,operation_id,expires_at?}; 默认 null；返回 {id,name,key?,expires_at,revoked_at}，只有首次创建有 key |
| /keys/{id}/revoke | POST | {}；返回 {ok:true} |
| /upstreams | GET/POST | POST {name,provider_kind,endpoint,api_key}; kind openai-compatible；列表绝不返回 api_key/ciphertext |
| /upstreams/{id} | PATCH | {expected_revision,name?,enabled?,api_key?} |
| /models | GET/POST | POST {id,upstream_id,upstream_model}; 暂一模型一路由；返回 {id,upstream_id,upstream_model,enabled} |
| /system/status | GET | {version,ready,storage,limitations:[]} |

普通 upstream 对象 {id,name,provider_kind,endpoint,enabled,revision}。
GET /healthz 只返回 {status}，不暴露员工或上游详情。
模型入口 GET /v1/models 和 POST /v1/chat/completions 使用 Bearer 员工 Key。
默认不自动重试；鉴权和上游执行同进程，员工秘密不向上游传递。
开发测试可显式允许回环模拟上游（仅测试配置），不能默认允许任意内部地址或重定向。

文件范围：服务任务拥有 go.mod/go.sum、cmd/、internal/ 和自己的 Go 测试；网页任务拥有 web/；主任务拥有 scripts/、deploy/、集成测试和顶层文档；研究任务仅 docs/research/。
本契约是预览最小面，不承诺包含完整产品所有接口；新增必要参数应与主任务协调。
