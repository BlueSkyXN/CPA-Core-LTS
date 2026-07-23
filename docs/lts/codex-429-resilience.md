# Codex 429 resilience

This document defines the CPA-Core-LTS contract for Codex quota/capacity
fallback. It is intentionally separate from `codex.abnormal-reasoning-retry`:
the abnormal-reasoning guard handles suspicious successful responses, while
this feature handles a precisely classified upstream failure before a response
is delivered.

## Model fallback

`codex.model-fallback` is disabled by default. When enabled, Core first exhausts
the normal same-model credential selection and retry path. Only then may it try
the configured target models in order.

Supported triggers are:

- `usage-limit`: Codex `error.type=usage_limit_reached`, including its reset
  timing when present.
- `capacity`: the Codex model-capacity response that asks the client to try a
  different model.

Transient `rate_limit_error` / `rate_limit_exceeded` responses are not model
fallback signals. A bare HTTP 429 without one of the typed Codex classifications
does not activate this feature.

Example:

```yaml
codex:
  model-fallback:
    enabled: true
    triggers: [usage-limit, capacity]
    reasoning-continuity: same-model-only
    mappings:
      - from: gpt-5.6-sol
        to: [gpt-5.6-terra, gpt-5.5]
    global-targets: [gpt-5.4]
```

The source mapping is exact and case-insensitive after trimming. Target order is
significant. Duplicate and source-equivalent targets are removed in memory.
Existing config files are not rewritten to add defaults.

`global-targets` is a final, shared target list for source models whose normal
mapping did not deliver a response. It is deliberately stricter than
`mappings`: Core appends it only when a typed Codex
`error.type=usage_limit_reached` has produced a currently active, formal quota
cooldown for every eligible source credential. A `FreshBlocked` observation,
an ordinary or untyped HTTP 429, `rate_limit_error`, and
`rate_limit_exceeded` never activate the global targets. This keeps the global
fallback tied to confirmed exhaustion rather than to the HTTP status alone.
The typed cooldown provenance is retained internally on `ModelState`; when
`save-cooldown-status` is enabled it is persisted as the additive
`model_fallback_reason` field in the `.cds` record. Older records without that
field fail closed for global fallback until a new typed failure is observed.

Mapped targets keep their existing precedence. Zero-dispatch mapped targets and
mapped targets that return another typed fallback signal allow selection to
continue into `global-targets`; a real dispatched request-invalid, auth,
transient, or other unclassified target error still stops the chain. Global
targets remain inside the Codex provider boundary and must be registered under
`codex`; the word "global" means shared by all Codex source mappings, not an
implicit cross-provider route.

The fallback path is limited to standard Codex response execution. It does not
apply to compact, image, or video requests. Streaming fallback is only possible
while the upstream failure is still in the bootstrap/pre-delivery phase; once a
stream has been returned and client-visible payload can be emitted, Core does
not replace it with a different model.

## Rate-limit continuity for established sessions

`codex.rate-limit-continuity` is a separate, default-off observation state
machine for Codex `usage_limit_reached`. It is effective only when
`routing.session-affinity` is enabled and Core can extract a stable session ID.
Message-content hashes, generic user IDs, and per-request client request IDs do
not qualify for this exemption.

```yaml
routing:
  session-affinity: true
codex:
  rate-limit-continuity:
    enabled: true
    observation-window-seconds: 30
    established-success-threshold: 2
    established-session-ttl-seconds: 3600
```

The state is scoped by Codex auth ID and the auth-selection model. Successful
sessions receive process-local leases. A selector cache binding alone does not
make a session established: the request must complete successfully on that
auth/model first. The effective lease TTL is the minimum of
`established-session-ttl-seconds`, the configured
`routing.session-affinity-ttl`, and the actual `SessionAffinitySelector` cache
TTL. Replacing the selector invalidates old absolute lease deadlines.

The process-local phase is explicit:

- `Healthy`
- `FreshBlocked`
- `ConfirmedCooldown`

While `Healthy`, an admitted execution attempt is registered as incumbent
in-flight before executor dispatch. If another request reports a typed
`usage_limit_reached` before that attempt completes, its later result is still
classified as incumbent rather than as a new canary.

The first typed usage-limit from a fresh session always moves the auth/model to
`FreshBlocked`, even when no established or in-flight incumbent currently
exists. Core records normal request/error accounting but does not write the
formal `ModelState` quota cooldown or suspend the registry model. Other fresh
sessions exclude that candidate and can continue through another auth or the
configured model fallback. Established leases and incumbent in-flight sessions
remain eligible for the same auth/model; their streams are not cancelled.

`FreshBlocked` transitions are:

- An established or incumbent success renews or creates its lease and keeps the
  phase `FreshBlocked`. A success from an attempt that began before the first
  fresh 429 does not silently restore `Healthy`.
- One fresh-session canary becomes eligible after
  `observation-window-seconds`, or earlier after
  `established-success-threshold` successful established/incumbent
  completions.
- A successful canary returns the auth/model to `Healthy`.
- A canary typed usage-limit while any established lease or incumbent request
  remains preserves those leases, stays `FreshBlocked`, and starts a new
  observation window. Repeated fresh canary failures alone cannot globally
  interrupt established work.
- A canary typed usage-limit with no remaining incumbent evidence moves to
  `ConfirmedCooldown`.
- A typed usage-limit from an established or incumbent request confirms the
  shared auth/model limit immediately and moves to `ConfirmedCooldown`.

`ConfirmedCooldown` rejects admission in `begin()` as well as at the final
pre-dispatch recheck. The existing persisted cooldown remains the source of the
recovery deadline. When that cooldown expires, a generation-checked transition
returns to canary-eligible `FreshBlocked`; it does not open the auth/model to a
fresh-session stampede.

Canary reservation and every live attempt use bounded in-memory tokens that are
released on terminal result, local abandonment, cancellation, lifecycle reset,
or confirmation. Continuity observation and `ModelState` mutation run in one
`Manager.MarkResult` critical section with a fixed manager-to-continuity lock
order. Stale success and stale typed usage-limit results are record-only, so
they cannot clear or double-increment a newer cooldown. Stale non-quota results
such as `401`, `403`, `invalid_grant`, and model-not-supported still update the
normal auth/model availability state.

Non-quota canary errors are inconclusive: another canary waits for a new
observation window, and the ordinary error policy still applies to that
failure. Post-bootstrap stream failures copy the provider `RetryAfter` into the
formal cooldown. Config changes affecting the observation window, success
threshold, lease TTL, Home mode, affinity enablement/TTL, or selector clear
process-local continuity state. `Load` and selector replacement are global
snapshot boundaries. A single `CloseExecutionSession` or auth removal is scoped
to that execution session or auth, so unrelated sessions/auths and requests for
which continuity is inactive remain dispatchable. A context-reset fallback
closes the source execution session while preserving the already-admitted
target auth/model attempt; later ordered targets therefore remain eligible if
the first dispatched target returns another typed fallback error.

This feature intentionally does not persist session leases or FreshBlocked/canary
state to the cooldown store. Process restart starts from normal routing state.
Requests without a stable session ID retain the existing immediate cooldown
behavior. Transient `rate_limit_error`, model capacity, and bare HTTP 429 do not
activate the continuity state machine. Home runtime-auth dispatch is excluded;
the feature applies to normal Core-managed Codex auth files.

Session affinity and continuity are related but have distinct responsibilities:
the selector supplies a stable session-to-auth routing preference, while the
continuity state machine records which bindings actually completed and decides
whether a typed usage-limit failure is fresh-session ambiguity or confirmed
auth/model exhaustion.

## Reasoning signature boundary

Model fallback does **not** repair or migrate
`reasoning.encrypted_content`. CPA-Core-LTS can validate the transport shape of
a GPT reasoning signature, but it cannot prove that a different model can
decrypt or accept it.

The default `reasoning-continuity: same-model-only` policy therefore blocks a
cross-model fallback before target auth selection when any source-scoped state
is present:

- the translated request already contains a reasoning item; or
- the source model/session has cached Codex reasoning replay state.
- `previous_response_id` or an incremental WebSocket request;
- a pinned auth or an execution session.

This still permits normal same-model auth failover, whose replay cache is scoped
by model and session rather than auth ID.

`reasoning-continuity: context-reset` is an explicit lossy mode. It is allowed
for stateful Responses WebSocket turns only when the CPA handler has constructed
a complete transcript, verified all function/custom-tool call and output pairs,
and added its internal replay marker before any payload from that turn is sent
to the client. End-to-end WebSocket passthrough, bare incremental input, an
unpaired tool transcript, any request retaining `previous_response_id`, and an
already-delivered stream are blocked rather than guessed.

The source replay-cache preflight uses the same resolver as the Codex executor.
The executor attaches the final source model/session scope after request
interception, translation, payload shaping, and header resolution to the typed
source error. Fallback checks that exact scope before target auth selection, so
lowercase/Gin-only headers or an after-auth payload rewrite cannot bypass the
continuity gate.

For an approved reset Core removes all reasoning items and
`previous_response_id`, releases the source auth pin, closes the source
execution session, and reselects auth using the target model. If source replay
state contains function/custom-tool calls needed by tool outputs in the current
request, those replayable call items may be retained so the tool pair remains
valid. No reasoning signature is copied to the target model, and normal target
model replay injection is skipped for that fallback attempt.

This mode means "continue without model-private reasoning history". It must not
be described as signature repair, signature translation, or equivalent
reasoning continuity.

## Retry, usage, and auth state

- Same-model auth selection and ordinary retry run before model fallback.
- Per-source mappings run before confirmed-cooldown-only `global-targets`.
- Source and fallback targets share one request-level abnormal-reasoning retry
  counter, hedge state, and `UsageAccumulator`; fallback cannot reset or extend
  the configured retry/hedge budget. A target gets one auth-selection wave,
  while that wave still follows `max-retry-credentials`.
- With `client-usage-aggregation: sum`, all dispatched source and target
  attempts participate in the client-visible aggregate. The default
  delivered-only policy continues to expose only the delivered result.
- Each dispatched attempt keeps normal attempt-level usage/failure attribution
  to its actual auth and model.
- A fallback blocked locally by the reasoning gate is a zero-dispatch outcome:
  it does not cool down the target model and does not create a synthetic upstream
  failure usage record.
- `auth_not_found`, target model cooldown, and continuity-observation-pending
  are zero-dispatch target outcomes and continue ordered target selection. A
  typed target usage-limit/capacity also continues to the next target. A local
  continuity block returns the original source error; a real dispatched
  request-invalid, auth, transient, or other non-fallback target error stops
  selection and is returned unchanged. If every target is zero-dispatch, Core
  returns the original source typed 429.
- Selected-auth callbacks are withheld for skipped and locally blocked targets;
  only an auth that reaches the executor dispatch boundary and becomes the final
  outcome is published to the external WebSocket pinning path.
- A failed source model and a successful target model retain independent model
  availability state.

## Downstream surface

The config is available through Core's normal config serialization and hot
reload path. `CPA-Panel-LTS` needs a separate UI/schema follow-up before the
visual editor can be treated as supporting these fields; this Core change does
not modify the Panel repository.
