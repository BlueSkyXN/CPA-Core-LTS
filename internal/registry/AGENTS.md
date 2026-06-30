# internal/registry navigation card

`internal/registry/` owns model registration, embedded model catalogs, remote model refresh hooks, client/model availability, and quota-related routing state.
Read this card before changing model catalog JSON, model aliases, quota state, registry refresh behavior, provider model metadata, or client availability logic.
Key files: `model_registry.go`, `model_definitions.go`, `model_updater.go`, `codex_client_models.go`, `models/models.json`, `models/codex_client_models.json`.

## Local invariants

- Go module path remains `github.com/router-for-me/CLIProxyAPI/v7`; registry changes must not introduce legacy `/v6` imports.
- `models/models.json` is refreshed in CI from `router-for-me/models.git`; local copies may be stale when offline.
- Quota state and suspended clients affect runtime routing and auth conductor behavior.
- Model metadata may be exposed through Management API, SDK, and provider routing.

## Local rules

- Treat embedded JSON catalogs as generated/externally refreshed unless the task is explicitly catalog maintenance.
- Codex/Antigravity helper command changes need auth and registry review together.
- Quota reset or routing-state changes require checking `sdk/cliproxy/auth` and relevant Management API tests.

## Do not

- 不要 hand-edit large model catalog JSON for unrelated changes.
- 不要 drop token limits, reasoning metadata, aliases, or quota markers without checking routing behavior.
- 不要 rely on network refresh in local validation unless the user requested it.

## Validation

- `go test ./internal/registry`
- Quota/auth routing changes: `go test ./sdk/cliproxy/auth`
- Build smoke: `go build -o test-output ./cmd/server && rm -f test-output`
