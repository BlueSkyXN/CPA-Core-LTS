# Codex Client Context Degradation Defense

本文记录 Codex abnormal reasoning retry 的客户端上下文降级对抗设计，以及从初版重试到当前 client usage shaping 的版本演进。

“降智”指上游 reasoning tokens 被截断到固定异常值（当前主观察值为 516/1034）导致模型推理退化；abnormal reasoning retry 是第一道防线，client usage shaping 是第二道防线。本文聚焦第二道防线：如何避免重试产生的 hidden retry cost 被客户端误判为 active context 增量。

本文件不新增 LTS contract marker；受保护能力以 `docs/lts/core-feature-contracts.yaml` 中的 `codex-abnormal-reasoning-retry` 为准。

## 摘要

- 当前防线的核心不是“隐藏成本”，而是把 internal usage accounting 和 client-visible context accounting 分面处理。
- 内部 usage statistics 必须记录所有 attempt，包括被丢弃的 abnormal attempt。
- 返回给 Codex CLI/App 的 `response.usage.total_tokens` 默认只代表最终 delivered/fallback attempt 对客户端上下文的贡献。
- 版本历史显示该算法经历了 6 个落地阶段：serial retry、attempt-level accounting、exhaustion controls、speed hedge、quality/fold 聚合、最终 client usage aggregation 分离。
- 当前推荐模式是 `client-usage-aggregation: delivered-only`；需要诊断 hidden retry 小项成本时使用 `sum-with-delivered-total`。

## 背景

上游有时会将 reasoning tokens 截断到固定值（典型如 516/1034），导致模型推理链严重缩短、输出质量骤降。`local/codex-degraded-reasoning-516-study-2026-07-04.md` 用 535 行 usage 事件和 `candy_reason_probe.py` 复核了这个现象：84 条 failed 记录全部精确等于 516（80 条）或 1034（4 条）；gpt-5.5 xhigh 16 路糖果题探测中，reasoning=516 的 3 条样本 0/3 全错，非 516 的 13 条 13/13 全对。

这个结论有两个边界：

- `reasoning_tokens == 0` 不等于降智。它常见于工具步、秒回或不需要深推理的普通 turn；用“低 reasoning 阈值”拦截会误伤。
- `output_tokens` 包含 hidden reasoning，`visible = output_tokens - reasoning_tokens`。可见输出长短不能单独代表质量；516 样本甚至可能比健康回答的可见输出更长。
- probe 的 3 条 516 样本提供的是质量 ground truth，不等同于“当时生产 Core 一定已经拦截了 probe”。`local/codex-degraded-reasoning-516-study-2026-07-04.md` 明确把“probe 为何未被拦截”列为部署/config open issue，需要用 `x-cpa-commit`、runtime config 和真实代理路径另行验证。

Codex abnormal reasoning retry 是我们的第一道防线：拦截可疑响应，丢弃异常 attempt 后透明重试。但这个机制天然产生两类 token usage：

- **attempt-level usage**：真实上游调用成本，含被丢弃的 abnormal attempt，必须写入内部 usage statistics 和审计记录。
- **client-visible `response.usage`**：返回给 Codex CLI/App 的 usage，直接影响客户端上下文预算判断。

第二道防线解决的问题是：被丢弃 attempt 的成本不应暴露给 Codex CLI 当前 turn 的 `usage.total_tokens`。一旦异常重试的 hidden cost 被累加到客户端，Codex CLI 会将其误判为真实上下文占用，提前触发 auto compact——一次代理侧恢复动作因此变成客户端侧 premature compaction。

## 版本历史

版本历史基于 `main` 分支的 `git log`、`git show` 和 GitHub PR 记录，时间均为北京时间。

| 版本 | 时间 | 证据 | 算法变化 | 当前结论 |
| --- | --- | --- | --- | --- |
| V1 serial retry | 2026-06-30 | `1e6d428b` `feat(lts): retry abnormal Codex reasoning responses` | 新增 Codex abnormal reasoning guard：按 `model-contains`、`reasoning-tokens`、`auth-kinds` 等条件识别可疑响应，丢弃 abnormal attempt 并透明重试。 | 奠定异常识别和重试主路径。 |
| V1.5 accountable retry | 2026-06-30 至 2026-07-01 | `88cf5c61`、`877a8033`、`929056e4` | 补齐 discarded attempt 的 internal usage accounting；增加 `reasoning-efforts` 过滤；增加独立 `max-retries` 和 `exhausted-behavior: error/pass-through`。 | 从“只重试”升级为可审计、可限额、可配置耗尽行为的 retry-without-penalty 机制。 |
| V2 speed hedge | 2026-07-02 | `6af7fc59` `feat(codex): add hedged abnormal reasoning retry` | 新增 hedged retry：异常重试路径先启动 primary lane，`hedge-delay-ms` 后可启动 second lane；默认 first success wins；支持 `require-distinct-auth`。 | 用并发 lane 降低异常链路尾延迟，但仍以速度优先。 |
| V3 quality hedge + reasoning-fold | 2026-07-03 | PR `#134` / merge `809acd08`；commits `c73d5610`、`4e954156` | 新增 `hedged-retry.mode: speed/quality`；quality 模式等待同一波已派发 lane，并按最大成功 `output_tokens` 选胜者；新增 `client-usage-aggregation: reasoning-fold/sum`；修复 streaming quality usage 在 OpenAI Chat、Claude、Gemini 下游格式中的折算。 | 这是第一版“质量优先”算法，但 `reasoning-fold` 后来被证明容易与 Codex CLI usage 语义冲突。 |
| V4 default quality + longest fallback | 2026-07-03 | PR `#135` / merge `c16b22a8`; commit `b7546294` | 将 `hedged-retry.mode` 默认和非法值归一从 `speed` 改为 `quality`；`exhausted-behavior: pass-through` 时选择 retry 链内 `output_tokens` 最大的 abnormal fallback。 | 质量优先成为默认语义；耗尽时也尽量返回最长可用 fallback。 |
| V5 separated client usage | 2026-07-04 | PR `#136` / merge `c269724d`; PR `#137` / merge `c63986f8`; commits `0390c13c`、`dc11b24b` | 将 `client-usage-aggregation` 收敛为 `delivered-only`、`sum`、`sum-with-delivered-total`；默认 `delivered-only`；删除旧 `reasoning-fold` 正式行为和 alias；`total_tokens` 缺失时只用 `input_tokens + output_tokens` 兜底。 | 当前最终防线：真实成本进入 internal usage，客户端 context pressure 默认只看 delivered/fallback attempt。 |

### 不计入当前主线的线索

PR `#89`、`#90`、`#108`、`#109` 涉及 compact evidence、invalid encrypted reasoning retry、compact routing controls、compact defer loading 等方向。这些是 2026-05-31 至 2026-06-01 的 closed draft PR，未 merge 到 `main`，仅作为上下文问题的早期线索，不计入当前算法版本。

### local 素材口径

`local/handoff-codex-abnormal-reasoning-retry-client-usage.md`、`local/2026-07-04-codex-cli-context-usage-handoff.md`、`local/2026-07-04-codex-delivered-only-implementation-guide.md` 和 `local/2026-07-04-codex-usage-total-only-vs-delivered-design-note.md` 是 PR `#136` / `#137` 之前和实施过程中的交接材料。它们对 root cause、Codex CLI `total_tokens` 语义、以及 delivered-only 方向的判断仍有效；但其中“当前 Core 只有 `reasoning-fold` / `sum`”“unknown value 回落 `reasoning-fold`”等状态描述已经被 `0390c13c` 和 `dc11b24b` 覆盖。

`local/2026-07-04-codex-abnormal-reasoning-quality-selection-note.md` 与 `local/codex-degraded-reasoning-516-study-2026-07-04.md` 讨论的是另一个问题：多个候选响应之间最终交付谁。它们建议未来引入 special-aware `delivery-policy`，例如 `best-non-special` / `salvage-collapsed`；截至本文所述当前代码，这类 `delivery-policy` 尚未实现，不能把它当作当前 runtime 行为。

## Codex CLI 源码回读

以下结论来自本机 Codex CLI 源码回读，源码路径 `/Volumes/TP4000PRO/Program/codex`，回读 commit 为 `98d28aab54`。

### 上下文窗口和 auto compact 参数

Codex CLI 的 TOML 和 runtime config 中存在以下上下文管理参数：

- `model_context_window`：模型上下文窗口 override。
- `model_auto_compact_token_limit`：触发 auto compact 的 token usage 阈值。
- `model_auto_compact_token_limit_scope`：阈值适用范围。
- `compact_prompt`：compact 时使用的 prompt override。

源码位置：

- TOML 定义：`codex-rs/config/src/config_toml.rs:164`(`model_context_window`)、`:167`(`model_auto_compact_token_limit`)、`:171`(`model_auto_compact_token_limit_scope`)、`:244`(`compact_prompt`)
- runtime Config：`codex-rs/core/src/config/mod.rs:628`、`:631`、`:635`、`:709`

`AutoCompactTokenLimitScope` 只有两个值：

- `total`：完整 active context 都计入 auto compact limit。
- `body_after_prefix`：只把当前 compact window prefix 之后增长的部分计入 auto compact limit，同时仍保留 full context window 上限保护。

源码位置：`codex-rs/protocol/src/config_types.rs:24` 至 `:37`。

### 模型上下文窗口的计算

Codex 的 model metadata 里有这些相关字段：

- `context_window`
- `max_context_window`
- `auto_compact_token_limit`
- `effective_context_window_percent`

关键逻辑：

- `ModelInfo.resolved_context_window()` 使用 `context_window.or(max_context_window)`。
- `ModelInfo.auto_compact_token_limit()` 默认取 resolved context window 的 90%，如果显式配置了 `auto_compact_token_limit`，则 clamp 到 90% context window。
- `with_config_overrides()` 会用 `model_context_window` 覆盖 `ModelInfo.context_window`，但不超过 `max_context_window`。
- `TurnContext.model_context_window()` 会再乘 `effective_context_window_percent / 100`，得到 runtime effective context window。

源码位置：

- `codex-rs/protocol/src/openai_models.rs:391` 至 `:406`
- `codex-rs/protocol/src/openai_models.rs:437` 至 `:448`
- `codex-rs/models-manager/src/model_info.rs:23` 至 `:39`
- `codex-rs/core/src/session/turn_context.rs:188` 至 `:194`

### auto compact 的触发依据

Codex context status 通过 `sess.get_total_token_usage()` 获取 active context token 数，按 scope 计算 `auto_compact_scope_tokens` 和 limit：

- `total` scope：`active_context_tokens` 直接对比 `model_info.auto_compact_token_limit()`。
- `body_after_prefix` scope：用 `active_context_tokens - prefill_input_tokens` 对比配置 limit，同时检查 full context window limit。

当 `auto_compact_scope_tokens >= auto_compact_scope_limit` 或 full context window reached 时，`token_limit_reached = true`。turn 采样后，若模型需要 follow-up 或仍有 pending input 且 `token_limit_reached`，进入 `run_auto_compact(...)`。

源码位置：`codex-rs/core/src/session/context_window.rs:24` 至 `:91`、`codex-rs/core/src/session/turn.rs:304` 至 `:355`。

### Codex 真正关注的 usage 字段

Responses SSE 的 `response.completed.usage` 被映射为 `TokenUsage`：

- `input_tokens`
- `cached_input_tokens`
- `output_tokens`
- `reasoning_output_tokens`
- `total_tokens`

源码位置：`codex-rs/codex-api/src/sse/responses.rs:122` 至 `:145`。

真正进入上下文预算主路径的是 `last_token_usage.total_tokens`。`History.get_total_token_usage()` 以 `token_info.last_token_usage.total_tokens` 为基线，叠加本地尚未反映在 server usage 中的 items——而非将 `input_tokens + output_tokens + reasoning_output_tokens` 重新相加。（`codex-rs/core/src/context_manager/history.rs:326` 至 `:345`）

**对我们的启示**：客户端 usage 策略的关键是保证返回给 Codex 的 `usage.total_tokens` 不含被丢弃 abnormal attempt 的隐藏成本。`reasoning_tokens` 仅用于展示和诊断，不是 Codex auto compact 主触发字段。

### `x-reasoning-included` 的含义

Codex API SSE client 读取 `x-reasoning-included` header，表示 server 已将 reasoning tokens 计入 usage，client 不应再次估算。（`codex-rs/codex-api/src/sse/responses.rs:28`、`codex-rs/codex-api/src/common.rs:86` 至 `:89`）

这说明客户端 context manager 以 provider 报告的 `total_tokens` 为权威基线，不会自行把 usage detail 重新 fold。

### compact prompt

auto compact 使用 `compact_prompt` override，缺省时回退到内置 `SUMMARIZATION_PROMPT`。（`codex-rs/core/src/compact.rs:91` 至 `:103`）

## 当前算法设计

我们的防御目标是把”真实成本统计”和”客户端上下文压力”拆开：

1. 内部 usage statistics 必须记录每个已完成且产生 usage 的 attempt，包括 failed/intercepted abnormal attempt。若 speed hedge loser 被取消且没有走到 completed usage，则没有可记录的上游 usage，这属于并发 hedge 的可观测性边界。
2. 返回给 Codex CLI/App 的 `response.usage.total_tokens` 必须只代表最终 delivered/fallback attempt 对当前客户端上下文的贡献。
3. 需要诊断时可以暴露 summed detail，但不能让 summed detail 污染 Codex auto compact 主判断。

当前三种 `client-usage-aggregation` 模式定义在 `internal/config/config.go:321` 至 `:323`：

| 模式 | 客户端 usage 行为 | 适用场景 |
| --- | --- | --- |
| `delivered-only` | 直接返回最终 delivered/fallback attempt 的上游原始 `usage` | 默认模式，最适合 Codex CLI/App 生产流量 |
| `sum` | 对 input/cache/output/reasoning/total 逐字段相加 | 需要让客户端看见全部 retry 成本；可能增加 false context pressure |
| `sum-with-delivered-total` | 小项逐字段相加，但 `total_tokens` 保持 delivered/fallback attempt 的 `total_tokens` | 需要诊断 retry 小项成本，同时避免 Codex 提前 compact |

配置归一化在 `internal/config/config.go:444` 至 `:454`。空值、unknown value 和不支持的 legacy value 都回落到 `delivered-only`。

核心聚合逻辑在 `internal/runtime/executor/codex_abnormal_reasoning_retry.go`：

- `delivered-only`：`patchCodexAbnormalReasoningClientUsageWithSnapshot()` 直接 `return eventData`，不改客户端 payload（`:414` 至 `:417`）。
- `sum`：`addCodexUsageDetail(previous.Detail, current)` 对字段逐项相加（`:492` 至 `:515`）。
- `sum-with-delivered-total`：先复用 summed detail，再把 `TotalTokens` 覆盖为当前 delivered/fallback attempt 的 normalized total（`:496` 至 `:499`）。
- total fallback：`normalizeCodexUsageDetail()` 仅在上游缺失 `total_tokens` 时用 `input_tokens + output_tokens` 兜底，不额外加 `reasoning_tokens`，避免 output detail 重复计入 total（`:672` 至 `:679`）。

伪代码：

```text
current = delivered_or_fallback_attempt.usage
previous = discarded_abnormal_attempts.usage_snapshot

if aggregation == delivered-only:
    return upstream_client_payload_unchanged

summed = add_fields(previous, current)

if aggregation == sum:
    return usage(summed)

if aggregation == sum-with-delivered-total:
    summed.total_tokens = normalize(current).total_tokens
    return usage(summed)

return usage(normalize(current))
```

`sum-with-delivered-total` 的设计意图：允许 `reasoning_tokens` 等小项展示全部 retry 成本，但 `total_tokens` 锁定为最终交付 attempt 的数值，避免丢弃 attempt 变成 Codex context manager 的 active context 增量。该模式的 usage shape 可能出现 `input_tokens + output_tokens` 大于 `total_tokens`，因此只适合诊断或明确接受这种非标准形状的客户端；生产默认仍应使用全字段自洽的 `delivered-only`。

注意：`client-usage-aggregation` 只改变返回给客户端的 usage JSON，不改变 abnormal detection、retry budget、hedge 调度、fallback 选择，也不删除内部 attempt-level usage record。

## 当前实现地图

- Config schema 和默认值：`internal/config/config.go:318` 至 `:326`、`:381` 至 `:430`。
- Config normalization：`internal/config/config.go:433` 至 `:464`。
- Codex abnormal retry policy 和 error contract：`internal/runtime/executor/codex_abnormal_reasoning_retry.go:22` 至 `:58`。
- Client-visible usage patch：`internal/runtime/executor/codex_abnormal_reasoning_retry.go:414` 至 `:515`。
- Streaming final usage patch：`internal/runtime/executor/codex_abnormal_reasoning_retry.go:634` 至 `:669`。
- Attempt-level usage metadata：`sdk/cliproxy/auth/retry_without_penalty.go:22` 至 `:33`。
- Longest pass-through fallback candidate：`sdk/cliproxy/auth/retry_without_penalty.go:150` 至 `:197`。
- Quality hedge 主循环：`sdk/cliproxy/auth/hedged_retry_without_penalty.go:489` 至 `:652`。
- Quality winner score：`internal/runtime/executor/codex_abnormal_reasoning_retry.go:523` 至 `:528` 写入 `detail.OutputTokens`，`sdk/cliproxy/auth/hedged_retry_without_penalty.go:954` 至 `:984` 选择最高 score。
- LTS protected registry：`docs/lts/core-feature-contracts.yaml:157` 至 `:209`。

## 架构边界

### Client-visible usage 与 internal usage 分离

`response.usage` 是客户端协议面，影响 Codex CLI/App 的 context manager。内部 usage statistics 是审计和成本统计面，必须保存 attempt-level 事实。

因此 abnormal retry 的正确架构不是“隐藏成本”，而是“分面记录”：

- 客户端面：默认只告诉 Codex 本次最终交付内容对应的 usage，避免 false context pressure。
- 内部面：已完成的 failed/intercepted attempt 和 success/fallback attempt 分别写入 usage records，保留真实成本、模型、auth、failure reason 和 request metadata。没有 completed usage 的 canceled lane 不应被伪造统计。

### Streaming 与 non-streaming 一致

Codex abnormal retry 在 non-streaming final response 和 streaming finalizer 上遵守相同的 client usage aggregation 规则：`delivered-only` 下 streaming `response.completed` 的 usage 保持 delivered attempt 原始值；`sum-with-delivered-total` 下 streaming final usage 可展示 summed reasoning detail，但 `total_tokens` 仍锁定为 delivered/fallback total。

### `reasoning-fold` 已移除

旧的 `reasoning-fold` 把 reasoning tokens fold 到 output/total，与 Codex CLI 当前 usage 语义冲突，已移除。不再使用 fold helper，也不再回填 `FoldedOutputTokens` 到客户端 usage 路径。SDK 公开字段 `FoldedOutputTokens` 为兼容保留，但 Codex executor 的 client-visible usage 不应消费它。

### 候选质量选择仍是独立问题

当前 `hedged-retry.mode: quality` 的“quality”是实现层名称：等待同一 wave 已派发 lane，再按 executor 写入的 `detail.OutputTokens` 选最高分的非 abnormal 成功响应。`exhausted-behavior: pass-through` 的 fallback 也按 `OutputTokens` 选择最长 abnormal 响应。

7 月 4 日 local 复核材料表明，`max(output_tokens)` 不适合作为跨 special / non-special 候选的第一排序键：在 23 条 failed 后恢复的真实链里，直接按 `max(output_tokens)` 会选中 special attempt 18 次；更稳的未来设计应先按 `reasoning_tokens in {516,1034}` 区分 special，再只在同可信层级内用长度做 tie-break。该 special-aware `delivery-policy` 仍是后续设计，不属于本文记录的已落地 client usage 防线。

### Config、Panel、HFS 分工

- Core：实现三模式、归一化、payload patch、attempt-level usage records 和测试。
- Panel：展示并写入 `client-usage-aggregation` 枚举值。
- HFS：按部署场景选择 runtime config。Codex CLI/App 生产流量优先使用 `delivered-only`；需要观察 hidden retry 小项成本时使用 `sum-with-delivered-total`。

## 运维建议

| 场景 | 推荐模式 |
| --- | --- |
| Codex CLI/App 生产流量 | `delivered-only` |
| 诊断 retry 成本但避免 premature compact | `sum-with-delivered-total` |
| 成本完全外显（不推荐常规使用） | `sum` |

单纯增大 Codex CLI 的 `model_context_window` 或 `model_auto_compact_token_limit` 只能推迟阈值，不能消除 hidden retry cost 被误计为 active context 的根因。正确防线在代理侧：保证 client-visible `usage.total_tokens` 表达 delivered/fallback attempt，而非 discarded attempts 的累计成本。

## 验证点

Core 侧至少覆盖以下验证：

```bash
go test ./internal/config -run Codex
go test ./internal/runtime/executor -run TestCodexExecutorAbnormalReasoningRetry
scripts/check-lts-contract.sh
go build ./cmd/server
git diff --check
```

行为断言重点：

- `delivered-only` 返回 delivered/fallback attempt 的原始 usage。
- `sum` 对字段级 usage 相加。
- `sum-with-delivered-total` 的 `reasoning_tokens` 等小项可相加，但 `total_tokens` 必须保持 delivered/fallback total。
- 内部 attempt-level usage records 不因 client-visible aggregation 改变而丢失。
- streaming 和 non-streaming 的 final usage 语义一致。

HFS/live runtime 侧需额外确认：

- Space Variables 或挂载配置中的 `client-usage-aggregation` 实际值。
- runtime log 中加载的配置路径和 commit。
- 实际 Codex response sample 中 `usage.total_tokens` 是否保持 delivered/fallback attempt total。
- 若继续排查“重试后答案质量”而非 “auto compact”，还需另行抓取候选响应并验证是否存在 516/1034 special 被 `max(output_tokens)` 误选的场景；这不由 `client-usage-aggregation` 解决。
