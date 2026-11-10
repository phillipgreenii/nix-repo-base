# Remove the Legacy `apply_command` Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pn workspace apply` has exactly one flow, the build-as-user flow of ADR 0038, and fails loudly instead of silently falling back to a root build.

**Architecture:** `activate_command` becomes required for apply. The three fallbacks (preflight failure, per-run directory failure, probe failure) become errors. `applyFallback`, the non-as-user branch of `applyVerb.run`, the `apply_command` field and template, the `ApplyCmd` option, the enforcer's `--apply-command` flag and every doc and test mention of the legacy flow are deleted outright, with no deprecation window: an unknown flag fails as any unknown flag does, and a leftover `apply_command` line in a toml is an unknown key, which the decoder ignores like any other. Because the as-user flow is darwin-only today, the platform-specific parts of it (the activate program checks, the probe, root's home directory) move behind a small Strategy, `activationPlatform`, selected by the builder (`darwin-rebuild` or `nixos-rebuild`), so apply works on both. The enforcer flag, its provisioning in `nix-ziprecruiter` and the pn change land together as one coordinated workforest set. This plan is the prerequisite of the early-sudo plan (`2026-11-10-pn-apply-early-sudo-auth.md`), which then has one flow to change.

**Tech Stack:** Go (`modules/pn`), cobra CLI, `x/gittest` fixtures, the `pn` smoke tier (`internal/workspace/smoke`), markdown ADRs.

**Spec:** Operator request (Phillip, 2026-11-10): "do we need to have build-as-user and the legacy command? it seems the legacy should be removed" followed by "ok, i want 2 plans. one to remove legacy." Background and rationale: `docs/adr/0038-apply-builds-as-the-user-and-runs-only-profile-set-and-activate-as-root.md` (decision 1 "Opt-in by config", the fallbacks in decisions 2.1 and 2.4, and the migration paragraph "apply_command is untouched, so the legacy path is one key away").

All paths below are relative to `<repo-base>` = `/Volumes/gitrepos/phillipg/nix-repo-base` unless they start with `nix-ziprecruiter/` (= `/Volumes/gitrepos/phillipg/nix-ziprecruiter`). Go commands run in `<repo-base>/modules/pn`. Inside the workforest set the same paths are under `<set>/nix-repo-base`, `<set>/nix-ziprecruiter` and `<set>/nix-agent-support` (`<set>` = `/Volumes/gitrepos/phillipg/.workforests/pn-apply-one-flow`).

## Global Constraints

- Process (skills `pn-workspace-rules:pn-workspace-rules`, `fork-workforest`, `validate-workforest`, `land-workforest`, `cleanup-workforest`): the change spans three repos (`nix-repo-base`, `nix-ziprecruiter`, `nix-agent-support`), so it is done in a coordinated workforest set, not a plain worktree. From the canonical root `/Volumes/gitrepos/phillipg`: `pnwf fork-preflight pn-apply-one-flow --repos nix-repo-base,nix-ziprecruiter,nix-agent-support`, then `pn workspace workforest add pn-apply-one-flow --repos nix-repo-base,nix-ziprecruiter,nix-agent-support`, then `cd <set>` and `export PN_WORKSPACE_ROOT="$PWD"` (the branch name is one path segment). Never `git checkout -b` on a canonical clone.
- Validation before declaring done (Completion Gate): targeted checks for the touched repos (`nix build .#checks.<system>.<name> -L` in the background, naming the pn go-tests check and the smoke check); `pn workspace flake-check` in the set via `validate-workforest`; because the change reaches the assembled system, `pn workspace build` in the set (background, `--wait=5m`); then `pn workspace doctor`. An agent MUST NOT run `pn workspace apply`.
- Landing: `land-workforest` (topological order: `nix-repo-base` first), then `cleanup-workforest`. Pushing and relocking are the operator's later, separate steps and block nothing.
- Go tests: `nice -n 10 go test -p 4 ./internal/workspace/... ./internal/cli/... ./cmd/...` run in the background with an explicit timeout (`bgrun`), never in the foreground.
- No agent runs `sudo`, `pn workspace apply` or `pn workspace update`; the operator does. Every test uses the fake runner (`asUserFake`, `cmdruntest`-style) or the smoke stand-ins.
- No bead ids in any committed file (commit messages, comments, docs, test names).
- Commit flow: `git add <files>`; `nix fmt -- <files>`; `pg-hooks fix`; `git commit`. Run `pg-hooks run pre-land` before landing.
- Root-executed argv is unchanged: `setProfileArgv(out)` and the `activate_command` expansion stay byte-for-byte as pinned by `TestAsUser_CommandSequenceExactRootArgvAndOptions`.
- Failed-step values of the apply-log trailer (`failedStepBuild`, `failedStepReadResult`, `failedStepProbe`, `failedStepSetProfile`, `failedStepActivate`) keep their strings.
- `apply` MUST work on NixOS and on darwin (operator ruling, Phillip, 2026-11-10: "apply can't be darwin only. it should work on nixos or darwin."). The platform is chosen from the program named in `activate_command`, which is explicit configuration and independent of the host and of `{builder}` detection (`defaultBuilder()` returns an empty builder on a non-NixOS Linux host, and no CLI flag sets one): `{out}/sw/bin/darwin-rebuild` selects darwin and `{out}/bin/switch-to-configuration` selects NixOS; any other program fails apply, before any process runs, with an error naming the two accepted programs. Facts the NixOS strategy relies on, UNVERIFIED on this machine (a NixOS host is needed; Task 2b has a spike and a drift guard that skips when the files are absent): `nixos-rebuild build` leaves `./result` in its working directory; the system closure has an executable `bin/switch-to-configuration` and `activate`; root's home is `/root`; `/run/current-system/sw/bin/nix-env` exists; the set-profile step is the same `nix-env -p /nix/var/nix/profiles/system --set <out>`; activation is `sudo <out>/bin/switch-to-configuration switch` (the `activate_command` for NixOS).
- No deprecation window and no compatibility shim: nothing in `pn` mentions `apply_command` or the legacy flow after this plan, except the ADR that records the removal.

## Review Focus

- A `pn-workspace.toml` that still has `apply_command` but no `activate_command`: apply MUST fail with the message naming `activate_command`, before any process runs, not silently build as root.
- A `pn-workspace.toml` with both keys, as the workspace has today: apply MUST run the as-user flow and MUST NOT mention or run `apply_command`.
- A probe failure (built system lacks `darwin-rebuild activate`): apply MUST fail, run no root step, and record `failed_step=probe`.
- `pn workspace upgrade`, which forwarded `ApplyCmd` into apply: it MUST still apply through the one flow.
- `--show-nix-commands` with no `activate_command`: it MUST fail the same way, not print a legacy command.
- NixOS: with `activate_command = "sudo {out}/bin/switch-to-configuration switch"` the build runs as the user, the out path is validated with the NixOS file checks (not `sw/bin/darwin-rebuild`), the probe is a file check (no process), set-profile uses root home `/root`, and activate runs `sudo <out>/bin/switch-to-configuration switch`; no darwin-only string appears in its argv or error text.
- An `activate_command` whose program is neither of the two accepted paths fails apply before any process runs, naming both. A Linux host with no detectable builder and a darwin-style `activate_command` still works (the selector never looks at the host).
- Every consumer of `pn workspace apply` in the workspace keeps working: the `nix-agent-support` pb gate smoke test and the `pg-repo-runner` workspace fixture used `apply_command` (Task 5b).
- `pn-workspace-toml-enforce --apply-command x` exits non-zero as an unknown flag, and a toml that still contains `apply_command = "..."` parses and applies normally with the line ignored.

---

### Task 1: Require `activate_command`; apply expands `build_command` only

**Files:**

- Modify: `modules/pn/internal/workspace/apply.go` (`applyVerb.commandTemplate`)
- Modify: `modules/pn/internal/workspace/apply_asuser.go` (`applyVerb.extendPlan`, `asUserPlan`)
- Modify: `modules/pn/internal/workspace/config.go` (`ActivateCommandTemplate` docs only)
- Test: `modules/pn/internal/workspace/apply_asuser_test.go`, `modules/pn/internal/workspace/apply_test.go`

**Interfaces:**

- Consumes: `newAsUserEnv(t, toml, wrap)`, `e.apply(ctx, out)`, `e.f.significant()` from `apply_asuser_test.go`.
- Produces: `applyVerb.commandTemplate` returns `ws.config.BuildCommandTemplate()` unconditionally; `applyVerb.extendPlan` returns `errActivateCommandUnset` when `ActivateCommandTemplate() == ""`; `asUserPlan` keeps only `activateTmpl string`.

- [ ] **Step 1: Write the failing tests**

In `apply_asuser_test.go` add `TestAsUser_ApplyWithoutActivateCommandFailsBeforeAnyProcess`: a toml with `build_command` and `apply_command` but no `activate_command`; `err := e.apply(ctx, &out)`; assert `err != nil`, `strings.Contains(err.Error(), "workspace.activate_command is not set")`, `len(e.f.significant()) == 0`, and `!e.applied(t)`.

Add `TestAsUser_ShowNixCommandsWithoutActivateCommandFails`: same toml with `ShowNixCommandsOnly: true`; assert the same error and nothing printed to `out`.

Add `TestAsUser_ApplyCommandInTomlIsNeverRun`: a toml with both keys; run a successful apply; assert no call in `e.f.significant()` has `Name == "sudo"` and `Args[0] == "darwin-rebuild"` with `switch`.

- [ ] **Step 2: Run the tests and see them fail**

Run: `nice -n 10 go test -p 4 -run 'TestAsUser_(ApplyWithoutActivate|ShowNixCommandsWithoutActivate|ApplyCommandInToml)' ./internal/workspace/` (via `bgrun`, `bgwait`).
Expected: FAIL (the first two fall back to `apply_command` today).

- [ ] **Step 3: Implement**

`applyVerb.commandTemplate(ws, opts) (string, error)` returns `ws.config.BuildCommandTemplate(), nil`; the `opts.Cmd` override branch is removed in Task 2. `applyVerb.extendPlan` becomes: if `ActivateCommandTemplate() == ""` return `fmt.Errorf("workspace.activate_command is not set in pn-workspace.toml: apply builds as you and activates as root, so it needs the activation command (darwin: 'sudo {out}/sw/bin/darwin-rebuild activate'; nixos: 'sudo {out}/bin/switch-to-configuration switch')")`; the one preflight reason left (`nix-env` unusable; the prelude already expanded `build_command` before `extendPlan` runs, so the old "build_command does not expand" branch was dead and is deleted) returns `fmt.Errorf("apply failed: the build-as-user flow cannot start (%s)", reason)` instead of printing and falling back. Drop `fallbackArgs` and `fallbackErr` from `asUserPlan`. Define `errActivateCommandUnset` only if a test needs `errors.Is`; otherwise inline.

- [ ] **Step 4: Run the tests and see them pass**

Same command. Expected: PASS. Then run the whole package once to list the tests this change breaks (they are migrated in Task 3): `nice -n 10 go test -p 4 ./internal/workspace/ 2>&1 | rg '^--- FAIL'`.

- [ ] **Step 5: Commit** (after Task 3 makes the package green; do not commit a red package). Stage the three source files and test files together with Task 2 and Task 3.

### Task 2: Turn the fallbacks into errors; delete the legacy branch and `ApplyCmd`

**Files:**

- Modify: `modules/pn/internal/workspace/apply_asuser.go` (`applyAsUser`, delete `applyFallback`)
- Modify: `modules/pn/internal/workspace/apply.go` (`applyVerb.run`, `ApplyOptions`)
- Modify: `modules/pn/internal/workspace/upgrade.go` (`UpgradeOptions.ApplyCmd`)
- Modify: `modules/pn/internal/workspace/rebuild.go` (`rebuildPlan.asUser` comment, `runPlannedIn` comment)
- Test: `modules/pn/internal/workspace/apply_asuser_test.go` (also delete the `fallback` field of `asUserFake` and its `sudo darwin-rebuild switch` case in `asUserFake.Run`)

`rebuildOptions.Cmd` STAYS: `build.go` uses it through `BuildCmd`. Only `ApplyOptions.ApplyCmd` and `UpgradeOptions.ApplyCmd` go (verified: no CLI flag sets them). `ownGroup := p.cmdArgs[0] != "sudo"` in `runPlannedIn` stays for a `build_command` that starts with `sudo`.

**Interfaces:**

- Consumes: Task 1's `asUserPlan{activateTmpl}`.
- Produces: `applyAsUser(ctx, out, alog, p) error` returns, with no fallback: `apply failed: build: <err>` (run-dir failure: `apply failed: cannot create the per-run build directory: <err>`, trailer `failed_step=build`) and `apply failed: probe: the built system has no usable 'darwin-rebuild activate' (<reason>)` (trailer `failed_step=probe`, `exit_code` when the probe exited). `ApplyOptions` loses `ApplyCmd`; `rebuildOptions.Cmd` stays (`build` uses it). The `rebuild.go` comments on `stepProbe` and `stepActivate` (around lines 59 to 61) are reworded to be platform neutral.

- [ ] **Step 1: Write the failing tests**

Replace these existing tests (read each first, keep the setup, change the assertions):

- `TestAsUser_ProbeFailureFallsBackToTheLegacyApply` becomes `TestAsUser_ProbeFailureFailsWithNoRootStep`: assert the error contains `probe`, `e.f.rootCalls()` is empty, trailer `failed_step` is `probe`, nothing is marked applied.
- `TestAsUser_RunDirFailureRunsTheLegacyApplyBeforeAnyBuild` becomes `TestAsUser_RunDirFailureFailsBeforeAnyBuild`: assert no build call, error text names the per-run directory, trailer `failed_step=build`.
- `TestAsUser_PreflightFailureRunsTheLegacyApplyBeforeAnyBuild` becomes `TestAsUser_PreflightFailureFailsBeforeAnyBuild`.
- `TestAsUser_ProbeTimeoutFallsBack` (it asserts `len(rootCalls()) == 1`, the fallback) becomes `TestAsUser_ProbeTimeoutFails`: assert zero root calls and trailer `failed_step=probe`.
- Delete `TestAsUser_ProbeFailureWithoutApplyCommandFailsClearly`, `TestAsUser_PreflightFailureWithoutApplyCommandFailsBeforeAnyBuild`, `TestAsUser_ExplicitApplyCommandKeepsTheLegacyFlow`, `TestAsUser_LegacyApplyLogHasNoFailedStep` (their subject no longer exists; `TestAsUser_ApplyLogRecordsAllStepsAndNoFailure` covers the log).
- Keep `TestAsUser_CancelDuringTheProbeDoesNotFallBack`, renamed `TestAsUser_CancelDuringTheProbeStartsNoRootStep`.

- [ ] **Step 2: Run them and see them fail**

Run: `nice -n 10 go test -p 4 -run 'TestAsUser_' ./internal/workspace/` (background). Expected: the renamed tests FAIL.

- [ ] **Step 3: Implement**

In `applyAsUser`, replace each `return ws.applyFallback(...)` with `alog.fail(<step>, err)` (probe: use `alog.failExit(failedStepProbe, res.ExitCode)` when `res.Signal == ""`, else `alog.fail(failedStepProbe, nil)`) and a returned error with the texts above. Delete `applyFallback`. In `applyVerb.run` delete the `else` branch that calls `ws.runPlanned` for the legacy command and the `p.asUser != nil` guard around `extraHeader`/`applyAsUser` (apply always has `p.asUser`). Remove `ApplyOptions.ApplyCmd`, `UpgradeOptions.ApplyCmd` and their forwarding. Update the `runPlannedIn` comment that mentions "apply's `sudo darwin-rebuild switch` legacy/fallback command": the `ownGroup := p.cmdArgs[0] != "sudo"` test stays because `build_command` may start with `sudo` in a user config, but the comment now says so.

- [ ] **Step 4: Run and see them pass**

Same command. Expected: the `TestAsUser_` family PASS.

### Task 2b: `activationPlatform` Strategy so apply works on NixOS and darwin

**Files:**

- Create: `modules/pn/internal/workspace/apply_platform.go`
- Modify: `modules/pn/internal/workspace/apply_asuser.go` (`validateBuildOut`, `readBuildResult`, `probeActivate`, `setProfileArgv`, the `rootHome` constant, the package comment), `modules/pn/internal/workspace/template.go` (`validateActivateCommand` error example)
- Test: `modules/pn/internal/workspace/apply_platform_test.go`, `modules/pn/internal/workspace/apply_asuser_test.go`

**Interfaces:**

- Consumes: the `activate_command` template (`ws.config.ActivateCommandTemplate()`), `asUserPlan`. The host and `templateVars.Builder` are NOT consulted: production apply never sets a builder (no CLI flag does), `defaultBuilder()` returns `""` on a non-NixOS Linux host, and the smoke scenarios run on Linux with a literal `darwin-rebuild build` command.
- Produces:
  - `type activationPlatform struct { name string; rootHome string; checkOut func(out string) error; probe func(ctx context.Context, ws *Workspace, runDir, out string) (exec.Result, string) }` (Strategy pattern; `probe` returns `""` when usable, else the reason).
  - `func platformForActivate(activateTmpl string) (activationPlatform, error)`: takes the program element after `sudo` (`strings.Fields(tmpl)[1]`, already guaranteed to begin with `{out}/` by `validateActivateCommand`). `{out}/sw/bin/darwin-rebuild` gives name `darwin`, `rootHome` `/var/root`, `checkOut` = `out/sw/bin/darwin-rebuild` is a regular executable after following symlinks, `probe` = today's `probeActivate`. `{out}/bin/switch-to-configuration` gives name `nixos`, `rootHome` `/root`, `checkOut` = `out/bin/switch-to-configuration` is a regular executable, `probe` = a function that runs no process and returns `""`. Anything else returns `fmt.Errorf("activate_command %q: the program must be {out}/sw/bin/darwin-rebuild (darwin) or {out}/bin/switch-to-configuration (nixos)", tmpl)`.
  - `asUserPlan.platform activationPlatform`, set in `extendPlan` (an unrecognised activate program is a preflight error, before any process).
  - `func setProfileArgv(out string, p activationPlatform) []string`: byte-for-byte today's argv except `HOME=` is `p.rootHome`.
  - `func validateBuildOut(out, storeDir string, p activationPlatform) error`: the common checks (store path pattern, `Lstat(out)` directory, `Lstat(out/activate)` regular executable) then `p.checkOut(out)`.
  - The `rootHome` constant is deleted; `nixEnvPath` and `rootPath` stay (both exist on NixOS).

- [ ] **Step 1: Write the failing tests** in `apply_platform_test.go` unless noted:
  - `TestPlatformForActivate`: the two activate templates resolve with the names and root homes above; `sudo {out}/bin/other activate` returns an error containing both accepted program paths.
  - `TestSetProfileArgv_DarwinIsUnchanged`: equals the exact slice pinned today (`sudo env -i HOME=/var/root NIX_REMOTE=daemon PATH=/run/current-system/sw/bin:/usr/bin:/bin /run/current-system/sw/bin/nix-env -p /nix/var/nix/profiles/system --set <out>`).
  - `TestSetProfileArgv_NixOSUsesRootHome`: the same slice with `HOME=/root`.
  - `TestValidateBuildOut_PerPlatform`: a table with two fixtures built by `newFakeSystem` (darwin layout) and a new `newFakeNixOSSystem(t, store, hash)` (executable `activate` and `bin/switch-to-configuration`, no `sw/bin/darwin-rebuild`): each fixture passes for its own platform and fails for the other with an error naming the missing file.
  - In `apply_asuser_test.go`: `TestAsUser_NixOSCommandSequence`: an env whose toml uses `build_command = "nixos-rebuild build --flake {terminal_nix_dir}#{hostname}"` and `activate_command = "sudo {out}/bin/switch-to-configuration switch"`, applied with the default `e.apply` (no builder; the test must pass on Linux too); assert, in order, the user build call (`nixos-rebuild build ...` with `Dir` the run directory), set-profile argv with `HOME=/root`, activate argv `sudo <out>/bin/switch-to-configuration switch`, and that no process ran for the probe step. `asUserFake.Run` needs two new cases beside the darwin ones, because it calls `t.Errorf("unexpected call")` for anything unmatched: `nixos-rebuild build` (same symlink behaviour as `darwin-rebuild build`) and an activate call whose name ends in `/bin/switch-to-configuration` with args `[switch]` (the current activate matcher requires the `/sw/bin/darwin-rebuild` suffix; give both a field, `activate`, that the existing tests already use).
  - `TestAsUser_UnrecognisedActivateProgramFailsBeforeAnyProcess`: `activate_command = "sudo {out}/bin/other activate"`; assert the error text, zero calls, nothing applied.
  - `TestAsUser_NixOSMissingSwitchToConfigurationFailsAtReadResult`: the NixOS fixture without `bin/switch-to-configuration`; assert trailer `failed_step=read_result` and zero root calls.
  - `TestDriftGuard_NixOSSystemLayout`: if `/run/current-system/activate` and `/run/current-system/bin/switch-to-configuration` exist (a NixOS host), assert both are regular executables; otherwise `t.Skip("not a NixOS host")`. It is the NixOS twin of the existing darwin drift guard and is the only automatic check of the layout assumption.
- [ ] **Step 2: Run them and see them fail** (compile errors count): `nice -n 10 go test -p 4 -run 'TestPlatformForActivate|TestSetProfileArgv|TestValidateBuildOut_PerPlatform|TestAsUser_(NixOS|Unrecognised)|TestDriftGuard_NixOS' ./internal/workspace/` in the background.
- [ ] **Step 3: Implement** `apply_platform.go` and thread `asUserPlan.platform` through `applyAsUser` (`readBuildResult` and `validateBuildOut` take the platform, `probeActivate` becomes the darwin platform's `probe`, `setProfileArgv(built, p.asUser.platform)`). Update every existing caller and test of `setProfileArgv` and `validateBuildOut` to pass the darwin platform. Change the `validateActivateCommand` error example to name both: `sudo {out}/sw/bin/darwin-rebuild activate` (darwin) and `sudo {out}/bin/switch-to-configuration switch` (NixOS). Task 1's missing-key message uses the same two examples.
- [ ] **Step 4: Run them and see them pass**, then the whole `internal/workspace` package.
- [ ] **Step 5: Operator spike (optional but needed before the NixOS path is trusted), on a NixOS host, no sudo needed** : run the following from a scratch directory (replace the flake and host), paste the output into the ADR.

```bash
d=$(mktemp -d) && cd "$d" && nixos-rebuild build --flake <your-flake>#<host>
ls -l result result/activate result/bin/switch-to-configuration
test -x result/bin/switch-to-configuration && test -x result/activate && echo "layout ok"
ls -l /run/current-system/sw/bin/nix-env
getent passwd root | cut -d: -f6
```

Expected: `layout ok`, a `nix-env` symlink, and `/root`. Also compare how `nixos-rebuild switch` activates (its script runs `switch-to-configuration` under `systemd-run` and with `LOCALE_ARCHIVE` set in newer versions): if the plain `sudo <out>/bin/switch-to-configuration switch` differs in a way that matters, the NixOS `activate_command` documented in the ADR is adjusted (the program must still be `{out}/bin/switch-to-configuration`). Record the result. Until it is run, the NixOS path is implemented to the documented layout and unverified on a real host.

- [ ] **Step 6: NixOS hand-off (operator request, Phillip, 2026-11-10: "wire up as much as you can for nixos, include TODO and create a NIXOS_TODO.md in the repo root of the project. once things are working for osx, i'll push the changes and have an agent running on linux spin up an agent to work on the nixos changes").** Wire the NixOS platform as far as it can be done on macOS (everything in this task except the real-host checks). Mark every NixOS assumption that only a Linux host can confirm with a `// TODO(nixos): <what to verify, how>` comment at the code site, and create `NIXOS_TODO.md` at the root of `nix-repo-base` listing each one: the file and symbol, the assumption, the exact command or test that verifies it on a NixOS host (the spike above, `TestDriftGuard_NixOSSystemLayout`, a real `nixos-rebuild build` through `pn workspace build`, activation behaviour compared with `nixos-rebuild switch`), what to change if it is false, and a final checklist for the Linux agent (run the Go tests natively, run the smoke tier on Linux, run the NixOS spike, fill in ADR 0041). `NIXOS_TODO.md` is deleted by the change that completes the list.
- [ ] **Step 7: No commit yet** (Tasks 1 to 5 land as one commit in `nix-repo-base` after Task 5).

### Task 3: Migrate the tests that used `apply_command` as a stand-in

**Files:**

- Modify: `modules/pn/internal/workspace/apply_test.go`, `upgrade_test.go`, `rebuild_telemetry_test.go`, `rebuild_plan_test.go`, `rebuild_parity_test.go`, `rebuild_tool_test.go`, `rebuildlock_test.go`, `terminal_guard_test.go`, `applylog_test.go`, `info_test.go`, `wrapnix_test.go`, `activate_config_test.go`, `config_test.go`
- Modify: `modules/pn/internal/cli/workspace_test.go`, `modules/pn/internal/exec/wrap_test.go`, `modules/pn/internal/exec/exec_telemetry_test.go` (only where they spell `sudo darwin-rebuild switch` as an example argv; leave exec-level tests alone if the string is just sample data)

**Interfaces:**

- Consumes: `newAsUserEnv`, `newFakeSystem(t, store, hash)`, `asUserFake` from `apply_asuser_test.go`.
- Produces: (1) the existing `const asUserTOML` in `apply_asuser_test.go` loses its `apply_command` line (it is also used by `apply_asuser_realprocess_test.go`); (2) `func asUserTOMLWith(extra string) string` returning `asUserTOML` plus the caller's extra lines; (3) `func withAsUserSeams(t *testing.T, w *Workspace) string` that sets `w.asUserSeams = &asUserSeams{nixEnvUsable: func() bool { return true }, storeDir: store}` for a fresh `t.TempDir()` store, lays out `newFakeSystem(t, store, fakeHash)` in it and returns the out path. It is the extraction of the seam lines of `newAsUserEnv`, and `newAsUserEnv` calls it. Tests outside `newAsUserEnv` (`rebuild_parity_test.go`, `info_test.go`, `wrapnix_test.go`, `rebuild_telemetry_test.go`, `rebuild_plan_test.go`, `terminal_guard_test.go`, `rebuildlock_test.go`) MUST call it, because after Task 1 `extendPlan` calls `nixEnvUsable()` for real, which needs `/run/current-system/sw/bin/nix-env` and fails on Linux and in the nix sandbox. Their own fake runners also need a build answer that leaves `./result` (`os.Symlink(out, filepath.Join(opts.Dir, "result"))`) and a probe answer (copy the defaults of `asUserFake.Run`); the simplest migration is to reuse `asUserFake` itself as their runner.

- [ ] **Step 1: Find the work**

Run: `rg -n 'apply_command|ApplyCommand|ApplyCmd|sudo darwin-rebuild switch' --glob '*_test.go' modules/pn` and list the files and tests. Each hit is one of: (a) a test of the prelude (trust gate, lock, skip gate, telemetry, plan, parity) that only needs apply to run something, so it moves to `asUserTOML` and the as-user fake; (b) a test of `apply_command` itself (`TestApply_RunsApplyCommandWithOverrides`, `TestApply_ErrorsWhenApplyCommandMissing`, `config_test.go` `ApplyCommandTemplate` cases) which is deleted or, for `--override-input` coverage, re-asserted on the build call of the as-user flow.

- [ ] **Step 2: Migrate group (a), run the package after each file**

For each file: switch the config to `asUserTOML`, run `nice -n 10 go test -p 4 -run '<TestNames in that file>' ./internal/workspace/`, fix until green. `TestApply_UntrustedAborts` keeps its meaning: assert that an untrusted workspace aborts before the build call and that the output no longer contains `sudo darwin-rebuild switch` but the build argv.

- [ ] **Step 3: Delete group (b)**

Remove `TestApply_RunsApplyCommandWithOverrides` after confirming `TestAsUser_CommandSequenceExactRootArgvAndOptions` asserts the `--override-input` pairs on the build call (add the assertion there if missing). Remove `TestApply_ErrorsWhenApplyCommandMissing` (covered by Task 1). In `config_test.go` keep one test that a toml with `apply_command` still parses (Task 5 depends on it) and delete the `ApplyCommandTemplate` assertions.

- [ ] **Step 4: Run the whole Go module**

Run (background): `nice -n 10 go test -p 4 -race -run '^(Test|Example)' ./...` in `modules/pn`. Expected: PASS.

- [ ] **Step 5: No commit yet.** The smoke tier (`pn-smoke-tests`, a separate check) is red until Task 4 migrates S19 and S36, so Tasks 1 to 4 land as ONE commit at the end of Task 4.

Also in this task: `activate_config_test.go` (around line 54) loops over `apply_command` to prove `{out}` is rejected there; once the placeholder validation of `apply_command` is dropped (Task 5) that loop case is deleted, and the `{out}` rejection stays asserted for `build_command`.

### Task 4: Migrate the smoke scenarios

**Files:**

- Modify: `modules/pn/internal/workspace/smoke/scenarios/s19-happy-path-apply/{pn-workspace.toml,setup.sh,command.txt}`
- Modify: `modules/pn/internal/workspace/smoke/scenarios/s36-workspace-info-applied/{pn-workspace.toml,setup.sh,command.txt}` (S19 and S36 both create an `apply.sh` that writes `applied.txt`; that machinery is replaced by the S42 stand-ins)
- Modify: `modules/pn/internal/workspace/smoke/smoke_bare_remote.go` (about lines 154 to 157, `assertS19ApplyMarker`, which asserts `applied.txt`) and `modules/pn/internal/workspace/smoke/smoke_test.go` (about line 322 and the scenario wiring)
- Modify: `modules/pn/internal/workspace/smoke/scenarios/s42-build-as-user-apply/{pn-workspace.toml,setup.sh}`
- Modify: `modules/pn/internal/workspace/smoke/smoke_build_as_user.go`, `smoke/p1_invariant_test.go`
- Modify: `flake.nix` (the comment near line 1763 that says `apply` runs the synthetic `apply_command`)

**Interfaces:**

- Consumes: the S42 pattern (fake store directory through the `smokeseams` build tag, `build.sh`-style stand-in that leaves `./result`).
- Produces: S19 and S36 run the as-user flow against the same stand-ins S42 uses; S42 no longer asserts that `apply.sh` did not run, it asserts the happy path and adds a probe-failure scenario `s44-probe-failure-fails-apply` (s43 is taken by `s43-workforest-no-hooks`; the early-sudo plan uses s45). Each new scenario needs a `TestSmoke_S44_*`-style test and its `runScenario` wiring in `smoke/smoke_test.go` (follow how S42 and S43 are wired near lines 509-514 and 699-739).

- [ ] **Step 1: Read `s42-build-as-user-apply/setup.sh` and `smoke_build_as_user.go` fully**, then copy S42's stand-in setup into S19 and S36 (their `setup.sh` already builds a workspace; replace only the `apply_command = "./apply.sh"` line and add the stand-in build and activate scripts).
- [ ] **Step 2: Write `s44-probe-failure-fails-apply`**: a stand-in whose `sw/bin/darwin-rebuild --help` prints no activate action; the scenario's expected result is a non-zero exit, the stderr text `no usable 'darwin-rebuild activate'`, and no `activate-ran.txt`.
- [ ] **Step 3: Run the smoke tier** the way the repo's smoke check runs it (find the check in `flake.nix` near the `smoke` references; run it as a targeted `nix build .#checks.<system>.<name>` in the background with a timeout of 600000 ms or `bgrun`). Expected: S19, S36, S42, S44 PASS. Also run the Go module's tests once more (Task 3 step 4 command): the smoke Go files are part of the module.
- [ ] **Step 4: Commit the nix-repo-base changes of Tasks 1 to 5 together** after Task 5 step 5 (the Go tree and the smoke tier are green only then), in the set's `nix-repo-base` worktree:

```bash
git add modules/pn flake.nix
nix fmt -- modules/pn flake.nix
pg-hooks fix
git commit -m "pn: apply has one flow, builds as the user on darwin and nixos; apply_command is removed"
```

### Task 5: Delete `apply_command` and the enforcer flag everywhere, in both repos

**Files (nix-repo-base):**

- Modify: `modules/pn/internal/workspace/config.go` (remove the `ApplyCommand` field and its comments, `ApplyCommandTemplate`, and the `apply_command` placeholder-validation block near line 309), `enforce_keys.go` (remove the `applyCommand` parameter and its branch), `modules/pn/cmd/pn-workspace-toml-enforce/main.go` (remove the `--apply-command` flag and anything reading `PN_APPLY_COMMAND`), `build.go`, `template.go`, `upgrade.go`, `doctor_checks_hooks.go` (comments and messages that say "build_command/apply_command" now say "build_command/activate_command")
- Test: `config_test.go`, `activate_config_test.go` (the `apply_command` loop case), `enforce_keys_test.go` (about lines 290 to 500), `cmd/pn-workspace-toml-enforce/main_test.go`, `apply_asuser_test.go`

**Files (nix-ziprecruiter):**

- Modify: `home/ziprecruiter/pn-workspace-toml/default.nix` (remove the `applyCommand` option, its documentation and `PN_APPLY_COMMAND`; the `activateCommand` documentation stops saying "applyCommand stays set as the fallback"), `home/default.nix` (about lines 107 to 110: remove the `applyCommand` setting), `home/ziprecruiter/pn-workspace-toml/pn-workspace-toml-provision/pn-workspace-toml-provision.sh` (remove the `--apply-command` pass-through, about lines 19 and 55 to 56), `home/ziprecruiter/pn-workspace-toml/pn-workspace-toml-provision/tests/pn-workspace-toml-provision.bats` (about lines 36 to 54), `docs/adr/0049-nix-enforce-pn-workspace-toml-keys.md` (add a status line "`apply_command` was removed; see nix-repo-base ADR 0041" and fix the fallback wording)

**Interfaces:**

- Produces: `WorkspaceConfig.Workspace` has no `ApplyCommand`; `EnforceKeys` takes only the keys that remain (`build_command`, `activate_command`, and whatever else it takes today); `pn-workspace-toml-enforce --apply-command x` fails like any unknown flag (the standard library `flag` package prints `flag provided but not defined: -apply-command` and exits 2).

- [ ] **Step 1: Inventory.** Run `rg -n 'apply-command|PN_APPLY_COMMAND|applyCommand|apply_command|ApplyCommand' /Volumes/gitrepos/phillipg --glob '!**/.workforests/**' --glob '!**/.worktrees/**' --glob '!**/docs/superpowers/**'` (inside the set, with `<set>` as the root) and list every hit. Every hit is deleted or reworded, except hits in ADR 0038 and in the decision records of ADR 0041 (history).
- [ ] **Step 2: Write the failing tests.**
  - `TestEnforce_ApplyCommandFlagIsUnknown` (`main_test.go`): run the enforcer's `main`/`run` with `--apply-command x` and assert a non-zero exit (2) and stderr containing `flag provided but not defined`.
  - `TestConfig_LeftoverApplyCommandLineIsIgnored` (`config_test.go`): a toml with `build_command`, `activate_command` and a stale `apply_command = "sudo darwin-rebuild switch"` parses without error and `Workspace.BuildCommand` and `Workspace.ActivateCommand` hold their values (the decoder ignores unknown keys; this pins that decision).
  - `TestAsUser_LeftoverApplyCommandLineIsIgnored` (`apply_asuser_test.go`): the same toml through `newAsUserEnv`; a successful apply makes no call whose argv contains `switch` and prints no line mentioning `apply_command`.
  - In `enforce_keys_test.go` delete the cases that write `apply_command` and keep the `build_command` and `activate_command` cases.
  - nix-ziprecruiter, `home/ziprecruiter/pn-workspace-toml/pn-workspace-toml-provision/tests/pn-workspace-toml-provision.bats`: `provision passes no --apply-command to the enforcer` (a stub enforcer records its argv; assert the file contains no `--apply-command`; use the repo's existing bats harness and `gfh_setup` conventions).
- [ ] **Step 3: Run them and see them fail**: `go test -run 'TestEnforce_ApplyCommandFlagIsUnknown|TestConfig_LeftoverApplyCommand|TestAsUser_LeftoverApplyCommand' ./internal/workspace/ ./cmd/...` (background) and the bats file with `direnv exec <workspace-root> bats home/ziprecruiter/pn-workspace-toml/pn-workspace-toml-provision/tests/pn-workspace-toml-provision.bats` from `nix-ziprecruiter` in the set.
- [ ] **Step 4: Implement** the deletions above. The compiler lists every remaining reference to `ApplyCommand` and `applyCommand`.
- [ ] **Step 5: Run them and see them pass**, then the whole Go module (`nice -n 10 go test -p 4 -race -run '^(Test|Example)' ./...`, background) and the ziprecruiter bats suite for the touched file.
- [ ] **Step 6: Commit per repo** (one commit in each repo of the set):

```bash
# nix-repo-base: Tasks 1 to 5 together (see Task 4 step 4)
# nix-ziprecruiter:
git add home docs
nix fmt -- home docs
pg-hooks fix
git commit -m "pn-workspace-toml: stop provisioning apply_command"
```

### Task 5b: Keep every other consumer of `pn workspace apply` working (`nix-agent-support`)

**Files (nix-agent-support):**

- Modify: `packages/pb/internal/gate/lifecycle_smoke_test.go` (about lines 99 to 112 and 198: it writes `apply_command = "./apply.sh"` with no `activate_command` and runs the real `pn workspace apply`; its gate assertions depend on that apply recording applied state)
- Modify: `packages/pg-repo-runner/test-support/workspace_fixture.bash` (about line 43: delete the `apply_command` line it writes; apply is not run there)

**Interfaces:**

- Consumes: the apply stand-in machinery of Task 4 (a stand-in store directory through the `smokeseams` seams, stand-in `darwin-rebuild` build and `sudo`).
- Produces: the pb gate smoke test records applied state through the one apply flow, or through the state store directly if the stand-in machinery is not reachable from that package.

- [ ] **Step 1: Read `lifecycle_smoke_test.go` lines 80 to 220 and `pn workspace apply`'s applied-state writer (`markApplied` in `modules/pn/internal/workspace/appliedstate.go`)**, then pick one of two ways and write the choice into the commit message: (a) the test runs a `pn` built with the `smokeseams` tag (the `pn-smoke-tests` check builds one; find how `smoke_env.go` passes `PN_SMOKE_STORE_DIR` and reuse that, with stand-in `darwin-rebuild` and `sudo` on `PATH`); or (b) the test calls the same state writer the gate reads (the `pn workspace info` applied-state store keyed by `appliedStateKeyPath`), because what the gate test proves is the gate, not apply. Prefer (a) if a smokeseams `pn` is a flake output the pb test can consume without a new cross-repo dependency; otherwise (b).
- [ ] **Step 2: Write or adapt the failing test first:** run the existing smoke test against the in-set `pn` (`go test -tags smoke -run 'TestLifecycle' ./packages/pb/internal/gate/` with the set's `pn` on `PATH`; find the exact test names with `rg -n '^func Test' packages/pb/internal/gate/lifecycle_smoke_test.go`) and see it fail with `activate_command is not set`.
- [ ] **Step 3: Implement the chosen option; run it and see it pass.** Smoke-tagged tests are not in the unit gate (V-4): run them with an explicit timeout in the background.
- [ ] **Step 4: Commit in the set's `nix-agent-support` worktree:**

```bash
git add packages/pb packages/pg-repo-runner
nix fmt -- packages/pb packages/pg-repo-runner
pg-hooks fix
git commit -m "pb smoke: record applied state without the removed apply_command flow"
```

### Task 6: Documentation and ADR

**Files:**

- Create: `docs/adr/0041-apply-has-one-flow-the-build-as-user-apply.md`
- Modify: `docs/adr/index.md`, `docs/adr/0038-apply-builds-as-the-user-and-runs-only-profile-set-and-activate-as-root.md` (status line only: "Partly superseded by ADR 0041: decision 1 and the fallbacks")
- Modify: `modules/pn/README.md` (the apply section around lines 110 to 145), `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` (the `apply_command has no default` and `activate_command (optional ...)` paragraphs: rewrite as "`activate_command` is required", with a darwin and a NixOS example, builders `darwin-rebuild` and `nixos-rebuild`), `pn-workspace-rules/skills/pn-workspace-rules/USER_JOURNEYS.md`
- Modify: any other hit of `rg -n 'apply_command|legacy' docs CLAUDE.md pn-workspace-rules modules/pn/README.md` that describes apply behavior (leave dated plans and ADR 0015/0017/0018/0019/0024/0028 as history)

**Interfaces:**

- Produces: ADR 0041 stating, with RFC 2119 language: apply MUST have `activate_command`; the three former fallbacks MUST be errors; apply MUST support the `darwin-rebuild` and `nixos-rebuild` builders through `activationPlatform`; `apply_command` and `--apply-command` do not exist; a stale `apply_command` line in a toml is an ignored unknown key. It quotes the operator rulings verbatim with provenance (Phillip, 2026-11-10: "remove all of the legacy, leaving it in is confusing. if an unknown arg/flag fails today, it should fail with --apply-command. apply can't be darwin only. it should work on nixos or darwin.") and records the NixOS spike output (or "not yet run on a NixOS host").

- [ ] **Step 1: Write ADR 0041** with Context (what 0038 kept and why), Decision, Consequences (a root build is no longer reachable from `pn`; `--show-nix-commands` now fails on a machine without `/run/current-system/sw/bin/nix-env`, because the preflight in `extendPlan` runs before the dry-run gate; the NixOS path rests on the documented layout until the spike has run on a NixOS host), and a "Supersedes" section naming 0038 decision 1 and its fallback clauses.
- [ ] **Step 2: Update README, SKILL.md and USER_JOURNEYS.md.** In `SKILL.md` delete the NixOS `--sudo` example and the sentence "`apply_command` has no default", and describe the two supported builders.
- [ ] **Step 3: Format and run hooks to convergence**: `nix fmt -- <files>` twice, then `pg-hooks run pre-commit <files>`. Expected: all hooks pass.
- [ ] **Step 4: Commit**

```bash
git add docs modules/pn/README.md pn-workspace-rules
git commit -m "docs: ADR 0041, apply has one flow and supports darwin and nixos"
```

### Task 7: Validate, land and clean up the set

- [ ] **Step 1 (Completion Gate, in the set, background, explicit timeouts):** the targeted checks of Global Constraints; `pn workspace flake-check` through `validate-workforest`; `pn workspace build --wait=5m`; `pn workspace doctor`. Expected: all pass. A red step is fixed in the set and re-run.
- [ ] **Step 2:** `pg-hooks run pre-land` in each repo of the set.
- [ ] **Step 3:** `land-workforest` (it runs `integrate-branch` per repo in topological order and stops at the first blocked repo), then `cleanup-workforest`.
- [ ] **Step 4: Tell the operator what only they can do:** run `pn workspace apply` (an agent MUST NOT), which installs the new `pn` and the enforcer without `--apply-command`; the leftover `apply_command` line in `/Volumes/gitrepos/phillipg/pn-workspace.toml` is an ignored unknown key and is dropped by the next rewrite of the file (the enforcer, `pn workspace init`, `doctor --fix` and workforest creation re-marshal through a struct that no longer has the field), or the operator may delete it by hand; any rewrite re-arms the trust gate, so run `pn workspace allow` once afterwards. Pushing and relocking come later and block nothing.

## Self-review

- Spec coverage: required `activate_command` (Task 1), fallbacks to errors and deletion of the legacy branch and `ApplyCmd` (Task 2), NixOS and darwin through one Strategy selected by the activate program (Task 2b), test migration (Tasks 3 and 4), full deletion of the key, the enforcer flag, the consumer provisioning and the docs with no compatibility shim (Tasks 5 and 6), the other consumer of apply in `nix-agent-support` (Task 5b), process through a three-repo workforest set with the Completion Gate (Global Constraints, Task 7).
- Unknowns, stated: the NixOS layout (`bin/switch-to-configuration`, root home `/root`) is unverified on this macOS machine; Task 2b has a drift guard that runs on a NixOS host and an operator spike.
- Out of scope: the early sudo prompt (the other plan).
