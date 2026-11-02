# Responses 客户端与多代理委派兼容

CPA 根据入口协议和实际选中的 provider 转换请求。Codex、ZCode 等客户端可以使用同一个 Responses 入口；Claude Code 的 Messages 入口继续使用对应的协议转换。客户端名称和出站 provider 是两个独立维度。

[OpenAI 公开 Responses 协议](https://developers.openai.com/api/reference/resources/responses/methods/create)包含 `tool_search`、`tool_search_call`、`tool_search_output` 和 `additional_tools`。工具搜索是否可用还取决于目标 provider 的声明、请求转换、返回事件和历史回放支持。Codex 的 `agent_message` 委派消息需要额外适配。

| 路径 | 委派消息处理 |
|---|---|
| 原生 Codex | 保留原始 `agent_message` 和加密内容，由原生路径处理。 |
| Codex → xAI/Grok、Kimi Responses | 开启下述客户端适配后，将完整明文任务转换为 `user/input_text`。仍未转换的消息在本地返回 HTTP 400。 |
| Codex → 配置了 `is-compat` 的 Codex API-key 模型 | 沿用本次已选模型的兼容设置，将符合条件的明文任务转换成普通用户消息；无法表示的委派消息返回 HTTP 400。 |
| Responses → Claude、Gemini、Chat Completions | 保留已转换任务的用户轮次。目标转换仍遇到 `agent_message` 时返回 HTTP 400，避免遗漏任务后生成成功响应。 |
| ZCode 等客户端的普通 Responses 请求 | 继续按目标协议处理消息和工具；不因工具名字类似协作工具而修改其参数加密信息。 |
| Claude Code 的 Messages 请求 | 沿用 Messages 转换路径；本功能不引入新的 Codex 委派消息。 |

Codex 多代理适配使用现有配置，默认值保持关闭：

```yaml
client:
  codex:
    optimize-multi-agent-v2: true
```

开启后，从父会话发起新的委派。适配会对已确认的协作工具声明请求明文参数，并在返回客户端时补齐本次调用的明文参数标记。HTTP、SSE 使用各自请求的证据；WebSocket 沿用未被替换的工具声明来源，明确替换声明时重新校验。已有加密标记、调用 ID、参数正文和无关工具保持原样。

如果返回 `agent_message cannot be sent to this provider`：

1. 确认父会话和子会话请求经过启用了上述配置的 CPA 实例。
2. 新建委派，让父会话重新发送明文任务。旧历史中的加密任务不会被此开关解密。
3. 需要继续使用原有加密历史时，保留其原生 Codex 路径；也可以由调用者重新明确下发任务。

这个错误属于当前请求，不能通过切换同一 provider 的账号解决。CPA 在出站前拒绝无法传达的委派，避免让上游产生无法理解任务的回复，或把本地转换问题当成凭据故障。

只在每个内容块都是非空 `input_text` 时才将委派视为可转换的明文。包含 `encrypted_content`、混合内容或无法确认的内容时，不把其中的字符串猜作任务正文。原生 Codex 的加密历史继续保留。

工具搜索与此开关分别控制不同能力。Claude 的工具搜索使用 `upstream.claude.tool-search` 和实际凭据能力；自定义 Claude 网关还需符合 `native-tool-search` 条件。Grok、Kimi 的能力声明继续沿用各自已有边界，开启多代理适配不会给它们声明完整的原生工具搜索支持。

已有的并行工具轮次分组、Desktop 工具补入、订阅插件转换和网页结果回放继续独立生效。Desktop 工具补入仍只作用于满足条件的主会话；它不负责恢复子代理的任务正文。声明 Claude 或 Chat Completions 输入的订阅插件使用共享请求转换与错误传播规则。声明直接接收 Responses 的插件保留自己的透传协议，宿主不根据插件名称推断它是否支持 Codex 扩展。

ZCode Coding Plan 插件声明 Claude 输入，因此 Codex 或 ZCode 的 Responses 请求先经过共享转换，再进入订阅插件。xAI/Grok 的订阅路径由内建 xAI executor 处理，在共用的 Responses 请求准备阶段完成委派检查。适配依据实际协议路径生效，订阅鉴权和客户端协议分别保留各自的处理方式。
