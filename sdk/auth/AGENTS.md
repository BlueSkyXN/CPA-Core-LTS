# sdk/auth navigation card

`sdk/auth/` is the public provider-login layer and file token store used by CLI commands and embedders.
Read this card before changing exported authenticator/login options, refresh lead registration, plugin auth parsing, token-store selection, file serialization, or delete/list behavior.
Key files: `interfaces.go`, `manager.go`, provider files, `filestore.go`, `store_registry.go`, `refresh_registry.go`.

## Local invariants

- `LoginOptions.NoBrowser`, callback port, prompt function, project ID, and provider metadata remain caller-controlled; authenticators must honor noninteractive/no-browser flows.
- `Manager.Login` may return a valid auth record without a saved path when no store is configured; persistence errors return the record plus error for caller-controlled recovery.
- File token storage preserves auth weight validation, `disabled` metadata, `0700` directories, `0600` files, source/path attributes, and plugin multi-auth expansion semantics.
- Plugin auth parsers may intentionally handle/suppress built-in parsing. Registration remains safe for concurrent reads and never exposes raw auth JSON outside the approved parser contract.
- Global token-store and refresh-lead registries stay compatible with `internal/auth`, watcher synthesis, Management auth-file handlers, and `sdk/cliproxy/auth`.

## Do not

- 不要 let a plain logical ID escape the configured auth root. Caller-supplied explicit paths (`Attributes["path"]`, absolute `FileName`/ID, or a separator-bearing delete ID) are an existing public compatibility surface; containing or removing that behavior requires an explicit migration and regression tests.
- 不要 log tokens, OAuth codes, service-account JSON, raw auth files, or provider login responses.
- 不要 make public login helpers depend on process-global browser/UI state when the caller supplied options.

## Validation

- `go test ./sdk/auth`
- Provider internals: `go test ./sdk/auth ./internal/auth/...`
- Watcher/Management integration: `go test ./sdk/auth ./internal/watcher/... ./internal/api/handlers/management -run 'Auth|OAuth|Token|File'`
