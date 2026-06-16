# pn-workspace Agent Conventions — Design

**Status:** Draft — pending implementation plan
**Date:** 2026-05-18
**Repos affected:**

- `phillipg-nix-repo-base` (tooling: new and renamed `pn-workspace-*` / `pn-ws-*` scripts)
- `phillipgreenii-nix-agent-support` (rule plugin shipped to Claude Code agents)

## Problem

Agents working in a `pn-workspace.toml`-managed multi-repo workspace repeatedly:

- Override flake input URLs to `path:` references to make WIP cross-repo work build, leaving the consumer flake broken for everyone else.
- Push WIP branches to remotes solely so a flake input URL can resolve them.
- Validate work with bare `nix flake check` / `darwin-rebuild build` against stale `flake.lock` content rather than the workspace's local working trees.
- Skip the workspace-wide build, missing breakage in cross-project consumers.

The workspace's existing `pn-workspace-*` tooling already injects `--override-input` at build time, but agents don't know that and reach for bare `nix` instead. Documentation lives nowhere agents reliably read.

## Goals

1. Ship a Claude Code rule plugin (auto-enabled in our environments) that codifies the conventions.
2. Add a generic project-level wrapper `pn-ws-nix` so any `nix <subcommand>` invocation routes through workspace overrides — replacing the temptation to edit `flake.nix`.
3. Add `pn-workspace-flake-check` for cross-repo flake-check coverage that mirrors the existing `pn-workspace-check` pre-commit runner.
4. Rename the existing `pn-workspace-check` to `pn-workspace-pre-commit-check` so the command family is symmetrically named (`<subject>-check`).

## Non-goals

- Migrating away from the `pn-workspace-*` family or rethinking workspace.toml schema.
- Adding wrappers for interactive/one-off `nix` subcommands (`develop`, `shell`, `repl`, `run`) — agents rarely use these; bare `nix` is fine.
- Adding wrappers for non-flake `nix` subcommands (`store *`, `profile list`, `log`, `key *`, `nar *`, `daemon`, `doctor`). `--override-input` doesn't apply; bare `nix` is correct.
- Force-deprecation tooling for `pn-workspace-check` consumers outside this monorepo. Hard rename; loud break is the chosen UX.

## Architecture

```text
+--------------------------------------------+
|  phillipg-nix-repo-base                    |
|  modules/pn/                               |
|    pn-workspace-build/        (existing)   |
|    pn-workspace-apply/        (existing)   |
|    pn-workspace-upgrade/      (existing)   |
|    pn-workspace-update/       (existing)   |
|    pn-workspace-rebase/       (existing)   |
|    pn-workspace-push/         (existing)   |
|    pn-workspace-status/       (existing)   |
|    pn-workspace-pre-commit-check/ (RENAMED from -check)
|    pn-workspace-flake-check/  (NEW)        |
|    pn-ws-nix/                 (NEW)        |
+--------------------------------------------+
                |
                v
+--------------------------------------------+
|  phillipgreenii-nix-agent-support          |
|  home/programs/pn-workspace-rules/ (NEW)   |
|    default.nix      ── HM module           |
|    pn-workspace-rules.md ── shipped CLAUDE.md
+--------------------------------------------+
                |
                v
   ~/.local/share/pgii-local-plugins/pn-workspace-rules/
       .claude-plugin/plugin.json
       CLAUDE.md
```

The plugin is materialized via the same Home Manager pattern the existing `agent-rules` plugin uses (see `agent-support/home/programs/agent-rules/default.nix`). Single marketplace `pgii-local-plugins`, per ADR-0003.

## Naming convention

Two distinct prefixes in `pn-*`:

- `pn-workspace-*` — operates at the workspace level (touches every project in the workspace, or operates on workspace state). Examples: `pn-workspace-build` (build current host's system, requires all workspace projects to evaluate), `pn-workspace-flake-check` (run flake check across every project).
- `pn-ws-*` — workspace-aware but project-scoped. Operates on a single flake inside the workspace, but reads workspace state to inject overrides. Currently only `pn-ws-nix`.

Future project-scoped commands that need workspace context should adopt the `pn-ws-*` prefix.

## Components

### `pn-ws-nix` (new)

Generic wrapper around the `nix` CLI. Injects `--override-input <project> <local-path>` for every project declared in the nearest `pn-workspace.toml`, then `exec nix "$@"`.

**Usage:**

```text
pn-ws-nix [--non-override-subcommand-action {error|warn|ignore}] <nix-args...>
```

**Behavior:**

1. Resolve workspace root: walk up from CWD for `pn-workspace.toml`, or honor `PN_WORKSPACE_ROOT`.
2. Parse `pn-workspace.toml` for project entries; compute `--override-input <name> <abs-path>` flags.
3. Identify the `nix` subcommand. Two are flagged as "non-override-applicable":
   - `nix flake update`
   - `nix flake lock`
     Both accept `--override-input` as a global flag but silently ignore it for lock operations — failing silently. The wrapper detects these and applies the configured action.
4. Resolve the action (highest priority first):
   1. `--non-override-subcommand-action` flag value
   2. `$PN_WS_NIX_NON_OVERRIDE_SUBCOMMAND_ACTION` env var value
   3. Default: `warn`
5. Apply action:
   - `error` → print a message to stderr stating that overrides do not apply to this subcommand and pointing at bare `nix`, then exit 2.
   - `warn` → print the same message to stderr, then exec `nix` with the user's args, no overrides.
   - `ignore` → exec `nix` with the user's args, no overrides, silently.
6. For all other subcommands: `exec nix <args> --override-input <pairs...>`. If `nix` itself errors on an unrecognized flag for a non-flake subcommand, that error surfaces directly — clear feedback to the agent.

**Implementation:** Bash script at `modules/pn/pn-ws-nix/pn-ws-nix.sh`, built via `mkBashScript`. Reuses the same workspace-resolution helper used by existing `pn-workspace-*` scripts.

### `pn-workspace-flake-check` (new)

Iterates each project declared in `pn-workspace.toml`, `cd`s into the project, and runs `pn-ws-nix flake check`. Aggregates exit codes; returns non-zero if any project failed.

**Usage:** identical option surface to the existing `pn-workspace-check` (root resolution, `--root`, `--override-path`).

**Implementation:** Bash script at `modules/pn/pn-workspace-flake-check/pn-workspace-flake-check.sh`. Loops projects, invokes `pn-ws-nix flake check` per-project, captures exit code per project, prints a summary at the end.

### `pn-workspace-pre-commit-check` (renamed from `pn-workspace-check`)

Functionality unchanged from the current `pn-workspace-check`: runs `pre-commit run --all-files` (or `prek`) inside each workspace project. Only the source path, command name, registration name, and references change.

### `pn-workspace-rules` plugin (new)

Home Manager module at `phillipgreenii-nix-agent-support/home/programs/pn-workspace-rules/default.nix`. Mirrors the `agent-rules` pattern:

- Writes `~/.local/share/pgii-local-plugins/pn-workspace-rules/.claude-plugin/plugin.json` (name, version, description).
- Writes `~/.local/share/pgii-local-plugins/pn-workspace-rules/CLAUDE.md` from a shipped `pn-workspace-rules.md`.
- Registers the plugin under `phillipgreenii.programs.claude.plugins.local.plugins.pn-workspace-rules` with `enabledByDefault = true`.

Activation requires no consumer change: any machine that imports `agent-support/home/default.nix` picks up the plugin.

## Rule content (`pn-workspace-rules.md`)

> **Source of truth:** the rendered agent-rules file is
> `pn-workspace-rules/CLAUDE.md` in `phillipg-nix-repo-base`. The snapshot
> below reflects the original design from 2026-05-18. For the current content
> — including the **Coordinated Worktree Sets** section added in pg2-4kto
> (cd-into-set workflow, `PN_WORKSPACE_ROOT` caveat, `WORKSPACE_ROOT`
> recompute-from-`SCRIPT_DIR` note, absolute-path-in-hooks caveat, and the P1
> guarantee) — read `pn-workspace-rules/CLAUDE.md` directly. The design doc
> section below is kept for historical context; it is not maintained in sync.

````markdown
# pn-workspace Conventions for Agents

Rules for AI agents working inside a `pn-workspace.toml` workspace. These apply to ANY repo whose flake is declared as a project in the workspace.

## Cardinal Rule

**Never modify `flake.nix` to point input URLs at local paths.** `pn-workspace-*` and `pn-ws-nix` inject `--override-input <name> <local-path>` at build/eval time — the lock file and flake.nix stay clean. Local-path URLs in `flake.nix` break every other consumer (CI, teammates, future you on another machine).

## Completion Gate

After completing any task in a project that participates in a `pn-workspace.toml`, you MUST run `pn-workspace-build` from the workspace root (or anywhere with `PN_WORKSPACE_ROOT` set) before declaring the task complete. Cross-project changes (a new flake output consumed by another workspace repo, for instance) only show up here.

```
pn-workspace-build
```

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

```
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

```
pn-ws-nix <subcommand>          Generic wrapper around `nix`; injects overrides
```
````

## Test strategy

- `pn-ws-nix`: bats tests in `modules/pn/pn-ws-nix/tests/test-pn-ws-nix.bats`.
  - Workspace root resolution (CWD walk, env var override, --root flag).
  - Override flag generation matches `pn-workspace.toml` projects.
  - Deny-list subcommands trigger configured action.
  - Flag overrides env var; env var overrides default.
  - Invalid action value exits with usage.
  - Non-flake subcommands pass through cleanly.
- `pn-workspace-flake-check`: bats tests in `modules/pn/pn-workspace-flake-check/tests/...`.
  - Iterates all projects in `pn-workspace.toml`.
  - Per-project exit code aggregation: full sweep (all projects run regardless of individual failures), summary line at the end, overall exit = non-zero if any failed.
  - Honors `--root` and `--override-path`.
- `pn-workspace-pre-commit-check`: existing bats tests renamed + updated to match new command name; behavior unchanged.
- `pn-workspace-rules` plugin: `nix flake check` on agent-support; existing plugin pattern validates that `plugin.json` and `CLAUDE.md` materialize at expected paths and that `enabledByDefault = true` propagates.

## Migration

Hard rename of `pn-workspace-check` → `pn-workspace-pre-commit-check`:

- Rename source dir + script + test file in `phillipg-nix-repo-base/modules/pn/`.
- Update `modules/pn/scripts.nix` registry.
- Grep all consuming repos under `~/phillipg_mbp/` for `pn-workspace-check` mentions (CLAUDE.md, README, ADR, other shell scripts) and rewrite.
- No deprecation shim; loud failure is the chosen feedback path.

## Open items

None. Spec is implementation-ready.
