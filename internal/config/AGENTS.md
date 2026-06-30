# internal/config navigation card

`internal/config/` owns the YAML/JSON config model, defaults, compatibility helpers, sanitization, and generated config preservation behavior.
Read this card before adding, renaming, deleting, migrating, or changing the default of any config key.
Key files: `config.go`, `sdk_config.go`, `disable_image_generation_mode.go`, `vertex_compat.go`, `config.example.yaml`.

## Why this is high-risk

- Config keys are user data and Management API surface, not just internal structs.
- YAML tags, JSON tags, defaults, sanitize logic, hot reload, TUI labels, Panel forms, watcher diff, and SDK config can all depend on the same key.
- Startup legacy migration is intentionally disabled in current code; do not re-enable persistence accidentally.
- `usage-statistics-enabled`, `redis-usage-queue-*`, and `panel-github-repository` are LTS contract-adjacent.

## Required before changes

- Search for the exact key in `internal/`, `sdk/`, `test/`, `config.example.yaml`, and Management API handlers.
- Check whether the key appears in TUI config tabs, Panel-facing endpoints, watcher diff, or SDK config structs.
- Do not infer runtime defaults from `config.example.yaml` alone; verify `config.go` and `parse.go`.
- Decide whether old config files must keep loading without mutation.

## Do not

- 不要 silently rename config keys without compatibility handling.
- 不要在 startup 期间自动重写用户 `config.yaml`，除非用户明确要求恢复迁移持久化。
- 不要把 secret defaults 写进 `config.example.yaml`。
- 不要移除 `panel-github-repository` 指向 `CPA-Panel-LTS` 的默认意图。

## Validation

- `go test ./internal/config`
- Config Management API changes: `go test ./internal/api/handlers/management`
- Watcher/diff changes: `go test ./internal/watcher/diff`
- Broad config schema changes should finish with `go test ./...`.
