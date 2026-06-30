# pn — workspace flake orchestration

`pn` is a Go program that bootstraps and maintains a `pn-workspace.toml` project, coordinating nix flake dependencies across multiple local git repositories.

## pn workspace doctor

`pn workspace doctor` audits a pn workspace against the build-equality invariant and (with `--fix`) repairs the safe drifts.

**The Invariant (one sentence):** If `doctor` reports no errors, a local override build (`--override-input git+file://<clone>` for each workspace dependency) and a pure-remote build (a plain nix build that uses each repo's committed `flake.lock`, no local overrides) produce the same output.

### Modes

- **Primary mode**: Compares local checkouts against the remote default branch (obtained via `git ls-remote` on the canonical URL).
- **Worktree mode**: Relaxes to branch-name uniformity across the set and drops remote checks; dirty trees become warnings instead of errors.

### Flags

- `--fix` — Apply safe, auto-fixable repairs (respects dependency order).
- `--dry-run` — Print the fix plan without applying changes (requires `--fix`).
- `--offline` — Skip remote-dependent checks; reported as skipped, never silently ok.
- `--json` — Machine-readable output (on stdout only; no banner or progress).
- `--strict` — Treat warnings as errors for the exit code.

### Exit Codes

- `0` — No errors (and, under `--strict`, no warnings). Local and remote builds will match.
- `1` — Errors present (or any finding under `--strict`).
- `2` — Doctor itself failed (e.g., `ls-remote` unreachable without `--offline`).

### Important Note: flake-lock-fresh Fix

The `flake-lock-fresh` fix delegates to `pn workspace update`, which is the only fix that pushes. It relocks affected repos, commits the new lock, and pushes to remote — the one auto-fix that modifies remote state. It is always gated behind `--fix` and shown in `--dry-run`.

### Example: Clean Run

```
$ pn workspace doctor
workspace doctor — primary checkouts (origin/main is the baseline)

=== workspace ===

=== phillipg-nix-repo-base ===

=== phillipgreenii-nix-support-apps ===

workspace doctor: no errors (0 warnings). local and remote builds will match.
```

### Example: Run with Findings

```
$ pn workspace doctor
workspace doctor — primary checkouts (origin/main is the baseline)

=== workspace ===
ERROR toml-valid [manual] Workspace config is malformed — cannot parse pn-workspace.toml

=== phillipg-nix-repo-base ===
ERROR branch-current [fixable] Expected branch 'main', found 'feature-x'
ERROR tree-clean [manual] 2 tracked files modified — local build will differ from remote. Commit or stash:  git -C /path/to/repo stash
ERROR flake-lock-fresh [would fix] flake.lock pin for nixpkgs (1.2.3) ≠ workspace target (1.2.4)
ERROR branch-synced [fixable] local main behind origin/main (behind 3); will pull

=== phillipgreenii-nix-support-apps ===
WARN repos-extra [fixable] nix-support-lib present on disk but not in pn-workspace.toml
WARN lock-present [fixable] pn-workspace.lock.json missing; will derive
SKIP branch-synced   <repo>   remote comparison skipped (--offline)   [—]

workspace doctor: 5 errors (2 warnings). Fix these to make builds consistent.
```
