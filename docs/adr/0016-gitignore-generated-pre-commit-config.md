# Gitignore the git-hooks.nix-generated `.pre-commit-config.yaml`

**Status**: Accepted
**Date**: 2026-07-01
**Deciders**: Phillip Green II

## Context

The shared pre-commit flake-module (`flake-modules/pre-commit.nix`, consumed by
every `nix-*` repo and used by this repo) builds its hook set with
`cachix/git-hooks.nix`. That library's `installationScript` (run on devShell
entry and by `packages.install-pre-commit-hooks`) writes `.pre-commit-config.yaml`
as a **symlink into `/nix/store`** (git mode `120000`, target
`…-pre-commit-config.json`). The file is a pure build artifact; the source of
truth is `flake-modules/pre-commit.nix`.

Historically most repos **committed** this symlink. That is fragile:

- The committed target is a `/nix/store` path. `nix-store --gc` can collect it,
  leaving a **dangling symlink**. `prek` then fails with "config not found",
  and after regeneration with "config not staged" — blocking `git commit`
  entirely. This was observed live in `phillipgreenii-nix-personal` and while
  landing an unrelated change in this repo.
- The committed value is meaningless off the generating machine: a symlink to a
  machine-local store path that will not exist for another clone or in CI (a
  non-Nix contributor gets a broken link). There is no upside to committing it.
- `cachix/git-hooks.nix`'s own installer creates a GC root
  (`nix-store --add-root --indirect --realise`), so a devShell user's working
  symlink is protected without committing it.

`phillipgreenii-nix-overlay` already gitignored the file (its "Chunk 4" hygiene
migration) and never exhibited the problem — the reference for this decision.

## Decision

The git-hooks.nix-generated `.pre-commit-config.yaml` MUST NOT be committed. Every
repo that consumes the shared pre-commit flake-module MUST gitignore it.

### Normative rules

- Each consuming repo MUST list `.pre-commit-config.yaml` (exact, full line) in
  its `.gitignore`.
- The file MUST NOT be tracked in git; a previously-tracked copy MUST be removed
  with `git rm --cached .pre-commit-config.yaml`.
- The working-tree symlink is regenerated on demand by the devShell shellHook or
  `nix run .#install-pre-commit-hooks`; nothing else is required to obtain it.
- The shared flake-module MUST enforce this: it exposes a
  `checks.pre-commit-config-gitignored` derivation that reads the consumer's
  `.gitignore` at eval time and `throw`s (failing `nix flake check`) if the entry
  is absent or the file is missing. This rides each repo's existing CI
  `nix flake check`, so the rule cannot be silently forgotten on a new repo.

## Consequences

### Positive

- The dangling-symlink / "config not staged" failure class is eliminated.
- New consumer repos are caught automatically by the flake check — no per-repo
  discipline required.
- Repos stop carrying a machine-specific store path in version control.

### Negative

- The check reads `.gitignore` at eval time; a consumer with an unusual
  `.gitignore` layout (e.g. a non-exact pattern) will fail the check and must add
  the exact line. This strictness is intentional.

### Neutral

- The commit half of `update-locks-lib.bash`'s `_ul_ensure_pre_commit_hooks`
  (which committed the tracked symlink to keep the clean-tree gate happy) becomes
  dead code post-migration — the guard `git diff --quiet` reports clean for an
  ignored file, so the `git add`/`git commit` branch is never reached. It does
  not break; it is tracked for removal as separate hygiene.
- `modules/pn/internal/workspace/propagate.go` already treats the config as "a
  gitignored dev-shell symlink" (`PREK_ALLOW_NO_CONFIG=1`), so it is consistent
  with this decision with no change.

## Alternatives Considered

### Auto-append the entry to `.gitignore` from the shellHook

Have the shellHook / `install-pre-commit-hooks` idempotently add the entry.
Rejected: silently editing a tracked, version-controlled file on devShell entry
violates least-astonishment, and it is redundant — git-hooks.nix already
GC-roots the symlink for devShell users, and the flake check already enforces
the entry loudly.

### Commit a real (dereferenced) `.pre-commit-config.yaml` instead of the symlink

Generate concrete YAML content and commit it so non-Nix users can run
pre-commit. Rejected: it duplicates the source of truth
(`flake-modules/pre-commit.nix`), drifts on every hook change, and this workspace
has no non-Nix pre-commit consumers.

### Docs only (no enforcement)

An ADR + CLAUDE.md rule with no check. Rejected as the sole mechanism: it relies
on humans remembering on each new repo, which is exactly what failed here. Kept
as documentation _alongside_ the check.

## Related Decisions

- Enforced by `flake-modules/pre-commit.nix` (`checks.pre-commit-config-gitignored`).
- Reference implementation: `phillipgreenii-nix-overlay` `.gitignore`.
