# Extend the Activation-Output Convention to home-manager Activation

**Status**: Accepted
**Date**: 2026-06-30
**Deciders**: Phillip Green II

## Context

ADR [0013](0013-activation-output-convention.md) standardized author-controlled
`system.activationScripts` (nix-darwin) on `mkActivationSection` plus the `act_*`
helper functions. But `darwin-rebuild` / `pn workspace apply` output also includes
**home-manager activation** — the `home.activation.<name>` DAG entries — which ADR
0013 never covered. An audit of the apply output found these still used several
inconsistent styles: `--== Title ==--` headers, `name:` message prefixes, bare `✓`
glyphs without indent (the `zr-lib.bash` color vocabulary), and plain `echo`. The
worst offender is `setupZmWorktrees` in `phillipg-nix-ziprecruiter`, whose chained
`zm-*` scripts mixed three of those styles in a single section.

The two activation mechanisms differ in ways that matter here:

- nix-darwin composes all `system.activationScripts.<name>.text` fragments into a
  **single** root shell; ADR 0013 gives each a grep-friendly `[tag]` header via
  `mkActivationSection`.
- home-manager runs **each** `home.activation` entry as a separate step in the
  user's activation process and prints its own `Activating <name>` header line,
  which the entry cannot replace.

Two implementation constraints also shaped the decision:

- The `act_*` helpers previously existed only as an inline Nix string
  (`activationHelpers`), so standalone bash scripts (the `zm-*` packages built via
  `mkBashScript` / `mkBashLibrary`) had no way to `source` them.
- `mkBashLibrary` (`lib/bash-builders.nix`) requires per-system `pkgs`, so a
  prebuilt library **cannot** be a system-agnostic `flake.lib` output.

## Decision

### Single source of truth, two consumption forms

The `act_*` bash now lives in one file: `lib/activation/activation-lib.bash` in
`phillipg-nix-repo-base`. It is consumed two ways:

- **String form** — `activationHelpers = builtins.readFile ./activation/activation-lib.bash`,
  a `flake.lib` output. Used for inline injection into `system.activationScripts`
  (via `mkActivationSection`) and into inline `home.activation` text (the
  child-injection pattern from ADR 0013's `beads-dolt` case).
- **Library form** — a `mkBashLibrary` built **by each consumer** from
  `inputs.<repo-base> + "/lib/activation"`, mirroring how `phillipg-nix-ziprecruiter`
  already builds `zr-lib`. It is NOT a `flake.lib` output of `phillipg-nix-repo-base`
  because `mkBashLibrary` needs per-system `pkgs` (see Alternatives).

### home-manager rendering convention

The following are normative (RFC 2119):

- A `home.activation` entry that produces console output **MUST** route status
  lines through `act_ok` / `act_warn` / `act_fail` / `act_info` / `act_detail`.
- The section header **MUST** be home-manager's own `Activating <name>` line; an
  entry **MUST NOT** print an additional `[tag]` header. This is an accepted
  asymmetry with system scripts (which use `[tag]`) — home-manager owns its header
  line, so the convention standardizes only the **body**.
- A standalone script run from a `home.activation` entry **MUST** obtain `act_*`
  by depending on a locally-built `activation-lib` through the bash framework's
  `libraries = [ … ]`. Inline `home.activation` text (no sourced library) **MAY**
  instead source the `activationHelpers` string.
- **Silent-entry rule:** an entry that performs real work **SHOULD** emit at least
  one closing `act_ok` / `act_info` under its `Activating <name>` header, so every
  activated section shows evidence (mirrors ADR 0013's "every section ends with a
  clear marker"). An entry that is **intentionally** silent — e.g. a git hook that
  fires per git-operation rather than during activation, or a disabled stub —
  **MUST** carry a code comment documenting the deliberate silence.
- A pure-CLI tool that merely shares a library with activation scripts is **NOT**
  required to adopt `act_*` output; `act_*` being defined-but-unused in it is
  acceptable. Scope is limited to activation-invoked scripts (`act_*`'s 2-space
  indent suits output nested under an activation header, not standalone CLI).

### Color / UTF-8 caveat

`act_*`'s UTF-8-vs-ASCII marker detection applies in home-manager activation as in
system activation: UTF-8 glyphs and the 2-space structure render in
`home.activation` under `pn workspace apply` (e.g. `configureGithubAuth` prints
`  ✓ …`), degrading gracefully to ASCII (`[OK]`) outside a UTF-8 locale.

**Color** was originally an open question here — whether `CLICOLOR_FORCE` /
`LC_CTYPE` would reach the home-manager activation subprocess on the `pn apply`
path. It is now **resolved**: ADR [0015](0015-activation-color-default-on.md) made
activation color **default ON** (`NO_COLOR` the only off-switch), precisely because
runtime `CLICOLOR_FORCE` / TTY detection cannot survive nix-darwin's `env -i`
activation (the dead `CLICOLOR_FORCE` apply-env plumbing was subsequently removed).
With color defaulting on, the `act_*` sections emit ANSI color in `home.activation`
regardless of propagation — **confirmed at apply time (2026-07-01)**: colored `✓`
markers are visible in the converted `home.activation` sections under
`pn workspace apply`.

## Consequences

### Positive

- One `act_*` vocabulary across system and home-manager activation output.
- A single `.bash` source of truth feeds both the inline string and the
  consumer-built library — no copy-paste divergence.
- `setupZmWorktrees` now reads as one coherent section instead of three styles.

### Negative

- **Header asymmetry** — system sections use `[tag]`, home sections use
  `Activating <name>`. Accepted: home-manager owns its header.
- **Per-consumer boilerplate** — each repo that wants the library builds it
  locally (a few lines mirroring `zr-lib`).
- **Color** in home-manager activation is now confirmed (see caveat); the
  mechanism is color-defaults-ON per ADR
  [0015](0015-activation-color-default-on.md).

### Neutral

- `activation-lib` cannot be a `flake.lib` output (per-system `pkgs`); the string
  form remains the `lib.*` output and the inline-injection escape hatch.
- Cross-repo ordering: a consumer change that uses `activation-lib` only validates
  against a repo-base rev that contains the source file. Land/push repo-base first,
  then bump the consumer's `flake.lock` (standard `pn` workspace ordering).

## Alternatives Considered

### Expose `activation-lib` as a `phillipg-nix-repo-base` `flake.lib` output

Rejected. `mkBashLibrary` calls `pkgs.writeText` / `pkgs.runCommand` and so needs
per-system `pkgs`, which the system-agnostic `flake.lib` block does not provide.
Consumers build the library locally instead (as they already do for `zr-lib`).

### Inject the `activationHelpers` string into every consuming script

Rejected as the primary mechanism. It duplicates the ~30-line helper bash into each
script and abandons the framework's `libraries = [ … ]` dependency chaining. The
string injection is reserved as the escape hatch for shells that cannot `source` a
library (sudo'd subprocesses, inline activation text) — the documented `beads-dolt`
pattern from ADR 0013.

### Convert all `zm-*` scripts (including pure-CLI tools) to `act_*`

Rejected. `act_*`'s 2-space indent is designed for output nested under an activation
header; a standalone CLI tool printing `  ✓ done` looks odd. Scope is limited to
activation-invoked scripts; pure-CLI tools keep `zr-lib`'s raw glyphs.

## Related Decisions

- Extends ADR [0013](0013-activation-output-convention.md) (activation-output
  convention for `system.activationScripts`).
- Color mechanism codified — and this ADR's color caveat resolved — by ADR
  [0015](0015-activation-color-default-on.md) (activation color defaults ON).
- See also: `phillipg-nix-ziprecruiter` — the `setupZmWorktrees` activation section
  was converted to `act_*` as the first consumer of `activation-lib`.
