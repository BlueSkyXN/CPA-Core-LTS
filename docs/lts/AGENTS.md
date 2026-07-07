# docs/lts navigation card

`docs/lts/` owns the CPA-Core-LTS maintenance contract registry, protected delta policy, protected full-sync runbook, and LTS-specific operational guides.
Read this card before changing contract markers, sync workflow instructions, validation gates, or feature registry entries.
Key files: `sync-runbook.md`, `protected-deltas.yaml`, `core-feature-contracts.yaml`, `codex-client-context-degradation-defense.md`, `scripts/check-lts-contract.sh`.

## Local invariants

- Core maintenance mode is protected full-sync, not Panel-style selective-port.
- `core-feature-contracts.yaml` is the feature registry; `protected-deltas.yaml` is the product boundary and sync policy.
- `scripts/check-lts-contract.sh` is a sentinel gate, not a replacement for behavior-level usage/API tests.
- Registry entries should protect stable contracts: routes, config keys, JSON fields, release asset names, directory boundaries, and review-adjacent seams.
- `codex-client-context-degradation-defense.md` is the canonical user/developer guide for `codex.abnormal-reasoning-retry` configuration semantics. It explains code defaults, HFS deployment recommendations, candidate delivery policy, fallback policy, and client usage shaping. It is not a local research-note index and should not cite `local/` materials as operational source of truth.

## Local rules

- Adding a protected feature requires a clear reason why it is an LTS product boundary, not one sync's temporary concern.
- If YAML markers change, update `scripts/check-lts-contract.sh` in the same change.
- Sync runbook edits must keep preflight, rehearsal, validation, PR body, merge policy, and HF smoke responsibilities distinct.
- When `codex.abnormal-reasoning-retry` config keys, defaults, delivery/fallback behavior, client usage aggregation, or hedged retry semantics change, update `codex-client-context-degradation-defense.md` alongside `config.example.yaml` and contract markers as needed. If Panel config surface changes, call out the `CPA-Panel-LTS` follow-up explicitly.

## Do not

- 不要 remove full usage statistics, Management usage API, panel release asset, auth attribution, config compatibility, or usage queue from the protected contract.
- 不要 make the guard script assert brittle local implementation details that normal upstream syncs will churn.

## Validation

- `scripts/check-lts-contract.sh`
- Contract behavior changes: `go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'`
