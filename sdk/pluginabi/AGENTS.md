# sdk/pluginabi navigation card

`sdk/pluginabi/` owns low-level dynamic plugin ABI constants, method names, schema version, and RPC envelope identifiers shared by core and external plugins.
Read this card before changing schema version, method constants, callback names, or ABI compatibility behavior.
Key files: package Go files under this directory and corresponding `internal/pluginhost` RPC/loader code.

## Local invariants

- Method names and schema versions are external compatibility contracts for compiled plugins.
- Changes must stay aligned with `internal/pluginhost` dispatch, `sdk/pluginapi` capability interfaces, and examples.
- Additive method introduction is safer than renaming or removing existing methods.

## Required before changes

- Read `internal/pluginhost/AGENTS.md` and `sdk/pluginapi/AGENTS.md`.
- Identify whether existing plugin examples or third-party plugins would need recompilation.
- If a breaking change is unavoidable, get explicit user approval for the compatibility strategy.

## Do not

- 不要 rename or remove ABI constants casually.
- 不要 bump schema version without updating host compatibility checks and tests.
- 不要 use ABI changes to expose raw credentials outside approved host callbacks.

## Validation

- `go test ./sdk/pluginabi`
- `go test ./internal/pluginhost`
- Public capability changes: `go test ./sdk/pluginapi`
