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

# Boolean: does a member checkout exist at <setdir>/<member>? Plain path
# existence — deliberately NOT `git worktree list` (never aborts under
# set -e either way, but `git worktree list`'s admin entries in
# .git/worktrees linger until an explicit `git worktree prune`, so
# list-based detection can report a stale "present" for a directory that
# was already removed on disk).
pnwf_worktree_present() {
  local setdir="$1" member="$2"
  [ -e "$setdir/$member" ]
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

# Prints the integer count of commits <branch> has that are not on
# <primary> (git rev-list --count <primary>..<branch>). Callers compare the
# printed value themselves (e.g. `[ "$(pnwf_ahead_of_primary ...)" -gt 0 ]`).
# On a guarded rev-list failure (e.g. an absent ref), nothing is printed to
# STDOUT (no bogus count) and the captured rc is returned without aborting
# under set -e; a diagnostic goes to stderr, matching every other guarded
# relay in this file (pnwf_working_tree_dirty, pnwf_resolve_primary_branch,
# pnwf_strategy, pnwf_topo_order) — needed so a caller/test can tell "the
# guard caught this and returned cleanly" apart from "the git call aborted
# the function via errexit before this point," which are NOT otherwise
# distinguishable: a bare `count=$(cmd)` failing without `|| rc=$?` also
# propagates that same rc as the function's own return value under set -e.
# git's own raw diagnostic is discarded (2>/dev/null on the git call) so
# stderr carries exactly one, first-party message.
pnwf_ahead_of_primary() {
  local repo_dir="$1" branch="$2" primary="$3" rc=0 count
  count=$(git -C "$repo_dir" rev-list --count "${primary}..${branch}" 2>/dev/null) || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_ahead_of_primary: git rev-list failed unexpectedly (rc=$rc)" >&2
    return "$rc"
  fi
  echo "$count"
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

# Classifies one workforest member's landing status from git state alone
# (never aborts under set -e; backs `pnwf status`). Prints ONE line:
# "<label>\t<reason>", label one of: landed | not-started | blocked | kept.
#
# Args: setdir member canonical_dir branch primary
#
# Design note: ahead==0 (git rev-list --count primary..branch) is logically
# EQUIVALENT to is-ancestor(branch, primary)==true, so a PRESENT worktree can
# never simultaneously be "not landed" and "zero ahead" -- that combination
# does not occur in real git state. "landed" is therefore derived ONLY from
# worktree-absence (the one unambiguous signal: FF-4 completed, or a prior
# cleanup pass already removed it); a present worktree with zero-ahead is
# "not-started" (no work recorded yet in this repo). This also means the
# merge-base ancestor check is unnecessary here: `pnwf_ahead_of_primary`
# alone gives the same absent-ref (128) signal as `merge-base --is-ancestor`
# (both fail identically on an unresolvable ref), one guarded git call
# instead of two.
pnwf_classify_member() {
  local setdir="$1" member="$2" canonical_dir="$3" branch="$4" primary="$5"
  local setpath="$setdir/$member"

  if ! pnwf_worktree_present "$setdir" "$member"; then
    printf '%s\t%s\n' "landed" "worktree removed (landed)"
    return 0
  fi

  local dirty_rc=0
  pnwf_working_tree_dirty "$setpath" || dirty_rc=$?
  case "$dirty_rc" in
  0)
    printf '%s\t%s\n' "blocked" "working tree has uncommitted changes"
    return 0
    ;;
  1) : ;;
  *)
    echo "pnwf_classify_member: dirty check failed unexpectedly (rc=$dirty_rc)" >&2
    return "$dirty_rc"
    ;;
  esac

  # stderr discarded on this call: 128 (absent ref) is an EXPECTED case here
  # (reported below as "blocked", not a genuine error), so
  # pnwf_ahead_of_primary's own "failed unexpectedly" diagnostic for that rc
  # would otherwise leak into a caller capturing combined stdout+stderr
  # (e.g. bats' `run`) ahead of the clean "blocked\t…" line this function
  # promises — same rationale as pnwf_is_ancestor_of_primary discarding raw
  # git stderr on its own 128 path. A truly unexpected rc still gets an
  # explicit diagnostic via the `*)` branch below.
  local ahead ahead_rc=0
  ahead=$(pnwf_ahead_of_primary "$canonical_dir" "$branch" "$primary" 2>/dev/null) || ahead_rc=$?
  case "$ahead_rc" in
  0)
    if [ "$ahead" -eq 0 ]; then
      printf '%s\t%s\n' "not-started" "no commits ahead of $primary"
    else
      printf '%s\t%s\n' "kept" "$ahead commit(s) ahead of $primary, not yet landed"
    fi
    ;;
  128)
    printf '%s\t%s\n' "blocked" "member branch '$branch' not found in $canonical_dir although its worktree is present"
    ;;
  *)
    echo "pnwf_classify_member: ahead-of-primary check failed unexpectedly (rc=$ahead_rc)" >&2
    return "$ahead_rc"
    ;;
  esac
}

# Fetches origin then attempts to rebase repo_dir's current branch onto
# origin/<primary>. Backs `pnwf sync-fetch` -- the one MUTATING WORK-recipe
# helper in this file (every other function above is a read-only probe).
#
# Returns 0 on a clean pass (already up to date counts as clean). A nonzero
# return is the FIRST stopping point the caller must hand off on, and the
# CODE itself tells the caller WHICH step stopped it -- the two failures
# need different recovery advice, since a fetch failure never starts a
# rebase (so "git rebase --continue" would be actively wrong there):
#   2  `git fetch origin` failed (network/remote/auth -- no rebase started)
#   3  `git rebase origin/<primary>` failed (typically a conflict) -- git
#      itself leaves `.git/rebase-merge` (or `-apply`) in place in
#      repo_dir, deliberately NOT cleaned up here (no `git rebase
#      --abort`), so the caller's hand-off message ("resolve here, then
#      `git rebase --continue`") points at exactly the state this function
#      stopped in.
# Never aborts the caller under set -e; a first-party diagnostic naming
# which git step failed AND its real exit code goes to stderr, while git's
# own chatter (e.g. the conflicting paths) is left on its normal stdout/
# stderr for whoever resolves it.
pnwf_fetch_and_rebase() {
  local repo_dir="$1" primary="$2" rc=0
  git -C "$repo_dir" fetch origin || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_fetch_and_rebase: git fetch origin failed in $repo_dir (rc=$rc)" >&2
    return 2
  fi

  rc=0
  git -C "$repo_dir" rebase "origin/$primary" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "pnwf_fetch_and_rebase: git rebase origin/$primary failed in $repo_dir (rc=$rc)" >&2
    return 3
  fi
}
