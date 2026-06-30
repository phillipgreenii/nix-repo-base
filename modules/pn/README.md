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
$ pn workspace doctor --fix --dry-run
workspace doctor — primary checkouts (origin/<branch> is the baseline)

=== dep ===
  ERROR branch-current        repo "dep" is not on its default branch "main" (on "feature") [manual]
          ↳ git -C /workspace/dep switch main
  WARN  repos-extra            git repo "stray" is on disk but not in pn-workspace.toml [fixable]

=== lib ===
  ERROR flake-lock-fresh      flake.lock input "dep" (→ "dep") pins abc1234 but "dep" is at def5678 [would fix]
  SKIP  branch-synced         remote comparison skipped [—]

workspace doctor: 1 errors, 1 warnings.
```
