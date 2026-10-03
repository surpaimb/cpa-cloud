# 员工自助订阅能力的 CLI 初始化前置校验契约

状态：2026-10-03，开发预览期的独立单点修复契约。设计基线为已合并 `main` `b907eec6ea6f7ff13fa032c04dc1a32e8e4ea337`，tree `689ad9f087510b729bf1ae920541f98a5fb5f990`。本文件只定义验收边界；实现、精确提交的 CI 和独立进程验收须另行报告，不能由合同本身推断通过。

本契约延续[产品计划](product-plan.md)、[独立实现规则](independent-implementation.md)、[开发计划](development-plan.md)、[预览契约](preview-contract.md)、[员工自助基础](employee-self-service-foundation-contract.md)、[本人订阅状态](employee-self-subscription-status-contract.md)、[本人钱包余额](employee-self-wallet-balance-contract.md)、[本人购买时记录](employee-self-subscription-purchase-snapshot-contract.md)和[员工本人月度续购](employee-self-monthly-renewal-contract.md)。后两份现有合同都要求缺前置在修改持久状态前拒绝；本批只补齐 CLI `--init` 与普通启动共用的写前校验，不新增业务能力。

## 已有能力与缺口

| 现有 CLI 开关（均默认 `false`） | 现有 self session 能力位 | 显式开启所需的现有开关 |
| --- | --- | --- |
| `--employee-self-subscription-purchase-snapshot-enabled` | `features.employee_self_subscription_purchase_snapshot` | `--employee-self-service-enabled`、`--employee-self-subscription-status-enabled`、`--employee-self-wallet-balance-enabled` 全部开启 |
| `--employee-self-subscription-renewal-enabled` | `features.employee_self_subscription_renewal` | 同上三个开关全部开启 |

两项 opt-in 相互独立；仅开启三个前置不自动开启任一目标能力，也不要求另一个目标能力或当前商业执行开关。默认关闭、路由/会话授权、响应、网页入口和商业事务的原有含义均不变。尤其不能把购买时本地钱包记录称作外部付款，也不能把月度续购扩展为自动续费或模型权益。

在本基线，`cmd/cpa-cloud/main.go` 已于解析参数后、分支到 `--init` 前校验许多自助开关，但遗漏上表两项。`--init` 随后直接调用 `service.Initialize` 并返回；普通服务启动则继续进入 `service.Open`。现有 `internal/service/app.go` 的 `App.Open` 对两项均有三前置校验，因此普通启动已有后一道拒绝，而 `--init` 不经过它。修复应在 CLI 的共同写前路径验证两项；保留 `App.Open` 现有校验及其语义，不以仅修改 `App.Open` 充当 `--init` 修复。

## 输入、拒绝与状态不变量

对每个目标开关，只要显式为 true 且三个前置中任一个为 false，普通服务启动和 `--init` 都以非零退出码（现有 CLI 约定为 `1`）及固定、脱敏的前置错误拒绝。错误必须明确指向被启用的目标开关和其三个必需开关；不包含管理员密码、目录内容、凭据或原始存储错误。两目标同时开启且缺前置时，错误选择应确定，不依赖 map 遍历或运行时序。校验必须在读取 `--init` 管理员密码、创建 data-dir、打开或迁移数据库、生成/修改安装密钥、持久写入和监听端口之前完成；拒绝不允许以 `service.Initialize` 或 `App.Open` 的后续失败代替。

对原本不存在的 `--data-dir`，无效组合退出后该路径仍不存在，且指定的动态、非 8787 监听地址不可被进程占用。对已存在的目录，拒绝前后的相对路径集合、文件类型、文件长度和内容 SHA-256 快照相同；原有管理员/数据库/密钥、WAL 和备份若存在则保持原样，不产生新文件、迁移或事务事实。该保证同时适用于普通启动和 `--init`，不以“初始化已存在”错误冒充前置拒绝。不得改变默认 data-dir，更不得在真实使用目录上做破坏性验收。

三个前置全部开启时，任一目标单独开启或两者同时开启都不应被此校验误拒。对新临时目录，带合法管理员密码的 `--init` 仍成功并可由既有 `--check-initialized` 确认；随后普通服务启动仍可按现有规则监听、停止，且只宣告实际开启的原有能力。没有目标开关时的既有 `--init`、普通启动和 `--check-initialized` 行为保持原义。CLI 层不得把可变的 `financial_settings.execution_enabled`、购买时记录开关或其他自助能力误增为这两项的启动前置。

## 独立验收与阶段门

测试输入和预期须从上表契约独立编写，使用**真实 CLI 进程**和表驱动矩阵，不只调用与实现同构的布尔表达式。对每个目标单独开启及两目标同时开启，枚举三个前置的全部七种缺项组合，并分别以普通启动、`--init` 运行：核非零退出、固定错误、输入密码不被回显、缺失目录仍缺失、没有监听和无持久文件。为每种模式另以预先初始化且含哨兵文件的目录重复代表性缺项组合，比对运行前后完整路径/类型/长度/哈希快照和初始化状态。至少覆盖三个前置全部开启时两个目标各自及共同开启的合法 `--init`，并回归默认关闭与已有普通启动/`--check-initialized`。动态端口必须由测试分配，不使用默认 8787、真实员工数据或真实上游。

后续实施先做定向 Go 测试、`go vet ./...`，并明确报告 Web typecheck/tests/build 未改变或实际复跑结果；再做动态非 8787 的隔离二进制进程验收及真实 Chrome 桌面/390px 既有自助能力回归。Chrome 回归只证明现有入口未受影响，不把本 CLI 修复描述为新 UI。最终单次推送须绑定新的精确 HEAD：非缓存全 Go、CGO race、vet、双 CLI build、Web 和 smoke 的 GitHub CI 结果与独立同 HEAD 固定二进制验收分开记录；未跑的项标为未验证。文档先独立提交供全文只读审，审完前不实施代码、不 push、不建 PR 或触发本批 CI。随后也不因测试通过自动授权 tag、native package、发布、部署或生产启用；`GOV-02` 仍是生产门槛。

## 排除范围、来源与许可

本批不改变旧路由、capability 名称/真值、JSON、UI、Origin、密码规则、员工会话、账务、DDL、worker、provider 或 `App.Open` 已有校验语义；不引入新的开关、SDK、依赖或素材。新代码和测试须显式注明本合同为 provenance。本契约根据上述本仓库功能规格和精确 `main` 源码独立编写，不复制、翻译或移植 CLIProxyAPI、Sub2API、归档 CPA 或邻近参考仓库；此前看过参考代码，因此不宣称严格 clean-room。

2026-10-03 核对的公开通用机制为 [Go `flag.FlagSet` 参数解析](https://pkg.go.dev/flag#FlagSet.Parse)、[Go `os.MkdirAll` 目录创建](https://pkg.go.dev/os#MkdirAll)与 [Go `net.Listen` 监听](https://pkg.go.dev/net#Listen)。它们不定义 CPA Cloud 的业务前置；本批没有新增供应商协议或第三方源码。现有依赖各保留原许可证与[依赖许可记录](research/dependency-notices.md)，本合同不引入许可证变更。
