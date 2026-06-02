# pn workspace build & apply — Go port — Design

**Status:** Draft — pending implementation plan
**Date:** 2026-06-01
**Repos affected:** `phillipg-nix-repo-base` (`modules/pn`)

## Problem

The Go `pn` rewrite left `workspace build` and `workspace apply` as placeholders:
both run `nix fmt` + `nix build .` in **every** repo (`build.go` / `apply.go`),
with explicit TODOs. They do not select the terminal flake, honor the
`build_command` / `apply_command` templates, run a real `darwin-rebuild`, gate
on whether anything changed, diff the resulting profile, or handle interrupts.

The authoritative behavior is the deleted bash (`d463549^`):
`pn-workspace-build.sh`, `pn-workspace-apply.sh`, plus the helpers in
`pn-lib.bash` (`workspace_resolve_root`, `workspace_parse_overrides`,
`workspace_get_projects`, `workspace_check_follows`, `workspace_read_toml`) and
`lib/scripts/update-cache-lib.bash` (`ul_*`).

A prerequisite — restoring per-repo `input-name` so `--override-input` uses the
real flake input name — already landed (see
[2026-06-01 input-name fix](#prerequisite-already-landed)).

## Goals

- `pn workspace build` and `pn workspace apply` build/activate **only the
  terminal flake**, with every non-terminal repo injected as a local override.
- Full-fidelity parity with the old bash, including: command templates,
  `--override-path`, `--show-nix-commands-only`, `check_follows` validation,
  nix-daemon health check, rebuild-skip gate, `nvd` profile diff, and
  interrupt-safe `sudo` apply.
- Declarative config: the terminal and the input names come from
  `pn-workspace.toml`, not from `nix eval` discovery.

## Non-goals

- Re-porting the topological-sort / `nix eval` discovery from
  `pn-discover-workspace.sh`: superseded by declarative `terminal` +
  `input-name`.
- Fixing the unrelated `pn workspace nix` stdout-swallowing
  (`exec.go` buffers output). Tracked separately.
- `use_lock` toggle: overrides are computed from config + on-disk clones, so the
  lock is not consulted for override construction.

## Decisions

1. **Terminal identification:** explicit `[workspace] terminal = "<repo-key>"`.
   Keeps "`input-name` optional, defaults to directory name" intact.
2. **Override URL scheme:** `git+file://<clone>` (old behavior) — respects
   `.gitignore`, picks up uncommitted edits to tracked files, never drags
   `result` symlinks / `.git` into the store. The shared override helper and its
   test switch from `path:` to `git+file://`.
3. **Rebuild-skip gate:** dirty-aware. Skip the rebuild only when every repo's
   `HEAD` matches the last applied hash **and** no repo has a dirty working tree
   (`git status --porcelain` empty). `--force` / `NIX_UL_FORCE_UPDATE=true`
   always rebuilds.
4. **Scope:** full fidelity (see Components).

## Config additions (`[workspace]`)

| key             | build                                                             | apply                      |
| --------------- | ----------------------------------------------------------------- | -------------------------- |
| `terminal`      | required                                                          | required                   |
| `build_command` | optional; default `darwin-rebuild build --flake {terminal_flake}` | —                          |
| `apply_command` | —                                                                 | required (error if absent) |

Templates expand `{terminal_flake}` (absolute terminal clone path) and
`{hostname}` (short hostname, `hostname -s`). Target workspace values:

```toml
[workspace]
terminal = "phillipg-nix-ziprecruiter"
build_command = "darwin-rebuild build --flake {terminal_flake}"
apply_command = "sudo darwin-rebuild switch --flake {terminal_flake}#{hostname}"
```

## Architecture

Resolve root → load config → resolve terminal → compute overrides (config-driven)
→ `check_follows` → substitute template → `cd` terminal → `nix fmt` → run
command + overrides. `apply` wraps the run with daemon-check, rebuild-gate,
signal-forwarding, profile diff, and mark-applied.

## Components (all in `internal/workspace` unless noted)

### `config.go` (modify)

Add to `WorkspaceSection`: `Terminal`, `BuildCommand`, `ApplyCommand`
(`toml:"...,omitempty"`). `ParseConfig` validates that `terminal`, when set,
names a declared repo. New accessors: `TerminalRepo()`, `BuildCommandOrDefault()`,
`ApplyCommandOrError()`.

### `helpers.go` (modify) — unified override builder

Replace `computeOverrideArgs` with
`overrideInputArgs(opts overrideOpts) []string`, driven by **config repos** (not
the lock): for each declared repo — apply `--override-path` swap → skip if the
clone dir is missing → skip the terminal when `opts.ExcludeTerminal` → emit
`--override-input <InputNameFor(key)> git+file://<abs-path>`, sorted by repo key.
`NixCommand` calls it with `ExcludeTerminal:false`; build/apply with
`ExcludeTerminal:true` + the parsed override paths.

### `overridepaths.go` (new)

`parseOverridePaths(specs []string) (map[string]string, error)` — ports
`workspace_parse_overrides`: env `PN_WORKSPACE_OVERRIDE_PATHS` (comma-separated,
lower precedence) then `--override-path name=path` flags (higher precedence);
`name=path` shape, absolute-path required, trimmed.

### `template.go` (new)

`substituteCommand(tmpl, terminalFlake, hostname string) []string` — replace
placeholders, split on whitespace into argv. `shortHostname()` helper
(`os.Hostname()` truncated at first dot).

### `follows.go` (new)

`checkFollows(terminalDir string, inputNames []string) error` — port
`workspace_check_follows`: read `<terminalDir>/flake.lock` (absent → ok); for
each workspace input that is a direct root dep, verify every _other_ workspace
input is a `follows` (array) and not an unfollowed copy (string); on violation
return an error naming the pair plus the
`inputs.<a>.inputs.<b>.follows = "<b>"` fix hint.

### `updatecache.go` (new)

Go port of the `ul_*` apply integration, sharing the bash state layout
(`${XDG_STATE_HOME:-~/.local/state}/zn-self-upgrade/apply/applied-hash/<repo>`):

- `checkNixDaemon(runner)` — `timeout 10 nix eval --expr true`; on timeout, if a
  TTY, offer `sudo launchctl kickstart -k system/org.nixos.nix-daemon` then
  re-probe; else actionable error.
- `needsRebuild(repos, force)` — `force` ⇒ true; true if any repo's `HEAD` ≠
  stored hash, **or** any repo dirty (`git status --porcelain` non-empty), or no
  stored hash; else false (prints "Skipping rebuild").
- `markApplied(repos)` — write each repo's `HEAD` to its hash file.

### `build.go` (rewrite)

Resolve terminal → overrides (exclude terminal) → `check_follows` → template
(`--build-cmd` > `build_command` > default) → `--show-nix-commands-only` prints
`cd … && nix fmt` and the build argv, else `nix fmt` in terminal dir + run
build argv + overrides.

### `apply.go` (rewrite)

As build, but: `apply_command` required (`--apply-cmd` > toml, else error);
`checkNixDaemon` (unless dry-run); after `nix fmt`, gate on
`needsRebuild(allRepos, force)`; capture `/nix/var/nix/profiles/system` symlink
before/after; run the apply argv + overrides **as a signal-forwarded child**
(own process group; on SIGINT/SIGTERM, `sudo kill -TERM` the group, wait, exit
`128+signo`); if the profile changed and `nvd` is on PATH, `nvd diff old new`;
`markApplied(allRepos)`.

### `cli/workspace.go` (modify)

- `openWorkspace` → resolve root: `--root` > `PN_WORKSPACE_ROOT` > walk up from
  cwd for `pn-workspace.toml`.
- `build`: `--root`, `--build-cmd`, `--override-path` (repeatable),
  `--show-nix-commands-only`.
- `apply`: the above with `--apply-cmd`, plus `--force`.

## Override scheme migration

`computeOverrideArgs` → `overrideInputArgs`; `path:` → `git+file://`. Existing
`nix_test.go` / `build_test.go` / `apply_test.go` expectations update to
`git+file://` and to the config-driven (terminal-excluded for build/apply) set.

## Test strategy

`FakeRunner` asserts exact argv for `nix fmt`, the build/apply command, and
git/nix probes. Unit tests per component:

- override builder: terminal excluded, missing-clone skipped, `--override-path`
  swap, `git+file://`, alphabetical.
- template: both placeholders, multi-word, missing placeholder.
- override paths: env+flag precedence, absolute-path + shape errors.
- follows: missing lock (ok), unfollowed copy (error + hint), proper follows (ok).
- updatecache: HEAD-match skip, HEAD-changed rebuild, dirty-tree rebuild, force,
  mark/needs round-trip (temp `XDG_STATE_HOME`).
- build/apply: terminal selection, default vs toml vs flag command,
  `--show-nix-commands-only`, apply missing-`apply_command` error, profile-diff
  only when `nvd` present and profile changed, rebuild-skip path.
- daemon check timeout handling (fake runner returns timeout).

`nvd`-absent, non-TTY, and `sudo` are guarded so tests don't shell out for real.

## Migration

Add `terminal` / `build_command` / `apply_command` to the workspace
`pn-workspace.toml`. Rebuild `pn` (`nix build .#pn` or apply the darwin config
with nix-repo-base overridden local). No lock changes.

## Prerequisite (already landed)

Per-repo optional `input-name` (defaults to the directory name) on `RepoConfig`,
resolved in the override builder so `--override-input` uses the real flake input
name (e.g. dir `phillipg-nix-repo-base` → input `phillipgreenii-nix-base`).

## Open items

- Signal forwarding through `sudo` in Go: child started in its own process
  group; verify `sudo kill -TERM <pgid>` reaps `darwin-rebuild`. May need
  `os/signal` + `syscall.SysProcAttr{Setpgid:true}`; covered by manual test
  since it is hard to unit-test deterministically.
