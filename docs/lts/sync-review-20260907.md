# 2026-09-07 protected full-sync 差异审查

## 范围与分段

本次 preflight：本地无 stash，只有 main 与 Desktop Tools 未合入分支；远端只有 main，没有开放 PR。Desktop Tools 已经通过 #249 合入，保留两个原提交。

额外查询了作者已关闭未合并的历史 PR（81 项），不能把 GitHub 的 CLOSED 直接当作丢失工作：#229 已由 `b6287b2c` 及 #230 吸收；#228 由 `2555cde2` / `9b88808f` 取代，当前有 MCP schema 回归；#192 明确撤回且禁止原样复活，后续 provider-state/generation-fence 已按当前主线实现维护。历史 selective-port 批次（如 #115、#42）在关闭说明中明确由 #119/#121/#122 的 `05b97247` full-sync 替代；该 baseline 在当前 main 可达。旧发布和 AGENTS 混合候选不恢复为产品代码。

- 原 upstream baseline：`17a65ee5470fbaf0e22fc219381e6a4ae9e07624`。
- 第一段目标：`4c1bebe837a6e624215a97dddee5db11f0eeb8bf`。
- 第二段目标：`934fb7928c42a8dd0aeaf39a321bef6601b55eb6`。
- 缺失历史共 71 commits；按 first-parent 切成两段，使用 merge commit，不以文件覆盖或 squash 冒充同步。
- 近七天中已属于旧 baseline 的提交保持 ancestry，不重复 cherry-pick。

## 实际合并判断

| 范围 | 分类与处理 |
|---|---|
| provider / translator / listener / wsrelay | upstream-shared，吸收原生签名、cache usage、terminal event、连接并发与兼容修复。 |
| Auth / refresh / retry | adapted：保留 LTS Flow、generation、异常 retry 和 continuity；吸收上游独立 floor/tracker/epoch/valid-token 修复。 |
| 插件 schema 5 | divergent：LTS schema 5 已用于 lifecycle，history 省略按 StreamChunkHistoryOmitted 明示协商；吸收按实际能力跳过历史复制的优化。 |
| Codex agent_message | divergent：保留严格明文 provenance 与用户 turn 边界；新增 orphan-delegation-compatibility 为独立默认关闭功能。 |
| token cache-write | adapted：新增透传保留；OpenAI input 总量转换成 Claude uncached 时同时扣除 cache read/write，避免双计。 |
| README / assets | commercial-neutral：保留 upstream 历史，不恢复赞助、返佣和付费推荐图片；中性开源项目介绍可吸收。 |
| usage / Management / Panel | retained-capability：保持 canonical v3、timing v1、全部 usage routes、Panel 下载来源和现有 JSON 字段。 |

## 非 retired 补丁逐项复查

以下结论以两个明确目标的源码 diff 为依据；`removable` 不越级退休，新增本地修正另行登记。执行测试结果在末尾记录。

| 补丁 | 结论 | 源码差异判断 |
|---|---|
| `codex-model-fallback` | patch-still-required | 上游 attempted-auth/TPM 等待解决同模型重试，不替代有条件跨模型 fallback 和 replay 隔离。 |
| `codex-rate-limit-continuity` | patch-still-required | quota floor 与 model/credential 区分不是 incumbent/fresh/canary 状态机，保留连续性策略。 |
| `codex-interactions-service-tier-response` | patch-still-required | 本轮未修改 Codex → Interactions 的 tier 修正入口。 |
| `plugin-configured-enable-default` | patch-still-required | 插件 history 性能优化不改变默认 enabled 的 YAML/runtime 语义。 |
| `home-plugin-sync-cancellation` | retired | #5374 已进入旧基线；删除提交 `5384ca6d` 只移除重复注释与直接 wrapper 测试，保留实际 cancellation/TLS 回归。 |
| `codex-gpt56-ultra-level` | patch-still-required | 模型目录更新只更新客户端能力，不替代最终 wire Ultra/Max 转换及自定义模型隔离。 |
| `codex-model-header-provider-snapshot` | patch-still-required | UA/catalog 更新不替代 provider-specific header 快照与连接 key 绑定。 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 默认 UA 更新不替代模型覆盖后最终 OAuth identity/header 归一化。 |
| `codex-client-metadata-privacy` | patch-still-required | fork lineage、Header.Clone 可吸收；保持 canonical 优先、工作区策略、session hash/auth_index 脱敏和延期发布。 |
| `codex-model-not-found-request-scope` | patch-still-required | 429 策略改动不等价于 request-scoped 404，不恢复全池惩罚。 |
| `codex-websocket-reader-generation` | patch-still-required | 客户端 ping 和 wsrelay pendingRequest 是不同连接层，不替代 Codex upstream reader generation fence。 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 目录和 Claude summary 更新不替代 Spark 最终 payload 的 summary 移除。 |
| `xai-explicit-tool-choice-none` | patch-still-required | Antigravity none 修复针对不同 provider；xAI search allowlist 仍单独需要。 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | Kimi thinking helper 更换后仍保留 clone，不修改共享 Auth。 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | WriteTo 接口和 header clone 可吸收；不恢复 LTS 已移除的 request spool，保留 unwind 清理。 |
| `api-request-body-size-limit` | patch-still-required | WebSocket ping/orphan 输入修复不替代请求体上限和热重载限制。 |
| `object-store-auth-path-containment` | patch-still-required | 本轮无 remote store path containment 等价实现。 |
| `auth-token-private-file-permissions` | patch-still-required | SDK refresh lead 改动不替代跨平台凭据文件权限保护。 |
| `auth-store-list-error-propagation` | patch-still-required | 本轮无 store List 错误传播等价修复。 |
| `auth-store-metadata-hydration` | patch-still-required | 异步 merge 的 metadata 与初始 File/Git/Object/Postgres hydration 不同。 |
| `panel-release-token-isolation` | patch-still-required | 本轮无 Panel release token 隔离改动。 |
| `gemini-unknown-method-not-found` | patch-still-required | Gemini translator 内容归一化不替代未知 API method 的 404。 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | token counting 清理与 quota floor 不替代 count endpoint 404 的局部错误分类。 |
| `management-logs-bounded-response` | patch-still-required | 日志 io.WriterTo 修改不替代 Management 分页/响应大小上限。 |
| `transient-eof-cooldown` | patch-still-required | wsrelay 并发修复不替代 EOF/UnexpectedEOF 的 transient 分类。 |
| `expired-availability-pruning` | patch-still-required | scheduler/cooldown 更新不替代 Management 读取前的过期状态收敛。 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | Claude tool alignment 与 Responses → Chat assistant turn 合组是不同转换方向。 |
| `codex-desktop-tool-overlay` | patch-still-required | orphan delegation 处理已有返回项，不负责向主会话补入缺失工具。 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | orphan delegation 是新开关；保留 agent_message 全部内容可证为明文及 namespace 验证。 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | xAI compact 功能不替代工具定义 provenance 与 encrypted_function_args 回填。 |
| `provider-runtime-state-isolation` | patch-still-required | 上游并发合并、quota scope 部分相关，仍需 provider 切换清 runtime、transient 与 quota 区分及 reconstruction cap。 |
| `auth-provider-generation-fence` | patch-still-required | 上游 RegistrationEpoch 不替代 LTS 私有生命周期 generation，保留 execution/count/stream/refresh 写回防线。 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | live 上游变化保留零 token 计账、SDP 脱敏、Home 401 单次归属和 proxy fail-closed。 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 本轮未修改隐式 message 的本地 token 估算入口。 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | compaction/reconnect 路径与满 channel 取消顺序不同，保留 reader 失效测试。 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | quota 单调性不替代 bad-credentials 的 auth/model 一致 unauthorized 分类。 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | Claude fingerprint 更新可共存，Codex direct image 与 caller-owned custom header 修复仍存在。 |
| `astra-native-responses-controls` | patch-still-required | Astra 目录新增不替代 native configuration_update/parallel_tool_calls 以及拒绝有损 Chat 转换。 |
| `astra-async-guidance-hotfix` | patch-still-required | 远程 catalog 仍可能携带旧句，保留精确句子修正及未来 catalog 不重写保护。 |
| `local-flow-control` | patch-still-required | upstream quota/scheduler 是服务商可用性，不是本地联合 admission；保留默认关闭和最后有效配置。 |

## 上游 first-parent 提交清单

本轮新增登记：`openai-claude-cache-write-accounting`、`plugin-stream-history-capability-negotiation` 为 required；`wsrelay-terminal-frame-preservation`、`codex-dotted-collaboration-tool-restoration` 为 upstreamed（仅 dev 已有等价实现）。对应实际引入或既有实现提交均保持可达，不提前越级退休。

普通维护提交由 merge 统一吸收；表中的“适配”指保留前述 LTS 不同解法，不是丢弃整个提交。

| Commit | 主题 | 处理 |
|---|---|
| `893abbab` | feat(translator): support cache write tokens in claude responses | 适配 LTS 契约后吸收 |
| `15231e9f` | fix(antigravity): support native thinking signatures without prefixes in claude translator | 吸收 |
| `c2834b68` | fix(antigravity): retry model fetching per endpoint and prioritize daily base url | 吸收 |
| `8deeb4ac` | fix(antigravity): preserve unsigned gemini thinking blocks with trailing carriers | 吸收 |
| `02c02cda` | fix: harden concurrent session handling, listener lifecycle, and token accounting | 吸收 |
| `d0fb44ca` | fix(antigravity): strip tool config, labels, and session id in token counting | 吸收 |
| `272c1cff` | fix(antigravity): bypass quota cooldowns and credit hints when cooling is disabled | 吸收 |
| `dacae582` | feat(registry): add claude fable 5.1 and gemini 3.8 flash models | 吸收 |
| `bdcccfb8` | chore(registry): remove "minimal" level from dynamic_allowed definitions in models | 吸收 |
| `9812b1e7` | fix(auth): validate access token expiration and retain valid credentials on refresh failure | 吸收 |
| `f416175f` | fix(home): map user_credits_insufficient to 402 and user_period_limit_exceeded to 429 | 吸收 |
| `18e01a76` | fix(auth): enforce minimum cooldown floor and track attempted credentials on 429 | 适配 LTS 契约后吸收 |
| `df7e04ea2` | fix(claude): upgrade default Claude Code baseline and fingerprint to 2.1.258 | 吸收 |
| `63fdd77f` | Merge pull request #5436 from router-for-me/models | 吸收 |
| `d577e630` | feat(routing): make subagent session affinity configurable via session-affinity-subagents | 适配 LTS 契约后吸收 |
| `09471dd9` | fix(auth): prevent individual model quota cooldowns from blocking credential | 吸收 |
| `e899f0e5` | feat(session): derive distinct branch session ID, parent lineage on Merkle LCP forks, and enhance Codex fork/subagent affinity (#5418) (#5454) | 适配 LTS 契约后吸收 |
| `ebbce50e` | chore: remove sponsorship images for Claude API and Code0 from README files | 仅保留 ancestry，不恢复商业内容 |
| `699b0659` | feat: 添加 AxisNow 赞助信息及相关图像到 README 文件 | 仅保留 ancestry，不恢复商业内容 |
| `93f5266b` | feat: add Swiftproxy Sponser | 仅保留 ancestry，不恢复商业内容 |
| `291cfb87` | feat(codex): support orphan delegation compatibility via orphan-delegation-compatibility | 吸收 |
| `2a6b87ac` | feat(openai): send periodic ping control frames during responses websocket streaming | 吸收 |
| `728ea8b8` | fix(translator/gemini): nest image parts inside functionResponse | 吸收 |
| `f804fb5f` | fix(translator/claude): defer message_delta and cache streaming usage | 吸收 |
| `e44432ab` | perf(antigravity): batch replay degradation rewrites (#5461) | 吸收 |
| `ba2cdea3` | fix(translator/claude): handle incomplete status and terminal state on max_tokens | 吸收 |
| `649a8bdb` | feat(plugin): omit stream chunk history on payload chunks for schema v5 | 适配 LTS 契约后吸收 |
| `b9110ec9` | Merge pull request #5452 from deathemperor/docs/add-infinitus | 吸收 |
| `9ec2bc21` | Merge pull request #5435 from huangruiteng/codex/openai-compat-bounded-rate-limit-waits | 吸收 |
| `6a26e92a` | fix(translator/interactions): avoid tool name collisions with Antigravity intrinsic tools | 吸收 |
| `aa365277` | fix(translator/claude): downgrade strict mode when schema misses required properties | 吸收 |
| `4c1bebe8` | fix(auth): preserve concurrent modifications during auth refresh and preparation | 适配 LTS 契约后吸收 |
| `1c45093d` | fix(antigravity): tighten replacement offset guard in tool provenance degradation | 吸收 |
| `086ad91b` | feat(claude): implement 2.1.258 billing header fingerprint chain and upstream request continuity | 吸收 |
| `d7052c96` | feat(claude): add 2.1.258 dynamic beta headers, model fallbacks, and paired cache TTL | 吸收 |
| `de4aa600` | feat(claude): add Fable 5.1 reporting outcomes block and post-payload reconciliation | 吸收 |
| `4a5ab534` | feat(claude): harden probe and helper request classification, diagnostics isolation, and late cloaking | 吸收 |
| `0fe19ede` | fix(translator): preserve Gemini prompt cache by demoting mid-session developer messages (#5490) | 吸收 |
| `e56fae88` | fix(translator): flush pending developer notice before intervening user turn | 吸收 |
| `f2041a2c` | fix(antigravity): use ContentHasGeminiFunctionResponse instead of gjson projection | 吸收 |
| `f6d19a32` | fix(gemini): ensure functionResponse normalizes to user role in Gemini request normalizer | 吸收 |
| `acf919ce` | perf(antigravity): batch reasoning replay mutations | 吸收 |
| `c77b1369` | feat(models): add gpt-6-astra model and update codex client configurations | 吸收 |
| `5208aec7` | fix(codex): preserve empty supported_reasoning_levels array | 吸收 |
| `31ec4362` | chore(models): remove gpt-5.4 and gpt-5.4-mini models | 吸收 |
| `9dfddd61` | fix(aistudio): normalize thinking level to uppercase | 吸收 |
| `084f25c7` | fix(watcher): preserve concurrent file updates during auth snapshot rescans | 吸收 |
| `7c2f6ce0` | fix(claude): avoid mid-conversation system splicing for advisor calls or results | 吸收 |
| `5ab0bca0` | fix(codex): scope usage limit errors to credentials and parse flexible quota resets | 适配 LTS 契约后吸收 |
| `580df364` | feat(usage): propagate session and parent session hierarchy to usage reporting queue | 吸收 |
| `c76dfd4e` | chore(codex): update codex user-agent to 0.153.3 | 吸收 |
| `fa01468e` | perf(auth): optimize scheduler result updates with targeted model shards | 适配 LTS 契约后吸收 |
| `00c63a56` | feat(pluginhost): expose outbound HTTP wire profile to plugin requests | 吸收 |
| `70f45604` | feat(antigravity): add conversation compaction support and capsule encryption | 吸收 |
| `0e85eb46` | fix(xai): support compaction fallback from payload input or previous response id | 吸收 |
| `5dc428f3` | fix(gemini): append trailing user turn for requests ending with model content | 吸收 |
| `8564142f` | fix(claude): preserve non-result block positions during tool result alignment | 吸收 |
| `a76da711` | fix(antigravity): omit tools when tool_choice is none | 吸收 |
| `1c22598d` | fix(auth): preserve active cooldown deadlines on subsequent failures | 适配 LTS 契约后吸收 |
| `e92f6cf5` | fix(antigravity): enable thinking summary when responses reasoning effort is set | 保留 Responses effort/summary 独立语义；显式 summary 回归保留 |
| `d2f71220` | fix(claude): normalize codex agent messages in responses conversion | 适配 LTS 契约后吸收 |
| `63fd2550` | feat: add Aiberm sponser | 仅保留 ancestry，不恢复商业内容 |
| `578ac8fd` | fix Aiberm sponser logo | 仅保留 ancestry，不恢复商业内容 |
| `4cd8ee7f` | feat: add Aiberm sponsorship information to README_JA.md | 仅保留 ancestry，不恢复商业内容 |
| `934fb792` | feat: add link to Aiberm in sponsorship section of README files | 仅保留 ancestry，不恢复商业内容 |

## 验证与交付状态

## upstream/dev：只回补可独立验证的修复

此次读取的 dev HEAD 为 `7871a5a978d5753f926c565d28997503497da8b0`，其中 7 个提交尚未进入 main；不整体合入开发分支，也不把其 ancestry 冒充稳定同步 baseline。

| Commit | 判断 | 原因 |
|---|---|---|
| `7871a5a9` | 窄回补，upstreamed | 本轮 main 的 pendingRequest 在终止帧投递时直接读掉一个缓存帧；改为可取消背压，并验证完整顺序及满队列取消。 |
| `82f4f370` | 窄回补，upstreamed | 补齐 collaboration-optimize 点号前缀的冲突识别和 namespace/name 还原；保留 LTS 明文门禁，不扩展 schema 改写。 |
| `510c9c8f` | 暂不吸收 | 新增 trailing signature 隐式缓存会改变可见 timeline 和 replay 依赖；现有显式 carrier 是另一种有效实现，等待 main 基线再复核。 |
| `d5397905` / `f19d6da0` | 暂不吸收 | 连接池默认改短连接、新 config 及 reload purge 是较大的网络策略变化，不能视作无影响的小修复；未进入 main。 |
| `bf20b999` | 暂不吸收 | 同时改写复杂工具 schema 并改变零输出 incomplete 的错误分类，跨 HTTP/WebSocket 和 LTS abnormal retry，留待稳定基线审查。 |
| `4f038099` | 暂不吸收 | 通过 system prompt 模拟 structured output 不是协议级强约束，会改变 prompt/cache 行为；不默认当作等价支持。 |

第一段本地验证完成：`go test ./...` 为 99 packages pass、30 packages 无测试，零失败；server build、LTS guard、registry lifecycle validator、Usage/Management 契约测试均通过。Auth hedge 测试使用 barrier 固定两条 lane 已 dispatch 的前提，不改变生产 cooldown；定向重复及 race 测试通过。wsrelay、session/Flow 的定向 race 通过。

第一段已通过 #250 合入主线（`872c8431ad1445ad79c9638928568e092dd7faf1`），exact-head 的 7 项 CI 全部成功，无未解决 review conversation。

第二段适配说明：

- `5ab0bca0` 的 credential quota scope 作为独立错误能力加入 LTS statusErr，保留既有 classifier、model fallback reason 与 transient rate-limit class。
- `580df364` 的 session/parent session 为 SDK、插件、queue 的加法字段；保留 canonical v3、timing v1、service tier 与 provenance。Live/Alpha Search 保留 request-scoped selection cleanup，再合入 session hierarchy。
- `e92f6cf5` 保留 ancestry，但不采用 effort 自动开启 summary 的行为；保留其新增的显式 summary、generate_summary、null summary 回归。现有 Responses effort/summary 独立语义继续有效。
- `d2f71220` 仅吸收明文 agent_message 转换；opaque、mixed、空数组及非 string 内容保持原状，复用既有 plaintext contract 的生命周期，不把 encrypted_content 当明文。
- catalog 吸收上游新增及下架条目，Astra 保留 LTS async guidance；Ultra capability 继续由兼容层维护。
- watcher rescan revision、plugin HTTP wire profile 与 Antigravity compaction 按独立上游功能吸收；HTTP profile 不改默认 transport，保留 proxy 错误失败关闭与 streaming 零总超时。

第二段本地 `go test ./...` 已通过：99 packages pass、30 packages 无测试、零失败。Auth 全包及 quota/cooldown/scheduler/session/Flow/generation/continuity/retry 定向 race 通过；watcher、pluginhost、redisqueue、session、httpwire race 通过。

验证适配还包括：更新跟随 upstream catalog 的 Codex 0.153.3 identity 断言；同凭据模型 fallback 的聚合测试改用 model-scoped capacity 错误，账户 quota 耗尽不再假设切换同凭据模型可绕过。新上游 quota 跨模型回归继续保留。

第二段 server build、LTS contract guard 与 registry lifecycle validator（相对 origin/main）均通过。本报告记录合并候选的本地验证；GitHub exact-head CI 与最终合并证据见 [PR #251](https://github.com/BlueSkyXN/CPA-Core-LTS/pull/251)。本文件不声称发布或部署。
