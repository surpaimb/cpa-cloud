# 核心模块设计 v0.1

状态：待实现的详细设计提案。本文为独立编写的规格，不是对参考项目源码的改写。
适用范围：单公司、单服务进程、单机 SQLite；多副本部署不在此一致性方案内。

## 1. 员工与 Key

员工是权限主体，Key 是员工的访问凭据；部门只用于分组统计，不引入部门权限继承。

| 实体 | 核心字段 | 约束 |
| --- | --- | --- |
| employee | id、name、department、note、status、model_mode、revision、created_at | 姓名不要求唯一；状态 active/disabled；revision 乐观锁 |
| employee_model | employee_id、model_id | selected 模式使用显式集合；空集合表示无模型权限 |
| access_key | id、employee_id、name、selector、digest、digest_version、expires_at、revoked_at、created_at | selector 唯一；secret 不入库；撤销不可逆 |

Key 拟采用 `cpac_<随机标识>.<随机秘密>`，秘密由密码学随机源产生，至少 256 bit。
服务端使用用途隔离的 HMAC-SHA256 密钥计算摘要，常量时间比较。随机标识不是认证秘密。
Key 有效性 = 员工 active 且 Key 未撤销且未过期；默认 expires_at 为 NULL，即无固定到期时间；显式设置时使用 UTC，达到 expires_at 即失效。
员工停用可恢复，但明确撤销的 Key 不因恢复员工而复活。

第一版 Key 不单独扩大/缩小员工模型权限，所有 Key 继承员工当前权限。列表只返回 Key ID、名称、状态和时间。
创建成功时仅返回一次完整 Key，响应禁止缓存。生成前写入并提交记录，提交失败不交付秘密。
创建携带 operation_id：重复提交只返回已有 Key ID，不重显秘密；响应丢失后管理员撤销该 Key 再生成。
不会因为不能找回旧秘密而在服务器增加明文备份。

停用或撤销默认阻断后续发起的上游请求，已发出的流允许完成，避免中断员工工作。
明确提供独立“停止该员工正在运行的请求”操作作为后续能力，不能声称撤销能追回上游已接收的数据。

### 撤销一致性

不使用异步过期的权限缓存作为撤销依据。第一版在本进程维护不可变权限视图及短持有期的准入读写锁。
管理员变更持写锁：提交数据库 → 替换内存视图 → 释放锁 → 返回成功。
请求在读锁内完成最后权限验证与派发登记，再释放锁；登记早于撤销成功的请求属于已准入请求。
每次新上游尝试都重新验证权限，不允许撤销后自动重试。
事务失败保留旧视图并报失败；提交后视图无法发布则进入不就绪状态，拒绝新模型请求，不能返回成功。
启动时从数据库构建视图；禁止旁路直接修改数据库。该锁不贯穿模型推理或流式输出。

## 2. 上游账号与模型

第一版目标包括 API Key 和 ChatGPT/Codex、Claude、Gemini 的会员账号导入/授权。三家分别验证公开协议、凭据格式、授权、刷新、可用模型与流式请求；未满足公开协议独立实现条件的方式列为阻塞项，不以 API Key 验证替代会员账号验证。后续数据模型需按凭据类型扩展，下面 API Key 字段只是基础设计。

| 实体 | 核心字段 | 说明 |
| --- | --- | --- |
| upstream_account | id、name、provider_kind、endpoint、enabled、credential_ciphertext、key_version、revision | 凭据仅可替换，不能从网页回读 |
| upstream_observation | account_id、health、checked_at、error_code | 健康是观测，不代替管理员启停意图 |
| model | id、display_name、enabled、protocols | 对外 ID，不从模型名字猜能力 |
| model_route | model_id、protocol、account_id、upstream_model | 第一版每种模型/协议只配置一个活动路由 |

健康状态 unknown/healthy/degraded/auth_error；认证失败不会永久删除账号。手动探测与普通请求独立记录。
凭据更新失败必须保留旧凭据。健康检查仅用固定、受支持的请求，不执行管理员提供的任意脚本。
端点只能由管理员配置。正式内置提供商限制已知 HTTPS 主机；自定义兼容服务作为单独功能评审，
不能借任意 URL 探测访问服务器元数据、回环或内部管理地址。凭据不得跟随重定向。

有效模型 = 员工授权集合 ∩ 已启用且有匹配协议路由的模型。全部模式会纳入未来启用的模型，页面需说明。
健康探测短暂失败不自动删除模型权限。目录变化有 revision；固定列表不自动改成全部。

账号池调度放在后续：先补资格筛选、会话归属、冷却与故障分类规格，再决定负载选择策略。
会话保持必须隔离员工，不能仅使用客户端提供的相同会话字符串作为全局键。

## 3. 标准模型 API

一个请求在同一 Go 进程完成鉴权、模型路由与 HTTP 上游调用。管理 API 不是模型请求的跳点。
优先同协议传递，不在首版隐式转换 OpenAI 与 Anthropic 协议。

| 交付批次 | 接口 | 能力边界 |
| --- | --- | --- |
| A | GET /v1/models、POST /v1/chat/completions | 首个公开 API Key 上游；非流式、SSE、工具调用与取消 |
| B | POST /v1/responses | 先做显式限定的同步/流式创建；存储、后台任务、响应检索及会话续接另立规格 |
| C | POST /v1/messages | Anthropic 同协议上游；版本头、流式事件及工具调用独立验证 |

批次不等于兼容声明：Codex、Claude Code、CC Switch 都要在实际配置下单独验证。
不支持的端点返回明确错误，不用通用转发兜底。Responses 中需要服务端状态的功能在未实现所有权隔离前拒绝，
避免共享上游账号导致员工访问他人的响应或会话。

OpenAI 风格入口接受 Bearer 员工 Key；Messages 的认证头规则待其官方协议核实后冻结。
混合或重复认证信息拒绝。员工凭据在出站前移除，出站只带所选上游凭据及明确允许的协议头。
请求模型和 route 校验后，同协议业务正文尽可能保留；必要的模型 ID 映射必须有测试，不过滤普通工具参数。
管理 Cookie、客户端 Authorization、任意转发头不得直接传到上游。

首版不自动重试生成请求：发送结果不确定时可能已产生模型输出或费用。
后续重试需有按协议与错误类型定义的条件；一旦开始输出流，禁止切换账号并拼接第二次生成。
流式实现必须有背压、客户端取消传播、有界解析和资源释放；不得缓存整段流等待统计。
用量解析不能损坏原始事件。未收到完整终态时标注 incomplete，不推测成功。

稳定错误分类：认证失败 401、模型权限拒绝 403、请求格式错误 400、无可用路由 503。
上游错误由协议适配器映射；保留可安全传递的重试信息，屏蔽凭据、内部地址和未审查的错误正文。
每个请求有服务端 request_id。管理接口使用独立错误结构，不混入供应商模型协议。

## 4. 用量与审计

用量模型在 P0 建立；P1 才完成持久化可靠性和面板验收。不得把临时统计包装成可靠计费。

| 实体 | 字段概要 | 语义 |
| --- | --- | --- |
| model_request | id、employee_id、key_id、model_id、protocol、started_at、finished_at、outcome | 一次客户端请求一条，状态 running/succeeded/failed/cancelled/interrupted |
| upstream_attempt | id、request_id、account_id、sequence、http_status、error_code、latency_ms、usage_state | 重试按尝试分开；唯一 request_id+sequence |
| usage_measurement | attempt_id、input_tokens、output_tokens、cache_read_tokens、cache_write_tokens、source | 每尝试最终记录；未知字段为 NULL，零值须有依据 |
| admin_audit | id、actor_id、action、target_id、result、occurred_at | 记录谁操作了什么，不保存密码、Key、配置正文 |

缓存 Token 在不同协议中可能是子集或独立维度，必须记录来源语义，不直接把所有列相加。
统计界面分别展示客户端请求数、上游尝试数、已知 Token 和用量不完整次数。
进程崩溃后未终结请求转为 interrupted；上游消耗可能未知，不能回填为零。
终态写入以 attempt_id 幂等；重复回调不重复累加；每日汇总可从明细重新计算。

P1 可靠性方案：发往上游前持久化 started，失败则 503 不出站；终态采用有界队列及本地持久日志，
恢复后幂等合入数据库。队列/日志不可写时降低就绪状态，停止新准入，已发送请求标识统计风险。
日志轮转、磁盘上限和保留期必须与实现一起交付；该方案仍不保证捕获上游未返回的使用量。
拟定原始明细保留 90 天、日汇总保留 365 天，作为可调整运维参数，不当作已确认业务要求。

## 5. 管理接口草案

前缀 /admin/api/v1；除初始化/登录外全部要求管理员会话，写操作验证 CSRF 与同源。
员工 Key 不可访问管理 API；管理员 Cookie 不可替代模型 Key。

| 路径 | 方法 | 作用 |
| --- | --- | --- |
| /setup | POST | 持一次性本机初始化凭据创建唯一初始管理员，成功后关闭初始化 |
| /sessions | POST/DELETE | 登录/退出；服务端保存会话摘要；密码变更撤销旧会话 |
| /employees | GET/POST | 分页列表与创建 |
| /employees/{id} | PATCH | 姓名、部门、备注、启停；要求 expected_revision |
| /employees/{id}/model-policy | PUT | 全部或显式模型列表；要求 expected_revision |
| /employees/{id}/keys | GET/POST | 列表与一次性 Key 创建；POST 要求 operation_id |
| /keys/{id}/revoke | POST | 幂等撤销；成功响应意味着新准入已禁止 |
| /upstreams | GET/POST | 账号列表与创建；返回值不含可恢复凭据 |
| /upstreams/{id} | PATCH | 凭据替换、启停；乐观锁 |
| /upstreams/{id}/check | POST | 固定探测，合并重复任务并限频 |
| /models | GET | 管理员模型与路由视图 |
| /models/{id} | PUT | 配置模型启停与同协议路由；乐观锁 |
| /usage/summary | GET | 有界时间窗口、员工/模型维度聚合 |
| /usage/requests | GET | 脱敏请求分页及用量完整性 |
| /system/status | GET | 版本、就绪、存储与统计状态 |

列表使用游标分页；写冲突 409；未知 JSON 字段和过大请求拒绝。
具体 JSON Schema/OpenAPI 在接口实现前冻结，本文不能替代可执行契约。

## 6. 部署、秘密与恢复

单机 SQLite 使用事务和 WAL；不部署到网络共享盘，不同时运行多个写服务。
备份必须用一致性快照或停机备份，不能只复制活跃的主数据库文件。
数据库事务不跨越上游网络调用。迁移、兼容版本与恢复流程需在首次升级前验证。

上游凭据采用带版本的认证加密，绑定账号 ID 和用途；员工摘要密钥与加密密钥分开派生。
安装根密钥由操作系统随机源生成，通过受限凭据文件/系统服务凭据挂载读取，不进入参数、日志和数据库。
仅凭文件权限不称为加密存储；应明确 root/主机管理员可以读取运行时秘密。
备份分离保存数据库和密钥恢复材料；缺失根密钥不能解密上游凭据，启动应明确失败。
密码哈希算法、参数和密码重置恢复命令在安全实现阶段依据官方规范选定。

TLS、Cookie Secure/HttpOnly/SameSite、管理员初始化和登录限流必须在公网部署前完成。
模型 API 不因第一版缺少完整运维就默认绑定公网并明文运行。
