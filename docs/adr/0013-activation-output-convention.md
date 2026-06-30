# Activation-Script Output Convention

**Status**: Accepted
**Date**: 2026-06-29
**Deciders**: Phillip Green II

## Context

`pn workspace apply` (→ `sudo darwin-rebuild switch`) produces a large volume of
output. The `system.activationScripts` we author are a small fraction of it, but
they were inconsistent and easy to miss:

- Headers differed in verb and casing; only `launchd-health-check` used a
  grep-friendly `[tag]` prefix.
- Status signals varied: `launchd-health-check` used `ok`/`FAIL`/`WARN`; others
  printed `WARNING:` or nothing at all. No script used color or symbols.
- Sections ran together with no blank line between them.

nix-darwin composes all `system.activationScripts.<name>.text` fragments into a
**single** shell process (`#!/usr/bin/env -i ${stdenv.shell}`, `set -e`,
`set -o pipefail`, `LANG=C`, `LC_CTYPE=UTF-8`). No per-section prefix or wrapper
is injected by the framework — consistency is entirely ours to author.

A color/glyph vocabulary already existed in the workspace (`zr-lib.bash`:
`✓`/`⚠`/`✗` + GREEN/YELLOW/RED). This ADR codifies the house style and the
mechanism that makes color reach the terminal on the `pn workspace apply` path.

The full rationale, component specifications, and data-flow diagrams are in
`docs/superpowers/specs/2026-06-29-activation-script-output-consistency-design.md`.

## Decision

All author-controlled `system.activationScripts` MUST be wrapped with
`inputs.phillipgreenii-nix-base.lib.mkActivationSection` and MUST use its
companion helper functions for all status output.

### Helper API (`lib/activation.nix` in `phillipg-nix-repo-base`)

**`lib.activationHelpers`** — a standalone bash string that defines five logging
functions and two runtime-detection guards. It is exposed separately so it can be
injected into child scripts that run in their own process (e.g. the `beads-dolt`
sudo'd init sub-process).

**`lib.mkActivationSection { tag, headline ? null, body }`** — a pure string
builder that:

1. Re-emits `activationHelpers` (so every fragment is self-sufficient regardless
   of `mkAfter` merge order).
2. Prints a `[tag] headline` header line to stdout.
3. Runs `body`.
4. Prints a trailing blank line.

**Functions available inside `body` (and in any script that includes
`activationHelpers`):**

| Function           | Output                                                     |
| ------------------ | ---------------------------------------------------------- |
| `act_ok "msg"`     | `  ✓ msg` (green when color active)                        |
| `act_warn "msg"`   | `  ⚠ msg` (yellow when color active)                       |
| `act_fail "msg"`   | `  ✗ msg` (red when color active)                          |
| `act_info "msg"`   | `    msg` (plain progress at the message column, no glyph) |
| `act_detail "msg"` | `  msg` (plain note at the glyph column, no glyph)         |

`act_detail` is the glyph-column (2-space) sibling of the message-column (4-space)
`act_info`. Use it for recovery/inspect hints printed directly under an `act_fail`
line, where aligning the hint to the glyph column reads as a note on the failure.

### Normative rules

- Color MUST be emitted when `CLICOLOR_FORCE` is non-empty OR stdout is a TTY
  (`[ -t 1 ]`).
- Color MUST NOT be emitted when `NO_COLOR` is non-empty. `NO_COLOR` MUST take
  precedence over `CLICOLOR_FORCE`.
- Glyphs (`✓`/`⚠`/`✗`) MUST be used when `LC_ALL` or `LC_CTYPE` matches
  `*UTF-8*` or `*utf8*`. Otherwise output MUST degrade to width-padded ASCII
  markers: `[OK]  `, `[WARN] `, `[FAIL] ` (padded to the width of `[WARN]` +
  one trailing space so message text aligns).
- Messages MUST be printed via `printf '%s\n' "  ✓ $msg"` — i.e. the message
  MUST NOT be used as a `printf` format string. This makes messages containing
  `%`, `\`, or `$` safe.
- `act_fail` MUST log only. It MUST NOT call `exit`. The activate script runs
  under `set -e`; a helper that exited would abort all later sections.
- Operational commands inside a retrofitted script MUST remain byte-identical.
  Only the echo/printf status lines change.
- Because nix-darwin shellchecks the assembled activate script, each `act_*`
  function definition in the emitted helper MUST carry
  `# shellcheck disable=SC2329` (function-never-invoked is expected for sections
  that do not call all five helpers).

### `pn` force-color mechanism

`pn workspace apply` wires `cmd.Stdout = io.MultiWriter(&buf, os.Stdout)` — a
pipe, not a PTY. So `[ -t 1 ]` is false inside the activate script and color
would otherwise self-suppress.

`pn` MUST export `CLICOLOR_FORCE=1` into the apply subprocess when `pn`'s own
stdout is a TTY and `NO_COLOR` is unset (gated on the existing
`colorEnabled(os.Stdout)` helper in `apply.go`). This preserves clean behavior
when the operator redirects `pn workspace apply > file`.

### Child-process injection (`beads-dolt` pattern)

Sub-processes that run in their own shell (e.g. `pkgs.writeShellScript` bodies
run via `sudo`) do not inherit bash functions from the parent activation shell.
Such scripts MUST have `${lib.activationHelpers}` injected at the top. The
`sudo` invocation MUST forward `CLICOLOR_FORCE` and `LC_CTYPE` explicitly
(e.g. `sudo -H CLICOLOR_FORCE="${CLICOLOR_FORCE:-}" LC_CTYPE="${LC_CTYPE:-}" -u …`).

### Re-emission is deliberate

Each `mkActivationSection` call re-emits the helper definitions. Because all
fragments share one shell process, the last definition wins — identical
redefinitions are harmless. The `act_` prefix ensures no collision with existing
locals (`state`, `label`, `rc`, …) or framework-defined helpers such as
`launchd-health-check`'s `_check_*` functions. This is preferable to defining
the helpers once via a single `mkBefore` fragment (see Alternatives).

## Consequences

### Positive

- **Structural consistency** — the section shape (grep-friendly tag, colored
  glyphs, trailing blank line) is produced by the helper, not by each author
  remembering a convention.
- **Color on the `pn` path** — `CLICOLOR_FORCE` propagation means color renders
  correctly when `pn workspace apply` is running interactively.
- **Safe degradation** — no color or only ASCII markers render correctly in CI,
  redirected output, or legacy locales.
- **Cosmetic-only retrofit** — operational behavior of each activation script is
  unchanged; existing CI checks remain green.

### Negative

- **Per-section re-emission** — the helper bash (~30 lines) is re-emitted once
  per section in the assembled activate script. This is intentional but increases
  the script size slightly.
- **Ordering caveat** — section ordering across modules is `mkAfter` +
  import-order dependent. `launchd-health-check` calls `exit 1` on failure;
  sections ordered after it are skipped on that failure. The trailing blank line
  is a separator, not a guaranteed sentinel. This is pre-existing behavior.

### Neutral

- Scripts with no current status signal gain a closing `act_ok` so every section
  ends with a clear marker.
- The `lib.activationHelpers` string is also available for injection into child
  scripts; its exposure is a deliberate escape hatch, not an invitation to build
  arbitrary helpers on top of it.

## Alternatives Considered

### Darwin module defining helpers via a single `mkBefore`

A darwin module that emits the helper function definitions once into an early
`postActivation` fragment, with all later sections relying on those definitions
being in scope.

**Rejected.** It depends on `mkBefore` ordering always winning across every
module that imports activation scripts — a fragile assumption. The
`terminalProfile` activation script runs in a separately named script process
and would need its own definitions regardless. Re-emitting per section is
simpler, more robust, and keeps every section self-sufficient.

### Convention + copy-paste, no shared library

Document the style convention and have authors copy the helper bash verbatim
into each script.

**Rejected.** Copy-paste duplicates diverge: one style fix must be applied to
every script by hand. It fails the structural-consistency goal — consistency
becomes dependent on each author remembering to update their copy.
