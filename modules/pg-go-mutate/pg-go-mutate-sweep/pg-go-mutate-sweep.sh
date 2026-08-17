# shellcheck shell=bash
#
# pg-go-mutate-sweep: resumable unattended mutation sweep. Orchestration only --
# enumeration, ledger, lock and classification live in pg-go-mutate-sweep.bash.
#
# Exit: 0 completed or nothing left; 2 usage or a plan-time defect; 3 lock held;
# 4 fatal abort mid-sweep. A unit's RECORDED failure never changes the exit
# status -- that is the whole point of an unattended sweep.

show_help() {
  cat <<'HELP'
pg-go-mutate-sweep: analyse every Go package in the workspace, one unit at a time.

Usage: pg-go-mutate-sweep [OPTIONS]

A unit is one (project, package) pair. Because pg-go-mutate walks its target
RECURSIVELY, a unit whose directory contains further packages is a directory
SUBTREE rather than a single package, and is therefore larger; such units are
ordered last within their project.

Options:
  --root <dir>            Workspace root. Default: PN_WORKSPACE_ROOT, else cwd.
  --only <project>        Restrict the run list to one project (repeatable).
  --unit-timeout <sec>    Per-unit wall-clock cap. Default 3600.
  --unit-kill-grace <sec> Grace before escalating to KILL. Default 60.
  --mutant-timeout <sec>  Passed to pg-go-mutate --timeout. Default 60.
  --workers <n>           Passed to pg-go-mutate --workers. Default 2.
  --auto-tags <list>      Build tags eligible for automatic application.
                          Default: none. A tag-gated suite runs once per mutant.
  --retry <spec>          Re-attempt units by status, or 'transient' for the cohort.
  --redo <key>            Re-attempt one unit, keyed <project>#<package>.
  --dry-run               Print the plan and resume position. Runs nothing.
  --no-beads              Analyse and record; file no beads.
  --force-unlock          Break a lock whose holder is gone or wedged.
  -h, --help              Show this help message
  -v, --version           Show version information

State lives under ${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-sweep.
This is a diagnostic driver: it records unit STATUS only, never a score.
HELP
}

root="${PN_WORKSPACE_ROOT:-$PWD}"
unit_timeout=3600
kill_grace=60
mutant_timeout=60
workers=2
auto_tags=""
retry_spec=""
redo_key=""
dry_run=0
no_beads=0
force_unlock=0
only_projects=()

while [ $# -gt 0 ]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --root)
    root="${2:?--root needs a value}"
    shift 2
    ;;
  --only)
    only_projects+=("${2:?--only needs a value}")
    shift 2
    ;;
  --unit-timeout)
    unit_timeout="${2:?--unit-timeout needs a value}"
    shift 2
    ;;
  --unit-kill-grace)
    kill_grace="${2:?--unit-kill-grace needs a value}"
    shift 2
    ;;
  --mutant-timeout)
    mutant_timeout="${2:?--mutant-timeout needs a value}"
    shift 2
    ;;
  --workers)
    workers="${2:?--workers needs a value}"
    shift 2
    ;;
  --auto-tags)
    auto_tags="${2:?--auto-tags needs a value}"
    # Validated here because it is the only path by which an operator-supplied
    # tag reaches --tags, where pg-go-mutate interpolates it into GOFLAGS.
    case "$auto_tags" in
    '' | *[!A-Za-z0-9_,.]* | [!A-Za-z0-9_]*)
      printf 'pg-go-mutate-sweep: --auto-tags must match [A-Za-z0-9_][A-Za-z0-9_,.]*, got '\''%s'\''\n' "$auto_tags" >&2
      exit 2
      ;;
    esac
    shift 2
    ;;
  --retry)
    retry_spec="${2:?--retry needs a value}"
    shift 2
    ;;
  --redo)
    # shellcheck disable=SC2034  # consumed by the drive loop Task 9 adds to this file
    redo_key="${2:?--redo needs a value}"
    shift 2
    ;;
  --dry-run)
    dry_run=1
    shift
    ;;
  --no-beads)
    # shellcheck disable=SC2034  # consumed by the drive/bead-filing loop Task 9/10 add to this file
    no_beads=1
    shift
    ;;
  --force-unlock)
    force_unlock=1
    shift
    ;;
  --)
    shift
    break
    ;;
  *)
    printf 'pg-go-mutate-sweep: unknown option: %s\n' "$1" >&2
    exit 2
    ;;
  esac
done

for n in "$unit_timeout" "$kill_grace" "$mutant_timeout" "$workers"; do
  case "$n" in '' | *[!0-9]*)
    printf 'pg-go-mutate-sweep: timeouts and --workers must be positive integers\n' >&2
    exit 2
    ;;
  esac
  [ "$n" -ge 1 ] || {
    printf 'pg-go-mutate-sweep: timeouts and --workers must be >= 1\n' >&2
    exit 2
  }
done

[ -d "$root" ] || {
  printf 'pg-go-mutate-sweep: --root %s is not a directory\n' "$root" >&2
  exit 2
}
root="$(cd "$root" && pwd)"

# Preflight is command resolution ONLY. It deliberately does not verify the
# engine pin: PG_GO_MUTATE_GOMU{,_VERSION} are set inside pg-go-mutate's own
# wrapper and gomu is not on PATH, so a sweep-side check would resolve a bare
# gomu, fail always, and check the wrong binary. The pin is delegated to the
# first unit's exit 13.
for cmd in pg-go-mutate bd; do
  command -v "$cmd" >/dev/null 2>&1 || {
    printf 'pg-go-mutate-sweep: %s is required but was not found on PATH. Enable homeModules.pg-go-mutate and apply.\n' "$cmd" >&2
    exit 4
  }
done

[ "$force_unlock" -eq 1 ] && pgms_lock_release
pgms_lock_acquire || exit 3
# Released on EVERY path, not just the happy one: the lock is taken before the
# plan is built, so a slug collision or a fatal abort would otherwise leave it held.
trap 'pgms_lock_release' EXIT
trap 'pgms_lock_release; exit 130' INT
trap 'pgms_lock_release; exit 143' TERM HUP

pgms_check_slug_collisions "$root" || exit 2

if [ "$dry_run" -eq 1 ]; then
  printf 'pg-go-mutate-sweep: plan for %s\n\n' "$root"
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    st="$(pgms_unit_status "$key")"
    if pgms_unit_needs_run "$key" "$retry_spec"; then
      printf '  RUN   %s\n' "$key"
    else
      printf '  skip  %s (%s)\n' "$key" "$st"
    fi
  done < <(pgms_plan "$root")
  exit 0
fi
exit 0
