# Usage 请求体积字段（`size_version: 1`）

每条 usage detail 可选记录本次请求的入站、出站体积和上游传输方式，用来定位与请求体大小相关的上游失败（例如某些上游区域对约 1MB 以上 HTTP 请求体返回 Envoy `upstream connect error ... remote connection failure`，而 websocket 正常）。

字段只描述字节数和编码，不记录请求或响应内容。

## 字段

全部为可选字段，未测量时省略（0 和空字符串不输出）。

| 字段 | 含义 |
|---|---|
| `trace_id` | 入站请求 ID，与 request log 文件名后缀一致，可用 `GET /v0/management/request-log-by-id/<trace_id>` 定位日志 |
| `size_version` | 体积字段语义版本；当前为 `1`，没有任何体积字段被测量时省略 |
| `client_request_bytes` | 客户端请求体字节数（按 `Content-Length`，解码 `Content-Encoding` 之前）；chunked 请求和 websocket 下游不测量 |
| `client_request_encoding` | 客户端请求 `Content-Encoding`，如 `zstd` |
| `upstream_transport` | 最终一次上游尝试的传输方式：`http` 或 `websocket` |
| `upstream_request_bytes` | 最终一次上游请求的 payload 字节数（应用 `Content-Encoding` 之前） |
| `upstream_request_wire_bytes` | 最终一次上游请求写入连接的字节数（应用 `Content-Encoding` 之后） |
| `upstream_request_encoding` | 最终一次上游请求的 `Content-Encoding` |
| `upstream_response_bytes` | 从上游响应体或 websocket 消息读取的字节数（传输层解码之后） |

说明：

- 同一请求内有重试时，`upstream_*` 描述最后一次尝试。
- HTTP 路径在传输层统一测量，覆盖所有使用 usage reporter 跟踪 HTTP client 的 executor；请求体带 `Content-Encoding` 时传输层只能看到线上字节，`upstream_request_bytes` 可能缺失，此时以 `upstream_request_wire_bytes` 为准。
- Codex websocket 路径记录发送的 `response.create` 消息字节数；出站消息不压缩，payload 与线上字节相同。启用 response steering 的双工会话中，后续轮次只记录传输方式和响应字节。
- 图片等附加模型的 usage 记录不重复携带体积，避免重复计数。

## 兼容性

- 字段为 canonical v3 export 的增量可选字段，不改变 `CanonicalExportVersion`；旧导出可直接导入，旧 Core 导入新导出时忽略这些字段。
- 导入时 `size_version` 不是 `1` 的记录会丢弃体积字段，不让导入失败；负数清零，`upstream_transport` 只接受 `http`/`websocket`，编码值统一小写，含路径分隔符或过长的 `trace_id` 被清除。
- 体积字段和 `trace_id` 不参与去重 identity。
- `/v0/management/usage-queue` 事件携带同名字段（同样省略未测量值）。

## 查询与分析

- `GET /v0/management/usage/query/capabilities` 返回 `request_size_version: 1`，Panel 据此判断 Core 是否支持体积字段。
- `POST /v0/management/usage/query/analytics` 的 `data.requestSize` 按上游请求体积分桶（下界：0、64 KiB、256 KiB、512 KiB、768 KiB、1 MiB、2 MiB），给出每桶请求数、失败数，以及 websocket 请求数和失败数；`sampleCount` 是有体积测量的请求数，`unmeasured` 是没有测量的请求数。范围内没有任何测量记录时省略该字段。

## 日志

`upstream execution failed` warn 日志在 `auth=` 之后追加最终尝试的体积，例如：

```
503 | 1.2s | upstream execution failed: provider=codex model=gpt-5.6 auth=... transport=http req_bytes=1050000 resp_bytes=91 err=...
```

`wire_bytes` 仅在与 `req_bytes` 不同时输出；`encoding` 仅在请求体有编码时输出。

## 验证

- `go test ./sdk/cliproxy/usage -run 'RequestSize|ContentEncoding'`
- `go test ./internal/runtime/executor/helps -run RequestSize`
- `go test ./internal/runtime/executor -run 'RecordsHTTPRequestSize|RecordsWebsocketRequestSize'`
- `go test ./internal/usage -run 'RequestSize|AnalyticsOmitsRequestSize'`
- `go test ./internal/redisqueue -run RequestSize`
- `go test ./internal/logging -run UpstreamRequestSize`
- `go test ./sdk/cliproxy/auth -run WarnLogUpstreamFailureIncludesUpstreamRequestSize`
