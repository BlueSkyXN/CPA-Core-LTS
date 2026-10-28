# V8 上游同步审查（2026-10-28）

本轮维护 `v8-dev`，为后续升级评估准备一组配套的 Core 与 Panel。Core 按 protected full-sync 保留官方 merge 历史；Panel 按 selective-port 吸收兼容增量。

## 基线与同步范围

| 对象 | 提交 |
|---|---|
| Core 起点 `origin/v8-dev` | `afc1f234883bad744432d4cebcc4823a69e8914b` |
| 上次官方同步点 | `d946c4fba6fc9a5cfbca1f3d85184d62ad2fb683` |
| 本轮官方目标 | `9717e325ba6ab2c7483ea93ccb5f2047eb2e7dd5` |
| 一并合入的 LTS main | `a68f061e0c6af6eabe06ff0faad14fd376bb8e95`，包含 #324 的 stop-reason 修复 |
| Panel 起点 `origin/v8-dev` | `829ec19dcd9075c39815966862c00e84e3194d11` |
| Panel 官方审查目标 | `64b4f5aed19b8f791eb21eb9e785ea379ea05b44` |

Core 官方区间含 96 个新增历史提交，覆盖插件执行与 schema 7、压缩、OAuth session、WebSocket 终止、Home latency、模型目录、Gemini/Claude/Interactions 翻译及诊断修复。此次没有另行关闭或标记旧 upstream-port PR。

| 阶段 | 官方终点 | 本地 merge commit | 冲突文件数 |
|---|---|---|---:|
| LTS main 增量 | `a68f061e0` | `54521bfeb` | 单独保留 V8 translator 语义 |
| Stage 1 | `bc0643405a1d0adf77dd0f109972ac3313d66397` | `ee9081c6e` | 11 |
| Stage 2 | `aa4130e64c84546aa3f0c5db23af3961b62ba14e` | `32ab643ff` | 22 |
| Stage 3 | `9717e325ba6ab2c7483ea93ccb5f2047eb2e7dd5` | `e335f0b1f` | 26 |

`git merge-base --is-ancestor 9717e325b HEAD` 通过。三段之间没有 squash 或 rebase；后续合入开发线也应使用 merge commit。

## Protected delta review

| 契约与边界 | 本轮处理 | 验证依据 |
|---|---|---|
| 完整 usage、Management usage、queue | 保留 LTS v0 usage 扩展、聚合、导入导出、队列及 request/auth/model/tier/timing/request-size 字段；合入上游 identity capture。 | usage、Management、queue、SDK usage 回归和临时 Core/Panel usage round-trip。 |
| Interactions token accounting | `input_tokens` / `prompt_tokens` 的显式 cache read/write 继续独立计数；`total_input_tokens` 已包含 cache。保留不一致 token breakdown 的诊断，禁止在合并时提升为 complete。 | helper 测试及插件 handler 流式测试同时覆盖独立 cache 与 inclusive total。 |
| Config / auth / watcher | 保留 V8 配置投影、ETag 条件写、opaque plugin YAML、auth index 排除、Flow 和旧认证身份。新压缩项接入现有配置/热重载链。 | config、Management、watcher、auth 测试及 Panel 写入后回读。 |
| Credential replacement | 重新登录可解除 unauthorized；typed quota 状态继续保留。只有没有 typed error 时才使用文本 unauthorized fallback。 | `TestCredentialChangeClearsUnauthorizedDeadlineButPreservesQuota` 及 auth 回归。 |
| Codex compression / metadata | 保留 LTS outbound transport、identity、metadata、affinity、请求大小。两条请求构造路径在完成语义改写后压缩；显式 `request-compression: none` 优先；原 auto 默认保留，新 compression 开关启用时使用其阈值。接入上游 415 fallback 与响应解压。 | compression、metadata、affinity、request-size 回归，新增 explicit-none 同步测试。 |
| WebSocket / stream lifecycle | 保留 connection generation fence、会话身份和投递后不重放；接入上游 transport-loss、active response、late interrupt 处理。保留 typed handshake error 和 Retry-After；避免重复包装 bootstrap error。 | executor、auth、Responses 流式测试及 race；write-loss fixture 使用真实连接身份。 |
| 429 / model fallback | 上游 turn transient rate limit 与 LTS usage-limit/model fallback 分类并存；transient 分支提前返回，不落入普通 quota cooldown。 | auth/cooldown/model fallback 回归及 race。 |
| 请求日志 | 保留 1 MiB preview、redaction、临时文件 cleanup；鉴权前只记录 metadata，鉴权后有界捕获；error-only 使用 deferred tee。zstd window/memory 限制为 8 MiB，最终 preview 继续受 1 MiB 或更小配置约束。 | body-limit、压缩、logging、middleware 回归；匿名请求不读 body；最终 truncation marker 保留。 |
| 插件 ABI / 能力 | 接入 schema 7 的 request-body omission，同时保留 LTS stream history opt-out、执行 lifecycle、权限和 typed failure reason。保留所选账号模型的 thinking 能力与 normalizer authority。 | pluginhost、pluginapi、pluginabi、handler 测试与 race；四个原生插件另行构建验证。 |
| Home / Flow / persistence | 保留 dispatch 生命周期、credential/Flow 归属和本地统计；合入 latency/负载修复，保留 store 的路径、权限和错误传播规则。 | Home、Flow、auth、store、SDK service 与 watcher 测试。 |
| Codex catalog | 接入新的 catalog source API、官方模型及 workspace/cloud 字段，继续运行 LTS validator；保留 Daybreak guard 与 Astra experimental context 字段。 | registry、models、catalog validator 回归；模型显式能力优先于 fallback 模板。 |
| Claude / Gemini / Devin 翻译与诊断 | 保留 #324 的 `sensitive` / plugin stops、V8 空 output 位置、signature 及完整 usage；Go 1.26 JSON 错误措辞映射到固定 Devin 诊断码。 | translator、signature、executor、pluginhost 测试；诊断码测试使用真实 JSON decoder。 |
| 配套 Panel / 分发 | 继续使用 CPA-Panel-LTS 与 `management.html`；保留 npm、完整 usage、Flow 与插件能力门控。发布 workflow 和容器触发规则没有变化。 | Panel validate、mock browser、临时 Core browser smoke；两仓均未改 AGENTS。 |

## 回归测试适配

- Spark 已不在嵌入目录中，原测试借用了 GPT-5.5 fallback 的四档推理能力；本轮 GPT-5.5 增加了 max/ultra。测试显式提供 Spark 的四档能力，继续严格检查 summary 默认关闭与 effort 保留。
- Interactions 插件测试使用两个合法 fixture，分别验证 `input_tokens=80 + cache_read_tokens=20` 和 `total_input_tokens=100`，断言同一完整 140-token breakdown。
- Anthropic 流式测试按独立 auth identity 接收异步 usage，避免上一用例的同 provider 事件混入。失败标志和 token 断言保持不变。
- Home failover 测试允许已有的 100 ms heartbeat timeout 加最多 1 s 初始 backoff，并在重试期间继续断言活跃 scope 不被 drain。
- 后两组用例重复 20 次通过，另以 race 重复 3 次通过。

## Downstream patch ledger 复查

沿用既有 86 项登记：74 项 required、9 项 removable、3 项 retired。本轮范围涉及其中 59 项的登记路径。
对 required 项保留现有下游实现与回归；相似的上游变更按上表组合，未发现足以在本次同步中取消完整 LTS 语义的依据。
9 项既有 removable 继续保持可退休状态，本次不混入 retirement；3 项 retired 保留历史记录，不改写 introduced_in / retired_in。

下表的“涉及路径”来自本轮官方 diff 与补丁登记路径的交集；“登记测试数”含历史 retired 项，实际执行情况在验证段单列。

| Patch ID | 复查结论 | 涉及路径数 | 登记测试数 |
|---|---|---:|---:|
| `responses-effort-summary-independence` | patch-still-required | 0 | 4 |
| `codex-model-fallback` | patch-still-required | 9 | 35 |
| `codex-rate-limit-continuity` | patch-still-required | 2 | 32 |
| `codex-interactions-service-tier-response` | patch-still-required | 0 | 4 |
| `codex-image-generation-tool-dedup` | retired，保持历史记录 | 0 | 2 |
| `plugin-configured-enable-default` | patch-still-required | 1 | 3 |
| `home-plugin-sync-cancellation` | retired，保持历史记录 | 2 | 3 |
| `codex-gpt56-ultra-level` | patch-still-required | 7 | 30 |
| `codex-client-media-model-visibility` | retired，保持历史记录 | 2 | 2 |
| `codex-model-header-provider-snapshot` | patch-still-required | 2 | 5 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 3 | 4 |
| `codex-client-metadata-privacy` | patch-still-required | 11 | 60 |
| `codex-model-not-found-request-scope` | patch-still-required | 0 | 4 |
| `codex-websocket-reader-generation` | patch-still-required | 5 | 9 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 3 | 10 |
| `xai-explicit-tool-choice-none` | patch-still-required | 1 | 5 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | 0 | 1 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | 3 | 6 |
| `api-request-body-size-limit` | patch-still-required | 5 | 9 |
| `object-store-auth-path-containment` | patch-still-required | 0 | 20 |
| `auth-token-private-file-permissions` | patch-still-required | 0 | 2 |
| `auth-store-list-error-propagation` | patch-still-required | 2 | 5 |
| `auth-store-metadata-hydration` | patch-still-required | 2 | 10 |
| `panel-release-token-isolation` | patch-still-required | 0 | 4 |
| `gemini-unknown-method-not-found` | patch-still-required | 0 | 1 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | 0 | 4 |
| `management-logs-bounded-response` | patch-still-required | 0 | 5 |
| `transient-eof-cooldown` | patch-still-required | 1 | 3 |
| `expired-availability-pruning` | patch-still-required | 1 | 10 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | 0 | 12 |
| `codex-desktop-tool-overlay` | patch-still-required | 5 | 25 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | 2 | 18 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | 5 | 6 |
| `provider-runtime-state-isolation` | patch-still-required | 6 | 18 |
| `auth-provider-generation-fence` | patch-still-required | 7 | 11 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | 1 | 12 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 1 | 1 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | 3 | 1 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | 1 | 2 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | 2 | 2 |
| `astra-native-responses-controls` | patch-still-required | 4 | 5 |
| `astra-async-guidance-hotfix` | patch-still-required | 1 | 3 |
| `local-flow-control` | patch-still-required | 9 | 21 |
| `openai-claude-cache-write-accounting` | upstream-equivalent，保留待单独退休 | 1 | 4 |
| `plugin-stream-history-capability-negotiation` | patch-still-required | 6 | 1 |
| `wsrelay-terminal-frame-preservation` | upstream-equivalent，保留待单独退休 | 0 | 3 |
| `codex-dotted-collaboration-tool-restoration` | upstream-equivalent，保留待单独退休 | 0 | 3 |
| `session-dispatch-context-after-selection` | patch-still-required | 2 | 1 |
| `antigravity-compaction-completion-validation` | patch-still-required | 1 | 1 |
| `gemini-explicit-text-signature-carriers` | patch-still-required | 2 | 2 |
| `antigravity-pool-settings-cache-key` | upstream-equivalent，保留待单独退休 | 1 | 1 |
| `claude-missing-tool-schema-default` | upstream-equivalent，保留待单独退休 | 1 | 1 |
| `amp-session-extraction-priority` | patch-still-required | 0 | 3 |
| `antigravity-response-json-schema-dialect-preservation` | patch-still-required | 0 | 2 |
| `management-auth-refresh-status-redaction` | patch-still-required | 0 | 2 |
| `responses-stream-error-flat-compatibility` | patch-still-required | 0 | 3 |
| `responses-tool-output-item-id-disambiguation` | upstream-equivalent，保留待单独退休 | 1 | 4 |
| `bounded-forced-auth-refresh` | upstream-equivalent，保留待单独退休 | 1 | 4 |
| `antigravity-empty-text-stream-block-lifetime` | upstream-equivalent，保留待单独退休 | 2 | 2 |
| `responses-same-chunk-reasoning-order` | upstream-equivalent，保留待单独退休 | 0 | 2 |
| `usage-base-url-credential-redaction` | patch-still-required | 4 | 3 |
| `claude-legacy-credential-identity-preservation` | patch-still-required | 0 | 2 |
| `codex-compat-unknown-signature-rejection` | patch-still-required | 1 | 2 |
| `text-only-relayed-tool-image-guard` | patch-still-required | 0 | 1 |
| `gemini-server-tool-signature-validation` | patch-still-required | 2 | 3 |
| `model-list-response-boundary` | patch-still-required | 1 | 1 |
| `codex-outbound-strategy` | patch-still-required | 5 | 16 |
| `management-proxy-url-probe` | patch-still-required | 0 | 2 |
| `credential-change-unauthorized-recovery` | patch-still-required | 2 | 1 |
| `management-auth-sync-error-redaction` | patch-still-required | 0 | 2 |
| `claude-setup-token-metadata-lock` | patch-still-required | 0 | 2 |
| `daybreak-blue-catalog-guard` | patch-still-required | 2 | 11 |
| `daybreak-blue-access-program-errors` | patch-still-required | 2 | 8 |
| `v8-plugin-config-opaque-yaml` | patch-still-required | 1 | 6 |
| `translation-failure-request-scoped` | patch-still-required | 1 | 2 |
| `openai-compat-image-status-errors` | patch-still-required | 1 | 1 |
| `v8-turn-credential-ambiguity` | patch-still-required | 0 | 2 |
| `interceptor-path-override-validation` | patch-still-required | 2 | 2 |
| `apply-patch-final-snapshot-consistency` | patch-still-required | 0 | 4 |
| `terminal-unauthorized-same-credential-update` | patch-still-required | 1 | 2 |
| `usage-queue-record-outcome` | patch-still-required | 1 | 1 |
| `v8-yaml-replace-document-comments` | patch-still-required | 0 | 1 |
| `codex-empty-incomplete-failure-usage` | patch-still-required | 4 | 1 |
| `openai-compat-complete-sse-usage` | patch-still-required | 1 | 3 |
| `usage-request-size-fields` | patch-still-required | 8 | 17 |
| `codex-permanent-refresh-failure` | patch-still-required | 2 | 7 |

## 验证与交付边界

| 检查 | 结果 |
|---|---|
| `go test -p 4 -count=1 -json ./...` | 110 包通过、30 包无测试；10,492 个顶层测试通过。含子测试共 24,277 项通过、10 项跳过、0 失败。 |
| `scripts/check-lts-contract.sh` | protected sentinels 与 registry validator 均通过。 |
| `go build -o <temporary-output> ./cmd/server` | 通过；运行代码为 Stage 3 的 `e335f0b1f`，后续只改测试与审查文档。 |
| 关键路径 race | 下列 21 个实际包通过；Home failover / Anthropic stream 的最终测试适配另以 race 重复 3 次通过。 |
| Downstream 登记回归 | 83 项 active patch 对应的 563 个去重测试全部通过，无缺失。retired 项沿用历史登记，不要求恢复已删除测试。 |
| 原生插件 | CodeBuddy、Qoder、Copilot、ZCode Coding Plan 各自的 nested module race tests、vet、macOS c-shared 构建通过；Qoder/Copilot 普通测试含动态加载通过，ZCode `test-cpa.sh` 动态集成通过；Core 全量测试包含 CodeBuddy 动态测试。 |
| Panel | `npm run validate:lts` 的 703 项测试、contract、type-check、build 通过；保留 1 条既有 lint warning。mock 与临时真实 Core browser smoke 均通过。 |
| Diff / ancestry | diff whitespace 检查与官方目标祖先检查通过；AGENTS、release workflow、Dockerfile 没有本轮变更。 |

Race 命令：

```bash
go test -race -p 2 -count=1 -json \
  ./sdk/cliproxy/auth ./sdk/cliproxy/usage ./internal/pluginhost \
  ./internal/runtime/executor ./internal/runtime/executor/helps \
  ./internal/api/middleware ./sdk/api/handlers ./sdk/api/handlers/openai \
  ./internal/usage ./internal/redisqueue ./internal/home ./internal/watcher/... \
  ./internal/store ./internal/registry ./internal/client/codex/models \
  ./sdk/pluginapi ./sdk/pluginabi ./internal/thinking ./sdk/cliproxy/flowcontrol
```

全量测试的 10 项 skip 分别为：1 项显式 opt-in 的 Devin live test、6 项缺少外部采集语料的 signature 测试、1 项未配置 `CPA_TLS_FP_PROXY` 的 TLS capture、2 个由父测试调用的 helper process 入口。未为本次同步采集真实凭据或流量。

验证使用临时本机实例、合成配置与合成认证。没有启动或更新 HF Space，没有读取/迁移生产 auth 或运行供应商真实推理；既有 UAT 保持原状态。
本轮结果用于开发线同步，生产升级仍需冻结配套 Core/Panel/插件版本，并安排配置迁移、真实客户端验收与回滚演练。macOS 动态插件验证不覆盖 Linux/Windows 发行包或用户已安装的历史插件二进制。

## 冲突文件记录

### Stage 1

```text
internal/home/cluster_balance.go
internal/pluginhost/adapters_executors.go
internal/pluginhost/host_callbacks.go
internal/pluginhost/rpc_client.go
internal/pluginhost/rpc_client_error_test.go
internal/pluginhost/stream_bridge.go
internal/runtime/executor/helps/plugin_executor_usage.go
internal/signature/gemini_sanitize.go
internal/store/gitstore.go
internal/store/gitstore_test.go
sdk/pluginabi/types.go
```

### Stage 2

```text
config.example.yaml
internal/config/config.go
internal/config/config_types.go
internal/config/config_v8.go
internal/pluginhost/adapters_interceptors.go
internal/pluginhost/host_callbacks_test.go
internal/pluginhost/rpc_schema.go
internal/runtime/executor/codex_executor_cache_test.go
internal/runtime/executor/codex_executor_execute.go
internal/runtime/executor/codex_executor_request.go
internal/runtime/executor/codex_executor_stream.go
internal/runtime/executor/codex_openai_images.go
internal/runtime/executor/helps/usage_helpers.go
internal/runtime/executor/openai_compat_executor.go
internal/runtime/executor/websocket_session_target_test.go
internal/runtime/executor/xai_websockets_executor_test.go
sdk/cliproxy/auth/conductor_cooldown.go
sdk/cliproxy/auth/conductor_home.go
sdk/cliproxy/auth/conductor_lifecycle.go
sdk/cliproxy/auth/conductor_refresh.go
sdk/cliproxy/usage/manager.go
sdk/pluginabi/types.go
```

### Stage 3

```text
README.md
README_CN.md
README_JA.md
config.example.yaml
internal/api/max_request_body_bytes_test.go
internal/api/middleware/request_logging.go
internal/api/middleware/request_logging_test.go
internal/api/middleware/response_writer.go
internal/logging/cpa_trace.go
internal/registry/catalog_sources.go
internal/registry/models/codex_client_models.json
internal/runtime/executor/codex_executor_terminal.go
internal/runtime/executor/codex_websockets_duplex.go
internal/runtime/executor/codex_websockets_errors.go
internal/runtime/executor/codex_websockets_execute.go
internal/runtime/executor/codex_websockets_executor.go
internal/runtime/executor/codex_websockets_session.go
internal/runtime/executor/codex_websockets_stream.go
internal/runtime/executor/helps/usage_helpers.go
internal/runtime/executor/openai_compat_executor.go
internal/signature/gemini_sanitize.go
sdk/cliproxy/auth/conductor_cooldown.go
sdk/cliproxy/auth/conductor_execution.go
sdk/cliproxy/auth/conductor_selection.go
sdk/cliproxy/auth/conductor_stream.go
sdk/cliproxy/auth/home_concurrency.go
```
