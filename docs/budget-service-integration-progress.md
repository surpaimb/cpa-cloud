# 预算服务集成进度

2026-09-24；工作分支 `codex/budget-integration`。**预算核心、管理配置及网页已集成，请求执行已接线；尚未合入主线，完整验收待收尾。**
本文件记录当前独立实现，不改变 [预算契约](budget-persistence-integration-contract.md) 的完成条件。

## 分工

| 负责方 | 当前交付范围 |
| --- | --- |
| 根协调任务 | App 联合迁移/恢复、实际请求派发与终结接线、集成审查和验收 |
| `scheduler_finish` | 已完成 `e7d5e9c`：预算持久核心；集成提交 `eb3ee18` |
| `batch_import_finish` | 已完成 `2cb7b1a`：管理配置、迁移及回执兼容；集成提交 `1199b81` |
| `gemini_sse_finish` | 已完成 `9796f9f`：网页预算配置；集成提交 `d84bbc6`；此前的固定模型证明也已集成 |

这是当前协调会话的协作子任务；名称沿用先前任务，与目前负责功能未必同名。
核心、配置与网页使用各自工作树，根通过提交审阅集成，不共同修改一个 Git index。

用户于 2026-09-24 明确要求后续不要在此协调会话启动协作子任务，因为侧栏不可见、难以查看进度。
当前三个任务已经正常结束，不再续派新阶段或扩展子任务。后续并行工作使用侧栏可见的独立任务，
本地会话继续负责协调和最终集成。不得因重复使用旧任务名而把已交付阶段和新工作混为一项长期运行任务。

## 根已独立核实的增量

- 固定官方 OpenAI Chat endpoint、实际 `gpt-4.1-2025-04-14`、明确输出上限、文本非流式请求的严格证明。
  使用实际持久化的 `openai-compatible` provider 值，再检查官方 URL；第三方兼容服务不获得证明。
  参数重复、未知键、工具/媒体/状态及其他协议均不纳入本 profile。
- `proveModelBudgetWire` 从服务端准备的最终 HTTP request 的 `GetBody` 读取同一串行化正文，不消费发送流，
  不持久化正文或正文 hash。实际派发先使用该证明，再在同一事务冻结价格、创建 attempt 与持久预留。
  确认预留及 may_have_sent 标记均已提交后才发送；任一提交返回不确定结果均不发送。
- 固定 snapshot 专用用量归一化器：要求返回 model 精确匹配，拒绝用量对象重复字段；未报告缓存写入字段时，
  按该模型的普通输入计费分类处理，不能描述为物理缓存写入为零。显式 null 保留 unknown。
  普通兼容协议解析器仍维持原行为；实际用量超出任一上界时，结算同事务隔离对应 profile。
- App 在同一个升级事务中校验并迁移 core、management 和预算表；仅接受完整旧版、完整新版或全新治理库。
  启动在同一事务恢复预算 reservation、accounting request/attempt、governance request 和 legacy model request。
  `openStore` 不再提前提交 legacy 恢复，生产 App 在此恢复之后才启动 worker。
  统一恢复时间包含既有账本/治理时钟，避免系统时钟回退造成部分恢复。
- 终结按 attempt → budget → parent → legacy → governance 同事务提交；失败不输出正常完成响应。
  同原 ID/冻结输入核对不确定结果，不重发模型请求。续租在同事务更新治理与预算两个截止；失败取消本地执行，
  保留之前的持久截止。未知 Token 与费用分别持有上界，不当作零。

## 本地测试证据

- 固定 snapshot 用量测试 `TestGPT41*`、最终请求证明 `TestBudgetWire*`、profile 参数测试通过。
- `TestRequestLedgerJointRecoveryRollbackRetryAndClockRollback`：逐一注入四张表的 UPDATE 失败，
  验证所有 sibling 状态保持 pending/running；去除故障后重试成功，重复恢复不清空原租约。
- `TestRequestLedgerRecoveryOwnedByAppStartup`：普通打开数据库不提前恢复；真实 `Open` 重启完成联合恢复。
- 既有独立 accounting 重启及价格迁移测试通过；相关 Go 静态检查通过。
- `TestBudgetDispatch*`：真实 App/员工鉴权/SQLite + 注入的合成 HTTP transport，覆盖四协议无证明拒绝、关闭后恢复原执行、
  账号池价格快照、预留及标记提交前后响应丢失、结算回滚及已提交响应丢失、续租失败回滚、单项用量越界隔离。
  其中固定官方 URL 只在 mock transport 内匹配，从未连接真实提供商。
- `TestBudgetStartup*`：以自有旧提交 `4403958` 的 DDL 建立合成旧库；验证联合迁移失败整体回滚、修复重试、
  revision/旧操作回执/员工 Key 保留；预算恢复失败时所有关联账本不部分结束。
- 根独立 `go test -p 1 ./... -count=1 -timeout 15m` 全量通过：service 451.061 秒，governance 42.132 秒，
  accounting 17.550 秒，其余 Go 包均通过；全项目 `go vet ./...` 通过。
- 网页预算专项 11 项、网页全量 109 项测试通过，typecheck/build 通过；无真实浏览器验证声明。
- 真实 Go 可执行文件运行增强的 `scripts/smoke-governance.mjs` 通过：临时端口/目录、旧治理回归、预算默认关闭、
  双开关、策略及操作回执重启保留、无证明时零上游派发、关闭预算后恢复执行及 Key 撤销。
  仅模拟流量，已检查凭据不落日志/明文数据库并清理临时进程与数据。

全部为合成数据与本地自动化。尚未执行本分支 Linux race、真实提供商调用和真实浏览器验收。
不发布安装包，不据此宣称已对齐 Sub2API 预算机制。

## 下一步仍须完成

完成真实浏览器验收、Linux race 与轻量 CI，再决定是否合入主线。
当前 profile 仅覆盖明确参数的官方固定模型文本非流式请求；其他模型、会员、工具、媒体或 SSE 请求无上界证明时，
命中 deny_unknown hard 策略将被拒绝，不代表这些协议已有完整预算支持。完整模型覆盖和真实提供商兼容仍待后续。

## 协调机制

子任务结束会向父任务投递完成通知，根可用状态查询确认；这与根持续执行是两件事。
根的一轮已结束时，不能声称它仍在等待并自动合并。2026-09-24 用户反馈的空档已确认；本轮收到完成通知后实际接管
集成和验收，没有为保持“运行中”重启已交付任务，也未新建内嵌子任务。
