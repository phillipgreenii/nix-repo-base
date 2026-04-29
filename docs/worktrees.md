# Working with worktrees

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
  --override-path phillipg-nix-ziprecruiter=$HOME/phillipg_mbp/phillipg-nix-ziprecruiter/.git/worktrees/p \
  --override-path phillipgreenii-nix-personal=$HOME/phillipg_mbp/phillipgreenii-nix-personal/.git/worktrees/c
```

Env: comma-separated.

```bash
export PN_WORKSPACE_OVERRIDE_PATHS=phillipg-nix-ziprecruiter=/path/p,phillipgreenii-nix-personal=/path/c
```

Flags win over env per key. Validation rejects unknown project names, missing directories, and paths without `flake.nix`.

## Workspace root

When running from inside a worktree, `pn-workspace-*` cannot walk up to find the workspace root. Pass `--root` (preferred) or set `PN_WORKSPACE_ROOT`:

```bash
pn-workspace-build --root ~/phillipg_mbp --override-path repo-base=$PWD
```

```bash
export PN_WORKSPACE_ROOT=~/phillipg_mbp
export PN_WORKSPACE_OVERRIDE_PATHS=repo-base=$PWD
pn-workspace-build
```

The deprecated `--workspace` flag continues to work as an alias for `--root` and emits a deprecation notice on stderr.

## Caveats

- Status, update, push, and rebase operate on the swapped path. `git push` pushes the worktree's branch, `git mu` (rebase) rebases it, etc.
- Overrides are transient. They never modify `pn-workspace.lock`.
- Stale env entries pointing at deleted worktrees are caught at validation before nix runs.
