# ADR 0003：Responses 状态、后台任务与托管工具安全边界

- 状态：D2 开发预览已实现；D3 保持关闭
- 日期：2026-09-24
- 决策范围：单实例、单租户内部部署；正文审计仍不实现

## 背景

当前 `/v1/responses` 只允许无状态请求，并明确拒绝 `store=true`、`background=true`、非 null
`previous_response_id` 和 conversation。官方 Responses 协议允许保存响应、串联上下文、轮询/取消后台
任务和调用提供商托管工具。这些能力需要功能性正文存储和异步执行，但不能借此把正文放入日志、审计或
用量记录，也不能让共享上游凭据变成跨员工/Key 读取资源的渠道。

本 ADR 细化独立集成契约中的门禁。功能开关固定为 `responses_stateful_resources`、
`responses_background_tasks`、`managed_tools`；三者默认关闭且分别启用。

## 决策

### 1. 最小状态和加密

D2 首批只保存 D1 已验证的文本 message、function_call、字符串 function_call_output、必要 instructions、
模型/状态/时间/顺序和用量元数据。图片、音频、文件、reasoning、未知 item、conversation 对象和托管工具
结果正文继续拒绝。`store` 默认 false；只有员工显式 `store=true` 且功能开关开启时才落功能性正文。

每个 response 生成随机 256-bit DEK。DEK 用安装主密钥按独立 purpose `response-state-wrap/v1` 包装；
每个 item 使用 AEAD purpose `response-state-item/v1`，AAD 固定绑定 schema version、employee ID、Key ID、
response ID、item sequence 和 item type。数据库只保存包裹 DEK、密文、nonce 与有界无正文元数据。
任何解密或认证失败固定报资源不可用，不回显密文、AAD 或正文。

### 2. 所有权和授权

资源所有者是创建时不可变的 `(employee_id, key_id)`，并绑定 public model。读取、删除、取消、
`previous_response_id` 续接和后台恢复都要求当前 Bearer Key 与两个 ID 完全一致，并重新检查员工状态、
Key 撤销/到期和模型权限。另一把属于同员工的 Key 也不能读取或续接。管理员接口默认只看无正文元数据、
取消和删除；不提供通用正文读取或导出。

员工停用、Key 撤销/到期后立即禁止新的读取、续接、上游或工具派发；已明确派发的单次尝试只可完成或
取消。恢复权限不自动恢复已中断任务。

### 3. TTL、删除、备份和恢复

首批保存资源 TTL 固定 30 天，从 terminal 时间起算；未终结任务从创建时间起最长 24 小时，超过即中断。
删除幂等：同一事务先销毁包裹 DEK并写 tombstone，再删除 item 密文；后续读取固定 404，续接固定拒绝。
清理 worker 默认关闭，启用后只处理已过期资源，不在事务中等待网络。

SQLite 在线加密备份会包含当时仍存在的密文和包裹 DEK。恢复保留快照时的 TTL、tombstone 和所有权，
管理员 session 全部失效，后台 `dispatch_authorized`/`in_progress` attempt 一律恢复为 interrupted。旧快照
可能早于后来的删除；因此在生产发布前必须完成可随备份保存并在恢复后重放的删除清单/密钥销毁方案和
演练。在该门禁通过前，状态能力只能标开发实验，不能承诺跨旧备份的删除不可恢复。安装主密钥丢失时
正文不可恢复，不得降级为明文或忽略认证。

### 4. `previous_response_id`

续接只引用本地 response ID；先做所有权、Key、模型和 TTL 检查，再解密受支持 items，并按原始顺序
构造一次新的上游请求。父 response 不修改，新 response 保存 parent ID，允许分叉。所有上游输入 Token
仍由供应商 usage 结算，不能因为本地引用而免计。父资源删除/过期后禁止新续接；已派发子请求不重放。

### 5. 后台状态机和持久派发屏障

状态机为：

`queued → dispatch_authorized → in_progress → completed | failed | cancelled | interrupted`

创建 background response 只返回 queued 资源，HTTP 接受不是模型成功。worker claim queued 后重新检查
所有权/权限/模型/账号 revision/价格/预算；在同一数据库事务写 attempt 和 durable
`MarkAttemptDispatched`。只有该事务提交成功后才允许网络 I/O。提交失败零出站。

取消 queued 直接 terminal cancelled；取消 dispatch_authorized/in_progress 会取消本进程 context，并以
实际结果竞争一次 terminal CAS。取消重复调用幂等。进程崩溃或启动发现 dispatch_authorized/in_progress
时一律记 interrupted、usage 未知、预算占用不确定；绝不自动重放可能收费的请求。只有从未通过持久派发
屏障的 queued 工作可在重启后重新 claim，且必须重新授权。

读取返回当前状态；删除先请求取消，只有 terminal 后销毁资源 key。`GET /v1/responses/{id}`、
`DELETE /v1/responses/{id}` 和 `POST /v1/responses/{id}/cancel` 都执行同一所有权检查。后台 stream 恢复和
cursor 在首批不实现；请求时明确拒绝，不能从不完整事件缓存伪造恢复流。

### 6. 用量和费用

使用 `accounting.EventRecorder`，不新建平行账本。一个后台 response 创建一个 request 事实；每次实际
上游或托管工具派发各有唯一 attempt，并携 response/task/tool run ID。usage 只取供应商完整快照；失败、
取消或中断前已验证的累计值可保留，缺失保持 NULL。价格在持久派发事务冻结，后台未知结果不记成已确认
消费，也不记成精确零。

### 7. 托管工具

D3 只传递“上游种类 + 实际模型 + 管理员工具类型”三重白名单中的官方上游托管工具。默认白名单为空。
CPA Cloud 不下载或执行模型生成代码，不提供任意本地 shell/容器，不自动连接未知 MCP 服务，也不把
客户端 function tool 当托管工具执行。

每种工具在启用前单列：官方字段、输出 item/event、数据保留、网络/文件边界、取消、费用和模型支持。
上游返回工具调用仍属于同一 response；若供应商给出独立计费/运行 ID，以 `tool_run_id` 关联 usage event。
未知费用保持未知。file search/code interpreter 在资源所有权和文件存储契约完成前不可启用；remote MCP、
computer、shell 和 programmatic tool calling 在执行隔离与审批模型完成前不可启用。

## 日志、错误和测试

日志、通用审计、用量、管理列表和错误永不包含 prompt、response、工具参数/输出、密文、认证头或 Token。
专项测试必须扫描日志、错误、SQLite 主文件和 WAL，区分预期 AEAD 密文与意外明文；覆盖跨员工、跨 Key、
跨模型、撤销、到期、TTL、显式删除、备份恢复、取消竞争、持久化失败、派发屏障、重启中断和并发 claim。

## 当前结果与未完成项

D1 已实现无状态 Chat↔Responses 转换模块和严格流状态机。D2 开发预览通过独立、默认关闭的
`responses_stateful_resources` 与 `responses_background_tasks` 能力启用：同步 `store=true`、本地
`previous_response_id`、所有者绑定 GET/DELETE、后台创建/轮询/取消、加密正文、持久派发屏障和启动恢复均有
专项测试。后台首批只接受不会在持久化后丢失语义的字段，并只支持一个直接 Responses 路由；账号池后台路由、
后台 stream cursor、清理 worker、跨旧备份删除清单仍未实现。同步状态请求也明确拒绝 stream 续接，不能伪造事件恢复。

D3 的白名单仍为空，系统能力报告固定为关闭；除客户端 `function` 工具透传外，任何提供商托管工具在派发前拒绝。
每种托管工具仍须逐项完成官方字段、输出、保留、取消、费用、模型支持和合成测试后才可增加配置入口。真实供应商与
真实客户端证据继续单独报告。

## 官方来源

查阅日期：2026-09-24。

- Responses conversation state 与 `previous_response_id`：
  <https://developers.openai.com/api/docs/guides/conversation-state>
- background 创建、轮询、取消、临时存储与 stream cursor：
  <https://developers.openai.com/api/docs/guides/background>
- Responses 工具分类：<https://developers.openai.com/api/docs/guides/tools>
- function calling 的客户端执行边界：
  <https://developers.openai.com/api/docs/guides/function-calling>
- MCP 工具和审批边界：
  <https://developers.openai.com/api/docs/guides/tools-connectors-mcp>
- code interpreter 托管容器：
  <https://developers.openai.com/api/docs/guides/tools-code-interpreter>
