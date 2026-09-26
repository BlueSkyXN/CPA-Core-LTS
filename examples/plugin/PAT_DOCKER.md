# CodeBuddy / Copilot / Qoder 原生插件可选发行版

本发行版保持认证文件登记和动态插件架构：CodeBuddy/Qoder 走 PAT，Copilot 走 GitHub OAuth device 登录或自备 `github_token`。标准 Dockerfile、标准镜像和默认插件源不变。
这是独立的部署组合，不是对现有容器的自动升级脚本。不要将示例配置覆盖到已有生产配置。

## 提供什么

- 同一 Core 源码版本构建的 Core、CodeBuddy/Copilot/Qoder Linux 动态库。
- 镜像不含 Node、Qoder CLI、Qoder SDK 或 runner；插件为原生 Go 动态库。
- Qoder 默认原生 `direct_openai` 并内置中国区 endpoints：不安装 Qoder CLI、不启动原生 Agent 工具、不在模式之间自动兜底；国际区实例显式覆盖 endpoint。
- 镜像内 `/opt/cpa-pat-plugins/bundle.json` 记录 Core SHA 与插件版本及平台（`runner=null`、`node_major=null`）。
- PAT、配置、日志通过挂载保存；镜像中没有账号或管理凭据。

## 使用

1. 使用 `examples/plugin/pat-providers.config.yaml` 作为新部署的配置起点，设置自己的
   `remote-management.secret-key` 和客户端 `api-keys`，通过 `CLI_PROXY_CONFIG_PATH` 指向该文件。
   对已有配置只合并 `plugins` 配置，保留其他 provider、统计及访问控制设置。
2. Qoder 最小配置只需启用插件与 `auth-read` 权限：中国区 endpoints 与原生 `direct_openai` 均为内置默认值，
   模型目录按账号自动发现。上线前需要验证目标区域的 PAT、准确模型 ID、Chat/tools 及 Responses 行为。
   国际区需显式覆盖 `direct_endpoint` / `direct_models_endpoint` / `openapi_endpoint`。区域是实例级设置，PAT 表单不会写入虚构的每账号区域字段。
3. Copilot 最小配置同样只需启用插件与 `auth-read` 权限。账号登记两种方式：`github_token` 模式直接放置
   `type: copilot` 的 auth 文件，或 OAuth device 登录后由插件写入；登录状态经 Management 路由
   `/v0/management/plugins/copilot/login-info` 查询（Panel 登录入口为后续计划）。GHES 实例用
   `enterprise_domain` 或 `github_api_endpoint` 覆盖默认 github.com 端点。
4. 已正式发布扩展镜像时，设置 `CPA_PAT_IMAGE` 为相应的固定版本
   `ghcr.io/blueskyxn/cpa-core-lts:<Core-tag>-pat-providers`，执行：

   ```sh
   docker compose -f docker-compose.pat-providers.yml pull
   docker compose -f docker-compose.pat-providers.yml up -d --no-build
   ```

   首次发布前可从仓库构建，不能把尚未发布的标签当成可下载镜像：

   ```sh
   export VERSION=dev COMMIT="$(git rev-parse HEAD)"
   docker compose -f docker-compose.pat-providers.yml build
   docker compose -f docker-compose.pat-providers.yml up -d --no-build
   ```

5. Compose 默认只绑定主机 `127.0.0.1:8317`。远程使用通过 SSH 转发或已有的受保护入口访问；
   `CPA_BIND_ADDRESS` 可显式改变绑定地址，不自动开放公网管理端口。
6. 配套 Panel 必须包含 `pat_accounts` 功能。在认证文件页选择“添加 PAT 账号”，填写名称和 PAT，
   保存后通过账号卡片获取模型、查询额度。CodeBuddy 调用使用 `stream: true`。Copilot 暂无 Panel 表单，
   按「使用」第 3 步登记账号。
   保存成功只代表认证文件登记完成，不代表供应商验证通过。已有 JSON 上传方式继续可用。
7. 更新 PAT 通过账号卡片操作；保留原文件名及无关配置。套餐查询和模型调用是独立状态，
   额度查询失败不表示余额为零；一分钟内刷新可能返回带原始数据时间的服务器缓存。

## Panel 配套与升级

Core 保持从 CPA-Panel-LTS 下载 `management.html` 的既有机制。发布这套功能时需要先准备包含 PAT UI 的 Panel
Release，并记录验证过的 Panel tag/SHA；不能把旧 Panel 当作完整验收。
本地联调可将该 Panel 的 `dist/index.html` 作为 `management.html` 挂到 `/CLIProxyAPI/static/management.html`，
并在专用测试配置中设置 `remote-management.disable-auto-update-panel: true`；不要改变正式实例的更新策略。

内置插件随扩展镜像一起升级；不要在该目录使用插件商店独立覆盖升级。
标准 Compose 的 `/CLIProxyAPI/plugins` 不会遮住内置目录，但本发行版不会自动加载该标准目录中的其他插件；
需要其他插件的实例应明确整理其 `plugins.dir` 和配置，不能把本示例当作无差别替换。
回退镜像不会主动修改或删除 auth 文件。重建保留账号的前提是继续使用相同 auth 挂载。

## 独立产物与发布边界

`pat-provider-delivery` workflow 对相关 PR 构建两个 Linux 架构并运行无供应商请求的容器 smoke，
生成 CI artifacts；PR 不发布镜像或 Release。
手工运行时默认 `publish=false`。正式发布必须选择已经存在且包含本工作流的 Core Release tag，并显式启用
`publish`，才会上传固定版本镜像和附件；不会移动标准 `latest` 标签，也不覆盖已有 Release 附件。

- `cpa-provider-<provider>_<plugin-version>_linux_<arch>.zip`：ZIP 根目录只有 `.so`（Qoder 另含 `THIRD_PARTY_NOTICES.md`）。
- `provider-checksums-<arch>.txt`：仅这些产物的校验值，不覆盖 Core Release 的 `checksums.txt`。
- `pat-provider-bundle-<arch>.json`：配套版本和 SHA 清单，`runner=null`、`node_major=null` 为可审计字段。

独立插件商店接入应使用 `direct` 安装计划，填写对应附件固定 URL 与 SHA-256；不要直接套用 Core 的 latest
Release 与插件版本规则。

## 验证

```sh
python3 -m unittest discover -s scripts -p test_package_pat_providers.py
docker build -f Dockerfile.pat-providers --build-arg COMMIT="$(git rev-parse HEAD)" -t cpa-pat:test .
python3 scripts/smoke-pat-providers.py --image cpa-pat:test
```

容器 smoke 只验证三插件加载（最小 Qoder/Copilot 配置即默认值可用）、镜像无 Node/runner、认证文件登记及容器重建持久化。
它仅向容器内的关闭 loopback 端口配置 provider，不用真实凭据、不请求供应商、不证明真实模型可用。
真实验收另需：目标账号的模型/额度查询、实际流式调用、Responses、usage 归属、失效 PAT 提示。
