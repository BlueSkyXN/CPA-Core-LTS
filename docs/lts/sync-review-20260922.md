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

Stage 1 exact head `545da586751b56168da54f59506e7ac5bc720beb` 的 7 项 CI 全部成功；PR `#266` 以 merge commit `0f1516138e39256a876609965fe026fb020d1428` 合入，父节点为原 `main` `ca027310` 与 exact head `545da586`，不是 squash/rebase。

## Stage 2 diff 结论

| 主题 / 代表提交 | 分类 | LTS 处理 |
|---|---|---|
| `9b52a499` AI gateway discovery | absorbed | 纳入 discovery service、zeroconf、CLI command 与 SDK advertiser；server version 输出继续使用 LTS `buildinfo.ProductName`。 |
| `42ca5d34..65348b95` Meta provider、`8335eac7` alias/error rules | adapted | 纳入 auth/login/config/runtime/Management token resolution；minted key 在对 request 可见前原子持久化，同时保留 LTS generation/registration-epoch fence、per-auth persist ordering 与 stale reload/remove 拒绝。 |
| `46d4baff` auth-files pagination | adapted | 纳入分页 response；virtual source/status hook 均在锁外运行，hook failure 只返回固定文案，不回显 token、OIDC、secret 或原始 error。 |
| `b715526a` plugin priority、`afba07ba` config preservation | adapted | 纳入 priority scheduler 与 YAML 保存；保留 LTS plugin `permissions`、built-in route ownership、usage plugin/API 和旧 ABI 兼容面。 |
| `6c5f6e18` Codex bootstrap timeout、`bb20fa2d` non-content framing | adapted | 纳入可配置 timeout、48 frame 与 1 MiB byte/chunk budget；超时后的 overload 才转 in-stream。保留 LTS zero-output incomplete request-scoped failover、abnormal reasoning retry/finalizer、affinity、upstream-model 和 usage attribution。 |
| `bef1f65c` pre-HTTP transport retry | adapted | TLS/DNS/dial/reset 可在 request-retry 内重试且不污染 credential；typed EOF/UnexpectedEOF/abnormal-close 502 继续执行 LTS bounded transient cooldown，因此 `transient-eof-cooldown` 仍 `patch-still-required`。 |
| `c4982e84` relayed tool-result image guard | adapted / upstream-equivalent candidate | 吸收标准 synthetic relay stripping；继续保留非标准 relay 的 LTS fallback omission marker。候选补丁本阶段不退休、不删除 shared production code/tests。 |
| `7fcbdf88` web-search/citation、`8c984672` orphan function output、`7def8425` tool name | adapted | 吸收 translator 修复与 request envelope；保留 plaintext provenance、parallel tool-call turn grouping、`thinking.ParseSuffix` 和 Responses effort/summary 独立语义。 |
| Devin/Gemini/Interactions/Responses 修复 | absorbed + adapted | 普通 provider、schema、tool、signature、terminal-stream 修复吸收；经过对应 translator/executor tests，不改变 LTS signature/replay、token accounting 或 Management usage shape。 |

Stage 2 文本冲突集中在 `cmd/server/main.go`、`config.example.yaml`、Management auth handlers/tests、Codex client/bootstrap、pluginhost/config watcher、OpenAI/Antigravity translators、OpenAI-compatible tool results 与 auth conductor。三语 README 继续保持 commercial-neutral；upstream `AGENTS.md` 未进入 staged diff。

## Stage 2 protected delta review

- usage/Panel/queue：`internal/usage/`、Management `/usage*`、`usage-statistics-enabled`、Redis usage queue、canonical usage v3 和 `CPA-Panel-LTS` 默认源未被替换或降级。
- auth lifecycle：Meta prepare/refresh 使用 persist-lock -> manager-lock 顺序，持久化成功后才安装 runtime snapshot；reload/remove 与 provider generation 仍可阻止旧 mint 写回。
- retry/cooldown：吸收 pre-HTTP retry，但没有把 terminal EOF 泛化为 cooldown-free；Home retry limit、model fallback、request-scoped stop 与 LTS cooldown wait contract 均保留。
- Codex stream：timeout/frame/byte budget 按 translated chunks 计入；只有 timeout 前的 overload/capacity 可在 header commit 前 failover，zero-output incomplete 继续是 request-scoped，其他非 overload terminal 保持 in-stream。
- translator：Responses `reasoning.effort` 不隐式打开 visible summary；function/custom output 前继续 flush pending calls；Antigravity native web search 保留 suffix/capability 解析。
- tool image：text-only standard relay 删除 synthetic user image message并把 marker 合并到 tool content；非标准 image part 也替换 marker；multimodal/unspecified target 保留 image relay。
- Management/plugin：auth-file pagination、Meta token resolution、priority scheduler 与 plugin config 为 additive；hook errors 经过 redaction，不改变 usage endpoint/Panel response shape。
- downstream patches：`responses-effort-summary-independence`、`transient-eof-cooldown`、plaintext provenance、auth generation/state isolation、Management error redaction 和 text-only fallback 均继续 `patch-still-required`；本阶段没有直接退休补丁。
- validation boundary：本阶段只验证源码、本地构建和测试；Release、部署、HF/Home、真实 provider、真实账号与浏览器 UAT 未执行。

## Stage 2 本地验证

- `scripts/check-lts-contract.sh`
- `go run ./scripts/ltsregistry --root .`
- `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
- Codex bootstrap/abnormal retry/stream、Meta mint/generation、transport retry/cooldown、Management pagination/redaction、translator/tool-result、plugin/config/watcher 与 SDK WebSocket 定向测试
- `go build -o /dev/null ./cmd/server`
- `go test ./...`
- `go list ./... | rg -v '/tmp$' | xargs go test -count=1`
- `git diff --check` 与 `git diff --cached --check`

Stage 2 exact head `f34eb22d5a7733850c9b4005b70b3b04a7040999` 的 7 项 CI 全部成功；PR `#267` 以 merge commit `5d8757bf8fb14cbca0cb72b5799ad63c2eb488df` 合入。远端 readback 确认其父节点为 Stage 1 main `0f1516138e39256a876609965fe026fb020d1428` 与 exact head `f34eb22d`，不是 squash/rebase。

## Stage 3 diff 结论

| 主题 / 代表提交 | 分类 | LTS 处理 |
|---|---|---|
| `b773607e` FreeBSD release 与多镜像 fallback | absorbed | 纳入 FreeBSD amd64/arm64、直接 sysroot cross-build、镜像 fallback 与 `curl --retry`；保留 LTS release `timeout-minutes: 45`。只做 workflow 静态/构建验证，不触发 tag、Release 或 upload。 |
| `cd5af08e..e84e248c` response model observability | adapted | 新增专用 `ResponseModel`，同时保留首个已知 served/upstream model 的 LTS `UpstreamModel` 兼容字段、substitution warning 和多 provider stream bounded observer。Redis/plugin 继续做 endpoint redaction。 |
| `12773e74` cache-write deduction | upstream-equivalent / removable | production extraction 已采用 upstream 溢出安全的 cache-read + cache-write 合并扣减；四项 LTS streaming/non-streaming 回归通过，账本改为 `removable`，本轮不删除回归覆盖。 |
| `cc545cbf`、`c2ea2684`、`40cc6489` Responses tool order/reasoning | adapted | 保留 LTS parallel function/custom-call turn grouping，同时纳入完整输出对齐、重复 ID fail-closed 与 reasoning continuation。仅当 call/output ID 完整、唯一且一一对应时延期消息；不完整或有歧义时保持自然顺序。 |
| `b4ff581d` Management expired cooldown | adapted / patch-still-required | 纳入 read-only response reconciliation；继续先执行 LTS manager pruning，使 registry/scheduler/persisted cooldown 收敛，并保护 expired-token、unauthorized、Cloudflare 与独立 credential failure 不被过期 quota deadline 清空。 |
| `42c9680e`、`ddc3f731`、`7b6fafce` Codex duplex/steering/prewarm | adapted | HTTP stream 与 duplex 共用 LTS protected request preparation：metadata privacy、cache affinity、model fallback/replay、reasoning wire normalization、service tier 与 usage attribution。下游 WebSocket 仍保持单 reader、bounded queue、disconnect/error lifecycle；duplex 接管并完整释放每个 prepared affinity。 |
| `28100e54` LCP compaction/node metadata | adapted | 纳入 tail/environment fingerprint、node kind、fork/compaction metadata；仍在 admission 成功后绑定，日志只使用 redacted session/auth identity，不恢复原始 session ID 或 auth ID。 |
| `a5ab6952` Kimi.ai OAuth/runtime | absorbed + adapted | 纳入 `.ai` OAuth、refresh、Management route、runtime 与 thinking；request-local auth clone 后只按受信任 domain 选择官方 `.com`/`.ai` endpoint，不修改共享 auth，也不信任任意 delegated `base_url`。 |
| `660a5800` xAI web-search alias | adapted | 恢复 aliased/namespaced web search，同时保留 LTS plaintext Multi-Agent tool arguments provenance；HTTP 与 WebSocket 覆盖两条行为。 |
| `ac3849e5` usage plugin、Redis metadata | adapted | 新增 `ResponseModel`、service tier、stream flag、`node_kind`、`is_fork`、`is_compaction`；保留 LTS token/timing/provenance/upstream-model 字段、`SafeBaseURL`、canonical usage v3 和旧 plugin JSON 兼容。 |
| `c93978c4`、`bcd13ca9`、`ffe6ad3c` schema/cache-control 修复 | absorbed | 纳入 Gemini schema、tool-result cache-control hoisting、OpenAI-compatible max-token 与普通 provider 修复；相关 translator/executor package tests 通过。 |
| `784285a4`、`0b9a91fb`、`29bdd856` 商业链接/图片 | reject presentation | 不恢复 PatewayAI、FluxA、affiliate、充值优惠、付费推荐或 Kimi 商业链接；删除新增推广图片，三语 README 保持 commercial-neutral。 |

Stage 3 共有 27 个文本冲突，集中在 release workflow、三语 README、config、usage/plugin/Redis、Codex/Kimi/xAI executor、Responses translator、WebSocket handler、auth selector/session affinity 与 plugin usage types。所有冲突均逐 hunk 融合；没有使用整文件 `ours`/`theirs`，没有 `AGENTS.md` 进入产品差异，也没有遗留 conflict marker。

## Stage 3 protected delta review

- usage/Panel/queue：完整 usage statistics、Management `/usage*`、`usage-statistics-enabled`、Redis queue、canonical v3、`CPA-Panel-LTS` 默认源和 Panel response compatibility 均保留；新 response model/session metadata 均为 additive。
- auth/session：Kimi delegated auth 保持 clone；LCP binding 继续 admission 后提交；scheduler provider alias、execution close、Home session alias 和 model fallback 保留 LTS scope/lifecycle。
- Codex WebSocket：duplex/steering 复用同一条 protected preparation 管线；普通 stream 与 duplex 分别拥有明确 affinity ownership，single-reader 与 terminal/disconnect 语义通过完整 executor/API tests。
- Management cooldown：upstream projection 与 LTS destructive pruning 不重复清理独立错误；过期 token、unauthorized 和 Cloudflare warning 继续阻止错误的 active readback。
- translators：Responses 对齐只重排完整无歧义历史；LTS parallel turn grouping、cache-write accounting、text-only image omission、signature/plaintext provenance 和 service-tier normalization均保留。
- presentation：拒绝 sponsor/affiliate/referral/充值推广及两张新增推广图；上游 ancestry 保留，但产品树与三语 README 不恢复商业推广。
- downstream patches：`openai-claude-cache-write-accounting` 调整为 `removable` 而非本轮删除；`text-only-relayed-tool-image-guard`、`expired-availability-pruning`、`responses-chat-tool-call-turn-grouping`、WebSocket/affinity、usage/Panel、plaintext provenance 等继续 `required`。
- validation boundary：源码、构建、contract、registry 和全仓测试已验证；Release、部署、HF/Home、真实 provider、真实账号与浏览器 UAT 未执行。

## Stage 3 本地验证

- `scripts/check-lts-contract.sh`
- `go run ./scripts/ltsregistry --root .`
- `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
- `go test -count=1` 覆盖 executor/helpers、API/Management、auth/session/Home、全部 translators、Kimi/auth/config/watcher、plugin/Redis/usage、logging/registry/thinking
- patch ledger 原回归：Claude cache-write 四项与 text-only relayed tool-image guard
- `go build -o /dev/null ./cmd/server`
- `go test ./...`
- `go list ./... | rg -v '/tmp$' | xargs go test -count=1`
- `git diff --check` 与 `git diff --cached --check`

本阶段没有执行真实 Release workflow、tag、上传、部署、HF/Home、真实 provider inference 或浏览器 UAT；这些外部交付层不能由源码 merge 或 CI 替代。
