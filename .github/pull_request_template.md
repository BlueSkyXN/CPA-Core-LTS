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
- [ ] `docs/lts/core-feature-contracts.yaml` reviewed for touched protected or protected-adjacent features
- [ ] Usage record creation/schema reviewed or tested: API key, auth source, model, token breakdown, latency, success/failure
- [ ] Management usage response shape reviewed or tested: `usage`, `failed_requests`, `apis`, `models`, `details`
- [ ] Export/import roundtrip reviewed or tested when usage data shape is touched
- [ ] CPA-Panel-LTS response shape reviewed
- [ ] Local auth/config/panel/release/source customizations reviewed

Protected-adjacent upstream changes requiring review even without text conflicts:

- request lifecycle
- auth identity / API key attribution
- model resolution / alias normalization
- token accounting / final usage aggregation
- logging metadata
- Management usage response shape
- config schema / hot reload
- panel asset source / release source
- registry features listed in `docs/lts/core-feature-contracts.yaml`

## Conflict resolution notes

Describe any downstream resolution made to preserve LTS behavior.

## Validation

- [ ] `scripts/check-lts-contract.sh`
- [ ] `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
- [ ] `go build -o test-output ./cmd/server && rm -f test-output`
- [ ] `go test ./...`
- [ ] `git diff --check`

## Optional real runtime smoke

- [ ] Hugging Face Space smoke was run, or explicitly skipped with reason
- Space:
- Runtime SHA:
- CPA-Core-LTS branch/SHA deployed:
- `/healthz` status:
- `/management.html` status:
- `/v0/management/usage*` status:

Do not paste management passwords, API keys, auth files, OAuth tokens, or Hugging Face secrets here.
