# 出站代理首批契约

状态：首批源码已实现，2026-09-23；独立验收与未完成边界见[集成状态](integration-status.md)。对应 PROXY-01/SEC-02，不包含 Codex、HTTP/SOCKS、轮换或出口 IP 探测。
来源为本仓需求与公开标准，未使用参考产品源码作为实现模板。2026-09-23 核对的协议依据：
[RFC 9110 CONNECT](https://www.rfc-editor.org/rfc/rfc9110.html#name-connect)、
[Go 1.26.6 HTTP Transport](https://pkg.go.dev/net/http@go1.26.6#Transport)、
[Go 1.26.6 TLS Config](https://pkg.go.dev/crypto/tls@go1.26.6#Config)。

## 决策与信任边界

网络出口与企业 LAN 代理的新增信任边界见 [ADR 0001](adr/0001-explicit-outbound-proxy.md)。

管理员可选择模型流量经过一个显式配置的网络代理；员工仍只使用 CPA Cloud Key，管理 API 不加入模型请求链路。
这是网络出口选择，不是增加另一个模型业务代理。代理可观测目标地址与连接元数据；上游内容与凭据仍由端到端
TLS 保护。代理用户名和密码只发送给经过证书校验的代理，不写入模型请求头。

代理目录仅供管理员维护。允许明确配置公司 LAN 地址，拒绝环回、链路本地、组播、未指定地址及云元数据地址；
环回只可由已有测试开关允许。每个代理保存明确的 `address_scope=public|private`；所有 DNS answers 必须落在
同一许可类别，每次拨号重新检查，不允许跨类别重绑定或混合 answers，CGNAT 也拒绝。private 类别包括 RFC1918
和 IPv6 ULA；它不包含链路本地地址。LAN 代理的许可不能
放宽模型目标的地址限制。模型目标继续使用现有地址校验，
Gemini 仍固定官方目标；禁止依赖环境代理变量、跳过证书校验、自定义转发目标或自动回退直连。

首批支持 HTTPS CONNECT、API Key 账号一对一绑定，以及四个生成协议、目录读取、目录测试、生成恢复的一致出口。
不支持 HTTP/SOCKS、自动代理轮换、出口 IP 探测或 Codex 绑定。Codex 的生成/Responses、目录和 OAuth token/refresh
目前分用三个客户端；在这些路径及授权会话配置全部统一前，绑定接口必须明确拒绝 Codex，不能只有部分请求走代理。
这些未实现项继续属于 PROXY-01 总目标，不因首批范围删除。

## 存储与版本

- `outbound_proxies`：ID、名称、scheme、host、port、address_scope、启停、revision、connection_revision、
  凭据密文及 key version、创建/更新时间。
  地址字段不能带 userinfo、path、query、fragment；凭据以独立输入字段提供。用户名和密码整体加密，用独立 AEAD
  purpose 与 proxy ID 绑定。列表只返回 `has_credentials`，不返回用户名、密码、密文或密钥标识。
- `upstream_proxy_bindings`：每 upstream 至多一个 proxy，外键删除 RESTRICT。绑定必须要求已保存目标为 HTTPS，
  即使测试开关允许环回 HTTP，绑定接口也明确拒绝。没有绑定才使用显式直连；停用、解密失败或配置无效都失败关闭。
  重启只做严格 schema、外键与持久字段/关系校验，损坏则拒绝启动，不联网。单条密文无法解密可保留管理员修复入口，
  但相关代理和账号不可执行，绝不直连。
- 代理创建使用 operation ID 幂等；相同 ID、不同输入冲突。不得把明文秘密或无密钥密码摘要作为幂等索引。
  编辑使用 expected proxy revision；绑定/解绑使用 expected upstream revision 和预期 proxy revision。
- 任何代理编辑都递增管理 `revision`；只有地址、scope、认证或启停变化递增 `connection_revision`，并与
  **所有绑定账号的 revision 递增**在同一事务中提交。任一版本溢出或写失败全回滚。纯名称修改不改变出口版本，
  不打断恢复操作。绑定/解绑递增账号 revision，不改变其 API Key。
- 连接变更使现有路由、目录缓存、健康观测和恢复快照的账号版本屏障失效，迟到请求不能新增针对新版本的冷却。
  已有 cooldown/恢复隔离仍保留，变成需要管理员核对的配置变化；修改代理不是生成可用的证明，管理员可按当前
  账号版本和事件主动 clear。不能隐含承诺修改出口就立即解除已有隔离。
- 路由选定时冻结 proxy ID/connection_revision；最终派发再验证账号、映射和绑定。代理管理写与现有 admission 锁协调，
  不能拿着数据库事务等待外层锁。已派发请求可用原连接结束；旧排队请求保守终止，不静默换出口重放。

新增表必须具备严格 DDL/index/FK 校验、旧库升级、坏存量拒绝、事务回滚、修复后重试及重启验证。
降级必须使用升级前完整数据目录及匹配程序。此为运维要求：现有旧程序可能忽略新增绑定表并直连，新增 schema
本身无法约束旧二进制，不能宣称有技术降级屏障。安装/恢复文档须明确禁止旧程序直接读取含绑定的新库。

## 管理接口

所有入口要求管理员会话，写操作另有 CSRF/Origin，不接受员工 Key。

| 入口 | 用途 |
| --- | --- |
| `GET /admin/api/v1/outbound-proxies` | 有界分页代理列表与无秘密状态 |
| `POST /admin/api/v1/outbound-proxies` | operation ID 幂等创建 |
| `PATCH /admin/api/v1/outbound-proxies/{id}` | expected_revision 条件更新与凭据替换 |
| `PUT /admin/api/v1/upstreams/{id}/proxy` | 条件绑定或显式解绑为直连；不得因代理无效自动解绑 |
| `POST /admin/api/v1/outbound-proxies/{id}/tests` | 创建有界握手检查操作 |
| `GET /admin/api/v1/outbound-proxies/{id}/tests/{operation_id}` | 查询原操作，断线/重启不自动重发 |

握手检查只能使用已保存、且绑定该代理的 API Key 账号目标，提交相应账号与代理 revision。
只建立 CONNECT 与目标 TLS，不发送模型生成、目录或账号认证信息；检查耗时不等于模型延迟或生成可用。
不接受任意测试 URL，不调用外部出口 IP 服务。操作结果为固定诊断码与有界时长，代理错误正文永不回传。

创建/替换凭据后不读回密码。网络写入结果未知时查询实际版本或同 operation，不创建新 ID 规避冲突。
代理停用不把绑定账号改成直连；网页明确标识绑定账号不可用并允许管理员主动解绑。

## 单一出口获取与连接策略

使用共享 `clientForRoute`/出口快照接口，覆盖 API Key 生成、目录、健康 catalog、恢复 runner；本地凭据检查零网络。
不能分别在四个 forwarder 维护独立代理选择，也不能更改共享 `App.http` 的可变 Proxy 字段影响在途账号。
连接缓存按 proxy ID/connection_revision 隔离；连接配置编辑后关闭旧空闲连接，在途连接正常结束，容量和缓存总数有界。

集成时不能把当前 `accountPoolLease.MarkDispatch()` 当成数据库版本复核：它只更新内存派发阶段。
代理批次需增加共同的最终派发门槛，在 admission 读锁内校验原账号 revision、出口绑定及连接版本，并冻结
本次客户端；先完成需要的用量持久化，再标记 MayHaveSent，释放锁后执行网络。预检阶段已发现的账号出口问题
仍可按既有派发前规则换号；最终门槛之后不得换号或替换客户端。legacy 单账号路线与 `count_tokens` 也必须覆盖。
模型目录目前部分内部函数只接收 ID/密文，接线时需显式携带原 revision，并在每页请求前复核同一出口快照；
不能读新出口后继续发送旧版本密钥，也不能在分页期间静默切换代理。

每次建连本地解析并校验代理与目标；目标的全部解析地址都必须允许。CONNECT 使用已校验目标字面 IP 与端口，
避免由代理再次解析域名绕过目标地址限制。隧道内 TLS 仍以原目标 hostname 做 SNI 与证书校验；连接代理的 TLS
以代理 hostname 校验。两层 TLS 均保持验证，至少 TLS 1.2，禁止 redirect。标准允许的 CONNECT 2xx 语义由
有界解析器处理，保留紧随 header 到来的隧道数据；失败响应、超长 header 和取消均关闭 socket。

Proxy-Authorization 仅出现在代理握手；模型认证头、员工 Key、Cookie、Origin、CSRF 均不能进入该握手。
已绑定账号的 transport 永不丢弃绑定改为同账号直连。账号池仍可按既有配置选中另一个明确直连的账号，派发前
可证明安全的换号仍遵循原规则；不增加代理自动轮换。现有 handler 在进入执行器前已标记 MayHaveSent，因此即使连接失败，
也不能据网络错误猜测安全重放；仍按既有保守失败规则结束当前请求。

## 分工和验收

根任务负责契约、共享路由/版本与迁移集成；原服务任务负责代理目录、加密、绑定与事务；原协议任务负责共享
transport/CONNECT、全 API Key 执行点与网络边界；网页任务负责代理管理、绑定、测试结果及未知写入恢复。
文件所有权须在派发前列清，避免争改 app.go、store.go 和 route。

必须覆盖五环节：管理入口、全路径执行、加密持久化、连接/重启生命周期、可复现证据。

- 合成代理验证 Chat/Responses/Messages/Gemini JSON/SSE、目录、catalog health、恢复都走绑定出口；本地测试零联网。
- 员工密钥不进代理握手；代理密码不进上游；AAD/账号/代理身份替换、日志/DB/WAL/响应秘密扫描。
- 目标与代理 DNS 变化、混合公私地址、私网目标、坏证书、旧 TLS、301/307、407、慢 header、取消、响应上限。
- 代理编辑/停用、绑定修改与最终准入竞争；已派发不重放；代理失败无直连，旧失败不隔离新账号 revision。
- 迁移/事务失败、操作幂等、清单分页、坏密文、重启恢复；员工 Key、OAuth 来源绑定及旧账号不丢失。
- 本地/CI 只用合成代理、证书与凭据；测试注入不能暴露生产 endpoint override 或 InsecureSkipVerify。

本规格不是发布授权的扩展：仅当前仓库、轻量 CI；阶段完成后再统一决定安装包，不操作真实账号或用户代理。
