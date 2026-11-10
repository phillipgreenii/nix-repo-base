# NixOS hand-off for `pn workspace apply`

Operator request (Phillip, 2026-11-10): "wire up as much as you can for nixos, include TODO and
create a NIXOS_TODO.md in the repo root of the project. once things are working for osx, i'll push
the changes and have an agent running on linux spin up an agent to work on the nixos changes".

`pn workspace apply` has one flow, the build-as-user flow of ADR 0038. Its platform specific parts
are an `activationPlatform` (Strategy) in `modules/pn/internal/workspace/apply_platform.go`,
selected from the program named in `activate_command`:

- `sudo {out}/sw/bin/darwin-rebuild activate` selects darwin (verified on macOS).
- `sudo {out}/bin/switch-to-configuration switch` selects NixOS (implemented, fake-runner tested,
  NOT verified on a NixOS host).

The selector never looks at the host or at `{builder}`. The NixOS path is implemented to the
documented layout; every assumption below that only a NixOS host can confirm is marked
`// TODO(nixos): ...` at its code site (`rg -n 'TODO\(nixos\)' modules/pn`).

This file MUST be deleted by the change that completes the list.

## Assumptions to verify

Run every command below from `modules/pn` of this repo on a NixOS host, unless stated otherwise.

### 1. System closure layout

- Site: `apply_platform.go`, `nixosPlatformStrategy().checkOut`; `apply_platform_test.go`,
  `newFakeNixOSSystem`.
- Assumption: a built NixOS system has a regular, executable `<out>/activate` and
  `<out>/bin/switch-to-configuration` directly in the closure.
- Verify: `nice -n 10 go test -p 4 -run TestDriftGuard_NixOSSystemLayout -v ./internal/workspace/`
  (it must run, not skip: it checks `/run/current-system`), then the spike below
  (`test -x result/bin/switch-to-configuration && test -x result/activate`).
- If false: change `checkOut` (and the fixture `newFakeNixOSSystem`) to the real layout. If the
  program is elsewhere in the closure, change `nixosActivateProgram` and the documented
  `activate_command`.

### 2. `nixos-rebuild build` leaves `./result`

- Site: `apply_asuser.go`, `readBuildResult`.
- Assumption: `nixos-rebuild build --flake <flake>#<host>` leaves `./result` in its working
  directory (the per-run directory), like `darwin-rebuild build`.
- Verify: `d=$(mktemp -d) && cd "$d" && nixos-rebuild build --flake <your-flake>#<host> && ls -l result`.
  Also confirm through the real tool: `pn workspace build` with `build_command = "nixos-rebuild build --flake {terminal_nix_dir}#{hostname}"`
  (no `sudo` needed for a build).
- If false: change the NixOS `build_command` in the docs (for example `nix build ...#nixosConfigurations.<host>.config.system.build.toplevel`
  leaves `./result` too) and make the `readBuildResult` hint text platform neutral if needed.

### 3. Root's home directory is `/root`

- Site: `apply_platform.go`, `nixosPlatformStrategy().rootHome`; test
  `TestSetProfileArgv_NixOSUsesRootHome`.
- Assumption: the set-profile step's `HOME=` is `/root`.
- Verify: `getent passwd root | cut -d: -f6`.
- If false: change `rootHome` and the test's expected argv.

### 4. `nix-env` path for the set-profile step

- Site: `apply_asuser.go`, `nixEnvPath` (shared by both platforms) and `ws.nixEnvUsable`.
- Assumption: `/run/current-system/sw/bin/nix-env` exists on NixOS, is an executable regular file
  after following symlinks, and the set-profile argv
  `nix-env -p /nix/var/nix/profiles/system --set <out>` is right for NixOS.
- Verify: `ls -l /run/current-system/sw/bin/nix-env`; then read what `nixos-rebuild switch` does to
  set the profile (`nixos-rebuild` script: `nix-env -p /nix/var/nix/profiles/system --set`).
- If false: give `activationPlatform` a `nixEnvPath` field, use it in `setProfileArgv` and
  `nixEnvUsable`, and update the pinned argv tests.

### 5. Activation semantics versus `nixos-rebuild switch`

- Site: `apply_platform.go`, `platformForActivate` (the documented NixOS `activate_command`).
- Assumption: plain `sudo <out>/bin/switch-to-configuration switch` is an acceptable activation.
  `nixos-rebuild switch` may run it under `systemd-run` and set `LOCALE_ARCHIVE` (newer versions).
- Verify: read the installed script (`cat "$(command -v nixos-rebuild)"`, look for
  `switch-to-configuration`, `systemd-run`, `LOCALE_ARCHIVE`); on a disposable machine run a real
  `pn workspace apply` with the NixOS `activate_command` and confirm the system switches and
  `readlink /run/current-system` equals the built out path.
- If it differs in a way that matters: change the documented `activate_command` (the program must
  stay `{out}/bin/switch-to-configuration`) or extend the root step options in `applyAsUser`
  (`rootStepOptions`) to carry the needed environment.

### 6. No process probe on NixOS

- Site: `apply_platform.go`, `nixosPlatformStrategy().probe` (returns usable without running anything).
- Assumption: the file check in `checkOut` is enough before the root steps.
- Verify: run `<out>/bin/switch-to-configuration` with no arguments and with `--help` on the spike
  result and see whether it prints usage and exits non-zero in a stable way.
- If a stable probe exists: implement it like `ws.probeActivate` (darwin), with a parser, a fake
  runner test and a real-tool test; keep the failed step value `probe`.

### 7. Setuid `sudo` on NixOS

- Site: `modules/pn/internal/exec/tool.go`, `ResolveTool` and `SystemProfileBinDirs`; `rebuild.go`,
  `EnableToolResolution`; the set-profile and activate steps (`apply_asuser.go`) exec `sudo` by bare
  name.
- Assumption: `sudo` resolves through `PATH` to the setuid wrapper `/run/wrappers/bin/sudo`. The
  fallback in `ResolveTool` only searches `/run/current-system/sw/bin` and
  `/nix/var/nix/profiles/system/sw/bin`, where NixOS's sudo is NOT setuid, so a PATH without
  `/run/wrappers/bin` would resolve a non-setuid sudo that fails.
- Verify: in the environment pn runs in (a login shell and a service or SSH session, if both
  matter), `command -v sudo` must print `/run/wrappers/bin/sudo`; then run `pn workspace apply`
  with the NixOS `activate_command` and confirm both root steps start (the set-profile and activate
  steps). Also `echo "$PATH" | tr : '\n' | rg -n wrappers`.
- If false: add `/run/wrappers/bin` first to the directories `ResolveTool` searches for `sudo` (a
  platform specific list), or exec `/run/wrappers/bin/sudo` for the NixOS platform, and add a test.
  Marker: `TODO(nixos)` in `rebuild.go` `EnableToolResolution` and `apply_asuser.go`
  `rootStepOptions`.

### 8. Environment through `sudo`

- Site: `apply_asuser.go`, `rootStepOptions` (passes no `Env`).
- Assumption: the activation works without the variables `nixos-rebuild switch` sets for
  `switch-to-configuration`: `LOCALE_ARCHIVE`, `NIXOS_NO_CHECK`, and `NIXOS_INSTALL_BOOTLOADER`
  (only with `--install-bootloader`). sudo's `env_reset` drops them even if pn passed them.
- Verify: on a disposable NixOS machine, make `switch-to-configuration` log its environment (for
  example a temporary wrapper script that runs `env | sort > /tmp/env.<tag>` then execs the real
  one) and run a real `nixos-rebuild switch` (tag `rebuild`), then `pn workspace apply` (tag `pn`);
  `diff /tmp/env.rebuild /tmp/env.pn`.
- If different in a way that matters: pass the needed variables across sudo explicitly in the
  activate step (`sudo env VAR=... <out>/bin/switch-to-configuration switch`, built by pn, not by
  the template), keeping the set-profile argv unchanged, and pin it in a test. Marker:
  `rootStepOptions`.

### 9. Cancel and terminal-loss semantics

- Site: `apply_asuser.go`, `applyAsUser` (`rootCtx := context.WithoutCancel(ctx)`).
- Assumption: "both root steps run to completion once set-profile starts" is right for
  `switch-to-configuration`, which is long-running and restarts units, and survives an SSH drop.
  `nixos-rebuild` runs the activation under a `systemd-run` wrapper precisely to survive a lost
  terminal; pn's steps stay in pn's own process group and die with the terminal (SIGHUP).
- Verify on a NixOS VM (snapshot first): over SSH run `pn workspace apply`; during the activate
  step (a) press Ctrl-C and (b) kill the SSH connection from the other side; afterwards
  `readlink /run/current-system` must equal the built out path, `systemctl --failed` must be empty,
  and no activation process may remain half-done (`pgrep -af switch-to-configuration`).
- If it does not survive: run the activate step through `systemd-run` as `nixos-rebuild` does (for
  NixOS only), and update the cancel documentation in the README and ADR 0041.

### 10. `--sudo` and nixos-rebuild-ng

- Site: documentation (`modules/pn/README.md`, `pn-workspace-rules` SKILL.md); the removed SKILL.md
  example used `nixos-rebuild ... --sudo`.
- Assumption: pn's two root steps (`nix-env -p /nix/var/nix/profiles/system --set <out>`, then
  `sudo <out>/bin/switch-to-configuration switch`) are equivalent to what `nixos-rebuild-ng` does
  for `switch --sudo` (set the profile, then activate).
- Verify: read the installed tool's source or trace it: `nixos-rebuild-ng switch --flake
<flake>#<host> --sudo --verbose` (disposable machine) and compare its commands for the profile
  and activation steps with pn's two steps (profile path, `--set`, the `switch-to-configuration`
  action, `systemd-run`, environment).
- If different: align the NixOS platform's set-profile and activate steps with it (this feeds
  items 8 and 9) and correct the docs.

### 11. No NixOS smoke scenario; probe error text is darwin-worded

- Site: `modules/pn/internal/workspace/smoke/scenarios/`; the smoke scenarios S19, S36, S42 and
  S44 are darwin stand-ins and the NixOS path is covered only by the `TestAsUser_NixOS*`
  fake-runner tests. Also the probe error text `no usable 'darwin-rebuild activate'` in
  `apply_asuser.go` (`applyAsUser`, step 3).
- Assumption: fake-runner tests are enough. They are not an end-to-end proof.
- Add: scenario `s46-nixos-build-as-user-apply`, modeled on `s42-build-as-user-apply`
  (`setup.sh` plus `pn-workspace.toml`): a stand-in `nixos-rebuild build` that leaves `./result`
  pointing at a fake store directory holding an executable `activate` and an executable
  `bin/switch-to-configuration`, a stand-in `sudo` first on PATH, and
  `activate_command = "sudo {out}/bin/switch-to-configuration switch"`. Assert the two root steps ran
  with the NixOS argv and the apply log trailer reads `failed_step: none`.
- Run the smoke tier on Linux (from `modules/pn`; the `pn-smoke-tests` check in `flake.nix` runs the
  same thing with `-tags smoke -count=1`):
  `nice -n 10 go test -p 4 -tags smoke -count=1 ./internal/workspace/smoke/` (via `bgrun`), or
  `nix build .#checks.x86_64-linux.pn-smoke-tests -L` (via `bgrun`; use the host's system).
- Probe text: if a NixOS probe is ever added (item 6), generalise the `no usable 'darwin-rebuild
activate'` error in `apply_asuser.go` (and any `apply_platform.go` text) to a platform neutral
  wording and update the tests that match it. Marker: the `TODO(nixos)` above that error.

### 12. The NixOS `build_command` example is an inference

- Site: `modules/pn/README.md` and `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` (the
  NixOS example `build_command = "nixos-rebuild build --flake {terminal_nix_dir}#{hostname}"`).
- Assumption: that command, run as a user without sudo, builds and leaves `./result` (same as
  item 2) and `{hostname}` matches the `nixosConfigurations` attribute.
- Verify: run it through `pn workspace build` on a NixOS host and inspect `result`.
- If false: replace the example in both docs (and the `build_command` text in ADR 0041) with the
  command that works.

## Spike (no sudo)

```bash
d=$(mktemp -d) && cd "$d" && nixos-rebuild build --flake <your-flake>#<host>
ls -l result result/activate result/bin/switch-to-configuration
test -x result/bin/switch-to-configuration && test -x result/activate && echo "layout ok"
ls -l /run/current-system/sw/bin/nix-env
getent passwd root | cut -d: -f6
```

Expected: `layout ok`, a `nix-env` symlink, and `/root`. Paste the output into the ADR that
records the removal of `apply_command` (ADR 0041 in the plan
`docs/superpowers/plans/2026-11-10-pn-apply-remove-legacy-flow.md`, Task 6).

## Checklist for the Linux agent

- [ ] Run the Go tests natively: `nice -n 10 go test -p 4 ./internal/workspace/... ./internal/cli/... ./cmd/...` (use `bgrun`); every test must pass on Linux.
- [ ] `TestDriftGuard_NixOSSystemLayout` runs (does not skip) and passes.
- [ ] Run the smoke tier on Linux (the `pn-smoke-tests` check, or the smoke Go tests with their build tags).
- [ ] Run the spike above and record the output.
- [ ] Resolve each of the twelve items: confirm, or make the change listed and update the tests.
- [ ] Remove each `TODO(nixos)` comment as its item is resolved (`rg -n 'TODO\(nixos\)' modules/pn` must return nothing).
- [ ] Fill in the NixOS section of docs/adr/0041-apply-has-one-flow-the-build-as-user-apply.md with the spike output (or the corrections found).
- [ ] Update `modules/pn/README.md` and `pn-workspace-rules` skill docs if the documented NixOS `activate_command` changed.
- [ ] Add the `s46-nixos-build-as-user-apply` smoke scenario (item 11) and run the smoke tier on Linux.
- [ ] Before deleting this file, run `rg -n 'NIXOS_TODO|UNVERIFIED on a NixOS host|TODO\(nixos\)'` repo-wide and rewrite or remove every hit (known: `modules/pn/README.md` around line 130, `pn-workspace-rules/skills/pn-workspace-rules/SKILL.md` around line 133, `docs/adr/0041-apply-has-one-flow-the-build-as-user-apply.md` around lines 70 and 75, plus the code markers).
- [ ] Delete this file.
