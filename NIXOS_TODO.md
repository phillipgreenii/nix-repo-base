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
- [ ] Resolve each of the six items: confirm, or make the change listed and update the tests.
- [ ] Remove each `TODO(nixos)` comment as its item is resolved (`rg -n 'TODO\(nixos\)' modules/pn` must return nothing).
- [ ] Fill in the NixOS section of docs/adr/0041-apply-has-one-flow-the-build-as-user-apply.md with the spike output (or the corrections found).
- [ ] Update `modules/pn/README.md` and `pn-workspace-rules` skill docs if the documented NixOS `activate_command` changed.
- [ ] Delete this file.
