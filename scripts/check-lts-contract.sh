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

require_absent_path() {
  local path="$1"
  if [ -e "$path" ]; then
    echo "forbidden LTS contract path present: $path" >&2
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

forbid_grep() {
  local pattern="$1"
  shift
  if git grep -n -I -E -- "$pattern" -- "$@"; then
    echo "forbidden LTS contract marker '$pattern' found in: $*" >&2
    exit 1
  fi
}

forbid_grep_ci() {
  local pattern="$1"
  shift
  if git grep -n -I -i -E -- "$pattern" -- "$@"; then
    echo "forbidden promotional marker '$pattern' found in: $*" >&2
    exit 1
  fi
}

module_path="$(sed -n 's/^module //p' go.mod)"
if [ "$module_path" != "github.com/router-for-me/CLIProxyAPI/v7" ]; then
  echo "unexpected Go module path: $module_path" >&2
  exit 1
fi

# Keep CPA-Core-LTS documentation commercial-neutral across future upstream syncs.
for promotional_asset in \
  assets/apikey.png \
  assets/aicodemirror.png \
  assets/apimart-en.png \
  assets/apimart-zh.png \
  assets/bmoplus.png \
  assets/catapi.png \
  assets/claudeapi.png \
	assets/code0.png \
	assets/cubence.png \
	assets/cyberpay.jpg \
	assets/fastaitoken.png \
	assets/fennoai.png \
  assets/lingtrue.png \
  assets/packycode-cn.png \
  assets/packycode-en.png \
  assets/packycode.png \
  assets/poixeai.png \
  assets/qiniucloud.png \
  assets/runapi.png \
  assets/unity2.jpg \
  assets/visioncoder.png; do
  require_absent_path "$promotional_asset"
done
forbid_grep_ci '(^|[^[:alnum:]_])(sponsor(ship)?|affiliate)([^[:alnum:]_]|$)|赞助|スポンサー|[?&](aff|invitecode|utm_source)=|promo[[:space:]_-]*code|优惠码|邀请码|专属(链接|福利)|専用リンク' \
  README.md README_CN.md README_JA.md

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
require_path docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
require_path internal/registry/models/codex_client_models.json
require_path internal/managementasset/updater.go
require_path internal/config/config.go
require_path internal/redisqueue
require_path config.example.yaml
require_path docs/lts/codex-429-resilience.md
require_path .github/scripts/refresh-model-catalogs.sh

require_grep "mgmt.GET(\"/usage\"" internal/api/server_management.go
require_grep "mgmt.GET(\"/usage/export\"" internal/api/server_management.go
require_grep "mgmt.POST(\"/usage/import\"" internal/api/server_management.go
require_grep "mgmt.GET(\"/usage-queue\"" internal/api/server_management.go
require_grep "mgmt.GET(\"/usage-statistics-enabled\"" internal/api/server_management.go
require_grep "DefaultPanelGitHubRepository" internal/config/config_defaults.go
require_grep "defaultManagementReleaseURL" internal/managementasset/updater.go
require_grep "defaultManagementFallbackURL" internal/managementasset/updater.go
require_grep "managementAssetName" internal/managementasset/updater.go
require_grep "serveManagementControlPanel" internal/api/server_management.go
require_grep "BlueSkyXN/CPA-Panel-LTS" internal/managementasset/updater.go internal/managementasset/updater_test.go internal/config/config_defaults.go
require_grep "UsageReporter" internal/runtime/executor/helps/usage_helpers.go
require_grep "codex.model-fallback" docs/lts/core-feature-contracts.yaml docs/lts/codex-429-resilience.md
require_grep "global-targets" internal/config/config.go config.example.yaml docs/lts/core-feature-contracts.yaml docs/lts/codex-429-resilience.md
require_grep "model_fallback_reason" sdk/cliproxy/auth/cooldown_state.go docs/lts/core-feature-contracts.yaml docs/lts/codex-429-resilience.md
require_grep "CodexModelFallbackSourceModelMetadataKey" sdk/cliproxy/executor/types.go sdk/cliproxy/auth/codex_model_fallback.go
require_grep "codex.rate-limit-continuity" docs/lts/core-feature-contracts.yaml docs/lts/codex-429-resilience.md
require_grep "codexRateLimitContinuityStore" sdk/cliproxy/auth/codex_rate_limit_continuity.go docs/lts/core-feature-contracts.yaml
require_grep "codexRateLimitContinuityFreshBlocked" sdk/cliproxy/auth/codex_rate_limit_continuity.go
require_grep "codexRateLimitContinuityFreshBlocked" docs/lts/core-feature-contracts.yaml docs/lts/protected-deltas.yaml
require_grep "codexRateLimitContinuityConfirmedCooldown" sdk/cliproxy/auth/codex_rate_limit_continuity.go
require_grep "nextLeaseExpiry" sdk/cliproxy/auth/codex_rate_limit_continuity.go docs/lts/core-feature-contracts.yaml
require_grep "codexRateLimitContinuityLifecycleContextKey" sdk/cliproxy/auth/codex_rate_limit_continuity.go docs/lts/core-feature-contracts.yaml
require_grep "removeSessionPreservingKey" sdk/cliproxy/auth/codex_rate_limit_continuity.go sdk/cliproxy/auth/conductor_lts.go docs/lts/core-feature-contracts.yaml
require_grep "CodexModelFallbackContextResetReplayMetadataKey" sdk/cliproxy/executor/types.go sdk/api/handlers/openai/openai_responses_websocket.go
require_grep "ResolveCodexReasoningReplaySessionKey" internal/cache/codex_reasoning_replay_scope.go internal/runtime/executor/codex_executor_reasoning.go sdk/cliproxy/auth/codex_model_fallback.go
require_grep "ValidateCodexClientModelsLTSCompatibility" internal/registry/codex_client_models.go internal/registry/codex_client_models_test.go cmd/validate_codex_models/main.go
require_grep "refresh-model-catalogs.sh" .github/workflows/pr-test-build.yml .github/workflows/release.yaml .github/workflows/docker-image.yml
require_grep "codex_client_models.json" .github/scripts/refresh-model-catalogs.sh
require_grep "responsesWebsocketCanAttestContextReset" sdk/api/handlers/openai/openai_responses_websocket.go sdk/api/handlers/openai/openai_responses_websocket_test.go
require_grep "ModelFallbackZeroDispatch" sdk/cliproxy/auth/codex_model_fallback.go docs/lts/core-feature-contracts.yaml
require_grep "auth_index" internal/usage internal/api/handlers/management internal/runtime/executor/helps internal/redisqueue
require_grep "ttfb_ms" internal/usage internal/api/handlers/management
require_grep "timing_version" internal/usage internal/api/handlers/management internal/redisqueue
require_grep "ttft_ms" internal/usage internal/api/handlers/management internal/redisqueue
require_grep "ttfa_ms" internal/usage internal/api/handlers/management internal/redisqueue
require_grep "UserEndpoint" internal/auth/xai/types.go internal/auth/xai/xai.go docs/lts/core-feature-contracts.yaml
require_grep "UserEndpointUserAgent" internal/auth/xai/types.go internal/auth/xai/xai.go docs/lts/core-feature-contracts.yaml
require_grep 'case "amd64"' internal/auth/xai/types.go
require_grep 'archName = "x86_64"' internal/auth/xai/types.go
require_grep 'entry\["user_id"\]' internal/api/handlers/management/auth_files.go
require_grep 'metadata\["user_id"\]' internal/api/handlers/management/auth_files_provider_oauth.go internal/runtime/executor/xai_executor_auth.go sdk/auth/xai.go
require_grep "latency_ms" internal/usage internal/redisqueue internal/tui
require_grep 'json:"reasoning_effort,omitempty"' internal/usage
require_grep 'json:"service_tier,omitempty"' internal/usage
require_grep 'json:"request_service_tier,omitempty"' internal/usage
require_grep 'json:"outbound_service_tier,omitempty"' internal/usage internal/redisqueue
require_grep 'json:"response_service_tier,omitempty"' internal/usage
require_grep 'json:"effective_service_tier,omitempty"' internal/usage internal/redisqueue
forbid_grep 'BillingBasis|billing_basis' sdk/cliproxy/usage internal/runtime/executor/helps/usage_helpers.go internal/usage/logger_plugin.go internal/redisqueue/plugin.go internal/pluginhost sdk/pluginapi
require_grep 'json:"EffectiveServiceTier,omitempty"' sdk/pluginapi/types.go
require_grep 'json:"RequestServiceTier,omitempty"' sdk/pluginapi/types.go
require_grep 'json:"OutboundServiceTier,omitempty"' sdk/pluginapi/types.go
require_grep 'json:"ResponseServiceTier,omitempty"' sdk/pluginapi/types.go
require_grep 'json:"generate"' internal/usage internal/redisqueue
require_grep "GenerateEnabled" internal/usage internal/redisqueue sdk/cliproxy/usage
require_grep "CanonicalExportVersion" internal/usage/logger_plugin.go internal/api/handlers/management/usage.go docs/lts/core-feature-contracts.yaml docs/lts/protected-deltas.yaml
require_grep "TimingVersionV1" sdk/cliproxy/usage/manager.go internal/runtime/executor/helps/usage_timing.go docs/lts/core-feature-contracts.yaml
require_grep "uncached_input_tokens" internal/usage/logger_plugin.go internal/api/handlers/management/usage_contract_test.go docs/lts/core-feature-contracts.yaml docs/lts/protected-deltas.yaml
require_grep '"schema_version"' internal/api/handlers/management/usage.go internal/api/handlers/management/usage_contract_test.go docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
require_grep '"migrated_from_version"' internal/api/handlers/management/usage.go internal/api/handlers/management/usage_contract_test.go docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
require_grep '"migrations"' internal/api/handlers/management/usage.go internal/api/handlers/management/usage_contract_test.go docs/lts/change-control/CHG-20260902-USAGE-SCHEMA-V3.md
require_grep "v1_uncached_input_tokens_to_v2" internal/api/handlers/management/usage.go internal/api/handlers/management/usage_contract_test.go docs/lts/core-feature-contracts.yaml docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
require_grep '"code"' internal/api/handlers/management/usage.go internal/api/handlers/management/usage_contract_test.go docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
for approved_usage_code in \
  usage_version_unsupported \
  usage_shape_invalid \
  usage_v1_token_contract_invalid \
  usage_v1_cache_semantics_ambiguous \
  usage_v2_token_contract_invalid \
  usage_v1_timing_semantics_ambiguous \
  usage_v2_timing_semantics_ambiguous \
  usage_v3_token_contract_invalid \
  usage_v3_timing_contract_invalid \
  usage_aggregate_overflow; do
  require_grep "$approved_usage_code" \
    internal/api/handlers/management/usage.go \
    internal/api/handlers/management/usage_contract_test.go \
    docs/lts/core-feature-contracts.yaml \
    docs/lts/protected-deltas.yaml \
    docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
done
forbid_grep 'usage_''import_' \
  internal/api/handlers/management/usage.go \
  internal/api/handlers/management/usage_contract_test.go \
  docs/lts/core-feature-contracts.yaml \
  docs/lts/protected-deltas.yaml \
  docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
forbid_grep 'error_''code' \
  internal/api/handlers/management/usage.go \
  internal/api/handlers/management/usage_contract_test.go \
  docs/lts/core-feature-contracts.yaml \
  docs/lts/protected-deltas.yaml \
  docs/lts/change-control/CHG-20260722-USAGE-SCHEMA-V2.md
if git grep -n 'json:"thinking' -- internal/usage; then
  echo "non-canonical usage JSON field thinking found; use reasoning_effort" >&2
  exit 1
fi
require_grep "reasoning_effort" internal/api/handlers/management/usage_contract_test.go
require_grep '"thinking"' internal/api/handlers/management/usage_contract_test.go
require_grep "request_service_tier" internal/api/handlers/management/usage_contract_test.go
require_grep "outbound_service_tier" internal/api/handlers/management/usage_contract_test.go
require_grep "response_service_tier" internal/api/handlers/management/usage_contract_test.go
require_grep "effective_service_tier" internal/api/handlers/management/usage_contract_test.go
require_grep '"generate"' internal/api/handlers/management/usage_contract_test.go

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
