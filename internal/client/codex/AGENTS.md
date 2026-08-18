# internal/client/codex navigation card

`internal/client/codex/` adapts CPA to official Codex clients: model catalog responses, guarded Multi-Agent v2 rewrites, and Realtime/Live HTTP, WebSocket, WebRTC, and sideband flows.
Read this card before changing official-client detection, model metadata, collaboration tools, ephemeral secrets, Live sessions, media relay, or Codex client routes.
Key files: `models/`, `optimize-multi-agent-v2/`, `live/`, plus route wiring in `internal/api/server_routes.go`.

## Local invariants

- Client-specific rewrites run only for positively identified official Codex clients and enabled config; namespace conflicts, opaque/mixed agent content, and unproven tool shapes fail closed or remain untouched.
- Model responses derive capabilities from registry/catalog evidence. Preserve reasoning levels, visibility, context limits, input modalities, and search-tool eligibility; do not advertise unsupported upstream behavior.
- Realtime client secrets are short-lived, bounded, locally scoped, and bound to the issuing API principal/provider. Session/hangup/sideband calls must reject cross-principal or model scope mismatches.
- Live/Realtime auth selection, Home dispatch, usage attribution, response headers, session cleanup, and at-most-once unauthorized refresh remain coordinated.
- Media/sideband proxying must retain target validation, credential redaction, cancellation, bounded body/frame handling, and no unsafe direct-network fallback after a configured proxy failure.

## Do not

- 不要 make a Codex-only rewrite affect generic OpenAI-compatible clients.
- 不要 log ephemeral secrets, SDP credentials, attestation values, proxy credentials, or raw private session metadata.
- 不要 claim unsupported translation, transcription-only, or SIP capabilities; preserve the explicit error envelope.

## Validation

- `go test ./internal/client/codex/...`
- Route/handler changes: `go test ./internal/api ./sdk/api/handlers/openai`
- Multi-Agent/translator changes: `go test ./internal/client/codex/optimize-multi-agent-v2 ./internal/translator/... ./sdk/api/handlers/openai -run 'MultiAgent|Multi_Agent|multi_agent'`
