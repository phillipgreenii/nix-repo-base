# shellcheck shell=bash

# pg-go-mutate: report which assertions a Go package's tests are missing, by
# wrapping the gomu mutation-testing engine. Orchestration only -- every
# pgm_* function it calls lives in pg-go-mutate-lib.bash (prepended by the
# builder). Exits 0 whenever it completed an analysis, however many mutants
# survived (spec: this is a diagnostic, not a gate) -- non-zero is reserved
# for operational failure (a guard failing, gomu/go absent, invalid flags, an
# unreadable target, or a missing/insane report).

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
  --workers <n>     Parallel workers. Default 2.
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
workers=2
timeout=60
tags=""
as_json=0

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

# Directories only. A single .go file was documented but has never worked: every
# guard runs `cd "$target"`, so for a file target the cd fails, `go list` never
# runs, and the command aborts with "has no test files. Write a test first" —
# which sends an agent off to write a test that already exists. Rejecting it
# outright is the honest behaviour until the mode is actually implemented.
[ -d "$target" ] || {
  printf 'pg-go-mutate: %s is not a directory. Pass a directory (it is walked recursively, so a single package works); single-file targets are not supported.\n' "$target" >&2
  exit 2
}
# shellcheck disable=SC2164  # cd failure here would mean $target (already confirmed a directory above) vanished in the interim; nothing safer to fall back to
target="$(cd "$target" && pwd)"

pgm_validate_flags "$workers" "$timeout" || exit 2
pgm_require_go || exit 1
# The engine must exist AND be the pinned build (spec E1). Checked here rather
# than discovered as "the engine produced no report (exit 127)" after every
# guard has already spent its time.
pgm_require_engine || exit 1

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

has_tests_rc=0
pgm_has_tests "$target" || has_tests_rc=$?
case "$has_tests_rc" in
0) ;;
2)
  # pgm_has_tests already reported the enumeration failure in detail.
  exit 1
  ;;
*)
  printf 'pg-go-mutate: %s has no test files. Write a test first — mutation testing reports missing ASSERTIONS, and with no tests every mutant trivially survives.\n' "$target" >&2
  exit 1
  ;;
esac

pgm_tests_healthy "$target" || exit 1

detected_tags="$(pgm_detect_tags "$target")"
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

# NOT `report="$(pgm_run_engine …)"`. A command substitution would run the engine
# supervisor in a SUBSHELL, and that subshell would own the private workdir, the
# engine's pid and the INT/TERM/HUP trap that cleans both up — so a `kill` on THIS
# script's pid would kill this shell and orphan the engine, leaking the workdir.
# That is the exact failure observed on this branch. Called plainly instead, with
# the path read back from PGM_REPORT_PATH, the traps live in the process a user or
# agent actually signals.
pgm_run_engine "$target" "$workers" "$timeout" "$tags" >/dev/null || exit 1
report="${PGM_REPORT_PATH:-}"
[ -n "$report" ] || {
  printf 'pg-go-mutate: the engine step reported success but produced no report path\n' >&2
  exit 1
}
# All four signals, not EXIT alone (spec CL4): an untrapped TERM/HUP never ran
# the EXIT trap, so the harvested report file leaked. The signal handlers exit
# rather than fall through, because a handler that merely returns resumes the
# pipeline it interrupted and would print a worklist for a run the user killed.
trap 'rm -f -- "$report"' EXIT
trap 'rm -f -- "$report"; exit 130' INT
trap 'rm -f -- "$report"; exit 143' TERM HUP

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
