# internal/codexmetadata navigation card

`internal/codexmetadata/` normalizes Codex `client_metadata`, applies workspace privacy policy, regenerates compatibility projections, and redacts metadata for logs.
Read this card before changing repair/strict/off mode, workspace policy, canonical turn metadata, derived headers, session identity, or log sanitization.
Key files: `request.go`, `log.go`, `errors.go`, and their tests.

## Local invariants

- `client_metadata.x-codex-turn-metadata` is the canonical source when present; flat body/header fields are compatibility projections, not independent authority.
- `strict` rejects malformed, duplicate, ambiguous, or conflicting carriers; `repair` normalizes known-safe input; `off` does not mutate payloads but may derive unambiguous state.
- Workspace `passthrough`, `redact`, and `drop` are distinct public policies. Redaction remains deterministic within its configured scope without exposing original paths/remotes.
- Projection values must pass header safety and size limits. Validation errors are safe request-scoped 400s and never echo untrusted metadata.
- Log sanitizers clone input, remove workspace enrichment, mask derived session/window/parent/subagent headers, and fail closed on malformed/ambiguous canonical data.

## Do not

- 不要 trust a flat session/window/subagent projection over conflicting canonical metadata.
- 不要 loosen depth/count/size bounds or include workspace paths/remotes or derived session/window/parent/subagent header values in logs; any broader metadata reduction is a separate privacy-contract change.

## Validation

- `go test ./internal/codexmetadata`
- Outbound integration changes: `go test ./internal/runtime/executor ./sdk/cliproxy/auth -run 'ClientMetadata|Workspace|TurnMetadata|Identity'`
