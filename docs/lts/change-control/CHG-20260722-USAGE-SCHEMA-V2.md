# CHG-20260722-USAGE-SCHEMA-V2

Status: implementation candidate; merge and release approval are not implied.

## Change summary

The Management usage export contract advances from version 1 to canonical
version 2. Import remains backward compatible with released version 1 payloads
only where the old token semantics can be reconstructed without guessing.
Every rejected import is validated before mutation, returns the existing
human-readable `"error"` plus a stable top-level `"code"`, and leaves the
in-memory snapshot unchanged.

## Released evidence

- `v1-lts-0.0.13` (`6f9533491aec6cced1661a7bbcf7187582e588b8`)
  exported `version: 1` before `uncached_input_tokens` existed. Its required
  token fields were `input_tokens`, `output_tokens`, `reasoning_tokens`,
  `cached_tokens`, and `total_tokens`; zero cache read/creation fields were
  omitted.
- `v1-lts-0.0.15` (`e37e61aadb2e57883df54eef9d24af932fd1aa63`)
  added optional `uncached_input_tokens`. The marker is present only when the
  runtime knew the provider-specific uncached input contribution.

Both releases used the same version number, so migration is based on explicit
field evidence rather than release-name, model-name, source-path, or provider
guesses.

The released exporter wrote `reasoning_tokens` and `cached_tokens` even when
they were zero. The version 1 importer also accepts those two fields when they
are omitted and treats them as zero, because this is a lossless compatibility
extension for older saved or re-serialized no-cache backups. `input_tokens`,
`output_tokens`, and `total_tokens` remain required, and an explicitly present
legacy token field must still be a non-null, non-negative integer.

## Version 1 migration matrix

| Version 1 detail | Decision | Canonical result or error |
|---|---|---|
| `uncached_input_tokens` is present, integral, non-negative, and no greater than the released `input_tokens` | Migrate | `input_tokens = uncached + cache_read + cache_creation`; `cached_tokens` mirrors canonical `cache_read_tokens`. |
| Marker is absent and `cached_tokens`, `cache_read_tokens`, and `cache_creation_tokens` are all zero | Migrate | Preserve `input_tokens`; all cache categories remain zero. This covers released `v1-lts-0.0.13` no-cache exports and markerless no-cache `v1-lts-0.0.15` exports. |
| Marker is absent and any cache category is non-zero | Reject | `code: usage_v1_cache_semantics_ambiguous`. OpenAI-inclusive and Claude-uncached input semantics cannot be distinguished from the snapshot alone. |
| `input_tokens`, `output_tokens`, or `total_tokens` is missing; an optional legacy zero field is explicitly null/mistyped; the marker is invalid; or another version 1 token rule is invalid | Reject | `code: usage_v1_token_contract_invalid`. |

The legacy cache-creation alias (`cached_tokens == cache_creation_tokens`,
`cache_read_tokens == 0`) is treated as creation-only only when the explicit
uncached marker makes the conversion auditable.

The reconstructed canonical input is intentionally not required to equal the
legacy `input_tokens`. Released providers used different version 1 input
semantics: for example, a Claude detail can carry legacy input `3085`, explicit
uncached input `3085`, cache read `7`, and cache creation `19514`, which
canonically reconstructs to input `22606`. Requiring equality would reject a
released, auditable backup rather than detect corruption.

## Canonical version 2 contract

Required fields must be present even when their value is explicitly zero:

- `input_tokens`
- `output_tokens`
- `reasoning_tokens`
- `cached_tokens`
- `total_tokens`

`cache_read_tokens` and `cache_creation_tokens` remain optional because zero is
omitted by the exporter. Canonical details must also satisfy:

- every token count is non-negative;
- `cached_tokens == cache_read_tokens`;
- `cache_read_tokens + cache_creation_tokens <= input_tokens`, without integer overflow;
- `input_tokens + output_tokens <= total_tokens`, without integer overflow.

Reasoning is not added again to the minimum total because OpenAI/Codex output
already includes its reasoning subset and the snapshot does not persist enough
provider identity to impose a different formula universally.

Runtime producers must not emit a detail that the version 2 importer would
reject. OpenAI/Codex producer paths therefore use `input + output` when an
upstream total is absent, while a producer that still has an exact non-OpenAI
provider identity may retain its established separate-reasoning fallback.
Explicit totals below `input + output` are raised to that checked minimum.
If an OpenAI/Codex payload reports only a positive reasoning count, the producer
uses that count as the fallback total instead of erasing the only usage signal.
Unrepresentable token vectors are stored as an all-zero canonical vector, and
a record that would overflow an aggregate is retried with that zero vector so
request metadata can be retained without creating a non-roundtrippable export.

## Import response contract

Every successful import returns:

```json
{
  "added": 1,
  "skipped": 0,
  "total_requests": 1,
  "failed_requests": 0,
  "schema_version": 2
}
```

A successful version 1 migration additionally returns the migration receipt:

```json
{
  "migrated_from_version": 1,
  "migration": "v1_uncached_input_tokens_to_v2"
}
```

Errors preserve the existing `"error"` text and add one stable top-level
`"code"`:

| `code` | Meaning |
|---|---|
| `usage_version_unsupported` | The version is neither released v1 nor canonical v2. |
| `usage_shape_invalid` | The JSON/root/nested typed shape is invalid, the usage store is unavailable, or the request body cannot be read. |
| `usage_v1_token_contract_invalid` | A version 1 required input/output/total field or migration marker is missing or invalid, an optional legacy token field is explicitly null/mistyped, or another version 1 invariant fails. |
| `usage_v1_cache_semantics_ambiguous` | A markerless version 1 detail contains non-zero cache data whose input semantics cannot be reconstructed. |
| `usage_v2_token_contract_invalid` | A version 2 mandatory field is omitted, null, mistyped, or violates canonical invariants, or the retired `uncached_input_tokens` marker is present. |
| `usage_aggregate_overflow` | The complete candidate merge would overflow a request or token aggregate. |

Nested import containers are validated before typed decoding. `usage`, `apis`,
each API/model object, `models`, `details`, every detail object, and any present
day/hour aggregate maps must retain their object/array shape; `null` containers
are not interpreted as empty data. Missing or `null` `tokens` remain a
version-specific token-contract error rather than a generic shape error. Schema
field names are case-sensitive; case-colliding aliases such as `Version`,
`Usage`, `Details`, or `Tokens` are rejected before Go's case-insensitive struct
decoder can reinterpret the validated payload. Duplicate object keys are also
rejected so an earlier value cannot survive struct/map merge semantics after a
different duplicate value was inspected by the raw-shape pass.

## Atomicity

Parsing, v1 migration, field-presence checks, and canonical validation operate
on the detached request payload. `MergeSnapshot` builds a complete deduplicated
candidate list and preflights global, API, model, day, and hour request/token
aggregates before recording any detail. Any validation or overflow error returns
without a partial merge.

Missing, `null`, and Go-zero timestamps remain accepted for released-backup
compatibility. They are preserved as the Go zero sentinel rather than replaced
with `time.Now()`, so their identity remains explicitly uncertain but repeated
imports are deterministic. The sentinel may contribute to the existing
`0001-01-01` / `00` buckets; changing that bucket policy is outside this
schema-v2 change.

## Cross-repository requirement

`CPA-Panel-LTS` import preflight must implement the same version, required-field,
markerless-v1, canonical-invariant, stable `code`, and receipt semantics. A green
mock smoke in either repository does not by itself prove a released Core backup
roundtrip or a deployed runtime.

## Required validation

```text
go test -count=1 ./internal/usage
go test -count=1 ./internal/api/handlers/management -run 'Usage|usage'
go test -count=1 ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'
scripts/check-lts-contract.sh
git diff --check
go build -o test-output ./cmd/server
```

The implementation handoff must report which checks were actually run. The
change-control record does not authorize merge, release, deployment, or legacy
cache-semantic overrides.
