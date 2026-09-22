# CPA Cloud 网页管理后台

React + TypeScript + Vite 管理后台。开发服务器把 `/admin/api` 和 `/healthz` 代理到
`http://127.0.0.1:8787`；生产构建输出到 `web/dist`，由 CPA Cloud Go 服务的
`--web-dir` 参数提供静态文件。

## 开发与验证

```powershell
bun install
bun run dev
bun run typecheck
bun run test
bun run build
```

前端不包含演示数据。`src/test` 中的网络响应仅由 Vitest 测试进程注入，不会进入生产构建。

## 第三方来源与许可证

依赖版本由 `bun.lock` 固定。主要直接依赖均从 npm registry 获取：React、React DOM、
Vite、`@vitejs/plugin-react`、TypeScript、Vitest、jsdom 与 Testing Library；这些项目按各自
上游许可证使用（主要为 MIT，TypeScript 为 Apache-2.0）。发布前应依据锁文件生成完整依赖
清单并保留各依赖要求的许可证文本。

本目录的界面、文案、测试和图形均为本项目独立编写；未使用归档 CPA、CLIProxyAPI、
Sub2API 或 CC Switch 的实现与素材。
