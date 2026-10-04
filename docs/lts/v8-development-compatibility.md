# v8 开发线兼容边界

本开发线从 v7 LTS `572f8fade2c11aa52bd06c59930b8dd89e1b748c` 开始，按 protected full-sync 接入官方 `2044a01f422998de79a5da8015141b878886534d`（v8.0.12），随后跟进到 `d7914afdedca7af95ee974a42453dc49fc1388ce`；11 条新增提交与保护审查见 [follow-up intake](v8-intake-20261003-followup.md)。`v8-dev` 是隔离评审目标，不是已发布版本；不得据此推断 main、UAT 或 PRO 已升级 v8。

## 配置与 API

- Go module 为 `github.com/router-for-me/CLIProxyAPI/v8`。模型调用仍使用 `/v1/responses`、`/v1/messages` 等原路径。
- `/v0/management` 的 LTS 完整 usage、Flow、插件/readiness 和结构化兼容 setters 保留。新增 `/v8/management/config`、`/config.yaml` 与配置子路径。
- v8 配置请求发送直接 JSON 值：PATCH 对象合并、数组整体替换，DELETE 显式删除，不使用 v0 的 `{"value": ...}` 包装。
- 读取 legacy 或 mixed 配置仅计算有效值，不隐式清理布局或写回。既有明文 management secret 哈希行为保留，修改正确的有效路径。显式 v8 保存才迁移磁盘。
- canonical 路径按存在性优先于旧字段，不能用真假值判断；false、0、空数组/对象与缺失不同。
- 客户端访问密钥在 `access.api-keys`；根 `api-keys` 映射为上游 provider groups，不能混用。
- `flow-control`、`ampcode`、`api-request-body-max-bytes` 保留原根路径，并通过 strict v8 校验。
- LTS Codex 的 `cache-affinity`、`identity-confuse`、`client-metadata`、`desktop-tool-overlay`、`model-fallback`、`rate-limit-continuity`、`abnormal-reasoning-retry` 迁移为 `upstream.codex.*`。原 `codex.*` 和早期 `oauth.providers.codex.*` 拼写可读取；这些设置不会因 API-key 配置过滤而被清空。
- `client.codex.optimize-multi-agent-v2` 是客户端共享能力。原拼写为 YAML 边界别名；只有已确认客户端和明确明文形状可重写，opaque/encrypted/mixed 内容不能当明文。

## 旧面板防误写

旧缓存 Panel 可能把根 `api-keys` provider map 当访问密钥数组覆盖。服务端拒绝通过 v0 raw `/config.yaml` 替换已有 v8/mixed 文档，也拒绝通过该入口提交新 v8 文档，返回 HTTP 409 `unsupported_config_layout`。v0 结构化 setters 仍按已有布局保存。

配套 Panel 的配置域识别布局：legacy 保持 v0；v8/mixed 读取 v8 canonical YAML 视图，保存使用 v8 YAML 入口。这不是全局 API 前缀切换；usage、Flow 和插件业务路由继续 v0。失败写入不自动 fallback 重放。

GET 不迁移磁盘，但 canonical 视图会规范化旧拼写；保存前差异预览针对该视图，实际保存会完成布局规范化。回退 v7 必须恢复原 legacy 配置，不能直接交给旧 Core v8 YAML。

## 配置条件写入

- v0 raw YAML 与 v8 配置 GET 返回 `ETag`：带双引号的 SHA-256，标识磁盘文档字节，不是 canonical 视图或某个子树的哈希。GET 不迁移磁盘；不存在子树的 404 也携带文档版本。
- 所有 v8 config PUT/PATCH/DELETE 必须发送刚读取的单个强 `If-Match`。缺少版本返回 428 `config_revision_required`；过期、弱标签、通配符、列表或重复头返回 412 `config_revision_conflict`，不写入。校验与持久化在同一 Handler 锁内完成。
- v0 raw YAML 可选支持 `If-Match`，不强制旧 v7 客户端升级；v0 结构化 setters 保持原接口。新 Panel 要求 v8 Core 提供版本，不能向不支持版本的旧 v8 候选盲写。
- CORS 暴露 `ETag`。成功写入不返回旧版本；客户端保存后重新 GET，不能凭冲突响应的新版本自动重放旧草稿。
- 这是单进程管理写入的并发保护，不是跨进程文件事务。已完成的外部文件修改可被检测，但任意外部进程同时写文件、无条件 v0 写者、多步骤 sponsor 修改、短写或进程中断仍不保证原子性。

## 统计、插件与 runtime

canonical usage export 仍为 v3；execution/trace IDs、日志 UUIDv7 不改变 dedup identity，Blue/standard/absent、token/timing、队列字段与归属保持。

Codex HTTP、WebSocket 非流式完成及 compact 路径在转换成功后才发布主请求成功 usage；空输出或 tool-input 转换错误返回 502，发布带上游 token 的失败记录，deferred failure 不重复发布。HTTP image-tool usage 继续作为独立上游消耗记录，主请求转换失败不抹掉已发生的工具消耗。此修复不改变异常 reasoning retry/finalizer 策略，也不声称所有 provider 转换路径均已审计。Responses 插件测试以独立 trace ID 关联本次请求，通过队列屏障复现并隔离前一用例的异步事件。

Codex API-key 请求创建独立配置视图，共享 affinity store/generation，不复制 `sync.Once`。新增 `host.routing.reset_cooldown` 回调必须显式授予 `auth-write`，默认拒绝。插件保持请求所有权、detach/drain、generation fencing、manual-only/readiness、permissions、config_yaml/config_json 能力。无法无损表示成 JSON 的插件 YAML 使用 YAML 入口；JSON GET 返回 422 `config_not_json_compatible`，不转换数字 map key 或无穷值。

零上游字节断流可在输出承诺前 failover；已见 protocol frame 的不完整响应保留 LTS request-scoped 边界。不能因本地转换错误换号或 cooldown。

本轮修复与验证边界见 [2026-10-04 review fixes](v8-review-fixes-20261004.md)，包括尚未通过的 authenticated GUI 回归；不能用 API 测试替代该验收。

## 交付与恢复

本轮仅提交 Core/Panel 开发 PR 到各自 `v8-dev`，不合 main、不发正式 Release、不升级 PRO。完整发布前仍须独立确认：Linux native/no-plugin 制品、四插件旧新交叉运行、完整 GUI、挂载/短写/进程中断故障注入、真实灰度与回退。

回退组合包括 v7 Core、配套 Panel/插件、原配置/auth 备份和 canonical v3 usage；真实凭据远端刷新不可通过复制本地 auth 文件保证逆转。不得在 PRO 可写目录运行灰度。
