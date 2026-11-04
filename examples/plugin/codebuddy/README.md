# CodeBuddy Provider Plugin

`cpa-provider-codebuddy` is a native Go dynamic plugin for the CodeBuddy and
WorkBuddy-compatible direct HTTPS Chat Completions lane. `workbuddy` is not a
separate CPA Provider: the selected CodeBuddy CLI PAT/API key is the only
credential passed to the vendor service.

从 0.2.0 起支持流式 SSE 和普通 JSON 响应；非流式通过同一次上游 SSE 聚合完成，
不额外生成或重放请求。插件保留直接模型调用边界，不声明原生 session、workspace
tools、`codebuddy --serve`、ACP 或 execution-session closer 能力。

## Auth file

The recommended credential format is:

```json
{
  "type": "codebuddy",
  "auth_mode": "pat",
  "pat": "[REDACTED_SECRET]",
  "label": "CodeBuddy 主账号"
}
```

The existing API-key format remains accepted:

```json
{
  "type": "codebuddy",
  "auth_mode": "api_key",
  "api_key": "[REDACTED_SECRET]",
  "label": "CodeBuddy Legacy"
}
```

The selected credential is sent to Chat as both `Authorization: Bearer` and
`X-API-Key`. Catalog and billing calls use the vendor-required `X-API-Key`,
`X-Product: SaaS`, and catalog User-Agent headers. Credential values remain in
provider-owned `StorageJSON` and are never returned by readiness or Summary.

## Build

Run from this directory:

```bash
cd go
go test -count=1 ./...
go test -race -count=1 ./...
go build -buildmode=c-shared -o /tmp/cpa-provider-codebuddy.dylib .
rm -f /tmp/cpa-provider-codebuddy.h
```

Use `.so` on Linux and `.dll` on Windows. The installed dynamic-library
basename must be `cpa-provider-codebuddy` so it matches the plugin key.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-provider-codebuddy:
      enabled: true
      priority: 100
      permissions:
        auth-read: true # required by the read-only Summary route
      endpoint: https://copilot.tencent.com/v2/chat/completions
      catalog_endpoint: https://copilot.tencent.com/v3/config
      catalog_user_agent: WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0
      billing_endpoint: https://copilot.tencent.com/v2/billing/meter/get-user-resource
      # account_endpoint is optional; failed account probing falls back to the
      # auth label and a non-secret credential fingerprint.
```

`endpoint`, `catalog_endpoint`, and `billing_endpoint` are validated as
absolute HTTPS URLs. Plain HTTP is accepted only for loopback test fixtures.
`account_endpoint` is optional because the client account route is not
available for every CLI key or product family.

## Dynamic catalog

`ModelsForAuth` fetches the catalog for each credential. 厂商会按完整
User-Agent 返回不同场景；默认使用经目录接口核查的完整 WorkBuddy/CLI 标识，
不要求安装 WorkBuddy 或 CLI。已有显式 `user_agent` / `catalog_user_agent` 配置
继续优先，不会被升级覆盖。The parser prefers `cli` when
present and otherwise requires `craft`; it does not expose completion, rewrite,
image, or other task-specific entries merely because they appear in
`data.models`.

Model IDs are preserved exactly as returned. Display names are not executable
IDs, and no alias or fallback is guessed. Model metadata such as image,
reasoning, context, and output limits is advertised only when explicitly
reported by the selected catalog. 目录按凭证、endpoint、客户端标识和配置代次缓存
一分钟，并合并并发刷新。短暂网络、429、5xx 故障最多使用五分钟内的旧目录，
Summary 明确标记 `stale`；明确拒绝或空场景不会使用旧目录掩盖。

解析 `reasoning.supportedEfforts`、`defaultEffort`、`canDisableThinking`，
同时兼容旧 `reasoning.effort`。`contextWindow.defaultLength` 是默认窗口，
不会因为存在更大窗口而自动升档。相同显示名（如 `hy3` / `hy3-x`）保持不同 ID。
上游原始 `credits` 保存在 Summary 的 `catalog.hints[ID].credits_hint`，
它只是供应商提示，不是金额、每请求账单或已验证计费权限。

The selected exact ID is checked again by the executor against that
credential's last successful catalog before the Chat request is opened. A
catalog outage never substitutes the static `hy3` or
`hy3-preview-agent` list.

## Read-only account and quota Summary

The plugin registers:

```text
GET /v0/management/plugins/codebuddy/summary?auth_index=<AUTH_INDEX>
```

The route is protected by CPA Management authentication. It uses the existing
`host.auth.get`/`host.auth.get_runtime` callbacks, so the plugin instance must
grant `permissions.auth-read: true`.

The response contains the selected `auth_index`, file name, label, a short
credential fingerprint, account status, quota status, and a catalog snapshot. Quota packages retain
exact decimal strings and also expose convenient numeric values; real zero is
distinct from `unsupported`, `not_configured`, or `auth_rejected`。
空 `Accounts` 返回 `unsupported / no_personal_resources`，所有总额保持空，
绝不推断企业额度为零。`quota.scope=personal_resources` 明确查询范围；
`account.scope=tenant` 的 catalog ID 只是租户线索，不是已验证成员 UID。
保留原 `account.id` 兼容字段，同时补充 `enterprise_id` 和 `member_identity_unverified`。
`plan` 保持 `unknown / plan_not_verified`，不根据 `paymentType=free` 猜订阅。
The response
never contains a PAT, Job Token, Refresh Token, raw upstream body, or auth-file
path.

CodeBuddy billing uses the read-only `POST
/v2/billing/meter/get-user-resource` operation. The implementation accepts both
the current `data.Response.Data.Accounts` envelope and the historical direct
`Accounts` shape. 按 100 条/页查询有效个人资源，最多 10 页；分页不完整、重复或
超限标记 `partial`，不发布错误合计。周期 `CycleCapacity*` 优先于终身 `Capacity*`；
保留 `basis` 和周期边界。`TotalDosage` 不是已使用量，不用于凑合计。
Account lookup is best-effort; if the configured account route
rejects the PAT, Summary still identifies the selected auth file and reports
the safe fingerprint fallback.

## Supported behavior

- Input/output boundary: OpenAI Chat Completions.
- Dynamic per-auth model catalog with exact ID forwarding.
- Text, image, tools, and reasoning capabilities follow the selected vendor catalog and live capability checks.
- The complete client-supplied `messages` array is preserved, including system and prior-turn context.
- 支持 `stream=true` 和 `stream=false`；均使用一次上游 SSE，保留工具片段、reasoning 和成功响应的最终 usage。两种模式都要求各 choice 有 finish reason 及 `[DONE]`；非流式聚合上限为 4 MiB。
- SSE 按完整事件解析，支持分片、多行 `data:`、LF/CRLF/CR 和 UTF-8 BOM；单行上限 1 MiB、事件上限 4 MiB。
- `event:error`、业务错误码和 `finish_reason: error` 返回结构化失败。error finish 没有 usage 时继续读取尾帧；流式路径在关闭错误流前保留已取得的 usage。插件不重放生成请求。
- HTTP 401/403 直接关闭上游连接并返回鉴权失败。其余非 2xx 和非 SSE 响应最多读取 64 KiB 错误体，仅向 Core 返回错误类别与数值码。`6000–6008` / `14003` 为 429，额度或 Token 预算耗尽为 402，许可过期或未激活为 403，`11115` 为 400。
- 模型显式 `compat.maxTokensField` 优先；缺失时沿用 WorkBuddy 的旧 OpenAI 模型前缀规则选择 `max_completion_tokens`。未知模型保留客户端字段，显式同时提供两个上限时保持原请求。
- Upstream SSE must terminate with `data: [DONE]`.
- Downstream disconnect and explicit execution cancellation close the host-owned upstream stream.
- OpenAI Responses clients use CPA's existing Responses-to-Chat translation; the plugin itself remains a Chat provider.

CLI-native workspace sessions, skill discovery, MCP configuration, and native
Agent tool execution remain outside this direct plugin boundary.

## 协议参考与诊断

0.2.1 的主要协议依据是 WorkBuddy 5.8.2 随附 CLI（product commit
`b8f6c953610cb16e2bd275c003ded4bc315ec528`）的 ModelProvider、错误枚举和
MaxTokensFieldRule。API Key 推理同时携带 `X-API-Key` 与 Bearer，与该客户端一致。
客户端身份和 endpoint 继续由插件配置指定；模型名单仍按所选 Key 的 `cli` / `craft`
目录隔离。消息角色、提示词和模型 ID 按原请求保留。

readiness 检查本地配置和凭据格式；远端目录可通过刷新模型查询验证。真实推理、缓存
命中和企业扣费需分别验收。非流式执行若返回错误，现有错误 RPC 只能报告失败与账号
归属，usage 标记为 `unavailable`；流式错误之前已收到的 usage 仍由 Core 记录。

## 企业账号与后续 OAuth

PAT/API Key 模式没有网页 Cookie 会话，不能假定有企业余额查询权限。当前版本
**尚未实现原生 OAuth 登录和企业额度查询**；企业账号的 Key 不会因此被禁用、改写
为个人账号或自动切换到另一凭证。个人资源查询失败与推理权限分开处理。

下一步先验证现有 CLI Key 对企业成员额度接口的权限，并核对成员 UID、enterpriseId
和计费范围。确需登录 Token 时，再在同一个 `codebuddy` Provider 下增加显式 OAuth
模式。企业与个人模式需明确绑定，企业接口 401/403 不能自动降级到个人额度或切换
计费账号。
实现范围和验收条件见 [PAT Provider 指南](../../../docs/lts/pat-providers.md)。

本地动态库到 Core 的统计回归：在仓库根运行
`go test -count=1 ./test -run TestCodeBuddyDynamic`（需要 Unix + CGO；不与 race 动态加载混跑）。
