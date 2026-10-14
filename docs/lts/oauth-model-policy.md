# OAuth Model Policy（渠道/账号黑白名单，LTS）

`oauth-model-policy` 是 Core 的通用模型访问策略，作用于所有 OAuth／认证文件账号（原生 provider 与动态插件 provider，包括 copilot、qoder、codebuddy）。配置式 `*-api-key` 条目不在范围内，它们继续使用自己的静态 `models` 与 `excluded-models`。

## 组合契约

- 只有两层：**渠道**（`config.yaml` 的 `oauth-model-policy`）和**账号**（认证 JSON 的 `model_policy`）。
- 白名单逐层取交集：模型必须通过每一层已启用的白名单。
- 黑名单取并集：任一层命中即拒绝，同层黑白同时命中时黑名单胜出。
- 规则按账号分别计算；合并展示只用于 UI 汇总，不共享授权。
- 匹配对象是上游真实模型 ID，发生在别名与前缀处理之前；匹配忽略大小写、支持 `*` 通配符。
- `enabled: false` 表示本名单不参与计算但内容保留；未配置等价于不限制。
- **白名单启用且名单为空 = 该账号零模型**；这不是配置错误。
- 名单只能收窄账号原有目录，不能创造上游不存在的模型。

## 渠道级配置

```yaml
oauth-model-policy:
  copilot:
    allowlist:
      enabled: true
      models: ["gpt-*", "claude-sonnet-*"]
    denylist:
      enabled: true
      models: ["*-preview"]
```

渠道键是 provider 名（`copilot`、`qoder`、`codebuddy`、`codex`、`claude`…），不是插件库名。

## 账号级配置

认证 JSON 顶层新增可选对象：

```json
{
  "type": "copilot",
  "auth_mode": "oauth",
  "github_token": "...",
  "model_policy": {
    "allowlist": { "enabled": true, "models": ["gpt-5-codex"] },
    "denylist": { "enabled": false, "models": ["*-preview"] }
  }
}
```

结构非法（非对象、`enabled` 非布尔、`models` 含非字符串）时整个认证文件加载失败并给出明确错误，不会静默当作未限制。管理面 `PATCH /v0/management/auth-files/fields` 支持嵌套路径（如 `model_policy.allowlist.enabled`），写入前做同样校验并同步运行时属性、热重载模型注册。

渠道和账号名单均限制为最多 512 项，拒绝控制字符；无效渠道限制会使配置加载失败，保留运行中的旧配置。运行时账号策略使用 JSON 保存，模型文本中的逗号和分号不会被解释为其他策略。

## 与 legacy 黑名单的关系

- `oauth-excluded-models`（渠道）与认证文件 `excluded_models`（账号）视为**始终生效、没有开关的黑名单来源**，与新的 denylist 取并集，不做冲突报错。
- 旧 Management API（`/v0/management/oauth-excluded-models`）继续只操作旧键，行为不变。
- 账号停用继续使用 `disabled`；不要用黑名单 `*` 表达停用（config API-key 的旧惯例不带入本结构）。
- Copilot 插件自身的 `excluded_model_prefixes` 在插件返回目录时即生效（区分大小写、前缀语义），发生在 Core 策略之前，保持原样、不做机械迁移。

## 生效路径

1. **注册**：`registerResolvedModelsForAuth` 在别名/前缀投影前对真实模型 ID 统一评估（`sdk/cliproxy/service_executors.go`）。
2. **选择**：本地候选枚举（`pickNextLegacy`）跳过被禁账号；该检查独立于 registry 成员资格，因此 Codex Live 这类未入目录端点同样受限。
3. **派发**：`admitFlowExecution` 在发请求前用同一评估函数做最后复核，覆盖普通执行、Home dispatch、Codex Live uncatalogued 路由与 Antigravity credits 回退。策略拒绝是本地拒绝：不请求上游、不进入凭据冷却、Home 已占用的并发额度立即释放。
4. **热重载**：`oauth-model-policy` 变化经 `internal/watcher/diff/oauth_model_policy.go` 识别受影响渠道并重建其账号注册；认证文件 `model_policy` 变化经既有 auth 文件热重载生效。

## 过滤前目录快照与预览

Core 在注册时按账号保存一份过滤前候选目录（`sdk/cliproxy/model_policy_store_lts.go`）。它支撑：

- `GET /v0/management/auth-files/models/policy-preview?name=<auth>`：只读评估当前策略下每个候选模型的允许/拒绝与原因（channel/account/legacy 层标注），不联网、不改注册状态。无快照时返回 `catalog_unknown`。
- 预览只读取快照，不触发上游目录发现。策略热重载沿用现有账号注册流程，插件的 `ModelsForAuth` 可能刷新目录；此版本没有新增跳过插件注册的目录缓存。

被禁模型仍保留在快照中，预览界面可以看到"当前禁止、原因、放开后生效"，不会出现"禁掉就看不到、无法再勾选"的问题。

## Management API 与 Panel

Core 提供两组配套面：

- 渠道策略 CRUD：`/v0/management/oauth-model-policy`（GET/PUT/PATCH/DELETE）。PATCH 按 provider 单条写入，空 policy 删除该条；非法结构返回 400。旧 `/oauth-excluded-models` 端点继续只操作旧键。
- 账号策略预览：`GET /v0/management/auth-files/models/policy-preview?name=<auth>`（见上文）。
- 账号策略编辑：`PATCH /v0/management/auth-files/fields` 的 `model_policy` 嵌套字段。

Panel（`CPA-Panel-LTS`）提供：

- Auth Files 页"渠道模型策略"卡片与 `/auth-files/model-policy` 编辑页：渠道黑白名单开关与名单编辑、legacy 排除规则展示与一键迁移（并入 denylist 并删除旧键，删除失败会提示且不影响已写入的 denylist）。
- 账号编辑器（前缀/代理弹窗）内的"模型策略（本账号）"区块：账号级黑白名单，保存走同一 PATCH 通道并即时热重载。
- 旧 Core（无此端点）时显示升级提示，不影响其余页面。

## 已知边界

- Home 控制面返回的账号清单由控制面下发；Core 对 Home dispatch 的执行复核使用 Home 返回的实际上游模型与账号属性，不依赖控制面是否过滤其模型列表。
- 快照"未知"（如进程刚启动且注册未完成时）不能被解释为"目录为空"。
