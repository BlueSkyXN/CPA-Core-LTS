# Standard Dynamic Library Plugin Examples

This directory contains standard dynamic library plugin examples for the CLIProxyAPI C ABI.

## Layout

- `simple/`: broad provider-native skeleton for the original synchronous capabilities.
- `model/`: model capability only.
- `auth/`: auth provider capability only.
- `frontend-auth/`: frontend auth provider capability only.
- `frontend-auth-exclusive/`: frontend auth provider that becomes the only request authentication provider when selected.
- `executor/`: executor capability only.
- `protocol-format/`: minimal executor focused on input/output format declarations.
- `request-translator/`: request translation capability only.
- `request-normalizer/`: request normalization capability only.
- `codex-service-tier/`: Go-only request normalizer that sets Codex `gpt-5.5` requests to the priority service tier when enabled.
- `request-lifecycle/`: Go-only request admission example with concurrency control, active HTTP termination, and terminal callbacks.
- `scheduler/`: Go-only scheduler that can select a configured auth ID, delegate to a built-in scheduler, or deny picks.
- `claude-web-search-router/`: ModelRouter + executor for Claude Code built-in `web_search` (antigravity / codex / xai / Tavily). See `claude-web-search-router/README.md`.
- `codebuddy/`: schema 5 CodeBuddy AuthProvider/ModelsForAuth/Executor and read-only Summary integration over the direct HTTPS lane; models are discovered per PAT.
- `qoder/`: schema 5 Qoder AuthProvider/ModelsForAuth/Executor with new PAT auth, legacy auth-file compatibility, and read-only Summary integration through the separately versioned `cpa-qoder-runner`.
- `response-translator/`: response translation capability only.
- `response-normalizer/`: response normalization capability only.
- `thinking/`: thinking applier capability only.
- `usage/`: usage observer capability only.
- `cli/`: command-line capability only.
- `management-api/`: Management API and resource capability only.
- `host-callback/`: minimal plugin resource that demonstrates host callbacks.
- `host-callback-auth-files/`: Go-only plugin resource that calls host auth file callbacks.
- `host-model-callback/`: Go-only plugin resource that calls the host model execution callbacks.

Most standard capability examples contain `go/`, `c/`, and `rust/` subdirectories. Specialized examples may provide only the implementation language they need.

## Codex Service Tier

`codex-service-tier` declares the request normalization capability. When `fast` is `true`, it sets `service_tier` to `priority` for requests where `req.ToFormat` is `codex` and `req.Model` is `gpt-5.5`.

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

## Request Lifecycle

`request-lifecycle` combines `request_interceptor` with `request_lifecycle_plugin`. It acquires a concurrency slot before auth selection, can return a custom `403` or `429` response without contacting an upstream model, and releases admitted slots from `request.complete` on success, failure, rejection, or cancellation.

```yaml
plugins:
  configs:
    request-lifecycle:
      enabled: true
      priority: 100
      max_concurrency: 2
      reject_keyword: "blocked"
```

See `request-lifecycle/README.md` for build instructions and lifecycle semantics.

## Provider Execution Lifecycle (schema 5)

Schema 5 adds typed `RequestID`, `ExecutionSessionID`, `CallerScope`, `WorkspaceIdentity`, and `AuthIndex` fields to executor requests and three optional provider capabilities. Plugins must key native sessions by provider/auth/session plus the caller/workspace namespace; `CallerScope` is irreversible and `WorkspaceIdentity` must be opaque and secret-safe rather than a raw path:

- `execution_canceller` / `executor.cancel` interrupts one active execution while preserving its session;
- `execution_session_closer` / `executor.close_session` releases one session, one auth's sessions, or all plugin sessions;
- `provider_readiness` / `executor.readiness` reports `plugin_installed`, `runner_installed`, `protocol_ready`, `auth_ready`, and `session_ready` separately.

Cancel and close calls are idempotent lifecycle operations. A plugin that advertises them must allow the host to call them concurrently with `executor.execute` or `executor.execute_stream`; it must not implement cancel by closing the whole session. The host bounds how long Core waits during context cancellation, plugin replacement, auth removal, and shutdown. A timed-out native call may still be running, so the plugin/Runner remains responsible for cooperative cancellation, graceful drain, and kill fallback.

Pre-execution admission uses `Purpose=admission` and probes plugin, runner, protocol, and the selected auth with a bounded host timeout. It receives the selected auth's provider-owned storage, metadata, and attributes, which are credential-bearing and must never enter diagnostics or logs. Admission intentionally omits `ExecutionSessionID`: `executor.execute` or `executor.execute_stream` may be the operation that creates or attaches the vendor-native session. Explicit status probes use `Purpose=diagnostic`; supply `ExecutionSessionID` when checking an already-known session. Probes may inspect or start configured local runner resources, but must remain idempotent/non-interactive and must not execute a model turn, create a vendor session, mutate a workspace, persist credentials, or trigger billable work. Provider/runner/protocol failures stop credential fallback without cooling the selected auth; an `auth_ready` failure may rotate to another credential without cooling the failed one. Admission runs before selected-auth callbacks, dispatch markers, and result accounting, so a rejected candidate is not reported or counted as an upstream attempt and any provisional session-affinity binding is released.

Plugins that negotiate schema 4 or earlier continue to execute without these optional methods. Their RPC executor request omits the new typed fields; the existing metadata extension bag remains available for compatibility. `RequestID` identifies one execution across auth retries and is distinct from the HTTP/logging trace ID. `ExecutionSessionID` is populated only for an explicit Core-owned lifecycle; an affinity-only `derived_session_id` remains metadata and must not be treated as a native session.

## Host Auth Files Callback

`host-callback-auth-files` declares the Management API capability and exposes a browser resource named `Host Auth Files`. The resource demonstrates `host.auth.list`, `host.auth.get` (physical JSON file), `host.auth.get_runtime`, and `host.auth.save`.

```yaml
plugins:
  configs:
    host-callback-auth-files:
      enabled: true
      priority: 1
      permissions:
        auth-list: true
        auth-read: true
        auth-write: true
```

See `host-callback-auth-files/README.md` for URL examples.

## Host Model Callback

`host-model-callback` declares the Management API capability and exposes a browser resource named `Host Model Callback`. The resource calls `host.model.execute` for non-streaming requests and `host.model.execute_stream` plus `host.model.stream_read` for streaming requests. It demonstrates explicit stream close with `host.model.stream_close` and an `implicit_close=true` option for RPC-scope host cleanup.

When the resource forwards its `host_callback_id`, CPA identifies the plugin that initiated the host model callback and skips that same plugin's interceptors for the nested execution. This makes host model callbacks non-recursive for the caller while allowing other plugins to intercept the nested request.

```yaml
plugins:
  configs:
    host-model-callback:
      enabled: true
      priority: 1
      permissions:
        model-execute: true
```

The default example model is `gpt-5.5`, but the request succeeds only when the current CPA model and auth configuration can route that model.

## Scheduler

`scheduler` declares the scheduler capability. It can select a configured auth ID from the candidate list, delegate to the built-in `fill-first` or `round-robin` scheduler, or reject picks when `deny` is `true`.

```yaml
plugins:
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

`auth_id` selects a matching candidate when `delegate` is empty. `delegate` accepts `""`, `fill-first`, or `round-robin`; other non-empty values leave the pick unhandled. `deny` returns a scheduler error.

## Build All Examples

```bash
make -C examples/plugin list
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin`.

## Notes

`protocol-format` uses a minimal executor because format declarations belong to executor capabilities.

`host-callback` uses a minimal plugin resource because host callbacks are invoked from plugin methods and are not standalone capabilities.

Menu resources returned by `management.register` through the `resources` field are exposed by CPA under `/v0/resource/plugins/<pluginID>/...`. Authenticated plugin Management API routes remain under `/v0/management/...`.
