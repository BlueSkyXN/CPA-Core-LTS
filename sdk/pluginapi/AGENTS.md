# sdk/pluginapi navigation card

`sdk/pluginapi/` owns public plugin capability interfaces, metadata types, callback request/response structs, scheduler contracts, Management route contracts, and usage plugin records.
Read this card before changing any exported plugin API type, method, struct field, JSON tag, or capability behavior.
Key files: package Go files under this directory, plus examples under `examples/plugin/`.

## Local invariants

- Exported names, JSON tags, and method signatures are public API for external plugins.
- Capability contracts must stay aligned with `internal/pluginhost`, `sdk/pluginabi`, and `sdk/cliproxy` runtime wiring.
- Scheduler requests must not expose mutable auth internals beyond the safe candidate data contract.
- Usage plugin records must preserve source, model, auth index, token, latency, and failure fields where available.

## Local rules

- Prefer additive fields/methods over renames/removals.
- New capability types need host support, tests, and an example or doc update when user-facing.
- If changing Management route types, check built-in route namespace collision behavior in `internal/pluginhost`.

## Do not

- 不要 expose raw token, API key, cookie, service-account JSON, or full auth file content through public plugin structs.
- 不要 make plugin APIs depend on process-global state or interactive flows.
- 不要 change JSON tags without migration/compatibility reasoning.

## Validation

- `go test ./sdk/pluginapi`
- Host compatibility: `go test ./internal/pluginhost`
- SDK integration: `go test ./sdk/cliproxy/...`
- Example-facing changes: inspect `examples/plugin/` and run targeted example builds when practical.
