# internal/auth navigation card

`internal/auth/` owns provider OAuth/device flows, token storage structs, credential import helpers, and auth file naming.
Read this card before changing OAuth URLs, PKCE/device flow code, token refresh fields, auth file serialization, or provider credential parsing.
Key files: provider subdirectories under `antigravity/`, `claude/`, `codex/`, `empty/`, `kimi/`, `vertex/`, `xai/`, plus `models.go`.

## Why this is high-risk

- Auth files may contain access tokens, refresh tokens, account IDs, API keys, project IDs, and provider metadata.
- These structs feed watcher synthesis, SDK auth conductor, plugin scheduler, Management API auth-file endpoints, executor selection, and usage attribution.
- OAuth callback ports and browser behavior are user-visible CLI behavior.

## Required before changes

- Trace the provider through CLI command, token storage, watcher/auth manager, executor, SDK conductor, and management handlers.
- Confirm logs use redacted summaries, not raw token payloads.
- Preserve existing auth file compatibility unless a migration is explicitly requested.
- OAuth/browser flow changes also inspect `internal/cmd/*_login.go`, `cmd/server/main.go`, and Management OAuth session handlers.
- Token storage keeps private parent directories and `0600` credential files on supported platforms.

## Do not

- 不要 log raw token、refresh token、authorization code、JWT、API key、service-account JSON 或完整 auth file。
- 不要 hard-code user credentials into source, examples, tests, or docs.
- 不要 change auth filename conventions without checking watcher and Management API download/delete paths.
- 不要 make OAuth flows require interactive browser opening when `--no-browser` is set.
- 不要 move Management OAuth session/cancel/save/rollback decisions into a provider package without checking the Management handler contract.

## Validation

- Provider-local tests: `go test ./internal/auth/...`.
- CLI login flow changes: `go test ./internal/cmd ./cmd/server`.
- SDK auth integration changes: `go test ./sdk/cliproxy/auth`.
- Management auth-file changes: `go test ./internal/api/handlers/management -run Auth`.
