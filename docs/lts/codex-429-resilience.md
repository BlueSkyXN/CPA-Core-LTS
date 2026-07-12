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
```

The source mapping is exact and case-insensitive after trimming. Target order is
significant. Duplicate and source-equivalent targets are removed in memory.
Existing config files are not rewritten to add defaults.

The fallback path is limited to standard Codex response execution. It does not
apply to compact, image, or video requests. Streaming fallback is only possible
while the upstream failure is still in the bootstrap/pre-delivery phase; once a
stream has been returned and client-visible payload can be emitted, Core does
not replace it with a different model.

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
