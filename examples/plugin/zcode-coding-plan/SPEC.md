# Coding Plan provider plugin contract

## Scope and source ownership

`examples/plugin/zcode-coding-plan/` is the maintained source for the single-account, native Go `zcode-coding-plan` dynamic plugin. It follows the existing provider-plugin layout without making the provider a built-in Core executor. Runtime code uses the Go standard library and C ABI 1 / RPC schema 6. Node is an optional test oracle, never a runtime worker.

The repository includes plugin source, placeholder-only configuration examples, synthetic fixtures and repeatable offline integration tests. It must not include personal credentials, device values, private configuration, local research, captured live traffic or compiled libraries. The plugin metadata points to the containing CPA-Core-LTS repository. This source integration does not authorize installation, inference, quota queries, artifact publication or a release.

## Host and management contracts

The host must advertise all five features before registration/reconfiguration can succeed: `anthropic-plugin-responses-v1`, `plugin-model-compat-v1`, `http-disable-redirects-v1`, `sensitive-endpoints-v1` and `plugin-management-v1`. Read the equivalent `config_json` projection; do not add a YAML parser. ABI/schema numbers alone do not imply these features.

- Declare manual import (`auth_import_only=true`), auth parsing/refresh, models, execution, cancellation and readiness. No interactive login, arbitrary HTTP, tool execution, exact token counting or quota polling.
- The account form is a single self-contained JSON file: `type=zcode-coding-plan`, a label, `request_retry=0` and inline `api_key`+`device_id` credentials (both required together), mirroring the sibling PAT provider files. Account creators accept a manually entered API key and optional device ID. An explicit device ID wins; editing with no new value preserves the stored ID; a new account with no ID receives one UUID v4, persisted once. The plugin only consumes the saved value and never generates or rotates it. User-initiated local import may supply an existing client deviceMid, but it is not mandatory and does not verify billing entitlement. Legacy `config_file` references from 0.3.x are rejected. Never scan official application data from the plugin.
- Public config fields are `host_logging_disabled`, `upstream` (bigmodel|zai), `models` and `model_limits`; all are Panel-editable. Panel clear operations delete overrides rather than storing false implicitly.
- Built-in defaults own the effective configuration: `bigmodel` upstream, the `glm-5.3` (text-only) / `glm-5.3-flash` (text+image) allowlist with 1000000/128000 limits and thinking levels low/high/max. `host_logging_disabled` is an explicit deployment-audit acknowledgement, not a logging switch. Caller system prompts are preserved as-is; there is no template machinery.
- Provider-only readiness is not ready because no account was selected. Scoped diagnostics validate local configuration only; they do not handshake, invoke a model or verify upstream acceptance/billing. Status/error messages must not echo paths, keys or device values.

## Request and lifecycle rules

Core owns routing, account selection, Responses/Chat translation, connection history, usage attribution and callback contexts. The plugin owns its configuration snapshot, signer and active executions. It never keeps a second conversation history or emits a second usage report.

```mermaid
sequenceDiagram
    participant P as Panel
    participant C as Core config/auth/history
    participant R as Plugin runtime
    participant H as Host HTTP
    P->>C: PATCH touched config fields
    C->>R: reconfigure with equivalent config_json
    R->>R: reject busy; atomically replace valid snapshot
    P->>C: explicit diagnostic, optional auth_index
    C->>R: selected-account readiness
    R-->>P: local-only status through Core
    C->>R: effective Anthropic payload and selected auth
    R->>R: reject stale config; admit execution
    R->>H: signing handshake when required, no redirects
    R->>H: at most one model dispatch, no redirects
    H-->>R: JSON or SSE
    R-->>C: response or complete stream events
    C->>R: cancel scoped execution
    R->>H: close the owned response handle
```

System prompts are preserved; there are no template files, prompt modes or per-request overrides. Reject `x_coding_plan` and removed private/prompt management fields. Never recover controls removed by Core from `OriginalRequest`.

Client-executed function/custom/MCP tools require non-empty name and object input_schema, with absent type or native Messages type=custom. Preserve their definitions, cache controls and paired history. Native `web_search_20250305` / `web_search_20260209` are the supported exception: probe-verified (2026-10-01) as upstream-executed, they are allowed through without a client schema, and web_search/web_search_prime server-tool history pairs round-trip. All other provider-native tool types are rejected even when a caller supplies an input_schema; distinguish unsupported native tools from malformed client fields without echoing caller-controlled names, types or schemas. web_fetch history is still rejected explicitly rather than fabricating results. All local rejections remain request-scoped and occur before signer/network callbacks. Never restore native tools stripped by Core from OriginalRequest.

ModelInfo.NativeCapabilities.WebSearch is an optional additive plugin API field, retaining unknown (nil), false and true without changing ABI/schema versions. Coding Plan declares true only for probe-verified built-in allowlisted IDs; custom IDs stay unknown. The host preserves this metadata across model conversion/cloning and the existing CPA capability catalog; legacy plugins remain unknown. Generic model listings and client-local capability overrides remain separate contracts, and ordinary client tools are unaffected. NativeCapabilities.WebSearchReplay is a second optional additive field selecting the search-history replay protocol (`bigmodel`); it is a protocol discriminator, not a capability - search-capable native Anthropic models keep the strict `encrypted_content` replay rules, so search support alone never selects it. Coding Plan sets both fields on built-in allowlisted IDs and neither on custom IDs; legacy payloads decode with the field empty.

Built-in model effort is low/high/max. Normalize effective native `reasoning_effort`, `output_config.effort` or translated Responses effort to GLM `reasoning_effort` and `thinking.type=enabled`; do not invent manual budgets. Reject unsupported levels, contradictory controls and disabled thinking. Flash image sources are validated base64 JPEG/PNG/GIF/WebP or HTTP(S) URLs without credentials; text-only models reject images, and the plugin never fetches image URLs. Custom allowlisted IDs do not advertise unverified built-in capabilities. Tests assert the outgoing fields; live acceptance at the Anthropic-compatible endpoint remains separate.

For built-in GLM, accept effective `thinking.display=summarized` and consume it locally; never send display upstream. Preserve effective effort, enabled thinking and boolean clear_thinking. Reject effective omitted, unsupported strings and non-string display values before handshake/model dispatch. Do not recover removed controls from OriginalRequest. Responses auto/concise/detailed are display intent, not a promise of actual summarization. The shared translator only carries disabled summary intent when target thinking is active; reliable server-side hiding remains unsupported.

Expose provider-public thinking through Responses summary_text, Chat reasoning_content or native Messages thinking, without mixing it into the final answer, duplicating raw/summary, decoding opaque payloads or adding an inference call. Retain existing Responses/Messages signed history. Chat has no new signature carrier. Chat and Responses requests use selected-route capabilities; non-nil empty capability metadata must not inherit peers or invent a thinking budget. Chat supports ordinary Message JSON and checked full-frame SSE through the shared Anthropic event bridge; retain hook order, cancellation, terminal validation and single usage reporting.

Bind the configured account to the Core AuthID. Same-account credential rotation replaces config/signing snapshots only when no execution is active; busy and stale admission are explicit errors. Reconfiguration retains account ownership. Account/provider closure cancels owned work and releases credentials and ownership after the final execution finishes; session-only closure does not. Every configuration lookup and admission checks the logging acknowledgement. Successful false/inherit reconfiguration removes cached credentials and reports not-ready; re-acknowledgement reloads the selected account.

Registration/reconfiguration validates management input before swapping state. Reconfiguration rejects active executions; failures preserve the plugin's prior memory snapshot. Admission rejects a config snapshot replaced after validation. This is not a promise that Core rolls back persisted YAML when its existing asynchronous reload fails.

Handshake and model requests both opt into DisableRedirects. SensitiveEndpoints covers handshake credentials, not arbitrary model content or signature headers. Termination/cancellation releases owned handles once. Core unload waits for active RPCs and is not a force-cancel mechanism: stop traffic and cancel/finish requests before unload/restart. Do not promise arbitrary c-shared hot replacement safety.

## Full provider distribution

Reuse the existing `Dockerfile.pat-providers`, `pat-provider-delivery` workflow and package manifest. The optional full image includes CodeBuddy, Copilot, Qoder and Coding Plan built from the same Core commit on Linux amd64/arm64. Standard Core images/releases remain unchanged; no new publication channel, automatic deployment or runtime Node dependency.

- Keep the Coding Plan library/plugin ID `zcode-coding-plan.so` / `zcode-coding-plan`; do not rename it to a new provider identity. Publishable archive names are `zcode-coding-plan_<pluginVersion>_linux_<arch>.zip`, carrying one library, README, SPEC, license and a placeholder-only auth example. Existing three plugin archive names and contents remain compatible.
- A single Go `pluginVersion` constant supplies runtime metadata/readiness and packaging. The manifest retains existing Core SHA/platform/version, runner and checksum fields, adds Coding Plan to `plugins`, and reports mixed transports with a per-plugin map rather than claiming every plugin uses OpenAI wire format.
- Accounts are self-contained auth files with inline credentials; no additional private configuration or credential mount is needed beyond the normal auth directory. Examples must not include personal defaults or weaken the logging acknowledgement.
- Full-image fixtures register a synthetic inline account without an additional credential mount, check provider-only not-ready and selected-auth locally-ready states, and repeat after container recreation. Block container egress and do not invoke models, handshake or quota endpoints. The Linux-local Docker smoke connects to the inspected private bridge IP; it does not require port publishing on an internal network, and reports container state/logs before cleanup when startup fails. An unconfigured account is never reported ready merely because the library loaded.
- Both PR architecture jobs build/smoke the image and export all four archives/checksums. Publishing remains manual, pinned to an existing Core tag, preserves standard latest aliases and checks SHA/platform/checksums before upload. A previous three-plugin artifact cannot satisfy a four-plugin release.

## Acceptance

1. Standalone Go unit/race/vet, including removed-field rejection, low/high/max wire controls, model-specific images, logging acknowledgement revocation/recovery, same-account rotation, second-account rejection, deletion/replacement, signature vectors, stream termination, cancellation and stale admission.
2. Node 24.14.0 synthetic crypto parity runs in CI via explicit `CP_NODE`; runtime/build do not need Node.
3. `test-cpa.sh` builds the actual C-shared library in a temporary directory and exercises the checkout's Core using an in-memory upstream transport and loopback WebSocket listener. Preserve JSON/SSE, tool/thinking history, parent-ID checks, no retry/replay, log canaries, cancellation and management/config tests.
4. Each dynamic scenario runs in a child process with its existing bounded deadline; failures and nonzero exits must fail the suite. Never treat a printed PASS as process success or increase deadlines to hide a hang.
5. Integrate into the existing provider-connectors PR job and LTS source guard. The Core root `go test ./...` does not traverse nested modules, so the explicit plugin job is required.
6. The new three-protocol reasoning-display dynamic fixture is an acceptance requirement, not a completed E2E claim. Run it only when that testing is authorized. Mock host/translator tests are distinct from dynamic-library, real-client and upstream acceptance.
7. The source tree must build and test without the originating local workspace, private files or a separate Node product checkout. Cross-platform/live acceptance is claimed only for environments actually verified.
