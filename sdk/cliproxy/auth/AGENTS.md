# sdk/cliproxy/auth navigation card

`sdk/cliproxy/auth/` owns runtime credential registration, selection, execution, cooldown/result classification, session affinity, Home dispatch, model fallback, refresh, and retry policy.
Read this card after `sdk/cliproxy/AGENTS.md` before changing auth state, selector/scheduler, conductor execution, error classification, fallback, or Home lifecycle.
Key files: `conductor*.go`, `selector.go`, `scheduler.go`, `classification.go`, `cooldown_state.go`, `codex_model_fallback.go`, `codex_rate_limit_continuity.go`, `home_*.go`.

## Local invariants

- Auth identity/index, provider, model alias, selected model, session key, and result attribution remain stable across selection, execution, callbacks, usage, and Management views.
- Cooldown and retry depend on typed/request-scoped classifications. Connection-lifecycle/cancellation/local preparation failures must not become quota cooldown merely from matching error text.
- Stream retries/fallbacks occur only before first delivered payload; established sessions, pinned auth, Home selections, and selected-auth callbacks preserve per-dispatch ownership and dedup contracts.
- Codex model fallback is opt-in and trigger-typed. It must not replay model-private reasoning across models unless the configured continuity mode and context-reset evidence allow it.
- Rate-limit continuity distinguishes fresh blocked observations from confirmed shared cooldown and preserves established/incumbent work until the protected confirmation rules are met.
- Home dispatch/attempt/release and local selection are different lifecycles; ambiguous Home dispatch, unauthorized refresh, in-flight accounting, and release flushing must not double-dispatch or leak capacity.
- Manager locks do not cover plugin scheduler callbacks, executor network calls, or long-running refresh work.

## Do not

- 不要 mark an auth unavailable, cool it, or remove session state solely from an untyped string match.
- 不要 rotate auth/model after response commitment, reuse stale Home selections, or publish selected-auth metadata more than once for the same actual dispatch/attempt. Fallback/retry may legitimately report source attempts and the final target according to the collector contract; never report a target that was not dispatched.
- 不要 expose raw token/auth metadata to scheduler plugins, callbacks, logs, or errors.

## Validation

- `go test ./sdk/cliproxy/auth`
- Selection/cooldown/race changes: `go test -race ./sdk/cliproxy/auth`
- Codex resilience: `go test ./sdk/cliproxy/auth -run 'CodexModelFallback|CodexRateLimitContinuity|HedgedRetry|RetryWithoutPenalty|SessionAffinity'`
- Home changes: `go test ./internal/home ./sdk/cliproxy/auth -run 'Home|Concurrency|InFlight|Unauthorized|Dispatch'`
- Cross-layer execution changes: `go test ./sdk/api/... ./internal/runtime/executor`
