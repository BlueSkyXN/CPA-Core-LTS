# internal/managementasset navigation card

`internal/managementasset/` owns download, caching, and update behavior for the bundled `management.html` control panel asset.
Read this card before changing panel release source, fallback URL, asset name, cache path, auto-update behavior, or Management panel serving assumptions.
Key files: `updater.go`, `updater_test.go`.

## Why this is high-risk

- CPA-Core-LTS must default to `BlueSkyXN/CPA-Panel-LTS`, not the upstream panel source.
- `/management.html` is part of the LTS distribution contract and HF Space smoke.
- Network fetching and local caching behavior affects startup, Docker/HF deployment, and remote-management config.

## Required before changes

- Confirm whether the change affects `DefaultPanelGitHubRepository`, `defaultManagementReleaseURL`, `defaultManagementFallbackURL`, or `managementAssetName`.
- Check `internal/config/AGENTS.md` if config keys or defaults are involved.
- Check `internal/api/AGENTS.md` if serving routes or auth behavior are involved.

## Do not

- 不要 change the default source away from `BlueSkyXN/CPA-Panel-LTS` without explicit user request and Panel compatibility verification.
- 不要 assume network is available during local validation or startup.
- 不要 log release tokens or private download URLs.

## Validation

- `go test ./internal/managementasset`
- Contract guard: `scripts/check-lts-contract.sh`
- Serving/build smoke: `go build -o test-output ./cmd/server && rm -f test-output`
