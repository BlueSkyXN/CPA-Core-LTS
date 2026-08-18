# internal/homeplugins navigation card

`internal/homeplugins/` applies Home-resolved plugin install/delete work and produces node status reports.
Read this card before changing plugin sync manifests, resolved auth lifetime, artifact installation/deletion, load verification, task phases, or report JSON.
Key files: `sync.go`, `sync_test.go`, `sdk/pluginstore/`, `internal/pluginstore/`, and `internal/pluginhost/`.

## Local invariants

- Home plugin operations run only when both Home and plugins are enabled and use the resolved plugin root/platform.
- Resolved auth is temporary, expiry-bounded, and cleared after use; reports and errors never include secret values or private auth URLs.
- Install/delete honors plugin busy/unload state, manifest/platform validation, checksum/archive safeguards delegated to pluginstore, and context cancellation before destructive steps.
- Sync reports preserve stable schema/task/phase/status fields and distinguish install outcome from runtime load outcome.
- An installed plugin that fails to load is not a successful completed sync; missing delete targets remain idempotent success where the current contract says so.

## Do not

- 不要 execute a downloaded plugin as an installation check or bypass pluginstore validation.
- 不要 overwrite/delete a busy or loaded plugin outside the existing unload/BeforeWrite contract.
- 不要 retain resolved auth after completion, expiry, cancellation, or failure.

## Validation

- `go test ./internal/homeplugins`
- Store/host interactions: `go test ./internal/homeplugins ./internal/pluginstore ./internal/pluginhost ./sdk/pluginstore`
