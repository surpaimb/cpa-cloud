# Key 与账号池分组绑定契约

状态：2026-09-29 开发预览实现契约。本文只定义员工 Key 对既有账号池 `account_groups` 的收窄策略；不引入员工身份组、计费组、TPM/成本倍率、支付、自助授权或真实供应商验证。

## 独立实现与术语

本批依据 CPA Cloud 自有功能规格、Go [`database/sql`](https://pkg.go.dev/database/sql) 公共事务接口，以及 SQLite 的[事务](https://www.sqlite.org/lang_transaction.html)、[外键](https://www.sqlite.org/foreignkeys.html)与 [`PRAGMA`](https://www.sqlite.org/pragma.html) 文档独立实现；不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或相邻参考工作区的源码、测试、迁移、资产和文档。本批不新增第三方依赖；既有依赖及许可证记录继续适用。

本文的“账号池分组”只指既有关系：

```text
account_groups <- account_channels.group_id <- model_account_pool_routes.channel_id
```

它不是员工治理组，也不能扩大员工、Key 模型、协议、来源网段或上游提供商权限。最终可用范围始终是所有既有授权层与本策略的交集。

## Key 策略表示与兼容语义

Key 策略的读取结果、创建输入和替换输入增加：

```json
{
  "account_group_mode": "all",
  "account_group_ids": []
}
```

- `account_group_mode` 只允许 `all` 或 `selected`。
- `all` 必须搭配空数组，保留此前的路由行为。
- `selected` 可搭配 0 到 64 个账号池分组 ID；空数组是明确的 deny-all，不回退为 `all`。
- 每个 ID 为 1 到 128 个 UTF-8 字节，只允许 ASCII 字母、数字、`-`、`_`、`.`、`:`、`/`，总字节数不超过 8192；重复 ID 非法。规范化结果按字节升序排列并与调用方切片脱离。
- 每个所选 ID 必须在同一数据库事务中指向现存 `account_groups` 行；未知 ID 返回固定的 `invalid_key_policy`，不暴露数据库细节。

创建 Key 时，整个 `policy` 省略或其中两个账号分组字段同时省略，都解释为 `all` 加空数组。替换 Key 策略时：

- 两个字段同时省略，保留当前账号分组策略；
- 只提供一个字段，拒绝为 `invalid_key_policy`；
- 显式提交 `all` 与空数组，清除此前选择；
- 显式提交 `selected` 与空数组，保存 deny-all。

策略使用既有 `revision` 和 `expected_revision`。协议、公开模型、来源网段或账号分组任一维度成功替换，都在同一事务中只推进同一个 revision；旧 revision 返回 409 `revision_conflict`。创建操作的幂等指纹包含规范化后的账号分组字段，因此同一 `operation_id` 不能以省略、清空或不同选择重试来改变策略。Key 明文仍只在首次成功创建时返回；相同操作重试不返回明文。

## 持久化与启动迁移

`internal/keypolicy` 持有这一策略维度的值对象及存储边界。迁移在既有账号池表建立后运行，并在一个事务中建立和核验：

- `access_key_policy_account_group_migration_state`：单例、版本 1、完成时间的耐久标记；
- `access_key_policy_account_groups`：每个 Key 一行，保存 `account_group_mode`；
- `access_key_policy_account_group_members`：Key 与 `account_groups` 的选择关系；
- `account_group_id,key_id` 查询索引。

升级旧数据库时，每条既有 Key 策略回填 `all` 与零成员，原 revision 不变。迁移必须核验精确列、关键约束、外键、索引、覆盖完整性和 `PRAGMA foreign_key_check`，再写入耐久标记并提交。以下情况均 fail closed 且整笔回滚：未标记的部分 schema、标记缺失或损坏、已标记库缺少任一 Key 覆盖、孤儿成员、未知分组外键、约束或索引不兼容。修复冲突后可重试；正常重启只验证，不重写授权。

Key 创建、加载和替换必须与账号分组行在同一 `*sql.Tx` 中完成。任何存储错误都不得留下半写入的基础策略、账号分组模式、成员或 revision。

## 选择、预检与最终派发

`all` 保持旧行为，包括没有显式账号池配置时的旧单路由。`selected` 只允许满足全部条件的显式池候选：

1. 当前公开模型具有正 revision 的 `model_account_pool_configs`；
2. 候选 `model_account_pool_routes.channel_id` 非空；
3. 渠道存在且 `account_channels.group_id` 非空；
4. 分组 ID 位于当前 Key 的规范化选择中；
5. 上游、模型、提供商、wire protocol、凭据状态、恢复隔离、冷却与并发条件仍满足既有运行时规则。

因此，无渠道、无分组、断开的映射与旧单路由在 `selected` 下均不匹配。候选选择、账号特定预检、最多一次的预检换号和租约持久化不得落到选择范围之外；池内没有合格候选时返回既有脱敏的不可用/不允许错误，不向其他组回退。

真正派发前必须在现有 admission 锁内的同一个 `*sql.Tx` 重新核验：

- Key 存在、未撤销、未过期，员工仍启用；
- Key 策略 revision 与最初快照相同，协议、公开模型、来源和账号分组仍允许；
- 公开模型与员工授权仍有效；
- 显式池 revision 与租约快照相同；
- 精确路由仍连接同一账号、上游模型和 wire protocol；
- 精确 `route → channel → group` 映射仍属于该 Key；
- 账号 revision、状态、代理出口和恢复状态仍匹配；
- 既有预算价格快照与账本前置写仍成功。

事务提交成功后才允许恰好一次网络调用。策略收紧、映射收紧、CAS 冲突、池或账号 ABA、陈旧缓存、存储失败、预算或价格变化都必须产生 0 个新 attempt、0 次上游调用，并且不能触发选择范围外的 fallback。网络开始后沿用原路由、策略、价格和 attempt 快照结算；崩溃恢复不得重放可能已经派发的请求。

## 目录、资源与后台任务

- `/v1/models`、`/v1beta/models` 和管理端 Key 的 `effective_models` 只列出同时满足既有授权且至少有一个当前合格组内候选的公开模型。`all` 保留旧目录行为。
- 四个员工协议入口、Gemini count-tokens 及任何预检换号都使用同一组策略。
- 后台 Responses 任务在入队时保存原 Key policy revision；未派发任务在 claim、重启恢复和最终派发时重新核验原 Key、来源信任、账号分组及精确映射。失败任务不得新建 attempt 或调用上游。
- 资源 GET 与继续执行必须按当前 Key 和当前账号分组可达性收窄，防止交叉 Key 或策略收紧后继续访问。已由合法 owner 发起的 cancel/delete 是停止操作：即使策略后来收紧，仍允许它终止原资源，但不能据此继续或重放上游工作。

## 管理界面与安全边界

网页 Key 创建和编辑器提供“全部账号池分组 / 指定账号池分组”选择，清楚区分 `selected + []` 的拒绝全部语义。保存采用同一 revision CAS；冲突保留未提交草稿并要求刷新。创建成功只展示一次 Key 明文。旧服务不具备该能力时，网页必须以固定提示禁用保存，不得静默丢弃字段。桌面宽度和 390px 视口均须可操作。

`GET /system/status` 以独立只读能力位 `features.key_account_group_policy` 声明完整后端接线。旧服务即使已有 `key_access_policy=true` 但缺少该能力位，新网页仍可编辑协议、模型和已声明的来源策略，却不读取、不发送也不假保存账号组字段。新服务声明能力后，Key 列表或策略 GET 缺少任一账号组字段、返回 `null`、未知 mode 或错误成员类型时，网页失败关闭并禁用保存，不能把畸形值默认为 `all`。

审计与错误只记录固定 action、操作者、Key/目标 ID、成功结果和时间等最小元数据；不得记录请求正文、Key、Authorization、Cookie、上游凭据、提示或模型响应。运行日志同样不得出现这些内容。TLS 校验、员工凭据不上游及现有密钥摘要/上游凭据加密要求保持不变。

## 验收与发布校准

自动验收至少覆盖：

- 旧库回填、部分 schema、标记损坏、覆盖缺失、外键孤儿、迁移中断回滚和正常重启；
- `all`、`selected + []`、空组、未知组、规范化、上限、创建省略与 PUT 保留/清除；
- 跨 Key 隔离、四协议、两个模型目录、资源 GET/continue/cancel/delete、后台 claim 与重启；
- 候选筛选、预检换号、租约、最终事务、策略/映射 CAS 与 ABA，断言拒绝路径 0 attempt、0 上游；
- 浏览器管理员流程、一次性明文、冲突草稿、旧服务能力提示、桌面与 390px；
- 完整 Go 测试、关键 race 套件、Web lint/test/build、固定二进制的临时目录真实进程 smoke。

README、产品计划、开发计划和功能对齐表必须区分“源码已测试”“preview.3 下载包未包含”“真实提供商/会员账号未验证”。本批不发布仓库、包、tag、部署或生产配置；最终合并由协调线程按精确 head SHA 执行。
