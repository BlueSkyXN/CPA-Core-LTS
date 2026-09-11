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

## Downstream patch review

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
