#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # HOME is read-only in the nix check sandbox ($HOME=/homeless-shelter), so
  # `go` cannot create its build cache there and every go list/vet/test call
  # fails outright -- masking the guards under test behind an unrelated
  # environment error. Same reason the library suite does this, and the same
  # HOME/GOCACHE-under-TMPDIR convention lib/go-builders.nix uses.
  export HOME="$TEST_DIR" GOCACHE="$TEST_DIR/go-build"

  # `go` also writes telemetry counters under
  # "$HOME/Library/Application Support/go/telemetry" (the macOS
  # os.UserConfigDir(), which is where nix's build-sandbox convention of
  # setting a fresh $HOME lands too) -- a location GOCACHE above does
  # nothing to redirect. That write can be done by a process that outlives
  # the `go list`/`go vet`/`go test` invocation which triggered it, and since
  # $TEST_DIR now IS $HOME, a still-running writer races teardown()'s
  # `rm -rf "$TEST_DIR"` below: the directory gets repopulated under the
  # delete, and the rm errors out (pg2-0uk00). `go telemetry off` writes only
  # its own one-line "mode" file and nothing else (verified empirically
  # 2026-08-20: a fresh $HOME running it gains exactly that one file), so
  # doing it here, before the script under test ever invokes `go`, means no
  # go process writes anywhere under $HOME afterward -- closing the race by
  # removing its only remaining writer rather than timing around it.
  go telemetry off >/dev/null 2>&1 || true

  if [ -n "${SCRIPT_UNDER_TEST:-}" ]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    # Running the raw .sh directly would fail: unlike the assembled artifact
    # the nix check exercises, it has no `source` line of its own -- the
    # builder prepends the library at build time (mkBashScript's
    # `libraries` composition) -- so none of the pgm_* functions the script
    # calls would be in scope. Assemble an equivalent wrapper here,
    # replicating that composition order (library sourced before the
    # command's .sh), per the bash-scripting skill's "Library wrapper
    # pattern" (the same technique modules/pnwf/pnwf/tests/test-pnwf.bats
    # uses for its own SCRIPT_UNDER_TEST fallback).
    local lib_path resolved_lib
    lib_path="${LIB_PATH:-$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pg-go-mutate-lib.bash}"
    if [ -d "$lib_path" ]; then
      resolved_lib="$lib_path/pg-go-mutate-lib.bash"
    else
      resolved_lib="${lib_path%%:*}"
    fi
    cat >"$TEST_DIR/pg-go-mutate-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$resolved_lib"
source "${BATS_TEST_DIRNAME}/../pg-go-mutate.sh"
WRAPPER
    chmod +x "$TEST_DIR/pg-go-mutate-wrapper"
    SCRIPT="$TEST_DIR/pg-go-mutate-wrapper"
  fi
  export SCRIPT

  # The version every stub engine below reports, and the value driven through the
  # PG_GO_MUTATE_GOMU_VERSION seam. Read from the environment rather than
  # hardcoded: mkBashScript exports every `config` entry into this check's env
  # (lib/bash-builders.nix), so this IS the string the derivation baked in, and
  # gomu-pin.nix stays the single source. That matters beyond tidiness — the baked
  # pin makes the E1 comparison fire in every end-to-end case here, so a hardcoded
  # literal would make a legitimate pin bump break six cases that are not about
  # the pin at all. Exported because the stubs are child processes.
  #
  # The fallback is reached only on a raw-source run, which carries no baked pin:
  # there both E1 seams are absent, so pgm_require_engine skips the comparison
  # outright and the value is immaterial — it need only be non-empty and unequal
  # to what the negative cases' stubs report.
  PGM_TEST_PINNED_VERSION="${PGM_PINNED_GOMU_VERSION:-0.0.0-raw-source}"
  export PGM_TEST_PINNED_VERSION
}

teardown() {
  # The interrupt test starts a long-lived stub engine and kills it as part of its
  # assertions; if that test fails midway the sandbox would sit waiting on it.
  if [ -n "${PGM_TEST_PID_LOG:-}" ] && [ -s "${PGM_TEST_PID_LOG}" ]; then
    kill -KILL "$(cat "$PGM_TEST_PID_LOG")" 2>/dev/null || true
  fi
  # The same interrupt test backgrounds the CLI itself and records its pid to
  # "$TEST_DIR/.cli-pid" before making any assertion. If one of ITS OWN
  # assertions fails first -- e.g. the wait for the engine's pid-log times
  # out because the pre-engine `go vet`/`go test` guards ran long under a
  # contended sandbox (observed under nix, pg2-0uk00) -- the test body never
  # reaches its intended `kill -TERM "$cli_pid"`, so that backgrounded CLI
  # (and whatever real `go` guard it may still be running) is orphaned into
  # this teardown with nothing having told it to stop, and its write into
  # "$TEST_DIR/go-build" (GOCACHE) races the rm -rf below exactly like the
  # engine would. Best-effort kill it too, on every test (the file only
  # exists for that one test).
  if [ -n "${TEST_DIR:-}" ] && [ -s "$TEST_DIR/.cli-pid" ]; then
    kill -KILL "$(cat "$TEST_DIR/.cli-pid")" 2>/dev/null || true
    wait "$(cat "$TEST_DIR/.cli-pid")" 2>/dev/null || true
  fi
  if [ -n "${TEST_DIR:-}" ]; then
    # A grandchild forked by either process just killed above (e.g. a `go`
    # compile/link tool already spawned before the kill landed) can still
    # hold a brief, in-flight write under $TEST_DIR after its parent is gone
    # -- an `rm -rf` run against that instant can fail ENOTEMPTY. Retry
    # briefly rather than failing the test on a race that resolves itself
    # within a second or two; if it is still failing after that, let the
    # last attempt's real failure surface rather than swallowing it.
    ok=
    for _ in 1 2 3 4 5; do
      rm -rf "$TEST_DIR" 2>/dev/null && ok=1 && break
      sleep 0.5
    done
    [ -n "$ok" ] || rm -rf "$TEST_DIR"
  fi
}

@test "--help exits 0 and documents every flag" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *--tags* ]]
  [[ "$output" == *--json* ]]
  [[ "$output" == *--timeout* ]]
  [[ "$output" == *--workers* ]]
}

@test "--help states that ./... is not accepted" {
  run "$SCRIPT" --help
  [[ "$output" == *"./..."* ]]
}

@test "rejects the ./... package pattern with a clear message" {
  run "$SCRIPT" './...'
  [ "$status" -ne 0 ]
  [[ "$output" == *"./..."* ]]
}

@test "rejects --workers 0 before invoking the engine" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --workers 0 "$TEST_DIR"
  [ "$status" -ne 0 ]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "rejects --timeout 0 before invoking the engine" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --timeout 0 "$TEST_DIR"
  [ "$status" -ne 0 ]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "rejects an unknown flag" {
  run "$SCRIPT" --nope
  [ "$status" -ne 0 ]
}

@test "aborts non-zero on a target that does not exist" {
  run "$SCRIPT" "$TEST_DIR/missing"
  [ "$status" -eq 14 ]
}

@test "exits 14 when the target does not exist" {
  run "$SCRIPT" "$TEST_DIR/nope"
  [ "$status" -eq 14 ]
}

@test "exits 14 when the target is a file, not a directory" {
  : >"$TEST_DIR/afile.go"
  run "$SCRIPT" "$TEST_DIR/afile.go"
  [ "$status" -eq 14 ]
}

@test "rejects a single .go file target instead of misdiagnosing it as untested" {
  printf 'package fixture\n' >"$TEST_DIR/a.go"
  run "$SCRIPT" "$TEST_DIR/a.go"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not a directory"* ]]
  # The old message sent an agent off to write a test that already exists.
  [[ "$output" != *"Write a test first"* ]]
}

@test "-- does not discard the target that follows it" {
  run "$SCRIPT" -- "$TEST_DIR/missing"
  [ "$status" -ne 0 ]
  # A bare `break` on `--` dropped the target and silently analysed `.` instead.
  [[ "$output" == *"$TEST_DIR/missing"* ]]
}

@test "--tags with no value fails with the standard error shape" {
  run "$SCRIPT" --tags
  [ "$status" -eq 2 ]
  [[ "$output" == *"pg-go-mutate: --tags needs a value"* ]]
}

@test "--tags rejects a value that would inject go flags" {
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" --tags 'contract -toolexec=/bin/sh' "$TEST_DIR"
  [ "$status" -eq 2 ]
  [[ "$output" == *"--tags must be"* ]]
  [ ! -e "$TEST_DIR/ran" ]
}

# --- fixtures for the end-to-end cases below ---------------------------------

# A module with a passing test, so every §5.1 guard is satisfied and the run
# reaches the engine.
make_module() {
  local dir="$TEST_DIR/$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF'
module example.com/fixture

go 1.25
EOF
  printf 'package fixture\n\nfunc Add(a, b int) int { return a + b }\n' >"$dir/fixture.go"
  cat >"$dir/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
  printf '%s\n' "$dir"
}

# A stub engine that writes a canned report with one real survivor into its cwd,
# via the PG_GO_MUTATE_GOMU seam (spec T7 -- never a PATH-prepended stub, which
# would only work because --suffix PATH leaves the pin breakable).
write_survivor_stub() {
  local target="$1" stub="$TEST_DIR/stub-gomu"
  cat >"$stub" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = version ]; then
  printf 'gomu version %s\n' "\$PGM_TEST_PINNED_VERSION"
  exit 0
fi
cat >mutation-report.json <<JSON
{
  "totalMutants": 3,
  "killedMutants": 0,
  "results": [
    { "mutant": { "id": "$target/fixture.go_0", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "conditional_binary",
        "original": "+", "mutated": "-",
        "description": "Replace + with -" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$target/fixture.go_1", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "return_zero_value",
        "original": "0", "mutated": "0",
        "description": "Replace return 0 with return 0" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$target/fixture.go_2", "filePath": "$target/fixture.go",
        "line": 3, "column": 30, "type": "conditional_binary",
        "original": "+", "mutated": "*",
        "description": "Replace + with *" },
      "status": "KILLED", "output": "", "error": "" }
  ],
  "statistics": { "killed": 1, "survived": 2, "timedOut": 0, "errors": 0,
                  "notViable": 0, "mutationScore": 33.3 }
}
JSON
EOF
  chmod +x "$stub"
  printf '%s\n' "$stub"
}

# The single most important behavioural promise on the tool (spec C2): it is a
# diagnostic, so survivors are a FINDING, never a failure. Every other CLI case
# is a rejection, so without this one nothing proved the success path end to end.
# stdout is captured SEPARATELY from stderr here, rather than through bats' `run`
# (which merges them): the assertions below are about which stream carries what
# and about the FIRST LINE of the worklist, and a merged capture cannot decide
# either.
@test "survivors present still exits 0 and prints the worklist (spec C2)" {
  target="$(make_module survivors)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  rc=0
  "$SCRIPT" "$target" >"$TEST_DIR/out" 2>"$TEST_DIR/err" || rc=$?
  [ "$rc" -eq 0 ]
  out="$(cat "$TEST_DIR/out")"
  [[ "$out" == *"surviving mutants"* ]]
  [[ "$out" == *"fixture.go"* ]]
  [[ "$out" == *"L3"* ]]
  [[ "$out" == *"Replace + with -"* ]]
  # No-op dropped from the worklist AND from the count (spec O5), with the raw
  # bucket still shown so the five statuses sum (spec O7).
  [[ "$out" != *"Replace return 0 with return 0"* ]]
  [[ "$out" == *"survived 2 (1 actionable, 1 no-op)"* ]]
  # First line free of a percentage and a killed count (spec O2).
  first="$(head -1 "$TEST_DIR/out")"
  [[ "$first" != *%* ]]
  [[ "$first" != *killed* ]]
  # Paths are target-relative (spec O4/O1).
  [[ "$out" != *"$target/fixture.go"* ]]
}

@test "--json exits 0 with target-relative paths and no mutation score" {
  target="$(make_module survivorsjson)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  rc=0
  "$SCRIPT" --json "$target" >"$TEST_DIR/out.json" 2>"$TEST_DIR/err" || rc=$?
  [ "$rc" -eq 0 ]
  # stdout must be JSON and nothing else -- a machine consumer parses this.
  jq -e . "$TEST_DIR/out.json" >/dev/null
  jq -e '.survivors[0].file == "fixture.go"' "$TEST_DIR/out.json"
  jq -e '.statistics.survivedActionable == 1' "$TEST_DIR/out.json"
  jq -e '.statistics.survivedNoOp == 1' "$TEST_DIR/out.json"
  jq -e '.statistics | has("mutationScore") == false' "$TEST_DIR/out.json"
  jq -e '.buildTagsNotRun == null' "$TEST_DIR/out.json"
}

@test "aborts when the engine reports a version other than the pinned one (spec E1)" {
  target="$(make_module devengine)"
  stub="$TEST_DIR/stub-gomu-dev"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  printf 'gomu version dev\n  commit: none\n  built:  unknown\n'
  exit 0
fi
touch "$PGM_TEST_RAN_MARKER"
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"
  export PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION"
  PGM_TEST_RAN_MARKER="$TEST_DIR/engine-ran"
  export PGM_TEST_RAN_MARKER
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"version mismatch"* ]]
  # And it aborted BEFORE spending a run on an unattributable engine.
  [ ! -e "$PGM_TEST_RAN_MARKER" ]
}

@test "accepts the pinned engine version and completes the analysis (spec E1)" {
  target="$(make_module pinnedengine)"
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  export PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION"
  run "$SCRIPT" "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *"surviving mutants"* ]]
}

# The bead's scenario, driven through the SHIPPED artifact: a consumer who
# installs pkgs.pg-go-mutate (or takes it from overlays.default) instead of
# importing homeModules.pg-go-mutate sets NEITHER PG_GO_MUTATE_GOMU_VERSION nor
# the store-path binding, so this run has only the build-time pin to protect it.
#
# `env -u PGM_PINNED_GOMU_VERSION` is load-bearing, not hygiene: mkBashScript
# exports every `config` entry into this check's environment
# (lib/bash-builders.nix:417), so without it the script would find the pin
# AMBIENTLY and this case would pass just as well against an artifact that baked
# nothing in. Unsetting it leaves the assignment rendered into the shipped script
# as the only place the expectation can come from.
#
# What each half proves, claimed no higher than it goes:
#
#   * negative -- with the home-manager seam absent AND the ambient copy removed,
#     an unpinned engine is refused and never invoked. Only the baked assignment
#     can have supplied the expectation.
#   * positive -- the same artifact still ACCEPTS an engine reporting the pinned
#     version, so the refusal is a pin and not a blanket abort. This fixture's
#     reported version and the baked assignment both derive from the single
#     `config` entry, so what it establishes is that the assignment really is IN
#     the artifact rather than only in the check environment.
#
# Neither half says anything about the OVERLAY, and no bats case here can: the
# engine is a stub this file writes, and pkgs.phillipgreenii.gomu is not
# resolvable in this flake at all. Agreement between the pin and the engine the
# overlay ships is checked at EVAL, by the assertion in
# home/pg-go-mutate/default.nix -- the only place both values are visible at once.
# Skipped on raw source, which has no build-time pin by construction.
@test "the build-time pin makes a direct install of the package reject an unpinned engine (spec E1)" {
  [ -n "${SCRIPT_UNDER_TEST:-}" ] || skip "assembled-artifact case: a raw-source run carries no build-time pin"
  target="$(make_module bakedpin)"
  stub="$TEST_DIR/stub-gomu-dev-nopin"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  printf 'gomu version dev\n  commit: none\n  built:  unknown\n'
  exit 0
fi
touch "$PGM_TEST_RAN_MARKER"
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"
  # The whole point: the home-manager module's seam is ABSENT here.
  unset PG_GO_MUTATE_GOMU_VERSION
  PGM_TEST_RAN_MARKER="$TEST_DIR/engine-ran"
  export PGM_TEST_RAN_MARKER
  run env -u PGM_PINNED_GOMU_VERSION "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"version mismatch"* ]]
  [ ! -e "$PGM_TEST_RAN_MARKER" ]

  # ... and the assignment baked into the artifact still ACCEPTS the version it
  # pins, so the protection is a pin and not a blanket refusal. The stub reports
  # $PGM_TEST_PINNED_VERSION, which this test process still holds even though the
  # child no longer does.
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  run env -u PGM_PINNED_GOMU_VERSION "$SCRIPT" "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *"surviving mutants"* ]]
}

# The EXPECTATION is read from the environment, not hardcoded: mkBashScript
# exports every `config` value into this check's env, so this compares the help
# text against the very value the derivation baked in rather than a third copy.
# The script under test, though, is run with that variable UNSET -- otherwise the
# ambient copy would satisfy the --help branch and the test would pass against an
# artifact that baked nothing in. The two directions are the point: the value
# comes from the check environment, the behaviour from the artifact.
@test "--help discloses the pinned engine version to a direct installer" {
  [ -n "${SCRIPT_UNDER_TEST:-}" ] || skip "assembled-artifact case: a raw-source run carries no build-time pin"
  [ -n "${PGM_PINNED_GOMU_VERSION:-}" ]
  run env -u PGM_PINNED_GOMU_VERSION "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *ENGINE* ]]
  [[ "$output" == *"pinned to gomu $PGM_PINNED_GOMU_VERSION"* ]]
}

@test "aborts when the engine is missing entirely rather than reporting an empty report" {
  target="$(make_module noengine)"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/no-such-engine"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"was not found"* ]]
  [[ "$output" != *"produced no report"* ]]
}

# --tags has to reach the GUARDS, not just the engine. A package whose only test
# is behind a custom tag and FAILS is the dangerous case: without the tag in
# GOFLAGS the guard never compiles it, passes, and the engine then reads every
# mutant as KILLED -- 100%, zero survivors, exit 0.
@test "--tags reaches the tests-healthy guard, which aborts on a failing tag-gated test" {
  target="$(make_module taggedfailing)"
  rm -f "$target/fixture_test.go"
  cat >"$target/contract_test.go" <<'EOF'
//go:build contract

package fixture

import "testing"

func TestContract(t *testing.T) { t.Fatal("deliberately failing") }
EOF
  export PG_GO_MUTATE_GOMU="$TEST_DIR/must-not-run"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\ntouch "%s/ran"\n' "$TEST_DIR" >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"

  run "$SCRIPT" --tags contract "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"do not pass on unmutated source"* ]]
  [ ! -e "$TEST_DIR/ran" ]
}

@test "a package whose only tests are tag-gated is not misreported as untested" {
  target="$(make_module taggedonly)"
  rm -f "$target/fixture_test.go"
  cat >"$target/contract_test.go" <<'EOF'
//go:build contract

package fixture

import "testing"

func TestContract(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
  # Without --tags the guard genuinely cannot see any test file.
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-unused"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\n' >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"has no test files"* ]]

  # WITH --tags it must get past that guard -- the false diagnosis this fixes.
  PG_GO_MUTATE_GOMU="$(write_survivor_stub "$target")"
  export PG_GO_MUTATE_GOMU
  run "$SCRIPT" --tags contract "$target"
  [ "$status" -eq 0 ]
  [[ "$output" != *"has no test files"* ]]
}

# The scenario actually observed on this branch: a TERM sent to the top-level
# wrapper's own pid. It leaked a workdir and orphaned the engine, because the
# engine supervisor ran inside a command-substitution SUBSHELL that owned the
# trap, the workdir and the engine pid -- so the signal never reached it. This
# asserts the whole CLI, not the library function, cleans up under that signal.
@test "a TERM on the CLI's own pid kills the engine and removes the private workdir (spec CL4)" {
  target="$(make_module cliinterrupt)"

  PGM_TEST_PID_LOG="$TEST_DIR/engine.pid"
  PGM_TEST_WORKDIR_LOG="$TEST_DIR/engine.cwd"
  export PGM_TEST_PID_LOG PGM_TEST_WORKDIR_LOG

  stub="$TEST_DIR/stub-gomu-sleep"
  cat >"$stub" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  printf 'gomu version %s\n' "$PGM_TEST_PINNED_VERSION"
  exit 0
fi
mkdir -p "${TMPDIR:-/tmp}/gomu_overlay_$$_1"
pwd >"$PGM_TEST_WORKDIR_LOG"
printf '%s\n' "$$" >"$PGM_TEST_PID_LOG"
# `exec` so the announced pid stays valid for this process's whole life.
exec sleep 120
EOF
  chmod +x "$stub"
  export PG_GO_MUTATE_GOMU="$stub"

  "$SCRIPT" "$target" >"$TEST_DIR/cli.out" 2>&1 &
  cli_pid=$!
  # Recorded for teardown() (pg2-0uk00): if this test's own assertions abort
  # before the "kill -TERM $cli_pid" below runs, teardown reaps this
  # backgrounded CLI too, rather than leaving it to keep writing under
  # $TEST_DIR while the rm -rf races it.
  printf '%s\n' "$cli_pid" >"$TEST_DIR/.cli-pid"

  # 120s: a pure liveness wait for the stub engine to start, which happens
  # only after the CLI's real pre-engine `go vet`/`go test` guards finish
  # against the fixture module -- normally near-instant, but observed to
  # take long enough to blow a 30s window under a contended sandbox (nix
  # build, batsJobs=4; pg2-0uk00). Widening this does not weaken what the
  # test asserts: it is a has-it-started-yet check, not the timing-sensitive
  # proof below (which stays tight on purpose, see its own comment).
  for _ in $(seq 1 1200); do
    [ -s "$PGM_TEST_PID_LOG" ] && break
    sleep 0.1
  done
  [ -s "$PGM_TEST_PID_LOG" ]
  engine_pid="$(cat "$PGM_TEST_PID_LOG")"
  workdir="$(cat "$PGM_TEST_WORKDIR_LOG")"
  [ -d "$workdir" ]
  [ -d "$workdir/gomu_overlay_${engine_pid}_1" ]

  # The signal goes to the CLI's pid ONLY -- not to the process group -- which is
  # what made the observed leak possible.
  kill -TERM "$cli_pid"
  wait "$cli_pid" || true

  # 5s, deliberately FAR shorter than the stub's 120s sleep. A generous window
  # would let the stub simply expire on its own, at which point the orphaned
  # supervisor tidies up and the test passes without the fix -- verified: at a 30s
  # window this case passed against the un-fixed command-substitution shape.
  for _ in $(seq 1 50); do
    kill -0 "$engine_pid" 2>/dev/null || break
    sleep 0.1
  done
  run kill -0 "$engine_pid"
  [ "$status" -ne 0 ]
  [ ! -e "$workdir" ]
}

@test "a target that is not a Go module says so instead of blaming missing tests" {
  target="$TEST_DIR/notamodule"
  mkdir -p "$target"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-unused-2"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\n' >"$PG_GO_MUTATE_GOMU"
  chmod +x "$PG_GO_MUTATE_GOMU"
  run "$SCRIPT" "$target"
  [ "$status" -ne 0 ]
  [[ "$output" == *"not a Go module"* ]]
  [[ "$output" != *"Write a test first"* ]]
}

# --- exit-code allocation 10-14 (ADR 0026 decision 3) ------------------------
#
# These tighten the `-ne 0` guard-failure assertions above into the specific
# codes the sweep (pg-go-mutate-sweep, a later task) classifies on. Each stub
# engine below is created inline, matching the pattern the guard-related cases
# above already use (e.g. "a target that is not a Go module says so...").

@test "exits 10 when the module has no test files" {
  mkdir -p "$TEST_DIR/m"
  cat >"$TEST_DIR/m/go.mod" <<'EOF'
module example.com/m

go 1.25.0
EOF
  printf 'package m\n\nfunc F() int { return 1 }\n' >"$TEST_DIR/m/m.go"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\n' >"$TEST_DIR/stub-gomu"
  chmod +x "$TEST_DIR/stub-gomu"
  run env PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu" \
      PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION" "$SCRIPT" "$TEST_DIR/m"
  [ "$status" -eq 10 ]
  [[ "$output" == *"has no test files"* ]]
}

@test "exits 11 when the target is not a Go module" {
  mkdir -p "$TEST_DIR/plain"
  printf 'package plain\n' >"$TEST_DIR/plain/p.go"
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\n' >"$TEST_DIR/stub-gomu"
  chmod +x "$TEST_DIR/stub-gomu"
  run env PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu" \
      PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION" "$SCRIPT" "$TEST_DIR/plain"
  [ "$status" -eq 11 ]
}

@test "exits 12 when the target's tests already fail on unmutated source" {
  mkdir -p "$TEST_DIR/bad"
  cat >"$TEST_DIR/bad/go.mod" <<'EOF'
module example.com/bad

go 1.25.0
EOF
  printf 'package bad\n\nfunc F() int { return 1 }\n' >"$TEST_DIR/bad/b.go"
  cat >"$TEST_DIR/bad/b_test.go" <<'EOF'
package bad

import "testing"

func TestF(t *testing.T) {
	if F() != 2 {
		t.Fatal("deliberately failing")
	}
}
EOF
  printf '#!/usr/bin/env bash\nif [ "${1:-}" = version ]; then printf "gomu version %%s\\n" "$PGM_TEST_PINNED_VERSION"; exit 0; fi\n' >"$TEST_DIR/stub-gomu"
  chmod +x "$TEST_DIR/stub-gomu"
  run env PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu" \
      PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION" "$SCRIPT" "$TEST_DIR/bad"
  [ "$status" -eq 12 ]
}

@test "exits 13 when the engine is absent" {
  mkdir -p "$TEST_DIR/m2"
  cat >"$TEST_DIR/m2/go.mod" <<'EOF'
module example.com/m2

go 1.25.0
EOF
  printf 'package m2\n' >"$TEST_DIR/m2/m.go"
  run env PG_GO_MUTATE_GOMU="$TEST_DIR/definitely-not-here" "$SCRIPT" "$TEST_DIR/m2"
  [ "$status" -eq 13 ]
}
