# sdk/translator navigation card

`sdk/translator/` is the public translation registry for request, response, stream, token-count, and plugin-provided protocol transforms.
Read this card before changing exported formats/types, registry lookup, fallback behavior, summary preservation, hook ordering, or byte ownership.
Key files: `registry.go`, `types.go`, `format.go`, `plugin_hooks.go`, `pipeline.go`, `builtin/`, `internal/translator/AGENTS.md`.

## Local invariants

- Registration and lookup remain concurrency-safe; request maps use source-to-target while response lookup preserves the established target/source orientation.
- Missing request transforms return the original shape after normalizing only the resolved `model`; missing response transforms preserve the normalized input instead of dropping output.
- Reasoning summary intent is extracted before native translation and restored for the target model. Plugin normalizers/translators run in the documented before/native-or-plugin/after order.
- Transform and hook APIs do not guarantee that input `[]byte` is cloned, and the current hook API does not carry headers. A transform/hook that retains or mutates a backing array must clone it explicitly; callers must not assume registry isolation.
- Stream transforms preserve chunk order and per-stream state through the supplied `param` without leaking state across requests.

## Do not

- 不要 silently change format strings, exported transform signatures, default registry behavior, or fallback output.
- 不要 apply target-protocol summary fields when no translation occurred and no plugin handled the route.
- 不要 hide malformed payloads by returning empty success output.

## Validation

- `go test ./sdk/translator/...`
- Built-in/internal integration: `go test ./sdk/translator/... ./internal/translator/...`
- Handler/executor integration: `go test ./sdk/api/handlers ./internal/runtime/executor -run 'Translation|Translator|Summary|Stream'`
