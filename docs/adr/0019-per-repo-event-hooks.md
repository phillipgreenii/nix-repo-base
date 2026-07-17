# ADR-0019: Event-based workspace + per-repo hooks with override-aware `{nix_run}`

**Date:** 2026-07-07
**Status:** Accepted (supersedes the hooks decision in [ADR-0002](0002-pn-workspace-toml-schema.md); amends [ADR-0017](0017-pn-workspace-toml-enforce-entrypoint.md))
**Deciders:** phillipgreenii

## Context

ADR-0002 defined hooks as `[hooks.<command>]` tables with `pre`/`post` arrays, run once at the
workspace root. That model could not express the recurring need behind bd **pg2-5yq5**: after a
repo's checkout or flake inputs change (clone/rebase/update), its git pre-commit gate — a
git-hooks.nix `/nix/store` symlink pinned at install time (ADR-0016) — goes **stale** and silently
runs an outdated gate (e.g. missing a newly-added gofumpt). Resyncing it requires running
`nix run .#install-pre-commit-hooks` **in that repo**, and — critically — with the workspace's
`--override-input` overlays so the gate rebuilds against the **local** sibling clones rather than
locked inputs. The old root-scoped, command-keyed hook model had no per-repo scope and no way to
inject overrides.

A prior attempt added a first-class `pn workspace install-hooks` subcommand plus a per-repo
`install-hooks` opt-in list (bd pg2-ic7x). That was rejected: a workspace command shelling out to
itself is a smell, the subcommand did not inject overrides (so it built against locked inputs — the
very bug), and a separate participation list duplicated information.

## Decision

- **Hooks are lists of `{ when, run }`**, at two scopes:
  - `[[hooks]]` — **workspace-scoped**; each entry runs once at the workspace root.
  - `[[repos.<key>.hooks]]` — **per-repo**; each entry runs in that repo (`cwd=<repo>`).
- **`when`** is a list of **events** `<phase>-<command>` where phase ∈ {`pre`, `post`} and command is
  any hookable pn-workspace command (clone, rebase, update, status, flake-check, format, push,
  pre-commit-check, build, apply, upgrade, lock, init, tree). An unknown event MUST be a config-load
  error.
- **Firing:** when command `C` runs, workspace `[[hooks]]` whose `when` contains `pre-C`/`post-C` run
  once at the root; for each repo `C` _processes_ (repo-iterating commands and `upgrade` → all repos;
  `build`/`apply` → the terminal only), that repo's per-repo hooks whose `when` contains `pre-C`/`post-C`
  run in the repo. `pre-*` failures abort the command; `post-*` failures warn and continue.
- **`{nix_run <attr>}` token** (valid ONLY in per-repo `run`) expands to
  `nix run <--override-input …> '<repo-flake-dir>#<attr>'`, single-quoted, with overrides resolved
  from the **effective lock** (`effectiveLock`, derived when the disk lock is absent/stale — never the
  possibly-empty raw lock). A `{nix_run}` token in a workspace hook, or more than one per `run` entry,
  MUST be a config-load error. **cwd is load-bearing:** `install-pre-commit-hooks` installs into `$PWD`,
  so per-repo hooks always run with `cwd` = the repo whose flakeref is used.
- **Path resolution / failure semantics** are otherwise as ADR-0002 (first token `/foo` absolute,
  `./foo` file-relative, bare name PATH lookup; via `sh -c`).
- The `pn workspace install-hooks` subcommand and the per-repo `install-hooks` field are **removed**.
- `pn workspace workforest add`/`add-repo` fire the `post-clone` event on each new worktree via a
  **set-rooted** Workspace, so overrides resolve to the set's worktrees (P1-safe).
- `doctor` verifies that a per-repo `{nix_run <attr>}` names a real flake output and that a per-repo
  hook can actually fire, and warns otherwise (advisory `SevWarning`; runtime failure is the
  backstop). _(Shipped in bd pg2-uswb: `pn workspace doctor` emits `hook-nix-run-output` — probed via
  `nix eval`, swallowed-as-absent, skipped under `--offline` — and `hook-never-fires`. Load-time
  validation already hard-errors unknown build/apply placeholders and malformed `{nix_run}` tokens.)_

### Amendment to ADR-0017 (enforce semantics)

`EnforceKeys` / `pn-workspace-toml-enforce` previously set `[hooks.apply].post` to **exactly**
`[<applyPost>]`. Under the list schema it now uses **ensure-present** semantics: it guarantees some
`[[hooks]]` entry whose `when` contains `post-apply` has `<applyPost>` in its `run` (appending to the
first such entry, or creating a dedicated one), and no longer clobbers other `run` commands a user
added to a post-apply hook. The binary's flags are unchanged, so the nix `home.activation` wiring is
untouched.

The enforced gate entry's `when` also covers **`post-upgrade`** (bd pg2-vn2k). `pn workspace
upgrade` calls `Update` + `Apply` directly (`upgrade.go`), so its inner apply phase does **not**
emit `post-apply` — only the outer `post-upgrade` fires. Enforcing the gate under both events keeps
`pn workspace upgrade` gated (the operator who upgrades instead of update+apply still runs
`<applyPost>`), with no double-firing of per-repo `post-update` hooks. `EnforceKeys` ensures both
events on the gate entry (create-with-both / add-`post-upgrade`-if-missing); it is idempotent.

## Consequences

- The homogeneous "resync all repos after clone/rebase/update" case is expressed as one
  `[[repos.<key>.hooks]]` entry per repo with `when = ['post-clone','post-rebase','post-update']`
  (duplication across repos is accepted; a shared/category-event model was considered and deferred).
- **Breaking config change:** old `[hooks.<command>]` tables do not parse into the new `[]RepoHook`
  (go-toml/v2 errors "cannot store a table in a slice"), and an old binary errors on `[[hooks]]`.
  Because `pn-workspace-toml-enforce` runs during `home.activation`, the deployed binary and the
  committed `pn-workspace.toml` hook shape MUST switch together (one coordinated apply). Migration:
  `[hooks.apply] post=['pb gate check']` → `[[hooks]] when=['post-apply'] run=['pb gate check']`.
- The override-injection fix means a local (unpushed) change to a shared repo (e.g. repo-base's
  treefmt/gofumpt config) is reflected in every consumer's gate on the next clone/rebase/update.

Reference: bd pg2-5yq5 (supersedes pg2-ic7x). Implementation plan:
`docs/superpowers/plans/2026-07-07-overlay-aware-nix-hooks.md`.

## Amendment: hook trust gate (bd pg2-oymai)

Event hooks execute `sh -c` from a `pn-workspace.toml` that `resolveWorkspaceRoot`
discovers by walking up from the cwd, with no ownership/trust check. Merely `cd`-ing
into an attacker-controlled checkout that declares e.g. `[[hooks]] when=['pre-status']`
therefore ran arbitrary code on the next hookable `pn workspace` command. This is closed
with a **direnv-style trust-on-first-use (TOFU) allowlist**:

- **Guard at the single chokepoint.** `RunEventHooks` (the only path that executes hooks)
  MUST call `trust.EnsureAllowed(root)` — but ONLY when a hook actually fires for the
  `(phase, command)` (fire-precise gating; hook-free commands incur no friction). Pre-phase:
  the command aborts (nothing executed). Post-phase: a warning is emitted and hooks are
  skipped (the command result is never masked).
- **Content-hash TOFU.** The allow record (`${XDG_STATE_HOME:-~/.local/state}/pn/trust/`,
  keyed by the SHA-256 of the absolute root, dir `0o700` / file `0o600`) stores the SHA-256
  of `pn-workspace.toml`. Editing the TOML re-blocks until re-allowed. `pn workspace allow`
  echoes the declared hook `run` lines for review, then records trust; `pn workspace deny`
  revokes it.
- **`--root` / `PN_WORKSPACE_ROOT` do NOT bypass** the gate (an untrusted directory can also
  plant an env var). One `pn workspace allow` per workspace is required regardless.
- **Trust propagation, never conjuring.** `WorkforestAdd`/`WorkforestAddRepo` (via
  `installSetHooks`) propagate trust to the pn-authored set root ONLY when the canonical root
  is already trusted; an untrusted canonical's derived set stays untrusted (its post-clone
  hooks warn-skip).
- **One-time migration.** Existing hook-bearing workspaces — including the operator's own,
  whose enforced `post-apply`/`post-upgrade` gate runs during `pn workspace apply`/`upgrade`
  — MUST run `pn workspace allow` once after the new `pn` is deployed.
- **Residual TOCTOU:** the guard hashes the TOML, then `RunHooks` re-reads/executes; a swap
  between check and exec requires local write access to the root (already full compromise).
- **Residual, now closed (bd pg2-x2q6o):** `build_command` / `apply_command` are also arbitrary
  commands taken from the discovered `pn-workspace.toml`, executed as argv at
  `build.go`/`apply.go`; they do NOT pass through `RunEventHooks`. They MUST pass the same
  `trust.EnsureAllowed(root)` check before execution: `Build` and `Apply` abort pre-exec when the
  root is untrusted (`Apply` before even the nix-daemon probe). The `ShowNixCommandsOnly` dry-run
  is exempt — it only prints the resolved argv and never executes. `--root` / `PN_WORKSPACE_ROOT`
  do NOT bypass it, matching the hook gate.

Reference: bd pg2-oymai; residual closed by bd pg2-x2q6o (RFC 2119 language MUST/SHOULD as above).

## Amendment: forward-compat of unknown hook events — RATIFIED hard-error (bd pg2-mbi5)

`when` events are validated at config load (`validateAllHooks` → `splitEvent`,
`internal/workspace/nix_hooks.go`; command table at `nix_hooks.go:17`), and per the Decision above
an unknown event is a **hard config-load error**. Because `ParseConfig` runs on **every**
`pn workspace` subcommand — including the `apply` that deploys a newer `pn` — forward-declaring
`[[hooks]] when=['<phase>-<newcmd>']` for a command only a _newer_ `pn` will add **bricks the
current `pn` and blocks the very apply that would upgrade it** (a version deadlock, bd pg2-mbi5).

**Decision (ratified 2026-07-17): keep the hard-error; do NOT warn+skip unknown events.** The
warn+skip alternative was rejected because it would reintroduce a **silent-gate-failure** mode in
the per-repo resync hooks (`when=['post-clone','post-rebase','post-update']` →
`{nix_run install-pre-commit-hooks}`): a command-name typo (e.g. `post-rebse`) would warn+skip and
that repo's git gate would silently go stale — exactly the pg2-5yq5 class this ADR exists to fix —
and those per-repo hooks have **no** backstop. (The enforced `post-apply` gate is _not_ the risk
here: it **self-heals** — `EnforceKeys` re-creates a correct `post-apply`/`post-upgrade` gate entry
whenever no matching one is found, `internal/workspace/enforce_keys.go:99-102`, on every
activation.) Trading loud detection for one saved commit on future adoptions is a poor trade in a
single-operator workspace whose recurring pain is silent gates. Warn+skip (Option A) / hybrid
(Option C) remain available as a follow-up **iff** forward-declaration friction is later
demonstrated; see `docs/superpowers/specs/2026-07-15-pn-unknown-hook-event-forward-compat-design.md`.

### Runbook: adopting a new hookable command (the two-step)

When a newer `pn` adds a hookable command and you want a hook for it, do it in **two applies** —
never forward-declare against a `pn` that does not yet know the command:

1. **Deploy the new `pn` first, without the new hook.** Land + `pn workspace apply` the new `pn`
   version in a commit that does **not** add the `[[hooks]] when=['<phase>-<newcmd>']` entry. This
   installs a `pn` whose `hookableCommands` (`nix_hooks.go`) includes `<newcmd>`.
2. **Then add the hook.** In a second commit, add the `[[hooks]] when=['<phase>-<newcmd>']` entry
   and `pn workspace apply` again. Editing the TOML re-arms the TOFU trust gate, so run
   `pn workspace allow` once (see the trust-gate amendment above).

**MUST NOT** forward-declare a `when` event for a command the _currently deployed_ `pn` does not
list in `hookableCommands` — config load hard-fails on **every** subcommand, including the `apply`
that would deploy the fix.

**Recovery if already wedged:** remove the offending `when` event from `pn-workspace.toml`, run
`pn workspace apply` to deploy the new `pn`, then re-add the event and `apply` again.

Reference: bd pg2-mbi5.
