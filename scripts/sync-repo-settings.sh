#!/usr/bin/env bash
# Sync GitHub repo settings across the nix-* family to a canonical set.
#
# Why: settings drift over time and across new repos (default GitHub config
# diverges from how we actually want to use the repos — e.g. allow_auto_merge
# defaults to false so `gh pr merge --auto` silently no-ops). This script is
# the single source of truth for nix-* repo settings; re-run it whenever a
# new repo is added or settings drift is suspected.
#
# Scope: the four nix-* repos under phillipgreenii/*. ha-addon-esphome-mcp
# and homelab are intentionally out of scope (homelab is on Forgejo, not
# GitHub; ha-addon-* is a separate HA-add-on family).
#
# Usage:
#   scripts/sync-repo-settings.sh            # audit + apply
#   scripts/sync-repo-settings.sh --dry-run  # audit only, no writes
#   scripts/sync-repo-settings.sh --audit    # alias for --dry-run
#
# Requires: gh CLI authenticated to github.com with `repo` scope.
# Source of truth: tc-olcz3.

set -euo pipefail

REPOS=(
  phillipgreenii/nix-repo-base
  phillipgreenii/nix-overlay
  phillipgreenii/nix-personal
  phillipgreenii/nix-agent-support
)

# Canonical settings. Single set applied to all four repos. Visibility is
# NOT enforced (nix-personal stays private; others stay public — that's the
# operator's decision per repo, not a fleet-wide knob).
#
# Rationale per key:
#   has_projects/has_wiki/has_discussions=false: unused, removes UI clutter.
#   has_issues=true: used for bug reports + RFC-style discussions.
#   allow_squash_merge/allow_merge_commit=false + allow_rebase_merge=true:
#     enforces linear history matching the local ff-merge workflow.
#   allow_auto_merge=true: enables `gh pr merge --auto` (this was the
#     original tc-21ql1 trigger for tc-olcz3).
#   delete_branch_on_merge=true: keeps the branch list clean post-merge.
#   allow_update_branch=true: lets PRs auto-rebase when main moves ahead.
declare -A SETTINGS=(
  [has_issues]=true
  [has_projects]=false
  [has_wiki]=false
  [has_discussions]=false
  [allow_squash_merge]=false
  [allow_merge_commit]=false
  [allow_rebase_merge]=true
  [allow_auto_merge]=true
  [delete_branch_on_merge]=true
  [allow_update_branch]=true
  [web_commit_signoff_required]=false
)

DRY_RUN=0
case "${1:-}" in
--dry-run | --audit) DRY_RUN=1 ;;
'') ;;
*)
  echo "Unknown flag: $1" >&2
  echo "Usage: $0 [--dry-run | --audit]" >&2
  exit 2
  ;;
esac

# Keys that GitHub rejects on private repos without a paid plan. These are
# silently ignored by the API (PATCH returns success, but the field stays at
# its prior value). Skip auditing them on private repos so the script doesn't
# report perpetual drift.
PRIVATE_REPO_UNSUPPORTED=(allow_auto_merge)

is_unsupported_on_private() {
  local key="$1"
  for unsupported in "${PRIVATE_REPO_UNSUPPORTED[@]}"; do
    [[ $unsupported == "$key" ]] && return 0
  done
  return 1
}

audit_repo() {
  local repo="$1"
  echo "=== $repo ==="
  local current
  current=$(gh api "repos/$repo" 2>&1)
  local visibility
  visibility=$(echo "$current" | jq -r '.visibility')
  local drift=0
  for key in "${!SETTINGS[@]}"; do
    if [[ $visibility == "private" ]] && is_unsupported_on_private "$key"; then
      continue
    fi
    local want="${SETTINGS[$key]}"
    local got
    got=$(echo "$current" | jq -r ".$key")
    if [[ $want != "$got" ]]; then
      printf "  DRIFT: %s = %s (want: %s)\n" "$key" "$got" "$want"
      drift=$((drift + 1))
    fi
  done
  if [[ $drift -eq 0 ]]; then
    echo "  OK (matches canonical)"
  fi
  return $drift
}

apply_repo() {
  local repo="$1"
  local visibility="$2"
  local jq_args=()
  local jq_expr='{}'
  for key in "${!SETTINGS[@]}"; do
    if [[ $visibility == "private" ]] && is_unsupported_on_private "$key"; then
      continue
    fi
    jq_args+=(--argjson "$key" "${SETTINGS[$key]}")
    jq_expr+=" + {$key: \$$key}"
  done
  local payload
  payload=$(jq -nc "${jq_args[@]}" "$jq_expr")
  echo "  applying: $payload"
  gh api --silent -X PATCH "repos/$repo" --input - <<<"$payload"
  echo "  done"
}

total_drift=0
for repo in "${REPOS[@]}"; do
  if audit_repo "$repo"; then
    repo_drift=0
  else
    repo_drift=$?
  fi
  total_drift=$((total_drift + repo_drift))
  if [[ $DRY_RUN -eq 0 && $repo_drift -gt 0 ]]; then
    visibility=$(gh api "repos/$repo" --jq '.visibility')
    apply_repo "$repo" "$visibility"
  fi
done

echo
if [[ $DRY_RUN -eq 1 ]]; then
  echo "DRY-RUN: $total_drift drifted settings across ${#REPOS[@]} repos. Re-run without --dry-run to apply."
elif [[ $total_drift -eq 0 ]]; then
  echo "All ${#REPOS[@]} repos already match canonical."
else
  echo "Applied canonical to $total_drift drifted settings across ${#REPOS[@]} repos."
fi
