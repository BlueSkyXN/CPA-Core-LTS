#!/usr/bin/env bash
# cleanup-ghcr-tls-images.sh — Delete superseded v*-tls-* Docker image versions.
set -euo pipefail

APPLY=false
PACKAGE_NAME="cpa-core-lts"
OWNER="BlueSkyXN"

usage() {
  cat <<'EOF'
Usage: scripts/cleanup-ghcr-tls-images.sh [--dry-run | --apply] [--owner OWNER]

The default is a read-only dry run. Deletion requires --apply. Every v*-tls-*
tag must have a matching v*-lts-* tag, and a version carrying latest or any
other non-tls tag is refused.

Requirements:
  - gh CLI authenticated with read:packages for inspection
  - delete:packages additionally required with --apply
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) APPLY=false; shift ;;
    --apply)   APPLY=true; shift ;;
    --owner)   OWNER="${2:-}"; shift 2 ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) echo "unknown: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$OWNER" ]]; then
  echo "--owner must not be empty" >&2
  exit 2
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI is required but not found" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
versions_file="$tmp_dir/versions.tsv"
user_errors_file="$tmp_dir/gh-user-errors.txt"
org_errors_file="$tmp_dir/gh-org-errors.txt"
targets_file="$tmp_dir/targets.tsv"
lts_tags_file="$tmp_dir/lts-tags.txt"

fetch_versions() {
  local endpoint="$1"
  local errors_file="$2"
  gh api "$endpoint" --paginate \
    --jq '.[] | [.id, ((.metadata.container.tags // []) | join(","))] | @tsv' \
    >"$versions_file" 2>"$errors_file"
}

user_endpoint="/users/${OWNER}/packages/container/${PACKAGE_NAME}/versions"
org_endpoint="/orgs/${OWNER}/packages/container/${PACKAGE_NAME}/versions"
package_endpoint=""

if fetch_versions "$user_endpoint" "$user_errors_file"; then
  package_endpoint="$user_endpoint"
elif fetch_versions "$org_endpoint" "$org_errors_file"; then
  package_endpoint="$org_endpoint"
else
  echo "Unable to read GHCR package versions for ${OWNER}/${PACKAGE_NAME}." >&2
  echo "Confirm the owner type and a token with read:packages; no deletion was attempted." >&2
  echo "User endpoint:" >&2
  sed -n '1,4p' "$user_errors_file" >&2
  echo "Organization endpoint:" >&2
  sed -n '1,4p' "$org_errors_file" >&2
  exit 1
fi

echo "Package: ghcr.io/${OWNER}/${PACKAGE_NAME}"
echo "Endpoint: ${package_endpoint}"

: >"$lts_tags_file"
while IFS=$'\t' read -r _ tags_csv; do
  [[ -z "$tags_csv" ]] && continue
  IFS=',' read -r -a tags <<<"$tags_csv"
  for tag in "${tags[@]}"; do
    if [[ "$tag" == v*-lts-* ]]; then
      printf '%s\n' "$tag" >>"$lts_tags_file"
    fi
  done
done <"$versions_file"
sort -u -o "$lts_tags_file" "$lts_tags_file"

: >"$targets_file"
unsafe=false
while IFS=$'\t' read -r version_id tags_csv; do
  [[ -z "$version_id" || -z "$tags_csv" ]] && continue
  IFS=',' read -r -a tags <<<"$tags_csv"
  has_tls_tag=false
  has_shared_tag=false

  for tag in "${tags[@]}"; do
    if [[ "$tag" == v*-tls-* ]]; then
      has_tls_tag=true
      replacement="${tag/-tls-/-lts-}"
      if ! grep -Fqx -- "$replacement" "$lts_tags_file"; then
        echo "BLOCKED version $version_id: $tag has no verified replacement tag $replacement" >&2
        unsafe=true
      fi
    else
      has_shared_tag=true
    fi
  done

  if [[ "$has_tls_tag" == "true" ]]; then
    if [[ "$has_shared_tag" == "true" ]]; then
      echo "BLOCKED version $version_id: non-tls tags share this version ($tags_csv)" >&2
      unsafe=true
    fi
    printf '%s\t%s\n' "$version_id" "$tags_csv" >>"$targets_file"
  fi
done <"$versions_file"

if [[ "$unsafe" == "true" ]]; then
  echo "Refusing cleanup until every replacement exists and latest/shared tags have moved." >&2
  exit 1
fi

target_count="$(wc -l <"$targets_file" | tr -d '[:space:]')"
if [[ "$target_count" -eq 0 ]]; then
  echo "No removable v*-tls-* tagged image versions found."
  exit 0
fi

echo "Found $target_count removable GHCR image versions:"
while IFS=$'\t' read -r version_id tags_csv; do
  printf '  %s  %s\n' "$version_id" "$tags_csv"
done <"$targets_file"

if [[ "$APPLY" != "true" ]]; then
  echo "DRY RUN — no versions were deleted. Re-run with --apply after reviewing the list."
  exit 0
fi

failures=0
while IFS=$'\t' read -r version_id tags_csv; do
  if gh api --method DELETE "${package_endpoint}/${version_id}" >/dev/null; then
    echo "  OK deleted version $version_id ($tags_csv)"
  else
    echo "  FAILED to delete version $version_id ($tags_csv)" >&2
    failures=$((failures + 1))
  fi
done <"$targets_file"

if [[ "$failures" -ne 0 ]]; then
  echo "GHCR cleanup failed for $failures version(s)." >&2
  exit 1
fi

if ! fetch_versions "$package_endpoint" "$user_errors_file"; then
  echo "Deleted versions, but failed to read back the package state." >&2
  sed -n '1,8p' "$user_errors_file" >&2
  exit 1
fi

while IFS=$'\t' read -r version_id _; do
  if awk -F '\t' -v id="$version_id" '$1 == id { found = 1 } END { exit !found }' "$versions_file"; then
    echo "GHCR cleanup readback failed: version $version_id still exists." >&2
    exit 1
  fi
done <"$targets_file"

echo "GHCR cleanup complete and deletion readback passed."
