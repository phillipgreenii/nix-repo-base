# update-locks resilience and dev-shell wrapping — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `update-locks.sh` resilient to host-tool absence by re-execing through each flake's `devShells.default`, and make `pn-workspace-update` continue past per-project failures while still failing the run overall.

**Architecture:** Add one new function `ul_reexec_in_dev_shell` to the shared bash lib. Convert two nix-wrapped `update-locks.sh` shims (personal, agent-support) into standalone bash scripts matching the others. Give each flake's `devShells.default` the tools its `update-locks.sh` needs (nodejs/uv/go/jq/curl/gnused as applicable). Wire the new helper into all six scripts. Rewrite the per-project loop in `pn-workspace-update.sh` to aggregate failures instead of exiting on the first one.

**Tech Stack:** bash, bats (testing), nix flakes, `pkgs.mkShell`, `nix develop`.

**Spec:** `phillipg-nix-repo-base/docs/superpowers/specs/2026-05-29-update-locks-resilience-design.md`

---

## Task 1: Add `ul_reexec_in_dev_shell` to shared lib (TDD)

**Files:**

- Modify: `phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash` (append new function before `ul_setup`)
- Modify: `phillipg-nix-repo-base/lib/tests/test-update-locks-lib.bats` (append new tests at end)

### Step 1.1: Write the failing tests

Append the following to `phillipg-nix-repo-base/lib/tests/test-update-locks-lib.bats` (after the final `@test`):

- [ ] **Step 1.1: Add the three new tests**

```bash
# --- ul_reexec_in_dev_shell ---

@test "ul_reexec_in_dev_shell returns 0 without exec when IN_NIX_SHELL is set" {
  source "$UL_LOCKS_LIB"
  export IN_NIX_SHELL=impure

  run bash -c "
    export IN_NIX_SHELL=impure
    source '$UL_LOCKS_LIB'
    ul_reexec_in_dev_shell
    echo POST_CALL
  "
  [ "$status" -eq 0 ]
  [[ "$output" =~ "already in nix shell" ]]
  [[ "$output" =~ "POST_CALL" ]]
}

@test "ul_reexec_in_dev_shell warns and returns 0 when nix develop probe fails" {
  # Mock nix so that `nix develop ... --command true` exits non-zero
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" ]]; then
  exit 1
fi
exit 0
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  run bash -c "
    unset IN_NIX_SHELL
    source '$UL_LOCKS_LIB'
    ul_reexec_in_dev_shell
    echo POST_CALL
  "
  [ "$status" -eq 0 ]
  [[ "$output" =~ "WARNING" ]]
  [[ "$output" =~ "falling back" ]]
  [[ "$output" =~ "POST_CALL" ]]
}

@test "ul_reexec_in_dev_shell execs into nix develop when probe succeeds" {
  # Mock nix:
  #   - `nix develop <dir> --command true` -> exit 0 (probe ok)
  #   - `nix develop <dir> --command bash <script> <args...>` -> echo what it would run and exit
  cat > "$MOCK_BIN/nix" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == "develop" && "$3" == "--command" && "$4" == "true" ]]; then
  exit 0
fi
if [[ "$1" == "develop" && "$3" == "--command" && "$4" == "bash" ]]; then
  shift 4
  echo "REEXEC: $*"
  exit 0
fi
exit 99
MOCK
  _fix_mock_shebang "$MOCK_BIN/nix"
  chmod +x "$MOCK_BIN/nix"

  cat > "$TEST_DIR/wrap-test.sh" <<SCRIPT
#!/usr/bin/env bash
source "$UL_LOCKS_LIB"
ul_reexec_in_dev_shell "\$@"
echo FALLTHROUGH
SCRIPT
  _fix_mock_shebang "$TEST_DIR/wrap-test.sh"
  chmod +x "$TEST_DIR/wrap-test.sh"

  run env -u IN_NIX_SHELL "$TEST_DIR/wrap-test.sh" arg1 arg2
  [ "$status" -eq 0 ]
  [[ "$output" =~ "entering dev shell" ]]
  [[ "$output" =~ "REEXEC:" ]]
  [[ "$output" =~ "wrap-test.sh arg1 arg2" ]]
  # exec replaced the shell, so FALLTHROUGH must NOT appear
  [[ ! "$output" =~ "FALLTHROUGH" ]]
}
```

- [ ] **Step 1.2: Run the new tests to verify they fail**

Run from inside `phillipg-nix-repo-base/`:

```bash
cd phillipg-nix-repo-base
bats lib/tests/test-update-locks-lib.bats --filter ul_reexec_in_dev_shell
```

Expected: 3 failing tests with messages like `ul_reexec_in_dev_shell: command not found`.

- [ ] **Step 1.3: Implement `ul_reexec_in_dev_shell`**

Insert the following function into `phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash` immediately before the `ul_setup()` definition (around line 92):

```bash
# Re-exec the calling script inside its flake's devShells.default if possible.
# Safe to call from any update-locks.sh as the first thing after sourcing this lib.
# Behavior:
#   - If IN_NIX_SHELL is already set, prints a notice and returns 0 (no re-exec).
#   - Probes `nix develop <script_dir> --command true`; if that fails (broken flake),
#     prints a warning and returns 0 so the script can still run with host tooling.
#   - Otherwise execs the script inside `nix develop ... --command bash`. Does not return.
ul_reexec_in_dev_shell() {
  local script="$0"
  local script_dir
  script_dir="$(cd "$(dirname "$script")" && pwd)"

  if [[ -n ${IN_NIX_SHELL:-} ]]; then
    echo "==> already in nix shell (IN_NIX_SHELL=$IN_NIX_SHELL); using current shell" >&2
    return 0
  fi

  echo "==> entering dev shell at $script_dir..." >&2
  if ! nix develop "$script_dir" --command true >/dev/null 2>&1; then
    echo "WARNING: nix develop failed at $script_dir — falling back to system tools" >&2
    return 0
  fi

  exec nix develop "$script_dir" --command bash "$script" "$@"
}
```

- [ ] **Step 1.4: Run the new tests to verify they pass**

```bash
cd phillipg-nix-repo-base
bats lib/tests/test-update-locks-lib.bats --filter ul_reexec_in_dev_shell
```

Expected: 3 tests pass.

- [ ] **Step 1.5: Run the full bats suite to verify no regressions**

```bash
cd phillipg-nix-repo-base
bats lib/tests/test-update-locks-lib.bats
```

Expected: all tests pass (existing + 3 new).

- [ ] **Step 1.6: Commit**

```bash
cd phillipg-nix-repo-base
git add lib/scripts/update-locks-lib.bash lib/tests/test-update-locks-lib.bats
git commit -m "feat(update-locks-lib): add ul_reexec_in_dev_shell helper"
```

---

## Task 2: Migrate `phillipgreenii-nix-personal/update-locks.sh` to standalone bash

**Why:** Today `phillipgreenii-nix-personal/update-locks.sh` is a one-line shim `exec nix run .#update-locks -- "$@"` that delegates to a `writeShellApplication` in `flake.nix`. The plan needs it to be a normal bash script so `ul_reexec_in_dev_shell` can re-exec it inside the dev shell. The dev shell already has the only tool this flake's update needs (`nix`).

**Files:**

- Modify: `phillipgreenii-nix-personal/update-locks.sh` (replace 4-line shim with full standalone script)
- Modify: `phillipgreenii-nix-personal/flake.nix` (remove the `update-locks = pkgs.writeShellApplication { ... }` block)

- [ ] **Step 2.1: Rewrite `update-locks.sh`**

Overwrite `phillipgreenii-nix-personal/update-locks.sh` with:

```bash
#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
-h | --help)
  echo "Usage: $0 [--ci]"
  echo "  --ci  Disable laptop-only checks (nix daemon health, time-based cache)"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  echo "Usage: $0 [--ci]" >&2
  exit 1
  ;;
esac

# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_setup "phillipgreenii-nix-personal" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_finalize
```

Ensure it's executable:

```bash
chmod +x phillipgreenii-nix-personal/update-locks.sh
```

- [ ] **Step 2.2: Remove the `update-locks` package from `flake.nix`**

In `phillipgreenii-nix-personal/flake.nix`, locate the block (around lines 137–161):

```nix
update-locks = pkgs.writeShellApplication {
  name = "update-locks";
  runtimeInputs = [
    pkgs.nix
    pkgs.git
    pkgs.coreutils
  ];
  text = ''
    # shellcheck source=/dev/null
    source "${phillipgreenii-nix-base}/lib/scripts/update-locks-lib.bash"
    ul_setup "phillipgreenii-nix-personal" "$PWD"

    case "''${1:-}" in
    --ci) export UL_CI_MODE=true ;;
    "") ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac

    ul_run_step "nix-flake-update" \
      "update-locks: update nix flake.lock" \
      nix flake update

    ul_finalize
  '';
};
```

Delete the entire block. If removing it leaves the surrounding `packages = { ... }` attrset with only `test-update-locks-lib`, keep `test-update-locks-lib`; don't remove it.

- [ ] **Step 2.3: Verify the flake still evaluates**

```bash
cd phillipgreenii-nix-personal
nix flake check --no-build 2>&1 | tail -20
```

Expected: no evaluation errors (build steps may take a while; `--no-build` skips heavy checks).

- [ ] **Step 2.4: Manually run the standalone script**

```bash
cd phillipgreenii-nix-personal
./update-locks.sh --help
```

Expected: prints the usage block and exits 0.

- [ ] **Step 2.5: Commit**

```bash
cd phillipgreenii-nix-personal
git add update-locks.sh flake.nix
git commit -m "refactor(update-locks): convert to standalone bash script"
```

---

## Task 3: Migrate `phillipgreenii-nix-agent-support/update-locks.sh` to standalone bash + add `pkgs.go` to dev shell

**Why:** Same pattern as Task 2, but this flake has six update steps including three Go vendor-hash bumps. Those need `go` on PATH, which is currently in `runtimeInputs`. We move that to the dev shell.

**Files:**

- Modify: `phillipgreenii-nix-agent-support/update-locks.sh` (replace shim with full script)
- Modify: `phillipgreenii-nix-agent-support/flake.nix` (remove `update-locks` package, add `pkgs.go` to dev shell)

- [ ] **Step 3.1: Rewrite `update-locks.sh`**

Overwrite `phillipgreenii-nix-agent-support/update-locks.sh` with:

```bash
#!/usr/bin/env bash
# Standalone developer utility — not Nix-wrapped intentionally
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."

case "${1:-}" in
--ci)
  export UL_CI_MODE=true
  shift
  ;;
-h | --help)
  echo "Usage: $0 [--ci]"
  echo "  --ci  Disable laptop-only checks (nix daemon health, time-based cache)"
  exit 0
  ;;
"") ;;
*)
  echo "Unknown argument: $1" >&2
  echo "Usage: $0 [--ci]" >&2
  exit 1
  ;;
esac

# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_setup "phillipgreenii-nix-agent-support" "${SCRIPT_DIR}"

ul_run_step "nix-flake-update" \
  "update-locks: update nix flake.lock" \
  nix flake update

ul_run_step "update-deps-claude-extended-tool-approver" \
  "update-locks: update claude-extended-tool-approver Go deps + vendorHash" \
  bash -c 'cd packages/claude-extended-tool-approver && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pg-pr" \
  "update-locks: update pg-pr Go deps + vendorHash" \
  bash -c 'cd packages/pg-pr && go get -u ./... && ./update-deps.sh'

ul_run_step "update-deps-pa-monitor" \
  "update-locks: update pa-monitor Go deps + vendorHash" \
  bash -c 'cd packages/pa-monitor && go get -u ./... && ./update-deps.sh'

ul_run_step "update-goccc" \
  "update-locks: bump goccc rev + src hash" \
  nix run nixpkgs#nix-update -- -F goccc

ul_run_step "update-toktrack" \
  "update-locks: bump toktrack rev + src hash + cargoHash" \
  nix run nixpkgs#nix-update -- -F toktrack

ul_finalize
```

```bash
chmod +x phillipgreenii-nix-agent-support/update-locks.sh
```

- [ ] **Step 3.2: Remove the `update-locks` package and add `pkgs.go` to the dev shell**

In `phillipgreenii-nix-agent-support/flake.nix`:

(a) Delete the entire `update-locks = pkgs.writeShellApplication { ... };` block (around lines 533–578).

(b) Locate the `devShells.default` definition (around line 597):

```nix
devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
  inherit pkgs;
  pre-commit-shellHook = pre-commit.shellHook;
};
```

Change to:

```nix
devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
  inherit pkgs;
  pre-commit-shellHook = pre-commit.shellHook;
  extraInputs = [ pkgs.go ];
};
```

- [ ] **Step 3.3: Verify the flake evaluates**

```bash
cd phillipgreenii-nix-agent-support
nix flake check --no-build 2>&1 | tail -20
```

Expected: no evaluation errors.

- [ ] **Step 3.4: Verify the dev shell has `go`**

```bash
cd phillipgreenii-nix-agent-support
nix develop --command go version
```

Expected: prints a `go version go1.xx ...` line.

- [ ] **Step 3.5: Manually run the standalone script**

```bash
cd phillipgreenii-nix-agent-support
./update-locks.sh --help
```

Expected: prints the usage block and exits 0.

- [ ] **Step 3.6: Commit**

```bash
cd phillipgreenii-nix-agent-support
git add update-locks.sh flake.nix
git commit -m "refactor(update-locks): convert to standalone bash + go in devShell"
```

---

## Task 4: Add `nodejs`, `prefetch-npm-deps`, `uv` to `phillipgreenii-nix-support-apps` dev shell

**Why:** `support-apps/update-locks.sh` runs `packages/jsonl-log-parser/update-deps.sh` (calls `npm update` and `nix run nixpkgs#prefetch-npm-deps`) and `uv lock --upgrade` for two python packages. After Task 6 wires the dev-shell wrap, these tools need to be in `devShells.default`.

**Files:**

- Modify: `phillipgreenii-nix-support-apps/flake.nix` (`devShells.default` definition)

- [ ] **Step 4.1: Locate the dev shell definition**

In `phillipgreenii-nix-support-apps/flake.nix`, find the `devShells = { ... }` attrset. (There are multiple dev shells defined; you want the `default` one.) Confirm whether it currently uses `phillipgreenii-nix-base.lib.mkDevShell` or `pkgs.mkShell` directly.

- [ ] **Step 4.2: Add the three packages to its inputs**

If the shape is `mkDevShell { ... }`:

```nix
devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
  inherit pkgs;
  pre-commit-shellHook = pre-commit.shellHook;
  extraInputs = [
    pkgs.nodejs
    pkgs.prefetch-npm-deps
    pkgs.uv
  ];
};
```

If the shape is `pkgs.mkShell { buildInputs = [...]; ... }`, append `pkgs.nodejs pkgs.prefetch-npm-deps pkgs.uv` to `buildInputs`.

- [ ] **Step 4.3: Verify the flake evaluates and the tools are present**

```bash
cd phillipgreenii-nix-support-apps
nix flake check --no-build 2>&1 | tail -20
nix develop --command bash -c 'command -v node && command -v npm && command -v prefetch-npm-deps && command -v uv'
```

Expected: all four `command -v` lines print absolute paths inside `/nix/store/...`.

- [ ] **Step 4.4: Commit**

```bash
cd phillipgreenii-nix-support-apps
git add flake.nix
git commit -m "feat(devShell): add nodejs, prefetch-npm-deps, uv for update-locks"
```

---

## Task 5: Add `jq`, `curl`, `gnused` to `phillipgreenii-nix-overlay` dev shell

**Why:** `overlay/update-locks.sh` calls `nix run nixpkgs#nix-prefetch-github`, then pipes its output through `jq`, calls `curl` against the GitHub API, and uses `sed` to rewrite hashes. After Task 6 wires the dev-shell wrap, these tools need to be in `devShells.default`.

**Files:**

- Modify: `phillipgreenii-nix-overlay/flake.nix`

- [ ] **Step 5.1: Locate the dev shell definition**

In `phillipgreenii-nix-overlay/flake.nix` find:

```nix
devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
  ...
};
```

- [ ] **Step 5.2: Add the three packages via `extraInputs`**

```nix
devShells.default = phillipgreenii-nix-base.lib.mkDevShell {
  inherit pkgs;
  pre-commit-shellHook = pre-commit.shellHook;
  extraInputs = [
    pkgs.jq
    pkgs.curl
    pkgs.gnused
  ];
};
```

If `extraInputs` is already set, append these three to its list.

- [ ] **Step 5.3: Verify**

```bash
cd phillipgreenii-nix-overlay
nix flake check --no-build 2>&1 | tail -20
nix develop --command bash -c 'command -v jq && command -v curl && command -v sed'
```

Expected: all three `command -v` lines print absolute paths inside `/nix/store/...`.

- [ ] **Step 5.4: Commit**

```bash
cd phillipgreenii-nix-overlay
git add flake.nix
git commit -m "feat(devShell): add jq, curl, gnused for update-locks"
```

---

## Task 6: Wire `ul_reexec_in_dev_shell` into all six `update-locks.sh` files

**Why:** With the helper defined (Task 1), the migrations done (Tasks 2-3), and the dev shells properly stocked (Tasks 4-5), every `update-locks.sh` can now opt into the dev-shell wrap. This task adds the single new line to each script.

**Files:**

- Modify: `phillipg-nix-repo-base/update-locks.sh`
- Modify: `phillipgreenii-nix-overlay/update-locks.sh`
- Modify: `phillipg-nix-ziprecruiter/update-locks.sh`
- Modify: `phillipgreenii-nix-personal/update-locks.sh`
- Modify: `phillipgreenii-nix-agent-support/update-locks.sh`
- Modify: `phillipgreenii-nix-support-apps/update-locks.sh`

- [ ] **Step 6.1: Modify `phillipg-nix-repo-base/update-locks.sh`**

This script doesn't currently define `WORKSPACE_ROOT` — it sources the lib directly via `SCRIPT_DIR`. The wrap is added after the source. Find the lines:

```bash
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/scripts/update-locks-lib.bash"
ul_setup "phillipgreenii-nix-repo-base" "${SCRIPT_DIR}"
```

Insert one line between them:

```bash
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-repo-base" "${SCRIPT_DIR}"
```

- [ ] **Step 6.2: Modify `phillipgreenii-nix-overlay/update-locks.sh`**

Insert `ul_reexec_in_dev_shell "$@"` between the `source` and `ul_setup` lines (around lines 26-27):

```bash
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-overlay" "${SCRIPT_DIR}"
```

(Note: this file sources from `${SCRIPT_DIR}/lib/...` not `${WORKSPACE_ROOT}/...`. If you see the latter, use that.)

- [ ] **Step 6.3: Modify `phillipg-nix-ziprecruiter/update-locks.sh`**

```bash
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipg-nix-ziprecruiter" "${SCRIPT_DIR}"
```

- [ ] **Step 6.4: Modify `phillipgreenii-nix-personal/update-locks.sh`**

```bash
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-personal" "${SCRIPT_DIR}"
```

- [ ] **Step 6.5: Modify `phillipgreenii-nix-agent-support/update-locks.sh`**

```bash
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-agent-support" "${SCRIPT_DIR}"
```

- [ ] **Step 6.6: Modify `phillipgreenii-nix-support-apps/update-locks.sh`**

```bash
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "phillipgreenii-nix-support-apps" "${SCRIPT_DIR}"
```

- [ ] **Step 6.7: Smoke-test one of the simpler scripts end-to-end**

Pick `phillipg-nix-repo-base/update-locks.sh` (only does `nix flake update`):

```bash
cd phillipg-nix-repo-base
# Ensure working tree is clean first (commit or stash any changes from Task 1)
git status --short
./update-locks.sh
```

Expected output includes:

```
==> entering dev shell at /Users/.../phillipg-nix-repo-base...
==> nix-flake-update...
...
=== Update Summary ===
  Ran:     1
  Passed:  1
  ...
✓ All steps completed successfully!
```

Also test the IN_NIX_SHELL short-circuit:

```bash
cd phillipg-nix-repo-base
nix develop --command ./update-locks.sh
```

Expected: prints `==> already in nix shell (IN_NIX_SHELL=impure); using current shell` instead of `entering dev shell...`.

- [ ] **Step 6.8: Commit all six files**

Commit each per-repo (since they're in different git repos):

```bash
cd phillipg-nix-repo-base
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"

cd ../phillipgreenii-nix-overlay
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"

cd ../phillipg-nix-ziprecruiter
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"

cd ../phillipgreenii-nix-personal
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"

cd ../phillipgreenii-nix-agent-support
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"

cd ../phillipgreenii-nix-support-apps
git add update-locks.sh
git commit -m "feat(update-locks): wire dev-shell re-exec"
```

---

## Task 7: Rewrite `pn-workspace-update.sh` for inter-project failure aggregation (TDD)

**Why:** Today `pn-workspace-update.sh` exits on the first failing project, so a single broken repo blocks updates to all the others. Change it to record per-project failures, continue iterating, regenerate the workspace lock, and exit 1 only at the end if any project failed.

**Files:**

- Modify: `phillipg-nix-repo-base/modules/pn/pn-workspace-update/pn-workspace-update.sh`
- Modify: `phillipg-nix-repo-base/modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats`

### Step 7.1–7.2: Add failing tests

- [ ] **Step 7.1: Add three new tests**

Append the following to `phillipg-nix-repo-base/modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats`:

```bash
@test "pn-workspace-update continues past a failing project and reports it" {
  cat >"$TEST_DIR/workspace/repo-base/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
echo "Mock: repo-base update-locks.sh failed"
exit 1
EOF
  chmod +x "$TEST_DIR/workspace/repo-base/update-locks.sh"
  # terminal-flake's update-locks.sh stays as the default success stub

  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  echo "$output" | grep -q "Update repo-base"
  echo "$output" | grep -q "Update terminal-flake"
  echo "$output" | grep -q "Failed projects"
  echo "$output" | grep -q "repo-base"
}

@test "pn-workspace-update regenerates workspace lock even when a project fails" {
  cat >"$TEST_DIR/workspace/repo-base/update-locks.sh" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
  chmod +x "$TEST_DIR/workspace/repo-base/update-locks.sh"

  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 1 ]
  [ -f "$TEST_DIR/workspace/pn-workspace.lock" ]
  echo "$output" | grep -q "Regenerating workspace lock"
}

@test "pn-workspace-update exits 0 when all projects succeed" {
  # Default stubs both succeed
  run bash -c "
    source '${LIB_PATH%%:*}'
    cd '$TEST_DIR/workspace'
    source '$SCRIPTS_DIR/pn-workspace-update.sh'
  "
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "All projects updated successfully"
  ! echo "$output" | grep -q "Failed projects"
}
```

- [ ] **Step 7.2: Run the new tests to verify they fail**

```bash
cd phillipg-nix-repo-base
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats --filter "continues past\|regenerates workspace lock even\|exits 0 when all"
```

Expected: the first two tests fail (current behavior exits on first failure, so `terminal-flake` never runs and there's no `Failed projects` block). The third may already pass but we want it as a regression guard.

### Step 7.3–7.5: Implement the new loop

- [ ] **Step 7.3: Rewrite the main loop in `pn-workspace-update.sh`**

In `phillipg-nix-repo-base/modules/pn/pn-workspace-update/pn-workspace-update.sh`, replace lines 110–141 (the existing `while IFS= read -r project_path; do ... done` block) with the following. Keep everything above (arg parse, workspace resolution, trap setup) and below (the `pn-discover-workspace` lock regeneration) intact, except move that lock-regeneration block to be unconditional and add the failure summary after it:

Replace:

```bash
while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || exit 1

  if workspace_has_upstream; then
    git pull --rebase --autostash &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  fi

  ./update-locks.sh &
  _child_pid=$!
  wait "$_child_pid" || exit $?
  _child_pid=""

  if workspace_has_upstream; then
    git push &
    _child_pid=$!
    wait "$_child_pid" || exit $?
    _child_pid=""
  else
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
  fi

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

# Regenerate lock file so pn-workspace.lock reflects any repos added since last update
echo "  --== Regenerating workspace lock ==--  "
pn-discover-workspace "$PN_WORKSPACE_ROOT" >"$PN_WORKSPACE_ROOT/pn-workspace.lock"
echo
```

With:

```bash
failed_projects=()

# Run a command, tracking it as the current child so the existing signal traps
# (_cleanup) can kill it. Returns the command's exit code; never exits early.
_run_step() {
  local label="$1"
  shift
  "$@" &
  _child_pid=$!
  local rc=0
  wait "$_child_pid" || rc=$?
  _child_pid=""
  if [[ $rc -ne 0 ]]; then
    echo "  ✗ $label failed for $_current_project (exit $rc)" >&2
  fi
  return $rc
}

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || {
    failed_projects+=("$project_name (cd failed)")
    echo
    continue
  }

  pull_failed=false
  project_failed=false

  if workspace_has_upstream; then
    if ! _run_step "git pull" git pull --rebase --autostash; then
      pull_failed=true
      project_failed=true
    fi
  fi

  # Skip update-locks if pull failed: the working tree is suspect.
  if ! $pull_failed; then
    if ! _run_step "update-locks" ./update-locks.sh; then
      project_failed=true
      # but keep going to push the steps that committed successfully
    fi
  fi

  # Push only when pull succeeded. Push even on partial update-locks failure —
  # each ul_run_step commits atomically, so successful work should land remotely.
  if workspace_has_upstream && ! $pull_failed; then
    if ! _run_step "git push" git push; then
      project_failed=true
    fi
  elif ! workspace_has_upstream; then
    _branch=$(git branch --show-current)
    _branch_label="${_branch:-DETACHED HEAD}"
    echo "no upstream for branch '$_branch_label' — skipping pull/push for $project_name"
  fi

  if $project_failed; then
    failed_projects+=("$project_name")
  fi

  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

# Regenerate lock file even if some projects failed — captures whatever did update.
echo "  --== Regenerating workspace lock ==--  "
pn-discover-workspace "$PN_WORKSPACE_ROOT" >"$PN_WORKSPACE_ROOT/pn-workspace.lock"
echo

if [[ ${#failed_projects[@]} -gt 0 ]]; then
  echo "=== Failed projects (${#failed_projects[@]}) ==="
  for p in "${failed_projects[@]}"; do
    echo "  ✗ $p"
  done
  exit 1
fi

echo "✓ All projects updated successfully"
```

- [ ] **Step 7.4: Run the new tests to verify they pass**

```bash
cd phillipg-nix-repo-base
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats --filter "continues past\|regenerates workspace lock even\|exits 0 when all"
```

Expected: all three new tests pass.

- [ ] **Step 7.5: Run the full pn-workspace-update test suite**

```bash
cd phillipg-nix-repo-base
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
```

Expected: all tests pass. If any pre-existing test fails because it expected the old early-exit behavior, examine carefully — the test may need updating to match the new aggregate-then-exit behavior (in which case update it and document why in the commit message). Do NOT just delete failing tests.

- [ ] **Step 7.6: Commit**

```bash
cd phillipg-nix-repo-base
git add modules/pn/pn-workspace-update/pn-workspace-update.sh \
        modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
git commit -m "feat(pn-workspace-update): aggregate per-project failures instead of early exit"
```

---

## Task 8: Add bats test for `pn-workspace-upgrade` apply-gating behavior

**Why:** `pn-workspace-upgrade.sh` uses `pn-workspace-update "${update_args[@]}" && pn-workspace-apply "${apply_args[@]}"` so apply only runs when update succeeds. With Task 7's change, update will now sometimes exit non-zero while leaving partial work committed — we want a test that pins down "apply is skipped on update failure" so a future refactor doesn't accidentally regress it.

**Files:**

- Modify: `phillipg-nix-repo-base/modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats`

- [ ] **Step 8.1: Inspect the existing test file**

```bash
cd phillipg-nix-repo-base
bats modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats --tap | head -20
```

Look at the file to see how it mocks `pn-workspace-update` and `pn-workspace-apply`. Match that pattern in the new test.

- [ ] **Step 8.2: Add the new test**

Append to `phillipg-nix-repo-base/modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats`:

```bash
@test "pn-workspace-upgrade does not run apply when update fails" {
  # Mock pn-workspace-update to exit non-zero
  cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update failing"
exit 1
EOF
  chmod +x "$TEST_DIR/pn-workspace-update"

  # Mock pn-workspace-apply to record if it ran
  cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "APPLY_RAN"
EOF
  chmod +x "$TEST_DIR/pn-workspace-apply"

  export PATH="$TEST_DIR:$PATH"

  run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
  [ "$status" -ne 0 ]
  echo "$output" | grep -q "pn-workspace-update failing"
  ! echo "$output" | grep -q "APPLY_RAN"
}

@test "pn-workspace-upgrade runs apply when update succeeds" {
  cat >"$TEST_DIR/pn-workspace-update" <<'EOF'
#!/usr/bin/env bash
echo "Mock: pn-workspace-update succeeding"
exit 0
EOF
  chmod +x "$TEST_DIR/pn-workspace-update"

  cat >"$TEST_DIR/pn-workspace-apply" <<'EOF'
#!/usr/bin/env bash
echo "APPLY_RAN"
EOF
  chmod +x "$TEST_DIR/pn-workspace-apply"

  export PATH="$TEST_DIR:$PATH"

  run bash "$SCRIPTS_DIR/pn-workspace-upgrade.sh"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "APPLY_RAN"
}
```

Adjust `$SCRIPTS_DIR` / `$TEST_DIR` / `$LIB_PATH` references to match the conventions already used in this test file (see the existing tests at the top of the file).

- [ ] **Step 8.3: Run the tests**

```bash
cd phillipg-nix-repo-base
bats modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats
```

Expected: all tests pass, including the two new ones.

- [ ] **Step 8.4: Commit**

```bash
cd phillipg-nix-repo-base
git add modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats
git commit -m "test(pn-workspace-upgrade): pin apply-gating behavior on update failure"
```

---

## Final verification

After all tasks complete:

- [ ] **Run all bats tests**

```bash
cd phillipg-nix-repo-base
bats lib/tests/test-update-locks-lib.bats
bats modules/pn/pn-workspace-update/tests/test-pn-workspace-update.bats
bats modules/pn/pn-workspace-upgrade/tests/test-pn-workspace-upgrade.bats
```

Expected: all green.

- [ ] **Run `nix flake check --no-build` in every modified flake**

```bash
for d in phillipg-nix-repo-base phillipgreenii-nix-overlay phillipg-nix-ziprecruiter phillipgreenii-nix-personal phillipgreenii-nix-agent-support phillipgreenii-nix-support-apps; do
  echo "=== $d ==="
  (cd ~/phillipg_mbp/$d && nix flake check --no-build 2>&1 | tail -5)
done
```

Expected: each prints no errors.

- [ ] **End-to-end smoke: run `pn-workspace-update` on the live workspace**

```bash
cd ~/phillipg_mbp
pn-workspace-update
```

Expected behavior:

- Each repo's `update-locks.sh` prints `==> entering dev shell at ...`
- If any repo fails (e.g., `support-apps` if `nodejs` isn't yet in its dev shell), the run continues to the next repo
- At the end, a `=== Failed projects (N) ===` block lists every failure, and the overall exit code is 1 if anything failed
- `pn-workspace.lock` is regenerated in either case

- [ ] **Optional: verify `pn-workspace-upgrade` skips apply when something fails**

If a repo fails the update, run `pn-workspace-upgrade` and confirm `pn-workspace-apply` is not invoked.

---

## Self-review notes

**Spec coverage:**

- §"Convert all `update-locks.sh` to standalone bash scripts" → Tasks 2, 3
- §"Re-exec each `update-locks.sh` inside its flake's `devShells.default`" → Tasks 1, 6
- §"Add the per-flake update-locks tools to `devShells.default` via `extraInputs`" → Tasks 3 (go), 4 (nodejs/prefetch-npm-deps/uv), 5 (jq/curl/gnused)
- §"Rewrite `pn-workspace-update.sh` to aggregate per-project failures" → Task 7
- §"No change to `pn-workspace-upgrade.sh`" → Task 8 adds a regression test only

**Push-on-partial-success rule:** Task 7 Step 7.3 implements this — `_run_step "git push"` runs whenever pull succeeded, regardless of update-locks's outcome.

**Pull-failure-skips-push rule:** Task 7 Step 7.3 implements this — the push branch is gated on `! $pull_failed`.

**Always-regenerate-lock rule:** Task 7 Step 7.3 unconditionally runs `pn-discover-workspace` after the loop, even when `failed_projects` is non-empty.
