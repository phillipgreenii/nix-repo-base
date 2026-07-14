# ADR-0020: `update-locks` failure classification (transient / resource / hard)

**Date:** 2026-07-14
**Status:** Accepted
**Deciders:** phillipgreenii

## Context

On 2026-07-14 two consecutive full-workspace `pn workspace update` runs failed all
remaining repos. The root cause was **disk exhaustion**: agent-support's `gomod2nix`
regeneration pulls large Go toolchains (~1.4 GiB unpacked per module) and filled the
shared APFS container, so a step died with `error: write of N bytes: No space left on
device`. The step exited a generic `1`, which `ul_run_step` classified as a hard
failure, so `update-locks.sh` exited non-zero, the repo was marked failed, and — in the
worktree flow — its already-successful steps were discarded too. `pn` then marched on to
the next repo and hit the same full disk.

The framework already had a deferral mechanism (`ul_attempted` → exit `75` /
`UL_RC_ATTEMPTED`: roll back, stamp, keep the run green) but it is **opt-in** and the
wrapped tools (`nix flake update`, `nvfetcher`, `gomod2nix`, `uv`) never opt in — they
exit generic non-zero on _any_ error, transient or not. So two distinct environmental
failure modes were indistinguishable from a genuine broken lock:

1. **Transient external-source failure** (couldn't reach cache.nixos.org / GitHub /
   PyPI): should be _tracked_ and retried, not fail the run; other repos/sources may
   still succeed.
2. **Local resource exhaustion** (ENOSPC): deferring is pointless — every remaining repo
   hits the same wall — so the _whole run_ should stop with a clear, actionable message.

The opt-in-`75` design comment is deliberate: "a real tool failure is never misread as a
deferral." Any classification we add must preserve that — a false "transient" **silently
skips a real update**, which is worse than a visible failure.

## Decision

`ul_run_step` captures each step's stderr and, on a non-zero (non-`75`) exit, classifies
it with `ul_classify_step_failure <stderr_file>` → `resource` | `transient` | `hard`:

- **transient** — a curated allowlist of **transport-scoped** connectivity signatures
  (DNS/TLS/timeout/connection-reset, git remote transients, HTTP 5xx, 429). Roll back
  content, write **no stamp** (so it retries next run rather than being skipped until the
  TTL), count `_UL_STEPS_TRANSIENT`, keep the run passing. Because `update-locks.sh` still
  exits 0, a repo's _successful_ steps still land and integrate.
- **resource** — **only** `No space left on device` / `ENOSPC` (checked first, so it beats
  co-occurring network noise). Roll back, print an actionable message, and `exit
UL_RC_ABORT` (**77**). A wedged/unreachable nix daemon in `ul_setup` also exits 77.
- **hard** — everything else (unchanged): roll back, record the failed step, `ul_finalize`
  exits 1. This deliberately includes genuine **broken pins** (HTTP **4xx**), **OOM**
  (`cannot allocate memory` — too ambiguous to abort the world or silently defer), and all
  provenance/verification failures (hash mismatch, cosign/attestation).

`pn` recognizes `update-locks.sh` exit **77** (`ulExitAbort`) in both update flows and
**stops the whole run** — the aborting repo's worktree/branch are left for inspection, no
later repo is attempted, and `run_end` reports `status: "aborted"` with an error that says
so (never reported as success). Any other non-zero update-locks exit keeps the existing
per-repo "continue and aggregate" behaviour.

### Bash ↔ Go exit-code contract

| update-locks.sh exit | meaning                             | pn behaviour                          |
| -------------------- | ----------------------------------- | ------------------------------------- |
| `0`                  | repo ok (transient steps invisible) | integrate the repo                    |
| `77` (`UL_RC_ABORT`) | environmental / resource abort      | stop the whole run (`statusAborted`)  |
| other non-zero       | genuine failure                     | mark repo failed, continue (as today) |

Step-command exit codes seen _inside_ `ul_run_step`: `0` = success; `75`
(`UL_RC_ATTEMPTED`) = deferred (stamp written); any other non-zero → classified as above.

`77` is `EX_NOPERM` in sysexits terms, an imperfect fit, but it is reserved here purely as
a pn-internal sentinel (clear of `1`/`2`, Nix's `100`/`101`, and `75`). No tool in the
pipeline surfaces 77 as `update-locks.sh`'s own exit code.

## Consequences

- **Conservative by construction.** The transient allowlist is small and transport-scoped;
  the default is always `hard`. Signatures are extended deliberately (ADR amendment), not
  broadened speculatively. A genuine failure is never silently skipped.
- **Partial progress survives a transient blip.** A repo's good steps land even when one
  step can't reach its source; the failed step retries next run (no stamp).
- **Consumers are unaffected.** They call `ul_run_step <cmd>` + `ul_finalize`; the lib owns
  classification. `verify-provenance.sh` (nix-overlay) is a step whose genuine failures
  carry no network signature, so they correctly stay `hard`.
- **Known gap — silent transient churn.** A step misclassified as transient every run keeps
  the run green with only a `Transient: N` summary line; `pn` sees exit 0 and cannot see the
  transient count across the bash↔Go boundary. Accepted for now; a future amendment may
  surface the transient count to `pn` (e.g. a `project_result` field or a warn event).
- **Archived `note` may omit the ENOSPC line.** `CommandError.Error()` keeps the first 512
  bytes of stderr; ENOSPC usually appears at the end of a long build log. The live abort
  banner and streamed message carry the cause, so only the archived note degrades.

## Testing

- Bash: `ul_classify_step_failure` signature table; `ul_run_step` transient (rollback, no
  stamp, run stays green), transient-then-success (good step still commits), resource
  (exit 77), and `ul_setup` daemon abort — `lib/tests/test-update-locks-lib.bats`.
- Go: worktree and in-place flows abort on update-locks exit 77, stop before later repos,
  and return an error naming the abort — `modules/pn/internal/workspace/*_test.go`.
