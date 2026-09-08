# 2026-09-08 分支收尾与七日 upstream diff 审查

## 边界

- 审查窗口：2026-09-01 至 2026-09-08；上游 main 固定为 `d198db54d4c4886c99b21488d54fc576933019a3`，dev 固定为 `68dd99d56f6867062547f80acf6e2b22f0766b58`。
- 起点：LTS main `094a6e992d82731b0da947c02d285861533c433b`，已合并 upstream `934fb792`。已吸收的七日历史以 [前次逐提交审查](sync-review-20260907.md) 为基础，并核对当前 ancestry；不把 upstream 提交数当作未完成工作数。
- 本次增加 13 个 upstream commits（含一个 merge）。预演发现 protected-adjacent 冲突，分两阶段：第一阶段到 `f19d6da0`，第二阶段到 `d198db54`。每阶段从已验证主线推进，使用 merge commit。
- 不包含 tag、Release、部署、付费 provider 验收或删除 Git 恢复对象。

## 自有工作逐项处理

| 对象 | 当前证据与处理 |
|---|---|
| `codex/handoff-desktop-tools-20260907` | #249 已合并，head `04f54976` 是主线祖先，工作区无 tracked/untracked 改动；属于收尾候选，不重复合并。 |
| `codex/sync-upstream-20260907-stage1` | #250 已合并，head `d145afde` 是主线祖先，工作区无 tracked/untracked 改动。 |
| `codex/sync-upstream-20260907-stage2` | #251 已合并，head `4e43f878` 是主线祖先，工作区无 tracked/untracked 改动。 |
| 主工作区 session WIP | `219812ef`：先提交选择，再把 canonical/parent session 写入执行 context；覆盖 Execute、Count、Stream 与 401 refresh。保留 Flow admission 与原有 attribution。 |
| 主工作区 compaction WIP | `d3f275eb`：保存原始 finishReason；仅 STOP 且非空摘要可封装 capsule；覆盖 Gemini/Claude 三条入口，不修改调用方历史。 |
| 主工作区 provider/runner WIP | `dc9da190`：CodeBuddy 在 DONE 时关闭；Qoder 完整消息补齐未流出的后缀、不重复最终答案，保留 cache/reasoning 子项及 inclusive totals。新增字段均可省略，不更改 AgentEvent 版本或旧必填字段。 |
| 远端 PR | preflight 无 open PR；最近七日更新的自有 PR 均已合并。第三方 #238 已由 #244 接管，不恢复旧分支。 |
| 历史恢复对象 | fsck 的 unreachable 不等于活跃分支。近期候选的 runner、refresh、Astra、release、timing 已有主线交付；未执行 GC、prune 或恢复旧 timing 语义。 |

## upstream main 新增 diff 判断

| Commit | 分类 | 判断及保护边界 |
|---|---|---|
| `510c9c8f` | divergent：保留显式 carrier | 新方案把签名存入 model/message-ID replay cache，并从客户端 timeline 隐藏。缓存写入成功不保证下一请求未过期、未淘汰或落在同一 worker；LTS 保留显式 carrier 和 stripped-ID/顺序回归，不引入该隐式依赖。 |
| `82f4f370` | already-equivalent | 点号 collaboration 工具名修复前次已回补；保留 LTS 明文 provenance/namespace 门禁。ledger 先进入 removable，不越级退休或删除仍必需的 upstream 实现。 |
| `d5397905`、`f19d6da0` | adapt / upstream-shared | 吸收可配置连接池、短连接默认、idle timeout 上限、429 idle 清理和 reload purge；保留 LTS usage reload、Flow 及 Amp 配置回归。未给已建立的 stream 新增 timeout。 |
| `bf20b999` | 待第二阶段验证 | 只对可证明等价的纯常量 union 转 enum；零输出 incomplete 是 request-scoped 故障，不等同于 LTS abnormal reasoning retry。检查 HTTP/WebSocket 各入口与首 payload 边界。 |
| `4f038099` | 待第二阶段验证 | 客户端明确提供 JSON format 时转为 system prompt 指令，是尽力的输出格式提示，不是原生 JSON Schema 强约束保证；保留明文 agent_message 处理。 |
| `7871a5a9` | already-equivalent，第二阶段纳入 ancestry | LTS 已有相同 wsrelay 实现；不是 Codex/xAI upstream reader generation fence 的替代品。 |
| `8696585c` | 待第二阶段适配 | 透传实际 service tier、有效 cache-write 整数及 legacy alias；保留 LTS token-breakdown shaping，不用请求 tier 冒充实际 tier，不重复计费。 |
| `d01516c1` | 待第二阶段吸收 | Chat function strict 缺省显式转 false，显式 true/false 保留；不改变 Responses 原生请求的默认。 |
| `03a054e3` | 待第二阶段吸收 | 只在当前请求恰好一个匹配声明时恢复省略的 namespace；精确名称优先，歧义保持不猜。 |
| `bee20b99` | 待第二阶段吸收 | 工具名合法化与冲突消歧覆盖声明、tool_choice、历史调用及返回还原；不重写用户工具含义。 |
| `ba7e5583` | 待第二阶段适配 | 上游明确 server_error + retryable message 的 bootstrap failover；保留 LTS typed cooldown、continuity 和已交付输出后的重试限制。 |
| `d198db54` | already-equivalent：commercial-neutral | 上游删除推广；LTS 早已没有该推广，保留 ancestry 而不恢复其他商业内容。 |

## dev 独立变更

`68dd99d5` 作为窄回补进入第一阶段：resolved pool settings 必须进入 transport cache key，避免热重载 purge 后旧请求重新填入旧配置。引入 commit `a715ad97`，登记 upstreamed 并保留对应回归；不整体 merge dev。

其余 dev 的 Kimi Responses 新协议、Claude helper/beta 及 Gemini response schema 转换尚未纳入 main，不与已稳定修复混为同一件事；应按各自 diff 和现有合同继续分类，不以 dev HEAD 冒充已接受稳定基线。

## Protected delta review

- retained capabilities：full usage、Management `/usage*`、canonical v3、timing v1、Panel asset source 和 auth/config 兼容均保留。
- LTS-owned features：Flow、abnormal retry、fallback、rate-limit continuity、desktop overlay、plugin lifecycle 保留；schema/transport 更新不替代它们。
- shared seams：重点验证 config clone/reload、executor stream 收尾、cache/replay、session attribution、公开 AgentUsage 加法字段。
- downstream patches：仅本次证明等价的 dotted restoration 进入 removable；session context、compaction integrity、显式 Gemini carrier 仍 required，连接池 settings-key 回补为 upstreamed。其他补丁沿前次逐项结论保留：本次差异未提供同等的 LTS 行为。
- Panel：新增 connection-pool 子树通过现有完整 YAML Document 保存；不为了覆盖新 key 强加一套 UI。

## 验证

原有 WIP 首轮：executor、auth conductor、pluginapi、pluginhost 四包通过；CodeBuddy/Qoder 两个嵌套 Go module 通过；Qoder runner 43/43 通过。阶段集成后的全量、race、契约及构建结果在 PR 中逐项回读；本文件不把尚未运行的检查写成通过。
