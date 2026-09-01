# Qoder provider plugin

This example implements the schema 5 `cpa-provider-qoder` dynamic plugin and
connects it to the separately installed `cpa-qoder-runner`. The plugin supports
two explicit Qoder transports in one provider:

- `sdk_cli` (default): the verified Qoder Agent SDK/CLI path;
- `direct_openai`: a configured OpenAI-compatible Qoder endpoint for raw
  Chat/stream/tool requests.

The transport can be the plugin default or an auth-file override. It is part of
the execution-session identity, so one session never changes transport midway.
There is no silent fallback between transports.

The plugin implements `AuthProvider`, `ModelsForAuth`, `ProviderExecutor`,
`ProviderReadiness`, `ExecutionCanceller`, and `ExecutionSessionCloser` through
the dynamic plugin ABI. The executor declares Chat Completions as its canonical
host format; CPA's existing translator converts Responses requests and
responses around that boundary.

Minimal configuration:

```yaml
runner_command: /absolute/path/to/cpa-qoder-runner
runner_args: []
qoder_cli_path: /absolute/path/to/qoderclicn
working_directory: /private/tmp
max_queue_frames: 128
request_timeout: 30s
model_cache_ttl: 1m
transport: sdk_cli
permission_default: deny
permission_rules: []
skills: []
setting_sources: []
allowed_tools: []
disallowed_tools: []
mcp_servers: {}
```

For `direct_openai`, configure the endpoint and exact model source. A PAT is
exchanged for a short-lived Qoder job token when `direct_token_mode` is `auto`
or `pat_exchange`; an existing `jt-`/`dt-` token is used directly:

```yaml
transport: direct_openai
direct_endpoint: https://api2-v2.qoder.sh/model/v1/chat/completions
direct_auth_endpoint: https://openapi.qoder.sh
direct_token_mode: auto
direct_models:
  - id: qfmodel
    display_name: Qwen3.8-Flash
```

`direct_models_endpoint` may be used instead of `direct_models` when the
configured endpoint returns an OpenAI-compatible `{data:[...]}` catalog. CN and
global endpoints must be selected explicitly; the plugin does not guess a
region or convert a display name such as `Qwen3.8-Flash` into an executable ID.

An individual auth file may opt into the second transport while the plugin
default remains `sdk_cli`:

```json
{
  "type": "qoder",
  "auth_mode": "pat",
  "transport": "direct_openai",
  "access_token": "[REDACTED_SECRET]"
}
```

The optional capability settings are fixed administrator configuration, not
request-controlled values. `skills` selects named Qoder skills;
`setting_sources` accepts `user`, `project`, and `local`; tool allow/deny lists
use Qoder SDK names; and `mcp_servers` accepts bounded `stdio`, `sse`, or `http`
entries. Stdio commands must be absolute. Remote MCP uses HTTPS, except that
plain HTTP is accepted on loopback for controlled local testing. An execution
session rejects changes to system, skill, tool, or MCP configuration instead
of silently applying only part of a new configuration.

The `sdk_cli` path does not run `npm install`, use the SDK-bundled
`qodercli 1.0.30`, start an interactive login, or refresh a PAT. PATs enter the
dedicated runner through an environment variable; JSONL frames identify that
variable without carrying its value. Qoder SDK 1.0.10 then creates a short-lived
mode-0600 auth payload for the CLI, so each runner receives a plugin-owned
private `TMPDIR` and PAT `HOME`; the plugin terminates the Unix process group
and removes that runtime directory after exit. Local CLI auth uses
`qodercliAuth()` with `config_dir` selected through the isolated runner
environment; `profile_id` is only a CPA label, and distinct accounts require
distinct config directories. The `direct_openai` path may exchange a PAT for a
short-lived bearer token in runner memory and never persists the derived token.

Known executable model IDs are recorded exactly as returned by the installed
Qoder CLI. On the current CN catalog, `qfmodel` has the display name
`Qwen3.8-Flash`; the display name and guessed variants such as
`qwen3.8-flash` are not executable IDs. Per-auth discovery uses the SDK typed
`getAvailableModels()` response and preserves each returned ID verbatim.
Vision, reasoning levels, disable-thinking support, token limits, and context
windows are also projected from the live typed response when the selected
account reports them. Chat system text, conversation history, text blocks, and
base64 or HTTP(S) image blocks are converted to structured SDK input. Qoder
executes its configured native tools and MCP tools inside the Agent session;
those internal tool events are not exposed as client-owned OpenAI tool calls.

## `direct_openai` behavior

- Preserves the original Chat `messages`, images, `tools`, `tool_choice`, and
  supported generation fields while forcing an upstream streaming request.
- Provides both stream and non-stream projections through the shared AgentEvent
  lifecycle; upstream usage is marked `provider_reported_unverified`.
- Supports one bounded 401/403 refresh retry for PAT-exchanged credentials,
  explicit cancel, close, downstream disconnect, and `[DONE]` validation.
- Advertises `client_tools` but does not advertise native Qoder sessions,
  skills, MCP, or Qoder CLI tools for this transport.
- Requires an exact configured or live-discovered model ID. It never silently
  falls back to `auto`.

The direct adapter intentionally does not implement the reverse-engineered
legacy COSY/QoderEncoding protocol. That protocol remains a separate
characterization reference and is not part of the supported transport set.

Build:

```bash
cd go
go test ./...
go build -buildmode=c-shared -o cpa-provider-qoder.so .
```
