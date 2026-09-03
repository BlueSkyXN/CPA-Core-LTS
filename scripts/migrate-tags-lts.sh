#!/usr/bin/env bash
# migrate-tags-lts.sh — Rename v1-tls-* tags to v1-lts-* (Core & Panel)
#
# This script handles:
#   1. Recreating annotated git tags with corrected names (tls → lts)
#   2. Migrating GitHub Releases (with assets) to the new tag names
#   3. Cleaning up old tags locally and on the remote
#
# Usage:
#   bash scripts/migrate-tags-lts.sh [--dry-run] [--repo OWNER/NAME] [--skip-releases]
#
# Requirements: git, gh CLI (authenticated), curl (for GHCR cleanup)
set -euo pipefail

DRY_RUN=false
SKIP_RELEASES=false
REPO="${REPO:-BlueSkyXN/CPA-Core-LTS}"
TAG_PATTERN='v1-tls-*'

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)   DRY_RUN=true; shift ;;
    --skip-releases) SKIP_RELEASES=true; shift ;;
    --repo)      REPO="${2:-}"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [--dry-run] [--repo OWNER/NAME] [--skip-releases]"
      exit 0
      ;;
    *) echo "unknown: $1" >&2; exit 2 ;;
  esac
done

if [[ "$DRY_RUN" == "true" ]]; then
  echo "=== DRY RUN MODE — no changes will be made ==="
fi

echo "Target repo: $REPO"
echo "Tag pattern: $TAG_PATTERN"
echo ""

# Collect old tags (sorted by version)
mapfile -t OLD_TAGS < <(git tag --list "$TAG_PATTERN" --sort=v:refname)
if [[ ${#OLD_TAGS[@]} -eq 0 ]]; then
  echo "No tags matching $TAG_PATTERN found."
  exit 0
fi

echo "Found ${#OLD_TAGS[@]} tags to migrate:"
printf '  %s\n' "${OLD_TAGS[@]}"
echo ""

# ─── Phase 1: Create new annotated tags locally ───
echo "━━━ Phase 1: Create new v1-lts-* annotated tags ━━━"

declare -A TAG_MAP  # old_tag -> new_tag
for old_tag in "${OLD_TAGS[@]}"; do
  new_tag="${old_tag/tls/lts}"
  TAG_MAP["$old_tag"]="$new_tag"

  # Check if new tag already exists
  if git rev-parse -q --verify "refs/tags/$new_tag" >/dev/null 2>&1; then
    echo "  SKIP $new_tag (already exists)"
    continue
  fi

  # Get the commit this annotated tag points to
  commit_sha="$(git rev-parse "${old_tag}^{commit}")"

  # Get full tag message (subject + body), replace tls → lts in tag content
  full_message="$(git for-each-ref --format='%(contents)' "refs/tags/$old_tag")"
  corrected_message="$(printf '%s' "$full_message" | sed 's/tls/lts/g; s/TLS/LTS/g')"

  # If message is empty or just the tag name, create a minimal message
  subject="$(printf '%s\n' "$corrected_message" | head -1)"
  if [[ -z "$subject" || "$subject" == "$old_tag" || "$subject" == "$new_tag" \
    || "$subject" == "CPA-Core-LTS $old_tag" || "$subject" == "CPA-Core-LTS $new_tag" \
    || "$subject" == "CPA Core LTS $old_tag" || "$subject" == "CPA Core LTS $new_tag" \
    || "$subject" == "$old_tag:"* ]]; then
    # Preserve original subject but fix tag references
    corrected_message="$(printf '%s' "$full_message" | sed "s/${old_tag}/${new_tag}/g")"
  fi

  if [[ "$DRY_RUN" == "true" ]]; then
    echo "  DRY-RUN: git tag -a $new_tag $commit_sha -m '...'"
    echo "    Message preview: $(printf '%s' "$corrected_message" | head -1)"
  else
    # Create new annotated tag
    git tag -a "$new_tag" "$commit_sha" -f -m "$corrected_message"
    echo "  OK $old_tag → $new_tag (commit ${commit_sha:0:8})"
  fi
done

echo ""

# ─── Phase 2: Push new tags to remote ───
echo "━━━ Phase 2: Push new tags to origin ━━━"

if [[ "$DRY_RUN" == "true" ]]; then
  echo "  DRY-RUN: git push origin ${#OLD_TAGS[@]} new tags"
else
  push_args=()
  for old_tag in "${OLD_TAGS[@]}"; do
    new_tag="${TAG_MAP[$old_tag]}"
    push_args+=("refs/tags/$new_tag")
  done
  git push origin "${push_args[@]}"
  echo "  OK pushed ${#push_args[@]} new tags"
fi

echo ""

# ─── Phase 3: Migrate GitHub Releases ───
echo "━━━ Phase 3: Migrate GitHub Releases ━━━"

if [[ "$SKIP_RELEASES" == "true" ]]; then
  echo "  SKIPPED (--skip-releases)"
elif ! command -v gh >/dev/null 2>&1; then
  echo "  SKIPPED (gh CLI not found)"
else
  for old_tag in "${OLD_TAGS[@]}"; do
    new_tag="${TAG_MAP[$old_tag]}"
    echo "  Processing $old_tag → $new_tag ..."

    # Check if old release exists
    if ! gh release view "$old_tag" -R "$REPO" >/dev/null 2>&1; then
      echo "    No GitHub release for $old_tag, skipping"
      continue
    fi

    if [[ "$DRY_RUN" == "true" ]]; then
      echo "    DRY-RUN: migrate release $old_tag → $new_tag"
      continue
    fi

    # Get old release title and notes
    old_title="$(gh release view "$old_tag" -R "$REPO" --json name -q .name 2>/dev/null || echo "")"
    old_notes="$(gh release view "$old_tag" -R "$REPO" --json body -q .body 2>/dev/null || echo "")"

    # Fix title and notes: replace tls → lts
    new_title="$(printf '%s' "$old_title" | sed 's/tls/lts/g; s/TLS/LTS/g')"
    new_notes="$(printf '%s' "$old_notes" | sed 's/tls/lts/g; s/TLS/LTS/g')"

    # Download assets to temp dir
    tmp_dir="$(mktemp -d)"
    trap "rm -rf '$tmp_dir'" EXIT
    gh release download "$old_tag" -R "$REPO" --dir "$tmp_dir" --clobber 2>/dev/null || true

    # Delete old release
    gh release delete "$old_tag" -R "$REPO" --yes 2>/dev/null || true
    echo "    Deleted old release $old_tag"

    # Create new release
    notes_file="$(mktemp)"
    printf '%s' "$new_notes" > "$notes_file"
    gh release create "$new_tag" -R "$REPO" --title "$new_title" --notes-file "$notes_file" 2>/dev/null || \
      gh release create "$new_tag" -R "$REPO" --title "$new_title" --notes "$new_notes" 2>/dev/null || true
    rm -f "$notes_file"

    # Re-upload assets
    shopt -s nullglob
    assets=("$tmp_dir"/*)
    if [[ ${#assets[@]} -gt 0 ]]; then
      gh release upload "$new_tag" -R "$REPO" "${assets[@]}" --clobber
      echo "    Re-uploaded ${#assets[@]} assets"
    fi
    rm -rf "$tmp_dir"
    trap - EXIT

    echo "    OK release $new_tag created"
  done
fi

echo ""

# ─── Phase 4: Delete old tags ───
echo "━━━ Phase 4: Delete old v1-tls-* tags ━━━"

if [[ "$DRY_RUN" == "true" ]]; then
  for old_tag in "${OLD_TAGS[@]}"; do
    echo "  DRY-RUN: git tag -d $old_tag && git push origin :refs/tags/$old_tag"
  done
else
  # Delete old tags locally
  for old_tag in "${OLD_TAGS[@]}"; do
    git tag -d "$old_tag" 2>/dev/null || true
  done
  echo "  OK deleted ${#OLD_TAGS[@]} local tags"

  # Delete old tags from remote
  delete_args=()
  for old_tag in "${OLD_TAGS[@]}"; do
    delete_args+=(":refs/tags/$old_tag")
  done
  git push origin "${delete_args[@]}" 2>/dev/null || true
  echo "  OK deleted ${#OLD_TAGS[@]} remote tags"
fi

echo ""
echo "━━━ Migration complete ━━━"
echo ""
echo "Remaining manual steps:"
echo "  1. Update workflow files: v*-tls-* → v*-lts-*"
echo "  2. Update scripts/generate-lts-release-notes.sh"
echo "  3. Update docs/lts/sync-runbook.md"
echo "  4. Update test files"
echo "  5. GHCR: old v1-tls-* Docker images remain; consider deleting via GitHub Packages UI"
echo "  6. Repeat for CPA-Panel-LTS if needed"
