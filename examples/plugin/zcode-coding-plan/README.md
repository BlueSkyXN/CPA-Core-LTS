# Coding Plan native Go plugin

`zcode-coding-plan` v0.4.2 is a single-account provider plugin maintained in this CPA-Core-LTS repository. It is a C-shared dynamic library, not a built-in provider or a Node worker. Product rules and ownership are in [SPEC.md](SPEC.md).

The plugin supports direct Anthropic Messages with Core's Responses HTTP JSON/SSE adapter and same-connection WebSocket continuation. The matching Core source also provides Chat Completions JSON/SSE conversion; updating the plugin alone on an older Core does not add that adapter. It does not execute tools, maintain another chat history, discover accounts, scan official application directories, query quota or implement interactive login. Use only credentials and client identity you are authorized to use. Local synthetic acceptance is not proof of upstream acceptance, pricing or production logging safety.

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

The full image includes `/opt/cpa-pat-plugins/zcode-coding-plan.so` and a placeholder-only `auth.example.json` under `/opt/cpa-plugin-examples/zcode-coding-plan/`. No extra credential mounts, private configuration directories or environment variables are required: the account is one self-contained auth file (see below). The normal Core configuration and auth-directory persistence remain required.

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

## Accounts: single self-contained file

The only account form is one JSON file with inline credentials, exactly like the Qoder/CodeBuddy PAT files:

```json
{"type":"zcode-coding-plan","label":"Coding Plan","api_key":"<your key>","device_id":"<device id>","request_retry":0}
```

- Panel's add-account form requires plugin 0.4.0 or newer and accepts a manually entered API key. You may enter an existing client `deviceMid` as `device_id`; an explicit value wins. Leave it blank to preserve the saved ID while editing, or generate and save one UUID v4 for a new account. The plugin never generates a per-request replacement. Manual entry does not require an official client installation; importing an existing ID is optional and neither path guarantees upstream acceptance or billing discounts.
- `api_key` and `device_id` must be provided together. Legacy `config_file` auth references and removed private/prompt management fields are rejected. Panel editing removes the obsolete account reference; administrators must remove obsolete plugin config fields separately.
- Credentials load only while `host_logging_disabled` is explicitly true in **Panel → Plugins → Edit config**. Setting it true does not disable any logger; it acknowledges that CPA raw request and error-body logs are off. A successful reconfiguration to false or inherit discards cached credentials/signing state, blocks new execution and reports not-ready. Reconfirming allows the selected account to load again.
- Built-in defaults: upstream `bigmodel` (`https://open.bigmodel.cn/api/anthropic/v1/messages`; switch to `zai`/`api.z.ai` via the management `upstream` field), models `glm-5.3` (text-only) and `glm-5.3-flash` (text+image) with 1,000,000-token context, 128,000-token output budget, thinking levels low/high/max, `max_inflight` 2, caller system prompt preserved as-is.
- Optional management overrides (`models`, `model_limits`) cover non-default allowlists; they are Panel-editable fields, not files.

Upload through the Panel Auth Files page or place the file in your configured auth directory. A plugin instance supports one account, not an account pool. Ownership follows Core's stable `AuthID`, not the API key. Updating that account's key or device ID replaces the signer once active requests finish; an overlapping update returns busy and can be retried. Removing the account cancels its executions and releases the binding after cleanup, so a replacement can load without restarting Core. Ordinary conversation closure does not remove the account binding.

Panel **Plugins → Edit config** exposes the four public fields (host_logging_disabled, upstream, models, model_limits). Boolean fields distinguish inherit, true and false; choosing inherit removes that override. **Manage accounts** opens the existing account page. **Check readiness** loads available accounts but does not automatically probe; select an account and click the diagnostic button. Provider-only results cannot claim auth readiness. Local readiness does not verify remote acceptance or billing.

Configuration saved means the existing PATCH persisted its fields, not that asynchronous runtime reconfiguration succeeded. Check registration/effective status and explicit readiness separately. Reconfiguration rejects active requests and preserves the plugin's old memory snapshot on failure; Core's existing reload behavior is unchanged.

## Supported request shape

Caller system prompts are preserved. Template files, prompt modes and per-request prompt selection were removed. `x_coding_plan` is rejected and `X-Coding-Plan-Prompt` no longer selects a policy. Controls stripped from the effective Core payload are never restored from `OriginalRequest`.

The built-in GLM models accept `low`, `high` and `max` effort. Native Messages may supply `reasoning_effort` or `output_config.effort`; Responses supplies `reasoning.effort`, and Chat Completions supplies `reasoning_effort`, which Core translates before plugin execution. Responses and Chat use the selected account/model capabilities, not another provider's same-name model. The plugin normalizes the effective control to `reasoning_effort` plus `thinking.type=enabled`, without inventing token budgets. Conflicting fields, unsupported levels, manual budgets and disabled thinking are rejected. Omitting controls preserves the upstream default. Optional custom model IDs do not inherit unverified built-in thinking/image capabilities.

### Reasoning display compatibility

With the matching Core source, `reasoning.summary: "auto"` in Responses (including summary-only requests) is translated to `thinking.display: "summarized"`. Built-in GLM models accept this effective display control locally and omit it from the upstream request. Chat's non-`none` effort retains Core's existing implicit request for visible reasoning. Native Messages may send `thinking: {"type":"enabled","display":"summarized"}`. Effort, enabled thinking and boolean `clear_thinking` are preserved.

Public thinking text returned by the provider stays separate from the final answer:

| Client protocol | JSON | SSE |
|---|---|---|
| Responses | reasoning item `summary[].text` (`summary_text`) | `response.reasoning_summary_text.delta` and matching part/done events |
| Chat Completions | `choices[].message.reasoning_content` | `choices[].delta.reasoning_content` |
| Messages | `content[]` thinking blocks | thinking blocks / `thinking_delta` |

The text is not compressed or regenerated. `concise`/`detailed` currently select the same summary-compatible display channel; they do not guarantee a particular summary length. No second model call or raw/summary duplication is introduced. Only provider-public text is displayed; signatures and redacted/encrypted payloads are not decrypted or rendered as reasoning. Responses/Messages retain their existing opaque replay handling; Chat `reasoning_content` is a text compatibility extension, not a lossless carrier for Anthropic signatures.

Effective `thinking.display: "omitted"` is still rejected before upstream dispatch, as are invalid display values/types. Responses `summary: "none"` or `null` is not equivalent to leaving the field out: Core converts it to `omitted` when thinking is active. Without an active target thinking mode, the shared converter may not carry that hide intent; this change does not provide reliable server-side hiding. Do not use these fields as a privacy guarantee. Controls removed by Core normalizers are never restored from `OriginalRequest`.

Current Codex and ZCode clients can consume the summary compatibility channel without raw-only output. The offline dynamic-library suite covers all three protocol adapters; actual client UI rendering and live upstream acceptance remain separate checks. Installing this source does not edit client settings, publish packages or update a deployment; xAI/Grok's existing normalization remains unchanged.

The field names and mandatory thinking behavior follow the official [GLM-5.3 model guide](https://docs.bigmodel.cn/cn/guide/models/text/glm-5.3.md) and [migration guide](https://docs.bigmodel.cn/cn/guide/start/migrate-to-glm-new.md). These guides demonstrate Chat Completions fields; the plugin's normalization over its Anthropic-compatible endpoint is covered by synthetic wire assertions, not a claim of live upstream acceptance. Real account/provider acceptance remains a deployment check.

`glm-5.3-flash` accepts native Anthropic image blocks and translated Responses `input_image`: base64 JPEG/PNG/GIF/WebP or HTTP(S) image URLs without embedded credentials. The plugin validates and forwards image sources but does not fetch URLs itself. Text-only `glm-5.3` rejects images before upstream dispatch. Custom function tools, paired text/image tool results and thinking/signature history remain supported.

### Client tools and native WebSearch

Client-executed function/custom tools remain supported across Responses, Chat and Messages. Core translates OpenAI function/custom definitions into Anthropic `name` plus `input_schema`; MCP search and coding tools use this same path. A client-executed function named `web_search` is valid. The plugin does not execute any tool itself.

Provider-native tools such as Anthropic `web_search_20250305` / `web_search_20260209` are not supported by this plugin. Supplying an `input_schema` does not turn a server tool into a client tool. Native definitions fail before signing/model dispatch, with a specific WebSearch diagnostic; malformed client definitions identify their `tools[index]` field without echoing caller-provided names or schemas. Native search history is also rejected explicitly, not silently dropped or rewritten into a fake tool result.

The matching Core preserves optional plugin `ModelInfo.NativeCapabilities.WebSearch` metadata. Coding Plan declares it `false` for every allowlisted model, including custom IDs: this is an executor boundary, not an inference about a model family. CPA-aware `/v1/models?client_version=cpa` consumers receive `cpa_capabilities.web_search=false`. Other clients must configure their own capability flag; generic model listings do not automatically reconfigure ZCode or Codex. Older plugins omit the additive field and retain unknown support; ABI/schema versions are unchanged.

For ZCode, set only this model property, preserving ordinary tool support and the rest of the model configuration:

```json
{"properties":{"supportsNativeWebSearch":false}}
```

Merge it into the relevant provider/model rule in the active `provider_config.json`, under `config.modelConfigRules.manualProviderModelRules[].config` for a manually configured model. The current client keeps that file under its selected data base directory's `.zcode/v2/`; it is not the older `config.json`. Do not create a second automatic and manual rule for the same provider/model. This disables only the built-in WebSearch exposed for that model. ZCode's built-in WebSearch makes a second provider-native model request; it is not an independent client-side search service and has no automatic external-search fallback. Use a separately configured client-executed function/MCP search tool when needed. After changing capabilities, start a new conversation if the old history contains server-tool blocks. Do not disable all tools, fabricate schemas, or silently remove tools to force a successful response.

Strict output schemas, priority tier, HTTP store/background, files, server tools and unsupported cache shapes fail explicitly. HTTP multi-turn requests must carry full input; a response ID alone does not restore HTTP history. WS input remains an array, and an explicit previous response ID must match the connection's latest response. The offline suite loads the actual C-shared library into the current Core and covers three-protocol JSON/SSE client tools plus native-tool pre-dispatch rejection. It uses synthetic credentials and an in-memory upstream transport, not a real search service or client UI.

## Safety and validation boundary

- Handshake and model requests both disable redirects; 307/308 cannot replay credentials or bodies elsewhere.
- SensitiveEndpoints protects the handshake credential exchange, not arbitrary model bodies, device fields or dynamic signature headers. Production logging still needs a separate audit.
- Cancellation closes owned handshake/model response handles. Normal message_stop finishes without waiting for EOF or counting a second terminal event.
- A plugin execution dispatches at most one model request. Disable unsuitable caller/Core retries and fallbacks separately; this is not an end-to-end exactly-once guarantee.
- Core unload drains active RPCs and does not force-cancel them. Stop traffic and cancel/finish requests before unload/restart. Tests isolate each C-shared scenario in a child process; they do not prove arbitrary hot replacement safety.

Validation before this source import included macOS arm64 Go 1.27.0/1.26.5 unit/race/vet, synthetic crypto parity, actual dynamic JSON/SSE/WS and management flows. macOS dynamic test children on Go 1.27 and 1.26.5 have intermittently failed to exit after their scenario finished; full suites have also passed, but the cause is not established and nonzero exits remain failures. Linux results are determined by the current PR's explicit plugin CI; no Windows dynamic-runtime, live billing, long-running load, TLS fingerprint, or production deployment claim is made.
