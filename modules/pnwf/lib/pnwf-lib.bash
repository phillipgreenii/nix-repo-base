# shellcheck shell=bash

# pnwf-lib: guarded git/pn primitives shared by every `pnwf` subcommand.
#
# Every function here is called from scripts running under the builder's
# injected `set -euo pipefail`. git (and jq) commands whose exit code IS the
# meaningful boolean/tri-state signal (not-found, not-an-ancestor, absent
# ref, ...) MUST NOT be allowed to trip `errexit` on that "expected failure".
# The shape used throughout:
#
#   rc=0
#   git ... || rc=$?
#   case "$rc" in
#     <expected-code>) ... ;;
#     *) echo "..." >&2; return "$rc" ;;   # genuine, unexpected error
#   esac
#
# `pnwf_is_ancestor_of_primary` and `pnwf_branch_exists` are the two guard
# primitives this task exists to get right; the rest follow the same shape.

# Prints: landed | not-landed | absent  (never aborts under set -e)
#
# stderr is discarded on the git call: unlike `rev-parse --verify --quiet`,
# `merge-base --is-ancestor` has no quiet flag, and its "fatal: Not a valid
# object name" chatter on rc=128 would otherwise leak into the caller's
# captured output alongside the clean "absent" token this function promises
# (e.g. under bats' `run`, which merges stdout+stderr by default). A truly
# unexpected rc still gets an explicit diagnostic via the `*)` branch below.
pnwf_is_ancestor_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0
  git -C "$repo_dir" merge-base --is-ancestor "$branch" "$primary" 2>/dev/null || rc=$?
  case "$rc" in
  0) echo "landed" ;;
  1) echo "not-landed" ;;
  128) echo "absent" ;;
  *)
    echo "pnwf_is_ancestor_of_primary: git merge-base failed unexpectedly (rc=$rc)" >&2
    return "$rc"
    ;;
  esac
}

# Boolean: does refs/heads/<branch> exist in repo_dir? (never aborts under set -e)
pnwf_branch_exists() {
  local repo_dir="$1" branch="$2" rc=0
  git -C "$repo_dir" rev-parse --verify --quiet "refs/heads/$branch" >/dev/null || rc=$?
  [ "$rc" -eq 0 ]
}

# Boolean: is there a worktree (main or linked) checked out for <branch>?
# (never aborts under set -e)
pnwf_worktree_present() {
  local repo_dir="$1" branch="$2" rc=0
  git -C "$repo_dir" worktree list --porcelain | grep -qx "branch refs/heads/${branch}" || rc=$?
  [ "$rc" -eq 0 ]
}

# Boolean: does repo_dir have uncommitted changes (staged, unstaged, or
# untracked)? A genuine `git status` failure (e.g. repo_dir is not a git
# repo) propagates its own rc rather than being reported as "clean".
pnwf_working_tree_dirty() {
  local repo_dir="$1" rc=0 status_output
  status_output=$(git -C "$repo_dir" status --porcelain) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_working_tree_dirty: git status failed (rc=$rc)" >&2
    return "$rc"
  fi
  [ -n "$status_output" ]
}

# Boolean: does <branch> have at least one commit not on <primary>?
# (never aborts under set -e; a genuine rev-list failure, e.g. an absent
# ref, propagates its own rc)
pnwf_ahead_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0 count
  count=$(git -C "$repo_dir" rev-list --count "${primary}..${branch}") || rc=$?
  case "$rc" in
  0) [ "$count" -gt 0 ] ;;
  *)
    echo "pnwf_ahead_of_primary: git rev-list failed unexpectedly (rc=$rc)" >&2
    return "$rc"
    ;;
  esac
}

# Boolean: is repo_dir currently on <primary> AND clean? This is the R-3
# steady-state check for the canonical clone (see repo CLAUDE.md's Git
# Worktree / Integration Discipline rules). Detached HEAD, a different
# branch, or a dirty tree all classify as false without aborting.
pnwf_canonical_on_primary_and_clean() {
  local repo_dir="$1" primary="$2" branch_rc=0 dirty_rc=0 current
  current=$(git -C "$repo_dir" symbolic-ref --quiet --short HEAD) || branch_rc=$?
  case "$branch_rc" in
  0) : ;;
  1) return 1 ;; # detached HEAD: not "on" any branch
  *)
    echo "pnwf_canonical_on_primary_and_clean: git symbolic-ref failed unexpectedly (rc=$branch_rc)" >&2
    return "$branch_rc"
    ;;
  esac

  [ "$current" = "$primary" ] || return 1

  pnwf_working_tree_dirty "$repo_dir" || dirty_rc=$?
  case "$dirty_rc" in
  0) return 1 ;; # dirty
  1) return 0 ;; # clean
  *)
    echo "pnwf_canonical_on_primary_and_clean: working-tree check failed unexpectedly (rc=$dirty_rc)" >&2
    return "$dirty_rc"
    ;;
  esac
}

# Prints the resolved primary branch name. integrate-branch-support is
# called BARE (no --json flag — it emits JSON unconditionally) and already
# implements the git-config -> symbolic-ref -> "main" resolution chain;
# this is a thin, guarded relay over its `.primary_branch` field.
pnwf_resolve_primary_branch() {
  local repo_dir="$1" rc=0 primary
  primary=$(cd "$repo_dir" && integrate-branch-support | jq -r .primary_branch) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_resolve_primary_branch: integrate-branch-support failed (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$primary"
}

# Prints the resolved integration strategy (e.g. "ff-merge-to-main",
# "pull-request"), or the literal string "null" when integrate-branch-support
# has not declared one. Same guarded relay shape as pnwf_resolve_primary_branch.
pnwf_strategy() {
  local repo_dir="$1" rc=0 strategy
  strategy=$(cd "$repo_dir" && integrate-branch-support | jq -r '.strategy // "null"') || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_strategy: integrate-branch-support failed (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$strategy"
}

# Prints each repo name in the workforest set lock's topological order,
# one per line (jq -r '.order[]').
pnwf_topo_order() {
  local lock_file="$1" rc=0
  jq -r '.order[]' "$lock_file" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_topo_order: failed to read .order from $lock_file (rc=$rc)" >&2
    return "$rc"
  fi
}
