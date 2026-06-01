## Type

- [ ] Protected upstream full-sync
- [ ] Staged upstream full-sync
- [ ] LTS protected-delta patch
- [ ] Maintenance docs / CI
- [ ] Emergency hotfix

## Upstream sync info

- Upstream repo:
- Upstream branch:
- Upstream from SHA:
- Upstream to SHA:
- Sync branch:

## Protected delta checklist

- [ ] `usage-statistics-enabled` still exists
- [ ] `internal/usage/` still exists
- [ ] `/v0/management/usage` still exists
- [ ] `/v0/management/usage/export` still exists
- [ ] `/v0/management/usage/import` still exists
- [ ] CPA-Panel-LTS response shape reviewed
- [ ] Local auth/config/panel/release/source customizations reviewed

## Conflict resolution notes

Describe any downstream resolution made to preserve LTS behavior.

## Validation

- [ ] `scripts/check-lts-contract.sh`
- [ ] usage / management contract tests
- [ ] `go build ./cmd/server`
- [ ] `go test ./...`

## Optional real runtime smoke

- [ ] Hugging Face Space smoke was run, or explicitly skipped with reason
- Space:
- Runtime SHA:
- CPA-Core-LTS branch/SHA deployed:
- `/healthz` status:
- `/management.html` status:
- `/v0/management/usage*` status:

Do not paste management passwords, API keys, auth files, OAuth tokens, or Hugging Face secrets here.
