# ADR-0042: `pn workspace apply` authenticates sudo before the build and keeps it fresh

**Date:** 2026-11-10
**Status:** Accepted (supersedes the UX consequence of ADR 0038, "the sudo prompt now comes AFTER the build")
**Deciders:** phillipgreenii

## Context

ADR 0038 made apply build as the invoking user and run only two steps as root. A consequence it
recorded was that the sudo prompt (password or Touch ID) came after the build, so pn printed
`apply: waiting for sudo ...` once the build had finished. For a long build that is the wrong way
round: the operator had to be present at the end, not the start.

Operator rulings, Phillip, 2026-11-10, verbatim: "i used to be able to trigger an apply, it would
quickly prompt for sudo and i would submit the password. then i could go do something else, even
walk away from the computer, and the apply would be back by the time i return"; "my use case would
be if trigger apply, approve sudo, lock screen, go to lunch, come back and have apply complete";
"i still want the build to work like today, ie, as my user, not root"; "make sure the sudo
refreshes are correct and will be cleaned up correctly"; "make sure that the goroutines follow go
best practices of being cancellable and they do the correct thing and there are tests verifying".
The operator also ruled that a machine asleep is not a case to solve: after the machine wakes, a
further prompt is fine.

sudo caches its authentication for a timeout (five minutes by default), and a build can outlast it.
Authenticating first is therefore not enough: the credentials must be kept warm while the build
runs.

## Decision

1. Apply MUST authenticate sudo before it captures the repository HEADs and before it builds. The
   authentication MUST come after the skip gate: a skipped apply (nothing changed) MUST NOT prompt.
   It MUST fail the apply, before anything is built, when the password is wrong or the prompt is
   cancelled (`failed_step: sudo_auth`).
2. The session is acquired with three hard-coded calls, never templated: `sudo -n -v` (does the
   operator already hold credentials?), `sudo -v` (the one prompt, only when the probe failed),
   `sudo -n -v` again (verify that sudo cached them). When sudo caches nothing at all, apply MUST say
   so once and fall back to prompting after the build.
3. While the build runs, the session MUST be refreshed with `sudo -n -v` no less often than every
   60 s.
4. The refresh MUST be one cancellable goroutine:
   - it is started only after acquisition succeeded, and its context is derived from the apply
     context, so cancelling the apply stops it even if `Stop` is never called;
   - it selects on `ctx.Done()` and a `time.Ticker` that is stopped with `defer`;
   - every call it makes has its own deadline and uses the loop context, so cancel interrupts a call
     in flight;
   - it signals exit by closing a `done` channel (`defer close(done)`) that `Stop` waits for;
   - it MUST NOT write to the apply output (the log tee is not safe for concurrent writes) and
     MUST NOT block on a send; it shares state only through atomics;
   - the interval is copied into the session at start.
5. Credentials pn created (the probe failed, so pn ran `sudo -v`) MUST be invalidated with `sudo -k`
   on every exit path: right after the activate step, and again through a deferred backstop that
   covers a failed build, a cancel and an error. The `sudo -k` call MUST use a context that survives
   the apply's cancellation and a bounded deadline. Credentials the operator already held MUST NOT be
   invalidated.
6. The session calls stay in pn's own process group (a background group would be stopped by SIGTTIN
   when sudo reads the terminal). The build stays a user process in its own process group; root never
   evaluates. Root-executed argv is unchanged.
7. The apply log trailer records the time spent at the prompt as `sudo_auth_seconds` and the phase as
   `sudo_auth`.

## Operator spike

Run by the operator 2026-11-10 (macOS, zsh login shell, one prompt), recorded verbatim from the plan:

> **Result, run by the operator 2026-11-10 (macOS, zsh login shell, one prompt):** `refresh from background group: exit=0`, `use: exit=0`, `release: exit=0`, `after release: exit=1`. The `prime` and `refresh` lines printed `exit=` blank because `PIPESTATUS` is a bash variable and the shell was zsh (use `bash -c '...'` or zsh's `pipestatus`). Reading: after the initial `sudo -k`, only the prime (`sudo -v </dev/null 2>&1 | cat`) could have created the ticket the later calls found, so a prime with the runner's file-descriptor shape works, and a background-group refresh and the release behave as designed. **Decision: Task 3 passes `Stdin: nil` (no `os.Stdin`).** Follow-up run in bash (operator, same day): `prime: exit=0`, `refresh: exit=0`. The spike is complete and every expected value held.

## Alternatives rejected

- **A root helper process held for the whole build.** A long-lived root process is a larger attack
  surface and breaks the property ADR 0038 established, that root never evaluates.
- **Touch ID alone.** It makes the prompt quicker but does not fix the ordering: the prompt would
  still come after the build.
- **A shell keepalive loop** (`while true; do sudo -n -v; sleep 60; done &`). It survives `kill -9`
  of pn and keeps refreshing with nobody to stop it. A goroutine dies with the process.

## Consequences

- After `kill -9` of pn nothing keeps refreshing, and the credentials decay within sudo's timeout;
  they are not invalidated. This is documented, not fixed.
- A machine sleep longer than sudo's timeout loses the credentials, and the prompt returns before the
  root steps. The operator accepted this. Apply prints one notice before the root steps. A lock
  screen or display sleep does not stop the refresh.
- A sudoers `timestamp_timeout` below 60 s is treated as a lost session at worst (the notice may be
  missing and sudo prompts), which is benign.
- The prompt is held while the single-flight apply lock is held, so a second apply waits or fails
  during it.
- When the operator already held valid credentials, the refresh extends that ticket by up to the
  build time and apply never invalidates it.
- `sudo -k` removes only this terminal's ticket, and may find none after a SIGHUP from a closed
  terminal.
- Real-sudo behaviour cannot be proven by tests: the smoke tier uses a stateful stand-in, and the
  real behaviour is checked by the operator-run spike above and a post-deploy check.
