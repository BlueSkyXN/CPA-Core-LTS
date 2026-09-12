# 2026-09-12 Core 分支收尾与七日 upstream 审查

## 范围与基线

- 七日窗口：`2026-09-05T05:52:48Z` 至 `2026-09-12T05:52:48Z`；冻结读取的 upstream main/dev 均为 `5b2785617d1e7de84a9f4dee599d275a4ccd8999`。窗口有 104 个非 merge 提交，其中 75 个已在原主线历史内，29 个非 merge 加 2 个 merge 本次纳入。
- 起始 LTS main：`a215f40c17e30230f976e2df6f43d0b4fd6440d9`，upstream merge-base：`09a29bd345bc44c473abe7fd07859e32df2ea543`。已纳入部分的 diff 分类见 `sync-review-20260907.md`、`sync-review-20260908.md`、`sync-review-20260911.md`，不重复 cherry-pick。
- 仅一个干净 root worktree，空 stash、零开放 PR；五条本地及云端分支均无独有提交：usage-query-v1 (#254)、pat-provider-delivery (#255)、pat-publish-recovery (#256)、20260911-stage1 (#257)、20260911-stage2 (#258)。不是未完成代码，不重复合并或删除。
- 从最新 origin/main 建隔离工作树，阶段一 merge 到 `5c80a01c`（15 commits），阶段二到 `5b278561`（16 commits）。request lifecycle、auth identity、usage、model、plugin 与 signature 同时受影响，因此分段。
- 本次不发布、部署、不修改线上凭据、不迁移用户数据、不删除旧分支或恢复对象；配套 Panel 不作无关改动。

## 阶段一逐提交 diff 结论

| Upstream | 分类 | 实际处理 |
|---|---|---|
| `c8ecb4f3` | upstream-equivalent | 昨天已回补 reasoning-before-content；此次只纳入 ancestry，保留全部回归。 |
| `fd3e6623` | upstream-equivalent | 空文本不关闭 thinking block 已有同实现；不重复修改。 |
| `9fad5055` | adapted | 520–526 是 origin failure，不因 HTML/Cloudflare 字符串误当 WAF challenge；支持正 RetryAfter，保留默认 30 秒、禁用语义、typed request fault、xAI unauthorized、quota 延长与 transient 分离。 |
| `b8e6ec0a` | absorbed | 中断及并行工具调用补齐明确的 interrupted/no-output 占位，保留真实结果和顺序；不改变 LTS 显式 signature carrier。 |
| `6dce7867` | upstream-equivalent | 强制 refresh worker 上限与取消已回补；只纳入 ancestry。 |
| `5a07045e` | upstream-equivalent | fco_ output item ID 不等于 call_id；已有修复和 Gemini/Antigravity 配对回归保留。 |
| `c8f723e0` | adapted | SDK usage、plugin usage、auth summary 新增 BaseURL；去掉 URL userinfo/query/fragment，保留普通 endpoint 路径。没有把它加到 canonical v3 snapshot/import/export 或 queue dedup identity。 |
| `8461b4e9` | absorbed | Codex 0.154.0 / macOS 26.5.2 默认身份；更新相关断言，保留 caller override、最终 identity 和 WS profile 绑定。 |
| `8f23ad02` | adapted | alias 使用 canonical template 且按实际 provider 限制能力；LTS Ultra overlay 不得重新扩宽用户显式 thinking。普通未覆盖的 Codex Ultra 保留。 |
| `4efcac79` | absorbed | Responses → Interactions 识别 incomplete terminal 并传递真实 status，不将截断响应标为 completed。 |
| `2912516c` | adapted | additive ResultPolicy 在 manager 锁外、状态写入前调用，默认 nil 不改变行为；继续经过 LTS generation、continuity 和 deferred persistence，不替换 usage/Flow finalizer。 |
| `4cd17293` | adapted | OpenAI tool content 保持字符串，图片交接为 user image parts；显式 text-only 模型仍替换图片为 omission marker，图片能力未声明时保留图片。 |
| `fc96a87f` | adapted/divergent | 新身份使用组织哈希避免撞名；匹配现存 legacy 身份则原位保存并保留 ID/FileName/Index，不自动改名删除。disabled metadata、显式 login creation intent 与安全 store path 保留。 |
| `942bda99` | reject behavior / retain ancestry | 未知非空签名不能凭 compatibility mode 变成可跨 provider 重放的 encrypted_content；保留空 thinking 兼容与已确认 GPT/Grok 特例。上游测试改为验证拒绝未知签名且不丢周围文本。 |
| `5c80a01c` | absorbed | Gemini audioTranscription text 映射到 Chat 文本，stream/nonstream 一致；不冒充新增 Realtime/transcription endpoint。 |

冲突：fetch_codex_models 注释、config 示例、usage reporter/adapter、ObjectStore、两组配对测试、FileStore 测试和 conductor_cooldown。均按字段或逻辑块合并：保留 canonical timing v1、全部 tier/provenance 字段和 secureReadAuthFile；未整文件选边。

## Protected delta review

- retained capabilities：完整 usage、canonical v3、timing v1、Management `/usage*`、Panel 默认发布源、auth/config 兼容保留；BaseURL 只扩展上游 SDK/plugin metadata，不改变 Panel/TUI snapshot shape。
- LTS-owned features：Flow、fallback、abnormal retry、rate-limit continuity、Redis queue、Desktop tool overlay 保留。ResultPolicy 是可选嵌入点，不是自动配置的插件策略，不绕过 generation 检查。
- shared review seams：UA/header、alias metadata、tool pairing/image relay、transient classifier、login/store 与 usage adapter 按上表适配；未触及 Home 协议、count endpoint、Management logs、request spool 或 full usage 存储实现。
- downstream patches：原有 required 条目均继续 required；本轮没有完整替代 fallback/continuity/privacy/Flow/replay 或 store containment 的上游实现。四项昨日 upstreamed 回补在当前 main baseline 等价且回归通过，进入 removable；旧四项 removable 保留，不删除已成为 shared 的代码来追求标签退休。
- 新登记四项具体差异：usage-base-url-credential-redaction、claude-legacy-credential-identity-preservation、codex-compat-unknown-signature-rejection、text-only-relayed-tool-image-guard。introduced_in 指向实际 merge implementation commit，不改写历史。
- validation profiles：只做本地与 CI 源码验证；未运行真实账号、Home/HF 部署或浏览器 UAT，不将单元测试描述为这些层已验收。

## 阶段一验证

首次全量测试揭示三类需要适配的断言/行为：旧 UA 固定值、非 Codex provider 的 service_tiers、图片 relay 与 text-only/显式 thinking 约束。已修正生产适配和对应回归，没有删除测试或放宽受保护契约。

修正后 `go test ./...`、过滤 `/tmp` 的无缓存产品全量、Usage/Management 专项、完整 Responses translator、六包 race（auth conductor、SDK auth、store、pluginhost、usage helpers、client models）、server build、LTS guard 通过。复查还补齐 ResultPolicy 丢弃/改写身份时的 continuity attempt 释放，沿用现有 abandon 路径并增加定向回归。最终精确 head CI 在阶段 PR 中回读；不会把后续阶段未执行的验证记为通过。

首轮 CI 的 full-test-observation 发现旧 Antigravity pool fixture 只同时启动 goroutine、未保证请求同时占用连接，Linux 实际观察 9 条连接而断言最多 8 条。为大池和默认小池两项测试加入每波 8 请求的服务端屏障，保留原连接数阈值，不修改生产 transport 或放宽断言。最终 CI 需在这个修正后的精确 head 重新通过。

## 阶段二逐提交 diff 结论

阶段一 [#259](https://github.com/BlueSkyXN/CPA-Core-LTS/pull/259) 已通过 merge commit `fe1c960a43f1d93e4c7c9fdbe1b676c7249954b9` 合入；精确 head `b395a4b1` 的 7 项 CI 全部通过（含 full-test-observation、Windows 凭据权限、provider connectors）。第二阶段从该 origin/main 开始，使用 `git merge --no-ff --log` 纳入剩余历史。

| Upstream | 分类 | 实际处理 |
|---|---|---|
| `d1702fdf` | adapted | watcher monotonic revision 阻止旧更新/删除覆盖新状态；保留 LTS private generation/provider fence。已持久化状态同步不依赖请求仍未取消；Management status/fields/upload 的 hook 错误不回显原始内容。 |
| `54776fb3` | adapted/divergent | 接受具有已识别 protobuf/Tink 结构的 Gemini server-tool signature；不采用 toolCall/toolResponse 一律绕过校验。有效 Gemini 签名保留，未知/跨 provider 签名仍拒绝；显式 carrier 和 functionResponse 规则不变。 |
| `4edf9d1d` | adapted / existing alternative | 请求侧、translated 及 xAI usage 扩展复用 LTS InspectResponsesControls；保留 suffix 优先和 top-level wire baseline。compaction 清除旧 effort，畸形/相邻 update 不构成有效证据，不引入另一套宽松解释器。 |
| `75ce6352` | absorbed | CAQS envelope 的 container signature bytes 与 thinking/narration 结构校验；不将“能 base64 解码”当成可移植签名。 |
| `377c315f` | absorbed | Claude billing fingerprint 固定首轮用户文本，continuity tags 统一解析；保留明确 execution metadata 和 caller tags 的区分，不跨凭据共享状态。 |
| `8bd67f33` | absorbed | Kimi K2.8/code 目录、别名和 temperature 规则，Chat/Responses 两条路径一致；保留 delegated-auth clone 和 K3 独立能力。 |
| `e88cd947`、`ae8f1f8b` | reject presentation / retain ancestry | 三语赞助内容与 rapidproxy.png 不进入产品树；商业中立 README 原样保留。 |
| `b5ba02c2` | adapted | 大 WS payload 分 chunk 写，Pong 使用可并发 WriteControl；无 session 请求使用临时 session reader。保留 LTS connection generation/key、typed handshake、原始帧分发、取消唤醒、失败 usage 和已输出后不重放。 |
| `cd1e8e10`、`c2562d8a` | absorbed | Gemini Chat/Responses 输出总数包含 candidates + thoughts，reasoning 仍是子项，不额外加到 total。compaction 测试同步 inclusive-output 断言。`9781fe46`、`137db720` 保留 merge ancestry。 |
| `456d4c37` | adapted | 凭据确实改变才清 unauthorized；补齐旧 auth deadline 清除，不因普通 quota 错误文本包含 unauthorized 而清掉模型额度。plan_type 从 metadata/JWT 同步，provider 更换仍清独立 runtime generation。 |
| `d9b8fdb7` | absorbed | direct Anthropic 转发非 managed caller betas，managed beta 仍走现有门禁，保留 custom gateway 与 confirmed-client 区分。 |
| `5b278561` | adapted | model-list response 进入既有 plugin interceptor/lifecycle；响应头经过共享过滤且不覆盖 CPA-owned 字段，实际 Write 后按结果完成一次 lifecycle。序列化失败只返回固定错误。 |

文本冲突：三语 README、Codex WS execute/session/stream、xAI WS invalidation、thinking、auth lifecycle/refresh。保留 generation-aware 状态机，不把旧 upstream conn 指针调用覆盖到 LTS connection ref 上；上游新增 chunking fixture 同步使用真实 generation-aware reader。

## 第二阶段复查与验证

- 复查发现旧 Claude setup-token 判定的三个 bool metadata 裸读与 profile writer 竞争；沿用 claudeDevicePoolMu 增加同锁读取，原 shared-credential 回归 race 连续 20 次通过，没有增加第二把锁或串行化网络请求。
- Management 一个 parallel test 再次写 gin.SetMode，与其他测试的 Gin 初始化竞争；删除重复设置，保留已有 TestMain 的统一设置及并行测试。
- 新 upstream scheduler fixture 只写 Quota、不写真实 availability 状态，不符合 LTS 状态契约；改经 MarkResult 制造真实 credential-scoped 429，再断言 hook 同步后模型仍登记、scheduler 仍阻止调用。
- 无缓存产品全量通过；原始 `go test ./...`、最终 build/guard/registry 在候选 head 再核验。第一次失败和修正内容均如上保留，不用忽略失败或单独重跑凑绿。
- race 覆盖 auth、Service、Flow、watcher 全树、signature、SDK handlers、API 全树；executor/Management 的上述竞争修正后分别整包 race 通过。Usage/Management 专项、完整 Responses translator 通过。
- 上游 dev 与 main 在冻结时相同，无新的 dev-only backport。`feat/devin-provider` 的近期 18 项是独立新 provider、OAuth、配置、日志与目录开发线，尚未进入 main；defer，不整体吸收开发线或把反向 tree diff 当作主线回归。
- 既有 91 个不可达 commit 最新为 2026-09-05，未出现上次 09-11 恢复对象核查后的新对象；不恢复已被主线替代的旧集成快照。
- 未做正式 Release、部署、真实账号生成、Home/HF 操作或浏览器 UAT。该边界不能由本地测试、PR merge 或 CI 成功替代。


## Downstream patch 逐项结论

两个阶段均对照 from/to diff 与 ledger 所列回归；removable 不表示要删除 upstream-shared 生产实现。

| Patch | 结论 | 核查依据 |
|---|---|---|
| `responses-effort-summary-independence` | patch-still-required | usage effort 解析不改变显式 summary intent；effort 不自动开启 summary。 |
| `codex-model-fallback` | patch-still-required | ResultPolicy/520 分类不是跨模型 fallback；typed trigger、目标重选和已交付门禁保留。 |
| `codex-rate-limit-continuity` | patch-still-required | 新增 hook 不替代 incumbent/fresh/canary；suppression 额外释放原 attempt。 |
| `codex-interactions-service-tier-response` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `plugin-configured-enable-default` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `codex-gpt56-ultra-level` | patch-still-required | alias/provider 约束吸收；显式 thinking 优先，默认 Codex Ultra overlay 仍需保留。 |
| `codex-model-header-provider-snapshot` | patch-still-required | UA 更新不替代最终 model/auth/header profile 连接 key。 |
| `codex-oauth-client-identity-finalization` | patch-still-required | 同步默认 UA，但 caller override、控制字符与最终 OAuth identity 规则仍保留。 |
| `codex-client-metadata-privacy` | patch-still-required | provider 功能及 usage 新字段不替代 canonical carrier/privacy policy；BaseURL 另行脱敏。 |
| `codex-model-not-found-request-scope` | patch-still-required | 新 520/401 修复不改变请求作用域 404，不惩罚其他凭据。 |
| `codex-websocket-reader-generation` | patch-still-required | chunking/ephemeral reader 适配 connection ref；旧 reader、满 channel 取消与终止帧仍需本地 fence。 |
| `codex-spark-reasoning-summary-compat` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `xai-explicit-tool-choice-none` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `kimi-claude-delegated-auth-immutability` | patch-still-required | K2.8/temperature 新增，delegated Claude Auth 仍必须 clone。 |
| `request-body-panic-tempfile-cleanup` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `api-request-body-size-limit` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `object-store-auth-path-containment` | patch-still-required | 创建 disabled credential 的显式意图不绕过 secureReadAuthFile/root containment。 |
| `auth-token-private-file-permissions` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `auth-store-list-error-propagation` | patch-still-required | 新 legacy identity 查询依赖 List 错误传播，不能忽略 backend 读取失败。 |
| `auth-store-metadata-hydration` | patch-still-required | 新组织哈希与 creation intent 保留 hydration、disabled 和属性来源。 |
| `panel-release-token-isolation` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `gemini-unknown-method-not-found` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `count-tokens-not-found-no-cooldown` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `management-logs-bounded-response` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `transient-eof-cooldown` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `expired-availability-pruning` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `responses-chat-tool-call-turn-grouping` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `codex-desktop-tool-overlay` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `codex-multi-agent-plaintext-contract` | patch-still-required | 新签名分类不授权 opaque agent_message；继续要求可证的明文和 namespace provenance。 |
| `xai-multi-agent-plaintext-provenance` | patch-still-required | 相关路径的上游变更见前述逐提交表；未提供本项契约的完整等价替代，保留原回归。 |
| `provider-runtime-state-isolation` | patch-still-required | 凭据更新恢复 401 不等于 provider 更换；继续清除旧 provider runtime，保留独立 quota。 |
| `auth-provider-generation-fence` | patch-still-required | watcher revision/RegistrationEpoch 与私有 execution generation 分层共存。 |
| `codex-live-bootstrap-accounting-and-egress` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `xai-implicit-responses-message-token-accounting` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `xai-websocket-saturated-reader-invalidation` | patch-still-required | keepalive 不替代饱和 read channel 的先取消后通知顺序。 |
| `xai-model-bad-credentials-auth-normalization` | patch-still-required | 401 credential reset/520 分类与 xAI bad-credentials 错误码归一化是不同层。 |
| `dynamic-custom-header-transport-coverage` | patch-still-required | unmanaged beta 扩展不替代 caller-owned 动态 header 展开和 direct image 覆盖。 |
| `astra-native-responses-controls` | patch-still-required | 统一复用现有严格 parser；不采用忽略 compaction/畸形 update 的上游扫描器。 |
| `astra-async-guidance-hotfix` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `local-flow-control` | patch-still-required | quota/result hook/watcher 顺序不替代本地 request+attempt admission 和生产端释放。 |
| `openai-claude-cache-write-accounting` | patch-still-required | Gemini thoughts 加入 output 不替代 Claude input/cache read/write 的防双计。 |
| `plugin-stream-history-capability-negotiation` | patch-still-required | 新 model-list interceptor 不替代 schema 5 lifecycle/history 显式协商。 |
| `wsrelay-terminal-frame-preservation` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `codex-dotted-collaboration-tool-restoration` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `session-dispatch-context-after-selection` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `antigravity-compaction-completion-validation` | patch-still-required | token 输出改为 inclusive；摘要必须完整、非空、非截断的判断保留。 |
| `gemini-explicit-text-signature-carriers` | patch-still-required | server-tool native envelope 和 tool pairing 是另一问题；显式 text carrier 不改为隐式缓存。 |
| `antigravity-pool-settings-cache-key` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `claude-missing-tool-schema-default` | upstream-equivalent / removable | 同等 schema default 已在主线；image relay 不替换该能力，保留 shared 测试。 |
| `amp-session-extraction-priority` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `antigravity-response-json-schema-dialect-preservation` | patch-still-required | 本轮 from/to 未触及登记的实现路径，没有对应等价修复；保留既有行为。 |
| `management-auth-refresh-status-redaction` | patch-still-required | 刷新状态白名单仍保留；新同步 hook 错误脱敏另行登记。 |
| `responses-stream-error-flat-compatibility` | patch-still-required | 新 model-list 和 Interactions incomplete 不替代 nested+flat error 并存。 |
| `responses-tool-output-item-id-disambiguation` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `bounded-forced-auth-refresh` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `antigravity-empty-text-stream-block-lifetime` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `responses-same-chunk-reasoning-order` | upstream-equivalent / removable | 等价实现已在本轮或先前稳定 baseline，原回归保留；不删 shared 实现或越级退休。 |
| `usage-base-url-credential-redaction` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `claude-legacy-credential-identity-preservation` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `codex-compat-unknown-signature-rejection` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `text-only-relayed-tool-image-guard` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `gemini-server-tool-signature-validation` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `model-list-response-boundary` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `credential-change-unauthorized-recovery` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `management-auth-sync-error-redaction` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
| `claude-setup-token-metadata-lock` | patch-still-required | 本次复查确认的具体保留/适配差异；实现 SHA、回归和退休条件已登记。 |
