# ADR-0043: `pn workspace apply` activates NixOS under systemd-run and verifies before root

**Date:** 2026-11-11
**Status:** Accepted (amends ADR 0041 decision 3 for NixOS; amends 0038 and 0042 on the activate step)
**Deciders:** phillipgreenii

## Context

ADR 0041 made the build-as-user apply the only flow and wired a NixOS platform to the documented
system layout, with the activation as a bare `sudo <out>/bin/switch-to-configuration switch`. That
path was written on macOS and was unverified on NixOS. It was then compared with the reference
implementation, `nixos-rebuild-ng switch --sudo` (nixos-rebuild-ng 26.05, read from its source and
checked on a NixOS 26.05 host), and differs in ways that matter:

- `nixos-rebuild` refuses a closure whose `<out>/nixos-version` is not a regular file (a nix bug can
  leave a partial closure). pn did not check.
- `nixos-rebuild` runs the activation under `systemd-run` when `/run/systemd/system` exists, so a
  terminal Ctrl-C, an SSH drop or a SIGHUP does not kill a half-finished activation that is
  restarting units. pn ran `sudo <out>/bin/switch-to-configuration switch` as its own child.
- `nixos-rebuild` passes `NIXOS_INSTALL_BOOTLOADER=0` explicitly and sets the profile only for
  `switch` and `boot`; pn always sets the profile first, so a template ending in `test`,
  `dry-activate` or `boot` would silently make the system the boot default or leave
  `/run/current-system` unchanged, which breaks pn's post-conditions (nvd, the applied record).
- pn had no probe for NixOS.

Operator rulings, Phillip, 2026-11-11: after the explanation of systemd-run, "using systemd-run
seems correct to me", and on the outline of this ADR, "this all looks good". Also, 2026-11-11:
"we will not run mac tests on a non mac machine" (the native darwin test run happens only on the
Mac) and, on whether that run gates landing, "soft check".

## Decision

1. **The NixOS activation is rewritten by the platform, not the template.** `activate_command` stays
   `sudo {out}/bin/switch-to-configuration switch` and remains the platform selector. One function,
   `activateArgv`, expands the template and then applies the platform's rewrite (`wrapActivate`, nil
   for darwin, so the darwin path is untouched and its argv is exactly the template expansion). The
   dry-run lines, the apply-log header and the run all call it, so what is shown is what runs. With
   systemd present the argv is

   ```text
   sudo env -i PATH=<rootPath> NIXOS_INSTALL_BOOTLOADER=0 systemd-run -E NIXOS_INSTALL_BOOTLOADER --collect --no-ask-password --pipe --quiet --service-type=exec --unit=nixos-rebuild-switch-to-configuration <out>/bin/switch-to-configuration switch
   ```

   and without systemd the same without the `systemd-run ...` part. This applies to every NixOS
   host; there is no per-host toggle. Whether systemd is present (`/run/systemd/system` exists, the
   test `nixos-rebuild` makes) is decided once in the preflight and stored in the plan; it is a seam
   that defaults to the production stat.

   Deliberate differences from `nixos-rebuild`: PATH is pn's fixed `rootPath`
   (`/run/current-system/sw/bin:/usr/bin:/bin`), not the caller's, so `env` and `systemd-run` MUST
   resolve under it (checked on a NixOS 26.05 host); only `NIXOS_INSTALL_BOOTLOADER` is exported to
   the unit; pn does NOT forward `LOCALE_ARCHIVE` (the `switch-to-configuration` wrapper sets it
   itself) and does NOT forward `NIXOS_NO_CHECK` (a root-executed step MUST NOT let an unprivileged
   environment variable skip the pre-switch checks, the same reasoning as the hard-coded set-profile
   step). The unit name equals `nixos-rebuild`'s, so a concurrent `nixos-rebuild switch` and pn
   exclude each other. The variables travel as `env -i` arguments because sudo drops the
   environment; `RunOptions.Env` stays nil for every root step.

2. **Only `switch` is accepted.** The NixOS template MUST be exactly the three tokens
   `sudo {out}/bin/switch-to-configuration switch`. `boot`, `test`, `dry-activate`, `check`, a
   missing action and any extra token are rejected in the preflight, before the trust gate and any
   process, with an error that explains pn always sets the profile first. Darwin templates get no
   new validation.

3. **The built closure is verified before root.** The NixOS file check also requires a regular,
   readable `<out>/nixos-version` (`nixos-rebuild` parity). The probe runs
   `<out>/bin/switch-to-configuration` with no arguments as the user, unwrapped, with a 10 s limit in
   the run directory. It is accepted when it exits 1, writes nothing to stdout and its stderr mentions
   `switch` and `boot`; this is a keyword match, not an exact first-line match, so a nixpkgs
   rewording does not block every apply. A mismatch is a hard `probe` failure with no configuration
   bypass (the fix is a pn release). The error says the probe is a pn check and not a system fault,
   names the manual command (`<out>/bin/switch-to-configuration` with no arguments, run as your
   user) and quotes the first output line. The darwin probe error text is unchanged.

4. **Detached-failure semantics (systemd path only).** The activation is no longer a child of pn. A
   terminal Ctrl-C kills `sudo` and `systemd-run` but the unit keeps running, and a SIGHUP kills pn
   itself, so no message is possible then. When the activate step fails or is interrupted, the
   error MUST add that activation may still be running in unit
   `nixos-rebuild-switch-to-configuration` and name `systemctl status` and `journalctl -u` for it.
   It is one combined message for a still-running or failed unit, not stderr scraping (the step
   does not capture stderr). Best effort, pinned only against the fake runner because whether
   `systemd-run --pipe` forwards the unit's exit status through sudo cannot be verified without
   activating: exit 4 of `switch-to-configuration` means some units failed but the system HAS
   switched, so the message says to run `systemctl --failed` and not to re-run apply. The run records
   `failed_step: activate`, runs neither nvd nor the fsmonitor restart and does not record the applied
   state (as `nixos-rebuild` treats a failure). A pn-initiated cancel never interrupts the root steps
   (they run on a context detached from the cancel), so on NixOS the set-profile step and the unit
   run to completion; only pn's own client does not.

5. **A stale `apply_command` is ignored and says so.** The error for an unset `activate_command` now
   states that a leftover `apply_command` is ignored.

6. **The default `build_command` relies on the kernel hostname.** pn never appends `#host`; with the
   default `{builder} build --flake {terminal_nix_dir}` nixos-rebuild-ng selects
   `nixosConfigurations.<kernel hostname>`. If that name ever differs from the attribute,
   nixos-rebuild-ng fails with its own error and the fix is an explicit `build_command`.

## Spike result (NixOS 26.05, monorepod, 2026-11-11)

`nixos-rebuild build --flake <homelab flake dir>` (no `#host`, the command pn runs) in an empty
directory built the host closure and left `./result`, a symlink to
`/nix/store/<hash>-nixos-system-monorepod-26.05.20261031.fa13731`. The flake lives in `homelab/nix`
and the default `build_command` resolved `nixosConfigurations.monorepod` from the kernel hostname
(the attribute exists; `nix eval` lists it among the other machines). Layout of the fresh closure:
`activate` (`-r-xr-xr-x`), `bin/switch-to-configuration` (`-r-xr-xr-x`), `nixos-version`
(`-r--r--r--`, content `26.05.20261031.fa13731`), `sw` and `systemd` symlinks.
`PATH=/run/current-system/sw/bin:/usr/bin:/bin command -v env systemd-run` finds both. Run as the
plain user with no arguments, `<out>/bin/switch-to-configuration` wrote nothing to stdout, exited 1
and wrote 392 bytes to stderr:

```text
Usage: switch-to-configuration [check|switch|boot|test|dry-activate]
check:        run pre-switch checks and exit
switch:       make the configuration the boot default and activate now
boot:         make the configuration the boot default
test:         activate the configuration, but don't make it the boot default
dry-activate: show what would be done if this configuration were activated

```

That text is the parser's test fixture. Not exercised by an agent: any activation. The first live
apply is an operator step.

## Consequences

- A NixOS apply survives a dropped terminal once the unit has started; the cost is that a failed or
  interrupted client no longer proves the activation stopped, which the error message now says.
- A mismatched `switch-to-configuration` usage blocks the apply until pn is released with the new
  text. This is accepted: the probe is the only pre-root check of the script's behaviour.
- Linux unit and smoke tiers cover the NixOS flow with fakes and stand-ins on every host,
  independent of systemd (a seam; the smoke binary takes it from `PN_SMOKE_SYSTEMD`). An opt-in real
  probe test (`PN_NIXOS_REAL_PROBE=1`, operator-run) runs the parser on the host's own script. The
  native darwin test run is a soft check on the Mac and does not gate landing.
- Dated ADRs 0015, 0017 to 0019, 0024 and 0028 remain records and are not rewritten.

## Supersedes

ADR 0041 decision 3's NixOS bullet ("a `bin/switch-to-configuration` file check and no process
probe") and its "UNVERIFIED" consequence. ADR 0038 and ADR 0042: the activate step on NixOS is the
wrapped argv above, and the cancel text for NixOS is as in decision 4.
