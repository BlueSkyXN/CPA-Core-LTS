# internal/redisqueue navigation card

`internal/redisqueue/` owns the Redis-compatible usage queue plugin, in-memory queue, usage toggle, retention behavior, and queue wire payload.
Read this card before changing `/v0/management/usage-queue`, queue retention, event JSON fields, success/failure mapping, response headers, or token breakdown.
Key files: `plugin.go`, `queue.go`, `usage_toggle.go`, `*_test.go`.

## Local invariants

- Usage queue is additive; it must not replace built-in `internal/usage` full statistics.
- Payload fields such as `auth_index`, `latency_ms`, `tokens`, success/failure status, endpoint, request ID, and response headers are downstream-facing.
- Token totals should follow the same normalization semantics as the full usage logger.
- Async delivery must not depend on recycled Gin contexts.

## Local rules

- Schema changes require checking `docs/lts/core-feature-contracts.yaml`, Management API route registration, and any external dashboard assumptions.
- Retention/toggle changes require checking config defaults and watcher diff output.

## Do not

- 不要 publish raw prompt, raw response, token, auth file content, management secret, or Authorization headers in queue payloads.
- 不要 make queue availability a prerequisite for core request handling.
- 不要 remove `/v0/management/usage-queue` from the LTS contract.

## Validation

- `go test ./internal/redisqueue`
- Route/config changes: `go test ./internal/api ./internal/config ./internal/watcher/diff`
- Contract guard: `scripts/check-lts-contract.sh`
