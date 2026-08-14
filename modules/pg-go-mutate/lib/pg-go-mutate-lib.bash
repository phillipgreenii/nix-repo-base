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
