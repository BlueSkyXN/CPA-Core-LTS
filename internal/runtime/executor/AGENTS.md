# internal/runtime/executor navigation card

`internal/runtime/executor/` owns provider execution, retries, streaming, WebSocket handling, payload helpers, and usage emission.
Read this card before changing executors, retry/cooldown behavior, streaming parsing, upstream headers, WebSocket output, or usage emission.
Key files: `codex_executor.go`, `codex_websockets_executor.go`, `claude_executor.go`, `gemini_executor.go`, `antigravity_executor.go`, `openai_compat_executor.go`, `helps/`.

## Local invariants

- Upstream streaming responses must remain streaming; do not buffer full streams unless the existing path already does so.
- Network timeout policy: credential acquisition may use timeout; after upstream connection establishment, do not casually add timeouts that kill long-running streams.
- Usage records should include model, source, auth index, token counts, latency, and failure state when available.
- Retry handling must preserve explicit upstream errors, especially 429/usage-limit cases.
- Provider-specific headers and request bodies often mimic official clients; changing them can break auth or quota behavior.

## Local rules

- Prefer existing helpers in `helps/` for proxy, token, payload, cache, logging, and usage behavior.
- Codex WebSocket output needs tests for event shape and final stream output.
- Antigravity/Claude signature changes need translator/cache tests too, not only executor tests.

## Do not

- 不要 log upstream Authorization headers, cookies, tokens, raw auth payloads, or full request bodies containing user content.
- 不要 convert streaming endpoints into non-streaming paths for easier testing.
- 不要 special-case model strings without checking registry/routing semantics.

## Validation

- `go test ./internal/runtime/executor`
- Helper changes: `go test ./internal/runtime/executor/helps`
- Cross-protocol streaming/tool changes may also require `go test ./internal/translator/...` and `go test ./test`.
