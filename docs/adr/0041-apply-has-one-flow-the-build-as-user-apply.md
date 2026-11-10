# ADR-0041: `pn workspace apply` has one flow, the build-as-user apply, on darwin and NixOS

**Date:** 2026-11-10
**Status:** Accepted (supersedes ADR 0038 decision 1 "Opt-in by config", the fallback clauses of its decisions 2.1 and 2.4, and its migration paragraph; amends 0017 by removing the `--apply-command` flag)
**Deciders:** phillipgreenii

## Context

ADR 0038 added the build-as-user apply behind an opt-in key, `[workspace].activate_command`, and kept
the single `apply_command` (`sudo darwin-rebuild switch ...`) as the flow for every config without
that key, and as the fallback when the preflight, the per-run directory or the probe failed. The
reason was a safe rollout: the new flow could be enabled one machine at a time and rolled back by
clearing one key.

Keeping both made apply harder to reason about: two flows, three silent fallbacks to a root build
(which reintroduces the root evaluation, the "dubious ownership" failures and the root-only nix
logs that 0038 removed), and a darwin-only design.

Operator rulings, Phillip, 2026-11-10: "do we need to have build-as-user and the legacy command? it
seems the legacy should be removed." and "remove all of the legacy, leaving it in is confusing. if
an unknown arg/flag fails today, it should fail with --apply-command. apply can't be darwin only. it
should work on nixos or darwin."

The rationale of 0038 stands (operator ruling, Phillip, 2026-10-26: "I'm ok if we change the apply
to run a build as the user and only root for switch, which should mean it doesn't need to build and
can use the config from the user build.").

## Decision

1. **`activate_command` is required.** Apply MUST have `[workspace].activate_command`. Without it
   apply MUST fail with `workspace.activate_command is not set in pn-workspace.toml`, naming both
   supported forms.
2. **No fallbacks.** The three former fallbacks to `apply_command` (preflight failure, per-run
   directory failure, probe failure) MUST be errors that name the reason and run before any root
   step. Apply MUST NOT run a root build under any condition.
3. **Two platforms through one Strategy.** Apply MUST support darwin and NixOS through the
   `activationPlatform` Strategy (`modules/pn/internal/workspace/apply_platform.go`). The
   Strategy MUST be selected by the program named in `activate_command`, never by the host or by
   `{builder}`:
   - `sudo {out}/sw/bin/darwin-rebuild activate` selects darwin: root home `/var/root`, the
     `sw/bin/darwin-rebuild` check and the `--help` usage probe.
   - `sudo {out}/bin/switch-to-configuration switch` selects NixOS: root home `/root`, a
     `bin/switch-to-configuration` file check and no process probe.

   Any other program MUST be an error. Everything else of ADR 0038 (user build in a private per-run
   directory, store path validation, the hard-coded `nix-env --set` step, the activate step, cancel
   semantics, telemetry, `failed_step`) is unchanged and shared by both platforms.

4. **`apply_command` does not exist.** The `ApplyCmd` field, the `apply_command` key handling and the
   enforcer flag `--apply-command` are removed with no deprecation. `pn-workspace-toml-enforce
--apply-command` fails as any unknown flag does, and `EnforceKeys` no longer takes the parameter.
   A stale `apply_command` line in a `pn-workspace.toml` is an ignored unknown key, not an error.
5. **A root build is not reachable from `pn`.** The only root processes are the set-profile and
   activate steps.

## Consequences

- One flow to document, test and debug; no silent change of trust level (root evaluation) when a
  probe fails; nix logs of an apply are always in the user's own `nix-logs` directory.
- the dry-run option (`ShowNixCommandsOnly`, library only: pn has no CLI flag for it) now fails on a machine without `/run/current-system/sw/bin/nix-env`,
  because the preflight in `extendPlan` runs before the dry-run gate.
- In the apply log trailer, `exit_status` for a failed probe is the probe's exit code only when the
  probe exited non-zero; it is `none` when the probe could not run, was signalled, timed out, or
  exited 0 with unusable output.
- A config without `activate_command` can no longer apply until the key is added. Rollback to a
  root build is no longer a config change.
- The enforcer rewrites of `pn-workspace.toml` (and `pn workspace init`, `doctor --fix`, workforest
  creation) re-marshal through a struct without the field, so they drop a stale `apply_command`
  line. Any rewrite changes the file's SHA and re-arms the trust gate, so the operator runs
  `pn workspace allow` once afterwards.
- The NixOS path rests on the documented system layout (`<out>/activate`,
  `<out>/bin/switch-to-configuration`, root home `/root`) and is covered by fake-runner tests only.
  It is UNVERIFIED on a NixOS host until the spike in `NIXOS_TODO.md` has run; its assumptions are
  marked `TODO(nixos)` at their code sites.

## NixOS spike result

Not yet run on a NixOS host. The Linux agent MUST paste the output of the spike in `NIXOS_TODO.md`
here (expected: `layout ok`, a `nix-env` symlink under `/run/current-system/sw/bin`, and root's home
`/root`), or record the corrections found.

## Supersedes

- ADR 0038 decision 1, "Opt-in by config": with the key absent apply no longer runs `apply_command`.
- ADR 0038 decision 2.1 (preflight) and 2.4 (probe) fallback clauses: the legacy `apply_command`
  flow and the announcement `falling back to: <apply_command expansion> (root re-evaluates)` no
  longer exist; those failures are errors. Decision 3's sentence that `apply_command` stays set as
  the fallback and decision 4's note on a failed legacy fallback are likewise void.
- ADR 0038, the migration paragraph of its Consequences ("land with the key absent ... the legacy
  path is one key away"): the key is required and there is no legacy path.
- ADR 0017: the `EnforceKeys` signature with an `applyCommand` parameter and the
  `--apply-command` flag. ADRs 0015, 0018, 0019, 0024 and 0028 remain dated records and are not
  rewritten.
