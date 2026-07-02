# OS-aware `{builder}` and explicit terminal-path template variables

**Status**: Accepted
**Date**: 2026-07-02
**Deciders**: Phillip Green II

## Context

`pn workspace build` / `pn workspace apply` expand a command template
(`build_command` / `apply_command` in `pn-workspace.toml`, or a built-in default)
into an argv, then run it in the terminal repo with sibling `--override-input`
flags appended. Historically the vocabulary was two placeholders —
`{terminal_flake}` and `{hostname}` — and the default build command was
`darwin-rebuild build --flake {terminal_flake}`.

Two latent problems:

1. **macOS-only default.** The default hard-codes `darwin-rebuild`, so
   `pn workspace build` on a NixOS host runs the wrong activation tool. For a
   workspace like `homelab` — a NixOS target with a **subdir flake** (`nix/flake.nix`)
   and no `build_command` set — build was already non-functional pre-change: the
   wrong tool AND a repo-root flake path. **No working baseline is regressed by
   this decision.**

2. **`{terminal_flake}` collapsed several distinct facts** (repo dir, flake dir,
   relative path) into one repo-root path, ignoring the flake subdir entirely.
   `checkFollows` compounded this: it read `<repoRoot>/flake.lock`, which does not
   exist for a subdir-flake terminal, so the follows check **silently no-op'd** for
   exactly the workspace shape that needed it.

## Decision

### Template vocabulary (five variables, no TOML schema change)

`{terminal_flake}` is **removed**. Templates expand these placeholders, all
derived from data `pn` already has (`ws.root`, the terminal key + `OverridePaths`,
`resolveFlakePath`, `os.Hostname`, `runtime.GOOS` / `/etc`):

| placeholder                    | value                                                | homelab (subdir flake) | root-flake repo  |
| ------------------------------ | ---------------------------------------------------- | ---------------------- | ---------------- |
| `{terminal_repo_dir}`          | `Join(ws.root, terminal)` (honoring `OverridePaths`) | `/…/homelab`           | `/…/repo`        |
| `{terminal_nix_dir}`           | `Join(repoDir, Dir(flakePath))`                      | `/…/homelab/nix`       | `/…/repo`        |
| `{terminal_nix_relative_path}` | `Dir(resolveFlakePath(terminal))`                    | `nix`                  | `.`              |
| `{hostname}`                   | short `os.Hostname()`                                | `monorepod`            | `monorepod`      |
| `{builder}`                    | OS-detected activation tool (MAY be empty)           | `nixos-rebuild`        | `darwin-rebuild` |

`filepath.Dir("flake.nix") == "."`, so for a root-flake repo
`{terminal_nix_dir} == {terminal_repo_dir}` and `{terminal_nix_relative_path} == "."`.
The `.` is left `filepath`-consistent (NOT coerced to `""`); `{terminal_repo_dir}/{terminal_nix_relative_path}`
yields `/…/repo/.` — valid, if ugly.

The flake reference passed to the builder MUST stay a **plain absolute path**
(not `git+file://`) so working-tree / uncommitted changes are copied into the
build — the whole point of `pn workspace build`/`apply`. `pn-workspace.toml` and
`pn-workspace.lock.json` schemas are untouched.

### OS-aware builder with real NixOS detection

The default build command becomes `{builder} build --flake {terminal_nix_dir}`.
`{builder}` resolves via a pure `detectBuilder(goos, isNixOS)`:

- `darwin` → `darwin-rebuild`
- `linux` **and NixOS** → `nixos-rebuild`
- anything else → `""` (no built-in builder)

`linux` alone is NOT sufficient — real NixOS detection is the correctness crux, so
that foreign-Linux-with-Nix correctly yields `""`. `isNixOSHost(etcDir)` returns
true when `<etcDir>/NIXOS` exists **or** `<etcDir>/os-release` declares `ID=nixos`
(surrounding quotes stripped, so both `ID=nixos` and `ID="nixos"` match). There is
**no PATH probing** — a stray `nixos-rebuild` binary on a non-NixOS host MUST NOT
trigger detection. `etcDir` is injectable for tests.

`{builder}` is defined ONLY for the symmetric `nixos-rebuild` / `darwin-rebuild`
pair. `home-manager` (`homeConfigurations.<user@host>`) and `system-manager`
(`systemConfigs`) do not fit this shape and stay fully explicit; `{builder}` MUST
NOT later be overloaded to mean home-manager (that wants a multi-step apply model
— an explicit non-goal here).

### Loud-fail policy

`substituteCommand(tmpl, templateVars) ([]string, error)` fails loudly when:

1. **`{builder}` is referenced but empty for this OS** →
   `no built-in builder for this OS (GOOS=<goos>); set build_command/apply_command explicitly in pn-workspace.toml`.
2. **An unknown placeholder survives** — any `{lowercase_token}` that is not one
   of the five known names. This turns a lingering `{terminal_flake}` (in an
   in-repo fixture or an external consumer's committed toml beyond grep reach)
   into a loud error — the intended coordinated-migration signal (pin is the
   version). The same NAME check also runs at **parse time** in `ParseConfig` for
   `build_command` / `apply_command`, so a stale placeholder fails at config-load,
   not only at build/apply. (Emptiness of `{builder}` is host-dependent and stays
   a run-time guard.)
3. **The template expands to an empty command** (folds in the former
   `len(cmdArgs)==0` check).

**`$`-awareness.** The unknown-placeholder scan is `$`-aware: a `{token}`
immediately preceded by `$` is a shell variable (e.g. `${home}`) and is left
untouched, not rejected. Go's `regexp` has no lookbehind, so tokens are matched
with `\{[a-z_]+\}` and the preceding byte is inspected directly (skip when it is
`$`). Uppercase `${VAR}` is already safe (the token regex requires lowercase), and
`{}` / `{a,b}` brace expansions do not match.

### `#{hostname}` omission from the default (boundary)

The default build command omits `#{hostname}`: `nixos-rebuild build` /
`darwin-rebuild build` default to the current host's config attr. **Boundary:** the
default is correct only when the flake's config attr name equals the hostname. A
machine whose attr differs MUST set `build_command` explicitly. This is the
pre-existing `{hostname}` limitation, now documented as the default's boundary.

### `checkFollows` re-wired to the nix dir (behavior change)

`checkFollows` now reads `<terminalNixDir>/flake.lock` instead of
`<repoRoot>/flake.lock`. **Behavior change:** subdir-flake terminals that were
silently skipping follows validation now actually run it — real follows problems
may surface. That is the point.

### Dry-run on a builderless OS

`substituteCommand` runs **before** the `ShowNixCommandsOnly` branch. With the
default template and `{builder} == ""`, dry-run returns the builder-empty error
rather than printing. **Decision:** keep the error — honest ("there is no command
to show on this OS"), with the actionable message above.

### Builder seam: `Options.Builder`

`BuildOptions.Builder` / `ApplyOptions.Builder` (string) override the detected
value, defaulting to `defaultBuilder()` when empty. This feeds
`templateVars.Builder` for both the default template and any explicit template
containing `{builder}`. `Upgrade` builds `ApplyOptions` without `Builder`, so it
falls through to detection (correct; unchanged).

**Trade-off:** a `Workspace`-struct field is arguably cleaner for a host-level
fact, but it changes construction across every call site. Per-call `Options` is the
lower-blast-radius pick: it mirrors the existing inject-the-dependency seam
(`Open(root, runner)`), is a genuine user escape hatch (mis-detected host, remote
builder), and — unlike a package var — cleanly keeps the dry-run tests
deterministic across host OSes.

## Consequences

### Positive

- `pn workspace build` produces the OS-appropriate default (macOS →
  `darwin-rebuild`, NixOS → `nixos-rebuild`).
- Subdir-flake terminals get correct flake paths AND real follows validation.
- Template authors have an unambiguous vocabulary; typos and the removed
  `{terminal_flake}` fail loudly (at parse time where possible).

### Negative / Neutral

- Foreign hosts (non-NixOS Linux, etc.) get no default build — build fails with an
  actionable message directing the author to set `build_command`. Intended.
- Subdir-flake terminals may now surface previously-hidden follows violations.
- A shell variable colliding by name with a known placeholder (e.g.
  `${terminal_nix_dir}`) would still be substituted — an accepted edge case; the
  `$`-awareness guarantee is only that legitimate shell vars are not _rejected_.

## Residual / out of scope

- **Sibling flake in a subdir:** `overrideInputArgsFor` emits
  `git+file://<sibling_repo_root>` regardless of the sibling's flake location. A
  sibling with a subdir flake is already mis-overridden today; this decision does
  not change or fix that. Track separately if it bites.
- **`nh` frontend** not adopted; a possible future workspace-level opt-in for
  `{builder}` is a follow-up.

## Related Decisions

- Amends [0002](0002-pn-workspace-toml-schema.md) (`pn-workspace.toml` command
  templates) — vocabulary only; no schema field added.
