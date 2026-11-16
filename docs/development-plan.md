# AI Hub 云托管平台（aihub-cloud）开发计划

## 目标与首期边界

基于 Coolify 为 AIHub Agent 提供源码包交付入口和开发者 Web 管理界面。Agent 通过 Skill/CLI 上传本地项目；平台安全检查并构建 OCI 镜像、推送到镜像仓库，再调用 Coolify API 完成应用部署。Coolify 是应用、部署、域名、运行日志、主机资源和用户权限的唯一控制面。

首期复用 Coolify 已有的团队/RBAC、应用模型、部署队列、域名/TLS、日志和运行时能力，交付 AI 项目从本地源码包到 Coolify 应用的闭环。不另建一套 Docker 运行时、路由控制面、宿主机资源采集或重复的服务生命周期 API。

## 架构与技术决策

- Coolify Laravel 是产品控制面与部署状态的事实来源；AIHub 保留 Go API/CLI、React 门户、源码包安全处理和 Skill。
- 受控构建继续使用 BuildKit。完成构建后推送不可变 OCI 镜像到 Coolify 可访问的镜像仓库，通过 Coolify API 创建/更新 Docker Image 应用并触发部署。AIHub 不直接管理生产容器或路由，也不维护第二套部署状态机。
- Coolify 管理用户/团队、应用、部署、运行参数、资源限制、域名/TLS、日志、回滚、主机资源、审计与生命周期操作。AIHub 仅保存 CLI 项目映射、构建元数据、镜像摘要和 Coolify 资源 UUID 引用。
- 管理台聚焦 AI 项目、源码部署、构建事件、CLI Token 和 Coolify 连接健康；应用配置、运行日志、域名、回滚、实例资源和运行操作链接或委托给 Coolify。
- Coolify API Token 和 registry credentials 只保存在服务端并加密，不暴露给浏览器或 CLI。外层 Compose 不再启动与 Coolify 重复的 Docker socket 管理器和 edge proxy。

## 开发者工作流与 API

- 提供 aihub-deploy Skill 与 aihubctl CLI：配置目标、扫描/打包工作区、部署、查询状态/URL、查看构建事件并打开 Coolify 应用详情。
- 上传包排除 .git、依赖目录、.env 和常见密钥文件，支持 .platformignore；服务端限制大小/文件数并安全解包，拒绝路径穿越、符号链接、硬链接和设备文件。
- Skill 生成 platform.yaml 声明 Web/Worker、端口/启动命令、健康检查和资源档位；歧义时停止并给出可操作错误。构建阶段不获得运行时密钥。
- AIHub API 使用本地会话管理门户、使用短期 Bearer Token 接收 CLI 请求；部署提交具备幂等性。Coolify API Token 和 registry credentials 仅由服务端持有。
- 开发者身份和项目范围必须映射到 Coolify Team/RBAC。AIHub Token 不能绕过 Coolify 团队边界；运行日志、部署历史和资源状态从 Coolify 获取或以其 UUID 关联。

## 部署、可靠性与观测

目标架构及新旧职责边界以 docs/coolify-integration.md 为准。

- 部署流程：源码包上传/校验 → BuildKit 构建 → 推送不可变 OCI 镜像 → 创建/更新 Coolify Docker Image Application → 触发 Coolify 部署 → 轮询终态并回写 Coolify URL/UUID。
- AIHub PostgreSQL 队列只负责构建和调用 Coolify；Coolify 管理部署排队、健康检查、流量切换、运行时清理和回滚。
- AIHub 凭证加密保存；CLI token、Coolify token 和 registry password 不写入源码包、镜像构建上下文或运行时环境。
- 门户展示源码构建事件和 Coolify 应用深链。应用运行日志、部署历史、域名、资源和生命周期操作由 Coolify 页面/API 提供。

## 测试与验收

- 单元测试：归档安全、项目识别、Coolify API 鉴权/响应、不可变镜像引用、凭证加密与脱敏、失败状态映射。
- API 测试：登录/CSRF/RBAC、幂等部署、集成设置权限和凭证掩码、越权访问拒绝。
- 端到端：Node 与 Python 源码包经 BuildKit 推送到真实 registry，由 Coolify 部署并返回可访问 URL；验证重部署、失败构建、失败部署、启停和删除。
- 安全验收：CLI Token 和服务端 API credentials 不泄露；构建凭证不进入构建上下文/产物；AIHub 用户不能绕过 Coolify team 授权。

## 里程碑

1. Coolify 接入契约：API Token/Project/Environment/Server/registry 配置、连接检查、API 版本与权限验证。
2. CLI/Skill：配置 Coolify 目标；源码安全打包、项目命名、幂等提交、状态/日志和 Coolify 资源链接。
3. 构建发布桥接：归档校验、Node/Python 检测、BuildKit 构建并推送 OCI 镜像，映射到 Coolify Docker Image 应用并触发部署。
4. 门户体验：保留 React 管理台，聚焦项目映射、构建记录、CLI Token 和 Coolify 连接健康。
5. 收敛旧运行时：确认 Coolify 闭环后，移除 AIHub 直接 Docker 管理、Traefik 路由、重复环境变量和宿主机指标职责。
6. 验收：使用本地 Coolify + 镜像仓库完成 Node/Python 从 CLI 源码包部署，验证重试、重复提交、权限、失败诊断和状态恢复。

## 明确假设

- 仅公司内网访问；首期不启用公网/ACME。内网 HTTPS 使用组织提供的 DNS 与证书；开发/演示环境可配置 HTTP 入口。
- 保持 Go + React + TypeScript；Coolify 原有 Laravel/Svelte 技术栈和界面不被替换。
- AIHub 首发只生成受控 Node.js/Python Web 镜像，不在 AIHub Worker 执行任意用户 Dockerfile/Compose；镜像运行由 Coolify 配置与治理。
- 必须配置 Coolify 可访问的 OCI registry；私有仓库凭证采用最小权限并可轮换。
- 开发者身份和访问范围映射到 Coolify Team/RBAC；AIHub Token 不得绕过团队边界。

## 当前实现状态（2026-11-11）

- 已实现：Coolify API client、加密集成设置、registry image push、Coolify Docker Image 应用创建/更新、触发并轮询部署、门户深链、CLI 构建/状态/事件。
- Worker 不再通过 Docker Engine 启动 AIHub 管理的应用；Coolify UUID 为应用与部署的外部引用。
- 尚未完成：用真实 Coolify 和 registry credentials 进行端到端验收，清理遗留 Docker workload 管理代码和代理，并改为展示 Coolify 目标主机/实例指标。
