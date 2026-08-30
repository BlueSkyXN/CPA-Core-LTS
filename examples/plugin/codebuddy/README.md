# CodeBuddy G1 Provider Plugin

`cpa-provider-codebuddy` is a schema 5 dynamic plugin for the verified CodeBuddy direct HTTPS streaming lane. It registers `codebuddy` as an auth, model, and executor provider and exposes `hy3-preview-agent` through CPA's normal auth selection and provider execution path.

This G1 implementation is deliberately stream-only. It does not advertise native sessions, workspace tools, `codebuddy --serve`, ACP, or an execution-session closer.

## Auth file

Place a credential file under CPA's configured `auth-dir`:

```json
{
  "type": "codebuddy",
  "auth_mode": "api_key",
  "api_key": "[REDACTED_SECRET]"
}
```

The plugin sends the selected key in both the upstream `Authorization: Bearer` and `X-API-Key` headers. Credential values remain in provider-owned `StorageJSON` and are not copied into readiness diagnostics or errors.

## Build

Run from this directory:

```bash
cd go
go test -count=1 ./...
go test -race -count=1 ./...
go build -buildmode=c-shared -o /tmp/cpa-provider-codebuddy.dylib .
rm -f /tmp/cpa-provider-codebuddy.h
```

Use `.so` on Linux and `.dll` on Windows. The installed dynamic-library basename must be `cpa-provider-codebuddy` so it matches the plugin configuration key.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-provider-codebuddy:
      enabled: true
      priority: 100
```

The default upstream is `https://copilot.tencent.com/v2/chat/completions`. The optional `endpoint` setting exists for controlled testing and accepts plain HTTP only on loopback. `user_agent` is non-secret and defaults to the plugin name/version.

## Supported behavior

- Input/output format: OpenAI Chat Completions.
- Model: `hy3-preview-agent`.
- `stream=true` is required; non-streaming requests fail before contacting the provider.
- Upstream SSE must terminate with `data: [DONE]`.
- Downstream disconnect and explicit execution cancellation close the host-owned upstream stream.
- Readiness reports direct HTTPS as requiring no external Runner and reports native session readiness as unsupported.

OpenAI Responses clients are handled through CPA's existing Responses-to-Chat request and streaming response translation; the plugin itself remains a Chat Completions provider.
