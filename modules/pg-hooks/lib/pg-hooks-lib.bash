# shellcheck shell=bash
# shellcheck disable=SC2034  # the PGH_* exit-code constants are read by sourcing scripts and tests

# pg-hooks shared library: bundle resolution, pointer validation, staleness
# stamp and the fixed message texts. Sourced by the bundle runner
# (pg-hooks-run), by the pg-hooks command and directly by bats.
#
# Design: docs/superpowers/specs/2026-10-01-per-clone-hook-bundle-design.md
# (sections 4.2 pointer rules, 4.3 stubs and runner, 4.5 staleness, 5.3 exit
# codes, 5.4 messages).
#
# BASH 3.2 COMPATIBILITY IS REQUIRED. The runner is launched by git hooks that
# may carry a minimal environment (a launchd job with PATH=/usr/bin:/bin), where
# `env bash` can resolve to macOS /bin/bash 3.2. So: no mapfile, no associative
# arrays, no ${var,,}, and empty arrays are expanded with ${a[@]+"${a[@]}"}.
#
# Every message starts with "pg-hooks:" and goes to stderr; stdout carries only
# documented output. Functions here never `exit`; they return a status.

# Exit codes (spec 5.3). Plain assignments, not `readonly`, so sourcing the
# library twice (a wrapper plus a test) is harmless.
PGH_OK=0
PGH_USAGE=2
PGH_FAILED=10
PGH_SKIPPED=11
PGH_BROKEN=12
PGH_NO_BUNDLE=13
PGH_STALE=14
PGH_UNREACHABLE=15
PGH_RELOCATED=16

# --- git context -----------------------------------------------------------
#
# Every git path is requested with --path-format=absolute: the canonical clone
# otherwise yields a relative ".git". The getters below call git on every use
# UNLESS a caller has run pgh_ctx at the top level of its own shell (never in a
# command substitution, which would lose the variables): then the cached values
# are used and no further git process is spawned. The runner relies on this to
# stay within its git process budget.

# pgh_ctx_reset: forget the cached context.
pgh_ctx_reset() {
  PGH_CTX_SET=0
  PGH_COMMON=""
  PGH_GITDIR=""
  PGH_TOP=""
}

# pgh_ctx [--toplevel]: one git process; caches the absolute common dir, the
# absolute git dir and (with --toplevel) the work tree root. Returns 2 outside
# a repository. A missing work tree (bare repo) leaves PGH_TOP empty and is not
# an error.
pgh_ctx() {
  local out="" l1="" l2="" l3=""
  if [[ ${1:-} == --toplevel ]]; then
    out=$(git rev-parse --path-format=absolute --git-common-dir --absolute-git-dir --show-toplevel 2>/dev/null) || true
  else
    out=$(git rev-parse --path-format=absolute --git-common-dir --absolute-git-dir 2>/dev/null) || true
  fi
  {
    IFS= read -r l1
    IFS= read -r l2
    IFS= read -r l3
  } <<<"$out" || true
  if [[ -z $l1 || -z $l2 ]]; then
    pgh_ctx_reset
    return 2
  fi
  PGH_COMMON=$l1
  PGH_GITDIR=$l2
  PGH_TOP=$l3
  PGH_CTX_SET=1
  return 0
}

# _pgh_common / _pgh_gitdir: print the cached value, else ask git.
_pgh_common() {
  if [[ ${PGH_CTX_SET:-0} == 1 ]]; then
    printf '%s\n' "$PGH_COMMON"
    return 0
  fi
  git rev-parse --path-format=absolute --git-common-dir 2>/dev/null
}

_pgh_gitdir() {
  if [[ ${PGH_CTX_SET:-0} == 1 ]]; then
    printf '%s\n' "$PGH_GITDIR"
    return 0
  fi
  git rev-parse --path-format=absolute --absolute-git-dir 2>/dev/null
}

# pgh_shared_dir: <abs common-dir>/pg-hooks
pgh_shared_dir() {
  local c
  c=$(_pgh_common) || return 2
  [[ -n $c ]] || return 2
  printf '%s/pg-hooks\n' "$c"
}

# pgh_private_dir: <abs git-dir>/pg-hooks (equals the shared dir in the
# canonical clone)
pgh_private_dir() {
  local g
  g=$(_pgh_gitdir) || return 2
  [[ -n $g ]] || return 2
  printf '%s/pg-hooks\n' "$g"
}

# pgh_canonical: the canonical clone's path (the common dir's parent).
pgh_canonical() {
  local c
  c=$(_pgh_common) || return 2
  [[ -n $c ]] || return 2
  printf '%s\n' "${c%/*}"
}

# pgh_repo_name: basename of the canonical clone.
pgh_repo_name() {
  local k
  k=$(pgh_canonical) || return 2
  printf '%s\n' "${k##*/}"
}

# pgh_is_linked: status 0 when this checkout is a linked worktree.
pgh_is_linked() {
  local c g
  c=$(_pgh_common) || return 2
  g=$(_pgh_gitdir) || return 2
  [[ -n $c && -n $g && $c != "$g" ]]
}

# pgh_select_dir: the private dir if it holds `current`, else the shared dir.
pgh_select_dir() {
  local priv shared
  priv=$(pgh_private_dir) || return 2
  shared=$(pgh_shared_dir) || return 2
  if [[ -e $priv/current || -L $priv/current ]]; then
    printf '%s\n' "$priv"
  else
    printf '%s\n' "$shared"
  fi
}

# --- pointer and bundle ----------------------------------------------------

# pgh_read_pointer <dir>: print gen-N. Returns 13 if `current` is absent and 12
# if it is invalid: a symlink (the pointer is a regular file by rule), not a
# regular file, or not matching ^gen-[0-9]+$ on its first line.
pgh_read_pointer() {
  local dir=$1 cur line=""
  cur=$dir/current
  if [[ -L $cur ]]; then
    return 12
  fi
  if [[ ! -e $cur ]]; then
    return 13
  fi
  if [[ ! -f $cur ]]; then
    return 12
  fi
  IFS= read -r line <"$cur" || true
  case $line in
  gen-*[!0-9]* | gen- | "") return 12 ;;
  gen-[0-9]*)
    printf '%s\n' "$line"
    return 0
    ;;
  *) return 12 ;;
  esac
}

# pgh_bundle <dir> <gen>: print <dir>/<gen>/bundle (always, so a caller can
# name it in a message) and return 13 when the bundle has no bin/pg-hooks-run
# (no bundle), 12 when the bundle exists but bin/prek or bin/pg-hooks-run is not
# executable (broken), else 0.
pgh_bundle() {
  local b=$1/$2/bundle
  printf '%s\n' "$b"
  if [[ ! -e $b/bin/pg-hooks-run ]]; then
    return 13
  fi
  if [[ ! -x $b/bin/prek || ! -x $b/bin/pg-hooks-run ]]; then
    return 12
  fi
  return 0
}

# pgh_write_pointer <dir> <gen>: point <dir>/current at <gen>. The pointer is a
# REGULAR file replaced by rename(2) from a temp file in the same directory (a
# symlink swap is not atomic for concurrent readers on APFS; BSD `mv -f` onto a
# symlink-to-directory silently fails, so a legacy symlink pointer is removed
# first: it is an invalid state readers already treat as broken). Returns 2 for
# an invalid <gen> and 1 when the pointer cannot be written. Used by the
# installer (Task 4); also callable from tests that race readers against swaps.
pgh_write_pointer() {
  local dir=$1 gen=$2 tmp
  case $gen in
  gen-*[!0-9]* | gen- | "") return 2 ;;
  gen-[0-9]*) ;;
  *) return 2 ;;
  esac
  if [[ -d $dir/current && ! -L $dir/current ]]; then
    return 1
  fi
  tmp=$(mktemp "$dir/.current.XXXXXX") || return 1
  if ! printf '%s\n' "$gen" >"$tmp" || ! chmod 644 "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  if [[ -L $dir/current ]]; then
    rm -f "$dir/current"
  fi
  if ! mv -f "$tmp" "$dir/current"; then
    rm -f "$tmp"
    return 1
  fi
  return 0
}

# --- stamp -----------------------------------------------------------------

# pgh_stamp <input>...: `git ls-files -s -- <inputs> | git hash-object --stdin`
# (the index, so a staged edit counts); prints "unknown" when none of the inputs
# is tracked. Callers pass `flake.lock flake.nix` plus meta.json stampPaths and
# run it from the work tree root. Two git processes.
pgh_stamp() {
  local listing
  listing=$(git ls-files -s -- "$@" 2>/dev/null) || listing=""
  if [[ -z $listing ]]; then
    printf 'unknown\n'
    return 0
  fi
  printf '%s\n' "$listing" | git hash-object --stdin
}

# --- messages (spec 5.4, 4.3, 4.5) -----------------------------------------

# pgh_reinstall <dir>: the `reinstall` file line, else the canonical command.
pgh_reinstall() {
  local dir=$1 line="" k
  if [[ -f $dir/reinstall ]]; then
    IFS= read -r line <"$dir/reinstall" || true
  fi
  if [[ -z $line ]]; then
    k=$(pgh_canonical) || k="<canonical>"
    line="(cd $k && nix run .#install-pre-commit-hooks)"
  fi
  printf '%s\n' "$line"
}

# pgh_private_reinstall: the command that builds a worktree-only bundle.
pgh_private_reinstall() {
  local top=${PGH_TOP:-}
  if [[ -z $top ]]; then
    top=$(git rev-parse --path-format=absolute --show-toplevel 2>/dev/null) || top="<worktree>"
  fi
  printf '(cd %s && nix run .#install-pre-commit-hooks -- --private)\n' "$top"
}

# pgh_msg_no_bundle <repo> <stage> [<canonical> [<linked 0|1> [<fix>]]]
# The optional arguments default from the git context. The stub
# (stub.sh.in) carries a hand-written copy of this text; a test asserts they
# are identical.
pgh_msg_no_bundle() {
  local repo=$1 stage=$2 canonical=${3:-} linked=${4:-} fix=${5:-}
  if [[ -z $canonical ]]; then
    canonical=$(pgh_canonical) || canonical="<canonical>"
  fi
  if [[ -z $linked ]]; then
    if pgh_is_linked; then linked=1; else linked=0; fi
  fi
  if [[ -z $fix ]]; then
    if [[ $linked == 1 ]]; then
      fix=$(pgh_reinstall "$(pgh_private_dir)")
    else
      fix="(cd $canonical && nix run .#install-pre-commit-hooks)"
    fi
  fi
  if [[ $linked == 1 ]]; then
    printf 'pg-hooks: no hook bundle for %s (shared bundle lives in %s); %s hooks not run. Fix: %s\n' \
      "$repo" "$canonical" "$stage" "$fix" >&2
  else
    printf 'pg-hooks: no hook bundle for %s; %s hooks not run. Fix: %s\n' \
      "$repo" "$stage" "$fix" >&2
  fi
}

# pgh_msg_broken <repo> <reason> [<dir>]
pgh_msg_broken() {
  local repo=$1 reason=$2 dir=${3:-} reinstall
  if [[ -z $dir ]]; then
    dir=$(pgh_select_dir) || dir=""
  fi
  reinstall=$(pgh_reinstall "$dir")
  printf 'pg-hooks: hook bundle for %s is broken (%s). Rebuild: %s\n' \
    "$repo" "$reason" "$reinstall" >&2
}

# pgh_msg_stale <repo> <built_at> <reinstall> [<notes>]
# <notes> is the optional " (override <name> changed)..." suffix.
pgh_msg_stale() {
  printf 'pg-hooks: hook bundle is stale for %s (built %s)%s. Rebuild: %s\n' \
    "$1" "$2" "${4:-}" "$3" >&2
}

# pgh_msg_worktree_differs <private reinstall>
pgh_msg_worktree_differs() {
  printf "pg-hooks: this worktree's hook definitions differ from the shared bundle; the old hooks ran. To test your hook changes: %s\\n" \
    "$1" >&2
}

# pgh_msg_hooks_failed <stage> <repo>
pgh_msg_hooks_failed() {
  printf "pg-hooks: %s hooks failed in %s. Auto-fixable formatting: run 'pg-hooks fix', then git add and commit again. Other failures: fix by hand.\\n" \
    "$1" "$2" >&2
}

# pgh_msg_unreachable <common-dir> <value> <origin> <canonical>
pgh_msg_unreachable() {
  printf 'pg-hooks: git does not run hooks from %s/hooks (core.hooksPath=%s from %s). Operator: git -C %s config --local --unset core.hooksPath\n' \
    "$1" "$2" "$3" "$4" >&2
}

# pgh_msg_relocated <clone_path> <reinstall>
pgh_msg_relocated() {
  printf 'pg-hooks: this clone moved from %s; its bundle is no longer GC-rooted. Rebuild: %s\n' \
    "$1" "$2" >&2
}

# --- progress line (noise policy: at most one line after N seconds) ---------

PGH_PROGRESS_PID=""

# pgh_progress_start <seconds> <message>: after <seconds>, print <message> once
# to stderr unless pgh_progress_stop ran first.
pgh_progress_start() {
  local after=$1 msg=$2
  (
    sp=""
    trap 'if [ -n "$sp" ]; then kill "$sp" 2>/dev/null; fi; exit 0' TERM
    sleep "$after" >/dev/null 2>&1 &
    sp=$!
    if wait "$sp"; then
      printf '%s\n' "$msg" >&2
    fi
  ) &
  PGH_PROGRESS_PID=$!
}

pgh_progress_stop() {
  if [[ -n ${PGH_PROGRESS_PID:-} ]]; then
    kill "$PGH_PROGRESS_PID" 2>/dev/null || true
    wait "$PGH_PROGRESS_PID" 2>/dev/null || true
    PGH_PROGRESS_PID=""
  fi
}

# --- staleness (spec 4.5) --------------------------------------------------

# _pgh_overrides_changed <source.json>: print " (override <name> changed)" for
# every recorded override whose HEAD differs from now or whose dirty state
# differs from the recorded one. Two git processes per override; none when no
# override is recorded.
_pgh_overrides_changed() {
  local src=$1 line name path head dirty now_head now_dirty out=""
  [[ -f $src ]] || return 0
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    name=$(jq -r '.name // ""' <<<"$line")
    path=$(jq -r '.path // ""' <<<"$line")
    head=$(jq -r '.head // ""' <<<"$line")
    dirty=$(jq -r '.dirty // false' <<<"$line")
    now_head=$(git -C "$path" rev-parse HEAD 2>/dev/null) || now_head=""
    if [[ -n $(git -C "$path" status --porcelain 2>/dev/null) ]]; then
      now_dirty=true
    else
      now_dirty=false
    fi
    if [[ $now_head != "$head" || $now_dirty != "$dirty" ]]; then
      out="$out (override $name changed)"
    fi
  done < <(jq -c '.overrides[]?' "$src" 2>/dev/null)
  printf '%s' "$out"
}

# pgh_stale_check <dir> <gen> <stage>: print at most one stale warning per
# (checkout, stamp), and only for pre-commit and pre-push. The marker
# stale-warned-<stamp> lives in the checkout's private git dir. Returns 0 always:
# staleness is advisory.
pgh_stale_check() {
  local dir=$1 gen=$2 stage=$3
  case $stage in
  pre-commit | pre-push) ;;
  *) return 0 ;;
  esac

  local top=${PGH_TOP:-}
  if [[ ${PGH_CTX_SET:-0} != 1 ]]; then
    pgh_ctx --toplevel || return 0
    top=$PGH_TOP
  fi
  [[ -n $top ]] || return 0

  local gdir=$dir/$gen
  local src=$gdir/source.json meta=$gdir/bundle/meta.json
  [[ -f $src ]] || return 0

  local recorded built_at repo p
  recorded=$(jq -r '.stamp // ""' "$src" 2>/dev/null) || recorded=""
  built_at=$(jq -r '.built_at // "unknown"' "$src" 2>/dev/null) || built_at="unknown"
  repo=""
  local -a inputs=(flake.lock flake.nix)
  if [[ -f $meta ]]; then
    repo=$(jq -r '.repo // ""' "$meta" 2>/dev/null) || repo=""
    while IFS= read -r p; do
      [[ -n $p ]] && inputs+=("$p")
    done < <(jq -r '.stampPaths[]?' "$meta" 2>/dev/null)
  fi
  if [[ -z $repo ]]; then
    repo=$(pgh_repo_name) || repo="unknown"
  fi

  local current
  current=$(
    cd "$top" 2>/dev/null || exit 0
    pgh_stamp "${inputs[@]}"
  ) || return 0
  [[ -n $current ]] || return 0

  local stamp_differs=0
  if [[ $current != unknown && $current != "$recorded" ]]; then
    stamp_differs=1
  fi
  local notes
  notes=$(_pgh_overrides_changed "$src")
  if [[ $stamp_differs == 0 && -z $notes ]]; then
    return 0
  fi

  local priv marker
  priv=$(pgh_private_dir) || return 0
  marker=$priv/stale-warned-$current
  if [[ -e $marker ]]; then
    return 0
  fi
  mkdir -p "$priv" 2>/dev/null || true
  : >"$marker" 2>/dev/null || true

  local shared
  shared=$(pgh_shared_dir) || shared=""
  if [[ $stamp_differs == 1 && $dir == "$shared" ]] && pgh_is_linked; then
    pgh_msg_worktree_differs "$(pgh_private_reinstall)"
  else
    pgh_msg_stale "$repo" "$built_at" "$(pgh_reinstall "$dir")" "$notes"
  fi
  return 0
}
