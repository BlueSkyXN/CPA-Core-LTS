# 2026-10-03 v8 development follow-up

## Scope

PR #290 remains a draft against `v8-dev`. This is not a main merge, release, UAT deployment or production migration approval. Companion Panel selective-port work is PR #98.

- Previous candidate: `db0d6f74802c090225ab0a75276bee3b7a6d7814`.
- Previous official boundary: `2044a01f422998de79a5da8015141b878886534d`.
- Frozen official main target: `d7914afdedca7af95ee974a42453dc49fc1388ce`.
- Stage A merge: `66146828`, through `d4692663be0bbc370e53110f95fd319ce64f0f82` (six commits).
- Stage B merge: `4ba6c6d5a3cf51d0ea86da342d497c61e6e5d731`, through `d7914afd` (five commits).
- All eleven official commits are ancestors. No squash/rebase/file-tree replacement was used.

## Protected delta review

1. Claude JSON prefilter and 408 timeout mapping preserve the existing pre-commit error and SSE boundaries. Auth Register/Update snapshots are cloned under the manager lock before scheduler callbacks; LTS identity, persistence, Flow and generation behavior remain.
2. Antigravity model updates are confined to its catalog segment. Official dev-only restoration of Claude 4.6 is not silently mixed into the main target.
3. Devin early content/late signature behavior, Interactions Responses summary lifecycle, Claude search sources and bounded tool-schema integer/union normalization are absorbed with their tests.
4. The only textual Stage B conflict is `internal/runtime/executor/codex_executor_execute.go`: LTS already publishes main usage before image-tool usage at its retained abnormal-retry boundary. The duplicated upstream publication block is not added. The implementation remains identical to Stage A at that seam.
5. The image usage regression additionally asserts distinct main/image request IDs, shared trace/auth attribution, and no guessed image response/upstream model evidence. This does not claim a new audit of every pre-existing Codex translation-failure publication path.
6. Full usage, canonical-v3 import/export, timing semantics, usage queue, auth selection/Flow/Home, plugin permissions/readiness/drain, config compatibility and the CPA-Panel-LTS download source are retained. The 71-entry downstream patch ledger remains 59 required / 9 removable / 3 retired; no automatic retirement or guard weakening.

## Verification

At the merged source tree:

- `go test -mod=readonly -count=1 ./...` passes.
- `go test -mod=readonly -race -count=1` passes for auth conductor, executor/helps, usage, redisqueue, pluginhost, config, management, Interactions Responses and Claude/Codex translators.
- Targeted `go vet`, `scripts/check-lts-contract.sh`, `go run ./scripts/ltsregistry --root .`, and server build pass.
- 27 standalone plugin/example Go modules pass readonly tests. The zcode coding-plan integration directory is a runner template, not a standalone module; the native `test-cpa.sh` runner passes. An initial generic enumeration incorrectly tested that template directly and failed on missing module sums; it was reclassified rather than changing dependency policy.
- Temporary real v7/v8 Core plus Panel API checks cover provider groups, unknown metadata, explicit false/zero, deletions, usage and v0 raw-write rejection.
- Synthetic v7 baseline → v8 legacy no-write read → explicit migration → v8 restart → restored v7 configuration/usage passes; repeated usage import adds zero duplicates.

These are local/synthetic checks. Full native plugin combinations on every platform, real credential-refresh rollback, fault injection, in-flight traffic draining and v8 UAT/PRO acceptance are not certified. UI/CI head details are reported in the paired PRs, not inferred from this document.

## Watchlist

Official `upstream/dev` was `0594a632d935947002e9fae7908b5e8ea7cb0ff1`, with four history commits not in the frozen main target: `8a945b3f` (Claude 4.6 restoration), `9b9c2bdf` and `174248ca` (CAQS replay), and merge `0594a632`. They are not included and require a separate main/intake decision.

Panel official main is frozen at `752e0ee772220ce49aae1221a3f39f23236590d7`; its 92-item selective-port ledger explicitly distinguishes accepted fixes from deferred new provider/policy/credential-mutation/consumption UI. A matching Panel build is not a claim of complete official UI parity or unrestricted multi-writer configuration safety.
