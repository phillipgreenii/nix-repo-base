# pn-workspace Agent Conventions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land three tooling additions/renames in `phillipg-nix-repo-base` and one Claude Code rule plugin in `phillipgreenii-nix-agent-support` so AI agents working inside a `pn-workspace.toml` workspace stop overriding flake inputs to local paths, stop pushing branches just to make builds work, and consistently use the workspace-aware command surface.

**Architecture:** Two repos. `phillipg-nix-repo-base` ships the bash scripts (`pn-ws-nix`, `pn-workspace-flake-check`, rename of `pn-workspace-check`) under `modules/pn/` using the existing `mkBashScript` builder pattern. `phillipgreenii-nix-agent-support` ships a new Home Manager module that materializes a `pn-workspace-rules` plugin into the existing `pgii-local-plugins` marketplace, mirroring the `agent-rules` plugin pattern.

**Tech Stack:** Bash (bats tests), Nix (`mkBashScript`, `mkBashLibrary` via `phillipgreenii-nix-base/lib/bash-builders.nix`), nix-darwin/home-manager, `nix --override-input <name> git+file://<path>`.

**Reference spec:** `docs/superpowers/specs/2026-05-18-pn-workspace-agent-conventions-design.md`

---

## File map

Per the spec, files this plan creates or modifies:

| Repo             | Path                                                                                                                | Action           | Responsibility                                                       |
| ---------------- | ------------------------------------------------------------------------------------------------------------------- | ---------------- | -------------------------------------------------------------------- |
| nix-repo-base    | `modules/pn/pn-workspace-check/`                                                                                    | DELETE           | Renamed to `pn-workspace-pre-commit-check`                           |
| nix-repo-base    | `modules/pn/pn-workspace-pre-commit-check/default.nix`                                                              | CREATE (renamed) | mkBashScript wrapper                                                 |
| nix-repo-base    | `modules/pn/pn-workspace-pre-commit-check/pn-workspace-pre-commit-check.sh`                                         | CREATE (renamed) | The pre-commit runner                                                |
| nix-repo-base    | `modules/pn/pn-workspace-pre-commit-check/tests/test-pn-workspace-pre-commit-check.bats`                            | CREATE (renamed) | bats tests                                                           |
| nix-repo-base    | `modules/pn/pn-ws-nix/default.nix`                                                                                  | CREATE           | mkBashScript wrapper                                                 |
| nix-repo-base    | `modules/pn/pn-ws-nix/pn-ws-nix.sh`                                                                                 | CREATE           | Generic `nix` wrapper with override injection                        |
| nix-repo-base    | `modules/pn/pn-ws-nix/tests/test-pn-ws-nix.bats`                                                                    | CREATE           | bats tests                                                           |
| nix-repo-base    | `modules/pn/pn-workspace-flake-check/default.nix`                                                                   | CREATE           | mkBashScript wrapper                                                 |
| nix-repo-base    | `modules/pn/pn-workspace-flake-check/pn-workspace-flake-check.sh`                                                   | CREATE           | Cross-repo flake check                                               |
| nix-repo-base    | `modules/pn/pn-workspace-flake-check/tests/test-pn-workspace-flake-check.bats`                                      | CREATE           | bats tests                                                           |
| nix-repo-base    | `modules/pn/scripts.nix`                                                                                            | MODIFY           | Drop old name, add three new entries                                 |
| nix-repo-base    | `docs/superpowers/specs/2026-04-29-pn-workspace-no-upstream-design.md` and `home/pn/default.nix` and any README/ADR | MODIFY           | Doc sweep for renamed command                                        |
| agent-support    | `home/programs/pn-workspace-rules/default.nix`                                                                      | CREATE           | HM module                                                            |
| agent-support    | `home/programs/pn-workspace-rules/pn-workspace-rules.md`                                                            | CREATE           | Rules content materialized as CLAUDE.md                              |
| agent-support    | `home/default.nix`                                                                                                  | MODIFY           | Add import of new module                                             |
| (consumer repos) | various CLAUDE.md, scripts                                                                                          | MODIFY           | Doc sweep for `pn-workspace-check` → `pn-workspace-pre-commit-check` |

---

## Task 1: Rename `pn-workspace-check` → `pn-workspace-pre-commit-check`

**Files (in `phillipg-nix-repo-base`):**

- Rename: `modules/pn/pn-workspace-check/` directory → `modules/pn/pn-workspace-pre-commit-check/`
- Rename: `pn-workspace-check.sh` → `pn-workspace-pre-commit-check.sh`
- Rename: `tests/test-pn-workspace-check.bats` → `tests/test-pn-workspace-pre-commit-check.bats`
- Modify: `modules/pn/pn-workspace-check/default.nix` → renamed file: change `name = "pn-workspace-check"` → `"pn-workspace-pre-commit-check"`
- Modify: `pn-workspace-pre-commit-check.sh` — replace every occurrence of `pn-workspace-check` with `pn-workspace-pre-commit-check` in the help text, comments, and any self-references
- Modify: `tests/test-pn-workspace-pre-commit-check.bats` — same find/replace
- Modify: `modules/pn/scripts.nix` — replace `pn-workspace-check = pkgs.callPackage ./pn-workspace-check { ... }` with `pn-workspace-pre-commit-check = pkgs.callPackage ./pn-workspace-pre-commit-check { ... }`; update `allScripts` list; update `inherit ... ;` re-export list

Branch: work on `feat/pn-workspace-agent-conventions` (already exists from spec commit).

- [ ] **Step 1: Create branch (if not already on it)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git checkout feat/pn-workspace-agent-conventions 2>/dev/null || git checkout -b feat/pn-workspace-agent-conventions
git status
```

Expected: clean working tree on `feat/pn-workspace-agent-conventions` (the spec is the most recent commit).

- [ ] **Step 2: Rename the directory and files**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
git mv modules/pn/pn-workspace-check modules/pn/pn-workspace-pre-commit-check
git mv modules/pn/pn-workspace-pre-commit-check/pn-workspace-check.sh \
       modules/pn/pn-workspace-pre-commit-check/pn-workspace-pre-commit-check.sh
git mv modules/pn/pn-workspace-pre-commit-check/tests/test-pn-workspace-check.bats \
       modules/pn/pn-workspace-pre-commit-check/tests/test-pn-workspace-pre-commit-check.bats
git status
```

Expected: three renames, no other changes.

- [ ] **Step 3: Update the renamed `default.nix`**

Open `modules/pn/pn-workspace-pre-commit-check/default.nix`. Replace the `name = "pn-workspace-check";` line with `name = "pn-workspace-pre-commit-check";`. Description should also be updated:

```nix
{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-pre-commit-check";
  src = ./.;
  description = "Run pre-commit checks for all workspace repos";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.pre-commit
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
```

- [ ] **Step 4: Update the renamed script's self-references**

In `modules/pn/pn-workspace-pre-commit-check/pn-workspace-pre-commit-check.sh`, find every occurrence of the literal string `pn-workspace-check` (in comments, the help text, error messages, the example) and replace with `pn-workspace-pre-commit-check`. The functional logic is unchanged.

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
sed -i.bak 's/pn-workspace-check/pn-workspace-pre-commit-check/g' \
  modules/pn/pn-workspace-pre-commit-check/pn-workspace-pre-commit-check.sh
rm modules/pn/pn-workspace-pre-commit-check/pn-workspace-pre-commit-check.sh.bak
```

- [ ] **Step 5: Update the renamed test file's self-references**

```bash
sed -i.bak 's/pn-workspace-check/pn-workspace-pre-commit-check/g' \
  modules/pn/pn-workspace-pre-commit-check/tests/test-pn-workspace-pre-commit-check.bats
rm modules/pn/pn-workspace-pre-commit-check/tests/test-pn-workspace-pre-commit-check.bats.bak
```

- [ ] **Step 6: Update `modules/pn/scripts.nix`**

Open `modules/pn/scripts.nix`. Replace:

```nix
  pn-workspace-check = pkgs.callPackage ./pn-workspace-check {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };
```

with:

```nix
  pn-workspace-pre-commit-check = pkgs.callPackage ./pn-workspace-pre-commit-check {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };
```

In the `allScripts = [ ... ];` list, replace `pn-workspace-check` with `pn-workspace-pre-commit-check`.

In the trailing `inherit ... ;` re-export, replace `pn-workspace-check` with `pn-workspace-pre-commit-check`.

- [ ] **Step 7: Build the renamed package**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
pn-ws-nix build .#pn-workspace-pre-commit-check --no-link --print-out-paths 2>/dev/null \
  || nix build .#pn-workspace-pre-commit-check --no-link --print-out-paths
```

Expected: produces a store path. (Use `pn-ws-nix` if it already exists; otherwise bare `nix` — note that **at this point in the plan, `pn-ws-nix` does not yet exist**, so this Task will use bare `nix`. Subsequent Tasks 2/3 will introduce `pn-ws-nix`, and from Task 4 onward all build/eval commands MUST use `pn-ws-nix`.)

- [ ] **Step 8: Run the renamed bats test**

```bash
nix build .#checks.aarch64-darwin.test-pn-workspace-pre-commit-check --no-link 2>&1 | tail -5 \
  || echo "(if the check name differs, look at the flake.nix tests block for the exact attribute name)"
```

If the test check isn't wired by `inherit (...)` automatically: inspect `flake.nix` for where pn-related tests are declared. The existing pattern likely auto-discovers from `scripts.nix`; verify by running a broader command:

```bash
nix flake check --no-build
```

Expected: succeeds; the renamed test is picked up by the flake's auto-discovery from `scripts.nix`'s exports.

- [ ] **Step 9: Commit**

```bash
git add modules/pn/pn-workspace-pre-commit-check modules/pn/scripts.nix
git commit -m "pn-workspace-pre-commit-check: rename from pn-workspace-check

Symmetric with the new pn-workspace-flake-check; suffix names the verb
('check'), prefix names the subject ('pre-commit' or 'flake')."
```

---

## Task 2: Add `pn-ws-nix`

**Files (in `phillipg-nix-repo-base`):**

- Create: `modules/pn/pn-ws-nix/default.nix`
- Create: `modules/pn/pn-ws-nix/pn-ws-nix.sh`
- Create: `modules/pn/pn-ws-nix/tests/test-pn-ws-nix.bats`
- Modify: `modules/pn/scripts.nix` (register `pn-ws-nix`)

- [ ] **Step 1: Write the failing test**

Create `modules/pn/pn-ws-nix/tests/test-pn-ws-nix.bats`:

```bash
#!/usr/bin/env bats

# Tests for pn-ws-nix script

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

LIB_PATH="${LIB_PATH:-$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash}"

run_script() {
  # shellcheck disable=SC1090
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-ws-nix.sh'" -- "$@"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace

    create_mock_pn_discover_workspace

    # Mock nix binary that prints its args and exits 0
    cat >"$TEST_DIR/nix" <<'EOF'
#!/usr/bin/env bash
echo "Mock nix called with: $*"
exit 0
EOF
    chmod +x "$TEST_DIR/nix"
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    teardown_test_home
}

@test "passes args through to nix when not in deny-list" {
    run run_script build .#hello
    [ "$status" -eq 0 ]
    [[ "$output" == *"Mock nix called with: build .#hello"* ]]
}

@test "injects --override-input for each workspace project" {
    run run_script eval .#x
    [ "$status" -eq 0 ]
    [[ "$output" == *"--override-input"* ]]
}

@test "flake update triggers warn by default" {
    run run_script flake update
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
    # Confirm no --override-input flags reached the mock nix
    [[ "$output" != *"Mock nix called with"*"--override-input"* ]]
}

@test "flake lock triggers warn by default" {
    run run_script flake lock
    [ "$status" -eq 0 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
}

@test "--non-override-subcommand-action=error exits 2 for flake update" {
    run run_script --non-override-subcommand-action=error flake update
    [ "$status" -eq 2 ]
    [[ "$output" == *"pn-ws-nix: overrides not applicable"* ]]
}

@test "--non-override-subcommand-action=ignore is silent and runs nix" {
    run run_script --non-override-subcommand-action=ignore flake update
    [ "$status" -eq 0 ]
    [[ "$output" == *"Mock nix called with: flake update"* ]]
    [[ "$output" != *"pn-ws-nix: overrides not applicable"* ]]
}

@test "env var PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION sets action" {
    PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION=ignore run run_script flake update
    [ "$status" -eq 0 ]
    [[ "$output" != *"pn-ws-nix: overrides not applicable"* ]]
}

@test "flag overrides env var" {
    PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION=ignore \
      run run_script --non-override-subcommand-action=error flake update
    [ "$status" -eq 2 ]
}

@test "invalid action value exits 2 with usage" {
    run run_script --non-override-subcommand-action=bogus build .#x
    [ "$status" -eq 2 ]
    [[ "$output" == *"--non-override-subcommand-action"* ]]
}
```

- [ ] **Step 2: Run tests to verify they fail (script doesn't exist yet)**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/pn/pn-ws-nix
nix develop ../../.. --command bats tests/test-pn-ws-nix.bats 2>&1 | tail -5 \
  || echo "(test failure expected — script and default.nix don't exist yet)"
```

Expected: tests fail to find `pn-ws-nix.sh` (file not found errors from the `source` calls). This confirms tests are wired correctly.

- [ ] **Step 3: Write the script**

Create `modules/pn/pn-ws-nix/pn-ws-nix.sh`:

```bash
# shellcheck shell=bash
# pn-ws-nix: workspace-aware nix wrapper that injects --override-input
# for every project declared in pn-workspace.toml / pn-workspace.lock.

set -euo pipefail

_action_arg=""
_remaining_args=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-ws-nix: Workspace-aware nix wrapper that injects --override-input

Purpose: Runs `nix <subcommand>` with --override-input flags pointing at the
local working copy of every project declared in the nearest pn-workspace.toml.
Searches ancestor directories from the current working directory to find the
workspace root (honors PN_WORKSPACE_ROOT env var).

Usage: pn-ws-nix [--non-override-subcommand-action {error|warn|ignore}] <nix-args...>

Options:
  --non-override-subcommand-action {error|warn|ignore}
                          Behavior when the nix subcommand is one for which
                          overrides do not apply (currently: `flake update`
                          and `flake lock`).
                            error  → print message to stderr, exit 2
                            warn   → print message to stderr, exec nix without overrides (default)
                            ignore → exec nix without overrides, silently
                          Honors PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION env var.
                          Flag takes priority over env var.

Examples:
  # Run flake check on the current project with workspace overrides
  pn-ws-nix flake check

  # Build a single package with workspace overrides
  pn-ws-nix build .#my-package

  # Update the lock (skips override injection automatically)
  pn-ws-nix flake update

For non-flake nix subcommands (`store *`, `profile list`, `log`, etc.), use
bare `nix` directly; --override-input does not apply to those.
HELP
    exit 0
    ;;
  --non-override-subcommand-action)
    _action_arg="$2"
    shift 2
    ;;
  --non-override-subcommand-action=*)
    _action_arg="${1#*=}"
    shift
    ;;
  *)
    _remaining_args+=("$1")
    shift
    ;;
  esac
done

# Resolve action: flag > env var > default
_action="${_action_arg:-${PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION:-warn}}"

case "$_action" in
  error|warn|ignore) ;;
  *)
    echo "error: invalid --non-override-subcommand-action value: $_action (allowed: error, warn, ignore)" >&2
    exit 2
    ;;
esac

if [[ ${#_remaining_args[@]} -eq 0 ]]; then
  echo "error: pn-ws-nix requires at least one nix argument; try 'pn-ws-nix --help'" >&2
  exit 2
fi

# Identify subcommand: first non-flag arg, plus the second if first is "flake"
_subcommand="${_remaining_args[0]}"
if [[ "$_subcommand" == "flake" && ${#_remaining_args[@]} -ge 2 ]]; then
  _subcommand="flake ${_remaining_args[1]}"
fi

# Deny-list: nix subcommands where --override-input is silently ignored.
_is_deny_listed() {
  case "$1" in
    "flake update"|"flake lock") return 0 ;;
    *) return 1 ;;
  esac
}

if _is_deny_listed "$_subcommand"; then
  case "$_action" in
    error)
      echo "pn-ws-nix: overrides not applicable to '$_subcommand'. Run \`nix ${_remaining_args[*]}\` directly if intentional." >&2
      exit 2
      ;;
    warn)
      echo "pn-ws-nix: overrides not applicable to '$_subcommand'. Running nix without overrides; use bare \`nix\` directly to silence this." >&2
      ;;
    ignore) ;;
  esac
  exec nix "${_remaining_args[@]}"
fi

# Resolve workspace root + build override flags
PN_WORKSPACE_ROOT=$(workspace_resolve_root "") || exit 1

overrides_json=$(workspace_parse_overrides) || exit 1
workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

overrides=()
while IFS= read -r entry; do
  path=$(echo "$entry" | jq -r '.path')
  input_name=$(echo "$entry" | jq -r '.inputName')
  overrides+=(--override-input "$input_name" "git+file://$path")
done < <(echo "$workspace_json" | jq -c '.[] | select(.inputName != null)')

exec nix "${_remaining_args[@]}" "${overrides[@]}"
```

- [ ] **Step 4: Write the package default.nix**

Create `modules/pn/pn-ws-nix/default.nix`:

```nix
{
  mkBashScript,
  pkgs,
  pn-lib,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-ws-nix";
  src = ./.;
  description = "Workspace-aware nix wrapper that injects --override-input for every project in pn-workspace.toml";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pkgs.nix
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
```

- [ ] **Step 5: Register in `modules/pn/scripts.nix`**

Open `modules/pn/scripts.nix`. After the `pn-workspace-pre-commit-check` block, add:

```nix
  pn-ws-nix = pkgs.callPackage ./pn-ws-nix {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs pn-lib testSupport;
  };
```

In the `allScripts = [ ... ];` list, append `pn-ws-nix`.

In the trailing `inherit ... ;` re-export, add `pn-ws-nix`.

- [ ] **Step 6: Run tests to verify they pass**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
nix flake check --no-build 2>&1 | tail -5
nix build .#pn-ws-nix --no-link --print-out-paths
```

Expected: flake check succeeds, build produces a store path.

Run the bats tests directly (the flake-check auto-runs them if wired correctly, but a direct run gives faster feedback):

```bash
nix build .#checks.aarch64-darwin.test-pn-ws-nix --no-link 2>&1 | tail -10
```

Expected: all 10 bats tests pass.

- [ ] **Step 7: Commit**

```bash
git add modules/pn/pn-ws-nix modules/pn/scripts.nix
git commit -m "pn-ws-nix: workspace-aware nix wrapper with override injection

Generic project-level wrapper around \`nix <subcommand>\` that injects
--override-input for every project in the nearest pn-workspace.toml.
Skips override injection for 'flake update' / 'flake lock' (where they
silently no-op); behavior on those subcommands controlled by
--non-override-subcommand-action {error|warn|ignore} flag and
PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION env var. Default: warn."
```

---

## Task 3: Add `pn-workspace-flake-check`

**Files (in `phillipg-nix-repo-base`):**

- Create: `modules/pn/pn-workspace-flake-check/default.nix`
- Create: `modules/pn/pn-workspace-flake-check/pn-workspace-flake-check.sh`
- Create: `modules/pn/pn-workspace-flake-check/tests/test-pn-workspace-flake-check.bats`
- Modify: `modules/pn/scripts.nix`

This task builds on `pn-ws-nix` from Task 2.

- [ ] **Step 1: Write the failing test**

Create `modules/pn/pn-workspace-flake-check/tests/test-pn-workspace-flake-check.bats`:

```bash
#!/usr/bin/env bats

# Tests for pn-workspace-flake-check script

if [[ -z ${SCRIPTS_DIR:-} ]]; then
  SCRIPTS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
fi

if [[ -n ${TEST_SUPPORT:-} ]]; then
  load "$TEST_SUPPORT/test_helper"
else
  load "$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../test-support" && pwd)/test_helper"
fi

LIB_PATH="${LIB_PATH:-$(cd "$(dirname "${BATS_TEST_FILENAME}")/../../pn-lib" && pwd)/pn-lib.bash}"

run_script() {
  # shellcheck disable=SC1090
  bash -c "source '${LIB_PATH%%:*}'; source '$SCRIPTS_DIR/pn-workspace-flake-check.sh'" -- "$@"
}

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    export REAL_HOME="$HOME"
    setup_test_home
    setup_workspace

    create_mock_pn_discover_workspace

    # Mock pn-ws-nix that always succeeds
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
echo "Mock pn-ws-nix called with: $*"
exit 0
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    export PATH="$TEST_DIR:$PATH"
}

teardown() {
    teardown_test_home
}

@test "calls pn-ws-nix flake check in each workspace project" {
    run run_script
    [ "$status" -eq 0 ]
    # Count invocations; at minimum once per project in the test fixture
    invocations=$(echo "$output" | grep -c "Mock pn-ws-nix called with: flake check" || true)
    [ "$invocations" -ge 1 ]
}

@test "non-zero exit when any project's flake check fails" {
    # Replace mock pn-ws-nix to fail for the second project
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
case "$PWD" in
  *project-two*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    run run_script
    [ "$status" -ne 0 ]
}

@test "full sweep: continues past failures and visits all projects" {
    # Mock fails on first project but should still run on the rest
    cat >"$TEST_DIR/pn-ws-nix" <<'EOF'
#!/usr/bin/env bash
echo "ran in $PWD"
case "$PWD" in
  *project-one*) exit 1 ;;
  *) exit 0 ;;
esac
EOF
    chmod +x "$TEST_DIR/pn-ws-nix"
    run run_script
    [ "$status" -ne 0 ]
    # All projects should have been visited despite the early failure
    invocations=$(echo "$output" | grep -c "ran in" || true)
    [ "$invocations" -ge 2 ]
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base/modules/pn/pn-workspace-flake-check
nix develop ../../.. --command bats tests/test-pn-workspace-flake-check.bats 2>&1 | tail -5 \
  || echo "(test failure expected — script doesn't exist yet)"
```

Expected: tests fail to source `pn-workspace-flake-check.sh`.

- [ ] **Step 3: Write the script**

Create `modules/pn/pn-workspace-flake-check/pn-workspace-flake-check.sh`:

```bash
# shellcheck shell=bash
# pn-workspace-flake-check: Run `nix flake check` for all workspace repos via pn-ws-nix

_root_arg=""
_workspace_arg=""
_override_specs=()

while [[ $# -gt 0 ]]; do
  case "$1" in
  -h | --help)
    cat <<'HELP'
pn-workspace-flake-check: Run `nix flake check` for all workspace repos

Purpose: Runs `nix flake check` (via pn-ws-nix, so overrides are injected
automatically) for every repo declared in the nearest pn-workspace.toml.
Searches ancestor directories from the current working directory to find the
workspace root. Continues past per-project failures (full sweep); overall
exit code is non-zero if any project failed.

Usage: pn-workspace-flake-check [OPTIONS]

Options:
  -h, --help                    Show this help message and exit
  --root <dir>                  Workspace root directory.
                                Default: PN_WORKSPACE_ROOT env or walk up from CWD.
  --workspace <dir>             Deprecated alias for --root.
  --override-path <name>=<path> Override the path used for a workspace project.
                                Repeatable. Checks run in the swapped path.
                                Also accepts PN_WORKSPACE_OVERRIDE_PATHS env var
                                with comma-separated entries.

Example:
  # Run flake check across all workspace repos
  pn-workspace-flake-check
HELP
    exit 0
    ;;
  --root)
    _root_arg="$2"
    shift 2
    ;;
  --root=*)
    _root_arg="${1#*=}"
    shift
    ;;
  --workspace)
    _workspace_arg="$2"
    shift 2
    ;;
  --workspace=*)
    _workspace_arg="${1#*=}"
    shift
    ;;
  --override-path)
    _override_specs+=("$2")
    shift 2
    ;;
  --override-path=*)
    _override_specs+=("${1#*=}")
    shift
    ;;
  *)
    echo "error: unknown option: $1" >&2
    exit 1
    ;;
  esac
done

if [[ -n $_root_arg && -n $_workspace_arg ]]; then
  echo "error: --root and --workspace are mutually exclusive (use --root)" >&2
  exit 1
fi

if [[ -n $_workspace_arg ]]; then
  echo "warning: --workspace is deprecated; use --root instead" >&2
  _root_arg="$_workspace_arg"
fi

PN_WORKSPACE_ROOT=$(workspace_resolve_root "$_root_arg") || exit 1

overrides_json=$(workspace_parse_overrides "${_override_specs[@]}") || exit 1

workspace_json=$(workspace_get_projects "$PN_WORKSPACE_ROOT" "$overrides_json") || exit 1

# Full sweep: visit every project, accumulate failures, exit non-zero at end.
declare -a _failed=()
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  echo "  --== Flake check $project_name ==--  "
  if ! (cd "$project_path" && pn-ws-nix flake check); then
    _failed+=("$project_name")
  fi
  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')

if [[ ${#_failed[@]} -gt 0 ]]; then
  echo "FAIL: ${#_failed[@]} project(s) failed flake check: ${_failed[*]}" >&2
  exit 1
fi

echo "OK: all projects passed flake check"
```

- [ ] **Step 4: Write the package default.nix**

Create `modules/pn/pn-workspace-flake-check/default.nix`:

```nix
{
  mkBashScript,
  pkgs,
  pn-lib,
  pn-ws-nix,
  testSupport ? null,
}:

mkBashScript {
  name = "pn-workspace-flake-check";
  src = ./.;
  description = "Run nix flake check for all workspace repos via pn-ws-nix";
  libraries = [ pn-lib ];
  runtimeDeps = [
    pkgs.jq
    pn-ws-nix
  ];
  testDeps = [
    pkgs.jq
  ];
  inherit testSupport;
}
```

- [ ] **Step 5: Register in `modules/pn/scripts.nix`**

In `scripts.nix`, after the `pn-ws-nix` block, add:

```nix
  pn-workspace-flake-check = pkgs.callPackage ./pn-workspace-flake-check {
    inherit (bashBuilders) mkBashScript;
    inherit
      pkgs
      pn-lib
      pn-ws-nix
      testSupport
      ;
  };
```

Append `pn-workspace-flake-check` to `allScripts` and to the trailing `inherit ... ;` re-export.

- [ ] **Step 6: Build + run tests**

```bash
cd /Users/phillipg/phillipg_mbp/phillipg-nix-repo-base
pn-ws-nix build .#pn-workspace-flake-check --no-link --print-out-paths
pn-ws-nix build .#checks.aarch64-darwin.test-pn-workspace-flake-check --no-link 2>&1 | tail -10
pn-ws-nix flake check 2>&1 | tail -5
```

Expected: package builds, all bats tests pass, flake check succeeds. (`pn-ws-nix` is now available from Task 2's commit; use it instead of bare `nix`.)

- [ ] **Step 7: Commit**

```bash
git add modules/pn/pn-workspace-flake-check modules/pn/scripts.nix
git commit -m "pn-workspace-flake-check: cross-repo flake check via pn-ws-nix

Workspace-level companion to pn-workspace-pre-commit-check. Iterates each
project declared in pn-workspace.toml and runs \`pn-ws-nix flake check\`
per project. Full sweep: continues past per-project failures, exits
non-zero overall if any project failed."
```

---

## Task 4: Cross-repo doc sweep for the rename

**Files (across all repos under `~/phillipg_mbp/`):** any file containing the literal string `pn-workspace-check`.

The rename in Task 1 changed the command name. Any CLAUDE.md, README, ADR, or script that referenced the old name needs to be updated. Hard rename per spec — no shim is kept.

- [ ] **Step 1: Inventory remaining references**

```bash
cd /Users/phillipg/phillipg_mbp
grep -rln 'pn-workspace-check' \
  --exclude-dir=.git --exclude-dir=node_modules \
  --exclude-dir=.beads --exclude-dir=.gc \
  --exclude-dir=result --exclude-dir=.direnv \
  2>/dev/null | grep -v '/nix/store/' || echo "(no matches)"
```

Expected: a list of remaining files containing the old name across all workspace repos. Each must be updated.

- [ ] **Step 2: Per file, replace `pn-workspace-check` with `pn-workspace-pre-commit-check`**

For each file from Step 1, edit by hand (preserve surrounding context — some matches may be inside example code blocks where the old name is intentionally referenced as historical). Use `sed -i` only when you've reviewed the file and confirmed every match should change. Conservative pattern: open each file in turn and replace per-line.

If you're confident all references can be mechanically rewritten:

```bash
cd /Users/phillipg/phillipg_mbp
for f in $(grep -rln 'pn-workspace-check' \
              --exclude-dir=.git --exclude-dir=node_modules \
              --exclude-dir=.beads --exclude-dir=.gc \
              --exclude-dir=result --exclude-dir=.direnv \
              2>/dev/null | grep -v '/nix/store/'); do
  echo "Updating: $f"
  sed -i.bak 's/pn-workspace-check/pn-workspace-pre-commit-check/g' "$f"
  rm "${f}.bak"
done
```

- [ ] **Step 3: Verify no stray references remain**

```bash
cd /Users/phillipg/phillipg_mbp
grep -rln 'pn-workspace-check[^-]' \
  --exclude-dir=.git --exclude-dir=node_modules \
  --exclude-dir=.beads --exclude-dir=.gc \
  --exclude-dir=result --exclude-dir=.direnv \
  2>/dev/null | grep -v '/nix/store/' || echo "(none — rename complete)"
```

(The `[^-]` ensures we don't match the new name `pn-workspace-pre-commit-check` itself.)

Expected: no output — only `pn-workspace-pre-commit-check` references remain.

- [ ] **Step 4: Commit per-repo**

For each repo with changes, cd in and commit:

```bash
for repo in phillipg-nix-repo-base phillipgreenii-nix-overlay phillipgreenii-nix-personal phillipgreenii-nix-agent-support phillipgreenii-nix-support-apps phillipg-nix-ziprecruiter; do
  if [[ -d /Users/phillipg/phillipg_mbp/$repo ]]; then
    cd /Users/phillipg/phillipg_mbp/$repo
    if [[ -n $(git status --porcelain) ]]; then
      git add -A
      git commit -m "docs: rename pn-workspace-check → pn-workspace-pre-commit-check"
    fi
  fi
done
```

For `phillipg-nix-repo-base`, the change should be on the same `feat/pn-workspace-agent-conventions` branch (Tasks 1-3 are there). For other repos, the change can go directly to `main` (each repo has its own branching norms; if you're unsure, branch per-repo).

---

## Task 5: Add `pn-workspace-rules` plugin (agent-support repo)

**Files (in `phillipgreenii-nix-agent-support`):**

- Create: `home/programs/pn-workspace-rules/default.nix`
- Create: `home/programs/pn-workspace-rules/pn-workspace-rules.md`
- Modify: `home/default.nix` (add import)

Mirrors `home/programs/agent-rules/default.nix` exactly.

- [ ] **Step 1: Branch in agent-support**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git checkout -b feat/pn-workspace-rules
```

- [ ] **Step 2: Write `pn-workspace-rules.md`**

Create `home/programs/pn-workspace-rules/pn-workspace-rules.md` with the exact content from the spec's "Rule content" section. The file is the literal CLAUDE.md the plugin ships:

````markdown
# pn-workspace Conventions for Agents

Rules for AI agents working inside a `pn-workspace.toml` workspace. These apply to ANY repo whose flake is declared as a project in the workspace.

## Cardinal Rule

**Never modify `flake.nix` to point input URLs at local paths.** `pn-workspace-*` and `pn-ws-nix` inject `--override-input <name> <local-path>` at build/eval time — the lock file and flake.nix stay clean. Local-path URLs in `flake.nix` break every other consumer (CI, teammates, future you on another machine).

## Completion Gate

After completing any task in a project that participates in a `pn-workspace.toml`, you MUST run `pn-workspace-build` from the workspace root (or anywhere with `PN_WORKSPACE_ROOT` set) before declaring the task complete. Cross-project changes (a new flake output consumed by another workspace repo, for instance) only show up here.

```text
pn-workspace-build
```
````

Per-project `pn-ws-nix flake check` is necessary but not sufficient. Workspace-level build catches consumer-side breakage.

If `pn-workspace-build` fails, the task is not complete. Fix the failure (in this or the consuming project) and re-run.

## Builds and Validation

| Goal                                       | Use                                    | Don't use                                                                |
| ------------------------------------------ | -------------------------------------- | ------------------------------------------------------------------------ |
| Build the system (current host)            | `pn-workspace-build`                   | `darwin-rebuild build`, `nix build .#darwinConfigurations.<host>.system` |
| Activate the system                        | the **user** runs `pn-workspace-apply` | NEVER invoke from agent context                                          |
| Run `nix flake check` on a project         | `pn-ws-nix flake check`                | `nix flake check`                                                        |
| Run `nix flake check` across every project | `pn-workspace-flake-check`             | per-repo `nix flake check`                                               |
| Build a single package                     | `pn-ws-nix build .#<pkg>`              | `nix build .#<pkg>`                                                      |
| Evaluate an attribute                      | `pn-ws-nix eval .#<attr>`              | `nix eval .#<attr>`                                                      |
| Pre-commit checks across all repos         | `pn-workspace-pre-commit-check`        | per-repo `pre-commit run --all-files`                                    |
| Update flake locks across all repos        | `pn-workspace-update`                  | per-repo `nix flake update`                                              |

## When to Push

You don't need to push branches for builds to work. `pn-workspace-*` and `pn-ws-nix` operate on the local working tree. Push only when:

- The user explicitly asks.
- The work is ready for review/merge.

A failing remote build is **not** a reason to push agent-only branches.

## When `pn-ws-nix` Doesn't Apply

Two `nix` subcommands operate on lock state and override flags don't do anything useful:

- `nix flake update`
- `nix flake lock`

`pn-ws-nix` detects these and (by default) warns + exec's without overrides. Use `pn-workspace-update` for cross-repo lock refresh; use bare `nix flake lock` only when you specifically need single-repo lock manipulation.

## When `pn-ws-nix` Is Insufficient

Non-flake `nix` subcommands (`store *`, `profile list`, `log`, `key *`, `nar *`, `daemon`, `doctor`, `config show`) don't take `--override-input`. Use bare `nix` for those.

Interactive operations like `nix develop` / `nix shell` are user concerns; agents rarely need them.

## Command Surface Cheat-Sheet

Workspace-level (operate on every repo in the workspace):

```text
pn-workspace-build              Build the current host's system config
pn-workspace-apply              Activate (USER ONLY)
pn-workspace-pre-commit-check   Run pre-commit checks across all repos
pn-workspace-flake-check        Run `nix flake check` across all repos
pn-workspace-update             Refresh flake locks across all repos
pn-workspace-upgrade            Update + apply (USER ONLY for the apply step)
pn-workspace-rebase             Rebase each repo on its remote
pn-workspace-push               Push each repo (USER-INITIATED ONLY)
pn-workspace-status             Per-repo working-tree summary
```

Project-level workspace-aware (operate on one flake with overrides):

```text
pn-ws-nix <subcommand>          Generic wrapper around `nix`; injects overrides
```

````

- [ ] **Step 3: Write the HM module**

Create `home/programs/pn-workspace-rules/default.nix`:

```nix
{
  config,
  lib,
  ...
}:
let
  cfg = config.phillipgreenii.programs.claude;
  rulesFile = ./pn-workspace-rules.md;
in
{
  config = lib.mkIf cfg.enable {
    phillipgreenii.programs.claude.plugins.local.plugins.pn-workspace-rules = {
      description = "pn-workspace conventions for AI agents (cardinal rules + command surface cheat-sheet)";
      source = "pn-workspace-rules";
      enabledByDefault = true;
    };

    home.file.".local/share/pgii-local-plugins/pn-workspace-rules/CLAUDE.md".source = rulesFile;

    home.file.".local/share/pgii-local-plugins/pn-workspace-rules/.claude-plugin/plugin.json".text =
      builtins.toJSON
        {
          name = "pn-workspace-rules";
          inherit (cfg.plugins.local) version;
          description = "pn-workspace conventions for AI agents";
        };
  };
}
````

- [ ] **Step 4: Add to home/default.nix imports**

Modify `home/default.nix`. After `./programs/agent-rules`, add `./programs/pn-workspace-rules`:

```nix
_: {
  imports = [
    ./programs/agent-rules
    ./programs/pn-workspace-rules
    # ... existing other imports ...
  ];
}
```

(Confirm exact final form by reading the file first; preserve any existing import order.)

- [ ] **Step 5: Verify build**

```bash
cd /Users/phillipg/phillipg_mbp/phillipgreenii-nix-agent-support
git add -N home/programs/pn-workspace-rules home/default.nix
nix flake check --no-build 2>&1 | tail -5
```

Expected: succeeds. The `git add -N` is needed so the dirty tree sees the new files.

Also verify the materialized plugin paths would resolve. Eval against the existing ci-test darwin/nixos configurations (whichever this repo provides):

```bash
nix eval .#homeModules.default 2>&1 | tail -3 \
  || nix eval --raw .#homeModules.default --apply 'lib: "ok"' 2>&1 | tail -3
```

(The exact attribute path depends on agent-support's flake; the important thing is the module evaluates without error.)

- [ ] **Step 6: Commit**

```bash
git add home/programs/pn-workspace-rules home/default.nix
git commit -m "pn-workspace-rules: ship Claude Code plugin for workspace agent conventions

Auto-enabled via existing pgii-local-plugins marketplace pattern. Mirrors
the agent-rules plugin shape (HM module + ships a CLAUDE.md from a
checked-in markdown file). enabledByDefault = true; consumers get it
without opting in."
```

---

## Task 6: Integration verification

Cross-repo build to confirm nothing broke from the rename + new commands + new plugin.

- [ ] **Step 1: Workspace-level pre-commit + flake checks**

```bash
cd /Users/phillipg/phillipg_mbp
pn-workspace-pre-commit-check
pn-workspace-flake-check
```

Expected: both pass for every project. (`pn-workspace-pre-commit-check` confirms the rename + per-repo pre-commit setups still work; `pn-workspace-flake-check` confirms the new command works against real workspaces and that `pn-ws-nix` correctly resolves overrides.)

- [ ] **Step 2: Workspace-level system build**

```bash
pn-workspace-build
```

Expected: builds the current host's system config end-to-end using all workspace overrides. This is the completion gate per the new rules.

- [ ] **Step 3: Smoke test the new plugin materializes**

```bash
ls -la ~/.local/share/pgii-local-plugins/pn-workspace-rules/
cat ~/.local/share/pgii-local-plugins/pn-workspace-rules/.claude-plugin/plugin.json
head -20 ~/.local/share/pgii-local-plugins/pn-workspace-rules/CLAUDE.md
```

(This only verifies after `pn-workspace-apply` has been run by the user. If not yet applied, this step is informational — the materialization happens at home-manager activation.)

- [ ] **Step 4: Commit any incidental fixes uncovered by the integration**

If Steps 1-2 surfaced fixes (e.g. a missed reference, a stale script not caught by the grep in Task 4), commit them to the appropriate repo with descriptive messages.

---

## Task 7: Merge back to main per repo

User's policy: "rebase and merge all of your work back to main; do not push from agents."

For each repo with a feature branch (likely `phillipg-nix-repo-base` and `phillipgreenii-nix-agent-support`):

- [ ] **Step 1: Verify branches are clean**

```bash
for repo in phillipg-nix-repo-base phillipgreenii-nix-agent-support; do
  cd /Users/phillipg/phillipg_mbp/$repo
  echo "=== $repo ==="
  git status --short
  git log --oneline main..HEAD | head
done
```

Expected: each branch has the relevant commits, no dirty working tree.

- [ ] **Step 2: ff-merge to local main, delete local feature branch**

```bash
for repo in phillipg-nix-repo-base phillipgreenii-nix-agent-support; do
  cd /Users/phillipg/phillipg_mbp/$repo
  feature=$(git branch --show-current)
  if [[ "$feature" == "main" ]]; then
    echo "$repo already on main; skipping"
    continue
  fi
  git checkout main
  git merge --ff-only "$feature"
  git branch -d "$feature"
done
```

Expected: ff-merges succeed; feature branches removed locally.

- [ ] **Step 3: Report final state to the user**

```bash
for repo in phillipg-nix-repo-base phillipgreenii-nix-agent-support; do
  cd /Users/phillipg/phillipg_mbp/$repo
  echo "=== $repo ==="
  git branch
  git log --oneline -5
done
```

Do NOT push. The user pushes when ready.

---

## Self-review

- **Spec coverage:**
  - `pn-ws-nix` design (flag, env var, deny-list, action precedence) → Task 2.
  - `pn-workspace-flake-check` (full sweep, summary, non-zero on any failure) → Task 3.
  - Rename `pn-workspace-check` → `pn-workspace-pre-commit-check` → Tasks 1 + 4 (in-repo + cross-repo sweep).
  - `pn-workspace-rules` plugin (HM module, CLAUDE.md, enabledByDefault=true) → Task 5.
  - Naming convention (`pn-workspace-*` workspace-level, `pn-ws-*` project-level workspace-aware) → reflected in directory and command names throughout.
  - Test strategy (bats for both new scripts, full sweep aggregation tested) → Tasks 2 and 3 each include bats tests.
  - Migration policy (hard rename, loud break) → Task 4 enforces.

- **Placeholders:** none. Every step has the exact code, command, or content needed.

- **Type/name consistency:**
  - `pn-ws-nix`, `pn-workspace-flake-check`, `pn-workspace-pre-commit-check` are used identically across Tasks 1-7 and in the plugin's CLAUDE.md.
  - `--non-override-subcommand-action` flag name + `PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION` env var name match between the script (Task 2 Step 3), tests (Task 2 Step 1), and spec.
  - Override URI format `git+file://<path>` matches the existing `pn-workspace-build` pattern; consistent in `pn-ws-nix.sh`.
