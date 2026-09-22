# CPA Cloud

面向公司内部员工的单租户 AI 接入平台，依据公开协议独立实现。

**当前阶段：需求与架构规划。尚无可运行服务。**

## 已确认的方向

- Linux 无图形桌面部署；网页后台管理，命令行部署与维护。
- 管理员管理上游账号、员工、Key、模型权限和用量。
- 员工无需微信、Gate 或网页注册；使用独立 Key 接入标准 API。
- 员工可以使用 CC Switch 或直接配置工具，专用客户端是后续可选能力。
- 模型鉴权、权限检查和上游执行位于同一 Go 服务进程，不向 CLIProxyAPI 或 Sub2API 服务再转发。
- 借鉴 CLIProxyAPI、Sub2API 的产品能力；不嵌入、复制或逐段改写其实现。

## 开发入口

- [第一版功能与架构](docs/product-plan.md)
- [四个核心模块详细设计](docs/core-design.md)
- [员工直接使用 CC Switch](docs/employee-access.md)
- [验收矩阵](docs/acceptance-matrix.md)
- [协议来源记录](docs/protocol-sources.md)
- [独立实现与来源规则](docs/independent-implementation.md)
- [第三方声明](THIRD_PARTY_NOTICES.md)
- [贡献规则](CONTRIBUTING.md)

自有代码暂拟采用 MIT；权利人署名与许可证正文在首次公开发布前确认，目前不声明已授予 MIT 许可。

## 本地项目关系

本仓库从空白 Git 仓库开始。此前从 CPA Enterprise 克隆的派生项目完整归档在
`C:\workspace\cpa-cloud-reference`，原微信版保留在 `C:\workspace\cpa`。
归档仓库不参与本项目构建或发布，也不作为逐段改写的模板。
此前研究接触过部分原项目源码，因此不宣称严格洁净室开发或绝对无许可风险。
