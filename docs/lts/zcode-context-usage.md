# GLM 搜索后的 Codex 上下文计数

`upstream.codex.zcode-context-usage` 用于 Codex 客户端通过 `zcode-coding-plan` 使用 GLM 搜索时的上下文总量校正。Codex 把最近一次 Responses 响应的 `usage.total_tokens` 作为上下文判断基线；GLM 搜索过程报告的累计用量可能高于下一轮实际回放的上下文。

```yaml
upstream:
  codex:
    zcode-context-usage: observe
```

| 模式 | 行为 |
| --- | --- |
| `disabled`（默认） | 保留当前转换结果，不额外计数。 |
| `observe` | 在已完成的搜索响应后调用一次原生 `count_tokens`，记录差异，保留客户端数值。 |
| `correct` | 同样执行计数，并用结果替换发给 Codex 的 `usage.total_tokens`。 |

设置可热重载。Panel 的「Codex 优化 → 上下文计数」下的「GLM 搜索上下文校正」提供同一配置。符合计数条件的观察和校正请求会增加一次上游计数调用，可能占用请求额度，并延后最终完成事件；生成中的文本仍正常流出。

## 两种 usage 协议与字段口径

Codex 读取的是 Responses usage。插件上游使用 Claude Messages，Core 先转换协议，再执行此项校正。原生 Responses 已有 `total_tokens`；当前 Claude 转换器读取输入、输出和缓存分项，按下式生成 Responses usage：

```text
input_tokens = Claude.input_tokens + cache_read_input_tokens + cache_creation_input_tokens
output_tokens = Claude.output_tokens
total_tokens = input_tokens + output_tokens
```

这是转换后的用量总和。它是否适合表示上下文，取决于上游报告的口径。`correct` 成功后只把 `total_tokens` 改为原生上下文计数，输入、输出、缓存分项仍保留转换后的上游用量。原生计数可以比原总量小、相等或更大，Core 不按差异方向裁剪结果。

## 计数范围和兼容性

校正同时要求 provider 为 `zcode-coding-plan`、输入和输出均为 Responses、客户端具有已识别的 Codex User-Agent 或 Originator、模型声明 BigModel 搜索回放协议，以及本次已完成响应中存在 `web_search_call`。普通客户端、无搜索请求、Chat Completions、原生 Messages 和未完成响应沿用原行为。

Core 保留本次已转换的 system、tools 和历史 messages，按 Codex 的回放格式追加交付的回答、思考和工具调用。Codex 的 `web_search_call` 不保留扩展 `results` 字段，辅助计数会先应用同样的字段投影，再走下一轮请求使用的结果恢复路径。原生计数返回的 `input_tokens` 已包含本轮可回放内容，校正时直接作为 total 使用。

搜索及 `webReader` 的结果恢复复用 `client.codex.web-search-result-cache`：

```yaml
client:
  codex:
    web-search-result-cache:
      enabled: true # 需要后续轮次保留搜索与网页正文时启用；默认 false。
      ttl: "24h"
      limit: 1024
```

缓存按插件、provider、账号、模型、调用方和会话隔离。流式响应在交付 `response.output_item.done` 前保存结果，下一轮在协议转换前恢复。缓存关闭、缺少会话标识或本轮结果未命中时，计数按缺失结果的回放内容进行；若生成期间的缓存变化影响已经恢复的输入历史，则跳过校正，保留原 total；已由客户端提供的结果保持原样。Home 模式沿用现有 KV 读写语义，失败时不读取本机旧副本。切换账号、模型或会话后不会继承原范围的结果。

后台 usage、导入导出、计费与缓存统计继续使用完整上游用量。`correct` 模式只改变 Codex 客户端的 `total_tokens`，因此客户端的 `input_tokens + output_tokens` 可能与 `total_tokens` 不同。这是专用于 Codex 压缩判断的兼容行为。

插件必须实现原生 `count_tokens`，复用同一账号、模型与传输路径，并携带已有的显式会话标识。HTTP 请求没有显式插件长会话标识时，插件沿用逐请求生成 session 的行为；计数载荷包含完整上下文。Core 等生成请求结束并释放插件并发名额后再计数；流结束等待和辅助调用合计最多 5 秒。计数载荷限制为 16 MiB。未知回放类型、无法确认客户端 reasoning 计数、无效计数、插件不支持、超时或取消均保留原值，不改用本地 tokenizer 估算。

## 观察与验收

`zcode context usage` 日志使用以下字段区分计数来源，不记录提示词或凭据：

| 字段 | 含义 |
| --- | --- |
| `original_response_total_tokens` | Claude → Responses 转换后、辅助计数前的 total。 |
| `counted_context_tokens` | 原生上下文计数；未取得有效计数时为 0。 |
| `context_count_delta_tokens` | 原生计数减去原 total，仅取得有效计数时出现，保留正负号。 |
| `client_total_tokens` | 最终发给 Codex 的 total。 |
| `client_total_source` | `translated_usage` 表示保留转换结果，`native_count` 表示采用原生计数。 |
| `context_count_applied` | 本次是否实际采用原生计数；观察模式为 false。 |

控制台和文件日志使用相同的字段格式，可通过日志中的 request ID 对应原请求。`mode`、`status` 和 `duration_ms` 记录模式、结果和耗时。旧字段 `provider_total_tokens` 保留为 `original_response_total_tokens` 的兼容别名，不能把它当作上游独立返回的原生 total。成功观察为 `counted`，成功校正为 `corrected`；`count_failed`、`invalid_count`、`unsupported_replay`、`size_limit`、`stream_cleanup_incomplete` 和 `count_canceled_or_timed_out` 表示未应用校正。`replay_changed` 表示输入历史的缓存结果在生成期间发生变化，无法沿用原计数基线。`client_reasoning_unconfirmed` 表示请求历史含有非空 reasoning 签名，Core 跳过校正和辅助计数，保留原 total。

Codex 还可能在最近一次 total 上追加本地新增内容和历史 reasoning 的估算。历史 `encrypted_content` 非空时，客户端可能额外估算签名承载的 reasoning；为避免将这类响应误报为已校正，`correct` 采用上述回退，`observe` 仍可记录原生计数。空签名历史继续支持校正。Core 不提前添加 `X-Reasoning-Included`，因为流式 headers 提交时尚不能确定辅助计数成功，计数失败路径也须保留原值。

先用 `observe` 比较有搜索与无搜索轮次，确认原生计数与实际回放内容相符，再启用 `correct`。比较下一轮输入时，应区分新增用户消息、工具结果和工具定义变化。验收覆盖连续搜索、网页读取、历史 reasoning 和缓存命中，分别记录日志中的原 total、原生计数、交付 total 和客户端上下文。

回放一致性应在同一账号、模型、会话和工具定义下比较，并先经过客户端字段解析。协议 total、客户端最终上下文和上游计费 usage 应分别记录；原生计数接口的准确性及实际减少压缩的效果需要真实 GLM/Codex 会话验收。

旧的 `upstream.codex.abnormal-reasoning-retry.client-usage-aggregation` 处理 Codex provider 的隐藏重试，不覆盖 zcode 搜索。Anthropic 的非缓存输入、缓存读取、缓存写入仍按现有协议求和；这里不全局减去缓存，也不按搜索次数除以用量。
