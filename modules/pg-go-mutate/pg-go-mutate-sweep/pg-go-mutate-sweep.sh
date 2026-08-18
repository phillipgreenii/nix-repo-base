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
    redo_key="${2:?--redo needs a value}"
    shift 2
    ;;
  --dry-run)
    dry_run=1
    shift
    ;;
  --no-beads)
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

runs_dir="$(pgms_runs_dir)"
inconclusive_threshold=50

_in_scope() { # <project>
  local p
  [ "${#only_projects[@]}" -eq 0 ] && return 0
  for p in "${only_projects[@]}"; do [ "$p" = "$1" ] && return 0; done
  return 1
}

_bead_title() { printf 'go-test-gaps triage: %s\n' "$1"; }

# The withheld tags of a unit's LAST record, read from the LEDGER rather than
# from the drive loop's variables: a project's bead is filed by replay and may
# summarise units recorded by an earlier, crashed invocation that this process
# never ran.
_pgms_withheld_for() { # <unit-key>
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || {
    printf 'none'
    return 0
  }
  pgms_valid_lines <"$ledger" |
    jq -rs --arg u "$1" 'map(select(.kind=="unit" and .unit==$u)) | if length==0 then "none"
                          else (last.tags_withheld // "") | if . == "" then "none" else . end end'
}

# The body carries the protocol because this bead will be handed to a session
# with none of this context. It carries a tally of UNITS by status -- never a
# mutant count (N1).
_bead_body() { # <project>
  local proj="$1" u key st
  printf 'pg-go-mutate has analysed every package in `%s`. The worklists are on this machine at:\n\n' "$proj"
  printf '    %s/%s/\n\n' "$(pgms_runs_dir)" "$(pgms_slug "$proj")"
  printf 'One JSON file per unit, overwritten in place on each attempt. `.survivors` is the\n'
  printf 'actionable worklist: each entry names a file, a line and the mutation operator.\n\n'
  printf 'Units by status:\n\n'
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    key="$(pgms_unit_key "$proj" "$u")"
    st="$(pgms_unit_status "$key")"
    printf '  - %s: %s (withheld tags: %s)\n' "$u" "${st:-unrun}" "$(_pgms_withheld_for "$key")"
  done < <(pgms_find_units "$root" "$proj")
  cat <<'PROTOCOL'

## How to turn this into fix beads

1. Check `tags_withheld` FIRST, before reading any survivor. If it is non-empty that
   unit's survivors are UNANALYSED and MUST NOT be filed as gaps until re-run with
   `--auto-tags` widened deliberately -- whatever the unit's status. The status is not a
   reliable warning: the common shape is partially-gated tests over visible source, which
   records `done` and looks exactly like a genuine gap.
2. Start with error paths. Across a sixteen-module measurement the go-test-gaps skill
   records `err != nil` mutated to `false` surviving 70 times, and `error_nilify`
   surviving 44 of 48 completed cases.
3. Prefer DUPLICATED unasserted code. The two highest-value findings of the manual
   campaign were extraction opportunities, not missing tests: a 64KB-overflow scanner
   buffer duplicated at six sites in claude-transcript where no test read a line over 64KB
   (pg2-j54i7), and a newFileLogger duplicated in two support-apps binaries with zero test
   references (pg2-70l4r). One test against one extracted helper kills mutants everywhere.
4. Cite file:line:operator concretely. "Add more tests" is not actionable.
5. Deprioritise explicitly. The `==` to `<=`/`>=` family on STRING equality is a weak
   mutant -- killing it needs an input differing only in lexicographic order. Record the
   judgement instead of chasing it.
6. Verify per mutant on file:line:type. Survivor totals move by a mutant or two between
   runs on identical source, so a count dropping by one is indistinguishable from noise.
7. Record NO scores anywhere -- not in a file, not in a bead, not in a commit message.

Close this bead once focused fix beads exist for the worthwhile clusters.
PROTOCOL
}

# Predicate 2 is evaluated here, by replay, for EVERY project -- including ones
# --only excluded, because an earlier crashed run may have completed them.
_file_due_beads() {
  local proj action id body rc
  [ "$no_beads" -eq 1 ] && return 0
  while IFS= read -r proj; do
    [ -n "$proj" ] || continue
    pgms_bead_due "$root" "$proj" || continue
    action="$(pgms_bead_action "$root" "$proj")"
    body="$(mktemp)"
    _bead_body "$proj" >"$body"
    rc=0
    if [ "$action" = "amend" ]; then
      id="$(_pgms_latest_bead "$proj" | jq -r '.bead')"
      bd comment "$id" --file "$body" >/dev/null 2>&1 || rc=$?
      [ "$rc" -eq 0 ] && pgms_append_record "$(jq -nc --arg p "$proj" --arg b "$id" \
        --arg t "$(date -Iseconds)" '{kind:"bead",project:$p,bead:$b,action:"amended",finished:$t}')"
    else
      id="$(bd create "$(_bead_title "$proj")" --type task --priority 3 \
        --labels "go-test-gaps,$(printf '%s' "$proj" | cut -d/ -f1)" \
        --body-file "$body" --silent 2>/dev/null)" || rc=$?
      if [ "$rc" -eq 0 ] && [ -n "$id" ]; then
        pgms_append_record "$(jq -nc --arg p "$proj" --arg b "$id" \
          --arg t "$(date -Iseconds)" '{kind:"bead",project:$p,bead:$b,action:"filed",finished:$t}')"
      fi
    fi
    # A bead-filing failure is logged and does NOT abort the sweep; with no bead
    # record appended, the next run retries it.
    [ "$rc" -ne 0 ] && printf 'pg-go-mutate-sweep: filing the bead for %s failed; will retry next run\n' "$proj" >&2
    rm -f "$body"
  done < <(pgms_find_projects "$root")
}

_file_due_beads

while IFS= read -r key; do
  [ -n "$key" ] || continue
  proj="$(pgms_unit_project "$key")"
  pkg="$(pgms_unit_pkg "$key")"
  _in_scope "$proj" || continue
  if [ -n "$redo_key" ]; then
    [ "$key" = "$redo_key" ] || continue
  else
    pgms_unit_needs_run "$key" "$retry_spec" || continue
  fi

  dir="$root/$proj/$pkg"
  report="$runs_dir/$(pgms_slug "$proj")/$(pgms_slug "$pkg").json"
  mkdir -p "$(dirname "$report")"

  # Reset per unit. Without this the vanished arm below -- which never invokes
  # the engine and so never assigns them -- would record the PREVIOUS unit's
  # exit code and tag fields.
  rc=0
  tags_applied=""
  tags_withheld=""

  # Re-stat immediately before invoking: over a multi-hour unattended sweep in a
  # live workspace one branch switch can remove a package directory.
  if [ ! -d "$dir" ]; then
    unit_status=vanished
    : >"$report"
  else
    tags_pair="$(pgms_apply_tags "$dir" "$auto_tags")"
    tags_applied="${tags_pair%%$'\t'*}"
    tags_withheld="${tags_pair#*$'\t'}"

    args=(--json --workers "$workers" --timeout "$mutant_timeout")
    [ -n "$tags_applied" ] && args+=(--tags "$tags_applied")

    printf 'pg-go-mutate-sweep: %s\n' "$key" >&2
    # No --foreground: that mode exists so a command can read the TTY and in it
    # children of COMMAND are NOT timed out, which is the opposite of the subtree
    # kill wanted here. Default mode signals the child's process group.
    timeout --kill-after="$kill_grace" "$unit_timeout" \
      pg-go-mutate "${args[@]}" "$dir" >"$report" 2>/dev/null || rc=$?
    unit_status="$(pgms_classify "$rc" "$report" "$inconclusive_threshold")"
    if [ "$unit_status" = "fatal" ]; then
      printf 'pg-go-mutate-sweep: %s exited %s -- aborting the sweep rather than recording %s identical failures\n' \
        "$key" "$rc" "$(pgms_plan "$root" | grep -c . || true)" >&2
      exit 4
    fi
  fi

  # The record is appended only AFTER the report is written, so a unit is never
  # marked complete with no artifact behind it.
  pgms_append_record "$(jq -nc \
    --arg u "$key" --arg p "$proj" --arg k "$pkg" --arg s "$unit_status" \
    --arg e "$rc" --arg ta "$tags_applied" --arg tw "$tags_withheld" \
    --arg t "$(date -Iseconds)" --arg r "${report#"$(pgms_state_root)"/}" \
    '{kind:"unit",unit:$u,project:$p,pkg:$k,status:$s,exit:($e|tonumber),
      tags_applied:$ta,tags_withheld:$tw,finished:$t,report:$r}')"

  _file_due_beads
done < <(pgms_plan "$root")

exit 0
