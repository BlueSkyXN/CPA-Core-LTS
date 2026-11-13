> **V8 LTS:** `main` is the maintained V8 product line. Use the V8 configuration layout and companion V8 Panel. See [LTS V8 compatibility](docs/lts/v8-development-compatibility.md) for configuration paths, retained v0 APIs, cached-Panel write protection, and rollback requirements. Download builds from [Releases](https://github.com/BlueSkyXN/CPA-Core-LTS/releases/latest).

# CPA Core LTS

English | [中文](README_CN.md) | [日本語](README_JA.md)

CPA Core LTS is a long-term-maintained fork of `router-for-me/CLIProxyAPI`.

- LTS baseline: `v6.9.49`
- Baseline commit: `b8bba053fcdafd80abc2152c88c78f4e7713c05a`
- Upstream source: <https://github.com/router-for-me/CLIProxyAPI>
- LTS repository: <https://github.com/BlueSkyXN/CPA-Core-LTS>
- Companion Web UI: <https://github.com/BlueSkyXN/CPA-Panel-LTS>
- Default panel release source: `https://github.com/BlueSkyXN/CPA-Panel-LTS`

## Maintenance Model

This fork exists because upstream releases after the selected baseline changed or removed the full usage statistics flow. The goal of this repository is to keep the CLI proxy core maintainable while preserving the statistics behavior that existed at `v6.9.49`.

Maintenance rules:

- `main` is the only CPA-Core-LTS product line. Do not create a separate "statistics branch" for normal maintenance.
- CPA-Core-LTS tracks `router-for-me/CLIProxyAPI` by operator-driven protected full-sync merge PRs, usually prepared by AI agents when maintainers request a sync.
- Preserve usage collection, `internal/usage/`, `/v0/management/usage`, `/v0/management/usage/export`, `/v0/management/usage/import`, and `usage-statistics-enabled`.
- Upstream changes are accepted by default. If an upstream change conflicts with a protected delta, adapt the upstream change instead of allowing it to remove or weaken the LTS contract.
- Do not use GitHub Sync fork, file-level overwrites, or direct `git pull upstream main` on `main`.
- This repository does not schedule automatic upstream syncs; the documented process is the repeatable method for human/AI-operated sync work.
- Planned cleanup can remove promotional copy, unused features, and non-LTS release machinery, but must not weaken the statistics contract or `CPA-Panel-LTS` compatibility.

This core service is intended to pair with `CPA-Panel-LTS`, which keeps the matching management panel usage statistics UI. By default, `/management.html` is downloaded from the latest `CPA-Panel-LTS` release asset named `management.html`.

## Original Project Overview

A proxy server that provides OpenAI/Gemini/Claude/Codex/Grok compatible API interfaces for CLI.

It now also supports OpenAI Codex (GPT models) and Claude Code via OAuth.

So you can use local or multi-account CLI access with OpenAI(include Responses)/Gemini/Claude-compatible clients and SDKs.

## Overview

- OpenAI/Gemini/Claude/Grok compatible API endpoints for CLI models
- OpenAI Codex support (GPT models) via OAuth login
- Claude Code support via OAuth login
- Grok Build support via OAuth login
- Amp CLI and IDE extensions support with provider routing
- Streaming, non-streaming, and WebSocket responses where supported
- Function calling/tools support
- Multimodal input support (text and images)
- Multiple accounts with round-robin load balancing (Gemini, OpenAI, Claude, Grok)
- Simple CLI authentication flows (Gemini, OpenAI, Claude, Grok)
- Generative Language API Key support
- AI Studio Build multi-account load balancing
- Claude Code multi-account load balancing
- OpenAI Codex multi-account load balancing
- Grok Build multi-account load balancing
- OpenAI-compatible upstream providers via config (e.g., OpenRouter)
- Reusable Go SDK for embedding the proxy (see `docs/sdk-usage.md`)

## Getting Started

### Optional local admission control (Flow V3)

Flow V3 combines caller, model and individual upstream Auth-record concurrency/rolling-window limits with bounded queues and on-demand observation. Admission, realtime updates and resource sampling default to off. An omitted version means schema 3 for a new empty policy; existing non-empty policies without a version retain legacy semantics and are flagged as legacy-policy. Explicit version 1/2 also keeps legacy semantics. It does not replace provider quotas, Home or full usage statistics. Rejected policy updates retain the last good policy; streams release capacity only when the actual producer ends. See the [Flow guide](docs/lts/flow-control.md) for paired versions, migration and rollback.

CLIProxyAPI Guides: [https://help.router-for.me/](https://help.router-for.me/)

## Management API

see [MANAGEMENT_API.md](https://help.router-for.me/management/api)

## Usage Statistics

Upstream CLIProxyAPI v6.10.0 and later moved built-in usage statistics out of the core project. CPA-Core-LTS keeps the built-in `/v0/management/usage` statistics and export/import APIs for CPA-Panel-LTS compatibility, and also keeps the Redis-compatible usage queue so external dashboards can consume the same events if desired.

### [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper)

Standalone persistence and visualization service for CLIProxyAPI, with periodic data sync, SQLite storage, aggregate APIs, and a built-in dashboard for usage and statistics.

### [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus)

A self-hosted CLIProxyAPI management console and observability dashboard, available as a lightweight drop-in panel or a full Manager Server with persistent analytics. Manage OAuth credentials, AI providers, client API keys, model aliases, plugins, and CPA settings in one interface. SQLite-backed request history enables live monitoring, failure diagnosis, and token, latency, and estimated-cost breakdowns by model, provider, account, key, and channel. Supports editable model prices and one-click price sync, alongside Codex and xAI quota checks, reset information, account-health inspection, and controlled recovery actions. Deploy via Docker or cross-platform native packages, with data stored locally.

### [Oh-My-CPA](https://github.com/WizisCool/oh-my-cpa)

Modern Ant Design-based management console for CLIProxyAPI v8+, combining CPAMC-style administration with SQLite-backed request records and usage analytics. Covers OAuth accounts, API providers, client keys, quotas, pricing and CPA operations in one interface. Tracks request-level latency, TTFT, tokens and cost, with multi-dimensional filtering, live dashboards and token heatmaps. OpenRouter price sync, custom rates and per-request price snapshots keep historical costs stable; a built-in Agent and MCP tools support assisted investigation and management.

## SDK Docs

- Usage: [docs/sdk-usage.md](docs/sdk-usage.md)
- Advanced (executors & translators): [docs/sdk-advanced.md](docs/sdk-advanced.md)
- Access: [docs/sdk-access.md](docs/sdk-access.md)
- Watcher: [docs/sdk-watcher.md](docs/sdk-watcher.md)
- Custom Provider Example: `examples/custom-provider`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Who is with us?

The ecosystem list of projects built on CLIProxyAPI is maintained upstream: <https://github.com/router-for-me/CLIProxyAPI#who-is-with-us>

## More choices

Those projects are ports of CLIProxyAPI or inspired by it:

### [9Router](https://github.com/decolua/9router)

A Next.js implementation inspired by CLIProxyAPI, easy to install and use, built from scratch with format translation (OpenAI/Claude/Gemini/Ollama), combo system with auto-fallback, multi-account management with exponential backoff, a Next.js web dashboard, and support for CLI tools (Cursor, Claude Code, Cline, RooCode) - no API keys needed.

### [OmniRoute](https://github.com/diegosouzapw/OmniRoute)

Never stop coding. Smart routing to FREE & low-cost AI models with automatic fallback.

OmniRoute is an AI gateway for multi-provider LLMs: an OpenAI-compatible endpoint with smart routing, load balancing, retries, and fallbacks. Add policies, rate limits, caching, and observability for reliable, cost-aware inference.

### [Playful Proxy API Panel (PPAP)](https://github.com/daishuge/playful-proxy-api-panel)

A public CLIProxyAPI-compatible fork and bundled management panel. It keeps upstream-style usage while restoring built-in usage statistics, adding cache hit rate, first-byte latency, TPS tracking, and Docker-oriented self-hosted installation docs.

### [Codex Switch](https://github.com/9ycrooked/CodexSwitch)

This is a tool built with Tauri 2 + Vue 3 for managing multiple OpenAI Codex desktop accounts. Switch between saved ChatGPT/Codex certification profiles, check 5-hour and weekly quota usage in real time, verify token health, view active account details, and import or save auth.json files without manual copying.

### [Alex](https://github.com/madhavajay/alex)

A local Rust LLM proxy with an optional UI, inspired by CLIProxyAPI. It routes coding agents across providers with local trace capture, scriptable middleware, subscription bonding, failover, and messenger-assisted re-authentication.
### [cliproxy-rs](https://github.com/vayungodara/cliproxy-rs)

A Rust port of CLIProxyAPI that reads the same `config.yaml` and auth files and serves the same routes and v8 Management API, so you can switch between the two in either direction. Ships as a single binary with the management dashboard built in (per-account 5-hour and weekly quota view, account sign-in, client setup guides), and adds an opt-in `soonest-reset` routing strategy that spends the account whose weekly window resets first.

> [!NOTE]  
> If you have developed a port of CLIProxyAPI or a project inspired by it, please open a PR to add it to this list.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
