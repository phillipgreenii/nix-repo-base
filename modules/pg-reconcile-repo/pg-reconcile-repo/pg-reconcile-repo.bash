# shellcheck shell=bash

die_usage() {
  echo "pg-reconcile-repo: $1" >&2
  exit 2
}

# pgrr_resolve_primary REPO: configured primary, else origin/HEAD, else main.
pgrr_resolve_primary() {
  local repo="$1" p
  p="$(git -C "$repo" config pgii-integrate-branch.primaryBranch 2>/dev/null || true)"
  if [[ -z $p ]]; then
    p="$(git -C "$repo" symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null || true)"
    p="${p#origin/}"
  fi
  printf '%s\n' "${p:-main}"
}

# pgrr_steady_state REPO PRIMARY: 0 iff on PRIMARY, clean, no operation in progress.
pgrr_steady_state() {
  local repo="$1" primary="$2" gitdir marker
  [[ "$(git -C "$repo" branch --show-current)" == "$primary" ]] || return 1
  [[ -z "$(git -C "$repo" status --porcelain)" ]] || return 1
  gitdir="$(git -C "$repo" rev-parse --absolute-git-dir)"
  for marker in rebase-merge rebase-apply MERGE_HEAD CHERRY_PICK_HEAD; do
    [[ ! -e "$gitdir/$marker" ]] || return 1
  done
}

# Scratch state for the cleanup trap.
PGRR_WT=""
PGRR_TMP_BRANCH=""
PGRR_SCRATCH=""

pgrr_cleanup() {
  if [[ -n $PGRR_WT && -d $PGRR_WT ]]; then
    git -C "$REPO" worktree remove --force "$PGRR_WT" >/dev/null 2>&1 || true
  fi
  if [[ -n $PGRR_TMP_BRANCH ]]; then
    git -C "$REPO" branch -D "$PGRR_TMP_BRANCH" >/dev/null 2>&1 || true
  fi
  git -C "$REPO" worktree prune >/dev/null 2>&1 || true
  if [[ -n $PGRR_SCRATCH ]]; then
    rm -rf "$PGRR_SCRATCH"
  fi
}

pgrr_main() {
  local primary old new counts ahead behind ref
  primary="${PRIMARY:-$(pgrr_resolve_primary "$REPO")}"
  ref="refs/heads/$primary"

  if ! pgrr_steady_state "$REPO" "$primary"; then
    echo "pg-reconcile-repo: $REPO is not in steady state (need: on $primary, clean, no rebase/merge/cherry-pick in progress)" >&2
    exit 3
  fi

  if ! git -C "$REPO" fetch origin >&2; then
    echo "pg-reconcile-repo: git fetch origin failed" >&2
    exit 8
  fi
  if ! git -C "$REPO" rev-parse --verify --quiet "refs/remotes/origin/$primary" >/dev/null; then
    echo "pg-reconcile-repo: origin/$primary does not resolve" >&2
    exit 8
  fi

  counts="$(git -C "$REPO" rev-list --left-right --count "$primary...origin/$primary")"
  read -r ahead behind <<<"$counts"
  old="$(git -C "$REPO" rev-parse "$ref")"

  if ((ahead == 0 && behind == 0)); then
    echo "up-to-date"
    return 0
  fi
  if ((behind == 0)); then
    echo "ahead-only"
    return 0
  fi
  if ((ahead == 0)); then
    git -C "$REPO" merge --ff-only "origin/$primary" >&2
    echo "fast-forwarded $old $(git -C "$REPO" rev-parse "$ref")"
    return 0
  fi

  # Diverged. Step 1: rebase in a temporary worktree outside REPO.
  PGRR_SCRATCH="$(mktemp -d)"
  trap pgrr_cleanup EXIT
  PGRR_WT="$PGRR_SCRATCH/wt"
  PGRR_TMP_BRANCH="pg-reconcile-repo-$$"
  git -C "$REPO" worktree add -q -b "$PGRR_TMP_BRANCH" "$PGRR_WT" "$old" >&2

  if ! git -C "$PGRR_WT" rebase "origin/$primary" >&2; then
    git -C "$PGRR_WT" diff --name-only --diff-filter=U | sed 's/^/conflict: /' >&2
    git -C "$PGRR_WT" rebase --abort >/dev/null 2>&1 || true
    echo "pg-reconcile-repo: rebase conflict; nothing changed" >&2
    exit 4
  fi
  new="$(git -C "$PGRR_WT" rev-parse HEAD)"

  if [[ -n $CHECK_CMD ]]; then
    # shellcheck disable=SC2086  # the check command is a word-split command line by design
    if ! (cd "$PGRR_WT" && $CHECK_CMD) >&2; then
      echo "pg-reconcile-repo: check command failed on the rebased tip; nothing changed" >&2
      exit 5
    fi
  fi

  # Step 2: compare-and-swap in the canonical clone, then move its working tree.
  if ! pgrr_steady_state "$REPO" "$primary"; then
    echo "pg-reconcile-repo: $REPO left steady state during the rebase; nothing changed" >&2
    exit 3
  fi
  if ! git -C "$REPO" update-ref "$ref" "$new" "$old" >&2; then
    echo "pg-reconcile-repo: compare-and-swap refused ($primary moved); nothing changed" >&2
    exit 6
  fi
  # The ref already points at $new, so `git reset --keep $new` would be a no-op
  # (HEAD == target); a two-way read-tree from $old to $new is what actually
  # moves index and working tree, and like --keep it refuses to clobber local changes.
  if ! git -C "$REPO" read-tree -m -u "$old" "$new" >&2; then
    git -C "$REPO" update-ref "$ref" "$old" "$new" >&2 || true
    git -C "$REPO" reset --keep "$old" >&2 || true
    echo "pg-reconcile-repo: working-tree move failed; ref restored" >&2
    exit 7
  fi

  # Step 3 (worktree and branch removal) runs in the EXIT trap.
  echo "reconciled $old $new"
}
