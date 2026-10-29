# 按模型配置推理强度

从 `v1-lts-0.1.1` 开始，Core 在 V8 配置的 `requests.reasoning-effort` 中提供统一强度策略。Messages、Chat Completions 和 Responses 客户端可共用一条规则，Core 根据实际执行协议转换字段；Coding Plan 插件也经过同一策略入口。

## 强制 GLM 使用 max

```yaml
config-version: 8
requests:
  reasoning-effort:
    - models: ["glm-5.3", "glm-5.3-flash", "glm-5.3-flashx"]
      mode: force
      effort: max
```

`models` 匹配客户端请求的模型名，支持 `*` 通配符，匹配时忽略推理后缀。使用模型别名时填写客户端看到的别名。规则按顺序处理，**最后匹配的规则生效**。例如可先为 `glm-*` 强制 max，再用一个具体模型的 follow 规则保留客户端选择。

## 三种模式

| 模式 | 客户端已有强度 | 客户端未指定强度 | `effort` 配置 |
| --- | --- | --- | --- |
| `follow` | 沿用客户端与原有模型后缀规则 | 沿用模型默认行为 | 不填写 |
| `default` | 保留客户端选择 | 使用配置值 | 必填 |
| `force` | 使用配置值，包括覆盖模型后缀 | 使用配置值 | 必填 |

显式禁用 thinking、auto、预算值、模型后缀和动态控制项都算客户端已经指定强度。单独的 `thinking.type: adaptive` 没有指定等级，default 可补充等级。

可配置的等级为 `minimal`、`low`、`medium`、`high`、`xhigh`、`max`、`ultra`。每个模型支持的集合不同：已知模型声明了等级列表时，不支持的配置会报错，需改用模型支持的等级。预算型模型复用既有 level-to-budget 映射及模型预算限制；usage 记录实际发送预算对应的等级。未知或自定义模型缺少能力信息时，由上游验证其支持情况。Codex 的 ultra 继续遵循既有官方 wire 映射。

## 与 Payload 规则的优先级

没有匹配的专用策略时，现有 Payload 行为保持原样。匹配 follow、default 或 force 后，专用策略接管强度字段，旧 Payload 的 default、override、raw、filter 都不能再次改写这些字段。

迁移强制规则时，添加上面的配置，并删除旧规则中仅用于强度的 `reasoning.effort`、`reasoning_effort`、`output_config.effort` 等写入或删除项。保留同一规则中的其他参数。Core 不会自动重写已有配置。

客户端协议与执行协议可能不同，例如 Coding Plan 接收 Claude Messages，再由插件转成 GLM Chat。Core 先使用该阶段的原生强度字段，插件完成后续转换，避免同时添加互相冲突的 effort 别名。

推理摘要显示、结构化输出格式和输出 token 上限分别由原有参数控制。强度策略仅保护强度字段，其他 Payload 参数仍然生效。

## 动态控制与传输

支持动态控制的 Responses/Claude 历史保持原有顺序，force 在末尾更新或追加当前轮控制。Payload 可以改写普通消息内容，但不能改写动态强度字段；如果它整段替换或重排含动态控制的数组，Core 保留策略处理后的原数组，防止把控制项套到错误的历史消息上。

普通 WebSocket steering 沿用当前响应设置，不新增 response.create 字段；显式携带强度控制的 steering 按 force 处理。SIWC 继续使用原有兼容处理，将不支持的配置更新转换到顶层强度，最终 Payload 保护不会重新注入这些更新。

原生 OpenAI Chat、Responses/Codex、Claude、Gemini/Interactions、Antigravity、Kimi 和 xAI 复用各自的 thinking applier。采用这些标准输入协议的插件可接入同一入口。Devin、图像生成和没有标准强度适配的自定义协议无法应用 default/force，匹配后返回不支持错误；请用精确模型范围或 follow 例外排除这些路由。

## Panel、热更新与统计

配套 Panel 在配置编辑器的 Payload 分组中提供“推理强度”，支持添加、排序和删除规则。Core 在 Management 响应中提供 `X-CPA-REASONING-EFFORT-POLICY: 1`；缺少此能力的 Core 上，Panel 将该表单设为只读。

策略随现有配置保存和 watcher 热更新流程生效。非法模式、空模型列表或无效等级会被拒绝；热更新失败时保留上一份有效配置。规则列表保存沿用配置冲突检查，保留未知 YAML 字段和注释。

usage 继续使用现有 `reasoning_effort` 字段记录最终执行值，不增加统计字段，也不改变 token、auth index、模型归属或导入导出结构。
