# 2026-10-05 v8-native follow-up fixes

## Scope

PR #290 remains a draft against `v8-dev`. This round follows the 2026-10-05 product decision that the v8 LTS line supports only the v8 configuration layout and the companion v8 Panel; users reconfigure from the v8 tutorial. It fixes the defects from the fixed-coordinate review of `a7cfe4658ea60f4d4dac19636a2be099f1733d4c` that affect v8 delivery. Defects that only concern legacy/mixed configuration editing (historical object aliases, mixed empty-container precedence) and the v7/v8 paired migration matrix are out of scope by that decision; upstream's legacy-read code is kept unchanged.

## Changes

| Area | Change | Origin |
| --- | --- | --- |
| Config | `plugins.configs` stays opaque to the v8 layout helpers: no whole-tree map decode or alias expansion inside it; merge keys compare tag and value; recursive aliases are still rejected. | upstream v8 |
| Executors | Local translation failures reported with the shared apply_patch sentinel are request-scoped: no credential/model cooldown and no replay on another credential. | upstream + LTS Codex |
| Codex abnormal retry | Pass-through fallback candidates are attached only after a checked translation; the finalizer keeps the validated payload if its re-translation fails; exhaustion without a deliverable candidate returns an error. | LTS |
| Usage | Discarded abnormal-retry attempts still publish image tool usage (stream and non-stream); completed Codex HTTP responses without usage publish a zero-token success; the statistics logger and usage queue derive outcome from the record (`Failed` or `Fail.StatusCode >= 400`), not the final inbound status. | LTS + upstream queue |
| Auth | Same-credential re-synthesis keeps the terminal unauthorized state (401 + `invalid_grant`); new credential material clears it. | upstream |
| Plugins | The generic plugin executor adapter converts responses with a checked state before reporting success; conversion failures are request-scoped 502s with failed usage. | LTS |
| Images | OpenAI-compatible non-stream image generations/edits return the provider status error for non-2xx responses. | upstream |
| Management | Redacted TURN credential restores that cannot be matched (duplicate URL lists whose entry count changed) fail with 422 `ambiguous_turn_credentials`. | upstream v8 |
| Interceptors | `Path` overrides must keep the operation (or switch image generations/edits); others fail with a request-scoped `RequestPathOverrideError`. | upstream |
| Translators | apply_patch bridges reject final snapshots that disagree with completed streamed arguments; Codex to Chat Completions completes a missing tail instead of truncating, and reports conflicts as tool-input errors. | upstream |
| Docs | `config.example.yaml` Antigravity/Devin examples moved under `oauth.providers`; response-steering boundaries inline; `management-api-v8.md` documents revisions/If-Match and the v8-only contract; SDK docs use `/v8` and `sdk/config`. | mixed |
| Tests | Streaming request-log worker race removed; batch auth-registration fixture identifies the blocked auth by ID. | upstream / LTS fixture |

Upstream-origin deltas are registered in `downstream-patches.yaml` with retire conditions. Each defect fix carries a regression test that was confirmed to fail on the previous code.

## Local verification

- `go test -count=1 ./...`: all packages pass except `internal/discovery` `TestAdvertiserAndBrowser_Integration` (local mDNS multicast; it fails identically on the unmodified baseline in this environment).
- `go test -race` on `sdk/cliproxy/auth`, `internal/runtime/executor`, `internal/usage`, `internal/redisqueue`, `internal/pluginhost`, `internal/config`, `internal/api/handlers/management`, `internal/api/middleware`, `sdk/cliproxy`, `sdk/api/...`: pass.
- `go vet ./...`, `scripts/check-lts-contract.sh`, `go run ./scripts/ltsregistry --root .`, server build: pass.
- 27 standalone plugin/example modules (`go test -mod=readonly ./...`): pass.
- Coding Plan native dynamic-load runner (`examples/plugin/zcode-coding-plan/test-cpa.sh`, synthetic credentials): intermittent isolated-subprocess timeouts occur after the plugin is unloaded or after the child already reported PASS (process exit does not complete within 30s). Observed 4/25 runs on this head and 1/22 runs on the unmodified `a7cfe465` baseline with the same signature, while 80 direct runs of the affected child tests did not hang. This is treated as a pre-existing dynamic-library exit flake, not an attributed regression; the higher observed rate on this head is not statistically conclusive and remains a follow-up.

## Remaining release gates

Authenticated GUI regression against the companion Panel, native/no-plugin distribution checks, real-credential refresh and quota behavior, fault injection and v8 staging/rollback remain separate acceptance work. No UAT/PRO state, real credentials, release assets, tags or user clients were changed.
