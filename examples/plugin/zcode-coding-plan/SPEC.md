# Coding Plan provider plugin contract

## Scope and source ownership

`examples/plugin/zcode-coding-plan/` is the maintained source for the single-account, native Go `zcode-coding-plan` dynamic plugin. It follows the existing provider-plugin layout without making the provider a built-in Core executor. Runtime code uses the Go standard library and C ABI 1 / RPC schema 6. Node is an optional test oracle, never a runtime worker.

The repository includes plugin source, placeholder-only configuration examples, synthetic fixtures and repeatable offline integration tests. It must not include personal credentials, device values, private configuration, local research, captured live traffic or compiled libraries. The plugin metadata points to the containing CPA-Core-LTS repository. This source integration does not authorize installation, inference, quota queries, artifact publication or a release.

## Host and management contracts

The host must advertise all five features before registration/reconfiguration can succeed: `anthropic-plugin-responses-v1`, `plugin-model-compat-v1`, `http-disable-redirects-v1`, `sensitive-endpoints-v1` and `plugin-management-v1`. Read the equivalent `config_json` projection; do not add a YAML parser. ABI/schema numbers alone do not imply these features.

- Declare manual import (`auth_import_only=true`), auth parsing/refresh, models, execution, cancellation and readiness. No interactive login, arbitrary HTTP, tool execution, exact token counting or quota polling.
- The account reference contains `type=zcode-coding-plan`, a label and `request_retry=0`. Its optional absolute `config_file` falls back to the plugin-level reference; two explicit references must agree. Never scan official application data.
- Public config fields are `config_file`, `prompt_mode`, `prompt_template`, `prompt_move_position` and `allow_request_override`. Unset fields inherit the server-side private file. A template is a preconfigured name, never a request-supplied path. Panel clear operations delete overrides rather than storing false implicitly.
- The private file owns credentials, device identity, model allowlist, model limits and template contents. Only the user supplies those values. `host_logging_disabled` is an explicit deployment-audit acknowledgement, not a logging switch.
- Provider-only readiness is not ready because no account was selected. Scoped diagnostics validate local configuration only; they do not handshake, invoke a model or verify upstream acceptance/billing. Status/error messages must not echo paths, keys or device values.

## Request and lifecycle rules

Core owns routing, account selection, Responses translation, connection history, usage attribution and callback contexts. The plugin owns its configuration snapshot, signer and active executions. It never keeps a second conversation history or emits a second usage report.

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

Prompt modes are preserve, replace, prepend, append and move_to_user. Request overrides require explicit permission. Use `X-Coding-Plan-Prompt` for cross-protocol selection; native `x_coding_plan.prompt` remains accepted, but conflicting sources fail. Never recover controls removed by Core from `OriginalRequest`.

Registration/reconfiguration validates management input before swapping state. Reconfiguration rejects active executions; failures preserve the plugin's prior memory snapshot. Admission rejects a config snapshot replaced after validation. This is not a promise that Core rolls back persisted YAML when its existing asynchronous reload fails.

Handshake and model requests both opt into DisableRedirects. SensitiveEndpoints covers handshake credentials, not arbitrary model content or signature headers. Termination/cancellation releases owned handles once. Core unload waits for active RPCs and is not a force-cancel mechanism: stop traffic and cancel/finish requests before unload/restart. Do not promise arbitrary c-shared hot replacement safety.

## Acceptance

1. Standalone Go unit/race/vet, including config, five prompt modes, valid/invalid controls, signature vectors, stream termination, cancellation and stale reconfiguration admission.
2. Node 24.14.0 synthetic crypto parity runs in CI via explicit `CP_NODE`; runtime/build do not need Node.
3. `test-cpa.sh` builds the actual C-shared library in a temporary directory and exercises the checkout's Core using an in-memory upstream transport and loopback WebSocket listener. Preserve JSON/SSE, tool/thinking history, parent-ID checks, no retry/replay, log canaries, cancellation and management/config tests.
4. Each dynamic scenario runs in a child process with its existing bounded deadline; failures and nonzero exits must fail the suite. Never treat a printed PASS as process success or increase deadlines to hide a hang.
5. Integrate into the existing provider-connectors PR job and LTS source guard. The Core root `go test ./...` does not traverse nested modules, so the explicit plugin job is required.
6. The source tree must build and test without the originating local workspace, private files or a separate Node product checkout. Cross-platform/live acceptance is claimed only for environments actually verified.
