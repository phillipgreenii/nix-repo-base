# Early sudo Authentication for `pn workspace apply` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**BLOCKED ON:** `2026-11-10-pn-apply-remove-legacy-flow.md` (landed). That plan leaves apply with one flow (`applyAsUser`), which is the only place this plan integrates. Do not start before it has landed.

**Goal:** `pn workspace apply` asks for sudo (password or Touch ID) as its first interactive step, builds as the invoking user, and runs the two root steps on the already-authenticated session with no second prompt, even when the build takes longer than sudo's credential timeout.

**Architecture:** A `sudoSession` (Resource Acquisition Is Initialization style: acquired once, released by a deferred `Stop`) primes the sudo credential cache with `sudo -v`, keeps it warm with a refresh goroutine that runs `sudo -n -v` on a fixed interval while the build runs, and on `Stop` ends the goroutine and, only if pn created the credentials, invalidates them with `sudo -k`. The build stays a user process in its own process group; root never evaluates.

**Tech Stack:** Go (`modules/pn/internal/workspace`), the `exec.Runner` seam with `asUserFake` in tests, markdown ADRs.

**Spec:** Operator request (Phillip, 2026-11-10), verbatim: "i used to be able to trigger an apply, it would quickly prompt for sudo and i would submit the password. then i could go do something else, even walk away from the computer, and the apply would be back by the time i return"; "my use case would be if trigger apply, approve sudo, lock screen, go to lunch, come back and have apply complete"; "a sleeping computer isn't my problem. after awake, it is fine for another prompt"; "i still want the build to work like today, ie, as my user, not root"; "make sure the sudo refreshes are correct and will be cleaned up correctly". Background: `docs/adr/0038-apply-builds-as-the-user-and-runs-only-profile-set-and-activate-as-root.md` ("UX change: the sudo prompt now comes AFTER the build") and ADR 0041 from the prerequisite plan.

All paths are relative to `<repo-base>` = `/Volumes/gitrepos/phillipg/nix-repo-base`; Go commands run in `<repo-base>/modules/pn`.

## Global Constraints

- Process (skills `pn-workspace-rules:pn-workspace-rules`, `fork-workforest`, `validate-workforest`, `land-workforest`, `cleanup-workforest`): this plan changes only `nix-repo-base`, but the canonical workspace root `/Volumes/gitrepos/phillipg` is not a git repository, so `EnterWorktree` does not work there. Use a subset workforest set that includes the terminal repo (`nix-ziprecruiter`; a subset without the terminal clears `workspace.terminal` and cannot run `pn workspace build` or `doctor`'s terminal check): from that root run `pnwf fork-preflight pn-apply-sudo-session --repos nix-repo-base,nix-ziprecruiter`, `pn workspace workforest add pn-apply-sudo-session --repos nix-repo-base,nix-ziprecruiter`, `cd /Volumes/gitrepos/phillipg/.workforests/pn-apply-sudo-session`, `export PN_WORKSPACE_ROOT="$PWD"`. Never `git checkout -b` on a canonical clone.
- Completion Gate before declaring done: the targeted checks for `nix-repo-base` (the pn go-tests check and the smoke check, as `nix build .#checks.<system>.<name> -L` in the background); because the change reaches the assembled system, `pn workspace build --wait=5m` in the set; then `pn workspace doctor`. Land with `land-workforest`, clean up with `cleanup-workforest`. An agent MUST NOT run `pn workspace apply`.
- The sudo session is platform independent (darwin and NixOS use the same `sudo -v`, `sudo -n -v`, `sudo -k`). The prerequisite plan makes apply work on both platforms; nothing here is darwin-specific.
- Goroutine rules for the refresh loop (Go best practice, operator request 2026-11-10: "make sure that the goroutines follow go best practices of being cancellable and they do the correct thing and there are tests verifying"): (1) one goroutine, started only after acquisition succeeded; (2) its context derives from the apply's `ctx`, so cancelling the apply stops it even if `Stop` is never called; (3) it selects on `ctx.Done()` and a `time.Ticker` that is stopped with `defer`; (4) every external call inside it has its own deadline and uses the loop context, so cancel interrupts a call in flight; (5) it signals exit by closing a channel that `Stop` waits on, and exit is guaranteed on every return path (`defer close(done)`); (6) it never writes to `out`, never blocks on an unbuffered send, and shares state only through `atomic` values; (7) `Stop` is idempotent, concurrency-safe and bounded; (8) the interval is copied into the struct at start so tests do not race on a package variable; (9) there is a leak test and a `-race` run.
- Go tests: `nice -n 10 go test -p 4 ...` in the background with an explicit timeout (`bgrun`/`bgwait`); no test sleeps for real intervals (refresh interval is a package variable, set to milliseconds in tests).
- No agent runs `sudo`, `pn workspace apply` or `pn workspace update`. Real-sudo behaviour is proven only by the operator-run spike (Task 1) and the post-deploy check (Task 6).
- Root-executed argv is unchanged (`setProfileArgv`, the `activate_command` expansion). The new sudo calls are exactly `sudo -v`, `sudo -n -v` and `sudo -k`, hard-coded like the set-profile step, never templated.
- The build keeps `OwnProcessGroup: true`; every sudo session call stays in pn's own process group (a background group would be stopped by SIGTTIN when sudo reads the terminal).
- The refresh goroutine MUST NOT write to `out` (the apply log tee is not safe for concurrent writes); it records state in atomics and the apply flow reports it from its own goroutine.
- No bead ids in any committed file.
- Commit flow: `git add <files>`; `nix fmt -- <files>`; `pg-hooks fix`; `git commit`; `pg-hooks run pre-land` before landing.

## Review Focus

- The build outlasts sudo's timeout (default 5 minutes, possibly shorter or `0` in sudoers): the refresh keeps the session valid, or, when sudo caches nothing at all, apply says so once up front and prompts after the build as it does today.
- The operator already had a valid sudo ticket when apply started: apply MUST NOT invalidate it on exit (`sudo -k` only when pn created the credentials).
- Cancel (Ctrl-C, SIGTERM, SIGHUP from a closed terminal) during the build, during the prompt and during the root steps: the refresh goroutine ends, no `sudo` child remains, and `sudo -k` runs with a context that survives the cancel.
- `kill -9` of pn: the refresh dies with the process (it is a goroutine, not a shell loop), so nothing keeps refreshing; the credential decays within sudo's timeout. This is documented, not fixed.
- The build fails, or the skip gate decides "unchanged": no sudo prompt appears for a skipped apply, and a failed build leaves no refreshing session.
- Wrong password or cancelled prompt: apply fails before building anything.
- Refresh failure mid-build (credentials lost, for example after a sleep longer than the timeout): the build finishes, the operator sees one notice before the root steps, and sudo prompts then, which the operator has accepted as fine. A sudoers `timestamp_timeout` below the 60 s refresh interval, or a refresh that is mid-flight when the build ends so that `Lost()` is read just before it flips, takes the same path: at worst the notice is missing and sudo prompts, which is benign.

---

### Task 1: Operator spike: prove the runner's file-descriptor shape keeps the ticket

The agents cannot run sudo, so this task is run by the operator and its result decides one parameter of Task 3.

**Files:** none (a result recorded in the ADR of Task 6).

- [ ] **Step 1: Operator runs, in a terminal, with Touch ID or password at the first prompt:**

```bash
sudo -k
sudo -v </dev/null 2>&1 | cat; echo "prime with stdin=/dev/null, output piped: exit=${PIPESTATUS[0]}"
sudo -n -v </dev/null 2>&1 | cat; echo "refresh, same shape: exit=${PIPESTATUS[0]}"
bash -c 'set -m; sudo -n -v </dev/null >/dev/null 2>&1 & wait $!; echo "refresh from a background process group: exit=$?"'
sudo -n true </dev/null >/dev/null 2>&1; echo "sudo -n true: exit=$?"
sudo -k </dev/null >/dev/null 2>&1; echo "sudo -k: exit=$?"
sudo -n true </dev/null >/dev/null 2>&1; echo "after sudo -k: exit=$? (expect nonzero)"
```

The prime step prompts (password or Touch ID) with the same file-descriptor shape the runner gives a child (stdin `/dev/null`, output through a pipe); the prompt itself reads the controlling terminal. Expected: the prime, the refresh, the background-group refresh and the use all exit `0`, and the last line is nonzero. Also record, without acting on it: whether `sudo -k` run from a process whose terminal was closed (`kill -HUP` of the terminal's shell, then a new terminal) leaves a ticket behind; the answer goes into the ADR as a known limit, it does not block the plan.

- [ ] **Step 2: Decide.** If every expected `0` holds: Task 3 passes `Stdin: nil` as today. If one fails (macOS keys the ticket on a file descriptor that is the terminal): Task 3 passes `Stdin: os.Stdin` for every session call and the spike is repeated with `</dev/tty` in place of `</dev/null` to confirm.
- [ ] **Step 3: Record the output** in the plan's execution notes; Task 6 copies it into the ADR.

**Result, run by the operator 2026-11-10 (macOS, zsh login shell, one prompt):** `refresh from background group: exit=0`, `use: exit=0`, `release: exit=0`, `after release: exit=1`. The `prime` and `refresh` lines printed `exit=` blank because `PIPESTATUS` is a bash variable and the shell was zsh (use `bash -c '...'` or zsh's `pipestatus`). Reading: after the initial `sudo -k`, only the prime (`sudo -v </dev/null 2>&1 | cat`) could have created the ticket the later calls found, so a prime with the runner's file-descriptor shape works, and a background-group refresh and the release behave as designed. **Decision: Task 3 passes `Stdin: nil` (no `os.Stdin`).** Follow-up run in bash (operator, same day): `prime: exit=0`, `refresh: exit=0`. The spike is complete and every expected value held.

### Task 2: `sudoSession`

**Files:**

- Create: `modules/pn/internal/workspace/sudo_session.go`
- Test: `modules/pn/internal/workspace/sudo_session_test.go`

**Interfaces:**

- Consumes: `exec.Runner`, `exec.RunOptions`, and the fake `asUserFake` (its `Run` records `exec.Call`s and returns scripted `(exec.Result, error)`; script rules by `argvOf(call)`).
- Produces:
  - `var sudoRefreshInterval = 60 * time.Second` and `var sudoRefreshTimeout = 10 * time.Second` (package variables so tests shorten them).
  - `type sudoSession struct` with unexported fields: `runner exec.Runner`, `stopCtx context.Context` (`context.WithoutCancel(ctx)`, derived ONCE in `startSudoSession`; it carries values such as the trace but not the cancellation, so `Stop` and the cancel-path cleanup can still run `sudo -k`), `stdin io.Reader`, `interval, timeout time.Duration`, `created bool` (pn made the credentials), `cancel context.CancelFunc`, `done chan struct{}`, `lost atomic.Bool` (a refresh failed), `cacheless bool` (sudo caches nothing here), `stopOnce sync.Once`.
  - `func startSudoSession(ctx context.Context, runner exec.Runner, out io.Writer, stdin io.Reader) (*sudoSession, error)`: acquires the session (below) and starts the refresh goroutine. It returns `(nil, err)` only when authentication failed or `ctx` was cancelled; when `ctx.Err() != nil` after a failed `sudo -v` it returns `ctx.Err()` itself (the real runner returns a `*exec.CommandError` after a SIGTERM, not the context error).
  - `func (s *sudoSession) Lost() bool`: true once a refresh failed. `func (s *sudoSession) Cacheless() bool`: true when verification showed sudo caches nothing.
  - `func (s *sudoSession) Stop()`: idempotent, safe on a nil receiver; ends the goroutine, waits for it, then runs `sudo -k` iff `created`, with `context.WithTimeout(s.stopCtx, 5*time.Second)`. `Stop` is bounded by the slowest of the in-flight refresh being cancelled and the `sudo -k` call, which the runner ends at most 10 s (its kill grace) after SIGTERM.
  - The probe, verify and refresh calls set no `Stdout` or `Stderr` (they never touch `out`); only the prompting `sudo -v` gets `Stdout: out, Stderr: out`.

Acquire semantics (the whole algorithm):

1. Probe `sudo -n -v`. Exit 0 means the operator already holds credentials: `created=false`, skip step 2.
2. Print `apply: authenticate sudo now; the build runs as you and the root steps reuse this authentication` to `out`, then run `sudo -v` (not wrapped, own group never set, `Dir: "/"`, output to `out`). Set `created=true` BEFORE the call (a SIGTERM during the prompt may still have created the ticket). On a non-nil error: if `ctx.Err() != nil` run `sudo -k` with `s.stopCtx` and return `ctx.Err()`; otherwise return `fmt.Errorf("sudo authentication: %w", err)`.
3. Verify with `sudo -n -v`. Check `ctx.Err()` FIRST: if the context was cancelled between the prompt and the verify, run `sudo -k` with `stopCtx` (pn created the ticket) and return `ctx.Err()`; this MUST NOT be classified as "sudo caches nothing". Otherwise a failure means sudo caches nothing (`timestamp_timeout=0` or an unusual policy): print `apply: sudo does not cache credentials here; it will ask again after the build`, and return a session with no goroutine (`done` already closed, `cacheless` true, `lost` false, `cancel` set to a no-op func so `Stop` never calls a nil function) and `created=true`.
4. Start the goroutine, `go s.refreshLoop(loopCtx)`, where `loopCtx, cancel := context.WithCancel(ctx)` (derived from the apply's `ctx`, so a cancelled apply ends the loop with no `Stop`). Its exact shape:

```go
func (s *sudoSession) refreshLoop(ctx context.Context) {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.refreshOnce(ctx) {
				return
			}
		}
	}
}
```

`refreshOnce(ctx) bool` runs `sudo -n -v` under `context.WithTimeout(ctx, s.timeout)`. On success it returns `true`. On error, if `ctx.Err() != nil` (the loop was cancelled, so the call was interrupted) it returns `false` WITHOUT setting `lost`; otherwise (a real failure or the per-call timeout) it sets `lost` and returns `false`. No retries: a refresh failure means the credentials are gone and only a person can restore them. `interval` and `timeout` are struct fields copied from `sudoRefreshInterval` and `sudoRefreshTimeout` in `startSudoSession`.

`Stop`: `cancel()`; `<-done`; if `created`, run `sudo -k` with `context.WithTimeout(s.stopCtx, 5*time.Second)`; ignore its error.

Every test in this file that starts a session registers `t.Cleanup(sess.Stop)` (idempotent), so a leftover goroutine can never call the fake after its test ended (`asUserFake` and the session fake call `t.Errorf` on an unexpected call, which panics after the test finishes), and so one test's goroutine cannot pollute the next test's leak count. Fakes signal tests only through BUFFERED channels with a non-blocking send (`select { case ch <- struct{}{}: default: }`), so a fake never blocks the loop and `Stop` never deadlocks on `done`. Tests that need "the loop has ended" wait on the session's `done` channel (with a 5 s timeout guard that fails the test), never on a sleep, and they NEVER call `Stop` before `done` has closed when asserting `Lost()` (`Stop` cancels the loop context, which would turn a real failure into a cancelled refresh).

- [ ] **Step 1: Write the failing tests** (all with `sudoRefreshInterval = 5 * time.Millisecond` set through `t.Cleanup`-restored assignment):
  - `TestSudoSession_PrimesWithVThenVerifiesWithNV`: fake answers probe `sudo -n -v` with exit 1 first, `sudo -v` with success, verify `sudo -n -v` with success; assert the exact call order `[sudo -n -v, sudo -v, sudo -n -v]` before any refresh call, and that `sudo -v` has `Stdout == out` and `Dir == "/"` and `OwnProcessGroup == false`.
  - `TestSudoSession_ExistingCredentialsAreNeverInvalidated`: probe succeeds; after `Stop()` assert no call contains `-k`, and `sudo -v` (the prompting form) never ran.
  - `TestSudoSession_StopInvalidatesCredentialsPnCreated`: created path; after `Stop()` the last call is `sudo -k`.
  - `TestSudoSession_RefreshesWhileRunningAndStopsOnStop`: the fake signals a channel on each refresh call; receive 3 signals (with a `select` against a 5 s timeout that fails the test), call `Stop()` (which waits for the goroutine, so the count read right after it is final), record the count, call `Stop()` again and assert the count and the call list are unchanged. No sleeps.
  - `TestSudoSession_RefreshFailureSetsLostAndEndsTheLoop`: the fake fails the second refresh; wait on the session's `done` channel (the loop ends itself on failure), THEN assert `Lost()` is true and no refresh call follows the failing one; then `Stop()`.
  - `TestSudoSession_AuthenticationFailureReturnsErrorAndStartsNoLoop`: `sudo -v` fails; assert the error wraps it, the returned session is nil, and no `sudo -n -v` refresh ran after the failure.
  - `TestSudoSession_NoCredentialCacheWarnsOnceAndKeepsNoLoop`: verify step fails; assert the warning text in `out`, `Cacheless()` true, `Lost()` false, no refresh calls, and that `Stop()` still runs `sudo -k`.
  - `TestSudoSession_StopSurvivesACancelledContext`: cancel the parent `ctx` first, then `Stop()`; assert `sudo -k` ran and its context was not cancelled (record `ctx.Err()` inside the fake's `Run`).
  - `TestSudoSession_CancelDuringPromptReturnsContextError`: the fake blocks `sudo -v` until `ctx` is cancelled and then returns a `*exec.CommandError` (what the real runner returns after SIGTERM), not `ctx.Err()`; assert `errors.Is(err, context.Canceled)` anyway (the session maps it).
  - `TestSudoSession_ProbeRefreshAndVerifyNeverTouchOut`: assert every `sudo -n -v` call has `Opts.Stdout == nil` and `Opts.Stderr == nil`.
  - `TestSudoSession_StopIsIdempotentAndNilSafe`: call `Stop()` twice and on a nil `*sudoSession`; assert one `sudo -k` only.
  - `TestSudoSession_CancelAfterPromptBeforeVerifyInvalidatesAndReturnsCancel`: the fake cancels the context as it returns from `sudo -v`; assert the error is `context.Canceled`, `sudo -k` ran, no goroutine started, and `out` has no "does not cache credentials" warning.
  - `TestSudoSession_StopOnACachelessSessionDoesNotPanic`: the verify step fails (no cancel); `Stop()` twice; assert no panic, one `sudo -k`.
  - Goroutine tests (helper `runningRefreshLoops() int` in the test file counts goroutines whose stack, from `runtime.Stack(buf, true)` in a buffer that grows until the stack fits, contains `(*sudoSession).refreshLoop`; asserting zero uses a poll-until-zero helper with a 2 s deadline, because `defer close(done)` runs before the goroutine has fully left the stack; `goleak` is not a dependency of this module and adding one would force a `gomod2nix.toml` regeneration, so the helper stays in-package):
    - `TestSudoSession_ParentCancelEndsTheLoopWithoutStop`: start a session, cancel the parent `ctx`, receive from the session's `done` channel with a 5 s timeout guard, assert `runningRefreshLoops() == 0` and `Lost()` is false (a cancelled refresh is not a lost session); then `Stop()` still runs `sudo -k` once.
    - `TestSudoSession_StopInterruptsAHungRefresh`: the fake blocks the refresh call until its `ctx.Done()`; wait for the call to start (channel), call `Stop()` in a goroutine and assert it returns within 2 s, that the blocked call's context was cancelled, and `Lost()` is false.
    - `TestSudoSession_RefreshTimeoutMarksLost`: set `sudoRefreshTimeout` to 10 ms, let the fake block until `ctx.Done()`; assert the refresh call's `ctx` had a deadline (record `ctx.Deadline()` in the fake); wait on `done` (the loop ends itself), then assert `Lost()` is true; then `Stop()` returns.
    - `TestSudoSession_NoGoroutineAfterStop`: for 20 iterations start a session, let one refresh happen, `Stop()`; after the loop assert `runningRefreshLoops() == 0`.
    - `TestSudoSession_NoGoroutineWhenAuthenticationFails`: `sudo -v` fails; assert `runningRefreshLoops() == 0` immediately (the goroutine starts only after acquisition succeeded).
    - `TestSudoSession_ConcurrentStopRunsOneInvalidation`: 8 goroutines call `Stop()` at once (start them behind a closed channel); assert exactly one `sudo -k` and no race under `-race`.
    - `TestSudoSession_StopAfterTheLoopAlreadyEnded`: the loop ended through a refresh failure; `Stop()` returns at once (no deadlock on `done`) and still invalidates when `created`.
  - `TestSudoSession_StdinIsPassedThrough`: start with a non-nil `stdin` reader and assert every sudo call's `RunOptions.Stdin` is that reader (Task 1 may decide it is always `nil`; the parameter stays so the decision is a one-line change).

- [ ] **Step 2: Run them and see them fail** (`nice -n 10 go test -p 4 -run 'TestSudoSession_' ./internal/workspace/`; compile error is the expected failure).
- [ ] **Step 3: Implement** `sudo_session.go` as specified. One goroutine, one `sync.WaitGroup`-free `done` channel; hold no lock around runner calls.
- [ ] **Step 4: Run them with the race detector, repeated** (`nice -n 10 go test -race -count=20 -run 'TestSudoSession_' ./internal/workspace/`, in the background). Expected: PASS, no race, no flake across 20 runs (a flaky goroutine test is a defect in the test or the loop, not a reason to loosen it). Also `go vet ./internal/workspace/` (copylocks: `sudoSession` is only ever used through a pointer).
- [ ] **Step 5: Commit**

```bash
git add modules/pn/internal/workspace/sudo_session.go modules/pn/internal/workspace/sudo_session_test.go
nix fmt -- modules/pn/internal/workspace/sudo_session.go modules/pn/internal/workspace/sudo_session_test.go
pg-hooks fix
git commit -m "pn: sudoSession primes, refreshes and releases sudo credentials"
```

### Task 3: Wire the session into apply

**Files:**

- Modify: `modules/pn/internal/workspace/apply.go` (`applyVerb.run`)
- Modify: `modules/pn/internal/workspace/apply_asuser.go` (`applyAsUser`, `describeSteps`, `dryRunLines`)
- Modify: `modules/pn/internal/workspace/rebuild.go` (`rebuildPlan.sudo`, step constant `stepSudoAuth`)
- Modify: `modules/pn/internal/workspace/applylog.go` (`phaseSudoAuth`, `phaseOrder`)
- Test: `modules/pn/internal/workspace/apply_asuser_test.go`, `modules/pn/internal/workspace/applylog_test.go`, `modules/pn/internal/workspace/rebuild_parity_test.go`
- Test support in `apply_asuser_test.go`: add to `asUserFake` a `sudoSession func(args []string) (exec.Result, error)` field and a case in `asUserFake.Run` for `name == "sudo"` with `args[0]` in `-v`, `-n` (followed by `-v`) or `-k` that calls it (default: success, and `-n -v` reports "no credentials" (exit 1) until the first `-v` has run, as real sudo does); change `rootCalls()` to exclude those three argv forms so the existing assertions (about lines 647, 776, 806, 946, 1000, 1023, 1048, 1238) keep meaning "root steps"; add `sessionCalls()` returning only them. Existing tests that count `rootCalls()` need no edit after that.
- Test fixtures outside `newAsUserEnv` that run apply (`rebuild_parity_test.go` `parityRun`, `info_test.go`, `wrapnix_test.go`, `rebuild_telemetry_test.go`, `rebuild_plan_test.go`, `terminal_guard_test.go`, `rebuildlock_test.go`) already use `asUserFake` after the prerequisite plan; confirm each answers the three sudo forms through it.

**Interfaces:**

- Consumes: `startSudoSession(ctx, runner, out, stdin) (*sudoSession, error)`, `(*sudoSession).Lost()`, `(*sudoSession).Stop()`.
- Produces: `const stepSudoAuth = "sudo_auth"`; `const phaseSudoAuth = "sudo_auth"` with `phaseOrder = []string{phasePrelude, phaseSudoAuth, phaseBuild, phaseActivate, phasePostSteps}`; `rebuildPlan.sudo *sudoSession`; `failedStepSudoAuth = "sudo_auth"` trailer value.

Placement: in `applyVerb.run`, after the skip gate returns "rebuild" and after the apply log is open (so the prompt text reaches the log), and BEFORE `captureHeads`, so a long wait at the prompt cannot make the recorded eval-time HEADs stale. Sequence: `alog.useSteps()` (so `failed_step` is written to the trailer: it is printed only when steps are enabled, and `applyAsUser` calls it too late; make that second call a no-op if already set); `ws.recordStep("apply", false, stepSudoAuth)`; `alog.phase(phaseSudoAuth)`; `sess, err := startSudoSession(ctx, ws.runner, out, nil)` (the last argument is `os.Stdin` if Task 1 decided so); on error `alog.fail(failedStepSudoAuth, err)` and `return fmt.Errorf("apply failed: %w", err)`; `defer sess.Stop()` as a backstop; `p.sudo = sess`.

Release: `applyAsUser` calls `p.sudo.Stop()` itself immediately after the activate step returns (success or failure) so credentials do not stay alive through `nvd`, the fsmonitor restart and `markApplied`; `Stop` is idempotent so the deferred call is a no-op then.

In `applyAsUser`, replace the unconditional line `apply: waiting for sudo (set the system profile, then activate)` with: if `p.sudo.Lost()` print `apply: sudo authentication expired during the build; sudo will ask again now`; else if `p.sudo.Cacheless()` print nothing extra (the earlier warning already said so); else print `apply: running the root steps on the authenticated sudo session`.

`dryRunLines` adds `sudo -v (before the build; kept fresh with sudo -n -v while it runs; released with sudo -k)` as the first extra line. `describeSteps` adds `0 sudo-auth   sudo -v, then sudo -n -v every <interval> while the build runs` to the log header list.

- [ ] **Step 1: Write the failing tests**
  - `TestAsUser_SudoAuthRunsBeforeTheBuildAndBeforeCaptureHeads`: assert the order of recorded steps (`e.steps`) has `sudo_auth` before `capture_heads`, and that the first `sudo` call precedes the build call in `e.f.significant()`.
  - `TestAsUser_RootStepsDoNotPromptAgain`: assert exactly one call with args `[-v]` (the prompting form) in the whole apply, and that set-profile and activate still carry the pinned argv.
  - `TestAsUser_SessionIsReleasedRightAfterActivate`: assert the `sudo -k` call comes after the activate call and before the `nvd` and `markApplied` work (the apply-state file is written after it; check call order against the first `git` call that follows activate).
  - `TestAsUser_SudoAuthFailureTrailerHasFailedStep`: the apply log trailer text contains `failed_step: sudo_auth` (read it with `e.applyLogText(t)` and `trailerValue`).
  - `TestAsUser_SkippedApplyNeverPromptsForSudo`: state "unchanged" (reuse the existing skip-gate fixture); assert zero `sudo` calls.
  - `TestAsUser_SudoAuthFailureFailsBeforeAnyBuild`: `sudo -v` fails; assert error contains `sudo authentication`, no build call, trailer `failed_step=sudo_auth`, nothing marked applied.
  - `TestAsUser_BuildFailureStopsTheRefreshAndInvalidates`: build fails; assert the last `sudo` call is `sudo -k` and refresh calls stop growing.
  - `TestAsUser_CancelDuringBuildStopsTheRefreshAndInvalidates`: cancel `ctx` during the build; same assertions plus no root step.
  - `TestAsUser_LostSessionPrintsNoticeBeforeRootSteps`: fake fails a refresh; assert the notice precedes the set-profile call in `out`.
  - `TestAsUser_ShowNixCommandsListsTheSudoAuthLine`: assert the dry-run output contains the new line first among the extras and that no process ran.
  - Extend `applylog_test.go` so `phaseOrder` is asserted as above and a log records `sudo_auth` elapsed time.

- [ ] **Step 2: Run them and see them fail** (`go test -run 'TestAsUser_(SudoAuth|RootSteps|Skipped|BuildFailure|CancelDuringBuild|LostSession|ShowNix)' ./internal/workspace/`).
- [ ] **Step 3: Implement** as described above.
- [ ] **Step 4: Run the whole module** (background): `nice -n 10 go test -p 4 -race -run '^(Test|Example)' ./...`. Expected: PASS, including `TestAsUser_CommandSequenceExactRootArgvAndOptions` (its significant-call list now starts with the sudo session calls; keep the root argv assertions byte-for-byte) and `TestRebuildParity_VerbSpecificSteps` in `rebuild_parity_test.go` (about line 115), whose expected apply-only list changes to `[stepSkipGate, stepApplyLog, stepSudoAuth, stepCaptureHeads, stepRunCommand, stepPostSteps, stepMarkApplied]`; the shared-prelude parity assertion is unchanged because `stepSudoAuth` is recorded with `shared=false`.
- [ ] **Step 5: Commit**

```bash
git add modules/pn
nix fmt -- modules/pn
pg-hooks fix
git commit -m "pn: apply authenticates sudo before the build and keeps it fresh"
```

### Task 4: Smoke scenario

**Files:**

- Create: `modules/pn/internal/workspace/smoke/scenarios/s45-sudo-session/{pn-workspace.toml,setup.sh,command.txt}` plus the expectation file the other scenarios use (s43 and s44 are taken after the prerequisite plan)
- Modify: `modules/pn/internal/workspace/smoke/smoke_test.go` (a `TestSmoke_S45_*` test and its `runScenario` wiring, as S42 does)
- Modify: `modules/pn/internal/workspace/smoke/smoke_build_as_user.go` (S42's assertions) and the stand-in `sudo` in S42, S19 and S36 `setup.sh`. Today the stand-in numbers EVERY sudo call as `sudo.N.argv` and the assertions pin `sudo.1` = set-profile, `sudo.2` = activate and no `sudo.3`; the new `-n -v`, `-v` and `-k` calls shift those numbers. Change the stand-in to write the three session forms to a separate `sudo-session.log` and number only the other calls, so `sudo.1` and `sudo.2` keep their meaning.

**Interfaces:**

- Consumes: S42's stand-in store and its stand-in `sudo` (now split as above). The smoke tier never runs the real sudo.
- Produces: scenario `s45-sudo-session` with a STATEFUL stand-in `sudo`: `-n -v` exits 1 until a `-v` has run (a marker file), then 0; `-v` creates the marker and exits 0; `-k` removes it. A stand-in that always exits 0 would make the first probe succeed, so `created` would be false and neither `-v` nor `-k` would run. The refresh interval is a Go variable, so the smoke binary built with the `smokeseams` tag sets `sudoRefreshInterval` to 100 ms in `asuser_smokeseams.go`; the stand-in build sleeps about 1 s.

- [ ] **Step 1:** Add the `smokeseams` setting of `sudoRefreshInterval = 100 * time.Millisecond` to `asuser_smokeseams.go` (production binaries keep 60 s: the file only builds under the tag).
- [ ] **Step 2:** Write the scenario: assert `sudo-session.log` starts with `-n -v`, `-v`, `-n -v`, contains at least two further `-n -v` lines, has exactly one `-v`, ends with `-k`; assert `sudo.1.argv` is the set-profile and `sudo.2.argv` the activate; and that the stand-in build ran as the invoking user. Update S42's own assertions as described under Files.
- [ ] **Step 3:** Run the smoke check as in the prerequisite plan, Task 4 step 3. Expected: S19, S36, S42, S45 PASS.
- [ ] **Step 4: Commit**

```bash
git add modules/pn
nix fmt -- modules/pn
pg-hooks fix
git commit -m "pn smoke: sudo session scenario"
```

### Task 5: Docs and ADR

**Files:**

- Create: `docs/adr/0042-apply-authenticates-sudo-before-the-build.md`
- Modify: `docs/adr/index.md`, `docs/adr/0038-apply-builds-as-the-user-and-runs-only-profile-set-and-activate-as-root.md` (status line: "UX consequence superseded by ADR 0042")
- Modify: `modules/pn/README.md` (apply section), `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` (the `activate_command` paragraph)

**Interfaces:**

- Produces: ADR 0042 with: Context (ADR 0038's UX consequence and the operator's quoted use case, provenance Phillip, 2026-11-10); Decision (the algorithm of Task 2 in RFC 2119 words: apply MUST authenticate sudo before capturing HEADs and building; MUST refresh with `sudo -n -v` no less often than every 60 s; MUST NOT write to the apply log from the refresh goroutine; MUST invalidate with `sudo -k` only credentials pn created; MUST NOT keep the session beyond the apply); Alternatives rejected (a root helper process held for the whole build: larger attack surface and breaks "root never evaluates"; Touch ID alone: does not fix ordering; a shell keepalive loop: survives `kill -9`); Consequences (the credential decays within sudo's timeout after `kill -9`; a lock-screen or display sleep does not stop the refresh, a machine sleep does and the prompt returns); the spike output of Task 1; the prompt is held while the single-flight apply lock is held (a second apply waits or fails during it); when pn did not create the credentials (the operator already had a valid ticket) the refresh still extends that ticket by up to the build time and apply never invalidates it; `sudo -k` removes only this terminal's ticket and may find none after a SIGHUP from a closed terminal (as recorded by the spike).

- [ ] **Step 1:** Write ADR 0042, README and SKILL.md changes. In the skill, change the sentence that the prompt comes after the build to: apply asks for sudo first, then builds and activates unattended.
- [ ] **Step 2:** Format to convergence: `nix fmt -- <files>` twice, `pg-hooks run pre-commit <files>`. Expected: pass.
- [ ] **Step 3: Commit**

```bash
git add docs modules/pn/README.md pn-workspace-rules
git commit -m "docs: ADR 0042, apply authenticates sudo before the build"
```

### Task 6: Land, apply and verify on the real machine

- [ ] **Step 1:** Run the Completion Gate of Global Constraints in the set (targeted checks, `pn workspace build --wait=5m`, `pn workspace doctor`), `pg-hooks run pre-land`, then `land-workforest` and `cleanup-workforest`.
- [ ] **Step 2:** Operator runs `pn workspace apply` to install the new `pn` (an agent MUST NOT). File a post-deploy verification bead gated on that apply (`pb:pb-gate-lifecycle`) whose acceptance is the operator's own run of: `sudo -k; pn workspace apply --force`, approve the single prompt, lock the screen for longer than 5 minutes of build time (use `--force` on a change that triggers a real build), return to a finished apply. Evidence to record: exactly one prompt appeared, `~/.local/state/pn` apply log shows `sudo_auth` before `build`, `sudo -n true` fails right after the apply (credentials released).
- [ ] **Step 3:** If the operator's run shows a second prompt, the spike's assumption (Task 1) was wrong for the real path: reopen Task 2 and Task 3 with `Stdin: os.Stdin`.

## Self-review

- Spec coverage: prompt first (Task 3 placement), build as user and own process group (Global Constraints, unchanged), no second prompt over long builds (refresh, Task 2), cancel and cleanup on every exit path (Task 2 `Stop`, Task 3 `defer`, tests for build failure, cancel, cancelled-context release, idempotence), sleep tolerated (Review Focus, ADR), real-sudo behaviour not provable by tests (Tasks 1 and 6).
- Type consistency: `startSudoSession`, `sudoSession.Lost`, `sudoSession.Stop`, `stepSudoAuth`, `phaseSudoAuth`, `failedStepSudoAuth` are used with the same names in Tasks 2 to 4.
- Known unknown: whether macOS keys the credential ticket on the file descriptors the runner gives sudo. Task 1 decides it before code depends on it, and Task 6 confirms it on the real system.
