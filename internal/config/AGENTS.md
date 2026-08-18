# internal/config navigation card

`internal/config/` owns the YAML/JSON config model, defaults, compatibility helpers, sanitization, and generated config preservation behavior.
Read this card before adding, renaming, deleting, migrating, or changing the default of any config key.
Key files: `config.go`, `config_types.go`, `config_defaults.go`, `config_load.go`, `parse.go`, `config_normalization.go`, `config_yaml.go`, `sdk_config.go`, `config.example.yaml`.

## Why this is high-risk

- Config keys are user data and Management API surface, not just internal structs.
- YAML tags, JSON tags, defaults, sanitize logic, hot reload, TUI labels, Panel forms, watcher diff, and SDK config can all depend on the same key.
- Load/parse paths perform in-memory defaults, compatibility normalization, and sanitization. `ParseConfigBytes` does not write disk; `LoadConfigOptional` may intentionally persist a plaintext `remote-management.secret-key` as a bcrypt hash after validation.
- `usage-statistics-enabled`, `redis-usage-queue-*`, and `panel-github-repository` are LTS contract-adjacent.

## Required before changes

- Search for the exact key in `internal/`, `sdk/`, `test/`, `config.example.yaml`, and Management API handlers.
- Check whether the key appears in TUI config tabs, Panel-facing endpoints, watcher diff, or SDK config structs.
- Do not infer runtime defaults from `config.example.yaml` alone; verify `config_load.go`, `parse.go`, and `config_defaults.go`.
- Decide whether old config files must keep loading without mutation.
- Keep `LoadConfigOptional` and `ParseConfigBytes` aligned for defaults, validation, and sanitize semantics, while preserving their documented persistence difference.

## Do not

- 不要 silently rename config keys without compatibility handling.
- 不要 add startup writes beyond an explicit, tested persistence contract such as validated remote-management secret hashing.
- 不要把 secret defaults 写进 `config.example.yaml`。
- 不要移除 `remote-management.panel-github-repository` 指向 `CPA-Panel-LTS` 的默认意图。

## Validation

- `go test ./internal/config`
- Config Management API changes: `go test ./internal/api/handlers/management`
- Watcher/diff changes: `go test ./internal/watcher/diff`
- Cross-surface schema changes: `go test ./internal/config ./internal/watcher/... ./internal/api/handlers/management -run 'Config|config|Usage|usage'`
- Broad config schema changes should finish with `go test ./...`.
