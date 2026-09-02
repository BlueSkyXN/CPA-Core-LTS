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

## Contract registry

Panel 的实践证明，单靠“这几个文件不能删”的文字规则不够；需要把产品能力、维护功能、临时补丁和审查边界拆开，再让脚本和 PR checklist 同时引用。Core 使用 version 2 contract registry，定位如下：

- `docs/lts/protected-deltas.yaml` 定义 LTS 产品边界和同步策略。
- `docs/lts/core-feature-contracts.yaml` 把 `retained-capability`、`lts-feature`、`review_seams`、`validation_profiles` 和能力关系分别登记。
- `docs/lts/downstream-patches.yaml` 只登记需要随 upstream baseline 复查和退休的下游补丁；普通 upstream model/catalog 吸收不登记为 LTS feature。
- `go run ./scripts/ltsregistry --root .` 严格解析三份 YAML，检查枚举、引用、路径、marker、补丁测试和退休条件。
- `scripts/check-lts-contract.sh` 保留少量产品身份 sentinel，并调用结构化 validator。
- `.github/pull_request_template.md` 负责让评审者显式说明是否按 registry 做了 protected delta review。

分类字段的含义：

- `kind: retained-capability`：LTS 产品身份的一部分，例如完整 usage、Management usage API、Panel 兼容和 auth/config 兼容。
- `kind: lts-feature`：CPA-Core-LTS 长期维护但不提升为核心身份契约的功能，例如 abnormal reasoning retry、transient cooldown 和 Redis-compatible usage queue。
- `support`：表达支持强度，使用 `protected / maintained / optional / upstream-shared`，不再用单一 `status` 混合产品重要性和来源关系。
- `owner`：表达维护归属，使用 `cpa-core-lts / upstream / shared`。
- `upstream_relation`：表达与 upstream 的关系，使用 `downstream-only / divergent / upstream-equivalent / removed-upstream`。
- `review_seams`：只表达每次 sync 必须人工审查的边界，不伪装成产品 feature。
- `validation_profiles`：只表达运行态交付证据，不伪装成长期维护能力。
- `relationships`：表达 coexist 等能力关系；Redis queue 与 built-in full usage 的关系不再挤进支持级别。

`redis-compatible-usage-queue` 的 optional 只表示部署时可以不启用该外部集成；其 route、代码和 wire contract 仍属于必须保留的 LTS 维护面。`commercial-neutral-documentation` 则记录 README 和仓库展示资产不接受自动恢复的赞助、返佣或付费推荐内容。

和 Panel 不同，Core 的正确模式仍然是 protected full-sync，不是 selective-port。原因是 Core 的受保护 delta 主要集中在 usage、Management API、panel asset source、auth/config attribution 等明确接口上；普通 provider/runtime/security upstream 变更默认吸收，只在 registry 标出的 protected-adjacent seam 上做保留或适配。

保持这套方案轻量的规则：

- 只为稳定 contract 增加 sentinel：route、config key、JSON 字段、release asset、目录边界。
- 不为普通函数名、局部变量、临时实现细节增加 marker；这些应该由 tests 或人工 review 覆盖。
- `scripts/check-lts-contract.sh` 只能证明“保护面没有明显消失”；不能替代 usage schema、response shape、import/export、runtime attribution 的 Go 测试。
- 每次新增 registry feature，都要写清为什么它是 LTS 产品边界，而不是单次 sync 的临时关注点。
- 每个 active downstream patch 都必须有 affected upstream range、真实存在的 regression tests 和明确的 `retire_when`。

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
git fetch origin --prune --tags
git fetch upstream --prune --tags
git rev-parse origin/main
git rev-parse upstream/main
git merge-base origin/main upstream/main
git rev-list --count origin/main..upstream/main
git tag --points-at "$(git merge-base origin/main upstream/main)"
git describe --tags --always upstream/main
git worktree list --porcelain
git branch --list 'codex/*'
git for-each-ref refs/remotes/origin/codex --format='%(refname:short)'
scripts/check-lts-contract.sh
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

审查时先对照 `docs/lts/core-feature-contracts.yaml` 的 retained capability、LTS feature 或 review seam：

- `full-usage-statistics-core`
- `management-usage-api`
- `panel-release-asset`
- `auth-identity-attribution`
- `config-compatibility-and-hot-reload`
- `redis-compatible-usage-queue`
- review seam `provider-runtime-usage-seams`

同时逐项检查 `docs/lts/downstream-patches.yaml`。普通 GPT/model catalog 更新属于 upstream/shared absorption；只有 LTS 仍需携带的差异才进入 patch ledger。

## Downstream patch lifecycle

Patch ledger 使用以下状态：

- `required`：当前 upstream baseline 尚无等价实现，本地代码和 regression tests 必须存在。
- `upstreamed`：upstream 已合并等价实现，但尚未进入当前 LTS sync baseline。
- `removable`：当前 baseline 已含等价实现；必须先用 ledger 中的 tests 验证，再删除本地实现。
- `retired`：本地实现已删除；条目保留并记录 `retired_in`，用于历史追溯。

每次 protected full-sync 都执行：

1. 对照 upstream from/to range 搜索 active patch 的等价实现或 upstream PR。
2. 逐项记录 `patch-still-required / upstream-equivalent / retired`，不能因为无文本冲突而跳过。
3. 如果等价实现只在 upstream dev 或未进入本轮 baseline，状态改为 `upstreamed`，不能提前删除。
4. 如果当前 baseline 已包含等价实现，先把状态改为 `removable`，在代码仍存在时运行原 regression tests，并把该状态作为一个独立 PR 或已合并基线。
5. 后续 retirement PR 先提交删除 downstream 实现的 commit，再提交 ledger commit，把状态从 `removable` 改为 `retired`，并令 `retired_in` 指向前一个删除 commit；最终 PR head 必须重新通过 validator。历史条目不得删除。
6. 没有等价 upstream 修复时保持 `required`，必要时另行准备 upstream issue/PR，但不把发布 upstream PR 混入 sync 操作。

Validator 会把当前 ledger 与 `--base-ref`（CI 使用 PR base branch）比较，禁止删除历史条目、越级退休、改写 `introduced_in` 或已退休记录。`upstreamed / removable` 可以在 upstream 修复被撤回或等价判断被推翻时保守回退为 `required`，但不能绕过验证直接前进。`upstreamed / removable / retired` 必须提供具体 upstream issue、PR 或 commit。`retired_in` 必须填写实际删除 downstream 实现且可达于当前产品历史的 commit；由于 `required -> retired` 和 `upstreamed -> retired` 都被禁止，补丁发现等价实现、进入 `removable`、最终退休不能压缩成同一个未合并基线。

如果同一 PR 内产生 ledger 引用的实现 commit（`introduced_in`）或删除 commit（`retired_in`），该 downstream patch PR 必须使用 **Create a merge commit**，禁止 squash 或 rebase；否则被引用的 SHA 会被改写并立即失效。若实现 commit 已经在 `main` 上，后续只登记该既有 SHA 的治理 PR 不因此强制使用 merge commit。

## Conflict policy

冲突解决原则：

- `internal/usage/`：保留 LTS 完整统计，必要时适配 upstream 新结构。
- `usage-statistics-enabled`：必须继续控制新 usage record 写入，不影响既有 snapshot/export/import 读取。
- `/v0/management/usage`、`/export`、`/import`：必须保留。
- usage record schema：保留 API key、auth file/source、model、token、latency、`timing_version`、首字节 `ttfb_ms`、首 reasoning `ttft_ms`、首回答 `ttfa_ms`、success/failure、auth index 等字段。
- Management usage response shape：保持 CPA-Panel-LTS 兼容。
- config schema：接收 upstream 新项，同时保留旧配置兼容。
- panel release source：保持 `BlueSkyXN/CPA-Panel-LTS`，除非用户明确改变策略。
- README 与展示资产：保留 upstream commit ancestry，但默认不恢复 sponsor、affiliate、注册返利、充值优惠、付费推荐文案或配套商业图片；只有维护者明确批准时才改变 commercial-neutral 策略，并在 PR body 记录决策。
- 如果 upstream 转向外置 usage service，可以适配新架构，但不能降级本仓库内置完整统计。

## Validation gate

sync PR 合入 `main` 前至少运行：

```bash
scripts/check-lts-contract.sh
go run ./scripts/ltsregistry --root .
go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'
go build -o test-output ./cmd/server && rm -f test-output
go test ./...
go list ./... | rg -v '/tmp$' | xargs go test -count=1
git diff --check
```

`scripts/check-lts-contract.sh` 必须调用结构化 validator，覆盖 registry、protected deltas 和 patch ledger；如果 guard 只证明文件存在或 marker 只存在于 registry 自身，不能视为 contract review 完成。原始 `go test ./...` 的结果仍要记录；若仅根目录 `tmp/` scratch package 造成污染，过滤 `/tmp` 的 repo-wide 命令作为产品代码信号，并在 PR body 如实区分两者。

当同步触碰 `internal/translator/openai/openai/responses/` 时，必须额外运行不带 restrictive `-run` 的 `go test ./internal/translator/openai/openai/responses`，确保 custom tool、namespace 和 event sequencing 测试不会因测试名不含 `Translation` 而被跳过。

如果 `go test ./...` 因环境、网络或已知非本次改动原因无法完成，PR body 必须写明实际命令、失败位置和剩余风险。不能把未运行的检查写成通过。

最小行为级 contract tests 必须覆盖：

- usage record creation / schema：API key、auth source、model、token、latency、`timing_version`、`ttfb_ms`、`ttft_ms`、`ttfa_ms`、success/failure。
- Management usage response shape：`usage`、`failed_requests`、`apis`、`models`、`details`。
- export/import roundtrip：导出后导入仍保留核心字段。

## Release note contract

正式发版使用 annotated `v*-tls-*` tag。tag subject 是用户可见摘要，tag body 必须包含且只能包含一条显式配套版本：

```text
Core LTS: upstream full-sync through ..., retained LTS fixes

Companion-Panel: v1-tls-0.0.13
```

`scripts/generate-lts-release-notes.sh` 不按发布时间猜测 Panel 版本，也不从 PR 顺序合成摘要。`workflow_dispatch` 重跑已有 Release 时默认保留人工修订过的标题和正文；只有明确启用 `rewrite_release_notes` 才重新生成。发版前运行 `scripts/generate-lts-release-notes_test.sh`，并在发布后回读配套链接、资产和 Latest 状态。

## PR body checklist

每个 sync PR 必须写清：

- upstream repo / branch
- upstream from SHA
- upstream to SHA
- stage 编号和分段理由
- 冲突文件列表
- protected delta review
- contract registry features reviewed
- downstream patch ledger conclusions
- 普通 upstream 改动吸收范围
- 被覆盖的旧 upstream-port PR 范围，如有
- validation 命令和结果
- `go test ./...` 状态
- HF Space smoke 是否执行，未执行则写明原因

Protected delta review 建议使用固定格式：

```text
Protected delta review:
- retained capabilities: preserved / adapted / newly-added
- LTS-owned features: preserved / adapted / newly-added
- shared review seams: reviewed / adapted / not-touched with reason
- downstream patches: patch-still-required / upstream-equivalent / retired
- validation profiles: run / skipped with reason
- upstream ordinary changes: absorbed
```

## Merge policy

upstream sync PR，以及在本 PR 内产生 `introduced_in` 或 `retired_in` 所指 commit 的 downstream patch PR，合入 `main` 时必须选择 Create a merge commit。

禁止：

- squash and merge
- rebase and merge
- 把 sync PR 拆成文件覆盖 commit
- squash 或 rebase 在本 PR 内产生 patch provenance commit 的 introduction / retirement PR

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
