# internal/thinking navigation card

`internal/thinking/` provides provider-neutral reasoning/thinking parsing, validation, summary intent, suffix handling, and provider-specific request application.
Read this card before changing level/budget modes, model suffixes, summary visibility, model capability validation, provider appliers, or Codex wire canonicalization.
Key files: `types.go`, `apply.go`, `suffix.go`, `summary.go`, `validate.go`, `codex_wire.go`, `provider/`.

## Local invariants

- Model suffix configuration has explicit precedence over request-body thinking settings; base model identity remains available for routing and registry lookup.
- Thinking effort and summary visibility are orthogonal. Preserve explicit summary intent across translation and provider-specific rewrites.
- Known catalog models follow registry capability limits; unknown/user-defined models pass supported config through for upstream validation rather than inheriting an unrelated built-in model contract.
- `max` and Codex-client `ultra` are distinct user-facing levels even where official Codex wire canonicalizes Ultra to Max.
- Provider appliers are idempotent, return a modified copy, and do not mutate shared config/model metadata. Native provider names cannot be replaced by plugin providers.

## Do not

- 不要 infer summary enablement from effort for protocols that have an explicit summary field; OpenAI Chat is the documented compatibility exception.
- 不要 silently clamp, drop, or translate a level/budget without the provider/model rule and regression coverage.
- 不要 invent provider names outside the existing registry/plugin ownership model.

## Validation

- `go test ./internal/thinking/...`
- Translator/executor integration: `go test ./internal/thinking/... ./internal/translator/... ./internal/runtime/executor -run 'Thinking|Reasoning|Summary|Ultra|Max'`
- Sentinel coverage: `go test ./test -run 'thinking|Thinking|Summary'`
