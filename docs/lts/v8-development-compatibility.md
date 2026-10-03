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

## 统计、插件与 runtime

canonical usage export 仍为 v3；execution/trace IDs、日志 UUIDv7 不改变 dedup identity，Blue/standard/absent、token/timing、队列字段与归属保持。

Codex API-key 请求创建独立配置视图，共享 affinity store/generation，不复制 `sync.Once`。新增 `host.routing.reset_cooldown` 回调必须显式授予 `auth-write`，默认拒绝。插件保持请求所有权、detach/drain、generation fencing、manual-only/readiness、permissions、config_yaml/config_json 能力。无法无损表示成 JSON 的插件 YAML 使用 YAML 入口；JSON GET 返回 422 `config_not_json_compatible`，不转换数字 map key 或无穷值。

零上游字节断流可在输出承诺前 failover；已见 protocol frame 的不完整响应保留 LTS request-scoped 边界。不能因本地转换错误换号或 cooldown。

## 交付与恢复

本轮仅提交 Core/Panel 开发 PR 到各自 `v8-dev`，不合 main、不发正式 Release、不升级 PRO。完整发布前仍须独立确认：Linux native/no-plugin 制品、四插件旧新交叉运行、完整 GUI、挂载/短写/进程中断故障注入、真实灰度与回退。

回退组合包括 v7 Core、配套 Panel/插件、原配置/auth 备份和 canonical v3 usage；真实凭据远端刷新不可通过复制本地 auth 文件保证逆转。不得在 PRO 可写目录运行灰度。
