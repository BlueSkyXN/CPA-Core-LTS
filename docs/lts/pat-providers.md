# Qoder / CodeBuddy / Copilot 原生插件

三家保持原有 Provider 身份 `qoder`、`codebuddy`、`copilot`，通过 CPA 的账号选择、模型注册、协议转换和 usage 链路执行。CodeBuddy 已经是原生 Go HTTP Provider；Qoder 从插件 0.3.0 起只支持 PAT 认证和原生 Go HTTP，不再包含 runner、Node、Qoder CLI/SDK 或 `sdk_cli` 兼容路径。中国区默认使用 COSY 签名推理；显式配置的 OpenAI 兼容 `direct_endpoint` 保留原有 Bearer 请求格式。Copilot（0.1.0）支持 GitHub OAuth device 登录与自备 `github_token` 两种认证，同为纯 Go 原生动态库。

## 配置与兼容

使用 `examples/plugin/pat-providers.config.yaml` 作为新部署示例，合并所需插件字段到现有配置，不覆盖已有 API key、auth、流控或 usage 配置。`Dockerfile.pat-providers` 的默认运行镜像只包含 Core 和三家原生动态库，不包含 Node、厂商 CLI、Qoder SDK 或 runner。原生插件与 Core 由同一份源码构建。

Qoder 插件 0.3.1 的中国区原生默认值：`transport` 固定为 `direct_openai` 兼容名称，`direct_endpoint` 默认指向 `/algo/api/v2/service/pro/sse/agent_chat_generation`，`direct_models_endpoint` / `openapi_endpoint` / `direct_catalog_format=qoder` 也有默认值，最小启用配置只需 `enabled` 与 `auth-read` 权限。auth 或配置中的 `transport` 仅作为兼容字段：`direct_openai` 等价无操作，`sdk_cli` 明确拒绝。认证只接受 `pat`（legacy `access_token` 继续作为旧 PAT 文件的读取别名）；`local_cli` 已移除并明确报错。国际区或私有网关实例需显式覆盖并验证相应 endpoint，显式覆盖永远优先于默认值。已有显式 `/model/v1/chat/completions` 覆盖仍按 OpenAI wire 发送；若要使用已验证的中国区 COSY 推理，需移除该覆盖或改为上述 COSY endpoint。

Qoder 目录有两种使用方式：

- 精确手动名单：继续配置 `direct_models`，它优先于动态 endpoint，不自动增加模型。
- 自动发现：移除手动名单，设置 `direct_models_endpoint` 和 `direct_catalog_format`。`openai` 保持 Bearer + OpenAI 数组解析；`qoder` 使用 COSY 获取启用的 `chat` 模型。CN 示例使用 `https://gateway.qoder.com.cn/algo/api/v2/model/list`，签名内部添加 `Encode=1`。

地区不能从 PAT 前缀判断。OpenAPI、catalog 和 inference 必须来自对应部署的已知地区配置。COSY 目录报告可用模型不等于所有模型已通过推理验收；新增模型保留上游 key 和显示名，真实调用由管理员按需验证。中国区默认 COSY 推理已用一份真实 PAT 验证 `qfmodel` 文本 Chat 的流式和非流式响应；其他模型与图片、工具仍需单独验收。当前 COSY 路径对图片和客户端工具明确返回 `unsupported_input`，目录只向客户端声明文本输入。

显示名使用上游名称，缺失时使用内置映射，再缺失使用原始 ID。显示名不改变路由 ID；自定义可执行别名沿用 CPA 原有 alias 配置。目录按凭证、端点和配置代次隔离；并发获取合并；失败不伪造静态成功目录。`enable:false` 模型不发布，也不自动提升上下文计费档位。

Management 的 `GET /v0/management/auth-files/models?name=...` 只读取当前 registry。若凭证暂无已注册模型，Panel 会调用 `POST /v0/management/auth-files/models/refresh?name=...` 重新发现单个凭证的目录；Core 沿原注册路径更新 registry 和 scheduler。重新发现可能触发 PAT 换票和目录 HTTP 请求，但不执行推理。成功返回 `status=ready|empty` 和 `models`；凭证不存在返回 404、已禁用返回 409、目录失败返回 502 与安全错误码、超时返回 504，不回传插件原始错误或上游响应体。旧 Core 没有此 POST 时，Panel 仍按原有空列表处理。

CodeBuddy 0.2.0 按 `/v3/config` 的 `cli` / `craft` 场景获取精确模型名单。默认客户端标识是 `WorkBuddy/5.3.14 WorkBuddy/5.3.14 CLI/2.115.0`；此标识只用于 HTTP 请求，不安装 CLI。已有显式配置优先，升级时若仍配置了旧 `catalog_user_agent`，需要管理员主动更新或移除该覆盖项。

新的模型元数据包含可选 reasoning levels、能否关闭 thinking、默认 context tier 和原始 credits 提示；不拼接其他场景名单，不把显示名当可执行 ID，不根据 `credits` 估算真实扣费。Summary 的 `catalog` 包含来源、场景、数量、获取时间、过期状态、模型能力和提示。Core `ModelInfo` 与 ABI 不变。

CodeBuddy 同时支持流式和非流式。普通 JSON 响应由同一次上游 SSE 聚合，保留文字、reasoning、工具参数分片和最终 usage；要求 `[DONE]`，非流式还要求 finish reason，聚合上限 4 MiB。不新增厂商 Agent 或本机工具执行能力。

## 认证、生命周期与统计

Qoder 推理、目录和 Summary 共用内存中的 PAT 换票和刷新缓存。到期时间优先 `expires_at`，Qoder OpenAPI 的 `expires_in` 按毫秒处理。换票与刷新并发合并，更新 PAT 或重载配置不会复用旧缓存。Core HTTP 日志对 job/device token 交换端点的双向 body 脱敏，线上发送的字节不变。

Qoder refresh 仅在明确认证失效时退回 PAT exchange；网络、限流、上游异常和无效响应不触发额外换票。刷新响应没有新 refresh token 时保留原票据。动态目录的显式 `default_context_window` / `defaultContextWindow` 优先于可选最大窗口。

中国区默认 COSY 推理映射文本 messages、模型、常用生成参数和会话标识，并对实际发送的编码体签名；不发送第三方模板的系统提示词。显式非 COSY OpenAI 兼容 endpoint 保留原始 messages、工具、图片、生成参数和会话标识；工具由客户端执行。HTTP 401 和明确 token 失效的 403 最多在输出前刷新重试一次；排队、额度和模型拒绝分别分类。流中断不在插件内自动重放生成请求。取消与关闭遵循账号、调用方和工作区身份；关闭上游后释放会话占用。

SSE 必须读到 `[DONE]`；finish 后的 usage 仍会被读取。`usage` / `raw_usage` 归一并保留 cache-read、cache-creation 和 reasoning 分项，不重复累加到总量。上游流失败时，已收到的 usage 在下游仍连接时通过 usage-only 帧交给 Core，随后报告失败，不伪造成功终结。统计由 Core 正式 Provider 路径发布，插件不另建账本。额度查询与模型请求统计互相独立。

## Copilot（GitHub Copilot provider）

Copilot 插件（`cpa-provider-copilot`，0.1.0）以 Provider 身份 `copilot` 接入，纯 Go 原生动态库，无 Node、GitHub CLI 或 runner。认证两种模式：`oauth` 走 GitHub OAuth device flow（发起后经插件 Management 路由 `GET /v0/management/plugins/copilot/login-info` 查询 user_code 与状态，`device_code` 不外发到日志或响应）；`github_token` 直接使用用户自备的 GitHub PAT。两种模式的 auth 文件 `type` 均为 `copilot`。

凭据安全：`POST /login/oauth/access_token`（长期 `ghu_` token）、`POST /login/device/code`（device_code）与 `GET /copilot_internal/v2/token`（短时 Copilot JWT）三个凭据交换端点在 Core HTTP 请求日志中做双向 body 脱敏，路径后缀匹配以兼容 GHES；`/copilot_internal/user` 与 chat/inference 端点不脱敏。该清单目前由宿主手工维护，后续方向是插件注册时自声明敏感端点。

端点覆盖：`github_api_endpoint` 显式覆盖 GitHub API base（token 交换、user、quota）；GHES 用 `enterprise_domain` 同时改写 `github.com`、`api.github.com` 与 Copilot API base。默认面向 github.com 云端。

模型目录按账号发现（`ExecutorModelScopeOAuth`）；显示名、路由 ID、目录隔离语义与 Qoder/CodeBuddy 一致。执行协商格式为 `chat-completions` 与 `embeddings`：客户端四协议入口 `/v1/chat/completions`、`/v1/messages`、`/v1/responses`、`/v1beta generateContent` 经 Core 标准转换层进入 chat-completions 格式，`/v1/embeddings` 为 Core 公共入口直达插件。生命周期支持取消、readiness 与会话关闭（schema 与 capability 双门控）。usage 经 Core 正式 Provider 统计管道发布，插件不另建账本；配额 summary 查询 `copilot_internal/user`，不把配额快照当计费事实。

## 插件 ABI 契约与 LTS schema 语义（第三方插件作者须知）

`sdk/pluginabi` 与 `sdk/pluginapi` 是 LTS 的稳定公开契约：破坏性变更必须升级 SchemaVersion/ABIVersion，并保留旧行为分支。宿主接受 schema 低于当前的插件，缺失协商字段按 schema 1 处理。

schema 语义在 LTS 与 upstream 存在一处分叉，第三方作者必须区分：

- **LTS schema 5 = 执行生命周期**：`executor.cancel` / `executor.close_session` / `executor.readiness` 三个方法仅在插件 schema ≥ 5 且 capability 显式声明时才会被调用（双门控）。upstream 插件不会被误调。
- **upstream schema 5 = 流 chunk history 省略**：LTS 不采用该隐式语义，以显式 `StreamChunkHistoryOmitted` capability 表达；声明后才生效。
- **schema 6 双边语义相同**（raw management response）。

平台成本结论（实测）：接入一个标准 OAuth/PAT 型 provider 是纯插件工作，零 Core 改动；接入新推理形态（非 OpenAI 兼容上游）需要协商层注册格式名加 Core 入口 handler，约 40 行级。

## 发布前验证

1. 分别在 `examples/plugin/qoder/go`、`examples/plugin/copilot/go` 和 `examples/plugin/codebuddy/go` 运行 `go test -race -count=1 ./...`、`go vet ./...`。
2. Qoder 与 Copilot 的非 race 测试包含动态库构建与真实 Core host 加载，使用本地假上游检查目录、流式/非流式、登录状态、usage 归属；不依赖真实凭证。
   CodeBuddy 在仓库根执行 `go test -count=1 ./test -run TestCodeBuddyDynamic`，覆盖真实动态库注册、目录、流式/非流式、截断流失败及正式 usage 归属（Unix + CGO，非 race）。
3. 仓库根执行 `python3 -m unittest discover -s scripts -p test_package_pat_providers.py`、`scripts/check-lts-contract.sh` 和 Core 测试。
4. 具备 Docker 环境时构建 PAT 镜像并运行 `scripts/smoke-pat-providers.py --image <image>`；检查镜像中没有 Node/CLI/runner，验证插件注册及 auth 重建持久化。
5. 在授权环境单独验收真实账号的 catalog、token 刷新、文本/图片/tools、usage、断连取消，以及所需的 Home 路径。模型请求可能消耗额度，不能用本地 fixture 代替真实验收。

本地构建、GitHub CI、正式发行包、部署版本和真实上游验收分别报告。`local_cli` / `sdk_cli` / runner 已随 Qoder 0.3.0 移除，存量部署升级前必须确认没有依赖这些路径的账号或配置；旧 `access_token` PAT 文件继续可读。插件路线稳定后再评估是否内置 Core，不改变这次的 Provider/auth/model 身份。

## 外部参考

COSY 签名参考 Sliverkiss/cpa-plugin 的 MIT QoderWork 实现，归属和许可证见 `examples/plugin/qoder/THIRD_PARTY_NOTICES.md`，并随 Qoder 插件归档和镜像分发。推理请求字段另与 [qoder2api-hub 的公开实现](https://github.com/shuishuipingan/qoder2api-hub/tree/81eee37f0b4f3d7a6382c3ff402937cba565bfd1) 对照，并通过当前 Qoder CN PAT 独立实测；没有引入第三方网关、其 baseprompt 模板或新厂商 SDK。

## CodeBuddy 企业 / 个人账号边界与下一阶段

当前 Key 模式可用于目录和推理，但企业额度查询需要另行验证可用的认证方式。不能将
`get-payment-type` 返回的 `free` 直接解释成“个人免费账号”，也不能把个人资源
`Accounts:null/[]` 解释成企业余额为零。当前 Summary 明确输出：

- `account.scope=tenant`：catalog 提供的 enterpriseId，仅为租户线索，成员身份未验证。
- `plan.status=unknown`：尚无可证明的订阅计划。
- `quota.scope=personal_resources`：本次查询的是个人资源包；空包为 `unsupported / no_personal_resources`，不是数值零。
- 个人资源按有效状态/日期、分页读取；优先精确周期计数，缺字段、分页异常或混合单位返回 `partial`；不拿 `TotalDosage` 当消费量。

已有 PAT/API Key auth 文件继续有效；不改默认账号、不自动跨企业/个人计费回退。
本版本未实现 OAuth 登录或企业额度拉取，不能宣称已打通企业付费闭环。

### 建议的最小后续实现

1. 同一 `codebuddy` Provider 下新增显式 OAuth 凭据通道，复用 Core `AuthProvider` 的 Start/Poll/Refresh、auth storage 和 host HTTP。参考 WorkBuddy 原生 HTTP 登录，不依赖厂商 CLI/SDK或浏览器 Cookie 导出。
2. 把 region、credential kind、成员 UID、enterpriseId、选定 billing scope 分开保存。OAuth 登录结果必须与用户选择的企业/个人身份核对；不能只比较 enterpriseId 就认为是同一成员。
3. 企业模式使用 `POST /billing/meter/get-enterprise-user-usage`，按实际 OAuth 接口契约携带身份，读取成员 `credit`（已用）、`limitNum`（上限）和周期。保持小数，不四舍五入成整数，不称为公司总池。个人模式继续 `get-user-resource`。
4. 企业 401/403 显示 `needs_login` / `forbidden`；只有厂商明确表示不支持且用户配置允许，才考虑回退查询。**查询回退不能变成推理的计费账号回退**。余额查询失败也不能直接禁用可用推理凭据。
5. 先做状态机、刷新持久化、并发和身份绑定 fixture，再由用户完成一次登录 UAT。付费闭环另需授权：固定一个非零费率模型、一次请求、无重试/回退，核对企业明细的唯一标记与实际扣费。免费模型成功仅证明调用和明细归属，不证明付费企业扣减。个人真实账号也需独立验收。

### 对比来源与取舍

- [Sliverkiss WorkBuddy](https://github.com/Sliverkiss/cpa-plugin/tree/3a039f9ddf9a7cc248f231fc5964a9f6fab3805b/workbuddy)：原生 Go OAuth、企业成员额度查询、精确 scene 是有用路线；其 enterprise credits 默认关闭，不能把源码支持当成默认已启用。不要照搬金额取整、自动签到、试用领取、auth 删除或独立账本。
- [9router CodeBuddy CN](https://github.com/decolua/9router/blob/a8c9d3802c5933500fba95416f5bf0c130581396/open-sse/services/usage/codebuddy-cn.js)：个人资源周期/赠送包区分和 SSE 聚合可参考；静态模型名单不是当前账号权限，不能合并进动态目录。
- [codebuddy2api 历史实现](https://github.com/nopperabbo/codebuddy2api/tree/18b9ba3483d7516a2144f92c0deba83908faf9d5)：该快照主线已转向 Kiro，CodeBuddy 在 legacy；旧国际版 quota API 和本地估算器不是国内企业余额证明。不跨地区发送 Key，不关闭 TLS 验证，不使用固定用户 ID 兜底。

以上第三方仅作公开源码只读对比，没有安装或执行其程序。CPA 保留自己的账号选择、usage、日志和管理契约。
