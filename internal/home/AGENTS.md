# internal/home navigation card

`internal/home/` implements the Home control-plane client for config subscription, auth dispatch, membership/failover, KV commands, in-flight snapshots, concurrency release, and plugin status.
Read this card before changing Home wire commands, lifecycle state, cluster discovery, dispatch classification, KV semantics, heartbeat, or release frames.
Key files: `client.go`, `requests.go`, `concurrency_release.go`, `kv_helpers.go`, `certificate.go`, `plugin_status.go`.

## Local invariants

- Home settings are runtime-only and originate from the Home JWT/control plane; ordinary YAML parsing must not synthesize or persist `HomeConfig`.
- Dispatch distinguishes deterministic pre-send failure from ambiguous post-send/read failure. Ambiguity fences takeover/redispatch until the lifecycle proves it safe.
- Membership, command pools, subscriber lifetime, heartbeat readiness, cluster failover, and global `Current()` publication move together; stale lifetimes must not become current.
- Credential in-flight and concurrency release frames are wire contracts. Release flushing is cumulative, retryable, bounded on shutdown, and must not lose a sequence during concurrent updates.
- `Required` KV helpers surface Home-mode errors/misses; `BestEffort` helpers may swallow errors only with secret-safe key-prefix logging. CAS, NX, expiry, and miss semantics are not interchangeable.
- Plugin status and other dedicated command paths retain their own timeout/cancellation behavior rather than inheriting an unrelated base timeout.

## Do not

- 不要 log Home JWT, full Redis/Home keys, dispatched credentials, certificates, or raw control-plane payloads.
- 不要 add local fallback after an ambiguous dispatch or required Home KV failure.
- 不要 update Home protocol fields without checking `sdk/cliproxy/service_home.go`, `sdk/cliproxy/auth`, and executor/Home tests.

## Validation

- `go test ./internal/home`
- Home service/auth changes: `go test ./internal/home ./sdk/cliproxy/...`
- Wire or concurrency lifecycle changes: `go test -race ./internal/home ./sdk/cliproxy/auth -run 'Home|Concurrency|InFlight|Release|Membership'`
