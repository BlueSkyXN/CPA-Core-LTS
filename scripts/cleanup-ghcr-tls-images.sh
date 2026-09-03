#!/usr/bin/env bash
# cleanup-ghcr-tls-images.sh — Delete old v1-tls-* Docker images from GHCR
#
# Usage:
#   bash scripts/cleanup-ghcr-tls-images.sh [--dry-run]
#
# Requirements: gh CLI (authenticated with packages:delete scope)
set -euo pipefail

DRY_RUN=false
PACKAGE_NAME="cpa-core-lts"
OWNER="BlueSkyXN"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=true; shift ;;
    --owner)   OWNER="${2:-}"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [--dry-run] [--owner OWNER]"
      exit 0
      ;;
    *) echo "unknown: $1" >&2; exit 2 ;;
  esac
done

echo "Package: ghcr.io/${OWNER}/${PACKAGE_NAME}"
echo ""

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI is required but not found" >&2
  exit 1
fi

# List all versions and filter for tls pattern
echo "Fetching package versions..."
versions_json="$(gh api \
  "/orgs/${OWNER}/packages/container/${PACKAGE_NAME}/versions" \
  --paginate --jq '.[] | select(.metadata.container.tags[]? | test("^v[0-9]+-tls-")) | .id' 2>/dev/null || \
  gh api \
  "/users/${OWNER}/packages/container/${PACKAGE_NAME}/versions" \
  --paginate --jq '.[] | select(.metadata.container.tags[]? | test("^v[0-9]+-tls-")) | .id' 2>/dev/null || echo "")"

if [[ -z "$versions_json" ]]; then
  echo "No v*-tls-* tagged images found on GHCR."
  echo "(This may also mean the API call needs 'packages:delete' scope)"
  exit 0
fi

count="$(printf '%s\n' "$versions_json" | wc -l | tr -d ' ')"
echo "Found $count GHCR image versions with v*-tls-* tags"

if [[ "$DRY_RUN" == "true" ]]; then
  echo "DRY RUN — would delete the following version IDs:"
  printf '  %s\n' $versions_json
  exit 0
fi

echo "Deleting..."
while IFS= read -r version_id; do
  [[ -z "$version_id" ]] && continue
  gh api --method DELETE \
    "/orgs/${OWNER}/packages/container/${PACKAGE_NAME}/versions/${version_id}" 2>/dev/null || \
  gh api --method DELETE \
    "/users/${OWNER}/packages/container/${PACKAGE_NAME}/versions/${version_id}" 2>/dev/null || \
  echo "  FAILED to delete version $version_id"
  echo "  OK deleted version $version_id"
done <<< "$versions_json"

echo ""
echo "GHCR cleanup complete. New v*-lts-* images will be created by the docker-image workflow."
