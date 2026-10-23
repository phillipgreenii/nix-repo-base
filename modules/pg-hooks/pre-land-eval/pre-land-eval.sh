# shellcheck shell=bash

# pre-land-eval: the eval-only pre-land check (design pg2-3i12x section 9;
# un-deferred by the operator ruling on pg2-pla9d.26, 2026-10-23; bead
# pg2-d2yka). It evaluates, at one COMMIT, the derivation paths of the flake's
# checks, packages and devShells for this machine's system, so an evaluation
# error (a missing option, a throw, a bad import) is caught before landing
# without paying for the builds.
#
# Mechanism: `nix eval --json <ref>#<set>.<system> --apply 'drvPath of each'`,
# once per set. It is NOT `nix flake check --no-build`: that runs read-only, so
# any source tree copied into the store at eval time reads back as "path is
# not valid". An absent set (for example a flake with no devShells) is
# tolerated. darwinConfigurations is not evaluated (flake check does not cover
# it either).
#
# Sibling overrides (bead pg2-8uc2d): a repo whose flake.lock pins workspace
# siblings that lag the local clones would fail on drift that a relock fixes
# later. The tool therefore asks `pn workspace overrides <repo>` for the repo's
# `--override-input <alias> git+file://<sibling clone>` arguments, the SAME set
# pn workspace build/apply/flake-check inject, and passes them to every nix
# eval. It derives no override itself. No push, no relock and no flake.lock write
# is involved (--no-write-lock-file stays). When pn is absent, or the repo is not
# a workspace member, one stderr note says so and the locked inputs are evaluated.
#
# The tool does NOT run prek or pg-hooks (`pg-hooks run pre-land` owns that).
# It never cds into the repo for git or nix: git runs with -C, and the commit is
# evaluated as a pure `git+file://<abs path>?rev=<sha>` ref, so a dirty tree and
# untracked files are ignored and a linked worktree works (shared object store).
# Only the pn lookup runs in a subshell inside the repo, because pn finds the
# workspace root by walking up from the working directory (or PN_WORKSPACE_ROOT).
#
# stdout is exactly ONE word; everything else is stderr.
# Exit codes: 0 ok or not-enabled, 10 eval-failed, 11 eval-inconclusive,
# 2 usage error (including an unresolvable ref).
# BASH 3.2 STYLE (no mapfile, no associative arrays).

show_help() {
  cat <<'HELP'
pre-land-eval: eval-only pre-land check of a flake at a commit

Usage: pre-land-eval [--no-ifd] [--no-overrides] <repo-path> <ref>

Evaluates (does not build) the derivation paths of the flake's checks, packages
and devShells for this machine's system at the commit <ref> of <repo-path>,
as the pure flake ref git+file://<abs repo-path>?rev=<sha>. A dirty tree and
untracked files are ignored; a linked worktree is fine.

The repo's workspace siblings are pinned to their LOCAL clones with the
--override-input arguments that `pn workspace overrides <repo-path>` prints
(the set pn workspace build/apply/flake-check use), so siblings that lag in
flake.lock do not fail the check and no push, relock or flake.lock write is
needed. If pn is not on PATH, or <repo-path> is not a workspace repo, one note
goes to stderr and the locked inputs are evaluated instead.

The check is opt-in per clone: it runs only when
  git -C <repo-path> config --get pgii-integrate-branch.preLandEval
prints exactly "true". Any other value, or no value, prints "not-enabled".

Options:
  --no-ifd       Pass --option allow-import-from-derivation false. Import from
                 derivation is allowed by default, which makes the eval BUILD
                 the small IFD derivations (a store-write operation). With
                 --no-ifd an IFD error is classified eval-inconclusive.
  --no-overrides Do not ask pn for sibling overrides: evaluate the locked
                 inputs only (how the published lock resolves).
  -h, --help     Show this help message
  -v, --version  Show version information

stdout: exactly one of ok, eval-failed, eval-inconclusive, not-enabled.
stderr: details (the first lines of nix's error, a reproduce command, timings).

Exit codes:
  0   ok, or not-enabled
  10  eval-failed
  11  eval-inconclusive (--no-ifd only, or a system it cannot name)
  2   usage error, including a ref that does not resolve

Runs 1 to 2 minutes: run it through bgrun and bgwait. It runs nix through
pg-nix-log-wrapped when that command is on PATH, and directly otherwise.
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
NO_OVERRIDES=0
POS=()
while (($# > 0)); do
  case "$1" in
  --no-ifd)
    NO_IFD=1
    shift
    ;;
  --no-overrides)
    NO_OVERRIDES=1
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

SHA="$(git -C "$REPO_ABS" rev-parse --verify --quiet --end-of-options "$REF^{commit}" 2>/dev/null || true)"
[[ -n $SHA ]] || die_usage "ref does not resolve to a commit in $REPO: $REF"

# The system comes from uname, not from an impure builtins.currentSystem.
case "$(uname -m)" in
arm64 | aarch64) ARCH=aarch64 ;;
x86_64 | amd64) ARCH=x86_64 ;;
*) ARCH="" ;;
esac
case "$(uname -s)" in
Darwin) OS=darwin ;;
Linux) OS=linux ;;
*) OS="" ;;
esac
if [[ -z $ARCH || -z $OS ]]; then
  say "cannot derive a nix system from uname ($(uname -sm)); nothing evaluated"
  printf 'eval-inconclusive\n'
  exit 11
fi
SYSTEM="$ARCH-$OS"

# URL-encode the characters that would end or split the path part of the ref.
ENC_PATH="$(printf '%s' "$REPO_ABS" | sed -e 's/%/%25/g' -e 's/ /%20/g' -e 's/?/%3F/g' -e 's/#/%23/g')"
FLAKE_REF="git+file://${ENC_PATH}?rev=${SHA}"

if command -v pg-nix-log-wrapped >/dev/null 2>&1; then
  NIX_CMD=(pg-nix-log-wrapped nix)
else
  say "pg-nix-log-wrapped is not on PATH; running nix directly (no telemetry)"
  NIX_CMD=(nix)
fi
NIX_OPTS=(--extra-experimental-features 'nix-command flakes' --no-write-lock-file)
if ((NO_IFD == 1)); then
  NIX_OPTS+=(--option allow-import-from-derivation false)
fi

APPLY='set: builtins.mapAttrs (n: v: v.drvPath or null) set'

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Sibling overrides from pn: one argument per line (--override-input, alias,
# git+file://<sibling>), repeated. The lookup runs in a subshell inside the repo
# so pn finds the workspace root by walking up (PN_WORKSPACE_ROOT wins when set).
# Any failure degrades to the locked inputs, with one note.
if ((NO_OVERRIDES == 1)); then
  say "sibling overrides disabled (--no-overrides); evaluating the locked inputs"
elif ! command -v pn >/dev/null 2>&1; then
  say "pn is not on PATH; evaluating the locked sibling inputs (no workspace overrides)"
else
  if (cd "$REPO_ABS" && pn workspace overrides "$REPO_ABS") >"$TMP/overrides.out" 2>"$TMP/overrides.err"; then
    N_OVERRIDES=0
    while IFS= read -r line; do
      [[ -n $line ]] || continue
      NIX_OPTS+=("$line")
      if [[ $line == --override-input ]]; then N_OVERRIDES=$((N_OVERRIDES + 1)); fi
    done <"$TMP/overrides.out"
    say "pinned $N_OVERRIDES workspace sibling input(s) to their local clones (pn workspace overrides)"
  else
    say "pn workspace overrides failed for $REPO_ABS ($(head -n 1 "$TMP/overrides.err")); evaluating the locked sibling inputs"
  fi
fi

IFD_RE="allow-import-from-derivation' is disabled"
ABSENT_RE="^[[:space:]]*error: flake '.*' does not provide attribute "

# classify_failure <stderr file>: print absent, ifd or failed for a nix eval
# that exited non-zero. nix reports each error as one final "error: <message>"
# line (the bare "error:" opening line of a trace carries no message), so the
# message lines are what is counted.
classify_failure() {
  local msgs nonifd
  msgs="$(grep -E '^[[:space:]]*error: .+' "$1" || true)"
  if [[ -z $msgs ]]; then
    echo failed
    return 0
  fi
  nonifd="$(printf '%s\n' "$msgs" | grep -v -F -e "$IFD_RE" || true)"
  if [[ -z $nonifd ]]; then
    # Only IFD errors. Without --no-ifd, IFD is allowed, so this is a real failure.
    if ((NO_IFD == 1)); then echo ifd; else echo failed; fi
    return 0
  fi
  if [[ $nonifd == "$msgs" ]] && ! printf '%s\n' "$nonifd" | grep -v -E -e "$ABSENT_RE" >/dev/null; then
    echo absent
    return 0
  fi
  echo failed
}

N_OK=0
N_ABSENT=0
N_IFD=0
N_FAILED=0
FAILED_SETS=""
T_START=$SECONDS

for set in checks packages devShells; do
  OUT="$TMP/$set.out"
  ERR="$TMP/$set.err"
  ATTR="${FLAKE_REF}#${set}.${SYSTEM}"
  say "reproduce: ${NIX_CMD[*]} eval ${NIX_OPTS[*]} --json '$ATTR' --apply '$APPLY'"
  T_SET=$SECONDS
  RC=0
  "${NIX_CMD[@]}" eval "${NIX_OPTS[@]}" --json "$ATTR" --apply "$APPLY" >"$OUT" 2>"$ERR" || RC=$?
  DT=$((SECONDS - T_SET))
  if ((RC == 0)); then
    N_OK=$((N_OK + 1))
    say "$set.$SYSTEM: ok, $( (grep -o '\.drv"' "$OUT" || true) | wc -l | tr -d ' ') derivations evaluated (${DT}s)"
    continue
  fi
  KIND="$(classify_failure "$ERR")"
  case "$KIND" in
  absent)
    N_ABSENT=$((N_ABSENT + 1))
    say "$set.$SYSTEM: not provided by the flake, skipped (${DT}s)"
    ;;
  ifd)
    N_IFD=$((N_IFD + 1))
    say "$set.$SYSTEM: needs import from derivation, which --no-ifd disables (${DT}s)"
    head -n 10 "$ERR" >&2
    ;;
  *)
    N_FAILED=$((N_FAILED + 1))
    FAILED_SETS="$FAILED_SETS $set"
    say "$set.$SYSTEM: nix exited $RC (${DT}s); first lines of its error follow"
    head -n 10 "$ERR" >&2
    # A long trace buries the actual message at the end: repeat it.
    if (($(wc -l <"$ERR") > 10)); then
      say "$set.$SYSTEM: the final error message(s):"
      (grep -E '^[[:space:]]*error: .+' "$ERR" || true) | tail -n 3 >&2
    fi
    ;;
  esac
done

say "total $((SECONDS - T_START))s evaluating $SYSTEM at $SHA ($REPO_ABS)"
if ((N_OK == 0 && N_FAILED == 0 && N_IFD == 0)); then
  say "the flake provides none of checks, packages or devShells for $SYSTEM; nothing was evaluated"
fi

if ((N_FAILED > 0)); then
  say "eval failed in:$FAILED_SETS"
  printf 'eval-failed\n'
  exit 10
fi
if ((N_IFD > 0)); then
  say "inconclusive: re-run without --no-ifd to evaluate through import from derivation"
  printf 'eval-inconclusive\n'
  exit 11
fi
printf 'ok\n'
exit 0
