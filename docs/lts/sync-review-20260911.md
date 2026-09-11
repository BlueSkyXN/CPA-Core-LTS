# 2026-09-11 Core / Panel 收尾与上游审查

## 范围

- 窗口：`2026-09-04T11:30:45Z` 至 `2026-09-11T11:30:45Z`。Core upstream main `09a29bd345bc44c473abe7fd07859e32df2ea543`（v7.2.157），dev `ae8f1f8be5a680c54b02ef7b639b8ecea3a0e18a`。
- 窗口内 main 有 77 个非 merge 提交，其中 35 个已由上次审查纳入主线；本次待同步 42 个非 merge、3 个 merge 提交。起始 LTS main 为 `aa4eef0441fb35021ecb64698d8dd698b6f98371`，merge-base 是 `d198db54d4c4886c99b21488d54fc576933019a3`。
- 按 first-parent 分两阶段：阶段一到 `a59b1764e781e0c20bf102349cd9bc202ce8f668`（27 个非 merge、2 个 merge）；阶段二到 `09a29bd3`。因为同时涉及 session、usage、plugin 与 auth lifecycle，不能只凭自动 merge 无冲突放行。
- Core 的 PAT delivery、publication recovery、usage-query 三条本地/云端分支均已合并（#255/#256/#254），没有独有提交；起始只有一个干净工作树、空 stash、零开放 PR。91 个不可达 commit 没有 9 月 8 日以来的新对象，沿用上次恢复对象审查，不恢复旧快照。
- Panel `937aa4ac` 已由 #83 完成源码收尾、七日 selective-port 与主线 CI；本次再次 fetch，upstream 仍为 `ed5f1c48`，无新增。保留其独立目录、完整统计和四语言，不套用 Core full-sync。
- 不发布、部署、写线上配置、删除分支/工作树或 Git 恢复对象。

## 阶段一逐提交判断

| 上游提交 | 分类与依据 |
|---|---|
| `6e1f9ec4` | upstream-equivalent：missing/null tool schema 先前已窄回补；纳入 ancestry，ledger 从 upstreamed 到 removable，不删除测试或实现。 |
| `dc21a426` | adapted/divergent：保留历史但不启用 JSON Schema 到 responseSchema 的无条件改名。两种 schema 方言不等价；保留双键冲突交给上游验证，不静默丢字段。 |
| `280b96ac`、`35a47238` | absorbed：现在两者均进入 main，成组吸收 Claude helper beta、压缩及 request-ID 识别，保留 confirmed-client 和自定义 header 规则；模拟请求测试不是新一轮真实账号指纹验收。 |
| `d4146bde` | absorbed：Kimi native Responses stream/nonstream、URL/thinking/usage 独立于旧 Claude delegated-auth 修复；保留不可变 auth clone，compact 仍明确拒绝。 |
| `68dd99d5` | upstream-equivalent：transport settings cache key 先前已回补，纳入 ancestry；保留 direct/proxy/reload 回归，ledger 进入 removable。 |
| `ef99119e` | absorbed：交错 OpenAI tool delta 转为顺序 Claude content blocks，保留 tool index、arguments 和结束顺序。 |
| `48e5e9e0` | absorbed：refresh timer wait 有上限，避免超长等待影响重新计算；不改认证失败策略。 |
| `e365ab0c` | absorbed：reasoning 映射为 Claude thinking 子项；保留 LTS inclusive total 和 cache-write 扣除，不能把子项重复计费。 |
| `39058915`、`1119ef14` | adapted：统一 harness hierarchy 提取并修复空前缀/根 context；保留 LTS execution/canonical metadata 优先级，补回重构遗漏的 Amp thread ID 及原优先级。 |
| `6b187e77` | adapted：queue 上报 session/parent 使用 deterministic UUIDv8；不改变 canonical v3 import、dedup identity、token/timing 字段或已有统计。保留 Redis 与完整 usage 并存。 |
| `1d5f7b2a` | adapted：quota deadline 去除 monotonic reading；保留 LTS xAI bad credentials、transient rate limit 与已有冷却延长规则。 |
| `20e3f731` | absorbed：headless Antigravity OAuth helper 为 additive API；保留 token store 私有权限和调用方生命周期。 |
| `d8f2dcee` | absorbed：functionResponse 名称修复批量拼接，保持 ID/name 匹配与越界 fallback，避免每次字段修改复制完整历史。 |
| `1c9d7194`、`54b17ce8`、`8c0ad8cc`、`3639d924` | absorbed：只查已安装来源、同身份合并 release 请求、并发 2、成功/失败 TTL、共享 GitHub 限流且按认证/网络出口隔离；保留源信任、重定向、checksum 和取消清理。 |
| `d0bb908c` | absorbed：规范化 Responses tool outputs 和 alternate call IDs；保留 LTS Gemini 显式 signature carrier 与请求内顺序。 |
| `e026cbf4`、`a163c5e7` | absorbed：仅静默成功 health probes；失败探针保留日志，不改 request/usage 采集。 |
| `c6327a86` | adapted：schema 6 的 raw Management JSON；保留 LTS schema 5 execution lifecycle 含义和显式 StreamChunkHistoryOmitted 协商，旧插件仍兼容。 |
| `4fde97f4` | absorbed：Gemini user turn 内 trailing text 排到 functionResponse 之前，保留非文本 parts 和工具对应关系。 |
| `7fac6b15` | absorbed：Codex tool schema 移除不支持的 dialect keywords，不把配置规则变为通用重写。 |
| `0796d6d1` | adapted：additive host affinity lookup，只读且不刷新 TTL、不重绑账号；锁内只取快照，插件回调在锁外，返回 auth_index 而不是凭据。 |
| `a59b1764` | adapted：Claude start/delta usage 聚合后发布，失败保留已有用量；上游新增 3 个测试按 LTS canonical inclusive-input 断言，其余 cache 子项和总量断言保留。 |

两个 merge commit 只汇集 plugin-store 与 health-log 的上述变更，不另作 cherry-pick。文本冲突为 `internal/redisqueue/plugin.go`、`sdk/cliproxy/auth/{conductor_cooldown,conductor_selection,selector}.go`、`sdk/pluginabi/types.go`，按各自合同合并，而非整文件选择一侧。

## 阶段一验证

首次 `go test ./...` 暴露三个 Amp 优先级回归和三个 canonical input 断言冲突；已修正。修正后 `go test -count=1 ./...`、六包 race（auth/session/flowcontrol/redisqueue/pluginhost/pluginstore）、LTS guard、server build、Usage/Management 专项、完整 Responses translator 测试、配套 Panel 临时 Core smoke 全部通过。精确 head CI 的最终结果在阶段 PR 中回读。

完整 usage、Management `/usage*`、Panel 下载源、auth/config 兼容保留；Flow/abnormal retry/continuity/fallback 均未由新的 session 或 plugin 能力替代。真实账号生成、Home 部署和 HF Space 未操作。

首轮 CI 的嵌套 provider module 测试发现 CodeBuddy/Qoder 把 schema 固定为 5，主模块 `go test ./...` 不包含这些嵌套 module。调整为“等于当前 ABI schema 且不低于 lifecycle 引入版本”，保留所有 capability 断言，单独运行两个 module 的 race/vet/build；不降回 schema 5 或关闭 CI。

## 阶段一 Downstream patch review

以下逐项对照本阶段 from/to diff 和当前 regression tests；没有等价 upstream 实现的条目继续保留，不因无文本冲突或能构建而退休。

| Patch | Conclusion | 本阶段依据 |
|---|---|---|
| `responses-effort-summary-independence` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-model-fallback` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-rate-limit-continuity` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-interactions-service-tier-response` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `plugin-configured-enable-default` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `codex-gpt56-ultra-level` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-model-header-provider-snapshot` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-client-metadata-privacy` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-model-not-found-request-scope` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `codex-websocket-reader-generation` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `xai-explicit-tool-choice-none` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `api-request-body-size-limit` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `object-store-auth-path-containment` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `auth-token-private-file-permissions` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `auth-store-list-error-propagation` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `auth-store-metadata-hydration` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `panel-release-token-isolation` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `gemini-unknown-method-not-found` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `management-logs-bounded-response` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `transient-eof-cooldown` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `expired-availability-pruning` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-desktop-tool-overlay` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `provider-runtime-state-isolation` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `auth-provider-generation-fence` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `astra-native-responses-controls` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `astra-async-guidance-hotfix` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `local-flow-control` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `openai-claude-cache-write-accounting` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `plugin-stream-history-capability-negotiation` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `wsrelay-terminal-frame-preservation` | upstream-equivalent | 等价实现已纳入；保留 regression tests，未越级退休。 |
| `codex-dotted-collaboration-tool-restoration` | upstream-equivalent | 等价实现已纳入；保留 regression tests，未越级退休。 |
| `session-dispatch-context-after-selection` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `antigravity-compaction-completion-validation` | patch-still-required | 本阶段无对应实现变更，原回归仍保留。 |
| `gemini-explicit-text-signature-carriers` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `antigravity-pool-settings-cache-key` | upstream-equivalent | 等价实现已纳入；保留 regression tests，未越级退休。 |
| `claude-missing-tool-schema-default` | upstream-equivalent | 等价实现已纳入；保留 regression tests，未越级退休。 |
| `amp-session-extraction-priority` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |
| `antigravity-response-json-schema-dialect-preservation` | patch-still-required | 触及相关路径；LTS 行为及所列回归保留，新增上游能力不构成完整等价替代。 |


## 阶段二逐提交判断

阶段一 #257 已以 merge commit `e0e8071a` 合入，精确 head `aa531e5f` 的九项 CI 通过，publish 按 PR 条件跳过。阶段二从该主线创建独立工作树，merge `d5940280` 纳入 upstream `09a29bd3`，补齐剩余 15 个非 merge、1 个 merge 提交。

| 上游提交 | 分类与保护边界 |
|---|---|
| `b064b832` | adapted：新增默认 false 的 `codex.model-level-cooling`，只改变 usage_limit 的 model/credential 范围。保留 LTS typed fallback/retry-after/continuity，并补齐已有 handshake helper 的同一开关，避免 HTTP/WS 不一致。 |
| `e56abd56`、`37ce368c` | absorbed：工具 schema 不支持的 Unicode property escapes、patternProperties 键和转义 fast-path，保留嵌套对象及非匹配 pattern。 |
| `60e5b8bd` | adapted：显式 Management auth refresh 和 TUI 入口，保留原路由鉴权；不把完整 Auth（含 token metadata/attributes）返回给状态请求，成功只返回白名单摘要，失败统一脱敏。批量并发补回 dev worker 修复。file-store 读取不再触发 Antigravity 网络写回；保留 LTS 权限和 metadata hydration。 |
| `3bf787fc` | adapted：canonical session 自定义头展开与跨 executor 覆盖；保留 post-selection context、generation fence、原客户端 header 解析和 scope，不把派生 session 提前写入错误的执行上下文。 |
| `aedc9e6a` | absorbed：永久认证失败用 typed terminal error，区分耗尽与可恢复冷却；不把普通取消/网络错误当认证失效。 |
| `d1a024e9` | absorbed：GPT Image 2.5/Flare/Sunburst 及 provider-scoped capability 更新，保留 client registry epoch、LTS 模型目录字段和 direct images header。 |
| `6a73f396` | absorbed：显式请求 1h TTL 的 subagent 保留 cache TTL/beta；普通 helper/probe 不擅自提升。 |
| `bd03aabc` | adapted：prewarm 后续请求恢复未发送给上游的输入，支持 named tool outputs；保留 LTS request lifecycle、上下文控制和 generation-aware WebSocket。 |
| `25913086` | adapted：nested error、large-number 精度和 sequence 透传；旧客户端使用的顶层 code/message 同时保留，不用替换 JSON shape 的方式破坏已有客户端。native Codex response.failed 路径仍独立。 |
| `3ae9093d` | adapted：模型容量错误参与 pre-payload bootstrap failover；保留 LTS classifier、原始 frame buffering 和已交付后不重放的限制。 |
| `dde250f1` | adapted：明确的 model_not_found capability 错误可以模型冷却/轮换；typed request fault 与既有 invalid_request_error/param=model 404 优先，不能全部按字符串归为模型故障。新旧两组回归同时通过。 |
| `4dce5f3a` | absorbed：忽略 null/空 finish_reason，不提前结束 Gemini 响应。 |
| `638ed7e1` | absorbed：SSE 跨 chunk 的 CRLF 合并，保留前序合法 frame、终止状态和取消语义。 |
| `09a29bd3` | upstream-equivalent：删除 RunAPI sponsor，LTS commercial-neutral 文档本已不含这些内容；保留 ancestry，不恢复其他推广。 |

冲突文件：三套 README、`internal/config/codex_websocket_header_defaults_test.go`、Codex HTTP stream/terminal、WebSocket errors/execute/stream、custom headers tests。README 保留商业中立；测试保留双方独立函数；executor 保留 LTS 状态机并适配 model-level cooling，不覆盖整个上游文件。

## dev 观察与窄回补

没有整体 merge dev，也不把尚未合并的工作称为 upstream main 已支持。

| dev 提交 | 决策 |
|---|---|
| `5a07045e` → `cadb883b` | narrow backport / upstreamed：fco_ 是 output item ID，不是 call ID；common/Gemini/Antigravity 的配对回归通过。 |
| `6dce7867` → `f6262181` | narrow backport / upstreamed：refresh-all 使用配置的 worker 上限，取消后不启动剩余请求；补齐本次新增接口的资源边界。 |
| `fd3e6623` → `6eb672ec` | narrow backport / upstreamed：空文本不提前结束 thinking block，签名接受规则不变。 |
| `c8ecb4f3` → `902579de` | narrow backport / upstreamed：同 chunk reasoning 先于正文，保留输出序列和完整统计。 |
| `4edf9d1d` | partial already-equivalent / defer：LTS 已通过 InspectResponsesControls 记录有效的末次 translated effort，并在 compaction 后重置；上游新提取器忽略 compaction、接受相邻/不完整 update。保留 LTS 方案，不复制另一套解释器；请求侧及 xAI 扩展未自动纳入。 |
| `9fad5055` | defer：5xx/challenge 区分有价值，但同时改变 recoverable RetryAfter 与现有 transient cooldown 规则；尚在 dev，待独立 typed-error/continuity 验证，不把本轮已批准的模型冷却兼容扩成新的暂退算法。 |
| `b8e6ec0a`、`4cd17293`、`4efcac79`、`5c80a01c` | defer：分别新增中断 tool pairing、tool-result image、incomplete event 与 audio-part 翻译。需要额外跨协议/显式 carrier/terminal 契约核验，本轮只窄回补可独立验证的 ID 与块顺序问题。 |
| `942bda99`、`54776fb3`、`75ce6352` | defer：未知/服务端工具/CAQS 签名分类涉及 provider-private replay 的信任边界；不以签名可解码或新增测试文件代替兼容证明。 |
| `d1702fdf` | defer：watcher revision 是另一层迟到更新控制，不能把 LTS 已有 auth generation/reconcile fence 直接删除；等稳定基线逐层适配。 |
| `fc96a87f` | defer：组织哈希凭据名和 legacy migration 涉及已保存身份/文件兼容，不随 dev 更新自动迁移。 |
| `2912516c` | defer：插件 execution-result policy 位于 quota/cooldown 写入前，需与 Flow、usage/fallback 的 finalizer 所有权独立审查。 |
| `c8f723e0` | defer：usage/base_url 新字段需队列、完整统计、脱敏和 Panel 合同一起核对，不先声明数据模型已兼容。 |
| `8f23ad02`、`8461b4e9`、`377c315f`、`8bd67f33` | defer：alias 元数据/能力收紧、Codex UA、Claude billing fingerprint、Kimi K2.8/temperature 归下一稳定 provider/model 基线；本轮不为这些 dev 能力新增推理调用。 |
| `e88cd947`、`ae8f1f8b` | reject：README sponsor/图片，继续遵守 commercial-neutral。 |

## 阶段二验证和剩余边界

本地首次预演出现 error-classifier 递归，已把请求作用域的优先检查缩到非递归的 typed/404 条件；修复后无缓存全量通过。并行负载下一个旧 plugin reload timeout，单独原样重跑三次通过，未放宽等待阈值。新增 upstream stream-error fixture 先关 data 再发 err 导致 race 下先报告 missing terminal；改为生产约定的先排入 terminal error 再关闭 data，原 nested error/sequence 断言全部保留，race 连续十次及整个 OpenAI handler 包通过。

阶段二 `go test -count=1 ./...`、auth/session/Flow/executor/registry/handler race、当前 Panel 临时真实 Core smoke、server build、LTS guard、registry lifecycle、Usage/Management 和完整 Responses translator 均已通过；最终精确 head CI 在 PR 中回读。Panel 无需新增表单或修改默认值：新增 Core YAML 字段由既有源码编辑和未知字段保留机制承载。未做真实账号付费生成、发布、部署或 HF Space 变更。

## 阶段二 Downstream patch review

每条非 retired 补丁继续核对到 upstream `09a29bd3`；四个 dev 回补为 upstreamed，不提前宣称已经进入 main。既有 removable 条目保留实现/测试，不为状态标签删除已成为 shared 的代码。

| Patch | Conclusion | 本阶段依据 |
|---|---|---|
| `responses-effort-summary-independence` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-model-fallback` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-rate-limit-continuity` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-interactions-service-tier-response` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `plugin-configured-enable-default` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-gpt56-ultra-level` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-model-header-provider-snapshot` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-client-metadata-privacy` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-model-not-found-request-scope` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-websocket-reader-generation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `xai-explicit-tool-choice-none` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `api-request-body-size-limit` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `object-store-auth-path-containment` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `auth-token-private-file-permissions` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `auth-store-list-error-propagation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `auth-store-metadata-hydration` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `panel-release-token-isolation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `gemini-unknown-method-not-found` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `management-logs-bounded-response` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `transient-eof-cooldown` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `expired-availability-pruning` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-desktop-tool-overlay` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `provider-runtime-state-isolation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `auth-provider-generation-fence` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `astra-native-responses-controls` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `astra-async-guidance-hotfix` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `local-flow-control` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `openai-claude-cache-write-accounting` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `plugin-stream-history-capability-negotiation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `wsrelay-terminal-frame-preservation` | upstream-equivalent | 已合并基线的等价实现与原回归保留；本阶段不退休。 |
| `codex-dotted-collaboration-tool-restoration` | upstream-equivalent | 已合并基线的等价实现与原回归保留；本阶段不退休。 |
| `session-dispatch-context-after-selection` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `antigravity-compaction-completion-validation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `gemini-explicit-text-signature-carriers` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `antigravity-pool-settings-cache-key` | upstream-equivalent | 已合并基线的等价实现与原回归保留；本阶段不退休。 |
| `claude-missing-tool-schema-default` | upstream-equivalent | 已合并基线的等价实现与原回归保留；本阶段不退休。 |
| `amp-session-extraction-priority` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `antigravity-response-json-schema-dialect-preservation` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `management-auth-refresh-status-redaction` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `responses-stream-error-flat-compatibility` | patch-still-required | 核对受影响路径并保留 LTS 条件、生命周期和回归；不以部分重叠取代完整行为。 |
| `responses-tool-output-item-id-disambiguation` | adapted | 按记录的 dev SHA 窄回补；尚未进入 upstream main，所列回归通过。 |
| `bounded-forced-auth-refresh` | adapted | 按记录的 dev SHA 窄回补；尚未进入 upstream main，所列回归通过。 |
| `antigravity-empty-text-stream-block-lifetime` | adapted | 按记录的 dev SHA 窄回补；尚未进入 upstream main，所列回归通过。 |
| `responses-same-chunk-reasoning-order` | adapted | 按记录的 dev SHA 窄回补；尚未进入 upstream main，所列回归通过。 |
