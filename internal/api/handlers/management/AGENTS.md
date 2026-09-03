# internal/api/handlers/management navigation card

`internal/api/handlers/management/` owns the external Management API for config, auth files/OAuth sessions, usage import/export, logs, quota, and plugin management.
Read this card after `internal/api/AGENTS.md` before changing any Management handler, response shape, persistence path, or side effect.
Key files: `handler.go`, `config_*.go`, `auth_files*.go`, `oauth_sessions.go`, `usage.go`, `plugins.go`, `plugin_store.go`.

## Local invariants

- Built-in routes are registered in `internal/api/server_management.go`; normal `/v0/management/*` endpoints pass availability and `Handler.Middleware()` checks.
- `/v0/management/oauth-callback` is an intentional route-level exception to management-key middleware and is protected by OAuth state/session validation. Do not generalize that exception.
- Management availability, route registration, remote-access policy, and credential validation are separate concerns. Home mode keeps the Management surface unavailable.
- Config writes must preserve comments/order through existing config helpers and must trigger the established generation-ordered reload path.
- Auth file upload/download/delete/patch must preserve safe filename/path handling, private-file permissions, registered-record synchronization, plugin-virtual-source rules, and the rollback behavior implemented by each flow.
- Usage export/import follows the canonical v3 contract in `internal/usage/AGENTS.md`; released v1/v2 payloads migrate only when their token and timing semantics are provable, while malformed, unsupported, ambiguous, or overflowing imports fail atomically with stable error codes.
- Plugin routes must remain namespaced and must not collide with built-in method/path pairs.

## Do not

- 不要 add a Management endpoint outside the existing middleware/availability model without an explicit contract decision.
- 不要 echo secrets, raw auth files, OAuth codes, untrusted paths, or raw upstream bodies in errors.
- 不要 treat a successful HTTP mutation as complete until the intended config/auth/runtime readback or reload path has succeeded.

## Validation

- `go test ./internal/api/handlers/management`
- Focused surface: `go test ./internal/api/handlers/management -run 'Auth|OAuth|Config|Usage|Plugin'`
- Usage changes: `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
- Route/middleware changes: `go test ./internal/api/...`
