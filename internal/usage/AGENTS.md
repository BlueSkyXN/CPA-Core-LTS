# internal/usage navigation card

`internal/usage/` 是 LTS 完整 usage statistics 的核心实现。
Read this card before changing aggregation, snapshot, import/export, token fields, latency, or success/failure logic.
Key files: `logger_plugin.go`, `logger_plugin_test.go`, `internal/api/handlers/management/usage.go`, `sdk/cliproxy/usage/`.

## Local invariants

- `usage-statistics-enabled` 只控制是否继续记录新数据；不要破坏已有 snapshot/export/import 的读取能力。
- Snapshot JSON 字段要继续兼容 Panel：`total_requests`、`success_count`、`failure_count`、`total_tokens`、`apis`、`models`、`details`、day/hour buckets。
- Request detail 必须保留 timestamp、latency、source、auth_index、token breakdown、failed 状态。
- Token 统计要兼容 input/output/reasoning/cached/total；`total_tokens` 缺失时按现有 normalise 逻辑补齐。
- Import 必须 merge，而不是覆盖；重复记录要按现有去重语义跳过。
- 没有 API key 时继续通过 context/auth metadata 解析统计归属，不要退化成单一 unknown bucket。

## Local rules

- 改统计字段时，同时检查 Management API usage handlers、Panel usage 页面、TUI dashboard、SDK usage record。
- 新增字段优先向后兼容：旧 export payload 能导入，新 export payload 不应让旧字段消失。
- 统计存储当前是 in-memory；不要在本目录里偷偷引入外部数据库、文件写入或后台网络依赖。

## Do not

- 不要删除 `/v0/management/usage`、`/v0/management/usage/export`、`/v0/management/usage/import` 所需结构。
- 不要把完整统计替换成 recent requests 或 API-key-only summary。
- 不要在统计 detail 中记录 token、raw prompt、raw response、auth file 内容或 management secret。

## Validation

- `go test ./internal/usage`
- `go test ./internal/api/handlers/management -run Usage`
- `go test ./test -run Usage`
- 跨字段或 import/export 兼容性改动后，再运行 `go test ./...`。
