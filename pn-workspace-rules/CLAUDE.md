# pn-workspace Conventions for Agents

Rules for AI agents working inside a `pn-workspace.toml` workspace. These apply to ANY repo whose flake is declared as a project in the workspace.

## Cardinal Rule

**Never modify `flake.nix` to point input URLs at local paths.** `pn workspace build/apply/flake-check` inject `--override-input <alias> git+file://<local-path>` at build/eval time — the lock file and flake.nix stay clean. Local-path URLs in `flake.nix` break every other consumer (CI, teammates, future you on another machine).

## Completion Gate

After completing any task in a project that participates in a `pn-workspace.toml`, you MUST run `pn workspace build` from the workspace root before declaring the task complete. Cross-project changes (a new flake output consumed by another workspace repo, for instance) only show up here.

```text
pn workspace build
```

Per-project `nix flake check` is necessary but not sufficient. Workspace-level build catches consumer-side breakage.

If `pn workspace build` fails, the task is not complete. Fix the failure (in this or the consuming project) and re-run.

## Workspace Lifecycle: Three Commands

A pn workspace is bootstrapped and maintained with three separate commands:

### 1. `pn workspace init`

Scans the workspace root for existing git repos and reconciles them into `pn-workspace.toml`. Config-only: does NOT clone repos and does NOT write a lock file. Run this when repos already exist on disk but are not yet in the TOML config.

### 2. `pn workspace clone`

Clones repos listed in `pn-workspace.toml` that are missing on disk. Idempotent: repos already cloned are skipped. Run after writing or editing `pn-workspace.toml` to get repos onto disk.

### 3. `pn workspace lock`

Evaluates each cloned repo's `flake.nix` inputs, discovers workspace dependency edges (by matching input URLs to workspace repo remote URLs), resolves the terminal repo, and writes `pn-workspace.lock.json` atomically. Exits non-zero and preserves the existing lock file if validation errors occur (e.g. `missing_terminal`, `terminal_not_sink`).

## Config and Lock Files

- **`pn-workspace.toml`**: Declares repos, workspace settings (terminal, apply command, etc.), and hooks. The `workspace.terminal` key names the repo whose flake is the build/apply target.
- **`pn-workspace.lock.json`**: Records dependency edges (consumer → alias → target triples), topological order, per-repo flake paths, and the terminal repo. Written by `pn workspace lock`.

The `input-name` field on `[repos.*]` sections has been removed. Alias names are now derived automatically from each consumer's declared flake input aliases at lock time. If you see an error about `input-name`, remove that field from `pn-workspace.toml`.

## --terminal Flag

Every `pn workspace` subcommand accepts `--terminal <name>` to override `workspace.terminal` for that invocation. This is a persistent flag on the `workspace` group.

- **Required-terminal commands** (build, apply, tree, update): Error with a standard message if no terminal is configured and no flag is given.
- **Optional-terminal commands** (init, clone, lock, rebase, push, status, flake-check, pre-commit-check): Warn once and continue if no terminal is configured.

## In-Memory Lock Fallback

Commands that need a topological order (rebase, push, status, flake-check, pre-commit-check, update) derive the order in memory if no lock file exists on disk or the disk lock is stale (does not cover the current repo set). This uses `deriveLock` to evaluate flake inputs and build edges without writing anything to disk. The result is the same as with a current lock file, just slower (requires nix eval per repo). To persist the derived order, run `pn workspace lock`.

## Builds and Validation

| Goal                                           | Use                                            | Don't use                                                                |
| ---------------------------------------------- | ---------------------------------------------- | ------------------------------------------------------------------------ |
| Build the system (current host)                | `pn workspace build`                           | `darwin-rebuild build`, `nix build .#darwinConfigurations.<host>.system` |
| Activate the system                            | the **user** runs `pn workspace apply`         | NEVER invoke from agent context                                          |
| Run `nix flake check` on a project             | `nix flake check` inside a project dir         | (no special wrapper needed)                                              |
| Run `nix flake check` across every project     | `pn workspace flake-check`                     | per-repo `nix flake check`                                               |
| Build a single package                         | `nix build .#<pkg>` inside the project         | (no workspace-level wrapper)                                             |
| Pre-commit checks across all repos             | `pn workspace pre-commit-check`                | per-repo `pre-commit run --all-files`                                    |
| Update flake locks across all repos            | `pn workspace update`                          | per-repo `nix flake update`                                              |

## When to Push

You don't need to push branches for builds to work. `pn workspace build/flake-check` operate on the local working tree. Push only when:

- The user explicitly asks.
- The work is ready for review/merge.

A failing remote build is **not** a reason to push agent-only branches.

## Command Surface Cheat-Sheet

Workspace-level (operate on every repo in the workspace):

```text
pn workspace init                 Scan for git repos; reconcile into pn-workspace.toml
pn workspace clone                Clone repos from pn-workspace.toml missing on disk
pn workspace lock                 Derive and write pn-workspace.lock.json
pn workspace build                Build the current host's system config
pn workspace apply                Activate (USER ONLY)
pn workspace pre-commit-check     Run pre-commit checks across all repos
pn workspace flake-check          Run `nix flake check` across all repos
pn workspace update               Refresh flake locks across all repos (terminal required)
pn workspace upgrade              Update + apply (USER ONLY for the apply step)
pn workspace rebase               Rebase each repo on its remote
pn workspace push                 Push each repo (USER-INITIATED ONLY)
pn workspace status               Per-repo working-tree summary
pn workspace tree                 Print the workspace dependency DAG (terminal required)
pn workspace nix -- <nix args>    Run nix with --override-input flags injected
pn workspace discover             List workspace repos and their paths
```

All subcommands accept `--terminal <name>` to override `workspace.terminal`.
