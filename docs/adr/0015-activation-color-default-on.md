# Activation color defaults ON; `NO_COLOR` is the only off-switch

**Status**: Accepted
**Date**: 2026-07-01
**Deciders**: Phillip Green II

## Context

ADR [0013](0013-activation-output-convention.md) codified an activation-output
convention whose color mechanism relies on `CLICOLOR_FORCE` reaching the
activation script (exported by `pn` on the `pn workspace apply` path) because
`pn` pipes the output, so `[ -t 1 ]` is false and color would otherwise
self-suppress.

That mechanism was never verified end-to-end. When it was (`pn workspace apply`
run through a PTY-preserving capture), **system** activation sections rendered
UTF-8 glyphs but **no color**, on every section. Root cause, confirmed by
reading the built activate script:

nix-darwin composes `system.activationScripts` into a single script whose
shebang is `#!/usr/bin/env -i …/bash`. `env -i` runs it with a **wiped
environment**; the script then re-exports only `PATH`, `USER`, `LOGNAME`,
`HOME`, `MAIL`, `SHELL`, `LANG=C`, and `LC_CTYPE=UTF-8`. It never re-exports
`CLICOLOR_FORCE` — and it also never re-exports `NO_COLOR`.

Consequences of `env -i` for the color guard:

- `CLICOLOR_FORCE` (exported by `pn` into the `sudo darwin-rebuild` subprocess)
  is stripped before the activate script runs. Even `sudo --preserve-env` cannot
  help: `env -i` sits between `darwin-rebuild` and the activation script.
- `[ -t 1 ]` is false (`pn` pipes the output).
- `LC_CTYPE=UTF-8` is re-exported, so glyph detection still works — which is why
  glyphs appeared but color did not.

Therefore the ADR 0013 runtime color detection (`CLICOLOR_FORCE` OR `[ -t 1 ]`)
can only ever evaluate to "off" for system-activation sections. Home-manager
activation is **not** run under `env -i`, so it can see `CLICOLOR_FORCE` — this
is the asymmetry ADR [0014](0014-activation-output-home-manager.md) flagged as
"color unverified."

## Decision

The color decision in `activationHelpers` (`lib/activation/activation-lib.bash`)
MUST default color **ON**, with `NO_COLOR` (per <https://no-color.org>) as the
sole off-switch:

```bash
if [ -n "${NO_COLOR:-}" ]; then
  _act_color=0
else
  _act_color=1
fi
```

### Normative rules (supersede ADR 0013's color rules)

- The color decision MUST default to on.
- Color MUST NOT be emitted when `NO_COLOR` is non-empty.
- The color decision MUST NOT consult `CLICOLOR_FORCE` or `[ -t 1 ]`. Under
  `env -i` neither is observable, so consulting them can only ever answer "off"
  and defeats the feature; defaulting on is the only mechanism that colorizes
  system-activation sections.
- Glyph/UTF-8 detection (`LC_ALL`/`LC_CTYPE` matching `*UTF-8*`/`*utf8*`) is
  unchanged. Color and glyph selection remain independent axes.
- Suppressing color for system-activation sections in response to `NO_COLOR`
  cannot be done inside the activation script (it cannot see `NO_COLOR` under
  `env -i`). If that suppression is desired it MUST be enforced in `pn`, the only
  layer that both sees the operator's real environment and the full activation
  output stream (e.g. an ANSI-stripping filter on the `darwin-rebuild` output
  gated on `NO_COLOR`). This is intentionally **not** implemented here.

## Consequences

### Positive

- System-activation `[tag]` sections finally render colored on the
  `pn workspace apply` path — the original goal of ADR 0013, previously
  unreachable.
- In non-`env -i` contexts (home-manager activation, direct interactive runs) a
  visible `NO_COLOR` is still honored directly by the helper.

### Negative

- `pn workspace apply > file` (or into a non-color sink) **without** `NO_COLOR`
  now captures raw ANSI for system-activation sections. Prefix `NO_COLOR=1` for
  clean logs.
- `NO_COLOR` does **not** suppress system-activation color until the `pn`-side
  strip described above is implemented. `NO_COLOR` is still honored for
  home-manager sections and for `nvd` (both env-visible), so suppression is
  partial. Tracked as follow-up.

### Neutral

- `CLICOLOR_FORCE` is no longer consulted anywhere in the helper. The `pn`
  `applyColorEnv` `CLICOLOR_FORCE` export, the `apply_command`
  `--preserve-env=CLICOLOR_FORCE`, and the `beads-dolt` sudo `CLICOLOR_FORCE=`
  forward are now dead and MAY be removed as cleanup (tracked as follow-up).

## Related Decisions

- Supersedes the color portions of ADR [0013](0013-activation-output-convention.md):
  the "Normative rules" color bullets, the "`pn` force-color mechanism" section,
  and the `CLICOLOR_FORCE` forwarding in "Child-process injection". The structural
  convention (`mkActivationSection`, `[tag]` headers, glyph vocabulary, trailing
  blank line, `printf '%s\n'` safety, `act_fail` MUST NOT `exit`) is unchanged.
- Resolves the "color unverified" caveat in ADR
  [0014](0014-activation-output-home-manager.md) for system activation: color is
  now default-on rather than dependent on unreachable `CLICOLOR_FORCE`.
