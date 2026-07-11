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
cross-model fallback when either of these is present:

- the translated request already contains a reasoning item; or
- the source model/session has cached Codex reasoning replay state.

This still permits normal same-model auth failover, whose replay cache is scoped
by model and session rather than auth ID.

`reasoning-continuity: context-reset` is an explicit lossy mode. Before the
fallback request is sent, Core removes all reasoning items. If source replay
state contains function/custom-tool calls needed by tool outputs in the current
request, those replayable call items may be retained so the tool pair remains
valid. No reasoning signature is copied to the target model, and normal target
model replay injection is skipped for that fallback attempt.

This mode means "continue without model-private reasoning history". It must not
be described as signature repair, signature translation, or equivalent
reasoning continuity.

## Retry, usage, and auth state

- Same-model auth selection and ordinary retry run before model fallback.
- Model fallback is not a `RetryWithoutPenalty` lane and does not consume or
  extend the abnormal-reasoning hedge budget.
- Each dispatched attempt keeps normal attempt-level usage/failure attribution
  to its actual auth and model.
- A fallback blocked locally by the reasoning gate is a zero-dispatch outcome:
  it does not cool down the target model and does not create a synthetic upstream
  failure usage record.
- A failed source model and a successful target model retain independent model
  availability state.

## Downstream surface

The config is available through Core's normal config serialization and hot
reload path. `CPA-Panel-LTS` needs a separate UI/schema follow-up before the
visual editor can be treated as supporting these fields; this Core change does
not modify the Panel repository.
