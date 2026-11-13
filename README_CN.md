> **V8 LTS 主线：** `main` 维护 V8 产品线，使用 V8 配置布局与配套 V8 Panel。配置路径、保留的 v0 API、旧面板禁写保护及回退要求见 [LTS V8 兼容说明](docs/lts/v8-development-compatibility.md)，正式制品从 [Releases](https://github.com/BlueSkyXN/CPA-Core-LTS/releases/latest) 下载。

# CPA Core LTS

[English](README.md) | 中文 | [日本語](README_JA.md)

CPA Core LTS 是基于 `router-for-me/CLIProxyAPI` 的长期维护分支。

- LTS 基线版本：`v6.9.49`
- 基线提交：`b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- 上游来源：<https://github.com/router-for-me/CLIProxyAPI>
- LTS 仓库：<https://github.com/BlueSkyXN/CPA-Core-LTS>
- 配套 Web 管理面板：<https://github.com/BlueSkyXN/CPA-Panel-LTS>
- 默认面板发布源：`https://github.com/BlueSkyXN/CPA-Panel-LTS`

## 维护模型

本仓库存在的原因是：上游在所选基线之后调整或移除了完整的使用统计链路。本仓库的目标是在继续维护 CLI 代理核心能力的同时，保留 `v6.9.49` 已具备的统计能力。

维护规则：

- `main` 是 CPA-Core-LTS 的唯一产品主线，不再单独维护一个“统计分支”。
- CPA-Core-LTS 通过操作者驱动的 protected full-sync merge PR 跟踪 `router-for-me/CLIProxyAPI`，通常由维护者要求 AI agent 执行同步。
- 必须保留使用统计采集、`internal/usage/`、`/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import` 和 `usage-statistics-enabled`。
- 普通上游改动默认吸收。若上游改动与 protected delta 冲突，应改写上游变更以保留 LTS 契约，而不是让上游覆盖或削弱统计功能。
- 禁止使用 GitHub Sync fork、文件级覆盖，或在 `main` 上直接 `git pull upstream main`。
- 本仓库不安排自动同步上游；这里沉淀的是可重复执行的人/AI 操作方法。
- 后续轻量化改造可以移除推广文案、无用功能和非 LTS 发布链路，但不能削弱统计契约或 `CPA-Panel-LTS` 兼容性。

本核心服务应与 `CPA-Panel-LTS` 配套使用，后者保留了与该统计接口匹配的管理面板统计页面。默认情况下，`/management.html` 会从 `CPA-Panel-LTS` 最新 release 中名为 `management.html` 的资产下载。

## 原项目概览

一个为 CLI 提供 OpenAI/Gemini/Claude/Codex/Grok 兼容 API 接口的代理服务器。

现已支持通过 OAuth 登录接入 OpenAI Codex（GPT 系列）和 Claude Code。

您可以使用本地或多账户的CLI方式，通过任何与 OpenAI（包括Responses）/Gemini/Claude 兼容的客户端和SDK进行访问。

## 功能特性

- 为 CLI 模型提供 OpenAI/Gemini/Claude/Codex/Grok 兼容的 API 端点
- 新增 OpenAI Codex（GPT 系列）支持（OAuth 登录）
- 新增 Claude Code 支持（OAuth 登录）
- 新增 Grok Build 支持（OAuth 登录）
- 支持流式、非流式响应，以及受支持场景下的 WebSocket 响应
- 函数调用/工具支持
- 多模态输入（文本、图片）
- 多账户支持与轮询负载均衡（Gemini、OpenAI、Claude、Grok）
- 简单的 CLI 身份验证流程（Gemini、OpenAI、Claude、Grok）
- 支持 Gemini AIStudio API 密钥
- 支持 AI Studio Build 多账户轮询
- 支持 Claude Code 多账户轮询
- 支持 OpenAI Codex 多账户轮询
- 支持 Grok Build 多账户轮询
- 通过配置接入上游 OpenAI 兼容提供商（例如 OpenRouter）
- 可复用的 Go SDK（见 `docs/sdk-usage_CN.md`）

## 新手入门

### 可选本地流控（Flow V3）

支持调用方、模型和单条上游 Auth 记录的联合并发/滑动窗口限制、有界排队与按需观察。执行、实时观察和资源采样默认关闭；新建空策略未写 version 时按 schema 3 解释，已有非空且未写 version 的策略继续保留旧语义并标记 legacy-policy，显式 version 1/2 也保留旧语义。不替代上游额度、Home 或完整 usage。配置保存失败保留最后有效策略，已有流按真实生产端结束释放。配套版本、迁移和回退见 [流控指南](docs/lts/flow-control.md)。

CLIProxyAPI 用户手册： [https://help.router-for.me/](https://help.router-for.me/cn/)

## 管理 API 文档

请参见 [MANAGEMENT_API_CN.md](https://help.router-for.me/cn/management/api)

## 使用量统计

上游 CLIProxyAPI v6.10.0 以后将内置使用量统计移出核心项目。CPA-Core-LTS 会继续保留内置 `/v0/management/usage` 统计和 export/import API，以兼容 CPA-Panel-LTS；同时也保留 Redis 兼容使用量队列，方便外部看板按需消费同一批事件。

### [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper)

独立的 CLIProxyAPI 使用量持久化与可视化服务，定期同步 CLIProxyAPI 数据，存储到 SQLite，提供聚合 API，并内置使用量分析与统计仪表盘。

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

面向 CLIProxyAPI 的自托管管理面板与可观测性平台，既可作为无需额外服务的轻量管理界面运行，也可通过完整 Manager Server 获得持久化分析和运维能力。统一管理 OAuth 凭证、AI 提供商、客户端 API 密钥、模型别名、插件和 CPA 配置。基于 SQLite 的请求历史支持实时监控、失败诊断，以及按模型、提供商、账号、密钥和渠道统计 Token、延迟与预估成本。支持自定义模型价格与一键同步，并提供 Codex 与 xAI 配额检查、重置信息、账号健康巡检及受控恢复操作。支持 Docker 和跨平台原生部署，数据保存在本地。

### [Oh-My-CPA](https://github.com/WizisCool/oh-my-cpa)

基于 Ant Design 的现代 CLIProxyAPI v8+ 管理面板，将 CPAMC 核心管理能力与 SQLite 持久化请求记录、用量分析整合在一起。覆盖 OAuth 账号、API 提供商、客户端密钥、配额、定价与 CPA 运维；逐请求追踪延迟、首 Token 延迟（TTFT）、Token 用量和成本，支持多维筛选、实时仪表盘与 Token 热力图。支持 OpenRouter 价格同步、自定义定价和逐请求价格快照，保持历史成本稳定；内置 Agent 与 MCP 工具，辅助用量分析和日常管理。

## SDK 文档

- 使用文档：[docs/sdk-usage_CN.md](docs/sdk-usage_CN.md)
- 高级（执行器与翻译器）：[docs/sdk-advanced_CN.md](docs/sdk-advanced_CN.md)
- 认证: [docs/sdk-access_CN.md](docs/sdk-access_CN.md)
- 凭据加载/更新: [docs/sdk-watcher_CN.md](docs/sdk-watcher_CN.md)
- 自定义 Provider 示例：`examples/custom-provider`

## 贡献

欢迎贡献！请随时提交 Pull Request。

1. Fork 仓库
2. 创建您的功能分支（`git checkout -b feature/amazing-feature`）
3. 提交您的更改（`git commit -m 'Add some amazing feature'`）
4. 推送到分支（`git push origin feature/amazing-feature`）
5. 打开 Pull Request

## 谁与我们在一起？

基于 CLIProxyAPI 构建的项目生态列表由上游维护：<https://github.com/router-for-me/CLIProxyAPI#who-is-with-us>

## 更多选择

以下项目是 CLIProxyAPI 的移植版或受其启发：

### [9Router](https://github.com/decolua/9router)

基于 Next.js 的实现，灵感来自 CLIProxyAPI，易于安装使用；自研格式转换（OpenAI/Claude/Gemini/Ollama）、组合系统与自动回退、多账户管理（指数退避）、Next.js Web 控制台，并支持 Cursor、Claude Code、Cline、RooCode 等 CLI 工具，无需 API 密钥。

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

代码不止，创新不停。智能路由至免费及低成本 AI 模型，并支持自动故障转移。

OmniRoute 是一个面向多供应商大语言模型的 AI 网关：它提供兼容 OpenAI 的端点，具备智能路由、负载均衡、重试及回退机制。通过添加策略、速率限制、缓存和可观测性，确保推理过程既可靠又具备成本意识。

### [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel)

一个公开的 CLIProxyAPI 兼容二开版本和配套管理面板，尽量保持与上游一致的使用方式，同时恢复内置使用量统计，并补充缓存命中率、首字响应时间、TPS 记录和面向 Docker 自托管的安装说明。

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

这是一个使用 Tauri 2 + Vue 3 构建的工具，用于管理多个 OpenAI Codex 桌面账户。它可以在已保存的 ChatGPT/Codex 认证配置之间切换，实时查看 5 小时和每周配额使用情况，验证 token 健康状态，查看当前账户详情，并在无需手动复制的情况下导入或保存 auth.json 文件。

### [Alex](https://github.com/madhavajay/alex)

受 CLIProxyAPI 启发、带可选 UI 的本地 Rust LLM 代理。它可将编码 Agent 路由到多个提供商，支持本地链路追踪、可脚本化中间件、多订阅聚合、故障转移，以及通过即时通讯完成重新认证。
### [cliproxy-rs](https://github.com/vayungodara/cliproxy-rs)

CLIProxyAPI 的 Rust 移植版，读取相同的 `config.yaml` 和认证文件，提供相同的路由和 v8 管理 API，可以在两者之间双向切换。单个二进制文件内置管理面板（按账户显示 5 小时和每周配额、账户登录、客户端配置指南），并新增可选的 `soonest-reset` 路由策略，优先使用每周额度最先重置的账户。

> [!NOTE]  
> 如果你开发了 CLIProxyAPI 的移植或衍生项目，请提交 PR 将其添加到此列表中。

## 许可证

此项目根据 MIT 许可证授权 - 有关详细信息，请参阅 [LICENSE](LICENSE) 文件。

## 写给所有中国网友的

QQ 群：1081218164

或

Telegram 群：https://t.me/CLIProxyAPI
