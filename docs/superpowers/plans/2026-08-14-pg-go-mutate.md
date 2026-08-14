# pg-go-mutate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `pg-go-mutate`, a CLI that reports which assertions a Go package's tests are missing, using a pinned `gomu` as the mutation engine.

**Architecture:** A **Facade** bash CLI over `gomu`. `gomu` is pinned as a third-party repackage in `phillipgreenii-nix-overlay`; the wrapper and its home-manager feature module live in `phillipg-nix-repo-base` beside `pn`/`pjira`; the agent skill lives in `phillipgreenii-nix-agent-support`. The wrapper adds guards `gomu` lacks, runs it in a private temp cwd, and transforms its JSON into a survivor worklist. It is a diagnostic: it always exits 0 on a completed analysis and gates nothing.

**Tech Stack:** Nix flakes, `mkBashScript`/`mkBashLibrary` (repo-base `lib/bash-builders.nix`), bats, `nvfetcher` + `buildGoModule`, home-manager, `jq`, Go 1.25.

**Spec:** `docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md` (this repo, branch `spec/pg2-xulhg`). **Read it before Task 1** — every requirement ID below (`C3`, `CL1`, `W9`, …) refers to it.

**Bead:** `pg2-xulhg`

## Global Constraints

- **Engine invocation:** always `gomu run`, never bare `gomu` (bare reads `workers` as 0 and deadlocks permanently). Always pass `--incremental=false` literally. (**C5**)
- **Engine resolution:** `"${PG_GO_MUTATE_GOMU:-gomu}"`. Never a `runtimeDeps` entry — repo-base's builders append those with `--suffix PATH`, so an ambient binary would win. (**W9**)
- **Counts:** read only from `.statistics.*` in gomu's JSON. Top-level `.killedMutants` is always `0` outside CI mode. (**§5.1**)
- **Exit codes:** `0` for any completed analysis regardless of survivors; non-zero **only** for operational failure. (**C2**, **C3**)
- **Working directory:** every `gomu` invocation runs with cwd set to a fresh private `mktemp -d`. gomu writes `mutation-report.json` and `.gomu_history.json` relative to cwd, not the target. (**CL1**, **CL2**)
- **Overlay attribute:** `pkgs.phillipgreenii.gomu`. The flat top-level names in that overlay are a frozen back-compat bridge and must not be grown. (**W1**)
- **Option namespace:** `phillipgreenii.pg-go-mutate`, not `phillipgreenii.programs.*`. (**W14**)
- **No third-party fetch in repo-base.** It has none today; `gomu` is pinned in the overlay flake only. (**W6**)
- **Repo-base package family rule:** `gomu` is a raw `buildGoModule` third-party repackage, so it keeps its own `vendorHash`. The `gomod2nix` rule in `CLAUDE.md` scopes to `mkGoApp`/`mkGoBinary` only.
- **`bash-scripting` skill is authoritative** for the wrapper. Required artifacts for a public command: `.sh` source, `default.nix`, `--help` text, tldr page, bash completion, zsh completion, bats tests. Never pass `excludeShellChecks`; use inline `# shellcheck disable=SCxxxx`.
- **Long commands:** `nix build`, `nix flake check` and `pn workspace flake-check` exceed the 2-minute default. Run them with an explicit timeout of at least 600000 ms or in the background.
- **Worktree pre-commit:** `.pre-commit-config.yaml` is a gitignored symlink into `/nix/store`. A fresh worktree lacks it and commits then skip the hooks. Copy it in from the canonical clone first: `cp -a <canonical>/.pre-commit-config.yaml <worktree>/`.

## File Structure

**`phillipgreenii-nix-overlay`** (Task 1)

- Modify `nvfetcher.toml` — add the `[gomu]` source stanza
- Regenerate `_sources/generated.nix`, `_sources/generated.json`
- Create `packages/gomu/default.nix` — `buildGoModule` recipe
- Modify `flake.nix` — `phillipgreenii.gomu` attribute + `packages` output entry
- Modify `README.md`, `verify-provenance.sh`

**`phillipg-nix-repo-base`** (Tasks 2–5)

- Create `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash` — guards, engine invocation, JSON transform. All logic lives here so bats can test it directly.
- Create `modules/pg-go-mutate/lib/default.nix`, `lib/tests/test-pg-go-mutate-lib.bats`
- Create `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh` — argument parsing and orchestration only
- Create `modules/pg-go-mutate/pg-go-mutate/{default.nix,pg-go-mutate.md,completions/pg-go-mutate.bash,completions/_pg-go-mutate,tests/test-pg-go-mutate.bats}`
- Create `modules/pg-go-mutate/scripts.nix` — assembles lib + command, exposes `packages`/`tldr`/`checks`/`check`
- Create `home/pg-go-mutate/default.nix` — the feature module
- Modify `flake.nix` — thread `pgGoMutateScripts`, add to `packages`, `overlays.default`, `homeModules`, `checks`
- Modify `CLAUDE.md` — one line pointing at the skill

**`phillipgreenii-nix-agent-support`** (Task 6)

- Create `claude-marketplace/pg-go-mutate/.claude-plugin/plugin.json`
- Create `claude-marketplace/pg-go-mutate/skills/go-test-gaps/SKILL.md`
- Modify `claude-marketplace/.claude-plugin/marketplace.json`

Splitting the logic into a `.bash` library with a thin `.sh` entry point is what makes Tasks 2 and 3 independently testable: bats sources the library directly and calls single functions, with no CLI parsing in the way.

---

### Task 1: Pin gomu in the overlay

**Repo:** `phillipgreenii-nix-overlay`

**Files:**

- Modify: `nvfetcher.toml`
- Create: `packages/gomu/default.nix`
- Modify: `_sources/generated.nix`, `_sources/generated.json` (generated — do not hand-edit)
- Modify: `flake.nix` (the `phillipgreenii = { … }` attrset, and the `packages = { inherit (extended.phillipgreenii) … }` block)
- Modify: `README.md` (packages table), `verify-provenance.sh` (`METHODS`)

**Interfaces:**

- Consumes: nothing.
- Produces: `pkgs.phillipgreenii.gomu`, a derivation whose `$out/bin/gomu` reports `gomu version <X.Y.Z>` matching the pinned tag. Consumed by Task 5's feature module.

- [ ] **Step 1: Determine whether the upstream publishes GitHub releases**

`src.github` is nvchecker's `CheckGitHubRelease` and 404s on a tags-only repo (**W4**).

```bash
gh api repos/sivchari/gomu/releases --jq 'length'
gh api repos/sivchari/gomu/tags --jq '.[0].name'
```

Expected: a non-zero release count means `src.github` works. If it prints `0`, use the tags-only fallback in Step 2 instead.

- [ ] **Step 2: Add the nvfetcher stanza**

Append to `nvfetcher.toml`. Use the first form if Step 1 found releases, the second if it found only tags:

```toml
# gomu -- Go mutation testing engine, wrapped by pg-go-mutate in nix-repo-base.
# fetch.github takes rev $ver verbatim, so the leading "v" is kept (see [pint]).
[gomu]
src.github = "sivchari/gomu"
fetch.github = "sivchari/gomu"
```

```toml
# Tags-only fallback: this repo publishes no GitHub releases.
[gomu]
src.github_tag = "sivchari/gomu"
src.include_regex = "v[0-9.]+"
fetch.github = "sivchari/gomu"
```

- [ ] **Step 3: Regenerate the sources**

```bash
nix run nixpkgs#nvfetcher
git diff --numstat -- _sources/
```

Expected: `_sources/generated.nix` and `_sources/generated.json` both change and now contain a `gomu` entry. Without this, `sources.gomu` is an undefined attribute (**W2**).

- [ ] **Step 4: Write the package recipe with a deliberately wrong vendorHash**

Create `packages/gomu/default.nix`:

```nix
{
  lib,
  buildGoModule,
  sources,
}:
buildGoModule rec {
  pname = "gomu";
  version = lib.removePrefix "v" sources.gomu.version;
  src = sources.gomu.src;

  vendorHash = lib.fakeHash;

  subPackages = [ "cmd/gomu" ];

  # Load-bearing: without -X main.version the binary reports "gomu version dev"
  # and the pin is unattributable, defeating spec E1.
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  doCheck = false;

  meta = {
    description = "Mutation testing engine for Go";
    homepage = "https://github.com/sivchari/gomu";
    license = lib.licenses.mit;
    mainProgram = "gomu";
    platforms = lib.platforms.unix;
  };
}
```

- [ ] **Step 5: Register the attribute and the packages output**

In `flake.nix`, add to the `phillipgreenii = { … }` attrset alongside `gh-stack`:

```nix
gomu = final.callPackage ./packages/gomu { inherit sources; };
```

and add `gomu` to the `packages = { inherit (extended.phillipgreenii) … }` list (alphabetically, after `glowm`). Omitting the `packages` entry means the overlay's CI never builds it (**W3**).

- [ ] **Step 6: Build to learn the real vendorHash**

```bash
nix build .#gomu 2>&1 | tee /tmp/gomu-build.log
```

Expected: FAIL with `hash mismatch in fixed-output derivation`, printing `specified: sha256-AAAA…` and `got: sha256-<real>`. Copy the `got:` value into `vendorHash` replacing `lib.fakeHash`.

- [ ] **Step 7: Build for real and verify the version stamp**

```bash
nix build .#gomu && ./result/bin/gomu version
```

Expected: PASS, and the output's first line reports the pinned version — **not** `gomu version dev`. If it says `dev`, the `ldflags` did not apply; check that upstream's version variable is really `main.version` (`rg -n 'version\s*=' cmd/gomu/main.go` in the fetched source) and correct the `-X` path.

- [ ] **Step 8: Add the README row and provenance entry**

Add a `gomu` row to the packages table in `README.md`, and a `gomu="git-source"` entry to `METHODS` in `verify-provenance.sh`, matching the `pint`/`glowm` entries.

- [ ] **Step 9: Commit**

```bash
git add nvfetcher.toml _sources packages/gomu flake.nix README.md verify-provenance.sh
git commit -m "feat(gomu): pin the Go mutation testing engine (pg2-xulhg)"
```

---

### Task 2: Library — guards

**Repo:** `phillipg-nix-repo-base`

This task delivers only the preconditions. It is separately reviewable because a wrong guard is the spec's highest-severity risk: **gomu marks any non-zero `go test` exit as `KILLED`, so a package whose tests already fail reports 100% with zero survivors and exits 0.**

**Files:**

- Create: `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`
- Create: `modules/pg-go-mutate/lib/default.nix`
- Create: `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`

**Interfaces:**

- Consumes: nothing.
- Produces, for Tasks 3 and 4:
  - `pgm_require_go()` → `0` if `go` is on PATH, else `1` after printing an actionable message to stderr.
  - `pgm_has_tests <abs-target>` → `0` if the target has at least one test file, else `1`.
  - `pgm_tests_healthy <abs-target>` → `0` if the target's tests link **and** pass on unmutated source, else `1` with the failure on stderr.
  - `pgm_detect_tags <abs-target>` → prints a comma-separated list of _unsatisfied_ custom build tags found in the target's `_test.go` files; empty output means none.
  - `pgm_validate_flags <workers> <timeout>` → `0` if both are integers `>= 1`, else `1`.

- [ ] **Step 1: Write the failing tests**

Create `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`:

```bash
#!/usr/bin/env bats

setup() {
  LIB_PATH="${LIB_PATH:-${BATS_TEST_DIRNAME}/../pg-go-mutate-lib.bash}"
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "$LIB_PATH"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
}

teardown() {
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
}

# Builds a module OUTSIDE any git repo, per spec CL6 and T5.
make_module() {
  local dir="$TEST_DIR/$1"
  mkdir -p "$dir"
  cat >"$dir/go.mod" <<'EOF'
module example.com/fixture

go 1.25
EOF
  printf 'package fixture\n\nfunc Add(a, b int) int { return a + b }\n' >"$dir/fixture.go"
  printf '%s\n' "$dir"
}

add_passing_test() {
  cat >"$1/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) {
  if Add(1, 2) != 3 {
    t.Fatal("want 3")
  }
}
EOF
}

@test "pgm_validate_flags rejects zero workers" {
  run pgm_validate_flags 0 60
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags rejects zero timeout" {
  run pgm_validate_flags 2 0
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags rejects non-numeric input" {
  run pgm_validate_flags two 60
  [ "$status" -eq 1 ]
}

@test "pgm_validate_flags accepts one and one" {
  run pgm_validate_flags 1 1
  [ "$status" -eq 0 ]
}

@test "pgm_has_tests fails on a module with no test files" {
  target="$(make_module notests)"
  run pgm_has_tests "$target"
  [ "$status" -eq 1 ]
}

@test "pgm_has_tests succeeds when a test file exists" {
  target="$(make_module withtests)"
  add_passing_test "$target"
  run pgm_has_tests "$target"
  [ "$status" -eq 0 ]
}

@test "pgm_tests_healthy succeeds on a passing suite" {
  target="$(make_module healthy)"
  add_passing_test "$target"
  run pgm_tests_healthy "$target"
  [ "$status" -eq 0 ]
}

@test "pgm_tests_healthy fails when tests are already failing" {
  target="$(make_module failing)"
  cat >"$target/fixture_test.go" <<'EOF'
package fixture

import "testing"

func TestAdd(t *testing.T) { t.Fatal("deliberately failing") }
EOF
  run pgm_tests_healthy "$target"
  [ "$status" -eq 1 ]
}

@test "pgm_tests_healthy fails when a test file does not compile" {
  target="$(make_module noncompiling)"
  printf 'package fixture\n\nthis is not go\n' >"$target/fixture_test.go"
  run pgm_tests_healthy "$target"
  [ "$status" -eq 1 ]
}

@test "pgm_detect_tags ignores satisfied constraints like darwin and linux" {
  target="$(make_module satisfiedtags)"
  add_passing_test "$target"
  printf '//go:build darwin\n\npackage fixture\n' >"$target/plat_test.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [ -z "$output" ]
}

@test "pgm_detect_tags reports an unsatisfied custom tag" {
  target="$(make_module customtags)"
  add_passing_test "$target"
  printf '//go:build contract\n\npackage fixture\n' >"$target/contract_test.go"
  run pgm_detect_tags "$target"
  [ "$status" -eq 0 ]
  [[ "$output" == *contract* ]]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/.worktrees/pg2-xulhg
bats modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
```

Expected: FAIL — every test errors because `pg-go-mutate-lib.bash` does not exist yet.

- [ ] **Step 3: Implement the guards**

Create `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`:

```bash
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
    '' | *[!0-9]*) pgm_die "--workers must be a positive integer, got '$workers'"; return 1 ;;
  esac
  case "$timeout" in
    '' | *[!0-9]*) pgm_die "--timeout must be a positive integer, got '$timeout'"; return 1 ;;
  esac
  # --workers 0 makes gomu's semaphore unbuffered: every worker blocks forever
  # and it installs no signal handler, so the deadlock is only escapable by
  # SIGKILL. --timeout 0 marks every mutant TIMED_OUT.
  [ "$workers" -ge 1 ] || { pgm_die "--workers must be >= 1 (0 deadlocks gomu permanently)"; return 1; }
  [ "$timeout" -ge 1 ] || { pgm_die "--timeout must be >= 1 (0 times out every mutant)"; return 1; }
  return 0
}

pgm_has_tests() {
  local target="$1" counts
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
  if ! out="$(cd "$target" && go vet ./... 2>&1)"; then
    printf 'pg-go-mutate: the target does not vet cleanly on unmutated source:\n%s\n' "$out" >&2
    return 1
  fi
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
bats modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
```

Expected: PASS, 11 tests. If `pgm_detect_tags` fails, print `$visible` and the `find` output to see which file names are being compared — the comparison is on basenames.

- [ ] **Step 5: Add the library derivation**

Create `modules/pg-go-mutate/lib/default.nix`:

```nix
{
  mkBashLibrary,
  pkgs,
}:

mkBashLibrary {
  name = "pg-go-mutate-lib";
  src = ./.;
  description = "Guards, engine invocation and report transform for pg-go-mutate";
  runtimeDeps = [
    pkgs.jq
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
```

- [ ] **Step 6: Commit**

```bash
git add modules/pg-go-mutate/lib
git commit -m "feat(pg-go-mutate): add guard library with the tests-healthy precondition (pg2-xulhg)"
```

---

### Task 3: Library — engine invocation and worklist transform

**Repo:** `phillipg-nix-repo-base`

**Files:**

- Modify: `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`
- Modify: `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`

**Interfaces:**

- Consumes: `pgm_gomu_bin`, `pgm_die` from Task 2.
- Produces, for Task 4:
  - `pgm_run_engine <abs-target> <workers> <timeout> <tags>` → runs `gomu run` in a fresh private cwd, prints the absolute path of the harvested `mutation-report.json` on stdout, returns `1` on operational failure. Cleans up its private cwd and its own overlay dirs.
  - `pgm_report_sane <report-path>` → `0` if the report passes the **C6** gate, else `1` with the reason on stderr.
  - `pgm_worklist <report-path> <abs-target>` → prints the human worklist.
  - `pgm_worklist_json <report-path> <abs-target>` → prints the JSON worklist with target-relative paths.

- [ ] **Step 1: Write the failing tests**

Append to `modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats`:

```bash
# A canned report shaped exactly like gomu's: absolute paths, uppercase
# statuses, per-operator mutationTypes, and killedMutants deliberately 0
# (gomu only populates it in CI mode).
write_report() {
  cat >"$TEST_DIR/report.json" <<EOF
{
  "version": "0.1.0",
  "totalFiles": 1,
  "processedFiles": 1,
  "totalMutants": 5,
  "killedMutants": 0,
  "files": null,
  "duration": 1000,
  "statistics": {
    "killed": 1,
    "survived": 3,
    "timedOut": 0,
    "errors": 0,
    "notViable": 1,
    "mutationScore": 25.0,
    "mutationTypes": {
      "branch_condition": { "total": 3, "killed": 1, "survived": 2 },
      "return_zero_value": { "total": 2, "killed": 0, "survived": 1 }
    }
  },
  "results": [
    { "mutant": { "id": "$TEST_DIR/mod/a.go_0", "filePath": "$TEST_DIR/mod/a.go",
        "line": 30, "column": 5, "type": "branch_condition",
        "original": "err != nil", "mutated": "false",
        "description": "Replace branch condition \\"err != nil\\" with false" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_1", "filePath": "$TEST_DIR/mod/a.go",
        "line": 42, "column": 9, "type": "branch_condition",
        "original": "n > 0", "mutated": "true",
        "description": "Replace branch condition \\"n > 0\\" with true" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_2", "filePath": "$TEST_DIR/mod/a.go",
        "line": 50, "column": 2, "type": "return_zero_value",
        "original": "\\"\\"", "mutated": "\\"\\"",
        "description": "Replace return \\"\\" with return \\"\\"" },
      "status": "SURVIVED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_3", "filePath": "$TEST_DIR/mod/a.go",
        "line": 60, "column": 2, "type": "branch_condition",
        "original": "ok", "mutated": "false",
        "description": "Replace branch condition \\"ok\\" with false" },
      "status": "KILLED", "output": "", "error": "" },
    { "mutant": { "id": "$TEST_DIR/mod/a.go_4", "filePath": "$TEST_DIR/mod/a.go",
        "line": 70, "column": 2, "type": "return_zero_value",
        "original": "0", "mutated": "0",
        "description": "Replace return 0 with return 0" },
      "status": "NOT_VIABLE", "output": "compilation error", "error": "x" }
  ]
}
EOF
  printf '%s\n' "$TEST_DIR/report.json"
}

@test "pgm_report_sane accepts a healthy report" {
  r="$(write_report)"
  run pgm_report_sane "$r"
  [ "$status" -eq 0 ]
}

@test "pgm_report_sane rejects a missing report" {
  run pgm_report_sane "$TEST_DIR/nope.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane rejects unparseable json" {
  printf 'not json' >"$TEST_DIR/bad.json"
  run pgm_report_sane "$TEST_DIR/bad.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane rejects a null results array" {
  printf '{"totalMutants":0,"results":null,"statistics":{}}' >"$TEST_DIR/null.json"
  run pgm_report_sane "$TEST_DIR/null.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane rejects a majority-non-viable report" {
  printf '{"totalMutants":10,"results":[1],"statistics":{"notViable":9,"errors":0,"killed":1,"survived":0}}' >"$TEST_DIR/nv.json"
  run pgm_report_sane "$TEST_DIR/nv.json"
  [ "$status" -eq 1 ]
}

@test "pgm_report_sane flags the all-killed signature as suspect" {
  printf '{"totalMutants":4,"results":[1],"statistics":{"notViable":0,"errors":0,"killed":4,"survived":0}}' >"$TEST_DIR/allk.json"
  run pgm_report_sane "$TEST_DIR/allk.json"
  [ "$status" -eq 1 ]
  [[ "$output" == *suspect* ]]
}

@test "pgm_worklist drops no-op mutants where original equals mutated" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  # The two return_zero_value no-ops must not appear.
  [[ "$output" != *"return \"\" with return \"\""* ]]
  [[ "$output" != *"L50"* ]]
  [[ "$output" != *"L70"* ]]
}

@test "pgm_worklist lists the two real survivors with line numbers" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  [[ "$output" == *"L30"* ]]
  [[ "$output" == *"L42"* ]]
  [[ "$output" != *"L60"* ]]  # KILLED
}

@test "pgm_worklist first line carries no percentage and no killed count" {
  r="$(write_report)"
  run pgm_worklist "$r" "$TEST_DIR/mod"
  first="$(printf '%s\n' "$output" | head -1)"
  [[ "$first" != *%* ]]
  [[ "$first" != *killed* ]]
}

@test "pgm_worklist_json emits target-relative paths only" {
  r="$(write_report)"
  run pgm_worklist_json "$r" "$TEST_DIR/mod"
  [ "$status" -eq 0 ]
  [[ "$output" != *"$TEST_DIR"* ]]
  printf '%s' "$output" | jq -e '.survivors[0].file == "a.go"'
}

@test "pgm_run_engine leaves no artifacts in the target directory" {
  target="$(make_module cleanrun)"
  add_passing_test "$target"
  export PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu"
  cat >"$PG_GO_MUTATE_GOMU" <<'EOF'
#!/usr/bin/env bash
# Stub engine: writes a minimal report into CWD, as the real gomu does.
printf '{"totalMutants":1,"killedMutants":0,"results":[{"mutant":{"id":"x","filePath":"/x/a.go","line":1,"column":1,"type":"t","original":"a","mutated":"b","description":"d"},"status":"SURVIVED"}],"statistics":{"killed":0,"survived":1,"notViable":0,"timedOut":0,"errors":0,"mutationScore":0}}' >mutation-report.json
EOF
  chmod +x "$PG_GO_MUTATE_GOMU"
  run pgm_run_engine "$target" 2 60 ""
  [ "$status" -eq 0 ]
  [ ! -e "$target/mutation-report.json" ]
  [ ! -e "$target/.gomu_history.json" ]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
bats modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
```

Expected: the 11 Task 2 tests PASS; the 11 new ones FAIL with `command not found`.

- [ ] **Step 3: Implement the engine invocation and transform**

Append to `modules/pg-go-mutate/lib/pg-go-mutate-lib.bash`:

```bash
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
  local target="$1" workers="$2" timeout="$3" tags="$4"
  local workdir gomu_pid rc report
  workdir="$(mktemp -d)" || { pgm_die "could not create a private working directory"; return 1; }

  # shellcheck disable=SC2064  # expand workdir now, deliberately
  trap "rm -rf -- '$workdir'" EXIT INT TERM HUP

  if [ -n "$tags" ]; then
    # APPEND, never clobber: gomu sets no cmd.Env, so its `go` subprocesses
    # inherit this and both `go build -overlay` and `go test -overlay` honour
    # it. This is a real fix for upstream issue #94 (spec W11).
    export GOFLAGS="-tags=$tags ${GOFLAGS:-}"
  fi

  # `gomu run`, never bare `gomu`: the root command runs the same function with
  # the run flags unregistered, so workers reads 0 and deadlocks (spec C5).
  ( cd "$workdir" && "$(pgm_gomu_bin)" run \
      --incremental=false --fail-on-gate=false --output json \
      --workers "$workers" --timeout "$timeout" "$target" >"$workdir/engine.log" 2>&1 ) &
  gomu_pid=$!
  wait "$gomu_pid"
  rc=$?

  # Scope overlay-dir removal to THIS run: the name is
  # gomu_overlay_<pid>_<unixnano>, so a bare gomu_overlay_* glob would delete a
  # concurrent run's live working directories (spec CL3).
  rm -rf -- "${TMPDIR:-/tmp}"/gomu_overlay_"$gomu_pid"_* 2>/dev/null || true

  if [ ! -f "$workdir/mutation-report.json" ]; then
    printf 'pg-go-mutate: the engine produced no report (exit %s). Output:\n' "$rc" >&2
    cat "$workdir/engine.log" >&2
    return 1
  fi

  # Harvest out of the private cwd BEFORE the trap removes it.
  report="$(mktemp -t pg-go-mutate-report.XXXXXX.json)"
  cp "$workdir/mutation-report.json" "$report"
  printf '%s\n' "$report"
  return 0
}

# Gates on the REPORT, never on the engine's exit code: gomu `continue`s past
# per-file generate and execute errors and still exits 0 (spec C6).
pgm_report_sane() {
  local report="$1"
  [ -f "$report" ] || { pgm_die "no report at $report"; return 1; }
  jq -e . "$report" >/dev/null 2>&1 || { pgm_die "the engine's report is not valid JSON"; return 1; }
  jq -e '.results != null' "$report" >/dev/null 2>&1 \
    || { pgm_die "the engine analyzed nothing (results is null)"; return 1; }
  jq -e '(.totalMutants // 0) > 0' "$report" >/dev/null 2>&1 \
    || { pgm_die "the engine generated no mutants for this target"; return 1; }
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
  local report="$1" target="$2" survivors n
  survivors="$(jq -r --arg target "$target" "$(_pgm_survivors_filter) | group_by(.file)[] | \"\\(.[0].file)\", (.[] | \"    L\\(.line)   \\(.description)   [\\(.type)]\"), \"\"" "$report")"
  n="$(jq -r --arg target "$target" "$(_pgm_survivors_filter) | length" "$report")"

  # First line carries no percentage and no killed count (spec O2).
  printf 'pg-go-mutate: %s surviving mutants in %s\n\n' "$n" "$target"
  printf '%s\n' "$survivors"
  printf 'Each surviving mutant is an assertion your tests do not make.\n\n'
  # All five buckets, or the summary will not sum (spec O7).
  jq -r '.statistics | "  killed \(.killed)  survived \(.survived)  not-viable \(.notViable)  timed-out \(.timedOut)  errors \(.errors)"' "$report"
}

pgm_worklist_json() {
  local report="$1" target="$2"
  jq --arg target "$target" "{ target: \$target, survivors: $(_pgm_survivors_filter), statistics: .statistics }" "$report"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
bats modules/pg-go-mutate/lib/tests/test-pg-go-mutate-lib.bats
```

Expected: PASS, 22 tests.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/lib
git commit -m "feat(pg-go-mutate): run the engine in a private cwd and emit a survivor worklist (pg2-xulhg)"
```

---

### Task 4: The CLI

**Repo:** `phillipg-nix-repo-base`

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh`
- Create: `modules/pg-go-mutate/pg-go-mutate/default.nix`
- Create: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.md`
- Create: `modules/pg-go-mutate/pg-go-mutate/completions/pg-go-mutate.bash`
- Create: `modules/pg-go-mutate/pg-go-mutate/completions/_pg-go-mutate`
- Create: `modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`
- Create: `modules/pg-go-mutate/scripts.nix`

**Interfaces:**

- Consumes: every `pgm_*` function from Tasks 2 and 3.
- Produces: the `pg-go-mutate` executable, and `scripts.nix` exposing `{ pg-go-mutate-lib, pg-go-mutate, packages, tldr, checks, check }` for Task 5.

- [ ] **Step 1: Write the failing CLI tests**

Create `modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`:

```bash
#!/usr/bin/env bats

setup() {
  SCRIPT="${SCRIPT_UNDER_TEST:-${BATS_TEST_DIRNAME}/../pg-go-mutate.sh}"
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
}

teardown() {
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
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

@test "rejects an unknown flag" {
  run "$SCRIPT" --nope
  [ "$status" -ne 0 ]
}

@test "aborts non-zero on a target that is not a directory or .go file" {
  run "$SCRIPT" "$TEST_DIR/missing"
  [ "$status" -ne 0 ]
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
bats modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats
```

Expected: FAIL — the script does not exist.

- [ ] **Step 3: Implement the CLI**

Create `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh`. The builder injects `set -euo pipefail` and prepends the library, so neither is written here:

```bash
usage() {
  cat <<'EOF'
pg-go-mutate — report which assertions a Go package's tests are missing.

USAGE
  pg-go-mutate [PATH] [options]

  PATH   A directory (walked RECURSIVELY, so nested packages are included) or a
         single .go file. Defaults to the current directory.
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
    -h | --help) usage; exit 0 ;;
    --json) as_json=1; shift ;;
    --tags) tags="${2:?--tags needs a value}"; shift 2 ;;
    --workers) workers="${2:?--workers needs a value}"; shift 2 ;;
    --timeout) timeout="${2:?--timeout needs a value}"; shift 2 ;;
    --) shift; break ;;
    -*) printf 'pg-go-mutate: unknown flag %s\n\n' "$1" >&2; usage >&2; exit 2 ;;
    *) target="$1"; shift ;;
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
target="$(cd "$(dirname "$target")" && pwd)/$(basename "$target")"
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
  [ -n "$detected_tags" ] && [ -z "$tags" ] && \
    printf '\n  NOTE tests behind build tags (%s) were not run, so some entries above may be false gaps.\n' "$detected_tags"
fi

exit 0
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
bats modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats
```

Expected: PASS, 6 tests. Running the raw `.sh` requires the library on the source path — if `pgm_*` is not found, export `LIB_PATH` and source the library in `setup()` exactly as the lib suite does.

- [ ] **Step 5: Write the tldr page**

Create `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.md`:

```markdown
# pg-go-mutate

> Report which assertions a Go package's tests are missing.
> Every surviving mutant is an assertion you do not make.

- Analyze the current package:

`pg-go-mutate`

- Analyze a specific package:

`pg-go-mutate ./internal/collect`

- Include tests behind build tags:

`pg-go-mutate --tags {{contract,smoke}} ./internal/collect`

- Emit machine-readable output:

`pg-go-mutate --json ./internal/collect`

- Widen parallelism on an idle machine:

`pg-go-mutate --workers {{4}} ./internal/collect`
```

- [ ] **Step 6: Write the completions**

Create `modules/pg-go-mutate/pg-go-mutate/completions/pg-go-mutate.bash`:

```bash
_pg_go_mutate() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
    --workers | --timeout) return 0 ;;
    --tags) return 0 ;;
  esac

  if [[ "$cur" == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--tags --json --timeout --workers --help" -- "$cur")
    return 0
  fi
  mapfile -t COMPREPLY < <(compgen -d -- "$cur")
}
complete -F _pg_go_mutate pg-go-mutate
```

Create `modules/pg-go-mutate/pg-go-mutate/completions/_pg-go-mutate`:

```zsh
#compdef pg-go-mutate

_arguments -s \
  '--tags[comma-separated build tags to enable]:tags:' \
  '--json[emit the machine-readable worklist]' \
  '--timeout[per-mutant test timeout in seconds]:seconds:' \
  '--workers[parallel workers]:count:' \
  '(-h --help)'{-h,--help}'[show help]' \
  '1:target:_directories'
```

- [ ] **Step 7: Add the command derivation and module assembly**

Create `modules/pg-go-mutate/pg-go-mutate/default.nix`:

```nix
{
  mkBashScript,
  pkgs,
  pg-go-mutate-lib,
}:

mkBashScript {
  name = "pg-go-mutate";
  src = ./.;
  description = "Report which assertions a Go package's tests are missing";
  public = true;
  libraries = [ pg-go-mutate-lib ];
  # NOTE: gomu is deliberately NOT a runtimeDep. runtimeDeps are appended with
  # --suffix PATH, so an ambient ~/go/bin/gomu would win and defeat the pin. The
  # engine is bound by home/pg-go-mutate via makeWrapper --set (spec W9).
  runtimeDeps = [
    pkgs.jq
    pkgs.findutils
    pkgs.gnused
    pkgs.gnugrep
    pkgs.coreutils
  ];
  batsJobs = 4;
  testDeps = [
    pkgs.go
    pkgs.jq
  ];
}
```

Create `modules/pg-go-mutate/scripts.nix`:

```nix
# Pure script builders for the pg-go-mutate module.
# Mirrors modules/pnwf/scripts.nix and modules/ul/scripts.nix.
{
  pkgs,
  bashBuilders,
}:
let
  pg-go-mutate-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs;
  };

  pg-go-mutate = pkgs.callPackage ./pg-go-mutate {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-go-mutate-lib;
  };

  allScripts = [ pg-go-mutate ];
in
{
  inherit pg-go-mutate-lib pg-go-mutate;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-pg-go-mutate-lib = pg-go-mutate-lib.check;
    test-pg-go-mutate = pg-go-mutate.check;
  };

  check = pkgs.runCommand "test-pg-go-mutate-scripts" { } ''
    echo ${pg-go-mutate-lib.check}
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
```

- [ ] **Step 8: Commit**

```bash
git add modules/pg-go-mutate
git commit -m "feat(pg-go-mutate): add the CLI, completions, tldr page and module assembly (pg2-xulhg)"
```

---

### Task 5: Wire it into the flake and expose the feature module

**Repo:** `phillipg-nix-repo-base`

**Files:**

- Create: `home/pg-go-mutate/default.nix`
- Modify: `flake.nix` (the `let` block near the other `*Scripts`; `packages`; `checks`; `overlays.default`; `homeModules`)
- Modify: `CLAUDE.md`

**Interfaces:**

- Consumes: `scripts.nix` from Task 4; `pkgs.phillipgreenii.gomu` from Task 1 (resolved in the consumer only).
- Produces: `pkgs.pg-go-mutate` via `overlays.default`, and `homeModules.pg-go-mutate`.

- [ ] **Step 1: Write the feature module**

Create `home/pg-go-mutate/default.nix`:

```nix
# pg-go-mutate home-manager module — installs the Go mutation-testing diagnostic.
# The package is sourced from pkgs.pg-go-mutate via this flake's overlays.default.
#
# The ENGINE is bound here, not at package build time: this flake's own pkgs
# applies only overlays.gomod2nix, and overlays.default is exported for
# consumers and never applied here (the same constraint modules/pnwf/scripts.nix
# documents for pn). So pkgs.phillipgreenii.gomu is resolvable only in a
# consumer's pkgs — which is exactly where this module evaluates.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib)
    mkEnableOption
    mkPackageOption
    mkIf
    getExe
    ;
  cfg = config.phillipgreenii.pg-go-mutate;
  # --set, not --suffix: the pin must be authoritative, so an ambient
  # ~/go/bin/gomu cannot substitute itself for the engine.
  wrapped = pkgs.symlinkJoin {
    name = "pg-go-mutate-wrapped";
    paths = [ cfg.package ];
    nativeBuildInputs = [ pkgs.makeWrapper ];
    postBuild = ''
      wrapProgram $out/bin/pg-go-mutate \
        --set PG_GO_MUTATE_GOMU ${getExe cfg.gomuPackage}
    '';
  };
in
{
  options.phillipgreenii.pg-go-mutate = {
    enable = mkEnableOption "pg-go-mutate, the Go mutation-testing diagnostic";
    package = mkPackageOption pkgs "pg-go-mutate" { };
    # Forced only under mkIf cfg.enable, so a consumer that never enables the
    # feature never evaluates this and needs no overlay input.
    gomuPackage = mkPackageOption pkgs [ "phillipgreenii" "gomu" ] { };
  };

  config = mkIf cfg.enable {
    home.packages = [ wrapped ];
  };
}
```

- [ ] **Step 2: Thread the module scripts into the flake**

In `flake.nix`, beside the existing `pnwfScripts` binding in the same `let` block, add:

```nix
pgGoMutateScripts = import ./modules/pg-go-mutate/scripts.nix {
  inherit pkgs bashBuilders;
};
```

- [ ] **Step 3: Register packages, checks, overlay and homeModule**

Four edits in `flake.nix`, each mirroring how `pnwf` and `pjira` are registered:

```nix
# packages: merge the module's package list, as the other *Scripts do
# checks: merge pgGoMutateScripts.checks  (this is the hermetic bats suite
#         permitted by spec N3 -- without it the bats tests never run)
# overlays.default: add pg-go-mutate to the inherit list
overlays.default = final: _prev: {
  inherit (self.packages.${final.stdenv.hostPlatform.system})
    pn
    pn-workspace-toml-enforce
    pjira
    pg-go-mutate
    ;
};
# homeModules: add the module
homeModules = {
  pn = import ./home/pn/default.nix;
  pjira = import ./home/pjira/default.nix;
  pg-go-mutate = import ./home/pg-go-mutate/default.nix;
  # ... existing entries unchanged
};
```

- [ ] **Step 4: Verify the flake evaluates and the checks are registered**

```bash
nix flake show --json 2>/dev/null | jq -r '.checks."aarch64-darwin" | keys[]' | rg 'pg-go-mutate'
nix eval --raw .#packages.aarch64-darwin.pg-go-mutate.outPath
```

Expected: both `test-pg-go-mutate-lib` and `test-pg-go-mutate` are listed, and the package path resolves.

- [ ] **Step 5: Build the package and run its checks**

```bash
nix build .#pg-go-mutate .#checks.aarch64-darwin.test-pg-go-mutate-lib .#checks.aarch64-darwin.test-pg-go-mutate --no-link
```

Expected: PASS. Run in the background or with `timeout: 900000` — this exceeds the 2-minute default.

- [ ] **Step 6: Verify the wrapper refuses to run without an engine**

```bash
PG_GO_MUTATE_GOMU=/nonexistent ./result/bin/pg-go-mutate --help
```

Expected: `--help` still exits 0 (it must not require the engine). Then confirm a real run fails cleanly rather than with a confusing shell error.

- [ ] **Step 7: Add the CLAUDE.md pointer**

Add to `CLAUDE.md`:

```markdown
## Mutation testing (`pg-go-mutate`)

`pg-go-mutate` reports which assertions a Go package's tests are missing. Every surviving mutant is
an assertion the tests do not make. It is a diagnostic, not a gate: it always exits 0 on a completed
analysis, records nothing, and tracks no score over time. Use it when strengthening tests; see the
`go-test-gaps` skill for the workflow. Design: `docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md`.
```

- [ ] **Step 8: Commit**

```bash
git add home/pg-go-mutate flake.nix CLAUDE.md
git commit -m "feat(pg-go-mutate): expose homeModules.pg-go-mutate and wire the flake (pg2-xulhg)"
```

---

### Task 6: The agent skill

**Repo:** `phillipgreenii-nix-agent-support`

**Files:**

- Create: `claude-marketplace/pg-go-mutate/.claude-plugin/plugin.json`
- Create: `claude-marketplace/pg-go-mutate/skills/go-test-gaps/SKILL.md`
- Modify: `claude-marketplace/.claude-plugin/marketplace.json`

**Interfaces:**

- Consumes: the `pg-go-mutate` CLI contract from Task 4.
- Produces: a marketplace plugin registered in `marketplace.json`.

- [ ] **Step 1: Create the plugin manifest**

Nothing scans the directory — the marketplace builder reads `marketplace.json` and locates each plugin by its `source` path, so both files are required. Copy the shape from `claude-marketplace/pg-ccaudit/.claude-plugin/plugin.json`:

```json
{
  "name": "pg-go-mutate",
  "description": "Find missing test assertions in Go packages with pg-go-mutate",
  "version": "0.1.0",
  "defaultEnabled": true
}
```

- [ ] **Step 2: Write the skill**

Create `claude-marketplace/pg-go-mutate/skills/go-test-gaps/SKILL.md`:

````markdown
---
name: go-test-gaps
description: Use when writing, reviewing, or strengthening tests for a Go package in this workspace — including when asked to "improve test coverage", "add missing tests", "make these tests better", or after implementing a Go feature and deciding what to test. Runs pg-go-mutate to find which assertions the tests do not make, then closes those gaps. Do NOT use for non-Go code, for running the normal test suite, or to measure or track a coverage or mutation score over time.
---

# Finding Go test gaps

Line coverage cannot tell you whether a test ASSERTS anything. A test can execute
an error branch and assert nothing, and coverage still reports the line green.
`pg-go-mutate` finds exactly that: it mutates the code and reports which mutations
the tests fail to catch.

**Every surviving mutant is an assertion the tests do not make.** It is not a bug
in production code, and you MUST NOT "fix" production code to make a mutant die.

## The loop

1. Run it scoped to one package. Cost is roughly `mutants x the package's
test-suite runtime`, so never point it at a whole large module:

   ```bash
   pg-go-mutate ./internal/collect
   ```
````

2. Read the worklist. Each entry is `file`, line, the mutation, and the operator.
3. Write an assertion that would fail under that mutation.
4. Re-run and confirm **that specific mutant**, matched on `file:line:type`, is
   now killed.

## MUST

- **MUST** verify per-mutant, never by comparing survivor counts. The run-to-run
  variance is about one mutant, so a count that drops by one is indistinguishable
  from noise.
- **MUST** check for the build-tag note in the output before writing assertions.
  If a package gates tests behind custom tags (`contract`, `smoke`, `hostile`),
  mutants covered only by those tests appear as survivors. Re-run with
  `--tags contract,smoke` before treating them as real gaps.
- **MUST** treat a non-zero exit as an operational failure, not as a finding. The
  command exits 0 whenever it completed an analysis, however many mutants
  survived.
- **MUST** stop and write a test first if it reports that the package has no test
  files. Mutation testing reports missing assertions; with no tests, every mutant
  trivially survives and the worklist is meaningless.

## MUST NOT

- **MUST NOT** record the score anywhere — not in a file, not in a bead, not in a
  commit message. This is a diagnostic, not a tracked metric.
- **MUST NOT** add it to CI, a pre-commit hook, or a `checks.*` derivation. It is
  too slow and its result is not reproducible enough to gate on.
- **MUST NOT** point it at `./...`; that pattern is rejected. Pass a directory
  (walked recursively) or a single `.go` file.

## Where the highest-value gaps usually are

In this workspace's Go code, the consistent finding is that **returned errors are
almost never asserted on**. Mutating `err != nil` to `false` survived 70 times
across a sixteen-module sweep, and `error_nilify` (replacing a returned error with
`nil`) survived 44 of 48 completed cases. Branch and conditional coverage is
otherwise respectable. So when the worklist is long, start with the error paths.

````

- [ ] **Step 3: Register the plugin**

Add an entry to the `plugins` array in `claude-marketplace/.claude-plugin/marketplace.json`, matching the shape of the existing entries (a `name` and a `source` relative path pointing at `./pg-go-mutate`).

- [ ] **Step 4: Verify the marketplace still builds**

```bash
nix build .#checks.aarch64-darwin.claude-marketplace --no-link
````

Expected: PASS. If the check name differs, find it with `nix flake show --json | jq -r '.checks."aarch64-darwin"|keys[]' | rg -i market`. Run with `timeout: 900000`.

- [ ] **Step 5: Commit**

```bash
git add claude-marketplace/pg-go-mutate claude-marketplace/.claude-plugin/marketplace.json
git commit -m "feat(pg-go-mutate): add the go-test-gaps agent skill (pg2-xulhg)"
```

---

### Task 7: End-to-end validation against a real module

**Repos:** all three, as a workforest set

**Files:** none created; this task validates and records.

**Interfaces:**

- Consumes: everything from Tasks 1–6.
- Produces: a verified end-to-end run, and the reference worklist example the spec's **O8** requires.

- [ ] **Step 1: Cross-repo evaluation**

From the workspace root:

```bash
pn workspace flake-check
```

Expected: PASS across the set. This exceeds the command ceiling — run it in the background and watch it, or use `timeout: 900000`.

- [ ] **Step 2: Enable it on this machine**

Add to the machine's home-manager configuration and apply:

```nix
imports = [ inputs.phillipgreenii-nix-base.homeModules.pg-go-mutate ];
phillipgreenii.pg-go-mutate.enable = true;
```

Do **not** enable this before Tasks 1 and 5 have both landed and the locks are bumped for both — enabling earlier fails eval on the missing `pkgs.phillipgreenii.gomu`.

Applying the configuration is operator-only. Ask the operator to run the apply; do not run `darwin-rebuild switch` or `pn workspace apply` yourself.

- [ ] **Step 3: Verify the engine is pinned, not ambient**

```bash
command -v pg-go-mutate
strings "$(command -v pg-go-mutate)" 2>/dev/null | rg -o '/nix/store/[a-z0-9]*-gomu[^ ]*' | head -1
```

Expected: the wrapper embeds an absolute `/nix/store/...-gomu-.../bin/gomu` path. If it does not, the `makeWrapper --set` did not apply and the pin is defeated.

- [ ] **Step 4: Run against a module known to be healthy**

`grafana-notifier` is small and its tests pass, so it completes quickly:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/packages/grafana-notifier
pg-go-mutate .
echo "exit=$?"
```

Expected: a survivor worklist, `exit=0`. Cross-check the survivor count against `pg-go-mutate --json . | jq '.survivors|length'`.

- [ ] **Step 5: Verify the repo is left pristine**

```bash
git -C /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps status --porcelain --untracked-files=all
ls "${TMPDIR:-/tmp}"/gomu_overlay_* 2>/dev/null || echo "no overlay dirs leaked"
```

Expected: **empty** status output, and no leaked overlay dirs. A `mutation-report.json`, a `.gomu_history.json`, or a stray compiled binary appearing here means **CL1**/**CL5** are not satisfied — fix before proceeding.

- [ ] **Step 6: Verify the build guard fires on the known-broken module**

`pg-pr-zr` has a `replace => ./pg-pr-src` whose target is materialized by nix and absent from the source tree:

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/modules/pg-pr-zr
pg-go-mutate .
echo "exit=$?"
```

Expected: a **non-zero** exit and a message naming the build failure. It must **not** report a 0% score or an empty worklist — that is the exact defect the guard exists to prevent.

- [ ] **Step 7: Record the reference example and close out**

Paste the Step 4 worklist into the tldr page as the reference example (spec **O8**), commit, then:

```bash
bd update pg2-xulhg --append-notes "[e2e-verified $(date +%F)] End-to-end verified: healthy module produced a worklist and exit 0; pg-pr-zr correctly aborted non-zero on the build guard; target repo left pristine with no leaked overlay dirs." --actor "<session-id>"
bd close pg2-xulhg --reason "pg-go-mutate shipped: gomu pinned in nix-overlay, wrapper + homeModules.pg-go-mutate in nix-repo-base, go-test-gaps skill in nix-agent-support. Verified end-to-end." --actor "<session-id>"
```

Then land the set with the `integrate-branch:integrate-branch` skill, one repo at a time, and tear the workforest down with `cleanup-workforest`.

---

## Self-Review

**Spec coverage.** Walked every requirement ID. `G1`→T3/T4; `G2`→no project files created anywhere; `G3`→T5 S3, T7 S2; `G4`→T6. `N1`–`N5` are honoured by omission and asserted in the skill's MUST NOT. `E1`→T1 S7 (partial: the plan verifies the stamp at build time; the runtime startup assertion is folded into T4's engine resolution and should be added as an explicit check if T4's reviewer wants it separated). `C1`–`C8`→T4 S3. `O1`–`O8`→T3 S3, T4 S5, T7 S7. `CL1`–`CL6`→T3 S3, T7 S5. `W1`–`W4`→T1. `W5`–`W14`→T5 S1. `A1`–`A7`→T6, T5 S7. `T1`–`T8`→T2/T3/T4 test steps, T5 S3 (`checks`). `L1`–`L4`→T7.

**Gap found and accepted:** `CL5` (removing the compiled binary gomu writes into `main` package dirs) has no dedicated implementation step. It is covered incidentally because `pgm_run_engine` runs in a private cwd, but gomu's compile precheck sets `cmd.Dir` to the _target_ file's directory, so for a `main` package the binary still lands in the target tree. **T7 Step 5 will catch this**, and the fix belongs in `pgm_run_engine`. Flagged rather than silently omitted; the Task 3 reviewer should require it.

**Placeholder scan.** No TBD/TODO. The two intentionally deferred values are `vendorHash = lib.fakeHash` (Task 1 Step 4 exists specifically to replace it) and `<session-id>` in the `bd` calls, which is per-session by nature.

**Type consistency.** All `pgm_*` names are consistent between the Interfaces blocks and both the implementation and the call sites in `pg-go-mutate.sh`: `pgm_gomu_bin`, `pgm_die`, `pgm_require_go`, `pgm_validate_flags`, `pgm_has_tests`, `pgm_tests_healthy`, `pgm_detect_tags`, `pgm_run_engine`, `pgm_report_sane`, `pgm_worklist`, `pgm_worklist_json`, `_pgm_survivors_filter`.
