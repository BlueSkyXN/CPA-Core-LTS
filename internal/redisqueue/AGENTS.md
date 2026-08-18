# internal/redisqueue navigation card

`internal/redisqueue/` owns the Redis-compatible usage queue plugin, in-memory queue, usage toggle, retention behavior, and queue wire payload.
Read this card before changing `/v0/management/usage-queue`, queue retention, event JSON fields, success/failure mapping, response headers, or token breakdown.
Key files: `plugin.go`, `queue.go`, `usage_toggle.go`, `*_test.go`.

## Local invariants

- Usage queue is additive; it must not replace built-in `internal/usage` full statistics.
- Payload fields such as `auth_index`, `latency_ms`, `tokens`, success/failure status, endpoint, request ID, and response headers are downstream-facing.
- Token totals should follow the same normalization semantics as the full usage logger.
- Async delivery must not depend on recycled Gin contexts.
- This package implements a Redis-compatible HTTP/wire queue, not an external Redis persistence client. With subscribers it broadcasts directly; without them it retains bounded in-memory events according to current semantics.
- Disable clears retained events and closes subscribers; slow subscribers may be removed. Error events remain broadcast-only where the current protocol specifies it.
- Response headers are copied and filtered to remove Authorization, Proxy-Authorization, Cookie, and Set-Cookie values.

## Local rules

- Schema changes require checking `docs/lts/core-feature-contracts.yaml`, Management API route registration, and any external dashboard assumptions.
- Retention/toggle changes require checking config defaults and watcher diff output.

## Do not

- 不要 publish raw prompt, raw response, token, auth file content, management secret, or Authorization headers in queue payloads.
- 不要 make queue availability a prerequisite for core request handling.
- 不要 let the queue toggle drift from the shared `usage-statistics-enabled` recording contract.
- 不要 remove `/v0/management/usage-queue` from the LTS contract.

## Validation

- `go test ./internal/redisqueue`
- Queue/API behavior: `go test ./internal/redisqueue ./internal/api -run 'Redis|redis|Usage|usage'`
- Route/config changes: `go test ./internal/api ./internal/config ./internal/watcher/diff`
- Contract guard: `scripts/check-lts-contract.sh`
