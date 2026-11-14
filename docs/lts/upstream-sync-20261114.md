# 上游同步 intake（2026-11-14）

本轮为 V8 主线上的 protected full-sync 常规批次。Core 按 protected full-sync 分段合入官方历史；Panel 按 selective-port 吸收兼容增量。交付为 Core stack PR（#358/#359/#360，stack 361）与 Panel PR #150，均停在待审查。

## 基线与同步范围

| 对象 | 提交 |
|---|---|
| Core 起点 `origin/main` | `eb11126e5` |
| 上游 merge-base（真实同步起点） | `c2f402b45` |
| Stage 1 官方目标 | `681ce41b6` |
| Stage 2 官方目标 | `905637daa` |
| Stage 3 官方目标 | `1aaf56bc6` |
| 追加 wstunnel 修复层 | `7cfa267b2`（Closes #7504，连带 b285660f8/6acfb76b5/d6785c8da） |
| Panel 起点 `origin/main` | `21fc9dda` |
| Panel 官方审查目标 | `f9dcf358b` 所吸收的 #554/#555 两个特性提交 |

官方区间内已吸收主题：README 对齐（生态墙保持删除）、server/response writer、TUI、executor/translator 增量、quota-exceeded 示例迁移、config v8 迁移测试增量、wstunnel 新包及其 deflake 修复、standalone in-flight 观测端点、caller-provided interceptor host。

| 阶段 | 官方终点 | 本地 merge commit | 冲突文件数 |
|---|---|---|---:|
| Stage 1 | `681ce41b6` | `de875eeaf` | 6 |
| Stage 2 | `905637daa` | `e42d97424`（+reflow `f95879029`） | 16 |
| Stage 3 | `1aaf56bc6` | `66eca721c`（+race fix `b702834c1`，+L2 承接 `2a1d6b5a5`） | 30 |
| wstunnel 追加层 | `7cfa267b2` | `5405af492`（+L2 承接 `9bed67f68`） | 8 |
| Stage 1 夹具修复 | — | `bdfbb9ada`（L2 承接 `bb98c04cc`） | 0 |

`git merge-base --is-ancestor` 全链通过；全程 merge commit，无 squash/rebase。

## Protected delta review

| 契约与边界 | 本轮处理 | 验证依据 |
|---|---|---|
| usage 统计与 `/v0/management/usage*` | 未触碰响应形状；LTS 哨兵全量保留 | `scripts/check-lts-contract.sh` 绿；usage/management 定向测试绿 |
| LTS 精确后缀路由查找链 vs 上游 family-merge | Stage 3 冲突重放 LTS 查找链（`conductor_models.go`） | auth 定向测试 + race 绿 |
| 冷却分类（typed vs 裸 lifecycle 消息） | `shouldSkipCredentialCooldown` 例外收窄为仅裸 lifecycle 消息；executor 已 typed 的 transient transport 保持上游轮换契约 | conductor cooldown 测试组绿（待用户裁决项，见下） |
| scheduler-reject 轮次标记 | execute/stream 重试环补 `terminalRoundRejectedByScheduler` | auth 重试测试绿 |
| Flow V3 包装 vs 上游 in-flight tracker | 三个 Execute 入口保留 LTS wrapper，内部吸收 `beginInFlight`/`wrapInFlightStreamResult`/`contextWithInFlightScope` | `TestManager_InFlight*` 绿；race 绿 |
| README 商业中立 | 上游赞助商表（含 d6785c8da 的 Modelflare 行）继续不采纳 | 三语 README 无赞助商表 |
| config v8 mixed_explicit_zero | 保留 LTS 显式零值语义，恢复 LTS 侧断言（既有文档决策，不新开裁决） | `TestV8*` 全绿 |

## 验证摘要

- Core L1/L2/L3 每层：全量 `go test ./...`（111–112 包）绿、contract guard 绿、server build 绿；L3 追加 race（wstunnel+auth）绿、wstunnel deflake 组 `-count=10`×3 绿、executor 夹具目标组 `-count=30 -parallel 16` 绿。
- Panel：`validate:lts` 绿（新增 resetGrantChoice 三回归入 `test:auth-files`）；D1 交互 smoke 8/8（mock anthropic 端点，零真实 grant）；D2 两仓矩阵 3/3（候选×候选、候选Panel×mainCore、mainPanel×候选Core，真浏览器 smoke）。
- CI：`full-test-observation` 曾现的 executor 失败已定性为夹具缺陷并修复（见上）；wstunnel 失败由上游 7cfa267b2 修复吸收。

## 待用户裁决

1. quota-exceeded 示例注释化：示例复制后两开关有效值 true→false，测试断言已同步；裁决示例政策即可（推荐默认 vs 可见开关）。
2. Devin typed transient-transport：LTS 分类冷却面收窄、已 typed 错误恢复上游轮换；接受与否待裁决。

## 未吸收区间（下轮候选）

`1aaf56bc6..905e70c48` 中本批未吸收的 11 个提交：905e70c48（RE2 sensitive-patterns-replace）、f2efe2c41（antigravity 零输出 fail）、3b480776f（SIWC bind 代理）、5a351d957（TOS_VIOLATION 下线）、4da9f6852（429 终结冷却重置）、100f2de1a（model-directory 拦截器身份）、7904efd79（插件优先级暴露）、51b98e746（management secret 哈希缓存）、cf07c78ab（gemini/antigravity 零输出 fail）、5b63e29ac（OAuth 插件模型绑定）、7fd90c7aa（/muse-code 端点）。Panel 侧 defer 与 adapt-port 候选清单见 PR #150 描述与会话记录。
