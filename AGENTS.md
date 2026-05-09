# AGENTS.md

本仓库是 `CPA-Core-LTS`，不是上游 `router-for-me/CLIProxyAPI` 的普通同步 fork。

## 项目定位

- LTS 仓库：`https://github.com/BlueSkyXN/CPA-Core-LTS`
- 上游来源：`https://github.com/router-for-me/CLIProxyAPI`
- 基线版本：`v6.9.49`
- 基线提交：`b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- 配套面板：`https://github.com/BlueSkyXN/CPA-Panel-LTS`

`main` 是唯一的 LTS 主线。后续维护应在 `main` 上推进，不要为“保留统计”再创建长期分支。

## LTS 目标

本项目要在跟进有价值的新功能和安全修复时，保留上游后续版本移除或重构掉的完整使用统计能力。

必须保留的统计契约：

- `usage-statistics-enabled`
- `internal/usage/`
- `/v0/management/usage`
- `/v0/management/usage/export`
- `/v0/management/usage/import`
- API key、auth file、model、token、latency、success/failure 等统计字段的兼容性
- 与 `CPA-Panel-LTS` 的 `/usage` 页面、provider status bar、request events table 兼容

禁止把上游后续“只保留 recent requests / api-key usage，移除完整 usage statistics”的方向直接合入。

## 关联项目

`CPA-Core-LTS` 是服务端核心，负责代理、鉴权、管理 API 和统计数据采集。

`CPA-Panel-LTS` 是前端管理面板，负责读取 `CPA-Core-LTS` 的 Management API，并展示配置、凭据、日志、配额和完整使用统计。

这两个项目应作为一组 LTS 分发维护：

- Core 负责稳定输出统计接口。
- Panel 负责保留和演进统计 UI。
- Core 改动统计数据结构时，必须同步检查 Panel。
- Panel 改动统计页面时，必须确认 Core 仍提供对应接口。

## 跟进上游规则

- 不要使用 GitHub 的 `Sync fork` 盲同步。
- 跟进上游时先读 diff，再选择 cherry-pick、手工移植或放弃。
- 合入前检查是否触碰 `internal/usage/`、Management API usage endpoints、config schema、auth/API key usage 结构。
- 如果上游改动会破坏统计，优先保留 LTS 统计实现，再单独吸收其他无冲突部分。
- 后续轻量化改造可以移除广告、赞助描述、无用文档、非目标 provider 或发布链路，但不要删除仍被统计、管理面板或部署流程依赖的代码。

## 常用命令

```bash
gofmt -w .
go build -o cli-proxy-api ./cmd/server
go run ./cmd/server
go test ./...
go test -v -run TestName ./path/to/pkg
go build -o test-output ./cmd/server && rm test-output
```

常用启动参数：

```bash
--config <path>
--tui
--standalone
--local-model
--no-browser
--oauth-callback-port <port>
```

## 主要目录

- `cmd/server/`：服务端入口
- `internal/api/`：Gin HTTP API、middleware、management routes
- `internal/api/modules/amp/`：Amp 路由与管理代理
- `internal/runtime/executor/`：各 provider executor
- `internal/translator/`：协议转换
- `internal/registry/`：模型注册与远程更新
- `internal/watcher/`：配置热重载与凭据合成
- `internal/usage/`：LTS 必须保留的使用统计实现
- `internal/tui/`：终端 UI
- `sdk/cliproxy/`：可嵌入 SDK
- `test/`：跨模块集成测试

## 修改要求

- 默认用中文沟通；代码、命令和专有名词保留英文。
- 修改 Go 代码后运行 `gofmt`。
- 修改统计、管理 API、配置 schema、auth/API key 结构时，要补充或运行相关测试。
- 不要在日志里泄露 token、secret、credential 内容。
- 不要用 `log.Fatal` / `log.Fatalf` 终止进程；优先返回错误并用 logrus 记录上下文。
- 网络超时策略沿用现有设计：凭据获取阶段可以有 timeout；上游连接建立后的流式行为不要随意增加 timeout。
