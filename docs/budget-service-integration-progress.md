# 预算服务集成进度

2026-09-24；工作分支 `codex/budget-integration`。**尚未合入主线，也未完成预算执行闭环。**
本文件记录当前独立实现，不改变 [预算契约](budget-persistence-integration-contract.md) 的完成条件。

## 分工

| 负责方 | 当前交付范围 |
| --- | --- |
| 根协调任务 | App 联合迁移/恢复、实际请求派发与终结接线、集成审查和验收 |
| `scheduler_finish` | 预算预留、发送前标记、续租、结算、中断恢复、profile 越界隔离的持久核心 |
| `batch_import_finish` | 开关及 hard 策略、严格旧库迁移、管理 API、原操作回执兼容 |
| `gemini_sse_finish` | 已交付固定模型证明；继续网页预算配置及操作恢复体验 |

这是当前协调会话的协作子任务；名称沿用先前任务，与目前负责功能未必同名。
核心、配置与网页使用各自工作树，根通过提交审阅集成，不共同修改一个 Git index。

## 根已独立核实的增量

- 固定官方 OpenAI Chat endpoint、实际 `gpt-4.1-2025-04-14`、明确输出上限、文本非流式请求的严格证明。
  使用实际持久化的 `openai-compatible` provider 值，再检查官方 URL；第三方兼容服务不获得证明。
  参数重复、未知键、工具/媒体/状态及其他协议均不纳入本 profile。
- `proveModelBudgetWire` 从服务端准备的最终 HTTP request 的 `GetBody` 读取同一串行化正文，不消费发送流，
  不持久化正文或正文 hash。**尚未被实际派发路径调用**；它是下一步预留接线的输入边界。
- 固定 snapshot 专用用量归一化器：要求返回 model 精确匹配，拒绝用量对象重复字段；未报告缓存写入字段时，
  按该模型的普通输入计费分类处理，不能描述为物理缓存写入为零。显式 null 保留 unknown。
  普通兼容协议解析器仍维持原行为；上界违约的持久隔离由后续结算负责。
- App 启动在同一事务恢复 accounting request/attempt、governance request 和 legacy model request。
  `openStore` 不再提前提交 legacy 恢复，生产 App 在此恢复之后才启动 worker。
  统一恢复时间包含既有账本/治理时钟，避免系统时钟回退造成部分恢复。
  **预算表尚未接入此事务**；预算核心交付后仍需加入恢复、迁移和关系校验。

## 本地测试证据

- 固定 snapshot 用量测试 `TestGPT41*`、最终请求证明 `TestBudgetWire*`、profile 参数测试通过。
- `TestRequestLedgerJointRecoveryRollbackRetryAndClockRollback`：逐一注入四张表的 UPDATE 失败，
  验证所有 sibling 状态保持 pending/running；去除故障后重试成功，重复恢复不清空原租约。
- `TestRequestLedgerRecoveryOwnedByAppStartup`：普通打开数据库不提前恢复；真实 `Open` 重启完成联合恢复。
- 既有独立 accounting 重启及价格迁移测试通过；相关 Go 静态检查通过。

全部为合成数据与本地自动化。尚未执行本分支 Linux race、真实提供商调用、完整预算进程/浏览器验收。
不发布安装包，不据此宣称已对齐 Sub2API 预算机制。

## 下一步仍须完成

合入严格管理/core 同事务升级及预算核心；将 attempt 和 reserve 放入唯一派发事务，确认持久 mark 后再发送。
接入原 ID 的不确定提交恢复、finish 同事务结算、联合续租及预算启动恢复，再核实网页配置与全部失败路径。
最后运行服务测试、Linux race 和轻量 CI；这些步骤通过前，预算功能不能列为可用。
