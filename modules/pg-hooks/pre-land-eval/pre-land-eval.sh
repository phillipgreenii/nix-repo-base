# shellcheck shell=bash

# pre-land-eval: the eval-only pre-land check (design pg2-3i12x section 9;
# un-deferred by the operator ruling on pg2-pla9d.26, 2026-10-23; beads
# pg2-d2yka and pg2-8uc2d). It is a thin caller of
#
#   pn workspace flake-check --no-build --caller ff-1c [--no-ifd] --rev <ref> <repo-path>
#
# which owns everything about the check itself: the drvPath evaluation of the
# flake's checks, packages and devShells (NOT nix's own `nix flake check
# --no-build`, which is read-only and fails on source copies at eval time), the
# --override-input set that pins the repo's workspace siblings to their LOCAL
# clones (so no push, relock or flake.lock write is needed), the system, the
# tolerance of an absent set, and the experiment log. This script derives no
# override and runs no nix; it adds only the two things the land step needs and pn
# must not know: the per-clone opt-in key, and the one-word verdict with its
# 0/10/2/127 exit codes.
#
# There is no retry and no inconclusive class (operator ruling, 2026-10-23): every
# failure of the check, environmental or not, is eval-failed.
#
# stdout is exactly ONE word; everything else, pn's output included, is stderr.
# Exit codes: 0 ok or not-enabled, 10 eval-failed, 2 usage error (including an
# unresolvable ref or a repo that is no workspace member), 127 pn not installed.
# BASH 3.2 STYLE (no mapfile, no associative arrays).

show_help() {
  cat <<'HELP'
pre-land-eval: eval-only pre-land check of a flake at a commit

Usage: pre-land-eval [--no-ifd] <repo-path> <ref>

Runs, from inside <repo-path>:

  pn workspace flake-check --no-build --caller ff-1c [--no-ifd] --rev <ref> <repo-path>

which evaluates (does not build) the derivation paths of the flake's checks,
packages and devShells for this machine's system at the commit <ref> of
<repo-path>, with the repo's workspace siblings pinned to their LOCAL clones
(working tree) by the same --override-input set pn workspace build and apply use.
No push, relock or flake.lock write is involved. A dirty tree and untracked files
of <repo-path> are ignored; a linked worktree is fine. See `pn workspace
flake-check --help`; the run is appended to the experiment log it names.

The check is opt-in per clone: it runs only when
  git -C <repo-path> config --get pgii-integrate-branch.preLandEval
prints exactly "true". Any other value, or no value, prints "not-enabled".

Options:
  --no-ifd       Pass --no-ifd to pn (nix --option allow-import-from-derivation
                 false). A needed import from derivation then fails the check.
  -h, --help     Show this help message
  -v, --version  Show version information

stdout: exactly one of ok, eval-failed, not-enabled.
stderr: pn's output (the reproduce commands, timings, the first lines of nix's error).

Exit codes:
  0   ok, or not-enabled
  10  eval-failed (pn reported a failed evaluation; there is no retry)
  2   usage error, including a ref that does not resolve and a repo that is no
      workspace member
  127 pn is not installed

Runs 1 to 3 minutes: run it through bgrun and bgwait.
HELP
}

say() {
  printf 'pre-land-eval: %s\n' "$*" >&2
}

die_usage() {
  say "$1"
  exit 2
}

# A GIT_DIR-family variable leaked from a hook or a worktree environment pins
# git's repository discovery and makes `git -C` look at the wrong repository.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_PREFIX GIT_OBJECT_DIRECTORY

NO_IFD=0
POS=()
while (($# > 0)); do
  case "$1" in
  --no-ifd)
    NO_IFD=1
    shift
    ;;
  -h | --help)
    show_help
    exit 0
    ;;
  --)
    shift
    while (($# > 0)); do
      POS+=("$1")
      shift
    done
    ;;
  -*)
    die_usage "unknown option: $1 (see --help)"
    ;;
  *)
    POS+=("$1")
    shift
    ;;
  esac
done

((${#POS[@]} == 2)) || die_usage "expected <repo-path> <ref> (see --help)"
REPO=${POS[0]}
REF=${POS[1]}

[[ -d $REPO ]] || die_usage "not a directory: $REPO"
REPO_ABS="$(cd "$REPO" && pwd -P)"

# Enablement: only the exact value "true" turns the check on. An absent key is
# the normal case and is never an error.
ENABLED="$(git -C "$REPO_ABS" config --get pgii-integrate-branch.preLandEval 2>/dev/null || true)"
if [[ $ENABLED != true ]]; then
  say "pre-land eval: not enabled for $REPO"
  printf 'not-enabled\n'
  exit 0
fi

if ! command -v pn >/dev/null 2>&1; then
  say "pn not installed on this machine; ask the operator to run pn workspace apply"
  exit 127
fi

PN_ARGS=(workspace flake-check --no-build --caller ff-1c)
if ((NO_IFD == 1)); then
  PN_ARGS+=(--no-ifd)
fi
PN_ARGS+=(--rev "$REF" "$REPO_ABS")

# pn finds the workspace root by walking up from the working directory (or
# PN_WORKSPACE_ROOT), so it runs in a subshell inside the repo. Its stdout goes to
# stderr: this tool's stdout is the verdict word alone.
RC=0
(cd "$REPO_ABS" && pn "${PN_ARGS[@]}") >&2 || RC=$?

case "$RC" in
0)
  printf 'ok\n'
  exit 0
  ;;
1)
  say "eval failed: pn workspace flake-check --no-build reported a failure (see its output above)"
  printf 'eval-failed\n'
  exit 10
  ;;
2)
  die_usage "pn rejected the invocation (a ref that does not resolve, or a repo that is no workspace member); see its message above"
  ;;
*)
  die_usage "pn workspace flake-check --no-build exited $RC; the check could not run"
  ;;
esac
