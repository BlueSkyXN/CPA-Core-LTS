# sdk/cliproxy navigation card

`sdk/cliproxy/` is the public embeddable SDK for constructing and running the proxy core from other Go programs.
Read this card before changing public types, builder/service behavior, auth conductor, runtime provider binding, watcher integration, pluginhost wiring, or usage plugin contracts.
Key files: `builder.go`, `service.go`, `types.go`, `providers.go`, `rtprovider.go`, `auth/`, `usage/`, `executor/`, `pipeline/`.

## Local invariants

- Public structs, methods, and package paths are external API; avoid breaking changes unless user explicitly asks for a major contract change.
- SDK auth conductor behavior must stay compatible with core auth manager, watcher updates, provider availability, cooldown, quota state, and recent request tracking.
- Changes inside `auth/` must also read `auth/AGENTS.md`; Home, model fallback, rate-limit continuity, and retry semantics are narrower contracts than this parent card.
- SDK usage records feed `internal/usage`; preserve token, auth index, source, latency, model, and failed state where available.
- `sdk/config`, `internal/config`, `internal/pluginhost`, and `internal/watcher` compatibility matters when embedding the service.

## Local rules

- Prefer additive API changes over renames/removals.
- If behavior changes, update or check `docs/sdk-*.md` and `examples/custom-provider`.
- Keep tests close to the SDK package that owns the behavior; auth scheduler/conductor changes need focused tests.

## Do not

- 不要 expose internal-only structs as public SDK surface without a clear compatibility reason.
- 不要 make SDK startup require global process state, network calls, or interactive OAuth unless already configured by caller.
- 不要 duplicate core auth/executor/plugin logic in SDK when an existing core helper can be reused safely.

## Validation

- `go test ./sdk/cliproxy/...`
- Public behavior changes should also run affected core package tests and `go test ./examples/...` if examples compile.
