# Codex Client Context Degradation Defense

CPA-Core-LTS 针对 Codex 上游 reasoning 截断（516/1034）的多层防御机制。

本文件不新增 LTS contract marker；受保护能力以 `docs/lts/core-feature-contracts.yaml` 中的 `codex-abnormal-reasoning-retry` 为准。

文档结构：

- **简介 + 配置指南**：面向运维和配置用户，说明这是什么、怎么配。
- **架构与实现**：面向开发者，说明设计原则、算法细节和代码位置。
- **附录**：面向存档和 AI，记录背景研究、Codex CLI 源码分析和版本演进。

---

## 简介

上游有时将 Codex reasoning tokens 截断到固定异常值（当前主观察值为 516/1034），导致模型推理链严重缩短、输出质量骤降。这种现象被称为"降智"。

CPA-Core-LTS 提供三层防御：

1. **Abnormal reasoning retry**（第一道防线）：识别可疑响应（reasoning tokens 精确命中 516/1034），丢弃异常 attempt 后透明重试，支持并发 hedge lane。
2. **Delivery / fallback policy**：控制最终交付哪条候选——mixed pool（同时有正常和异常候选）优先 non-special；all-special 且 retry 耗尽时按 fallback 策略兜底。
3. **Client usage shaping**（第二道防线）：将 internal usage accounting 和 client-visible context accounting 分面处理。被丢弃 attempt 的 hidden cost 不暴露给 Codex CLI 的 `usage.total_tokens`，避免客户端误判为 active context 增量而提前触发 auto compact。

核心原则：

- 内部 usage statistics 记录所有 attempt（含被丢弃的 abnormal attempt）。
- 返回给 Codex CLI/App 的 `response.usage.total_tokens` 默认只代表最终交付 attempt 对客户端上下文的贡献。

---

## 配置指南

本节面向配置用户和运维人员。若只想使用推荐组合，直接看"推荐组合"和"推荐 YAML"；若要理解每个字段的取舍，再看后续字段详解。

### 推荐组合

| 目标 | `action` | `max-retries` | `exhausted-behavior` | `delivery-policy` | `fallback-policy` | `client-usage-aggregation` | `hedged-retry.mode` | 说明 |
| --- | --- | ---: | --- | --- | --- | --- | --- | --- |
| 推荐（默认） | `retry` | `2` | `pass-through` | `best-non-special` | `best-special` | `sum-with-delivered-total` | `quality` | 兼顾质量、兜底和 Codex compact；客户端能看到 retry 小项成本，但 `total_tokens` 不累加 hidden retry。 |
| 最标准 OpenAI-compatible shape | `retry` | `2` | `pass-through` | `best-non-special` | `best-special` | `delivered-only` | `quality` | 全字段 usage 自洽；客户端看不到 hidden retry 小项成本。 |
| 灰度观察 | `observe-only` | 任意 | 任意 | 任意 | 任意 | 任意 | 任意 | 只记录命中日志，不重试、不改响应；streaming 不会为了观察而完整缓冲。 |
| 快速止损关闭 | `disabled` | 任意 | 任意 | 任意 | 任意 | 任意 | 任意 | 完全停用检测；建议同时写 `enabled: false`，避免旧配置面误解。 |
| 低延迟优先 | `retry` | `1` 或 `2` | `pass-through` | `first-non-special` | `best-special` | `delivered-only` 或 `sum-with-delivered-total` | `speed` | 第一条 non-special 成功即交付；牺牲同 wave 质量比较。 |
| 完整性实验 | `retry` | `2` | `pass-through` | `max-output` | `max-output-special` | `sum-with-delivered-total` | `quality` | 允许 516/1034 凭长度反选，风险较高，不建议生产默认。 |

### 推荐 YAML

```yaml
codex:
  abnormal-reasoning-retry:
    enabled: true
    action: retry
    reasoning-efforts:
      - xhigh
    reasoning-tokens:
      - 516
      - 1034
    auth-kinds:
      - oauth
    stream-buffer: true
    stream-buffer-max-bytes: 0
    max-retries: 2
    exhausted-behavior: pass-through
    delivery-policy: best-non-special
    fallback-policy: best-special
    client-usage-aggregation: sum-with-delivered-total
    hedged-retry:
      enabled: true
      hedge-delay-ms: 1000
      require-distinct-auth: true
      mode: quality
```

多实例部署时可为不同实例配置不同的 `model-contains`，按需缩窄或扩大命中范围。

### 字段详解：顶层开关与命中范围

| 字段 | 默认/推荐 | 可选值 | 如何选择 | 注意 |
| --- | --- | --- | --- | --- |
| `enabled` | 默认 `false` | `true` / `false` | 新配置建议与 `action` 同步：`action: retry` 写 `enabled: true`，`disabled` 写 `false` | 历史入口；不要只改 `enabled` 而忘记 `action`。 |
| `action` | 推荐 `retry` | `retry` / `observe-only` / `disabled` | 生产选 `retry`；灰度选 `observe-only`；关闭选 `disabled` | `observe-only` 只打日志不重试不改响应。 |
| `model-contains` | 代码默认 `gpt-5.5` | 字符串列表 | 建议先用精确子串小范围验证，确认后再扩大 | 过宽影响不需要防线的模型；过窄漏掉流量。 |
| `reasoning-efforts` | 推荐 `xhigh` | 字符串列表；空列表不限 | 当前证据主要来自 xhigh | 空列表会扩大到所有 effort，可能误伤。 |
| `reasoning-tokens` | `516, 1034` | 正整数列表 | 保持精确值；不要用"低于某阈值" | `reasoning_tokens == 0` 不等于降智。 |
| `auth-kinds` | `oauth` | 字符串列表 | Codex OAuth 路径选 `oauth` | 放开到 API key 可能影响非 Codex upstream。 |
| `auth-ids` | 空（不限） | auth id 列表 | 灰度时填少量；稳定后留空 | 用于安全放量。 |

### 字段详解：行为控制

| 字段 | 推荐值 | 可选值 | 如何选择 | 注意 |
| --- | --- | --- | --- | --- |
| `max-retries` | `2` | `0` 或正整数 | 有粘性 516 链，推荐 `2` | 太高烧钱增延迟；`0` 直接进入耗尽行为。 |
| `exhausted-behavior` | `pass-through` | `error` / `pass-through` | 全 special 也想给用户响应选 `pass-through` | `error` 更保守但体验更差。 |

`exhausted-behavior` 详解:

| 值 | 行为 | 与 `fallback-policy` 的关系 |
| --- | --- | --- |
| `error` | retry 耗尽后返回错误 | `fallback-policy` 不生效 |
| `pass-through` | 从 retained special fallback 中选一条交付 | `fallback-policy` 决定选哪条 |

### 字段详解：交付策略（mixed pool）

mixed pool 指候选池里同时有 special（516/1034）和 non-special 成功响应。`delivery-policy` 只在这里生效。

| `delivery-policy` | 选择规则 | 推荐程度 | 风险 |
| --- | --- | --- | --- |
| `best-non-special` | 有 non-special 就不交付 special；non-special 内再比 score / visible tokens | **默认推荐** | 可能交付更短但更可信的 non-special。 |
| `first-non-special` | 第一条 non-special 成功即交付 | 可选（低延迟） | 可能错过更好的 non-special。 |
| `max-output` | 允许 special 凭更大 `output_tokens` 反选 non-special | 实验 | 最容易放回"自信错答案"。 |
| `latest` | 选最后完成的成功候选 | 调试 | "最后完成"没有质量证据。 |

### 字段详解：Fallback 策略（all-special 耗尽）

all-special 指 retry 耗尽后所有候选都是 special。只有 `exhausted-behavior: pass-through` 时 `fallback-policy` 才生效。

| `fallback-policy` | 选择规则 | 推荐程度 | 风险 |
| --- | --- | --- | --- |
| `best-special` | 优先 1034 > 516，再比 output / visible tokens | **默认推荐** | 1034 优于 516 是机制推断，样本量较少。 |
| `max-output-special` | 选 `output_tokens` 最大的 special | 实验 | 长不等于对。 |
| `latest-special` | 选最新保留的 special | 调试 | 没有证据说更好。 |

### 字段详解：Client Usage 策略

| `client-usage-aggregation` | 小项 | `total_tokens` | 推荐 | 风险 |
| --- | --- | --- | --- | --- |
| `delivered-only` | 最终 attempt 原始值 | 最终 attempt 原始值 | **Core 默认** | 看不到 hidden retry 成本。 |
| `sum` | 所有 attempt 相加 | 所有 attempt 相加 | 不推荐 | 会触发 Codex premature compact。 |
| `sum-with-delivered-total` | 所有 attempt 相加 | 最终 attempt 值 | 可选（诊断） | usage shape 非标准，小项可大于 total。 |

`sum-with-delivered-total` 适用于需要观察 hidden retry 成本、但又不希望触发 Codex premature compact 的场景。Codex compact 主要看 `total_tokens`，该模式将小项成本可见和 context pressure 隔离为两套独立语义。

### 字段详解：Streaming 缓冲

| 字段 | 推荐值 | 可选值 | 注意 |
| --- | --- | --- | --- |
| `stream-buffer` | `true` | `true` / `false` | retry 防线需要打开；`observe-only` 不使用完整缓冲。 |
| `stream-buffer-max-bytes` | `0` | `0` 或正整数 | `0` 不限；超限会 flush 并禁用本次检测。 |

保留 streaming 体验但观察命中用 `action: observe-only`，不要靠 `stream-buffer: false` 达成。

### 字段详解：Hedged Retry

| 字段 | 推荐值 | 可选值 | 作用 | 注意 |
| --- | --- | --- | --- | --- |
| `hedged-retry.enabled` | `true` | `true` / `false` | 是否启用并发 lane | 增加并发成本。 |
| `hedge-delay-ms` | `1000` | `0` 或正整数 | primary 后多久启 second lane | 太小更贵，太大更接近串行。 |
| `require-distinct-auth` | `true` | `true` / `false` | hedge lane 要求不同 auth | auth 池太小可能无法派发 second lane。 |
| `mode` | `quality` | `speed` / `quality` | 调度等待策略（不是 winner policy） | `speed` first-success-wins；`quality` 等待后按 `delivery-policy` 选。 |

常用组合:

| 目标 | `enabled` | `delay-ms` | `distinct-auth` | `mode` |
| --- | --- | ---: | --- | --- |
| 推荐（默认） | `true` | `1000` | `true` | `quality` |
| 更强比较 | `true` | `0` | `true` | `quality` |
| 低延迟 | `true` | `1000` | `true` | `speed` |
| 最省成本 | `false` | — | — | — |

### 配置关系速查

| 配置关系 | 结论 |
| --- | --- |
| `delivery-policy` vs `fallback-policy` | 前者只管 mixed pool；后者只管 all-special exhausted pass-through。 |
| `delivery-policy` vs `hedged-retry.mode` | 前者管 winner 比较；后者管调度等待。不能互相覆盖。 |
| `exhausted-behavior: error` vs `fallback-policy` | `error` 下不交付 fallback，`fallback-policy` 不生效。 |
| `action: observe-only` vs `max-retries` | observe-only 不重试，`max-retries` 无实际影响。 |
| `client-usage-aggregation` vs internal usage | 只改客户端 JSON；内部 attempt-level usage 不变。 |
| `sum-with-delivered-total` vs OpenAI shape | 有意非标准；换取 retry 成本可见且不误触发 compact。 |

### 运维速查

| 场景 | 推荐 |
| --- | --- |
| 最标准 OpenAI-compatible usage | `client-usage-aggregation: delivered-only` |
| 展示 retry 成本但避免 premature compact | `client-usage-aggregation: sum-with-delivered-total` |
| mixed pool 生产默认 | `delivery-policy: best-non-special` |
| all-special pass-through 默认 | `fallback-policy: best-special` |
| 灰度观察 / 降风险回滚 | `action: observe-only` |
| 完全关闭 | `action: disabled` + `enabled: false` |

单纯增大 Codex CLI 的 `model_context_window` 或 `model_auto_compact_token_limit` 只能推迟阈值，不能消除 hidden retry cost 被误计为 active context 的根因。正确防线在代理侧。

---

## 架构与实现

本节面向开发者，说明设计原则、算法实现和代码位置。

### 设计原则

#### Client-visible usage 与 internal usage 分离

`response.usage` 是客户端协议面，影响 Codex CLI/App 的 context manager。内部 usage statistics 是审计面，必须保存 attempt-level 事实。

abnormal retry 的架构是"分面记录"而非"隐藏成本"：

- **客户端面**：默认只报最终交付 attempt 的 usage，避免 false context pressure。
- **内部面**：已完成的 failed/intercepted attempt 和 success/fallback attempt 分别写入 usage records。没有 completed usage 的 canceled lane 不应被伪造统计。

#### Streaming 与 non-streaming 一致

non-streaming final response 和 streaming finalizer 遵守相同的 client usage aggregation 规则。

#### 候选质量选择是独立配置层

`hedged-retry.mode` 只决定调度（`speed` 不等待，`quality` 等待同 wave 已派发 lane），不隐含 winner policy。`delivery-policy` 管 mixed pool winner 比较，`fallback-policy` 管 all-special exhausted 兜底。两层独立。

`salvage-collapsed`、`judge`、`consensus`、内容 validator 等更复杂候选策略没有进入 V6，不能伪装成当前 runtime 已支持的策略。

#### `reasoning-fold` 已移除

旧的 `reasoning-fold` 把 reasoning tokens fold 到 output/total，与 Codex CLI usage 语义冲突，已移除。SDK 公开字段 `FoldedOutputTokens` 为兼容保留，但 Codex executor 的 client-visible usage 不应消费它。

#### Core 与 Panel 分工

- **Core**：实现三模式、归一化、payload patch、attempt-level usage records 和测试。
- **Panel**：展示并写入枚举值（CPA-Panel-LTS 已合并对应支持）。
- **部署侧**：按实际场景选择 runtime config 组合。

### 算法设计

防御目标：

1. 内部 usage statistics 记录每个已完成且产生 usage 的 attempt（含 failed/intercepted）。若 speed hedge loser 被取消且没有走到 completed usage，则没有可记录的上游 usage。
2. 返回给 Codex CLI/App 的 `response.usage.total_tokens` 只代表最终 delivered/fallback attempt 对客户端上下文的贡献。
3. 诊断时可以暴露 summed detail，但不能污染 Codex auto compact 主判断。

#### action 模式

`action` 模式定义在 `internal/config/config.go:319` 至 `:321`：

| 模式 | 行为 |
| --- | --- |
| `retry` | 命中后生成 retry-without-penalty error，按 retry budget、hedge、delivery/fallback 策略处理 |
| `observe-only` | 只记录命中日志，原样返回响应；streaming 不完整缓冲，仍在 completed 后记录命中 |
| `disabled` | 关闭检测和重试 |

`enabled` 作为历史入口保留；未设置 `action` 时，`enabled: true` 归一为 `retry`，`enabled: false` 归一为 `disabled`。

#### delivery / fallback / usage 策略

`delivery-policy`（mixed pool only）：`best-non-special` 默认先按 special/non-special 分层，避免 516/1034 凭 `output_tokens` 反杀 non-special。`max-output` 是显式 opt-in。

`fallback-policy`（all-special + pass-through only）：`best-special` 是耗尽兜底，不代表 special 被重新定义为健康响应。

`client-usage-aggregation` 三种模式定义在 `internal/config/config.go:324` 至 `:326`：`delivered-only`（默认，全字段自洽）、`sum`（全量相加）、`sum-with-delivered-total`（小项相加但 total 锁定）。

#### 聚合逻辑

核心聚合在 `internal/runtime/executor/codex_abnormal_reasoning_retry.go`：

- **`delivered-only`**：`patchCodexAbnormalReasoningClientUsageWithSnapshot()` 直接 `return eventData`（`:466` 至 `:469`）。
- **`sum`**：`codexAbnormalReasoningRetryAggregateClientUsage()` 调用 `addCodexUsageDetail()` 对字段逐项相加（`:544` 至 `:568`）。
- **`sum-with-delivered-total`**：先复用 summed detail，再把 `TotalTokens` 覆盖为 delivered/fallback attempt 的 normalized total（`:548` 至 `:551`）。
- **total fallback**：`normalizeCodexUsageDetail()` 仅在上游缺失 `total_tokens` 时用 `input_tokens + output_tokens` 兜底，不额外加 `reasoning_tokens`（`:742` 至 `:749`）。

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

`client-usage-aggregation` 只改返回给客户端的 usage JSON，不改变 abnormal detection、retry budget、hedge 调度、fallback 选择，也不删除内部 attempt-level usage record。

#### 配置归一化

归一化逻辑在 `internal/config/config.go:453` 至 `:530`。空值、unknown value 和 legacy value 按默认策略回落：`action` 从 `enabled` 推导，`client-usage-aggregation` 回落 `delivered-only`，`delivery-policy` 回落 `best-non-special`，`fallback-policy` 回落 `best-special`，`hedged-retry.mode` 回落 `quality`。

### 实现地图

| 组件 | 文件 | 行号 |
| --- | --- | --- |
| Config schema 和默认值 | `internal/config/config.go` | `:318` 至 `:451` |
| Config normalization | `internal/config/config.go` | `:453` 至 `:530` |
| Policy 和 error contract | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:22` 至 `:173` |
| Policy constructor（config → runtime） | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:175` 至 `:232` |
| Retry / observe-only 分支 | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:238` 至 `:321` |
| Client-visible usage patch | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:458` 至 `:568` |
| Candidate policy metadata | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:575` 至 `:603` |
| Streaming recorder 和 final usage patch | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:624` 至 `:740` |
| Total tokens fallback | `internal/runtime/executor/codex_abnormal_reasoning_retry.go` | `:742` 至 `:749` |
| Attempt-level usage metadata | `sdk/cliproxy/auth/retry_without_penalty.go` | `:28` 至 `:39` |
| Hedge policy 从 error 读取 | `sdk/cliproxy/auth/retry_without_penalty.go` | `:237` 至 `:278` |
| Mixed pool special fallback 反选 | `sdk/cliproxy/auth/retry_without_penalty.go` | `:607` 至 `:627` |
| Quality hedge 主循环 | `sdk/cliproxy/auth/hedged_retry_without_penalty.go` | `:489` 至 `:659` |
| Quality winner 选择 | `sdk/cliproxy/auth/hedged_retry_without_penalty.go` | `:966` 至 `:1018` |
| Candidate 比较器 | `sdk/cliproxy/auth/hedged_retry_without_penalty.go` | `:1206` 至 `:1237` |
| LTS protected registry | `docs/lts/core-feature-contracts.yaml` | `:157` 至 `:222` |

### 验证

#### Core

```bash
go test ./internal/config -run Codex
go test ./internal/runtime/executor -run TestCodexExecutorAbnormalReasoningRetry
go test ./sdk/cliproxy/auth -run 'RetryWithoutPenalty|Hedged'
scripts/check-lts-contract.sh
go build ./cmd/server
git diff --check
```

行为断言：

- `delivered-only` 返回 delivered/fallback attempt 的原始 usage。
- `sum` 对字段级 usage 相加。
- `sum-with-delivered-total` 的小项可相加，但 `total_tokens` 必须保持 delivered/fallback total。
- `best-non-special` 在 mixed pool 中不让 longer special 反选 non-special。
- `max-output` 只有显式 opt-in 时才允许 special 在 mixed pool 中按 output 反选。
- `best-special` 在 all-special pass-through 时优先 1034，再比 output / visible tokens。
- `hedged-retry.mode: speed` 保持 first-success-wins，不被 `delivery-policy` 改写。
- `observe-only` streaming 不缓冲到 completed，但仍在 completed usage 解析后记录命中日志。
- 内部 attempt-level usage records 不因 client-visible aggregation 改变而丢失。
- streaming 和 non-streaming 的 final usage 语义一致。

#### Panel

```bash
npm run check:lts
npm run type-check
npm run lint
npm run build
npm run smoke:lts
git diff --check
```

行为断言：

- visual editor 能展示并保存 `action`、`delivery-policy`、`fallback-policy`、`client-usage-aggregation`。
- 保存时以 `action` 为权威，同步写旧 `enabled` 布尔字段。
- 旧 Panel 不应在新 Core 配置上做 visual save；先升级 Panel 再改部署配置。

#### Live runtime

- 部署环境变量或挂载配置中的实际值。
- runtime log 中加载的配置路径、Core release tag/commit 或响应头 `x-cpa-commit`。
- 实际 Codex response sample 中 `usage.total_tokens` 是否保持 delivered/fallback attempt total。
- `sum-with-delivered-total` 下小项合计大于 `total_tokens` 是正常诊断语义，不是聚合错误。

---

## 附录

本节包含背景研究、Codex CLI 源码分析和版本演进，主要用于存档和 AI 上下文。

### 背景研究：516/1034 现象

上游有时会将 reasoning tokens 截断到固定值（典型如 516/1034），导致模型推理链严重缩短、输出质量骤降。基于 535 行 usage 事件和探测脚本的复核结论：

- 84 条 failed 记录全部精确等于 516（80 条）或 1034（4 条）。
- gpt-5.5 xhigh 16 路糖果题探测中，reasoning=516 的 3 条样本 0/3 全错，非 516 的 13 条 13/13 全对。
- 23 条 failed 后恢复的真实链里，直接按 `max(output_tokens)` 会选中 special attempt 18 次。

边界条件：

- `reasoning_tokens == 0` 不等于降智。它常见于工具步、秒回或不需要深推理的普通 turn；用"低 reasoning 阈值"拦截会误伤。
- `output_tokens` 包含 hidden reasoning，`visible = output_tokens - reasoning_tokens`。可见输出长短不能单独代表质量；516 样本甚至可能比健康回答的可见输出更长。
- probe 的 3 条 516 样本提供的是质量 ground truth，不等同于"当时生产 Core 一定已经拦截了 probe"。"probe 为何未被拦截"属于部署/config open issue，需要用 `x-cpa-commit`、runtime config 和真实代理路径另行验证。

### Codex CLI 源码回读

以下结论来自 Codex CLI 源码回读（基于 commit `98d28aab54`）。

#### 上下文窗口和 auto compact 参数

Codex CLI 的 TOML 和 runtime config 中存在以下上下文管理参数：

- `model_context_window`：模型上下文窗口 override。
- `model_auto_compact_token_limit`：触发 auto compact 的 token usage 阈值。
- `model_auto_compact_token_limit_scope`：阈值适用范围（`total` / `body_after_prefix`）。
- `compact_prompt`：compact 时使用的 prompt override。

源码位置：

- TOML 定义：`codex-rs/config/src/config_toml.rs:164`、`:167`、`:171`、`:244`
- runtime Config：`codex-rs/core/src/config/mod.rs:628`、`:631`、`:635`、`:709`
- scope 枚举：`codex-rs/protocol/src/config_types.rs:24` 至 `:37`

#### 模型上下文窗口的计算

- `ModelInfo.resolved_context_window()` 使用 `context_window.or(max_context_window)`。
- `ModelInfo.auto_compact_token_limit()` 默认取 resolved context window 的 90%，显式配置时 clamp 到 90%。
- `with_config_overrides()` 用 `model_context_window` 覆盖 `ModelInfo.context_window`，不超过 `max_context_window`。
- `TurnContext.model_context_window()` 再乘 `effective_context_window_percent / 100`。

源码位置：

- `codex-rs/protocol/src/openai_models.rs:391` 至 `:406`、`:437` 至 `:448`
- `codex-rs/models-manager/src/model_info.rs:23` 至 `:39`
- `codex-rs/core/src/session/turn_context.rs:188` 至 `:194`

#### auto compact 的触发依据

Codex context status 通过 `sess.get_total_token_usage()` 获取 active context token 数：

- `total` scope：`active_context_tokens` 直接对比 `model_info.auto_compact_token_limit()`。
- `body_after_prefix` scope：用 `active_context_tokens - prefill_input_tokens` 对比配置 limit，同时检查 full context window limit。

当 `auto_compact_scope_tokens >= auto_compact_scope_limit` 或 full context window reached 时，`token_limit_reached = true`，进入 `run_auto_compact(...)`。

源码位置：`codex-rs/core/src/session/context_window.rs:24` 至 `:91`、`codex-rs/core/src/session/turn.rs:304` 至 `:355`。

#### Codex 关注的 usage 字段

Responses SSE 的 `response.completed.usage` 映射为 `TokenUsage`：`input_tokens`、`cached_input_tokens`、`output_tokens`、`reasoning_output_tokens`、`total_tokens`。

真正进入上下文预算主路径的是 `last_token_usage.total_tokens`。`History.get_total_token_usage()` 以此为基线，叠加本地尚未反映在 server usage 中的 items——而非将小项重新相加。

源码位置：`codex-rs/codex-api/src/sse/responses.rs:122` 至 `:145`、`codex-rs/core/src/context_manager/history.rs:326` 至 `:345`。

**对我们的启示**：客户端 usage 策略的关键是保证 `usage.total_tokens` 不含被丢弃 abnormal attempt 的隐藏成本。`reasoning_tokens` 仅用于展示和诊断，不是 auto compact 主触发字段。

#### `x-reasoning-included` 的含义

Codex API SSE client 读取 `x-reasoning-included` header，表示 server 已将 reasoning tokens 计入 usage，client 不应再次估算。客户端 context manager 以 provider 报告的 `total_tokens` 为权威基线，不自行 fold。

源码位置：`codex-rs/codex-api/src/sse/responses.rs:28`、`codex-rs/codex-api/src/common.rs:86` 至 `:89`。

#### compact prompt

auto compact 使用 `compact_prompt` override，缺省时回退到内置 `SUMMARIZATION_PROMPT`。

源码位置：`codex-rs/core/src/compact.rs:91` 至 `:103`。

### 版本历史

版本历史基于 `main` 分支的 `git log`、`git show` 和 GitHub PR 记录，时间均为北京时间。该算法经历了 7 个落地阶段：serial retry → attempt-level accounting → exhaustion controls → speed hedge → quality/fold 聚合 → client usage aggregation 分离 → special-aware delivery/fallback policy。

| 版本 | 时间 | 证据 | 算法变化 | 当前结论 |
| --- | --- | --- | --- | --- |
| V1 serial retry | 2026-06-30 | `1e6d428b` | 新增 Codex abnormal reasoning guard：按 `model-contains`、`reasoning-tokens`、`auth-kinds` 等条件识别可疑响应，丢弃 abnormal attempt 并透明重试。 | 奠定异常识别和重试主路径。 |
| V1.5 accountable retry | 2026-06-30 至 2026-07-01 | `88cf5c61`、`877a8033`、`929056e4` | 补齐 discarded attempt 的 internal usage accounting；增加 `reasoning-efforts` 过滤；增加独立 `max-retries` 和 `exhausted-behavior: error/pass-through`。 | 从"只重试"升级为可审计、可限额、可配置耗尽行为的 retry-without-penalty 机制。 |
| V2 speed hedge | 2026-07-02 | `6af7fc59` | 新增 hedged retry：异常重试路径先启动 primary lane，`hedge-delay-ms` 后可启动 second lane；默认 first success wins；支持 `require-distinct-auth`。 | 用并发 lane 降低异常链路尾延迟，但仍以速度优先。 |
| V3 quality hedge + reasoning-fold | 2026-07-03 | PR `#134` / merge `809acd08`；commits `c73d5610`、`4e954156` | 新增 `hedged-retry.mode: speed/quality`；quality 模式等待同一波已派发 lane，并按最大成功 `output_tokens` 选胜者；新增 `client-usage-aggregation: reasoning-fold/sum`；修复 streaming quality usage 在 OpenAI Chat、Claude、Gemini 下游格式中的折算。 | 第一版"质量优先"算法，但 `reasoning-fold` 后来被证明与 Codex CLI usage 语义冲突。 |
| V4 default quality + longest fallback | 2026-07-03 | PR `#135` / merge `c16b22a8`; commit `b7546294` | 将 `hedged-retry.mode` 默认和非法值归一从 `speed` 改为 `quality`；`exhausted-behavior: pass-through` 时选择 retry 链内 `output_tokens` 最大的 abnormal fallback。 | 质量优先成为默认语义；耗尽时也尽量返回最长可用 fallback。 |
| V5 separated client usage | 2026-07-04 | PR `#136` / merge `c269724d`; PR `#137` / merge `c63986f8`; commits `0390c13c`、`dc11b24b` | 将 `client-usage-aggregation` 收敛为 `delivered-only`、`sum`、`sum-with-delivered-total`；默认 `delivered-only`；删除旧 `reasoning-fold` 正式行为和 alias；`total_tokens` 缺失时只用 `input_tokens + output_tokens` 兜底。 | client usage 防线定型：真实成本进入 internal usage，客户端 context pressure 默认只看 delivered/fallback attempt。 |
| V6 delivery/fallback policy | 2026-07-05 | PR `#138` / merge `961d2a2c`; commits `0fb2d0cb`、`a4b25cc6`、`8cf93478`; release tag `v1-tls-0.0.8` | 新增 `action: retry/observe-only/disabled`、`delivery-policy`、`fallback-policy`；`delivery-policy` 只管 mixed pool，all-special 仍由 `exhausted-behavior + fallback-policy` 决定；`hedged-retry.mode` 只保留调度语义；修复 observe-only streaming 不应缓冲到 completed 的语义。 | 当前主线策略：best-non-special 阻止 516/1034 凭长度反杀，best-special 为全 special 耗尽链提供兜底。 |

#### 不计入当前主线的线索

PR `#89`、`#90`、`#108`、`#109` 涉及 compact evidence、invalid encrypted reasoning retry、compact routing controls、compact defer loading 等方向。这些是 2026-05-31 至 2026-06-01 的 closed draft PR，未 merge 到 `main`，仅作为上下文问题的早期线索，不计入当前算法版本。
