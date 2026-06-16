# Working with worktrees

> **Two models exist.** The newer **coordinated-set model** (`pn workspace worktree`) creates a full workspace of worktrees — every repo on a shared feature branch — and is the recommended approach for cross-repo feature work. The older **single-override model** (`--override-path` / `PN_WORKSPACE_OVERRIDE_PATHS`) patches one or more repo paths into an existing workspace run; it is still valid for single-repo overrides. Both are documented here.

## Coordinated worktree sets (recommended for cross-repo work)

`pn workspace worktree add <branch>` creates a **coordinated set**: a directory that is itself a complete workspace root, containing a git worktree for every repo in `pn-workspace.toml`, all on the shared `<branch>`. The normal `pn workspace` verbs (`build`, `update`, `rebase`, `push`, `status`, etc.) run unchanged inside the set because the set directory satisfies the existing path model — `pn` finds the set's own `pn-workspace.toml` via the upward walk and resolves all repo paths as `{set}/{repo}`.

### Quick start

```bash
# From the canonical workspace root:
pn workspace worktree add my-feature          # create set at .worktrees/my-feature
cd .worktrees/my-feature
unset PN_WORKSPACE_ROOT                       # must not point at the canonical root
pn workspace status                           # operates on the set's worktrees
pn workspace build
pn workspace rebase main                      # rebase each worktree onto local main
pn workspace push --set-upstream              # publish the feature branch (first time)
```

### Worktree set commands

| Command                                              | What it does                                                                                                                                          |
| ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pn workspace worktree add <branch> [<commit-ish>]`  | Create a set for `<branch>` under `worktrees_dir` (default `.worktrees`). Pre-flights before creating anything; mirrors `git worktree add` semantics. |
| `pn workspace worktree list`                         | List existing sets.                                                                                                                                   |
| `pn workspace worktree remove <branch>` (alias `rm`) | Remove a set; mirrors `git worktree remove` (refuses dirty/locked unless `--force`). Does NOT delete the branch.                                      |
| `pn workspace worktree prune`                        | Run `git worktree prune` in every canonical repo (clears stale admin entries after a manual `rm -rf` of a set).                                       |

The `worktrees_dir` field in `pn-workspace.toml`'s `[workspace]` table configures where sets live (default: `.worktrees`, relative to workspace root). When it uses a non-dot-prefixed value, the filesystem scanners skip it automatically.

### Key caveats

- **`PN_WORKSPACE_ROOT` must be unset (or point at the set).** If it is exported pointing at the canonical root, `pn` resolves the primary workspace instead of the set. Unset it before running any verb inside a set.
- **P1 guarantee:** no `pn workspace` verb run from inside a set modifies the canonical checkouts' working state (HEAD, branch, index, working tree). New commits/branches/objects land in the shared object store. `update`/`rebase` may update shared remote-tracking refs and `FETCH_HEAD` but never the primary's working tree or checked-out branch.
- **Hooks must use workspace-relative paths.** Hooks that hard-code absolute canonical paths escape the set and violate P1. Use `{root}/{repo}` construction (where `root` is resolved from cwd or `PN_WORKSPACE_ROOT`) rather than hard-coded absolute paths.

For the full agent-rules treatment of coordinated worktrees — including the `rebase [branch]` and `push --set-upstream` forms, the `WORKSPACE_ROOT` recompute-from-`SCRIPT_DIR` note, and the detailed P1 explanation — see [`pn-workspace-rules/CLAUDE.md`](../pn-workspace-rules/CLAUDE.md), section **Coordinated Worktree Sets**.

---

## Single-repo override model (`--override-path`)

Agents commonly create git worktrees of workspace projects, either outside the workspace directory or under `<repo>/.git/worktrees/<name>`. To make `pn-workspace-*` commands operate on a worktree path instead of the original sibling directory, use `--override-path` (or the `PN_WORKSPACE_OVERRIDE_PATHS` env var).

The override key is the workspace **directory name** (the basename of the project's path in `pn-workspace.lock`). It works for terminal and non-terminal projects identically.

## Single override

CLI:

```bash
pn-workspace-build --override-path phillipg-nix-repo-base=$HOME/worktrees/repo-base-feature-foo
```

Env:

```bash
export PN_WORKSPACE_OVERRIDE_PATHS=phillipg-nix-repo-base=$HOME/worktrees/repo-base-feature-foo
pn-workspace-build
```

## Multiple overrides

CLI: repeat the flag.

```bash
pn-workspace-build \
  --override-path my-other-repo=$HOME/worktrees/my-other-repo-feature-foo \
  --override-path phillipgreenii-nix-personal=$HOME/worktrees/nix-personal-feature-foo
```

Env: comma-separated.

```bash
export PN_WORKSPACE_OVERRIDE_PATHS=my-other-repo=/path/p,phillipgreenii-nix-personal=/path/c
```

Flags win over env per key. Validation rejects unknown project names, missing directories, and paths without `flake.nix`.

## Workspace root

When running from inside a worktree, `pn-workspace-*` cannot walk up to find the workspace root. Pass `--root` (preferred) or set `PN_WORKSPACE_ROOT`:

```bash
pn-workspace-build --root ~/workspace --override-path repo-base=$PWD
```

```bash
export PN_WORKSPACE_ROOT=~/workspace
export PN_WORKSPACE_OVERRIDE_PATHS=repo-base=$PWD
pn-workspace-build
```

The deprecated `--workspace` flag continues to work as an alias for `--root` and emits a deprecation notice on stderr.

## Caveats

- Status, update, push, and rebase operate on the swapped path. `git push` pushes the worktree's branch, `git mu` (rebase) rebases it, etc.
- Overrides are transient. They never modify `pn-workspace.lock`.
- Stale env entries pointing at deleted worktrees are caught at validation before nix runs.
