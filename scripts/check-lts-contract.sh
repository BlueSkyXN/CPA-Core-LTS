#!/usr/bin/env bash
set -euo pipefail

echo "Checking CPA-Core-LTS protected contract sentinels..."

test -d internal/usage
test -f docs/lts/protected-deltas.yaml
test -f docs/lts/core-feature-contracts.yaml
test -f docs/lts/downstream-patches.yaml

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
require_grep 'json:"service_tier,omitempty"' internal/usage
require_grep "service_tier" internal/api/handlers/management/usage_contract_test.go

go test ./scripts/ltsregistry -count=1

validator_args=(--root .)
if [ -n "${GITHUB_BASE_REF:-}" ]; then
  base_remote_ref="refs/remotes/origin/${GITHUB_BASE_REF}"
  if ! git rev-parse --verify --quiet "${base_remote_ref}^{commit}" >/dev/null; then
    git fetch --no-tags --depth=1 origin "${GITHUB_BASE_REF}:$base_remote_ref"
  fi
  validator_args+=(--base-ref "$base_remote_ref")
fi
go run ./scripts/ltsregistry "${validator_args[@]}"

echo "CPA-Core-LTS protected contract sentinels passed."
