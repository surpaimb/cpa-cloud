# 出站代理管理首批接口

状态：2026-09-23，首批源码接口已实现；验收见[集成状态](integration-status.md)，下载版 preview.3 不含此功能。信任与执行边界继承[代理契约](outbound-proxy-contract.md)。

以下均为 `/admin/api/v1` 下管理员接口；写入验证 session、Origin、CSRF，员工 Key 不可使用。
JSON 字段采用 snake_case；revision 为 1 到 9007199254740991 的整数，时间为 RFC3339。

## 代理目录

`Proxy` 只包含：`id, name, scheme, host, port, address_scope, enabled, revision, connection_revision,
has_credentials, created_at, updated_at`。scheme 固定 `https`；scope 为 `public|private`。
用户名、密码、密文、key version 和幂等指纹均不读回。host 为主机名或裸 IPv4/IPv6，不接受 URL。

- `GET /outbound-proxies?after_id=...&limit=50`：`{items:Proxy[],next_cursor:string|null}`，按 ID 排序，limit 1..100。
- `GET /outbound-proxies/{id}`：读取一个 Proxy，包含其最新 revision。
- `POST /outbound-proxies`：`{operation_id,name,scheme,host,port,address_scope,enabled,credentials?}`。
  credentials 形如 `{username,password}`；不需要认证时省略整个字段。operation_id 是 UUID，首次和同载荷重试
  均返回原 Proxy；同 ID 不同载荷为 409。列表密码输入不可预填或从旧值推测。
- `PATCH /outbound-proxies/{id}`：完整配置 `{expected_revision,name,scheme,host,port,address_scope,enabled,
  credential_mode,credentials?}`，`credential_mode=keep|replace|clear`。keep/clear 不接受 credentials；replace
  必须携带新凭据。返回最新 Proxy。该操作是 CAS，不能以新 revision 自动重放丢失响应的变更。

操作编号只在同一次创建尝试中稳定复用。创建结果未知时保留原载荷和 operation_id，可由用户显式重试原操作，
不能改字段后沿用同号，也不能自动生成新号重复创建。PATCH 结果未知时阻止继续写入，先 GET 最新对象并让用户
核对，不自动覆盖。密码仅在当前表单内存保留，成功、取消或重开页面时清空，不能写 local/sessionStorage。

## 账号绑定

- `GET /upstreams/{id}/proxy`：`{upstream_id,upstream_revision,binding:null|Binding}`。
  `Binding` 为 `{proxy_id,proxy_revision,connection_revision,enabled,name}`，不表示代理握手或模型生成已验证。
- `PUT /upstreams/{id}/proxy`：`{expected_upstream_revision,proxy_id,expected_proxy_revision,bind:boolean}`；
  bind=true 表示绑定/换绑到指定代理；false 表示明确解绑为直连，此时必须提交当前绑定代理的 ID/revision。
  返回与 GET 相同的最新结构。Codex 账号和 HTTP 目标均明确返回 400；没有当前绑定时解绑返回 409。

绑定编辑前重新获取账号绑定与可选代理的最新 revision。任何 409 都先读取实际状态，不能自动以新 revision
重放。结果未知也先读取实际绑定再允许新写入。停用代理不会解绑；网页明确提示绑定账号不可用，解绑由管理员
主动决定。代理连接配置修改会改变相关账号版本，但不清已有故障隔离。纯名称编辑不改变连接版本。

## 失败和界面

沿用 `{error:{code,message}}` 固定脱敏错误：400 `invalid_proxy`/`unsupported_proxy_binding`，404 `not_found`，
409 `revision_conflict`/`operation_conflict`/`binding_conflict`，503 `storage_unavailable`/`proxy_unavailable`。
解析未知字段、无效 revision、额外凭据字段时均拒绝。不得把网络失败说成“保存失败，原状态不变”。

本 UI 子批先交付目录和绑定，可命名“出站代理”。首屏说明 HTTPS CONNECT；scope 提示公网/公司内网，并提醒
内网出口不允许内网模型目标。用户名与密码单独输入，密码隐藏；编辑默认“保留认证”。可看到启停、配置版本、
是否保存认证，但不声称连通、IP、延迟或生成正常。桌面和手机均可操作；以旧版本后端 404/缺能力字段给出
明确不可用说明，不假造数据。

握手测试操作、查询恢复及结果展示仍按总契约单独接线；本子批不显示空实现测试按钮，也不因此把 PROXY-01
或全部代理能力标记完成。未来测试不会发送模型提示或上游认证信息。
