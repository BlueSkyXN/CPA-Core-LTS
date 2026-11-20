# 按客户端 API key 授权上游凭据和模型

在独立运行的 CPA-Core-LTS 实例中，可以用 `access.resource-allowlists` 将认证文件、单条上游 API key 或模型保留给指定的客户端 key。每个资源可以列出一把或多把客户端 key。

## 配置

1. 在 `access.api-keys` 中配置客户端 key，或沿用已有的 key。
2. 用 Management Key 读取 `GET /v0/management/flow-control`，从 `keys` 中取得对应 key 的 `ref`。界面标签 `API Key 1`、`API Key 2` 按 `access.api-keys` 的顺序编号。关闭流控时也能读取这些引用。
3. 将规则合并到现有配置。给需要授权的上游 API key 条目设置 `credential-id`，在 `upstream-api-keys` 下引用这个名称。把示例中的引用换成实际的 `ref`。

```yaml
api-keys:
  codex:
    - name: example-codex
      base-url: https://upstream.example/v1
      keys:
        - api-key: "replace-with-your-upstream-api-key"
          credential-id: codex-team

access:
  resource-allowlists:
    auth-files:
      team.json:
        - "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    upstream-api-keys:
      codex-team:
        - "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        - "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    models:
      gpt-5.5:
        - "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
        - "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
```

用现有的 YAML 配置流程保存，或通过 `/v8/management/config/access/resource-allowlists` 读写整段配置。v8 写接口使用同一次读取返回的 ETag，详见 [v8 Management API](../management-api-v8.md#revisions)。

## 规则如何生效

- `auth-files` 填 `oauth.auth-dir` 下的 JSON 文件名。一个文件生成多条账号记录时，所有记录共用该名单。文件改名时同步修改规则。其他 key 仍可用提供同一模型的其他授权账号。
- `upstream-api-keys` 填 `api-keys.<provider>[].keys[].credential-id`。ID 在实例内唯一，允许 1–64 个英文字母、数字、点、下划线或连字符，首字符为字母或数字。轮换上游密钥时保留 ID。若删除带规则的上游条目，在同一次配置更新中删除它的规则。Core 校验重复 ID 和无效引用后应用配置。
- 同一个上游密钥被写进多个 YAML 条目时，调用方必须满足它命中的每一份名单。轮换或撤销上游密钥时，一并处理仍在使用旧密钥的条目。
- `models` 填模型 ID，可填写对外模型名或上游实际模型 ID。保护上游实际模型时，其别名、前缀和附加 reasoning 后缀的请求也会检查。一个别名指向多个上游模型时，Core 从该 key 有权使用的目标中选择。
- 资源名按完整名称匹配，忽略大小写。未列出的资源共享，空数组 `[]` 拒绝所有客户端。一次调用命中多条规则时，所有名单都必须允许该 key。新发的客户端 key 自动受已有名单约束，轮换客户端 key 后应更新对应引用。
- 模型列表和详情只展示至少有一条授权的已注册路由的模型。直接请求受限模型返回 `403`（`resource_access_denied`），没有授权账号可用时返回正常的账号不可用错误。独立 Core 按下节规则解析 `auto`。

## auto 的选路顺序

1. 管理员显式将公开名 `auto` 绑定到模型时，只有发布该别名且调用方获准使用的账号可以接单。绑定账号不可用时返回该路由错误。
2. 没有显式别名时，`routing.auto-model.enabled` 缺省关闭，旧配置升级也关闭。此时按普通模型名匹配已发布的原生 `auto`。
3. 开启全局代选后，`routing.auto-model.candidates` 可填写有序公开名，例如 `[free]`。Core 选第一个有获准可用账号的候选，名单用尽即返回错误。未填或空列表时，在可用路由中按可信发布时间降序选择，无时间的模型排后，同分按公开名排序。

全局代选检查调用方、公开名、账号和实际上游模型。启用模型白名单的动态插件账号还须发布 `auto` 才能承接原始 `auto` 请求。配置式 API Key 渠道按候选公开名匹配。重试限于本次选中的公开模型及其授权账号。Home 模式由控制面选路。

## 运行范围

- 标准 HTTP、SSE 和 Responses WebSocket 逐次检查授权。重试和故障切换重新筛选候选目标，派发前复核所选账号与实际模型。启用规则时，Responses WebSocket 使用逐回合选路。配置变更前已建立的 Codex duplex 流若拒绝后续回合，重连后使用逐回合选路。
- Codex Live / Realtime、Muse Code 的原样转发和直接插件执行只向已获全部保留资源权限的调用方开放。Amp 转发到外部服务时要求调用方具备全部保留模型的权限。已经建立的 Live / Realtime 会话持续到关闭，收紧权限时应结束相关会话。
- Home 模式的账号选择由控制面负责，本地资源规则只用于独立 Core。日常配置通过 YAML 或现有 v8 配置 API 管理。需要同时更新上游条目和名单时，使用一次整份配置写入。

保存后用两个不同的客户端 key 查询 `/v1/models` 并请求同一模型。确认未授权 key 看不到受限模型，仍能使用公共模型。检查请求事件中的上游账号，确认重试只在授权账号内进行。
