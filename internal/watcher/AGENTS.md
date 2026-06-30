# internal/watcher navigation card

`internal/watcher/` owns config/auth file watching, config diff summaries, auth synthesis, plugin auth parsing, runtime auth updates, and hot reload dispatch.
Read this card before changing file watch events, debounce behavior, auth file parsing/synthesis, config reload, diff redaction, or watcher-to-SDK update flow.
Key files: `watcher.go`, `events.go`, `config_reload.go`, `diff/`, `synthesizer/`.

## Why this is high-risk

- Watcher output can change live auth availability, routing, plugin models, and Management/TUI state without restart.
- Diff summaries may mention secrets if redaction is wrong.
- Auth synthesis touches file-backed credentials, config API keys, plugin auth files, and SDK auth conductor updates.

## Required before changes

- Trace the update through `sdk/cliproxy/service.go`, `internal/auth`, and affected Management API handlers.
- For config key changes, read `internal/config/AGENTS.md`.
- For auth file shape changes, read `internal/auth/AGENTS.md` and `sdk/cliproxy/AGENTS.md`.

## Do not

- 不要 include token, API key, OAuth secret, service-account JSON, or full auth file content in diff summaries.
- 不要 silently drop delete events that should disable or remove an auth entry.
- 不要 auto-persist rewritten config/auth files unless the caller explicitly owns that flow.

## Validation

- `go test ./internal/watcher ./internal/watcher/diff ./internal/watcher/synthesizer`
- Config changes: `go test ./internal/config`
- SDK update flow changes: `go test ./sdk/cliproxy/...`
