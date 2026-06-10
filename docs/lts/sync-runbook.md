# CPA-Core-LTS protected full-sync runbook

本文档说明 CPA-Core-LTS 后续如何由人工触发、AI 操作来稳定跟踪 `router-for-me/CLIProxyAPI`，同时保留完整 usage statistics、Management usage API、CPA-Panel-LTS 兼容和本仓库 downstream customizations。

## 目标

目标产品线是：

```text
CLIProxyAPI upstream/main latest
+ built-in full usage statistics / usage logging
+ /v0/management/usage* Management API
+ CPA-Panel-LTS compatibility
+ CPA-Core-LTS downstream customizations
```

本仓库不是普通 mirror fork。`main` 是唯一产品主线；`upstream/main` 只是只读同步坐标。正常维护方式是 manual / AI-operated protected full-sync，不安排自动同步。

## 禁止操作

- 不使用 GitHub `Sync fork`。
- 不在 `main` 上直接执行 `git pull upstream main`。
- 不使用 `git checkout upstream/main -- .` 或其他文件级覆盖方式同步。
- 不对 upstream sync PR 使用 squash 或 rebase。
- 不把普通产品代码变更和 `AGENTS.md` 变更混在同一个 PR。
- 不移除或降级 `internal/usage/`、`usage-statistics-enabled`、`/v0/management/usage*`。

## Preflight

先在主 worktree 检查真实状态：

```bash
cd /Users/sky/Github/CPA-Core-LTS
git status --short --branch
git fetch origin --prune
git fetch upstream --prune
git rev-parse origin/main
git rev-parse upstream/main
git merge-base origin/main upstream/main
git rev-list --count origin/main..upstream/main
git worktree list --porcelain
git branch --list 'codex/*'
git for-each-ref refs/remotes/origin/codex --format='%(refname:short)'
```

如果工作区不干净，先确认改动来源。不要覆盖或丢弃用户改动。

## Rehearsal

大范围同步前优先做一次不提交、不推送的 rehearsal：

```bash
date_tag="$(date +%Y%m%d)"
git worktree add -b "codex/rehearse-upstream-sync-${date_tag}" ../CPA-Core-LTS-rehearse origin/main
cd ../CPA-Core-LTS-rehearse
git merge --no-ff --no-commit --log upstream/main
mkdir -p local
git status --short > "local/rehearsal-status-${date_tag}.txt"
git diff --name-only --diff-filter=U > "local/rehearsal-conflicts-${date_tag}.txt"
git merge --abort
cd /Users/sky/Github/CPA-Core-LTS
git worktree remove ../CPA-Core-LTS-rehearse
```

`local/` 内容只作为本地记录，默认不提交。

## Staged sync decision

即使冲突不多，首轮或大批量同步也优先按 upstream first-parent SHA 分 2 到 3 段，原因是便于审查和 bisect。

出现以下任一情况时，必须 staged sync：

- upstream ahead 超过 50 commits，且包含 runtime、auth、stream、logging、management 或 pluginhost 重构。
- protected delta 相关路径出现文本冲突。
- request lifecycle、auth identity、logging、management response shape 同时受影响。
- merge 后需要手工重写 usage 调用链。
- targeted contract tests 或 `go test ./...` 出现多类失败，需要缩小定位范围。

分段依据优先使用 upstream first-parent SHA；不要按文件分段，不要逐个 upstream PR cherry-pick 普通维护改动。

## Sync workflow

每段同步从最新 `origin/main` 创建隔离 worktree：

```bash
git fetch origin --prune
git fetch upstream --prune
git worktree add -b codex/sync-upstream-stage-N ../CPA-Core-LTS-sync-stage-N origin/main
cd ../CPA-Core-LTS-sync-stage-N
git merge --no-ff --log <UPSTREAM_STAGE_SHA>
```

普通 upstream 改动默认吸收，包括 provider、model、translator、runtime、安全修复、crash 修复和 stream 修复。

下列 protected-adjacent 改动必须按 protected delta 路径审查，即使没有文本冲突：

- request lifecycle
- auth identity / API key attribution
- model resolution / alias normalization
- token accounting / final usage aggregation
- logging metadata
- Management usage response shape
- config schema / hot reload
- panel asset source / release source
- `internal/usage/`
- `/v0/management/usage*`

## Conflict policy

冲突解决原则：

- `internal/usage/`：保留 LTS 完整统计，必要时适配 upstream 新结构。
- `usage-statistics-enabled`：必须继续控制新 usage record 写入，不影响既有 snapshot/export/import 读取。
- `/v0/management/usage`、`/export`、`/import`：必须保留。
- usage record schema：保留 API key、auth file/source、model、token、latency、success/failure、auth index 等字段。
- Management usage response shape：保持 CPA-Panel-LTS 兼容。
- config schema：接收 upstream 新项，同时保留旧配置兼容。
- panel release source：保持 `BlueSkyXN/CPA-Panel-LTS`，除非用户明确改变策略。
- 如果 upstream 转向外置 usage service，可以适配新架构，但不能降级本仓库内置完整统计。

## Validation gate

sync PR 合入 `main` 前至少运行：

```bash
scripts/check-lts-contract.sh
go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'
go build -o test-output ./cmd/server && rm -f test-output
go test ./...
git diff --check
```

如果 `go test ./...` 因环境、网络或已知非本次改动原因无法完成，PR body 必须写明实际命令、失败位置和剩余风险。不能把未运行的检查写成通过。

最小行为级 contract tests 必须覆盖：

- usage record creation / schema：API key、auth source、model、token、latency、success/failure。
- Management usage response shape：`usage`、`failed_requests`、`apis`、`models`、`details`。
- export/import roundtrip：导出后导入仍保留核心字段。

## PR body checklist

每个 sync PR 必须写清：

- upstream repo / branch
- upstream from SHA
- upstream to SHA
- stage 编号和分段理由
- 冲突文件列表
- protected delta review
- 普通 upstream 改动吸收范围
- 被覆盖的旧 upstream-port PR 范围，如有
- validation 命令和结果
- `go test ./...` 状态
- HF Space smoke 是否执行，未执行则写明原因

Protected delta review 建议使用固定格式：

```text
Protected delta review:
- usage statistics: preserved / changed / retested
- management usage API: preserved / changed / retested
- CPA-Panel-LTS response shape: preserved / changed / retested
- downstream auth/config/panel/source customizations: preserved / changed / retested
- upstream ordinary changes: absorbed
```

## Merge policy

upstream sync PR 合入 `main` 时必须选择 Create a merge commit。

禁止：

- squash and merge
- rebase and merge
- 把 sync PR 拆成文件覆盖 commit

原因是 merge commit 会推进 merge-base，让 Git 记住本轮 upstream 历史和冲突解决。否则下一轮 sync 会重新计算已处理过的 upstream commits。

## HF Space smoke

Hugging Face Space smoke 适合证明真实部署基本可运行，但不能替代 contract tests。

推荐在 sync 合入后更新 `CPA_COMMIT`，对 `https://huggingface.co/spaces/BlueSkyXN/CPA-HFS-2026-03/` 做 smoke：

- `/healthz` 返回 ok。
- `/management.html` 可访问。
- `/v0/management/usage*` 存在且需要 management authentication。
- 日志确认运行在目标 CPA-Core-LTS commit。
- 日志确认 management asset 来自 CPA-Panel-LTS。

不要在 PR、日志或截图中暴露 management password、API key、auth file、OAuth token 或 Hugging Face secret。

注意：该 Space 的 HF Variables 会覆盖 Dockerfile 里的默认 `ARG`。只改 Dockerfile 或 Space repo commit 不等于已经部署目标 Core commit；必须同步更新变量并从 live runtime 反查。

推荐操作：

```bash
target_full_sha="<merged-main-sha>"
target_short_sha="${target_full_sha:0:8}"

hf spaces variables add BlueSkyXN/CPA-HFS-2026-03 \
  -e "CPA_VERSION=${target_short_sha}" \
  -e "CPA_COMMIT=${target_full_sha}"
hf spaces restart BlueSkyXN/CPA-HFS-2026-03
hf spaces info BlueSkyXN/CPA-HFS-2026-03
hf spaces logs BlueSkyXN/CPA-HFS-2026-03 --build -n 200
hf spaces logs BlueSkyXN/CPA-HFS-2026-03 -n 120
```

Live smoke：

```bash
space_url="https://blueskyxn-cpa-hfs-2026-03.hf.space"
curl -k -sS "${space_url}/healthz"
curl -k -sS -D - -o /tmp/cpa-management.html "${space_url}/management.html"
curl -k -sS -D - "${space_url}/v0/management/usage"
```

期望结果：

- `/healthz` 返回 200 且 body 包含 ok。
- `/management.html` 返回 200。
- `/v0/management/usage` 返回 401 missing management key，而不是 404 或路由消失。
- 响应头 `x-cpa-commit` 等于目标 `target_full_sha`。

## Cleanup

同步完成后清理临时 worktree：

```bash
cd /Users/sky/Github/CPA-Core-LTS
git worktree remove ../CPA-Core-LTS-sync-stage-N
```

PR 合入并确认远端状态后，再删除对应 local/remote `codex/sync-*` 分支。不要在合入前删除仍需要复核的分支。

## Current approval rule

维护模式可以批准，但下一轮 upstream sync 不应无条件合入。合入前必须至少具备：

- merge commit PR
- protected delta review
- sentinel contract check
- behavior-level usage / management tests
- server build
- `go test ./...` 或明确的失败说明
- 合入后的可选 HF Space smoke
