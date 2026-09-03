# CPA-Core-LTS agent instructions

## Purpose

`CPA-Core-LTS` 是 `router-for-me/CLIProxyAPI` 的长期维护分支：跟踪 upstream latest，同时稳定保留 `v6.9.49` 基线已有的完整 usage statistics、`CPA-Panel-LTS` 兼容性，以及本仓库 downstream 专属修改。

本仓库不是普通同步 fork。任何维护动作都必须先判断是否影响 LTS 统计契约、Management API、auth/config 兼容性、runtime usage attribution 和配套面板。

## Codex startup behavior

- Codex 通常从仓库根目录启动；本文件是启动期主规则和目录 router。
- 子目录 `AGENTS.md` 是按需 navigation card。从根目录启动时，它们通常不会自动进入上下文。
- 修改带有本地 `AGENTS.md` 的目录前，先运行 `cat <path>/AGENTS.md` 读取对应卡片。
- 如果目标路径上有多层 `AGENTS.md`，按从浅到深的顺序读取；冲突时更深层规则优先。
- 当前仓库没有 `AGENTS.override.md`。不要为绕过现有层级创建 override；如果未来出现 override，先确认它会替换而不是补充同目录 `AGENTS.md`。
- `.github/workflows/agents-md-guard.yml` 会限制修改 `AGENTS.md` 的 PR：OWNER 可直接放行，MEMBER/COLLABORATOR 需要 `allow-agents-md-update` label，外部 PR 触碰 `AGENTS.md` 会被关闭。除非用户明确要求维护 agent 指令，不要把 AGENTS 改动混入产品代码 PR。

## LTS contract

- LTS 仓库：`https://github.com/BlueSkyXN/CPA-Core-LTS`
- 上游来源：`https://github.com/router-for-me/CLIProxyAPI`
- 基线版本 / 提交：`v6.9.49` / `b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- 配套面板：`https://github.com/BlueSkyXN/CPA-Panel-LTS`
- Go module path 当前跟随 upstream major path：`github.com/router-for-me/CLIProxyAPI/v7`；不要因为 LTS 仓库名而随意改 import path。

必须保留：`usage-statistics-enabled`、`internal/usage/`、`/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import`、`/v0/management/usage-queue`、`/v0/management/usage-statistics-enabled`，以及 API key、auth file、model、token、latency、success/failure、auth index 等统计字段。Core 默认从 `BlueSkyXN/CPA-Panel-LTS` latest release 下载 `management.html`，并保持 Panel `/usage` 页面、provider status bar、request events table 兼容。

## Protected full-sync workflow

本仓库使用人工 / AI 操作的 protected full-sync，不安排自动同步任务。`main` 是唯一 LTS 主线；`upstream/main` 只是只读同步坐标。真实 upstream sync 前必须阅读 `docs/lts/sync-runbook.md`。

同步原则：从最新 `origin/main` 创建隔离 worktree / 分支；fetch 后按 upstream first-parent SHA 分段；使用 `git merge --no-ff --log <UPSTREAM_STAGE_SHA>` 合入上游历史；普通 provider/model/translator/runtime/security/crash/stream 修复默认吸收；冲突触碰 protected deltas 时保留或重放 CPA-Core-LTS 行为。sync PR body 必须写 upstream from/to SHA、stage、冲突文件、protected delta review、contract/build/test 状态和覆盖的旧 upstream-port PR。合入 `main` 必须使用 Create a merge commit，禁止 squash 或 rebase sync PR。

如果 upstream diff 触碰 request lifecycle、auth identity、model resolution、token accounting、logging metadata、Management usage response shape、config hot reload、panel release source、plugin runtime 或 usage queue，即使没有文本冲突，也必须写 `Protected delta review`。

## Source lookup priority

源码查找和 GitHub 远程状态核验是两类操作，不要混用：

- **当前项目代码**：先读取当前 worktree 以及本地 `main` / `origin/main`；优先使用 `rg`、`git show`、`git diff`、`git log`。不要仅因任务出现“当前”“最新”或“原作者”就调用 `gh api`。
- **原作者 upstream 代码**：先读取本地 `upstream/main` 及其他 remote-tracking refs。只有本地 ref 缺失或明显过旧，且确实需要最新 upstream 源码时，才说明原因并执行只读目的的 `git fetch upstream`（需要网络，会更新本地 remote-tracking refs），随后核对 fetch 前后的 SHA。
- **GitHub 远程状态**：PR、CI、Release、Issue、远程分支状态等元数据才使用 `gh` / `gh api`；本地 refs 尚未包含的源码优先通过上面的 `git fetch upstream` 获取，不用 `gh api` 代替 Git 历史。调用 `gh` 时必须显式指定目标仓库（LTS 使用 `-R BlueSkyXN/CPA-Core-LTS`，upstream 使用 `-R router-for-me/CLIProxyAPI`）。
- **部署和运行态**：部署版本、runtime、UAT 和外部服务状态必须单独核验，不能用本地源码、GitHub 源码或 PR 状态替代。
- 如果确实需要远程查询，先写明本地检查过的 ref、远程查询要解决的问题及预期证据；不得用远程内容覆盖 dirty worktree 或替代本地未提交改动。

## Directory map

| Path | Responsibility | Local AGENTS.md | Read when |
|---|---|---:|---|
| `.github/` | Actions、PR guard、release、path/contract checks | Yes | 修改 workflow、权限、release、PR guard、CI gate 前 |
| `.codex/` | 本地 Codex 配置/临时上下文 | No | 仅当用户明确要求维护本地 Codex 资产 |
| `.gocache/` | 本地 Go build/test cache | No | 默认忽略，不纳入产品改动或验收结论 |
| `.playwright-mcp/` | 本地 Playwright MCP 运行残留/配置 | No | 默认忽略，不纳入产品改动 |
| `auths/` | auth 目录占位；运行时挂载真实凭据目录 | No | 默认不要提交真实 token/auth file；形状变更读 `internal/auth/` 和 `internal/watcher/` |
| `cmd/` | Server and catalog/validation command entrypoints | No | 修改具体命令前按下方子目录/关联 domain 读取卡片 |
| `cmd/server/` | 服务端入口、CLI flags、TUI/standalone/home 启动 | Yes | 修改启动参数、登录流程入口、build metadata、server mode 前 |
| `cmd/fetch_antigravity_models/` | Antigravity model catalog 辅助拉取命令 | No | 修改模型拉取辅助工具前，同时读 `internal/auth/`、`internal/registry/` 卡片 |
| `cmd/fetch_codex_models/` | Codex model catalog 辅助拉取命令 | No | 修改 Codex 模型拉取、token refresh、client_version 或输出格式前 |
| `cmd/validate_codex_models/` | Codex client model catalog LTS compatibility validator | No | 修改 validator contract 或 catalog schema 前，同时读 `internal/registry/` 与 `docs/lts/` 卡片 |
| `config.example.yaml` | 用户配置示例和 schema 可见面 | No | 新增/改名/删除 config key 前，同时读 `internal/config/AGENTS.md` |
| `Dockerfile` / `docker-compose*.yml` / `docker-build.*` | 容器构建和本地 Docker 运行 | No | 修改镜像、端口、volume、usage backup 脚本前 |
| `docs/` | SDK 文档、多语言 README 相关资料 | No | 修改 SDK 行为或公开接口文档前 |
| `docs/lts/` | LTS contract registry、protected delta、sync runbook、Codex abnormal retry 指南 | Yes | 修改 protected contract、sync runbook、guard marker、Codex abnormal retry 配置指南前 |
| `examples/` | SDK/translator/plugin/http-request 示例 | No | 修改公开示例或 API 使用方式前，必要时检查对应 SDK 卡片 |
| `internal/` | Core server implementation and downstream compatibility seams | No | 修改具体模块前按下方目录地图读取最近的本地卡片 |
| `internal/access/` | API key/access manager 适配 | No | 修改鉴权判定或 auth manager 集成前，同时读 `internal/auth/AGENTS.md` |
| `internal/api/` | Gin server、middleware、Management API、Amp/WebSocket endpoints | Yes | 修改 routes、middleware、Management API、Amp endpoints、HTTP/WebSocket 协议前 |
| `internal/api/handlers/management/` | Management API handlers、config/auth/usage/plugin 管理面 | Yes | 修改 Management response shape、持久化、OAuth session、auth CRUD、usage import/export 前；先读父卡再读本卡 |
| `internal/auth/` | OAuth/device auth、token storage、credential helpers | Yes | 修改 token、OAuth callback、auth file、credential 保存/刷新前 |
| `internal/cache/` | Signature/reasoning replay cache、Home KV fallback 边界 | Yes | 修改 replay scope、signature cache、CAS/TTL、local/Home cache 语义前 |
| `internal/client/codex/` | Codex client models、Multi-Agent v2 adaptation、Realtime/Live bridge | Yes | 修改 Codex client catalog、official-client rewrite、Realtime secret/WebRTC/session 行为前 |
| `internal/cmd/` | CLI login/import command helpers | No | 修改登录命令、vertex import、auth manager CLI 前 |
| `internal/config/` | YAML config model、defaults、sanitize、compat helpers | Yes | 新增/迁移/删除 config key 或改变默认值前 |
| `internal/codexmetadata/` | Codex outbound client metadata normalization/privacy policy | Yes | 修改 `client_metadata`、workspace policy、turn metadata header 或 repair/strict/drop 语义前 |
| `internal/home/` | Home control-plane client、dispatch/KV、membership/concurrency lifecycle | Yes | 修改 Home protocol、cluster failover、dispatch ambiguity、KV 或 release/in-flight wire shape 前 |
| `internal/homeplugins/` | Home/plugin sync manifest、artifact apply and status report boundary | Yes | 修改 Home plugin inventory、artifact validation/apply、task/status wire contract 前 |
| `internal/logging/` | request/global logging、request metadata、log rotation | Yes | 修改日志格式、落盘策略、request metadata、redaction 前 |
| `internal/managementasset/` | `management.html` 下载和更新 | Yes | 修改面板下载源、缓存路径、auto-update 行为前 |
| `internal/pluginhost/` | Dynamic plugin ABI host、callbacks、scheduler、management routes | Yes | 修改 plugin lifecycle、callbacks、ABI/RPC、scheduler、stream bridge 前 |
| `internal/pluginstore/` | Plugin registry lookup、GitHub release install、checksum/install logic | Yes | 修改 plugin store registry、download、checksum、install/update 前 |
| `internal/redisqueue/` | Redis-compatible usage queue plugin and wire payload | Yes | 修改 `/usage-queue`、usage event schema、retention/toggle 语义前 |
| `internal/registry/` | model registry、remote model updates、embedded model catalogs、quota state | Yes | 修改模型注册、catalog JSON、quota routing state、remote refresh 前 |
| `internal/runtime/executor/` | Provider executors、upstream calls、stream/WebSocket handling | Yes | 修改 Codex/Claude/Gemini/Kimi/Antigravity executor、retry、streaming、timeout 前 |
| `internal/signature/` | Provider thinking signature/encrypted-content validation and sanitization | Yes | 修改 signature 识别、跨 provider 兼容、sanitize 或 replay acceptance 前 |
| `internal/store/` | Git/Postgres/object-store config/auth persistence and secure local rendering | Yes | 修改 remote store、auth path containment、atomic write、Windows/POSIX file safety 前 |
| `internal/thinking/` | Unified reasoning/thinking config、summary intent、provider appliers | Yes | 修改 suffix、level/budget、summary、provider apply 或 plugin thinking provider 前 |
| `internal/translator/` | Provider protocol translators and token-count adapters | Yes | 修改 request/response translation、tool call、usage metadata、signature handling 前 |
| `internal/tui/` | Terminal UI and TUI management client | No | 修改 TUI views 或 management client calls 前，检查 affected API card |
| `internal/usage/` | LTS 完整 usage statistics 聚合和导入导出结构 | Yes | 任何统计字段、聚合、import/export、success/failure 语义改动前 |
| `internal/watcher/` | Config/auth file watcher、diff、auth synthesis、hot reload | Yes | 修改热重载、auth file 合成、config diff、plugin auth parsing 前 |
| `internal/wsrelay/` | WebSocket relay session/message plumbing | No | 修改 relay wire behavior 前，同时读 `internal/api/` 和 executor card |
| `local/` | 本地审计/工作记录 | No | 默认不提交；只在用户明确要求整理本地材料时处理 |
| `scripts/` | repo helper scripts | No | 修改 LTS guard 或 automation 前读目标脚本和 `docs/lts/AGENTS.md` |
| `sdk/` | Public embeddable SDK packages | No | 修改 public package 前检查 docs、examples、tests |
| `sdk/access/` | Public inbound request authentication provider chain | Yes | 修改 `Provider`、auth error semantics、registry order/exclusive mode、principal metadata 前 |
| `sdk/api/` | Public protocol handlers、stream lifecycle、routing/interceptor bridge | Yes | 修改 handler API、SSE/WebSocket、headers/errors、request lifecycle 或 plugin interception 前 |
| `sdk/auth/` | Public OAuth authenticator manager and file token store | Yes | 修改 exported authenticator、login options、token-store persistence、plugin auth parser 前 |
| `sdk/cliproxy/` | 可嵌入服务 SDK、auth conductor、usage plugin API | Yes | 修改 public SDK type、builder、service、auth scheduler、usage record 前 |
| `sdk/cliproxy/auth/` | Runtime auth conductor、selection、cooldown、retry、Home dispatch | Yes | 修改 auth selection/result、session affinity、cooldown、model fallback、Home execution 前；先读父卡再读本卡 |
| `sdk/pluginabi/` | Dynamic plugin ABI method/schema constants | Yes | 修改 plugin ABI schema version、method names、RPC envelope 前 |
| `sdk/pluginapi/` | Public plugin API types and capability contracts | Yes | 修改 plugin capability interfaces、request/response structs、public metadata 前 |
| `sdk/pluginstore/` | Public plugin registry/install/auth facade | Yes | 修改 exported pluginstore aliases、resolved auth、manifest/install helper 或 compatibility surface 前 |
| `sdk/translator/` | Public protocol translation registry and plugin hooks | Yes | 修改 exported formats/transforms、fallback、summary preservation、hook ordering 或 byte ownership 前 |
| `test/` | 跨模块集成和协议 sentinel 测试 | No | 修改跨模块行为或新增回归测试前，跟随被测目录规则 |
| `tmp/` | 本地临时运行输出 | No | 默认忽略，不作为源码、持久化或验收证据 |

## On-demand cat protocol

Before editing files under a directory with `Local AGENTS.md = Yes`, read the local card with `cat <path>/AGENTS.md`. If a change spans multiple domains, read all relevant cards before editing and validate the cross-domain contract explicitly in the final report.

Examples:

```bash
cat internal/api/AGENTS.md
cat internal/api/handlers/management/AGENTS.md
cat internal/usage/AGENTS.md
cat internal/pluginhost/AGENTS.md
```

## Commands

These commands are confirmed from Go tooling, CI workflows, Docker files, source flags, `scripts/check-lts-contract.sh`, or `docs/lts/sync-runbook.md`.

| Command | Purpose | Scope | Sandbox notes |
|---|---|---|---|
| `gofmt -w <files>` | Format touched Go files | touched files | OK after Go edits; avoid repo-wide formatting unless needed |
| `go test ./...` | Run all Go tests | repo | Usually local; first run may need Go 1.26 toolchain/module download if cache is missing |
| `go test -v -run TestName ./path/to/pkg` | Run targeted Go test | package | OK; replace target with real package/test |
| `scripts/check-lts-contract.sh` | Check LTS protected sentinels | repo | OK; uses local files plus `git grep` |
| `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'` | Usage/Management contract tests | repo | OK; CI derives same package set with `go list` |
| `go build -o test-output ./cmd/server && rm -f test-output` | CI-style server build smoke | repo | OK after deps are available; matches PR/LTS workflows |
| `git diff --check` | Check whitespace/conflict-marker issues | repo | OK; read-only diff validation |
| `go run ./cmd/server --config <path> --no-browser` | Run server locally with explicit config | repo | Starts local service and binds ports; needs valid config for meaningful smoke |
| `go run ./cmd/fetch_antigravity_models --auths-dir auths --output antigravity_models.json` | Fetch Antigravity model data | helper | Requires auth files and network; do not run by default |
| `go run ./cmd/fetch_codex_models --auths-dir auths --output codex_models.json` | Fetch Codex model data | helper | Requires auth files and network; may refresh/rewrite Codex auth metadata; do not run by default |
| `docker compose build` | Build container image | repo | Requires Docker daemon; not default sandbox validation |
| `./docker-build.sh [--with-usage]` | Interactive Docker build/run helper | repo | Interactive and Docker required; `--with-usage` calls Management API and stores temp secret under `temp/stats/` |

Workflow notes: PR/release/container workflows call `.github/scripts/refresh-model-catalogs.sh` to refresh embedded model catalogs, including Codex client metadata; local offline copies may be stale. `lts-contract.yml` runs contract guard, usage/management tests, build, and observes `go test ./...`. `release.yaml` is tag-triggered for `v*-tls-*` and requires release context plus `GITHUB_TOKEN`.

## Global rules

- 默认使用中文沟通；代码、命令、flag、API path、config key、package name 保留英文。
- 修改 Go 代码后运行 `gofmt`；不要用格式化改动掩盖行为变更。
- 先查询真实接口、配置、workflow、测试和源码实现，再下结论；不要臆造 API、flag、config key 或命令。
- 复用现有 helper、interfaces、translator registry、auth manager、config parser、pluginhost、usage manager 和 SDK types。
- 不要用 `log.Fatal` / `log.Fatalf` 终止服务进程；优先返回 error，并用 logrus 记录必要上下文。
- 不要在日志、error、test snapshot、PR 文案或公开文档中泄露 token、secret、management key、refresh token、API key、OAuth code、auth file 原文。
- 网络 timeout 策略沿用现有设计：凭据获取阶段可以有 timeout；上游连接建立后的 streaming 行为不要随意增加 timeout。
- Management API、Config、Auth file、API key、Public SDK / plugin API 都是外部契约；变更前检查 Panel、TUI、SDK、watcher、examples 和 tests 中的对应面。
- Stream lifecycle、auth selection、Home dispatch、model fallback、reasoning replay 和 usage attribution 是跨模块契约；改动其中一处时按目录地图读取所有受影响卡片，不以“无文本冲突”代替行为审查。
- 上游同步默认走 protected full-sync merge PR。任何触碰 usage、Management usage endpoints、config schema、auth/API key usage 结构、plugin runtime 或 `CPA-Panel-LTS` response shape 的冲突，必须按 protected delta 审查并保留 LTS 契约。

## Do not

- 不要使用 GitHub `Sync fork` 盲同步上游。
- 不要在 `main` 上直接 `git pull upstream main`。
- 不要用 `git checkout upstream/main -- .` 或文件级覆盖作为同步方式。
- 不要 squash 或 rebase upstream sync PR。
- 不要移除或降级 `internal/usage/`、`usage-statistics-enabled`、`/v0/management/usage*` 或 `/v0/management/usage-queue`。
- 不要把 Core 默认面板源改回上游或其他仓库，除非用户明确要求并同步检查 Panel 兼容性。
- 不要提交真实 `auths/` 内容、`.env`、`config.yaml`、token、secret、management key、本地日志或 `temp/stats/`。
- 不要把 `AGENTS.md` 改动混入普通产品 PR。
- 不要创建 `AGENTS.override.md` 或给纯组织目录机械新增空卡片来追求覆盖率。
- 不要把 CI/release/Docker 变更当作无风险文本修改；这些文件影响分发和运行路径。
- 不要在没有对应测试或明确说明缺口的情况下声称已验证统计、auth、translator、executor、pluginhost 或 Management API 行为。
- 不要执行发布、推送、tag、Docker destructive cleanup、release-equivalent manual build/upload commands、`terraform`、sudo 或外部平台变更，除非用户明确要求。

## Validation

默认验证按改动风险选择最小充分集合：

1. Go 源码改动：先 `gofmt -w <files>`，再运行相关 package 的 `go test`。
2. 通用构建风险：运行 `go build -o test-output ./cmd/server && rm -f test-output`。
3. LTS contract 风险：运行 `scripts/check-lts-contract.sh`。
4. 跨模块或共享契约改动：优先运行 `go test ./...`；如果耗时或环境阻断，在最终汇报中明确未跑原因。
5. Usage/Management usage：`go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`。
6. API/config/auth/watcher/executor/translator/plugin/SDK 改动：运行对应本地卡片列出的 package tests。
7. Docker/release 改动：本地只能做静态/构建验证；需要 Docker、network、tag 或 token 的步骤必须标注。

If validation cannot be run, say exactly which command was skipped and why. Do not present unrun checks as passing.

## Notes for future agents

- 当前工作区里的 `.playwright-mcp/` 和 `.gocache/` 不是产品源码；除非任务明确相关，忽略即可。
- README 主文档实际是 `README.md`、`README_CN.md`、`README_JA.md`；用户面向文档改动要检查多语言是否需要同步。
- `docker-build.sh --with-usage` 会通过 Management API export/import 统计并在 `temp/stats/.api_secret` 保存临时管理密钥；不要把该目录纳入 Git。
- `internal/registry/models/models.json`、`internal/registry/models/codex_client_models.json` 等嵌入 catalog 由 workflow helper 远程刷新；离线环境下不要假设本地 catalog 一定最新。
- `docs/lts/` 是维护策略和同步 runbook 的可提交文档目录；新增或修改 contract marker 时必须同步检查 `scripts/check-lts-contract.sh`。
- Codex abnormal reasoning retry 的配置语义、交付策略、fallback 策略和 client usage shaping 说明集中维护在 `docs/lts/codex-client-context-degradation-defense.md`。修改 `codex.abnormal-reasoning-retry` 的 config key、默认值、delivery/fallback 行为、client usage aggregation 或 hedged retry 语义时，先核对当前 Go 代码，再同步该文档、`config.example.yaml`、必要的 LTS contract marker / guard，以及 `CPA-Panel-LTS` 配置面影响。
- HF Space `BlueSkyXN/CPA-HFS-2026-03` smoke 要区分 Space Variables、runtime log 和响应头 `x-cpa-commit` 的真实 commit。
