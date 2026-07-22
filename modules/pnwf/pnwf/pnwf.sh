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
  fork-preflight <branch> [--repos a,b]
                     Pre-flight checks before forking a workforest set on
                     <branch>. Prints "proceed", "resume", or "stop" on the
                     first line, followed by a reason line.
  land-plan <branch>
                     Print the topo-ordered member repos of <branch>'s set
                     that still need landing (present worktree, not an
                     ancestor of primary), one per line. Absent worktrees
                     (already landed) are skipped.
  cleanup <branch> [--force-dirty-worktree-removal] [--force-unlanded-branch-removal]
                     Best-effort teardown of <branch>'s set from the
                     canonical clone: removes worktree + branch for every
                     landed member, keeps (and reports) the rest. Removes
                     the set directory itself only when nothing was kept.
  status <branch>    Print a per-repo table for <branch>'s set: member,
                     label (landed/not-started/blocked/kept), reason.

Subcommands (not yet implemented):
  sync-fetch        Fetch + rebase onto the remote primary branch.

Options:
  -h, --help        Show this help message
  -v, --version     Show version information

--set (on resolve/repos/stage): guard — exit non-zero unless the resolved
workspace is inside a coordinated workforest set (in_workforest=true).

fork-preflight/land-plan/cleanup/status take <branch> explicitly and derive
the set directory from canonical_root + workforests_dir — they work whether
invoked from the canonical root or from inside any set.

Examples:
  pnwf resolve
  pnwf repos --set
  pnwf stage --set
  pnwf fork-preflight my-branch
  pnwf fork-preflight my-branch --repos repoA,repoB
  pnwf land-plan my-branch
  pnwf cleanup my-branch
  pnwf status my-branch
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

# Resolves <canonical_root> and the on-disk set directory for <branch> from
# the CURRENT `_pnwf_info_json`. canonical_root + workforests_dir are stable
# regardless of whether cwd is the canonical root or inside any set (the Go
# side derives canonical_root by walking back up out of a set — see
# modules/pn/internal/workspace/info.go's canonicalRoot()) — so
# fork-preflight/land-plan/cleanup/status work the same either way, and can
# name a DIFFERENT branch's set than the one cwd happens to be inside.
# Prints "canonical_root<TAB>set_dir".
_pnwf_resolve_set_dir() {
  local branch="$1" info canonical_root workforests_dir
  info=$(_pnwf_info_json) || die "'pn workspace info --json' failed"
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root')
  workforests_dir=$(printf '%s' "$info" | jq -r '.workforests_dir')
  printf '%s\t%s\n' "$canonical_root" "$canonical_root/$workforests_dir/$branch"
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

cmd_fork_preflight() {
  local branch="" repos_csv=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --repos)
      [[ $# -ge 2 ]] || die "fork-preflight: --repos requires a value"
      repos_csv="$2"
      shift
      ;;
    --repos=*)
      repos_csv="${1#--repos=}"
      ;;
    -h | --help)
      cat <<'HELP'
Usage: pnwf fork-preflight <branch> [--repos a,b]

Pre-flight checks before forking a coordinated workforest set on <branch>.
Prints one of "proceed", "resume", or "stop" on the first line, followed by
a reason line. Checks, in order (first match wins):
  1. not nested       — cwd must NOT already be inside a workforest set.
  2. canonical clean   — every checked repo must be on its primary branch
                          and clean (R-3/R-8; a stop-and-report, never a
                          warn-and-continue).
  3. resume detection   — the set dir and/or <branch> already existing in
                          any checked repo reports "resume" (the caller
                          decides resume-vs-discard, not this tool).
Otherwise: "proceed".

--repos a,b: restrict checks 2/3 to this comma-separated subset of the
canonical workspace's repos (default: all of them).
HELP
      exit 0
      ;;
    --*)
      die "fork-preflight: unknown argument: $1"
      ;;
    *)
      [[ -z $branch ]] || die "fork-preflight: unexpected extra argument: $1"
      branch="$1"
      ;;
    esac
    shift
  done
  [[ -n $branch ]] || die "fork-preflight: <branch> is required"

  local info in_workforest canonical_root workforests_dir
  info=$(_pnwf_info_json) || die "'pn workspace info --json' failed"
  in_workforest=$(printf '%s' "$info" | jq -r '.in_workforest')
  canonical_root=$(printf '%s' "$info" | jq -r '.canonical_root')
  workforests_dir=$(printf '%s' "$info" | jq -r '.workforests_dir')

  # (1) not nested.
  if [[ $in_workforest == "true" ]]; then
    echo "stop"
    echo "reason: cwd is already inside a workforest set (in_workforest=true); fork-preflight must run from the canonical workspace root"
    return 0
  fi

  # Repos to check: --repos filter, else every repo in the canonical info.
  local repo_tsv
  # `. as $r` binds the current repos[] object to a variable BEFORE the
  # `$want | index(...)` pipe below; that pipe rebinds `.` to $want itself
  # for evaluating its argument, so a bare `.name` there would try to index
  # the $want ARRAY (a jq type error) rather than the repo object -- $r.name
  # is a variable reference and stays correct regardless of the ambient `.`.
  repo_tsv=$(printf '%s' "$info" | jq -r --arg repos_csv "$repos_csv" '
    (if $repos_csv == "" then null else ($repos_csv | split(",")) end) as $want
    | .repos[] | . as $r
    | select($want == null or ($want | index($r.name)) != null)
    | [$r.name, $r.path] | @tsv
  ')

  # (2) canonical on primary + clean, for every checked repo.
  local bad_repos="" name path primary
  while IFS=$'\t' read -r name path; do
    [[ -n $name ]] || continue
    primary=$(pnwf_resolve_primary_branch "$path") ||
      die "could not resolve primary branch for repo '$name'"
    if ! pnwf_canonical_on_primary_and_clean "$path" "$primary"; then
      bad_repos+="${bad_repos:+, }$name (expected on '$primary', clean)"
    fi
  done <<<"$repo_tsv"

  if [[ -n $bad_repos ]]; then
    echo "stop"
    echo "reason: canonical is not clean/on-primary for: $bad_repos"
    return 0
  fi

  # (3) resume detection: the set dir itself, or the branch already existing
  # in any checked repo (`pn workspace workforest add` errors on an existing
  # set dir and reuses an existing branch's stale tip — see workforest.go's
  # pre-flight; pnwf reports it and leaves resume-vs-discard to the caller).
  local resume_reasons=""
  if pnwf_worktree_present "$canonical_root/$workforests_dir" "$branch"; then
    resume_reasons+="set directory already exists at $canonical_root/$workforests_dir/$branch"
  fi
  local existing_branch_repos=""
  while IFS=$'\t' read -r name path; do
    [[ -n $name ]] || continue
    if pnwf_branch_exists "$path" "$branch"; then
      existing_branch_repos+="${existing_branch_repos:+, }$name"
    fi
  done <<<"$repo_tsv"
  if [[ -n $existing_branch_repos ]]; then
    resume_reasons+="${resume_reasons:+; }branch '$branch' already exists in: $existing_branch_repos"
  fi

  if [[ -n $resume_reasons ]]; then
    echo "resume"
    echo "reason: $resume_reasons"
    return 0
  fi

  echo "proceed"
  echo "reason: canonical is clean and on primary for all checked repos, and no existing set or branch was found for '$branch'"
}

cmd_land_plan() {
  local branch=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    -h | --help)
      cat <<'HELP'
Usage: pnwf land-plan <branch>

Print the topo-ordered member repos of <branch>'s set that still need
landing, one per line, from the SET's OWN pn-workspace.lock.json (subset
sets only enumerate their own members). A member is included when its
worktree is present AND it is not (yet) an ancestor of its primary branch.
An absent worktree is skipped (FF-4 already removed it — already landed).
Never aborts on an absent member branch (guarded).
HELP
      exit 0
      ;;
    --*)
      die "land-plan: unknown argument: $1"
      ;;
    *)
      [[ -z $branch ]] || die "land-plan: unexpected extra argument: $1"
      branch="$1"
      ;;
    esac
    shift
  done
  [[ -n $branch ]] || die "land-plan: <branch> is required"

  local resolved canonical_root setdir lock_file
  resolved=$(_pnwf_resolve_set_dir "$branch")
  canonical_root="${resolved%%$'\t'*}"
  setdir="${resolved#*$'\t'}"
  lock_file="$setdir/pn-workspace.lock.json"
  [[ -f $lock_file ]] || die "lock file not found: $lock_file"

  local members=()
  mapfile -t members < <(pnwf_topo_order "$lock_file")

  local member member_canonical primary status
  for member in "${members[@]}"; do
    pnwf_worktree_present "$setdir" "$member" || continue

    member_canonical="$canonical_root/$member"
    primary=$(pnwf_resolve_primary_branch "$member_canonical") ||
      die "could not resolve primary branch for member '$member'"
    status=$(pnwf_is_ancestor_of_primary "$member_canonical" "$branch" "$primary") ||
      die "could not resolve ancestor status for member '$member'"
    if [[ $status != "landed" ]]; then
      echo "$member"
    fi
  done
}

cmd_status() {
  local branch=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
    -h | --help)
      cat <<'HELP'
Usage: pnwf status <branch>

Print a per-repo table for <branch>'s set (member, label, reason), one line
per member, tab-separated. label is one of:
  landed        worktree already removed (FF-4 completed), or nothing to do.
  not-started   worktree present, clean, no commits ahead of primary yet.
  blocked       worktree present, but dirty, or its branch ref is missing.
  kept          worktree present, clean, ahead of primary, not yet landed.
Members come from the set's own pn-workspace.lock.json (subset-aware).
Used by the land and cleanup skills for their operator reports.
HELP
      exit 0
      ;;
    --*)
      die "status: unknown argument: $1"
      ;;
    *)
      [[ -z $branch ]] || die "status: unexpected extra argument: $1"
      branch="$1"
      ;;
    esac
    shift
  done
  [[ -n $branch ]] || die "status: <branch> is required"

  local resolved canonical_root setdir lock_file
  resolved=$(_pnwf_resolve_set_dir "$branch")
  canonical_root="${resolved%%$'\t'*}"
  setdir="${resolved#*$'\t'}"
  lock_file="$setdir/pn-workspace.lock.json"
  [[ -f $lock_file ]] || die "lock file not found: $lock_file"

  local members=()
  mapfile -t members < <(pnwf_topo_order "$lock_file")

  local member member_canonical primary classified label reason
  for member in "${members[@]}"; do
    member_canonical="$canonical_root/$member"
    primary=$(pnwf_resolve_primary_branch "$member_canonical") ||
      die "could not resolve primary branch for member '$member'"
    classified=$(pnwf_classify_member "$setdir" "$member" "$member_canonical" "$branch" "$primary") ||
      die "could not classify member '$member'"
    label="${classified%%$'\t'*}"
    reason="${classified#*$'\t'}"
    printf '%s\t%s\t%s\n' "$member" "$label" "$reason"
  done
}

# Removes one member's worktree + branch as part of `cmd_cleanup`. Guarded
# end to end: git failures are reported, never allowed to abort the caller's
# best-effort loop (every path below explicitly `return`s 0). Prints ONE
# line: "<removed 0|1>\t<reason>".
#
# Args: setdir member canonical_dir branch primary force_dirty forced_unlanded
_pnwf_cleanup_remove_member() {
  local setdir="$1" member="$2" canonical_dir="$3" branch="$4" primary="$5" \
    force_dirty="$6" forced_unlanded="$7"
  local member_setpath="$setdir/$member"

  if pnwf_worktree_present "$setdir" "$member"; then
    local dirty_rc=0
    pnwf_working_tree_dirty "$member_setpath" || dirty_rc=$?
    case "$dirty_rc" in
    0)
      if [[ $force_dirty -ne 1 ]]; then
        printf '%s\t%s\n' "0" "worktree has uncommitted changes; rerun with --force-dirty-worktree-removal to remove anyway"
        return 0
      fi
      ;;
    1) : ;;
    *)
      printf '%s\t%s\n' "0" "could not check worktree cleanliness (rc=$dirty_rc)"
      return 0
      ;;
    esac

    local wt_flags=() wt_rc=0 wt_out
    [[ $dirty_rc -eq 0 ]] && wt_flags=(--force)
    wt_out=$(git -C "$canonical_dir" worktree remove "${wt_flags[@]}" "$member_setpath" 2>&1) || wt_rc=$?
    if [[ $wt_rc -ne 0 ]]; then
      printf '%s\t%s\n' "0" "git worktree remove failed (rc=$wt_rc): $wt_out"
      return 0
    fi
  fi

  # Landed members use plain -d (git itself confirms it's merged); a forced
  # not-landed removal uses -D (the whole point of the --force-… flag is to
  # discard commits git would otherwise refuse to drop).
  local branch_flag="-d" br_rc=0 br_out
  [[ $forced_unlanded -eq 1 ]] && branch_flag="-D"
  br_out=$(git -C "$canonical_dir" branch "$branch_flag" "$branch" 2>&1) || br_rc=$?
  if [[ $br_rc -ne 0 ]]; then
    printf '%s\t%s\n' "0" "git branch $branch_flag failed (rc=$br_rc): $br_out"
    return 0
  fi

  if [[ $forced_unlanded -eq 1 ]]; then
    printf '%s\t%s\n' "1" "not landed, but forcibly removed via --force-unlanded-branch-removal"
  else
    printf '%s\t%s\n' "1" "branch was an ancestor of $primary; worktree + branch removed"
  fi
}

cmd_cleanup() {
  local branch="" force_dirty=0 force_unlanded=0
  while [[ $# -gt 0 ]]; do
    case "$1" in
    --force-dirty-worktree-removal)
      force_dirty=1
      ;;
    --force-unlanded-branch-removal)
      force_unlanded=1
      ;;
    -h | --help)
      cat <<'HELP'
Usage: pnwf cleanup <branch> [--force-dirty-worktree-removal] [--force-unlanded-branch-removal]

Best-effort teardown of <branch>'s set, from the canonical clone on each
member's primary branch. Per member (enumerated from the SET's own
pn-workspace.lock.json — subset-aware):
  - branch absent      already landed/removed elsewhere; nothing to do.
  - landed (ancestor)  remove worktree + `git branch -d` (never `git branch
                       -d` AS the landed-test itself — it never runs before
                       `git merge-base --is-ancestor` has confirmed landed).
  - not landed         kept by default (incl. pull-request repos); report
                       names the two force flags.
Processes EVERY member and never aborts on one un-removable repo — the
overall exit code reflects tool success, not how many repos were kept.
The set directory itself is removed (via `pn workspace workforest remove`)
only when nothing was kept; otherwise it is left in place and reported.

--force-dirty-worktree-removal   force `git worktree remove` on a dirty
                                  worktree (landed or forced-unlanded).
--force-unlanded-branch-removal  remove worktree + `git branch -D` for a
                                  member that is NOT landed.
HELP
      exit 0
      ;;
    --*)
      die "cleanup: unknown argument: $1"
      ;;
    *)
      [[ -z $branch ]] || die "cleanup: unexpected extra argument: $1"
      branch="$1"
      ;;
    esac
    shift
  done
  [[ -n $branch ]] || die "cleanup: <branch> is required"

  local resolved canonical_root setdir lock_file
  resolved=$(_pnwf_resolve_set_dir "$branch")
  canonical_root="${resolved%%$'\t'*}"
  setdir="${resolved#*$'\t'}"
  lock_file="$setdir/pn-workspace.lock.json"
  [[ -f $lock_file ]] || die "lock file not found: $lock_file"

  local members=()
  mapfile -t members < <(pnwf_topo_order "$lock_file")
  [[ ${#members[@]} -gt 0 ]] || die "no members found in $lock_file"

  local any_kept=0
  local report_lines=()

  local member member_canonical primary status_rc status result removed reason
  for member in "${members[@]}"; do
    member_canonical="$canonical_root/$member"

    primary=$(pnwf_resolve_primary_branch "$member_canonical") ||
      die "could not resolve primary branch for member '$member'"

    status_rc=0
    status=$(pnwf_is_ancestor_of_primary "$member_canonical" "$branch" "$primary") || status_rc=$?
    if [[ $status_rc -ne 0 ]]; then
      die "could not resolve ancestor status for member '$member' (rc=$status_rc)"
    fi

    case "$status" in
    absent)
      report_lines+=("$member	landed	branch '$branch' not found in $member_canonical (already landed/removed)")
      ;;
    landed)
      result=$(_pnwf_cleanup_remove_member "$setdir" "$member" "$member_canonical" "$branch" "$primary" "$force_dirty" "0")
      removed="${result%%$'\t'*}"
      reason="${result#*$'\t'}"
      if [[ $removed -eq 1 ]]; then
        report_lines+=("$member	removed	$reason")
      else
        any_kept=1
        report_lines+=("$member	kept	$reason")
      fi
      ;;
    not-landed)
      if [[ $force_unlanded -eq 1 ]]; then
        result=$(_pnwf_cleanup_remove_member "$setdir" "$member" "$member_canonical" "$branch" "$primary" "$force_dirty" "1")
        removed="${result%%$'\t'*}"
        reason="${result#*$'\t'}"
        if [[ $removed -eq 1 ]]; then
          report_lines+=("$member	removed	$reason")
        else
          any_kept=1
          report_lines+=("$member	kept	$reason")
        fi
      else
        any_kept=1
        local ahead ahead_rc=0
        ahead=$(pnwf_ahead_of_primary "$member_canonical" "$branch" "$primary") || ahead_rc=$?
        if [[ $ahead_rc -ne 0 ]]; then
          die "could not compute ahead-of-primary for member '$member' (rc=$ahead_rc)"
        fi
        report_lines+=("$member	kept	$ahead commit(s) ahead of $primary, not yet landed; rerun with --force-unlanded-branch-removal to remove anyway (add --force-dirty-worktree-removal too if its worktree is dirty)")
      fi
      ;;
    esac
  done

  if [[ $any_kept -eq 0 ]]; then
    local rm_rc=0 rm_out
    rm_out=$(env -u PN_WORKSPACE_ROOT PN_WORKSPACE_ROOT="$canonical_root" pn workspace workforest remove "$branch" 2>&1) || rm_rc=$?
    if [[ $rm_rc -ne 0 ]]; then
      report_lines+=("(set)	kept	pn workspace workforest remove failed (rc=$rm_rc): $rm_out")
    else
      report_lines+=("(set)	removed	set directory removed: $setdir")
    fi
  else
    report_lines+=("(set)	kept	set directory left in place (at least one member is still kept): $setdir")
  fi

  printf '%s\n' "${report_lines[@]}"
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
# §4.5); resolve/repos/stage/fork-preflight/land-plan/cleanup/status are
# implemented — only sync-fetch (a mutating WORK-recipe helper, not a
# read-only probe — task 5) remains stubbed here.
case "$SUBCOMMAND" in
resolve) cmd_resolve "$@" ;;
repos) cmd_repos "$@" ;;
stage) cmd_stage "$@" ;;
fork-preflight) cmd_fork_preflight "$@" ;;
land-plan) cmd_land_plan "$@" ;;
cleanup) cmd_cleanup "$@" ;;
status) cmd_status "$@" ;;
sync-fetch)
  echo "pnwf: '$SUBCOMMAND' is not yet implemented" >&2
  exit 1
  ;;
*)
  die "unknown subcommand: $SUBCOMMAND"
  ;;
esac
