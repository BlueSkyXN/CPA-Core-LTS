#!/usr/bin/env bash
set -euo pipefail

echo "Checking CPA-Core-LTS protected contract sentinels..."

test -d internal/usage
test -f docs/lts/protected-deltas.yaml
test -f docs/lts/core-feature-contracts.yaml

require_path() {
  local path="$1"
  if [ ! -e "$path" ]; then
    echo "missing required LTS contract path: $path" >&2
    exit 1
  fi
}

require_grep() {
  local pattern="$1"
  shift
  if ! git grep -n -- "$pattern" -- "$@" >/dev/null; then
    echo "missing required LTS contract marker '$pattern' in: $*" >&2
    exit 1
  fi
}

module_path="$(sed -n 's/^module //p' go.mod)"
if [ "$module_path" != "github.com/router-for-me/CLIProxyAPI/v7" ]; then
  echo "unexpected Go module path: $module_path" >&2
  exit 1
fi

if git grep -n "github.com/router-for-me/CLIProxyAPI/v6" -- . ':(exclude)scripts/check-lts-contract.sh'; then
  echo "legacy v6 module path references found; CPA-Core-LTS now follows upstream /v7" >&2
  exit 1
fi

grep -R "usage-statistics-enabled" -n \
  internal config.example.yaml README.md README_CN.md README_JA.md docs \
  --exclude-dir=.git \
  --exclude-dir=local \
  --exclude-dir=vendor \
  --exclude-dir=node_modules \
  >/dev/null

grep -R "/v0/management/usage" -n \
  internal config.example.yaml README.md README_CN.md README_JA.md docs \
  --exclude-dir=.git \
  --exclude-dir=local \
  --exclude-dir=vendor \
  --exclude-dir=node_modules \
  >/dev/null

grep -R "/v0/management/usage/export" -n \
  internal config.example.yaml README.md README_CN.md README_JA.md docs \
  --exclude-dir=.git \
  --exclude-dir=local \
  --exclude-dir=vendor \
  --exclude-dir=node_modules \
  >/dev/null

grep -R "/v0/management/usage/import" -n \
  internal config.example.yaml README.md README_CN.md README_JA.md docs \
  --exclude-dir=.git \
  --exclude-dir=local \
  --exclude-dir=vendor \
  --exclude-dir=node_modules \
  >/dev/null

require_path internal/api/handlers/management/usage.go
require_path internal/api/handlers/management/usage_contract_test.go
require_path internal/runtime/executor/helps/usage_helpers.go
require_path internal/managementasset/updater.go
require_path internal/config/config.go
require_path internal/redisqueue
require_path config.example.yaml

require_grep "mgmt.GET(\"/usage\"" internal/api/server.go
require_grep "mgmt.GET(\"/usage/export\"" internal/api/server.go
require_grep "mgmt.POST(\"/usage/import\"" internal/api/server.go
require_grep "mgmt.GET(\"/usage-queue\"" internal/api/server.go
require_grep "mgmt.GET(\"/usage-statistics-enabled\"" internal/api/server.go
require_grep "DefaultPanelGitHubRepository" internal/config/config.go
require_grep "defaultManagementReleaseURL" internal/managementasset/updater.go
require_grep "defaultManagementFallbackURL" internal/managementasset/updater.go
require_grep "managementAssetName" internal/managementasset/updater.go
require_grep "serveManagementControlPanel" internal/api/server.go
require_grep "BlueSkyXN/CPA-Panel-LTS" internal/managementasset/updater.go internal/managementasset/updater_test.go internal/config/config.go
require_grep "UsageReporter" internal/runtime/executor/helps/usage_helpers.go
require_grep "auth_index" internal/usage internal/api/handlers/management internal/runtime/executor/helps internal/redisqueue
require_grep "latency_ms" internal/usage internal/redisqueue internal/tui
require_grep "abnormal-reasoning-retry" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "hedged-retry" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "stream-buffer-max-bytes" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "max-retries" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "exhausted-behavior" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "reasoning-efforts" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "RetryWithoutPenalty" sdk/cliproxy/auth/conductor.go sdk/cliproxy/auth/retry_without_penalty.go docs/lts/core-feature-contracts.yaml
require_grep "ExcludeAuthIDsMetadataKey" sdk/cliproxy/executor/types.go docs/lts/core-feature-contracts.yaml
require_grep "CodexAbnormalReasoningRetryUsageMetadataKey" sdk/cliproxy/executor/types.go docs/lts/core-feature-contracts.yaml
require_grep "codex_abnormal_reasoning_response" internal/runtime/executor/codex_abnormal_reasoning_retry.go docs/lts/core-feature-contracts.yaml
require_grep "codex_abnormal_reasoning_retry_exhausted" sdk/cliproxy/auth/retry_without_penalty.go docs/lts/core-feature-contracts.yaml
require_grep "failure_reason" internal/usage/logger_plugin.go docs/lts/core-feature-contracts.yaml
require_grep "transient-error-cooldown-seconds" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml
require_grep "TransientErrorCooldownSeconds" internal/config/config.go internal/api/server.go sdk/cliproxy/service.go docs/lts/core-feature-contracts.yaml
require_grep "SetTransientErrorCooldownSeconds" cmd/server/main.go internal/api/server.go sdk/cliproxy/auth/conductor.go sdk/cliproxy/service.go docs/lts/core-feature-contracts.yaml
require_grep "nextTransientErrorRetryAfter" sdk/cliproxy/auth/conductor.go docs/lts/core-feature-contracts.yaml

python3 - <<'PY'
from pathlib import Path
import sys

path = Path("docs/lts/protected-deltas.yaml")
text = path.read_text(encoding="utf-8")

required = [
    "protected-full-sync",
    "full-usage-statistics",
    "usage-statistics-enabled",
    "internal/usage/",
    "/v0/management/usage",
    "/v0/management/usage/export",
    "/v0/management/usage/import",
    "cpa-panel-lts-compatibility",
    "local-downstream-customizations",
    "preserve_or_reapply_lts_usage",
    "core-feature-contracts.yaml",
    "contract_registry_required",
]

missing = [item for item in required if item not in text]
if missing:
    for item in missing:
        print(f"missing protected contract marker: {item}", file=sys.stderr)
    sys.exit(1)

registry_path = Path("docs/lts/core-feature-contracts.yaml")
registry_text = registry_path.read_text(encoding="utf-8")

registry_required = [
    "maintenance_model: protected-full-sync-with-contract-registry",
    "guard_policy",
    "sentinel gate",
    "not a replacement for contract tests",
    "full-usage-statistics-core",
    "management-usage-api",
    "panel-release-asset",
    "auth-identity-attribution",
    "provider-runtime-usage-seams",
    "codex-abnormal-reasoning-retry",
    "config-compatibility-and-hot-reload",
    "redis-compatible-usage-queue",
    "hf-space-runtime-smoke",
    "usage-statistics-enabled",
    "/v0/management/usage",
    "/v0/management/usage/export",
    "/v0/management/usage/import",
    "/v0/management/usage-statistics-enabled",
    "BlueSkyXN/CPA-Panel-LTS",
    "management.html",
    "auth_index",
    "latency_ms",
    "success_count",
    "failure_count",
    "go test ./internal/usage ./internal/api/handlers/management ./test -run 'Usage|usage'",
    "abnormal-reasoning-retry",
    "hedged-retry",
    "stream-buffer-max-bytes",
    "hedge-delay-ms",
    "require-distinct-auth",
    "max-retries",
    "exhausted-behavior",
    "reasoning-efforts",
    "ExcludeAuthIDsMetadataKey",
    "RetryWithoutPenalty",
    "CodexAbnormalReasoningRetryUsageMetadataKey",
    "codex_abnormal_reasoning_response",
    "codex_abnormal_reasoning_retry_exhausted",
    "failure_reason",
    "transient-error-cooldown-config",
    "transient-error-cooldown-seconds",
    "TransientErrorCooldownSeconds",
    "SetTransientErrorCooldownSeconds",
    "nextTransientErrorRetryAfter",
]

missing_registry = [item for item in registry_required if item not in registry_text]
if missing_registry:
    for item in missing_registry:
        print(f"missing core feature contract marker: {item}", file=sys.stderr)
    sys.exit(1)
PY

echo "CPA-Core-LTS protected contract sentinels passed."
