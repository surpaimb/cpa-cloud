# 出站代理握手检查契约

状态：待实现，2026-09-23；是[出站代理契约](outbound-proxy-contract.md)的管理生命周期补充，不能把保存代理或目录读取当成握手通过。

## 入口与结果

管理员写接口 `POST /admin/api/v1/outbound-proxies/{id}/tests`，请求仅接受
`{operation_id,expected_proxy_revision,expected_connection_revision,upstream_id,expected_upstream_revision}`。
ID 为 UUID，revision 为 1..9007199254740991 的整数，既有 session/Origin/CSRF 规则保持。
只允许绑定到此代理的已启用 HTTPS API Key 账号。生产目标限制、Gemini 固定目标、两层 TLS 验证保持。
不接受目标 URL、提示、请求头或凭据；测试只完成 CONNECT 与目标 TLS，绝不发送目录或模型 HTTP 请求。

首次创建返回 HTTP 202，同 ID 同输入返回已保存操作（200）；不同输入为 409 `operation_conflict`。
`GET /admin/api/v1/outbound-proxies/{id}/tests/{operation_id}` 返回同一对象，404 不代表可以重新发起不同 ID。
对象字段固定为 `operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,
result_code,created_at,started_at,finished_at,latency_ms`，未产生的值为 null。
state 为 `pending|in_progress|completed`；result_code 仅 `handshake_ok|configuration_changed|proxy_unavailable|
target_unavailable|address_rejected|timeout|cancelled|interrupted|internal_failure`。
不能从统一 transport 错误猜测更精确诊断；无法区分代理与目标故障时用 `internal_failure` 或 transport 能证明的固定类别。
耗时使用 monotonic duration 且限定 0..10000ms，表示双层握手时间，不表示模型生成延迟。历史结果始终带被测版本。

## 持久化与工作生命周期

独立 `outbound_proxy_test_operations` 表保存上述无正文元数据、外键和严格状态约束。创建与版本/绑定校验在 admission
读锁及一个 SQLite 事务中完成。最多四个活动操作，全表历史上限一万；每代理同时最多一个活动操作。达到上限明确拒绝，
不删除旧操作来复用 ID，也不排无界队列。相同 ID 的查询/幂等返回优先于容量拒绝，不增加新任务。

提交成功后才启动一次 app-owned worker，HTTP 客户端断线不创建第二次操作。实际连接前再次验证原账号、代理管理/连接
版本、绑定与启停，在 admission 读锁内冻结 client；释放所有应用/数据库锁后执行 `egress.Client.ProbeTLS`，总 deadline 十秒。
不为网络失败自动重试，不回退直连。传输层在连接尚未建立时尝试本次已校验 DNS 地址属于同次握手，不另创建操作。

已执行的探测完成时，保存原版本及固定结果；若配置在探测期间改变，结果标 `configuration_changed`，不把历史成功标为
当前代理可用。不能解密代理凭据时零网络并失败关闭。只使用代理认证，不读取或传递上游 API Key。

终结存储失败只有限重试同一元数据（最多三次，短间隔），禁止再次连接；仍不确定保持未终结状态供重启恢复。
首次终结时间与耗时冻结，重复结算不变。Close 停止接收新操作、取消并等待实际工作及有界终结后再关库，不能只等待 timer。
重启把遗留 pending/in_progress 事务改为 completed/interrupted，时间回拨时钳制不早于原 created/started；绝不重新握手。
迁移严格验证 DDL、CHECK 字面量、索引、外键、存量字段与状态关系，错误全回滚，修复后可重试。

## 网页与验收

可选择一个明确绑定的账号作为目标，显示被测版本及“仅握手检查”。POST 结果丢失后按原 operation ID 查询，未知状态
不宣称未执行，不自动创建新 ID。超时轮询停止并保留查询入口；切换代理或退出页面取消浏览器查询，不影响已保存操作。
显示握手通过时同时说明未验证模型/会员调用；不显示出口 IP、代理密码、证书或原始错误正文。

使用合成证书、回环代理/目标与临时数据库验收：首次/幂等/输入冲突、CSRF/员工拒绝、四活动/同代理限制、坏证书、
禁止任意目标和 Codex、配置竞争、代理身份与凭据隔离、目标零 HTTP 请求、取消/终结写失败、关闭等待、重启零重放。
不修改 OS 信任、真实用户目录、8787 实例或生产凭据；首批不发布安装包。
