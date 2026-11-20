# pn workspace update scopes, per-step update-locks timing, and `pn workspace explain` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task (operator ruling D11; not superpowers:executing-plans). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a human or agent choose how much `pn workspace update` relocks (siblings only, every flake input, or everything including `update-locks.sh`), and explain afterwards, from the run's trace, where an update spent its time.

**Architecture:** A `--scope siblings|flake|full` option on `pn workspace update` selects a per-repo relock Strategy. The new `flake` strategy generalizes the existing sibling relock (`propagateWorkspaceEdges`) to every input; `full` runs `update-locks.sh` exactly as today and then the `flake` relock. `update-locks.sh` reports one `UL_STEP` line per step on the stdout channel that already carries `UL_RESULT`; pn turns those into `pn.ul.step` spans and `events.jsonl` fields. A new `pn workspace explain` command fetches one trace from Tempo by id and reports where the time went.

**Tech Stack:** Go (cobra, OpenTelemetry SDK) in `modules/pn`; bash + bats in `lib/scripts` and `modules/pnwf`; Tempo HTTP API v2.

**Spec:** No separate spec document. The decisions below are the spec; they come from the 2026-11-18 interactive session (operator: Phillip) and independent reviews of revisions 1 and 3 of this plan (revision 4 folds in the second review, read against nix-repo-base `main` as of 2026-11-18 evening). Repo root for every path: `/Volumes/gitrepos/phillipg/nix-repo-base` (canonical clone; work happens in a worktree, Task 0). Sibling repos live under `/Volumes/gitrepos/phillipg`.

## Decisions (the spec)

- D1 (verbatim): "what i want to have is to be able to only update sibling references OR sibling references and the flake lock OR sibling references, flake lock, and run the full update-locks.sh."
- D2 (verbatim): "by default, lets have the skills run flake scope."
- D3 (verbatim): "we would only do the flake one per week, on the weekend, so if it build extra stuff, that isn't terrible. but we should have something which checks the metrics/traces to see why it took so long."
- D4 (verbatim): "not schedules as part of this effort, just provide the capability of humans agents to have more control over what is being updated."
- D5 (operator ruling, Phillip, 2026-11-18, confirming the agent proposal): no guard against expensive nixpkgs bumps in this effort; the weekly run absorbs them (D3). Verbatim answer to "confirm no guard": "yes; i would like to do more, but there doesn't seem to be a simple check, so we will contiue as is for now."
- D6: the `pn` CLI default stays `full`; the skills (`/pn-workspace-update`, `pnwf update-relock`) default to `flake` (D2); `doctor --fix` uses `siblings`. `pn workspace upgrade` uses `full`, so it gains the trailing relock of D7.
- D7: `full` = `./update-locks.sh` exactly as today, THEN the `flake` relock. Running the relock last keeps today's step order and setup cost in every consumer script, and still closes the gap that makes today's update not a superset of `siblings`: `update-locks.sh`'s `nix-flake-update` step sits behind the 12-hour step stamp (`UL_STALE_SECONDS=43200`) and runs without `--refresh`, so within 12 hours of the last run neither siblings nor nixpkgs move. When that stamp is stale the trailing relock is a fast near no-op. (Revision 1 ran the relock first; the review showed that moves the bumped-lock `resolve-nrb-ref` fetch and the dev-shell rebuild into every consumer's setup and inverts nix-support-apps' and nix-overlay's step order.)
- D8: rejected alternative, for the ADR: filtering steps inside `update-locks.sh` (`UL_ONLY_STEPS=nix-flake-update`). It keeps the lib's rollback and failure classification, but every repo still pays `resolve-nrb-ref`, `determine-ul-lib-dir`, the `nix develop` re-exec and the hook-bundle install, which is the cost `flake` scope exists to avoid.
- D9: relock failure handling (pn has no ADR 0020 classifier). A failed `nix flake update` is retried once. A failure whose stderr contains `No space left on device` is not retried and aborts the whole run (the same environmental abort `update-locks.sh` signals with exit 77). Any other failure after the retry: in `flake`/`siblings` scope the repo fails at `relock-siblings`/`relock-flake`; in `full` scope the repo still integrates its `update-locks.sh` commits, and the relock failure is a warning (`project_result` level `warn`, `relock_failed: true`), because discarding a long `update-locks.sh` run over a relock blip is worse than one week of unmoved inputs. ADR 0045 records this as a known difference from ADR 0020's transient handling.
- D10 (operator ruling, Phillip, 2026-11-18): Task 8 files a nix-agent-support bead (label `agent-support`) to add `explain` to the tool approver's approved `pn workspace` subcommands and `--scope` to its `pn`/`pnwf` command specs. Verbatim answer to "may an agent file that bead": "yes".
- D11 (operator ruling, Phillip, 2026-11-18): implementation mode is Subagent-driven (superpowers:subagent-driven-development: a fresh implementer and a fresh reviewer per task), not Native. Verbatim answer: "subagent".
- D12 (operator ruling, Phillip, 2026-11-18, on revision 4's plan to defer the smoke suite to a post-deploy bead): Task 8 runs `pn-smoke-tests` deliberately before landing, because this change alters the default `full` update path that its scenarios drive. ADR 0024 still keeps the suite out of `checks`, hooks and the land gate; this is a run the change warrants, not a gate. Verbatim: "if in this specific situations, it makes sense to run the smoke tests, then consider it. the rule about blocking landing is the general rule. you should run what you need to be confident of the current changes."

Evidence behind D3 and Tasks 4 to 7 (2026-11-18, the full in-place update 14:49Z to 17:14Z): `pn.repo` seconds were nix-agent-support 4834, nix-overlay 1834, x 603, nix-support-apps 441, nix-repo-base 198, nix-ziprecruiter 171, nix-personal 114. Each repo's `update-locks.sh` ran inside one `nix develop` `nix.invocation` span, and its non-nix steps had no spans. In nix-overlay and nix-agent-support that `nix develop` span ended 348 s and 126 s after its parent `pn.exec`, at the exact second the next repo's `pn.repo` began. The cause was found and fixed on 2026-11-19, before Task 0: the machine slept during those runs, and pn's spans ended on the monotonic clock, which stops during sleep, while the wrapper stamps from the wall clock. pn now ends every span from the wall clock (see Global Constraints).

## Global Constraints

- Line numbers are pointers, not addresses. This plan's `file:line` citations were checked against nix-repo-base `main` at `e0a5b2d` (2026-11-18). `main` then gained eight commits before Task 0 (2026-11-19), which shift lines in `flake.nix` (about +96: the turn-boundary check now starts at `:1387`, the land-gate check at `:1611`, `integration-checks` at `:517`), `lib/scripts/update-locks-lib.bash` (about +9 to +13: `ul_run_step` at `:742`, `_ul_disable_fsmonitor` at `:434`, `_ul_commit_updated` at `:938`, `ul_brew_lock_update_step` at `:1028`) and `modules/pn/internal/workspace/propagate.go` (about +7 to +10). Every citation also names a symbol, check, test or quoted string: find that by name, and if the line number disagrees, trust the name.
- Every pn span ends with an explicit wall-clock timestamp, `span.End(endNow())` (`telemetry.go:362-371`, commit "stamp span ends from the wall clock so a sleep cannot orphan child spans"): the SDK's default end reads the monotonic clock, which stops while a Mac sleeps, so a child stamped from the wall clock could outlive its parent. New span code in this plan follows it.
- Exit code 1 is generic; any branchable exit code is >= 2.
- Go tests: `nice -n 10 go test -p 4 ...` with `GOCACHE=/Volumes/gitrepos/build_tmp/shared/go-build`; unit tests take milliseconds and run in any order; nothing in this plan needs a build tag.
- Tests that need git use the existing `newFixtureRepo` / `propEnv` helpers (backed by `x/gittest`). Subprocesses are answered with `exec.NewFakeRunner()` or the `fsNixRunner` test double, never by re-executing the test binary.
- No bead ids in commits, code comments, docs or test names. This plan's committed copy (Task 0) counts as a doc, so it carries none either.
- `update` stays local-only: no `git push` anywhere (ADR 0023). Nothing in this work pushes.
- Markdown: run `nix fmt` to convergence before committing; diagrams are mermaid.
- `UL_RESULT transient=<N>` keeps its meaning and stays the last `UL_RESULT` line (ADR 0020); `UL_STEP` lines are additive.
- Span ids from Tempo are opaque base64 strings (verified 2026-11-18: `spanId` `3JpUvBWOTMY=`); never hex-decode them. A trace id on the command line is 32 hex characters (Tempo's API form).
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. A `flake` relock whose only `flake.lock` change is `lastModified` (common under `--refresh`) leaves a clean tree and makes no commit. Test: Task 1 `TestRelockAll_NoCommitOnLastModifiedChurn`.
2. A repo whose `flake.nix` declares an input missing from `flake.lock` gets a commit whose body names the input `(new)`. Tests: Task 1 `TestDiffFlakeLocks` row `new root input`, `TestFlakeUpdateCommitMessage` row `(new)`, `TestRelockAll_NewInputCommits`.
3. A network blip in the trailing relock of a `full` run keeps the repo's `update-locks.sh` commits and integrates them, with a warning. Tests: Task 1 `TestRelockAll_RetriesOnce`, Task 2 `TestUpdate_ScopeFull_RelockFailureWarnsAndIntegrates` (in-place and worktree).
4. A run whose lib predates `UL_STEP` (a subset set or a standalone script run) records no step spans, and `explain` says why instead of showing zero-second steps. Tests: Task 5 `TestParseULSteps` row `no lines`, Task 6 `TestAnalyze_RepoWithoutSteps`.
5. `explain` on a run that is still going, was killed, has not been ingested, has expired, or with Tempo down exits with a code that has exactly one next action (75 retry shortly, 4 re-run with `--partial`, 3 nothing to do, 5 fix the Tempo connection) and prints one line saying what to do. Tests: Task 6 `TestAnalyze_IncompleteTrace`, Task 7 `TestExplainCmd_ExitCodes`.

---

### Task 0: Isolation

- [ ] **Step 1:** From `/Volumes/gitrepos/phillipg`, invoke `pn-workspace-rules:fork-workforest` for a subset set: `pn workspace workforest add pn-update-scope --repos nix-repo-base`. If `pg-gwm config check` in the canonical clone shows a worktree root, lease through `pg-gwm:pg-gwm` instead. Later paths are relative to the set's `nix-repo-base` worktree.
- [ ] **Step 2:** Copy this plan to `docs/superpowers/plans/2026-11-18-pn-update-scope-and-explain.md`. Confirm `rg -n 'pg2-[a-z0-9]{4,}' docs/superpowers/plans/2026-11-18-pn-update-scope-and-explain.md` prints nothing (the pattern does not match its own text), then commit it alone (`docs: plan for update scopes and pn workspace explain`).

### Task 1: Relock-all primitive with its own span

**Files:**

- Create: `modules/pn/internal/workspace/relock.go`
- Modify: `modules/pn/internal/telemetry/telemetry.go` (new span starter)
- Test: `modules/pn/internal/workspace/relock_test.go`; extend `fsNixRunner` in `propagate_test.go` (`:83-102`), keeping its existing `mutate`, `nixErr` and `nixArgs` fields working for their current users (`propagate_test.go`, `push_test.go:1122`, and the seven `.nixArgs` reads in `doctor_fix_nix_test.go` from `:81`), so that (1) it records each call's name, args, `opts.Dir` and `opts.Env` in one ordered list; (2) each scripted nix call carries an optional `mutate`, a `(Result, error)` pair and an optional cancel hook, and runs the `mutate` BEFORE returning the error (today it returns `nixErr` before `mutate`); and (3) it intercepts, records and answers the three fsmonitor git calls itself (`git -C <dir> config --type=bool --get core.fsmonitor`, `git -C <dir> rev-parse --absolute-git-dir`, `git -C <dir> fsmonitor--daemon stop`) instead of passing them to real git. When a test scripts nothing for them, it answers the config read with exit 1 and no output (unset) and `rev-parse` with exit 1; scripted answers end in `\n`, as git's do; `modules/pn/internal/telemetry/telemetry_test.go`

**Interfaces:**

- Consumes: `openGitMutator`, `restoreAfterFailedCommit`, `ws.pathClean`, `shortRev` (`propagate.go`); `exec.Result.Stderr` (captured while streaming, `internal/exec/exec.go:201`); `exec.RunOptions` (`internal/exec/exec.go:34`).
- Produces:
  - `type inputChange struct{ Name, Old, New string }`
  - `func diffFlakeLocks(before, after []byte) ([]inputChange, error)`
  - `func flakeUpdateCommitMessage(changes []inputChange) string`
  - `var errRelockNoSpace = errors.New("nix flake update: no space left on device")`
  - `var errRelockRestore = errors.New("relock: could not restore flake.lock")` (wrapped with the cause; a repo failure in every scope, never downgraded to a warning)
  - `var writeLockFile = os.WriteFile`, a package-level seam the restore uses, so a test can make the write-back fail
  - `func (ws *Workspace) relockAllInputs(ctx context.Context, out io.Writer, name, dir, flakeRel string) (changed int, err error)`
  - `func StartRelock(ctx context.Context, kind string) (context.Context, func(changed int, err error))` in `telemetry`: a `pn.relock` span, child of the current span, attributes `pn.relock.kind` (`siblings`|`flake`) and, at end, `pn.relock.changed`; Error status on err; ended with `span.End(endNow())` like every pn span.

- [ ] **Step 1: Write the failing tests**
  - `TestDiffFlakeLocks` (table): `rev moved` → `[{sib 1111111 2222222}]`; `lastModified only` → empty; `follows input` (root input is an array) → ignored; `new root input` → `Old == "(new)"`; `removed root input` → `New == "(removed)"`; `tarball input without rev` → Old/New are the first 12 characters of `narHash` after `sha256-`; `structure only` (root `inputs` map changes, no root locked change) → `[{"(lock structure)" "" ""}]`; malformed JSON → error.
  - `TestFlakeUpdateCommitMessage` (table): subject exactly `chore(deps): nix flake update`, a blank line, then one line per change `<name> <old> -> <new>` in name order; rows for a rev change, `(new)`, `(removed)` and `(lock structure)` (that row's body line is exactly `(lock structure) changed`).
  - `TestRelockAll_BumpsAndCommits`: the lock gets a new rev; `changed == 1`; one nix call with args exactly `flake update --refresh` and `Dir == dir`; subject `chore(deps): nix flake update`; committed files `[flake.lock]`; clean tree.
  - `TestRelockAll_NoCommitOnLastModifiedChurn`: only `lastModified` changes; `changed == 0`; commit count unchanged; clean tree.
  - `TestRelockAll_NixWroteNothing`: nix succeeds without touching the lock; `changed == 0`; no commit.
  - `TestRelockAll_StructuralOnlyChangeCommits`: only the root `inputs` map changes; `changed == 1`; one commit whose body contains `(lock structure) changed`.
  - `TestRelockAll_NewInputCommits`: the old lock has no `newin` root input; nix writes a lock that adds `newin` locked at rev `3333333333333333333333333333333333333333`; `changed == 1`; one commit whose body contains `newin (new) -> 3333333`.
  - `TestRelockAll_NoFlakeLockIsNoOp`: no `flake.lock`; zero nix calls; output contains `no flake.lock — skipping (pn does not bootstrap a lock)`.
  - `TestRelockAll_RetriesOnce`: first nix call fails with stderr `unable to download`, second succeeds with a rev change → two nix calls, `changed == 1`, one commit, and the output contains `    [foo] relock all flake inputs: attempt 1 failed, retrying once`.
  - `TestRelockAll_CancelledContextNoRetry`: nix rewrites the lock, the call fails, and the context is cancelled before it returns → exactly one nix call, `errors.Is(err, context.Canceled)` (NOT `errRelockRestore`: the write-back needs no context), and `flake.lock` byte-identical to before.
  - `TestRelockAll_RestoresLockOnEveryErrorAfterNix` (table): nix rewrites the lock, then (a) `diffFlakeLocks` cannot parse the new lock, (b) `openGitMutator` fails, or (c) `Add` fails → non-nil error and `flake.lock` byte-identical to before; and (d) `writeLockFile` is replaced by a function that fails → `errors.Is(err, errRelockRestore)`.
  - `TestRelockAll_DisablesFsmonitorForNix`: with `GIT_CONFIG_COUNT=1` already set, the nix call's `Env` has `GIT_CONFIG_KEY_1=core.fsmonitor`, `GIT_CONFIG_VALUE_1=false` and `GIT_CONFIG_COUNT=2`; with it unset, the same keys at index 0 and `GIT_CONFIG_COUNT=1`.
  - `TestRelockAll_RemovesStaleFsmonitorSocket` (table): the runner answers the config read with `true` in one row and with exit 1 and no output (unset) in the other, and answers `rev-parse --absolute-git-dir` with the fixture's real git dir. A file named `fsmonitor--daemon.ipc` placed in that git dir is gone before the nix call in both rows; the `true` row records one `fsmonitor--daemon stop` call before the nix call, and the unset row none. The test NEVER writes fsmonitor config into the real fixture repo: a real `core.fsmonitor=true` lets later real git calls start a daemon, which once hung `go test` until its 600 s timeout (`realgit_test.go:25-28`).
  - `TestRelockAll_FailsAfterRetry`: both calls fail → non-nil error, exactly two nix calls, commit count unchanged, clean tree.
  - `TestRelockAll_NoSpaceAbortsWithoutRetry`: first call fails with stderr `error: writing to file: No space left on device` → `errors.Is(err, errRelockNoSpace)`, exactly one nix call, clean tree.
  - `TestRelockAll_CommitFailureRestoresCleanTree`: set up the failing hook as `TestPropagate_CommitFailsUnderMissingConfigPreCommitHook` (`propagate_test.go:347`) does; non-nil error, commit count unchanged, clean tree.
  - `TestRelockAll_FlakeInSubdir`: `flakeRel = "nix/flake.nix"`; the nix call's `Dir` is `<dir>/nix`; committed file `nix/flake.lock`.
  - `TestRelockAll_GitMutatorFailures` (table; `propEnv` + `fsNixRunner` + `stubGitMutatorOpener(t, map[string]*fakeGitMutator{dir: {...}})`, signature at `gitclient_test.go:254`, fields at `:124-142`): `commitErr` (the commit fails before starting) and `resetHardErr` inside `restoreAfterFailedCommit` → each returns a non-nil error, and the fake records the `ResetHard` call. These rows assert the call, not the file bytes, because `fakeGitMutator.ResetHard` does not touch the file (`:231-234`); `TestRelockAll_CommitFailureRestoresCleanTree` proves the real reset with real git. Script nix failures as `&exec.CommandError{Result: res}` with the stderr text in both `res.Stderr` and the error's `Result`, so the test does not depend on which copy the code reads.
  - `TestRelockAll_NoSpaceOnRetry`: attempt 1 fails with `unable to download`, attempt 2 with `No space left on device` → `errors.Is(err, errRelockNoSpace)`, exactly two nix calls.
  - `TestRelockAll_PrintsSuccessLine`: a move of `sib` and `nixpkgs` prints exactly `  → foo: nix flake update moved 2 input(s): nixpkgs, sib`.
  - `TestDiffFlakeLocks` gains rows: a root input pointing at a node missing from `nodes` → an error naming the input (no panic); a custom top-level `"root"` key naming the root node is honoured; `lastModified` churn on a transitive (non-root) node → empty; three moved inputs → returned in name order (Go map order is random, so sort); a `path:` input whose `locked` object changed but has neither `rev` nor `narHash` → one change whose Old and New are both `(no rev)`.
  - `TestStartRelock`: the recorder sees one `pn.relock` span under the parent with `pn.relock.kind=flake`, `pn.relock.changed=2`, and Error status only when an error is passed; with the clock pinned through the test-only `SetWallNow` (`export_test.go`), the span's end time equals the pinned time, as `TestSpansEndOnTheWallClockAcrossASleep` in `telemetry_test.go` checks for the existing starters.
- [ ] **Step 2:** Run `cd modules/pn && nice -n 10 go test -p 4 -run 'TestDiffFlakeLocks|TestFlakeUpdateCommitMessage|TestRelockAll_|TestStartRelock' ./internal/workspace/ ./internal/telemetry/`; expect FAIL (undefined symbols).
- [ ] **Step 3: Implement.** `diffFlakeLocks` compares each root input's resolved node `locked` object with `lastModified` removed (follows arrays skipped, as `readAliasRevs` does). If no root input differs but the files still differ once every `lastModified` is removed, it returns the single `(lock structure)` change, so a real structural change is never reverted. `relockAllInputs` follows `propagateWorkspaceEdges`, with these differences:
  - a missing lock prints the notice and returns 0;
  - it runs `nix flake update --refresh` (`WrapNix: true`) in the flake dir, retries once, and returns `errRelockNoSpace` without retrying when `res.Stderr` contains `No space left on device`;
  - it ports all of `_ul_disable_fsmonitor` (`update-locks-lib.bash:401-446`), because nix fails with `unsupported type` on an fsmonitor socket, and in `flake` scope nothing else runs that function. The nix call's `Env` (`exec.RunOptions.Env`, `internal/exec/exec.go:34-38`) appends `GIT_CONFIG_KEY_<n>=core.fsmonitor` and `GIT_CONFIG_VALUE_<n>=false` after any `GIT_CONFIG_COUNT` already in the environment (a non-numeric count counts as 0) and sets `GIT_CONFIG_COUNT=<n+1>`. That stops git clients from using a daemon, but not a running daemon or a stale socket, so before nix runs it also stops the daemon (`git -C <dir> fsmonitor--daemon stop`) when `git -C <dir> config --type=bool --get core.fsmonitor` prints `true` (the lib's own probe, `update-locks-lib.bash:428`), and always removes `fsmonitor--daemon.ipc` from the dir `git -C <dir> rev-parse --absolute-git-dir` prints. Both outputs are trimmed of whitespace; when `rev-parse` fails or prints nothing, the removal is skipped (never a path relative to the current directory). All three git calls go through `ws.runner` (the `gitMutator` interface, `gitclient.go:56-65`, has no config or git-dir read, and the relock opens its mutator only after nix runs). They run after the missing-lock check, so a repo without a lock makes no new calls and the exact call counts at `update_test.go:300` and `:572` stand. All three steps are best-effort, as in the lib: an error is ignored and never fails the relock;
  - before the retry it prints `    [<name>] relock all flake inputs: attempt 1 failed, retrying once`; when `ctx.Err() != nil` it does not retry, restores the lock, and returns the context error itself, which callers treat as an interrupt, never as a relock warning;
  - it reads the `before` bytes of `flake.lock` before nix runs, and restores the lock by writing those bytes back with `writeLockFile`. The write-back needs no git client and no context, so it still works after a cancel or when `openGitMutator` failed. Callers start from a clean tree, so `before` matches HEAD;
  - when no input changed, it writes `before` back;
  - otherwise it calls `Add`, then `Commit(flakeUpdateCommitMessage(...))` streamed with `AttachStream`, and calls `restoreAfterFailedCommit` on either commit-failure shape (the lock may be staged by then, so it needs git's reset, not a write-back);
  - once nix has run, EVERY other error return (a lock `diffFlakeLocks` cannot parse, an `openGitMutator` or `Add` failure, a retry that still fails, a cancel) first writes `before` back. A write-back that fails returns `errRelockRestore`, so no error path leaves a dirty lock behind;
  - on success it prints `  → <name>: nix flake update moved <n> input(s): <names>`.

  Callers wrap both relock kinds in `StartRelock`.

- [ ] **Step 4:** Re-run Step 2's command; expect PASS.
- [ ] **Step 5:** Commit `feat(pn): relock every flake input with pn's own commit path`.

### Task 2: `--scope` on `pn workspace update`, both flows, doctor, docs, ADR

**Files:**

- Modify:
  - `modules/pn/internal/workspace/update.go` (UpdateOptions, `updateInPlace`, banners at :299);
  - `modules/pn/internal/workspace/update_worktree.go` (`updateViaWorktree`, `updateRepoViaWorktree`, banner at :324);
  - `modules/pn/internal/workspace/doctor_checks_flakelock.go:103,108`;
  - `modules/pn/internal/workspace/doctor_fix.go:120` (`planAction` text);
  - `modules/pn/internal/cli/workspace.go:587-617` (update) and the `upgrade` command's `Short`;
  - `modules/pn/internal/telemetry/telemetry.go` (package doc span vocabulary, `RecordUpdateScope`);
  - comments that name `SiblingsOnly` or `--siblings-only`: `doctor_checks_flakelock.go:81-84`, `update_worktree.go:125,144,294`, `propagate.go:224`, `doctor_fix_nix_test.go:20,33`;
  - `home/pn/default.nix:12` (its event-level list gains `relock failed → warn`).
- Test:
  - `modules/pn/internal/workspace/update_test.go`;
  - `update_worktree_test.go` (including `assertNoPushNoPropagate` at :282-307 and the two `NeverPushesNorPropagates` tests at `update_test.go:415-459` and `update_worktree_test.go:349-370`);
  - `doctor_checks_flakelock_test.go`;
  - the cli workspace command tests;
  - `telemetry_test.go`.
- Docs:
  - `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`: the Builds table row, the cheat sheet, the `--siblings-only` bullet, and the "`pn workspace update` / `upgrade`" section's description of what update runs.
  - `pn-workspace-rules/skills/pn-workspace-rules/USER_JOURNEYS.md`: J7.
  - `modules/pn/README.md`: it has no update section today (only the event-log text at `:80` and `:452`), so add a short `pn workspace update --scope` section; and fix `:452`, which says the doctor fix pushes.
  - `docs/worktrees.md`: where it says update always runs `update-locks.sh`, and its "`--siblings-only` surgical relock" section (`:118-143`), which now describes `--scope siblings` and its alias.
  - `docs/adr/0009-pn-workspace-update-worktree-isolation.md:238,245`: an "Amended by ADR 0045" note (`UL_LIB_DIR` is now skipped for `siblings` and `flake`).
  - New `docs/adr/0045-pn-workspace-update-scopes.md`, plus a row in `docs/adr/index.md`, whose status cells for 0009 and 0023 also gain `amended by 0045`, in the form existing rows use.
  - `docs/adr/0023-workspace-push-owns-sibling-propagation.md`: an "Amended by ADR 0045" note on item 1 and on the "`--siblings-only` is retained" paragraph.

**Interfaces:**

- Consumes: `relockAllInputs`, `errRelockNoSpace`, `StartRelock` (Task 1); `propagateWorkspaceEdges`.
- Produces:
  - `type UpdateScope string` with `ScopeSiblings = "siblings"`, `ScopeFlake = "flake"`, `ScopeFull = "full"`
  - `func ParseUpdateScope(s string) (UpdateScope, error)` — error text `unknown scope %q (want siblings, flake or full)`
  - `UpdateOptions.Scope UpdateScope` replaces `UpdateOptions.SiblingsOnly` (zero value means `full`); `func (o UpdateOptions) scope() UpdateScope`
  - `func scopeSkipBanner(name string, scope UpdateScope, relocked bool) string` replaces `siblingsOnlySkipBanner`
  - `func RecordUpdateScope(ctx context.Context, scope string)` in `telemetry`: sets `pn.update.scope` on the current (`pn.verb`) span
  - `run_start` events gain `"scope"` and `"root"` (the resolved workspace root, absolute)
  - CLI: `pn workspace update --scope siblings|flake|full` (default `full`); `--siblings-only` is an alias for `--scope siblings`; usage errors exit 2

- [ ] **Step 1: Write the failing tests.** Fixture rule for every test below that expects an all-inputs relock call: write a `flake.lock` into the repo (or worktree) dir and script `git -C <dir> diff --quiet -- flake.lock`, as `update_test.go:500-502` does. Without the lock and the `diff` answer, the relock is a silent no-op (no lock) or fails on an unscripted call (and in `full` scope is downgraded to a warning), so the test passes for the wrong reason. Also script Task 1's two fsmonitor probes, `git -C <dir> config --type=bool --get core.fsmonitor` answered with exit 1 and no output (unset, so no daemon stop follows) and `git -C <dir> rev-parse --absolute-git-dir` answered with a temp dir and a trailing `\n`. The probes are best-effort, so an unscripted one would not fail the run (`FakeRunner` returns an error, `internal/exec/fake.go:58`, which the relock ignores); they are scripted so tests that assert exact call lists stay exact. `TestUpdate_ScopeFlake_RelockedBannerAtUpdateLevel` runs under `fsNixRunner{real: FakeRunner}`, so its probes are answered by `fsNixRunner`'s defaults and never reach the `FakeRunner`. Add a small helper for the shared scripting rather than repeating it in each test.
  - `TestParseUpdateScope`: `siblings`, `flake`, `full` parse; `""` → `ScopeFull`; `Flake` and `bogus` → error containing `want siblings, flake or full`.
  - Rename the existing `SiblingsOnly` tests (`update_test.go:163,465`; `update_worktree_test.go:244,377,774`) to `ScopeSiblings`, using `Scope: ScopeSiblings`.
  - Split `assertNoPushNoPropagate` into `assertNoPush` (no `git -C <dir> push`) and `assertNoSiblingRelock` (no `nix flake update --refresh` call carrying input names). In both `NeverPushesNorPropagates` tests, script the all-inputs `nix flake update --refresh` response, assert both guards, and rename them `..._NeverPushesNorRelocksSiblingsOnly`.
  - `TestScopeSkipBanner` (replaces `TestSiblingsOnlySkipBanner`):

    | Scope    | Relocked | Expected line (each ends in `\n`)                                                                                   |
    | -------- | -------- | ------------------------------------------------------------------------------------------------------------------- |
    | siblings | yes      | `  ⊘ foo: scope siblings — skipping update-locks.sh (workspace inputs relocked, nixpkgs/third-party untouched)`     |
    | siblings | no       | `  ⊘ foo: scope siblings — skipping update-locks.sh (no workspace inputs to relock, nixpkgs/third-party untouched)` |
    | flake    | yes      | `  ⊘ foo: scope flake — skipping update-locks.sh (flake inputs relocked, per-package locks untouched)`              |
    | flake    | no       | `  ⊘ foo: scope flake — skipping update-locks.sh (no flake input moved, per-package locks untouched)`               |

    None of the four contains `⊘ skipping`.

  - `TestUpdate_ScopeFull_NoUpdateLocksScriptStillRelocks`: a repo without the script prints `  ⊘ foo: no update-locks.sh — skipping its per-package steps (flake inputs relocked next)` and makes one all-inputs relock call.
  - `TestUpgrade_RunsTrailingRelock`: `Upgrade` (`upgrade.go:22` builds `UpdateOptions` with no `Scope`) runs `./update-locks.sh` and then one all-inputs relock call, in that order.
  - `TestUpdate_ScopeFlake_InPlace_RelocksAllSkipsUpdateLocks` and `TestUpdateViaWorktree_ScopeFlake_SkipsUpdateLocksAndULLibDir`: no `./update-locks.sh` call, no `nix run <ulLibResolverRef>` call, exactly one `nix flake update --refresh` with no input names, flake banner present.
  - `TestUpdate_ScopeFull_RunsUpdateLocksBeforeRelock` (in-place and worktree): the `./update-locks.sh` call index is lower than the `nix flake update --refresh` call index.
  - `TestUpdate_ScopeFull_RelockFailureWarnsAndIntegrates` (in-place and worktree): `update-locks.sh` succeeds and both relock attempts fail. The repo's `project_result` has level `warn`, `outcome: "ok"` and `relock_failed: true`; the output contains the repo line `  ⚠ foo: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved:` (followed by the error); the in-place variant's per-repo end line starts `  [1/1] foo: ok, relock failed (`; the worktree variant still issues `merge --ff-only` and its summary line is `  ⚠ foo — ok, relock failed; re-run 'pn workspace update --scope flake' to move flake inputs`.
  - `TestUpdate_ScopeFull_InterruptDuringRelockIsNotAWarning` (in-place and worktree): the context is cancelled during the trailing relock → no `project_result` with level `warn`; the repo's `project_result` has level `error` and `failed_step: "relock-flake"`; no `rebase`, `merge` or other git call follows the cancelled relock; and `Update` returns an error containing `update interrupted`.
  - `TestUpdate_ScopeFull_RelockRestoreFailureFailsRepo`: the relock returns `errRelockRestore` → `failed_step: "relock-flake"` even in `full` scope.
  - `TestUpdate_ScopeFlake_RelockFailureFailsRepo` (in-place and worktree): `failed_step: "relock-flake"`. The in-place output contains `update FAILED at step relock-flake` (`update.go:329`); the worktree output contains `failed at relock-flake` (`update_worktree.go:255`) and its summary `failed@relock-flake` (`:453`), and the worktree variant does not issue `merge --ff-only`.
  - `TestUpdate_RelockNoSpaceAbortsRun` (table over scope `full` and `flake`, each in-place and worktree): two repos, the first relock reports no space; the run returns an error containing `aborted`, the second repo is never attempted, and `project_result` and `run_end` both carry `failed_step: "relock-flake"`.
  - `TestUpdate_RunStartRecordsScopeAndRoot` (in-place and worktree, because each flow writes its own `run_start`: `update.go:182`, `update_worktree.go:156`): with an `eventlog` writer on a temp file, the `run_start` event has `"scope":"flake"` and `"root"` equal to the workspace root.
  - `TestUpdate_RecordsScopeAndRelockSpans` (in-place and worktree; `telemetrytest.New()`): the `pn.verb` span carries `pn.update.scope=flake`, and each `pn.repo` has one `pn.relock` child span.
  - `TestUpdate_RelockProgressLines` (in-place and worktree): the output contains `    [foo] relock all flake inputs (nix flake update --refresh): started` and then `    [foo] relock all flake inputs (nix flake update --refresh): finished, ok (` (a failure ends `finished, failed (`).
  - `TestUpdate_ScopeFull_UpdateLocksFailureSkipsRelock` (in-place and worktree, with a `flake.lock` and `git diff --quiet -- flake.lock` scripted so a misplaced relock would really run). The existing failure fixtures (`TestUpdate_ContinuesPastFailureAndAggregates`, `TestUpdateInPlace_AbortsRunOnResourceExit77`, `TestUpdate_PullFailureSkipsLocksAndPush`, `TestUpdateViaWorktree_UpdateLocksFails`, `TestUpdateViaWorktree_AbortsRunOnResourceExit77`) write no lock, so they cannot catch it. Rows: `update-locks.sh` exits 1 → zero `nix flake update` calls, `failed_step` `update-locks`; exits 77 → zero relock calls, `failed_step` `update-locks` in both `project_result` and `run_end`; the pull fails (in-place only) → zero relock calls.
  - `TestUpdate_ScopeFull_Success` (in-place and worktree): level `info`, no `relock_failed` key, `Update` returns nil.
  - `TestUpdate_ScopeFull_RelockFailureWarnsAndIntegrates` also asserts `Update` returns nil and `run_end.status == "ok"`; a third row combines `UL_RESULT transient=1` with the relock failure and asserts both `transient` and `relock_failed` on `project_result`.
  - `TestUpdate_ScopeFlake_RelockedBannerAtUpdateLevel`: with `fsNixRunner{real: FakeRunner}` and `stubGitMutatorOpener`, nix moves an input, and `Update` prints the `relocked=yes` flake banner (unit tests of `scopeSkipBanner` alone never wire it).
  - `TestUpdate_RecordsScopeAndRelockSpans` asserts `pn.relock.kind` per scope: `siblings` → `siblings`, `flake` → `flake`, `full` → `flake`.
  - `TestUpdateViaWorktree_ScopeFull_NoUpdateLocksScriptStillRelocks`: the worktree variant of the no-script test.
  - For `flake` and `full`, assert no `effectiveLock` nix call (it belongs to `siblings` only).
  - Doctor: the flake-lock-fresh fix calls `Update` with `Scope: ScopeSiblings`; its `Manual` text is `pn workspace update --scope siblings`; `planAction` reads `pn workspace update --scope siblings (relock → commit, local only)`.
  - CLI:
    - `--scope flake` reaches `UpdateOptions.Scope`. `internal/cli/workspace.go:610` calls the concrete `w.Update`, so there is no options seam: stub `openWorkspace` with a FakeRunner workspace as `workspace_update_events_test.go:16-55` does, run `update --in-place --scope flake`, and assert there is no `./update-locks.sh` call and the output contains `scope flake — skipping update-locks.sh`. Use the same stub for the two alias rows next;
    - `--siblings-only` alone and `--siblings-only --scope siblings` both give `ScopeSiblings` (assert the siblings banner);
    - `--siblings-only` with any explicit `--scope` other than `siblings` exits 2 with `--siblings-only is an alias for --scope siblings and conflicts with --scope <s>; pass one`; test `full` and `flake`;
    - `--scope bogus`, a trailing `--scope` with no value, and `--scope=` with an explicit empty value all exit 2 (`ParseUpdateScope("")` returns `ScopeFull` for the unset flag, so reject an empty value whenever `Changed("scope")` is true);
    - on any of these usage errors no pre-update hook runs.

- [ ] **Step 2:** Run `cd modules/pn && nice -n 10 go test -p 4 ./internal/workspace/ ./internal/cli/ ./internal/telemetry/`; expect FAIL.
- [ ] **Step 3: Implement.** Per repo, both flows switch on `opts.scope()`:

  | Scope      | Runs                                                                                 | On failure                                                                                                                                                                                             |
  | ---------- | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
  | `siblings` | `propagateWorkspaceEdges`                                                            | repo fails at `relock-siblings`                                                                                                                                                                        |
  | `flake`    | `relockAllInputs`                                                                    | repo fails at `relock-flake`                                                                                                                                                                           |
  | `full`     | `./update-locks.sh` as today, then (unless that failed or aborted) `relockAllInputs` | relock failure is a warning (D9), except that `errRelockNoSpace` aborts, `errRelockRestore` fails the repo at `relock-flake`, and a context interrupt ends the repo and the run (see Interrupts below) |

  `errRelockNoSpace` takes the existing abort path (`statusAborted` in the worktree flow, `aborted` in the in-place flow) and records `failed_step` `relock-flake` in `project_result`, in `run_end`, and in the returned error. The in-place abort path hardcodes `update-locks` today (`update.go:307,310,350-351`); carry the failing step's name instead, so an `update-locks.sh` exit 77 still reads `update-locks`.

  The one retry and the no-space abort of D9 apply to `relockAllInputs` only (`flake` and `full`). `siblings` keeps `propagateWorkspaceEdges` unchanged: no retry, no no-space abort, and its first failure fails the repo at `relock-siblings`. ADR 0045 states this.

  Interrupts. The worktree flow checks `ctx.Err()` only at the top of its repo loop (`update_worktree.go:163`); the in-place flow also checks before the pull and before `update-locks.sh` (`update.go:241,258`), but not after it. So a cancel during the relock would otherwise carry on into the repo's later steps. A context error from `relockAllInputs`, in any scope, ends that repo at once: `failed_step` `relock-flake`, level `error`, and no rebase, merge or other git call after it (in the worktree flow, `updateRepoViaWorktree` returns through its existing `fail` helper). Both flows then also check `ctx.Err()` right AFTER each repo and return `update interrupted: <cause>`.

  Remaining wiring:
  - In the worktree flow, resolve `UL_LIB_DIR` (`update_worktree.go:129`) only for `full`; the in-place flow never resolves it. In both flows derive `edgeLock` only for `siblings`.
  - The worktree flow emits `project_result` from `repoOutcome` in its outer loop (`update_worktree.go:176-193`), so add a `relockFailed bool` field to `repoOutcome` and emit `relock_failed` and level `warn` from it.
  - A `full`-scope relock warning is visible on stdout, not only in `events.jsonl`: both flows print `  ⚠ <name>: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved: <err>` when it happens, and the worktree summary prints `  ⚠ <name> — ok, relock failed; re-run 'pn workspace update --scope flake' to move flake inputs` instead of `✓ <name> — ok` (`update_worktree.go:448`). The in-place flow has no summary; its per-repo end line (`finishRepo`, `update.go:208-210`) reads `  [<i>/<n>] <name>: ok, relock failed (<secs>)`. `⚠` is already the worktree flow's warning glyph. The per-repo `⚠ <name>: nix flake update (all inputs) failed after one retry` line is the one BOTH flows print, so it is the line pnwf (Task 3, which always runs `--in-place`, `pnwf.sh:1808`) matches.
  - Progress lines, in BOTH flows (the worktree flow prints none today, `update_worktree.go:301-325`), in the shape of the existing `update-locks.sh` line at `update.go:288`: `    [<name>] relock all flake inputs (nix flake update --refresh): started` and `    [<name>] relock all flake inputs (nix flake update --refresh): finished, ok|failed (<secs>)`. The label differs from `update-locks.sh`'s own `nix-flake-update` step so a `full` run does not show two identical "nix flake update" lines.
  - Call `RecordUpdateScope` once at the top of `Update`.
  - In cobra, validate `--scope` and the alias conflict in `RunE` before `openWorkspace`, the event log and `runWithHooks`; use `cmd.Flags().Changed("scope")` to detect the conflict.
  - Return `ExitCodeError{Code: 2, ...}`, as `internal/cli/workspace.go:402` does.
  - Give the command a `SetFlagErrorFunc` that wraps flag errors in `ExitCodeError{Code: 2}`.
  - Help text. The `--scope` flag usage is `siblings, flake or full (see above)`; `--siblings-only` becomes `alias for --scope siblings`; the `upgrade` `Short` becomes `Update (--scope full) + apply each workspace repo`. The update `Long` gains, verbatim:

    ```text
    --scope chooses how much is relocked:
      siblings  workspace-sibling flake inputs only; skips update-locks.sh
      flake     every flake input (siblings, nixpkgs, third-party); skips update-locks.sh,
                so Go/uv/npm/nvfetcher/Homebrew/action-pin locks are untouched
      full      ./update-locks.sh, then every flake input (default)
    The CLI defaults to full; /pn-workspace-update and 'pnwf update-relock' default to flake.
    Afterwards, 'pn workspace explain' reports where the time went.
    ```

- [ ] **Step 4:** Re-run Step 2's command, then `cd modules/pn && nice -n 10 go test -p 4 ./...`; expect PASS.
- [ ] **Step 5: Docs + ADR.** Write ADR 0045 (status Accepted):
  - context: D1, D7's superset gap, the 12-hour stamp;
  - decision: the three scopes, D6's defaults, D7's order, D9's failure handling;
  - alternatives: D8, and "relock first" (rejected per D7);
  - consequences: `--siblings-only` becomes a permanent alias (not deprecated: doctor's older docs, the tool approver and existing habits use it), doctor uses `--scope siblings`, the Homebrew lock and the other per-package locks refresh only in `full` scope (so nix-personal ADR 0060's "`pn workspace update` MUST refresh the lock" holds for the CLI default but not for the skill's `flake` default), `flake` needs no `UL_LIB_DIR` so it runs headless, `upgrade` gains the trailing relock, and there is no ADR 0020 transient classification for pn's relock (one retry instead, for the all-inputs relock only; `siblings` keeps today's single attempt);
  - an amendment to ADR 0023 item 1: plain update's relock is no longer only `update-locks.sh`'s.

  Update the docs listed under Files. The skill's cheat sheet gains `pn workspace update --scope flake   Relock every flake input (siblings + nixpkgs + third-party), skip update-locks.sh; local-only`. Run `nix fmt` to convergence.

- [ ] **Step 6:** Confirm `rg -n 'SiblingsOnly|siblingsOnlySkipBanner' modules/pn` prints nothing, and that every `rg -n -- '--siblings-only'` match is the alias flag itself, its tests, or ADR history. Then `git add -A`, `pg-hooks fix`, commit `feat(pn): --scope siblings|flake|full for workspace update`.

### Task 3: Skills default to `flake` (pnwf and `/pn-workspace-update`)

**Files:**

- Modify:
  - `modules/pnwf/pnwf/pnwf.sh`: `cmd_update_relock`, its usage text, and line 1808.
  - `modules/pnwf/pnwf/pnwf.md`.
  - `modules/pnwf/pnwf/completions/_pnwf:35` and `modules/pnwf/pnwf/completions/pnwf.bash:24`: split `update-relock` out of the shared case arm.
  - `pn-workspace-rules/commands/pn-workspace-update.md`: frontmatter, the announce text, step 2a, `:98-100`, the "Known limitation" note about ADR 0020 transient steps, and the final report.
  - `pn-workspace-rules/agents/pnwf-update-runner.md:67-68` and `pn-workspace-rules/agents/pnwf-runner.md:66-68` (the same stale "`nix flake update` plus each repo's `update-locks.sh`" text).
  - `flake.nix`: the `pnwf-runner-turn-boundary-rule` check (`:1291`) gains `argument-hint: "[siblings|flake|full]"` as a required string in `pn-workspace-update.md`, so D2's skill default has an automated guard.
  - `modules/pn/internal/cli/root.go`: pn reads `PN_TRACE_HINT` once at startup and unsets it, so a nested `pn` (for example nix-ziprecruiter's `build_out_path` running `pn workspace nix` inside `update-locks.sh`) does not print its own `trace:` line into the outer run's output.
- Test:
  - `modules/pnwf/pnwf/tests/test-pnwf.bats`: the `update-relock` section near line 2385 (reuse its mock `pn`), and the backstop test near line 2567.
  - `modules/pn/internal/cli/telemetry_test.go`, following its existing hint test (`:207`): `TestExecuteWith_ClearsTraceHintForChildren`, with `t.Setenv("PN_TRACE_HINT", "1")`: after `executeWith` returns, `os.Getenv("PN_TRACE_HINT")` is empty and the `trace:` hint was still written to stderr.

**Interfaces:**

- Consumes: `pn workspace update --scope <s>` (Task 2); pn's `trace: <id>` stderr hint when `PN_TRACE_HINT=1` (`internal/cli/root.go:143`).
- Produces: `pnwf update-relock [--set] [--scope siblings|flake|full]`, default `flake`. It runs pn with `PN_TRACE_HINT=1` and prints one stdout line `update-relock: trace <id>` whenever pn printed a trace id, on success and on failure, so a failed run can still be explained. `/pn-workspace-update [siblings|flake|full]`, default `flake`.

- [ ] **Step 1: Write the failing bats tests:**
  - `update-relock passes --scope flake to pn by default`;
  - `update-relock --scope full passes --scope full`;
  - `update-relock --scope bogus exits 2 and never calls pn`;
  - `update-relock --scope with no value exits 2`;
  - `update-relock prints the trace id pn reported` (the mock prints `trace: 0123456789abcdef0123456789abcdef` to stderr; stdout contains `update-relock: trace 0123456789abcdef0123456789abcdef`);
  - `update-relock prints the trace id even when pn fails` (same mock line, mock exits 1; update-relock exits non-zero and stdout still contains the trace line);
  - `update-relock prints no trace line when pn printed none` (telemetry off): stdout has no `update-relock: trace` line;
  - `update-relock takes only a whole trace line, the last one`: the mock prints `building... trace: ffffffffffffffffffffffffffffffff` (not a whole line) mid-output and `trace: 0123456789abcdef0123456789abcdef` as a line at the end → stdout has `update-relock: trace 0123456789abcdef0123456789abcdef` and no `ffff` id;
  - `update-relock relays a full-scope relock warning`: the mock prints the in-place lines pn really prints, `  ⚠ foo: nix flake update (all inputs) failed after one retry — update-locks.sh commits kept, flake inputs not moved: boom` and `  [1/1] foo: ok, relock failed (12.0s)`, and exits 0 → update-relock exits 0 and stdout contains exactly one `update-relock: warning: foo: flake relock failed (update-locks.sh commits kept)`;
  - `update-relock backstop ignores the scope banners`: the mock prints these five lines verbatim (each Task 2 banner, with repo `foo`) and update-relock exits 0:

    ```text
      ⊘ foo: scope siblings — skipping update-locks.sh (workspace inputs relocked, nixpkgs/third-party untouched)
      ⊘ foo: scope siblings — skipping update-locks.sh (no workspace inputs to relock, nixpkgs/third-party untouched)
      ⊘ foo: scope flake — skipping update-locks.sh (flake inputs relocked, per-package locks untouched)
      ⊘ foo: scope flake — skipping update-locks.sh (no flake input moved, per-package locks untouched)
      ⊘ foo: no update-locks.sh — skipping its per-package steps (flake inputs relocked next)
    ```

  - `update-relock --scope full --set` run from `$CANONICAL_DIR` → status 13 and no `workspace update` call (the `--set` guard survives the new argument parsing);
  - `update-relock --set --scope siblings` inside a set → the mock's logged argv contains `--scope siblings`.

  Extend the mock `pn` to log its argv and env `PN_TRACE_HINT` if it does not already, and to print its `trace:` line ONLY when `PN_TRACE_HINT=1`, as `root.go:143` does, so a pnwf that forgets to set the variable fails the trace tests.

- [ ] **Step 2:** Run `nix build .#checks.aarch64-darwin.test-pnwf -L` through `bgrun` + `bgwait --timeout 540` (Bash timeout 600000 ms), and `cd modules/pn && nice -n 10 go test -p 4 ./internal/cli/ -run TestExecuteWith_ClearsTraceHintForChildren`; expect both to FAIL. Step 4 also builds `.#checks.aarch64-darwin.pnwf-runner-turn-boundary-rule` and `.#checks.aarch64-darwin.land-gate-rule-docs` the same way; both must pass once the command file and the check change.
- [ ] **Step 3: Implement.**
  - pnwf: parse `--scope <v>` with a `case`; any other value, or a missing value, calls `die_usage`. Run `env -u PN_WORKSPACE_ROOT PN_TRACE_HINT=1 pn workspace update --in-place --scope "$scope"`. Right after pn returns, and before the `rc` check at `pnwf.sh:1812-1814` (whose `die` would otherwise skip it), take the last line of the captured output that matches `^trace: [0-9a-f]{32}$` exactly and print `update-relock: trace <id>`; print nothing when no line matches. Then, for each line matching `⚠ ([^:]+): nix flake update \(all inputs\) failed after one retry` (the per-repo line both pn flows print; the worktree-only summary line never reaches pnwf, which always runs `--in-place`), print `update-relock: warning: <repo>: flake relock failed (update-locks.sh commits kept)`; these warnings never change the exit status.
  - In `root.go`, `executeWith` (`:105`) calls `telemetrycfg.WriteHint(stderr, state, res.Verbose, os.Getenv)` at `:146`. Read `PN_TRACE_HINT` once at the start of `executeWith`, pass `WriteHint` a getter that returns that saved value, and `os.Unsetenv("PN_TRACE_HINT")` before any subcommand runs.
  - Usage text, `pnwf.md` and both completion files gain the flag.
  - The command file:
    - frontmatter `argument-hint: "[siblings|flake|full]"`;
    - an empty `$ARGUMENTS` means `flake`; exactly `siblings`, `flake` or `full` selects that scope; anything else stops before forking and replies `Unknown scope '<arg>'. Use /pn-workspace-update [siblings|flake|full] (default flake). Nothing was forked.`;
    - step 2a runs `pnwf update-relock --set`, adding `--scope <scope>` AFTER `--set` (`pnwf update-relock --set --scope <scope>`) only for a non-default scope. The trace id comes from the line matching `^update-relock: trace [0-9a-f]{32}$` in step 2a's background output file; with no such line, the run has no trace id (telemetry off);
    - step 2a's halt path quotes the trace line and the command `pn workspace explain <id>` when there is one. A `relock-flake` failure with no other residue adds: one retry was already made, and re-running `pnwf update-relock --set` is safe;
    - the announce line and the `description` name the scope, for `flake`: `…relock every repo's flake inputs (scope flake)… Per-package locks (Go modules, uv, npm, nvfetcher, Homebrew, action pins) are not refreshed; /pn-workspace-update full does that.`;
    - the ADR 0020 "Known limitation" note now applies to `full` only.
    - The command file has no final-report section today; add `Step 6 REPORT` after step 5 (and a node for it in the command's mermaid diagram). It lists every `update-relock: warning:` line from step 2a. Task 7 adds the `explain` part.
  - Fix the stale "update-relock runs a nix flake update plus each repo's update-locks.sh" text in both files.
  - Keep the strings the `pnwf-runner-turn-boundary-rule` check (`flake.nix:1291-1392`) greps for. It reads five files (`:1293-1297`); this task edits three of them, `pn-workspace-update.md`, `pnwf-update-runner.md` and `pnwf-runner.md`. The command file keeps the literal `pnwf update-relock --set` (hence `--set` before `--scope`), at least two `run_in_background: true` lines, `incomplete-update`, `pn workspace build`, and ``MUST NOT offer the runner `run_in_background` ``, and has no `STAGE = validate`. Both runner files keep `Constraint: One Turn, Foreground Only`, `**R1**` to `**R4**` and `600000` (`:1299-1308`). The update runner also keeps `You MUST NOT run the relock` and R5's ``You MUST NOT run `pn workspace build` ``; `pnwf-runner.md` also keeps `incomplete-sync` (`:1348`). In each runner file, every mention of `run_in_background` must sit inside a MUST NOT window (`:1311-1325`). These lists are a guide; read the check itself before editing any of the five files.
- [ ] **Step 4:** Re-run Step 2; expect PASS.
- [ ] **Step 5:** Commit `feat(pnwf): update-relock --scope, default flake for /pn-workspace-update`.

### Task 4: `UL_STEP` result lines from `update-locks-lib.bash`

**Files:**

- Modify:
  - `lib/scripts/update-locks-lib.bash`: the contract header, `ul_run_step` (lines 733-866), and the `ul_brew_lock_update_step` skip path (line 1019).
  - `docs/adr/0020-update-locks-failure-classification.md`: append an "Amendment 2026-11-18: per-step result lines" section.
- Test: `lib/tests/test-update-locks-lib.bats` (reuse its `ul_run_step` fixture setup)

**Interfaces:**

- Produces one stdout line per step, emitted once the step's outcome is known and after its commit:
  `UL_STEP name=<step> outcome=<ok|skipped|deferred|transient|failed|aborted> start_ms=<epoch milliseconds> ms=<integer milliseconds>`
  - `name` matches `^[A-Za-z0-9._-]+$`.
  - `start_ms` is taken before the stamp check. It comes from `EPOCHREALTIME` when that is set (bash 5); otherwise it is `date +%s` × 1000, because host bash 3.2, on the dev-shell fallback path, has no `EPOCHREALTIME`.
  - `ms` covers the command plus its commit.
  - `failed` covers four cases: the command failed hard; the command succeeded but its commit failed; the deferred stamp-only commit failed; or the dirty-tree FATAL path fired, in which case the line is printed before `exit 1`.
  - `aborted` is printed before the resource-abort `exit`.

- [ ] **Step 1: Write the failing bats tests**, one per case:
  - `ul_run_step prints UL_STEP outcome=ok for a succeeding step`;
  - `... outcome=skipped when the stamp is fresh`;
  - `... outcome=deferred on exit 75`;
  - `... outcome=transient on a transient stderr signature`;
  - `... outcome=failed on a hard failure`;
  - `... outcome=failed when the step commit is rejected by a hook`;
  - `... outcome=failed when the deferred stamp-only commit fails`;
  - `... outcome=failed before the dirty-tree FATAL exit`;
  - `... outcome=aborted before exiting on ENOSPC`;
  - `ul_brew_lock_update_step prints UL_STEP outcome=skipped when it skips`;
  - `ul_finalize still prints UL_RESULT as the last UL_RESULT line after UL_STEP lines`;
  - `UL_STEP falls back to whole seconds without EPOCHREALTIME` (run `unset EPOCHREALTIME` inside a subshell, because once unset the variable loses its special meaning for the rest of that shell; `start_ms` ends in `000`).
  - `_ul_epochrealtime_to_ms converts both decimal separators without octal errors`: `1700000000.089123` and `1700000000,089123` (a comma-decimal `LC_NUMERIC`) both give exactly `1700000000089`; `1700000000.000999` gives `1700000000000`. This is a pure-helper test with fixed inputs, because the bug it guards (a zero-led fraction read as octal) would otherwise show up only in a few percent of runs.
  - `ul_brew_lock_update_step skip path survives a lib copy without the new helpers`: copy only `_ul_brew_lock_skip_reason`, `ul_brew_lock_update` and `ul_brew_lock_update_step` out of the lib, as nix-ziprecruiter's stand-in does (`nix-ziprecruiter/tests/update-locks/support/fake-update-locks-lib.bash:24`), and run the skip path under `set -euo pipefail` → exit 0 and the skip message printed.

  Each outcome test asserts the exact line, not a loose regex (a regex on `[0-9]+` would also accept microseconds, whole seconds, or an end time taken before the commit). Pin the clock: `unset EPOCHREALTIME; EPOCHREALTIME=1700000000.250000` turns it into a plain variable, which `_ul_now_ms` re-reads on every call, so the line must be exactly `UL_STEP name=<step> outcome=<o> start_ms=1700000000250 ms=0`. Three more tests:
  - `UL_STEP reads a comma-decimal EPOCHREALTIME`: `EPOCHREALTIME=1700000000,250000` gives the same `start_ms=1700000000250`;
  - `UL_STEP ms covers the step commit`: wrap `_ul_commit_updated` (`update-locks-lib.bash:925`) so it sets `EPOCHREALTIME=1700000002.750000` before calling the original → `ms=2500` (2,750 ms − 250 ms = 2,500 ms);
  - the whole-second fallback (subshell, `EPOCHREALTIME` unset) is asserted on the `ok`, `aborted` and dirty-tree FATAL paths, each line's `start_ms` ending in `000`.

- [ ] **Step 2:** Run `nix build .#checks.aarch64-darwin.test-update-locks-lib -L` through `bgrun` + `bgwait --timeout 540`; expect FAIL.
- [ ] **Step 3: Implement** `_ul_now_ms` and `_ul_emit_step_result <name> <outcome> <start_ms>`, and call the second on every exit path of `ul_run_step` and on the brew skip path. `_ul_now_ms` tests `EPOCHREALTIME` on every call and never caches the choice when the lib is sourced, so the fallback test can unset it after sourcing. It converts through a pure helper, `_ul_epochrealtime_to_ms <value>`, which strips every non-digit and divides: `t=${1//[!0-9]/}; echo $((10#$t / 1000))`. Never split on `.`: bash formats `EPOCHREALTIME` with the locale's decimal separator, and a zero-led fraction such as `089123` is read as octal (the lib already guards the same trap at `update-locks-lib.bash:436`). The brew skip path calls the new helpers only behind `declare -F _ul_emit_step_result >/dev/null &&`, because nix-ziprecruiter's stand-in copies three brew functions out of the locked real lib and would otherwise exit 127 under `set -euo pipefail` at its next relock. Add the contract anchor `ANCHOR: ul_run_step-step-result-line`, describing the line, its outcomes, its precision, and that older libs print none.
- [ ] **Step 4:** Re-run Step 2; expect PASS.
- [ ] **Step 5:** Write the ADR 0020 amendment:
  - why: a trace cannot name the time inside `update-locks.sh`;
  - contract: the line above;
  - compatibility: a run whose resolved lib predates `UL_STEP` prints none, and pn treats that as "no step data". In a canonical run, `determine-ul-lib-dir` prefers the workspace's own `nix-repo-base/lib/scripts`, so every repo gets steps once this lands.

  In `docs/adr/index.md`, the 0020 row's status cell becomes `Accepted (amended 2026-11-18: per-step result lines)`. Then `nix fmt`, `pg-hooks fix`, and commit `feat(update-locks): per-step UL_STEP result lines`.

### Task 5: pn records `UL_STEP` as spans and events

**Files:**

- Create: `modules/pn/internal/workspace/ulresult.go` (move `ulResultPrefix` and `parseULTransient` here from `update.go`, add the step parser)
- Modify: `modules/pn/internal/workspace/update.go` and `update_worktree.go` (after `./update-locks.sh`), `modules/pn/internal/telemetry/telemetry.go`
- Test: `modules/pn/internal/workspace/ulresult_test.go`, `update_test.go`, `update_worktree_test.go`, `modules/pn/internal/telemetry/telemetry_test.go`

**Interfaces:**

- Consumes: the `UL_STEP` line (Task 4), exactly `UL_STEP name=<step> outcome=<outcome> start_ms=<int> ms=<int>`, where `<step>` matches `^[A-Za-z0-9._-]+$`, `<outcome>` is one of `ok`, `skipped`, `deferred`, `transient`, `failed`, `aborted`, and both integers are non-negative decimal milliseconds (`start_ms` is epoch milliseconds).
- Produces:
  - `type ULStep struct{ Name, Outcome string; StartMS, MS int64 }` in `telemetry`
  - `func parseULSteps(stdout []byte) []telemetry.ULStep` in `workspace`. It finds `UL_STEP ` anywhere in a line, so a step's unterminated partial line cannot hide it, and then requires the four fields exactly.
  - `func RecordULSteps(ctx context.Context, steps []ULStep)` in `telemetry`: one `pn.ul.step` span per step, child of the current span, started `trace.WithTimestamp(StartMS)` and ended at `StartMS+MS`, with attributes `pn.ul.step.name` and `pn.ul.step.outcome`, and status Error for `failed` and `aborted`.
  - `project_result` events gain `"steps": [{"name","outcome","ms"}]` when non-empty, and `"steps_ms": <sum>`.

- [ ] **Step 1: Write the failing tests**
  - `TestParseULSteps` (table):
    - `no lines` → empty;
    - two valid lines → two steps in order;
    - lines interleaved with `UL_RESULT transient=0` and ordinary output → only the steps;
    - a valid line preceded on the same line by partial output (`downloading...UL_STEP name=a ...`) → parsed;
    - unknown outcome, a missing field, non-numeric `ms`, `ms=-1`, an empty `name=`, or a name with a space → that line ignored;
    - a valid line ending in `\r` → parsed, with the `\r` stripped;
    - two lines with the same name → both kept, in order;
    - a valid line with a fifth unknown field (`extra=1`) → ignored, because the fields must match exactly.
  - `TestParseULTransient` still passes unchanged after the move.
  - `TestRecordULSteps`: the recorder sees `pn.ul.step` spans whose start and end equal `StartMS` and `StartMS+MS`, whose parent is the span in `ctx`, whose attributes are exactly the keys `pn.ul.step.name` and `pn.ul.step.outcome` with the input values, and whose status is Error only for `failed` and `aborted`. `RecordULSteps(ctx, nil)` records zero spans.
  - `TestUpdate_InPlace_RecordsULStepsFromUpdateLocksStdout` and its worktree variant: the fake `./update-locks.sh` Stdout carries two `UL_STEP` lines. The recorder sees two `pn.ul.step` spans under the repo's `pn.repo`, and `project_result` has two `steps` with `steps_ms` equal to their sum. A third variant returns the lines with a `CommandError`, and a fourth with exit 77; both still record the spans, and both assert `steps` and `steps_ms` on the failed or aborted `project_result`. The in-place flow emits that event from three separate sites (`update.go:306,325,340`), and each must carry the fields.
- [ ] **Step 2:** Run `cd modules/pn && nice -n 10 go test -p 4 ./internal/workspace/ ./internal/telemetry/`; expect FAIL.
- [ ] **Step 3: Implement.** Call `RecordULSteps(<repo context>, parseULSteps(res.Stdout))` next to the existing `parseULTransient(res.Stdout)` in both flows, and add the event fields. The in-place flow's repo context is the variable `ctx`, shadowed by `telemetry.StartRepo` at `update.go:197`; the worktree flow uses its own repo context. In the worktree flow, add a `steps` field to `repoOutcome` so the outer loop's `project_result` (`update_worktree.go:176-193`) carries `steps` and `steps_ms`.
- [ ] **Step 4:** Re-run Step 2; expect PASS.
- [ ] **Step 5: Docs.**
  - Add rows for `pn.update.scope`, `pn.relock` (kind, changed) and `pn.ul.step` (name, outcome) to both span-attribute tables: `modules/pn/README.md` "Rebuild, hook and exec span attributes", and `pn-workspace-rules/skills/pg-nix-log-wrapped/SKILL.md:268-297`.
  - Add the same spans to the `telemetry.go` package doc.
  - Amend `docs/adr/0028-pn-nix-telemetry.md` with the new span shapes, and mark the 0028 row's status cell in `docs/adr/index.md` `amended 2026-11-18` in the form existing rows use. Note there that `pn.relock.changed` is 0 or 1 for `siblings` (`propagateWorkspaceEdges` returns a bool) and a count for `flake`.

  Then `nix fmt` and commit `feat(pn): record update-locks steps as pn.ul.step spans`.

### Task 6: Trace analysis package

**Files:**

- Create: `modules/pn/internal/traceexplain/parse.go`, `analyze.go`, `render.go`, `testdata/update-two-repos.json`, `testdata/incomplete.json`, `testdata/flake-scope.json`, `testdata/sums-and-clamps.json`
- Test: `modules/pn/internal/traceexplain/parse_test.go`, `analyze_test.go`, `render_test.go`

**Interfaces:**

- Consumes: the JSON of Tempo's `GET /api/v2/traces/<id>`, in this shape (verified 2026-11-18):

  ```text
  .trace.resourceSpans[]
    .scopeSpans[].spans[]
      traceId, spanId, parentSpanId   base64 strings; parentSpanId absent on a root span
      name
      startTimeUnixNano, endTimeUnixNano   decimal strings
      status.code
      attributes[]: {key, value: {stringValue | intValue | boolValue | doubleValue | arrayValue}}
  ```

  `intValue` is a string. The pn and pg-nix-log-wrapped services arrive as separate `resourceSpans` (187 in the 2026-11-18 trace).

  The span keys it reads:
  - `pn.repo` on `pn.repo`;
  - `process.executable.name` on `pn.exec`: `./update-locks.sh` marks the script, `git` marks git;
  - `nix.subcommand` on `nix.invocation`, where `develop` marks the wrapper;
  - `nix.plan.build` and `nix.plan.fetch` (integers the wrapper sets when it finishes, `modules/pg-nix-log-wrapped/internal/app/app.go:566-567`) on `nix.invocation`, feeding `NixCall.plan_builds` and `plan_fetches` (0 when absent);
  - the span names `nix.substitute` (summed into `substitute_seconds_summed`) and `nix.build.wait` (summed into `lock_wait_seconds_summed`), from `modules/pg-nix-log-wrapped/internal/recorder/recorder.go:91-93`;
  - `nix.drv.name` on `nix.build`;
  - `pn.update.scope` and `pn.verb` on `pn.verb`;
  - `pn.hook.when` and `pn.hook.name` on `pn.hook`;
  - `pn.ul.step.name` and `pn.ul.step.outcome` on `pn.ul.step`.

- Produces:
  - `type Span struct{ SpanID, ParentID, Name string; Start, End time.Time; Attrs map[string]string; Error bool }` (`arrayValue` joined with `,`)
  - `func ParseTempoV2(data []byte) ([]Span, error)`
  - `var ErrIncompleteTrace = errors.New("trace has no pn.verb span")`
  - `func Analyze(traceID string, spans []Span, top int, partial bool) (Report, error)`. When `pn.verb` is missing it returns `ErrIncompleteTrace`, unless `partial` is set; then it analyzes the `pn.repo` spans and sets `incomplete: true`.
  - `type Report struct`, with JSON fields:
    - `trace_id`, `verb`, `scope`, `start`, `incomplete`;
    - `wall_seconds`, `outside_repos_seconds`, `hooks` (`[]Hook{when,name,seconds}`);
    - `repos` (`[]RepoReport`), `top_builds` (`[]Build`);
    - `lock_wait_seconds_summed`, `substitute_seconds_summed`, `anomalies` (`[]string`).
  - `type RepoReport struct`, with JSON fields:
    - `name`, `wall_seconds`, `git_seconds`, `relock_seconds`;
    - `update_locks` (`*ULReport`, absent when the script did not run);
    - `nix` (`[]NixCall{subcommand,seconds,plan_builds,plan_fetches}`);
    - `unattributed_seconds`.
  - `type ULReport struct`: `seconds`, `setup_seconds` (script start → first step start), `steps` (`[]Step{name,outcome,seconds}`, in start order), `between_steps_seconds`, `finalize_seconds` (last step end → script end), `steps_missing` (bool).
  - `type Build struct{ Drv string `json:"drv"`; Repo string `json:"repo"`; Step string `json:"step"`; Seconds float64 `json:"seconds"` }`. `Step` is the `pn.ul.step` whose interval contains the build's start, and is empty when none does.
  - `func RenderText(w io.Writer, r Report, header string) error`. It prints, in order: the `INCOMPLETE TRACE` line when `r.Incomplete`; `header` when non-empty (the CLI passes its `Explaining …` line; tests pass `""`); then the report. So the `INCOMPLETE TRACE` line appears exactly once and always first.
  - Seconds stay unrounded `float64` inside `Report`. JSON rounds every field whose JSON name contains `seconds` (including `lock_wait_seconds_summed` and `substitute_seconds_summed`, in `Report`, `RepoReport`, `ULReport`, `Step`, `Build`, `Hook` and `NixCall`) to the nearest 0.1 s when marshalling; text uses the duration format given under `TestRenderText`.

- [ ] **Step 1: Build the fixtures by hand.** Use small round numbers, base64 ids, and explicit `parentSpanId` on every non-root span; don't copy from a live trace. Times are seconds from 0.

  `update-two-repos.json`, as a span tree:

  ```text
  pn.verb  0-1050   pn.verb=workspace update, pn.update.scope=full
  ├── repo a  0-600
  │   ├── pn.exec git  0-10
  │   ├── pn.exec ./update-locks.sh  10-590
  │   │   └── nix.invocation develop  12-588   nix.plan.build=2, nix.plan.fetch=1
  │   │       ├── nix.build fast-drv  40-70
  │   │       ├── nix.substitute some-path  75-95
  │   │       ├── nix.build slow-drv  200-500
  │   │       └── nix.build.wait  300-340
  │   ├── pn.ul.step nix-flake-update  20-120  ok
  │   ├── pn.ul.step update-deps-x  130-530  ok
  │   └── pn.relock  590-600  kind=flake
  ├── repo b  600-1000
  │   ├── pn.exec git  600-605
  │   └── pn.exec ./update-locks.sh  605-900
  │       └── nix.invocation develop  607-950   (ends 50 s after its parent)
  │       (no pn.ul.step spans)
  └── pn.hook post update  1000-1040
  ```

  Add exactly one orphan span: `pn.exec git` 0-1 s with `parentSpanId` `AQIDBAUGBwg=` (bytes 01 to 08), a non-zero id no span in the file has. Do not use `AAAAAAAAAAA=`: it decodes to the all-zero span id, which OTLP treats as "no parent", so it would read as a root, not an orphan. The orphan has no `pn.repo` ancestor, and orphans are left out of every figure. `incomplete.json` holds repo `a`'s spans with no `pn.verb`.

  Two more fixtures keep `update-two-repos.json`'s numbers unchanged. `flake-scope.json` is a repo with no `update-locks.sh` (the skills' default scope) and a relock whose children include git:

  ```text
  pn.verb  0-100   pn.verb=workspace update, pn.update.scope=flake
  └── repo c  0-100
      ├── pn.exec git  0-10            (overlaps the relock, so union and sum differ)
      └── pn.relock  5-95  kind=flake
          ├── pn.exec nix  6-90
          │   └── nix.invocation flake update  7-89   (nix.subcommand is "flake update", as pg-nix-log-wrapped records it: allowlist.go:54)
          └── pn.exec git  90-94
  ```

  `sums-and-clamps.json` has two substitutes and two waits (so "summed" differs from "first"), a build outside any step, and a clamp that applies to a clamped parent:

  ```text
  pn.verb  0-200   pn.verb=workspace update, pn.update.scope=full
  ├── repo d  0-100
  │   └── pn.exec ./update-locks.sh  0-100
  │       └── nix.invocation develop  1-99
  │           ├── nix.substitute p1  10-20
  │           ├── nix.substitute p2  30-45
  │           ├── nix.build.wait  50-60
  │           ├── nix.build.wait  70-75
  │           └── nix.build late-drv  80-130   (ends 31 s after its parent)
  └── repo e  100-200
      └── pn.exec ./update-locks.sh  100-190
          └── nix.invocation develop  101-199   (ends 9 s after its parent)
              └── nix.build e-drv  110-195     (inside its raw parent, past its clamped parent)
  ```

- [ ] **Step 2: Write the failing tests**
  - `TestParseTempoV2`:
    - span count across two `resourceSpans`, and names;
    - `Start`/`End` from the nano strings;
    - an `intValue` rendered as decimal;
    - an `arrayValue` joined with `,`;
    - absent `parentSpanId` → empty `ParentID`;
    - `Error` from `STATUS_CODE_ERROR`;
    - a `doubleValue` rendered with `strconv.FormatFloat(v, 'f', -1, 64)` (`0.25` → `0.25`), and a `boolValue` as `true`/`false`;
    - malformed JSON → error.
  - `TestAnalyze_FlakeScopeRepo` (`flake-scope.json`): repo `c` has no `update_locks` (decode the JSON into `map[string]any` and assert the key is absent); `relock_seconds` 90 (95 − 5); `git_seconds` 10 (direct children only, so the git call at 90-94 inside the relock is not counted); `unattributed_seconds` 5 (the union of 0-10 and 5-95 is 0-95, so 100 − 95 = 5; a plain sum would give 100 − (10 + 90) = 0); `nix == [{"flake update" 82 0 0}]` (89 − 7 = 82). `RenderText` prints `  c  wall 1m40s  relock 1m30s  git 10s  unattributed 5s` (no `update-locks` field when the script did not run) and no "no step data" line for `c`.
  - `TestAnalyze_SumsAndRecursiveClamp` (`sums-and-clamps.json`):
    - `substitute_seconds_summed` 25 ((20 − 10) + (45 − 30) = 10 + 15 = 25) and `lock_wait_seconds_summed` 15 ((60 − 50) + (75 − 70) = 10 + 5 = 15);
    - `top_builds == [{e-drv e "" 80}, {late-drv d "" 19}]`. Clamping runs top-down, each span to its parent's CLAMPED interval: e's develop clamps to 101-190, so e-drv clamps to 110-190 = 80 s; late-drv clamps to develop's 99, so 99 − 80 = 19 s. Neither build starts inside a `pn.ul.step`, so `Step` is empty;
    - `anomalies` are exactly, in the start order of the late span: `nix.build late-drv in d ends 31s after its parent nix.invocation (d's figures clamp it to its parent's end)` (130 − 99 = 31) and `nix.invocation develop in e ends 9s after its parent pn.exec (e's figures clamp it to its parent's end)` (199 − 190 = 9). An anomaly compares a span with its parent's RAW end, so e-drv (195, inside the raw 199) raises none. The label after a span name is `nix.drv.name` for `nix.build`, `nix.subcommand` for `nix.invocation`, and nothing otherwise;
    - repo `e`'s `unattributed_seconds` 10 (100 − 90).
  - `TestAnalyze_NoRepoSpans`: a trace with only `pn.verb` 0-10 → no error, `repos` empty, `outside_repos_seconds` 10.
  - `TestAnalyze_NestedRepo`: a `pn.repo` inside another → the inner is reported as its own repo (a span's repo is its nearest `pn.repo` ancestor), and `anomalies` gains `pn.repo <inner> is nested inside pn.repo <outer>`.
  - `TestReportJSONKeys`: marshal the `update-two-repos.json` report and decode it into `map[string]any`; assert the exact top-level key set and one repo's key set from the Interfaces list (decoding into `Report` cannot catch a wrong struct tag).
  - `TestAnalyze_SpansFromTelemetryStarters` (the producer-consumer seam): under `telemetrytest.New()`, build a trace with the real starters (`StartVerb`, `RecordUpdateScope`, `StartRepo`, `StartExec("./update-locks.sh")`, `RecordULSteps`, `StartRelock`), convert the recorder's spans to `[]Span` with a test-only adapter, run `Analyze`, and assert the step names and outcomes, `scope == "flake"` and `relock_seconds > 0` on the in-memory `Report` (it keeps unrounded seconds, so a relock span that lasts microseconds under the wall clock still counts; the 0.1 s rounding happens only when marshalling). This catches a renamed span or attribute key that the hand-written fixtures would not.
  - `TestAnalyze_RepoBreakdown`, for repo `a`:
    - wall 600, `git_seconds` 10, `relock_seconds` 10;
    - `update_locks`: seconds 580, `setup_seconds` 10, steps `[nix-flake-update 100 ok, update-deps-x 400 ok]`, `between_steps_seconds` 10, `finalize_seconds` 60;
    - `unattributed_seconds` 0. Calculation: the counted intervals 0-10 (git), 10-590 (script) and 590-600 (relock) cover 600 of 600 s. A `develop` `nix.invocation` never counts, because it nests inside the script.
  - `TestAnalyze_RepoWithoutSteps`, for repo `b`:
    - `update_locks.steps_missing=true`, `steps` empty, `update_locks.seconds` 295;
    - `unattributed_seconds` 100. Calculation: wall 400 minus git 5 and script 295 = 400 - 300 = 100.
  - `TestAnalyze_OutsideReposAndHooks`: `outside_repos_seconds` 50 (verb 1050 minus repos 600 + 400 = 1050 - 1000 = 50), and `hooks == [{post update 40}]`.
  - `TestAnalyze_TopBuildsOrderedAndStepTagged`: with `top=2`, `top_builds == [{slow-drv a update-deps-x 300}, {fast-drv a nix-flake-update 30}]`.
  - `TestAnalyze_Anomalies`: `anomalies` contains exactly two entries: `nix.invocation develop in b ends 50s after its parent pn.exec (b's figures clamp it to its parent's end)`, and `1 span(s) reference a parent missing from the trace (Tempo may have truncated it; those spans are left out of every figure)`. Repo `b`'s develop span is clamped to 900 s for accounting.
  - `TestAnalyze_LockWaitSummed`: `lock_wait_seconds_summed == 40` (340 - 300 = 40).
  - `TestAnalyze_NixCallsAndSubstitutes`: repo `a`'s `nix == [{develop 576 2 1}]` (588 - 12 = 576); repo `b`'s `nix == [{develop 293 0 0}]` (its develop span clamped to its parent's end: 900 - 607 = 293); `substitute_seconds_summed == 20` (95 - 75 = 20).
  - `TestAnalyze_IncompleteTrace`: `incomplete.json` without `partial` → `errors.Is(err, ErrIncompleteTrace)`; with `partial` → a report with `incomplete: true` and repo `a`.
  - `TestRenderText` asserts on these lines rather than on a golden file. Text rounds a duration to the nearest whole second before formatting (`39.6` → `40s`), then renders whole seconds under a minute (`40s`), `XmYYs` under an hour (`6m40s`), and `XhYYm` from an hour (`1h20m`); a percentage rounds to a whole number. For the fixture, the output contains, in order (two-space indents shown literally):
    - `pn workspace update (scope full), trace <id>, started <start, RFC3339 UTC>, wall 17m30s` (1050 s = 17m30s);
    - `Slowest: a 10m00s (57%) — step update-deps-x 6m40s; top local build slow-drv 5m00s` (600 / 1050 = 0.571, so 57%);
    - repos slowest first, one line each: `  a  wall 10m00s  update-locks 9m40s  relock 10s  git 10s  unattributed 0s`, then `  b  wall 6m40s  update-locks 4m55s  relock 0s  git 5s  unattributed 1m40s` (580 s = 9m40s; 295 s = 4m55s; 100 s = 1m40s);
    - below repo `a`, its steps slowest first, `    step update-deps-x 6m40s ok` then `    step nix-flake-update 1m40s ok` (JSON keeps start order; a code comment says the difference is intended), then `    nix develop 9m36s (plan: 2 builds, 1 fetch)` (576 s = 9m36s);
    - below repo `b`, `    no step data: update-locks.sh printed no UL_STEP lines (it exited before its first step, or its lib predates UL_STEP)` and `    nix develop 4m53s (plan: 0 builds, 0 fetches)` (293 s = 4m53s);
    - `Outside repos: 50s (hooks: post update 40s)`;
    - `Nix (summed across parallel work, can exceed wall): substitutes 20s, build-lock wait 40s`;
    - a `Top local builds` block: `  slow-drv  5m00s  a / update-deps-x`, then `  fast-drv  30s  a / nix-flake-update`;
    - an `Anomalies` block with the two `TestAnalyze_Anomalies` lines;
    - with `incomplete`, a first line `INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded`, and, when a header is passed, the header as the second line (the `INCOMPLETE TRACE` line appears once).
  - `TestRenderText_NoBuildsNoAnomalies`: a report with no builds and no anomalies prints neither block heading.
- [ ] **Step 3:** Run `cd modules/pn && nice -n 10 go test -p 4 ./internal/traceexplain/`; expect FAIL.
- [ ] **Step 4: Implement.**
  - A span's repo is its nearest `pn.repo` ancestor.
  - Clamp every span to its parent's interval before any accounting, top-down, so each span is clamped to its parent's already-clamped interval. Record an anomaly when a child ends more than 1 s after its parent's RAW end, listing anomalies in the start order of the offending span.
  - Count orphans: spans whose `ParentID` is not in the trace. Leave them out of every figure.
  - `git_seconds` counts only `pn.exec git` spans that are DIRECT children of the `pn.repo` span, so git calls inside `pn.relock` are not counted twice.
  - Unattributed time is the repo wall minus the length of the union of its counted intervals (direct-child `pn.exec` and `pn.relock`), each clamped to the repo.
  - JSON: `start` is RFC3339 UTC, and every field whose JSON name contains `seconds` is rounded to the nearest 0.1 s when marshalled (a custom `MarshalJSON` or rounding at the encode step), never inside `Analyze`.
  - Steps come only from `pn.ul.step`.
- [ ] **Step 5:** Re-run Step 3; expect PASS.
- [ ] **Step 6:** Commit `feat(pn): trace analysis for pn workspace explain`.

### Task 7: `pn workspace explain` command, docs and skill wiring

**Files:**

- Create: `modules/pn/internal/cli/explain.go`
- Modify:
  - `modules/pn/internal/cli/workspace.go`: register the subcommand.
  - `modules/pn/internal/cli/workspace.go`: the `pn workspace` `Long` environment list (`:41-51`) gains `PG_TEMPO_URL  Tempo base URL for 'pn workspace explain' (default http://127.0.0.1:3200)`.
  - `modules/pn/README.md`: a new "pn workspace explain" section with the exit codes, why it uses 75 (pn's existing "retry later" code), and that JSON `steps` keep start order while the text sorts them; plus a bullet in "Finding a run's trace" (`:74`) pointing to `pn workspace explain`.
  - `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md`: the cheat sheet.
  - `pn-workspace-rules/skills/pn-workspace-rules/USER_JOURNEYS.md`: a new journey J31, "Find out why an update was slow", under "Observability" after J30 (`:782`), and the coverage summary (`:823` onward) updated.
  - `pn-workspace-rules/skills/pg-nix-log-wrapped/SKILL.md`: in the `## Where to look: Tempo (TraceQL)` section (`:203`; the Prometheus section at `:303` is unchanged), start with `pn workspace explain <trace-id>` for a pn run.
  - `pn-workspace-rules/commands/pn-workspace-update.md`: the final report.
- Test: `modules/pn/internal/cli/explain_test.go`

**Interfaces:**

- Consumes: `traceexplain.ParseTempoV2`, `Analyze`, `RenderText`, `ErrIncompleteTrace` (Task 6); `eventlog.DefaultPath()`; the `root` field on `run_start` (Task 2).
- Produces: `pn workspace explain [<trace-id>] [--json] [--top N] [--partial] [--tempo-url URL]`.
  - **Default trace:** the newest `run_start` whose `root` equals the current workspace root. The root comes from `resolveWorkspaceRoot("")` (`internal/cli/workspace.go:135`), the same function that produces the `root` pn records (`filepath.Abs`, no symlink resolution), so the comparison is exact. When it returns an error (no `pn-workspace.toml` at `PN_WORKSPACE_ROOT`, or above the cwd), treat that as no root: use the newest `run_start` overall, and write `<any root>` for `<root>` in the exit-3 messages. Never skip back past a newer matching run that has no `trace_id`. Scan `events.jsonl` and then `events.jsonl.1`: an update that fails before writing `run_start` can still rotate the log when it opens it (`internal/eventlog/eventlog.go:62-65`), leaving the newest real run only in `.1`. `pn workspace upgrade` and `doctor --fix` record no run (`upgrade.go:22` passes no `Log`), so the command's `Long` says: `pn workspace upgrade and doctor --fix record no run; pass the trace id (PN_TRACE_HINT=1 or -v prints it).`
  - **Header** (text mode only; `--json` prints only the JSON), passed to `RenderText` as its `header` argument, which prints it after the `INCOMPLETE TRACE` line when there is one. Default trace: `Explaining the newest 'pn workspace update' run for <root>: started <RFC3339 UTC> (<age> ago)`, plus, when a newer `run_start` exists for another root, `(a newer run exists for <other root> at <time>: pn workspace explain <id>)`. Explicit id: `Explaining trace <id>`.
  - **Clock:** the command reads the time through a package-level `now func() time.Time` so tests can pin "how long ago the run ended". An injected clock (not `testing/synctest`) is the right tool here under `docs/go-testing-time.md`, because the time it compares against is persisted (`run_end` in `events.jsonl`).
  - **Defaults:** the URL is `$PG_TEMPO_URL`, else `http://127.0.0.1:3200`; the request times out after 30 s; `--top` is 10.
  - **Input handling:** a trace id must be 32 hex characters. `events.jsonl` is read with `bufio.Reader.ReadBytes('\n')` (no line-length cap), and lines that don't parse are skipped.
  - **Run-end lookup** (used when Tempo answers 404, or returns a trace with no `pn.verb` and `--partial` is unset): search `events.jsonl`, then `events.jsonl.1`, for this trace id's records. One open file descriptor writes a run's `run_start` and `run_end`, so a rotation moves both together. The outcome picks the exit code in the table below: a `run_end` less than 7 days old → 75; a `run_end` 7 or more days old → 3 (expired); a `run_start` with no `run_end` → 4 when Tempo returned a trace without `pn.verb` (it has spans), 3 when Tempo answered 404 (it has none); no record of the id at all, including no events file → 3.
  - **Scope:** it doesn't open the workspace and fires no hooks.
  - **Exit codes.** A non-zero exit prints nothing on stdout, in text and JSON mode alike. Each code has one next action, so an agent can branch on the code alone:

    | Code | Meaning                                                                                                                                                                                                        | Message (stderr)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
    | ---- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
    | 0    | report printed; anomalies and `--partial` don't change it                                                                                                                                                      | —                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
    | 2    | usage, via `SetFlagErrorFunc` and an `Args` validator returning `ExitCodeError{Code: 2}`                                                                                                                       | `trace id must be 32 hex characters, got "<arg>"`; `--top must be a positive integer, got "<arg>"`; `explain takes at most one trace id`; unknown flags keep cobra's message                                                                                                                                                                                                                                                                                                                                                                                                                                            |
    | 3    | nothing to explain: no events file, no matching run, the newest matching run has no trace id, the trace expired, an unknown id, or a run with no `run_end` and no spans in Tempo                               | `no update run with a trace id for <root> in <path>; pass a trace id`; `run <id> has no run_end and none of its spans are in Tempo: it is still running, or was killed before exporting; nothing to explain yet`; `the newest update run for <root> (<time>) recorded no trace id (telemetry was off: enable it in ~/.config/pn/telemetry.toml or pass --otel-spool-dir); pass an older trace id from <path>`; `trace <id> has expired from Tempo (it keeps up to 7 days; the run ended <time>)`; `trace <id> is not in Tempo and no pn run in <path> recorded it; check the id, or pass --tempo-url for another Tempo` |
    | 4    | the run has no `run_end` but Tempo has some of its spans (still running, or killed); only ever returned without `--partial`, because with it those spans are analyzed and the exit is 0                        | `run <id> has no run_end: it is still running or was killed; pass --partial to analyze what was recorded`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
    | 75   | the run ended less than 7 days ago but Tempo has not ingested it (404, or no `pn.verb` yet). 75 is sysexits `EX_TEMPFAIL`, which pn already uses for "retry later" (`internal/workspace/rebuildlock.go:51-56`) | Tempo answered 404: `trace <id> is not in Tempo yet (the run ended <time>); retry in a minute; if it persists, run 'pn workspace doctor'`. Tempo has spans but no `pn.verb` yet: the same, ending `run 'pn workspace doctor' or pass --partial` (a 404 has nothing for `--partial` to analyze, so only this case offers it)                                                                                                                                                                                                                                                                                             |
    | 5    | Tempo unreachable, any other non-200 answer, or a 200 whose body is not a parseable trace                                                                                                                      | `cannot read trace <id> from <url>: <cause>; is Tempo running? set PG_TEMPO_URL or --tempo-url`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |

    The command's own `Long` lists these codes, as `flake-check`'s `Long` does (`cli/workspace.go:362-366`).

- [ ] **Step 1: Write the failing tests.** Tempo is an `httptest.NewServer`; `t.Setenv("XDG_STATE_HOME", t.TempDir())` with a hand-written `pn/events.jsonl`; `t.Setenv("PN_WORKSPACE_ROOT", ...)` selects the root, and every root dir a test uses gets a `pn-workspace.toml` so `resolveWorkspaceRoot` accepts it.
  - `TestExplainCmd_DefaultsToNewestRunForRoot`: three `run_start` records (root A old, root B newest, root A newer). With root A, the server sees the "root A newer" id, and the output header names that run's time and root.
  - `TestExplainCmd_NewestRunWithoutTraceIDExits3`.
  - `TestExplainCmd_TextAndJSON`: serving `../traceexplain/testdata/update-two-repos.json` prints the repo lines, and `--json` unmarshals into `traceexplain.Report` with two repos.
  - `TestExplainCmd_DefaultFoundOnlyInRotatedLog`: `events.jsonl` holds only another root's records and `events.jsonl.1` holds root A's run → the server sees root A's id.
  - `TestExplainCmd_HeaderNotesNewerRunElsewhere`: root A's newest run is older than root B's → the text header ends `(a newer run exists for <root B> at <time>: pn workspace explain <B's id>)`.
  - `TestExplainCmd_ExitCodes` (table; `now` pinned to 2026-11-18T12:00:00Z). Each row gives the events content, the Tempo answer, the exit code and the message from the exit-code table:
    - no events file, no id → 3, `no update run with a trace id for <root> in <path>; pass a trace id`;
    - empty events file, no id → 3, the same message;
    - `run_start` and `run_end` (ended 1 minute before `now`), 404 → 75, `trace <id> is not in Tempo yet (the run ended <time>); ...`, and the message does NOT contain `--partial`;
    - `run_start` and `run_end` (ended 8 days before `now`), 404 → 3, `trace <id> has expired from Tempo ...`;
    - `run_start` only, 404 → 3, `run <id> has no run_end and none of its spans are in Tempo: ...; nothing to explain yet`, and the same with `--partial` (a 404 has nothing to analyze);
    - both records only in `events.jsonl.1` (ended 1 minute before), 404 → 75;
    - no events file, explicit id, 404 → 3, `trace <id> is not in Tempo and no pn run in <path> recorded it; ...`;
    - `run_start` and `run_end` (recent), Tempo serves `incomplete.json` → 75, and the message ends `or pass --partial`;
    - `run_start` and `run_end` (recent), Tempo serves `incomplete.json`, `--partial` → 0 with the `INCOMPLETE TRACE` line first (proves the advice on the row above works);
    - `run_start` only, Tempo serves `incomplete.json` → 4, `run <id> has no run_end: it is still running or was killed; pass --partial ...`;
    - `run_start` only, `incomplete.json`, `--partial` → 0; the first stdout line is `INCOMPLETE TRACE: no pn.verb span; figures cover only what was recorded` and the second is the header;
    - explicit id, server closed → 5, a message starting `cannot read trace <id> from <url>: `;
    - explicit id, 500 → 5, the same prefix;
    - id `xyz` → 2, `trace id must be 32 hex characters, got "xyz"`;
    - `--top abc` → 2, `--top must be a positive integer, got "abc"`;
    - two positionals → 2, `explain takes at most one trace id`;
    - `--json` with any non-zero row above → stdout is empty.
  - `TestExplainCmd_TopFlag`: `--top 1` gives one `top_builds` entry.
  - `TestExplainCmd_TempoURLPrecedence`: two servers A and B; `PG_TEMPO_URL=A` plus `--tempo-url B` → only B gets a request; `PG_TEMPO_URL=A` alone → only A. The default URL is tested as a pure function (`tempoURL("", "")` returns `http://127.0.0.1:3200`), and EVERY explain test sets `PG_TEMPO_URL` or `--tempo-url`, so no test can reach a real Tempo on the default port.
  - `TestExplainCmd_NoWorkspaceUsesNewestOverall`: `t.Setenv("PN_WORKSPACE_ROOT", "")`, `t.Chdir` into a temp dir with no `pn-workspace.toml`, records for root A (older) and root B (newer) → the server sees B's id; with no records at all, the exit-3 message contains `<any root>`.
  - `TestExplainCmd_NewestRunWithoutTraceIDExits3` uses a fixture with an OLDER matching run that does have a trace id, and asserts exit 3, zero Tempo requests, and the newer run's `(<time>)` in the message (so the command never skips back to the older run).
  - `TestExplainCmd_FindsRunStartWrittenByUpdate` (the producer-consumer seam): with an `eventlog` writer whose trace id is set (`lw.SetTraceID(id)`), call `w.Update(..., UpdateOptions{Log: lw, ...})` on a FakeRunner workspace, then run `explain` with no id from that root → the server receives `/api/v2/traces/<id>`.
  - `TestExplainCmd_EventsReader`: a line over 64 KiB before the `run_start` is read (this pins `ReadBytes` over `bufio.Scanner`'s 64 KiB default); a garbage line is skipped; a pre-change `run_start` with no `root` field never matches a root, and is chosen only by the no-root newest-overall fallback.
  - `TestExplainCmd_ExitCodes` gains rows: Tempo answers 200 with a body that is not valid JSON → 5, `cannot read trace <id> from <url>: ...`; `--top -1` → 2, `--top must be a positive integer, got "-1"`; an unknown flag → 2; `--partial` on a complete trace → 0 with no `INCOMPLETE TRACE` line.
- [ ] **Step 2:** Run `cd modules/pn && nice -n 10 go test -p 4 ./internal/cli/ -run TestExplainCmd`; expect FAIL.
- [ ] **Step 3: Implement** with `ExitCodeError` for every non-zero exit.
- [ ] **Step 4:** Re-run Step 2, then `cd modules/pn && nice -n 10 go test -p 4 ./...`; expect PASS.
- [ ] **Step 5: Docs and skill wiring.** In `pn-workspace-update.md`'s `Step 6 REPORT` (added by Task 3):
  - with no `update-relock: trace <id>` line from step 2a, report `no trace id (telemetry off)` and do NOT run `pn workspace explain` (with no id it would explain the newest canonical run, possibly last week's);
  - otherwise run `pn workspace explain <id>` and include its `Slowest:` line, its repo lines and its anomalies;
  - on exit 75, wait with Monitor, using an until-loop on the command that stops on any exit other than 75, for at most 120 s; if it still exits 75, report "trace not yet ingested";
  - on exit 4, re-run with `--partial` and say the figures are partial; if that re-run exits non-zero, report its message as is;
  - on exit 3 or 5, report the message as is.

  Run `nix fmt` to convergence.

- [ ] **Step 6:** Build `.#checks.aarch64-darwin.pnwf-runner-turn-boundary-rule` and `.#checks.aarch64-darwin.land-gate-rule-docs` through `bgrun` + `bgwait --timeout 540` (Step 5 edited `pn-workspace-update.md` and two skill files they read); both must reach `DONE exit=0`. Then `git add -A`, `pg-hooks fix`, commit `feat(pn): pn workspace explain reports where an update spent its time`.

### Task 8: Validate, land, follow-ups

- [ ] **Step 1:** Run each of these through `bgrun` + `bgwait --timeout 540` (Bash timeout 600000 ms); each must reach `DONE exit=0`: `nix build .#checks.aarch64-darwin.pn-go-tests -L`, `.#checks.aarch64-darwin.test-update-locks-lib`, `.#checks.aarch64-darwin.test-pnwf`, and the two checks that read the markdown Tasks 3 and 7 rewrite, `.#checks.aarch64-darwin.pnwf-runner-turn-boundary-rule` (`flake.nix:1291`) and `.#checks.aarch64-darwin.land-gate-rule-docs` (`flake.nix:1515`). Neither is a commit hook or a land gate, and nothing here pushes, so CI would never run them for this change.

  Before the smoke run, make it exercise the trailing relock. Today none of s20, s32, s33 or s33b commits a `flake.lock`, and nothing in `init`/`clone`/`lock` writes one, so `relockAllInputs` takes its missing-lock early return in every scenario. Extend two scenarios, one per flow:
  - s32 (in-place) already has a producer and a consumer that reads it through `git+file://` (`setup.sh:46-52`);
  - s33 (`s33-worktree-update`; s33b also runs the worktree flow, but its subject is a dirty main, so leave it alone) is a single repo `solo` with `inputs = {}` (`setup.sh:19-21`). First add a producer repo as a workspace member in BOTH workspace files: the scenario's static `scenarios/s33-worktree-update/pn-workspace.toml`, which the harness copies before `setup.sh` overwrites it (`smoke_test.go:616-621`), gains a `[repos.producer]` placeholder entry like s32's (`url = "file:///PLACEHOLDER_producer"`); and the `pn-workspace.toml` that `setup.sh` writes gains the real producer entry (read s32's `setup.sh` for the exact form). Make `solo` consume the producer through `git+file://`, in s32's shape, and update `setup.sh`'s header comment, which still says "One bare-remote repo".

  In both scenarios, setup runs in this order: push producer commit 1; write the consumer's `flake.nix`, `git add flake.nix`, run `nix flake lock` in the consumer (confirm its `flake.nix` has no input besides the local producer, so this needs no network); commit and push the consumer's `flake.lock`; then push producer commit 2, so `update` has a real move to relock. The new assertions go in the Go assertion functions, not in the scenario directories: `assertS32EventsJSONL` (`smoke/smoke_bare_remote.go:650`) and `assertS33WorktreeUpdate` (`:739`) each assert the consumer's log gains a `chore(deps): nix flake update` commit. Both scenarios gain the `requires-nix` marker file (`smoke_test.go:144-160`; s23 is an existing user). No scenario yet shows that `nix flake lock` can write the store inside the smoke build's sandbox. If it cannot, stop and report to the operator rather than hand-writing a lock or dropping the assertion, because D12's smoke run would then prove nothing about the relock. Then, per D12, run the integration suite deliberately the same way: `nix build .#integration-checks.pn-smoke-tests -L` (it lives at `legacyPackages.<system>.integration-checks.pn-smoke-tests`, `flake.nix:514,550`), through `bgrun` + `bgwait --timeout 540`, calling `bgwait` again while it exits 2. It must reach `DONE exit=0` before landing. Scenarios s20, s32, s33 and s33b drive the default `full` path, which now ends with a `nix flake update --refresh` relock and adds `scope` and `root` to `run_start`. With the extension above, s32 and s33 each relock a real `git+file://` input (`modules/pn/internal/workspace/smoke/scenarios/s32-update-events-jsonl/setup.sh:46-52` shows the shape) and must add a commit when a nix daemon is reachable (see the ruling below). A failing scenario is fixed on this branch before landing. This is a run the change warrants; do not wire the suite into `checks`, a commit hook or a land hook (ADR 0024).

  Ruling, 2026-11-20: the operator ruled (Phillip, verbatim "continue", answering "Should I go ahead with option A plus this harness fix?") that the relock half of s32 and s33 self-skips inside the nix build sandbox, because a sandboxed builder cannot reach the nix daemon, and is proven by a host run with `go test -tags smoke`. The harness also writes the smoke identity to the throwaway `$HOME/.gitconfig`, because pn commits through `x/gitclient`, which passes only PATH, HOME and SSH_AUTH_SOCK. This replaces the earlier sentence that s32 and s33 relock "in the sandbox ... and must add a commit"; the rest of this step stands.

- [ ] **Step 2:** Run `pg-hooks run pre-land`; expect exit 0. Then `pn workspace doctor` from inside the set; expect no BLOCKING line. Nothing here changes a flake module, so no cross-repo flake check is needed.
- [ ] **Step 3:** Land with `pn-workspace-rules:land-workforest`, then clean up with `pn-workspace-rules:cleanup-workforest`. No push.
- [ ] **Step 4:** File these beads, invoking `beads-lifecycle` first:
  - **A `human` verify bead** (labels `base`, `pn`), gated on apply with `pb:pb-gate-lifecycle` and keyed on the landed commits' patch-ids. After the operator's next canonical `pn workspace update` with `--scope full`, `pn workspace explain` must show `pn.ul.step` steps for every repo. `steps_missing` in a canonical run is a bug unless that repo's `update-locks.sh` exited in `ul_setup` before its first step (a dirty tree, or exit 77 for an unhealthy nix daemon), which the run's output shows. The deterministic behaviour is covered by the tests above; this bead only proves that the installed `pn` and lib changed.
  - The `nix develop` span that ended after its `pn.exec` parent had a known cause, fixed on 2026-11-19 before Task 0 (pn now ends spans from the wall clock; see Global Constraints). If `explain` still reports a child ending after its parent on a trace recorded after that fix, it is a new bug: file a `base`, `pn` bead carrying the trace id and the anomaly line.
  - **Approver bead** (D10), filed in the nix-agent-support tracker with label `agent-support`, in `packages/claude-extended-tool-approver/`:
    - add `explain` to `approvedSubcommands` in `internal/rules/pnworkspace/pnworkspace.go`;
    - teach its command specs (`internal/cmddesc/registry_repo_base.go`, `internal/embeddedspecs/data/pn.json`, `internal/embeddedspecs/data/pnwf.json`) the `pn workspace explain` subcommand and its flags, and the `--scope` flag of `pn workspace update` and `pnwf update-relock`. `--scope` takes a value (`siblings`, `flake` or `full`), unlike the `inert` boolean flags the registry models for update today (`registry_repo_base.go:159-161`);
    - update the tests and golden files that pin those specs: `internal/cmddesc/registry_repo_base_test.go:26,89`, `internal/effectpolicy/p15report.go:579-582`, `internal/effectpolicy/testdata/p15/pn-workspace-marker.md:130` (one golden line per flag) and `internal/cmddesc/plugin_tools_test.go:371`;
    - approve the Monitor until-loop that `/pn-workspace-update`'s Step 6 REPORT runs around `pn workspace explain <id>`, or that wait prompts on every update.
  - **A nix-personal doc bead** (label `personal`): `docs/adr/0060-homebrew-follows-nix-lock-model-on-activation.md:54` says "`pn workspace update` MUST refresh the lock"; since ADR 0045 that holds for `--scope full` (the CLI default) but not for the skill's `flake` default. Amend it to say which scope refreshes the Homebrew lock.

## Out of scope

- Scheduling any update (D4).
- A guard that rolls back expensive nixpkgs bumps (D5).
- Changing what `update-locks.sh` refreshes (the separate review of every step beyond `nix flake update` covers that).
- Mutation testing.
