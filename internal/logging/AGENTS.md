# internal/logging navigation card

`internal/logging/` owns global logging, request logging, request IDs, request metadata, home log forwarding, and log directory cleanup.
Read this card before changing log formats, request/response body capture, metadata keys, redaction, log file paths, rotation, or streaming log behavior.
Key files: `request_logger.go`, `request_logger_lts.go`, `request_logger_streaming.go`, `requestmeta.go`, `requestid.go`, `cpa_trace.go`, `gin_logger.go`, `global_logger.go`, `home_app_log_forwarder.go`.

## Why this is high-risk

- Logs can contain user prompts, provider responses, API keys, OAuth tokens, cookies, auth headers, management secrets, and internal URLs.
- Request metadata feeds usage attribution, Redis queue payload, debugging, home forwarding, and management views.
- Streaming logging can accidentally consume or buffer response bodies and break clients.
- `X-CPA-TRACE-ID` is a downstream-visible correlation contract derived after request/auth identity is known and before response commitment.

## Required before changes

- Trace whether metadata is read by `internal/usage`, `internal/redisqueue`, `internal/runtime/executor`, `sdk/cliproxy/auth`, or Management handlers.
- Confirm any new logged field is redacted or safe by construction.
- For body logging changes, test both non-streaming and streaming paths.
- Request metadata/header holders used asynchronously must copy mutable values rather than retain request/Gin-owned buffers.
- Body capture changes preserve config gates, size/truncation limits, compression handling, deferred cleanup, and redaction.

## Do not

- 不要 log Authorization headers, cookies, raw tokens, raw auth files, management secret, or unredacted metadata/body content outside the established request-log policy.
- 不要 change request ID propagation without checking Gin middleware and SDK auth selector logs.
- 不要 add blocking disk/network work to hot request paths without a bounded reason.

## Validation

- `go test ./internal/logging`
- Middleware/trace changes: `go test ./internal/logging ./internal/api/middleware ./internal/api -run 'Trace|RequestLog|Websocket|Streaming'`
- Metadata/usage changes: `go test ./internal/usage ./internal/redisqueue`
- API logging middleware changes: `go test ./internal/api`
