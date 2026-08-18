# sdk/access navigation card

`sdk/access/` is the public inbound-request authentication provider chain used by the server and embedders.
Read this card before changing exported provider/result/error types, registry ordering, exclusive-provider behavior, principal metadata, or manager aggregation.
Key files: `registry.go`, `manager.go`, `types.go`, `errors.go`, `docs/sdk-access*.md`, `internal/access/`.

## Local invariants

- `Provider.Authenticate` distinguishes `not_handled`, `no_credentials`, `invalid_credential`, and internal errors; manager traversal and HTTP status behavior depend on those exact classes.
- Providers are evaluated in stable registration order. Exclusive mode restricts the snapshot only when the named provider exists; clearing it restores the registered chain.
- A nil/empty manager means access control is disabled for the embedding caller; do not turn that into implicit rejection without an API decision.
- `Result.Principal` may feed API-key attribution and request context. Never place raw credentials in metadata or logs unless the established config API-key contract requires the matched principal internally.
- Registry mutation and manager snapshots remain concurrency-safe and defensively copied.

## Do not

- 不要 rename exported error codes, interfaces, fields, or default provider identifiers without compatibility handling.
- 不要 make a provider claim requests it does not recognize; return `not_handled` so later providers can run.
- 不要 log headers, query credentials, API keys, or principals.

## Validation

- `go test ./sdk/access ./internal/access/...`
- Server/SDK integration: `go test ./internal/api ./sdk/cliproxy -run 'Access|AuthProvider|APIKey'`
