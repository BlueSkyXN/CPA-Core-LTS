# Qoder provider plugin

This example implements the schema 5 `cpa-provider-qoder` dynamic plugin and
connects it to the separately installed `cpa-qoder-runner`. One `qoder`
Provider exposes two explicit transports:

- `sdk_cli` (default): Qoder Agent SDK + the administrator-selected external
  `qoderclicn`/`qodercli`, with native sessions, workspace tools, skills, MCP,
  permissions, image input, and Agent events.
- `direct_openai`: a configured OpenAI-compatible Qoder endpoint for raw
  Chat/stream/tool requests and non-stream projection.

The transport can be selected by plugin configuration or an auth-file override.
It is part of the execution-session identity, so a session never changes
transport midway. There is no silent fallback between transports.

New auth files should use the long-lived `pt-` PAT. Both SDK Agent and Direct
paths exchange it for a short-lived Job Token, and the read-only
account/plan/quota Summary uses the same regional OpenAPI. Existing auth files using
`access_token` or `local_cli` remain readable for compatibility; the latter
continues to use its explicitly isolated `config_dir` and is not available to
the Direct transport. OAuth, interactive login, and persisted short-lived
tokens are not part of this plugin.

The plugin implements `AuthProvider`, `ModelsForAuth`, `ProviderExecutor`,
`ProviderReadiness`, `ExecutionCanceller`, `ExecutionSessionCloser`, and the
read-only Plugin Management API through the dynamic plugin ABI. The executor
uses Chat Completions as its canonical host format; CPA's existing translator
handles Responses requests and responses around that boundary.

## Auth file

```json
{
  "type": "qoder",
  "auth_mode": "pat",
  "pat": "[REDACTED_SECRET]",
  "transport": "sdk_cli",
  "label": "Qoder 主账号"
}
```

The PAT is never put into JSONL frames or logs. The plugin gives the runner a
dedicated environment variable. The runner exchanges it in memory and Qoder
Agent SDK creates and removes a mode-0600 host-callback payload that contains no
token. Each runner also receives a private `TMPDIR` and PAT `HOME`.

## Configuration

```yaml
runner_command: /absolute/path/to/cpa-qoder-runner
runner_args: []
qoder_cli_path: /absolute/path/to/qoderclicn
working_directory: /private/tmp
max_queue_frames: 128
request_timeout: 30s
model_cache_ttl: 1m
openapi_endpoint: https://openapi.qoder.com.cn
openapi_user_agent: qoder/1.1.40
direct_auth_endpoint: https://openapi.qoder.com.cn # legacy alias
direct_token_mode: auto # auto, bearer, or pat_exchange
transport: sdk_cli
permission_default: deny
permission_rules: []
skills: []
setting_sources: []
allowed_tools: []
disallowed_tools: []
mcp_servers: {}
```

`openapi_endpoint` is the verified regional Qoder OpenAPI base used for PAT
exchange and account/plan/quota calls. CN and global Qoder endpoints are not
interchangeable; choose one explicitly. Plain HTTP is accepted only for
loopback test fixtures.

For `direct_openai`, configure an exact model source:

```yaml
transport: direct_openai
openapi_endpoint: https://openapi.qoder.com.cn
direct_endpoint: https://gateway.qoder.com.cn/model/v1/chat/completions
direct_models:
  - id: qmodel_38max
    display_name: Qwen3.8-Max
```

`direct_models_endpoint` may be used instead of `direct_models` when the
configured endpoint returns an OpenAI-compatible `{data:[...]}` catalog.
Direct mode always exchanges the PAT through
`POST /api/v1/jobToken/exchange`, refreshes in memory when needed, and retries
one 401/403 response. Existing `direct_token_mode: bearer` and opaque legacy
`access_token` values remain supported; new PAT files should use `auto` or
`pat_exchange`.

## Dynamic models and exact IDs

The SDK path exchanges the PAT through the configured `openapi_endpoint`, then
uses the typed `getAvailableModels({ fetchStrategy: "live" })` response and
preserves every returned ID and capability field.
Vision, reasoning, disable-thinking support, token limits, and context windows
are projected only when the selected account reports them.

The Direct path is independent: it uses the configured exact model list or the
configured Direct catalog endpoint. A SDK model is never automatically treated
as a Direct model. Display names such as `Qwen3.8-Max` are not executable IDs;
the real exact ID must come from that transport's catalog. No alias or silent
fallback to `auto` is performed.

### Legacy auth-file compatibility

The following existing shapes remain accepted so an upgrade does not strand
stored Qoder credentials:

```json
{
  "type": "qoder",
  "auth_mode": "pat",
  "access_token": "pt-LEGACY_PAT",
  "account_id": "legacy-account"
}
```

```json
{
  "type": "qoder",
  "auth_mode": "local_cli",
  "profile_id": "cn-main",
  "config_dir": "/absolute/path/to/qoder-config"
}
```

`pat` is preferred for new files. When both `pat` and `access_token` are
present, they must match. `local_cli` is SDK-only and its profile directory is
never copied into the PAT Summary or Direct transport.

## Read-only account, plan and quota Summary

The plugin registers:

```text
GET /v0/management/plugins/qoder/summary?auth_index=<AUTH_INDEX>
```

The route is protected by CPA Management authentication and uses the existing
`host.auth.get`/`host.auth.get_runtime` callbacks. The plugin instance must
grant `permissions.auth-read: true`.

Summary exchanges the selected PAT, then reads `/api/v1/userinfo`,
`/api/v2/user/plan`, and `/api/v2/quota/usage`. Account, plan, and quota are
returned independently so an unavailable component does not hide the others.
The quota response keeps exact decimal strings alongside convenient numeric
values and distinguishes real zero from `unsupported`, `not_configured`, and
`auth_rejected`. Package details retain exhausted or expired historical grants,
but top-level totals include only currently available packages so they stay in
the same current-quota scope as the upstream percentage. Short-lived tokens and
raw vendor payloads are never returned.

## Agent and Direct behavior

The SDK path supports fixed administrator-selected skills, setting sources,
tool allow/deny rules, bounded MCP configuration, structured text/image input,
native Qoder tools, continuation, cancellation, and session close. Tool and
MCP actions stay inside the Agent session; they are not exposed as client-owned
OpenAI tool calls.

The Direct path preserves the original Chat `messages`, images, `tools`,
`tool_choice`, and supported generation fields while forcing an upstream
streaming request. It projects both stream and non-stream responses through the
shared AgentEvent lifecycle and marks upstream usage as
`provider_reported_unverified`.

Both transports require an exact executable model ID and preserve explicit
cancel, close, downstream disconnect, and SSE `[DONE]` handling. The plugin does
not implement the reverse-engineered legacy COSY/QoderEncoding protocol.

## Build

```bash
cd go
go test ./...
go test -race ./...
go build -buildmode=c-shared -o cpa-provider-qoder.so .
```
