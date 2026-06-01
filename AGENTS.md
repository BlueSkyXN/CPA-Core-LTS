# CPA-Core-LTS agent instructions

## Purpose

`CPA-Core-LTS` 是 `router-for-me/CLIProxyAPI` 的长期维护分支，目标是跟踪 CLIProxyAPI latest，同时稳定保留 `v6.9.49` 基线已有的完整 usage statistics 能力、`CPA-Panel-LTS` 兼容性，以及本仓库 downstream 专属修改。

本仓库不是普通同步 fork。任何维护动作都必须先判断是否影响 LTS 统计契约、Management API、auth/config 兼容性和配套面板。

## Codex startup behavior

- Codex 通常从仓库根目录启动；本文件是启动期主规则和目录 router。
- 子目录 `AGENTS.md` 是按需导航卡片。从根目录启动时，它们通常不会自动进入上下文。
- 修改带有本地 `AGENTS.md` 的目录前，先运行 `cat <path>/AGENTS.md` 读取对应卡片。
- 如果目标路径上有多层 `AGENTS.md`，按从浅到深的顺序读取，冲突时更深层规则优先。
- 如果用户只问问题或做只读审计，不需要为了读取本地卡片而修改文件。
- 本仓库存在 `.github/workflows/agents-md-guard.yml`，会限制修改 `AGENTS.md` 的 PR；外部 PR 触碰 `AGENTS.md` 会被关闭，OWNER 可直接放行，MEMBER/COLLABORATOR 需要 `allow-agents-md-update` label。除非用户明确要求维护 agent 指令，不要把 AGENTS 改动混入产品代码 PR。

## LTS contract

LTS 仓库信息：

- LTS 仓库：`https://github.com/BlueSkyXN/CPA-Core-LTS`
- 上游来源：`https://github.com/router-for-me/CLIProxyAPI`
- 基线版本：`v6.9.49`
- 基线提交：`b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- 配套面板：`https://github.com/BlueSkyXN/CPA-Panel-LTS`
- Go module path 仍是 `github.com/router-for-me/CLIProxyAPI/v6`；不要因为 LTS 仓库名而随意改 import path。

`main` 是唯一的 LTS 主线。正常维护不要为“保留统计”再创建长期分支。

必须保留的统计契约：

- `usage-statistics-enabled`
- `internal/usage/`
- `/v0/management/usage`
- `/v0/management/usage/export`
- `/v0/management/usage/import`
- API key、auth file、model、token、latency、success/failure 等统计字段的兼容性
- 与 `CPA-Panel-LTS` 的 `/usage` 页面、provider status bar、request events table 兼容

正常维护模式是 manual / AI-operated protected full-sync。普通 upstream 改动默认吸收；如果 upstream 改动会破坏完整 usage statistics、`CPA-Panel-LTS` 兼容性或 downstream customizations，必须保留或重放 LTS delta 后再合入。禁止让 upstream 的 recent requests / api-key usage 简化方向移除或降级本仓库内置完整统计。

`CPA-Core-LTS` 和 `CPA-Panel-LTS` 作为一组 LTS 分发维护：

- Core 负责代理、鉴权、管理 API、统计采集和统计接口。
- Panel 负责读取 Management API，并展示配置、凭据、日志、配额和完整使用统计。
- Core 默认应从 `BlueSkyXN/CPA-Panel-LTS` latest release 下载 `management.html`。
- Core 改动统计数据结构时，必须同步检查 Panel 是否仍能展示。
- Panel 改动统计页面时，必须确认 Core 仍提供对应接口。

## Protected full-sync workflow

本仓库使用人工 / AI 操作的 protected full-sync，不安排自动同步任务。`upstream/main` 只是只读同步坐标，不是长期产品分支。

同步流程：

1. 从最新 `origin/main` 创建隔离 worktree / 分支，例如 `codex/sync-upstream-stage-N`。
2. 运行 `git fetch origin --prune` 和 `git fetch upstream --prune`。
3. 对大范围同步优先按 upstream first-parent SHA 分段；不要按文件分段，也不要把普通维护拆成逐个 upstream PR cherry-pick。
4. 使用 `git merge --no-ff --log <UPSTREAM_STAGE_SHA>` 合入该段 upstream 历史。
5. provider、model、translator、runtime、security、crash、stream 等普通 upstream 修复默认吸收。
6. 冲突触碰 protected deltas 时，保留或重放 CPA-Core-LTS 行为，再适配 upstream 改动。
7. sync PR body 必须写明 upstream from/to SHA、stage 编号、冲突文件列表、protected delta 处理、contract/build/test 状态，以及覆盖了哪些旧 upstream-port PR。
8. sync PR 合入 `main` 必须使用 Create a merge commit；禁止 squash 或 rebase sync PR。

Protected full-sync 的硬门禁：

- Go module path 仍保持 `github.com/router-for-me/CLIProxyAPI/v6`，除非用户明确批准 breaking module-path 策略。
- `internal/usage/`、`usage-statistics-enabled`、`/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import` 必须保留。
- usage record schema 必须保留 API key、auth file、model、token、latency、success/failure 等统计字段兼容性。
- Management usage response shape 必须保持 `CPA-Panel-LTS` 兼容。
- config schema 接收 upstream 新项时，必须保留旧配置读取和热重载兼容。
- panel release source 保持 `BlueSkyXN/CPA-Panel-LTS`，除非用户明确要求改变并同步验证 Panel 兼容性。
- 如果 upstream 删除内置 usage seam 或转向外置 usage service，可以适配新架构，但不能降级本仓库内置完整统计。

## Directory map

| Path | Responsibility | Local AGENTS.md | Read when |
|---|---|---:|---|
| `.github/` | GitHub Actions、PR guard、release workflow | No | 修改 workflow、权限、release、PR guard 前直接读目标 workflow |
| `.codex/` | 本地 Codex 配置/临时上下文 | No | 仅当用户明确要求维护本地 Codex 资产 |
| `.playwright-mcp/` | 本地 Playwright MCP 运行残留/配置 | No | 默认不要纳入产品改动 |
| `assets/` | README 和展示图片资产 | No | 修改 README 图片引用或赞助资产前 |
| `auths/` | auth 目录占位；运行时挂载真实凭据目录 | No | 默认不要提交真实 token/auth file |
| `cmd/server/` | 服务端入口、CLI flags、TUI/standalone 启动 | No | 修改启动参数、登录流程入口、build metadata 前 |
| `cmd/fetch_antigravity_models/` | Antigravity model catalog 辅助拉取命令 | No | 修改模型拉取辅助工具前 |
| `config.example.yaml` | 用户配置示例和 schema 可见面 | No | 新增/改名/删除 config key 前，同时读 `internal/config/AGENTS.md` |
| `Dockerfile` / `docker-compose.yml` / `docker-build.*` | 容器构建和本地 Docker 运行 | No | 修改镜像、端口、volume、usage backup 脚本前 |
| `.goreleaser.yml` | tag release artifact 配置 | No | 修改 release binary/archive/checksum 前 |
| `docs/` | SDK 文档，含中英文版本 | No | 修改 SDK 行为或公开接口文档前 |
| `examples/` | SDK/translator/http-request 示例 | No | 修改公开示例或 API 使用方式前 |
| `internal/access/` | API key/access manager 适配 | No | 修改鉴权判定或 auth manager 集成前，同时读 `internal/auth/AGENTS.md` |
| `internal/api/` | Gin server、middleware、Management API、Amp module | Yes | 修改 routes、middleware、Management API、Amp endpoints、HTTP/WebSocket 协议前 |
| `internal/auth/` | OAuth/device auth、token storage、provider credential helpers | Yes | 修改 token、OAuth callback、auth file、credential 保存/刷新前 |
| `internal/cache/` | Signature/cache helpers | No | 修改 Antigravity/Claude signature cache 前，必要时也读 translator/executor 卡片 |
| `internal/cmd/` | CLI login/import command helpers | No | 修改登录命令、vertex import、auth manager CLI 前 |
| `internal/config/` | YAML config model、默认值、sanitize、compat helpers | Yes | 新增/迁移/删除 config key 或改变默认值前 |
| `internal/logging/` | request/global logging、log rotation、request metadata | No | 修改日志格式、落盘策略、request metadata 前 |
| `internal/managementasset/` | `management.html` 下载和更新 | No | 修改面板下载源、缓存路径、auto-update 行为前 |
| `internal/redisqueue/` | Redis protocol/queue plugin | No | 修改 Redis queue wire protocol 或 auth parsing 前 |
| `internal/registry/` | model registry、remote model updates、embedded models | No | 修改模型注册、`internal/registry/models/models.json` 或远程刷新策略前 |
| `internal/runtime/executor/` | Provider executors、upstream calls、stream/WebSocket handling | Yes | 修改 Codex/Claude/Gemini/Kimi/Antigravity executor、retry、streaming、timeout 前 |
| `internal/translator/` | Provider protocol translators and token-count adapters | Yes | 修改 request/response translation、tool/function call、usage metadata、signature handling 前 |
| `internal/tui/` | Terminal UI and TUI client | No | 修改 TUI views 或 management client calls 前 |
| `internal/usage/` | LTS 完整 usage statistics 聚合和导入导出数据结构 | Yes | 任何统计字段、聚合、import/export、success/failure 语义改动前 |
| `internal/watcher/` | Config/auth file watcher and auth synthesis | No | 修改热重载、auth file 合成、config diff 前，同时读 config/auth 卡片 |
| `sdk/` | Public embeddable SDK packages | No | 修改非 `sdk/cliproxy` public package 前，检查 docs/examples |
| `sdk/cliproxy/` | 可嵌入服务 SDK、auth conductor、usage plugin API | Yes | 修改 public SDK type、builder、service、auth scheduler、usage record 前 |
| `test/` | 跨模块集成和协议 sentinel 测试 | No | 修改跨模块行为或新增回归测试前，跟随被测目录规则 |

## On-demand cat protocol

Before editing files under a directory with `Local AGENTS.md = Yes`, read the local card:

```bash
cat internal/usage/AGENTS.md
cat internal/api/AGENTS.md
cat internal/config/AGENTS.md
cat internal/auth/AGENTS.md
cat internal/runtime/executor/AGENTS.md
cat internal/translator/AGENTS.md
cat sdk/cliproxy/AGENTS.md
```

Only read the cards that apply to the target change. If a change spans multiple domains, read all relevant cards before editing and validate the cross-domain contract explicitly in the final report.

## Commands

These commands are confirmed from the existing AGENTS file, Go tooling, CI workflows, Docker files, `.goreleaser.yml`, or source flags.

| Command | Purpose | Scope | Sandbox notes |
|---|---|---|---|
| `gofmt -w .` | Format all Go files | repo | OK; run after Go edits, or scope to touched files when possible |
| `go test ./...` | Run all Go tests | repo | Usually local; first run may need module/toolchain download if cache is missing |
| `go test -v -run TestName ./path/to/pkg` | Run targeted Go test | package | OK; replace `TestName` and package path with real target |
| `go build -o test-output ./cmd/server && rm -f test-output` | CI-style server build smoke | repo | OK after deps are available; matches `.github/workflows/pr-test-build.yml` build step |
| `go build -o cli-proxy-api ./cmd/server` | Build local server binary | repo | Writes `cli-proxy-api`; remove it if only used as a smoke artifact |
| `go run ./cmd/server --config <path> --no-browser` | Run server locally with explicit config | repo | Starts local service and may bind ports; needs valid config for meaningful smoke |
| `go run ./cmd/server --tui --standalone --no-browser` | Run embedded server with TUI | repo | Interactive terminal flow; not default automated validation |
| `go run ./cmd/fetch_antigravity_models --auths-dir auths --output antigravity_models.json` | Fetch Antigravity model data | helper | Requires auth files and network; do not run by default |
| `docker compose build` | Build container image | repo | Requires Docker daemon; not default sandbox validation |
| `docker compose up -d --remove-orphans --pull never` | Start local compose stack from local image | repo | Requires Docker daemon and local config/auth/log volume paths |
| `./docker-build.sh` | Interactive Docker build/run helper | repo | Interactive, Docker required; `--with-usage` calls Management API and stores temp secret under `temp/stats/` |
| `goreleaser release --clean --skip=validate` | Release build action used by workflow | repo | Requires release context, tag, network, and `GITHUB_TOKEN`; do not run unless explicitly asked |

Workflow-specific note: `.github/workflows/pr-test-build.yml` refreshes `internal/registry/models/models.json` from `https://github.com/router-for-me/models.git` before building. Local validation may skip that network refresh unless the change is model catalog related.

## Global rules

- 默认使用中文沟通；代码、命令、flag、API path、config key、package name 保留英文。
- 修改 Go 代码后运行 `gofmt`；不要用格式化改动掩盖行为变更。
- 先查询真实接口、配置、workflow、测试和源码实现，再下结论；不要臆造 API、flag、config key 或命令。
- 复用现有 helper、interfaces、translator registry、auth manager、config parser 和 SDK types；不要为绕过局部问题创建平行接口。
- 不要用 `log.Fatal` / `log.Fatalf` 终止服务进程；优先返回 error，并用 logrus 记录必要上下文。
- 不要在日志、error、test snapshot 或 PR 文案中泄露 token、secret、management key、refresh token、API key、OAuth code、auth file 原文。
- 网络 timeout 策略沿用现有设计：凭据获取阶段可以有 timeout；上游连接建立后的 streaming 行为不要随意增加 timeout。
- Management API 兼容性按外部 contract 看待。变更 response shape、status code、route、method 或 auth header 前，检查 Panel、TUI、SDK、tests。
- Config 兼容性按用户数据迁移看待。新增 key 要考虑 `config.example.yaml`、default、YAML/JSON tag、sanitize、hot reload、Management API 和旧配置读取。
- Auth file 和 API key 结构影响 watcher、management UI、TUI、SDK auth conductor 和 usage attribution；不要只改一个入口。
- Public SDK 类型和方法是外部契约；改 `sdk/` 前检查 docs、examples、tests，并避免无必要的 breaking change。
- 上游同步默认走经过审核的 protected full-sync merge PR。任何触碰 `internal/usage/`、Management usage endpoints、config schema、auth/API key usage 结构或 `CPA-Panel-LTS` response shape 的冲突，必须按 protected delta 审查并保留 LTS 契约。

## Do not

- 不要使用 GitHub `Sync fork` 盲同步上游。
- 不要在 `main` 上直接 `git pull upstream main`。
- 不要用 `git checkout upstream/main -- .` 或文件级覆盖作为同步方式。
- 不要 squash 或 rebase upstream sync PR。
- 不要移除或降级 `internal/usage/`、`usage-statistics-enabled`、`/v0/management/usage*`。
- 不要把 Core 默认面板源改回上游或其他仓库，除非用户明确要求并同步检查 Panel 兼容性。
- 不要提交真实 `auths/` 内容、`.env`、`config.yaml`、token、secret、management key 或本地日志。
- 不要把 `AGENTS.md` 改动混入普通产品 PR；本仓库 workflow 会关闭触碰 AGENTS 的 PR。
- 不要把 CI/release/Docker 变更当作无风险文本修改；这些文件影响分发和运行路径。
- 不要在没有对应测试或明确说明缺口的情况下声称已验证统计、auth、translator、executor 或 Management API 行为。
- 不要执行发布、推送、tag、Docker destructive cleanup、`goreleaser release`、`terraform`、sudo 或外部平台变更，除非用户明确要求。

## Validation

默认验证按改动风险选择最小充分集合：

1. Go 源码改动：先 `gofmt`，再运行相关 package 的 `go test`。
2. 通用构建风险：运行 `go build -o test-output ./cmd/server && rm -f test-output`。
3. 跨模块或共享契约改动：优先运行 `go test ./...`；如果耗时或环境阻断，在最终汇报中明确未跑原因。
4. `internal/usage/` 或 Management usage endpoints：至少运行 `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`，必要时补 `go test ./...`。
5. `internal/api/` routes/middleware/Amp：运行相关 `internal/api`、`internal/api/handlers/management`、`internal/api/modules/amp`、`test` 包测试。
6. `internal/config/`：运行 `go test ./internal/config ./internal/watcher/diff ./internal/api/handlers/management` 中相关测试。
7. `internal/auth/` 或 `sdk/cliproxy/auth/`：运行 provider/auth package tests，并确认日志不输出 secret。
8. `internal/runtime/executor/`：运行对应 executor package tests；stream/WebSocket/retry 改动要覆盖 streaming failure path。
9. `internal/translator/`：运行对应 translator package tests和 `test/*translation*` sentinel。
10. Docker/release 改动：本地只能做静态/构建验证；需要 Docker、network、tag 或 token 的步骤必须标注。

If validation cannot be run, say exactly which command was skipped and why. Do not present unrun checks as passing.

## Notes for future agents

- 当前工作区曾出现未跟踪 `.playwright-mcp/`，它不是产品源码；除非任务明确相关，忽略即可。
- README 有英文、中文、日文版本；用户面向文档改动要检查多语言是否需要同步。
- `docker-build.sh --with-usage` 会通过 Management API export/import 统计并在 `temp/stats/.api_secret` 保存临时管理密钥；不要把该目录纳入 Git。
- `internal/registry/models/models.json` 由 workflow 远程刷新；离线环境下不要假设模型目录一定最新。
