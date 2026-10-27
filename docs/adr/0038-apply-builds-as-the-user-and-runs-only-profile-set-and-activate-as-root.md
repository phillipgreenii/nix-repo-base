# ADR-0038: `pn workspace apply` builds as the invoking user and runs only the profile set and the activation as root

**Date:** 2026-10-27
**Status:** Accepted (supersedes the "apply runs one root `sudo darwin-rebuild switch`" statements of ADR 0028 and ADR 0030; amends 0017 for the new `activate_command` key)
**Deciders:** phillipgreenii
**Tracking:** `pg2-sdbhh4` (epic), `pg2-sdbhh4.7` (the spike whose recorded outcome this ADR follows), `pg2-sdbhh4.9` (implementation)

## Context

`pn workspace apply` ran one command, `sudo darwin-rebuild switch --flake ...`. That makes root
evaluate the whole flake, so evaluation happened twice per apply cycle (once for `pn workspace
build`, once as root), root kept its own evaluation, fetcher and git caches under `/var/root`, and a
root evaluation of sibling override repos owned by the user hit git "dubious ownership" errors.
The nix JSONL of an apply was also root-only (`/var/log/pg-nix-log-wrapped`), invisible to agents.

Operator ruling, Phillip, 2026-10-26: "I'm ok if we change the apply to run a build as the user and
only root for switch, which should mean it doesn't need to build and can use the config from the
user build." The spike `pg2-sdbhh4.7` (2026-10-27, decision GO) established the mechanism and every
detail below with probes run as the user; nothing root-side was executed (agents MUST NOT run sudo).

## Decision

1. **Opt-in by config.** A new optional `[workspace].activate_command` selects the new flow. With
   the key absent, apply is the single `apply_command` it always was, byte for byte (the nine
   existing tests coupled to `sudo darwin-rebuild switch` keep covering it).
2. **Command sequence** (the new flow, after the unchanged prelude: skip gate, apply log, captured
   HEADs):
   1. _Preflight_ (no process): `activate_command` is set and a per-run `apply-build` directory can
      be created, `/run/current-system/sw/bin/nix-env` is executable, `build_command` expands. If
      any fails, one line is printed and the legacy `apply_command` flow runs BEFORE anything is
      built (an error if `apply_command` is unset).
   2. _Build_, as the invoking user, wrapped like `pn workspace build`: `build_command` plus the
      `--override-input` pairs, with the working directory a fresh private per-run directory
      `R = ${XDG_STATE_HOME:-$HOME/.local/state}/pn/apply-build/run-<random>` (mode 0700, own
      user). `darwin-rebuild build` prints no out path and takes no `--out-link`, and nix writes
      `./result` into the CURRENT directory, so with cwd = `R` the link is private to this run:
      that directory is the pin (the single-flight build key of `pg2-sdbhh4.11` is still held, for
      de-duplication and as defense in depth, but is no longer the only pin).
   3. _Read and validate_ the out path: `os.Readlink(R/result)` ONCE, then, before any root process:
      absolute, `filepath.Clean`-stable, matching `^/nix/store/[0-9a-df-np-sv-z]{32}-[A-Za-z0-9+._?=-]+$`
      (exactly one element under the store), `Lstat(out)` a directory, `Lstat(out/activate)` a
      regular executable file, `Stat(out/sw/bin/darwin-rebuild)` (following symlinks) a regular
      executable file. The INVOKED path is validated, never its realpath (which is a separate store
      package). A missing, non-symlink, relative or invalid result is a distinct error and NEVER a
      second build. The validated string is the only value later steps use.
   4. _Probe_, as the user, unwrapped, cwd `R`, 10 s: `<out>/sw/bin/darwin-rebuild --help`. Accepted
      only on exit 1, empty stdout and a first stderr line `darwin-rebuild ... {a | b | ...}` whose
      group contains `activate`, `switch` and `build` (the script's `showSyntax` writes the usage to
      stderr and exits 1). A failed probe prints
      `apply: built system has no usable 'darwin-rebuild activate' (<reason>); falling back to: <apply_command expansion> (root re-evaluates)`
      and runs the legacy flow (an error naming the reason if `apply_command` is unset).
   5. _Set the system profile_, as root, hard-coded in pn and NOT templated:
      `sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set <out>`.
      `env -i` makes the step independent of sudoers' `env_keep`, which is unreadable; `HOME` is
      explicit because macOS sudo preserves the caller's `HOME`, which would let root create
      `.nix-defexpr` and `.local/state/nix` in the user's home; `nix-env` MUST be invoked by its
      `nix-env` symlink name (it is a multi-call binary: the resolved path fails with
      `unrecognised flag '-p'`). `nix-env --set` of a store path evaluates nothing.
   6. _Activate_, as root: `sudo <out>/sw/bin/darwin-rebuild activate` (the `activate_command`
      expansion), through the INVOKED path, because the script derives
      `systemConfig=$(readlink -f "${0%*/sw/bin/darwin-rebuild}")` from `$0`. `activate` is exactly
      the tail of `switch` (activate-user when present, then `"$systemConfig/activate"`), so the
      nix-darwin activation checks inside `<out>/activate` are not skipped.
   7. Remove `R` (deferred, also on failure and cancel; a stale `R` from a SIGKILL is pruned when
      older than 24 h at the start of the next apply), then nvd, the fsmonitor restart and
      `markApplied` with the pre-build HEADs, only if every step succeeded.
3. **Config.** `activate_command` takes the five existing placeholders plus `{out}`, valid ONLY
   there (parse-time check; in `build_command`/`apply_command` `{out}` stays an unknown-placeholder
   error). `{out}` is substituted after the argv split. `argv[0]` MUST be `sudo` (no sudo options)
   and the activate program MUST be one element beginning with `{out}/`; v1 value:
   `sudo {out}/sw/bin/darwin-rebuild activate`. The probe runs when that element is
   `{out}/sw/bin/darwin-rebuild`. `activate_command` requires a `build_command` that leaves
   `./result` in its cwd (a `darwin-rebuild build`). `apply_command` stays set as the fallback.
   `pn-workspace-toml-enforce` takes `--activate-command` (key-scoped: empty leaves the key
   untouched and never deletes it) and `EnforceKeys` a matching parameter.
4. **Failure and exit codes.** Errors are wrapped with `%w`, so `cli.ExitCode` returns the failed
   subprocess's own exit code; the steps are told apart by error text (`apply failed: build: ...`,
   `apply failed: read build result: ...`, `apply failed: probe: ...` (no fallback available),
   `apply failed: set system profile: ...`, `apply failed: activate: ...`) and by a new apply-log
   trailer line `failed_step: build|read-result|probe|set-profile|activate|none` (new flow only;
   `exit_status` is that step's code, `-1` a signal, `none` when no process failed). The header
   records every step's command. `activate` never runs after a failed set-profile. An activate
   failure leaves the profile advanced and `/run/current-system` not updated, as `switch` does;
   recovery is `sudo darwin-rebuild --rollback` or re-running apply (the user build is then a cache
   hit). `markApplied` is never written on a failure. A failed legacy fallback is recorded as
   `failed_step: activate`.
5. **Cancel.** The build runs in its own process group (`pg2-sdbhh4.20`), so a pn-initiated cancel
   reaches `nix build | jq` and `R` is removed; a cancel between the build and the first root step
   aborts before any root process starts (also when it lands during the probe: no fallback). Once
   set-profile has started, both root steps run to completion (`context.WithoutCancel`) and the
   cancel is reported afterwards (without `markApplied`). The two sudo steps NEVER get their own
   process group: sudo reads the password from the terminal and a background group would be stopped
   by SIGTTIN; a terminal Ctrl-C still reaches them through the foreground group as with `switch`.
6. **Telemetry.** The user build is wrapped as `pn workspace build` is (user wrapper form,
   `--log-dir <state>/pn/nix-logs`), so the nix JSONL of an apply now lands in the user's log
   directory and `/var/log/pg-nix-log-wrapped` gets nothing for it. The probe and both root steps run
   with `WrapNix` false (they run no evaluation; as `sudo env ...` and `sudo <absolute path>
activate` they would not be recognized anyway). `pn.exec` is named `darwin-rebuild` for the build
   and `sudo` for each root step; `pn.exec.subcommand` normalizes to `nix-env` and `darwin-rebuild
activate` (the `pg2-sdbhh4.13` normalizer skips `sudo`, `env`, `-i`, `NAME=VALUE` and takes the
   basename of an absolute `argv[0]`).

## Consequences

- One evaluation per apply, as the user, with the user's caches; no root-side evaluation, no
  "dubious ownership" class of failures, agent-visible nix logs for apply.
- UX change: the sudo prompt (password or Touch ID) now comes AFTER the build, so pn prints
  `apply: waiting for sudo ...` first. A long build no longer needs the operator present at the start.
- Residual risks, accepted and documented: a same-uid process that knows `R` can replace the link
  during the window, and a trusted user can import arbitrary store paths; root already trusts
  user-owned flake sources and `pn-workspace.toml` in the old flow, so the trust level is unchanged.
  The operator's real sudoers cannot be read by agents: a rule keyed on the exact `darwin-rebuild
switch` command would reject the new argv (the post-deploy check covers it).
- Tests: fake-runner tests pin the sequence, the exact root argv and environment, the run directory
  lifecycle, validation, fallback, failure mapping and cancel; a smoke scenario (S42) drives the real
  `pn` binary against stand-ins; a drift guard reads the REAL installed darwin-rebuild script and
  asserts the four facts the root steps depend on; a real-tool test runs the probe through a fixture
  whose `sw/bin/darwin-rebuild` symlinks to the installed script; the wrapper module's contract test
  and pn's `exec/wrap_test.go` share one golden argv file.
- Migration: land with the key absent (no change); one apply installs the new pn and enforcer; the
  consuming repo then enables `activate_command` (after a relock bringing in the new enforcer, and
  after `pg2-sdbhh4.18` makes `build_command` target the same `#<hostname>` apply used); the enforced
  toml then has a new SHA, so the next pn command needs `pn workspace allow`. Rollback: clear
  `activate_command` (the key-scoped enforcer never deletes an existing key, so the operator edits the
  file and re-allows); `apply_command` is untouched, so the legacy path is one key away.

## Supersedes and corrects

- ADR 0028, "Item 3" (the `.20` note that apply's `sudo darwin-rebuild switch` keeps the old
  cancel limitation): with `activate_command` set the build is a user command covered by the process
  group fix, and only the two short root steps keep the documented limitation.
- ADR 0028 and ADR 0030 statements that an apply's nix run is a root wrapper run logging to
  `/var/log/pg-nix-log-wrapped`: true only for the legacy flow and the fallback.
- `modules/pn/README.md` (the apply-log and wrapper sections) and the `pn-workspace-rules` skill
  state the new behavior.
