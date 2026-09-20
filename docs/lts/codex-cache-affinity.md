# ChatGPT OAuth 缓存适配

此功能修复第三方客户端提供 `X-Session-Id`、但 Codex 出站层未使用该身份而生成随机 `Session-Id` 的缺口，并为第三方请求增加独立的内容前缀检查。

只对 **Codex 普通生成路径且 `Auth.AuthKind() == oauth`** 启用。判断依据是实际凭据类型，不是 `apiKey` 变量名、入口协议或目标域名；OAuth 使用自定义反向代理 `base_url` 仍适用。Responses、Chat Completions、Claude、Gemini、Interactions、原生 Codex 都通过已有翻译器进入该层。HTTP/SSE 与 WebSocket 使用相同决策。

API key、未知凭据、其他 provider、CountTokens、专用 compact 和图片接口保留原行为。Responses 内的普通生成工具不等于图片专用接口。

## 配置与升级

```yaml
codex:
  cache-affinity:
    strategy: client-aware
```

| 值 | 行为 |
| --- | --- |
| `legacy` | 保留修订前的 HTTP/WS helper 及其各自优先级 |
| `stable-id` | 补齐 OAuth 出站身份、保留官方缓存路由语义；不推断前缀缓存组 |
| `client-aware` | 默认值；包含稳定身份修复、客户端判断和严格前缀匹配 |

缺失或空值在内存中解析成 `client-aware`；加载旧配置不会写回此默认值。无关配置保存也不会插入此前不存在的默认项。非法值导致加载失败，文件热更新保留上一份有效配置。现有配置 JSON/YAML 序列化及 watcher 差异日志包含该字段，不新增 Management endpoint；配套 Panel 可在现有 Codex 配置区域提供策略选择。

回退时将策略改为 `stable-id` 可关闭推断、保留确定的兼容修复；改为 `legacy` 恢复旧行为。策略切换清空推断索引并推进 generation；旧回调不能发布到新索引。仍在执行的逻辑请求保留已经冻结的自动标识。进程重启后确定性身份可重建，推断组不保证恢复。

## 字段与优先级

`Session-Id` 和 `prompt_cache_key`（pck）分别保存。新增自动亲和标识只补出站 header，**不自动补原本缺失的 pck**。

1. 保留客户端/配置的显式缓存字段以及已有覆盖契约。官方 Guardian 的 pck 与真实 session header 可以不同。
2. 保留 Claude Code 专用 session＋agent 映射和 execution 映射。
3. `stable-id` 保留已有 derived/调用方 API key 兜底结果；`client-aware` 将其当作候选种子，不先写入 pck 阻止推断。缺少隔离域时保留已有兼容结果。
4. 原始 XSID、明确的 session/thread/conversation 字段经过已有 `NormalizeExplicitID` 校验后，可生成确定性 header。输入为版本化命名空间、CallerScope、身份来源类型、规范化身份；不包含账号、模型、时间、正文或 request ID。
5. request/trace/window、普通 user ID 和任意 `canonical_session_id` 不直接作为会话身份。缺少可靠 CallerScope 不建立共享推断组；无可用身份时，仅冻结本次逻辑请求的临时标识。

官方根 fork 的 `pck=P / session-id=P / metadata.session_id=F` 会保留 P/P/F。例外要求原始 pck 和原生 header 明确一致、载体无歧义且 canonical metadata 已通过原有校验。任意不一致 header 仍由 canonical metadata 处理；strict/repair/off 与重复载体检查保持原语义。

## 检查与采用

原始 UA、originator 和经过校验的原生字段仅用于解释协议。cloaking 后的出站 UA 不参与客户端判断。完整官方缓存语义及明确缓存选择不做无必要的全文推断。其余可隔离的第三方请求，包括 **已有 XSID 的 ZCode**，检查实际出站内容的历史连续性。

候选索引按 CallerScope、后端入口、已选账号域和请求中实际已知的模型隔离，并从最长前缀向前检索，避免公共短前缀先耗尽候选预算。可靠身份的既有绑定按已隔离 CallerScope 的身份摘要保存，换模型或账号不会改变已确立的亲和标识。小请求复用现有 canonical-turn / fast fingerprint 作为候选提示；超过 64 KiB 的请求直接使用严格摘要索引，避免通用文本规范化的额外大字符串分配。严格判定另外使用完整内容的 SHA-256 链；快速指纹本身不能证明匹配。

HTTP 和 WS 在严格摘要之前均运行现有 input item ID 规范化（包括碰撞、超长 ID 和无效超长 encrypted reasoning item 的既有处理），摘要后不再发生未纳入比较的 input 改写。

严格摘要保留 instructions、tools 及其顺序、消息与工具参数、reasoning、text format、服务设置和内联媒体内容。只排除已知的传输/缓存路由载体。不会删除正文时间戳、UUID 或 thinking 差异。可变媒体 URL、未能验证的 file ID、依赖未知 previous response 的增量正文、重复 JSON 字段和超限内容，返回“无法判断”。

采用规则：

- 显式缓存意图优先。
- 可靠身份保留既有组；编辑、compact、工具变化和前缀失败不会令它换号。
- 新可靠身份建立确定性映射；明确的父会话/fork 关系且严格前缀相符时可继承父组。UA 或 subagent 标签不证明父子关系。
- 弱/无身份仅在最佳合格候选指向唯一组时采用。不同组同分则回退，不按访问时间强选。
- 无候选可采用未冲突的 legacy 种子，否则原子预留一个 UUID；同一冷请求内容并发到达会复用预留结果，不兼容内容不会因种子相同而合组。
- 完整重发可精确复用；其他自动延续要求已完成的用户—助手/工具交互锚点，或明确父关系。成功响应的 output 只在后续请求完整保留相同有效表示时作为延续锚点，不做宽松文本相似匹配。

检查结果在私有决策中区分 eligible / mismatch / unknown。可靠 XSID 可观察到与其他会话内容连续，但不会仅凭相似前缀被合并。

## 状态边界与资源限制

索引保存在 executor 实例中，HTTP/WS 共享，无关配置热更新继承。它不写入 `canonical_session_id`、`execution_session_id`，不改变账号 LCP 调度、WS 连接归属、`previous_response_id`、usage 会话或私有 reasoning。

Claude replay scope 在新推断组应用前计算，继续使用原有合法 session、Home KV、签名、TTL 和模型回退规则。新组只参与最终出站亲和字段。若最终 auth/model header 覆盖改变了实际 Session-Id，禁止本次发布轨迹和建立绑定，但保留冻结字段、资源释放及已建立的绑定。WS 以连接 generation 保存的实际握手头为准，取得或重连连接后核验；复用连接不会让本轮准备的 header 自动生效，不为自动亲和变化强制重连或修改 pck。

| 资源 | 上限 |
| --- | ---: |
| 成功轨迹 | 4096 |
| 前缀索引项 | 262144 |
| 单条轨迹 item | 1024 |
| 单请求候选复核 | 64 |
| 单请求扫描 | 32 MiB |
| 空闲 TTL | 1 小时 |

同时限制逻辑请求冻结记录、冷组预留和身份绑定。Manager 提供整次 Codex 逻辑请求的完成信号，覆盖账号重试和并行 lane；流式调用在通道结束后释放，不能在返回 StreamResult 时释放。已结束且无活跃引用的冻结记录在下一次索引维护时回收，不占用完整 1 小时 TTL；活跃逻辑请求即使暂时没有 attempt 引用、跨 TTL 或策略切换，也继续保留 H/K。直接调用 executor、未经过 Manager 的嵌入方式缺少该完成信号，仍使用有界 TTL 兼容回退。解析深度及节点数也有边界。索引只保存摘要、生成后的缓存组和时间信息，不保留 prompt、媒体、凭据或原始会话身份。

超限停止推断并兼容回退，不拒绝推理、不把截断视为完整匹配、不淘汰活跃决策。仅成功 `response.completed` 发布轨迹；失败、取消、旧 generation 响应不发布。服务重启及多实例之间不共享此内存索引。

## 开发审查与验证

可按两阶段审查：

- 阶段一：`codex_cache_affinity.go` 的身份/来源处理、HTTP/WS 接入、metadata 例外，以及 `FinalWireProtocolMatrix`、`CredentialsAndLegacyFinalWire`、`NativeSemantics`、`PreservesMappingsAndReplay` 测试。
- 阶段二：`codex_cache_affinity_store.go` 的候选/严格摘要/采用/生命周期，以及前缀、并发、预算、重载测试；配置与 watcher 测试验证默认值和回退。

建议验收命令：

```sh
go test ./internal/runtime/executor -run '^TestCodexCacheAffinity' -count=1
go test ./sdk/cliproxy/auth -run 'ManagerLogicalRequestLifetime|CodexCacheLifetime' -count=1
go test ./internal/config ./internal/watcher/... -run 'CacheAffinity' -count=1
go test -race ./internal/runtime/executor ./internal/config ./internal/watcher/... ./sdk/cliproxy -run 'CacheAffinity|CodexForceReplace|CodexDoesNotReplace' -count=1
go test ./internal/runtime/executor -run '^$' -bench '^BenchmarkCodexCacheAffinity' -benchtime=1x -benchmem
scripts/check-lts-contract.sh
go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'
go test ./...
```

额外回归覆盖持久 WS 的一致/不一致握手（含 required-existing）、连接 generation 更新、三条执行路径的最终正文摘要、4096 次已结束逻辑请求、lane 取消后的重试冻结，以及实际 Chat Completions 翻译往返的保守输出锚点回退。跨协议接入不保证首轮 output 在下一轮保留完全相同的有效表示，可靠 XSID 的稳定映射不依赖该锚点。

这些合成测试验证代理侧算法、最终字段和状态隔离，**不证明 ChatGPT 后端每次都会命中，也不代表全部历史缓存损失已被解释**。

## 后端对照实验（需独立授权）

实验构造器在 helper、metadata 和最终 header shaping **之后**独立修改 H/K，并用本地假上游抓取最终值：

| 组 | Session-Id | pck |
| --- | --- | --- |
| A | 旧逻辑生成 | 旧逻辑生成 |
| B | 固定 | 不新增 |
| C | 保持旧逻辑变化 | 固定 |
| D | 固定 | 固定 |

`TestCodexCacheAffinityExperimentWireIndependence` 验证 C 没有因 helper 联动变成 D。真实实验在带 `codex_cache_live` build tag 的独立测试中，普通 `go test ./...` 不会编译或运行；执行还要求显式授权环境变量。

获准后，在私有终端设置 `CPA_AFFINITY_ENDPOINT`（完整 `/responses` URL）、`CPA_AFFINITY_TOKEN`、`CPA_AFFINITY_MODEL`、必要的 `CPA_AFFINITY_ACCOUNT`。不要把 token 写进 Git、聊天或公开日志。先运行 HTTP/SSE，再单独设置 `CPA_AFFINITY_TRANSPORT=ws` 测连接。每次实验会发送 24 次请求（两轮热身、四轮轮换顺序的测量），消耗真实额度。

```sh
CPA_AFFINITY_LIVE_AUTHORIZED=yes go test -tags codex_cache_live ./internal/runtime/executor -run '^TestCodexCacheAffinityLiveExperiment$' -v -count=1
```

测试只输出模型、组别、轮次、input/cached/output token 和完成延迟，不输出 header、标识、正文、凭据或完整错误响应。根据 measured rounds 分别计算 token 加权缓存率、零读率和延迟分布；响应中的缓存 token 不等于实际费用或额度减免，后者需从独立账单/配额证据核对。若实际模型不一致，排除该轮，不能声称是受控对照。

默认仍为“新亲和标识只补 header”。只有获授权的后端实验支持时，才另行讨论更改自动 pck 行为。

嵌入式 SDK 可直接设置 `cfg.Codex.CacheAffinity.Strategy`；`Builder.Build()` 同样拒绝非法值。此配置不新增 SDK 会话语义。
