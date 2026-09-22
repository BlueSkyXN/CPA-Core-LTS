# 2026-09-22 Core upstream 30 天审查与 protected full-sync

## 冻结范围与分段

- 审查窗口：`2026-08-23T00:00:00+08:00` 至 `2026-09-22`。
- 起始 LTS main：`ca027310076a7687ba2aa1436a44560edee83181`。
- upstream merge-base：`5b2785617d1e7de84a9f4dee599d275a4ccd8999`。
- 冻结 upstream main：`ffe6ad3c5fcf0a5eedd2198cd2e04b0249dc5063`。
- 30 天窗口内 upstream/main 可达 478 个提交；其中 247 个已进入 LTS 基线，本轮待纳入 231 个提交、165 个 first-parent 节点。
- 本轮同时触碰 runtime、auth、stream、logging、Management、pluginhost、config/watcher 和 usage-adjacent seam，按 runbook 分三段，不做文件覆盖或逐 PR cherry-pick。
- Stage 1：`5b278561..44e62bc8`，55 个 first-parent 节点，基础 Management cooldown/plugin quota、Codex native fidelity 与 Devin provider/OAuth/catalog。
- Stage 2：`44e62bc8..0b550539`，55 个 first-parent 节点，Meta/plugin/translator/auth 扩展。
- Stage 3：`0b550539..ffe6ad3c`，55 个 first-parent 节点，Codex duplex/response-model、Kimi、usage/plugin API 与 schema 修复。

## 30 天 intake 总体分类

- `absorbed`：普通 discovery、provider、schema、translator、stream、security、crash 和模型目录修复；仍按对应 protected seam 运行行为测试。
- `adapted`：auth identity/session/model resolution、Management auth/cooldown/pagination、plugin ABI/quota/scheduler、Codex native Responses、response model/service tier、token/cache/final usage 与 Redis/plugin usage 扩展。
- `upstream-equivalent candidate`：`883660fb` 的 OpenAI/Codex -> Claude cache-write accounting，以及 `c4982e84` 的 text-only relayed tool-image guard；只有在进入对应 stage、保留 LTS 额外行为并通过原 ledger 回归后才能调整状态。
- `patch-still-required`：LTS usage/Panel/queue、Codex fallback/rate-limit/client metadata/WebSocket lifecycle、provider generation fence/state isolation、Kimi delegated-auth immutability、Management error redaction、signature/plaintext provenance、Flow 与 plugin stream-history negotiation。
- `reject presentation`：sponsor、affiliate、referral、充值优惠和付费推荐 README/图片；保留 ancestry，不恢复产品树中的商业推广。
- `reject from product sync`：upstream `AGENTS.md` 变更；agent 指令不混入普通产品同步。

## Stage 1 diff 结论

| 主题 / 代表提交 | 分类 | LTS 处理 |
|---|---|---|
| `e30de3d5` Antigravity capability probe cache | absorbed | 复用 bounded cache；不改变 signature/replay scope。 |
| `b192f655` plugin quota/probe | adapted | 新 Management/plugin capability 保持 namespaced；不替换 built-in usage，不暴露 credential payload。 |
| `3c3938fe` auth-provider login query metadata | adapted | 保留 OAuth state/session、no-browser、auth file identity 与 redaction。 |
| `ac02da6c` sponsor link | reject presentation | 三语 LTS README 保持 commercial-neutral。 |
| `e696ea47` Codex turn-state header | absorbed | 与 LTS canonical metadata/header projection 并存。 |
| `f702bc1a` Responses Lite native fidelity | adapted | 原生请求不补 instructions、不重建完成 output；保留 LTS canonical `Session-Id`、cache affinity、metadata privacy、service tier、reasoning retry 与 usage attribution。 |
| `d23ba5ee` Management cooldown snapshot | adapted/divergent | 吸收只读 cooldown view；Management GET 继续执行 LTS expired-availability pruning，使 registry/scheduler/persisted snapshot 收敛，并新增组合回归证明只清过期项。 |
| Devin provider/OAuth/catalog `f9475276..44e62bc8` | absorbed + adapted | 吸收 provider、OAuth、wire、catalog 与 quota status；保持 auth index/source、config hot reload、request-log bounded capture、usage reporter 与 secret redaction。 |

Stage 1 冲突集中在三语 README、`config.example.yaml`、request logging/API tests、Codex HTTP/WebSocket stream、plugin API tests。处理原则：README 只保留商业中立侧；配置同时保留 Responses Lite filter 示例与 Flow V3；Codex 同时保留 native fidelity 和 LTS metadata/affinity/reasoning/usage；plugin API 以 additive capability 保持旧 ABI。

## Stage 1 protected delta review

- retained capabilities：`internal/usage/`、canonical usage v3、Management `/usage*`、`usage-statistics-enabled`、Panel 默认 `BlueSkyXN/CPA-Panel-LTS` 均保留。
- LTS-owned features：abnormal reasoning retry、model fallback、rate-limit continuity、Flow、Redis usage queue 和 client metadata privacy 均保留。
- auth/config：Devin 新 auth/config 采用现有 Manager、watcher、hot reload 与 private-file contract；没有把 token、OAuth code 或 raw auth file 写入日志/文档。
- Management/plugin：cooldown 与 quota/probe 为 additive response/capability；没有改动 usage response shape，也没有让 plugin route 覆盖 built-in route。
- runtime/usage：Codex native output 只在明确 Lite 请求和 Codex provider 上启用；非原生请求继续 compatibility normalization，所有路径保留 attempt/final usage 和 upstream-model attribution。
- downstream patches：Stage 1 没有可直接退休的补丁；`expired-availability-pruning`、Codex metadata/session/fallback/WebSocket、provider generation/state isolation 与 logging bounds 继续 `patch-still-required`。
- validation boundary：本阶段只做源码、本地构建和测试；Release、部署、HF/Home、真实账号与浏览器 UAT 未执行。

## Stage 1 本地验证

- `scripts/check-lts-contract.sh`
- `go run ./scripts/ltsregistry --root .`
- `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
- `go test ./internal/runtime/executor ./sdk/api/handlers/openai`
- `go test ./internal/api/... ./sdk/pluginapi ./sdk/pluginabi ./internal/pluginhost`
- `go test ./internal/auth/... ./internal/config ./internal/registry ./internal/watcher/... ./sdk/auth ./sdk/cliproxy/auth`
- `go build -o /dev/null ./cmd/server`
- `go test ./...`
- `git diff --check` 与 `git diff --cached --check`

Stage 1 exact-head CI、PR merge 与 post-merge readback 在远端完成后补记；Stage 2/3 尚未执行，不能用本阶段测试代替。
