# shellcheck shell=bash

# pnwf: deterministic helper for the workforest work-cycle (fork/validate/
# land/cleanup). Thin layer over `pn` + `git`: reads `pn workspace info
# --json` and a workspace's own `pn-workspace.lock.json`, and shells guarded
# git primitives from pnwf-lib.bash. Never re-derives the workspace graph.

show_help() {
  cat <<'HELP'
pnwf: deterministic helper for the workforest work-cycle

Usage: pnwf <subcommand> [OPTIONS]

Subcommands (read-only, implemented):
  resolve [--set]   Print {canonical_root, in_workforest, set_dir,
                     pn_workspace_root} as JSON for the resolved workspace.
  repos [--set]     Print member repo keys, one per line, in the resolved
                     workspace's own topological order.
  stage [--set]     Print the resolved set's lifecycle stage:
                     work | ready-to-land | resuming-land | landed.

Subcommands (not yet implemented):
  fork-preflight    Pre-flight checks before forking a workforest set.
  land-plan         Topo-ordered repos still needing landing.
  cleanup           Best-effort teardown after landing.
  status            Per-repo status table (landed / blocked / kept).
  sync-fetch        Fetch + rebase onto the remote primary branch.

Options:
  -h, --help        Show this help message
  -v, --version     Show version information

--set (on resolve/repos/stage): guard — exit non-zero unless the resolved
workspace is inside a coordinated workforest set (in_workforest=true).

Examples:
  pnwf resolve
  pnwf repos --set
  pnwf stage --set
HELP
}

die() {
  echo "pnwf: error: $1" >&2
  exit "${2:-1}"
}

# Runs `pn workspace info --json` with any inherited PN_WORKSPACE_ROOT
# cleared first (H2 / CRUX guard). `pn` honors an exported PN_WORKSPACE_ROOT
# BEFORE its cwd upward-walk, so a stale value left over from a prior shell
# (e.g. still pointing at the canonical root) would make this read the
# CANONICAL workspace even while cwd is inside a set — silently defeating
# every location guard below. Every subcommand MUST route through this
# helper rather than calling `pn workspace info --json` directly.
_pnwf_info_json() {
  env -u PN_WORKSPACE_ROOT pn workspace info --json
}

# Shared location guard for resolve/repos/stage: when require_set=1, dies
# unless in_workforest is exactly "true". Centralizing this keeps the guard
# behavior (and its message) identical across all three subcommands.
_pnwf_require_set_guard() {
  local subcommand="$1" require_set="$2" in_workforest="$3" root="$4"
  if [[ $require_set -eq 1 && $in_workforest != "true" ]]; then
    die "$subcommand --set: expected to be inside a workforest set, but the resolved root ($root) is not in_workforest"
  fi
}

cmd_resolve() {
  local require_set=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --set)
      require_set=1
      ;;
    -h | --help)
      cat <<'HELP'
Usage: pnwf resolve [--set]

Print JSON: canonical_root, in_workforest, set_dir, pn_workspace_root.
pn_workspace_root is the explicit PN_WORKSPACE_ROOT=<value> that other
stages should pin to for this workspace (avoids re-walking cwd, and avoids
inheriting a stale exported PN_WORKSPACE_ROOT).

--set: exit non-zero unless the resolved workspace is inside a set.
HELP
      exit 0
      ;;
    *)
      die "resolve: unknown argument: $1"
      ;;
    esac
    shift
  done

  local info in_workforest canonical_root root set_dir
  info=$(_pnwf_info_json) || die "'pn workspace info --json' failed"
  in_workforest=$(printf '%s' "$info" | jq -r '.in_workforest')
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root')
  root=$(printf '%s' "$info" | jq -r '.root')

  _pnwf_require_set_guard resolve "$require_set" "$in_workforest" "$root"

  if [[ $in_workforest == "true" ]]; then
    set_dir="$root"
  else
    set_dir=""
  fi

  jq -n \
    --arg canonical_root "$canonical_root" \
    --argjson in_workforest "$in_workforest" \
    --arg set_dir "$set_dir" \
    --arg pn_workspace_root "$root" \
    '{
      canonical_root: $canonical_root,
      in_workforest: $in_workforest,
      set_dir: (if $set_dir == "" then null else $set_dir end),
      pn_workspace_root: $pn_workspace_root
    }'
}

cmd_repos() {
  local require_set=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --set)
      require_set=1
      ;;
    -h | --help)
      cat <<'HELP'
Usage: pnwf repos [--set]

Print member repo keys, one per line, in the resolved workspace's own
topological order (read from its own pn-workspace.lock.json — a subset
set's own lock lists only its members).

--set: exit non-zero unless the resolved workspace is inside a set.
HELP
      exit 0
      ;;
    *)
      die "repos: unknown argument: $1"
      ;;
    esac
    shift
  done

  local info in_workforest root lock_file
  info=$(_pnwf_info_json) || die "'pn workspace info --json' failed"
  in_workforest=$(printf '%s' "$info" | jq -r '.in_workforest')
  root=$(printf '%s' "$info" | jq -r '.root')

  _pnwf_require_set_guard repos "$require_set" "$in_workforest" "$root"

  lock_file="$root/pn-workspace.lock.json"
  [[ -f $lock_file ]] || die "lock file not found: $lock_file"

  pnwf_topo_order "$lock_file"
}

cmd_stage() {
  local require_set=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --set)
      require_set=1
      ;;
    -h | --help)
      cat <<'HELP'
Usage: pnwf stage [--set]

Print the resolved set's lifecycle stage, derived from git:
  work           uncommitted changes in a present member worktree
  ready-to-land  clean, and every member worktree present, and >=1 member
                 branch is ahead of primary
  resuming-land  some member worktrees are absent AND >=1 member branch
                 (present or absent) is not landed
  landed         every member branch is an ancestor of primary, or gone

--set: exit non-zero unless the resolved workspace is inside a set.
HELP
      exit 0
      ;;
    *)
      die "stage: unknown argument: $1"
      ;;
    esac
    shift
  done

  local info in_workforest root canonical_root lock_file
  info=$(_pnwf_info_json) || die "'pn workspace info --json' failed"
  in_workforest=$(printf '%s' "$info" | jq -r '.in_workforest')
  root=$(printf '%s' "$info" | jq -r '.root')
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root')

  _pnwf_require_set_guard stage "$require_set" "$in_workforest" "$root"

  lock_file="$root/pn-workspace.lock.json"
  [[ -f $lock_file ]] || die "lock file not found: $lock_file"

  # The set lives at <workforests_dir>/<branch> (pn's WorkforestAdd), so the
  # branch name is the resolved root's own last path segment. Pure parameter
  # expansion (no `basename` process) — root is always an absolute path with
  # no trailing slash per `pn workspace info --json`.
  local branch="${root##*/}"

  local members=()
  mapfile -t members < <(pnwf_topo_order "$lock_file")
  [[ ${#members[@]} -gt 0 ]] || die "no members found in $lock_file"

  local any_dirty=0 any_worktree_absent=0 any_unlanded=0 any_ahead=0
  local member member_setpath member_canonical primary ahead status

  for member in "${members[@]}"; do
    member_setpath="$root/$member"
    member_canonical="$canonical_root/$member"

    primary=$(pnwf_resolve_primary_branch "$member_canonical") ||
      die "could not resolve primary branch for member '$member'"

    if pnwf_worktree_present "$root" "$member"; then
      if pnwf_working_tree_dirty "$member_setpath"; then
        any_dirty=1
      fi
      ahead=$(pnwf_ahead_of_primary "$member_setpath" "$branch" "$primary") ||
        die "could not compute ahead-of-primary for member '$member'"
      if [[ $ahead -gt 0 ]]; then
        any_ahead=1
      fi
    else
      any_worktree_absent=1
    fi

    status=$(pnwf_is_ancestor_of_primary "$member_canonical" "$branch" "$primary") ||
      die "could not resolve ancestor status for member '$member'"
    if [[ $status == "not-landed" ]]; then
      any_unlanded=1
    fi
  done

  if [[ $any_dirty -eq 1 ]]; then
    echo "work"
  elif [[ $any_worktree_absent -eq 1 && $any_unlanded -eq 1 ]]; then
    echo "resuming-land"
  elif [[ $any_unlanded -eq 0 ]]; then
    echo "landed"
  elif [[ $any_ahead -eq 1 ]]; then
    echo "ready-to-land"
  else
    die "could not determine lifecycle stage for branch '$branch'"
  fi
}

# --- Top-level arg parsing + dispatch --------------------------------------

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    break
    ;;
  --*)
    die "unknown option: $1"
    ;;
  *)
    break
    ;;
  esac
  shift
done

if [[ $# -eq 0 ]]; then
  show_help
  exit 1
fi

SUBCOMMAND="$1"
shift

# Dispatch table listing ALL EIGHT future pnwf subcommands (design spec
# §4.5); only resolve/repos/stage are implemented in this task — the rest
# are wired here so the surface is stable, and print a clear, non-zero
# "not yet implemented" until a later task fills them in.
case "$SUBCOMMAND" in
resolve) cmd_resolve "$@" ;;
repos) cmd_repos "$@" ;;
stage) cmd_stage "$@" ;;
fork-preflight | land-plan | cleanup | status | sync-fetch)
  echo "pnwf: '$SUBCOMMAND' is not yet implemented" >&2
  exit 1
  ;;
*)
  die "unknown subcommand: $SUBCOMMAND"
  ;;
esac
