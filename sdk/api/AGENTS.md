# sdk/api navigation card

`sdk/api/` is the public protocol-handler layer for OpenAI/Responses, Claude, Gemini/Interactions, images/video, SSE/WebSocket streaming, model routing, and plugin interception.
Read this card before changing handler types/options, request lifecycle, protocol error envelopes, stream forwarding, header policy, routing metadata, or interceptor behavior.
Key files: `handlers/handlers*.go`, `handlers/model_execution.go`, `handlers/stream_forwarder.go`, `handlers/header_filter.go`, protocol subdirectories, `options.go`.

## Local invariants

- Once a `requestLifecycleTracker` has been created, handler execution must complete it exactly once across non-stream, stream, direct plugin executor, and tracked termination paths; `sync.Once` enforces at-most-once delivery. Routing/provider/plugin-host failures that occur before tracker creation do not emit `RequestCompletion` under the current contract.
- Streaming may retry/fallback only before committed payload according to the auth/executor contract; after first payload it must preserve ordering, terminal errors, cancellation, and protocol-specific SSE/WebSocket framing.
- OpenAI Responses streams validate event JSON and terminal state without losing valid preceding frames; error envelopes must not expose raw upstream bodies or credentials.
- Request routing/interceptors preserve original/requested/selected model, entry/exit protocol, query, headers, and skip-plugin identity. Home mode and plugin-direct execution fail closed where required.
- Upstream response headers are cloned and filtered: remove hop-by-hop, connection-scoped, `Set-Cookie`, gateway-identifying, and CPA-reserved headers; do not overwrite headers already owned by CPA.
- Execution metadata carries the LTS usage/session/fallback fields consumed by auth, executor, logging, and usage layers.

## Do not

- 不要 write headers or a success frame before synchronous stream bootstrap/validation has produced a committable payload.
- 不要 replay a non-idempotent request or switch auth/model after output has been delivered.
- 不要 let plugin interceptors receive mutable shared header/body buffers or run after a terminal completion.

## Validation

- `go test ./sdk/api/...`
- Responses/WebSocket changes: `go test ./sdk/api/handlers/openai -run 'Responses|Websocket|Stream|Compact'`
- Lifecycle/routing/plugin changes: `go test ./sdk/api/handlers -run 'Lifecycle|Interceptor|ModelRouter|ExecuteModel|Stream'`
- Server route integration: `go test ./internal/api/...`
