# 2026-10-04 v8 candidate review fixes

This follow-up fixes the independently reproduced issues in the draft candidate; it does not approve a main merge, release, or UAT/PRO deployment. Core PR #290 and companion Panel PR #98 continue to target `v8-dev`.

## Protected delta review

- Configuration reads expose a persisted-document revision. All v8 config mutations require an exact `If-Match`, checked under the same Handler lock as persistence. Missing/stale revisions return 428/412 without mutation. Legacy raw YAML accepts optional conditional writes; cached v0 editors still cannot replace a v8/mixed document. CORS exposes `ETag`; clients reload after successful writes. See [the compatibility contract](v8-development-compatibility.md#配置条件写入).
- The companion Panel binds YAML saves to immutable read snapshots and native provider-group writes to document revisions. Opened provider forms compare their original normalized snapshot even after global configuration refresh; whole-record enable/disable changes also carry a snapshot. Sponsor forms check their aggregate before the first protocol write. Group metadata, unknown fields, inheritance, connection generation and no-fallback behavior remain protected.
- Codex HTTP and WebSocket non-stream conversions publish successful primary usage only after conversion succeeds. Empty/tool-input-error conversions return 502 with failed usage retaining upstream token counts. Compact failures retain tokens too. Deferred failure reporting cannot duplicate the primary record. Independent HTTP image-tool consumption remains separately attributed, including when final main-response conversion fails.
- No changes to abnormal-reasoning retry/finalizer policy, affinity/replay cache ordering, streaming timeout policy, usage export v3, Panel release source, module baseline, or historical release-tag exceptions.
- Responses plugin tests filter on a unique request trace rather than provider name alone. A deterministic dispatcher barrier queues a previous streaming event before registration of the next capture; the next non-stream test accepts only its own event. No sleeps or weakened token/stream assertions.

## Local verification

- Final Core tree: `go test -mod=readonly ./... -count=1`, 108 packages with tests passed. The preceding full run also passed after the WebSocket fix.
- `go test -mod=readonly -race ./internal/api/handlers/management ./internal/runtime/executor ./sdk/api/handlers/openai -run 'ConfigRevision|CodexNonStreamTranslationUsageOutcome|CodexExecutorExecutePublishesMainUsageBeforeImageUsage|Responses.*Plugin|ResponsesUsageCapture' -count=5` passed.
- Responses plugin routing/capture tests additionally passed 30 consecutive repetitions.
- `go vet ./internal/api/... ./internal/runtime/executor/... ./sdk/api/handlers/openai`, `scripts/check-lts-contract.sh`, server build and `git diff --check` passed.
- Companion Panel `npm run validate:lts` passed: configured test suites, contract guards, type checking, lint and single-file build. Lint retains one pre-existing `useConnectivityTest.ts` effect-cleanup warning; no lint errors. API-client suite has 38 tests, including real workbench hook snapshot forwarding, sponsor preflight, unchanged-snapshot success and final-read conflict without replay.
- Isolated local Core processes, synthetic credentials only: v8 missing revision 428, stale revision 412, simultaneous writes 200/412, CORS revision visibility, usage/export 200 and v0 raw-write guard 409 passed. Actual Panel API modules passed native-group save, stale form rejection, stale YAML rejection and fresh YAML save against the running Core. A separate legacy-config instance passed provider/YAML writes without implicit migration.
- Browser automation opened the local `management.html` login page and filled the synthetic key, but login click operations timed out and no authenticated GUI flow was accepted. This is a **GUI validation gap**, not a passing browser result and not a proven product regression. Local API integration is not a substitute for the remaining GUI gate.

## Remaining release gates

The candidates stay Draft. Full authenticated GUI regression, native/no-plugin distribution checks, cross-version/plugin/platform matrices, mounted-file/short-write/process-interruption fault injection and real v8 staging/rollback remain separate acceptance work. Configuration revisions are not a cross-process filesystem transaction; unconditional v0 writers and multi-protocol sponsor operations are not made transactional by this patch. No UAT/PRO state, real credentials, release assets, tags or user clients were changed.
