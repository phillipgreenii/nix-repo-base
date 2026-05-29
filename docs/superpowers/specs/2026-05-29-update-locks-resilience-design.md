# update-locks resilience and dev-shell wrapping

**Status**: Draft
**Date**: 2026-05-29
**Scope**: Cross-repo (`phillipg-nix-repo-base`, `phillipgreenii-nix-overlay`, `phillipg-nix-ziprecruiter`, `phillipgreenii-nix-personal`, `phillipgreenii-nix-agent-support`, `phillipgreenii-nix-support-apps`)

## Context

The workspace has six sibling Nix flakes. Each one carries an `update-locks.sh` script that refreshes flake inputs and any non-flake dependencies that have to be tracked by hand (npm hashes, Go vendor hashes, Cargo hashes, tmux plugin pins, bat syntax pins, etc.). They share a bash library at `phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash` that provides `ul_setup`, `ul_run_step`, and `ul_finalize`.

The orchestrator `pn-workspace-update` walks every project in dependency order and, per project, does: `git pull --rebase --autostash` → `./update-locks.sh` → `git push`. `pn-workspace-upgrade` chains `pn-workspace-update && pn-workspace-apply`.

Two problems surfaced:

1. **Inter-project failure stops everything.** `pn-workspace-update` uses `wait "$_child_pid" || exit $?` after each per-project step, so a single repo whose `update-locks.sh` fails halts the run before the remaining repos get a chance. The user's machine recently lost global `node`/`npm`, which broke `phillipgreenii-nix-support-apps/update-locks.sh` (its `jsonl-log-parser/update-deps.sh` calls `npm update`). The other five repos never got their chance.
2. **Updates depend on the host's PATH.** Each `update-locks.sh` runs whatever tools it needs (npm, uv, go, cargo, nix-update) using whatever happens to be installed system-wide. There's no enforcement that those tools exist, and no easy recovery when they don't.

A subtler structural issue rides along: `update-locks.sh` is both a consumer of and the way you fix the very flake it lives in. If the flake is broken, it has to still be runnable enough to repair itself.

## Decision

Make `update-locks` resilient at two layers and reduce its dependence on host-installed tooling, without losing the ability to recover from a fully-broken flake.

### Changes

1. **Convert all `update-locks.sh` to standalone bash scripts.** Remove the `packages.update-locks = pkgs.writeShellApplication { ... }` blocks from `phillipgreenii-nix-personal/flake.nix` and `phillipgreenii-nix-agent-support/flake.nix`. Their `update-locks.sh` files become standalone scripts that source `phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash`, matching the pattern already used by `overlay`, `ziprecruiter`, `support-apps`, and `repo-base`. No stubs kept — `update-locks.sh` is only invoked by the user via the `pn-workspace-*` commands.

2. **Re-exec each `update-locks.sh` inside its flake's `devShells.default`.** Add `ul_reexec_in_dev_shell` to the shared lib. Each `update-locks.sh` calls it after sourcing the lib and before `ul_setup`. The helper:
   - Skips the re-exec when `IN_NIX_SHELL` is already set (respects the user's existing shell and prevents recursion after we `exec`).
   - Probes the dev shell with `nix develop "$script_dir" --command true`. On failure, prints a warning and returns 0 so the script proceeds with whatever tools the host has — this is the manual escape hatch when the flake is broken.
   - On success, `exec nix develop "$script_dir" --command bash "$script" "$@"`. The re-entered invocation sees `IN_NIX_SHELL=impure` and skips the guard.

3. **Add the per-flake update-locks tools to `devShells.default` via `extraInputs`.**
   - `phillipgreenii-nix-support-apps`: `pkgs.nodejs`, `pkgs.prefetch-npm-deps`, `pkgs.uv`.
   - `phillipgreenii-nix-agent-support`: `pkgs.go` for the Go vendor-hash steps. `nix-update` is fetched ad-hoc via `nix run nixpkgs#nix-update` and needs only nix, which the dev shell already provides.
   - `phillipgreenii-nix-overlay`: `pkgs.jq`, `pkgs.curl`, `pkgs.gnused` for the tmux-plugin/bat-syntax updaters. `mkDevShell`'s default buildInputs (nixfmt-rfc-style, statix, deadnix, shellcheck) do not include them.
   - Other flakes: no additions; they use only `nix flake update` or `nix run nixpkgs#...` invocations.

4. **Rewrite `pn-workspace-update.sh` to aggregate per-project failures.** Each project's pull/update/push runs in a small `run_step` helper that returns non-zero instead of exiting. The loop tracks `pull_failed` and `project_failed` flags. Rules:
   - Pull failure on a project: skip `update-locks.sh` _and_ skip `git push` for that project (working tree is suspect, and the local would diverge badly from the remote on push).
   - `update-locks.sh` failure (which already means "at least one step failed; the rest committed cleanly"): still attempt `git push` to publish the successful step commits. This matches the existing per-step atomic-commit design in `ul_run_step` and respects the user's explicit choice to push partial success.
   - Always regenerate `pn-workspace.lock` at the end via `pn-discover-workspace`, even on partial failure — captures whatever did update.
   - Print a `=== Failed projects (N) ===` block when any project failed, and exit 1.

5. **No change to `pn-workspace-upgrade.sh`.** Its existing `pn-workspace-update && pn-workspace-apply` already short-circuits the apply step when update reports any failure, which is the desired behavior.

## Architecture

```
pn-workspace-upgrade
    └─ pn-workspace-update           (orchestrator, aggregates per-project failures)
         └─ for each project:
              ├─ git pull --rebase --autostash      (per-project run_step)
              ├─ ./update-locks.sh                  (per-project run_step)
              │     └─ ul_reexec_in_dev_shell       (re-execs into nix develop or warns + continues)
              │          └─ ul_setup
              │          └─ ul_run_step × N         (per-step failure isolation already exists)
              │          └─ ul_finalize             (exits 1 on partial failure)
              └─ git push                            (per-project run_step; skipped on pull failure)
    └─ pn-workspace-apply             (only runs if pn-workspace-update exited 0)
```

Three failure-isolation layers, each with its own boundary:

| Layer                                | Lives in                                        | Mechanism                               | Already exists?               |
| ------------------------------------ | ----------------------------------------------- | --------------------------------------- | ----------------------------- |
| Step inside `update-locks.sh`        | `ul_run_step`                                   | Record + reset, never exits early       | Yes                           |
| `update-locks.sh` invocation         | `ul_finalize`                                   | Aggregates step failures, exit 1 if any | Yes                           |
| Project inside `pn-workspace-update` | new `run_step` helper + `failed_projects` array | Record + continue, exit 1 if any        | **No — added by this change** |

## Components

### `phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash`

Add one new function. Existing functions unchanged.

```bash
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

### Each `update-locks.sh` (six files)

Shape becomes:

```bash
#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="${SCRIPT_DIR}/.."   # repo-base also uses its own SCRIPT_DIR

# arg parse — same as today

# shellcheck disable=SC1091
source "${WORKSPACE_ROOT}/phillipg-nix-repo-base/lib/scripts/update-locks-lib.bash"
ul_reexec_in_dev_shell "$@"
ul_setup "<project-name>" "${SCRIPT_DIR}"

# ul_run_step calls — unchanged from today's content
# (For personal and agent-support, content is moved verbatim out of the
#  writeShellApplication.text blocks.)

ul_finalize
```

`phillipg-nix-repo-base/update-locks.sh` sources the lib via its own `SCRIPT_DIR` (no `..` walk).

### `devShells.default` per flake

Each flake's `mkDevShell { ... }` call gets `extraInputs` for its update-locks tooling. `mkDevShell` itself in `phillipg-nix-repo-base/nix/dev-env.nix` is **not** modified — it remains generic.

| Flake                              | Adds to `extraInputs`                                                                          |
| ---------------------------------- | ---------------------------------------------------------------------------------------------- |
| `phillipg-nix-repo-base`           | (none)                                                                                         |
| `phillipgreenii-nix-overlay`       | `pkgs.jq`, `pkgs.curl`, `pkgs.gnused` (`mkDevShell`'s default buildInputs do not include them) |
| `phillipg-nix-ziprecruiter`        | (none — `brew` lives outside nix on purpose)                                                   |
| `phillipgreenii-nix-personal`      | (none)                                                                                         |
| `phillipgreenii-nix-agent-support` | `pkgs.go`                                                                                      |
| `phillipgreenii-nix-support-apps`  | `pkgs.nodejs`, `pkgs.prefetch-npm-deps`, `pkgs.uv`                                             |

### `phillipg-nix-repo-base/modules/pn/pn-workspace-update/pn-workspace-update.sh`

Replace the main `while` loop with a per-project failure-aggregating loop. Key shape:

```bash
failed_projects=()

run_step() {
  local label="$1"; shift
  "$@" &
  _child_pid=$!
  if ! wait "$_child_pid"; then
    _child_pid=""
    echo "  ✗ ${label} failed for ${_current_project}" >&2
    return 1
  fi
  _child_pid=""
}

while IFS= read -r project_path; do
  project_name=$(basename "$project_path")
  _current_project="$project_name"
  echo "  --== Update $project_name ==--  "
  cd "$project_path" || { failed_projects+=("$project_name (cd failed)"); continue; }

  pull_failed=false
  project_failed=false

  if workspace_has_upstream; then
    run_step "git pull" git pull --rebase --autostash || { pull_failed=true; project_failed=true; }
  fi

  if ! $pull_failed; then
    run_step "update-locks" ./update-locks.sh || project_failed=true
  fi

  if workspace_has_upstream && ! $pull_failed; then
    run_step "git push" git push || project_failed=true
  elif ! workspace_has_upstream; then
    _branch=$(git branch --show-current)
    echo "no upstream for branch '${_branch:-DETACHED HEAD}' — skipping push for $project_name"
  fi

  $project_failed && failed_projects+=("$project_name")
  echo
done < <(echo "$workspace_json" | jq -r '.[] | .path')
_current_project=""

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

The signal-handling `_cleanup` trap and `_child_pid` tracking remain — `run_step` updates `_child_pid` so SIGINT/SIGTERM still kill the active child.

### Tests

Add bats tests to `phillipg-nix-repo-base/lib/scripts/tests/`:

- `ul_reexec_in_dev_shell` returns 0 immediately when `IN_NIX_SHELL` is set.
- `ul_reexec_in_dev_shell` warns and returns 0 when `nix develop` cannot load (point at a fixture dir with a broken `flake.nix`).
- `ul_reexec_in_dev_shell` re-execs into the dev shell when it loads cleanly (fixture flake exports a `devShells.default` whose `buildInputs` includes a sentinel binary; verify it's on PATH inside the re-exec).

Add bats tests to `phillipg-nix-repo-base/modules/pn/pn-workspace-update/tests/`:

- `pn-workspace-update` continues past a failing project and exits 1 with the failed project listed in the summary.
- `pn-workspace-update` regenerates `pn-workspace.lock` even when one project failed.
- `pn-workspace-update` skips `git push` for a project whose pull failed but still attempts push for a project whose `update-locks.sh` had a partial failure.

Add a bats test (or update existing) to `phillipg-nix-repo-base/modules/pn/pn-workspace-upgrade/tests/`:

- `pn-workspace-upgrade` does not invoke `pn-workspace-apply` when `pn-workspace-update` exits non-zero.

## Consequences

### Positive

- A missing host tool (npm, node, uv, go) no longer breaks any `update-locks.sh` as long as the flake evaluates — the dev shell provides it.
- One failing repo no longer prevents the other five from updating.
- The dev-shell wrap is opt-in by graceful degradation: if the flake can't `nix develop`, the script still runs with host tooling, so a broken flake remains repairable.
- Step-level atomic commits + push-on-partial-success means successful work isn't lost when something later in the same script fails.
- Uniform shape across all six `update-locks.sh` files (no more two-style split between standalone and nix-wrapped) reduces ongoing cognitive load.

### Negative

- Per-repo latency: every `update-locks.sh` now does a `nix develop --command true` probe and an `exec nix develop --command bash ...` re-entry. Warm-cache cost is ~1–2s per repo (~6–12s for the full workspace run).
- Push-on-partial-update means consumers may temporarily see a half-updated state (e.g., `flake.lock` bumped without the matching `package-lock.json.hash`). Accepted because consumers run their own `update-locks` and ride over the inconsistency.
- The `nix run .#update-locks` invocation no longer exists in `phillipgreenii-nix-personal` and `phillipgreenii-nix-agent-support`. Acceptable per the user's confirmation that `update-locks.sh` is only invoked via `pn-workspace-*` commands.
- Output is noisier on a bad day: per-step failure messages from `ul_run_step` _and_ per-project failure messages from `pn-workspace-update`. Mitigated by aggregating both into clear summary blocks.

### Neutral

- `mkDevShell` in `phillipg-nix-repo-base/nix/dev-env.nix` stays generic. Tooling additions are scoped to each consumer flake's `extraInputs`, so interactive `nix develop` for repo-base/personal/etc. doesn't gain heavy tools they don't otherwise need.
- The signal-handling trap in `pn-workspace-update` keeps its existing shape — only the per-step `wait || exit` calls become per-step `run_step` calls with explicit failure tracking.

## Alternatives Considered

### Replace `update-locks.sh` with `nix run .#update-locks`

Considered and rejected. `nix run` requires the flake to fully evaluate. If a flake is broken (which is the common case `update-locks.sh` is being used to fix), `nix run` fails before any recovery can happen. The dev-shell wrap is more forgiving: the script falls back to host tooling when `nix develop` can't load.

### Wrap the dev-shell entry inside `ul_setup` rather than as a separate call

Considered. Rejected because `ul_setup` does meaningful work (signal traps, fsmonitor handling, pre-commit hook installation) that should happen _after_ the re-exec — otherwise the original-shell instance pays the cost and then immediately exec's away. A separate `ul_reexec_in_dev_shell` called before `ul_setup` keeps the cost on the surviving process only.

### Add a custom marker env var (`UL_DEV_SHELL_LOADED=1`) for the re-entry guard

Considered. Rejected after verifying that `nix develop` on this system already sets `IN_NIX_SHELL=impure`. Using the existing env var also makes the guard respect any user-initiated `nix develop` or `nix-shell` session (don't redundantly re-enter).

### Skip the push entirely on any `update-locks.sh` failure

Considered. Rejected per the user's explicit choice: each step in `ul_run_step` already commits atomically and the user wants the successful work to land remotely so consumers can benefit. The downside (consumers seeing a half-updated state) is acceptable because they run their own update-locks.

## Related Decisions

- `phillipgreenii-nix-personal/docs/adr/0049` (launchd stable path) — unrelated, but the same general principle applies here: scripts that bootstrap themselves should not depend on the very thing they're bootstrapping (system tools, current-system profile, etc.).
