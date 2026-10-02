# shellcheck shell=bash

# pg-hooks: inspect and run the per-clone hook bundle (design: per-clone hook
# bundle, sections 5.1, 5.3, 5.4, 7.1). The builder prepends pg-hooks-lib.bash,
# which provides pgh_* (pointer, bundle, stamp, messages, exit codes).
#
# Messages start with "pg-hooks:" and go to stderr; stdout carries only the
# requested output of status, list and explain.
#
# BASH 3.2 COMPATIBLE (see pg-hooks-lib.bash): no mapfile, no associative
# arrays, and empty arrays expand as ${a[@]+"${a[@]}"}.

show_help() {
  cat <<'HELP'
pg-hooks: inspect and run the per-clone hook bundle

Usage: pg-hooks [command] [args]

Commands:
  status [--porcelain]   Report the bundle state (exit code carries the state)
  list                   List the stages that have configured hooks
  explain <stage>        Show what a stage runs and which hooks it holds
  run <stage> [files...|--all-files] [-- prek-args]
                         Run a stage's hooks now (staged files by default)
  run pre-land [<ref>]   Run the pre-commit hooks over the branch diff
  fix                    Apply the repo's fixers to the staged files and restage them
                         (also installed as: pre-commit-fix)

Common tasks:
  before committing:  git add <files>; pg-hooks fix; git commit
  before landing:     pg-hooks run pre-land
  diagnose:           pg-hooks status

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Exit codes:
  0   success or nothing to do (a legacy repo counts as success)
  2   usage error, unknown stage, refused operation, or not in a git repo
  10  a hook or fixer failed
  11  file(s) skipped (staged and unstaged changes)
  12  bundle broken (pointer invalid or bin/prek not runnable)
  13  no bundle (run, fix, list, explain)
  14  status only: bundle stale
  15  status only: hooks unreachable (core.hooksPath bypasses <common-dir>/hooks)
  16  status only: clone relocated

Environment:
  PG_HOOKS_PROGRESS_AFTER_S  print one progress line after N seconds (default 3; 0 disables)
  XDG_STATE_HOME             fix appends timings to $XDG_STATE_HOME/pg-hooks/timings.jsonl
                             (default ~/.local/state)
HELP
}

# The stage names prek understands, plus pre-land (a pg-hooks pseudo-stage).
PGH_KNOWN_STAGES="pre-commit pre-merge-commit pre-push pre-rebase commit-msg prepare-commit-msg post-checkout post-commit post-merge post-rewrite manual pre-land"

die() {
  printf 'pg-hooks: %s\n' "$1" >&2
  exit "${2:-$PGH_USAGE}"
}

is_known_stage() {
  local s
  for s in $PGH_KNOWN_STAGES; do
    if [[ $s == "$1" ]]; then
      return 0
    fi
  done
  return 1
}

# need_repo [--work-tree]: load the git context (top level of this shell, never
# in a command substitution) or exit 2.
need_repo() {
  pgh_ctx --toplevel || die "not inside a git repository"
  if [[ ${1:-} == --work-tree && -z $PGH_TOP ]]; then
    die "this command needs a work tree (this is a bare repository)"
  fi
  return 0
}

# physical_dir <path>: the physical path when the directory exists, else the
# path unchanged.
physical_dir() {
  if [[ -d $1 ]]; then
    (cd -P "$1" 2>/dev/null && pwd -P) || printf '%s\n' "$1"
  else
    printf '%s\n' "$1"
  fi
}

# bundle_repo <bundle>: the repo name recorded in the bundle, else the
# canonical clone's directory name.
bundle_repo() {
  local repo=""
  if [[ -n $1 && -f $1/meta.json ]]; then
    repo=$(jq -r '.repo // ""' "$1/meta.json" 2>/dev/null) || repo=""
  fi
  if [[ -z $repo ]]; then
    repo=$(pgh_repo_name) || repo="unknown"
  fi
  printf '%s\n' "$repo"
}

# --- bundle resolution -------------------------------------------------------
#
# resolve_bundle sets, at the caller's level:
#   RB_DIR     the selected pg-hooks dir (private if it has `current`, else shared)
#   RB_GEN     gen-N, or empty
#   RB_BUNDLE  <dir>/<gen>/bundle, or empty
#   RB_STATUS  0 usable, 12 broken, 13 none
#   RB_REASON  why it is broken (RB_STATUS 12)
resolve_bundle() {
  local rc=0
  RB_DIR=$(pgh_select_dir) || die "not inside a git repository"
  RB_GEN=""
  RB_BUNDLE=""
  RB_REASON=""
  RB_GEN=$(pgh_read_pointer "$RB_DIR") || rc=$?
  if ((rc != 0)); then
    RB_GEN=""
    RB_STATUS=$rc
    if ((rc == 12)); then
      RB_REASON="pointer $RB_DIR/current is invalid"
    fi
    return 0
  fi
  rc=0
  RB_BUNDLE=$(pgh_bundle "$RB_DIR" "$RB_GEN") || rc=$?
  RB_STATUS=$rc
  if ((rc == 12)); then
    RB_REASON="bin/prek or bin/pg-hooks-run is not executable"
  elif ((rc != 0)); then
    RB_BUNDLE=""
  fi
  return 0
}

# legacy_config: print the usable legacy .pre-commit-config.yaml (the work
# tree's, else the canonical clone's), read in place and never linked. Returns
# 1 when there is none.
legacy_config() {
  local c canonical
  if [[ -n $PGH_TOP && -f $PGH_TOP/.pre-commit-config.yaml && -r $PGH_TOP/.pre-commit-config.yaml ]]; then
    printf '%s\n' "$PGH_TOP/.pre-commit-config.yaml"
    return 0
  fi
  canonical=$(pgh_canonical) || return 1
  c=$canonical/.pre-commit-config.yaml
  if [[ -f $c && -r $c ]]; then
    printf '%s\n' "$c"
    return 0
  fi
  return 1
}

# hooks_unreachable: status 0 (and UR_VALUE/UR_ORIGIN set) when git does NOT run
# hooks from <common-dir>/hooks. A core.hooksPath that resolves there is fine; a
# global/system one is fine only when it names the dispatcher (a directory
# holding an executable pre-commit, which chains to <common-dir>/hooks).
hooks_unreachable() {
  local line scope origin value resolved want
  line=$(git config --show-scope --show-origin --get core.hooksPath 2>/dev/null) || return 1
  [[ -n $line ]] || return 1
  scope=${line%%$'\t'*}
  line=${line#*$'\t'}
  origin=${line%%$'\t'*}
  value=${line#*$'\t'}
  [[ -n $value ]] || return 1
  UR_VALUE=$value
  UR_ORIGIN=${origin#file:}
  # git reports a repository config file relative (".git/config"); name it by
  # its absolute path so the operator-facing message is unambiguous.
  case $UR_ORIGIN in
  /*) ;;
  *)
    case $scope in
    local) UR_ORIGIN=$PGH_COMMON/config ;;
    worktree) UR_ORIGIN=$PGH_GITDIR/config.worktree ;;
    esac
    ;;
  esac
  case $value in
  /*) resolved=$value ;;
  \~/*) resolved=${HOME:-}/${value#\~/} ;;
  *) resolved=${PGH_TOP:-$PWD}/$value ;;
  esac
  want=$(physical_dir "$PGH_COMMON/hooks")
  if [[ $(physical_dir "$resolved") == "$want" ]]; then
    return 1
  fi
  case $scope in
  global | system)
    if [[ -x $resolved/pre-commit ]]; then
      return 1
    fi
    ;;
  esac
  return 0
}

# compute_stale <dir> <gen>: status 0 when the bundle is stale. Sets
# ST_DIFFERS (1 when the stamp differs) and ST_NOTES (override notes). Pure: no
# warning marker and no output (pgh_stale_check is the hook-time variant).
compute_stale() {
  local gdir=$1/$2 src meta recorded current p
  src=$gdir/source.json
  meta=$gdir/bundle/meta.json
  ST_DIFFERS=0
  ST_NOTES=""
  [[ -f $src && -n $PGH_TOP ]] || return 1
  recorded=$(jq -r '.stamp // ""' "$src" 2>/dev/null) || recorded=""
  local inputs=(flake.lock flake.nix)
  if [[ -f $meta ]]; then
    while IFS= read -r p; do
      if [[ -n $p ]]; then
        inputs+=("$p")
      fi
    done < <(jq -r '.stampPaths[]?' "$meta" 2>/dev/null)
  fi
  current=$(
    cd "$PGH_TOP" 2>/dev/null || exit 0
    pgh_stamp "${inputs[@]}"
  ) || current=""
  if [[ -n $current && $current != unknown && $current != "$recorded" ]]; then
    ST_DIFFERS=1
  fi
  ST_NOTES=$(_pgh_overrides_changed "$src")
  if [[ $ST_DIFFERS == 1 || -n $ST_NOTES ]]; then
    return 0
  fi
  return 1
}

# bundle_stages <bundle>: comma list of the bundle's stages.
bundle_stages() {
  local b=$1 out=""
  if [[ -f $b/stages.json ]]; then
    out=$(jq -r 'keys_unsorted | join(",")' "$b/stages.json" 2>/dev/null) || out=""
  elif [[ -f $b/meta.json ]]; then
    out=$(jq -r '(.stages // []) | join(",")' "$b/meta.json" 2>/dev/null) || out=""
  fi
  printf '%s\n' "$out"
}

# --- status ------------------------------------------------------------------

cmd_status() {
  local porcelain=0
  while [[ $# -gt 0 ]]; do
    case $1 in
    --porcelain) porcelain=1 ;;
    -h | --help)
      show_help
      return 0
      ;;
    *) die "status: unknown argument: $1" ;;
    esac
    shift
  done
  need_repo
  resolve_bundle

  local state="" code=0 clone_path="" reinstall repo stages=""
  local relocated=0 unreachable=0
  UR_VALUE=""
  UR_ORIGIN=""
  if hooks_unreachable; then
    unreachable=1
  fi
  if [[ -n $RB_GEN && -f $RB_DIR/$RB_GEN/source.json ]] && ((RB_STATUS == 0 || RB_STATUS == 12)); then
    clone_path=$(jq -r '.clone_path // ""' "$RB_DIR/$RB_GEN/source.json" 2>/dev/null) || clone_path=""
    if [[ -n $clone_path && $(physical_dir "$clone_path") != "$(physical_dir "$PGH_COMMON")" ]]; then
      relocated=1
    fi
  fi

  # Precedence: relocated > unreachable > broken > stale > present; with no
  # bundle: unreachable, else legacy (usable legacy config), else missing.
  if ((relocated)); then
    state=relocated
    code=$PGH_RELOCATED
  elif ((unreachable)); then
    state=unreachable
    code=$PGH_UNREACHABLE
  elif ((RB_STATUS == 12)); then
    state=broken
    code=$PGH_BROKEN
  elif ((RB_STATUS == 0)); then
    if compute_stale "$RB_DIR" "$RB_GEN"; then
      state=stale
      code=$PGH_STALE
    else
      state=present
      code=$PGH_OK
    fi
  elif legacy_config >/dev/null; then
    state=legacy
    code=$PGH_OK
  else
    state=missing
    code=$PGH_NO_BUNDLE
  fi

  local bundle_path="" generation=""
  if [[ -n $RB_BUNDLE ]]; then
    bundle_path=$RB_BUNDLE
    generation=$RB_GEN
    stages=$(bundle_stages "$RB_BUNDLE")
  fi
  # A linked worktree without a private bundle reinstalls through its private
  # command; everything else through the selected dir's `reinstall` file.
  if [[ $state == missing || $state == legacy ]] && pgh_is_linked; then
    reinstall=$(pgh_reinstall "$(pgh_private_dir)")
  else
    reinstall=$(pgh_reinstall "$RB_DIR")
  fi

  if ((porcelain)); then
    printf 'state=%s\nbundle=%s\ngeneration=%s\nstages=%s\nreinstall=%s\n' \
      "$state" "$bundle_path" "$generation" "$stages" "$reinstall"
  else
    printf 'state:      %s\nbundle:     %s\ngeneration: %s\nstages:     %s\nreinstall:  %s\n' \
      "$state" "$bundle_path" "$generation" "$stages" "$reinstall"
    repo=$(bundle_repo "$bundle_path")
    case $state in
    relocated) pgh_msg_relocated "$clone_path" "$reinstall" ;;
    unreachable) pgh_msg_unreachable "$PGH_COMMON" "$UR_VALUE" "$UR_ORIGIN" "$(pgh_canonical)" ;;
    broken) pgh_msg_broken "$repo" "$RB_REASON" "$RB_DIR" ;;
    stale)
      local built_at
      built_at=$(jq -r '.built_at // "unknown"' "$RB_DIR/$RB_GEN/source.json" 2>/dev/null) || built_at=unknown
      if [[ $ST_DIFFERS == 1 && $RB_DIR == "$(pgh_shared_dir)" ]] && pgh_is_linked; then
        pgh_msg_worktree_differs "$(pgh_private_reinstall)"
      else
        pgh_msg_stale "$repo" "$built_at" "$reinstall" "$ST_NOTES"
      fi
      ;;
    missing) printf 'pg-hooks: no hook bundle for %s. Fix: %s\n' "$repo" "$reinstall" >&2 ;;
    esac
  fi
  return "$code"
}

# --- shared by list, explain and run ----------------------------------------

# require_bundle <what>: resolve the bundle; return 0 when usable. A broken
# bundle exits 12; no bundle returns 13 (the caller decides on the legacy
# fallback).
require_bundle() {
  local repo
  resolve_bundle
  if ((RB_STATUS == 12)); then
    repo=$(bundle_repo "$RB_BUNDLE")
    pgh_msg_broken "$repo" "$RB_REASON" "$RB_DIR"
    exit "$PGH_BROKEN"
  fi
  return "$RB_STATUS"
}

# no_bundle_exit <stage-or-command>: the explicit-invocation exit for no bundle
# (D3): the notice, exit 13.
no_bundle_exit() {
  pgh_msg_no_bundle "$(pgh_repo_name)" "$1"
  exit "$PGH_NO_BUNDLE"
}

# --- list and explain --------------------------------------------------------

cmd_list() {
  if [[ $# -gt 0 ]]; then
    case $1 in
    -h | --help)
      show_help
      return 0
      ;;
    *) die "list: unexpected argument: $1" ;;
    esac
  fi
  need_repo
  if ! require_bundle; then
    if legacy_config >/dev/null; then
      die "list: this repository uses a legacy .pre-commit-config.yaml; stage metadata exists only in a hook bundle" "$PGH_NO_BUNDLE"
    fi
    no_bundle_exit list
  fi
  if [[ -f $RB_BUNDLE/stages.json ]]; then
    jq -r 'keys_unsorted[]' "$RB_BUNDLE/stages.json"
    if jq -e 'has("pre-commit")' "$RB_BUNDLE/stages.json" >/dev/null 2>&1; then
      printf 'pre-land\n'
    fi
  fi
  return 0
}

# stage_trigger <stage>: what fires it, for explain.
stage_trigger() {
  case $1 in
  pre-commit) printf 'git commit\n' ;;
  pre-merge-commit) printf 'git merge (before the merge commit)\n' ;;
  pre-push) printf 'git push\n' ;;
  pre-rebase) printf 'git rebase\n' ;;
  commit-msg) printf 'git commit (message check)\n' ;;
  prepare-commit-msg) printf 'git commit (message preparation)\n' ;;
  post-checkout) printf 'git checkout / switch / worktree add\n' ;;
  post-commit) printf 'git commit (after it is made)\n' ;;
  post-merge) printf 'git merge / pull\n' ;;
  post-rewrite) printf 'git commit --amend / rebase (after rewriting)\n' ;;
  manual) printf 'nothing automatic; only pg-hooks run manual\n' ;;
  pre-land) printf 'ff-merge-to-main FF-1b, before a land\n' ;;
  esac
}

cmd_explain() {
  local stage=${1:-}
  case $stage in
  -h | --help)
    show_help
    return 0
    ;;
  "") die "explain: a stage is required (try: pg-hooks list)" ;;
  esac
  if [[ $# -gt 1 ]]; then
    die "explain: unexpected argument: $2"
  fi
  is_known_stage "$stage" || die "explain: unknown stage: $stage"
  need_repo
  if ! require_bundle; then
    if legacy_config >/dev/null; then
      die "explain: this repository uses a legacy .pre-commit-config.yaml; stage metadata exists only in a hook bundle" "$PGH_NO_BUNDLE"
    fi
    no_bundle_exit explain
  fi
  local source_stage=$stage n
  if [[ $stage == pre-land ]]; then
    source_stage=pre-commit
  fi
  n=0
  if [[ -f $RB_BUNDLE/stages.json ]]; then
    n=$(jq -r --arg s "$source_stage" '(.[$s] // []) | length' "$RB_BUNDLE/stages.json" 2>/dev/null) || n=0
  fi
  printf 'stage: %s\n' "$stage"
  printf 'triggered by: %s\n' "$(stage_trigger "$stage")"
  if [[ $stage == pre-land ]]; then
    printf 'runs: the pre-commit hooks over the branch diff (pg-hooks run pre-land [<ref>])\n'
  else
    printf 'runs: pg-hooks run %s [files...|--all-files] [-- prek-args]\n' "$stage"
  fi
  if ((n == 0)); then
    printf 'hooks: none configured\n'
    return 0
  fi
  printf 'hooks (%s):\n' "$n"
  jq -r --arg s "$source_stage" '
    (.[$s] // [])[]
    | if type == "string" then "  " + .
      else "  " + .id + ": " + (.reason // .description // .id) end
  ' "$RB_BUNDLE/stages.json"
  return 0
}

# --- run ---------------------------------------------------------------------

# primary_branch: pgii-integrate-branch.primaryBranch, else origin/HEAD's branch
# name, else main.
primary_branch() {
  local p=""
  p=$(git config --get pgii-integrate-branch.primaryBranch 2>/dev/null) || p=""
  if [[ -z $p ]]; then
    p=$(git symbolic-ref -q refs/remotes/origin/HEAD 2>/dev/null) || p=""
    p=${p#refs/remotes/origin/}
  fi
  if [[ -z $p ]]; then
    p=main
  fi
  printf '%s\n' "$p"
}

cmd_run() {
  local stage=${1:-}
  case $stage in
  -h | --help)
    show_help
    return 0
    ;;
  "") die "run: a stage is required (try: pg-hooks list)" ;;
  esac
  shift
  is_known_stage "$stage" || die "run: unknown stage: $stage"

  local all_files=0 ref="" arg
  local -a files=()
  local -a raw=()
  while [[ $# -gt 0 ]]; do
    arg=$1
    case $arg in
    --)
      shift
      if [[ $# -gt 0 ]]; then
        raw=("$@")
      fi
      break
      ;;
    --all-files) all_files=1 ;;
    -*) die "run: unknown option: $arg (put raw prek flags after --)" ;;
    *)
      if [[ $stage == pre-land ]]; then
        if [[ -n $ref ]]; then
          die "run pre-land takes at most one <ref>"
        fi
        ref=$arg
      else
        files+=("$arg")
      fi
      ;;
    esac
    shift
  done
  if [[ $stage == pre-land && $all_files == 1 ]]; then
    die "run pre-land does not take --all-files"
  fi
  if ((all_files)) && ((${#files[@]} > 0)); then
    die "run: give file paths or --all-files, not both"
  fi

  need_repo --work-tree

  local cfg prek repo
  if require_bundle; then
    cfg=$RB_BUNDLE/prek-config.json
    prek=$RB_BUNDLE/bin/prek
    repo=$(bundle_repo "$RB_BUNDLE")
  elif cfg=$(legacy_config); then
    # Legacy repo (dual mode, D1): the usable legacy config is read in place and
    # never linked; prek comes from PATH.
    prek=$(command -v prek 2>/dev/null) || prek=""
    if [[ -z $prek ]]; then
      die "legacy hook config $cfg found but prek is not on PATH" "$PGH_NO_BUNDLE"
    fi
    repo=$(pgh_repo_name) || repo=unknown
  else
    no_bundle_exit "$stage"
  fi

  local orig
  orig=$(pwd -P)

  local prek_stage=$stage
  local -a args
  args=(-q run -c "$cfg")
  if [[ $stage == pre-land ]]; then
    prek_stage=pre-commit
    ref=${ref:-HEAD}
    local head want primary
    head=$(git rev-parse --verify -q 'HEAD^{commit}' 2>/dev/null) || die "run pre-land: this branch has no commits"
    want=$(git rev-parse --verify -q "$ref^{commit}" 2>/dev/null) || die "run pre-land: cannot resolve ref: $ref"
    if [[ $head != "$want" ]]; then
      die "run pre-land refuses $ref: it is not the commit checked out in this worktree (HEAD is $head); prek reads working-tree files, so check $ref out first"
    fi
    primary=$(primary_branch)
    git rev-parse --verify -q "$primary^{commit}" >/dev/null 2>&1 || die "run pre-land: cannot resolve the primary branch: $primary"
    args+=(--hook-stage "$prek_stage" --from-ref "$primary" --to-ref "$ref")
  else
    args+=(--hook-stage "$prek_stage")
  fi
  if ((all_files)); then
    args+=(--all-files)
  fi
  if ((${#raw[@]} > 0)); then
    args+=("${raw[@]}")
  fi
  if ((${#files[@]} > 0)); then
    local f abs
    args+=(--files)
    for f in "${files[@]}"; do
      case $f in
      /*) abs=$f ;;
      *) abs=$orig/$f ;;
      esac
      case $abs in
      "$PGH_TOP"/*) abs=${abs#"$PGH_TOP"/} ;;
      esac
      args+=("$abs")
    done
  fi

  cd "$PGH_TOP" || die "cannot enter $PGH_TOP"

  local progress_after=${PG_HOOKS_PROGRESS_AFTER_S:-3}
  if [[ $progress_after =~ ^[0-9]+$ ]] && ((progress_after > 0)); then
    pgh_progress_start "$progress_after" "pg-hooks: running $stage hooks in $repo..."
    trap pgh_progress_stop EXIT
  fi
  local rc=0
  "$prek" "${args[@]}" || rc=$?
  pgh_progress_stop
  if ((rc != 0)); then
    pgh_msg_hooks_failed "$stage" "$repo"
    return "$PGH_FAILED"
  fi
  return 0
}

# --- fix ---------------------------------------------------------------------
#
# pg-hooks fix (spec 5.2): run the bundle's fixers over the STAGED files of this
# checkout, in the order of the bundle's fixers.json, and restage only the files
# a fixer changed. Never `prek run`: prek's hooks are check-mode.

FIX_BATCH=200
FIX_CONVERGE_MAX=5

# fix_refuse_in_progress: exit 2 while a merge, rebase, cherry-pick, revert or
# `git am` is in progress in this checkout (its git dir is per worktree).
fix_refuse_in_progress() {
  local g=$PGH_GITDIR what=""
  if [[ -e $g/MERGE_HEAD ]]; then
    what=merge
  elif [[ -d $g/rebase-merge || -d $g/rebase-apply ]]; then
    what=rebase
  elif [[ -e $g/CHERRY_PICK_HEAD ]]; then
    what=cherry-pick
  elif [[ -e $g/REVERT_HEAD ]]; then
    what=revert
  fi
  if [[ -n $what ]]; then
    die "fix refused: a $what is in progress in this checkout; finish or abort it first (fixers restage files, which would corrupt it)"
  fi
  return 0
}

# fix_glob_match <path> <glob>...: status 0 when <path> matches any glob. A glob
# is a shell pattern, so `*` also crosses `/` (`*.nix` matches a/b/c.nix).
fix_glob_match() {
  local f=$1 g
  shift
  for g in "$@"; do
    # shellcheck disable=SC2254  # the glob is meant to be a pattern, not a literal
    case $f in $g) return 0 ;; esac
  done
  return 1
}

# fix_hash_files <file>...: fill FIX_HASHES (parallel to the arguments) with the
# content hash of each file; "-" for a file that cannot be read.
fix_hash_files() {
  local -a all=("$@") batch
  local i=0 n=$# out h f
  FIX_HASHES=()
  while ((i < n)); do
    batch=("${all[@]:i:FIX_BATCH}")
    if out=$(git hash-object -- "${batch[@]}" 2>/dev/null); then
      while IFS= read -r h; do
        FIX_HASHES+=("$h")
      done <<<"$out"
    else
      for f in "${batch[@]}"; do
        if h=$(git hash-object -- "$f" 2>/dev/null); then
          FIX_HASHES+=("$h")
        else
          FIX_HASHES+=("-")
        fi
      done
    fi
    i=$((i + FIX_BATCH))
  done
  return 0
}

# fix_record_timing <tool> <files> <seconds> <exit>: append one JSON line to
# ${XDG_STATE_HOME:-$HOME/.local/state}/pg-hooks/timings.jsonl. An unwritable
# state dir MUST NOT fail the run.
fix_record_timing() {
  local dir=${XDG_STATE_HOME:-${HOME:+$HOME/.local/state}}
  [[ -n $dir ]] || return 0
  dir=$dir/pg-hooks
  {
    mkdir -p "$dir" &&
      jq -nc --arg tool "$1" --argjson files "$2" --argjson seconds "$3" --argjson exit "$4" \
        '{tool: $tool, files: $files, seconds: $seconds, exit: $exit}' >>"$dir/timings.jsonl"
  } 2>/dev/null || true
  return 0
}

# fix_invoke: run FX_ARGV over the batch FX_BATCH once; sets FIX_RC and leaves
# the tool's combined output in $FIX_TMP/out. Two behaviors beyond a plain call:
#  - a nonzero exit is retried once on the same files. Several fixers (the
#    pre-commit-hooks trailing-whitespace and end-of-file fixers) exit 1 AFTER
#    fixing a file; the retry sees the fixed file and exits 0, while a real
#    failure fails again.
#  - treefmt runs to convergence: repeated until the files stop changing, at
#    most FIX_CONVERGE_MAX passes (prettier markdown can need 2+).
fix_invoke() {
  local out=$FIX_TMP/out pass=0 converge=0 before="" after=""
  FIX_RC=0
  if [[ $FX_MODE == files && ${FX_ARGV[0]##*/} == treefmt ]]; then
    converge=1
  fi
  while :; do
    pass=$((pass + 1))
    if ((converge)); then
      fix_hash_files "${FX_BATCH[@]}"
      before="${FIX_HASHES[*]}"
    fi
    FIX_RC=0
    "${FX_ARGV[@]}" "${FX_BATCH[@]}" >"$out" 2>&1 </dev/null || FIX_RC=$?
    if ((FIX_RC != 0)); then
      FIX_RC=0
      "${FX_ARGV[@]}" "${FX_BATCH[@]}" >"$out" 2>&1 </dev/null || FIX_RC=$?
      if ((FIX_RC != 0)); then
        return 0
      fi
    fi
    if ((!converge)); then
      return 0
    fi
    fix_hash_files "${FX_BATCH[@]}"
    after="${FIX_HASHES[*]}"
    if [[ $before == "$after" ]]; then
      return 0
    fi
    if ((pass >= FIX_CONVERGE_MAX)); then
      printf 'pg-hooks: fixer %s did not converge in %s passes; the files may change again on the next run.\n' \
        "$FX_NAME" "$FIX_CONVERGE_MAX" >&2
      return 0
    fi
  done
}

# fix_run_fixer: run the fixer described by FX_NAME / FX_MODE / FX_ARGV over
# FX_FILES, in batches of at most FIX_BATCH paths (one path per call in
# per-file mode). Exits 10 on a failure.
fix_run_fixer() {
  local started=$SECONDS total=${#FX_FILES[@]} i=0 step=$FIX_BATCH elapsed
  if [[ $FX_MODE == per-file ]]; then
    step=1
  fi
  while ((i < total)); do
    FX_BATCH=("${FX_FILES[@]:i:step}")
    fix_invoke
    if ((FIX_RC != 0)); then
      elapsed=$((SECONDS - started))
      fix_record_timing "$FX_NAME" "$total" "$elapsed" "$FIX_RC"
      pgh_progress_stop
      printf 'pg-hooks: fixer %s failed on %s (exit %s); its output follows:\n' \
        "$FX_NAME" "${FX_BATCH[*]}" "$FIX_RC" >&2
      cat "$FIX_TMP/out" >&2
      printf 'Fix by hand, git add the file(s), then rerun pg-hooks fix.\n' >&2
      exit "$PGH_FAILED"
    fi
    i=$((i + step))
  done
  elapsed=$((SECONDS - started))
  fix_record_timing "$FX_NAME" "$total" "$elapsed" 0
  return 0
}

cmd_fix() {
  case ${1:-} in
  "") ;;
  -h | --help)
    show_help
    return 0
    ;;
  *) die "fix: unexpected argument: $1 (fix takes no arguments; it works on the staged files)" ;;
  esac

  need_repo --work-tree
  fix_refuse_in_progress
  if ! require_bundle; then
    no_bundle_exit fix
  fi
  local repo fixers_file=$RB_BUNDLE/fixers.json
  repo=$(bundle_repo "$RB_BUNDLE")
  if [[ ! -f $fixers_file ]] || ! jq -e 'type == "array"' "$fixers_file" >/dev/null 2>&1; then
    pgh_msg_broken "$repo" "fixers.json is missing or invalid" "$RB_DIR"
    return "$PGH_BROKEN"
  fi

  cd "$PGH_TOP" || die "cannot enter $PGH_TOP"

  # The staged files that still exist (added, copied, modified, renamed).
  local f
  local -a staged=() unstaged=() cand=() skipped=()
  while IFS= read -r -d '' f; do
    staged+=("$f")
  done < <(git diff --cached --name-only -z --no-ext-diff --diff-filter=ACMR)
  if ((${#staged[@]} == 0)); then
    return 0
  fi

  # Repo excludes are prek regexes (the top-level `exclude` of the prek config),
  # applied with grep -E.
  local exclude="" rc=0
  FIX_TMP=$(mktemp -d)
  trap 'pgh_progress_stop; rm -rf "$FIX_TMP"' EXIT
  if [[ -f $RB_BUNDLE/prek-config.json ]]; then
    exclude=$(jq -r '.exclude // ""' "$RB_BUNDLE/prek-config.json" 2>/dev/null) || exclude=""
  fi
  local -a kept=()
  if [[ -n $exclude ]]; then
    printf '%s\0' "${staged[@]}" | grep -zEv -- "$exclude" >"$FIX_TMP/kept" || rc=$?
    if ((rc > 1)); then
      die "cannot apply the repo excludes: $exclude is not a valid extended regex" 1
    fi
    while IFS= read -r -d '' f; do
      kept+=("$f")
    done <"$FIX_TMP/kept"
  else
    kept=("${staged[@]}")
  fi

  # A kept file with both staged and unstaged changes is skipped untouched.
  while IFS= read -r -d '' f; do
    unstaged+=("$f")
  done < <(git diff --name-only -z --no-ext-diff)
  local k u hit
  for k in ${kept[@]+"${kept[@]}"}; do
    hit=0
    for u in ${unstaged[@]+"${unstaged[@]}"}; do
      if [[ $u == "$k" ]]; then
        hit=1
        break
      fi
    done
    if ((hit)); then
      skipped+=("$k")
    else
      cand+=("$k")
    fi
  done

  local -a before_hashes=() after_hashes=() changed=()
  if ((${#cand[@]} > 0)); then
    fix_hash_files "${cand[@]}"
    before_hashes=("${FIX_HASHES[@]}")

    local progress_after=${PG_HOOKS_PROGRESS_AFTER_S:-3}
    if [[ $progress_after =~ ^[0-9]+$ ]] && ((progress_after > 0)); then
      pgh_progress_start "$progress_after" "pg-hooks: running fixers in $repo..."
    fi

    local n i j count line
    local -a inc exc
    n=$(jq 'length' "$fixers_file")
    for ((i = 0; i < n; i++)); do
      FX_NAME=$(jq -r --argjson i "$i" '.[$i].name' "$fixers_file")
      FX_MODE=$(jq -r --argjson i "$i" '.[$i].mode // "files"' "$fixers_file")
      FX_ARGV=()
      # `command` is an argv array, or a string split on whitespace (store paths
      # contain no spaces).
      while IFS= read -r line; do
        FX_ARGV+=("$line")
      done < <(jq -r --argjson i "$i" \
        '.[$i].command | if type == "array" then . else (split(" ") | map(select(length > 0))) end | .[]' \
        "$fixers_file")
      if ((${#FX_ARGV[@]} == 0)); then
        pgh_progress_stop
        pgh_msg_broken "$repo" "fixer $FX_NAME has no command" "$RB_DIR"
        return "$PGH_BROKEN"
      fi
      inc=()
      while IFS= read -r line; do
        inc+=("$line")
      done < <(jq -r --argjson i "$i" '.[$i].includes // [] | .[]' "$fixers_file")
      exc=()
      while IFS= read -r line; do
        exc+=("$line")
      done < <(jq -r --argjson i "$i" '.[$i].excludes // [] | .[]' "$fixers_file")

      FX_FILES=()
      for f in "${cand[@]}"; do
        if ((${#inc[@]} > 0)) && ! fix_glob_match "$f" "${inc[@]}"; then
          continue
        fi
        if ((${#exc[@]} > 0)) && fix_glob_match "$f" "${exc[@]}"; then
          continue
        fi
        FX_FILES+=("$f")
      done
      if ((${#FX_FILES[@]} > 0)); then
        fix_run_fixer
      fi
    done
    pgh_progress_stop

    # Restage only the files a fixer changed.
    fix_hash_files "${cand[@]}"
    after_hashes=("${FIX_HASHES[@]}")
    count=${#cand[@]}
    for ((j = 0; j < count; j++)); do
      if [[ ${before_hashes[j]} != "${after_hashes[j]}" && -e ${cand[j]} ]]; then
        changed+=("${cand[j]}")
      fi
    done
    i=0
    while ((i < ${#changed[@]})); do
      git add -- "${changed[@]:i:FIX_BATCH}"
      i=$((i + FIX_BATCH))
    done
  fi

  if ((${#skipped[@]} > 0)); then
    for f in "${skipped[@]}"; do
      printf 'pg-hooks: fix skipped %s: it has staged and unstaged changes. Run: git add %q && pg-hooks fix (or git restore --staged %q)\n' \
        "$f" "$f" "$f" >&2
    done
    return "$PGH_SKIPPED"
  fi
  return 0
}

# --- dispatch ----------------------------------------------------------------

if [[ $# -eq 0 ]]; then
  show_help
  exit 0
fi

cmd=$1
shift
case $cmd in
-h | --help | help)
  show_help
  exit 0
  ;;
status) cmd_status "$@" ;;
list) cmd_list "$@" ;;
explain) cmd_explain "$@" ;;
run) cmd_run "$@" ;;
fix) cmd_fix "$@" ;;
*)
  printf 'pg-hooks: unknown command: %s\n\n' "$cmd" >&2
  show_help >&2
  exit "$PGH_USAGE"
  ;;
esac
