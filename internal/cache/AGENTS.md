# internal/cache navigation card

`internal/cache/` owns bounded local signature/reasoning replay caches and their Home KV-backed equivalents.
Read this card before changing cache keys, scope resolution, TTL, CAS/generation logic, signature bypass, or replay append/delete behavior.
Key files: `signature_cache.go`, `codex_reasoning_replay_cache.go`, `codex_reasoning_replay_scope.go`, provider replay caches, `bounded_lru.go`.

## Local invariants

- Replay state is scoped by the existing model/session identity; do not widen a cache key to share provider-private signatures or reasoning across unrelated models, auths, or sessions.
- When Home mode owns a required read/write, Home failure or miss must follow the explicit required/best-effort contract; do not silently fall back to stale local state.
- Codex append preserves cumulative turn boundaries and bounded retention; Antigravity conditional mutation preserves generation/CAS fencing against stale and ABA writers.
- Signature validation and special sentinels stay aligned with `internal/signature`, translators, and executors.
- Cache values and logs must not expose raw credentials, prompts, or full private reasoning payloads.

## Do not

- 不要 change key prefixes, hashing inputs, TTL, tombstone, or eviction semantics without migration/compatibility reasoning and focused tests.
- 不要 replay `reasoning.encrypted_content` or thinking signatures across a model-fallback boundary unless the existing typed continuity policy explicitly permits it.

## Validation

- `go test ./internal/cache`
- Signature changes: `go test ./internal/cache ./internal/signature ./internal/translator/...`
- Codex replay/fallback changes: `go test ./internal/cache ./internal/runtime/executor ./sdk/cliproxy/auth -run 'Codex.*Replay|Codex.*Signature|CodexModelFallback'`
