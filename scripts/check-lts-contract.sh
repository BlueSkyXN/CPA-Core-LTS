#!/usr/bin/env bash
set -euo pipefail

echo "Checking CPA-Core-LTS protected contract sentinels..."

test -d internal/usage
test -f docs/lts/protected-deltas.yaml

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
]

missing = [item for item in required if item not in text]
if missing:
    for item in missing:
        print(f"missing protected contract marker: {item}", file=sys.stderr)
    sys.exit(1)
PY

echo "CPA-Core-LTS protected contract sentinels passed."
