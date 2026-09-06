#!/usr/bin/env bash
set -euo pipefail

models_repository="${MODELS_REPOSITORY_URL:-https://github.com/router-for-me/models.git}"
models_ref="${MODELS_REPOSITORY_REF:-main}"
catalog_dir="${MODEL_CATALOG_DIR:-internal/registry/models}"
codex_catalog="$catalog_dir/codex_client_models.json"
models_catalog="$catalog_dir/models.json"
codex_candidate="$(mktemp)"
models_candidate="$(mktemp)"
trap 'rm -f "$codex_candidate" "$models_candidate"' EXIT

# LTS-required model IDs must survive a catalog refresh. The embedded catalog is
# kept when the remote copy is older or trimmed, mirroring the Codex client
# catalog guard below.
lts_required_models=("gpt-6-astra")

lts_models_present() {
  local candidate="$1" model
  for model in "${lts_required_models[@]}"; do
    if ! grep -q "\"$model\"" "$candidate"; then
      return 1
    fi
  done
}

git fetch --depth 1 "$models_repository" "$models_ref"

if git show FETCH_HEAD:models.json > "$models_candidate" && lts_models_present "$models_candidate"; then
  mv "$models_candidate" "$models_catalog"
  printf 'Refreshed model catalog.\n'
else
  printf '::warning::Remote models.json is missing, invalid, or lacks LTS-required models; using embedded fallback.\n'
fi

if git show FETCH_HEAD:codex_client_models.json > "$codex_candidate" &&
  go run ./cmd/validate_codex_models --file "$codex_candidate"; then
	mv "$codex_candidate" "$codex_catalog"
	printf 'Refreshed validated Codex client model catalog.\n'
else
  printf '::warning::Remote Codex client model catalog is missing, invalid, or LTS-incompatible; using embedded fallback.\n'
fi

go run ./cmd/validate_codex_models --file "$codex_catalog"
