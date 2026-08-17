# pg-go-mutate-sweep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `pg-go-mutate-sweep`, a resumable unattended runner that analyses every Go package in the workspace one unit at a time and files one triage bead per project.

**Architecture:** A new public command inside the existing `modules/pg-go-mutate` module. All resumable state is an append-only JSONL ledger under XDG state, replayed last-record-wins; both "run this unit?" and "file this project's bead?" are replay predicates, never in-loop events. Enumeration, ledger, lock and classification live in a `.bash` library testable with no engine present; the `.sh` owns arg parsing, preflight and the drive loop. Tag detection reuses `pgm_detect_tags` from the module's existing library rather than reimplementing it.

**Tech Stack:** bash via `mkBashScript` (mkBashBuilders framework), bats, jq, GNU coreutils `timeout`, `bd` (beads), `pg-go-mutate` + pinned `gomu`.

**Spec:** `docs/superpowers/specs/2026-08-17-pg-go-mutate-sweep-design.md`

## Global Constraints

- **Every count in the spec is a snapshot. Re-derive, never hardcode.** Measured 233/17/216 then 232/17/215 hours later the same day. No test or code may assert a plan size.
- **N1 — no scores.** The ledger records STATUS ONLY. No mutant/survivor count in the ledger or in any bead. A tally of UNITS by status is permitted; a count of mutants is not.
- **N2 — never a gate.** Do not add a mutation run to CI, a git hook, or a `checks.*` derivation. The hermetic stubbed bats suite IS a `checks.*` entry.
- **`pg-go-mutate` and `bd` resolve from `PATH` and are NEVER `runtimeDeps`** (spec §9). A `runtimeDeps` entry would be a silent fallback to an unmanaged `bd`, losing `BEADS_DOLT_AUTO_START=0`.
- **`runtimeDeps` = `jq findutils gnused gnugrep coreutils`** exactly, mirroring the sibling. NOT `gawk` (the sweep never calls `pgm_has_tests`). `go` goes in `testDeps`, not `runtimeDeps`.
- **Framework source rules:** no shebang, no `set -euo pipefail`, every `.sh`/`.bash` opens with `# shellcheck shell=bash`, never hand-write `--version`, never pass `excludeShellChecks`.
- **The drive loop MUST NOT trip errexit:** always `rc=0; cmd || rc=$?`.
- **treefmt/prettier is non-idempotent on markdown.** Run treefmt to CONVERGENCE (2+ passes until clean) before committing any `.md`, or both `nix flake check` and prek go red.
- **Worktree pre-commit:** `.pre-commit-config.yaml` is a gitignored symlink into `/nix/store` absent in fresh worktrees. `ln -s "$(readlink <canonical>/.pre-commit-config.yaml)" <worktree>/.pre-commit-config.yaml` before the first commit.
- **`git add` before any `prek run`** — prek silently skips untracked files, so an un-added file yields a vacuous green.
- **Unit key format:** `<project-key>#<pkg-path>`, where project-key is workspace-root-relative. Parse on the FIRST `#`. Slugs (`/` → `__`) are for filesystem paths only.

---

### Task 1: ADR 0026 — the state contract

Recompute the ADR number before writing: `git ls-tree -r --name-only main -- docs/adr | rg -o '/(\d{4})-' -r '$1' | sort -n | tail -1` and add 1. It was 0025 (⇒ 0026) on 2026-08-17, but someone else may have taken it.

**Files:**

- Create: `docs/adr/0026-mutation-sweep-state-contract.md`
- Modify: `docs/adr/index.md`

**Interfaces:**

- Consumes: nothing.
- Produces: the ledger schema and unit-key format that Tasks 4–6 implement, and the exit-code allocation Task 2 implements.

- [ ] **Step 1: Recompute the number and confirm it is free**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/.worktrees/pg2-l36xv
printf '%04d\n' "$(( 10#$(git ls-tree -r --name-only main -- docs/adr | rg -o '/(\d{4})-' -r '$1' | sort -n | tail -1) + 1 ))"
```

Expected: `0026`. If it prints anything else, use that number throughout this task.

- [ ] **Step 2: Write the ADR**

Follow the shape of `docs/adr/0024-build-tagged-suites-need-their-own-check.md`: `# ADR-0026: <title>`, then `**Date:**`, `**Status:** Accepted`, `**Deciders:** phillipgreenii`, then `## Context`, `## Decision`, `## Consequences`.

Record exactly four decisions, because each is a compatibility surface a future reader depends on:

1. **State root and layout.** `${XDG_STATE_HOME:-$HOME/.local/state}/pg-go-mutate-sweep/` with `ledger.jsonl`, `runs/<project-slug>/<pkg-slug>.json`, `lock/`. Rationale: session-scoped scratch is reclaimed (a full sweep's output was destroyed that way on 2026-08-15).
2. **Ledger schema.** Append-only JSONL, `kind` discriminated, two kinds (`unit`, `bead`), replayed last-record-wins. Status only — no mutant counts (N1). Unit key `<project-key>#<pkg-path>`, project key workspace-root-relative.
3. **Exit-code allocation for `pg-go-mutate`.** 10 no-tests, 11 not-enumerable, 12 unhealthy, 13 environment precondition, 14 target absent. Strictly additive: every existing consumer asserts only the 0/non-zero dichotomy. This one is here because amending a shipped public command's exit contract is the most compatibility-relevant decision of the four.
4. **One triage bead per project, never an epic.** An open epic sits in `bd ready` permanently.

State the two rejected alternatives with reasons: classifying by string-matching `pg-go-mutate`'s stderr (unversioned coupling to interpolated prose), and storing state in the repo (commits machine-local progress; artifacts are regenerable).

- [ ] **Step 3: Add the index row**

Match the existing row format in `docs/adr/index.md`.

- [ ] **Step 4: Format to convergence**

```bash
nix fmt docs/adr/0026-mutation-sweep-state-contract.md docs/adr/index.md
nix fmt docs/adr/0026-mutation-sweep-state-contract.md docs/adr/index.md
git diff --stat
```

Expected: the second run changes nothing. If it does, run again until stable (prettier is non-idempotent).

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs(adr): record the mutation-sweep state contract (ADR-0026, pg2-l36xv)"
```

---

### Task 2: `pg-go-mutate` exit codes 10–14

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.sh` (guard region, lines ~126–187)
- Modify: `modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.md` (the exit-status bullet)
- Modify: `modules/pg-go-mutate/pg-go-mutate/tests/test-pg-go-mutate.bats`
- Modify: `docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md` (exit-status paragraph)

**Interfaces:**

- Consumes: nothing.
- Produces: exit codes `10` (no test files), `11` (not enumerable), `12` (unhealthy), `13` (`go` or engine absent/mismatched), `14` (target absent or not a directory). Task 7's `pgms_classify` switches on these.

- [ ] **Step 1: Write the failing tests**

Add to `tests/test-pg-go-mutate.bats`. These TIGHTEN existing `-ne 0` assertions, so also update the existing `aborts non-zero on a target that does not exist` case to assert 14.

```bash
@test "exits 14 when the target does not exist" {
  run "$SCRIPT" "$TEST_DIR/nope"
  [ "$status" -eq 14 ]
}

@test "exits 14 when the target is a file, not a directory" {
  : >"$TEST_DIR/afile.go"
  run "$SCRIPT" "$TEST_DIR/afile.go"
  [ "$status" -eq 14 ]
}

@test "exits 10 when the module has no test files" {
  mkdir -p "$TEST_DIR/m"
  cat >"$TEST_DIR/m/go.mod" <<'EOF'
module example.com/m

go 1.25.0
EOF
  printf 'package m\n\nfunc F() int { return 1 }\n' >"$TEST_DIR/m/m.go"
  run env PG_GO_MUTATE_GOMU="$TEST_DIR/stub-gomu" \
      PG_GO_MUTATE_GOMU_VERSION="$PGM_TEST_PINNED_VERSION" "$SCRIPT" "$TEST_DIR/m"
  [ "$status" -eq 10 ]
  [[ "$output" == *"has no test files"* ]]
}

@test "exits 11 when the target is not a Go module" {
  mkdir -p "$TEST_DIR/plain"
  printf 'package plain\n' >"$TEST_DIR/plain/p.go"
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
```

`$TEST_DIR/stub-gomu` must exist and report `$PGM_TEST_PINNED_VERSION` for the 10/11/12 cases, which fail before the engine runs but after `pgm_require_engine`. Reuse whatever stub-engine helper the file already defines for its other end-to-end cases; if it defines the stub inline per test, create it the same way here.

- [ ] **Step 2: Run to verify they fail**

```bash
cd modules/pg-go-mutate/pg-go-mutate && bats tests/test-pg-go-mutate.bats
```

Expected: the six new cases FAIL, each reporting `status` 1 or 2 where 10/11/12/13/14 was expected.

- [ ] **Step 3: Reallocate the codes**

In `pg-go-mutate.sh`, four edits in the guard region:

```bash
# line ~126 and ~138: the "not a directory" / "does not exist" arms
#   exit 2   ->   exit 14

# line ~155
pgm_require_go || exit 13

# line ~159
pgm_require_engine || exit 13

# lines ~173-187: the has_tests case and the healthy guard
has_tests_rc=0
pgm_has_tests "$target" || has_tests_rc=$?
case "$has_tests_rc" in
0) ;;
2)
  # pgm_has_tests already reported the enumeration failure in detail.
  exit 11
  ;;
*)
  printf 'pg-go-mutate: %s has no test files. Write a test first — mutation testing reports missing ASSERTIONS, and with no tests every mutant trivially survives.\n' "$target" >&2
  exit 10
  ;;
esac

pgm_tests_healthy "$target" || exit 12
```

Leave `pgm_validate_flags … || exit 2` and every `--flag` error at 2 — those are genuinely usage errors. Leave the post-engine `exit 1` paths (no report, insane report) alone.

- [ ] **Step 4: Run to verify they pass**

```bash
bats tests/test-pg-go-mutate.bats
```

Expected: all cases PASS, including the pre-existing ones (the `-ne 0` assertions still hold for the new codes).

- [ ] **Step 5: Update the two contracts**

In `pg-go-mutate.md`, replace the exit-status bullet with a short table listing 0, 1, 2, 10, 11, 12, 13, 14 and keep the sentence that a completed analysis is always 0 however many mutants survived.

In `docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md`, amend the exit-status paragraph in §6 to reference the allocation and note it is additive.

- [ ] **Step 6: Format to convergence and commit**

```bash
nix fmt modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.md docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md
nix fmt modules/pg-go-mutate/pg-go-mutate/pg-go-mutate.md docs/superpowers/specs/2026-08-14-pg-go-mutate-design.md
git add -A
git commit -m "feat(pg-go-mutate): allocate distinguishing guard exit codes 10-14 (pg2-l36xv)"
```

---

### Task 3: Library scaffold + state root

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.bash`
- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep-lib.bats`

**Interfaces:**

- Consumes: nothing.
- Produces: `pgms_state_root() -> path`, `pgms_ledger_path() -> path`, `pgms_runs_dir() -> path`, `pgms_slug <path> -> slug`, `pgms_unit_key <project> <pkg> -> key`, `pgms_unit_project <key> -> project`, `pgms_unit_pkg <key> -> pkg`.

- [ ] **Step 1: Write the failing test**

Isolation is not just `HOME`: `XDG_STATE_HOME` is exported in this environment, so a test that overrides only `HOME` writes to the operator's real ledger.

```bash
#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  # BOTH are load-bearing: the state root reads XDG_STATE_HOME first and it IS
  # exported in this environment, so overriding HOME alone would append to the
  # operator's real ledger and contend for the real lock.
  export HOME="$TEST_DIR" XDG_STATE_HOME="$TEST_DIR/state"
  LIB="${LIB_PATH:-$(cd "${BATS_TEST_DIRNAME}/.." && pwd)/pg-go-mutate-sweep.bash}"
  [ -d "$LIB" ] && LIB="$LIB/pg-go-mutate-sweep.bash"
  # shellcheck disable=SC1090  # runtime-resolved library path
  source "${LIB%%:*}"
}

teardown() {
  [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"
}

@test "state root honours XDG_STATE_HOME" {
  run pgms_state_root
  [ "$status" -eq 0 ]
  [ "$output" = "$TEST_DIR/state/pg-go-mutate-sweep" ]
}

@test "state root falls back to HOME/.local/state" {
  unset XDG_STATE_HOME
  run pgms_state_root
  [ "$output" = "$TEST_DIR/.local/state/pg-go-mutate-sweep" ]
}

@test "slug replaces every path separator" {
  run pgms_slug "internal/gate/deep"
  [ "$output" = "internal__gate__deep" ]
}

@test "unit key round-trips when both halves contain slashes" {
  key="$(pgms_unit_key "repo/packages/pb" "internal/gate")"
  [ "$key" = "repo/packages/pb#internal/gate" ]
  [ "$(pgms_unit_project "$key")" = "repo/packages/pb" ]
  [ "$(pgms_unit_pkg "$key")" = "internal/gate" ]
}

@test "unit key parses on the FIRST hash" {
  [ "$(pgms_unit_pkg "a/b#c/d#e")" = "c/d#e" ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd modules/pg-go-mutate/pg-go-mutate-sweep && bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: FAIL — no such file `pg-go-mutate-sweep.bash`.

- [ ] **Step 3: Write the implementation**

```bash
# shellcheck shell=bash
#
# pg-go-mutate-sweep shared library: plan enumeration, ledger, lock, and status
# classification. Sourced by pg-go-mutate-sweep.sh and directly by bats.
#
# Composed AFTER pg-go-mutate-lib, so pgm_detect_tags is in scope here.

pgms_die() {
  printf 'pg-go-mutate-sweep: %s\n' "$1" >&2
  return "${2:-1}"
}

# The state root deliberately reads XDG_STATE_HOME first: a session-scoped
# scratch directory is reclaimed out from under a long sweep (observed
# 2026-08-15, a full sweep's output destroyed two days after it ran).
pgms_state_root() {
  printf '%s/pg-go-mutate-sweep\n' "${XDG_STATE_HOME:-$HOME/.local/state}"
}

pgms_ledger_path() { printf '%s/ledger.jsonl\n' "$(pgms_state_root)"; }
pgms_runs_dir() { printf '%s/runs\n' "$(pgms_state_root)"; }

# Slugs are for FILESYSTEM paths only, never for keys: the mapping is not
# injective, which is why pgms_check_slug_collisions exists.
pgms_slug() { printf '%s\n' "${1//\//__}"; }

# '#' separates the two halves because it cannot appear in either: no path in
# this workspace contains one, and Go rejects it in an import-path element, so a
# '#' directory could never be a package `go list` enumerates.
pgms_unit_key() { printf '%s#%s\n' "$1" "$2"; }
pgms_unit_project() { printf '%s\n' "${1%%#*}"; }
pgms_unit_pkg() { printf '%s\n' "${1#*#}"; }
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): add library scaffold, state root and unit keys (pg2-l36xv)"
```

---

### Task 4: Plan enumeration

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.bash`
- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep-lib.bats`

**Interfaces:**

- Consumes: `pgms_slug`, `pgms_unit_key` (Task 3).
- Produces: `pgms_find_projects <root>` (newline project keys), `pgms_find_units <root> <project> ` (newline pkg paths, non-nested), `pgms_plan <root>` (newline unit keys, ordered), `pgms_check_slug_collisions <root>` (returns 2 and names both paths on collision).

- [ ] **Step 1: Write the failing test**

Build the fixture in `$TEST_DIR` — never assert a count against the real workspace (Global Constraints).

```bash
_mkmod() { # <dir> <module-path>
  mkdir -p "$1"
  printf 'module %s\n\ngo 1.25.0\n' "$2" >"$1/go.mod"
}

@test "projects exclude vendor, node_modules, worktrees, workforests, fixtures and testdata" {
  _mkmod "$TEST_DIR/ws/repo/a" example.com/a
  _mkmod "$TEST_DIR/ws/repo/vendor/v" example.com/v
  _mkmod "$TEST_DIR/ws/repo/node_modules/n" example.com/n
  _mkmod "$TEST_DIR/ws/repo/.worktrees/w" example.com/w
  _mkmod "$TEST_DIR/ws/repo/.workforests/f" example.com/f
  _mkmod "$TEST_DIR/ws/repo/lib/tests/fixtures/x" example.com/x
  _mkmod "$TEST_DIR/ws/repo/pkg/testdata/t" example.com/t
  run pgms_find_projects "$TEST_DIR/ws"
  [ "$output" = "repo/a" ]
}

@test "units exclude dirs nested under another candidate" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/outer/inner" "$TEST_DIR/ws/p/leaf"
  printf 'package outer\n' >"$TEST_DIR/ws/p/outer/o.go"
  printf 'package inner\n' >"$TEST_DIR/ws/p/outer/inner/i.go"
  printf 'package leaf\n' >"$TEST_DIR/ws/p/leaf/l.go"
  run pgms_find_units "$TEST_DIR/ws" p
  [ "$(printf '%s\n' "$output" | sort | tr '\n' ' ')" = "leaf outer " ]
}

@test "a dir holding only _test.go files is not a candidate" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/only"
  printf 'package only\n' >"$TEST_DIR/ws/p/only/o_test.go"
  run pgms_find_units "$TEST_DIR/ws" p
  [ -z "$output" ]
}

@test "plan orders cheap projects first and subtree units last" {
  _mkmod "$TEST_DIR/ws/big" example.com/big
  mkdir -p "$TEST_DIR/ws/big/aaa" "$TEST_DIR/ws/big/sub/deep"
  printf 'package big\n' >"$TEST_DIR/ws/big/aaa/a.go"
  printf 'package sub\n' >"$TEST_DIR/ws/big/sub/s.go"
  printf 'package deep\n' >"$TEST_DIR/ws/big/sub/deep/d.go"
  _mkmod "$TEST_DIR/ws/small" example.com/small
  mkdir -p "$TEST_DIR/ws/small/one"
  printf 'package one\n' >"$TEST_DIR/ws/small/one/o.go"
  run pgms_plan "$TEST_DIR/ws"
  # small (1 candidate) precedes big (2 kept); within big, leaf aaa precedes subtree sub
  [ "$(printf '%s\n' "$output" | head -1)" = "small#one" ]
  [ "$(printf '%s\n' "$output" | sed -n 2p)" = "big#aaa" ]
  [ "$(printf '%s\n' "$output" | sed -n 3p)" = "big#sub" ]
}

@test "plan is deterministic across invocations" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/b" "$TEST_DIR/ws/p/a"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  first="$(pgms_plan "$TEST_DIR/ws")"
  [ "$first" = "$(pgms_plan "$TEST_DIR/ws")" ]
}

@test "slug collision aborts naming both paths" {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/a/b__c" "$TEST_DIR/ws/p/a__b/c"
  printf 'package x\n' >"$TEST_DIR/ws/p/a/b__c/x.go"
  printf 'package y\n' >"$TEST_DIR/ws/p/a__b/c/y.go"
  run pgms_check_slug_collisions "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
  [[ "$output" == *"a/b__c"* ]]
  [[ "$output" == *"a__b/c"* ]]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: the six new cases FAIL with `pgms_find_projects: command not found` etc.

- [ ] **Step 3: Write the implementation**

```bash
# The exclusion set is shared by project and package discovery. Each entry is
# load-bearing: without the fixtures prune the workspace yields 19 deliberately
# broken fixture modules instead of 16 real projects, .workforests holds full
# duplicate checkouts of every repo, and node_modules can contain a stray .go
# file that is harmless only because no go.mod sits above it.
_PGMS_PRUNE=(.git vendor node_modules .worktrees .workforests fixtures testdata)

_pgms_prune_args() {
  local p out=()
  for p in "${_PGMS_PRUNE[@]}"; do out+=(-name "$p" -prune -o); done
  printf '%s\n' "${out[@]}"
}

_pgms_is_pruned() { # <relative-path>
  local p
  for p in "${_PGMS_PRUNE[@]}"; do
    case "/$1/" in */"$p"/*) return 0 ;; esac
  done
  return 1
}

# Project keys are workspace-root-relative paths, not basenames: a basename is
# not unique across six repos, and the key's first component is the repo label
# a project's bead carries.
pgms_find_projects() {
  local root="$1" f rel
  [ -d "$root" ] || return 0
  while IFS= read -r f; do
    rel="${f#"$root"/}"
    rel="${rel%/go.mod}"
    _pgms_is_pruned "$rel" && continue
    printf '%s\n' "$rel"
  done < <(find "$root" -type f -name go.mod 2>/dev/null) | sort
}

# Candidate dirs directly contain a non-test .go file. Nested candidates are
# dropped because pg-go-mutate walks its PATH argument RECURSIVELY, so an
# ancestor's run already covers its descendants.
_pgms_candidates() { # <project-abs-dir>
  local dir="$1" f rel d
  while IFS= read -r f; do
    case "$f" in *_test.go) continue ;; esac
    d="$(dirname "$f")"
    rel="${d#"$dir"}"
    rel="${rel#/}"
    [ -z "$rel" ] && rel="."
    _pgms_is_pruned "$rel" && continue
    printf '%s\n' "$rel"
  done < <(find "$dir" -type f -name '*.go' 2>/dev/null) | sort -u
}

pgms_find_units() { # <root> <project-key>
  local root="$1" proj="$2" dirs x y keep
  dirs="$(_pgms_candidates "$root/$proj")"
  [ -n "$dirs" ] || return 0
  while IFS= read -r x; do
    [ -n "$x" ] || continue
    keep=1
    while IFS= read -r y; do
      [ -n "$y" ] || continue
      [ "$x" = "$y" ] && continue
      case "$x" in "$y"/*) keep=0; break ;; esac
    done <<<"$dirs"
    [ "$keep" -eq 1 ] && printf '%s\n' "$x"
  done <<<"$dirs"
}

# A unit is a SUBTREE when another candidate lives beneath it. Subtree units sort
# last within their project so the cheap leaves bank findings first.
_pgms_is_subtree() { # <project-abs-dir> <pkg-rel>
  local dir="$1" pkg="$2" c
  while IFS= read -r c; do
    [ "$c" = "$pkg" ] && continue
    case "$c" in "$pkg"/*) return 0 ;; esac
  done < <(_pgms_candidates "$dir")
  return 1
}

# Ordering: projects ascending by candidate count then key; within a project,
# leaves lexicographically then subtree units lexicographically. Deterministic,
# which is what makes resume stable across invocations.
pgms_plan() { # <root>
  local root="$1" proj units n leaves=() subs=() u
  while IFS= read -r proj; do
    [ -n "$proj" ] || continue
    units="$(pgms_find_units "$root" "$proj")"
    n="$(printf '%s\n' "$units" | grep -c . || true)"
    printf '%s\t%s\n' "$n" "$proj"
  done < <(pgms_find_projects "$root") | sort -n -k1,1 -k2,2 | while IFS=$'\t' read -r n proj; do
    leaves=(); subs=()
    while IFS= read -r u; do
      [ -n "$u" ] || continue
      if _pgms_is_subtree "$root/$proj" "$u"; then subs+=("$u"); else leaves+=("$u"); fi
    done < <(pgms_find_units "$root" "$proj")
    for u in $(printf '%s\n' "${leaves[@]+"${leaves[@]}"}" | sort); do
      [ -n "$u" ] && pgms_unit_key "$proj" "$u"
    done
    for u in $(printf '%s\n' "${subs[@]+"${subs[@]}"}" | sort); do
      [ -n "$u" ] && pgms_unit_key "$proj" "$u"
    done
  done
}

# Slugs are not injective, so a collision would silently overwrite one unit's
# report with another's. Detected at PLAN time and fatal.
pgms_check_slug_collisions() { # <root>
  local root="$1" key proj pkg slug seen_file rc=0 prev
  seen_file="$(mktemp)"
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    proj="$(pgms_unit_project "$key")"
    pkg="$(pgms_unit_pkg "$key")"
    slug="$(pgms_slug "$proj")/$(pgms_slug "$pkg")"
    prev="$(grep -F "	$slug" "$seen_file" 2>/dev/null | head -1 | cut -f1)"
    if [ -n "$prev" ]; then
      printf 'pg-go-mutate-sweep: slug collision: %s and %s both map to %s\n' "$prev" "$key" "$slug" >&2
      rc=2
    fi
    printf '%s\t%s\n' "$key" "$slug" >>"$seen_file"
  done < <(pgms_plan "$root")
  rm -f "$seen_file"
  return "$rc"
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): enumerate projects, units and the ordered plan (pg2-l36xv)"
```

---

### Task 5: Ledger append and replay

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.bash`
- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep-lib.bats`

**Interfaces:**

- Consumes: `pgms_ledger_path` (Task 3).
- Produces: `pgms_append_record <json>`, `pgms_valid_lines` (stdin filter), `pgms_replay_units` (prints `unit<TAB>status`, last wins), `pgms_unit_status <unit>`, `pgms_unit_needs_run <unit> <retry-spec>`.

- [ ] **Step 1: Write the failing test**

```bash
_ledger() { mkdir -p "$(pgms_state_root)"; cat >"$(pgms_ledger_path)"; }

@test "replay keeps the LAST record per unit" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
{"kind":"unit","unit":"p#a","status":"done"}
EOF
  run pgms_unit_status "p#a"
  [ "$output" = "done" ]
}

@test "replay tolerates a truncated final line" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done"}
{"kind":"unit","unit":"p#b","sta
EOF
  run pgms_unit_status "p#a"
  [ "$output" = "done" ]
  run pgms_unit_status "p#b"
  [ -z "$output" ]
}

@test "replay ignores bead records when building unit state" {
  _ledger <<'EOF'
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed"}
{"kind":"unit","unit":"p#a","status":"done"}
EOF
  run pgms_replay_units
  [ "$(printf '%s\n' "$output" | grep -c .)" -eq 1 ]
  [[ "$output" == "p#a	done" ]]
}

@test "a recorded unit does not need a run by default" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
EOF
  run pgms_unit_needs_run "p#a" ""
  [ "$status" -eq 1 ]
}

@test "an unrecorded unit needs a run" {
  mkdir -p "$(pgms_state_root)"; : >"$(pgms_ledger_path)"
  run pgms_unit_needs_run "p#zzz" ""
  [ "$status" -eq 0 ]
}

@test "--retry selects by status and 'transient' expands to the cohort" {
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed"}
{"kind":"unit","unit":"p#b","status":"done"}
{"kind":"unit","unit":"p#c","status":"timeout"}
EOF
  run pgms_unit_needs_run "p#a" "failed";    [ "$status" -eq 0 ]
  run pgms_unit_needs_run "p#b" "failed";    [ "$status" -eq 1 ]
  run pgms_unit_needs_run "p#c" "transient"; [ "$status" -eq 0 ]
  run pgms_unit_needs_run "p#b" "transient"; [ "$status" -eq 1 ]
}

@test "append writes one line and creates the state root" {
  pgms_append_record '{"kind":"unit","unit":"p#a","status":"done"}'
  [ "$(grep -c . "$(pgms_ledger_path)")" -eq 1 ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: the seven new cases FAIL with `pgms_append_record: command not found` etc.

- [ ] **Step 3: Write the implementation**

```bash
# Statuses whose re-attempt is likely to differ. This is a REAL partition, and
# `--retry transient` is its shorthand; by default NO status is re-attempted, so
# a re-run always makes forward progress and never loops on a broken unit.
_PGMS_TRANSIENT="not-enumerable unhealthy vanished inconclusive timeout failed"

pgms_append_record() { # <json-line>
  local root
  root="$(pgms_state_root)"
  mkdir -p "$root"
  printf '%s\n' "$1" >>"$(pgms_ledger_path)"
}

# Per-line validation, not `jq -s`: a kill -9 can truncate the final line, and
# slurping would then fail over the WHOLE ledger rather than skipping one line.
pgms_valid_lines() {
  local line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '%s' "$line" | jq -e 'type == "object"' >/dev/null 2>&1 && printf '%s\n' "$line"
  done
}

pgms_replay_units() {
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs 'map(select(.kind == "unit")) | group_by(.unit) | map(last)
            | .[] | "\(.unit)\t\(.status)"'
}

pgms_unit_status() { # <unit-key>
  pgms_replay_units | awk -F'\t' -v u="$1" '$1 == u { print $2 }' | tail -1
}

pgms_unit_needs_run() { # <unit-key> <retry-spec>
  local unit="$1" spec="${2:-}" status s
  status="$(pgms_unit_status "$unit")"
  [ -z "$status" ] && return 0
  [ -z "$spec" ] && return 1
  if [ "$spec" = "transient" ]; then
    for s in $_PGMS_TRANSIENT; do [ "$s" = "$status" ] && return 0; done
    return 1
  fi
  printf ',%s,' "$spec" | grep -qF ",$status," && return 0
  return 1
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): append-only ledger with last-record-wins replay (pg2-l36xv)"
```

---

### Task 6: The bead-due predicate

This is the task the whole resume design turns on. An in-loop "was that the last unit?" event files nothing for a project whose units are all already recorded — the exact loss it was meant to prevent.

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.bash`
- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep-lib.bats`

**Interfaces:**

- Consumes: `pgms_find_units`, `pgms_unit_key` (Task 4), `pgms_replay_units`, `pgms_valid_lines` (Task 5).
- Produces: `pgms_bead_due <root> <project>` returning 0 (due) / 1 (not due), and `pgms_bead_action <root> <project>` printing `file` or `amend`.

- [ ] **Step 1: Write the failing test**

```bash
_proj_with_two_units() {
  _mkmod "$TEST_DIR/ws/p" example.com/p
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/p/b"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
}

@test "bead is not due while a unit is unrecorded" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "bead is due once every unit is recorded" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "file" ]
}

@test "bead is due on a fresh invocation that runs ZERO units" {
  # The lost-project regression: the process died after the last unit record but
  # before bd create, so a resumed sweep runs nothing for this project.
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
}

@test "bead is not due once a bead record exists" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed","finished":"2026-08-17T10:02:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "bead is due again when a unit record is NEWER than the bead record" {
  # Without this a project whose units all failed keeps a bead of failures and
  # the worklists a later --retry produces can never reach any bead.
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"failed","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"failed","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-x","action":"filed","finished":"2026-08-17T10:02:00-04:00"}
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T11:00:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 0 ]
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "amend" ]
}

@test "a suppressed marker has no id, so the action is file rather than amend" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","action":"suppressed","finished":"2026-08-17T10:02:00-04:00"}
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T11:00:00-04:00"}
EOF
  run pgms_bead_action "$TEST_DIR/ws" p
  [ "$output" = "file" ]
}

@test "a suppressed marker keeps the bead not-due on a plain re-run" {
  _proj_with_two_units
  _ledger <<'EOF'
{"kind":"unit","unit":"p#a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"unit","unit":"p#b","status":"done","finished":"2026-08-17T10:01:00-04:00"}
{"kind":"bead","project":"p","action":"suppressed","finished":"2026-08-17T10:02:00-04:00"}
EOF
  run pgms_bead_due "$TEST_DIR/ws" p
  [ "$status" -eq 1 ]
}

@test "a zero-unit project is never due" {
  _mkmod "$TEST_DIR/ws/empty" example.com/empty
  run pgms_bead_due "$TEST_DIR/ws" empty
  [ "$status" -eq 1 ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: the eight new cases FAIL with `pgms_bead_due: command not found`.

- [ ] **Step 3: Write the implementation**

```bash
_pgms_latest_bead() { # <project> -> the whole record, or empty
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs --arg p "$1" 'map(select(.kind == "bead" and .project == $p))
                         | if length == 0 then empty else (last | @json) end'
}

_pgms_newest_unit_stamp() { # <project> -> RFC3339 or empty
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || return 0
  pgms_valid_lines <"$ledger" |
    jq -rs --arg p "$1" '[ .[] | select(.kind == "unit" and (.project == $p))
                           | .finished // "" ] | max // empty'
}

# Predicate 2, evaluated by REPLAY at startup and after every unit record --
# never as an in-loop "last unit" event. The event form files nothing for a
# project whose units are all already recorded, which is the loss this closes.
pgms_bead_due() { # <root> <project>
  local root="$1" proj="$2" units u bead bead_stamp newest
  units="$(pgms_find_units "$root" "$proj")"
  [ -n "$units" ] || return 1                  # a zero-unit project is never due
  while IFS= read -r u; do
    [ -n "$u" ] || continue
    [ -z "$(pgms_unit_status "$(pgms_unit_key "$proj" "$u")")" ] && return 1
  done <<<"$units"

  bead="$(_pgms_latest_bead "$proj")"
  [ -z "$bead" ] && return 0                   # all recorded, never filed

  bead_stamp="$(printf '%s' "$bead" | jq -r '.finished // ""')"
  newest="$(_pgms_newest_unit_stamp "$proj")"
  # Strictly newer, so appending the bead record makes this false again and the
  # amend cannot loop. A same-second tie resolves in the terminating direction.
  [ -n "$newest" ] && [ -n "$bead_stamp" ] && [[ "$newest" > "$bead_stamp" ]] && return 0
  return 1
}

# A suppressed marker carries no id, so there is nothing to comment on: file.
pgms_bead_action() { # <root> <project> -> file | amend
  local bead id
  bead="$(_pgms_latest_bead "$2")"
  id="$(printf '%s' "$bead" | jq -r '.bead // ""' 2>/dev/null || true)"
  if [ -n "$bead" ] && [ -n "$id" ]; then printf 'amend\n'; else printf 'file\n'; fi
}
```

Note: the unit records the tests use carry no `project` field, so `_pgms_newest_unit_stamp` must derive it. Add `"project"` to every unit record written in Task 9 (the record shape in the spec includes it), and in this helper fall back to deriving it from the key:

```bash
    map(select(.kind == "unit"))
    | map(select((.project // (.unit | split("#")[0])) == $p))
```

Use that filter form so both shapes work.

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): bead-due replay predicate with an amend path (pg2-l36xv)"
```

---

### Task 7: Lock, classification and tag gating

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.bash`
- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep-lib.bats`

**Interfaces:**

- Consumes: `pgms_state_root` (Task 3).
- Produces: `pgms_lock_acquire` (0 held / 3 refused), `pgms_lock_release`, `pgms_classify <exit> [report] [threshold]` printing one of `done no-tests not-enumerable unhealthy vanished inconclusive timeout failed fatal`, `pgms_apply_tags <abs-dir> <allowlist>` printing `applied<TAB>withheld`.

- [ ] **Step 1: Write the failing test**

```bash
@test "lock acquires, refuses a live holder, and releases" {
  run pgms_lock_acquire
  [ "$status" -eq 0 ]
  [ -d "$(pgms_state_root)/lock" ]
  run pgms_lock_acquire
  [ "$status" -eq 3 ]
  pgms_lock_release
  [ ! -d "$(pgms_state_root)/lock" ]
}

@test "lock reclaims a stale holder" {
  mkdir -p "$(pgms_state_root)/lock"
  # PID 99999 is not running; the stamp format is "<pid> <iso8601>"
  printf '99999 2026-08-17T10:00:00-04:00\n' >"$(pgms_state_root)/lock/holder"
  run pgms_lock_acquire
  [ "$status" -eq 0 ]
  [ "$(awk '{print $1}' "$(pgms_state_root)/lock/holder")" = "$$" ]
}

@test "a leftover lock.stale directory does not corrupt the reclaim" {
  mkdir -p "$(pgms_state_root)/lock" "$(pgms_state_root)/lock.stale.leftover"
  printf '99999 2026-08-17T10:00:00-04:00\n' >"$(pgms_state_root)/lock/holder"
  run pgms_lock_acquire
  [ "$status" -eq 0 ]
  # the reclaimed directory is removed, not accumulated
  [ -z "$(find "$(pgms_state_root)" -maxdepth 1 -name 'lock.stale.*' -newer "$(pgms_state_root)/lock" 2>/dev/null)" ]
}

@test "classification maps every exit code" {
  [ "$(pgms_classify 10)"  = "no-tests" ]
  [ "$(pgms_classify 11)"  = "not-enumerable" ]
  [ "$(pgms_classify 12)"  = "unhealthy" ]
  [ "$(pgms_classify 14)"  = "vanished" ]
  [ "$(pgms_classify 124)" = "timeout" ]
  [ "$(pgms_classify 13)"  = "fatal" ]
  [ "$(pgms_classify 2)"   = "fatal" ]
  [ "$(pgms_classify 1)"   = "failed" ]
  [ "$(pgms_classify 99)"  = "failed" ]
}

@test "137 is failed, NOT timeout" {
  # timeout(1) returns 124 whether or not it escalated to KILL; 137 means
  # timeout ITSELF was killed, e.g. OOM.
  [ "$(pgms_classify 137)" = "failed" ]
}

@test "exit 0 with a low timed-out fraction is done" {
  cat >"$TEST_DIR/r.json" <<'EOF'
{"statistics":{"killed":90,"survived":8,"notViable":1,"timedOut":1,"errors":0}}
EOF
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "done" ]
}

@test "exit 0 with a high timed-out fraction is inconclusive" {
  cat >"$TEST_DIR/r.json" <<'EOF'
{"statistics":{"killed":0,"survived":0,"notViable":0,"timedOut":100,"errors":0}}
EOF
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "inconclusive" ]
}

@test "a zero denominator is failed, not a division error" {
  # A report can carry totalMutants > 0 with an empty statistics object and still
  # pass pgm_report_sane, so the fraction must be guarded.
  printf '{"statistics":{}}\n' >"$TEST_DIR/r.json"
  [ "$(pgms_classify 0 "$TEST_DIR/r.json" 50)" = "failed" ]
}

@test "tags are gated by the allowlist and default to none applied" {
  pgm_detect_tags() { printf 'contract,hostile\n'; }
  run pgms_apply_tags "$TEST_DIR" ""
  [ "$output" = "	contract,hostile" ]
  run pgms_apply_tags "$TEST_DIR" "contract"
  [ "$output" = "contract	hostile" ]
}

@test "no detected tags yields both fields empty" {
  pgm_detect_tags() { printf '\n'; }
  run pgms_apply_tags "$TEST_DIR" "contract"
  [ "$output" = "	" ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: the ten new cases FAIL with `pgms_lock_acquire: command not found` etc.

- [ ] **Step 3: Write the implementation**

```bash
# flock(1) is absent on darwin, so the lock is an atomic mkdir stamped with the
# holder's pid and start time.
pgms_lock_acquire() {
  local root lock pid stale
  root="$(pgms_state_root)"
  lock="$root/lock"
  mkdir -p "$root"
  if mkdir "$lock" 2>/dev/null; then
    printf '%s %s\n' "$$" "$(date -Iseconds)" >"$lock/holder"
    return 0
  fi
  pid="$(awk '{print $1}' "$lock/holder" 2>/dev/null || true)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    printf 'pg-go-mutate-sweep: another sweep holds the lock (pid %s). Use --force-unlock if it is wedged.\n' "$pid" >&2
    return 3
  fi
  # Reclaim by RENAME onto a unique, NON-EXISTENT destination. A plain
  # `mv lock lock.stale.$$` would move `lock` INSIDE a leftover directory of that
  # name and still return 0, so "proceed only if the rename succeeded" would stop
  # meaning what it says. Exactly one racer can win an atomic rename(2).
  stale="$(mktemp -d "$root/lock.stale.XXXXXX")"
  rmdir "$stale"
  if mv "$lock" "$stale" 2>/dev/null; then
    rm -rf "$stale"
    if mkdir "$lock" 2>/dev/null; then
      printf '%s %s\n' "$$" "$(date -Iseconds)" >"$lock/holder"
      return 0
    fi
  fi
  printf 'pg-go-mutate-sweep: lost the lock-reclaim race\n' >&2
  return 3
}

pgms_lock_release() { rm -rf -- "$(pgms_state_root)/lock"; }

# Classification is by EXIT CODE, never by matching the child's prose: a TERM
# inside pgm_run_engine makes pg-go-mutate report "the engine produced no report
# (exit 143)" and exit 1, byte-identical to a genuine failure.
pgms_classify() { # <exit> [json-report] [threshold-percent]
  local rc="$1" report="${2:-}" thr="${3:-50}" total timed
  case "$rc" in
  10) printf 'no-tests\n'; return 0 ;;
  11) printf 'not-enumerable\n'; return 0 ;;
  12) printf 'unhealthy\n'; return 0 ;;
  14) printf 'vanished\n'; return 0 ;;
  124) printf 'timeout\n'; return 0 ;;
  13 | 2) printf 'fatal\n'; return 0 ;;
  0) ;;
  *) printf 'failed\n'; return 0 ;;
  esac
  [ -n "$report" ] && [ -f "$report" ] || { printf 'failed\n'; return 0; }
  total="$(jq -r '[.statistics.killed, .statistics.survived, .statistics.notViable,
                   .statistics.timedOut, .statistics.errors] | map(. // 0) | add' \
           "$report" 2>/dev/null || printf '0')"
  timed="$(jq -r '.statistics.timedOut // 0' "$report" 2>/dev/null || printf '0')"
  case "$total" in '' | *[!0-9]*) total=0 ;; esac
  case "$timed" in '' | *[!0-9]*) timed=0 ;; esac
  # Guarded: an empty statistics object passes pgm_report_sane, and an unguarded
  # division would abort a unit the engine considered acceptable.
  [ "$total" -le 0 ] && { printf 'failed\n'; return 0; }
  if [ $((timed * 100 / total)) -gt "$thr" ]; then printf 'inconclusive\n'; else printf 'done\n'; fi
}

# Detection is pgm_detect_tags (reused, never reimplemented -- its header records
# why a naive //go:build scan is wrong). APPLICATION is opt-in: a tag-gated suite
# runs once per mutant, and these suites drive real bd/git/tmux/daemons.
pgms_apply_tags() { # <abs-dir> <allowlist-csv> -> "applied<TAB>withheld"
  local dir="$1" allow="${2:-}" detected t applied=() withheld=() det=()
  detected="$(pgm_detect_tags "$dir")"
  detected="${detected//[[:space:]]/}"
  if [ -z "$detected" ]; then printf '\t\n'; return 0; fi
  IFS=, read -r -a det <<<"$detected"
  for t in "${det[@]}"; do
    [ -n "$t" ] || continue
    if [ -n "$allow" ] && printf ',%s,' "$allow" | grep -qF ",$t,"; then
      applied+=("$t")
    else
      withheld+=("$t")
    fi
  done
  printf '%s\t%s\n' \
    "$(IFS=,; printf '%s' "${applied[*]+"${applied[*]}"}")" \
    "$(IFS=,; printf '%s' "${withheld[*]+"${withheld[*]}"}")"
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep-lib.bats
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): lock, exit-code classification and tag gating (pg2-l36xv)"
```

---

### Task 8: The command — args, help, preflight, `--dry-run`

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.sh`
- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep.bats`

**Interfaces:**

- Consumes: every `pgms_*` function from Tasks 3–7.
- Produces: the CLI. Exit `0` completed, `2` usage or slug collision, `3` lock held, `4` fatal abort.

- [ ] **Step 1: Write the failing test**

Replicate the sibling's `SCRIPT_UNDER_TEST` wrapper pattern — the raw `.sh` has no `source` line of its own, because the builder prepends both libraries at build time.

```bash
#!/usr/bin/env bats

setup() {
  TEST_DIR="$(mktemp -d)"
  export TEST_DIR
  export HOME="$TEST_DIR" XDG_STATE_HOME="$TEST_DIR/state" GOCACHE="$TEST_DIR/go-build"
  export BIN="$TEST_DIR/bin"
  mkdir -p "$BIN"
  PATH="$BIN:$PATH"; export PATH

  if [ -n "${SCRIPT_UNDER_TEST:-}" ]; then
    SCRIPT="$SCRIPT_UNDER_TEST"
  else
    # Assemble what the builder assembles: pg-go-mutate-lib, then this command's
    # own library, then the .sh -- in that composition order.
    local pgm_lib sweep_lib
    pgm_lib="$(cd "${BATS_TEST_DIRNAME}/../../lib" && pwd)/pg-go-mutate-lib.bash"
    sweep_lib="$(cd "${BATS_TEST_DIRNAME}/.." && pwd)/pg-go-mutate-sweep.bash"
    cat >"$TEST_DIR/sweep-wrapper" <<WRAPPER
#!/usr/bin/env bash
set -euo pipefail
source "$pgm_lib"
source "$sweep_lib"
source "${BATS_TEST_DIRNAME}/../pg-go-mutate-sweep.sh"
WRAPPER
    chmod +x "$TEST_DIR/sweep-wrapper"
    SCRIPT="$TEST_DIR/sweep-wrapper"
  fi
  export SCRIPT
}

teardown() { [ -n "${TEST_DIR:-}" ] && rm -rf "$TEST_DIR"; }

_stub() { # <name> <body>
  printf '#!/usr/bin/env bash\n%s\n' "$2" >"$BIN/$1"
  chmod +x "$BIN/$1"
}

_ws_one_unit() {
  mkdir -p "$TEST_DIR/ws/p/a"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
}

@test "--help exits 0 and documents every flag" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  for f in --root --only --unit-timeout --unit-kill-grace --mutant-timeout --workers \
           --auto-tags --retry --redo --dry-run --no-beads --force-unlock; do
    [[ "$output" == *"$f"* ]] || { echo "missing $f"; false; }
  done
}

@test "--help documents that a unit may be a directory SUBTREE" {
  run "$SCRIPT" --help
  [[ "$output" == *"subtree"* || "$output" == *"SUBTREE"* ]]
}

@test "rejects an unknown flag with exit 2" {
  run "$SCRIPT" --nope
  [ "$status" -eq 2 ]
}

@test "rejects an invalid --auto-tags value at parse time" {
  run "$SCRIPT" --auto-tags 'x -toolexec=/bin/sh' --dry-run --root "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
}

@test "preflight fails when pg-go-mutate is absent" {
  _ws_one_unit
  _stub bd 'exit 0'
  run env PATH="$BIN:/usr/bin:/bin" "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
  [[ "$output" == *"pg-go-mutate"* ]]
}

@test "preflight fails when bd is absent" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 0'
  run env PATH="$BIN:/usr/bin:/bin" "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
  [[ "$output" == *"bd"* ]]
}

@test "--dry-run prints the plan and runs nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/ran"; exit 0'
  _stub bd 'exit 0'
  run "$SCRIPT" --root "$TEST_DIR/ws" --dry-run
  [ "$status" -eq 0 ]
  [[ "$output" == *"p#a"* ]]
  [ ! -f "$TEST_DIR/ran" ]
}

@test "a slug collision aborts with exit 2 and releases the lock" {
  mkdir -p "$TEST_DIR/ws/p/a/b__c" "$TEST_DIR/ws/p/a__b/c"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package x\n' >"$TEST_DIR/ws/p/a/b__c/x.go"
  printf 'package y\n' >"$TEST_DIR/ws/p/a__b/c/y.go"
  _stub pg-go-mutate 'exit 0'
  _stub bd 'exit 0'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 2 ]
  [ ! -d "$TEST_DIR/state/pg-go-mutate-sweep/lock" ]
}

@test "refuses to run while another sweep holds the lock" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 0'
  _stub bd 'exit 0'
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep/lock"
  printf '%s %s\n' "$$" "2026-08-17T10:00:00-04:00" \
    >"$TEST_DIR/state/pg-go-mutate-sweep/lock/holder"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 3 ]
}

@test "--force-unlock breaks a live-looking lock" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep/lock"
  printf '%s %s\n' "$$" "2026-08-17T10:00:00-04:00" \
    >"$TEST_DIR/state/pg-go-mutate-sweep/lock/holder"
  run "$SCRIPT" --root "$TEST_DIR/ws" --force-unlock
  [ "$status" -eq 0 ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd modules/pg-go-mutate/pg-go-mutate-sweep && bats tests/test-pg-go-mutate-sweep.bats
```

Expected: FAIL — no `pg-go-mutate-sweep.sh`.

- [ ] **Step 3: Write the implementation**

```bash
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
  -h | --help) show_help; exit 0 ;;
  --root) root="${2:?--root needs a value}"; shift 2 ;;
  --only) only_projects+=("${2:?--only needs a value}"); shift 2 ;;
  --unit-timeout) unit_timeout="${2:?--unit-timeout needs a value}"; shift 2 ;;
  --unit-kill-grace) kill_grace="${2:?--unit-kill-grace needs a value}"; shift 2 ;;
  --mutant-timeout) mutant_timeout="${2:?--mutant-timeout needs a value}"; shift 2 ;;
  --workers) workers="${2:?--workers needs a value}"; shift 2 ;;
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
  --retry) retry_spec="${2:?--retry needs a value}"; shift 2 ;;
  --redo) redo_key="${2:?--redo needs a value}"; shift 2 ;;
  --dry-run) dry_run=1; shift ;;
  --no-beads) no_beads=1; shift ;;
  --force-unlock) force_unlock=1; shift ;;
  --) shift; break ;;
  *)
    printf 'pg-go-mutate-sweep: unknown option: %s\n' "$1" >&2
    exit 2
    ;;
  esac
done

for n in "$unit_timeout" "$kill_grace" "$mutant_timeout" "$workers"; do
  case "$n" in '' | *[!0-9]*) printf 'pg-go-mutate-sweep: timeouts and --workers must be positive integers\n' >&2; exit 2 ;; esac
  [ "$n" -ge 1 ] || { printf 'pg-go-mutate-sweep: timeouts and --workers must be >= 1\n' >&2; exit 2; }
done

[ -d "$root" ] || { printf 'pg-go-mutate-sweep: --root %s is not a directory\n' "$root" >&2; exit 2; }
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
```

Then the drive loop, added in Task 9. For this task end the script after the collision check with the `--dry-run` branch:

```bash
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
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep.bats
```

Expected: all 11 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): CLI, preflight, locking and --dry-run (pg2-l36xv)"
```

---

### Task 9: The drive loop and the watchdog

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.sh`
- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep.bats`

**Interfaces:**

- Consumes: everything from Tasks 3–8.
- Produces: the executed sweep. Writes `runs/<project-slug>/<pkg-slug>.json` then appends one unit record per attempt.

- [ ] **Step 1: Write the failing test**

```bash
_ok_json() {
  printf '{"statistics":{"killed":1,"survived":0,"notViable":0,"timedOut":0,"errors":0},"survivors":[],"buildTagsNotRun":null}\n'
}

@test "a failing unit records failed and the sweep CONTINUES" {
  # The errexit regression: the builder injects set -euo pipefail, so the loop
  # must capture the exit status with `rc=0; cmd || rc=$?`.
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/p/b"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/p/b/b.go"
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; exit 1'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 2 ]
  grep -q '"status":"failed"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "exit 13 aborts the whole sweep with exit 4" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 13'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
}

@test "exit 2 from the engine aborts as a sweep bug" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 2'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 4 ]
}

@test "exit 14 records vanished and continues" {
  _ws_one_unit
  _stub pg-go-mutate 'exit 14'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  grep -q '"status":"vanished"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "a high timed-out fraction records inconclusive, not done" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":0,\"survived\":0,\"notViable\":0,\"timedOut\":10,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q '"status":"inconclusive"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "the engine is invoked with --json and the report is stored" {
  _ws_one_unit
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q -- '--json' "$TEST_DIR/calls"
  [ -f "$TEST_DIR/state/pg-go-mutate-sweep/runs/p/a.json" ]
}

@test "--tags is passed ONLY when a tag is applied" {
  _ws_one_unit
  printf '//go:build contract\n\npackage a\n' >"$TEST_DIR/ws/p/a/a_test.go"
  _stub pg-go-mutate 'echo "$@" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  ! grep -q -- '--tags' "$TEST_DIR/calls"
  : >"$TEST_DIR/calls"
  run "$SCRIPT" --root "$TEST_DIR/ws" --redo 'p#a' --auto-tags contract
  grep -q -- '--tags contract' "$TEST_DIR/calls"
}

@test "the watchdog kills the whole subtree" {
  # This is the test that would have caught `timeout --foreground`, in which
  # children of COMMAND are NOT timed out.
  _ws_one_unit
  _stub pg-go-mutate 'bash -c "sleep 300 & echo \$! >\"$TEST_DIR/grandchild\"; sleep 300"'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --unit-timeout 1 --unit-kill-grace 1
  [ "$status" -eq 0 ]
  grep -q '"status":"timeout"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
  gc="$(cat "$TEST_DIR/grandchild" 2>/dev/null || true)"
  [ -n "$gc" ] && ! kill -0 "$gc" 2>/dev/null
}

@test "a resumed run redoes nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 1 ]
}

@test "--retry re-attempts only the selected statuses" {
  _ws_one_unit
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; exit 1'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  run "$SCRIPT" --root "$TEST_DIR/ws" --retry failed
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 2 ]
}

@test "--only restricts the run list but not the plan" {
  mkdir -p "$TEST_DIR/ws/p/a" "$TEST_DIR/ws/q/b"
  printf 'module example.com/p\n\ngo 1.25.0\n' >"$TEST_DIR/ws/p/go.mod"
  printf 'module example.com/q\n\ngo 1.25.0\n' >"$TEST_DIR/ws/q/go.mod"
  printf 'package a\n' >"$TEST_DIR/ws/p/a/a.go"
  printf 'package b\n' >"$TEST_DIR/ws/q/b/b.go"
  _stub pg-go-mutate 'echo "$PWD" >>"$TEST_DIR/calls"; printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --only p
  [ "$(grep -c . "$TEST_DIR/calls")" -eq 1 ]
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep.bats
```

Expected: the 11 new cases FAIL — the loop does not exist, so nothing is recorded.

- [ ] **Step 3: Write the implementation**

Replace the `exit 0` that ended Task 8's script with the loop.

```bash
runs_dir="$(pgms_runs_dir)"
inconclusive_threshold=50

_in_scope() { # <project>
  local p
  [ "${#only_projects[@]}" -eq 0 ] && return 0
  for p in "${only_projects[@]}"; do [ "$p" = "$1" ] && return 0; done
  return 1
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

  # Re-stat immediately before invoking: over a multi-hour unattended sweep in a
  # live workspace one branch switch can remove a package directory.
  if [ ! -d "$dir" ]; then
    status=vanished
    : >"$report"
  else
    tags_pair="$(pgms_apply_tags "$dir" "$auto_tags")"
    tags_applied="${tags_pair%%$'\t'*}"
    tags_withheld="${tags_pair#*$'\t'}"

    args=(--json --workers "$workers" --timeout "$mutant_timeout")
    [ -n "$tags_applied" ] && args+=(--tags "$tags_applied")

    printf 'pg-go-mutate-sweep: %s\n' "$key" >&2
    rc=0
    # No --foreground: that mode exists so a command can read the TTY and in it
    # children of COMMAND are NOT timed out, which is the opposite of the subtree
    # kill wanted here. Default mode signals the child's process group.
    timeout --kill-after="$kill_grace" "$unit_timeout" \
      pg-go-mutate "${args[@]}" "$dir" >"$report" 2>/dev/null || rc=$?
    status="$(pgms_classify "$rc" "$report" "$inconclusive_threshold")"
    if [ "$status" = "fatal" ]; then
      printf 'pg-go-mutate-sweep: %s exited %s -- aborting the sweep rather than recording %s identical failures\n' \
        "$key" "$rc" "$(pgms_plan "$root" | grep -c . || true)" >&2
      exit 4
    fi
  fi

  # The record is appended only AFTER the report is written, so a unit is never
  # marked complete with no artifact behind it.
  pgms_append_record "$(jq -nc \
    --arg u "$key" --arg p "$proj" --arg k "$pkg" --arg s "$status" \
    --arg e "${rc:-0}" --arg ta "${tags_applied:-}" --arg tw "${tags_withheld:-}" \
    --arg t "$(date -Iseconds)" --arg r "${report#"$(pgms_state_root)"/}" \
    '{kind:"unit",unit:$u,project:$p,pkg:$k,status:$s,exit:($e|tonumber),
      tags_applied:$ta,tags_withheld:$tw,finished:$t,report:$r}')"

  _file_due_beads
done < <(pgms_plan "$root")

exit 0
```

Add the two body helpers above the loop:

```bash
_bead_title() { printf 'go-test-gaps triage: %s\n' "$1"; }

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
    printf '  - %s: %s (withheld tags: %s)\n' "$u" "${st:-unrun}" \
      "$(pgms_replay_units >/dev/null 2>&1; printf '%s' "$(_pgms_withheld_for "$key")")"
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

_pgms_withheld_for() { # <unit-key>
  local ledger
  ledger="$(pgms_ledger_path)"
  [ -f "$ledger" ] || { printf 'none'; return 0; }
  pgms_valid_lines <"$ledger" |
    jq -rs --arg u "$1" 'map(select(.kind=="unit" and .unit==$u)) | if length==0 then "none"
                          else (last.tags_withheld // "") | if . == "" then "none" else . end end'
}
```

- [ ] **Step 4: Run to verify it passes**

```bash
bats tests/test-pg-go-mutate-sweep.bats
```

Expected: all tests PASS. If the watchdog test fails with a live grandchild, `--foreground` has crept in — remove it.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "feat(pg-go-mutate-sweep): drive loop, process-group watchdog and bead emission (pg2-l36xv)"
```

---

### Task 10: Bead emission tests

The bead path is the one place a bug silently loses a project's findings, so it gets its own gate.

**Files:**

- Modify: `modules/pg-go-mutate/pg-go-mutate-sweep/tests/test-pg-go-mutate-sweep.bats`

**Interfaces:**

- Consumes: the CLI (Tasks 8–9).
- Produces: no new interface.

- [ ] **Step 1: Write the failing test**

```bash
@test "the bead is filed exactly once per project" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$(grep -c 'create' "$TEST_DIR/bdcalls")" -eq 1 ]
  : >"$TEST_DIR/bdcalls"
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ ! -s "$TEST_DIR/bdcalls" ]
}

@test "a resumed run with ZERO units left still files the pending bead" {
  # The lost-project regression at script level: the ledger has every unit but
  # no bead record, exactly as a crash between the two would leave it.
  _ws_one_unit
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep"
  cat >"$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl" <<'EOF'
{"kind":"unit","unit":"p#a","project":"p","pkg":"a","status":"done","finished":"2026-08-17T10:00:00-04:00"}
EOF
  _stub pg-go-mutate 'echo RAN >>"$TEST_DIR/calls"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ ! -f "$TEST_DIR/calls" ]
  [ "$(grep -c 'create' "$TEST_DIR/bdcalls")" -eq 1 ]
}

@test "a --retry that produces a newer record AMENDS by comment" {
  _ws_one_unit
  mkdir -p "$TEST_DIR/state/pg-go-mutate-sweep"
  cat >"$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl" <<'EOF'
{"kind":"unit","unit":"p#a","project":"p","pkg":"a","status":"failed","finished":"2026-08-17T10:00:00-04:00"}
{"kind":"bead","project":"p","bead":"pg2-existing","action":"filed","finished":"2026-08-17T10:01:00-04:00"}
EOF
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --retry failed
  grep -q 'comment pg2-existing' "$TEST_DIR/bdcalls"
  ! grep -q 'create' "$TEST_DIR/bdcalls"
}

@test "a failing bd does not abort the sweep and leaves no bead record" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'exit 7'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  [ "$status" -eq 0 ]
  ! grep -q '"kind":"bead"' "$TEST_DIR/state/pg-go-mutate-sweep/ledger.jsonl"
}

@test "--no-beads files nothing" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":1,\"survived\":0,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'echo "$@" >>"$TEST_DIR/bdcalls"; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws" --no-beads
  [ ! -f "$TEST_DIR/bdcalls" ]
}

@test "the bead body carries the protocol and no mutant count" {
  _ws_one_unit
  _stub pg-go-mutate 'printf "{\"statistics\":{\"killed\":7,\"survived\":3,\"notViable\":0,\"timedOut\":0,\"errors\":0}}\n"; exit 0'
  _stub bd 'for a in "$@"; do case "$a" in *body*) ;; esac; done; while [ $# -gt 0 ]; do [ "$1" = "--body-file" ] && cp "$2" "$TEST_DIR/body.md"; shift; done; echo pg2-stub'
  run "$SCRIPT" --root "$TEST_DIR/ws"
  grep -q 'tags_withheld' "$TEST_DIR/body.md"
  grep -q 'Record NO scores' "$TEST_DIR/body.md"
  # N1: the body must not leak the engine's survivor count
  ! grep -qE '(^|[^0-9])3 surviv' "$TEST_DIR/body.md"
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
bats tests/test-pg-go-mutate-sweep.bats
```

Expected: the six new cases FAIL if Task 9's bead path has any of the defects they target.

- [ ] **Step 3: Fix whatever they catch**

No new code is planned here — this task exists to gate Task 9. If a test fails, fix `_file_due_beads` / `_bead_body` in `pg-go-mutate-sweep.sh`. The likely fixes: `bd create --silent` must be the only thing writing to stdout for the id capture, and `--no-beads` must return before any `bd` invocation.

- [ ] **Step 4: Run to verify they pass**

```bash
bats tests/test-pg-go-mutate-sweep.bats
```

Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/pg-go-mutate/pg-go-mutate-sweep
git commit -m "test(pg-go-mutate-sweep): gate the bead-emission path (pg2-l36xv)"
```

---

### Task 11: Package it — tldr, completions, `default.nix`, five wiring sites

**Files:**

- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/default.nix`
- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.md`
- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/completions/pg-go-mutate-sweep.bash`
- Create: `modules/pg-go-mutate/pg-go-mutate-sweep/completions/_pg-go-mutate-sweep`
- Modify: `modules/pg-go-mutate/scripts.nix`
- Modify: `flake.nix` (packages entry ~line 218; `overlays.default` inherit ~line 970)
- Modify: `home/pg-go-mutate/default.nix`

**Interfaces:**

- Consumes: the command from Tasks 8–10.
- Produces: `pkgs.pg-go-mutate-sweep`, `homeModules.pg-go-mutate` installing it.

- [ ] **Step 1: Write `default.nix`**

Mirror the sibling exactly, minus the engine-pin `config` (the sweep has no pin) and minus `gawk`/`go` in `runtimeDeps`.

```nix
{
  mkBashScript,
  pkgs,
  pg-go-mutate-lib,
}:

mkBashScript {
  name = "pg-go-mutate-sweep";
  src = ./.;
  description = "Resumable unattended mutation sweep over every Go package in the workspace";
  public = true;
  libraries = [ pg-go-mutate-lib ];
  # NEITHER pg-go-mutate NOR bd is a runtimeDep, deliberately. runtimeDeps are
  # appended with --suffix PATH, so listing them could not displace the machine's
  # wrapper -- the hazard is the reverse: it would provide a SILENT FALLBACK to an
  # unwrapped pg-go-mutate or an unmanaged bd when the wrapped one is absent. A
  # bd from outside the machine wrapper loses BEADS_DOLT_AUTO_START=0 and can
  # spawn a competing dolt server on port 25252. With no fallback the preflight
  # fails loudly instead.
  #
  # gawk is NOT here: awk is used by pgm_has_tests, which this command never
  # calls. go is in testDeps, not runtimeDeps -- matching the sibling, which
  # requires an ambient toolchain. In runtimeDeps it is inert today, but if it
  # ever won it would run pgm_detect_tags under a different toolchain than the
  # analysis, changing which go1.NN-gated files are visible.
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

- [ ] **Step 2: Write the tldr page**

Follow the sibling's `pg-go-mutate.md` shape: `# pg-go-mutate-sweep`, a `>` blockquote summary, then dash-bulleted examples each followed by a backticked command with `{{placeholders}}`. Cover: run the sweep; resume it (same command); dry-run the plan; scope to one project; retry the transient cohort; redo one unit with a widened tag allowlist and a raised timeout; show usage. Add a short "Reading the state" section naming `ledger.jsonl` and `runs/<project>/<unit>.json`, and state that it records unit status only and no score.

- [ ] **Step 3: Write both completions**

Mirror the sibling's structure. Bash:

```bash
_pg_go_mutate_sweep() {
  local cur prev
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD - 1]}"

  case "$prev" in
  --root) mapfile -t COMPREPLY < <(compgen -d -- "$cur"); return 0 ;;
  --unit-timeout | --unit-kill-grace | --mutant-timeout | --workers) return 0 ;;
  --only | --auto-tags | --redo) return 0 ;;
  --retry)
    mapfile -t COMPREPLY < <(compgen -W "transient failed timeout unhealthy not-enumerable vanished inconclusive done no-tests" -- "$cur")
    return 0
    ;;
  esac

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--root --only --unit-timeout --unit-kill-grace --mutant-timeout --workers --auto-tags --retry --redo --dry-run --no-beads --force-unlock --help -h --version -v" -- "$cur")
    return 0
  fi
}
complete -F _pg_go_mutate_sweep pg-go-mutate-sweep
```

Zsh:

```zsh
#compdef pg-go-mutate-sweep

_arguments -s \
  '--root[workspace root]:dir:_directories' \
  '*--only[restrict the run list to one project]:project:' \
  '--unit-timeout[per-unit wall-clock cap in seconds]:seconds:' \
  '--unit-kill-grace[grace before escalating to KILL]:seconds:' \
  '--mutant-timeout[per-mutant test timeout in seconds]:seconds:' \
  '--workers[parallel workers within a unit]:count:' \
  '--auto-tags[build tags eligible for automatic application]:tags:' \
  '--retry[re-attempt units by status]:spec:(transient failed timeout unhealthy not-enumerable vanished inconclusive)' \
  '--redo[re-attempt one unit]:key:' \
  '--dry-run[print the plan and resume position]' \
  '--no-beads[file no beads]' \
  '--force-unlock[break a wedged lock]' \
  '(-h --help)'{-h,--help}'[show help]' \
  '(-v --version)'{-v,--version}'[show version information]'
```

- [ ] **Step 4: Wire all five sites**

`modules/pg-go-mutate/scripts.nix` — four edits, and the `inherit` is the one that is easy to miss:

```nix
  pg-go-mutate-sweep = pkgs.callPackage ./pg-go-mutate-sweep {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pg-go-mutate-lib;
  };

  allScripts = [
    pg-go-mutate
    pg-go-mutate-sweep
  ];
in
{
  inherit pg-go-mutate-lib pg-go-mutate pg-go-mutate-sweep;
  ...
  checks = {
    test-pg-go-mutate-lib = pg-go-mutate-lib.check;
    test-pg-go-mutate = pg-go-mutate.check;
    test-pg-go-mutate-sweep = pg-go-mutate-sweep.check;
  };
```

`flake.nix` — beside the existing `pg-go-mutate` packages entry (~line 218):

```nix
            # pg-go-mutate-sweep: resumable unattended mutation sweep, one
            # (project, package) unit at a time, filing a triage bead per project.
            pg-go-mutate-sweep = pgGoMutateScripts.pg-go-mutate-sweep.script;
```

and in `overlays.default`'s inherit list (~line 970), add `pg-go-mutate-sweep` after `pg-go-mutate`.

`home/pg-go-mutate/default.nix` — the sweep needs no wrapper, so it goes ALONGSIDE `wrapped`, not inside the `symlinkJoin` (whose `postBuild` hard-codes `wrapProgram $out/bin/pg-go-mutate`):

```nix
    sweepPackage = mkPackageOption pkgs "pg-go-mutate-sweep" { };
```

```nix
    home.packages = [
      wrapped
      cfg.sweepPackage
    ];
```

```nix
    programs.tldr.customPages.pg-go-mutate-sweep = mkIf config.programs.tldr.enable {
      platform = "common";
      source = "${cfg.sweepPackage}/share/tldr/pages.common/pg-go-mutate-sweep.md";
    };
```

Omitting that last entry means the page is built into the store and reaches nobody — the module's own comment says so.

- [ ] **Step 5: Verify the build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/.worktrees/pg2-l36xv
nix build --no-link .#pg-go-mutate-sweep
nix build --no-link .#checks.aarch64-darwin.test-pg-go-mutate-sweep
```

Expected: both succeed. Set an explicit timeout of 600000 ms or run in the background with Monitor — these outlive the 2-minute default.

- [ ] **Step 6: Format to convergence and commit**

```bash
nix fmt modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.md
nix fmt modules/pg-go-mutate/pg-go-mutate-sweep/pg-go-mutate-sweep.md
git add -A
git commit -m "feat(pg-go-mutate-sweep): package the command and wire it into the module (pg2-l36xv)"
```

---

### Task 12: Repo docs and the full gate

**Files:**

- Modify: `CLAUDE.md` (the "Mutation testing (`pg-go-mutate`)" section)

**Interfaces:**

- Consumes: everything.
- Produces: nothing.

- [ ] **Step 1: Update `CLAUDE.md`**

The existing section is not wrong about exit 0 or about `pg-go-mutate` recording nothing — both remain true. Add: the guard failure taxonomy now has distinguishing exit codes (ADR-0026), and a sibling `pg-go-mutate-sweep` exists that DOES record durable state under XDG (unit status only, never a score) and files one triage bead per project. Reference both specs and ADR-0026.

- [ ] **Step 2: Run the complete gate**

```bash
cd modules/pg-go-mutate/pg-go-mutate-sweep && bats tests/
cd ../pg-go-mutate && bats tests/
cd ../lib && bats tests/
```

Expected: all suites PASS.

- [ ] **Step 3: Run the scoped pre-commit gate**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/.worktrees/pg2-l36xv
git add -A
prek run --files $(git diff --cached --name-only | tr '\n' ' ')
```

Expected: all hooks pass. `git add` FIRST — prek silently skips untracked files, so an un-added file yields a vacuous green. Do NOT use `--all-files`: it re-runs every hook over the whole repo and can false-block on a pre-existing violation in a file this change never touched.

- [ ] **Step 4: Run `nix flake check`**

```bash
nohup nix flake check >/tmp/flake-check-l36xv.log 2>&1 &
```

Then watch it with Monitor. `nix flake check` outruns the 10-minute Bash cap, so detach it — do not raise the timeout and do not re-issue it unchanged after a timeout.

Expected: green, including the new `test-pg-go-mutate-sweep` check.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: describe the mutation sweep and the new guard exit codes (pg2-l36xv)"
```

- [ ] **Step 6: Hand back for the applies and the first sweep**

Two operator applies are required and neither is an agent action:

1. `pg2-un41a` must land and be applied — import `homeModules.pg-go-mutate`, set `phillipgreenii.pg-go-mutate.enable = true` in the terminal repo.
2. A SECOND apply after this branch lands, because Task 2 changes the installed `pg-go-mutate` and Task 11 adds a new command, and neither is on `PATH` until then. The sweep's preflight fails without it.

Report to the operator, then stop. After their apply, the first real run is:

```bash
pg-go-mutate-sweep --dry-run          # confirm the plan and the resume position
pg-go-mutate-sweep                    # the sweep itself; re-run it to resume
```

---

## Self-Review

**Spec coverage.** §1–§4 → Tasks 3–9 (architecture, drive loop). §5.1/5.2 → Task 4. §5.3 → Task 4 (subtree ordering) plus `--help` text in Task 8. §5.4 → Task 4. §5.5 → Task 7 (`pgms_apply_tags`) and Task 9 (conditional `--tags`). §6.1 → Tasks 3–4. §6.2 → Tasks 5, 9. §6.3 → Tasks 5, 6, 9. §6.4 → Task 9. §6.5 → Task 7, released in Task 8. §7.1 → Task 2. §7.2 → Task 7. §7.3 → Task 7 (`pgms_classify` fraction). §7.4 → Task 9. §8 → Task 8. §9 → Task 11 (`default.nix` comment). §10 → Task 9 (`_bead_body`), gated by Task 10. §11 → Task 11. §12 → Tasks 3–10. §13 risks → covered by the tasks each mitigation names. §15 landing order → Task 1 (ADR first), Task 12 Step 6 (both applies). §16 open items → the `inconclusive` threshold is `inconclusive_threshold=50` in Task 9 with the spec's note that it should be tuned against one real slow module; `--only` accepting a unit key is deliberately not implemented (spec lists it as open, no task needed).

**Placeholder scan.** No "TBD"/"TODO"/"handle edge cases". Every code step carries real code. Task 10 Step 3 has no new code by design — it is a gate on Task 9 — and says so explicitly with the two likely fixes named.

**Type consistency.** Function names verified consistent across tasks: `pgms_state_root`, `pgms_ledger_path`, `pgms_runs_dir`, `pgms_slug`, `pgms_unit_key`, `pgms_unit_project`, `pgms_unit_pkg` (Task 3, used in 4–9); `pgms_find_projects`, `pgms_find_units`, `pgms_plan`, `pgms_check_slug_collisions` (Task 4, used in 6, 8, 9); `pgms_append_record`, `pgms_valid_lines`, `pgms_replay_units`, `pgms_unit_status`, `pgms_unit_needs_run` (Task 5, used in 6, 8, 9); `pgms_bead_due`, `pgms_bead_action`, `_pgms_latest_bead` (Task 6, used in 9); `pgms_lock_acquire`, `pgms_lock_release`, `pgms_classify`, `pgms_apply_tags` (Task 7, used in 8, 9). `pgms_classify` returns the literal `fatal` in Task 7 and Task 9 branches on exactly that string.

One gap the review found and this plan closes: `_pgms_newest_unit_stamp` needs a project on each unit record, so Task 6 Step 3 specifies the `.project // (.unit | split("#")[0])` fallback filter and Task 9 writes `project` on every record.
