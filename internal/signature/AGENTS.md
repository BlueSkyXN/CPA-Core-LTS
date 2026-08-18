# internal/signature navigation card

`internal/signature/` validates and sanitizes provider-specific thinking signatures and encrypted reasoning content.
Read this card before changing signature detection, provider compatibility, malformed-content handling, Claude/Gemini/GPT/Grok/Kimi validation, or sanitization.
Key files: `provider_compatibility.go`, provider `*_validation.go` files, `claude_messages_sanitize.go`, `gemini_sanitize.go`.

## Local invariants

- A signature/encrypted-content value is provider-private unless an explicit compatibility classifier proves otherwise; unknown or malformed values never become portable by default.
- Sanitizers preserve valid blocks and required empty placeholders while removing only content proven incompatible for the target protocol.
- Claude classic/CAIS, Gemini, GPT/Codex encrypted content, Grok, and Kimi have distinct validation semantics; model/provider fallback must not collapse them.
- Validation and sanitize behavior stays aligned with `internal/cache`, translators, executors, and reasoning replay tests.

## Do not

- 不要 accept opaque content merely because it is base64-shaped or non-empty.
- 不要 strip valid signatures/thinking blocks to make a translator pass, or replay them across an incompatible target.
- 不要 include full signature/encrypted-content values in logs or test failure snapshots.

## Validation

- `go test ./internal/signature`
- Cross-protocol changes: `go test ./internal/signature ./internal/cache ./internal/translator/... ./internal/runtime/executor -run 'Signature|Encrypted|Thinking|Replay'`
