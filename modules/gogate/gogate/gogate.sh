# shellcheck shell=bash
# gogate: sequential Go validation gate (gofmt -l, go build, go vet, go test).
# Stops at the first failing stage and prints only a fixed-size tail of that
# stage's combined output, then exactly one machine-readable verdict line.
#
# Rationale (bead pg2-i1hwk, pg-ccaudit audit 2026-08-15..08-31): 3,667
# Go-validation Bash calls across 136 sessions, ~27/session, differing almost
# entirely in the truncation choice (`head -100`, `tail -60`, ...). This
# fixes the truncation and the stage ordering so agents stop re-deciding both
# on every call.
#
# No separate .bash library: the logic is small enough to stay inline, and
# nothing else in this module needs to share it (unlike pg-go-mutate/pnwf,
# which split out a lib consumed by a second command).

TAIL_LINES=60

show_help() {
  cat <<'HELP'
gogate: sequential Go validation gate (fmt, build, vet, test).

USAGE
  gogate [--pkg <pattern>] [--quick] [-- <go test flags>...]

Runs, in order, STOPPING at the first failure:
  1. gofmt -l   Checks formatting of every .go file under the working
                directory, recursively. Independent of --pkg: gofmt takes
                files/directories, not Go package patterns.
  2. go build <pattern>
  3. go vet   <pattern>
  4. go test  <pattern> [passthrough flags]   (skipped by --quick)

On failure, prints only the LAST 60 lines of the failing stage's combined
stdout+stderr, then exactly one machine-readable line:
  GOGATE: FAIL(<stage>)     <stage> is one of: fmt, build, vet, test
On success (every stage ran and passed), prints exactly:
  GOGATE: PASS

OPTIONS
  --pkg <pattern>   Go package pattern passed to build/vet/test. Default:
                     ./...
  --quick           Skip the go test stage (run fmt/build/vet only). Any
                     passthrough flags after -- are ignored in this mode.
  -h, --help        Show this help message.

PASSTHROUGH
  Everything after a literal -- is appended verbatim to the `go test`
  invocation, so:
    gogate -- -run TestFoo -count=1
  runs exactly:
    go test ./... -run TestFoo -count=1
  This is the sanctioned way to target a subset of tests through this gate —
  running a hand-rolled `go test -run ...` directly bypasses the gate
  entirely, which is the failure mode this tool exists to close off.

EXIT CODES
  0    GOGATE: PASS
  1    Generic/unexpected error.
  2    Usage error (bad flags).
  10   GOGATE: FAIL(fmt)
  11   GOGATE: FAIL(build)
  12   GOGATE: FAIL(vet)
  13   GOGATE: FAIL(test)
HELP
}

PKG="./..."
QUICK=0
GO_TEST_ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    show_help
    exit 0
    ;;
  --pkg)
    [[ $# -ge 2 ]] || {
      echo "gogate: --pkg needs a value" >&2
      exit 2
    }
    PKG="$2"
    shift 2
    ;;
  --quick)
    QUICK=1
    shift
    ;;
  --)
    shift
    GO_TEST_ARGS=("$@")
    break
    ;;
  --*)
    echo "gogate: unknown option: $1" >&2
    exit 2
    ;;
  *)
    echo "gogate: unexpected argument: $1 (the package pattern is set with --pkg, not positionally)" >&2
    exit 2
    ;;
  esac
done

# Runs one gate stage ($3..: the argv). $1 is the stage's short name (used in
# the FAIL(<stage>) line), $2 its exit code. Captures combined stdout+stderr;
# on a non-zero exit, prints the last TAIL_LINES of that output, prints the
# FAIL verdict, and exits the WHOLE script with the stage's code.
#
# The command-substitution assignment is deliberately guarded with `&&/||`
# (never a plain `output="$(...)"`) — under the strict mode this script is
# assembled with, an unguarded failing assignment here would trip errexit
# and abort before the FAIL verdict / truncated output are ever printed,
# exactly the hazard pg-test-runner.bash documents for the same shape.
gogate_run_stage() {
  local stage="$1" code="$2"
  shift 2
  local output status
  output="$("$@" 2>&1)" && status=0 || status=$?
  if [[ $status -ne 0 ]]; then
    printf '%s\n' "$output" | tail -n "$TAIL_LINES"
    printf 'GOGATE: FAIL(%s)\n' "$stage"
    exit "$code"
  fi
}

# gofmt -l reports formatting violations as OUTPUT on an otherwise-zero exit
# (a non-zero exit means gofmt itself failed, e.g. a parse error) -- so
# unlike the other three stages, "failed" here means "exited non-zero OR
# produced any output", not just a non-zero exit.
gofmt_output="$(gofmt -l . 2>&1)" && gofmt_status=0 || gofmt_status=$?
if [[ $gofmt_status -ne 0 || -n $gofmt_output ]]; then
  printf '%s\n' "$gofmt_output" | tail -n "$TAIL_LINES"
  printf 'GOGATE: FAIL(fmt)\n'
  exit 10
fi

gogate_run_stage build 11 go build "$PKG"
gogate_run_stage vet 12 go vet "$PKG"

if [[ $QUICK -eq 0 ]]; then
  gogate_run_stage test 13 go test "$PKG" "${GO_TEST_ARGS[@]}"
fi

printf 'GOGATE: PASS\n'
exit 0
