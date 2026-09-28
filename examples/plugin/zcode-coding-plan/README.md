# Coding Plan native Go plugin

`zcode-coding-plan` v0.4.0 is a single-account provider plugin maintained in this CPA-Core-LTS repository. It is a C-shared dynamic library, not a built-in provider or a Node worker. Product rules and ownership are in [SPEC.md](SPEC.md).

The plugin supports direct Anthropic Messages with Core's Responses HTTP JSON/SSE adapter and same-connection WebSocket continuation. It does not execute tools, maintain another chat history, discover accounts, scan official application directories, query quota or implement interactive login. Use only credentials and client identity you are authorized to use. Local synthetic acceptance is not proof of upstream acceptance, pricing or production logging safety.

## Build and test

Go 1.26+, CGO and a C compiler are required. From the repository root:

```sh
# Node is used only for the optional cross-language synthetic crypto test.
CP_NODE="$(command -v node)" go -C examples/plugin/zcode-coding-plan/go test -race -count=1 ./...
go -C examples/plugin/zcode-coding-plan/go vet ./...
sh examples/plugin/zcode-coding-plan/test-cpa.sh

# macOS output; use .so on Linux or .dll with an appropriate Windows CGO toolchain.
mkdir -p examples/plugin/bin
go -C examples/plugin/zcode-coding-plan/go build -trimpath -buildmode=c-shared \
  -o ../../bin/zcode-coding-plan.dylib .
```

CI provides Node 24.14.0 for the crypto reference. Without `CP_NODE`, only that optional parity test is skipped; the library build and runtime never start Node. `test-cpa.sh` builds a temporary library and tests the current Core checkout by default; `CPA_SOURCE` may explicitly select another checkout. It uses synthetic credentials, an in-memory upstream transport and a loopback WebSocket listener. Module resolution can use the network; setting `GOPROXY=off GOSUMDB=off` works when dependencies are already cached. It never calls real model or quota endpoints.

The root `go test ./...` does not enter nested modules. The existing `provider-connectors` PR job explicitly runs this plugin's unit/race/vet and dynamic integration tests. `make -C examples/plugin build` also includes the Go library; when installing its `zcode-coding-plan-go.*` output, rename it to `zcode-coding-plan.*` to keep the configured plugin ID stable. Compiled artifacts are not tracked.

## Full provider image and installable packages

The existing full provider distribution now builds Coding Plan together with CodeBuddy, Copilot and Qoder from the same Core commit for Linux amd64/arm64. It retains the `pat-provider-delivery` workflow and `-pat-providers` image tags; the standard Core image is unchanged. See [the full-image guide](../PAT_DOCKER.md) in the repository.

The full image includes `/opt/cpa-pat-plugins/zcode-coding-plan.so` and placeholder-only examples under `/opt/cpa-plugin-examples/zcode-coding-plan/`. Use `docker-compose.coding-plan.yml` alongside `docker-compose.pat-providers.yml` to mount an existing user-maintained private directory read-only at `/run/cpa-coding-plan`. Configure file-based key/device references relative to that directory; do not embed secrets or device identity in build arguments. The extra override is not required for deployments using only the other providers.

Each architecture exports `zcode-coding-plan_<version>_linux_<arch>.zip`, containing the stable library name, README, SPEC, LICENSE and example JSON files. The shared provider checksum/manifest records the Core commit, plugin version and platform, plus the per-plugin `direct_anthropic` transport. The examples leave the logging acknowledgement disabled and contain no personal values.

PR builds export CI artifacts and run an egress-blocked four-plugin image smoke; they do not publish releases or images. Manual publication still requires an existing Core release tag and explicit `publish=true`, and does not move the standard latest image tag. Source availability or successful packaging must not be described as a released/downloadable artifact until publication actually completes.

## Host requirements

C ABI 1 / schema 6 plus all five host features are required:

- `anthropic-plugin-responses-v1`
- `plugin-model-compat-v1`
- `http-disable-redirects-v1`
- `sensitive-endpoints-v1`
- `plugin-management-v1`

Missing capability declarations reject registration/reconfiguration before secrets or network use. Use the companion [Panel management feature](https://github.com/BlueSkyXN/CPA-Panel-LTS/pull/95) for generic configuration and readiness controls. No auth-list, auth-read, auth-write or model-execute permissions are needed.

## Accounts: inline single file (default) or private config reference (advanced)

The default account form is a single self-contained JSON file — credentials live inline, exactly like the Qoder/CodeBuddy PAT files:

```json
{"type":"zcode-coding-plan","label":"Coding Plan","api_key":"<your key>","device_id":"<device id>","request_retry":0}
```

- Panel's add-account form submits this shape directly; leave `device_id` blank to have the Panel generate one.
- `api_key` and `device_id` must be provided together, and cannot be combined with `config_file`.
- Inline credentials load only after the deployment owner enables `host_logging_disabled` once in **Panel → Plugins → Edit config** (see below). Until then readiness reports the acknowledgement as missing and no credential is used.
- Defaults without a private config: upstream `bigmodel` (Anthropic-compatible endpoint), models `glm-5.3` (text-only) and `glm-5.3-flash` (text+image) with 1,000,000-token context, 128,000-token output budget, thinking levels low/high/max, `max_inflight` 2, prompt mode `preserve`.

## Manual private configuration (advanced)

Copy [config.example.json](config.example.json) to a server-side private directory and replace every placeholder manually. Never place it or its secrets in the repository, a release archive or a browser field.

- API key and device ID each accept exactly one of their `_env` or `_file` references. Relative files resolve against the private config directory.
- Set environment variables before starting Core: a C-shared library has its own Go runtime and must not rely on later `os.Setenv` changes.
- Identity fields are explicit user-supplied values. There is no machine/account discovery or embedded personal fallback.
- `models` is optional; without it the built-in `glm-5.3` / `glm-5.3-flash` allowlist applies. Both built-in models carry context/output metadata limits 1000000/128000; limits do not increase the default output budget. Other limits require explicit configuration.
- `host_logging_disabled` in the private file defaults false and blocks secret loading until the deployment owner has audited logging. Setting it true does not disable any Core/proxy logger. `request-log: false` alone is not sufficient proof that error capture is off. For inline accounts the same acknowledgement is the management-config field of the same name.

Configure the file-reference form after manually installing the library in your selected plugin directory:

```yaml
plugins:
  enabled: true
  dir: "/absolute/path/to/plugins"
  configs:
    zcode-coding-plan:
      enabled: true
      config_file: "/absolute/path/to/private/config.json"
      # host_logging_disabled: true   # inline accounts only; private files keep it inside the file
      # Optional overrides; omitted fields inherit the private file.
      # prompt_mode: replace
      # prompt_template: review
      # prompt_move_position: first_user
      # allow_request_override: false
```

Upload an account reference through the existing Panel Auth Files page or place it in your configured auth directory:

```json
{"type":"zcode-coding-plan","label":"Coding Plan","request_retry":0}
```

Alternatively use [auth.example.json](auth.example.json) with its own absolute `config_file`. Two explicit references must agree. A plugin instance supports one config/account, not an account pool.

Panel **Plugins → Edit config** exposes the six public fields. Boolean fields distinguish inherit, true and false; choosing inherit removes that override. **Manage accounts** opens the existing account page. **Check readiness** loads available accounts but does not automatically probe; select an account and click the diagnostic button. Provider-only results cannot claim auth readiness. Local readiness does not verify remote acceptance or billing.

Configuration saved means the existing PATCH persisted its fields, not that asynchronous runtime reconfiguration succeeded. Check registration/effective status and explicit readiness separately. Reconfiguration rejects active requests and preserves the plugin's old memory snapshot on failure; Core's existing reload behavior is unchanged. For changes to identity, credentials or template files, stop traffic, cancel/finish requests and restart Core.

## Prompt policy and supported request shape

Private `prompt.templates` maps names to relative UTF-8 template files. `prompt.template` selects a default name. Modes:

| Mode | Effect |
| --- | --- |
| `preserve` | Keep caller system unchanged. |
| `replace` | Use the configured template as system. |
| `prepend` | Put the template before caller system blocks. |
| `append` | Put the template after caller system blocks. |
| `move_to_user` | Use the template as system and move caller system text into the first/last user message. |

Request selection uses the effective header:

```text
X-Coding-Plan-Prompt: {"mode":"replace","template":"review"}
```

Only mode/template/move_position are allowed, and `allow_request_override` must be true. Template names cannot be paths or URLs. Native Messages `x_coding_plan.prompt` remains supported; conflicting header/body sources fail. Responses body extensions that Core drops are rejected rather than recovered from OriginalRequest. WebSocket headers select a connection-level policy, not a separate per-turn history.

Text, custom function tools, paired tool results and existing thinking/signature replay are covered by synthetic tests. Default thinking is supplied by the provider. Explicit effort/manual/adaptive thinking, strict output schemas, priority tier, HTTP store/background, image/file/server tools and unsupported cache shapes fail explicitly. HTTP multi-turn requests must carry full input; a response ID alone does not restore HTTP history. WS input remains an array, and an explicit previous response ID must match the connection's latest response. Chat HTTP is not a verified delivery entry point.

## Safety and validation boundary

- Handshake and model requests both disable redirects; 307/308 cannot replay credentials or bodies elsewhere.
- SensitiveEndpoints protects the handshake credential exchange, not arbitrary model bodies, device fields or dynamic signature headers. Production logging still needs a separate audit.
- Cancellation closes owned handshake/model response handles. Normal message_stop finishes without waiting for EOF or counting a second terminal event.
- A plugin execution dispatches at most one model request. Disable unsuitable caller/Core retries and fallbacks separately; this is not an end-to-end exactly-once guarantee.
- Core unload drains active RPCs and does not force-cancel them. Stop traffic and cancel/finish requests before unload/restart. Tests isolate each C-shared scenario in a child process; they do not prove arbitrary hot replacement safety.

Validation before this source import included macOS arm64 Go 1.27.0/1.26.5 unit/race/vet, synthetic crypto parity, actual dynamic JSON/SSE/WS and management flows. macOS dynamic test children on Go 1.27 and 1.26.5 have intermittently failed to exit after their scenario finished; full suites have also passed, but the cause is not established and nonzero exits remain failures. Linux results are determined by the current PR's explicit plugin CI; no Windows dynamic-runtime, live billing, long-running load, TLS fingerprint, or production deployment claim is made.
