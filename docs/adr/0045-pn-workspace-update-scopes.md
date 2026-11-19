# ADR-0045: `pn workspace update` takes a `--scope`: siblings, flake or full

**Date:** 2026-11-19
**Status:** Accepted (amends [ADR-0009](0009-pn-workspace-update-worktree-isolation.md) and [ADR-0023](0023-workspace-push-owns-sibling-propagation.md))
**Deciders:** phillipgreenii (implemented by Claude)

## Context

`pn workspace update` had two behaviours: plain update ran each repo's `./update-locks.sh`, and
`--siblings-only` relocked only the workspace-sibling flake inputs. The operator wants three levels
of control (verbatim): "what i want to have is to be able to only update sibling references OR
sibling references and the flake lock OR sibling references, flake lock, and run the full
update-locks.sh." The middle level did not exist, and the skills would default to it (verbatim):
"by default, lets have the skills run flake scope." It runs weekly on the weekend, so extra build
work is acceptable (verbatim): "we would only do the flake one per week, on the weekend, so if it
build extra stuff, that isn't terrible. but we should have something which checks the metrics/traces
to see why it took so long." That last part is `pn workspace explain` and is not decided here.

Plain update is also not a superset of `--siblings-only`. `update-locks.sh`'s `nix-flake-update` step
sits behind the 12-hour step stamp (`UL_STALE_SECONDS=43200`) and runs without `--refresh`, so
within 12 hours of the last run neither the sibling inputs nor nixpkgs move. A caller who ran the
full update could still be left with a stale sibling pin.

## Decision

1. **`--scope siblings|flake|full`** (`UpdateOptions.Scope`; the zero value is `full`):

   | Scope      | What runs per repo                                                    | `update-locks.sh` | `UL_LIB_DIR` |
   | ---------- | --------------------------------------------------------------------- | ----------------- | ------------ |
   | `siblings` | `propagateWorkspaceEdges` (`nix flake update --refresh <sibling>...`) | skipped           | not needed   |
   | `flake`    | one `nix flake update --refresh` over every input, then pn's commit   | skipped           | not needed   |
   | `full`     | `./update-locks.sh` as before, then the `flake` relock                | runs              | resolved     |

   Every scope stays local-only (ADR 0023): nothing is pushed. Worktree isolation, topological order,
   the rebases and the local integration are the same in every scope.

2. **Defaults.** The `pn` CLI default stays `full`, so existing callers see no change beyond the
   trailing relock. The skills (`/pn-workspace-update`, `pnwf update-relock`) default to `flake`.
   `doctor --fix` uses `siblings`. `pn workspace upgrade` uses `full`.

3. **Order in `full`: the script first, the relock last.** The script runs exactly as today, with
   today's step order and setup cost in every consumer, and the all-inputs relock then closes the
   superset gap above. When the stamp is stale the relock is a fast near no-op. Running the relock
   first was rejected: it moves the bumped-lock `resolve-nrb-ref` fetch and the dev-shell rebuild
   into every consumer's setup and inverts the step order in nix-support-apps and nix-overlay.

4. **Failure handling of the all-inputs relock** (`flake` and `full` only). pn has no ADR 0020
   transient classifier, so it uses one retry instead:
   - A failed `nix flake update` is retried once. `siblings` keeps today's single attempt through
     `propagateWorkspaceEdges`: no retry, no no-space abort, and its first failure fails the repo at
     `relock-siblings`.
   - A failure whose output contains `No space left on device` is not retried and aborts the whole
     run, the same environmental abort `update-locks.sh` signals with exit 77. `project_result` and
     `run_end` carry `failed_step: relock-flake`.
   - A lock that could not be restored after a failure fails the repo at `relock-flake` in every
     scope, because the repo is left dirty.
   - A context cancel ends the repo (`relock-flake`, level `error`) and the run (`update interrupted`).
   - Any other failure after the retry: in `flake` scope the repo fails at `relock-flake`. In `full`
     scope the repo still integrates its `update-locks.sh` commits and the failure is a warning
     (`project_result` level `warn`, `relock_failed: true`, a `⚠` line on stdout), because discarding
     a long `update-locks.sh` run over a relock blip is worse than one week of unmoved inputs.

5. **Observability.** `run_start` records `scope` and `root`; the `pn.verb` span carries
   `pn.update.scope`; each relock is a `pn.relock` span with a `pn.relock.kind` of `siblings` or
   `flake`.

## Alternatives considered

- **Filter steps inside `update-locks.sh`** (for example `UL_ONLY_STEPS=nix-flake-update`). It would
  keep the library's rollback and failure classification, but every repo would still pay
  `resolve-nrb-ref`, `determine-ul-lib-dir`, the `nix develop` re-exec and the hook-bundle install,
  which is exactly the cost the `flake` scope exists to avoid.
- **Relock first, then the script** (decision 3). Rejected for the setup-cost reasons above.
- **A guard against expensive nixpkgs bumps.** Operator ruling (Phillip, 2026-11-18): "yes; i would
  like to do more, but there doesn't seem to be a simple check, so we will contiue as is for now."
  The weekly run absorbs them.

## Consequences

- **`--siblings-only` is a permanent alias** for `--scope siblings`, not deprecated: older doctor
  docs, the tool approver and existing habits use it. Combining it with a different explicit
  `--scope` is a usage error (exit 2). Every other bad `--scope` input (unknown, empty, missing) also
  exits 2 before any hook runs.
- `doctor --fix` for `flake-lock-fresh` runs `--scope siblings`; its manual hint says the same.
- The Homebrew lock and the other per-package locks (Go, uv, npm, nvfetcher, action pins) refresh only
  in `full` scope. nix-personal ADR 0060's "`pn workspace update` MUST refresh the lock" therefore
  holds for the CLI default but not for the skills' `flake` default.
- `flake` needs no `UL_LIB_DIR`, so like `siblings` it runs headless; only `full` resolves it.
- `pn workspace upgrade` gains the trailing relock, because it runs `full`.
- There is no ADR 0020 transient classification for pn's relock: one retry instead, for the
  all-inputs relock only.

## Amendments to earlier ADRs

- **ADR 0023, Decision item 1.** Plain update's relock is no longer only `update-locks.sh`'s
  `nix flake update`: `full` adds a trailing all-inputs relock. The rest of item 1 stands: no
  special-casing of workspace siblings in `full` and `flake`, and no push.
- **ADR 0023, "`update --siblings-only` is retained".** It is now the alias for `--scope siblings`.
- **ADR 0009.** `UL_LIB_DIR` is skipped for `siblings` and for `flake`, not only for `--siblings-only`.
