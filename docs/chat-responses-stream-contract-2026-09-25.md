# Chat Completions ↔ Responses 流式转换契约

状态：2026-09-25 开发预览源码；不属于已发布安装包。

## 支持范围

只有显式持久路由的以下两个方向允许跨协议流式执行：

- 员工 `POST /v1/chat/completions`、`stream:true` → OpenAI Responses wire；
- 员工 `POST /v1/responses`、`stream:true` → OpenAI Chat Completions wire。

请求继续经过既有的严格转换计划、员工鉴权、模型/账号池选择、治理与最终派发屏障。每个员工请求只有一个治理/记账 parent 和一次上游派发；不会先试探一个协议再重放另一个协议。Messages、Gemini、有状态 Responses、background、previous/conversation 和未列出的跨协议流方向仍按原规则失败关闭。

首批只承诺转换模块已经支持的文本、函数工具调用及结果关联语义。未知或无法无损表示的输入、事件或终止状态返回固定协议错误，不能静默删除后继续。员工 Key、Cookie 和其他员工鉴权信息不会转发上游。

## SSE 与终止语义

- 上游必须返回 `text/event-stream`，解析器按完整 SSE 帧处理拆包、多行 data、CRLF、注释行和既有限额。
- Responses wire 只有合法 `response.completed` 构成成功；failed、incomplete、error 或提前 EOF 失败。
- Chat wire 只有合法终止 choice 后的 `[DONE]` 构成成功；提前 EOF 失败。
- 转换逐事件进行，不为缺失的上游终止伪造成功事件。原始上游事件 JSON 先进入既有用量观察，再转换为员工协议。
- 客户端取消会取消唯一上游请求，并把共享治理、记账、模型请求和 attempt 结算为 `cancelled`。

## 下游写入安全边界

开始上游请求前，服务必须确认响应写入器支持 flush 和 write deadline。每个转换后事件使用 30 秒写 deadline；短写、write error、flush error 或无法设置/清除 deadline 都终止执行并取消上游。已经向客户端尝试写入任何 SSE 字节后，不再追加 JSON 错误或重放请求。终止事件只有在写入和 flush 都成功后才允许把请求记为成功。

## 验收证据

精确提交 `f2d461559c21fcd862c167d63d3eb5b0a672fd34` 的 Windows 二进制通过隔离真实进程 smoke：两个方向均看到转换后的文本终止序列、每次一次派发、上游未收到员工凭据，并且客户端取消传播到上游。组件和 HTTP 测试另覆盖 Responses/Chat 终止状态机、拆包、工具事件、短写、flush failure、write deadline、终止 flush failure 不得记成功，以及共享账本结算。

这些证据只使用随机回环端口、临时数据目录和合成上游，不代表真实供应商、会员账号、未修改 CLI 或 CC Switch GUI 兼容。完整 Go、vet 与 Linux `CGO_ENABLED=1 -race` 以该提交对应 PR 的实际 CI 结果为准；结果出现前不借用旧提交的 CI。

## 公共协议来源

- OpenAI Chat Completions streaming: https://platform.openai.com/docs/api-reference/chat-streaming
- OpenAI Responses streaming: https://developers.openai.com/api/reference/resources/responses/streaming-events
- WHATWG Server-Sent Events framing: https://html.spec.whatwg.org/multipage/server-sent-events.html

实现未引入新的运行时第三方依赖；既有依赖许可证仍以仓库记录为准。
