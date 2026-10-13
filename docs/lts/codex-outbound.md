# Codex 出站策略（`codex.outbound`）

`codex.outbound` 集中控制 Codex 请求怎样发往上游：传输方式、请求体压缩、Accept-Encoding。三项都可单独配置；设计原则与默认值的取舍见第 0 节。

```yaml
codex:
  outbound:
    transport: follow                 # follow | websocket | auto
    websocket-threshold-bytes: 786432 # 仅 auto 模式使用
    request-compression: auto         # auto | none
    accept-encoding: official         # official | legacy

codex-api-key:
  - api-key: "..."
    base-url: "https://relay.example.com/v1"
    request-compression: none         # none（默认）| zstd
```

非法取值（未知枚举、负阈值）会让配置加载失败，与 `codex.cache-affinity` 一致，避免拼写错误被静默回退。修改后经热重载生效，watcher 日志输出 `codex.outbound.<key>: A -> B` 变更摘要。

## 0. 设计原则：对齐官方 Codex 客户端

Codex OAuth credential 发往 chatgpt.com Codex backend 的出站行为，以官方 Codex 客户端直连时的行为为准。这条路径本来就以官方客户端身份发出请求（User-Agent / Originator cloaking），线路特征与官方不一致时，既可能触发上游只针对非官方特征的问题，也会让排障时无法拿官方客户端做对照。

边界：

- 只约束 Codex OAuth → chatgpt.com 的出站；`codex-api-key` 与其他 provider 不受此原则约束。
- 对齐通过配置项提供；只有经过测试环境与生产观察的项才改为代码默认值，因为代码默认值会改变所有部署的行为。
- 不改写上游错误的语义。例如不把上游连接失败改写成 `context_length_exceeded` 去诱导客户端压缩上下文：官方客户端没有这种行为，误判时还会让客户端丢弃上下文，并让失败原因统计失真。

| 项 | 官方客户端 | 本配置的默认值 | 说明 |
|---|---|---|---|
| HTTP 请求体压缩 | zstd | `request-compression: auto`（zstd） | 已对齐 |
| Accept-Encoding | 不发送 | `accept-encoding: official`（不发送） | 已对齐；`legacy` 保留历史行为 |
| 传输方式 | 默认 websocket，重试预算耗尽后整个会话回退 HTTP | `transport: follow` | 未改默认值：HTTP 下游转 websocket 时每个请求新建一条上游连接，握手开销尚无生产数据；需要时按部署开启 `auto` / `websocket` |
| websocket 出站消息压缩 | 协商 permessage-deflate 后压缩每条出站消息 | 不压缩（仅协商，读取上游压缩消息） | 未对齐；需先确认上游是否接受 deflate，再以配置项形式提供 |
| 客户端标识（User-Agent / Originator） | `{originator}/{version} ({OS} {版本}; {架构}) {终端}`，版本随 release 更新 | 下游为官方客户端时原样透传其 UA 与 Originator；其他客户端回退到内置 `codex-tui/<版本>`（与 upstream 一致） | 回退版本可能落后于官方最新 release；部署可用 `codex-header-defaults.user-agent` 设为官方最新版本的同格式 UA，实测后生效 |
| Responses websocket `OpenAI-Beta` | `responses_websockets=2026-02-06` | 相同 | 已对齐 |

已知且接受的差异：

- **websocket 增量续接**：官方客户端一个会话保持一条长连接，只发送新增 input 并带 `previous_response_id`。HTTP 下游请求没有会话状态，转发到上游 websocket 时只能发送完整请求；只有下游本身是 websocket 时才能保留增量。
- **deflate 上下文复用**：官方客户端默认复用跨消息的压缩上下文；Go 侧使用的 gorilla/websocket 只支持 no context takeover。
- **回退时机**：官方在重试预算耗尽后会话级回退 HTTP；CPA 按请求选择传输，仅在上游 websocket 握手返回 426 时回退 HTTP。

## 1. 传输方式：`transport` / `websocket-threshold-bytes`

### 背景

Codex 请求到达 CPA 时，下游可能是 HTTP（`POST /v1/responses` + SSE，如 ZCode、未开启 websocket 的 Codex Desktop provider），也可能是 Responses websocket。历史行为是"上游传输跟随下游"。

2026-10 的线上排查观察到：同一账号、同一出口、同一请求体（约 1MB），**经 HTTP 发往 chatgpt.com 的请求在部分上游区域返回 Envoy `upstream connect error ... reset reason: remote connection failure`，经 websocket 则正常完成**；同一请求从其他区域直连 HTTP 也正常。官方 Codex 客户端的默认传输本来就是 websocket，HTTP 只是失败后的回退。

### 取值

| 取值 | 行为 |
|---|---|
| `follow`（默认，留空等同） | 保持历史行为：HTTP 下游走上游 HTTP |
| `websocket` | 所有符合条件的 HTTP 下游请求改走上游 Responses websocket，事件翻译回 HTTP/SSE 返回客户端 |
| `auto` | 仅当请求 payload ≥ `websocket-threshold-bytes` 时改走上游 websocket，小请求继续走 HTTP，省去握手开销 |

`websocket-threshold-bytes` 默认 786432（768 KiB），`0` 或省略用默认值。阈值低于约 1MB，给 executor 追加的字段留出余量。

### 适用范围

- 只影响 HTTP 下游请求；websocket 下游会话仍由各 credential 的 `websockets` 属性决定。
- 只有开启了 `websockets` 的 credential 才会改走 websocket；未开启的 credential 始终保持 HTTP。
- `/responses/compact` 始终走 HTTP。
- 上游 websocket 握手返回 426 时沿用既有行为回退 HTTP；其他握手错误按普通上游错误处理。
- HTTP 下游请求没有 websocket 会话 ID，每个请求使用一条临时上游连接，完成后关闭，不做跨请求增量续接。

## 2. 请求体压缩：`request-compression`

官方 Codex 客户端（ChatGPT 登录，`enable_request_compression` 默认开启）会以 zstd level 3 压缩发往 `chatgpt.com/backend-api/codex` 的 HTTP `/responses` 请求体，并带 `Content-Encoding: zstd`。

| 范围 | 行为 |
|---|---|
| Codex OAuth credential，目标为 `https://chatgpt.com/backend-api/codex/responses`（含 `*.chatgpt.com`） | `auto`（默认）：zstd 压缩；`none`：不压缩 |
| `codex-api-key` credential | 默认不压缩；仅当条目设置 `request-compression: zstd` 时压缩（适用于确认支持 zstd 的上游，例如另一台 CPA） |
| `/responses/compact`、websocket、images 等其他端点 | 不压缩（官方客户端也不压缩） |

`codex-api-key` 条目的 `request-compression` 可通过 `PUT/PATCH /v0/management/codex-api-key` 更新。接口与配置加载使用相同的归一化和校验规则：接受空字符串、`none`、`zstd`，忽略大小写及首尾空格；待保存条目包含非法值时返回 400，且不修改配置或触发重载。PATCH 省略该字段时保留原值，空字符串恢复默认的不压缩行为。

- 压缩发生在请求头和请求体全部准备好之后；request-log / error-log 记录的仍是压缩前的原文，请求头中可见 `Content-Encoding: zstd`。
- 上游对压缩请求返回 415 时，用原文自动重发一次并记录 warn。
- 压缩只改变线上编码，不改变请求内容、token 统计或下游响应；usage 明细同时记录原文大小（`upstream_request_bytes`）、线上大小（`upstream_request_wire_bytes`）和编码（`upstream_request_encoding: zstd`）。

## 3. Accept-Encoding：`accept-encoding`

官方 Codex 客户端不发送 `Accept-Encoding`。CPA 历史上发往 chatgpt.com 的 HTTP/2 请求由 Go 传输层自动加 `Accept-Encoding: gzip` 并透明解压。

| 取值 | 行为 |
|---|---|
| `official`（默认，留空等同） | 与官方一致：不发送 `Accept-Encoding`，响应以原始编码返回 |
| `legacy` | 历史行为：自动请求 gzip 并透明解压 |

只作用于 chatgpt.com 的 uTLS 传输路径，并且只改变该请求自己的传输设置；Claude 等其他 provider 显式设置的 Accept-Encoding 不受影响。

## 部署建议

- 默认配置下 `transport` 不改变现有行为；`request-compression: auto` 与 `accept-encoding: official` 让 Codex OAuth 发往 chatgpt.com 的 HTTP 请求编码与官方客户端对齐（相对历史版本属于行为变化）。
- 上游区域出现"大请求 HTTP 失败、websocket 正常"时：设置 `transport: auto`（或 `websocket`），并确认 codex credential 开启了 `websockets`。
- 若怀疑压缩引发兼容问题，可设置 `request-compression: none` 立即回退；若响应编码引发问题，可设置 `accept-encoding: legacy` 回到历史行为。

## 验证

- `go test ./internal/config -run CodexOutbound`
- `go test ./internal/runtime/executor -run 'CodexAutoExecutorOutbound|CodexBackendResponsesURL|CodexRequestCompression|CompressesOptedIn|LeavesDefaultAPIKey|Uncompressed|AcceptEncodingMode'`
- `go test ./internal/runtime/executor/helps -run AcceptEncoding`
- `go test ./internal/watcher/diff -run 'CodexOutbound|TestBuildConfigChangeDetails'`
- 线上冒烟：`transport: auto` 下用大于阈值的 HTTP 请求确认成功；调试日志可见 `codex outbound: sending HTTP request over upstream websocket`；error-log 的上游请求头可见 `Content-Encoding: zstd`。
