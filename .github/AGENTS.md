# .github navigation card

`.github/` owns repository automation: PR guards, LTS contract checks, build smoke, Docker image publishing, and release workflows.
Read this card before modifying any workflow, PR template, permissions block, trigger, release asset logic, or path guard.
Key files: `workflows/agents-md-guard.yml`, `workflows/pr-path-guard.yml`, `workflows/lts-contract.yml`, `workflows/pr-test-build.yml`, `workflows/docker-image.yml`, `workflows/release.yaml`, `pull_request_template.md`.

## Why this is high-risk

- Workflow changes can publish artifacts, close PRs, change required checks, or alter LTS protected contract enforcement.
- `release.yaml` uses tag-triggered `gh release` upload paths and `GITHUB_TOKEN`.
- `docker-image.yml` can publish container images, so trigger and tag logic matter.
- `pr-test-build.yml` refreshes `internal/registry/models/models.json` from `router-for-me/models.git` before build.

## Required before changes

- Read the target workflow end to end and confirm its `on`, `permissions`, and external network/API usage.
- For LTS guard changes, also read `docs/lts/AGENTS.md` and `scripts/check-lts-contract.sh`.
- For release changes, identify whether a local check is only static/build validation because real release requires tag context and token.

## Do not

- 不要 weaken `agents-md-guard.yml` without explicit user request.
- 不要 add `pull_request_target` checkout/execution of untrusted PR code.
- 不要 run release upload, tag creation, or artifact publish commands locally unless the user explicitly asks.

## Validation

- YAML/static review of the changed workflow.
- LTS workflow changes: `scripts/check-lts-contract.sh`.
- Build workflow changes: `go build -o test-output ./cmd/server && rm -f test-output`.
- Release workflow changes are not fully reproducible locally; state remaining release-context risk in the final report.
