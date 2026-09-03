# CodeBuddy Provider Plugin

`cpa-provider-codebuddy` is a schema 5 dynamic plugin for the CodeBuddy and
WorkBuddy-compatible direct HTTPS Chat Completions lane. `workbuddy` is not a
separate CPA Provider: the selected CodeBuddy CLI PAT/API key is the only
credential passed to the vendor service.

The plugin keeps the existing direct, stream-only execution boundary. It does
not advertise native sessions, workspace tools, `codebuddy --serve`, ACP, or an
execution-session closer.

## Auth file

The recommended credential format is:

```json
{
  "type": "codebuddy",
  "auth_mode": "pat",
  "pat": "[REDACTED_SECRET]",
  "label": "CodeBuddy 主账号"
}
```

The existing API-key format remains accepted:

```json
{
  "type": "codebuddy",
  "auth_mode": "api_key",
  "api_key": "[REDACTED_SECRET]",
  "label": "CodeBuddy Legacy"
}
```

The selected credential is sent to Chat as both `Authorization: Bearer` and
`X-API-Key`. Catalog and billing calls use the vendor-required `X-API-Key`,
`X-Product: SaaS`, and catalog User-Agent headers. Credential values remain in
provider-owned `StorageJSON` and are never returned by readiness or Summary.

## Build

Run from this directory:

```bash
cd go
go test -count=1 ./...
go test -race -count=1 ./...
go build -buildmode=c-shared -o /tmp/cpa-provider-codebuddy.dylib .
rm -f /tmp/cpa-provider-codebuddy.h
```

Use `.so` on Linux and `.dll` on Windows. The installed dynamic-library
basename must be `cpa-provider-codebuddy` so it matches the plugin key.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-provider-codebuddy:
      enabled: true
      priority: 100
      permissions:
        auth-read: true # required by the read-only Summary route
      endpoint: https://copilot.tencent.com/v2/chat/completions
      catalog_endpoint: https://copilot.tencent.com/v3/config
      catalog_user_agent: WorkBuddy/5.4.5
      billing_endpoint: https://copilot.tencent.com/v2/billing/meter/get-user-resource
      # account_endpoint is optional; failed account probing falls back to the
      # auth label and a non-secret credential fingerprint.
```

`endpoint`, `catalog_endpoint`, and `billing_endpoint` are validated as
absolute HTTPS URLs. Plain HTTP is accepted only for loopback test fixtures.
`account_endpoint` is optional because the client account route is not
available for every CLI key or product family.

## Dynamic catalog

`ModelsForAuth` fetches the catalog for each credential. Current WorkBuddy
responses use the `craft` Agent's model IDs for interactive Chat, while the
earlier catalog shape used an Agent named `cli`. The parser prefers `cli` when
present and otherwise requires `craft`; it does not expose completion, rewrite,
image, or other task-specific entries merely because they appear in
`data.models`.

Model IDs are preserved exactly as returned. Display names are not executable
IDs, and no alias or fallback is guessed. Model metadata such as image,
reasoning, context, and output limits is advertised only when explicitly
reported by the selected catalog. The catalog is cached per credential and
endpoint for one minute.

The selected exact ID is checked again by the executor against that
credential's last successful catalog before the Chat request is opened. A
catalog outage never substitutes the static `hy3` or
`hy3-preview-agent` list.

## Read-only account and quota Summary

The plugin registers:

```text
GET /v0/management/plugins/codebuddy/summary?auth_index=<AUTH_INDEX>
```

The route is protected by CPA Management authentication. It uses the existing
`host.auth.get`/`host.auth.get_runtime` callbacks, so the plugin instance must
grant `permissions.auth-read: true`.

The response contains the selected `auth_index`, file name, label, a short
credential fingerprint, account status, and quota status. Quota packages retain
exact decimal strings and also expose convenient numeric values; real zero is
distinct from `unsupported`, `not_configured`, or `auth_rejected`. The response
never contains a PAT, Job Token, Refresh Token, raw upstream body, or auth-file
path.

CodeBuddy billing uses the read-only `POST
/v2/billing/meter/get-user-resource` operation. The implementation accepts both
the current `data.Response.Data.Accounts` envelope and the historical direct
`Accounts` shape. Account lookup is best-effort; if the configured account route
rejects the PAT, Summary still identifies the selected auth file and reports
the safe fingerprint fallback.

## Supported behavior

- Input/output boundary: OpenAI Chat Completions.
- Dynamic per-auth model catalog with exact ID forwarding.
- Text, image, tools, and reasoning capabilities follow the selected vendor catalog and live capability checks.
- The complete client-supplied `messages` array is preserved, including system and prior-turn context.
- `stream=true` is required; non-streaming requests fail before contacting the provider.
- Upstream SSE must terminate with `data: [DONE]`.
- Downstream disconnect and explicit execution cancellation close the host-owned upstream stream.
- OpenAI Responses clients use CPA's existing Responses-to-Chat translation; the plugin itself remains a Chat provider.

CLI-native workspace sessions, skill discovery, MCP configuration, and native
Agent tool execution remain outside this direct plugin boundary.
