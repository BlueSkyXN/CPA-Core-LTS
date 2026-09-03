# internal/usage navigation card

`internal/usage/` 是 LTS 完整 usage statistics 的核心实现。
Read this card before changing aggregation, snapshot, import/export, token fields, latency, auth attribution, plugin delivery, or success/failure logic.
Key files: `logger_plugin.go`, `internal/api/handlers/management/usage.go`, `sdk/cliproxy/usage/`, `internal/redisqueue/`.

## Local invariants

- `usage-statistics-enabled` 只控制是否继续记录新数据；不要破坏已有 snapshot/export/import 的读取能力。
- Snapshot JSON 字段要继续兼容 Panel：`total_requests`、`success_count`、`failure_count`、`total_tokens`、`apis`、`models`、`details`、day/hour buckets。
- Request detail 必须保留 timestamp、latency、source、auth_index、token breakdown、failed 状态。
- Canonical request detail 还包括 alias、`reasoning_effort`、request/outbound/response/effective service tier、`generate`、`failure_reason`、`failure_status`。
- Canonical v3 timing 使用 `timing_version: 1`：`ttfb_ms` 是首个非空 upstream byte/payload，`ttft_ms` 是首个非空 reasoning content，`ttfa_ms` 是首个非空用户可见 assistant text；缺失事件保持 absent，不伪造零值。
- Token 统计要兼容 input/output/reasoning/cached/cache-read/cache-creation/total，并保留现有 normalise 逻辑。
- Export 使用 `CanonicalExportVersion = 3`。Import 接受 canonical v3，并仅迁移 token/timing 语义可证明的 released v1/v2 payload；migration 必须返回稳定 error code / receipt。
- Import 必须先完整校验 shape、token semantics 和 aggregate overflow，再原子 merge；不能部分写入。重复记录按现有 dedup identity 跳过。
- 没有 API key 时统计 key 依次使用 request endpoint、provider、`unknown`；不要把所有非 API-key 请求折叠到同一 bucket。

## Local rules

- 改统计字段时，同时检查 Management API usage handlers、Panel usage 页面、TUI dashboard、SDK usage record、Redis queue payload。
- 新增字段优先向后兼容：旧 export payload 能导入，新 export payload 不应让旧字段消失。
- 不要在未迁移既有数据的情况下改变 dedup identity；当前 identity 包括 API/model/timestamp/source/auth/failure/token shape，不包括 latency 与 service-tier metadata。
- 统计存储当前是 in-memory；不要在本目录里偷偷引入外部数据库、文件写入或后台网络依赖。

## Do not

- 不要删除 `/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import` 所需结构。
- 不要把完整统计替换成 recent requests 或 API-key-only summary。
- 不要在统计 detail 中记录 token、raw prompt、raw response、auth file 内容或 management secret。

## Validation

- `go test ./internal/usage`
- `go test ./internal/api/handlers/management -run Usage`
- `go test ./test -run Usage`
- Canonical import/export changes: `go test ./internal/usage ./internal/api/handlers/management -run 'MigrateV1|CanonicalV3|TimingV3|UsageManagementImport'`
- Contract guard: `scripts/check-lts-contract.sh`
- 跨字段或 import/export 兼容性改动后，再运行 `go test ./...`。
