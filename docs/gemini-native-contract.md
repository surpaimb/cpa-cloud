# Gemini 原生 API 开发预览契约

状态：开发预览，2026-09-23。本文先于实现定义 CPA Cloud 的 Gemini Developer API Key 通路及 Google 会员授权边界。实现依据 Google 官方公开文档与本项目独立测试，不复制第三方代理实现。

## 交付范围

- 员工使用 CPA Cloud Bearer Key 调用 `POST /v1beta/models/{model}:generateContent` 与 `POST /v1beta/models/{model}:streamGenerateContent`。
- `{model}` 是 CPA Cloud 管理员创建的本地模型 ID；服务在同一 Go 进程内校验员工、模型权限和路由，再替换为该路由的 `upstream_model`。
- 上游类型为 `gemini-api-key`，凭据必须是 Google AI Studio/Gemini Developer API Key。它不是员工 Google Key，也不代表 Google AI Pro、Ultra 或 Code Assist 会员已经接入。
- `GET /v1beta/models` 返回当前员工有权使用且路由可用的本地 Gemini 模型，并支持官方 `pageSize`/`pageToken` 分页形状。管理员仍通过 `POST /admin/api/v1/upstreams/{id}/discover-models` 从上游同步候选模型，发现结果不自动创建路由或放宽员工权限。
- 本批不实现 Files、Caches、Batch、Live、Interactions、embeddings、grounding、内置工具、图片/音频/视频内联数据或 Vertex AI。

## 上游与凭据

- 生产端点固定为 `https://generativelanguage.googleapis.com`。管理 API 可省略 `endpoint` 或提交该固定地址；不能提交其他生产主机、查询参数或凭据化 URL。
- 仅在显式 `--allow-loopback-upstream` 测试模式下允许字面量回环 HTTP(S) 地址，以便假上游验收。仍复用禁止重定向、重新解析并校验实际拨号 IP、TLS 校验、超时和响应上限。
- 上游请求使用 `x-goog-api-key`；不得使用员工 `Authorization`，不得将 CPA Cloud Key、管理 Cookie、CSRF Token 或原请求鉴权头转发给 Google。
- Gemini API Key 以独立 AEAD 用途串绑定上游 ID 和 `gemini-api-key/api-key` 类型，密文与现有 OpenAI-compatible/Codex 凭据不可互换。列表、错误和日志不得返回 Key、密文或上游原始错误正文。
- 创建/替换密文与数据库写入保持事务边界；迁移失败必须保留旧表、旧路由和旧员工 Key。正常重启不改变员工 Key 或 Gemini 路由。

## 请求子集

请求体上限 4 MiB，必须是单个 JSON 对象且拒绝未知顶层字段。支持：

- `contents`：非空数组；每项支持可选 `role`（`user` 或 `model`）及非空 `parts`。
- `parts`：本批每个 Part 恰好支持 `text`、`functionCall` 或 `functionResponse` 之一。`functionCall` 支持 `id?`、`name`、`args?`；`functionResponse` 支持 `id?`、`name`、`response`。这些对象原样转发，不由 CPA Cloud 执行函数。
- `systemInstruction`：Content 对象，允许可选 `role` 和仅含 `text` 的 `parts`。
- `tools`：仅支持含 `functionDeclarations` 的工具；声明支持 `name`、`description`、`parameters?`、`response?`。内置搜索、代码执行、URL 上下文等工具在本批返回 `UNIMPLEMENTED`，不能静默丢弃。
- `toolConfig.functionCallingConfig`：支持 `mode` 和 `allowedFunctionNames`。
- `generationConfig`：支持 `candidateCount`、`stopSequences`、`maxOutputTokens`、`temperature`、`topP`、`topK`、`seed`、`presencePenalty`、`frequencyPenalty`、`responseMimeType`、`responseSchema`。其他生成配置返回 `UNIMPLEMENTED`。
- `safetySettings`：按 JSON 原样转发，但必须为数组。`cachedContent`、`serviceTier`、`store` 和未列字段暂不支持。

CPA Cloud 只验证本地支持边界、结构上限和函数名；模型相关取值范围由 Gemini 上游决定。上游返回的 `candidates[].finishReason`、`usageMetadata`、`promptFeedback`、`functionCall` 和其他成功响应字段保持原生 JSON，不伪造未知 usage，也不改写为 OpenAI 格式。

## 流式、取消与错误

- 流式请求固定调用上游 `:streamGenerateContent?alt=sse`，仅接受 `text/event-stream`，逐帧转发原生 `data:` JSON 事件，不增加 OpenAI `[DONE]`。
- 服务限制单个 SSE 事件和总流量；检测到畸形事件、上游提前断流或超限时终止并把请求记为 `failed`/`interrupted`，不能发送伪造成功候选。
- 员工断连直接取消上游 request context，并将账目结果记为 `cancelled`。撤销/停用与 admission 锁保持现有语义：新请求立即拒绝，已通过准入的在途请求不承诺强制中断。
- 在尚未发送 2xx 时，CPA Cloud 返回 Gemini 风格 `{error:{code,message,status}}` 固定脱敏错误。员工鉴权失败为 401 `UNAUTHENTICATED`；无权限为 403 `PERMISSION_DENIED`；无可用路由为 503 `UNAVAILABLE`；本地不支持字段为 400 `UNIMPLEMENTED`；上游 400、401/403、429、其他失败分别映射为脱敏的 400、502、429、502。
- 一旦流式 200 已发送，后续本地失败只能关闭流；不把内部错误、凭据或上游正文作为 SSE 数据发给员工。

## 模型发现

- 管理发现调用 `GET /v1beta/models?pageSize=1000`，使用保存的 `x-goog-api-key`，并跟随合法 `nextPageToken` 直到完整结束；仅采纳 `name` 形如 `models/{id}` 且 `supportedGenerationMethods` 包含 `generateContent` 的项目。
- 返回值仍是 `{items:[{id}]}`，ID 去掉 `models/` 前缀、跨页去重并排序。整个分页操作共用一次超时、累计响应字节上限、总唯一模型数上限和页数上限；page token 有长度/字符约束并拒绝重复或循环。取消、中间页失败、畸形/超限响应均返回固定错误或停止响应，绝不把已读取页面伪装成完整目录。
- 员工 `GET /v1beta/models` 是已配置且获准的本地目录，不暴露未路由的上游发现结果。

## Google 会员授权边界

Google 官方 Gemini CLI 文档区分三条通路：Google 账号登录使用 Gemini Code Assist，AI Studio 使用 Gemini Developer API Key，Vertex AI 使用 ADC/服务账号/API Key。部分组织或 Code Assist 许可证还需要 Google Cloud Project。

官方 Gemini CLI 条款与 FAQ 同时明确：第三方软件复用或“搭便车”使用 Gemini CLI OAuth 去访问其后端服务违反适用条款和政策；推荐第三方使用 Vertex AI 或 Google AI Studio API Key。基于这一明确边界，本项目本批不导入 Gemini CLI 缓存 OAuth Token、不复用官方 CLI OAuth 客户端、不调用未公开 Code Assist 后端，也不提供所谓“会员池”实验开关。

如果未来 Google 提供适用于 CPA Cloud 这类服务端代理的公开授权/委托协议，需另写契约，明确 OAuth 客户端注册、redirect URI、scope、Cloud Project、刷新、撤销、组织管理员授权和服务条款，再做默认关闭实验。缺少该协议不推导为普遍法律结论，也不从产品目标中删除会员能力；当前状态只是有据可验的阻塞。

## 验收

独立假上游测试至少覆盖：

1. API Key 创建、类型隔离、密文落盘无明文、替换和重启恢复。
2. 事务迁移保留旧上游、模型、员工 Key；失败回滚。
3. 员工 Key 鉴权、模型 allowlist、撤销和停用。
4. 文本、system instruction、函数声明、function call/response 和 generation config 原样转发。
5. 非流式 finish reason/usage 保留；SSE、多帧、取消、畸形/过大响应和上游错误映射。
6. 管理模型发现和员工模型目录；不得携带员工 Key 探测上游。
7. 测试只使用回环假上游，不读取真实凭据，不向 Google 发请求，不能据此宣称真实账号已验证。

## 官方来源

查阅日期均为 2026-09-23：

- Gemini API 参考与 `x-goog-api-key`：<https://ai.google.dev/api>
- `generateContent` / `streamGenerateContent`、Content、FunctionCall、FunctionResponse、GenerationConfig、finishReason、UsageMetadata：<https://ai.google.dev/api/generate-content>
- Models list/get：<https://ai.google.dev/api/models>
- API 版本说明：<https://ai.google.dev/gemini-api/docs/api-versions>
- Gemini CLI 鉴权方式：<https://github.com/google-gemini/gemini-cli/blob/main/docs/get-started/authentication.mdx>
- Gemini CLI 条款与隐私边界：<https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/tos-privacy.md>
- Gemini CLI FAQ 的第三方 OAuth 边界：<https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/faq.md>
