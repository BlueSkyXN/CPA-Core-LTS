# Usage schema v3: semantic timing contract

Status: proposed implementation baseline

## Scope

The Management usage export/import contract advances from canonical v2 to
canonical v3. The change adds request timing for first byte, first reasoning
content, and first user-visible assistant text while preserving the existing
token accounting and duplicate identity rules.

## Timing fields

Request details may contain the following millisecond fields:

- `timing_version: 1` identifies the semantic timing contract.
- `ttfb_ms` is the elapsed time from the upstream attempt start to the first
  non-empty response byte or WebSocket payload.
- `ttft_ms` is the elapsed time to the first non-empty reasoning content.
- `ttfa_ms` is the elapsed time to the first non-empty assistant text.

All timing fields are optional. A missing field means that the event was not
observed; it must not be converted to zero. When semantic fields are present,
`ttfb_ms` must also be present and the values must be no greater than
`latency_ms`. There is no ordering requirement between `ttft_ms` and
`ttfa_ms`. Non-streaming and unknown-format requests do not receive fabricated
semantic timing.

New Core records always carry `timing_version: 1`, even when an event is not
available. Migrated v1/v2 details may remain legacy timing details without a
timing version and may retain only a verifiable `ttfb_ms`.

## Migration matrix

| Input version | Accepted timing | Migration receipt |
|---|---|---|
| v1 | Optional verified `latency_ms`/`ttfb_ms`; semantic timing fields are rejected | `migrated_from_version: 1`, `migrations: ["v1_uncached_input_tokens_to_v2", "v2_timing_contract_to_v3"]` |
| v2 | Optional legacy `ttfb_ms`; semantic timing fields are rejected | `migrated_from_version: 2`, `migrations: ["v2_timing_contract_to_v3"]` |
| v3 | Strict timing and token validation | No migration fields |

The singular v2 `migration` response property remains accepted only by the
Panel decoder for old Core responses. New Core v3 responses use `migrations`.
All validation runs before merge, and rejected imports leave the in-memory
snapshot unchanged.

Stable timing-related errors are:

- `usage_v1_timing_semantics_ambiguous`
- `usage_v2_timing_semantics_ambiguous`
- `usage_v3_timing_contract_invalid`
- `usage_v3_token_contract_invalid`

## SDK/plugin and queue compatibility

`usage.Record` and `pluginapi.UsageRecord` now carry `TimingVersion`, `TTFB`,
`TTFT`, and `TTFA`. `TTFT` is intentionally redefined from the historical
first-byte value to first reasoning content. Unversioned runtime timing values
are not guessed or persisted.

The Redis-compatible usage queue emits `timing_version`, `ttfb_ms`, `ttft_ms`,
and `ttfa_ms`. Consumers must stop interpreting the old queue `ttft_ms` as
TTFB and use `ttfb_ms` instead. This is an intentional compatibility change;
there is no dual write of the old incorrect meaning.

## Rollout and rollback

Release a Panel that accepts v1/v2/v3 and both receipt shapes before releasing
the Core that emits v3. Update queue/plugin consumers before the Core cutover.
Before upgrading Core, save a v2 export. A pre-v3 Core cannot import a v3-only
export, and v3-only semantic timing cannot be retained by a pre-v3 rollback.
