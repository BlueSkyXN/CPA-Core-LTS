# Qoder provider plugin

This example implements the schema 5 `cpa-provider-qoder` dynamic plugin and
connects it to the separately installed `cpa-qoder-runner`.

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
permission_default: deny
permission_rules: []
```

The plugin does not run `npm install`, use the SDK-bundled `qodercli 1.0.30`,
start an interactive login, or refresh a PAT. PATs enter the dedicated runner
through an environment variable; JSONL frames identify that variable without
carrying its value. Qoder SDK 1.0.10 then creates a short-lived mode-0600 auth
payload for the CLI, so each runner receives a plugin-owned private `TMPDIR`
and PAT `HOME`; the plugin terminates the Unix process group and removes that
runtime directory after exit. Local CLI auth uses `qodercliAuth()` with
`config_dir` selected through the isolated runner environment; `profile_id` is
only a CPA label, and distinct accounts require distinct config directories.

Known executable model IDs are recorded exactly as returned by the installed
Qoder CLI. On the current CN catalog, `qfmodel` has the display name
`Qwen3.8-Flash`; the display name and guessed variants such as
`qwen3.8-flash` are not executable IDs. Per-auth discovery uses the SDK typed
`getAvailableModels()` response and preserves each returned ID verbatim.

Build:

```bash
cd go
go test ./...
go build -buildmode=c-shared -o cpa-provider-qoder.so .
```
