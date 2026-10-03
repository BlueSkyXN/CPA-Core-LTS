# Daybreak Blue usage metadata

`response_cyber_program` 是 canonical v3 usage request detail 的可选字段，记录上游**实际回显**的网络安全访问程序，不代表请求意图或账号资格。Management `/usage`、`/usage/export`、`/usage/import`、`/usage/query/details` 与 usage queue 使用相同字段；SDK `usage.Record` / `usage.Detail` 和插件 `UsageRecord` 使用 `ResponseCyberProgram`。

| 值 | 含义 |
| --- | --- |
| `daybreak_blue` | 上游响应 `access_programs.cyber` 为 `daybreak_blue` |
| `standard` | 上游响应明确返回 `cyber: standard`，或公开 Responses API 文档定义的 `access_programs: null` |
| `unknown` | 上游有该字段，但值不受支持或形状无法识别；包括本次不支持的 Red |
| absent | 上游未回显、历史记录或该路径未提供证据；不能推断为 Standard |

Core 从 OpenAI-compatible JSON、SSE 和 Codex HTTP / WebSocket 的响应提取该字段。流式 created/in_progress 回显在后续事件省略时保留；terminal 明确值优先。即使没有 token usage，也保留已观察到的访问程序。附属 image-tool model 不继承主模型的程序。

不根据模型名、`-blue` 后缀、OAuth 账号或请求参数猜测程序。显式 Blue 请求被拒绝且没有成功响应回显时，记录保持未知。一个已确认程序的响应随后流式失败，可以同时有程序字段和 `failed: true`，不代表请求最终成功。

该字段不改动 `service_tier` / request/outbound/response/effective tier、速度筛选、Fast 计费、token、timing、auth attribution 或 usage dedup identity。旧 canonical v3 数据继续导入，新字段可被旧消费者忽略；不升级 export version，也不增加统计持久化。

Panel 同列显示 `STD / BLUE / FAST / BLUE FAST`，未知程序的速度标签带 `?`，悬浮说明证据。速度沿用现有 tier resolution 及其证据优先级（含未知速度的旧有估算），Blue 本身不产生计费倍率。Core 与新版 Panel 均升级后，新请求才会显示 Blue；旧记录不能回填猜测。

本功能不做资格检测，不注入请求参数，不修改 auth file，也不提供 Red 专属支持。

依据：[OpenAI Daybreak — Check the response / Understand defaults when omitted](https://developers.openai.com/api/docs/guides/daybreak#check-the-response)。
