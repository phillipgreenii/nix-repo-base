# shellcheck shell=bash

# pg-go-mutate: report which assertions a Go package's tests are missing, by
# wrapping the gomu mutation-testing engine. Orchestration only -- every
# pgm_* function it calls lives in pg-go-mutate-lib.bash (prepended by the
# builder). Exits 0 whenever it completed an analysis, however many mutants
# survived (spec: this is a diagnostic, not a gate) -- non-zero is reserved
# for operational failure (a guard failing, gomu/go absent, invalid flags, an
# unreadable target, a missing/insane report, or every concurrency-semaphore
# slot staying busy past its timeout (exit 3, beads pg2-y3a8t/pg2-1qcro.3)).

usage() {
  cat <<'EOF'
pg-go-mutate — report which assertions a Go package's tests are missing.

USAGE
  pg-go-mutate [PATH] [options]

  PATH   A directory, walked RECURSIVELY so nested packages are included.
         Defaults to the current directory. Go package patterns such as ./...
         are NOT accepted, and single-file targets are not supported.

OPTIONS
  --tags <list>     Comma-separated build tags to enable, e.g. contract,smoke.
  --json            Emit the machine-readable worklist instead of the human one.
  --timeout <sec>   Per-mutant TEST timeout. Default 60. Does NOT bound the
                    compile phase, which the engine runs unbounded.
  --workers <n>     IGNORED. Still validated (must be >= 1), but every gomu
                    invocation is now forced to --workers 1 regardless of this
                    value -- concurrency comes solely from the semaphore slot
                    count instead (pg-go-mutate-tui's own `concurrency`
                    setting). Default 1. Kept for backward-compatible flag
                    parsing.
  --keep-report     Do not delete the harvested engine report on exit; print
                    its path instead. The worklist output above is unchanged --
                    this only preserves the underlying per-mutant report file.
  -h, --help        Show this help.

NOTES
  Every surviving mutant is an assertion your tests do not make. This command is
  a diagnostic: it exits 0 whenever it completed an analysis, however many
  mutants survived, and gates nothing. A non-zero exit means the run itself
  failed.

  Cost is roughly (number of mutants) x (the package's test-suite runtime), so
  scope the run by passing a narrow PATH.
EOF
  # Printed OUTSIDE the quoted heredoc so the pinned version is the real baked
  # one. Disclosed in --help, not only in the design doc, because this is the
  # only place a consumer who installed the package DIRECTLY -- via
  # pkgs.pg-go-mutate or overlays.default rather than homeModules.pg-go-mutate --
  # will ever read it. Empty in a raw-source run, which has no build-time pin.
  printf '\nENGINE\n'
  if [ -n "${PGM_PINNED_GOMU_VERSION:-}" ]; then
    printf '  This build is pinned to gomu %s and refuses to run against any other\n' "$PGM_PINNED_GOMU_VERSION"
    # Backticks would trip SC2016 inside a single-quoted string, so the build
    # name is quoted rather than code-fenced.
    printf '  version, including a "dev" build (no release ldflags, so its results are\n'
    printf '  unattributable). Installing via homeModules.pg-go-mutate additionally\n'
    printf '  binds the engine by store path, so an ambient gomu cannot be used at all.\n'
    printf '  PG_GO_MUTATE_GOMU overrides the binary; PG_GO_MUTATE_GOMU_VERSION\n'
    printf '  overrides the expected version, and an EMPTY value skips the check.\n'
  else
    printf '  Running from raw source, so no engine version is pinned: whatever gomu\n'
    printf '  resolves from PG_GO_MUTATE_GOMU or PATH is used unchecked.\n'
  fi
}

target="."
workers=1
timeout=60
tags=""
as_json=0
keep_report=0

while [ $# -gt 0 ]; do
  case "$1" in
  -h | --help)
    usage
    exit 0
    ;;
  --json)
    as_json=1
    shift
    ;;
  --keep-report)
    keep_report=1
    shift
    ;;
  --tags)
    # `${2:?...}` would exit 1 with a bash-shaped "line N: 2: --tags needs a
    # value", unlike every other flag error here (pg-go-mutate: … + exit 2).
    [ $# -ge 2 ] || {
      printf 'pg-go-mutate: --tags needs a value\n' >&2
      exit 2
    }
    tags="$2"
    # Validated because the value is interpolated straight into GOFLAGS, where
    # an unvalidated one injects arbitrary go flags:
    # --tags 'x -toolexec=/bin/sh' would be honoured by every `go` subprocess.
    case "$tags" in
    '' | *[!A-Za-z0-9_,.]* | [!A-Za-z0-9_]*)
      printf 'pg-go-mutate: --tags must be a comma-separated build-tag list matching [A-Za-z0-9_][A-Za-z0-9_,.]*, got '\''%s'\''\n' "$tags" >&2
      exit 2
      ;;
    esac
    shift 2
    ;;
  --workers)
    [ $# -ge 2 ] || {
      printf 'pg-go-mutate: --workers needs a value\n' >&2
      exit 2
    }
    workers="$2"
    shift 2
    ;;
  --timeout)
    [ $# -ge 2 ] || {
      printf 'pg-go-mutate: --timeout needs a value\n' >&2
      exit 2
    }
    timeout="$2"
    shift 2
    ;;
  --)
    # A bare `break` here DISCARDED everything after the separator, so
    # `pg-go-mutate -- ./pkg` silently analysed `.` instead -- a wrong-scope run
    # that looks entirely legitimate. Written as an `if` rather than an
    # `&&` list because a false test as the last command of this case branch
    # would trip the injected errexit inside the while body.
    shift
    if [ $# -gt 0 ]; then
      target="$1"
      shift
    fi
    ;;
  -*)
    printf 'pg-go-mutate: unknown flag %s\n\n' "$1" >&2
    usage >&2
    exit 2
    ;;
  *)
    target="$1"
    shift
    ;;
  esac
done

case "$target" in
*'...'*)
  printf 'pg-go-mutate: Go package patterns such as ./... are not accepted; the engine errors on them. Pass a directory — it is walked recursively.\n' >&2
  exit 2
  ;;
esac

# A directory, or a single .go file. The health guard (go vet / go test) is
# inherently package-scoped, so a lone file target still resolves to its
# containing directory for guard purposes -- pgm_resolve_guard_target does
# that resolution. $target itself keeps its KIND (file stays a file, directory
# stays a directory) -- only $guard_target is what the guard section below,
# and pgm_run_engine's private workdir, cd into.
guard_target="$(pgm_resolve_guard_target "$target")" || {
  printf 'pg-go-mutate: %s is not a directory or a .go file\n' "$target" >&2
  exit 14
}
# $target must be made absolute here, not left relative (the default is "."):
# pgm_run_engine cds into a private workdir before invoking the engine with
# "$target" as an argument, so a relative target would resolve against that
# workdir instead of the caller's original directory.
if [ -d "$target" ]; then
  # shellcheck disable=SC2164  # cd failure here would mean $target (already confirmed a directory above) vanished in the interim; nothing safer to fall back to
  target="$(cd "$target" && pwd)"
else
  # shellcheck disable=SC2164  # ditto, for the containing directory of a file target pgm_resolve_guard_target already confirmed exists
  target="$(cd "$(dirname -- "$target")" && pwd)/$(basename -- "$target")"
fi
# guard_target is what the health guard cds into; $target (now absolute, but
# still a file target if it started as one) is what gets passed to
# `gomu run` below -- a file target stays a file target there.
guard_target="$(cd "$guard_target" && pwd)"

pgm_validate_flags "$workers" "$timeout" || exit 2

# N-slot semaphore instead of a single exclusive lock (pg2-1qcro.3):
# concurrency is now bounded by pg-go-mutate-tui's own `concurrency` setting
# (pgm_sem_capacity), read fresh from the static nix-rendered config on every
# acquisition -- never cached as runtime-mutable shared state (design
# §5.3/§11). The common case -- no pg-go-mutate-tui config rendered at all,
# so capacity defaults to 1 -- behaves exactly like the single exclusive lock
# this replaces.
#
# Blocks up to PGM_SEM_TIMEOUT (default one hour) rather than the old lock's
# instant fail-fast: unlike that lock, several concurrent callers up to the
# configured capacity are now legitimate, so a caller should wait for a slot
# to free rather than be refused the moment every slot is briefly busy.
pgm_sem_acquire "${PGM_SEM_TIMEOUT:-3600}" || exit 3
# Installed NOW, before any guard below can exit, so every one of those exit
# paths (pgm_require_go/pgm_require_engine/pgm_has_tests/pgm_tests_healthy,
# and pgm_run_engine's own `|| exit 1`) still releases the slot via the EXIT
# trap even though none of them calls cleanup explicitly. Superseded below,
# once the harvested report is known, by a combined trap that ALSO runs
# report_cleanup -- a later `trap ... EXIT` assignment REPLACES rather than
# chains, so that later statement is written to call both. pgm_sem_release
# needs no once-only wrapper (unlike the old lock's release): it is
# naturally idempotent, so an INT/TERM/HUP handler's own `exit N`
# re-triggering the EXIT trap, and so calling it twice for the same run, is
# harmless.
trap 'pgm_sem_release' EXIT
trap 'pgm_sem_release; exit 130' INT
trap 'pgm_sem_release; exit 143' TERM HUP

pgm_require_go || exit 13
# The engine must exist AND be the pinned build (spec E1). Checked here rather
# than discovered as "the engine produced no report (exit 127)" after every
# guard has already spent its time.
pgm_require_engine || exit 13

# BEFORE the guards, not just before the engine. `go list`, `go vet` and
# `go test` cannot see tag-gated tests without this, so a package whose tests
# are entirely behind a custom tag aborted with "has no test files" even when
# --tags was passed — and worse, tag-gated tests that fail to compile or already
# fail stayed invisible to the guards while the engine measured them and
# reported every mutant KILLED, which is the most dangerous failure mode the
# guards exist to prevent. APPEND, never clobber (spec W11); pgm_run_engine's
# own export is idempotent against this one.
if [ -n "$tags" ]; then
  export GOFLAGS="-tags=$tags ${GOFLAGS:-}"
fi

# Per-package guard cache (bead pg2-1qcro.2): the guard below (pgm_has_tests +
# pgm_tests_healthy) is package-scoped and pure of the package's *.go content,
# so it is keyed on a content hash and skipped entirely on a hit. A cached
# PASS short-circuits straight past both guard calls; a cached FAIL exits 12
# without re-running pgm_tests_healthy. Only the healthy/unhealthy verdict is
# cached -- the has-tests/exit-10/exit-11 diagnosis below is cheap and always
# re-run so its message stays accurate.
pkg_hash="$(pgm_pkg_hash "$guard_target")"
if cached="$(pgm_guard_cache_get "$guard_target" "$pkg_hash")"; then
  [ "$cached" = "PASS" ] || exit 12
else
  has_tests_rc=0
  pgm_has_tests "$guard_target" || has_tests_rc=$?
  case "$has_tests_rc" in
  0) ;;
  2)
    # pgm_has_tests already reported the enumeration failure in detail.
    exit 11
    ;;
  *)
    printf 'pg-go-mutate: %s has no test files. Write a test first — mutation testing reports missing ASSERTIONS, and with no tests every mutant trivially survives.\n' "$target" >&2
    exit 10
    ;;
  esac

  if pgm_tests_healthy "$guard_target"; then
    pgm_guard_cache_put "$guard_target" "$pkg_hash" "PASS"
  else
    pgm_guard_cache_put "$guard_target" "$pkg_hash" "FAIL"
    exit 12
  fi
fi

detected_tags="$(pgm_detect_tags "$guard_target")"
if [ -z "$tags" ] && [ -n "$detected_tags" ]; then
  printf 'pg-go-mutate: NOTE %s has tests behind build tags (%s) that are not enabled.\n' "$target" "$detected_tags" >&2
  printf '              Mutants covered only by those tests will appear as survivors.\n' >&2
  printf '              Re-run with --tags %s to include them.\n\n' "$detected_tags" >&2
fi

# Disclose any .gomuignore in effect: the engine discovers it by walking from
# the target to the filesystem root, so a stray file outside the project can
# silently change which files are mutated (spec C8).
ignore_dir="$target"
while :; do
  [ -f "$ignore_dir/.gomuignore" ] && {
    printf 'pg-go-mutate: NOTE honouring %s/.gomuignore\n\n' "$ignore_dir" >&2
    break
  }
  [ "$ignore_dir" = "/" ] && break
  ignore_dir="$(dirname "$ignore_dir")"
done

# Every semaphore-dispatched gomu invocation is forced to --workers 1: the
# semaphore's slot count is now the SOLE concurrency dimension (design
# §5.3) -- letting gomu additionally fan out across its own worker pool
# would multiply concurrency by both dimensions at once, exactly the
# cross-process contention the semaphore (and the exclusive lock it
# replaces) exists to prevent. Any --workers value the caller passed on this
# command's own line is still validated above (pgm_validate_flags) but never
# reaches the engine -- there is no code path left that forwards it.
#
# GOMAXPROCS is capped to this run's fair share of the machine's cores
# instead of Go's own default of "every core": with capacity-many gomu
# invocations able to run at once, each defaulting to using every core would
# multiply core contention by that same capacity. Floored at 1 -- a machine
# with fewer cores than configured slots must still make forward progress,
# one core each, rather than computing 0 and leaving GOMAXPROCS unset to
# Go's uncapped default.
sem_cap="$(pgm_sem_capacity)"
gomaxprocs=$(($(nproc) / sem_cap))
[ "$gomaxprocs" -ge 1 ] || gomaxprocs=1
export GOMAXPROCS="$gomaxprocs"

# NOT `report="$(pgm_run_engine …)"`. A command substitution would run the engine
# supervisor in a SUBSHELL, and that subshell would own the private workdir, the
# engine's pid and the INT/TERM/HUP trap that cleans both up — so a `kill` on THIS
# script's pid would kill this shell and orphan the engine, leaking the workdir.
# That is the exact failure observed on this branch. Called plainly instead, with
# the path read back from PGM_REPORT_PATH, the traps live in the process a user or
# agent actually signals.
pgm_run_engine "$target" 1 "$timeout" "$tags" >/dev/null || exit 1
report="${PGM_REPORT_PATH:-}"
[ -n "$report" ] || {
  printf 'pg-go-mutate: the engine step reported success but produced no report path\n' >&2
  exit 1
}
# All four signals, not EXIT alone (spec CL4): an untrapped TERM/HUP never ran
# the EXIT trap, so the harvested report file leaked. The signal handlers exit
# rather than fall through, because a handler that merely returns resumes the
# pipeline it interrupted and would print a worklist for a run the user killed.
#
# --keep-report suppresses the rm and prints the path instead of removing it.
# report_cleanup is invoked from every trap below (matching this script's
# existing pgm_run_engine/pgm-go-mutate-sweep convention of calling the same
# cleanup function from the signal traps AND letting the eventual `exit` also
# fire the EXIT trap) but guards against running its visible side effect
# twice: an INT/TERM/HUP trap's own `exit N` triggers the EXIT trap a second
# time, and `rm -f` tolerates that for free, but printing the kept-report
# path would otherwise print twice for one interrupted run.
report_cleanup_done=0
# shellcheck disable=SC2329  # invoked only via the trap strings below, never a direct call
report_cleanup() {
  [ "$report_cleanup_done" -eq 0 ] || return 0
  report_cleanup_done=1
  if [ "$keep_report" -eq 1 ]; then
    printf 'pg-go-mutate: report kept at %s\n' "$report" >&2
  else
    rm -f -- "$report"
  fi
}
# Chained with pgm_sem_release (never a second, independent `trap`
# statement): a later `trap ... EXIT` assignment REPLACES the earlier one
# installed above rather than adding to it, so writing these as
# report_cleanup alone would silently stop releasing the semaphore slot from
# this point on. pgm_sem_release is a no-op once the slot is already
# removed, so chaining it here is safe on every path.
trap 'report_cleanup; pgm_sem_release' EXIT
trap 'report_cleanup; pgm_sem_release; exit 130' INT
trap 'report_cleanup; pgm_sem_release; exit 143' TERM HUP

pgm_report_sane "$report" || exit 1

# What the JSON renderer reports as buildTagsNotRun: tags detected in the target
# but NOT enabled for this run.
tags_not_run=""
if [ -z "$tags" ]; then
  tags_not_run="$detected_tags"
fi

if [ "$as_json" -eq 1 ]; then
  pgm_worklist_json "$report" "$target" "$tags_not_run" || exit 1
else
  pgm_worklist "$report" "$target" || exit 1
  [ -n "$detected_tags" ] && [ -z "$tags" ] &&
    printf '\n  NOTE tests behind build tags (%s) were not run, so some entries above may be false gaps.\n' "$detected_tags"
fi

exit 0
