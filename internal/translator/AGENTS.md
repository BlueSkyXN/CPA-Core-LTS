# internal/translator navigation card

`internal/translator/` owns request/response conversion between OpenAI, Gemini, Claude, Codex, Gemini CLI, and Antigravity.
Read this card before changing registration, request JSON, response events, tool/function call conversion, token-count adapters, or signature/thinking handling.
Key files: `init.go`, `translator/translator.go`, provider pair directories such as `codex/openai/`, `openai/claude/`, `antigravity/claude/`, and `common/`.

## Local invariants

- Translator registration must keep source/target protocol pairs discoverable through the existing registry.
- Streaming event order and sentinel fields matter; preserve provider event names and IDs unless tests prove a migration.
- Builtin tools, function calls, images, thinking blocks, and usage metadata need explicit conversion rules; do not drop unsupported fields silently unless existing behavior already does.
- Usage metadata should remain available for usage logging when upstream provides it.
- Antigravity/Claude signature validation and bypass/cache behavior must stay aligned with `internal/cache` and executor behavior.

## Local rules

- Use structured JSON helpers already present in the codebase instead of ad hoc string concatenation for mutable payloads.
- Add table-driven tests near the translator pair being changed.
- If a change affects SDK-facing behavior, check `sdk/translator` and examples.

## Do not

- 不要 invent new protocol names or registry keys without checking `internal/constant` and existing registration patterns.
- 不要 hide malformed upstream payloads by returning success with empty content.
- 不要 strip tool calls, images, or thinking signatures merely to simplify a conversion.

## Validation

- Run the package tests for the changed translator pair, for example `go test ./internal/translator/codex/openai/...`.
- Cross-provider changes: `go test ./internal/translator/...`.
- Builtin tool or sentinel changes: `go test ./test -run 'Translation|thinking|builtin|sentinel'`.
