# Qoder provider plugin

This example implements the `cpa-provider-qoder` dynamic plugin. The provider
supports PAT authentication only and runs native Go HTTP: the China-zone
default uses COSY-signed inference, while an explicit OpenAI-compatible
`direct_endpoint` retains the existing OpenAI request format. No Node,
Qoder CLI, Qoder SDK, or runner is installed or started. The former `sdk_cli`/`local_cli` compatibility path was
removed in plugin 0.3.0; configuration or auth files that explicitly request
`sdk_cli` are rejected with a clear error instead of silently changing
meaning.

Auth files use the long-lived `pt-` PAT. The plugin exchanges it for a
short-lived Job Token for inference, catalog, and the read-only
account/plan/quota Summary, all through the same regional OpenAPI. Existing
auth files using a legacy `access_token` field remain readable (a `pt-`
prefixed value follows the PAT exchange path; an opaque value keeps bearer
semantics). OAuth, interactive login, and persisted short-lived tokens are
not part of this plugin.

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
  "label": "Qoder 主账号"
}
```

A `transport` field is still accepted as a no-op compatibility input when set
to `direct_openai`; any other value (including `sdk_cli`) is rejected.
`local_cli` profiles with `profile_id`/`config_dir` are rejected and must be
migrated to a PAT before upgrading.

Native Direct shares an in-memory token manager between inference, catalog
and Summary. Concurrent exchanges/refreshes are coalesced; token expiry uses
Qoder OpenAPI's millisecond `expires_in` (absolute `expires_at` takes
precedence). The PAT is never put into JSONL frames or logs.

## Configuration

The minimal enablement is:

```yaml
enabled: true
permissions:
  auth-read: true
```

From plugin 0.3.1, China-zone native defaults are built in: `direct_endpoint`
(`https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation`),
`direct_models_endpoint`
(`https://gateway.qoder.com.cn/algo/api/v2/model/list`),
`openapi_endpoint` (`https://openapi.qoder.com.cn`),
`direct_catalog_format: qoder`, `direct_token_mode: auto`, and
`openapi_user_agent: qoder/1.1.40`. The default inference endpoint adds
`FetchKeys=llm_model_result`, `AgentId=agent_common`, and `Encode=1` per
request. Explicit overrides always win. Other regional or private gateways
must use their corresponding verified endpoints:

```yaml
openapi_endpoint: https://openapi.example.test
direct_endpoint: https://api.example.test/model/v1/chat/completions
direct_models_endpoint: https://api.example.test/algo/api/v2/model/list
request_timeout: 30s
model_cache_ttl: 1m
```

An existing explicit `direct_endpoint` ending in `/model/v1/chat/completions`
continues to use the OpenAI-compatible wire format. To use the verified CN
COSY path, remove that override or replace it with the COSY endpoint above.

`openapi_endpoint` is the verified regional Qoder OpenAPI base used for PAT
exchange and account/plan/quota calls. CN and global Qoder endpoints are not
interchangeable; choose one explicitly. Plain HTTP is accepted only for
loopback test fixtures. `direct_auth_endpoint` remains as a legacy alias and
must match `openapi_endpoint` when both are configured.

An exact manual model source remains supported and takes precedence over any
endpoint:

```yaml
direct_models:
  - id: qmodel_38max
    display_name: Qwen3.8-Max
```

With `direct_models` absent, the plugin discovers the per-account catalog
through `direct_models_endpoint`. `direct_catalog_format: qoder` (the
default) fetches user identity and the COSY-signed enabled `chat` scene;
`direct_catalog_format: openai` preserves Bearer catalog endpoints returning
`{data:[...]}`, `{models:[...]}`, or a model array.

Discovery retains raw model metadata and exposes upstream display names with
a built-in name fallback. Hidden/disabled entries are excluded. Discovery
does not run billable inference or prove every listed model works with the
configured endpoint. No protocol fallback, model guessing, or automatic
context-tier escalation occurs; empty/failed live catalogs never substitute a
guessed static list. Catalogs are cached per
credential/endpoint/config generation for `model_cache_ttl`, with concurrent
refresh coalescing. Friendly display names are not executable aliases.

With `auto` or `pat_exchange`, Direct exchanges the PAT through
`POST /api/v1/jobToken/exchange`, refreshes in memory when needed, and
retries one pre-output 401 or explicit token-expiry 403 response.
Queue/quota/model-denial errors do not trigger credential refresh, and no
mid-stream generation is replayed. `direct_token_mode: bearer` and opaque
legacy `access_token` values remain supported; new PAT files should use
`auto`.

### Legacy auth-file compatibility

The following existing shape remains accepted so an upgrade does not strand
stored Qoder credentials:

```json
{
  "type": "qoder",
  "auth_mode": "pat",
  "access_token": "pt-LEGACY_PAT",
  "account_id": "legacy-account"
}
```

`pat` is preferred for new files. When both `pat` and `access_token` are
present, they must match. Extra legacy fields such as `account_id` are
ignored.

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
`auth_rejected`. Package details retain exhausted or expired historical
grants, but top-level totals include only currently available packages so
they stay in the same current-quota scope as the upstream percentage.
Short-lived tokens and raw vendor payloads are never returned.

## Direct behavior

The China-zone COSY path signs the exact encoded request body and unwraps
the vendor's nested SSE envelope through Core's `host.http.*` bridge. The
verified scope is text Chat with stream and non-stream responses. It maps
text message history, model ID, and basic generation parameters without
shipping a vendor base prompt. Image content and client tools fail with
`unsupported_input`; the COSY catalog advertises text input only. An
explicit non-COSY OpenAI-compatible endpoint retains the original direct
payload behavior, including images and tools. Both paths use the existing
event projection and mark upstream usage as `provider_reported_unverified`.

Usage events retain reported cache-read, cache-creation, and reasoning counts
through optional `cache_read_tokens`, `cache_creation_tokens`, and
`reasoning_tokens` fields. Direct Chat cache and reasoning counts remain
subsets of the reported input/output totals. Missing details stay absent
rather than being inferred from text or filled with zero.

Execution requires an exact executable model ID and preserves explicit
cancel, close, downstream disconnect, and SSE `[DONE]` handling. The plugin
uses COSY/QoderEncoding for the China-zone catalog and its default inference
endpoint. The optional OpenAI-compatible endpoint keeps its own Bearer wire
format; there is no automatic retry from one protocol to the other.

The PAT image and native plugin archives need no runner. Existing
`sdk_cli`/`local_cli` deployments must migrate their accounts to PATs before
switching to plugin 0.3.0+. See
[the rollout and validation guide](../../../docs/lts/pat-providers.md).

## Build

```bash
cd go
go test ./...
go test -race ./...
go build -buildmode=c-shared -o cpa-provider-qoder.so .
```
