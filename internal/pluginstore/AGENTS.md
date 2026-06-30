# internal/pluginstore navigation card

`internal/pluginstore/` owns plugin registry parsing, GitHub release lookup, archive selection, checksum validation, and plugin binary installation.
Read this card before changing registry URLs, plugin metadata validation, GitHub API calls, checksum policy, archive layout, platform selection, or install overwrite behavior.
Key files: `registry.go`, `github.go`, `install.go`, `checksum.go`, `version.go`.

## Why this is high-risk

- Plugin installation downloads executable code from remote GitHub releases.
- Windows loaded-plugin overwrite behavior can fail or corrupt an active runtime if not handled carefully.
- Registry and version validation are user-visible Management API behavior.

## Required before changes

- Check `internal/pluginhost/AGENTS.md` for plugin ID, platform, and loaded-plugin behavior.
- Keep network calls context-aware and testable through injected HTTP clients.
- Preserve checksum and archive validation unless the user explicitly accepts a weaker policy.

## Do not

- 不要 execute downloaded plugin binaries during install validation.
- 不要 overwrite a loaded Windows plugin without the existing `BeforeWrite`/loaded-plugin guard path.
- 不要 log GitHub tokens or private release URLs.
- 不要 accept path traversal or absolute paths from plugin archives.

## Validation

- `go test ./internal/pluginstore`
- Host integration changes: `go test ./internal/pluginhost`
- Management plugin-store endpoint changes: `go test ./internal/api/handlers/management -run Plugin`
