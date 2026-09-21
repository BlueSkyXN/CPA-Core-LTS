# Qoder provider plugin

This example implements the `cpa-provider-qoder` dynamic plugin. One `qoder`
Provider exposes two explicit transports. The PAT distribution selects native
`direct_openai` and does not install Node, Qoder CLI, Qoder SDK or a runner:

- `sdk_cli` (legacy default when transport is omitted): separately installed
  `cpa-qoder-runner`, Node, Qoder Agent SDK + the administrator-selected external
  `qoderclicn`/`qodercli`, with native sessions, workspace tools, skills, MCP,
  permissions, image input, and Agent events.
- `direct_openai` (recommended): native Go with Core-owned HTTP callbacks to a
  configured Qoder endpoint for Chat/stream/tool requests and non-stream projection.

The transport can be selected by plugin configuration or an auth-file override.
It is part of the execution-session identity, so a session never changes
transport midway. There is no silent fallback between transports.

New auth files should use the long-lived `pt-` PAT. Both SDK Agent and Direct
paths exchange it for a short-lived Job Token, and the read-only
account/plan/quota Summary uses the same regional OpenAPI. Existing auth files using
`access_token` or `local_cli` remain executable for compatibility. A legacy
`access_token` with the `pt-` prefix follows the PAT exchange path, while an
opaque value keeps the released SDK access-token selector and Direct bearer
semantics. `local_cli` continues to use its explicitly isolated `config_dir`
and is not available to the Direct transport. OAuth, interactive login, and
persisted short-lived tokens are not part of this plugin.

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
  "transport": "direct_openai",
  "label": "Qoder 主账号"
}
```

Native Direct shares an in-memory token manager between inference, catalog and
Summary. Concurrent exchanges/refreshes are coalesced; token expiry uses Qoder
OpenAPI's millisecond `expires_in` (absolute `expires_at` takes precedence).
The PAT is never put into JSONL frames or logs. For SDK mode, the plugin gives the runner a
dedicated environment variable. The runner exchanges it in memory and Qoder
Agent SDK creates and removes a mode-0600 host-callback payload that contains no
token. Each runner also receives a private `TMPDIR` and PAT `HOME`.

## Configuration

The following is the optional `sdk_cli` compatibility configuration. Native
Direct configuration is shown immediately below it.

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

For native `direct_openai`, no runner or workspace settings are required. An
existing exact manual model source remains supported:

```yaml
transport: direct_openai
openapi_endpoint: https://openapi.qoder.com.cn
direct_endpoint: https://gateway.qoder.com.cn/model/v1/chat/completions
direct_models:
  - id: qmodel_38max
    display_name: Qwen3.8-Max
```

For automatic QoderCN catalog discovery, remove `direct_models` and set:

```yaml
direct_models_endpoint: https://gateway.qoder.com.cn/algo/api/v2/model/list
direct_catalog_format: qoder
```

This fetches user identity and the COSY-signed enabled `chat` scene, retains raw
model metadata, and exposes upstream display names with a built-in name fallback.
Hidden/disabled entries are excluded. Discovery does not run billable inference
or prove every listed model works with the configured Direct endpoint. The
operator must select a matching regional catalog and inference endpoint; no
protocol fallback, model guessing or automatic context-tier escalation occurs.

The default `direct_catalog_format: openai` preserves existing Bearer catalog
endpoints returning `{data:[...]}`, `{models:[...]}` or a model array. Explicit
`direct_models` still takes precedence over any endpoint. Empty/failed live
catalogs do not substitute a guessed static list. Catalogs are cached per
credential/endpoint/config generation for `model_cache_ttl`, with concurrent
refresh coalescing. Friendly display names are not executable aliases.

With `auto` or `pat_exchange`, Direct exchanges the PAT through
`POST /api/v1/jobToken/exchange`, refreshes in memory when needed, and retries
one pre-output 401 or explicit token-expiry 403 response. Queue/quota/model-denial
errors do not trigger credential refresh, and no mid-stream generation is replayed.
Existing `direct_token_mode: bearer` and opaque legacy
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
streaming request through Core's `host.http.*` bridge. It projects both stream
and non-stream responses using the existing in-process event projection (no runner)
and marks upstream usage as
`provider_reported_unverified`.

Usage events retain reported cache-read, cache-creation, and reasoning counts
through optional `cache_read_tokens`, `cache_creation_tokens`, and
`reasoning_tokens` fields. SDK cache buckets are added to its uncached input
count before projecting inclusive Chat `prompt_tokens`; Direct Chat cache and
reasoning counts remain subsets of the reported input/output totals. Missing
details stay absent rather than being inferred from text or filled with zero.

Both transports require an exact executable model ID and preserve explicit
cancel, close, downstream disconnect, and SSE `[DONE]` handling. The plugin
uses COSY/QoderEncoding only for explicitly selected catalog discovery. It does
not use the legacy `agent_chat_generation` inference endpoint or a bundled base prompt.

The default PAT image and native plugin archives need no runner. Existing
`sdk_cli` deployments must retain/install their separately managed runner and
CLI before switching images. Old explicit runner fields are accepted but unused
by Direct. See [the rollout and validation guide](../../../docs/lts/pat-providers.md).

## Build

```bash
cd go
go test ./...
go test -race ./...
go build -buildmode=c-shared -o cpa-provider-qoder.so .
```
