# internal/api navigation card

`internal/api/` owns the Gin server, middleware, Management API routes, provider routes, WebSocket handling, and Amp module.
Read this card before editing routes, middleware, management handlers, Amp endpoints, auth gates, plugin management routes, or response writers.
Key files: `server_routes.go`, `server_management.go`, `server_middleware.go`, `handlers/management/`, `middleware/`, `modules/amp/`.

## Local invariants

- Built-in Management routes live under `/v0/management`; normal endpoints pass the availability middleware and `Handler.Middleware()`. The state/session-validated `/oauth-callback` route is an intentional management-key exception.
- Management availability, route registration, remote-access policy, and credential validation are separate layers. Home mode keeps the Management surface unavailable.
- Management usage endpoints must remain available as `/usage`, `/usage/export`, `/usage/import`, `/usage-queue`, and `/usage-statistics-enabled` under `/v0/management`.
- Management middleware must keep localhost/remote behavior aligned with `remote-management.secret-key` and `remote-management.allow-remote`.
- Provider routes must preserve OpenAI/Gemini/Claude/Codex/Amp compatible wire shapes.
- WebSocket auth behavior is controlled by `ws-auth`; do not make it always-on or always-off.
- Plugin Management API routes must stay namespaced and must not override built-in Management endpoints.
- Logging must avoid secret leakage and must not consume streaming bodies.

## Local rules

- Route changes require checking registration in `server_routes.go` / `server_management.go` and handler tests under `handlers/management/`.
- Management handler changes must also read `handlers/management/AGENTS.md`.
- Amp route changes require checking `internal/api/modules/amp/*_test.go` and `test/amp_management_test.go`.
- Middleware changes require HTTP and streaming/WebSocket coverage when applicable.
- Management API response shape changes are Panel/TUI/SDK breaking changes unless proven compatible.

## Do not

- 不要把 management routes 暴露为无鉴权远程接口。
- 不要在 handler error response 中回显 credential、token、raw auth file、full upstream body。
- 不要为了修一个 provider route 改坏 shared `/v1`、`/v1beta` 或 provider routes。

## Validation

- `go test ./internal/api/...`
- `go test ./test -run Amp`
- Usage route changes also run the validation listed in `internal/usage/AGENTS.md`.
