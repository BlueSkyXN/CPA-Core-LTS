# cpa-qoder-runner

`cpa-qoder-runner` is the versioned Node.js boundary between the native
`cpa-provider-qoder` plugin and Qoder. It supports two explicit transports:

- `sdk_cli` (default): Qoder Agent SDK + an administrator-selected external
  `qoderclicn`/`qodercli`, with native sessions, workspace tools, skills, MCP,
  permissions, image input, and agent events.
- `direct_openai`: an OpenAI-compatible Qoder endpoint, with client-owned
  messages/tools and standard SSE. It is a model/chat transport, not a native
  Qoder Agent session; no skills, MCP, or native tool execution are advertised.

The transport is selected by administrator configuration or an auth profile,
never by a client request. A session keeps one transport for its lifetime.

The runner deliberately requires an external Qoder CLI path:

```bash
npm ci --ignore-scripts
npm run build
node dist/index.js --stdio --cli-path /absolute/path/to/qodercli
```

Direct OpenAI mode requires an explicit endpoint and either an exact model list
or an OpenAI-compatible model catalog endpoint:

```bash
node dist/index.js --stdio \
  --transport direct_openai \
  --direct-endpoint https://api2-v2.qoder.sh/model/v1/chat/completions \
  --direct-auth-endpoint https://openapi.qoder.sh \
  --direct-models-json '[{"id":"qfmodel","display_name":"Qwen3.8-Flash"}]'
```

`--direct-token-mode auto` treats a `pt-` source as a PAT and exchanges it at
`/api/v1/jobToken/exchange`; `jt-`/`dt-` sources are used as bearer tokens.
The exchange and one subsequent 401/403 refresh retry stay inside the runner;
tokens are never written to the JSONL protocol or logs. CN and global Qoder
endpoints must be configured explicitly because their product families are not
interchangeable.

The checked-in `.npmrc` sets `ignore-scripts=true` because Qoder SDK
postinstall may download runtime assets; `--ignore-scripts` is retained above
as an explicit deployment signal. The runner never installs, updates,
logs in, logs out, or refreshes Qoder credentials. It does not fall back to the
SDK-bundled `qodercli 1.0.30`.

## Protocol

stdin and stdout carry one JSON object per line. Every request and response has
`protocol_version: 1`; asynchronous events carry one validated
`AgentEventV1`-compatible envelope. Supported methods are:

- `handshake`
- `readiness`
- `models`
- `start`
- `cancel`
- `close`
- `shutdown`

`start` carries structured text/image content plus the system prompt and the
Plugin's fixed skill, setting-source, tool, and MCP policy. The original bounded
Chat request is also carried for `direct_openai`, where messages and client
tools are preserved. The runner passes fixed values to Qoder Agent SDK 1.0.10
only when it creates the native session; later turns must retain the same fixed
configuration. Live model discovery preserves Qoder's vision, reasoning,
token-limit, and context-window metadata.

PAT runners receive the PAT through a per-process environment variable and the
request names only that variable. Qoder SDK 1.0.10 materializes a temporary
mode-0600 auth payload for its CLI child; the owning Plugin therefore supplies
an isolated `TMPDIR`/PAT `HOME`, terminates the runner process group on Unix,
and removes the private runtime root after exit. Local CLI mode uses
`qodercliAuth()` and does not invoke an interactive login flow; its
`profile_id` is a CPA label while the configured Qoder directory is the actual
credential-isolation boundary. Output is bounded by frame and queue limits,
and vendor stderr is redacted before it reaches runner stderr.
