# GLM 搜索后的 Codex 上下文计数

`upstream.codex.zcode-context-usage` 用于 Codex 客户端通过 `zcode-coding-plan` 使用 GLM 搜索时的上下文总量校正。Codex 用最近一次响应的 `usage.total_tokens` 判断是否压缩；上游搜索过程报告的累计用量可能高于下一轮实际回放的上下文。

```yaml
upstream:
  codex:
    zcode-context-usage: observe
```

| 模式 | 行为 |
| --- | --- |
| `disabled`（默认） | 沿用上游 usage，不额外计数。 |
| `observe` | 在已完成的搜索响应后调用一次原生 `count_tokens`，记录差异，保留客户端数值。 |
| `correct` | 同样执行计数，并用结果替换发给 Codex 的 `usage.total_tokens`。 |

设置可热重载。Panel 的「Codex 优化 → 上下文计数」提供同一配置。观察和校正都会增加一次上游计数请求，可能占用请求额度，并延后最终完成事件；生成中的文本仍正常流出。

## 计数范围和兼容性

校正同时要求 provider 为 `zcode-coding-plan`、输入和输出均为 Responses、客户端具有已识别的 Codex User-Agent 或 Originator、模型声明 BigModel 搜索回放协议，以及本次已完成响应中存在 `web_search_call`。普通客户端、无搜索请求、Chat Completions、原生 Messages 和未完成响应沿用原行为。

Core 保留本次已转换的 system、tools 和历史 messages，按现有回放规则追加实际交付的回答、搜索结果、思考和工具调用。计数不会添加未来用户消息或伪造工具结果。返回的 `input_tokens` 已经包含这些交付内容，校正时不再加一次输出 token。

后台 usage、导入导出、计费与缓存统计继续使用完整上游用量。`correct` 模式只改变 Codex 客户端的 `total_tokens`，因此客户端的 `input_tokens + output_tokens` 可能与 `total_tokens` 不同。这是专用于 Codex 压缩判断的兼容行为。

插件必须实现原生 `count_tokens`，复用同一账号、模型与传输路径，并携带已有的显式会话标识。HTTP 请求没有显式插件长会话标识时，插件沿用逐请求生成 session 的行为；计数载荷包含完整上下文。Core 等生成请求结束并释放插件并发名额后再计数；流结束等待和辅助调用合计最多 5 秒。计数载荷限制为 16 MiB。未知回放类型、无效计数、插件不支持、超时或取消均保留原值，不改用本地 tokenizer 估算。

## 观察与验收

`zcode context usage` 日志只记录模式、状态、上游总量、计数结果、客户端总量和耗时。成功观察为 `counted`，成功校正为 `corrected`；`count_failed`、`invalid_count`、`unsupported_replay`、`size_limit`、`stream_cleanup_incomplete` 和 `count_canceled_or_timed_out` 表示未应用校正。

先用 `observe` 比较有搜索与无搜索轮次，确认原生计数与下一轮实际输入规模相符，再启用 `correct`。原生计数接口本身的准确性及实际减少压缩的效果需要真实 GLM 会话验收；本地模拟测试覆盖转换、客户端范围、失败回退和后台用量隔离。

旧的 `upstream.codex.abnormal-reasoning-retry.client-usage-aggregation` 处理 Codex provider 的隐藏重试，不覆盖 zcode 搜索。Anthropic 的非缓存输入、缓存读取、缓存写入仍按现有协议求和；这里不全局减去缓存，也不按搜索次数除以用量。
