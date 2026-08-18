# cmd/server navigation card

`cmd/server/` owns the main CLI/server entrypoint, startup modes, build metadata, login flags, TUI/standalone flow, Home mode, plugin bootstrap, and local service startup.
Read this card before changing flags, mode selection, config loading, OAuth entrypoints, warning-only server behavior, or build metadata injection.
Key files: `main.go`, `main_test.go`.

## Local invariants

- Build metadata variables `Version`, `Commit`, and `BuildDate` are set by Docker/release build flags; keep fallback values safe.
- `--no-browser` must be honored by OAuth login flows.
- `--tui` without `--standalone` is a client flow; `--tui --standalone` starts an embedded local server.
- Cloud/Home mode config loading must not silently require local `config.yaml` when config is supplied by the remote store.
- Plugin command-line flags are registered before parse and executed through `pluginHost`.
- Warning-only server behavior for unsafe example API keys must not mask a valid production config.

## Local rules

- New flags need source help text, parsing, and tests for mode interaction when behavior changes.
- Config path changes require checking `internal/config/AGENTS.md` and `internal/watcher/AGENTS.md`.
- Login/auth changes require checking `internal/auth/AGENTS.md`.
- Home startup/JWT/config subscription changes require checking `internal/home/AGENTS.md` and `sdk/cliproxy/AGENTS.md`.

## Do not

- 不要 add interactive prompts to default server startup.
- 不要 log password, home JWT, OAuth code, management secret, or raw auth file content.
- 不要 call `os.Exit`/fatal logging from helper logic that can return an error.

## Validation

- `go test ./cmd/server`
- Startup/config behavior changes: relevant `internal/config`, `internal/watcher`, or `internal/auth` tests.
- Build smoke: `go build -o test-output ./cmd/server && rm -f test-output`.
