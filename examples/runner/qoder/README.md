# cpa-qoder-runner

`cpa-qoder-runner` is the versioned Node.js boundary between the native
`cpa-provider-qoder` plugin and Qoder. It supports two explicit transports:

- `sdk_cli` (default): Qoder Agent SDK + an administrator-selected external
  `qoderclicn`/`qodercli`, with native sessions, workspace tools, skills, MCP,
  permissions, image input, and Agent events.
- `direct_openai`: an OpenAI-compatible Qoder endpoint, with client-owned
  messages/tools and standard SSE. It is a model/chat transport, not a native
  Qoder Agent session; no skills, MCP, or native Qoder tools are advertised.

The transport is selected by administrator configuration or an auth profile,
never by a client request. A session keeps one transport for its lifetime.
New integrations should use a `pt-` PAT. The runner also retains the legacy
`access_token`/`local_cli` auth-file paths for compatibility: `access_token`
values use the configured Direct token mode, while `local_cli` is SDK-only and
uses the caller's isolated `QODER_CONFIG_DIR`.

The runner deliberately requires an external Qoder CLI path for `sdk_cli`:

```bash
npm ci --ignore-scripts
npm run build
node dist/index.js --stdio \
  --cli-path /absolute/path/to/qoderclicn \
  --openapi-endpoint https://openapi.qoder.com.cn \
  --openapi-user-agent qoder/1.1.40
```

For PAT auth, `sdk_cli` exchanges the long-lived PAT for an in-memory Job Token
and supplies it through the SDK host-token callback. The runner adapts the
mode-0600 one-shot payload field used by Qoder Agent SDK 1.0.10 to the field
accepted by `qoderclicn` 1.1.40; the payload contains only the host-callback
selector, not either token. `local_cli` continues to use the selected read-only
CLI profile and does not require PAT exchange.

Direct OpenAI mode requires an explicit endpoint and either an exact model list
or an OpenAI-compatible model catalog endpoint:

```bash
node dist/index.js --stdio \
  --transport direct_openai \
  --direct-endpoint https://gateway.qoder.com.cn/model/v1/chat/completions \
  --openapi-endpoint https://openapi.qoder.com.cn \
  --openapi-user-agent qoder/1.1.40 \
  --direct-models-json '[{"id":"qmodel_38max","display_name":"Qwen3.8-Max"}]'
```

Direct mode always exchanges the PAT through
`POST /api/v1/jobToken/exchange`, keeps the Job Token and Refresh Token in
memory, and performs one bounded 401/403 refresh retry. Legacy
`--direct-token-mode bearer` remains available for opaque `access_token`
values. CN and global Qoder
endpoints must be configured explicitly because their product families are not
interchangeable.

The checked-in `.npmrc` sets `ignore-scripts=true` because Qoder SDK postinstall
may download runtime assets; `--ignore-scripts` is retained above as an explicit
deployment signal. The runner never installs, updates, logs in, logs out, or
persists Qoder credentials. It does not fall back to a bundled CLI.

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
only when it creates the native session; later turns retain the same fixed
configuration. Live model discovery preserves Qoder's vision, reasoning,
token-limit, and context-window metadata.

PAT runners receive the PAT through a per-process environment variable and the
request names only that variable. The runner keeps exchanged Job/Refresh Tokens
in memory, while Qoder SDK materializes and removes a temporary mode-0600
host-callback payload for its CLI child. The owning Plugin supplies an isolated
`TMPDIR` and PAT `HOME`, terminates the runner process group on Unix, and removes
the private runtime root after exit. Output is bounded by frame and queue
limits, and vendor stderr is redacted before it reaches runner stderr.
