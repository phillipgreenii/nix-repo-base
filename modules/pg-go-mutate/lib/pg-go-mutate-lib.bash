# shellcheck shell=bash

# pg-go-mutate shared library: guards, engine invocation and report transform.
# Sourced by pg-go-mutate.sh and directly by bats.

# Engine resolution. NEVER a runtimeDeps entry: repo-base's bash builders append
# runtimeDeps with --suffix PATH, so an ambient ~/go/bin/gomu would win and
# defeat the pin. The home-manager module binds PG_GO_MUTATE_GOMU with
# makeWrapper --set (spec W9); this default exists for raw-source bats runs.
pgm_gomu_bin() {
  printf '%s\n' "${PG_GO_MUTATE_GOMU:-gomu}"
}

pgm_die() {
  printf 'pg-go-mutate: %s\n' "$1" >&2
  return 1
}

pgm_require_go() {
  command -v go >/dev/null 2>&1 && return 0
  pgm_die "the Go toolchain is required but 'go' is not on PATH. Enable the golang capability, or enter a devShell that provides it."
}

pgm_validate_flags() {
  local workers="$1" timeout="$2"
  case "$workers" in
  '' | *[!0-9]*)
    pgm_die "--workers must be a positive integer, got '$workers'"
    return 1
    ;;
  esac
  case "$timeout" in
  '' | *[!0-9]*)
    pgm_die "--timeout must be a positive integer, got '$timeout'"
    return 1
    ;;
  esac
  # --workers 0 makes gomu's semaphore unbuffered: every worker blocks forever
  # and it installs no signal handler, so the deadlock is only escapable by
  # SIGKILL. --timeout 0 marks every mutant TIMED_OUT.
  [ "$workers" -ge 1 ] || {
    pgm_die "--workers must be >= 1 (0 deadlocks gomu permanently)"
    return 1
  }
  [ "$timeout" -ge 1 ] || {
    pgm_die "--timeout must be >= 1 (0 times out every mutant)"
    return 1
  }
  return 0
}

pgm_has_tests() {
  local target="$1" counts
  # shellcheck disable=SC2164  # cd failure yields empty go-list output, which the -gt check below already treats as "no tests"
  counts="$(cd "$target" && go list -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>/dev/null | awk '{i+=$1; x+=$2} END {print i+x}')"
  [ -n "$counts" ] && [ "$counts" -gt 0 ] && return 0
  return 1
}

# The critical guard. `go build ./...` is NOT sufficient: it never compiles
# _test.go, and gomu classifies ANY non-zero `go test` exit as KILLED -- so a
# package whose tests fail to compile, or already fail, reports 100% with zero
# survivors and exits 0. That reads as "your tests are perfect" (spec 5.1).
pgm_tests_healthy() {
  local target="$1" out
  # shellcheck disable=SC2164  # cd failure surfaces as a go-vet error caught below, not a silent success
  if ! out="$(cd "$target" && go vet ./... 2>&1)"; then
    printf 'pg-go-mutate: the target does not vet cleanly on unmutated source:\n%s\n' "$out" >&2
    return 1
  fi
  # shellcheck disable=SC2164  # cd failure surfaces as a go-test error caught below, not a silent success
  if ! out="$(cd "$target" && go test -count=1 ./... 2>&1)"; then
    printf 'pg-go-mutate: the target'"'"'s tests do not pass on unmutated source, so mutation results would be meaningless (gomu reads any test failure as a killed mutant):\n%s\n' "$out" >&2
    return 1
  fi
  return 0
}

# Prints unsatisfied custom build tags found in the target's _test.go files.
# A naive //go:build scan is wrong: it fires on linux/darwin/cgo/go1.24, which
# the current build context already satisfies (spec W12). Satisfied files are
# visible to `go list` as TestGoFiles, so a tag is "custom and unsatisfied"
# exactly when it appears in a //go:build line of a file go list does NOT see.
pgm_detect_tags() {
  local target="$1" visible tags=() f tag
  # shellcheck disable=SC2164  # cd failure yields empty go-list output; every file is then treated as "not visible", which is the conservative direction
  visible="$(cd "$target" && go list -f '{{range .TestGoFiles}}{{.}}
{{end}}{{range .XTestGoFiles}}{{.}}
{{end}}' ./... 2>/dev/null)"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    printf '%s\n' "$visible" | grep -qxF "$(basename "$f")" && continue
    while read -r tag; do
      [ -n "$tag" ] && tags+=("$tag")
    done < <(sed -n 's|^//go:build ||p' "$f" | tr '&|()!' '\n' | tr -d ' ' | grep -v '^$')
  done < <(find "$target" -name '*_test.go' -type f 2>/dev/null)
  [ ${#tags[@]} -eq 0 ] && return 0
  printf '%s\n' "${tags[@]}" | sort -u | paste -sd, -
  return 0
}

# Cleanup for pgm_run_engine. Called EXPLICITLY on every return path (success,
# engine failure, missing report) rather than from an EXIT trap: the CLI (a
# later task) installs its own `trap ... EXIT` to remove the report file it
# harvests, and a second EXIT trap set here would simply REPLACE it rather
# than chain with it, leaking this private working directory on every single
# run. INT/TERM/HUP are different -- nothing downstream re-registers those --
# so they stay wired to this same function as an interrupt safety net while
# pgm_run_engine is still on the call stack; relies on bash's dynamic scoping,
# under which a function invoked from a trap (or directly) while a caller's
# locals are in scope can still see them.
_pgm_run_engine_cleanup() {
  rm -rf -- "$workdir" 2>/dev/null || true

  # Scope overlay-dir removal to THIS run: the name is
  # gomu_overlay_<pid>_<unixnano>, so a bare gomu_overlay_* glob would delete a
  # concurrent run's live working directories (spec CL3).
  rm -rf -- "${TMPDIR:-/tmp}"/gomu_overlay_"${gomu_pid:-}"_* 2>/dev/null || true

  # Remove only the main-package compile artifacts THIS run created. gomu's
  # compile precheck sets its own cmd.Dir to the mutated file's directory, so
  # for a `main` package it drops a binary named after that directory INTO THE
  # TARGET TREE itself, not our private workdir -- never delete one that
  # predates this run, that would be a developer's own build output.
  local i
  for i in "${!main_bins[@]}"; do
    if [ "${pre_existing[$i]}" = "0" ] && [ -e "${main_bins[$i]}" ]; then
      rm -f -- "${main_bins[$i]}" 2>/dev/null || true
    fi
  done
}

# Runs the engine in a PRIVATE cwd and prints the harvested report path.
#
# The private cwd is load-bearing, not hygiene. gomu writes both
# mutation-report.json and .gomu_history.json relative to CWD, not the target,
# and has no flag to relocate either -- so running from a repo root drops them
# at the repo root. A fresh cwd per run also guarantees an empty history:
# --incremental=false does NOT disable history skipping (the history is
# consulted unconditionally), and a stale history makes gomu skip files and,
# if all are skipped, return before writing any report at all (spec CL1/CL2).
pgm_run_engine() {
  # Named build_tags, not tags: shellcheck's SC2178/SC2128 conflate a same-
  # named local with the ARRAY `tags=()` declared in pgm_detect_tags above,
  # even though the two are unrelated locals in different functions.
  local target="$1" workers="$2" timeout="$3" build_tags="$4"
  local workdir gomu_pid rc report d bin
  local -a main_bins=() pre_existing=()
  workdir="$(mktemp -d)" || {
    pgm_die "could not create a private working directory"
    return 1
  }

  # Capture the expected compile-artifact path (spec: <dir>/<basename of dir>)
  # for every `main` package under the target, and whether it already exists,
  # BEFORE running the engine -- so cleanup afterward removes only what this
  # run created.
  # shellcheck disable=SC2164  # cd failure yields empty go-list output, so main_bins stays empty and cleanup below is a no-op
  while IFS= read -r d; do
    [ -n "$d" ] || continue
    bin="$d/$(basename "$d")"
    main_bins+=("$bin")
    if [ -e "$bin" ]; then
      pre_existing+=(1)
    else
      pre_existing+=(0)
    fi
  done < <(cd "$target" && go list -f '{{if eq .Name "main"}}{{.Dir}}{{end}}' ./... 2>/dev/null)

  # Interrupt safety net only -- see _pgm_run_engine_cleanup above for why
  # this is not also an EXIT trap. Disarmed before every return below.
  trap '_pgm_run_engine_cleanup' INT TERM HUP

  if [ -n "$build_tags" ]; then
    # APPEND, never clobber: gomu sets no cmd.Env, so its `go` subprocesses
    # inherit this and both `go build -overlay` and `go test -overlay` honour
    # it. This is a real fix for upstream issue #94 (spec W11).
    export GOFLAGS="-tags=$build_tags ${GOFLAGS:-}"
  fi

  # `gomu run`, never bare `gomu`: the root command runs the same function with
  # the run flags unregistered, so workers reads 0 and deadlocks (spec C5).
  (cd "$workdir" && "$(pgm_gomu_bin)" run \
    --incremental=false --fail-on-gate=false --output json \
    --workers "$workers" --timeout "$timeout" "$target" >"$workdir/engine.log" 2>&1) &
  gomu_pid=$!
  wait "$gomu_pid"
  rc=$?

  if [ ! -f "$workdir/mutation-report.json" ]; then
    printf 'pg-go-mutate: the engine produced no report (exit %s). Output:\n' "$rc" >&2
    cat "$workdir/engine.log" >&2
    _pgm_run_engine_cleanup
    trap - INT TERM HUP
    return 1
  fi

  # Harvest out of the private cwd BEFORE cleanup removes it. Guarded like the
  # workdir mktemp above: this script runs under the builder-injected
  # `set -euo pipefail`, so an unguarded failure here would errexit straight
  # out of this function -- skipping _pgm_run_engine_cleanup and leaking
  # $workdir, which the whole explicit-cleanup design (see the comment on
  # _pgm_run_engine_cleanup above) exists to prevent.
  report="$(mktemp -t pg-go-mutate-report.XXXXXX.json)" || {
    pgm_die "could not create a temp file to harvest the report"
    _pgm_run_engine_cleanup
    trap - INT TERM HUP
    return 1
  }
  cp "$workdir/mutation-report.json" "$report"

  _pgm_run_engine_cleanup
  trap - INT TERM HUP
  printf '%s\n' "$report"
  return 0
}

# Gates on the REPORT, never on the engine's exit code: gomu `continue`s past
# per-file generate and execute errors and still exits 0 (spec C6).
pgm_report_sane() {
  local report="$1"
  [ -f "$report" ] || {
    pgm_die "no report at $report"
    return 1
  }
  jq -e . "$report" >/dev/null 2>&1 || {
    pgm_die "the engine's report is not valid JSON"
    return 1
  }
  jq -e '.results != null' "$report" >/dev/null 2>&1 ||
    {
      pgm_die "the engine analyzed nothing (results is null)"
      return 1
    }
  jq -e '(.totalMutants // 0) > 0' "$report" >/dev/null 2>&1 ||
    {
      pgm_die "the engine generated no mutants for this target"
      return 1
    }
  if jq -e '((.statistics.notViable // 0) + (.statistics.errors // 0)) / .totalMutants > 0.5' "$report" >/dev/null 2>&1; then
    pgm_die "more than half the mutants failed to build -- the target does not build under mutation, so these results are meaningless"
    return 1
  fi
  if jq -e '(.statistics.survived // 0) == 0 and (.statistics.killed // 0) == .totalMutants' "$report" >/dev/null 2>&1; then
    pgm_die "suspect result: every mutant was killed and none survived. This is the signature of a test suite that was already failing, which the engine reads as a killed mutant. Re-check the target's tests."
    return 1
  fi
  return 0
}

# jq filter shared by both worklist renderers. Drops no-op mutants where
# original == mutated (53 of 63 return_zero_value mutants in the evidence sweep
# are '"" -> ""' or '0 -> 0', un-killable by any assertion -- spec O5), and
# relativizes gomu's absolute filePath (spec O4).
#
# Defined via a QUOTED heredoc -- no bash expansion at all -- and always
# spliced into a full jq program by plain string CONCATENATION below, never by
# retyping jq's own quoting/interpolation syntax inside a bash DOUBLE-quoted
# literal. That is the fragile form: jq's `"\(...)"` interpolation typed
# directly in bash source needs its quotes and backslashes escaped for bash's
# sake too, and the two escaping layers are easy to mismatch. An UNQUOTED
# heredoc is not a safe substitute either: bash would expand this filter's own
# `$target` (a jq variable bound via --arg) as a BASH variable while writing
# the heredoc, corrupting `ltrimstr($target)` before jq ever sees it.
_pgm_survivors_filter() {
  cat <<'JQ'
[ .results[]
  | select(.status == "SURVIVED")
  | select(.mutant.original != .mutant.mutated)
  | { file: (.mutant.filePath | ltrimstr($target) | ltrimstr("/")),
      line: .mutant.line,
      type: .mutant.type,
      description: .mutant.description } ]
JQ
}

pgm_worklist() {
  local report="$1" target="$2" program survivors n
  # Adjacent-literal concatenation: the double-quoted command substitution's
  # output is inserted verbatim (no re-escaping needed regardless of the
  # quotes/backslashes it contains), immediately followed by a single-quoted
  # literal in which jq's own `"..."` and `\(...)` need no bash escaping.
  program="$(_pgm_survivors_filter)"'
    | group_by(.file)[]
    | "\(.[0].file)", (.[] | "    L\(.line)   \(.description)   [\(.type)]"), ""'
  survivors="$(jq -r --arg target "$target" "$program" "$report")"
  n="$(jq -r --arg target "$target" "$(_pgm_survivors_filter) | length" "$report")"

  # First line carries no percentage and no killed count (spec O2).
  printf 'pg-go-mutate: %s surviving mutants in %s\n\n' "$n" "$target"
  printf '%s\n' "$survivors"
  printf 'Each surviving mutant is an assertion your tests do not make.\n\n'
  # All five buckets, or the summary will not sum (spec O7).
  jq -r '.statistics | "  killed \(.killed)  survived \(.survived)  not-viable \(.notViable)  timed-out \(.timedOut)  errors \(.errors)"' "$report"
}

pgm_worklist_json() {
  local report="$1" target="$2" program
  # No top-level `target` field: the brief's literal code included one
  # holding the raw ABSOLUTE target path, which leaks it into output the
  # brief's own test asserts is target-relative throughout (spec correction:
  # the interface contract promises target-relative paths, never a raw
  # absolute-target field, and the caller already has the target it passed
  # in). $target is still bound for the filter's own ltrimstr($target) use.
  program='{ survivors: ('"$(_pgm_survivors_filter)"'), statistics: .statistics }'
  jq --arg target "$target" "$program" "$report"
}
