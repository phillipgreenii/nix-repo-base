# determine-ul-lib-dir Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-`update-locks.sh` hard-coded sibling-source line with a single canonical resolver (`determine-ul-lib-dir`) shipped as a flake output of `phillipg-nix-repo-base`. Each consumer becomes a two-line invocation; the resolver picks between `WORKSPACE_ROOT`-sibling-on-disk and the flake-baked nix-store copy at runtime.

**Architecture:** Add `packages.update-locks-lib` (a derivation that contains the two bash files in `$out/lib/scripts/`). Add `modules/ul/determine-ul-lib-dir/` as a new `mkBashScript` module that prints the resolved path. Each of the six consumer `update-locks.sh` files calls `nix run github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir` to set `UL_LIB_DIR` before sourcing the lib.

**Tech Stack:** Nix flakes, `mkBashBuilders` framework (`mkBashScript`), bats, shellcheck.

**Spec / bd issue:** `beads_pg2-i5to` (`bd show beads_pg2-i5to`).

---

## File Structure

This plan touches one repo for the build (`phillipg-nix-repo-base`) and six repos for the migration. Files in scope:

- **Create**: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/default.nix`
- **Create**: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/determine-ul-lib-dir.sh`
- **Create**: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/tests/test-determine-ul-lib-dir.bats`
- **Create**: `phillipg-nix-repo-base/modules/ul/scripts.nix`
- **Modify**: `phillipg-nix-repo-base/flake.nix` — add `update-locks-lib` derivation, import `modules/ul/scripts.nix`, expose `determine-ul-lib-dir` package + check
- **Modify** (one update-locks.sh per repo, all identical pattern):
  - `phillipg-nix-repo-base/update-locks.sh`
  - `phillipgreenii-nix-overlay/update-locks.sh`
  - `phillipg-nix-ziprecruiter/update-locks.sh`
  - `phillipgreenii-nix-personal/update-locks.sh`
  - `phillipgreenii-nix-agent-support/update-locks.sh`
  - `phillipgreenii-nix-support-apps/update-locks.sh`

Each consumer ends up with two lines of resolver-related boilerplate at the top, replacing the existing `source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"` line.

---

## Task 1: Build the resolver module in `phillipg-nix-repo-base`

This is the bulk of the work — TDD'd. Result is a single commit in `phillipg-nix-repo-base` adding the lib derivation, the resolver module, its tests, the module wiring, and the flake-level exposure.

**Files:**

- Create: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/default.nix`
- Create: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/determine-ul-lib-dir.sh`
- Create: `phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/tests/test-determine-ul-lib-dir.bats`
- Create: `phillipg-nix-repo-base/modules/ul/scripts.nix`
- Modify: `phillipg-nix-repo-base/flake.nix`

### Step 1.1: Add the `update-locks-lib` derivation to `flake.nix`

In `phillipg-nix-repo-base/flake.nix`, find the `packages = { ... };` attrset (around lines 72–103). Add a new entry **before** the `fix-lint` entry:

```nix
# Packaged shared bash lib. Consumed by determine-ul-lib-dir and
# referenced via flake input by external consumers of update-locks tooling.
update-locks-lib = pkgs.runCommand "update-locks-lib" { } ''
  mkdir -p $out/lib/scripts
  cp ${./lib/scripts/update-locks-lib.bash} $out/lib/scripts/update-locks-lib.bash
  cp ${./lib/scripts/update-cache-lib.bash} $out/lib/scripts/update-cache-lib.bash
'';
```

- [ ] **Step 1.2: Verify the lib derivation builds**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix build .#update-locks-lib --print-out-paths
ls -la "$(nix build .#update-locks-lib --print-out-paths)/lib/scripts/"
```

Expected: prints a `/nix/store/<hash>-update-locks-lib` path; `ls` shows both `update-locks-lib.bash` and `update-cache-lib.bash`.

### Step 1.3: Create the module directory and the test file (RED)

- [ ] **Step 1.3: Create `tests/test-determine-ul-lib-dir.bats`**

```bash
mkdir -p /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/tests
```

Write to `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/tests/test-determine-ul-lib-dir.bats`:

```bash
#!/usr/bin/env bats

# Tests for determine-ul-lib-dir script
# SCRIPTS_DIR is set by the nix check derivation; UL_LIB_PACKAGE_PATH is
# injected by mkBashScript's config = { UL_LIB_PACKAGE_PATH = ...; }.
# For local bats runs we set sentinel values in setup().

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

SCRIPT="$SCRIPTS_DIR/determine-ul-lib-dir.sh"

setup() {
  TEST_DIR=$(mktemp -d)
  export TEST_DIR

  # Sentinel value for the nix-store fallback path. The nix check derivation
  # exports UL_LIB_PACKAGE_PATH=<store path>; for local bats we provide a
  # sentinel so the assertion is identity-checkable either way.
  export UL_LIB_PACKAGE_PATH="${UL_LIB_PACKAGE_PATH:-/sentinel/nix-store/lib/scripts}"

  # Always clear env vars we depend on so tests start from a known state.
  unset UL_LIB_DIR_OVERRIDE UL_IGNORE_WORKSPACE_ROOT WORKSPACE_ROOT
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Construct a workspace layout with a real update-locks-lib.bash sibling
_make_workspace_with_sibling() {
  local ws="$1"
  mkdir -p "$ws/phillipg-nix-repo-base/lib/scripts"
  : >"$ws/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
}

@test "UL_LIB_DIR_OVERRIDE takes precedence over everything" {
  export UL_LIB_DIR_OVERRIDE="/override/path"
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "/override/path" ]
}

@test "WORKSPACE_ROOT + sibling-on-disk wins over baked nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$WORKSPACE_ROOT/phillipg-nix-repo-base/lib/scripts" ]
}

@test "UL_IGNORE_WORKSPACE_ROOT skips the sibling check even when sibling exists" {
  export WORKSPACE_ROOT="$TEST_DIR"
  export UL_IGNORE_WORKSPACE_ROOT=1
  _make_workspace_with_sibling "$WORKSPACE_ROOT"

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "WORKSPACE_ROOT set but sibling missing falls back to nix-store path" {
  export WORKSPACE_ROOT="$TEST_DIR"
  # Do NOT create the sibling file
  [ ! -f "$WORKSPACE_ROOT/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash" ]

  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "no WORKSPACE_ROOT falls back to nix-store path" {
  # WORKSPACE_ROOT already unset by setup()
  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}

@test "non-existent WORKSPACE_ROOT directory does not match" {
  export WORKSPACE_ROOT="/this/path/does/not/exist"
  run bash "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$output" = "$UL_LIB_PACKAGE_PATH" ]
}
```

- [ ] **Step 1.4: Run the new tests to verify they fail (RED)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir
bats tests/
```

Expected: 6 failing tests (`bash: ...determine-ul-lib-dir.sh: No such file or directory`).

### Step 1.5: Implement the resolver script (GREEN)

- [ ] **Step 1.5: Create `determine-ul-lib-dir.sh`**

Write to `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/determine-ul-lib-dir.sh`:

```bash
# shellcheck shell=bash
# determine-ul-lib-dir: print the directory containing update-locks-lib.bash.
#
# Precedence:
#   1. UL_LIB_DIR_OVERRIDE (highest — operator escape hatch)
#   2. WORKSPACE_ROOT-relative sibling if the file exists AND
#      UL_IGNORE_WORKSPACE_ROOT is unset
#   3. UL_LIB_PACKAGE_PATH (injected at build time by mkBashScript's `config`)
#
# Consumers invoke this via:
#   UL_LIB_DIR=$(nix run github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir)
#
# WORKSPACE_ROOT must be exported by the caller for the sibling check to fire.

show_help() {
  cat <<'HELP'
determine-ul-lib-dir: Print the resolved path containing update-locks-lib.bash.

Usage: determine-ul-lib-dir [-h|--help]

Reads env vars in this precedence order and prints the chosen directory:
  UL_LIB_DIR_OVERRIDE             — absolute path; if set, used directly.
  WORKSPACE_ROOT (+ sibling file) — used if the sibling update-locks-lib.bash
                                     exists AND UL_IGNORE_WORKSPACE_ROOT is unset.
  UL_LIB_PACKAGE_PATH             — baked-in nix-store fallback (always defined).

Output is a single line on stdout containing the resolved directory.
HELP
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) show_help; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; exit 1 ;;
  esac
done

if [[ -n "${UL_LIB_DIR_OVERRIDE:-}" ]]; then
  echo "$UL_LIB_DIR_OVERRIDE"
  exit 0
fi

if [[ -z "${UL_IGNORE_WORKSPACE_ROOT:-}" \
      && -n "${WORKSPACE_ROOT:-}" \
      && -f "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash" ]]; then
  echo "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts"
  exit 0
fi

echo "$UL_LIB_PACKAGE_PATH"
```

**Critical reminders about source-file rules:**

- No `#!/usr/bin/env bash` shebang — the builder injects it.
- No `set -euo pipefail` — the builder injects strict mode.
- First line is `# shellcheck shell=bash`.

- [ ] **Step 1.6: Re-run tests to verify they pass (GREEN)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir
bats tests/
```

Expected: all 6 tests pass.

### Step 1.7: Wrap the script with mkBashScript

- [ ] **Step 1.7: Create `default.nix`**

Write to `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/determine-ul-lib-dir/default.nix`:

```nix
{
  mkBashScript,
  pkgs,
  update-locks-lib,
}:

mkBashScript {
  name = "determine-ul-lib-dir";
  src = ./.;
  description = "Resolve which copy of update-locks-lib.bash to source";
  public = true; # exposed as a flake package so `nix run #determine-ul-lib-dir` works
  runtimeDeps = [ pkgs.coreutils ];
  testDeps = [ pkgs.coreutils ];
  config = {
    # Injected as a local shell var. The script reads $UL_LIB_PACKAGE_PATH
    # as the baked-in fallback path.
    UL_LIB_PACKAGE_PATH = "${update-locks-lib}/lib/scripts";
  };
}
```

### Step 1.8: Create the `modules/ul/scripts.nix` wiring

- [ ] **Step 1.8: Create `modules/ul/scripts.nix`**

Write to `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/ul/scripts.nix`:

```nix
# Pure script builders for the ul (update-locks) module.
# Mirrors modules/pn/scripts.nix.
{
  pkgs,
  bashBuilders,
  update-locks-lib,
}:
let
  determine-ul-lib-dir = pkgs.callPackage ./determine-ul-lib-dir {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs update-locks-lib;
  };

  allScripts = [
    determine-ul-lib-dir
  ];
in
{
  inherit determine-ul-lib-dir;

  packages = builtins.concatLists (map (s: s.packages) allScripts);

  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;

  checks = {
    test-determine-ul-lib-dir = determine-ul-lib-dir.check;
  };

  # Aggregate check that runs all ul script tests
  check = pkgs.runCommand "test-ul-scripts" { } ''
    ${builtins.concatStringsSep "\n" (map (s: "echo ${s.check}") allScripts)}
    touch $out
  '';
}
```

### Step 1.9: Wire into the top-level flake

- [ ] **Step 1.9: Modify `phillipg-nix-repo-base/flake.nix`**

Three edits to `flake.nix`:

**(a)** After the `pnScripts = import ./modules/pn/scripts.nix { ... };` line (around line 65), add:

```nix
ulScripts = import ./modules/ul/scripts.nix {
  inherit pkgs bashBuilders;
  inherit (self.packages.${system}) update-locks-lib;
};
```

Note: `self.packages.${system}.update-locks-lib` is the derivation added in Step 1.1.

**(b)** In the `packages = { ... };` attrset (around lines 72–103), after `pn-store-deepclean`, add:

```nix
# Update-locks resolver
determine-ul-lib-dir = ulScripts.determine-ul-lib-dir.script;
```

**(c)** In the `checks = { ... };` attrset (around lines 116–127), after `test-update-locks-lib`, add:

```nix
}
// pnScripts.checks
// ulScripts.checks;
```

Replace the existing `// pnScripts.checks;` line so checks merges both pn and ul.

- [ ] **Step 1.10: Run `nix flake check`**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix flake check --no-build 2>&1 | tail -20
```

Expected: no evaluation errors. Some heavy build checks may print but no `error:` lines for the new module.

- [ ] **Step 1.11: Build the package and the check**

```bash
nix build .#determine-ul-lib-dir .#checks.aarch64-darwin.test-determine-ul-lib-dir --print-out-paths
ls result*
```

Expected: two store paths printed. `result/bin/determine-ul-lib-dir` exists and is executable.

- [ ] **Step 1.12: Smoke-test `nix run`**

```bash
unset WORKSPACE_ROOT UL_LIB_DIR_OVERRIDE UL_IGNORE_WORKSPACE_ROOT
nix run .#determine-ul-lib-dir
```

Expected: prints a `/nix/store/<hash>-update-locks-lib/lib/scripts` path on stdout.

Then test the WORKSPACE_ROOT branch:

```bash
WORKSPACE_ROOT=/Users/phillipg/phillipg_mbp nix run .#determine-ul-lib-dir
```

Expected: prints `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/lib/scripts` (because the sibling exists at that path on this machine).

- [ ] **Step 1.13: Run the bats suite inside nix to confirm test wiring**

```bash
nix build .#checks.aarch64-darwin.test-determine-ul-lib-dir --print-build-logs 2>&1 | tail -20
```

Expected: all 6 bats tests pass.

### Step 1.14: Commit

- [ ] **Step 1.14: Commit Task 1**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git add flake.nix \
        modules/ul/scripts.nix \
        modules/ul/determine-ul-lib-dir/
git commit -m "feat(update-locks): add determine-ul-lib-dir resolver module"
```

If pre-commit reformats, re-stage and create a NEW commit (do NOT amend).

---

## Task 2: Migrate `phillipgreenii-nix-overlay/update-locks.sh` to the resolver

The overlay is the canary — it has the cleanest dependency on the sibling pattern (we just removed its vendored lib in commit `0e77bda`). Migrating it first exercises the no-sibling-needed path.

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay/update-locks.sh`

- [ ] **Step 2.1: Edit `update-locks.sh`**

Open the file. Find these two consecutive lines (around lines 26–28):

```bash
# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
# Pass WORKSPACE_ROOT so the resolver can prefer the on-disk sibling when present.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

The `export WORKSPACE_ROOT` makes the already-defined local variable available to the nix-run subprocess. The `source` line now uses `${UL_LIB_DIR}` instead of the hardcoded sibling path.

- [ ] **Step 2.2: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay
./update-locks.sh --help
```

Expected: prints the usage block and exits 0. If it errors with "no such file" before reaching the help, the resolver failed — investigate.

Also exercise the resolver call directly:

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay
WORKSPACE_ROOT="$(pwd)/.." nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir"
```

Expected: prints `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/lib/scripts` (the sibling).

(Note: when first published, the github URL may not yet contain the new flake output. Use `path:/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base#determine-ul-lib-dir` instead during local pre-push iteration. See the "Pre-push iteration" note at the end of this plan.)

- [ ] **Step 2.3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-overlay
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 3: Migrate `phillipg-nix-repo-base/update-locks.sh`

Same edit as Task 2, but in repo-base. Note: this file uses `SCRIPT_DIR`-relative sourcing (not `WORKSPACE_ROOT`) today — different shape.

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/update-locks.sh`

- [ ] **Step 3.1: Read the current shape**

```bash
head -30 /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/update-locks.sh
```

The current file (after the resilience work) sources via `${SCRIPT_DIR}/lib/scripts/...` (no `..` walk) because repo-base IS the canonical location. There is no `WORKSPACE_ROOT` defined in this script today.

- [ ] **Step 3.2: Add `WORKSPACE_ROOT` and replace the source line**

Find the block:

```bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
```

Append immediately after:

```bash
# WORKSPACE_ROOT is the parent of this repo so the resolver can find the
# in-tree update-locks-lib.bash via sibling-on-disk semantics. For repo-base,
# this is unusual since repo-base IS the canonical source — but exporting it
# means the resolver returns "${SCRIPT_DIR}/lib/scripts" (this repo's own
# copy) instead of the older flake-baked version. That preserves the
# "iterating on the lib itself works without re-publishing" property.
WORKSPACE_ROOT="${SCRIPT_DIR}/.."
```

Then find the two consecutive lines:

```bash
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

- [ ] **Step 3.3: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
./update-locks.sh --help
```

Expected: prints usage and exits 0.

- [ ] **Step 3.4: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 4: Migrate `phillipg-nix-ziprecruiter/update-locks.sh`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter/update-locks.sh`

- [ ] **Step 4.1: Edit `update-locks.sh`**

Find the consecutive lines:

```bash
# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

- [ ] **Step 4.2: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
./update-locks.sh --help
```

Expected: usage and exit 0.

- [ ] **Step 4.3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-ziprecruiter
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 5: Migrate `phillipgreenii-nix-personal/update-locks.sh`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-personal/update-locks.sh`

- [ ] **Step 5.1: Edit `update-locks.sh`**

Find the consecutive lines:

```bash
# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

- [ ] **Step 5.2: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-personal
./update-locks.sh --help
```

Expected: usage and exit 0.

- [ ] **Step 5.3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-personal
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 6: Migrate `phillipgreenii-nix-agent-support/update-locks.sh`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support/update-locks.sh`

- [ ] **Step 6.1: Edit `update-locks.sh`**

Find the consecutive lines:

```bash
# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

- [ ] **Step 6.2: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
./update-locks.sh --help
```

Expected: usage and exit 0.

- [ ] **Step 6.3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 7: Migrate `phillipgreenii-nix-support-apps/update-locks.sh`

**Files:**

- Modify: `/Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps/update-locks.sh`

- [ ] **Step 7.1: Edit `update-locks.sh`**

Find the consecutive lines:

```bash
# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

Replace with:

```bash
# Resolve which update-locks-lib.bash to source via the canonical flake resolver.
export WORKSPACE_ROOT
UL_LIB_DIR=$(nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir")
# shellcheck disable=SC1091
source "${UL_LIB_DIR}/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
```

- [ ] **Step 7.2: Smoke-test**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps
./update-locks.sh --help
```

Expected: usage and exit 0.

- [ ] **Step 7.3: Commit**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-support-apps
git add update-locks.sh
git commit -m "refactor(update-locks): source lib via determine-ul-lib-dir resolver"
```

---

## Task 8: End-to-end verification and close the bd issue

- [ ] **Step 8.1: Run `pn-workspace-update` against the live workspace**

```bash
cd /Users/phillipg/phillipg_mbp
pn-workspace-update
```

Expected: each repo's `update-locks.sh` prints (in order):

1. `==> entering dev shell at /Users/.../<repo>...` (or `==> already in nix shell...` if you're inside one)
2. The resolver call to `nix run github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir` succeeds silently (its stdout is captured by `$(...)`)
3. `ul_setup` runs, `ul_run_step` calls run, etc.

If any repo skips with `⊘ skipping ... — working tree has uncommitted changes`, that's expected behavior (Task-9-of-resilience-plan work). Make sure none of the repos fail due to the resolver itself — look for `ERROR: ... determine-ul-lib-dir` or similar resolver errors.

- [ ] **Step 8.2: Tear down old WORKSPACE_ROOT assumptions verification**

The migration leaves `WORKSPACE_ROOT="${SCRIPT_DIR}/.."` defined in each script (with `export`). The resolver uses it; nothing else does. Don't remove it — `update-locks-lib.bash`'s `ul_reexec_in_dev_shell` may still reference it indirectly via the sibling-on-disk path that `nix develop` walks.

Confirm nothing else in any of the six scripts references the _old_ hardcoded `${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/...` path:

```bash
for d in phillipg-nix-repo-base phillipgreenii-nix-overlay phillipg-nix-ziprecruiter \
         phillipgreenii-nix-personal phillipgreenii-nix-agent-support phillipgreenii-nix-support-apps; do
  echo "=== $d ==="
  grep -n 'WORKSPACE_ROOT.*phillipg-nix-repo-base/lib/scripts' /Users/phillipg/phillipg_mbp/$d/update-locks.sh || echo "  (no hardcoded sibling reference)"
done
```

Expected: every repo prints `(no hardcoded sibling reference)`. If any still has the old line, the migration missed a script.

- [ ] **Step 8.3: Run all bats suites to confirm no regressions**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix build .#checks.aarch64-darwin.test-update-locks-lib \
          .#checks.aarch64-darwin.test-pn-workspace-update \
          .#checks.aarch64-darwin.test-pn-workspace-upgrade \
          .#checks.aarch64-darwin.test-determine-ul-lib-dir
echo "all green"
```

Expected: prints "all green" with no nix-build failures.

- [ ] **Step 8.4: Close the bd issue**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
bd close beads_pg2-i5to --reason="determine-ul-lib-dir module shipped; all six consumers migrated; tests green"
```

---

## Pre-push iteration note

While implementing Task 1, the `github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir` URL won't yet resolve to the new flake output (the work is local-only). For Tasks 2-7's smoke tests, temporarily replace the URL with the local path while testing:

```bash
# In each consumer's update-locks.sh during pre-push testing:
UL_LIB_DIR=$(nix run "path:/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base#determine-ul-lib-dir")
```

But COMMIT the github URL form — that's what consumers will use once the change is pushed. If you commit the path: form by accident, the commit will reference a path that doesn't exist on other machines.

A safer pattern: leave the github URL in the script, but set the env var `NIX_FLAKE_REGISTRY` or use `--override-input` to point at the local copy:

```bash
nix run "github:phillipgreenii/phillipg-nix-repo-base#determine-ul-lib-dir" \
  --override-input phillipg-nix-repo-base "path:/Users/phillipg/phillipg_mbp/phillipg-nix-repo-base"
```

But this would also require the script to support override forwarding, which is out of scope. The simplest practical workflow:

1. Implement Task 1 locally, commit, push to github.
2. Migrate Tasks 2–7 against the live github URL.

Or use the `path:` URL during local testing, then revert to the github URL before committing.

---

## Self-Review

**Spec coverage** (vs bd issue `beads_pg2-i5to` acceptance criteria):

1. ✅ `packages.${system}.update-locks-lib` added — Step 1.1.
2. ✅ `modules/ul/determine-ul-lib-dir/` created with default.nix, .sh, tests/ — Steps 1.3, 1.5, 1.7.
3. ✅ default.nix uses mkBashScript (not inline writeShellApplication) — Step 1.7.
4. ✅ Bats tests cover override / WORKSPACE_ROOT-sibling / ignore / fallback / non-existent — Step 1.3 (6 tests).
5. ✅ Tests registered under nix flake check — Steps 1.8, 1.9, 1.13.
6. ✅ shellcheck via .pre-commit-config.yaml — mkBashScript runs shellcheck during build (line 196 of `lib/bash-builders.nix`); also separately by pre-commit on the raw .sh.
7. ✅ All six consumers migrated — Tasks 2–7.
8. ✅ Overlay no longer uses WORKSPACE_ROOT sibling source line directly — Task 2.
9. ✅ Documentation about URL bumping — covered in the "Pre-push iteration note" + Task 1.7's default.nix comment.

**Placeholder scan:** none. Every step has either complete code or an explicit command with expected output.

**Type / name consistency:** `determine-ul-lib-dir` used uniformly; `UL_LIB_PACKAGE_PATH` is the config var name from default.nix and the .sh; `UL_LIB_DIR_OVERRIDE` and `UL_IGNORE_WORKSPACE_ROOT` match the bd issue.

**Open items intentionally deferred:** the long-term "make `UL_RESOLVER_URL` env-overridable for local iteration" knob is NOT in this plan. If the github URL friction becomes painful, that's a follow-on.
