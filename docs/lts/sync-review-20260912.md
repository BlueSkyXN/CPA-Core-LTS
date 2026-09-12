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
