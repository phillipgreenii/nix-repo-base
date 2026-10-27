# Git hooks: the per-clone hook bundle and `pg-hooks`

This page is the canonical reference for how git hooks work in the repos that consume
`flakeModules.pre-commit`, and for the `pg-hooks` command. It documents behavior that exists in
`modules/pg-hooks/` and `flake-modules/pre-commit.nix`. The design and its rulings live in
`docs/superpowers/specs/2026-10-01-per-clone-hook-bundle-design.md`; the decision to make the bundle
the only mechanism is [ADR 0032](adr/0032-per-clone-hook-bundle-replaces-generated-config-and-shim.md).

Contents: [Overview](#overview), [Stages](#stages), [The `pg-hooks` command](#the-pg-hooks-command),
[The eval-only pre-land check](#the-eval-only-pre-land-check-pre-land-eval),
[Messages](#messages), [Exit codes](#exit-codes), [Bundle layout](#bundle-layout),
[Staleness](#staleness), [Old config symlinks](#old-config-symlinks), [Configuration](#configuration),
[Grafana alert uid hook](#grafana-alert-uid-hook), [Drift guard](#drift-guard),
[Operator runbook](#operator-runbook), [Open follow-ups](#open-follow-ups).

## Overview

A hook bundle replaces the old per-worktree `.pre-commit-config.yaml` symlink and the commit-time
`nix run` shim. The repo's flake renders a bundle (prek, the prek config, the fixer list, the stage
list, a runner and a library). `install-pre-commit-hooks` roots that bundle against the clone's git
common directory and writes static stubs into `<git-common-dir>/hooks`. A stub finds the bundle through a
pointer file and runs it, so no nix runs at hook time and nothing is written into a working tree.

Policies that hold everywhere on this page:

- A git hook MUST NOT invoke nix (HK-2). Stubs and the runner need only `sh`, `git` and the bundle.
- Nothing MUST be written into a working tree to make hooks run.
- Agents MUST NOT write `core.hooksPath`, and MUST NOT push.
- A missing bundle at hook time is a notice and exit 0; it never blocks a commit.
- Every `pg-hooks:` message goes to stderr. stdout carries only the requested output of `status`,
  `list` and `explain`.

```mermaid
flowchart LR
  G["git commit, push or rebase in any worktree"] --> H{"local core.hooksPath set?"}
  H -->|"yes: absolute common-dir/hooks"| S
  H -->|"no: fresh clone"| D["global dispatcher, one script per stage name"]
  D -->|"pre-commit only"| I["pg-git-check-identity"]
  D --> S["common-dir/hooks/STAGE stub, marker-owned"]
  S --> R["read pointer: private pg-hooks dir, else shared"]
  R -->|"pointer and bundle valid"| X["bundle bin/pg-hooks-run STAGE"]
  R -->|"missing"| N["one notice line, exit 0"]
  X --> P["bundle bin/prek hook-impl with the bundle config"]
  INST["install-pre-commit-hooks"] -->|"mkdir gen-N, add-root, source.json, rename pointer, prune, stubs"| B["gen-N/bundle in /nix/store"]
  C["pg-hooks status, list, explain, run, fix"] --> R
```

## Stages

`pg-hooks run <stage>` and `pg-hooks explain <stage>` accept exactly the stage names in the first
table (the `stages` vocabulary of `pg-hooks.sh`). The second table lists the workflow entry points
that are not git stages.

| Stage                | Triggered by                                      |
| -------------------- | ------------------------------------------------- |
| `pre-commit`         | `git commit`                                      |
| `pre-merge-commit`   | `git merge`, before the merge commit              |
| `pre-push`           | `git push`                                        |
| `pre-rebase`         | `git rebase`                                      |
| `commit-msg`         | `git commit`, message check                       |
| `prepare-commit-msg` | `git commit`, message preparation                 |
| `post-checkout`      | `git checkout`, `git switch`, `git worktree add`  |
| `post-commit`        | `git commit`, after the commit is made            |
| `post-merge`         | `git merge`, `git pull`                           |
| `post-rewrite`       | `git commit --amend`, `git rebase`, after rewrite |
| `manual`             | nothing automatic; only `pg-hooks run manual`     |
| `pre-land`           | `ff-merge-to-main` FF-1b, before a land           |

`pre-land` is a `pg-hooks` pseudo-stage, not a git hook: it runs the `pre-commit` hooks over the
branch diff. `manual` never gets a stub.

| Entry point      | Configured by                             | Used by                                               | Run directly   |
| ---------------- | ----------------------------------------- | ----------------------------------------------------- | -------------- |
| `pre-commit-fix` | `fixers`                                  | an agent or operator after `git add`, never by a hook | `pg-hooks fix` |
| `post-land`      | not available yet (deferred, `pg2-na2nr`) | nothing                                               | none           |

Which stages actually fire in a clone is decided by the bundle: a stub is written only for a stage
that has at least one configured prek hook (`stages.json`), restricted to real client-side hook
names. Hooks are configured per repo through `phillipgreenii.pre-commit.extraHooks`; each hook's own
`stages` decides where it runs. Nothing else configures hooks (no per-worktree file, no git config
key).

## The `pg-hooks` command

`pg-hooks` is on PATH after `pn workspace apply` (the `homeModules.pg-hooks` home module, built from
`modules/pg-hooks`). Run with no arguments it prints usage and the common-tasks block. `pre-commit-fix`
is a second name for `pg-hooks fix`, delivered as its own tiny wrapper because agents and tool
approvers match commands by exact name.

| Command                                                       | What it does                                                                             |
| ------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `pg-hooks status [--porcelain]`                               | Report the bundle state. The exit code carries the state.                                |
| `pg-hooks list`                                               | List the stages that have configured hooks, plus `pre-land` when `pre-commit` has hooks. |
| `pg-hooks explain <stage>`                                    | Show what triggers a stage, what it runs and which hooks it holds.                       |
| `pg-hooks run <stage> [files...\|--all-files] [-- prek-args]` | Run a stage's hooks now, over the staged files by default.                               |
| `pg-hooks fix`                                                | Apply the repo's fixers to the staged files and restage the files they changed.          |

Common tasks:

```text
before committing:  git add <files>; pg-hooks fix; git commit
before landing:     pg-hooks run pre-land
diagnose:           pg-hooks status
```

### `status`

`status` prints these lines to stdout (`--porcelain` prints `key=value`, the human form prints
`key:` with padding). Callers MUST parse `--porcelain` and MUST NOT parse prose:

```text
state=<present|stale|missing|broken|unreachable|relocated>
bundle=<path of the selected bundle, or empty>
generation=<gen-N, or empty>
stages=<comma list, or empty>
reinstall=<the exact command that rebuilds the bundle>
```

The human form also prints the matching message from the [messages](#messages) table to stderr.
`status` exits non-zero for most states by design, so a caller reads the `state=` line, not the exit
code.

| State         | Exit | Meaning                                                                                 |
| ------------- | ---- | --------------------------------------------------------------------------------------- |
| `present`     | 0    | A valid bundle whose stamp matches.                                                     |
| `broken`      | 12   | The pointer is invalid, or `bin/prek` or `bin/pg-hooks-run` is not executable.          |
| `missing`     | 13   | No bundle. A leftover `.pre-commit-config.yaml` does not count as one.                  |
| `stale`       | 14   | The bundle's stamp, or a recorded override, differs from now.                           |
| `unreachable` | 15   | git does not run hooks from `<common-dir>/hooks` (a `core.hooksPath` bypasses it).      |
| `relocated`   | 16   | The recorded `clone_path` differs from this clone's common dir (moved or copied clone). |

State precedence is `relocated`, then `unreachable`, then `broken`, then `stale`, then `present`.
With no bundle: `unreachable` if hooks are unreachable, else `missing`.

```mermaid
flowchart TD
  A["status"] --> B{"clone_path differs from common dir?"}
  B -->|"yes"| R["relocated, exit 16"]
  B -->|"no"| C{"core.hooksPath bypasses common-dir/hooks?"}
  C -->|"yes"| U["unreachable, exit 15"]
  C -->|"no"| D{"bundle resolved?"}
  D -->|"pointer invalid or prek not runnable"| BR["broken, exit 12"]
  D -->|"valid bundle"| E{"stamp or override differs?"}
  E -->|"yes"| ST["stale, exit 14"]
  E -->|"no"| P["present, exit 0"]
  D -->|"no bundle"| M["missing, exit 13"]
```

A `core.hooksPath` that resolves to `<common-dir>/hooks` is fine. A global or system value is fine
only when it names the dispatcher, meaning a directory that holds an executable `pre-commit` (the
dispatcher chains to `<common-dir>/hooks`).

### `list` and `explain`

Both read `stages.json` from the bundle, so they need a bundle. With no bundle they exit 13. A broken
bundle exits 12. `explain` of an unknown stage exits 2.

### `run`

`pg-hooks run <stage>` changes to the work-tree root and runs
`<bundle>/bin/prek -q run -c <bundle>/prek-config.json --hook-stage <stage> ...`.

- Bare arguments are file paths (made relative to the work-tree root). With no paths and no
  `--all-files`, prek runs over the staged files.
- `--all-files` passes through. Giving both file paths and `--all-files` exits 2.
- `--` forwards everything after it to prek unchanged. Any other `-option` before `--` exits 2.
- `run` needs a bundle. With none it prints the no-bundle notice and exits 13; a leftover
  `.pre-commit-config.yaml` is never read.
- A failing hook prints the hooks-failed hint after prek's output and exits 10.
- After `PG_HOOKS_PROGRESS_AFTER_S` seconds (default 3, `0` disables) one progress line is printed
  to stderr.
- `-q` is prek's global quiet flag. Verified on the pinned prek 0.3.11: it suppresses the per-hook
  "Passed" lines and the "Using config file" line on success, and still prints a failing hook's
  output in full. So a passing run is silent.
- `--timings` (or `PG_HOOKS_TIMINGS=1` in the environment) swaps `-q` for prek's `-v`: every hook
  that ran prints its `Passed`/`Failed` line followed by `- duration: <n>s`, so a slow gate can be
  attributed to one hook without re-running each separately. It goes anywhere after the stage
  (`pg-hooks run pre-land --timings`). It only changes what is printed, never which hooks run or the
  exit status. The `run-unit-tests` hook's time is the sum of its projects; `pg-test-runner` prints
  `[PASS] <project> (<lang>) <n>s` per project. Those projects run one after the other unless the
  `pg-test-runner` config sets `projectConcurrency` above `1` (or
  `PG_TEST_RUNNER_PROJECT_CONCURRENCY` is set); see
  `modules/pg-test-runner/pg-test-runner/pg-test-runner.md`. A Go project whose staged files are
  all non-test-inputs (docs, YAML, README; no `*.go`, `go.mod`/`go.sum`, `testdata/` or
  `//go:embed` target) is skipped with a `[SKIP] <project> (go) ... suite not run` line instead of
  running its suite; `projectExtraPaths` declares extra counted paths (same page).

`pg-hooks run pre-land [<ref>]` runs the `pre-commit` stage over the branch diff, with
`--from-ref <primary> --to-ref <ref>`:

- `<ref>` defaults to `HEAD`. It MUST be the commit checked out in the current worktree, because prek
  reads working-tree files. Anything else exits 2 and names the commit that is checked out.
- `<primary>` resolves as git config `pgii-integrate-branch.primaryBranch`, then
  `git symbolic-ref refs/remotes/origin/HEAD`, then `main`. If it does not resolve to a commit, the
  run exits 2.
- `pre-land` does not take `--all-files` (exit 2).
- `ff-merge-to-main` FF-1b runs this command. On exit 13 it records the notice line in the land
  outcome and continues; on exit 10 it stops the land. When `pg-hooks` is not on PATH (exit 127) a
  caller MUST report `pg-hooks not installed on this machine; ask the operator to run pn workspace
apply` and continue.

### `fix`

`pg-hooks fix` (alias `pre-commit-fix`) runs the bundle's fixers over the STAGED files of this
checkout and restages only the files a fixer changed. It calls the fixer tools directly, never
`prek run`, because prek's hooks are check-mode. Run `git add` first.

- It takes no arguments. With nothing staged it exits 0.
- It operates on added, copied, modified and renamed staged files that still exist. It never stages
  an untracked or unstaged file.
- It exits 2 while a merge, rebase (including `git am`), cherry-pick or revert is in progress in this
  checkout, because fixers restage files.
- The repo's `exclude` (a prek regex from the prek config) is applied with `grep -E`. Fixer
  `includes` and `excludes` are shell globs, so `*` also crosses `/`.
- A file with both staged and unstaged changes is skipped untouched. After the run, `fix` prints one
  line per skipped file and exits 11:
  `pg-hooks: fix skipped <file>: it has staged and unstaged changes. Run: git add <file> && pg-hooks fix (or git restore --staged <file>)`.
- Fixers run in the order of the bundle's `fixers.json`. The default order is `treefmt`, `statix`
  (once per staged `*.nix` file, because `statix fix` takes one target), `treefmt` again,
  `trailing-whitespace`, `end-of-file-fixer`. A repo inserts more by anchor (see
  [Configuration](#configuration)).
- A fixer that exits non-zero is retried once on the same files, because some fixers exit 1 after
  fixing a file. A second failure is a real failure.
- `treefmt` runs to convergence: it repeats until the files stop changing, at most 5 passes. If it
  does not converge, `fix` prints `pg-hooks: fixer <name> did not converge in 5 passes; the files may change again on the next run.`
  and continues.
- File lists are batched at most 200 paths per invocation. ruff MUST NOT be given `--unsafe-fixes`.
- A fixer failure exits 10 after
  `pg-hooks: fixer <name> failed on <files> (exit <n>); its output follows:`, the tool's full
  output, and `Fix by hand, git add the file(s), then rerun pg-hooks fix.`
- `fix` appends one JSON line per fixer per run (`tool`, `files`, `seconds`, `exit`) to
  `${XDG_STATE_HOME:-$HOME/.local/state}/pg-hooks/timings.jsonl`. An unwritable state directory does
  not fail the run.
- `fix` needs a bundle: with no bundle it exits 13, and with an invalid `fixers.json` it exits 12.

A fixer MUST NOT be run from a git hook and MUST NOT be attached to a prek stage.

## The eval-only pre-land check (`pre-land-eval`)

`pre-land-eval` is the land check for a repo that opted in: it evaluates, without building, the
derivation paths of a flake's `checks`, `packages` and `devShells` at one commit, so an evaluation
error (a missing option, a `throw`, a bad import) is caught before landing. It is a standalone
command in `modules/pg-hooks/pre-land-eval/`, a sibling of `pre-commit-fix`, and NOT a `pg-hooks`
subcommand: it needs no hook bundle, and its exit codes are unrelated to the `pg-hooks` ones. It
does NOT run prek or any hook; `pg-hooks run pre-land` keeps owning that. The eval part was deferred
in design pg2-3i12x section 9 and un-deferred by the operator ruling on `pg2-pla9d.26`
(2026-10-23); the work is beads `pg2-d2yka` and `pg2-8uc2d`.

**`pre-land-eval` is a thin caller of `pn workspace flake-check --no-build`.** The evaluation, the
sibling overrides, the system and the experiment log all live in pn (one override derivation, in
pn); `pre-land-eval` adds only the per-clone opt-in key and the one-word verdict with its exit
codes. It runs, from inside the repo:

```text
pn workspace flake-check --no-build --caller ff-1c [--no-ifd] --rev <ref> <abs repo-path>
```

```text
pre-land-eval [--no-ifd] <repo-path> <ref>
```

| Stdout (exactly one line) | Exit | Meaning                                                                                              |
| ------------------------- | ---- | ---------------------------------------------------------------------------------------------------- |
| `ok`                      | 0    | every evaluated set evaluated cleanly                                                                |
| `not-enabled`             | 0    | the clone has not opted in (or the key is absent)                                                    |
| `eval-failed`             | 10   | pn reported a failed evaluation                                                                      |
| (nothing)                 | 2    | usage error: a repo path, ref or workspace repo that does not resolve, or pn failed to run           |
| (nothing)                 | 127  | pn is not installed (`pn not installed on this machine; ask the operator to run pn workspace apply`) |

- stderr carries pn's output: the command to reproduce, the timings and the first error of each
  failed set. No other exit code is produced. There is no `eval-inconclusive` outcome and no retry
  (operator ruling, 2026-10-23, `pg2-8uc2d`): a failure is a failure to fix before landing.
- **Enablement is opt-in per clone.** The check runs only when
  `git -C <repo-path> config --get pgii-integrate-branch.preLandEval` prints exactly `true`. Any
  other value, and an absent key, prints `not-enabled` and exits 0; that is never an error. A repo is
  opted in with `git -C <repo> config pgii-integrate-branch.preLandEval true` (in `pn-workspace.toml`,
  a `post-clone`/`post-update` hook for that repo).
- **Runtime and callers.** Expect 1 to 3 minutes for a small flake, longer for `nix-ziprecruiter`
  (measured 2026-10-23: 191 s for `checks`, 13 s for `packages`, 26 s for `devShells`). Callers MUST
  run it through `bgrun` and `bgwait`.

### `pn workspace flake-check --no-build`

```text
pn workspace flake-check [--no-build [--rev <rev>] [--caller manual|ff-1c] [--no-ifd]] [<repo-or-path>]
```

Without `--no-build` the command is what it always was: a full `nix flake check` per repo, with the
local sibling overrides, which BUILDS the checks. With `--no-build` it is the land check:

- **What it does.** For each repo in topological order (or the one selected), from that repo's
  directory, for each of `checks`, `packages` and `devShells`:
  `nix eval --extra-experimental-features 'nix-command flakes' --no-write-lock-file <override args> --json '<flake>#<set>.<system>' --apply 'set: builtins.mapAttrs (n: v: v.drvPath or null) set'`.
  Nothing is built and `flake.lock` is never written. `<system>` comes from the Go runtime (arm64 to
  aarch64, darwin to darwin), never from an impure `builtins.currentSystem`. A set the flake does not
  provide (nix's `does not provide attribute` error and nothing else) is skipped. A repo failing does
  not stop the others; the command exits 1 if any failed, with each repo's output.
- **Why it is NOT nix's own `--no-build`.** `nix flake check --no-build` runs read-only, so any
  source tree copied into the store during evaluation reads back as `path '...' is not valid` unless
  the same copy is already in the store: the verdict depends on store warmth (measured 2026-10-23,
  session f7a9ef3b: four repos passed on a warm store and `nix-personal` failed the same way with
  and without overrides). `nix eval` realises nothing but is not read-only, so source copies and
  import from derivation both work and the verdict does not depend on the store. Passing nix's flag
  through is therefore NOT the implementation, and the flag's help text says so.
- **Sibling overrides.** The overrides are the SAME set `pn workspace build`, `apply` and
  `flake-check` inject, computed once in pn from the effective workspace lock:
  `--override-input <alias> git+file://<absolute sibling clone>` per workspace dependency (the repo is
  not overridden by itself, and a sibling whose clone is missing is left out). They read the
  sibling's WORKING TREE, not its committed HEAD (operator ruling, 2026-10-23), so a sibling's
  uncommitted edits can fail the check. A repo whose locked siblings lag the local clones therefore
  gets a true answer with no push, no relock and no `flake.lock` write. No land step may call
  `pn workspace update`, `upgrade` or `lock` to make this pass.
- **Selecting one repo.** A positional `<repo-or-path>` is a workspace repo key, or a path to the
  repo or to one of its git worktrees (inside the repo or kept elsewhere). A key evaluates the
  canonical clone; a path evaluates THAT directory, which is how the land step checks its worktree
  against the canonical siblings. `--rev <rev>` evaluates that commit of the selected repo as the
  pure ref `git+file://<abs dir>?rev=<sha>` (an unresolvable rev exits 2; a dirty tree and untracked
  files are ignored). Selection options are an error (exit 2) without `--no-build`, so the
  no-flag behavior is unchanged.
- **Failure semantics.** A failure is a real problem to fix before landing. There is no
  `inconclusive` class, no retry and no classifier: an environmental failure (store database busy,
  fetch failure, timeout) fails the check like an evaluation error, a sibling's uncommitted edits can
  fail it, and a pass proves evaluation only (nothing is built or tested, one system, and
  `darwinConfigurations` is not evaluated). A failure that is not a flake defect is reported on the
  bead, not classified away. `--no-ifd` adds `--option allow-import-from-derivation false`, so a
  needed import from derivation then fails.
- **Exit codes.** 0 every repo evaluated, 1 a repo failed, 2 a bad invocation (unknown repo, an
  unresolvable `--rev`, an unknown `--caller`, no workspace).
- **Experiment log.** The land step is an experiment (operator ruling, 2026-10-23; review bead
  `pg2-z1r54`). Every `--no-build` run appends ONE JSON line per evaluated repo to
  `${XDG_STATE_HOME:-$HOME/.local/state}/pn/flake-check-no-build.jsonl`:
  `ts` (UTC, RFC 3339), `repo`, `caller` (`ff-1c` or `manual`), `duration_s`, `outcome` (`ok` or
  `failed`), `nix_exit` (0, or the exit code of the first failing set), `failed_sets` (the sets that
  failed), `first_error` (the first `error:` line of nix's stderr, at most 300 characters, empty on
  success). An unwritable state directory prints a warning and does not fail the check.
- **Tests.** Go unit tests with a scripted runner pin the argument plumbing; real-nix tests in
  `modules/pn/cmd/pn/flake_check_no_build_realnix_test.go` pin the behavior against a real nix and
  real git (nothing is built, a source copy at eval time passes on a cold store twice with the same
  verdict, the sibling-drift fixture fails without overrides and passes with them, the log line
  shape). They skip inside a nix builder, where no daemon exists.

Deliberate gaps:

- `darwinConfigurations` is NOT evaluated. `nix flake check` does not cover it either.
- Checks that need import from derivation are evaluated through it by default (the evaluation then
  builds the small IFD derivations: fixed source copies and fetches). Evaluation is a store-write
  operation: it writes `.drv` files and source copies to the store.
- Only workspace siblings are overridden. A third-party input still resolves from the repo's
  `flake.lock`, and a repo outside the workspace is not checkable (exit 2).
- `nix-ziprecruiter` is enabled in `pn-workspace.toml` (its `preLandEval` hook): its locked
  siblings lag the local clones (`The option phillipgreenii.observability.dockerState does not
exist` against the locked inputs), and with the overrides the check returns `ok`. `nix-overlay`
  is not enabled.

## Messages

Every message starts with `pg-hooks:` and goes to stderr. The texts are fixed, because callers and
tests match them.

| Situation                               | Text                                                                                                                                                                  |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| no bundle, canonical clone              | `pg-hooks: no hook bundle for <repo>; <stage> hooks not run. Fix: (cd <canonical> && nix run .#install-pre-commit-hooks)`                                             |
| no bundle, plain worktree               | `pg-hooks: no hook bundle for <repo> (shared bundle lives in <canonical>); <stage> hooks not run. Fix: (cd <canonical> && nix run .#install-pre-commit-hooks)`        |
| no private bundle, set member           | the same text, with the private `reinstall` line as the fix                                                                                                           |
| broken bundle                           | `pg-hooks: hook bundle for <repo> is broken (<reason>). Rebuild: <reinstall>`                                                                                         |
| stale bundle                            | `pg-hooks: hook bundle is stale for <repo> (built <built_at>). Rebuild: <reinstall>`                                                                                  |
| stale, a recorded override changed      | the stale text with ` (override <name> changed)` after the built time                                                                                                 |
| worktree differs from the shared bundle | `pg-hooks: this worktree's hook definitions differ from the shared bundle; the old hooks ran. To test your hook changes: <private reinstall command>`                 |
| unreachable hooks                       | `pg-hooks: git does not run hooks from <common-dir>/hooks (core.hooksPath=<value> from <origin>). Operator: git -C <canonical> config --local --unset core.hooksPath` |
| relocated clone                         | `pg-hooks: this clone moved from <clone_path>; its bundle is no longer GC-rooted. Rebuild: <reinstall>`                                                               |
| hooks failed                            | `pg-hooks: <stage> hooks failed in <repo>. Auto-fixable formatting: run 'pg-hooks fix', then git add and commit again. Other failures: fix by hand.`                  |
| progress (after 3 s, once)              | `pg-hooks: running <stage> hooks in <repo>...`                                                                                                                        |

A rebuild is a nix build. Agents MUST run it through `bgrun` and check it with `bgcheck`. The
installer's own refusals are in [Installer](#installer).

## Exit codes

`1` is a generic failure and is never given a branchable meaning. Branch only on the codes below.

| Code | Meaning                                                                               |
| ---- | ------------------------------------------------------------------------------------- |
| `0`  | success or nothing to do                                                              |
| `1`  | generic failure, no branchable meaning                                                |
| `2`  | usage error, unknown stage, refused operation, or not in a git repository             |
| `10` | a hook or fixer failed                                                                |
| `11` | file(s) skipped because they have staged and unstaged changes                         |
| `12` | bundle broken (pointer invalid, or `bin/prek` not runnable)                           |
| `13` | no bundle (`run`, `fix`, `list`, `explain`)                                           |
| `14` | `status` only: bundle stale                                                           |
| `15` | `status` only: hooks unreachable (`core.hooksPath` does not resolve to the hooks dir) |
| `16` | `status` only: clone relocated                                                        |

Exit codes at the other seams:

- A git hook (the stub) exits 0 on "no bundle". With a bundle it returns prek's exit status
  unchanged. A bundle whose `bin/prek` is not executable prints the broken-bundle message and exits 12.
- `127` means `pg-hooks` is not on PATH. Stubs do not depend on PATH.
- `pg-hooks-install` (see [Installer](#installer)) exits 0, 2 for a refusal or usage error, and 1
  for anything unexpected.

## Bundle layout

The flake output `packages.<system>.pg-hooks-bundle` (present unless `bundle.enable = false`; also
exposed as `legacyPackages.<system>.pgHooksBundle`) is a store path holding:

```text
bin/prek                  the repo's locked prek
bin/pg-hooks-run          the stage runner
lib/pg-hooks-lib.bash     the library shared with the pg-hooks command
prek-config.json          the rendered prek config (embeds hook store paths, which roots their closure)
stages.json               stage -> [{ id, reason }]
fixers.json               the resolved, ordered fixer list
meta.json                 { repo, stampPaths, stages }
```

Per clone, in the git common directory:

```text
<common-dir>/pg-hooks/
  current                 regular file, one line matching ^gen-[0-9]+$
  reinstall               regular file, one line: the exact command that rebuilds this bundle
  stale-warned-<stamp>    marker for the once-per-stamp stale warning (under the checkout's own <git-dir>/pg-hooks)
  gen-7/bundle        ->  /nix/store/...-pg-hooks-bundle   (nix indirect GC root, never moved)
  gen-7/source.json       { stamp, overrides, clone_path, built_at }
  gen-6/...               previous generation
```

A private bundle (for a workforest set member) has the same layout under the worktree's own git
directory, `$(git rev-parse --absolute-git-dir)/pg-hooks`. A hook or `pg-hooks` command uses the
private directory when it holds a `current` file, else the shared one.

Rules:

- `current` MUST be a regular file replaced by `rename(2)` from a temp file in the same directory.
  A symlink swap is not atomic for concurrent readers on APFS, and BSD `mv -f` onto a
  symlink-to-directory silently fails, so a symlink pointer MUST NOT be used.
- A reader MUST read `current` once, validate it against `^gen-[0-9]+$`, and resolve everything
  under that one generation. A pointer that is empty, has path separators, or is a symlink is
  invalid (`broken`, 12).
- The installer claims generation N (1 plus the largest existing) with `mkdir gen-N`, which is
  atomic, and retries with N+1 if it exists. Concurrent installs each get their own generation and
  the last writer wins.
- A generation directory MUST NOT be moved after `nix-store --add-root --indirect`: the GC root
  names the out-link path.
- Pruning keeps the generation named by `current` and the highest complete generation below it. It
  never deletes `current`, `reinstall`, the stale-warned markers, generations above `current`, or a
  generation with no `source.json` (a concurrent install may own it).
- All git paths are requested with `--path-format=absolute`.

### Stubs

A stub at `<common-dir>/hooks/<stage>` is static POSIX sh with no store path. Its second line is
`# managed-by: pg-hooks`. It chooses the private bundle directory if it has `current`, else the
shared one; reads and validates `current`; and runs `gen-N/bundle/bin/pg-hooks-run <stage> "$@"`
with `PG_HOOKS_BUNDLE` set. If the pointer or the runner is missing, it prints the no-bundle notice
and exits 0.

`pg-hooks-run` warns about staleness (see below), runs
`<bundle>/bin/prek -q hook-impl --config <bundle>/prek-config.json --hook-type <stage> --hook-dir <common-dir>/hooks --script-version 4 -- "$@"`
as a child with stdin and arguments passed through, and returns prek's exit status. On a non-zero
status it prints the hooks-failed hint after prek's own output.

### Installer

`install-pre-commit-hooks` keeps its name. Unless `bundle.enable = false` it execs
`pg-hooks-install --bundle <store path> "$@"`; the script text embeds the bundle's store path, so
`nix run [--override-input ...] .#install-pre-commit-hooks` realises the bundle with the same
overrides, and the installer only roots it. It never calls `nix build`, never writes
`core.hooksPath`, and the devShell installs nothing.

| Flag                       | Meaning                                                                                                      |
| -------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `--bundle <path>`          | The built bundle (supplied by the wrapper).                                                                  |
| `--private`                | In a linked worktree: install a worktree-only bundle. In the canonical clone it is a plain install.          |
| `--override <name>=<path>` | Repeatable. Records the path's HEAD and dirty state in `source.json`; pn passes its `--override-input` pins. |

- Canonical clone: claim `gen-N`, add the root, write `source.json`, swap `current`, write
  `reinstall`, prune, then write or refresh the stubs.
- Linked worktree without `--private`: refused with exit 2 and
  `pg-hooks: install refused in a linked worktree; the shared bundle belongs to <canonical>. Run there: (cd <canonical> && nix run .#install-pre-commit-hooks), or add --private for a worktree-only bundle.`
- `--private` writes only under the worktree's git directory. It MAY add a stub for a stage that has
  none, and MUST NOT rewrite or remove an existing stub.
- Stubs: a file carrying `# managed-by: pg-hooks` or the legacy prek marker `File generated by prek`
  is replaced. Own stubs for stages that no longer have hooks are removed. A foreign file at a
  needed stage refuses the install (exit 2) before anything changes:
  `pg-hooks: install refused: <path> is not managed by pg-hooks (first line: <line>). Move it aside or chain it yourself, then rerun: <reinstall>`.
- The `reinstall` line is `(cd <canonical> && nix run .#install-pre-commit-hooks)` for the canonical
  clone, and `(cd <worktree> && nix run <pins> .#install-pre-commit-hooks -- --private <overrides>)`
  for a private bundle.

## Staleness

The stamp is `git ls-files -s -- <inputs> | git hash-object --stdin`. It reads the index, so a staged
edit counts. The inputs are `flake.lock`, `flake.nix` and the repo's `stampPaths` (from
`meta.json`). The bundle reflects the working tree at install time while the stamp reflects the
index, so an unstaged edit at install time is in the bundle but not in the stamp; that is accepted.
With no tracked input the stamp is `unknown` and nothing warns.

`pg-hooks-run` warns only on `pre-commit` and `pre-push`, and at most once per (checkout, stamp): it
writes a marker `stale-warned-<stamp>` under the checkout's own `<git-dir>/pg-hooks`, so a rebase that
replays many commits prints one line. A recorded override whose HEAD differs, or whose dirty state
differs, adds ` (override <name> changed)`. A plain worktree on the shared bundle whose own stamp
inputs differ gets the worktree-differs message, which names the private reinstall command. Hooks
still run when the bundle is stale.

`ff-merge-to-main` FF-4a refreshes the canonical clone's bundle after a successful land that touches
a stamp input: it runs `<canonical>/.git/pg-hooks/reinstall` in the background through `bgrun`. A
failure is reported in the land outcome and never fails the land.

## Old config symlinks

The per-clone bundle is the only hook mechanism. A clone that still holds the old
`.pre-commit-config.yaml` symlink (ADR [0016](adr/0016-gitignore-generated-pre-commit-config.md)) but
no bundle reports `state=missing` and exits 13 from `run`, `fix`, `list` and `explain`; the file is
never read. Install the bundle to fix it.

- Tooling MUST NOT link, copy or regenerate a hook config to make hooks run, and MUST NOT read a
  leftover `.pre-commit-config.yaml`.
- The probe for "does this repo have hooks" is `pg-hooks status --porcelain`, never
  `test -f .pre-commit-config.yaml`: a bundle repo has no such file in its working tree yet its hooks
  are live.
- The `.gitignore` line for `.pre-commit-config.yaml` and the `pre-commit-config-gitignored` check
  stay until no clone holds the old symlink (see [Open follow-ups](#open-follow-ups)).

Workforest sets use the shared bundle by default. pn's set setup builds a private bundle with the
set's pins (`--private` plus the `--override` pins) after the worktree exists, so a set that changes
a producer tests that change in its own hooks. After the agent edits the producer, the private bundle
is stale and the stale line names the private reinstall command.

## Configuration

Each repo configures hooks in its `flake.nix` through `phillipgreenii.pre-commit`:

| Option                 | Meaning                                                                                                                                                             |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `extraHooks`           | Extra prek hooks. Each hook's `stages` decides where it runs.                                                                                                       |
| `excludes`             | Path excludes (prek regexes).                                                                                                                                       |
| `fixers`               | Extra fixers for `pg-hooks fix`: `{ name, command, mode, includes, excludes, after, before }`.                                                                      |
| `stampPaths`           | Repo-relative paths hashed into the stamp in addition to `flake.lock` and `flake.nix`.                                                                              |
| `bundle.enable`        | Render the bundle and make `install-pre-commit-hooks` install it. Default `true` (ADR 0032); `false` installs no hooks (the installer prints a notice and exits 0). |
| `driftGuard.enable`    | Contribute the `hook-drift-guard` flake check (see [Drift guard](#drift-guard)). Default `false`.                                                                   |
| `driftGuard.allowlist` | Explicit exceptions to the drift guard: `{ path, categories, reason }`, all three mandatory.                                                                        |

`fixers`: an entry without `after` or `before` is appended after the default fixers; with an anchor
it is inserted directly after or before the named fixer (an anchor MAY name another added fixer).
`mode = "per-file"` runs the command once per file. Evaluation fails, when `bundle.enable` is set,
for: duplicate fixer names; an unknown or
unresolvable anchor, or both `after` and `before`; a fixer carrying a stage; and any enabled prek hook
whose `entry` invokes `pg-hooks fix` or `pre-commit-fix`. An unknown stage name is rejected by
git-hooks.nix's own stage type.

Hook definitions that arrive through `flake.lock` need no `stampPaths`; repo-base itself lists
`flake-modules/pre-commit.nix`, `modules/pg-git-check-identity` and
`modules/pg-check-grafana-alert-uids` because its hooks live in the repo.

The global dispatcher (the personal repo's `home/programs/git`, one script registered under every
client-side hook name) exists so that a fresh clone with no local `core.hooksPath` still reaches
`<common-dir>/hooks`. It runs `pg-git-check-identity` first on `pre-commit`, then execs
`<common-dir>/hooks/<stage>` when that file is executable, else exits 0. A local `core.hooksPath`
shadows the dispatcher entirely; one equal to the absolute `<common-dir>/hooks` is correct, and any
other local value is reported as `unreachable`.

pn reads the bundle state for its install gate ("already installed" means the pointer is valid, the
bundle is live, the stamp matches and every recorded override is unchanged), runs
`pg-hooks run pre-commit --all-files` per clone for `pn workspace pre-commit-check`, and its doctor
check `pre-commit-hook-live` reports, for repos whose `pn-workspace.toml` entry runs
`install-pre-commit-hooks`: a missing pointer, a broken or dangling bundle, a relocated clone,
unreachable hooks, and missing or foreign stubs.

## Grafana alert uid hook

`check-grafana-alert-uids` is a base hook: it is part of the standard set in
`flake-modules/pre-commit.nix`, so every repo that imports the module gets it on the next hook-bundle
rebuild, with no per-repo wiring. It runs at the `pre-commit` stage (so also under
`pg-hooks run pre-land`) and rejects, over Grafana alerting provisioning files, the uid mistakes that
make Grafana refuse to start (2026-10-13: a 42-character uid in `pg-desk` crash-looped Grafana and
aborted `pn workspace apply`).

| Rejected                                                                | Query on the YAML                  |
| ----------------------------------------------------------------------- | ---------------------------------- |
| an alert-rule uid longer than 40 characters                             | `.groups[].rules[].uid`            |
| an alert-rule uid declared twice within one file                        | `.groups[].rules[].uid`            |
| a contact-point receiver uid longer than 40 characters                  | `.contactPoints[].receivers[].uid` |
| a selected file that does not parse as YAML (Grafana could not load it) |                                    |

Policies:

- The hook MUST select only `grafana/alerting/**/*.yaml` and `*.yml`, matched at any depth of the
  repo (`(^|/)grafana/alerting/.*\.ya?ml$`).
- It MUST NOT select promtool rule-test fixtures: anything under a `rule-tests*/` directory, and any
  `*.test.yaml` / `*.test.yml`, are not provisioning files.
- A receiver uid is only length-checked; a repeated receiver uid is not an error (not verified
  against Grafana). A duplicate is checked per file, not across files: the cross-file duplicate check
  stays with nix-support-apps' build-time funnel validator
  (`darwin/modules/observability/alerting-validate.nix`), which this hook complements and does not
  replace.
- Length is counted in bytes, which is how Grafana counts it.
- The YAML MUST be read with a real parser (`yq-go`), never a regex. The hook invokes no nix (HK-2):
  the script and `yq-go` come from the bundle's store closure.

Each violation is printed on stderr with the file, the uid and its length, for example
`ERROR: pkg/grafana/alerting/alerts.yaml: alert-rule uid '<uid>' is 41 bytes long (limit 40); Grafana rejects uids longer than 40 and fails to start`,
and the hook exits 1 (`pg-hooks run` then exits 10). Source: `modules/pg-check-grafana-alert-uids/`.
Tests: the bats suite, flake check `test-pg-check-grafana-alert-uids` (the script against the real `yq`:
41-character rule uid rejected, duplicate rejected, 41-character receiver uid rejected, 40-character
uid passes) and the flake check `pg-hooks-grafana-alert-uids-real-tools` (the same verdicts through
the real bundle, plus file selection).

## Drift guard

Two checks keep the removed hook machinery from coming back. The source-tree half is the
`hook-drift-guard` flake check; the runtime half is the `hook-drift` rows of `pn doctor`. They are in
addition to the bundle-state check `pre-commit-hook-live`.

`hook-drift-guard` (enabled per repo with `phillipgreenii.pre-commit.driftGuard.enable`, default
`false`, so a relock never reddens a consumer) runs `pg-hooks-drift-guard` over the repo's source and
fails on any of these outside `driftGuard.allowlist`:

| Category              | Finds                                                                                                    |
| --------------------- | -------------------------------------------------------------------------------------------------------- |
| `githooks-dir`        | a `.githooks` directory                                                                                  |
| `githooks-ref`        | text that names `.githooks`                                                                              |
| `relative-hooks-path` | text that sets `core.hooksPath` to a relative value (`.githooks`, `./x`, `../x`, `dir/x`)                |
| `config-link`         | link code or text for the old generated config (`ln -s`, `Symlink`, `readlink`, "link") on a config line |
| `removed-machinery`   | an identifier of the deleted shim, link and higher-scope `core.hooksPath` code (spec 7.5)                |

The allowlist is data, never a loosened pattern: each entry is a path glob, the categories it allows
(or `*`) and a reason, so an exception to `config-link` in one file never hides a `.githooks` in it.
An entry that matches nothing is noted on stderr and is not a failure. A consumer adopts the guard by
setting `driftGuard.enable = true` and listing its own intentional hits; repo-base's list is
`nix/hook-drift-allowlist.nix`.

`pn doctor` check `hook-drift` audits every workspace repo that is a git work tree (declared or not)
and its linked worktrees:

| Row                                 | Severity | Condition                                                                                           |
| ----------------------------------- | -------- | --------------------------------------------------------------------------------------------------- |
| `.githooks` exists                  | error    | the removed shim's hook directory is in a checkout                                                  |
| relative `core.hooksPath`           | error    | any scope, including a value a later absolute one shadows                                           |
| hand-made `.pre-commit-config.yaml` | warning  | a regular file, or a symlink that does not point into `/nix/store` (the old generated link is kept) |

## Operator runbook

These steps are for the operator. Agents MUST NOT run them.

1. `pn workspace apply` puts `pg-hooks`, `pre-commit-fix` and `pre-land-eval` on PATH and ships the
   dispatcher. Afterwards, `command -v pg-hooks` and `command -v pre-land-eval` succeed.
2. For a repo that has set `bundle.enable = true`, install in its canonical clone:
   `(cd <canonical> && nix run .#install-pre-commit-hooks)`, then `pg-hooks status` and one real commit.
3. If `status` reports `unreachable`, remove the local `core.hooksPath` that bypasses the hooks
   directory, or point it at the absolute `<common-dir>/hooks`:
   `git -C <canonical> config --local --unset core.hooksPath` (the message names the exact command).
4. `pn workspace push` publishes a producer repo before its consumers relock onto it.

## Open follow-ups

Dated 2026-10-03. This section is the only part of the page that goes stale; the rest describes the
code.

- Retiring the ADR 0016 `.gitignore` line and `checks.pre-commit-config-gitignored` MUST wait until
  the repos are pushed and relocked and no clone holds the old symlink. Consumer flakes MUST NOT drop
  `bundle.enable = true` or their `.gitignore` lines before that relock.
- The pn Go cleanup (the githooks-shim doctor check and the remaining link code) is tracked as
  `pg2-o1fle`.
- Not available yet: the `post-land` entry point (deferred, `pg2-na2nr`). Consumer repos other than repo-base have not yet enabled `driftGuard`.
