#!/usr/bin/env bash
# migrate-tags-lts.sh — Rename v1-tls-* tags to v1-lts-* (Core & Panel)
#
# This script handles:
#   1. Recreating annotated git tags with corrected names (tls → lts)
#   2. Migrating GitHub Releases and preserving the old assets as transitional copies
#   3. Cleaning up old tags locally and on the remote
#
# Usage:
#   bash scripts/migrate-tags-lts.sh [--dry-run | --apply] [--repo OWNER/NAME] [--skip-releases]
#
# Requirements: git, gh CLI (authenticated)
set -euo pipefail

DRY_RUN=true
SKIP_RELEASES=false
REPO="${REPO:-BlueSkyXN/CPA-Core-LTS}"
TAG_PATTERN='v1-tls-*'

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)   DRY_RUN=true; shift ;;
    --apply)     DRY_RUN=false; shift ;;
    --skip-releases) SKIP_RELEASES=true; shift ;;
    --repo)      REPO="${2:-}"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [--dry-run | --apply] [--repo OWNER/NAME] [--skip-releases]"
      exit 0
      ;;
    *) echo "unknown: $1" >&2; exit 2 ;;
  esac
done

if [[ "$DRY_RUN" == "true" ]]; then
  echo "=== DRY RUN MODE — no changes will be made; use --apply to mutate tags or Releases ==="
fi

echo "Target repo: $REPO"
echo "Tag pattern: $TAG_PATTERN"
echo ""

# Collect old tags (sorted by version). Keep compatibility with macOS Bash 3.2.
OLD_TAGS=()
while IFS= read -r old_tag; do
  [[ -n "$old_tag" ]] && OLD_TAGS+=("$old_tag")
done < <(git tag --list "$TAG_PATTERN" --sort=v:refname)
if [[ ${#OLD_TAGS[@]} -eq 0 ]]; then
  echo "No tags matching $TAG_PATTERN found."
  exit 0
fi

echo "Found ${#OLD_TAGS[@]} tags to migrate:"
printf '  %s\n' "${OLD_TAGS[@]}"
echo ""

# ─── Phase 1: Create new annotated tags locally ───
echo "━━━ Phase 1: Create new v1-lts-* annotated tags ━━━"

for old_tag in "${OLD_TAGS[@]}"; do
  new_tag="${old_tag/tls/lts}"

  # Check if new tag already exists
  if git rev-parse -q --verify "refs/tags/$new_tag" >/dev/null 2>&1; then
    old_commit="$(git rev-parse "${old_tag}^{commit}")"
    new_commit="$(git rev-parse "${new_tag}^{commit}")"
    if [[ "$new_commit" != "$old_commit" ]]; then
      echo "  ERROR $new_tag points to $new_commit, expected $old_commit" >&2
      exit 1
    fi
    echo "  SKIP $new_tag (already exists at ${new_commit:0:8})"
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
  for old_tag in "${OLD_TAGS[@]}"; do
    new_tag="${old_tag/tls/lts}"
    echo "  DRY-RUN: git push origin refs/tags/${new_tag}:refs/tags/${new_tag}"
  done
else
  for old_tag in "${OLD_TAGS[@]}"; do
    new_tag="${old_tag/tls/lts}"
    git push origin "refs/tags/${new_tag}:refs/tags/${new_tag}"
    expected_commit="$(git rev-parse "${new_tag}^{commit}")"
    remote_commit="$(git ls-remote origin "refs/tags/${new_tag}^{}" | awk 'NR == 1 { print $1 }')"
    if [[ "$remote_commit" != "$expected_commit" ]]; then
      echo "  ERROR remote ${new_tag} resolves to ${remote_commit:-missing}, expected $expected_commit" >&2
      exit 1
    fi
    echo "  OK pushed and verified $new_tag (${expected_commit:0:8})"
  done
fi

echo "  NOTE: renamed historical tags may not contain a workflow that matches v*-lts-*."
echo "        Rebuild them with the fixed workflow_dispatch path; do not rely on tag push events."

echo ""

# ─── Phase 3: Migrate GitHub Releases ───
echo "━━━ Phase 3: Migrate GitHub Releases ━━━"

if [[ "$SKIP_RELEASES" == "true" ]]; then
  echo "  SKIPPED (--skip-releases)"
elif ! command -v gh >/dev/null 2>&1; then
  echo "  ERROR gh CLI is required unless --skip-releases is used" >&2
  exit 1
else
  for old_tag in "${OLD_TAGS[@]}"; do
    new_tag="${old_tag/tls/lts}"
    echo "  Processing $old_tag → $new_tag ..."

    if [[ "$DRY_RUN" == "true" ]]; then
      echo "    DRY-RUN: copy and verify release $old_tag → $new_tag, then delete $old_tag"
      continue
    fi

    # Fail closed: an authentication/permission error must not look like a missing release.
    gh release view "$old_tag" -R "$REPO" >/dev/null

    # Get old release title and notes
    old_title="$(gh release view "$old_tag" -R "$REPO" --json name -q .name)"
    old_notes="$(gh release view "$old_tag" -R "$REPO" --json body -q .body)"

    # Fix title and notes: replace tls → lts
    new_title="$(printf '%s' "$old_title" | sed 's/tls/lts/g; s/TLS/LTS/g')"
    new_notes="$(printf '%s' "$old_notes" | sed 's/tls/lts/g; s/TLS/LTS/g')"

    # Download assets to temp dir
    tmp_dir="$(mktemp -d)"
    notes_file="$(mktemp)"
    trap 'rm -rf "$tmp_dir"; rm -f "$notes_file"' EXIT
    gh release download "$old_tag" -R "$REPO" --dir "$tmp_dir" --clobber
    shopt -s nullglob
    assets=("$tmp_dir"/*)
    if [[ ${#assets[@]} -eq 0 ]]; then
      echo "    ERROR old release $old_tag has no downloadable assets" >&2
      exit 1
    fi

    printf '%s' "$new_notes" > "$notes_file"
    if gh release view "$new_tag" -R "$REPO" >/dev/null 2>&1; then
      gh release edit "$new_tag" -R "$REPO" --title "$new_title" --notes-file "$notes_file"
    else
      gh release create "$new_tag" -R "$REPO" --title "$new_title" --notes-file "$notes_file"
    fi

    # Upload and read back before removing the old release.
    gh release upload "$new_tag" -R "$REPO" "${assets[@]}" --clobber
    uploaded_count="$(gh release view "$new_tag" -R "$REPO" --json assets --jq '.assets | length')"
    if [[ "$uploaded_count" -ne ${#assets[@]} ]]; then
      echo "    ERROR release $new_tag has $uploaded_count assets, expected ${#assets[@]}" >&2
      exit 1
    fi
    echo "    Uploaded and verified ${#assets[@]} transitional assets"

    gh release delete "$old_tag" -R "$REPO" --yes
    echo "    Deleted old release $old_tag after new release verification"

    rm -rf "$tmp_dir"
    rm -f "$notes_file"
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
  # Delete and read back old remote tags before removing the local recovery refs.
  for old_tag in "${OLD_TAGS[@]}"; do
    git push origin ":refs/tags/$old_tag"
    set +e
    git ls-remote --exit-code origin "refs/tags/$old_tag" >/dev/null
    readback_status=$?
    set -e
    case "$readback_status" in
      2)
        echo "  OK deleted remote tag $old_tag"
        ;;
      0)
        echo "  ERROR remote tag $old_tag still exists" >&2
        exit 1
        ;;
      *)
        echo "  ERROR could not read back remote tag $old_tag" >&2
        exit 1
        ;;
    esac
  done

  for old_tag in "${OLD_TAGS[@]}"; do
    git tag -d "$old_tag"
  done
  echo "  OK deleted ${#OLD_TAGS[@]} local tags"
fi

echo ""
echo "━━━ Migration complete ━━━"
echo ""
echo "Remaining manual steps:"
echo "  1. Update workflow files: v*-tls-* → v*-lts-*"
echo "  2. Update scripts/generate-lts-release-notes.sh"
echo "  3. Update docs/lts/sync-runbook.md"
echo "  4. Update test files"
echo "  5. Rebuild every new Release from its exact tag with workflow_dispatch."
echo "     Copied assets are transitional and do not prove corrected version/provenance."
echo "  6. Build and verify v*-lts-* GHCR images before deleting any v*-tls-* images."
echo "  7. Repeat for CPA-Panel-LTS if needed"
