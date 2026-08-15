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

# Asserts the engine EXISTS and is the pinned build (spec E1). Both halves are
# load-bearing:
#
#   * absence: without this, a missing engine surfaces only as "the engine
#     produced no report (exit 127)" after every guard has already run, instead
#     of the named "gomu absent" operational failure spec C3 requires.
#   * version: every empirical number behind this tool's design was produced by
#     a `gomu version dev` binary -- a `go install` build whose release ldflags
#     never ran -- so those measurements are unattributable. E1 exists so that
#     can never recur silently. It matters most for a consumer who installs
#     pkgs.pg-go-mutate DIRECTLY rather than importing homeModules.pg-go-mutate:
#     that path never sets PG_GO_MUTATE_GOMU, so the store-path binding (W9) is
#     bypassed and whatever `gomu` is on PATH is used.
#
# The expectation comes from the one place that knows it -- the home-manager
# module, which sets PG_GO_MUTATE_GOMU_VERSION from the pinned package's
# `version` beside the PG_GO_MUTATE_GOMU binding. When the var is UNSET the
# comparison is skipped, because a raw-source run and the bats suites have no
# pinned version to compare against (they stub the engine entirely).
pgm_require_engine() {
  local bin raw first expected field
  local -a fields
  bin="$(pgm_gomu_bin)"
  command -v "$bin" >/dev/null 2>&1 || {
    pgm_die "the mutation engine is required but '$bin' was not found. Install the tool via homeModules.pg-go-mutate (which binds the pinned engine by store path), or point PG_GO_MUTATE_GOMU at a gomu binary."
    return 1
  }

  expected="${PG_GO_MUTATE_GOMU_VERSION:-}"
  [ -n "$expected" ] || return 0

  # Not `| head -1`: under the injected `pipefail` a SIGPIPE'd engine would fail
  # the pipeline, so the first line is taken with parameter expansion instead.
  raw="$("$bin" version 2>&1)" || {
    pgm_die "the mutation engine at $bin does not answer 'gomu version', so the pinned version (${expected}) cannot be verified. Output: ${raw}"
    return 1
  }
  # Compared field-by-field rather than by substring: a substring test would
  # accept 0.2.10 for an expected 0.2.1. A leading `v` is tolerated because the
  # overlay strips it from the tag while `gomu version` may or may not print it.
  first="${raw%%$'\n'*}"
  read -r -a fields <<<"$first"
  for field in "${fields[@]}"; do
    [ "${field#v}" = "$expected" ] && return 0
  done
  pgm_die "engine version mismatch: expected gomu ${expected} (the pinned build), but ${bin} reports '${first}'. A 'dev' build has no release ldflags, so its results are unattributable (spec E1). Unset PG_GO_MUTATE_GOMU to use the pinned engine, or PG_GO_MUTATE_GOMU_VERSION to skip this check deliberately."
  return 1
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

# Three-valued on purpose: 0 = tests exist, 1 = a loadable module with zero test
# files, 2 = `go list` could not enumerate the target at all (not a Go module, a
# module that fails to load, or an unreadable directory). Collapsing 2 into 1
# made every one of those report "has no test files. Write a test first", which
# sends the reader off to write a test that is not the problem.
pgm_has_tests() {
  local target="$1" listing counts
  # shellcheck disable=SC2164  # cd failure makes go list fail, which is reported as the enumeration failure (2) below rather than silently read as "no tests"
  if ! listing="$(cd "$target" && go list -f '{{len .TestGoFiles}} {{len .XTestGoFiles}}' ./... 2>&1)"; then
    printf 'pg-go-mutate: could not enumerate Go packages under %s -- it is not a Go module, or the module does not load:\n%s\n' "$target" "$listing" >&2
    return 2
  fi
  counts="$(printf '%s\n' "$listing" | awk '{i+=$1; x+=$2} END {print i+x}')"
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
#
# Visibility is matched on the FULL PATH, never the basename: with basenames, a
# tag-gated foo_test.go in one package counted as satisfied whenever ANY other
# package happened to have a foo_test.go, so its tags went unreported. `go list`
# prints .Dir from its own cwd (verified: it honours $PWD rather than resolving
# symlinks), so prefixing .Dir yields exactly the paths `find "$target"` emits.
# The walk is pruned to match what `go list ./...` itself ignores.
pgm_detect_tags() {
  local target="$1" visible tags=() f tag
  # shellcheck disable=SC2164  # cd failure yields empty go-list output; every file is then treated as "not visible", which is the conservative direction
  # shellcheck disable=SC2016  # $d is a GO TEMPLATE variable consumed by `go list -f`; single quotes are required so bash does not expand it first
  visible="$(cd "$target" && go list -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' ./... 2>/dev/null)"
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    printf '%s\n' "$visible" | grep -qxF "$f" && continue
    while read -r tag; do
      [ -n "$tag" ] && tags+=("$tag")
    done < <(sed -n 's|^//go:build ||p' "$f" | tr '&|()!' '\n' | tr -d ' ' | grep -v '^$')
  done < <(find "$target" -name '*_test.go' -type f -not -path '*/testdata/*' -not -path '*/vendor/*' 2>/dev/null)
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
  # Kill the engine FIRST. Without this, a TERM/HUP arriving mid-run ran the
  # rest of this function and returned while gomu carried on working against a
  # workdir that had just been deleted -- observed on this branch as a TERM'd
  # wrapper leaving live gomu/go-build children behind. `kill -0` keeps this a
  # no-op on the normal path, where `wait` has already reaped the engine.
  if [ -n "${gomu_pid:-}" ] && kill -0 "$gomu_pid" 2>/dev/null; then
    kill -TERM "$gomu_pid" 2>/dev/null || true
    wait "$gomu_pid" 2>/dev/null || true
  fi

  # Removing $workdir is also what removes this run's overlay dirs: the engine
  # is launched with TMPDIR="$workdir" (see pgm_run_engine), so gomu's
  # gomu_overlay_<pid>_<unixnano> dirs are created INSIDE it and die with it.
  # That satisfies spec CL3 by construction rather than by pid arithmetic.
  rm -rf -- "$workdir" 2>/dev/null || true

  # Second line of defence, for an engine that ignores TMPDIR: scoped to THIS
  # run's pid, because a bare gomu_overlay_* glob would delete a concurrent
  # run's live working directories (spec CL3). $gomu_pid is the ENGINE's pid --
  # the subshell execs it, so `$!` is not a shell wrapper (see pgm_run_engine).
  if [ -n "${gomu_pid:-}" ]; then
    rm -rf -- "${TMPDIR:-/tmp}"/gomu_overlay_"${gomu_pid}"_* 2>/dev/null || true
  fi

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

# Runs the engine in a PRIVATE cwd. On success the harvested report path is BOTH
# printed on stdout AND assigned to the global PGM_REPORT_PATH.
#
# The global is not redundant. A caller that reads the path the obvious way --
# `report="$(pgm_run_engine …)"` -- puts this whole function inside a COMMAND
# SUBSTITUTION SUBSHELL, and that subshell is what owns $workdir, $gomu_pid and
# the INT/TERM/HUP trap below. A signal sent to the CLI's own pid then never
# reaches it: the main shell dies, the subshell is orphaned, and the engine keeps
# running against a workdir nobody will remove. That is not hypothetical -- it is
# precisely how the leak observed on this branch happened (a `kill` on the
# top-level wrapper). Callers that need the interrupt guarantee MUST therefore
# call this function WITHOUT a command substitution and read PGM_REPORT_PATH, so
# the traps live in the process a user or agent actually signals.
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
  local workdir gomu_pid rc report d bin gomu_bin
  local -a main_bins=() pre_existing=()
  gomu_bin="$(pgm_gomu_bin)"
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
    #
    # IDEMPOTENT: the CLI exports the same thing BEFORE running the §5.1 guards,
    # because tag-gated tests are invisible to `go list`/`go vet`/`go test`
    # without it -- so the guards would either false-abort with "has no test
    # files" or, far worse, pass without ever seeing tests that do not compile
    # while the engine measures them and reports every mutant killed. This
    # branch therefore has to tolerate the flag already being present rather
    # than stacking a second copy.
    case " ${GOFLAGS:-} " in
    *" -tags=$build_tags "*) : ;;
    *) export GOFLAGS="-tags=$build_tags ${GOFLAGS:-}" ;;
    esac
  fi

  # `gomu run`, never bare `gomu`: the root command runs the same function with
  # the run flags unregistered, so workers reads 0 and deadlocks (spec C5).
  #
  # Two details in this launch are load-bearing:
  #
  #   * `exec` -- bash does NOT exec the last command of a compound, so without
  #     it `$!` is the background SUBSHELL's pid, not the engine's. Every
  #     pid-scoped cleanup keyed on that value could therefore never match, and
  #     the engine could not be signalled either.
  #   * TMPDIR="$workdir" -- gomu creates its gomu_overlay_<pid>_<unixnano>
  #     working dirs under TMPDIR and reaps them only on its NORMAL exit path,
  #     leaking on signal, on error return, and on its zero-files early return.
  #     Pointing TMPDIR at the private workdir makes those dirs children of a
  #     directory this function always removes, so spec CL3 holds by
  #     construction instead of by pid arithmetic.
  (
    cd "$workdir" || exit 127
    export TMPDIR="$workdir"
    exec "$gomu_bin" run \
      --incremental=false --fail-on-gate=false --output json \
      --workers "$workers" --timeout "$timeout" "$target" >"$workdir/engine.log" 2>&1
  ) &
  gomu_pid=$!
  # `|| rc=$?`, not a bare `wait` followed by `rc=$?`: under the injected
  # errexit a bare `wait` returning non-zero exits the function outright,
  # skipping the cleanup below -- and it returns 128+signum precisely when an
  # INT/TERM/HUP trap interrupted it, which is the interrupted-run path the
  # cleanup exists for.
  rc=0
  wait "$gomu_pid" || rc=$?

  if [ ! -f "$workdir/mutation-report.json" ]; then
    printf 'pg-go-mutate: the engine produced no report (exit %s). Output:\n' "$rc" >&2
    # `|| true`: on the interrupted path the trap has already removed $workdir,
    # so there is no log to show and an errexit here would skip the cleanup.
    cat "$workdir/engine.log" >&2 2>/dev/null || true
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
  # Guarded for exactly the reason the mktemp above is: an unguarded failure
  # here errexits straight out of the function, skipping the cleanup and leaking
  # $workdir -- the failure mode the explicit-cleanup design exists to prevent.
  cp "$workdir/mutation-report.json" "$report" || {
    pgm_die "could not harvest the engine's report out of the private working directory"
    rm -f -- "$report" 2>/dev/null || true
    _pgm_run_engine_cleanup
    trap - INT TERM HUP
    return 1
  }

  _pgm_run_engine_cleanup
  trap - INT TERM HUP
  # Deliberately NOT `local`: this is the half of the contract a caller can read
  # without wrapping the call in a command substitution (see the header above).
  # shellcheck disable=SC2034  # read by the CONSUMER (pg-go-mutate.sh), which is a separate file shellcheck does not see from here; not exported, so it does not leak into the engine's environment
  PGM_REPORT_PATH="$report"
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
  | { file: (.mutant.filePath | ltrimstr($target) | ltrimstr($target_real) | ltrimstr("/")),
      line: .mutant.line,
      type: .mutant.type,
      description: .mutant.description } ]
JQ
}

# Resolves the target's symlink-free form. ltrimstr is a plain prefix strip, so
# stripping only the LOGICAL target silently ships absolute paths whenever the
# engine reports the resolved one instead -- on darwin /var/folders/... and
# /private/var/folders/... are the same directory under two names, and $TMPDIR
# is the first. Both forms are stripped; pgm_assert_relative then proves it
# worked rather than trusting it.
_pgm_target_real() {
  local target="$1" resolved
  # shellcheck disable=SC2164  # a target that cannot be entered (e.g. a synthetic path in a unit test) has no resolved form; falling back to the logical one keeps the second ltrimstr a harmless no-op
  resolved="$(cd "$target" 2>/dev/null && pwd -P)" || resolved="$target"
  printf '%s\n' "$resolved"
}

# Fails the run rather than shipping a path the relativization did not actually
# handle. Checked on the RAW filePath, before stripping, and NOT by looking for a
# leading "/" in the result: the filter ends in ltrimstr("/"), so an absolute
# path that matched NEITHER target form still comes out looking relative --
# /somewhere/else/a.go becomes "somewhere/else/a.go", a plausible-looking path
# that points nowhere. The invariant that actually holds is that every absolute
# filePath is under the target in one of its two forms (spec O1/O4).
pgm_assert_relative() {
  local report="$1" target="$2" target_real="$3" unrelativizable
  unrelativizable="$(jq -r --arg target "$target" --arg target_real "$target_real" '
    [ .results[]
      | select(.status == "SURVIVED")
      | select(.mutant.original != .mutant.mutated)
      | .mutant.filePath
      | select(startswith("/"))
      | select((startswith($target) or startswith($target_real)) | not) ] | length' "$report")"
  [ "$unrelativizable" = "0" ] && return 0
  pgm_die "internal error: $unrelativizable survivor path(s) are absolute but not under $target, so they cannot be made target-relative and the worklist would be wrong"
  return 1
}

pgm_worklist() {
  local report="$1" target="$2" target_real program survivors n
  target_real="$(_pgm_target_real "$target")"
  pgm_assert_relative "$report" "$target" "$target_real" || return 1
  # Adjacent-literal concatenation: the double-quoted command substitution's
  # output is inserted verbatim (no re-escaping needed regardless of the
  # quotes/backslashes it contains), immediately followed by a single-quoted
  # literal in which jq's own `"..."` and `\(...)` need no bash escaping.
  program="$(_pgm_survivors_filter)"'
    | group_by(.file)[]
    | "\(.[0].file)", (.[] | "    L\(.line)   \(.description)   [\(.type)]"), ""'
  survivors="$(jq -r --arg target "$target" --arg target_real "$target_real" "$program" "$report")"
  n="$(jq -r --arg target "$target" --arg target_real "$target_real" "$(_pgm_survivors_filter) | length" "$report")"

  # First line carries no percentage and no killed count (spec O2).
  printf 'pg-go-mutate: %s surviving mutants in %s\n\n' "$n" "$target"
  printf '%s\n' "$survivors"
  printf 'Each surviving mutant is an assertion your tests do not make.\n\n'
  # All five buckets, or the summary will not sum to totalMutants (spec O7) --
  # so the raw `survived` bucket stays, ANNOTATED with the split between the
  # actionable survivors the worklist above lists and the no-op mutants spec O5
  # drops (original == mutated, un-killable by any assertion). Reporting the raw
  # bucket alone made the first line and the summary disagree with no
  # explanation, which is the one thing a diagnostic's output must never do.
  jq -r --arg actionable "$n" '.statistics
    | "  killed \(.killed)  survived \(.survived) (\($actionable) actionable, \((.survived // 0) - ($actionable | tonumber)) no-op)  not-viable \(.notViable)  timed-out \(.timedOut)  errors \(.errors)"' "$report"
}

pgm_worklist_json() {
  local report="$1" target="$2" build_tags_not_run="${3:-}" target_real program
  target_real="$(_pgm_target_real "$target")"
  pgm_assert_relative "$report" "$target" "$target_real" || return 1
  # No top-level `target` field: the brief's literal code included one
  # holding the raw ABSOLUTE target path, which leaks it into output the
  # brief's own test asserts is target-relative throughout (spec correction:
  # the interface contract promises target-relative paths, never a raw
  # absolute-target field, and the caller already has the target it passed
  # in). $target is still bound for the filter's own ltrimstr($target) use.
  #
  # Three deliberate shapes here:
  #   * survivedActionable / survivedNoOp -- spec O5 requires no-ops dropped
  #     before ANY count, and a consumer given only the raw bucket cannot tell
  #     why it exceeds the survivor array's length.
  #   * mutationScore is DELETED. It is not the score the design refuses to
  #     track over time (spec N1/N2), but passing it through hands a CI author
  #     the exact number to threshold on, which is the same outcome.
  #   * buildTagsNotRun -- the human renderer emits this caveat and the JSON one
  #     did not, so a machine consumer could not tell that some entries may be
  #     false gaps.
  # shellcheck disable=SC2016  # $survivors and $build_tags_not_run are JQ variables (one bound by `as`, one by --arg); bash must not expand them
  program='('"$(_pgm_survivors_filter)"') as $survivors
    | { survivors: $survivors,
        statistics: ((.statistics | del(.mutationScore))
                     + { survivedActionable: ($survivors | length),
                         survivedNoOp: ((.statistics.survived // 0) - ($survivors | length)) }),
        buildTagsNotRun: (if $build_tags_not_run == "" then null else $build_tags_not_run end) }'
  jq --arg target "$target" --arg target_real "$target_real" \
    --arg build_tags_not_run "$build_tags_not_run" "$program" "$report"
}
