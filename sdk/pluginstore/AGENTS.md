# sdk/pluginstore navigation card

`sdk/pluginstore/` is the public facade for plugin registry, manifest, artifact installation, and scoped store-auth helpers.
Read this card before changing exported aliases/constants, client constructors, resolved auth, manifest/install helpers, or update/version semantics.
Key files: `pluginstore.go`, `pluginstore_test.go`, `internal/pluginstore/AGENTS.md`, `internal/homeplugins/AGENTS.md`.

## Local invariants

- Exported aliases/constants mirror `internal/pluginstore` and are compatibility surface for embedders; keep schema, install/auth types, and error identity aligned.
- Client constructors normalize configured auth; resolved auth remains request-target/kind scoped and may be expiry-bounded. The owning caller must invoke `Client.ClearAuth()` after the final request; Home sync already owns that cleanup, and the client does not clear itself automatically.
- Fetch/install methods delegate checksum, archive, path, platform, and loaded-plugin safeguards to the internal implementation without weakening them.
- Manifest/version/update helpers remain deterministic and side-effect free.

## Do not

- 不要 expose or log resolved secret values, private registry URLs, or auth headers.
- 不要 add a facade path that bypasses internal plugin validation or executes downloaded code.
- 不要 rename/remove exported aliases or constants without an explicit public compatibility strategy.

## Validation

- `go test ./sdk/pluginstore ./internal/pluginstore`
- Home/host integration: `go test ./sdk/pluginstore ./internal/homeplugins ./internal/pluginhost`
