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

  PATH   A directory (walked RECURSIVELY, so nested packages are included) or
         a single .go file. Defaults to the current directory.
         Go package patterns such as ./... are NOT accepted.

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
    tags="${2:?--tags needs a value}"
    shift 2
    ;;
  --workers)
    workers="${2:?--workers needs a value}"
    shift 2
    ;;
  --timeout)
    timeout="${2:?--timeout needs a value}"
    shift 2
    ;;
  --)
    shift
    break
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
  printf 'pg-go-mutate: Go package patterns such as ./... are not accepted; the engine errors on them. Pass a directory (it is walked recursively) or a single .go file.\n' >&2
  exit 2
  ;;
esac

[ -d "$target" ] || [ -f "$target" ] || {
  printf 'pg-go-mutate: %s is neither a directory nor a file\n' "$target" >&2
  exit 2
}
# shellcheck disable=SC2164  # cd failure here would mean $target vanished between the -d/-f check above and now; the guaranteed-existing dirname is safe to cd into
target="$(cd "$(dirname "$target")" && pwd)/$(basename "$target")"
# shellcheck disable=SC2164  # cd failure here would mean $target (already confirmed a directory above) vanished in the interim; nothing safer to fall back to
[ -d "$target" ] && target="$(cd "$target" && pwd)"

pgm_validate_flags "$workers" "$timeout" || exit 2
pgm_require_go || exit 1

pgm_has_tests "$target" || {
  printf 'pg-go-mutate: %s has no test files. Write a test first — mutation testing reports missing ASSERTIONS, and with no tests every mutant trivially survives.\n' "$target" >&2
  exit 1
}

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

report="$(pgm_run_engine "$target" "$workers" "$timeout" "$tags")" || exit 1
trap 'rm -f -- "$report"' EXIT

pgm_report_sane "$report" || exit 1

if [ "$as_json" -eq 1 ]; then
  pgm_worklist_json "$report" "$target"
else
  pgm_worklist "$report" "$target"
  [ -n "$detected_tags" ] && [ -z "$tags" ] &&
    printf '\n  NOTE tests behind build tags (%s) were not run, so some entries above may be false gaps.\n' "$detected_tags"
fi

exit 0
