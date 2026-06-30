# internal/pluginhost navigation card

`internal/pluginhost/` owns dynamic plugin lifecycle, ABI/RPC adapters, host callbacks, command-line plugins, scheduler hooks, management/resource routes, model registration, and stream bridges.
Read this card before changing plugin loading, callback surfaces, panic/fuse behavior, RPC schema, scheduler selection, Management plugin routes, or executor/stream adapters.
Key files: `host.go`, `abi.go`, `rpc_schema.go`, `auth_callbacks.go`, `management.go`, `scheduler.go`, `stream_bridge.go`, `command_line.go`.

## Local invariants

- Plugin IDs, dynamic library selection, ABI method names, and schema version must stay compatible with `sdk/pluginabi` and `sdk/pluginapi`.
- Plugin-provided Management routes must remain namespaced and must not override built-in Management API.
- Plugin callbacks must not expose raw credentials unless the public API contract explicitly allows safe host-mediated access.
- Scheduler hooks must run outside auth manager locks and must fall back safely when unhandled, unknown, fused, or panicking.
- Stream bridges must preserve chunk order, error propagation, and nonblocking cleanup.

## Local rules

- Public contract changes require reading `sdk/pluginapi/AGENTS.md` and `sdk/pluginabi/AGENTS.md`.
- Install/update interactions require reading `internal/pluginstore/AGENTS.md`.
- Auth callback changes require reading `internal/auth/AGENTS.md` and `internal/watcher/AGENTS.md`.

## Do not

- 不要 let plugin panics crash the core process; preserve fuse/recover behavior.
- 不要 serve plugin Management paths outside `/v0/management/plugins` or `/v0/resource/plugins` style namespaces.
- 不要 log plugin-supplied auth payloads, API keys, tokens, or full request bodies.

## Validation

- `go test ./internal/pluginhost`
- Public API/ABI changes: `go test ./sdk/pluginapi ./sdk/pluginabi`
- Scheduler/auth changes: `go test ./sdk/cliproxy/auth`
- Management route changes: `go test ./internal/api/handlers/management -run Plugin`
