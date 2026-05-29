# internal/api navigation card

`internal/api/` owns the Gin server, middleware, Management API routes, provider routes, WebSocket handling, and Amp module.
Read this card before editing routes, middleware, management handlers, Amp endpoints, auth gates, request logging, or response writers.
Key files: `server.go`, `handlers/management/`, `middleware/`, `modules/amp/`.

## Local invariants

- Management routes live under `/v0/management` and are registered only when a management secret is available.
- Usage endpoints must remain available as `/usage`, `/usage/export`, `/usage/import`, and `usage-statistics-enabled`.
- Management middleware must keep localhost/remote behavior aligned with `remote-management` config.
- Provider routes must preserve OpenAI/Gemini/Claude/Codex/Amp compatible wire shapes.
- WebSocket auth behavior is controlled by `websocket-auth`; do not make it always-on or always-off.
- Request/response logging must avoid secret leakage and must not consume streaming bodies in a way that breaks clients.

## Local rules

- Route changes require checking both registration in `server.go` and handler tests under `handlers/management/`.
- Amp route changes require checking `internal/api/modules/amp/*_test.go` and `test/amp_management_test.go`.
- Middleware changes require tests for both normal HTTP and streaming/WebSocket paths when applicable.
- Management API response shape changes are Panel/TUI/SDK breaking changes unless proven compatible.

## Do not

- 不要把 management routes 暴露为无鉴权远程接口。
- 不要在 handler error response 中回显 credential、token、raw auth file、full upstream body。
- 不要为了修一个 provider route 改坏 shared `/v1`、`/v1beta` 或 provider routes。

## Validation

- `go test ./internal/api`
- `go test ./internal/api/handlers/management`
- `go test ./internal/api/modules/amp`
- `go test ./test -run Amp`
- Usage route changes also run the validation listed in `internal/usage/AGENTS.md`.
