# 按凭据和接口匹配 payload 规则

`requests.payload` 的模型匹配项支持两个可选条件：

| 字段 | 取值与行为 |
| --- | --- |
| `auth-kind` | `apikey` 或 `oauth`，匹配本次执行器实际使用的上游凭据类型。省略或空字符串时不限制。 |
| `request-paths` | 非空的精确业务接口路径列表，命中任意一项即可。省略时不限制。 |

同一模型项中的模型、协议、凭据、路径、请求头和请求体条件需要全部满足；同一 `models` 列表中的各项是“或”的关系。条件适用于 `default`、`default-raw`、`override`、`override-raw` 和 `filter`，沿用现有执行顺序：默认值在原始转换后的请求缺少字段时补充，override 后匹配者覆盖前者，filter 最后删除字段。后面的规则仍可匹配前面规则修改后的请求体。

## 配置示例

以下示例仅对明确列出的模型、API key 凭据和接口设置 Flex。请把占位模型替换成该上游已经确认支持 Flex 的模型 ID。

```yaml
requests:
  payload:
    override:
      - models:
          - name: "<支持 Flex 的上游模型 ID>"
            scope: upstream
            protocol: codex
            auth-kind: apikey
            request-paths:
              - /v1/responses
              - /backend-api/codex/responses
              - /v1/chat/completions
        params:
          service_tier: flex
```

选中订阅凭据时，这条规则不匹配，原有 Fast／Priority 处理继续生效。重试更换凭据或模型时会重新匹配。客户端请求头、请求体中的 `auth-kind` 和访问 CPA 使用的客户端 key 都不参与上游凭据类型判断。

如果配置中原本还有无凭据条件的 Flex 规则，应将其修改为所需范围；新增一条更窄的规则不会禁用其他规则。相同上游模型名可能存在于多个渠道，模型名单应按这些渠道的实际支持情况维护。

## 路径与图片请求

路径取自 CPA 当前执行上下文记录的业务接口，可以来自 HTTP 路由或 SDK 指定路径。已有插件路径改写仍受 Core 的操作边界校验约束。它不是最终远端 URL：图片入口内部转接到 `/responses`，仍按图片业务路径匹配。

路径精确匹配，区分大小写，不支持通配符、查询串、fragment、URL 转义或相对路径。`/v1/responses` 与 `/backend-api/codex/responses` 需要分别列出；HTTP 和 WebSocket 使用相同路径时采用相同条件。compact、图片和音频等未列出的接口不匹配。

接口条件只限定规则范围，不探测上游能力。直接 Images API 和 Responses 中的图片生成工具是不同场景；模型和工具组合是否支持 Flex，以及图片工具的计费方式，需另行确认。普通请求也可能由执行器自动补充图片工具声明，因此不宜单凭工具声明的存在判断其是否属于图片专用请求。

## 校验和保存

`auth-kind` 接受上述小写值、空字符串或省略；其他值报配置错误。显式空的 `request-paths: []`、空路径和无效路径会报配置错误。已配置条件而运行时对应凭据类型或路径未知时，该模型项不匹配。未配置新条件的旧规则维持原有行为。

v8 YAML 的规范位置为 `requests.payload`，旧布局的顶层 `payload` 仍按已有兼容规则读取。配置通过现有启动加载、Management YAML 保存和热重载路径验证；新的条件无需新增 Management endpoint。

Panel 的 YAML 编辑入口可以保存这些字段。使用尚未提供对应可视化控件的 Panel 时，条件不会在表单中显示；保存后应通过 YAML 回读确认配置。旧 Core 不支持这两个条件，不能把配置发送给旧版本并依赖它们进行隔离。

本功能只选择 payload 规则的适用范围，不自动改写其他层级、不自动降级重试，也不扩展 Flex 的统计分类或价格表。usage 的原始请求、实际出站和响应层级继续由既有链路记录。
