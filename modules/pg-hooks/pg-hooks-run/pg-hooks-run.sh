# shellcheck shell=bash

# pg-hooks-run: the per-clone hook bundle's stage runner (design: per-clone hook
# bundle, section 4.3). The static stub in <git-common-dir>/hooks execs
#   $BUNDLE/bin/pg-hooks-run <stage> [args...]
# with PG_HOOKS_BUNDLE=$BUNDLE exported (a bundle's own path is the fallback,
# derived from $0). The runner emits the advisory staleness warning, runs the
# bundle's prek as a CHILD (stdin and arguments pass through) and returns its
# exit status unchanged.
#
# BASH 3.2 COMPATIBLE: a launchd job with PATH=/usr/bin:/bin can reach macOS
# /bin/bash. See pg-hooks-lib.bash.

show_help() {
  cat <<'HELP'
pg-hooks-run: run the clone's hook bundle for one git hook stage

Usage: pg-hooks-run <stage> [args...]

Normally executed from a bundle's bin/ by the static stub in
<git-common-dir>/hooks, with PG_HOOKS_BUNDLE naming the bundle. Runs the
bundle's prek for <stage>, passing stdin and args through, and returns its
exit status.

Options:
  -h, --help     Show this help message
  -v, --version  Show version information

Environment:
  PG_HOOKS_BUNDLE            the bundle directory (default: derived from $0)
  PG_HOOKS_PROGRESS_AFTER_S  print one progress line after N seconds (default 3; 0 disables)

Exit status: prek's, or 12 when the bundle is broken, 2 on a usage error.
HELP
}

case ${1:-} in
-h | --help)
  show_help
  exit 0
  ;;
"" | -*)
  show_help >&2
  exit "$PGH_USAGE"
  ;;
esac
stage=$1
shift

bundle=${PG_HOOKS_BUNDLE:-}
if [[ -z $bundle ]]; then
  bundle=$(cd -P "$(dirname "$0")/.." && pwd -P)
fi

# One git process: absolute common dir, absolute git dir and (for the stages
# that compute a stamp) the work tree root. See pgh_ctx.
ctx_ok=1
case $stage in
pre-commit | pre-push) pgh_ctx --toplevel || ctx_ok=0 ;;
*) pgh_ctx || ctx_ok=0 ;;
esac

# The bundle lives at <dir>/<gen>/bundle, <dir> being the shared or private
# pg-hooks directory. Anything else (a store path) has no pointer context.
dir=""
gen=""
case $bundle in
*/gen-[0-9]*/bundle)
  genpath=${bundle%/bundle}
  gen=${genpath##*/}
  dir=${genpath%/*}
  ;;
esac

if [[ $ctx_ok == 1 ]]; then
  hook_dir=$PGH_COMMON/hooks
elif [[ -n $dir ]]; then
  hook_dir=${dir%/pg-hooks}/hooks
else
  hook_dir=""
fi

repo=""
if [[ -f $bundle/meta.json ]]; then
  repo=$(jq -r '.repo // ""' "$bundle/meta.json" 2>/dev/null) || repo=""
fi
if [[ -z $repo ]]; then
  repo=$(pgh_repo_name) || repo="unknown"
fi

if [[ ! -x $bundle/bin/prek ]]; then
  pgh_msg_broken "$repo" "bin/prek is not executable" "$dir"
  exit "$PGH_BROKEN"
fi

if [[ $ctx_ok == 1 && -n $dir && -n $gen ]]; then
  pgh_stale_check "$dir" "$gen" "$stage" || true
fi

prek_args=(-q hook-impl --config "$bundle/prek-config.json" --hook-type "$stage")
if [[ -n $hook_dir ]]; then
  prek_args+=(--hook-dir "$hook_dir")
fi
prek_args+=(--script-version 4 -- "$@")

progress_after=${PG_HOOKS_PROGRESS_AFTER_S:-3}
if [[ $progress_after =~ ^[0-9]+$ ]] && ((progress_after > 0)); then
  pgh_progress_start "$progress_after" "pg-hooks: running $stage hooks in $repo..."
  trap pgh_progress_stop EXIT
fi

# Run prek, and every hook it spawns, with git's fsmonitor off. prek hardcodes
# `-c core.useBuiltinFSMonitor=false` (crates/prek/src/git.rs) to avoid the
# daemon, but git has long since replaced that key with core.fsmonitor, so on a
# modern git the flag is ignored: with core.fsmonitor=true in the repo config
# prek's own `git write-tree` / `git diff` block forever in
# fsmonitor_ipc__send_query whenever the fsmonitor daemon (or the OS file-event
# service behind it) stops answering, holding index.lock and the prek lock.
# GIT_CONFIG_COUNT is git's env-based -c: append our entry after any the caller
# already set. An env entry outranks the config files, so this wins over a
# repo's core.fsmonitor=true.
gc_n=${GIT_CONFIG_COUNT:-0}
[[ $gc_n =~ ^[0-9]+$ ]] || gc_n=0
gc_n=$((10#$gc_n)) # base 10: bash would read a zero-padded "08" as invalid octal
export "GIT_CONFIG_KEY_$gc_n=core.fsmonitor" "GIT_CONFIG_VALUE_$gc_n=false"
export GIT_CONFIG_COUNT=$((gc_n + 1))

rc=0
"$bundle/bin/prek" "${prek_args[@]}" || rc=$?
pgh_progress_stop

if ((rc != 0)); then
  pgh_msg_hooks_failed "$stage" "$repo"
fi
exit "$rc"
